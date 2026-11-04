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

// upkeepInstanceQuery reads what the Upkeep card needs from the instance, in one batch
// (docs/phase3d-contract.md, "Reads"): the SQL Server Agent service, the msdb jobs, job history,
// job steps, maintenance plan subplans, alerts and operators, the msdb history and what prunes it
// (MNT08), the default trace (shrinks, cache
// clears, this database's file growths, severe errors) and the memory dumps. Every msdb, trace and
// dump area is read in its own TRY, so a missing permission gives that area's error column, not a
// failed read; the msdb reads run through sp_executesql so that even msdb being closed to the
// login is caught. Nothing in msdb is read on Express (no SQL Server Agent). SELECTs only.
// Result sets: one row of facts, then the failed jobs, the shrinking job steps, the shrinks, cache
// clears, plan evictions and auto-shrinks, this database's growth events (with the slowest one),
// the severe errors and the alerts that notify no one.
// Source: DarlingData sp_PerfCheck/sp_PerfCheck.sql 1984-1985 (MIT): the plan-handle FREEPROCCACHE
// patterns that tell a single-plan eviction from a cache clear.
const upkeepInstanceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
` + openAutoClose + `
DECLARE @found bit = 0, @state nvarchar(60), @autoShrink bit = 0;
SELECT @found = 1, @state = d.state_desc, @autoShrink = d.is_auto_shrink_on FROM sys.databases AS d WHERE d.name = @db;
DECLARE @express bit = CASE WHEN CONVERT(int, SERVERPROPERTY('EngineEdition')) = 4 THEN 1 ELSE 0 END;
DECLARE @vss bit = CASE WHEN HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 1 THEN 1 ELSE 0 END;
DECLARE @noVss nvarchar(100) = N'the login does not have VIEW SERVER STATE.';
DECLARE @since int = CONVERT(int, CONVERT(char(8), DATEADD(DAY, -7, @now), 112));

/* MNT01 SQL Server Agent */
DECLARE @agentStatus nvarchar(60), @agentStartup nvarchar(60), @agentError nvarchar(2048);
IF @express = 0
BEGIN
    IF @vss = 0 SET @agentError = @noVss;
    ELSE
    BEGIN TRY
        SELECT TOP (1) @agentStatus = status_desc, @agentStartup = startup_type_desc
        FROM sys.dm_server_services WHERE servicename LIKE N'SQL Server Agent%';
    END TRY
    BEGIN CATCH
        SET @agentError = ERROR_MESSAGE();
    END CATCH;
END;

/* MNT02 Failed jobs in the last 7 days */
DECLARE @jobs TABLE (name sysname NOT NULL, fails int NOT NULL, last_date int NOT NULL, step_name sysname NULL, message nvarchar(4000) NULL);
DECLARE @jobsError nvarchar(2048);
IF @express = 0
BEGIN TRY
    INSERT @jobs (name, fails, last_date, step_name, message)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT j.name, f.Fails, f.LastDate, m.step_name, m.message
        FROM msdb.dbo.sysjobs j
        CROSS APPLY (SELECT COUNT(*) AS Fails, MAX(h.run_date) AS LastDate
                     FROM msdb.dbo.sysjobhistory h
                     WHERE h.job_id = j.job_id AND h.step_id = 0 AND h.run_status = 0 AND h.run_date >= @since) f
        OUTER APPLY (SELECT TOP (1) h.step_name, h.message
                     FROM msdb.dbo.sysjobhistory h
                     WHERE h.job_id = j.job_id AND h.step_id > 0 AND h.run_status = 0
                     ORDER BY h.run_date DESC, h.run_time DESC) m
        WHERE f.Fails > 0;', N'@since int', @since = @since;
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;

/* MNT03 Index / statistics maintenance job (heuristic on job names and commands) */
DECLARE @maintJob sysname, @plans int, @maintError nvarchar(2048);
IF @express = 0
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT TOP (1) @j = j.name
        FROM msdb.dbo.sysjobs j
        JOIN msdb.dbo.sysjobsteps s ON s.job_id = j.job_id
        WHERE j.enabled = 1
          AND (   s.command LIKE N''%IndexOptimize%'' OR s.command LIKE N''%UPDATE STATISTICS%''
               OR s.command LIKE N''%sp[_]updatestats%'' OR s.command LIKE N''%ALTER INDEX%''
               OR s.command LIKE N''%DBREINDEX%'' OR j.name LIKE N''%statistic%''
               OR j.name LIKE N''%index%'' OR j.name LIKE N''%maint%'')
        ORDER BY j.name;
        SELECT @p = COUNT(*) FROM msdb.dbo.sysmaintplan_subplans;',
        N'@j sysname OUTPUT, @p int OUTPUT', @j = @maintJob OUTPUT, @p = @plans OUTPUT;
END TRY
BEGIN CATCH
    SET @maintError = ERROR_MESSAGE();
END CATCH;

/* MNT05 Agent jobs that shrink */
DECLARE @shrinkJobs TABLE (job sysname NOT NULL, step int NOT NULL, command nvarchar(300) NULL);
DECLARE @shrinkError nvarchar(2048);
IF @express = 0
BEGIN TRY
    INSERT @shrinkJobs (job, step, command)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT j.name, s.step_id, LEFT(s.command, 300)
        FROM msdb.dbo.sysjobs j
        JOIN msdb.dbo.sysjobsteps s ON s.job_id = j.job_id
        WHERE j.enabled = 1 AND (s.command LIKE N''%SHRINKDATABASE%'' OR s.command LIKE N''%SHRINKFILE%'')
        ORDER BY j.name, s.step_id;';
END TRY
BEGIN CATCH
    SET @shrinkError = ERROR_MESSAGE();
END CATCH;

/* MNT07 Alerts for severity 19-25 and errors 823/824/825, and an operator to send them to */
DECLARE @missing nvarchar(400), @operators int, @alertsOn int, @alertsError nvarchar(2048);
DECLARE @silent TABLE (name sysname NOT NULL);
IF @express = 0
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @m = STUFF((
            SELECT N'', '' + v.Label
            FROM (VALUES (1, 19, 0, N''severity 19''), (2, 20, 0, N''20''), (3, 21, 0, N''21''), (4, 22, 0, N''22''),
                         (5, 23, 0, N''23''), (6, 24, 0, N''24''), (7, 25, 0, N''25''),
                         (8, 0, 823, N''error 823''), (9, 0, 824, N''824''), (10, 0, 825, N''825'')) v (Ord, Sev, Msg, Label)
            WHERE NOT EXISTS (SELECT 1 FROM msdb.dbo.sysalerts a
                              WHERE a.enabled = 1 AND ((v.Sev > 0 AND a.severity = v.Sev) OR (v.Msg > 0 AND a.message_id = v.Msg)))
            ORDER BY v.Ord
            FOR XML PATH(''''), TYPE).value(''.'', ''nvarchar(400)''), 1, 2, N'''');
        SELECT @o = COUNT(*) FROM msdb.dbo.sysoperators WHERE enabled = 1 AND email_address IS NOT NULL;
        SELECT @a = COUNT(*) FROM msdb.dbo.sysalerts WHERE enabled = 1;',
        N'@m nvarchar(400) OUTPUT, @o int OUTPUT, @a int OUTPUT', @m = @missing OUTPUT, @o = @operators OUTPUT, @a = @alertsOn OUTPUT;
    /* The alerts among those that notify no enabled operator with an email address by email
       (notification_method bit 1): an alert that tells no one is the same as none */
    INSERT @silent (name)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT a.name
        FROM msdb.dbo.sysalerts a
        WHERE a.enabled = 1 AND (a.severity BETWEEN 19 AND 25 OR a.message_id IN (823, 824, 825))
          AND NOT EXISTS (SELECT 1 FROM msdb.dbo.sysnotifications n
                          JOIN msdb.dbo.sysoperators o ON o.id = n.operator_id
                          WHERE n.alert_id = a.id AND (n.notification_method & 1) = 1
                            AND o.enabled = 1 AND o.email_address IS NOT NULL AND o.email_address <> N'''')
        ORDER BY a.name;';
END TRY
BEGIN CATCH
    SET @alertsError = ERROR_MESSAGE();
END CATCH;

/* MNT08 msdb history (backup and job) and whether an enabled job prunes it */
DECLARE @bhOldest datetime, @bhRows bigint, @jhRows bigint, @jhOldest int, @cleanupJob sysname, @historyError nvarchar(2048);
IF @express = 0
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @bo = MIN(backup_finish_date), @br = COUNT_BIG(*) FROM msdb.dbo.backupset;
        SELECT @jo = MIN(run_date), @jr = COUNT_BIG(*) FROM msdb.dbo.sysjobhistory;
        SELECT TOP (1) @c = j.name
        FROM msdb.dbo.sysjobs j
        JOIN msdb.dbo.sysjobsteps s ON s.job_id = j.job_id
        WHERE j.enabled = 1
          AND (s.command LIKE N''%sp[_]delete[_]backuphistory%'' OR s.command LIKE N''%sp[_]purge[_]jobhistory%'' OR s.command LIKE N''%CommandLog%'')
        ORDER BY j.name;',
        N'@bo datetime OUTPUT, @br bigint OUTPUT, @jo int OUTPUT, @jr bigint OUTPUT, @c sysname OUTPUT',
        @bo = @bhOldest OUTPUT, @br = @bhRows OUTPUT, @jo = @jhOldest OUTPUT, @jr = @jhRows OUTPUT, @c = @cleanupJob OUTPUT;
END TRY
BEGIN CATCH
    SET @historyError = ERROR_MESSAGE();
END CATCH;

/* MNT05 / MNT06 Events from the default trace (last 7 days): shrinks, cache clears and plan evictions,
   file growth (with the slowest), auto-shrink (94, 95), severe errors */
DECLARE @traceRunning bit = 0, @traceError nvarchar(2048), @tracePath nvarchar(260);
DECLARE @trace TABLE (EventClass int, StartTime datetime, DatabaseName nvarchar(256), TextData nvarchar(400),
                      LoginName nvarchar(256), ApplicationName nvarchar(256), Duration bigint, Severity int,
                      FileName nvarchar(256));
DECLARE @cmds TABLE (kind nvarchar(12) NOT NULL, database_name nvarchar(256) NULL, cnt int NOT NULL, last_at datetime NULL,
                     login_name nvarchar(256) NULL, app nvarchar(256) NULL, sample nvarchar(400) NULL);
DECLARE @growth TABLE (cnt int NOT NULL, secs bigint NULL, log_grows int NULL, last_at datetime NULL,
                       slowest_ms bigint NULL, slowest_file nvarchar(256) NULL, slowest_at datetime NULL);
DECLARE @errors TABLE (database_name nvarchar(256) NULL, cnt int NOT NULL, last_at datetime NULL, sample nvarchar(400) NULL);
IF HAS_PERMS_BY_NAME(NULL, NULL, 'ALTER TRACE') = 0
    SET @traceError = N'the login does not have ALTER TRACE.';
ELSE
BEGIN TRY
    SELECT @tracePath = REVERSE(SUBSTRING(REVERSE(path), CHARINDEX(N'\', REVERSE(path)), 260)) + N'log.trc'
    FROM sys.traces WHERE is_default = 1 AND status = 1;
    IF @tracePath IS NOT NULL
    BEGIN
        SET @traceRunning = 1;
        INSERT @trace (EventClass, StartTime, DatabaseName, TextData, LoginName, ApplicationName, Duration, Severity, FileName)
        SELECT t.EventClass, t.StartTime, t.DatabaseName, CONVERT(nvarchar(400), t.TextData), t.LoginName, t.ApplicationName,
               t.Duration, t.Severity, t.FileName
        FROM sys.fn_trace_gettable(@tracePath, DEFAULT) t
        WHERE t.StartTime >= DATEADD(DAY, -7, GETDATE()) AND t.EventClass IN (22, 92, 93, 94, 95, 116);

        /* A FREEPROCCACHE with a plan handle (or a variable holding one) evicts one plan, not the cache */
        INSERT @cmds (kind, database_name, cnt, last_at, login_name, app, sample)
        SELECT k.kind, t.DatabaseName, COUNT(*), MAX(t.StartTime), MAX(t.LoginName), MAX(t.ApplicationName), MAX(t.TextData)
        FROM @trace AS t
        CROSS APPLY (SELECT CASE WHEN t.TextData LIKE N'%SHRINK%' THEN N'shrink'
                                 WHEN t.TextData LIKE N'%FREEPROCCACHE%(%0x%' OR t.TextData LIKE N'%FREEPROCCACHE%(%@%' THEN N'evict'
                                 ELSE N'cache' END) AS k (kind)
        WHERE t.EventClass = 116
          AND (t.TextData LIKE N'%SHRINK%' OR t.TextData LIKE N'%FREEPROCCACHE%' OR t.TextData LIKE N'%DROPCLEANBUFFERS%' OR t.TextData LIKE N'%FREESYSTEMCACHE%')
        GROUP BY k.kind, t.DatabaseName;

        INSERT @cmds (kind, database_name, cnt, last_at, login_name, app, sample)
        SELECT N'autoshrink', DatabaseName, COUNT(*), MAX(StartTime), MAX(LoginName), MAX(ApplicationName), NULL
        FROM @trace WHERE EventClass IN (94, 95) GROUP BY DatabaseName;

        /* Duration is in microseconds; the slowest growth is evidence (sp_PerfCheck reads the same) */
        DECLARE @slowMs bigint, @slowFile nvarchar(256), @slowAt datetime;
        SELECT TOP (1) @slowMs = ISNULL(Duration, 0) / 1000, @slowFile = FileName, @slowAt = StartTime
        FROM @trace WHERE EventClass IN (92, 93) AND DatabaseName = @db ORDER BY Duration DESC;
        INSERT @growth (cnt, secs, log_grows, last_at, slowest_ms, slowest_file, slowest_at)
        SELECT COUNT(*), SUM(ISNULL(Duration, 0)) / 1000000, SUM(CASE WHEN EventClass = 93 THEN 1 ELSE 0 END), MAX(StartTime),
               @slowMs, @slowFile, @slowAt
        FROM @trace WHERE EventClass IN (92, 93) AND DatabaseName = @db
        HAVING COUNT(*) > 0;

        INSERT @errors (database_name, cnt, last_at, sample)
        SELECT DatabaseName, COUNT(*), MAX(StartTime), MAX(TextData)
        FROM @trace WHERE EventClass = 22 AND Severity >= 20 GROUP BY DatabaseName;
    END;
END TRY
BEGIN CATCH
    SET @traceError = ERROR_MESSAGE();
END CATCH;

/* MNT06 Memory dumps (crashes) in the last 30 days; creation_time in the server's own clock */
DECLARE @dumps int, @dumpLast datetime, @dumpFile nvarchar(260), @dumpsError nvarchar(2048);
IF @vss = 0 SET @dumpsError = @noVss;
ELSE
BEGIN TRY
    SELECT @dumps = COUNT(*), @dumpLast = MAX(CONVERT(datetime, creation_time))
    FROM sys.dm_server_memory_dumps WHERE CONVERT(datetime, creation_time) >= DATEADD(DAY, -30, GETDATE());
    SELECT TOP (1) @dumpFile = filename FROM sys.dm_server_memory_dumps ORDER BY creation_time DESC;
END TRY
BEGIN CATCH
    SET @dumpsError = ERROR_MESSAGE();
END CATCH;

SELECT @found, @autoShrink,
       CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)),
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')),
       @state,
       @agentStatus, @agentStartup, @agentError,
       @jobsError,
       @maintJob, ISNULL(@plans, 0), @maintError,
       @shrinkError,
       @missing, ISNULL(@operators, 0), ISNULL(@alertsOn, 0), @alertsError,
       @bhOldest, ISNULL(@bhRows, 0), ISNULL(@jhRows, 0), ISNULL(@jhOldest, 0), @cleanupJob, @historyError,
       @traceRunning, @traceError,
       ISNULL(@dumps, 0), @dumpLast, @dumpFile, @dumpsError,
       @now, DATEPART(TZOFFSET, SYSDATETIMEOFFSET());
SELECT name, fails, last_date, step_name, message FROM @jobs ORDER BY name;
SELECT job, step, command FROM @shrinkJobs ORDER BY job, step;
SELECT kind, database_name, cnt, last_at, login_name, app, sample FROM @cmds ORDER BY kind DESC, database_name;
SELECT cnt, secs, log_grows, last_at, slowest_ms, slowest_file, slowest_at FROM @growth;
SELECT database_name, cnt, last_at, sample FROM @errors ORDER BY database_name;
SELECT name FROM @silent ORDER BY name;`

// staleStatsPredicate is the scan's MNT04 rule (and PerformanceTriage.sql's STA01, Apply-DoNow step 5):
// statistics on user tables with 10,000 rows or more, at least 20% of them modified, not updated
// for 7 days.
const staleStatsPredicate = `FROM sys.stats AS s
JOIN sys.objects AS o ON o.object_id = s.object_id
CROSS APPLY sys.dm_db_stats_properties(s.object_id, s.stats_id) AS sp
WHERE o.type = 'U' AND o.is_ms_shipped = 0
  AND sp.rows >= 10000
  AND sp.modification_counter >= sp.rows * 0.2
  AND sp.last_updated < DATEADD(DAY, -7, GETDATE())`

// staleStatsQuery runs inside the database (EXEC [db].sys.sp_executesql): the out-of-date
// statistics with their table's row count from sys.dm_db_partition_stats.
const staleStatsQuery = `SET NOCOUNT ON;
SELECT OBJECT_SCHEMA_NAME(s.object_id), OBJECT_NAME(s.object_id), s.name, sp.rows, sp.modification_counter, sp.last_updated,
       ISNULL((SELECT SUM(p.row_count) FROM sys.dm_db_partition_stats AS p WHERE p.object_id = s.object_id AND p.index_id IN (0, 1)), 0)
` + staleStatsPredicate + `
ORDER BY 1, 2, 3;`

// staleStatsCountQuery counts the out-of-date statistics inside the database: the fix's read-back.
const staleStatsCountQuery = `SET NOCOUNT ON;
SELECT COUNT(*) ` + staleStatsPredicate + `;`

// inDatabase is the batch that runs @stmt inside a database.
func inDatabase(database string) string {
	return Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt;")
}

// UpkeepFacts reads what the Upkeep card needs about one database and its instance
// (docs/phase3d-contract.md), SELECTs only, in two batches: the instance, then the out-of-date
// statistics inside the database when it is online. An area the login may not read carries its
// reason in the facts instead of failing the read; the error returned is for a read that could
// not run at all, or a database that does not exist. CanChange is left for the caller.
func UpkeepFacts(ctx context.Context, db *sql.DB, database string) (domain.UpkeepFacts, error) {
	f := domain.UpkeepFacts{Database: database}
	if err := readUpkeepInstance(ctx, db, database, &f); err != nil {
		return f, err
	}
	if f.State == "ONLINE" {
		stats, err := ReadStaleStats(ctx, db, database)
		if err != nil {
			f.StatsError = err.Error()
		} else {
			f.StaleStats = stats
		}
	}
	return f, nil
}

func readUpkeepInstance(ctx context.Context, db *sql.DB, database string, f *domain.UpkeepFacts) error {
	rows, err := db.QueryContext(ctx, Tag(upkeepInstanceQuery), sql.Named("db", database))
	if err != nil {
		return friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return friendlyError(err)
		}
		return errors.New("SQL Server returned no upkeep facts")
	}
	var (
		found                                         bool
		state, agentStatus, agentStartup, agentErr    sql.NullString
		jobsErr, maintJob, maintErr, shrinkErr        sql.NullString
		missing, alertsErr, traceErr, dumpFile, dumpE sql.NullString
		dumpLast                                      sql.NullTime
		bhOldest                                      sql.NullTime
		jhOldest                                      int
		cleanupJob, historyErr                        sql.NullString
	)
	if err := rows.Scan(&found, &f.AutoShrinkOn, &f.ServerName, &f.ProductVersion, &f.EngineEdition, &state,
		&agentStatus, &agentStartup, &agentErr,
		&jobsErr,
		&maintJob, &f.MaintenancePlans, &maintErr,
		&shrinkErr,
		&missing, &f.Operators, &f.AlertsOn, &alertsErr,
		&bhOldest, &f.BackupHistoryRows, &f.JobHistoryRows, &jhOldest, &cleanupJob, &historyErr,
		&f.TraceRunning, &traceErr,
		&f.Dumps, &dumpLast, &dumpFile, &dumpE,
		&f.ServerNow, &f.UTCOffsetMinutes); err != nil {
		return fmt.Errorf("read the upkeep facts of %s: %w", database, err)
	}
	if !found {
		return fmt.Errorf("there is no database named %q", database)
	}
	f.ServerNow = naive(f.ServerNow)
	f.State = state.String
	f.AgentStatus, f.AgentStartup, f.AgentError = agentStatus.String, agentStartup.String, agentErr.String
	f.JobsError = jobsErr.String
	f.MaintenanceJob, f.MaintenanceError = maintJob.String, maintErr.String
	f.ShrinkJobsError = shrinkErr.String
	f.AlertsError = alertsErr.String
	f.MissingAlerts = []string{}
	if missing.String != "" {
		f.MissingAlerts = strings.Split(missing.String, ", ")
	}
	f.BackupHistoryOldest = naivePtr(bhOldest)
	if jhOldest > 0 {
		t := runDate(jhOldest)
		f.JobHistoryOldest = &t
	}
	f.CleanupJob, f.HistoryError = cleanupJob.String, historyErr.String
	f.TraceError = traceErr.String
	f.DumpLast, f.DumpFile, f.DumpsError = naivePtr(dumpLast), dumpFile.String, dumpE.String

	// Failed jobs.
	if err := nextResultSet(rows, "failed jobs"); err != nil {
		return err
	}
	f.FailedJobs = []domain.FailedJob{}
	for rows.Next() {
		var j domain.FailedJob
		var lastDate int
		var step, msg sql.NullString
		if err := rows.Scan(&j.Name, &j.Fails, &lastDate, &step, &msg); err != nil {
			return err
		}
		j.LastDate = runDate(lastDate)
		j.Step, j.Message, j.HasStep = step.String, msg.String, step.Valid && msg.Valid
		f.FailedJobs = append(f.FailedJobs, j)
	}

	// Shrinking job steps.
	if err := nextResultSet(rows, "shrinking jobs"); err != nil {
		return err
	}
	f.ShrinkJobs = []domain.ShrinkJob{}
	for rows.Next() {
		var j domain.ShrinkJob
		var cmd sql.NullString
		if err := rows.Scan(&j.Job, &j.Step, &cmd); err != nil {
			return err
		}
		j.Command = cmd.String
		f.ShrinkJobs = append(f.ShrinkJobs, j)
	}

	// Shrinks and cache clears.
	if err := nextResultSet(rows, "trace commands"); err != nil {
		return err
	}
	f.TraceCommands = []domain.TraceCommand{}
	for rows.Next() {
		var x domain.TraceCommand
		var dbName, login, app, sample sql.NullString
		var last sql.NullTime
		if err := rows.Scan(&x.Kind, &dbName, &x.Count, &last, &login, &app, &sample); err != nil {
			return err
		}
		x.Database, x.Login, x.App, x.Sample = dbName.String, login.String, app.String, sample.String
		if last.Valid {
			x.LastAt = naive(last.Time)
		}
		f.TraceCommands = append(f.TraceCommands, x)
	}

	// This database's growth events.
	if err := nextResultSet(rows, "growth events"); err != nil {
		return err
	}
	for rows.Next() {
		var g domain.TraceGrowth
		var secs, slowMs sql.NullInt64
		var logGrows sql.NullInt32
		var last, slowAt sql.NullTime
		var slowFile sql.NullString
		if err := rows.Scan(&g.Count, &secs, &logGrows, &last, &slowMs, &slowFile, &slowAt); err != nil {
			return err
		}
		g.Seconds, g.LogGrows = secs.Int64, int(logGrows.Int32)
		if last.Valid {
			g.LastAt = naive(last.Time)
		}
		g.SlowestMs, g.SlowestFile = slowMs.Int64, slowFile.String
		if slowAt.Valid {
			g.SlowestAt = naive(slowAt.Time)
		}
		f.Growth = &g
	}

	// Severe errors.
	if err := nextResultSet(rows, "severe errors"); err != nil {
		return err
	}
	f.SevereErrors = []domain.SevereErrors{}
	for rows.Next() {
		var e domain.SevereErrors
		var dbName, sample sql.NullString
		var last sql.NullTime
		if err := rows.Scan(&dbName, &e.Count, &last, &sample); err != nil {
			return err
		}
		e.Database, e.Sample = dbName.String, sample.String
		if last.Valid {
			e.LastAt = naive(last.Time)
		}
		f.SevereErrors = append(f.SevereErrors, e)
	}

	// Alerts that notify no one.
	if err := nextResultSet(rows, "silent alerts"); err != nil {
		return err
	}
	f.SilentAlerts = []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		f.SilentAlerts = append(f.SilentAlerts, name)
	}
	return friendlyErrOrNil(rows.Err())
}

// runDate turns msdb's run_date (yyyymmdd as an int) into a date.
func runDate(v int) time.Time {
	return time.Date(v/10000, time.Month(v/100%100), v%100, 0, 0, 0, 0, time.UTC)
}

// ReadStaleStats reads the out-of-date statistics inside a database (MNT04), SELECTs only.
func ReadStaleStats(ctx context.Context, db *sql.DB, database string) ([]domain.StaleStat, error) {
	rows, err := db.QueryContext(ctx, inDatabase(database), sql.Named("stmt", Tag(staleStatsQuery)))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.StaleStat{}
	for rows.Next() {
		var s domain.StaleStat
		var schema, table sql.NullString
		if err := rows.Scan(&schema, &table, &s.Stats, &s.Rows, &s.Modifications, &s.LastUpdated, &s.TableRows); err != nil {
			return nil, err
		}
		if !schema.Valid || !table.Valid {
			continue // dropped while we read
		}
		s.Schema, s.Table, s.LastUpdated = schema.String, table.String, naive(s.LastUpdated)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, friendlyError(err)
	}
	return out, nil
}

// ReadStaleStatsCount counts the out-of-date statistics inside a database: the statistics fix's
// read-back. SELECTs only.
func ReadStaleStatsCount(ctx context.Context, db *sql.DB, database string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, inDatabase(database), sql.Named("stmt", Tag(staleStatsCountQuery))).Scan(&n); err != nil {
		return 0, friendlyError(err)
	}
	return n, nil
}

// ExecFixInDatabase runs one fix statement that a preview built and the user confirmed inside a
// database (EXEC [db].sys.sp_executesql), as one batch, through ExecFix.
func ExecFixInDatabase(ctx context.Context, db *sql.DB, database, statement string) error {
	return ExecFix(ctx, db, "EXEC "+domain.QuoteName(database)+".sys.sp_executesql @stmt;", sql.Named("stmt", Tag(statement)))
}
