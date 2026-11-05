package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Phase 5k (docs/phase5k-contract.md): the Performance screen says whether the database is better or
// worse than usual, and what made the difference: the queries that got a new plan, against themselves
// before, with the change kakapo saw just before each.

// PlanMoverFact is one query shape whose plan in use (the one that ran last) began in the 7 days and
// that ran at least 20 times before and since (QRY01's batch). CPU in microseconds.
type PlanMoverFact struct {
	QueryHash  string
	Since      time.Time // UTC: when the plan in use began
	Minutes    int       // from its first run in the window to now
	BeforeRuns int64
	BeforeCPU  int64
	NowRuns    int64
	NowCPU     int64
	ObjectName string // schema.procedure; "" for ad hoc
	Text       string // the first 300 characters
}

// ChangedObjectFact is a table, view or module whose modify_date moved in the last 8 days, with the
// views and modules that reference it.
type ChangedObjectFact struct {
	Schema     string
	Name       string
	Type       string    // U, V, P, FN, IF, TF, TR
	Created    time.Time // server time
	Modified   time.Time // server time
	Referenced []string  // the names of the views and modules that reference it
}

// PerformanceTrend is the top of the Performance screen (JSON).
type PerformanceTrend struct {
	Verdict  string        `json:"verdict"` // better, worse, same; "" when there is nothing to compare with
	Words    string        `json:"words"`   // Better, Worse, As usual
	Sentence string        `json:"sentence"`
	Day      string        `json:"day"` // the day judged: "today", "yesterday", "Wed 4 Nov"
	Figures  []TrendFigure `json:"figures"`
	Compared string        `json:"compared"` // what it was compared with, and the days left out
	Days     []TrendDay    `json:"days"`
	Movers   TrendMovers   `json:"movers"`
	Message  string        `json:"message"` // why there is no verdict
}

// TrendFigure is one of the three figures: work asked, CPU per query, queries waiting.
type TrendFigure struct {
	ID     string  `json:"id"` // queries, cpu, waiting
	Label  string  `json:"label"`
	Value  string  `json:"value"`
	Change string  `json:"change"` // "+6%: as usual", "−73%: cheaper"
	Usual  string  `json:"usual"`  // "usual 21.9 to 28.2 ms"
	Tone   string  `json:"tone"`   // better, worse, same
	Pct    float64 `json:"pct"`    // the change against the usual median, in percent
}

// TrendDay is one bar of the days: CPU per query in business hours.
type TrendDay struct {
	Date          string  `json:"date"`  // server date, 2006-01-02
	Label         string  `json:"label"` // "Mon 2"
	Kind          string  `json:"kind"`  // working, weekend, quiet, judged, none
	CPUPerQueryMs float64 `json:"cpuPerQueryMs"`
	QueriesPerHr  float64 `json:"queriesPerHour"`
	PlanChanges   int     `json:"planChanges"` // queries that got a faster or slower plan that day
	Note          string  `json:"note"`
}

// TrendMovers is "what made the difference".
type TrendMovers struct {
	Faster        int          `json:"faster"`
	SavedMsPerDay float64      `json:"savedMsPerDay"`
	Slower        int          `json:"slower"`
	AddedMsPerDay float64      `json:"addedMsPerDay"`
	SlowerList    []TrendMover `json:"slowerList"` // every one, most CPU added first
	FasterList    []TrendMover `json:"fasterList"` // the 5 that save the most
	Error         string       `json:"error"`
}

// TrendMover is one query that got a new plan.
type TrendMover struct {
	QueryHash   string     `json:"queryHash"`
	ObjectName  string     `json:"objectName"`
	Text        string     `json:"text"`
	Since       time.Time  `json:"since"` // UTC
	BeforeCPUMs float64    `json:"beforeCpuMs"`
	NowCPUMs    float64    `json:"nowCpuMs"`
	NowRuns     int64      `json:"nowRuns"`
	CPUMsPerDay float64    `json:"cpuMsPerDay"` // added (positive) or saved (negative), at its usual runs a day
	After       string     `json:"after"`       // "dbo.MasterItemAlias was changed"; "" when none was seen
	AfterAt     *time.Time `json:"afterAt"`     // UTC
}

// Trend verdicts.
const (
	TrendBetter = "better"
	TrendWorse  = "worse"
	TrendSame   = "same"
)

// trendFaster lists this many faster queries.
const trendFasterShown = 5

// trendHours: the business hours the trend reads, the toolkit's default.
var trendHours = BusinessHours{From: 8, To: 17}

type trendSums struct {
	hours      int
	execs, cpu int64
	waitSec    float64
}

func (s trendSums) queriesPerHour() float64 { return safeDiv(float64(s.execs), float64(s.hours)) }
func (s trendSums) cpuPerQueryMs() float64  { return safeDiv(float64(s.cpu)/1000, float64(s.execs)) }
func (s trendSums) threads() float64        { return safeDiv(s.waitSec, float64(s.hours)*3600) }

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// BuildPerformanceTrend builds the top of the Performance screen; nil when the load could not be read
// or Query Store has nothing.
func BuildPerformanceTrend(f PerformanceFacts) *PerformanceTrend {
	if f.Load == nil || f.Load.HoursError != "" || len(f.Load.Hours) == 0 {
		return nil
	}
	l := f.Load
	t := &PerformanceTrend{Figures: []TrendFigure{}, Days: []TrendDay{}}
	t.Movers = trendMovers(f)

	byDate := map[time.Time]map[int]LoadHourFact{}
	for _, h := range l.Hours {
		d := tDate(h.Hour)
		if byDate[d] == nil {
			byDate[d] = map[int]LoadHourFact{}
		}
		byDate[d][h.Hour.Hour()] = h
	}
	today := tDate(l.ServerNow)
	complete := func(d time.Time, h int) bool { return d.Before(today) || h < l.ServerNow.Hour() }
	business := func(d time.Time) []int {
		out := []int{}
		for h := trendHours.From; h < trendHours.To; h++ {
			if _, ok := byDate[d][h]; ok && complete(d, h) {
				out = append(out, h)
			}
		}
		return out
	}
	sumOf := func(d time.Time, hours []int) trendSums {
		s := trendSums{}
		for _, h := range hours {
			if x, ok := byDate[d][h]; ok {
				s.hours++
				s.execs += x.Executions
				s.cpu += x.CPU
				s.waitSec += x.WaitSec
			}
		}
		return s
	}

	// The day judged: today with two complete business hours, else the last day before that has them.
	judged := time.Time{}
	for d := today; !d.Before(today.AddDate(0, 0, -7)); d = d.AddDate(0, 0, -1) {
		if len(business(d)) >= 2 {
			judged = d
			break
		}
	}

	// Quiet days, per kind, among the days held before today (loadQuietDays, as the strip).
	quiet, quietQ, quietC := map[time.Time]bool{}, map[time.Time]float64{}, map[time.Time]float64{}
	for _, kind := range []string{DayWorking, DayWeekend} {
		days := []loadDay{}
		for d, hs := range byDate {
			if d.Before(today) && DayKind(d) == kind && len(hs) > 0 {
				days = append(days, loadDay{date: d, hours: hs})
			}
		}
		sort.Slice(days, func(i, j int) bool { return days[i].date.Before(days[j].date) })
		q, qp, cp := loadQuietDays(days)
		for i, d := range days {
			quiet[d.date], quietQ[d.date], quietC[d.date] = q[i], qp[i], cp[i]
		}
	}

	// The days: the 7 before today and today.
	changes := map[string]int{}
	off := time.Duration(f.UTCOffsetMinutes) * time.Minute
	for _, m := range f.Movers {
		if v := moverVerdict(m); v == VerdictFaster || v == VerdictSlower {
			changes[m.Since.Add(off).Format("2006-01-02")]++
		}
	}
	for d := today.AddDate(0, 0, -7); !d.After(today); d = d.AddDate(0, 0, 1) {
		s := sumOf(d, business(d))
		x := TrendDay{Date: d.Format("2006-01-02"), Label: d.Format("Mon 2"), Kind: DayKind(d), PlanChanges: changes[d.Format("2006-01-02")]}
		switch {
		case s.execs == 0:
			x.Kind, x.Note = "none", "Query Store holds no business hours of this day"
		case d.Equal(judged):
			x.Kind = "judged"
		case quiet[d]:
			x.Kind, x.Note = "quiet", "a quiet day, left out"
		}
		x.CPUPerQueryMs, x.QueriesPerHr = roundHalfAway(s.cpuPerQueryMs(), 1), roundHalfAway(s.queriesPerHour(), 0)
		t.Days = append(t.Days, x)
	}

	if judged.IsZero() {
		t.Message = "Query Store holds no two complete business hours of a day in the last 7 days."
		return t
	}
	hours := business(judged)
	switch {
	case judged.Equal(today):
		t.Day = "today"
	case judged.Equal(today.AddDate(0, 0, -1)):
		t.Day = "yesterday"
	default:
		t.Day = judged.Format("Mon 2 Jan")
	}
	subject := sumOf(judged, hours)

	// Usual: the same hours of the days of the same kind in the 7 days before, quiet days left out.
	var compared, leftOut []time.Time
	var qs, cs, ws []float64
	for d := judged.AddDate(0, 0, -7); d.Before(judged); d = d.AddDate(0, 0, 1) {
		if DayKind(d) != DayKind(judged) || len(byDate[d]) == 0 {
			continue
		}
		if quiet[d] {
			leftOut = append(leftOut, d)
			continue
		}
		s := sumOf(d, hours)
		if s.execs == 0 {
			continue
		}
		compared = append(compared, d)
		qs, cs, ws = append(qs, s.queriesPerHour()), append(cs, s.cpuPerQueryMs()), append(ws, s.threads())
	}
	kind := "working days"
	if DayKind(judged) == DayWeekend {
		kind = "weekend days"
	}
	span := fmt.Sprintf("%02d:00 to %02d:00", hours[0], hours[len(hours)-1]+1)
	t.Compared = upperFirst(t.Day) + " " + span + " against the same hours of " + trendDates(compared) + "."
	for _, d := range leftOut {
		t.Compared += fmt.Sprintf(" %s is left out: a quiet day (%s%% of the queries, %s%% of the CPU).", d.Format("Mon 2 Jan"), n0(quietQ[d]), n0(quietC[d]))
	}
	if len(compared) < LoadMinDays {
		t.Message = fmt.Sprintf("Too few %s before %s to compare with: Query Store holds %d.", kind, t.Day, len(compared))
		t.Compared = ""
		return t
	}

	waits := l.WaitsError == ""
	qr, cr, wr := safeDiv(subject.queriesPerHour(), median(qs)), safeDiv(subject.cpuPerQueryMs(), median(cs)), safeDiv(subject.threads(), median(ws))
	t.Figures = append(t.Figures,
		trendFigure("queries", "Work asked (queries an hour)", n0(subject.queriesPerHour()), qr, "busier", "quieter", "as usual",
			"usual "+n0(minOf(qs))+" to "+n0(maxOf(qs)), false),
		trendFigure("cpu", "CPU per query", trendMs(subject.cpuPerQueryMs()), cr, "dearer", "cheaper", "as usual",
			"usual "+trendMs(minOf(cs))+" to "+trendMs(maxOf(cs)), true))
	if waits {
		t.Figures = append(t.Figures, trendFigure("waiting", "Queries waiting (threads)", n1(subject.threads()), wr, "more waiting", "less waiting", "as usual",
			"usual "+n1(minOf(ws))+" to "+n1(maxOf(ws)), true))
	}

	worseWait := waits && wr >= TuningSlowerRatio && subject.threads() >= 1
	switch {
	case cr >= TuningSlowerRatio || worseWait:
		t.Verdict, t.Words = TrendWorse, "Worse"
	case cr <= TuningFasterRatio:
		t.Verdict, t.Words = TrendBetter, "Better"
	default:
		t.Verdict, t.Words = TrendSame, "As usual"
	}
	busy := "As busy as usual"
	switch {
	case qr >= TuningSlowerRatio:
		busy = "Busier than usual"
	case qr <= TuningFasterRatio:
		busy = "Quieter than usual"
	}
	clauses := []string{trendClause(cr, "CPU per query")}
	if waits {
		clauses = append(clauses, trendClause(wr, "waiting"))
	}
	when := t.Day
	if when != "today" && when != "yesterday" {
		when = "on " + when
	}
	t.Sentence = busy + " " + when + ", with " + qJoinAnd(clauses) + "."
	return t
}

// TrendFact is the card's first fact on the database page: "Better, 73% less CPU per query".
func TrendFact(t *PerformanceTrend) (Fact, bool) {
	if t == nil || t.Verdict == "" {
		return Fact{}, false
	}
	value := t.Words
	for _, x := range t.Figures {
		if x.ID == "cpu" && x.Tone != TrendSame {
			value += ", " + trendClause(1+x.Pct/100, "CPU per query")
		}
	}
	tone := ToneNormal
	if t.Verdict == TrendWorse {
		tone = ToneBad
	}
	return Fact{Label: "Against usual", Value: value, Tone: tone}, true
}

func minOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = min(m, x)
	}
	return m
}

func maxOf(xs []float64) float64 {
	m := xs[0]
	for _, x := range xs {
		m = max(m, x)
	}
	return m
}

// trendMs writes CPU per query: "6.3 ms", "0.4 ms", "120 ms".
func trendMs(ms float64) string {
	if ms >= 100 {
		return n0(ms) + " ms"
	}
	return n1(ms) + " ms"
}

// trendFigure says one figure's change against the usual median. lowerIsBetter: for CPU and waiting.
func trendFigure(id, label, value string, ratio float64, up, down, same, usual string, lowerIsBetter bool) TrendFigure {
	pct := (ratio - 1) * 100
	x := TrendFigure{ID: id, Label: label, Value: value, Usual: usual, Pct: roundHalfAway(pct, 0), Tone: TrendSame}
	sign := "+"
	if pct < 0 {
		sign = "−"
	}
	word := same
	switch {
	case ratio >= TuningSlowerRatio:
		word = up
		if lowerIsBetter {
			x.Tone = TrendWorse
		}
	case ratio <= TuningFasterRatio:
		word = down
		if lowerIsBetter {
			x.Tone = TrendBetter
		}
	}
	x.Change = sign + n0(abs(pct)) + "%: " + word
	return x
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// trendClause: "73% less CPU per query", "40% more waiting", "the usual waiting".
func trendClause(ratio float64, what string) string {
	switch {
	case ratio <= TuningFasterRatio:
		return n0((1-ratio)*100) + "% less " + what
	case ratio >= TuningSlowerRatio:
		return n0((ratio-1)*100) + "% more " + what
	}
	return "the usual " + what
}

// trendDates: "Fri 30 Oct, Mon 2 and Tue 3 Nov".
func trendDates(ds []time.Time) string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.Format("Mon 2 Jan")
		if i+1 < len(ds) && ds[i+1].Month() == d.Month() {
			out[i] = d.Format("Mon 2")
		}
	}
	return qJoinAnd(out)
}

func moverVerdict(m PlanMoverFact) string {
	return TuningVerdict(m.BeforeRuns, m.NowRuns, safeDiv(float64(m.BeforeCPU), float64(m.BeforeRuns)), safeDiv(float64(m.NowCPU), float64(m.NowRuns)))
}

// trendMovers: every query that got faster or slower with its new plan, the CPU a day it saves or
// adds at its usual runs a day, and the change seen just before it.
func trendMovers(f PerformanceFacts) TrendMovers {
	out := TrendMovers{SlowerList: []TrendMover{}, FasterList: []TrendMover{}, Error: f.MoversError}
	off := time.Duration(f.UTCOffsetMinutes) * time.Minute
	var faster []TrendMover
	for _, m := range f.Movers {
		v := moverVerdict(m)
		if v != VerdictFaster && v != VerdictSlower {
			continue
		}
		before := safeDiv(float64(m.BeforeCPU), float64(m.BeforeRuns)) / 1000
		now := safeDiv(float64(m.NowCPU), float64(m.NowRuns)) / 1000
		perDay := float64(m.BeforeRuns+m.NowRuns) * 1440 / float64(max(m.Minutes, 60))
		x := TrendMover{QueryHash: m.QueryHash, ObjectName: m.ObjectName, Text: oneLine(m.Text), Since: m.Since,
			BeforeCPUMs: roundHalfAway(before, 1), NowCPUMs: roundHalfAway(now, 1), NowRuns: m.NowRuns,
			CPUMsPerDay: roundHalfAway((now-before)*perDay, 0)}
		if c, ok := changeBefore(m, f.Changed, m.Since.Add(off)); ok {
			x.After = changedWords(c)
			at := c.Modified.Add(-off)
			x.AfterAt = &at
		}
		if v == VerdictSlower {
			out.Slower++
			out.AddedMsPerDay += x.CPUMsPerDay
			out.SlowerList = append(out.SlowerList, x)
		} else {
			out.Faster++
			out.SavedMsPerDay -= x.CPUMsPerDay
			faster = append(faster, x)
		}
	}
	sort.SliceStable(out.SlowerList, func(i, j int) bool { return out.SlowerList[i].CPUMsPerDay > out.SlowerList[j].CPUMsPerDay })
	sort.SliceStable(faster, func(i, j int) bool { return faster[i].CPUMsPerDay < faster[j].CPUMsPerDay })
	if len(faster) > trendFasterShown {
		faster = faster[:trendFasterShown]
	}
	out.FasterList = append(out.FasterList, faster...)
	return out
}

// changeBefore is the latest change in the 24 hours before the query's new plan began (to the end of
// its first hour) to an object the query names: its own procedure, a name in its text, or a view or
// module in its text (or its procedure) that references the changed object. sinceLocal is server time.
func changeBefore(m PlanMoverFact, changed []ChangedObjectFact, sinceLocal time.Time) (ChangedObjectFact, bool) {
	own := ""
	if i := strings.LastIndex(m.ObjectName, "."); i >= 0 {
		own = m.ObjectName[i+1:]
	}
	var best ChangedObjectFact
	found := false
	for _, c := range changed {
		if c.Modified.Before(sinceLocal.Add(-24*time.Hour)) || !c.Modified.Before(sinceLocal.Truncate(time.Hour).Add(time.Hour)) {
			continue
		}
		hit := strings.EqualFold(own, c.Name) || containsName(m.Text, c.Name)
		for _, r := range c.Referenced {
			hit = hit || strings.EqualFold(own, r) || containsName(m.Text, r)
		}
		if hit && (!found || c.Modified.After(best.Modified)) {
			best, found = c, true
		}
	}
	return best, found
}

// containsName says whether an identifier appears in a statement as a whole name (any case): not
// "Document" inside "DocumentDetail".
func containsName(text, name string) bool {
	if name == "" {
		return false
	}
	lt, ln := strings.ToLower(text), strings.ToLower(name)
	for i := 0; ; {
		j := strings.Index(lt[i:], ln)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(ln)
		if (a == 0 || !identChar(lt[a-1])) && (b == len(lt) || !identChar(lt[b])) {
			return true
		}
		i = a + 1
	}
}

func identChar(c byte) bool {
	return c == '_' || c == '@' || c == '#' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// changedWords: "dbo.MasterItemAlias was changed", "the procedure dbo.X was changed".
func changedWords(c ChangedObjectFact) string {
	name := c.Schema + "." + c.Name
	verb := "was changed"
	if c.Modified.Sub(c.Created) < time.Minute {
		verb = "was created"
	}
	switch c.Type {
	case "P":
		return "the procedure " + name + " " + verb
	case "V":
		return "the view " + name + " " + verb
	case "FN", "IF", "TF":
		return "the function " + name + " " + verb
	case "TR":
		return "the trigger " + name + " " + verb
	}
	return name + " " + verb
}
