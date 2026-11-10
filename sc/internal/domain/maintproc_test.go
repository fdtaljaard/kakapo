package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Phase 5p tests (docs/phase5p-contract.md, Tests): the generated T-SQL of each task, native and Ola; the
// due and window rules on fixtures; the previews' warnings and blocks; the plan row's words from log
// rows, including "not run since".

// mnov is a server time in November 2026: the 8th is a Sunday, the 10th a Tuesday.
func mnov(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

func tp(t time.Time) *time.Time { return &t }

func maintRow() MaintSettings { return maintDefaults("WarehouseLive", `D:\Backup`) }

func TestMaintProcSQL(t *testing.T) {
	express, other := MaintProcSQL(false), MaintProcSQL(true)
	if strings.Contains(express, "{{") || strings.Contains(other, "{{") {
		t.Fatal("a placeholder was left in the procedure")
	}
	if strings.Contains(express, "COMPRESSION") || !strings.Contains(express, "@Compress = ''N''") || !strings.Contains(express, "WITH CHECKSUM, INIT;") {
		t.Error("Express's procedure compresses")
	}
	if !strings.Contains(other, "WITH CHECKSUM, INIT, COMPRESSION;") || !strings.Contains(other, "@Compress = ''Y''") {
		t.Error("the procedure for an edition that can compress does not")
	}
	for _, want := range []string{
		// the marker and the parameter
		"/* kakapo maintenance procedure, version 1.", "ALTER PROCEDURE dbo.kakapo_Maintenance\n    @Database sysname = NULL",
		// the due rules
		"DATEDIFF(DAY, '19000107', @now) % 7 + 1", "Outcome IN ('OK', 'Failed')", "AND Outcome = 'OK')", "DATEADD(HOUR, 6, @checkDue)",
		"@logMinutes * 60 - 60", "WHERE Task = 'history-cleanup' AND StartTime >= @cleanupDue AND Outcome = 'OK'",
		"IF DATEDIFF(DAY, '19000107', @statsDue) % 7 + 1 = @checkDay", "last_log_backup_lsn IS NOT NULL", "@tookFull = 1",
		// native commands
		"DBCC CHECKDB (' + QUOTENAME(@db) + N') WITH NO_INFOMSGS, ALL_ERRORMSGS'", "N', PHYSICAL_ONLY'", ">= 26214400",
		"N'.sys.sp_updatestats;'", "N'BACKUP DATABASE '", "N'BACKUP LOG '", "N'RESTORE VERIFYONLY FROM DISK = N'''", "master.dbo.xp_delete_file 0, N'''",
		"master.dbo.xp_create_subdir N'''", "EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;", "DELETE FROM master.dbo.kakapo_MaintenanceLog WHERE StartTime < @oldest;",
		"DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;",
		// Ola's
		"EXECUTE master.dbo.DatabaseIntegrityCheck @Databases = N'''", "@CheckCommands = ''CHECKDB'', @PhysicalOnly = '''",
		"@FragmentationHigh = NULL, @UpdateStatistics = ''ALL'', @OnlyModifiedStatistics = ''Y'', @StatisticsModificationLevel = 5;'",
		"@FragmentationMedium = ''INDEX_REORGANIZE'', @FragmentationHigh = ''INDEX_REBUILD_ONLINE,INDEX_REORGANIZE''", "@FragmentationLevel1 = 50, @FragmentationLevel2 = 80, @MinNumberOfPages = 1000",
		"EXECUTE master.dbo.DatabaseBackup @Databases = N'''", "@BackupType = ''FULL'', @Verify = ''Y'', @CheckSum = ''Y''", "@BackupType = ''LOG''",
		"Skipped: index work needs Ola Hallengren''s IndexOptimize",
		// the locks, the failures, the tag
		"sys.sp_getapplock @Resource = @lockRes, @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = 0",
		"N'kakapo_Maintenance ' + @lockKind", "sys.sp_releaseapplock @Resource = @lockRes, @LockOwner = 'Session'",
		"Skipped: another call holds the lock", "BEGIN CATCH", "Outcome = 'Failed', ErrorNumber = ERROR_NUMBER()", "RAISERROR(N'kakapo_Maintenance failed: %s', 16, 1, @failed);",
		"SET LOCK_TIMEOUT -1;",
	} {
		if !strings.Contains(express, want) {
			t.Errorf("the procedure lacks %q", want)
		}
	}
	// Every command the procedure runs is tagged: native and Ola for the check, statistics and both backups,
	// Ola's for index work, and the cleanup; and it runs them through one sp_executesql.
	if n := strings.Count(express, "N'/* kakapo */"); n != 10 || strings.Count(express, "EXEC sys.sp_executesql") != 1 {
		t.Errorf("%d tagged commands", n)
	}
	for _, task := range maintTasks {
		if !strings.Contains(express, "'"+task+"'") {
			t.Errorf("the procedure never names the task %s", task)
		}
	}
	// The order of the tasks.
	last := -1
	for _, task := range []string{"1, 'integrity-check'", "2, 'statistics'", "3, 'indexes'", "4, 'full-backup'", "5, 'log-backup'", "6, 'history-cleanup'"} {
		i := strings.Index(express, task)
		if i < last {
			t.Errorf("%s is out of order", task)
		}
		last = i
	}
}

func TestMaintInstallStatementBatches(t *testing.T) {
	s := maintRow()
	stmt := MaintInstallStatement(false, s)
	b := SplitBatches(stmt)
	if len(b) != 6 || b[0] != "USE master;" || !strings.HasPrefix(b[4], "/* kakapo */\nALTER PROCEDURE dbo.kakapo_Maintenance") {
		t.Fatalf("%d batches: %q", len(b), b)
	}
	for i, x := range b[1:] {
		if !strings.HasPrefix(x, "/* kakapo */") {
			t.Errorf("batch %d is not tagged: %.40s", i+2, x)
		}
	}
	for _, want := range []string{"CREATE TABLE dbo.kakapo_MaintenanceSettings", "CREATE TABLE dbo.kakapo_MaintenanceLog", "IF OBJECT_ID(N'dbo.kakapo_MaintenanceSettings', N'U') IS NULL"} {
		if !strings.Contains(b[1], want) {
			t.Errorf("tables batch lacks %q", want)
		}
	}
	if !strings.Contains(b[3], "EXEC (N'CREATE PROCEDURE dbo.kakapo_Maintenance AS RETURN 0;')") {
		t.Errorf("stub %q", b[3])
	}
	up := b[5]
	for _, want := range []string{"WHERE DatabaseName = N'WarehouseLive';", "IF @@ROWCOUNT = 0", "BackupFolder = N'D:\\Backup', KeepBackupDays = 7, FullTime = '22:00', LogMinutes = 15, CheckDay = 1",
		"VALUES (N'WarehouseLive', N'D:\\Backup', 7, '22:00', 15, 1, '01:00', '01:00', 0, '03:00', 1, '05:00', 30, 1)"} {
		if !strings.Contains(up, want) {
			t.Errorf("upsert lacks %q\n%s", want, up)
		}
	}
	quoted := maintDefaults("Ware'house", `D:\O'Brien`)
	if up := MaintSettingsUpsert(quoted); !strings.Contains(up, "N'Ware''house'") || !strings.Contains(up, `N'D:\O''Brien'`) {
		t.Errorf("quotes %s", up)
	}
	if got := SplitBatches("a\n go \nb\r\nGO\n\nGO\n"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("split %q", got)
	}
	drop := SplitBatches(MaintDropStatement())
	if len(drop) != 2 || !strings.Contains(drop[1], "DROP PROCEDURE dbo.kakapo_Maintenance") || !strings.Contains(drop[1], "DROP TABLE dbo.kakapo_MaintenanceLog") {
		t.Errorf("drop %q", drop)
	}
	if got := MaintRunNowStatement("Ware'house"); got != "EXEC master.dbo.kakapo_Maintenance @Database = N'Ware''house';" {
		t.Error(got)
	}
}

// The due and window rules, as a table of times and log rows.
func TestMaintDue(t *testing.T) {
	s := maintRow()
	idx := s
	idx.IndexWork = true
	cases := []struct {
		name      string
		s         MaintSettings
		task      string
		log       MaintTaskLog
		logReady  bool
		cleanupOK *time.Time
		now       time.Time
		due       bool
		skipped   bool
	}{
		{"check on Sunday 01:10, never ran", s, PieceIntegrity, MaintTaskLog{}, true, nil, mnov(8, 1, 10), true, false},
		{"check on Tuesday: missed, outside the window", s, PieceIntegrity, MaintTaskLog{}, true, nil, mnov(10, 2, 0), true, true},
		{"check ran Sunday", s, PieceIntegrity, MaintTaskLog{LastOK: tp(mnov(8, 1, 5))}, true, nil, mnov(10, 2, 0), false, false},
		{"check failed Sunday: not again until next Sunday", s, PieceIntegrity, MaintTaskLog{LastFailed: tp(mnov(8, 1, 5))}, true, nil, mnov(8, 1, 20), false, false},
		{"statistics Tuesday 01:15", s, PieceStatistics, MaintTaskLog{}, true, nil, mnov(10, 1, 15), true, false},
		{"statistics Tuesday 07:00: the window has closed", s, PieceStatistics, MaintTaskLog{}, true, nil, mnov(10, 7, 0), true, true},
		{"statistics Tuesday 06:59: still in the window", s, PieceStatistics, MaintTaskLog{}, true, nil, mnov(10, 6, 59), true, false},
		{"statistics ran at 01:05", s, PieceStatistics, MaintTaskLog{LastOK: tp(mnov(10, 1, 5))}, true, nil, mnov(10, 2, 0), false, false},
		{"statistics Tuesday 00:30: Monday's ran", s, PieceStatistics, MaintTaskLog{LastOK: tp(mnov(9, 1, 5))}, true, nil, mnov(10, 0, 30), false, false},
		{"statistics on Sunday: Saturday's time, not the check day's", s, PieceStatistics, MaintTaskLog{LastOK: tp(mnov(7, 1, 5))}, true, nil, mnov(8, 1, 30), false, false},
		{"index work off", s, PieceIndexes, MaintTaskLog{}, true, nil, mnov(8, 3, 10), false, false},
		{"index work on, Sunday 03:10", idx, PieceIndexes, MaintTaskLog{}, true, nil, mnov(8, 3, 10), true, false},
		{"full backup: last night's not taken, the call comes late", s, PieceFullBackup, MaintTaskLog{LastOK: tp(mnov(8, 22, 1))}, true, nil, mnov(10, 9, 0), true, false},
		{"full backup taken at 22:01", s, PieceFullBackup, MaintTaskLog{LastOK: tp(mnov(9, 22, 1))}, true, nil, mnov(10, 9, 0), false, false},
		{"full backup failed at 22:01: backups retry", s, PieceFullBackup, MaintTaskLog{LastOK: tp(mnov(8, 22, 1)), LastFailed: tp(mnov(9, 22, 1))}, true, nil, mnov(9, 22, 16), true, false},
		{"log: 14:01 then 14:15:30 (the slack)", s, PieceLogBackup, MaintTaskLog{LastOK: tp(mnov(10, 14, 1))}, true, nil, mnov(10, 14, 15).Add(30 * time.Second), true, false},
		{"log: 14:01 then 14:10", s, PieceLogBackup, MaintTaskLog{LastOK: tp(mnov(10, 14, 1))}, true, nil, mnov(10, 14, 10), false, false},
		{"log: never, chain ready", s, PieceLogBackup, MaintTaskLog{}, true, nil, mnov(10, 14, 10), true, false},
		{"log: SIMPLE or no full backup yet", s, PieceLogBackup, MaintTaskLog{}, false, nil, mnov(10, 14, 10), false, false},
		{"cleanup Sunday 05:10", s, PieceCleanup, MaintTaskLog{}, true, nil, mnov(8, 5, 10), true, false},
		{"cleanup done for another database", s, PieceCleanup, MaintTaskLog{}, true, tp(mnov(8, 5, 1)), mnov(10, 5, 10), false, false},
		{"cleanup catches up on Tuesday", s, PieceCleanup, MaintTaskLog{}, true, tp(mnov(1, 5, 1)), mnov(10, 5, 10), true, false},
	}
	for _, c := range cases {
		due, skip := MaintDue(c.s, c.task, c.log, c.logReady, c.cleanupOK, c.now)
		if due != c.due || (skip != "") != c.skipped {
			t.Errorf("%s: due %v, skip %q", c.name, due, skip)
		}
		if c.skipped && !strings.HasPrefix(skip, "Skipped: outside the window (") {
			t.Errorf("%s: skip %q", c.name, skip)
		}
	}
	if got := s.Latest(PieceStatistics, mnov(9, 0, 30)); !got.Equal(mnov(7, 1, 0)) {
		t.Errorf("statistics latest on Monday 00:30 is %v: Sunday is the check day, so Saturday's", got)
	}
	if got := s.MaintTaskWords(PieceStatistics); got != "Monday to Saturday from 01:00 (until 07:00)" {
		t.Error(got)
	}
}

func maintInstallFacts() MaintInstallFacts {
	return MaintInstallFacts{MaintProcFacts: MaintProcFacts{ServerName: `SQL1\EXPRESS`, ProductVersion: "16.0.4135.4", EngineEdition: 4, Edition: "Express Edition (64-bit)",
		ServerNow: mnov(10, 14, 0), Database: "WarehouseLive", RecoveryModel: "FULL", DefaultBackupDir: `D:\Backup\`, Settings: []MaintSettings{}},
		FolderChecked: true, FolderExists: true}
}

func TestBuildInstallMaintenanceProcFix(t *testing.T) {
	f := maintInstallFacts()
	p, fix, err := BuildInstallMaintenanceProcFix(f, nil)
	if err != nil || fix.ID != FixInstallMaintenanceProc || len(p.Changes) != 10 {
		t.Fatalf("%v %+v %d changes %+v", err, fix, len(p.Changes), p.Changes)
	}
	if p.Changes[0].Target != "created" || p.Changes[3].Setting != "Settings for WarehouseLive" || p.Changes[3].Current != "none" ||
		!strings.Contains(p.Changes[3].Target, `backups to D:\Backup, keep 7 days; full 22:00; log every 15 min; check Sunday 01:00`) {
		t.Errorf("changes %+v", p.Changes[:4])
	}
	if p.Changes[4].Setting != "Integrity check" || p.Changes[4].Target != "weekly, Sunday from 01:00 (until 07:00)" ||
		p.Changes[6].Target != "off" || p.Changes[8].Target != "at each call, 15 minutes or more after the last" {
		t.Errorf("task rows %+v", p.Changes[4:])
	}
	// The warnings, in the contract's order.
	w := p.Warnings
	if len(w) != 5 || !strings.HasPrefix(w[0], "Nothing runs until something calls the procedure.") ||
		!strings.Contains(w[0], `schtasks /Create /TN "kakapo maintenance" /SC MINUTE /MO 15 /RU SYSTEM /TR "sqlcmd -S SQL1\EXPRESS -E -b -Q \"EXEC master.dbo.kakapo_Maintenance\""`) ||
		!strings.Contains(w[0], "sysadmin, or db_owner of each database") || !strings.Contains(w[1], "FULL recovery: call the procedure every 15 minutes") ||
		!strings.Contains(w[2], "service account") || !strings.Contains(w[3], "Express cannot compress") || !strings.HasPrefix(w[4], "Needs sysadmin") {
		t.Errorf("warnings %q", w)
	}
	if strings.Contains(p.Statement, "COMPRESSION") || !strings.Contains(p.Statement, "USE master;\nGO\n") || p.Rollback != MaintDropStatement() {
		t.Errorf("statement or rollback\n%s", p.Rollback)
	}
	if len(p.Needs) != 2 || p.Needs[0].Path != `D:\Backup` {
		t.Errorf("room %+v", p.Needs)
	}
	if MaintInstallDescription("SQL1", p, f.MaintProcFacts) != "Installed kakapo's maintenance procedure master.dbo.kakapo_Maintenance on SQL1, serving WarehouseLive" {
		t.Error(MaintInstallDescription("SQL1", p, f.MaintProcFacts))
	}

	// Options: times, the day, Run now; Ola's index work only when IndexOptimize is there.
	p, _, _ = BuildInstallMaintenanceProcFix(f, map[string]string{OptionProcDay: "saturday", OptionProcFullTime: "21:30", OptionProcRunNow: "true", OptionProcIndex: "true"})
	if !strings.Contains(p.Statement, "FullTime = '21:30'") || !strings.Contains(p.Statement, "CheckDay = 7") || !strings.Contains(p.Statement, "CleanupDay = 7") ||
		!strings.Contains(p.Statement, "IndexWork = 0") || p.Changes[len(p.Changes)-1].Setting != "Run now" || p.Options[OptionProcRunNow] != "true" {
		t.Errorf("options %s", p.Statement[len(p.Statement)-500:])
	}
	ola := f
	ola.OlaIndex, ola.OlaBackup = true, true
	p, _, _ = BuildInstallMaintenanceProcFix(ola, map[string]string{OptionProcIndex: "true"})
	if !strings.Contains(p.Statement, "IndexWork = 1") || !strings.Contains(strings.Join(p.Warnings, " "), "Ola Hallengren's procedures are in master") {
		t.Error("index work with Ola")
	}
	if s, ok := MaintSettingsFor(ola.MaintProcFacts, p.Options); !ok || !s.IndexWork || s.Folder != `D:\Backup` {
		t.Errorf("settings for the options %+v", s)
	}

	// SIMPLE recovery: no log, no FULL warning, one room line.
	simple := f
	simple.RecoveryModel = "SIMPLE"
	p, _, _ = BuildInstallMaintenanceProcFix(simple, nil)
	if p.Changes[8].Target != logNotNeeded || strings.Contains(strings.Join(p.Warnings, " "), "FULL recovery") || len(p.Needs) != 1 {
		t.Errorf("simple %+v", p.Changes[8])
	}

	// Developer: compressed, no Express warning.
	dev := f
	dev.EngineEdition, dev.Edition = 3, "Developer Edition (64-bit)"
	p, _, _ = BuildInstallMaintenanceProcFix(dev, nil)
	if !strings.Contains(p.Statement, "COMPRESSION") || strings.Contains(strings.Join(p.Warnings, " "), "compress") {
		t.Error("developer compresses, without a warning")
	}

	// Blocks: a missing folder, a bad time, a procedure kakapo did not create.
	for name, c := range map[string]struct {
		f    MaintInstallFacts
		opts map[string]string
		want string
	}{
		"folder":  {MaintInstallFacts{MaintProcFacts: f.MaintProcFacts, FolderChecked: true, FolderExists: false}, nil, `The folder D:\Backup does not exist on SQL1\EXPRESS`},
		"path":    {f, map[string]string{OptionProcFolder: "Backup"}, "is not a full Windows path"},
		"time":    {f, map[string]string{OptionProcStatsTime: "25:00"}, "Statistics from: The time must be HH:MM"},
		"keep":    {f, map[string]string{OptionProcKeepBackupDays: "0"}, "Keep backups (days) must be a whole number from 1 to 60."},
		"foreign": {MaintInstallFacts{MaintProcFacts: MaintProcFacts{ServerName: "SQL1", EngineEdition: 4, Database: "WarehouseLive", ProcExists: true, DefaultBackupDir: `D:\Backup`}, FolderChecked: true, FolderExists: true}, nil, "kakapo did not create it"},
	} {
		p, _, err := BuildInstallMaintenanceProcFix(c.f, c.opts)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || !strings.Contains(p.Warnings[0], c.want) {
			t.Errorf("%s: %v %d changes, warnings %q", name, err, len(p.Changes), p.Warnings)
		}
	}

	// Installed, with this database's row: updated in place, the row kept for what is not given, the rollback puts it back.
	inst := f
	inst.ProcExists, inst.ProcOurs, inst.ProcVersion, inst.SettingsExists, inst.LogExists = true, true, 1, true, true
	row := maintRow()
	row.KeepBackupDays, row.Folder = 14, `E:\Bak`
	inst.Settings = []MaintSettings{row}
	p, _, _ = BuildInstallMaintenanceProcFix(inst, nil)
	if p.Changes[0].Target != "updated in place (version 1 to 1)" || p.Changes[1].Target != "kept as it is" || !strings.Contains(p.Statement, "KeepBackupDays = 14") ||
		!strings.Contains(p.Statement, `N'E:\Bak'`) || !strings.Contains(p.Rollback, "UPDATE dbo.kakapo_MaintenanceSettings") || strings.Contains(p.Rollback, "DROP") {
		t.Errorf("update %+v\n%s", p.Changes[:2], p.Rollback)
	}
	// Installed for another database: this one is added, the rollback takes it off again.
	other := inst
	other.Settings = []MaintSettings{maintDefaults("Payroll", `D:\Backup`)}
	p, _, _ = BuildInstallMaintenanceProcFix(other, nil)
	if !strings.Contains(p.Rollback, "DELETE FROM dbo.kakapo_MaintenanceSettings WHERE DatabaseName = N'WarehouseLive';") {
		t.Errorf("second database rollback %s", p.Rollback)
	}
}

func TestBuildRemoveMaintenanceProcFix(t *testing.T) {
	if _, _, err := BuildRemoveMaintenanceProcFix(MaintProcFacts{ServerName: "SQL1"}, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("nothing installed: %v", err)
	}
	f := maintInstallFacts().MaintProcFacts
	f.ProcExists, f.ProcOurs, f.ProcVersion, f.SettingsExists, f.LogExists = true, true, 1, true, true
	f.Settings = []MaintSettings{maintRow(), maintDefaults("Payroll", `D:\Backup`)}
	p, fix, err := BuildRemoveMaintenanceProcFix(f, nil)
	if err != nil || len(p.Changes) != 3 || p.Statement != MaintDropStatement() || fix.Target != "none" {
		t.Fatalf("%v %+v", err, p.Changes)
	}
	if b := SplitBatches(p.Rollback); len(b) != 7 || !strings.Contains(b[5], "N'WarehouseLive'") || !strings.Contains(b[6], "N'Payroll'") {
		t.Errorf("rollback recreates both rows: %d batches", len(b))
	}
	joined := strings.Join(p.Warnings, " ")
	for _, want := range []string{"backup files stay on disk", MaintSchtasksDelete, "is dropped with it", "Needs sysadmin"} {
		if !strings.Contains(joined, want) {
			t.Errorf("remove warnings lack %q", want)
		}
	}
	p, fix, _ = BuildRemoveMaintenanceProcFix(f, map[string]string{OptionProcRemove: "payroll"})
	if len(p.Changes) != 1 || p.Statement != MaintDeleteRowStatement("Payroll") || !strings.Contains(p.Rollback, "N'Payroll'") || fix.Target != "not serving Payroll" {
		t.Errorf("one database %+v %s", p.Changes, p.Statement)
	}
	if p, _, _ = BuildRemoveMaintenanceProcFix(f, map[string]string{OptionProcRemove: "Nope"}); len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "Pick everything") {
		t.Errorf("bad option %q", p.Warnings)
	}
	foreign := MaintProcFacts{ServerName: "SQL1", ProcExists: true}
	if p, _, _ = BuildRemoveMaintenanceProcFix(foreign, nil); len(p.Changes) != 0 || !strings.Contains(p.Warnings[0], "was not created by kakapo") {
		t.Errorf("foreign %q", p.Warnings)
	}
	if text, ok := MaintRemoveReadBack(MaintProcFacts{}, MaintRemoveAll); !ok || text != "nothing of kakapo's maintenance procedure left in master" {
		t.Error(text)
	}
	if text, ok := MaintRemoveReadBack(f, MaintRemoveAll); ok || !strings.Contains(text, "the procedure") {
		t.Error(text)
	}
}

func TestMaintInstallReadBack(t *testing.T) {
	f := maintInstallFacts().MaintProcFacts
	f.ProcExists, f.ProcOurs, f.ProcVersion, f.SettingsExists, f.LogExists = true, true, 1, true, true
	want := maintRow()
	f.Settings = []MaintSettings{want}
	runs := []MaintRun{{Task: PieceFullBackup, Outcome: MaintOK, File: `D:\Backup\WarehouseLive\WarehouseLive_FULL_20261110_140000.bak`},
		{Task: PieceStatistics, Outcome: MaintSkipped}}
	text, ok := MaintInstallReadBack(f, want, true, "", runs)
	if !ok || !strings.Contains(text, "procedure version 1 and both tables in master; settings for WarehouseLive as previewed; run now: full-backup ok (D:") {
		t.Errorf("%v %s", ok, text)
	}
	changed := want
	changed.FullTime = "21:00"
	if _, ok := MaintInstallReadBack(f, changed, false, "", nil); ok {
		t.Error("a row that differs is verified")
	}
}

func maintInstalled(created time.Time, rows ...MaintTaskLog) MaintProcFacts {
	s := maintRow()
	s.CreatedAt = &created
	return MaintProcFacts{ServerName: "SQL1", EngineEdition: 4, Database: "WarehouseLive", RecoveryModel: "FULL",
		ProcExists: true, ProcOurs: true, ProcVersion: 1, SettingsExists: true, LogExists: true, Settings: []MaintSettings{s}, Tasks: rows}
}

// The plan row's words from the log rows.
func TestMaintPlanPiece(t *testing.T) {
	created := mnov(10, 14, 0)
	piece := func(f MaintProcFacts, id string, now time.Time) PlanPiece {
		p := PlanPiece{ID: id}
		s, _ := f.Row("WarehouseLive")
		maintPlanPiece(&p, f, s, now)
		return p
	}
	f := maintInstalled(created)
	if p := piece(f, PieceFullBackup, mnov(10, 14, 10)); p.Status != PlanNeverRan || p.Note != "Not run yet: due at 2026-11-10 14:00." {
		t.Errorf("full, just installed: %+v", p)
	}
	if p := piece(f, PieceFullBackup, mnov(10, 15, 0)); p.Status != PlanOverdue ||
		p.Note != "Not run since it was set up on 2026-11-10 14:00: it was due at 2026-11-10 14:00. Is the scheduled task calling it?" {
		t.Errorf("full, never called: %+v", p)
	}
	if p := piece(f, PieceIntegrity, mnov(10, 14, 10)); p.Status != PlanNeverRan || p.Note != "Not run yet: the first time is 2026-11-15 01:00." {
		t.Errorf("check before its first Sunday: %+v", p)
	}
	if p := piece(f, PieceStatistics, mnov(10, 14, 10)); p.Status != PlanNeverRan || p.Note != "Not run yet: the first time is 2026-11-11 01:00." {
		t.Errorf("statistics before the first night: %+v", p)
	}
	full := MaintTaskLog{Database: "WarehouseLive", Task: PieceFullBackup, LastOK: tp(mnov(10, 14, 1)),
		Latest: MaintRun{Task: PieceFullBackup, Start: mnov(10, 14, 1), End: tp(mnov(10, 14, 3)), Outcome: MaintOK}}
	f = maintInstalled(created, full)
	if p := piece(f, PieceFullBackup, mnov(10, 16, 0)); p.Status != PlanInPlace || p.LastRun != "2026-11-10 14:01" || p.LastOutcome != "succeeded" ||
		p.LastDuration != "2 min" || p.Job != "master.dbo.kakapo_Maintenance" {
		t.Errorf("full taken: %+v", p)
	}
	if p := piece(f, PieceFullBackup, mnov(11, 23, 0)); p.Status != PlanOverdue ||
		p.Note != "Not run since 2026-11-10 14:01: it was due at 2026-11-11 22:00. Is the scheduled task calling it?" {
		t.Errorf("tonight's not taken: %+v", p)
	}
	failed := MaintTaskLog{Database: "WarehouseLive", Task: PieceLogBackup, LastOK: tp(mnov(10, 14, 16)),
		Latest: MaintRun{Task: PieceLogBackup, Start: mnov(10, 14, 31), Outcome: MaintFailed, Message: "Cannot open backup device."}}
	f = maintInstalled(created, full, failed)
	if p := piece(f, PieceLogBackup, mnov(10, 14, 40)); p.Status != PlanFailing || p.Note != "Failed: Cannot open backup device." || p.LastOutcome != "failed" {
		t.Errorf("log failed: %+v", p)
	}
	skipped := MaintTaskLog{Database: "WarehouseLive", Task: PieceStatistics,
		Latest: MaintRun{Task: PieceStatistics, Start: mnov(11, 9, 0), Outcome: MaintSkipped, Message: "Skipped: outside the window (2026-11-11 01:00 to 2026-11-11 07:00); it waits for the next one."}}
	f = maintInstalled(created, skipped)
	if p := piece(f, PieceStatistics, mnov(11, 10, 0)); p.Status != PlanOverdue || !strings.HasPrefix(p.Note, "Skipped: outside the window") {
		t.Errorf("statistics skipped: %+v", p)
	}
	simple := maintInstalled(created)
	simple.RecoveryModel = "SIMPLE"
	if p := piece(simple, PieceLogBackup, mnov(10, 16, 0)); p.Status != PlanNotNeeded {
		t.Errorf("simple: %+v", p)
	}
	if p := piece(f, PieceIndexes, mnov(10, 16, 0)); p.Status != PlanOptional {
		t.Errorf("index work off: %+v", p)
	}

	// The whole row on Express: read from the log, no fix while the procedure serves the database.
	plan := BuildMaintenancePlan(MaintenancePlanFacts{ServerName: "SQL1", EngineEdition: 4, Database: "WarehouseLive", RecoveryModel: "FULL",
		ServerNow: mnov(10, 16, 0), Proc: &f}, PlanAlerts{})
	if plan.FixID != "" || !strings.Contains(plan.Note, "kakapo's maintenance procedure (master.dbo.kakapo_Maintenance) does this database's maintenance") {
		t.Errorf("installed plan %+v", plan.Note)
	}
	if p := pieceOf(t, plan, PieceFullBackup); p.Status != PlanOverdue {
		t.Errorf("plan full %+v", p)
	}
	// Installed but not serving this database: offered again.
	other := maintInstalled(created)
	other.Settings[0].Database = "Payroll"
	plan = BuildMaintenancePlan(MaintenancePlanFacts{EngineEdition: 4, Database: "WarehouseLive", ServerNow: mnov(10, 16, 0), Proc: &other}, PlanAlerts{})
	if plan.FixID != FixInstallMaintenanceProc || !strings.Contains(pieceOf(t, plan, PieceFullBackup).Note, "does not serve this database") {
		t.Errorf("not serving %+v", plan)
	}
}

func TestMaintHelperLine(t *testing.T) {
	if _, ok := maintHelperLine(&MaintProcFacts{}, 3); ok {
		t.Error("a line on Developer with nothing installed")
	}
	h, ok := maintHelperLine(&MaintProcFacts{}, 4)
	if !ok || h.State != HelperNotInstalled || h.FixID != "" || !strings.Contains(h.Sentence, "Install it from a database's Upkeep card") {
		t.Errorf("express, not installed %+v", h)
	}
	f := maintInstalled(mnov(10, 14, 0))
	f.ServerNow = mnov(10, 14, 20)
	f.Settings[0].LastCall = tp(mnov(10, 14, 15))
	f.LastFailure = &MaintRun{Database: "WarehouseLive", Task: PieceLogBackup, Start: mnov(10, 14, 15), Outcome: MaintFailed, Message: "Cannot open backup device."}
	h, ok = maintHelperLine(&f, 3)
	if !ok || h.State != HelperInstalled || h.FixID != FixRemoveMaintenanceProc || h.FixTitle != "Remove kakapo's maintenance procedure" ||
		h.Sentence != "kakapo's maintenance procedure serves WarehouseLive. Last call 2026-11-10 14:15. Last failure: log-backup of WarehouseLive at 2026-11-10 14:15 (Cannot open backup device.)." {
		t.Errorf("installed %+v", h)
	}
	f.ServerNow = mnov(10, 18, 0)
	if h, _ = maintHelperLine(&f, 3); !strings.Contains(h.Sentence, "nothing has called it since") {
		t.Error(h.Sentence)
	}
	all := BuildInstalledHelpers(HelperFacts{ServerName: "SQL1", EngineEdition: 3, Session: XESession{ThresholdSeconds: 0}, Maintenance: &f}, "WarehouseLive")
	if len(all.Helpers) != 2 || !strings.HasPrefix(all.Sentence, "1 helper installed by kakapo: kakapo's maintenance procedure serves") {
		t.Errorf("row %q", all.Sentence)
	}
}
