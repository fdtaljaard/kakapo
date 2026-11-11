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

// q5tLiveDB is the throwaway database TestPhase5tLive creates and drops: its name carries the process id,
// so that two runs at once (a repeated launch) cannot meet in one database.
var q5tLiveDB = fmt.Sprintf("kakapo_5t_test_%d", os.Getpid())

// TestPhase5tLive runs phase 5t's live checks (docs/phase5t-contract.md, Done when) on a throwaway
// database when SQLMONITOR_TEST_HOST (Windows authentication) names a server and SQLMONITOR_TEST_5T=1
// allows writing to it. Query Store on; tables A and B (fragmented, scanned), C (small, scanned), D
// (big, only sought), E (big, scanned, statistics 66% out of date) and Big (long enough to stop). The
// page lists A, B, E and Big; Measure; the batch with a session killed mid-way (a failure that does not
// stop the next index) and then stopped (the one in flight keeps its progress, the next is not
// started); and the query page's verdict splitting at the change. Everything is dropped afterwards.
func TestPhase5tLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5T") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5T=1 to run phase 5t's checks on a live instance")
	}
	ctx := context.Background()
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", q5tLiveDB).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%s exists already (%v)", q5tLiveDB, err)
	}
	exec("CREATE DATABASE [" + q5tLiveDB + "]")
	defer func() {
		if _, err := db.ExecContext(ctx, "USE master; ALTER DATABASE ["+q5tLiveDB+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+q5tLiveDB+"];"); err != nil {
			t.Errorf("could not drop %s: %v", q5tLiveDB, err)
		}
	}()
	exec("ALTER DATABASE [" + q5tLiveDB + "] SET RECOVERY SIMPLE; ALTER DATABASE [" + q5tLiveDB + "] SET AUTO_UPDATE_STATISTICS OFF; " +
		"ALTER DATABASE [" + q5tLiveDB + "] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = ALL, INTERVAL_LENGTH_MINUTES = 1)")

	// One connection in the database for the set-up and the workload.
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cx := func(q string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	cx("USE [" + q5tLiveDB + "]")
	fill := func(table string, batches int) {
		cx("DECLARE @i int = 0; WHILE @i < " + fmt.Sprint(batches) + " BEGIN INSERT dbo." + table + " (pad) SELECT TOP (1000) 'x' FROM sys.all_columns; SET @i += 1; END;")
	}
	for _, c := range []struct {
		name    string
		batches int
	}{{"A", 30}, {"B", 30}, {"C", 5}, {"D", 30}, {"E", 30}, {"Big", 300}} {
		cx("CREATE TABLE dbo." + c.name + " (id uniqueidentifier NOT NULL DEFAULT NEWID() PRIMARY KEY, pad char(700) NOT NULL DEFAULT 'x')")
		fill(c.name, c.batches)
	}
	cx("UPDATE STATISTICS dbo.E")
	fill("E", 20) // 66% more rows with automatic updates off: 5j's "very changed"
	pkOf := func(table string) string {
		var name string
		if err := db.QueryRowContext(ctx, "SELECT name FROM ["+q5tLiveDB+"].sys.indexes WHERE object_id = OBJECT_ID(N'["+q5tLiveDB+"].dbo."+table+"') AND index_id = 1").Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}
	workload := func(runs int) {
		for i := 0; i < runs; i++ {
			for _, tbl := range []string{"A", "B", "C", "E", "Big"} {
				cx("SELECT COUNT(*) FROM dbo." + tbl + " WHERE pad = 'q'")
			}
			cx("SELECT COUNT(*) FROM dbo.D WHERE id = NEWID()")
		}
	}
	workload(20)
	cx("EXEC sp_query_store_flush_db")
	if os.Getenv("SQLMONITOR_TEST_5T_HOUR") == "1" {
		// The query page's before and after are read by the hour: the change must fall in a later hour than the
		// runs before it. Start this within a few minutes before an hour; it waits for the hour to turn.
		next := time.Now().Truncate(time.Hour).Add(time.Hour + 5*time.Second)
		t.Logf("waiting until %s so that the change falls in the next hour", next.Format("15:04:05"))
		time.Sleep(time.Until(next))
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
	svc := New(st, fakeCreds{})

	// 1. The page lists A, B, E and Big, not C (under 1,000 pages) or D (only sought).
	lens, err := svc.IndexLens(ctx, srv.ID, q5tLiveDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer: %s (%d queries read)", lens.Answer, lens.Queries)
	rows := map[string]domain.LensRow{}
	for _, r := range lens.Rows {
		t.Logf("  %s.%s pages %d (%.0f MB), %d queries, %.0f ms, stale %v: %s", r.Name, r.Index, r.Pages, r.SizeMB, r.QueryCount, r.TotalMs, r.Stale, r.StatsNote)
		rows[r.Table] = r
	}
	if lens.Reason != "" || len(lens.Rows) != 4 || rows["A"].Index != pkOf("A") || rows["B"].Index == "" || rows["E"].Index == "" || rows["Big"].Index == "" ||
		rows["C"].Index != "" || rows["D"].Index != "" {
		t.Fatalf("rows %+v (%s)", lens.Rows, lens.Reason)
	}
	if !rows["E"].Stale || rows["A"].Stale || rows["E"].StatsAction == nil || rows["A"].StatsAction == nil || rows["A"].QueryCount != 1 || len(rows["A"].Queries) != 1 || rows["A"].Queries[0].Runs < 20 {
		t.Errorf("statistics or queries: E %+v A %+v", rows["E"], rows["A"])
	}

	// 2. Measure A and B: a gain on both.
	for _, tbl := range []string{"A", "B"} {
		shape, err := svc.IndexShape(ctx, srv.ID, q5tLiveDB, "dbo", tbl, rows[tbl].Index)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("measured %s: %s (gain %v)", tbl, shape.Text, shape.Gain)
		if !shape.Measured || !shape.Gain {
			t.Errorf("%s: %+v", tbl, shape)
		}
	}

	batch := func(tables ...string) map[string]string {
		var list []domain.BatchIndex
		for _, tbl := range tables {
			list = append(list, domain.BatchIndex{Schema: "dbo", Table: tbl, Index: rows[tbl].Index})
		}
		return map[string]string{domain.OptionIndexes: domain.BatchIndexesOption(list)}
	}
	// 3. The preview: Big, A and B in the order given, E's old statistics named, the statement to copy.
	pv, err := svc.PreviewFixWithOptions(ctx, srv.ID, q5tLiveDB, domain.FixReorganizeIndexes, batch("Big", "A", "B", "E"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("preview: %s\n%s\nwarnings:\n  %s", pv.Lead, pv.Statement, strings.Join(pv.Warnings, "\n  "))
	if len(pv.Changes) != 4 || !strings.Contains(strings.Join(pv.Warnings, "\n"), "Old statistics: dbo.E.") {
		t.Errorf("preview %+v", pv)
	}

	// 4. Big first, A, B: a session killed during Big is Big's failure; A and B go on and are reorganised.
	res, err := svc.ApplyFixWithOptions(ctx, srv.ID, q5tLiveDB, domain.FixReorganizeIndexes, batch("Big", "A", "B"))
	if err != nil || !res.Pending || res.RunID == "" {
		t.Fatalf("apply %+v %v", res, err)
	}
	if _, err := svc.ApplyFixWithOptions(ctx, srv.ID, q5tLiveDB, domain.FixReorganizeIndexes, batch("A")); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("a second batch while one runs: %v", err)
	}
	killed := false
	for i := 0; i < 100 && !killed; i++ {
		time.Sleep(200 * time.Millisecond)
		var spid sql.NullInt64
		if err := db.QueryRowContext(ctx, `SELECT TOP (1) r.session_id FROM sys.dm_exec_requests AS r CROSS APPLY sys.dm_exec_sql_text(r.sql_handle) AS x
            WHERE x.text LIKE N'%REORGANIZE%' AND x.text NOT LIKE N'%dm_exec_requests%' AND r.session_id <> @@SPID`).Scan(&spid); err == nil && spid.Valid {
			if _, err := db.ExecContext(ctx, fmt.Sprintf("KILL %d", spid.Int64)); err == nil {
				killed = true
			}
		}
	}
	if !killed {
		t.Fatal("never saw the ALTER INDEX to kill")
	}
	run := waitBatch(t, svc, srv.ID, res.RunID, 5*time.Minute)
	logRun(t, "after a kill", run)
	if run.Result == nil || len(run.Items) != 3 || run.Items[0].State != domain.ItemFailed || run.Items[1].State != domain.ItemDone || run.Items[2].State != domain.ItemDone {
		t.Fatalf("run %+v", run)
	}
	if run.Result.Verified || !strings.Contains(run.Result.After, "2 reorganised") || !strings.Contains(run.Result.After, "1 failed") {
		t.Errorf("result %+v", run.Result)
	}
	changes, err := st.ListChanges(ctx, srv.ID, q5tLiveDB, 20)
	if err != nil || len(changes) != 2 {
		t.Fatalf("change log (a row for A and for B, none for the failed Big): %+v %v", changes, err)
	}

	// 5. Big again, then B: stopped while Big is in flight (its progress kept, a change row for it);
	// B is not started.
	cx("DECLARE @i int = 0; WHILE @i < 30 BEGIN INSERT dbo.Big (pad) SELECT TOP (1000) 'x' FROM sys.all_columns; SET @i += 1; END;")
	res, err = svc.ApplyFixWithOptions(ctx, srv.ID, q5tLiveDB, domain.FixReorganizeIndexes, batch("Big", "B"))
	if err != nil || !res.Pending {
		t.Fatalf("apply %+v %v", res, err)
	}
	for i := 0; i < 100; i++ {
		time.Sleep(200 * time.Millisecond)
		r, _ := svc.FixRunStatus(ctx, srv.ID, q5tLiveDB, res.RunID)
		if len(r.Items) > 0 && strings.HasPrefix(r.Items[0].Text, "reorganising") {
			break
		}
	}
	time.Sleep(2 * time.Second)
	if _, err := svc.StopFixRun(ctx, srv.ID, q5tLiveDB, res.RunID); err != nil {
		t.Fatal(err)
	}
	run = waitBatch(t, svc, srv.ID, res.RunID, 2*time.Minute)
	logRun(t, "after a stop", run)
	if run.Result == nil || len(run.Items) != 2 || run.Items[0].State != domain.ItemStopped || run.Items[1].State != domain.ItemStopped ||
		!strings.Contains(run.Items[1].Text, "not started") || run.Result.Verified || !run.Stopping {
		t.Fatalf("run %+v", run)
	}
	changes, _ = st.ListChanges(ctx, srv.ID, q5tLiveDB, 20)
	if len(changes) != 3 || !strings.Contains(changes[0].Result, "stopped part-way") {
		t.Errorf("change log after the stop: %+v", changes)
	}

	// 6. The query page of the query that reads A: the verdict splits at kakapo's change.
	time.Sleep(65 * time.Second) // a new Query Store interval, so the runs after the change fall in their own
	workload(30)
	cx("EXEC sp_query_store_flush_db")
	var hash string
	if err := db.QueryRowContext(ctx, "SELECT CONVERT(varchar(18), q.query_hash, 1) FROM ["+q5tLiveDB+"].sys.query_store_query q JOIN ["+q5tLiveDB+"].sys.query_store_query_text t ON t.query_text_id = q.query_text_id WHERE t.query_sql_text LIKE N'%FROM [[]dbo].[[]A] %'").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	d, err := svc.QueryDetail(ctx, srv.ID, q5tLiveDB, hash)
	if err != nil {
		t.Fatal(err)
	}
	if d.Verdict != nil {
		t.Logf("verdict: %s (since %v, change %q)", d.Verdict.Headline, d.Verdict.Since, d.Verdict.Change)
	}
	hasChange := false
	for _, e := range d.Events {
		hasChange = hasChange || e.Kind == "change"
	}
	if !hasChange {
		t.Errorf("the query page does not show kakapo's change: %+v", d.Events)
	}
	if os.Getenv("SQLMONITOR_TEST_5T_HOUR") == "1" && (d.Verdict == nil || d.Verdict.Change == "") {
		t.Errorf("the verdict does not split at kakapo's change: %+v", d.Verdict)
	}
}

// waitBatch polls a run until it ends.
func waitBatch(t *testing.T, s *Service, serverID int64, id string, limit time.Duration) FixRun {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		run, err := s.FixRunStatus(context.Background(), serverID, q5tLiveDB, id)
		if err != nil {
			t.Fatal(err)
		}
		if !run.Pending {
			return run
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("the run did not end")
	return FixRun{}
}

func logRun(t *testing.T, what string, run FixRun) {
	t.Helper()
	if run.Err != nil {
		t.Logf("%s: error %v", what, run.Err)
	}
	if run.Result != nil {
		t.Logf("%s: %s (verified %v)", what, run.Result.After, run.Result.Verified)
	}
	for _, it := range run.Items {
		t.Logf("  %-10s %s: %s", it.State, it.Label, it.Text)
	}
}
