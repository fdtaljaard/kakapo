package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The maintenance plan (docs/phase5b-contract.md, Tests): the status words for every case, the
// cleanup statement and blocks, the composite's choice and stitching, the apply order and
// stop-at-failure, and the default times that do not overlap.

var never = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)

// planFacts is a database with nothing scheduled: FULL recovery, no integrity check ever, Ola's
// IndexOptimize installed, no job.
func planFacts() MaintenancePlanFacts {
	return MaintenancePlanFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2, Database: "WarehouseLive",
		RecoveryModel: "FULL", LastGoodCheckDb: &never, IndexOptimize: true, Jobs: []PlanJob{}, ServerNow: setupNow}
}

func clockAt(s string) *time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return &t
}

// planJob is an enabled job with one step and one enabled schedule that last succeeded.
func planJob(name, command string, sc PlanSchedule) PlanJob {
	sc.Enabled = true
	return PlanJob{Name: name, Enabled: true, Steps: []PlanJobStep{{Command: command, Database: "master"}}, Schedules: []PlanSchedule{sc},
		LastRun: clockAt("2026-11-02 01:00"), LastStatus: PlanRunSucceeded, LastSeconds: 240, NextRun: clockAt("2026-11-04 01:00")}
}

var (
	daily2200  = PlanSchedule{FreqType: 4, FreqInterval: 1, SubdayType: 1, StartTime: 220000}
	every15    = PlanSchedule{FreqType: 4, FreqInterval: 1, SubdayType: 4, SubdayInterval: 15}
	sunday0100 = PlanSchedule{FreqType: 8, FreqInterval: JobSunday, SubdayType: 1, StartTime: 10000}
	monSat0100 = PlanSchedule{FreqType: 8, FreqInterval: JobMonToSat, SubdayType: 1, StartTime: 10000}
	sunday0500 = PlanSchedule{FreqType: 8, FreqInterval: JobSunday, SubdayType: 1, StartTime: 50000}
)

func pieceOf(t *testing.T, plan MaintenancePlan, id string) PlanPiece {
	t.Helper()
	p, ok := plan.piece(id)
	if !ok {
		t.Fatalf("no %s line in %+v", id, plan.Pieces)
	}
	return p
}

func TestPlanNothingScheduled(t *testing.T) {
	plan := BuildMaintenancePlan(planFacts(), PlanAlerts{})
	want := []struct{ id, status, fix string }{
		{PieceFullBackup, PlanMissing, FixBackupSchedule},
		{PieceLogBackup, PlanMissing, FixBackupSchedule},
		{PieceIntegrity, PlanMissing, FixCheckdbJob},
		{PieceStatistics, PlanMissing, FixMaintenanceJob},
		{PieceIndexes, PlanOptional, FixMaintenanceJob},
		{PieceCleanup, PlanMissing, FixHistoryCleanup},
		{PieceAlerts, PlanMissing, FixOperatorAlerts},
	}
	if len(plan.Pieces) != len(want) {
		t.Fatalf("pieces %+v", plan.Pieces)
	}
	for i, w := range want {
		p := plan.Pieces[i]
		if p.ID != w.id || p.Status != w.status || p.FixID != w.fix || p.Title == "" || p.Cadence == "" || !strings.Contains(p.Source, "Source") && p.ID != PieceIndexes {
			t.Errorf("%d: %+v", i, p)
		}
	}
	if plan.Missing != 6 || plan.FixID != FixMaintenancePlan || plan.Express || plan.Note != "" || !strings.Contains(plan.Order, "perform the full backup after the integrity check") {
		t.Errorf("plan %+v", plan)
	}
	// The pieces' options make them fit the plan's times.
	if s := pieceOf(t, plan, PieceStatistics); !reflect.DeepEqual(s.Options, map[string]string{OptionStatsOnly: "true", OptionMaintDay: "Sunday"}) {
		t.Errorf("statistics options %v", s.Options)
	}
	if x := pieceOf(t, plan, PieceIndexes); x.Options[OptionMaintIndexOnly] != "true" || x.Options[OptionMaintIndexTime] != "03:00" {
		t.Errorf("index options %v", x.Options)
	}
	if a := pieceOf(t, plan, PieceAlerts); a.Note != "No operator with an email address: no alert or job failure reaches anyone." {
		t.Errorf("alerts %+v", a)
	}
	// The card offers the composite from it.
	fix, ok := MaintenancePlanOffer(&plan)
	if !ok || fix.ID != FixMaintenancePlan || fix.CheckID != "MNT03" ||
		fix.Current != "6 piece(s) missing: full backup, log backup, integrity check, statistics update, history cleanup, alerts and an operator" {
		t.Errorf("offer %+v", fix)
	}
	if _, ok := MaintenancePlanOffer(nil); ok {
		t.Error("no plan, no offer")
	}
}

func TestPlanHealthy(t *testing.T) {
	f := planFacts()
	f.Jobs = []PlanJob{
		planJob("kakapo: WarehouseLive full backup", "/* kakapo */\nBACKUP DATABASE [WarehouseLive] TO DISK = @file", daily2200),
		planJob("kakapo: WarehouseLive log backup", "/* kakapo */\nBACKUP LOG [WarehouseLive] TO DISK = @file", every15),
		planJob("kakapo: WarehouseLive integrity check", "/* kakapo */\nDBCC CHECKDB (N'WarehouseLive') WITH NO_INFOMSGS", sunday0100),
		planJob("kakapo: statistics (WarehouseLive)", olaStatisticsCommand("WarehouseLive"), monSat0100),
		planJob(HistoryCleanupJobName, HistoryCleanupCommand(30), sunday0500),
		planJob("kakapo: archive WarehouseLive.dbo.MailLog", "/* kakapo-archive */ DELETE ...", PlanSchedule{FreqType: 4, FreqInterval: 1, SubdayType: 1, StartTime: 23000}),
		planJob("kakapo: Other full backup", "BACKUP DATABASE [Other] TO DISK = @file", daily2200),
	}
	g := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	f.LastGoodCheckDb = &g
	plan := BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	for _, p := range plan.Pieces {
		want := PlanInPlace
		if p.ID == PieceIndexes {
			want = PlanOptional
		}
		if p.Status != want {
			t.Errorf("%s: %+v", p.ID, p)
		}
		if p.ID != PieceIndexes && p.ID != PieceAlerts && (p.LastRun != "2026-11-02 01:00" || p.LastOutcome != "succeeded" || p.LastDuration != "4 min" || p.NextRun != "2026-11-04 01:00") {
			t.Errorf("%s runs: %+v", p.ID, p)
		}
		if p.Status == PlanInPlace && p.FixID != "" {
			t.Errorf("%s in place with a fix", p.ID)
		}
	}
	schedules := map[string]string{PieceFullBackup: "daily 22:00", PieceLogBackup: "every 15 minutes", PieceIntegrity: "Sunday 01:00",
		PieceStatistics: "Monday to Saturday 01:00", PieceCleanup: "Sunday 05:00"}
	for id, want := range schedules {
		if p := pieceOf(t, plan, id); p.Schedule != want {
			t.Errorf("%s schedule %q", id, p.Schedule)
		}
	}
	if len(plan.Pieces) != 8 || plan.Pieces[7].Title != "Archive dbo.MailLog" || plan.Pieces[7].Status != PlanInPlace || plan.Pieces[7].Schedule != "daily 02:30" {
		t.Errorf("archive line %+v", plan.Pieces)
	}
	if plan.Missing != 0 || plan.FixID != "" {
		t.Errorf("healthy plan %+v", plan)
	}
}

func TestPlanStatusWords(t *testing.T) {
	// Another enabled job covers the backups (Ola's DatabaseBackup for every user database).
	f := planFacts()
	ola := planJob("DatabaseBackup - USER_DATABASES - FULL", "EXECUTE dbo.DatabaseBackup @Databases = 'USER_DATABASES', @BackupType = 'FULL'", daily2200)
	failing := planJob("DatabaseIntegrityCheck - USER_DATABASES", "EXECUTE dbo.DatabaseIntegrityCheck @Databases = 'USER_DATABASES'", sunday0100)
	failing.LastStatus = PlanRunFailed
	disabled := planJob("Nightly stats", "EXEC sp_MSforeachdb 'USE ?; EXEC sp_updatestats'", monSat0100)
	disabled.Enabled = false
	f.Jobs = []PlanJob{ola, failing, disabled}
	plan := BuildMaintenancePlan(f, PlanAlerts{Operators: 1, Missing: []string{"severity 19"}})
	cases := map[string]string{PieceFullBackup: PlanOtherJob, PieceLogBackup: PlanMissing, PieceIntegrity: PlanFailing,
		PieceStatistics: PlanDisabled, PieceIndexes: PlanOptional, PieceCleanup: PlanMissing, PieceAlerts: PlanMissing}
	for id, want := range cases {
		if p := pieceOf(t, plan, id); p.Status != want {
			t.Errorf("%s: %s, want %s (%+v)", id, p.Status, want, p)
		}
	}
	if p := pieceOf(t, plan, PieceFullBackup); p.Job != ola.Name || p.FixID != "" {
		t.Errorf("other job %+v", p)
	}
	if p := pieceOf(t, plan, PieceIntegrity); p.Job != failing.Name || p.FixID != "" || !strings.Contains(p.Note, "MNT02") {
		t.Errorf("failing %+v", p)
	}
	// Someone else's disabled job: kakapo can create its own, and it counts as missing.
	if p := pieceOf(t, plan, PieceStatistics); p.FixID != FixMaintenanceJob || p.Note != "The only job that does this is disabled." {
		t.Errorf("disabled other %+v", p)
	}
	if a := pieceOf(t, plan, PieceAlerts); a.Note != "No alert for: severity 19." {
		t.Errorf("alerts %+v", a)
	}
	if plan.Missing != 4 || plan.FixID != FixMaintenancePlan {
		t.Errorf("missing %d", plan.Missing)
	}

	// kakapo's own job disabled, or never run; SIMPLE recovery; a recent good check.
	f = planFacts()
	f.RecoveryModel = "SIMPLE"
	g := setupNow.AddDate(0, 0, -3)
	f.LastGoodCheckDb = &g
	own := planJob(StatisticsJobName("WarehouseLive"), olaStatisticsCommand("WarehouseLive"), monSat0100)
	own.Enabled = false
	fresh := planJob(HistoryCleanupJobName, HistoryCleanupCommand(30), sunday0500)
	fresh.LastRun, fresh.LastStatus = nil, PlanNoRun
	unscheduled := planJob("kakapo: WarehouseLive full backup", "BACKUP DATABASE [WarehouseLive] TO DISK = @f", daily2200)
	unscheduled.Schedules[0].Enabled = false
	f.Jobs = []PlanJob{own, fresh, unscheduled}
	plan = BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	cases = map[string]string{PieceFullBackup: PlanDisabled, PieceLogBackup: PlanNotNeeded, PieceIntegrity: PlanMissing,
		PieceStatistics: PlanDisabled, PieceCleanup: PlanNeverRan, PieceAlerts: PlanInPlace}
	for id, want := range cases {
		if p := pieceOf(t, plan, id); p.Status != want {
			t.Errorf("%s: %s, want %s (%+v)", id, p.Status, want, p)
		}
	}
	if p := pieceOf(t, plan, PieceStatistics); p.FixID != "" || !strings.Contains(p.Note, "kakapo's job is disabled") {
		t.Errorf("own disabled %+v", p)
	}
	if p := pieceOf(t, plan, PieceFullBackup); p.Schedule != "daily 22:00 (schedule disabled)" || p.FixID != "" {
		t.Errorf("unscheduled %+v", p)
	}
	// A recent check by hand is not a schedule: the weekly job is still offered.
	if p := pieceOf(t, plan, PieceIntegrity); p.Status != PlanMissing || p.FixID != FixCheckdbJob {
		t.Errorf("recent check %+v", p)
	}
	if p := pieceOf(t, plan, PieceLogBackup); p.FixID != "" || !strings.Contains(p.Note, "SIMPLE") {
		t.Errorf("simple %+v", p)
	}
	if plan.Missing != 1 || plan.FixID != FixMaintenancePlan {
		t.Errorf("only the integrity check creatable %+v", plan)
	}

	// msdb not readable: every job line unknown, no composite.
	f = planFacts()
	f.JobsError = "The SELECT permission was denied on the object 'sysjobs'"
	plan = BuildMaintenancePlan(f, PlanAlerts{Error: "denied"})
	for _, p := range plan.Pieces {
		if p.ID != PieceLogBackup && p.Status != PlanUnknown {
			t.Errorf("unread %+v", p)
		}
	}
	if plan.FixID != "" || !strings.Contains(plan.Note, "could not be read") {
		t.Errorf("unread plan %+v", plan)
	}

	// Express (phase 5p): missing lines without scripts, no alerts, one fix: install the procedure.
	f = planFacts()
	f.EngineEdition = 4
	plan = BuildMaintenancePlan(f, PlanAlerts{})
	if plan.FixID != FixInstallMaintenanceProc || !plan.Express || pieceOf(t, plan, PieceAlerts).Status != PlanNotNeeded || pieceOf(t, plan, PieceCleanup).FixID != "" ||
		pieceOf(t, plan, PieceCleanup).Status != PlanMissing || pieceOf(t, plan, PieceIndexes).Status != PlanOptional {
		t.Errorf("express %+v", plan)
	}
	if fix, ok := MaintenancePlanOffer(&plan); !ok || fix.ID != FixInstallMaintenanceProc {
		t.Errorf("express offer %+v", fix)
	}
}

func TestPlanCoverage(t *testing.T) {
	db := "WarehouseLive"
	cases := []struct {
		piece, cmd, stepDB string
		want               bool
	}{
		{PieceFullBackup, "BACKUP DATABASE [WarehouseLive] TO DISK = 'x'", "", true},
		{PieceFullBackup, "BACKUP DATABASE WarehouseLive TO DISK = 'x'", "", true},
		{PieceFullBackup, "BACKUP DATABASE WarehouseLive2 TO DISK = 'x'", "", false},
		{PieceFullBackup, "EXECUTE dbo.DatabaseBackup @Databases = 'USER_DATABASES', @BackupType = 'FULL'", "", true},
		{PieceFullBackup, "EXECUTE dbo.DatabaseBackup @Databases = 'USER_DATABASES', @BackupType = 'LOG'", "", false},
		{PieceLogBackup, "EXECUTE dbo.DatabaseBackup @Databases = 'ALL_DATABASES', @BackupType = N'LOG'", "", true},
		{PieceLogBackup, "BACKUP LOG [Other] TO DISK = 'x'", "", false},
		{PieceIntegrity, "DBCC CHECKDB WITH NO_INFOMSGS", db, true},
		{PieceIntegrity, "EXEC sp_MSforeachdb 'DBCC CHECKDB(?)'", "master", true},
		{PieceIntegrity, "EXECUTE dbo.DatabaseIntegrityCheck @Databases = 'Other'", "master", false},
		{PieceStatistics, "EXEC sp_updatestats", db, true},
		{PieceStatistics, olaStatisticsCommand(db), "master", true},
		{PieceStatistics, "EXECUTE dbo.IndexOptimize @Databases = 'USER_DATABASES'", "master", false},
		{PieceIndexes, olaStatisticsCommand(db), "master", false},
		{PieceIndexes, olaIndexCommand(db), "master", true},
		{PieceIndexes, "EXECUTE dbo.IndexOptimize @Databases = 'USER_DATABASES'", "master", true},
		{PieceIndexes, "ALTER INDEX ALL ON dbo.T REBUILD", db, true},
		{PieceCleanup, "EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @d", "msdb", true},
		{PieceCleanup, "DELETE FROM [dbo].[CommandLog] WHERE StartTime < DATEADD(dd,-30,GETDATE())", "master", true},
		{PieceCleanup, "EXEC sp_updatestats", db, false},
	}
	for _, c := range cases {
		if got := (PlanJobStep{Command: c.cmd, Database: c.stepDB}).covers(c.piece, db); got != c.want {
			t.Errorf("%s %q: %v", c.piece, c.cmd, got)
		}
	}
}

func TestPlanScheduleWords(t *testing.T) {
	cases := map[string]PlanSchedule{
		"daily 22:00":                  daily2200,
		"every 15 minutes":             every15,
		"Sunday 01:00":                 sunday0100,
		"Monday to Saturday 01:00":     monSat0100,
		"every 2 days, 03:00":          {FreqType: 4, FreqInterval: 2, SubdayType: 1, StartTime: 30000},
		"monthly on day 1 at 03:00":    {FreqType: 16, FreqInterval: 1, StartTime: 30000},
		"when SQL Server Agent starts": {FreqType: 64},
		"Monday, Wednesday 04:30":      {FreqType: 8, FreqInterval: 2 | 8, SubdayType: 1, StartTime: 43000},
	}
	for want, s := range cases {
		if got := s.Words(); got != want {
			t.Errorf("%+v: %q, want %q", s, got, want)
		}
	}
}

// The default times do not overlap: Sunday runs the check at 01:00, index work at 03:00 and the
// cleanup at 05:00; the full backup at 22:00 follows them; statistics run at 01:00 Monday to
// Saturday.
func TestPlanDefaultTimes(t *testing.T) {
	want := []PlanSlot{
		{PieceIntegrity, JobSunday, "01:00"},
		{PieceIndexes, JobSunday, "03:00"},
		{PieceCleanup, JobSunday, "05:00"},
		{PieceStatistics, JobMonToSat, "01:00"},
		{PieceFullBackup, JobSunday | JobMonToSat, "22:00"},
	}
	slots := DefaultPlanSlots()
	if !reflect.DeepEqual(slots, want) {
		t.Fatalf("slots %+v", slots)
	}
	if o := PlanOverlaps(slots); len(o) != 0 {
		t.Errorf("default times overlap: %v", o)
	}
	// On Sunday the full backup comes after everything else.
	for _, s := range slots[:3] {
		if minutesOf(s.Clock) >= minutesOf("22:00") {
			t.Errorf("%s after the full backup", s.Piece)
		}
	}
	// Two pieces within the same hour on a common day are named.
	o := PlanOverlaps([]PlanSlot{{PieceIntegrity, JobSunday, "01:00"}, {PieceStatistics, JobSunday | 2, "01:30"}, {PieceFullBackup, JobSunday, "23:30"},
		{PieceCleanup, JobSunday, "00:15"}})
	if len(o) != 3 || !strings.HasPrefix(o[0], "integrity check (01:00) and statistics update (01:30) start within the same hour on Sunday") {
		t.Errorf("overlaps %q", o)
	}
}

// --- history-cleanup ---

func historyFacts() HistoryCleanupFacts {
	old := time.Date(2019, 3, 2, 0, 0, 0, 0, time.UTC)
	return HistoryCleanupFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2, KeepDays: 30,
		Tables: []HistoryTable{
			{Label: "Backup history", Table: "msdb.dbo.backupset", Rows: 182_000, Older: 176_500, Oldest: &old},
			{Label: "Restore history", Table: "msdb.dbo.restorehistory", Rows: 0},
			{Label: "Job history", Table: "msdb.dbo.sysjobhistory", Rows: 9_400, Older: 0, Oldest: clockAt("2026-10-20 00:00")},
		},
		Operator: "Database support", AgentStatus: "Running", ServerNow: setupNow}
}

func TestHistoryCleanupStatement(t *testing.T) {
	want := "DECLARE @oldest datetime = DATEADD(DAY, -30, GETDATE());\n" +
		"EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;\n" +
		"EXEC msdb.dbo.sp_purge_jobhistory @oldest_date = @oldest;\n" +
		"IF OBJECT_ID(N'master.dbo.CommandLog') IS NOT NULL DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;"
	if got := HistoryCleanupCommand(30); got != want {
		t.Errorf("command\n%s", got)
	}
}

func TestHistoryCleanupFix(t *testing.T) {
	f := historyFacts()
	p, fix, err := BuildHistoryCleanupFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fix.ID != FixHistoryCleanup || fix.CheckID != "MNT08" || fix.Current != "182,000 backup and 9,400 job history rows, nothing prunes them" ||
		fix.Target != "kakapo: history cleanup (Sunday 05:00, keeps 30 days)" {
		t.Errorf("fix %+v", fix)
	}
	wantChanges := []Drift{
		{Setting: "History cleanup job", Current: "none", Target: "kakapo: history cleanup, Sunday 05:00, keeps 30 days"},
		{Setting: "Backup history (msdb.dbo.backupset)", Current: "182,000 rows since 2019-03-02", Target: "176,500 rows older than 30 days deleted at the first run"},
		{Setting: "Restore history (msdb.dbo.restorehistory)", Current: "no rows", Target: "nothing older than 30 days yet"},
		{Setting: "Job history (msdb.dbo.sysjobhistory)", Current: "9,400 rows since 2026-10-20", Target: "nothing older than 30 days yet"},
		{Setting: "Tell on failure", Target: "Database support"},
	}
	if !reflect.DeepEqual(p.Changes, wantChanges) {
		t.Errorf("changes\n got %+v\nwant %+v", p.Changes, wantChanges)
	}
	for _, w := range []string{historyFiles, historyFloor, historyNoUndo, "Needs sysadmin on SQL01\\WMS (creating Agent jobs owned by sa)."} {
		if !suContains(p.Warnings, w) {
			t.Errorf("warning missing: %s", w)
		}
	}
	for _, want := range []string{"N'kakapo: history cleanup'", "@freq_type = 8, @freq_interval = 1, @freq_recurrence_factor = 1, @active_start_time = 50000",
		"@category_name = N'Database Maintenance', @owner_login_name = @owner", "@notify_email_operator_name = N'Database support'",
		"sp_delete_backuphistory @oldest_date = @oldest;", "DATEADD(DAY, -30, GETDATE())", "/* kakapo */"} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q", want)
		}
	}
	if p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: history cleanup';" || p.Express {
		t.Errorf("rollback %q", p.Rollback)
	}
	if len(p.Inputs) != 3 || p.Inputs[0].Name != "keepDays" || p.Inputs[0].Advanced || !p.Inputs[1].Advanced || !p.Inputs[2].Advanced ||
		!reflect.DeepEqual(p.Options, map[string]string{"keepDays": "30", "time": "05:00", "day": "Sunday"}) {
		t.Errorf("inputs %+v %v", p.Inputs, p.Options)
	}

	// Options: 60 days on Saturday at 04:00.
	p, _, _ = BuildHistoryCleanupFix(f, map[string]string{"keepDays": "60", "time": "4:00", "day": "saturday"})
	if !strings.Contains(p.Statement, "DATEADD(DAY, -60, GETDATE())") || !strings.Contains(p.Statement, "@freq_interval = 64") ||
		p.Changes[0].Target != "kakapo: history cleanup, Saturday 04:00, keeps 60 days" {
		t.Errorf("options %s %+v", p.Statement, p.Changes[0])
	}

	// Blocks: under 7 days, over a year, not a number, a bad time or day.
	for opt, warn := range map[[2]string]string{
		{"keepDays", "6"}: historyKeepWarn, {"keepDays", "366"}: historyKeepWarn, {"keepDays", "x"}: historyKeepWarn,
		{"time", "25:00"}: historyTimeWarn, {"day", "Funday"}: checkdbDayWarn,
	} {
		p, _, err := BuildHistoryCleanupFix(f, map[string]string{opt[0]: opt[1]})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != warn {
			t.Errorf("%v: %+v %v", opt, p, err)
		}
	}

	// Another job prunes already: a warning, not a block. A big first run says so.
	f.OtherJob = "sp_delete_backuphistory"
	f.Tables[0].Older = 2_000_000
	p, _, _ = BuildHistoryCleanupFix(f, nil)
	if len(p.Changes) == 0 || p.Warnings[0] != "The job sp_delete_backuphistory already prunes history on this server: check what it keeps before adding a second cleanup." ||
		!suContains(p.Warnings, "The first run deletes 2,000,000 rows: on a large msdb that takes a while and holds locks on the history tables, which is why it runs at a quiet hour.") {
		t.Errorf("other job %q", p.Warnings)
	}

	// The Agent stopped; Express gets the script.
	f = historyFacts()
	f.AgentStatus = "Stopped"
	if p, _, _ = BuildHistoryCleanupFix(f, nil); !suContains(p.Warnings, "SQL Server Agent is stopped: the job will not run until it starts (MNT01).") {
		t.Errorf("agent %q", p.Warnings)
	}
	f.EngineEdition = 4
	p, _, _ = BuildHistoryCleanupFix(f, nil)
	if !p.Express || p.Rollback != "" || !strings.HasPrefix(p.Statement, "-- kakapo: history cleanup on SQL01\\WMS.") || !strings.Contains(p.Statement, "sp_purge_jobhistory") ||
		p.Changes[len(p.Changes)-1].Target != "no one (Express has no Agent to send an email)" {
		t.Errorf("express %+v", p)
	}

	// kakapo's job exists: nothing to apply.
	f = historyFacts()
	f.JobExists = true
	if _, _, err := BuildHistoryCleanupFix(f, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("job exists: %v", err)
	}
	if HistoryKeepDays(map[string]string{"keepDays": "45"}) != 45 || HistoryKeepDays(map[string]string{"keepDays": "3"}) != 30 || HistoryKeepDays(nil) != 30 {
		t.Error("keep days")
	}
}

// --- maintenance-plan: the choice, the stitching, the apply ---

func nothingPlan() MaintenancePlan { return BuildMaintenancePlan(planFacts(), PlanAlerts{}) }

func callSteps(c PlanChoice) []string {
	out := []string{}
	for _, x := range c.Calls {
		out = append(out, x.Step+"/"+x.FixID)
	}
	return out
}

func TestChooseMaintenancePlan(t *testing.T) {
	c, err := ChooseMaintenancePlan(nothingPlan(), PlanAlerts{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The apply order: alerts, cleanup, check, statistics, backup (index off by default).
	want := []string{"alerts/operator-alerts", "cleanup/history-cleanup", "check/checkdb-job", "maintenance/maintenance-job", "backup/backup-schedule"}
	if !reflect.DeepEqual(callSteps(c), want) {
		t.Errorf("calls %v", callSteps(c))
	}
	byStep := map[string]PlanCall{}
	for _, x := range c.Calls {
		byStep[x.Step] = x
	}
	if !reflect.DeepEqual(byStep[PlanStepAlerts].Options, map[string]string{"alerts": "true", "operator": "Database support", "email": ""}) {
		t.Errorf("alerts %v", byStep[PlanStepAlerts].Options)
	}
	if !reflect.DeepEqual(byStep[PlanStepCleanup].Options, map[string]string{"keepDays": "30", "time": "05:00", "day": "Sunday"}) {
		t.Errorf("cleanup %v", byStep[PlanStepCleanup].Options)
	}
	if !reflect.DeepEqual(byStep[PlanStepCheck].Options, map[string]string{"day": "Sunday", "time": "01:00"}) {
		t.Errorf("check %v", byStep[PlanStepCheck].Options)
	}
	if m := byStep[PlanStepMaintenance]; m.Title != "Statistics update" || !reflect.DeepEqual(m.Options,
		map[string]string{"time": "01:00", "statisticsOnly": "true", "day": "Sunday", "indexTime": "03:00"}) {
		t.Errorf("maintenance %+v", m)
	}
	if b := byStep[PlanStepBackup]; b.Title != "Backups" || !reflect.DeepEqual(b.Options, map[string]string{"runNow": "false", "fullTime": "22:00", "logMinutes": "15"}) ||
		!reflect.DeepEqual(b.Pieces, []string{PieceFullBackup, PieceLogBackup}) {
		t.Errorf("backup %+v", b)
	}
	names := []string{}
	for _, in := range c.Inputs {
		names = append(names, in.Name)
		if in.Advanced != map[string]bool{"checkDay": true, "checkTime": true, "indexTime": true, "cleanupTime": true, "keepDays": true,
			"statsTime": true, "fullTime": true, "logMinutes": true}[in.Name] {
			t.Errorf("advanced %+v", in)
		}
	}
	if strings.Join(names, ",") != "backup,check,statistics,index,cleanup,alerts,folder,operator,email,checkDay,checkTime,indexTime,cleanupTime,keepDays,statsTime,fullTime,logMinutes" {
		t.Errorf("inputs %v", names)
	}
	if c.Options["index"] != "false" || len(c.Blocked) != 0 || len(c.Warnings) != 0 {
		t.Errorf("choice %+v", c)
	}

	// Index work on, statistics off, backup off, another day and times; an operator exists already.
	c, _ = ChooseMaintenancePlan(nothingPlan(), PlanAlerts{Operators: 1}, map[string]string{
		"index": "true", "statistics": "false", "backup": "false", "checkDay": "Saturday", "indexTime": "02:30", "folder": `D:\Backup`})
	want = []string{"alerts/operator-alerts", "cleanup/history-cleanup", "check/checkdb-job", "maintenance/maintenance-job"}
	if !reflect.DeepEqual(callSteps(c), want) {
		t.Errorf("calls %v", callSteps(c))
	}
	m := c.Calls[3]
	if m.Title != "Index maintenance" || m.Options["indexOnly"] != "true" || m.Options["statisticsOnly"] != "false" || m.Options["day"] != "Saturday" ||
		m.Options["indexTime"] != "02:30" || !reflect.DeepEqual(c.Calls[0].Options, map[string]string{"alerts": "true"}) {
		t.Errorf("index call %+v %+v", m, c.Calls[0])
	}
	for _, in := range c.Inputs {
		if in.Name == "folder" || in.Name == "email" || in.Name == "operator" {
			t.Errorf("input %s shown", in.Name)
		}
	}

	// Bad values block, each on its input; times in the same hour warn.
	c, _ = ChooseMaintenancePlan(nothingPlan(), PlanAlerts{}, map[string]string{"checkTime": "1am", "keepDays": "3", "logMinutes": "7", "statsTime": "22:30"})
	if len(c.Blocked) != 3 || c.Blocked[0] != "Integrity check at: The time must be HH:MM, for example 01:00." {
		t.Errorf("blocked %q", c.Blocked)
	}
	if len(c.Warnings) != 1 || !strings.HasPrefix(c.Warnings[0], "statistics update (22:30) and full backup (22:00) start within the same hour on Monday") {
		t.Errorf("warnings %q", c.Warnings)
	}

	// Nothing to create.
	healthy := MaintenancePlan{Pieces: []PlanPiece{}}
	if _, err := ChooseMaintenancePlan(healthy, PlanAlerts{}, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("nothing: %v", err)
	}
}

// fixturePart is a piece prepared by its fake fix.
func fixturePart(step, title, fixID, stmt, rollback string, warnings ...string) PlanPart {
	return PlanPart{Call: PlanCall{Step: step, Title: title, FixID: fixID},
		Preview: FixPreview{EngineVersion: "16.0.4255.3", Statement: stmt, Rollback: rollback, Warnings: warnings,
			Changes: []Drift{{Setting: "Tell on failure", Target: "Database support"}}}}
}

func TestStitchMaintenancePlan(t *testing.T) {
	choice, _ := ChooseMaintenancePlan(nothingPlan(), PlanAlerts{}, map[string]string{"email": "dba@client.co.za"})
	alerts := fixturePart(PlanStepAlerts, "Alerts and an operator", FixOperatorAlerts, "EXEC msdb.dbo.sp_add_operator ...;", "EXEC msdb.dbo.sp_delete_operator ...;", "Needs sysadmin on S.")
	alerts.Preview.Changes = append([]Drift{{Setting: "Operator", Current: "none with an email address", Target: "Database support (dba@client.co.za)"}}, alerts.Preview.Changes...)
	alerts.Preview.Inputs = []FixInput{{Name: OptionOperatorName, Value: "Database support"}, {Name: OptionOperatorEmail, Value: "dba@client.co.za"}}
	cleanup := fixturePart(PlanStepCleanup, "History cleanup", FixHistoryCleanup, "-- cleanup batch", "-- delete cleanup", historyFiles, "Needs sysadmin on S.")
	backup := fixturePart(PlanStepBackup, "Backups", FixBackupSchedule, "-- backup batch", "-- delete backups", "Needs sysadmin on S.")
	backup.Preview.Inputs = []FixInput{{Name: OptionFolder, Value: `D:\SQLBackup`}}
	parts := []PlanPart{alerts, cleanup, backup}
	p := StitchMaintenancePlan(choice, parts)
	if len(p.Changes) != 4 || p.Changes[0].Setting != "Alerts and an operator: Operator" || p.Changes[3].Setting != "Backups: Tell on failure" {
		t.Errorf("changes %+v", p.Changes)
	}
	wantStmt := "-- 1. Alerts and an operator (operator-alerts)\nEXEC msdb.dbo.sp_add_operator ...;\nGO\n" +
		"-- 2. History cleanup (history-cleanup)\n-- cleanup batch\nGO\n" +
		"-- 3. Backups (backup-schedule)\n-- backup batch\nGO"
	if p.Statement != wantStmt {
		t.Errorf("statement\n%s", p.Statement)
	}
	wantRb := "-- Undo 3. Backups\n-- delete backups\nGO\n-- Undo 2. History cleanup\n-- delete cleanup\nGO\n-- Undo 1. Alerts and an operator\nEXEC msdb.dbo.sp_delete_operator ...;\nGO"
	if p.Rollback != wantRb {
		t.Errorf("rollback\n%s", p.Rollback)
	}
	// The warnings without repeats: the order, the operator note, then the pieces'.
	if strings.Count(strings.Join(p.Warnings, "\n"), "Needs sysadmin on S.") != 1 ||
		p.Warnings[0] != "kakapo creates the pieces one after another, each through its own fix: alerts and an operator, then history cleanup, then backups. If one fails, the pieces before it stay (each has its own change log entry and rollback) and the rest are not created." ||
		p.Warnings[1] != planOperatorWarn || !suContains(p.Warnings, historyFiles) {
		t.Errorf("warnings %q", p.Warnings)
	}
	// The folder the backup piece defaulted shows in the composite's input and options.
	for _, in := range p.Inputs {
		if in.Name == OptionPlanFolder && in.Value != `D:\SQLBackup` {
			t.Errorf("folder %+v", in)
		}
	}
	if p.Options[OptionPlanFolder] != `D:\SQLBackup` || p.Options[OptionPlanEmail] != "dba@client.co.za" || p.EngineVersion != "16.0.4255.3" || p.Database != "" {
		t.Errorf("options %v", p.Options)
	}

	// A piece whose own preview blocks blocks the plan with its reason, and its input carries it.
	blocked := backup
	blocked.Preview = FixPreview{Changes: []Drift{}, Warnings: []string{"The folder D:\\Nope does not exist on S (as the SQL Server service sees it): create it, or type another."},
		Inputs: []FixInput{{Name: OptionFolder, Value: `D:\Nope`, Warning: "The folder D:\\Nope does not exist on S (as the SQL Server service sees it): create it, or type another."}}}
	notApplicable := PlanPart{Call: PlanCall{Step: PlanStepCheck, Title: "Integrity check", FixID: FixCheckdbJob}, Err: "nothing to create now"}
	p = StitchMaintenancePlan(choice, []PlanPart{alerts, cleanup, notApplicable, blocked})
	if len(p.Changes) != 0 || p.Statement != "" || p.Rollback != "" ||
		p.Warnings[0] != "Integrity check: nothing to create now" || !strings.HasPrefix(p.Warnings[1], "Backups: The folder D:\\Nope does not exist") {
		t.Errorf("blocked %+v", p)
	}
	for _, in := range p.Inputs {
		if in.Name == OptionPlanFolder && (in.Value != `D:\Nope` || in.Warning == "") {
			t.Errorf("folder %+v", in)
		}
	}
	// An Express preview cannot be applied from the plan.
	express := cleanup
	express.Preview.Express = true
	if p = StitchMaintenancePlan(choice, []PlanPart{express}); len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "History cleanup: SQL Server Express") {
		t.Errorf("express %+v", p.Warnings)
	}
	// Nothing ticked.
	none, _ := ChooseMaintenancePlan(nothingPlan(), PlanAlerts{}, map[string]string{"backup": "false", "check": "false", "statistics": "false", "cleanup": "false", "alerts": "false"})
	if p = StitchMaintenancePlan(none, nil); len(p.Changes) != 0 || p.Warnings[0] != planNothing {
		t.Errorf("nothing ticked %+v", p)
	}
}

func TestRunMaintenancePlan(t *testing.T) {
	parts := []PlanPart{
		fixturePart(PlanStepAlerts, "Alerts and an operator", FixOperatorAlerts, "a", "ra"),
		fixturePart(PlanStepCleanup, "History cleanup", FixHistoryCleanup, "c", "rc"),
		fixturePart(PlanStepCheck, "Integrity check", FixCheckdbJob, "k", "rk"),
		fixturePart(PlanStepMaintenance, "Statistics update", FixMaintenanceJob, "s", "rs"),
		fixturePart(PlanStepBackup, "Backups", FixBackupSchedule, "b", "rb"),
	}
	parts[0].Call.Pieces, parts[1].Call.Pieces = []string{PieceAlerts}, []string{PieceCleanup}
	var order []string
	o := RunMaintenancePlan(parts, func(i int, p PlanPart) error {
		order = append(order, p.Call.Step)
		if p.Call.Step == PlanStepCheck {
			return errors.New("SQL Server refused to create the integrity check job")
		}
		return nil
	})
	if !reflect.DeepEqual(order, []string{"alerts", "cleanup", "check"}) {
		t.Errorf("order %v", order)
	}
	if len(o.Created) != 2 || o.Failed == nil || o.Failed.Call.Step != PlanStepCheck || len(o.NotCreated) != 2 ||
		o.Why != "SQL Server refused to create the integrity check job" {
		t.Errorf("outcome %+v", o)
	}
	if d := o.Description(`SQL01\WMS`); d != `Maintenance plan on SQL01\WMS: created alerts and an operator, history cleanup; stopped at integrity check (SQL Server refused to create the integrity check job); not created: statistics update, backups` {
		t.Errorf("description %q", d)
	}
	if !reflect.DeepEqual(o.CreatedPieces(), []string{PieceAlerts, PieceCleanup}) {
		t.Errorf("pieces %v", o.CreatedPieces())
	}
	stmt, rb := PlanStatements(o.Created)
	if stmt != "-- 1. Alerts and an operator (operator-alerts)\na\nGO\n-- 2. History cleanup (history-cleanup)\nc\nGO" ||
		rb != "-- Undo 2. History cleanup\nrc\nGO\n-- Undo 1. Alerts and an operator\nra\nGO" {
		t.Errorf("summary statements\n%s\n%s", stmt, rb)
	}
	all := RunMaintenancePlan(parts, func(int, PlanPart) error { return nil })
	if len(all.Created) != 5 || all.Failed != nil || all.Description("S") != "Maintenance plan on S: created alerts and an operator, history cleanup, integrity check, statistics update, backups" {
		t.Errorf("all %+v", all)
	}
}

func TestPlanReadBack(t *testing.T) {
	f := planFacts()
	f.Jobs = []PlanJob{planJob(HistoryCleanupJobName, HistoryCleanupCommand(30), sunday0500)}
	f.Jobs[0].LastRun, f.Jobs[0].LastStatus = nil, PlanNoRun
	after := BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	if s, ok := PlanReadBack(after, []string{PieceAlerts, PieceCleanup}); !ok || s != "Alerts and an operator: in place; History cleanup: created, not run yet." {
		t.Errorf("read back %q %v", s, ok)
	}
	if s, ok := PlanReadBack(after, []string{PieceIntegrity}); ok || s != "Integrity check: still missing." {
		t.Errorf("missing %q %v", s, ok)
	}
	if _, ok := PlanReadBack(after, nil); ok {
		t.Error("nothing created")
	}
}

// maintenance-job with the plan's options: statistics skip the maintenance day, the index job runs on
// it at its own time, and indexOnly leaves the statistics job out. Without them it is as before.
func TestMaintenanceJobPlanOptions(t *testing.T) {
	f := MaintenanceJobFacts{ServerName: "S", EngineEdition: 2, Database: "WarehouseLive", Ola: true, ExistingJobs: []string{}, ServerNow: setupNow}
	p, _, err := BuildMaintenanceJobFix(f, map[string]string{OptionStatsOnly: "true", OptionMaintDay: "Sunday"})
	if err != nil || !strings.Contains(p.Statement, "N'kakapo: statistics (WarehouseLive), Monday to Saturday 01:00'") || strings.Contains(p.Statement, "indexes") {
		t.Errorf("statistics on the other nights: %v\n%s", err, p.Statement)
	}
	p, _, _ = BuildMaintenanceJobFix(f, map[string]string{OptionStatsOnly: "false", OptionMaintDay: "Saturday", OptionMaintIndexTime: "03:00"})
	if !strings.Contains(p.Statement, "N'kakapo: statistics (WarehouseLive), Sunday, Monday, Tuesday, Wednesday, Thursday, Friday 01:00'") ||
		!strings.Contains(p.Statement, "N'kakapo: indexes (WarehouseLive), Saturday 03:00'") ||
		!suContains(p.Warnings, "Index work writes a lot of log: check that Saturday 03:00 is a quiet hour and that the log has room. It never rebuilds offline: without Enterprise edition it reorganises, which keeps the table open to users.") {
		t.Errorf("index on Saturday\n%s\n%q", p.Statement, p.Warnings)
	}
	p, _, _ = BuildMaintenanceJobFix(f, map[string]string{OptionMaintIndexOnly: "true", OptionMaintIndexTime: "03:00"})
	if strings.Contains(p.Statement, "kakapo: statistics") || !strings.Contains(p.Statement, "N'kakapo: indexes (WarehouseLive), Sunday 03:00'") {
		t.Errorf("index only\n%s", p.Statement)
	}
	if names := MaintenanceJobNames(f, map[string]string{OptionMaintIndexOnly: "true"}); !reflect.DeepEqual(names, []string{"kakapo: indexes (WarehouseLive)"}) {
		t.Errorf("names %v", names)
	}
	f.ExistingJobs = []string{IndexJobName("WarehouseLive")}
	if _, _, err := BuildMaintenanceJobFix(f, map[string]string{OptionMaintIndexOnly: "true"}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("index exists: %v", err)
	}
	f.ExistingJobs = []string{}
	if p, _, _ := BuildMaintenanceJobFix(f, map[string]string{OptionMaintIndexTime: "3am"}); len(p.Changes) != 0 || p.Warnings[0] != maintTimeWarn {
		t.Errorf("bad index time %q", p.Warnings)
	}
	// Without the plan's options the options echoed are as before.
	p, _, _ = BuildMaintenanceJobFix(f, nil)
	if !reflect.DeepEqual(p.Options, map[string]string{OptionMaintTime: "01:00", OptionStatsOnly: "true"}) {
		t.Errorf("options %v", p.Options)
	}
}

func TestUpkeepPlanJSONAndFixes(t *testing.T) {
	f := goodUpkeep()
	plan := nothingPlan()
	f.MaintenancePlan = &plan
	c, fs, cs := runUpkeep(f)
	fixes := AvailableUpkeepFixes(f, fs)
	if len(fixes) != 1 || fixes[0].ID != FixMaintenancePlan {
		t.Errorf("fixes %+v", fixes)
	}
	b, _ := json.Marshal(CardDetail{Card: c, Findings: fs, Checks: cs, Fixes: fixes, MaintenancePlan: &plan})
	s := string(b)
	for _, want := range []string{`"maintenancePlan":{"pieces":[{"id":"full-backup","title":"Full backup","cadence":"daily 22:00","status":"missing","fixId":"backup-schedule"`,
		`"missing":6,"fixId":"maintenance-plan"`, `"id":"maintenance-plan"`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON lacks %s:\n%s", want, s)
		}
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardBackups}, Findings: []Finding{}, Checks: []CheckStatus{}})
	if strings.Contains(string(b), "maintenancePlan") {
		t.Errorf("other card: %s", b)
	}
}

// Phase 5q: kakapo's own index job made before 5q rebuilds offline; the row says so and gives the step.
func TestPlanOldOfflineIndexJob(t *testing.T) {
	f := planFacts()
	old := "EXECUTE master.dbo.IndexOptimize @Databases = N'WarehouseLive', @FragmentationLow = NULL, @FragmentationMedium = 'INDEX_REORGANIZE', " +
		"@FragmentationHigh = 'INDEX_REBUILD_ONLINE,INDEX_REBUILD_OFFLINE', @FragmentationLevel1 = 30, @FragmentationLevel2 = 50, @MinNumberOfPages = 1000"
	f.Jobs = []PlanJob{planJob(IndexJobName("WarehouseLive"), old, sunday0100)}
	x := pieceOf(t, BuildMaintenancePlan(f, PlanAlerts{Operators: 1}), PieceIndexes)
	if !strings.HasPrefix(x.Note, "This job was made before kakapo stopped offline rebuilds") || !strings.Contains(x.Note, "INDEX_REBUILD_ONLINE,INDEX_REORGANIZE") {
		t.Errorf("old job %+v", x)
	}
	f.Jobs = []PlanJob{planJob(IndexJobName("WarehouseLive"), olaIndexCommand("WarehouseLive"), sunday0100)}
	if x := pieceOf(t, BuildMaintenancePlan(f, PlanAlerts{Operators: 1}), PieceIndexes); strings.Contains(x.Note, "offline") {
		t.Errorf("new job %+v", x)
	}
}
