package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// kakapo's maintenance procedure (docs/phase5p-contract.md), registered with the fix registry:
// install-maintenance-proc creates master.dbo.kakapo_Maintenance with its two tables (or updates it in
// place) and upserts the settings row of the database it is applied from, then with Run now calls it
// once for that database; remove-maintenance-proc drops everything, or deletes one database's row. Each
// prepare reads the procedure's state (collect/maintproc.go) and builds the preview in domain; each apply
// runs the previewed batches through collect.ExecFixBatches, reads back and writes the change row (type
// HELPER) with the rollback.

func init() {
	registerFix(domain.FixInstallMaintenanceProc, prepareInstallMaintProc, applyInstallMaintProc)
	registerFix(domain.FixRemoveMaintenanceProc, prepareRemoveMaintProc, applyRemoveMaintProc)
}

// Keys of what the prepares keep for their applies.
const (
	maintWantKey      = "kakapo.maintSettings"
	maintInstalledKey = "kakapo.maintInstalled"
)

// maintHint is the permission the procedure's fixes need, as a refusal names it.
const maintHint = "Needs sysadmin (objects in master)."

// maintRunNowTimeout bounds Run now: the first full backup, and whatever else is due, of one database.
const maintRunNowTimeout = 4 * time.Hour

// --- install-maintenance-proc ---

func prepareInstallMaintProc(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	mf, err := collect.ReadMaintProc(rctx, g.db, g.database.Name)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's maintenance procedure on %s: %v", g.srv.Name, err)
	}
	f := domain.MaintInstallFacts{MaintProcFacts: mf}
	if folder := domain.MaintFolder(mf, options); domain.ValidBackupFolder(folder) {
		if exists, err := collect.FolderExists(rctx, g.db, folder); err != nil {
			f.FolderError = err.Error()
		} else {
			f.FolderChecked, f.FolderExists = true, exists
		}
	}
	f.Sizes = collect.BackupSizes(rctx, g.db, g.database.Name)
	return installMaintPreview(g, f, options)
}

func installMaintPreview(g *genericFix, f domain.MaintInstallFacts, options map[string]string) error {
	p, fix, err := domain.BuildInstallMaintenanceProcFix(f, options)
	if err != nil {
		return err
	}
	if len(p.Changes) > 0 {
		want, err := json.Marshal(maintWanted(f.MaintProcFacts, p))
		if err != nil {
			return err
		}
		suKeep(g, maintWantKey, string(want))
	}
	suKeep(g, suDescriptionKey, domain.MaintInstallDescription(suServerLabel(g, f.ServerName), p, f.MaintProcFacts))
	suKeep(g, maintInstalledKey, boolText(f.ProcExists))
	return suPreviewed(g, p, fix)
}

// maintWanted is the settings row the preview writes, read back from its options as domain built them.
func maintWanted(f domain.MaintProcFacts, p domain.FixPreview) domain.MaintSettings {
	// Rebuilding with the echoed options gives the row exactly as previewed.
	opts := map[string]string{}
	for k, v := range p.Options {
		opts[k] = v
	}
	s, _ := domain.MaintSettingsFor(f, opts)
	return s
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func applyInstallMaintProc(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	var want domain.MaintSettings
	if err := json.Unmarshal([]byte(g.options[maintWantKey]), &want); err != nil {
		return domain.FixResult{}, newError(KindConflict, "the maintenance procedure was not prepared")
	}
	change := suChange(serverID, g, domain.MaintObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if err := collect.ExecFixBatches(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, s.maintPartial(ctx, g, change, err, "install kakapo's maintenance procedure", func(after domain.MaintProcFacts) bool {
			_, has := after.Row(want.Database)
			return (after.ProcExists || after.SettingsExists || after.LogExists) && (g.options[maintInstalledKey] != "true" || has)
		})
	}
	runNow := g.preview.Options[domain.OptionProcRunNow] == "true"
	runErr := ""
	var runs []domain.MaintRun
	if runNow {
		runs, runErr = maintRunNow(ctx, g.db, want.Database)
	}
	// Read back after Run now, with a read bound of its own: Run now may take long.
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, err := collect.ReadMaintProc(rctx, g.db, want.Database)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "kakapo's maintenance procedure")
	}
	text, ok := domain.MaintInstallReadBack(after, want, runNow, runErr, runs)
	return s.suLog(ctx, change, g, g.fix.Current, text, ok)
}

// maintRunNow calls the procedure once for one database (install-maintenance-proc's Run now) and reads
// the log rows the call wrote; the error is why the call raised (a task failed) or could not run.
func maintRunNow(ctx context.Context, db *sql.DB, database string) ([]domain.MaintRun, string) {
	mctx, cancel := suReadCtx(ctx)
	mark, err := collect.MaintLogMark(mctx, db)
	cancel()
	if err != nil {
		return nil, err.Error()
	}
	runErr := ""
	runCtx, runCancel := context.WithTimeout(ctx, maintRunNowTimeout)
	if err := collect.ExecFix(runCtx, db, domain.MaintRunNowStatement(database)); err != nil {
		runErr = err.Error()
	}
	runCancel()
	rctx, rcancel := suReadCtx(ctx)
	defer rcancel()
	runs, err := collect.ReadMaintRuns(rctx, db, database, mark)
	if err != nil && runErr == "" {
		runErr = "the log could not be read after the call: " + err.Error()
	}
	return runs, runErr
}

// --- remove-maintenance-proc ---

func prepareRemoveMaintProc(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	mf, err := collect.ReadMaintProc(rctx, g.db, g.database.Name)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's maintenance procedure on %s: %v", g.srv.Name, err)
	}
	return removeMaintPreview(g, mf, options)
}

func removeMaintPreview(g *genericFix, f domain.MaintProcFacts, options map[string]string) error {
	p, fix, err := domain.BuildRemoveMaintenanceProcFix(f, options)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "kakapo's maintenance procedure is not installed on %s: nothing to remove", suServerLabel(g, f.ServerName))
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.MaintRemoveDescription(suServerLabel(g, f.ServerName), p.Options[domain.OptionProcRemove]))
	return suPreviewed(g, p, fix)
}

func applyRemoveMaintProc(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	what := g.preview.Options[domain.OptionProcRemove]
	change := suChange(serverID, g, domain.MaintObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if err := collect.ExecFixBatches(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, s.maintPartial(ctx, g, change, err, "remove kakapo's maintenance procedure", func(after domain.MaintProcFacts) bool {
			_, ok := domain.MaintRemoveReadBack(after, what)
			return ok || (what == domain.MaintRemoveAll && !after.ProcExists)
		})
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, err := collect.ReadMaintProc(rctx, g.db, g.database.Name)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The removal of kakapo's maintenance procedure")
	}
	text, ok := domain.MaintRemoveReadBack(after, what)
	return s.suLog(ctx, change, g, g.fix.Current, text, ok)
}

// maintPartial answers a script SQL Server refused part way. It reads the procedure again: when part of
// the script ran (moved says so), the change row says so with the rollback; nothing is logged when
// nothing changed or nothing can be read.
func (s *Service) maintPartial(ctx context.Context, g *genericFix, change domain.Change, execErr error, what string, moved func(domain.MaintProcFacts) bool) error {
	refused := newError(KindUnreachable, "SQL Server refused to %s on %s: %v. %s", what, g.srv.Name, execErr, maintHint)
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, err := collect.ReadMaintProc(rctx, g.db, g.database.Name)
	if err != nil || !moved(after) {
		return refused
	}
	now := "nothing of kakapo's maintenance procedure in master"
	if after.ProcExists || after.SettingsExists || after.LogExists {
		now, _ = domain.MaintRemoveReadBack(after, domain.MaintRemoveAll)
	}
	change.Result = "failed part way (" + execErr.Error() + "); now: " + now
	if _, err := s.store.InsertChange(ctx, change); err != nil {
		return err
	}
	return newError(KindUnreachable, "SQL Server refused to %s on %s part way: %v. Now: %s; the change log has the rollback. %s",
		what, g.srv.Name, execErr, now, maintHint)
}

// readMaintFor reads kakapo's maintenance procedure for the plan and the Installed helpers row: a read
// that fails is still an answer, its Error says why.
func readMaintFor(ctx context.Context, db *sql.DB, database string) *domain.MaintProcFacts {
	mf, err := collect.ReadMaintProc(ctx, db, database)
	if err != nil {
		mf.Error = err.Error()
	}
	return &mf
}
