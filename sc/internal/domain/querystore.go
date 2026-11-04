package domain

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DatabaseStatus is one user database on a server, with its Query Store state when it could be read.
type DatabaseStatus struct {
	Name               string            `json:"name"`
	State              string            `json:"state"` // ONLINE, RESTORING, RECOVERING, OFFLINE, ...
	ReadOnly           bool              `json:"readOnly"`
	IsSecondaryReplica bool              `json:"isSecondaryReplica"`
	AutoClose          bool              `json:"autoClose"`  // shows as RECOVERING for a moment while it opens
	QueryStore         *QueryStoreStatus `json:"queryStore"` // nil when it could not be read; then Error says why
	Error              string            `json:"error,omitempty"`
}

// CanFix says whether a fix may be applied to this database from here.
func (d DatabaseStatus) CanFix() bool {
	return d.State == "ONLINE" && !d.ReadOnly && !d.IsSecondaryReplica
}

// QueryStoreOptions is one read of sys.database_query_store_options for one database.
type QueryStoreOptions struct {
	ActualState             string // OFF, READ_ONLY, READ_WRITE, ERROR
	DesiredState            string
	ReadonlyReason          int
	CurrentStorageMb        int64
	MaxStorageMb            int64
	CaptureMode             string // ALL, AUTO, NONE, CUSTOM
	WaitStatsCaptureMode    string // ON, OFF; "" on SQL Server 2016, where the column does not exist
	IntervalLengthMinutes   int64
	StaleQueryThresholdDays int64
	FlushIntervalSeconds    int64
	MaxPlansPerQuery        int64
	SizeBasedCleanupMode    string // OFF, AUTO
}

// QueryStoreSettings are the settings the fix manages, as values.
type QueryStoreSettings struct {
	OperationMode           string
	CaptureMode             string
	MaxStorageMb            int64
	SizeBasedCleanupMode    string
	IntervalLengthMinutes   int64
	StaleQueryThresholdDays int64
	FlushIntervalSeconds    int64
	MaxPlansPerQuery        int64
	WaitStatsCaptureMode    string // only on engine major 14 (SQL Server 2017) and later
}

// QueryStoreTarget is the agreed target (SCOPE.md section 3a).
var QueryStoreTarget = QueryStoreSettings{
	OperationMode:           "READ_WRITE",
	CaptureMode:             "AUTO",
	MaxStorageMb:            2048,
	SizeBasedCleanupMode:    "AUTO",
	IntervalLengthMinutes:   15,
	StaleQueryThresholdDays: 30,
	FlushIntervalSeconds:    900,
	MaxPlansPerQuery:        200,
	WaitStatsCaptureMode:    "ON",
}

// Drift is one setting whose current value differs from the target.
type Drift struct {
	Setting string `json:"setting"` // the T-SQL option name, e.g. INTERVAL_LENGTH_MINUTES
	Current string `json:"current"`
	Target  string `json:"target"`
}

// Verdicts for a database's Query Store.
const (
	VerdictOK          = "ok"
	VerdictDrift       = "drift"
	VerdictOff         = "off"
	VerdictUnavailable = "unavailable"
)

// QueryStoreStatus is what the card shows for one database.
type QueryStoreStatus struct {
	CapturedAt              time.Time `json:"capturedAt"`
	ActualState             string    `json:"actualState"`
	DesiredState            string    `json:"desiredState"`
	ReadonlyReason          int       `json:"readonlyReason"`
	ReadonlyReasonText      string    `json:"readonlyReasonText"`
	CurrentStorageMb        int64     `json:"currentStorageMb"`
	MaxStorageMb            int64     `json:"maxStorageMb"`
	StorageUsedPercent      float64   `json:"storageUsedPercent"`
	CaptureMode             string    `json:"captureMode"`
	WaitStatsCaptureMode    string    `json:"waitStatsCaptureMode"`
	IntervalLengthMinutes   int64     `json:"intervalLengthMinutes"`
	StaleQueryThresholdDays int64     `json:"staleQueryThresholdDays"`
	FlushIntervalSeconds    int64     `json:"flushIntervalSeconds"`
	MaxPlansPerQuery        int64     `json:"maxPlansPerQuery"`
	SizeBasedCleanupMode    string    `json:"sizeBasedCleanupMode"`
	Verdict                 string    `json:"verdict"`
	Drift                   []Drift   `json:"drift"`
	CanFix                  bool      `json:"canFix"`
	Summary                 string    `json:"summary"`
}

// FixPreview is exactly what a fix will run, before it runs.
type FixPreview struct {
	Database      string   `json:"database"`
	EngineVersion string   `json:"engineVersion"`
	Changes       []Drift  `json:"changes"`   // empty means nothing to do
	Statement     string   `json:"statement"` // "" when Changes is empty
	Rollback      string   `json:"rollback"`  // "" when Changes is empty
	Warnings      []string `json:"warnings"`
	// Inputs are what the user may set for this fix (docs/phase3g-contract.md); the preview was
	// built with Options, which are echoed so that apply sends the same. Left out for a fix
	// without inputs.
	Inputs  []FixInput        `json:"inputs,omitempty"`
	Options map[string]string `json:"options,omitempty"`
	// Express true: nothing will be applied (SQL Server Express has no Agent); Statement is a
	// script to run by hand.
	Express bool `json:"express,omitempty"`
}

// FixInput is one value the user may set in a fix's preview (docs/phase3g-contract.md).
type FixInput struct {
	Name  string `json:"name"` // "folder", "runNow"
	Label string `json:"label"`
	Value string `json:"value"` // the value the preview was built with
	Help  string `json:"help"`
	Kind  string `json:"kind"` // "text", "bool", "select"
	// Warning is what is wrong with this value, shown under the field as well as in the
	// preview's warnings; "" when nothing is.
	Warning string `json:"warning,omitempty"`
	// Options are the values a "select" input offers (docs/phase4b-contract.md).
	Options []string `json:"options,omitempty"`
}

// QueryStoreFixResult is the phase 1 Query Store fix that ran, with the settings read before and after.
type QueryStoreFixResult struct {
	Preview   FixPreview       `json:"preview"`
	Before    QueryStoreStatus `json:"before"`
	After     QueryStoreStatus `json:"after"`
	ChangeID  int64            `json:"changeId"`
	AppliedAt time.Time        `json:"appliedAt"`
}

// qsSetting describes one managed option: where its current value comes from, its target,
// and how it is written in ALTER DATABASE ... SET QUERY_STORE (...).
type qsSetting struct {
	name     string
	minMajor int // leave the setting out below this engine major version
	current  func(QueryStoreOptions) string
	target   func(QueryStoreSettings) string
	// atLeast: a current value above the target also counts as matching. Used for the
	// storage limit, so the fix never shrinks a bigger store below what it already holds.
	atLeast bool
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

var qsSettings = []qsSetting{
	{name: "OPERATION_MODE",
		current: func(o QueryStoreOptions) string { return o.ActualState },
		target:  func(t QueryStoreSettings) string { return t.OperationMode }},
	{name: "QUERY_CAPTURE_MODE",
		current: func(o QueryStoreOptions) string { return o.CaptureMode },
		target:  func(t QueryStoreSettings) string { return t.CaptureMode }},
	{name: "MAX_STORAGE_SIZE_MB", atLeast: true,
		current: func(o QueryStoreOptions) string { return itoa(o.MaxStorageMb) },
		target:  func(t QueryStoreSettings) string { return itoa(t.MaxStorageMb) }},
	{name: "SIZE_BASED_CLEANUP_MODE",
		current: func(o QueryStoreOptions) string { return o.SizeBasedCleanupMode },
		target:  func(t QueryStoreSettings) string { return t.SizeBasedCleanupMode }},
	{name: "INTERVAL_LENGTH_MINUTES",
		current: func(o QueryStoreOptions) string { return itoa(o.IntervalLengthMinutes) },
		target:  func(t QueryStoreSettings) string { return itoa(t.IntervalLengthMinutes) }},
	{name: "STALE_QUERY_THRESHOLD_DAYS",
		current: func(o QueryStoreOptions) string { return itoa(o.StaleQueryThresholdDays) },
		target:  func(t QueryStoreSettings) string { return itoa(t.StaleQueryThresholdDays) }},
	{name: "DATA_FLUSH_INTERVAL_SECONDS",
		current: func(o QueryStoreOptions) string { return itoa(o.FlushIntervalSeconds) },
		target:  func(t QueryStoreSettings) string { return itoa(t.FlushIntervalSeconds) }},
	{name: "MAX_PLANS_PER_QUERY",
		current: func(o QueryStoreOptions) string { return itoa(o.MaxPlansPerQuery) },
		target:  func(t QueryStoreSettings) string { return itoa(t.MaxPlansPerQuery) }},
	{name: "WAIT_STATS_CAPTURE_MODE", minMajor: 14,
		current: func(o QueryStoreOptions) string { return o.WaitStatsCaptureMode },
		target:  func(t QueryStoreSettings) string { return t.WaitStatsCaptureMode }},
}

func (s qsSetting) matches(current, target string) bool {
	if s.atLeast {
		c, err1 := strconv.ParseInt(current, 10, 64)
		t, err2 := strconv.ParseInt(target, 10, 64)
		if err1 == nil && err2 == nil {
			return c >= t
		}
	}
	return strings.EqualFold(current, target)
}

// clause renders one option as T-SQL. The stale-query threshold sits inside CLEANUP_POLICY.
func clause(setting, value string) string {
	if setting == "STALE_QUERY_THRESHOLD_DAYS" {
		return "CLEANUP_POLICY = (STALE_QUERY_THRESHOLD_DAYS = " + value + ")"
	}
	return setting + " = " + value
}

// Comparison is the outcome of comparing a database's options with the target.
type Comparison struct {
	Verdict string
	Drift   []Drift
	Summary string
}

// Compare names the drift between the options read and the target. When Query Store is off,
// every target setting is listed, because turning it on sets them all.
func Compare(o QueryStoreOptions, engineMajor int) Comparison {
	off := strings.EqualFold(o.ActualState, "OFF")
	drift := []Drift{}
	for _, s := range qsSettings {
		if engineMajor < s.minMajor {
			continue
		}
		cur, tgt := s.current(o), s.target(QueryStoreTarget)
		if off {
			if s.atLeast && s.matches(cur, tgt) && cur != tgt {
				continue // a bigger limit is kept as it is (see qsSetting.atLeast)
			}
			drift = append(drift, Drift{Setting: s.name, Current: cur, Target: tgt})
			continue
		}
		if !s.matches(cur, tgt) {
			drift = append(drift, Drift{Setting: s.name, Current: cur, Target: tgt})
		}
	}

	c := Comparison{Drift: drift, Summary: summary(o)}
	switch {
	case off:
		c.Verdict = VerdictOff
	case len(drift) > 0:
		c.Verdict = VerdictDrift
	default:
		c.Verdict = VerdictOK
	}
	return c
}

func summary(o QueryStoreOptions) string {
	switch strings.ToUpper(o.ActualState) {
	case "OFF":
		return "Off"
	case "READ_ONLY":
		if t := ReadonlyReasonText(o.ReadonlyReason); t != "" {
			return "Read-only: " + t
		}
		return "Read-only"
	case "ERROR":
		return "Error: Query Store is in an error state"
	case "READ_WRITE":
		return fmt.Sprintf("On, %s%% full, %d-min intervals, %d days",
			strconv.FormatFloat(math.Round(storagePercent(o)), 'f', 0, 64),
			o.IntervalLengthMinutes, o.StaleQueryThresholdDays)
	}
	return o.ActualState
}

func storagePercent(o QueryStoreOptions) float64 {
	if o.MaxStorageMb <= 0 {
		return 0
	}
	p := float64(o.CurrentStorageMb) / float64(o.MaxStorageMb) * 100
	return math.Round(p*10) / 10
}

// readonlyReasons are the readonly_reason bits of sys.database_query_store_options in plain words,
// lowest bit first.
// Source: DarlingData sp_PerfCheck.sql 4805-4820 (MIT), worded for a non-DBA.
var readonlyReasons = []struct {
	bit  int
	text string
}{
	{1, "the database is in read-only mode"},
	{2, "the database is in single-user mode"},
	{4, "the database is in emergency mode"},
	{8, "secondary replica"},
	{65536, "storage full"},
	{131072, "too many different queries for its memory limit"},
	{262144, "too much data waiting to be written to disk"},
	{524288, "the database reached its disk size limit"},
}

// ReadonlyReasonText decodes readonly_reason from sys.database_query_store_options, bit by bit;
// several reasons join with ", ", and bits it does not know are named by their value.
func ReadonlyReasonText(reason int) string {
	if reason == 0 {
		return ""
	}
	parts, rest := []string{}, reason
	for _, r := range readonlyReasons {
		if reason&r.bit != 0 {
			parts = append(parts, r.text)
			rest &^= r.bit
		}
	}
	if rest != 0 {
		parts = append(parts, fmt.Sprintf("other reason (%d)", rest))
	}
	return strings.Join(parts, ", ")
}

// NewQueryStoreStatus builds what the card shows from one read of the options. When the
// database cannot be fixed from here and is not already right, the verdict is "unavailable".
// On SQL Server 2016 and 2017 the build goes next to the state, because early cumulative
// updates had Query Store bugs.
func NewQueryStoreStatus(o QueryStoreOptions, engineVersion string, capturedAt time.Time, canFix bool) QueryStoreStatus {
	major := EngineMajor(engineVersion)
	c := Compare(o, major)
	st := QueryStoreStatus{
		CapturedAt:              capturedAt.UTC(),
		ActualState:             o.ActualState,
		DesiredState:            o.DesiredState,
		ReadonlyReason:          o.ReadonlyReason,
		ReadonlyReasonText:      ReadonlyReasonText(o.ReadonlyReason),
		CurrentStorageMb:        o.CurrentStorageMb,
		MaxStorageMb:            o.MaxStorageMb,
		StorageUsedPercent:      storagePercent(o),
		CaptureMode:             o.CaptureMode,
		WaitStatsCaptureMode:    o.WaitStatsCaptureMode,
		IntervalLengthMinutes:   o.IntervalLengthMinutes,
		StaleQueryThresholdDays: o.StaleQueryThresholdDays,
		FlushIntervalSeconds:    o.FlushIntervalSeconds,
		MaxPlansPerQuery:        o.MaxPlansPerQuery,
		SizeBasedCleanupMode:    o.SizeBasedCleanupMode,
		Verdict:                 c.Verdict,
		Drift:                   c.Drift,
		CanFix:                  canFix,
		Summary:                 c.Summary,
	}
	if !canFix && st.Verdict != VerdictOK {
		st.Verdict = VerdictUnavailable
	}
	if major == 13 || major == 14 {
		st.Summary += fmt.Sprintf(" (%s build %s)", ProductName(major), engineVersion)
	}
	return st
}

// QuoteName bracket-quotes a name as T-SQL QUOTENAME does: [name] with ] doubled.
func QuoteName(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// EngineMajor returns the major version from SERVERPROPERTY('ProductVersion'), e.g. 16 for 16.0.1000.6.
func EngineMajor(productVersion string) int {
	head, _, _ := strings.Cut(strings.TrimSpace(productVersion), ".")
	n, _ := strconv.Atoi(head)
	return n
}

// ProductName names the release for an engine major version.
func ProductName(major int) string {
	switch major {
	case 11:
		return "SQL Server 2012"
	case 12:
		return "SQL Server 2014"
	case 13:
		return "SQL Server 2016"
	case 14:
		return "SQL Server 2017"
	case 15:
		return "SQL Server 2019"
	case 16:
		return "SQL Server 2022"
	case 17:
		return "SQL Server 2025"
	}
	return fmt.Sprintf("SQL Server (engine %d)", major)
}

// sqlWord guards values read from the server before they go into a rollback statement.
var sqlWord = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// BuildQueryStoreFix builds the statement that brings a database's Query Store to the target, only for
// the settings that differ, and the statement that puts the previous values back.
func BuildQueryStoreFix(o QueryStoreOptions, engineMajor int, dbName string) FixPreview {
	p := FixPreview{Database: dbName, Changes: []Drift{}, Warnings: []string{}}
	c := Compare(o, engineMajor)
	if c.Verdict == VerdictOK || len(c.Drift) == 0 {
		return p
	}
	p.Changes = c.Drift
	db := QuoteName(dbName)
	off := c.Verdict == VerdictOff

	set := make([]string, 0, len(c.Drift))
	var back []string
	changed := map[string]Drift{}
	for _, d := range c.Drift {
		set = append(set, clause(d.Setting, d.Target))
		if d.Current == d.Target {
			continue
		}
		changed[d.Setting] = d
		if d.Setting == "OPERATION_MODE" && !strings.EqualFold(d.Current, "READ_ONLY") {
			continue // OFF is restored by SET QUERY_STORE = OFF below; ERROR cannot be set back
		}
		if !sqlWord.MatchString(d.Current) {
			p.Warnings = append(p.Warnings, fmt.Sprintf("The previous value of %s (%q) is not in the rollback; set it by hand if you roll back.", d.Setting, d.Current))
			continue
		}
		back = append(back, clause(d.Setting, d.Current))
	}

	if off {
		p.Statement = fmt.Sprintf("ALTER DATABASE %s SET QUERY_STORE = ON (%s);", db, strings.Join(set, ", "))
		rb := []string{}
		if len(back) > 0 {
			rb = append(rb, fmt.Sprintf("ALTER DATABASE %s SET QUERY_STORE (%s);", db, strings.Join(back, ", ")))
		}
		rb = append(rb, fmt.Sprintf("ALTER DATABASE %s SET QUERY_STORE = OFF;", db))
		p.Rollback = strings.Join(rb, "\n")
	} else {
		p.Statement = fmt.Sprintf("ALTER DATABASE %s SET QUERY_STORE (%s);", db, strings.Join(set, ", "))
		if len(back) > 0 {
			p.Rollback = fmt.Sprintf("ALTER DATABASE %s SET QUERY_STORE (%s);", db, strings.Join(back, ", "))
		}
	}

	t := QueryStoreTarget
	if off {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Query Store is off on %s. This turns it on with the recommended settings; it starts capturing queries straight away.", dbName))
	}
	if d, ok := changed["INTERVAL_LENGTH_MINUTES"]; ok {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Changing INTERVAL_LENGTH_MINUTES from %s to %s clears the runtime statistics collected so far.", d.Current, d.Target))
	}
	if _, ok := changed["MAX_STORAGE_SIZE_MB"]; ok {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The store can grow to %d MB (2 GB) inside the database. It lives in the PRIMARY filegroup, so it adds to the database and its backups.", t.MaxStorageMb))
	}
	if strings.EqualFold(o.ActualState, "READ_ONLY") && o.ReadonlyReason&65536 != 0 && o.CurrentStorageMb >= t.MaxStorageMb {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The store already holds %d MB, at or above the %d MB target. Size-based cleanup will remove old queries to make room; if it stays read-only, clear it with ALTER DATABASE %s SET QUERY_STORE CLEAR.", o.CurrentStorageMb, t.MaxStorageMb, db))
	}
	if strings.EqualFold(o.ActualState, "ERROR") {
		p.Warnings = append(p.Warnings, "Query Store is in the ERROR state. If setting READ_WRITE does not clear it, run sys.sp_query_store_consistency_check in the database, or turn Query Store off and on again.")
	}
	if engineMajor < 14 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s has no wait-stats capture in Query Store, so WAIT_STATS_CAPTURE_MODE is left out.", ProductName(engineMajor)))
	}
	if engineMajor == 13 || engineMajor == 14 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("This is %s. Early cumulative updates had Query Store bugs; check that the build is current before relying on it.", ProductName(engineMajor)))
	}
	return p
}

// ChangeDescription is the one line the change log keeps for a Query Store fix,
// e.g. "Query Store on WarehouseLive: INTERVAL_LENGTH_MINUTES 60 -> 15, MAX_STORAGE_SIZE_MB 1000 -> 2048".
func ChangeDescription(dbName string, changes []Drift) string {
	parts := []string{}
	for _, d := range changes {
		if d.Current != d.Target {
			parts = append(parts, fmt.Sprintf("%s %s -> %s", d.Setting, d.Current, d.Target))
		}
	}
	if len(parts) == 0 {
		return "Query Store on " + dbName + ": no setting changed"
	}
	return "Query Store on " + dbName + ": " + strings.Join(parts, ", ")
}
