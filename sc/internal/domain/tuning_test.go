package domain

import (
	"strings"
	"testing"
	"time"
)

// The fixture: two weeks of Query Store hours of one database on a server two hours ahead of UTC,
// read on Wednesday 4 November 2026 at 10:30 server time (the 10:00 hour still in progress). A
// business hour runs 1,000 executions at 12 ms of CPU each with 19 threads waiting on parallelism
// and half a thread on locks; after the change 8 ms and one thread on parallelism. Nights run 100 at
// 5 ms, weekends 50 at 5 ms.

var (
	tzOffset = 120
	readAt   = time.Date(2026, 11, 4, 10, 30, 0, 0, time.UTC) // Wednesday, server time
	bh0817   = BusinessHours{From: 8, To: 17}
)

// nov is a day of November 2026 at a time, server clock.
func nov(day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, time.UTC)
}

// oct is a day of October 2026 at a time, server clock.
func oct(day, hour, minute int) time.Time {
	return time.Date(2026, 10, day, hour, minute, 0, 0, time.UTC)
}

// change is a change log row at a server time (stored in UTC, as the log keeps it).
func tunChange(id int64, at time.Time, object, kind string) Change {
	return Change{ID: id, ServerID: 1, Database: "WarehouseLive", ChangedAt: at.Add(-time.Duration(tzOffset) * time.Minute),
		Object: object, ChangeType: kind, Description: "the " + strings.ToLower(kind) + " change", Statement: "s"}
}

// weekHours is the fixture's hours from 14 days before the read to the hour in progress; the
// business hours from changeAt's hour on cost 8 ms (zero changeAt: never).
func weekHours(changeAt time.Time) []TuningHourFact {
	out := []TuningHourFact{}
	start := readAt.Truncate(time.Hour).Add(-14 * 24 * time.Hour)
	for at := start; !at.After(readAt.Truncate(time.Hour)); at = at.Add(time.Hour) {
		h := TuningHourFact{Hour: at}
		switch bh0817.Class(at) {
		case HourBusiness:
			ms, par := 12.0, 19.0
			if !changeAt.IsZero() && !at.Before(changeAt.Truncate(time.Hour)) {
				ms, par = 8, 1
			}
			h.Executions, h.CPU = 1000, int64(1000*ms*1000)
			h.ParallelWaitSec, h.LockWaitSec = par*3600, 0.5*3600
		case HourNight:
			h.Executions, h.CPU = 100, 100*5*1000
		default:
			h.Executions, h.CPU = 50, 50*5*1000
		}
		h.Reads = h.Executions * 40
		h.Duration = h.CPU * 2
		out = append(out, h)
	}
	return out
}

func tunFacts(changeAt time.Time, days int) TuningFacts {
	oldest := readAt.Add(-30 * 24 * time.Hour)
	return TuningFacts{Database: "WarehouseLive", ServerName: "SQL01", Days: days, ServerNow: readAt, UTCOffsetMinutes: tzOffset,
		QueryStoreState: "READ_WRITE", IntervalMinutes: 15, WaitCapture: "ON", OldestInterval: &oldest, Hours: weekHours(changeAt)}
}

func hoursIn(r TuningReport, window string) []string {
	out := []string{}
	for _, h := range r.Hours {
		if h.Window == window {
			out = append(out, h.Hour[5:13])
		}
	}
	return out
}

func tile(r TuningReport, id string) TuningTile {
	for _, t := range r.Tiles {
		if t.ID == id {
			return t
		}
	}
	return TuningTile{}
}

func val(v *float64) float64 {
	if v == nil {
		return -1
	}
	return *v
}

func TestBusinessHoursAndParsing(t *testing.T) {
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{nov(2, 8, 0), HourBusiness}, {nov(2, 16, 0), HourBusiness}, {nov(2, 17, 0), HourNight}, {nov(2, 7, 0), HourNight},
		{oct(31, 10, 0), HourWeekend}, {nov(1, 23, 0), HourWeekend}, {nov(6, 12, 0), HourBusiness},
	} {
		if got := bh0817.Class(c.at); got != c.want {
			t.Errorf("%s: %s, want %s", c.at.Format("Mon 15:04"), got, c.want)
		}
	}
	if b, err := ParseBusinessHours(""); err != nil || b != bh0817 || b.String() != "08-17" {
		t.Errorf("default %v %v", b, err)
	}
	if b, err := ParseBusinessHours("07-16"); err != nil || b.From != 7 || b.To != 16 {
		t.Errorf("07-16: %v %v", b, err)
	}
	for _, bad := range []string{"8-17", "08:00-17:00", "17-08", "08-08", "00-25", "ab-cd", "08-17x"} {
		if _, err := ParseBusinessHours(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for s, want := range map[string]int{"": 7, "1": 1, "14": 14, "30": 30} {
		if n, err := ParseTuningDays(s); err != nil || n != want {
			t.Errorf("days %q: %d %v", s, n, err)
		}
	}
	for _, bad := range []string{"0", "31", "-1", "x", "7.5"} {
		if _, err := ParseTuningDays(bad); err == nil {
			t.Errorf("days %q accepted", bad)
		}
	}
	for s, want := range map[string]int64{"": 0, "none": TuningChangeNone, "NONE": TuningChangeNone, "12": 12} {
		if n, err := ParseTuningChange(s); err != nil || n != want {
			t.Errorf("change %q: %d %v", s, n, err)
		}
	}
	for _, bad := range []string{"0", "-3", "latest", "1x"} {
		if _, err := ParseTuningChange(bad); err == nil {
			t.Errorf("change %q accepted", bad)
		}
	}
	got := WorkingDaysBefore(nov(2, 8, 30), 5) // Monday: the week before, Friday first
	if len(got) != 5 || !got[0].Equal(oct(30, 0, 0)) || !got[4].Equal(oct(26, 0, 0)) {
		t.Errorf("working days before Monday: %v", got)
	}
}

// Every hour from the first in the window to the one in progress; the in-progress hour and the
// change hour are marked and never compared.
func TestHoursCompletenessAndChangeHour(t *testing.T) {
	r := BuildTuningReport(tunFacts(nov(3, 14, 20), 7), []Change{tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")}, TuningOptions{Business: bh0817})
	if len(r.Hours) != 7*24+1 {
		t.Fatalf("%d hours, want %d", len(r.Hours), 7*24+1)
	}
	last := r.Hours[len(r.Hours)-1]
	if last.Hour != "2026-11-04T10:00:00" || !last.InProgress || last.Complete || last.Window != "" {
		t.Errorf("hour in progress: %+v", last)
	}
	prev := r.Hours[len(r.Hours)-2]
	if !prev.Complete || prev.InProgress || prev.Window != "now" {
		t.Errorf("09:00: %+v", prev)
	}
	for _, h := range r.Hours {
		if h.Hour == "2026-11-03T14:00:00" && (!h.Change || h.Window != "") {
			t.Errorf("change hour: %+v", h)
		}
		if h.Hour == "2026-10-31T11:00:00" && h.Class != HourWeekend {
			t.Errorf("Saturday: %+v", h)
		}
	}
	if r.CapturedAt != "2026-11-04T10:30:00" || r.BusinessHours != "08-17" || r.OldestHour == "" || !r.WaitsRead {
		t.Errorf("about: %s %s %s %v", r.CapturedAt, r.BusinessHours, r.OldestHour, r.WaitsRead)
	}
	if len(r.Changes) != 1 || r.Changes[0].At != "2026-11-03T14:20:00" || r.Changes[0].ByHand {
		t.Errorf("changes on the server's clock: %+v", r.Changes)
	}
}

// A change on a weekday afternoon: Now is the complete business hours after it, Before the business
// hours earlier the same day.
func TestWindowWeekdayAfternoon(t *testing.T) {
	r := BuildTuningReport(tunFacts(nov(3, 14, 20), 14), []Change{tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")}, TuningOptions{Business: bh0817})
	if got := strings.Join(hoursIn(r, "now"), ","); got != "11-03T15,11-03T16,11-04T08,11-04T09" {
		t.Errorf("now %s", got)
	}
	if got := strings.Join(hoursIn(r, "before"), ","); got != "11-03T08,11-03T09,11-03T10,11-03T11,11-03T12,11-03T13" {
		t.Errorf("before %s", got)
	}
	if r.Centre.ChangeID != 5 || !strings.Contains(r.Before.Label, "earlier the same day") || r.Now.Hours != 4 || r.Before.Hours != 6 {
		t.Errorf("centre %+v before %+v now %+v", r.Centre, r.Before, r.Now)
	}
	// The four tiles from sums, with their tones.
	cpu, ex, par, lock := tile(r, "cpu-per-execution"), tile(r, "executions-per-hour"), tile(r, "parallel-threads"), tile(r, "lock-threads")
	if val(cpu.Before) != 12 || val(cpu.Now) != 8 || val(cpu.Pct) != -33 || cpu.Tone != ToneBetter {
		t.Errorf("cpu tile %+v %v %v %v", cpu, val(cpu.Before), val(cpu.Now), val(cpu.Pct))
	}
	if val(ex.Before) != 1000 || val(ex.Now) != 1000 || val(ex.Pct) != 0 || ex.Tone != ToneSame {
		t.Errorf("executions tile %+v", ex)
	}
	if val(par.Before) != 19 || val(par.Now) != 1 || val(par.Pct) != -95 || par.Tone != ToneBetter || par.Words != "1 thread waiting on parallelism all the time" {
		t.Errorf("parallel tile %+v", par)
	}
	if val(lock.Before) != 0.5 || val(lock.Now) != 0.5 || lock.Tone != ToneSame {
		t.Errorf("lock tile %+v", lock)
	}
	want := "Since the statistics update on dbo.StockUnit on Tue 3 Nov 14:20, CPU per execution in business hours is down 33% (from 12.0 ms to 8.0 ms) over 4 hours; executions per hour are the same, so the work got cheaper, not rarer."
	if len(r.Explain) == 0 || r.Explain[0].Text != want {
		t.Errorf("headline:\n%v\nwant\n%s", r.Explain, want)
	}
	// The same hour before and after: Tuesday beside the five working days before.
	if len(r.SameHour) != 9 {
		t.Fatalf("same hour rows %d", len(r.SameHour))
	}
	h8, h14, h15 := r.SameHour[0], r.SameHour[6], r.SameHour[7]
	if h8.Hour != "08:00" || val(h8.Day.CPUPerExecMs) != 12 || val(h8.Previous.CPUPerExecMs) != 12 || h8.Previous.Days != 5 || val(h8.Previous.Executions) != 1000 {
		t.Errorf("08:00 %+v", h8)
	}
	if h14.Day.Note != "change" || val(h15.Day.CPUPerExecMs) != 8 || val(h15.Previous.CPUPerExecMs) != 12 {
		t.Errorf("14:00 %+v 15:00 %+v", h14, h15)
	}
	if !strings.Contains(r.Handover, "CPU per execution: 12.0 ms -> 8.0 ms (-33%, better)") || !strings.Contains(r.Handover, "What we see\n- Since the statistics") {
		t.Errorf("handover:\n%s", r.Handover)
	}
}

// A change on a Monday morning: nothing earlier the same day, so Before is the five working days
// before (the whole previous week with 14 days read).
func TestWindowMondayMorning(t *testing.T) {
	r := BuildTuningReport(tunFacts(nov(2, 8, 30), 14), []Change{tunChange(6, nov(2, 8, 30), "dbo.USP_PICKING", "PROCEDURE")}, TuningOptions{Business: bh0817})
	before := hoursIn(r, "before")
	if len(before) != 45 || before[0] != "10-26T08" || before[44] != "10-30T16" {
		t.Errorf("before %d %v", len(before), before)
	}
	if now := hoursIn(r, "now"); len(now) != 8+9+2 || now[0] != "11-02T09" {
		t.Errorf("now %d %v", len(now), now)
	}
	if !strings.Contains(r.Before.Label, "five working days before (Mon 26 Oct to Fri 30 Oct)") {
		t.Errorf("before label %q", r.Before.Label)
	}
	// With 7 days read, only the part of the week Query Store window holds.
	r7 := BuildTuningReport(tunFacts(nov(2, 8, 30), 7), []Change{tunChange(6, nov(2, 8, 30), "dbo.USP_PICKING", "PROCEDURE")}, TuningOptions{Business: bh0817})
	if b := hoursIn(r7, "before"); len(b) != 7+9+9 || b[0] != "10-28T10" {
		t.Errorf("7 days: before %d %v", len(b), b)
	}
}

// A change at a weekend: the five working days rule, and the same hour table on the next Monday.
func TestWindowWeekend(t *testing.T) {
	r := BuildTuningReport(tunFacts(oct(31, 11, 0), 14), []Change{tunChange(7, oct(31, 11, 0), "max server memory", "SETTING")}, TuningOptions{Business: bh0817})
	before, now := hoursIn(r, "before"), hoursIn(r, "now")
	if len(before) != 45 || before[0] != "10-26T08" || before[44] != "10-30T16" {
		t.Errorf("before %d %v", len(before), before)
	}
	if len(now) != 9+9+2 || now[0] != "11-02T08" {
		t.Errorf("now %d %v", len(now), now)
	}
	if r.SameHour[0].Day.CPUPerExecMs == nil || val(r.SameHour[0].Day.CPUPerExecMs) != 8 || val(r.SameHour[0].Previous.CPUPerExecMs) != 12 {
		t.Errorf("same hour on Monday: %+v", r.SameHour[0])
	}
	if !strings.HasPrefix(r.Explain[0].Text, "Since the change to max server memory on Sat 31 Oct 11:00, CPU per execution in business hours is down 33%") {
		t.Errorf("headline %q", r.Explain[0].Text)
	}
}

// Without a change: the latest complete business hour against the same hour on the five working
// days before, and "no change in the log" with Record a change.
func TestWindowWithoutChange(t *testing.T) {
	r := BuildTuningReport(tunFacts(time.Time{}, 14), nil, TuningOptions{Business: bh0817})
	if got := strings.Join(hoursIn(r, "now"), ","); got != "11-04T09" {
		t.Errorf("now %s", got)
	}
	if got := strings.Join(hoursIn(r, "before"), ","); got != "10-28T09,10-29T09,10-30T09,11-02T09,11-03T09" {
		t.Errorf("before %s", got)
	}
	if r.Centre.ChangeID != 0 || r.Centre.At != "2026-11-04T09:00:00" {
		t.Errorf("centre %+v", r.Centre)
	}
	if len(r.Explain) != 2 || !strings.HasPrefix(r.Explain[0].Text, "In the latest complete business hour (Wed 4 Nov 09:00), CPU per execution is about the same (12.0 ms before, 12.0 ms now)") ||
		!strings.HasPrefix(r.Explain[1].Text, "No change is in the log for the last 14 days") || !r.Explain[1].RecordChange {
		t.Errorf("sentences %+v", r.Explain)
	}
	// "none" with changes in the log: centred on the latest hour, no "no change" sentence, but the
	// change hour is still never compared.
	rn := BuildTuningReport(tunFacts(nov(3, 9, 10), 7), []Change{tunChange(5, nov(3, 9, 10), "dbo.StockUnit", "STATISTICS")},
		TuningOptions{ChangeID: TuningChangeNone, Business: bh0817})
	if got := strings.Join(hoursIn(rn, "before"), ","); got != "10-29T09,10-30T09,11-02T09" {
		t.Errorf("none: before %s", got)
	}
	for _, l := range rn.Explain {
		if l.RecordChange {
			t.Errorf("none with changes: %q", l.Text)
		}
	}
}

// Every ratio from sums: two hours of 1,000 runs at 10 ms and 10 runs at 100 ms are 10.9 ms per
// run, not the 55 ms an average of the hourly ratios would give.
func TestSumsNotAverages(t *testing.T) {
	f := tunFacts(time.Time{}, 14)
	for i := range f.Hours {
		h := &f.Hours[i]
		switch h.Hour {
		case nov(4, 9, 0):
			h.Executions, h.CPU = 1000, 1000*10*1000
		case nov(3, 9, 0):
			h.Executions, h.CPU = 10, 10*100*1000
		case nov(2, 9, 0), oct(30, 9, 0), oct(29, 9, 0), oct(28, 9, 0):
			h.Executions, h.CPU = 1000, 1000*10*1000
		}
	}
	r := BuildTuningReport(f, nil, TuningOptions{Business: bh0817})
	cpu := tile(r, "cpu-per-execution")
	// Before: four hours of 1,000 at 10 ms and one of 10 at 100 ms: 41,000 ms / 4,010 runs.
	if val(cpu.Before) != 10.22 || val(cpu.Now) != 10 {
		t.Errorf("cpu tile %v -> %v", val(cpu.Before), val(cpu.Now))
	}
	if ex := tile(r, "executions-per-hour"); val(ex.Before) != 802 || val(ex.Now) != 1000 || ex.Tone != ToneMore {
		t.Errorf("executions %v", val(ex.Before))
	}
}

func TestTones(t *testing.T) {
	for _, c := range []struct {
		pct   float64
		lower bool
		want  string
	}{
		{-10, true, ToneBetter}, {-9.9, true, ToneSame}, {9.9, true, ToneSame}, {10, true, ToneWorse}, {50, true, ToneWorse},
		{10, false, ToneMore}, {-10, false, ToneFewer}, {5, false, ToneSame},
	} {
		if got := tTone(c.pct, c.lower); got != c.want {
			t.Errorf("tone %v lower=%v: %s, want %s", c.pct, c.lower, got, c.want)
		}
	}
	// Worse: 8 ms before the change, 12 ms after.
	f := tunFacts(time.Time{}, 7)
	for i := range f.Hours {
		h := &f.Hours[i]
		if bh0817.Class(h.Hour) == HourBusiness {
			ms := 8.0
			if !h.Hour.Before(nov(3, 14, 0)) {
				ms = 12
			}
			h.CPU = int64(float64(h.Executions) * ms * 1000)
		}
	}
	r := BuildTuningReport(f, []Change{tunChange(5, nov(3, 14, 20), "dbo.USP_PICKING", "PROCEDURE")}, TuningOptions{Business: bh0817})
	if cpu := tile(r, "cpu-per-execution"); cpu.Tone != ToneWorse || val(cpu.Pct) != 50 {
		t.Errorf("worse tile %+v", cpu)
	}
	if !strings.Contains(r.Explain[0].Text, "is up 50% (from 8.0 ms to 12.0 ms) over 4 hours; executions per hour are the same, so each run costs more than before.") {
		t.Errorf("worse headline %q", r.Explain[0].Text)
	}
	// No waits recorded: the threads tiles say so and have no values.
	f2 := tunFacts(time.Time{}, 7)
	f2.WaitCapture = ""
	f2.WaitsError = "Query Store keeps waits from SQL Server 2017 on."
	r2 := BuildTuningReport(f2, nil, TuningOptions{Business: bh0817})
	if par := tile(r2, "parallel-threads"); par.Before != nil || par.Now != nil || !strings.Contains(par.Words, "no waits") {
		t.Errorf("no waits %+v", par)
	}
	if len(r2.Notes) != 1 || !strings.HasPrefix(r2.Notes[0], "Waits: could not be read: Query Store keeps waits from SQL Server 2017 on.") {
		t.Errorf("notes %v", r2.Notes)
	}
}

func TestVerdicts(t *testing.T) {
	for _, c := range []struct {
		rb, rn int64
		b, n   float64
		want   string
	}{
		{100, 100, 10, 8, VerdictFaster}, {100, 100, 10, 8.01, VerdictSame}, {100, 100, 10, 12.5, VerdictSlower},
		{100, 100, 10, 12.49, VerdictSame}, {0, 20, 0, 5, VerdictNew}, {0, 19, 0, 5, VerdictTooFew}, {20, 0, 5, 0, VerdictGone},
		{19, 0, 5, 0, VerdictTooFew}, {19, 100, 10, 1, VerdictTooFew}, {100, 19, 10, 1, VerdictTooFew}, {20, 20, 10, 5, VerdictFaster},
	} {
		if got := TuningVerdict(c.rb, c.rn, c.b, c.n); got != c.want {
			t.Errorf("verdict %d/%d %v->%v: %s, want %s", c.rb, c.rn, c.b, c.n, got, c.want)
		}
	}
}

func TestPlanRegressionThresholds(t *testing.T) {
	for _, c := range []struct {
		old, new float64
		runs     int64
		want     bool
	}{
		{1, 1.5, 50, true}, {1, 1.49, 50, false}, {0.99, 2, 50, false}, {1, 1.5, 49, false}, {4, 10, 300, true},
	} {
		if got := PlanRegressed(c.old, c.new, c.runs); got != c.want {
			t.Errorf("regressed %v -> %v (%d runs): %v", c.old, c.new, c.runs, got)
		}
	}
}

// The fixture with the change of 3 Nov at 14:20 and its queries: the picking procedure got faster
// but one of its queries got a new plan 40 minutes after the change; an ad hoc query is new; the
// audit runs too rarely to judge and started failing after the change.
func tunedFacts() TuningFacts {
	f := tunFacts(nov(3, 14, 20), 7)
	f.Shapes = []TuningShapeFact{
		{Rank: 1, QueryHash: "0x0000000000000001", ObjectName: "dbo.USP_PICKING", Text: "SELECT TOP (1) su.ID FROM dbo.StockUnit su WHERE su.Barcode = @code"},
		{Rank: 2, QueryHash: "0x0000000000000004", ObjectName: "dbo.USP_SLOW", Text: "UPDATE dbo.Location SET Qty = @q"},
		{Rank: 3, QueryHash: "0x0000000000000002", Text: "SELECT COUNT(*) FROM dbo.Movement WHERE CreatedDate > @d"},
		{Rank: 4, QueryHash: "0x0000000000000003", ObjectName: "dbo.USP_AUDIT", Text: "INSERT dbo.Audit (...) VALUES (...)"},
	}
	for _, h := range f.Hours {
		if bh0817.Class(h.Hour) != HourBusiness {
			continue
		}
		after := !h.Hour.Before(nov(3, 14, 0))
		pick, slow := 20.0, 4.0
		if after {
			pick, slow = 8, 6
			f.ShapeHours = append(f.ShapeHours, TuningShapeHourFact{QueryHash: "0x0000000000000002", Hour: h.Hour, Executions: 30, CPU: 30 * 5 * 1000})
		}
		f.ShapeHours = append(f.ShapeHours,
			TuningShapeHourFact{QueryHash: "0x0000000000000001", Hour: h.Hour, Executions: 400, CPU: int64(400 * pick * 1000)},
			TuningShapeHourFact{QueryHash: "0x0000000000000004", Hour: h.Hour, Executions: 100, CPU: int64(100 * slow * 1000)},
			TuningShapeHourFact{QueryHash: "0x0000000000000003", Hour: h.Hour, Executions: 3, CPU: 3 * 50 * 1000})
	}
	f.Regressions = []PlanRegressionFact{
		{QueryID: 1873, QueryHash: "0x0000000000000001", ObjectName: "dbo.USP_PICKING", Text: "SELECT TOP (1) su.ID FROM dbo.StockUnit su",
			OldPlanID: 7, OldCPUMs: 5, OldRuns: 4000, NewPlanID: 9, NewCPUMs: 10.5, NewRuns: 300, NewFirstSeen: nov(3, 15, 0)},
		{QueryID: 2001, QueryHash: "0x0000000000000004", ObjectName: "dbo.USP_SLOW", OldPlanID: 11, OldCPUMs: 4, OldRuns: 100,
			NewPlanID: 12, NewCPUMs: 5.6, NewRuns: 900, NewFirstSeen: oct(29, 10, 0)}, // 1.4 x: under the rule
		{QueryID: 2002, QueryHash: "0x0000000000000005", ObjectName: "dbo.USP_OLD", OldPlanID: 21, OldCPUMs: 2, OldRuns: 100,
			NewPlanID: 22, NewCPUMs: 6, NewRuns: 90, NewFirstSeen: oct(29, 10, 0)},
	}
	first, last := nov(3, 15, 0), nov(4, 9, 45)
	early, earlyLast := oct(29, 11, 0), oct(30, 11, 0)
	f.Failures = []QueryFailureFact{
		{QueryID: 3001, QueryHash: "0x0000000000000003", ObjectName: "dbo.USP_AUDIT", Aborted: 12, Exceptions: 3, FirstFailed: &first, LastFailed: &last},
		{QueryID: 3002, QueryHash: "0x0000000000000006", ObjectName: "dbo.USP_REPORT", Aborted: 2, FirstFailed: &early, LastFailed: &earlyLast},
	}
	return f
}

func TestQueriesRegressionsFailuresAndSentences(t *testing.T) {
	r := BuildTuningReport(tunedFacts(), []Change{tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")}, TuningOptions{Business: bh0817, CanChange: true})

	// Per query: sorted by the CPU saved or added, too few last.
	order := []string{}
	for _, q := range r.Queries {
		order = append(order, q.Name+"="+q.Verdict)
	}
	want := `dbo.USP_PICKING=faster,dbo.USP_SLOW=slower,the ad hoc query "SELECT COUNT(*) FROM dbo.Movement WHERE CreatedDate > @d"=new,dbo.USP_AUDIT=too few`
	if got := strings.Join(order, ","); got != want {
		t.Errorf("queries\n%s\nwant\n%s", got, want)
	}
	p := r.Queries[0]
	if val(p.CPUBeforeMs) != 20 || val(p.CPUNowMs) != 8 || p.RunsBefore != 2400 || p.RunsNow != 1600 || p.ImpactMsPerHour != -4800 || p.ExecsPerHourNow != 400 {
		t.Errorf("picking %+v", p)
	}
	if a := r.Queries[3]; a.RunsBefore != 18 || a.RunsNow != 12 {
		t.Errorf("audit %+v", a)
	}

	// Plan regressions: the toolkit's rule; the new one 40 minutes after the change with force-plan.
	if len(r.Regressions) != 2 || r.Regressions[0].NewPlanID != 9 || r.Regressions[1].NewPlanID != 22 {
		t.Fatalf("regressions %+v", r.Regressions)
	}
	g := r.Regressions[0]
	if g.Relation != "40 minutes after the change" || !g.AfterChange || g.Ratio != 2.1 || g.Action == nil || g.Action.FixID != "force-plan" ||
		g.Action.Options["planId"] != "7" {
		t.Errorf("regression %+v", g)
	}
	if r.Regressions[1].Relation != "5 days before the change" || r.Regressions[1].AfterChange {
		t.Errorf("old regression %+v", r.Regressions[1])
	}
	if r.Failures[0].Relation != "40 minutes after the change" || !r.Failures[0].AfterChange || r.Failures[1].AfterChange {
		t.Errorf("failures %+v", r.Failures)
	}

	texts := []string{}
	for _, l := range r.Explain {
		texts = append(texts, l.Text)
	}
	wantTexts := []string{
		"Since the statistics update on dbo.StockUnit on Tue 3 Nov 14:20, CPU per execution in business hours is down 33% (from 12.0 ms to 8.0 ms) over 4 hours; executions per hour are the same, so the work got cheaper, not rarer.",
		"dbo.USP_PICKING got faster since the change: 20.0 ms of CPU per run before, 8.0 ms now, over 1,600 runs.",
		"dbo.USP_SLOW got slower since the change: 4.0 ms of CPU per run before, 6.0 ms now, over 400 runs. Investigate it.",
		"The new plan of dbo.USP_PICKING uses 2.1 x the CPU per run of the one before and appeared 40 minutes after the change: force the old plan, or revert.",
		"dbo.USP_AUDIT has failed 15 times since the change (12 timed out or were cancelled, 3 ended in an error), first at Tue 3 Nov 15:00.",
	}
	if strings.Join(texts, "\n") != strings.Join(wantTexts, "\n") {
		t.Errorf("sentences\n%s\nwant\n%s", strings.Join(texts, "\n"), strings.Join(wantTexts, "\n"))
	}
	if l := r.Explain[3]; l.Action == nil || l.Action.FixID != "force-plan" || l.QueryHash != "0x0000000000000001" ||
		!strings.Contains(l.Copy, "Query hash: 0x0000000000000001") {
		t.Errorf("regression line %+v", l)
	}
	if l := r.Explain[1]; l.QueryHash == "" || !strings.HasPrefix(l.Copy, "dbo.USP_PICKING in WarehouseLive\nQuery hash: 0x0000000000000001\n\ndbo.USP_PICKING got faster") {
		t.Errorf("faster line %+v", l)
	}
	for _, s := range []string{"What is expensive (CPU per run before -> now; runs before -> now; verdict)",
		"- dbo.USP_PICKING (0x0000000000000001): 20.0 ms -> 8.0 ms; 2,400 -> 1,600 runs; faster", "Plan regressions", "Failed executions"} {
		if !strings.Contains(r.Handover, s) {
			t.Errorf("handover lacks %q:\n%s", s, r.Handover)
		}
	}

	// Without the right to change, no force-plan.
	ro := BuildTuningReport(tunedFacts(), []Change{tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS")}, TuningOptions{Business: bh0817})
	if ro.Regressions[0].Action != nil {
		t.Errorf("read-only database offers force-plan")
	}
}

// "Too early to say" when Now holds fewer than two complete business hours, and when to look again;
// "Query Store is off" with its fix.
func TestTooEarlyAndQueryStoreOff(t *testing.T) {
	f := tunFacts(nov(4, 8, 10), 7)
	f.QueryStoreState = "OFF"
	r := BuildTuningReport(f, []Change{tunChange(8, nov(4, 8, 10), "IX_StockUnit_Barcode", "INDEX")}, TuningOptions{Business: bh0817, CanChange: true})
	texts := []string{}
	for _, l := range r.Explain {
		texts = append(texts, l.Text)
	}
	want := []string{
		"Since the index change on IX_StockUnit_Barcode on Wed 4 Nov 08:10, CPU per execution in business hours is down 33% (from 12.0 ms to 8.0 ms) over 1 hour; executions per hour are the same, so the work got cheaper, not rarer.",
		"It is too early to say: only one complete business hour has passed since the change. Look again after Wed 4 Nov 11:00.",
		"Query Store is off for WarehouseLive, so only the history from when it was last on is here and nothing new is recorded. Turn it on before the next change to measure it.",
	}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Errorf("sentences\n%s\nwant\n%s", strings.Join(texts, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(r.Before.Label, "five working days before") {
		t.Errorf("before %q", r.Before.Label)
	}
	if a := r.Explain[2].Action; a == nil || a.FixID != FixQueryStore {
		t.Errorf("query store action %+v", a)
	}
	// A change in the last minutes: no hour yet.
	f2 := tunFacts(nov(4, 10, 5), 7)
	r2 := BuildTuningReport(f2, []Change{tunChange(9, nov(4, 10, 5), "dbo.USP_PICKING", "PROCEDURE")}, TuningOptions{Business: bh0817})
	if len(r2.Explain) != 1 || r2.Explain[0].Text != "It is too early to say: there is no complete business hour since the change yet. Look again after Wed 4 Nov 13:00." {
		t.Errorf("no hour yet %+v", r2.Explain)
	}
	// An older change chosen: Now stops at the next change.
	r3 := BuildTuningReport(tunFacts(nov(3, 14, 20), 7), []Change{tunChange(5, nov(3, 14, 20), "dbo.StockUnit", "STATISTICS"),
		tunChange(4, nov(2, 10, 0), "dbo.USP_PICKING", "PROCEDURE")}, TuningOptions{ChangeID: 4, Business: bh0817})
	if now := hoursIn(r3, "now"); len(now) != 6+6 || now[len(now)-1] != "11-03T13" || !strings.HasSuffix(r3.Now.Label, "up to the next change") {
		t.Errorf("older change: now %v %q", now, r3.Now.Label)
	}
}

func TestParseRecordChange(t *testing.T) {
	ok := RecordChangeInput{Object: " dbo.USP_PICKING ", ChangeType: "procedure", Description: "Added OPTION (RECOMPILE)"}
	r, err := ParseRecordChange(ok)
	if err != nil || r.Object != "dbo.USP_PICKING" || r.ChangeType != "PROCEDURE" || !r.At.IsZero() {
		t.Errorf("now: %+v %v", r, err)
	}
	ok.ChangedAt = "2026-11-03T14:20"
	if r, err = ParseRecordChange(ok); err != nil || r.Exact || !r.At.Equal(nov(3, 14, 20)) {
		t.Errorf("server time: %+v %v", r, err)
	}
	ok.ChangedAt = "2026-11-03T14:20:00+02:00"
	if r, err = ParseRecordChange(ok); err != nil || !r.Exact || !r.At.Equal(time.Date(2026, 11, 3, 12, 20, 0, 0, time.UTC)) {
		t.Errorf("exact: %+v %v", r, err)
	}
	c := r.Change(3, "WarehouseLive", r.At)
	if c.Result != ChangeRecordedByHand || c.Statement != "" || c.ServerID != 3 || c.Database != "WarehouseLive" {
		t.Errorf("row %+v", c)
	}
	for _, bad := range []RecordChangeInput{
		{Object: "", ChangeType: "INDEX", Description: "d"},
		{Object: strings.Repeat("x", 201), ChangeType: "INDEX", Description: "d"},
		{Object: "x", ChangeType: "TABLE", Description: "d"},
		{Object: "x", ChangeType: "INDEX", Description: " "},
		{Object: "x", ChangeType: "INDEX", Description: strings.Repeat("d", 1001)},
		{Object: "x", ChangeType: "INDEX", Description: "d", ChangedAt: "yesterday"},
		{Object: "x", ChangeType: "INDEX", Description: "d", ChangedAt: "03/11/2026 14:20"},
	} {
		if _, err := ParseRecordChange(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// A hand-recorded change is marked so in the report.
	rep := BuildTuningReport(tunFacts(nov(3, 14, 20), 7), []Change{c}, TuningOptions{Business: bh0817})
	if len(rep.Changes) != 1 || !rep.Changes[0].ByHand || rep.Changes[0].At != "2026-11-03T14:20:00" {
		t.Errorf("by hand %+v", rep.Changes)
	}
}
