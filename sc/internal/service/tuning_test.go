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

// The tuning report and Record a change against a real store; nothing here reaches a SQL Server
// (a SQL login with no stored password is refused before the dial).
func TestTuningReportAndRecordChange(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "monitor"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	bh := domain.BusinessHours{From: 8, To: 17}

	var se *Error
	for _, days := range []int{0, 31} {
		if _, err := svc.TuningReport(ctx, srv.ID, "WarehouseLive", days, 0, bh); !errors.As(err, &se) || se.Kind != KindInvalid {
			t.Errorf("days %d: want KindInvalid, got %v", days, err)
		}
	}
	if _, err := svc.TuningReport(ctx, 99, "WarehouseLive", 7, 0, bh); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	if _, err := svc.TuningReport(ctx, srv.ID, " ", 7, 0, bh); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database: want KindNotFound, got %v", err)
	}
	if _, err := svc.TuningReport(ctx, srv.ID, "WarehouseLive", 7, 0, bh); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("cannot connect: want KindUnreachable, got %v", err)
	}

	// Record a change: the fields are checked first, then the server.
	in := domain.RecordChangeInput{Object: "dbo.USP_PICKING", ChangeType: "PROCEDURE", Description: "Added OPTION (RECOMPILE) to the barcode lookup"}
	if _, err := svc.RecordChange(ctx, srv.ID, "WarehouseLive", domain.RecordChangeInput{Object: "x", ChangeType: "TABLE", Description: "d"}); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("bad type: want KindInvalid, got %v", err)
	}
	if _, err := svc.RecordChange(ctx, 99, "WarehouseLive", in); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	before := time.Now().UTC().Add(-time.Second)
	got, err := svc.RecordChange(ctx, srv.ID, "WarehouseLive", in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 || got.Result != domain.ChangeRecordedByHand || got.Database != "WarehouseLive" || got.ChangedAt.Before(before) || got.ChangeType != "PROCEDURE" {
		t.Errorf("recorded now: %+v", got)
	}
	in.ChangedAt = "2026-11-03T14:20:00+02:00"
	exact, err := svc.RecordChange(ctx, srv.ID, "WarehouseLive", in)
	if err != nil || !exact.ChangedAt.Equal(time.Date(2026, 11, 3, 12, 20, 0, 0, time.UTC)) {
		t.Errorf("exact time: %+v %v", exact, err)
	}
	in.ChangedAt = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := svc.RecordChange(ctx, srv.ID, "WarehouseLive", in); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("future: want KindInvalid, got %v", err)
	}
	// Server time needs the server's offset: without a connection nothing is written.
	in.ChangedAt = "2026-11-03T14:20"
	if _, err := svc.RecordChange(ctx, srv.ID, "WarehouseLive", in); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("server time without a server: want KindUnreachable, got %v", err)
	}
	rows, _ := svc.ListChanges(ctx, srv.ID, "WarehouseLive", 10)
	if len(rows) != 2 || rows[0].ID != got.ID || rows[0].Result != domain.ChangeRecordedByHand {
		t.Errorf("change log: %+v", rows)
	}
}
