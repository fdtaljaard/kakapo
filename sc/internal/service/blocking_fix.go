package service

import (
	"context"
	"errors"
	"maps"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The blocking page's fixes (docs/phase4c-contract.md, part 2), through the fix registry: kill-session
// ends one session (option sessionId) and rcsi turns READ COMMITTED SNAPSHOT on for the database.

func init() {
	registerFix(domain.FixKillSession, prepareKillSession, applyKillSession)
	registerFix(domain.FixRCSI, prepareRCSI, applyRCSI)
}

// killDescriptionKey keeps the change log line, built from the session read at prepare, in g.options
// for the apply. g.options never goes back to the client; the preview's options do.
const killDescriptionKey = "kakapo.killDescription"

// killReadBackTries and killReadBackPause: a killed session takes a moment to go, so the read back
// looks a few times before it says the session is still there.
const (
	killReadBackTries = 6
	killReadBackPause = 500 * time.Millisecond
)

// prepareKillSession reads the session again and builds the kill preview.
func prepareKillSession(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	n, err := domain.ParseSessionID(options["sessionId"])
	if err != nil {
		return newError(KindInvalid, "%v", err)
	}
	t, err := collect.ReadKillTarget(ctx, g.db, n)
	if err != nil {
		return newError(KindUnreachable, "could not read session %d on %s: %v", n, g.srv.Name, err)
	}
	if t.ServerName == "" {
		t.ServerName = g.srv.Name
	}
	return buildKillFix(g, t, options)
}

// buildKillFix builds the kill preview from the session read: a session that has gone is
// KindNotFound; one that is a new connection with the previewed id (its login time differs from the
// one the preview echoed) is KindConflict, so Apply never kills someone else.
func buildKillFix(g *genericFix, t domain.KillTarget, options map[string]string) error {
	n := t.Session.Session
	if !t.Found {
		n, _ = domain.ParseSessionID(options["sessionId"])
	}
	p, fix, err := domain.BuildKillSessionFix(t)
	if errors.Is(err, domain.ErrSessionGone) {
		return newError(KindNotFound, "session %d is no longer connected to %s: there is nothing to kill", n, t.ServerName)
	}
	if err != nil {
		return newError(KindNotFound, "%v", err)
	}
	if domain.KillLoginChanged(options, t.Session.LoginTime) {
		return newError(KindConflict, "session %d on %s is a different connection now (it logged in after the preview): close this and preview it again", n, t.ServerName)
	}
	g.preview, g.fix = p, fix
	g.options = maps.Clone(p.Options)
	g.options[killDescriptionKey] = domain.KillChangeDescription(t)
	return nil
}

// killApplyRefusal is why the previewed kill cannot run: the preview has no change (a system
// session, kakapo's own, one already rolling back). nil when it can.
func killApplyRefusal(g *genericFix) error {
	if len(g.preview.Changes) > 0 && g.preview.Statement != "" {
		return nil
	}
	msg := "this session cannot be killed from kakapo"
	if len(g.preview.Warnings) > 0 {
		msg = g.preview.Warnings[0]
	}
	return newError(KindConflict, "%s", msg)
}

// applyKillSession runs KILL, reads the session back (gone, or rolling back with KILL ... WITH
// STATUSONLY), and writes the change row; there is no rollback statement.
func applyKillSession(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := killApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	n, err := domain.ParseSessionID(g.preview.Options["sessionId"])
	if err != nil {
		return domain.FixResult{}, newError(KindInvalid, "%v", err)
	}
	loginTime := g.preview.Options["loginTime"]
	server := g.srv.Name
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to kill session %d on %s: %v. %s", n, server, err, domain.KillPermission)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   appliedAt,
		Object:      g.fix.Setting, // "Session 83"
		ChangeType:  "KILL",
		Description: g.options[killDescriptionKey],
		Statement:   g.preview.Statement,
	}
	rb, err := readKillBack(ctx, g, n, loginTime)
	if err != nil {
		change.Result = "applied, but the session could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "session %d on %s was killed, but could not be read back: %v", n, server, err)
	}
	after, verified := domain.KillResult(n, loginTime, rb)
	change.Result = after + " Differs from what was asked."
	if verified {
		change.Result = after + " Verified."
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: g.preview, Before: g.fix.Current, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// readKillBack reads the session after KILL until it has gone or is rolling back, a few times.
func readKillBack(ctx context.Context, g *genericFix, n int, loginTime string) (domain.KillReadBack, error) {
	var rb domain.KillReadBack
	for i := 0; i < killReadBackTries; i++ {
		var err error
		if rb, err = collect.ReadKillStatus(ctx, g.db, n); err != nil {
			return rb, err
		}
		if _, done := domain.KillResult(n, loginTime, rb); done {
			return rb, nil
		}
		select {
		case <-ctx.Done():
			return rb, ctx.Err()
		case <-time.After(killReadBackPause):
		}
	}
	return rb, nil
}

// rcsiVerdictKey keeps the row versioning verdict the preview was built on in g.options, for the
// decision record the apply writes. g.options never goes back to the client; the preview's options do.
const rcsiVerdictKey = "kakapo.rcsiVerdict"

// prepareRCSI reads the row versioning facts (docs/phase5m-contract.md, section 5) and builds the preview
// with the row versioning decision's evidence and cost lines, from the stored reports.
func prepareRCSI(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	name := g.database.Name
	f, err := collect.ReadRowVersioning(ctx, g.db, name)
	if err == nil && f.SettingsError != "" {
		err = errors.New(f.SettingsError)
	}
	if err != nil {
		return newError(KindUnreachable, "could not read READ_COMMITTED_SNAPSHOT of %s on %s: %v", name, g.srv.Name, err)
	}
	in, err := s.storedRowVersioningInputs(ctx, g.srv.ID, name)
	if err != nil {
		return err
	}
	in.Facts = &f
	dec, err := s.rowVersioningDecision(ctx, g.srv.ID, in)
	if err != nil {
		return err
	}
	if err := buildRCSIFix(g, domain.RCSIFactsOf(f), &dec); err != nil {
		return err
	}
	g.options = maps.Clone(g.options)
	if g.options == nil {
		g.options = map[string]string{}
	}
	g.options[rcsiVerdictKey] = dec.Verdict
	return nil
}

// buildRCSIFix builds the preview from the setting read and the decision; already on is KindNotFound.
func buildRCSIFix(g *genericFix, f domain.RCSIFacts, d *domain.Decision) error {
	p, fix, err := domain.BuildRCSIFix(f, d)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "READ COMMITTED SNAPSHOT is already on for %s: nothing to apply", f.Database)
	}
	if err != nil {
		return newError(KindNotFound, "%v", err)
	}
	g.preview, g.fix = p, fix
	return nil
}

// applyRCSI runs the ALTER DATABASE, reads is_read_committed_snapshot_on back, writes the change row
// with the OFF statement as its rollback, and the decision record "Turned on" with the change's id
// (docs/phase5m-contract.md, section 4).
func applyRCSI(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, fix, p := g.database.Name, g.fix, g.preview
	if err := collect.ExecFix(ctx, g.db, p.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to turn READ COMMITTED SNAPSHOT on for %s: %v. %s",
			name, err, domain.PermissionHint(domain.ScopeDatabase, name))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      fix.Setting,
		ChangeType:  "SETTING",
		Description: domain.FixChangeDescription(fix, g.srv.Name, name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	f, err := collect.ReadRCSI(ctx, g.db, name)
	if err != nil {
		change.Result = "applied, but the value could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "READ COMMITTED SNAPSHOT was changed on %s, but could not be read back: %v", name, err)
	}
	after := domain.RCSIValue(f.RCSI)
	change.Result = after + ", differs"
	if f.RCSI {
		change.Result = after + ", verified"
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	verdict := g.options[rcsiVerdictKey]
	if verdict == "" {
		verdict = domain.DecisionNotEnoughEvidence
	}
	if _, err := s.store.InsertDecision(ctx, domain.DecisionRecord{ServerID: serverID, Database: name, Decision: domain.DecisionRowVersioning,
		DecidedAt: appliedAt, DecidedBy: windowsUser(), Choice: domain.ChoiceChange, Verdict: verdict, ChangeID: &id}); err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: fix.Current, After: after, Verified: f.RCSI, ChangeID: id, AppliedAt: appliedAt}, nil
}
