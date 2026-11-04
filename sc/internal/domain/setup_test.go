package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

var setupNow = time.Date(2026, 11, 3, 11, 21, 0, 0, time.UTC) // server clock, UTC+2

// goodFacts is a healthy SQL Server 2022 CU25 Standard instance and database: no finding but the
// INS01 Info summary.
func goodFacts() SetupFacts {
	q := okQS()
	return SetupFacts{
		ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", ProductLevel: "RTM", UpdateLevel: "CU25",
		Edition: "Standard Edition (64-bit)", EngineEdition: 2, Collation: "Latin1_General_CI_AS",
		CPUCount: 8, PhysicalMB: 32768, StartTime: setupNow.Add(-75 * time.Hour), CoresPerNode: 8,
		MemoryModel: ptr("CONVENTIONAL"), IFI: ptr("Y"),
		MaxServerMemoryMB: 28672, MaxDop: 8, CostThreshold: 50, OptimizeAdHoc: 1, BackupCompression: 1, ClrEnabled: 1,
		ClrStrict: ptr(1), PlanCacheMB: 500, SingleUseMB: 50,
		MemoryState: "Available physical memory is high", AvailableMB: 4000, PowerScheme: ptr(powerHigh),
		MinServerMemoryMB: 16, PendingConfig: []PendingConfig{{Name: "min server memory (MB)", Value: 0, ValueInUse: 16}}, TraceFlags: []int{},
		Database: "WarehouseLive", State: "ONLINE", UserAccess: "MULTI_USER", PageVerify: "CHECKSUM", AutoCreateStats: true, AutoUpdateStats: true,
		CompatLevel: 160, TargetRecoverySec: 60, DbCollation: "Latin1_General_CI_AS",
		DB:           DatabaseStatus{Name: "WarehouseLive", State: "ONLINE", QueryStore: &q},
		DataFileName: "WarehouseLive", LegacyCE: ptr(0), Orphans: []OrphanUser{},
		ScopedConfigs: []ScopedConfig{{"MAXDOP", "0"}, {"LEGACY_CARDINALITY_ESTIMATION", "0"}, {"PARAMETER_SNIFFING", "1"},
			{"QUERY_OPTIMIZER_HOTFIXES", "0"}, {"DOP_FEEDBACK", "0"}, {"ELEVATE_ONLINE", "OFF"}},
		ServerNow: setupNow, UTCOffsetMinutes: 120,
	}
}

func setupDef() CardDef { d, _ := FindCardDef(CardSetup); return d }

func runSetup(f SetupFacts) (Card, []Finding, []CheckStatus) { return BuildSetupCard(f, setupDef()) }

func findingsOf(fs []Finding, id string) []Finding {
	out := []Finding{}
	for _, x := range fs {
		if x.CheckID == id {
			out = append(out, x)
		}
	}
	return out
}

func statusOf(cs []CheckStatus, id string) CheckStatus {
	for _, c := range cs {
		if c.CheckID == id {
			return c
		}
	}
	return CheckStatus{}
}

// one asserts that check id fired exactly once and returns it.
func one(t *testing.T, fs []Finding, id string) Finding {
	t.Helper()
	got := findingsOf(fs, id)
	if len(got) != 1 {
		t.Fatalf("%s: want 1 finding, got %d: %+v", id, len(got), fs)
	}
	return got[0]
}

func none(t *testing.T, fs []Finding, id string) {
	t.Helper()
	if got := findingsOf(fs, id); len(got) != 0 {
		t.Errorf("%s: want no finding, got %+v", id, got)
	}
}

func TestSetupGreen(t *testing.T) {
	c, fs, cs := runSetup(goodFacts())
	if c.Status != StatusGreen || c.StatusText != "No problem found" || c.ToDo+c.ForClientIT+c.Fixable != 0 {
		t.Errorf("green: %+v", c)
	}
	want := "SQL Server 2022 Standard Edition (64-bit), build 16.0.4255.3 (RTM CU25). 8 logical CPUs, 32 GB RAM, running for 3 days."
	if c.Verdict != want {
		t.Errorf("verdict %q", c.Verdict)
	}
	if c.AsOf == nil || !c.AsOf.Equal(time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)) {
		t.Errorf("asOf %v", c.AsOf)
	}
	wantFacts(t, c, "Version=SQL Server 2022 RTM CU25, up to date/normal", "Memory=28 of 32 GB cap/normal",
		"Parallelism=MAXDOP 8, cost threshold 50/normal", "Query Store=On, 3% full, 15-min intervals, 30 days/normal")
	if len(fs) != 1 {
		t.Fatalf("findings: %+v", fs)
	}
	x := fs[0]
	if x.CheckID != "INS01" || x.Severity != SeverityInfo || x.Finding != "Instance summary." || x.Object != `SQL01\WMS` ||
		x.DoThis != "Up to date with the latest known cumulative update (CU25 (May 2026))." || x.Evidence != want ||
		x.Owner != OwnerClientIT || x.When != WhenNone || x.Effort != EffortNone || x.FixID != "" {
		t.Errorf("INS01 summary: %+v", x)
	}
	ids := []string{}
	for _, s := range cs {
		ids = append(ids, s.CheckID)
		if s.Status != CheckPassed {
			t.Errorf("check %+v", s)
		}
	}
	if strings.Join(ids, ",") != "INS01,INS02,INS03,INS04,INS05,INS06,INS07,INS08,INS09,INS10,INS11,INS12,INS13,INS14,INS15,INS16,"+
		"DBC02,DBC03,DBC04,DBC05,DBC06,DBC07,DBC09,DBC10,DBC11,DBC13,DBC14,CFG01,CPY01,CPY02,CPY04,CPY06" {
		t.Errorf("checks %v", ids)
	}
	if fixes := AvailableFixes(goodFacts(), fs); len(fixes) != 0 {
		t.Errorf("fixes %+v", fixes)
	}
}

func TestSetupGreyCards(t *testing.T) {
	f := goodFacts()
	f.Error = "could not read the setup facts: boom"
	c, fs, cs := runSetup(f)
	if c.Status != StatusGrey || c.StatusText != "Could not be read" || c.Note != f.Error || len(fs) != 0 || len(cs) != 32 ||
		c.Facts == nil || len(c.Facts) != 0 || c.AsOf != nil {
		t.Errorf("error: %+v", c)
	}
	f = goodFacts()
	f.State = "RESTORING"
	c, _, cs = runSetup(f)
	if c.Status != StatusGrey || c.Note != "WarehouseLive is RESTORING, not online, so its setup was not checked." ||
		cs[0].Status != CheckSkipped {
		t.Errorf("restoring: %+v %+v", c, cs[0])
	}
}

func TestINS01(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		now           time.Time
		sev           Severity
		finding, fact string
	}{
		{"out of support", "13.0.7055.1", setupNow, SeverityHigh,
			"SQL Server 2016 is out of Microsoft support (extended support ended 2026-07-14).", "out of support/worse"},
		{"unknown old", "10.50.6000.34", setupNow, SeverityHigh, "SQL Server v10 is out of Microsoft support.", "out of support/worse"},
		{"behind, recent", "16.0.4105.2", setupNow, SeverityInfo,
			"Build 16.0.4105.2 is behind the latest update for SQL Server 2022: CU25 (May 2026) (build 16.0.4255).", "6 months behind/bad"},
		{"behind, 11 months", "16.0.4105.2", time.Date(2027, 4, 30, 9, 0, 0, 0, time.UTC), SeverityInfo, "", "11 months behind/bad"},
		{"behind, 12 months", "16.0.4105.2", time.Date(2027, 5, 1, 9, 0, 0, 0, time.UTC), SeverityLow, "", "12 months behind/bad"},
		{"behind, mainstream ended", "15.0.4400.1", setupNow, SeverityLow,
			"Build 15.0.4400.1 is behind the latest update for SQL Server 2019: CU32 GDR (May 2026) (build 15.0.4470). Mainstream support ended 2025-01-07; only security fixes until 2030-01-08.",
			"6 months behind/bad"},
		{"newer than the table", "18.0.100.1", setupNow, SeverityInfo, "Instance summary.", "newer than the build table/normal"},
	} {
		f := goodFacts()
		f.ProductVersion, f.ServerNow = tc.version, tc.now
		f.CompatLevel = EngineMajor(tc.version) * 10
		c, fs, _ := runSetup(f)
		x := one(t, fs, "INS01")
		if x.Severity != tc.sev || (tc.finding != "" && x.Finding != tc.finding) {
			t.Errorf("%s: %s %q", tc.name, x.Severity, x.Finding)
		}
		if got := c.Facts[0].Value + "/" + c.Facts[0].Tone; !strings.HasSuffix(got, tc.fact) {
			t.Errorf("%s: version fact %q", tc.name, got)
		}
		switch {
		case tc.sev == SeverityHigh:
			if x.When != WhenDecideFirst || x.Effort != EffortProject || x.Why != ins01OutWhy || x.DoThis != ins01OutDo {
				t.Errorf("%s: %+v", tc.name, x)
			}
		case strings.HasPrefix(x.Finding, "Build"):
			if x.When != WhenWindow || x.Effort != EffortModerate || x.DoThis != ins01BehindDo ||
				!strings.HasSuffix(x.Evidence, "(build table refreshed 2026-11-02).") {
				t.Errorf("%s: %+v", tc.name, x)
			}
		default:
			if x.DoThis != ins01NewerDo {
				t.Errorf("%s: %+v", tc.name, x)
			}
		}
	}
}

func TestINS02AndINS03(t *testing.T) {
	f := goodFacts()
	f.MaxServerMemoryMB = 2147483647
	c, fs, _ := runSetup(f)
	x := one(t, fs, "INS02")
	if x.Severity != SeverityHigh || x.FixID != "max-server-memory" || x.When != WhenOnline || x.Effort != EffortQuickWin ||
		x.Evidence != "Physical memory: 32768 MB. Max server memory: unlimited." ||
		x.DoThis != "Leave room for the operating system: EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', 28672; RECONFIGURE;  Use a lower value if other applications run on this server." {
		t.Errorf("INS02: %+v", x)
	}
	if c.Facts[1].Value != "no cap of 32 GB" || c.Facts[1].Tone != ToneBad || c.Fixable != 1 || c.Verdict != x.Finding {
		t.Errorf("card %+v", c)
	}
	f.EngineEdition = 4
	if x := one(t, func() []Finding { _, fs, _ := runSetup(f); return fs }(), "INS02"); x.Severity != SeverityLow {
		t.Errorf("Express INS02: %s", x.Severity)
	}

	f = goodFacts()
	f.MaxServerMemoryMB = 30721 // leaves 2047 MB
	_, fs, _ = runSetup(f)
	x = one(t, fs, "INS03")
	if x.Severity != SeverityMedium || x.FixID != "max-server-memory" ||
		x.DoThis != "Lower max server memory to about 28672 MB: EXEC sp_configure 'max server memory (MB)', 28672; RECONFIGURE;" {
		t.Errorf("INS03 low: %+v", x)
	}
	f.MaxServerMemoryMB = 30720 // leaves exactly 2048
	_, fs, _ = runSetup(f)
	none(t, fs, "INS03")

	f.MaxServerMemoryMB = 16383 // below half of 32 GB
	c, fs, _ = runSetup(f)
	x = one(t, fs, "INS03")
	if x.Severity != SeverityLow || x.FixID != "" || c.Facts[1].Value != "16.0 of 32 GB cap" {
		t.Errorf("INS03 half: %+v %+v", x, c.Facts[1])
	}
	f.MaxServerMemoryMB = 16384
	_, fs, _ = runSetup(f)
	none(t, fs, "INS03")
	f.MaxServerMemoryMB, f.EngineEdition = 8000, 4
	_, fs, _ = runSetup(f)
	none(t, fs, "INS03")
}

func TestINS04AndINS05(t *testing.T) {
	f := goodFacts()
	f.MaxDop, f.CPUCount = 0, 16
	c, fs, _ := runSetup(f)
	x := one(t, fs, "INS04")
	if x.Severity != SeverityMedium || x.FixID != "maxdop" || x.Finding != "MAXDOP is 0, so one query can use all 16 CPUs." ||
		x.DoThis != "EXEC sp_configure 'max degree of parallelism', 8; RECONFIGURE;" || x.Evidence != "Logical CPUs: 16, per NUMA node: 8." {
		t.Errorf("INS04 zero: %+v", x)
	}
	if c.Facts[2].Value != "MAXDOP 0, cost threshold 50" || c.Facts[2].Tone != ToneBad {
		t.Errorf("parallelism fact %+v", c.Facts[2])
	}
	f.CPUCount = 8 // 0 on 8 CPUs is not a finding
	_, fs, _ = runSetup(f)
	none(t, fs, "INS04")

	f = goodFacts()
	f.MaxDop, f.CoresPerNode = 8, 4
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS04"); x.Severity != SeverityLow || x.FixID != "maxdop" || x.Finding != "MAXDOP is 8, above the recommended 4." {
		t.Errorf("INS04 above: %+v", x)
	}

	f = goodFacts()
	f.MaxDop = 1
	c, fs, cs := runSetup(f)
	x = one(t, fs, "INS04")
	if x.Severity != SeverityInfo || x.FixID != "" || x.When != WhenDecideFirst || x.Effort != EffortNone ||
		x.DoThis != "Confirm whether a vendor requires MAXDOP 1. If not, consider 8 together with a cost threshold of 50 (INS05)." {
		t.Errorf("INS04 one: %+v", x)
	}
	if statusOf(cs, "INS04").Status != CheckPassed || c.Facts[2].Tone != ToneNormal {
		t.Errorf("an Info INS04 is not a finding to act on: %+v", statusOf(cs, "INS04"))
	}

	f = goodFacts()
	f.CostThreshold = 5
	_, fs, cs = runSetup(f)
	x = one(t, fs, "INS05")
	if x.Severity != SeverityMedium || x.FixID != "cost-threshold" || x.Evidence != "Current value: 5." || x.DoThis != ins05Do ||
		statusOf(cs, "INS05").Status != CheckFinding {
		t.Errorf("INS05: %+v", x)
	}
	f.CostThreshold = 6
	_, fs, _ = runSetup(f)
	none(t, fs, "INS05")
	f.CostThreshold, f.MaxDop = 5, 1
	_, fs, _ = runSetup(f)
	none(t, fs, "INS05")
}

func TestINS06ToINS11(t *testing.T) {
	f := goodFacts()
	f.OptimizeAdHoc, f.PlanCacheMB, f.SingleUseMB = 0, 400, 150
	_, fs, _ := runSetup(f)
	if x := one(t, fs, "INS06"); x.Severity != SeverityMedium || x.FixID != "optimize-adhoc" ||
		x.Evidence != "Single-use plans: 150.0 MB of 400.0 MB plan cache." {
		t.Errorf("INS06 medium: %+v", x)
	}
	f.SingleUseMB = 100 // not more than 25% of 400
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS06"); x.Severity != SeverityLow {
		t.Errorf("INS06 low: %+v", x)
	}
	f.PlanCacheMB, f.SingleUseMB = 300, 99.9
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS06"); x.Severity != SeverityLow {
		t.Errorf("INS06 under 100 MB: %+v", x)
	}
	f.PlanCacheError = "denied"
	_, fs, cs := runSetup(f)
	none(t, fs, "INS06")
	if s := statusOf(cs, "INS06"); s.Status != CheckSkipped || !strings.Contains(s.Note, "denied") {
		t.Errorf("INS06 skipped: %+v", s)
	}

	f = goodFacts()
	f.IFI = ptr("N")
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS07"); x.Severity != SeverityMedium || x.When != WhenWindow || x.Owner != OwnerClientIT || x.FixID != "" {
		t.Errorf("INS07: %+v", x)
	}
	f.IFI = nil
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "INS07"); s.Status != CheckSkipped || s.Note != ins07Skipped {
		t.Errorf("INS07 skipped: %+v", s)
	}

	f = goodFacts()
	f.PhysicalMB, f.MaxServerMemoryMB = 65536, 58982
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS08"); x.Severity != SeverityLow || x.Evidence != "Memory model: CONVENTIONAL. Physical memory: 64 GB." {
		t.Errorf("INS08: %+v", x)
	}
	f.PhysicalMB = 65535
	_, fs, _ = runSetup(f)
	none(t, fs, "INS08")
	f.MemoryModel = nil
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "INS08"); s.Status != CheckSkipped || s.Note != ins08Skipped {
		t.Errorf("INS08 skipped: %+v", s)
	}

	f = goodFacts()
	f.PriorityBoost = 1
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS09"); x.Severity != SeverityHigh || x.When != WhenWindow || x.FixID != "" || x.DoThis != ins09Do {
		t.Errorf("INS09: %+v", x)
	}

	f = goodFacts()
	f.BackupCompression = 0
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS10"); x.Severity != SeverityLow || x.FixID != "backup-compression" {
		t.Errorf("INS10: %+v", x)
	}
	f.EngineEdition = 4
	_, fs, _ = runSetup(f)
	none(t, fs, "INS10")

	f = goodFacts()
	f.FillFactor = 80
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS11"); x.Severity != SeverityLow || x.Finding != "Server-wide fill factor is 80%." ||
		x.Evidence != "fill factor (%) = 80" || x.When != WhenWindow || x.Effort != EffortModerate {
		t.Errorf("INS11: %+v", x)
	}
	f.FillFactor = 100
	_, fs, _ = runSetup(f)
	none(t, fs, "INS11")
}

func TestINS12AndINS13(t *testing.T) {
	f := goodFacts()
	f.MemoryState, f.AvailableMB = "Available physical memory is low", 1234
	_, fs, _ := runSetup(f)
	if x := one(t, fs, "INS12"); x.Severity != SeverityHigh || x.Finding != "Windows is low on physical memory right now (1,234 MB free)." ||
		x.Evidence != "Memory state: Available physical memory is low. Available: 1,234 MB. Last low-memory signal: none in the last 7 days." {
		t.Errorf("INS12 now: %+v", x)
	}
	f = goodFacts()
	f.Low7, f.LowLast = 3, ptr(time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC))
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS12"); x.Severity != SeverityMedium || x.Finding != "Windows signalled low physical memory 3 time(s) in the last 7 days." ||
		!strings.HasSuffix(x.Evidence, "Last low-memory signal: 2026-10-30 14:05.") || x.When != WhenDecideFirst {
		t.Errorf("INS12 week: %+v", x)
	}
	f.Low24 = 1
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS12"); x.Severity != SeverityHigh || !strings.HasSuffix(x.Finding, ", 1 of them in the last 24 hours.") {
		t.Errorf("INS12 day: %+v", x)
	}
	f.MemoryError = "denied"
	_, _, cs := runSetup(f)
	if s := statusOf(cs, "INS12"); s.Status != CheckSkipped || s.Note != "The memory pressure check could not run: denied" {
		t.Errorf("INS12 skipped: %+v", s)
	}

	f = goodFacts()
	f.PowerScheme = ptr(strings.ToUpper(powerBalanced))
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS13"); x.Severity != SeverityLow || x.Finding != "The Windows power plan is Balanced." || x.Owner != OwnerClientIT {
		t.Errorf("INS13: %+v", x)
	}
	f.PowerScheme = ptr("00000000-0000-0000-0000-000000000000")
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS13"); x.Finding != "The Windows power plan is not High performance." {
		t.Errorf("INS13 other: %+v", x)
	}
	f.PowerScheme = ptr(powerUltimate)
	_, fs, _ = runSetup(f)
	none(t, fs, "INS13")
	f.PowerScheme = nil
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "INS13"); s.Status != CheckSkipped || s.Note != ins13Skipped {
		t.Errorf("INS13 skipped: %+v", s)
	}
	f.PowerError = "EXECUTE permission was denied"
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "INS13"); s.Note != "Could not read the power plan: EXECUTE permission was denied" {
		t.Errorf("INS13 error: %+v", s)
	}
}

func TestInstanceAreasNotRead(t *testing.T) {
	f := goodFacts()
	f.SysInfoError = "the login does not have VIEW SERVER STATE."
	c, _, cs := runSetup(f)
	for _, id := range []string{"INS02", "INS03", "INS04", "INS05", "INS08"} {
		if s := statusOf(cs, id); s.Status != CheckSkipped || !strings.HasPrefix(s.Note, sysInfoNotRead) {
			t.Errorf("%s: %+v", id, s)
		}
	}
	if c.Facts[1].Value != "could not be read" || strings.Contains(c.Verdict, "logical CPUs") {
		t.Errorf("card without sys info: %+v", c)
	}
	f = goodFacts()
	f.ConfigError = "denied"
	c, _, cs = runSetup(f)
	if s := statusOf(cs, "INS10"); s.Status != CheckSkipped || c.Facts[2].Value != "could not be read" {
		t.Errorf("config not read: %+v %+v", s, c.Facts)
	}
}

func TestDatabaseOptionChecks(t *testing.T) {
	f := goodFacts()
	f.AutoClose, f.AutoShrink, f.PageVerify, f.AutoCreateStats, f.AutoUpdateStats = true, true, "TORN_PAGE_DETECTION", false, false
	c, fs, _ := runSetup(f)
	for id, want := range map[string]Finding{
		"DBC02": {Severity: SeverityHigh, FixID: "auto-close", Finding: "AUTO_CLOSE is on.", DoThis: "ALTER DATABASE [WarehouseLive] SET AUTO_CLOSE OFF;"},
		"DBC03": {Severity: SeverityHigh, FixID: "auto-shrink", Finding: "AUTO_SHRINK is on.", DoThis: "ALTER DATABASE [WarehouseLive] SET AUTO_SHRINK OFF;"},
		"DBC04": {Severity: SeverityMedium, FixID: "page-verify", Finding: "Page verify is TORN_PAGE_DETECTION instead of CHECKSUM.",
			DoThis: "ALTER DATABASE [WarehouseLive] SET PAGE_VERIFY CHECKSUM;  (Pages are protected as they are next written; an index rebuild protects them all.)"},
		"DBC05": {Severity: SeverityHigh, FixID: "auto-statistics", Finding: "Automatic creation and updating of statistics are both off.",
			DoThis: "Check first that the application vendor does not require this (rare), then: ALTER DATABASE [WarehouseLive] SET AUTO_CREATE_STATISTICS ON; ALTER DATABASE [WarehouseLive] SET AUTO_UPDATE_STATISTICS ON;"},
	} {
		x := one(t, fs, id)
		if x.Severity != want.Severity || x.FixID != want.FixID || x.Finding != want.Finding || x.DoThis != want.DoThis ||
			x.Object != "WarehouseLive" || x.When != WhenOnline || x.Effort != EffortQuickWin || x.Owner != OwnerUs {
			t.Errorf("%s: %+v", id, x)
		}
	}
	if c.ToDo != 4 || c.Fixable != 4 || c.ForClientIT != 0 || c.StatusText != "4 things to do" || c.Status != StatusAmber {
		t.Errorf("card %+v", c)
	}
	f.AutoCreateStats = true
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC05"); x.Finding != "Automatic updating of statistics is off." || x.Evidence != "auto create = 1, auto update = 0" {
		t.Errorf("DBC05 update only: %+v", x)
	}
	f.AutoCreateStats, f.AutoUpdateStats = false, true
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC05"); x.Finding != "Automatic creation of statistics is off." {
		t.Errorf("DBC05 create only: %+v", x)
	}

	// A read-only database keeps its findings but offers no fix.
	f = goodFacts()
	f.AutoClose, f.DB.ReadOnly = true, true
	c, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC02"); x.FixID != "" || c.Fixable != 0 {
		t.Errorf("read-only DBC02: %+v", x)
	}
}

func TestDatabaseOtherChecks(t *testing.T) {
	f := goodFacts()
	f.CompatLevel = 110
	_, fs, _ := runSetup(f)
	if x := one(t, fs, "DBC06"); x.Severity != SeverityMedium || x.Owner != OwnerVendor || x.When != WhenDecideFirst ||
		x.Finding != "Compatibility level is 110; this server supports 160." || x.FixID != "" {
		t.Errorf("DBC06: %+v", x)
	}
	f.CompatLevel = 150
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC06"); x.Severity != SeverityLow {
		t.Errorf("DBC06 150: %+v", x)
	}

	f = goodFacts()
	f.OwnerMissing = true
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC07"); x.Severity != SeverityLow || x.FixID != "" {
		t.Errorf("DBC07: %+v", x)
	}
	f.Trustworthy = true
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC07"); x.Severity != SeverityMedium {
		t.Errorf("DBC07 trustworthy: %+v", x)
	}
	if x := one(t, fs, "DBC11"); x.Severity != SeverityLow || x.When != WhenDecideFirst || x.Effort != EffortModerate {
		t.Errorf("DBC11: %+v", x)
	}

	f = goodFacts()
	f.TargetRecoverySec = 0
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC09"); x.Severity != SeverityLow || x.FixID != "indirect-checkpoints" {
		t.Errorf("DBC09: %+v", x)
	}
	f.TargetRecoverySec = -1 // not shown
	_, fs, cs := runSetup(f)
	none(t, fs, "DBC09")
	if s := statusOf(cs, "DBC09"); s.Status != CheckSkipped || s.Note != dbc09NotShown {
		t.Errorf("DBC09 not shown: %+v", s)
	}
	f.DbCollation = ""
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "CPY02"); s.Status != CheckSkipped || s.Note != cpy02NotShown {
		t.Errorf("CPY02 not shown: %+v", s)
	}
	f.TargetRecoverySec = 0
	f.ProductVersion, f.CompatLevel = "12.0.6449.1", 120
	_, fs, cs = runSetup(f)
	none(t, fs, "DBC09")
	if s := statusOf(cs, "DBC09"); s.Status != CheckSkipped {
		t.Errorf("DBC09 on 2014: %+v", s)
	}

	f = goodFacts()
	f.LegacyCE = ptr(1)
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "CFG01"); x.Severity != SeverityLow || x.When != WhenDecideFirst || x.DoThis != cfg01Do {
		t.Errorf("CFG01: %+v", x)
	}
	f.LegacyCE = nil
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "CFG01"); s.Status != CheckSkipped {
		t.Errorf("CFG01 before 2016: %+v", s)
	}
}

func TestDBC10(t *testing.T) {
	f := goodFacts()
	q := offQS(true)
	f.DB.QueryStore = &q
	c, fs, _ := runSetup(f)
	x := one(t, fs, "DBC10")
	if x.Severity != SeverityLow || x.FixID != "query-store" || x.Why != dbc10Why || x.Evidence != "Storage used: 0 MB of 100 MB." ||
		x.Finding != "Query Store is off on WarehouseLive, so its query history is lost at every restart." ||
		!strings.HasPrefix(x.DoThis, "Apply the recommended Query Store settings: ALTER DATABASE [WarehouseLive] SET QUERY_STORE = ON (") {
		t.Errorf("DBC10 off: %+v", x)
	}
	if c.Facts[3].Value != "Off" || c.Facts[3].Tone != ToneBad || c.Verdict != x.Finding {
		t.Errorf("card %+v", c)
	}

	q = driftQS(true, Drift{"INTERVAL_LENGTH_MINUTES", "60", "15"}, Drift{"MAX_STORAGE_SIZE_MB", "1000", "2048"})
	f.DB.QueryStore = &q
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC10"); x.Severity != SeverityMedium ||
		x.Finding != "Query Store is on but differs from the recommended settings on WarehouseLive: INTERVAL_LENGTH_MINUTES is 60, recommended 15." {
		t.Errorf("DBC10 drift: %+v", x)
	}
	q.CanFix = false
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC10"); x.FixID != "" {
		t.Errorf("DBC10 cannot fix: %+v", x)
	}

	q.Verdict = VerdictUnavailable
	f.DB.ReadOnly = true
	_, fs, cs := runSetup(f)
	none(t, fs, "DBC10")
	if s := statusOf(cs, "DBC10"); s.Status != CheckSkipped || s.Note != "WarehouseLive is read-only, so Query Store cannot be changed." {
		t.Errorf("DBC10 unavailable: %+v", s)
	}

	f = goodFacts()
	f.DB.QueryStore, f.DB.Error = nil, "could not read the Query Store options: boom"
	c, _, cs = runSetup(f)
	if s := statusOf(cs, "DBC10"); s.Status != CheckSkipped || !strings.HasSuffix(s.Note, "boom") || c.Facts[3].Value != "could not be read" {
		t.Errorf("DBC10 not read: %+v", s)
	}
}

func TestCopiedDatabaseChecks(t *testing.T) {
	f := goodFacts()
	f.RestoredAt, f.SourceServer, f.SourceDb = ptr(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)), "CLIENTSQL01", "WarehouseProd"
	f.SourceBackupAt = ptr(time.Date(2026, 8, 31, 22, 15, 0, 0, time.UTC))
	_, fs, _ := runSetup(f)
	if x := one(t, fs, "CPY01"); x.Severity != SeverityInfo || x.When != WhenNone ||
		x.Finding != "This database is a copy of WarehouseProd on CLIENTSQL01, restored on 2026-09-01." ||
		x.Evidence != "Backup taken: 2026-08-31 22:15. First data file name: WarehouseLive." {
		t.Errorf("CPY01 restored: %+v", x)
	}
	f.SourceServer, f.SourceDb = `SQL01\WMS`, "WarehouseLive"
	_, fs, _ = runSetup(f)
	none(t, fs, "CPY01")

	f = goodFacts()
	f.DataFileName = "Warehouse_Ortho"
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "CPY01"); x.Finding != "The file names suggest this database was copied from Warehouse_Ortho." {
		t.Errorf("CPY01 file name: %+v", x)
	}
	f.RestoreError = "denied"
	_, _, cs := runSetup(f)
	if s := statusOf(cs, "CPY01"); s.Status != CheckSkipped || s.Note != cpy01MsdbNote {
		t.Errorf("CPY01 msdb: %+v", s)
	}

	f = goodFacts()
	f.DbCollation, f.OtherCollationCt = "SQL_Latin1_General_CP1_CI_AS", 3
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "CPY02"); x.Severity != SeverityMedium || x.When != WhenDecideFirst || x.Effort != EffortProject ||
		x.Finding != "The database collation (SQL_Latin1_General_CP1_CI_AS) is different from the server and TempDB (Latin1_General_CI_AS)." ||
		x.Evidence != "Text columns in this database with yet another collation: 3." {
		t.Errorf("CPY02: %+v", x)
	}
	f.SameCollationDb = "Sage300"
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "CPY02"); x.Severity != SeverityInfo || !strings.HasSuffix(x.Finding, "It matches Sage300, which this database's code reads from, so it is most likely deliberate.") {
		t.Errorf("CPY02 deliberate: %+v", x)
	}

	f = goodFacts()
	f.Orphans = []OrphanUser{{Name: "appsvc", DefaultSchema: "dbo", LoginExists: true}, {Name: "old"}}
	c, fs, _ := runSetup(f)
	got := findingsOf(fs, "CPY04")
	if len(got) != 2 || got[0].Severity != SeverityMedium ||
		got[0].DoThis != "A login with the same name exists. Link them: ALTER USER [appsvc] WITH LOGIN = [appsvc];" ||
		got[1].DoThis != cpy04NoDo || got[1].Evidence != "Type: SQL user. Default schema: -." || c.ToDo != 2 {
		t.Errorf("CPY04: %+v", got)
	}

	f = goodFacts()
	f.Assemblies, f.SafeAssemblies, f.ClrEnabled = 2, 2, 0
	_, fs, _ = runSetup(f)
	got = findingsOf(fs, "CPY06")
	if len(got) != 2 || got[0].Severity != SeverityHigh || got[1].Severity != SeverityMedium ||
		got[1].Evidence != "Assemblies needing more than SAFE: 2. clr strict security = 1." {
		t.Errorf("CPY06 off and strict: %+v", got)
	}
	f.ClrEnabled, f.ClrStrict = 1, ptr(0)
	_, fs, _ = runSetup(f)
	none(t, fs, "CPY06")
	f.SafeAssemblies = 1
	_, fs, _ = runSetup(f)
	one(t, fs, "CPY06")
	f.ClrLoaded = true
	_, fs, _ = runSetup(f)
	none(t, fs, "CPY06")
}

func TestSetupCountsAndClientIT(t *testing.T) {
	f := goodFacts()
	f.IFI, f.PowerScheme, f.CostThreshold, f.AutoClose = ptr("N"), ptr(powerBalanced), 5, true
	c, fs, _ := runSetup(f)
	if c.ToDo != 4 || c.ForClientIT != 2 || c.Fixable != 2 {
		t.Errorf("counts %+v", c)
	}
	// Ranked: High DBC02, then Medium quick wins online before window.
	if fs[0].CheckID != "DBC02" || fs[1].CheckID != "INS05" || fs[2].CheckID != "INS07" {
		t.Errorf("rank %s %s %s", fs[0].CheckID, fs[1].CheckID, fs[2].CheckID)
	}
	fixes := AvailableFixes(f, fs)
	if len(fixes) != 2 || fixes[0].ID != "auto-close" || fixes[1].ID != "cost-threshold" {
		t.Errorf("fixes %+v", fixes)
	}
}

func TestSetupDetailJSON(t *testing.T) {
	f := goodFacts()
	f.AutoClose = true
	c, fs, cs := runSetup(f)
	b, _ := json.Marshal(CardDetail{Card: c, Findings: fs, Checks: cs, Fixes: AvailableFixes(f, fs)})
	s := string(b)
	if !strings.Contains(s, `"fixId":"auto-close"`) || !strings.Contains(s, `"fixes":[{"id":"auto-close","title":"AUTO_CLOSE off","scope":"database","checkId":"DBC02","setting":"AUTO_CLOSE","current":"ON","target":"OFF","needsRestart":false}]`) ||
		strings.Count(s, `"fixId"`) != 1 || strings.Contains(s, "checkdbRoom") {
		t.Errorf("setup detail JSON: %s", s)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardSetup}, Findings: []Finding{}, Checks: []CheckStatus{}})
	if !strings.Contains(string(b), `"fixes":[]`) {
		t.Errorf("no fixes: %s", b)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardBackups}, Findings: []Finding{}, Checks: []CheckStatus{}})
	if strings.Contains(string(b), "fixes") {
		t.Errorf("backups carries fixes: %s", b)
	}
}

func TestBuildCardsWithSetup(t *testing.T) {
	f := goodFacts()
	cards := BuildCards(&f, nil, nil, nil, nil, nil)
	if cards[1].ID != CardSetup || cards[1].Status != StatusGreen || len(cards[1].Facts) != 4 {
		t.Errorf("setup card %+v", cards[1])
	}
}

func TestSetupCatalogue(t *testing.T) {
	checks := ChecksFor(CardSetup)
	if len(checks) != 32 {
		t.Fatalf("setup checks: %d", len(checks))
	}
	for _, c := range checks {
		if c.Title == "" || c.Question == "" || c.Owner == "" || c.ThenCheck == "" {
			t.Errorf("check %+v", c)
		}
	}
	if c, _ := CheckByID("CPY04"); c.Owner != OwnerUs || c.Card != CardSetup {
		t.Errorf("CPY04 %+v", c)
	}
	if b, ok := buildFor(16); !ok || b.LatestMinor != 4255 || BuildTableRefreshed != "2026-11-02" || len(BuildTable) != 7 {
		t.Errorf("build table %+v", b)
	}
}

func TestSuggestions(t *testing.T) {
	for phys, want := range map[int64]int64{8192: 6144, 16384: 12288, 32768: 28672, 65536: 58983, 131072: 117965} {
		if got := SuggestedMaxMemoryMB(phys); got != want {
			t.Errorf("suggested for %d: %d, want %d", phys, got, want)
		}
	}
	if versionPart("16.0.4255.3", 2) != 4255 || versionPart("16.0.4255.3", 4) != 16 || versionPart("x", 2) != -1 {
		t.Error("versionPart")
	}
	if monthsBetween(time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) != 1 {
		t.Error("monthsBetween counts boundaries")
	}
}

func TestINS14(t *testing.T) {
	f := goodFacts()
	f.PendingConfig = append(f.PendingConfig, PendingConfig{Name: "cost threshold for parallelism", Value: 50, ValueInUse: 5})
	_, fs, cs := runSetup(f)
	x := one(t, fs, "INS14")
	if x.Severity != SeverityMedium || x.Owner != OwnerUs || x.When != WhenDecideFirst || x.FixID != FixReconfigure || x.Object != `SQL01\WMS` ||
		x.Finding != "1 server setting has a changed value that has not taken effect: cost threshold for parallelism (5 -> 50)." ||
		x.Why != ins14Why || !strings.HasSuffix(x.DoThis, "(no RECONFIGURE needed): EXEC sp_configure 'cost threshold for parallelism', 5;") ||
		x.Evidence != "sys.configurations: cost threshold for parallelism: value_in_use 5, value 50." ||
		statusOf(cs, "INS14").Status != CheckFinding {
		t.Errorf("INS14 one: %+v", x)
	}
	f.PendingConfig = append(f.PendingConfig, PendingConfig{Name: "max server memory (MB)", Value: 20000, ValueInUse: 28672})
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS14"); x.Finding != "2 server settings have a changed value that has not taken effect: "+
		"cost threshold for parallelism (5 -> 50), max server memory (MB) (28672 -> 20000)." {
		t.Errorf("INS14 two: %q", x.Finding)
	}
	// min server memory configured 0 shows 16 in use: not pending (goodFacts carries it).
	_, fs, _ = runSetup(goodFacts())
	none(t, fs, "INS14")
	f.ConfigError = "denied"
	_, fs, cs = runSetup(f)
	none(t, fs, "INS14")
	if s := statusOf(cs, "INS14"); s.Status != CheckSkipped || !strings.HasPrefix(s.Note, configNotRead) {
		t.Errorf("INS14 skipped: %+v", s)
	}
}

func TestINS15(t *testing.T) {
	f := goodFacts()
	f.TraceFlags = []int{9999, 1117, 834, 3226, 1211}
	_, fs, cs := runSetup(f)
	got := findingsOf(fs, "INS15")
	if len(got) != 5 {
		t.Fatalf("INS15: %+v", got)
	}
	evidence := "Global trace flags (DBCC TRACESTATUS(-1)): 834, 1117, 1211, 3226, 9999."
	by := map[string]Finding{}
	for _, x := range got {
		by[x.Finding] = x
		// Phase 4d: one finding per flag, its object the flag, each offering trace-flag-off.
		if x.Evidence != evidence || x.Owner != OwnerUs || x.FixID != FixTraceFlagOff || !strings.Contains(x.Finding, "Trace flag "+x.Object+" ") {
			t.Errorf("INS15 %+v", x)
		}
	}
	if x := by["Trace flag 1211 is on for the whole server."]; x.Severity != SeverityMedium || x.When != WhenDecideFirst ||
		!strings.HasPrefix(x.Why, "Disables lock escalation entirely") || !strings.HasSuffix(x.DoThis, "DBCC TRACEOFF(1211, -1);") {
		t.Errorf("INS15 1211: %+v", x)
	}
	if x := by["Trace flag 834 is on for the whole server."]; x.Severity != SeverityLow || !strings.HasPrefix(x.Why, "Uses large-page allocations") {
		t.Errorf("INS15 834: %+v", x)
	}
	if x := by["Trace flag 1117 is on for the whole server."]; x.Severity != SeverityInfo || x.When != WhenNone ||
		!strings.Contains(x.DoThis, "-T1117") {
		t.Errorf("INS15 1117: %+v", x)
	}
	for _, n := range []string{"3226", "9999"} {
		if x := by["Trace flag "+n+" is on; look it up before a restart removes or keeps it."]; x.Severity != SeverityInfo ||
			x.Why != ins15UnknownWhy || x.Object != n {
			t.Errorf("INS15 unknown %s: %+v", n, x)
		}
	}
	if statusOf(cs, "INS15").Status != CheckFinding {
		t.Errorf("INS15 status %+v", statusOf(cs, "INS15"))
	}

	for flag, sev := range map[int]Severity{3608: SeverityMedium, 3609: SeverityMedium, 1224: SeverityLow, 4199: SeverityInfo,
		8048: SeverityInfo, 1118: SeverityInfo, 2371: SeverityInfo} {
		f.TraceFlags = []int{flag}
		_, fs, _ = runSetup(f)
		if x := one(t, fs, "INS15"); x.Severity != sev || x.Why != traceFlags[flag].text {
			t.Errorf("INS15 %d: %+v", flag, x)
		}
	}
	f.TraceFlags = []int{3608}
	_, fs, _ = runSetup(f)
	// A startup-only flag needs a restart: no trace-flag-off.
	if x := one(t, fs, "INS15"); x.When != WhenWindow || !strings.Contains(x.DoThis, "Remove -T3608") || x.FixID != "" || x.Object != "3608" {
		t.Errorf("INS15 startup: %+v", x)
	}
	f.TraceFlags = []int{7745}
	_, fs, cs = runSetup(f)
	if x := one(t, fs, "INS15"); x.Finding != "Trace flag 7745 is on; look it up before a restart removes or keeps it." ||
		statusOf(cs, "INS15").Status != CheckPassed {
		t.Errorf("INS15 one unknown: %+v", x)
	}

	f.TraceFlags, f.TraceFlagError = nil, "User does not have permission to run DBCC TRACESTATUS."
	_, fs, cs = runSetup(f)
	none(t, fs, "INS15")
	if s := statusOf(cs, "INS15"); s.Status != CheckSkipped ||
		s.Note != traceFlagsNotRead+"User does not have permission to run DBCC TRACESTATUS." {
		t.Errorf("INS15 skipped: %+v", s)
	}
}

func TestINS16(t *testing.T) {
	f := goodFacts()
	f.MinServerMemoryMB = 26000 // 90.7% of 28672
	_, fs, _ := runSetup(f)
	x := one(t, fs, "INS16")
	if x.Severity != SeverityLow || x.Owner != OwnerUs || x.When != WhenOnline || x.FixID != FixMinServerMemory ||
		x.Finding != "Min server memory is set to 26000 MB, 90% of max: SQL Server cannot give memory back to Windows." ||
		x.Evidence != "min server memory (MB) = 26000, max server memory (MB) = 28672." {
		t.Errorf("INS16 min: %+v", x)
	}
	f.MinServerMemoryMB = 25000 // 87%
	_, fs, _ = runSetup(f)
	none(t, fs, "INS16")
	f.MinServerMemoryMB, f.MaxServerMemoryMB = 30000, 2147483647 // max not set: INS02's business
	_, fs, _ = runSetup(f)
	none(t, fs, "INS16")

	f = goodFacts()
	f.LightweightPooling, f.AffinityMask = 1, 15
	_, fs, _ = runSetup(f)
	x = one(t, fs, "INS16")
	if x.Severity != SeverityLow || x.When != WhenWindow ||
		x.Finding != "Server options that are rarely needed are set: lightweight pooling (fiber mode) is on, affinity mask is 15." ||
		!strings.Contains(x.DoThis, "EXEC sp_configure 'lightweight pooling', 0; EXEC sp_configure 'affinity mask', 0; RECONFIGURE;") ||
		!strings.Contains(x.Why, "Lightweight pooling") || !strings.Contains(x.Why, "affinity mask") {
		t.Errorf("INS16 pooling: %+v", x)
	}
	f.LightweightPooling, f.AffinityMask, f.AffinityIOMask, f.Affinity64Mask = 0, 0, 3, -1
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "INS16"); x.When != WhenDecideFirst ||
		x.Finding != "Server options that are rarely needed are set: affinity64 mask is -1, affinity I/O mask is 3." {
		t.Errorf("INS16 affinity: %+v", x)
	}
	f.ConfigError = "denied"
	_, fs, cs := runSetup(f)
	none(t, fs, "INS16")
	if s := statusOf(cs, "INS16"); s.Status != CheckSkipped {
		t.Errorf("INS16 skipped: %+v", s)
	}
}

func TestDBC13(t *testing.T) {
	for mode, want := range map[string]string{
		"SINGLE_USER":     "WarehouseLive is in SINGLE_USER mode (only one connection at a time): the application cannot connect.",
		"RESTRICTED_USER": "WarehouseLive is in RESTRICTED_USER mode (only administrators and the database owner can connect): the application cannot connect.",
	} {
		f := goodFacts()
		f.UserAccess = mode
		c, fs, cs := runSetup(f)
		x := one(t, fs, "DBC13")
		if x.Severity != SeverityHigh || x.Owner != OwnerUs || x.FixID != FixMultiUser || x.Object != "WarehouseLive" || x.Finding != want ||
			!strings.HasSuffix(x.DoThis, "ALTER DATABASE [WarehouseLive] SET MULTI_USER;") || x.Evidence != "user_access_desc = "+mode ||
			statusOf(cs, "DBC13").Status != CheckFinding || c.Verdict != want {
			t.Errorf("DBC13 %s: %+v", mode, x)
		}
	}
	_, fs, _ := runSetup(goodFacts())
	none(t, fs, "DBC13")
}

func TestDBC14(t *testing.T) {
	_, fs, cs := runSetup(goodFacts())
	none(t, fs, "DBC14")
	if statusOf(cs, "DBC14").Status != CheckPassed {
		t.Errorf("DBC14 defaults: %+v", statusOf(cs, "DBC14"))
	}

	f := goodFacts()
	f.ScopedConfigs[1].Value, f.ScopedConfigs[2].Value = "1", "OFF"
	_, fs, cs = runSetup(f)
	// Phase 4d: one finding per configuration, its object the configuration, each offering
	// scoped-config-reset.
	got := findingsOf(fs, "DBC14")
	list := "LEGACY_CARDINALITY_ESTIMATION = ON (default OFF), PARAMETER_SNIFFING = OFF (default ON)"
	if len(got) != 2 {
		t.Fatalf("DBC14: %+v", got)
	}
	for i, want := range []struct{ object, item, back string }{
		{"LEGACY_CARDINALITY_ESTIMATION", "LEGACY_CARDINALITY_ESTIMATION = ON (default OFF)", "SET LEGACY_CARDINALITY_ESTIMATION = OFF;"},
		{"PARAMETER_SNIFFING", "PARAMETER_SNIFFING = OFF (default ON)", "SET PARAMETER_SNIFFING = ON;"},
	} {
		x := got[i]
		if x.Severity != SeverityInfo || x.Owner != OwnerUs || x.When != WhenNone || x.FixID != FixScopedConfigReset || x.Object != want.object ||
			x.Finding != "WarehouseLive changes a database scoped configuration from the default: "+want.item+"." ||
			x.Evidence != "sys.database_scoped_configurations: "+list+"." || !strings.HasSuffix(x.DoThis, want.back) {
			t.Errorf("DBC14 info %d: %+v", i, x)
		}
	}
	if statusOf(cs, "DBC14").Status != CheckPassed {
		t.Errorf("DBC14 info status: %+v", statusOf(cs, "DBC14"))
	}
	// A database that cannot be changed from here carries no fix.
	f.DB.ReadOnly = true
	_, fs, _ = runSetup(f)
	for _, x := range findingsOf(fs, "DBC14") {
		if x.FixID != "" {
			t.Errorf("DBC14 read-only: %+v", x)
		}
	}

	// A database MAXDOP that differs from the server's is Medium.
	f = goodFacts()
	f.ScopedConfigs[0].Value = "4"
	_, fs, cs = runSetup(f)
	x := one(t, fs, "DBC14")
	if x.Severity != SeverityMedium || x.When != WhenDecideFirst || x.Object != "MAXDOP" || x.FixID != FixScopedConfigReset ||
		x.Finding != "WarehouseLive sets its own MAXDOP of 4, which overrides the server's 8 for every query in this database." ||
		x.Evidence != "Non-default database scoped configurations: MAXDOP = 4 (default 0)." || x.Why != dbc14MaxWhy ||
		statusOf(cs, "DBC14").Status != CheckFinding {
		t.Errorf("DBC14 maxdop: %+v", x)
	}
	// ... the same as the server's is only listed.
	f.ScopedConfigs[0].Value = "8"
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC14"); x.Severity != SeverityInfo ||
		x.Finding != "WarehouseLive changes a database scoped configuration from the default: MAXDOP = 8 (default 0)." {
		t.Errorf("DBC14 same maxdop: %+v", x)
	}

	// DOP_FEEDBACK's default is ON from SQL Server 2025.
	f = goodFacts()
	f.ScopedConfigs[4].Value = "1"
	_, fs, _ = runSetup(f)
	if x := one(t, fs, "DBC14"); !strings.Contains(x.Finding, "DOP_FEEDBACK = ON (default OFF)") {
		t.Errorf("DBC14 DOP_FEEDBACK on 2022: %+v", x)
	}
	f.ProductVersion, f.CompatLevel = "17.0.4055.1", 170
	_, fs, _ = runSetup(f)
	none(t, fs, "DBC14")

	f = goodFacts()
	f.ScopedConfigs = nil
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "DBC14"); s.Status != CheckSkipped || s.Note != dbc14Skipped {
		t.Errorf("DBC14 before 2016: %+v", s)
	}
	f.ScopedConfigsError = "denied"
	_, _, cs = runSetup(f)
	if s := statusOf(cs, "DBC14"); s.Status != CheckSkipped || s.Note != legacyCENotRead+"denied" {
		t.Errorf("DBC14 not read: %+v", s)
	}
}
