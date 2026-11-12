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

// Every path here fails before the dial or at it (an .invalid host); nothing reaches a SQL Server.
func TestPerformanceSampleRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{}, "test")

	var se *Error
	for _, n := range []int{9, 121} {
		if _, err := svc.PerformanceSample(ctx, 42, "WarehouseLive", n); !errors.As(err, &se) || se.Kind != KindInvalid {
			t.Errorf("%d seconds: want KindInvalid, got %v", n, err)
		}
	}
	if _, err := svc.PerformanceSample(ctx, 42, "WarehouseLive", 0); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	if _, err := svc.LastPerformanceSample(ctx, 42, "WarehouseLive"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("last of unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PerformanceSample(ctx, srv.ID, " ", 30); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database name: want KindNotFound, got %v", err)
	}
	if _, err := svc.PerformanceSample(ctx, srv.ID, "WarehouseLive", 30); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
	// Nothing was stored by the refusals; a stored sample is the last one.
	if p, err := svc.LastPerformanceSample(ctx, srv.ID, "WarehouseLive"); p != nil || err != nil {
		t.Errorf("stored after a refusal: %+v %v", p, err)
	}
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	if err := st.InsertPerfSample(ctx, srv.ID, "WarehouseLive", domain.PerfSample{Database: "WarehouseLive", CapturedAt: at, Seconds: 15, Verdict: "v"}); err != nil {
		t.Fatal(err)
	}
	if p, err := svc.LastPerformanceSample(ctx, srv.ID, "WarehouseLive"); err != nil || p == nil || p.Verdict != "v" || !p.CapturedAt.Equal(at) {
		t.Errorf("last: %+v %v", p, err)
	}
}
