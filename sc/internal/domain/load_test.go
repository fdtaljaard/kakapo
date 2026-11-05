package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

// loadNow is the fixture's server clock: Thursday 5 Nov 2026, 11:40.
var loadNow = time.Date(2026, 11, 5, 11, 40, 0, 0, time.UTC)

func lday(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// fullDay is 24 hours of a day: queries an hour, and CPU so that hour h runs cores(h) cores busy.
func fullDay(d time.Time, queries int64, cores func(h int) float64) []LoadHourFact {
	out := []LoadHourFact{}
	for h := 0; h < 24; h++ {
		out = append(out, LoadHourFact{Hour: d.Add(time.Duration(h) * time.Hour), Executions: queries, CPU: int64(cores(h) * 3_600_000_000),
			WaitSec: 3600 * 2})
	}
	return out
}

func flat(c float64) func(int) float64 { return func(int) float64 { return c } }

// oldSchool is OldSchool's shape (measured 2026-11-05): Query Store from Fri 9 Oct 10:00 to Fri 16 Oct
// 16:00, a gap, then from Fri 30 Oct 08:00 to now; 15 Oct ran a quarter of a normal day (a normal night,
// then about 600 queries an hour) and 16 Oct almost nothing; Wed 4 Nov ran nine tenths.
func oldSchool() LoadFacts {
	hours := []LoadHourFact{}
	add := func(hs []LoadHourFact) { hours = append(hours, hs...) }
	add(fullDay(lday(2026, 10, 9), 130_000, flat(0.5))[10:])
	add(fullDay(lday(2026, 10, 10), 110_000, flat(0.2)))
	add(fullDay(lday(2026, 10, 11), 110_000, flat(0.2)))
	add(fullDay(lday(2026, 10, 12), 132_000, flat(0.6)))
	add(fullDay(lday(2026, 10, 13), 136_000, flat(0.7)))
	add(fullDay(lday(2026, 10, 14), 132_000, flat(0.4)))
	d15 := fullDay(lday(2026, 10, 15), 600, flat(0.1))
	for h := 0; h < 7; h++ {
		d15[h].Executions = 106_000
	}
	add(d15)
	add(fullDay(lday(2026, 10, 16), 350, flat(0.05))[:16])
	add(fullDay(lday(2026, 10, 30), 129_000, flat(0.8))[8:])
	add(fullDay(lday(2026, 10, 31), 109_000, flat(0.3)))
	add(fullDay(lday(2026, 11, 1), 106_000, flat(0.2)))
	add(fullDay(lday(2026, 11, 2), 133_000, func(h int) float64 { return 0.6 + float64(h)/100 }))
	add(fullDay(lday(2026, 11, 3), 141_000, flat(1.2)))
	add(fullDay(lday(2026, 11, 4), 120_000, flat(0.9)))
	today := fullDay(lday(2026, 11, 5), 110_000, flat(0.1))[:12]
	today[9].CPU, today[10].CPU = 158_400_000, 4_000_000_000 // 0.044 and 1.11 cores busy
	today[11].CPU, today[11].Executions = 600_000_000, 2_000 // 40 minutes so far: 0.25 cores, 3,000 an hour
	add(today)
	oldest, newest := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC), loadNow.Add(5*time.Minute)
	nowStart := time.Date(2026, 11, 5, 11, 15, 0, 0, time.UTC)
	return LoadFacts{Database: "OldSchool", ServerNow: loadNow, UTCOffsetMinutes: 120, QueryStoreState: "READ_WRITE", IntervalMinutes: 15,
		OldestInterval: &oldest, NewestInterval: &newest, CPUCount: 20, Hours: hours,
		NowStart: &nowStart, NowExecutions: 1_250, NowCPU: 750_000_000, NowWaitSec: 1_500}
}

func TestParseLoadDays(t *testing.T) {
	for s, want := range map[string]int{"": 7, "7": 7, " 14 ": 14, "30": 30} {
		if got, err := ParseLoadDays(s); err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"0", "1", "8", "31", "week", "-7"} {
		if _, err := ParseLoadDays(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestDayKindAndFrom(t *testing.T) {
	if DayKind(loadNow) != DayWorking || DayKind(lday(2026, 11, 7)) != DayWeekend || DayKind(lday(2026, 11, 8)) != DayWeekend ||
		DayKind(lday(2026, 11, 9)) != DayWorking {
		t.Error("day kinds")
	}
	if got := LoadFrom(loadNow, 7); !got.Equal(lday(2026, 10, 29)) {
		t.Errorf("from %v", got)
	}
}

func dates(ts []time.Time) string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.Format("2 Jan"))
	}
	return strings.Join(out, ",")
}

// The days compared: today's kind only, inside the selected days, the hours each holds; a gap in Query
// Store holds no day; 30 Oct counts though Query Store started at 08:00; barely used days are left out
// under a quarter of the median day's queries per hour, and 4 Nov (nine tenths) is kept.
func TestLoadBaselineDays(t *testing.T) {
	f := oldSchool()
	for _, c := range []struct {
		days              int
		compared, leftOut string
	}{
		{7, "30 Oct,2 Nov,3 Nov,4 Nov", ""},
		{14, "30 Oct,2 Nov,3 Nov,4 Nov", ""}, // 22 to 28 Oct are in the gap
		{30, "9 Oct,12 Oct,13 Oct,14 Oct,30 Oct,2 Nov,3 Nov,4 Nov", "15 Oct,16 Oct"},
	} {
		b := LoadBaseline(f, c.days)
		if dates(b.Compared) != c.compared || dates(b.LeftOut) != c.leftOut || b.Kind != DayWorking {
			t.Errorf("%d days: compared %s, left out %s", c.days, dates(b.Compared), dates(b.LeftOut))
		}
		if !b.For(loadNow.Add(time.Hour), c.days) || b.For(loadNow.Add(13*time.Hour), c.days) || b.For(loadNow, 99) {
			t.Errorf("%d days: For", c.days)
		}
	}
	// A Saturday compares with the weekend days only.
	sat := f
	sat.ServerNow = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	if b := LoadBaseline(sat, 30); b.Kind != DayWeekend || dates(b.Compared) != "10 Oct,11 Oct,31 Oct,1 Nov" {
		t.Errorf("Saturday: %s %s", b.Kind, dates(b.Compared))
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

// The range, the median and "above" per hour; cores busy for a complete hour and for the hour in
// progress; now from the two latest intervals; the key.
func TestBuildLoad(t *testing.T) {
	f := oldSchool()
	l := BuildLoad(f, LoadBaseline(f, 7), LoadOptions{CanChange: true})
	if len(l.Hours) != 24 || l.Kind != DayWorking || l.DaysCompared != 4 || l.DaysLeftOut != 0 || !l.Chart || l.Message != "" || l.Action != nil {
		t.Fatalf("load %+v", l)
	}
	// 10:00: 30 Oct 0.8, 2 Nov 0.7, 3 Nov 1.2, 4 Nov 0.9 cores; median 0.85. Today 1.11: above.
	h10 := l.Hours[10]
	if !near(*h10.Cores.Low, 0.7) || !near(*h10.Cores.High, 1.2) || !near(*h10.Cores.Median, 0.85) || !near(*h10.Cores.Value, 1.111) || h10.Above ||
		h10.InProgress || h10.Future {
		t.Errorf("10:00 %+v", h10.Cores)
	}
	if !near(*h10.Queries.Low, 120_000) || !near(*h10.Queries.High, 141_000) || !near(*h10.Queries.Median, 131_000) || !near(*h10.Threads.Median, 2) {
		t.Errorf("10:00 queries %+v threads %+v", h10.Queries, h10.Threads)
	}
	// 09:00: 0.044 cores, under the band.
	if h := l.Hours[9]; !near(*h.Cores.Value, 0.044) || h.Above {
		t.Errorf("09:00 %+v", h.Cores)
	}
	// 11:00 in progress: 0.6e9 µs over 40 minutes = 0.25 cores; 2,000 queries in 40 minutes = 3,000 an hour.
	if h := l.Hours[11]; !h.InProgress || h.Future || !near(*h.Cores.Value, 0.25) || *h.Queries.Value != 3000 {
		t.Errorf("11:00 %+v %+v", h, h.Cores)
	}
	// Hours to come: the band only.
	if h := l.Hours[12]; !h.Future || h.Cores.Value != nil || h.Cores.High == nil {
		t.Errorf("12:00 %+v", h)
	}
	// Above: 3 Nov's hour 08:00 with today at 1.5 cores.
	g := oldSchool()
	g.Hours[len(g.Hours)-4].CPU = 5_400_000_000
	if h := BuildLoad(g, LoadBaseline(g, 7), LoadOptions{}).Hours[8]; !h.Above {
		t.Errorf("08:00 at 1.5 cores is not above %+v", h.Cores)
	}
	// Now: 11:15 to 11:40, 25 minutes: 0.75e9 µs = 0.5 cores; 1,250 runs = 3,000 an hour; 1,500 s = 1 thread.
	n := l.Now
	if !near(*n.Cores.Value, 0.5) || *n.Queries.Value != 3000 || !near(*n.Threads.Value, 1) || n.Since != "2026-11-05T11:15:00" || n.ShowSince ||
		n.CPUCount != 20 || n.Cores.High == nil || !near(*n.Cores.High, 1.2) {
		t.Errorf("now %+v cores %+v", n, n.Cores)
	}
	if l.Key != "range of the last 4 working days at that hour" {
		t.Errorf("key %q", l.Key)
	}
	if k := BuildLoad(f, LoadBaseline(f, 30), LoadOptions{}).Key; k != "range of the last 8 working days at that hour (2 quiet or barely used, left out)" {
		t.Errorf("30 days key %q", k)
	}
}

// Now from 60-minute intervals: the hour before and the hour in progress, and the line says since.
func TestLoadNowHourIntervals(t *testing.T) {
	f := oldSchool()
	f.IntervalMinutes = 60
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	f.NowStart, f.NowCPU, f.NowExecutions = &start, 3_600_000_000, 100_000 // 100 minutes
	n := BuildLoad(f, LoadBaseline(f, 7), LoadOptions{}).Now
	if !near(*n.Cores.Value, 0.6) || *n.Queries.Value != 60_000 || !n.ShowSince || n.Since != "2026-11-05T10:00:00" {
		t.Errorf("now %+v %+v", n, n.Cores)
	}
}

// Fewer than two days of today's kind: no band, the bars only, and the key says so.
func TestLoadTooFewDays(t *testing.T) {
	f := oldSchool()
	f.Hours = append(fullDay(lday(2026, 11, 4), 120_000, flat(0.9)), fullDay(lday(2026, 11, 5), 110_000, flat(0.1))[:12]...)
	l := BuildLoad(f, LoadBaseline(f, 7), LoadOptions{})
	if l.DaysCompared != 1 || l.Hours[10].Cores.High != nil || l.Hours[10].Cores.Value == nil || l.Now.Cores.High != nil ||
		l.Key != "Query Store holds 1 day like today here; the usual range needs 2." {
		t.Errorf("too few: %d %+v %q", l.DaysCompared, l.Hours[10].Cores, l.Key)
	}
}

// Query Store off: the line says so with the fix; read only: the hours it holds and since when nothing
// new was recorded; 2016: no threads waiting.
func TestLoadQueryStoreStates(t *testing.T) {
	f := oldSchool()
	f.QueryStoreState = "OFF"
	l := BuildLoad(f, LoadBaseline(f, 7), LoadOptions{CanChange: true})
	if l.Message != "Query Store is off, so kakapo cannot show the load." || l.Chart || l.Action == nil || l.Action.FixID != FixQueryStore {
		t.Errorf("off: %+v", l)
	}
	if l := BuildLoad(f, LoadBaseline(f, 7), LoadOptions{}); l.Action != nil {
		t.Error("off and not changeable: an action")
	}

	ro := oldSchool()
	ro.QueryStoreState = "READ_ONLY"
	newest := time.Date(2026, 11, 5, 9, 45, 0, 0, time.UTC)
	ro.NewestInterval = &newest
	ro.Hours = ro.Hours[:len(ro.Hours)-2] // nothing after 09:45
	l = BuildLoad(ro, LoadBaseline(ro, 7), LoadOptions{CanChange: true})
	if !l.Chart || l.Message != "" || l.Action == nil || l.Now.Cores.Value != nil || l.Hours[9].Cores.Value == nil || l.Hours[10].Cores.Value != nil ||
		!strings.HasSuffix(l.Key, ". Query Store is read-only: nothing new recorded since Thu 5 Nov 09:45.") {
		t.Errorf("read only: %+v %q", l.Now.Cores, l.Key)
	}

	old := oldSchool()
	old.WaitsError = "Query Store keeps waits from SQL Server 2017 on."
	l = BuildLoad(old, LoadBaseline(old, 7), LoadOptions{})
	if l.Now.Threads != nil || l.Hours[10].Threads != nil || len(l.Notes) != 1 || !strings.Contains(l.Notes[0], "2017") {
		t.Errorf("2016: %+v %v", l.Now.Threads, l.Notes)
	}
}

func TestCoresBusy(t *testing.T) {
	if !near(CoresBusy(3_600_000_000, time.Hour), 1) || !near(CoresBusy(900_000_000, 30*time.Minute), 0.5) || CoresBusy(1, 0) != 0 {
		t.Error("cores busy")
	}
}
