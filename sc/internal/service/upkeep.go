package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// upkeepApplyTimeout is how long the statistics fix may run: on large tables an update of
// statistics takes minutes (docs/phase3d-contract.md).
const upkeepApplyTimeout = 10 * time.Minute

// update-statistics is a long fix (docs/phase4f-contract.md): it joins the fix registry so it runs in
// the background, with its preparation and apply unchanged.
func init() {
	registerLongFix(domain.FixUpdateStatistics,
		func(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, _ map[string]string) error {
			return s.prepareUpkeepFix(ctx, g, def)
		},
		func(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
			return s.applyUpkeepFix(ctx, serverID, g)
		},
		longFix{timeout: upkeepApplyTimeout + runReadMargin, completed: statisticsCompleted})
}

// readUpkeepFacts reads what the Upkeep card needs (docs/phase3d-contract.md). When nothing can be
// read, the facts say why, so the card turns grey instead of failing the page.
func readUpkeepFacts(ctx context.Context, db *sql.DB, d domain.DatabaseStatus) domain.UpkeepFacts {
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	f, err := collect.UpkeepFacts(readCtx, db, d.Name)
	if err != nil {
		f = domain.UpkeepFacts{Database: d.Name, State: d.State, Error: "could not read the upkeep facts: " + err.Error()}
	}
	f.CanChange = d.CanFix()
	return f
}

// upkeepDetail reads the Upkeep card of one database, keeps the read, and lists the fixes
// available right now.
func (s *Service) upkeepDetail(ctx context.Context, db *sql.DB, serverID int64, d domain.DatabaseStatus, def domain.CardDef) (domain.CardDetail, error) {
	facts := readUpkeepFacts(ctx, db, d)
	card, findings, checks := domain.BuildUpkeepCard(facts, def)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.CardDetail{}, err
	}
	// The Maintenance plan row (docs/phase5b-contract.md), with the operator and alerts state of the
	// facts just read.
	if facts.Error == "" && facts.State == "ONLINE" {
		plan := readUpkeepPlan(ctx, db, facts)
		facts.MaintenancePlan = &plan
	}
	return domain.CardDetail{Card: card, Findings: findings, Checks: checks, Fixes: domain.AvailableUpkeepFixes(facts, findings),
		MaintenancePlan: facts.MaintenancePlan}, nil
}

// prepareUpkeepFix reads what an Upkeep fix needs and builds it: for update-statistics, the
// out-of-date statistics inside the database (the instance is not read, MNT04 does not need it).
func (s *Service) prepareUpkeepFix(ctx context.Context, g *genericFix, def domain.FixDef) error {
	d := g.database
	version, err := collect.EngineVersion(ctx, g.db)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	stats, err := collect.ReadStaleStats(ctx, g.db, d.Name)
	if err != nil {
		return newError(KindUnreachable, "could not read the statistics of %s: %v", d.Name, err)
	}
	g.upkeep = domain.UpkeepFacts{Database: d.Name, State: d.State, CanChange: d.CanFix(), ProductVersion: version, StaleStats: stats}
	g.preview, g.fix, err = domain.BuildUpkeepFix(def.ID, g.upkeep)
	return err
}

// applyUpkeepFix runs the statistics update the preview built as one batch inside the database,
// waiting up to upkeepApplyTimeout, reads back how many statistics are still out of date, and
// writes the change log.
func (s *Service) applyUpkeepFix(ctx context.Context, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, fix := g.database.Name, g.fix
	before := fix.Current
	applyCtx, cancel := context.WithTimeout(ctx, upkeepApplyTimeout)
	err := collect.ExecFixInDatabase(applyCtx, g.db, name, g.preview.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the statistics update on %s: %v. %s",
			name, err, domain.UpdateStatisticsPermission)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)

	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      fix.Setting,
		ChangeType:  "MAINTENANCE",
		Description: domain.UpdateStatisticsDescription(name, g.upkeep.UpdateStatisticsTables()),
		Statement:   g.preview.Statement,
	}
	n, err := collect.ReadStaleStatsCount(ctx, g.db, name)
	if err != nil {
		change.Result = "applied, but the statistics could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the statistics update on %s ran, but the statistics could not be read back: %v", name, err)
	}
	after := domain.StaleStatsValue(n)
	verified := n == 0
	change.Result = after + ", differs"
	if verified {
		change.Result = after + ", verified"
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: g.preview, Before: before, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// statisticsCompleted reads an update-statistics run back after its connection was lost
// (docs/phase4f-contract.md): done when the statistics of any table in the batch were updated after
// the run started (stats_date, on the server's clock), with the apply's read-back (the statistics
// still out of date); the tables not updated are counted in the result.
func statisticsCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	name := g.database.Name
	seen, tables := map[[2]string]bool{}, []domain.ShowplanObject{}
	for _, x := range g.upkeep.StaleStats {
		if k := [2]string{x.Schema, x.Table}; !seen[k] {
			seen[k] = true
			tables = append(tables, domain.ShowplanObject{Schema: x.Schema, Table: x.Table})
		}
	}
	facts, serverNow, err := collect.ReadTableStatistics(ctx, g.db, name, tables)
	if err != nil {
		return recovered{}, false, err
	}
	start := serverStart(startedAt, serverNow, time.Now()).Add(-2 * time.Second)
	updated := 0
	for _, t := range facts {
		if t.OldestUpdate != nil && !t.OldestUpdate.Before(start) {
			updated++
		}
	}
	if updated == 0 {
		return recovered{after: fmt.Sprintf("none of the %d tables had its statistics updated after the run started: the update did not finish; apply it again", len(tables))}, false, nil
	}
	n, err := collect.ReadStaleStatsCount(ctx, g.db, name)
	if err != nil {
		return recovered{}, false, err
	}
	after := domain.StaleStatsValue(n)
	if updated < len(tables) {
		after += fmt.Sprintf("; %d of %d tables updated before the connection was lost", updated, len(tables))
	}
	return recovered{after: after, verified: n == 0, change: domain.Change{Object: g.fix.Setting, ChangeType: "MAINTENANCE",
		Description: domain.UpdateStatisticsDescription(name, g.upkeep.UpdateStatisticsTables()), Statement: g.preview.Statement}}, true, nil
}

// readUpkeepPlan reads the maintenance plan of the database for the Upkeep card. A plan that could
// not be read is still a plan: its job lines say why.
func readUpkeepPlan(ctx context.Context, db *sql.DB, facts domain.UpkeepFacts) domain.MaintenancePlan {
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	f, err := collect.MaintenancePlan(readCtx, db, facts.Database)
	if err != nil {
		f = domain.MaintenancePlanFacts{ServerName: facts.ServerName, EngineEdition: facts.EngineEdition, Database: facts.Database,
			JobsError: err.Error(), ServerNow: facts.ServerNow}
	}
	return domain.BuildMaintenancePlan(f, facts.PlanAlerts())
}
