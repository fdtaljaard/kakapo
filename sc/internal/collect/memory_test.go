package collect

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The Memory screen's reads (docs/phase5v-contract.md) are read only, check the rights first, cast every
// sum to bigint, count pages with COUNT_BIG, and never join cached plans to sessions.
func TestMemoryQueries(t *testing.T) {
	outer := fmt.Sprintf(databaseCacheOuter, domain.QuoteName("Sales]Live"))
	queries := map[string]string{"overview": memoryOverviewQuery, "buffers": memoryBuffersQuery,
		"detail outer": outer, "detail": TagStatements(databaseCacheQuery)}
	sumNotBigint := regexp.MustCompile(`SUM\((?:[^C(]|C[^A])`) // SUM( not followed by CAST
	for name, q := range queries {
		readOnly(t, name, q)
		upper := strings.ToUpper(q)
		if strings.Contains(upper, "DM_EXEC_SESSIONS") || strings.Contains(upper, "DM_EXEC_REQUESTS") {
			t.Errorf("%s joins sessions or requests", name)
		}
		if strings.Contains(upper, "COUNT(*) AS PAGES") || strings.Contains(upper, "CONNECTION TIMEOUT") {
			t.Errorf("%s counts pages with COUNT or carries a socket deadline", name)
		}
		for _, m := range sumNotBigint.FindAllString(q, -1) {
			// SUM(a.pages) and SUM(t.pages) add bigint COUNT_BIG and bigint sums.
			if m != "SUM(a" && m != "SUM(t" {
				t.Errorf("%s has a SUM not cast to bigint: %s", name, m)
			}
		}
	}
	for _, name := range []string{"overview", "buffers", "detail outer"} {
		q := queries[name]
		if !strings.Contains(q, "VIEW SERVER STATE") || !strings.Contains(q, "VIEW SERVER PERFORMANCE STATE") {
			t.Errorf("%s does not check the rights first", name)
		}
	}
	if !strings.Contains(outer, "VIEW DATABASE STATE") || !strings.Contains(outer, "EXEC [Sales]]Live].sys.sp_executesql @stmt") {
		t.Errorf("detail outer: %s", outer)
	}
	for _, want := range []string{"COUNT_BIG(*)", "GROUP BY database_id", "MEMORYCLERK_SQLBUFFERPOOL"} {
		if !strings.Contains(memoryBuffersQuery, want) {
			t.Errorf("buffers query lacks %q", want)
		}
	}
	for _, want := range []string{"COUNT_BIG(*)", "bd.database_id = DB_ID()", "sys.dm_db_partition_stats", "sys.dm_db_index_usage_stats",
		"is_included_column = 1", "key_ordinal = 1"} {
		if !strings.Contains(databaseCacheQuery, want) {
			t.Errorf("detail query lacks %q", want)
		}
	}
	for _, want := range []string{"usecounts = 1 AND objtype = N'Adhoc'", "SUM(CAST(size_in_bytes AS bigint))", "Buffer Node",
		"Memory Grants Pending", "process_physical_memory_low", "committed_target_kb", "value_in_use"} {
		if !strings.Contains(memoryOverviewQuery, want) {
			t.Errorf("overview query lacks %q", want)
		}
	}
	if strings.Contains(memoryOverviewQuery, "dm_os_buffer_descriptors") {
		t.Error("the overview scans the buffer pool: that runs only on a click")
	}
}

func TestServerStateRight(t *testing.T) {
	if serverStateRight(15) != "VIEW SERVER STATE" || !strings.HasPrefix(serverStateRight(16), "VIEW SERVER PERFORMANCE STATE") {
		t.Error(serverStateRight(15), serverStateRight(16))
	}
}
