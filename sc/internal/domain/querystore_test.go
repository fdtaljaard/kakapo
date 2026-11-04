package domain

import (
	"strings"
	"testing"
	"time"
)

// defaults2022 is a database where Query Store is on with the SQL Server 2022 defaults.
func defaults2022() QueryStoreOptions {
	return QueryStoreOptions{
		ActualState: "READ_WRITE", DesiredState: "READ_WRITE",
		CurrentStorageMb: 12, MaxStorageMb: 1000, CaptureMode: "AUTO", WaitStatsCaptureMode: "ON",
		IntervalLengthMinutes: 60, StaleQueryThresholdDays: 30, FlushIntervalSeconds: 900,
		MaxPlansPerQuery: 200, SizeBasedCleanupMode: "AUTO",
	}
}

func onTarget() QueryStoreOptions {
	o := defaults2022()
	o.MaxStorageMb, o.CurrentStorageMb, o.IntervalLengthMinutes = 2048, 471, 15
	return o
}

func settings(changes []Drift) []string {
	out := []string{}
	for _, d := range changes {
		out = append(out, d.Setting)
	}
	return out
}

func TestBuildFixOffOn2016(t *testing.T) {
	o := QueryStoreOptions{ActualState: "OFF", DesiredState: "OFF", MaxStorageMb: 100, CaptureMode: "ALL",
		IntervalLengthMinutes: 60, StaleQueryThresholdDays: 30, FlushIntervalSeconds: 900, MaxPlansPerQuery: 200,
		SizeBasedCleanupMode: "OFF"}
	p := BuildQueryStoreFix(o, 13, "GraniteLive")

	want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = AUTO, " +
		"MAX_STORAGE_SIZE_MB = 2048, SIZE_BASED_CLEANUP_MODE = AUTO, INTERVAL_LENGTH_MINUTES = 15, " +
		"CLEANUP_POLICY = (STALE_QUERY_THRESHOLD_DAYS = 30), DATA_FLUSH_INTERVAL_SECONDS = 900, MAX_PLANS_PER_QUERY = 200);"
	if p.Statement != want {
		t.Errorf("statement\n got %s\nwant %s", p.Statement, want)
	}
	if strings.Contains(p.Statement, "WAIT_STATS") {
		t.Error("SQL Server 2016 must not get the WAIT_STATS_CAPTURE_MODE clause")
	}
	if len(p.Changes) != 8 {
		t.Errorf("off lists every target setting but wait stats: got %v", settings(p.Changes))
	}
	wantRollback := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (QUERY_CAPTURE_MODE = ALL, MAX_STORAGE_SIZE_MB = 100, " +
		"SIZE_BASED_CLEANUP_MODE = OFF, INTERVAL_LENGTH_MINUTES = 60);\nALTER DATABASE [GraniteLive] SET QUERY_STORE = OFF;"
	if p.Rollback != wantRollback {
		t.Errorf("rollback\n got %s\nwant %s", p.Rollback, wantRollback)
	}
	if !hasWarning(p, "no wait-stats") || !hasWarning(p, "Early cumulative updates") {
		t.Errorf("warnings miss the 2016 notes: %q", p.Warnings)
	}
}

func TestBuildFixOffOn2022(t *testing.T) {
	o := defaults2022()
	o.ActualState, o.DesiredState = "OFF", "OFF"
	p := BuildQueryStoreFix(o, 16, "GraniteLive")

	want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = AUTO, " +
		"MAX_STORAGE_SIZE_MB = 2048, SIZE_BASED_CLEANUP_MODE = AUTO, INTERVAL_LENGTH_MINUTES = 15, " +
		"CLEANUP_POLICY = (STALE_QUERY_THRESHOLD_DAYS = 30), DATA_FLUSH_INTERVAL_SECONDS = 900, MAX_PLANS_PER_QUERY = 200, " +
		"WAIT_STATS_CAPTURE_MODE = ON);"
	if p.Statement != want {
		t.Errorf("statement\n got %s\nwant %s", p.Statement, want)
	}
	if len(p.Changes) != 9 {
		t.Errorf("off lists every target setting: got %v", settings(p.Changes))
	}
	if c := Compare(o, 16); c.Verdict != VerdictOff || c.Summary != "Off" || len(c.Drift) != 9 {
		t.Errorf("compare: %+v", c)
	}
	if hasWarning(p, "Early cumulative updates") {
		t.Error("no old-build warning on 2022")
	}
}

func TestBuildFixOnWithDefaultInterval(t *testing.T) {
	o := onTarget()
	o.IntervalLengthMinutes = 60
	p := BuildQueryStoreFix(o, 16, "GraniteLive")

	if want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (INTERVAL_LENGTH_MINUTES = 15);"; p.Statement != want {
		t.Errorf("statement\n got %s\nwant %s", p.Statement, want)
	}
	if want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (INTERVAL_LENGTH_MINUTES = 60);"; p.Rollback != want {
		t.Errorf("rollback\n got %s\nwant %s", p.Rollback, want)
	}
	if len(p.Changes) != 1 || p.Changes[0] != (Drift{"INTERVAL_LENGTH_MINUTES", "60", "15"}) {
		t.Errorf("changes: %+v", p.Changes)
	}
	if !hasWarning(p, "clears the runtime statistics") {
		t.Errorf("interval warning missing: %q", p.Warnings)
	}

	// The 2022 defaults drift in two places: the storage limit and the interval.
	p = BuildQueryStoreFix(defaults2022(), 16, "GraniteLive")
	if want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (MAX_STORAGE_SIZE_MB = 2048, INTERVAL_LENGTH_MINUTES = 15);"; p.Statement != want {
		t.Errorf("statement\n got %s\nwant %s", p.Statement, want)
	}
	if !hasWarning(p, "2 GB") {
		t.Errorf("storage warning missing: %q", p.Warnings)
	}
	if c := Compare(defaults2022(), 16); c.Verdict != VerdictDrift || c.Summary != "On, 1% full, 60-min intervals, 30 days" {
		t.Errorf("compare: %+v", c)
	}
}

func TestBuildFixOnAndMatching(t *testing.T) {
	p := BuildQueryStoreFix(onTarget(), 16, "GraniteLive")
	if p.Statement != "" || p.Rollback != "" || len(p.Changes) != 0 || p.Changes == nil {
		t.Errorf("want nothing to do, got %+v", p)
	}
	c := Compare(onTarget(), 16)
	if c.Verdict != VerdictOK || len(c.Drift) != 0 || c.Drift == nil {
		t.Errorf("compare: %+v", c)
	}
	if want := "On, 23% full, 15-min intervals, 30 days"; c.Summary != want {
		t.Errorf("summary %q, want %q", c.Summary, want)
	}
}

func TestBuildFixReadOnlyStorageFull(t *testing.T) {
	o := onTarget()
	o.ActualState, o.ReadonlyReason, o.MaxStorageMb, o.CurrentStorageMb = "READ_ONLY", 65536, 100, 100
	p := BuildQueryStoreFix(o, 16, "GraniteLive")

	if want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (OPERATION_MODE = READ_WRITE, MAX_STORAGE_SIZE_MB = 2048);"; p.Statement != want {
		t.Errorf("statement\n got %s\nwant %s", p.Statement, want)
	}
	if want := "ALTER DATABASE [GraniteLive] SET QUERY_STORE (OPERATION_MODE = READ_ONLY, MAX_STORAGE_SIZE_MB = 100);"; p.Rollback != want {
		t.Errorf("rollback\n got %s\nwant %s", p.Rollback, want)
	}
	c := Compare(o, 16)
	if c.Verdict != VerdictDrift || c.Summary != "Read-only: storage full" {
		t.Errorf("compare: %+v", c)
	}
}

func TestBiggerStorageLimitIsKept(t *testing.T) {
	o := onTarget()
	o.MaxStorageMb = 10240
	if c := Compare(o, 16); c.Verdict != VerdictOK {
		t.Errorf("a limit above the target should count as matching: %+v", c)
	}
	o.ActualState = "OFF"
	p := BuildQueryStoreFix(o, 16, "GraniteLive")
	if strings.Contains(p.Statement, "MAX_STORAGE_SIZE_MB") {
		t.Errorf("turning on must not shrink a bigger limit: %s", p.Statement)
	}
}

func TestQuoteName(t *testing.T) {
	if got := QuoteName("Gran]ite [Live]"); got != "[Gran]]ite [Live]]]" {
		t.Errorf("got %s", got)
	}
	o := onTarget()
	o.IntervalLengthMinutes = 60
	if p := BuildQueryStoreFix(o, 16, "a]b"); !strings.HasPrefix(p.Statement, "ALTER DATABASE [a]]b] SET") {
		t.Errorf("statement %s", p.Statement)
	}
}

func TestReadonlyReasonText(t *testing.T) {
	for reason, want := range map[int]string{
		0:      "",
		1:      "the database is in read-only mode",
		2:      "the database is in single-user mode",
		4:      "the database is in emergency mode",
		8:      "secondary replica",
		65536:  "storage full",
		131072: "too many different queries for its memory limit",
		262144: "too much data waiting to be written to disk",
		524288: "the database reached its disk size limit",
		// Several bits join, lowest first; an unknown bit is named by its value.
		1 | 65536:  "the database is in read-only mode, storage full",
		8 | 131072: "secondary replica, too many different queries for its memory limit",
		16:         "other reason (16)",
		2 | 32:     "the database is in single-user mode, other reason (32)",
	} {
		if got := ReadonlyReasonText(reason); got != want {
			t.Errorf("%d: got %q want %q", reason, got, want)
		}
	}
}

func TestNewQueryStoreStatus(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)

	st := NewQueryStoreStatus(onTarget(), "16.0.1000.6", now, true)
	if st.Verdict != VerdictOK || !st.CanFix || st.StorageUsedPercent != 23 || st.Summary != "On, 23% full, 15-min intervals, 30 days" {
		t.Errorf("on target: %+v", st)
	}

	// A secondary replica cannot be fixed here: grey, with the reason.
	o := onTarget()
	o.ActualState, o.ReadonlyReason = "READ_ONLY", 8
	st = NewQueryStoreStatus(o, "16.0.1000.6", now, false)
	if st.Verdict != VerdictUnavailable || st.ReadonlyReasonText != "secondary replica" || st.Summary != "Read-only: secondary replica" {
		t.Errorf("secondary: %+v", st)
	}

	// 2016 and 2017 show the build next to the state.
	st = NewQueryStoreStatus(onTarget(), "13.0.5026.0", now, true)
	if want := "On, 23% full, 15-min intervals, 30 days (SQL Server 2016 build 13.0.5026.0)"; st.Summary != want {
		t.Errorf("summary %q, want %q", st.Summary, want)
	}
	// ... and on 2016 there is no wait-stats setting to drift.
	if st.Verdict != VerdictOK {
		t.Errorf("2016 on target should be ok: %+v", st.Drift)
	}
}

func TestChangeDescription(t *testing.T) {
	got := ChangeDescription("GraniteLive", []Drift{{"INTERVAL_LENGTH_MINUTES", "60", "15"}, {"MAX_PLANS_PER_QUERY", "200", "200"}})
	if want := "Query Store on GraniteLive: INTERVAL_LENGTH_MINUTES 60 -> 15"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func hasWarning(p FixPreview, part string) bool {
	for _, w := range p.Warnings {
		if strings.Contains(w, part) {
			return true
		}
	}
	return false
}
