package service

import (
	"context"
	"strconv"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The Setup card's actions (docs/phase4d-contract.md, part 1), registered with the fix registry:
// multi-user, reconfigure, trace-flag-off, scoped-config-reset and min-server-memory. Each prepare
// reads what its preview is built from (collect/setup_actions.go) and builds it in domain; each
// apply runs the previewed statement through collect.ExecFix, reads back and writes the change
// row with the rollback.

func init() {
	registerFix(domain.FixMultiUser, prepareMultiUser, applyMultiUser)
	registerFix(domain.FixReconfigure, prepareReconfigure, applyReconfigure)
	registerFix(domain.FixTraceFlagOff, prepareTraceFlagOff, applyTraceFlagOff)
	registerFix(domain.FixScopedConfigReset, prepareScopedConfigReset, applyScopedConfigReset)
	registerFix(domain.FixMinServerMemory, prepareMinServerMemory, applyMinServerMemory)
}

// suReadTimeout bounds every read of an action's prepare and read-back.
const suReadTimeout = 60 * time.Second

// Keys of what a prepare keeps in g.options for its apply. g.options never goes back to the
// client; the preview's options do.
const (
	suDescriptionKey = "kakapo.description"
	suJobsKey        = "kakapo.jobs"
	suStepNameKey    = "kakapo.stepName"
	suStepCommandKey = "kakapo.stepCommand"
)

// suKeep stores a value in g.options for the apply.
func suKeep(g *genericFix, key, value string) {
	if g.options == nil {
		g.options = map[string]string{}
	} else if _, mine := g.options[key]; !mine {
		clone := make(map[string]string, len(g.options)+1)
		for k, v := range g.options {
			clone[k] = v
		}
		g.options = clone
	}
	g.options[key] = value
}

// suPreviewed keeps the preview and the fix in g. A preview with no changes and no inputs
// cannot be applied, and the dialog would not show why (it shows its warnings only next to inputs),
// so it is KindConflict with its first warning; one with inputs is returned for the user to fix the
// value.
func suPreviewed(g *genericFix, p domain.FixPreview, fix domain.Fix) error {
	g.preview, g.fix = p, fix
	if len(p.Changes) == 0 && len(p.Inputs) == 0 {
		msg := fix.Title + " cannot be applied now"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return newError(KindConflict, "%s", msg)
	}
	return nil
}

// suApplyRefusal is why a previewed action cannot run: no changes (a wrong option, a blocking
// warning) or an Express script. nil when it can.
func suApplyRefusal(g *genericFix) error {
	p := g.preview
	switch {
	case p.Express:
		return newError(KindConflict, "SQL Server Express has no Agent, so kakapo cannot create the job on %s: copy the script from the preview and schedule it in Windows Task Scheduler", g.srv.Name)
	case len(p.Changes) > 0 && p.Statement != "":
		return nil
	}
	msg := g.fix.Title + " cannot be applied as previewed"
	switch {
	case len(p.Warnings) > 0:
		msg = p.Warnings[0]
	case p.Note != "":
		msg = p.Note
	}
	return newError(KindConflict, "%s", msg)
}

// suServerLabel is @@SERVERNAME as read, else the server's name in kakapo.
func suServerLabel(g *genericFix, serverName string) string {
	if serverName != "" {
		return serverName
	}
	return g.srv.Name
}

// suChange is the change row of an applied action, before its result.
func suChange(serverID int64, g *genericFix, object, changeType, description string) domain.Change {
	return domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   time.Now().UTC().Truncate(time.Microsecond),
		Object:      object,
		ChangeType:  changeType,
		Description: description,
		Statement:   g.preview.Statement,
		Rollback:    g.preview.Rollback,
	}
}

// suLogUnread writes the change row of a statement that ran but could not be read back, and
// returns the error the caller answers with.
func (s *Service) suLogUnread(ctx context.Context, change domain.Change, readErr error, what string) error {
	change.Result = "applied, but it could not be read back: " + readErr.Error()
	if _, err := s.store.InsertChange(ctx, change); err != nil {
		return err
	}
	return newError(KindUnreachable, "%s ran, but could not be read back: %v", what, readErr)
}

// suLog writes the change row with what was read back and returns the result.
func (s *Service) suLog(ctx context.Context, change domain.Change, g *genericFix, before, after string, verified bool) (domain.FixResult, error) {
	change.Result = after + ", differs"
	if verified {
		change.Result = after + ", verified"
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: g.preview, Before: before, After: after, Verified: verified, ChangeID: id, AppliedAt: change.ChangedAt}, nil
}

func suReadCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, suReadTimeout)
}

// --- multi-user ---

func prepareMultiUser(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadUserAccess(rctx, g.db, g.database.Name)
	if err != nil {
		return newError(KindUnreachable, "could not read the user access of %s on %s: %v", g.database.Name, g.srv.Name, err)
	}
	return multiUserPreview(g, f)
}

func multiUserPreview(g *genericFix, f domain.UserAccessFacts) error {
	p, fix, err := domain.BuildMultiUserFix(f)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.FixChangeDescription(fix, suServerLabel(g, f.ServerName), f.Database))
	return suPreviewed(g, p, fix)
}

func applyMultiUser(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	name := g.database.Name
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to open %s to everyone: %v. %s", name, err, domain.PermissionHint(domain.ScopeDatabase, name))
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadUserAccess(rctx, g.db, name)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "MULTI_USER on "+name)
	}
	after, ok := domain.MultiUserReadBack(f.UserAccess)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- reconfigure ---

func prepareReconfigure(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadServerConfig(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read the server configuration of %s: %v", g.srv.Name, err)
	}
	return reconfigurePreview(g, f, options)
}

func reconfigurePreview(g *genericFix, f domain.ServerConfigFacts, options map[string]string) error {
	p, fix, err := domain.BuildReconfigureFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.ReconfigureDescription(p.Options[domain.OptionReconfigureAction], suServerLabel(g, f.ServerName), p.Changes))
	return suPreviewed(g, p, fix)
}

func applyReconfigure(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the change on %s: %v. %s", g.srv.Name, err, domain.PermissionHint(domain.ScopeServer, ""))
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadServerConfig(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The change to the pending settings")
	}
	names := make([]string, 0, len(g.preview.Changes))
	for _, c := range g.preview.Changes {
		names = append(names, c.Setting)
	}
	after, ok := domain.ReconfigureReadBack(g.preview.Options[domain.OptionReconfigureAction], names, f)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- trace-flag-off ---

func prepareTraceFlagOff(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadTraceFlags(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	return traceFlagPreview(g, f, options)
}

func traceFlagPreview(g *genericFix, f domain.TraceFlagFacts, options map[string]string) error {
	p, fix, err := domain.BuildTraceFlagOffFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.FixChangeDescription(fix, suServerLabel(g, f.ServerName), ""))
	return suPreviewed(g, p, fix)
}

func applyTraceFlagOff(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	n, err := strconv.Atoi(g.preview.Options[domain.OptionTraceFlag])
	if err != nil {
		return domain.FixResult{}, newError(KindInvalid, "the trace flag must be a number, not %q", g.preview.Options[domain.OptionTraceFlag])
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to turn trace flag %d off on %s: %v. Needs sysadmin.", n, g.srv.Name, err)
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadTraceFlags(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "DBCC TRACEOFF")
	}
	after, ok := domain.TraceFlagOffReadBack(n, f.Flags)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- scoped-config-reset ---

func prepareScopedConfigReset(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadScopedConfigs(rctx, g.db, g.database.Name)
	if err != nil {
		return newError(KindUnreachable, "could not read the database scoped configurations of %s: %v", g.database.Name, err)
	}
	return scopedConfigPreview(g, f, options)
}

func scopedConfigPreview(g *genericFix, f domain.ScopedConfigFacts, options map[string]string) error {
	p, fix, err := domain.BuildScopedConfigResetFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.FixChangeDescription(fix, suServerLabel(g, f.ServerName), f.Database))
	return suPreviewed(g, p, fix)
}

func applyScopedConfigReset(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	name := g.database.Name
	if err := collect.ExecFixInDatabase(ctx, g.db, name, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to change %s in %s: %v. Needs the ALTER ANY DATABASE SCOPED CONFIGURATION permission.",
			g.fix.Setting, name, err)
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadScopedConfigs(rctx, g.db, name)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The change to "+g.fix.Setting)
	}
	after, ok := domain.ScopedConfigReadBack(g.fix.Setting, f)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- min-server-memory ---

func prepareMinServerMemory(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadServerConfig(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read the server configuration of %s: %v", g.srv.Name, err)
	}
	return minMemoryPreview(g, f)
}

func minMemoryPreview(g *genericFix, f domain.ServerConfigFacts) error {
	p, fix, err := domain.BuildMinServerMemoryFix(f)
	if err != nil {
		return err
	}
	if f.ServerName == "" {
		f.ServerName = g.srv.Name
	}
	suKeep(g, suDescriptionKey, domain.MinServerMemoryDescription(fix, f))
	return suPreviewed(g, p, fix)
}

func applyMinServerMemory(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the change to min server memory on %s: %v. %s", g.srv.Name, err, domain.PermissionHint(domain.ScopeServer, ""))
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	v, err := collect.ReadConfiguration(rctx, g.db, g.fix.Setting)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The change to min server memory")
	}
	after, ok := domain.MinServerMemoryReadBack(v)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}
