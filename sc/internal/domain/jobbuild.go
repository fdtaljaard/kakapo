package domain

import (
	"fmt"
	"strings"
)

// The Agent jobs kakapo creates outside the backup schedule (docs/phase4d-contract.md, shared rule):
// created as the backup schedule creates them (docs/phase3g-contract.md): owned by the sa principal
// whatever its name, category Database Maintenance, a description starting "Created by kakapo",
// JobStepTag first in every step's command, all in one transaction inside TRY ... CATCH that
// refuses when a job of the same name exists; the rollback deletes them. backupschedule.go keeps
// its own copy of these blocks, whose text its tests pin.

// Agent schedule frequencies (sysschedules freq_type) and the weekly day bits (freq_interval).
const (
	JobDaily  = 4
	JobWeekly = 8

	JobSunday     = 1
	JobMonToSat   = 2 | 4 | 8 | 16 | 32 | 64
	jobAllWeekday = JobSunday | JobMonToSat
)

// KakapoJobStep is one step of a job kakapo creates: T-SQL run from master.
type KakapoJobStep struct {
	Name    string
	Command string // JobStepTag is put first when it is not there
}

// KakapoJobSchedule is when a job runs: daily (Days 0) or weekly on the Days bits, at Clock (HH:MM,
// server time).
type KakapoJobSchedule struct {
	Name  string
	Freq  int // JobDaily or JobWeekly
	Days  int // JobWeekly: the day bits
	Clock string
}

// KakapoJob is one Agent job kakapo creates.
type KakapoJob struct {
	Name        string
	Description string // "Created by kakapo on {date} for {db}: ..."
	Operator    string // the operator told on failure; "" no one
	Steps       []KakapoJobStep
	Schedule    KakapoJobSchedule
}

// kakapoStepCommand is a step's command with JobStepTag on its first line.
func kakapoStepCommand(cmd string) string {
	if strings.HasPrefix(cmd, JobStepTag) {
		return cmd
	}
	return JobStepTag + "\n" + cmd
}

// KakapoJobDescription is the description kakapo writes on a job it creates.
func KakapoJobDescription(date, db, what string) string {
	return "Created by kakapo on " + date + " for " + db + ": " + what
}

// block is the sp_add_job ... sp_add_jobserver calls of one job, indented for the batch.
func (j KakapoJob) block() string {
	name := nstr(j.Name)
	var b strings.Builder
	b.WriteString("    EXEC msdb.dbo.sp_add_job @job_name = " + name + ", @enabled = 1,\n")
	b.WriteString("        @description = " + nstr(j.Description) + ",\n")
	b.WriteString("        @category_name = N'Database Maintenance', @owner_login_name = @owner")
	if j.Operator != "" {
		b.WriteString(",\n        @notify_level_email = 2, @notify_email_operator_name = " + nstr(j.Operator))
	}
	b.WriteString(";\n")
	for i, s := range j.Steps {
		success := 3 // go to the next step
		if i == len(j.Steps)-1 {
			success = 1 // quit reporting success
		}
		fmt.Fprintf(&b, "    EXEC msdb.dbo.sp_add_jobstep @job_name = %s, @step_id = %d, @step_name = %s,\n", name, i+1, nstr(s.Name))
		fmt.Fprintf(&b, "        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = %d, @on_fail_action = 2,\n", success)
		b.WriteString("        @command = " + nstr(kakapoStepCommand(s.Command)) + ";\n")
	}
	sc := j.Schedule
	b.WriteString("    EXEC msdb.dbo.sp_add_jobschedule @job_name = " + name + ", @name = " + nstr(sc.Name) + ", @enabled = 1,\n")
	if sc.Freq == JobWeekly {
		fmt.Fprintf(&b, "        @freq_type = 8, @freq_interval = %d, @freq_recurrence_factor = 1, @active_start_time = %d;\n",
			sc.Days&jobAllWeekday, agentStartTime(sc.Clock))
	} else {
		fmt.Fprintf(&b, "        @freq_type = 4, @freq_interval = 1, @active_start_time = %d;\n", agentStartTime(sc.Clock))
	}
	b.WriteString("    EXEC msdb.dbo.sp_add_jobserver @job_name = " + name + ", @server_name = N'(local)';\n")
	return b.String()
}

// KakapoJobsBatch is the statement that creates the jobs: one batch, in a transaction inside TRY ...
// CATCH, refusing when a job of the same name already exists, the category made when missing.
func KakapoJobsBatch(jobs []KakapoJob) string {
	var b strings.Builder
	b.WriteString("DECLARE @owner sysname = SUSER_SNAME(0x01);\n")
	b.WriteString("BEGIN TRY\n")
	for _, j := range jobs {
		b.WriteString("    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = " + nstr(j.Name) + ")\n")
		b.WriteString("        THROW 50000, " + nstr("A job named "+j.Name+" already exists") + ", 1;\n")
	}
	b.WriteString("    BEGIN TRANSACTION;\n")
	b.WriteString("    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)\n")
	b.WriteString("        EXEC msdb.dbo.sp_add_category @class = N'JOB', @type = N'LOCAL', @name = N'Database Maintenance';\n")
	for _, j := range jobs {
		b.WriteString(j.block())
	}
	b.WriteString("    COMMIT TRANSACTION;\n")
	b.WriteString("END TRY\n")
	b.WriteString("BEGIN CATCH\n")
	b.WriteString("    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n")
	b.WriteString("    THROW;\n")
	b.WriteString("END CATCH;")
	return b.String()
}

// KakapoJobsDelete deletes jobs by name: the rollback of KakapoJobsBatch.
func KakapoJobsDelete(names []string) string {
	lines := make([]string, 0, len(names))
	for _, n := range names {
		lines = append(lines, "EXEC msdb.dbo.sp_delete_job @job_name = "+nstr(n)+";")
	}
	return strings.Join(lines, "\n")
}

// KakapoJobNames are the names of the jobs, in order.
func KakapoJobNames(jobs []KakapoJob) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

// KakapoJobsReadBack says what was read back after the jobs were created (states as
// collect.ReadJobsByName reads them) and whether every job exists and is enabled.
func KakapoJobsReadBack(names []string, states []BackupJobState) (string, bool) {
	parts, verified := []string{}, true
	for _, name := range names {
		var st *BackupJobState
		for i := range states {
			if states[i].Name == name {
				st = &states[i]
				break
			}
		}
		if st == nil {
			parts, verified = append(parts, name+": not found"), false
			continue
		}
		text := name + ": enabled"
		if !st.Enabled {
			text, verified = name+": disabled", false
		}
		if !st.ScheduleEnabled {
			text += ", schedule disabled"
		}
		if st.NextRun != nil {
			text += ", next run " + sqlTime(*st.NextRun)
		} else {
			text += ", next run not set by Agent yet"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ") + ".", verified
}

// words is a schedule in words: "daily 01:00", "Sunday 01:00", "Monday to Saturday 01:00".
func (sc KakapoJobSchedule) words() string {
	if sc.Freq != JobWeekly {
		return "daily " + sc.Clock
	}
	switch sc.Days & jobAllWeekday {
	case JobSunday:
		return "Sunday " + sc.Clock
	case JobMonToSat:
		return "Monday to Saturday " + sc.Clock
	case jobAllWeekday:
		return "daily " + sc.Clock
	}
	days := []string{}
	for i, d := range []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"} {
		if sc.Days&(1<<i) != 0 {
			days = append(days, d)
		}
	}
	return strings.Join(days, ", ") + " " + sc.Clock
}
