package collect

import (
	"strings"
	"testing"
)

// A query's text loses the parameter list SQL Server puts in front of it.
func TestLensText(t *testing.T) {
	for in, want := range map[string]string{
		"(@1 varchar(8000))SELECT COUNT(*)\r\n  FROM [dbo].[Big] WHERE [pad]=@1": "SELECT COUNT(*) FROM [dbo].[Big] WHERE [pad]=@1",
		"(@p1 nvarchar(50),@p2 decimal(10,2)) SELECT 1":                          "SELECT 1",
		"  SELECT   a  FROM b ": "SELECT a FROM b",
		"(@1 int":               "(@1 int",
	} {
		if got := lensText(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The Indexes page's reads (docs/phase5t-contract.md) are tagged, once, and read only; the candidate
// read leaves kakapo's own statements out, takes the top 100 shapes by total duration and the plan
// that ran last, and reads a plan only up to the size limit.
func TestIndexLensQueries(t *testing.T) {
	for name, q := range map[string]string{"candidates": indexCandidatesQuery, "basics": indexBasicsQuery} {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
			t.Errorf("%s query is not tagged once: %.60s", name, q)
		}
		readOnly(t, name, q)
	}
	for _, want := range []string{"TOP (100)", "GROUP BY q.query_hash", "DATEADD(DAY, -7, @now)", "ORDER BY qp.last_execution_time DESC",
		"DATALENGTH(x.query_plan) / 2 <= @limit", "NOT LIKE N'%/* kakapo */%'", "database_query_store_options"} {
		if !strings.Contains(indexCandidatesQuery, want) {
			t.Errorf("candidates query lacks %q", want)
		}
	}
	for _, bad := range []string{"dm_db_index_physical_stats", "dm_db_partition_stats"} {
		if strings.Contains(indexCandidatesQuery, bad) || strings.Contains(indexBasicsQuery, bad) {
			t.Errorf("the page load reads %s: that is Measure's job", bad)
		}
	}
	if !strings.Contains(indexBasicsQuery, "sys.allocation_units") || !strings.Contains(indexBasicsQuery, "i.allow_page_locks") {
		t.Error("basics query lacks the allocation units or ALLOW_PAGE_LOCKS")
	}
}
