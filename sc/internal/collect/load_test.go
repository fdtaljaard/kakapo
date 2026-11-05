package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The load read is tagged and SELECTs only, each area in its own TRY, the 2017 wait view only behind
// its guard and inside a nested batch, Query Store views only besides cpu_count; and it buckets the
// hour with the tuning report's own fragment (docs/phase5i-contract.md, Tests).
func TestLoadReadTaggedAndReadOnly(t *testing.T) {
	for name, q := range map[string]string{"load": loadQuery, "live": liveQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
			t.Errorf("%s query is not tagged once: %.60s", name, q)
		}
		readOnly(t, name, q)
		if strings.Count(q, "sp_executesql N'") != strings.Count(q, "sp_executesql N'"+KakapoTag) {
			t.Errorf("%s query runs an untagged nested batch", name)
		}
	}
	q := loadQuery
	// About, the cores, hours, waits, now.
	if n := strings.Count(q, "BEGIN TRY"); n != 5 {
		t.Errorf("%d TRY blocks, want 5", n)
	}
	if !strings.Contains(q, "IF OBJECT_ID(N'sys.query_store_wait_stats') IS NULL") || !strings.Contains(q, "OBJECT_ID(N'sys.query_store_runtime_stats') IS NULL") {
		t.Error("the read does not guard Query Store and its 2017 wait view")
	}
	if strings.Contains(withoutNestedBatches(q), "query_store_wait_stats AS") {
		t.Error("the 2017 wait view is read outside its nested batch")
	}
	// Sleeping tasks and WAITFOR are not load: both wait reads (the hours and now) leave them out.
	if strings.Count(q, "ws.wait_category_desc NOT IN (N''Idle'', N''User Wait'')") != 2 {
		t.Error("a wait read counts Idle or User Wait as threads waiting")
	}
	if strings.Count(q, "sys.dm_") != 1 || !strings.Contains(q, "SELECT @cpuCount = cpu_count FROM sys.dm_os_sys_info") {
		t.Error("the load read touches a DMV besides cpu_count")
	}
	for _, v := range []string{"msdb.", "master."} {
		if strings.Contains(q, v) {
			t.Errorf("the read touches %s", v)
		}
	}
	// The hour is bucketed one way only: the tuning report and the load run the same fragment.
	for name, b := range map[string]string{"load": loadQuery, "tuning": tuningReportQuery} {
		if !strings.Contains(b, queryStoreHoursTable) || !strings.Contains(b, queryStoreHours) {
			t.Errorf("the %s read does not use the shared hour fragment", name)
		}
	}
	if strings.Count(loadQuery+tuningReportQuery, "DATEADD(HOUR, DATEDIFF(HOUR, 0,") != 2 {
		t.Error("an hour is bucketed outside the shared fragment")
	}
	// 5c's sums.
	if !strings.Contains(q, "rs.avg_cpu_time * rs.count_executions") {
		t.Error("CPU is not weighted by executions")
	}
}

// The live checks are one fragment, run by the Performance read and the load strip's live read.
func TestLiveFragmentShared(t *testing.T) {
	for name, q := range map[string]string{"performance": performanceInstanceQuery, "live": liveQuery} {
		if !strings.Contains(q, liveFragment) || !strings.Contains(q, liveColumns) || !strings.Contains(q, liveResultSets) {
			t.Errorf("the %s read does not run the live fragment", name)
		}
	}
	// ACT01's and PRS01's reads live in the fragment only.
	for _, marker := range []string{"/* ACT01 Head blockers", "/* PRS01 Worker threads", "/* PRS01 Memory grants"} {
		if strings.Count(liveFragment, marker) != 1 || strings.Count(performanceInstanceQuery, marker) != 1 {
			t.Errorf("%s is not read once, in the fragment", marker)
		}
	}
	if n := strings.Count(liveFragment, "BEGIN TRY"); n != 3 {
		t.Errorf("the live fragment has %d TRY blocks, want 3", n)
	}
}

// TestLoadLive reads a real database's load and live facts, read-only, when SQLMONITOR_TEST_HOST
// (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestLoadLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live load")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now, _, err := ServerClock(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f, err := LoadHours(context.Background(), db, dbName, domain.LoadFrom(now, 30))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: state %s, interval %d, cores %d, %d hours, now since %v: %d runs, %d µs CPU, %.1f s waits; errors %q %q %q %q %q",
		time.Since(start).Round(time.Millisecond), f.QueryStoreState, f.IntervalMinutes, f.CPUCount, len(f.Hours), f.NowStart, f.NowExecutions, f.NowCPU,
		f.NowWaitSec, f.AboutError, f.CPUCountError, f.HoursError, f.WaitsError, f.NowError)
	live, err := LiveFacts(context.Background(), db, dbName)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live: %d blockers, schedulers %+v, grants %+v; errors %q %q %q", len(live.Blockers), live.Schedulers, live.Grants,
		live.BlockingError, live.SchedulersError, live.GrantsError)
}
