package collect

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The plan read and the cleanup read are tagged and read-only (docs/phase5b-contract.md, Tests),
// each area in its own TRY with its error column.
func TestMaintenanceReads(t *testing.T) {
	for name, q := range map[string]string{"maintenance plan": Tag(maintenancePlanQuery), "history cleanup": Tag(historyCleanupQuery)} {
		suReadOnly(t, name, q)
	}
	// The last good check, then the jobs (not on Express).
	if n := strings.Count(maintenancePlanQuery, "BEGIN TRY"); n != 2 {
		t.Errorf("plan read: %d TRY blocks", n)
	}
	for _, want := range []string{"@checkError", "@jobsError", "IF @express = 0", "DATABASEPROPERTYEX(@db, 'LastGoodCheckDbTime')",
		"OBJECT_ID(N'master.dbo.IndexOptimize', N'P')", "j.name LIKE N'kakapo: %'", "jh.step_id = 0", "ORDER BY jh.run_date DESC, jh.run_time DESC",
		"js.next_run_date > 0", "msdb.dbo.sysschedules", "s.command LIKE N'%DatabaseIntegrityCheck%'", "s.command LIKE N'%sp[_]updatestats%'",
		"s.command LIKE N'%IndexOptimize%'", "s.command LIKE N'%sp[_]delete[_]backuphistory%'", "s.command LIKE N'%CommandLog%'",
		"recovery_model_desc"} {
		if !strings.Contains(maintenancePlanQuery, want) {
			t.Errorf("plan read lacks %q", want)
		}
	}
	// The history, CommandLog and the Agent each in its own TRY.
	if n := strings.Count(historyCleanupQuery, "BEGIN TRY"); n != 3 {
		t.Errorf("cleanup read: %d TRY blocks", n)
	}
	for _, want := range []string{"@historyError", "@commandLogError", "DATEADD(DAY, -@keepDays, GETDATE())", "b.backup_finish_date < @cutoff",
		"r.restore_date < @cutoff", "h.run_date < @cutoffDate", "OBJECT_ID(N'master.dbo.CommandLog', N'U')", "c.StartTime < @cutoff",
		"WHERE name = @job", "j.enabled = 1 AND j.name <> @job", "sys.dm_server_services"} {
		if !strings.Contains(historyCleanupQuery, want) {
			t.Errorf("cleanup read lacks %q", want)
		}
	}
	if got := agentTime(20261102, 13005); got == nil || !got.Equal(time.Date(2026, 11, 2, 1, 30, 5, 0, time.UTC)) {
		t.Errorf("agentTime %v", got)
	}
	if agentTime(0, 0) != nil {
		t.Error("no date, no time")
	}
}

// TestMaintenancePlanLive reads the plan and the history of one database on a real instance,
// read-only, when SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestMaintenancePlanLive(t *testing.T) {
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
	f, err := MaintenancePlan(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.BuildMaintenancePlan(f, domain.PlanAlerts{})
	for _, p := range plan.Pieces {
		t.Logf("%-24s %-10s %s %s %s %s", p.Title, p.Status, p.Job, p.Schedule, p.LastRun, p.Note)
	}
	h, err := ReadHistoryCleanupFacts(ctx, db, 30)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("history: %+v", h)
	if _, err := MaintenancePlan(ctx, db, "NoSuchDatabase_x9"); err == nil {
		t.Error("an unknown database should be an error")
	}
}
