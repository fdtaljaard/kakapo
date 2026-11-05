package domain

import (
	"strings"
	"testing"
	"time"
)

// Archive before an index (IDX10 and QRY01): when a costly query reads a quarter or more of a table
// that only grows and that nothing cleans up, the first advice is to archive the table's old rows.

// archiveFirstPlan is a plan that scans dbo.MessageQueue and dbo.AuditLog, with a missing index hint.
const archiveFirstPlan = `<ShowPlanXML><BatchSequence><Batch><Statements><StmtSimple><QueryPlan>
<MissingIndexes><MissingIndexGroup Impact="91.2"><MissingIndex Database="[WarehouseLive]" Schema="[dbo]" Table="[MessageQueue]">
<ColumnGroup Usage="EQUALITY"><Column Name="[Status]" ColumnId="3"/></ColumnGroup></MissingIndex></MissingIndexGroup></MissingIndexes>
<RelOp NodeId="0"><Hash><RelOp NodeId="1"><IndexScan><Object Database="[WarehouseLive]" Schema="[dbo]" Table="[MessageQueue]" Index="[PK_MessageQueue]" IndexKind="Clustered"/></IndexScan></RelOp>
<RelOp NodeId="2"><IndexScan><Object Database="[WarehouseLive]" Schema="[dbo]" Table="[AuditLog]" Index="[PK_AuditLog]" IndexKind="Clustered"/></IndexScan></RelOp></Hash></RelOp>
</QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

var queueOldest = time.Date(2025, 9, 25, 16, 19, 2, 0, time.UTC)

// messageQueue is 100 MB (12,800 pages), its oldest row from Sep 2025, read through its key.
func messageQueue() GrowingTable {
	return GrowingTable{Schema: "dbo", Name: "MessageQueue", Rows: 2000000, MB: 100, DateCol: "QueuedAt", KeyCol: "ID", Oldest: &queueOldest}
}

// auditLog is 1,000 MB (128,000 pages) whose age is unknown (its date column is not indexed).
func auditLog() GrowingTable {
	return GrowingTable{Schema: "dbo", Name: "AuditLog", Rows: 9000000, MB: 1000, DateCol: "LoggedAt"}
}

// archiveFirstQuery runs 1,000 times a day reading pagesPerRun pages each time, steadily.
func archiveFirstQuery(pagesPerRun int64) TopQuery {
	return TopQuery{QueryHash: "0xAB", Text: "SELECT * FROM dbo.MessageQueue WHERE Status = @0", CPU: 200000000, Execs: 7000,
		Reads: 7000 * pagesPerRun, MinReads: pagesPerRun, MaxReads: pagesPerRun, MaxCPU: 50000, Elapsed: 300000000, Variants: 1,
		Minutes: 10080, Plan: archiveFirstPlan}
}

func archiveFirstFacts(q TopQuery, candidates ...GrowingTable) PerformanceFacts {
	f := qsFacts(q)
	f.GrowingCandidates = candidates
	return f
}

const queueSentence = " A queue's old rows may still be needed (failed rows retried): archive only the finished ones with the archive's condition, " +
	"e.g. the status that means done, and confirm which statuses are finished with the vendor first."

func TestQRY01ArchiveFirst(t *testing.T) {
	// 25% of the queue's pages per run (3,200 of 12,800): archive first, the flag says so, Low, the fix.
	_, fs, _ := runPerf(archiveFirstFacts(archiveFirstQuery(3200), messageQueue()))
	x := one(t, fs, "QRY01")
	wantDo := "This query reads 25% of dbo.MessageQueue (100 MB) every run, a queue table whose oldest row is from Sep 2025 and that nothing cleans up. " +
		"Archive its old rows first (Storage card, Tables row): every run then reads less. Consider an index only if it is still slow after that." +
		queueSentence + " " + qry01Do
	if !strings.HasPrefix(x.DoThis, wantDo) {
		t.Errorf("do this\n got %q\nwant %q", x.DoThis, wantDo)
	}
	if !strings.Contains(x.Finding, " Its plan shows: SQL Server suggests an index for it, but archive dbo.MessageQueue first.") ||
		strings.Contains(x.Finding, "check IDX02 and IDX09") {
		t.Errorf("flag %q", x.Finding)
	}
	if x.Severity != SeverityLow || x.FixID != FixArchiveTable || x.Object != "WarehouseLive" {
		t.Errorf("finding %+v", x)
	}

	// 24% (3,072 pages): a query that reads little of the table gains nothing; the advice as before.
	_, fs, _ = runPerf(archiveFirstFacts(archiveFirstQuery(3072), messageQueue()))
	x = one(t, fs, "QRY01")
	if strings.Contains(x.DoThis, "Archive its old rows") || x.FixID != "" ||
		!strings.Contains(x.Finding, "Its plan shows: "+planMissingIndexWords+".") {
		t.Errorf("24%%: %+v", x)
	}

	// A log table whose age is unknown: "that keeps every row", no queue sentence. The query reads
	// both tables; the one it reads most of is named (100% of the queue, capped, against 30% of the log).
	q := archiveFirstQuery(38400)
	_, fs, _ = runPerf(archiveFirstFacts(q, auditLog()))
	x = one(t, fs, "QRY01")
	if !strings.HasPrefix(x.DoThis, "This query reads 30% of dbo.AuditLog (1,000 MB) every run, a log or history table that keeps every row and that nothing cleans up. "+
		"Archive its old rows first (Storage card, Tables row): every run then reads less. Consider an index only if it is still slow after that. "+qry01Do) {
		t.Errorf("log, age unknown: %q", x.DoThis)
	}
	_, fs, _ = runPerf(archiveFirstFacts(q, auditLog(), messageQueue()))
	if x = one(t, fs, "QRY01"); !strings.HasPrefix(x.DoThis, "This query reads 100% of dbo.MessageQueue (100 MB) every run") {
		t.Errorf("two tables: %q", x.DoThis)
	}

	// No hint: the candidates could not be read (nil), the table is archived by kakapo (not a
	// candidate), the plan does not touch it, or the plan was too large to read.
	archived := GrowingCandidates([]GrowingTable{messageQueue()}, []ArchiveJob{{Name: ArchiveJobName("WarehouseLive", "dbo", "MessageQueue")}},
		"WarehouseLive", setupNow)
	if len(archived) != 0 {
		t.Errorf("an archived table is a candidate: %+v", archived)
	}
	other := archiveFirstQuery(3200)
	other.Plan = strings.ReplaceAll(archiveFirstPlan, "MessageQueue", "Pick")
	tooBig := archiveFirstQuery(3200)
	tooBig.Plan, tooBig.PlanTooBig = "", true
	elsewhere := archiveFirstQuery(3200)
	elsewhere.Plan = strings.Replace(archiveFirstPlan, `<Object Database="[WarehouseLive]" Schema="[dbo]" Table="[MessageQueue]"`,
		`<Object Database="[Elsewhere]" Schema="[dbo]" Table="[MessageQueue]"`, 1)
	for name, f := range map[string]PerformanceFacts{
		"not read":       archiveFirstFacts(archiveFirstQuery(3200)),
		"archived":       archiveFirstFacts(archiveFirstQuery(3200), archived...),
		"other":          archiveFirstFacts(other, messageQueue()),
		"too big":        archiveFirstFacts(tooBig, messageQueue()),
		"other database": archiveFirstFacts(elsewhere, messageQueue()),
	} {
		_, fs, _ := runPerf(f)
		if x := one(t, fs, "QRY01"); strings.Contains(x.DoThis, "Archive its old rows") || x.FixID != "" || strings.Contains(x.Finding, "but archive") {
			t.Errorf("%s: %+v", name, x)
		}
		if fixes := AvailablePerformanceFixes(f, fs); len(fixes) != 0 {
			t.Errorf("%s: fixes %+v", name, fixes)
		}
	}

	// A plan compiled before the database was restored or renamed names its former name throughout
	// (Query Store keeps the plan as compiled): its tables are this database's.
	renamed := archiveFirstQuery(3200)
	renamed.Plan = strings.ReplaceAll(archiveFirstPlan, "[WarehouseLive]", "[WarehouseOld]")
	_, fs, _ = runPerf(archiveFirstFacts(renamed, messageQueue()))
	if x = one(t, fs, "QRY01"); !strings.HasPrefix(x.DoThis, "This query reads 25% of dbo.MessageQueue") || x.FixID != FixArchiveTable {
		t.Errorf("former database name: %+v", x)
	}

	// A table without a date column: the advice, but no fix to apply (as IDX10).
	noDate := messageQueue()
	noDate.DateCol, noDate.KeyCol, noDate.Oldest = "", "", nil
	_, fs, _ = runPerf(archiveFirstFacts(archiveFirstQuery(3200), noDate))
	if x = one(t, fs, "QRY01"); !strings.Contains(x.DoThis, "a queue table that keeps every row") || x.FixID != "" {
		t.Errorf("no date column: %+v", x)
	}
}

// The archive fix on a QRY01 finding names the table it reads most of, the finding's object and its
// query hash, so the card page finds its options; the date column only when it is indexed.
func TestQRY01ArchiveFix(t *testing.T) {
	f := archiveFirstFacts(archiveFirstQuery(3200), messageQueue())
	plain := TopQuery{QueryHash: "0xCD", ObjectName: "dbo.USP_SEND", Text: "SELECT 1", CPU: 100000000, Execs: 100, Reads: 100, MinReads: 1, MaxReads: 1,
		MaxCPU: 50000, Elapsed: 100000000, Variants: 1, Minutes: 10080, Plan: archiveFirstPlan}
	f.TopQueries = append(f.TopQueries, plain)
	_, fs, _ := runPerf(f)
	fixes := AvailablePerformanceFixes(f, fs)
	if len(fixes) != 1 {
		t.Fatalf("fixes %+v", fixes)
	}
	x := fixes[0]
	if x.ID != FixArchiveTable || x.CheckID != "QRY01" || x.Scope != ScopeDatabase || x.Title != "Archive or delete old rows" ||
		len(x.Options) != 3 || x.Options[OptionTable] != "[dbo].[MessageQueue]" || x.Options[OptionObject] != "WarehouseLive" ||
		x.Options[OptionQueryHash] != "0xAB" {
		t.Errorf("fix %+v", x)
	}
	indexed := messageQueue()
	indexed.DateIndexed = true
	f.GrowingCandidates = []GrowingTable{indexed}
	_, fs, _ = runPerf(f)
	if fixes = AvailablePerformanceFixes(f, fs); len(fixes) != 1 || fixes[0].Options[OptionArchiveDateColumn] != "QueuedAt" {
		t.Errorf("indexed date column: %+v", fixes)
	}
}

// GrowingCandidates is IDX10's rule: named, sized, old enough or of unknown age, not archived.
func TestGrowingCandidates(t *testing.T) {
	young := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	small := GrowingTable{Schema: "dbo", Name: "SmallLog", Rows: 900000, MB: 400}
	recent := GrowingTable{Schema: "dbo", Name: "RecentLog", Rows: 2000000, MB: 600, DateCol: "At", DateIndexed: true, Oldest: &young}
	plainName := GrowingTable{Schema: "dbo", Name: "Pick", Rows: 9000000, MB: 900}
	got := GrowingCandidates([]GrowingTable{messageQueue(), small, recent, plainName, auditLog()}, nil, "WarehouseLive", setupNow)
	if len(got) != 2 || got[0].Name != "MessageQueue" || got[1].Name != "AuditLog" {
		t.Errorf("candidates %+v", got)
	}
	if got := GrowingCandidates(nil, nil, "WarehouseLive", setupNow); got == nil || len(got) != 0 {
		t.Errorf("none %+v", got)
	}
}

// The query page says the same and offers the same fix; its missing index sentence says archive first.
func TestQueryDetailArchiveFirst(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	facts := func(pages int64, candidates ...GrowingTable) QueryFacts {
		return QueryFacts{Database: "WarehouseLive", QueryHash: "0xAB", ServerNow: now, UTCOffsetMinutes: 120, QueryStoreState: "OFF",
			CachedPlans: []CachedPlanFact{{PlanHandle: "0x06AA", PlanHash: "0xBB", Runs: 1000, CPU: 1000 * 2000, Duration: 1000 * 4000, MaxDuration: 9000,
				Reads: 1000 * pages, MaxReads: pages, Created: now.Add(-3 * time.Hour), LastRun: now, Text: "SELECT 1", Plan: archiveFirstPlan}},
			GrowingCandidates: candidates}
	}
	d := BuildQueryDetail(facts(3200, messageQueue()), true)
	var line *ExplainLine
	for i, l := range d.Explain {
		if strings.HasPrefix(l.Text, "This query reads") {
			line = &d.Explain[i]
		}
	}
	if line == nil || !strings.HasPrefix(line.Text, "This query reads 25% of dbo.MessageQueue (100 MB) every run, a queue table whose oldest row is from Sep 2025") ||
		!strings.HasSuffix(line.Text, queueSentence) {
		t.Fatalf("explain %+v", d.Explain)
	}
	if a := line.Action; a == nil || a.FixID != FixArchiveTable || a.Options[OptionTable] != "[dbo].[MessageQueue]" || len(a.Options) != 1 ||
		a.Title != "Archive or delete old rows of dbo.MessageQueue" {
		t.Errorf("action %+v", line.Action)
	}
	found := false
	for _, l := range d.Explain {
		if strings.HasPrefix(l.Text, "SQL Server suggests an index on dbo.MessageQueue") {
			found = true
			if !strings.HasSuffix(l.Text, " Archive dbo.MessageQueue first; create the index only if it is still slow after that.") {
				t.Errorf("index line %q", l.Text)
			}
		}
	}
	if !found || !strings.Contains(d.Handover, "This query reads 25% of dbo.MessageQueue") {
		t.Errorf("index line or handover:\n%s", d.Handover)
	}

	// Cannot change the database: the sentence, no action. 24%, or nothing read: no sentence.
	if d := BuildQueryDetail(facts(3200, messageQueue()), false); !strings.Contains(explainText(d), "This query reads 25%") || explainAction(d, FixArchiveTable) {
		t.Errorf("read-only %+v", d.Explain)
	}
	for name, f := range map[string]QueryFacts{"24%": facts(3072, messageQueue()), "not read": facts(3200)} {
		if d := BuildQueryDetail(f, true); strings.Contains(explainText(d), "This query reads") || explainAction(d, FixArchiveTable) ||
			!strings.Contains(explainText(d), "Create the index: the preview checks the existing indexes first.") {
			t.Errorf("%s: %+v", name, d.Explain)
		}
	}
}

func explainText(d QueryDetail) string { return strings.Join(ExplainQuery(d), "\n") }

func explainAction(d QueryDetail, fixID string) bool {
	for _, l := range d.Explain {
		if l.Action != nil && l.Action.FixID == fixID {
			return true
		}
	}
	return false
}
