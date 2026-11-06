package domain

import (
	"strings"
	"testing"
	"time"
)

// Fixtures of the wait classes (docs/phase5m-contract.md, section 2): a database Shop (id 5) on a server
// two hours ahead of UTC.
var clsBatch = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

func clsWaiter(spid int, mode, iso string, waitMs int64, program string) BlockedProcess {
	return BlockedProcess{SPID: spid, Login: "user" + program, Host: "HOST-" + program, Program: program, Database: "Shop", DatabaseID: 5,
		Status: "suspended", Isolation: iso, WaitMs: waitMs, LockMode: mode, LastBatchStarted: clsBatch,
		WaitResource: "KEY: 5:72057594043039744 (8194443284a0)", Object: "dbo.Orders", InputBuffer: "SELECT 1"}
}

func clsBlocker(spid int, iso, buf string, xact int64, program string) BlockedProcess {
	return BlockedProcess{SPID: spid, Login: "user" + program, Host: "HOST-" + program, Program: program, Database: "Shop", DatabaseID: 5,
		Status: "sleeping", Isolation: iso, TranCount: 1, XactID: xact, InputBuffer: buf, LastBatchStarted: clsBatch}
}

const rc = "read committed (2)"

func TestClassifyWait(t *testing.T) {
	upd := "BEGIN TRAN; UPDATE dbo.Orders SET Qty = 1 WHERE Id = 7"
	sel := "SELECT * FROM dbo.Orders WITH (UPDLOCK) WHERE Id = 7"
	for _, c := range []struct {
		name string
		r    BlockedProcessReport
		want string
	}{
		{"S at read committed", BlockedProcessReport{Blocked: clsWaiter(60, "S", rc, 9000, "Reports"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitReadsBehindWrites},
		{"IS at read committed", BlockedProcessReport{Blocked: clsWaiter(60, "IS", rc, 9000, "Reports"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitReadsBehindWrites},
		{"S at repeatable read", BlockedProcessReport{Blocked: clsWaiter(60, "S", "repeatable read (3)", 9000, "Reports"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitLockingReaders},
		{"RangeS-S", BlockedProcessReport{Blocked: clsWaiter(60, "RangeS-S", rc, 9000, "Reports"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitLockingReaders},
		{"Sch-S", BlockedProcessReport{Blocked: clsWaiter(60, "Sch-S", rc, 9000, "Reports"), Blocking: clsBlocker(55, rc, "ALTER TABLE dbo.Orders ADD x int", 1, "Deploy")}, WaitSchemaLocks},
		{"Sch-M at serializable", BlockedProcessReport{Blocked: clsWaiter(60, "Sch-M", "serializable (4)", 9000, "Deploy"), Blocking: clsBlocker(55, rc, sel, 1, "App")}, WaitSchemaLocks},
		{"U behind a SELECT at read committed", BlockedProcessReport{Blocked: clsWaiter(60, "U", rc, 9000, "App"), Blocking: clsBlocker(55, rc, sel, 1, "Reports")}, WaitWritesBehindReads},
		{"X behind a parameterised SELECT", BlockedProcessReport{Blocked: clsWaiter(60, "X", rc, 9000, "App"),
			Blocking: clsBlocker(55, rc, "(@P1 int)SELECT TOP (10) * FROM dbo.Orders WHERE Id > @P1", 1, "Reports")}, WaitWritesBehindReads},
		{"U behind a SELECT at serializable", BlockedProcessReport{Blocked: clsWaiter(60, "U", rc, 9000, "App"), Blocking: clsBlocker(55, "serializable (4)", sel, 1, "Reports")}, WaitLockingReaders},
		{"X behind an UPDATE", BlockedProcessReport{Blocked: clsWaiter(60, "X", rc, 9000, "App"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitWritesBehindWrites},
		{"IX behind a snapshot reader", BlockedProcessReport{Blocked: clsWaiter(60, "IX", rc, 9000, "App"), Blocking: clsBlocker(55, "snapshot (5)", sel, 1, "Reports")}, WaitWritesBehindWrites},
		{"a head-only report", BlockedProcessReport{Blocked: clsBlocker(55, rc, upd, 1, "App")}, WaitUnknown},
		{"a procedure call as blocker", BlockedProcessReport{Blocked: clsWaiter(60, "U", rc, 9000, "App"),
			Blocking: clsBlocker(55, rc, "Proc [Database Id = 5 Object Id = 1234]", 1, "App")}, WaitUnknown},
		{"no lock mode", BlockedProcessReport{Blocked: clsWaiter(60, "", rc, 9000, "App"), Blocking: clsBlocker(55, rc, upd, 1, "App")}, WaitUnknown},
	} {
		if got := ClassifyWait(c.r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if !RemovedByRCSI(WaitWritesBehindReads) || !RemovedByRCSI(WaitReadsBehindWrites) || RemovedByRCSI(WaitWritesBehindWrites) || RemovedByRCSI(WaitLockingReaders) {
		t.Error("RCSI removes the two reader/writer classes only")
	}
}

func TestLeadingKeyword(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1":                                  "SELECT",
		"  \r\n select * from t":                    "SELECT",
		"-- a comment\nSELECT 1":                    "SELECT",
		"/* a /* nested */ comment */ SELECT 1":     "SELECT",
		";SELECT 1":                                 "SELECT",
		"(@P1 int,@P2 nvarchar(10))SELECT a FROM t": "SELECT",
		"(@P1 int)UPDATE t SET a = @P1":             "UPDATE",
		"WITH c AS (SELECT 1 AS a) SELECT * FROM c": "SELECT",
		"WITH c (a) AS (SELECT ')') , d AS (SELECT 2) UPDATE t SET a = 1": "UPDATE",
		"BEGIN TRAN; SELECT 1": "BEGIN",
		"EXEC dbo.Proc":        "EXEC",
		"":                     "",
		"WITH":                 "",
	} {
		if got := LeadingKeyword(in); got != want {
			t.Errorf("LeadingKeyword(%q) = %q, want %q", in, got, want)
		}
	}
}

// clsHistory builds a history of Shop from reports, as kakapo's session or system_health wrote them.
func clsHistory(source string, reports ...BlockedProcessReport) BlockingHistory {
	in := BlockingHistoryInputs{Database: "Shop", DatabaseID: 5, CapturedAt: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC), Days: 7,
		Source: source, OffsetMinutes: 120, ThresholdSeconds: 5, Reports: reports}
	if source == BlockSourceKakapo {
		since := in.CapturedAt.AddDate(0, 0, -8)
		in.KakapoSince = &since
		for i := range in.Reports {
			in.Reports[i].Source = BlockSourceKakapo
		}
	}
	return BuildBlockingHistory(in)
}

// kakapo's session reports a wait at every threshold interval while it lasts: one episode, as long as
// its longest report; a system_health snapshot sees a wait once every five minutes, and the same waiting
// batch behind the same transaction in two snapshots is one episode too.
func TestBlockingClassesEpisodes(t *testing.T) {
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC) // 10:00 on the server
	upd := "BEGIN TRAN; UPDATE dbo.Orders SET Qty = 1 WHERE Id = 7"
	reports := []BlockedProcessReport{}
	for i, ms := range []int64{5100, 10200, 15300} { // one read behind one write, reported three times
		reports = append(reports, BlockedProcessReport{EventUTC: at.Add(time.Duration(5*i) * time.Second), MonitorLoop: int64(10 + i),
			Blocked: clsWaiter(60, "S", rc, ms, "Reports"), Blocking: clsBlocker(55, rc, upd, 900, "App")})
	}
	// A second read, another batch of the same session, behind another transaction, an hour later.
	w2 := clsWaiter(60, "S", rc, 7000, "Reports")
	w2.LastBatchStarted = clsBatch.Add(time.Hour)
	reports = append(reports, BlockedProcessReport{EventUTC: at.Add(time.Hour), MonitorLoop: 50, Blocked: w2, Blocking: clsBlocker(55, rc, upd, 901, "App")})
	h := clsHistory(BlockSourceKakapo, reports...)
	if len(h.Classes) != len(WaitClassOrder) {
		t.Fatalf("classes %+v", h.Classes)
	}
	r, _ := h.Class(WaitReadsBehindWrites)
	if r.Episodes != 2 || r.TotalWaitSec != 22 || r.LongestWaitSec != 15 || r.MostAtOnce != 1 || r.Days != 1 ||
		len(r.ByDay) != 1 || r.ByDay[0] != (BlockingClassDay{Day: "2026-11-05", WaitSec: 22}) {
		t.Errorf("reads behind writes %+v", r)
	}
	if len(r.Waiters) != 1 || r.Waiters[0] != (BlockingParty{Program: "Reports", Login: "userReports", Host: "HOST-Reports", Episodes: 2, WaitSec: 22}) ||
		len(r.Blockers) != 1 || r.Blockers[0].Program != "App" {
		t.Errorf("programs %+v %+v", r.Waiters, r.Blockers)
	}
	if w, _ := h.Class(WaitWritesBehindWrites); w.Episodes != 0 || w.Waiters != nil {
		t.Errorf("writes behind writes %+v", w)
	}
	if len(h.Leaders) != 1 || h.Leaders[0].Classes[WaitReadsBehindWrites] != 2 {
		t.Errorf("leader classes %+v", h.Leaders)
	}
	if h.ClassLine != "Waits RCSI would remove: 22 s (0 writes behind reads, 2 reads behind writes). Waits it would not: 0 s." {
		t.Errorf("class line %q", h.ClassLine)
	}

	// system_health: the same wait in two five-minute snapshots, its length "at least" the longest seen.
	sh := clsHistory(DeadlockSourceFile,
		BlockedProcessReport{EventUTC: at, MonitorLoop: 1, Blocked: clsWaiter(61, "U", rc, 40000, "App"), Blocking: clsBlocker(55, rc, "SELECT * FROM dbo.Orders", 902, "Reports")},
		BlockedProcessReport{EventUTC: at.Add(5 * time.Minute), MonitorLoop: 61, Blocked: clsWaiter(61, "U", rc, 340000, "App"), Blocking: clsBlocker(55, rc, "SELECT * FROM dbo.Orders", 902, "Reports")})
	if w, _ := sh.Class(WaitWritesBehindReads); w.Episodes != 1 || w.TotalWaitSec != 340 || w.LongestWaitSec != 340 {
		t.Errorf("system_health episode %+v", w)
	}
	// A history read before phase 5m has no classes and no line.
	if _, ok := (&BlockingHistory{}).Class(WaitReadsBehindWrites); ok || BlockingClassLine(&BlockingHistory{}) != "" {
		t.Error("no classes before 5m")
	}
}

// A wait that crosses midnight on the server's clock (UTC+2) is split between the two days; the programs
// are ranked by the time they waited, then by their episodes.
func TestBlockingClassesByDayAndPrograms(t *testing.T) {
	end := time.Date(2026, 11, 4, 22, 0, 20, 0, time.UTC) // 00:00:20 on 2026-11-05, server time
	upd := "UPDATE dbo.Orders SET Qty = 1"
	reports := []BlockedProcessReport{
		{EventUTC: end, MonitorLoop: 1, Blocked: clsWaiter(60, "S", rc, 60000, "Reports"), Blocking: clsBlocker(55, rc, upd, 1, "App")},
	}
	for i, p := range []struct {
		spid int
		ms   int64
		prog string
	}{{61, 5000, "Excel"}, {62, 9000, "BI"}, {63, 9000, "BI"}, {64, 2000, "Odbc"}} {
		w := clsWaiter(p.spid, "S", rc, p.ms, p.prog)
		reports = append(reports, BlockedProcessReport{EventUTC: end.Add(time.Duration(i+1) * time.Hour), MonitorLoop: int64(100 + i), Blocked: w,
			Blocking: clsBlocker(55, rc, upd, int64(2+i), "App")})
	}
	h := clsHistory(BlockSourceKakapo, reports...)
	r, _ := h.Class(WaitReadsBehindWrites)
	want := []BlockingClassDay{{Day: "2026-11-04", WaitSec: 40}, {Day: "2026-11-05", WaitSec: 20 + 5 + 9 + 9 + 2}}
	if r.Days != 2 || len(r.ByDay) != 2 || r.ByDay[0] != want[0] || r.ByDay[1] != want[1] {
		t.Errorf("by day %+v", r.ByDay)
	}
	names := []string{}
	for _, p := range r.Waiters {
		names = append(names, p.Program)
	}
	if strings.Join(names, ",") != "Reports,BI,Excel" || r.Waiters[1].Episodes != 2 || r.Waiters[1].WaitSec != 18 {
		t.Errorf("waiters %+v", r.Waiters)
	}
	if r.MostAtOnce != 1 {
		t.Errorf("at once %d", r.MostAtOnce)
	}
}

func TestWaitText(t *testing.T) {
	for sec, want := range map[int64]string{0: "0 s", 59: "59 s", 60: "1 min", 125: "2 min 5 s", 4200: "1 h 10 min", 90000: "25 h 0 min"} {
		if got := WaitText(sec); got != want {
			t.Errorf("WaitText(%d) = %q, want %q", sec, got, want)
		}
	}
}

// A deadlock is with a reader when a side wanted S or IS at read committed; the pairs and the report
// count them.
func TestDeadlocksWithAReader(t *testing.T) {
	now := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	reader := func(at time.Time) DeadlockEvent {
		e := deadlockEvent(at, "Shop", "dbo.A", "dbo.B", "Orders")
		e.Victim.LockMode, e.Victim.Isolation = "S", rc
		e.Winner.LockMode, e.Winner.Isolation = "X", rc
		return e
	}
	writer := func(at time.Time) DeadlockEvent {
		e := deadlockEvent(at, "Shop", "dbo.A", "dbo.B", "Orders")
		e.Victim.LockMode, e.Victim.Isolation = "U", rc
		e.Winner.LockMode, e.Winner.Isolation = "S", "serializable (4)"
		return e
	}
	r := BuildDeadlockReport(DeadlockInputs{Database: "Shop", CapturedAt: now, Days: 7, Source: DeadlockSourceFile,
		Events: []DeadlockEvent{reader(now.Add(-time.Hour)), reader(now.Add(-2 * time.Hour)), writer(now.Add(-3 * time.Hour))}})
	if len(r.Pairs) != 1 || r.Pairs[0].WithReader != 2 || r.Pairs[0].Count != 3 || r.WithReader == nil || *r.WithReader != 2 || *r.WithoutReader != 1 {
		t.Errorf("report %+v", r)
	}
	if (DeadlockProcess{LockMode: "IS", Isolation: rc}).Reader() != true || (DeadlockProcess{LockMode: "S", Isolation: "repeatable read (3)"}).Reader() {
		t.Error("a reader is S or IS at read committed")
	}
}
