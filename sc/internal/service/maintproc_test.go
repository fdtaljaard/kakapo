package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// Both fixes of the maintenance procedure are in the registry, short, under MNT03, server scope
// (docs/phase5p-contract.md, Tests).
func TestMaintProcFixesRegistered(t *testing.T) {
	for _, id := range []string{domain.FixInstallMaintenanceProc, domain.FixRemoveMaintenanceProc} {
		if r, ok := registeredFixFor(id); !ok || r.long != nil {
			t.Errorf("%s is not registered as a short fix", id)
		}
		if d, ok := domain.FindFixDef(id); !ok || d.CheckID != "MNT03" || d.Scope != domain.ScopeServer || d.Setting != domain.MaintObject || d.Card() != domain.CardUpkeep {
			t.Errorf("%s: fix definition %+v", id, d)
		}
	}
}

func maintTestFacts() domain.MaintInstallFacts {
	return domain.MaintInstallFacts{MaintProcFacts: domain.MaintProcFacts{ServerName: "SQL01", ProductVersion: "16.0.4135.4", EngineEdition: 4,
		Edition: "Express Edition (64-bit)", ServerNow: time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC), Database: "WarehouseLive", RecoveryModel: "FULL",
		DefaultBackupDir: `D:\Backup`}, FolderChecked: true, FolderExists: true}
}

// The install's preview keeps the settings row it writes (for the read-back) and its change log line; a
// missing folder gives a preview without changes, which the apply refuses.
func TestMaintProcPreviews(t *testing.T) {
	g := suTestFix()
	if err := installMaintPreview(g, maintTestFacts(), map[string]string{domain.OptionProcFullTime: "21:30"}); err != nil {
		t.Fatal(err)
	}
	var want domain.MaintSettings
	if err := json.Unmarshal([]byte(g.options[maintWantKey]), &want); err != nil || want.Database != "WarehouseLive" || want.FullTime != "21:30" || want.Folder != `D:\Backup` {
		t.Errorf("kept row %+v %v", want, err)
	}
	if !strings.HasPrefix(g.options[suDescriptionKey], "Installed kakapo's maintenance procedure master.dbo.kakapo_Maintenance on SQL01") || g.options[maintInstalledKey] != "false" {
		t.Errorf("kept %v", g.options)
	}
	if err := suApplyRefusal(g); err != nil {
		t.Errorf("apply refused: %v", err)
	}

	var se *Error
	f := maintTestFacts()
	f.FolderExists = false
	g = suTestFix()
	if err := installMaintPreview(g, f, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.preview.Changes) != 0 || g.options[maintWantKey] != "" {
		t.Errorf("missing folder: applicable %+v", g.preview)
	}
	if err := suApplyRefusal(g); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("missing folder: apply %v", err)
	}

	if err := removeMaintPreview(suTestFix(), domain.MaintProcFacts{ServerName: "SQL01"}, nil); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("nothing to remove: %v", err)
	}
	installed := f.MaintProcFacts
	installed.ProcExists, installed.ProcOurs, installed.ProcVersion, installed.SettingsExists, installed.LogExists = true, true, 1, true, true
	installed.Settings = []domain.MaintSettings{want}
	g = suTestFix()
	if err := removeMaintPreview(g, installed, map[string]string{domain.OptionProcRemove: "WarehouseLive"}); err != nil {
		t.Fatal(err)
	}
	if g.options[suDescriptionKey] != "Took WarehouseLive off kakapo's maintenance procedure on SQL01" || !strings.Contains(g.preview.Statement, "DELETE FROM dbo.kakapo_MaintenanceSettings") {
		t.Errorf("remove one %v %s", g.options, g.preview.Statement)
	}
}
