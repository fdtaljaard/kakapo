package collect

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// The reads of the Upkeep card's actions (docs/phase4d-contract.md, part 1): what each fix's
// preview is built from and what its read-back reads again. SELECTs only; msdb is read through
// sp_executesql inside a TRY so that msdb being closed to the login gives an error column, not a
// failed read. The statements that change something are built in domain and run by ExecFix.

// suAgentStatusBlock reads the SQL Server Agent service state into @agentStatus / @agentError.
const suAgentStatusBlock = `DECLARE @agentStatus nvarchar(60), @agentError nvarchar(2048);
IF HAS_PERMS_BY_NAME(NULL, NULL, 'VIEW SERVER STATE') = 0
    SET @agentError = N'the login does not have VIEW SERVER STATE';
ELSE
BEGIN TRY
    SELECT TOP (1) @agentStatus = status_desc FROM sys.dm_server_services WHERE servicename LIKE N'SQL Server Agent%';
END TRY
BEGIN CATCH
    SET @agentError = ERROR_MESSAGE();
END CATCH;
`

// alertFactsQuery reads the operators, the alerts and their notifications from msdb, whether
// Database Mail has a profile, the profile the Agent's alert system mails with (its registry value,
// as SSMS reads it) and the Agent's state, for operator-alerts and its read-back.
const alertFactsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @ops TABLE (name sysname NOT NULL, email nvarchar(100) NULL, enabled bit NOT NULL);
DECLARE @alerts TABLE (name sysname NOT NULL, message_id int NOT NULL, severity int NOT NULL, enabled bit NOT NULL,
                       database_name sysname NULL, keyword nvarchar(100) NULL);
DECLARE @notes TABLE (alert sysname NOT NULL, operator sysname NOT NULL, method int NOT NULL);
DECLARE @msdbError nvarchar(2048), @profiles int, @mailError nvarchar(2048), @agentProfile nvarchar(256), @agentMailError nvarchar(2048);
BEGIN TRY
    INSERT @ops (name, email, enabled)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT name, email_address, CAST(enabled AS bit) FROM msdb.dbo.sysoperators;';
    INSERT @alerts (name, message_id, severity, enabled, database_name, keyword)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT name, message_id, severity, CAST(enabled AS bit), database_name, event_description_keyword
        FROM msdb.dbo.sysalerts;';
    INSERT @notes (alert, operator, method)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT a.name, o.name, n.notification_method
        FROM msdb.dbo.sysnotifications AS n
        JOIN msdb.dbo.sysalerts AS a ON a.id = n.alert_id
        JOIN msdb.dbo.sysoperators AS o ON o.id = n.operator_id;';
END TRY
BEGIN CATCH
    SET @msdbError = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @p = COUNT(*) FROM msdb.dbo.sysmail_profile;', N'@p int OUTPUT', @p = @profiles OUTPUT;
END TRY
BEGIN CATCH
    SET @mailError = ERROR_MESSAGE();
END CATCH;
BEGIN TRY
    EXEC master.dbo.xp_instance_regread N'HKEY_LOCAL_MACHINE', N'SOFTWARE\Microsoft\MSSQLServer\SQLServerAgent',
         N'DatabaseMailProfile', @agentProfile OUTPUT, N'no_output';
END TRY
BEGIN CATCH
    SET @agentMailError = ERROR_MESSAGE();
END CATCH;
` + suAgentStatusBlock + `SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), @msdbError, ISNULL(@profiles, 0), @mailError, @agentProfile, @agentMailError,
       @agentStatus, @agentError;
SELECT name, email, enabled FROM @ops ORDER BY name;
SELECT name, message_id, severity, enabled, database_name, keyword FROM @alerts ORDER BY name;
SELECT alert, operator, method FROM @notes ORDER BY alert, operator;`

// ReadAlertFacts reads what operator-alerts needs. msdb that cannot be read is an error: the fix
// is built from it.
func ReadAlertFacts(ctx context.Context, db *sql.DB) (domain.AlertFacts, error) {
	f := domain.AlertFacts{Operators: []domain.AgentOperator{}, Alerts: []domain.AgentAlert{}, Notifications: []domain.AgentNotification{}}
	rows, err := db.QueryContext(ctx, Tag(alertFactsQuery))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned nothing for the alerts")
	}
	var msdbErr, mailErr, agentProfile, agentMailErr, agentStatus, agentErr sql.NullString
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.EngineEdition, &msdbErr, &f.MailProfiles, &mailErr, &agentProfile,
		&agentMailErr, &agentStatus, &agentErr); err != nil {
		return f, err
	}
	if msdbErr.String != "" {
		return f, fmt.Errorf("could not read the operators and alerts in msdb: %s", msdbErr.String)
	}
	f.MailError, f.AgentMailProfile, f.AgentMailError = mailErr.String, agentProfile.String, agentMailErr.String
	f.AgentStatus, f.AgentError = agentStatus.String, agentErr.String
	if err := nextResultSet(rows, "operators"); err != nil {
		return f, err
	}
	for rows.Next() {
		var o domain.AgentOperator
		var email sql.NullString
		if err := rows.Scan(&o.Name, &email, &o.Enabled); err != nil {
			return f, err
		}
		o.Email = email.String
		f.Operators = append(f.Operators, o)
	}
	if err := nextResultSet(rows, "alerts"); err != nil {
		return f, err
	}
	for rows.Next() {
		var a domain.AgentAlert
		var dbName, keyword sql.NullString
		if err := rows.Scan(&a.Name, &a.MessageID, &a.Severity, &a.Enabled, &dbName, &keyword); err != nil {
			return f, err
		}
		a.Database, a.Keyword = dbName.String, keyword.String
		f.Alerts = append(f.Alerts, a)
	}
	if err := nextResultSet(rows, "notifications"); err != nil {
		return f, err
	}
	for rows.Next() {
		var n domain.AgentNotification
		if err := rows.Scan(&n.Alert, &n.Operator, &n.Method); err != nil {
			return f, err
		}
		f.Notifications = append(f.Notifications, n)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// maintenanceJobQuery reads whether Ola Hallengren's IndexOptimize (with CommandExecute, which it
// calls) is in master, which of kakapo's two maintenance jobs on the database exist, the operator
// the jobs notify (the first enabled one with an email address, as the backup jobs pick it), the
// Agent's state and the server's clock, for maintenance-job.
const maintenanceJobQuery = safeSession + `SET NOCOUNT ON;
DECLARE @ola bit = CASE WHEN OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NOT NULL
                         AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL THEN 1 ELSE 0 END;
DECLARE @jobs TABLE (name sysname NOT NULL);
DECLARE @jobsError nvarchar(2048), @operator sysname;
BEGIN TRY
    INSERT @jobs (name)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT name FROM msdb.dbo.sysjobs WHERE name IN (@a, @b);',
        N'@a sysname, @b sysname', @a = @statsJob, @b = @indexJob;
    EXEC sys.sp_executesql N'/* kakapo */ SELECT TOP (1) @o = o.name FROM msdb.dbo.sysoperators AS o
        WHERE o.enabled = 1 AND o.email_address IS NOT NULL AND o.email_address <> N'''' ORDER BY o.name;',
        N'@o sysname OUTPUT', @o = @operator OUTPUT;
END TRY
BEGIN CATCH
    SET @jobsError = ERROR_MESSAGE();
END CATCH;
` + suAgentStatusBlock + `SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), @ola, @jobsError, @operator, @agentStatus, @agentError, GETDATE();
SELECT name FROM @jobs ORDER BY name;`

// ReadMaintenanceJobFacts reads what maintenance-job needs for one database. msdb that cannot be
// read is an error: whether the jobs exist decides the fix.
func ReadMaintenanceJobFacts(ctx context.Context, db *sql.DB, database string) (domain.MaintenanceJobFacts, error) {
	f := domain.MaintenanceJobFacts{Database: database, ExistingJobs: []string{}}
	rows, err := db.QueryContext(ctx, Tag(maintenanceJobQuery),
		sql.Named("statsJob", domain.StatisticsJobName(database)), sql.Named("indexJob", domain.IndexJobName(database)))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned nothing for the maintenance jobs")
	}
	var jobsErr, operator, agentStatus, agentErr sql.NullString
	var now time.Time
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.EngineEdition, &f.Ola, &jobsErr, &operator, &agentStatus, &agentErr, &now); err != nil {
		return f, err
	}
	if jobsErr.String != "" {
		return f, fmt.Errorf("could not read the Agent jobs in msdb: %s", jobsErr.String)
	}
	f.Operator, f.AgentStatus, f.AgentError, f.ServerNow = operator.String, agentStatus.String, agentErr.String, naive(now)
	if err := nextResultSet(rows, "jobs"); err != nil {
		return f, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return f, err
		}
		f.ExistingJobs = append(f.ExistingJobs, n)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// jobsByNameQuery reads up to two jobs back after a fix created them: whether each exists and is
// enabled, its schedule enabled, and the next run Agent worked out (0 before it has).
const jobsByNameQuery = safeSession + `SET NOCOUNT ON;
SELECT j.name, CAST(j.enabled AS bit), CAST(ISNULL(sc.enabled, 0) AS bit), ISNULL(js.next_run_date, 0), ISNULL(js.next_run_time, 0)
FROM msdb.dbo.sysjobs AS j
LEFT JOIN msdb.dbo.sysjobschedules AS js ON js.job_id = j.job_id
LEFT JOIN msdb.dbo.sysschedules AS sc ON sc.schedule_id = js.schedule_id
WHERE j.name IN (@a, @b)
ORDER BY j.name;`

// ReadJobsByName reads jobs back by name (at most two: the maintenance jobs).
func ReadJobsByName(ctx context.Context, db *sql.DB, names []string) ([]domain.BackupJobState, error) {
	if len(names) == 0 || len(names) > 2 {
		return nil, fmt.Errorf("read back 1 or 2 jobs, not %d", len(names))
	}
	a, b := names[0], names[0]
	if len(names) == 2 {
		b = names[1]
	}
	rows, err := db.QueryContext(ctx, Tag(jobsByNameQuery), sql.Named("a", a), sql.Named("b", b))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.BackupJobState{}
	for rows.Next() {
		var st domain.BackupJobState
		var day, at int
		if err := rows.Scan(&st.Name, &st.Enabled, &st.ScheduleEnabled, &day, &at); err != nil {
			return nil, fmt.Errorf("read the jobs back: %w", err)
		}
		if day > 0 {
			t := runDate(day).Add(time.Duration(at/10000)*time.Hour + time.Duration(at/100%100)*time.Minute + time.Duration(at%100)*time.Second)
			st.NextRun = &t
		}
		out = append(out, st)
	}
	return out, friendlyErrOrNil(rows.Err())
}

// jobStepsQuery reads one Agent job by name and every one of its steps, enough to create a step
// again (the remove-shrink-step rollback). msdb in its own TRY.
const jobStepsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @found bit = 0, @enabled bit = 0, @start int = 0, @error nvarchar(2048);
DECLARE @steps TABLE (step_id int NOT NULL, step_name sysname NOT NULL, subsystem nvarchar(40) NOT NULL, command nvarchar(max) NULL,
                      database_name sysname NULL, database_user_name sysname NULL, output_file_name nvarchar(200) NULL, proxy sysname NULL,
                      on_success_action int NOT NULL, on_success_step_id int NOT NULL, on_fail_action int NOT NULL, on_fail_step_id int NOT NULL,
                      retry_attempts int NOT NULL, retry_interval int NOT NULL, flags int NOT NULL);
BEGIN TRY
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @f = 1, @e = CAST(enabled AS bit), @s = start_step_id FROM msdb.dbo.sysjobs WHERE name = @job;',
        N'@job sysname, @f bit OUTPUT, @e bit OUTPUT, @s int OUTPUT', @job = @job, @f = @found OUTPUT, @e = @enabled OUTPUT, @s = @start OUTPUT;
    INSERT @steps (step_id, step_name, subsystem, command, database_name, database_user_name, output_file_name, proxy,
                   on_success_action, on_success_step_id, on_fail_action, on_fail_step_id, retry_attempts, retry_interval, flags)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT s.step_id, s.step_name, s.subsystem, s.command, s.database_name, s.database_user_name,
            s.output_file_name, p.name, s.on_success_action, s.on_success_step_id, s.on_fail_action, s.on_fail_step_id,
            s.retry_attempts, s.retry_interval, s.flags
        FROM msdb.dbo.sysjobs AS j
        JOIN msdb.dbo.sysjobsteps AS s ON s.job_id = j.job_id
        LEFT JOIN msdb.dbo.sysproxies AS p ON p.proxy_id = s.proxy_id
        WHERE j.name = @job;', N'@job sysname', @job = @job;
END TRY
BEGIN CATCH
    SET @error = ERROR_MESSAGE();
END CATCH;
SELECT @found, @enabled, ISNULL(@start, 0), @error, CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64));
SELECT step_id, step_name, subsystem, command, database_name, database_user_name, output_file_name, proxy,
       on_success_action, on_success_step_id, on_fail_action, on_fail_step_id, retry_attempts, retry_interval, flags
FROM @steps ORDER BY step_id;`

// ReadJobSteps reads one Agent job and its steps for remove-shrink-step and its read-back. A job
// that does not exist comes back with Found false.
func ReadJobSteps(ctx context.Context, db *sql.DB, job string) (domain.ShrinkStepFacts, error) {
	f := domain.ShrinkStepFacts{Job: job, Steps: []domain.AgentJobStep{}}
	rows, err := db.QueryContext(ctx, Tag(jobStepsQuery), sql.Named("job", job))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return f, fmt.Errorf("SQL Server returned nothing for the job %q", job)
	}
	var errText sql.NullString
	if err := rows.Scan(&f.Found, &f.Enabled, &f.StartStep, &errText, &f.ServerName, &f.ProductVersion); err != nil {
		return f, err
	}
	if errText.String != "" {
		return f, fmt.Errorf("could not read the job %s in msdb: %s", job, errText.String)
	}
	if err := nextResultSet(rows, "job steps"); err != nil {
		return f, err
	}
	for rows.Next() {
		var s domain.AgentJobStep
		var command, dbName, dbUser, output, proxy sql.NullString
		if err := rows.Scan(&s.ID, &s.Name, &s.Subsystem, &command, &dbName, &dbUser, &output, &proxy,
			&s.OnSuccessAction, &s.OnSuccessStep, &s.OnFailAction, &s.OnFailStep, &s.RetryAttempts, &s.RetryInterval, &s.Flags); err != nil {
			return f, err
		}
		s.Command, s.Database, s.DatabaseUser, s.OutputFile, s.Proxy = command.String, dbName.String, dbUser.String, output.String, proxy.String
		f.Steps = append(f.Steps, s)
	}
	return f, friendlyErrOrNil(rows.Err())
}

// defaultTraceQuery reads what enable-default-trace needs (docs/phase5f-contract.md): the value in
// use of 'default trace enabled' and, in its own TRY, whether the default trace runs (sys.traces
// needs ALTER TRACE). SELECTs only.
const defaultTraceQuery = safeSession + `SET NOCOUNT ON;
DECLARE @running bit = 0, @traceError nvarchar(2048);
BEGIN TRY
    IF EXISTS (SELECT 1 FROM sys.traces WHERE is_default = 1 AND status = 1) SET @running = 1;
END TRY
BEGIN CATCH
    SET @traceError = ERROR_MESSAGE();
END CATCH;
SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       ISNULL((SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'default trace enabled'), 0),
       @running, @traceError;`

// ReadDefaultTrace reads the default trace option and whether the trace runs, for
// enable-default-trace's preview and read-back; Pending is left for the caller.
func ReadDefaultTrace(ctx context.Context, db *sql.DB) (domain.DefaultTraceFacts, error) {
	var f domain.DefaultTraceFacts
	var traceErr sql.NullString
	if err := db.QueryRowContext(ctx, Tag(defaultTraceQuery)).Scan(&f.ServerName, &f.ProductVersion, &f.ValueInUse, &f.Running, &traceErr); err != nil {
		return f, friendlyError(err)
	}
	f.TraceError = traceErr.String
	return f, nil
}
