package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The tuning report (docs/phase5c-contract.md): "is the database better than before, and did the
// change I made do it?", from Query Store's hour by hour history and kakapo's change log. The rules
// are the toolkit's own (granite-sql SqlTuningReport, README "How the comparison works" and
// TuningReport.sql): business hours, the window around a change, ratios from sums, the plan
// regression rule. Every time here is the server's clock (no zone), as the toolkit shows it.

// The report's options and the toolkit's thresholds.
const (
	TuningMinDays     = 1
	TuningMaxDays     = 30
	TuningDefaultDays = 7

	// TuningDefaultBusinessHours is the toolkit's default: 08:00 to 17:00, Monday to Friday.
	TuningDefaultBusinessHours = "08-17"

	// TuningChangeNone centres the report on no change: the latest complete business hour.
	TuningChangeNone int64 = -1

	// Plan regressions: TuningReport.sql result 4. The newest plan ran at least 50 times and uses at
	// least 1.5 x the CPU per run of the previous plan, which used at least 1 ms.
	PlanRegressionMinRuns     = 50
	PlanRegressionMinOldCPUMs = 1.0
	PlanRegressionRatio       = 1.5

	// A tile is better or worse from a 10% change; within that it is the same.
	TuningTonePct = 10.0

	// Per query: faster at 0.8 x the CPU per run before or less, slower at 1.25 x or more, and no
	// verdict under 20 runs in either window.
	TuningFasterRatio = 0.8
	TuningSlowerRatio = 1.25
	TuningMinRuns     = 20

	// Before is the business hours earlier the same day when there are at least two, else the
	// business hours of the five working days before.
	TuningSameDayMinHours = 2
	TuningWorkingDays     = 5

	// TuningTopShapes is how many query shapes the read keeps (the toolkit's top 15 by CPU).
	TuningTopShapes = 15

	// ChangeRecordedByHand is the Result of a change the consultant recorded by hand: something done
	// outside kakapo (a procedure deployed, an index created in SSMS).
	ChangeRecordedByHand = "recorded by hand"

	// ChangeSeenOnServer is the Result of a change kakapo read from SQL Server's default trace: made
	// outside kakapo, by someone else (docs/phase5f-contract.md).
	ChangeSeenOnServer = "seen on the server"
)

// Hour classes.
const (
	HourBusiness = "business"
	HourNight    = "night"
	HourWeekend  = "weekend"
)

// Tile tones (ToneWorse is the facts' "worse"); executions are not better or worse, only more or fewer.
const (
	ToneBetter = "better"
	ToneSame   = "same"
	ToneMore   = "more"
	ToneFewer  = "fewer"
)

// Per-query verdicts.
const (
	VerdictFaster = "faster"
	VerdictSlower = "slower"
	VerdictSame   = "same"
	VerdictNew    = "new"
	VerdictGone   = "gone"
	VerdictTooFew = "too few"
)

// performanceChangeTypes are the change log's types that can affect performance; the latest of them
// (or of the changes recorded by hand) is the report's default centre. CHECK, KILL, JOB, ALERT, CLEAR,
// ARCHIVE, TABLE and the maintenance plan's PLAN summary stay choosable but never the default. DEPLOY,
// FUNCTION and TRIGGER are the changes seen on the server (docs/phase5f-contract.md); a table created or
// altered alone is rarely a performance change, and inside a DEPLOY when it comes with a release.
var performanceChangeTypes = []string{"SETTING", "INDEX", "STATISTICS", "PLAN", "PROCEDURE", "VIEW", "MAINTENANCE", "FILE", "OTHER",
	ChangeTypeDeploy, changeTypeFunction, changeTypeTrigger} // FILE: the TempDB file set changes how the server behaves

// maintenancePlanObject is the Object of the maintenance plan's summary row, whose type is PLAN too.
const maintenancePlanObject = "Maintenance plan"

// IsPerformanceChange says whether a change log row can affect performance: a change recorded by hand,
// or one of performanceChangeTypes other than the maintenance plan's summary.
func IsPerformanceChange(c Change) bool {
	if c.Result == ChangeRecordedByHand {
		return true
	}
	if strings.EqualFold(c.ChangeType, "PLAN") && strings.EqualFold(strings.TrimSpace(c.Object), maintenancePlanObject) {
		return false
	}
	return qContainsFold(performanceChangeTypes, c.ChangeType)
}

// ChangeTypes are the kinds of change a consultant may record by hand (the toolkit's change log,
// with OTHER for anything else).
var ChangeTypes = []string{"PROCEDURE", "VIEW", "INDEX", "STATISTICS", "SETTING", "OTHER"}

// TuningFacts is what collect.TuningReport read inside one database. Times are the server's clock
// (GETDATE), kept naive; CPU and duration are microseconds, summed as avg x count_executions per
// interval (execution-weighted, as sp_QuickieStore). An area that could not be read carries its
// reason in its error field.
type TuningFacts struct {
	Database         string
	ServerName       string
	Days             int
	ServerNow        time.Time // GETDATE
	UTCOffsetMinutes int

	QueryStoreState string // actual_state_desc
	IntervalMinutes int
	WaitCapture     string // wait_stats_capture_mode_desc; "" before SQL Server 2017
	OldestInterval  *time.Time
	AboutError      string

	HoursError string
	Hours      []TuningHourFact
	WaitsError string // waits per hour (SQL Server 2017 and later)

	ShapesError string
	Shapes      []TuningShapeFact
	ShapeHours  []TuningShapeHourFact

	RegressionsError string
	Regressions      []PlanRegressionFact

	FailuresError string
	Failures      []QueryFailureFact
}

// TuningHourFact is one hour of the database in Query Store (server clock, on the hour), every plan
// summed.
type TuningHourFact struct {
	Hour            time.Time
	Executions      int64
	CPU             int64 // microseconds
	Reads           int64 // 8 KB pages
	Duration        int64 // microseconds
	Aborted         int64 // execution_type 3
	Exceptions      int64 // execution_type 4
	ParallelWaitSec float64
	LockWaitSec     float64
	CPUWaitSec      float64
	OtherWaitSec    float64
}

// TuningShapeFact is one of the top query shapes (query_hash) by CPU in the window.
type TuningShapeFact struct {
	Rank       int
	QueryHash  string // 0x...
	ObjectName string // schema.procedure; "" for ad hoc
	Text       string // the first 300 characters, on one line
	Executions int64
	CPU        int64 // microseconds
}

// TuningShapeHourFact is one top shape's executions and CPU in one hour.
type TuningShapeHourFact struct {
	QueryHash  string
	Hour       time.Time
	Executions int64
	CPU        int64 // microseconds
}

// PlanRegressionFact is one query whose newest plan costs more CPU per run than the one before
// (TuningReport.sql result 4).
type PlanRegressionFact struct {
	QueryID      int64
	QueryHash    string
	ObjectName   string
	Text         string
	OldPlanID    int64
	OldCPUMs     float64 // per run
	OldRuns      int64
	NewPlanID    int64
	NewCPUMs     float64
	NewRuns      int64
	NewFirstSeen time.Time // the start of the first interval it ran in
}

// QueryFailureFact is one query with aborted or failed executions in the window (TuningReport.sql
// result 5, with the first failure too).
type QueryFailureFact struct {
	QueryID     int64
	QueryHash   string
	ObjectName  string
	Text        string
	Aborted     int64 // timeouts and cancels
	Exceptions  int64 // errors
	FirstFailed *time.Time
	LastFailed  *time.Time
}

// BusinessHours are the hours of a working day, From inclusive to To exclusive, Monday to Friday.
type BusinessHours struct {
	From int
	To   int
}

// String writes the hours as the page option does: "08-17".
func (b BusinessHours) String() string { return fmt.Sprintf("%02d-%02d", b.From, b.To) }

// Class says whether an hour (its start) is a business hour, a night hour of a working day, or a
// weekend hour.
func (b BusinessHours) Class(t time.Time) string {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return HourWeekend
	}
	if t.Hour() >= b.From && t.Hour() < b.To {
		return HourBusiness
	}
	return HourNight
}

var businessHoursPattern = regexp.MustCompile(`^(\d{2})-(\d{2})$`)

// ParseBusinessHours reads the page option "HH-HH" (08-17); empty is the toolkit's default.
func ParseBusinessHours(s string) (BusinessHours, error) {
	if strings.TrimSpace(s) == "" {
		s = TuningDefaultBusinessHours
	}
	m := businessHoursPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return BusinessHours{}, fmt.Errorf("business hours are HH-HH, like 08-17, not %q", s)
	}
	from, _ := strconv.Atoi(m[1])
	to, _ := strconv.Atoi(m[2])
	if from >= to || to > 24 {
		return BusinessHours{}, fmt.Errorf("business hours %q: the start must be before the end, and the end at most 24", s)
	}
	return BusinessHours{From: from, To: to}, nil
}

// ParseTuningDays reads ?days=: 7 when empty, 1 to 30.
func ParseTuningDays(s string) (int, error) {
	if strings.TrimSpace(s) == "" {
		return TuningDefaultDays, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < TuningMinDays || n > TuningMaxDays {
		return 0, fmt.Errorf("days must be a whole number from %d to %d, not %q", TuningMinDays, TuningMaxDays, s)
	}
	return n, nil
}

// ParseTuningChange reads ?change=: 0 (the latest change) when empty, TuningChangeNone for "none",
// else a change id.
func ParseTuningChange(s string) (int64, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return 0, nil
	case strings.EqualFold(s, "none"):
		return TuningChangeNone, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("change must be a change id or none, not %q", s)
	}
	return n, nil
}

// RecordChangeInput is the body of POST .../changes: a change made outside kakapo.
type RecordChangeInput struct {
	ChangedAt   string `json:"changedAt"` // server time "2026-11-03T14:20"; RFC 3339 with an offset is exact; empty is now
	Object      string `json:"object"`
	ChangeType  string `json:"changeType"`
	Description string `json:"description"`
}

// RecordedChange is a checked RecordChangeInput. At is UTC when Exact, the server's clock (to be
// placed with its UTC offset) when not, and zero for now.
type RecordedChange struct {
	Object      string
	ChangeType  string
	Description string
	At          time.Time
	Exact       bool
}

var serverTimeLayouts = []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02 15:04:05"}

// ParseRecordChange checks a hand-recorded change: an object (at most 200 characters), a type of
// ChangeTypes, a description (at most 1,000 characters) and an optional time.
func ParseRecordChange(in RecordChangeInput) (RecordedChange, error) {
	r := RecordedChange{Object: strings.TrimSpace(in.Object), Description: strings.TrimSpace(in.Description),
		ChangeType: strings.ToUpper(strings.TrimSpace(in.ChangeType))}
	switch {
	case r.Object == "" || len([]rune(r.Object)) > 200:
		return r, errors.New("object must be 1 to 200 characters: the procedure, view, index or setting that changed")
	case !qContainsFold(ChangeTypes, r.ChangeType):
		return r, fmt.Errorf("changeType must be one of %s, not %q", strings.Join(ChangeTypes, ", "), in.ChangeType)
	case r.Description == "" || len([]rune(r.Description)) > 1000:
		return r, errors.New("description must be 1 to 1,000 characters: what changed, in a few words")
	}
	at := strings.TrimSpace(in.ChangedAt)
	if at == "" {
		return r, nil
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		r.At, r.Exact = t.UTC(), true
		return r, nil
	}
	for _, layout := range serverTimeLayouts {
		if t, err := time.Parse(layout, at); err == nil {
			r.At = t
			return r, nil
		}
	}
	return r, fmt.Errorf("changedAt must be server time like 2026-11-03T14:20, not %q", in.ChangedAt)
}

// Change is the change row for the log: no statement, Result "recorded by hand"; at is UTC.
func (r RecordedChange) Change(serverID int64, database string, at time.Time) Change {
	return Change{ServerID: serverID, Database: database, ChangedAt: at.UTC(), Object: r.Object, ChangeType: r.ChangeType,
		Description: r.Description, Result: ChangeRecordedByHand}
}

// TuningOptions are the page's choices.
type TuningOptions struct {
	ChangeID  int64 // 0: the latest change in the window; TuningChangeNone: none; else that change
	Business  BusinessHours
	CanChange bool // the database can take a fix (online, writable, not a secondary)
}

// TuningReport is the page (GET .../tuning). Times are strings of the server's clock without a
// zone ("2026-11-03T14:00:00"), so the browser shows them as the server does.
type TuningReport struct {
	Database         string             `json:"database"`
	ServerName       string             `json:"serverName"`
	CapturedAt       string             `json:"capturedAt"`
	UTCOffsetMinutes int                `json:"utcOffsetMinutes"`
	Days             int                `json:"days"`
	BusinessHours    string             `json:"businessHours"` // "08-17"
	QueryStoreState  string             `json:"queryStoreState"`
	IntervalMinutes  int                `json:"intervalMinutes"`
	OldestHour       string             `json:"oldestHour"` // "" when Query Store holds nothing
	WaitsRead        bool               `json:"waitsRead"`
	Changes          []TuningChange     `json:"changes"` // in the window, newest first
	Centre           TuningCentre       `json:"centre"`
	Before           TuningWindow       `json:"before"`
	Now              TuningWindow       `json:"now"`
	Tiles            []TuningTile       `json:"tiles"`
	Hours            []TuningHour       `json:"hours"`
	SameHour         []TuningSameHour   `json:"sameHour"`
	Queries          []TuningQuery      `json:"queries"`
	Regressions      []TuningRegression `json:"regressions"`
	Failures         []TuningFailure    `json:"failures"`
	Explain          []TuningLine       `json:"explain"`
	Notes            []string           `json:"notes"`
	Handover         string             `json:"handover"`
}

// TuningChange is a change in the window, as a marker and a choice of the page's select.
type TuningChange struct {
	ID          int64  `json:"id"`
	At          string `json:"at"`
	Object      string `json:"object"`
	ChangeType  string `json:"changeType"`
	Description string `json:"description"`
	ByHand      bool   `json:"byHand"`
	OnServer    bool   `json:"onServer"`    // seen on the server: read from the default trace
	ServerLevel bool   `json:"serverLevel"` // a change to the whole server (no database)
	Performance bool   `json:"performance"` // can affect performance; only these are the default centre
}

// TuningCentre is what the comparison is centred on.
type TuningCentre struct {
	ChangeID int64  `json:"changeId"` // 0: no change, the latest complete business hour
	At       string `json:"at"`       // the change's time, or the latest complete business hour; "" when neither
	Label    string `json:"label"`
}

// TuningWindow is Before or Now: the business hours compared.
type TuningWindow struct {
	Label string `json:"label"`
	Hours int    `json:"hours"`
}

// TuningTile is one of the four "is it better than before?" tiles.
type TuningTile struct {
	ID     string   `json:"id"` // cpu-per-execution, executions-per-hour, parallel-threads, lock-threads
	Label  string   `json:"label"`
	Unit   string   `json:"unit"`
	Before *float64 `json:"before"`
	Now    *float64 `json:"now"`
	Pct    *float64 `json:"pct"` // (now - before) / before, whole percent
	Tone   string   `json:"tone"`
	Words  string   `json:"words"` // "19 threads waiting on parallelism all the time"
	// BeforeText and NowText, when set, stand for the value: "none" (zero, or rounds to zero), "no
	// runs" (no hours or no executions in that window).
	BeforeText string `json:"beforeText,omitempty"`
	NowText    string `json:"nowText,omitempty"`
}

// TuningHour is one hour of the chart.
type TuningHour struct {
	Hour              string  `json:"hour"`
	Class             string  `json:"class"` // business, night, weekend
	Complete          bool    `json:"complete"`
	InProgress        bool    `json:"inProgress"`
	Change            bool    `json:"change"` // holds a change: never compared
	NoData            bool    `json:"noData"`
	Window            string  `json:"window"` // before, now, ""
	Executions        int64   `json:"executions"`
	CPUPerExecMs      float64 `json:"cpuPerExecMs"`
	ReadsPerExec      float64 `json:"readsPerExec"`
	DurationPerExecMs float64 `json:"durationPerExecMs"`
	ParallelThreads   float64 `json:"parallelThreads"`
	LockThreads       float64 `json:"lockThreads"`
	Failed            int64   `json:"failed"`
}

// TuningSameHour is one business hour of the day of the change beside the same hour on the working
// days before.
type TuningSameHour struct {
	Hour     string         `json:"hour"` // "08:00"
	Day      TuningHourCell `json:"day"`
	Previous TuningHourCell `json:"previous"`
}

// TuningHourCell is an hour's CPU per execution and executions; Previous averages the days (sum
// over the days, divided by the days for executions).
type TuningHourCell struct {
	CPUPerExecMs *float64 `json:"cpuPerExecMs"`
	Executions   *float64 `json:"executions"`
	Days         int      `json:"days"`
	Note         string   `json:"note"` // "change", "in progress", "not yet", "no data"
}

// TuningQuery is one top query shape before and now.
type TuningQuery struct {
	QueryHash          string   `json:"queryHash"`
	Name               string   `json:"name"`
	ObjectName         string   `json:"objectName"`
	Text               string   `json:"text"`
	RunsBefore         int64    `json:"runsBefore"`
	RunsNow            int64    `json:"runsNow"`
	CPUBeforeMs        *float64 `json:"cpuBeforeMs"` // per run, execution-weighted
	CPUNowMs           *float64 `json:"cpuNowMs"`
	ExecsPerHourBefore float64  `json:"execsPerHourBefore"`
	ExecsPerHourNow    float64  `json:"execsPerHourNow"`
	Verdict            string   `json:"verdict"`
	ImpactMsPerHour    float64  `json:"impactMsPerHour"` // CPU added (+) or saved (-) per business hour
	Copy               string   `json:"copy"`
}

// TuningRegression is a plan regression with its relation to the change.
type TuningRegression struct {
	QueryID     int64        `json:"queryId"`
	QueryHash   string       `json:"queryHash"`
	Name        string       `json:"name"`
	Text        string       `json:"text"`
	OldPlanID   int64        `json:"oldPlanId"`
	OldCPUMs    float64      `json:"oldCpuMs"`
	OldRuns     int64        `json:"oldRuns"`
	NewPlanID   int64        `json:"newPlanId"`
	NewCPUMs    float64      `json:"newCpuMs"`
	NewRuns     int64        `json:"newRuns"`
	FirstSeen   string       `json:"firstSeen"`
	Ratio       float64      `json:"ratio"`
	Relation    string       `json:"relation"` // "40 minutes after the change"
	AfterChange bool         `json:"afterChange"`
	Action      *QueryAction `json:"action,omitempty"` // force the old plan
	Copy        string       `json:"copy"`
}

// TuningFailure is a query with failed executions, with its relation to the change.
type TuningFailure struct {
	QueryID     int64  `json:"queryId"`
	QueryHash   string `json:"queryHash"`
	Name        string `json:"name"`
	Text        string `json:"text"`
	Aborted     int64  `json:"aborted"`
	Exceptions  int64  `json:"exceptions"`
	FirstFailed string `json:"firstFailed"`
	LastFailed  string `json:"lastFailed"`
	Relation    string `json:"relation"`
	AfterChange bool   `json:"afterChange"`
	Copy        string `json:"copy"`
}

// TuningLine is one plain sentence of the report. A sentence that names a query carries its hash
// (Investigate opens the query page) and a Copy block for the vendor; Action is a fix; RecordChange
// opens the Record a change form.
type TuningLine struct {
	Text         string       `json:"text"`
	Action       *QueryAction `json:"action,omitempty"`
	QueryHash    string       `json:"queryHash,omitempty"`
	Copy         string       `json:"copy,omitempty"`
	RecordChange bool         `json:"recordChange,omitempty"`
}

// tHour is one hour of the window as the comparison sees it.
type tHour struct {
	at                             time.Time
	f                              TuningHourFact
	has, complete, inProgress, chg bool
	class                          string
}

// tChange is a change on the server's clock.
type tChange struct {
	c  Change
	at time.Time
}

// tSum is the sums over a set of hours.
type tSum struct {
	hours                 int
	execs, cpu            int64
	parallelSec, lockSec  float64
	reads, duration, fail int64
}

// BuildTuningReport compares the hours of the report (docs/phase5c-contract.md, "The comparison"):
// the window around the chosen change (or the latest complete business hour), the four tiles from
// sums, the same hour before and after, the per-query verdicts, the plan regressions and failures
// with their relation to the change, the sentences and the hand-over. changes are the change log's
// rows in the window (UTC), any order.
func BuildTuningReport(f TuningFacts, changes []Change, opt TuningOptions) TuningReport {
	bh := opt.Business
	if bh.To == 0 {
		bh, _ = ParseBusinessHours("")
	}
	r := TuningReport{
		Database: f.Database, ServerName: f.ServerName, CapturedAt: tStamp(f.ServerNow), UTCOffsetMinutes: f.UTCOffsetMinutes,
		Days: f.Days, BusinessHours: bh.String(), QueryStoreState: f.QueryStoreState, IntervalMinutes: f.IntervalMinutes,
		Changes: []TuningChange{}, Tiles: []TuningTile{}, Hours: []TuningHour{}, SameHour: []TuningSameHour{},
		Queries: []TuningQuery{}, Regressions: []TuningRegression{}, Failures: []TuningFailure{}, Explain: []TuningLine{},
		Notes: []string{},
	}
	if f.OldestInterval != nil {
		r.OldestHour = tStamp(f.OldestInterval.Truncate(time.Hour))
	}
	r.WaitsRead = f.WaitsError == "" && f.HoursError == "" && strings.EqualFold(f.WaitCapture, "ON")

	// The changes on the server's clock, newest first; the hours they fall in are never compared.
	off := time.Duration(f.UTCOffsetMinutes) * time.Minute
	tcs := make([]tChange, 0, len(changes))
	for _, c := range changes {
		tcs = append(tcs, tChange{c: c, at: naiveUTC(c.ChangedAt.UTC()).Add(off)})
	}
	sort.SliceStable(tcs, func(i, j int) bool {
		if !tcs[i].at.Equal(tcs[j].at) {
			return tcs[i].at.After(tcs[j].at)
		}
		return tcs[i].c.ID > tcs[j].c.ID
	})
	changeHours := map[time.Time]bool{}
	for _, c := range tcs {
		changeHours[c.at.Truncate(time.Hour)] = true
		r.Changes = append(r.Changes, TuningChange{ID: c.c.ID, At: tStamp(c.at), Object: c.c.Object, ChangeType: c.c.ChangeType,
			Description: c.c.Description, ByHand: c.c.Result == ChangeRecordedByHand, OnServer: c.c.Result == ChangeSeenOnServer, ServerLevel: c.c.Database == "",
			Performance: IsPerformanceChange(c.c)})
	}

	hours := tBuildHours(f, bh, changeHours)

	var centre *tChange
	switch {
	case opt.ChangeID == TuningChangeNone:
	case opt.ChangeID > 0:
		for i := range tcs {
			if tcs[i].c.ID == opt.ChangeID {
				centre = &tcs[i]
			}
		}
	default:
		for i := range tcs {
			if IsPerformanceChange(tcs[i].c) {
				centre = &tcs[i]
				break
			}
		}
	}

	before, now, beforeLabel, nowLabel, latest := tWindows(hours, centre, tcs)
	r.Before = TuningWindow{Label: beforeLabel, Hours: len(before)}
	r.Now = TuningWindow{Label: nowLabel, Hours: len(now)}
	switch {
	case centre != nil:
		r.Centre = TuningCentre{ChangeID: centre.c.ID, At: tStamp(centre.at), Label: tChangeName(centre.c) + " on " + tWhen(centre.at)}
	case latest != nil:
		r.Centre = TuningCentre{At: tStamp(*latest), Label: "the latest complete business hour (" + tWhen(*latest) + ")"}
	default:
		r.Centre = TuningCentre{Label: "no complete business hour yet"}
	}
	window := map[time.Time]string{}
	for _, i := range before {
		window[hours[i].at] = "before"
	}
	for _, i := range now {
		window[hours[i].at] = "now"
	}

	for _, h := range hours {
		th := TuningHour{Hour: tStamp(h.at), Class: h.class, Complete: h.complete, InProgress: h.inProgress, Change: h.chg,
			NoData: !h.has, Window: window[h.at], Executions: h.f.Executions, Failed: h.f.Aborted + h.f.Exceptions}
		if h.f.Executions > 0 {
			e := float64(h.f.Executions)
			th.CPUPerExecMs = roundHalfAway(float64(h.f.CPU)/e/1000, 3)
			th.ReadsPerExec = roundHalfAway(float64(h.f.Reads)/e, 1)
			th.DurationPerExecMs = roundHalfAway(float64(h.f.Duration)/e/1000, 3)
		}
		if r.WaitsRead {
			th.ParallelThreads = roundHalfAway(h.f.ParallelWaitSec/3600, 2)
			th.LockThreads = roundHalfAway(h.f.LockWaitSec/3600, 2)
		}
		r.Hours = append(r.Hours, th)
	}

	bs, ns := tSumOf(hours, before), tSumOf(hours, now)
	r.Tiles = tTiles(bs, ns, r.WaitsRead)
	r.SameHour = tSameHour(hours, centre, latest, bh, f.ServerNow)
	r.Queries = tQueries(f, hours, before, now)

	interval := time.Duration(max(f.IntervalMinutes, 1)) * time.Minute
	qsWritable := strings.EqualFold(f.QueryStoreState, "READ_WRITE")
	for _, g := range f.Regressions {
		if !PlanRegressed(g.OldCPUMs, g.NewCPUMs, g.NewRuns) {
			continue
		}
		tr := TuningRegression{QueryID: g.QueryID, QueryHash: g.QueryHash, Name: tQueryName(g.ObjectName, g.Text), Text: g.Text,
			OldPlanID: g.OldPlanID, OldCPUMs: roundHalfAway(g.OldCPUMs, 2), OldRuns: g.OldRuns, NewPlanID: g.NewPlanID,
			NewCPUMs: roundHalfAway(g.NewCPUMs, 2), NewRuns: g.NewRuns, FirstSeen: tStamp(g.NewFirstSeen),
			Ratio: roundHalfAway(g.NewCPUMs/g.OldCPUMs, 1)}
		tr.Relation, tr.AfterChange = tRelation(g.NewFirstSeen, centre, interval)
		if opt.CanChange && qsWritable && g.OldPlanID > 0 {
			a := qForceAction(g.OldPlanID)
			a.Label = "Force the old plan"
			tr.Action = &a
		}
		r.Regressions = append(r.Regressions, tr)
	}
	sort.SliceStable(r.Regressions, func(i, j int) bool {
		a, b := r.Regressions[i], r.Regressions[j]
		return (a.NewCPUMs-a.OldCPUMs)*float64(a.NewRuns) > (b.NewCPUMs-b.OldCPUMs)*float64(b.NewRuns)
	})
	if len(r.Regressions) > 10 {
		r.Regressions = r.Regressions[:10]
	}
	for _, fl := range f.Failures {
		tf := TuningFailure{QueryID: fl.QueryID, QueryHash: fl.QueryHash, Name: tQueryName(fl.ObjectName, fl.Text), Text: fl.Text,
			Aborted: fl.Aborted, Exceptions: fl.Exceptions}
		if fl.FirstFailed != nil {
			tf.FirstFailed = tStamp(*fl.FirstFailed)
			tf.Relation, tf.AfterChange = tRelation(*fl.FirstFailed, centre, interval)
		}
		if fl.LastFailed != nil {
			tf.LastFailed = tStamp(*fl.LastFailed)
		}
		r.Failures = append(r.Failures, tf)
	}

	for _, n := range []struct{ what, err string }{
		{"Query Store settings", f.AboutError}, {"Hour by hour", f.HoursError}, {"Waits", f.WaitsError},
		{"What is expensive", f.ShapesError}, {"Plan regressions", f.RegressionsError}, {"Failures", f.FailuresError},
	} {
		if n.err != "" {
			r.Notes = append(r.Notes, n.what+": could not be read: "+n.err)
		}
	}
	if f.WaitsError == "" && f.HoursError == "" && !strings.EqualFold(f.WaitCapture, "ON") {
		r.Notes = append(r.Notes, "Waits: Query Store is not recording waits here (WAIT_STATS_CAPTURE_MODE is off), so the threads waiting tiles are empty.")
	}

	r.Explain = tExplain(f, &r, centre, latest, bs, ns, hours, bh, tcs, opt.CanChange)
	for i := range r.Queries {
		q := &r.Queries[i]
		q.Copy = tQueryCopy(r.Database, q.Name, q.QueryHash, q.Text, tQueryNumbers(*q))
	}
	for i := range r.Regressions {
		g := &r.Regressions[i]
		g.Copy = tQueryCopy(r.Database, g.Name, g.QueryHash, g.Text, fmt.Sprintf("Plan %d uses %s x the CPU per run of plan %d (%s against %s, %s runs); first seen %s, %s.",
			g.NewPlanID, qRatioText(g.Ratio), g.OldPlanID, tMs(g.NewCPUMs), tMs(g.OldCPUMs), n0int(g.NewRuns), tWhenStamp(g.FirstSeen), g.Relation))
	}
	for i := range r.Failures {
		fl := &r.Failures[i]
		fl.Copy = tQueryCopy(r.Database, fl.Name, fl.QueryHash, fl.Text, fmt.Sprintf("%s aborted (timeouts, cancels) and %s ended in an error; last at %s.",
			n0int(fl.Aborted), n0int(fl.Exceptions), tWhenStamp(fl.LastFailed)))
	}
	r.Handover = TuningHandover(r)
	return r
}

// PlanRegressed is the toolkit's rule 4: the newest plan ran at least 50 times and uses at least
// 1.5 x the CPU per run of the previous plan, which used at least 1 ms.
func PlanRegressed(oldCPUMs, newCPUMs float64, newRuns int64) bool {
	return newRuns >= PlanRegressionMinRuns && oldCPUMs >= PlanRegressionMinOldCPUMs && newCPUMs >= oldCPUMs*PlanRegressionRatio
}

// TuningVerdict judges one query from its runs and CPU per run in each window.
func TuningVerdict(runsBefore, runsNow int64, cpuBefore, cpuNow float64) string {
	switch {
	case runsNow >= TuningMinRuns && runsBefore == 0:
		return VerdictNew
	case runsBefore >= TuningMinRuns && runsNow == 0:
		return VerdictGone
	case runsBefore < TuningMinRuns || runsNow < TuningMinRuns:
		return VerdictTooFew
	case cpuNow <= TuningFasterRatio*cpuBefore:
		return VerdictFaster
	case cpuNow >= TuningSlowerRatio*cpuBefore:
		return VerdictSlower
	}
	return VerdictSame
}

// WorkingDaysBefore are the n Monday-to-Friday dates before a day, nearest first.
func WorkingDaysBefore(day time.Time, n int) []time.Time {
	d := tDate(day)
	out := []time.Time{}
	for len(out) < n {
		d = d.AddDate(0, 0, -1)
		if wd := d.Weekday(); wd != time.Saturday && wd != time.Sunday {
			out = append(out, d)
		}
	}
	return out
}

// tBuildHours lays the read's hours out one per hour, from the start of the window to the hour in
// progress, each classed, complete or not, and marked when it holds a change.
func tBuildHours(f TuningFacts, bh BusinessHours, changeHours map[time.Time]bool) []tHour {
	now := f.ServerNow
	current := now.Truncate(time.Hour)
	start := current.Add(-time.Duration(max(f.Days, 1)) * 24 * time.Hour)
	byHour := map[time.Time]TuningHourFact{}
	for _, h := range f.Hours {
		at := naiveUTC(h.Hour).Truncate(time.Hour)
		if at.Before(start) || at.After(current) {
			continue
		}
		s := byHour[at]
		s.Hour = at
		s.Executions += h.Executions
		s.CPU += h.CPU
		s.Reads += h.Reads
		s.Duration += h.Duration
		s.Aborted += h.Aborted
		s.Exceptions += h.Exceptions
		s.ParallelWaitSec += h.ParallelWaitSec
		s.LockWaitSec += h.LockWaitSec
		s.CPUWaitSec += h.CPUWaitSec
		s.OtherWaitSec += h.OtherWaitSec
		byHour[at] = s
	}
	// Every hour of the window, so the week reads as a timeline: Query Store keeps an interval only
	// when something ran, so an hour without one is idle (noData), not missing.
	out := []tHour{}
	for at := start; !at.After(current); at = at.Add(time.Hour) {
		fact, has := byHour[at]
		end := at.Add(time.Hour)
		out = append(out, tHour{at: at, f: fact, has: has, complete: !end.After(now), inProgress: !at.After(now) && end.After(now),
			chg: changeHours[at], class: bh.Class(at)})
	}
	return out
}

// tNextChangeHour is the hour of the next change that can affect performance after the centre (zero
// when none): Now stops there, so a later change is not credited to this one.
func tNextChangeHour(centre *tChange, changes []tChange) time.Time {
	var bound time.Time
	for _, c := range changes {
		if c.at.After(centre.at) && IsPerformanceChange(c.c) && (bound.IsZero() || c.at.Before(bound)) {
			bound = c.at
		}
	}
	return bound.Truncate(time.Hour)
}

func tUsable(h tHour) bool { return h.has && h.complete && !h.chg && h.class == HourBusiness }

// tWindows chooses Before and Now (indexes into hours) and labels them. Around a change: Now is the
// complete business hours after it (up to the next change), Before the complete business hours
// earlier the same day when there are at least two, else the business hours of the five working
// days before. Without one: Now is the latest complete business hour, Before the same hour on the
// five working days before. latest is that hour, when there is no change.
func tWindows(hours []tHour, centre *tChange, changes []tChange) (before, now []int, beforeLabel, nowLabel string, latest *time.Time) {
	if centre != nil {
		cH := centre.at.Truncate(time.Hour)
		bound := tNextChangeHour(centre, changes)
		sameDay := []int{}
		for i, h := range hours {
			if !tUsable(h) {
				continue
			}
			if h.at.After(cH) && (bound.IsZero() || h.at.Before(bound)) {
				now = append(now, i)
			}
			if h.at.Before(cH) && tDate(h.at).Equal(tDate(centre.at)) {
				sameDay = append(sameDay, i)
			}
		}
		nowLabel = plural(int64(len(now)), "busy business hour", "busy business hours") + " since the change"
		if len(now) > 0 {
			nowLabel += " (" + tRange(hours[now[0]].at, hours[now[len(now)-1]].at.Add(time.Hour)) + ")"
		}
		if !bound.IsZero() {
			nowLabel += ", up to the next change"
		}
		if len(sameDay) >= TuningSameDayMinHours {
			before = sameDay
			beforeLabel = plural(int64(len(before)), "business hour", "business hours") + " earlier the same day (" +
				tRange(hours[before[0]].at, hours[before[len(before)-1]].at.Add(time.Hour)) + ")"
			return
		}
		days := WorkingDaysBefore(centre.at, TuningWorkingDays)
		before = tHoursOnDays(hours, days, -1)
		beforeLabel = plural(int64(len(before)), "business hour", "business hours") + " on the five working days before (" +
			days[len(days)-1].Format("Mon 2 Jan") + " to " + days[0].Format("Mon 2 Jan") + ")"
		return
	}
	for i := len(hours) - 1; i >= 0; i-- {
		if tUsable(hours[i]) {
			now = []int{i}
			at := hours[i].at
			latest = &at
			break
		}
	}
	if latest == nil {
		return nil, nil, "no business hours", "no complete business hour", nil
	}
	nowLabel = "the latest complete business hour (" + tRange(*latest, latest.Add(time.Hour)) + ")"
	days := WorkingDaysBefore(*latest, TuningWorkingDays)
	before = tHoursOnDays(hours, days, latest.Hour())
	beforeLabel = "the same hour on the five working days before (" + plural(int64(len(before)), "hour", "hours") + " with history)"
	return
}

// tHoursOnDays are the usable hours on the given dates; hour >= 0 keeps only that hour of the day.
func tHoursOnDays(hours []tHour, days []time.Time, hour int) []int {
	on := map[time.Time]bool{}
	for _, d := range days {
		on[d] = true
	}
	out := []int{}
	for i, h := range hours {
		if tUsable(h) && on[tDate(h.at)] && (hour < 0 || h.at.Hour() == hour) {
			out = append(out, i)
		}
	}
	return out
}

func tSumOf(hours []tHour, idx []int) tSum {
	s := tSum{hours: len(idx)}
	for _, i := range idx {
		f := hours[i].f
		s.execs += f.Executions
		s.cpu += f.CPU
		s.reads += f.Reads
		s.duration += f.Duration
		s.fail += f.Aborted + f.Exceptions
		s.parallelSec += f.ParallelWaitSec
		s.lockSec += f.LockWaitSec
	}
	return s
}

// cpuPerExecMs is total CPU / total executions, never an average of hourly ratios.
func (s tSum) cpuPerExecMs() (float64, bool) {
	if s.execs <= 0 {
		return 0, false
	}
	return float64(s.cpu) / float64(s.execs) / 1000, true
}

func (s tSum) execsPerHour() (float64, bool) {
	if s.hours == 0 {
		return 0, false
	}
	return float64(s.execs) / float64(s.hours), true
}

// threads is wait seconds / 3,600 per hour: "19 threads waiting all the time".
func (s tSum) threads(sec float64) (float64, bool) {
	if s.hours == 0 {
		return 0, false
	}
	return sec / 3600 / float64(s.hours), true
}

func tTiles(b, n tSum, waits bool) []TuningTile {
	tile := func(id, label, unit string, bv float64, bok bool, nv float64, nok bool, decimals int, lowerBetter bool) TuningTile {
		t := TuningTile{ID: id, Label: label, Unit: unit, Tone: ToneSame}
		br, nr := roundHalfAway(bv, decimals), roundHalfAway(nv, decimals)
		if bok {
			t.Before = &br
		} else {
			t.BeforeText = "no runs"
		}
		if nok {
			t.Now = &nr
		} else {
			t.NowText = "no runs"
		}
		if bok && br == 0 {
			t.BeforeText = "none"
		}
		if nok && nr == 0 {
			t.NowText = "none"
		}
		switch {
		case !bok || !nok, br == 0 && nr == 0:
			// Nothing to compare, or nothing on either side: the same, without a percentage.
		case br == 0:
			// From nothing to something: no percentage, but not the same either.
			t.Tone = tTone(100, lowerBetter)
		default:
			p := (nr - br) / br * 100
			pr := roundHalfAway(p, 0)
			t.Pct = &pr
			t.Tone = tTone(p, lowerBetter)
		}
		return t
	}
	bc, bcok := b.cpuPerExecMs()
	nc, ncok := n.cpuPerExecMs()
	be, beok := b.execsPerHour()
	ne, neok := n.execsPerHour()
	cpu := tile("cpu-per-execution", "CPU per execution", "ms", bc, bcok, nc, ncok, 2, true)
	if ncok {
		cpu.Words = tMs(nc) + " of CPU per execution"
	}
	ex := tile("executions-per-hour", "Executions per hour", "per hour", be, beok, ne, neok, 0, false)
	if neok {
		ex.Words = n0(ne) + " executions an hour"
	}
	bp, bpok := b.threads(b.parallelSec)
	np, npok := n.threads(n.parallelSec)
	bl, blok := b.threads(b.lockSec)
	nl, nlok := n.threads(n.lockSec)
	if !waits {
		bpok, npok, blok, nlok = false, false, false, false
	}
	par := tile("parallel-threads", "Parallel threads waiting", "threads", bp, bpok, np, npok, 1, true)
	lock := tile("lock-threads", "Lock threads waiting", "threads", bl, blok, nl, nlok, 1, true)
	if npok {
		par.Words = tThreads(np) + " on parallelism all the time"
	}
	if nlok {
		lock.Words = tThreads(nl) + " on locks all the time"
	}
	if !waits {
		par.Words = "Query Store keeps no waits here (SQL Server 2017 and wait capture on)"
		lock.Words = par.Words
		par.BeforeText, par.NowText, lock.BeforeText, lock.NowText = "", "", "", ""
	}
	return []TuningTile{cpu, ex, par, lock}
}

// tTone: better from a 10% improvement, worse from 10% the other way, else the same. Executions
// are more or fewer, never better or worse.
func tTone(pct float64, lowerBetter bool) string {
	switch {
	case !lowerBetter && pct >= TuningTonePct:
		return ToneMore
	case !lowerBetter && pct <= -TuningTonePct:
		return ToneFewer
	case !lowerBetter:
		return ToneSame
	case pct <= -TuningTonePct:
		return ToneBetter
	case pct >= TuningTonePct:
		return ToneWorse
	}
	return ToneSame
}

func tThreads(n float64) string {
	v := roundHalfAway(n, 1)
	if v == 0 {
		return "no threads waiting"
	}
	if v == 1 {
		return "1 thread waiting"
	}
	return strings.TrimSuffix(n1(v), ".0") + " threads waiting"
}

// tSameHour is each business hour of the day of the change (the next working day for a change at a
// weekend; without a change, the day of the latest complete business hour) beside the same hour on
// the five working days before.
func tSameHour(hours []tHour, centre *tChange, latest *time.Time, bh BusinessHours, now time.Time) []TuningSameHour {
	var day time.Time
	switch {
	case centre != nil:
		day = tDate(centre.at)
		for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			day = day.AddDate(0, 0, 1)
		}
	case latest != nil:
		day = tDate(*latest)
	default:
		return []TuningSameHour{}
	}
	byHour := map[time.Time]tHour{}
	for _, h := range hours {
		byHour[h.at] = h
	}
	prev := WorkingDaysBefore(day, TuningWorkingDays)
	out := []TuningSameHour{}
	for hr := bh.From; hr < bh.To; hr++ {
		at := day.Add(time.Duration(hr) * time.Hour)
		row := TuningSameHour{Hour: fmt.Sprintf("%02d:00", hr)}
		h, ok := byHour[at]
		switch {
		case ok && h.inProgress:
			row.Day.Note = "in progress"
		case at.Add(time.Hour).After(now):
			row.Day.Note = "not yet"
		case !ok:
			row.Day.Note = "no data"
		case !h.has:
			// Complete, but nothing ran: an idle hour, shown as zero runs.
			row.Day.Note = "idle"
			zero := 0.0
			row.Day.Executions = &zero
		}
		if ok && h.has {
			if h.chg {
				row.Day.Note = "change"
			}
			if h.f.Executions > 0 {
				c := roundHalfAway(float64(h.f.CPU)/float64(h.f.Executions)/1000, 2)
				row.Day.CPUPerExecMs = &c
			}
			e := float64(h.f.Executions)
			row.Day.Executions = &e
			row.Day.Days = 1
		}
		// The days before: idle hours count as days with no runs, so executions are per day of the
		// window; CPU per execution is still total CPU over total runs.
		var s tSum
		for _, d := range prev {
			p, ok := byHour[d.Add(time.Duration(hr)*time.Hour)]
			if !ok || !p.complete || p.chg {
				continue
			}
			s.hours++
			s.execs += p.f.Executions
			s.cpu += p.f.CPU
		}
		row.Previous.Days = s.hours
		if s.hours == 0 {
			row.Previous.Note = "no data"
		} else {
			if s.execs == 0 {
				row.Previous.Note = "idle"
			}
			e := roundHalfAway(float64(s.execs)/float64(s.hours), 0)
			row.Previous.Executions = &e
			if c, ok := s.cpuPerExecMs(); ok {
				c = roundHalfAway(c, 2)
				row.Previous.CPUPerExecMs = &c
			}
		}
		out = append(out, row)
	}
	return out
}

// tQueries is the top shapes before and now, execution-weighted (total CPU / total runs over the
// window's hours), with a verdict each, sorted by the CPU saved or added per business hour.
func tQueries(f TuningFacts, hours []tHour, before, now []int) []TuningQuery {
	inBefore, inNow := map[time.Time]bool{}, map[time.Time]bool{}
	for _, i := range before {
		inBefore[hours[i].at] = true
	}
	for _, i := range now {
		inNow[hours[i].at] = true
	}
	type acc struct{ rb, rn, cb, cn int64 }
	sums := map[string]*acc{}
	for _, sh := range f.ShapeHours {
		key := strings.ToUpper(sh.QueryHash)
		a := sums[key]
		if a == nil {
			a = &acc{}
			sums[key] = a
		}
		at := naiveUTC(sh.Hour).Truncate(time.Hour)
		if inBefore[at] {
			a.rb += sh.Executions
			a.cb += sh.CPU
		}
		if inNow[at] {
			a.rn += sh.Executions
			a.cn += sh.CPU
		}
	}
	out := []TuningQuery{}
	rank := map[string]int{}
	for _, s := range f.Shapes {
		a := sums[strings.ToUpper(s.QueryHash)]
		if a == nil {
			a = &acc{}
		}
		q := TuningQuery{QueryHash: s.QueryHash, Name: tQueryName(s.ObjectName, s.Text), ObjectName: s.ObjectName, Text: s.Text,
			RunsBefore: a.rb, RunsNow: a.rn}
		var cb, cn float64
		if a.rb > 0 {
			cb = float64(a.cb) / float64(a.rb) / 1000
			v := roundHalfAway(cb, 2)
			q.CPUBeforeMs = &v
		}
		if a.rn > 0 {
			cn = float64(a.cn) / float64(a.rn) / 1000
			v := roundHalfAway(cn, 2)
			q.CPUNowMs = &v
		}
		if len(before) > 0 {
			q.ExecsPerHourBefore = roundHalfAway(float64(a.rb)/float64(len(before)), 1)
		}
		if len(now) > 0 {
			q.ExecsPerHourNow = roundHalfAway(float64(a.rn)/float64(len(now)), 1)
		}
		q.Verdict = TuningVerdict(a.rb, a.rn, cb, cn)
		switch q.Verdict {
		case VerdictFaster, VerdictSlower, VerdictSame:
			q.ImpactMsPerHour = (cn - cb) * float64(a.rn) / float64(len(now))
		case VerdictNew:
			q.ImpactMsPerHour = float64(a.cn) / 1000 / float64(len(now))
		case VerdictGone:
			q.ImpactMsPerHour = -float64(a.cb) / 1000 / float64(len(before))
		}
		q.ImpactMsPerHour = roundHalfAway(q.ImpactMsPerHour, 1)
		rank[q.QueryHash] = s.Rank
		out = append(out, q)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Verdict == VerdictTooFew) != (b.Verdict == VerdictTooFew) {
			return b.Verdict == VerdictTooFew
		}
		if math.Abs(a.ImpactMsPerHour) != math.Abs(b.ImpactMsPerHour) {
			return math.Abs(a.ImpactMsPerHour) > math.Abs(b.ImpactMsPerHour)
		}
		return rank[a.QueryHash] < rank[b.QueryHash]
	})
	return out
}

// tRelation says when something appeared against the change: "40 minutes after the change", "in
// the same 60-minute interval as the change", "2 days before the change"; without a change, when it
// was first seen. t is the start of the Query Store interval it was first seen in, so "after" is any
// interval that ends after the change.
func tRelation(t time.Time, centre *tChange, interval time.Duration) (string, bool) {
	if centre == nil {
		return "first seen " + tWhen(t), false
	}
	switch {
	case !t.After(centre.at) && t.Add(interval).After(centre.at):
		return fmt.Sprintf("in the same %d-minute interval as the change", int(interval.Minutes())), true
	case t.After(centre.at):
		return tDuration(t.Sub(centre.at)) + " after the change", true
	}
	return tDuration(centre.at.Sub(t)) + " before the change", false
}

func tExplain(f TuningFacts, r *TuningReport, centre *tChange, latest *time.Time, bs, ns tSum, hours []tHour, bh BusinessHours, tcs []tChange, canChange bool) []TuningLine {
	out := []TuningLine{}
	add := func(l TuningLine) { out = append(out, l) }
	since := "since the change"
	if centre == nil {
		since = "in the latest hour"
	}

	// The headline.
	bc, bok := bs.cpuPerExecMs()
	nc, nok := ns.cpuPerExecMs()
	be, _ := bs.execsPerHour()
	ne, _ := ns.execsPerHour()
	switch {
	case centre != nil && nok && !bok:
		add(TuningLine{Text: fmt.Sprintf("There is nothing to compare %s with: Query Store holds no business hours before it in the last %s.",
			tChangeName(centre.c), plural(int64(f.Days), "day", "days"))})
	case centre != nil && bok && nok:
		add(TuningLine{Text: fmt.Sprintf("Since %s on %s, CPU per execution in business hours %s over %s; %s",
			tChangeName(centre.c), tWhen(centre.at), tCPUChange(bc, nc), plural(int64(ns.hours), "hour", "hours"), tExecsClause(bc, nc, be, ne))})
	case centre == nil && latest != nil && bok && nok:
		add(TuningLine{Text: fmt.Sprintf("In the latest complete business hour (%s), CPU per execution %s against the same hour on the five working days before; %s",
			tWhen(*latest), tCPUChange(bc, nc), tExecsClause(bc, nc, be, ne))})
	case centre == nil && latest != nil && nok:
		add(TuningLine{Text: fmt.Sprintf("There is nothing to compare the latest business hour (%s) with: Query Store holds no history for that hour on the five working days before.",
			tWhen(*latest))})
	case centre == nil && latest == nil && f.HoursError == "":
		add(TuningLine{Text: fmt.Sprintf("Query Store holds no complete business hour of %s in the last %s, so there is nothing to compare yet.",
			f.Database, plural(int64(f.Days), "day", "days"))})
	}

	// The queries that got faster and slower, by name.
	for _, verdict := range []string{VerdictFaster, VerdictSlower} {
		n := 0
		for _, q := range r.Queries {
			if q.Verdict != verdict || n >= 3 || q.CPUBeforeMs == nil || q.CPUNowMs == nil {
				continue
			}
			n++
			text := fmt.Sprintf("%s got %s %s: %s of CPU per run before, %s now, over %s.", tCap(q.Name), verdict, since,
				tMs(*q.CPUBeforeMs), tMs(*q.CPUNowMs), plural(q.RunsNow, "run", "runs"))
			if verdict == VerdictSlower {
				text += " Investigate it."
			}
			add(TuningLine{Text: text, QueryHash: q.QueryHash, Copy: tQueryCopy(r.Database, q.Name, q.QueryHash, q.Text, text)})
		}
	}

	// A new plan that costs more since the change.
	n := 0
	for _, g := range r.Regressions {
		if n >= 3 || (centre != nil && !g.AfterChange) {
			continue
		}
		n++
		text := fmt.Sprintf("The new plan of %s, first seen %s, uses %s x the CPU per run of the one before: force the old plan.",
			g.Name, tWhenStamp(g.FirstSeen), qRatioText(g.Ratio))
		if centre != nil {
			text = fmt.Sprintf("The new plan of %s uses %s x the CPU per run of the one before and appeared %s: force the old plan, or revert.",
				g.Name, qRatioText(g.Ratio), g.Relation)
		}
		add(TuningLine{Text: text, Action: g.Action, QueryHash: g.QueryHash, Copy: tQueryCopy(r.Database, g.Name, g.QueryHash, g.Text, text)})
	}

	// Failures that started after the change.
	if centre != nil {
		n = 0
		for _, fl := range r.Failures {
			if n >= 3 || !fl.AfterChange {
				continue
			}
			n++
			text := fmt.Sprintf("%s has failed %s since the change (%s timed out or were cancelled, %s ended in an error), first at %s.",
				tCap(fl.Name), plural(fl.Aborted+fl.Exceptions, "time", "times"), n0int(fl.Aborted), n0int(fl.Exceptions), tWhenStamp(fl.FirstFailed))
			add(TuningLine{Text: text, QueryHash: fl.QueryHash, Copy: tQueryCopy(r.Database, fl.Name, fl.QueryHash, fl.Text, text)})
		}
	}

	// Too early to say: business hours that have not passed yet, or passed without a query (Query
	// Store keeps an interval only when something ran, so Now counts busy hours, not hours passed).
	if centre != nil && ns.hours < TuningSameDayMinHours {
		changed := map[time.Time]bool{}
		for _, h := range hours {
			if h.chg {
				changed[h.at] = true
			}
		}
		passed := tBusinessHoursPassed(centre.at, tNextChangeHour(centre, tcs), bh, changed, f.ServerNow)
		if passed >= TuningSameDayMinHours {
			busy := "none"
			if ns.hours > 0 {
				busy = "only " + strconv.Itoa(ns.hours)
			}
			add(TuningLine{Text: fmt.Sprintf("Since the change at %s, %d business hours have passed but %s held any query; kakapo needs two busy hours to compare. Look again after a busy morning.",
				tWhen(centre.at), passed, busy)})
		} else {
			what := "there is no complete business hour since the change yet"
			if passed == 1 {
				what = "only one complete business hour has passed since the change"
			}
			add(TuningLine{Text: "It is too early to say: " + what + ". Look again after " + tWhen(tLookAgain(centre.at, bh, changed, f.ServerNow)) + "."})
		}
	}

	// No change that can affect performance in the log.
	perf := 0
	for _, c := range r.Changes {
		if c.Performance {
			perf++
		}
	}
	if len(r.Changes) > 0 && perf == 0 {
		add(TuningLine{Text: fmt.Sprintf("No change in the log for the last %s can affect performance (only checks, jobs and the like), so this compares the latest complete business hour with the same hour on the five working days before. Record a change made by hand to measure it.",
			plural(int64(f.Days), "day", "days")), RecordChange: true})
	}

	// No change in the log.
	if len(r.Changes) == 0 {
		add(TuningLine{Text: fmt.Sprintf("No change is in the log for the last %s, so this compares the latest complete business hour with the same hour on the five working days before. Record a change made by hand to measure it.",
			plural(int64(f.Days), "day", "days")), RecordChange: true})
	}

	// Query Store is off.
	if strings.EqualFold(f.QueryStoreState, "OFF") {
		l := TuningLine{Text: "Query Store is off for " + f.Database + ", so only the history from when it was last on is here and nothing new is recorded. Turn it on before the next change to measure it."}
		if canChange {
			l.Action = &QueryAction{FixID: FixQueryStore, Label: "Turn Query Store on", Title: "Apply recommended Query Store settings", Options: map[string]string{}}
		}
		add(l)
	}
	return out
}

// tCPUChange: "is down 31% (from 12.4 ms to 8.6 ms)", "is up 12% (...)", "is about the same (12.4
// ms before, 12.1 ms now)".
func tCPUChange(before, now float64) string {
	p := (now - before) / before * 100
	switch tTone(p, true) {
	case ToneBetter:
		return fmt.Sprintf("is down %s (from %s to %s)", tPct(p), tMs(before), tMs(now))
	case ToneWorse:
		return fmt.Sprintf("is up %s (from %s to %s)", tPct(p), tMs(before), tMs(now))
	}
	return fmt.Sprintf("is about the same (%s before, %s now)", tMs(before), tMs(now))
}

// tExecsClause says what executions per hour did and what that means with the CPU per execution.
func tExecsClause(bc, nc, be, ne float64) string {
	cpu := tTone((nc-bc)/bc*100, true)
	ex := ToneSame
	pe := 0.0
	if be > 0 {
		pe = (ne - be) / be * 100
		ex = tTone(pe, false)
	}
	execs := "executions per hour are the same"
	switch ex {
	case ToneMore:
		execs = "executions per hour are up " + tPct(pe)
	case ToneFewer:
		execs = "executions per hour are down " + tPct(pe)
	}
	switch {
	case cpu == ToneBetter && ex == ToneSame:
		return execs + ", so the work got cheaper, not rarer."
	case cpu == ToneBetter && ex == ToneMore:
		return execs + ", so it does more work for less CPU each time."
	case cpu == ToneBetter:
		return execs + " too, so part of the gain may be less work rather than cheaper work."
	case cpu == ToneWorse && ex == ToneSame:
		return execs + ", so each run costs more than before."
	case cpu == ToneWorse && ex == ToneMore:
		return execs + ", so it is busier and each run costs more."
	case cpu == ToneWorse:
		return execs + ", so each run costs more even with less work."
	case ex == ToneSame:
		return execs + ", so there is no clear difference yet."
	}
	return execs + ", so the load changed, not the cost of each run."
}

// tBusinessHoursPassed counts the complete business hours after the change's hour, before the next
// change (bound, zero for none) and without a change in them, whether anything ran in them or not.
func tBusinessHoursPassed(changeAt, bound time.Time, bh BusinessHours, changed map[time.Time]bool, now time.Time) int {
	n := 0
	for at := changeAt.Truncate(time.Hour).Add(time.Hour); !at.Add(time.Hour).After(now); at = at.Add(time.Hour) {
		if !bound.IsZero() && !at.Before(bound) {
			break
		}
		if bh.Class(at) == HourBusiness && !changed[at] {
			n++
		}
	}
	return n
}

// tLookAgain is when the comparison will have enough to say: the end of the second complete business
// hour after the change, or, when that is already past, the next working day once its first two
// business hours are over (10:00 with the default hours). Always after now.
func tLookAgain(changeAt time.Time, bh BusinessHours, changed map[time.Time]bool, now time.Time) time.Time {
	n := 0
	at := changeAt.Truncate(time.Hour).Add(time.Hour)
	for i := 0; i < 24*31; i++ {
		if bh.Class(at) == HourBusiness && !changed[at] {
			n++
			if n == TuningSameDayMinHours {
				if end := at.Add(time.Hour); end.After(now) {
					return end
				}
				break
			}
		}
		at = at.Add(time.Hour)
	}
	d := tDate(now).AddDate(0, 0, 1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return d.Add(time.Duration(min(bh.From+TuningSameDayMinHours, 24)) * time.Hour)
}

// TuningHandover is Copy for IT: where and when, the sentences, the four tiles and the per-query
// table, then the plan regressions and failures, as one plain-text block.
func TuningHandover(r TuningReport) string {
	var b strings.Builder
	head := "Tuning report for " + r.Database
	if r.ServerName != "" {
		head += " on " + r.ServerName
	}
	b.WriteString(head + "\n")
	fmt.Fprintf(&b, "Read at %s server time; the last %s; business hours %s, Monday to Friday.\n",
		tWhenStamp(r.CapturedAt), plural(int64(r.Days), "day", "days"), r.BusinessHours)
	b.WriteString("Centred on " + r.Centre.Label + ".\n")
	if r.Now.Label != "" {
		b.WriteString("Now: " + r.Now.Label + ".\nBefore: " + r.Before.Label + ".\n")
	}
	if len(r.Explain) > 0 {
		b.WriteString("\nWhat we see\n")
		for _, l := range r.Explain {
			b.WriteString("- " + l.Text + "\n")
		}
	}
	b.WriteString("\nIs it better than before?\n")
	for _, t := range r.Tiles {
		b.WriteString("- " + t.Label + ": " + tTileText(t) + "\n")
	}
	if len(r.Queries) > 0 {
		b.WriteString("\nWhat is expensive (CPU per run before -> now; runs before -> now; verdict)\n")
		for _, q := range r.Queries {
			fmt.Fprintf(&b, "- %s (%s): %s -> %s; %s -> %s runs; %s\n", q.Name, q.QueryHash, tMsPtr(q.CPUBeforeMs), tMsPtr(q.CPUNowMs),
				n0int(q.RunsBefore), n0int(q.RunsNow), q.Verdict)
		}
	}
	if len(r.Regressions) > 0 {
		b.WriteString("\nPlan regressions (the newest plan costs at least 1.5 x the CPU per run of the one before)\n")
		for _, g := range r.Regressions {
			fmt.Fprintf(&b, "- %s: plan %d %s per run (%s runs) -> plan %d %s per run (%s runs), %s x; %s\n", g.Name, g.OldPlanID, tMs(g.OldCPUMs),
				n0int(g.OldRuns), g.NewPlanID, tMs(g.NewCPUMs), n0int(g.NewRuns), qRatioText(g.Ratio), g.Relation)
		}
	}
	if len(r.Failures) > 0 {
		b.WriteString("\nFailed executions\n")
		for _, fl := range r.Failures {
			fmt.Fprintf(&b, "- %s: %s aborted, %s errors, last at %s\n", fl.Name, n0int(fl.Aborted), n0int(fl.Exceptions), tWhenStamp(fl.LastFailed))
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\nNot everything could be read\n")
		for _, n := range r.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func tTileText(t TuningTile) string {
	if t.Before == nil && t.Now == nil && t.BeforeText == "" && t.NowText == "" {
		if t.Words != "" {
			return t.Words
		}
		return "nothing to compare"
	}
	val := func(v *float64, text string) string {
		if text != "" {
			return text
		}
		if v == nil {
			return "-"
		}
		switch t.Unit {
		case "ms":
			return tMs(*v)
		case "threads":
			return strings.TrimSuffix(n1(*v), ".0")
		}
		return n0(*v)
	}
	s := val(t.Before, t.BeforeText) + " -> " + val(t.Now, t.NowText)
	if t.Pct != nil {
		s += fmt.Sprintf(" (%+.0f%%, %s)", *t.Pct, t.Tone)
	}
	return s
}

func tQueryNumbers(q TuningQuery) string {
	return fmt.Sprintf("CPU per run: before %s (%s runs), now %s (%s runs); verdict: %s.", tMsPtr(q.CPUBeforeMs), n0int(q.RunsBefore),
		tMsPtr(q.CPUNowMs), n0int(q.RunsNow), q.Verdict)
}

// tQueryCopy is the Copy for the vendor of one query: the query, the sentence and its text.
func tQueryCopy(database, name, hash, text, sentence string) string {
	var b strings.Builder
	b.WriteString(tCap(name) + " in " + database + "\n")
	if hash != "" {
		b.WriteString("Query hash: " + hash + "\n")
	}
	b.WriteString("\n" + sentence + "\n")
	if strings.TrimSpace(text) != "" {
		b.WriteString("\nQuery text (first 300 characters)\n" + strings.TrimSpace(text) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// tQueryName names a query in a sentence: its procedure, else "the ad hoc query "SELECT ..."".
func tQueryName(object, text string) string {
	if object != "" {
		return object
	}
	t := []rune(strings.TrimSpace(text))
	if len(t) == 0 {
		return "an ad hoc query"
	}
	if len(t) > 60 {
		t = append(t[:60], '…')
	}
	return `the ad hoc query "` + string(t) + `"`
}

// tChangeName names a change in a sentence: "the statistics update on dbo.StockUnit".
func tChangeName(c Change) string {
	obj := strings.TrimSpace(c.Object)
	if obj == "" {
		return "the change"
	}
	switch strings.ToUpper(c.ChangeType) {
	case "STATISTICS":
		return "the statistics update on " + obj
	case "INDEX":
		return "the index change on " + obj
	case "PROCEDURE":
		return "the change to procedure " + obj
	case "VIEW":
		return "the change to view " + obj
	case changeTypeFunction:
		return "the change to function " + obj
	case changeTypeTrigger:
		return "the change to trigger " + obj
	case ChangeTypeDeploy:
		return "the deploy of " + obj
	}
	return "the change to " + obj
}

// tMs writes a CPU time in milliseconds: "0.04 ms", "12.4 ms", "450 ms", "12.5 s".
func tMs(ms float64) string {
	switch {
	case ms < 0.1:
		return fmt.Sprintf("%.2f ms", ms)
	case ms < 100:
		return n1(ms) + " ms"
	case ms < 10_000:
		return n0(ms) + " ms"
	}
	return n1(ms/1000) + " s"
}

func tMsPtr(v *float64) string {
	if v == nil {
		return "-"
	}
	return tMs(*v)
}

func tPct(p float64) string { return n0(math.Abs(p)) + "%" }

func tDuration(d time.Duration) string {
	m := int64(math.Round(d.Minutes()))
	switch {
	case m < 1:
		return "less than a minute"
	case m < 60:
		return plural(m, "minute", "minutes")
	case m < 48*60:
		return plural(int64(math.Round(float64(m)/60)), "hour", "hours")
	}
	return plural(int64(math.Round(float64(m)/1440)), "day", "days")
}

// tCap starts a sentence with a query's name: "The ad hoc query ...", but "dbo.USP_PICKING" as it is.
func tCap(s string) string {
	if strings.HasPrefix(s, "the ") || strings.HasPrefix(s, "an ") {
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

const tStampLayout = "2006-01-02T15:04:05"

func tStamp(t time.Time) string { return t.Format(tStampLayout) }

func tWhen(t time.Time) string { return t.Format("Mon 2 Jan 15:04") }

// tWhenStamp writes a tStamp as a sentence does; "" stays "-".
func tWhenStamp(s string) string {
	t, err := time.Parse(tStampLayout, s)
	if err != nil {
		return "-"
	}
	return tWhen(t)
}

// tRange: "Mon 3 Nov 08:00 to 11:00", or across days "Mon 3 Nov 15:00 to Tue 4 Nov 10:00".
func tRange(from, to time.Time) string {
	if tDate(from).Equal(tDate(to)) || (to.Hour() == 0 && to.Minute() == 0 && tDate(to.Add(-time.Minute)).Equal(tDate(from))) {
		return tWhen(from) + " to " + to.Format("15:04")
	}
	return tWhen(from) + " to " + tWhen(to)
}

func tDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// naiveUTC keeps a time's wall clock in UTC, as every time of the report is kept.
func naiveUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
