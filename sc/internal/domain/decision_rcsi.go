package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Database decisions (docs/phase5m-contract.md): settings that are neither wrong nor right on their own,
// decided for one database from the evidence kakapo keeps, the cost, and a record of what was decided.
// The first is row versioning, READ COMMITTED SNAPSHOT (RCSI). A decision is not a finding: it is on no
// card, in no count and not in the red flags; its catalogue entry is DEC01.
//
// Sources (Microsoft Learn, verified 2026-11-06):
//   - "Transaction locking and row versioning guide": readers at read committed with RCSI take no S
//     locks but still take Sch-S; turning READ_COMMITTED_SNAPSHOT or ALLOW_SNAPSHOT_ISOLATION on versions
//     every change in the database, kept in tempdb's version store; each row gains up to 14 bytes the
//     first time it is changed or inserted after that; the common version store is roughly 2 x the
//     version data generated per minute x the longest transaction in minutes; update conflicts (error
//     3960) are SNAPSHOT isolation's, RCSI has no update conflict detection; while the option is set,
//     only the connection running ALTER DATABASE may be in the database.
//   - "Accelerated database recovery" (SQL Server 2019 and later): with ADR the row versions are kept in
//     the database's own persistent version store instead of tempdb, which needs the database's space.
//   - DarlingData sp_HumanEventsBlockViewer.sql 3694-3729 (MIT): "You Might Need RCSI" from blocked lock
//     mode S or IS; sp_PressureDetector.sql 2745-2811: the oldest transaction holds the version store.

// DecisionRowVersioning names the row versioning decision in the decision record.
const DecisionRowVersioning = "row-versioning"

// The row versioning verdicts.
const (
	DecisionOn                = "on"
	DecisionFixFirst          = "fix-first"
	DecisionNotEnoughEvidence = "not-enough-evidence"
	DecisionWorthConsidering  = "worth-considering"
	DecisionNotNeeded         = "not-needed"
)

// DecisionVerdicts are the verdicts a record may carry.
var DecisionVerdicts = []string{DecisionOn, DecisionFixFirst, DecisionNotEnoughEvidence, DecisionWorthConsidering, DecisionNotNeeded}

// The choices of a decision record: keep it as it is, change it (the fix's Apply records itself),
// decide later.
const (
	ChoiceKeep   = "keep"
	ChoiceChange = "change"
	ChoiceLater  = "later"
)

// DecisionReviewDays is how far ahead "Review by" is by default.
const DecisionReviewDays = 90

// decisionFreshDays is how old a stored report may be and still count as evidence.
const decisionFreshDays = 7

// Tones of a decision's tag.
const (
	ToneGreen  = "green"
	ToneAmber  = "amber"
	ToneOrange = "orange"
	ToneGrey   = "grey"
)

// RowVersioningFacts is the row versioning read (collect.ReadRowVersioning, docs/phase5m-contract.md
// section 5): the settings, the version stores, the oldest transaction open in the database and the
// other sessions in it, each area with its own error.
type RowVersioningFacts struct {
	Database       string
	ReadAt         time.Time // UTC
	ProductVersion string

	RCSI               bool
	SnapshotIsolation  string // snapshot_isolation_state_desc: OFF, ON, IN_TRANSITION_TO_ON, IN_TRANSITION_TO_OFF
	ADR                *bool  // is_accelerated_database_recovery_on; nil before SQL Server 2019
	CompatibilityLevel int
	SettingsError      string

	TempdbVersionStoreKB *int64 // tempdb's version store, as TMP03 reads it
	DatabaseVersionKB    *int64 // this database's share (sys.dm_tran_version_store_space_usage)
	VersionStoreError    string

	PVSKB    *int64 // persistent_version_store_size_kb of this database (ADR on)
	PVSError string

	Oldest            *OpenTransaction // the oldest transaction open in this database now; nil when none
	TransactionsError string

	Users      int // other user sessions in the database now
	UsersError string
}

// ADROn says whether accelerated database recovery is on.
func (f RowVersioningFacts) ADROn() bool { return f.ADR != nil && *f.ADR }

// DecisionRecord is one decision taken on a database (the decision table): what was chosen, on which
// verdict, why, by whom, and when to look again. A record is never edited; a new one replaces it.
type DecisionRecord struct {
	ID        int64     `json:"id"`
	ServerID  int64     `json:"serverId"`
	Database  string    `json:"database"`
	Decision  string    `json:"decision"`
	DecidedAt time.Time `json:"decidedAt"`
	DecidedBy string    `json:"decidedBy"`
	Choice    string    `json:"choice"`
	Verdict   string    `json:"verdict"`
	Reason    string    `json:"reason,omitempty"`
	ReviewOn  string    `json:"reviewOn,omitempty"` // "2027-02-04"
	ChangeID  *int64    `json:"changeId,omitempty"`
	// Line says the record in words (RecordLine) and ReviewDue is set once its review date has passed;
	// both are filled for the page (DescribeRecords), not stored.
	Line      string `json:"line,omitempty"`
	ReviewDue bool   `json:"reviewDue,omitempty"`
}

// DescribeRecords fills each record's line and whether its review is due on the day of now.
func DescribeRecords(rs []DecisionRecord, now time.Time) []DecisionRecord {
	out := make([]DecisionRecord, 0, len(rs))
	today := now.Format("2006-01-02")
	for _, r := range rs {
		r.Line = RecordLine(r)
		r.ReviewDue = r.Choice != ChoiceChange && r.ReviewOn != "" && r.ReviewOn < today
		out = append(out, r)
	}
	return out
}

// DecisionInput is Record a decision's body: the choice (keep or later), the verdict the row showed,
// the reason (required for keep) and the review date ("" for the default, 90 days from today).
type DecisionInput struct {
	Choice   string `json:"choice"`
	Verdict  string `json:"verdict"`
	Reason   string `json:"reason"`
	ReviewOn string `json:"reviewOn"`
}

// ParseDecisionInput checks a recorded decision and fills the default review date from today (the
// local date). Change it is the rcsi fix's Apply, which records itself, so it is refused here.
func ParseDecisionInput(in DecisionInput, today time.Time) (DecisionRecord, error) {
	r := DecisionRecord{Decision: DecisionRowVersioning, Choice: strings.TrimSpace(in.Choice), Verdict: strings.TrimSpace(in.Verdict),
		Reason: strings.TrimSpace(in.Reason)}
	switch r.Choice {
	case ChoiceKeep, ChoiceLater:
	case ChoiceChange:
		return r, errors.New("change it is the fix's Apply, which records itself: open the preview instead")
	default:
		return r, fmt.Errorf("choice must be keep or later, not %q", in.Choice)
	}
	known := false
	for _, v := range DecisionVerdicts {
		known = known || v == r.Verdict
	}
	if !known {
		return r, fmt.Errorf("verdict must be one of %s, not %q", strings.Join(DecisionVerdicts, ", "), in.Verdict)
	}
	if r.Choice == ChoiceKeep && r.Reason == "" {
		return r, errors.New("a reason is needed to keep it as it is")
	}
	if len(r.Reason) > 2000 {
		return r, errors.New("the reason is longer than 2000 characters")
	}
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	review := day.AddDate(0, 0, DecisionReviewDays)
	if s := strings.TrimSpace(in.ReviewOn); s != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return r, fmt.Errorf("reviewOn must be a date (2027-02-04), not %q", in.ReviewOn)
		}
		review = t
	}
	r.ReviewOn = review.Format("2006-01-02")
	return r, nil
}

// DecisionLine is one line of a decision's opened view: a label, the text, and the window or time it
// was read in.
type DecisionLine struct {
	Label  string `json:"label"`
	Text   string `json:"text"`
	Window string `json:"window,omitempty"`
}

// DecisionAction is a decision's button: Apply a fix (FixID, with its preview), or go to a page under the
// database (To: "blocking", "cards/storage", "cards/performance").
type DecisionAction struct {
	Label string `json:"label"`
	FixID string `json:"fixId,omitempty"`
	To    string `json:"to,omitempty"`
}

// DecisionStep is what must be dealt with before the change (Do first), with its own button.
type DecisionStep struct {
	Text   string         `json:"text"`
	Action DecisionAction `json:"action"`
}

// Decision is one row of the Decisions card: the verdict as a tag and a sentence, what kakapo found, what
// to do first, what it costs, the buttons, and the record.
type Decision struct {
	Decision string `json:"decision"`
	CheckID  string `json:"checkId"`
	Title    string `json:"title"`
	Question string `json:"question"`
	Verdict  string `json:"verdict"`
	Tag      string `json:"tag"`
	Tone     string `json:"tone"` // green, amber, orange, grey
	Sentence string `json:"sentence"`

	Found       []DecisionLine   `json:"found"`
	DoFirst     []DecisionStep   `json:"doFirst"`
	Costs       []DecisionLine   `json:"costs"`
	NotMeasured string           `json:"notMeasured,omitempty"`
	Facts       []DecisionLine   `json:"facts"`
	Actions     []DecisionAction `json:"actions"`
	ThenCheck   string           `json:"thenCheck"`
	Sources     []string         `json:"sources"`

	// Record is the latest record and RecordLine says it on the closed row; ReviewDue once its review
	// date has passed. History is every record, newest first.
	Record     *DecisionRecord  `json:"record,omitempty"`
	RecordLine string           `json:"recordLine,omitempty"`
	ReviewDue  bool             `json:"reviewDue,omitempty"`
	History    []DecisionRecord `json:"history"`

	// Evidence is the line the rcsi preview starts with (BuildRCSIFix).
	Evidence string `json:"-"`
}

// RowVersioningInputs is what the row versioning decision is built from: the read of section 5, the last
// stored blocking history and deadlock report, kakapo's event session (nil when not read), tempdb's room
// from the Storage card (nil when not read: the fix preview), whether TMP03 is raised, and the records,
// newest first.
type RowVersioningInputs struct {
	Database   string
	Now        time.Time
	Facts      *RowVersioningFacts
	FactsError string
	Blocking   *BlockingHistory
	Deadlocks  *DeadlockReport
	Helper     *XESession
	Tempdb     *TempdbRoom
	TMP03      *Finding
	Records    []DecisionRecord
}

// rcsiSources are the pages and scripts the decision rests on, as the row shows them.
var rcsiSources = []string{
	"Microsoft Learn: Transaction locking and row versioning guide",
	"Microsoft Learn: Accelerated database recovery",
	"Microsoft Learn: sys.dm_tran_version_store_space_usage, sys.dm_tran_persistent_version_store_stats",
	"DarlingData sp_HumanEventsBlockViewer (MIT): You Might Need RCSI",
}

// rcsiEvidence is what the stored reports say about readers and writers.
type rcsiEvidence struct {
	days        int
	fresh       bool // a blocking history read in the last 7 days
	classes     bool // and it tells readers from writers
	kakapo      bool // kakapo's event session covered (part of) the window
	writes      BlockingClass
	reads       BlockingClass
	deadlocks   *DeadlockReport // read in the last 7 days, with its readers known
	topReader   DeadlockPair
	mediumBlock bool
	highBlock   bool
	mediumDL    bool
	highDL      bool
}

func (e rcsiEvidence) reached() bool { return e.mediumBlock || e.mediumDL }
func (e rcsiEvidence) strong() bool  { return e.kakapo && (e.highBlock || e.highDL) }

// classHigh says whether one class reaches BLK01's High: 10 episodes, a 60-second wait, or 10 sessions at
// once.
func classHigh(c BlockingClass) bool {
	return c.Episodes >= blk01HighOccurrences || c.LongestWaitSec >= blk01HighWaitSec || c.MostAtOnce >= blk01HighAtOnce
}

func fresh(at, now time.Time) bool {
	return !at.IsZero() && now.Sub(at) <= decisionFreshDays*24*time.Hour
}

func readRCSIEvidence(in RowVersioningInputs) rcsiEvidence {
	e := rcsiEvidence{days: BlockingHistoryDays}
	if h := in.Blocking; h != nil && fresh(h.CapturedAt, in.Now) {
		e.fresh, e.days, e.kakapo = true, h.Days, FromKakapo(h.Source)
		e.writes, e.classes = h.Class(WaitWritesBehindReads)
		e.reads, _ = h.Class(WaitReadsBehindWrites)
		e.mediumBlock = e.classes && e.writes.Episodes+e.reads.Episodes >= blk01MediumOccurrences
		e.highBlock = e.classes && (classHigh(e.writes) || classHigh(e.reads))
	}
	if d := in.Deadlocks; d != nil && fresh(d.CapturedAt, in.Now) && d.WithReader != nil {
		e.deadlocks = d
		for _, p := range d.Pairs {
			if p.WithReader > e.topReader.WithReader {
				e.topReader = p
			}
		}
		e.mediumDL = e.topReader.WithReader >= dlk01Medium
		e.highDL = e.topReader.WithReader >= dlk01High
	}
	return e
}

// BuildRowVersioningDecision builds the row versioning row (docs/phase5m-contract.md, section 3). The
// verdict is the first that applies: On; Fix first (a transaction open 30 minutes or more here, or
// TempDB cannot hold more); Not enough evidence (no blocking history of the last 7 days, one without
// classes, or system_health alone with nothing that reaches Worth considering); Worth considering (the
// two classes RCSI removes together reach BLK01's Medium, or deadlocks with a reader reach DLK01's
// Medium for a pair; strongly at their High, with kakapo's session behind it); Not needed now.
func BuildRowVersioningDecision(in RowVersioningInputs) Decision {
	chk, _ := CheckByID("DEC01")
	d := Decision{Decision: DecisionRowVersioning, CheckID: chk.ID, Title: chk.Title, Question: chk.Question, ThenCheck: chk.ThenCheck,
		Found: []DecisionLine{}, DoFirst: []DecisionStep{}, Costs: []DecisionLine{}, Facts: []DecisionLine{}, Actions: []DecisionAction{},
		Sources: rcsiSources, History: []DecisionRecord{}}
	d.History = append(d.History, DescribeRecords(in.Records, in.Now)...)
	if len(d.History) > 0 {
		r := d.History[0]
		d.Record = &r
		d.RecordLine, d.ReviewDue = r.Line, r.ReviewDue
	}
	ev := readRCSIEvidence(in)
	d.Found = rcsiFound(in, ev)
	d.Evidence = rcsiEvidenceLine(ev)
	installed := in.Helper != nil && in.Helper.Exists
	install := DecisionAction{Label: "Keep every block and deadlock on the server", FixID: FixInstallXESession}

	f := in.Facts
	if f == nil {
		d.Verdict, d.Tag, d.Tone = DecisionNotEnoughEvidence, "Not enough evidence", ToneGrey
		d.Sentence = "kakapo cannot tell yet: the database's row versioning settings could not be read (" + orQ(in.FactsError) + ")."
		return d
	}
	d.Costs, d.NotMeasured, d.Facts = rcsiCosts(*f, in.Tempdb)
	adr := f.ADROn()

	// Do first: the open transaction (ACT02's High), then TempDB with no room (TMP03), which matters only
	// when the versions go to tempdb (ADR off).
	clauses := []string{}
	if o := f.Oldest; o != nil && o.Minutes >= Act02HighMinutes && !f.RCSI {
		where := "TempDB would grow"
		if adr {
			where = "this database's version store would grow"
		}
		d.DoFirst = append(d.DoFirst, DecisionStep{
			Text: fmt.Sprintf("%s. With row versioning on it would keep every version made since it began, so %s for as long as it stays open; "+
				"and the change's WITH ROLLBACK IMMEDIATE would roll it back, which ACT02 says not to do without knowing what it is.", openTranText(*o, f.Database), where),
			Action: DecisionAction{Label: "Investigate (ACT02)", To: "blocking"}})
		clauses = append(clauses, fmt.Sprintf("deal with the transaction session %d has kept open here for %d minutes (ACT02)", o.Session, o.Minutes))
	}
	if t := in.Tempdb; t != nil && t.NoRoom && !adr && !f.RCSI {
		what := "TempDB is full and cannot grow"
		if in.TMP03 != nil {
			what = strings.TrimSuffix(in.TMP03.Finding, ".")
		}
		d.DoFirst = append(d.DoFirst, DecisionStep{Text: "TempDB cannot hold more: " + what + ". Row versions would be kept there.",
			Action: DecisionAction{Label: "Open TMP03", To: "cards/storage"}})
		clauses = append(clauses, "make room in TempDB (TMP03)")
	}

	switch {
	case f.RCSI:
		d.Verdict, d.Tag, d.Tone = DecisionOn, "On", ToneGreen
		d.Sentence = "Readers do not wait for writers here. What still blocks is writers against writers or a lock hint. " + versionStoreLine(*f)
		if in.TMP03 != nil {
			d.Actions = append(d.Actions, DecisionAction{Label: "Investigate (TMP03)", To: "cards/storage"})
		}
	case len(d.DoFirst) > 0:
		d.Verdict, d.Tag, d.Tone = DecisionFixFirst, "Fix first", ToneAmber
		d.Sentence = "Before deciding on row versioning, " + clauses[0] + "."
		d.Actions = append(d.Actions, d.DoFirst[0].Action)
	case !ev.fresh || !ev.classes || (!ev.kakapo && !ev.reached()):
		d.Verdict, d.Tag, d.Tone = DecisionNotEnoughEvidence, "Not enough evidence", ToneGrey
		missing := "only system_health's five-minute snapshots covered the window, and they show too little to tell; kakapo's event session keeps every wait over 5 seconds"
		switch {
		case !ev.fresh:
			missing = "the blocking history has not been read in the last 7 days"
		case !ev.classes:
			missing = "the blocking history was read before kakapo told readers from writers. Read the blocking history again to tell readers from writers"
		}
		d.Sentence = "kakapo cannot tell yet whether readers and writers wait for each other here: " + missing + "."
		if installed {
			d.Actions = append(d.Actions, DecisionAction{Label: "Read blocking history", To: "cards/performance"})
		} else {
			d.Actions = append(d.Actions, install)
		}
	case ev.reached():
		d.Verdict, d.Tag, d.Tone = DecisionWorthConsidering, "Worth considering", ToneAmber
		if ev.strong() {
			d.Tag, d.Tone = "Strongly worth considering", ToneOrange
		}
		d.Sentence = rcsiWorthSentence(ev)
		d.Actions = append(d.Actions, DecisionAction{Label: "Turn on READ COMMITTED SNAPSHOT", FixID: FixRCSI})
		if !ev.kakapo {
			d.Sentence += " kakapo's event session would show every wait over 5 seconds."
			if !installed {
				d.Actions = append(d.Actions, install)
			}
		}
	default:
		d.Verdict, d.Tag, d.Tone = DecisionNotNeeded, "Not needed now", ToneGreen
		d.Sentence = "Readers and writers did not wait for each other enough to change this: " + rcsiCounts(ev) + "."
	}
	return d
}

// rcsiWorthSentence is Worth considering's sentence: the waits RCSI would remove, and the deadlocks with
// a reader when they count.
func rcsiWorthSentence(ev rcsiEvidence) string {
	blocking := fmt.Sprintf("Writes waited %s for reads and reads %s for writes over %s",
		WaitText(ev.writes.TotalWaitSec), WaitText(ev.reads.TotalWaitSec), plural(int64(ev.days), "day", "days"))
	switch {
	case ev.mediumBlock && ev.mediumDL:
		return blocking + fmt.Sprintf(", and %d deadlocks had a reader in them; READ COMMITTED SNAPSHOT would remove those waits and deadlocks.", *ev.deadlocks.WithReader)
	case ev.mediumDL:
		return fmt.Sprintf("%d of %d deadlocks in %s had a reader in them; READ COMMITTED SNAPSHOT would remove those deadlocks.",
			*ev.deadlocks.WithReader, ev.deadlocks.Total, plural(int64(ev.deadlocks.Days), "day", "days"))
	}
	return blocking + "; READ COMMITTED SNAPSHOT would remove those waits."
}

// rcsiCounts is Not needed now's counts.
func rcsiCounts(ev rcsiEvidence) string {
	s := fmt.Sprintf("%s and %s in %s", plural(int64(ev.writes.Episodes), "write behind reads", "writes behind reads"),
		plural(int64(ev.reads.Episodes), "read behind writes", "reads behind writes"), plural(int64(ev.days), "day", "days"))
	if ev.deadlocks != nil {
		s += fmt.Sprintf(", %s with a reader", plural(int64(*ev.deadlocks.WithReader), "deadlock", "deadlocks"))
	}
	return s
}

// rcsiEvidenceLine is the line the rcsi preview starts with: the waits RCSI would remove when the
// evidence reaches Worth considering, else that kakapo has none.
func rcsiEvidenceLine(ev rcsiEvidence) string {
	if ev.fresh && ev.classes && ev.reached() {
		return rcsiWorthSentence(ev)
	}
	return "kakapo has no evidence that readers and writers wait for each other here."
}

// blockingWindow is where a history's waits came from, as a line's window.
func blockingWindow(h *BlockingHistory) string {
	days := plural(int64(h.Days), "day", "days")
	switch {
	case h.Source == BlockSourceKakapo:
		return "last " + days + ", kakapo's event session"
	case FromKakapo(h.Source) && h.KakapoSince != nil:
		return fmt.Sprintf("last %s: kakapo's event session since %s, system_health's five-minute snapshots before", days, h.KakapoSince.Format("2006-01-02"))
	}
	return "last " + days + ", system_health snapshots, every five minutes"
}

// classText is a removable class's line: "{h} h {m} min a day on average, on {d} of {n} days; longest
// {s}", with the programs.
func classText(c BlockingClass, days int) string {
	if c.Episodes == 0 {
		return "None in " + plural(int64(days), "day", "days") + "."
	}
	s := fmt.Sprintf("%s a day on average, on %d of %s; longest %s (%s).", WaitText(c.TotalWaitSec/int64(max(days, 1))), c.Days,
		plural(int64(days), "day", "days"), WaitText(c.LongestWaitSec), plural(int64(c.Episodes), "wait", "waits"))
	party := func(ps []BlockingParty) string {
		out := []string{}
		for _, p := range ps {
			who := orQ(p.Program)
			if extra := nonEmpty(p.Login, p.Host); len(extra) > 0 {
				who += " (" + strings.Join(extra, ", ") + ")"
			}
			out = append(out, who)
		}
		return strings.Join(out, "; ")
	}
	if len(c.Waiters) > 0 {
		s += " Waiting: " + party(c.Waiters) + "."
	}
	if len(c.Blockers) > 0 {
		s += " Blocking: " + party(c.Blockers) + "."
	}
	return s
}

// rcsiFound is What kakapo found: the two classes RCSI removes, what it does not remove, the deadlocks
// with a reader, and where the evidence came from.
func rcsiFound(in RowVersioningInputs, ev rcsiEvidence) []DecisionLine {
	out := []DecisionLine{}
	h := in.Blocking
	switch {
	case !ev.fresh && h == nil:
		out = append(out, DecisionLine{Label: "Blocking", Text: "The blocking history has not been read (Read blocking history on the Performance card)."})
	case !ev.fresh:
		out = append(out, DecisionLine{Label: "Blocking", Text: fmt.Sprintf("The last blocking history was read %s, more than 7 days ago: read it again.", utcText(h.CapturedAt))})
	case !ev.classes:
		out = append(out, DecisionLine{Label: "Blocking", Text: "Read the blocking history again to tell readers from writers.", Window: blockingWindow(h)})
	default:
		win := blockingWindow(h)
		out = append(out,
			DecisionLine{Label: "Writes waiting for reads", Text: classText(ev.writes, ev.days), Window: win},
			DecisionLine{Label: "Reads waiting for writes", Text: classText(ev.reads, ev.days), Window: win})
		parts := []string{}
		for _, x := range []struct{ class, name string }{{WaitWritesBehindWrites, "writes waiting for writes"}, {WaitLockingReaders, "readers that keep their locks"},
			{WaitSchemaLocks, "schema locks"}} {
			c, _ := h.Class(x.class)
			parts = append(parts, fmt.Sprintf("%s %d (%s)", x.name, c.Episodes, WaitText(c.TotalWaitSec)))
		}
		out = append(out, DecisionLine{Label: "Not removed by RCSI", Text: upperFirst(strings.Join(parts, ", ")) + ".", Window: win})
	}

	dl := in.Deadlocks
	switch {
	case dl == nil || !fresh(dl.CapturedAt, in.Now):
		out = append(out, DecisionLine{Label: "Deadlocks with a reader", Text: "The deadlock report has not been read in the last 7 days (Read deadlocks on the Performance card)."})
	case dl.WithReader == nil:
		out = append(out, DecisionLine{Label: "Deadlocks with a reader", Text: "The deadlock report was read before kakapo told deadlocks with a reader apart: read it again."})
	default:
		s := fmt.Sprintf("%d of %d deadlocks in %s had a reader in them", *dl.WithReader, dl.Total, plural(int64(dl.Days), "day", "days"))
		if ev.topReader.WithReader > 0 {
			s += fmt.Sprintf("; the most: %s / %s (%d)", ev.topReader.Procedures[0], ev.topReader.Procedures[1], ev.topReader.WithReader)
		}
		out = append(out, DecisionLine{Label: "Deadlocks with a reader", Text: s + ".", Window: "last " + plural(int64(dl.Days), "day", "days") + ", " + deadlockSourceText(dl.Source)})
	}

	if ev.fresh {
		threshold := "0 (off)"
		if h.ThresholdSeconds > 0 {
			threshold = fmt.Sprintf("%d s", h.ThresholdSeconds)
		}
		s := fmt.Sprintf("Blocking history read %s from %s; blocked process threshold %s", utcText(h.CapturedAt), blockingWindow(h), threshold)
		if u, ok := h.Class(WaitUnknown); ok {
			s += fmt.Sprintf("; %s kakapo could not place (a head of a chain, or a blocker that called a procedure)", plural(int64(u.Episodes), "wait", "waits"))
		}
		out = append(out, DecisionLine{Label: "Evidence", Text: s + "."})
	}
	return out
}

// deadlockSourceText names where a deadlock report came from.
func deadlockSourceText(source string) string {
	switch {
	case source == BlockSourceKakapo:
		return "kakapo's event session"
	case FromKakapo(source):
		return "kakapo's event session and system_health before it"
	}
	return "system_health"
}

// versionStoreLine is On's line about where the versions are and how much they hold now.
func versionStoreLine(f RowVersioningFacts) string {
	if f.ADROn() {
		if f.PVSKB != nil {
			return fmt.Sprintf("Row versions are kept in this database's persistent version store (ADR): %s MB now.", n1(float64(*f.PVSKB)/1024))
		}
		return "Row versions are kept in this database's persistent version store (ADR)."
	}
	if f.TempdbVersionStoreKB == nil {
		return "Row versions are kept in tempdb's version store; TMP03 shows how much it holds."
	}
	s := fmt.Sprintf("The version store in tempdb holds %s MB now", n1(float64(*f.TempdbVersionStoreKB)/1024))
	if f.DatabaseVersionKB != nil {
		s += fmt.Sprintf(", %s MB of it from this database", n1(float64(*f.DatabaseVersionKB)/1024))
	}
	return s + "."
}

// openTranText is "Session 57 (Program on HOST, sleeping) has had a transaction open in db for 42
// minutes".
func openTranText(o OpenTransaction, db string) string {
	who := orQ(o.Program)
	if o.Host != "" {
		who += " on " + o.Host
	}
	if o.Status != "" {
		who += ", " + o.Status
	}
	return fmt.Sprintf("Session %d (%s) has had a transaction open in %s for %s", o.Session, who, db, plural(int64(o.Minutes), "minute", "minutes"))
}

// rcsiCosts is What it costs: the measured values with their time, the line about what cannot be
// measured, and snapshot isolation and ADR as facts with their sentence.
func rcsiCosts(f RowVersioningFacts, t *TempdbRoom) (costs []DecisionLine, notMeasured string, facts []DecisionLine) {
	costs, facts = []DecisionLine{}, []DecisionLine{}
	at := "read " + utcText(f.ReadAt)
	switch {
	case f.VersionStoreError != "":
		costs = append(costs, DecisionLine{Label: "Version store", Text: "Could not be read: " + f.VersionStoreError})
	case f.TempdbVersionStoreKB != nil:
		s := fmt.Sprintf("%s MB in tempdb now", n1(float64(*f.TempdbVersionStoreKB)/1024))
		if f.DatabaseVersionKB != nil {
			s += fmt.Sprintf(", %s MB of it from this database", n1(float64(*f.DatabaseVersionKB)/1024))
		}
		costs = append(costs, DecisionLine{Label: "Version store", Text: s + ".", Window: at})
	}
	if f.ADROn() {
		switch {
		case f.PVSError != "":
			costs = append(costs, DecisionLine{Label: "Persistent version store (ADR)", Text: "Could not be read: " + f.PVSError})
		case f.PVSKB != nil:
			costs = append(costs, DecisionLine{Label: "Persistent version store (ADR)", Text: fmt.Sprintf("%s MB in this database now.", n1(float64(*f.PVSKB)/1024)), Window: at})
		}
	}
	longest := 0
	switch {
	case f.TransactionsError != "":
		costs = append(costs, DecisionLine{Label: "Longest open transaction here", Text: "Could not be read: " + f.TransactionsError})
	case f.Oldest != nil:
		longest = f.Oldest.Minutes
		costs = append(costs, DecisionLine{Label: "Longest open transaction here", Text: openTranText(*f.Oldest, f.Database) + ".", Window: at})
	default:
		costs = append(costs, DecisionLine{Label: "Longest open transaction here", Text: "None open now.", Window: at})
	}
	if f.UsersError != "" {
		costs = append(costs, DecisionLine{Label: "Other sessions in the database", Text: "Could not be read: " + f.UsersError})
	} else {
		costs = append(costs, DecisionLine{Label: "Other sessions in the database",
			Text:   fmt.Sprintf("%d now; WITH ROLLBACK IMMEDIATE disconnects them for a moment when the setting changes.", f.Users),
			Window: at})
	}
	if t != nil {
		grow := "can grow"
		if !t.CanGrow {
			grow = "cannot grow"
		}
		s := fmt.Sprintf("%s MB, %s MB free, %s", n0int(t.AllocMB), n0int(t.FreeMB), grow)
		if t.VolumeFreeMB != nil {
			s += fmt.Sprintf("; its drive has %s MB free", n0int(*t.VolumeFreeMB))
		}
		costs = append(costs, DecisionLine{Label: "TempDB", Text: s + ".", Window: "the Storage card's read"})
	}
	notMeasured = fmt.Sprintf("How much version data this database would write cannot be read before it is on. Microsoft sizes the version store "+
		"as about twice the version data written per minute times the longest transaction in minutes; the longest here now is %s. "+
		"After the change, TMP03 shows what the version store holds: look again after a few working days.", plural(int64(longest), "minute", "minutes"))
	if f.SettingsError != "" {
		facts = append(facts, DecisionLine{Label: "Settings", Text: "Could not be read: " + f.SettingsError})
	}
	if !f.RCSI && strings.EqualFold(f.SnapshotIsolation, "ON") {
		facts = append(facts, DecisionLine{Label: "Snapshot isolation is on",
			Text: "Code can already ask for row versions (SET TRANSACTION ISOLATION LEVEL SNAPSHOT), and with it on every change here is already versioned: " +
				"turning on READ COMMITTED SNAPSHOT adds no new version data, it lets every reader at read committed use the versions."})
	}
	if f.ADROn() {
		facts = append(facts, DecisionLine{Label: "Accelerated database recovery is on",
			Text: "Row versions are kept in this database's own persistent version store, not in tempdb, so the cost is this database's free space (STO01, STO04) instead of tempdb's."})
	}
	return costs, notMeasured, facts
}

// RecordLine says a record on the closed row: "Kept off on 2026-11-06: waiting for the vendor; review by
// 2027-02-04".
func RecordLine(r DecisionRecord) string {
	day := r.DecidedAt.Format("2006-01-02")
	var s string
	switch r.Choice {
	case ChoiceChange:
		s = "Turned on on " + day
		if r.ChangeID != nil {
			s += fmt.Sprintf(" (change %d)", *r.ChangeID)
		}
		return s
	case ChoiceKeep:
		state := "off"
		if r.Verdict == DecisionOn {
			state = "on"
		}
		s = "Kept " + state + " on " + day
	default:
		s = "Left for later on " + day
	}
	if reason := strings.TrimRight(r.Reason, ". "); reason != "" {
		s += ": " + reason
	}
	if r.ReviewOn != "" {
		s += "; review by " + r.ReviewOn
	}
	return s
}
