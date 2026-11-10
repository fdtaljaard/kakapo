package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Phase 5j (docs/phase5j-contract.md): the query page answers first whether the query still needs
// attention. The plan in use against the runs before it began, with 5c's TuningVerdict; what
// changed in between (plans begun, statistics updated); what to do.

// QueryVerdict is the top of the query page: the plan in use since it began against the runs before.
type QueryVerdict struct {
	Verdict  string    `json:"verdict"` // faster, slower, same, too few (TuningVerdict)
	Headline string    `json:"headline"`
	Since    time.Time `json:"since"` // UTC: when the plan in use began, or kakapo's latest change after it
	// Change is kakapo's own change at Since, when the verdict splits there (docs/phase5q-contract.md,
	// decision 3): "Reorganised IX_A on dbo.T in Shop (...)". "" when Since is the plan's start.
	Change  string       `json:"change,omitempty"`
	PlanID  int64        `json:"planId"`
	Before  QueryPeriod  `json:"before"`
	Now     QueryPeriod  `json:"now"`
	Changed []QueryEvent `json:"changed"` // what changed between the costliest plan before and Since
	ToDo    string       `json:"toDo"`
}

// QueryPeriod is the query's runs in one side of the verdict, every plan together.
type QueryPeriod struct {
	Label         string  `json:"label"` // "30 Oct 08:00 to 4 Nov 15:00", "since 4 Nov 15:34"
	Runs          int64   `json:"runs"`
	AvgDurationMs float64 `json:"avgDurationMs"`
	AvgCPUMs      float64 `json:"avgCpuMs"`
	AvgReads      float64 `json:"avgReads"`
	CPUMs         float64 `json:"cpuMs"` // in total
	DurationMs    float64 `json:"durationMs"`
}

// QueryEvent is something that happened to the query in the window: a plan SQL Server began to use,
// statistics updated on a table its plans read, or a change kakapo made to one of those tables.
type QueryEvent struct {
	At     time.Time `json:"at"`   // UTC
	Kind   string    `json:"kind"` // plan, statistics, change
	PlanID int64     `json:"planId,omitempty"`
	Text   string    `json:"text"`
}

// Kinds of QueryEvent.
const (
	QueryEventPlan       = "plan"
	QueryEventStatistics = "statistics"
	QueryEventChange     = "change"
)

// qOwnChanges are kakapo's own changes (not seen on the server) to the tables the plans read, in
// the window, oldest first: an index fix's object is "schema.table.index", the statistics fix's
// "Statistics on schema.table".
func qOwnChanges(f QueryFacts, d QueryDetail, start time.Time) []Change {
	out := []Change{}
	for _, c := range f.Changes {
		if c.Result == ChangeSeenOnServer || !strings.EqualFold(c.Database, f.Database) || c.ChangedAt.Before(start) {
			continue
		}
		for _, t := range d.Tables {
			name := strings.ToLower(t.Schema + "." + t.Table)
			obj := strings.ToLower(c.Object)
			if (c.ChangeType == "INDEX" && strings.HasPrefix(obj, name+".")) || (c.ChangeType == "MAINTENANCE" && obj == "statistics on "+name) {
				out = append(out, c)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ChangedAt.Before(out[j].ChangedAt) })
	return out
}

// queryNearlyAllRatio: when the time spent before is this many times the time spent now, the
// window's waits are "nearly all from before".
const queryNearlyAllRatio = 100

// qPlanFirstRuns sets each Query Store plan's first run in the window: its first hour in the history,
// or its first compile when that falls inside that hour.
func qPlanFirstRuns(f QueryFacts, plans []QueryPlan) {
	compiled := map[int64]*time.Time{}
	for _, p := range f.Plans {
		compiled[p.PlanID] = p.InitialCompile
	}
	for i := range plans {
		p := &plans[i]
		if p.PlanID <= 0 || p.Source != QueryDetailFromQueryStore {
			continue
		}
		var first *time.Time
		for _, h := range f.History {
			if h.PlanID == p.PlanID && h.Runs > 0 && (first == nil || h.Hour.Before(*first)) {
				t := h.Hour
				first = &t
			}
		}
		if first == nil {
			continue
		}
		if c := compiled[p.PlanID]; c != nil && !c.Before(*first) && c.Before(first.Add(time.Hour)) {
			t := *c
			first = &t
		}
		p.FirstRun = first
	}
}

// qWindowStart is the first hour of the query's history; zero when it has none.
func qWindowStart(f QueryFacts) time.Time {
	var start time.Time
	for _, h := range f.History {
		if start.IsZero() || h.Hour.Before(start) {
			start = h.Hour
		}
	}
	return start
}

// qQueryEvents are the plans that began in the window (not those already running at its start) and
// the statistics updated on the tables the plans read, oldest first.
func qQueryEvents(f QueryFacts, d QueryDetail) []QueryEvent {
	out := []QueryEvent{}
	start := qWindowStart(f)
	if start.IsZero() {
		return out
	}
	for _, p := range d.Plans {
		if p.FirstRun == nil || p.FirstRun.Before(start.Add(time.Hour)) || p.Runs == 0 {
			continue
		}
		out = append(out, QueryEvent{At: *p.FirstRun, Kind: QueryEventPlan, PlanID: p.PlanID,
			Text: fmt.Sprintf("SQL Server began to use plan %d: %s a run, %s pages read.", p.PlanID, QueryDuration(p.AvgDurationMs), n0(p.AvgReads))})
	}
	off := time.Duration(f.UTCOffsetMinutes) * time.Minute
	for _, t := range f.Tables {
		groups := map[time.Time][]QueryStatUpdateFact{}
		for _, u := range t.Updates {
			at := u.Updated.Add(-off).Truncate(time.Minute)
			if at.Before(start) {
				continue
			}
			groups[at] = append(groups[at], u)
		}
		for at, us := range groups {
			name := t.Schema + "." + t.Table
			all := true
			for _, u := range us {
				all = all && u.Rows > 0 && u.Sampled >= u.Rows
			}
			text := ""
			if len(us) == 1 && us[0].Index && all {
				text = fmt.Sprintf("Index %s on %s: created or rebuilt.", us[0].Name, name)
			} else {
				read := "a sample of rows read"
				if all {
					read = "every row read"
				}
				text = fmt.Sprintf("Statistics updated on %s (%s, %s).", name, plural(int64(len(us)), "statistic", "statistics"), read)
			}
			out = append(out, QueryEvent{At: at, Kind: QueryEventStatistics, Text: text})
		}
	}
	for _, c := range qOwnChanges(f, d, start) {
		out = append(out, QueryEvent{At: c.ChangedAt.UTC(), Kind: QueryEventChange, Text: "kakapo: " + strings.TrimSuffix(c.Description, ".") + "."})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// qQueryVerdict is the plan in use against the runs before it began; nil when the page reads no
// history, or the query ran the same plan all week.
func qQueryVerdict(f QueryFacts, d QueryDetail) *QueryVerdict {
	var active *QueryPlan
	for i := range d.Plans {
		if d.Plans[i].Active && d.Plans[i].Source == QueryDetailFromQueryStore {
			active = &d.Plans[i]
		}
	}
	if active == nil || active.FirstRun == nil {
		return nil
	}
	since := *active.FirstRun
	// Decision 3 (docs/phase5q-contract.md): kakapo's latest change to a table the plans read, after the
	// plan began, splits Before and Now there.
	change := ""
	if own := qOwnChanges(f, d, since); len(own) > 0 {
		last := own[len(own)-1]
		since, change = last.ChangedAt.UTC(), strings.TrimSuffix(last.Description, ".")
	}
	sinceHour := since.Truncate(time.Hour)
	type sums struct{ runs, cpu, dur, reads int64 }
	var before, now sums
	byPlan := map[int64]*sums{}
	lastHour := map[int64]time.Time{}
	var first, lastBefore time.Time
	for _, h := range f.History {
		s := &now
		if h.Hour.Before(sinceHour) {
			s = &before
			if byPlan[h.PlanID] == nil {
				byPlan[h.PlanID] = &sums{}
			}
			byPlan[h.PlanID].cpu += h.CPU
			if h.Hour.After(lastHour[h.PlanID]) {
				lastHour[h.PlanID] = h.Hour
			}
			if first.IsZero() || h.Hour.Before(first) {
				first = h.Hour
			}
			if h.Hour.After(lastBefore) {
				lastBefore = h.Hour
			}
		}
		s.runs += h.Runs
		s.cpu += h.CPU
		s.dur += h.Duration
		s.reads += h.Reads
	}
	if before.runs == 0 || now.runs == 0 {
		return nil
	}
	period := func(s sums, label string) QueryPeriod {
		return QueryPeriod{Label: label, Runs: s.runs, AvgDurationMs: qPerRunMs(s.dur, s.runs), AvgCPUMs: qPerRunMs(s.cpu, s.runs),
			AvgReads: qPerRun(s.reads, s.runs), CPUMs: roundHalfAway(float64(s.cpu)/1000, 1), DurationMs: roundHalfAway(float64(s.dur)/1000, 1)}
	}
	v := &QueryVerdict{Since: since, Change: change, PlanID: active.PlanID, Changed: []QueryEvent{},
		Before: period(before, qServerClock(first, f)+" to "+qServerClock(lastBefore.Add(time.Hour), f)),
		Now:    period(now, "since "+qServerClock(since, f))}
	v.Verdict = TuningVerdict(v.Before.Runs, v.Now.Runs, v.Before.AvgCPUMs, v.Now.AvgCPUMs)

	// What changed: from the last hour of the plan that cost the most before, up to Since.
	var costliest int64
	for id, s := range byPlan {
		if costliest == 0 || s.cpu > byPlan[costliest].cpu || (s.cpu == byPlan[costliest].cpu && id < costliest) {
			costliest = id
		}
	}
	from := lastHour[costliest]
	for _, e := range d.Events {
		if !e.At.Before(from) && !e.At.After(since) {
			v.Changed = append(v.Changed, e)
		}
	}

	at := qServerClock(since, f)
	if change != "" {
		short := change // its facts in brackets stay in the change log
		if i := strings.Index(short, " ("); i > 0 {
			short = short[:i]
		}
		at += " (kakapo: " + qLowerFirst(short) + ")"
	}
	switch v.Verdict {
	case VerdictFaster, VerdictSlower, VerdictSame:
		v.Headline = fmt.Sprintf("Each run now takes %s. Before %s it took %s.", QueryDuration(v.Now.AvgDurationMs), at, QueryDuration(v.Before.AvgDurationMs))
	default:
		v.Headline = fmt.Sprintf("Since %s it ran %s: too few runs to compare with before.", at, plural(v.Now.Runs, "time", "times"))
	}
	switch vd := v.Verdict; {
	case vd == VerdictFaster:
		v.ToDo = fmt.Sprintf("Nothing: it uses %s of CPU a run now, against %s before. If it gets slower again, it comes back on the Performance card.",
			QueryDuration(v.Now.AvgCPUMs), QueryDuration(v.Before.AvgCPUMs))
	case vd == VerdictSlower && change != "":
		v.ToDo = "It got slower after that change. How it works below says why; the change log has its rollback, when it has one."
	case vd == VerdictSlower:
		v.ToDo = fmt.Sprintf("It got slower when SQL Server began to use plan %d. How it works below says why, and whether a faster plan can be forced.", active.PlanID)
	case vd == VerdictSame && change != "":
		v.ToDo = "That change did not change its cost. How it works below says where its cost is."
	case vd == VerdictSame:
		v.ToDo = "The new plan did not change its cost. How it works below says where its cost is."
	default:
		v.ToDo = "Look again after more runs."
	}
	return v
}

// qLowerFirst lowers a sentence's first letter to run it on inside another: "Reorganised IX_A ..."
// becomes "reorganised IX_A ...".
func qLowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// qStatements is how many Query Store queries (statements) the plans belong to.
func qStatements(plans []QueryPlan) int {
	ids := map[int64]bool{}
	for _, p := range plans {
		if p.QueryID > 0 {
			ids[p.QueryID] = true
		}
	}
	return len(ids)
}

// qRanSince says whether a plan ran at or after a time.
func qRanSince(p QueryPlan, since time.Time) bool {
	return p.LastRun != nil && !p.LastRun.Before(since.Truncate(time.Hour))
}

func qShownWaits(ws []QueryWait) []QueryWait {
	out := []QueryWait{}
	for _, w := range ws {
		if qShownWait(w) {
			out = append(out, w)
		}
	}
	return out
}

// qShownWait leaves out the wait categories that say nothing to the reader: idle, unknown, and
// under 1% of the query's waiting.
func qShownWait(w QueryWait) bool {
	return w.Pct >= 1 && !strings.EqualFold(w.Category, "Idle") && !strings.EqualFold(w.Category, "Unknown")
}

// qPlansAlike says whether the Query Store plans that ran are within queryRegressionRatio of each
// other in CPU per run: only then are they "of similar speed".
func qPlansAlike(plans []QueryPlan) bool {
	lo, hi := -1.0, -1.0
	for _, p := range plans {
		if p.Runs == 0 || p.Source != QueryDetailFromQueryStore {
			continue
		}
		if lo < 0 || p.AvgCPUMs < lo {
			lo = p.AvgCPUMs
		}
		hi = max(hi, p.AvgCPUMs)
	}
	return lo >= 0 && (hi <= 1 || hi <= queryRegressionRatio*max(lo, 0.1))
}
