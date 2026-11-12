package service

import (
	"context"
	"errors"
	"math"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The query page's fixes (docs/phase4c-contract.md, part 1), registered with the fix registry:
// force-plan and unforce-plan (Query Store, inside the database), evict-plan (one plan out of the
// plan cache, on the server) and update-statistics-table (one table, inside the database). Each
// prepare reads what the preview is built from; each apply runs the previewed statement through
// collect.ExecFix, reads back, and writes the change row.

func init() {
	registerFix(domain.FixForcePlan, preparePlanForceFix, applyPlanForceFix)
	registerFix(domain.FixUnforcePlan, preparePlanForceFix, applyPlanForceFix)
	registerFix(domain.FixEvictPlan, prepareEvictPlanFix, applyEvictPlanFix)
	// A long fix (docs/phase4f-contract.md): it runs in the background, bounded by the longest
	// update (30 minutes over 10 million rows).
	registerLongFix(domain.FixUpdateStatisticsTable, prepareTableStatsFix, applyTableStatsFix,
		longFix{timeout: domain.TableStatsTimeout(math.MaxInt64) + runReadMargin, completed: tableStatsCompleted})
}

// qFixError turns a domain refusal into the service's kinds: wrong options are KindInvalid,
// nothing to apply KindNotFound.
func qFixError(err error) error {
	var qe *domain.QueryFixError
	if errors.As(err, &qe) {
		if qe.Invalid {
			return newError(KindInvalid, "%s", qe.Reason)
		}
		return newError(KindNotFound, "nothing to apply: %s", qe.Reason)
	}
	return err
}

// qWithOptions echoes the options a preview was built with, so apply sends the same.
func qWithOptions(p domain.FixPreview, fix domain.Fix) domain.FixPreview {
	if len(fix.Options) > 0 {
		p.Options = map[string]string{}
		for k, v := range fix.Options {
			p.Options[k] = v
		}
	}
	return p
}

// preparePlanForceFix reads the plan's forcing in the database's Query Store and builds the
// force-plan or unforce-plan preview.
func preparePlanForceFix(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	planID, err := domain.ParsePlanID(options["planId"])
	if err != nil {
		return qFixError(err)
	}
	f, err := collect.ReadPlanForcing(ctx, g.db, g.database.Name, planID)
	if err != nil {
		return newError(KindUnreachable, "could not read Query Store in %s on %s: %v", g.database.Name, g.srv.Name, err)
	}
	f.ProductVersion, _ = collect.EngineVersion(ctx, g.db)
	g.preview, g.fix, err = domain.BuildPlanForceFix(def.ID, f)
	if err != nil {
		return qFixError(err)
	}
	if hash := options["queryHash"]; hash != "" && f.QueryHash != "" && hash != f.QueryHash {
		g.preview.Warnings = append(g.preview.Warnings, "This plan belongs to query "+f.QueryHash+", not to "+hash+".")
	}
	g.preview = qWithOptions(g.preview, g.fix)
	return nil
}

func applyPlanForceFix(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, fix := g.database.Name, g.fix
	planID, err := domain.ParsePlanID(fix.Options["planId"])
	if err != nil {
		return domain.FixResult{}, qFixError(err)
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to change the forcing of plan %d in %s: %v. Needs ALTER on the database %s.",
			planID, name, err, name)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID: serverID, Database: name, ChangedAt: appliedAt,
		Object: "Query Store plan", ChangeType: "PLAN",
		Statement: g.preview.Statement, Rollback: g.preview.Rollback,
	}
	after, err := collect.ReadPlanForcing(ctx, g.db, name, planID)
	change.Description = domain.PlanForceDescription(fix.ID, after, fix)
	if err != nil {
		change.Result = "applied, but the plan could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the forcing of plan %d in %s changed, but could not be read back: %v", planID, name, err)
	}
	text, ok := domain.PlanForceReadBack(fix.ID, after)
	return s.logQueryFix(ctx, change, g, fix.Current, appliedAt, text, ok)
}

// logQueryFix writes the change row with the read-back and returns the result.
func (s *Service) logQueryFix(ctx context.Context, change domain.Change, g *genericFix, before string, appliedAt time.Time, after string, verified bool) (domain.FixResult, error) {
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

// prepareEvictPlanFix reads the plan handle in the plan cache and builds the evict-plan preview.
func prepareEvictPlanFix(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	handle, raw, err := domain.ParsePlanHandle(options["planHandle"])
	if err != nil {
		return qFixError(err)
	}
	f, err := collect.ReadCachedPlan(ctx, g.db, handle, raw)
	if err != nil {
		return newError(KindUnreachable, "could not read the plan cache on %s: %v (reading it needs VIEW SERVER STATE)", g.srv.Name, err)
	}
	version, _ := collect.EngineVersion(ctx, g.db)
	g.preview, g.fix, err = domain.BuildEvictPlanFix(f, g.database.Name, version)
	if err != nil {
		return qFixError(err)
	}
	if hash := options["queryHash"]; hash != "" {
		if norm, _, err := domain.ParseQueryHash(hash); err == nil {
			g.fix.Options["queryHash"] = norm
		}
	}
	g.preview = qWithOptions(g.preview, g.fix)
	return nil
}

func applyEvictPlanFix(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	fix := g.fix
	handle, raw, err := domain.ParsePlanHandle(fix.Options["planHandle"])
	if err != nil {
		return domain.FixResult{}, qFixError(err)
	}
	before, err := collect.ReadCachedPlan(ctx, g.db, handle, raw)
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "could not read the plan cache on %s: %v", g.srv.Name, err)
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to remove the plan on %s: %v. Needs the ALTER SERVER STATE permission.", g.srv.Name, err)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID: serverID, Database: g.database.Name, ChangedAt: appliedAt,
		Object: "Plan cache", ChangeType: "PLAN",
		Description: domain.EvictPlanDescription(before, fix.Options["queryHash"]),
		Statement:   g.preview.Statement,
	}
	after, err := collect.ReadCachedPlan(ctx, g.db, handle, raw)
	if err != nil {
		change.Result = "applied, but the plan cache could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the plan was removed on %s, but the plan cache could not be read back: %v", g.srv.Name, err)
	}
	text, ok := domain.EvictPlanReadBack(after)
	return s.logQueryFix(ctx, change, g, fix.Current, appliedAt, text, ok)
}

// prepareTableStatsFix reads the table's rows and statistics inside the database and builds the
// update-statistics-table preview.
func prepareTableStatsFix(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	schema, table, err := domain.ParseTableOption(options["table"])
	if err != nil {
		return qFixError(err)
	}
	f, err := collect.ReadTableStats(ctx, g.db, g.database.Name, schema, table)
	if err != nil {
		return newError(KindUnreachable, "could not read the statistics of %s.%s in %s: %v", schema, table, g.database.Name, err)
	}
	f.ProductVersion, _ = collect.EngineVersion(ctx, g.db)
	g.preview, g.fix, err = domain.BuildTableStatsFix(f)
	if err != nil {
		return qFixError(err)
	}
	g.preview = qWithOptions(g.preview, g.fix)
	return nil
}

// applyTableStatsFix runs the update inside the database, as the Upkeep fix does, with up to 10
// minutes (30 over 10 million rows), reads the statistics back and writes the change row.
func applyTableStatsFix(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, fix := g.database.Name, g.fix
	schema, table, err := domain.ParseTableOption(fix.Options["table"])
	if err != nil {
		return domain.FixResult{}, qFixError(err)
	}
	before, err := collect.ReadTableStats(ctx, g.db, name, schema, table)
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "could not read the statistics of %s.%s in %s: %v", schema, table, name, err)
	}
	applyCtx, cancel := context.WithTimeout(ctx, domain.TableStatsTimeout(before.Rows))
	err = collect.ExecFixInDatabase(applyCtx, g.db, name, g.preview.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the statistics update on %s.%s in %s: %v. %s",
			schema, table, name, err, domain.UpdateStatisticsPermission)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID: serverID, Database: name, ChangedAt: appliedAt,
		Object: "Statistics on " + schema + "." + table, ChangeType: "MAINTENANCE",
		Description: domain.TableStatsDescription(before, g.preview.Statement),
		Statement:   g.preview.Statement,
	}
	after, err := collect.ReadTableStats(ctx, g.db, name, schema, table)
	if err != nil {
		change.Result = "applied, but the statistics could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the statistics update on %s.%s ran, but could not be read back: %v", schema, table, err)
	}
	text, ok := domain.TableStatsReadBack(after, before.ServerNow)
	return s.logQueryFix(ctx, change, g, fix.Current, appliedAt, text, ok)
}

// tableStatsCompleted reads an update-statistics-table run back after its connection was lost
// (docs/phase4f-contract.md): done when every statistic of the table was updated after the run
// started (stats_date, on the server's clock).
func tableStatsCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	name, fix := g.database.Name, g.fix
	schema, table, err := domain.ParseTableOption(fix.Options["table"])
	if err != nil {
		return recovered{}, false, qFixError(err)
	}
	after, err := collect.ReadTableStats(ctx, g.db, name, schema, table)
	if err != nil {
		return recovered{}, false, err
	}
	text, ok := domain.TableStatsReadBack(after, serverStart(startedAt, after.ServerNow, time.Now()))
	if !ok {
		return recovered{after: "the statistics of " + schema + "." + table + " were not all updated after the run started (" + text + "): the update did not finish; apply it again"}, false, nil
	}
	return recovered{after: text, verified: true, change: domain.Change{
		Object: "Statistics on " + schema + "." + table, ChangeType: "MAINTENANCE",
		Description: domain.TableStatsDescriptionOf(schema+"."+table, fix.Current, g.preview.Statement), Statement: g.preview.Statement}}, true, nil
}
