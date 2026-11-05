package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// t0 is 2026-11-05 05:00 UTC (07:00 on a server at UTC+2).
var t0 = time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)

// ddl is one committed DDL event by DOMAIN\jane from SSMS in WarehouseLive.
func ddl(min int, class, objType int, schema, name string) SchemaChangeFact {
	return SchemaChangeFact{At: t0.Add(time.Duration(min) * time.Minute), DatabaseID: 7, Database: "WarehouseLive",
		EventClass: class, SubClass: 1, ObjectType: objType, Schema: schema, Name: name, TransactionID: int64(1000 + min),
		Login: `DOMAIN\jane`, Host: "APP-SRV01", Program: "Microsoft SQL Server Management Studio"}
}

func TestSeenChangesEachTypeAndEvent(t *testing.T) {
	idx := ddl(1, TraceObjectCreated, ObjTypeIndex, "dbo", "IX_Orders_Customer")
	idx.Table = "Orders"
	goneIdx := ddl(2, TraceObjectDeleted, ObjTypeIndex, "", "IX_Old")
	db := ddl(3, TraceObjectAltered, ObjTypeDatabase, "", "")
	facts := []SchemaChangeFact{idx, goneIdx, db}
	got := BuildSeenChanges(9, facts, t0)
	want := []struct{ object, typ, desc string }{
		{"dbo.Orders.IX_Orders_Customer", "INDEX", `Index dbo.Orders.IX_Orders_Customer created by DOMAIN\jane on APP-SRV01 (Microsoft SQL Server Management Studio)`},
		{"IX_Old", "INDEX", `Index IX_Old dropped by DOMAIN\jane on APP-SRV01 (Microsoft SQL Server Management Studio)`},
		{"WarehouseLive", "SETTING", `Database WarehouseLive altered by DOMAIN\jane on APP-SRV01 (Microsoft SQL Server Management Studio)`},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %+v", got)
	}
	for i, w := range want {
		c := got[i]
		if c.Object != w.object || c.ChangeType != w.typ || c.Description != w.desc || c.Result != ChangeSeenOnServer || c.Statement != "" ||
			c.ServerID != 9 || c.Database != "WarehouseLive" || !c.ChangedAt.Equal(facts[i].At) {
			t.Errorf("row %d: %+v", i, c)
		}
	}

	// Every mapped type in its own hour (one row each), and its words.
	types := map[int]string{8272: "PROCEDURE", 17232: "PROCEDURE", 8278: "VIEW", 20038: "FUNCTION", 17993: "FUNCTION", 18004: "FUNCTION",
		21321: "FUNCTION", 21318: "FUNCTION", 21574: "FUNCTION", 17985: "FUNCTION", 8277: "TABLE", 21076: "TRIGGER", 16724: "TRIGGER",
		21572: "TRIGGER"}
	words := map[string]string{"PROCEDURE": "Procedure", "VIEW": "View", "FUNCTION": "Function", "TABLE": "Table", "TRIGGER": "Trigger"}
	for code, typ := range types {
		for _, ev := range []struct {
			class int
			word  string
		}{{TraceObjectCreated, "created"}, {TraceObjectAltered, "altered"}, {TraceObjectDeleted, "dropped"}} {
			rows := BuildSeenChanges(1, []SchemaChangeFact{ddl(0, ev.class, code, "dbo", "X")}, t0)
			if len(rows) != 1 || rows[0].ChangeType != typ || rows[0].Object != "dbo.X" ||
				!strings.HasPrefix(rows[0].Description, words[typ]+" dbo.X "+ev.word+" by ") {
				t.Errorf("type %d event %d: %+v", code, ev.class, rows)
			}
		}
	}
	if codes := SchemaObjectTypeCodes(); len(codes) != 16 || codes[0] != 8272 || codes[len(codes)-1] != 22601 {
		t.Errorf("codes %v", codes)
	}
}

func TestSeenChangesLeftOut(t *testing.T) {
	keep := ddl(0, TraceObjectAltered, 8272, "dbo", "GetOrders")
	begin, rollback := keep, keep
	begin.SubClass, rollback.SubClass = 0, 2
	tempdb, msdb := keep, keep
	tempdb.DatabaseID, msdb.DatabaseID = 2, 4
	stats := ddl(1, TraceObjectCreated, ObjTypeStatistics, "dbo", "_WA_Sys_00000002")
	rebuild := ddl(2, TraceObjectAltered, ObjTypeIndex, "dbo", "PK_Customers")
	mine := keep
	mine.Program = "SQLMonitor"
	dbCreated := ddl(3, TraceObjectCreated, ObjTypeDatabase, "", "")
	dbDropped := ddl(3, TraceObjectDeleted, ObjTypeDatabase, "", "")
	schema := ddl(4, TraceObjectCreated, 17235, "", "sales")
	// CREATE TABLE with a primary key: the key's index is created in the table's transaction.
	table := ddl(5, TraceObjectCreated, 8277, "dbo", "Customers")
	pk := ddl(5, TraceObjectCreated, ObjTypeIndex, "dbo", "PK_Customers")
	pk.TransactionID, pk.Table = table.TransactionID, "Customers"
	later := ddl(6, TraceObjectCreated, ObjTypeIndex, "dbo", "IX_Customers_Name")
	later.Table = "Customers"
	got := BuildSeenChanges(1, []SchemaChangeFact{begin, rollback, tempdb, msdb, stats, rebuild, mine, dbCreated, dbDropped, schema, table, pk, later}, t0)
	if len(got) != 2 || got[0].Object != "dbo.Customers" || got[0].ChangeType != "TABLE" || got[1].Object != "dbo.Customers.IX_Customers_Name" {
		t.Errorf("only the table and the later index: %+v", got)
	}
	// Before the window's start: not recomputed.
	if got := BuildSeenChanges(1, []SchemaChangeFact{keep}, t0.Add(time.Minute)); len(got) != 0 {
		t.Errorf("before since: %+v", got)
	}
}

func TestSeenChangesGroups(t *testing.T) {
	five := []SchemaChangeFact{}
	for i := 0; i < 5; i++ {
		five = append(five, ddl(i, TraceObjectCreated, 8272, "dbo", "P"+string(rune('1'+i))))
	}
	if got := BuildSeenChanges(1, five, t0); len(got) != 5 || got[4].Object != "dbo.P5" || got[4].ChangeType != "PROCEDURE" {
		t.Errorf("five: %+v", got)
	}

	// Six in one person-hour: one DEPLOY row at the last event, the names one per line.
	six := append([]SchemaChangeFact{}, five...)
	tbl := ddl(40, TraceObjectAltered, 8277, "dbo", "Orders")
	tbl.Host = "BUILD01"
	six = append(six, tbl)
	got := BuildSeenChanges(1, six, t0)
	if len(got) != 1 {
		t.Fatalf("six: %+v", got)
	}
	d := got[0]
	want := `Deploy by DOMAIN\jane on APP-SRV01, BUILD01 (Microsoft SQL Server Management Studio): 6 objects (5 procedures, 1 table)`
	if d.ChangeType != ChangeTypeDeploy || d.Object != "6 objects" || d.Description != want || d.Result != ChangeSeenOnServer ||
		!d.ChangedAt.Equal(tbl.At) || d.Database != "WarehouseLive" {
		t.Errorf("deploy %+v", d)
	}
	if lines := strings.Split(d.Statement, "\n"); len(lines) != 6 || lines[0] != "Procedure dbo.P1 created" || lines[5] != "Table dbo.Orders altered" {
		t.Errorf("names %q", d.Statement)
	}
	if e := d.Entry(); e.Copy != want+"\n"+d.Statement {
		t.Errorf("copy %q", e.Copy)
	}

	// Two logins in one hour stay apart; the same login in the next hour (UTC) is its own group.
	mixed := append([]SchemaChangeFact{}, five...)
	bob := ddl(10, TraceObjectAltered, 8278, "dbo", "V1")
	bob.Login = `DOMAIN\bob`
	next := ddl(60, TraceObjectAltered, 8278, "dbo", "V2")
	mixed = append(mixed, bob, next)
	if got := BuildSeenChanges(1, mixed, t0); len(got) != 7 {
		t.Errorf("two logins and two hours: %+v", got)
	}
	// Another program, or another database, is another group too.
	other := ddl(11, TraceObjectAltered, 8278, "dbo", "V3")
	other.Program = "DacFx Deploy"
	if got := BuildSeenChanges(1, append(append([]SchemaChangeFact{}, five...), other), t0); len(got) != 6 {
		t.Errorf("two programs: %+v", got)
	}
}

func TestSeenWindowAndReplace(t *testing.T) {
	oldest := time.Date(2026, 10, 2, 3, 47, 41, 0, time.UTC)
	if got := SeenWindowStart(oldest); !got.Equal(time.Date(2026, 10, 2, 4, 0, 0, 0, time.UTC)) {
		t.Errorf("window start %v", got)
	}

	a := Change{ID: 1, Database: "W", ChangedAt: t0, Object: "dbo.P1", ChangeType: "PROCEDURE", Description: "d1", Result: ChangeSeenOnServer}
	b := Change{ID: 2, Database: "W", ChangedAt: t0.Add(time.Minute), Object: "dbo.P2", ChangeType: "PROCEDURE", Description: "d2", Result: ChangeSeenOnServer}
	// The same rows again: nothing to do.
	fa, fb := a, b
	fa.ID, fb.ID = 0, 0
	if stale, add := DiffSeenChanges([]Change{a, b}, []Change{fb, fa}); len(stale) != 0 || len(add) != 0 {
		t.Errorf("same: %v %v", stale, add)
	}
	// The hour recomputed as one deploy: the two rows go, the deploy comes.
	deploy := Change{Database: "W", ChangedAt: t0.Add(50 * time.Minute), Object: "6 objects", ChangeType: ChangeTypeDeploy, Description: "Deploy", Statement: "x"}
	if stale, add := DiffSeenChanges([]Change{a, b}, []Change{deploy}); len(stale) != 2 || stale[0] != 1 || stale[1] != 2 || len(add) != 1 {
		t.Errorf("replace: %v %v", stale, add)
	}
	// Two equal rows kept once each.
	a2 := a
	a2.ID = 3
	if stale, add := DiffSeenChanges([]Change{a, a2}, []Change{fa}); len(stale) != 1 || stale[0] != 3 || len(add) != 0 {
		t.Errorf("duplicates: %v %v", stale, add)
	}
	if SchemaChangesSeenKey(4) != "schemachanges.seen.4" {
		t.Error("key")
	}
}

func traceFacts() UpkeepFacts {
	f := goodUpkeep()
	// setupNow is 2026-11-03 11:21 server time, 09:21 UTC; the trace holds 34 days.
	oldest := time.Date(2026, 9, 30, 3, 47, 0, 0, time.UTC)
	newest := time.Date(2026, 11, 3, 9, 20, 0, 0, time.UTC)
	f.TraceOldestUTC, f.TraceNewestUTC, f.LoginName = &oldest, &newest, `DOMAIN\kakapo`
	return f
}

func TestChangeWindow(t *testing.T) {
	f := traceFacts()
	if f.TraceDays() != 34 {
		t.Fatalf("days %d", f.TraceDays())
	}
	w := BuildChangeWindow(f, nil)
	want := "Changes made outside kakapo are read from SQL Server's default trace, which holds 34 days here (since 30 Sep 2026). kakapo keeps what it reads, so changes older than that stay here as long as this page was opened within 34 days of them."
	if w.State != ChangeWindowRunning || w.Text != want || w.Days != 34 || w.Copy != "" {
		t.Errorf("running %+v", w)
	}
	// A previous harvest saw events after the trace's oldest: no gap.
	seen := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	if w := BuildChangeWindow(f, &seen); w.State != ChangeWindowRunning {
		t.Errorf("no gap %+v", w)
	}
	// The newest event the previous harvest saw is older than the trace's oldest: a gap, on the server's clock.
	seen = time.Date(2026, 9, 20, 14, 5, 0, 0, time.UTC)
	w = BuildChangeWindow(f, &seen)
	if w.State != ChangeWindowGap || !strings.HasPrefix(w.Text, "Changes made between 20 Sep 2026 16:05 and 30 Sep 2026 05:47 were not seen: the trace turned over before kakapo was opened again. Changes made outside") {
		t.Errorf("gap %+v", w)
	}
	off := goodUpkeep()
	off.TraceRunning = false
	if w := BuildChangeWindow(off, nil); w.State != ChangeWindowOff || w.Text != "SQL Server's default trace is off, so changes made outside kakapo are not seen." {
		t.Errorf("off %+v", w)
	}
	unread := goodUpkeep()
	unread.TraceRunning, unread.TraceError, unread.LoginName = false, "the login does not have ALTER TRACE.", `DOMAIN\kakapo`
	if w := BuildChangeWindow(unread, nil); w.State != ChangeWindowUnread ||
		w.Text != "Could not read the default trace (needs ALTER TRACE): the login does not have ALTER TRACE." || w.Copy != `GRANT ALTER TRACE TO [DOMAIN\kakapo];` {
		t.Errorf("unread %+v", w)
	}
	if BuildChangeWindow(UpkeepFacts{Error: "x"}, nil) != nil {
		t.Error("nothing read: no line")
	}
}

func TestCHG01(t *testing.T) {
	// The trace runs and holds 34 days: CHG01 passes.
	_, fs, cs := runUpkeep(traceFacts())
	if len(findingsOf(fs, "CHG01")) != 0 || statusOf(cs, "CHG01").Status != CheckPassed {
		t.Errorf("34 days: %+v", fs)
	}
	// Off: Medium, with the fix.
	f := goodUpkeep()
	f.TraceRunning = false
	_, fs, _ = runUpkeep(f)
	x := findingsOf(fs, "CHG01")
	if len(x) != 1 || x[0].Severity != SeverityMedium || x[0].FixID != FixEnableDefaultTrace || x[0].Owner != OwnerUs ||
		x[0].Finding != "SQL Server's default trace is off, so kakapo cannot see changes made outside it, nor shrinks, cache clears and growths (MNT05, MNT06)." {
		t.Fatalf("off: %+v", x)
	}
	fixes := AvailableUpkeepFixes(f, fs)
	found := false
	for _, fx := range fixes {
		if fx.ID == FixEnableDefaultTrace && fx.Scope == ScopeServer && fx.Setting == DefaultTraceSetting && fx.Current == "0 (off)" && fx.Target == "1 (on)" {
			found = true
		}
	}
	if !found {
		t.Errorf("fixes %+v", fixes)
	}
	// Five days: Low, no fix, open the page within five days.
	f = traceFacts()
	five := f.AsOf().Add(-5*24*time.Hour - time.Hour)
	f.TraceOldestUTC = &five
	_, fs, _ = runUpkeep(f)
	x = findingsOf(fs, "CHG01")
	if len(x) != 1 || x[0].Severity != SeverityLow || x[0].FixID != "" ||
		x[0].Finding != "The default trace holds only 5 days here, less than the week the tuning report compares against." ||
		x[0].DoThis != "Open this database's page at least every 5 days so kakapo keeps the changes; a busy server turns its trace over fast." {
		t.Errorf("five days: %+v", x)
	}
	// Not readable: skipped.
	f = goodUpkeep()
	f.TraceError = "the login does not have ALTER TRACE."
	_, _, cs = runUpkeep(f)
	if st := statusOf(cs, "CHG01"); st.Status != CheckSkipped || !strings.Contains(st.Note, "ALTER TRACE") {
		t.Errorf("unread: %+v", st)
	}
}

func TestEnableDefaultTraceFix(t *testing.T) {
	f := DefaultTraceFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", ValueInUse: 0,
		Pending: []PendingConfig{{Name: "max degree of parallelism", Value: 4, ValueInUse: 0}}}
	p, fix, err := BuildEnableDefaultTraceFix(f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'default trace enabled', 1; RECONFIGURE;" ||
		p.Rollback != "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'default trace enabled', 0; RECONFIGURE;" {
		t.Errorf("statement %q rollback %q", p.Statement, p.Rollback)
	}
	if fix.ID != FixEnableDefaultTrace || fix.Title != "Switch SQL Server's default trace back on" || fix.CheckID != "CHG01" || fix.Scope != ScopeServer ||
		fix.Current != "0 (off)" || fix.Target != "1 (on)" || len(p.Changes) != 1 || p.Changes[0].Setting != DefaultTraceSetting {
		t.Errorf("fix %+v changes %+v", fix, p.Changes)
	}
	w := strings.Join(p.Warnings, "|")
	for _, want := range []string{"This is a server setting: it changes every database on SQL01\\WMS.", "at most 100 MB on disk (5 files of 20 MB)",
		"RECONFIGURE also activates these pending changes: max degree of parallelism", "ALTER SETTINGS"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q: %v", want, p.Warnings)
		}
	}
	if d := EnableDefaultTraceDescription(fix, f); !strings.HasPrefix(d, `default trace enabled on SQL01\WMS: 0 (off) -> 1 (on); RECONFIGURE also activated: max degree of parallelism`) {
		t.Errorf("description %q", d)
	}
	// On and running: nothing to apply.
	if _, _, err := BuildEnableDefaultTraceFix(DefaultTraceFacts{ValueInUse: 1, Running: true}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("on: %v", err)
	}
	// Read back.
	if s, ok := DefaultTraceReadBack(DefaultTraceFacts{ValueInUse: 1, Running: true}); !ok || s != "default trace enabled 1 (on), the default trace runs" {
		t.Errorf("read back %q %v", s, ok)
	}
	if s, ok := DefaultTraceReadBack(DefaultTraceFacts{ValueInUse: 0}); ok || s != "default trace enabled 0 (off), no default trace runs" {
		t.Errorf("read back off %q %v", s, ok)
	}
}

func TestSeenChangesInTheTuningReport(t *testing.T) {
	for _, typ := range []string{ChangeTypeDeploy, "FUNCTION", "TRIGGER", "PROCEDURE", "INDEX", "SETTING"} {
		if !IsPerformanceChange(Change{ChangeType: typ, Result: ChangeSeenOnServer}) {
			t.Errorf("%s is a performance change", typ)
		}
	}
	if IsPerformanceChange(Change{ChangeType: "TABLE", Result: ChangeSeenOnServer}) {
		t.Error("a table seen alone is not")
	}
	r := BuildTuningReport(TuningFacts{Database: "W", ServerNow: setupNow, UTCOffsetMinutes: 120},
		[]Change{{ID: 5, ChangedAt: t0.AddDate(0, 0, -3), Object: "6 objects", ChangeType: ChangeTypeDeploy, Database: "W", Result: ChangeSeenOnServer}},
		TuningOptions{})
	if len(r.Changes) != 1 || !r.Changes[0].OnServer || r.Changes[0].ByHand || !r.Changes[0].Performance || r.Centre.ChangeID != 5 ||
		!strings.HasPrefix(r.Centre.Label, "the deploy of 6 objects on ") {
		t.Errorf("tuning %+v centre %+v", r.Changes, r.Centre)
	}
}
