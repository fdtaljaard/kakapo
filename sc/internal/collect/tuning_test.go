package collect

import (
	"context"
	"database/sql"
	"os"
	"regexp"
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
	// Each area in its own TRY: about, hours, waits, top shapes, did it help (5h), regressions, failures.
	if n := strings.Count(q, "BEGIN TRY"); n != 7 {
		t.Errorf("%d TRY blocks, want 7", n)
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
	// Query Store views and the database's own catalogue only: no instance view, and no DMV but the two
	// a followed index needs (its use, and when SQL Server started), inside the focus area.
	outsideFocus := strings.Replace(q, tuningFocusArea, "", 1)
	for _, v := range []string{"sys.dm_", "msdb.", "master."} {
		if strings.Contains(outsideFocus, v) {
			t.Errorf("the read touches %s", v)
		}
	}
	if dm := regexp.MustCompile(`sys\.dm_\w+`).FindAllString(tuningFocusArea, -1); strings.Join(dm, ",") != "sys.dm_os_sys_info,sys.dm_db_index_usage_stats" {
		t.Errorf("the focus area reads %v", dm)
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
	f, err := TuningReport(context.Background(), db, dbName, 7, domain.TuningFocusTarget{})
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

// The focus area (docs/phase5h-contract.md, section 2): in its own TRY after Top shapes and guarded on
// @topErr, the names one parameter split without STRING_SPLIT, every LIKE with COLLATE DATABASE_DEFAULT
// on both sides, the plan text read only for an index and only for the plans of the queries whose text
// names its table, and no second read of query_store_runtime_stats.
func TestTuningFocusArea(t *testing.T) {
	q, a := tuningReportQuery, tuningFocusArea
	top, focus, reg := strings.Index(q, "/* Top shapes"), strings.Index(q, a), strings.Index(q, "/* Plan regressions")
	if top < 0 || focus < top || reg < focus {
		t.Fatalf("the focus area is not between Top shapes and Plan regressions: %d %d %d", top, focus, reg)
	}
	if strings.Count(a, "BEGIN TRY") != 1 || !strings.Contains(a, "SET @focusErr = ERROR_MESSAGE()") ||
		!strings.Contains(a, "IF @focusKind <> N'' AND @aboutErr IS NULL AND @hoursErr IS NULL AND @topErr IS NULL\nBEGIN TRY") {
		t.Error("the focus area is not its own TRY guarded on @focusKind and @topErr")
	}
	if strings.Contains(strings.ToUpper(q), "STRING_SPLIT") || !strings.Contains(a, "CHARINDEX(NCHAR(10), @rest)") {
		t.Error("the names are not split with CHARINDEX")
	}
	if strings.Contains(a, "query_store_runtime_stats") || !strings.Contains(a, "JOIN #rs AS r ON r.plan_id = p.plan_id") {
		t.Error("the focus area reads query_store_runtime_stats again")
	}
	for _, line := range strings.Split(a, "\n") {
		if strings.Contains(line, " LIKE ") && strings.Count(line, "COLLATE DATABASE_DEFAULT") != 2 {
			t.Errorf("a LIKE without COLLATE DATABASE_DEFAULT on both sides: %s", strings.TrimSpace(line))
		}
	}
	if strings.Count(a, " LIKE ") != 4 {
		t.Errorf("%d LIKEs in the focus area, want 4 (the view or the table, two statements left out, the plan)", strings.Count(a, " LIKE "))
	}
	// The plan text: only an index's candidates (#fcand: the queries whose text names the table, in the
	// window), Table and Index adjacent, [ escaped; never CONVERT.
	plan := a[strings.Index(a, "p.query_plan"):]
	plan = plan[:strings.Index(plan, ";")]
	if strings.Count(a, "query_plan") != 1 || !strings.Contains(a, "FROM #fcand AS c") || !strings.Contains(plan, `N'Table="[[]' + `) ||
		!strings.Contains(plan, `N']" ' END + N'Index="[[]' + `) || strings.Contains(a, "CONVERT(nvarchar(max), p.query_plan") {
		t.Errorf("plan match: %s", plan)
	}
	if !strings.Contains(a, "WHERE f.object_id IS NOT NULL AND (@focusKind = N'view' OR f.index_id IS NOT NULL)") || !strings.Contains(a, "FROM #ftext AS t") ||
		!strings.Contains(a, "WHERE t.head COLLATE DATABASE_DEFAULT NOT LIKE N'CREATE%INDEX%'") ||
		!strings.Contains(a, "JOIN #plans AS p ON p.query_id = q.query_id\n            WHERE t.head") {
		t.Error("the candidates are not an index's queries in the window")
	}
	if !strings.Contains(a, focusLikeName("f.tbl")) || focusLikeName("x") != "REPLACE(REPLACE(REPLACE(x, N'[', N'[[]'), N'%', N'[%]'), N'_', N'[_]')" {
		t.Error("names in a LIKE pattern are not escaped")
	}
	// The texts are read once, a binary prefilter before the LIKE in the database's collation.
	if !strings.Contains(a, "CASE WHEN CHARINDEX(@needle COLLATE Latin1_General_BIN2, UPPER(qt.query_sql_text) COLLATE Latin1_General_BIN2) = 0 THEN 0") ||
		strings.Count(a, "sys.query_store_query_text") != 1 {
		t.Error("the query texts are not read once with the prefilter first")
	}
	// A module's runs are its most-run statement's, per hour.
	if !strings.Contains(a, "SELECT x.n, NULL, x.hour_start, CONVERT(bigint, SUM(x.cpu)), MAX(x.execs)") || !strings.Contains(a, "GROUP BY fq.n, i.hour_start, fq.query_id") {
		t.Error("a module's runs are not its most-run statement's")
	}
	// The batch's parameters.
	if !strings.Contains(q, "@names") || strings.Contains(q, "' + @names") {
		t.Error("the names are not one parameter")
	}
}
