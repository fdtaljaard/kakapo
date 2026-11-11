package domain

import (
	"fmt"
	"strings"
)

// The T-SQL of phase 5p (docs/phase5p-contract.md): the two tables and the procedure kakapo installs in
// master, the settings row of one database, and the statements that take them away. The procedure is
// generated once per server edition: only the backup's compression differs (Express and Web cannot
// compress). Everything else it decides when it runs: which tasks are due, whether Ola Hallengren's
// procedures are there, and whether the check is physical only.

// The objects, all in master.
const (
	MaintProcName     = "kakapo_Maintenance"
	MaintSettingsName = "kakapo_MaintenanceSettings"
	MaintLogName      = "kakapo_MaintenanceLog"
	// MaintProcVersion is written into the procedure's header; a newer kakapo replaces an older one.
	MaintProcVersion = 1
	// maintMarker is the header line that makes a kakapo_Maintenance kakapo's.
	maintMarker = "kakapo maintenance procedure, version "
)

// The tasks, as the log's Task column and the plan's pieces name them.
var maintTasks = []string{PieceIntegrity, PieceStatistics, PieceIndexes, PieceFullBackup, PieceLogBackup, PieceCleanup}

// The outcomes the log keeps. Running is the row of a task under way (or of a call that died in it).
const (
	MaintOK      = "OK"
	MaintFailed  = "Failed"
	MaintSkipped = "Skipped"
	MaintRunning = "Running"
)

// The rules' numbers: heavy work runs within 6 hours of its time; a log backup is due one minute
// early, so a call every 15 minutes is not put off by a second's drift; the check is physical only
// from 200 GB of data (26,214,400 pages of 8 KB), as the CHECKDB fix decides it.
const (
	maintWindowHours   = 6
	maintLogSlackSecs  = 60
	maintPhysicalPages = 26214400
)

// maintTablesBatch creates the two tables when they are not there.
const maintTablesBatch = `/* kakapo */
IF OBJECT_ID(N'dbo.kakapo_MaintenanceSettings', N'U') IS NULL
CREATE TABLE dbo.kakapo_MaintenanceSettings (
    DatabaseName    sysname       NOT NULL CONSTRAINT PK_kakapo_MaintenanceSettings PRIMARY KEY,
    BackupFolder    nvarchar(400) NOT NULL,
    KeepBackupDays  int           NOT NULL,
    FullTime        time(0)       NOT NULL,
    LogMinutes      int           NOT NULL,
    CheckDay        tinyint       NOT NULL, /* 1 Sunday .. 7 Saturday */
    CheckTime       time(0)       NOT NULL,
    StatsTime       time(0)       NOT NULL,
    IndexWork       bit           NOT NULL,
    IndexTime       time(0)       NOT NULL,
    CleanupDay      tinyint       NOT NULL,
    CleanupTime     time(0)       NOT NULL,
    KeepHistoryDays int           NOT NULL,
    Enabled         bit           NOT NULL CONSTRAINT DF_kakapo_MaintenanceSettings_Enabled DEFAULT (1),
    CreatedAt       datetime      NOT NULL CONSTRAINT DF_kakapo_MaintenanceSettings_CreatedAt DEFAULT (GETDATE()),
    LastCall        datetime      NULL);
IF OBJECT_ID(N'dbo.kakapo_MaintenanceLog', N'U') IS NULL
CREATE TABLE dbo.kakapo_MaintenanceLog (
    ID           int IDENTITY(1, 1) NOT NULL CONSTRAINT PK_kakapo_MaintenanceLog PRIMARY KEY,
    DatabaseName sysname        NOT NULL,
    Task         varchar(20)    NOT NULL,
    StartTime    datetime       NOT NULL,
    EndTime      datetime       NULL,
    Outcome      varchar(10)    NOT NULL, /* OK, Failed, Skipped; Running while it runs */
    ErrorNumber  int            NULL,
    Message      nvarchar(4000) NULL,
    Command      nvarchar(max)  NULL,
    BackupFile   nvarchar(600)  NULL);`

// maintIndexBatch is the log's index, in a batch of its own (the table may be new in the batch before).
const maintIndexBatch = `/* kakapo */
IF NOT EXISTS (SELECT 1 FROM sys.indexes WHERE name = N'IX_kakapo_MaintenanceLog_Task' AND object_id = OBJECT_ID(N'dbo.kakapo_MaintenanceLog'))
CREATE INDEX IX_kakapo_MaintenanceLog_Task ON dbo.kakapo_MaintenanceLog (DatabaseName, Task, StartTime) INCLUDE (Outcome);`

// maintStubBatch makes sure a procedure exists, so the body is always an ALTER (no CREATE OR ALTER
// before SQL Server 2016 SP1).
const maintStubBatch = `/* kakapo */
IF OBJECT_ID(N'dbo.kakapo_Maintenance', N'P') IS NULL EXEC (N'CREATE PROCEDURE dbo.kakapo_Maintenance AS RETURN 0;');`

// maintProcBody is the procedure. {{VERSION}}, {{WITH}} (the native backup's options) and
// {{COMPRESS}} (Ola's @Compress) are filled by edition.
//
// Sources: the commands are the ones kakapo's single fixes write (CheckdbStatement, the backup
// schedule's native steps with RESTORE VERIFYONLY and xp_delete_file, sp_updatestats, Ola Hallengren's
// DatabaseBackup, DatabaseIntegrityCheck and IndexOptimize with 5b's thresholds, HistoryCleanupCommand).
// The due rules are docs/phase5p-contract.md's, mirrored in Go by MaintDue.
const maintProcBody = `/* kakapo */
ALTER PROCEDURE dbo.kakapo_Maintenance
    @Database sysname = NULL /* only this database's row; NULL: every enabled row */
AS
/* kakapo maintenance procedure, version {{VERSION}}. Created by kakapo (docs/phase5p-contract.md): do not edit, kakapo replaces it.
   Call it every 15 minutes from any scheduler:  EXEC master.dbo.kakapo_Maintenance;
   Each call does what is due for each enabled row of dbo.kakapo_MaintenanceSettings, in this order: the integrity check,
   statistics, index work, the full backup, the log backup, the history cleanup; and writes each task to
   dbo.kakapo_MaintenanceLog. A task is due when its latest scheduled time has passed and the log has no successful run
   that started after it. The check, statistics and index work run only within 6 hours of their time, and once per
   time (a failed one waits for the next); backups always catch up. One lock per database for the heavy work and the
   full backup, another for the log backup. A failed task does not stop the rest; the call raises an error at the end. */
BEGIN
    SET NOCOUNT ON;
    SET LOCK_TIMEOUT -1;
    DECLARE @failed nvarchar(4000) = N'';
    DECLARE @rows TABLE (n int IDENTITY(1, 1) PRIMARY KEY, db sysname NOT NULL, folder nvarchar(400) NOT NULL, keepDays int NOT NULL,
        fullTime time(0) NOT NULL, logMinutes int NOT NULL, checkDay tinyint NOT NULL, checkTime time(0) NOT NULL, statsTime time(0) NOT NULL,
        indexWork bit NOT NULL, indexTime time(0) NOT NULL, cleanupDay tinyint NOT NULL, cleanupTime time(0) NOT NULL, historyDays int NOT NULL);
    INSERT @rows (db, folder, keepDays, fullTime, logMinutes, checkDay, checkTime, statsTime, indexWork, indexTime, cleanupDay, cleanupTime, historyDays)
    SELECT DatabaseName, BackupFolder, KeepBackupDays, FullTime, LogMinutes, CheckDay, CheckTime, StatsTime, IndexWork, IndexTime, CleanupDay,
           CleanupTime, KeepHistoryDays
    FROM dbo.kakapo_MaintenanceSettings
    WHERE Enabled = 1 AND (@Database IS NULL OR DatabaseName = @Database)
    ORDER BY DatabaseName;

    DECLARE @i int = 1, @n int = (SELECT COUNT(*) FROM @rows);
    DECLARE @db sysname, @folder nvarchar(400), @keepDays int, @fullTime time(0), @logMinutes int, @checkDay tinyint, @checkTime time(0),
            @statsTime time(0), @indexWork bit, @indexTime time(0), @cleanupDay tinyint, @cleanupTime time(0), @historyDays int;
    DECLARE @now datetime, @today datetime, @dow int, @state nvarchar(60), @recovery nvarchar(60), @olaName bit, @physical bit,
            @fullDue datetime, @checkDue datetime, @statsDue datetime, @indexDue datetime, @cleanupDue datetime, @lastLog datetime,
            @safe nvarchar(260), @dir nvarchar(600), @stamp nvarchar(20), @qdb nvarchar(300), @qdir nvarchar(1300), @qfolder nvarchar(900),
            @file nvarchar(800), @cutoff nvarchar(30);
    DECLARE @tasks TABLE (ord int NOT NULL PRIMARY KEY, task varchar(20) NOT NULL, lockKind nvarchar(10) NOT NULL, due datetime NOT NULL,
            cmd nvarchar(max) NULL, backupFile nvarchar(800) NULL, skip nvarchar(400) NULL);
    DECLARE @t int, @task varchar(20), @lockKind nvarchar(10), @due datetime, @cmd nvarchar(max), @skip nvarchar(400), @lockRes nvarchar(255),
            @held nvarchar(100), @id int, @rc int, @started datetime, @tookFull bit;

    IF @Database IS NOT NULL AND @n = 0
        SET @failed = @Database + N' has no enabled row in dbo.kakapo_MaintenanceSettings';

    WHILE @i <= @n
    BEGIN
        SELECT @db = db, @folder = folder, @keepDays = keepDays, @fullTime = fullTime, @logMinutes = logMinutes, @checkDay = checkDay,
               @checkTime = checkTime, @statsTime = statsTime, @indexWork = indexWork, @indexTime = indexTime, @cleanupDay = cleanupDay,
               @cleanupTime = cleanupTime, @historyDays = historyDays
        FROM @rows WHERE n = @i;
        SET @i += 1;
        SET @now = GETDATE();
        UPDATE dbo.kakapo_MaintenanceSettings SET LastCall = @now WHERE DatabaseName = @db;

        SET @state = CONVERT(nvarchar(60), DATABASEPROPERTYEX(@db, 'Status'));
        IF @state IS NULL OR @state <> N'ONLINE'
        BEGIN
            SET @failed += CASE WHEN @failed = N'' THEN N'' ELSE N'; ' END + @db + N' is ' + ISNULL(LOWER(@state), N'not on this server');
            CONTINUE;
        END;

        /* The latest scheduled time of each task (1 Sunday .. 7 Saturday, whatever DATEFIRST says). */
        SET @today = CONVERT(datetime, CONVERT(date, @now));
        SET @dow = DATEDIFF(DAY, '19000107', @now) % 7 + 1;
        SET @fullDue = @today + CONVERT(datetime, @fullTime);
        IF @fullDue > @now SET @fullDue = DATEADD(DAY, -1, @fullDue);
        SET @checkDue = DATEADD(DAY, -((@dow - @checkDay + 7) % 7), @today) + CONVERT(datetime, @checkTime);
        IF @checkDue > @now SET @checkDue = DATEADD(DAY, -7, @checkDue);
        SET @indexDue = DATEADD(DAY, -((@dow - @checkDay + 7) % 7), @today) + CONVERT(datetime, @indexTime);
        IF @indexDue > @now SET @indexDue = DATEADD(DAY, -7, @indexDue);
        SET @statsDue = @today + CONVERT(datetime, @statsTime);
        IF @statsDue > @now SET @statsDue = DATEADD(DAY, -1, @statsDue);
        IF DATEDIFF(DAY, '19000107', @statsDue) % 7 + 1 = @checkDay SET @statsDue = DATEADD(DAY, -1, @statsDue);
        SET @cleanupDue = DATEADD(DAY, -((@dow - @cleanupDay + 7) % 7), @today) + CONVERT(datetime, @cleanupTime);
        IF @cleanupDue > @now SET @cleanupDue = DATEADD(DAY, -7, @cleanupDue);
        SET @lastLog = (SELECT MAX(StartTime) FROM dbo.kakapo_MaintenanceLog WHERE DatabaseName = @db AND Task = 'log-backup' AND Outcome = 'OK');

        /* What the commands need: the name in a literal, Ola's list syntax, the file names, the size. */
        SELECT @recovery = recovery_model_desc FROM sys.databases WHERE name = @db;
        SET @olaName = CASE WHEN CHARINDEX(N',', @db) + CHARINDEX(N'%', @db) + CHARINDEX(N'[', @db) + CHARINDEX(N']', @db) = 0
                             AND LEFT(@db, 1) <> N'-' THEN 1 ELSE 0 END;
        SET @physical = CASE WHEN (SELECT SUM(CONVERT(bigint, size)) FROM sys.master_files WHERE database_id = DB_ID(@db) AND type = 0) >= {{PHYSICAL}}
                             THEN 1 ELSE 0 END;
        SET @safe = REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(@db, N'\', N'_'), N'/', N'_'), N':', N'_'), N'*', N'_'),
                    N'?', N'_'), N'"', N'_'), N'<', N'_'), N'>', N'_'), N'|', N'_');
        SET @dir = CASE WHEN RIGHT(@folder, 1) = N'\' THEN @folder ELSE @folder + N'\' END + @safe;
        SET @stamp = CONVERT(nvarchar(8), @now, 112) + N'_' + REPLACE(CONVERT(nvarchar(8), @now, 108), N':', N'');
        SET @qdb = REPLACE(@db, N'''', N'''''');
        SET @qdir = REPLACE(@dir, N'''', N'''''');
        SET @qfolder = REPLACE(@folder, N'''', N'''''');
        SET @cutoff = CONVERT(nvarchar(19), DATEADD(DAY, -@keepDays, @now), 126);

        DELETE @tasks;
        /* 1. Integrity check: weekly; heavy. */
        IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog
                       WHERE DatabaseName = @db AND Task = 'integrity-check' AND StartTime >= @checkDue AND Outcome IN ('OK', 'Failed'))
            INSERT @tasks (ord, task, lockKind, due, cmd, skip)
            SELECT 1, 'integrity-check', N'heavy', @checkDue,
                   CASE WHEN OBJECT_ID(N'master.dbo.DatabaseIntegrityCheck', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL
                             AND @olaName = 1
                        THEN N'/* kakapo */ EXECUTE master.dbo.DatabaseIntegrityCheck @Databases = N''' + @qdb + N''', @CheckCommands = ''CHECKDB'', @PhysicalOnly = '''
                             + CASE WHEN @physical = 1 THEN N'Y' ELSE N'N' END + N''';'
                        ELSE N'/* kakapo */ DBCC CHECKDB (' + QUOTENAME(@db) + N') WITH NO_INFOMSGS, ALL_ERRORMSGS'
                             + CASE WHEN @physical = 1 THEN N', PHYSICAL_ONLY' ELSE N'' END + N';' END,
                   CASE WHEN @now >= DATEADD(HOUR, {{WINDOW}}, @checkDue)
                        THEN N'Skipped: outside the window (' + CONVERT(nvarchar(16), @checkDue, 120) + N' to '
                             + CONVERT(nvarchar(16), DATEADD(HOUR, {{WINDOW}}, @checkDue), 120) + N'); it waits for the next one.' END;

        /* 2. Statistics: daily except the check day; heavy. */
        IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog
                       WHERE DatabaseName = @db AND Task = 'statistics' AND StartTime >= @statsDue AND Outcome IN ('OK', 'Failed'))
            INSERT @tasks (ord, task, lockKind, due, cmd, skip)
            SELECT 2, 'statistics', N'heavy', @statsDue,
                   CASE WHEN OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND @olaName = 1
                        THEN N'/* kakapo */ EXECUTE master.dbo.IndexOptimize @Databases = N''' + @qdb + N''', @FragmentationLow = NULL, @FragmentationMedium = NULL, '
                             + N'@FragmentationHigh = NULL, @UpdateStatistics = ''ALL'', @OnlyModifiedStatistics = ''Y'', @StatisticsModificationLevel = 5;'
                        ELSE N'/* kakapo */ EXEC ' + QUOTENAME(@db) + N'.sys.sp_updatestats;' END,
                   CASE WHEN @now >= DATEADD(HOUR, {{WINDOW}}, @statsDue)
                        THEN N'Skipped: outside the window (' + CONVERT(nvarchar(16), @statsDue, 120) + N' to '
                             + CONVERT(nvarchar(16), DATEADD(HOUR, {{WINDOW}}, @statsDue), 120) + N'); it waits for the next one.' END;

        /* 3. Index work: weekly on the check day, when it is on; heavy; only with Ola Hallengren's IndexOptimize.
              Brent Ozar's thresholds and never an offline rebuild (docs/phase5q-contract.md, decisions 1 and 2). */
        IF @indexWork = 1 AND NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog
                       WHERE DatabaseName = @db AND Task = 'indexes' AND StartTime >= @indexDue AND Outcome IN ('OK', 'Failed'))
            INSERT @tasks (ord, task, lockKind, due, cmd, skip)
            SELECT 3, 'indexes', N'heavy', @indexDue,
                   N'/* kakapo */ EXECUTE master.dbo.IndexOptimize @Databases = N''' + @qdb + N''', @FragmentationLow = NULL, '
                   + N'@FragmentationMedium = ''INDEX_REORGANIZE'', @FragmentationHigh = ''INDEX_REBUILD_ONLINE,INDEX_REORGANIZE'', '
                   + N'@FragmentationLevel1 = 50, @FragmentationLevel2 = 80, @MinNumberOfPages = 1000, @UpdateStatistics = ''ALL'', '
                   + N'@OnlyModifiedStatistics = ''Y'', @StatisticsModificationLevel = 5;',
                   CASE WHEN OBJECT_ID(N'master.dbo.IndexOptimize', N'P') IS NULL OR OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NULL OR @olaName = 0
                        THEN N'Skipped: index work needs Ola Hallengren''s IndexOptimize in master (and a database name it can read).'
                        WHEN @now >= DATEADD(HOUR, {{WINDOW}}, @indexDue)
                        THEN N'Skipped: outside the window (' + CONVERT(nvarchar(16), @indexDue, 120) + N' to '
                             + CONVERT(nvarchar(16), DATEADD(HOUR, {{WINDOW}}, @indexDue), 120) + N'); it waits for the next one.' END;

        /* 4. Full backup: daily; catches up; under the heavy lock. */
        IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog
                       WHERE DatabaseName = @db AND Task = 'full-backup' AND StartTime >= @fullDue AND Outcome = 'OK')
        BEGIN
            SET @file = @dir + N'\' + @safe + N'_FULL_' + @stamp + N'.bak';
            INSERT @tasks (ord, task, lockKind, due, cmd, backupFile)
            SELECT 4, 'full-backup', N'heavy', @fullDue,
                   CASE WHEN OBJECT_ID(N'master.dbo.DatabaseBackup', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND @olaName = 1
                        THEN N'/* kakapo */ EXECUTE master.dbo.DatabaseBackup @Databases = N''' + @qdb + N''', @Directory = N''' + @qfolder
                             + N''', @BackupType = ''FULL'', @Verify = ''Y'', @CheckSum = ''Y'', @CleanupTime = ' + CONVERT(nvarchar(10), @keepDays * 24)
                             + N', @Compress = ''{{COMPRESS}}'';'
                        ELSE N'/* kakapo */' + NCHAR(10)
                             + N'DECLARE @rc int;' + NCHAR(10)
                             + N'EXEC @rc = master.dbo.xp_create_subdir N''' + @qdir + N''';' + NCHAR(10)
                             + N'IF @rc <> 0 RAISERROR(N''The folder %s could not be created.'', 16, 1, N''' + @qdir + N''');' + NCHAR(10)
                             + N'BACKUP DATABASE ' + QUOTENAME(@db) + N' TO DISK = N''' + REPLACE(@file, N'''', N'''''') + N''' WITH {{WITH}};' + NCHAR(10)
                             + N'RESTORE VERIFYONLY FROM DISK = N''' + REPLACE(@file, N'''', N'''''') + N''' WITH CHECKSUM;' + NCHAR(10)
                             + N'EXEC master.dbo.xp_delete_file 0, N''' + @qdir + N''', N''bak'', N''' + @cutoff + N''', 0;' END,
                   CASE WHEN OBJECT_ID(N'master.dbo.DatabaseBackup', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND @olaName = 1
                        THEN NULL ELSE @file END;
        END;

        /* 5. Log backup: every call, at least LogMinutes after the last one, in FULL or BULK_LOGGED recovery, once a full backup exists. */
        IF @recovery IN (N'FULL', N'BULK_LOGGED') AND (@lastLog IS NULL OR DATEDIFF(SECOND, @lastLog, @now) >= @logMinutes * 60 - {{SLACK}})
        BEGIN
            SET @file = @dir + N'\' + @safe + N'_LOG_' + @stamp + N'.trn';
            INSERT @tasks (ord, task, lockKind, due, cmd, backupFile)
            SELECT 5, 'log-backup', N'log', @now,
                   CASE WHEN OBJECT_ID(N'master.dbo.DatabaseBackup', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND @olaName = 1
                        THEN N'/* kakapo */ EXECUTE master.dbo.DatabaseBackup @Databases = N''' + @qdb + N''', @Directory = N''' + @qfolder
                             + N''', @BackupType = ''LOG'', @Verify = ''Y'', @CheckSum = ''Y'', @CleanupTime = ' + CONVERT(nvarchar(10), @keepDays * 24)
                             + N', @Compress = ''{{COMPRESS}}'';'
                        ELSE N'/* kakapo */' + NCHAR(10)
                             + N'DECLARE @rc int;' + NCHAR(10)
                             + N'EXEC @rc = master.dbo.xp_create_subdir N''' + @qdir + N''';' + NCHAR(10)
                             + N'IF @rc <> 0 RAISERROR(N''The folder %s could not be created.'', 16, 1, N''' + @qdir + N''');' + NCHAR(10)
                             + N'BACKUP LOG ' + QUOTENAME(@db) + N' TO DISK = N''' + REPLACE(@file, N'''', N'''''') + N''' WITH {{WITH}};' + NCHAR(10)
                             + N'RESTORE VERIFYONLY FROM DISK = N''' + REPLACE(@file, N'''', N'''''') + N''' WITH CHECKSUM;' + NCHAR(10)
                             + N'EXEC master.dbo.xp_delete_file 0, N''' + @qdir + N''', N''trn'', N''' + @cutoff + N''', 0;' END,
                   CASE WHEN OBJECT_ID(N'master.dbo.DatabaseBackup', N'P') IS NOT NULL AND OBJECT_ID(N'master.dbo.CommandExecute', N'P') IS NOT NULL AND @olaName = 1
                        THEN NULL ELSE @file END;
        END;

        /* 6. History cleanup: weekly; server-wide, so a run for any database counts. */
        IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog WHERE Task = 'history-cleanup' AND StartTime >= @cleanupDue AND Outcome = 'OK')
            INSERT @tasks (ord, task, lockKind, due, cmd)
            SELECT 6, 'history-cleanup', N'cleanup', @cleanupDue,
                   N'/* kakapo */' + NCHAR(10)
                   + N'DECLARE @oldest datetime = DATEADD(DAY, -' + CONVERT(nvarchar(10), @historyDays) + N', GETDATE());' + NCHAR(10)
                   + N'EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;' + NCHAR(10)
                   /* The job history directly: msdb.dbo.sp_purge_jobhistory checks the caller's role itself and refuses
                      the certificate's sysadmin (error 14392), where sp_delete_backuphistory accepts it. */
                   + N'IF OBJECT_ID(N''msdb.dbo.sysjobhistory'', N''U'') IS NOT NULL DELETE FROM msdb.dbo.sysjobhistory '
                   + N'WHERE run_date < CONVERT(int, CONVERT(char(8), @oldest, 112));' + NCHAR(10)
                   + N'IF OBJECT_ID(N''master.dbo.CommandLog'', N''U'') IS NOT NULL DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;' + NCHAR(10)
                   + N'DELETE FROM master.dbo.kakapo_MaintenanceLog WHERE StartTime < @oldest;';

        /* Run what is due, in order. */
        SET @t = 0;
        SET @tookFull = 0;
        SET @held = N'|';
        WHILE 1 = 1
        BEGIN
            SELECT TOP (1) @t = ord, @task = task, @lockKind = lockKind, @due = due, @cmd = cmd, @file = backupFile, @skip = skip
            FROM @tasks WHERE ord > @t ORDER BY ord;
            IF @@ROWCOUNT = 0 BREAK;

            IF @task = 'log-backup'
               AND (@tookFull = 1 /* this call's full backup is the fresh point: the next call backs up the log */
                    OR NOT EXISTS (SELECT 1 FROM sys.database_recovery_status WHERE database_id = DB_ID(@db) AND last_log_backup_lsn IS NOT NULL))
                CONTINUE; /* no full backup yet: SQL Server cannot back up the log */

            IF @skip IS NOT NULL
            BEGIN
                IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog WHERE DatabaseName = @db AND Task = @task AND StartTime >= @due)
                    INSERT dbo.kakapo_MaintenanceLog (DatabaseName, Task, StartTime, EndTime, Outcome, Message)
                    VALUES (@db, @task, GETDATE(), GETDATE(), 'Skipped', @skip);
                CONTINUE;
            END;

            IF CHARINDEX(N'|' + @lockKind + N'|', @held) = 0
            BEGIN
                SET @lockRes = N'kakapo_Maintenance ' + @lockKind + CASE WHEN @lockKind = N'cleanup' THEN N'' ELSE N' ' + @db END;
                EXEC @rc = sys.sp_getapplock @Resource = @lockRes, @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = 0;
                IF @rc < 0
                BEGIN
                    IF NOT EXISTS (SELECT 1 FROM dbo.kakapo_MaintenanceLog WHERE DatabaseName = @db AND Task = @task AND StartTime >= @due)
                        INSERT dbo.kakapo_MaintenanceLog (DatabaseName, Task, StartTime, EndTime, Outcome, Message)
                        VALUES (@db, @task, GETDATE(), GETDATE(), 'Skipped', N'Skipped: another call holds the lock ' + @lockRes + N'.');
                    CONTINUE;
                END;
                SET @held += @lockKind + N'|';
            END;

            SET @started = GETDATE();
            INSERT dbo.kakapo_MaintenanceLog (DatabaseName, Task, StartTime, Outcome, Command, BackupFile)
            VALUES (@db, @task, @started, 'Running', @cmd, @file);
            SET @id = SCOPE_IDENTITY();
            BEGIN TRY
                EXEC sys.sp_executesql @cmd;
                IF @file IS NULL AND @task IN ('full-backup', 'log-backup') /* Ola names its own files: read it back from msdb */
                    SELECT TOP (1) @file = mf.physical_device_name
                    FROM msdb.dbo.backupset AS b
                    JOIN msdb.dbo.backupmediafamily AS mf ON mf.media_set_id = b.media_set_id
                    WHERE b.database_name = @db AND b.type = CASE WHEN @task = 'full-backup' THEN 'D' ELSE 'L' END
                      AND b.backup_start_date >= DATEADD(SECOND, -1, @started)
                    ORDER BY b.backup_finish_date DESC;
                UPDATE dbo.kakapo_MaintenanceLog SET EndTime = GETDATE(), Outcome = 'OK', BackupFile = @file WHERE ID = @id;
                IF @task = 'full-backup' SET @tookFull = 1;
            END TRY
            BEGIN CATCH
                IF XACT_STATE() <> 0 ROLLBACK;
                UPDATE dbo.kakapo_MaintenanceLog
                SET EndTime = GETDATE(), Outcome = 'Failed', ErrorNumber = ERROR_NUMBER(), Message = LEFT(ERROR_MESSAGE(), 4000)
                WHERE ID = @id;
                SET @failed += CASE WHEN @failed = N'' THEN N'' ELSE N'; ' END + @db + N' ' + @task + N' (' + LEFT(ERROR_MESSAGE(), 200) + N')';
            END CATCH;
        END;

        /* This database's locks go with its tasks. */
        IF CHARINDEX(N'|heavy|', @held) > 0
        BEGIN
            SET @lockRes = N'kakapo_Maintenance heavy ' + @db;
            EXEC sys.sp_releaseapplock @Resource = @lockRes, @LockOwner = 'Session';
        END;
        IF CHARINDEX(N'|log|', @held) > 0
        BEGIN
            SET @lockRes = N'kakapo_Maintenance log ' + @db;
            EXEC sys.sp_releaseapplock @Resource = @lockRes, @LockOwner = 'Session';
        END;
        IF CHARINDEX(N'|cleanup|', @held) > 0
        BEGIN
            SET @lockRes = N'kakapo_Maintenance cleanup';
            EXEC sys.sp_releaseapplock @Resource = @lockRes, @LockOwner = 'Session';
        END;
    END;

    IF @failed <> N''
    BEGIN
        SET @failed = LEFT(@failed, 1800);
        RAISERROR(N'kakapo_Maintenance failed: %s', 16, 1, @failed);
    END;
END;`

// MaintProcSQL is the procedure for an edition: compress says the edition can compress backups.
func MaintProcSQL(compress bool) string {
	with, ola := "CHECKSUM, INIT", "N"
	if compress {
		with, ola = "CHECKSUM, INIT, COMPRESSION", "Y"
	}
	return strings.NewReplacer("{{VERSION}}", fmt.Sprint(MaintProcVersion), "{{WITH}}", with, "{{COMPRESS}}", ola,
		"{{WINDOW}}", fmt.Sprint(maintWindowHours), "{{SLACK}}", fmt.Sprint(maintLogSlackSecs), "{{PHYSICAL}}", fmt.Sprint(maintPhysicalPages)).Replace(maintProcBody)
}

// maintDayNumber is a day as the settings keep it: 1 Sunday .. 7 Saturday.
func maintDayNumber(day string) int { return weekdayOf(day) + 1 }

// weekdayOf is a day's time.Weekday number (Sunday 0); 0 when unknown.
func weekdayOf(day string) int {
	for i, d := range CheckdbDays {
		if strings.EqualFold(d, strings.TrimSpace(day)) {
			return i
		}
	}
	return 0
}

// MaintSettingsUpsert writes one database's settings row: updated when there, else inserted (Enabled).
func MaintSettingsUpsert(s MaintSettings) string {
	set := fmt.Sprintf("BackupFolder = %s, KeepBackupDays = %d, FullTime = '%s', LogMinutes = %d, CheckDay = %d, CheckTime = '%s', StatsTime = '%s',\n"+
		"    IndexWork = %d, IndexTime = '%s', CleanupDay = %d, CleanupTime = '%s', KeepHistoryDays = %d, Enabled = 1",
		nstr(s.Folder), s.KeepBackupDays, s.FullTime, s.LogMinutes, maintDayNumber(s.CheckDay), s.CheckTime, s.StatsTime,
		bit(s.IndexWork), s.IndexTime, maintDayNumber(s.CleanupDay), s.CleanupTime, s.KeepHistoryDays)
	values := fmt.Sprintf("%s, %s, %d, '%s', %d, %d, '%s', '%s', %d, '%s', %d, '%s', %d, 1",
		nstr(s.Database), nstr(s.Folder), s.KeepBackupDays, s.FullTime, s.LogMinutes, maintDayNumber(s.CheckDay), s.CheckTime, s.StatsTime,
		bit(s.IndexWork), s.IndexTime, maintDayNumber(s.CleanupDay), s.CleanupTime, s.KeepHistoryDays)
	return "/* kakapo */\nUPDATE dbo.kakapo_MaintenanceSettings\nSET " + set + "\nWHERE DatabaseName = " + nstr(s.Database) + ";\n" +
		"IF @@ROWCOUNT = 0\n    INSERT dbo.kakapo_MaintenanceSettings (DatabaseName, BackupFolder, KeepBackupDays, FullTime, LogMinutes, CheckDay, CheckTime, StatsTime,\n" +
		"        IndexWork, IndexTime, CleanupDay, CleanupTime, KeepHistoryDays, Enabled)\n    VALUES (" + values + ");"
}

// maintUseMaster starts every script: the procedure must be created in master.
const maintUseMaster = "USE master;"

// The procedure is signed with a certificate so that the scheduled task can run as NT AUTHORITY\SYSTEM,
// which is not sysadmin, with EXECUTE on this one procedure and nothing else. The certificate's login
// is sysadmin; while the signed procedure runs, the caller's security context carries that login's
// token, its dynamic SQL included. The private key is removed after signing, so nothing else can ever
// be signed with it; every re-apply (ALTER PROCEDURE drops the signature) makes a new certificate.
//
// Sources: Erland Sommarskog, "Packaging Permissions in Stored Procedures"
// (https://www.sommarskog.se/grantperm.html), the certificate, its login, ADD SIGNATURE and REMOVE
// PRIVATE KEY; Microsoft Learn, "Module signing (Database Engine)" and "ADD SIGNATURE (Transact-SQL)".
const (
	MaintCertName       = "kakapo_MaintenanceCert"
	MaintCertLogin      = "kakapo_MaintenanceLogin"
	MaintSchedulerLogin = `NT AUTHORITY\SYSTEM`
)

// maintSignBatch signs the procedure: the old signature, login and certificate dropped, a new
// certificate with a throwaway password, a login from it in sysadmin, the signature, the private key
// removed. One transaction: a half-made signing (a sysadmin login with no signature) never stays.
const maintSignBatch = `/* kakapo */
/* Sign kakapo_Maintenance (module signing: Erland Sommarskog, "Packaging Permissions in Stored Procedures";
   Microsoft Learn, "Module signing"). The certificate's login is sysadmin, because every task reaches other
   databases, msdb and xp_delete_file; the private key is removed, so nothing else can be signed with it. */
SET XACT_ABORT ON;
DECLARE @pw nvarchar(80) = CONVERT(nvarchar(36), NEWID()) + LOWER(CONVERT(nvarchar(36), NEWID())), @sql nvarchar(max);
BEGIN TRANSACTION;
IF EXISTS (SELECT 1 FROM sys.crypt_properties AS cp JOIN sys.certificates AS c ON c.thumbprint = cp.thumbprint
           WHERE cp.class = 1 AND cp.major_id = OBJECT_ID(N'dbo.kakapo_Maintenance') AND c.name = N'kakapo_MaintenanceCert')
    DROP SIGNATURE FROM dbo.kakapo_Maintenance BY CERTIFICATE [kakapo_MaintenanceCert];
IF SUSER_ID(N'kakapo_MaintenanceLogin') IS NOT NULL DROP LOGIN [kakapo_MaintenanceLogin];
IF CERT_ID(N'kakapo_MaintenanceCert') IS NOT NULL DROP CERTIFICATE [kakapo_MaintenanceCert];
SET @sql = N'CREATE CERTIFICATE [kakapo_MaintenanceCert] ENCRYPTION BY PASSWORD = ''' + @pw + N''''
         + N' WITH SUBJECT = N''kakapo: signs master.dbo.kakapo_Maintenance'', EXPIRY_DATE = ''99991231'';';
EXEC (@sql);
CREATE LOGIN [kakapo_MaintenanceLogin] FROM CERTIFICATE [kakapo_MaintenanceCert];
ALTER SERVER ROLE [sysadmin] ADD MEMBER [kakapo_MaintenanceLogin];
SET @sql = N'ADD SIGNATURE TO dbo.kakapo_Maintenance BY CERTIFICATE [kakapo_MaintenanceCert] WITH PASSWORD = ''' + @pw + N''';';
EXEC (@sql);
ALTER CERTIFICATE [kakapo_MaintenanceCert] REMOVE PRIVATE KEY;
COMMIT;`

// maintSchedulerBatch lets the scheduled task's account (SYSTEM) execute the procedure and nothing
// more: its master user, made (and marked as kakapo's) when there is none, and EXECUTE on the procedure.
const maintSchedulerBatch = `/* kakapo */
/* The scheduled task runs as NT AUTHORITY\SYSTEM: it may EXECUTE kakapo_Maintenance and nothing else. */
DECLARE @user sysname = (SELECT dp.name FROM sys.database_principals AS dp WHERE dp.sid = SUSER_SID(N'NT AUTHORITY\SYSTEM')), @sql nvarchar(400);
IF SUSER_SID(N'NT AUTHORITY\SYSTEM') IS NOT NULL
BEGIN
    IF @user IS NULL
    BEGIN
        CREATE USER [NT AUTHORITY\SYSTEM] FOR LOGIN [NT AUTHORITY\SYSTEM];
        EXEC sys.sp_addextendedproperty @name = N'kakapo', @value = N'created by kakapo for kakapo_Maintenance',
             @level0type = N'USER', @level0name = N'NT AUTHORITY\SYSTEM';
        SET @user = N'NT AUTHORITY\SYSTEM';
    END;
    SET @sql = N'GRANT EXECUTE ON dbo.kakapo_Maintenance TO ' + QUOTENAME(@user) + N';';
    EXEC (@sql);
END;`

// MaintInstallStatement is install-maintenance-proc's statement, in batches separated by GO (the
// service runs them one by one on one connection; sqlcmd and SSMS run the same text): master, the
// tables, the log's index, the procedure (created as a stub, then altered: it is updated in place when
// it is there), its signature, SYSTEM's EXECUTE, then each settings row.
func MaintInstallStatement(compress bool, rows ...MaintSettings) string {
	batches := []string{maintUseMaster, maintTablesBatch, maintIndexBatch, maintStubBatch, MaintProcSQL(compress), maintSignBatch, maintSchedulerBatch}
	for _, s := range rows {
		batches = append(batches, MaintSettingsUpsert(s))
	}
	return JoinBatches(batches)
}

// MaintDropStatement drops the procedure (its signature and SYSTEM's EXECUTE with it) and both tables
// (the log with them), the certificate's login and the certificate, and SYSTEM's master user when kakapo
// made it: remove-maintenance-proc's statement, and the rollback of a first install.
func MaintDropStatement() string {
	return JoinBatches([]string{maintUseMaster, "/* kakapo */\n" +
		"IF OBJECT_ID(N'dbo.kakapo_Maintenance', N'P') IS NOT NULL DROP PROCEDURE dbo.kakapo_Maintenance;\n" +
		"IF OBJECT_ID(N'dbo.kakapo_MaintenanceLog', N'U') IS NOT NULL DROP TABLE dbo.kakapo_MaintenanceLog;\n" +
		"IF OBJECT_ID(N'dbo.kakapo_MaintenanceSettings', N'U') IS NOT NULL DROP TABLE dbo.kakapo_MaintenanceSettings;\n" +
		"IF SUSER_ID(N'kakapo_MaintenanceLogin') IS NOT NULL DROP LOGIN [kakapo_MaintenanceLogin];\n" +
		"IF CERT_ID(N'kakapo_MaintenanceCert') IS NOT NULL DROP CERTIFICATE [kakapo_MaintenanceCert];\n" +
		"DECLARE @user sysname = (SELECT dp.name FROM sys.database_principals AS dp\n" +
		"    JOIN sys.extended_properties AS ep ON ep.class = 4 AND ep.major_id = dp.principal_id AND ep.name = N'kakapo'\n" +
		"    WHERE dp.sid = SUSER_SID(N'NT AUTHORITY\\SYSTEM')), @sql nvarchar(400);\n" +
		"SET @sql = N'DROP USER ' + QUOTENAME(@user) + N';';\n" +
		"IF @user IS NOT NULL EXEC (@sql);"})
}

// MaintDeleteRowStatement deletes one database's settings row: the procedure stops serving it, its log
// rows stay until the cleanup.
func MaintDeleteRowStatement(db string) string {
	return JoinBatches([]string{maintUseMaster, "/* kakapo */\nDELETE FROM dbo.kakapo_MaintenanceSettings WHERE DatabaseName = " + nstr(db) + ";"})
}

// MaintRestoreRowStatement puts one database's settings row back as it was read (Enabled as read).
func MaintRestoreRowStatement(s MaintSettings) string {
	stmt := MaintSettingsUpsert(s)
	if !s.Enabled {
		stmt += "\nUPDATE dbo.kakapo_MaintenanceSettings SET Enabled = 0 WHERE DatabaseName = " + nstr(s.Database) + ";"
	}
	return JoinBatches([]string{maintUseMaster, stmt})
}

// MaintRunNowStatement calls the procedure once for one database: install-maintenance-proc's Run now.
func MaintRunNowStatement(db string) string {
	return "EXEC master.dbo.kakapo_Maintenance @Database = " + nstr(db) + ";"
}

// JoinBatches writes batches as one script, each ended by GO on its own line.
func JoinBatches(batches []string) string {
	return strings.Join(batches, "\nGO\n") + "\nGO"
}

// SplitBatches cuts a script at the lines that are only GO (any case, any spaces): the batches the
// service runs one after another. Empty batches are dropped.
func SplitBatches(script string) []string {
	out := []string{}
	var cur []string
	flush := func() {
		if b := strings.TrimSpace(strings.Join(cur, "\n")); b != "" {
			out = append(out, b)
		}
		cur = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(script, "\r\n", "\n"), "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "GO") {
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}
