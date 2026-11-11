package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The app was SQLMonitor until 2026-11-11: an old data file and its write-ahead log become
// kakapo.duckdb once, and an existing kakapo.duckdb always wins.
func TestDataFile(t *testing.T) {
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	empty := t.TempDir()
	if got := dataFile(empty); got != filepath.Join(empty, "kakapo.duckdb") {
		t.Errorf("first run: %s", got)
	}

	old := t.TempDir()
	write(filepath.Join(old, "sqlmonitor.duckdb"))
	write(filepath.Join(old, "sqlmonitor.duckdb.wal"))
	if got := dataFile(old); got != filepath.Join(old, "kakapo.duckdb") {
		t.Errorf("old file: %s", got)
	}
	if !exists(filepath.Join(old, "kakapo.duckdb")) || !exists(filepath.Join(old, "kakapo.duckdb.wal")) ||
		exists(filepath.Join(old, "sqlmonitor.duckdb")) || exists(filepath.Join(old, "sqlmonitor.duckdb.wal")) {
		t.Error("old file and its .wal were not both renamed")
	}

	both := t.TempDir()
	write(filepath.Join(both, "sqlmonitor.duckdb"))
	write(filepath.Join(both, "kakapo.duckdb"))
	if got := dataFile(both); got != filepath.Join(both, "kakapo.duckdb") || !exists(filepath.Join(both, "sqlmonitor.duckdb")) {
		t.Errorf("kakapo.duckdb wins and the old file is left alone: %s", got)
	}
}
