package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// noisePatterns are the statements QRY01 leaves out of the costliest queries besides kakapo's own
// (docs/phase3f-contract.md, item 2): reads of the system views, index and statistics maintenance,
// the statistics sampling SQL Server runs itself, DBCC, and the maintenance plans' parameters.
// Source: DarlingData sp_QueryStoreCleanup/sp_QueryStoreCleanup.sql 647-664 (MIT)
var noisePatterns = []string{"%FROM sys.%", "ALTER INDEX%", "UPDATE STATISTICS%", "%SELECT StatMan%", "DBCC%", "(@[_]msparam%"}

// noiseFilter is the SQL predicate that keeps a statement text out of QRY01 when it carries
// kakapo's tag or matches a noise pattern; the prefix patterns are matched after the leading
// spaces.
func noiseFilter(column string) string {
	text := "ISNULL(" + column + ", N'')"
	parts := []string{text + " NOT LIKE N'%" + KakapoTag + "%'"}
	for _, p := range noisePatterns {
		parts = append(parts, "LTRIM("+text+") NOT LIKE N'"+p+"'")
	}
	return strings.Join(parts, "\n          AND ")
}

// benignValues is the scan's benign wait list as a VALUES list for @benign.
func benignValues() string {
	vals := make([]string, 0, len(domain.BenignWaits))
	for _, w := range domain.BenignWaits {
		vals = append(vals, "(N'"+strings.ReplaceAll(w, "'", "''")+"')")
	}
	return strings.Join(vals, ", ")
}

// performanceInstanceQuery reads what the Performance card needs from the instance, in one batch
// (docs/phase3e-contract.md, "Reads"): the waits since the restart without the benign ones, the
// deadlock and compile counters, the head blockers, the transactions open in this database and the
// sessions sleeping with one anywhere, the schedulers and worker threads, the memory grants and
// their timeouts (PRS02), the load per database (SRV04), this database's missing index suggestions,
// the CPU history of the last four hours (WRK08) and the database's READ COMMITTED SNAPSHOT setting.
// Every area is read in its own TRY, so a missing permission gives that area's error column, not a
// failed read. SELECTs only. The head blockers (ACT01), the schedulers and the memory grants (PRS01)
// are the live fragment the load strip runs too (live.go, docs/phase5i-contract.md). Result sets: one
// row of facts (the live columns last), then the waits, the open transactions, the idle sessions with
// a transaction, the load per database, the missing indexes, the CPU history, and the live fragment's
// two: the head blockers and the largest memory grants held now.
//
// Source: DarlingData sp_PressureDetector/sp_PressureDetector.sql 3930-3960 (MIT): the scheduler
// monitor ring buffer XPaths and the other-processes arithmetic of WRK08.
var performanceInstanceQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
DECLARE @found bit = 0, @state nvarchar(60), @dbid int, @rcsi bit = 0;
SELECT @found = 1, @state = d.state_desc, @dbid = d.database_id, @rcsi = d.is_read_committed_snapshot_on
FROM sys.databases AS d WHERE d.name = @db;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';

DECLARE @started datetime;
IF @vss = 1
    SELECT @started = sqlserver_start_time FROM sys.dm_os_sys_info;
IF @started IS NULL
    SELECT @started = create_date FROM sys.databases WHERE database_id = 2;

/* WRK01, WRK02, WRK05 Waits since the restart, the scan's benign list and SLEEP_% left out */
DECLARE @benign TABLE (wait_type nvarchar(60) PRIMARY KEY);
INSERT @benign (wait_type) VALUES ` + benignValues() + `;
DECLARE @waits TABLE (wait_type nvarchar(60) NOT NULL, wait_ms bigint NOT NULL, signal_ms bigint NOT NULL, tasks bigint NOT NULL, max_ms bigint NOT NULL);
DECLARE @waitsError nvarchar(2048);
IF @vss = 0 SET @waitsError = @noVss;
ELSE
BEGIN TRY
    INSERT @waits (wait_type, wait_ms, signal_ms, tasks, max_ms)
    SELECT ws.wait_type, ws.wait_time_ms, ws.signal_wait_time_ms, ws.waiting_tasks_count, ws.max_wait_time_ms
    FROM sys.dm_os_wait_stats ws
    WHERE ws.wait_time_ms > 0
      AND ws.wait_type NOT IN (SELECT b.wait_type FROM @benign b)
      AND ws.wait_type NOT LIKE N'SLEEP[_]%';
END TRY
BEGIN CATCH
    SET @waitsError = ERROR_MESSAGE();
END CATCH;

/* WRK06, SRV07 Cumulative performance counters */
DECLARE @deadlocks bigint, @batches bigint, @compiles bigint, @recompiles bigint, @countersError nvarchar(2048);
IF @vss = 0 SET @countersError = @noVss;
ELSE
BEGIN TRY
    SELECT @deadlocks  = MAX(CASE WHEN object_name LIKE N'%:Locks%' AND counter_name = N'Number of Deadlocks/sec' AND instance_name = N'_Total' THEN cntr_value END),
           @batches    = MAX(CASE WHEN object_name LIKE N'%:SQL Statistics%' AND counter_name = N'Batch Requests/sec'      THEN cntr_value END),
           @compiles   = MAX(CASE WHEN object_name LIKE N'%:SQL Statistics%' AND counter_name = N'SQL Compilations/sec'    THEN cntr_value END),
           @recompiles = MAX(CASE WHEN object_name LIKE N'%:SQL Statistics%' AND counter_name = N'SQL Re-Compilations/sec' THEN cntr_value END)
    FROM sys.dm_os_performance_counters
    WHERE object_name LIKE N'%:Locks%' OR object_name LIKE N'%:SQL Statistics%';
END TRY
BEGIN CATCH
    SET @countersError = ERROR_MESSAGE();
END CATCH;

` + openMinutesDeclaration + openTransactionsFragment + `
/* WRK07 sessions sleeping with a transaction open more than 5 minutes, instance-wide */
DECLARE @idle TABLE (session_id int NOT NULL, login_name nvarchar(128) NULL, host_name nvarchar(128) NULL, program_name nvarchar(128) NULL,
                     minutes int NOT NULL, implicit bit NOT NULL, began datetime NULL);
IF @transError IS NULL
BEGIN TRY
    INSERT @idle (session_id, login_name, host_name, program_name, minutes, implicit, began)
    SELECT TOP (200) s.session_id, s.login_name, s.host_name, s.program_name,
           DATEDIFF(MINUTE, tat.transaction_begin_time, GETDATE()),
           CASE WHEN tat.name = N'implicit_transaction' THEN 1 ELSE 0 END, tat.transaction_begin_time
    FROM sys.dm_tran_session_transactions tst
    JOIN sys.dm_tran_active_transactions tat ON tat.transaction_id = tst.transaction_id
    JOIN sys.dm_exec_sessions s ON s.session_id = tst.session_id
    WHERE s.is_user_process = 1 AND s.status = N'sleeping' AND tat.transaction_begin_time < DATEADD(MINUTE, -5, GETDATE())
    ORDER BY tat.transaction_begin_time;
END TRY
BEGIN CATCH
    SET @transError = ERROR_MESSAGE();
END CATCH;

` + liveFragment + `
/* SRV04 Which databases use the server: query CPU from cached plans, disk reads since the restart */
DECLARE @load TABLE (db_id int NOT NULL, cpu bigint NOT NULL, read_bytes bigint NOT NULL);
DECLARE @loadError nvarchar(2048);
IF @vss = 0 SET @loadError = @noVss;
ELSE
BEGIN TRY
    WITH cpu AS (
        SELECT CONVERT(int, pa.value) AS DbId, SUM(p.Cpu) AS Cpu
        FROM (SELECT plan_handle, SUM(total_worker_time) AS Cpu FROM sys.dm_exec_query_stats GROUP BY plan_handle) p
        CROSS APPLY sys.dm_exec_plan_attributes(p.plan_handle) pa
        WHERE pa.attribute = N'dbid'
        GROUP BY CONVERT(int, pa.value)),
    io AS (
        SELECT database_id AS DbId, SUM(num_of_bytes_read) AS ReadBytes
        FROM sys.dm_io_virtual_file_stats(NULL, NULL)
        GROUP BY database_id)
    INSERT @load (db_id, cpu, read_bytes)
    SELECT COALESCE(c.DbId, i.DbId), ISNULL(c.Cpu, 0), ISNULL(i.ReadBytes, 0)
    FROM cpu c
    FULL JOIN io i ON i.DbId = c.DbId
    WHERE COALESCE(c.DbId, i.DbId) <> 32767;
END TRY
BEGIN CATCH
    SET @loadError = ERROR_MESSAGE();
END CATCH;

/* WRK08 CPU over the last four hours: the scheduler monitor ring buffer, one record a minute, the last 256
   (sp_PressureDetector's read); other programs = 100 - SQL Server - idle */
DECLARE @cpu TABLE (at datetime NOT NULL, sql_pct int NOT NULL, idle_pct int NOT NULL);
DECLARE @cpuError nvarchar(2048), @msTicks bigint;
IF @vss = 0 SET @cpuError = @noVss;
ELSE
BEGIN TRY
    SELECT @msTicks = ms_ticks FROM sys.dm_os_sys_info;
    INSERT @cpu (at, sql_pct, idle_pct)
    SELECT TOP (256) DATEADD(SECOND, -CONVERT(int, (@msTicks - t.timestamp) / 1000), @now),
           ISNULL(t.record.value('(Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int'), 0),
           ISNULL(t.record.value('(Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int'), 0)
    FROM (SELECT rb.timestamp, CONVERT(xml, rb.record) AS record
          FROM sys.dm_os_ring_buffers AS rb
          WHERE rb.ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR' AND rb.record LIKE N'%<SystemHealth>%') AS t
    ORDER BY t.timestamp DESC;
END TRY
BEGIN CATCH
    SET @cpuError = ERROR_MESSAGE();
END CATCH;

/* WRK03 Missing index suggestions for this database, top 5 by the scan's score */
DECLARE @missing TABLE (statement nvarchar(4000) NULL, equality nvarchar(4000) NULL, inequality nvarchar(4000) NULL, included nvarchar(4000) NULL,
                        uses bigint NOT NULL, impact float NOT NULL, score float NOT NULL, last_seek datetime NULL);
DECLARE @missingError nvarchar(2048);
IF @vss = 0 SET @missingError = @noVss;
ELSE
BEGIN TRY
    INSERT @missing (statement, equality, inequality, included, uses, impact, score, last_seek)
    SELECT TOP (5) mid.statement, mid.equality_columns, mid.inequality_columns, mid.included_columns,
           migs.user_seeks + migs.user_scans, migs.avg_user_impact, x.Score, migs.last_user_seek
    FROM sys.dm_db_missing_index_group_stats migs
    JOIN sys.dm_db_missing_index_groups mig ON mig.index_group_handle = migs.group_handle
    JOIN sys.dm_db_missing_index_details mid ON mid.index_handle = mig.index_handle
    CROSS APPLY (SELECT (migs.user_seeks + migs.user_scans) * migs.avg_total_user_cost * migs.avg_user_impact AS Score) x
    WHERE mid.database_id = @dbid AND migs.avg_user_impact >= 50 AND x.Score >= 100000
    ORDER BY x.Score DESC;
END TRY
BEGIN CATCH
    SET @missingError = ERROR_MESSAGE();
END CATCH;

SELECT @found,
       CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')),
       @state, @started, DATEDIFF(HOUR, @started, @now),
       @waitsError,
       ISNULL(@deadlocks, 0), ISNULL(@batches, 0), ISNULL(@compiles, 0), ISNULL(@recompiles, 0), @countersError,
       @transError,
       @loadError, @missingError,
       @rcsi, @cpuError,
       @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
       ` + liveColumns + `;
SELECT wait_type, wait_ms, signal_ms, tasks, max_ms FROM @waits ORDER BY wait_ms DESC;
SELECT ` + openTransactionColumns + ` FROM @open ORDER BY started, session_id;
SELECT session_id, login_name, host_name, program_name, minutes, implicit FROM @idle ORDER BY began, session_id;
SELECT db_id, ISNULL(DB_NAME(db_id), N'database ' + CONVERT(nvarchar(10), db_id)), cpu, read_bytes FROM @load ORDER BY cpu DESC, read_bytes DESC;
SELECT statement, equality, inequality, included, uses, impact, score, last_seek FROM @missing ORDER BY score DESC;
SELECT at, sql_pct, idle_pct FROM @cpu ORDER BY at;
` + liveResultSets)

// planLimitChars is the size of plan XML the app still reads for warnings: 2 MB of text.
const planLimitChars = 2 * 1024 * 1024

// queryStoreTopQuery runs inside the database (EXEC [db].sys.sp_executesql): the top 5 query shapes
// by total CPU over the last 7 days, grouped by query_hash, from sys.query_store_runtime_stats
// joined to its interval, plan and query (sp_QuickieStore's join shape), with the top wait
// category of each (sys.query_store_wait_stats joined on plan, interval and execution type, 2017
// and later) and the plan XML of the plan that used the most CPU, unless it is over 2 MB. Kakapo's
// own statements and the noise patterns are left out (docs/phase3f-contract.md, item 2). Per
// query also the plans and how many of them ran under five times, the longest run, the TempDB
// pages used (avg_tempdb_space_used, 2017 and later) and the largest memory used
// (max_query_max_used_memory), each version-dependent column read only when it exists. Result
// sets: one row (actual_state_desc, error, total CPU), then the top 5.
var queryStoreTopQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @err nvarchar(2048), @total bigint, @now datetimeoffset = SYSDATETIMEOFFSET();
CREATE TABLE #top (query_hash binary(8) NOT NULL PRIMARY KEY, cpu bigint NOT NULL, execs bigint NOT NULL, reads bigint NOT NULL,
                   min_reads bigint NOT NULL, max_reads bigint NOT NULL, max_cpu bigint NOT NULL, elapsed bigint NOT NULL,
                   variants int NOT NULL, minutes int NOT NULL, plan_id bigint NULL, query_id bigint NULL, top_wait nvarchar(60) NULL,
                   max_elapsed bigint NOT NULL, plans int NULL, low_run_plans int NULL, tempdb_kb bigint NULL, max_used_kb bigint NULL,
                   now_plan bigint NULL, since datetimeoffset NULL, since_shown datetimeoffset NULL,
                   b_runs bigint NULL, b_cpu bigint NULL, n_runs bigint NULL, n_cpu bigint NULL);
CREATE TABLE #mv (query_hash binary(8) NOT NULL PRIMARY KEY, plan_id bigint NOT NULL, query_id bigint NOT NULL, since datetimeoffset NOT NULL,
                  first_run datetimeoffset NOT NULL, b_runs bigint NOT NULL, b_cpu bigint NOT NULL, n_runs bigint NOT NULL, n_cpu bigint NOT NULL);
DECLARE @chg TABLE (schema_name sysname NOT NULL, name sysname NOT NULL, type char(2) NOT NULL, created datetime NOT NULL,
                    modified datetime NOT NULL, ref_name sysname NULL);
BEGIN TRY
    SELECT @state = CONVERT(nvarchar(60), actual_state_desc) FROM sys.database_query_store_options;
    IF @state IN (N'READ_WRITE', N'READ_ONLY')
    BEGIN
        SELECT rs.runtime_stats_id, rs.plan_id, p.query_id, q.query_hash, q.query_text_id, rs.runtime_stats_interval_id, rs.execution_type, rsi.start_time,
               CONVERT(bigint, rs.count_executions) AS execs,
               CONVERT(bigint, rs.avg_cpu_time * rs.count_executions) AS cpu,
               CONVERT(bigint, rs.avg_logical_io_reads * rs.count_executions) AS reads,
               CONVERT(bigint, rs.min_logical_io_reads) AS min_reads,
               CONVERT(bigint, rs.max_logical_io_reads) AS max_reads,
               CONVERT(bigint, rs.max_cpu_time) AS max_cpu,
               CONVERT(bigint, rs.avg_duration * rs.count_executions) AS elapsed,
               CONVERT(bigint, rs.max_duration) AS max_elapsed
        INTO #rs
        FROM sys.query_store_runtime_stats AS rs
        JOIN sys.query_store_runtime_stats_interval AS rsi ON rsi.runtime_stats_interval_id = rs.runtime_stats_interval_id
        JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id
        JOIN sys.query_store_query AS q ON q.query_id = p.query_id
        JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
        WHERE rsi.start_time >= DATEADD(DAY, -7, @now)
          AND ` + noiseFilter("qt.query_sql_text") + `;

        SELECT @total = SUM(cpu) FROM #rs;

        INSERT #top (query_hash, cpu, execs, reads, min_reads, max_reads, max_cpu, elapsed, variants, minutes, max_elapsed)
        SELECT TOP (5) query_hash, SUM(cpu), SUM(execs), SUM(reads), MIN(min_reads), MAX(max_reads), MAX(max_cpu), SUM(elapsed),
               COUNT(DISTINCT query_text_id), DATEDIFF(MINUTE, MIN(start_time), @now), MAX(max_elapsed)
        FROM #rs
        GROUP BY query_hash
        ORDER BY SUM(cpu) DESC;

        UPDATE t SET plan_id = r.plan_id, query_id = r.query_id
        FROM #top t
        CROSS APPLY (SELECT TOP (1) x.plan_id, x.query_id FROM #rs x WHERE x.query_hash = t.query_hash
                     GROUP BY x.plan_id, x.query_id ORDER BY SUM(x.cpu) DESC) r;

        UPDATE t SET plans = p.plans, low_run_plans = p.low_run_plans
        FROM #top t
        CROSS APPLY (SELECT COUNT(*) AS plans, SUM(CASE WHEN y.execs < 5 THEN 1 ELSE 0 END) AS low_run_plans
                     FROM (SELECT x.plan_id, SUM(x.execs) AS execs FROM #rs x WHERE x.query_hash = t.query_hash GROUP BY x.plan_id) y) p;

        -- Phase 5j: the plan in use (the one that ran last), when it began, and the runs and CPU before and since.
        UPDATE t SET now_plan = r.plan_id
        FROM #top t
        CROSS APPLY (SELECT TOP (1) x.plan_id FROM #rs x WHERE x.query_hash = t.query_hash ORDER BY x.start_time DESC, x.cpu DESC) r;
        UPDATE t SET since = s.since
        FROM #top t
        CROSS APPLY (SELECT MIN(x.start_time) AS since FROM #rs x WHERE x.query_hash = t.query_hash AND x.plan_id = t.now_plan) s;
        UPDATE t SET since_shown = CASE WHEN qp.initial_compile_start_time >= t.since AND qp.initial_compile_start_time < DATEADD(HOUR, 1, t.since)
                                        THEN qp.initial_compile_start_time ELSE t.since END
        FROM #top t JOIN sys.query_store_plan AS qp ON qp.plan_id = t.now_plan;
        UPDATE t SET b_runs = s.b_runs, b_cpu = s.b_cpu, n_runs = s.n_runs, n_cpu = s.n_cpu
        FROM #top t
        JOIN (SELECT x.query_hash,
                     SUM(CASE WHEN x.start_time < w.since THEN x.execs ELSE 0 END) AS b_runs,
                     SUM(CASE WHEN x.start_time < w.since THEN x.cpu ELSE 0 END) AS b_cpu,
                     SUM(CASE WHEN x.start_time >= w.since THEN x.execs ELSE 0 END) AS n_runs,
                     SUM(CASE WHEN x.start_time >= w.since THEN x.cpu ELSE 0 END) AS n_cpu
              FROM #rs x JOIN #top w ON w.query_hash = x.query_hash
              GROUP BY x.query_hash) s ON s.query_hash = t.query_hash;

        IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.query_store_runtime_stats') AND name = N'avg_tempdb_space_used')
            EXEC sys.sp_executesql N'/* kakapo */ UPDATE t SET tempdb_kb = x.kb
                FROM #top t
                CROSS APPLY (SELECT SUM(CONVERT(bigint, s.avg_tempdb_space_used * 8 * s.count_executions)) AS kb
                             FROM #rs r JOIN sys.query_store_runtime_stats s ON s.runtime_stats_id = r.runtime_stats_id
                             WHERE r.query_hash = t.query_hash) x;';

        IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.query_store_runtime_stats') AND name = N'max_query_max_used_memory')
            EXEC sys.sp_executesql N'/* kakapo */ UPDATE t SET max_used_kb = x.kb
                FROM #top t
                CROSS APPLY (SELECT MAX(CONVERT(bigint, s.max_query_max_used_memory) * 8) AS kb
                             FROM #rs r JOIN sys.query_store_runtime_stats s ON s.runtime_stats_id = r.runtime_stats_id
                             WHERE r.query_hash = t.query_hash) x;';

        IF OBJECT_ID(N'sys.query_store_wait_stats') IS NOT NULL
            EXEC sys.sp_executesql N'/* kakapo */ UPDATE t SET top_wait = w.wait_category_desc
                FROM #top t
                CROSS APPLY (SELECT TOP (1) CONVERT(nvarchar(60), ws.wait_category_desc) AS wait_category_desc
                             FROM #rs r
                             JOIN sys.query_store_wait_stats ws
                               ON ws.plan_id = r.plan_id AND ws.runtime_stats_interval_id = r.runtime_stats_interval_id
                              AND ws.execution_type = r.execution_type
                             WHERE r.query_hash = t.query_hash
                             GROUP BY ws.wait_category_desc
                             ORDER BY SUM(ws.total_query_wait_time_ms) DESC) w;';

        -- Phase 5k: every query shape whose plan in use (the one that ran last) began in the window and that
        -- ran at least 20 times (5c's TuningMinRuns) before and since, with its runs and CPU on each side.
        WITH lastp AS (SELECT query_hash, plan_id, ROW_NUMBER() OVER (PARTITION BY query_hash ORDER BY start_time DESC, cpu DESC) AS rn
                       FROM #rs),
             sincep AS (SELECT l.query_hash, l.plan_id, MIN(r.start_time) AS since
                        FROM lastp AS l JOIN #rs AS r ON r.query_hash = l.query_hash AND r.plan_id = l.plan_id
                        WHERE l.rn = 1
                        GROUP BY l.query_hash, l.plan_id)
        INSERT #mv (query_hash, plan_id, query_id, since, first_run, b_runs, b_cpu, n_runs, n_cpu)
        SELECT s.query_hash, s.plan_id, MAX(CASE WHEN r.plan_id = s.plan_id THEN r.query_id END), s.since, MIN(r.start_time),
               SUM(CASE WHEN r.start_time < s.since THEN r.execs ELSE 0 END), SUM(CASE WHEN r.start_time < s.since THEN r.cpu ELSE 0 END),
               SUM(CASE WHEN r.start_time >= s.since THEN r.execs ELSE 0 END), SUM(CASE WHEN r.start_time >= s.since THEN r.cpu ELSE 0 END)
        FROM sincep AS s JOIN #rs AS r ON r.query_hash = s.query_hash
        GROUP BY s.query_hash, s.plan_id, s.since
        HAVING SUM(CASE WHEN r.start_time < s.since THEN r.execs ELSE 0 END) >= 20
           AND SUM(CASE WHEN r.start_time >= s.since THEN r.execs ELSE 0 END) >= 20;
    END;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
-- Phase 5k: the tables, views and modules changed in the last 8 days (server time), one row per view or
-- module that references each (none: one row), to say what changed just before a query got a new plan.
-- Its own TRY: a failure here leaves the top queries as they are.
BEGIN TRY
    INSERT @chg (schema_name, name, type, created, modified, ref_name)
    SELECT SCHEMA_NAME(o.schema_id), o.name, o.type, o.create_date, o.modify_date, OBJECT_NAME(d.referencing_id)
    FROM sys.objects AS o
    LEFT JOIN sys.sql_expression_dependencies AS d ON d.referenced_id = o.object_id AND d.referencing_id <> o.object_id
    WHERE o.is_ms_shipped = 0 AND o.type IN ('U', 'V', 'P', 'FN', 'IF', 'TF', 'TR') AND o.modify_date >= DATEADD(DAY, -8, GETDATE());
END TRY
BEGIN CATCH
    DELETE @chg;
END CATCH;
SELECT @state, @err, @total;
SELECT CONVERT(varchar(18), t.query_hash, 1), t.cpu, t.execs, t.reads, t.min_reads, t.max_reads, t.max_cpu, t.elapsed, t.variants, t.minutes,
       CASE WHEN q.object_id > 0 THEN OBJECT_SCHEMA_NAME(q.object_id) + N'.' + OBJECT_NAME(q.object_id) END,
       qt.query_sql_text, t.top_wait,
       CONVERT(bigint, DATALENGTH(p.query_plan) / 2),
       CASE WHEN DATALENGTH(p.query_plan) / 2 <= @limit THEN p.query_plan END,
       CONVERT(bigint, -1), CONVERT(bigint, -1), ISNULL(t.max_used_kb, -1), ISNULL(t.tempdb_kb, -1),
       ISNULL(t.plans, 0), ISNULL(t.low_run_plans, 0), t.max_elapsed,
       CONVERT(datetime2, SWITCHOFFSET(t.since_shown, '+00:00')), ISNULL(t.b_runs, 0), ISNULL(t.b_cpu, 0), ISNULL(t.n_runs, 0), ISNULL(t.n_cpu, 0)
FROM #top t
LEFT JOIN sys.query_store_plan p ON p.plan_id = t.plan_id
LEFT JOIN sys.query_store_query q ON q.query_id = t.query_id
LEFT JOIN sys.query_store_query_text qt ON qt.query_text_id = q.query_text_id
ORDER BY t.cpu DESC;
SELECT CONVERT(varchar(18), m.query_hash, 1),
       CONVERT(datetime2, SWITCHOFFSET(CASE WHEN p.initial_compile_start_time >= m.since AND p.initial_compile_start_time < DATEADD(HOUR, 1, m.since)
                                            THEN p.initial_compile_start_time ELSE m.since END, '+00:00')),
       DATEDIFF(MINUTE, m.first_run, @now), m.b_runs, m.b_cpu, m.n_runs, m.n_cpu,
       CASE WHEN q.object_id > 0 THEN OBJECT_SCHEMA_NAME(q.object_id) + N'.' + OBJECT_NAME(q.object_id) END,
       LEFT(qt.query_sql_text, 300)
FROM #mv m
LEFT JOIN sys.query_store_plan p ON p.plan_id = m.plan_id
LEFT JOIN sys.query_store_query q ON q.query_id = m.query_id
LEFT JOIN sys.query_store_query_text qt ON qt.query_text_id = q.query_text_id
ORDER BY m.query_hash;
SELECT schema_name, name, RTRIM(type), created, modified, ISNULL(ref_name, N'') FROM @chg ORDER BY modified, schema_name, name;`)

// planCacheTopQuery is the triage's section 0 for one database (PerformanceTriage.sql): the top 300
// query shapes in the plan cache by total CPU, the top 5 of those whose plan belongs to this
// database, with the plan XML of the most expensive statement of each unless it is over 2 MB.
// Kakapo's own batches and the noise patterns are left out (docs/phase3f-contract.md, item 2).
// Per query also the plans and how many ran under five times, the longest run, and the spills
// and memory grants (total_spills, max_grant_kb, max_used_grant_kb: 2016 SP2 and later, read
// only when the columns exist, as sp_QuickieCache checks them). Result sets: one row (error,
// total CPU), then the top 5.
var planCacheTopQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @dbid int = DB_ID(@db), @total bigint, @err nvarchar(2048);
CREATE TABLE #topq (rnk int NOT NULL, query_hash binary(8) NOT NULL, cpu bigint NOT NULL, execs bigint NOT NULL, reads bigint NOT NULL,
                    min_reads bigint NOT NULL, max_reads bigint NOT NULL, max_cpu bigint NOT NULL, variants int NOT NULL,
                    first_cached datetime NOT NULL, elapsed bigint NOT NULL, object_name nvarchar(300) NULL, query_text nvarchar(max) NULL,
                    plan_handle varbinary(64) NOT NULL, stmt_start int NOT NULL, stmt_end int NOT NULL,
                    max_elapsed bigint NOT NULL, plans int NULL, low_run_plans int NULL, spills bigint NULL, max_grant_kb bigint NULL,
                    max_used_kb bigint NULL);
BEGIN TRY
    SELECT @total = SUM(total_worker_time) FROM sys.dm_exec_query_stats;

    WITH agg AS (
        SELECT TOP (300) query_hash, SUM(total_worker_time) AS Cpu, SUM(execution_count) AS Execs,
               SUM(total_logical_reads) AS Reads, MIN(min_logical_reads) AS MinReads,
               MAX(max_logical_reads) AS MaxReads, MAX(max_worker_time) AS MaxCpu,
               COUNT(DISTINCT sql_handle) AS Variants, MIN(creation_time) AS FirstCached,
               SUM(total_elapsed_time) AS Elapsed, MAX(max_elapsed_time) AS MaxElapsed
        FROM sys.dm_exec_query_stats
        GROUP BY query_hash
        ORDER BY SUM(total_worker_time) DESC),
    rep AS (
        SELECT query_hash, sql_handle, plan_handle, statement_start_offset, statement_end_offset,
               ROW_NUMBER() OVER (PARTITION BY query_hash ORDER BY total_worker_time DESC) AS rn
        FROM sys.dm_exec_query_stats)
    INSERT #topq (rnk, query_hash, cpu, execs, reads, min_reads, max_reads, max_cpu, variants, first_cached, elapsed, object_name, query_text,
                  plan_handle, stmt_start, stmt_end, max_elapsed)
    SELECT TOP (5) ROW_NUMBER() OVER (ORDER BY a.Cpu DESC), a.query_hash, a.Cpu, a.Execs, a.Reads, a.MinReads, a.MaxReads, a.MaxCpu,
        a.Variants, a.FirstCached, a.Elapsed,
        CASE WHEN st.objectid IS NOT NULL THEN OBJECT_SCHEMA_NAME(st.objectid, st.dbid) + N'.' + OBJECT_NAME(st.objectid, st.dbid) END,
        x.stmt, r.plan_handle, r.statement_start_offset, r.statement_end_offset, a.MaxElapsed
    FROM agg a
    JOIN rep r ON r.query_hash = a.query_hash AND r.rn = 1
    CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) st
    CROSS APPLY (SELECT SUBSTRING(st.text, r.statement_start_offset / 2 + 1,
                     (CASE r.statement_end_offset WHEN -1 THEN DATALENGTH(st.text) ELSE r.statement_end_offset END - r.statement_start_offset) / 2 + 1) AS stmt) x
    CROSS APPLY (SELECT CONVERT(int, pa.value) AS dbid FROM sys.dm_exec_plan_attributes(r.plan_handle) pa WHERE pa.attribute = N'dbid') pa
    WHERE pa.dbid = @dbid
      AND ISNULL(st.text, N'') NOT LIKE N'%` + KakapoTag + `%'
      AND ` + noiseFilter("x.stmt") + `
    ORDER BY a.Cpu DESC;

    UPDATE q SET plans = p.plans, low_run_plans = p.low_run_plans FROM #topq q
    CROSS APPLY (SELECT COUNT(*) AS plans, SUM(CASE WHEN y.execs < 5 THEN 1 ELSE 0 END) AS low_run_plans
                 FROM (SELECT qs.plan_handle, SUM(qs.execution_count) AS execs FROM sys.dm_exec_query_stats qs
                       WHERE qs.query_hash = q.query_hash GROUP BY qs.plan_handle) y) p;

    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_exec_query_stats') AND name = N'total_spills')
        EXEC sys.sp_executesql N'/* kakapo */ UPDATE q SET spills = x.spills FROM #topq q
            CROSS APPLY (SELECT SUM(qs.total_spills) AS spills FROM sys.dm_exec_query_stats qs WHERE qs.query_hash = q.query_hash) x;';

    IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.dm_exec_query_stats') AND name = N'max_grant_kb')
        EXEC sys.sp_executesql N'/* kakapo */ UPDATE q SET max_grant_kb = x.grant_kb, max_used_kb = x.used_kb FROM #topq q
            CROSS APPLY (SELECT MAX(qs.max_grant_kb) AS grant_kb, MAX(qs.max_used_grant_kb) AS used_kb
                         FROM sys.dm_exec_query_stats qs WHERE qs.query_hash = q.query_hash) x;';
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @err, @total;
SELECT CONVERT(varchar(18), q.query_hash, 1), q.cpu, q.execs, q.reads, q.min_reads, q.max_reads, q.max_cpu, q.elapsed, q.variants,
       DATEDIFF(MINUTE, q.first_cached, GETDATE()), q.object_name, q.query_text, CONVERT(nvarchar(60), NULL),
       CONVERT(bigint, DATALENGTH(p.PlanText) / 2),
       CASE WHEN DATALENGTH(p.PlanText) / 2 <= @limit THEN p.PlanText END,
       ISNULL(q.spills, -1), ISNULL(q.max_grant_kb, -1), ISNULL(q.max_used_kb, -1), CONVERT(bigint, -1),
       ISNULL(q.plans, 0), ISNULL(q.low_run_plans, 0), q.max_elapsed,
       CONVERT(datetime2, NULL), CONVERT(bigint, 0), CONVERT(bigint, 0), CONVERT(bigint, 0), CONVERT(bigint, 0)
FROM #topq q
OUTER APPLY (SELECT CONVERT(nvarchar(max), tp.query_plan) AS PlanText
             FROM sys.dm_exec_text_query_plan(q.plan_handle, q.stmt_start, q.stmt_end) tp) p
ORDER BY q.rnk;`)

// PerformanceFacts reads what the Performance card needs about one database and its instance
// (docs/phase3e-contract.md), SELECTs only, in a few batches: the instance; then, when the database
// is online, Query Store's top queries inside it; and when Query Store is off or has nothing for
// the last 7 days, the plan cache's top queries for it. An area the login may not read carries its
// reason in the facts instead of failing the read; the error returned is for a read that could not
// run at all, or a database that does not exist.
func PerformanceFacts(ctx context.Context, db *sql.DB, database string) (domain.PerformanceFacts, error) {
	f := domain.PerformanceFacts{Database: database}
	if err := readPerformanceInstance(ctx, db, database, &f); err != nil {
		return f, err
	}
	if f.State != "ONLINE" {
		return f, nil
	}
	state, total, top, err := readQueryStoreTop(ctx, db, database, &f)
	f.QueryStoreState = state
	if err != nil {
		f.QueryStoreError = err.Error()
	}
	if err == nil && len(top) > 0 {
		f.QuerySource, f.TotalCPU, f.TopQueries = domain.QuerySourceQueryStore, total, top
		return f, nil
	}
	total, top, cacheErr := readPlanCacheTop(ctx, db, database)
	if cacheErr != nil {
		f.QueriesError = cacheErr.Error()
		if err != nil {
			f.QueriesError = "Query Store: " + err.Error() + " Plan cache: " + cacheErr.Error()
		}
		return f, nil
	}
	f.QuerySource, f.TotalCPU, f.TopQueries = domain.QuerySourcePlanCache, total, top
	return f, nil
}

func readPerformanceInstance(ctx context.Context, db *sql.DB, database string, f *domain.PerformanceFacts) error {
	rows, err := db.QueryContext(ctx, performanceInstanceQuery, sql.Named("db", database))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned no performance facts")
	}
	var (
		found, rcsi                              bool
		state, waitsErr, countersErr, transErr   sql.NullString
		loadErr, missingErr, cpuErr              sql.NullString
		started                                  sql.NullTime
		uptime                                   sql.NullInt32
		deadlocks, batches, compiles, recompiles int64
		live                                     liveRow
	)
	if err := rows.Scan(append([]any{&found, &f.ServerName, &f.ProductVersion, &f.EngineEdition,
		&state, &started, &uptime,
		&waitsErr,
		&deadlocks, &batches, &compiles, &recompiles, &countersErr,
		&transErr,
		&loadErr, &missingErr,
		&rcsi, &cpuErr,
		&f.ServerNow, &f.UTCOffsetMinutes}, live.dest()...)...); err != nil {
		return fmt.Errorf("read the performance facts of %s: %w", database, err)
	}
	if !found {
		return fmt.Errorf("there is no database named %q", database)
	}
	f.ServerNow = naive(f.ServerNow)
	f.State, f.RCSI = state.String, rcsi
	if started.Valid {
		f.StartedAt = naive(started.Time)
	}
	f.UptimeHours = int(uptime.Int32)
	f.WaitsError = waitsErr.String
	f.Deadlocks, f.Batches, f.Compiles, f.Recompiles, f.CountersError = deadlocks, batches, compiles, recompiles, countersErr.String
	f.TransactionsError = transErr.String
	// ACT01 and PRS01: the live fragment's values, read as the load strip reads them.
	live.apply(f)
	f.LoadError, f.MissingIndexError = loadErr.String, missingErr.String
	f.CPUHistoryError = cpuErr.String

	// Waits.
	if err := nextResultSet(rows, "waits"); err != nil {
		return err
	}
	f.Waits = []domain.WaitStat{}
	for rows.Next() {
		var w domain.WaitStat
		if err := rows.Scan(&w.Type, &w.WaitMs, &w.SignalMs, &w.Tasks, &w.MaxWaitMs); err != nil {
			return err
		}
		f.Waits = append(f.Waits, w)
	}

	// Open transactions in this database.
	if err := nextResultSet(rows, "open transactions"); err != nil {
		return err
	}
	f.OpenTransactions = []domain.OpenTransaction{}
	for rows.Next() {
		x, err := scanOpenTransaction(rows)
		if err != nil {
			return err
		}
		f.OpenTransactions = append(f.OpenTransactions, x)
	}

	// Sessions sleeping with a transaction open.
	if err := nextResultSet(rows, "idle transactions"); err != nil {
		return err
	}
	f.IdleTransactions = []domain.IdleTransaction{}
	for rows.Next() {
		var x domain.IdleTransaction
		var login, host, program sql.NullString
		if err := rows.Scan(&x.Session, &login, &host, &program, &x.Minutes, &x.Implicit); err != nil {
			return err
		}
		x.Login, x.Host, x.Program = login.String, host.String, program.String
		f.IdleTransactions = append(f.IdleTransactions, x)
	}

	// Load per database.
	if err := nextResultSet(rows, "load per database"); err != nil {
		return err
	}
	f.Loads = []domain.DatabaseLoad{}
	for rows.Next() {
		var x domain.DatabaseLoad
		if err := rows.Scan(&x.DatabaseID, &x.Name, &x.CPU, &x.ReadBytes); err != nil {
			return err
		}
		f.Loads = append(f.Loads, x)
	}

	// Missing indexes.
	if err := nextResultSet(rows, "missing indexes"); err != nil {
		return err
	}
	f.MissingIndexes = []domain.MissingIndex{}
	for rows.Next() {
		var x domain.MissingIndex
		var stmt, eq, ineq, inc sql.NullString
		var last sql.NullTime
		if err := rows.Scan(&stmt, &eq, &ineq, &inc, &x.Uses, &x.Impact, &x.Score, &last); err != nil {
			return err
		}
		x.Statement, x.Equality, x.Inequality, x.Included, x.LastSeek = stmt.String, eq.String, ineq.String, inc.String, naivePtr(last)
		f.MissingIndexes = append(f.MissingIndexes, x)
	}

	// CPU history, oldest first.
	if err := nextResultSet(rows, "CPU history"); err != nil {
		return err
	}
	f.CPUHistory = []domain.CPUSample{}
	for rows.Next() {
		var x domain.CPUSample
		var at time.Time
		if err := rows.Scan(&at, &x.SQL, &x.Idle); err != nil {
			return err
		}
		x.At, x.Other = naive(at), max(0, 100-x.SQL-x.Idle)
		f.CPUHistory = append(f.CPUHistory, x)
	}

	// The head blockers and the largest memory grants held now (the live fragment's result sets).
	if err := scanLiveSets(rows, f); err != nil {
		return err
	}
	return friendlyErrOrNil(rows.Err())
}

// readQueryStoreTop reads Query Store's top 5 inside the database: its state, the CPU of every
// query in the window, and the top 5. An error says why Query Store could not be read.
func readQueryStoreTop(ctx context.Context, db *sql.DB, database string, f *domain.PerformanceFacts) (string, int64, []domain.TopQuery, error) {
	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt, N'@limit bigint', @limit = @limit;")
	rows, err := db.QueryContext(ctx, q, sql.Named("stmt", queryStoreTopQuery), sql.Named("limit", int64(planLimitChars)))
	if err != nil {
		return "", 0, nil, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", 0, nil, friendlyError(err)
		}
		return "", 0, nil, errors.New("SQL Server returned nothing from Query Store")
	}
	var state, qsErr sql.NullString
	var total sql.NullInt64
	if err := rows.Scan(&state, &qsErr, &total); err != nil {
		return "", 0, nil, err
	}
	if err := nextResultSet(rows, "Query Store top queries"); err != nil {
		return state.String, 0, nil, err
	}
	top, err := scanTopQueries(rows)
	if err != nil {
		return state.String, 0, nil, err
	}
	if qsErr.String != "" {
		return state.String, 0, nil, errors.New(qsErr.String)
	}
	// Phase 5k: what changed. A part that fails is said in the facts; the top queries stand.
	if err := nextResultSet(rows, "plan changes"); err != nil {
		f.MoversError = err.Error()
		return state.String, total.Int64, top, nil
	}
	if f.Movers, err = scanPlanMovers(rows); err != nil {
		f.MoversError = err.Error()
		return state.String, total.Int64, top, nil
	}
	if err := nextResultSet(rows, "changed objects"); err != nil {
		f.MoversError = err.Error()
		return state.String, total.Int64, top, nil
	}
	if f.Changed, err = scanChangedObjects(rows); err != nil {
		f.MoversError = err.Error()
	}
	return state.String, total.Int64, top, nil
}

// scanPlanMovers reads the batch's third result set (phase 5k): the query shapes whose plan in use
// began in the window, with their runs and CPU before and since.
func scanPlanMovers(rows *sql.Rows) ([]domain.PlanMoverFact, error) {
	out := []domain.PlanMoverFact{}
	for rows.Next() {
		var m domain.PlanMoverFact
		var since sql.NullTime
		var minutes sql.NullInt32
		var object, text sql.NullString
		if err := rows.Scan(&m.QueryHash, &since, &minutes, &m.BeforeRuns, &m.BeforeCPU, &m.NowRuns, &m.NowCPU, &object, &text); err != nil {
			return nil, err
		}
		if since.Valid {
			m.Since = naive(since.Time)
		}
		m.Minutes, m.ObjectName, m.Text = int(minutes.Int32), object.String, text.String
		out = append(out, m)
	}
	return out, friendlyErrOrNil(rows.Err())
}

// scanChangedObjects reads the batch's fourth result set (phase 5k): the objects changed in the last 8
// days, in the server's clock.
func scanChangedObjects(rows *sql.Rows) ([]domain.ChangedObjectFact, error) {
	out := []domain.ChangedObjectFact{}
	at := map[string]int{}
	for rows.Next() {
		var c domain.ChangedObjectFact
		var ref string
		if err := rows.Scan(&c.Schema, &c.Name, &c.Type, &c.Created, &c.Modified, &ref); err != nil {
			return nil, err
		}
		key := c.Schema + "." + c.Name
		i, ok := at[key]
		if !ok {
			c.Created, c.Modified, c.Referenced = naive(c.Created), naive(c.Modified), []string{}
			i = len(out)
			at[key] = i
			out = append(out, c)
		}
		if ref = strings.TrimSpace(ref); ref != "" {
			out[i].Referenced = append(out[i].Referenced, ref)
		}
	}
	return out, friendlyErrOrNil(rows.Err())
}

// readPlanCacheTop reads the plan cache's top 5 for the database, as PerformanceTriage.sql does.
func readPlanCacheTop(ctx context.Context, db *sql.DB, database string) (int64, []domain.TopQuery, error) {
	rows, err := db.QueryContext(ctx, planCacheTopQuery, sql.Named("db", database), sql.Named("limit", int64(planLimitChars)))
	if err != nil {
		return 0, nil, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, nil, friendlyError(err)
		}
		return 0, nil, errors.New("SQL Server returned nothing from the plan cache")
	}
	var cacheErr sql.NullString
	var total sql.NullInt64
	if err := rows.Scan(&cacheErr, &total); err != nil {
		return 0, nil, err
	}
	if err := nextResultSet(rows, "plan cache top queries"); err != nil {
		return 0, nil, err
	}
	top, err := scanTopQueries(rows)
	if err != nil {
		return 0, nil, err
	}
	if cacheErr.String != "" {
		return 0, nil, errors.New(cacheErr.String)
	}
	return total.Int64, top, nil
}

func scanTopQueries(rows *sql.Rows) ([]domain.TopQuery, error) {
	out := []domain.TopQuery{}
	for rows.Next() {
		var q domain.TopQuery
		var hash, object, text, wait, plan sql.NullString
		var minutes sql.NullInt32
		var planChars sql.NullInt64
		var since sql.NullTime
		if err := rows.Scan(&hash, &q.CPU, &q.Execs, &q.Reads, &q.MinReads, &q.MaxReads, &q.MaxCPU, &q.Elapsed, &q.Variants, &minutes,
			&object, &text, &wait, &planChars, &plan,
			&q.Spills, &q.MaxGrantKB, &q.MaxUsedGrantKB, &q.TempdbKB, &q.Plans, &q.LowRunPlans, &q.MaxElapsed,
			&since, &q.BeforeRuns, &q.BeforeCPU, &q.NowRuns, &q.NowCPU); err != nil {
			return nil, err
		}
		q.Since = naivePtr(since)
		q.QueryHash, q.ObjectName, q.Text, q.TopWait = hash.String, object.String, text.String, wait.String
		q.Minutes = int(minutes.Int32)
		q.Plan = plan.String
		q.PlanTooBig = planChars.Int64 > planLimitChars
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, friendlyError(err)
	}
	return out, nil
}
