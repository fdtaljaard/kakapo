package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// backupFactsQuery reads everything the Backups card needs about one database in one batch, from
// the instance, never inside the database: sys.databases, DATABASEPROPERTYEX, sys.master_files and
// the msdb backup history, all against the server's GETDATE() read once at the top. A login that
// cannot read msdb gets the reason in the msdb error column instead of a failed batch.
// The first result set is one row of facts, the second the restorable points of the last 7 days,
// the third the full backups written to NUL in the last 30 days (BAK06). A full backup to NUL, and
// every differential based on one, is not a restorable point (the rule from DarlingData
// TestBackupPerformance.sql 942, which backs up to DISK = N'NUL' to time a backup).
// The backup schedule fix (docs/phase3g-contract.md) adds, each area in its own TRY: the Agent
// service, the Agent jobs that back this database up (the fourth result set), kakapo's own jobs on
// it (the fifth), maintenance plans, an operator and the default backup folder; and the data file
// paths (the sixth). Express has no Agent, so its jobs are not read.
const backupFactsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @now datetime = GETDATE();
DECLARE @since datetime = DATEADD(DAY, -7, @now);
DECLARE @dbid int, @create datetime, @recovery nvarchar(60), @msdbError nvarchar(2048),
        @lastFull datetime, @lastDiff datetime, @lastLog datetime,
        @fulls int, @diffs int, @logs int, @suspect int, @suspectLast datetime,
        @lastRealFull datetime, @durability nvarchar(60), @durabilityError nvarchar(2048);
DECLARE @points TABLE (t datetime NOT NULL);
DECLARE @nul TABLE (t datetime NOT NULL);

SELECT @dbid = d.database_id, @create = d.create_date, @recovery = d.recovery_model_desc
FROM sys.databases AS d WHERE d.name = @db;

/* An AUTO_CLOSE database that is closed reports no CHECKDB date. Open it with a statement that
   returns nothing, so the overview and the card page see the same answer. */
IF EXISTS (SELECT 1 FROM sys.databases WHERE database_id = @dbid AND is_auto_close_on = 1 AND state = 0)
BEGIN TRY
    DECLARE @open nvarchar(400) = N'EXEC ' + QUOTENAME(@db) + N'.sys.sp_executesql N''/* kakapo */ DECLARE @opened int = 1;'';';
    EXEC sp_executesql @open;
END TRY
BEGIN CATCH
END CATCH;

BEGIN TRY
    SELECT @lastFull = MAX(CASE WHEN bs.type = 'D' THEN bs.backup_finish_date END),
           @lastDiff = MAX(CASE WHEN bs.type = 'I' THEN bs.backup_finish_date END),
           @lastLog  = MAX(CASE WHEN bs.type = 'L' THEN bs.backup_finish_date END)
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = @db AND bs.backup_finish_date >= @create;

    SELECT @fulls = COUNT(CASE WHEN bs.type = 'D' THEN 1 END),
           @diffs = COUNT(CASE WHEN bs.type = 'I' THEN 1 END),
           @logs  = COUNT(CASE WHEN bs.type = 'L' THEN 1 END)
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = @db AND bs.backup_finish_date >= @since;

    /* A full backup to the NUL device is thrown away, yet it becomes the base of the next
       differentials: neither it nor a differential based on it can be restored (BAK06). */
    INSERT @points (t)
    SELECT bs.backup_finish_date
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = @db AND bs.backup_finish_date >= @since AND bs.is_copy_only = 0
      AND (bs.type IN ('D', 'I') OR (bs.type = 'L' AND @recovery IN (N'FULL', N'BULK_LOGGED')))
      AND NOT (bs.type = 'D' AND EXISTS (SELECT 1 FROM msdb.dbo.backupmediafamily AS mf
                                         WHERE mf.media_set_id = bs.media_set_id AND mf.physical_device_name IN (N'NUL', N'NUL:')))
      AND NOT (bs.type = 'I' AND EXISTS (SELECT 1 FROM msdb.dbo.backupset AS nb
                                         JOIN msdb.dbo.backupmediafamily AS nm ON nm.media_set_id = nb.media_set_id
                                         WHERE nb.backup_set_uuid = bs.differential_base_guid AND nb.type = 'D'
                                           AND nm.physical_device_name IN (N'NUL', N'NUL:')));

    INSERT @nul (t)
    SELECT bs.backup_finish_date
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = @db AND bs.type = 'D' AND bs.is_copy_only = 0
      AND bs.backup_finish_date >= DATEADD(DAY, -30, @now)
      AND EXISTS (SELECT 1 FROM msdb.dbo.backupmediafamily AS mf
                  WHERE mf.media_set_id = bs.media_set_id AND mf.physical_device_name IN (N'NUL', N'NUL:'));

    SELECT @lastRealFull = MAX(bs.backup_finish_date)
    FROM msdb.dbo.backupset AS bs
    WHERE bs.database_name = @db AND bs.type = 'D' AND bs.is_copy_only = 0 AND bs.backup_finish_date >= @create
      AND NOT EXISTS (SELECT 1 FROM msdb.dbo.backupmediafamily AS mf
                      WHERE mf.media_set_id = bs.media_set_id AND mf.physical_device_name IN (N'NUL', N'NUL:'));

    SELECT @suspect = COUNT(*), @suspectLast = MAX(sp.last_update_date)
    FROM msdb.dbo.suspect_pages AS sp
    WHERE sp.database_id = @dbid;
END TRY
BEGIN CATCH
    SET @msdbError = ERROR_MESSAGE();
END CATCH;

/* DBC12 delayed durability: the column exists from SQL Server 2014, so it is named only in dynamic SQL */
BEGIN TRY
    IF COL_LENGTH(N'sys.databases', N'delayed_durability_desc') IS NOT NULL
        EXEC sys.sp_executesql N'/* kakapo */ SELECT @d = CAST(delayed_durability_desc AS nvarchar(60)) FROM sys.databases WHERE database_id = @id;',
             N'@d nvarchar(60) OUTPUT, @id int', @d = @durability OUTPUT, @id = @dbid;
END TRY
BEGIN CATCH
    SET @durabilityError = ERROR_MESSAGE();
END CATCH;

/* Phase 3g, the backup schedule fix */
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

/* Agent jobs, enabled or not, with a step that backs up (native BACKUP or Ola's DatabaseBackup)
   and names this database ([db], 'db' or N'db', or the bare name as a word) or covers them all
   (USER_DATABASES, ALL_DATABASES); and the jobs kakapo created for this database. */
DECLARE @jobs TABLE (name sysname NOT NULL, enabled bit NOT NULL, has_full int NOT NULL, has_log int NOT NULL);
DECLARE @kakapoJobs TABLE (name sysname NOT NULL, command nvarchar(max) NULL, cleanup nvarchar(max) NULL,
                           start_time int NULL, subday_interval int NULL);
DECLARE @jobsError nvarchar(2048);
DECLARE @dbLike nvarchar(1000) = REPLACE(REPLACE(REPLACE(@db, N'[', N'[[]'), N'%', N'[%]'), N'_', N'[_]');
IF @express = 0
BEGIN TRY
    INSERT @jobs (name, enabled, has_full, has_log)
    SELECT j.name, j.enabled,
           MAX(CASE WHEN s.command LIKE N'%BACKUP DATABASE%' OR s.command LIKE N'%@BackupType%=%''FULL''%' THEN 1 ELSE 0 END),
           MAX(CASE WHEN s.command LIKE N'%BACKUP LOG%' OR s.command LIKE N'%@BackupType%=%''LOG''%' THEN 1 ELSE 0 END)
    FROM msdb.dbo.sysjobs AS j
    JOIN msdb.dbo.sysjobsteps AS s ON s.job_id = j.job_id
    WHERE (s.command LIKE N'%BACKUP DATABASE%' OR s.command LIKE N'%BACKUP LOG%' OR s.command LIKE N'%DatabaseBackup%')
      AND (   CHARINDEX(QUOTENAME(@db), s.command) > 0
           OR CHARINDEX(N'''' + @db + N'''', s.command) > 0
           OR N' ' + s.command + N' ' LIKE N'%[^0-9A-Za-z_@#$]' + @dbLike + N'[^0-9A-Za-z_@#$]%'
           OR s.command LIKE N'%USER[_]DATABASES%' OR s.command LIKE N'%ALL[_]DATABASES%')
    GROUP BY j.name, j.enabled;

    /* kakapo's jobs: step 1 (the backup), step 2 (the cleanup, native only) and the schedule, so
       the remove fix's rollback rebuilds them with the same folder, time, interval and days */
    INSERT @kakapoJobs (name, command, cleanup, start_time, subday_interval)
    SELECT j.name,
           (SELECT TOP (1) s.command FROM msdb.dbo.sysjobsteps AS s WHERE s.job_id = j.job_id ORDER BY s.step_id),
           (SELECT TOP (1) s.command FROM msdb.dbo.sysjobsteps AS s WHERE s.job_id = j.job_id AND s.step_id > 1 ORDER BY s.step_id),
           sc.active_start_time, sc.freq_subday_interval
    FROM msdb.dbo.sysjobs AS j
    OUTER APPLY (SELECT TOP (1) ss.active_start_time, ss.freq_subday_interval
                 FROM msdb.dbo.sysjobschedules AS js
                 JOIN msdb.dbo.sysschedules AS ss ON ss.schedule_id = js.schedule_id
                 WHERE js.job_id = j.job_id ORDER BY ss.schedule_id) AS sc
    WHERE j.name IN (N'kakapo: ' + @db + N' full backup', N'kakapo: ' + @db + N' log backup');
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;

/* Maintenance plans may back up too; kakapo does not parse them */
DECLARE @plans int, @plansError nvarchar(2048);
IF @express = 0
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @p = COUNT(*) FROM msdb.dbo.sysmaintplan_subplans;', N'@p int OUTPUT', @p = @plans OUTPUT;
END TRY
BEGIN CATCH
    SET @plansError = ERROR_MESSAGE();
END CATCH;

/* The operator the jobs notify: the first enabled one with an email address */
DECLARE @operator sysname, @operatorError nvarchar(2048);
IF @express = 0
BEGIN TRY
    SELECT TOP (1) @operator = o.name
    FROM msdb.dbo.sysoperators AS o
    WHERE o.enabled = 1 AND o.email_address IS NOT NULL AND o.email_address <> N''
    ORDER BY o.name;
END TRY
BEGIN CATCH
    SET @operatorError = ERROR_MESSAGE();
END CATCH;

/* The instance's default backup folder, which the preview suggests; NULL when refused */
DECLARE @backupDir nvarchar(4000);
DECLARE @reg TABLE (value nvarchar(400) NULL, data nvarchar(4000) NULL);
BEGIN TRY
    INSERT @reg (value, data)
    EXEC master.dbo.xp_instance_regread N'HKEY_LOCAL_MACHINE', N'Software\Microsoft\MSSQLServer\MSSQLServer', N'BackupDirectory';
    SELECT TOP (1) @backupDir = data FROM @reg;
END TRY
BEGIN CATCH
    SET @backupDir = NULL;
END CATCH;

SELECT CAST(d.state_desc AS nvarchar(60)),
       CAST(d.recovery_model_desc AS nvarchar(60)),
       d.create_date,
       CAST(CASE WHEN d.replica_id IS NULL THEN 0 ELSE 1 END AS bit),
       CONVERT(datetime, DATABASEPROPERTYEX(d.name, 'LastGoodCheckDbTime')),
       (SELECT SUM(CONVERT(bigint, f.size) * 8 / 1024) FROM sys.master_files AS f
        WHERE f.database_id = d.database_id AND f.type_desc = N'LOG'),
       @now,
       DATEPART(TZOFFSET, SYSDATETIMEOFFSET()),
       @msdbError,
       @lastFull, @lastDiff, @lastLog,
       ISNULL(@fulls, 0), ISNULL(@diffs, 0), ISNULL(@logs, 0),
       ISNULL(@suspect, 0), @suspectLast,
       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       @lastRealFull, @durability, @durabilityError,
       COALESCE(@@SERVERNAME, CAST(SERVERPROPERTY('ServerName') AS nvarchar(256))),
       CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
       CAST(CASE WHEN @express = 1 THEN 0 ELSE 1 END AS bit),
       @agentStatus, @agentError, @jobsError,
       ISNULL(@plans, 0), @plansError,
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.DatabaseBackup') IS NULL THEN 0 ELSE 1 END AS bit),
       @operator, @operatorError, @backupDir,
       /* Web and Express cannot compress backups, nor anything before SQL Server 2008 */
       CAST(CASE WHEN CAST(SERVERPROPERTY('Edition') AS nvarchar(128)) LIKE N'Web%' OR @express = 1
                   OR CONVERT(int, PARSENAME(CONVERT(nvarchar(64), SERVERPROPERTY('ProductVersion')), 4)) < 10
                 THEN 0 ELSE 1 END AS bit)
FROM sys.databases AS d WHERE d.database_id = @dbid;

SELECT t FROM @points ORDER BY t;
SELECT t FROM @nul ORDER BY t;
SELECT name, enabled, has_full, has_log FROM @jobs ORDER BY name;
SELECT name, command, cleanup, start_time, subday_interval FROM @kakapoJobs ORDER BY name;
SELECT CAST(f.physical_name AS nvarchar(260)) FROM sys.master_files AS f WHERE f.database_id = @dbid AND f.type = 0 ORDER BY f.file_id;`

// BackupFacts reads what the Backups card needs about one database (docs/phase3a-contract.md),
// SELECTs only. Every time is the server's own clock; ServerNow is its GETDATE() from the same
// batch. msdb that cannot be read sets MsdbError rather than failing.
func BackupFacts(ctx context.Context, db *sql.DB, database string) (domain.BackupFacts, error) {
	f := domain.BackupFacts{Database: database}
	rows, err := db.QueryContext(ctx, Tag(backupFactsQuery), sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, fmt.Errorf("there is no database named %q", database)
	}
	var (
		checkDb, lastFull, lastDiff, lastLog, suspectLast sql.NullTime
		lastRealFull                                      sql.NullTime
		logMB                                             sql.NullInt64
		msdbError, durability, durabilityErr              sql.NullString
		serverName, edition, agentStatus, agentErr        sql.NullString
		jobsErr, plansErr, operator, operatorErr, dir     sql.NullString
		create, now                                       time.Time
	)
	if err := rows.Scan(&f.State, &f.RecoveryModel, &create, &f.IsAgMember, &checkDb, &logMB, &now,
		&f.UTCOffsetMinutes, &msdbError, &lastFull, &lastDiff, &lastLog,
		&f.Fulls7, &f.Diffs7, &f.Logs7, &f.SuspectPages, &suspectLast, &f.EngineVersion,
		&lastRealFull, &durability, &durabilityErr,
		&serverName, &edition, &f.HasAgent, &agentStatus, &agentErr, &jobsErr,
		&f.MaintenancePlans, &plansErr, &f.OlaBackup, &operator, &operatorErr, &dir, &f.CompressionOK); err != nil {
		return f, fmt.Errorf("read the backup facts of %s: %w", database, err)
	}
	f.ServerName, f.Edition = serverName.String, edition.String
	f.AgentStatus, f.AgentError = agentStatus.String, agentErr.String
	f.AgentRunning = f.AgentStatus == "Running"
	f.JobsError, f.PlansError = jobsErr.String, plansErr.String
	f.Operator, f.OperatorError = operator.String, operatorErr.String
	f.DefaultBackupDir = dir.String
	f.CreateDate, f.ServerNow = naive(create), naive(now)
	f.LastGoodCheckDb = naivePtr(checkDb)
	f.LastFull, f.LastDiff, f.LastLog = naivePtr(lastFull), naivePtr(lastDiff), naivePtr(lastLog)
	f.SuspectLast = naivePtr(suspectLast)
	if logMB.Valid {
		f.LogSizeMB = &logMB.Int64
	}
	f.MsdbError = msdbError.String
	f.LastRealFull = naivePtr(lastRealFull)
	f.DelayedDurability, f.DurabilityError = durability.String, durabilityErr.String

	// The driver reaches the next result set only once this one is read to its end.
	for rows.Next() {
	}
	if !rows.NextResultSet() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no backup history result")
	}
	f.RestorePoints = []time.Time{}
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return f, fmt.Errorf("read the backup history of %s: %w", database, err)
		}
		f.RestorePoints = append(f.RestorePoints, naive(t))
	}
	if err := nextResultSet(rows, "NUL backups"); err != nil {
		return f, err
	}
	f.NulFulls = []time.Time{}
	for rows.Next() {
		var t time.Time
		if err := rows.Scan(&t); err != nil {
			return f, fmt.Errorf("read the NUL backups of %s: %w", database, err)
		}
		f.NulFulls = append(f.NulFulls, naive(t))
	}
	if err := nextResultSet(rows, "backup jobs"); err != nil {
		return f, err
	}
	f.BackupJobs = []domain.BackupJob{}
	for rows.Next() {
		var j domain.BackupJob
		var full, log int
		if err := rows.Scan(&j.Name, &j.Enabled, &full, &log); err != nil {
			return f, fmt.Errorf("read the backup jobs of %s: %w", database, err)
		}
		switch {
		case full == 1 && log == 1:
			j.Kind = domain.JobBoth
		case full == 1:
			j.Kind = domain.JobFull
		case log == 1:
			j.Kind = domain.JobLog
		}
		f.BackupJobs = append(f.BackupJobs, j)
	}
	if err := nextResultSet(rows, "kakapo jobs"); err != nil {
		return f, err
	}
	f.KakapoJobs = []string{}
	for rows.Next() {
		var name string
		var command, cleanup sql.NullString
		var start, interval sql.NullInt64
		if err := rows.Scan(&name, &command, &cleanup, &start, &interval); err != nil {
			return f, fmt.Errorf("read kakapo's jobs on %s: %w", database, err)
		}
		if len(f.KakapoJobs) == 0 {
			// The full backup job's when it exists: it sorts first.
			f.KakapoCommand = command.String
			if cleanup.Valid {
				f.KakapoCommand += "\n" + cleanup.String
			}
		}
		switch {
		case name == domain.BackupJobName(database, domain.JobFull) && start.Valid:
			f.KakapoFullTime = domain.AgentClock(int(start.Int64))
		case name == domain.BackupJobName(database, domain.JobLog) && interval.Valid:
			f.KakapoLogMinutes = int(interval.Int64)
		}
		f.KakapoJobs = append(f.KakapoJobs, name)
	}
	if err := nextResultSet(rows, "data files"); err != nil {
		return f, err
	}
	f.DataFiles = []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return f, fmt.Errorf("read the data files of %s: %w", database, err)
		}
		f.DataFiles = append(f.DataFiles, path)
	}
	if err := rows.Err(); err != nil {
		return f, friendlyError(err)
	}
	f.JobsRead = true
	if f.MsdbError != "" {
		f.RestorePoints, f.NulFulls, f.LastRealFull = []time.Time{}, []time.Time{}, nil
	}
	return f, nil
}

// folderExistsQuery asks SQL Server whether a folder exists as its service account sees it
// (xp_fileexist: File Exists, File is a Directory, Parent Directory Exists). SELECT only.
const folderExistsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @r TABLE (file_exists int NULL, is_directory int NULL, parent_exists int NULL);
INSERT @r (file_exists, is_directory, parent_exists) EXEC master.dbo.xp_fileexist @folder;
SELECT CAST(ISNULL(MAX(is_directory), 0) AS bit), CAST(ISNULL(MAX(parent_exists), 0) AS bit) FROM @r;`

// FolderExists says whether a folder exists on the server as the SQL Server service sees it: the
// backup schedule fix's check before its preview (docs/phase3g-contract.md).
func FolderExists(ctx context.Context, db *sql.DB, folder string) (bool, error) {
	var isDir, parent bool
	if err := db.QueryRowContext(ctx, Tag(folderExistsQuery), sql.Named("folder", folder)).Scan(&isDir, &parent); err != nil {
		return false, friendlyError(err)
	}
	return isDir, nil
}

// backupJobsQuery reads kakapo's two backup jobs on one database back after a fix: whether each
// exists and is enabled, its schedule enabled, and the next run Agent worked out (0 before it
// has); then the server's GETDATE(). SELECT only.
const backupJobsQuery = safeSession + `SET NOCOUNT ON;
SELECT j.name, CAST(j.enabled AS bit), CAST(ISNULL(sc.enabled, 0) AS bit), ISNULL(js.next_run_date, 0), ISNULL(js.next_run_time, 0)
FROM msdb.dbo.sysjobs AS j
LEFT JOIN msdb.dbo.sysjobschedules AS js ON js.job_id = j.job_id
LEFT JOIN msdb.dbo.sysschedules AS sc ON sc.schedule_id = js.schedule_id
WHERE j.name IN (@full, @log)
ORDER BY j.name;
SELECT GETDATE();`

// ReadBackupJobs reads kakapo's backup jobs on a database back ("kakapo: {db} full backup" and
// "kakapo: {db} log backup"), with the server's clock.
func ReadBackupJobs(ctx context.Context, db *sql.DB, database string) ([]domain.BackupJobState, time.Time, error) {
	rows, err := db.QueryContext(ctx, Tag(backupJobsQuery),
		sql.Named("full", domain.BackupJobName(database, domain.JobFull)), sql.Named("log", domain.BackupJobName(database, domain.JobLog)))
	if err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.BackupJobState{}
	for rows.Next() {
		var st domain.BackupJobState
		var day, at int
		if err := rows.Scan(&st.Name, &st.Enabled, &st.ScheduleEnabled, &day, &at); err != nil {
			return nil, time.Time{}, fmt.Errorf("read the backup jobs back: %w", err)
		}
		if day > 0 {
			t := runDate(day).Add(time.Duration(at/10000)*time.Hour + time.Duration(at/100%100)*time.Minute + time.Duration(at%100)*time.Second)
			st.NextRun = &t
		}
		out = append(out, st)
	}
	if err := nextResultSet(rows, "server clock"); err != nil {
		return nil, time.Time{}, err
	}
	var now time.Time
	if rows.Next() {
		if err := rows.Scan(&now); err != nil {
			return nil, time.Time{}, fmt.Errorf("read the server clock: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, friendlyError(err)
	}
	return out, naive(now), nil
}

// naive keeps the wall-clock reading of a SQL Server datetime, which has no zone, in UTC.
func naive(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

func naivePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	n := naive(t.Time)
	return &n
}
