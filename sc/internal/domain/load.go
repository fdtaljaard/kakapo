package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The load strip at the top of the database page (docs/phase5i-contract.md, section 2): how loaded
// the database is now and today, hour by hour, against the usual range of the selected days at the
// same hour. Query Store hour by hour as the tuning report reads it (5c: server-local hours, sums
// not averages, Monday to Friday as working days); CPU as cores busy (5h: CPU microseconds in the
// hour / 3,600,000,000). Every time here is the server's clock, kept naive in UTC.

// The days the strip compares with: 7, 14 or 30 calendar days before today, 7 by default.
const LoadDefaultDays = 7

var loadDayChoices = []int{7, 14, 30}

// LoadBarelyUsedShare is kakapo's own line for a day that was barely used: its queries per hour held
// under a quarter of the median day's. Set on OldSchool's 30 days (measured 2026-11-05): 15 and 16 Oct
// ran a fifth or less of a normal day's queries per hour and would pull the usual range down to
// nothing; Wed 4 Nov, quiet but used, ran nine tenths and stays.
const LoadBarelyUsedShare = 0.25

// LoadMinDays is how many days of today's kind the usual range needs.
const LoadMinDays = 2

// Kinds of day (5c: Monday to Friday is a working day).
const (
	DayWorking = "working"
	DayWeekend = "weekend"
)

// ParseLoadDays reads ?days=: 7 when empty, else 7, 14 or 30.
func ParseLoadDays(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return LoadDefaultDays, nil
	}
	n, err := strconv.Atoi(s)
	if err == nil {
		for _, d := range loadDayChoices {
			if n == d {
				return n, nil
			}
		}
	}
	return 0, fmt.Errorf("days must be 7, 14 or 30, not %q", s)
}

// LoadFacts is what collect.LoadHours read inside one database from the day From on. Times are the
// server's clock (GETDATE), kept naive; CPU is microseconds summed as avg x count_executions per
// interval, as 5c. An area that could not be read carries its reason.
type LoadFacts struct {
	Database         string
	ServerNow        time.Time
	UTCOffsetMinutes int
	From             time.Time // the first day read, server-local midnight

	QueryStoreState string // actual_state_desc
	IntervalMinutes int
	OldestInterval  *time.Time // the start of the oldest interval held
	NewestInterval  *time.Time // the end of the newest interval held
	CPUCount        int        // sys.dm_os_sys_info cpu_count; 0 when not read
	AboutError      string
	CPUCountError   string

	Hours      []LoadHourFact // the hours held from From on, oldest first
	HoursError string
	WaitsError string // waits per hour (SQL Server 2017 and later)

	// Now: the interval in progress and the one before it, from NowStart to ServerNow.
	NowStart      *time.Time
	NowExecutions int64
	NowCPU        int64 // microseconds
	NowWaitSec    float64
	NowError      string
}

// LoadHourFact is one hour Query Store holds (server clock, on the hour), every plan summed.
type LoadHourFact struct {
	Hour       time.Time
	Executions int64
	CPU        int64   // microseconds
	WaitSec    float64 // every wait category summed; 0 when waits are not read
}

// LoadFrom is the first day the read needs: the server's date less the days compared.
func LoadFrom(serverNow time.Time, days int) time.Time {
	return tDate(serverNow).AddDate(0, 0, -days)
}

// DayKind says whether a day is a working day or a weekend day.
func DayKind(t time.Time) string {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return DayWeekend
	}
	return DayWorking
}

// CoresBusy is the cores a database's queries kept busy on average over a span (5h): its CPU
// microseconds over the span's microseconds; for an hour, CPU / 3,600,000,000.
func CoresBusy(cpuMicroseconds int64, span time.Duration) float64 {
	if span <= 0 {
		return 0
	}
	return float64(cpuMicroseconds) / float64(span.Microseconds())
}

// LoadRange is the usual range of one measure at one hour of the day: the lowest, the highest and the
// median of the days compared.
type LoadRange struct {
	Low    float64
	High   float64
	Median float64
}

// loadHourRange is the usual range of the three measures at one hour of the day.
type loadHourRange struct {
	cores, queries LoadRange
	threads        *LoadRange // nil when waits are not read
}

// LoadBase is what the strip compares today with, built once per server date and kept by the
// service, so the minute refresh reads only today: the days of today's kind in the selected days, the
// barely used ones left out, and the usual range of each hour of the day.
type LoadBase struct {
	Date     time.Time // the server date it was built for
	Days     int
	Kind     string
	Held     int // days of today's kind Query Store holds in the selected days
	Compared []time.Time
	LeftOut  []time.Time
	ranges   [24]*loadHourRange // nil when too few days or no day compared holds the hour
}

// For says whether the base was built for this server date and these days.
func (b LoadBase) For(serverNow time.Time, days int) bool {
	return !b.Date.IsZero() && b.Date.Equal(tDate(serverNow)) && b.Days == days
}

// loadDay is one day Query Store holds, with the hours it holds.
type loadDay struct {
	date  time.Time
	hours map[int]LoadHourFact
}

// queriesPerHour is the day's queries per hour held.
func (d loadDay) queriesPerHour() float64 {
	if len(d.hours) == 0 {
		return 0
	}
	var n int64
	for _, h := range d.hours {
		n += h.Executions
	}
	return float64(n) / float64(len(d.hours))
}

// LoadBaseline builds the comparison from the hours before today (docs/phase5i-contract.md, "The
// comparison"): the days of today's kind in the selected days before today that Query Store holds,
// each with the hours it holds (an hour Query Store has no interval for is not held, so a day it
// started partway through counts for the hours it has); a day whose queries per hour held are under
// LoadBarelyUsedShare of the median day's is left out; the usual range of each hour of the day is the
// lowest, the highest and the median of the days left, for cores busy, queries and threads waiting.
// Fewer than LoadMinDays days left: no range.
func LoadBaseline(f LoadFacts, days int) LoadBase {
	today := tDate(f.ServerNow)
	b := LoadBase{Date: today, Days: days, Kind: DayKind(today), Compared: []time.Time{}, LeftOut: []time.Time{}}
	byDate := map[time.Time]*loadDay{}
	for _, h := range f.Hours {
		d := tDate(h.Hour)
		if !d.Before(today) || d.Before(today.AddDate(0, 0, -days)) || DayKind(d) != b.Kind {
			continue
		}
		if byDate[d] == nil {
			byDate[d] = &loadDay{date: d, hours: map[int]LoadHourFact{}}
		}
		byDate[d].hours[h.Hour.Hour()] = h
	}
	held := make([]loadDay, 0, len(byDate))
	for _, d := range byDate {
		held = append(held, *d)
	}
	sort.Slice(held, func(i, j int) bool { return held[i].date.Before(held[j].date) })
	b.Held = len(held)

	qph := make([]float64, 0, len(held))
	for _, d := range held {
		qph = append(qph, d.queriesPerHour())
	}
	line := median(qph) * LoadBarelyUsedShare
	kept := []loadDay{}
	for _, d := range held {
		if d.queriesPerHour() < line {
			b.LeftOut = append(b.LeftOut, d.date)
			continue
		}
		kept = append(kept, d)
		b.Compared = append(b.Compared, d.date)
	}
	if len(kept) < LoadMinDays {
		return b
	}
	waits := f.WaitsError == ""
	for h := 0; h < 24; h++ {
		var cores, queries, threads []float64
		for _, d := range kept {
			x, ok := d.hours[h]
			if !ok {
				continue
			}
			cores = append(cores, CoresBusy(x.CPU, time.Hour))
			queries = append(queries, float64(x.Executions))
			threads = append(threads, x.WaitSec/3600)
		}
		if len(cores) == 0 {
			continue
		}
		r := &loadHourRange{cores: rangeOf(cores), queries: rangeOf(queries)}
		if waits {
			t := rangeOf(threads)
			r.threads = &t
		}
		b.ranges[h] = r
	}
	return b
}

func rangeOf(xs []float64) LoadRange {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range xs {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return LoadRange{Low: lo, High: hi, Median: median(xs)}
}

// median is the middle value, or the mean of the middle two; 0 for none.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// Load is the strip as the page shows it (JSON).
type Load struct {
	Database     string `json:"database"`
	Days         int    `json:"days"`
	ServerNow    string `json:"serverNow"` // server clock
	Kind         string `json:"kind"`      // working or weekend: today's kind
	DaysCompared int    `json:"daysCompared"`
	DaysLeftOut  int    `json:"daysLeftOut"`

	Now   LoadNow    `json:"now"`
	Hours []LoadHour `json:"hours"` // 24, one per hour of today

	QueryStore LoadQueryStore `json:"queryStore"`
	// Message replaces the load line when the load cannot be shown ("Query Store is off, so kakapo
	// cannot show the load."); "" otherwise. Chart says whether the chart is shown.
	Message string `json:"message,omitempty"`
	Chart   bool   `json:"chart"`
	// Key is the band's line under the chart: "range of the last 5 working days at that hour (2 barely
	// used, left out)", or why there is no band.
	Key string `json:"key"`
	// Action is the query-store fix when Query Store is off or not recording and it can be changed here.
	Action *QueryAction `json:"action,omitempty"`
	Notes  []string     `json:"notes"` // what could not be read, and why

	// LiveFlags are the live red flags (ACT01 and PRS01, read with this load); LiveError says why they
	// could not be read, and then the page keeps the overview's.
	LiveFlags []RedFlag `json:"liveFlags"`
	LiveError string    `json:"liveError,omitempty"`
}

// LoadMeasure is one measure with the usual range at its hour; nil fields are not known.
type LoadMeasure struct {
	Value  *float64 `json:"value"`
	Low    *float64 `json:"low"`
	High   *float64 `json:"high"`
	Median *float64 `json:"median"`
}

// LoadNow is the load line: the last 15 to 30 minutes (two intervals) as rates, against the usual
// range of the current hour.
type LoadNow struct {
	Cores   LoadMeasure  `json:"cores"`   // cores busy
	Queries LoadMeasure  `json:"queries"` // queries an hour
	Threads *LoadMeasure `json:"threads"` // threads waiting; nil before SQL Server 2017
	// Since is the start of the interval before the one in progress (server clock); ShowSince when the
	// intervals are an hour or longer, so the line says "since {start}".
	Since     string `json:"since"`
	ShowSince bool   `json:"showSince"`
	CPUCount  int    `json:"cpuCount"` // 0 when not read
}

// LoadHour is one hour of today with the usual range at that hour.
type LoadHour struct {
	Hour       int          `json:"hour"`
	Cores      LoadMeasure  `json:"cores"`
	Queries    LoadMeasure  `json:"queries"`
	Threads    *LoadMeasure `json:"threads"`
	Above      bool         `json:"above"` // cores busy above the usual range
	InProgress bool         `json:"inProgress"`
	Future     bool         `json:"future"`
}

// LoadQueryStore is Query Store's state as the strip read it.
type LoadQueryStore struct {
	State           string `json:"state"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Oldest          string `json:"oldest"` // "" when it holds nothing
	Newest          string `json:"newest"`
	Recording       bool   `json:"recording"` // READ_WRITE
}

// LoadOptions are what the service knows besides the facts.
type LoadOptions struct {
	CanChange bool // the query-store fix can be applied on this database
}

// BuildLoad answers the strip from today's facts and the base (LoadBaseline of the same server date
// and days): today's 24 hours, each against its usual range; the load now against the current hour's;
// the key; and what to do when Query Store is off or not recording.
func BuildLoad(f LoadFacts, base LoadBase, opt LoadOptions) Load {
	now := f.ServerNow
	today := tDate(now)
	l := Load{Database: f.Database, Days: base.Days, ServerNow: tStamp(now), Kind: DayKind(today),
		DaysCompared: len(base.Compared), DaysLeftOut: len(base.LeftOut), Hours: []LoadHour{}, Notes: []string{}, LiveFlags: []RedFlag{}}
	qs := LoadQueryStore{State: f.QueryStoreState, IntervalMinutes: f.IntervalMinutes, Recording: strings.EqualFold(f.QueryStoreState, "READ_WRITE")}
	if f.OldestInterval != nil {
		qs.Oldest = tStamp(*f.OldestInterval)
	}
	if f.NewestInterval != nil {
		qs.Newest = tStamp(*f.NewestInterval)
	}
	l.QueryStore = qs
	l.Now.CPUCount = f.CPUCount
	fix := &QueryAction{FixID: FixQueryStore, Label: "Apply", Title: "Apply recommended Query Store settings", Options: map[string]string{}}

	switch {
	case f.AboutError != "":
		l.Message = "Query Store could not be read, so kakapo cannot show the load: " + f.AboutError
		return l
	case strings.EqualFold(f.QueryStoreState, "OFF") || f.QueryStoreState == "":
		l.Message = "Query Store is off, so kakapo cannot show the load."
		if opt.CanChange {
			l.Action = fix
		}
		return l
	}
	l.Chart = true
	waits := f.WaitsError == ""
	if f.HoursError != "" {
		l.Notes = append(l.Notes, "Could not read Query Store's hours: "+f.HoursError)
	}
	if !waits {
		l.Notes = append(l.Notes, "Threads waiting are not shown: "+f.WaitsError)
	}
	if f.CPUCountError != "" {
		l.Notes = append(l.Notes, "Could not read how many cores SQL Server has (sys.dm_os_sys_info): "+f.CPUCountError)
	}
	if f.NowError != "" {
		l.Notes = append(l.Notes, "Could not read the last 30 minutes: "+f.NowError)
	}

	// Today's hours.
	held := map[int]LoadHourFact{}
	for _, h := range f.Hours {
		if tDate(h.Hour).Equal(today) {
			held[h.Hour.Hour()] = h
		}
	}
	for h := 0; h < 24; h++ {
		start := today.Add(time.Duration(h) * time.Hour)
		row := LoadHour{Hour: h, Future: start.After(now), InProgress: !start.After(now) && now.Before(start.Add(time.Hour))}
		r := base.ranges[h]
		row.Cores, row.Queries = withRange(nil, rangePtr(r, "cores")), withRange(nil, rangePtr(r, "queries"))
		if waits {
			t := withRange(nil, rangePtr(r, "threads"))
			row.Threads = &t
		}
		if !row.Future {
			x, ok := held[h]
			// An hour Query Store holds no interval for ran nothing, while it records and once it began.
			began := f.OldestInterval != nil && start.Add(time.Hour).After(*f.OldestInterval)
			if ok || (qs.Recording && began && f.HoursError == "") {
				span := time.Hour
				if row.InProgress {
					span = now.Sub(start)
				}
				row.Cores.Value = fptr(round3(CoresBusy(x.CPU, span)))
				row.Queries.Value = fptr(math.Round(float64(x.Executions) / span.Hours()))
				if row.Threads != nil {
					row.Threads.Value = fptr(round3(x.WaitSec / span.Seconds()))
				}
				row.Above = row.Cores.High != nil && *row.Cores.Value > *row.Cores.High
			}
		}
		l.Hours = append(l.Hours, row)
	}

	// Now: the two latest intervals as rates over their elapsed time.
	cur := base.ranges[now.Hour()]
	l.Now.Cores, l.Now.Queries = withRange(nil, rangePtr(cur, "cores")), withRange(nil, rangePtr(cur, "queries"))
	if waits {
		t := withRange(nil, rangePtr(cur, "threads"))
		l.Now.Threads = &t
	}
	if f.NowStart != nil {
		l.Now.Since = tStamp(*f.NowStart)
		l.Now.ShowSince = f.IntervalMinutes >= 60
		if span := now.Sub(*f.NowStart); qs.Recording && f.NowError == "" && span > 0 {
			l.Now.Cores.Value = fptr(round3(CoresBusy(f.NowCPU, span)))
			l.Now.Queries.Value = fptr(math.Round(float64(f.NowExecutions) / span.Hours()))
			if l.Now.Threads != nil {
				l.Now.Threads.Value = fptr(round3(f.NowWaitSec / span.Seconds()))
			}
		}
	}

	// The key.
	kind := "working days"
	if l.Kind == DayWeekend {
		kind = "weekend days"
	}
	left := ""
	if n := len(base.LeftOut); n > 0 {
		left = fmt.Sprintf(" (%d barely used, left out)", n)
	}
	if len(base.Compared) < LoadMinDays {
		l.Key = fmt.Sprintf("Query Store holds %s like today here; the usual range needs %d.", plural(int64(len(base.Compared)), "day", "days"), LoadMinDays) + left
	} else {
		l.Key = fmt.Sprintf("range of the last %d %s at that hour", len(base.Compared), kind) + left
	}
	if !qs.Recording {
		since := "it was turned read-only"
		if f.NewestInterval != nil {
			since = tWhen(*f.NewestInterval)
		}
		state := "read-only"
		if !strings.EqualFold(f.QueryStoreState, "READ_ONLY") {
			state = "in state " + f.QueryStoreState
		}
		l.Key += fmt.Sprintf(". Query Store is %s: nothing new recorded since %s.", state, since)
		if opt.CanChange {
			l.Action = fix
		}
	}
	return l
}

func rangePtr(r *loadHourRange, measure string) *LoadRange {
	if r == nil {
		return nil
	}
	switch measure {
	case "cores":
		return &r.cores
	case "queries":
		return &r.queries
	}
	return r.threads
}

// withRange is a measure with its value and the usual range, rounded for the page.
func withRange(v *float64, r *LoadRange) LoadMeasure {
	m := LoadMeasure{Value: v}
	if r != nil {
		m.Low, m.High, m.Median = fptr(round3(r.Low)), fptr(round3(r.High)), fptr(round3(r.Median))
	}
	return m
}

func fptr(x float64) *float64 { return &x }

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }
