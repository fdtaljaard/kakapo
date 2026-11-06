package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Readers and writers apart (docs/phase5m-contract.md, section 2): every blocked process report of the
// blocking history is put in one wait class, from the waiting side's lock mode and isolation level and
// the blocking side's statement and isolation level. Two of the classes are what READ COMMITTED
// SNAPSHOT removes; the row versioning decision (decision_rcsi.go) weighs them against BLK01's
// thresholds.

// Wait classes.
const (
	WaitWritesBehindReads  = "writes-behind-reads"
	WaitReadsBehindWrites  = "reads-behind-writes"
	WaitLockingReaders     = "locking-readers"
	WaitSchemaLocks        = "schema-locks"
	WaitWritesBehindWrites = "writes-behind-writes"
	WaitUnknown            = "unknown"
)

// WaitClassOrder is the order the classes are listed in: the two RCSI removes first, writes behind
// reads (the operational work that waits) at the head.
var WaitClassOrder = []string{WaitWritesBehindReads, WaitReadsBehindWrites, WaitLockingReaders, WaitSchemaLocks, WaitWritesBehindWrites, WaitUnknown}

// RemovedByRCSI says whether READ COMMITTED SNAPSHOT removes the waits of a class: readers at read
// committed stop taking shared locks, so neither waits for the other (Microsoft Learn, "Transaction
// locking and row versioning guide").
func RemovedByRCSI(class string) bool {
	return class == WaitWritesBehindReads || class == WaitReadsBehindWrites
}

// blkPartiesMax is how many waiting and blocking programs a removable class keeps.
const blkPartiesMax = 3

// BlockingClass is the waits of one class in the window. An episode is one waiting session (its spid and
// lastbatchstarted) behind one blocker transaction; its length is the longest wait reported for it.
type BlockingClass struct {
	Class          string `json:"class"`
	Episodes       int    `json:"episodes"`
	TotalWaitSec   int64  `json:"totalWaitSec"`
	LongestWaitSec int64  `json:"longestWaitSec"`
	// MostAtOnce is the most sessions of this class waiting in one snapshot or lock monitor pass
	// (BLK01's "10 sessions at once").
	MostAtOnce int                `json:"mostAtOnce"`
	Days       int                `json:"days"`  // server days with a wait of this class
	ByDay      []BlockingClassDay `json:"byDay"` // the total wait per server day, a wait across midnight split
	// Waiters and Blockers are the programs that wait most and block most, for the two classes RCSI
	// removes; left out for the others.
	Waiters  []BlockingParty `json:"waiters,omitempty"`
	Blockers []BlockingParty `json:"blockers,omitempty"`
}

// BlockingClassDay is a server day (2026-11-03) and the seconds waited in it.
type BlockingClassDay struct {
	Day     string `json:"day"`
	WaitSec int64  `json:"waitSec"`
}

// BlockingParty is a program as the report wrote it, with its login and host, and the waits it had a
// part in.
type BlockingParty struct {
	Program  string `json:"program"`
	Login    string `json:"login"`
	Host     string `json:"host"`
	Episodes int    `json:"episodes"`
	WaitSec  int64  `json:"waitSec"`
}

// Class returns the history's class by name; ok false when the history has no classes (read before
// phase 5m).
func (h *BlockingHistory) Class(name string) (BlockingClass, bool) {
	if h == nil {
		return BlockingClass{}, false
	}
	for _, c := range h.Classes {
		if c.Class == name {
			return c, true
		}
	}
	return BlockingClass{}, false
}

// readCommitted says whether an isolation level as the report writes it ("read committed (2)") is READ
// COMMITTED; keepsLocks whether it is REPEATABLE READ or SERIALIZABLE.
func readCommitted(iso string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(iso)), "read committed")
}

func keepsLocks(iso string) bool {
	s := strings.ToLower(strings.TrimSpace(iso))
	return strings.HasPrefix(s, "repeatable read") || strings.HasPrefix(s, "serializable")
}

// writeModes are the lock modes a write asks for.
var writeModes = map[string]bool{"U": true, "X": true, "IX": true, "SIX": true, "UIX": true}

// readerMode says whether a lock mode is what a reader at read committed asks for: S or IS.
func readerMode(mode string) bool {
	return strings.EqualFold(mode, "S") || strings.EqualFold(mode, "IS")
}

// ClassifyWait puts one blocked process report in its wait class (docs/phase5m-contract.md, section 2;
// DarlingData sp_HumanEventsBlockViewer.sql 3694-3729 reads the same lock mode and isolation level):
// a head-only report is unknown; a schema lock wanted is a schema lock; either side at REPEATABLE READ
// or SERIALIZABLE, or a key-range shared lock, is a reader that keeps its locks; S or IS wanted at read
// committed is a read behind a write; a write lock wanted behind a SELECT at read committed is a write
// behind a read, and behind a statement kakapo cannot read (a procedure call, or none) unknown; any
// other wait with both sides known is a write behind a write.
func ClassifyWait(r BlockedProcessReport) string {
	if r.HeadOnly() {
		return WaitUnknown
	}
	mode := strings.TrimSpace(r.Blocked.LockMode)
	switch {
	case mode == "":
		return WaitUnknown
	case strings.EqualFold(mode, "Sch-S") || strings.EqualFold(mode, "Sch-M"):
		return WaitSchemaLocks
	case keepsLocks(r.Blocked.Isolation) || keepsLocks(r.Blocking.Isolation) || strings.HasPrefix(strings.ToUpper(mode), "RANGES-"):
		return WaitLockingReaders
	case readerMode(mode) && readCommitted(r.Blocked.Isolation):
		return WaitReadsBehindWrites
	case writeModes[strings.ToUpper(mode)] && readCommitted(r.Blocking.Isolation):
		buf := strings.TrimSpace(r.Blocking.InputBuffer)
		if _, _, proc := ProcInputBuffer(buf); proc || buf == "" {
			return WaitUnknown
		}
		if LeadingKeyword(buf) == "SELECT" {
			return WaitWritesBehindReads
		}
	}
	return WaitWritesBehindWrites
}

// LeadingKeyword is the first keyword of a batch, upper case, after blank space, comments, semicolons, a
// parameter list ("(@P1 int)SELECT …", as a driver's sp_executesql call shows it) and the common table
// expressions of a WITH; "" when there is none.
func LeadingKeyword(s string) string {
	r := []rune(s)
	i := 0
	skip := func() {
		for i < len(r) {
			switch {
			case unicode.IsSpace(r[i]) || r[i] == ';':
				i++
			case r[i] == '-' && i+1 < len(r) && r[i+1] == '-':
				for i < len(r) && r[i] != '\n' {
					i++
				}
			case r[i] == '/' && i+1 < len(r) && r[i+1] == '*':
				depth := 0
				for i < len(r) {
					if r[i] == '/' && i+1 < len(r) && r[i+1] == '*' {
						depth++
						i += 2
					} else if r[i] == '*' && i+1 < len(r) && r[i+1] == '/' {
						depth--
						i += 2
						if depth == 0 {
							break
						}
					} else {
						i++
					}
				}
			default:
				return
			}
		}
	}
	// closeParen moves past the parenthesis at i and everything inside it, strings and brackets included.
	closeParen := func() {
		depth := 0
		for i < len(r) {
			switch r[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					i++
					return
				}
			case '\'':
				for i++; i < len(r) && r[i] != '\''; i++ {
				}
			case '[':
				for i++; i < len(r) && r[i] != ']'; i++ {
				}
			}
			i++
		}
	}
	word := func() string {
		start := i
		if i < len(r) && r[i] == '[' {
			for i < len(r) && r[i] != ']' {
				i++
			}
			i = min(i+1, len(r))
			return string(r[start:i])
		}
		for i < len(r) && (unicode.IsLetter(r[i]) || unicode.IsDigit(r[i]) || strings.ContainsRune("_@#$", r[i])) {
			i++
		}
		return strings.ToUpper(string(r[start:i]))
	}
	skip()
	if i+1 < len(r) && r[i] == '(' && r[i+1] == '@' {
		closeParen()
		skip()
	}
	w := word()
	if w != "WITH" {
		return w
	}
	// WITH name [(columns)] AS (body) [, name …], then the statement.
	for i < len(r) {
		skip()
		if word() == "" {
			return ""
		}
		skip()
		if i < len(r) && r[i] == '(' {
			closeParen()
			skip()
		}
		if word() != "AS" {
			return ""
		}
		skip()
		if i >= len(r) || r[i] != '(' {
			return ""
		}
		closeParen()
		skip()
		if i < len(r) && r[i] == ',' {
			i++
			continue
		}
		return word()
	}
	return ""
}

// waitEpisodeID is one waiting session behind one blocker transaction: the waiting spid and its
// lastbatchstarted, and the blocker's transaction (blkEpisodeID's), or "head" for a head-only report.
func waitEpisodeID(r BlockedProcessReport) string {
	id := fmt.Sprintf("%d/%s", r.Blocked.SPID, r.Blocked.LastBatchStarted.Format(time.RFC3339Nano))
	if r.HeadOnly() {
		return id + "/head"
	}
	return id + "/" + blkEpisodeID(blkEpisode{head: r.Blocking, snapshot: r.EventUTC})
}

// waitEpisode is one wait episode as the reports of the window show it: its class (from the report that
// waited longest), its length, when that report was (UTC), and who waited and who blocked.
type waitEpisode struct {
	class   string
	waitMs  int64
	at      time.Time
	waiter  BlockedProcess
	blocker BlockedProcess
}

// blockingWaitEpisodes reads the wait episodes of the reports, by id.
func blockingWaitEpisodes(bySnap map[time.Time][]BlockedProcessReport) map[string]*waitEpisode {
	out := map[string]*waitEpisode{}
	for _, reports := range bySnap {
		for _, r := range reports {
			id := waitEpisodeID(r)
			e := out[id]
			if e == nil || r.Blocked.WaitMs > e.waitMs || (r.Blocked.WaitMs == e.waitMs && r.EventUTC.After(e.at)) {
				out[id] = &waitEpisode{class: ClassifyWait(r), waitMs: r.Blocked.WaitMs, at: r.EventUTC.UTC(), waiter: r.Blocked, blocker: r.Blocking}
			}
		}
	}
	return out
}

// splitByDay spreads a wait that ended at end (on the server's clock) over the server days it covered,
// in milliseconds.
func splitByDay(end time.Time, waitMs int64, add func(day string, ms int64)) {
	if waitMs <= 0 {
		add(end.Format("2006-01-02"), 0)
		return
	}
	for t := end.Add(-time.Duration(waitMs) * time.Millisecond); t.Before(end); {
		upto := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, 1)
		if end.Before(upto) {
			upto = end
		}
		add(t.Format("2006-01-02"), upto.Sub(t).Milliseconds())
		t = upto
	}
}

// buildBlockingClasses states every class of the window from its wait episodes: the episodes, the total
// and longest wait, the most sessions at once, the server days and the wait per day, and for the two
// classes RCSI removes the programs that wait and block most.
func buildBlockingClasses(bySnap map[time.Time][]BlockedProcessReport, eps map[string]*waitEpisode, offset time.Duration) []BlockingClass {
	type partyAcc struct {
		p   BlockingParty
		ids map[string]bool
		ms  int64
	}
	type acc struct {
		c                 BlockingClass
		totalMs, longest  int64
		days              map[string]int64
		waiters, blockers map[string]*partyAcc
	}
	accs := map[string]*acc{}
	for _, cl := range WaitClassOrder {
		accs[cl] = &acc{c: BlockingClass{Class: cl}, days: map[string]int64{}, waiters: map[string]*partyAcc{}, blockers: map[string]*partyAcc{}}
	}
	party := func(m map[string]*partyAcc, p BlockedProcess, id string, ms int64) {
		k := p.Program + "\x00" + p.Login + "\x00" + p.Host
		a := m[k]
		if a == nil {
			a = &partyAcc{p: BlockingParty{Program: p.Program, Login: p.Login, Host: p.Host}, ids: map[string]bool{}}
			m[k] = a
		}
		a.ids[id] = true
		a.ms += ms
	}
	ids := make([]string, 0, len(eps))
	for id := range eps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := eps[id]
		a := accs[e.class]
		a.c.Episodes++
		a.totalMs += e.waitMs
		a.longest = max(a.longest, e.waitMs)
		splitByDay(e.at.Add(offset), e.waitMs, func(day string, ms int64) { a.days[day] += ms })
		if RemovedByRCSI(e.class) {
			party(a.waiters, e.waiter, id, e.waitMs)
			party(a.blockers, e.blocker, id, e.waitMs)
		}
	}
	// The most sessions of a class waiting in one snapshot or pass.
	for _, reports := range bySnap {
		at := map[string]map[int]bool{}
		for _, r := range reports {
			cl := eps[waitEpisodeID(r)].class
			if at[cl] == nil {
				at[cl] = map[int]bool{}
			}
			at[cl][r.Blocked.SPID] = true
		}
		for cl, s := range at {
			accs[cl].c.MostAtOnce = max(accs[cl].c.MostAtOnce, len(s))
		}
	}
	ranked := func(m map[string]*partyAcc) []BlockingParty {
		var out []BlockingParty // nil when none, as the stored JSON reads back
		for _, a := range m {
			p := a.p
			p.Episodes, p.WaitSec = len(a.ids), a.ms/1000
			out = append(out, p)
		}
		sort.Slice(out, func(i, j int) bool {
			a, b := out[i], out[j]
			switch {
			case a.WaitSec != b.WaitSec:
				return a.WaitSec > b.WaitSec
			case a.Episodes != b.Episodes:
				return a.Episodes > b.Episodes
			}
			return a.Program+a.Login+a.Host < b.Program+b.Login+b.Host
		})
		if len(out) > blkPartiesMax {
			out = out[:blkPartiesMax]
		}
		return out
	}
	out := make([]BlockingClass, 0, len(WaitClassOrder))
	for _, cl := range WaitClassOrder {
		a := accs[cl]
		c := a.c
		c.TotalWaitSec, c.LongestWaitSec = a.totalMs/1000, a.longest/1000
		c.ByDay = []BlockingClassDay{}
		for d, ms := range a.days {
			c.ByDay = append(c.ByDay, BlockingClassDay{Day: d, WaitSec: ms / 1000})
		}
		sort.Slice(c.ByDay, func(i, j int) bool { return c.ByDay[i].Day < c.ByDay[j].Day })
		c.Days = len(c.ByDay)
		if RemovedByRCSI(cl) {
			c.Waiters, c.Blockers = ranked(a.waiters), ranked(a.blockers)
		}
		out = append(out, c)
	}
	return out
}

// WaitText is a length of waiting as the decision writes it: "12 s", "4 min 5 s", "1 h 10 min".
func WaitText(sec int64) string {
	switch {
	case sec < 60:
		return fmt.Sprintf("%d s", sec)
	case sec < 3600:
		if sec%60 == 0 {
			return fmt.Sprintf("%d min", sec/60)
		}
		return fmt.Sprintf("%d min %d s", sec/60, sec%60)
	}
	return fmt.Sprintf("%d h %d min", sec/3600, sec%3600/60)
}

// BlockingClassLine is BLK01's line under its summary (docs/phase5m-contract.md, section 7): what RCSI
// would remove and what it would not; "" for a history read before phase 5m.
func BlockingClassLine(h *BlockingHistory) string {
	if h == nil || len(h.Classes) == 0 {
		return ""
	}
	w, _ := h.Class(WaitWritesBehindReads)
	r, _ := h.Class(WaitReadsBehindWrites)
	var not int64
	for _, cl := range []string{WaitLockingReaders, WaitSchemaLocks, WaitWritesBehindWrites} {
		c, _ := h.Class(cl)
		not += c.TotalWaitSec
	}
	return fmt.Sprintf("Waits RCSI would remove: %s (%s, %s). Waits it would not: %s.", WaitText(w.TotalWaitSec+r.TotalWaitSec),
		plural(int64(w.Episodes), "write behind reads", "writes behind reads"), plural(int64(r.Episodes), "read behind writes", "reads behind writes"),
		WaitText(not))
}
