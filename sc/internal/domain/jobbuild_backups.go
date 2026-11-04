package domain

import (
	"fmt"
	"strings"
)

// The parts every Agent job kakapo creates on the Backups card shares (docs/phase3g-contract.md, the
// shared rule of docs/phase4d-contract.md): owned by the sa principal whatever its name, in the
// Database Maintenance category, a description that says kakapo created it, each step tagged
// /* kakapo */, all of it in one transaction that refuses when a job of the same name exists, and a
// rollback that deletes the jobs.

// agentJobBatch is the batch that creates kakapo's jobs: the owner, a refusal per job name that
// already exists, then the category and the job blocks inside one transaction, inside TRY ... CATCH.
func agentJobBatch(names []string, blocks string) string {
	var b strings.Builder
	b.WriteString("DECLARE @owner sysname = SUSER_SNAME(0x01);\n")
	b.WriteString("BEGIN TRY\n")
	for _, name := range names {
		b.WriteString("    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = " + nstr(name) + ")\n")
		b.WriteString("        THROW 50000, " + nstr("A job named "+name+" already exists") + ", 1;\n")
	}
	b.WriteString("    BEGIN TRANSACTION;\n")
	b.WriteString("    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)\n")
	b.WriteString("        EXEC msdb.dbo.sp_add_category @class = N'JOB', @type = N'LOCAL', @name = N'Database Maintenance';\n")
	b.WriteString(blocks)
	b.WriteString("    COMMIT TRANSACTION;\n")
	b.WriteString("END TRY\n")
	b.WriteString("BEGIN CATCH\n")
	b.WriteString("    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n")
	b.WriteString("    THROW;\n")
	b.WriteString("END CATCH;")
	return b.String()
}

// agentJobAdd is the sp_add_job call of one job: enabled, owned by @owner, in Database Maintenance,
// notifying the operator by email on failure when there is one.
func agentJobAdd(b *strings.Builder, name, description, operator string) {
	b.WriteString("    EXEC msdb.dbo.sp_add_job @job_name = " + nstr(name) + ", @enabled = 1,\n")
	b.WriteString("        @description = " + nstr(description) + ",\n")
	b.WriteString("        @category_name = N'Database Maintenance', @owner_login_name = @owner")
	if operator != "" {
		b.WriteString(",\n        @notify_level_email = 2, @notify_email_operator_name = " + nstr(operator))
	}
	b.WriteString(";\n")
}

// agentJobStep is the sp_add_jobstep call of one T-SQL step run in master: the last step quits
// reporting success, the others go to the next step; a failure quits reporting failure. The command
// is expected to start with JobStepTag.
func agentJobStep(b *strings.Builder, name string, id int, stepName, command string, last bool) {
	success := 3
	if last {
		success = 1
	}
	fmt.Fprintf(b, "    EXEC msdb.dbo.sp_add_jobstep @job_name = %s, @step_id = %d, @step_name = %s,\n", nstr(name), id, nstr(stepName))
	fmt.Fprintf(b, "        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = %d, @on_fail_action = 2,\n", success)
	b.WriteString("        @command = " + nstr(command) + ";\n")
}

// agentJobServer is the sp_add_jobserver call that targets the local server.
func agentJobServer(b *strings.Builder, name string) {
	b.WriteString("    EXEC msdb.dbo.sp_add_jobserver @job_name = " + nstr(name) + ", @server_name = N'(local)';\n")
}

// agentDeleteJobs is the statement that deletes kakapo's jobs by name, one line each.
func agentDeleteJobs(names []string) string {
	lines := []string{}
	for _, n := range names {
		lines = append(lines, "EXEC msdb.dbo.sp_delete_job @job_name = "+nstr(n)+";")
	}
	return strings.Join(lines, "\n")
}
