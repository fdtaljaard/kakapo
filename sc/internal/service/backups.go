package service

import (
	"context"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// prepareBackupFix reads the backup facts and builds one of the Backups card's fixes
// (docs/phase3g-contract.md): for the backup schedule, with the folder the user set checked on the
// server first.
func (s *Service) prepareBackupFix(ctx context.Context, g *genericFix, def domain.FixDef) error {
	f := readBackupFacts(ctx, g.db, g.database)
	if f.Error != "" {
		return newError(KindUnreachable, "%s: %s", g.srv.Name, f.Error)
	}
	g.backups = f
	var err error
	g.preview, g.fix, err = buildBackupFix(def.ID, f, g.options, func(folder string) (bool, error) {
		return collect.FolderExists(ctx, g.db, folder)
	})
	return err
}

// buildBackupFix builds one of the Backups card's fixes from the facts read and the options the
// user set. For the backup schedule it checks the folder with folderExists (on the server,
// xp_fileexist) between reading the facts and building the preview; a missing or malformed folder
// is not checked, the preview says what is wrong with it. A folder that cannot be checked is
// KindUnreachable, naming the permission. Nothing to apply is KindNotFound.
func buildBackupFix(fixID string, f domain.BackupFacts, options map[string]string, folderExists func(string) (bool, error)) (domain.FixPreview, domain.Fix, error) {
	if fixID == domain.FixRemoveBackupSchedule {
		p, fix, err := domain.BuildRemoveBackupScheduleFix(f)
		if err != nil {
			return p, fix, newError(KindNotFound, "nothing to remove on %s: kakapo created no backup job for it", f.Database)
		}
		return p, fix, nil
	}
	if !domain.OffersBackupSchedule(f) {
		return domain.FixPreview{}, domain.Fix{}, domain.ErrFixNotApplicable
	}
	folder := domain.BackupScheduleFolder(f, options)
	exists := false
	if domain.ValidBackupFolder(folder) {
		var err error
		if exists, err = folderExists(folder); err != nil {
			return domain.FixPreview{}, domain.Fix{}, newError(KindUnreachable, "could not check the folder %s on %s: %v. %s",
				folder, f.ServerName, err, domain.BackupFixPermission(f.ServerName))
		}
	}
	return domain.BuildBackupScheduleFix(f, f.Database, options, exists, domain.SameVolume(folder, f.DataFiles))
}

// applyBackupFix creates (or, for the remove fix, deletes) kakapo's backup jobs as the preview
// built them, reads them back, starts the full backup when asked, and writes the change log. On
// Express, or with a folder the preview could not use, nothing is applied (KindConflict).
func (s *Service) applyBackupFix(ctx context.Context, serverID int64, g *genericFix) (domain.FixResult, error) {
	if g.fix.ID == domain.FixRemoveBackupSchedule {
		return s.applyRemoveBackupSchedule(ctx, serverID, g)
	}
	name, p, server := g.database.Name, g.preview, g.backups.ServerName
	switch {
	case p.Express:
		return domain.FixResult{}, newError(KindConflict, "SQL Server Express has no Agent, so kakapo cannot create the schedule on %s: copy the script from the preview and schedule it in Windows Task Scheduler", server)
	case len(p.Changes) == 0 || p.Statement == "":
		msg := "the backup schedule cannot be created as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "%s", msg)
	}
	sched := domain.NewBackupSchedule(g.backups, g.options)
	if err := collect.ExecFix(ctx, g.db, p.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the backup jobs on %s: %v. %s",
			server, err, domain.BackupFixPermission(server))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      "Backup schedule",
		ChangeType:  "JOB",
		Description: sched.Description(),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	states, now, err := collect.ReadBackupJobs(ctx, g.db, name)
	if err != nil {
		// The jobs were created; log it even though we could not prove it.
		change.Result = "applied, but the jobs could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the backup jobs were created on %s, but could not be read back: %v", server, err)
	}
	// Started after the read-back, as its own statement: a started job cannot be rolled back.
	started, startErr := false, ""
	if sched.RunNow {
		start := sched.StartStatement()
		if err := collect.ExecFix(ctx, g.db, start); err != nil {
			startErr = err.Error()
		} else {
			started = true
			change.Statement += "\n" + start
		}
	}
	after, verified := sched.ReadBack(states, now, started, startErr)
	change.Result = after + " Differs from what was created."
	if verified {
		change.Result = after + " Verified."
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// applyRemoveBackupSchedule deletes kakapo's backup jobs on the database, reads back that they are
// gone, and writes the change log.
func (s *Service) applyRemoveBackupSchedule(ctx context.Context, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p, server := g.database.Name, g.preview, g.backups.ServerName
	sched, _ := domain.ExistingBackupSchedule(g.backups)
	names := sched.JobNames()
	if err := collect.ExecFix(ctx, g.db, p.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to delete the backup jobs on %s: %v. %s",
			server, err, domain.RemoveFixPermission(server))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      "Backup schedule",
		ChangeType:  "JOB",
		Description: domain.RemoveDescription(names),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	states, _, err := collect.ReadBackupJobs(ctx, g.db, name)
	if err != nil {
		change.Result = "applied, but the jobs could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the backup jobs were deleted on %s, but that could not be read back: %v", server, err)
	}
	after, verified := domain.RemoveReadBack(states)
	change.Result = after + ", differs"
	if verified {
		change.Result = after + ", verified"
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}
