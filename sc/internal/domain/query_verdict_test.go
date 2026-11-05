package domain

import (
	"strings"
	"testing"
	"time"
)

// A plan as a restored copy keeps it: its objects name the database it was compiled in on production,
// and it asks for an index.
const restoredPlanXML = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.564" Build="16.0.1210.3">
<BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT * FROM dbo.TrackingView ORDER BY Barcode" StatementId="1" StatementType="SELECT">
 <QueryPlan>
  <MissingIndexes><MissingIndexGroup Impact="48"><MissingIndex Database="[ProdLive]" Schema="[dbo]" Table="[ItemAlias]">
   <ColumnGroup Usage="EQUALITY"><Column Name="[Item_id]" ColumnId="2"/></ColumnGroup>
  </MissingIndex></MissingIndexGroup></MissingIndexes>
  <RelOp NodeId="0"><IndexScan><Object Database="[ProdLive]" Schema="[dbo]" Table="[Tracking]" Index="[PK_Tracking]"/></IndexScan></RelOp>
  <RelOp NodeId="1"><IndexScan><Object Database="[ProdLive]" Schema="[dbo]" Table="[ItemAlias]" Index="[PK_ItemAlias]"/></IndexScan></RelOp>
  <RelOp NodeId="2"><IndexScan><Object Database="[tempdb]" Schema="[dbo]" Table="[#w]"/></IndexScan></RelOp>
 </QueryPlan>
</StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

// The 5 Nov review's query, shaped: a slow parallel plan for days, statistics updated, a middle plan,
// an index, the fast plan in use; two statements of one hash, one with a forced plan.
func fixedQueryFacts() QueryFacts {
	now := time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC) // 18:00 on the server (UTC+2)
	slowFrom := time.Date(2026, 10, 30, 6, 0, 0, 0, time.UTC)
	f := QueryFacts{
		Database: "Sandbox", QueryHash: "0xDE6731249BBE798C", ServerNow: now, UTCOffsetMinutes: 120, QueryStoreState: "READ_WRITE",
		Text: "SELECT * FROM dbo.TrackingView ORDER BY Barcode OFFSET 0 ROWS FETCH NEXT 100 ROWS ONLY",
		Plans: []QueryStorePlanFact{
			{PlanID: 35351, QueryID: 7686, Runs: 248, CPU: 248 * 98000000, Duration: 248 * 17000000, Reads: 248 * 2400000, TempdbKB: -1,
				InitialCompile: qPtime(time.Date(2026, 4, 17, 9, 37, 0, 0, time.UTC)), LastRun: qPtime(time.Date(2026, 11, 3, 15, 0, 8, 0, time.UTC)), Plan: restoredPlanXML},
			{PlanID: 269671, QueryID: 7686, Runs: 5, CPU: 5 * 325000, Duration: 5 * 325000, Reads: 5 * 51263, TempdbKB: -1,
				InitialCompile: qPtime(time.Date(2026, 11, 3, 20, 55, 0, 0, time.UTC)), LastRun: qPtime(time.Date(2026, 11, 4, 12, 13, 0, 0, time.UTC)), Plan: restoredPlanXML},
			{PlanID: 268947, QueryID: 1075882, Forced: true, Runs: 1, CPU: 389000, Duration: 254000, Reads: 13312, TempdbKB: -1,
				InitialCompile: qPtime(time.Date(2026, 11, 3, 14, 19, 0, 0, time.UTC)), LastRun: qPtime(time.Date(2026, 11, 3, 14, 19, 27, 0, time.UTC))},
			{PlanID: 270017, QueryID: 7686, Runs: 87, CPU: 87 * 10000, Duration: 87 * 10000, Reads: 87 * 8537, TempdbKB: -1,
				InitialCompile: qPtime(time.Date(2026, 11, 4, 13, 34, 0, 0, time.UTC)), LastRun: qPtime(time.Date(2026, 11, 5, 15, 21, 0, 0, time.UTC))},
		},
		Waits: []QueryWaitFact{{Category: "Parallelism", WaitMs: 42820000}, {Category: "CPU", WaitMs: 13836000}, {Category: "Idle", WaitMs: 2598000}, {Category: "Network IO", WaitMs: 9}},
		Tables: []QueryTableFact{
			{Schema: "dbo", Table: "ItemAlias", Found: true, Rows: 16898, Stats: 8, Updates: []QueryStatUpdateFact{
				{Name: "PK_ItemAlias", Index: true, Updated: time.Date(2026, 11, 3, 17, 16, 10, 0, time.UTC), Rows: 16898, Sampled: 16898},
				{Name: "_WA_Sys_00000002", Updated: time.Date(2026, 11, 3, 17, 16, 20, 0, time.UTC), Rows: 16898, Sampled: 16898},
				{Name: "IX_ItemAlias_Item_id", Index: true, Updated: time.Date(2026, 11, 4, 15, 0, 5, 0, time.UTC), Rows: 16898, Sampled: 16898},
			}},
			{Schema: "dbo", Table: "Tracking", Found: true, Rows: 703694, Stats: 14, Updates: []QueryStatUpdateFact{
				{Name: "PK_Tracking", Index: true, Updated: time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), Rows: 703694, Sampled: 113770},
			}},
		},
	}
	// The slow plan: 8 runs an hour, 08:00 to 16:00 on the server, every day to 3 Nov 17:00.
	for d := slowFrom; d.Before(time.Date(2026, 11, 3, 16, 0, 0, 0, time.UTC)); d = d.Add(time.Hour) {
		if h := d.Hour(); h >= 6 && h <= 15 {
			f.History = append(f.History, QueryHourFact{Hour: d, PlanID: 35351, Runs: 8, CPU: 8 * 98000000, Duration: 8 * 17000000, Reads: 8 * 2400000})
		}
	}
	f.History = append(f.History,
		QueryHourFact{Hour: time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC), PlanID: 268947, Runs: 1, CPU: 389000, Duration: 254000, Reads: 13312},
		QueryHourFact{Hour: time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC), PlanID: 269671, Runs: 1, CPU: 325000, Duration: 325000, Reads: 51263},
		QueryHourFact{Hour: time.Date(2026, 11, 4, 6, 0, 0, 0, time.UTC), PlanID: 269671, Runs: 4, CPU: 4 * 325000, Duration: 4 * 325000, Reads: 4 * 51263},
		QueryHourFact{Hour: time.Date(2026, 11, 4, 13, 0, 0, 0, time.UTC), PlanID: 270017, Runs: 1, CPU: 10000, Duration: 10000, Reads: 8537},
	)
	for h := 6; h <= 15; h++ {
		f.History = append(f.History, QueryHourFact{Hour: time.Date(2026, 11, 5, h, 0, 0, 0, time.UTC), PlanID: 270017, Runs: 9, CPU: 9 * 10000, Duration: 9 * 10000, Reads: 9 * 8537})
	}
	return f
}

// The plans of a restored copy name the database by its old name: they are this database's tables.
func TestShowplanTablesRestoredCopy(t *testing.T) {
	tables := ShowplanTables("Sandbox", restoredPlanXML)
	if len(tables) != 2 || tables[0].Table != "Tracking" || tables[1].Table != "ItemAlias" || tables[0].Database != "Sandbox" {
		t.Errorf("tables %+v", tables)
	}
	// A plan that names this database and another: only this one's tables.
	if got := ShowplanTables("WarehouseLive", queryPlanXML); len(got) != 2 {
		t.Errorf("own database %+v", got)
	}
}

// The plan in use since 4 Nov 15:34 against the runs before: faster, what changed, nothing to do.
func TestQueryVerdictFaster(t *testing.T) {
	d := BuildQueryDetail(fixedQueryFacts(), true)
	v := d.Verdict
	if v == nil {
		t.Fatal("no verdict")
	}
	if v.Verdict != VerdictFaster || v.PlanID != 270017 || !v.Since.Equal(time.Date(2026, 11, 4, 13, 34, 0, 0, time.UTC)) {
		t.Errorf("verdict %s plan %d since %s", v.Verdict, v.PlanID, v.Since)
	}
	if v.Headline != "Each run now takes 10 ms. Before 4 Nov 15:34 it took 17 s." {
		t.Errorf("headline %q", v.Headline)
	}
	if v.Now.Runs != 91 || v.Before.Runs != 406 || v.Before.Label != "30 Oct 08:00 to 4 Nov 09:00" || v.Now.Label != "since 4 Nov 15:34" {
		t.Errorf("before %+v now %+v", v.Before, v.Now)
	}
	if !strings.HasPrefix(v.ToDo, "Nothing: it uses 10 ms of CPU a run now, against 97 s before.") {
		t.Errorf("to do %q", v.ToDo)
	}
	changed := []string{}
	for _, e := range v.Changed {
		changed = append(changed, e.Text)
	}
	want := []string{
		"Statistics updated on dbo.ItemAlias (2 statistics, every row read).",
		"SQL Server began to use plan 269671: 325 ms a run, 51,263 pages read.",
		"Index IX_ItemAlias_Item_id on dbo.ItemAlias: created or rebuilt.",
		"SQL Server began to use plan 270017: 10 ms a run, 8,537 pages read.",
	}
	if strings.Join(changed, "\n") != strings.Join(want, "\n") {
		t.Errorf("changed:\n%s", strings.Join(changed, "\n"))
	}

	text := explainTexts(d)
	for _, w := range []string{
		"Each run now uses 10 ms of CPU and reads 8,537 pages (67 MB) from memory or disk; before 4 Nov 15:34 it used 97 s and read 2,365,196 pages (18,478 MB).",
		"Plan 35351 runs on several CPUs at once (in parallel), so its CPU per run (98 s) is more than its time per run (17 s).",
		"It is 2 statements that differ only in the values typed into them",
		"Plan 268947 is forced for one of its statements: SQL Server must use it there. It ran 1 time in 7 days.",
		"Most of its waiting (72%) is its parallel threads waiting for each other",
		"Nearly all of it is from before 4 Nov 15:34",
	} {
		if !strings.Contains(text, w) {
			t.Errorf("explain lacks %q in:\n%s", w, text)
		}
	}
	for _, no := range []string{"similar speed", "suggests an index", "stop forcing"} {
		if strings.Contains(text, no) {
			t.Errorf("explain has %q in:\n%s", no, text)
		}
	}
	for _, w := range d.Waits {
		if w.Category == "Idle" || w.Category == "Network IO" {
			t.Errorf("wait %s shown", w.Category)
		}
	}
	if len(d.Tables) != 2 {
		t.Errorf("tables %+v", d.Tables)
	}
}

// One plan all week: no verdict, and no events.
func TestQueryVerdictOnePlan(t *testing.T) {
	f := fixedQueryFacts()
	f.Plans = f.Plans[3:]
	keep := []QueryHourFact{}
	for _, h := range f.History {
		if h.PlanID == 270017 {
			keep = append(keep, h)
		}
	}
	f.History, f.Tables = keep, nil
	d := BuildQueryDetail(f, true)
	if d.Verdict != nil || len(d.Events) != 0 {
		t.Errorf("verdict %+v events %+v", d.Verdict, d.Events)
	}
}
