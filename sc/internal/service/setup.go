package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// readSetupFacts reads what the Setup card needs (docs/phase3c-contract.md) and the database's
// Query Store, which it keeps as a snapshot as the database list does; d gets its Query Store
// state. When nothing can be read, the facts say why, so the card turns grey instead of failing
// the page.
func (s *Service) readSetupFacts(ctx context.Context, db *sql.DB, serverID int64, d *domain.DatabaseStatus) (domain.SetupFacts, error) {
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	f, err := collect.SetupFacts(readCtx, db, d.Name)
	cancel()
	if err != nil {
		f = domain.SetupFacts{Database: d.Name, State: d.State, Error: "could not read the setup facts: " + err.Error()}
	}
	version := f.ProductVersion
	if version == "" {
		if version, err = collect.EngineVersion(ctx, db); err != nil {
			version = ""
		}
	}
	if d.QueryStore == nil && d.Error == "" {
		readQueryStore(ctx, db, version, d)
		if d.QueryStore != nil {
			if err := s.store.InsertQueryStoreSnapshot(ctx, serverID, d.Name, *d.QueryStore); err != nil {
				return f, err
			}
		}
	}
	f.DB = *d
	return f, nil
}

// setupDetail reads the Setup card of one database, keeps the read, and lists the fixes available
// right now.
func (s *Service) setupDetail(ctx context.Context, db *sql.DB, serverID int64, d domain.DatabaseStatus, def domain.CardDef) (domain.CardDetail, error) {
	facts, err := s.readSetupFacts(ctx, db, serverID, &d)
	if err != nil {
		return domain.CardDetail{}, err
	}
	card, findings, checks := domain.BuildSetupCard(facts, def)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.CardDetail{}, err
	}
	return domain.CardDetail{Card: card, Findings: findings, Checks: checks, Fixes: domain.AvailableFixes(facts, findings)}, nil
}

// genericFix is everything a preview of a generic fix reads before it builds a statement.
type genericFix struct {
	db       *sql.DB
	srv      domain.Server
	database domain.DatabaseStatus
	facts    domain.SetupFacts
	upkeep   domain.UpkeepFacts // for an Upkeep fix: what it was built from
	backups  domain.BackupFacts // for a Backups fix: what it was built from
	options  map[string]string  // what the user set in the preview (the backup schedule's folder)
	preview  domain.FixPreview
	fix      domain.Fix
	// plan is what the maintenance plan's prepare keeps for its apply (docs/phase5b-contract.md): the
	// pieces prepared through their own fixes, in apply order.
	plan *planRun
	// runID is the background run this fix is being applied by, set when the run starts; a batch fix
	// reports its parts to it (docs/phase5t-contract.md).
	runID string
	// action is the run's entry in kakapo's own log (docs/phase5w-contract.md, B2): the run writes its
	// ended row.
	action *action
}

// prepareGenericFix connects, finds the database, reads the facts the fix's card needs (the setup
// facts, for an Upkeep fix the out-of-date statistics, for a Backups fix the backup facts and the
// folder check) and builds the fix with the options the user set (only the backup schedule has
// any). An unknown fix id, or one whose finding did not fire, is KindNotFound; a database that is
// not online, or (for a database fix) a secondary replica or read-only, is KindConflict (a
// read-only database can still be backed up). The caller closes g.db.
func (s *Service) prepareGenericFix(ctx context.Context, serverID int64, dbName, fixID string, options map[string]string) (*genericFix, error) {
	def, ok := domain.FindFixDef(fixID)
	if !ok {
		return nil, newError(KindNotFound, "there is no fix named %q", fixID)
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return nil, err
	}
	g := &genericFix{db: db, srv: srv, options: options}
	fail := func(err error) (*genericFix, error) {
		db.Close()
		return nil, err
	}
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return fail(newError(KindUnreachable, "%s: %v", srv.Name, err))
	}
	if d == nil {
		return fail(newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name))
	}
	switch {
	case d.State != "ONLINE":
		return fail(newError(KindConflict, "%s is %s, not online, so %s cannot be changed from here", d.Name, d.State, def.Title))
	case def.Scope == domain.ScopeDatabase && d.IsSecondaryReplica:
		return fail(newError(KindConflict, "%s is a secondary replica here: change %s on the primary", d.Name, def.Setting))
	case def.Scope == domain.ScopeDatabase && d.ReadOnly && def.Card() != domain.CardBackups:
		return fail(newError(KindConflict, "%s is read-only, so %s cannot be changed", d.Name, def.Setting))
	}
	g.database = *d
	if r, ok := registeredFixFor(fixID); ok {
		// A registered fix (docs/phase4c-contract.md, section 0) prepares itself.
		err = r.prepare(ctx, s, g, def, options)
		var se *Error
		if errors.As(err, &se) {
			return fail(err)
		}
	} else {
		switch def.Card() {
		case domain.CardUpkeep, domain.CardBackups:
			if def.Card() == domain.CardUpkeep {
				err = s.prepareUpkeepFix(ctx, g, def)
			} else {
				err = s.prepareBackupFix(ctx, g, def)
			}
			var se *Error
			if errors.As(err, &se) {
				return fail(err)
			}
		default:
			g.facts, err = s.readSetupFacts(ctx, db, serverID, d)
			if err != nil {
				return fail(err)
			}
			if g.facts.Error != "" {
				return fail(newError(KindUnreachable, "%s: %s", srv.Name, g.facts.Error))
			}
			g.preview, g.fix, err = domain.BuildFix(fixID, g.facts, d.Name, nil)
		}
	}
	switch {
	case errors.Is(err, domain.ErrFixNotApplicable):
		return fail(newError(KindNotFound, "nothing to apply on %s: %s did not find anything for %s", d.Name, def.CheckID, def.Title))
	case err != nil:
		return fail(newError(KindNotFound, "there is no fix named %q", fixID))
	}
	s.resolveRoom(ctx, serverID, db, d.Name, &g.preview)
	return g, nil
}

// PreviewFix says exactly what applying one fix would run, with its rollback and warnings, built
// with the fix's default options. It never changes anything. The query-store fix is the phase 1
// preview.
func (s *Service) PreviewFix(ctx context.Context, serverID int64, dbName, fixID string) (domain.FixPreview, error) {
	return s.PreviewFixWithOptions(ctx, serverID, dbName, fixID, nil)
}

// PreviewFixWithOptions is PreviewFix with the options the user set in the preview
// (docs/phase3g-contract.md); fixes without inputs ignore them.
func (s *Service) PreviewFixWithOptions(ctx context.Context, serverID int64, dbName, fixID string, options map[string]string) (domain.FixPreview, error) {
	if fixID == domain.FixQueryStore {
		fc, err := s.prepareQueryStoreFix(ctx, serverID, dbName)
		if err != nil {
			return domain.FixPreview{}, err
		}
		defer fc.db.Close()
		warnNoChangeLog(ctx, fc.db, fixID, &fc.preview)
		return fc.preview, nil
	}
	g, err := s.prepareGenericFix(ctx, serverID, dbName, fixID, options)
	if err != nil {
		return domain.FixPreview{}, err
	}
	defer g.db.Close()
	// docs/phase5w-contract.md, B7: while kakapo's change log is not on the server, every preview says so.
	warnNoChangeLog(ctx, g.db, fixID, &g.preview)
	return g.preview, nil
}

// prepareQueryStoreFix is the phase 1 preparation, with "nothing to apply" as KindNotFound, as the
// generic routes answer it.
func (s *Service) prepareQueryStoreFix(ctx context.Context, serverID int64, dbName string) (*fixContext, error) {
	fc, err := s.prepareFix(ctx, serverID, dbName)
	if err != nil {
		return nil, err
	}
	if fc.preview.Statement == "" {
		fc.db.Close()
		return nil, newError(KindNotFound, "nothing to apply on %s: Query Store already has the recommended settings", fc.database.Name)
	}
	s.resolveRoom(ctx, serverID, fc.db, fc.database.Name, &fc.preview)
	fc.preview.Warnings = append(fc.preview.Warnings, domain.PermissionHint(domain.ScopeDatabase, fc.database.Name))
	return fc, nil
}

// ApplyFix runs the statement the preview builds, reads the value back to prove it took, and
// writes the change log. SQL Server refusing the change is KindUnreachable, with the permission
// the fix needs. The query-store fix runs the phase 1 apply; update-statistics runs inside the
// database with a 10-minute timeout (applyUpkeepFix); the Backups card's fixes create or delete
// Agent jobs (applyBackupFix). The fix's default options are used.
func (s *Service) ApplyFix(ctx context.Context, serverID int64, dbName, fixID string) (domain.FixResult, error) {
	return s.ApplyFixWithOptions(ctx, serverID, dbName, fixID, nil)
}

// ApplyFixWithOptions is ApplyFix with the options the preview was built with
// (docs/phase3g-contract.md); fixes without inputs ignore them. A long fix whose preview can be
// applied (docs/phase4f-contract.md) returns at once with Pending and its RunID: its apply runs in
// the background and FixRunStatus has the outcome.
func (s *Service) ApplyFixWithOptions(ctx context.Context, serverID int64, dbName, fixID string, options map[string]string) (domain.FixResult, error) {
	if fixID == domain.FixQueryStore {
		fc, err := s.prepareQueryStoreFix(ctx, serverID, dbName)
		if err != nil {
			return domain.FixResult{}, err
		}
		preview := fc.preview
		fc.db.Close()
		res, err := s.ApplyQueryStoreFix(ctx, serverID, dbName)
		if err != nil {
			return domain.FixResult{}, err
		}
		return domain.FixResult{Preview: preview, Before: res.Before.Summary, After: res.After.Summary,
			Verified: res.After.Verdict == domain.VerdictOK, ChangeID: res.ChangeID, AppliedAt: res.AppliedAt}, nil
	}

	g, err := s.prepareGenericFix(ctx, serverID, dbName, fixID, options)
	if err != nil {
		return domain.FixResult{}, err
	}
	if err := roomHeld(g.preview); err != nil {
		g.db.Close()
		return domain.FixResult{}, err
	}
	if g.preview.Held != "" {
		// docs/phase5m-contract.md, section 4: something the preview names holds Apply back.
		g.db.Close()
		return domain.FixResult{}, newError(KindConflict, "%s", g.preview.Held)
	}
	r, registered := registeredFixFor(fixID)
	// kakapo's own log (docs/phase5w-contract.md, B2): the started row, after the preview checks and before
	// dispatch; nothing runs when it cannot be written.
	def, _ := domain.FindFixDef(fixID)
	ctx, act, err := s.startAction(ctx, g.db, genericActionInfo(serverID, def, g))
	if err != nil {
		g.db.Close()
		return domain.FixResult{}, err
	}
	if registered && runsInBackground(r, g) {
		// A long fix (docs/phase4f-contract.md) answers at once with a pending run; the run owns g, and
		// writes the ended row when it ends, is stopped or is recovered.
		g.action = act
		res, err := s.startRun(ctx, serverID, fixID, g, r.apply, *r.long)
		if err != nil {
			s.endAction(ctx, nil, act, domain.FixResult{}, err, false)
		}
		return res, err
	}
	defer g.db.Close()
	res, err := s.applyPrepared(ctx, serverID, fixID, g, r, registered)
	s.endAction(ctx, g.db, act, res, err, false)
	return res, err
}

// applyPrepared runs a prepared fix that is not a background run: a registered fix's own apply, the Upkeep
// and Backups cards' applies, or a Setup setting.
func (s *Service) applyPrepared(ctx context.Context, serverID int64, fixID string, g *genericFix, r registeredFix, registered bool) (domain.FixResult, error) {
	if registered {
		return r.apply(ctx, s, serverID, g)
	}
	switch def, _ := domain.FindFixDef(fixID); def.Card() {
	case domain.CardUpkeep:
		return s.applyUpkeepFix(ctx, serverID, g)
	case domain.CardBackups:
		return s.applyBackupFix(ctx, serverID, g)
	}
	name, fix := g.database.Name, g.fix
	on := name
	if fix.Scope == domain.ScopeServer {
		on = g.facts.ServerName
	}

	before, err := collect.ReadSetting(ctx, g.db, fixID, name)
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "could not read %s on %s before the change: %v", fix.Setting, on, err)
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the change to %s on %s: %v. %s",
			fix.Setting, on, err, domain.PermissionHint(fix.Scope, name))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)

	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      fix.Setting,
		ChangeType:  "SETTING",
		Description: domain.FixChangeDescription(fix, g.facts.ServerName, name, g.facts.PendingOtherThan(fix.Setting)...),
		Statement:   g.preview.Statement,
		Rollback:    g.preview.Rollback,
	}
	after, err := collect.ReadSetting(ctx, g.db, fixID, name)
	if err != nil {
		// The statement ran; log it even though we could not prove it took.
		change.Result = "applied, but the value could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the change to %s on %s ran, but the value could not be read back: %v", fix.Setting, on, err)
	}
	verified := after == fix.Target
	change.Result = after + ", differs"
	if verified {
		change.Result = after + ", verified"
	}
	id, err := s.insertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: g.preview, Before: before, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}
