package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestListChangesAndUpdateServerTested(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.InsertServer(ctx, domain.ServerInput{Name: "b", Host: "b", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	rows := []struct {
		server int64
		db     string
	}{{a.ID, "WarehouseLive"}, {a.ID, "WarehouseLive"}, {b.ID, "WarehouseLive"}, {a.ID, "Other"}, {a.ID, "WarehouseLive"}}
	for i, r := range rows {
		c := domain.Change{ServerID: r.server, Database: r.db, ChangedAt: base.Add(time.Duration(i) * time.Hour), Object: "Query Store",
			ChangeType: "SETTING", Description: "d", Statement: "s"}
		if i == 0 {
			c.Result = "On"
		}
		if _, err := st.InsertChange(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Only server a, only WarehouseLive, newest first.
	got, err := st.ListChanges(ctx, a.ID, "WarehouseLive", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != 5 || got[1].ID != 2 || got[2].ID != 1 {
		t.Fatalf("newest first for server a, WarehouseLive: %+v", got)
	}
	if got[2].Database != "WarehouseLive" || got[2].Result != "On" || !got[2].ChangedAt.Equal(base) || got[2].ChangedAt.Location() != time.UTC {
		t.Errorf("row 1: %+v", got[2])
	}
	if got[0].Result != "" {
		t.Errorf("null result comes back empty: %+v", got[0])
	}
	if got, _ := st.ListChanges(ctx, a.ID, "WarehouseLive", 2); len(got) != 2 {
		t.Errorf("limit 2: %d rows", len(got))
	}
	if got, _ := st.ListChanges(ctx, a.ID, "Other", 10); len(got) != 1 || got[0].ID != 4 {
		t.Errorf("other database: %+v", got)
	}
	for _, c := range []struct {
		server int64
		db     string
	}{{99, "WarehouseLive"}, {a.ID, "Nope"}, {a.ID, "warehouselive"}} {
		if got, _ := st.ListChanges(ctx, c.server, c.db, 10); got == nil || len(got) != 0 {
			t.Errorf("server %d database %q: %v", c.server, c.db, got)
		}
	}

	at := time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)
	if err := st.UpdateServerTested(ctx, a.ID, "16.0.1000.6", "Developer Edition (64-bit)", at); err != nil {
		t.Fatal(err)
	}
	srv, err := st.GetServer(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if srv.LastVersion != "16.0.1000.6" || srv.LastEdition != "Developer Edition (64-bit)" || srv.LastTestedAt == nil || !srv.LastTestedAt.Equal(at) {
		t.Errorf("updated server: %+v", srv)
	}
	if err := st.UpdateServerTested(ctx, 99, "x", "y", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}

func TestUpdateServerDatabase(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthWindows}, domain.ConnectionInfo{ProductVersion: "16.0"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.UpdateServerDatabase(ctx, a.ID, "WarehouseLive")
	if err != nil {
		t.Fatal(err)
	}
	if srv.ID != a.ID || srv.Database != "WarehouseLive" || srv.Name != "a" || srv.LastVersion != "16.0" {
		t.Errorf("returned server: %+v", srv)
	}
	if got, _ := st.GetServer(ctx, a.ID); got.Database != "WarehouseLive" {
		t.Errorf("stored server: %+v", got)
	}
	if srv, _ = st.UpdateServerDatabase(ctx, a.ID, "Other DB"); srv.Database != "Other DB" {
		t.Errorf("changed again: %+v", srv)
	}
	if _, err := st.UpdateServerDatabase(ctx, 99, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}

func TestUpdateServer(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a, err := st.InsertServer(ctx, domain.ServerInput{Name: "a", Host: "a", Auth: domain.AuthSQL, Username: "sa", Database: "WarehouseLive"},
		domain.ConnectionInfo{ProductVersion: "15.0", Edition: "Express"})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.ServerInput{Name: "b", Host: `b\SQL2022`, Auth: domain.AuthWindows, Encrypt: true, TrustCert: true}
	srv, err := st.UpdateServer(ctx, a.ID, in, domain.ConnectionInfo{ProductVersion: "16.0", Edition: "Developer"})
	if err != nil {
		t.Fatal(err)
	}
	if srv.ID != a.ID || srv.Name != "b" || srv.Host != `b\SQL2022` || srv.Auth != domain.AuthWindows || srv.Username != "" ||
		srv.Database != "" || !srv.Encrypt || !srv.TrustCert || srv.LastVersion != "16.0" || srv.LastEdition != "Developer" ||
		!srv.CreatedAt.Equal(a.CreatedAt) || srv.LastTestedAt == nil || srv.LastTestedAt.Before(*a.LastTestedAt) {
		t.Errorf("returned server: %+v (was %+v)", srv, a)
	}
	if got, _ := st.GetServer(ctx, a.ID); got.Name != "b" || got.Database != "" || got.Username != "" {
		t.Errorf("stored server: %+v", got)
	}
	if _, err := st.UpdateServer(ctx, 99, in, domain.ConnectionInfo{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown server: want ErrNotFound, got %v", err)
	}
}
