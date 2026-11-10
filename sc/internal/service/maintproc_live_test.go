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

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// maintLiveDB is the throwaway database the live test creates and drops.
const maintLiveDB = "kakapo_maint_test"

// TestMaintProcLive runs phase 5p's live checks (docs/phase5p-contract.md, Done when) through the real
// fixes, on a throwaway database, when SQLMONITOR_TEST_HOST (Windows authentication) names a server and
// SQLMONITOR_TEST_MAINT=1 allows writing to it: it creates kakapo_maint_test, installs the procedure in
// master, calls it, removes it, drops the database and deletes its backup files. It refuses to start when
// anything named kakapo_* is already in master or the database exists. The procedure is not limited to
// Express, so it is proven on any edition; the plan row is built as Express would build it.
func TestMaintProcLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_MAINT") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_MAINT=1 to install kakapo's maintenance procedure on a live instance")
	}
	ctx := context.Background()
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	// Closed last: t.Cleanup runs after the deferred calls, and the cleanup below needs the connection.
	t.Cleanup(func() { db.Close() })
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
	if n := count("SELECT COUNT(*) FROM master.sys.objects WHERE name LIKE 'kakapo[_]%'"); n != 0 {
		t.Fatalf("master already holds %d kakapo_* objects: this test does not touch an install it did not make", n)
	}
	if n := count("SELECT COUNT(*) FROM sys.databases WHERE name = @p1", maintLiveDB); n != 0 {
		t.Fatalf("%s exists already", maintLiveDB)
	}
	var folder string
	if err := db.QueryRowContext(ctx, "SELECT CAST(SERVERPROPERTY('InstanceDefaultBackupPath') AS nvarchar(400))").Scan(&folder); err != nil || folder == "" {
		t.Fatalf("default backup folder: %v", err)
	}
	folder = strings.TrimRight(folder, `\`)
	dir := folder + `\` + maintLiveDB

	exec("CREATE DATABASE [" + maintLiveDB + "]; ALTER DATABASE [" + maintLiveDB + "] SET RECOVERY FULL;")
	t.Cleanup(func() {
		ctx := context.Background()
		for _, q := range []string{
			"IF OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P') IS NOT NULL DROP PROCEDURE dbo.kakapo_Maintenance;",
			"IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceLog', N'U') IS NOT NULL DROP TABLE master.dbo.kakapo_MaintenanceLog;",
			"IF OBJECT_ID(N'master.dbo.kakapo_MaintenanceSettings', N'U') IS NOT NULL DROP TABLE master.dbo.kakapo_MaintenanceSettings;",
		} {
			if _, err := db.ExecContext(ctx, "USE master; "+q); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
		if _, err := db.ExecContext(ctx, "USE master; IF DB_ID(N'"+maintLiveDB+"') IS NOT NULL BEGIN ALTER DATABASE ["+maintLiveDB+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+maintLiveDB+"]; END;"); err != nil {
			t.Logf("cleanup: %v", err)
		}
		if _, err := db.ExecContext(ctx, "EXEC msdb.dbo.sp_delete_database_backuphistory @database_name = N'"+maintLiveDB+"';"); err != nil {
			t.Logf("cleanup history: %v", err)
		}
		for _, ext := range []string{"bak", "trn"} {
			if _, err := db.ExecContext(ctx, "EXEC master.dbo.xp_delete_file 0, @p1, @p2, N'2100-01-01T00:00:00', 0;", dir, ext); err != nil {
				t.Logf("cleanup files: %v", err)
			}
		}
	})
	exec("USE [" + maintLiveDB + "]; CREATE TABLE dbo.Item (ID int IDENTITY PRIMARY KEY, Code nvarchar(20) NOT NULL, Qty int NOT NULL); " +
		"INSERT dbo.Item (Code, Qty) SELECT TOP (5000) CONCAT(N'IT', ROW_NUMBER() OVER (ORDER BY (SELECT NULL))), 1 FROM sys.all_objects AS a CROSS JOIN sys.all_objects AS b; " +
		"CREATE STATISTICS st_qty ON dbo.Item (Qty); UPDATE dbo.Item SET Qty = 2 WHERE ID % 2 = 0; USE master;")

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "SQL1", Host: host, Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, fakeCreds{})
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
		r, err := collect.ReadMaintRuns(ctx, db, maintLiveDB, after)
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
	call := func() (int64, []domain.MaintRun, error) {
		m := mark()
		err := collect.ExecFix(ctx, db, "EXEC master.dbo.kakapo_Maintenance;")
		return m, runs(m), err
	}

	// 1. Install it, with Run now: a full backup and a statistics row in the log. Statistics are set an
	// hour ago so they are in their window; the maintenance day is three days on, so it is not today's.
	now := clock()
	day := domain.CheckdbDays[(int(now.Weekday())+3)%7]
	opts := map[string]string{domain.OptionProcFolder: folder, domain.OptionProcRunNow: "true", domain.OptionProcDay: day,
		domain.OptionProcStatsTime: now.Add(-time.Hour).Format("15:04")}
	p, err := svc.PreviewFixWithOptions(ctx, srv.ID, maintLiveDB, domain.FixInstallMaintenanceProc, opts)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	t.Logf("preview: %d changes, room %+v, first warning: %s", len(p.Changes), p.Room, p.Warnings[0])
	start := mark()
	res, err := svc.ApplyFixWithOptions(ctx, srv.ID, maintLiveDB, domain.FixInstallMaintenanceProc, p.Options)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	first := runs(start)
	t.Logf("1. install: verified %v; %s\n   log: %s", res.Verified, res.After, words(first))
	if !res.Verified || !hasRun(first, domain.PieceFullBackup, domain.MaintOK) || !hasRun(first, domain.PieceStatistics, domain.MaintOK) {
		t.Fatalf("check 1 failed")
	}
	for _, r := range first {
		if r.Task == domain.PieceFullBackup && !strings.HasPrefix(r.File, dir+`\`+maintLiveDB+"_FULL_") {
			t.Errorf("full backup file %q", r.File)
		}
	}

	// 2. A second call within 15 minutes does nothing heavy and takes a log backup; a third does nothing.
	_, second, err := call()
	t.Logf("2. second call (err %v): %s", err, words(second))
	if err != nil || len(second) != 1 || second[0].Task != domain.PieceLogBackup || second[0].Outcome != domain.MaintOK {
		t.Fatalf("check 2 failed")
	}
	_, third, err := call()
	t.Logf("2. third call (err %v): %q", err, words(third))
	if err != nil || len(third) != 0 {
		t.Fatalf("check 2 (third call) failed")
	}

	// 3. With the heavy lock held by another session, the heavy work is skipped; the log backup still runs.
	// The check is made due (its day today, its time half an hour ago), the last log backup 20 minutes old.
	now = clock()
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET CheckDay = @p1, CheckTime = @p2 WHERE DatabaseName = @p3",
		int(now.Weekday())+1, now.Add(-30*time.Minute).Format("15:04"), maintLiveDB)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(MINUTE, -20, StartTime) WHERE DatabaseName = @p1 AND Task = 'log-backup'", maintLiveDB)
	// The check's rows so far (call 1 skipped it, outside the old day's window) go back a day, as if from before its new time.
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -1, StartTime) WHERE DatabaseName = @p1 AND Task = 'integrity-check'", maintLiveDB)
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rc int
	if err := holder.QueryRowContext(ctx, "USE master; DECLARE @rc int; EXEC @rc = sys.sp_getapplock @Resource = N'kakapo_Maintenance heavy "+maintLiveDB+"', @LockMode = 'Exclusive', @LockOwner = 'Session', @LockTimeout = 0; SELECT @rc;").Scan(&rc); err != nil || rc < 0 {
		t.Fatalf("hold the lock: %v %d", err, rc)
	}
	_, locked, err := call()
	t.Logf("3. call with the heavy lock held elsewhere (err %v): %s", err, words(locked))
	holder.ExecContext(ctx, "EXEC sys.sp_releaseapplock @Resource = N'kakapo_Maintenance heavy "+maintLiveDB+"', @LockOwner = 'Session'; USE ["+maintLiveDB+"];")
	holder.Close()
	if err != nil || !hasRun(locked, domain.PieceIntegrity, domain.MaintSkipped) || hasRun(locked, domain.PieceIntegrity, domain.MaintOK) ||
		!hasRun(locked, domain.PieceLogBackup, domain.MaintOK) || !strings.Contains(words(locked), "another call holds the lock") {
		t.Fatalf("check 3 failed")
	}
	_, freed, err := call()
	t.Logf("3. next call, lock free (err %v): %s", err, words(freed))
	if err != nil || !hasRun(freed, domain.PieceIntegrity, domain.MaintOK) {
		t.Fatalf("check 3 (the skipped check runs at the next call) failed")
	}

	// 4. A failing task: the backup folder on a drive that is not there. The full backup is made due (its
	// last run two days back), the cleanup too (its last run eight days back), which runs after it.
	missing := missingDrive(t) + `kakapo_maint_missing`
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET BackupFolder = @p1 WHERE DatabaseName = @p2", missing, maintLiveDB)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -2, StartTime) WHERE DatabaseName = @p1 AND Task = 'full-backup'", maintLiveDB)
	exec("UPDATE master.dbo.kakapo_MaintenanceLog SET StartTime = DATEADD(DAY, -8, StartTime) WHERE Task = 'history-cleanup'")
	_, failing, err := call()
	t.Logf("4. call with the folder on a missing drive: raised %v\n   log: %s", err, words(failing))
	if err == nil || !strings.Contains(err.Error(), "kakapo_Maintenance failed") || !strings.Contains(err.Error(), "full-backup") ||
		!hasRun(failing, domain.PieceFullBackup, domain.MaintFailed) || !hasRun(failing, domain.PieceCleanup, domain.MaintOK) {
		t.Fatalf("check 4 failed")
	}

	// 5. The plan row reads the last runs (built as on Express), and the Installed helpers row lists it.
	pf, err := collect.MaintenancePlan(ctx, db, maintLiveDB)
	if err != nil {
		t.Fatal(err)
	}
	pf.EngineEdition = 4
	pf.Proc = readMaintFor(ctx, db, maintLiveDB)
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
			t.Logf("   %s [%s] %s", x.Title, x.State, x.Sentence)
			if x.State != domain.HelperInstalled || !strings.Contains(x.Sentence, "Last failure: ") {
				t.Errorf("check 5: helper line %+v", x)
			}
		}
	}

	// 6. Remove leaves nothing in master.
	exec("UPDATE master.dbo.kakapo_MaintenanceSettings SET BackupFolder = @p1 WHERE DatabaseName = @p2", folder, maintLiveDB)
	res, err = svc.ApplyFixWithOptions(ctx, srv.ID, maintLiveDB, domain.FixRemoveMaintenanceProc, map[string]string{domain.OptionProcRemove: domain.MaintRemoveAll})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	left := count("SELECT COUNT(*) FROM master.sys.objects WHERE name LIKE 'kakapo[_]%'")
	t.Logf("6. remove: verified %v; %s; kakapo_* objects left in master: %d", res.Verified, res.After, left)
	if !res.Verified || left != 0 {
		t.Fatalf("check 6 failed")
	}
	changes, err := st.ListChanges(ctx, srv.ID, maintLiveDB, 10)
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
