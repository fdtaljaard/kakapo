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

var queryPageQueries = map[string]string{
	"query store detail": queryDetailStoreQuery, "plan cache detail": queryDetailCacheQuery, "tables": queryTablesQuery,
	"plan forcing": planForcingQuery, "cached plan": cachedPlanQuery,
}

// The query page's reads are tagged (docs/phase3f-contract.md, item 2) and SELECTs only: no
// statement that writes outside temporary tables and table variables; the fixes' statements are
// built in domain and run through ExecFix.
func TestQueryReadsTaggedAndReadOnly(t *testing.T) {
	for name, q := range queryPageQueries {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.Count(q, KakapoTag) < 1 {
			t.Errorf("%s query does not start with the tag: %.60s", name, q)
		}
		upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
		for _, verb := range []string{"ALTER ", "DROP ", "TRUNCATE ", "DBCC ", "CREATE INDEX", "SP_CONFIGURE", "KILL ", "MERGE ",
			"FORCE_PLAN", "UNFORCE_PLAN", "FREEPROCCACHE", "UPDATE STATISTICS"} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, line := range strings.Split(upper, "\n") {
			for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE "} {
				if i := strings.Index(line, verb); i >= 0 && !strings.Contains(line[i:], "@") && !strings.Contains(line[i:], "#") {
					t.Errorf("%s query writes: %s", name, line)
				}
			}
		}
		// A nested batch carries the tag too.
		if strings.Contains(q, "sp_executesql N'") && !strings.Contains(q, "sp_executesql N'"+KakapoTag) {
			t.Errorf("%s query runs an untagged nested batch", name)
		}
	}
	// Version-dependent reads are guarded.
	if !strings.Contains(queryDetailStoreQuery, "name = N'avg_tempdb_space_used'") || !strings.Contains(queryDetailStoreQuery, "OBJECT_ID(N'sys.query_store_wait_stats') IS NULL") {
		t.Error("the Query Store read does not guard its 2017 columns")
	}
	// Each area has its own TRY.
	if n := strings.Count(queryDetailStoreQuery, "BEGIN TRY"); n != 3 {
		t.Errorf("Query Store detail has %d TRY blocks, want 3", n)
	}
	if n := strings.Count(queryDetailCacheQuery, "BEGIN TRY"); n != 2 {
		t.Errorf("plan cache detail has %d TRY blocks, want 2", n)
	}
	if !strings.Contains(queryTablesQuery, "sys.dm_db_partition_stats") || !strings.Contains(queryTablesQuery, "sys.partitions") {
		t.Error("the tables read falls back to sys.partitions without VIEW DATABASE STATE")
	}
}

func TestTablesXML(t *testing.T) {
	got := tablesXML([]domain.ShowplanObject{{Schema: "dbo", Table: "Pick"}, {Schema: `we"ird`, Table: "a<b&c"}})
	if got != `<t s="dbo" n="Pick"/><t s="we&#34;ird" n="a&lt;b&amp;c"/>` {
		t.Errorf("xml %s", got)
	}
}

func TestServerToUTC(t *testing.T) {
	local := time.Date(2026, 11, 3, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if got := serverToUTC(local, 120); !got.Equal(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("utc %v", got)
	}
}

// TestQueryDetailLive reads one query of a real instance, read-only, when SQLMONITOR_TEST_HOST
// (Windows authentication), SQLMONITOR_TEST_DB and SQLMONITOR_TEST_HASH name one.
func TestQueryDetailLive(t *testing.T) {
	host, dbName, hash := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB"), os.Getenv("SQLMONITOR_TEST_HASH")
	if host == "" || dbName == "" || hash == "" {
		t.Skip("set SQLMONITOR_TEST_HOST, SQLMONITOR_TEST_DB and SQLMONITOR_TEST_HASH to read a live query")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := QueryDetail(context.Background(), db, dbName, hash)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state %s, %d plans, %d cached, %d hours, %d waits, %d statements, %d tables; errors %q %q %q %q %q %q",
		f.QueryStoreState, len(f.Plans), len(f.CachedPlans), len(f.History), len(f.Waits), len(f.Statements), len(f.Tables),
		f.QueryStoreError, f.HistoryError, f.WaitsError, f.PlanCacheError, f.StatementsError, f.TablesError)
}
