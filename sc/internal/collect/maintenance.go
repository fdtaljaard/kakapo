package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// The reads of the maintenance plan (docs/phase5b-contract.md): the plan read (every piece's job,
// its steps, schedules, next run and last outcome, in one msdb batch) and history-cleanup's read
// (the history msdb keeps, counted against the days kept). SELECTs only, each area in its own TRY
// with its error column; the statements that change something are built in domain and run by
// ExecFix.

// maintenancePlanQuery reads, for one database: its recovery model and last good integrity check,
// whether Ola Hallengren's IndexOptimize is in master, and kakapo's jobs by name ("kakapo: ...") with
// every other job that has a step doing maintenance (a backup, CHECKDB or DatabaseIntegrityCheck,
// sp_updatestats, UPDATE STATISTICS, IndexOptimize, ALTER INDEX or DBREINDEX, or a history cleanup),
// each with its steps, its schedules, the next run Agent worked out and the outcome of its last run
// (sysjobhistory step 0). Which piece a job covers is decided in domain from its step commands. No
// job is read on Express (no SQL Server Agent).
// Result sets: one row of facts, the jobs, their steps, their schedules.
const maintenancePlanQuery = safeSession + `SET NOCOUNT ON;
DECLARE @express bit = CASE WHEN CONVERT(int, SERVERPROPERTY('EngineEdition')) = 4 THEN 1 ELSE 0 END;
DECLARE @found bit = 0, @recovery nvarchar(60);
SELECT @found = 1, @recovery = d.recovery_model_desc FROM sys.databases AS d WHERE d.name = @db;

/* The last good integrity check, from which BAK03 offers the weekly job; NULL before SQL Server 2016 SP2 */
DECLARE @lastGood datetime, @checkError nvarchar(2048);
BEGIN TRY
    SET @lastGood = CONVERT(datetime, DATABASEPROPERTYEX(@db, 'LastGoodCheckDbTime'));
END TRY
BEGIN CATCH
    SET @checkError = ERROR_MESSAGE();
END CATCH;

DECLARE @ola bit = CASE WHEN OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NOT NULL
                         AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL THEN 1 ELSE 0 END;

/* kakapo's jobs and the jobs with a maintenance step: steps, schedules, next run, last outcome */
DECLARE @jobs TABLE (job_id uniqueidentifier NOT NULL PRIMARY KEY, name sysname NOT NULL, enabled bit NOT NULL,
                     next_date int NULL, next_time int NULL, last_status int NULL, last_date int NULL, last_time int NULL,
                     last_duration int NULL);
DECLARE @steps TABLE (job_id uniqueidentifier NOT NULL, step_id int NOT NULL, command nvarchar(max) NULL, database_name sysname NULL);
DECLARE @schedules TABLE (job_id uniqueidentifier NOT NULL, schedule_id int NOT NULL, enabled bit NOT NULL, freq_type int NOT NULL,
                          freq_interval int NOT NULL, freq_subday_type int NOT NULL, freq_subday_interval int NOT NULL,
                          active_start_time int NOT NULL);
DECLARE @jobsError nvarchar(2048);
IF @express = 0
BEGIN TRY
    INSERT @jobs (job_id, name, enabled, next_date, next_time, last_status, last_date, last_time, last_duration)
    SELECT j.job_id, j.name, CAST(j.enabled AS bit), n.next_run_date, n.next_run_time, h.run_status, h.run_date, h.run_time, h.run_duration
    FROM msdb.dbo.sysjobs AS j
    OUTER APPLY (SELECT TOP (1) js.next_run_date, js.next_run_time
                 FROM msdb.dbo.sysjobschedules AS js
                 WHERE js.job_id = j.job_id AND js.next_run_date > 0
                 ORDER BY js.next_run_date, js.next_run_time) AS n
    OUTER APPLY (SELECT TOP (1) jh.run_status, jh.run_date, jh.run_time, jh.run_duration
                 FROM msdb.dbo.sysjobhistory AS jh
                 WHERE jh.job_id = j.job_id AND jh.step_id = 0
                 ORDER BY jh.run_date DESC, jh.run_time DESC) AS h
    WHERE j.name LIKE N'kakapo: %'
       OR EXISTS (SELECT 1 FROM msdb.dbo.sysjobsteps AS s
                  WHERE s.job_id = j.job_id
                    AND (   s.command LIKE N'%BACKUP%' OR s.command LIKE N'%CHECKDB%' OR s.command LIKE N'%DatabaseIntegrityCheck%'
                         OR s.command LIKE N'%sp[_]updatestats%' OR s.command LIKE N'%UPDATE STATISTICS%' OR s.command LIKE N'%IndexOptimize%'
                         OR s.command LIKE N'%ALTER INDEX%' OR s.command LIKE N'%DBREINDEX%'
                         OR s.command LIKE N'%sp[_]delete[_]backuphistory%' OR s.command LIKE N'%sp[_]purge[_]jobhistory%'
                         OR s.command LIKE N'%CommandLog%'));
    INSERT @steps (job_id, step_id, command, database_name)
    SELECT s.job_id, s.step_id, s.command, s.database_name
    FROM msdb.dbo.sysjobsteps AS s
    JOIN @jobs AS j ON j.job_id = s.job_id;
    INSERT @schedules (job_id, schedule_id, enabled, freq_type, freq_interval, freq_subday_type, freq_subday_interval, active_start_time)
    SELECT js.job_id, sc.schedule_id, CAST(sc.enabled AS bit), sc.freq_type, sc.freq_interval, sc.freq_subday_type, sc.freq_subday_interval,
           sc.active_start_time
    FROM msdb.dbo.sysjobschedules AS js
    JOIN msdb.dbo.sysschedules AS sc ON sc.schedule_id = js.schedule_id
    JOIN @jobs AS j ON j.job_id = js.job_id;
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;

SELECT @found, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), @recovery, @lastGood, @checkError, @ola, @jobsError, GETDATE();
SELECT j.name, j.enabled, ISNULL(j.next_date, 0), ISNULL(j.next_time, 0), ISNULL(j.last_status, -1), ISNULL(j.last_date, 0),
       ISNULL(j.last_time, 0), ISNULL(j.last_duration, 0)
FROM @jobs AS j ORDER BY j.name;
SELECT j.name, s.command, s.database_name FROM @steps AS s JOIN @jobs AS j ON j.job_id = s.job_id ORDER BY j.name, s.step_id;
SELECT j.name, s.enabled, s.freq_type, s.freq_interval, s.freq_subday_type, s.freq_subday_interval, s.active_start_time
FROM @schedules AS s JOIN @jobs AS j ON j.job_id = s.job_id ORDER BY j.name, s.schedule_id;`

// MaintenancePlan reads the maintenance plan of one database (docs/phase5b-contract.md), SELECTs
// only. The jobs that could not be read carry their reason in JobsError; the error returned is for
// a read that could not run at all, or a database that does not exist.
func MaintenancePlan(ctx context.Context, db *sql.DB, database string) (domain.MaintenancePlanFacts, error) {
	f := domain.MaintenancePlanFacts{Database: database, Jobs: []domain.PlanJob{}}
	rows, err := db.QueryContext(ctx, Tag(maintenancePlanQuery), sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned no maintenance plan facts")
	}
	var (
		found                       bool
		recovery, checkErr, jobsErr sql.NullString
		lastGood                    sql.NullTime
		now                         time.Time
	)
	if err := rows.Scan(&found, &f.ServerName, &f.ProductVersion, &f.EngineEdition, &recovery, &lastGood, &checkErr, &f.IndexOptimize,
		&jobsErr, &now); err != nil {
		return f, fmt.Errorf("read the maintenance plan of %s: %w", database, err)
	}
	if !found {
		return f, fmt.Errorf("there is no database named %q", database)
	}
	f.RecoveryModel, f.LastGoodCheckDb, f.CheckError, f.JobsError, f.ServerNow = recovery.String, naivePtr(lastGood), checkErr.String, jobsErr.String, naive(now)

	if err := nextResultSet(rows, "jobs"); err != nil {
		return f, err
	}
	at := map[string]int{}
	for rows.Next() {
		var j domain.PlanJob
		var nextDate, nextTime, lastDate, lastTime, duration int
		if err := rows.Scan(&j.Name, &j.Enabled, &nextDate, &nextTime, &j.LastStatus, &lastDate, &lastTime, &duration); err != nil {
			return f, err
		}
		j.NextRun, j.LastRun = agentTime(nextDate, nextTime), agentTime(lastDate, lastTime)
		if j.LastRun == nil {
			j.LastStatus = domain.PlanNoRun
		}
		j.LastSeconds = duration/10000*3600 + duration/100%100*60 + duration%100
		j.Steps, j.Schedules = []domain.PlanJobStep{}, []domain.PlanSchedule{}
		at[j.Name] = len(f.Jobs)
		f.Jobs = append(f.Jobs, j)
	}

	if err := nextResultSet(rows, "job steps"); err != nil {
		return f, err
	}
	for rows.Next() {
		var name string
		var command, dbName sql.NullString
		if err := rows.Scan(&name, &command, &dbName); err != nil {
			return f, err
		}
		if i, ok := at[name]; ok {
			f.Jobs[i].Steps = append(f.Jobs[i].Steps, domain.PlanJobStep{Command: command.String, Database: dbName.String})
		}
	}

	if err := nextResultSet(rows, "job schedules"); err != nil {
		return f, err
	}
	for rows.Next() {
		var name string
		var s domain.PlanSchedule
		if err := rows.Scan(&name, &s.Enabled, &s.FreqType, &s.FreqInterval, &s.SubdayType, &s.SubdayInterval, &s.StartTime); err != nil {
			return f, err
		}
		if i, ok := at[name]; ok {
			f.Jobs[i].Schedules = append(f.Jobs[i].Schedules, s)
		}
	}
	return f, friendlyErrOrNil(rows.Err())
}

// agentTime is an msdb run_date / run_time pair (yyyymmdd, HHMMSS) as a time; nil for no date.
func agentTime(date, clock int) *time.Time {
	if date <= 0 {
		return nil
	}
	t := runDate(date).Add(time.Duration(clock/10000)*time.Hour + time.Duration(clock/100%100)*time.Minute + time.Duration(clock%100)*time.Second)
	return &t
}

// historyCleanupQuery reads what history-cleanup needs: the rows of the backup, restore and job
// history (and Ola Hallengren's CommandLog in master when it exists), each with the rows older than
// the days kept (what the first run deletes: sp_delete_backuphistory goes by backup_finish_date,
// sp_purge_jobhistory by run_date) and the oldest; whether kakapo's job exists, another enabled job
// that prunes, the operator the job would mail, and the Agent's state.
const historyCleanupQuery = safeSession + `SET NOCOUNT ON;
DECLARE @cutoff datetime = DATEADD(DAY, -@keepDays, GETDATE());
DECLARE @cutoffDate int = CONVERT(int, CONVERT(char(8), @cutoff, 112));
DECLARE @tables TABLE (ord int NOT NULL, label nvarchar(60) NOT NULL, tbl nvarchar(128) NOT NULL, total bigint NOT NULL,
                       older bigint NOT NULL, oldest datetime NULL);
DECLARE @historyError nvarchar(2048), @commandLogError nvarchar(2048), @jobExists bit = 0, @otherJob sysname, @operator sysname;
BEGIN TRY
    INSERT @tables (ord, label, tbl, total, older, oldest)
    SELECT 1, N'Backup history', N'msdb.dbo.backupset', COUNT_BIG(*), COUNT_BIG(CASE WHEN b.backup_finish_date < @cutoff THEN 1 END),
           MIN(b.backup_finish_date)
    FROM msdb.dbo.backupset AS b;
    INSERT @tables (ord, label, tbl, total, older, oldest)
    SELECT 2, N'Restore history', N'msdb.dbo.restorehistory', COUNT_BIG(*), COUNT_BIG(CASE WHEN r.restore_date < @cutoff THEN 1 END),
           MIN(r.restore_date)
    FROM msdb.dbo.restorehistory AS r;
    INSERT @tables (ord, label, tbl, total, older, oldest)
    SELECT 3, N'Job history', N'msdb.dbo.sysjobhistory', COUNT_BIG(*), COUNT_BIG(CASE WHEN h.run_date < @cutoffDate THEN 1 END),
           CASE WHEN MIN(h.run_date) > 0 THEN CONVERT(datetime, CONVERT(char(8), MIN(h.run_date)), 112) END
    FROM msdb.dbo.sysjobhistory AS h;
    SELECT @jobExists = 1 FROM msdb.dbo.sysjobs WHERE name = @job;
    SELECT TOP (1) @otherJob = j.name
    FROM msdb.dbo.sysjobs AS j
    JOIN msdb.dbo.sysjobsteps AS s ON s.job_id = j.job_id
    WHERE j.enabled = 1 AND j.name <> @job
      AND (s.command LIKE N'%sp[_]delete[_]backuphistory%' OR s.command LIKE N'%sp[_]purge[_]jobhistory%' OR s.command LIKE N'%CommandLog%')
    ORDER BY j.name;
    SELECT TOP (1) @operator = o.name FROM msdb.dbo.sysoperators AS o
    WHERE o.enabled = 1 AND o.email_address IS NOT NULL AND o.email_address <> N'' ORDER BY o.name;
END TRY
BEGIN CATCH
    SET @historyError = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    IF OBJECT_ID(N'master.dbo.CommandLog', N'U') IS NOT NULL
        INSERT @tables (ord, label, tbl, total, older, oldest)
        SELECT 4, N'Command log (Ola Hallengren)', N'master.dbo.CommandLog', COUNT_BIG(*), COUNT_BIG(CASE WHEN c.StartTime < @cutoff THEN 1 END),
               MIN(c.StartTime)
        FROM master.dbo.CommandLog AS c;
END TRY
BEGIN CATCH
    SET @commandLogError = ERROR_MESSAGE();
END CATCH;
` + suAgentStatusBlock + `SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), @historyError, @commandLogError, @jobExists, @otherJob, @operator,
       @agentStatus, @agentError, GETDATE();
SELECT label, tbl, total, older, oldest FROM @tables ORDER BY ord;`

// ReadHistoryCleanupFacts reads what history-cleanup needs, with the rows counted against keepDays.
// msdb that cannot be read is an error: the fix is built from it. CommandLog that cannot be read is
// left out.
func ReadHistoryCleanupFacts(ctx context.Context, db *sql.DB, keepDays int) (domain.HistoryCleanupFacts, error) {
	f := domain.HistoryCleanupFacts{KeepDays: keepDays, Tables: []domain.HistoryTable{}}
	rows, err := db.QueryContext(ctx, Tag(historyCleanupQuery), sql.Named("keepDays", keepDays), sql.Named("job", domain.HistoryCleanupJobName))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, errors.New("SQL Server returned nothing for the msdb history")
	}
	var historyErr, commandLogErr, otherJob, operator, agentStatus, agentErr sql.NullString
	var now time.Time
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.EngineEdition, &historyErr, &commandLogErr, &f.JobExists, &otherJob, &operator,
		&agentStatus, &agentErr, &now); err != nil {
		return f, err
	}
	if historyErr.String != "" {
		return f, fmt.Errorf("could not read the history in msdb: %s", historyErr.String)
	}
	f.OtherJob, f.Operator, f.AgentStatus, f.AgentError, f.ServerNow = otherJob.String, operator.String, agentStatus.String, agentErr.String, naive(now)
	if err := nextResultSet(rows, "history tables"); err != nil {
		return f, err
	}
	for rows.Next() {
		var t domain.HistoryTable
		var oldest sql.NullTime
		if err := rows.Scan(&t.Label, &t.Table, &t.Rows, &t.Older, &oldest); err != nil {
			return f, err
		}
		t.Oldest = naivePtr(oldest)
		f.Tables = append(f.Tables, t)
	}
	return f, friendlyErrOrNil(rows.Err())
}
