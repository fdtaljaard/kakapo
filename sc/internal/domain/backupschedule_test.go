package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// scheduleFacts is a database in a recovery model that was never backed up, on a Standard edition
// instance whose Agent runs, with no backup job, no operator, no Ola, and E:\Backup as the
// instance's backup folder; its data is on D:.
func scheduleFacts(model string) BackupFacts {
	f := facts(model)
	f.CreateDate = serverNow.AddDate(0, -1, 0)
	f.ServerName, f.Edition, f.EngineVersion = `SQL01\WMS`, "Standard Edition (64-bit)", "16.0.4255.2"
	f.HasAgent, f.AgentRunning, f.AgentStatus = true, true, "Running"
	f.JobsRead, f.BackupJobs, f.KakapoJobs = true, []BackupJob{}, []string{}
	f.DefaultBackupDir, f.CompressionOK = `E:\Backup\`, true
	f.DataFiles = []string{`D:\Data\GraniteLive.mdf`}
	return f
}

func mustSchedule(t *testing.T, f BackupFacts, options map[string]string, folderOK, sameDrive bool) (FixPreview, Fix) {
	t.Helper()
	p, fix, err := BuildBackupScheduleFix(f, "", options, folderOK, sameDrive)
	if err != nil {
		t.Fatalf("backup schedule: %v", err)
	}
	return p, fix
}

// The exact batch for a FULL-recovery database without Ola: two jobs, native BACKUP with
// compression, nobody to notify.
const wantNativeBatch = `DECLARE @owner sysname = SUSER_SNAME(0x01);
BEGIN TRY
    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'kakapo: GraniteLive full backup')
        THROW 50000, N'A job named kakapo: GraniteLive full backup already exists', 1;
    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'kakapo: GraniteLive log backup')
        THROW 50000, N'A job named kakapo: GraniteLive log backup already exists', 1;
    BEGIN TRANSACTION;
    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)
        EXEC msdb.dbo.sp_add_category @class = N'JOB', @type = N'LOCAL', @name = N'Database Maintenance';
    EXEC msdb.dbo.sp_add_job @job_name = N'kakapo: GraniteLive full backup', @enabled = 1,
        @description = N'Created by kakapo on 2026-11-03 for GraniteLive: daily 22:00, log every 15 min, keep 7 days',
        @category_name = N'Database Maintenance', @owner_login_name = @owner;
    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: GraniteLive full backup', @step_id = 1, @step_name = N'Back up and verify',
        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 3, @on_fail_action = 2,
        @command = N'/* kakapo */
DECLARE @dir nvarchar(400) = N''E:\Backup\GraniteLive'';
EXEC master.dbo.xp_create_subdir @dir;
DECLARE @file nvarchar(600) = @dir + N''\GraniteLive_FULL_'' + CONVERT(nvarchar(8), GETDATE(), 112) + N''_'' + REPLACE(CONVERT(nvarchar(5), GETDATE(), 108), N'':'', N'''') + N''.bak'';
BACKUP DATABASE [GraniteLive] TO DISK = @file WITH CHECKSUM, INIT, COMPRESSION;
RESTORE VERIFYONLY FROM DISK = @file WITH CHECKSUM;';
    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: GraniteLive full backup', @step_id = 2, @step_name = N'Delete backups older than 7 days',
        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 1, @on_fail_action = 2,
        @command = N'/* kakapo */
DECLARE @cutoff datetime = DATEADD(DAY, -7, GETDATE());
EXEC master.dbo.xp_delete_file 0, N''E:\Backup\GraniteLive'', N''bak'', @cutoff, 0;';
    EXEC msdb.dbo.sp_add_jobschedule @job_name = N'kakapo: GraniteLive full backup', @name = N'kakapo: GraniteLive full backup, daily 22:00', @enabled = 1,
        @freq_type = 4, @freq_interval = 1, @active_start_time = 220000;
    EXEC msdb.dbo.sp_add_jobserver @job_name = N'kakapo: GraniteLive full backup', @server_name = N'(local)';
    EXEC msdb.dbo.sp_add_job @job_name = N'kakapo: GraniteLive log backup', @enabled = 1,
        @description = N'Created by kakapo on 2026-11-03 for GraniteLive: daily 22:00, log every 15 min, keep 7 days',
        @category_name = N'Database Maintenance', @owner_login_name = @owner;
    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: GraniteLive log backup', @step_id = 1, @step_name = N'Back up and verify',
        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 3, @on_fail_action = 2,
        @command = N'/* kakapo */
DECLARE @dir nvarchar(400) = N''E:\Backup\GraniteLive'';
EXEC master.dbo.xp_create_subdir @dir;
DECLARE @file nvarchar(600) = @dir + N''\GraniteLive_LOG_'' + CONVERT(nvarchar(8), GETDATE(), 112) + N''_'' + REPLACE(CONVERT(nvarchar(5), GETDATE(), 108), N'':'', N'''') + N''.trn'';
BACKUP LOG [GraniteLive] TO DISK = @file WITH CHECKSUM, INIT, COMPRESSION;
RESTORE VERIFYONLY FROM DISK = @file WITH CHECKSUM;';
    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: GraniteLive log backup', @step_id = 2, @step_name = N'Delete backups older than 7 days',
        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 1, @on_fail_action = 2,
        @command = N'/* kakapo */
DECLARE @cutoff datetime = DATEADD(DAY, -7, GETDATE());
EXEC master.dbo.xp_delete_file 0, N''E:\Backup\GraniteLive'', N''trn'', @cutoff, 0;';
    EXEC msdb.dbo.sp_add_jobschedule @job_name = N'kakapo: GraniteLive log backup', @name = N'kakapo: GraniteLive log backup, every 15 minutes', @enabled = 1,
        @freq_type = 4, @freq_interval = 1, @freq_subday_type = 4, @freq_subday_interval = 15;
    EXEC msdb.dbo.sp_add_jobserver @job_name = N'kakapo: GraniteLive log backup', @server_name = N'(local)';
    COMMIT TRANSACTION;
END TRY
BEGIN CATCH
    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;
    THROW;
END CATCH;`

func TestBackupScheduleNativeFull(t *testing.T) {
	f := scheduleFacts("FULL")
	_, fs, _ := build(f)
	if findingOf(t, fs, "BAK01").FixID != FixBackupSchedule || findingOf(t, fs, "BAK02").FixID != FixBackupSchedule {
		t.Errorf("findings without the fix: %+v", fs)
	}
	p, fix := mustSchedule(t, f, nil, true, false)
	if p.Statement != wantNativeBatch {
		t.Errorf("batch:\n%s", p.Statement)
	}
	if p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: GraniteLive full backup';\nEXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: GraniteLive log backup';" {
		t.Errorf("rollback %q", p.Rollback)
	}
	wantChanges := []Drift{
		{"Full backup job", "none", `daily 22:00 to E:\Backup\GraniteLive, verified`},
		{"Log backup job", "none", `every 15 minutes to E:\Backup\GraniteLive`},
		{"Keep", "", "7 days of files"},
		{"Compressed", "", "yes"},
		{"Tell on failure", "", "no one (MNT07: no operator with an email)"},
		{"First full backup", "", "now, when you apply"},
	}
	if !reflect.DeepEqual(p.Changes, wantChanges) {
		t.Errorf("changes %q", p.Changes)
	}
	if !reflect.DeepEqual(p.Warnings, []string{
		"The backup job runs as the SQL Server service account; the folder must let it write.",
		`Needs sysadmin on SQL01\WMS (creating Agent jobs owned by sa).`}) {
		t.Errorf("warnings %q", p.Warnings)
	}
	if len(p.Inputs) != 6 || p.Inputs[0].Name != OptionFolder || p.Inputs[0].Value != `E:\Backup` || p.Inputs[0].Kind != "text" ||
		p.Inputs[5].Name != OptionRunNow || p.Inputs[5].Value != "true" || p.Inputs[5].Kind != "bool" || p.Express {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if !reflect.DeepEqual(p.Options, map[string]string{OptionFolder: `E:\Backup`, OptionFullTime: "22:00", OptionLogMinutes: "15",
		OptionKeepDays: "7", OptionCompress: "true", OptionRunNow: "true"}) {
		t.Errorf("options %v", p.Options)
	}
	if fix.ID != FixBackupSchedule || fix.CheckID != "BAK01" || fix.Current != "no backup job" ||
		fix.Target != "kakapo: GraniteLive full backup (daily 22:00) and kakapo: GraniteLive log backup (every 15 minutes)" {
		t.Errorf("fix %+v", fix)
	}
	s := NewBackupSchedule(f, nil)
	if s.Description() != `Created the jobs kakapo: GraniteLive full backup (daily 22:00) and kakapo: GraniteLive log backup (every 15 minutes) writing to E:\Backup\GraniteLive, 7 days kept` ||
		s.StartStatement() != "EXEC msdb.dbo.sp_start_job @job_name = N'kakapo: GraniteLive full backup';" || !s.RunNow {
		t.Errorf("schedule %+v: %q", s, s.Description())
	}
	// Every step command carries the kakapo tag.
	if n := strings.Count(p.Statement, "@command = N'/* kakapo */\n"); n != 4 {
		t.Errorf("%d tagged step commands", n)
	}
}

func TestBackupScheduleOla(t *testing.T) {
	f := scheduleFacts("FULL")
	f.OlaBackup = true
	p, _ := mustSchedule(t, f, nil, true, false)
	for _, want := range []string{
		"@command = N'/* kakapo */\nEXECUTE master.dbo.DatabaseBackup @Databases = N''GraniteLive'', @Directory = N''E:\\Backup'', @BackupType = ''FULL'', @Verify = ''Y'', @CheckSum = ''Y'', @CleanupTime = 168, @Compress = ''Y'';';",
		"@BackupType = ''LOG''",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("Ola batch lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Contains(p.Statement, "@step_id = 2") || strings.Contains(p.Statement, "xp_delete_file") ||
		strings.Count(p.Statement, "@on_success_action = 1") != 2 || strings.Contains(p.Statement, "BACKUP DATABASE") {
		t.Errorf("Ola batch has a native step:\n%s", p.Statement)
	}
	if p.Changes[0].Target != `daily 22:00 to E:\Backup, verified` || p.Changes[3] != (Drift{"Compressed", "", "yes"}) {
		t.Errorf("Ola target %q", p.Changes[0].Target)
	}
	if w := p.Warnings[len(p.Warnings)-2]; w != "Uses the DatabaseBackup procedure already on this server." {
		t.Errorf("Ola warning %q", w)
	}
	// Without compression Ola is told not to compress.
	f.CompressionOK = false
	if p, _ := mustSchedule(t, f, nil, true, false); strings.Contains(p.Statement, "@Compress = ''Y''") || strings.Count(p.Statement, "@Compress = ''N''") != 2 {
		t.Errorf("Ola compresses on Web:\n%s", p.Statement)
	}
}

func TestBackupScheduleCompressionOff(t *testing.T) {
	f := scheduleFacts("FULL")
	f.CompressionOK, f.Edition = false, "Web Edition (64-bit)"
	p, _ := mustSchedule(t, f, nil, true, false)
	if strings.Contains(p.Statement, "COMPRESSION") || !strings.Contains(p.Statement, "WITH CHECKSUM, INIT;") {
		t.Errorf("batch compresses:\n%s", p.Statement)
	}
	if p.Changes[0].Target != `daily 22:00 to E:\Backup\GraniteLive, verified` {
		t.Errorf("target %q", p.Changes[0].Target)
	}
	if p.Warnings[0] != "Web edition cannot compress backups: the files will be full size." || p.Changes[3].Target != "no (this edition cannot compress)" {
		t.Errorf("warnings %q", p.Warnings)
	}
}

func TestBackupScheduleSimpleOneJob(t *testing.T) {
	f := scheduleFacts("SIMPLE")
	p, fix := mustSchedule(t, f, nil, true, false)
	if strings.Contains(p.Statement, "log backup") || strings.Count(p.Statement, "sp_add_job ") != 1 ||
		p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: GraniteLive full backup';" {
		t.Errorf("SIMPLE batch:\n%s\n%s", p.Statement, p.Rollback)
	}
	if p.Changes[1] != (Drift{"Log backup job", "none", "not needed (SIMPLE recovery; DBC08 decides)"}) {
		t.Errorf("log row %+v", p.Changes[1])
	}
	if fix.Current != "no backup job" || fix.Target != "kakapo: GraniteLive full backup (daily 22:00)" {
		t.Errorf("fix %+v", fix)
	}
	if d := NewBackupSchedule(f, nil).Description(); d != `Created the job kakapo: GraniteLive full backup (daily 22:00) writing to E:\Backup\GraniteLive, 7 days kept` {
		t.Errorf("description %q", d)
	}
}

func TestBackupScheduleOperator(t *testing.T) {
	f := scheduleFacts("FULL")
	f.Operator = "DBA's team"
	p, _ := mustSchedule(t, f, nil, true, false)
	if n := strings.Count(p.Statement, "@owner_login_name = @owner,\n        @notify_level_email = 2, @notify_email_operator_name = N'DBA''s team';"); n != 2 {
		t.Errorf("%d jobs notify the operator:\n%s", n, p.Statement)
	}
	if p.Changes[4] != (Drift{"Tell on failure", "", "DBA's team"}) {
		t.Errorf("tell row %+v", p.Changes[4])
	}
	f.Operator = ""
	p, _ = mustSchedule(t, f, nil, true, false)
	if strings.Contains(p.Statement, "notify") || p.Changes[4].Target != noOperator {
		t.Errorf("no operator: %+v", p.Changes[4])
	}
}

func TestBackupScheduleFolder(t *testing.T) {
	f := scheduleFacts("FULL")
	// A folder that does not exist: the warning, under the field too, and nothing to apply.
	p, _ := mustSchedule(t, f, map[string]string{OptionFolder: `X:\nowhere`, OptionRunNow: "false"}, false, false)
	want := `The folder X:\nowhere does not exist on SQL01\WMS (as the SQL Server service sees it): create it, or type another.`
	if len(p.Changes) != 0 || p.Statement != "" || p.Rollback != "" || p.Warnings[0] != want || p.Inputs[0].Warning != want ||
		p.Inputs[0].Value != `X:\nowhere` || p.Options[OptionFolder] != `X:\nowhere` || p.Options[OptionRunNow] != "false" {
		t.Errorf("missing folder: %+v", p)
	}
	// On the data drive: a warning, still applicable.
	p, _ = mustSchedule(t, f, map[string]string{OptionFolder: `d:\SQLBackup`}, true, SameVolume(`d:\SQLBackup`, f.DataFiles))
	want = "This folder is on the same drive as the data files (D:): a disk failure loses the database and its backups together. Prefer another drive or a share."
	if len(p.Changes) == 0 || p.Warnings[0] != want || p.Inputs[0].Warning != want {
		t.Errorf("same drive: %q", p.Warnings)
	}
	// No folder at all, and one that is not a full path: not checked, nothing to apply.
	f.DefaultBackupDir = ""
	p, _ = mustSchedule(t, f, nil, false, false)
	if len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "Type the folder the backups go to") {
		t.Errorf("no folder: %q", p.Warnings)
	}
	p, _ = mustSchedule(t, f, map[string]string{OptionFolder: `Backup`}, true, false)
	if len(p.Changes) != 0 || !strings.Contains(p.Warnings[0], "is not a full Windows path") {
		t.Errorf("relative folder: %q", p.Warnings)
	}
	// runNow off: the first full backup is tonight, and the log job fails until then.
	p, _ = mustSchedule(t, f, map[string]string{OptionFolder: `\\nas01\sql\`, OptionRunNow: "false"}, true, false)
	if p.Changes[5].Target != "tonight at 22:00" || inputOf(t, p, OptionRunNow).Value != "false" || p.Options[OptionFolder] != `\\nas01\sql` ||
		!strings.HasPrefix(p.Warnings[0], "Until the first full backup tonight") {
		t.Errorf("run later: %+v %q", p.Changes, p.Warnings)
	}
	if NewBackupSchedule(f, map[string]string{OptionRunNow: "false"}).RunNow {
		t.Error("runNow false still runs")
	}

	for _, c := range []struct {
		folder string
		valid  bool
	}{
		{`E:\Backup`, true}, {`E:\`, true}, {`\\nas01\sql`, true}, {`\\nas01\sql\db's`, true},
		{"", false}, {`E:`, false}, {`Backup`, false}, {`\\nas01`, false}, {`E:\a:b`, false}, {`E:\a*`, false},
		{"E:\\a\nb", false}, {`E:\` + strings.Repeat("a", 200), false},
	} {
		if ValidBackupFolder(c.folder) != c.valid {
			t.Errorf("ValidBackupFolder(%q) = %v", c.folder, !c.valid)
		}
	}
	if normalizeFolder(` E:\Backup\ `) != `E:\Backup` || normalizeFolder(`E:\`) != `E:\` || normalizeFolder(`e:`) != `e:\` {
		t.Error("normalizeFolder")
	}
	if !SameVolume(`\\NAS01\b`, []string{`\\nas01\data\x.mdf`}) || SameVolume(`E:\b`, []string{`D:\x.mdf`}) || SameVolume(`E:\b`, nil) {
		t.Error("SameVolume")
	}
}

// The warnings in the contract's order: folder, Agent stopped, maintenance plan, Web edition,
// service account, Ola, permission.
func TestBackupScheduleWarningOrder(t *testing.T) {
	f := scheduleFacts("FULL")
	f.AgentRunning, f.AgentStatus = false, "Stopped"
	f.MaintenancePlans, f.OlaBackup, f.CompressionOK, f.Edition = 2, true, false, "Web Edition (64-bit)"
	p, _ := mustSchedule(t, f, map[string]string{OptionFolder: `D:\Backup`}, true, true)
	want := []string{
		"This folder is on the same drive as the data files (D:): a disk failure loses the database and its backups together. Prefer another drive or a share.",
		"SQL Server Agent is stopped (Stopped): the jobs will not run until it starts (MNT01).",
		"A maintenance plan exists on this server; check it does not already back up GraniteLive before creating a second schedule.",
		"Web edition cannot compress backups: the files will be full size.",
		"The backup job runs as the SQL Server service account; the folder must let it write.",
		"Uses the DatabaseBackup procedure already on this server.",
		`Needs sysadmin on SQL01\WMS (creating Agent jobs owned by sa).`,
	}
	if !reflect.DeepEqual(p.Warnings, want) {
		t.Errorf("warnings %q", p.Warnings)
	}
	// An Agent whose state could not be read is not called stopped.
	f.AgentError = "the login does not have VIEW SERVER STATE."
	if p, _ := mustSchedule(t, f, nil, true, false); strings.Contains(strings.Join(p.Warnings, " "), "Agent is stopped") {
		t.Errorf("unknown Agent: %q", p.Warnings)
	}
}

func TestBackupScheduleExpress(t *testing.T) {
	f := scheduleFacts("FULL")
	f.ServerName, f.Edition, f.HasAgent, f.AgentRunning, f.AgentStatus, f.CompressionOK = `SQL01\EXPRESS`, "Express Edition (64-bit)", false, false, "", false
	_, fs, _ := build(f)
	for _, id := range []string{"BAK01", "BAK02"} {
		x := findingOf(t, fs, id)
		if x.FixID != FixBackupSchedule || !strings.HasSuffix(x.DoThis, " SQL Server Express has no Agent: kakapo cannot schedule the backup here; see the script in the preview.") {
			t.Errorf("%s on Express: %+v", id, x)
		}
	}
	p, _ := mustSchedule(t, f, nil, true, false)
	if !p.Express || len(p.Inputs) != 1 || p.Inputs[0].Name != OptionFolder || p.Rollback != "" || len(p.Changes) != 6 {
		t.Errorf("Express preview %+v", p)
	}
	for _, want := range []string{
		`-- Save the FULL part as backup-GraniteLive.sql and run it daily at 22:00: sqlcmd -S SQL01\EXPRESS -E -i backup-GraniteLive.sql`,
		`-- Save the LOG part as backup-GraniteLive-log.sql and run it every 15 minutes: sqlcmd -S SQL01\EXPRESS -E -i backup-GraniteLive-log.sql`,
		"BACKUP DATABASE [GraniteLive] TO DISK = @file WITH CHECKSUM, INIT;", "BACKUP LOG [GraniteLive]", "\nGO",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("Express script lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Contains(p.Statement, "sp_add_job") || strings.Contains(p.Statement, "COMPRESSION") {
		t.Errorf("Express script creates jobs or compresses:\n%s", p.Statement)
	}
	if p.Changes[4].Target != "no one (Express has no Agent to send an email)" || p.Changes[5].Target != "when the scheduled task first runs" {
		t.Errorf("Express changes %+v", p.Changes)
	}
	if !strings.HasPrefix(p.Warnings[0], "SQL Server Express has no Agent: kakapo cannot create the schedule.") ||
		p.Warnings[1] != "Express edition cannot compress backups: the files will be full size." {
		t.Errorf("Express warnings %q", p.Warnings)
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"express":true`) || !strings.Contains(string(b), `"inputs":[{"name":"folder"`) {
		t.Errorf("Express JSON %s", b)
	}
}

// The four job-coverage cases, and the Do this when a job should be doing it.
func TestAvailableBackupFixesCoverage(t *testing.T) {
	run := func(jobs ...BackupJob) (BackupFacts, []Finding, []Fix) {
		f := scheduleFacts("FULL")
		f.BackupJobs = jobs
		c, fs, _ := build(f)
		fixes := AvailableBackupFixes(f, fs)
		fixable := 0
		for _, x := range fs {
			if x.FixID != "" {
				fixable++
			}
		}
		if c.Fixable != fixable {
			t.Errorf("fixable %d, findings with a fix %d", c.Fixable, fixable)
		}
		return f, fs, fixes
	}

	// 1. No job: the fix on BAK01 and BAK02, both jobs.
	f, fs, fixes := run()
	if len(fixes) != 1 || fixes[0].ID != FixBackupSchedule || fixes[0].CheckID != "BAK01" ||
		findingOf(t, fs, "BAK01").FixID == "" || findingOf(t, fs, "BAK02").FixID == "" {
		t.Errorf("no job: %+v", fixes)
	}
	if s := NewBackupSchedule(f, nil); !s.Full || !s.Log {
		t.Errorf("no job schedule %+v", s)
	}

	// 2. A full job: BAK01 points at it, BAK02 still offers the fix, for the log job only.
	f, fs, fixes = run(BackupJob{Name: "Nightly full", Enabled: true, Kind: JobFull})
	b1 := findingOf(t, fs, "BAK01")
	if b1.FixID != "" || b1.DoThis != "The job Nightly full should be doing this; check why it has not produced a backup (MNT02)." {
		t.Errorf("BAK01 with a full job: %+v", b1)
	}
	if findingOf(t, fs, "BAK02").FixID != FixBackupSchedule || len(fixes) != 1 || fixes[0].CheckID != "BAK02" ||
		fixes[0].Current != "no log backup job" || fixes[0].Target != "kakapo: GraniteLive log backup (every 15 minutes)" {
		t.Errorf("BAK02 with a full job: %+v", fixes)
	}
	p, _ := mustSchedule(t, f, nil, true, false)
	if strings.Contains(p.Statement, "full backup") || p.Changes[0] != (Drift{"Full backup job", "Nightly full", "kept as it is"}) ||
		len(p.Changes) != 5 || len(p.Inputs) != 4 || inputOf(t, p, OptionLogMinutes).Value != "15" || strings.Contains(p.Statement, "sp_start_job") {
		t.Errorf("log job only: %+v\n%s", p.Changes, p.Statement)
	}

	// 3. A job taking both: nothing offered, both point at it.
	_, fs, fixes = run(BackupJob{Name: "DatabaseBackup - USER_DATABASES", Enabled: true, Kind: JobBoth})
	if len(fixes) != 0 || findingOf(t, fs, "BAK01").FixID != "" || findingOf(t, fs, "BAK02").FixID != "" ||
		!strings.HasPrefix(findingOf(t, fs, "BAK02").DoThis, "The job DatabaseBackup - USER_DATABASES should be doing this") {
		t.Errorf("both: %+v", fs)
	}

	// 4. A log job: BAK02 points at it, BAK01 offers the full job only.
	f, fs, fixes = run(BackupJob{Name: "Log every 15", Enabled: true, Kind: JobLog})
	if findingOf(t, fs, "BAK02").FixID != "" || findingOf(t, fs, "BAK01").FixID != FixBackupSchedule || len(fixes) != 1 ||
		fixes[0].Current != "no full backup job" {
		t.Errorf("log job: %+v", fixes)
	}
	if s := NewBackupSchedule(f, nil); !s.Full || s.Log {
		t.Errorf("log job schedule %+v", s)
	}

	// A disabled job covers nothing.
	_, fs, _ = run(BackupJob{Name: "Old full", Enabled: false, Kind: JobBoth})
	if findingOf(t, fs, "BAK01").FixID != FixBackupSchedule || findingOf(t, fs, "BAK01").DoThis != bak01Do {
		t.Errorf("disabled job: %+v", fs)
	}
}

// No fix without the jobs read, with an error reading them, or on a secondary replica.
func TestBackupScheduleNotOffered(t *testing.T) {
	for name, change := range map[string]func(*BackupFacts){
		"jobs not read": func(f *BackupFacts) { f.JobsRead = false },
		"jobs error":    func(f *BackupFacts) { f.JobsError = "SELECT permission denied" },
		"secondary":     func(f *BackupFacts) { f.IsSecondary = true },
		"msdb error":    func(f *BackupFacts) { f.MsdbError = "denied" },
		"covered":       func(f *BackupFacts) { f.BackupJobs = []BackupJob{{Name: "Ola", Enabled: true, Kind: JobBoth}} },
	} {
		f := scheduleFacts("FULL")
		change(&f)
		c, fs, _ := build(f)
		for _, x := range fs {
			if x.FixID != "" {
				t.Errorf("%s: %s offers %s", name, x.CheckID, x.FixID)
			}
		}
		if c.Fixable != 0 || OffersBackupSchedule(f) || len(AvailableBackupFixes(f, fs)) != 0 {
			t.Errorf("%s: fixable %d", name, c.Fixable)
		}
		if _, _, err := BuildBackupScheduleFix(f, "", nil, true, false); !errors.Is(err, ErrFixNotApplicable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The card counts the findings with a fix.
	c, _, _ := build(scheduleFacts("FULL"))
	if c.Fixable != 2 {
		t.Errorf("fixable %d", c.Fixable)
	}
}

func TestBackupScheduleReadBack(t *testing.T) {
	s := NewBackupSchedule(scheduleFacts("FULL"), nil)
	next := time.Date(2026, 11, 3, 22, 0, 0, 0, time.UTC)
	states := []BackupJobState{
		{Name: "kakapo: GraniteLive full backup", Enabled: true, ScheduleEnabled: true, NextRun: &next},
		{Name: "kakapo: GraniteLive log backup", Enabled: true, ScheduleEnabled: true},
	}
	after, ok := s.ReadBack(states, serverNow, true, "")
	if !ok || after != "kakapo: GraniteLive full backup: enabled, next run 2026-11-03 22:00; kakapo: GraniteLive log backup: enabled, next run 2026-11-03 09:30 (expected; Agent has not set it yet); full backup started: it runs under SQL Server Agent, and the Backups card shows it when it finishes." {
		t.Errorf("read back %v %q", ok, after)
	}
	after, ok = s.ReadBack(states[:1], serverNow, false, "Agent is not running")
	if ok || !strings.Contains(after, "kakapo: GraniteLive log backup: not found") || !strings.HasSuffix(after, "the full backup could not be started: Agent is not running") {
		t.Errorf("missing job %v %q", ok, after)
	}
	if got := s.expectedNextRun(JobFull, time.Date(2026, 11, 3, 23, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 11, 4, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("after 22:00 the next full is tomorrow: %v", got)
	}
}

// The remove fix deletes kakapo's jobs; its rollback is the creation batch rebuilt from the jobs.
func TestRemoveBackupSchedule(t *testing.T) {
	for _, ola := range []bool{false, true} {
		f := scheduleFacts("FULL")
		f.OlaBackup, f.Operator = ola, "DBA"
		f.DefaultBackupDir = `\\nas01\sql\O'Brien`
		created := NewBackupSchedule(f, nil)
		f.KakapoJobs = created.JobNames()
		f.KakapoCommand = created.backupCommand(JobFull) + "\n" + created.cleanupCommand(JobFull)
		// Once the jobs exist, the findings point at them and only the remove fix is offered.
		f.BackupJobs = []BackupJob{{Name: f.KakapoJobs[0], Enabled: true, Kind: JobFull}, {Name: f.KakapoJobs[1], Enabled: true, Kind: JobLog}}
		_, fs, _ := build(f)
		fixes := AvailableBackupFixes(f, fs)
		if len(fixes) != 1 || fixes[0].ID != FixRemoveBackupSchedule || fixes[0].Current != "kakapo: GraniteLive full backup, kakapo: GraniteLive log backup" {
			t.Fatalf("ola %v: fixes %+v", ola, fixes)
		}
		p, fix, err := BuildRemoveBackupScheduleFix(f)
		if err != nil || fix.Target != "deleted" || fix.CheckID != "BAK01" {
			t.Fatalf("remove: %v %+v", err, fix)
		}
		if p.Rollback != created.Batch() {
			t.Errorf("ola %v: rollback is not the creation batch:\n%s\n---\n%s", ola, p.Rollback, created.Batch())
		}
		if p.Statement != created.Rollback() {
			t.Errorf("statement %q", p.Statement)
		}
		if !reflect.DeepEqual(p.Changes, []Drift{{"Full backup job", "daily 22:00, keep 7 days", "deleted"}, {"Log backup job", "every 15 minutes, keep 7 days", "deleted"}}) {
			t.Errorf("changes %+v", p.Changes)
		}
		if p.Warnings[0] != "The backup files in "+created.Dir()+" are not deleted." || p.Warnings[1] != `Needs sysadmin on SQL01\WMS (deleting Agent jobs owned by sa).` {
			t.Errorf("warnings %q", p.Warnings)
		}
	}

	// Only the log job left; a step changed by hand: no rollback, said so.
	f := scheduleFacts("FULL")
	f.KakapoJobs, f.KakapoCommand = []string{"kakapo: GraniteLive log backup"}, "BACKUP LOG [GraniteLive] TO DISK = N'X:\\l.trn';"
	p, _, err := BuildRemoveBackupScheduleFix(f)
	if err != nil || p.Rollback != "" || len(p.Changes) != 1 || p.Changes[0].Setting != "Log backup job" || !strings.Contains(p.Warnings[1], "no rollback") {
		t.Errorf("hand-changed: %v %+v", err, p)
	}
	if RemoveDescription([]string{"a", "b"}) != "Deleted the jobs a and b" || RemoveDescription([]string{"a"}) != "Deleted the job a" {
		t.Error("RemoveDescription")
	}
	if after, ok := RemoveReadBack(nil); !ok || after != "no kakapo backup job" {
		t.Errorf("gone: %q", after)
	}
	if after, ok := RemoveReadBack([]BackupJobState{{Name: "x"}}); ok || after != "still there: x" {
		t.Errorf("left: %q", after)
	}
	// No kakapo job: nothing to remove.
	if _, _, err := BuildRemoveBackupScheduleFix(scheduleFacts("FULL")); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("nothing to remove: %v", err)
	}
}

// A database name with a quote and characters a file name cannot hold.
func TestBackupScheduleOddName(t *testing.T) {
	f := scheduleFacts("SIMPLE")
	f.Database = "Shop's: A/B"
	p, _ := mustSchedule(t, f, nil, true, false)
	for _, want := range []string{
		"@job_name = N'kakapo: Shop''s: A/B full backup'",
		"BACKUP DATABASE [Shop''s: A/B] TO DISK",
		`DECLARE @dir nvarchar(400) = N''E:\Backup\Shop''''s_ A_B'';`,
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("odd name lacks %q:\n%s", want, p.Statement)
		}
	}
	if folder, ok := folderFromCommand(NewBackupSchedule(f, nil).backupCommand(JobFull), f.Database, false); !ok || folder != `E:\Backup` {
		t.Errorf("folder back %q %v", folder, ok)
	}
}

// A schedule that stopped: an old full backup, SIMPLE recovery, nothing in 7 days. BAK01 and
// BAK02 do not fire, so BAK05 carries the fix, for the full job only; a job that covers it points
// at the job instead.
func TestBackupScheduleOnStaleBAK05(t *testing.T) {
	f := scheduleFacts("SIMPLE")
	old := serverNow.AddDate(0, -2, 0)
	f.LastFull, f.LastRealFull = &old, &old
	f.Fulls7, f.Diffs7, f.Logs7 = 0, 0, 0
	_, fs, _ := build(f)
	wantNoFinding(t, fs, "BAK01")
	b5 := findingOf(t, fs, "BAK05")
	if b5.FixID != FixBackupSchedule || !strings.HasPrefix(b5.DoThis, bak05DoApply) || !strings.HasSuffix(b5.DoThis, bak05DoSimple) {
		t.Errorf("BAK05 fix %q do this %q", b5.FixID, b5.DoThis)
	}
	// Since phase 4d DBC08 (SIMPLE) also offers the recovery model, after the schedule.
	fixes := AvailableBackupFixes(f, fs)
	if len(fixes) != 2 || fixes[0].CheckID != "BAK05" || fixes[1].ID != FixRecoveryModel || fixes[1].CheckID != "DBC08" {
		t.Errorf("fixes %+v", fixes)
	}
	if s := NewBackupSchedule(f, nil); !s.Full || s.Log {
		t.Errorf("schedule %+v", s)
	}
	if _, _, err := BuildBackupScheduleFix(f, "", nil, true, false); err != nil {
		t.Errorf("build: %v", err)
	}

	// A gap under 7 days (a backup 3 days ago) is BAK05 without the fix.
	recent := serverNow.AddDate(0, 0, -3)
	f.LastFull, f.Fulls7, f.RestorePoints = &recent, 1, []time.Time{recent}
	_, fs, _ = build(f)
	if b5 := findingOf(t, fs, "BAK05"); b5.FixID != "" {
		t.Errorf("BAK05 with a backup 3 days ago offers %q", b5.FixID)
	}

	// A covering job: no fix, the job named.
	f.LastFull, f.Fulls7, f.RestorePoints = &old, 0, nil
	f.BackupJobs = []BackupJob{{Name: "Nightly full", Enabled: true, Kind: JobFull}}
	_, fs, _ = build(f)
	if b5 := findingOf(t, fs, "BAK05"); b5.FixID != "" || !strings.Contains(b5.DoThis, "Nightly full") {
		t.Errorf("BAK05 with a job: fix %q do this %q", b5.FixID, b5.DoThis)
	}
}

// A database backed up well by something that is not an Agent job on this server (a VM tool, a
// job elsewhere): no finding carries the fix, but the card still offers it as a standing row.
func TestBackupScheduleOfferedWithoutFinding(t *testing.T) {
	f := scheduleFacts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	c, fs, _ := build(f)
	for _, x := range fs {
		if x.FixID != "" {
			t.Errorf("%s offers %s", x.CheckID, x.FixID)
		}
	}
	if c.Fixable != 0 || !OffersBackupSchedule(f) {
		t.Errorf("fixable %d offered %v", c.Fixable, OffersBackupSchedule(f))
	}
	fixes := AvailableBackupFixes(f, fs)
	if len(fixes) != 1 || fixes[0].ID != FixBackupSchedule || fixes[0].CheckID != "BAK01" {
		t.Errorf("fixes %+v", fixes)
	}
	if _, _, err := BuildBackupScheduleFix(f, "", nil, true, false); err != nil {
		t.Errorf("build: %v", err)
	}
}

func inputOf(t *testing.T, p FixPreview, name string) FixInput {
	t.Helper()
	for _, in := range p.Inputs {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no input %s in %+v", name, p.Inputs)
	return FixInput{}
}

func inputNames(p FixPreview) []string {
	out := []string{}
	for _, in := range p.Inputs {
		out = append(out, in.Name)
	}
	return out
}

// The inputs in the contract's order with their defaults (docs/phase4b-contract.md); logMinutes
// only when the log job would be created, compress only when the edition can compress.
func TestBackupScheduleInputs(t *testing.T) {
	p, _ := mustSchedule(t, scheduleFacts("FULL"), nil, true, false)
	want := []FixInput{
		{Name: OptionFolder, Label: "Backup folder", Value: `E:\Backup`, Help: folderHelp, Kind: "text"},
		{Name: OptionFullTime, Label: "Full backup time", Value: "22:00", Help: fullTimeHelp, Kind: "text"},
		{Name: OptionLogMinutes, Label: "Log backup every (minutes)", Value: "15", Help: logMinutesHelp, Kind: "select",
			Options: []string{"5", "10", "15", "30", "60"}},
		{Name: OptionKeepDays, Label: "Keep backups (days)", Value: "7", Help: keepDaysHelp, Kind: "text"},
		{Name: OptionCompress, Label: "Compress the backups", Value: "true", Help: compressHelp, Kind: "bool"},
		{Name: OptionRunNow, Label: "Take the first full backup now", Value: "true", Help: runNowHelp, Kind: "bool"},
	}
	if !reflect.DeepEqual(p.Inputs, want) {
		t.Errorf("inputs\n%+v\nwant\n%+v", p.Inputs, want)
	}
	b, _ := json.Marshal(p.Inputs[2])
	if !strings.Contains(string(b), `"kind":"select","options":["5","10","15","30","60"]`) {
		t.Errorf("select JSON %s", b)
	}
	if b, _ := json.Marshal(p.Inputs[1]); strings.Contains(string(b), "options") {
		t.Errorf("text input JSON carries options: %s", b)
	}

	p, _ = mustSchedule(t, scheduleFacts("SIMPLE"), nil, true, false)
	if got := inputNames(p); !reflect.DeepEqual(got, []string{OptionFolder, OptionFullTime, OptionKeepDays, OptionCompress, OptionRunNow}) {
		t.Errorf("SIMPLE inputs %q", got)
	}
	if _, ok := p.Options[OptionLogMinutes]; ok {
		t.Errorf("SIMPLE options %v", p.Options)
	}

	f := scheduleFacts("FULL")
	f.CompressionOK, f.Edition = false, "Web Edition (64-bit)"
	p, _ = mustSchedule(t, f, map[string]string{OptionCompress: "true"}, true, false)
	if got := inputNames(p); !reflect.DeepEqual(got, []string{OptionFolder, OptionFullTime, OptionLogMinutes, OptionKeepDays, OptionRunNow}) {
		t.Errorf("Web inputs %q", got)
	}
	if strings.Contains(p.Statement, "COMPRESSION") {
		t.Errorf("Web compresses when asked:\n%s", p.Statement)
	}
}

// The settings change the batch: the full backup time, the log interval, the days kept, compression
// (docs/phase4b-contract.md).
func TestBackupScheduleSettings(t *testing.T) {
	f := scheduleFacts("FULL")
	options := map[string]string{OptionFullTime: "1:30", OptionLogMinutes: "5", OptionKeepDays: " 14 ", OptionCompress: "false", OptionRunNow: "false"}
	p, fix := mustSchedule(t, f, options, true, false)
	for _, want := range []string{
		"@name = N'kakapo: GraniteLive full backup, daily 01:30', @enabled = 1,\n        @freq_type = 4, @freq_interval = 1, @active_start_time = 13000;",
		"@name = N'kakapo: GraniteLive log backup, every 5 minutes', @enabled = 1,\n        @freq_type = 4, @freq_interval = 1, @freq_subday_type = 4, @freq_subday_interval = 5;",
		"@description = N'Created by kakapo on 2026-11-03 for GraniteLive: daily 01:30, log every 5 min, keep 14 days',",
		"WITH CHECKSUM, INIT;\n",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("batch lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Count(p.Statement, "DATEADD(DAY, -14, GETDATE())") != 2 || strings.Count(p.Statement, "@step_name = N'Delete backups older than 14 days'") != 2 ||
		strings.Contains(p.Statement, "COMPRESSION") || strings.Contains(p.Statement, "220000") || strings.Contains(p.Statement, "= 15;") {
		t.Errorf("batch:\n%s", p.Statement)
	}
	wantChanges := []Drift{
		{"Full backup job", "none", `daily 01:30 to E:\Backup\GraniteLive, verified`},
		{"Log backup job", "none", `every 5 minutes to E:\Backup\GraniteLive`},
		{"Keep", "", "14 days of files"},
		{"Compressed", "", "no"},
		{"Tell on failure", "", noOperator},
		{"First full backup", "", "tomorrow at 01:30"},
	}
	if !reflect.DeepEqual(p.Changes, wantChanges) {
		t.Errorf("changes %q", p.Changes)
	}
	if !reflect.DeepEqual(p.Options, map[string]string{OptionFolder: `E:\Backup`, OptionFullTime: "01:30", OptionLogMinutes: "5",
		OptionKeepDays: "14", OptionCompress: "false", OptionRunNow: "false"}) {
		t.Errorf("options %v", p.Options)
	}
	if inputOf(t, p, OptionFullTime).Value != "01:30" || inputOf(t, p, OptionCompress).Value != "false" {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if !strings.HasPrefix(p.Warnings[0], "Until the first full backup tomorrow at 01:30, the log backup job fails every 5 minutes") {
		t.Errorf("warnings %q", p.Warnings)
	}
	if fix.Target != "kakapo: GraniteLive full backup (daily 01:30) and kakapo: GraniteLive log backup (every 5 minutes)" {
		t.Errorf("fix target %q", fix.Target)
	}
	s := NewBackupSchedule(f, options)
	if d := s.Description(); d != `Created the jobs kakapo: GraniteLive full backup (daily 01:30) and kakapo: GraniteLive log backup (every 5 minutes) writing to E:\Backup\GraniteLive, 14 days kept` {
		t.Errorf("description %q", d)
	}
	if got := s.expectedNextRun(JobLog, serverNow); !got.Equal(time.Date(2026, 11, 3, 9, 25, 0, 0, time.UTC)) {
		t.Errorf("next log run %v", got)
	}
	if got := s.expectedNextRun(JobFull, serverNow); !got.Equal(time.Date(2026, 11, 4, 1, 30, 0, 0, time.UTC)) {
		t.Errorf("next full run %v", got)
	}

	// Ola: the days as hours, compression said either way.
	f.OlaBackup = true
	p, _ = mustSchedule(t, f, options, true, false)
	if strings.Count(p.Statement, "@CleanupTime = 336, @Compress = ''N'';") != 2 || strings.Contains(p.Statement, "xp_delete_file") {
		t.Errorf("Ola batch:\n%s", p.Statement)
	}
	// An afternoon time is today.
	f.OlaBackup = false
	if p, _ := mustSchedule(t, f, map[string]string{OptionFullTime: "14:00", OptionRunNow: "false"}, true, false); p.Changes[5].Target != "today at 14:00" {
		t.Errorf("first full %q", p.Changes[5].Target)
	}
}

// A value that is not valid gives its warning, under the field too, the value as typed, and no
// changes: nothing can be applied.
func TestBackupScheduleInvalidSettings(t *testing.T) {
	for _, c := range []struct {
		name, value, warning string
	}{
		{OptionFullTime, "25:00", "The full backup time must be HH:MM, for example 22:00."},
		{OptionFullTime, "10pm", "The full backup time must be HH:MM, for example 22:00."},
		{OptionFullTime, "", "The full backup time must be HH:MM, for example 22:00."},
		{OptionLogMinutes, "7", "The log backup must run every 5, 10, 15, 30 or 60 minutes."},
		{OptionKeepDays, "0", "Keep between 1 and 60 days."},
		{OptionKeepDays, "61", "Keep between 1 and 60 days."},
		{OptionKeepDays, "seven", "Keep between 1 and 60 days."},
	} {
		p, _ := mustSchedule(t, scheduleFacts("FULL"), map[string]string{c.name: c.value}, true, false)
		in := inputOf(t, p, c.name)
		if len(p.Changes) != 0 || p.Statement != "" || p.Rollback != "" || p.Warnings[0] != c.warning ||
			in.Warning != c.warning || in.Value != c.value || p.Options[c.name] != c.value {
			t.Errorf("%s %q: %+v", c.name, c.value, p)
		}
	}
	for _, c := range []struct{ in, out string }{{"00:00", "00:00"}, {"9:05", "09:05"}, {" 23:59 ", "23:59"}} {
		if got, ok := parseClock(c.in); !ok || got != c.out {
			t.Errorf("parseClock(%q) = %q %v", c.in, got, ok)
		}
	}
	for _, bad := range []string{"24:00", "12:60", "1:5", "123:00", "+1:00", "12:3a", "12"} {
		if _, ok := parseClock(bad); ok {
			t.Errorf("parseClock(%q) ok", bad)
		}
	}
	if AgentClock(13000) != "01:30" || AgentClock(220000) != "22:00" || agentStartTime("01:30") != 13000 || agentStartTime("00:00") != 0 {
		t.Error("Agent clock")
	}
}

// The remove fix reads the schedule back from the jobs (time, interval) and the days and
// compression from the steps, so its rollback rebuilds the batch with the same values.
func TestRemoveBackupScheduleSettings(t *testing.T) {
	options := map[string]string{OptionFullTime: "01:30", OptionLogMinutes: "5", OptionKeepDays: "14", OptionCompress: "false"}
	for _, ola := range []bool{false, true} {
		f := scheduleFacts("FULL")
		f.OlaBackup = ola
		created := NewBackupSchedule(f, options)
		f.KakapoJobs, f.KakapoFullTime, f.KakapoLogMinutes = created.JobNames(), AgentClock(13000), 5
		f.KakapoCommand = created.backupCommand(JobFull)
		if !ola {
			f.KakapoCommand += "\n" + created.cleanupCommand(JobFull)
		}
		p, _, err := BuildRemoveBackupScheduleFix(f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Rollback != created.Batch() {
			t.Errorf("ola %v: rollback is not the creation batch:\n%s\n---\n%s", ola, p.Rollback, created.Batch())
		}
		if !reflect.DeepEqual(p.Changes, []Drift{{"Full backup job", "daily 01:30, keep 14 days", "deleted"}, {"Log backup job", "every 5 minutes, keep 14 days", "deleted"}}) {
			t.Errorf("ola %v: changes %+v", ola, p.Changes)
		}
	}
	// A schedule not read back is the default.
	f := scheduleFacts("FULL")
	created := NewBackupSchedule(f, nil)
	f.KakapoJobs, f.KakapoCommand = created.JobNames(), created.backupCommand(JobFull)
	if s, ok := ExistingBackupSchedule(f); !ok || s.FullTime != "22:00" || s.LogMinutes != 15 || s.KeepDays != 7 || !s.Compress {
		t.Errorf("defaults %+v", s)
	}
}
