package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The maintenance plan (docs/phase5b-contract.md), registered with the fix registry:
// history-cleanup (MNT08), which reads msdb's history and creates the weekly cleanup job, and
// maintenance-plan (MNT03), the composite that prepares and applies the missing pieces through the
// pieces' own fixes, one after another on the server's connection, stopping at the first failure.

func init() {
	registerFix(domain.FixHistoryCleanup, prepareHistoryCleanup, applyHistoryCleanup)
	registerFix(domain.FixMaintenancePlan, prepareMaintenancePlan, applyMaintenancePlan)
}

// --- history-cleanup ---

func prepareHistoryCleanup(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.ReadHistoryCleanupFacts(rctx, g.db, domain.HistoryKeepDays(options))
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	return historyCleanupPreview(g, f, options)
}

func historyCleanupPreview(g *genericFix, f domain.HistoryCleanupFacts, options map[string]string) error {
	p, fix, err := domain.BuildHistoryCleanupFix(f, options)
	if err != nil {
		return err
	}
	suKeep(g, suDescriptionKey, domain.HistoryCleanupDescription(fix))
	return suPreviewed(g, p, fix)
}

func applyHistoryCleanup(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	if err := collect.ExecFix(ctx, g.db, g.preview.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the history cleanup job on %s: %v. %s",
			g.srv.Name, err, domain.BackupFixPermission(g.srv.Name))
	}
	change := suChange(serverID, g, "History cleanup", "JOB", g.options[suDescriptionKey])
	names := []string{domain.HistoryCleanupJobName}
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	states, err := collect.ReadJobsByName(rctx, g.db, names)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The history cleanup job was created; it")
	}
	after, ok := domain.KakapoJobsReadBack(names, states)
	return s.suLog(ctx, change, g, g.fix.Current, after, ok)
}

// --- maintenance-plan ---

// planPieceFix is a piece's own prepare and apply.
type planPieceFix struct {
	prepare fixPreparer
	apply   fixApplier
}

// planPieceFor is how the composite reaches a piece's fix: the registry, or the Backups card's
// built-in branch for the backup schedule. A variable so that the tests can put fakes in.
var planPieceFor = func(id string) (planPieceFix, bool) {
	if r, ok := registeredFixFor(id); ok {
		return planPieceFix{prepare: r.prepare, apply: r.apply}, true
	}
	if id == domain.FixBackupSchedule {
		return planPieceFix{
			prepare: func(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, _ map[string]string) error {
				return s.prepareBackupFix(ctx, g, def)
			},
			apply: func(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
				return s.applyBackupFix(ctx, serverID, g)
			},
		}, true
	}
	return planPieceFix{}, false
}

// planRead is the plan as read: the plan, the operator and alerts state it was built with, and
// @@SERVERNAME.
type planRead struct {
	plan   domain.MaintenancePlan
	alerts domain.PlanAlerts
	server string
}

// readPlanFor reads the plan of g's database: collect.MaintenancePlan and the operators and alerts
// operator-alerts reads. A variable so that the tests can put a fake in.
var readPlanFor = func(ctx context.Context, g *genericFix) (planRead, error) {
	f, alerts, err := readPlanFacts(ctx, g.db, g.database.Name)
	if err != nil {
		return planRead{}, err
	}
	return planRead{plan: domain.BuildMaintenancePlan(f, alerts), alerts: alerts, server: f.ServerName}, nil
}

// readPlanFacts reads the plan facts of a database and the alerts state; alerts that cannot be read
// carry their reason.
func readPlanFacts(ctx context.Context, db *sql.DB, database string) (domain.MaintenancePlanFacts, domain.PlanAlerts, error) {
	rctx, cancel := suReadCtx(ctx)
	defer cancel()
	f, err := collect.MaintenancePlan(rctx, db, database)
	if err != nil {
		return f, domain.PlanAlerts{}, err
	}
	var alerts domain.PlanAlerts
	if f.EngineEdition == 4 {
		f.Proc = readMaintFor(rctx, db, database)
	}
	if f.EngineEdition != 4 {
		if af, err := collect.ReadAlertFacts(rctx, db); err != nil {
			alerts.Error = err.Error()
		} else {
			alerts = af.PlanAlerts()
		}
	}
	return f, alerts, nil
}

// planRun is what the composite's prepare keeps for its apply: the pieces prepared, and the
// genericFix each was prepared into (the same connection, the piece's own options), in apply order.
type planRun struct {
	parts []domain.PlanPart
	fixes []*genericFix
}

func prepareMaintenancePlan(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	read, err := readPlanFor(ctx, g)
	if err != nil {
		return newError(KindUnreachable, "%s: %v", g.srv.Name, err)
	}
	if read.plan.Express {
		return newError(KindConflict, "SQL Server Express has no SQL Server Agent, so kakapo cannot schedule the maintenance plan on %s: create each piece from its own line to get its script", g.srv.Name)
	}
	return s.planPreview(ctx, g, read, options)
}

// planPreview chooses the pieces from the options, prepares each through its own fix and stitches
// the previews (docs/phase5b-contract.md); the prepared pieces stay in g.plan for the apply.
func (s *Service) planPreview(ctx context.Context, g *genericFix, read planRead, options map[string]string) error {
	choice, err := domain.ChooseMaintenancePlan(read.plan, read.alerts, options)
	if err != nil {
		return err
	}
	run := &planRun{}
	for _, call := range choice.Calls {
		part, child := s.preparePlanPart(ctx, g, call)
		run.parts = append(run.parts, part)
		run.fixes = append(run.fixes, child)
	}
	p := domain.StitchMaintenancePlan(choice, run.parts)
	// The plan is the database's, though some of its pieces are the server's.
	p.Database = g.database.Name
	fix, _ := domain.MaintenancePlanOffer(&read.plan)
	titles := []string{}
	for _, c := range choice.Calls {
		titles = append(titles, strings.ToLower(c.Title))
	}
	fix.Target, fix.Options = "creates "+strings.Join(titles, ", "), p.Options
	g.plan = run
	suKeep(g, suServerKey, suServerLabel(g, read.server))
	return suPreviewed(g, p, fix)
}

// preparePlanPart prepares one piece through its own fix, into a genericFix of its own on g's
// connection and database.
func (s *Service) preparePlanPart(ctx context.Context, g *genericFix, call domain.PlanCall) (domain.PlanPart, *genericFix) {
	child := &genericFix{db: g.db, srv: g.srv, database: g.database, options: call.Options}
	part := domain.PlanPart{Call: call}
	piece, ok := planPieceFor(call.FixID)
	if !ok {
		part.Err = "kakapo has no fix named " + call.FixID
		return part, child
	}
	def, _ := domain.FindFixDef(call.FixID)
	err := piece.prepare(ctx, s, child, def, call.Options)
	part.Preview = child.preview
	if err != nil {
		part.Err = planErrorText(def, err)
	}
	return part, child
}

// planErrorText is why a piece could not be prepared or applied, in the words its fix used.
func planErrorText(def domain.FixDef, err error) string {
	var se *Error
	switch {
	case errors.Is(err, domain.ErrFixNotApplicable):
		return "nothing to create on this read (" + def.CheckID + " does not offer it)"
	case errors.As(err, &se):
		return se.Msg
	}
	return err.Error()
}

// suServerKey keeps @@SERVERNAME for the composite's summary row.
const suServerKey = "kakapo.server"

func applyMaintenancePlan(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	if g.plan == nil {
		return domain.FixResult{}, newError(KindConflict, "the maintenance plan was not prepared")
	}
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	run := g.plan
	operatorMade := false
	var firstErr error
	outcome := domain.RunMaintenancePlan(run.parts, func(i int, part domain.PlanPart) error {
		piece, ok := planPieceFor(part.Call.FixID)
		if !ok {
			return fmt.Errorf("kakapo has no fix named %s", part.Call.FixID)
		}
		child := run.fixes[i]
		def, _ := domain.FindFixDef(part.Call.FixID)
		if operatorMade && part.Call.Step != domain.PlanStepAlerts {
			// Prepared again now that the operator exists, so that the job mails it on failure.
			fresh := &genericFix{db: g.db, srv: g.srv, database: g.database, options: part.Call.Options}
			if err := piece.prepare(ctx, s, fresh, def, part.Call.Options); err != nil {
				firstErr = err
				return errors.New("could not be prepared again after the operator was created: " + planErrorText(def, err))
			}
			again := domain.PlanPart{Call: part.Call, Preview: fresh.preview}
			if why := again.Blocks(); why != "" {
				return errors.New("could not be prepared again after the operator was created: " + why)
			}
			child, run.fixes[i], run.parts[i] = fresh, fresh, again
		}
		if _, err := piece.apply(ctx, s, serverID, child); err != nil {
			firstErr = err
			return errors.New(planErrorText(def, err))
		}
		if part.Call.Step == domain.PlanStepAlerts && part.CreatesOperator() {
			operatorMade = true
		}
		return nil
	})
	if len(outcome.Created) == 0 && outcome.Failed != nil {
		// Nothing was created: the first piece's own error, named.
		kind := KindUnreachable
		var se *Error
		if errors.As(firstErr, &se) {
			kind = se.Kind
		}
		return domain.FixResult{}, newError(kind, "%s was not created, so nothing was: %s", outcome.Failed.Call.Title, outcome.Why)
	}

	server := g.options[suServerKey]
	if server == "" {
		server = g.srv.Name
	}
	created := run.parts[:len(outcome.Created)]
	stmt, rollback := domain.PlanStatements(created)
	change := domain.Change{ServerID: serverID, Database: g.database.Name, ChangedAt: time.Now().UTC().Truncate(time.Microsecond),
		Object: "Maintenance plan", ChangeType: "PLAN", Description: outcome.Description(server), Statement: stmt, Rollback: rollback}
	read, err := readPlanFor(ctx, g)
	if err != nil {
		return domain.FixResult{}, s.suLogUnread(ctx, change, err, "The maintenance plan's pieces were created; the plan")
	}
	after, ok := domain.PlanReadBack(read.plan, outcome.CreatedPieces())
	if outcome.Failed != nil {
		ok = false
		after += " Stopped at " + strings.ToLower(outcome.Failed.Call.Title) + ": " + outcome.Why + "."
		if len(outcome.NotCreated) > 0 {
			titles := []string{}
			for _, p := range outcome.NotCreated {
				titles = append(titles, strings.ToLower(p.Call.Title))
			}
			after += " Not created: " + strings.Join(titles, ", ") + "."
		}
	}
	res, err := s.suLog(ctx, change, g, g.fix.Current, after, ok)
	if err != nil {
		return res, err
	}
	res.Preview.Statement, res.Preview.Rollback = stmt, rollback
	return res, nil
}
