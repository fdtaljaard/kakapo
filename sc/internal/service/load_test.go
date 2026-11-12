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

// The minute refresh reads only today when the comparison is kept; a first read, a read without
// refresh and a refresh with nothing kept read the whole comparison.
func TestLoadPlan(t *testing.T) {
	today := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	kept := domain.LoadBase{Date: today, Days: 7}
	if from, full := loadPlan(kept, true, true); full || !from.Equal(today) {
		t.Errorf("refresh with a kept comparison: from %v, full %v", from, full)
	}
	for _, c := range []struct {
		base          domain.LoadBase
		kept, refresh bool
	}{
		{kept, true, false},
		{domain.LoadBase{}, false, true},
		{domain.LoadBase{}, false, false},
	} {
		if _, full := loadPlan(c.base, c.kept, c.refresh); !full {
			t.Errorf("%+v: not a full read", c)
		}
	}
	// The cache keeps one comparison per server, database and days.
	c := newLoadCache()
	c.put(loadKey{1, "Shop", 7}, kept)
	if b, ok := c.get(loadKey{1, "Shop", 7}); !ok || !b.Date.Equal(today) {
		t.Error("kept comparison not found")
	}
	if _, ok := c.get(loadKey{1, "Shop", 14}); ok {
		t.Error("another days' comparison found")
	}
}

// Load refuses days other than 7, 14 or 30, an unknown server and a missing name before it dials.
func TestLoadRefuses(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, fakeCreds{}, "test")
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "monitor"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	var se *Error
	if _, err := svc.Load(ctx, srv.ID, "Shop", 8, false); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("days 8: %v", err)
	}
	if _, err := svc.Load(ctx, 99, "Shop", 7, false); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("unknown server: %v", err)
	}
	if _, err := svc.Load(ctx, srv.ID, " ", 7, false); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no database: %v", err)
	}
	if _, err := svc.Load(ctx, srv.ID, "Shop", 7, true); !errors.As(err, &se) || se.Kind != KindUnreachable {
		t.Errorf("cannot connect: %v", err)
	}
}
