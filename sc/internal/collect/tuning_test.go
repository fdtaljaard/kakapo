package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The tuning report's read is tagged (docs/phase3f-contract.md, item 2) and SELECTs only: nothing
// written outside temporary tables, every nested batch tagged, Query Store views only.
func TestTuningReadTaggedAndReadOnly(t *testing.T) {
	for name, q := range map[string]string{"tuning report": tuningReportQuery, "server clock": serverClockQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") {
			t.Errorf("%s query does not start with the tag: %.60s", name, q)
		}
		upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
		for _, verb := range []string{"ALTER ", "DROP ", "TRUNCATE ", "DBCC ", "CREATE INDEX", "SP_CONFIGURE", "KILL ", "MERGE ",
			"FORCE_PLAN", "FREEPROCCACHE", "UPDATE STATISTICS", "SP_QUERY_STORE", "UPDATE "} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, line := range strings.Split(upper, "\n") {
			for _, verb := range []string{"INSERT ", "DELETE "} {
				if i := strings.Index(line, verb); i >= 0 && !strings.Contains(line[i:], "#") {
					t.Errorf("%s query writes outside a temporary table: %s", name, line)
				}
			}
		}
		if strings.Count(q, "sp_executesql N'") != strings.Count(q, "sp_executesql N'"+KakapoTag) {
			t.Errorf("%s query runs an untagged nested batch", name)
		}
	}
	q := tuningReportQuery
	// Each area in its own TRY: about, hours, waits, top shapes, regressions, failures.
	if n := strings.Count(q, "BEGIN TRY"); n != 6 {
		t.Errorf("%d TRY blocks, want 6", n)
	}
	// The 2017 wait view only behind its guard, and only inside a nested batch, so SQL Server 2016
	// compiles the read; Query Store itself behind the 2016 guard.
	if !strings.Contains(q, "IF OBJECT_ID(N'sys.query_store_wait_stats') IS NULL") || !strings.Contains(q, "OBJECT_ID(N'sys.query_store_runtime_stats') IS NULL") {
		t.Error("the read does not guard Query Store and its 2017 wait view")
	}
	outside := withoutNestedBatches(q)
	if strings.Contains(outside, "query_store_wait_stats AS") || strings.Contains(outside, "wait_stats_capture_mode_desc") {
		t.Error("a 2017 column or view is read outside its nested batch")
	}
	// Query Store views and the database's own catalogue only: no DMV, no instance view.
	for _, v := range []string{"sys.dm_", "msdb.", "master."} {
		if strings.Contains(q, v) {
			t.Errorf("the read touches %s", v)
		}
	}
	// The execution-weighted sums, and kakapo's own statements left out of the queries.
	if !strings.Contains(q, "rs.avg_cpu_time * rs.count_executions") || !strings.Contains(q, "NOT LIKE N'%"+KakapoTag+"%'") {
		t.Error("the read does not weight by executions or does not leave kakapo's statements out")
	}
	// The toolkit's rule 4 with domain's thresholds.
	if !strings.Contains(q, "n.rn = 1 AND n.execs >= 50 AND o.cpu_ms >= 1 AND n.cpu_ms >= o.cpu_ms * 1.5") ||
		!strings.Contains(q, "ORDER BY pp.first_seen DESC, pp.plan_id DESC") {
		t.Errorf("plan regression rule: %s", tuningRegressionRule)
	}
	if !strings.Contains(q, "execution_type") || !strings.Contains(q, "r.exec_type = 3") || !strings.Contains(q, "r.exec_type = 4") {
		t.Error("aborted and exception runs are not read")
	}
}

// TestTuningReportLive reads a real database's report, read-only, when SQLMONITOR_TEST_HOST
// (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestTuningReportLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live tuning report")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := TuningReport(context.Background(), db, dbName, 7)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state %s, interval %d, offset %d, %d hours, %d shapes, %d regressions, %d failures; errors %q %q %q %q %q %q",
		f.QueryStoreState, f.IntervalMinutes, f.UTCOffsetMinutes, len(f.Hours), len(f.Shapes), len(f.Regressions), len(f.Failures),
		f.AboutError, f.HoursError, f.WaitsError, f.ShapesError, f.RegressionsError, f.FailuresError)
}

// withoutNestedBatches drops the text of every sp_executesql N'...' literal (doubled quotes inside).
func withoutNestedBatches(q string) string {
	var b strings.Builder
	for {
		i := strings.Index(q, "sp_executesql N'")
		if i < 0 {
			b.WriteString(q)
			return b.String()
		}
		b.WriteString(q[:i])
		j := i + len("sp_executesql N'")
		for j < len(q) {
			if q[j] == '\'' {
				if j+1 < len(q) && q[j+1] == '\'' {
					j += 2
					continue
				}
				break
			}
			j++
		}
		q = q[min(j+1, len(q)):]
	}
}
