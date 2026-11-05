package domain

import (
	"strings"
	"testing"
	"time"
)

// trendFacts: 8 days of hours to Thu 5 Nov 2026 18:50. Working days 150,000 queries an hour in business
// hours at 22 ms of CPU each and 7 threads waiting, 100,000 at 5 ms at night; weekends 100,000 at 5 ms;
// Wed 4 Nov a holiday (100,000 at 6 ms by day, 60,000 at 2 ms by night); today 180,000 at 6 ms and half a
// thread waiting.
func trendFacts() PerformanceFacts {
	now := time.Date(2026, 11, 5, 18, 50, 0, 0, time.UTC)
	l := LoadFacts{Database: "Sandbox", ServerNow: now}
	for d := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC); !d.After(now); d = d.Add(time.Hour) {
		if !d.Before(now.Truncate(time.Hour)) {
			break
		}
		h := d.Hour()
		day := h >= 8 && h < 17
		execs, cpu, wait := int64(100000), int64(5000), 0.5*3600
		switch {
		case DayKind(d) == DayWeekend:
		case d.YearDay() == 308: // Wed 4 Nov
			execs, cpu = 60000, 2000
			if day {
				execs, cpu = 100000, 6000
			}
		case d.YearDay() == 309 && day: // today
			execs, cpu = 180000, 6000
		case day:
			execs, cpu, wait = 150000, 22000, 7*3600
		}
		l.Hours = append(l.Hours, LoadHourFact{Hour: d, Executions: execs, CPU: execs * cpu, WaitSec: wait})
	}
	return PerformanceFacts{Database: "Sandbox", ServerNow: now, UTCOffsetMinutes: 120, Load: &l,
		Movers: []PlanMoverFact{
			// Slower since 3 Nov 15:59 UTC (17:59 on the server), 2 minutes after its procedure changed.
			{QueryHash: "0x01", Since: time.Date(2026, 11, 3, 15, 59, 0, 0, time.UTC), Minutes: 7 * 1440, BeforeRuns: 5000, BeforeCPU: 5000 * 49000,
				NowRuns: 2000, NowCPU: 2000 * 353000, ObjectName: "dbo.PickSequence", Text: "MERGE OptionalFieldValues AS T USING ..."},
			// Faster since 4 Nov 13:34 UTC; it reads a view that references the table changed at 15:00 server time.
			{QueryHash: "0x02", Since: time.Date(2026, 11, 4, 13, 34, 0, 0, time.UTC), Minutes: 7 * 1440, BeforeRuns: 255, BeforeCPU: 255 * 95000000,
				NowRuns: 88, NowCPU: 88 * 10000, Text: "SELECT * FROM API_QueryTracking ORDER BY Barcode"},
			// The same cost: not a mover.
			{QueryHash: "0x03", Since: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), Minutes: 7 * 1440, BeforeRuns: 100, BeforeCPU: 100 * 1000,
				NowRuns: 100, NowCPU: 100 * 1050, Text: "SELECT 1 FROM DocumentDetail"},
		},
		Changed: []ChangedObjectFact{
			{Schema: "dbo", Name: "PickSequence", Type: "P", Created: time.Date(2026, 2, 18, 9, 0, 0, 0, time.UTC), Modified: time.Date(2026, 11, 3, 17, 54, 0, 0, time.UTC)},
			{Schema: "dbo", Name: "ItemAlias", Type: "U", Created: time.Date(2025, 9, 16, 10, 0, 0, 0, time.UTC), Modified: time.Date(2026, 11, 4, 15, 0, 9, 0, time.UTC),
				Referenced: []string{"API_QueryTracking", "Label_Item"}},
			// A table whose name is only part of a word in the text: not a match.
			{Schema: "dbo", Name: "Document", Type: "U", Created: time.Date(2025, 9, 16, 10, 0, 0, 0, time.UTC), Modified: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		},
	}
}

func TestPerformanceTrend(t *testing.T) {
	tr := BuildPerformanceTrend(trendFacts())
	if tr == nil {
		t.Fatal("no trend")
	}
	if tr.Verdict != TrendBetter || tr.Words != "Better" || tr.Day != "today" ||
		tr.Sentence != "As busy as usual today, with 73% less CPU per query and 93% less waiting." {
		t.Errorf("verdict %q %q %q", tr.Verdict, tr.Day, tr.Sentence)
	}
	if tr.Compared != "Today 08:00 to 17:00 against the same hours of Thu 29, Fri 30 Oct, Mon 2 and Tue 3 Nov. Wed 4 Nov is left out: a quiet day (67% of the queries, 18% of the CPU)." {
		t.Errorf("compared %q", tr.Compared)
	}
	got := []string{}
	for _, f := range tr.Figures {
		got = append(got, f.Label+"="+f.Value+" "+f.Change+" ("+f.Usual+") "+f.Tone)
	}
	want := []string{
		"Work asked (queries an hour)=180,000 +20%: as usual (usual 150,000 to 150,000) same",
		"CPU per query=6.0 ms −73%: cheaper (usual 22.0 ms to 22.0 ms) better",
		"Queries waiting (threads)=0.5 −93%: less waiting (usual 7.0 to 7.0) better",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("figures:\n%s", strings.Join(got, "\n"))
	}
	kinds := []string{}
	for _, d := range tr.Days {
		kinds = append(kinds, d.Label+":"+d.Kind)
	}
	if strings.Join(kinds, " ") != "Wed 29:working Thu 29:working Fri 30:working Sat 31:weekend Sun 1:weekend Mon 2:working Tue 3:working Wed 4:quiet Thu 5:judged" &&
		strings.Join(kinds, " ") != "Thu 29:working Fri 30:working Sat 31:weekend Sun 1:weekend Mon 2:working Tue 3:working Wed 4:quiet Thu 5:judged" {
		t.Errorf("days %s", strings.Join(kinds, " "))
	}
	for _, d := range tr.Days {
		if d.Label == "Tue 3" && d.PlanChanges != 1 || d.Label == "Wed 4" && d.PlanChanges != 1 || d.Label == "Mon 2" && d.PlanChanges != 0 {
			t.Errorf("plan changes %+v", d)
		}
	}

	m := tr.Movers
	if m.Faster != 1 || m.Slower != 1 || len(m.SlowerList) != 1 || len(m.FasterList) != 1 {
		t.Fatalf("movers %+v", m)
	}
	s, f := m.SlowerList[0], m.FasterList[0]
	// 7,000 runs over 7 days: 1,000 a day, 304 ms more each.
	if s.QueryHash != "0x01" || s.CPUMsPerDay != 304000 || s.After != "the procedure dbo.PickSequence was changed" ||
		s.AfterAt == nil || !s.AfterAt.Equal(time.Date(2026, 11, 3, 15, 54, 0, 0, time.UTC)) {
		t.Errorf("slower %+v", s)
	}
	if f.QueryHash != "0x02" || f.After != "dbo.ItemAlias was changed" || f.CPUMsPerDay >= 0 || m.SavedMsPerDay != -f.CPUMsPerDay {
		t.Errorf("faster %+v saved %v", f, m.SavedMsPerDay)
	}

	// The card's fact on the database page.
	if x, ok := TrendFact(tr); !ok || x.Label != "Against usual" || x.Value != "Better, 73% less CPU per query" || x.Tone != ToneNormal {
		t.Errorf("fact %+v", x)
	}
}

// Before 10:00 today has fewer than two complete business hours: the trend judges yesterday, and a
// day with too few days of its kind before it says why instead of a verdict.
func TestPerformanceTrendYesterdayAndTooFew(t *testing.T) {
	f := trendFacts()
	f.Load.ServerNow = time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	keep := []LoadHourFact{}
	for _, h := range f.Load.Hours {
		if h.Hour.Before(time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)) {
			keep = append(keep, h)
		}
	}
	f.Load.Hours = keep
	tr := BuildPerformanceTrend(f)
	if tr.Day != "yesterday" || tr.Verdict != TrendBetter || !strings.HasPrefix(tr.Sentence, "Quieter than usual yesterday") {
		t.Errorf("yesterday %q %q %q", tr.Day, tr.Verdict, tr.Sentence)
	}

	f = trendFacts()
	keep = []LoadHourFact{}
	for _, h := range f.Load.Hours {
		if !h.Hour.Before(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)) {
			keep = append(keep, h)
		}
	}
	f.Load.Hours = keep
	if tr := BuildPerformanceTrend(f); tr.Verdict != "" || tr.Message != "Too few working days before today to compare with: Query Store holds 1." {
		t.Errorf("too few %q %q", tr.Verdict, tr.Message)
	}
	if BuildPerformanceTrend(PerformanceFacts{}) != nil {
		t.Error("a trend without the load")
	}
}

func TestContainsName(t *testing.T) {
	for _, c := range []struct {
		text, name string
		want       bool
	}{
		{"SELECT * FROM DocumentDetail WHERE x = 1", "Document", false},
		{"SELECT * FROM Document WHERE x = 1", "document", true},
		{"SELECT * FROM [dbo].[Document]", "Document", true},
		{"EXEC dbo.Document_Save", "Document", false},
		{`SELECT * FROM "Document"`, "Document", true},
	} {
		if got := containsName(c.text, c.name); got != c.want {
			t.Errorf("%q in %q: %v", c.name, c.text, got)
		}
	}
}
