package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// backupActionTestFacts is a SIMPLE-recovery database never checked by CHECKDB, with two suspect
// pages, on an instance whose Agent runs: DBC08, BAK03 and BAK04 fire.
func backupActionTestFacts() domain.BackupFacts {
	f := backupScheduleFacts()
	never := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	last := f.ServerNow.Add(-48 * time.Hour)
	f.RecoveryModel, f.LastGoodCheckDb, f.SuspectPages, f.SuspectLast = "SIMPLE", &never, 2, &last
	return f
}

// A preview with a bad option has no changes, and Apply refuses it before touching the server.
func TestBackupActionBadOptions(t *testing.T) {
	ctx := context.Background()
	f := backupActionTestFacts()
	pages := []domain.SuspectPage{{FileID: 1, PageID: 153, EventType: 2, ErrorCount: 1, LastUpdate: *f.SuspectLast}}
	for _, c := range []struct {
		fixID   string
		options map[string]string
		apply   fixApplier
	}{
		{domain.FixRecoveryModel, map[string]string{domain.OptionRecoveryModel: "BULK_LOGGED"}, applyRecoveryModel},
		{domain.FixRecoveryModel, map[string]string{domain.OptionRecoveryModel: "SIMPLE"}, applyRecoveryModel},
		{domain.FixCheckdbJob, map[string]string{domain.OptionCheckdbTime: "25:00"}, applyCheckdbJob},
		{domain.FixCheckdbJob, map[string]string{domain.OptionCheckdbDay: "Someday"}, applyCheckdbJob},
		{domain.FixCheckdbNow, map[string]string{domain.OptionPhysicalOnly: "maybe"}, applyCheckdbNow},
		// No option to get wrong: without a clean CHECKDB run the preview refuses.
		{domain.FixSuspectPagesClear, nil, applySuspectPagesClear},
	} {
		g := actionFix()
		g.backups = f
		if err := buildBackupActionFix(g, c.fixID, f, backupActionFacts{pages: pages}, c.options); err != nil {
			t.Errorf("%s %v: %v", c.fixID, c.options, err)
			continue
		}
		if len(g.preview.Changes) != 0 || g.preview.Statement != "" || len(g.preview.Warnings) == 0 {
			t.Errorf("%s %v: %+v", c.fixID, c.options, g.preview)
		}
		var se *Error
		if _, err := c.apply(ctx, nil, 1, g); !errors.As(err, &se) || se.Kind != KindConflict || se.Error() != g.preview.Warnings[0] {
			t.Errorf("%s apply: want KindConflict with the warning, got %v", c.fixID, err)
		}
	}

	// Good options: changes and a statement.
	for _, id := range []string{domain.FixRecoveryModel, domain.FixCheckdbJob, domain.FixCheckdbNow} {
		g := actionFix()
		if err := buildBackupActionFix(g, id, f, backupActionFacts{}, nil); err != nil || len(g.preview.Changes) == 0 || g.preview.Statement == "" {
			t.Errorf("%s: %v %+v", id, err, g.preview)
		}
	}
	// A clean run after the latest record: the clear can be applied.
	f.LastCheckdbRun = &domain.CheckdbRun{RanAt: f.ServerNow, Clean: true, Outcome: domain.CheckdbOutcomeClean}
	g := actionFix()
	if err := buildBackupActionFix(g, domain.FixSuspectPagesClear, f, backupActionFacts{pages: pages}, nil); err != nil || len(g.preview.Changes) != 1 {
		t.Errorf("clear: %v %+v", err, g.preview)
	}
	// Read-only: the recovery model cannot change.
	g = actionFix()
	g.database.ReadOnly = true
	if err := buildBackupActionFix(g, domain.FixRecoveryModel, f, backupActionFacts{}, nil); err != nil || len(g.preview.Changes) != 0 {
		t.Errorf("read-only: %v %+v", err, g.preview)
	}
	// Nothing fired: nothing to apply (prepareGenericFix answers KindNotFound).
	healthy := backupScheduleFacts()
	if err := buildBackupActionFix(actionFix(), domain.FixCheckdbJob, healthy, backupActionFacts{}, nil); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("checked recently: %v", err)
	}
	if err := buildBackupActionFix(actionFix(), domain.FixSuspectPagesClear, healthy, backupActionFacts{}, nil); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("no pages: %v", err)
	}
}

// An Express integrity check preview is a script: Apply refuses it.
func TestCheckdbJobExpressRefused(t *testing.T) {
	f := backupActionTestFacts()
	f.HasAgent = false
	g := actionFix()
	if err := buildBackupActionFix(g, domain.FixCheckdbJob, f, backupActionFacts{}, nil); err != nil || !g.preview.Express || len(g.preview.Changes) == 0 {
		t.Fatalf("express: %v %+v", err, g.preview)
	}
	var se *Error
	if _, err := applyCheckdbJob(context.Background(), nil, 1, g); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("express apply: %v", err)
	}
}
