package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// TestPhase5wStepTwoLive runs phase 5w step two's write checks (docs/phase5w-contract.md, Done when) on the
// shared test instance (SQLMONITOR_TEST_HOST, SQLMONITOR_TEST_5W=1): install kakapo's change log; change cost
// threshold in sqlcmd and set it back with kakapo's fix, whose started and ended rows must be in kakapo's log
// and in master.dbo.kakapo_ChangeLog; a DELETE must be refused by the trigger; a TRUNCATE must be put back by
// the next read and flagged red; then remove the log, whose local record stays complete. cost threshold and
// the table are put back as they were, whatever happened.
func TestPhase5wStepTwoLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5W") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5W=1 to run phase 5w step two's write checks on the shared test instance")
	}
	if _, err := exec.LookPath("sqlcmd"); err != nil {
		t.Skip("sqlcmd is not on the PATH: the outside change is made with it")
	}
	ctx := context.Background()
	s, serverID, db := liveService(t, host)
	if cl, err := collect.ReadChangeLog(ctx, db); err != nil || cl.Table || cl.Trigger {
		t.Fatalf("kakapo's change log must not be on the server before the test: %+v %v", cl, err)
	}

	var original, advanced, maxdop int64
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'cost threshold for parallelism'),
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'show advanced options'),
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'max degree of parallelism')`).Scan(&original, &advanced, &maxdop); err != nil {
		t.Fatal(err)
	}
	if original > 5 || maxdop == 1 {
		t.Skipf("cost threshold is %d and MAXDOP %d: kakapo's fix would not fire", original, maxdop)
	}
	var dbName string
	if err := db.QueryRowContext(ctx, `SELECT TOP (1) name FROM sys.databases WHERE database_id > 4 AND state = 0 AND is_read_only = 0
		AND is_in_standby = 0 ORDER BY name`).Scan(&dbName); err != nil {
		t.Fatalf("no online user database to apply the fixes from: %v", err)
	}
	setCost := func(value int64) error {
		q := "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'cost threshold for parallelism', " +
			strconv.FormatInt(value, 10) + "; RECONFIGURE; EXEC sp_configure 'show advanced options', " + strconv.FormatInt(advanced, 10) + "; RECONFIGURE;"
		out, err := exec.Command("sqlcmd", "-S", host, "-E", "-C", "-b", "-Q", q).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	defer func() {
		if err := setCost(original); err != nil {
			t.Errorf("could not put cost threshold back to %d: %v", original, err)
		}
		// The table, if the test left it: dropped outside kakapo.
		out, err := exec.Command("sqlcmd", "-S", host, "-E", "-C", "-b", "-d", "master", "-Q",
			"IF OBJECT_ID(N'dbo.kakapo_ChangeLog_AppendOnly', N'TR') IS NOT NULL DROP TRIGGER dbo.kakapo_ChangeLog_AppendOnly; "+
				"IF OBJECT_ID(N'dbo.kakapo_ChangeLog', N'U') IS NOT NULL DROP TABLE dbo.kakapo_ChangeLog;").CombinedOutput()
		if err != nil {
			t.Errorf("could not take the table off: %v\n%s", err, out)
		}
	}()
	keys := func() map[string]bool {
		t.Helper()
		k, installed, err := collect.ReadChangeLogKeys(ctx, db)
		if err != nil || !installed {
			t.Fatalf("the server's keys: installed %v, %v", installed, err)
		}
		return k
	}
	local := func() []domain.ActionLogRow {
		t.Helper()
		rows, err := s.store.ListActionLog(ctx, serverID)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	lastWarning := func(p domain.FixPreview) string {
		if len(p.Warnings) == 0 {
			return ""
		}
		return p.Warnings[len(p.Warnings)-1]
	}

	// B7: before the log is there, a fix's preview says so.
	if _, err := s.Changes(ctx, serverID, ChangesQuery{}); err != nil {
		t.Fatal(err)
	}
	if err := setCost(original - 1); err != nil {
		t.Fatal(err)
	}
	p, err := s.PreviewFix(ctx, serverID, dbName, "cost-threshold")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lastWarning(p), "kakapo's log is not on this server yet") {
		t.Fatalf("no B7 warning: %q", p.Warnings)
	}
	t.Logf("B7: %s", lastWarning(p))

	// 1. Install the log: the table, the trigger, the property, and kakapo's own log copied.
	res, err := s.ApplyFix(ctx, serverID, dbName, domain.FixInstallChangeLog)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified {
		t.Fatalf("install: %+v", res)
	}
	t.Logf("install: %s -> %s", res.Before, res.After)
	cl, err := collect.ReadChangeLog(ctx, db)
	if err != nil || !cl.Table || !cl.Trigger || cl.TriggerOff || !cl.Property {
		t.Fatalf("after the install: %+v %v", cl, err)
	}
	if k, l := keys(), local(); len(k) != len(l) || len(l) != 2 {
		t.Fatalf("after the install the server holds %d rows, kakapo %d", len(k), len(l))
	}
	if p, err := s.PreviewFix(ctx, serverID, dbName, "cost-threshold"); err != nil || strings.HasPrefix(lastWarning(p), "kakapo's log is not") {
		t.Fatalf("installed, yet the preview warns: %q %v", p.Warnings, err)
	}

	// 2. The outside change is listed; kakapo's fix writes started and ended locally and in master.
	page, err := s.Changes(ctx, serverID, ChangesQuery{Who: "outside"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range page.Rows {
		found = found || (r.Item == domain.ItemCostThreshold && r.After == strconv.FormatInt(original-1, 10) && r.Source == domain.SourceTrace)
	}
	if !found {
		t.Errorf("the outside change is not listed from the trace: %+v", page.Rows)
	}
	res, err = s.ApplyFix(ctx, serverID, dbName, "cost-threshold")
	if err != nil || !res.Verified {
		t.Fatalf("cost threshold: %+v %v", res, err)
	}
	acts := domain.GroupActions(local())
	cost := acts[len(acts)-1]
	if cost.Start.FixID != "cost-threshold" || cost.End == nil || cost.End.Outcome != domain.OutcomeVerified {
		t.Fatalf("kakapo's log: %+v", acts)
	}
	k := keys()
	if !k[domain.ChangeLogKey(cost.Start.ActionID, domain.ActionStarted)] || !k[domain.ChangeLogKey(cost.Start.ActionID, domain.ActionEnded)] {
		t.Fatalf("the cost threshold action is not on the server: %v", k)
	}
	srvRows, err := collect.ReadChangeLogRows(ctx, db, serverID)
	if err != nil {
		t.Fatal(err)
	}
	seenEnded := false
	for _, r := range srvRows {
		if strings.EqualFold(r.ActionID, cost.Start.ActionID) && r.Phase == domain.ActionEnded {
			seenEnded = true
			if r.ValueBefore != strconv.FormatInt(original-1, 10) || r.ValueAfter != "50" || r.Outcome != domain.OutcomeVerified ||
				r.WindowsUser == "" || r.SQLLogin == "" || r.KakapoHost == "" || r.KakapoVersion != "live-test" {
				t.Fatalf("the server's ended row: %+v", r)
			}
			t.Logf("server ended row: %s, %s -> %s, %s, %d ms", r.FixID, r.ValueBefore, r.ValueAfter, r.Outcome, *r.DurationMs)
		}
	}
	if !seenEnded {
		t.Fatal("the server's rows lack the ended row")
	}

	// 3. The trigger refuses a DELETE (and an UPDATE).
	for _, q := range []string{"DELETE FROM master.dbo.kakapo_ChangeLog;", "UPDATE master.dbo.kakapo_ChangeLog SET Result = N'x';"} {
		_, err := db.ExecContext(ctx, q)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s was not refused: %v", q, err)
		}
		t.Logf("refused: %v", err)
	}
	if len(keys()) != len(local()) {
		t.Fatal("rows went missing after the refused DELETE")
	}

	// 4. A TRUNCATE bypasses the trigger: the next read puts the rows back and flags it red.
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE master.dbo.kakapo_ChangeLog;"); err != nil {
		t.Fatal(err)
	}
	page, err = s.Changes(ctx, serverID, ChangesQuery{Flagged: true})
	if err != nil {
		t.Fatal(err)
	}
	if k, l := keys(), local(); len(k) != len(l) {
		t.Fatalf("after the TRUNCATE and a read the server holds %d rows, kakapo %d", len(k), len(l))
	}
	lost := false
	for _, r := range page.Rows {
		if r.Rule == "log-removed" && r.Flag == domain.FlagRed && strings.HasPrefix(r.Headline, "kakapo's log on this server lost ") {
			lost = true
			t.Logf("flagged: %s | %s", r.Headline, r.Who)
		}
	}
	if !lost {
		t.Fatalf("the TRUNCATE is not flagged: %+v", page.Rows)
	}

	// 5. Remove the log: the table goes, kakapo's own record is complete.
	res, err = s.ApplyFix(ctx, serverID, dbName, domain.FixRemoveChangeLog)
	if err != nil || !res.Verified {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if cl, err := collect.ReadChangeLog(ctx, db); err != nil || cl.Table || cl.Trigger {
		t.Fatalf("after the remove: %+v %v", cl, err)
	}
	acts = domain.GroupActions(local())
	if len(acts) != 3 || acts[2].Start.FixID != domain.FixRemoveChangeLog {
		t.Fatalf("kakapo's log: %+v", acts)
	}
	for _, a := range acts {
		if a.End == nil || a.End.Outcome != domain.OutcomeVerified {
			t.Fatalf("incomplete: %+v", a)
		}
	}
	page, err = s.Changes(ctx, serverID, ChangesQuery{})
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, r := range page.Rows {
		if r.Item == domain.ItemChangeLog && r.After == domain.ChangeLogAbsent {
			removed = r.Flag == domain.FlagGrey && r.ByKakapo
			t.Logf("removal: %s | %s | %s", r.Headline, r.Who, r.Flag)
		}
	}
	if !removed {
		t.Errorf("the removal by kakapo is not listed grey: %+v", page.Rows)
	}
	var u domain.UpkeepFacts
	s.chg02Facts(ctx, db, serverID, &u)
	if !u.KakapoActed || u.ChangeLog != domain.ChangeLogAbsent {
		t.Errorf("CHG02 facts %+v", u)
	}
	t.Logf("copy:\n%s", page.Copy)
}
