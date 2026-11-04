package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The checkdb_run round trip: none, then the latest per server and database, messages kept in order.
func TestCheckdbRun(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if r, err := st.LastCheckdbRun(ctx, 1, "WarehouseLive"); err != nil || r != nil {
		t.Fatalf("never run: %+v %v", r, err)
	}
	first := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	damaged := domain.CheckdbRun{RanAt: first, DurationSeconds: 3725.5, PhysicalOnly: true,
		Outcome:  "CHECKDB found 0 allocation errors and 2 consistency errors in database 'WarehouseLive'.",
		Messages: []string{"Msg 8928, Level 16: Page (1:153) could not be processed.", "Msg 8989, Level 16: CHECKDB found 0 allocation errors and 2 consistency errors in database 'WarehouseLive'."}}
	if err := st.InsertCheckdbRun(ctx, 1, "WarehouseLive", damaged); err != nil {
		t.Fatal(err)
	}
	got, err := st.LastCheckdbRun(ctx, 1, "WarehouseLive")
	if err != nil || got == nil || !reflect.DeepEqual(*got, damaged) {
		t.Fatalf("damaged run: %+v %v", got, err)
	}

	clean := domain.CheckdbRun{RanAt: first.Add(24 * time.Hour), DurationSeconds: 95, Clean: true, Outcome: domain.CheckdbOutcomeClean}
	if err := st.InsertCheckdbRun(ctx, 1, "WarehouseLive", clean); err != nil {
		t.Fatal(err)
	}
	// Another database and another server keep their own.
	if err := st.InsertCheckdbRun(ctx, 2, "WarehouseLive", damaged); err != nil {
		t.Fatal(err)
	}
	got, err = st.LastCheckdbRun(ctx, 1, "WarehouseLive")
	clean.Messages = []string{}
	if err != nil || got == nil || !reflect.DeepEqual(*got, clean) || got.RanAt.Location() != time.UTC {
		t.Errorf("latest: %+v %v", got, err)
	}
	if got, _ := st.LastCheckdbRun(ctx, 2, "WarehouseLive"); got == nil || got.Clean {
		t.Errorf("other server: %+v", got)
	}
	if got, _ := st.LastCheckdbRun(ctx, 1, "Other"); got != nil {
		t.Errorf("other database: %+v", got)
	}
}
