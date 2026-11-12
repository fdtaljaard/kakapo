package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

func TestResolvePassword(t *testing.T) {
	svc := New(nil, fakeCreds{7: "stored"}, "test")
	for _, c := range []struct {
		name string
		in   domain.ServerInput
		want string
	}{
		{"stored password for an empty one", domain.ServerInput{ID: 7, Auth: domain.AuthSQL, Username: "sa"}, "stored"},
		{"a typed password wins", domain.ServerInput{ID: 7, Auth: domain.AuthSQL, Username: "sa", Password: "typed"}, "typed"},
		{"no id, nothing to look up", domain.ServerInput{Auth: domain.AuthSQL, Username: "sa"}, ""},
		{"windows needs none", domain.ServerInput{ID: 7, Auth: domain.AuthWindows}, ""},
	} {
		in := c.in
		if err := svc.resolvePassword(&in); err != nil || in.Password != c.want {
			t.Errorf("%s: password %q err %v, want %q", c.name, in.Password, err, c.want)
		}
	}
	in := domain.ServerInput{ID: 8, Auth: domain.AuthSQL, Username: "sa"}
	var se *Error
	if err := svc.resolvePassword(&in); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}
}

// Every path here fails before or at the dial (an .invalid host), so nothing reaches a SQL Server.
func TestUpdateServerRefusesAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	creds := fakeCreds{}
	svc := New(st, creds, "test")

	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "monitor"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	valid := domain.ServerInput{Name: "renamed", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "monitor"}

	var se *Error
	if _, err := svc.UpdateServer(ctx, srv.ID, domain.ServerInput{Auth: domain.AuthSQL}); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("invalid input: want KindInvalid, got %v", err)
	}
	if _, err := svc.UpdateServer(ctx, 99, valid); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: want KindNotFound, got %v", err)
	}
	if _, err := svc.UpdateServer(ctx, srv.ID, valid); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("no stored password: want KindUnreachable, got %v", err)
	}

	// With a stored password the edit reaches the dial, which fails: nothing changes.
	creds[srv.ID] = "stored"
	if _, err := svc.UpdateServer(ctx, srv.ID, valid); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("unreachable host: want KindUnreachable, got %v", err)
	}
	if got, _ := st.GetServer(ctx, srv.ID); got.Name != "client" || got.LastTestedAt == nil || !got.LastTestedAt.Equal(*srv.LastTestedAt) {
		t.Errorf("row changed after a failed edit: %+v", got)
	}
	valid.Password = "typed"
	if _, err := svc.UpdateServer(ctx, srv.ID, valid); err == nil || creds[srv.ID] != "stored" {
		t.Errorf("failed edit with a typed password: err %v, stored %q", err, creds[srv.ID])
	}

	if _, err := svc.GetServer(ctx, 99); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("GetServer unknown: want KindNotFound, got %v", err)
	}
	if _, err := svc.RegisterServer(ctx, domain.ServerInput{Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "sa"}); err == nil {
		t.Error("registering a SQL login without a password should be refused")
	}
}
