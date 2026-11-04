package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestDeadlockReport(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if r, err := st.LastDeadlockReport(ctx, 1, "GraniteLive"); err != nil || r != nil {
		t.Fatalf("never run: %+v %v", r, err)
	}

	first := time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC)
	ev := domain.DeadlockEvent{Time: first.Add(-time.Hour), Database: "GraniteLive",
		Victim:  domain.DeadlockProcess{Procedure: "dbo.PRESCRIPT_PICKING", Statement: "UPDATE dbo.Pick SET Done = 1"},
		Winner:  domain.DeadlockProcess{Procedure: "dbo.IntegrationPost"},
		Objects: []string{"dbo.Pick"}, LockModes: []string{"keylock dbo.Pick: held X, wanted U"}}
	older := domain.BuildDeadlockReport(domain.DeadlockInputs{Database: "GraniteLive", CapturedAt: first, Duration: 2500 * time.Millisecond,
		Days: 7, Source: domain.DeadlockSourceFile, Events: []domain.DeadlockEvent{ev, ev, ev}})
	if err := st.InsertDeadlockReport(ctx, 1, "GraniteLive", older); err != nil {
		t.Fatal(err)
	}
	newer := domain.BuildDeadlockReport(domain.DeadlockInputs{Database: "GraniteLive", CapturedAt: first.Add(24 * time.Hour),
		Days: 7, Source: domain.DeadlockSourceRingBuffer})
	if err := st.InsertDeadlockReport(ctx, 1, "GraniteLive", newer); err != nil {
		t.Fatal(err)
	}
	// Another database and another server keep their own.
	if err := st.InsertDeadlockReport(ctx, 1, "Other", newer); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertDeadlockReport(ctx, 2, "GraniteLive", older); err != nil {
		t.Fatal(err)
	}

	got, err := st.LastDeadlockReport(ctx, 1, "GraniteLive")
	if err != nil || got == nil {
		t.Fatalf("last: %+v %v", got, err)
	}
	if !got.CapturedAt.Equal(newer.CapturedAt) || got.Source != "ring-buffer" || got.Total != 0 || got.Days != 7 ||
		got.Pairs == nil || len(got.Pairs) != 0 || got.Findings == nil || len(got.Findings) != 0 {
		t.Errorf("newer %+v", got)
	}
	o, err := st.LastDeadlockReport(ctx, 2, "GraniteLive")
	if err != nil || o == nil {
		t.Fatalf("server 2: %+v %v", o, err)
	}
	if o.Total != 3 || o.DurationSeconds != 2.5 || o.Source != "file" || len(o.Pairs) != 1 || o.Pairs[0].Count != 3 ||
		o.Pairs[0].Procedures != older.Pairs[0].Procedures || !o.Pairs[0].Last.Equal(older.Pairs[0].Last) ||
		o.Pairs[0].LastVictimStatement != "UPDATE dbo.Pick SET Done = 1" || len(o.Findings) != 1 || o.Findings[0] != older.Findings[0] {
		t.Errorf("server 2 %+v", o)
	}

	var rows int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM deadlock_report`).Scan(&rows); err != nil || rows != 4 {
		t.Errorf("history kept: %d %v", rows, err)
	}
}

// A finding's clock is kept with the card read.
func TestCardReadKeepsClock(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.InsertCardRead(ctx, 1, "GraniteLive", domain.Card{ID: domain.CardPerformance, Status: domain.StatusAmber},
		[]domain.Finding{{CheckID: "PRS01", Severity: domain.SeverityHigh, Clock: domain.ClockNow}, {CheckID: "STO01", Severity: domain.SeverityLow}},
		[]domain.CheckStatus{})
	if err != nil {
		t.Fatal(err)
	}
	var now, none int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE clock = 'now'), count(*) FILTER (WHERE clock IS NULL) FROM finding WHERE card_read_id = ?`, id).
		Scan(&now, &none); err != nil || now != 1 || none != 1 {
		t.Errorf("clocks: %d %d %v", now, none, err)
	}
}
