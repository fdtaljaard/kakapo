package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Every path here fails before the dial or at it (an .invalid host); nothing reaches a SQL Server.
func TestBlockingHistoryRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{}, "test")

	var se *Error
	if _, err := svc.BlockingHistory(ctx, 42, "WarehouseLive", 7); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{-1, 8, 30} {
		if _, err := svc.BlockingHistory(ctx, srv.ID, "WarehouseLive", days); !errors.As(err, &se) || se.Kind != KindInvalid {
			t.Errorf("%d days: want KindInvalid, got %v", days, err)
		}
	}
	if _, err := svc.BlockingHistory(ctx, srv.ID, " ", 7); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database name: want KindNotFound, got %v", err)
	}
	for _, days := range []int{0, 1, 7} {
		if _, err := svc.BlockingHistory(ctx, srv.ID, "WarehouseLive", days); !errors.As(err, &se) || se.Kind != KindUnreachable {
			t.Errorf("%d days, no stored password: want KindUnreachable, got %v", days, err)
		}
	}
	// Nothing was stored by the refusals, so the card's BLK01 still reads "Not run yet".
	h, err := st.LastBlockingHistory(ctx, srv.ID, "WarehouseLive")
	if h != nil || err != nil {
		t.Errorf("stored after a refusal: %+v %v", h, err)
	}
	def, _ := domain.FindCardDef(domain.CardPerformance)
	_, _, checks := domain.BuildPerformanceCard(domain.PerformanceFacts{Database: "WarehouseLive", State: "ONLINE"}, def, nil, h)
	found := false
	for _, c := range checks {
		if c.CheckID == "BLK01" {
			found = true
			if c.Status != domain.CheckSkipped || c.Note != domain.Blk01NotRun {
				t.Errorf("BLK01 before a run %+v", c)
			}
		}
	}
	if !found {
		t.Error("BLK01 is not in the Performance card's checks")
	}
}
