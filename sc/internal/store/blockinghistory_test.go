package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestBlockingHistory(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if h, err := st.LastBlockingHistory(ctx, 1, "WarehouseLive"); err != nil || h != nil {
		t.Fatalf("never read: %+v %v", h, err)
	}

	first := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	blocker := domain.BlockedProcess{SPID: 83, Status: "sleeping", TranCount: 1, XactID: 9001, Database: "WarehouseLive", Program: "AppScheduler",
		InputBuffer: "UPDATE dbo.Pick SET Qty = 5 WHERE Id = 1042"}
	victim := domain.BlockedProcess{SPID: 91, Database: "WarehouseLive", WaitMs: 90000, LockMode: "U", Object: "dbo.Pick",
		WaitResource: "KEY: 7:72057594043039744 (8194443284a0)", InputBuffer: "SELECT 1"}
	second := blocker
	second.XactID = 9002
	older := domain.BuildBlockingHistory(domain.BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: first,
		Duration: 1500 * time.Millisecond, Days: 7, Source: domain.DeadlockSourceFile, OffsetMinutes: 120, Notes: []string{"a note"},
		Reports: []domain.BlockedProcessReport{
			{EventUTC: first.Add(-26 * time.Hour), Blocked: victim, Blocking: blocker},
			{EventUTC: first.Add(-2 * time.Hour), Blocked: victim, Blocking: second},
		}})
	if len(older.Leaders) != 1 || len(older.Findings) != 1 {
		t.Fatalf("fixture %+v", older)
	}
	if err := st.InsertBlockingHistory(ctx, 1, "WarehouseLive", older); err != nil {
		t.Fatal(err)
	}
	newer := domain.BuildBlockingHistory(domain.BlockingHistoryInputs{Database: "WarehouseLive", CapturedAt: first.Add(24 * time.Hour),
		Days: 3, Source: domain.DeadlockSourceRingBuffer, ThresholdSeconds: 20})
	if err := st.InsertBlockingHistory(ctx, 1, "WarehouseLive", newer); err != nil {
		t.Fatal(err)
	}
	// Another database and another server keep their own.
	if err := st.InsertBlockingHistory(ctx, 1, "Other", older); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertBlockingHistory(ctx, 2, "WarehouseLive", older); err != nil {
		t.Fatal(err)
	}

	got, err := st.LastBlockingHistory(ctx, 1, "WarehouseLive")
	if err != nil || got == nil {
		t.Fatalf("last: %+v %v", got, err)
	}
	if !got.CapturedAt.Equal(newer.CapturedAt) || got.Source != "ring-buffer" || got.Days != 3 || got.ThresholdSeconds != 20 ||
		got.MissedNote != domain.MissedNote(20) || got.Leaders == nil || len(got.Leaders) != 0 || got.Findings == nil || got.Notes == nil ||
		len(got.ByHour) != 3*24+1 {
		t.Errorf("newer %+v", got)
	}
	o, err := st.LastBlockingHistory(ctx, 2, "WarehouseLive")
	if err != nil || o == nil {
		t.Fatalf("server 2: %+v %v", o, err)
	}
	// The round trip keeps everything the JSON carries (MostAtOnce stays out of it).
	want := older
	want.Leaders = append([]domain.BlockingLeader{}, older.Leaders...)
	for i := range want.Leaders {
		want.Leaders[i].MostAtOnce = 0
	}
	if !reflect.DeepEqual(*o, want) {
		t.Errorf("round trip\n got %+v\nwant %+v", *o, want)
	}
	if o.Leaders[0].Occurrences != 2 || o.Findings[0].Severity != domain.SeverityHigh || o.DurationSeconds != 1.5 {
		t.Errorf("server 2 %+v", o)
	}

	var rows int
	if err := st.db.QueryRowContext(ctx, "SELECT count(*) FROM blocking_report").Scan(&rows); err != nil || rows != 4 {
		t.Errorf("history kept: %d %v", rows, err)
	}
}
