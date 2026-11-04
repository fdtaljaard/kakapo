package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// storageActionFacts is a database whose data file grows by 10% and a TempDB with 2 data files on an
// 8-CPU server: STO02 and TMP01 fire.
func storageActionFacts() domain.StorageFacts {
	now := time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)
	f := domain.StorageFacts{Database: "Scratch", State: "ONLINE", RecoveryModel: "FULL", EngineVersion: "16.0.4255.2",
		CPUCount: 8, ServerNow: now}
	f.Files = []domain.StorageFile{
		{DatabaseID: 5, Database: "Scratch", FileID: 1, Type: "ROWS", LogicalName: "Scratch", PhysicalName: `D:\Data\Scratch.mdf`,
			SizeMB: 100, Growth: 10, IsPercentGrowth: true, MaxSizePages: -1, Volume: `D:\`, VolumeTotalMB: 100000, VolumeFreeMB: 50000},
		{DatabaseID: 5, Database: "Scratch", FileID: 2, Type: "LOG", LogicalName: "Scratch_log", PhysicalName: `L:\Log\Scratch_log.ldf`,
			SizeMB: 100, Growth: 8192, MaxSizePages: 268435456, Volume: `L:\`, VolumeTotalMB: 100000, VolumeFreeMB: 50000},
		{DatabaseID: 2, Database: "tempdb", FileID: 1, Type: "ROWS", LogicalName: "tempdev", PhysicalName: `T:\TempDB\tempdb.mdf`,
			SizeMB: 8, Growth: 8192, MaxSizePages: -1, Volume: `T:\`, VolumeTotalMB: 100000, VolumeFreeMB: 50000},
		{DatabaseID: 2, Database: "tempdb", FileID: 3, Type: "ROWS", LogicalName: "temp2", PhysicalName: `T:\TempDB\tempdb_mssql_2.ndf`,
			SizeMB: 8, Growth: 8192, MaxSizePages: -1, Volume: `T:\`, VolumeTotalMB: 100000, VolumeFreeMB: 50000},
		{DatabaseID: 2, Database: "tempdb", FileID: 2, Type: "LOG", LogicalName: "templog", PhysicalName: `T:\TempDB\templog.ldf`,
			SizeMB: 8, Growth: 8192, MaxSizePages: -1, Volume: `T:\`, VolumeTotalMB: 100000, VolumeFreeMB: 50000},
	}
	return f
}

func actionFix() *genericFix {
	return &genericFix{srv: domain.Server{ID: 1, Name: `SQL01\WMS`}, database: domain.DatabaseStatus{Name: "Scratch", State: "ONLINE"}}
}

func TestStorageAndBackupActionsRegistered(t *testing.T) {
	for _, id := range []string{domain.FixAutogrowth, domain.FixAddTempdbFiles, domain.FixTempdbFiles, domain.FixRecoveryModel, domain.FixCheckdbJob,
		domain.FixCheckdbNow, domain.FixSuspectPagesClear} {
		if _, ok := registeredFixFor(id); !ok {
			t.Errorf("%s is not registered", id)
		}
		if _, ok := domain.FindFixDef(id); !ok {
			t.Errorf("%s has no definition", id)
		}
	}
}

// A preview with a bad option has no changes, and Apply refuses it before touching the server.
func TestStorageActionBadOptions(t *testing.T) {
	ctx := context.Background()
	f := storageActionFacts()
	for _, c := range []struct {
		fixID   string
		options map[string]string
		apply   fixApplier
	}{
		{domain.FixAutogrowth, map[string]string{domain.OptionGrowthFile: "Scratch", domain.OptionGrowthMB: "0"}, applyAutogrowth},
		{domain.FixAutogrowth, map[string]string{domain.OptionGrowthFile: "NoSuchFile"}, applyAutogrowth},
		{domain.FixAddTempdbFiles, map[string]string{domain.OptionTempdbCount: "99"}, applyAddTempdbFiles},
	} {
		g := actionFix()
		if err := buildStorageActionFix(g, c.fixID, f, domain.StorageFileState{}, c.options); err != nil {
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
	g := actionFix()
	if err := buildStorageActionFix(g, domain.FixAutogrowth, f, domain.StorageFileState{}, map[string]string{domain.OptionGrowthFile: "Scratch"}); err != nil ||
		len(g.preview.Changes) != 1 || g.preview.Rollback == "" {
		t.Errorf("autogrowth: %v %+v", err, g.preview)
	}
	g = actionFix()
	if err := buildStorageActionFix(g, domain.FixAddTempdbFiles, f, domain.StorageFileState{}, nil); err != nil || len(g.preview.Changes) != 7 {
		t.Errorf("tempdb: %v %+v", err, g.preview.Changes)
	}
	// The TempDB file set (TMP02 fires on unequal files): a bad size gives no changes and Apply refuses
	// it before touching the server; good values give one statement per file and the rollback.
	tf := storageActionFacts()
	tf.Files[3].SizeMB = 64
	for _, bad := range []map[string]string{{domain.OptionTempdbDataSizeMB: "10"}, {domain.OptionTempdbLogGrowthMB: "5000"}} {
		g = actionFix()
		if err := buildStorageActionFix(g, domain.FixTempdbFiles, tf, domain.StorageFileState{}, bad); err != nil ||
			len(g.preview.Changes) != 0 || g.preview.Statement != "" || len(g.preview.Warnings) == 0 {
			t.Errorf("tempdb-files %v: %v %+v", bad, err, g.preview)
		}
		var se *Error
		if _, err := applyTempdbFiles(ctx, nil, 1, g); !errors.As(err, &se) || se.Kind != KindConflict || se.Error() != g.preview.Warnings[0] {
			t.Errorf("tempdb-files apply %v: want KindConflict with the warning, got %v", bad, err)
		}
	}
	g = actionFix()
	if err := buildStorageActionFix(g, domain.FixTempdbFiles, tf, domain.StorageFileState{}, nil); err != nil ||
		len(g.preview.Changes) != 4 || strings.Count(g.preview.Statement, "MODIFY FILE") != 3 || strings.Count(g.preview.Rollback, "MODIFY FILE") != 3 ||
		g.fix.ID != domain.FixTempdbFiles {
		t.Errorf("tempdb-files: %v %+v", err, g.preview)
	}
	// Equal TempDB files and no growth history: still offered, so a healthy set can be pre-sized from
	// the Storage card's TempDB row.
	if err := buildStorageActionFix(actionFix(), domain.FixTempdbFiles, f, domain.StorageFileState{}, nil); err != nil {
		t.Errorf("equal tempdb: %v", err)
	}
	// A file the findings did not name: nothing to apply (prepareGenericFix answers KindNotFound).
	if err := buildStorageActionFix(actionFix(), domain.FixAutogrowth, f, domain.StorageFileState{}, map[string]string{domain.OptionGrowthFile: "Scratch_log"}); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("healthy file: %v", err)
	}
}
