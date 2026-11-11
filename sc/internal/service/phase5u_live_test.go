package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// TestPhase5uLive runs phase 5u's live checks (docs/phase5u-contract.md, Tests) when SQLMONITOR_TEST_HOST
// (Windows authentication) names a test instance and SQLMONITOR_TEST_5U=1 allows writing to master there.
// It refuses an instance that has any of Ola Hallengren's objects already. It installs kakapo's copy as
// previewed, reads the version back, runs IndexOptimize on a throwaway database with @Execute = 'N' (and
// @LogToTable, so CommandLog is used), then runs the preview's rollback and checks master is as before.
func TestPhase5uLive(t *testing.T) {
	host := os.Getenv("SQLMONITOR_TEST_HOST")
	if host == "" || os.Getenv("SQLMONITOR_TEST_5U") != "1" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_5U=1 to run phase 5u's checks on a test instance")
	}
	ctx := context.Background()
	db, err := sql.Open("sqlserver", collect.ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	before, err := collect.ReadOlaIndex(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if before.IndexOptimize || before.CommandExecute || before.CommandLog {
		t.Skipf("%s has Ola Hallengren's objects already: the live test only runs on an instance without them", host)
	}
	p, _, err := domain.BuildInstallOlaIndexFix(before)
	if err != nil || len(p.Changes) != 3 {
		t.Fatalf("%v %+v", err, p.Warnings)
	}
	rolledBack := false
	rollback := func() {
		if rolledBack {
			return
		}
		rolledBack = true
		if err := collect.ExecFixBatches(ctx, db, p.Rollback); err != nil {
			t.Errorf("rollback: %v", err)
		}
	}
	defer rollback()

	if err := collect.ExecFixBatches(ctx, db, p.Statement); err != nil {
		t.Fatalf("install: %v", err)
	}
	after, err := collect.ReadOlaIndex(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if text, ok := domain.OlaInstallReadBack(after); !ok {
		t.Fatalf("read-back: %s", text)
	}

	// IndexOptimize runs: only prints its commands, and logs to CommandLog.
	scratch := fmt.Sprintf("kakapo_5u_test_%d", os.Getpid())
	if _, err := db.ExecContext(ctx, "CREATE DATABASE ["+scratch+"]"); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, "DROP DATABASE ["+scratch+"]") //nolint:errcheck
	if _, err := db.ExecContext(ctx, "EXECUTE master.dbo.IndexOptimize @Databases = N'"+scratch+"', @FragmentationLow = NULL, "+
		"@FragmentationMedium = 'INDEX_REORGANIZE', @FragmentationHigh = 'INDEX_REBUILD_ONLINE,INDEX_REORGANIZE', @UpdateStatistics = 'ALL', "+
		"@LogToTable = 'Y', @TimeLimit = 60, @Execute = 'N';"); err != nil {
		t.Errorf("IndexOptimize: %v", err)
	}

	rollback()
	gone, err := collect.ReadOlaIndex(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if gone.IndexOptimize || gone.CommandExecute || gone.CommandLog {
		t.Errorf("after the rollback: %+v", gone)
	}
}
