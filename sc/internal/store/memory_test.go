package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// Migration 13 keeps every Memory snapshot: newest first, by kind and database, with whether an instance
// snapshot holds the databases panel; a snapshot of another kind or server is not found.
func TestMemorySnapshots(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	at := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	first, err := st.InsertMemorySnapshot(ctx, 1, MemoryKindInstance, "", at, domain.MemorySnapshot{Facts: domain.MemoryFacts{TotalMB: 1}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.InsertMemorySnapshot(ctx, 1, MemoryKindInstance, "", at.Add(time.Hour),
		domain.MemorySnapshot{Facts: domain.MemoryFacts{TotalMB: 2}, Buffers: &domain.BufferFacts{DataCacheKB: 5}})
	if err != nil {
		t.Fatal(err)
	}
	dbID, err := st.InsertMemorySnapshot(ctx, 1, MemoryKindDatabase, "Sales", at, domain.DatabaseMemory{Database: "Sales", Answer: "x"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := st.ListMemorySnapshots(ctx, 1, MemoryKindInstance, "", 10)
	if err != nil || len(list) != 2 || list[0].ID != second || list[1].ID != first || !list[0].Databases || list[1].Databases {
		t.Fatalf("instance list %+v %v", list, err)
	}
	if !list[0].TakenAt.Equal(at.Add(time.Hour)) {
		t.Errorf("taken at %v", list[0].TakenAt)
	}
	if l, _ := st.ListMemorySnapshots(ctx, 1, MemoryKindDatabase, "Sales", 10); len(l) != 1 || l[0].ID != dbID || l[0].Database != "Sales" {
		t.Errorf("database list %+v", l)
	}
	if l, _ := st.ListMemorySnapshots(ctx, 2, MemoryKindInstance, "", 10); len(l) != 0 {
		t.Errorf("another server's %+v", l)
	}

	var snap domain.MemorySnapshot
	if _, err := st.MemorySnapshot(ctx, 1, second, MemoryKindInstance, &snap); err != nil || snap.Facts.TotalMB != 2 || snap.Buffers.DataCacheKB != 5 {
		t.Errorf("read %+v %v", snap, err)
	}
	var dm domain.DatabaseMemory
	if _, err := st.MemorySnapshot(ctx, 1, dbID, MemoryKindDatabase, &dm); err != nil || dm.Answer != "x" {
		t.Errorf("read db %+v %v", dm, err)
	}
	if _, err := st.MemorySnapshot(ctx, 1, dbID, MemoryKindInstance, &snap); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong kind: %v", err)
	}
	if _, err := st.MemorySnapshot(ctx, 2, second, MemoryKindInstance, &snap); !errors.Is(err, ErrNotFound) {
		t.Errorf("another server: %v", err)
	}
}
