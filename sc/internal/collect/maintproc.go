package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The reads of kakapo's maintenance procedure (docs/phase5p-contract.md): whether master.dbo.kakapo_Maintenance
// is there and kakapo's (its header carries the marker), its two tables, Ola Hallengren's procedures, every
// settings row, and the log by database and task. SELECTs only; the tables are read through sp_executesql
// in their own TRY, so a server without them (or with tables of the same name and another shape) still
// answers. The statements that install and remove it are built in domain and run by ExecFixBatches.

// maintProcQuery reads, for one database: its recovery model; the server's name, version, edition, clock
// and default backup folder; the procedure, its tables and Ola's procedures; then the settings rows, the
// latest log row of every database and task with its last successful and last failed start, and the last
// failure. Result sets: one row, the settings, the tasks, the last failure.
const maintProcQuery = safeSession + `SET NOCOUNT ON;
DECLARE @recovery nvarchar(60);
SELECT @recovery = d.recovery_model_desc FROM sys.databases AS d WHERE d.name = @db;

DECLARE @procExists bit = 0, @header nvarchar(200), @settingsExists bit = 0, @logExists bit = 0, @procErr nvarchar(2048), @tablesErr nvarchar(2048);
BEGIN TRY
    IF OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P') IS NOT NULL
    BEGIN
        SET @procExists = 1;
        SELECT @header = SUBSTRING(m.definition, NULLIF(CHARINDEX(N'kakapo maintenance procedure, version ', m.definition), 0), 60)
        FROM master.sys.sql_modules AS m WHERE m.object_id = OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P');
    END;
    IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceSettings', N'U') IS NOT NULL SET @settingsExists = 1;
    IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceLog', N'U') IS NOT NULL SET @logExists = 1;
END TRY
BEGIN CATCH
    SET @procErr = ERROR_MESSAGE();
END CATCH;

/* The signature (phase 5p): the certificate, whether it signs the procedure and still has its private key,
   its login and whether that is sysadmin; SYSTEM's login, its master user (kakapo's or not) and its EXECUTE */
DECLARE @certExists bit = 0, @certKey bit = 0, @signed bit = 0, @certLogin bit = 0, @certSysadmin bit = 0,
        @systemLogin bit = 0, @systemUser bit = 0, @systemUserOurs bit = 0, @systemExecute bit = 0, @signErr nvarchar(2048);
BEGIN TRY
    SELECT @certExists = 1, @certKey = CASE WHEN c.pvt_key_encryption_type = 'NA' THEN 0 ELSE 1 END,
           @signed = CASE WHEN EXISTS (SELECT 1 FROM master.sys.crypt_properties AS cp
                                       WHERE cp.class = 1 AND cp.major_id = OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P')
                                         AND cp.thumbprint = c.thumbprint) THEN 1 ELSE 0 END
    FROM master.sys.certificates AS c WHERE c.name = N'kakapo_MaintenanceCert';
    /* From the role's members: IS_SRVROLEMEMBER does not answer for a login made from a certificate */
    SELECT @certLogin = 1,
           @certSysadmin = CASE WHEN EXISTS (SELECT 1 FROM sys.server_role_members AS rm
                                             JOIN sys.server_principals AS r ON r.principal_id = rm.role_principal_id AND r.name = N'sysadmin'
                                             WHERE rm.member_principal_id = p.principal_id) THEN 1 ELSE 0 END
    FROM sys.server_principals AS p WHERE p.name = N'kakapo_MaintenanceLogin' AND p.type = 'C';
    SET @systemLogin = CASE WHEN SUSER_SID(N'NT AUTHORITY\SYSTEM') IS NOT NULL THEN 1 ELSE 0 END;
    SELECT @systemUser = 1,
           @systemUserOurs = CASE WHEN EXISTS (SELECT 1 FROM master.sys.extended_properties AS ep
                                               WHERE ep.class = 4 AND ep.major_id = dp.principal_id AND ep.name = N'kakapo') THEN 1 ELSE 0 END,
           @systemExecute = CASE WHEN EXISTS (SELECT 1 FROM master.sys.database_permissions AS pe
                                              WHERE pe.grantee_principal_id = dp.principal_id AND pe.class = 1
                                                AND pe.major_id = OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P')
                                                AND pe.type = 'EX' AND pe.state IN ('G', 'W')) THEN 1 ELSE 0 END
    FROM master.sys.database_principals AS dp WHERE dp.sid = SUSER_SID(N'NT AUTHORITY\SYSTEM');
END TRY
BEGIN CATCH
    SET @signErr = ERROR_MESSAGE();
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

DECLARE @settings TABLE (db sysname NOT NULL, folder nvarchar(400) NOT NULL, keepDays int NOT NULL, fullTime char(5) NOT NULL, logMinutes int NOT NULL,
                         checkDay tinyint NOT NULL, checkTime char(5) NOT NULL, statsTime char(5) NOT NULL, indexWork bit NOT NULL, indexTime char(5) NOT NULL,
                         cleanupDay tinyint NOT NULL, cleanupTime char(5) NOT NULL, historyDays int NOT NULL, enabled bit NOT NULL,
                         createdAt datetime NULL, lastCall datetime NULL);
DECLARE @tasks TABLE (db sysname NOT NULL, task varchar(20) NOT NULL, startTime datetime NOT NULL, endTime datetime NULL, outcome varchar(10) NOT NULL,
                      errorNumber int NULL, message nvarchar(4000) NULL, backupFile nvarchar(600) NULL, lastOK datetime NULL, lastFailed datetime NULL);
DECLARE @failure TABLE (db sysname NOT NULL, task varchar(20) NOT NULL, startTime datetime NOT NULL, endTime datetime NULL, errorNumber int NULL,
                        message nvarchar(4000) NULL);
IF @settingsExists = 1 AND @logExists = 1
BEGIN TRY
    INSERT @settings (db, folder, keepDays, fullTime, logMinutes, checkDay, checkTime, statsTime, indexWork, indexTime, cleanupDay, cleanupTime,
                      historyDays, enabled, createdAt, lastCall)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT s.DatabaseName, s.BackupFolder, s.KeepBackupDays, CONVERT(char(5), s.FullTime, 108), s.LogMinutes,
           s.CheckDay, CONVERT(char(5), s.CheckTime, 108), CONVERT(char(5), s.StatsTime, 108), s.IndexWork, CONVERT(char(5), s.IndexTime, 108),
           s.CleanupDay, CONVERT(char(5), s.CleanupTime, 108), s.KeepHistoryDays, s.Enabled, s.CreatedAt, s.LastCall
    FROM master.dbo.kakapo_MaintenanceSettings AS s;';
    INSERT @tasks (db, task, startTime, endTime, outcome, errorNumber, message, backupFile, lastOK, lastFailed)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT l.DatabaseName, l.Task, l.StartTime, l.EndTime, l.Outcome, l.ErrorNumber, l.Message, l.BackupFile,
           (SELECT MAX(o.StartTime) FROM master.dbo.kakapo_MaintenanceLog AS o
            WHERE o.DatabaseName = l.DatabaseName AND o.Task = l.Task AND o.Outcome = ''OK''),
           (SELECT MAX(o.StartTime) FROM master.dbo.kakapo_MaintenanceLog AS o
            WHERE o.DatabaseName = l.DatabaseName AND o.Task = l.Task AND o.Outcome = ''Failed'')
    FROM master.dbo.kakapo_MaintenanceLog AS l
    WHERE l.ID = (SELECT MAX(x.ID) FROM master.dbo.kakapo_MaintenanceLog AS x WHERE x.DatabaseName = l.DatabaseName AND x.Task = l.Task);';
    INSERT @failure (db, task, startTime, endTime, errorNumber, message)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT TOP (1) l.DatabaseName, l.Task, l.StartTime, l.EndTime, l.ErrorNumber, l.Message
    FROM master.dbo.kakapo_MaintenanceLog AS l WHERE l.Outcome = ''Failed'' ORDER BY l.ID DESC;';
END TRY
BEGIN CATCH
    SET @tablesErr = ERROR_MESSAGE();
END CATCH;

SELECT CAST(ISNULL(@@SERVERNAME, N'') AS nvarchar(256)), CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
       CONVERT(int, SERVERPROPERTY('EngineEdition')), CAST(SERVERPROPERTY('Edition') AS nvarchar(128)), GETDATE(), @recovery, @backupDir,
       @procExists, @header, @settingsExists, @logExists, @procErr, @tablesErr,
       @certExists, @certKey, @signed, @certLogin, @certSysadmin, @systemLogin, @systemUser, @systemUserOurs, @systemExecute, @signErr,
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.DatabaseBackup', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit),
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.DatabaseIntegrityCheck', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit),
       CAST(CASE WHEN OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NOT NULL THEN 1 ELSE 0 END AS bit);
SELECT db, folder, keepDays, fullTime, logMinutes, checkDay, checkTime, statsTime, indexWork, indexTime, cleanupDay, cleanupTime, historyDays, enabled,
       createdAt, lastCall
FROM @settings ORDER BY db;
SELECT db, task, startTime, endTime, outcome, errorNumber, message, backupFile, lastOK, lastFailed FROM @tasks ORDER BY db, task;
SELECT db, task, startTime, endTime, errorNumber, message FROM @failure;`

// maintVersion reads the version from the procedure's header ("kakapo maintenance procedure, version
// 3."): 0 and false when the marker is not there.
func maintVersion(header string) (int, bool) {
	const marker = "kakapo maintenance procedure, version "
	i := strings.Index(header, marker)
	if i < 0 {
		return 0, false
	}
	rest := header[i+len(marker):]
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	v, err := strconv.Atoi(rest[:n])
	if err != nil {
		return 0, true
	}
	return v, true
}

// ReadMaintProc reads kakapo's maintenance procedure as it stands on the server, for one database (the
// fix's or the plan's; "" for none). An error means nothing could be read; a part that failed is in Error.
func ReadMaintProc(ctx context.Context, db *sql.DB, database string) (domain.MaintProcFacts, error) {
	f := domain.MaintProcFacts{Database: database, Settings: []domain.MaintSettings{}, Tasks: []domain.MaintTaskLog{}}
	rows, err := db.QueryContext(ctx, Tag(maintProcQuery), sql.Named("db", database))
	if err != nil {
		return f, friendlyError(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return f, friendlyError(err)
		}
		return f, errors.New("SQL Server returned nothing for kakapo's maintenance procedure")
	}
	var (
		edition                                       sql.NullString
		now                                           time.Time
		recovery, backupDir, header, procErr, tablErr sql.NullString
		signErr                                       sql.NullString
	)
	s := &f.Signing
	if err := rows.Scan(&f.ServerName, &f.ProductVersion, &f.EngineEdition, &edition, &now, &recovery, &backupDir,
		&f.ProcExists, &header, &f.SettingsExists, &f.LogExists, &procErr, &tablErr,
		&s.CertExists, &s.CertPrivateKey, &s.Signed, &s.CertLogin, &s.CertSysadmin, &s.SchedulerLogin, &s.SchedulerUser, &s.SchedulerUserOurs,
		&s.SchedulerExecute, &signErr,
		&f.OlaBackup, &f.OlaCheck, &f.OlaIndex); err != nil {
		return f, fmt.Errorf("read kakapo's maintenance procedure: %w", err)
	}
	f.Edition, f.ServerNow, f.RecoveryModel, f.DefaultBackupDir = edition.String, naive(now), recovery.String, backupDir.String
	f.ProcVersion, f.ProcOurs = maintVersion(header.String)
	errs := []string{}
	if procErr.String != "" {
		errs = append(errs, "the procedure ("+procErr.String+")")
	}
	if tablErr.String != "" {
		errs = append(errs, "its tables ("+tablErr.String+")")
	}
	if signErr.String != "" {
		errs = append(errs, "its signature ("+signErr.String+")")
	}
	if len(errs) > 0 {
		f.Error = "could not read " + strings.Join(errs, "; ")
	}

	if err := nextResultSet(rows, "maintenance settings"); err != nil {
		return f, err
	}
	for rows.Next() {
		var s domain.MaintSettings
		var checkDay, cleanupDay int
		var created, lastCall sql.NullTime
		if err := rows.Scan(&s.Database, &s.Folder, &s.KeepBackupDays, &s.FullTime, &s.LogMinutes, &checkDay, &s.CheckTime, &s.StatsTime, &s.IndexWork,
			&s.IndexTime, &cleanupDay, &s.CleanupTime, &s.KeepHistoryDays, &s.Enabled, &created, &lastCall); err != nil {
			return f, err
		}
		s.CheckDay, s.CleanupDay = maintDayName(checkDay), maintDayName(cleanupDay)
		s.CreatedAt, s.LastCall = naivePtr(created), naivePtr(lastCall)
		f.Settings = append(f.Settings, s)
	}

	if err := nextResultSet(rows, "maintenance log"); err != nil {
		return f, err
	}
	for rows.Next() {
		var t domain.MaintTaskLog
		var end, lastOK, lastFailed sql.NullTime
		var errNum sql.NullInt64
		var msg, file sql.NullString
		var start time.Time
		if err := rows.Scan(&t.Database, &t.Task, &start, &end, &t.Latest.Outcome, &errNum, &msg, &file, &lastOK, &lastFailed); err != nil {
			return f, err
		}
		t.Latest.Database, t.Latest.Task, t.Latest.Start, t.Latest.End = t.Database, t.Task, naive(start), naivePtr(end)
		t.Latest.ErrorNumber, t.Latest.Message, t.Latest.File = int(errNum.Int64), msg.String, file.String
		t.LastOK, t.LastFailed = naivePtr(lastOK), naivePtr(lastFailed)
		f.Tasks = append(f.Tasks, t)
	}

	if err := nextResultSet(rows, "maintenance failure"); err != nil {
		return f, err
	}
	for rows.Next() {
		var r domain.MaintRun
		var end sql.NullTime
		var errNum sql.NullInt64
		var msg sql.NullString
		if err := rows.Scan(&r.Database, &r.Task, &r.Start, &end, &errNum, &msg); err != nil {
			return f, err
		}
		r.Start, r.End, r.Outcome, r.ErrorNumber, r.Message = naive(r.Start), naivePtr(end), domain.MaintFailed, int(errNum.Int64), msg.String
		f.LastFailure = &r
	}
	return f, friendlyErrOrNil(rows.Err())
}

// maintDayName is a settings day (1 Sunday .. 7 Saturday) as its name; Sunday when out of range.
func maintDayName(n int) string {
	if n < 1 || n > 7 {
		return domain.CheckdbDays[0]
	}
	return domain.CheckdbDays[n-1]
}

// maintRunsQuery reads one database's log rows that started at or after a time, oldest first: what a
// call did (Run now's read-back). SELECT only, through sp_executesql so a server without the table answers.
const maintRunsQuery = safeSession + `SET NOCOUNT ON;
DECLARE @runs TABLE (task varchar(20) NOT NULL, startTime datetime NOT NULL, endTime datetime NULL, outcome varchar(10) NOT NULL,
                     errorNumber int NULL, message nvarchar(4000) NULL, backupFile nvarchar(600) NULL, id int NOT NULL);
IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceLog', N'U') IS NOT NULL
    INSERT @runs (task, startTime, endTime, outcome, errorNumber, message, backupFile, id)
    EXEC sys.sp_executesql N'/* kakapo */ SELECT l.Task, l.StartTime, l.EndTime, l.Outcome, l.ErrorNumber, l.Message, l.BackupFile, l.ID
    FROM master.dbo.kakapo_MaintenanceLog AS l WHERE l.DatabaseName = @db AND l.ID > @after;',
        N'@db sysname, @after int', @db = @db, @after = @after;
SELECT task, startTime, endTime, outcome, errorNumber, message, backupFile FROM @runs ORDER BY id;`

// maintMarkQuery reads the log's highest ID (0 when it is empty or not there). SELECT only.
const maintMarkQuery = safeSession + `SET NOCOUNT ON;
DECLARE @mark int = 0;
IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceLog', N'U') IS NOT NULL
    EXEC sys.sp_executesql N'/* kakapo */ SELECT @m = ISNULL(MAX(l.ID), 0) FROM master.dbo.kakapo_MaintenanceLog AS l;', N'@m int OUTPUT', @m = @mark OUTPUT;
SELECT @mark;`

// MaintLogMark is the log's highest ID now: read before a call, so ReadMaintRuns returns what it wrote.
func MaintLogMark(ctx context.Context, db *sql.DB) (int64, error) {
	var mark int64
	if err := db.QueryRowContext(ctx, Tag(maintMarkQuery)).Scan(&mark); err != nil {
		return 0, friendlyError(err)
	}
	return mark, nil
}

// ReadMaintRuns reads one database's log rows written after a mark (MaintLogMark), oldest first.
func ReadMaintRuns(ctx context.Context, db *sql.DB, database string, after int64) ([]domain.MaintRun, error) {
	rows, err := db.QueryContext(ctx, Tag(maintRunsQuery), sql.Named("db", database), sql.Named("after", after))
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.MaintRun{}
	for rows.Next() {
		r := domain.MaintRun{Database: database}
		var end sql.NullTime
		var errNum sql.NullInt64
		var msg, file sql.NullString
		if err := rows.Scan(&r.Task, &r.Start, &end, &r.Outcome, &errNum, &msg, &file); err != nil {
			return nil, err
		}
		r.Start, r.End, r.ErrorNumber, r.Message, r.File = naive(r.Start), naivePtr(end), int(errNum.Int64), msg.String, file.String
		out = append(out, r)
	}
	return out, friendlyErrOrNil(rows.Err())
}

// ExecFixBatches runs a script of batches separated by GO lines (domain.SplitBatches) one after another on
// one connection, as sqlcmd would, and stops at the first that fails, naming it. The script may change the
// connection's database (USE master); the connection is put back in the database it started in.
func ExecFixBatches(ctx context.Context, db *sql.DB, script string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return friendlyError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, safeSession); err != nil {
		return friendlyError(err)
	}
	var start string
	if err := conn.QueryRowContext(ctx, Tag("SELECT DB_NAME();")).Scan(&start); err != nil {
		return friendlyError(err)
	}
	batches := domain.SplitBatches(script)
	var runErr error
	for i, b := range batches {
		if _, err := conn.ExecContext(ctx, Tag(b)); err != nil {
			runErr = fmt.Errorf("batch %d of %d: %w", i+1, len(batches), friendlyError(err))
			break
		}
	}
	if start != "" {
		// Back where it started, so the pooled connection does not stay in master.
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "USE "+domain.QuoteName(start)+";"); err != nil && runErr == nil {
			runErr = friendlyError(err)
		}
	}
	return runErr
}
