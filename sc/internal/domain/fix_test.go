package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustFix(t *testing.T, id string, f SetupFacts) (FixPreview, Fix) {
	t.Helper()
	p, fix, err := BuildFix(id, f, "", nil)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return p, fix
}

const (
	wantServer     = `This is a server setting: it changes every database on SQL01\WMS.`
	wantServerPerm = "Needs the ALTER SETTINGS permission on the server (sysadmin or serveradmin)."
	wantDbPerm     = "Needs the ALTER permission on the database WarehouseLive."
)

func TestServerFixes(t *testing.T) {
	for _, tc := range []struct {
		id       string
		mutate   func(*SetupFacts)
		checkID  string
		setting  string
		cur, tgt string
		stmt     string
		rollback string
		warnings []string
	}{
		{"max-server-memory", func(f *SetupFacts) { f.MaxServerMemoryMB = 2147483647 }, "INS02", "max server memory (MB)",
			"2147483647 MB", "28672 MB",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', 28672; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', 2147483647; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, warnMemoryRoom, wantServerPerm}},
		{"max-server-memory", func(f *SetupFacts) { f.MaxServerMemoryMB = 32000 }, "INS03", "max server memory (MB)",
			"32000 MB", "28672 MB",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', 28672; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', 32000; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, warnMemoryRoom, wantServerPerm}},
		{"maxdop", func(f *SetupFacts) { f.MaxDop, f.CPUCount, f.CoresPerNode = 0, 24, 12 }, "INS04", "max degree of parallelism",
			"0", "8",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max degree of parallelism', 8; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max degree of parallelism', 0; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, wantServerPerm}},
		{"maxdop", func(f *SetupFacts) { f.MaxDop, f.CoresPerNode = 8, 6 }, "INS04", "max degree of parallelism", "8", "6",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max degree of parallelism', 6; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max degree of parallelism', 8; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, wantServerPerm}},
		{"cost-threshold", func(f *SetupFacts) { f.CostThreshold = 5 }, "INS05", "cost threshold for parallelism", "5", "50",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'cost threshold for parallelism', 50; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'cost threshold for parallelism', 5; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, wantServerPerm}},
		{"optimize-adhoc", func(f *SetupFacts) { f.OptimizeAdHoc = 0 }, "INS06", "optimize for ad hoc workloads", "0", "1",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'optimize for ad hoc workloads', 1; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'optimize for ad hoc workloads', 0; RECONFIGURE;",
			[]string{wantServer, warnPlanCache, wantServerPerm}},
		{"backup-compression", func(f *SetupFacts) { f.BackupCompression = 0 }, "INS10", "backup compression default", "0", "1",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'backup compression default', 1; RECONFIGURE;",
			"EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'backup compression default', 0; RECONFIGURE;",
			[]string{wantServer, "Ignore if a third-party tool takes the backups.", wantServerPerm}},
	} {
		f := goodFacts()
		tc.mutate(&f)
		p, fix := mustFix(t, tc.id, f)
		want := Fix{ID: tc.id, Title: fix.Title, Scope: ScopeServer, CheckID: tc.checkID, Setting: tc.setting, Current: tc.cur, Target: tc.tgt}
		if !reflect.DeepEqual(fix, want) || fix.Title == "" || fix.NeedsRestart {
			t.Errorf("%s fix\n got %+v\nwant %+v", tc.id, fix, want)
		}
		if p.Statement != tc.stmt || p.Rollback != tc.rollback {
			t.Errorf("%s statement/rollback\n got %s\n     %s", tc.id, p.Statement, p.Rollback)
		}
		if !reflect.DeepEqual(p.Warnings, tc.warnings) {
			t.Errorf("%s warnings %q", tc.id, p.Warnings)
		}
		if p.Database != "" || p.EngineVersion != "16.0.4255.3" || len(p.Changes) != 1 ||
			p.Changes[0] != (Drift{tc.setting, tc.cur, tc.tgt}) {
			t.Errorf("%s preview %+v", tc.id, p)
		}
		if d := FixChangeDescription(fix, f.ServerName, "WarehouseLive"); d != tc.setting+` on SQL01\WMS: `+tc.cur+" -> "+tc.tgt {
			t.Errorf("%s description %q", tc.id, d)
		}
	}
}

func TestDatabaseFixes(t *testing.T) {
	for _, tc := range []struct {
		id       string
		mutate   func(*SetupFacts)
		setting  string
		cur, tgt string
		stmt     string
		rollback string
		warnings []string
	}{
		{"auto-close", func(f *SetupFacts) { f.AutoClose = true }, "AUTO_CLOSE", "ON", "OFF",
			"ALTER DATABASE [WarehouseLive] SET AUTO_CLOSE OFF;", "ALTER DATABASE [WarehouseLive] SET AUTO_CLOSE ON;",
			[]string{wantDbPerm}},
		{"auto-shrink", func(f *SetupFacts) { f.AutoShrink = true }, "AUTO_SHRINK", "ON", "OFF",
			"ALTER DATABASE [WarehouseLive] SET AUTO_SHRINK OFF;", "ALTER DATABASE [WarehouseLive] SET AUTO_SHRINK ON;",
			[]string{wantDbPerm}},
		{"page-verify", func(f *SetupFacts) { f.PageVerify = "TORN_PAGE_DETECTION" }, "PAGE_VERIFY", "TORN_PAGE_DETECTION", "CHECKSUM",
			"ALTER DATABASE [WarehouseLive] SET PAGE_VERIFY CHECKSUM;", "ALTER DATABASE [WarehouseLive] SET PAGE_VERIFY TORN_PAGE_DETECTION;",
			[]string{"Pages are protected as they are next written; an index rebuild protects them all.", wantDbPerm}},
		{"auto-statistics", func(f *SetupFacts) { f.AutoCreateStats, f.AutoUpdateStats = false, false },
			"AUTO_CREATE_STATISTICS, AUTO_UPDATE_STATISTICS", "create OFF, update OFF", "create ON, update ON",
			"ALTER DATABASE [WarehouseLive] SET AUTO_CREATE_STATISTICS ON; ALTER DATABASE [WarehouseLive] SET AUTO_UPDATE_STATISTICS ON;",
			"ALTER DATABASE [WarehouseLive] SET AUTO_CREATE_STATISTICS OFF; ALTER DATABASE [WarehouseLive] SET AUTO_UPDATE_STATISTICS OFF;",
			[]string{"Check first that the application vendor does not require this (rare).", wantDbPerm}},
		{"auto-statistics", func(f *SetupFacts) { f.AutoUpdateStats = false },
			"AUTO_UPDATE_STATISTICS", "create ON, update OFF", "create ON, update ON",
			"ALTER DATABASE [WarehouseLive] SET AUTO_UPDATE_STATISTICS ON;",
			"ALTER DATABASE [WarehouseLive] SET AUTO_UPDATE_STATISTICS OFF;",
			[]string{"Check first that the application vendor does not require this (rare).", wantDbPerm}},
		{"indirect-checkpoints", func(f *SetupFacts) { f.TargetRecoverySec = 0 }, "TARGET_RECOVERY_TIME", "0 SECONDS", "60 SECONDS",
			"ALTER DATABASE [WarehouseLive] SET TARGET_RECOVERY_TIME = 60 SECONDS;", "ALTER DATABASE [WarehouseLive] SET TARGET_RECOVERY_TIME = 0 SECONDS;",
			[]string{wantDbPerm}},
	} {
		f := goodFacts()
		tc.mutate(&f)
		p, fix := mustFix(t, tc.id, f)
		if fix.Scope != ScopeDatabase || fix.Setting != tc.setting || fix.Current != tc.cur || fix.Target != tc.tgt {
			t.Errorf("%s fix %+v", tc.id, fix)
		}
		if p.Statement != tc.stmt || p.Rollback != tc.rollback {
			t.Errorf("%s statement/rollback\n got %s\n     %s", tc.id, p.Statement, p.Rollback)
		}
		if !reflect.DeepEqual(p.Warnings, tc.warnings) {
			t.Errorf("%s warnings %q", tc.id, p.Warnings)
		}
		if p.Database != "WarehouseLive" || len(p.Changes) == 0 {
			t.Errorf("%s preview %+v", tc.id, p)
		}
		// The value as the read-back shows it after the change is the target.
		o := f.Options()
		switch tc.id {
		case "auto-close":
			o.AutoClose = false
		case "auto-shrink":
			o.AutoShrink = false
		case "page-verify":
			o.PageVerify = "CHECKSUM"
		case "auto-statistics":
			o.AutoCreateStats, o.AutoUpdateStats = true, true
		case "indirect-checkpoints":
			o.TargetRecoverySec = 60
		}
		if DatabaseValue(tc.id, o) != fix.Target {
			t.Errorf("%s read back %q, target %q", tc.id, DatabaseValue(tc.id, o), fix.Target)
		}
		if d := FixChangeDescription(fix, f.ServerName, "WarehouseLive"); d != tc.setting+" on WarehouseLive: "+tc.cur+" -> "+tc.tgt {
			t.Errorf("%s description %q", tc.id, d)
		}
	}

	// Names are bracket-quoted, never taken from client input.
	f := goodFacts()
	f.AutoClose = true
	p, _, err := BuildFix("auto-close", f, "we]ird", nil)
	if err != nil || p.Statement != "ALTER DATABASE [we]]ird] SET AUTO_CLOSE OFF;" {
		t.Errorf("quoting: %v %s", err, p.Statement)
	}
	// An unexpected previous page-verify value is not put into a rollback.
	f = goodFacts()
	f.PageVerify = "BAD; DROP"
	p, _ = mustFix(t, "page-verify", f)
	if p.Rollback != "" || !strings.Contains(p.Warnings[0], "not in the rollback") {
		t.Errorf("odd page verify: %+v", p)
	}
	if two, _ := mustFix(t, "auto-statistics", func() SetupFacts { f := goodFacts(); f.AutoCreateStats, f.AutoUpdateStats = false, false; return f }()); len(two.Changes) != 2 {
		t.Errorf("auto-statistics changes %+v", two.Changes)
	}
}

func TestFixNotApplicable(t *testing.T) {
	f := goodFacts()
	for _, d := range FixDefs {
		if _, _, err := BuildFix(d.ID, f, "", nil); !errors.Is(err, ErrFixNotApplicable) {
			t.Errorf("%s on a healthy database: %v", d.ID, err)
		}
	}
	if _, _, err := BuildFix("drop-everything", f, "", nil); !errors.Is(err, ErrUnknownFix) {
		t.Errorf("unknown: %v", err)
	}
	// INS03's second case (below half of the memory) carries no fix.
	f.MaxServerMemoryMB = 8000
	if _, _, err := BuildFix("max-server-memory", f, "", nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("INS03 half: %v", err)
	}
	// MAXDOP 1 is Info, Decide first: no fix.
	f = goodFacts()
	f.MaxDop = 1
	if _, _, err := BuildFix("maxdop", f, "", nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("MAXDOP 1: %v", err)
	}
	// A database fix on a secondary replica is not offered.
	f = goodFacts()
	f.AutoClose, f.DB.IsSecondaryReplica = true, true
	if _, _, err := BuildFix("auto-close", f, "", nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("secondary: %v", err)
	}
	// Backup compression on Express is not a finding.
	f = goodFacts()
	f.BackupCompression, f.EngineEdition = 0, 4
	if _, _, err := BuildFix("backup-compression", f, "", nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("Express: %v", err)
	}
}

// The Query Store fix through the generic path is the phase 1 statement, with the permission line.
func TestQueryStoreFixThroughGenericPath(t *testing.T) {
	for _, q := range []QueryStoreStatus{
		offQS(true),
		driftQS(true, Drift{"INTERVAL_LENGTH_MINUTES", "60", "15"}, Drift{"MAX_STORAGE_SIZE_MB", "1000", "2048"}),
	} {
		f := goodFacts()
		phase1 := BuildQueryStoreFix(q.Options(), 16, "WarehouseLive")
		p, fix, err := BuildFix(FixQueryStore, f, "WarehouseLive", &q)
		if err != nil {
			t.Fatal(err)
		}
		if p.Statement != phase1.Statement || p.Rollback != phase1.Rollback || !reflect.DeepEqual(p.Changes, phase1.Changes) ||
			p.Statement == "" {
			t.Errorf("generic\n %s\nphase 1\n %s", p.Statement, phase1.Statement)
		}
		if !reflect.DeepEqual(p.Warnings[:len(p.Warnings)-1], phase1.Warnings) || p.Warnings[len(p.Warnings)-1] != wantDbPerm {
			t.Errorf("warnings %q", p.Warnings)
		}
		if fix.ID != FixQueryStore || fix.CheckID != "DBC10" || fix.Current != q.Summary || p.EngineVersion != "16.0.4255.3" {
			t.Errorf("fix %+v", fix)
		}
	}
	q := okQS()
	if _, _, err := BuildFix(FixQueryStore, goodFacts(), "WarehouseLive", &q); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("ok Query Store: %v", err)
	}
	q = offQS(false)
	if _, _, err := BuildFix(FixQueryStore, goodFacts(), "WarehouseLive", &q); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("Query Store that cannot be fixed: %v", err)
	}
}

func TestFixDefsMatchTheCatalogue(t *testing.T) {
	ids := []string{}
	for _, d := range FixDefs {
		ids = append(ids, d.ID)
		if c, ok := CheckByID(d.CheckID); !ok || (c.Card != CardSetup && c.Card != CardUpkeep && c.Card != CardBackups && c.Card != CardPerformance && c.Card != CardStorage && c.Card != CardDecisions && c.Card != CardDesign) || d.Card() != c.Card {
			t.Errorf("%s: check %s", d.ID, d.CheckID)
		}
	}
	want := "max-server-memory,maxdop,cost-threshold,optimize-adhoc,backup-compression,auto-close,auto-shrink,page-verify,auto-statistics,indirect-checkpoints,query-store,update-statistics,create-index,drop-index,disable-index,kill-session,rcsi,force-plan,unforce-plan,evict-plan,update-statistics-table,multi-user,reconfigure,trace-flag-off,scoped-config-reset,min-server-memory,operator-alerts,maintenance-job,remove-shrink-step,autogrowth,add-tempdb-files,recovery-model,checkdb-job,checkdb-now,suspect-pages-clear,tempdb-files,archive-table,remove-archive,history-cleanup,maintenance-plan,install-xe-session,remove-xe-session,enable-default-trace,backup-schedule,remove-backup-schedule"
	if strings.Join(ids, ",") != want {
		t.Errorf("fix ids %v", ids)
	}
	if ConfigValue("max-server-memory", 4096) != "4096 MB" || ConfigValue("maxdop", 4) != "4" {
		t.Error("ConfigValue")
	}
}

// A server fix's RECONFIGURE also activates whatever else is pending: the preview warns and the
// change description names them; its own setting and 'show advanced options' are not repeated.
func TestServerFixPendingReconfigure(t *testing.T) {
	f := goodFacts()
	f.CostThreshold = 5
	f.PendingConfig = append(f.PendingConfig,
		PendingConfig{Name: "cost threshold for parallelism", Value: 40, ValueInUse: 5},
		PendingConfig{Name: "show advanced options", Value: 0, ValueInUse: 1},
		PendingConfig{Name: "blocked process threshold (s)", Value: 5, ValueInUse: 0},
		PendingConfig{Name: "max degree of parallelism", Value: 4, ValueInUse: 8})
	p, fix := mustFix(t, "cost-threshold", f)
	want := []string{wantServer, warnPlanCache,
		"RECONFIGURE also activates these pending changes: blocked process threshold (s) 0 -> 5, max degree of parallelism 8 -> 4.",
		wantServerPerm}
	if !reflect.DeepEqual(p.Warnings, want) {
		t.Errorf("warnings %q", p.Warnings)
	}
	d := FixChangeDescription(fix, f.ServerName, "WarehouseLive", f.PendingOtherThan(fix.Setting)...)
	if d != `cost threshold for parallelism on SQL01\WMS: 5 -> 50; RECONFIGURE also activated: blocked process threshold (s) 0 -> 5, max degree of parallelism 8 -> 4` {
		t.Errorf("description %q", d)
	}
	// Every server fix carries the warning, max server memory included.
	f.MaxServerMemoryMB = 2147483647
	if p, _ := mustFix(t, "max-server-memory", f); !strings.HasPrefix(p.Warnings[len(p.Warnings)-2], "RECONFIGURE also activates these pending changes: cost threshold for parallelism 5 -> 40, blocked process") {
		t.Errorf("max server memory warnings %q", p.Warnings)
	}

	// Only its own setting pending: no warning.
	f = goodFacts()
	f.CostThreshold = 5
	f.PendingConfig = []PendingConfig{{Name: "Cost Threshold For Parallelism", Value: 40, ValueInUse: 5}}
	p, fix = mustFix(t, "cost-threshold", f)
	if len(p.Warnings) != 3 || FixChangeDescription(fix, f.ServerName, "", f.PendingOtherThan(fix.Setting)...) != `cost threshold for parallelism on SQL01\WMS: 5 -> 50` {
		t.Errorf("own setting: %q", p.Warnings)
	}

	// A database fix runs no RECONFIGURE: no warning, nothing in the description.
	f = goodFacts()
	f.AutoClose = true
	f.PendingConfig = []PendingConfig{{Name: "blocked process threshold (s)", Value: 5, ValueInUse: 0}}
	p, fix = mustFix(t, "auto-close", f)
	if !reflect.DeepEqual(p.Warnings, []string{wantDbPerm}) ||
		FixChangeDescription(fix, f.ServerName, "WarehouseLive", f.PendingOtherThan(fix.Setting)...) != "AUTO_CLOSE on WarehouseLive: ON -> OFF" {
		t.Errorf("database fix: %q", p.Warnings)
	}
}
