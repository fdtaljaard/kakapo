package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Did my change help? (docs/phase5h-contract.md) on the 5c fixture: a change on Tuesday 3 November at
// 14:20, read on Wednesday at 10:30; Now is 4 business hours (15:00, 16:00, 08:00, 09:00), Before the 6
// business hours earlier the same day.

func TestParseFocusObject(t *testing.T) {
	for _, c := range []struct {
		name, typ string
		ok        bool
		schema    string
		object    string
		index     string
	}{
		{"dbo.GetOrders", "PROCEDURE", true, "dbo", "GetOrders", ""},
		{"[dbo].[Get.Orders]", "procedure", true, "dbo", "Get.Orders", ""},
		{"[a]]b].[x]", "VIEW", true, "a]b", "x", ""},
		{"GetOrders", "PROCEDURE", true, "dbo", "GetOrders", ""}, // a bare name is in dbo
		{"Shop.sales.GetOrders", "FUNCTION", true, "sales", "GetOrders", ""},
		{"sales.trOrders", "TRIGGER", true, "sales", "trOrders", ""},
		{"dbo.T.IX_T_V", "INDEX", true, "dbo", "T", "IX_T_V"},
		{"[dbo].[T].[IX T]", "INDEX", true, "dbo", "T", "IX T"},
		{"T.IX_T_V", "INDEX", true, "dbo", "T", "IX_T_V"},
		{"IX_T_V", "INDEX", true, "dbo", "", "IX_T_V"}, // by hand, without its table
		{"a.b.c.d", "PROCEDURE", false, "", "", ""},
		{"a.b.c.d", "INDEX", false, "", "", ""},
		{"dbo..x", "PROCEDURE", false, "", "", ""},
		{"", "PROCEDURE", false, "", "", ""},
		{"dbo.x\tz", "PROCEDURE", false, "", "", ""},
		{"dbo.StockUnit", "STATISTICS", false, "", "", ""},
		{"dbo.T", "TABLE", false, "", "", ""},
	} {
		o, ok := ParseFocusObject(c.name, c.typ)
		if ok != c.ok || (ok && (o.Schema != c.schema || o.Object != c.object || o.Index != c.index || o.Name != c.name)) {
			t.Errorf("%q %s: %+v %v", c.name, c.typ, o, ok)
		}
	}
}

func TestParseDeployStatement(t *testing.T) {
	stmt := strings.Join([]string{
		"Procedure dbo.A altered", "Table dbo.T created", "Function dbo.F created", "Trigger sales.TR altered",
		"Procedure dbo.Old dropped", "View dbo.V altered", "Index dbo.T.IX created", "Procedure dbo.A altered",
		"Procedure [dbo].[B C] created", "Procedure ? altered", "Database Shop altered",
	}, "\n")
	got := []string{}
	for _, o := range ParseDeployStatement(stmt) {
		got = append(got, o.Type+" "+o.Schema+"."+o.Object)
	}
	if want := "PROCEDURE dbo.A,FUNCTION dbo.F,TRIGGER sales.TR,PROCEDURE dbo.B C"; strings.Join(got, ",") != want {
		t.Errorf("deploy objects %v, want %s", got, want)
	}
	// 5f's own line format, and no cap.
	lines := []string{}
	for i := 0; i < 40; i++ {
		x := SchemaChangeFact{EventClass: TraceObjectAltered, ObjectType: objTypeProcedure, Schema: "dbo", Name: fmt.Sprintf("P%02d", i)}
		lines = append(lines, x.line())
	}
	if n := len(ParseDeployStatement(strings.Join(lines, "\n"))); n != 40 {
		t.Errorf("%d of 40 deploy objects", n)
	}
}

func TestTuningFocusFor(t *testing.T) {
	for typ, kind := range map[string]string{"PROCEDURE": FocusModule, "FUNCTION": FocusModule, "TRIGGER": FocusModule, "VIEW": FocusView,
		"INDEX": FocusIndex, "SETTING": "", "STATISTICS": "", "PLAN": "", "TABLE": "", "OTHER": ""} {
		if got := TuningFocusFor(Change{ID: 3, Object: "dbo.X.Y", ChangeType: typ}); got.Kind != kind {
			t.Errorf("%s: kind %q, want %q", typ, got.Kind, kind)
		}
	}
	if got := TuningFocusFor(Change{ID: 3, Object: "dbo.GetOrders", ChangeType: "PROCEDURE"}); got.Kind != FocusModule || got.ChangeID != 3 ||
		got.FocusNames() != "dbo\tGetOrders\t" {
		t.Errorf("procedure %+v %q", got, got.FocusNames())
	}
	if got := TuningFocusFor(Change{Object: "IX_T_V", ChangeType: "INDEX"}); got.FocusNames() != "dbo\t\tIX_T_V" {
		t.Errorf("index without table %q", got.FocusNames())
	}
	d := TuningFocusFor(Change{Object: "3 objects", ChangeType: ChangeTypeDeploy, Statement: "Procedure dbo.A altered\nTable dbo.T created\nTrigger dbo.TR created"})
	if d.Kind != FocusModule || !d.Deploy || d.FocusNames() != "dbo\tA\t\ndbo\tTR\t" {
		t.Errorf("deploy %+v %q", d, d.FocusNames())
	}
	if d := TuningFocusFor(Change{Object: "8 objects", ChangeType: ChangeTypeDeploy, Statement: "Table dbo.T created\nView dbo.V created"}); d.Kind != "" {
		t.Errorf("a deploy of tables and views is followed: %+v", d)
	}
}

func TestTuningCentreChange(t *testing.T) {
	cs := []Change{tunChange(4, nov(2, 10, 0), "dbo.A", "PROCEDURE"), tunChange(5, nov(3, 14, 20), "x", "CHECK"), tunChange(6, nov(3, 9, 0), "dbo.B", "VIEW")}
	if c := TuningCentreChange(cs, 0); c == nil || c.ID != 6 {
		t.Errorf("latest performance change %+v", c)
	}
	if c := TuningCentreChange(cs, 4); c == nil || c.ID != 4 {
		t.Errorf("chosen %+v", c)
	}
	if c := TuningCentreChange(cs, TuningChangeNone); c != nil {
		t.Errorf("none %+v", c)
	}
}

// seen is a change seen on the server at a server time.
func seen(id int64, at time.Time, object, kind, description string) Change {
	c := tunChange(id, at, object, kind)
	c.Result, c.Description, c.Statement = ChangeSeenOnServer, description, ""
	return c
}

// spec is a followed object's business hours: runs and CPU per run (ms) before and after the change.
type spec struct {
	runsBefore, runsAfter int64
	msBefore, msAfter     float64
}

// focusHours are a spec's hours for line n (and a query hash for a view or an index), for every
// business hour of the fixture.
func focusHours(n int, hash string, changeAt time.Time, s spec) []TuningFocusHourFact {
	out := []TuningFocusHourFact{}
	for _, h := range weekHours(changeAt) {
		if bh0817.Class(h.Hour) != HourBusiness {
			continue
		}
		runs, ms := s.runsBefore, s.msBefore
		if !h.Hour.Before(changeAt.Truncate(time.Hour)) {
			runs, ms = s.runsAfter, s.msAfter
		}
		if runs > 0 {
			out = append(out, TuningFocusHourFact{N: n, QueryHash: hash, Hour: h.Hour, Runs: runs, CPU: int64(float64(runs) * ms * 1000)})
		}
	}
	return out
}

// moduleReport is the report centred on a procedure altered on Tuesday at 14:20 (seen on the server).
func moduleReport(changeAt time.Time, item TuningFocusItemFact, s spec) TuningReport {
	f := tunFacts(changeAt, 7)
	c := seen(11, changeAt, "dbo.GetOrders", "PROCEDURE", `Procedure dbo.GetOrders altered by DOMAIN\jane on APP01 (Microsoft SQL Server Management Studio)`)
	f.Focus = TuningFocusFor(c)
	item.N = 1
	f.FocusItems = []TuningFocusItemFact{item}
	f.FocusHours = focusHours(1, "", changeAt, s)
	return BuildTuningReport(f, []Change{c}, TuningOptions{Business: bh0817})
}

func found() TuningFocusItemFact {
	created := oct(1, 9, 0)
	return TuningFocusItemFact{ObjectID: 42, Created: &created, Modified: &created, QueryHash: "0x00000000000000AA"}
}

func TestFocusModuleSentences(t *testing.T) {
	at := nov(3, 14, 20)
	for _, c := range []struct {
		name    string
		item    TuningFocusItemFact
		s       spec
		verdict string
		want    string
	}{
		{"faster", found(), spec{3200, 3150, 12.4, 8.6}, VerdictFaster,
			"Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses 8.6 ms of CPU per run instead of 12.4 ms (31% less) over 4 business hours, and it runs about as often (3,150 runs an hour against 3,200): the change made it cheaper."},
		{"slower", found(), spec{3200, 3150, 12.4, 18}, VerdictSlower,
			"Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses 18.0 ms of CPU per run instead of 12.4 ms (45% more) over 4 business hours, and it runs about as often (3,150 runs an hour against 3,200): Investigate it."},
		{"same", found(), spec{3200, 3150, 12.4, 12.1}, VerdictSame,
			"Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses about the same CPU per run as before (12.1 ms against 12.4 ms): the change made no difference that Query Store can see."},
		{"same, runs more", found(), spec{3200, 4000, 12.4, 12.1}, VerdictSame,
			"Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses about the same CPU per run as before (12.1 ms against 12.4 ms), but it runs more often (4,000 runs an hour against 3,200), so its load changed even if each run did not."},
		{"faster, runs less", found(), spec{3200, 1000, 12.4, 8.6}, VerdictFaster,
			"Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses 8.6 ms of CPU per run instead of 12.4 ms (31% less) over 4 business hours, and it runs less often (1,000 runs an hour against 3,200): the change made it cheaper."},
		{"too few", found(), spec{3200, 2, 12.4, 8.6}, VerdictTooFew,
			"dbo.GetOrders ran only 8 times since the change; kakapo needs 20 to compare."},
		{"too few, none since", found(), spec{3, 0, 12.4, 0}, VerdictTooFew,
			"dbo.GetOrders has not run since the change, and ran only 18 times before it; kakapo needs 20 to compare."},
		{"gone", found(), spec{3200, 0, 12.4, 0}, VerdictGone, "dbo.GetOrders has not run since the change."},
		{"new", found(), spec{0, 30, 0, 8.6}, VerdictNew, "Query Store holds no runs of dbo.GetOrders before the change in the last 7 days."},
		{"no runs at all", found(), spec{}, VerdictTooFew, "Query Store holds no runs of dbo.GetOrders before the change in the last 7 days."},
		{"dropped and created", func() TuningFocusItemFact {
			it := found()
			c := nov(3, 14, 19) // the commit row comes a moment after the create
			it.Created = &c
			return it
		}(), spec{0, 3150, 0, 8.6}, "",
			"dbo.GetOrders was dropped and created again on Tue 3 Nov 14:19, so Query Store sees a new object and holds nothing before it. ALTER (or CREATE OR ALTER) keeps an object's history."},
		{"not found", TuningFocusItemFact{}, spec{}, "", "kakapo cannot find dbo.GetOrders in WarehouseLive, so the report compares the whole database."},
	} {
		r := moduleReport(at, c.item, c.s)
		fo := r.Focus
		if fo == nil {
			t.Fatalf("%s: no focus", c.name)
		}
		if fo.Verdict != c.verdict || fo.Sentence != c.want || r.Explain[0].Text != c.want {
			t.Errorf("%s: verdict %q\n%s\nwant\n%s", c.name, fo.Verdict, fo.Sentence, c.want)
		}
	}

	// The numbers, from the report's own windows, sums not averages; the module's runs are the read's
	// (its most-run statement), never summed again here.
	r := moduleReport(at, found(), spec{3200, 3150, 12.4, 8.6})
	fo := r.Focus
	if fo.Before.Hours != r.Before.Hours || fo.Now.Hours != r.Now.Hours || fo.Before.Hours != 6 || fo.Now.Hours != 4 {
		t.Errorf("windows: focus %d/%d, report %d/%d", fo.Before.Hours, fo.Now.Hours, r.Before.Hours, r.Now.Hours)
	}
	if fo.Before.Runs != 6*3200 || fo.Now.Runs != 4*3150 || val(fo.Before.CPUPerRunMs) != 12.4 || val(fo.Now.CPUPerRunMs) != 8.6 ||
		val(fo.Now.RunsPerHour) != 3150 || val(fo.Now.CPUPerHourMs) != 27090 || val(fo.Before.CPUPerHourMs) != 39680 {
		t.Errorf("numbers %+v %+v", fo.Before, fo.Now)
	}
	if !fo.Found || fo.Kind != FocusModule || fo.QueryHash != "0x00000000000000AA" || len(fo.Hours) != len(r.Hours) {
		t.Errorf("focus %+v", fo)
	}
	// Its chart: CPU per run per hour, the report's windows; an hour it did not run is no data.
	for i, h := range fo.Hours {
		if h.Window != r.Hours[i].Window || h.Hour != r.Hours[i].Hour {
			t.Fatalf("hour %d: %+v against %+v", i, h, r.Hours[i])
		}
		if h.Hour == "2026-11-03T15:00:00" && (h.CPUPerExecMs != 8.6 || h.Executions != 3150 || h.CoresBusy != 0.008) {
			t.Errorf("15:00 %+v", h)
		}
		if h.Class == HourNight && !h.NoData {
			t.Errorf("a night hour without runs has data: %+v", h)
		}
	}
	// Investigate and Copy on the sentence; Copy for IT starts with the object's block.
	if l := r.Explain[0]; l.QueryHash != "0x00000000000000AA" || !strings.HasPrefix(l.Copy, "dbo.GetOrders in WarehouseLive\nQuery hash of its costliest statement: 0x00000000000000AA\n\nDid it help dbo.GetOrders?") ||
		!strings.Contains(l.Copy, "- CPU per run: 12.4 ms -> 8.6 ms (19,200 runs -> 12,600 runs); faster") {
		t.Errorf("line %+v", l)
	}
	if !strings.HasPrefix(r.Handover, "Did it help dbo.GetOrders?\n- Since procedure") || !strings.Contains(r.Handover, "\n\nTuning report for WarehouseLive") {
		t.Errorf("handover:\n%s", r.Handover)
	}
	if !strings.HasPrefix(r.Explain[1].Text, "Since the change to procedure dbo.GetOrders on Tue 3 Nov 14:20, CPU per execution") {
		t.Errorf("the database's headline is not second: %q", r.Explain[1].Text)
	}

	// A change recorded by hand is "changed".
	f := tunFacts(at, 7)
	hand := tunChange(12, at, "GetOrders", "PROCEDURE")
	hand.Result = ChangeRecordedByHand
	f.Focus = TuningFocusFor(hand)
	it := found()
	it.N = 1
	f.FocusItems, f.FocusHours = []TuningFocusItemFact{it}, focusHours(1, "", at, spec{3200, 3150, 12.4, 8.6})
	if r := BuildTuningReport(f, []Change{hand}, TuningOptions{Business: bh0817}); r.Focus == nil ||
		!strings.HasPrefix(r.Focus.Sentence, "Since procedure GetOrders was changed on Tue 3 Nov 14:20, it uses 8.6 ms") {
		t.Errorf("by hand: %+v", r.Focus)
	}
}

func TestFocusTooEarlyAndNotFollowed(t *testing.T) {
	// One complete business hour since the change: 5c says "too early", the object says nothing.
	at := nov(4, 8, 10)
	r := moduleReport(at, found(), spec{3200, 3150, 12.4, 8.6})
	if r.Focus == nil || r.Focus.Sentence != "" || r.Focus.Verdict != VerdictFaster || !strings.HasPrefix(r.Explain[0].Text, "Since the change to procedure") {
		t.Errorf("too early: %+v\n%v", r.Focus, r.Explain)
	}
	// Structural facts still speak: not found.
	r = moduleReport(at, TuningFocusItemFact{}, spec{})
	if r.Focus.Sentence != "kakapo cannot find dbo.GetOrders in WarehouseLive, so the report compares the whole database." {
		t.Errorf("not found, too early: %q", r.Focus.Sentence)
	}

	// A type that is not followed, the read followed another change, or the read failed: no focus.
	f := tunFacts(nov(3, 14, 20), 7)
	stats := tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")
	f.Focus = TuningFocusFor(stats)
	if r := BuildTuningReport(f, []Change{stats}, TuningOptions{Business: bh0817}); r.Focus != nil {
		t.Errorf("statistics followed: %+v", r.Focus)
	}
	proc := tunChange(6, nov(3, 14, 20), "dbo.GetOrders", "PROCEDURE")
	f.Focus = TuningFocusFor(proc)
	if r := BuildTuningReport(f, []Change{stats, proc}, TuningOptions{ChangeID: 5, Business: bh0817}); r.Focus != nil {
		t.Errorf("another change followed: %+v", r.Focus)
	}
	f.FocusError = "Invalid object name"
	r = BuildTuningReport(f, []Change{proc}, TuningOptions{Business: bh0817})
	if r.Focus != nil || !strings.Contains(strings.Join(r.Notes, "\n"), "Did it help: could not be read: Invalid object name") {
		t.Errorf("failed read: %+v %v", r.Focus, r.Notes)
	}
	// The JSON carries focus null without a followed change, an object with one.
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"focus":null`) || !strings.Contains(string(b), `"coresBusy":0.003`) {
		t.Errorf("json without focus: %.200s", b)
	}
}

// indexReport is the report centred on index IX_T_V created by kakapo's fix on Tuesday at 14:20; four
// queries use it, each 50 runs an hour, 22 ms per run before and 0.9 ms after.
func indexReport(item TuningFocusItemFact, object string) TuningReport {
	at := nov(3, 14, 20)
	f := tunFacts(at, 7)
	c := tunChange(21, at, object, "INDEX")
	c.Result, c.Description = "applied, verified", "Created index IX_T_V in WarehouseLive"
	f.Focus = TuningFocusFor(c)
	item.N = 1
	f.FocusItems = []TuningFocusItemFact{item}
	for i := 0; i < 4; i++ {
		h := fmt.Sprintf("0x00000000000000B%d", i)
		f.FocusHours = append(f.FocusHours, focusHours(1, h, at, spec{50, 50, 22, 0.9})...)
		f.FocusQueries = append(f.FocusQueries, TuningFocusQueryFact{N: 1, QueryHash: h, Text: fmt.Sprintf("SELECT v FROM dbo.T WHERE k = @k%d", i)})
	}
	return BuildTuningReport(f, []Change{c}, TuningOptions{Business: bh0817})
}

func TestFocusIndex(t *testing.T) {
	start := oct(20, 6, 0)
	used := TuningFocusItemFact{ObjectID: 77, Table: "dbo.T", IndexFound: true, UsageRow: true, Seeks: 60, Updates: 1200, ServerStart: &start,
		QueryHash: "0x00000000000000B0"}
	r := indexReport(used, "dbo.T.IX_T_V")
	fo := r.Focus
	want := "Since index IX_T_V was created on dbo.T on Tue 3 Nov 14:20, the 4 queries that use it take 0.9 ms of CPU per run instead of 22.0 ms (96% less) over 4 business hours, and they run about as often (200 runs an hour against 200): the change made them cheaper. It was used 60 times (60 seeks); SQL Server also updates it on every write to dbo.T (1,200 updates so far)."
	if fo == nil || fo.Sentence != want || fo.Verdict != VerdictFaster {
		t.Fatalf("index used:\n%+v\nwant\n%s", fo, want)
	}
	// The queries that use it, named, before and after.
	if len(fo.Rows) != 4 || fo.Rows[0].Verdict != VerdictFaster || !strings.HasPrefix(fo.Rows[0].Name, `the ad hoc query "SELECT v FROM dbo.T`) ||
		val(fo.Rows[0].CPUBeforeMs) != 22 || val(fo.Rows[0].CPUNowMs) != 0.9 || fo.Rows[0].RunsPerHourNow != 50 {
		t.Errorf("rows %+v", fo.Rows)
	}
	if fo.Index == nil || !fo.Index.Used || fo.Index.Since != "2026-11-03T14:20:00" || fo.Index.Table != "dbo.T" ||
		!strings.Contains(strings.Join(fo.Notes, " "), "through a view or a nested call is not followed") {
		t.Errorf("index %+v %v", fo.Index, fo.Notes)
	}

	// Not used: no usage row since SQL Server started after the change; "since" is the later of the two.
	restart := nov(4, 7, 0)
	unused := TuningFocusItemFact{ObjectID: 77, Table: "dbo.T", IndexFound: true, ServerStart: &restart}
	r = indexReport(unused, "dbo.T.IX_T_V")
	want = "Index IX_T_V on dbo.T has not been used since Wed 4 Nov 07:00; SQL Server still updates it on every write to dbo.T (0 updates). The counters start again when SQL Server restarts or the database is restored or taken offline (SQL Server last started Wed 4 Nov 07:00)."
	if r.Focus.Sentence != want {
		t.Errorf("index not used:\n%s\nwant\n%s", r.Focus.Sentence, want)
	}
	// All zeros, started before the change: since the change.
	zeros := unused
	zeros.UsageRow, zeros.Updates, zeros.ServerStart = true, 1200, &start
	r = indexReport(zeros, "dbo.T.IX_T_V")
	if !strings.HasPrefix(r.Focus.Sentence, "Index IX_T_V on dbo.T has not been used since Tue 3 Nov 14:20; SQL Server still updates it on every write to dbo.T (1,200 updates).") ||
		!strings.HasSuffix(r.Focus.Sentence, "(SQL Server last started Tue 20 Oct 06:00).") {
		t.Errorf("index all zeros: %s", r.Focus.Sentence)
	}

	// By hand without its table: found by its name alone, and the page says so.
	r = indexReport(used, "IX_T_V")
	if r.Focus == nil || !r.Focus.Found || !strings.Contains(strings.Join(r.Focus.Notes, " "), "names no table, so kakapo found index IX_T_V by its name alone (on dbo.T)") {
		t.Errorf("index without table %+v", r.Focus)
	}
	// Not found.
	r = indexReport(TuningFocusItemFact{}, "dbo.T.IX_T_V")
	if r.Focus.Sentence != "kakapo cannot find index IX_T_V on dbo.T in WarehouseLive, so the report compares the whole database." {
		t.Errorf("index not found: %s", r.Focus.Sentence)
	}
}

func TestFocusDeploy(t *testing.T) {
	at := nov(3, 14, 20)
	f := tunFacts(at, 7)
	c := seen(31, at, "5 objects", ChangeTypeDeploy, `Deploy by DOMAIN\build on BUILD01 (DacFx): 5 objects (3 procedures, 2 tables)`)
	c.Statement = "Table dbo.T1 created\nProcedure dbo.A altered\nProcedure dbo.B altered\nTable dbo.T2 created\nProcedure dbo.C altered\nProcedure dbo.D dropped"
	f.Focus = TuningFocusFor(c)
	for i := 1; i <= 3; i++ {
		f.FocusItems = append(f.FocusItems, TuningFocusItemFact{N: i, ObjectID: int64(100 + i), QueryHash: fmt.Sprintf("0x00000000000000C%d", i)})
	}
	f.FocusHours = append(focusHours(1, "", at, spec{3200, 3150, 12.4, 8.6}), focusHours(2, "", at, spec{100, 100, 12.4, 18})...)
	f.FocusHours = append(f.FocusHours, focusHours(3, "", at, spec{1, 1, 5, 5})...)
	r := BuildTuningReport(f, []Change{c}, TuningOptions{Business: bh0817})
	fo := r.Focus
	want := `Of the 5 objects in the deploy by DOMAIN\build on Tue 3 Nov 14:20, 1 got slower and 1 got faster; the rest ran too little to say. Slower: dbo.B, 18.0 ms per run instead of 12.4 ms. Faster: dbo.A, 8.6 ms per run instead of 12.4 ms.`
	if fo == nil || fo.Kind != FocusDeploy || fo.Sentence != want || r.Explain[0].Text != want || r.Explain[0].QueryHash != "" {
		t.Fatalf("deploy:\n%+v\nwant\n%s", fo, want)
	}
	order := []string{}
	for _, row := range fo.Rows {
		order = append(order, row.Name+"="+row.Verdict)
	}
	// Sorted by the CPU saved or added per business hour, too few last.
	if strings.Join(order, ",") != "dbo.A=faster,dbo.B=slower,dbo.C=too few" || fo.Rows[0].QueryHash != "0x00000000000000C1" {
		t.Errorf("deploy rows %v", order)
	}
	if !strings.Contains(r.Handover, "- dbo.B (0x00000000000000C2): 12.4 ms -> 18.0 ms per run; 600 -> 400 runs; slower") {
		t.Errorf("handover:\n%s", r.Handover)
	}
	if deployBy("Deploy by ? (x): 6 objects") != "" || deployBy("something else") != "" {
		t.Error("deployBy")
	}
}

func TestFocusView(t *testing.T) {
	at := nov(3, 14, 20)
	f := tunFacts(at, 7)
	c := seen(41, at, "dbo.vOrders", "VIEW", `View dbo.vOrders altered by DOMAIN\jane on APP01 (SSMS)`)
	f.Focus = TuningFocusFor(c)
	f.FocusItems = []TuningFocusItemFact{{N: 1, ObjectID: 9, QueryHash: "0x00000000000000D0"}}
	f.FocusHours = focusHours(1, "0x00000000000000D0", at, spec{40, 40, 30, 10})
	f.FocusQueries = []TuningFocusQueryFact{{N: 1, QueryHash: "0x00000000000000D0", ObjectName: "dbo.Report", Text: "SELECT * FROM dbo.vOrders"}}
	r := BuildTuningReport(f, []Change{c}, TuningOptions{Business: bh0817})
	want := "Since view dbo.vOrders was altered on Tue 3 Nov 14:20, the 1 query that names it uses 10.0 ms of CPU per run instead of 30.0 ms (67% less) over 4 business hours, and it runs about as often (40 runs an hour against 40): the change made it cheaper."
	if r.Focus == nil || r.Focus.Sentence != want || len(r.Focus.Rows) != 1 || r.Focus.Rows[0].Name != "dbo.Report" {
		t.Errorf("view:\n%+v\nwant\n%s", r.Focus, want)
	}
}
