package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestQueryStoreSnapshotAndChange(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "local", Host: `localhost\SQL2022`, Auth: domain.AuthWindows},
		domain.ConnectionInfo{ProductVersion: "16.0.1000.6"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetServer(ctx, srv.ID)
	if err != nil || got.Host != srv.Host {
		t.Fatalf("get server: %+v %v", got, err)
	}
	if _, err := st.GetServer(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing server: want ErrNotFound, got %v", err)
	}

	captured := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	q := domain.NewQueryStoreStatus(domain.QueryStoreOptions{ActualState: "OFF", DesiredState: "OFF"}, "13.0.5026.0", captured, true)
	if err := st.InsertQueryStoreSnapshot(ctx, srv.ID, "WarehouseLive", q); err != nil {
		t.Fatal(err)
	}
	var verdict string
	var wait *string
	var at time.Time
	if err := st.db.QueryRowContext(ctx, `SELECT verdict, wait_stats_capture_mode, captured_at FROM db_query_store`).Scan(&verdict, &wait, &at); err != nil {
		t.Fatal(err)
	}
	if verdict != "off" || wait != nil || !asUTC(at).Equal(captured) {
		t.Errorf("snapshot: verdict=%q wait=%v at=%v", verdict, wait, at)
	}

	for want := int64(1); want <= 2; want++ {
		id, err := st.InsertChange(ctx, domain.Change{ServerID: srv.ID, Database: "WarehouseLive", Object: "Query Store",
			ChangeType: "SETTING", Description: "d", Statement: "ALTER DATABASE [WarehouseLive] SET QUERY_STORE = ON;",
			Rollback: "ALTER DATABASE [WarehouseLive] SET QUERY_STORE = OFF;", Result: "On"})
		if err != nil {
			t.Fatal(err)
		}
		if id != want {
			t.Errorf("change id %d, want %d", id, want)
		}
	}
	var rollback string
	if err := st.db.QueryRowContext(ctx, `SELECT rollback_statement, changed_at FROM change WHERE id = 1`).Scan(&rollback, &at); err != nil {
		t.Fatal(err)
	}
	if rollback == "" || time.Since(asUTC(at)) > time.Minute || time.Since(asUTC(at)) < 0 {
		t.Errorf("change row: rollback=%q changed_at=%v (want UTC now)", rollback, at)
	}
}
