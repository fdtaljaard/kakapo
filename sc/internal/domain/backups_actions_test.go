package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func bakHasWarning(p FixPreview, prefix string) bool {
	for _, w := range p.Warnings {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// neverChecked is scheduleFacts with no CHECKDB ever: BAK03 fires.
func neverChecked(model string) BackupFacts {
	f := scheduleFacts(model)
	f.LastGoodCheckDb = at(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC))
	return f
}

// Which findings carry which phase 4d fix, and the card counts them.
func TestBackupActionFindings(t *testing.T) {
	f := neverChecked("SIMPLE")
	f.SuspectPages, f.SuspectLast = 2, at(time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC))
	c, fs, _ := build(f)
	if findingOf(t, fs, "BAK03").FixID != FixCheckdbJob || findingOf(t, fs, "BAK04").FixID != FixCheckdbNow ||
		findingOf(t, fs, "DBC08").FixID != FixRecoveryModel {
		t.Errorf("fix ids %+v", fs)
	}
	fixable := 0
	for _, x := range fs {
		if x.FixID != "" {
			fixable++
		}
	}
	if c.Fixable != fixable || fixable < 4 {
		t.Errorf("fixable %d, findings with a fix %d", c.Fixable, fixable)
	}
	ids := []string{}
	for _, x := range AvailableBackupFixes(f, fs) {
		ids = append(ids, x.ID+"/"+x.CheckID)
	}
	if got := strings.Join(ids, " "); got != "backup-schedule/BAK01 recovery-model/DBC08 checkdb-job/BAK03 checkdb-now/BAK03" {
		t.Errorf("available %q", got)
	}

	// A clean run after the latest record (10-30 14:05 server time = 12:05 UTC): clear; before it or
	// with errors: run CHECKDB again.
	f.LastCheckdbRun = &CheckdbRun{RanAt: time.Date(2026, 10, 30, 12, 30, 0, 0, time.UTC), Clean: true, Outcome: CheckdbOutcomeClean}
	_, fs, _ = build(f)
	if findingOf(t, fs, "BAK04").FixID != FixSuspectPagesClear {
		t.Errorf("clean run after: %+v", findingOf(t, fs, "BAK04"))
	}
	if x := AvailableBackupFixes(f, fs); x[len(x)-1].ID != FixSuspectPagesClear || x[len(x)-1].Current != "2 record(s)" {
		t.Errorf("available with clear %+v", x)
	}
	f.LastCheckdbRun.RanAt = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if _, fs, _ = build(f); findingOf(t, fs, "BAK04").FixID != FixCheckdbNow {
		t.Errorf("clean run before: %+v", findingOf(t, fs, "BAK04"))
	}
	f.LastCheckdbRun = &CheckdbRun{RanAt: serverNow, Clean: false, Outcome: "CHECKDB found 0 allocation errors and 1 consistency errors in database 'WarehouseLive'."}
	if _, fs, _ = build(f); findingOf(t, fs, "BAK04").FixID != FixCheckdbNow {
		t.Errorf("run with errors: %+v", findingOf(t, fs, "BAK04"))
	}

	// Without the jobs read (or on a secondary) BAK03 offers no job.
	f = neverChecked("FULL")
	f.JobsRead = false
	if _, fs, _ := build(f); findingOf(t, fs, "BAK03").FixID != "" {
		t.Errorf("jobs not read: %+v", findingOf(t, fs, "BAK03"))
	}
	f = neverChecked("FULL")
	f.IsSecondary = true
	if _, fs, _ := build(f); findingOf(t, fs, "BAK03").FixID != "" {
		t.Errorf("secondary: %+v", findingOf(t, fs, "BAK03"))
	}
}

func TestRecoveryModelFix(t *testing.T) {
	// SIMPLE (DBC08): FULL by default, with the log job warning.
	f := scheduleFacts("SIMPLE")
	p, fix, err := BuildRecoveryModelFix(f, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "ALTER DATABASE [WarehouseLive] SET RECOVERY FULL;" || p.Rollback != "ALTER DATABASE [WarehouseLive] SET RECOVERY SIMPLE;" ||
		len(p.Changes) != 1 || p.Changes[0] != (Drift{"Recovery model", "SIMPLE", "FULL"}) || fix.CheckID != "DBC08" || fix.Target != "FULL" {
		t.Errorf("to FULL: %+v %+v", p, fix)
	}
	if p.Warnings[0] != recoveryToFull || p.Warnings[1] != recoveryChain || p.Warnings[len(p.Warnings)-1] != "Needs the ALTER permission on the database WarehouseLive." {
		t.Errorf("to FULL warnings %q", p.Warnings)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].Kind != "select" || p.Inputs[0].Value != "FULL" || p.Options[OptionRecoveryModel] != "FULL" {
		t.Errorf("inputs %+v", p.Inputs)
	}
	// The same model, an unknown one, a read-only database: the warning, no changes.
	for _, c := range []struct {
		opts     map[string]string
		readOnly bool
		warn     string
	}{
		{map[string]string{OptionRecoveryModel: "simple"}, false, "WarehouseLive already uses the SIMPLE recovery model: pick the other one."},
		{map[string]string{OptionRecoveryModel: "BULK_LOGGED"}, false, "Pick FULL or SIMPLE."},
		{nil, true, "WarehouseLive is read-only, so its recovery model cannot be changed."},
	} {
		p, _, err := BuildRecoveryModelFix(f, c.readOnly, c.opts)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != c.warn {
			t.Errorf("%v %v: %v %+v", c.opts, c.readOnly, err, p)
		}
	}

	// FULL without log backups (BAK02): SIMPLE by default, with the data-loss warning; refused in an AG.
	f = scheduleFacts("FULL")
	nightlyFulls(&f)
	p, fix, err = BuildRecoveryModelFix(f, false, nil)
	if err != nil || p.Statement != "ALTER DATABASE [WarehouseLive] SET RECOVERY SIMPLE;" || fix.CheckID != "BAK02" || p.Warnings[0] != recoveryToSimple {
		t.Errorf("to SIMPLE: %v %+v %+v", err, p, fix)
	}
	f.IsAgMember = true
	if p, _, _ = BuildRecoveryModelFix(f, false, nil); len(p.Changes) != 0 || p.Warnings[0] != "WarehouseLive is in an availability group, which needs FULL recovery: it stays in FULL." {
		t.Errorf("AG: %+v", p)
	}
	// A log job that takes log backups fails in SIMPLE: say so.
	f.IsAgMember = false
	f.BackupJobs = []BackupJob{{Name: "Logs", Enabled: true, Kind: JobLog}}
	if p, _, _ = BuildRecoveryModelFix(f, false, map[string]string{OptionRecoveryModel: "SIMPLE"}); !bakHasWarning(p, "The job Logs takes log backups of this database: they fail in SIMPLE recovery") {
		t.Errorf("log job: %q", p.Warnings)
	}
	// FULL with log backups: neither DBC08 nor BAK02, nothing to apply.
	f = scheduleFacts("FULL")
	nightlyFulls(&f)
	logsEvery15(&f)
	if _, _, err := BuildRecoveryModelFix(f, false, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("healthy FULL: %v", err)
	}
	if after, ok := RecoveryModelReadBack("FULL", "FULL"); !ok || after != "recovery model FULL" {
		t.Errorf("read back %q", after)
	}
	if _, ok := RecoveryModelReadBack("SIMPLE", "FULL"); ok {
		t.Error("read back differs")
	}
}

const wantCheckdbJobBatch = `DECLARE @owner sysname = SUSER_SNAME(0x01);
BEGIN TRY
    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'kakapo: WarehouseLive integrity check')
        THROW 50000, N'A job named kakapo: WarehouseLive integrity check already exists', 1;
    BEGIN TRANSACTION;
    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)
        EXEC msdb.dbo.sp_add_category @class = N'JOB', @type = N'LOCAL', @name = N'Database Maintenance';
    EXEC msdb.dbo.sp_add_job @job_name = N'kakapo: WarehouseLive integrity check', @enabled = 1,
        @description = N'Created by kakapo on 2026-11-03 for WarehouseLive: integrity check weekly on Sunday at 01:00, full check',
        @category_name = N'Database Maintenance', @owner_login_name = @owner;
    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: WarehouseLive integrity check', @step_id = 1, @step_name = N'Check integrity',
        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 1, @on_fail_action = 2,
        @command = N'/* kakapo */
DBCC CHECKDB ([WarehouseLive]) WITH NO_INFOMSGS, ALL_ERRORMSGS;';
    EXEC msdb.dbo.sp_add_jobschedule @job_name = N'kakapo: WarehouseLive integrity check', @name = N'kakapo: WarehouseLive integrity check, weekly on Sunday at 01:00', @enabled = 1,
        @freq_type = 8, @freq_interval = 1, @freq_recurrence_factor = 1, @active_start_time = 10000;
    EXEC msdb.dbo.sp_add_jobserver @job_name = N'kakapo: WarehouseLive integrity check', @server_name = N'(local)';
    COMMIT TRANSACTION;
END TRY
BEGIN CATCH
    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;
    THROW;
END CATCH;`

func TestCheckdbJobFix(t *testing.T) {
	f := neverChecked("FULL")
	p, fix, err := BuildCheckdbJobFix(f, CheckdbJobFacts{DataMB: i64(4096)}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != wantCheckdbJobBatch {
		t.Errorf("batch\n%s", p.Statement)
	}
	if p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: WarehouseLive integrity check';" || p.Express ||
		fix.Target != "kakapo: WarehouseLive integrity check (weekly on Sunday at 01:00, full check)" || fix.CheckID != "BAK03" {
		t.Errorf("rollback %q fix %+v", p.Rollback, fix)
	}
	if len(p.Changes) != 3 || p.Changes[0] != (Drift{"Integrity check job", "none", "kakapo: WarehouseLive integrity check, weekly on Sunday at 01:00"}) ||
		p.Changes[2] != (Drift{"Tell on failure", "", noOperator}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if !bakHasWarning(p, "kakapo does not know yet whether tempdb and the data volume have room") || !bakHasWarning(p, checkdbFirstRun) ||
		!bakHasWarning(p, checkdbNoOperator) || p.Warnings[len(p.Warnings)-1] != `Needs sysadmin on SQL01\WMS (creating Agent jobs owned by sa).` {
		t.Errorf("warnings %q", p.Warnings)
	}
	if len(p.Inputs) != 3 || p.Options[OptionCheckdbDay] != "Sunday" || p.Options[OptionCheckdbTime] != "01:00" || p.Options[OptionPhysicalOnly] != "false" {
		t.Errorf("inputs %+v options %+v", p.Inputs, p.Options)
	}

	// Options: another day and time, physical only, an operator, and the room estimate.
	f.Operator = "Database support"
	room := BuildCheckdbRoom(CheckdbRoomInputs{Database: "WarehouseLive", CapturedAt: serverNow, EstimateKB: 4 << 20, TempdbFreeMB: 100, TempdbRoomMB: 100, DataFreeMB: 50000, DataSizeMB: 4096})
	p, fix, _ = BuildCheckdbJobFix(f, CheckdbJobFacts{DataMB: i64(4096)}, &room,
		map[string]string{OptionCheckdbDay: "saturday", OptionCheckdbTime: "2:30", OptionPhysicalOnly: "true"})
	if !strings.Contains(p.Statement, "@freq_type = 8, @freq_interval = 64, @freq_recurrence_factor = 1, @active_start_time = 23000;") ||
		!strings.Contains(p.Statement, "DBCC CHECKDB ([WarehouseLive]) WITH NO_INFOMSGS, ALL_ERRORMSGS, PHYSICAL_ONLY;") {
		t.Errorf("options batch\n%s", p.Statement)
	}
	if !strings.Contains(p.Statement, "@notify_level_email = 2, @notify_email_operator_name = N'Database support'") {
		t.Errorf("operator\n%s", p.Statement)
	}
	if fix.Target != "kakapo: WarehouseLive integrity check (weekly on Saturday at 02:30, physical only)" || p.Options[OptionCheckdbDay] != "Saturday" {
		t.Errorf("fix %+v options %+v", fix, p.Options)
	}
	if !bakHasWarning(p, "Room for CHECKDB (STO10, 2026-11-03 09:21 UTC): CHECKDB on WarehouseLive needs about 8.0 GB of tempdb, but tempdb can only reach 100 MB. WITH PHYSICAL_ONLY needs far less.") ||
		!bakHasWarning(p, physicalOnlyNote) || bakHasWarning(p, checkdbNoOperator) {
		t.Errorf("warnings %q", p.Warnings)
	}
	// 200 GB of data: physical only by default.
	if p, _, _ = BuildCheckdbJobFix(f, CheckdbJobFacts{DataMB: i64(300 * 1024)}, nil, nil); p.Options[OptionPhysicalOnly] != "true" {
		t.Errorf("large: %+v", p.Options)
	}
	// Bad day or time, kakapo's job already there: the warning, no changes. Another job warns only.
	for _, c := range []struct {
		opts map[string]string
		jf   CheckdbJobFacts
		warn string
	}{
		{map[string]string{OptionCheckdbDay: "Someday"}, CheckdbJobFacts{}, checkdbDayWarn},
		{map[string]string{OptionCheckdbTime: "25:00"}, CheckdbJobFacts{}, checkdbTimeWarn},
		{nil, CheckdbJobFacts{Jobs: []IntegrityJob{{Name: "kakapo: WarehouseLive integrity check", Enabled: true}}}, "The job kakapo: WarehouseLive integrity check already exists"},
	} {
		p, _, err := BuildCheckdbJobFix(f, c.jf, nil, c.opts)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || !strings.HasPrefix(p.Warnings[0], c.warn) {
			t.Errorf("%v: %v %+v", c.opts, err, p)
		}
	}
	p, _, _ = BuildCheckdbJobFix(f, CheckdbJobFacts{Jobs: []IntegrityJob{{Name: "DatabaseIntegrityCheck - USER_DATABASES", Enabled: true}}}, nil, nil)
	if len(p.Changes) == 0 || !strings.HasPrefix(p.Warnings[0], "The job DatabaseIntegrityCheck - USER_DATABASES already runs an integrity check here") {
		t.Errorf("other job: %q", p.Warnings)
	}
	// Express: a script, nothing applied.
	f.HasAgent = false
	p, _, _ = BuildCheckdbJobFix(f, CheckdbJobFacts{}, nil, nil)
	if !p.Express || p.Rollback != "" || !strings.HasPrefix(p.Statement, "-- kakapo: integrity check of WarehouseLive on SQL01\\WMS. SQL Server Express has no SQL Server Agent") ||
		!strings.Contains(p.Statement, "sqlcmd -S SQL01\\WMS -E -b -i checkdb-WarehouseLive.sql") || strings.Contains(p.Statement, "sp_add_job") {
		t.Errorf("express: %+v", p)
	}
	// A recent check by hand is not a schedule: the job is offered even when BAK03 did not fire.
	if _, _, err := BuildCheckdbJobFix(scheduleFacts("FULL"), CheckdbJobFacts{}, nil, nil); err != nil {
		t.Errorf("checked: %v", err)
	}
	// Not offered when the jobs were not read (Express and other jobs unknown) or on a secondary replica.
	unread := scheduleFacts("FULL")
	unread.JobsRead = false
	if _, _, err := BuildCheckdbJobFix(unread, CheckdbJobFacts{}, nil, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("jobs not read: %v", err)
	}
}

func TestCheckdbJobReadBack(t *testing.T) {
	j := CheckdbJobFromOptions(neverChecked("FULL"), map[string]string{OptionCheckdbDay: "Sunday", OptionCheckdbTime: "01:00"})
	// serverNow is Tuesday 3 November 09:21: the next Sunday is the 8th.
	after, ok := j.ReadBack([]BackupJobState{{Name: "kakapo: WarehouseLive integrity check", Enabled: true, ScheduleEnabled: true}}, serverNow)
	if !ok || after != "kakapo: WarehouseLive integrity check: enabled, next run 2026-11-08 01:00 (expected; Agent has not set it yet)." {
		t.Errorf("read back %q", after)
	}
	if after, ok := j.ReadBack(nil, serverNow); ok || after != "kakapo: WarehouseLive integrity check: not found." {
		t.Errorf("missing %q", after)
	}
	if d := j.Description(); d != "Created the job kakapo: WarehouseLive integrity check (weekly on Sunday at 01:00, full check)" {
		t.Errorf("description %q", d)
	}
}

func TestCheckdbNowFix(t *testing.T) {
	f := neverChecked("FULL")
	p, fix := BuildCheckdbNowFix(f, i64(4096), nil, nil)
	if p.Statement != "DBCC CHECKDB ([WarehouseLive]) WITH NO_INFOMSGS, ALL_ERRORMSGS;" || p.Rollback != "" || len(p.Changes) != 1 ||
		p.Changes[0] != (Drift{"Integrity check of WarehouseLive", "last good: never", "run now, full check"}) || fix.CheckID != "BAK03" {
		t.Errorf("preview %+v fix %+v", p, fix)
	}
	if !bakHasWarning(p, "kakapo does not know yet") || !bakHasWarning(p, checkdbLoad) || !bakHasWarning(p, checkdbWait) ||
		p.Warnings[len(p.Warnings)-1] != "Needs db_owner of WarehouseLive or sysadmin." {
		t.Errorf("warnings %q", p.Warnings)
	}
	p, _ = BuildCheckdbNowFix(f, i64(4096), nil, map[string]string{OptionPhysicalOnly: "true"})
	if p.Statement != "DBCC CHECKDB ([WarehouseLive]) WITH NO_INFOMSGS, ALL_ERRORMSGS, PHYSICAL_ONLY;" || !PhysicalOnlyOption(p.Options) || !bakHasWarning(p, physicalOnlyNote) {
		t.Errorf("physical only %+v", p)
	}
	// A physicalOnly that is not a bool: the warning, no changes.
	p, _ = BuildCheckdbNowFix(f, nil, nil, map[string]string{OptionPhysicalOnly: "maybe"})
	if len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != physicalOnlyWarn || p.Inputs[0].Warning != physicalOnlyWarn {
		t.Errorf("bad bool %+v", p)
	}
	if p, _, _ := BuildCheckdbJobFix(f, CheckdbJobFacts{}, nil, map[string]string{OptionPhysicalOnly: "yes"}); len(p.Changes) != 0 || p.Warnings[0] != physicalOnlyWarn {
		t.Errorf("job bad bool %+v", p)
	}
	// BAK04 only: shown under BAK04.
	f = scheduleFacts("FULL")
	f.SuspectPages = 1
	if _, fix = BuildCheckdbNowFix(f, nil, nil, nil); fix.CheckID != "BAK04" || fix.Current != "last good check: 2026-10-31 09:21" {
		t.Errorf("BAK04 fix %+v", fix)
	}
}

func TestBuildCheckdbRun(t *testing.T) {
	start := time.Date(2026, 11, 3, 7, 0, 0, 0, time.UTC)
	r, ok := BuildCheckdbRun(start, 95*time.Second, false, nil, "")
	if !ok || !r.Clean || r.Outcome != CheckdbOutcomeClean || r.DurationSeconds != 95 || len(r.Messages) != 0 {
		t.Errorf("clean %+v", r)
	}
	if d := CheckdbRunChangeDescription("WarehouseLive", r); d != "DBCC CHECKDB on WarehouseLive (full check, 95 s): No errors found" {
		t.Errorf("description %q", d)
	}
	damage := []CheckdbMessage{
		{Number: 8928, Class: 16, Text: "Object ID 581577110, index ID 1: Page (1:153) could not be processed.", Error: true},
		{Number: 8989, Class: 16, Text: "CHECKDB found 0 allocation errors and 2 consistency errors in database 'WarehouseLive'.", Error: true},
		{Number: 8958, Class: 16, Text: "repair_allow_data_loss is the minimum repair level for the errors found by DBCC CHECKDB (WarehouseLive).", Error: true},
	}
	r, ok = BuildCheckdbRun(start, time.Hour+5*time.Minute, true, damage, "mssql: CHECKDB found 0 allocation errors")
	if !ok || r.Clean || r.Outcome != "CHECKDB found 0 allocation errors and 2 consistency errors in database 'WarehouseLive'." || len(r.Messages) != 3 ||
		r.Messages[0] != "Msg 8928, Level 16: Object ID 581577110, index ID 1: Page (1:153) could not be processed." || !r.PhysicalOnly {
		t.Errorf("damage %+v", r)
	}
	if d := CheckdbRunChangeDescription("WarehouseLive", r); !strings.HasPrefix(d, "DBCC CHECKDB on WarehouseLive (physical only, 1 h 05 min): CHECKDB found") {
		t.Errorf("description %q", d)
	}
	// Refused or cut off: not completed, not stored.
	r, ok = BuildCheckdbRun(start, time.Second, false, []CheckdbMessage{{Number: 2571, Class: 14, Text: "User 'x' does not have permission to run DBCC CHECKDB.", Error: true}}, "")
	if ok || r.Clean || r.Outcome != "Did not complete: User 'x' does not have permission to run DBCC CHECKDB." {
		t.Errorf("refused %+v", r)
	}
	if r, ok = BuildCheckdbRun(start, 4*time.Hour, false, nil, "context deadline exceeded"); ok || r.Outcome != "Did not complete: context deadline exceeded" {
		t.Errorf("timeout %+v", r)
	}
	b, _ := json.Marshal(CheckdbRun{RanAt: start, Messages: []string{}})
	if !strings.Contains(string(b), `"ranAt":"2026-11-03T07:00:00Z"`) || !strings.Contains(string(b), `"messages":[]`) {
		t.Errorf("JSON %s", b)
	}
}

func TestSuspectPagesClearFix(t *testing.T) {
	f := scheduleFacts("FULL")
	f.SuspectPages, f.SuspectLast = 2, at(time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC))
	pages := []SuspectPage{
		{FileID: 1, PageID: 153, EventType: 2, ErrorCount: 1, LastUpdate: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)},
		{FileID: 1, PageID: 7701, EventType: 1, ErrorCount: 3, LastUpdate: time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)},
	}
	// Refused without a run, with a run that found damage, with a clean run before the latest record.
	for _, c := range []struct {
		run  *CheckdbRun
		warn string
	}{
		{nil, "Run DBCC CHECKDB on WarehouseLive first (Run CHECKDB now)"},
		{&CheckdbRun{RanAt: serverNow, Outcome: "CHECKDB found 0 allocation errors and 1 consistency errors in database 'WarehouseLive'."}, "The last CHECKDB on WarehouseLive (2026-11-03 09:21 UTC) did not come back clean"},
		{&CheckdbRun{RanAt: time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC), Clean: true}, "The last clean CHECKDB (2026-10-30 11:00 UTC) ran before the latest record (2026-10-30 14:05 server time)"},
	} {
		f.LastCheckdbRun = c.run
		p, _, err := BuildSuspectPagesClearFix(f, pages)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || !strings.HasPrefix(p.Warnings[0], c.warn) {
			t.Errorf("%+v: %v %+v", c.run, err, p)
		}
	}
	f.LastCheckdbRun = &CheckdbRun{RanAt: time.Date(2026, 11, 3, 6, 0, 0, 0, time.UTC), Clean: true}
	p, fix, err := BuildSuspectPagesClearFix(f, pages)
	if err != nil || p.Statement != "DELETE msdb.dbo.suspect_pages WHERE database_id = DB_ID(N'WarehouseLive');" || p.Rollback != "" || len(p.Changes) != 2 ||
		p.Changes[1] != (Drift{"File 1, page 7701", "823/824 error, 3 error(s), last 2026-10-30 14:05", "cleared"}) || fix.Current != "2 record(s)" || p.Database != "" {
		t.Errorf("clear: %v %+v %+v", err, p, fix)
	}
	if !bakHasWarning(p, suspectNoRollback) || p.Warnings[len(p.Warnings)-1] != "Needs DELETE on msdb.dbo.suspect_pages (sysadmin, or db_owner of msdb)." {
		t.Errorf("warnings %q", p.Warnings)
	}
	after, ok := SuspectPagesReadBack(pages, nil)
	if !ok || after != "Removed 2 record(s): file 1 page 153 (bad checksum, 1 error(s), last 2026-10-29 08:00); file 1 page 7701 (823/824 error, 3 error(s), last 2026-10-30 14:05). None left." {
		t.Errorf("read back %q", after)
	}
	if after, ok := SuspectPagesReadBack(pages, pages[:1]); ok || !strings.HasSuffix(after, "Still there: 1 record(s).") {
		t.Errorf("left %q", after)
	}
	if _, _, err := BuildSuspectPagesClearFix(f, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("no pages: %v", err)
	}
}
