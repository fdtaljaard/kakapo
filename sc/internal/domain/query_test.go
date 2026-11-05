package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A cached plan as Query Store keeps it, trimmed: a missing index hint, a key lookup, a table
// variable, a temporary table, an implicit conversion and two compiled parameters.
const queryPlanXML = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.564" Build="16.0.4255.2">
<BatchSequence><Batch><Statements>
<StmtSimple StatementText="SELECT t.Qty FROM dbo.StockUnit t JOIN dbo.Location l ON l.ID = t.LocationID WHERE l.Code = @code" StatementId="1" StatementType="SELECT">
 <QueryPlan CachedPlanSize="40" CompileTime="3">
  <Warnings><PlanAffectingConvert ConvertIssue="Seek Plan" Expression="CONVERT_IMPLICIT(nvarchar(50),[l].[Code],0)=[@code]"/></Warnings>
  <MissingIndexes>
   <MissingIndexGroup Impact="87.5">
    <MissingIndex Database="[WarehouseLive]" Schema="[dbo]" Table="[StockUnit]">
     <ColumnGroup Usage="EQUALITY"><Column Name="[LocationID]" ColumnId="3"/></ColumnGroup>
     <ColumnGroup Usage="INEQUALITY"><Column Name="[CreatedDate]" ColumnId="7"/></ColumnGroup>
     <ColumnGroup Usage="INCLUDE"><Column Name="[Qty]" ColumnId="5"/><Column Name="[Barcode]" ColumnId="2"/></ColumnGroup>
    </MissingIndex>
   </MissingIndexGroup>
  </MissingIndexes>
  <RelOp NodeId="0" PhysicalOp="Nested Loops">
   <NestedLoops>
    <RelOp NodeId="1"><IndexScan Lookup="true"><Object Database="[WarehouseLive]" Schema="[dbo]" Table="[StockUnit]" Index="[PK_StockUnit]" IndexKind="Clustered"/></IndexScan></RelOp>
    <RelOp NodeId="2"><IndexScan><Object Database="[WarehouseLive]" Schema="[dbo]" Table="[StockUnit]" Index="[IX_TE_Location]"/></IndexScan></RelOp>
    <RelOp NodeId="3"><IndexScan><Object Database="[WarehouseLive]" Schema="[dbo]" Table="[Location]" Index="[IX_Location_Code]"/></IndexScan></RelOp>
    <RelOp NodeId="4"><TableScan><Object Table="[@t]"/></TableScan></RelOp>
    <RelOp NodeId="5"><IndexScan><Object Database="[tempdb]" Schema="[dbo]" Table="[#work]"/></IndexScan></RelOp>
    <RelOp NodeId="6"><IndexScan><Object Database="[OtherDb]" Schema="[dbo]" Table="[Elsewhere]"/></IndexScan></RelOp>
   </NestedLoops>
  </RelOp>
  <ParameterList>
   <ColumnReference Column="@FromDate" ParameterDataType="datetime" ParameterCompiledValue="'2019-01-01 00:00:00.000'"/>
   <ColumnReference Column="@Site" ParameterDataType="int" ParameterCompiledValue="(5)" ParameterRuntimeValue="(7)"/>
  </ParameterList>
 </QueryPlan>
</StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

func TestParseQueryHash(t *testing.T) {
	h, b, err := ParseQueryHash(" 0x9f3a00112233aabb ")
	if err != nil || h != "0x9F3A00112233AABB" || len(b) != 8 || b[0] != 0x9f {
		t.Errorf("hash %q %v %v", h, b, err)
	}
	for _, bad := range []string{"", "9F3A00112233AABB", "0x9F3A", "0x9F3A00112233AABBCC", "0xZZ3A00112233AABB", "'; DROP TABLE x--"} {
		if _, _, err := ParseQueryHash(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The compiled parameters, the missing index hint and the objects, from the plan fixture.
func TestParseShowplan(t *testing.T) {
	sp := ParseShowplan(queryPlanXML)
	if len(sp.Parameters) != 2 {
		t.Fatalf("parameters %+v", sp.Parameters)
	}
	if p := sp.Parameters[0]; p.Name != "@FromDate" || p.DataType != "datetime" || p.CompiledValue != "'2019-01-01 00:00:00.000'" || p.RuntimeValue != "" {
		t.Errorf("first parameter %+v", p)
	}
	if p := sp.Parameters[1]; p.Name != "@Site" || p.CompiledValue != "5" || p.RuntimeValue != "7" {
		t.Errorf("second parameter %+v", p)
	}
	if len(sp.MissingIndexes) != 1 {
		t.Fatalf("missing indexes %+v", sp.MissingIndexes)
	}
	mi := sp.MissingIndexes[0]
	if mi.Table != "[WarehouseLive].[dbo].[StockUnit]" || mi.TableName != "dbo.StockUnit" || mi.Equality != "[LocationID]" ||
		mi.Inequality != "[CreatedDate]" || mi.Included != "[Qty], [Barcode]" || mi.Impact != 87.5 {
		t.Errorf("missing index %+v", mi)
	}
	if len(sp.Objects) != 6 {
		t.Errorf("objects %+v", sp.Objects)
	}

	tables := ShowplanTables("WarehouseLive", queryPlanXML, queryPlanXML)
	if len(tables) != 2 || tables[0].Table != "StockUnit" || tables[0].Index != "PK_StockUnit\nIX_TE_Location" ||
		tables[1].Table != "Location" || tables[1].Schema != "dbo" {
		t.Errorf("tables %+v", tables)
	}

	// A declaration SQL Server writes as utf-16 still parses; garbage gives nothing, not a panic.
	if sp := ParseShowplan(`<?xml version="1.0" encoding="utf-16"?>` + queryPlanXML); len(sp.Parameters) != 2 {
		t.Errorf("utf-16 declaration: %+v", sp.Parameters)
	}
	if sp := ParseShowplan("<ShowPlanXML><unclosed"); len(sp.Parameters) != 0 || sp.Objects == nil {
		t.Errorf("broken plan %+v", sp)
	}
}

func qPtime(t time.Time) *time.Time { return &t }

// Two plans, the one in use six times slower than the other, with the plan fixture, a stale table,
// mostly lock waits and a procedure.
func regressedQueryFacts() QueryFacts {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	return QueryFacts{
		Database: "WarehouseLive", QueryHash: "0x9F3A00112233AABB", ServerName: "WMS-SQL01", ProductVersion: "16.0.4255.2",
		ServerNow: now, UTCOffsetMinutes: 120, QueryStoreState: "READ_WRITE",
		Text: "SELECT t.Qty FROM dbo.StockUnit t ...", ObjectName: "dbo.PickList",
		Plans: []QueryStorePlanFact{
			{PlanID: 12, QueryID: 345, PlanHash: "0x00000000000000AA", Runs: 10000, CPU: 10000 * 40000, MaxCPU: 900000,
				Duration: 10000 * 100000, MaxDuration: 2000000, Reads: 10000 * 3000, MaxReads: 9000, TempdbKB: -1,
				LastRun: qPtime(now.Add(-26 * time.Hour)), LastCompile: qPtime(now.Add(-200 * time.Hour))},
			{PlanID: 15, QueryID: 345, PlanHash: "0x00000000000000BB", Runs: 1240, CPU: 1240 * 300000, MaxCPU: 9000000,
				Duration: 1240 * 600000, MaxDuration: 41000000, Reads: 1240 * 42000, MaxReads: 900000, TempdbKB: 1240 * 2048,
				LastRun: qPtime(now.Add(-15 * time.Minute)), LastCompile: qPtime(now.Add(-26 * time.Hour)), Plan: queryPlanXML},
		},
		History: []QueryHourFact{
			{Hour: now.Add(-1 * time.Hour), PlanID: 15, Runs: 40, CPU: 40 * 300000, Duration: 40 * 600000, Reads: 40 * 42000},
			{Hour: now.Add(-26 * time.Hour), PlanID: 15, Runs: 10, CPU: 10 * 300000, Duration: 10 * 600000, Reads: 10 * 42000},
			{Hour: now.Add(-26 * time.Hour), PlanID: 12, Runs: 100, CPU: 100 * 40000, Duration: 100 * 100000, Reads: 100 * 3000},
		},
		Waits: []QueryWaitFact{{Category: "Lock", WaitMs: 1000000}, {Category: "CPU", WaitMs: 400000}},
		CachedPlans: []CachedPlanFact{
			{PlanHandle: "0x0600050012345678", PlanHash: "0x00000000000000BB", Runs: 300, Created: now.Add(-26 * time.Hour), LastRun: now},
		},
		Statements: []ProcStatementFact{
			{Line: 12, Text: "SELECT t.Qty FROM ...", QueryHash: "0x9F3A00112233AABB", Runs: 300, CPU: 300 * 300000, MinReads: 100, MaxReads: 900000, Reads: 7200000},
			{Line: 30, Text: "UPDATE dbo.PickList SET ...", QueryHash: "0x0000000000000001", Runs: 300, CPU: 300 * 1000, MinReads: 10, MaxReads: 20, Reads: 2800000},
		},
		Tables: []QueryTableFact{
			{Schema: "dbo", Table: "Location", Found: true, Rows: 300, Stats: 2, OldestUpdate: qPtime(now.Add(-24 * time.Hour)), MaxModPct: 1},
			{Schema: "dbo", Table: "StockUnit", Found: true, Rows: 4200000, Stats: 5,
				OldestUpdate: qPtime(now.Add(2*time.Hour - 31*24*time.Hour)), MaxModPct: 48, Modifications: 2000000},
		},
	}
}

func explainTexts(d QueryDetail) string { return strings.Join(ExplainQuery(d), "\n") }

func TestQueryDetailRegressedPlan(t *testing.T) {
	d := BuildQueryDetail(regressedQueryFacts(), true)
	if d.Source != QueryDetailFromQueryStore || len(d.Plans) != 2 {
		t.Fatalf("source %s plans %+v", d.Source, d.Plans)
	}
	active, fast := d.Plans[0], d.Plans[1]
	if active.PlanID != 15 || !active.Active || active.Fastest || active.PlanHandle != "0x0600050012345678" ||
		fast.PlanID != 12 || !fast.Fastest || fast.Active || fast.PlanHandle != "" {
		t.Errorf("active/fastest %+v / %+v", active, fast)
	}
	if active.AvgDurationMs != 600 || active.MaxDurationMs != 41000 || active.AvgReads != 42000 || active.TempdbMBPerRun == nil || *active.TempdbMBPerRun != 2 ||
		fast.TempdbMBPerRun != nil {
		t.Errorf("numbers %+v", active)
	}
	if len(active.Parameters) != 2 || active.Conversion == "" || len(active.MissingIndexes) != 1 || active.PlanXML == "" {
		t.Errorf("what the plan says %+v", active)
	}
	if a := active.MissingIndexes[0].Action; a == nil || a.FixID != "create-index" || a.Options["source"] != "plan" || a.Options["planId"] != "15" ||
		a.Options["table"] != "[WarehouseLive].[dbo].[StockUnit]" || a.Options["equality"] != "[LocationID]" ||
		a.Options["inequality"] != "[CreatedDate]" || a.Options["included"] != "[Qty], [Barcode]" {
		t.Errorf("create-index action %+v", a)
	}
	ids := func(as []QueryAction) string {
		s := []string{}
		for _, a := range as {
			s = append(s, a.FixID)
		}
		return strings.Join(s, ",")
	}
	if ids(active.Actions) != "force-plan,evict-plan" || ids(fast.Actions) != "force-plan" ||
		active.Actions[1].Options["planHandle"] != "0x0600050012345678" || active.Actions[1].Options["queryHash"] != d.QueryHash {
		t.Errorf("plan actions %+v / %+v", active.Actions, fast.Actions)
	}

	if d.Summary.Runs != 11240 || d.Summary.MaxDurationMs != 41000 || d.Summary.Window != "7 days" {
		t.Errorf("summary %+v", d.Summary)
	}
	if len(d.History) != 2 || d.History[0].Runs != 40 || d.History[1].Runs != 110 || len(d.History[1].Plans) != 2 || d.History[1].Plans[0] != 12 {
		t.Errorf("history %+v", d.History)
	}
	if len(d.Waits) != 2 || d.Waits[0].Category != "Lock" || d.Waits[0].Pct != 71.4 || !strings.Contains(d.Waits[0].Words, "locks") {
		t.Errorf("waits %+v", d.Waits)
	}
	if len(d.Statements) != 2 || d.Statements[0].Line != 12 || !d.Statements[0].ThisQuery || d.Statements[0].PctReads != 72 ||
		strings.Join(d.Statements[0].LookAt, "; ") != "most of the cost of this procedure; cost depends on the input: test with real values; reads many pages every run" {
		t.Errorf("statements %+v", d.Statements)
	}
	te := d.Tables[0]
	if te.Name != "dbo.StockUnit" || !te.Stale || te.AgeDays == nil || *te.AgeDays != 31 || te.Action == nil ||
		te.Action.Options["table"] != "[dbo].[StockUnit]" || strings.Join(te.Indexes, ",") != "PK_StockUnit,IX_TE_Location" ||
		d.Tables[1].Stale {
		t.Errorf("tables %+v", d.Tables)
	}

	want := []string{
		"It ran 11,240 times in 7 days, 155 ms each on average; the slowest run took 41 s.",
		"Two plans: the one in use since 2 Nov 10:00 is 6x slower than plan 12 (600 ms against 100 ms on average, 1,240 runs against 10,000 runs). Plan 12 uses 7.5x less CPU and reads 14x fewer pages per run, so the plan itself does less work. Force plan 12 so SQL Server uses the faster one.",
		"Its parameters were compiled with @FromDate = '2019-01-01 00:00:00.000' and @Site = 5, which may not be typical. Its runs swing from 600 ms on average to 41 s",
		"Remove the plan from the cache so the next run compiles for its own values.",
		"Its plan converts a column's type for every row (CONVERT_IMPLICIT(nvarchar(50),[l].[Code],0)=[@code])",
		"Its plan shows key lookups (the index used does not hold every column needed) and reads a table variable (no statistics, estimated as 1 row).",
		"SQL Server suggests an index on dbo.StockUnit (LocationID, CreatedDate) that it estimates would cut this query's cost by 88%. Create the index",
		"It reads dbo.StockUnit (4.2 M rows) whose statistics are 31 days old with 48% of rows changed. Update statistics on it.",
		"Most of its waiting (71%) is spent waiting for locks held by other sessions (blocking).",
		"In dbo.PickList, the statement on line 12 does 72% of the procedure's page reads; it is this query.",
	}
	text := explainTexts(d)
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("explain lacks %q in:\n%s", w, text)
		}
	}
	// Each sentence with an action names it, and carries it.
	actions := map[string]string{}
	for _, l := range d.Explain {
		if l.Action != nil {
			actions[l.Action.FixID] = l.Text
		}
	}
	if len(actions) != 4 || !strings.HasPrefix(actions["force-plan"], "Two plans") || !strings.HasPrefix(actions["evict-plan"], "Its parameters") ||
		!strings.HasPrefix(actions["create-index"], "SQL Server suggests") || !strings.HasPrefix(actions["update-statistics-table"], "It reads dbo.StockUnit") {
		t.Errorf("actions %+v", actions)
	}
	for _, l := range d.Explain {
		if strings.Contains(l.Text, "Two plans") && l.Action.Options["planId"] != "12" {
			t.Errorf("force action %+v", l.Action)
		}
	}

	// The hand-over for the vendor: the numbers, the warnings, the parameters, the text.
	for _, w := range []string{"Query 0x9F3A00112233AABB in WarehouseLive on WMS-SQL01", "Procedure: dbo.PickList", "Runs: 11,240.",
		"- plan 15 (in use): 1,240 runs", "- plan 12 (fastest)", "- Implicit conversion: CONVERT_IMPLICIT", "keys LocationID, CreatedDate include Qty, Barcode, impact 88%",
		"- @Site (int) = 5, last run with 7", "Query text\nSELECT t.Qty"} {
		if !strings.Contains(d.Handover, w) {
			t.Errorf("handover lacks %q:\n%s", w, d.Handover)
		}
	}

	// The JSON the page reads: arrays, never null.
	raw, _ := json.Marshal(d)
	for _, k := range []string{`"explain":[`, `"plans":[`, `"history":[`, `"waits":[`, `"statements":[`, `"tables":[`, `"notes":[]`, `"actions":[`, `"parameters":[`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("JSON lacks %s", k)
		}
	}

	// A database that cannot be changed from here offers no database fix, only the plan cache's.
	ro := BuildQueryDetail(regressedQueryFacts(), false)
	if ids(ro.Plans[0].Actions) != "evict-plan" || ro.Plans[0].MissingIndexes[0].Action != nil || ro.Tables[0].Action != nil {
		t.Errorf("read-only actions %+v", ro.Plans[0])
	}
}

// A forced plan that is slower than the other, with forcing failures: stop forcing it.
func TestQueryDetailForcedPlan(t *testing.T) {
	f := regressedQueryFacts()
	f.Plans[1].Forced, f.Plans[1].ForceFailures, f.Plans[1].ForceFailureReason = true, 3, "NO_INDEX"
	d := BuildQueryDetail(f, true)
	text := explainTexts(d)
	if !strings.Contains(text, "Plan 15 is forced: SQL Server must use it. Forcing it has failed 3 times (no_index), so SQL Server may be compiling its own plan instead. Plan 12 ran faster (100 ms against 600 ms): stop forcing plan 15.") {
		t.Errorf("forced:\n%s", text)
	}
	if d.Plans[0].Actions[0].FixID != "unforce-plan" {
		t.Errorf("forced plan actions %+v", d.Plans[0].Actions)
	}
}

// Query Store off: the plan cache's figures, and the Query Store fix offered.
func TestQueryDetailFromPlanCache(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	f := QueryFacts{Database: "WarehouseLive", QueryHash: "0x9F3A00112233AABB", ServerNow: now, UTCOffsetMinutes: 120, QueryStoreState: "OFF",
		CachedPlans: []CachedPlanFact{{PlanHandle: "0x06AA", PlanHash: "0xBB", Runs: 50, CPU: 50 * 2000, Duration: 50 * 4000, MaxDuration: 9000,
			Reads: 50 * 10, MaxReads: 20, Created: now.Add(-3 * time.Hour), LastRun: now, Text: "SELECT 1", Plan: queryPlanXML}},
		WaitsError: "", PlanCacheError: ""}
	d := BuildQueryDetail(f, true)
	if d.Source != QueryDetailFromPlanCache || d.Text != "SELECT 1" || len(d.Plans) != 1 || !d.Plans[0].Active || d.Plans[0].Fastest ||
		d.Summary.Window != "since 3 Nov 09:00" && d.Summary.Window != "since 09:00" {
		t.Fatalf("detail %+v", d)
	}
	last := d.Explain[len(d.Explain)-1]
	if !strings.HasPrefix(last.Text, "Query Store is off for WarehouseLive, so these figures come from the plan cache since 09:00") ||
		last.Action == nil || last.Action.FixID != FixQueryStore {
		t.Errorf("last line %+v", last)
	}
	if !strings.HasPrefix(d.Explain[0].Text, "It ran 50 times since 09:00, 4 ms each on average") {
		t.Errorf("first line %q", d.Explain[0].Text)
	}

	// Nothing anywhere: one sentence, and the areas not read are named.
	none := BuildQueryDetail(QueryFacts{Database: "WarehouseLive", QueryHash: "0x01", ServerNow: now, QueryStoreState: "READ_WRITE",
		PlanCacheError: "the login does not have VIEW SERVER STATE."}, true)
	if none.Source != QueryDetailFromNothing || len(none.Explain) != 1 || !strings.HasPrefix(none.Explain[0].Text, "Nothing is known") ||
		len(none.Notes) != 1 || none.Notes[0] != "Plan cache: could not be read: the login does not have VIEW SERVER STATE." {
		t.Errorf("none %+v", none)
	}
}

func TestQueryFormatting(t *testing.T) {
	for ms, want := range map[float64]string{0: "0 ms", 0.4: "under 1 ms", 45: "45 ms", 900: "900 ms", 2340: "2.3 s", 41000: "41 s", 210000: "3.5 min"} {
		if got := QueryDuration(ms); got != want {
			t.Errorf("QueryDuration(%v) = %q, want %q", ms, got, want)
		}
	}
	for n, want := range map[int64]string{980: "980", 45210: "45,210", 4200000: "4.2 M"} {
		if got := QueryRows(n); got != want {
			t.Errorf("QueryRows(%d) = %q", n, got)
		}
	}
	if QueryWaitWords("buffer io") != "reading data pages from disk" || QueryWaitWords("Something New") != "something new" {
		t.Error("wait words")
	}
}

// A missing index hint of a plan compiled before a restore or rename names the database it had then:
// the create-index action takes this database's name. A hint about another database the plan really
// reads (it also names this one) offers no index.
func TestThisDatabaseTable(t *testing.T) {
	old := Showplan{Objects: []ShowplanObject{{Database: "LiveDb", Schema: "dbo", Table: "Queue"}}}
	if got, ok := qThisDatabaseTable("[LiveDb].[dbo].[Queue]", "Copy", old); !ok || got != "[Copy].[dbo].[Queue]" {
		t.Errorf("restored copy %q %v", got, ok)
	}
	if got, ok := qThisDatabaseTable("[Copy].[dbo].[Queue]", "Copy", old); !ok || got != "[Copy].[dbo].[Queue]" {
		t.Errorf("this database %q %v", got, ok)
	}
	cross := Showplan{Objects: []ShowplanObject{{Database: "Copy", Schema: "dbo", Table: "A"}, {Database: "Other", Schema: "dbo", Table: "B"}}}
	if _, ok := qThisDatabaseTable("[Other].[dbo].[B]", "Copy", cross); ok {
		t.Error("an index offered in another database")
	}
}

// The force advice holds back when the faster plan ran only a few times, and when it does the same work
// per run (CPU and pages read) and only waited less.
func TestQueryDetailForceAdviceEvidence(t *testing.T) {
	explain := func(f QueryFacts) string {
		var lines []string
		for _, e := range BuildQueryDetail(f, true).Explain {
			lines = append(lines, e.Text)
		}
		return strings.Join(lines, "\n")
	}
	few := regressedQueryFacts()
	few.Plans[0].Runs, few.Plans[0].CPU, few.Plans[0].Duration, few.Plans[0].Reads = 2, 2*40000, 2*100000, 2*3000
	if got := explain(few); !strings.Contains(got, "But plan 12 ran only 2 times: compare it on more runs before forcing it.") ||
		strings.Contains(got, "Force plan 12 so") {
		t.Errorf("few runs:\n%s", got)
	}
	waiting := regressedQueryFacts()
	waiting.Plans[0].CPU, waiting.Plans[0].Reads = 10000*290000, 10000*41000
	if got := explain(waiting); !strings.Contains(got, "CPU and pages read per run are similar, so the difference is in elapsed time (waiting, not the work done)") ||
		strings.Contains(got, "does less work") || strings.Contains(got, "Force plan 12 so") {
		t.Errorf("waiting:\n%s", got)
	}
	// Fewer reads alone is enough evidence of less work, even with a little more CPU.
	reads := regressedQueryFacts()
	reads.Plans[0].CPU = 10000 * 320000
	if got := explain(reads); !strings.Contains(got, "Plan 12 reads 14x fewer pages per run, so the plan itself does less work. Force plan 12 so") {
		t.Errorf("reads only:\n%s", got)
	}
}
