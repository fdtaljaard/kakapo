package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Two fixture blockers in WarehouseLive (database id 7), on a server two hours ahead of UTC:
//   - session 83 (AppScheduler), sleeping with a transaction open: two episodes (transactions 9001 and
//     9002), the first seen in two snapshots with sessions 91 and 95 behind it and then 102 behind 91;
//     the statement's literals differ between the episodes;
//   - session 64, an RPC call to dbo.PostIntegration under SERIALIZABLE: one episode, session 70 behind it.
//
// And two reports outside the report: one older than the window, one of another database.
var bhNow = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func bhSleeping(xact int64, stmt string) BlockedProcess {
	return BlockedProcess{SPID: 83, Login: "appsvc", Host: "APP01", Program: "AppScheduler", Database: "WarehouseLive", DatabaseID: 7,
		Status: "sleeping", Isolation: "read committed (2)", TranCount: 1, XactID: xact, InputBuffer: stmt}
}

func bhVictim(spid int, waitMs int64, resource, object, stmt string) BlockedProcess {
	return BlockedProcess{SPID: spid, Login: "picker", Host: "HH-0" + string(rune('0'+spid%10)), Program: "Scanner", Database: "WarehouseLive",
		DatabaseID: 7, Status: "suspended", Isolation: "read committed (2)", WaitMs: waitMs, WaitResource: resource, LockMode: "U",
		Object: object, InputBuffer: stmt}
}

func bhReports() []BlockedProcessReport {
	key := "KEY: 7:72057594043039744 (8194443284a0)"
	s1 := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	s2 := s1.Add(5 * time.Minute)
	s3 := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	s4 := time.Date(2026, 11, 4, 7, 30, 0, 0, time.UTC)
	a1 := bhSleeping(9001, "BEGIN TRAN;\r\n  UPDATE dbo.Pick SET Qty = 5 WHERE Id = 1042")
	a2 := bhSleeping(9002, "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 7 WHERE Id = 2001")
	upd := "UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042"
	v91 := bhVictim(91, 120000, key, "dbo.Pick", upd)
	v91later := bhVictim(91, 300000, key, "dbo.Pick", upd)
	v91later.SPID = 91
	serial := BlockedProcess{SPID: 64, Login: "integration", Host: "INT01", Program: "Integration", Database: "WarehouseLive", DatabaseID: 7,
		Status: "running", Isolation: "serializable (4)", TranCount: 1, XactID: 5000, Procedure: "dbo.PostIntegration",
		InputBuffer: "Proc [Database Id = 7 Object Id = 1234]", LastBatchStarted: s4.Add(2*time.Hour - 10*time.Second), QueryHash: ""}
	other := bhVictim(40, 50000, "KEY: 9:72057594000000000 (aa)", "", "SELECT 1")
	other.Database, other.DatabaseID = "WebShop", 9
	return []BlockedProcessReport{
		{EventUTC: s1, MonitorLoop: 100, Blocked: v91, Blocking: a1},
		{EventUTC: s1, MonitorLoop: 100, Blocked: bhVictim(95, 60000, key, "dbo.Pick", "SELECT * FROM dbo.Pick WHERE Id = 1042"), Blocking: a1},
		{EventUTC: s2, MonitorLoop: 160, Blocked: v91later, Blocking: a1},
		{EventUTC: s2, MonitorLoop: 160, Blocked: bhVictim(95, 360000, key, "dbo.Pick", "SELECT * FROM dbo.Pick WHERE Id = 1042"), Blocking: a1},
		{EventUTC: s2, MonitorLoop: 160, Blocked: bhVictim(102, 30000, "KEY: 7:72057594043039744 (11aa)", "dbo.Pick", "UPDATE dbo.Pick SET Done = 1"),
			Blocking: bhVictim(91, 300000, key, "dbo.Pick", upd)},
		{EventUTC: s3, MonitorLoop: 900, Blocked: bhVictim(91, 20000, key, "dbo.Pick", upd), Blocking: a2},
		{EventUTC: s4, MonitorLoop: 1200, Blocked: bhVictim(70, 12000, "KEY: 7:72057594043105280 (77)", "dbo.Stock", "UPDATE dbo.Stock SET Qty = Qty - 1"),
			Blocking: serial},
		// Outside the report: older than the window, and another database.
		{EventUTC: bhNow.AddDate(0, 0, -9), Blocked: v91, Blocking: a1},
		{EventUTC: s3, Blocked: other, Blocking: bhSleeping(1, "SELECT 1")},
	}
}

func bhBuild(t *testing.T) BlockingHistory {
	t.Helper()
	return BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Duration: 2345 * time.Millisecond,
		Days: 7, Source: DeadlockSourceFile, OffsetMinutes: 120, Reports: bhReports(), Notes: []string{"a note"}})
}

func TestBuildBlockingHistory(t *testing.T) {
	h := bhBuild(t)
	if h.Days != 7 || h.Source != "file" || h.DurationSeconds != 2.345 || !h.CapturedAt.Equal(bhNow) || h.ReportsTotal != 7 || h.Snapshots != 4 ||
		h.ThresholdSeconds != 0 || h.MissedNote != MissedNote(0) || len(h.Notes) != 1 || len(h.Leaders) != 2 || len(h.Findings) != 2 {
		t.Fatalf("history %+v", h)
	}

	a := h.Leaders[0]
	if a.BlockerStatement != "BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" || a.Procedure != "" || a.Object != "dbo.Pick" ||
		a.Key != a.BlockerStatement+"|dbo.Pick" || a.Occurrences != 2 || a.Snapshots != 3 || a.TotalBlockedSessions != 4 || a.MostAtOnce != 3 ||
		a.LongestWaitSec != 360 || strings.Join(a.Kinds, ",") != "sleeping" || a.BlockerProgram != "AppScheduler" || a.BlockerLogin != "appsvc" ||
		a.BlockerHost != "APP01" || a.Database != "WarehouseLive" || a.LockMode != "U" ||
		!a.First.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)) || !a.Last.Equal(time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)) ||
		a.SampleBlockerStatement != "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 7 WHERE Id = 2001" || a.SampleVictimStatement != "UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042" {
		t.Errorf("sleeping leader %+v", a)
	}
	if len(a.ByDay) != 2 || a.ByDay[0] != (BlockingDay{"2026-11-02", 1}) || a.ByDay[1] != (BlockingDay{"2026-11-03", 1}) {
		t.Errorf("by day %+v", a.ByDay)
	}
	b := h.Leaders[1]
	if b.BlockerStatement != "dbo.PostIntegration" || b.Procedure != "dbo.PostIntegration" || b.Object != "dbo.Stock" || b.Occurrences != 1 ||
		b.Snapshots != 1 || b.TotalBlockedSessions != 1 || b.LongestWaitSec != 12 || strings.Join(b.Kinds, ",") != "serializable" {
		t.Errorf("serializable leader %+v", b)
	}

	// BLK01: the sleeping blocker came back with a 6-minute wait (High, for the vendor); the serializable
	// one was seen once (Info, under the finding bar, still the vendor's).
	x := h.Findings[0]
	if x.CheckID != "BLK01" || x.Title != "Blocking history" || x.Severity != SeverityHigh || x.Owner != OwnerVendor || x.Clock != ClockWindow ||
		x.When != WhenDecideFirst || x.Effort != EffortModerate || x.Why != wrk01Why[waitLOCK] || x.DoThis != blk01DoSleeping ||
		x.Object != `"BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" on dbo.Pick` ||
		x.Finding != `"BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" blocked 4 sessions twice in 7 days on dbo.Pick, the longest for 6 minutes; `+
			`the blocker held its transaction open while doing nothing (sleeping), the last on Tue 2026-11-03 15:00 (server time).` ||
		x.Evidence != "Blocker statement: BEGIN TRAN; UPDATE dbo.Pick SET Qty = 7 WHERE Id = 2001 (AppScheduler, appsvc, APP01). "+
			"Blocked statement: UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042. Lock wanted: U. "+
			"Object: dbo.Pick. Seen in 3 five-minute snapshots, 4 sessions blocked in all, at most 3 at once. By day: 2026-11-02 1, 2026-11-03 1." {
		t.Errorf("BLK01 sleeping %+v", x)
	}
	y := h.Findings[1]
	if y.Severity != SeverityInfo || y.Owner != OwnerVendor || y.DoThis != blk01DoSerializable || y.Object != "dbo.PostIntegration on dbo.Stock" ||
		y.Finding != "dbo.PostIntegration blocked 1 session once in 7 days on dbo.Stock, the longest for 12 seconds; "+
			"the blocker used the SERIALIZABLE isolation level, the last on Wed 2026-11-04 09:30 (server time)." {
		t.Errorf("BLK01 serializable %+v", y)
	}

	// The heat line: every hour of the window on the server's clock, the reports in each.
	if len(h.ByHour) != 7*24+1 || h.ByHour[0].Hour != "2026-10-28T12:00" || h.ByHour[len(h.ByHour)-1].Hour != "2026-11-04T12:00" {
		t.Fatalf("hours %d %+v %+v", len(h.ByHour), h.ByHour[0], h.ByHour[len(h.ByHour)-1])
	}
	total := 0
	for _, hr := range h.ByHour {
		total += hr.Reports
		switch hr.Hour {
		case "2026-11-02T10:00":
			if hr.Reports != 5 || hr.SessionsBlocked != 3 {
				t.Errorf("Monday 10:00 %+v", hr)
			}
		case "2026-11-03T15:00", "2026-11-04T09:00":
			if hr.Reports != 1 || hr.SessionsBlocked != 1 {
				t.Errorf("%s %+v", hr.Hour, hr)
			}
		}
	}
	if total != h.ReportsTotal {
		t.Errorf("hours hold %d reports", total)
	}

	// The JSON the panel reads.
	j, _ := json.Marshal(h)
	for _, want := range []string{`"capturedAt":"2026-11-04T10:00:00Z"`, `"source":"file"`, `"snapshots":4`, `"reportsTotal":7`, `"thresholdSeconds":0`,
		`"missedNote":"system_health looks every five minutes`, `"occurrences":2`, `"snapshots":3`, `"longestWaitSec":360`, `"totalBlockedSessions":4`,
		`"kinds":["sleeping"]`, `"byDay":[{"day":"2026-11-02","count":1},{"day":"2026-11-03","count":1}]`, `"procedure":"dbo.PostIntegration"`,
		`"byHour":[{"hour":"2026-10-28T12:00","reports":0,"sessionsBlocked":0}`, `"notes":["a note"]`} {
		if !strings.Contains(string(j), want) {
			t.Errorf("JSON lacks %s", want)
		}
	}
	if strings.Contains(string(j), "MostAtOnce") || strings.Contains(string(j), "queryHash") {
		t.Errorf("JSON %s", j)
	}

	// Nothing in the window: [] everywhere.
	empty := BuildBlockingHistory(BlockingHistoryInputs{Database: "Other", DatabaseID: 12, CapturedAt: bhNow, Days: 3, Source: DeadlockSourceRingBuffer,
		ThresholdSeconds: 20, Reports: bhReports()})
	j, _ = json.Marshal(empty)
	if empty.ReportsTotal != 0 || empty.Days != 3 || len(empty.ByHour) != 3*24+1 || empty.MissedNote != MissedNote(20) ||
		!strings.Contains(string(j), `"leaders":[]`) || !strings.Contains(string(j), `"findings":[]`) || !strings.Contains(string(j), `"notes":[]`) {
		t.Errorf("empty %s", j)
	}
}

func TestNormalizeStatement(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT  *\r\n FROM dbo.T1 WHERE a = N'x''y' AND b = 12.5 AND c = 0x1F AND [Col 2] = @p1": "SELECT * FROM dbo.T1 WHERE a = ? AND b = ? AND c = ? AND [Col 2] = @p1",
		"UPDATE t SET x = -1e5, y = 'it''s', z = \"col 9\"":                                       "UPDATE t SET x = -?, y = ?, z = \"col 9\"",
		"SELECT TOP (10) Id FROM #t2 WHERE n IN (1,2, 3)":                                         "SELECT TOP (?) Id FROM #t2 WHERE n IN (?,?, ?)",
		"":                             "",
		"SELECT name FROM sys.objects": "SELECT name FROM sys.objects",
	} {
		if got := NormalizeStatement(in); got != want {
			t.Errorf("NormalizeStatement(%q) = %q, want %q", in, got, want)
		}
	}
	for buf, want := range map[string][2]string{
		"EXEC dbo.USP_PICK @Id = 5, @Name = 'abc'": {"EXEC dbo.USP_PICK @Id = ?, @Name = ?", "dbo.USP_PICK"},
		"exec @rc = [dbo].[Post] 7":                {"exec @rc = [dbo].[Post] ?", "dbo.Post"},
		"Proc [Database Id = 7 Object Id = 1234]":  {"Proc [Database Id = 7 Object Id = 1234]", ""},
		"  SELECT 1  ": {"SELECT ?", ""},
	} {
		stmt, proc := BlockerStatement(BlockedProcess{InputBuffer: buf})
		if stmt != want[0] || proc != want[1] {
			t.Errorf("BlockerStatement(%q) = %q, %q", buf, stmt, proc)
		}
	}
	if s, p := BlockerStatement(BlockedProcess{InputBuffer: "Proc [Database Id = 7 Object Id = 1234]", Procedure: "dbo.X"}); s != "dbo.X" || p != "dbo.X" {
		t.Errorf("a named procedure %q %q", s, p)
	}
}

func TestWaitResources(t *testing.T) {
	for in, want := range map[string]struct {
		db  int
		key string
	}{
		"KEY: 7:72057594043039744 (8194443284a0)": {7, "h:72057594043039744"},
		"OBJECT: 7:245575913:0 ":                  {7, "o:245575913"},
		"TAB: 7:245575913:0":                      {7, "o:245575913"},
		"PAGE: 7:1:12345 ":                        {7, "p:1:12345"},
		"RID: 7:1:12345:2":                        {7, "p:1:12345"},
		"METADATA: database_id = 7 SCHEMA":        {0, ""},
		"":                                        {0, ""},
	} {
		if db, key := WaitResourceDatabase(in); db != want.db || key != want.key {
			t.Errorf("WaitResourceDatabase(%q) = %d %q", in, db, key)
		}
	}
	if db, obj, ok := ProcInputBuffer("Proc [Database Id = 7 Object Id = 1234]"); !ok || db != 7 || obj != 1234 {
		t.Errorf("proc %d %d %v", db, obj, ok)
	}
	if _, _, ok := ProcInputBuffer("EXEC dbo.X"); ok {
		t.Error("a batch is not an RPC call")
	}
}

// BLK01's thresholds and owner by kind.
func TestBlk01Rules(t *testing.T) {
	for _, c := range []struct {
		l    BlockingLeader
		want Severity
	}{
		{BlockingLeader{Occurrences: 1, LongestWaitSec: 3600, MostAtOnce: 40}, SeverityInfo},
		{BlockingLeader{Occurrences: 2, LongestWaitSec: 59, MostAtOnce: 9}, SeverityMedium},
		{BlockingLeader{Occurrences: 2, LongestWaitSec: 60}, SeverityHigh},
		{BlockingLeader{Occurrences: 3, MostAtOnce: 10}, SeverityHigh},
		{BlockingLeader{Occurrences: 10}, SeverityHigh},
	} {
		if got := blk01Severity(c.l); got != c.want {
			t.Errorf("severity %+v = %s", c.l, got)
		}
	}
	for kinds, want := range map[string]string{
		"": OwnerUs, "long-running": OwnerUs, "cross-database": OwnerUs, "sleeping": OwnerVendor, "implicit": OwnerVendor,
		"serializable": OwnerVendor, "repeatable-read": OwnerVendor, "long-running,serializable": OwnerVendor,
	} {
		l := BlockingLeader{Kinds: strings.Split(kinds, ",")}
		if got := blk01Owner(l); got != want {
			t.Errorf("owner of %q = %s", kinds, got)
		}
	}
	for _, c := range []struct {
		kinds   []string
		rcsi    bool
		do      string
		clauses string
	}{
		{[]string{BlockSleeping, BlockImplicit}, false, blk01DoImplicit, "implicit transactions"},
		{[]string{BlockRepeatableRead}, false, blk01DoRepeatable, "REPEATABLE READ"},
		{[]string{BlockLongRunning}, false, blk01DoLongRunning, "over 30 seconds"},
		{[]string{}, false, blk01DoIndex, "running its statement"},
		{[]string{BlockCrossDatabase}, true, blk01DoIndexRCSI, "working in database Other"},
	} {
		clause, do := blk01Kind(BlockingLeader{Kinds: c.kinds, Database: "Other"}, c.rcsi)
		if do != c.do || !strings.Contains(clause, c.clauses) {
			t.Errorf("kind %v: %q %q", c.kinds, clause, do)
		}
	}
	// The classification from what the report recorded.
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		p    BlockedProcess
		want string
	}{
		{BlockedProcess{Status: "sleeping", TranCount: 1, Database: "WarehouseLive"}, "sleeping"},
		{BlockedProcess{Status: "sleeping", TranCount: 0, Database: "WarehouseLive"}, ""},
		{BlockedProcess{Status: "sleeping", TranCount: 2, ClientOption1: 673319008 | 2, Database: "WarehouseLive"}, "sleeping,implicit"},
		{BlockedProcess{Status: "running", TranName: "implicit_transaction", Isolation: "repeatable read (3)", Database: "WarehouseLive"}, "implicit,repeatable-read"},
		{BlockedProcess{Status: "running", LastBatchStarted: at.Add(-45 * time.Second), Database: "WarehouseLive"}, "long-running"},
		{BlockedProcess{Status: "suspended", LastBatchStarted: at.Add(-29 * time.Second), Database: "Reporting"}, "cross-database"},
	} {
		if got := strings.Join(blkHistoryKinds(c.p, at, "warehouselive", false), ","); got != c.want {
			t.Errorf("kinds %+v = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestMissedNote(t *testing.T) {
	if MissedNote(0) != "system_health looks every five minutes, so blocks shorter than that are not in this history; the longest and most repeated ones are. "+
		"kakapo's event session (Installed helpers on the instance page) keeps every block over 5 seconds." {
		t.Errorf("no threshold %q", MissedNote(0))
	}
	if MissedNote(20) != "The blocked process threshold is 20 s, but nothing kakapo reads records those reports; kakapo's event session (Installed helpers on the instance page) would." {
		t.Errorf("threshold %q", MissedNote(20))
	}
	// Phase 5e: with kakapo's session files read.
	since := time.Date(2026, 11, 3, 23, 30, 0, 0, time.UTC)
	if got := KakapoMissedNote(5, since, since.Add(time.Hour), 2*time.Hour); got != "kakapo's session keeps every block over 5 s since 2026-11-04." {
		t.Errorf("kakapo covers the window %q", got)
	}
	if got := KakapoMissedNote(5, since, since.AddDate(0, 0, -3), 0); got != "kakapo's session keeps every block over 5 s since 2026-11-03; "+
		"the days before come from system_health's five-minute snapshots, which miss shorter blocks." {
		t.Errorf("kakapo since the middle of the window %q", got)
	}
	if got := KakapoMissedNote(0, since, since, 0); !strings.Contains(got, "threshold is 0 (off), so it records no block") {
		t.Errorf("kakapo without threshold %q", got)
	}
}

// BLK01's line in the Performance card's checks: skipped before a read, then what the last read found;
// the history never changes the card or its findings.
func TestPerformanceBLK01Check(t *testing.T) {
	h := bhBuild(t)
	c, fs, cs := BuildPerformanceCard(goodPerf(), perfDef(), nil, &h)
	if st := statusOf(cs, "BLK01"); st.Status != CheckFinding || st.Note != "" {
		t.Errorf("BLK01 with a repeated blocker %+v", st)
	}
	none(t, fs, "BLK01")
	if c.Status != StatusGreen {
		t.Errorf("the history never changes the card %+v", c)
	}
	once := BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Days: 7, OffsetMinutes: 120,
		Reports: bhReports()[6:7]})
	_, _, cs = BuildPerformanceCard(goodPerf(), perfDef(), nil, &once)
	if st := statusOf(cs, "BLK01"); st.Status != CheckPassed || st.Note != "No blocker came back in the last 7 days: 1 blocker seen once." {
		t.Errorf("BLK01 once %+v", st)
	}
	clean := BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", CapturedAt: bhNow, Days: 7})
	_, _, cs = BuildPerformanceCard(goodPerf(), perfDef(), nil, &clean)
	if st := statusOf(cs, "BLK01"); st.Status != CheckPassed ||
		st.Note != "No blocking in this database that system_health saw in the last 7 days (it looks every five minutes)." {
		t.Errorf("BLK01 clean %+v", st)
	}
	// Grey: skipped with the card's reason, whatever was read before.
	_, _, cs = BuildPerformanceCard(PerformanceFacts{Database: "WarehouseLive", Error: "login failed"}, perfDef(), nil, &h)
	if st := statusOf(cs, "BLK01"); st.Status != CheckSkipped {
		t.Errorf("BLK01 grey %+v", st)
	}

	// The card detail carries it as blockingHistory: null before a read, left out on the other cards.
	_, fs, cs = runPerf(goodPerf())
	b, _ := json.Marshal(CardDetail{Card: Card{ID: CardPerformance}, Findings: fs, Checks: cs})
	if !strings.Contains(string(b), `"blockingHistory":null`) || !strings.Contains(string(b), `"deadlocks":null`) {
		t.Errorf("performance detail %s", b)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardPerformance}, Findings: fs, Checks: cs, Blocking: &clean})
	if !strings.Contains(string(b), `"blockingHistory":{"capturedAt":"2026-11-04T10:00:00Z"`) {
		t.Errorf("performance detail with a history %s", b)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardStorage}, Findings: []Finding{}, Checks: []CheckStatus{}, Blocking: &clean})
	if strings.Contains(string(b), "blockingHistory") {
		t.Errorf("storage detail %s", b)
	}
}

// The live blocking sentences point to the week's history once it was read.
func TestBlockingHistoryHint(t *testing.T) {
	h := bhBuild(t)
	tree := BlockingTree{Explain: []string{"No session is blocking another right now."}}
	tree.AddHistoryHint(nil)
	if len(tree.Explain) != 1 {
		t.Errorf("no history, no hint %+v", tree.Explain)
	}
	tree.AddHistoryHint(&h)
	want := "For what blocked users before now, see the week's history on the card (read 2026-11-04 10:00 UTC)."
	if len(tree.Explain) != 2 || tree.Explain[1] != want {
		t.Errorf("tree %+v", tree.Explain)
	}
	p := PerfSample{Lines: []SampleLine{{Area: SampleAreaBlocking, Text: "Session 83 has blocked 2 sessions."}, {Area: SampleAreaWaits, Text: "Waits."}}}
	p.AddHistoryHint(&h)
	if p.Lines[0].Text != "Session 83 has blocked 2 sessions. "+want || p.Lines[1].Text != "Waits." {
		t.Errorf("sample %+v", p.Lines)
	}
	q := PerfSample{Lines: []SampleLine{{Area: SampleAreaWaits, Text: "Waits."}}}
	q.AddHistoryHint(&h)
	if q.Lines[0].Text != "Waits." {
		t.Errorf("a sample without blocking %+v", q.Lines)
	}
}

// How system_health writes a long block (seen live on SQL Server 2022): the head of the chain, holding an
// open transaction while it waits on a WAITFOR, as a blocked-process with trancount 1 and an empty
// blocking-process; the session behind it is often not in the snapshot at all.
func bhHead(waitMs int64) BlockedProcess {
	return BlockedProcess{SPID: 58, Login: "dev", Host: "WS01", Program: "SSMS", Database: "WarehouseLive", DatabaseID: 7,
		Status: "suspended", TranCount: 1, Isolation: "read committed (2)", WaitMs: waitMs, XactID: 7001,
		InputBuffer:      "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 5 WHERE Id = 1042; WAITFOR DELAY '00:40:00'; ROLLBACK;",
		LastBatchStarted: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC).Add(-time.Duration(waitMs) * time.Millisecond)}
}

func bhHeadHistory(reports ...BlockedProcessReport) BlockingHistory {
	return BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Days: 7,
		Source: DeadlockSourceFile, OffsetMinutes: 120, Reports: reports})
}

const bhHeadStatement = "BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?; WAITFOR DELAY ?; ROLLBACK;"

func TestBlockingHistoryHeadOnly(t *testing.T) {
	s := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	h := bhHeadHistory(BlockedProcessReport{EventUTC: s, MonitorLoop: 4100, Blocked: bhHead(2063753)})
	if h.ReportsTotal != 1 || h.Snapshots != 1 || len(h.Leaders) != 1 || len(h.Findings) != 1 {
		t.Fatalf("head only %+v", h)
	}
	l := h.Leaders[0]
	if l.BlockerStatement != bhHeadStatement || l.Object != "" || l.Key != bhHeadStatement+"|" || !l.HeldWhileWaiting ||
		l.Occurrences != 1 || l.Snapshots != 1 || l.LongestWaitSec != 2063 || l.TotalBlockedSessions != 0 || l.MostAtOnce != 0 ||
		len(l.Kinds) != 0 || l.SampleVictimStatement != "" || l.BlockerProgram != "SSMS" ||
		l.SampleBlockerStatement != "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 5 WHERE Id = 1042; WAITFOR DELAY '00:40:00'; ROLLBACK;" {
		t.Errorf("head leader %+v", l)
	}
	x := h.Findings[0]
	if x.Severity != SeverityHigh || x.Owner != OwnerUs || x.DoThis != blk01DoSleeping || x.Object != `"`+bhHeadStatement+`"` ||
		x.Finding != `"`+bhHeadStatement+`" held an open transaction for 34 minutes while waiting (no session was recorded behind it, `+
			`system_health keeps the head of the chain only), once in 7 days, the last on Wed 2026-11-04 10:00 (server time).` ||
		!strings.Contains(x.Evidence, "Blocked statement: none recorded (system_health keeps the head of the chain only). Object: not recorded.") {
		t.Errorf("head BLK01 %+v", x)
	}
	// The head is not a blocked session in the heat line.
	for _, hr := range h.ByHour {
		if hr.Hour == "2026-11-04T10:00" && (hr.Reports != 1 || hr.SessionsBlocked != 0) {
			t.Errorf("hour %+v", hr)
		}
	}

	// A sleeping head, its isolation level and implicit transactions are its kinds; a batch that waits is
	// not long-running.
	sleeping := bhHead(400000)
	sleeping.Status, sleeping.Isolation, sleeping.ClientOption1 = "sleeping", "serializable (4)", 2
	k := bhHeadHistory(BlockedProcessReport{EventUTC: s, Blocked: sleeping})
	if got := strings.Join(k.Leaders[0].Kinds, ","); got != "sleeping,implicit,serializable" {
		t.Errorf("head kinds %q", got)
	}
	if x := k.Findings[0]; x.Owner != OwnerVendor || x.DoThis != blk01DoImplicit ||
		!strings.Contains(x.Finding, "(no session was recorded behind it, system_health keeps the head of the chain only), once in 7 days; "+
			"the blocker ran with implicit transactions, so its transaction stayed open, the last on") {
		t.Errorf("sleeping head %+v", x)
	}
	// Held under a minute and seen once: Info.
	if short := bhHeadHistory(BlockedProcessReport{EventUTC: s, Blocked: bhHead(45000)}); short.Findings[0].Severity != SeverityInfo {
		t.Errorf("short head %+v", short.Findings[0])
	}
	// A head-only report of a session that itself waits for another one in the snapshot is not a head.
	waiting := bhHead(90000)
	h2 := bhHeadHistory(
		BlockedProcessReport{EventUTC: s, Blocked: waiting},
		BlockedProcessReport{EventUTC: s, Blocked: waiting, Blocking: bhSleeping(9001, "UPDATE dbo.Pick SET Qty = 1")})
	if len(h2.Leaders) != 1 || h2.Leaders[0].HeldWhileWaiting || h2.Leaders[0].BlockerStatement != "UPDATE dbo.Pick SET Qty = ?" {
		t.Errorf("head that waits %+v", h2.Leaders)
	}
}

// The same head with a victim report naming it in the snapshot: one leader, the victim the one session
// behind it; a later snapshot that keeps the head only joins the same leader and episode.
func TestBlockingHistoryHeadMerged(t *testing.T) {
	s := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	head := bhHead(2063753)
	asBlocker := head
	asBlocker.WaitMs, asBlocker.LastBatchStarted = 0, time.Time{}
	victim := bhVictim(66, 1500000, "KEY: 7:72057594043039744 (8194443284a0)", "dbo.Pick", "SELECT * FROM dbo.Pick WHERE Id = 1042")
	victim.LockMode = "S"
	h := bhHeadHistory(
		BlockedProcessReport{EventUTC: s, MonitorLoop: 4100, Blocked: head},
		BlockedProcessReport{EventUTC: s, MonitorLoop: 4100, Blocked: victim, Blocking: asBlocker})
	if h.ReportsTotal != 2 || len(h.Leaders) != 1 || len(h.Findings) != 1 {
		t.Fatalf("merged %+v", h)
	}
	l := h.Leaders[0]
	if l.BlockerStatement != bhHeadStatement || l.Object != "dbo.Pick" || !l.HeldWhileWaiting || l.Occurrences != 1 || l.Snapshots != 1 ||
		l.LongestWaitSec != 2063 || l.TotalBlockedSessions != 1 || l.MostAtOnce != 1 || l.LockMode != "S" ||
		l.SampleVictimStatement != "SELECT * FROM dbo.Pick WHERE Id = 1042" {
		t.Errorf("merged leader %+v", l)
	}
	x := h.Findings[0]
	if x.Severity != SeverityHigh || x.Object != `"`+bhHeadStatement+`" on dbo.Pick` ||
		x.Finding != `"`+bhHeadStatement+`" held an open transaction for 34 minutes while waiting (1 session was recorded behind it), `+
			`once in 7 days on dbo.Pick, the last on Wed 2026-11-04 10:00 (server time).` {
		t.Errorf("merged BLK01 %+v", x)
	}
	for _, hr := range h.ByHour {
		if hr.Hour == "2026-11-04T10:00" && (hr.Reports != 2 || hr.SessionsBlocked != 1) {
			t.Errorf("hour %+v", hr)
		}
	}

	// Five minutes later system_health keeps the head only: the same transaction, the same leader.
	later := bhHead(2363753)
	h = bhHeadHistory(
		BlockedProcessReport{EventUTC: s, Blocked: head},
		BlockedProcessReport{EventUTC: s, Blocked: victim, Blocking: asBlocker},
		BlockedProcessReport{EventUTC: s.Add(5 * time.Minute), Blocked: later})
	if len(h.Leaders) != 1 {
		t.Fatalf("across snapshots %+v", h.Leaders)
	}
	l = h.Leaders[0]
	if l.Object != "dbo.Pick" || l.Occurrences != 1 || l.Snapshots != 2 || l.TotalBlockedSessions != 1 || l.LongestWaitSec != 2363 ||
		l.SampleVictimStatement != "SELECT * FROM dbo.Pick WHERE Id = 1042" ||
		!strings.Contains(h.Findings[0].Finding, "for 39 minutes while waiting (1 session was recorded behind it), once in 7 days on dbo.Pick, the last on Wed 2026-11-04 10:05") {
		t.Errorf("across snapshots %+v %s", l, h.Findings[0].Finding)
	}
}

// Phase 5e: kakapo's session files first. A block of session 83 reported in three lock monitor passes
// (each pass's reports milliseconds apart) is one block with its real duration; system_health's report
// of the same time is left out, its report from before kakapo's files is kept; the days say where they
// came from; with the session installed BLK01's Do this does not ask for it.
func TestBlockingHistoryKakapoFirst(t *testing.T) {
	key := "KEY: 7:72057594043039744 (8194443284a0)"
	upd := "UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042"
	head := bhSleeping(9100, "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 5 WHERE Id = 1042")
	t0 := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	ms := time.Millisecond
	k := func(at time.Time, loop int64, v BlockedProcess) BlockedProcessReport {
		return BlockedProcessReport{EventUTC: at, MonitorLoop: loop, Blocked: v, Blocking: head, Source: BlockSourceKakapo}
	}
	reports := []BlockedProcessReport{
		k(t0, 500, bhVictim(91, 6000, key, "dbo.Pick", upd)),
		k(t0.Add(5*time.Second), 501, bhVictim(91, 11000, key, "dbo.Pick", upd)),
		k(t0.Add(5*time.Second+3*ms), 501, bhVictim(95, 5500, key, "dbo.Pick", "SELECT * FROM dbo.Pick WHERE Id = 1042")),
		k(t0.Add(10*time.Second), 502, bhVictim(91, 16000, key, "dbo.Pick", upd)),
		// system_health: the same block in its snapshot (after kakapo's files begin: left out), and an
		// older one before them (kept).
		{EventUTC: t0.Add(2 * time.Minute), MonitorLoop: 77, Blocked: bhVictim(91, 120000, key, "dbo.Pick", upd), Blocking: head},
		{EventUTC: time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC), MonitorLoop: 12, Blocked: bhVictim(70, 400000, key, "dbo.Pick", upd),
			Blocking: bhSleeping(8000, "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 1 WHERE Id = 1")},
	}
	since := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h := BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Days: 7, Source: KakapoSource("file", false),
		OffsetMinutes: 120, ThresholdSeconds: 5, Reports: reports, KakapoSince: &since, Helper: &XESession{Exists: true, Ours: true}})
	if h.Source != "kakapo+file" || h.KakapoSince == nil || !h.KakapoSince.Equal(since) || h.ReportsTotal != 5 || h.Snapshots != 4 ||
		h.MissedNote != "kakapo's session keeps every block over 5 s since 2026-11-02; the days before come from system_health's five-minute snapshots, which miss shorter blocks." {
		t.Fatalf("history %+v", h)
	}
	if len(h.DaySources) != 8 || h.DaySources[0] != (BlockingDaySource{"2026-10-28", DaySourceSystemHealth}) ||
		h.DaySources[5] != (BlockingDaySource{"2026-11-02", DaySourceBoth}) || h.DaySources[7] != (BlockingDaySource{"2026-11-04", DaySourceKakapo}) {
		t.Errorf("day sources %+v", h.DaySources)
	}
	// The kakapo block and the older system_health one share the statement: one leader, two episodes,
	// seen in one snapshot and three of kakapo's passes.
	if len(h.Leaders) != 1 {
		t.Fatalf("leaders %+v", h.Leaders)
	}
	if l := h.Leaders[0]; l.Occurrences != 2 || l.Passes != 3 || l.Snapshots != 1 || l.LongestWaitSec != 400 || l.TotalBlockedSessions != 3 || l.MostAtOnce != 2 {
		t.Errorf("leader %+v", l)
	}
	if x := h.Findings[0]; strings.Contains(x.DoThis, "Install kakapo's session") || x.FixID != "" ||
		!strings.Contains(x.Evidence, "Seen in 1 five-minute snapshot and 3 reports of kakapo's session") {
		t.Errorf("finding %+v", x)
	}

	// kakapo's files alone: "{n} blocks, the longest {s}".
	only := BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Days: 7, Source: KakapoSource("", true),
		ThresholdSeconds: 5, Reports: reports[:4], KakapoSince: &since})
	if x := only.Findings[0]; only.Source != "kakapo" ||
		x.Finding != `"BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" caused 1 block in 7 days on dbo.Pick, the longest 16 seconds, 2 sessions blocked in all; `+
			"the blocker held its transaction open while doing nothing (sleeping), the last on Tue 2026-11-03 09:00 (server time)." ||
		!strings.Contains(x.Evidence, "Seen in 3 reports of kakapo's session") {
		t.Errorf("kakapo only %q / %q", x.Finding, x.Evidence)
	}
	if st := blk01Check(CheckStatus{}, &BlockingHistory{Days: 7, Source: "kakapo", ThresholdSeconds: 5}); st.Note != "No block over 5 s in this database in the last 7 days (kakapo's session)." {
		t.Errorf("check note %q", st.Note)
	}

	// Without the session: BLK01's Do this ends with installing it and carries the fix.
	none := BuildBlockingHistory(BlockingHistoryInputs{Database: "WarehouseLive", DatabaseID: 7, CapturedAt: bhNow, Days: 7, Source: "file",
		Reports: bhReports(), Helper: &XESession{}})
	for _, x := range none.Findings {
		if !strings.HasSuffix(x.DoThis, " Install kakapo's session to keep every block over 5 seconds (Installed helpers on the instance page).") || x.FixID != FixInstallXESession {
			t.Errorf("no session: %q %q", x.DoThis, x.FixID)
		}
	}
	if none.KakapoSince != nil || none.DaySources != nil || none.MissedNote != MissedNote(0) {
		t.Errorf("no session history %+v", none)
	}
}
