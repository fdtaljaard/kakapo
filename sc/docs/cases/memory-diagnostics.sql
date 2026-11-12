/*
  memory-diagnostics.sql  -  where is SQL Server memory going, and is it abnormal?

  A hand-run, read-only companion to the Memory screen. Run it in SSMS or sqlcmd as one batch (do not
  split it: the variables below live for the whole batch). For sections 5 and 6 connect to the
  database that holds the table first (the sections look at the current database); sections 1 to 4
  do not care where you are connected. Needs VIEW SERVER STATE (VIEW SERVER PERFORMANCE STATE on
  SQL Server 2022); section 2 also needs the right to read the error log (securityadmin or
  EXECUTE on xp_readerrorlog) and ALTER TRACE for the default trace. A section whose right is missing
  prints one line and the rest carries on. SQL Server 2016 SP1 or later.

  Nothing here writes to a user database. Section 3 waits @SampleSeconds between two readings, and
  sections 6 and 6b read the whole buffer pool or sample the table (6b uses tempdb for its sample):
  on a large cache these two can take minutes, so run them last and off peak.

  Every result set starts with a "section" column so pasted results stay readable.
*/
SET NOCOUNT ON;
SET QUOTED_IDENTIFIER ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 5000;

-- ---- Edit these -----------------------------------------------------------------------------------
DECLARE @Table         nvarchar(256) = N'dbo.[Transaction]';  -- the table sections 5 and 6b look at (schema.table)
DECLARE @DaysBack      int           = 60;                    -- how far back section 2 looks
DECLARE @SampleSeconds int           = 10;                    -- gap between the two counter readings in section 3
DECLARE @TopN          int           = 30;                    -- rows kept in the "biggest" lists
-- ---------------------------------------------------------------------------------------------------

DECLARE @since  datetime  = DATEADD(DAY, -@DaysBack, GETDATE());
DECLARE @tableId int      = OBJECT_ID(@Table);
DECLARE @tableName sysname = PARSENAME(@Table, 1);
DECLARE @delay  char(8)   = CONVERT(char(8), DATEADD(SECOND, @SampleSeconds, CAST('00:00:00' AS time)), 108);
DECLARE @msg    nvarchar(4000);

SELECT section = N'0 Run', run_at = GETDATE(), server_name = @@SERVERNAME, current_database = DB_NAME(),
       table_checked = @Table, table_found = CASE WHEN @tableId IS NULL THEN N'NO: sections 5 and 6b will say so' ELSE N'yes' END;

/* =============================== 1  Windows and SQL Server memory =============================== */

-- 1a  How much RAM Windows has, how much is free, and whether Windows says it is under pressure.
SELECT section = N'1a Windows memory',
       total_gb     = CAST(total_physical_memory_kb / 1048576.0 AS decimal(12,1)),
       available_gb = CAST(available_physical_memory_kb / 1048576.0 AS decimal(12,1)),
       available_pct = CAST(100.0 * available_physical_memory_kb / NULLIF(total_physical_memory_kb, 0) AS decimal(5,1)),
       pagefile_total_gb = CAST(total_page_file_kb / 1048576.0 AS decimal(12,1)),
       pagefile_available_gb = CAST(available_page_file_kb / 1048576.0 AS decimal(12,1)),
       system_memory_state = system_memory_state_desc,
       worry = CASE WHEN available_physical_memory_kb < 2097152 OR system_memory_state_desc LIKE N'%low%' THEN N'Windows is short of memory' ELSE N'ok' END
FROM sys.dm_os_sys_memory;

-- 1b  What SQL Server holds, its target, since when it has run, and the memory model (Lock Pages in Memory shows here).
SELECT section = N'1b SQL Server memory',
       sql_server_start   = i.sqlserver_start_time,
       uptime_days        = DATEDIFF(DAY, i.sqlserver_start_time, GETDATE()),
       physical_ram_gb    = CAST(i.physical_memory_kb / 1048576.0 AS decimal(12,1)),
       committed_gb       = CAST(i.committed_kb / 1048576.0 AS decimal(12,1)),
       target_gb          = CAST(i.committed_target_kb / 1048576.0 AS decimal(12,1)),
       process_in_use_gb  = CAST(p.physical_memory_in_use_kb / 1048576.0 AS decimal(12,1)),
       locked_pages_gb    = CAST(p.locked_page_allocations_kb / 1048576.0 AS decimal(12,1)),
       large_pages_gb     = CAST(p.large_page_allocations_kb / 1048576.0 AS decimal(12,1)),
       memory_model       = i.sql_memory_model_desc,
       squeezed_by_windows = CASE WHEN p.process_physical_memory_low = 1 THEN N'YES: physical memory low' ELSE N'no' END,
       virtual_memory_low  = CASE WHEN p.process_virtual_memory_low = 1 THEN N'YES' ELSE N'no' END,
       note = CASE WHEN i.sql_memory_model_desc = N'LOCK_PAGES' THEN N'Lock Pages in Memory is on: Task Manager shows a small working set; trust process_in_use_gb' ELSE N'' END
FROM sys.dm_os_sys_info AS i
CROSS JOIN sys.dm_os_process_memory AS p;

-- 1c  The cap: max and min server memory, what is running now, and any setting changed but not yet activated.
SELECT section = N'1c Cap and pending changes',
       c.name,
       configured_mb = CAST(c.value AS bigint),
       in_use_mb     = CAST(c.value_in_use AS bigint),
       pending       = CASE WHEN CAST(c.value AS bigint) <> CAST(c.value_in_use AS bigint)
                             AND NOT (c.name = N'min server memory (MB)' AND CAST(c.value AS bigint) = 0 AND CAST(c.value_in_use AS bigint) = 16)   -- 0 is shown in use as 16: normal
                            THEN N'PENDING: run RECONFIGURE or someone forgot it' ELSE N'' END,
       worry = CASE WHEN c.name = N'max server memory (MB)' AND CAST(c.value_in_use AS bigint) >= 2147483647 THEN N'no cap (2147483647)'
                    WHEN c.name = N'max server memory (MB)' AND CAST(c.value_in_use AS bigint) > i.physical_memory_kb / 1024 THEN N'cap is above the RAM in the box'
                    WHEN c.name = N'max server memory (MB)' AND i.physical_memory_kb / 1024 - CAST(c.value_in_use AS bigint) < 2048 THEN N'leaves Windows under 2 GB'
                    ELSE N'' END
FROM sys.configurations AS c
CROSS JOIN sys.dm_os_sys_info AS i
WHERE c.name IN (N'max server memory (MB)', N'min server memory (MB)', N'optimize for ad hoc workloads');

-- 1d  Every configuration option whose value and value in use differ (pending, or waiting for a restart).
SELECT section = N'1d All pending configuration', c.name, value = CAST(c.value AS bigint), value_in_use = CAST(c.value_in_use AS bigint),
       is_dynamic = c.is_dynamic
FROM sys.configurations AS c
WHERE CAST(c.value AS bigint) <> CAST(c.value_in_use AS bigint)
  AND NOT (c.name = N'min server memory (MB)' AND CAST(c.value AS bigint) = 0 AND CAST(c.value_in_use AS bigint) = 16);   -- 0 is shown in use as 16: normal

-- 1e  Inside SQL Server: the biggest memory clerks, and the total they add up to (clerks should come close to committed memory).
SELECT TOP (15) section = N'1e Memory clerks',
       clerk = type,
       size_mb = CAST(SUM(CAST(pages_kb AS bigint)) / 1024 AS bigint),
       pct_of_clerks = CAST(100.0 * SUM(CAST(pages_kb AS bigint)) / NULLIF((SELECT SUM(CAST(pages_kb AS bigint)) FROM sys.dm_os_memory_clerks), 0) AS decimal(5,1))
FROM sys.dm_os_memory_clerks
GROUP BY type
ORDER BY SUM(CAST(pages_kb AS bigint)) DESC;

/* =============================== 2  Change history: is this new? =============================== */

-- 2a  Configuration changes in every retained error log ("Configuration option 'X' changed from A to B").
BEGIN TRY
    CREATE TABLE #logs (log_no int, last_changed datetime, size_bytes bigint);
    CREATE TABLE #cfg  (log_date datetime, process_info nvarchar(100), log_text nvarchar(4000));
    INSERT #logs EXEC sys.sp_enumerrorlogs;
    DECLARE @n int = (SELECT MIN(log_no) FROM #logs);
    WHILE @n IS NOT NULL
    BEGIN
        BEGIN TRY
            INSERT #cfg EXEC sys.xp_readerrorlog @n, 1, N'Configuration option', NULL, NULL, NULL, N'asc';
        END TRY
        BEGIN CATCH
            SET @msg = N'2a: error log ' + CAST(@n AS nvarchar(10)) + N' could not be read: ' + ERROR_MESSAGE();
            PRINT @msg;
        END CATCH;
        SET @n = (SELECT MIN(log_no) FROM #logs WHERE log_no > @n);
    END;
    SELECT section = N'2a Config changes (error logs)', log_date, log_text
    FROM #cfg
    WHERE log_text LIKE N'%changed from%'
    ORDER BY log_date DESC;
    SELECT section = N'2a Logs retained', logs = COUNT_BIG(*), oldest_log_ends = MIN(last_changed), newest = MAX(last_changed) FROM #logs;
    DROP TABLE #cfg;
    DROP TABLE #logs;
END TRY
BEGIN CATCH
    SET @msg = N'2a skipped (needs the right to read the error log): ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

-- 2b  Objects created, dropped or altered in the default trace (rolling window of a few files), with login, host and application.
BEGIN TRY
    DECLARE @trc nvarchar(520) = (SELECT TOP (1) path FROM sys.traces WHERE is_default = 1);
    IF @trc IS NULL
        PRINT N'2b: the default trace is off (sp_configure ''default trace enabled''), so there is no object history.';
    ELSE
    BEGIN
        SET @trc = LEFT(@trc, LEN(@trc) - PATINDEX(N'%\%', REVERSE(@trc))) + N'\log.trc';
        SELECT TOP (500) section = N'2b Default trace objects',
               t.StartTime, event = e.name, object_type = v.subclass_name, t.DatabaseName, t.ObjectName,
               t.LoginName, t.HostName, t.ApplicationName
        FROM sys.fn_trace_gettable(@trc, DEFAULT) AS t
        JOIN sys.trace_events AS e ON e.trace_event_id = t.EventClass
        LEFT JOIN sys.trace_subclass_values AS v ON v.trace_event_id = t.EventClass AND v.subclass_value = t.ObjectType AND v.subclass_name IS NOT NULL
        WHERE t.EventClass IN (46, 47, 164)               -- Object:Created, Object:Deleted, Object:Altered
          AND t.EventSubClass = 1                          -- commit only (no begin)
          AND t.StartTime >= @since
          AND t.DatabaseID > 4
          AND t.ObjectType IN (8272, 8277, 8278, 16964, 21587, 22601)   -- procedure, table, view, database, statistics, index
          AND t.ObjectType <> 21587                        -- statistics churn is noise
        ORDER BY t.StartTime DESC;
        SELECT section = N'2b Trace window', oldest_event = MIN(StartTime), newest_event = MAX(StartTime), events = COUNT_BIG(*)
        FROM sys.fn_trace_gettable(@trc, DEFAULT);
    END;
END TRY
BEGIN CATCH
    SET @msg = N'2b skipped (needs ALTER TRACE): ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

-- 2c  Global trace flags that are on now (1211 and 1224 change lock escalation; 3226, 1117, 1118 and others change behaviour).
BEGIN TRY
    CREATE TABLE #tf (TraceFlag int, Status int, Global int, Session int);
    INSERT #tf EXEC (N'DBCC TRACESTATUS(-1) WITH NO_INFOMSGS');
    SELECT section = N'2c Trace flags', TraceFlag, Status, Global, Session FROM #tf WHERE Global = 1 OR Session = 1 ORDER BY TraceFlag;
    IF NOT EXISTS (SELECT 1 FROM #tf) PRINT N'2c: no trace flags are on.';
    DROP TABLE #tf;
END TRY
BEGIN CATCH
    SET @msg = N'2c skipped: ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

-- 2d  Recovery model history: every backup records the model in force, so a change shows as two values for one database.
BEGIN TRY
    SELECT section = N'2d Recovery model per database', database_name, recovery_model,
           backups = COUNT_BIG(*), first_seen = MIN(backup_start_date), last_seen = MAX(backup_start_date)
    FROM msdb.dbo.backupset
    WHERE backup_start_date >= @since
    GROUP BY database_name, recovery_model
    ORDER BY database_name, MIN(backup_start_date);
    SELECT section = N'2d Recovery model now', d.name, d.recovery_model_desc,
           models_seen_in_period = (SELECT COUNT_BIG(DISTINCT b.recovery_model) FROM msdb.dbo.backupset AS b
                                    WHERE b.database_name = d.name COLLATE DATABASE_DEFAULT AND b.backup_start_date >= @since)
    FROM sys.databases AS d
    ORDER BY d.name;
END TRY
BEGIN CATCH
    SET @msg = N'2d skipped: ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

-- 2e  Agent jobs created or changed in the period (backups, CHECKDB, index and statistics jobs, shrink steps).
BEGIN TRY
    SELECT section = N'2e Agent jobs changed', j.name, j.enabled, j.date_created, j.date_modified, owner = SUSER_SNAME(j.owner_sid),
           has_shrink_step = CASE WHEN EXISTS (SELECT 1 FROM msdb.dbo.sysjobsteps AS s
                                               WHERE s.job_id = j.job_id AND (s.command LIKE N'%SHRINKDATABASE%' OR s.command LIKE N'%SHRINKFILE%'))
                                  THEN N'YES' ELSE N'' END
    FROM msdb.dbo.sysjobs AS j
    WHERE j.date_modified >= @since
    ORDER BY j.date_modified DESC;
END TRY
BEGIN CATCH
    SET @msg = N'2e skipped: ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

-- 2f  Objects in this database that kakapo left (indexes and objects with kakapo in the name, the archive schema); run in each database it touched.
SELECT section = N'2f kakapo objects here', o.create_date, o.modify_date, schema_name = s.name, object_name = o.name, o.type_desc, index_name = i.name
FROM sys.objects AS o
JOIN sys.schemas AS s ON s.schema_id = o.schema_id
LEFT JOIN sys.indexes AS i ON i.object_id = o.object_id AND i.name LIKE N'%kakapo%'
WHERE i.name IS NOT NULL OR s.name = N'archive' OR o.name LIKE N'%kakapo%'
ORDER BY o.modify_date DESC;

/* =============================== 3  Pressure: is memory short right now? =============================== */

-- 3a  Page life expectancy per NUMA node (the instance-wide value hides a node under pressure); compare with history, not a fixed number.
SELECT section = N'3a Page life expectancy',
       scope = CASE WHEN object_name LIKE N'%Buffer Node%' THEN N'node ' + RTRIM(instance_name) ELSE N'whole instance' END,
       ple_seconds = cntr_value
FROM sys.dm_os_performance_counters
WHERE counter_name = N'Page life expectancy'
  AND (object_name LIKE N'%Buffer Node%' OR object_name LIKE N'%Buffer Manager%')
ORDER BY object_name, instance_name;

-- 3b  Lazy writes, free list stalls and page reads per second, from two readings @SampleSeconds apart (the counters are cumulative).
DECLARE @first TABLE (counter_name nvarchar(128), cntr_value bigint);
DECLARE @t1 datetime2 = SYSDATETIME();
INSERT @first
SELECT RTRIM(counter_name) COLLATE DATABASE_DEFAULT, cntr_value
FROM sys.dm_os_performance_counters
WHERE object_name LIKE N'%Buffer Manager%' AND counter_name IN (N'Lazy writes/sec', N'Free list stalls/sec', N'Page reads/sec', N'Page writes/sec', N'Checkpoint pages/sec');
WAITFOR DELAY @delay;
DECLARE @secs decimal(9,3) = DATEDIFF(MILLISECOND, @t1, SYSDATETIME()) / 1000.0;
SELECT section = N'3b Buffer rates (sampled)', counter_name = RTRIM(b.counter_name),
       per_second = CAST((b.cntr_value - a.cntr_value) / @secs AS decimal(14,2)),
       sampled_seconds = @secs,
       worry = CASE WHEN b.counter_name = N'Lazy writes/sec' AND (b.cntr_value - a.cntr_value) / @secs > 20 THEN N'above about 20/s sustained'
                    WHEN b.counter_name = N'Free list stalls/sec' AND b.cntr_value - a.cntr_value > 0 THEN N'any sustained value is a worry'
                    ELSE N'' END
FROM sys.dm_os_performance_counters AS b
JOIN @first AS a ON a.counter_name = RTRIM(b.counter_name) COLLATE DATABASE_DEFAULT
WHERE b.object_name LIKE N'%Buffer Manager%'
ORDER BY b.counter_name;

-- 3c  Query workspace memory: grants pending (should be 0), outstanding, and the totals SQL Server is working with.
SELECT section = N'3c Memory manager counters', counter_name = RTRIM(counter_name), value = cntr_value,
       worry = CASE WHEN counter_name = N'Memory Grants Pending' AND cntr_value > 0 THEN N'queries are waiting for memory now' ELSE N'' END
FROM sys.dm_os_performance_counters
WHERE object_name LIKE N'%Memory Manager%'
  AND counter_name IN (N'Memory Grants Pending', N'Memory Grants Outstanding', N'Total Server Memory (KB)', N'Target Server Memory (KB)',
                       N'Granted Workspace Memory (KB)', N'Lock Memory (KB)', N'Optimizer Memory (KB)', N'SQL Cache Memory (KB)', N'Connection Memory (KB)')
ORDER BY counter_name;

-- 3d  Queries holding or waiting for a memory grant right now (grant_time empty means still waiting).
SELECT TOP (@TopN) section = N'3d Memory grants now', g.session_id, g.request_time, g.grant_time,
       requested_mb = g.requested_memory_kb / 1024, granted_mb = g.granted_memory_kb / 1024, used_mb = g.used_memory_kb / 1024,
       g.wait_time_ms, g.dop, g.query_cost, waiting = CASE WHEN g.grant_time IS NULL THEN N'WAITING' ELSE N'' END,
       s.login_name, s.host_name, s.program_name
FROM sys.dm_exec_query_memory_grants AS g
LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = g.session_id
ORDER BY CASE WHEN g.grant_time IS NULL THEN 0 ELSE 1 END, g.requested_memory_kb DESC;

-- 3e  RESOURCE_SEMAPHORE waits since the restart (queries waited for a memory grant); compare with uptime and rerun later to see if they rise.
SELECT section = N'3e Grant waits since restart', wait_type, waiting_tasks = waiting_tasks_count,
       wait_hours = CAST(wait_time_ms / 3600000.0 AS decimal(12,2)),
       avg_wait_ms = CASE WHEN waiting_tasks_count = 0 THEN 0 ELSE wait_time_ms / waiting_tasks_count END,
       since = (SELECT sqlserver_start_time FROM sys.dm_os_sys_info)
FROM sys.dm_os_wait_stats
WHERE wait_type IN (N'RESOURCE_SEMAPHORE', N'RESOURCE_SEMAPHORE_QUERY_COMPILE');

/* ================================ 4  Lock memory holders and tempdb ================================ */

-- 4a  Lock memory by session: lock counts per session (about 96 bytes each, so the MB is an estimate); the clerk total is the real number.
SELECT section = N'4a Lock memory clerk',
       lock_manager_mb = CAST(SUM(CAST(pages_kb AS bigint)) / 1024 AS bigint),
       worry = CASE WHEN SUM(CAST(pages_kb AS bigint)) / 1024 > 1024 THEN N'over 1 GB: find the holder below' ELSE N'' END
FROM sys.dm_os_memory_clerks
WHERE type = N'OBJECTSTORE_LOCK_MANAGER';

SELECT TOP (@TopN) section = N'4a Locks by session', l.request_session_id AS session_id,
       locks = COUNT_BIG(*), est_mb = CAST(COUNT_BIG(*) * 96 / 1048576.0 AS decimal(12,1)),
       databases = COUNT_BIG(DISTINCT l.resource_database_id),
       s.login_name, s.host_name, s.program_name, s.status, s.last_request_start_time
FROM sys.dm_tran_locks AS l
LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = l.request_session_id
GROUP BY l.request_session_id, s.login_name, s.host_name, s.program_name, s.status, s.last_request_start_time
ORDER BY COUNT_BIG(*) DESC;

-- 4b  The oldest open transactions: what holds locks, blocks log truncation and keeps the version store from cleaning.
SELECT TOP (@TopN) section = N'4b Oldest open transactions', st.session_id, at.transaction_id, at.transaction_begin_time,
       open_minutes = DATEDIFF(MINUTE, at.transaction_begin_time, GETDATE()),
       at.transaction_state, s.status, s.login_name, s.host_name, s.program_name, s.last_request_end_time,
       log_used_mb = CAST(ISNULL(dt.database_transaction_log_bytes_used, 0) / 1048576.0 AS decimal(12,1))
FROM sys.dm_tran_active_transactions AS at
JOIN sys.dm_tran_session_transactions AS st ON st.transaction_id = at.transaction_id
LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = st.session_id
OUTER APPLY (SELECT SUM(CAST(d.database_transaction_log_bytes_used AS bigint)) AS database_transaction_log_bytes_used
             FROM sys.dm_tran_database_transactions AS d WHERE d.transaction_id = at.transaction_id) AS dt
WHERE st.is_user_transaction = 1
ORDER BY at.transaction_begin_time;

-- 4c  tempdb space by use: version store, user objects, internal objects (sorts, spills, hashes), and what is free.
SELECT section = N'4c tempdb space',
       data_files_mb       = CAST(SUM(CAST(total_page_count AS bigint)) * 8 / 1024 AS bigint),
       version_store_mb    = CAST(SUM(CAST(version_store_reserved_page_count AS bigint)) * 8 / 1024 AS bigint),
       user_objects_mb     = CAST(SUM(CAST(user_object_reserved_page_count AS bigint)) * 8 / 1024 AS bigint),
       internal_objects_mb = CAST(SUM(CAST(internal_object_reserved_page_count AS bigint)) * 8 / 1024 AS bigint),
       free_mb             = CAST(SUM(CAST(unallocated_extent_page_count AS bigint)) * 8 / 1024 AS bigint)
FROM tempdb.sys.dm_db_file_space_usage;

-- 4d  Who uses tempdb: sessions with the most user (temp tables) and internal (spill) allocation now.
SELECT TOP (@TopN) section = N'4d tempdb by session', u.session_id,
       user_objects_mb     = CAST((SUM(CAST(u.user_objects_alloc_page_count AS bigint)) - SUM(CAST(u.user_objects_dealloc_page_count AS bigint))) * 8 / 1024 AS bigint),
       internal_objects_mb = CAST((SUM(CAST(u.internal_objects_alloc_page_count AS bigint)) - SUM(CAST(u.internal_objects_dealloc_page_count AS bigint))) * 8 / 1024 AS bigint),
       s.login_name, s.host_name, s.program_name, s.status
FROM sys.dm_db_session_space_usage AS u
LEFT JOIN sys.dm_exec_sessions AS s ON s.session_id = u.session_id
GROUP BY u.session_id, s.login_name, s.host_name, s.program_name, s.status
HAVING SUM(CAST(u.user_objects_alloc_page_count AS bigint)) + SUM(CAST(u.internal_objects_alloc_page_count AS bigint)) > 0
ORDER BY SUM(CAST(u.user_objects_alloc_page_count AS bigint)) + SUM(CAST(u.internal_objects_alloc_page_count AS bigint)) DESC;

-- 4e  Version store by database, and the oldest snapshot transaction that stops it from cleaning up (RCSI, snapshot isolation).
BEGIN TRY
    SELECT section = N'4e Version store by database', DB_NAME(database_id) AS database_name,
           version_store_mb = CAST(SUM(CAST(reserved_page_count AS bigint)) * 8 / 1024 AS bigint)
    FROM sys.dm_tran_version_store_space_usage
    GROUP BY database_id
    ORDER BY SUM(CAST(reserved_page_count AS bigint)) DESC;
    SELECT TOP (5) section = N'4e Oldest snapshot transactions', session_id, transaction_id, elapsed_time_seconds, is_snapshot
    FROM sys.dm_tran_active_snapshot_database_transactions
    ORDER BY elapsed_time_seconds DESC;
END TRY
BEGIN CATCH
    SET @msg = N'4e skipped (needs VIEW SERVER STATE and SQL Server 2017 or later): ' + ERROR_MESSAGE();
    PRINT @msg;
END CATCH;

/* ==================== 5  One table: its indexes, and who reads it (current database) ==================== */

-- 5a  The table's indexes with key and INCLUDE columns, size, compression and usage; look for overlapping keys and near-copies.
IF @tableId IS NULL
    PRINT N'5a, 5b and 6b skipped: table ' + @Table + N' was not found in ' + DB_NAME() + N'. Connect to the right database or fix @Table.';
ELSE
BEGIN
    SELECT section = N'5a Indexes of table', index_id = i.index_id, index_name = ISNULL(i.name, N'(heap)'), i.type_desc,
           i.is_unique, i.is_primary_key, i.is_disabled, i.filter_definition,
           key_columns = STUFF((SELECT N', ' + c.name + CASE WHEN ic.is_descending_key = 1 THEN N' DESC' ELSE N'' END
                                FROM sys.index_columns AS ic JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
                                WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 0 AND ic.key_ordinal > 0
                                ORDER BY ic.key_ordinal FOR XML PATH(N''), TYPE).value(N'.', N'nvarchar(max)'), 1, 2, N''),
           include_columns = STUFF((SELECT N', ' + c.name
                                FROM sys.index_columns AS ic JOIN sys.columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
                                WHERE ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.is_included_column = 1
                                ORDER BY c.column_id FOR XML PATH(N''), TYPE).value(N'.', N'nvarchar(max)'), 1, 2, N''),
           size_mb = CAST(ps.used_pages * 8 / 1024.0 AS decimal(14,1)),
           row_count = ps.row_count,
           compression = ISNULL(pc.compression, N'NONE'),
           user_seeks = ISNULL(us.user_seeks, 0), user_scans = ISNULL(us.user_scans, 0), user_lookups = ISNULL(us.user_lookups, 0),
           user_updates = ISNULL(us.user_updates, 0),
           last_read = (SELECT MAX(v) FROM (VALUES (us.last_user_seek), (us.last_user_scan), (us.last_user_lookup)) x(v) WHERE v IS NOT NULL),
           note = CASE WHEN us.index_id IS NULL OR (ISNULL(us.user_seeks,0) + ISNULL(us.user_scans,0) + ISNULL(us.user_lookups,0) = 0 AND ISNULL(us.user_updates,0) > 0)
                       THEN N'not read since restart (costs writes)' ELSE N'' END
    FROM sys.indexes AS i
    OUTER APPLY (SELECT used_pages = SUM(CAST(p.used_page_count AS bigint)), row_count = SUM(CASE WHEN p.index_id IN (0, 1) THEN CAST(p.row_count AS bigint) ELSE 0 END)
                 FROM sys.dm_db_partition_stats AS p WHERE p.object_id = i.object_id AND p.index_id = i.index_id) AS ps
    OUTER APPLY (SELECT TOP (1) compression = p.data_compression_desc FROM sys.partitions AS p WHERE p.object_id = i.object_id AND p.index_id = i.index_id) AS pc
    LEFT JOIN sys.dm_db_index_usage_stats AS us ON us.database_id = DB_ID() AND us.object_id = i.object_id AND us.index_id = i.index_id
    WHERE i.object_id = @tableId
    ORDER BY i.index_id;

    -- 5a  How long usage counters have been collecting (they reset at restart): read "not read" with that in mind.
    SELECT section = N'5a Usage counted since', sql_server_start = sqlserver_start_time, uptime_days = DATEDIFF(DAY, sqlserver_start_time, GETDATE()) FROM sys.dm_os_sys_info;
END;

-- 5b  Top readers of the table from Query Store: the queries whose text names the table, ranked by logical reads (8 KB pages) over the last @DaysBack days.
IF @tableId IS NOT NULL
BEGIN
    DECLARE @qsState nvarchar(60);
    BEGIN TRY
        EXEC sp_executesql N'SELECT @s = actual_state_desc FROM sys.database_query_store_options', N'@s nvarchar(60) OUTPUT', @s = @qsState OUTPUT;
    END TRY
    BEGIN CATCH
        SET @qsState = NULL;
    END CATCH;
    IF @qsState IS NULL OR @qsState = N'OFF'
        PRINT N'5b: Query Store is off in ' + DB_NAME() + N' (or unreadable). 5c below reads the plan cache instead.';
    ELSE
    BEGIN
        IF @qsState <> N'READ_WRITE' PRINT N'5b: Query Store is ' + @qsState + N' here: it shows history but is not collecting.';
        EXEC sp_executesql N'
        SELECT TOP (@TopN) section = N''5b Top readers (Query Store)'', q.query_id,
               logical_reads = SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint)),
               executions    = SUM(CAST(rs.count_executions AS bigint)),
               avg_reads     = SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint)) / NULLIF(SUM(CAST(rs.count_executions AS bigint)), 0),
               cpu_ms        = SUM(CAST(rs.avg_cpu_time * rs.count_executions / 1000.0 AS bigint)),
               plans         = COUNT_BIG(DISTINCT p.plan_id),
               object_name   = OBJECT_NAME(q.object_id),
               sample_text   = LEFT(REPLACE(REPLACE(qt.query_sql_text, CHAR(13), N'' ''), CHAR(10), N'' ''), 300)
        FROM sys.query_store_query_text AS qt
        JOIN sys.query_store_query AS q ON q.query_text_id = qt.query_text_id
        JOIN sys.query_store_plan AS p ON p.query_id = q.query_id
        JOIN sys.query_store_runtime_stats AS rs ON rs.plan_id = p.plan_id
        JOIN sys.query_store_runtime_stats_interval AS ri ON ri.runtime_stats_interval_id = rs.runtime_stats_interval_id
        WHERE qt.query_sql_text LIKE N''%'' + @name + N''%''
          AND ri.start_time >= @sinceUtc
        GROUP BY q.query_id, q.object_id, qt.query_sql_text
        ORDER BY SUM(CAST(rs.avg_logical_io_reads * rs.count_executions AS bigint)) DESC;',
        N'@TopN int, @name sysname, @sinceUtc datetimeoffset', @TopN = @TopN, @name = @tableName, @sinceUtc = @since;
    END;
END;

-- 5c  Fallback when Query Store is off: the cached plans whose text names the table, by total logical reads since the plan was cached (not joined to sessions).
IF @tableId IS NOT NULL AND (@qsState IS NULL OR @qsState = N'OFF')
BEGIN
    SELECT TOP (@TopN) section = N'5c Top readers (plan cache)',
           logical_reads = qs.total_logical_reads, executions = qs.execution_count,
           avg_reads = qs.total_logical_reads / NULLIF(qs.execution_count, 0),
           qs.creation_time, qs.last_execution_time,
           sample_text = LEFT(REPLACE(REPLACE(st.text, CHAR(13), N' '), CHAR(10), N' '), 300)
    FROM sys.dm_exec_query_stats AS qs
    CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) AS st
    WHERE st.text LIKE N'%' + @tableName + N'%' AND st.text NOT LIKE N'%dm_exec_query_stats%'
    ORDER BY qs.total_logical_reads DESC;
END;

/* ============== 6  The buffer pool: by database, by index (hot, warm, cold), and compression ============== */

-- 6  Buffer pool by database: one pass over sys.dm_os_buffer_descriptors, grouped by database only (the cheap view); can take a while on a large cache.
SELECT database_id, pages = COUNT_BIG(*), modified_pages = SUM(CASE WHEN is_modified = 1 THEN CAST(1 AS bigint) ELSE 0 END)
INTO #bp
FROM sys.dm_os_buffer_descriptors
GROUP BY database_id;

DECLARE @bpTotal bigint = (SELECT SUM(pages) FROM #bp);
DECLARE @bpCounter bigint = (SELECT cntr_value FROM sys.dm_os_performance_counters
                             WHERE object_name LIKE N'%Buffer Manager%' AND counter_name = N'Database pages');
SELECT section = N'6 Buffer pool by database',
       database_name = CASE database_id WHEN 32767 THEN N'(resource database)' ELSE ISNULL(DB_NAME(database_id), N'(id ' + CAST(database_id AS nvarchar(10)) + N')') END,
       cached_gb = CAST(pages * 8 / 1048576.0 AS decimal(12,2)),
       pct_of_cache = CAST(100.0 * pages / NULLIF(@bpTotal, 0) AS decimal(5,1)),
       modified_mb = modified_pages * 8 / 1024
FROM #bp
ORDER BY pages DESC;
SELECT section = N'6 Reconcile to Buffer Manager', descriptors_gb = CAST(@bpTotal * 8 / 1048576.0 AS decimal(12,2)),
       buffer_manager_database_pages_gb = CAST(@bpCounter * 8 / 1048576.0 AS decimal(12,2)),
       gap_pct = CAST(100.0 * (@bpTotal - @bpCounter) / NULLIF(@bpCounter, 0) AS decimal(7,2)),
       note = N'the two should agree within a few percent; a bigger gap means the cache changed during the scan';
DROP TABLE #bp;

-- 6a  Cache by index in the CURRENT database, with hot/warm/cold from index usage (hot: read in the last hour, warm: in 24 h, cold: not in 24 h).
--    One seek marks a whole index read, so "hot" overstates real use and "cold" is the number to trust. Usage resets at restart: give the server a day of uptime first.
IF DB_ID() <= 4
    PRINT N'6a: connect to a user database for the per-index view; ' + DB_NAME() + N' is a system database.';
ELSE
BEGIN
    SELECT p.object_id, p.index_id, cached_pages = COUNT_BIG(*)
    INTO #cache
    FROM sys.dm_os_buffer_descriptors AS bd
    JOIN sys.allocation_units AS au ON au.allocation_unit_id = bd.allocation_unit_id
    JOIN sys.partitions AS p ON (au.type IN (1, 3) AND au.container_id = p.hobt_id) OR (au.type = 2 AND au.container_id = p.partition_id)
    WHERE bd.database_id = DB_ID()
    GROUP BY p.object_id, p.index_id;

    SELECT c.object_id, c.index_id, table_name = OBJECT_SCHEMA_NAME(c.object_id) + N'.' + OBJECT_NAME(c.object_id),
           index_name = ISNULL(i.name, N'(heap)'),
           cached_pages = c.cached_pages,
           cached_mb = CAST(c.cached_pages * 8 / 1024.0 AS decimal(14,1)),
           lr.last_read,
           reads_since_restart = ISNULL(CAST(us.user_seeks AS bigint), 0) + ISNULL(us.user_scans, 0) + ISNULL(us.user_lookups, 0),
           temperature = CASE WHEN lr.last_read >= DATEADD(HOUR, -1, GETDATE())  THEN N'1 Hot: read in last hour'
                              WHEN lr.last_read >= DATEADD(HOUR, -24, GETDATE()) THEN N'2 Warm: read in last 24 h'
                              ELSE N'3 Cold: not read in 24 h' END
    INTO #detail
    FROM #cache AS c
    JOIN sys.indexes AS i ON i.object_id = c.object_id AND i.index_id = c.index_id
    LEFT JOIN sys.dm_db_index_usage_stats AS us ON us.database_id = DB_ID() AND us.object_id = c.object_id AND us.index_id = c.index_id
    OUTER APPLY (SELECT MAX(v) AS last_read FROM (VALUES (us.last_user_seek), (us.last_user_scan), (us.last_user_lookup)) x(v) WHERE v IS NOT NULL) AS lr;

    -- 6a  The split for the whole database: how much of this database's cache is hot, warm and cold.
    SELECT section = N'6a Temperature of this database', temperature, cached_gb = CAST(SUM(cached_pages) * 8 / 1048576.0 AS decimal(12,2)), indexes = COUNT_BIG(*)
    FROM #detail GROUP BY temperature ORDER BY temperature;

    -- 6a  The biggest cache holders in this database with their temperature, and what share of the table's pages is cached (near 100% means scanned).
    SELECT TOP (@TopN) section = N'6a Biggest holders', d.table_name, d.index_name, d.cached_mb, d.last_read, d.reads_since_restart, d.temperature,
           index_mb = CAST(ps.used_pages * 8 / 1024.0 AS decimal(14,1)),
           pct_of_index_cached = CAST(100.0 * d.cached_pages / NULLIF(ps.used_pages, 0) AS decimal(6,1))
    FROM #detail AS d
    OUTER APPLY (SELECT used_pages = SUM(CAST(p.used_page_count AS bigint)) FROM sys.dm_db_partition_stats AS p
                 WHERE p.object_id = d.object_id AND p.index_id = d.index_id) AS ps
    ORDER BY d.cached_pages DESC;

    SELECT section = N'6a Reconcile to 6', indexes_gb = CAST(SUM(cached_pages) * 8 / 1048576.0 AS decimal(12,2)), note = N'compare with this database in section 6; the rest is internal and system pages'
    FROM #detail;
    DROP TABLE #detail;
    DROP TABLE #cache;
END;

-- 6b  PAGE compression estimate for the table: sampled by SQL Server (the sample uses tempdb, the table is untouched); slow on a big table.
IF @tableId IS NULL
    PRINT N'6b skipped: table ' + @Table + N' was not found in ' + DB_NAME() + N'.';
ELSE
BEGIN
    BEGIN TRY
        CREATE TABLE #est (object_name sysname, schema_name sysname, index_id int, partition_number int,
                           size_current_kb bigint, size_page_kb bigint, sample_current_kb bigint, sample_page_kb bigint);
        DECLARE @sch sysname = OBJECT_SCHEMA_NAME(@tableId), @tbl sysname = OBJECT_NAME(@tableId);
        INSERT #est EXEC sys.sp_estimate_data_compression_savings @schema_name = @sch, @object_name = @tbl,
                                                                  @index_id = NULL, @partition_number = NULL, @data_compression = N'PAGE';
        SELECT section = N'6b PAGE compression estimate', e.index_id, index_name = ISNULL(i.name, N'(heap)'),
               current_mb = CAST(SUM(e.size_current_kb) / 1024.0 AS decimal(14,1)), page_mb = CAST(SUM(e.size_page_kb) / 1024.0 AS decimal(14,1)),
               saved_mb = CAST((SUM(e.size_current_kb) - SUM(e.size_page_kb)) / 1024.0 AS decimal(14,1)),
               saved_pct = CAST(100.0 * (SUM(e.size_current_kb) - SUM(e.size_page_kb)) / NULLIF(SUM(e.size_current_kb), 0) AS decimal(5,1))
        FROM #est AS e
        LEFT JOIN sys.indexes AS i ON i.object_id = @tableId AND i.index_id = e.index_id
        GROUP BY e.index_id, i.name
        UNION ALL
        SELECT N'6b PAGE compression estimate', NULL, N'(all indexes)', CAST(SUM(size_current_kb) / 1024.0 AS decimal(14,1)), CAST(SUM(size_page_kb) / 1024.0 AS decimal(14,1)),
               CAST((SUM(size_current_kb) - SUM(size_page_kb)) / 1024.0 AS decimal(14,1)),
               CAST(100.0 * (SUM(size_current_kb) - SUM(size_page_kb)) / NULLIF(SUM(size_current_kb), 0) AS decimal(5,1))
        FROM #est
        ORDER BY index_id;
        DROP TABLE #est;
    END TRY
    BEGIN CATCH
        SET @msg = N'6b skipped: ' + ERROR_MESSAGE();
        PRINT @msg;
        IF OBJECT_ID(N'tempdb..#est') IS NOT NULL DROP TABLE #est;
    END CATCH;
END;

SELECT section = N'End', finished_at = GETDATE(), note = N'Nothing was changed.';
