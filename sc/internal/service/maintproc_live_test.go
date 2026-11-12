package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	// Shared memory, so the live test also reaches a local SQL Server Express that has TCP/IP off (its
	// default): SQLMONITOR_TEST_HOST=lpc:localhost\SQLEXPRESS. Test only: kakapo itself connects over TCP.
	_ "github.com/microsoft/go-mssqldb/sharedmemory"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// maintLiveDB is the throwaway database the live test creates and drops.
const maintLiveDB = "kakapo_maint_test"

// maintAsSystem runs a statement as the scheduled task does: as NT AUTHORITY\SYSTEM, which is not sysadmin
// and may only EXECUTE the procedure. No TRY around it (a CATCH would hide the procedure's own error behind
// the first one an extended procedure sent): every statement used raises without ending the batch, so the
// REVERT runs; and the connections it runs on are never pooled (sysDB keeps no idle connection).
func maintAsSystem(stmt string) string {
	return "EXECUTE AS LOGIN = N'NT AUTHORITY\\SYSTEM';\n" + stmt + "\nREVERT;"
}

// TestMaintProcLive runs phase 5p's live checks (docs/phase5p-contract.md, Done when) through the real
// fixes when SQLMONITOR_TEST_HOST (Windows authentication) names a server and SQLMONITOR_TEST_MAINT=1
// allows writing to it. By default it creates and drops kakapo_maint_test; SQLMONITOR_TEST_MAINT_DB names
// an existing FULL-recovery database to use instead, which it never creates, drops or changes beyond what
// the procedure itself does (backups, statistics, the check), and whose backup files and history it
// deletes only when it was the one to make them. After the install, every call is made as the scheduled
// task makes it, as NT AUTHORITY\SYSTEM. It refuses to start when kakapo's objects, certificate or login
// are already on the server. The procedure is not limited to Express, so it is proven on any edition; the
// plan row is built as Express would build it.
func TestMaintProcLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_MAINT") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_MAINT=1 to install kakapo's maintenance procedure on a live instance")
	}
	dbName, existing := maintLiveDB, os.Getenv("SQLMONITOR_TEST_MAINT_DB")
	if existing != "" {
		dbName = existing
	}
	ctx := context.Background()
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	// Closed last: t.Cleanup runs after the deferred calls, and the cleanup below needs the connection.
	t.Cleanup(func() { db.Close() })
	// sysDB runs the calls as SYSTEM, on connections closed after each use.
	sysDB, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	sysDB.SetMaxIdleConns(0)
	t.Cleanup(func() { sysDB.Close() })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
		return n
	}
	kakapoLeft := func() int {
		return count("SELECT (SELECT COUNT(*) FROM master.sys.objects WHERE name LIKE 'kakapo[_]%') + (SELECT COUNT(*) FROM master.sys.certificates WHERE name LIKE 'kakapo[_]%')" +
			" + (SELECT COUNT(*) FROM sys.server_principals WHERE name LIKE 'kakapo[_]%')")
	}
	if n := kakapoLeft(); n != 0 {
		t.Fatalf("the server already holds %d of kakapo's objects, certificates or logins: this test does not touch an install it did not make", n)
	}
	systemUserBefore := count("SELECT COUNT(*) FROM master.sys.database_principals WHERE sid = SUSER_SID(N'NT AUTHORITY\\SYSTEM')")
	if count("SELECT COUNT(*) FROM sys.server_principals WHERE name = N'NT AUTHORITY\\SYSTEM'") == 0 {
		t.Skip(`NT AUTHORITY\SYSTEM has no login on this server`)
	}
	var folder string
	if err := db.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('InstanceDefaultBackupPath') AS nvarchar(400))").Scan(&folder); err != nil || folder == "" {
		t.Fatalf("default backup folder: %v", err)
	}
	folder = strings.TrimRight(folder, `\`)
	dir := folder + `\` + dbName
	var dirBefore bool
	if err := db.QueryRowContext(ctx, "DECLARE @r TABLE (f int, d int, p int); INSERT @r EXEC master.dbo.xp_fileexist @p1; SELECT CAST(ISNULL(MAX(d), 0) AS bit) FROM @r;", dir).Scan(&dirBefore); err != nil {
		t.Fatal(err)
	}
	historyBefore := count("SELECT COUNT(*) FROM msdb.dbo.backupset WHERE database_name = @p1", dbName)

	if existing == "" {
		if n := count("SELECT COUNT(*) FROM sys.databases WHERE name = @p1", dbName); n != 0 {
			t.Fatalf("%s exists already", dbName)
		}
		exec("CREATE DATABASE [" + dbName + "]; ALTER DATABASE [" + dbName + "] SET RECOVERY FULL;")
	} else if count("SELECT COUNT(*) FROM sys.databases WHERE name = @p1 AND recovery_model_desc = N'FULL' AND state_desc = N'ONLINE'", dbName) != 1 {
		t.Fatalf("%s must exist, be online and in FULL recovery", dbName)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if err := collect.ExecFixBatches(ctx, db, domain.MaintDropStatement()); err != nil {
			t.Logf("cleanup: %v", err)
		}
		if existing == "" {
			if _, err := db.ExecContext(ctx, "USE master; IF DB_ID(N'"+dbName+"') IS NOT NULL BEGIN ALTER DATABASE ["+dbName+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+dbName+"]; END;"); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
		if historyBefore == 0 {
			if _, err := db.ExecContext(ctx, "EXEC msdb.dbo.sp_delete_database_backuphistory @database_name = @p1;", dbName); err != nil {
				t.Logf("cleanup history: %v", err)
			}
		}
		if !dirBefore {
			for _, ext := range []string{"bak", "trn"} {
				if _, err := db.ExecContext(ctx, "EXEC master.dbo.xp_delete_file 0, @p1, @p2, N'2100-01-01T00:00:00', 0;", dir, ext); err != nil {
					t.Logf("cleanup files: %v", err)
				}
			}
		}
	})
	if existing == "" {
		exec("USE [" + dbName + "]; CREATE TABLE dbo.Item (ID int IDENTITY PRIMARY KEY, Code nvarchar(20) NOT NULL, Qty int NOT NULL); " +
			"INSERT dbo.Item (Code, Qty) SELECT TOP (5000) CONCAT(N'IT', ROW_NUMBER() OVER (ORDER BY (SELECT NULL))), 1 FROM sys.all_objects AS a CROSS JOIN sys.all_objects AS b; " +
			"CREATE STATISTICS st_qty ON dbo.Item (Qty); UPDATE dbo.Item SET Qty = 2 WHERE ID % 2 = 0; USE master;")
	}

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "SQL1", Host: host, Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, fakeCreds{}, "test")
	clock := func() time.Time {
		now, _, err := collect.ServerClock(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return now
	}
	mark := func() int64 {
		m, err := collect.MaintLogMark(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	runs := func(after int64) []domain.MaintRun {
		r, err := collect.ReadMaintRuns(ctx, db, dbName, after)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	words := func(rs []domain.MaintRun) string {
		out := []string{}
		for _, r := range rs {
			w := r.Task + " " + r.Outcome
			if r.Message != "" {
				w += " (" + r.Message + ")"
			}
			out = append(out, w)
		}
		return strings.Join(out, "; ")
	}
	// call is the scheduled task's call: as SYSTEM.
	call := func() (int64, []domain.MaintRun, error) {
		m := mark()
		err := collect.ExecFix(ctx, sysDB, maintAsSystem("EXEC master.dbo.kakapo_Maintenance;"))
		return m, runs(m), err
	}
	signing := func() domain.MaintSigning {
		f, err := collect.ReadMaintProc(ctx, db, dbName)
		if err != nil {
			t.Fatal(err)
		}
		return f.Signing
	}
	thumbprint := func() string {
		var tp string
		if err := db.QueryRowContext(ctx, "SELECT ISNULL(CONVERT(varchar(100), thumbprint, 1), '') FROM master.sys.certificates WHERE name = N'kakapo_MaintenanceCert' UNION ALL SELECT '' WHERE CERT_ID(N'kakapo_MaintenanceCert') IS NULL").Scan(&tp); err != nil {
			t.Fatal(err)
		}
		return tp
	}

	// 1. Install it, with Run now (as the user who applies it): a full backup and a statistics row in the
	// log, and the procedure signed. Statistics are set an hour ago so they are in their window; the
	// maintenance day is three days on, so it is not today's.
	now := clock()
	day := domain.CheckdbDays[(int(now.Weekday())+3)%7]
	opts := map[string]string{domain.OptionProcFolder: folder, domain.OptionProcRunNow: "true", domain.OptionProcDay: day,
		domain.OptionProcStatsTime: now.Add(-time.Hour).Format("15:04")}
	p, err := svc.PreviewFixWithOptions(ctx, srv.ID, dbName, domain.FixInstallMaintenanceProc, opts)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	t.Logf("preview: %d changes; first warning: %s", len(p.Changes), p.Warnings[0])
	for _, c := range p.Changes[len(p.Changes)-3:] {
		t.Logf("   %s: %s -> %s", c.Setting, c.Current, c.Target)
	}
	start := mark()
	res, err := svc.ApplyFixWithOptions(ctx, srv.ID, dbName, domain.FixInstallMaintenanceProc, p.Options)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	first := runs(start)
	firstPrint := thumbprint()
	t.Logf("1. install: verified %v; %s\n   log: %s", res.Verified, res.After, words(first))
	if !res.Verified || !signing().Ready() || !hasRun(first, domain.PieceFullBackup, domain.MaintOK) || !hasRun(first, domain.PieceStatistics, domain.MaintOK) {
		t.Fatalf("check 1 failed")
	}
	for _, r := range first {
		if r.Task == domain.PieceFullBackup && !strings.HasPrefix(r.File, dir+`\`+dbName+"_FULL_") {
			t.Errorf("full backup file %q", r.File)
		}
	}

	// 2. As SYSTEM: a second call within 15 minutes does nothing heavy and takes a log backup; a third does nothing.
	_, second, err := call()
	t.Logf("2. second call, as SYSTEM (err %v): %s", err, words(second))
	if err != nil || len(second) != 1 || second[0].Task != domain.PieceLogBackup || second[0].Outcome != domain.MaintOK {
		t.Fatalf("check 2 failed")
	}
	_, third, err := call()
	t.Logf("2. third call, as SYSTEM (err %v): %q", err, words(third))
	if err != nil || len(third) != 0 {
		t.Fatalf("check 2 (third call) failed")
	}

	// 3. With the heavy lock held by another session, the heavy work is skipped; the log backup still runs.
	// The check is made due (its day today, its time half an hour ago), the last log backup 20 minutes old.
	now = clock()
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET CheckDay = @p1, CheckTime = @p2 WHERE DatabaseName = @p3",
		int(now.Weekday())+1, now.Add(-30*time.Minute).Format("15:04"), dbName)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(MINUTE, -20, StartTime) WHERE DatabaseName = @p1 AND Task = 'log-backup'", dbName)
	// The check's rows so far (call 1 skipped it, outside the old day's window) go back a day, as if from before its new time.
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -1, StartTime) WHERE DatabaseName = @p1 AND Task = 'integrity-check'", dbName)
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rc int
	if err := holder.QueryRowContext(ctx, "USE master; DECLARE @rc int; EXEC @rc = sys.sp_getapplock @Resource = N'kakapo_Maintenance heavy "+dbName+"', @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = 0; SELECT @rc;").Scan(&rc); err != nil || rc < 0 {
		t.Fatalf("hold the lock: %v %d", err, rc)
	}
	_, locked, err := call()
	t.Logf("3. call as SYSTEM with the heavy lock held elsewhere (err %v): %s", err, words(locked))
	holder.ExecContext(ctx, "EXEC sys.sp_releaseapplock @Resource = N'kakapo_Maintenance heavy "+dbName+"', @LockOwner = 'Session';")
	holder.Close()
	if err != nil || !hasRun(locked, domain.PieceIntegrity, domain.MaintSkipped) || hasRun(locked, domain.PieceIntegrity, domain.MaintOK) ||
		!hasRun(locked, domain.PieceLogBackup, domain.MaintOK) || !strings.Contains(words(locked), "another call holds the lock") {
		t.Fatalf("check 3 failed")
	}
	_, freed, err := call()
	t.Logf("3. next call as SYSTEM, lock free (err %v): %s", err, words(freed))
	if err != nil || !hasRun(freed, domain.PieceIntegrity, domain.MaintOK) {
		t.Fatalf("check 3 (the skipped check runs at the next call) failed")
	}

	// 3b. Apply again: ALTER PROCEDURE drops the signature, so the fix signs it anew with a new
	// certificate. Then, as SYSTEM, a full backup and statistics (made due: their rows a day back).
	res, err = svc.ApplyFixWithOptions(ctx, srv.ID, dbName, domain.FixInstallMaintenanceProc,
		map[string]string{domain.OptionProcFolder: folder, domain.OptionProcDay: day, domain.OptionProcStatsTime: opts[domain.OptionProcStatsTime]})
	if err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	secondPrint := thumbprint()
	t.Logf("3b. re-apply: verified %v; %s; certificate %s -> %s", res.Verified, res.After, firstPrint, secondPrint)
	if !res.Verified || !signing().Ready() || secondPrint == "" || secondPrint == firstPrint {
		t.Fatalf("check 3b (re-signed with a new certificate) failed")
	}
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -1, StartTime) WHERE DatabaseName = @p1 AND Task IN ('full-backup', 'statistics')", dbName)
	_, again, err := call()
	t.Logf("3b. call as SYSTEM after the re-apply (err %v): %s", err, words(again))
	if err != nil || !hasRun(again, domain.PieceFullBackup, domain.MaintOK) || !hasRun(again, domain.PieceStatistics, domain.MaintOK) {
		t.Fatalf("check 3b (full backup and statistics as SYSTEM) failed")
	}

	// 4. A failing task: the backup folder on a drive that is not there. The full backup is made due (its
	// last run two days back), the cleanup too (its last run eight days back), which runs after it.
	missing := missingDrive(t) + `kakapo_maint_missing`
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET BackupFolder = @p1 WHERE DatabaseName = @p2", missing, dbName)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -2, StartTime) WHERE DatabaseName = @p1 AND Task = 'full-backup'", dbName)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -8, StartTime) WHERE Task = 'history-cleanup'")
	_, failing, err := call()
	t.Logf("4. call as SYSTEM with the folder on a missing drive: raised %v\n   log: %s", err, words(failing))
	if err == nil || !strings.Contains(err.Error(), "kakapo_Maintenance failed") || !strings.Contains(err.Error(), "full-backup") ||
		!hasRun(failing, domain.PieceFullBackup, domain.MaintFailed) || !hasRun(failing, domain.PieceCleanup, domain.MaintOK) {
		t.Fatalf("check 4 failed")
	}

	// 4b. SYSTEM can do nothing else: outside the procedure every one of these is refused.
	for name, stmt := range map[string]string{
		"a plain BACKUP":                         "BACKUP DATABASE [" + dbName + "] TO DISK = N'NUL' WITH COPY_ONLY;",
		"xp_delete_file":                         "EXEC master.dbo.xp_delete_file 0, N'" + dir + "', N'bak', N'2000-01-01T00:00:00', 0;",
		"reading the procedure's log":            "DECLARE @n int; SELECT @n = COUNT(*) FROM master.dbo.kakapo_MaintenanceLog;",
		"changing the procedure's settings":      "UPDATE master.dbo.kakapo_MaintenanceSettings SET Enabled = 0;",
		"DBCC CHECKDB":                           "DBCC CHECKDB ([" + dbName + "]) WITH NO_INFOMSGS;",
		"reading the database":                   "DECLARE @n int; SELECT @n = COUNT(*) FROM [" + dbName + "].sys.objects;",
		"altering the procedure (drops signing)": "EXEC (N'ALTER PROCEDURE dbo.kakapo_Maintenance AS RETURN 0;');",
	} {
		err := collect.ExecFix(ctx, sysDB, maintAsSystem(stmt))
		t.Logf("4b. as SYSTEM, %s: %v", name, err)
		if err == nil {
			t.Errorf("check 4b: SYSTEM could do %s", name)
		}
	}
	if !signing().Ready() {
		t.Fatalf("check 4b: the signature changed")
	}

	// 5. The plan row reads the last runs (built as on Express), and the Installed helpers row lists it.
	pf, err := collect.MaintenancePlan(ctx, db, dbName)
	if err != nil {
		t.Fatal(err)
	}
	pf.EngineEdition = 4
	pf.Proc = readMaintFor(ctx, db, dbName)
	plan := domain.BuildMaintenancePlan(pf, domain.PlanAlerts{})
	t.Logf("5. plan: %s", plan.Note)
	for _, piece := range plan.Pieces {
		t.Logf("   %s: %s; last run %s (%s, %s); %s", piece.Title, piece.Status, piece.LastRun, piece.LastOutcome, piece.LastDuration, piece.Note)
	}
	if got := planStatus(plan, domain.PieceFullBackup); got != domain.PlanFailing {
		t.Errorf("check 5: full backup %s", got)
	}
	for _, id := range []string{domain.PieceIntegrity, domain.PieceStatistics, domain.PieceCleanup} {
		if got := planStatus(plan, id); got != domain.PlanInPlace {
			t.Errorf("check 5: %s %s", id, got)
		}
	}
	h, err := svc.Helpers(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("5. installed helpers: %s", h.Sentence)
	for _, x := range h.Helpers {
		if x.ID == "maintenance-proc" {
			t.Logf("   %s [%s] %s %q", x.Title, x.State, x.Sentence, x.Details)
			if x.State != domain.HelperInstalled || !strings.Contains(x.Sentence, "Last failure: ") || strings.Contains(x.Sentence, "not ready") {
				t.Errorf("check 5: helper line %+v", x)
			}
		}
	}

	// 6. Remove leaves nothing in master: no procedure, tables, certificate or login, and SYSTEM's
	// master user only when it was there before.
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET BackupFolder = @p1 WHERE DatabaseName = @p2", folder, dbName)
	res, err = svc.ApplyFixWithOptions(ctx, srv.ID, dbName, domain.FixRemoveMaintenanceProc, map[string]string{domain.OptionProcRemove: domain.MaintRemoveAll})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	left := kakapoLeft()
	systemUserAfter := count("SELECT COUNT(*) FROM master.sys.database_principals WHERE sid = SUSER_SID(N'NT AUTHORITY\\SYSTEM')")
	t.Logf("6. remove: verified %v; %s; kakapo objects, certificates and logins left: %d; SYSTEM's master user %d before, %d after",
		res.Verified, res.After, left, systemUserBefore, systemUserAfter)
	if !res.Verified || left != 0 || systemUserAfter != systemUserBefore {
		t.Fatalf("check 6 failed")
	}
	changes, err := st.ListChanges(ctx, srv.ID, dbName, 10)
	if err == nil {
		for _, c := range changes {
			t.Logf("   change row: %s %s: %s -> %s", c.ChangeType, c.Object, c.Description, c.Result)
		}
	}
}

func hasRun(rs []domain.MaintRun, task, outcome string) bool {
	for _, r := range rs {
		if r.Task == task && r.Outcome == outcome {
			return true
		}
	}
	return false
}

func planStatus(plan domain.MaintenancePlan, id string) string {
	for _, p := range plan.Pieces {
		if p.ID == id {
			return p.Status
		}
	}
	return ""
}

// missingDrive is a drive letter with no drive behind it on this machine (the server is local).
func missingDrive(t *testing.T) string {
	for c := 'Q'; c <= 'Z'; c++ {
		d := fmt.Sprintf(`%c:\`, c)
		if _, err := os.Stat(d); err != nil {
			return d
		}
	}
	t.Skip("no free drive letter to point a backup at")
	return ""
}
