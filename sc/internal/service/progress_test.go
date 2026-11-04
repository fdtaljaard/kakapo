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
func TestProgressRefusesBeforeReading(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})

	var se *Error
	if _, err := svc.Progress(ctx, 42, "WMS"); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Progress(ctx, srv.ID, " "); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database name: want KindNotFound, got %v", err)
	}
	if _, err := svc.Progress(ctx, srv.ID, "WMS"); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
	if cs, err := st.ListChanges(ctx, srv.ID, "WMS", 10); err != nil || len(cs) != 0 {
		t.Errorf("a read logged changes: %v %v", cs, err)
	}
}
