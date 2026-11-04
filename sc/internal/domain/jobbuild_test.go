package domain

import (
	"strings"
	"testing"
	"time"
)

// kakapo's other jobs are created as the backup schedule creates its own: owner sa, the category,
// "Created by kakapo", the tag in each step, one transaction, a refusal when the name is taken.
func TestKakapoJobsBatch(t *testing.T) {
	jobs := []KakapoJob{
		{Name: "kakapo: a", Description: KakapoJobDescription("2026-11-03", "Shop", "statistics daily 01:00"), Operator: "IT",
			Steps:    []KakapoJobStep{{Name: "One", Command: "SELECT 1;"}, {Name: "Two", Command: JobStepTag + "\nSELECT 2;"}},
			Schedule: KakapoJobSchedule{Name: "kakapo: a, daily 01:00", Freq: JobDaily, Clock: "01:00"}},
		{Name: "kakapo: b", Description: "Created by kakapo", Steps: []KakapoJobStep{{Name: "Only", Command: "SELECT 3;"}},
			Schedule: KakapoJobSchedule{Name: "kakapo: b, Sunday 23:45", Freq: JobWeekly, Days: JobSunday, Clock: "23:45"}},
	}
	b := KakapoJobsBatch(jobs)
	for _, want := range []string{
		"DECLARE @owner sysname = SUSER_SNAME(0x01);\nBEGIN TRY\n",
		"    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'kakapo: a')\n        THROW 50000, N'A job named kakapo: a already exists', 1;\n",
		"    BEGIN TRANSACTION;\n    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)\n",
		"@description = N'Created by kakapo on 2026-11-03 for Shop: statistics daily 01:00',",
		"@category_name = N'Database Maintenance', @owner_login_name = @owner,\n        @notify_level_email = 2, @notify_email_operator_name = N'IT';",
		"@step_id = 1, @step_name = N'One',\n        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 3, @on_fail_action = 2,\n        @command = N'/* kakapo */\nSELECT 1;';",
		"@step_id = 2, @step_name = N'Two',\n        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 1, @on_fail_action = 2,\n        @command = N'/* kakapo */\nSELECT 2;';",
		"@freq_type = 4, @freq_interval = 1, @active_start_time = 10000;",
		"@freq_type = 8, @freq_interval = 1, @freq_recurrence_factor = 1, @active_start_time = 234500;",
		"EXEC msdb.dbo.sp_add_jobserver @job_name = N'kakapo: b', @server_name = N'(local)';",
		"    COMMIT TRANSACTION;\nEND TRY\nBEGIN CATCH\n    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n    THROW;\nEND CATCH;",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("batch lacks %q:\n%s", want, b)
		}
	}
	if strings.Count(b, JobStepTag) != 3 || strings.Contains(b, "N'kakapo: b', @enabled = 1,\n        @description = N'Created by kakapo',\n        @category_name = N'Database Maintenance', @owner_login_name = @owner,") {
		t.Errorf("one tag per step, no notification without an operator:\n%s", b)
	}
	if d := KakapoJobsDelete(KakapoJobNames(jobs)); d != "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: a';\nEXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: b';" {
		t.Errorf("delete %q", d)
	}
	for sc, want := range map[KakapoJobSchedule]string{
		{Freq: JobDaily, Clock: "01:00"}:                                "daily 01:00",
		{Freq: JobWeekly, Days: JobSunday, Clock: "01:00"}:              "Sunday 01:00",
		{Freq: JobWeekly, Days: JobMonToSat, Clock: "01:00"}:            "Monday to Saturday 01:00",
		{Freq: JobWeekly, Days: 2 | 32, Clock: "01:00"}:                 "Monday, Friday 01:00",
		{Freq: JobWeekly, Days: JobSunday | JobMonToSat, Clock: "1:00"}: "daily 1:00",
	} {
		if sc.words() != want {
			t.Errorf("%+v: %q", sc, sc.words())
		}
	}
}

func TestKakapoJobsReadBack(t *testing.T) {
	next := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	s, ok := KakapoJobsReadBack([]string{"kakapo: a", "kakapo: b"}, []BackupJobState{
		{Name: "kakapo: a", Enabled: true, ScheduleEnabled: true, NextRun: &next},
		{Name: "kakapo: b", Enabled: true, ScheduleEnabled: true},
	})
	if !ok || s != "kakapo: a: enabled, next run 2026-11-04 01:00; kakapo: b: enabled, next run not set by Agent yet." {
		t.Errorf("read back %q", s)
	}
	if s, ok := KakapoJobsReadBack([]string{"kakapo: a"}, nil); ok || s != "kakapo: a: not found." {
		t.Errorf("missing %q", s)
	}
}
