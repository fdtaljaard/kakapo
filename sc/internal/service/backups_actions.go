package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The Backups card's phase 4d fixes (docs/phase4d-contract.md, part 2), registered with the fix
// registry: the recovery model (DBC08, BAK02), the weekly integrity check job (BAK03), a CHECKDB run
// now (BAK03, BAK04; a run, stored in checkdb_run) and clearing the suspect-page records after a
// clean run (BAK04). Each preview reads the backup facts with the last stored CHECKDB run; Apply
// runs the statement through collect.ExecFix (the CHECKDB run through collect.RunCheckdb), reads
// back and writes the change row.
func init() {
	registerFix(domain.FixRecoveryModel, prepareBackupAction, applyRecoveryModel)
	registerFix(domain.FixCheckdbJob, prepareBackupAction, applyCheckdbJob)
	// A long fix (docs/phase4f-contract.md): runs in the background, bounded by the 4-hour run.
	registerLongFix(domain.FixCheckdbNow, prepareBackupAction, applyCheckdbNow,
		longFix{timeout: domain.CheckdbRunTimeout + runReadMargin, completed: checkdbCompleted})
	registerFix(domain.FixSuspectPagesClear, prepareBackupAction, applySuspectPagesClear)
}

const (
	backupActionReadTimeout = 60 * time.Second
	backupActionTimeout     = 2 * time.Minute
)

// backupActionFacts is what the Backups card's phase 4d fixes are built from besides the backup
// facts: the integrity facts (data size, the jobs that check the database), the last CHECKDB room
// estimate and the suspect-page records.
type backupActionFacts struct {
	integrity domain.CheckdbJobFacts
	room      *domain.CheckdbRoom
	pages     []domain.SuspectPage
}

// readBackupActionFacts reads the backup facts (with the last stored CHECKDB run) into g.backups and
// what the fix needs besides. Nothing read is KindUnreachable.
func (s *Service) readBackupActionFacts(ctx context.Context, g *genericFix, fixID string) (backupActionFacts, error) {
	var a backupActionFacts
	f := readBackupFacts(ctx, g.db, g.database)
	if f.Error != "" {
		return a, newError(KindUnreachable, "%s: %s", g.srv.Name, f.Error)
	}
	run, err := s.store.LastCheckdbRun(ctx, g.srv.ID, g.database.Name)
	if err != nil {
		return a, err
	}
	f.LastCheckdbRun = run
	g.backups = f

	readCtx, cancel := context.WithTimeout(ctx, backupActionReadTimeout)
	defer cancel()
	switch fixID {
	case domain.FixCheckdbJob, domain.FixCheckdbNow:
		if a.integrity, err = collect.CheckdbJobFacts(readCtx, g.db, g.database.Name); err != nil {
			a.integrity = domain.CheckdbJobFacts{DataError: err.Error(), JobsError: err.Error()}
		}
		if a.room, err = s.store.LastCheckdbRoom(ctx, g.srv.ID, g.database.Name); err != nil {
			return a, err
		}
	case domain.FixSuspectPagesClear:
		if a.pages, err = collect.ReadSuspectPages(readCtx, g.db, g.database.Name); err != nil {
			return a, newError(KindUnreachable, "could not read msdb.dbo.suspect_pages on %s: %v", g.srv.Name, err)
		}
	}
	return a, nil
}

// buildBackupActionFix builds one of the Backups card's phase 4d fixes from what was read and the
// options into g. A preview with a bad option has no changes; ErrFixNotApplicable when the finding
// behind the fix did not fire.
func buildBackupActionFix(g *genericFix, fixID string, f domain.BackupFacts, a backupActionFacts, options map[string]string) error {
	var err error
	switch fixID {
	case domain.FixRecoveryModel:
		g.preview, g.fix, err = domain.BuildRecoveryModelFix(f, g.database.ReadOnly, options)
	case domain.FixCheckdbJob:
		g.preview, g.fix, err = domain.BuildCheckdbJobFix(f, a.integrity, a.room, options)
	case domain.FixCheckdbNow:
		g.preview, g.fix = domain.BuildCheckdbNowFix(f, a.integrity.DataMB, a.room, options)
	case domain.FixSuspectPagesClear:
		g.preview, g.fix, err = domain.BuildSuspectPagesClearFix(f, a.pages)
	default:
		err = domain.ErrUnknownFix
	}
	return err
}

func prepareBackupAction(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	a, err := s.readBackupActionFacts(ctx, g, def.ID)
	if err != nil {
		return err
	}
	return buildBackupActionFix(g, def.ID, g.backups, a, options)
}

// logBackupAction writes the change row of a phase 4d Backups fix with what was read back; when the
// read-back failed, the row says so and the error names it.
func (s *Service) logBackupAction(ctx context.Context, change domain.Change, g *genericFix, after string, verified bool, readErr error, what string) (domain.FixResult, error) {
	if readErr != nil {
		change.Result = "applied, but " + what + " could not be read back: " + readErr.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the change ran on %s, but %s could not be read back: %v", g.srv.Name, what, readErr)
	}
	return s.logIndexChange(ctx, change, g.preview, g.fix.Current, after, verified, change.ChangedAt)
}

func applyRecoveryModel(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the recovery model cannot be changed as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	name, p := g.database.Name, g.preview
	applyCtx, cancel := context.WithTimeout(ctx, backupActionTimeout)
	err := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not change the recovery model of %s: %v. %s",
			name, err, domain.PermissionHint(domain.ScopeDatabase, name))
	}
	change := domain.Change{ServerID: serverID, Database: name, ChangedAt: time.Now().UTC().Truncate(time.Microsecond),
		Object: g.fix.Setting, ChangeType: "SETTING", Description: domain.FixChangeDescription(g.fix, g.srv.Name, name),
		Statement: p.Statement, Rollback: p.Rollback}
	model, readErr := collect.ReadRecoveryModel(ctx, g.db, name)
	after, verified := domain.RecoveryModelReadBack(model, g.fix.Target)
	return s.logBackupAction(ctx, change, g, after, verified, readErr, "the recovery model")
}

func applyCheckdbJob(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the integrity check job cannot be created as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	name, p, server := g.database.Name, g.preview, g.backups.ServerName
	applyCtx, cancel := context.WithTimeout(ctx, backupActionTimeout)
	err := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the integrity check job on %s: %v. %s",
			server, err, domain.BackupFixPermission(server))
	}
	j := domain.CheckdbJobFromOptions(g.backups, p.Options)
	change := domain.Change{ServerID: serverID, Database: name, ChangedAt: time.Now().UTC().Truncate(time.Microsecond),
		Object: "CHECKDB job", ChangeType: "JOB", Description: j.Description(), Statement: p.Statement, Rollback: p.Rollback}
	states, now, readErr := collect.ReadCheckdbJob(ctx, g.db, name)
	after, verified := j.ReadBack(states, now)
	return s.logBackupAction(ctx, change, g, after, verified, readErr, "the job")
}

// applyCheckdbNow runs DBCC CHECKDB as the preview built it, with a 4-hour timeout, stores the run
// (checkdb_run) and writes the change row (type CHECK, no rollback) with its outcome. A run that
// did not finish its check (refused, cancelled, timed out) is not stored: KindUnreachable says why.
func applyCheckdbNow(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "CHECKDB cannot run as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	name, p := g.database.Name, g.preview
	start := time.Now().UTC().Truncate(time.Microsecond)
	runCtx, cancel := context.WithTimeout(ctx, domain.CheckdbRunTimeout)
	msgs, err := collect.RunCheckdb(runCtx, g.db, p.Statement)
	cancel()
	took := time.Since(start)
	runErr := ""
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		runErr = "it ran for more than 4 hours and was stopped; run it in a quiet period, WITH PHYSICAL_ONLY, or on a restored copy"
	case err != nil:
		runErr = err.Error()
	}
	run, completed := domain.BuildCheckdbRun(start, took, domain.PhysicalOnlyOption(p.Options), msgs, runErr)
	if !completed {
		msg := strings.TrimPrefix(run.Outcome, "Did not complete: ")
		if lower := strings.ToLower(msg); strings.Contains(lower, "permission") || strings.Contains(lower, "denied") {
			msg += " (CHECKDB needs db_owner of the database or sysadmin)"
		}
		return domain.FixResult{}, newError(KindUnreachable, "DBCC CHECKDB on %s did not complete: %s", name, msg)
	}
	// The run finished: keep it even when the page that asked has gone.
	keep := context.WithoutCancel(ctx)
	if err := s.store.InsertCheckdbRun(keep, serverID, name, run); err != nil {
		return domain.FixResult{}, err
	}
	change := domain.Change{ServerID: serverID, Database: name, ChangedAt: start, Object: "CHECKDB", ChangeType: "CHECK",
		Description: domain.CheckdbRunChangeDescription(name, run), Statement: p.Statement, Result: run.Outcome}
	id, err := s.insertChange(keep, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: run.Outcome, Verified: run.Clean, ChangeID: id, AppliedAt: start}, nil
}

// checkdbUnread is a CHECKDB run's outcome when its connection was lost and LastGoodCheckDbTime did
// not move: the check ended (its request is gone), but how is in the SQL Server error log only.
const checkdbUnread = "CHECKDB finished with a result kakapo could not read: look at the SQL Server error log (error 8989 there gives the errors it found)"

// checkdbCompleted reads a CHECKDB run back after its connection was lost (docs/phase4f-contract.md):
// a LastGoodCheckDbTime later than the one the preview read means it came back clean, and the run is
// stored as the apply would; none later means it finished with a result kakapo could not read (an
// error 8989 is in the error log, out of reach), which is logged but not stored as a run: a run that
// could not be read is not one the Backups card can rely on.
func checkdbCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, backupActionReadTimeout)
	last, err := collect.ReadLastGoodCheckdb(readCtx, g.db, g.database.Name)
	cancel()
	if err != nil {
		return recovered{}, false, err
	}
	name, p := g.database.Name, g.preview
	// The request was last seen up to a quiet period before now.
	took := max(0, time.Since(startedAt)-runQuietAfter)
	change := domain.Change{Object: "CHECKDB", ChangeType: "CHECK", Statement: p.Statement}
	if before := g.backups.LastGoodCheckDb; last != nil && (before == nil || last.After(*before)) {
		run, _ := domain.BuildCheckdbRun(startedAt, took, domain.PhysicalOnlyOption(p.Options), nil, "")
		run.Messages = []string{"kakapo: " + lostConnectionNote + " (LastGoodCheckDbTime " + last.Format("2006-01-02 15:04:05") + ")."}
		change.Description = domain.CheckdbRunChangeDescription(name, run)
		return recovered{after: run.Outcome, verified: true, result: run.Outcome, change: change, checkdb: &run}, true, nil
	}
	run := domain.CheckdbRun{RanAt: startedAt, DurationSeconds: took.Seconds(), PhysicalOnly: domain.PhysicalOnlyOption(p.Options), Outcome: checkdbUnread}
	change.Description = domain.CheckdbRunChangeDescription(name, run)
	return recovered{after: checkdbUnread, result: checkdbUnread, change: change}, true, nil
}

// applySuspectPagesClear deletes this database's suspect-page records after reading them again (the
// change row keeps them: there is no rollback), and reads back that none is left.
func applySuspectPagesClear(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := actionApplyRefusal(g, "the suspect-page records cannot be cleared as previewed"); err != nil {
		return domain.FixResult{}, err
	}
	name, p := g.database.Name, g.preview
	removed, err := collect.ReadSuspectPages(ctx, g.db, name)
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "could not read msdb.dbo.suspect_pages on %s before clearing it: %v", g.srv.Name, err)
	}
	applyCtx, cancel := context.WithTimeout(ctx, backupActionTimeout)
	err = collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not clear the suspect-page records of %s: %v. Needs DELETE on msdb.dbo.suspect_pages (sysadmin, or db_owner of msdb).", name, err)
	}
	change := domain.Change{ServerID: serverID, Database: name, ChangedAt: time.Now().UTC().Truncate(time.Microsecond),
		Object: "msdb.dbo.suspect_pages", ChangeType: "CLEAR", Description: domain.SuspectPagesChangeDescription(name, len(removed)),
		Statement: p.Statement}
	remaining, readErr := collect.ReadSuspectPages(ctx, g.db, name)
	if readErr != nil {
		// Keep what was removed on record even though what is left could not be read.
		after, _ := domain.SuspectPagesReadBack(removed, nil)
		change.Result = after + " Could not read what is left: " + readErr.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the records were cleared on %s, but what is left could not be read back: %v", g.srv.Name, readErr)
	}
	after, verified := domain.SuspectPagesReadBack(removed, remaining)
	change.Result = after
	id, err := s.insertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: after, Verified: verified, ChangeID: id, AppliedAt: change.ChangedAt}, nil
}
