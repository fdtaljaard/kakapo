package domain

import (
	"strings"
	"testing"
	"time"
)

// An idle database: Query Store keeps an interval only when something ran, so business hours can pass
// without a busy one. The timeline still shows every hour (idle ones as noData), the same-hour table
// shows them as idle, and "too early" says the hours passed but held no queries.
func TestIdleHoursTooEarly(t *testing.T) {
	f := tunFacts(time.Time{}, 7)
	f.ServerNow = nov(4, 14, 45)
	kept := []TuningHourFact{}
	for _, h := range f.Hours {
		if h.Hour.Before(nov(4, 0, 0)) {
			kept = append(kept, h)
		}
	}
	f.Hours = append(kept, TuningHourFact{Hour: nov(4, 8, 0), Executions: 40, CPU: 40 * 9000},
		TuningHourFact{Hour: nov(4, 13, 0), Executions: 30, CPU: 30 * 8000})
	deployed := tunChange(9, nov(4, 8, 23), "dbo.USP_PICKING", "PROCEDURE") // a procedure deployed at 08:23
	r := BuildTuningReport(f, []Change{deployed}, TuningOptions{Business: bh0817})

	if len(r.Hours) != 7*24+1 || r.Hours[0].Hour != "2026-10-28T14:00:00" {
		t.Fatalf("timeline %d hours from %s", len(r.Hours), r.Hours[0].Hour)
	}
	idle := 0
	for _, h := range r.Hours {
		if h.Hour == "2026-11-04T09:00:00" && (!h.NoData || !h.Complete || h.Executions != 0 || h.Window != "") {
			t.Errorf("idle 09:00 %+v", h)
		}
		if h.NoData {
			idle++
		}
	}
	// Wednesday 00:00 to 07:00, 09:00 to 12:00 and 14:00 (in progress): 13 idle hours shown, none skipped.
	if idle != 13 {
		t.Errorf("%d idle hours, want 13", idle)
	}
	if got := strings.Join(hoursIn(r, "now"), ","); got != "11-04T13" {
		t.Errorf("now %s", got)
	}
	want := "Since the change at Wed 4 Nov 08:23, 5 business hours have passed but only 1 held any query; kakapo needs two busy hours to compare. Look again after a busy morning."
	found := false
	for _, l := range r.Explain {
		found = found || l.Text == want
		if strings.HasPrefix(l.Text, "It is too early") {
			t.Errorf("the hours have passed: %q", l.Text)
		}
	}
	if !found {
		t.Errorf("idle sentence missing:\n%v", r.Explain)
	}
	if h10 := r.SameHour[2]; h10.Day.Note != "idle" || val(h10.Day.Executions) != 0 || h10.Day.CPUPerExecMs != nil {
		t.Errorf("same hour 10:00 %+v", h10.Day)
	}
	if h14 := r.SameHour[6]; h14.Day.Note != "in progress" {
		t.Errorf("same hour 14:00 %+v", h14.Day)
	}

	// No busy hour at all since the change.
	f.Hours = f.Hours[:len(f.Hours)-1]
	r2 := BuildTuningReport(f, []Change{deployed}, TuningOptions{Business: bh0817})
	found = false
	for _, l := range r2.Explain {
		found = found || l.Text == "Since the change at Wed 4 Nov 08:23, 5 business hours have passed but none held any query; kakapo needs two busy hours to compare. Look again after a busy morning."
	}
	if !found {
		t.Errorf("none busy:\n%v", r2.Explain)
	}
	if cpu := tile(r2, "cpu-per-execution"); cpu.NowText != "no runs" || cpu.Now != nil || cpu.Pct != nil || cpu.Tone != ToneSame {
		t.Errorf("cpu tile without runs %+v", cpu)
	}
}

// "Look again after" is always in the future: the end of the second business hour after the change,
// or the next working day's 10:00 when that is past.
func TestLookAgain(t *testing.T) {
	for _, c := range []struct {
		change, now, want time.Time
	}{
		{nov(4, 8, 10), nov(4, 10, 30), nov(4, 11, 0)},  // 09:00 and 10:00 still to end
		{nov(4, 16, 30), nov(4, 16, 40), nov(5, 10, 0)}, // the next morning's 08:00 and 09:00
		{oct(30, 8, 10), oct(30, 18, 0), nov(2, 10, 0)}, // Friday, past: Monday 10:00
		{nov(4, 8, 10), nov(4, 20, 0), nov(5, 10, 0)},   // past: the next working day
	} {
		if got := tLookAgain(c.change, bh0817, map[time.Time]bool{}, c.now); !got.Equal(c.want) || !got.After(c.now) {
			t.Errorf("change %s now %s: %s, want %s", tWhen(c.change), tWhen(c.now), tWhen(got), tWhen(c.want))
		}
	}
	if n := tBusinessHoursPassed(nov(4, 8, 23), time.Time{}, bh0817, map[time.Time]bool{}, nov(4, 14, 45)); n != 5 {
		t.Errorf("passed %d, want 5", n)
	}
	if n := tBusinessHoursPassed(nov(4, 8, 23), nov(4, 11, 0), bh0817, map[time.Time]bool{nov(4, 10, 0): true}, nov(4, 14, 45)); n != 1 {
		t.Errorf("passed up to the next change, without a change hour: %d, want 1", n)
	}
}

// Tiles where both sides are zero, or round to zero, and windows without runs.
func TestTilesZeroAndNoRuns(t *testing.T) {
	b := tSum{hours: 5, execs: 5000, cpu: 5000 * 12000, parallelSec: 0, lockSec: 0.02}
	n := tSum{hours: 2, execs: 2000, cpu: 2000 * 8000, parallelSec: 2 * 3600, lockSec: 0}
	tiles := tTiles(b, n, true)
	lock, par := tiles[3], tiles[2]
	if lock.BeforeText != "none" || lock.NowText != "none" || lock.Pct != nil || lock.Tone != ToneSame {
		t.Errorf("lock 0.00001 -> 0: %+v", lock)
	}
	if par.BeforeText != "none" || par.NowText != "" || par.Pct != nil || par.Tone != ToneWorse || val(par.Now) != 1 {
		t.Errorf("parallel 0 -> 1: %+v", par)
	}
	if got := tTileText(lock); got != "none -> none" {
		t.Errorf("lock text %q", got)
	}

	// Now holds business hours but no executions; Before holds no hours at all.
	tiles = tTiles(b, tSum{hours: 2}, true)
	cpu, ex := tiles[0], tiles[1]
	if cpu.NowText != "no runs" || cpu.Now != nil || cpu.Pct != nil || cpu.Tone != ToneSame || cpu.Words != "" {
		t.Errorf("cpu without runs now %+v", cpu)
	}
	if got := tTileText(cpu); got != "12.0 ms -> no runs" {
		t.Errorf("cpu text %q", got)
	}
	if ex.NowText != "none" || val(ex.Pct) != -100 || ex.Tone != ToneFewer {
		t.Errorf("executions 1,000 -> 0 %+v", ex)
	}
	tiles = tTiles(tSum{}, n, true)
	for _, tl := range tiles {
		if tl.BeforeText != "no runs" || tl.Pct != nil || tl.Tone != ToneSame {
			t.Errorf("no Before: %+v", tl)
		}
	}
	// Without waits recorded the threads tiles say so, not "no runs".
	if tiles = tTiles(b, n, false); tiles[2].BeforeText != "" || tiles[2].NowText != "" || !strings.Contains(tiles[2].Words, "no waits") {
		t.Errorf("no waits %+v", tiles[2])
	}
}

// The default centre is the latest change that can affect performance: a CHECKDB run after the
// statistics update stays in the list but is not the default, and does not cut Now short.
func TestDefaultCentreIsAPerformanceChange(t *testing.T) {
	check := tunChange(9, nov(4, 8, 23), "CHECKDB", "CHECK")
	stats := tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")
	r := BuildTuningReport(tunFacts(nov(3, 14, 20), 7), []Change{check, stats}, TuningOptions{Business: bh0817})
	if r.Centre.ChangeID != 5 || len(r.Changes) != 2 || r.Changes[0].ID != 9 || r.Changes[0].Performance || !r.Changes[1].Performance {
		t.Errorf("centre %+v changes %+v", r.Centre, r.Changes)
	}
	// Wed 08:00 holds the CHECKDB run, so it is not compared; 09:00 still is.
	if got := strings.Join(hoursIn(r, "now"), ","); got != "11-03T15,11-03T16,11-04T09" {
		t.Errorf("now %s", got)
	}
	// It can still be chosen.
	if rc := BuildTuningReport(tunFacts(nov(3, 14, 20), 7), []Change{check, stats}, TuningOptions{ChangeID: 9, Business: bh0817}); rc.Centre.ChangeID != 9 {
		t.Errorf("chosen CHECK: %+v", rc.Centre)
	}
	// Only changes that cannot affect performance: no default centre, and a sentence that says so.
	ro := BuildTuningReport(tunFacts(time.Time{}, 7), []Change{check}, TuningOptions{Business: bh0817})
	if ro.Centre.ChangeID != 0 || ro.Now.Hours != 1 {
		t.Errorf("only a CHECK: %+v %+v", ro.Centre, ro.Now)
	}
	last := ro.Explain[len(ro.Explain)-1]
	if !strings.HasPrefix(last.Text, "No change in the log for the last 7 days can affect performance") || !last.RecordChange {
		t.Errorf("sentence %+v", ro.Explain)
	}

	byHand := RecordedChange{Object: "x", ChangeType: "OTHER", Description: "d"}.Change(1, "db", nov(3, 9, 0))
	for _, c := range []struct {
		c    Change
		want bool
	}{
		{Change{ChangeType: "SETTING"}, true}, {Change{ChangeType: "INDEX"}, true}, {Change{ChangeType: "statistics"}, true},
		{Change{ChangeType: "PLAN", Object: "Query Store plan"}, true}, {Change{ChangeType: "PLAN", Object: "Plan cache"}, true},
		{Change{ChangeType: "PLAN", Object: "Maintenance plan"}, false}, {Change{ChangeType: "PROCEDURE"}, true},
		{Change{ChangeType: "VIEW"}, true}, {Change{ChangeType: "MAINTENANCE"}, true}, {Change{ChangeType: "OTHER"}, true},
		{Change{ChangeType: "CHECK"}, false}, {Change{ChangeType: "KILL"}, false}, {Change{ChangeType: "JOB"}, false},
		{Change{ChangeType: "ALERT"}, false}, {Change{ChangeType: "CLEAR"}, false}, {Change{ChangeType: "ARCHIVE"}, false},
		{Change{ChangeType: "CHECK", Result: ChangeRecordedByHand}, true}, {byHand, true},
	} {
		if got := IsPerformanceChange(c.c); got != c.want {
			t.Errorf("%s %q by hand=%v: %v", c.c.ChangeType, c.c.Object, c.c.Result != "", got)
		}
	}
}
