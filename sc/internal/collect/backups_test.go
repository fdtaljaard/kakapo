package collect

import (
	"context"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// TestBackupQuery checks the phase 3f reads: full backups to NUL (BAK06) and the differentials
// based on them are not restore points, and delayed durability (DBC12) is read only where the
// column exists.
func TestBackupQuery(t *testing.T) {
	for _, want := range []string{"physical_device_name IN (N'NUL', N'NUL:')", "nb.backup_set_uuid = bs.differential_base_guid",
		"DATEADD(DAY, -30, @now)", "COL_LENGTH(N'sys.databases', N'delayed_durability_desc')", "SELECT t FROM @nul ORDER BY t;",
		"bs.is_copy_only = 0"} {
		if !strings.Contains(backupFactsQuery, want) {
			t.Errorf("backup query lacks %q", want)
		}
	}
	if n := strings.Count(backupFactsQuery, "BEGIN TRY"); n != 8 {
		t.Errorf("%d TRY blocks: AUTO_CLOSE open, msdb, delayed durability, and for the backup schedule fix the Agent, jobs, maintenance plans, operator, backup folder", n)
	}
}

// TestBackupScheduleQuery checks the phase 3g reads (docs/phase3g-contract.md): every batch is
// tagged, the backups batch only reads (the fix statements are built in domain and run through
// ExecFix), and the job-coverage SQL holds the contract's patterns.
func TestBackupScheduleQuery(t *testing.T) {
	for name, q := range map[string]string{"backups": backupFactsQuery, "folder": folderExistsQuery, "jobs": backupJobsQuery} {
		if !strings.HasPrefix(Tag(q), KakapoTag+" ") || !strings.HasPrefix(q, safeSession) {
			t.Errorf("%s query is not tagged and safe", name)
		}
		for _, write := range []string{"sp_add_job", "sp_delete_job", "sp_start_job", "sp_update_job", "sp_add_category",
			"xp_create_subdir", "xp_delete_file", "BACKUP DATABASE [", "BACKUP LOG [", "INSERT INTO msdb", "UPDATE msdb", "DELETE "} {
			if strings.Contains(q, write) {
				t.Errorf("%s query writes: %q", name, write)
			}
		}
	}
	for _, want := range []string{
		// what a backup job is
		"s.command LIKE N'%BACKUP DATABASE%'", "s.command LIKE N'%BACKUP LOG%'", "s.command LIKE N'%DatabaseBackup%'",
		// naming this database: [db], 'db' or N'db', the bare name as a word; or all of them
		"CHARINDEX(QUOTENAME(@db), s.command) > 0", "CHARINDEX(N'''' + @db + N'''', s.command) > 0",
		"N' ' + s.command + N' ' LIKE N'%[^0-9A-Za-z_@#$]' + @dbLike + N'[^0-9A-Za-z_@#$]%'",
		"REPLACE(REPLACE(REPLACE(@db, N'[', N'[[]'), N'%', N'[%]'), N'_', N'[_]')",
		"s.command LIKE N'%USER[_]DATABASES%'", "s.command LIKE N'%ALL[_]DATABASES%'",
		// the kind, from the command text (Ola's @BackupType too)
		"N'%@BackupType%=%''FULL''%'", "N'%@BackupType%=%''LOG''%'",
		// the rest of the facts
		"j.name IN (N'kakapo: ' + @db + N' full backup', N'kakapo: ' + @db + N' log backup')",
		// kakapo's jobs' schedules and cleanup steps, for the remove fix's rollback (phase 4b)
		"sc.active_start_time, sc.freq_subday_interval", "JOIN msdb.dbo.sysschedules AS ss ON ss.schedule_id = js.schedule_id",
		"s.job_id = j.job_id AND s.step_id > 1 ORDER BY s.step_id",
		"SELECT name, command, cleanup, start_time, subday_interval FROM @kakapoJobs ORDER BY name;",
		"msdb.dbo.sysmaintplan_subplans", "OBJECT_ID(N'master.dbo.DatabaseBackup')",
		"o.enabled = 1 AND o.email_address IS NOT NULL", "ORDER BY o.name",
		`EXEC master.dbo.xp_instance_regread N'HKEY_LOCAL_MACHINE', N'Software\Microsoft\MSSQLServer\MSSQLServer', N'BackupDirectory'`,
		"servicename LIKE N'SQL Server Agent%'", "CONVERT(int, SERVERPROPERTY('EngineEdition')) = 4",
		"LIKE N'Web%'", "f.type = 0",
	} {
		if !strings.Contains(backupFactsQuery, want) {
			t.Errorf("backup query lacks %q", want)
		}
	}
	if !strings.Contains(folderExistsQuery, "EXEC master.dbo.xp_fileexist @folder") {
		t.Error("folder check is not xp_fileexist")
	}
	for _, want := range []string{"msdb.dbo.sysjobschedules", "msdb.dbo.sysschedules", "next_run_date", "WHERE j.name IN (@full, @log)"} {
		if !strings.Contains(backupJobsQuery, want) {
			t.Errorf("job read-back lacks %q", want)
		}
	}
	// The job steps kakapo writes carry the same tag as every batch.
	if domain.JobStepTag != KakapoTag {
		t.Errorf("job step tag %q, batch tag %q", domain.JobStepTag, KakapoTag)
	}
}

// TestBackupFactsLive reads the backup facts of one database on a real instance, read-only, when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestBackupFactsLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := BackupFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if f.State == "" || f.RecoveryModel == "" || f.ServerNow.IsZero() || f.CreateDate.IsZero() {
		t.Errorf("facts: %+v", f)
	}
	t.Logf("%s: state %s, %s, created %v, now %v (UTC%+d min), last full %v, diff %v, log %v, 7 days %d/%d/%d, %d points, checkdb %v, suspect %d, log MB %v, msdb %q",
		name, f.State, f.RecoveryModel, f.CreateDate, f.ServerNow, f.UTCOffsetMinutes, f.LastFull, f.LastDiff, f.LastLog,
		f.Fulls7, f.Diffs7, f.Logs7, len(f.RestorePoints), f.LastGoodCheckDb, f.SuspectPages, f.LogSizeMB, f.MsdbError)
	t.Logf("schedule facts: server %q, edition %q, agent %v %q %q, jobs read %v %q: %+v, kakapo %q, plans %d %q, ola %v, operator %q %q, backup dir %q, compression %v, data %q",
		f.ServerName, f.Edition, f.HasAgent, f.AgentStatus, f.AgentError, f.JobsRead, f.JobsError, f.BackupJobs, f.KakapoJobs,
		f.MaintenancePlans, f.PlansError, f.OlaBackup, f.Operator, f.OperatorError, f.DefaultBackupDir, f.CompressionOK, f.DataFiles)
	if !f.JobsRead || f.ServerName == "" {
		t.Errorf("schedule facts not read: %+v", f)
	}

	if _, err := BackupFacts(ctx, db, "NoSuchDatabase_x9"); err == nil {
		t.Error("an unknown database should be an error")
	}
}
