package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

type fakeCreds map[int64]string

func (f fakeCreds) Set(id int64, pw string) error { f[id] = pw; return nil }
func (f fakeCreds) Delete(id int64) error         { delete(f, id); return nil }
func (f fakeCreds) Get(id int64) (string, error) {
	pw, ok := f[id]
	if !ok {
		return "", errors.New("no stored password for this server")
	}
	return pw, nil
}

func TestConnectRefusesBeforeDialling(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{})

	var se *Error
	if _, _, err := svc.connect(ctx, 42); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}

	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "monitor"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.connect(ctx, srv.ID)
	if !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Fatalf("SQL login without a stored password: want KindUnreachable, got %v", err)
	}
	t.Log(err)
}
