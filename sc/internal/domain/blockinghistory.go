package domain

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Blocking history (docs/phase5d-contract.md, BLK01): a week of blocking from the system_health
// session. Every five minutes sp_server_diagnostics records the blocking it sees in its
// QUERY_PROCESSING component as blocked-process-report nodes; collect.ReadBlockingHistory reads them
// (sp_HumanEventsBlockViewer's rules) and this file groups them by the head blocker's statement and
// the object it held, classifies the head blocker as the live blocking page does (domain/blocking.go)
// and states one BLK01 finding per leader. A block shorter than the five-minute snapshot is missed,
// and the report says so (MissedNote).
//
// Phase 5e (docs/phase5e-contract.md): when kakapo's event session exists, its files come first. Its
// blocked_process_report fires at every threshold interval while a block lasts, so every block over the
// threshold is there with its real duration; system_health's snapshots fill only the days before the
// session existed (KakapoSince), and the report says which source each day came from.

// BlockingHistoryDays is the window the read takes by default, and its most.
const BlockingHistoryDays = 7

// blkThresholdAdvised is the blocked process threshold kakapo's own session (phase 5e) would use: the
// value sp_HumanEvents sets (DarlingData sp_HumanEvents.sql, "blocked process threshold", 5 seconds).
const blkThresholdAdvised = 5

// blkLeadersMax is how many leaders a report keeps.
const blkLeadersMax = 20

// BlockCrossDatabase is a head blocker working in another database than the one the report is for.
const BlockCrossDatabase = "cross-database"

// Blk01NotRun is BLK01's note in the checks until the blocking history is read on request.
const Blk01NotRun = "Not run yet. Read blocking history reads the system_health session's files for the last 7 days; it can take a minute on a busy server."

// BlockedProcess is one side of a blocked process report, as the system_health session recorded it.
// The times are the server's local clock. Procedure, Object and QueryHash are what collect resolved in
// the database: the procedure behind an input buffer "Proc [Database Id = 7 Object Id = 1234]", the
// table behind the wait resource, and Query Store's hash for the input buffer's exact text.
type BlockedProcess struct {
	SPID             int
	ECID             int
	Login            string
	Host             string
	Program          string
	Database         string // currentdbname
	DatabaseID       int
	Status           string // running, suspended, sleeping, ...
	Isolation        string // "read committed (2)", as the report writes it
	TranCount        int
	TranName         string // the blocked side's transaction name; implicit_transaction when IMPLICIT_TRANSACTIONS opened it
	XactID           int64  // the blocking side's transaction id
	ClientOption1    int64  // the SET options; 2 is IMPLICIT_TRANSACTIONS
	WaitMs           int64
	WaitResource     string // "KEY: 7:72057594043039744 (8194443284a0)"
	LockMode         string
	LastTranStarted  time.Time
	LastBatchStarted time.Time
	InputBuffer      string
	Procedure        string
	Object           string
	QueryHash        string
}

// BlockedProcessReport is one blocked-process-report node of a system_health snapshot: when (UTC), the
// lock monitor's loop, the session that waits and the one it waits for. A head-only report has no
// blocker (Blocking.SPID 0): system_health wrote the head of a chain, a session holding an open
// transaction while it waits on something else, as a blocked-process with trancount > 0 and an empty
// blocking-process, and often left the sessions behind it out of the snapshot.
type BlockedProcessReport struct {
	EventUTC    time.Time
	MonitorLoop int64
	Blocked     BlockedProcess
	Blocking    BlockedProcess
	// Source is BlockSourceKakapo for a report of kakapo's session; "" for system_health's.
	Source string
}

// BlockSourceKakapo marks a report read from kakapo's session files, and the history source when they
// cover the whole window.
const BlockSourceKakapo = "kakapo"

// KakapoSource is the history's (and the deadlock report's) source when kakapo's files were read:
// "kakapo" when they cover the whole window, else "kakapo+" and system_health's source (file,
// ring-buffer) for the days before.
func KakapoSource(systemHealth string, coversWindow bool) string {
	if coversWindow || systemHealth == "" {
		return BlockSourceKakapo
	}
	return BlockSourceKakapo + "+" + systemHealth
}

// FromKakapo says whether a source includes kakapo's session files.
func FromKakapo(source string) bool { return strings.HasPrefix(source, BlockSourceKakapo) }

// HeadOnly says whether the report names a head of a chain without a blocker.
func (r BlockedProcessReport) HeadOnly() bool { return r.Blocking.SPID == 0 }

// Holder is the session that holds the locks in the report: the blocker, or the head itself in a
// head-only report.
func (r BlockedProcessReport) Holder() BlockedProcess {
	if r.HeadOnly() {
		return r.Blocked
	}
	return r.Blocking
}

// BlockingHistoryInputs is one read of the history: the database it is for (and its id), when it was
// read and how long it took, the window, where it read from, the server clock's offset from UTC in
// minutes, the blocked process threshold, whether the database has READ COMMITTED SNAPSHOT, the reports
// and what could not be read.
type BlockingHistoryInputs struct {
	Database         string
	DatabaseID       int
	CapturedAt       time.Time
	Duration         time.Duration
	Days             int
	Source           string // file, ring-buffer
	OffsetMinutes    int
	ThresholdSeconds int
	RCSI             bool
	Reports          []BlockedProcessReport
	Notes            []string
	// KakapoSince is when kakapo's session files begin (UTC): the session's start or its first report,
	// whichever is earlier; nil when they were not read. system_health's reports from then on are left
	// out (kakapo's have them all).
	KakapoSince *time.Time
	// Helper is kakapo's session as read before the history (nil when it could not be read): without
	// the session, BLK01's Do this ends with installing it and the finding carries the fix.
	Helper *XESession
}

// BlockingHistory is BLK01's report (POST .../cards/performance/blocking-history), stored as JSON.
type BlockingHistory struct {
	CapturedAt      time.Time `json:"capturedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	Days            int       `json:"days"`
	Source          string    `json:"source"`       // file, ring-buffer, kakapo, kakapo+file, kakapo+ring-buffer
	Snapshots       int       `json:"snapshots"`    // snapshots (system_health) and lock monitor passes (kakapo) that saw blocking here
	ReportsTotal    int       `json:"reportsTotal"` // blocked-process reports in this database
	MissedNote      string    `json:"missedNote"`
	// ThresholdSeconds is the blocked process threshold (s) from sys.configurations; 0 means only the
	// five-minute snapshots exist.
	ThresholdSeconds int              `json:"thresholdSeconds"`
	Leaders          []BlockingLeader `json:"leaders"`  // most occurrences first, at most 20
	ByHour           []BlockingHour   `json:"byHour"`   // every hour of the window, server time, oldest first
	Findings         []Finding        `json:"findings"` // one BLK01 finding per leader, ranked
	Notes            []string         `json:"notes"`
	// KakapoSince is when kakapo's session files begin (UTC); left out when they were not read.
	KakapoSince *time.Time `json:"kakapoSince,omitempty"`
	// DaySources says where each server day of the window came from (kakapo, system_health, or both on
	// the day the session began); left out when kakapo's files were not read.
	DaySources []BlockingDaySource `json:"daySources,omitempty"`
}

// BlockingDaySource is a server day (2026-11-03) and where its blocking was read from.
type BlockingDaySource struct {
	Day    string `json:"day"`
	Source string `json:"source"` // kakapo, system_health, both
}

// The sources of a day.
const (
	DaySourceKakapo       = "kakapo"
	DaySourceSystemHealth = "system_health"
	DaySourceBoth         = "both"
)

// BlockingLeader is the blocking by one head blocker's statement on one object, in the window.
type BlockingLeader struct {
	Key              string `json:"key"`
	BlockerStatement string `json:"blockerStatement"` // normalised: whitespace collapsed, literals as ?
	Procedure        string `json:"procedure,omitempty"`
	BlockerProgram   string `json:"blockerProgram"`
	BlockerLogin     string `json:"blockerLogin"`
	BlockerHost      string `json:"blockerHost"`
	Object           string `json:"object"`
	Database         string `json:"database"`
	// Occurrences are the distinct blocking episodes: one blocker's transaction (session and
	// transaction id) seen in one or more snapshots counts once.
	Occurrences int `json:"occurrences"`
	// Snapshots are the five-minute system_health snapshots (one lock monitor loop each) that saw it.
	Snapshots int `json:"snapshots"`
	// Passes are the lock monitor passes kakapo's session reported it in (one every threshold interval
	// while it lasted).
	Passes                 int           `json:"passes,omitempty"`
	LongestWaitSec         int64         `json:"longestWaitSec"`
	TotalBlockedSessions   int           `json:"totalBlockedSessions"` // distinct sessions behind it, per episode
	Kinds                  []string      `json:"kinds"`
	First                  time.Time     `json:"first"`
	Last                   time.Time     `json:"last"`
	ByDay                  []BlockingDay `json:"byDay"` // occurrences per server day
	SampleVictimStatement  string        `json:"sampleVictimStatement"`
	SampleBlockerStatement string        `json:"sampleBlockerStatement"`
	LockMode               string        `json:"lockMode,omitempty"` // the lock the longest-waiting victim wanted
	QueryHash              string        `json:"queryHash,omitempty"`

	// MostAtOnce is the most sessions behind it in one snapshot (BLK01's High threshold).
	MostAtOnce int `json:"-"`
	// HeldWhileWaiting: system_health wrote it as the head of a chain, holding an open transaction while
	// it waited on something else (BLK01 says so, and its wait is the longest).
	HeldWhileWaiting bool `json:"heldWhileWaiting,omitempty"`
}

// BlockingDay is a server day (2026-11-03) and how many occurrences began on it.
type BlockingDay struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

// BlockingHour is one hour of the window on the server's clock ("2026-11-03T14:00"): the blocked-process
// reports in it and the most sessions blocked in one snapshot of it.
type BlockingHour struct {
	Hour            string `json:"hour"`
	Reports         int    `json:"reports"`
	SessionsBlocked int    `json:"sessionsBlocked"`
}

// MissedNote is what the history cannot show without kakapo's session, by the blocked process threshold.
func MissedNote(thresholdSeconds int) string {
	if thresholdSeconds <= 0 {
		return fmt.Sprintf("system_health looks every five minutes, so blocks shorter than that are not in this history; "+
			"the longest and most repeated ones are. kakapo's event session (Installed helpers on the instance page) "+
			"keeps every block over %d seconds.", blkThresholdAdvised)
	}
	return fmt.Sprintf("The blocked process threshold is %d s, but nothing kakapo reads records those reports; kakapo's event session (Installed helpers on the instance page) would.", thresholdSeconds)
}

// KakapoMissedNote is what the history says when kakapo's session files were read: every block over
// the threshold since the session began (on the server's clock), and system_health for the days before
// when the window starts earlier.
func KakapoMissedNote(thresholdSeconds int, since, windowStart time.Time, offset time.Duration) string {
	if thresholdSeconds <= 0 {
		return "kakapo's event session runs, but the blocked process threshold is 0 (off), so it records no block, only deadlocks; " +
			"Remove and Create it again (Installed helpers on the instance page) to set the threshold."
	}
	s := fmt.Sprintf("kakapo's session keeps every block over %d s since %s", thresholdSeconds, since.Add(offset).Format("2006-01-02"))
	if since.After(windowStart) {
		s += "; the days before come from system_health's five-minute snapshots, which miss shorter blocks"
	}
	return s + "."
}

// blk01Install is what BLK01's Do this ends with on a server without kakapo's session.
const blk01Install = "Install kakapo's session to keep every block over 5 seconds (Installed helpers on the instance page)."

// kakapoPasses gives kakapo's reports of one lock monitor pass one time, the pass's first report:
// the pass reports each blocked session separately, milliseconds apart, where system_health writes a
// snapshot's reports with one time. A loop number seen again more than a minute later (the counter
// restarts with SQL Server) is another pass.
func kakapoPasses(reports []BlockedProcessReport) []BlockedProcessReport {
	out := append([]BlockedProcessReport(nil), reports...)
	idx := []int{}
	for i, r := range out {
		if r.Source == BlockSourceKakapo {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool { return out[idx[a]].EventUTC.Before(out[idx[b]].EventUTC) })
	start := map[int64]time.Time{}
	for _, i := range idx {
		t := out[i].EventUTC.UTC()
		if s, ok := start[out[i].MonitorLoop]; ok && t.Sub(s) <= time.Minute {
			out[i].EventUTC = s
			continue
		}
		start[out[i].MonitorLoop] = t
		out[i].EventUTC = t
	}
	return out
}

// daySources says where each server day of the window came from: start, end and since on the server's
// clock.
func daySources(start, end, since time.Time) []BlockingDaySource {
	out := []BlockingDaySource{}
	cut := since.Format("2006-01-02")
	for d := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC); !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		src := DaySourceKakapo
		switch {
		case day < cut:
			src = DaySourceSystemHealth
		case day == cut && since.After(start):
			src = DaySourceBoth
		}
		out = append(out, BlockingDaySource{Day: day, Source: src})
	}
	return out
}

// BlockingHistoryHint is what the live blocking sentences (the blocking page, the sample) gain once the
// week's history has been read.
func BlockingHistoryHint(h *BlockingHistory) string {
	if h == nil {
		return ""
	}
	return fmt.Sprintf("For what blocked users before now, see the week's history on the card (read %s).", utcText(h.CapturedAt))
}

// AddHistoryHint ends the blocking page's sentences with the pointer to the week's history.
func (t *BlockingTree) AddHistoryHint(h *BlockingHistory) {
	if s := BlockingHistoryHint(h); s != "" {
		t.Explain = append(t.Explain, s)
	}
}

// AddHistoryHint ends the sample's last blocking sentence with the pointer to the week's history.
func (p *PerfSample) AddHistoryHint(h *BlockingHistory) {
	s := BlockingHistoryHint(h)
	if s == "" {
		return
	}
	for i := len(p.Lines) - 1; i >= 0; i-- {
		if p.Lines[i].Area == SampleAreaBlocking {
			p.Lines[i].Text += " " + s
			return
		}
	}
}

// blkEpisode is one head blocker in one snapshot: who, the sessions behind it, and the object.
type blkEpisode struct {
	snapshot time.Time // UTC
	kakapo   bool      // a lock monitor pass of kakapo's session, not a system_health snapshot
	head     BlockedProcess
	object   string
	victims  map[int]bool
	longest  int64
	sample   BlockedProcess // the direct victim that waited longest
	headOnly bool           // system_health wrote the head itself: it holds a transaction while it waits
	local    time.Time      // the snapshot on the server's clock
}

// BuildBlockingHistory keeps the reports of this database in the window, attributes every blocked
// session to the head of its chain in each snapshot, groups the heads by their statement and the object
// their direct victims waited on, and states one BLK01 finding per leader: Medium from 2 occurrences,
// High from 10, or a wait of 60 s, or 10 sessions in one snapshot; Info for one occurrence.
func BuildBlockingHistory(in BlockingHistoryInputs) BlockingHistory {
	days := in.Days
	if days <= 0 || days > BlockingHistoryDays {
		days = BlockingHistoryDays
	}
	offset := time.Duration(in.OffsetMinutes) * time.Minute
	h := BlockingHistory{
		CapturedAt: in.CapturedAt.UTC(), DurationSeconds: math.Round(in.Duration.Seconds()*1000) / 1000,
		Days: days, Source: in.Source, ThresholdSeconds: in.ThresholdSeconds, MissedNote: MissedNote(in.ThresholdSeconds),
		Leaders: []BlockingLeader{}, ByHour: []BlockingHour{}, Findings: []Finding{}, Notes: []string{},
	}
	h.Notes = append(h.Notes, in.Notes...)
	since := h.CapturedAt.AddDate(0, 0, -days)

	// kakapo's files first: from the time they begin, system_health's snapshots are left out.
	reports := kakapoPasses(in.Reports)
	var cut *time.Time
	if in.KakapoSince != nil {
		c := in.KakapoSince.UTC()
		for _, r := range reports {
			if r.Source == BlockSourceKakapo && r.EventUTC.Before(c) {
				c = r.EventUTC.UTC()
			}
		}
		cut = &c
		h.KakapoSince = &c
		h.MissedNote = KakapoMissedNote(in.ThresholdSeconds, c, since, offset)
		h.DaySources = daySources(since.Add(offset), h.CapturedAt.Add(offset), c.Add(offset))
	}

	// The reports of this database in the window, by snapshot.
	bySnap := map[time.Time][]BlockedProcessReport{}
	for _, r := range reports {
		if r.EventUTC.Before(since) || !r.inDatabase(in.Database, in.DatabaseID) {
			continue
		}
		if cut != nil && r.Source != BlockSourceKakapo && !r.EventUTC.Before(*cut) {
			continue
		}
		t := r.EventUTC.UTC()
		bySnap[t] = append(bySnap[t], r)
		h.ReportsTotal++
	}
	h.Snapshots = len(bySnap)

	// The heat line: every hour of the window on the server's clock.
	hours := map[string]*BlockingHour{}
	start := h.CapturedAt.Add(offset).Add(-time.Duration(days) * 24 * time.Hour).Truncate(time.Hour)
	end := h.CapturedAt.Add(offset).Truncate(time.Hour)
	for t := start; !t.After(end); t = t.Add(time.Hour) {
		h.ByHour = append(h.ByHour, BlockingHour{Hour: t.Format("2006-01-02T15:04")})
	}
	for i := range h.ByHour {
		hours[h.ByHour[i].Hour] = &h.ByHour[i]
	}

	snaps := make([]time.Time, 0, len(bySnap))
	for t := range bySnap {
		snaps = append(snaps, t)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Before(snaps[j]) })

	episodes := []blkEpisode{}
	for _, t := range snaps {
		reports := bySnap[t]
		local := t.Add(offset)
		blocked := map[int]map[int]bool{}
		for _, r := range reports {
			if r.HeadOnly() {
				continue // the head itself, not a session blocked by it
			}
			if blocked[r.Blocked.SPID] == nil {
				blocked[r.Blocked.SPID] = map[int]bool{}
			}
			blocked[r.Blocked.SPID][r.Blocking.SPID] = true
		}
		if hr := hours[local.Truncate(time.Hour).Format("2006-01-02T15:04")]; hr != nil {
			hr.Reports += len(reports)
			hr.SessionsBlocked = max(hr.SessionsBlocked, len(blocked))
		}
		eps := blkSnapshotEpisodes(t, local, reports)
		for i := range eps {
			eps[i].kakapo = reports[0].Source == BlockSourceKakapo
		}
		episodes = append(episodes, eps...)
	}

	h.Leaders = blkLeaders(episodes, in.Database)
	if len(h.Leaders) > blkLeadersMax {
		h.Notes = append(h.Notes, fmt.Sprintf("%d more blockers were seen less often and are not listed.", len(h.Leaders)-blkLeadersMax))
		h.Leaders = h.Leaders[:blkLeadersMax]
	}
	install := in.Helper != nil && !in.Helper.Exists
	for _, l := range h.Leaders {
		x := blk01Finding(l, days, offset, in.RCSI)
		if install {
			x.DoThis += " " + blk01Install
			x.FixID = FixInstallXESession
		}
		h.Findings = append(h.Findings, x)
	}
	RankFindings(h.Findings)
	return h
}

// inDatabase says whether a report belongs to this database: the waiting session works in it, or the
// resource it waits on is in it.
func (r BlockedProcessReport) inDatabase(name string, id int) bool {
	if r.Blocked.Database != "" && strings.EqualFold(r.Blocked.Database, name) {
		return true
	}
	if id > 0 && r.Blocked.DatabaseID == id && r.Blocked.Database == "" {
		return true
	}
	db, _ := WaitResourceDatabase(r.Blocked.WaitResource)
	return id > 0 && db == id
}

// blkSnapshotEpisodes attributes each blocked session of one snapshot to the head of its chain: the
// blocker that waits for nobody in the snapshot. A circle (sessions waiting for each other) is cut at
// the session first met. A head-only report (no blocker; the head holds a transaction while it waits on
// something else) is an episode of its own, with the head's own attributes and wait; the reports that
// name it as their blocker in the same snapshot join that episode, and they alone count as sessions
// behind it.
func blkSnapshotEpisodes(t, local time.Time, reports []BlockedProcessReport) []blkEpisode {
	waitsFor := map[int]int{}
	heads := map[int]BlockedProcess{}
	for _, r := range reports {
		if r.HeadOnly() {
			if _, dup := heads[r.Blocked.SPID]; !dup {
				heads[r.Blocked.SPID] = r.Blocked
			}
			continue
		}
		if _, dup := waitsFor[r.Blocked.SPID]; !dup && r.Blocking.SPID != r.Blocked.SPID {
			waitsFor[r.Blocked.SPID] = r.Blocking.SPID
		}
	}
	headOf := func(spid int) int {
		seen := map[int]bool{spid: true}
		for {
			next, ok := waitsFor[spid]
			if !ok || seen[next] {
				return spid
			}
			seen[next] = true
			spid = next
		}
	}
	at := map[int]int{}
	out := []blkEpisode{}
	episode := func(head int) *blkEpisode {
		i, ok := at[head]
		if !ok {
			i = len(out)
			at[head] = i
			out = append(out, blkEpisode{snapshot: t, local: local, victims: map[int]bool{}})
		}
		return &out[i]
	}
	for _, r := range reports {
		if r.HeadOnly() {
			continue
		}
		head := headOf(r.Blocking.SPID)
		e := episode(head)
		e.victims[r.Blocked.SPID] = true
		e.longest = max(e.longest, r.Blocked.WaitMs)
		if r.Blocking.SPID == head {
			if e.head.SPID == 0 {
				e.head = r.Blocking
			}
			if e.sample.SPID == 0 || r.Blocked.WaitMs > e.sample.WaitMs {
				e.sample = r.Blocked
			}
		}
	}
	// The heads system_health wrote on their own. One that waits for another session in this snapshot is
	// not a head: its blocker's chain counts it.
	spids := make([]int, 0, len(heads))
	for spid := range heads {
		spids = append(spids, spid)
	}
	sort.Ints(spids)
	for _, spid := range spids {
		if _, waits := waitsFor[spid]; waits {
			continue
		}
		hp := heads[spid]
		e := episode(spid)
		if hp.XactID == 0 {
			hp.XactID = e.head.XactID // the same transaction, as its victims' reports name it
		}
		e.head, e.headOnly = hp, true
		e.longest = max(e.longest, hp.WaitMs)
	}
	// The object: the one most of the head's direct victims wait on; "" when none was recorded.
	for i := range out {
		e := &out[i]
		count := map[string]int{}
		best := ""
		for _, r := range reports {
			if r.HeadOnly() || r.Blocking.SPID != e.head.SPID || e.head.SPID == 0 {
				continue
			}
			o := blkObject(r.Blocked)
			count[o]++
			if best == "" || count[o] > count[best] {
				best = o
			}
		}
		e.object = best
	}
	// A head never seen as a blocking process (only possible in a circle) is left out.
	kept := out[:0]
	for _, e := range out {
		if e.head.SPID != 0 {
			kept = append(kept, e)
		}
	}
	return kept
}

// blkObject is the table a victim waited on, or its wait resource without the key hash when it could
// not be named.
func blkObject(p BlockedProcess) string {
	if p.Object != "" {
		return p.Object
	}
	r := strings.TrimSpace(p.WaitResource)
	if i := strings.Index(r, " ("); i > 0 {
		r = r[:i]
	}
	return r
}

// blkEpisodeID is one blocker's transaction: its session and transaction id, else its session and the
// start of its transaction or of its batch, else the snapshot alone.
func blkEpisodeID(e blkEpisode) string {
	switch {
	case e.head.XactID != 0:
		return fmt.Sprintf("%d/x%d", e.head.SPID, e.head.XactID)
	case !e.head.LastTranStarted.IsZero():
		return fmt.Sprintf("%d/t%s", e.head.SPID, e.head.LastTranStarted.Format(time.RFC3339Nano))
	case !e.head.LastBatchStarted.IsZero():
		return fmt.Sprintf("%d/b%s", e.head.SPID, e.head.LastBatchStarted.Format(time.RFC3339Nano))
	}
	return fmt.Sprintf("%d/s%s", e.head.SPID, e.snapshot.Format(time.RFC3339Nano))
}

// blkLeaders groups the episodes by statement and object. A head seen with no session recorded behind
// it has no object; it joins the leader of the same statement when that statement blocked on exactly
// one object in the window, so one blocker is one leader whether or not a snapshot listed its victims.
func blkLeaders(episodes []blkEpisode, database string) []BlockingLeader {
	type acc struct {
		l         BlockingLeader
		episodes  map[string]time.Time // episode id -> first sighting (server clock)
		sessions  map[string]bool      // episode id / victim
		snapshots map[time.Time]bool
		passes    map[time.Time]bool
		kinds     map[string]bool
	}
	objects := map[string]map[string]bool{}
	for _, e := range episodes {
		if e.object == "" {
			continue
		}
		stmt, _ := BlockerStatement(e.head)
		if objects[stmt] == nil {
			objects[stmt] = map[string]bool{}
		}
		objects[stmt][e.object] = true
	}
	byKey := map[string]*acc{}
	order := []string{}
	for _, e := range episodes {
		stmt, proc := BlockerStatement(e.head)
		if e.object == "" && len(objects[stmt]) == 1 {
			for o := range objects[stmt] {
				e.object = o
			}
		}
		key := stmt + "|" + e.object
		a := byKey[key]
		if a == nil {
			a = &acc{l: BlockingLeader{Key: key, BlockerStatement: stmt, Procedure: proc, Object: e.object},
				episodes: map[string]time.Time{}, sessions: map[string]bool{}, snapshots: map[time.Time]bool{}, passes: map[time.Time]bool{},
				kinds: map[string]bool{}}
			byKey[key] = a
			order = append(order, key)
		}
		l := &a.l
		id := blkEpisodeID(e)
		if first, ok := a.episodes[id]; !ok || e.local.Before(first) {
			a.episodes[id] = e.local
		}
		for v := range e.victims {
			a.sessions[fmt.Sprintf("%s/%d", id, v)] = true
		}
		if e.kakapo {
			a.passes[e.snapshot] = true
		} else {
			a.snapshots[e.snapshot] = true
		}
		for _, k := range blkHistoryKinds(e.head, e.local, database, e.headOnly) {
			a.kinds[k] = true
		}
		l.HeldWhileWaiting = l.HeldWhileWaiting || e.headOnly
		l.LongestWaitSec = max(l.LongestWaitSec, e.longest/1000)
		l.MostAtOnce = max(l.MostAtOnce, len(e.victims))
		if l.First.IsZero() || e.snapshot.Before(l.First) {
			l.First = e.snapshot
		}
		if !e.snapshot.Before(l.Last) {
			// The latest sighting names who, the samples and the lock; a sighting without a victim keeps
			// the last victim seen.
			l.Last = e.snapshot
			l.BlockerProgram, l.BlockerLogin, l.BlockerHost, l.Database = e.head.Program, e.head.Login, e.head.Host, e.head.Database
			l.SampleBlockerStatement = left(strings.TrimSpace(e.head.InputBuffer), 1000)
			if e.sample.SPID != 0 {
				l.SampleVictimStatement = left(strings.TrimSpace(e.sample.InputBuffer), 1000)
				l.LockMode = e.sample.LockMode
			}
			if e.head.QueryHash != "" {
				l.QueryHash = e.head.QueryHash
			}
		}
		if l.SampleVictimStatement == "" && e.sample.SPID != 0 {
			l.SampleVictimStatement = left(strings.TrimSpace(e.sample.InputBuffer), 1000)
			l.LockMode = e.sample.LockMode
		}
		if l.QueryHash == "" && e.head.QueryHash != "" {
			l.QueryHash = e.head.QueryHash
		}
	}
	out := make([]BlockingLeader, 0, len(order))
	for _, key := range order {
		a := byKey[key]
		l := a.l
		l.Occurrences, l.Snapshots, l.Passes, l.TotalBlockedSessions = len(a.episodes), len(a.snapshots), len(a.passes), len(a.sessions)
		l.Kinds = []string{}
		for _, k := range blkHistoryKindOrder {
			if a.kinds[k] {
				l.Kinds = append(l.Kinds, k)
			}
		}
		perDay := map[string]int{}
		for _, first := range a.episodes {
			perDay[first.Format("2006-01-02")]++
		}
		l.ByDay = []BlockingDay{}
		for d, n := range perDay {
			l.ByDay = append(l.ByDay, BlockingDay{Day: d, Count: n})
		}
		sort.Slice(l.ByDay, func(i, j int) bool { return l.ByDay[i].Day < l.ByDay[j].Day })
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.Occurrences != b.Occurrences:
			return a.Occurrences > b.Occurrences
		case a.TotalBlockedSessions != b.TotalBlockedSessions:
			return a.TotalBlockedSessions > b.TotalBlockedSessions
		case a.LongestWaitSec != b.LongestWaitSec:
			return a.LongestWaitSec > b.LongestWaitSec
		case !a.Last.Equal(b.Last):
			return a.Last.After(b.Last)
		}
		return a.Key < b.Key
	})
	return out
}

// blkHistoryKindOrder is the order the kinds are listed in.
var blkHistoryKindOrder = []string{BlockSleeping, BlockImplicit, BlockSerializable, BlockRepeatableRead, BlockLongRunning, BlockCrossDatabase}

// blkHistoryKinds classifies a head blocker as the live blocking page does (blkKinds; DarlingData
// sp_HumanEventsBlockViewer.sql's sleeping, implicit transaction, serializable and repeatable read
// findings), from what the report recorded: sleeping with a transaction open; implicit transactions
// (the transaction name, or IMPLICIT_TRANSACTIONS in clientoption1, bit 2); the isolation level; a
// batch running 30 seconds or more at the snapshot (not for a head system_health wrote on its own, whose
// batch waits rather than runs); a blocker working in another database.
func blkHistoryKinds(p BlockedProcess, at time.Time, database string, headOnly bool) []string {
	k := []string{}
	sleeping := strings.EqualFold(p.Status, "sleeping")
	if sleeping && p.TranCount > 0 {
		k = append(k, BlockSleeping)
	}
	if p.TranName == "implicit_transaction" || p.ClientOption1&2 == 2 {
		k = append(k, BlockImplicit)
	}
	iso := strings.ToLower(p.Isolation)
	switch {
	case strings.HasPrefix(iso, "serializable"):
		k = append(k, BlockSerializable)
	case strings.HasPrefix(iso, "repeatable read"):
		k = append(k, BlockRepeatableRead)
	}
	if !headOnly && !sleeping && !p.LastBatchStarted.IsZero() && at.Sub(p.LastBatchStarted) >= blkLongRunningSec*time.Second {
		k = append(k, BlockLongRunning)
	}
	if p.Database != "" && database != "" && !strings.EqualFold(p.Database, database) {
		k = append(k, BlockCrossDatabase)
	}
	return k
}

// procInputBuffer is the input buffer of a procedure call over RPC.
var procInputBuffer = regexp.MustCompile(`^Proc \[Database Id = (\d+) Object Id = (\d+)\]$`)

// execStatement is a batch that executes a procedure: EXEC dbo.Proc @a = 1.
var execStatement = regexp.MustCompile(`(?i)^(?:EXEC|EXECUTE)\s+(?:@\w+\s*=\s*)?((?:\[[^\]]+\]|[\w#$@]+)(?:\.(?:\[[^\]]+\]|[\w#$@]+)){0,3})`)

// BlockerStatement is how a blocker is grouped and named: the procedure collect resolved (or the one a
// batch executes) and the normalised input buffer; an RPC call kakapo could not name keeps its ids.
func BlockerStatement(p BlockedProcess) (statement, procedure string) {
	buf := strings.Join(strings.Fields(p.InputBuffer), " ")
	if p.Procedure != "" {
		return p.Procedure, p.Procedure
	}
	if procInputBuffer.MatchString(buf) {
		return buf, ""
	}
	stmt := NormalizeStatement(buf)
	if m := execStatement.FindStringSubmatch(stmt); m != nil {
		procedure = strings.NewReplacer("[", "", "]", "").Replace(m[1])
	}
	return stmt, procedure
}

// NormalizeStatement collapses whitespace and replaces the literals of a statement by ?: strings (N'..',
// with doubled quotes inside), numbers and 0x binaries. Names in brackets or double quotes, variables and the digits
// inside names stay.
func NormalizeStatement(s string) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	var b strings.Builder
	identChar := func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) || strings.ContainsRune("_@#$", c) }
	for i := 0; i < len(r); i++ {
		c := r[i]
		prevIdent := i > 0 && identChar(r[i-1])
		switch {
		case c == '[' || c == '"':
			end := ']'
			if c == '"' {
				end = '"'
			}
			j := i + 1
			for j < len(r) && r[j] != end {
				j++
			}
			b.WriteString(string(r[i:min(j+1, len(r))]))
			i = j
		case c == '\'' || (c == 'N' || c == 'n') && !prevIdent && i+1 < len(r) && r[i+1] == '\'':
			j := i
			if c != '\'' {
				j++
			}
			j++
			for j < len(r) {
				if r[j] == '\'' {
					if j+1 < len(r) && r[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteRune('?')
			i = j
		case unicode.IsDigit(c) && !prevIdent:
			j := i
			if c == '0' && i+1 < len(r) && (r[i+1] == 'x' || r[i+1] == 'X') {
				j += 2
				for j < len(r) && strings.ContainsRune("0123456789abcdefABCDEF", r[j]) {
					j++
				}
			} else {
				for j < len(r) && (unicode.IsDigit(r[j]) || r[j] == '.') {
					j++
				}
				if j < len(r) && (r[j] == 'e' || r[j] == 'E') {
					k := j + 1
					if k < len(r) && (r[k] == '+' || r[k] == '-') {
						k++
					}
					if k < len(r) && unicode.IsDigit(r[k]) {
						for k < len(r) && unicode.IsDigit(r[k]) {
							k++
						}
						j = k
					}
				}
			}
			b.WriteRune('?')
			i = j - 1
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// WaitResourceDatabase reads a wait resource as the blocked process report writes it: its database id
// and the key collect resolves the object by ("h:{hobt}" for KEY, "o:{object}" for OBJECT and TAB,
// "p:{file}:{page}" for PAGE and RID; "" for any other resource).
func WaitResourceDatabase(resource string) (dbID int, key string) {
	kind, rest, ok := strings.Cut(strings.TrimSpace(resource), ":")
	if !ok {
		return 0, ""
	}
	rest = strings.TrimSpace(rest)
	if i := strings.IndexAny(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 {
		return 0, ""
	}
	if _, err := fmt.Sscan(parts[0], &dbID); err != nil {
		return 0, ""
	}
	digits := func(s string) bool {
		if s == "" {
			return false
		}
		for _, c := range s {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "KEY":
		if digits(parts[1]) {
			return dbID, "h:" + parts[1]
		}
	case "OBJECT", "TAB":
		if digits(parts[1]) {
			return dbID, "o:" + parts[1]
		}
	case "PAGE", "RID":
		if len(parts) >= 3 && digits(parts[1]) && digits(parts[2]) {
			return dbID, "p:" + parts[1] + ":" + parts[2]
		}
	}
	return dbID, ""
}

// ProcInputBuffer reads the ids of an RPC call's input buffer, "Proc [Database Id = 7 Object Id = 1234]".
func ProcInputBuffer(buf string) (dbID, objectID int, ok bool) {
	m := procInputBuffer.FindStringSubmatch(strings.Join(strings.Fields(buf), " "))
	if m == nil {
		return 0, 0, false
	}
	fmt.Sscan(m[1], &dbID)
	fmt.Sscan(m[2], &objectID)
	return dbID, objectID, true
}

// blkLabel is how BLK01 names a leader: its procedure, or the start of its statement in quotes.
func blkLabel(l BlockingLeader) string {
	if l.Procedure != "" {
		return l.Procedure
	}
	if l.BlockerStatement == "" {
		return "a statement the report did not record"
	}
	return `"` + BlockSnippet(l.BlockerStatement, 80) + `"`
}

// BLK01's texts. The why is WRK01's lock why; the do-this follows the head blocker's kind
// (docs/phase5d-contract.md).
const (
	blk01DoSleeping     = "The application opens a transaction and waits for the user or a web call: commit sooner, or ask the vendor."
	blk01DoImplicit     = "The connection runs with IMPLICIT_TRANSACTIONS on, so every statement leaves a transaction open until the application commits: a connection-string or driver setting, for the vendor."
	blk01DoSerializable = "SERIALIZABLE is TransactionScope's default; ask the vendor for READ COMMITTED."
	blk01DoRepeatable   = "REPEATABLE READ keeps every row it read locked until it commits; ask the vendor for READ COMMITTED."
	blk01DoLongRunning  = "The statement itself is slow: open it."
	blk01DoIndex        = "The lasting fixes are an index so each statement touches fewer rows, shorter transactions, or READ COMMITTED SNAPSHOT for the database (decide first: it uses TempDB for row versions)."
	blk01DoIndexRCSI    = "The database already has READ COMMITTED SNAPSHOT, so this blocking is writers against writers or a lock hint; the fix is in the code or the index."
)

// blk01Kind is the leader's main kind and what it means: the sentence's clause and the do-this.
func blk01Kind(l BlockingLeader, rcsi bool) (clause, doThis string) {
	switch {
	case blkHas(l.Kinds, BlockImplicit):
		return "the blocker ran with implicit transactions, so its transaction stayed open", blk01DoImplicit
	case blkHas(l.Kinds, BlockSleeping):
		return "the blocker held its transaction open while doing nothing (sleeping)", blk01DoSleeping
	case blkHas(l.Kinds, BlockSerializable):
		return "the blocker used the SERIALIZABLE isolation level", blk01DoSerializable
	case blkHas(l.Kinds, BlockRepeatableRead):
		return "the blocker used the REPEATABLE READ isolation level", blk01DoRepeatable
	case blkHas(l.Kinds, BlockLongRunning):
		return "the blocker's statement had run for over 30 seconds", blk01DoLongRunning
	}
	clause = "the blocker was running its statement"
	if blkHas(l.Kinds, BlockCrossDatabase) {
		clause = "the blocker was working in database " + orQ(l.Database)
	}
	if rcsi {
		return clause, blk01DoIndexRCSI
	}
	return clause, blk01DoIndex
}

// blk01Owner is who acts: the vendor when the kind is code (sleeping, implicit, serializable,
// repeatable read), else us.
func blk01Owner(l BlockingLeader) string {
	for _, k := range []string{BlockImplicit, BlockSleeping, BlockSerializable, BlockRepeatableRead} {
		if blkHas(l.Kinds, k) {
			return OwnerVendor
		}
	}
	return OwnerUs
}

// blk01Severity: Info for one occurrence; Medium from two; High from ten, or a wait of 60 s or more, or
// 10 sessions blocked in one snapshot; a head system_health wrote holding its transaction while it waited
// is High by the same thresholds from its first occurrence.
func blk01Severity(l BlockingLeader) Severity {
	high := l.Occurrences >= 10 || l.LongestWaitSec >= 60 || l.MostAtOnce >= 10
	switch {
	case l.HeldWhileWaiting && high:
		// A head that held its transaction a minute or more while it waited is the problem itself, seen
		// once or not: system_health often keeps no victim behind it to count.
		return SeverityHigh
	case l.Occurrences < 2:
		return SeverityInfo
	case high:
		return SeverityHigh
	}
	return SeverityMedium
}

// blkTimes is "once", "twice", "3 times".
func blkTimes(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%s times", n0int(int64(n)))
}

// blkBehind says how many sessions system_health recorded behind a head.
func blkBehind(n int) string {
	switch n {
	case 0:
		return "no session was recorded behind it, system_health keeps the head of the chain only"
	case 1:
		return "1 session was recorded behind it"
	}
	return fmt.Sprintf("%s sessions were recorded behind it", n0int(int64(n)))
}

func blk01Finding(l BlockingLeader, days int, offset time.Duration, rcsi bool) Finding {
	chk, _ := CheckByID("BLK01")
	clause, doThis := blk01Kind(l, rcsi)
	on := l.Object
	if on == "" {
		on = "an object the report did not name"
	}
	longest := "under a second"
	if l.LongestWaitSec >= 1 {
		longest = BlockDuration(l.LongestWaitSec)
	}
	last := l.Last.Add(offset).Format("Mon 2006-01-02 15:04") + " (server time)"
	object := blkLabel(l) + " on " + on
	var finding string
	if l.HeldWhileWaiting {
		// "… held an open transaction for 34 minutes while waiting (no session was recorded behind it, …)".
		where := ""
		if l.Object != "" {
			where = " on " + l.Object
		} else {
			object = blkLabel(l)
		}
		if !blk01CodeKind(l) {
			clause, doThis = "", blk01DoSleeping
		}
		if clause != "" {
			clause = "; " + clause
		}
		finding = fmt.Sprintf("%s held an open transaction for %s while waiting (%s), %s in %s%s%s, the last on %s.",
			blkLabel(l), longest, blkBehind(l.TotalBlockedSessions), blkTimes(l.Occurrences), plural(int64(days), "day", "days"), where, clause, last)
	} else if l.Passes > 0 && l.Snapshots == 0 {
		// kakapo's session saw every block: "{n} blocks, the longest {s}".
		finding = fmt.Sprintf("%s caused %s in %s on %s, the longest %s, %s blocked in all; %s, the last on %s.",
			blkLabel(l), plural(int64(l.Occurrences), "block", "blocks"), plural(int64(days), "day", "days"), on, longest,
			plural(int64(l.TotalBlockedSessions), "session", "sessions"), clause, last)
	} else {
		finding = fmt.Sprintf("%s blocked %s %s in %s on %s, the longest for %s; %s, the last on %s.",
			blkLabel(l), plural(int64(l.TotalBlockedSessions), "session", "sessions"), blkTimes(l.Occurrences), plural(int64(days), "day", "days"), on, longest, clause, last)
	}

	byDay := []string{}
	for _, d := range l.ByDay {
		byDay = append(byDay, fmt.Sprintf("%s %d", d.Day, d.Count))
	}
	who := strings.TrimSpace(strings.Join(nonEmpty(l.BlockerProgram, l.BlockerLogin, l.BlockerHost), ", "))
	ev := []string{}
	ev = append(ev, "Blocker statement: "+oneLine(orQ(l.SampleBlockerStatement)))
	if who != "" {
		ev[0] += " (" + who + ")"
	}
	switch {
	case l.SampleVictimStatement != "":
		ev = append(ev, "Blocked statement: "+oneLine(l.SampleVictimStatement))
	case l.HeldWhileWaiting:
		ev = append(ev, "Blocked statement: none recorded (system_health keeps the head of the chain only)")
	default:
		ev = append(ev, "Blocked statement: ?")
	}
	if l.LockMode != "" {
		ev = append(ev, "Lock wanted: "+l.LockMode)
	}
	if l.Object == "" && l.HeldWhileWaiting {
		on = "not recorded"
	}
	ev = append(ev, fmt.Sprintf("Object: %s. Seen in %s, %s blocked in all, at most %d at once", on,
		blkSeen(l), plural(int64(l.TotalBlockedSessions), "session", "sessions"), l.MostAtOnce))
	if len(byDay) > 0 {
		ev = append(ev, "By day: "+strings.Join(byDay, ", "))
	}
	if l.QueryHash != "" {
		ev = append(ev, "Query hash: "+l.QueryHash)
	}
	return Finding{
		CheckID: chk.ID, Title: chk.Title, Severity: blk01Severity(l), Owner: blk01Owner(l), When: WhenDecideFirst, Effort: EffortModerate,
		Object: object, Finding: finding, Why: wrk01Why[waitLOCK], DoThis: doThis, ThenCheck: chk.ThenCheck,
		Evidence: left(strings.Join(ev, ". ")+".", 4000), Clock: ClockWindow,
	}
}

// blkSeen is where a leader was seen: system_health's snapshots, kakapo's reports, or both.
func blkSeen(l BlockingLeader) string {
	snaps := plural(int64(l.Snapshots), "five-minute snapshot", "five-minute snapshots")
	passes := plural(int64(l.Passes), "report of kakapo's session", "reports of kakapo's session")
	switch {
	case l.Passes == 0:
		return snaps
	case l.Snapshots == 0:
		return passes
	}
	return snaps + " and " + passes
}

// blk01CodeKind says whether the leader has a kind its sentence names: one that is code (implicit,
// sleeping, serializable, repeatable read), a long statement, or another database.
func blk01CodeKind(l BlockingLeader) bool {
	for _, k := range []string{BlockImplicit, BlockSleeping, BlockSerializable, BlockRepeatableRead, BlockLongRunning, BlockCrossDatabase} {
		if blkHas(l.Kinds, k) {
			return true
		}
	}
	return false
}

func nonEmpty(xs ...string) []string {
	out := []string{}
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

// blk01Check is BLK01's line in the checks once a history was read: a finding when a leader came back
// (an actionable finding), passed otherwise with what was seen.
func blk01Check(st CheckStatus, last *BlockingHistory) CheckStatus {
	st.Note = ""
	for _, x := range last.Findings {
		if x.Severity.Actionable() {
			st.Status = CheckFinding
			return st
		}
	}
	st.Status = CheckPassed
	switch {
	case len(last.Leaders) == 0 && FromKakapo(last.Source) && last.ThresholdSeconds > 0:
		st.Note = fmt.Sprintf("No block over %d s in this database in the last %d days (kakapo's session).", last.ThresholdSeconds, last.Days)
	case len(last.Leaders) == 0:
		st.Note = fmt.Sprintf("No blocking in this database that system_health saw in the last %d days (it looks every five minutes).", last.Days)
	default:
		st.Note = fmt.Sprintf("No blocker came back in the last %d days: %s seen once.", last.Days, plural(int64(len(last.Leaders)), "blocker", "blockers"))
	}
	return st
}
