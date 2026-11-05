package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// enable-default-trace is in the registry under CHG01, server scope (docs/phase5f-contract.md, Tests);
// its preview keeps the change log line, and one already on is KindNotFound.
func TestEnableDefaultTraceRegistered(t *testing.T) {
	if r, ok := registeredFixFor(domain.FixEnableDefaultTrace); !ok || r.long != nil {
		t.Error("enable-default-trace is not registered as a short fix")
	}
	if d, ok := domain.FindFixDef(domain.FixEnableDefaultTrace); !ok || d.CheckID != "CHG01" || d.Scope != domain.ScopeServer ||
		d.Setting != domain.DefaultTraceSetting || d.Card() != domain.CardUpkeep {
		t.Errorf("fix definition %+v", d)
	}
	g := suTestFix()
	if err := enableDefaultTracePreview(g, domain.DefaultTraceFacts{ValueInUse: 0}); err != nil {
		t.Fatal(err)
	}
	if g.preview.Statement == "" || len(g.preview.Changes) != 1 || g.options[suDescriptionKey] != "default trace enabled on local: 0 (off) -> 1 (on)" {
		t.Errorf("preview %+v options %v", g.preview, g.options)
	}
	if err := suApplyRefusal(g); err != nil {
		t.Errorf("apply refused: %v", err)
	}
	var se *Error
	if err := enableDefaultTracePreview(suTestFix(), domain.DefaultTraceFacts{ValueInUse: 1, Running: true}); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("already on: %v", err)
	}
}

// The harvest on the database page's load (docs/phase5f-contract.md): the trace's events become
// change rows for every database, a second load adds nothing, the newest event is kept for the gap
// check, and nothing is written when the trace is off.
func TestHarvestSchemaChanges(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 5, 7, 40, 0, 0, time.UTC) // server clock, UTC+2: 05:40 UTC
	oldest := time.Date(2026, 10, 2, 3, 47, 0, 0, time.UTC)
	newest := time.Date(2026, 11, 5, 5, 39, 0, 0, time.UTC)
	ev := func(min int, db string, class, objType int, name string) domain.SchemaChangeFact {
		return domain.SchemaChangeFact{At: time.Date(2026, 11, 5, 5, min, 0, 0, time.UTC), DatabaseID: 7, Database: db, EventClass: class, SubClass: 1,
			ObjectType: objType, Schema: "dbo", Name: name, TransactionID: int64(min), Login: `DOMAIN\jane`, Host: "PC1", Program: "SSMS"}
	}
	f := domain.UpkeepFacts{ServerName: "SQL01", Database: "W", State: "ONLINE", TraceRunning: true, ServerNow: now, UTCOffsetMinutes: 120,
		TraceOldestUTC: &oldest, TraceNewestUTC: &newest,
		SchemaChanges: []domain.SchemaChangeFact{ev(10, "W", domain.TraceObjectAltered, 8272, "GetOrders"), ev(11, "Other", domain.TraceObjectCreated, 8278, "V1")}}

	w, err := svc.harvestSchemaChanges(ctx, srv.ID, f)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || w.State != domain.ChangeWindowRunning || w.Days != 34 {
		t.Errorf("window %+v", w)
	}
	first, _ := svc.ListChanges(ctx, srv.ID, "W", 10)
	other, _ := svc.ListChanges(ctx, srv.ID, "Other", 10)
	if len(first) != 1 || first[0].Result != domain.ChangeSeenOnServer || first[0].Object != "dbo.GetOrders" || len(other) != 1 {
		t.Fatalf("after the harvest: %+v %+v", first, other)
	}
	if v, _ := st.Setting(ctx, domain.SchemaChangesSeenKey(srv.ID)); v != "2026-11-05T05:39:00Z" {
		t.Errorf("newest seen %q", v)
	}
	// The page opened again: nothing twice, the same row.
	if _, err := svc.harvestSchemaChanges(ctx, srv.ID, f); err != nil {
		t.Fatal(err)
	}
	if again, _ := svc.ListChanges(ctx, srv.ID, "W", 10); len(again) != 1 || again[0].ID != first[0].ID {
		t.Errorf("second load: %+v", again)
	}
	// The trace turned over past what the last harvest saw: the gap is said.
	later := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	g := f
	g.TraceOldestUTC, g.SchemaChanges = &later, nil
	if w, err := svc.harvestSchemaChanges(ctx, srv.ID, g); err != nil || w.State != domain.ChangeWindowGap {
		t.Errorf("gap: %+v %v", w, err)
	}
	// Off: the line says so and nothing is written or removed.
	off := f
	off.TraceRunning, off.SchemaChanges = false, nil
	if w, err := svc.harvestSchemaChanges(ctx, srv.ID, off); err != nil || w.State != domain.ChangeWindowOff {
		t.Errorf("off: %+v %v", w, err)
	}
	if again, _ := svc.ListChanges(ctx, srv.ID, "W", 10); len(again) != 1 {
		t.Errorf("off keeps the log: %+v", again)
	}
}
