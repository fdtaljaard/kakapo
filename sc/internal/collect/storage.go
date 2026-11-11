package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-sql/sqlexp"

	"sqlmonitor/internal/domain"
)

// storageInstanceQuery reads what the Storage card needs from the instance, in one batch
// (docs/phase3b-contract.md): this database's files from sys.master_files and tempdb's from
// tempdb.sys.database_files (its current sizes), the volumes under them, sys.databases, the
// edition, the CPUs and tempdb's fill and what fills it (TMP03), the sessions holding the most
// tempdb and the oldest snapshot transaction, the PAGELATCH waits against the uptime (TMP01) and
// this database's slowest autogrowth and tempdb's growth events from the default trace (STO02/STO03
// evidence, TMP02 and the tempdb-files fix, docs/phase4e-contract.md), with tempdb's startup sizes
// from sys.master_files. Each area that
// needs a permission the login may lack is read in its own TRY, so a login without VIEW SERVER
// STATE gets the reason in that area's error column, not a failed read. SELECTs only.
// The first result set is one row of facts, the second the files, the third the top tempdb
// sessions.
// Source: DarlingData sp_PressureDetector/sp_PressureDetector.sql 2716-2730 (MIT): tempdb pages per
// session as session plus task space usage, allocated minus deallocated.
const storageInstanceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
DECLARE @dbid int, @state nvarchar(60), @recovery nvarchar(60), @reuse nvarchar(60);
SELECT @dbid = d.database_id, @state = d.state_desc, @recovery = d.recovery_model_desc, @reuse = d.log_reuse_wait_desc
FROM sys.databases AS d WHERE d.name = @db;

DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';
DECLARE @filesError nvarchar(2048), @volError nvarchar(2048), @cpuError nvarchar(2048), @tempError nvarchar(2048);
DECLARE @cpu int, @sched int, @tAlloc bigint, @tFree bigint, @tLogPct decimal(5, 1), @tUser bigint, @tInternal bigint, @tVersion bigint;
DECLARE @tsessError nvarchar(2048), @snapSession int, @snapSecs bigint;
DECLARE @tsess TABLE (session_id int NOT NULL, login_name nvarchar(256) NULL, program_name nvarchar(256) NULL, mb decimal(18, 1) NOT NULL);
DECLARE @latchError nvarchar(2048), @latchMs bigint, @uptimeHours float;
DECLARE @growthError nvarchar(2048), @tracePath nvarchar(260), @slowMs bigint, @slowFile nvarchar(256), @slowAt datetime;
DECLARE @tgCount int, @tgSecs bigint, @tgLog int, @tgLast datetime, @tgSlowMs bigint, @tgSlowFile nvarchar(256), @tgSlowAt datetime;
DECLARE @grow TABLE (database_id int NOT NULL, event_class int NOT NULL, duration_us bigint NOT NULL, file_name nvarchar(256) NULL,
                     start_time datetime NOT NULL);
DECLARE @files TABLE (
    database_id int NOT NULL, file_id int NOT NULL, type_desc nvarchar(60) NOT NULL, name sysname NOT NULL,
    physical_name nvarchar(260) NOT NULL, size_mb bigint NOT NULL, growth bigint NOT NULL, is_percent_growth bit NOT NULL,
    max_size bigint NOT NULL, volume nvarchar(512) NULL, vol_total_mb bigint NULL, vol_free_mb bigint NULL, start_mb bigint NULL);

IF @dbid IS NOT NULL
BEGIN
    BEGIN TRY
        INSERT @files (database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size)
        SELECT mf.database_id, mf.file_id, mf.type_desc, mf.name, mf.physical_name, CONVERT(bigint, mf.size) * 8 / 1024,
               mf.growth, mf.is_percent_growth, mf.max_size
        FROM sys.master_files AS mf
        WHERE mf.database_id = @dbid AND mf.type IN (0, 1);
        IF @@ROWCOUNT = 0
            SET @filesError = N'sys.master_files shows no files of this database to this login (it needs VIEW ANY DEFINITION).';
    END TRY
    BEGIN CATCH
        SET @filesError = ERROR_MESSAGE();
    END CATCH;

    /* tempdb's current sizes and growth, which sys.master_files holds only as at startup; its
       startup size (what a restart recreates it at) from sys.master_files, when the login sees it */
    INSERT @files (database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size, start_mb)
    SELECT 2, df.file_id, df.type_desc, df.name, df.physical_name, CONVERT(bigint, df.size) * 8 / 1024,
           df.growth, df.is_percent_growth, df.max_size, CONVERT(bigint, smf.size) * 8 / 1024
    FROM tempdb.sys.database_files AS df
    LEFT JOIN sys.master_files AS smf ON smf.database_id = 2 AND smf.file_id = df.file_id
    WHERE df.type IN (0, 1);

    IF @vss = 0 SET @volError = @noVss;
    ELSE IF @state = N'ONLINE'
    BEGIN TRY
        UPDATE f SET volume = vs.volume_mount_point, vol_total_mb = vs.total_bytes / 1048576, vol_free_mb = vs.available_bytes / 1048576
        FROM @files AS f CROSS APPLY sys.dm_os_volume_stats(f.database_id, f.file_id) AS vs;
    END TRY
    BEGIN CATCH
        SET @volError = ERROR_MESSAGE();
    END CATCH;

    IF @vss = 0 SET @cpuError = @noVss;
    ELSE
    BEGIN TRY
        SELECT @cpu = cpu_count FROM sys.dm_os_sys_info;
        /* The CPUs SQL Server may use: an edition's limit (Express: one socket or four cores) or affinity leave the rest offline. */
        SELECT @sched = COUNT(*) FROM sys.dm_os_schedulers WHERE status = N'VISIBLE ONLINE';
    END TRY
    BEGIN CATCH
        SET @cpuError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        SELECT @tAlloc    = SUM(CONVERT(bigint, total_page_count)) * 8 / 1024,
               @tFree     = SUM(CONVERT(bigint, unallocated_extent_page_count)) * 8 / 1024,
               @tUser     = SUM(CONVERT(bigint, user_object_reserved_page_count)) * 8 / 1024,
               @tInternal = SUM(CONVERT(bigint, internal_object_reserved_page_count)) * 8 / 1024,
               @tVersion  = SUM(CONVERT(bigint, version_store_reserved_page_count)) * 8 / 1024
        FROM tempdb.sys.dm_db_file_space_usage;
        EXEC tempdb.sys.sp_executesql N'/* kakapo */ SELECT @p = CONVERT(decimal(5, 1), used_log_space_in_percent) FROM sys.dm_db_log_space_usage;',
             N'@p decimal(5, 1) OUTPUT', @p = @tLogPct OUTPUT;
    END TRY
    BEGIN CATCH
        SET @tempError = ERROR_MESSAGE();
    END CATCH;

    /* TMP03: the sessions holding the most tempdb pages now, and the oldest snapshot transaction
       (it keeps the version store from emptying) */
    IF @vss = 0 SET @tsessError = @noVss;
    ELSE
    BEGIN TRY
        INSERT @tsess (session_id, login_name, program_name, mb)
        SELECT TOP (3) u.session_id, s.login_name, s.program_name, SUM(u.pages) * 8 / 1024.0
        FROM (SELECT session_id, user_objects_alloc_page_count + internal_objects_alloc_page_count
                                 - user_objects_dealloc_page_count - internal_objects_dealloc_page_count AS pages
              FROM tempdb.sys.dm_db_session_space_usage
              UNION ALL
              SELECT session_id, user_objects_alloc_page_count + internal_objects_alloc_page_count
                                 - user_objects_dealloc_page_count - internal_objects_dealloc_page_count
              FROM tempdb.sys.dm_db_task_space_usage) AS u
        JOIN sys.dm_exec_sessions AS s ON s.session_id = u.session_id AND s.is_user_process = 1
        GROUP BY u.session_id, s.login_name, s.program_name
        HAVING SUM(u.pages) > 0
        ORDER BY SUM(u.pages) DESC;
        SELECT TOP (1) @snapSession = session_id, @snapSecs = elapsed_time_seconds
        FROM sys.dm_tran_active_snapshot_database_transactions
        ORDER BY elapsed_time_seconds DESC;
    END TRY
    BEGIN CATCH
        SET @tsessError = ERROR_MESSAGE();
    END CATCH;

    /* TMP01: PAGELATCH waits on allocation pages since the restart, against the uptime */
    IF @vss = 0 SET @latchError = @noVss;
    ELSE
    BEGIN TRY
        SELECT @latchMs = ISNULL(SUM(wait_time_ms), 0) FROM sys.dm_os_wait_stats
        WHERE wait_type IN (N'PAGELATCH_UP', N'PAGELATCH_EX');
        SELECT @uptimeHours = DATEDIFF(SECOND, sqlserver_start_time, @now) / 3600.0 FROM sys.dm_os_sys_info;
    END TRY
    BEGIN CATCH
        SET @latchError = ERROR_MESSAGE();
    END CATCH;

    /* STO02/STO03 evidence and TMP02: the autogrowth events of this database's files and of
       tempdb's in the default trace, 7 days, read once: this database's slowest, and tempdb's
       count, log growths (event 93; 92 is a data file), total time, last and slowest. Duration is
       in microseconds. */
    IF HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER TRACE') = 0 SET @growthError = N'the login does not have ALTER TRACE.';
    ELSE
    BEGIN TRY
        SELECT @tracePath = REVERSE(SUBSTRING(REVERSE(path), CHARINDEX(N'\', REVERSE(path)), 260)) + N'log.trc'
        FROM sys.traces WHERE is_default = 1 AND status = 1;
        IF @tracePath IS NULL SET @growthError = N'the default trace is off.';
        ELSE
        BEGIN
            INSERT @grow (database_id, event_class, duration_us, file_name, start_time)
            SELECT t.DatabaseID, t.EventClass, ISNULL(t.Duration, 0), t.FileName, t.StartTime
            FROM sys.fn_trace_gettable(@tracePath, DEFAULT) AS t
            WHERE t.EventClass IN (92, 93) AND t.DatabaseID IN (@dbid, 2) AND t.StartTime >= DATEADD(DAY, -7, @now);
            SELECT TOP (1) @slowMs = g.duration_us / 1000, @slowFile = g.file_name, @slowAt = g.start_time
            FROM @grow AS g WHERE g.database_id = @dbid
            ORDER BY g.duration_us DESC;
            SELECT @tgCount = COUNT(*), @tgSecs = ISNULL(SUM(g.duration_us), 0) / 1000000,
                   @tgLog = ISNULL(SUM(CASE WHEN g.event_class = 93 THEN 1 ELSE 0 END), 0), @tgLast = MAX(g.start_time)
            FROM @grow AS g WHERE g.database_id = 2;
            SELECT TOP (1) @tgSlowMs = g.duration_us / 1000, @tgSlowFile = g.file_name, @tgSlowAt = g.start_time
            FROM @grow AS g WHERE g.database_id = 2
            ORDER BY g.duration_us DESC;
        END;
    END TRY
    BEGIN CATCH
        SET @growthError = ERROR_MESSAGE();
    END CATCH;
END;

SELECT CAST(CASE WHEN @dbid IS NULL THEN 0 ELSE 1 END AS bit),
       @state, @recovery, @reuse,
       CONVERT(int, SERVERPROPERTY('EngineEdition')),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       @cpu, @sched, @tAlloc, @tFree, @tLogPct,
       @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
       @filesError, @volError, @cpuError, @tempError,
       @tUser, @tInternal, @tVersion, @snapSession, @snapSecs, @tsessError,
       @latchMs, @uptimeHours, @latchError,
       @slowMs, @slowFile, @slowAt, @growthError,
       @tgCount, @tgSecs, @tgLog, @tgLast, @tgSlowMs, @tgSlowFile, @tgSlowAt;

SELECT database_id, file_id, type_desc, name, physical_name, size_mb, growth, is_percent_growth, max_size,
       volume, vol_total_mb, vol_free_mb, start_mb
FROM @files
ORDER BY CASE WHEN database_id = 2 THEN 1 ELSE 0 END, file_id;

SELECT session_id, login_name, program_name, mb FROM @tsess ORDER BY mb DESC;`

// growingTablesBlock reads the tables that only grow (IDX10) into @gt, in its own TRY whose error
// goes to @growError (declared by the query that holds it): user tables whose name contains log,
// history, audit, trace, event, journal, archive or queue, of a million rows or 500 MB, the 10
// biggest, and how far back each one's rows go (see storageDatabaseQuery). The Storage card's read
// and the archive-first read of QRY01 and the query page (growingTablesQuery) share it.
const growingTablesBlock = `DECLARE @gt TABLE (n int NOT NULL, schema_name sysname NOT NULL, table_name sysname NOT NULL, rows_count bigint NOT NULL,
                   mb decimal(18, 1) NOT NULL, date_col sysname NULL, date_indexed bit NOT NULL, key_col sysname NULL,
                   oldest datetime2 NULL);

/* Tables that only grow (IDX10): user tables whose name says log, history or queue, of a million rows
   or 500 MB, the 10 biggest; then the lowest value of each one's first date column, read uncommitted
   and only when that column leads an index, else the date of the row with the lowest ascending
   identity key of the clustered index (a seek either way, never a scan) */
BEGIN TRY
    INSERT @gt (n, schema_name, table_name, rows_count, mb, date_col, date_indexed, key_col)
    SELECT ROW_NUMBER() OVER (ORDER BY x.mb DESC, x.schema_name, x.table_name), x.schema_name, x.table_name, x.rows_count, x.mb,
           d.name,
           CASE WHEN EXISTS (SELECT 1 FROM sys.index_columns AS ic
                             JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
                             WHERE ic.object_id = x.object_id AND ic.column_id = d.column_id AND ic.key_ordinal = 1
                               AND i.is_disabled = 0 AND i.is_hypothetical = 0 AND i.has_filter = 0)
                THEN 1 ELSE 0 END,
           k.name
    FROM (SELECT TOP (10) t.object_id, s.name AS schema_name, t.name AS table_name,
                 SUM(CASE WHEN p.index_id IN (0, 1) THEN p.row_count ELSE 0 END) AS rows_count,
                 CONVERT(decimal(18, 1), SUM(p.used_page_count) * 8 / 1024.0) AS mb
          FROM sys.tables AS t
          JOIN sys.schemas AS s ON s.schema_id = t.schema_id
          JOIN sys.dm_db_partition_stats AS p ON p.object_id = t.object_id
          WHERE t.is_ms_shipped = 0 AND s.name <> N'archive' /* kakapo's own archive tables */
            AND EXISTS (SELECT 1 FROM (VALUES (N'log'), (N'history'), (N'audit'), (N'trace'), (N'event'), (N'journal'), (N'archive'), (N'queue')) AS w (word)
                        WHERE LOWER(t.name) LIKE N'%' + w.word + N'%')
          GROUP BY t.object_id, s.name, t.name
          HAVING SUM(CASE WHEN p.index_id IN (0, 1) THEN p.row_count ELSE 0 END) >= 1000000
              OR SUM(p.used_page_count) * 8 / 1024.0 >= 500
          ORDER BY SUM(p.used_page_count) DESC) AS x
    OUTER APPLY (SELECT TOP (1) c.name, c.column_id FROM sys.columns AS c
                 WHERE c.object_id = x.object_id AND TYPE_NAME(c.system_type_id) IN (N'datetime', N'datetime2', N'date')
                 ORDER BY c.column_id) AS d
    OUTER APPLY (SELECT c.name FROM sys.index_columns AS ic
                 JOIN sys.identity_columns AS c ON c.object_id = ic.object_id AND c.column_id = ic.column_id
                 WHERE ic.object_id = x.object_id AND ic.index_id = 1 AND ic.key_ordinal = 1
                   AND ic.is_descending_key = 0 AND CONVERT(bigint, c.increment_value) > 0) AS k;

    DECLARE @i int = 1, @gtCount int, @schema sysname, @table sysname, @col sysname, @indexed bit, @key sysname, @o datetime2, @sql nvarchar(max);
    SELECT @gtCount = COUNT(*) FROM @gt;
    WHILE @i <= @gtCount
    BEGIN
        SELECT @schema = schema_name, @table = table_name, @col = date_col, @indexed = date_indexed, @key = key_col, @o = NULL FROM @gt WHERE n = @i;
        IF @col IS NOT NULL AND (@indexed = 1 OR @key IS NOT NULL)
        BEGIN TRY
            IF @indexed = 1
                SET @sql = N'` + KakapoTag + ` SELECT @o = CONVERT(datetime2, MIN(' + QUOTENAME(@col) + N')) FROM '
                         + QUOTENAME(@schema) + N'.' + QUOTENAME(@table) + N' WITH (READUNCOMMITTED);';
            ELSE
                SET @sql = N'` + KakapoTag + ` SELECT TOP (1) @o = CONVERT(datetime2, ' + QUOTENAME(@col) + N') FROM '
                         + QUOTENAME(@schema) + N'.' + QUOTENAME(@table) + N' WITH (READUNCOMMITTED) ORDER BY ' + QUOTENAME(@key) + N';';
            EXEC sys.sp_executesql @sql, N'@o datetime2 OUTPUT', @o = @o OUTPUT;
            UPDATE @gt SET oldest = @o WHERE n = @i;
        END TRY
        BEGIN CATCH
            SET @o = NULL; /* the age stays unknown for this table; the others are still read */
        END CATCH;
        SET @i += 1;
    END;
END TRY
BEGIN CATCH
    SET @growError = ERROR_MESSAGE();
END CATCH;`

// growingTablesSelect returns @gt, the result set scanGrowingTables reads.
const growingTablesSelect = `SELECT schema_name, table_name, rows_count, mb, date_col, date_indexed, key_col, oldest FROM @gt ORDER BY n;`

// growingTablesQuery runs inside the database and reads only the tables that only grow, the same
// block as the Storage card's read: QRY01 and the query page say "archive before an index" when a
// costly query reads much of one (docs/phase5a-contract.md). Result sets: the error, the tables.
const growingTablesQuery = `SET NOCOUNT ON;
DECLARE @growError nvarchar(2048);
` + growingTablesBlock + `
SELECT @growError;
` + growingTablesSelect

// storageDatabaseQuery runs inside the database (EXEC [db].sys.sp_executesql): the file latency and
// the VLF count and how full the log is (sys.dm_db_log_space_usage; read from inside, so an
// AUTO_CLOSE database is open while they are read), the space
// used per file, the table sizes, the tables that only grow and the identity columns, each in its
// own TRY.
// The tables that only grow are user tables whose name contains log, history, audit, trace, event,
// journal, archive or queue, of a million rows or 500 MB, the 10 biggest. For each whose first datetime,
// datetime2 or date column is the leading key column of an index (not filtered, disabled or
// hypothetical), one SELECT MIN of that column, read uncommitted, says how far back its rows go:
// on the index it is a seek. Otherwise, when the clustered index leads with an ascending identity,
// the row with the lowest key is the oldest, so its date is read with TOP (1) ORDER BY that key (a
// seek on the clustered index). A MIN on an unindexed column would scan millions of rows, so it is
// never read.
// Result sets: errors, the VLF count and the log space used, file latency, space used, totals, the 10 biggest tables,
// tables that only grow, identity columns.
const storageDatabaseQuery = `SET NOCOUNT ON;
DECLARE @spaceError nvarchar(2048), @tableError nvarchar(2048), @growError nvarchar(2048), @identError nvarchar(2048),
        @ioError nvarchar(2048), @vlfError nvarchar(2048), @vlf int, @vlfMissing bit = 0,
        @logSpaceError nvarchar(2048), @logBytes bigint, @logPct float;
DECLARE @io TABLE (file_id int NOT NULL, num_reads bigint NOT NULL, num_writes bigint NOT NULL,
                   stall_read_ms bigint NOT NULL, stall_write_ms bigint NOT NULL);

IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 0 SET @ioError = N'the login does not have VIEW SERVER STATE.';
ELSE
BEGIN TRY
    INSERT @io (file_id, num_reads, num_writes, stall_read_ms, stall_write_ms)
    SELECT v.file_id, v.num_of_reads, v.num_of_writes, v.io_stall_read_ms, v.io_stall_write_ms
    FROM sys.dm_io_virtual_file_stats(DB_ID(), NULL) AS v;
END TRY
BEGIN CATCH
    SET @ioError = ERROR_MESSAGE();
END CATCH;

/* VLF count: sys.dm_db_log_info exists from 2016 SP2, so it is named only in dynamic SQL */
IF OBJECT_ID(N'sys.dm_db_log_info') IS NULL SET @vlfMissing = 1;
ELSE
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @n = COUNT(*) FROM sys.dm_db_log_info(DB_ID());', N'@n int OUTPUT', @n = @vlf OUTPUT;
END TRY
BEGIN CATCH
    SET @vlfError = ERROR_MESSAGE();
END CATCH;

/* How full this database's log is (STO11); dynamic SQL, so a missing permission is caught here */
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @b = total_log_size_in_bytes, @p = CONVERT(float, used_log_space_in_percent) FROM sys.dm_db_log_space_usage;',
         N'@b bigint OUTPUT, @p float OUTPUT', @b = @logBytes OUTPUT, @p = @logPct OUTPUT;
END TRY
BEGIN CATCH
    SET @logSpaceError = ERROR_MESSAGE();
END CATCH;
DECLARE @space TABLE (file_id int NOT NULL, used_mb bigint NULL);
DECLARE @tables TABLE (object_id int NOT NULL, table_name nvarchar(300) NOT NULL, data_mb decimal(18, 2) NOT NULL,
                       index_mb decimal(18, 2) NOT NULL, index_count int NOT NULL, rows_count bigint NOT NULL, is_heap bit NOT NULL,
                       has_pk bit NOT NULL);
DECLARE @ident TABLE (table_name nvarchar(300) NOT NULL, column_name sysname NOT NULL, type_name sysname NOT NULL,
                      last_value bigint NOT NULL, seed_value bigint NOT NULL, increment_value bigint NOT NULL,
                      pct_used decimal(9, 2) NOT NULL);

BEGIN TRY
    INSERT @space (file_id, used_mb)
    SELECT file_id, CONVERT(bigint, FILEPROPERTY(name, 'SpaceUsed')) * 8 / 1024
    FROM sys.database_files WHERE type IN (0, 1);
END TRY
BEGIN CATCH
    SET @spaceError = ERROR_MESSAGE();
END CATCH;

BEGIN TRY
    ;WITH ix AS (
        SELECT p.object_id, p.index_id, SUM(p.used_page_count) * 8 / 1024.0 AS size_mb, SUM(p.row_count) AS rows_count
        FROM sys.dm_db_partition_stats AS p
        GROUP BY p.object_id, p.index_id)
    INSERT @tables (object_id, table_name, data_mb, index_mb, index_count, rows_count, is_heap, has_pk)
    SELECT o.object_id, QUOTENAME(s.name) + N'.' + QUOTENAME(o.name),
           SUM(CASE WHEN i.index_id IN (0, 1) THEN ISNULL(ix.size_mb, 0) ELSE 0 END),
           SUM(CASE WHEN i.index_id > 1 THEN ISNULL(ix.size_mb, 0) ELSE 0 END),
           SUM(CASE WHEN i.index_id > 1 AND i.is_hypothetical = 0 AND i.is_disabled = 0 THEN 1 ELSE 0 END),
           MAX(CASE WHEN i.index_id IN (0, 1) THEN ISNULL(ix.rows_count, 0) ELSE 0 END),
           MAX(CASE WHEN i.index_id = 0 THEN 1 ELSE 0 END),
           MAX(CASE WHEN i.is_primary_key = 1 THEN 1 ELSE 0 END)
    FROM sys.indexes AS i
    JOIN sys.objects AS o ON o.object_id = i.object_id AND o.type = 'U' AND o.is_ms_shipped = 0
    JOIN sys.schemas AS s ON s.schema_id = o.schema_id
    LEFT JOIN ix ON ix.object_id = i.object_id AND ix.index_id = i.index_id
    GROUP BY o.object_id, s.name, o.name;
END TRY
BEGIN CATCH
    SET @tableError = ERROR_MESSAGE();
END CATCH;

` + growingTablesBlock + `

/* The tables in the archive schema (kakapo's archive writes there), and Query Store's size (IDX01);
   each in its own TRY, an error leaves the area empty */
DECLARE @archTables TABLE (table_name sysname NOT NULL);
DECLARE @qsMb bigint, @qsMax bigint;
BEGIN TRY
    INSERT @archTables (table_name)
    SELECT t.name FROM sys.tables AS t JOIN sys.schemas AS s ON s.schema_id = t.schema_id
    WHERE s.name = N'archive' AND t.is_ms_shipped = 0;
END TRY
BEGIN CATCH
END CATCH;
BEGIN TRY
    IF OBJECT_ID(N'sys.database_query_store_options') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @c = current_storage_size_mb, @m = max_storage_size_mb FROM sys.database_query_store_options WHERE actual_state > 0;',
             N'@c bigint OUTPUT, @m bigint OUTPUT', @c = @qsMb OUTPUT, @m = @qsMax OUTPUT;
END TRY
BEGIN CATCH
END CATCH;

BEGIN TRY
    INSERT @ident (table_name, column_name, type_name, last_value, seed_value, increment_value, pct_used)
    SELECT i.table_name, i.name, i.type_name, i.last_value, i.seed_value, i.increment_value, i.pct_used
    FROM (SELECT QUOTENAME(OBJECT_SCHEMA_NAME(c.object_id)) + N'.' + QUOTENAME(OBJECT_NAME(c.object_id)) AS table_name,
                 c.name, t.name AS type_name,
                 CONVERT(bigint, c.last_value) AS last_value, CONVERT(bigint, c.seed_value) AS seed_value,
                 CONVERT(bigint, c.increment_value) AS increment_value,
                 CONVERT(decimal(9, 2), ABS(CONVERT(bigint, c.last_value)) * 100.0
                   / CASE t.name WHEN 'tinyint' THEN 255 WHEN 'smallint' THEN 32767 WHEN 'int' THEN 2147483647 ELSE 9223372036854775807 END) AS pct_used
          FROM sys.identity_columns AS c
          JOIN sys.types AS t ON t.user_type_id = c.user_type_id
          JOIN sys.objects AS o ON o.object_id = c.object_id AND o.type = 'U' AND o.is_ms_shipped = 0
          WHERE c.last_value IS NOT NULL AND t.name IN ('tinyint', 'smallint', 'int', 'bigint')) AS i
    WHERE i.pct_used >= 50;
END TRY
BEGIN CATCH
    SET @identError = ERROR_MESSAGE();
END CATCH;

SELECT @spaceError, @tableError, @growError, @identError, @ioError, @vlfError, @vlf, @vlfMissing,
       @logSpaceError, @logBytes, @logPct;
SELECT file_id, num_reads, num_writes, stall_read_ms, stall_write_ms FROM @io;
SELECT file_id, used_mb FROM @space;
SELECT COUNT(*), ISNULL(SUM(data_mb), 0), ISNULL(SUM(index_mb), 0) FROM @tables;
SELECT TOP (10) table_name, data_mb, index_mb, index_count, rows_count FROM @tables
WHERE data_mb + index_mb >= 1 ORDER BY data_mb + index_mb DESC;
` + growingTablesSelect + `
SELECT table_name, column_name, type_name, last_value, seed_value, increment_value, pct_used FROM @ident
ORDER BY pct_used DESC;
SELECT table_name FROM @archTables ORDER BY table_name;
SELECT @qsMb, @qsMax;
SELECT TOP (50) table_name, data_mb, index_mb, index_count, rows_count, has_pk FROM @tables
WHERE is_heap = 1 AND data_mb >= 1 ORDER BY data_mb DESC;`

// StorageFacts reads what the Storage card needs about one database (docs/phase3b-contract.md),
// SELECTs only. An area the login may not read (VIEW SERVER STATE, VIEW DATABASE STATE) carries
// its reason in the facts instead of failing the read; the error returned is for a read that could
// not run at all, or a database that does not exist.
func StorageFacts(ctx context.Context, db *sql.DB, database string) (domain.StorageFacts, error) {
	f := domain.StorageFacts{Database: database}
	if err := readStorageInstance(ctx, db, database, &f); err != nil {
		return f, err
	}
	readServerFiles(ctx, db, &f)
	if f.State == "ONLINE" {
		if err := readStorageDatabase(ctx, db, database, &f); err != nil {
			msg := "could not read inside the database: " + err.Error()
			f.SpaceUsedError, f.TablesError, f.GrowingTablesError, f.IdentityError = msg, msg, msg, msg
			f.LatencyError, f.VLFError, f.LogSpaceError = msg, msg, msg
		}
	}
	return f, nil
}

// serverFilesQuery is every database's files with their I/O since the restart (STO09: whether a disk is
// slow for the whole server, and who makes it busy). It needs VIEW SERVER STATE. Only the files of
// sys.master_files, kept first: the file statistics also list SQL Server 2022's hidden model_msdb and
// model_replicatedmaster (32761, 32762), and sys.dm_os_volume_stats on those never returns.
const serverFilesQuery = safeSession + `SET NOCOUNT ON;
DECLARE @f TABLE (database_id int NOT NULL, file_id int NOT NULL, type_desc nvarchar(60) NOT NULL, name nvarchar(128) NOT NULL,
                  physical_name nvarchar(260) NOT NULL, reads bigint NOT NULL, writes bigint NOT NULL, stall_read_ms bigint NOT NULL,
                  stall_write_ms bigint NOT NULL, bytes_read bigint NOT NULL, bytes_written bigint NOT NULL);
INSERT @f
SELECT v.database_id, v.file_id, mf.type_desc, mf.name, mf.physical_name, v.num_of_reads, v.num_of_writes,
       v.io_stall_read_ms, v.io_stall_write_ms, v.num_of_bytes_read, v.num_of_bytes_written
FROM sys.dm_io_virtual_file_stats(NULL, NULL) AS v
JOIN sys.master_files AS mf ON mf.database_id = v.database_id AND mf.file_id = v.file_id;
SELECT f.database_id, CAST(ISNULL(DB_NAME(f.database_id), N'') AS nvarchar(128)), f.file_id, f.type_desc, f.name, f.physical_name,
       CAST(vs.volume_mount_point AS nvarchar(256)), f.reads, f.writes, f.stall_read_ms, f.stall_write_ms, f.bytes_read, f.bytes_written
FROM @f AS f
CROSS APPLY sys.dm_os_volume_stats(f.database_id, f.file_id) AS vs;`

// readServerFiles reads every database's files with their I/O and whether SQL Server runs in a virtual
// machine; a failure is kept in ServerFilesError and costs nothing else.
func readServerFiles(ctx context.Context, db *sql.DB, f *domain.StorageFacts) {
	var vm sql.NullString
	if err := db.QueryRowContext(ctx, Tag(safeSession+"SELECT CONVERT(nvarchar(60), virtual_machine_type_desc) FROM sys.dm_os_sys_info;")).Scan(&vm); err == nil {
		f.VirtualMachine = vm.String
	}
	rows, err := db.QueryContext(ctx, Tag(serverFilesQuery))
	if err != nil {
		f.ServerFilesError = friendlyError(err).Error()
		return
	}
	defer rows.Close()
	files := []domain.ServerFile{}
	for rows.Next() {
		var x domain.ServerFile
		if err := rows.Scan(&x.DatabaseID, &x.Database, &x.FileID, &x.Type, &x.LogicalName, &x.PhysicalName, &x.Volume,
			&x.Reads, &x.Writes, &x.ReadStallMs, &x.WriteStallMs, &x.BytesRead, &x.BytesWritten); err != nil {
			f.ServerFilesError = err.Error()
			return
		}
		files = append(files, x)
	}
	if err := rows.Err(); err != nil {
		f.ServerFilesError = friendlyError(err).Error()
		return
	}
	f.ServerFiles = files
}

func readStorageInstance(ctx context.Context, db *sql.DB, database string, f *domain.StorageFacts) error {
	rows, err := db.QueryContext(ctx, Tag(storageInstanceQuery), sql.Named("db", database))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned no storage facts")
	}
	var (
		found                             bool
		state, recovery, reuse            sql.NullString
		cpu, sched                        sql.NullInt32
		tAlloc, tFree                     sql.NullInt64
		tLogPct                           sql.NullFloat64
		now                               time.Time
		filesErr, volErr, cpuErr, tempErr sql.NullString
		tUser, tInternal, tVersion        sql.NullInt64
		snapSession                       sql.NullInt32
		snapSecs, latchMs, slowMs         sql.NullInt64
		uptimeHours                       sql.NullFloat64
		tsessErr, latchErr, slowFile      sql.NullString
		growthErr                         sql.NullString
		slowAt                            sql.NullTime
		tgCount, tgLog                    sql.NullInt32
		tgSecs, tgSlowMs                  sql.NullInt64
		tgLast, tgSlowAt                  sql.NullTime
		tgSlowFile                        sql.NullString
	)
	if err := rows.Scan(&found, &state, &recovery, &reuse, &f.EngineEdition, &f.EngineVersion, &cpu, &sched,
		&tAlloc, &tFree, &tLogPct, &now, &f.UTCOffsetMinutes,
		&filesErr, &volErr, &cpuErr, &tempErr,
		&tUser, &tInternal, &tVersion, &snapSession, &snapSecs, &tsessErr,
		&latchMs, &uptimeHours, &latchErr,
		&slowMs, &slowFile, &slowAt, &growthErr,
		&tgCount, &tgSecs, &tgLog, &tgLast, &tgSlowMs, &tgSlowFile, &tgSlowAt); err != nil {
		return fmt.Errorf("read the storage facts of %s: %w", database, err)
	}
	if !found {
		return fmt.Errorf("there is no database named %q", database)
	}
	f.State, f.RecoveryModel, f.LogReuseWait = state.String, recovery.String, reuse.String
	f.CPUCount, f.OnlineSchedulers = int(cpu.Int32), int(sched.Int32)
	if tAlloc.Valid && tFree.Valid {
		f.TempdbAllocMB, f.TempdbFreeMB = &tAlloc.Int64, &tFree.Int64
	}
	if tLogPct.Valid {
		f.TempdbLogUsedPct = &tLogPct.Float64
	}
	f.ServerNow = naive(now)
	f.FilesError, f.VolumeError = filesErr.String, volErr.String
	f.CPUError, f.TempdbError = cpuErr.String, tempErr.String
	f.UserObjectsMB, f.InternalObjectsMB, f.VersionStoreMB = tUser.Int64, tInternal.Int64, tVersion.Int64
	f.TempdbSessionsError = tsessErr.String
	if snapSession.Valid {
		f.OldestSnapshot = &domain.SnapshotTransaction{SessionID: int(snapSession.Int32), ElapsedSeconds: snapSecs.Int64}
	}
	f.PageLatchError, f.UptimeHours = latchErr.String, uptimeHours.Float64
	if latchMs.Valid && f.PageLatchError == "" {
		f.PageLatchMs = &latchMs.Int64
	}
	f.SlowGrowthError = growthErr.String
	if slowMs.Valid && slowAt.Valid {
		f.SlowGrowth = &domain.SlowGrowth{Ms: slowMs.Int64, File: slowFile.String, At: naive(slowAt.Time)}
	}
	// TempDB's growth events: a count of 0 is a trace read that holds none; nil when not read.
	if tgCount.Valid && f.SlowGrowthError == "" {
		g := domain.TraceGrowth{Count: int(tgCount.Int32), Seconds: tgSecs.Int64, LogGrows: int(tgLog.Int32),
			SlowestMs: tgSlowMs.Int64, SlowestFile: tgSlowFile.String}
		if tgLast.Valid {
			g.LastAt = naive(tgLast.Time)
		}
		if tgSlowAt.Valid {
			g.SlowestAt = naive(tgSlowAt.Time)
		}
		f.TempdbGrowth = &g
	}

	if err := nextResultSet(rows, "files"); err != nil {
		return err
	}
	f.Files = []domain.StorageFile{}
	for rows.Next() {
		var (
			x           domain.StorageFile
			volume      sql.NullString
			total, free sql.NullInt64
			startMB     sql.NullInt64
		)
		if err := rows.Scan(&x.DatabaseID, &x.FileID, &x.Type, &x.LogicalName, &x.PhysicalName, &x.SizeMB, &x.Growth,
			&x.IsPercentGrowth, &x.MaxSizePages, &volume, &total, &free, &startMB); err != nil {
			return fmt.Errorf("read the files of %s: %w", database, err)
		}
		x.Database = database
		if x.DatabaseID == 2 {
			x.Database = "tempdb"
		}
		x.Volume, x.VolumeTotalMB, x.VolumeFreeMB = volume.String, total.Int64, free.Int64
		x.StartupSizeMB = startMB.Int64
		f.Files = append(f.Files, x)
	}

	if err := nextResultSet(rows, "tempdb sessions"); err != nil {
		return err
	}
	f.TempdbSessions = []domain.TempdbSession{}
	for rows.Next() {
		var x domain.TempdbSession
		var login, program sql.NullString
		if err := rows.Scan(&x.SessionID, &login, &program, &x.MB); err != nil {
			return fmt.Errorf("read the tempdb sessions: %w", err)
		}
		x.Login, x.Program = login.String, program.String
		f.TempdbSessions = append(f.TempdbSessions, x)
	}
	return friendlyErrOrNil(rows.Err())
}

func readStorageDatabase(ctx context.Context, db *sql.DB, database string, f *domain.StorageFacts) error {
	rows, err := db.QueryContext(ctx, inDatabaseQuoted(database), sql.Named("stmt", TagStatements(storageDatabaseQuery)))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()

	// 1: the error of each area.
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned nothing from inside the database")
	}
	var (
		spaceErr, tableErr, growErr, identErr, ioErr, vlfErr sql.NullString
		vlf                                                  sql.NullInt32
		logSpaceErr                                          sql.NullString
		logBytes                                             sql.NullInt64
		logPct                                               sql.NullFloat64
	)
	if err := rows.Scan(&spaceErr, &tableErr, &growErr, &identErr, &ioErr, &vlfErr, &vlf, &f.VLFUnsupported,
		&logSpaceErr, &logBytes, &logPct); err != nil {
		return err
	}
	f.LogSpaceError = logSpaceErr.String
	switch {
	case f.LogSpaceError != "":
	case logBytes.Valid && logPct.Valid:
		mb := logBytes.Int64 / 1048576
		f.LogSizeMB, f.LogUsedPct = &mb, &logPct.Float64
	default:
		f.LogSpaceError = "sys.dm_db_log_space_usage returned nothing."
	}
	f.SpaceUsedError, f.TablesError, f.GrowingTablesError, f.IdentityError = spaceErr.String, tableErr.String, growErr.String, identErr.String
	f.LatencyError, f.VLFError = ioErr.String, vlfErr.String
	if vlf.Valid {
		n := int(vlf.Int32)
		f.VLFCount = &n
	}

	// 2: file latency since the restart.
	if err := nextResultSet(rows, "file latency"); err != nil {
		return err
	}
	for rows.Next() {
		var id int
		var reads, writes, sRead, sWrite int64
		if err := rows.Scan(&id, &reads, &writes, &sRead, &sWrite); err != nil {
			return err
		}
		for i := range f.Files {
			if x := &f.Files[i]; x.DatabaseID != 2 && x.FileID == id {
				x.HasIO, x.Reads, x.Writes, x.ReadStallMs, x.WriteStallMs = true, reads, writes, sRead, sWrite
			}
		}
	}

	// 3: the space used per file.
	if err := nextResultSet(rows, "space used"); err != nil {
		return err
	}
	used := map[int]int64{}
	for rows.Next() {
		var id int
		var mb sql.NullInt64
		if err := rows.Scan(&id, &mb); err != nil {
			return err
		}
		if mb.Valid {
			used[id] = mb.Int64
		}
	}
	for i := range f.Files {
		if mb, ok := used[f.Files[i].FileID]; ok && f.Files[i].DatabaseID != 2 {
			f.Files[i].UsedMB = &mb
		}
	}

	// 4: totals over every table.
	if err := nextResultSet(rows, "table totals"); err != nil {
		return err
	}
	for rows.Next() {
		if err := rows.Scan(&f.TableCount, &f.TotalDataMB, &f.TotalIndexMB); err != nil {
			return err
		}
	}

	// 5: the 10 biggest tables.
	if err := nextResultSet(rows, "tables"); err != nil {
		return err
	}
	f.Tables = []domain.TableSize{}
	for rows.Next() {
		var t domain.TableSize
		if err := rows.Scan(&t.Name, &t.DataMB, &t.IndexMB, &t.IndexCount, &t.Rows); err != nil {
			return err
		}
		f.Tables = append(f.Tables, t)
	}

	// 6: the tables that only grow.
	if err := nextResultSet(rows, "tables that only grow"); err != nil {
		return err
	}
	if f.GrowingTables, err = scanGrowingTables(rows); err != nil {
		return err
	}

	// 7: identity columns at half their range or more.
	if err := nextResultSet(rows, "identity columns"); err != nil {
		return err
	}
	f.Identities = []domain.IdentityColumn{}
	for rows.Next() {
		var c domain.IdentityColumn
		if err := rows.Scan(&c.Table, &c.Column, &c.Type, &c.LastValue, &c.Seed, &c.Increment, &c.PctUsed); err != nil {
			return err
		}
		f.Identities = append(f.Identities, c)
	}

	// 8: the tables in the archive schema.
	if err := nextResultSet(rows, "archive tables"); err != nil {
		return err
	}
	f.ArchiveTables = []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		f.ArchiveTables = append(f.ArchiveTables, n)
	}

	// 9: Query Store's size.
	if err := nextResultSet(rows, "query store size"); err != nil {
		return err
	}
	for rows.Next() {
		var cur, max sql.NullInt64
		if err := rows.Scan(&cur, &max); err != nil {
			return err
		}
		if cur.Valid && max.Valid {
			f.QueryStoreMB, f.QueryStoreMaxMB = &cur.Int64, &max.Int64
		}
	}

	// 10: the heaps of 1 MB or more, biggest first (IDX06).
	if err := nextResultSet(rows, "heaps"); err != nil {
		return err
	}
	f.Heaps = []domain.TableSize{}
	for rows.Next() {
		var t domain.TableSize
		if err := rows.Scan(&t.Name, &t.DataMB, &t.IndexMB, &t.IndexCount, &t.Rows, &t.HasPrimaryKey); err != nil {
			return err
		}
		f.Heaps = append(f.Heaps, t)
	}
	return friendlyErrOrNil(rows.Err())
}

// scanGrowingTables reads the rows of growingTablesSelect.
func scanGrowingTables(rows *sql.Rows) ([]domain.GrowingTable, error) {
	out := []domain.GrowingTable{}
	for rows.Next() {
		var (
			t       domain.GrowingTable
			dateCol sql.NullString
			keyCol  sql.NullString
			oldest  sql.NullTime
		)
		if err := rows.Scan(&t.Schema, &t.Name, &t.Rows, &t.MB, &dateCol, &t.DateIndexed, &keyCol, &oldest); err != nil {
			return nil, err
		}
		t.DateCol, t.KeyCol, t.Oldest = dateCol.String, keyCol.String, naivePtr(oldest)
		out = append(out, t)
	}
	return out, nil
}

// inDatabaseQuoted is the batch that runs @stmt inside a database with QUOTED_IDENTIFIER ON, the
// standard setting, whatever the connection's default: the dynamic SQL is compiled with the
// session's setting.
func inDatabaseQuoted(database string) string {
	return Tag(safeSession + "SET QUOTED_IDENTIFIER ON; EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt;")
}

// GrowingTables reads only the tables that only grow of one database (growingTablesQuery, the
// Storage card's block), SELECTs only: for QRY01's and the query page's "archive before an index".
// The string is SQL Server's error when that block failed; the error is for a read that could not
// run at all.
func GrowingTables(ctx context.Context, db *sql.DB, database string) ([]domain.GrowingTable, string, error) {
	rows, err := db.QueryContext(ctx, inDatabaseQuoted(database), sql.Named("stmt", TagStatements(growingTablesQuery)))
	if err != nil {
		return nil, "", friendlyError(err)
	}
	defer rows.Close()
	var growErr sql.NullString
	if rows.Next() {
		if err := rows.Scan(&growErr); err != nil {
			return nil, "", err
		}
	}
	if err := nextResultSet(rows, "tables that only grow"); err != nil {
		return nil, "", err
	}
	tables, err := scanGrowingTables(rows)
	if err != nil {
		return nil, "", err
	}
	if err := rows.Err(); err != nil {
		return nil, "", friendlyError(err)
	}
	return tables, growErr.String, nil
}

// nextResultSet drains the current result set and moves to the next; the driver reaches the next
// one only once this one is read to its end.
func nextResultSet(rows *sql.Rows, what string) error {
	for rows.Next() {
	}
	if !rows.NextResultSet() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return fmt.Errorf("SQL Server returned no %s result", what)
	}
	return nil
}

func friendlyErrOrNil(err error) error {
	if err != nil {
		return friendlyError(err)
	}
	return nil
}

// checkdbEstimateQuery asks SQL Server how much tempdb DBCC CHECKDB would need. ESTIMATEONLY
// checks nothing and changes nothing; it needs db_owner of the database or sysadmin.
const checkdbEstimateQuery = safeSession + `DBCC CHECKDB (@db) WITH ESTIMATEONLY, NO_INFOMSGS;`

// estimateLine matches the estimate as a message, as SQL Server 2019 and later send it:
// "Estimated TEMPDB space (in KB) needed for CHECKDB on database X = 4604522."
var estimateLine = regexp.MustCompile(`(?i)Estimated TEMPDB space.*?=\s*([0-9]+)`)

// RunCheckdbEstimate runs DBCC CHECKDB (@db) WITH ESTIMATEONLY, NO_INFOMSGS and sums the
// estimate: older versions return it as result sets whose one column is named "Estimated TEMPDB
// space (in KB) needed for CHECKALLOC" and "... for CHECKTABLES"; later ones send it as a
// message. Every result set and message is read. It returns the sum in KB and how long it took.
func RunCheckdbEstimate(ctx context.Context, db *sql.DB, database string) (int64, time.Duration, error) {
	start := time.Now()
	msgs := &sqlexp.ReturnMessage{}
	rows, err := db.QueryContext(ctx, Tag(checkdbEstimateQuery), msgs, sql.Named("db", database))
	if err != nil {
		return 0, time.Since(start), friendlyError(err)
	}
	defer rows.Close()

	var total int64
	found := false
	var dbccErr error
	for active := true; active; {
		switch m := msgs.Message(ctx).(type) {
		case sqlexp.MsgNotice:
			if kb, ok := estimateFromText(m.Message.String()); ok {
				total, found = total+kb, true
			}
		case sqlexp.MsgError:
			if dbccErr == nil {
				dbccErr = m.Error
			}
		case sqlexp.MsgNext:
			cols, _ := rows.Columns()
			for rows.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return 0, time.Since(start), err
				}
				for i, c := range cols {
					if kb, ok := estimateFromColumn(c, vals[i]); ok {
						total, found = total+kb, true
					}
				}
			}
		case sqlexp.MsgNextResultSet:
			active = rows.NextResultSet()
		case nil:
			active = false
		}
		if ctx.Err() != nil {
			return 0, time.Since(start), ctx.Err()
		}
	}
	took := time.Since(start)
	if err := rows.Err(); err != nil && dbccErr == nil {
		dbccErr = err
	}
	if dbccErr != nil {
		return 0, took, friendlyError(dbccErr)
	}
	if !found {
		return 0, took, errors.New("DBCC CHECKDB returned no estimate")
	}
	return total, took, nil
}

// estimateFromText reads the KB of an estimate sent as a message.
func estimateFromText(s string) (int64, bool) {
	m := estimateLine.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	return n, err == nil
}

// estimateFromColumn reads the KB of an estimate returned as a result set: a column named
// "Estimated TEMPDB space ..." holding the number.
func estimateFromColumn(col string, v any) (int64, bool) {
	if !strings.Contains(strings.ToLower(col), "estimated tempdb space") {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case float64:
		return int64(x), true
	case []byte:
		n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	}
	return 0, false
}
