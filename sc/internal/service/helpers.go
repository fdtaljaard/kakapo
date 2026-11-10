package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// kakapo's installed helpers (docs/phase5e-contract.md): the event session that keeps every block and
// deadlock, created by install-xe-session and dropped by remove-xe-session, both registered with the
// fix registry; and the instance page's "Installed helpers" row (Helpers). Each prepare reads the
// session's state (collect/helpers.go), the pending settings and, for the install, whether the folder
// exists, and builds the preview in domain; each apply runs the previewed statement through
// collect.ExecFix, reads the state back and writes the change row (type HELPER) with the rollback.

func init() {
	registerFix(domain.FixInstallXESession, prepareInstallXESession, applyInstallXESession)
	registerFix(domain.FixRemoveXESession, prepareRemoveXESession, applyRemoveXESession)
}

// xeThresholdKey keeps the threshold in use when the preview was read, so a statement that failed part
// way can tell whether anything changed.
const xeThresholdKey = "kakapo.threshold"

// xeHint is the permission the helper's fixes need, as a refusal names it.
const xeHint = "Needs ALTER ANY EVENT SESSION and ALTER SETTINGS (sysadmin)."

// readXEPending reads the settings waiting for RECONFIGURE (INS14), which the threshold's RECONFIGURE
// also activates.
func readXEPending(ctx context.Context, g *genericFix) ([]domain.PendingConfig, error) {
	cfg, err := collect.ReadServerConfig(ctx, g.db)
	if err != nil {
		return nil, newError(KindUnreachable, "could not read the server configuration of %s: %v", g.srv.Name, err)
	}
	return cfg.PendingConfigs(), nil
}

// --- install-xe-session ---

func prepareInstallXESession(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's event session on %s: %v", g.srv.Name, err)
	}
	f := domain.XEInstallFacts{HelperFacts: hf, Folder: domain.XEFolder(hf, options)}
	if !(hf.Session.Exists && hf.Session.Ours) && domain.ValidXEFolder(f.Folder) {
		if exists, err := collect.FolderExists(rctx, g.db, f.Folder); err != nil {
			f.FolderError = err.Error()
		} else {
			f.FolderChecked, f.FolderExists = true, exists
		}
	}
	if f.Pending, err = readXEPending(rctx, g); err != nil {
		return err
	}
	return installXEPreview(g, f, options)
}

func installXEPreview(g *genericFix, f domain.XEInstallFacts, options map[string]string) error {
	p, fix, err := domain.BuildInstallXESessionFix(f, options)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		// kakapo's session is there already: the preview says so plainly, with nothing to apply (an
		// apply is refused by suApplyRefusal), rather than failing as an error.
		g.preview, g.fix = p, fix
		return nil
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.XEInstallDescription(suServerLabel(g, f.ServerName), p, f.Pending))
	suKeep(g, xeThresholdKey, strconv.Itoa(f.Session.ThresholdSeconds))
	return suPreviewed(g, p, fix)
}

func applyInstallXESession(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	threshold, _ := strconv.Atoi(g.preview.Options[domain.OptionXEThreshold])
	change := suChange(serverID, g, domain.XEObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, s.xePartial(ctx, g, change, err, "create kakapo's event session", func(after domain.XESession) bool {
			return after.Exists
		})
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "kakapo's event session")
	}
	after, ok := domain.XEInstallReadBack(hf.Session, threshold)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- remove-xe-session ---

func prepareRemoveXESession(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read kakapo's event session on %s: %v", g.srv.Name, err)
	}
	f := domain.XERemoveFacts{HelperFacts: hf}
	if folder := hf.Session.Folder(); hf.Session.Ours && folder != "" {
		if files, err := collect.ReadXEFiles(rctx, g.db, folder); err != nil {
			f.FilesError = err.Error()
		} else {
			f.Files = files
		}
	}
	if f.Pending, err = readXEPending(rctx, g); err != nil {
		return err
	}
	return removeXEPreview(g, f, options)
}

func removeXEPreview(g *genericFix, f domain.XERemoveFacts, options map[string]string) error {
	p, fix, err := domain.BuildRemoveXESessionFix(f, options)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "there is no event session named kakapo on %s: nothing to remove", suServerLabel(g, f.ServerName))
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.XERemoveDescription(suServerLabel(g, f.ServerName), p, f.Session.FilePattern(), f.Pending))
	suKeep(g, xeThresholdKey, strconv.Itoa(f.Session.ThresholdSeconds))
	return suPreviewed(g, p, fix)
}

func applyRemoveXESession(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	threshold, _ := strconv.Atoi(g.preview.Options[domain.OptionXEThreshold])
	change := suChange(serverID, g, domain.XEObject, domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, s.xePartial(ctx, g, change, err, "remove kakapo's event session", func(after domain.XESession) bool {
			return !after.Exists
		})
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The removal of kakapo's event session")
	}
	after, ok := domain.XERemoveReadBack(hf.Session, threshold)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// xePartial answers a statement SQL Server refused. It reads the session again: when part of the
// statement ran (the session changed as moved, or the threshold in use is no longer the one read
// before), the change row says so with the rollback, so the change log and the Installed helpers row
// show it; nothing is logged when nothing changed or nothing can be read.
func (s *Service) xePartial(ctx context.Context, g *genericFix, change domain.Change, execErr error, what string, moved func(domain.XESession) bool) error {
	refused := newError(KindUnreachable, "SQL Server refused to %s on %s: %v. %s", what, g.srv.Name, execErr, xeHint)
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, g.db)
	if err != nil {
		return refused
	}
	before, perr := strconv.Atoi(g.options[xeThresholdKey])
	after := hf.Session
	if !moved(after) && (perr != nil || after.ThresholdSeconds == before) {
		return refused
	}
	now, _ := domain.XEInstallReadBack(after, -2)
	change.Result = "failed part way (" + execErr.Error() + "); now: " + now
	if _, err := s.store.InsertChange(ctx, change); err != nil {
		return err
	}
	return newError(KindUnreachable, "SQL Server refused to %s on %s part way: %v. Now: %s; the change log has the rollback. %s",
		what, g.srv.Name, execErr, now, xeHint)
}

// --- the instance page's row ---

// Helpers reads where kakapo's installed helpers stand on a server for the instance page
// (docs/phase5e-contract.md): the event session's state, and the database the fix routes go through.
// KindNotFound for an unknown server, KindUnreachable when the server cannot be read.
func (s *Service) Helpers(ctx context.Context, serverID int64) (domain.InstalledHelpers, error) {
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.InstalledHelpers{}, err
	}
	defer db.Close()
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	hf, err := collect.ReadHelpers(rctx, db)
	if err != nil {
		return domain.InstalledHelpers{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if hf.ServerName == "" {
		hf.ServerName = srv.Name
	}
	// Phase 5p: kakapo's maintenance procedure, on the same row.
	hf.Maintenance = readMaintFor(rctx, db, "")
	return domain.BuildInstalledHelpers(hf, helperDatabase(rctx, db, srv)), nil
}

// helperDatabase is the database the helper's fixes go through: the remembered one when it is online,
// else the first online user database; "" when none is, or they cannot be listed.
func helperDatabase(ctx context.Context, db *sql.DB, srv domain.Server) string {
	dbs, err := collect.ListDatabases(ctx, db)
	if err != nil {
		return ""
	}
	return pickHelperDatabase(dbs, srv.Database)
}

// pickHelperDatabase picks the database the helper's fixes go through.
func pickHelperDatabase(dbs []domain.DatabaseStatus, remembered string) string {
	first := ""
	for _, d := range dbs {
		if !strings.EqualFold(d.State, "ONLINE") {
			continue
		}
		if remembered != "" && strings.EqualFold(d.Name, remembered) {
			return d.Name
		}
		if first == "" {
			first = d.Name
		}
	}
	return first
}

// xeReadForHistory reads kakapo's session for the blocking history and the deadlock report: the files
// they read first, and the session as BLK01's Do this needs it (nil when it could not be told whether it
// exists).
func xeReadForHistory(ctx context.Context, db *sql.DB) (domain.XEFiles, *domain.XESession) {
	hf, err := collect.ReadHelpers(ctx, db)
	if err != nil {
		return domain.XEFiles{}, nil
	}
	st := hf.Session
	if !st.Exists && st.Error != "" {
		return domain.XEFiles{}, nil
	}
	return st.Files(), &st
}

// xeSourceWords is how an error names where a history was read from.
func xeSourceWords(files domain.XEFiles) string {
	if files.Pattern != "" {
		return fmt.Sprintf("kakapo's session files (%s) or the system_health session", files.Pattern)
	}
	return "the system_health session"
}
