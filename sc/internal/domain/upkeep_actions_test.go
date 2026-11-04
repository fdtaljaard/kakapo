package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func suAlertFacts() AlertFacts {
	return AlertFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2, MailProfiles: 1, AgentMailProfile: "Mail", AgentStatus: "Running"}
}

func TestOperatorAlertsCreatesEverything(t *testing.T) {
	f := suAlertFacts()
	// Without an email address nothing can be applied; the field says why.
	p, fix, err := BuildOperatorAlertsFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != emailMissing || len(p.Inputs) != 3 ||
		p.Inputs[0].Value != DefaultOperatorName || p.Inputs[1].Warning != emailMissing || p.Inputs[2].Kind != "bool" || p.Inputs[2].Value != "true" {
		t.Errorf("no email %+v", p)
	}
	if fix.ID != FixOperatorAlerts || fix.CheckID != "MNT07" || fix.Current != "0 operator(s) with email, 10 alert(s) missing" {
		t.Errorf("fix %+v", fix)
	}
	for _, bad := range []string{"not an address", "a@b", "x@y.co.za; y@z.com", "Jo <jo@x.com>"} {
		if p, _, _ := BuildOperatorAlertsFix(f, map[string]string{OptionOperatorEmail: bad}); len(p.Changes) != 0 || p.Inputs[1].Warning == "" {
			t.Errorf("email %q accepted", bad)
		}
	}

	p, _, err = BuildOperatorAlertsFix(f, map[string]string{OptionOperatorEmail: "dba@client.co.za"})
	if err != nil || len(p.Changes) != 11 {
		t.Fatalf("%+v %v", p, err)
	}
	if !reflect.DeepEqual(p.Changes[0], Drift{"Operator", "none with an email address", "Granite support (dba@client.co.za)"}) ||
		!reflect.DeepEqual(p.Changes[1], Drift{"Alert severity 19", "none", "kakapo: severity 19, mails Granite support"}) ||
		!reflect.DeepEqual(p.Changes[10], Drift{"Alert error 825", "none", "kakapo: error 825, mails Granite support"}) {
		t.Errorf("changes %+v", p.Changes)
	}
	for _, want := range []string{
		"BEGIN TRY\n    BEGIN TRANSACTION;\n",
		"EXEC msdb.dbo.sp_add_operator @name = N'Granite support', @enabled = 1, @email_address = N'dba@client.co.za';",
		"EXEC msdb.dbo.sp_add_alert @name = N'kakapo: severity 19', @message_id = 0, @severity = 19, @enabled = 1,\n        @delay_between_responses = 60, @include_event_description_in = 1;",
		"EXEC msdb.dbo.sp_add_alert @name = N'kakapo: error 824', @message_id = 824, @severity = 0, @enabled = 1,",
		"EXEC msdb.dbo.sp_add_notification @alert_name = N'kakapo: error 825', @operator_name = N'Granite support', @notification_method = 1;",
		"COMMIT TRANSACTION;", "IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n    THROW;",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Count(p.Statement, "sp_add_alert") != 10 || strings.Count(p.Statement, "sp_add_notification") != 10 {
		t.Errorf("ten alerts, ten notifications:\n%s", p.Statement)
	}
	// The rollback deletes what was created, by name: the alerts, then the operator (and with it its notifications).
	if !strings.HasPrefix(p.Rollback, "EXEC msdb.dbo.sp_delete_alert @name = N'kakapo: severity 19';") ||
		!strings.HasSuffix(p.Rollback, "EXEC msdb.dbo.sp_delete_operator @name = N'Granite support';") ||
		strings.Count(p.Rollback, "sp_delete_alert") != 10 || strings.Contains(p.Rollback, "sp_delete_notification") {
		t.Errorf("rollback:\n%s", p.Rollback)
	}
	if p.Warnings[len(p.Warnings)-1] != AlertsPermission(`SQL01\WMS`) || p.Options[OptionOperatorEmail] != "dba@client.co.za" || p.Options[OptionCreateAlerts] != "true" {
		t.Errorf("warnings %q options %+v", p.Warnings, p.Options)
	}
	if d := OperatorAlertsDescription(`SQL01\WMS`, p.Changes); d != `Created the operator Granite support (dba@client.co.za) on SQL01\WMS; 10 alert(s) created, 0 enabled again, 0 more now mail it` {
		t.Errorf("description %q", d)
	}

	// Database Mail not set up, the Agent stopped: warned.
	f.MailProfiles, f.AgentStatus = 0, "Stopped"
	p, _, _ = BuildOperatorAlertsFix(f, map[string]string{OptionOperatorEmail: "dba@client.co.za"})
	if !suContains(p.Warnings, "Alerts are created but no mail leaves this server until Database Mail is set up: copy this for IT.") ||
		!suContains(p.Warnings, "SQL Server Agent is stopped: alerts are raised only while it runs (MNT01).") {
		t.Errorf("mail warnings %q", p.Warnings)
	}
	f.MailProfiles, f.AgentMailProfile = 1, ""
	if p, _, _ := BuildOperatorAlertsFix(f, map[string]string{OptionOperatorEmail: "dba@client.co.za"}); !strings.Contains(strings.Join(p.Warnings, "|"), "until SQL Server Agent uses Database Mail") {
		t.Errorf("agent profile %q", p.Warnings)
	}

	// Only the operator.
	p, _, _ = BuildOperatorAlertsFix(suAlertFacts(), map[string]string{OptionOperatorEmail: "dba@client.co.za", OptionCreateAlerts: "false"})
	if len(p.Changes) != 1 || strings.Contains(p.Statement, "sp_add_alert") || p.Rollback != "EXEC msdb.dbo.sp_delete_operator @name = N'Granite support';" {
		t.Errorf("operator only %+v", p)
	}

	// An operator of that name without an email: choose another name.
	f = suAlertFacts()
	f.Operators = []AgentOperator{{Name: "Granite support", Enabled: true}}
	if p, _, _ := BuildOperatorAlertsFix(f, map[string]string{OptionOperatorEmail: "dba@client.co.za"}); len(p.Changes) != 0 ||
		!strings.HasPrefix(p.Inputs[0].Warning, "An operator named Granite support already exists without an email address") {
		t.Errorf("name taken %+v", p)
	}
}

func suContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// An operator exists: the alerts that are missing are created (a disabled one on the condition is
// enabled again), and the silent ones get an email notification.
func TestOperatorAlertsWithAnOperator(t *testing.T) {
	f := suAlertFacts()
	f.Operators = []AgentOperator{{Name: "Pager only", Enabled: true}, {Name: "IT", Email: "it@client.co.za", Enabled: true}}
	f.Alerts = []AgentAlert{
		{Name: "Severity 019", Severity: 19, Enabled: true},  // mails IT: fine
		{Name: "Severity 020", Severity: 20, Enabled: true},  // mails no one
		{Name: "Severity 021", Severity: 21, Enabled: false}, // disabled: enabled again
		{Name: "Sev 22", Severity: 22, Enabled: true},        // pages IT only
		{Name: "Error 823", MessageID: 823, Enabled: true},
		{Name: "Error 824", MessageID: 824, Enabled: true},
		{Name: "Error 825", MessageID: 825, Enabled: true},
		{Name: "Sev 25 Shop", Severity: 25, Enabled: false, Database: "Shop"}, // one database only: not enabled, a new one is made
		{Name: "Something else", Severity: 10, Enabled: true},
	}
	f.Notifications = []AgentNotification{{"Severity 019", "IT", 1}, {"Sev 22", "IT", 2}, {"Error 823", "IT", 1}, {"Error 824", "IT", 1},
		{"Error 825", "IT", 1}, {"Severity 020", "Pager only", 1}}
	p, fix, err := BuildOperatorAlertsFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fix.Current != "1 operator(s) with email, 4 alert(s) missing, 2 alert(s) mail no one" || p.Inputs[0].Kind != "select" ||
		!reflect.DeepEqual(p.Inputs[0].Options, []string{"IT"}) || p.Inputs[0].Value != "IT" || len(p.Inputs) != 2 {
		t.Errorf("fix %+v inputs %+v", fix, p.Inputs)
	}
	for _, want := range []string{
		"EXEC msdb.dbo.sp_add_alert @name = N'kakapo: severity 23'", "EXEC msdb.dbo.sp_add_alert @name = N'kakapo: severity 24'",
		"EXEC msdb.dbo.sp_add_alert @name = N'kakapo: severity 25'",
		"EXEC msdb.dbo.sp_update_alert @name = N'Severity 021', @enabled = 1;",
		"EXEC msdb.dbo.sp_add_notification @alert_name = N'Severity 021', @operator_name = N'IT', @notification_method = 1;",
		"EXEC msdb.dbo.sp_add_notification @alert_name = N'Severity 020', @operator_name = N'IT', @notification_method = 1;",
		"EXEC msdb.dbo.sp_update_notification @alert_name = N'Sev 22', @operator_name = N'IT', @notification_method = 3;",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Contains(p.Statement, "sp_add_operator") || strings.Contains(p.Statement, "Severity 019'") || strings.Count(p.Statement, "sp_add_alert") != 3 {
		t.Errorf("statement:\n%s", p.Statement)
	}
	for _, want := range []string{
		"EXEC msdb.dbo.sp_update_notification @alert_name = N'Sev 22', @operator_name = N'IT', @notification_method = 2;",
		"EXEC msdb.dbo.sp_delete_notification @alert_name = N'Severity 020', @operator_name = N'IT';",
		"EXEC msdb.dbo.sp_update_alert @name = N'Severity 021', @enabled = 0;",
		"EXEC msdb.dbo.sp_delete_alert @name = N'kakapo: severity 25';",
	} {
		if !strings.Contains(p.Rollback, want) {
			t.Errorf("rollback lacks %q:\n%s", want, p.Rollback)
		}
	}
	if strings.Contains(p.Rollback, "sp_delete_operator") {
		t.Errorf("the operator was not created: %s", p.Rollback)
	}
	if !reflect.DeepEqual(p.Changes[0], Drift{"Operator", "IT (it@client.co.za)", "mailed by the alerts"}) || !suContains(suSettings(p.Changes), "Alert Severity 020") ||
		!suContains(suSettings(p.Changes), "Alert Sev 22") || !suContains(suSettings(p.Changes), "Alert Severity 021") {
		t.Errorf("changes %+v", p.Changes)
	}

	// An operator the options name that has no email: choose one that has.
	if p, _, _ := BuildOperatorAlertsFix(f, map[string]string{OptionOperatorName: "Pager only"}); len(p.Changes) != 0 || p.Inputs[0].Warning == "" {
		t.Errorf("pager operator %+v", p)
	}
	// An alert named as kakapo's on another condition blocks.
	g := f
	g.Alerts = append(append([]AgentAlert(nil), f.Alerts...), AgentAlert{Name: "kakapo: severity 23", Severity: 1, Enabled: true})
	if p, _, _ := BuildOperatorAlertsFix(g, nil); len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "An alert named kakapo: severity 23 already exists") {
		t.Errorf("name taken %+v", p)
	}
	// Alerts not asked for and the operator there: nothing to create.
	if p, _, _ := BuildOperatorAlertsFix(f, map[string]string{OptionCreateAlerts: "false"}); len(p.Changes) != 0 || p.Warnings[0] != alertsNoChange {
		t.Errorf("nothing %+v", p)
	}

	// Read-back: what is still missing or silent.
	if s, ok := OperatorAlertsReadBack("IT", true, f); ok || !strings.Contains(s, "still missing: severity 21, severity 23, severity 24, severity 25") ||
		!strings.Contains(s, "still mailing no one: Sev 22, Severity 020") {
		t.Errorf("read back before %q", s)
	}
	done := AlertFacts{Operators: []AgentOperator{{Name: "IT", Email: "it@client.co.za", Enabled: true}}}
	for _, s := range kakapoAlerts {
		done.Alerts = append(done.Alerts, AgentAlert{Name: s.Name(), Severity: s.Severity, MessageID: s.Message, Enabled: true})
		done.Notifications = append(done.Notifications, AgentNotification{s.Name(), "IT", 1})
	}
	if s, ok := OperatorAlertsReadBack("IT", true, done); !ok || s != "operator IT (it@client.co.za); no alert missing; every alert mails someone" {
		t.Errorf("read back after %q", s)
	}
	if _, _, err := BuildOperatorAlertsFix(done, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("all there: %v", err)
	}
}

func suSettings(ds []Drift) []string {
	out := []string{}
	for _, d := range ds {
		out = append(out, d.Setting)
	}
	return out
}

func TestValidEmail(t *testing.T) {
	for s, want := range map[string]bool{"dba@client.co.za": true, "it.support@granite.io": true, "": false, "a@b": false,
		"a b@c.com": false, "a@b.com,c@d.com": false, "o'neil@x.com": false, "@x.com": false, "a@.com": false, strings.Repeat("a", 95) + "@x.com": false} {
		if ValidEmail(s) != want {
			t.Errorf("%q: %v", s, !want)
		}
	}
}

func suMaintFacts() MaintenanceJobFacts {
	return MaintenanceJobFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2, Database: "GraniteLive", Ola: true,
		Operator: "IT", AgentStatus: "Running", ServerNow: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
}

func TestMaintenanceJobWithOla(t *testing.T) {
	f := suMaintFacts()
	p, fix, err := BuildMaintenanceJobFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"DECLARE @owner sysname = SUSER_SNAME(0x01);",
		"IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'kakapo: statistics (GraniteLive)')",
		"EXEC msdb.dbo.sp_add_job @job_name = N'kakapo: statistics (GraniteLive)', @enabled = 1,\n        @description = N'Created by kakapo on 2026-11-03 for GraniteLive: statistics daily 01:00 (IndexOptimize, statistics only)',",
		"@category_name = N'Database Maintenance', @owner_login_name = @owner,\n        @notify_level_email = 2, @notify_email_operator_name = N'IT';",
		"@command = N'/* kakapo */\nEXECUTE master.dbo.IndexOptimize @Databases = N''GraniteLive'', @FragmentationLow = NULL, @FragmentationMedium = NULL, @FragmentationHigh = NULL, @UpdateStatistics = ''ALL'', @OnlyModifiedStatistics = ''Y'', @StatisticsModificationLevel = 5;';",
		"@freq_type = 4, @freq_interval = 1, @active_start_time = 10000;",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, p.Statement)
		}
	}
	if strings.Contains(p.Statement, "kakapo: indexes") || p.Rollback != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: statistics (GraniteLive)';" {
		t.Errorf("statistics only:\n%s\n%s", p.Statement, p.Rollback)
	}
	if len(p.Inputs) != 2 || p.Inputs[0].Value != "01:00" || p.Inputs[1].Name != OptionStatsOnly || p.Inputs[1].Value != "true" ||
		!suContains(p.Warnings, maintOla) || p.Warnings[len(p.Warnings)-1] != BackupFixPermission(`SQL01\WMS`) {
		t.Errorf("inputs %+v warnings %q", p.Inputs, p.Warnings)
	}
	if !reflect.DeepEqual(p.Changes, []Drift{{"Statistics job", "none", "kakapo: statistics (GraniteLive), daily 01:00"}, {"Tell on failure", "", "IT"}}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if fix.ID != FixMaintenanceJob || fix.CheckID != "MNT03" || fix.Target != "kakapo: statistics (GraniteLive) (daily 01:00)" ||
		MaintenanceJobDescription(fix) != "Created the maintenance job(s) kakapo: statistics (GraniteLive) (daily 01:00)" {
		t.Errorf("fix %+v", fix)
	}

	// With the weekly index job: statistics Monday to Saturday, indexes on Sunday.
	opts := map[string]string{OptionMaintTime: "2:30", OptionStatsOnly: "false"}
	p, _, _ = BuildMaintenanceJobFix(f, opts)
	for _, want := range []string{
		"@freq_type = 8, @freq_interval = 126, @freq_recurrence_factor = 1, @active_start_time = 23000;",
		"@freq_type = 8, @freq_interval = 1, @freq_recurrence_factor = 1, @active_start_time = 23000;",
		"@FragmentationMedium = ''INDEX_REORGANIZE'', @FragmentationHigh = ''INDEX_REBUILD_ONLINE,INDEX_REBUILD_OFFLINE'', @FragmentationLevel1 = 30, @FragmentationLevel2 = 50, @MinNumberOfPages = 1000",
		"N'kakapo: indexes (GraniteLive), Sunday 02:30'",
	} {
		if !strings.Contains(p.Statement, want) {
			t.Errorf("index job lacks %q:\n%s", want, p.Statement)
		}
	}
	if !reflect.DeepEqual(MaintenanceJobNames(f, opts), []string{"kakapo: statistics (GraniteLive)", "kakapo: indexes (GraniteLive)"}) ||
		!strings.Contains(p.Rollback, "N'kakapo: indexes (GraniteLive)'") || len(p.Changes) != 3 ||
		!suContains(p.Warnings, "Index rebuilds write a lot of log and, without Enterprise edition, rebuild offline and block users of the table while they run: check that Sunday 02:30 is a quiet hour and that the log has room.") {
		t.Errorf("index job %+v", p)
	}

	// A wrong time: nothing to apply, the field says why.
	if p, _, _ := BuildMaintenanceJobFix(f, map[string]string{OptionMaintTime: "25:00"}); len(p.Changes) != 0 || p.Statement != "" ||
		p.Inputs[0].Warning != maintTimeWarn || p.Inputs[0].Value != "25:00" {
		t.Errorf("bad time %+v", p)
	}
	// Both jobs exist: nothing to offer. The statistics job exists: only the index job, on request.
	f.ExistingJobs = []string{StatisticsJobName("GraniteLive"), IndexJobName("GraniteLive")}
	if _, _, err := BuildMaintenanceJobFix(f, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("both exist: %v", err)
	}
	f.ExistingJobs = []string{StatisticsJobName("GraniteLive")}
	if p, _, _ := BuildMaintenanceJobFix(f, nil); len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "kakapo's statistics job exists already") {
		t.Errorf("stats exists %+v", p)
	}
	if p, _, _ := BuildMaintenanceJobFix(f, map[string]string{OptionStatsOnly: "false"}); len(p.Changes) != 2 || !strings.Contains(p.Statement, "kakapo: indexes") ||
		strings.Contains(p.Statement, "sp_add_job @job_name = N'kakapo: statistics") {
		t.Errorf("index only %+v", p)
	}
}

func TestMaintenanceJobWithoutOla(t *testing.T) {
	f := suMaintFacts()
	f.Ola, f.Operator, f.AgentStatus = false, "", "Stopped"
	p, _, err := BuildMaintenanceJobFix(f, map[string]string{OptionStatsOnly: "false"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Statement, "@command = N'/* kakapo */\nEXEC [GraniteLive].sys.sp_updatestats;';") || strings.Contains(p.Statement, "IndexOptimize") ||
		strings.Contains(p.Statement, "kakapo: indexes") || strings.Contains(p.Statement, "@notify_level_email") {
		t.Errorf("statement:\n%s", p.Statement)
	}
	if len(p.Inputs) != 1 || !suContains(p.Warnings, maintNoOla) || !suContains(p.Warnings, "SQL Server Agent is stopped: the jobs will not run until it starts (MNT01).") ||
		p.Changes[1].Target != noOperator {
		t.Errorf("inputs %+v warnings %q changes %+v", p.Inputs, p.Warnings, p.Changes)
	}
	// A name IndexOptimize would read as a list or a pattern: sp_updatestats.
	f = suMaintFacts()
	f.Database = "Shop,Old"
	if p, _, _ := BuildMaintenanceJobFix(f, nil); !strings.Contains(p.Statement, "EXEC [Shop,Old].sys.sp_updatestats;") || !suContains(p.Warnings, maintOlaOddName) {
		t.Errorf("odd name %+v", p)
	}
	// Express: a script to run by hand.
	f = suMaintFacts()
	f.EngineEdition = 4
	p, _, _ = BuildMaintenanceJobFix(f, nil)
	if !p.Express || p.Rollback != "" || !strings.HasPrefix(p.Statement, "-- kakapo: maintenance of GraniteLive on SQL01\\WMS. SQL Server Express has no SQL Server Agent") ||
		!strings.Contains(p.Statement, "/* kakapo */\nEXECUTE master.dbo.IndexOptimize @Databases = N'GraniteLive'") || !suContains(p.Warnings, maintExpress) {
		t.Errorf("express %+v", p)
	}
}

func suShrinkFacts() ShrinkStepFacts {
	return ShrinkStepFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", Job: "Nightly", Found: true, Enabled: true, StartStep: 2,
		Steps: []AgentJobStep{
			{ID: 1, Name: "Backup", Subsystem: "TSQL", Command: "BACKUP DATABASE x TO DISK = 'x'", Database: "master", OnSuccessAction: 4, OnSuccessStep: 2, OnFailAction: 2},
			{ID: 2, Name: "Shrink", Subsystem: "TSQL", Command: "DBCC SHRINKFILE (2, 0)", Database: "GraniteLive", DatabaseUser: "dbo",
				OutputFile: `C:\logs\o.txt`, OnSuccessAction: 3, OnFailAction: 4, OnFailStep: 3, RetryAttempts: 1, RetryInterval: 5, Flags: 2},
			{ID: 3, Name: "Report", Subsystem: "TSQL", Command: "SELECT 1", OnSuccessAction: 1, OnFailAction: 2},
		}}
}

func TestRemoveShrinkStepFix(t *testing.T) {
	f := suShrinkFacts()
	p, fix, err := BuildRemoveShrinkStepFix(f, map[string]string{OptionJobName: "Nightly", OptionJobStep: "2"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "EXEC msdb.dbo.sp_delete_jobstep @job_name = N'Nightly', @step_id = 2;" ||
		!reflect.DeepEqual(p.Changes, []Drift{{"Step 2 of Nightly", `"Shrink": DBCC SHRINKFILE (2, 0)`, "removed"}}) {
		t.Errorf("preview %+v", p)
	}
	wantRollback := "EXEC msdb.dbo.sp_add_jobstep @job_name = N'Nightly', @step_id = 2, @step_name = N'Shrink', @subsystem = N'TSQL',\n" +
		"    @on_success_action = 3, @on_success_step_id = 0, @on_fail_action = 4, @on_fail_step_id = 3,\n" +
		"    @retry_attempts = 1, @retry_interval = 5, @flags = 2, @database_name = N'GraniteLive', @database_user_name = N'dbo', @output_file_name = N'C:\\logs\\o.txt',\n" +
		"    @command = N'DBCC SHRINKFILE (2, 0)';\n" +
		"EXEC msdb.dbo.sp_update_job @job_name = N'Nightly', @start_step_id = 2;"
	if p.Rollback != wantRollback {
		t.Errorf("rollback\n%s\nwant\n%s", p.Rollback, wantRollback)
	}
	want := []string{
		"The job's other steps renumber; a step that jumps to this one by number breaks (shown below if any).",
		`Step 1 ("Backup") goes to step 2 when it succeeds.`,
		"The job starts at step 2: after the removal it starts at the step that takes its number.",
		"The rollback puts the step back as it was read; the jumps above are not restored by it.",
	}
	if !reflect.DeepEqual(p.Warnings[:4], want) || p.Warnings[len(p.Warnings)-1] != ShrinkStepPermission(`SQL01\WMS`) {
		t.Errorf("warnings %q", p.Warnings)
	}
	if fix.ID != FixRemoveShrinkStep || fix.CheckID != "MNT05" || fix.Setting != "Step 2 of Nightly" ||
		RemoveShrinkStepDescription(fix, `SQL01\WMS`) != `Step 2 of Nightly on SQL01\WMS: removed ("Shrink": DBCC SHRINKFILE (2, 0))` ||
		!reflect.DeepEqual(p.Options, map[string]string{OptionJobName: "Nightly", OptionJobStep: "2"}) || !reflect.DeepEqual(p.Inputs[0].Options, []string{"2"}) {
		t.Errorf("fix %+v options %+v", fix, p.Options)
	}

	// Bad options: a step that does not shrink, or none of that number.
	for _, bad := range []string{"3", "9", "x"} {
		p, _, err := BuildRemoveShrinkStepFix(f, map[string]string{OptionJobName: "Nightly", OptionJobStep: bad})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Inputs[0].Warning == "" || p.Inputs[0].Value != bad {
			t.Errorf("step %q: %+v %v", bad, p, err)
		}
	}

	// The only step: the job is disabled instead.
	one := ShrinkStepFacts{ServerName: "S", Job: "Shrink weekly", Found: true, Enabled: true, StartStep: 1,
		Steps: []AgentJobStep{{ID: 1, Name: "Shrink", Subsystem: "TSQL", Command: "DBCC SHRINKDATABASE (N'Shop')"}}}
	p, fix, _ = BuildRemoveShrinkStepFix(one, nil)
	if p.Statement != "EXEC msdb.dbo.sp_update_job @job_name = N'Shrink weekly', @enabled = 0;" ||
		p.Rollback != "EXEC msdb.dbo.sp_update_job @job_name = N'Shrink weekly', @enabled = 1;" || fix.Target != "disabled" ||
		!strings.HasPrefix(p.Warnings[0], "The shrink is the job's only step, so kakapo disables the job") {
		t.Errorf("only step %+v", p)
	}
	one.Enabled = false
	if _, _, err := BuildRemoveShrinkStepFix(one, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("disabled job: %v", err)
	}

	// Read-back.
	removed := f.Steps[1]
	after := suShrinkFacts()
	after.Steps = []AgentJobStep{after.Steps[0], after.Steps[2]}
	if s, ok := RemoveShrinkStepReadBack(false, removed, after); !ok || s != "step removed; the job has 2 step(s)" {
		t.Errorf("removed %q", s)
	}
	if s, ok := RemoveShrinkStepReadBack(false, removed, suShrinkFacts()); ok || s != "the step is still there (step 2)" {
		t.Errorf("still there %q", s)
	}
	if s, ok := RemoveShrinkStepReadBack(true, removed, one); !ok || s != "the job is disabled" {
		t.Errorf("disabled %q", s)
	}
}

// AvailableUpkeepFixes lists maintenance-job and operator-alerts once and remove-shrink-step once
// per shrink step, each with the options its finding's object names.
func TestAvailableUpkeepActionFixes(t *testing.T) {
	f := goodUpkeep()
	f.MaintenanceJob, f.Operators = "", 0
	f.ShrinkJobs = []ShrinkJob{{Job: "Nightly", Step: 2, Command: "DBCC SHRINKFILE (2)"}, {Job: "Nightly", Step: 4, Command: "DBCC SHRINKFILE (1)"},
		{Job: "Weekly", Step: 1, Command: "DBCC SHRINKDATABASE (x)"}}
	c, fs, _ := runUpkeep(f)
	fixes := AvailableUpkeepFixes(f, fs)
	got := []string{}
	for _, x := range fixes {
		got = append(got, x.ID+" "+x.Options[OptionJobName]+" "+x.Options[OptionJobStep])
	}
	want := []string{"maintenance-job  ", "operator-alerts  ", "remove-shrink-step Nightly 2", "remove-shrink-step Nightly 4", "remove-shrink-step Weekly 1"}
	if strings.Join(suSorted(got), "|") != strings.Join(want, "|") {
		t.Errorf("fixes %q", got)
	}
	if c.Fixable != 5 { // MNT03, MNT07 and the three job steps
		t.Errorf("fixable %d", c.Fixable)
	}
	for _, x := range fixes {
		if x.ID == FixMaintenanceJob && (x.Target != "kakapo: statistics (GraniteLive) (daily 01:00)" || x.CheckID != "MNT03") {
			t.Errorf("maintenance fix %+v", x)
		}
		if x.ID == FixOperatorAlerts && x.Current != "0 operator(s) with email" {
			t.Errorf("alerts fix %+v", x)
		}
	}
}

func suSorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
