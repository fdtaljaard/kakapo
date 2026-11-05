package collect

import (
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// Investigate a query (docs/phase4c-contract.md, part 1): one query shape of a database by its
// query hash. SELECTs only, each area in its own TRY with its own error, version-dependent columns
// read only when they exist. The fixes' reads (plan forcing, a cached plan, a table's statistics)
// are here too; the fixes' statements run through ExecFix.

// queryDetailStoreQuery runs inside the database (EXEC [db].sys.sp_executesql) with @hash and
// @limit: Query Store's state; the query's text and object (its most recent query id); every plan
// of the hash (sys.query_store_plan, at most 50, forced first, then the most recent) with its
// runtime over the last 7 days (sp_QuickieStore's join of runtime_stats to its interval) and the
// plan XML of the first ten unless over @limit characters; the runs per hour and plan over 7 days
// (UTC hours); the waits per category over 7 days (sys.query_store_wait_stats, 2017 and later).
// TempDB use per plan (avg_tempdb_space_used) only when the column exists. Result sets: one row
// (state, errors, text, object id, object name, compatibility level), the plans, the hours, the
// waits.
var queryDetailStoreQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @qsErr nvarchar(2048), @histErr nvarchar(2048), @waitErr nvarchar(2048),
        @now datetimeoffset = SYSDATETIMEOFFSET(), @since datetimeoffset, @text nvarchar(max), @objid int, @compat int;
SET @since = DATEADD(DAY, -7, @now);
SELECT @compat = CONVERT(int, compatibility_level) FROM sys.databases WHERE database_id = DB_ID();
CREATE TABLE #plans (plan_id bigint NOT NULL PRIMARY KEY, query_id bigint NOT NULL, forced bit NOT NULL, initial_compile datetimeoffset NULL,
                     last_compile datetimeoffset NULL, last_run datetimeoffset NULL, engine_version nvarchar(32) NULL, compat int NULL,
                     plan_hash varchar(18) NULL, force_failures bigint NULL, force_reason nvarchar(128) NULL, runs bigint NULL, cpu bigint NULL,
                     max_cpu bigint NULL, duration bigint NULL, max_duration bigint NULL, reads bigint NULL, max_reads bigint NULL,
                     tempdb_kb bigint NULL, rnk int NULL);
CREATE TABLE #hist (hour_utc datetime NOT NULL, plan_id bigint NOT NULL, runs bigint NOT NULL, cpu bigint NOT NULL, duration bigint NOT NULL,
                    reads bigint NOT NULL);
CREATE TABLE #waits (category nvarchar(60) NOT NULL, wait_ms bigint NOT NULL);

/* The query and its plans */
BEGIN TRY
    SELECT @state = CONVERT(nvarchar(60), actual_state_desc) FROM sys.database_query_store_options;
    IF @state IN (N'READ_WRITE', N'READ_ONLY')
    BEGIN
        SELECT TOP (1) @objid = q.object_id, @text = qt.query_sql_text
        FROM sys.query_store_query AS q
        JOIN sys.query_store_query_text AS qt ON qt.query_text_id = q.query_text_id
        WHERE q.query_hash = @hash
        ORDER BY q.last_execution_time DESC;

        INSERT #plans (plan_id, query_id, forced, initial_compile, last_compile, last_run, engine_version, compat, plan_hash, force_failures, force_reason)
        SELECT TOP (50) p.plan_id, p.query_id, p.is_forced_plan, p.initial_compile_start_time, p.last_compile_start_time, p.last_execution_time,
               CONVERT(nvarchar(32), p.engine_version), CONVERT(int, p.compatibility_level), CONVERT(varchar(18), p.query_plan_hash, 1),
               p.force_failure_count, CONVERT(nvarchar(128), p.last_force_failure_reason_desc)
        FROM sys.query_store_plan AS p
        JOIN sys.query_store_query AS q ON q.query_id = p.query_id
        WHERE q.query_hash = @hash
        ORDER BY p.is_forced_plan DESC, p.last_execution_time DESC;

        UPDATE #plans SET runs = r.runs, cpu = r.cpu, max_cpu = r.max_cpu, duration = r.duration, max_duration = r.max_duration,
                          reads = r.reads, max_reads = r.max_reads
        FROM #plans
        CROSS APPLY (SELECT SUM(rs.count_executions) AS runs,
                            CONVERT(bigint, SUM(rs.avg_cpu_time * rs.count_executions)) AS cpu, CONVERT(bigint, MAX(rs.max_cpu_time)) AS max_cpu,
                            CONVERT(bigint, SUM(rs.avg_duration * rs.count_executions)) AS duration, CONVERT(bigint, MAX(rs.max_duration)) AS max_duration,
                            CONVERT(bigint, SUM(rs.avg_logical_io_reads * rs.count_executions)) AS reads,
                            CONVERT(bigint, MAX(rs.max_logical_io_reads)) AS max_reads
                     FROM sys.query_store_runtime_stats AS rs
                     JOIN sys.query_store_runtime_stats_interval AS i ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
                     WHERE rs.plan_id = #plans.plan_id AND i.start_time >= @since) AS r;

        UPDATE #plans SET rnk = x.rnk
        FROM #plans
        JOIN (SELECT plan_id, ROW_NUMBER() OVER (ORDER BY forced DESC, last_run DESC) AS rnk FROM #plans) AS x ON x.plan_id = #plans.plan_id;

        IF EXISTS (SELECT 1 FROM sys.all_columns WHERE object_id = OBJECT_ID(N'sys.query_store_runtime_stats') AND name = N'avg_tempdb_space_used')
            EXEC sys.sp_executesql N'/* kakapo */ UPDATE #plans SET tempdb_kb = x.kb
                FROM #plans
                CROSS APPLY (SELECT CONVERT(bigint, SUM(rs.avg_tempdb_space_used * 8 * rs.count_executions)) AS kb
                             FROM sys.query_store_runtime_stats AS rs
                             JOIN sys.query_store_runtime_stats_interval AS i ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
                             WHERE rs.plan_id = #plans.plan_id AND i.start_time >= @since) AS x;', N'@since datetimeoffset', @since = @since;
    END;
END TRY
BEGIN CATCH
    SET @qsErr = ERROR_MESSAGE();
END CATCH;

/* History: runs, CPU, duration and reads per hour and plan, the last 7 days */
IF @qsErr IS NULL AND @state IN (N'READ_WRITE', N'READ_ONLY')
BEGIN TRY
    INSERT #hist (hour_utc, plan_id, runs, cpu, duration, reads)
    SELECT DATEADD(HOUR, DATEDIFF(HOUR, 0, CONVERT(datetime, SWITCHOFFSET(i.start_time, 0))), 0), rs.plan_id,
           SUM(rs.count_executions), CONVERT(bigint, SUM(rs.avg_cpu_time * rs.count_executions)),
           CONVERT(bigint, SUM(rs.avg_duration * rs.count_executions)), CONVERT(bigint, SUM(rs.avg_logical_io_reads * rs.count_executions))
    FROM sys.query_store_runtime_stats AS rs
    JOIN sys.query_store_runtime_stats_interval AS i ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
    JOIN sys.query_store_plan AS p ON p.plan_id = rs.plan_id
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    WHERE q.query_hash = @hash AND i.start_time >= @since
    GROUP BY DATEADD(HOUR, DATEDIFF(HOUR, 0, CONVERT(datetime, SWITCHOFFSET(i.start_time, 0))), 0), rs.plan_id;
END TRY
BEGIN CATCH
    SET @histErr = ERROR_MESSAGE();
END CATCH;

/* Waits by category, the last 7 days (2017 and later) */
IF @qsErr IS NULL AND @state IN (N'READ_WRITE', N'READ_ONLY')
BEGIN TRY
    IF OBJECT_ID(N'sys.query_store_wait_stats') IS NULL
        SET @waitErr = N'Query Store keeps waits from SQL Server 2017 on.';
    ELSE
        EXEC sys.sp_executesql N'/* kakapo */ INSERT #waits (category, wait_ms)
            SELECT CONVERT(nvarchar(60), ws.wait_category_desc), SUM(ws.total_query_wait_time_ms)
            FROM sys.query_store_wait_stats AS ws
            JOIN sys.query_store_runtime_stats_interval AS i ON i.runtime_stats_interval_id = ws.runtime_stats_interval_id
            JOIN sys.query_store_plan AS p ON p.plan_id = ws.plan_id
            JOIN sys.query_store_query AS q ON q.query_id = p.query_id
            WHERE q.query_hash = @hash AND i.start_time >= @since
            GROUP BY ws.wait_category_desc;', N'@hash binary(8), @since datetimeoffset', @hash = @hash, @since = @since;
END TRY
BEGIN CATCH
    SET @waitErr = ERROR_MESSAGE();
END CATCH;

SELECT @state, @qsErr, @histErr, @waitErr, @text, @objid,
       CASE WHEN @objid > 0 THEN OBJECT_SCHEMA_NAME(@objid) + N'.' + OBJECT_NAME(@objid) END, @compat;
SELECT p.plan_id, p.query_id, p.forced, p.initial_compile, p.last_compile, p.last_run, p.engine_version, p.compat, p.plan_hash,
       ISNULL(p.force_failures, 0), p.force_reason, ISNULL(p.runs, 0), ISNULL(p.cpu, 0), ISNULL(p.max_cpu, 0), ISNULL(p.duration, 0),
       ISNULL(p.max_duration, 0), ISNULL(p.reads, 0), ISNULL(p.max_reads, 0), ISNULL(p.tempdb_kb, -1),
       CONVERT(bigint, DATALENGTH(qp.query_plan) / 2),
       CASE WHEN p.rnk <= 10 AND DATALENGTH(qp.query_plan) / 2 <= @limit THEN qp.query_plan END
FROM #plans AS p
JOIN sys.query_store_plan AS qp ON qp.plan_id = p.plan_id
ORDER BY p.rnk;
SELECT hour_utc, plan_id, runs, cpu, duration, reads FROM #hist ORDER BY hour_utc DESC, plan_id;
SELECT category, wait_ms FROM #waits ORDER BY wait_ms DESC;`)

// queryDetailCacheQuery reads the instance with @db, @hash, @objid (the procedure Query Store
// named, or 0) and @limit: the plans of the hash in the plan cache that belong to the database
// (sys.dm_exec_query_stats by query_hash, one row per plan_handle, at most 20, the most recent
// first) with the statement text and, for the first ten, the statement's plan XML unless over
// @limit characters; and when the query belongs to a procedure, every statement of that
// procedure from the plan cache, most reads first (InvestigateProcedure.sql, part 2). Both need
// VIEW SERVER STATE and say so. Result sets: one row (errors, object id and name, server name,
// version, server time, UTC offset), the cached plans, the statements.
var queryDetailCacheQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @dbid int = DB_ID(@db), @cacheErr nvarchar(2048), @stmtErr nvarchar(2048), @object nvarchar(300), @obj int = @objid;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';
CREATE TABLE #cache (plan_handle varbinary(64) NOT NULL, plan_hash binary(8) NULL, sql_handle varbinary(64) NULL, stmt_start int NOT NULL,
                     stmt_end int NOT NULL, runs bigint NOT NULL, cpu bigint NOT NULL, max_cpu bigint NOT NULL, duration bigint NOT NULL,
                     max_duration bigint NOT NULL, reads bigint NOT NULL, min_reads bigint NOT NULL, max_reads bigint NOT NULL,
                     created datetime NOT NULL, last_run datetime NOT NULL, rnk int NOT NULL, objectid int NULL, stmt nvarchar(max) NULL,
                     plan_chars bigint NULL, plan_text nvarchar(max) NULL);
CREATE TABLE #stmts (line int NULL, stmt nvarchar(4000) NULL, query_hash varchar(18) NULL, runs bigint NOT NULL, min_cpu bigint NOT NULL,
                     max_cpu bigint NOT NULL, cpu bigint NOT NULL, min_reads bigint NOT NULL, max_reads bigint NOT NULL, reads bigint NOT NULL,
                     created datetime NULL, last_run datetime NULL, conversion bit NOT NULL);

/* The plans of the hash in the plan cache, for this database */
IF @vss = 0 SET @cacheErr = @noVss;
ELSE
BEGIN TRY
    SET @cacheErr = NULL;
    WITH s AS (
        SELECT qs.plan_handle, qs.query_plan_hash, qs.sql_handle, qs.statement_start_offset, qs.statement_end_offset,
               ROW_NUMBER() OVER (PARTITION BY qs.plan_handle ORDER BY qs.total_worker_time DESC) AS rn,
               SUM(qs.execution_count) OVER (PARTITION BY qs.plan_handle) AS runs,
               SUM(qs.total_worker_time) OVER (PARTITION BY qs.plan_handle) AS cpu,
               MAX(qs.max_worker_time) OVER (PARTITION BY qs.plan_handle) AS max_cpu,
               SUM(qs.total_elapsed_time) OVER (PARTITION BY qs.plan_handle) AS duration,
               MAX(qs.max_elapsed_time) OVER (PARTITION BY qs.plan_handle) AS max_duration,
               SUM(qs.total_logical_reads) OVER (PARTITION BY qs.plan_handle) AS reads,
               MIN(qs.min_logical_reads) OVER (PARTITION BY qs.plan_handle) AS min_reads,
               MAX(qs.max_logical_reads) OVER (PARTITION BY qs.plan_handle) AS max_reads,
               MIN(qs.creation_time) OVER (PARTITION BY qs.plan_handle) AS created,
               MAX(qs.last_execution_time) OVER (PARTITION BY qs.plan_handle) AS last_run
        FROM sys.dm_exec_query_stats AS qs
        WHERE qs.query_hash = @hash)
    INSERT #cache (plan_handle, plan_hash, sql_handle, stmt_start, stmt_end, runs, cpu, max_cpu, duration, max_duration, reads, min_reads, max_reads,
                   created, last_run, rnk)
    SELECT TOP (20) s.plan_handle, s.query_plan_hash, s.sql_handle, s.statement_start_offset, s.statement_end_offset, s.runs, s.cpu, s.max_cpu,
           s.duration, s.max_duration, s.reads, s.min_reads, s.max_reads, s.created, s.last_run,
           ROW_NUMBER() OVER (ORDER BY s.last_run DESC)
    FROM s
    CROSS APPLY (SELECT CONVERT(int, pa.value) AS dbid FROM sys.dm_exec_plan_attributes(s.plan_handle) AS pa WHERE pa.attribute = N'dbid') AS pa
    WHERE s.rn = 1 AND pa.dbid = @dbid
    ORDER BY s.last_run DESC;

    UPDATE #cache SET objectid = st.objectid,
                      stmt = SUBSTRING(st.text, #cache.stmt_start / 2 + 1,
                                 (CASE #cache.stmt_end WHEN -1 THEN DATALENGTH(st.text) ELSE #cache.stmt_end END - #cache.stmt_start) / 2 + 1)
    FROM #cache
    CROSS APPLY sys.dm_exec_sql_text(#cache.sql_handle) AS st;

    UPDATE #cache SET plan_chars = DATALENGTH(tp.PlanText) / 2,
                      plan_text = CASE WHEN DATALENGTH(tp.PlanText) / 2 <= @limit THEN tp.PlanText END
    FROM #cache
    CROSS APPLY (SELECT CONVERT(nvarchar(max), x.query_plan) AS PlanText
                 FROM sys.dm_exec_text_query_plan(#cache.plan_handle, #cache.stmt_start, #cache.stmt_end) AS x) AS tp
    WHERE #cache.rnk <= 10;
END TRY
BEGIN CATCH
    SET @cacheErr = ERROR_MESSAGE();
END CATCH;

/* Every statement of the procedure, most reads first (InvestigateProcedure.sql, part 2) */
IF @obj IS NULL OR @obj <= 0
    SELECT TOP (1) @obj = objectid FROM #cache WHERE objectid IS NOT NULL ORDER BY rnk;
IF @obj > 0
BEGIN
    SET @object = OBJECT_SCHEMA_NAME(@obj, @dbid) + N'.' + OBJECT_NAME(@obj, @dbid);
    IF @vss = 0 SET @stmtErr = @noVss;
    ELSE
    BEGIN TRY
        INSERT #stmts (line, stmt, query_hash, runs, min_cpu, max_cpu, cpu, min_reads, max_reads, reads, created, last_run, conversion)
        SELECT TOP (100)
               LEN(LEFT(st.text, qs.statement_start_offset / 2)) - LEN(REPLACE(LEFT(st.text, qs.statement_start_offset / 2), NCHAR(10), N'')) + 1,
               LEFT(LTRIM(REPLACE(REPLACE(REPLACE(x.stmt, NCHAR(13), N' '), NCHAR(10), N' '), NCHAR(9), N' ')), 4000),
               CONVERT(varchar(18), qs.query_hash, 1), qs.execution_count, qs.min_worker_time, qs.max_worker_time, qs.total_worker_time,
               qs.min_logical_reads, qs.max_logical_reads, qs.total_logical_reads, qs.creation_time, qs.last_execution_time,
               CASE WHEN p.PlanText LIKE N'%ConvertIssue="Seek Plan"%' THEN 1 ELSE 0 END
        FROM sys.dm_exec_query_stats AS qs
        CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) AS st
        CROSS APPLY (SELECT SUBSTRING(st.text, qs.statement_start_offset / 2 + 1,
                         (CASE qs.statement_end_offset WHEN -1 THEN DATALENGTH(st.text) ELSE qs.statement_end_offset END
                          - qs.statement_start_offset) / 2 + 1) AS stmt) AS x
        OUTER APPLY (SELECT CONVERT(nvarchar(max), tp.query_plan) AS PlanText
                     FROM sys.dm_exec_text_query_plan(qs.plan_handle, qs.statement_start_offset, qs.statement_end_offset) AS tp) AS p
        WHERE st.objectid = @obj AND st.dbid = @dbid
        ORDER BY qs.total_logical_reads DESC;
    END TRY
    BEGIN CATCH
        SET @stmtErr = ERROR_MESSAGE();
    END CATCH;
END;

SELECT @cacheErr, @stmtErr, ISNULL(@obj, 0), @object, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), GETDATE(), DATEPART(TZOFFSET, SYSDATETIMEOFFSET());
SELECT CONVERT(varchar(130), plan_handle, 1), CONVERT(varchar(18), plan_hash, 1), runs, cpu, max_cpu, duration, max_duration, reads, min_reads,
       max_reads, created, last_run, stmt, ISNULL(plan_chars, 0), plan_text
FROM #cache ORDER BY rnk;
SELECT line, stmt, query_hash, runs, min_cpu, max_cpu, cpu, min_reads, max_reads, reads, created, last_run, conversion
FROM #stmts ORDER BY reads DESC;`)

// queryTablesRows is where queryTablesQuery reads a table's rows: sys.dm_db_partition_stats
// (VIEW DATABASE STATE), or sys.partitions when the login may not read it.
const (
	queryTablesRowsDMV        = "SELECT SUM(ps.row_count) AS rows FROM sys.dm_db_partition_stats AS ps WHERE ps.object_id = o.id AND ps.index_id IN (0, 1)"
	queryTablesRowsPartitions = "SELECT SUM(ps.rows) AS rows FROM sys.partitions AS ps WHERE ps.object_id = o.id AND ps.index_id IN (0, 1)"
)

// queryTablesInsert fills @t with each named table's rows and statistics (sys.stats with
// sys.dm_db_stats_properties: how many, the oldest update, the highest share of rows changed);
// {rows} is one of queryTablesRows.
const queryTablesInsert = `INSERT @t (schema_name, table_name, object_id, rows, stats, oldest, max_mod_pct, max_mod)
        SELECT n.schema_name, n.table_name, o.id, r.rows, s.stats, s.oldest, s.max_mod_pct, s.max_mod
        FROM @names AS n
        CROSS APPLY (SELECT OBJECT_ID(QUOTENAME(n.schema_name) + N'.' + QUOTENAME(n.table_name), N'U') AS id) AS o
        OUTER APPLY ({rows}) AS r
        OUTER APPLY (SELECT COUNT(st.stats_id) AS stats, MIN(sp.last_updated) AS oldest,
                            MAX(sp.modification_counter * 100.0 / NULLIF(sp.rows, 0)) AS max_mod_pct, MAX(sp.modification_counter) AS max_mod
                     FROM sys.stats AS st
                     OUTER APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
                     WHERE st.object_id = o.id) AS s;`

// queryTablesQuery runs inside the database with @tables, an XML list <t s="schema" n="table"/>:
// per table whether it exists, its rows and its statistics' age; and each statistics object of
// those tables updated in the last 8 days (phase 5j: what changed), with whether it is an index's
// and how many rows it read. Result sets: one row (error, server time), the tables, the updates.
var queryTablesQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @err nvarchar(2048), @now datetime = GETDATE();
DECLARE @names TABLE (schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL);
DECLARE @t TABLE (schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL, object_id int NULL, rows bigint NULL, stats int NULL,
                  oldest datetime NULL, max_mod_pct float NULL, max_mod bigint NULL);
DECLARE @u TABLE (schema_name nvarchar(128) NOT NULL, table_name nvarchar(128) NOT NULL, stats_name nvarchar(128) NOT NULL, is_index bit NOT NULL,
                  updated datetime NOT NULL, rows bigint NULL, sampled bigint NULL);
BEGIN TRY
    INSERT @names (schema_name, table_name)
    SELECT DISTINCT x.t.value('@s', 'nvarchar(128)'), x.t.value('@n', 'nvarchar(128)')
    FROM (SELECT CONVERT(xml, @tables) AS doc) AS d
    CROSS APPLY d.doc.nodes('/t') AS x(t);
    IF HAS_PERMS_BY_NAME(DB_NAME(), N'DATABASE', N'VIEW DATABASE STATE') = 1
        ` + strings.ReplaceAll(queryTablesInsert, "{rows}", queryTablesRowsDMV) + `
    ELSE
        ` + strings.ReplaceAll(queryTablesInsert, "{rows}", queryTablesRowsPartitions) + `
    INSERT @u (schema_name, table_name, stats_name, is_index, updated, rows, sampled)
    SELECT t.schema_name, t.table_name, st.name, CASE WHEN i.index_id IS NULL THEN 0 ELSE 1 END, sp.last_updated, sp.rows, sp.rows_sampled
    FROM @t AS t
    JOIN sys.stats AS st ON st.object_id = t.object_id
    LEFT JOIN sys.indexes AS i ON i.object_id = st.object_id AND i.index_id = st.stats_id AND st.auto_created = 0 AND st.user_created = 0
    CROSS APPLY sys.dm_db_stats_properties(st.object_id, st.stats_id) AS sp
    WHERE sp.last_updated >= DATEADD(DAY, -8, @now);
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @err, @now;
SELECT schema_name, table_name, CASE WHEN object_id IS NULL THEN 0 ELSE 1 END, ISNULL(rows, 0), ISNULL(stats, 0), oldest, ISNULL(max_mod_pct, 0),
       ISNULL(max_mod, 0)
FROM @t ORDER BY schema_name, table_name;
SELECT schema_name, table_name, stats_name, is_index, updated, ISNULL(rows, 0), ISNULL(sampled, 0)
FROM @u ORDER BY updated, schema_name, table_name, stats_name;`)

// planForcingQuery runs inside the database with @plan: Query Store's state, the plan's query and
// hash, and every plan of that query with its forcing and its runs and duration over 7 days, for
// the force-plan and unforce-plan previews and their read-back. Result sets: one row (state,
// error, found, query id, query hash), the plans.
var planForcingQuery = TagStatements(`SET NOCOUNT ON;
DECLARE @state nvarchar(60), @err nvarchar(2048), @found bit = 0, @qid bigint, @hash varchar(18),
        @since datetimeoffset = DATEADD(DAY, -7, SYSDATETIMEOFFSET());
CREATE TABLE #fp (plan_id bigint NOT NULL PRIMARY KEY, query_id bigint NOT NULL, forced bit NOT NULL, last_run datetimeoffset NULL,
                  force_failures bigint NULL, force_reason nvarchar(128) NULL, runs bigint NULL, duration bigint NULL);
BEGIN TRY
    SELECT @state = CONVERT(nvarchar(60), actual_state_desc) FROM sys.database_query_store_options;
    SELECT @found = 1, @qid = p.query_id, @hash = CONVERT(varchar(18), q.query_hash, 1)
    FROM sys.query_store_plan AS p
    JOIN sys.query_store_query AS q ON q.query_id = p.query_id
    WHERE p.plan_id = @plan;
    IF @found = 1
    BEGIN
        INSERT #fp (plan_id, query_id, forced, last_run, force_failures, force_reason)
        SELECT TOP (200) p.plan_id, p.query_id, p.is_forced_plan, p.last_execution_time, p.force_failure_count,
               CONVERT(nvarchar(128), p.last_force_failure_reason_desc)
        FROM sys.query_store_plan AS p
        WHERE p.query_id = @qid
        ORDER BY p.last_execution_time DESC;

        UPDATE #fp SET runs = r.runs, duration = r.duration
        FROM #fp
        CROSS APPLY (SELECT SUM(rs.count_executions) AS runs, CONVERT(bigint, SUM(rs.avg_duration * rs.count_executions)) AS duration
                     FROM sys.query_store_runtime_stats AS rs
                     JOIN sys.query_store_runtime_stats_interval AS i ON i.runtime_stats_interval_id = rs.runtime_stats_interval_id
                     WHERE rs.plan_id = #fp.plan_id AND i.start_time >= @since) AS r;
    END;
END TRY
BEGIN CATCH
    SET @err = ERROR_MESSAGE();
END CATCH;
SELECT @state, @err, @found, ISNULL(@qid, 0), @hash;
SELECT plan_id, query_id, forced, last_run, ISNULL(force_failures, 0), force_reason, ISNULL(runs, 0), ISNULL(duration, 0)
FROM #fp ORDER BY plan_id;`)

// cachedPlanQuery reads one plan handle in the plan cache (instance, @handle): whether it is
// there, its type, use count and size, when its statements were cached, and its text and
// procedure, for the evict-plan preview and its read-back. Needs VIEW SERVER STATE.
var cachedPlanQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @found bit = 0, @objtype nvarchar(20), @uses bigint, @kb bigint, @created datetime, @text nvarchar(400), @object nvarchar(300);
SELECT @found = 1, @objtype = CONVERT(nvarchar(20), cp.objtype), @uses = cp.usecounts, @kb = cp.size_in_bytes / 1024
FROM sys.dm_exec_cached_plans AS cp
WHERE cp.plan_handle = @handle;
IF @found = 1
BEGIN
    SELECT @created = MIN(qs.creation_time) FROM sys.dm_exec_query_stats AS qs WHERE qs.plan_handle = @handle;
    SELECT @text = LEFT(st.text, 400),
           @object = CASE WHEN st.objectid IS NOT NULL THEN OBJECT_SCHEMA_NAME(st.objectid, st.dbid) + N'.' + OBJECT_NAME(st.objectid, st.dbid) END
    FROM sys.dm_exec_sql_text(@handle) AS st;
END;
SELECT @found, @objtype, ISNULL(@uses, 0), ISNULL(@kb, 0), @created, @text, @object;`)

// queryReadTimeout bounds each batch of the query page.
const queryReadTimeout = 60 * time.Second

// QueryDetail reads everything the query page shows about one query hash in one database: Query
// Store (inside the database), the plan cache and the procedure's statements (the instance),
// then the tables the plans touch (inside the database). An area that cannot be read carries its
// reason in the facts; the error is for a hash that is not one, or an instance that cannot be
// read at all.
func QueryDetail(ctx context.Context, db *sql.DB, database, hash string) (domain.QueryFacts, error) {
	norm, raw, err := domain.ParseQueryHash(hash)
	if err != nil {
		return domain.QueryFacts{}, err
	}
	f := domain.QueryFacts{Database: database, QueryHash: norm}

	readCtx, cancel := context.WithTimeout(ctx, queryReadTimeout)
	objID, objName, err := readQueryStoreDetail(readCtx, db, database, raw, &f)
	cancel()
	if err != nil {
		f.QueryStoreError = err.Error()
	}

	readCtx, cancel = context.WithTimeout(ctx, queryReadTimeout)
	err = readPlanCacheDetail(readCtx, db, database, raw, objID, &f)
	cancel()
	if err != nil {
		return f, err
	}
	if objName != "" {
		f.ObjectName = objName
	}

	plans := []string{}
	for _, p := range f.Plans {
		if p.Plan != "" {
			plans = append(plans, p.Plan)
		}
	}
	for _, c := range f.CachedPlans {
		if c.Plan != "" {
			plans = append(plans, c.Plan)
		}
	}
	if tables := domain.ShowplanTables(database, plans...); len(tables) > 0 {
		readCtx, cancel = context.WithTimeout(ctx, queryReadTimeout)
		facts, _, err := ReadTableStatistics(readCtx, db, database, tables)
		cancel()
		if err != nil {
			f.TablesError = err.Error()
		} else {
			f.Tables = facts
		}
	}
	return f, nil
}

func readQueryStoreDetail(ctx context.Context, db *sql.DB, database string, hash []byte, f *domain.QueryFacts) (int, string, error) {
	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt, N'@hash binary(8), @limit bigint', @hash = @hash, @limit = @limit;")
	rows, err := db.QueryContext(ctx, q, sql.Named("stmt", queryDetailStoreQuery), sql.Named("hash", hash), sql.Named("limit", int64(planLimitChars)))
	if err != nil {
		return 0, "", friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, "", friendlyError(err)
		}
		return 0, "", errors.New("SQL Server returned nothing from Query Store")
	}
	var state, qsErr, histErr, waitErr, text, object sql.NullString
	var objID, compat sql.NullInt32
	if err := rows.Scan(&state, &qsErr, &histErr, &waitErr, &text, &objID, &object, &compat); err != nil {
		return 0, "", err
	}
	f.QueryStoreState, f.QueryStoreError, f.HistoryError, f.WaitsError = state.String, qsErr.String, histErr.String, waitErr.String
	f.Text = text.String

	if err := nextResultSet(rows, "Query Store plans"); err != nil {
		return 0, "", err
	}
	f.Plans = []domain.QueryStorePlanFact{}
	for rows.Next() {
		var p domain.QueryStorePlanFact
		var initial, compiled, last sql.NullTime
		var engine, planHash, reason, plan sql.NullString
		var compatLevel sql.NullInt32
		if err := rows.Scan(&p.PlanID, &p.QueryID, &p.Forced, &initial, &compiled, &last, &engine, &compatLevel, &planHash,
			&p.ForceFailures, &reason, &p.Runs, &p.CPU, &p.MaxCPU, &p.Duration, &p.MaxDuration, &p.Reads, &p.MaxReads, &p.TempdbKB,
			&p.PlanChars, &plan); err != nil {
			return 0, "", err
		}
		p.InitialCompile, p.LastCompile, p.LastRun = utcPtr(initial), utcPtr(compiled), utcPtr(last)
		p.EngineVersion, p.Compat, p.PlanHash, p.ForceFailureReason, p.Plan = engine.String, int(compatLevel.Int32), planHash.String, reason.String, plan.String
		f.Plans = append(f.Plans, p)
	}

	if err := nextResultSet(rows, "Query Store history"); err != nil {
		return 0, "", err
	}
	f.History = []domain.QueryHourFact{}
	for rows.Next() {
		var h domain.QueryHourFact
		if err := rows.Scan(&h.Hour, &h.PlanID, &h.Runs, &h.CPU, &h.Duration, &h.Reads); err != nil {
			return 0, "", err
		}
		h.Hour = naive(h.Hour)
		f.History = append(f.History, h)
	}

	if err := nextResultSet(rows, "Query Store waits"); err != nil {
		return 0, "", err
	}
	f.Waits = []domain.QueryWaitFact{}
	for rows.Next() {
		var w domain.QueryWaitFact
		if err := rows.Scan(&w.Category, &w.WaitMs); err != nil {
			return 0, "", err
		}
		f.Waits = append(f.Waits, w)
	}
	if err := rows.Err(); err != nil {
		return 0, "", friendlyError(err)
	}
	return int(objID.Int32), object.String, nil
}

func readPlanCacheDetail(ctx context.Context, db *sql.DB, database string, hash []byte, objID int, f *domain.QueryFacts) error {
	rows, err := db.QueryContext(ctx, queryDetailCacheQuery, sql.Named("db", database), sql.Named("hash", hash),
		sql.Named("objid", objID), sql.Named("limit", int64(planLimitChars)))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned nothing from the plan cache")
	}
	var cacheErr, stmtErr, object sql.NullString
	var obj int
	var now time.Time
	var offset int
	if err := rows.Scan(&cacheErr, &stmtErr, &obj, &object, &f.ServerName, &f.ProductVersion, &now, &offset); err != nil {
		return fmt.Errorf("read the plan cache of %s: %w", database, err)
	}
	f.PlanCacheError, f.StatementsError = cacheErr.String, stmtErr.String
	f.UTCOffsetMinutes = offset
	f.ServerNow = serverToUTC(now, offset)
	if f.ObjectName == "" {
		f.ObjectName = object.String
	}

	if err := nextResultSet(rows, "cached plans"); err != nil {
		return err
	}
	f.CachedPlans = []domain.CachedPlanFact{}
	for rows.Next() {
		var c domain.CachedPlanFact
		var handle, planHash, stmt, plan sql.NullString
		var created, last time.Time
		if err := rows.Scan(&handle, &planHash, &c.Runs, &c.CPU, &c.MaxCPU, &c.Duration, &c.MaxDuration, &c.Reads, &c.MinReads, &c.MaxReads,
			&created, &last, &stmt, &c.PlanChars, &plan); err != nil {
			return err
		}
		c.PlanHandle, c.PlanHash, c.Text, c.Plan = handle.String, planHash.String, stmt.String, plan.String
		c.Created, c.LastRun = serverToUTC(created, offset), serverToUTC(last, offset)
		f.CachedPlans = append(f.CachedPlans, c)
	}

	if err := nextResultSet(rows, "procedure statements"); err != nil {
		return err
	}
	f.Statements = []domain.ProcStatementFact{}
	for rows.Next() {
		var s domain.ProcStatementFact
		var line sql.NullInt32
		var stmt, qh sql.NullString
		var created, last sql.NullTime
		if err := rows.Scan(&line, &stmt, &qh, &s.Runs, &s.MinCPU, &s.MaxCPU, &s.CPU, &s.MinReads, &s.MaxReads, &s.Reads,
			&created, &last, &s.Conversion); err != nil {
			return err
		}
		s.Line, s.Text, s.QueryHash = int(line.Int32), stmt.String, qh.String
		if created.Valid {
			s.Created = serverToUTC(created.Time, offset)
		}
		if last.Valid {
			s.LastRun = serverToUTC(last.Time, offset)
		}
		f.Statements = append(f.Statements, s)
	}
	return friendlyErrOrNil(rows.Err())
}

// serverToUTC turns a SQL Server datetime (the server's clock, no zone) into UTC with the
// server's offset in minutes.
func serverToUTC(t time.Time, offsetMinutes int) time.Time {
	return naive(t).Add(-time.Duration(offsetMinutes) * time.Minute)
}

// utcPtr is a datetimeoffset (or nothing) in UTC.
func utcPtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

// tablesXML writes the tables as queryTablesQuery's @tables: <t s="schema" n="table"/>.
func tablesXML(tables []domain.ShowplanObject) string {
	var b strings.Builder
	for _, t := range tables {
		b.WriteString(`<t s="`)
		_ = xml.EscapeText(&b, []byte(t.Schema))
		b.WriteString(`" n="`)
		_ = xml.EscapeText(&b, []byte(t.Table))
		b.WriteString(`"/>`)
	}
	return b.String()
}

// ReadTableStatistics reads the rows and statistics age of tables inside a database, and the
// server's clock (SQL Server's datetime, kept naive) when it read them. SELECTs only.
func ReadTableStatistics(ctx context.Context, db *sql.DB, database string, tables []domain.ShowplanObject) ([]domain.QueryTableFact, time.Time, error) {
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@tables nvarchar(max)', @tables = @tables;"),
		sql.Named("stmt", queryTablesQuery), sql.Named("tables", tablesXML(tables)))
	if err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, time.Time{}, friendlyError(err)
		}
		return nil, time.Time{}, errors.New("SQL Server returned nothing about the tables")
	}
	var tErr sql.NullString
	var now time.Time
	if err := rows.Scan(&tErr, &now); err != nil {
		return nil, time.Time{}, err
	}
	if err := nextResultSet(rows, "tables"); err != nil {
		return nil, time.Time{}, err
	}
	out := []domain.QueryTableFact{}
	for rows.Next() {
		var t domain.QueryTableFact
		var oldest sql.NullTime
		if err := rows.Scan(&t.Schema, &t.Table, &t.Found, &t.Rows, &t.Stats, &oldest, &t.MaxModPct, &t.Modifications); err != nil {
			return nil, time.Time{}, err
		}
		t.OldestUpdate = naivePtr(oldest)
		t.Updates = []domain.QueryStatUpdateFact{}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	if err := nextResultSet(rows, "statistics updates"); err != nil {
		return nil, time.Time{}, err
	}
	for rows.Next() {
		var schema, table string
		var u domain.QueryStatUpdateFact
		if err := rows.Scan(&schema, &table, &u.Name, &u.Index, &u.Updated, &u.Rows, &u.Sampled); err != nil {
			return nil, time.Time{}, err
		}
		u.Updated = naive(u.Updated)
		for i := range out {
			if out[i].Schema == schema && out[i].Table == table {
				out[i].Updates = append(out[i].Updates, u)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	if tErr.String != "" {
		return nil, time.Time{}, errors.New(tErr.String)
	}
	return out, naive(now), nil
}

// ReadTableStats reads one table for the update-statistics-table fix: its preview and read-back.
func ReadTableStats(ctx context.Context, db *sql.DB, database, schema, table string) (domain.TableStatsFacts, error) {
	facts, now, err := ReadTableStatistics(ctx, db, database, []domain.ShowplanObject{{Schema: schema, Table: table}})
	if err != nil {
		return domain.TableStatsFacts{}, err
	}
	f := domain.TableStatsFacts{Database: database, Schema: schema, Table: table, ServerNow: now}
	for _, t := range facts {
		if strings.EqualFold(t.Schema, schema) && strings.EqualFold(t.Table, table) {
			f.Found, f.Rows, f.Stats, f.OldestUpdate, f.MaxModPct = t.Found, t.Rows, t.Stats, t.OldestUpdate, t.MaxModPct
		}
	}
	return f, nil
}

// ReadPlanForcing reads a plan's forcing and its query's plans inside a database, for force-plan
// and unforce-plan. SELECTs only. Query Store that cannot be read is an error.
func ReadPlanForcing(ctx context.Context, db *sql.DB, database string, planID int64) (domain.PlanForceFacts, error) {
	f := domain.PlanForceFacts{Database: database, PlanID: planID, Plans: []domain.QueryStorePlanFact{}}
	rows, err := db.QueryContext(ctx, Tag(safeSession+"EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt, N'@plan bigint', @plan = @plan;"),
		sql.Named("stmt", planForcingQuery), sql.Named("plan", planID))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing from Query Store")
	}
	var state, qsErr, hash sql.NullString
	if err := rows.Scan(&state, &qsErr, &f.Found, &f.QueryID, &hash); err != nil {
		return f, err
	}
	f.QueryStoreState, f.QueryHash = state.String, hash.String
	if err := nextResultSet(rows, "plans"); err != nil {
		return f, err
	}
	for rows.Next() {
		var p domain.QueryStorePlanFact
		var last sql.NullTime
		var reason sql.NullString
		if err := rows.Scan(&p.PlanID, &p.QueryID, &p.Forced, &last, &p.ForceFailures, &reason, &p.Runs, &p.Duration); err != nil {
			return f, err
		}
		p.LastRun, p.ForceFailureReason = utcPtr(last), reason.String
		f.Plans = append(f.Plans, p)
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	if qsErr.String != "" {
		return f, errors.New(qsErr.String)
	}
	return f, nil
}

// ReadCachedPlan reads one plan handle in the plan cache, for evict-plan. SELECTs only.
func ReadCachedPlan(ctx context.Context, db *sql.DB, handle string, raw []byte) (domain.CachedPlanFacts, error) {
	f := domain.CachedPlanFacts{Handle: handle}
	var objType, text, object sql.NullString
	var created sql.NullTime
	err := db.QueryRowContext(ctx, cachedPlanQuery, sql.Named("handle", raw)).
		Scan(&f.Found, &objType, &f.UseCount, &f.SizeKB, &created, &text, &object)
	if err != nil {
		return f, friendlyError(err)
	}
	f.ObjectType, f.Text, f.ObjectName, f.Created = objType.String, text.String, object.String, naivePtr(created)
	return f, nil
}
