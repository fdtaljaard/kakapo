package service

import (
	"context"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// install-ola-index (docs/phase5u-contract.md, sections 2 and 3): the prepare reads Ola Hallengren's
// objects in master and builds the preview in domain; the apply runs the previewed batches through
// collect.ExecFixBatches, reads back the versions and writes the change row (type HELPER) with the
// rollback.

func init() {
	registerFix(domain.FixInstallOlaIndex, prepareInstallOlaIndex, applyInstallOlaIndex)
}

func prepareInstallOlaIndex(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, _ map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadOlaIndex(rctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "could not read Ola Hallengren's procedures in master on %s: %v", g.srv.Name, err)
	}
	p, fix, err := domain.BuildInstallOlaIndexFix(f)
	if err != nil {
		return err
	}
	p.Database = g.database.Name
	suKeep(g, suDescriptionKey, domain.OlaInstallDescription(suServerLabel(g, f.ServerName)))
	return suPreviewed(g, p, fix)
}

func applyInstallOlaIndex(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	change := suChange(serverID, g, "IndexOptimize", domain.ChangeTypeHelper, g.options[suDescriptionKey])
	if err := collect.ExecFixBatches(ctx, g.db, g.preview.Statement); err != nil {
		// A batch that failed may leave the earlier ones done: read back what is there and log it.
		rctx, cancel := suReadCtx(ctx)
		defer cancel()
		if after, rerr := collect.ReadOlaIndex(rctx, g.db); rerr == nil && (after.CommandExecute || after.IndexOptimize) {
			text, _ := domain.OlaInstallReadBack(after)
			_, _ = s.suLog(ctx, change, g, g.fix.Current, text, false)
		}
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to install Ola Hallengren's IndexOptimize on %s: %v. %s",
			g.srv.Name, err, maintHint)
	}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	after, err := collect.ReadOlaIndex(rctx, g.db)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "Ola Hallengren's IndexOptimize was installed; it")
	}
	text, ok := domain.OlaInstallReadBack(after)
	return s.suLog(ctx, change, g, g.fix.Current, text, ok)
}
