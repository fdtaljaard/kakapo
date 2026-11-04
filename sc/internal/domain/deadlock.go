package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// DeadlockReport is DLK01, the deadlock report read on request from the system_health session
// and stored (docs/phase3e-contract.md).
type DeadlockReport struct {
	CapturedAt      time.Time      `json:"capturedAt"`
	DurationSeconds float64        `json:"durationSeconds"`
	Days            int            `json:"days"`
	Total           int            `json:"total"`
	Source          string         `json:"source"`   // file, ring-buffer
	Pairs           []DeadlockPair `json:"pairs"`    // by count desc
	Findings        []Finding      `json:"findings"` // one DLK01 finding per pair, ranked
	// Health is the server's own health events of the window from the same session: SYSTEM and
	// IO_SUBSYSTEM components in state warning or error, newest first, at most 50
	// (docs/phase3f-contract.md, item 21). Left out of the JSON when there are none.
	Health []HealthEvent `json:"health,omitempty"`
}

// HealthEvent is one sp_server_diagnostics_component_result event worth showing: when (UTC), the
// component (SYSTEM, IO_SUBSYSTEM), the state (warning, error) and what it reported, in words.
type HealthEvent struct {
	At        time.Time `json:"at"`
	Component string    `json:"component"`
	State     string    `json:"state"`
	Detail    string    `json:"detail"`
}

// PendingIO is one of the longest pending I/O requests an IO_SUBSYSTEM event lists.
type PendingIO struct {
	File       string
	DurationMs int64
}

// HealthEventsMax is how many health events a report keeps.
const HealthEventsMax = 50

// HealthDetail says what a SYSTEM or IO_SUBSYSTEM health event reported, from the attributes of its
// data element (names in any case) and, for IO_SUBSYSTEM, the longest pending requests: the
// counters sp_HealthParser reads (non-yielding tasks, memory dumps, bad pages, access violations,
// sick spinlocks, latch warnings; I/O latch timeouts, long I/Os, the longest pending request).
func HealthDetail(component string, attrs map[string]string, pending []PendingIO) string {
	num := func(name string) int64 {
		var n int64
		fmt.Sscan(attrs[strings.ToLower(name)], &n)
		return n
	}
	parts := []string{}
	switch strings.ToUpper(component) {
	case "SYSTEM":
		if n := num("nonYieldingTasksReported"); n > 0 {
			parts = append(parts, plural(n, "non-yielding task", "non-yielding tasks")+" (a worker that would not give up its CPU)")
		}
		if n := num("totalDumpRequests"); n > 0 {
			p := plural(n, "memory dump", "memory dumps") + " since startup"
			if m := num("intervalDumpRequests"); m > 0 {
				p += fmt.Sprintf(" (%s in this interval)", n0int(m))
			}
			parts = append(parts, p)
		}
		if n := num("BadPagesDetected"); n > 0 {
			parts = append(parts, plural(n, "bad page", "bad pages")+" detected (corruption: run DBCC CHECKDB)")
		}
		if n := max(num("isAccessViolationOccurred"), num("writeAccessViolationCount")); n > 0 {
			parts = append(parts, "access violations")
		}
		if t := strings.TrimSpace(attrs["sickspinlocktype"]); t != "" && !strings.EqualFold(t, "none") {
			parts = append(parts, "sick spinlock "+t)
		}
		if n := num("latchWarnings"); n > 0 {
			parts = append(parts, plural(n, "latch warning", "latch warnings"))
		}
	case "IO_SUBSYSTEM":
		if n := num("ioLatchTimeouts"); n > 0 {
			parts = append(parts, plural(n, "I/O latch timeout", "I/O latch timeouts"))
		}
		if n := num("totalLongIos"); n > 0 {
			parts = append(parts, plural(n, "long I/O", "long I/Os")+" (over 15 s) since startup")
		}
		var worst *PendingIO
		for i, p := range pending {
			if worst == nil || p.DurationMs > worst.DurationMs {
				worst = &pending[i]
			}
		}
		if worst != nil && worst.DurationMs > 0 {
			parts = append(parts, fmt.Sprintf("the longest pending request has waited %s s on %s", n1(float64(worst.DurationMs)/1000), orQ(worst.File)))
		}
	}
	if len(parts) == 0 {
		return "No counter set; SQL Server reported the state only."
	}
	return upperFirst(strings.Join(parts, "; ")) + "."
}

// DeadlockPair is the deadlocks between one pair of procedures (or ad hoc statements), whichever
// was the victim.
type DeadlockPair struct {
	Procedures          [2]string `json:"procedures"`
	Count               int       `json:"count"`
	Last                time.Time `json:"last"`
	Objects             []string  `json:"objects"` // the tables involved, distinct
	LastVictimStatement string    `json:"lastVictimStatement"`
	// LastLocks are the lock types of the last deadlock of the pair, for the finding's evidence.
	LastLocks []string `json:"-"`
}

// Sources of a deadlock report.
const (
	DeadlockSourceFile       = "file"
	DeadlockSourceRingBuffer = "ring-buffer"
)

// DeadlockDays is the window the report reads.
const DeadlockDays = 7

// DeadlockProcess is one side of a deadlock: its procedure (schema.name, "" when ad hoc), the
// statement it was running, the lock mode it wanted, and its database.
type DeadlockProcess struct {
	ID        string
	Procedure string
	Statement string
	LockMode  string
	Database  string
	Login     string
	Host      string
	App       string
}

// Label is how the report names this side: its procedure, or the start of its statement when ad hoc.
func (p DeadlockProcess) Label() string {
	if p.Procedure != "" {
		return p.Procedure
	}
	s := strings.Join(strings.Fields(p.Statement), " ")
	if s == "" {
		return "ad hoc (statement unknown)"
	}
	return "ad hoc: " + left(s, 100)
}

// DeadlockEvent is one xml_deadlock_report: when (UTC), the victim, the winner (the first other
// process), the objects (schema.table, or "hobt {id}" when the name is not in the graph), the
// lock types ("keylock dbo.T: held X, wanted U") and the database.
type DeadlockEvent struct {
	Time      time.Time
	Victim    DeadlockProcess
	Winner    DeadlockProcess
	Objects   []string
	LockModes []string
	Database  string
}

// DeadlockInputs is one read of the deadlocks: the database the report is for, when it was read,
// how long it took, the window, where it read from and the events found there.
type DeadlockInputs struct {
	Database   string
	CapturedAt time.Time
	Duration   time.Duration
	Days       int
	Source     string
	Events     []DeadlockEvent
	Health     []HealthEvent
}

// BuildDeadlockReport keeps the deadlocks of this database in the window, groups them by the pair
// of procedures (whichever was the victim), and states one DLK01 finding per pair: 10 or more in
// the window High, 3 or more Medium, else Low.
func BuildDeadlockReport(in DeadlockInputs) DeadlockReport {
	days := in.Days
	if days <= 0 {
		days = DeadlockDays
	}
	r := DeadlockReport{
		CapturedAt: in.CapturedAt.UTC(), DurationSeconds: math.Round(in.Duration.Seconds()*1000) / 1000,
		Days: days, Source: in.Source, Pairs: []DeadlockPair{}, Findings: []Finding{}, Health: []HealthEvent{},
	}
	since := in.CapturedAt.UTC().AddDate(0, 0, -days)
	for _, h := range in.Health {
		state := strings.ToLower(h.State)
		if h.At.Before(since) || (state != "warning" && state != "error") {
			continue
		}
		h.At, h.State = h.At.UTC(), state
		r.Health = append(r.Health, h)
	}
	sort.SliceStable(r.Health, func(i, j int) bool { return r.Health[i].At.After(r.Health[j].At) })
	if len(r.Health) > HealthEventsMax {
		r.Health = r.Health[:HealthEventsMax]
	}
	at := map[[2]string]int{}
	objects := map[[2]string]map[string]bool{}
	for _, e := range in.Events {
		if e.Time.Before(since) || !e.touches(in.Database) {
			continue
		}
		r.Total++
		k := [2]string{e.Victim.Label(), e.Winner.Label()}
		if k[1] < k[0] {
			k[0], k[1] = k[1], k[0]
		}
		i, ok := at[k]
		if !ok {
			i = len(r.Pairs)
			at[k] = i
			objects[k] = map[string]bool{}
			r.Pairs = append(r.Pairs, DeadlockPair{Procedures: k})
		}
		p := &r.Pairs[i]
		p.Count++
		for _, o := range e.Objects {
			objects[k][o] = true
		}
		if p.Count == 1 || !e.Time.Before(p.Last) {
			p.Last = e.Time.UTC()
			p.LastVictimStatement = left(strings.TrimSpace(e.Victim.Statement), 500)
			p.LastLocks = e.LockModes
		}
	}
	for i := range r.Pairs {
		p := &r.Pairs[i]
		p.Objects = []string{}
		for o := range objects[p.Procedures] {
			p.Objects = append(p.Objects, o)
		}
		sort.Strings(p.Objects)
	}
	sort.SliceStable(r.Pairs, func(i, j int) bool {
		a, b := r.Pairs[i], r.Pairs[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return a.Procedures[0]+a.Procedures[1] < b.Procedures[0]+b.Procedures[1]
	})

	chk, _ := CheckByID("DLK01")
	for _, p := range r.Pairs {
		sev := SeverityLow
		switch {
		case p.Count >= 10:
			sev = SeverityHigh
		case p.Count >= 3:
			sev = SeverityMedium
		}
		on := "objects not named in the graph"
		if len(p.Objects) > 0 {
			on = strings.Join(p.Objects, ", ")
		}
		locks := "not recorded"
		if len(p.LastLocks) > 0 {
			locks = strings.Join(p.LastLocks, "; ")
		}
		stmt := p.LastVictimStatement
		if stmt == "" {
			stmt = "?"
		}
		r.Findings = append(r.Findings, Finding{
			CheckID: chk.ID, Title: chk.Title, Severity: sev, Owner: chk.Owner, When: WhenDecideFirst, Effort: EffortModerate,
			Object:    p.Procedures[0] + " / " + p.Procedures[1],
			Finding:   fmt.Sprintf("%d deadlocks in %d days between %s and %s, on %s; the last was %s.", p.Count, days, p.Procedures[0], p.Procedures[1], on, utcText(p.Last)),
			Why:       wrk06Why,
			DoThis:    dlk01Do,
			ThenCheck: chk.ThenCheck,
			Evidence:  left("Lock types: "+locks+". Last victim statement: "+oneLine(stmt), 4000),
			Clock:     ClockWindow,
		})
	}
	RankFindings(r.Findings)
	return r
}

// touches says whether a deadlock happened in this database: either side's database is it.
func (e DeadlockEvent) touches(database string) bool {
	for _, d := range []string{e.Database, e.Victim.Database, e.Winner.Database} {
		if d != "" && strings.EqualFold(d, database) {
			return true
		}
	}
	return false
}
