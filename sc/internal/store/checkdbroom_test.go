package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestCheckdbRoom(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if r, err := st.LastCheckdbRoom(ctx, 1, "GraniteLive"); err != nil || r != nil {
		t.Fatalf("never run: %+v %v", r, err)
	}

	first := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	in := domain.CheckdbRoomInputs{Database: "GraniteLive", CapturedAt: first, Duration: 2500 * time.Millisecond,
		EstimateKB: 4604522, TempdbFreeMB: 500, TempdbRoomMB: 200000, DataVolume: `D:\`, DataFreeMB: 51200, DataSizeMB: 40960}
	older := domain.BuildCheckdbRoom(in)
	if err := st.InsertCheckdbRoom(ctx, 1, "GraniteLive", older); err != nil {
		t.Fatal(err)
	}
	in.CapturedAt, in.TempdbFreeMB = first.Add(24*time.Hour), 10000
	newer := domain.BuildCheckdbRoom(in)
	if err := st.InsertCheckdbRoom(ctx, 1, "GraniteLive", newer); err != nil {
		t.Fatal(err)
	}
	// Another database and another server keep their own.
	if err := st.InsertCheckdbRoom(ctx, 1, "Other", older); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertCheckdbRoom(ctx, 2, "GraniteLive", older); err != nil {
		t.Fatal(err)
	}

	got, err := st.LastCheckdbRoom(ctx, 1, "GraniteLive")
	if err != nil || got == nil {
		t.Fatalf("last: %+v %v", got, err)
	}
	if !got.CapturedAt.Equal(newer.CapturedAt) || got.DurationSeconds != 2.5 || got.EstimateKB != 4604522 || got.NeedMB != 8994 ||
		got.TempdbFreeMB != 10000 || got.TempdbRoomMB != 200000 || got.DataVolume != `D:\` || got.DataFreeMB != 51200 || got.DataSizeMB != 40960 {
		t.Errorf("numbers %+v", got)
	}
	if got.Finding != newer.Finding {
		t.Errorf("finding\n got %+v\nwant %+v", got.Finding, newer.Finding)
	}
	if got.Finding.Severity != domain.SeverityInfo {
		t.Errorf("newer passed: %s", got.Finding.Severity)
	}
	if o, _ := st.LastCheckdbRoom(ctx, 2, "GraniteLive"); o == nil || o.Finding.Severity != domain.SeverityMedium || o.Finding.Finding != older.Finding.Finding {
		t.Errorf("server 2: %+v", o)
	}

	var rows int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM checkdb_room`).Scan(&rows); err != nil || rows != 4 {
		t.Errorf("history kept: %d %v", rows, err)
	}
}
