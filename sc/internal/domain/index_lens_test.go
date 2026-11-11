package domain

import (
	"strings"
	"testing"
	"time"
)

func lensQuery(hash string, elapsedUs int64, plan string) LensQueryFact {
	return LensQueryFact{Hash: hash, Text: "SELECT ...", Runs: 4310, ElapsedUs: elapsedUs, Reads: 1, LastRun: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), Plan: plan}
}

func lensBasics(schema, table, index string, typ int, pages int64) IndexBasicsFact {
	return IndexBasicsFact{Schema: schema, Table: table, Index: index, TableFound: true, IndexFound: true, Type: typ, PageLocks: true, Pages: pages, UsedPages: pages + 20}
}

func TestLensScans(t *testing.T) {
	qs := []LensQueryFact{lensQuery("0xA", 5e6, scanPlan), lensQuery("0xB", 3e6, seekPlan), lensQuery("0xC", 1e6, scanPlan), lensQuery("0xD", 1e6, "")}
	got := map[string][]int{}
	for _, s := range LensScans("Shop", qs) {
		got[s.Table+"."+s.Index] = s.Queries
	}
	// scanPlan scans PK_Orders and IX_Lines_Date; seekPlan scans IX_Lines_Date too (an Index Seek
	// RelOp is not). Seeks, lookups, a heap's table scan and a plan over the size limit add none.
	if len(got) != 2 || len(got["Orders.PK_Orders"]) != 2 || got["Orders.PK_Orders"][0] != 0 || got["Orders.PK_Orders"][1] != 2 ||
		len(got["Lines.IX_Lines_Date"]) != 2 {
		t.Errorf("scans %v", got)
	}
}

func TestBuildIndexLens(t *testing.T) {
	qs := []LensQueryFact{lensQuery("0xA", 5e6, scanPlan), lensQuery("0xB", 9e6, scanPlan)}
	f := LensFacts{Database: "Shop", QueryStoreState: "READ_WRITE", Queries: qs, CanChange: true,
		Basics: []IndexBasicsFact{lensBasics("dbo", "Orders", "PK_Orders", 1, 999), lensBasics("dbo", "Lines", "IX_Lines_Date", 2, 12400),
			lensBasics("dbo", "Lines", "IX_Lines_Order", 2, 50000)},
	}
	l := BuildIndexLens(f)
	// 999 pages is under Ola's floor; IX_Lines_Order is only sought.
	if len(l.Rows) != 1 || l.Rows[0].Index != "IX_Lines_Date" || l.Reason != "" {
		t.Fatalf("rows %+v", l.Rows)
	}
	r := l.Rows[0]
	if r.Pages != 12400 || r.Kind != "nonclustered" || r.QueryCount != 2 || r.TotalMs != 14000 || r.Queries[0].Hash != "0xB" ||
		r.Options[OptionTable] != "[dbo].[Lines]" || r.Options[OptionIndex] != "IX_Lines_Date" || r.Blocked != "" || r.StatsAction != nil {
		t.Errorf("row %+v", r)
	}
	if !strings.Contains(l.Answer, "1 big index in full") || !strings.Contains(l.Answer, "Measure it") || l.MeasureBatch != 20 || l.CapGB != 20 || l.GainPct != 5 {
		t.Errorf("answer %q", l.Answer)
	}
	// 1,000 pages is in.
	f.Basics[0].Pages = 1000
	if l = BuildIndexLens(f); len(l.Rows) != 2 || !strings.Contains(l.Answer, "2 big indexes") {
		t.Errorf("1,000 pages: %d rows, %q", len(l.Rows), l.Answer)
	}
	// Ordered by the queries' total duration: the clustered index here is read by the slower queries.
	f.Queries = []LensQueryFact{lensQuery("0xA", 2e6, scanPlan), lensQuery("0xB", 9e6, strings.Replace(scanPlan, `Index="[PK_Orders]"`, `Index="[PK_Other]"`, 1))}
	f.Basics = append(f.Basics, lensBasics("dbo", "Orders", "PK_Other", 1, 3000))
	l = BuildIndexLens(f)
	if len(l.Rows) != 3 || l.Rows[0].Index != "IX_Lines_Date" && l.Rows[0].Index != "PK_Other" || l.Rows[0].TotalMs < l.Rows[1].TotalMs || l.Rows[1].TotalMs < l.Rows[2].TotalMs {
		t.Errorf("order %+v", l.Rows)
	}
}

func TestBuildIndexLensStates(t *testing.T) {
	off := BuildIndexLens(LensFacts{Database: "Shop", QueryStoreState: "OFF"})
	if !strings.Contains(off.Reason, "Query Store is off in Shop") || off.Answer != off.Reason || len(off.Rows) != 0 {
		t.Errorf("off: %+v", off)
	}
	bad := BuildIndexLens(LensFacts{Database: "Shop", QueryStoreState: "READ_WRITE", Error: "denied"})
	if !strings.Contains(bad.Reason, "denied") {
		t.Errorf("error: %+v", bad)
	}
	none := BuildIndexLens(LensFacts{Database: "Shop", QueryStoreState: "READ_ONLY", Queries: []LensQueryFact{lensQuery("0xA", 1e6, seekPlan)}})
	if none.Reason != "" || len(none.Rows) != 0 || !strings.Contains(none.Answer, "No query in the last 7 days reads an index of 1,000 pages or more in full") ||
		!strings.Contains(none.Answer, "Index work would not help your queries.") {
		t.Errorf("none: %+v", none)
	}
}

func TestLensRowDetails(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	oldest := now.Add(-12 * 24 * time.Hour)
	var qs []LensQueryFact
	for i := 0; i < 5; i++ {
		qs = append(qs, lensQuery("0x"+string(rune('A'+i)), int64(i+1)*1e6, scanPlan))
	}
	locks := lensBasics("dbo", "Lines", "IX_Lines_Date", 2, 12400)
	locks.PageLocks = false
	f := LensFacts{Database: "Shop", QueryStoreState: "READ_WRITE", Queries: qs, CanChange: true, ServerNow: now,
		Basics: []IndexBasicsFact{locks},
		Tables: []QueryTableFact{{Schema: "dbo", Table: "Lines", Found: true, Rows: 4_000_000, Stats: 3, OldestUpdate: &oldest, MaxModPct: 22.3}},
		Changes: []Change{
			{ChangeType: "INDEX", Object: "dbo.Lines.IX_Lines_Date", Description: "Reorganised IX_Lines_Date on dbo.Lines", ChangedAt: now.Add(-48 * time.Hour), Result: "old, verified"},
			{ChangeType: "INDEX", Object: "dbo.Lines.IX_Lines_Date", Description: "Reorganised IX_Lines_Date on dbo.Lines", ChangedAt: now.Add(-2 * time.Hour), Result: "IX_Lines_Date: 12,400 pages to 7,250, verified"},
			{ChangeType: "INDEX", Object: "dbo.Lines.IX_Lines_Date", Description: "Disabled index", ChangedAt: now, Result: "x"},
			{ChangeType: "SETTING", Object: "dbo.Lines.IX_Lines_Date", Description: "Reorganised", ChangedAt: now},
		}}
	r := BuildIndexLens(f).Rows[0]
	if len(r.Queries) != 3 || r.More != 2 || r.QueryCount != 5 || r.Queries[0].TotalMs != 5000 || !strings.HasPrefix(r.Queries[0].Summary, "4,310 runs, 5.0 s") {
		t.Errorf("queries %+v more %d", r.Queries, r.More)
	}
	if !strings.Contains(r.Blocked, "ALLOW_PAGE_LOCKS OFF") {
		t.Errorf("blocked %q", r.Blocked)
	}
	// 12 days old and 22% changed: stale by 5j's test, said in its words, with the action.
	if !r.Stale || !strings.HasPrefix(r.StatsNote, "Statistics are 12 days old and 22% of rows have changed. Update its statistics first") ||
		r.StatsAction == nil || r.StatsAction.FixID != "update-statistics-table" {
		t.Errorf("stats %v %q %+v", r.Stale, r.StatsNote, r.StatsAction)
	}
	if r.Last == nil || r.Last.Result != "IX_Lines_Date: 12,400 pages to 7,250, verified" || !r.Last.At.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("last %+v", r.Last)
	}
	// Changed 14% and updated today is not stale, yet the action stays on the row.
	today := now
	f.Tables[0].OldestUpdate, f.Tables[0].MaxModPct = &today, 14.1
	if r = BuildIndexLens(f).Rows[0]; r.Stale || r.StatsNote != "" || r.StatsAction == nil {
		t.Errorf("fresh stats %v %q %+v", r.Stale, r.StatsNote, r.StatsAction)
	}
	// Read only: no action. A table whose statistics could not be read: neither.
	f.CanChange = false
	if r = BuildIndexLens(f).Rows[0]; r.StatsAction != nil {
		t.Error("an action on a database that cannot be changed")
	}
	f.TablesError = "denied"
	if r = BuildIndexLens(f).Rows[0]; r.Stale || r.StatsAction != nil {
		t.Errorf("tables unread: %+v", r)
	}
	if got := LensStaleTables([]QueryTableFact{{Schema: "dbo", Table: "Lines", Found: true, Rows: 4_000_000, Stats: 3, OldestUpdate: &oldest, MaxModPct: 22.3},
		{Schema: "dbo", Table: "Small", Found: true, Rows: 100, Stats: 1, OldestUpdate: &oldest, MaxModPct: 90}}, now); len(got) != 1 || got[0] != "dbo.Lines" {
		t.Errorf("stale tables %v", got)
	}
}

func TestLensHandover(t *testing.T) {
	f := LensFacts{Database: "Shop", QueryStoreState: "READ_WRITE", Queries: []LensQueryFact{lensQuery("0xA", 5e6, scanPlan)},
		Basics: []IndexBasicsFact{lensBasics("dbo", "Lines", "IX_Lines_Date", 2, 12400)}}
	h := BuildIndexLens(f).Handover
	if !strings.Contains(h, "dbo.Lines.IX_Lines_Date: 12,400 pages") || !strings.Contains(h, "read by 0xA") || !strings.Contains(h, "Maintain indexes optimally") {
		t.Errorf("handover %q", h)
	}
}
