package service

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Both fixes of docs/phase5b-contract.md are in the registry, under the checks they come from, and
// answer at once.
func TestMaintenanceFixesRegistered(t *testing.T) {
	for id, check := range map[string]string{domain.FixHistoryCleanup: "MNT08", domain.FixMaintenancePlan: "MNT03"} {
		r, ok := registeredFixFor(id)
		if !ok {
			t.Errorf("%s is not registered", id)
			continue
		}
		if r.long != nil {
			t.Errorf("%s runs in the background", id)
		}
		if d, ok := domain.FindFixDef(id); !ok || d.CheckID != check || d.Scope != domain.ScopeServer {
			t.Errorf("%s: %+v", id, d)
		}
	}
	// Every piece the composite calls has a fix it can reach.
	for _, id := range []string{domain.FixOperatorAlerts, domain.FixHistoryCleanup, domain.FixCheckdbJob, domain.FixMaintenanceJob, domain.FixBackupSchedule} {
		if p, ok := planPieceFor(id); !ok || p.prepare == nil || p.apply == nil {
			t.Errorf("piece %s unreachable", id)
		}
	}
}

// A history-cleanup preview with a keepDays under 7 has no changes, so Apply refuses it.
func TestHistoryCleanupBlocks(t *testing.T) {
	g := suTestFix()
	f := domain.HistoryCleanupFacts{ServerName: "S", EngineEdition: 2, KeepDays: 30, ServerNow: time.Now()}
	if err := historyCleanupPreview(g, f, map[string]string{domain.OptionHistoryKeepDays: "6"}); err != nil {
		t.Fatal(err)
	}
	var se *Error
	if len(g.preview.Changes) != 0 || !errors.As(suApplyRefusal(g), &se) || se.Kind != KindConflict || !strings.HasPrefix(se.Msg, "Keep between 7 and 365 days") {
		t.Errorf("blocked preview %+v", g.preview)
	}
	g = suTestFix()
	if err := historyCleanupPreview(g, f, nil); err != nil || len(g.preview.Changes) == 0 || g.options[suDescriptionKey] != "Created the job kakapo: history cleanup (Sunday 05:00, keeps 30 days)" {
		t.Errorf("preview %v %+v %v", err, g.preview, g.options)
	}
	f.JobExists = true
	if err := historyCleanupPreview(suTestFix(), f, nil); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("job exists: %v", err)
	}
}

// fakePieces records the composite's calls to the pieces: each prepare builds a preview with one
// change and a statement; apply fails for the fix named in failOn. The alerts piece creates the
// operator.
type fakePieces struct {
	calls  []string
	failOn string
}

func (fp *fakePieces) piece(id string) (planPieceFix, bool) {
	return planPieceFix{
		prepare: func(_ context.Context, _ *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
			fp.calls = append(fp.calls, "prepare "+id)
			changes := []domain.Drift{{Setting: def.Setting, Current: "none", Target: "created"}}
			if id == domain.FixOperatorAlerts {
				changes = append(changes, domain.Drift{Setting: "Operator", Current: "none with an email address", Target: "Database support (dba@client.co.za)"})
			}
			g.preview = domain.FixPreview{Changes: changes, Statement: "-- " + id, Rollback: "-- undo " + id, Warnings: []string{"Needs sysadmin on S."}, Options: options}
			g.fix = domain.Fix{ID: id}
			return nil
		},
		apply: func(_ context.Context, _ *Service, _ int64, g *genericFix) (domain.FixResult, error) {
			fp.calls = append(fp.calls, "apply "+id)
			if id == fp.failOn {
				return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the job: permission denied")
			}
			return domain.FixResult{After: "created"}, nil
		},
	}, true
}

func planServiceFixture(t *testing.T) (*Service, int64, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthWindows}, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return New(st, fakeCreds{}, "test"), srv.ID, st
}

// The composite prepares and applies the pieces through their own fixes in the contract's order
// (alerts, cleanup, check, statistics, backup), prepares the jobs again after the operator is
// created, stops at the first failure and writes one PLAN change row naming what was and was not
// created (docs/phase5b-contract.md, Tests).
func TestMaintenancePlanCallsPiecesInOrder(t *testing.T) {
	never := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	facts := domain.MaintenancePlanFacts{ServerName: `SQL01\WMS`, EngineEdition: 2, Database: "WarehouseLive", RecoveryModel: "FULL",
		LastGoodCheckDb: &never, Jobs: []domain.PlanJob{}, ServerNow: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}
	before := domain.BuildMaintenancePlan(facts, domain.PlanAlerts{})
	// After: the alerts and the cleanup job are there.
	facts.Jobs = []domain.PlanJob{{Name: domain.HistoryCleanupJobName, Enabled: true, LastStatus: domain.PlanNoRun,
		Steps:     []domain.PlanJobStep{{Command: domain.HistoryCleanupCommand(30)}},
		Schedules: []domain.PlanSchedule{{FreqType: 8, FreqInterval: 1, SubdayType: 1, StartTime: 50000, Enabled: true}}}}
	after := domain.BuildMaintenancePlan(facts, domain.PlanAlerts{Operators: 1})

	reads := 0
	oldRead, oldPiece := readPlanFor, planPieceFor
	t.Cleanup(func() { readPlanFor, planPieceFor = oldRead, oldPiece })
	readPlanFor = func(context.Context, *genericFix) (planRead, error) {
		reads++
		if reads == 1 {
			return planRead{plan: before, server: `SQL01\WMS`}, nil
		}
		return planRead{plan: after, alerts: domain.PlanAlerts{Operators: 1}, server: `SQL01\WMS`}, nil
	}
	fp := &fakePieces{failOn: domain.FixCheckdbJob}
	planPieceFor = fp.piece

	s, serverID, st := planServiceFixture(t)
	ctx := context.Background()
	g := &genericFix{srv: domain.Server{ID: serverID, Name: "client"}, database: domain.DatabaseStatus{Name: "WarehouseLive", State: "ONLINE"}}
	if err := prepareMaintenancePlan(ctx, s, g, domain.FixDef{}, map[string]string{"email": "dba@client.co.za"}); err != nil {
		t.Fatal(err)
	}
	wantPrepares := []string{"prepare operator-alerts", "prepare history-cleanup", "prepare checkdb-job", "prepare maintenance-job", "prepare backup-schedule"}
	if !reflect.DeepEqual(fp.calls, wantPrepares) {
		t.Errorf("prepares %v", fp.calls)
	}
	if len(g.preview.Changes) != 6 || !strings.HasPrefix(g.preview.Statement, "-- 1. Alerts and an operator (operator-alerts)\n-- operator-alerts\nGO") ||
		g.fix.ID != domain.FixMaintenancePlan || g.fix.Target != "creates alerts and an operator, history cleanup, integrity check, statistics update, backups" {
		t.Errorf("preview %+v fix %+v", g.preview, g.fix)
	}
	if strings.Count(strings.Join(g.preview.Warnings, "\n"), "Needs sysadmin on S.") != 1 {
		t.Errorf("warnings %q", g.preview.Warnings)
	}

	fp.calls = nil
	res, err := applyMaintenancePlan(ctx, s, serverID, g)
	if err != nil {
		t.Fatal(err)
	}
	// Alerts, then each job prepared again (the operator exists now) and applied; it stops at the check.
	wantApplies := []string{"apply operator-alerts", "prepare history-cleanup", "apply history-cleanup", "prepare checkdb-job", "apply checkdb-job"}
	if !reflect.DeepEqual(fp.calls, wantApplies) {
		t.Errorf("applies %v", fp.calls)
	}
	if res.Verified || res.After != "Alerts and an operator: in place; History cleanup: created, not run yet. Stopped at integrity check: SQL Server refused to create the job: permission denied. Not created: statistics update, backups." {
		t.Errorf("result %+v", res)
	}
	changes, err := st.ListChanges(ctx, serverID, "WarehouseLive", 10)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes %v %+v", err, changes)
	}
	c := changes[0]
	if c.ChangeType != "PLAN" || c.Object != "Maintenance plan" ||
		c.Description != `Maintenance plan on SQL01\WMS: created alerts and an operator, history cleanup; stopped at integrity check (SQL Server refused to create the job: permission denied); not created: statistics update, backups` ||
		c.Statement != "-- 1. Alerts and an operator (operator-alerts)\n-- operator-alerts\nGO\n-- 2. History cleanup (history-cleanup)\n-- history-cleanup\nGO" ||
		c.Rollback != "-- Undo 2. History cleanup\n-- undo history-cleanup\nGO\n-- Undo 1. Alerts and an operator\n-- undo operator-alerts\nGO" ||
		!strings.HasSuffix(c.Result, ", differs") {
		t.Errorf("change %+v", c)
	}

	// The first piece fails: nothing created, its error, no PLAN row.
	reads = 0
	fp = &fakePieces{failOn: domain.FixOperatorAlerts}
	planPieceFor = fp.piece
	g = &genericFix{srv: domain.Server{ID: serverID, Name: "client"}, database: domain.DatabaseStatus{Name: "WarehouseLive", State: "ONLINE"}}
	if err := prepareMaintenancePlan(ctx, s, g, domain.FixDef{}, nil); err != nil {
		t.Fatal(err)
	}
	var se *Error
	if _, err := applyMaintenancePlan(ctx, s, serverID, g); !errors.As(err, &se) || se.Kind != KindUnreachable ||
		se.Msg != "Alerts and an operator was not created, so nothing was: SQL Server refused to create the job: permission denied" {
		t.Errorf("first fails: %v", err)
	}
	if changes, _ := st.ListChanges(ctx, serverID, "WarehouseLive", 10); len(changes) != 1 {
		t.Errorf("a PLAN row without anything created: %+v", changes)
	}

	// Express: the plan cannot be scheduled from here.
	readPlanFor = func(context.Context, *genericFix) (planRead, error) {
		return planRead{plan: domain.MaintenancePlan{Express: true}}, nil
	}
	if err := prepareMaintenancePlan(ctx, s, suTestFix(), domain.FixDef{}, nil); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("express: %v", err)
	}
	// Nothing missing: nothing to apply.
	readPlanFor = func(context.Context, *genericFix) (planRead, error) {
		return planRead{plan: domain.MaintenancePlan{}}, nil
	}
	if err := prepareMaintenancePlan(ctx, s, suTestFix(), domain.FixDef{}, nil); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("nothing missing: %v", err)
	}
}
