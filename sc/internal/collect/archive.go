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

	"sqlmonitor/internal/domain"
)

// The archive reads (docs/phase5a-contract.md): SELECTs only, each area in its own TRY with its
// error column, every batch and every piece of dynamic SQL tagged. Table and column names reach SQL
// as parameters, or quoted through domain.QuoteName after the catalogue read named them; the one
// piece of user text, the condition, is checked by domain.CanonicalCondition first and only its
// canonical form is written into the estimate.

// archiveInstanceQuery reads what the archive fix needs from the instance (@db, @archiveDb): the
// server, the edition, the clock, whether the archive database exists and is online, the recovery
// model, the last log backup (msdb), the Agent service, and the free MB on the tightest drive of
// each database's data files.
const archiveInstanceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @dbid int = DB_ID(@db), @archId int = DB_ID(@archiveDb), @recovery nvarchar(60), @archOnline bit = 0;
SELECT @recovery = CAST(recovery_model_desc AS nvarchar(60)) FROM sys.databases WHERE database_id = @dbid;
SELECT @archOnline = CASE WHEN state = 0 THEN 1 ELSE 0 END FROM sys.databases WHERE database_id = @archId;
DECLARE @express bit = CASE WHEN CONVERT(int, SERVERPROPERTY('EngineEdition')) = 4 THEN 1 ELSE 0 END;

/* The Agent service: a stopped Agent runs no job */
DECLARE @agentStatus nvarchar(60), @agentError nvarchar(2048);
IF @express = 0
BEGIN
    IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') <> 1 SET @agentError = N'the login does not have VIEW SERVER STATE.';
    ELSE
    BEGIN TRY
        SELECT TOP (1) @agentStatus = CAST(status_desc AS nvarchar(60))
        FROM sys.dm_server_services WHERE servicename LIKE N'SQL Server Agent%';
        IF @agentStatus IS NULL SET @agentError = N'the SQL Server Agent service is not visible on this platform.';
    END TRY
    BEGIN CATCH
        SET @agentError = ERROR_MESSAGE();
    END CATCH;
END;

/* The last log backup: in FULL recovery the deletes are logged, and only a log backup frees it */
DECLARE @lastLog datetime, @logError nvarchar(2048);
BEGIN TRY
    SELECT @lastLog = MAX(bs.backup_finish_date) FROM msdb.dbo.backupset AS bs WHERE bs.database_name = @db AND bs.type = 'L';
END TRY
BEGIN CATCH
    SET @logError = ERROR_MESSAGE();
END CATCH;

/* Room: the free MB on the tightest drive of each database's data files */
DECLARE @free bigint, @archFree bigint, @roomError nvarchar(2048);
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') <> 1 SET @roomError = N'the login does not have VIEW SERVER STATE.';
ELSE
BEGIN TRY
    SELECT @free = MIN(vs.available_bytes / 1048576)
    FROM sys.master_files AS mf CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) AS vs
    WHERE mf.database_id = @dbid AND mf.type = 0;
    IF @archId IS NOT NULL AND @archOnline = 1
        SELECT @archFree = MIN(vs.available_bytes / 1048576)
        FROM sys.master_files AS mf CROSS APPLY sys.dm_os_volume_stats(mf.database_id, mf.file_id) AS vs
        WHERE mf.database_id = @archId AND mf.type = 0;
END TRY
BEGIN CATCH
    SET @roomError = ERROR_MESSAGE();
END CATCH;

SELECT COALESCE(@@SERVERNAME, CAST(SERVERPROPERTY('ServerName') AS nvarchar(256))),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)), CONVERT(int, SERVERPROPERTY('EngineEdition')), GETDATE(),
       CAST(CASE WHEN @archId IS NOT NULL AND @archOnline = 1 THEN 1 ELSE 0 END AS bit),
       @recovery, @lastLog, @logError, @agentStatus, @agentError, @free, @archFree, @roomError;`

// archiveTableQuery runs inside the database (EXEC [db].sys.sp_executesql, with @schema and
// @table) and reads one user table: its rows and MB (sys.dm_db_partition_stats), its columns with
// the date columns that lead an enabled, unfiltered index, the foreign keys pointing at it and the
// count of those from it, its DML triggers, and the features that block an archive (CDC, change
// tracking, replication, memory-optimised, temporal; the last two only where the columns exist).
// Result sets: one row, the columns, the keys in, the triggers.
const archiveTableQuery = `SET NOCOUNT ON;
DECLARE @obj int = OBJECT_ID(QUOTENAME(@schema) + N'.' + QUOTENAME(@table));
IF @obj IS NOT NULL AND NOT EXISTS (SELECT 1 FROM sys.tables AS t WHERE t.object_id = @obj AND t.is_ms_shipped = 0)
    SET @obj = NULL;
DECLARE @rows bigint, @mb float, @keysOut int, @cdc bit, @ct bit, @pub bit, @merge bit, @repl bit, @memopt bit, @temporal nvarchar(60);
DECLARE @tableError nvarchar(2048), @colsError nvarchar(2048), @keysError nvarchar(2048), @trgError nvarchar(2048), @featError nvarchar(2048);
DECLARE @cols TABLE (column_id int NOT NULL, name sysname NOT NULL, type_name sysname NULL, is_identity bit NOT NULL,
                     is_computed bit NOT NULL, is_nullable bit NOT NULL, date_indexed bit NOT NULL, clustered_key bit NOT NULL);
DECLARE @keys TABLE (name sysname NOT NULL, from_table nvarchar(300) NOT NULL);
DECLARE @trg TABLE (name sysname NOT NULL, event_type nvarchar(60) NULL, is_disabled bit NOT NULL, is_instead_of bit NOT NULL);
IF @obj IS NOT NULL
BEGIN
    BEGIN TRY
        SELECT @rows = SUM(CASE WHEN ps.index_id IN (0, 1) THEN ps.row_count ELSE 0 END), @mb = SUM(ps.used_page_count) * 8 / 1024.0
        FROM sys.dm_db_partition_stats AS ps WHERE ps.object_id = @obj;
    END TRY
    BEGIN CATCH
        SET @tableError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        INSERT @cols (column_id, name, type_name, is_identity, is_computed, is_nullable, date_indexed, clustered_key)
        SELECT c.column_id, c.name, TYPE_NAME(c.system_type_id), c.is_identity, c.is_computed, c.is_nullable,
               CASE WHEN TYPE_NAME(c.system_type_id) IN (N'datetime', N'datetime2', N'date')
                     AND EXISTS (SELECT 1 FROM sys.index_columns AS ic
                                 JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
                                 WHERE ic.object_id = c.object_id AND ic.column_id = c.column_id AND ic.key_ordinal = 1
                                   AND i.is_disabled = 0 AND i.is_hypothetical = 0 AND i.has_filter = 0)
                    THEN 1 ELSE 0 END,
               /* an ascending integer identity that leads the clustered index: the rows are stored oldest first */
               CASE WHEN TYPE_NAME(c.system_type_id) IN (N'int', N'bigint', N'smallint', N'tinyint')
                     AND EXISTS (SELECT 1 FROM sys.index_columns AS ic
                                 JOIN sys.identity_columns AS ident ON ident.object_id = ic.object_id AND ident.column_id = ic.column_id
                                 WHERE ic.object_id = c.object_id AND ic.column_id = c.column_id AND ic.index_id = 1
                                   AND ic.key_ordinal = 1 AND ic.is_descending_key = 0 AND CONVERT(bigint, ident.increment_value) > 0)
                    THEN 1 ELSE 0 END
        FROM sys.columns AS c WHERE c.object_id = @obj;
    END TRY
    BEGIN CATCH
        SET @colsError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        INSERT @keys (name, from_table)
        SELECT fk.name, OBJECT_SCHEMA_NAME(fk.parent_object_id) + N'.' + OBJECT_NAME(fk.parent_object_id)
        FROM sys.foreign_keys AS fk WHERE fk.referenced_object_id = @obj;
        SELECT @keysOut = COUNT(*) FROM sys.foreign_keys AS fk WHERE fk.parent_object_id = @obj;
    END TRY
    BEGIN CATCH
        SET @keysError = ERROR_MESSAGE();
    END CATCH;

    BEGIN TRY
        INSERT @trg (name, event_type, is_disabled, is_instead_of)
        SELECT tr.name, CAST(te.type_desc AS nvarchar(60)), tr.is_disabled, tr.is_instead_of_trigger
        FROM sys.triggers AS tr
        LEFT JOIN sys.trigger_events AS te ON te.object_id = tr.object_id
        WHERE tr.parent_id = @obj;
    END TRY
    BEGIN CATCH
        SET @trgError = ERROR_MESSAGE();
    END CATCH;

    /* is_memory_optimized exists from SQL Server 2014 and temporal_type_desc from 2016, so they are
       named only in dynamic SQL */
    BEGIN TRY
        SELECT @cdc = t.is_tracked_by_cdc, @merge = t.is_merge_published, @repl = t.is_replicated, @pub = o.is_published
        FROM sys.tables AS t JOIN sys.objects AS o ON o.object_id = t.object_id
        WHERE t.object_id = @obj;
        SET @ct = CASE WHEN EXISTS (SELECT 1 FROM sys.change_tracking_tables AS ctt WHERE ctt.object_id = @obj) THEN 1 ELSE 0 END;
        IF COL_LENGTH(N'sys.tables', N'is_memory_optimized') IS NOT NULL
            EXEC sys.sp_executesql N'/* kakapo */ SELECT @m = is_memory_optimized FROM sys.tables WHERE object_id = @o;',
                 N'@m bit OUTPUT, @o int', @m = @memopt OUTPUT, @o = @obj;
        IF COL_LENGTH(N'sys.tables', N'temporal_type_desc') IS NOT NULL
            EXEC sys.sp_executesql N'/* kakapo */ SELECT @t = CAST(temporal_type_desc AS nvarchar(60)) FROM sys.tables WHERE object_id = @o;',
                 N'@t nvarchar(60) OUTPUT, @o int', @t = @temporal OUTPUT, @o = @obj;
    END TRY
    BEGIN CATCH
        SET @featError = ERROR_MESSAGE();
    END CATCH;
END;
SELECT CAST(CASE WHEN @obj IS NULL THEN 0 ELSE 1 END AS bit), ISNULL(@rows, 0), ISNULL(@mb, 0),
       ISNULL(@cdc, 0), ISNULL(@ct, 0), ISNULL(@pub, 0), ISNULL(@merge, 0), ISNULL(@repl, 0), ISNULL(@memopt, 0), @temporal,
       ISNULL(@keysOut, 0), @tableError, @colsError, @keysError, @trgError, @featError;
SELECT name, type_name, is_identity, is_computed, is_nullable, date_indexed, clustered_key FROM @cols ORDER BY column_id;
SELECT name, from_table FROM @keys ORDER BY from_table, name;
SELECT name, event_type, is_disabled, is_instead_of FROM @trg ORDER BY name, event_type;`

// archiveTargetQuery runs inside a database (with @schema and @table) and reads one table, the
// archive table or a table whose rows are counted: whether it exists, its rows and its columns.
const archiveTargetQuery = `SET NOCOUNT ON;
DECLARE @obj int = OBJECT_ID(QUOTENAME(@schema) + N'.' + QUOTENAME(@table), N'U'), @rows bigint, @error nvarchar(2048);
DECLARE @cols TABLE (column_id int NOT NULL, name sysname NOT NULL, type_name sysname NULL, is_identity bit NOT NULL);
IF @obj IS NOT NULL
BEGIN TRY
    INSERT @cols (column_id, name, type_name, is_identity)
    SELECT c.column_id, c.name, TYPE_NAME(c.system_type_id), c.is_identity FROM sys.columns AS c WHERE c.object_id = @obj;
    SELECT @rows = SUM(ps.row_count) FROM sys.dm_db_partition_stats AS ps WHERE ps.object_id = @obj AND ps.index_id IN (0, 1);
END TRY
BEGIN CATCH
    SET @error = ERROR_MESSAGE();
END CATCH;
SELECT CAST(CASE WHEN @obj IS NULL THEN 0 ELSE 1 END AS bit), ISNULL(@rows, 0), @error;
SELECT name, type_name, is_identity FROM @cols ORDER BY column_id;`

// archiveJobsQuery reads kakapo's archive jobs on one database (@db) from msdb, in its own TRY:
// name, enabled, step 1's command (it carries the options line), the first schedule's time and
// whether it is enabled, and step 1's last run from sysjobhistory (outcome, date, time, duration
// and the message, which keeps the step's "rows moved: n").
const archiveJobsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @jobsError nvarchar(2048), @prefix nvarchar(300) = N'kakapo: archive ' + @db + N'.';
DECLARE @jobs TABLE (name sysname NOT NULL, enabled bit NOT NULL, command nvarchar(max) NULL, start_time int NULL, schedule_enabled bit NULL,
                     run_status int NULL, run_date int NULL, run_time int NULL, run_duration int NULL, message nvarchar(4000) NULL);
BEGIN TRY
    INSERT @jobs (name, enabled, command, start_time, schedule_enabled, run_status, run_date, run_time, run_duration, message)
    SELECT j.name, j.enabled,
           (SELECT TOP (1) s.command FROM msdb.dbo.sysjobsteps AS s WHERE s.job_id = j.job_id ORDER BY s.step_id),
           sc.active_start_time, sc.enabled, h.run_status, h.run_date, h.run_time, h.run_duration, h.message
    FROM msdb.dbo.sysjobs AS j
    OUTER APPLY (SELECT TOP (1) ss.active_start_time, ss.enabled
                 FROM msdb.dbo.sysjobschedules AS js
                 JOIN msdb.dbo.sysschedules AS ss ON ss.schedule_id = js.schedule_id
                 WHERE js.job_id = j.job_id ORDER BY ss.schedule_id) AS sc
    OUTER APPLY (SELECT TOP (1) jh.run_status, jh.run_date, jh.run_time, jh.run_duration, jh.message
                 FROM msdb.dbo.sysjobhistory AS jh
                 WHERE jh.job_id = j.job_id AND jh.step_id = 1 ORDER BY jh.instance_id DESC) AS h
    WHERE LEFT(j.name, LEN(@prefix)) = @prefix;
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;
SELECT @jobsError;
SELECT name, enabled, command, start_time, schedule_enabled, run_status, run_date, run_time, run_duration, message FROM @jobs ORDER BY name;`

// listTablesQuery runs inside the database and lists every user table for the archive picker: schema,
// name, rows, MB, its date columns (whether each leads an enabled, unfiltered index) and whether
// kakapo archives it (its job exists in msdb). The tables and the jobs each in their own TRY.
const listTablesQuery = `SET NOCOUNT ON;
DECLARE @tablesError nvarchar(2048), @jobsError nvarchar(2048), @prefix nvarchar(300) = N'kakapo: archive ' + DB_NAME() + N'.';
DECLARE @t TABLE (object_id int NOT NULL, schema_name sysname NOT NULL, table_name sysname NOT NULL, rows_count bigint NOT NULL, mb decimal(18, 1) NOT NULL);
DECLARE @jobs TABLE (name sysname NOT NULL);
BEGIN TRY
    INSERT @t (object_id, schema_name, table_name, rows_count, mb)
    SELECT t.object_id, s.name, t.name,
           ISNULL(SUM(CASE WHEN p.index_id IN (0, 1) THEN p.row_count ELSE 0 END), 0),
           CONVERT(decimal(18, 1), ISNULL(SUM(p.used_page_count), 0) * 8 / 1024.0)
    FROM sys.tables AS t
    JOIN sys.schemas AS s ON s.schema_id = t.schema_id
    LEFT JOIN sys.dm_db_partition_stats AS p ON p.object_id = t.object_id
    WHERE t.is_ms_shipped = 0
    GROUP BY t.object_id, s.name, t.name;
END TRY
BEGIN CATCH
    SET @tablesError = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    INSERT @jobs (name) SELECT j.name FROM msdb.dbo.sysjobs AS j WHERE LEFT(j.name, LEN(@prefix)) = @prefix;
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;
SELECT @tablesError, @jobsError;
SELECT x.object_id, x.schema_name, x.table_name, x.rows_count, x.mb,
       CAST(CASE WHEN EXISTS (SELECT 1 FROM @jobs AS j WHERE j.name = @prefix + x.schema_name + N'.' + x.table_name) THEN 1 ELSE 0 END AS bit)
FROM @t AS x ORDER BY x.mb DESC, x.schema_name, x.table_name;
SELECT c.object_id, c.name,
       CAST(CASE WHEN EXISTS (SELECT 1 FROM sys.index_columns AS ic
                              JOIN sys.indexes AS i ON i.object_id = ic.object_id AND i.index_id = ic.index_id
                              WHERE ic.object_id = c.object_id AND ic.column_id = c.column_id AND ic.key_ordinal = 1
                                AND i.is_disabled = 0 AND i.is_hypothetical = 0 AND i.has_filter = 0)
                 THEN 1 ELSE 0 END AS bit)
FROM sys.columns AS c
JOIN @t AS x ON x.object_id = c.object_id
WHERE TYPE_NAME(c.system_type_id) IN (N'datetime', N'datetime2', N'date')
ORDER BY c.object_id, c.column_id;`

// The estimate's two statements, run inside the database with @keep; {table}, {date} and {where}
// are replaced by the quoted names and the canonical condition (archiveEstimateStatement). COUNT_BIG
// on an indexed date column (a seek), with its lowest value; otherwise a 1 percent sample whose
// share of matching rows the caller scales to the table's rows.
const (
	archiveCountQuery = `SET NOCOUNT ON;
DECLARE @cutoff datetime2 = DATEADD(MONTH, -@keep, SYSDATETIME());
SELECT COUNT_BIG(*), CAST(NULL AS bigint), (SELECT CONVERT(datetime2, MIN({date})) FROM {table} WITH (READUNCOMMITTED))
FROM {table} WITH (READUNCOMMITTED) WHERE {where};`
	archiveSampleQuery = `SET NOCOUNT ON;
DECLARE @cutoff datetime2 = DATEADD(MONTH, -@keep, SYSDATETIME());
SELECT COUNT_BIG(CASE WHEN {where} THEN 1 END), COUNT_BIG(*), CAST(NULL AS datetime2)
FROM {table} TABLESAMPLE SYSTEM (1 PERCENT) WITH (READUNCOMMITTED);`
)

// archiveEstimateTimeout bounds the estimate; past it the rows due are unknown.
const archiveEstimateTimeout = 10 * time.Second

// archiveSmallTable is the size under which the estimate counts every row even without an index:
// a 1 percent sample of a small table holds too few pages to say anything.
const archiveSmallTable = 100000

// inDatabaseWithTable is the batch that runs a statement inside a database with @schema and @table.
func inDatabaseWithTable(database string) string {
	return Tag(safeSession + "EXEC " + domain.QuoteName(database) +
		".sys.sp_executesql @stmt, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;")
}

// ArchiveFacts reads what the archive fix needs about one table (docs/phase5a-contract.md): the
// instance (edition, Agent, recovery model, last log backup, room, the archive database), the table
// (rows, MB, columns, keys, triggers, features) and kakapo's archive jobs on the database. The
// archive table, which the options name, is read by ArchiveTarget; the rows due by ArchiveEstimate.
// An area that cannot be read carries its reason; the error is for a read that could not run.
func ArchiveFacts(ctx context.Context, db *sql.DB, database, schema, table string) (domain.ArchiveFacts, error) {
	f := domain.ArchiveFacts{Database: database, Schema: schema, Table: table, Columns: []domain.ArchiveColumn{},
		KeysIn: []domain.ArchiveForeignKey{}, Triggers: []domain.ArchiveTrigger{}, Jobs: []domain.ArchiveJob{}}
	var (
		recovery, logErr, agentStatus, agentErr, roomErr sql.NullString
		lastLog                                          sql.NullTime
		free, archFree                                   sql.NullInt64
		now                                              time.Time
	)
	err := db.QueryRowContext(ctx, Tag(archiveInstanceQuery), sql.Named("db", database), sql.Named("archiveDb", domain.ArchiveDatabaseName(database))).
		Scan(&f.ServerName, &f.ProductVersion, &f.EngineEdition, &now, &f.ArchiveDBExists, &recovery, &lastLog, &logErr,
			&agentStatus, &agentErr, &free, &archFree, &roomErr)
	if err != nil {
		return f, friendlyError(err)
	}
	f.ServerNow, f.RecoveryModel, f.LastLogBackup = naive(now), recovery.String, naivePtr(lastLog)
	f.LogBackupError, f.AgentStatus, f.AgentError, f.RoomError = logErr.String, agentStatus.String, agentErr.String, roomErr.String
	if free.Valid {
		f.FreeMB = &free.Int64
	}
	if archFree.Valid {
		f.ArchiveFreeMB = &archFree.Int64
	}
	if err := readArchiveTable(ctx, db, &f); err != nil {
		return f, err
	}
	jobs, jobsErr, err := ArchiveJobs(ctx, db, database, false)
	switch {
	case err != nil:
		f.JobsError = err.Error()
	case jobsErr != "":
		f.JobsError = jobsErr
	default:
		f.Jobs = jobs
	}
	return f, nil
}

func readArchiveTable(ctx context.Context, db *sql.DB, f *domain.ArchiveFacts) error {
	rows, err := db.QueryContext(ctx, inDatabaseWithTable(f.Database),
		sql.Named("stmt", Tag(archiveTableQuery)), sql.Named("schema", f.Schema), sql.Named("table", f.Table))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return fmt.Errorf("SQL Server returned no row for the table %s.%s", f.Schema, f.Table)
	}
	var temporal, tableErr, colsErr, keysErr, trgErr, featErr sql.NullString
	if err := rows.Scan(&f.Found, &f.Rows, &f.MB, &f.CDC, &f.ChangeTracking, &f.Published, &f.MergePublished, &f.Replicated,
		&f.MemoryOptimized, &temporal, &f.KeysOut, &tableErr, &colsErr, &keysErr, &trgErr, &featErr); err != nil {
		return fmt.Errorf("read the table %s.%s: %w", f.Schema, f.Table, err)
	}
	f.Temporal = temporal.String
	if strings.EqualFold(f.Temporal, "NON_TEMPORAL_TABLE") {
		f.Temporal = ""
	}
	f.TableError, f.ColumnsError, f.KeysError, f.TriggersError, f.FeaturesError = tableErr.String, colsErr.String, keysErr.String, trgErr.String, featErr.String

	if err := nextResultSet(rows, "columns"); err != nil {
		return err
	}
	for rows.Next() {
		var c domain.ArchiveColumn
		var typ sql.NullString
		if err := rows.Scan(&c.Name, &typ, &c.Identity, &c.Computed, &c.Nullable, &c.DateIndexed, &c.ClusteredKey); err != nil {
			return err
		}
		c.Type = typ.String
		f.Columns = append(f.Columns, c)
	}
	if err := nextResultSet(rows, "foreign keys"); err != nil {
		return err
	}
	for rows.Next() {
		var k domain.ArchiveForeignKey
		if err := rows.Scan(&k.Name, &k.FromTable); err != nil {
			return err
		}
		f.KeysIn = append(f.KeysIn, k)
	}
	if err := nextResultSet(rows, "triggers"); err != nil {
		return err
	}
	byName := map[string]int{}
	for rows.Next() {
		var name string
		var event sql.NullString
		var disabled, insteadOf bool
		if err := rows.Scan(&name, &event, &disabled, &insteadOf); err != nil {
			return err
		}
		i, ok := byName[name]
		if !ok {
			i = len(f.Triggers)
			byName[name] = i
			f.Triggers = append(f.Triggers, domain.ArchiveTrigger{Name: name, Enabled: !disabled, InsteadOf: insteadOf, Events: []string{}})
		}
		if event.Valid {
			f.Triggers[i].Events = append(f.Triggers[i].Events, event.String)
		}
	}
	return friendlyErrOrNil(rows.Err())
}

// ArchiveTarget reads one table of a database: whether it exists, its rows and its columns. It is
// the archive table's read (the options name it) and the row count of the Tables row.
func ArchiveTarget(ctx context.Context, db *sql.DB, database, schema, table string) (domain.ArchiveTarget, error) {
	t := domain.ArchiveTarget{Database: database, Schema: schema, Table: table, Columns: []domain.ArchiveColumn{}}
	rows, err := db.QueryContext(ctx, inDatabaseWithTable(database),
		sql.Named("stmt", Tag(archiveTargetQuery)), sql.Named("schema", schema), sql.Named("table", table))
	if err != nil {
		return t, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return t, friendlyError(err)
		}
		return t, fmt.Errorf("SQL Server returned no row for the table %s.%s", schema, table)
	}
	var errText sql.NullString
	if err := rows.Scan(&t.Exists, &t.Rows, &errText); err != nil {
		return t, err
	}
	t.Read, t.Error = true, errText.String
	if err := nextResultSet(rows, "columns"); err != nil {
		return t, err
	}
	for rows.Next() {
		var c domain.ArchiveColumn
		var typ sql.NullString
		if err := rows.Scan(&c.Name, &typ, &c.Identity); err != nil {
			return t, err
		}
		c.Type = typ.String
		t.Columns = append(t.Columns, c)
	}
	return t, friendlyErrOrNil(rows.Err())
}

// rowsMovedLine finds the step's "rows moved: n" in a job history message.
var rowsMovedLine = regexp.MustCompile(`rows moved: ([0-9]+)`)

// ArchiveJobs reads kakapo's archive jobs on a database (docs/phase5a-contract.md): each job, its
// options read back from the step's options line, its schedule and its last run. With counts, the
// rows of each table and of its archive table are counted too (the Tables row). The string is msdb's
// error when the jobs could not be read; the error is for a read that could not run.
func ArchiveJobs(ctx context.Context, db *sql.DB, database string, counts bool) ([]domain.ArchiveJob, string, error) {
	rows, err := db.QueryContext(ctx, Tag(archiveJobsQuery), sql.Named("db", database))
	if err != nil {
		return nil, "", friendlyError(err)
	}
	defer rows.Close()
	var jobsErr sql.NullString
	if rows.Next() {
		if err := rows.Scan(&jobsErr); err != nil {
			return nil, "", err
		}
	}
	if err := nextResultSet(rows, "archive jobs"); err != nil {
		return nil, "", err
	}
	jobs := []domain.ArchiveJob{}
	for rows.Next() {
		var (
			j                                domain.ArchiveJob
			command, message                 sql.NullString
			start, status, day, at, duration sql.NullInt64
			scheduleEnabled                  sql.NullBool
		)
		if err := rows.Scan(&j.Name, &j.Enabled, &command, &start, &scheduleEnabled, &status, &day, &at, &duration, &message); err != nil {
			return nil, "", fmt.Errorf("read the archive jobs: %w", err)
		}
		if !domain.IsArchiveJobName(j.Name, database) {
			continue
		}
		j.Command = command.String
		j.Options, j.OptionsOK = domain.ParseArchiveOptionsLine(j.Command)
		if start.Valid {
			j.Clock = domain.AgentClock(int(start.Int64))
		}
		j.ScheduleEnabled = scheduleEnabled.Valid && scheduleEnabled.Bool
		if day.Valid && day.Int64 > 0 {
			t := runDate(int(day.Int64)).Add(time.Duration(at.Int64/10000)*time.Hour + time.Duration(at.Int64/100%100)*time.Minute + time.Duration(at.Int64%100)*time.Second)
			j.LastRunAt = &t
			j.LastOutcome = domain.ArchiveRunOutcome(int(status.Int64))
			j.LastSeconds = domain.AgentDuration(int(duration.Int64))
			if m := rowsMovedLine.FindStringSubmatch(message.String); m != nil {
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
					j.LastRowsMoved = &n
				}
			}
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, "", friendlyError(err)
	}
	if counts {
		for i := range jobs {
			countArchiveRows(ctx, db, &jobs[i])
		}
	}
	return jobs, jobsErr.String, nil
}

// countArchiveRows counts the rows of a job's table and of its archive table; a count that cannot
// be read leaves its value nil and says why.
func countArchiveRows(ctx context.Context, db *sql.DB, j *domain.ArchiveJob) {
	if !j.OptionsOK {
		return
	}
	o := j.Options
	errs := []string{}
	if t, err := ArchiveTarget(ctx, db, o.Database, o.Schema, o.Table); err != nil || t.Error != "" {
		errs = append(errs, firstError(err, t.Error))
	} else if t.Exists {
		j.RowsKept = &t.Rows
	}
	if o.Action == domain.ArchiveActionArchive && o.TargetTable != "" {
		if t, err := ArchiveTarget(ctx, db, o.TargetDatabase, o.TargetSchema, o.TargetTable); err != nil || t.Error != "" {
			errs = append(errs, firstError(err, t.Error))
		} else if t.Exists {
			j.RowsArchived = &t.Rows
		}
	}
	j.CountError = strings.Join(errs, "; ")
}

func firstError(err error, text string) string {
	if err != nil {
		return err.Error()
	}
	return text
}

// ListTables lists every user table of a database for the archive picker: rows, MB, date columns
// and whether kakapo archives it, biggest first. The jobs that cannot be read leave Archived false
// and say why; tables that cannot be read are an error.
func ListTables(ctx context.Context, db *sql.DB, database string) (domain.TableList, error) {
	out := domain.TableList{Database: database, Tables: []domain.TableListing{}}
	rows, err := db.QueryContext(ctx, inDatabase(database), sql.Named("stmt", Tag(listTablesQuery)))
	if err != nil {
		return out, friendlyError(err)
	}
	defer rows.Close()
	var tablesErr, jobsErr sql.NullString
	if rows.Next() {
		if err := rows.Scan(&tablesErr, &jobsErr); err != nil {
			return out, err
		}
	}
	if tablesErr.Valid {
		return out, errors.New("could not read the tables: " + tablesErr.String)
	}
	out.JobsError = jobsErr.String
	if err := nextResultSet(rows, "tables"); err != nil {
		return out, err
	}
	byID := map[int64]int{}
	for rows.Next() {
		var id int64
		t := domain.TableListing{DateColumns: []domain.TableDateColumn{}}
		if err := rows.Scan(&id, &t.Schema, &t.Name, &t.Rows, &t.MB, &t.Archived); err != nil {
			return out, err
		}
		byID[id] = len(out.Tables)
		out.Tables = append(out.Tables, t)
	}
	if err := nextResultSet(rows, "date columns"); err != nil {
		return out, err
	}
	for rows.Next() {
		var id int64
		var c domain.TableDateColumn
		if err := rows.Scan(&id, &c.Name, &c.Indexed); err != nil {
			return out, err
		}
		if i, ok := byID[id]; ok {
			out.Tables[i].DateColumns = append(out.Tables[i].DateColumns, c)
		}
	}
	if err := rows.Err(); err != nil {
		return out, friendlyError(err)
	}
	domain.SortTableList(out.Tables)
	return out, nil
}

// archiveEstimateStatement builds the estimate's statement for a date column of the table read and
// a condition: the names quoted from the catalogue read, the condition only in its canonical form.
// A date column the table does not have, or a condition that fails the check, is an error and no
// statement.
func archiveEstimateStatement(f domain.ArchiveFacts, dateColumn, condition string, sample bool) (string, error) {
	var date *domain.ArchiveColumn
	for i, c := range f.Columns {
		if c.IsDate() && strings.EqualFold(c.Name, dateColumn) {
			date = &f.Columns[i]
		}
	}
	if date == nil {
		return "", fmt.Errorf("%s is not a date column of %s.%s", dateColumn, f.Schema, f.Table)
	}
	cond, err := domain.CanonicalCondition(condition, f.ColumnNames())
	if err != nil {
		return "", err
	}
	where := domain.QuoteName(date.Name) + " < @cutoff"
	if cond != "" {
		where += " AND (" + cond + ")"
	}
	q := archiveCountQuery
	if sample {
		q = archiveSampleQuery
	}
	r := strings.NewReplacer("{table}", domain.QuoteName(f.Schema)+"."+domain.QuoteName(f.Table),
		"{date}", domain.QuoteName(date.Name), "{where}", where)
	return Tag(r.Replace(q)), nil
}

// ArchiveEstimate counts the rows of the table older than keepMonths on the date column (and
// matching the condition): COUNT_BIG when the column is indexed or the table small, else a 1
// percent sample scaled to the table's rows and marked estimated. It gives up after 10 seconds
// (TimedOut); an error says why the count is unknown.
func ArchiveEstimate(ctx context.Context, db *sql.DB, f domain.ArchiveFacts, dateColumn string, keepMonths int, condition string) domain.ArchiveEstimate {
	var e domain.ArchiveEstimate
	sample := true
	for _, c := range f.Columns {
		if strings.EqualFold(c.Name, dateColumn) && c.DateIndexed {
			sample = false
		}
	}
	if f.Rows <= archiveSmallTable {
		sample = false
	}
	stmt, err := archiveEstimateStatement(f, dateColumn, condition, sample)
	if err != nil {
		e.Error = err.Error()
		return e
	}
	readCtx, cancel := context.WithTimeout(ctx, archiveEstimateTimeout)
	defer cancel()
	var due int64
	var sampled sql.NullInt64
	var oldest sql.NullTime
	err = db.QueryRowContext(readCtx, Tag(safeSession+"EXEC "+domain.QuoteName(f.Database)+".sys.sp_executesql @stmt, N'@keep int', @keep = @keep;"),
		sql.Named("stmt", stmt), sql.Named("keep", keepMonths)).Scan(&due, &sampled, &oldest)
	switch {
	case errors.Is(readCtx.Err(), context.DeadlineExceeded):
		e.TimedOut, e.Error = true, "counting took longer than 10 seconds"
		return e
	case err != nil:
		e.Error = friendlyError(err).Error()
		return e
	}
	e.Known, e.Rows, e.Oldest = true, due, naivePtr(oldest)
	if sample {
		e.Estimated = true
		if !sampled.Valid || sampled.Int64 == 0 {
			e.Known, e.Error = false, "the sample held no rows"
			return e
		}
		e.Rows = int64(float64(due) / float64(sampled.Int64) * float64(f.Rows))
	}
	return e
}
