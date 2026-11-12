package service

import (
	"context"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The Storage card's fixes (docs/phase4d-contract.md, part 2), registered with the fix registry:
// a fixed autogrowth on one file (STO02, STO03), more TempDB data files (TMP01) and the TempDB file
// set (TMP02, docs/phase4e-contract.md part 1). The preview reads the storage facts (to see that the
// finding fired) and the file state (instant file initialization); Apply runs the statement through
// collect.ExecFix, reads the files back and writes the change row with the rollback.
func init() {
	registerFix(domain.FixAutogrowth, prepareAutogrowth, applyAutogrowth)
	registerFix(domain.FixAddTempdbFiles, prepareAddTempdbFiles, applyAddTempdbFiles)
	// A long fix (docs/phase4f-contract.md): growing the files without instant file initialization
	// writes zeros, so it runs in the background.
	registerLongFix(domain.FixTempdbFiles, prepareTempdbFiles, applyTempdbFiles,
		longFix{timeout: tempdbSetupApplyTimeout + runReadMargin, completed: tempdbFilesCompleted})
}

const (
	storageActionReadTimeout = 60 * time.Second
	autogrowthApplyTimeout   = 2 * time.Minute
	// Adding TempDB files writes them out in full when instant file initialization is off.
	tempdbApplyTimeout = 10 * time.Minute
	// Growing TempDB's files to their starting size writes zeros without instant file
	// initialization; a large set on a slow disk takes a while.
	tempdbSetupApplyTimeout = 30 * time.Minute
)

// actionApplyRefusal is the error an apply returns, before touching the server, when the preview it
// was built from cannot be applied: an Express script, or a blocking warning (no changes).
func actionApplyRefusal(g *genericFix, fallback string) error {
	p := g.preview
	if p.Express {
		return newError(KindConflict, "SQL Server Express has no Agent, so kakapo cannot create the job on %s: copy the script from the preview and schedule it in Windows Task Scheduler", g.srv.Name)
	}
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := fallback
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return newError(KindConflict, "%s", msg)
	}
	return nil
}

// readStorageForFix reads the storage facts a storage fix is built from; nothing read is
// KindUnreachable.
func readStorageForFix(ctx context.Context, g *genericFix) (domain.StorageFacts, error) {
	f := readStorageFacts(ctx, g.db, g.database)
	if f.Error != "" {
		return f, newError(KindUnreachable, "%s: %s", g.srv.Name, f.Error)
	}
	return f, nil
}

// readFileState reads the files and instant file initialization; a failure is kept as the IFI
// reason, since the preview only needs IFI from it.
func readFileState(ctx context.Context, g *genericFix) domain.StorageFileState {
	readCtx, cancel := context.WithTimeout(ctx, storageActionReadTimeout)
	defer cancel()
	st, err := collect.StorageFileState(readCtx, g.db, g.database.Name)
	if err != nil && st.IFI == nil && st.IFIError == "" {
		st.IFIError = err.Error()
	}
	return st
}

// buildStorageActionFix builds one of the storage fixes from what was read (the facts, the file
// state) and the options into g. A preview with a bad option has no changes.
func buildStorageActionFix(g *genericFix, fixID string, f domain.StorageFacts, st domain.StorageFileState, options map[string]string) error {
	var err error
	switch fixID {
	case domain.FixAutogrowth:
		g.preview, g.fix, err = domain.BuildAutogrowthFix(f, st, options)
	case domain.FixAddTempdbFiles:
		g.preview, g.fix, err = domain.BuildAddTempdbFilesFix(f, options)
	case domain.FixTempdbFiles:
		g.preview, g.fix, err = domain.BuildTempdbFilesFix(f, st, options)
	default:
		err = domain.ErrUnknownFix
	}
	return err
}

func prepareAutogrowth(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	f, err := readStorageForFix(ctx, g)
	if err != nil {
		return err
	}
	return buildStorageActionFix(g, def.ID, f, readFileState(ctx, g), options)
}

func prepareAddTempdbFiles(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	f, err := readStorageForFix(ctx, g)
	if err != nil {
		return err
	}
	return buildStorageActionFix(g, def.ID, f, domain.StorageFileState{}, options)
}

func applyAutogrowth(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the autogrowth cannot be set as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	p := g.preview
	file, fileDB, _, _, _ := domain.AutogrowthTarget(p.Options)
	applyCtx, cancel := context.WithTimeout(ctx, autogrowthApplyTimeout)
	err := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not change the autogrowth of %s in %s: %v. %s",
			file, fileDB, err, domain.PermissionHint(domain.ScopeDatabase, fileDB))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   appliedAt,
		Object:      "File " + fileDB + "." + file,
		ChangeType:  "SETTING",
		Description: domain.AutogrowthChangeDescription(g.fix),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	readCtx, cancel := context.WithTimeout(ctx, storageActionReadTimeout)
	st, err := collect.StorageFileState(readCtx, g.db, g.database.Name)
	cancel()
	if err != nil {
		change.Result = "applied, but the file could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the autogrowth of %s was changed, but could not be read back: %v", file, err)
	}
	after, verified := domain.AutogrowthReadBack(st, p.Options)
	return s.logIndexChange(ctx, change, p, g.fix.Current, after, verified, appliedAt)
}

func applyAddTempdbFiles(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the TempDB files cannot be added as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	p := g.preview
	added := domain.AddedTempdbFiles(p)
	applyCtx, cancel := context.WithTimeout(ctx, tempdbApplyTimeout)
	execErr := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   appliedAt,
		Object:      "TempDB files",
		ChangeType:  "FILE",
		Description: domain.TempdbChangeDescription(g.fix, added),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	readCtx, cancel := context.WithTimeout(ctx, storageActionReadTimeout)
	st, readErr := collect.StorageFileState(readCtx, g.db, g.database.Name)
	cancel()
	if execErr != nil {
		// ALTER DATABASE cannot run in a transaction: the files before the failing one stay. Log
		// what is there when any was added, so the rollback is on record.
		if readErr == nil && anyAdded(st, added) {
			after, _ := domain.TempdbFilesReadBack(st, added)
			change.Result = "partly applied: " + execErr.Error() + "; " + after
			if _, logErr := s.insertChange(ctx, change); logErr != nil {
				return domain.FixResult{}, logErr
			}
		}
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not add the TempDB files: %v. %s",
			execErr, domain.PermissionHint(domain.ScopeDatabase, "tempdb"))
	}
	if readErr != nil {
		change.Result = "applied, but the files could not be read back: " + readErr.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the TempDB files were added, but could not be read back: %v", readErr)
	}
	after, verified := domain.TempdbFilesReadBack(st, added)
	return s.logIndexChange(ctx, change, p, g.fix.Current, after, verified, appliedAt)
}

func prepareTempdbFiles(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	f, err := readStorageForFix(ctx, g)
	if err != nil {
		return err
	}
	return buildStorageActionFix(g, def.ID, f, readFileState(ctx, g), options)
}

// applyTempdbFiles runs the TempDB file set batch, reads tempdb.sys.database_files back and logs the
// change with the previous growths as its rollback (sizes cannot be rolled back).
func applyTempdbFiles(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the TempDB files cannot be set up as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	p := g.preview
	applyCtx, cancel := context.WithTimeout(ctx, tempdbSetupApplyTimeout)
	execErr := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   appliedAt,
		Object:      "TempDB files",
		ChangeType:  "SETTING",
		Description: domain.TempdbSetupChangeDescription(g.fix),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	readCtx, cancel := context.WithTimeout(ctx, storageActionReadTimeout)
	st, readErr := collect.StorageFileState(readCtx, g.db, g.database.Name)
	cancel()
	if execErr != nil {
		// ALTER DATABASE cannot run in a transaction: the files before the failing statement keep
		// their change. Log what is there when anything changed, so the rollback is on record.
		if readErr == nil && domain.TempdbSetupChanged(p, st) {
			after, _ := domain.TempdbSetupReadBack(st, p.Options)
			change.Result = "partly applied: " + execErr.Error() + "; " + after
			if _, logErr := s.insertChange(ctx, change); logErr != nil {
				return domain.FixResult{}, logErr
			}
		}
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not set up the TempDB files: %v. %s",
			execErr, domain.PermissionHint(domain.ScopeDatabase, "tempdb"))
	}
	if readErr != nil {
		change.Result = "applied, but the files could not be read back: " + readErr.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the TempDB files were set up, but could not be read back: %v", readErr)
	}
	after, verified := domain.TempdbSetupReadBack(st, p.Options)
	return s.logIndexChange(ctx, change, p, g.fix.Current, after, verified, appliedAt)
}

// tempdbFilesCompleted reads a tempdb-files run back after its connection was lost
// (docs/phase4f-contract.md): done when the files are at the target, or when some changed (the
// statements before a failing one keep their change), which is logged as not verified.
func tempdbFilesCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	p := g.preview
	readCtx, cancel := context.WithTimeout(ctx, storageActionReadTimeout)
	st, err := collect.StorageFileState(readCtx, g.db, g.database.Name)
	cancel()
	if err != nil {
		return recovered{}, false, err
	}
	after, verified := domain.TempdbSetupReadBack(st, p.Options)
	if !verified && !domain.TempdbSetupChanged(p, st) {
		return recovered{after: "the TempDB files are as they were: the change did not finish; apply it again"}, false, nil
	}
	return recovered{after: after, verified: verified, change: domain.Change{Object: "TempDB files", ChangeType: "SETTING",
		Description: domain.TempdbSetupChangeDescription(g.fix), Statement: p.Statement, Rollback: p.Rollback}}, true, nil
}

// anyAdded says whether any of the files a preview adds is in tempdb now.
func anyAdded(st domain.StorageFileState, added []string) bool {
	for _, x := range st.Files {
		if x.DatabaseID != 2 {
			continue
		}
		for _, name := range added {
			if x.LogicalName == name {
				return true
			}
		}
	}
	return false
}
