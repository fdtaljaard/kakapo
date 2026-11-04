package collect

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// sampleSnapshotQuery is one snapshot of the live sample (docs/phase4c-contract.md, part 3;
// DESIGN.md 5.3), in one batch: the waits without the benign ones, the scheduler monitor ring
// buffer's latest records and the runnable tasks, the plan cache grouped by query hash, the I/O
// and size of this database's and tempdb's files, the head blockers, the long operations in
// progress, and page life expectancy with the memory grants pending. Every area is read in its own
// TRY, so a missing permission gives that area's error column, not a failed read. SELECTs only.
// Result sets: one row (server name, now, UTC offset, the errors, runnable tasks, schedulers,
// page life expectancy, grants pending), then the waits, the CPU records, the query stats, the
// files, the head blockers and the operations running.
//
// Source: DarlingData sp_PressureDetector/sp_PressureDetector.sql 3930-3960 (MIT): the scheduler
// monitor ring buffer XPaths. The delta itself (two snapshots, joined on their key, positive
// differences only) is sp_PressureDetector's @sample_seconds rule, computed in domain.BuildPerfSample.
var sampleSnapshotQuery = Tag(safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
DECLARE @dbid int = DB_ID(@db);
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';

/* Waits, the scan's benign list and SLEEP_% left out */
DECLARE @benign TABLE (wait_type nvarchar(60) PRIMARY KEY);
INSERT @benign (wait_type) VALUES ` + benignValues() + `;
DECLARE @waits TABLE (wait_type nvarchar(60) NOT NULL, wait_ms bigint NOT NULL, signal_ms bigint NOT NULL, tasks bigint NOT NULL, max_ms bigint NOT NULL);
DECLARE @waitsError nvarchar(2048);
IF @vss = 0 SET @waitsError = @noVss;
ELSE
BEGIN TRY
    INSERT @waits (wait_type, wait_ms, signal_ms, tasks, max_ms)
    SELECT ws.wait_type, ws.wait_time_ms, ws.signal_wait_time_ms, ws.waiting_tasks_count, ws.max_wait_time_ms
    FROM sys.dm_os_wait_stats AS ws
    WHERE ws.waiting_tasks_count > 0
      AND ws.wait_type NOT IN (SELECT b.wait_type FROM @benign AS b)
      AND ws.wait_type NOT LIKE N'SLEEP[_]%';
END TRY
BEGIN CATCH
    SET @waitsError = ERROR_MESSAGE();
END CATCH;

/* CPU: the scheduler monitor ring buffer's latest records (one a minute) and the runnable tasks now */
DECLARE @cpu TABLE (at datetime NOT NULL, sql_pct int NOT NULL, idle_pct int NOT NULL);
DECLARE @cpuError nvarchar(2048), @msTicks bigint, @runnable int, @schedulers int;
IF @vss = 0 SET @cpuError = @noVss;
ELSE
BEGIN TRY
    SELECT @msTicks = ms_ticks FROM sys.dm_os_sys_info;
    INSERT @cpu (at, sql_pct, idle_pct)
    SELECT TOP (5) DATEADD(SECOND, -CONVERT(int, (@msTicks - t.timestamp) / 1000), @now),
           ISNULL(t.record.value('(Record/SchedulerMonitorEvent/SystemHealth/ProcessUtilization)[1]', 'int'), 0),
           ISNULL(t.record.value('(Record/SchedulerMonitorEvent/SystemHealth/SystemIdle)[1]', 'int'), 0)
    FROM (SELECT rb.timestamp, CONVERT(xml, rb.record) AS record
          FROM sys.dm_os_ring_buffers AS rb
          WHERE rb.ring_buffer_type = N'RING_BUFFER_SCHEDULER_MONITOR' AND rb.record LIKE N'%<SystemHealth>%') AS t
    ORDER BY t.timestamp DESC;

    SELECT @runnable = SUM(dos.runnable_tasks_count), @schedulers = COUNT(*)
    FROM sys.dm_os_schedulers AS dos
    WHERE dos.status = N'VISIBLE ONLINE';
END TRY
BEGIN CATCH
    SET @cpuError = ERROR_MESSAGE();
END CATCH;

/* Top queries: the plan cache grouped by query hash, every shape (the delta picks the top) */
DECLARE @qs TABLE (query_hash binary(8) NOT NULL PRIMARY KEY, cpu bigint NOT NULL, execs bigint NOT NULL, reads bigint NOT NULL, elapsed bigint NOT NULL);
DECLARE @queriesError nvarchar(2048);
IF @vss = 0 SET @queriesError = @noVss;
ELSE
BEGIN TRY
    INSERT @qs (query_hash, cpu, execs, reads, elapsed)
    SELECT qs.query_hash, SUM(qs.total_worker_time), SUM(qs.execution_count), SUM(qs.total_logical_reads), SUM(qs.total_elapsed_time)
    FROM sys.dm_exec_query_stats AS qs
    WHERE qs.query_hash <> 0x0000000000000000
    GROUP BY qs.query_hash;
END TRY
BEGIN CATCH
    SET @queriesError = ERROR_MESSAGE();
END CATCH;

/* Files of this database and tempdb: I/O and size */
DECLARE @files TABLE (database_name nvarchar(128) NULL, file_id int NOT NULL, file_name nvarchar(128) NULL, type_desc nvarchar(60) NULL,
                      reads bigint NOT NULL, writes bigint NOT NULL, read_bytes bigint NOT NULL, write_bytes bigint NOT NULL,
                      read_stall bigint NOT NULL, write_stall bigint NOT NULL, size_bytes bigint NOT NULL);
DECLARE @filesError nvarchar(2048);
IF @vss = 0 SET @filesError = @noVss;
ELSE
BEGIN TRY
    INSERT @files (database_name, file_id, file_name, type_desc, reads, writes, read_bytes, write_bytes, read_stall, write_stall, size_bytes)
    SELECT DB_NAME(vfs.database_id), vfs.file_id, mf.name, mf.type_desc, vfs.num_of_reads, vfs.num_of_writes, vfs.num_of_bytes_read,
           vfs.num_of_bytes_written, vfs.io_stall_read_ms, vfs.io_stall_write_ms, vfs.size_on_disk_bytes
    FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS vfs
    JOIN sys.master_files AS mf ON mf.database_id = vfs.database_id AND mf.file_id = vfs.file_id
    WHERE vfs.database_id = 2 OR vfs.database_id = @dbid;
END TRY
BEGIN CATCH
    SET @filesError = ERROR_MESSAGE();
END CATCH;

/* Head blockers across the instance: they block others and are not blocked themselves (blocked only by their own
   parallel threads still counts as a head blocker) */
DECLARE @blockers TABLE (session_id int NOT NULL, blocked int NOT NULL, longest_sec bigint NOT NULL, database_name nvarchar(128) NULL,
                         login_name nvarchar(128) NULL, host_name nvarchar(128) NULL, program_name nvarchar(128) NULL,
                         status nvarchar(30) NULL, open_tran int NULL, last_stmt nvarchar(4000) NULL);
DECLARE @blockingError nvarchar(2048);
IF @vss = 0 SET @blockingError = @noVss;
ELSE
BEGIN TRY
    INSERT @blockers (session_id, blocked, longest_sec, database_name, login_name, host_name, program_name, status, open_tran, last_stmt)
    SELECT s.session_id, w.Blocked, w.LongestSec, w.DbName, s.login_name, s.host_name, s.program_name, s.status, s.open_transaction_count,
           LEFT(q.text, 4000)
    FROM (SELECT r.blocking_session_id AS Blocker, COUNT(*) AS Blocked, MAX(r.wait_time) / 1000 AS LongestSec,
                 MAX(DB_NAME(r.database_id)) AS DbName
          FROM sys.dm_exec_requests AS r
          WHERE r.blocking_session_id > 0 AND r.blocking_session_id <> r.session_id
          GROUP BY r.blocking_session_id) AS w
    JOIN sys.dm_exec_sessions AS s ON s.session_id = w.Blocker
    OUTER APPLY (SELECT TOP (1) t.text FROM sys.dm_exec_connections AS c CROSS APPLY sys.dm_exec_sql_text(c.most_recent_sql_handle) AS t
                 WHERE c.session_id = s.session_id) AS q
    WHERE NOT EXISTS (SELECT 1 FROM sys.dm_exec_requests AS r2
                      WHERE r2.session_id = w.Blocker AND r2.blocking_session_id > 0 AND r2.blocking_session_id <> r2.session_id);
END TRY
BEGIN CATCH
    SET @blockingError = ERROR_MESSAGE();
END CATCH;

/* Long operations in progress: backup, restore, integrity checks, index builds */
DECLARE @running TABLE (session_id int NOT NULL, command nvarchar(32) NOT NULL, database_name nvarchar(128) NULL, pct float NOT NULL,
                        elapsed_sec bigint NOT NULL, left_sec bigint NOT NULL);
DECLARE @runningError nvarchar(2048);
IF @vss = 0 SET @runningError = @noVss;
ELSE
BEGIN TRY
    INSERT @running (session_id, command, database_name, pct, elapsed_sec, left_sec)
    SELECT r.session_id, r.command, DB_NAME(r.database_id), r.percent_complete, r.total_elapsed_time / 1000, r.estimated_completion_time / 1000
    FROM sys.dm_exec_requests AS r
    WHERE r.session_id <> @@SPID
      AND (r.percent_complete > 0
           OR r.command LIKE N'BACKUP%' OR r.command LIKE N'RESTORE%' OR r.command LIKE N'DBCC%'
           OR r.command LIKE N'ALTER INDEX%' OR r.command LIKE N'CREATE INDEX%');
END TRY
BEGIN CATCH
    SET @runningError = ERROR_MESSAGE();
END CATCH;

/* Memory: page life expectancy and memory grants pending */
DECLARE @ple bigint, @pending bigint, @memoryError nvarchar(2048);
IF @vss = 0 SET @memoryError = @noVss;
ELSE
BEGIN TRY
    SELECT @ple     = MAX(CASE WHEN object_name LIKE N'%Buffer Manager%' AND counter_name = N'Page life expectancy' THEN cntr_value END),
           @pending = MAX(CASE WHEN object_name LIKE N'%Memory Manager%' AND counter_name = N'Memory Grants Pending' THEN cntr_value END)
    FROM sys.dm_os_performance_counters
    WHERE object_name LIKE N'%Buffer Manager%' OR object_name LIKE N'%Memory Manager%';
END TRY
BEGIN CATCH
    SET @memoryError = ERROR_MESSAGE();
END CATCH;

SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
       @waitsError, @cpuError, @queriesError, @filesError, @blockingError, @runningError, @memoryError,
       ISNULL(@runnable, 0), ISNULL(@schedulers, 0), ISNULL(@ple, -1), ISNULL(@pending, -1);
SELECT wait_type, wait_ms, signal_ms, tasks, max_ms FROM @waits;
SELECT at, sql_pct, idle_pct FROM @cpu ORDER BY at;
SELECT CONVERT(varchar(18), query_hash, 1), cpu, execs, reads, elapsed FROM @qs;
SELECT database_name, file_id, file_name, type_desc, reads, writes, read_bytes, write_bytes, read_stall, write_stall, size_bytes FROM @files;
SELECT session_id, blocked, longest_sec, database_name, login_name, host_name, program_name, status, open_tran, last_stmt
FROM @blockers ORDER BY longest_sec DESC, session_id;
SELECT session_id, command, database_name, pct, elapsed_sec, left_sec FROM @running ORDER BY session_id;`)

// queryHashPattern is a query hash as CONVERT(varchar(18), query_hash, 1) writes it.
var queryHashPattern = regexp.MustCompile(`^0x[0-9A-Fa-f]{1,16}$`)

// sampleTextQuery reads, for the given query hashes, the database, the procedure and the statement
// of the costliest cached statement of each, leaving out kakapo's own batches and the noise QRY01
// leaves out (a hash whose every statement is noise returns no row). The hashes go into the batch
// as binary literals after a strict check. SELECTs only.
func sampleTextQuery(hashes []string) (string, error) {
	vals := make([]string, 0, len(hashes))
	for _, h := range hashes {
		if !queryHashPattern.MatchString(h) {
			return "", fmt.Errorf("%q is not a query hash", h)
		}
		vals = append(vals, "("+h+")")
	}
	if len(vals) == 0 {
		return "", fmt.Errorf("no query hashes")
	}
	return Tag(safeSession + `SET NOCOUNT ON;
SELECT CONVERT(varchar(18), h.query_hash, 1), t.database_name, t.object_name, t.stmt
FROM (VALUES ` + strings.Join(vals, ", ") + `) AS h(query_hash)
CROSS APPLY (SELECT TOP (1) DB_NAME(CONVERT(int, pa.value)) AS database_name,
                    CASE WHEN st.objectid IS NOT NULL THEN OBJECT_SCHEMA_NAME(st.objectid, st.dbid) + N'.' + OBJECT_NAME(st.objectid, st.dbid) END AS object_name,
                    x.stmt
             FROM sys.dm_exec_query_stats AS qs
             CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) AS st
             CROSS APPLY (SELECT CONVERT(nvarchar(4000), SUBSTRING(st.text, qs.statement_start_offset / 2 + 1,
                                 (CASE qs.statement_end_offset WHEN -1 THEN DATALENGTH(st.text) ELSE qs.statement_end_offset END
                                  - qs.statement_start_offset) / 2 + 1)) AS stmt) AS x
             OUTER APPLY (SELECT a.value FROM sys.dm_exec_plan_attributes(qs.plan_handle) AS a WHERE a.attribute = N'dbid') AS pa
             WHERE qs.query_hash = CONVERT(binary(8), h.query_hash)
               AND ISNULL(st.text, N'') NOT LIKE N'%` + KakapoTag + `%'
               AND ` + noiseFilter("x.stmt") + `
             ORDER BY qs.total_worker_time DESC) AS t;`), nil
}

// Sample runs the live sample on one connection: a snapshot, a wait of seconds (cut short when
// ctx ends), a second snapshot, then the texts of the queries that used the most CPU in between.
// An error means a snapshot could not be read at all; an area that could not be read carries its
// reason in the snapshot instead.
func Sample(ctx context.Context, db *sql.DB, database string, seconds int) (domain.SampleInputs, error) {
	in := domain.SampleInputs{Database: database, Seconds: seconds, QueryTexts: map[string]domain.SampleQueryText{}}
	conn, err := db.Conn(ctx)
	if err != nil {
		return in, friendlyError(err)
	}
	defer conn.Close()

	if in.First, err = readSampleSnapshot(ctx, conn, database); err != nil {
		return in, err
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	select {
	case <-ctx.Done():
		timer.Stop()
		return in, ctx.Err()
	case <-timer.C:
	}
	if in.Second, err = readSampleSnapshot(ctx, conn, database); err != nil {
		return in, err
	}
	if in.First.QueriesError != "" || in.Second.QueriesError != "" {
		return in, nil
	}
	hashes := domain.TopQueryCandidates(in.First, in.Second, 15)
	if len(hashes) == 0 {
		return in, nil
	}
	texts, err := readSampleTexts(ctx, conn, hashes)
	if err != nil {
		in.Second.QueriesError = "could not read the query texts: " + err.Error()
		return in, nil
	}
	in.QueryTexts = texts
	return in, nil
}

func readSampleSnapshot(ctx context.Context, conn *sql.Conn, database string) (domain.SampleSnapshot, error) {
	var s domain.SampleSnapshot
	rows, err := conn.QueryContext(ctx, sampleSnapshotQuery, sql.Named("db", database))
	if err != nil {
		return s, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return s, friendlyError(err)
		}
		return s, fmt.Errorf("SQL Server returned no sample row")
	}
	var waitsErr, cpuErr, queriesErr, filesErr, blockingErr, runningErr, memoryErr sql.NullString
	var offset int
	if err := rows.Scan(&s.ServerName, &s.At, &offset, &waitsErr, &cpuErr, &queriesErr, &filesErr, &blockingErr, &runningErr, &memoryErr,
		&s.Runnable, &s.Schedulers, &s.PageLifeExpectancy, &s.GrantsPending); err != nil {
		return s, err
	}
	s.At, s.UTCOffsetMinutes = naive(s.At), offset
	s.WaitsError, s.CPUError, s.QueriesError, s.FilesError = waitsErr.String, cpuErr.String, queriesErr.String, filesErr.String
	s.BlockingError, s.RunningError, s.MemoryError = blockingErr.String, runningErr.String, memoryErr.String

	if err := nextResultSet(rows, "waits"); err != nil {
		return s, err
	}
	for rows.Next() {
		var w domain.WaitStat
		if err := rows.Scan(&w.Type, &w.WaitMs, &w.SignalMs, &w.Tasks, &w.MaxWaitMs); err != nil {
			return s, err
		}
		s.Waits = append(s.Waits, w)
	}

	if err := nextResultSet(rows, "CPU records"); err != nil {
		return s, err
	}
	for rows.Next() {
		var c domain.CPUSample
		if err := rows.Scan(&c.At, &c.SQL, &c.Idle); err != nil {
			return s, err
		}
		c.At = naive(c.At)
		c.Other = max(100-c.SQL-c.Idle, 0)
		s.CPURecords = append(s.CPURecords, c)
	}

	if err := nextResultSet(rows, "query stats"); err != nil {
		return s, err
	}
	for rows.Next() {
		var q domain.SampleQueryStats
		if err := rows.Scan(&q.QueryHash, &q.CPU, &q.Execs, &q.Reads, &q.Elapsed); err != nil {
			return s, err
		}
		s.Queries = append(s.Queries, q)
	}

	if err := nextResultSet(rows, "files"); err != nil {
		return s, err
	}
	for rows.Next() {
		var f domain.SampleFileStats
		var dbName, name, typ sql.NullString
		if err := rows.Scan(&dbName, &f.FileID, &name, &typ, &f.Reads, &f.Writes, &f.ReadBytes, &f.WriteBytes,
			&f.ReadStallMs, &f.WriteStallMs, &f.SizeBytes); err != nil {
			return s, err
		}
		f.Database, f.File, f.Type = dbName.String, name.String, typ.String
		s.Files = append(s.Files, f)
	}

	if err := nextResultSet(rows, "head blockers"); err != nil {
		return s, err
	}
	for rows.Next() {
		var b domain.Blocker
		var dbName, login, host, program, status, stmt sql.NullString
		var openTran sql.NullInt32
		if err := rows.Scan(&b.Session, &b.Blocked, &b.LongestSec, &dbName, &login, &host, &program, &status, &openTran, &stmt); err != nil {
			return s, err
		}
		b.Database, b.Login, b.Host, b.Program, b.Status = dbName.String, login.String, host.String, program.String, status.String
		b.OpenTran, b.LastStatement = int(openTran.Int32), stmt.String
		s.Blockers = append(s.Blockers, b)
	}

	if err := nextResultSet(rows, "operations running"); err != nil {
		return s, err
	}
	for rows.Next() {
		var r domain.RunningCommand
		var dbName sql.NullString
		if err := rows.Scan(&r.Session, &r.Command, &dbName, &r.PercentDone, &r.ElapsedSeconds, &r.LeftSeconds); err != nil {
			return s, err
		}
		r.Command, r.Database = strings.TrimSpace(r.Command), dbName.String
		s.Running = append(s.Running, r)
	}
	return s, friendlyErrOrNil(rows.Err())
}

func readSampleTexts(ctx context.Context, conn *sql.Conn, hashes []string) (map[string]domain.SampleQueryText, error) {
	q, err := sampleTextQuery(hashes)
	if err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := map[string]domain.SampleQueryText{}
	for rows.Next() {
		var hash string
		var dbName, object, stmt sql.NullString
		if err := rows.Scan(&hash, &dbName, &object, &stmt); err != nil {
			return nil, err
		}
		out[hash] = domain.SampleQueryText{Database: dbName.String, ObjectName: object.String, Text: strings.TrimSpace(stmt.String)}
	}
	return out, friendlyErrOrNil(rows.Err())
}
