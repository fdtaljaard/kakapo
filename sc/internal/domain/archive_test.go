package domain

import (
	"strings"
	"testing"
	"time"
)

// archiveFacts is Shop.dbo.MailLog: 200,000 rows over three years, an identity Id, an indexed
// LoggedAt, a computed column and a rowversion; the archive table archive.MailLog does not exist
// yet; FULL recovery with log backups; the Agent running.
func archiveFacts() ArchiveFacts {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	oldest := time.Date(2023, 11, 5, 8, 0, 0, 0, time.UTC)
	logBackup := time.Date(2026, 11, 4, 9, 45, 0, 0, time.UTC)
	free := int64(50000)
	return ArchiveFacts{
		Database: "Shop", Schema: "dbo", Table: "MailLog", ServerName: `SQL01`, ProductVersion: "16.0.4255.2",
		EngineEdition: 2, ServerNow: now, Found: true, Rows: 200000, MB: 120,
		Columns: []ArchiveColumn{
			{Name: "Id", Type: "int", Identity: true},
			{Name: "CreatedOn", Type: "datetime"},
			{Name: "LoggedAt", Type: "datetime2", DateIndexed: true},
			{Name: "Status", Type: "nvarchar", Nullable: true},
			{Name: "Body", Type: "nvarchar", Nullable: true},
			{Name: "Size", Type: "int", Computed: true},
			{Name: "RowVer", Type: "timestamp"},
		},
		RecoveryModel: "FULL", LastLogBackup: &logBackup, AgentStatus: "Running", FreeMB: &free,
		Jobs:     []ArchiveJob{},
		Target:   ArchiveTarget{Database: "Shop", Schema: "archive", Table: "MailLog", Read: true},
		Estimate: ArchiveEstimate{Known: true, Rows: 120000, Oldest: &oldest},
	}
}

func inputByName(p FixPreview, name string) (FixInput, bool) {
	for _, in := range p.Inputs {
		if in.Name == name {
			return in, true
		}
	}
	return FixInput{}, false
}

func TestArchiveDefaultPreview(t *testing.T) {
	f := archiveFacts()
	p, fix := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if len(p.Changes) != 4 || p.Statement == "" || p.Rollback == "" {
		t.Fatalf("no changes: %+v", p.Warnings)
	}
	// SCOPE rule 5: the four that change the decision up front, the rest under More options.
	want := []struct {
		name, value       string
		advanced, readOnl bool
	}{
		{OptionTable, "[dbo].[MailLog]", false, true},
		{OptionArchiveDateColumn, "LoggedAt (indexed)", false, false},
		{OptionArchiveKeepMonths, "12", false, false},
		{OptionArchiveAction, "archive", false, false},
		{OptionArchiveName, "schema", true, false},
		{OptionArchiveTo, "same database", true, false},
		{OptionArchiveCondition, "", true, false},
		{OptionArchiveSchedule, "02:30", true, false},
		{OptionArchiveBatchRows, "5000", true, false},
		{OptionArchiveMaxMinutes, "30", true, false},
		{OptionArchiveTidyUp, "false", true, false},
		{OptionArchiveFirstRun, "nothing", true, false},
	}
	if len(p.Inputs) != len(want) {
		t.Fatalf("%d inputs", len(p.Inputs))
	}
	for i, w := range want {
		in := p.Inputs[i]
		if in.Name != w.name || in.Value != w.value || in.Advanced != w.advanced || in.ReadOnly != w.readOnl || in.Warning != "" {
			t.Errorf("input %d: %+v, want %+v", i, in, w)
		}
	}
	if in, _ := inputByName(p, OptionArchiveDateColumn); strings.Join(in.Options, "|") != "LoggedAt (indexed)|CreatedOn" {
		t.Errorf("date columns %v: the indexed first and marked", in.Options)
	}
	if p.Options[OptionArchiveDateColumn] != "LoggedAt" || p.Options[OptionTable] != "[dbo].[MailLog]" {
		t.Errorf("options %v", p.Options)
	}
	changes := []Drift{
		{"Table", "200,000 rows, 120 MB, oldest 2023-11-05", "rows older than 12 months leave nightly"},
		{"Destination", "none", "archive.MailLog in Shop"},
		{"Rows affected now", "", "about 120,000 (counted), 1 night at 5,000 rows per statement and 30 minutes a night"},
		{"Job", "none", "kakapo: archive Shop.dbo.MailLog, daily 02:30"},
	}
	for i, c := range changes {
		if p.Changes[i] != c {
			t.Errorf("change %d: %+v, want %+v", i, p.Changes[i], c)
		}
	}
	if fix.ID != FixArchiveTable || fix.CheckID != "IDX10" || fix.Current != "no archive job" || fix.Options[OptionTable] != "[dbo].[MailLog]" {
		t.Errorf("fix %+v", fix)
	}
	for _, s := range []string{
		"IF OBJECT_ID(N'[Shop].[archive].[MailLog]', N'U') IS NOT NULL",
		"IF NOT EXISTS (SELECT 1 FROM [Shop].sys.schemas WHERE name = N'archive')",
		"EXEC [Shop].sys.sp_executesql N'CREATE SCHEMA [archive] AUTHORIZATION [dbo];';",
		"SELECT TOP (0) [Id], [CreatedOn], [LoggedAt], [Status], [Body] INTO [Shop].[archive].[MailLog] FROM [Shop].[dbo].[MailLog];",
		"ALTER TABLE [Shop].[archive].[MailLog] ADD [ArchivedAt] datetime2 NOT NULL DEFAULT SYSDATETIME();",
		"CREATE CLUSTERED INDEX [CX_kakapo_archive] ON [Shop].[archive].[MailLog] ([LoggedAt]) WITH (DATA_COMPRESSION = PAGE);",
		"@category_name = N'Database Maintenance', @owner_login_name = @owner",
		"@description = N'Created by kakapo on 2026-11-04 for Shop: rows of dbo.MailLog older than 12 months moved to archive.MailLog in Shop, daily 02:30'",
		"@step_name = N'Archive old rows'",
		"@freq_type = 4, @freq_interval = 1, @active_start_time = 23000;",
		"THROW 50000, N'A job named kakapo: archive Shop.dbo.MailLog already exists', 1;",
	} {
		if !strings.Contains(p.Statement, s) {
			t.Errorf("statement lacks %q", s)
		}
	}
	if strings.Contains(p.Statement, "Tidy up") || strings.Contains(p.Statement, "one batch now") {
		t.Error("tidy-up step or first batch without asking")
	}
	if p.Rollback != "-- The archive table archive.MailLog in Shop and its rows stay: drop it by hand when nobody needs them.\nEXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: archive Shop.dbo.MailLog';" {
		t.Errorf("rollback %q", p.Rollback)
	}
	last := p.Warnings[len(p.Warnings)-1]
	if last != "This deletes application data. Confirm with whoever owns the application that rows older than 12 months are not needed (reports, integrations, audits by law)." {
		t.Errorf("last warning %q", last)
	}
}

// The exact job step for an archive with an identity column and a condition.
func TestArchiveStepWithIdentityAndCondition(t *testing.T) {
	f := archiveFacts()
	p, _ := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveCondition: "status = 'Sent'"})
	plan, _, _, _ := f.archivePlan(map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveCondition: "status = 'Sent'"})
	want := `/* kakapo */
/* kakapo-archive {"database":"Shop","schema":"dbo","table":"MailLog","dateColumn":"LoggedAt","keepMonths":12,"action":"archive","archiveName":"schema","archiveTo":"same database","targetDatabase":"Shop","targetSchema":"archive","targetTable":"MailLog","condition":"[Status] = 'Sent'","schedule":"02:30","batchRows":5000,"maxMinutes":30,"tidyUp":false,"created":"2026-11-04"} */
SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;
DECLARE @cutoff datetime2 = DATEADD(MONTH, -12, SYSDATETIME()), @stop datetime2 = DATEADD(MINUTE, 30, SYSDATETIME()), @moved bigint = 0, @n int;
WHILE SYSDATETIME() < @stop
BEGIN
    BEGIN TRANSACTION;
    SET IDENTITY_INSERT [Shop].[archive].[MailLog] ON;
    WITH batch AS (SELECT TOP (5000) * FROM [Shop].[dbo].[MailLog] WITH (READPAST, UPDLOCK, ROWLOCK) WHERE [LoggedAt] < @cutoff AND ([Status] = 'Sent'))
    DELETE FROM batch OUTPUT deleted.[Id], deleted.[CreatedOn], deleted.[LoggedAt], deleted.[Status], deleted.[Body] INTO [Shop].[archive].[MailLog] ([Id], [CreatedOn], [LoggedAt], [Status], [Body]);
    SET @n = @@ROWCOUNT;
    SET IDENTITY_INSERT [Shop].[archive].[MailLog] OFF;
    COMMIT;
    SET @moved += @n;
    IF @n = 0 BREAK;
    WAITFOR DELAY '00:00:01';
END;
PRINT 'rows moved: ' + CONVERT(varchar(20), @moved);`
	if got := plan.StepCommand(); got != want {
		t.Errorf("step:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(p.Statement, nstr(want)) {
		t.Error("the statement does not carry the step")
	}
	if p.Changes[0].Target != "rows older than 12 months where [Status] = 'Sent' leave nightly" {
		t.Errorf("table row %q", p.Changes[0].Target)
	}
	if in, _ := inputByName(p, OptionArchiveCondition); in.Value != "[Status] = 'Sent'" || in.Warning != "" {
		t.Errorf("condition input %+v", in)
	}
}

func TestArchiveDeleteAction(t *testing.T) {
	f := archiveFacts()
	p, _ := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveAction: "delete", OptionArchiveKeepMonths: "6"})
	if len(p.Changes) != 4 {
		t.Fatalf("blocked: %v", p.Warnings)
	}
	for _, bad := range []string{"OUTPUT", "IDENTITY_INSERT", "SELECT TOP (0)", "CREATE SCHEMA", "OBJECT_ID("} {
		if strings.Contains(p.Statement, bad) {
			t.Errorf("delete statement has %q", bad)
		}
	}
	if !strings.Contains(p.Statement, "DELETE FROM batch;") || !strings.Contains(p.Statement, "@step_name = N'Delete old rows'") {
		t.Error("delete batch missing")
	}
	sentence := "This deletes application data and keeps no copy. Confirm with whoever owns the application that rows older than 6 months are not needed (reports, integrations, audits by law)."
	if p.Warnings[len(p.Warnings)-1] != sentence {
		t.Errorf("last warning %q", p.Warnings[len(p.Warnings)-1])
	}
	if in, _ := inputByName(p, OptionArchiveAction); in.Warning != sentence || in.Value != "delete" {
		t.Errorf("action input %+v: the red warning", in)
	}
	if p.Changes[1] != (Drift{"Destination", "none", "deleted, not kept"}) {
		t.Errorf("destination %+v", p.Changes[1])
	}
	if p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: archive Shop.dbo.MailLog';" {
		t.Errorf("rollback %q", p.Rollback)
	}
	for _, w := range p.Warnings {
		if strings.Contains(w, "IDENTITY_INSERT") || strings.Contains(w, "archive copy") || strings.Contains(w, "Left out") {
			t.Errorf("an archive warning in the delete case: %q", w)
		}
	}
}

// Without an identity column there is no IDENTITY_INSERT; with tidyUp a second step; with the
// first run one batch after the marker.
func TestArchiveStepVariants(t *testing.T) {
	f := archiveFacts()
	f.Columns[0].Identity = false
	p, _ := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveTidyUp: "true",
		OptionArchiveFirstRun: ArchiveFirstRunOneBatch, OptionArchiveBatchRows: "1000", OptionArchiveMaxMinutes: "60", OptionArchiveSchedule: "3:15"})
	if len(p.Changes) != 4 {
		t.Fatalf("blocked: %v", p.Warnings)
	}
	create, first := SplitArchiveStatement(p.Statement)
	if strings.Contains(create, "IDENTITY_INSERT") || strings.Contains(first, "IDENTITY_INSERT") {
		t.Error("IDENTITY_INSERT without an identity column")
	}
	for _, s := range []string{"@step_name = N'Tidy up indexes and statistics'",
		nstr("/* kakapo */\nALTER INDEX ALL ON [Shop].[dbo].[MailLog] REORGANIZE;\nUPDATE STATISTICS [Shop].[dbo].[MailLog];"),
		"@on_success_action = 3", "@active_start_time = 31500;", "DATEADD(MINUTE, 60, SYSDATETIME())", "SELECT TOP (1000) *"} {
		if !strings.Contains(create, s) {
			t.Errorf("creation lacks %q", s)
		}
	}
	wantFirst := `SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;
DECLARE @cutoff datetime2 = DATEADD(MONTH, -12, SYSDATETIME()), @n int;
BEGIN TRANSACTION;
WITH batch AS (SELECT TOP (1000) * FROM [Shop].[dbo].[MailLog] WITH (READPAST, UPDLOCK, ROWLOCK) WHERE [LoggedAt] < @cutoff)
DELETE FROM batch OUTPUT deleted.[Id], deleted.[CreatedOn], deleted.[LoggedAt], deleted.[Status], deleted.[Body] INTO [Shop].[archive].[MailLog] ([Id], [CreatedOn], [LoggedAt], [Status], [Body]);
SET @n = @@ROWCOUNT;
COMMIT;
PRINT 'rows moved: ' + CONVERT(varchar(20), @n);`
	if first != wantFirst {
		t.Errorf("first batch:\n%s", first)
	}
	if p.Changes[3].Target != "kakapo: archive Shop.dbo.MailLog, daily 03:15, one batch now" {
		t.Errorf("job row %q", p.Changes[3].Target)
	}
	plan, err := ArchivePlanFromPreview("Shop", p)
	if err != nil || !plan.FirstRun || !plan.TidyUp || plan.Schedule != "03:15" || plan.BatchRows != 1000 || plan.JobName() != "kakapo: archive Shop.dbo.MailLog" ||
		plan.TargetWords() != "archive.MailLog in Shop" {
		t.Errorf("plan from preview %+v %v", plan, err)
	}
	if d := plan.ChangeDescription(); d != "Archiving dbo.MailLog: rows older than 12 months move nightly to archive.MailLog in Shop, by the job kakapo: archive Shop.dbo.MailLog (daily 03:15, 1000 rows per statement, 60 minutes a night)" {
		t.Errorf("description %q", d)
	}
}

// Every blocking case: no changes, no statement, the reason first.
func TestArchiveBlocks(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*ArchiveFacts)
		options map[string]string
		want    string
	}{
		{"foreign keys in", func(f *ArchiveFacts) {
			f.KeysIn = []ArchiveForeignKey{{Name: "FK_Reply_MailLog", FromTable: "dbo.Reply"}}
		}, nil, "dbo.Reply (FK_Reply_MailLog)"},
		{"CDC", func(f *ArchiveFacts) { f.CDC = true }, nil, "Change data capture"},
		{"change tracking", func(f *ArchiveFacts) { f.ChangeTracking = true }, nil, "Change tracking"},
		{"published", func(f *ArchiveFacts) { f.Published = true }, nil, "replicated"},
		{"merge", func(f *ArchiveFacts) { f.MergePublished = true }, nil, "replicated"},
		{"replicated", func(f *ArchiveFacts) { f.Replicated = true }, nil, "replicated"},
		{"temporal", func(f *ArchiveFacts) { f.Temporal = "SYSTEM_VERSIONED_TEMPORAL_TABLE" }, nil, "temporal table"},
		{"history", func(f *ArchiveFacts) { f.Temporal = "HISTORY_TABLE" }, nil, "history table"},
		{"memory-optimised", func(f *ArchiveFacts) { f.MemoryOptimized = true }, nil, "memory-optimised"},
		{"instead of delete", func(f *ArchiveFacts) {
			f.Triggers = []ArchiveTrigger{{Name: "trg_keep", Events: []string{"DELETE"}, Enabled: true, InsteadOf: true}}
		}, nil, "INSTEAD OF DELETE"},
		{"no date column", func(f *ArchiveFacts) { f.Columns = []ArchiveColumn{{Name: "Id", Type: "int"}} }, nil, "has no date column"},
		{"target with other columns", func(f *ArchiveFacts) {
			f.Target.Exists, f.Target.Columns = true, []ArchiveColumn{{Name: "Id", Type: "bigint"}, {Name: "Extra", Type: "int"}}
		}, nil, "already exists with other columns (Id is bigint, not int"},
		{"archive database missing", nil, map[string]string{OptionArchiveTo: "Shop_Archive"}, "There is no database named Shop_Archive"},
		{"already archived", func(f *ArchiveFacts) {
			f.Jobs = []ArchiveJob{{Name: "kakapo: archive Shop.dbo.MailLog"}}
		}, nil, "kakapo already archives dbo.MailLog"},
		{"express", func(f *ArchiveFacts) { f.EngineEdition = 4 }, nil, "Express has no SQL Server Agent"},
		{"columns unread", func(f *ArchiveFacts) { f.ColumnsError = "denied" }, nil, "Could not read the columns of dbo.MailLog"},
		{"keys unread", func(f *ArchiveFacts) { f.KeysError = "denied" }, nil, "Could not read the foreign keys"},
		{"condition refused by SQL Server", func(f *ArchiveFacts) {
			f.Estimate = ArchiveEstimate{Error: "Conversion failed"}
		}, map[string]string{OptionArchiveCondition: "Status = 1"}, "SQL Server did not accept the condition"},
		{"keep 0", nil, map[string]string{OptionArchiveKeepMonths: "0"}, archiveKeepWarn},
		{"keep 121", nil, map[string]string{OptionArchiveKeepMonths: "121"}, archiveKeepWarn},
		{"batch 499", nil, map[string]string{OptionArchiveBatchRows: "499"}, archiveBatchWarn},
		{"batch 50001", nil, map[string]string{OptionArchiveBatchRows: "50001"}, archiveBatchWarn},
		{"minutes 4", nil, map[string]string{OptionArchiveMaxMinutes: "4"}, archiveMinutesWarn},
		{"minutes 241", nil, map[string]string{OptionArchiveMaxMinutes: "241"}, archiveMinutesWarn},
		{"schedule", nil, map[string]string{OptionArchiveSchedule: "25:00"}, archiveScheduleWarn},
		{"condition", nil, map[string]string{OptionArchiveCondition: "Id IN (SELECT 1)"}, "The condition: "},
		{"action", nil, map[string]string{OptionArchiveAction: "truncate"}, archiveActionWarn},
		{"archive name", nil, map[string]string{OptionArchiveName: "elsewhere"}, archiveNameWarn},
		{"archive to", nil, map[string]string{OptionArchiveTo: "Other"}, "Choose the same database or Shop_Archive."},
		{"date column", nil, map[string]string{OptionArchiveDateColumn: "Status"}, "Pick one of the table's date columns."},
		{"first run", nil, map[string]string{OptionArchiveFirstRun: "later"}, archiveFirstRunWarn},
	}
	for _, c := range cases {
		f := archiveFacts()
		if c.edit != nil {
			c.edit(&f)
		}
		options := map[string]string{OptionTable: "[dbo].[MailLog]"}
		for k, v := range c.options {
			options[k] = v
		}
		p, _ := BuildArchiveFix(f, options)
		if len(p.Changes) != 0 || p.Statement != "" || p.Rollback != "" {
			t.Errorf("%s: not blocked", c.name)
			continue
		}
		if len(p.Warnings) == 0 || !strings.Contains(p.Warnings[0], c.want) {
			t.Errorf("%s: first warning %q, want %q", c.name, first(p.Warnings), c.want)
		}
	}
	// A bad advanced option opens More options: its input carries the warning.
	p, _ := BuildArchiveFix(archiveFacts(), map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveBatchRows: "10"})
	if in, _ := inputByName(p, OptionArchiveBatchRows); !in.Advanced || in.Warning != archiveBatchWarn || in.Value != "10" {
		t.Errorf("batch input %+v", in)
	}
	// Delete does not need the archive database.
	p, _ = BuildArchiveFix(archiveFacts(), map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveAction: "delete", OptionArchiveTo: "Shop_Archive"})
	if len(p.Changes) == 0 {
		t.Errorf("delete blocked by the archive database: %v", p.Warnings)
	}
}

func first(ws []string) string {
	if len(ws) == 0 {
		return ""
	}
	return ws[0]
}

// Every warning (none of them blocks).
func TestArchiveWarnings(t *testing.T) {
	f := archiveFacts()
	f.Triggers = []ArchiveTrigger{{Name: "trg_audit", Events: []string{"DELETE", "UPDATE"}, Enabled: true},
		{Name: "trg_off", Events: []string{"DELETE"}}, {Name: "trg_ins", Events: []string{"INSERT"}, Enabled: true}}
	f.KeysOut = 2
	f.AgentStatus = "Stopped"
	p, _ := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if len(p.Changes) == 0 {
		t.Fatalf("blocked: %v", p.Warnings)
	}
	for _, w := range []string{
		"The trigger trg_audit on dbo.MailLog fires on DELETE: a delete trigger fires for every batch; the application may be counting on it.",
		"The identity column Id keeps its values in the archive (IDENTITY_INSERT)",
		"Left out of the archive: Size, RowVer (computed or rowversion columns",
		"dbo.MailLog has 2 foreign key(s) to other tables: deleting its rows is fine, and the archive copy does not carry them.",
		"The archive copy needs about 72 MB in Shop (50,000 MB free on its drive).",
		"Shop is in FULL recovery: each night's batches write about 144 MB to the log, which only a log backup frees. The last log backup was at 2026-11-04 09:45",
		"SQL Server Agent is stopped (Stopped): the job will not run until it starts (MNT01).",
		"Needs sysadmin on SQL01",
	} {
		if !hasWarning(p, w) {
			t.Errorf("no warning %q in %v", w, p.Warnings)
		}
	}
	if hasWarning(p, "trg_off") || hasWarning(p, "trg_ins") {
		t.Error("a disabled or insert-only trigger is named")
	}

	// The date column not indexed: each batch scans, with the create-index fix named; the oldest row
	// is not read; the nights at the scan pace (10 seconds a batch: 180 batches of 5,000 a night).
	f = archiveFacts()
	f.Columns[2].DateIndexed = false
	f.Estimate = ArchiveEstimate{Known: true, Rows: 2000000, Estimated: true}
	f.Rows, f.MB = 3000000, 1800
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveDateColumn: "LoggedAt"})
	if !hasWarning(p, "LoggedAt is not the first column of an index, so each batch scans the table: create an index on LoggedAt first (\"Create the suggested index\"") {
		t.Errorf("no index warning: %v", p.Warnings)
	}
	if p.Changes[0].Current != "3,000,000 rows, 1,800 MB, oldest not read (LoggedAt is not indexed)" ||
		p.Changes[2].Target != "about 2,000,000 (estimated from a 1% sample), 3 nights at 5,000 rows per statement and 30 minutes a night" {
		t.Errorf("changes %+v", p.Changes)
	}

	// Room: more than the drive has; unknown.
	f = archiveFacts()
	small := int64(50)
	f.FreeMB = &small
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if !hasWarning(p, "more than the 50 MB free on its drive") {
		t.Errorf("room: %v", p.Warnings)
	}
	f.FreeMB, f.RoomError = nil, "the login does not have VIEW SERVER STATE."
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if !hasWarning(p, "the drive's free space could not be read (the login does not have VIEW SERVER STATE.)") {
		t.Errorf("room unknown: %v", p.Warnings)
	}

	// The log: no log backup; unreadable history; SIMPLE says nothing.
	f = archiveFacts()
	f.LastLogBackup = nil
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if !hasWarning(p, "There is no log backup in the history: create the log backup job first (BAK02)") {
		t.Errorf("no log backup: %v", p.Warnings)
	}
	f.LogBackupError = "denied"
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if !hasWarning(p, "The log backup history could not be read (denied)") {
		t.Errorf("log history: %v", p.Warnings)
	}
	f.RecoveryModel = "SIMPLE"
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if hasWarning(p, "recovery: each night") {
		t.Error("log warning in SIMPLE recovery")
	}

	// The archive database: chosen by default when it exists, its own drive's room, the log once.
	f = archiveFacts()
	archFree := int64(9000)
	f.ArchiveDBExists, f.ArchiveFreeMB = true, &archFree
	f.Target = ArchiveTarget{Database: "Shop_Archive", Schema: "archive", Table: "MailLog", Read: true}
	if tgt := ArchiveTargetFor(f, nil); tgt.Database != "Shop_Archive" || tgt.Schema != "archive" || tgt.Table != "MailLog" {
		t.Errorf("target %+v", tgt)
	}
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if len(p.Changes) == 0 || p.Changes[1].Target != "archive.MailLog in Shop_Archive" || !hasWarning(p, "in Shop_Archive (9,000 MB free") ||
		!hasWarning(p, "about 72 MB to the log") {
		t.Errorf("archive database: %+v %v", p.Changes, p.Warnings)
	}
	if !strings.Contains(p.Statement, "SELECT TOP (0) [Id], [CreatedOn], [LoggedAt], [Status], [Body] INTO [Shop_Archive].[archive].[MailLog] FROM [Shop].[dbo].[MailLog];") {
		t.Error("the copy does not go to the archive database")
	}
	// The other names.
	for name, want := range map[string]string{ArchiveNameSuffix: "dbo.MailLog_Archive", ArchiveNamePrefix: "dbo.Archive_MailLog"} {
		g := archiveFacts()
		tgt := ArchiveTargetFor(g, map[string]string{OptionArchiveName: name})
		g.Target = ArchiveTarget{Database: tgt.Database, Schema: tgt.Schema, Table: tgt.Table, Read: true}
		p, _ = BuildArchiveFix(g, map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveName: name})
		if len(p.Changes) == 0 || p.Changes[1].Target != want+" in Shop" || strings.Contains(p.Statement, "CREATE SCHEMA") {
			t.Errorf("%s: %+v", name, p.Changes)
		}
	}
	// SQL Server 2014 Standard cannot compress.
	f = archiveFacts()
	f.ProductVersion = "12.0.6024.0"
	p, _ = BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if strings.Contains(p.Statement, "DATA_COMPRESSION") {
		t.Error("PAGE compression on 2014 Standard")
	}
}

// An existing archive table with the same columns (and ArchivedAt) is written into, not created.
func TestArchiveIntoExistingTable(t *testing.T) {
	f := archiveFacts()
	f.Target.Exists, f.Target.Rows = true, 4000
	f.Target.Columns = []ArchiveColumn{{Name: "Id", Type: "int", Identity: true}, {Name: "CreatedOn", Type: "datetime"},
		{Name: "LoggedAt", Type: "datetime2"}, {Name: "Status", Type: "nvarchar"}, {Name: "Body", Type: "nvarchar"}, {Name: "ArchivedAt", Type: "datetime2"}}
	p, _ := BuildArchiveFix(f, map[string]string{OptionTable: "[dbo].[MailLog]"})
	if len(p.Changes) == 0 {
		t.Fatalf("blocked: %v", p.Warnings)
	}
	if strings.Contains(p.Statement, "SELECT TOP (0)") || strings.Contains(p.Statement, "OBJECT_ID(") {
		t.Error("an existing archive table is created again")
	}
	if p.Changes[1] != (Drift{"Destination", "archive.MailLog in Shop (exists, 4,000 rows)", "archive.MailLog in Shop, added to"}) {
		t.Errorf("destination %+v", p.Changes[1])
	}
}

// The options line reads back what the step was built with, comment markers in names included.
func TestArchiveOptionsLineRoundTrip(t *testing.T) {
	f := archiveFacts()
	plan, _, _, _ := f.archivePlan(map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveTidyUp: "true", OptionArchiveKeepMonths: "24"})
	got, ok := ParseArchiveOptionsLine(plan.StepCommand())
	if !ok || got != plan.Options() || got.KeepMonths != 24 || !got.TidyUp {
		t.Errorf("read back %+v %v, want %+v", got, ok, plan.Options())
	}
	plan.Table = "Odd*/Name/*x"
	line := plan.Options().Line()
	if strings.Count(line, "*/") != 1 || strings.Count(line, "/*") != 1 {
		t.Errorf("comment markers inside the line: %s", line)
	}
	if got, ok := ParseArchiveOptionsLine("x\n" + line + "\nSELECT 1;"); !ok || got.Table != "Odd*/Name/*x" {
		t.Errorf("escaped name read back %+v %v", got, ok)
	}
	for _, bad := range []string{"", "/* kakapo */ SELECT 1;", "/* kakapo-archive {not json} */", `/* kakapo-archive {"database":"Shop"} */`} {
		if _, ok := ParseArchiveOptionsLine(bad); ok {
			t.Errorf("%q read as options", bad)
		}
	}
}

// The remove fix's rollback creates the job again as the archive fix created it.
func TestRemoveArchiveRollbackRoundTrip(t *testing.T) {
	f := archiveFacts()
	f.Target.Exists = true
	f.Target.Columns = []ArchiveColumn{{Name: "Id", Type: "int", Identity: true}, {Name: "CreatedOn", Type: "datetime"},
		{Name: "LoggedAt", Type: "datetime2"}, {Name: "Status", Type: "nvarchar"}, {Name: "Body", Type: "nvarchar"}, {Name: "ArchivedAt", Type: "datetime2"}}
	options := map[string]string{OptionTable: "[dbo].[MailLog]", OptionArchiveTidyUp: "true", OptionArchiveCondition: "Status = 'Sent'"}
	created, _ := BuildArchiveFix(f, options)
	plan, _, _, _ := f.archivePlan(options)
	cmd := plan.StepCommand()
	o, ok := ParseArchiveOptionsLine(cmd)
	moved, archived := int64(5000), int64(196000)
	ran := time.Date(2026, 11, 4, 2, 30, 0, 0, time.UTC)
	jobs := []ArchiveJob{{Name: plan.JobName(), Enabled: true, ScheduleEnabled: true, Clock: "02:30", Command: cmd, Options: o, OptionsOK: ok,
		LastRunAt: &ran, LastOutcome: "succeeded", LastRowsMoved: &moved, RowsArchived: &archived}}
	p, fix, err := BuildRemoveArchiveFix(jobs, "Shop", "dbo", "MailLog", "SQL01", "16.0.4255.2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Rollback != created.Statement {
		t.Errorf("rollback:\n%s\nwant:\n%s", p.Rollback, created.Statement)
	}
	if p.Statement != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: archive Shop.dbo.MailLog';" || fix.ID != FixRemoveArchive {
		t.Errorf("statement %q", p.Statement)
	}
	wantChanges := []Drift{
		{"Job", "kakapo: archive Shop.dbo.MailLog, daily 02:30, rows older than 12 months where [Status] = 'Sent' to archive.MailLog in Shop", "deleted"},
		{"Last run", "2026-11-04 02:30 (succeeded, 5,000 rows moved)", "no more runs"},
		{"Archive table", "archive.MailLog in Shop, 196,000 rows", "kept"},
	}
	for i, c := range wantChanges {
		if i >= len(p.Changes) || p.Changes[i] != c {
			t.Errorf("change %d: %+v", i, p.Changes)
		}
	}
	if p.Warnings[0] != "The archive table archive.MailLog and its 196,000 rows stay; drop it by hand when nobody needs them." {
		t.Errorf("warning %q", p.Warnings[0])
	}
	if p.Options[OptionTable] != "[dbo].[MailLog]" {
		t.Errorf("options %v", p.Options)
	}
	if _, _, err := BuildRemoveArchiveFix(jobs, "Shop", "dbo", "Other", "SQL01", ""); err != ErrNoArchiveJob {
		t.Errorf("no job: %v", err)
	}
	// Without its options line the job is still removed, and the preview says the rollback cannot rebuild it.
	jobs[0].OptionsOK, jobs[0].Options = false, ArchiveOptions{}
	p, _, err = BuildRemoveArchiveFix(jobs, "Shop", "dbo", "MailLog", "SQL01", "")
	if err != nil || p.Rollback != "" || !hasWarning(p, "options line could not be read") || p.Statement == "" {
		t.Errorf("no options line: %v %+v", err, p)
	}
	if text, ok := RemoveArchiveReadBack(plan.JobName(), nil); !ok || text != "the job kakapo: archive Shop.dbo.MailLog is gone" {
		t.Errorf("read back %q", text)
	}
	if _, ok := RemoveArchiveReadBack(plan.JobName(), []BackupJobState{{Name: plan.JobName()}}); ok {
		t.Error("a job still there read back as gone")
	}
}

func TestArchiveReadBack(t *testing.T) {
	f := archiveFacts()
	plan, _, _, _ := f.archivePlan(map[string]string{OptionTable: "[dbo].[MailLog]"})
	states := []BackupJobState{{Name: plan.JobName(), Enabled: true, ScheduleEnabled: true}}
	moved := int64(5000)
	text, ok := ArchiveReadBack(plan, states, ArchiveTarget{Exists: true}, &moved, "")
	if !ok || text != "kakapo: archive Shop.dbo.MailLog: enabled, next run not set by Agent yet; archive.MailLog in Shop exists; the first batch moved 5,000 rows." {
		t.Errorf("%q %v", text, ok)
	}
	if text, ok := ArchiveReadBack(plan, states, ArchiveTarget{}, nil, "Lock request time out period exceeded."); ok ||
		!strings.Contains(text, "was not found") || !strings.Contains(text, "the first batch did not run: Lock request") {
		t.Errorf("%q %v", text, ok)
	}
	if _, ok := ArchiveReadBack(plan, nil, ArchiveTarget{Exists: true}, nil, ""); ok {
		t.Error("a missing job verified")
	}
}

// IDX10 does not fire for a table kakapo archives: a note for information whose evidence says how;
// the other tables carry the archive fix with their table and indexed date column.
func TestIDX10ArchivedTable(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	ran := time.Date(2026, 11, 4, 2, 30, 0, 0, time.UTC)
	f := StorageFacts{Database: "Shop", State: "ONLINE", ServerNow: now,
		GrowingTables: []GrowingTable{
			{Schema: "dbo", Name: "MailLog", Rows: 2000000, MB: 900, DateCol: "LoggedAt", DateIndexed: true},
			{Schema: "dbo", Name: "AuditLog", Rows: 3000000, MB: 1200, DateCol: "At", DateIndexed: true},
			{Schema: "dbo", Name: "EventLog", Rows: 3000000, MB: 1200, DateCol: "At"},
			{Schema: "dbo", Name: "TraceLog", Rows: 3000000, MB: 1200},
		},
		Archives: []ArchiveJob{{Name: "kakapo: archive Shop.dbo.MailLog", OptionsOK: true, Enabled: true, LastRunAt: &ran, LastOutcome: "succeeded",
			Options: ArchiveOptions{Database: "Shop", Schema: "dbo", Table: "MailLog", KeepMonths: 12, Action: ArchiveActionArchive,
				TargetDatabase: "Shop", TargetSchema: "archive", TargetTable: "MailLog", Schedule: "02:30"}}},
	}
	b := &storageBuild{f: f, findings: []Finding{}, skipped: map[string]string{}}
	b.idx10()
	if len(b.findings) != 4 {
		t.Fatalf("%d findings", len(b.findings))
	}
	byObject := map[string]Finding{}
	for _, x := range b.findings {
		byObject[x.Object] = x
	}
	mail := byObject["[dbo].[MailLog]"]
	if mail.Severity.Actionable() || mail.FixID != "" ||
		mail.Evidence != "Rows: 2,000,000. Size: 900 MB. Archived nightly, 12 months kept, last run 2026-11-04 02:30 (succeeded)." {
		t.Errorf("archived table %+v", mail)
	}
	if x := byObject["[dbo].[AuditLog]"]; !x.Severity.Actionable() || x.FixID != FixArchiveTable {
		t.Errorf("audit %+v", x)
	}
	if x := byObject["[dbo].[TraceLog]"]; x.FixID != "" {
		t.Errorf("a table without a date column carries the fix: %+v", x)
	}
	if fact := b.fourthFact(); fact.Value != "3, biggest dbo.AuditLog 1,200 MB" {
		t.Errorf("fourth fact %+v: the archived table is not counted", fact)
	}
	fixes := ArchiveFixesFor(f, b.findings)
	if len(fixes) != 3 {
		t.Fatalf("fixes %+v", fixes)
	}
	if fixes[0].ID != FixArchiveTable || fixes[0].Options[OptionTable] != "[dbo].[AuditLog]" || fixes[0].Options[OptionArchiveDateColumn] != "At" {
		t.Errorf("archive entry %+v", fixes[0])
	}
	if _, ok := fixes[1].Options[OptionArchiveDateColumn]; ok || fixes[1].Options[OptionTable] != "[dbo].[EventLog]" {
		t.Errorf("not indexed: no date column option %+v", fixes[1])
	}
	r := fixes[2]
	if r.ID != FixRemoveArchive || r.Options[OptionTable] != "[dbo].[MailLog]" || r.Options["keepMonths"] != "12" ||
		r.Options["destination"] != "archive.MailLog in Shop" || r.Options["lastRun"] != "2026-11-04 02:30 (succeeded)" || r.Current != "daily 02:30, 12 months kept" {
		t.Errorf("remove entry %+v", r)
	}
}

func TestArchiveJobHelpers(t *testing.T) {
	if !IsArchiveJobName("kakapo: archive Shop.dbo.MailLog", "Shop") || IsArchiveJobName("kakapo: archive ShopX.dbo.MailLog", "Shop") {
		t.Error("job name")
	}
	if AgentDuration(10203) != 3723 || ArchiveRunOutcome(1) != "succeeded" || ArchiveRunOutcome(0) != "failed" {
		t.Error("history helpers")
	}
	ts := []TableListing{{Schema: "dbo", Name: "B", MB: 1}, {Schema: "dbo", Name: "A", MB: 9}, {Schema: "dbo", Name: "C", MB: 1}}
	SortTableList(ts)
	if ts[0].Name != "A" || ts[1].Name != "B" || ts[2].Name != "C" {
		t.Errorf("sorted %+v", ts)
	}
}
