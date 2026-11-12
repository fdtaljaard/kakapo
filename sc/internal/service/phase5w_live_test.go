package service

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// liveService is a service on a fresh store with the live host registered (Windows authentication).
func liveService(t *testing.T, host string) (*Service, int64, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "SQL1", Host: host, Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(st, fakeCreds{}, "live-test"), srv.ID, db
}

// TestPhase5wReadOnlyLive runs phase 5w's read-only checks (docs/phase5w-contract.md, Done when) when
// SQLMONITOR_TEST_HOST names a server and SQLMONITOR_TEST_5W is 1 or "readonly": the baseline read with every
// area read, the screen twice, and the diff of the two reads (nothing changed in between unless someone else
// changed something). It writes nothing to the server.
func TestPhase5wReadOnlyLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if mode := os.Getenv("SQLMONITOR_TEST_5W"); host == "" || (mode != "1" && mode != "readonly") {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5W=1 (or readonly) to run phase 5w's read-only checks")
	}
	ctx := context.Background()
	s, serverID, db := liveService(t, host)

	f, err := collect.ReadBaseline(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Errors) != 0 || len(f.Items) < 100 || f.SQLMemoryMB == nil || f.TargetMemoryMB == nil {
		t.Fatalf("baseline: %d items, errors %v", len(f.Items), f.Errors)
	}
	for _, k := range []string{domain.ItemMaxServerMemory, domain.ItemVersion, domain.ItemCU, domain.ItemStartTime, domain.ItemTraceFlags,
		domain.ItemChangeLog, "db/master/recovery_model", "db/tempdb/data_files"} {
		if f.Items[k] == nil {
			t.Errorf("baseline lacks %s", k)
		}
	}
	if v := f.Items[domain.ItemChangeLog]; v == nil || *v != "absent" {
		t.Errorf("step one installs nothing: kakapo/change_log = %v", v)
	}

	p1, err := s.Changes(ctx, serverID, ChangesQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// The problem two minutes from now (server time, as the server is this machine): both reads are in its window.
	p2, err := s.Changes(ctx, serverID, ChangesQuery{ProblemAt: time.Now().Add(2 * time.Minute).Format("2006-01-02T15:04")})
	if err != nil {
		t.Fatal(err)
	}
	reads, err := s.store.BaselineReads(ctx, serverID)
	if err != nil || len(reads) != 2 {
		t.Fatalf("two reads: %+v %v", reads, err)
	}
	if reads[0].Reason != domain.BaselineFirst || reads[1].Reason != domain.BaselineRead {
		t.Errorf("reasons %s, %s", reads[0].Reason, reads[1].Reason)
	}
	bs, err := s.store.Baselines(ctx, serverID, []int64{reads[0].BaselineID, reads[1].BaselineID})
	if err != nil {
		t.Fatal(err)
	}
	diff := domain.DiffBaselines(bs[reads[0].BaselineID], bs[reads[1].BaselineID], reads[0].ReadAt, reads[1].ReadAt)
	if reads[0].BaselineID == reads[1].BaselineID && len(diff) != 0 {
		t.Errorf("same snapshot, yet a diff: %+v", diff)
	}
	t.Logf("snapshots %d and %d, %d differences: %v", reads[0].BaselineID, reads[1].BaselineID, len(diff), diff)
	if !p1.Live || !p2.Live || p1.Verdict == "" || p2.ProblemAt == nil || !strings.Contains(p2.Verdict, "7 days before") ||
		len(p1.Memory.Points) != 1 || len(p2.Memory.Points) != 2 || p2.WatchedSince == nil {
		t.Fatalf("screen: live %v %v, verdicts %q %q, problem %v, points %d %d", p1.Live, p2.Live, p1.Verdict, p2.Verdict, p2.ProblemAt,
			len(p1.Memory.Points), len(p2.Memory.Points))
	}
	t.Logf("verdict: %s | %s; notes %v; %d rows listed", p1.Verdict, p2.Verdict, p2.Notes, p1.Listed)
}

// TestPhase5wLive runs phase 5w step one's write checks (docs/phase5w-contract.md, Done when) when
// SQLMONITOR_TEST_HOST names the shared test instance and SQLMONITOR_TEST_5W=1 allows writes there. It changes
// one server setting and puts it back: cost threshold for parallelism is changed in sqlcmd (outside kakapo)
// and must be listed with the login, host and program from the trace, dated between two of kakapo's reads;
// then kakapo's own fix sets it, and kakapo's log must hold started and ended with before and after. The
// original value is put back with sqlcmd at the end, whatever happened.
func TestPhase5wLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5W") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5W=1 to run phase 5w's write checks on the shared test instance")
	}
	if _, err := exec.LookPath("sqlcmd"); err != nil {
		t.Skip("sqlcmd is not on the PATH: the outside change is made with it")
	}
	ctx := context.Background()
	s, serverID, db := liveService(t, host)

	var original, advanced, maxdop int64
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'cost threshold for parallelism'),
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'show advanced options'),
		(SELECT CONVERT(bigint, value_in_use) FROM sys.configurations WHERE name = N'max degree of parallelism')`).Scan(&original, &advanced, &maxdop); err != nil {
		t.Fatal(err)
	}
	if original > 5 || maxdop == 1 {
		t.Skipf("cost threshold is %d and MAXDOP %d: kakapo's fix would not fire (INS05 needs 5 or less and MAXDOP not 1)", original, maxdop)
	}
	var dbName string
	if err := db.QueryRowContext(ctx, `SELECT TOP (1) name FROM sys.databases WHERE database_id > 4 AND state = 0 AND is_read_only = 0
		AND is_in_standby = 0 ORDER BY name`).Scan(&dbName); err != nil {
		t.Fatalf("no online user database to apply the fix from: %v", err)
	}
	sqlcmd := func(value int64) {
		t.Helper()
		q := "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'cost threshold for parallelism', " +
			strconv.FormatInt(value, 10) + "; RECONFIGURE; EXEC sp_configure 'show advanced options', " + strconv.FormatInt(advanced, 10) + "; RECONFIGURE;"
		out, err := exec.Command("sqlcmd", "-S", host, "-E", "-C", "-b", "-Q", q).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlcmd: %v\n%s", err, out)
		}
	}
	defer func() {
		out, err := exec.Command("sqlcmd", "-S", host, "-E", "-C", "-b", "-Q",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'cost threshold for parallelism', "+
				strconv.FormatInt(original, 10)+"; RECONFIGURE; EXEC sp_configure 'show advanced options', "+strconv.FormatInt(advanced, 10)+"; RECONFIGURE;").CombinedOutput()
		if err != nil {
			t.Errorf("could not put cost threshold back to %d: %v\n%s", original, err, out)
		}
	}()

	// Before: kakapo's first read.
	if _, err := s.Changes(ctx, serverID, ChangesQuery{}); err != nil {
		t.Fatal(err)
	}
	outside := original - 1
	before := time.Now().UTC()
	sqlcmd(outside)
	after := time.Now().UTC()

	// The screen lists it: who from the trace, at the trace's time, between the two reads.
	page, err := s.Changes(ctx, serverID, ChangesQuery{Who: "outside"})
	if err != nil {
		t.Fatal(err)
	}
	var row *domain.AuditRow
	for i := range page.Rows {
		if page.Rows[i].Item == domain.ItemCostThreshold && page.Rows[i].After == strconv.FormatInt(outside, 10) {
			row = &page.Rows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("the outside change is not listed: %+v (notes %v)", page.Rows, page.Notes)
	}
	reads, err := s.store.BaselineReads(ctx, serverID)
	if err != nil || len(reads) != 2 {
		t.Fatalf("reads %+v %v", reads, err)
	}
	if row.Source != domain.SourceTrace || row.At == nil || !strings.Contains(strings.ToUpper(row.Who), "SQLCMD") ||
		row.Before != strconv.FormatInt(original, 10) || row.Flag != domain.FlagAmber || row.ByKakapo {
		t.Fatalf("the outside change: %+v", *row)
	}
	slack := 2 * time.Minute
	if row.At.Before(reads[0].ReadAt.Add(-slack)) || row.At.After(reads[1].ReadAt.Add(slack)) || row.At.Before(before.Add(-slack)) || row.At.After(after.Add(slack)) {
		t.Errorf("dated %v, not between the reads %v and %v", row.At, reads[0].ReadAt, reads[1].ReadAt)
	}
	t.Logf("outside: %s | %s | at %v, between %v and %v", row.Headline, row.Who, row.At, reads[0].ReadAt, reads[1].ReadAt)

	// kakapo's own fix: started and ended, with before and after.
	res, err := s.ApplyFix(ctx, serverID, dbName, "cost-threshold")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified {
		t.Fatalf("fix: %+v", res)
	}
	logRows, err := s.store.ListActionLog(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	acts := domain.GroupActions(logRows)
	if len(acts) != 1 || acts[0].End == nil {
		t.Fatalf("kakapo's log: %+v", logRows)
	}
	a := acts[0]
	if a.Start.Phase != domain.ActionStarted || a.Start.FixID != "cost-threshold" || a.Start.ValueBefore != strconv.FormatInt(outside, 10) ||
		a.Start.WindowsUser == "" || a.Start.SQLLogin == "" || a.Start.KakapoHost == "" || a.Start.KakapoVersion != "live-test" ||
		a.End.Outcome != domain.OutcomeVerified || a.End.ValueAfter != "50" || a.End.ChangeIDs == "" || a.End.DurationMs == nil ||
		!a.Start.StartedAt.Before(*a.End.EndedAt) {
		t.Fatalf("started %+v\nended %+v", a.Start, *a.End)
	}
	page, err = s.Changes(ctx, serverID, ChangesQuery{Who: "kakapo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0].Item != domain.ItemCostThreshold || page.Rows[0].After != "50" || page.Rows[0].Outcome != domain.OutcomeVerified ||
		!strings.HasPrefix(page.Rows[0].Who, "Set by kakapo: ") {
		t.Fatalf("kakapo's change on the screen: %+v", page.Rows)
	}
	t.Logf("kakapo: %s | %s | %s", page.Rows[0].Headline, page.Rows[0].Who, page.Rows[0].Result)
	reads, _ = s.store.BaselineReads(ctx, serverID)
	reasons := []string{}
	for _, r := range reads {
		reasons = append(reasons, r.Reason)
	}
	if strings.Join(reasons, ",") != "first,read,before-action,after-action,read" {
		t.Errorf("reads %v", reasons)
	}
	t.Logf("copy:\n%s", page.Copy)
}
