package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The eight actions of docs/phase4d-contract.md part 1 are in the registry, so prepareGenericFix and
// ApplyFixWithOptions use them, each under the check its finding comes from.
func TestSetupAndUpkeepActionsRegistered(t *testing.T) {
	for id, check := range map[string]string{
		domain.FixMultiUser: "DBC13", domain.FixReconfigure: "INS14", domain.FixTraceFlagOff: "INS15", domain.FixScopedConfigReset: "DBC14",
		domain.FixMinServerMemory: "INS16", domain.FixOperatorAlerts: "MNT07", domain.FixMaintenanceJob: "MNT03", domain.FixRemoveShrinkStep: "MNT05",
	} {
		if _, ok := registeredFixFor(id); !ok {
			t.Errorf("%s is not registered", id)
		}
		if d, ok := domain.FindFixDef(id); !ok || d.CheckID != check {
			t.Errorf("%s: fix definition %+v", id, d)
		}
	}
}

func suTestFix() *genericFix {
	return &genericFix{srv: domain.Server{Name: "local"}, database: domain.DatabaseStatus{Name: "GraniteLive", State: "ONLINE"}}
}

// A preview with a bad option returns no changes, so Apply is disabled, and the apply refuses it
// (docs/phase4d-contract.md, Tests).
func TestActionPreviewWithABadOption(t *testing.T) {
	var se *Error
	cases := []struct {
		id  string
		run func(g *genericFix) error
	}{
		{domain.FixReconfigure, func(g *genericFix) error {
			return reconfigurePreview(g, domain.ServerConfigFacts{ServerName: "S", Pending: []domain.PendingSetting{{Name: "max degree of parallelism", Value: 4, ValueInUse: 8, Dynamic: true}}},
				map[string]string{domain.OptionReconfigureAction: "drop"})
		}},
		{domain.FixTraceFlagOff, func(g *genericFix) error {
			return traceFlagPreview(g, domain.TraceFlagFacts{ServerName: "S", Flags: []int{1117}}, map[string]string{domain.OptionTraceFlag: "1; SHUTDOWN"})
		}},
		{domain.FixScopedConfigReset, func(g *genericFix) error {
			return scopedConfigPreview(g, domain.ScopedConfigFacts{Database: "GraniteLive", ProductVersion: "16.0.4255.3", Configs: []domain.ScopedConfig{{Name: "MAXDOP", Value: "4"}}},
				map[string]string{domain.OptionScopedName: "MAXDOP = 1; DROP"})
		}},
		{domain.FixOperatorAlerts, func(g *genericFix) error {
			return operatorAlertsPreview(g, domain.AlertFacts{ServerName: "S", EngineEdition: 2}, map[string]string{domain.OptionOperatorEmail: "not an address"})
		}},
		{domain.FixMaintenanceJob, func(g *genericFix) error {
			return maintenanceJobPreview(g, domain.MaintenanceJobFacts{ServerName: "S", EngineEdition: 2, Database: "GraniteLive", ServerNow: time.Now()},
				map[string]string{domain.OptionMaintTime: "25:00"})
		}},
		{domain.FixRemoveShrinkStep, func(g *genericFix) error {
			return shrinkStepPreview(g, domain.ShrinkStepFacts{Job: "Nightly", Found: true, Enabled: true, Steps: []domain.AgentJobStep{
				{ID: 1, Name: "Backup", Command: "BACKUP DATABASE x TO DISK = 'x'"}, {ID: 2, Name: "Shrink", Command: "DBCC SHRINKFILE (2)"}}},
				map[string]string{domain.OptionJobName: "Nightly", domain.OptionJobStep: "1"})
		}},
	}
	for _, c := range cases {
		g := suTestFix()
		if err := c.run(g); err != nil {
			t.Errorf("%s: %v", c.id, err)
			continue
		}
		if len(g.preview.Changes) != 0 || g.preview.Statement != "" || len(g.preview.Inputs) == 0 || len(g.preview.Warnings) == 0 {
			t.Errorf("%s: a bad option still has changes: %+v", c.id, g.preview)
		}
		if err := suApplyRefusal(g); !errors.As(err, &se) || se.Kind != KindConflict || se.Msg != g.preview.Warnings[0] {
			t.Errorf("%s: apply refusal %v", c.id, err)
		}
	}
}

// A fix without inputs that cannot be applied is a conflict with its reason: the dialog shows
// warnings only next to inputs.
func TestActionBlockedWithoutInputs(t *testing.T) {
	var se *Error
	g := suTestFix()
	err := multiUserPreview(g, domain.UserAccessFacts{Database: "GraniteLive", UserAccess: "SINGLE_USER",
		Sessions: []domain.UserAccessSession{{Session: 61, Login: "sa", Host: "SQL01", Command: "RESTORE DATABASE"}}})
	if !errors.As(err, &se) || se.Kind != KindConflict || !strings.HasPrefix(se.Msg, "Session 61 (sa on SQL01) is running RESTORE DATABASE") {
		t.Errorf("busy database: %v", err)
	}
	// Nothing to apply is the domain's ErrFixNotApplicable, which prepareGenericFix answers 404.
	if err := multiUserPreview(suTestFix(), domain.UserAccessFacts{UserAccess: "MULTI_USER"}); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("multi user: %v", err)
	}
	if err := minMemoryPreview(suTestFix(), domain.ServerConfigFacts{MinServerMemoryMB: 0, MaxServerMemoryMB: 4096}); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("min memory 0: %v", err)
	}
	if err := operatorAlertsPreview(suTestFix(), domain.AlertFacts{EngineEdition: 4}, nil); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("Express alerts: %v", err)
	}
	if err := shrinkStepPreview(suTestFix(), domain.ShrinkStepFacts{Job: "Gone"}, nil); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("no such job: %v", err)
	}
	// The job's name is needed before anything is read.
	if err := prepareRemoveShrinkStep(context.Background(), nil, suTestFix(), domain.FixDef{}, map[string]string{domain.OptionJobStep: "2"}); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("no job: %v", err)
	}
	// An Express maintenance job is a script: never applied.
	g = suTestFix()
	if err := maintenanceJobPreview(g, domain.MaintenanceJobFacts{EngineEdition: 4, Database: "GraniteLive", ServerNow: time.Now()}, nil); err != nil || !g.preview.Express {
		t.Fatalf("express preview %v %+v", err, g.preview)
	}
	if err := suApplyRefusal(g); !errors.As(err, &se) || se.Kind != KindConflict || !strings.Contains(se.Msg, "Express has no Agent") {
		t.Errorf("express apply: %v", err)
	}
}

// A good preview keeps what its apply needs in g.options, never in the caller's map.
func TestActionPreviewKeepsForTheApply(t *testing.T) {
	g := suTestFix()
	opts := map[string]string{domain.OptionMaintTime: "02:00"}
	g.options = opts
	if err := maintenanceJobPreview(g, domain.MaintenanceJobFacts{ServerName: "S", EngineEdition: 2, Database: "GraniteLive", Ola: true, ServerNow: time.Now()},
		map[string]string{domain.OptionMaintTime: "02:00", domain.OptionStatsOnly: "false"}); err != nil {
		t.Fatal(err)
	}
	if g.options[suJobsKey] != "kakapo: statistics (GraniteLive)\nkakapo: indexes (GraniteLive)" || !strings.HasPrefix(g.options[suDescriptionKey], "Created the maintenance job(s) ") ||
		len(opts) != 1 || suApplyRefusal(g) != nil {
		t.Errorf("kept %+v caller %+v", g.options, opts)
	}

	g = suTestFix()
	f := domain.ShrinkStepFacts{ServerName: "S", Job: "Nightly", Found: true, Enabled: true, Steps: []domain.AgentJobStep{
		{ID: 1, Name: "Backup", Command: "BACKUP DATABASE x TO DISK = 'x'"}, {ID: 2, Name: "Shrink", Command: "DBCC SHRINKFILE (2)"}}}
	if err := shrinkStepPreview(g, f, map[string]string{domain.OptionJobName: "Nightly"}); err != nil {
		t.Fatal(err)
	}
	if g.options[suStepNameKey] != "Shrink" || g.options[suStepCommandKey] != "DBCC SHRINKFILE (2)" || g.preview.Options[domain.OptionJobStep] != "2" {
		t.Errorf("kept %+v preview %+v", g.options, g.preview.Options)
	}

	g = suTestFix()
	if err := traceFlagPreview(g, domain.TraceFlagFacts{Flags: []int{1117}}, nil); err != nil || g.options[suDescriptionKey] != "Trace flag 1117 on local: on -> off" {
		t.Errorf("trace flag %v %+v", err, g.options)
	}
}
