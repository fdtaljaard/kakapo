package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// The copy of Ola Hallengren's scripts is his, byte for byte (ola/SOURCE.md).
func TestOlaFilesPinned(t *testing.T) {
	want := map[string]string{
		"CommandLog.sql":     "7a508fa7ed562ec5ee09d4f587f7f2a87f7a4eb3c57450602ee99cdeb4e18b31",
		"CommandExecute.sql": "190962fc9802ce8d8f5082a23119567d27f05d2bfc36e1bdf5ce79edea5d8e76",
		"IndexOptimize.sql":  "ae23d8bd3d866c8c83a6ce27d982b0573e4f96a30e744b3549fa63a9548abed6",
		"LICENSE":            "11a9e9cd4b1552936dcae48b1a6b1da0d083767f8b1a813cdf180b38c76ed60d",
	}
	for name, h := range want {
		b, err := olaFiles.ReadFile("ola/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != h {
			t.Errorf("%s: SHA-256 %s, want %s (ola/SOURCE.md)", name, got, h)
		}
	}
	for _, name := range []string{"CommandExecute.sql", "IndexOptimize.sql"} {
		if !strings.Contains(olaScript(name), "--// Version: "+OlaVersion) {
			t.Errorf("%s is not version %s", name, OlaVersion)
		}
	}
	if b, _ := olaFiles.ReadFile("ola/LICENSE"); !strings.HasPrefix(string(b), "MIT License") {
		t.Error("LICENSE is not Ola's MIT licence")
	}
}

func TestOlaInstallStatement(t *testing.T) {
	s := OlaInstallStatement()
	if !strings.HasPrefix(s, "/* kakapo: Ola Hallengren's SQL Server Maintenance Solution (https://ola.hallengren.com), version "+OlaVersion+
		", MIT licence: CommandLog, CommandExecute and IndexOptimize only, no jobs. */\nUSE master;\nGO\n") {
		t.Errorf("head:\n%s", s[:300])
	}
	if strings.Contains(s, "\r") || strings.Contains(s, string(rune(0xFEFF))) {
		t.Error("a CR or a byte order mark is left in the statement")
	}
	log, exec, opt := strings.Index(s, "CREATE TABLE [dbo].[CommandLog]"), strings.Index(s, "ALTER PROCEDURE [dbo].[CommandExecute]"),
		strings.Index(s, "ALTER PROCEDURE [dbo].[IndexOptimize]")
	if log < 0 || exec < log || opt < exec {
		t.Errorf("order: CommandLog %d, CommandExecute %d, IndexOptimize %d", log, exec, opt)
	}
	// Only the index objects, and no jobs.
	for _, not := range []string{"DatabaseBackup]", "DatabaseIntegrityCheck]", "sp_add_job"} {
		if strings.Contains(s, not) {
			t.Errorf("the statement has %s", not)
		}
	}
	// Batches: master, the table's two SETs and its CREATE, then each procedure's SETs, stub and ALTER.
	b := SplitBatches(s)
	if len(b) < 8 || !strings.HasPrefix(b[len(b)-1], "ALTER PROCEDURE [dbo].[IndexOptimize]") {
		t.Errorf("%d batches; the last starts %q", len(b), b[len(b)-1][:min(len(b[len(b)-1]), 60)])
	}
}

func TestBuildInstallOlaIndexFix(t *testing.T) {
	f := OlaIndexFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3"}
	p, fix, err := BuildInstallOlaIndexFix(f)
	if err != nil || fix.ID != FixInstallOlaIndex || fix.Current != "missing" || fix.Target != "version "+OlaVersion {
		t.Fatalf("%v %+v", err, fix)
	}
	if len(p.Changes) != 3 || p.Changes[0] != (Drift{"Table master.dbo.CommandLog", "missing", "created"}) ||
		p.Changes[2] != (Drift{"Procedure master.dbo.IndexOptimize", "missing", "version " + OlaVersion}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if p.Statement != OlaInstallStatement() || !strings.HasSuffix(p.Rollback, "DROP PROCEDURE dbo.CommandExecute;\nDROP TABLE dbo.CommandLog;") {
		t.Errorf("rollback %q", p.Rollback)
	}
	if len(p.Warnings) != 2 || p.Warnings[0] != "Index work stays off until you turn it on in the maintenance plan." {
		t.Errorf("warnings %q", p.Warnings)
	}

	// CommandLog there already: kept, and the rollback leaves it; kakapo's procedure there: its statistics switch is said.
	f.CommandLog, f.MaintProc = true, true
	p, _, _ = BuildInstallOlaIndexFix(f)
	if p.Changes[0] != (Drift{"Table master.dbo.CommandLog", "exists", "kept as it is"}) || strings.Contains(p.Rollback, "DROP TABLE") ||
		len(p.Warnings) != 3 || !strings.Contains(p.Warnings[1], "instead of sp_updatestats") {
		t.Errorf("%+v %q %q", p.Changes, p.Rollback, p.Warnings)
	}

	// IndexOptimize there: nothing to do.
	if _, _, err := BuildInstallOlaIndexFix(OlaIndexFacts{ProductVersion: "16.0.1", IndexOptimize: true, CommandExecute: true}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("IndexOptimize there: %v", err)
	}

	// Held back: before 2017, and over someone else's CommandExecute. A warning, nothing to apply.
	for _, c := range []struct {
		f    OlaIndexFacts
		want string
	}{
		{OlaIndexFacts{ProductVersion: "13.0.6300.2"}, "Ola Hallengren's current IndexOptimize needs SQL Server 2017 or later; this server is SQL Server 2016."},
		{OlaIndexFacts{ProductVersion: "15.0.4355.3", CommandExecute: true, Version: "2019-06-14 00:05:34"},
			"Ola Hallengren's CommandExecute (version 2019-06-14 00:05:34) is already in master, without IndexOptimize. kakapo does not change someone else's install: add IndexOptimize.sql from the same version (https://ola.hallengren.com)."},
	} {
		p, _, err := BuildInstallOlaIndexFix(c.f)
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || len(p.Warnings) != 1 || p.Warnings[0] != c.want {
			t.Errorf("%+v: %v %+v", c.f, err, p)
		}
	}
}

func TestOlaInstallReadBack(t *testing.T) {
	f := OlaIndexFacts{IndexOptimize: true, CommandExecute: true, CommandLog: true, Version: OlaVersion}
	if text, ok := OlaInstallReadBack(f); !ok || text != "version "+OlaVersion+" (CommandLog, CommandExecute, IndexOptimize)" {
		t.Errorf("%v %s", ok, text)
	}
	f.Version = "2019-06-14 00:05:34"
	if text, ok := OlaInstallReadBack(f); ok || text != "IndexOptimize version 2019-06-14 00:05:34, not "+OlaVersion {
		t.Errorf("%v %s", ok, text)
	}
	if text, ok := OlaInstallReadBack(OlaIndexFacts{CommandLog: true, CommandExecute: true}); ok || text != "missing in master: IndexOptimize" {
		t.Errorf("%v %s", ok, text)
	}
	if OlaInstallDescription(`SQL01\WMS`) != `Ola Hallengren's IndexOptimize on SQL01\WMS: missing -> version `+OlaVersion {
		t.Error(OlaInstallDescription(`SQL01\WMS`))
	}
}

// The Index maintenance row: Install without Ola; the reason instead when kakapo does not install; the
// composite never carries it.
func TestPlanIndexRowOffersOlaInstall(t *testing.T) {
	f := planFacts()
	f.IndexOptimize = false
	plan := BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	p, _ := plan.piece(PieceIndexes)
	if p.Status != PlanOptional || p.FixID != FixInstallOlaIndex || !strings.HasPrefix(p.Note, "Index work needs Ola Hallengren's IndexOptimize in master: Install puts") {
		t.Errorf("%+v", p)
	}
	if c, err := ChooseMaintenancePlan(plan, PlanAlerts{Operators: 1}, nil); err == nil {
		for _, call := range c.Calls {
			for _, piece := range call.Pieces {
				if piece == PieceIndexes {
					t.Errorf("the composite does index work without IndexOptimize: %+v", call)
				}
			}
		}
	}

	f.ProductVersion = "13.0.6300.2"
	plan = BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	p, _ = plan.piece(PieceIndexes)
	if p.FixID != "" || p.Note != "Ola Hallengren's current IndexOptimize needs SQL Server 2017 or later; this server is SQL Server 2016. Statistics matter more." {
		t.Errorf("2016: %+v", p)
	}

	f.ProductVersion, f.OlaCommandExecute, f.OlaVersion = "16.0.4255.3", true, "2020-01-26 14:06:53"
	plan = BuildMaintenancePlan(f, PlanAlerts{Operators: 1})
	p, _ = plan.piece(PieceIndexes)
	if p.FixID != "" || !strings.HasPrefix(p.Note, "Ola Hallengren's CommandExecute (version 2020-01-26 14:06:53) is already in master") {
		t.Errorf("CommandExecute alone: %+v", p)
	}

	// With Ola: the weekly index job, as before.
	plan = BuildMaintenancePlan(planFacts(), PlanAlerts{Operators: 1})
	if p, _ := plan.piece(PieceIndexes); p.FixID != FixMaintenanceJob {
		t.Errorf("with Ola: %+v", p)
	}
}
