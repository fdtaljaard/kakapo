package service

import (
	"errors"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// Both helper fixes are in the registry, under BLK01, server scope (docs/phase5e-contract.md, Tests).
func TestHelperFixesRegistered(t *testing.T) {
	for _, id := range []string{domain.FixInstallXESession, domain.FixRemoveXESession} {
		if r, ok := registeredFixFor(id); !ok || r.long != nil {
			t.Errorf("%s is not registered as a short fix", id)
		}
		if d, ok := domain.FindFixDef(id); !ok || d.CheckID != "BLK01" || d.Scope != domain.ScopeServer || d.Setting != domain.XEObject {
			t.Errorf("%s: fix definition %+v", id, d)
		}
	}
}

func xeTestFacts() domain.XEInstallFacts {
	return domain.XEInstallFacts{
		HelperFacts: domain.HelperFacts{ServerName: "SQL01", ProductVersion: "16.0.4135.4", EngineEdition: 3,
			ErrorLogFile: `D:\MSSQL\Log\ERRORLOG`, Session: domain.XESession{ThresholdSeconds: 0}},
		FolderChecked: true, FolderExists: true,
	}
}

// A bad option, or a blocking warning, gives a preview without changes, so Apply is disabled, and the
// apply refuses it without running anything; a good preview keeps its change log line and the
// threshold it was read with.
func TestHelperPreviews(t *testing.T) {
	var se *Error
	for name, run := range map[string]func(g *genericFix) error{
		"bad threshold": func(g *genericFix) error {
			return installXEPreview(g, xeTestFacts(), map[string]string{domain.OptionXEThreshold: "5; SHUTDOWN"})
		},
		"folder missing": func(g *genericFix) error {
			f := xeTestFacts()
			f.FolderExists = false
			return installXEPreview(g, f, nil)
		},
		"remove a bad threshold": func(g *genericFix) error {
			f := domain.XERemoveFacts{HelperFacts: domain.HelperFacts{ServerName: "SQL01", Session: xeTestSession()}}
			return removeXEPreview(g, f, map[string]string{domain.OptionXEThreshold: "x"})
		},
	} {
		g := suTestFix()
		if err := run(g); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(g.preview.Changes) != 0 || g.preview.Statement != "" || len(g.preview.Inputs) == 0 {
			t.Errorf("%s: applicable %+v", name, g.preview)
		}
		if err := suApplyRefusal(g); !errors.As(err, &se) || se.Kind != KindConflict {
			t.Errorf("%s: apply %v", name, err)
		}
	}

	g := suTestFix()
	if err := installXEPreview(g, xeTestFacts(), nil); err != nil {
		t.Fatal(err)
	}
	if len(g.preview.Changes) != 2 || !strings.Contains(g.preview.Statement, "CREATE EVENT SESSION [kakapo] ON SERVER") ||
		!strings.HasPrefix(g.options[suDescriptionKey], "Created the event session kakapo on SQL01: ") || g.options[xeThresholdKey] != "0" {
		t.Errorf("install preview %+v %v", g.preview, g.options)
	}
	// kakapo's session already there: nothing to apply, said plainly.
	f := xeTestFacts()
	f.Session = xeTestSession()
	if err := installXEPreview(suTestFix(), f, nil); !errors.As(err, &se) || se.Kind != KindNotFound || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("exists: %v", err)
	}
	if err := removeXEPreview(suTestFix(), domain.XERemoveFacts{}, nil); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("nothing to remove: %v", err)
	}
	g = suTestFix()
	if err := removeXEPreview(g, domain.XERemoveFacts{HelperFacts: domain.HelperFacts{ServerName: "SQL01", Session: xeTestSession()}}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.preview.Statement, "DROP EVENT SESSION [kakapo] ON SERVER") || !strings.HasPrefix(g.options[suDescriptionKey], "Removed the event session kakapo on SQL01") {
		t.Errorf("remove preview %+v", g.preview)
	}
}

func xeTestSession() domain.XESession {
	s := domain.XESession{Exists: true, Running: true, Events: []string{"sqlserver.blocked_process_report", "sqlserver.xml_deadlock_report"},
		Targets: []string{"package0.event_file"}, FileName: `D:\MSSQL\Log\kakapo.xel`, MaxFileMB: 50, MaxFiles: 4, DispatchSec: 30, ThresholdSeconds: 5}
	s.Ours = s.HasKakapoShape()
	return s
}

// The database the helper's fixes go through: the remembered one when online, else the first online.
func TestPickHelperDatabase(t *testing.T) {
	dbs := []domain.DatabaseStatus{{Name: "Old", State: "OFFLINE"}, {Name: "Shop", State: "ONLINE"}, {Name: "Warehouse", State: "ONLINE"}}
	for _, c := range []struct{ remembered, want string }{{"Warehouse", "Warehouse"}, {"warehouse", "Warehouse"}, {"Old", "Shop"}, {"", "Shop"}, {"Gone", "Shop"}} {
		if got := pickHelperDatabase(dbs, c.remembered); got != c.want {
			t.Errorf("remembered %q: %q, want %q", c.remembered, got, c.want)
		}
	}
	if got := pickHelperDatabase(dbs[:1], ""); got != "" {
		t.Errorf("none online: %q", got)
	}
}
