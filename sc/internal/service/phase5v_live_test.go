package service

import (
	"context"
	"database/sql"
	"errors"
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

// q5vLiveDB is the throwaway database TestPhase5vLive creates and drops, named with the process id.
var q5vLiveDB = fmt.Sprintf("kakapo_5v_test_%d", os.Getpid())

// TestPhase5vLive runs phase 5v's live checks (docs/phase5v-contract.md, Done when) when SQLMONITOR_TEST_HOST
// (Windows authentication) names a server and SQLMONITOR_TEST_5V=1 allows it. Everything it reads on the
// instance is read only; the one write is a throwaway database with made-up tables, dropped afterwards: a
// table with a wide nonclustered index, a second index on the same leading key, and a heap, all read into
// cache. It reads panels 2 to 4 and checks both reconciliations within 5%, a saved snapshot shown again,
// Database detail of the throwaway database with its flags and temperatures, and a read stopped by
// cancelling its request.
func TestPhase5vLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5V") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5V=1 to run phase 5v's checks on a live instance")
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
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sys.databases WHERE name = @p1", q5vLiveDB).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%s exists already (%v)", q5vLiveDB, err)
	}
	exec("CREATE DATABASE [" + q5vLiveDB + "]")
	defer func() {
		if _, err := db.ExecContext(ctx, "USE master; ALTER DATABASE ["+q5vLiveDB+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+q5vLiveDB+"];"); err != nil {
			t.Errorf("could not drop %s: %v", q5vLiveDB, err)
		}
	}()
	exec("ALTER DATABASE [" + q5vLiveDB + "] SET RECOVERY SIMPLE")
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
	cx("USE [" + q5vLiveDB + "]")
	// Orders: about 110 MB; IX_Wide carries every column (a second copy), IX_Customer shares its leading key.
	// Notes: a heap of about 105 MB. Big enough for the flags' 100 MB floors.
	cx(`CREATE TABLE dbo.Orders (id int IDENTITY PRIMARY KEY, customer int NOT NULL, placed datetime2 NOT NULL, pad char(600) NOT NULL DEFAULT 'x')`)
	cx(`INSERT dbo.Orders (customer, placed) SELECT TOP (170000) ABS(CHECKSUM(NEWID())) % 500, SYSDATETIME() FROM sys.all_columns a CROSS JOIN sys.all_columns b`)
	cx(`CREATE INDEX IX_Wide ON dbo.Orders (customer) INCLUDE (placed, pad)`)
	cx(`CREATE INDEX IX_Customer ON dbo.Orders (customer, placed)`)
	cx(`CREATE TABLE dbo.Notes (n int NOT NULL, pad char(800) NOT NULL DEFAULT 'y')`)
	cx(`INSERT dbo.Notes (n) SELECT TOP (120000) 1 FROM sys.all_columns a CROSS JOIN sys.all_columns b`)
	// Read everything into cache, and register reads in the usage stats.
	cx(`DECLARE @c bigint;
	    SELECT @c = COUNT_BIG(*) FROM dbo.Orders WITH (INDEX (1)) WHERE pad = 'x';
	    SELECT @c = COUNT_BIG(*) FROM dbo.Orders WITH (INDEX (IX_Wide)) WHERE pad = 'x';
	    SELECT @c = COUNT_BIG(*) FROM dbo.Orders WITH (INDEX (IX_Customer)) WHERE placed > '2000-01-01';
	    SELECT @c = COUNT_BIG(*) FROM dbo.Notes WHERE pad = 'y';`)

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

	// Panels 2 and 3, before any snapshot: no databases panel.
	page, err := svc.Memory(ctx, srv.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := page.View
	if v.Permission != "" || !page.Live || v.Databases != nil || len(page.Snapshots) != 0 {
		t.Fatalf("first page: %+v", page)
	}
	t.Logf("verdict: %s", v.Verdict)
	t.Logf("glance: RAM %.1f GB, SQL %.2f GB, else %.1f GB, free %.1f GB; committed %.2f, target %.2f, cap %s, up %s, model %s, PLE %v, grants %d",
		v.Glance.TotalGB, v.Glance.SQLGB, v.Glance.OtherGB, v.Glance.FreeGB, v.Glance.CommittedGB, v.Glance.TargetGB, v.Glance.Cap,
		v.Glance.Uptime, v.Glance.MemoryModel, v.Glance.NodePLE, v.Glance.GrantsPending)
	if v.Glance.TotalGB <= 0 || v.Glance.SQLGB <= 0 || v.Glance.CommittedGB <= 0 || v.Glance.Uptime == "" || len(v.Glance.NodePLE) == 0 {
		t.Errorf("glance not read: %+v", v.Glance)
	}
	for _, g := range v.Clerks.Groups {
		t.Logf("clerks: %s %.3f GB (%.1f%%)", g.Name, g.GB, g.Share)
	}
	t.Logf("clerks: %s", v.Clerks.ReconcileText)
	if v.Clerks.Reconcile != "" {
		t.Errorf("the clerks do not add up to the committed memory within 5%%: %s", v.Clerks.Reconcile)
	}

	// Panel 4: read the cache by database, kept as a snapshot.
	page, err = svc.TakeMemorySnapshot(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	d := page.View.Databases
	if d == nil || page.SnapshotID == 0 || len(page.Snapshots) != 1 || !page.Snapshots[0].Databases {
		t.Fatalf("snapshot: %+v", page)
	}
	t.Logf("databases (%.2f s): %s", d.Seconds, d.ReconcileText)
	if d.Reconcile != "" {
		t.Errorf("the databases do not add up to the data cache clerk within 5%%: %s", d.Reconcile)
	}
	found := false
	for _, r := range d.Rows {
		if r.Name == q5vLiveDB {
			found = r.User && r.GB > 0.01
			t.Logf("throwaway database in cache: %.3f GB (%.1f%%)", r.GB, r.Share)
		}
	}
	if !found {
		t.Errorf("the throwaway database is not among the databases in cache")
	}
	if !strings.Contains(page.View.Handover, "Databases in cache") || !strings.Contains(page.View.Handover, q5vLiveDB) {
		t.Errorf("handover:\n%s", page.View.Handover)
	}

	// Opening the screen again shows that snapshot's databases beside live panels 2 and 3, and the snapshot alone.
	again, err := svc.Memory(ctx, srv.ID, 0)
	if err != nil || again.View.Databases == nil || again.SnapshotID != page.SnapshotID || !again.View.Databases.ReadAt.Equal(d.ReadAt) {
		t.Errorf("reopened: %+v %v", again.SnapshotID, err)
	}
	old, err := svc.Memory(ctx, srv.ID, page.SnapshotID)
	if err != nil || old.Live || old.View.Verdict != page.View.Verdict {
		t.Errorf("the snapshot alone: live %v, %q vs %q, %v", old.Live, old.View.Verdict, page.View.Verdict, err)
	}

	// Database detail of the throwaway database.
	dp, err := svc.TakeDatabaseMemory(ctx, srv.ID, q5vLiveDB)
	if err != nil {
		t.Fatal(err)
	}
	det := dp.Detail
	if det == nil || det.Permission != "" || dp.SnapshotID == 0 {
		t.Fatalf("detail: %+v", dp)
	}
	t.Logf("detail (%.2f s): %s %s", det.Seconds, det.Answer, det.UsageNote)
	rows := map[string]domain.IndexCacheRow{}
	for _, r := range det.Rows {
		t.Logf("  %s · %s (%s): %.1f of %.1f MB (%.0f%%), seeks %d scans %d, %s, keys [%s] include [%s] %v",
			r.Table, r.Index, r.Type, r.CachedMB, r.TotalMB, r.CachedPct, r.Seeks, r.Scans, r.Temperature, r.Keys, r.Includes, r.Flags)
		rows[r.Table+"."+r.Index] = r
	}
	wide, cust, heap := rows["dbo.Orders.IX_Wide"], rows["dbo.Orders.IX_Customer"], rows["dbo.Notes.(heap)"]
	if wide.Index == "" || cust.Index == "" || heap.Index == "" {
		t.Fatalf("rows missing: %v", rows)
	}
	if wide.Temperature != "hot" || wide.CachedPct < 90 || wide.Keys != "customer" || wide.Includes != "placed, pad" {
		t.Errorf("IX_Wide: %+v", wide)
	}
	if !hasFlag(wide.Flags, "a wide INCLUDE list") || !hasFlag(wide.Flags, "Shares its leading key column (customer) with 1 other index") {
		t.Errorf("IX_Wide flags: %v", wide.Flags)
	}
	if !hasFlag(wide.Flags, "probably scanned") || !hasFlag(rows["dbo.Orders."+det.Rows[0].Index].Flags, "probably scanned") {
		t.Errorf("mostly cached: %v / %v", wide.Flags, det.Rows[0].Flags)
	}
	if !hasFlag(cust.Flags, "Shares its leading key column") || heap.Type != "heap" || !hasFlag(heap.Flags, "A heap") {
		t.Errorf("IX_Customer %v / heap %+v", cust.Flags, heap)
	}
	if det.HotGB <= 0 || det.CachedGB < det.HotGB {
		t.Errorf("split: hot %v of %v", det.HotGB, det.CachedGB)
	}
	saved, err := svc.DatabaseMemory(ctx, srv.ID, q5vLiveDB, 0)
	if err != nil || saved.Detail == nil || saved.SnapshotID != dp.SnapshotID || len(saved.Detail.Rows) != len(det.Rows) {
		t.Errorf("saved detail: %+v %v", saved.SnapshotID, err)
	}

	// Stop: a read whose request is cancelled ends with the cancellation, and nothing is kept.
	stopCtx, stop := context.WithCancel(ctx)
	go func() { time.Sleep(5 * time.Millisecond); stop() }()
	if _, err := svc.TakeMemorySnapshot(stopCtx, srv.ID); err == nil {
		t.Log("the read finished before the stop (a small cache); nothing to check")
	} else if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("stopped read: %v", err)
	}
	if list, _ := st.ListMemorySnapshots(ctx, srv.ID, store.MemoryKindInstance, "", 10); len(list) > 2 {
		t.Errorf("snapshots after the stop: %d", len(list))
	}
}

func hasFlag(flags []string, s string) bool {
	for _, f := range flags {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}
