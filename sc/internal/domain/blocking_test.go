package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var blkNow = time.Date(2026, 11, 3, 10, 14, 0, 0, time.UTC)

// blockingFixture: two chains in WMS. Session 83 (GraniteScheduler, sleeping with a transaction open 6
// minutes) blocks 91 and 95; 91 blocks 102 (two levels). Session 64, a SQL Agent job under SERIALIZABLE,
// has run one INSERT for 4 minutes and blocks 70. Session 120 was blocking when the list was read and
// is not any more (no victims). READ COMMITTED SNAPSHOT is off.
func blockingFixture() BlockingFacts {
	ago := func(sec int) time.Time { return blkNow.Add(-time.Duration(sec) * time.Second) }
	return BlockingFacts{
		Database: "WMS", Found: true, RCSI: false, OwnSession: 140, ServerName: `SQL01\WMS`, ProductVersion: "16.0.4135.4", ServerNow: blkNow,
		Sessions: []BlockingSession{
			{Session: 64, Login: "WMS\\svc-agent", Host: "SQL01", Program: "SQLAgent - TSQL JobStep (Job 0x9A3F11C04B6E2D49A1F07E53D1C2B8A4 : Step 1)",
				JobName: "Nightly stock import", Status: "running", IsUser: true, Command: "INSERT", Database: "WMS",
				Statement: "INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock", IsolationLevel: 4, OpenTran: 1,
				TranName: "user_transaction", TranStarted: ago(250), RequestStarted: ago(240), LogBytes: 52428800, LoginTime: ago(3600)},
			{Session: 70, BlockedBy: 64, Login: "granite", Host: "WMS-APP02", Program: "Granite Integration", Status: "suspended", IsUser: true,
				Command: "UPDATE", Database: "WMS", Statement: "UPDATE dbo.Stock SET Qty = Qty - 1 WHERE ItemID = @id", WaitType: "LCK_M_IX",
				WaitMs: 45000, WaitResource: "OBJECT: 7:1977058079:0 ", IsolationLevel: 2, RequestStarted: ago(45)},
			{Session: 83, Login: "granite", Host: "WMS-APP01", Program: "GraniteScheduler", Status: "sleeping", IsUser: true, Database: "WMS",
				Statement: "UPDATE dbo.MasterItem SET LastCounted = GETDATE() WHERE MasterItemID = @id", ObjectName: "dbo.Prescript_Packing_MasterItem",
				StatementIsLast: true, IsolationLevel: 2, OpenTran: 1, TranName: "user_transaction", TranStarted: ago(362), LogBytes: 125829120,
				LoginTime: ago(7200)},
			{Session: 91, BlockedBy: 83, Login: "scanner", Host: "HH12", Program: "Granite Scanner", Status: "suspended", IsUser: true,
				Command: "UPDATE", Database: "WMS", Statement: "UPDATE dbo.MasterItem SET Qty = @q WHERE MasterItemID = @id", WaitType: "LCK_M_U",
				WaitMs: 300000, WaitResource: "KEY: 7:72057594043236352 (8194443284a0)", IsolationLevel: 2, OpenTran: 1,
				TranStarted: ago(301), RequestStarted: ago(300), LogBytes: 8192},
			{Session: 95, BlockedBy: 83, Login: "report", Host: "BI01", Program: "Report Server", Status: "suspended", IsUser: true,
				Command: "SELECT", Database: "WMS", Statement: "SELECT * FROM dbo.MasterItem", WaitType: "LCK_M_S", WaitMs: 240000,
				WaitResource: "KEY: 7:72057594043236352 (8194443284a0)", IsolationLevel: 2, RequestStarted: ago(240)},
			{Session: 102, BlockedBy: 91, Login: "scanner", Host: "HH07", Program: "Granite Scanner", Status: "suspended", IsUser: true,
				Command: "SELECT", Database: "WMS", Statement: "SELECT Qty FROM dbo.MasterItem WHERE MasterItemID = @id", WaitType: "LCK_M_S",
				WaitMs: 120000, WaitResource: "KEY: 7:72057594043236352 (8194443284a0)", IsolationLevel: 2, RequestStarted: ago(120)},
			{Session: 120, Login: "granite", Host: "WMS-APP01", Program: "GraniteScheduler", Status: "sleeping", IsUser: true, Database: "WMS"},
		},
		Waits: []LockWait{
			{Session: 70, ResourceType: "OBJECT", Database: "WMS", Object: "dbo.Stock", WaitMode: "IX", HeldMode: "X"},
			{Session: 91, ResourceType: "KEY", Database: "WMS", Object: "dbo.MasterItem", Index: "PK_MasterItem", WaitMode: "U", HeldMode: "X"},
			{Session: 95, ResourceType: "KEY", Database: "WMS", Object: "dbo.MasterItem", Index: "PK_MasterItem", WaitMode: "S", HeldMode: "X"},
			{Session: 102, ResourceType: "KEY", Database: "WMS", Object: "dbo.MasterItem", Index: "PK_MasterItem", WaitMode: "S", HeldMode: "U"},
		},
	}
}

func TestBlockingTree(t *testing.T) {
	tr := BuildBlockingTree(blockingFixture(), blkNow)
	if len(tr.Heads) != 2 || tr.Blocked != 4 || !tr.OfferRCSI || len(tr.Notes) != 0 {
		t.Fatalf("tree: %d heads, %d blocked, offer %v, notes %q", len(tr.Heads), tr.Blocked, tr.OfferRCSI, tr.Notes)
	}
	h := tr.Heads[0]
	if h.Session != 83 || h.Behind != 3 || h.LongestWaitSec != 300 || !h.Sleeping || h.TranSeconds != 362 || !h.CanKill ||
		strings.Join(h.Kinds, ",") != "sleeping" || len(h.Victims) != 2 {
		t.Errorf("head 83: %+v", h)
	}
	v := h.Victims[0]
	if v.Session != 91 || v.Resource != "dbo.MasterItem (PK_MasterItem)" || v.LockMode != "wants U, held X" || v.Behind != 1 ||
		len(v.Victims) != 1 || v.Victims[0].Session != 102 || h.Victims[1].Session != 95 {
		t.Errorf("second level: %+v", v)
	}
	j := tr.Heads[1]
	if j.Session != 64 || j.Behind != 1 || j.RequestSeconds != 240 || j.Isolation != "SERIALIZABLE" ||
		strings.Join(j.Kinds, ",") != "serializable,long-running" {
		t.Errorf("head 64: %+v", j)
	}
	want := []string{
		"Right now 4 sessions wait in 2 blocking chains.",
		"Session 83 (GraniteScheduler on WMS-APP01) has held a lock on dbo.MasterItem for 6 minutes in a transaction that is still open and is doing nothing (sleeping); 3 sessions wait behind it, the longest for 5 minutes.",
		"A sleeping session with an open transaction is usually an application that did not commit, or an SSMS window left open. Ask granite on WMS-APP01 to commit or close it; Kill session 83 ends it and undoes its work (the rollback can take up to 6 minutes).",
		"Session 64 (SQL Agent job Nightly stock import on SQL01) has held a lock on dbo.Stock for 4 minutes while one statement has run for 4 minutes: \"INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock\"; 1 session waits behind it.",
		"Session 64 uses the SERIALIZABLE isolation level, which locks ranges of rows and blocks far more than the default (.NET's TransactionScope picks it unless told otherwise). Copy the chain for the vendor.",
		"Its statement has run for 4 minutes. If it should be quicker, Copy it for the vendor; Kill session 64 undoes everything it did so far, which can take as long again.",
		"2 sessions are reading and wait for a writer in WMS. READ COMMITTED SNAPSHOT lets readers see the last committed rows instead of waiting: Turn on READ COMMITTED SNAPSHOT below.",
	}
	if strings.Join(tr.Explain, "\n") != strings.Join(want, "\n") {
		t.Errorf("explain:\n%s\nwant:\n%s", strings.Join(tr.Explain, "\n"), strings.Join(want, "\n"))
	}
	for _, s := range []string{"Blocking chain on SQL01\\WMS (database WMS)", "\nSession 83: granite, GraniteScheduler on WMS-APP01; sleeping, database WMS; transaction open 6 minutes, 120.0 MB of log; READ COMMITTED; last ran dbo.Prescript_Packing_MasterItem: UPDATE dbo.MasterItem",
		"\n  Session 91: scanner, Granite Scanner on HH12; suspended", "\n    Session 102: scanner", "waits 2 minutes on LCK_M_S for dbo.MasterItem (PK_MasterItem)"} {
		if !strings.Contains(tr.Handover, s) {
			t.Errorf("handover lacks %q:\n%s", s, tr.Handover)
		}
	}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"heads":[{"session":83,`, `"victims":[{"session":91,`, `"kinds":["sleeping"]`, `"offerRcsi":true`, `"canKill":true`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("JSON lacks %s", s)
		}
	}
	if strings.Contains(string(b), "LoginTime") || strings.Contains(string(b), "loginTime") {
		t.Error("the server-clock times stay out of the JSON")
	}
}

// The other classifications: implicit transaction, compile lock, repeatable read, a rolling-back kill,
// a system session and kakapo's own; and nothing blocking; and the chain not read.
func TestBlockingClassification(t *testing.T) {
	f := BlockingFacts{Database: "WMS", Found: true, RCSI: true, OwnSession: 140, ServerNow: blkNow, Sessions: []BlockingSession{
		{Session: 55, Login: "app", Host: "H1", Program: "App", Status: "sleeping", IsUser: true, OpenTran: 1, TranName: "implicit_transaction",
			TranStarted: blkNow.Add(-90 * time.Second), IsolationLevel: 3},
		{Session: 56, BlockedBy: 55, Status: "suspended", IsUser: true, WaitType: "LCK_M_X", WaitMs: 9000, WaitResource: "OBJECT: 7:123:0 [COMPILE]"},
		{Session: 12, Status: "background", IsUser: false, Command: "CHECKPOINT"},
		{Session: 57, BlockedBy: 12, Status: "suspended", IsUser: true, WaitMs: 6000},
		{Session: 140, Program: BlockingAppName, Status: "running", IsUser: true},
		{Session: 58, BlockedBy: 140, Status: "suspended", IsUser: true, WaitMs: 2000},
		{Session: 77, Program: "App", Status: "suspended", IsUser: true, Command: "KILLED/ROLLBACK", PercentComplete: 40},
		{Session: 78, BlockedBy: 77, Status: "suspended", IsUser: true, WaitMs: 3000},
	}}
	tr := BuildBlockingTree(f, blkNow)
	if len(tr.Heads) != 4 || tr.OfferRCSI {
		t.Fatalf("heads %d, offer %v", len(tr.Heads), tr.OfferRCSI)
	}
	byID := map[int]BlockingNode{}
	for _, h := range tr.Heads {
		byID[h.Session] = h
	}
	if k := strings.Join(byID[55].Kinds, ","); k != "sleeping,implicit,repeatable-read,compile" {
		t.Errorf("55 kinds %s", k)
	}
	if byID[12].CanKill || !strings.Contains(byID[12].KillRefusal, "system session") ||
		byID[140].CanKill || !strings.Contains(byID[140].KillRefusal, "kakapo's own connection") {
		t.Errorf("refusals: %q, %q", byID[12].KillRefusal, byID[140].KillRefusal)
	}
	if strings.Join(byID[77].Kinds, ",") != "rolling-back" {
		t.Errorf("77 kinds %q", byID[77].Kinds)
	}
	all := strings.Join(tr.Explain, "\n")
	for _, s := range []string{
		"Session 55 (App on H1) has held locks for 2 minutes in an implicit transaction (opened by the driver, not by the code) that is still open and is doing nothing (sleeping); 1 session waits behind it.",
		"Session 55 runs with implicit transactions",
		"Session 55 uses the REPEATABLE READ isolation level",
		"The sessions behind session 55 wait on a compile lock",
		"Session 77 (App) is being killed and is rolling its work back (40% done); 1 session waits until it finishes.",
		"Session 12 is a SQL Server system session: SQL Server needs it, so kakapo does not kill it.",
	} {
		if !strings.Contains(all, s) {
			t.Errorf("explain lacks %q:\n%s", s, all)
		}
	}
	if strings.Contains(all, "READ COMMITTED SNAPSHOT") {
		t.Error("RCSI is on: no sentence offers it")
	}

	none := BuildBlockingTree(BlockingFacts{Database: "WMS", Found: true}, blkNow)
	if len(none.Heads) != 0 || none.Explain[0] != "No session is blocking another right now. Blocking often lasts only seconds: Watch for 30 seconds to catch it." ||
		!none.OfferRCSI {
		t.Errorf("nothing blocking: %+v", none)
	}
	failed := BuildBlockingTree(BlockingFacts{Database: "WMS", SessionsError: "the login does not have VIEW SERVER STATE."}, blkNow)
	if failed.Explain[0] != "kakapo could not read the blocking chain: the login does not have VIEW SERVER STATE." || failed.OfferRCSI {
		t.Errorf("not read: %+v", failed)
	}
	notes := BuildBlockingTree(BlockingFacts{Database: "WMS", Found: true, LocksError: "denied", JobsError: "msdb"}, blkNow)
	if len(notes.Notes) != 2 {
		t.Errorf("notes %q", notes.Notes)
	}
}

// Sessions blocking each other in a circle have no head: the circle is cut, never walked forever.
func TestBlockingCycle(t *testing.T) {
	tr := BuildBlockingTree(BlockingFacts{ServerNow: blkNow, Sessions: []BlockingSession{
		{Session: 60, BlockedBy: 61, IsUser: true, WaitMs: 1000}, {Session: 61, BlockedBy: 60, IsUser: true, WaitMs: 2000},
	}}, blkNow)
	if len(tr.Heads) != 1 || tr.Heads[0].Session != 60 || tr.Heads[0].Behind != 1 {
		t.Errorf("cycle: %+v", tr.Heads)
	}
}

func TestBlockDuration(t *testing.T) {
	for sec, want := range map[int64]string{1: "1 second", 45: "45 seconds", 60: "1 minute", 362: "6 minutes", 5399: "90 minutes", 5400: "1.5 hours", 7200: "2 hours"} {
		if got := BlockDuration(sec); got != want {
			t.Errorf("%d s: %q, want %q", sec, got, want)
		}
	}
	if got := BlockSnippet("SELECT  *\n\tFROM dbo.T", 80); got != "SELECT * FROM dbo.T" {
		t.Errorf("snippet %q", got)
	}
	if got := BlockSnippet(strings.Repeat("x", 100), 10); got != "xxxxxxxxx…" {
		t.Errorf("cut %q", got)
	}
}
