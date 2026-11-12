package service

import (
	"context"
	"errors"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The change log fills itself from the server (docs/phase5f-contract.md): on the database page's load,
// after the Upkeep read, the default trace's DDL events of every user database become change rows
// (harvestSchemaChanges), and CHG01's fix enable-default-trace switches the trace back on.

func init() {
	registerFix(domain.FixEnableDefaultTrace, prepareEnableDefaultTrace, applyEnableDefaultTrace)
}

// harvestSchemaChanges keeps what the Upkeep read found in the default trace: every hour the trace
// still holds after the hour of its oldest event is recomputed and replaces the changes seen on the
// server in those hours, for every database, in one transaction; the newest event seen is kept for
// the next harvest's gap check. It returns the window line under Recent changes. Nothing is written
// when the trace could not be read or is off.
func (s *Service) harvestSchemaChanges(ctx context.Context, serverID int64, f domain.UpkeepFacts) (*domain.ChangeWindow, error) {
	key := domain.SchemaChangesSeenKey(serverID)
	var previous *time.Time
	if v, err := s.store.Setting(ctx, key); err != nil {
		return nil, err
	} else if t, err := time.Parse(time.RFC3339Nano, v); v != "" && err == nil {
		t = t.UTC()
		previous = &t
	}
	window := domain.BuildChangeWindow(f, previous)
	if f.Error != "" || f.TraceError != "" || !f.TraceRunning || f.TraceOldestUTC == nil {
		return window, nil
	}
	since := domain.SeenWindowStart(*f.TraceOldestUTC)
	// The DDL (5f) and the configuration changes (docs/phase5w-contract.md, A6) of the same hours, together.
	fresh := append(domain.BuildSeenChanges(serverID, f.SchemaChanges, since), domain.BuildSeenSettingChanges(serverID, f.SettingChanges, since)...)
	if _, _, err := s.store.ReplaceSeenChanges(ctx, serverID, since, fresh); err != nil {
		return nil, err
	}
	if f.TraceNewestUTC != nil {
		if err := s.store.SetSetting(ctx, key, f.TraceNewestUTC.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	return window, nil
}

// --- enable-default-trace (CHG01) ---

func prepareEnableDefaultTrace(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadDefaultTrace(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read the default trace of %s: %v", g.srv.Name, err)
	}
	cfg, err := collect.ReadServerConfig(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read the server configuration of %s: %v", g.srv.Name, err)
	}
	f.Pending = cfg.PendingConfigs()
	return enableDefaultTracePreview(g, f)
}

func enableDefaultTracePreview(g *genericFix, f domain.DefaultTraceFacts) error {
	if f.ServerName == "" {
		f.ServerName = g.srv.Name
	}
	p, fix, err := domain.BuildEnableDefaultTraceFix(f)
	if errors.Is(err, domain.ErrFixNotApplicable) {
		return newError(KindNotFound, "SQL Server's default trace is already on and running on %s: nothing to apply", f.ServerName)
	}
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.EnableDefaultTraceDescription(fix, f))
	return suPreviewed(g, p, fix)
}

func applyEnableDefaultTrace(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to switch the default trace on on %s: %v. %s",
			g.srv.Name, err, domain.PermissionHint(domain.ScopeServer, ""))
	}
	change := suChange(serverID, g, g.fix.Setting, "SETTING", g.options[suDescriptionKey])
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadDefaultTrace(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The change to the default trace")
	}
	after, ok := domain.DefaultTraceReadBack(f)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}
