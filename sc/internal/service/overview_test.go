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

func TestOverviewChangesAndRememberRefuseUnknownServer(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})

	var se *Error
	if _, err := svc.Overview(ctx, 42, "GraniteLive"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("overview of unknown server: want KindNotFound, got %v", err)
	}
	if _, err := svc.ListChanges(ctx, 42, "GraniteLive", 10); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("changes of unknown server: want KindNotFound, got %v", err)
	}
	if _, err := svc.RememberDatabase(ctx, 42, "GraniteLive"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("remember on unknown server: want KindNotFound, got %v", err)
	}

	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthWindows},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	// An empty name is refused before anything dials the server.
	if _, err := svc.Overview(ctx, srv.ID, " "); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("overview without a database: want KindNotFound, got %v", err)
	}
	got, err := svc.ListChanges(ctx, srv.ID, "GraniteLive", 10)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("no changes yet: want an empty list, got %v %v", got, err)
	}
	if _, err := st.InsertChange(ctx, domain.Change{ServerID: srv.ID, Database: "GraniteLive", ChangedAt: time.Now(),
		Object: "Query Store", ChangeType: "SETTING", Description: "d", Statement: "s"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.ListChanges(ctx, srv.ID, "GraniteLive", 10); len(got) != 1 || got[0].Database != "GraniteLive" {
		t.Errorf("one change on GraniteLive: %+v", got)
	}
	if got, _ := svc.ListChanges(ctx, srv.ID, "Other", 10); len(got) != 0 {
		t.Errorf("none on Other: %+v", got)
	}

	remembered, err := svc.RememberDatabase(ctx, srv.ID, " GraniteLive ")
	if err != nil || remembered.Database != "GraniteLive" || remembered.ID != srv.ID {
		t.Errorf("remember: %+v %v", remembered, err)
	}
}
