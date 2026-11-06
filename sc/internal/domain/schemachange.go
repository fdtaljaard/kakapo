package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The change log fills itself from the server (docs/phase5f-contract.md): the changes made outside
// kakapo (a release deployed, an index added in SSMS, a procedure altered) are read from SQL Server's
// default trace with the Upkeep read, grouped one row per person-hour, and kept in the change log
// with Result ChangeSeenOnServer, so they outlive the trace's rollover. CHG01 says when the trace is
// off or holds less than the tuning report's week; enable-default-trace switches it back on.
//
// Sources: Microsoft Learn, "Default trace" and "sys.fn_trace_gettable"; "Object:Created",
// "Object:Deleted" and "Object:Altered Event Class" (event classes 46, 47 and 164, EventSubClass 0
// Begin, 1 Commit, 2 Rollback, as sys.trace_subclass_values lists them); "ObjectType Trace Event
// Column" for the object type codes below, each seen in the live trace of localhost\SQL2022 except
// the CLR ones; "Server Configuration: default trace enabled": the option is on (1) by default,
// takes effect without a restart, and "This feature will be removed in a future version of SQL
// Server ... Use Extended Events instead." (phase 5g's second trigger).

// The default trace's DDL event classes and the commit subclass.
const (
	TraceObjectCreated  = 46
	TraceObjectDeleted  = 47
	TraceObjectAltered  = 164
	traceSubclassCommit = 1
	// traceFirstUserDatabase: database ids 1 to 4 are master, tempdb, model and msdb.
	traceFirstUserDatabase = 5
)

// KakapoProgram is the application name kakapo connects with (collect/mssql.go): its own changes
// are already in the log as fixes.
const KakapoProgram = "SQLMonitor"

// ObjectType codes (Microsoft, "ObjectType Trace Event Column").
const (
	objTypeProcedure      = 8272
	objTypeCLRProcedure   = 17232
	objTypeView           = 8278
	objTypeScalarFn       = 20038
	objTypeInlineTableFn  = 17993
	objTypeTableFn        = 18004
	objTypeInlineScalarFn = 21321
	objTypeCLRScalarFn    = 21318
	objTypeCLRTableFn     = 21574
	objTypeCLRAggregate   = 17985
	objTypeTable          = 8277
	objTypeTrigger        = 21076
	objTypeCLRTrigger     = 16724
	objTypeDatabaseTrig   = 21572
	ObjTypeIndex          = 22601
	ObjTypeDatabase       = 16964
	ObjTypeStatistics     = 21587 // left out: auto-created and manual statistics alike
)

// Change types of the changes seen on the server, beside the ones of ChangeTypes.
const (
	ChangeTypeDeploy   = "DEPLOY"
	changeTypeFunction = "FUNCTION"
	changeTypeTable    = "TABLE"
	changeTypeTrigger  = "TRIGGER"
)

// schemaObjectType is what a mapped ObjectType becomes in the change log.
type schemaObjectType struct {
	changeType string
	word       string // "procedure"
	words      string // "procedures"
}

// schemaObjectTypes are the object types kept; everything else (statistics included) is left out.
var schemaObjectTypes = map[int]schemaObjectType{
	objTypeProcedure:      {"PROCEDURE", "procedure", "procedures"},
	objTypeCLRProcedure:   {"PROCEDURE", "procedure", "procedures"},
	objTypeView:           {"VIEW", "view", "views"},
	objTypeScalarFn:       {changeTypeFunction, "function", "functions"},
	objTypeInlineTableFn:  {changeTypeFunction, "function", "functions"},
	objTypeTableFn:        {changeTypeFunction, "function", "functions"},
	objTypeInlineScalarFn: {changeTypeFunction, "function", "functions"},
	objTypeCLRScalarFn:    {changeTypeFunction, "function", "functions"},
	objTypeCLRTableFn:     {changeTypeFunction, "function", "functions"},
	objTypeCLRAggregate:   {changeTypeFunction, "function", "functions"},
	objTypeTable:          {changeTypeTable, "table", "tables"},
	objTypeTrigger:        {changeTypeTrigger, "trigger", "triggers"},
	objTypeCLRTrigger:     {changeTypeTrigger, "trigger", "triggers"},
	objTypeDatabaseTrig:   {changeTypeTrigger, "trigger", "triggers"},
	ObjTypeIndex:          {"INDEX", "index", "indexes"},
	ObjTypeDatabase:       {"SETTING", "database", "databases"},
}

// SchemaObjectTypeCodes are the ObjectType codes kept, in order: the read's IN list.
func SchemaObjectTypeCodes() []int {
	out := make([]int, 0, len(schemaObjectTypes))
	for k := range schemaObjectTypes {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// SeenGroupMax is the most events of one person-hour that become one change row each; more become
// one DEPLOY row.
const SeenGroupMax = 5

// SchemaChangeFact is one DDL event of the default trace as the Upkeep read returns it. At is UTC
// (StartTime with the server's offset). Schema, and an index's Table, are resolved at read time for
// objects that still exist; "" for one that is gone.
type SchemaChangeFact struct {
	At            time.Time
	DatabaseID    int
	Database      string
	EventClass    int
	SubClass      int
	ObjectType    int
	Schema        string
	Name          string // ObjectName: for an index, the index's name; "" for the database
	Table         string // an index's table
	TransactionID int64
	Login         string
	Host          string
	Program       string
}

// kept says whether an event becomes part of the change log: a commit, in a user database, of a
// mapped object type, not by kakapo; an index created or dropped (its alters are the nightly rebuild
// and reorganise); the database only altered (ALTER DATABASE, a setting).
func (x SchemaChangeFact) kept() bool {
	if x.SubClass != traceSubclassCommit || x.DatabaseID < traceFirstUserDatabase || strings.EqualFold(strings.TrimSpace(x.Program), KakapoProgram) {
		return false
	}
	if _, ok := schemaObjectTypes[x.ObjectType]; !ok {
		return false
	}
	switch x.EventClass {
	case TraceObjectCreated, TraceObjectDeleted:
		return x.ObjectType != ObjTypeDatabase
	case TraceObjectAltered:
		return x.ObjectType != ObjTypeIndex
	}
	return false
}

// object is the change row's object: schema.name, an index as schema.table.index, the database by
// its name; a dropped object keeps the bare name.
func (x SchemaChangeFact) object() string {
	switch {
	case x.ObjectType == ObjTypeDatabase:
		return x.Database
	case x.ObjectType == ObjTypeIndex && x.Schema != "" && x.Table != "":
		return x.Schema + "." + x.Table + "." + x.Name
	case x.ObjectType != ObjTypeIndex && x.Schema != "":
		return x.Schema + "." + x.Name
	}
	return x.Name
}

func (x SchemaChangeFact) event() string {
	switch x.EventClass {
	case TraceObjectCreated:
		return "created"
	case TraceObjectDeleted:
		return "dropped"
	}
	return "altered"
}

// line is one event in words: "Procedure dbo.GetOrders altered".
func (x SchemaChangeFact) line() string {
	return upperFirst(schemaObjectTypes[x.ObjectType].word) + " " + orQ(x.object()) + " " + x.event()
}

// SeenWindowStart is the first hour a harvest recomputes: the hour after the hour of the trace's
// oldest event (UTC). Hours before it are never touched, which is how the change log keeps what the
// trace has rolled off.
func SeenWindowStart(oldestUTC time.Time) time.Time {
	return oldestUTC.UTC().Truncate(time.Hour).Add(time.Hour)
}

// keptSchemaChanges are the events that become change rows from since (UTC) on, in time order:
// kept(), and without the index rows of a table's own key (an index created in the transaction that
// created a table).
func keptSchemaChanges(facts []SchemaChangeFact, since time.Time) []SchemaChangeFact {
	tableTx := map[int64]bool{}
	for _, x := range facts {
		if x.kept() && x.EventClass == TraceObjectCreated && x.ObjectType == objTypeTable && x.TransactionID != 0 {
			tableTx[x.TransactionID] = true
		}
	}
	out := []SchemaChangeFact{}
	for _, x := range facts {
		if !x.kept() || x.At.UTC().Before(since) {
			continue
		}
		if x.ObjectType == ObjTypeIndex && x.EventClass == TraceObjectCreated && tableTx[x.TransactionID] {
			continue
		}
		out = append(out, x)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// seenGroup is one person-hour: the events of one database, login and program in one clock hour (UTC).
type seenGroup struct {
	hour                     time.Time
	database, login, program string
	events                   []SchemaChangeFact
}

// BuildSeenChanges turns the default trace's DDL events into change rows from since (UTC) on
// (docs/phase5f-contract.md, "Into the change log"): per database, login, program and clock hour,
// five events or fewer become one row each, more become one DEPLOY row naming the objects in its
// statement, one per line. Every row has Result ChangeSeenOnServer and its time in UTC; the rows come
// in time order.
func BuildSeenChanges(serverID int64, facts []SchemaChangeFact, since time.Time) []Change {
	groups := []*seenGroup{}
	at := map[string]*seenGroup{}
	for _, x := range keptSchemaChanges(facts, since) {
		h := x.At.UTC().Truncate(time.Hour)
		key := h.Format(time.RFC3339) + "\x00" + x.Database + "\x00" + x.Login + "\x00" + x.Program
		g, ok := at[key]
		if !ok {
			g = &seenGroup{hour: h, database: x.Database, login: x.Login, program: x.Program}
			at[key] = g
			groups = append(groups, g)
		}
		g.events = append(g.events, x)
	}
	out := []Change{}
	for _, g := range groups {
		if len(g.events) <= SeenGroupMax {
			for _, x := range g.events {
				out = append(out, Change{ServerID: serverID, Database: x.Database, ChangedAt: x.At.UTC(), Object: orQ(x.object()),
					ChangeType: schemaObjectTypes[x.ObjectType].changeType, Description: x.line() + seenBy(x.Login, []string{x.Host}, x.Program),
					Result: ChangeSeenOnServer})
			}
			continue
		}
		out = append(out, g.deploy(serverID))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ChangedAt.Before(out[j].ChangedAt) })
	return out
}

// deploy is a person-hour of more than SeenGroupMax events as one row: "Deploy by DOMAIN\build on
// BUILD01 (DacFx): 40 objects (12 procedures, 28 tables)", at its last event, the events one per line
// in the statement.
func (g *seenGroup) deploy(serverID int64) Change {
	hosts, seenHost := []string{}, map[string]bool{}
	objects := map[string]bool{}
	perType := map[string]int64{}
	lines := make([]string, 0, len(g.events))
	for _, x := range g.events {
		if h := strings.TrimSpace(x.Host); h != "" && !seenHost[strings.ToLower(h)] {
			seenHost[strings.ToLower(h)] = true
			hosts = append(hosts, h)
		}
		t := schemaObjectTypes[x.ObjectType]
		if k := t.word + "\x00" + x.object(); !objects[k] {
			objects[k] = true
			perType[t.word]++
		}
		lines = append(lines, x.line())
	}
	words := make([]string, 0, len(perType))
	for w := range perType {
		words = append(words, w)
	}
	sort.SliceStable(words, func(i, j int) bool {
		if perType[words[i]] != perType[words[j]] {
			return perType[words[i]] > perType[words[j]]
		}
		return words[i] < words[j]
	})
	counts := make([]string, 0, len(words))
	for _, w := range words {
		counts = append(counts, plural(perType[w], w, typeWords(w)))
	}
	n := plural(int64(len(objects)), "object", "objects")
	last := g.events[len(g.events)-1]
	return Change{ServerID: serverID, Database: g.database, ChangedAt: last.At.UTC(), Object: n, ChangeType: ChangeTypeDeploy,
		Description: "Deploy" + seenBy(g.login, hosts, g.program) + ": " + n + " (" + strings.Join(counts, ", ") + ")",
		Statement:   strings.Join(lines, "\n"), Result: ChangeSeenOnServer}
}

func typeWords(word string) string {
	for _, t := range schemaObjectTypes {
		if t.word == word {
			return t.words
		}
	}
	return word + "s"
}

// seenBy is " by DOMAIN\jane on APP-SRV01 (Microsoft SQL Server Management Studio)", leaving out
// what the trace did not record.
func seenBy(login string, hosts []string, program string) string {
	s := " by " + orQ(strings.TrimSpace(login))
	hs := []string{}
	for _, h := range hosts {
		if strings.TrimSpace(h) != "" {
			hs = append(hs, strings.TrimSpace(h))
		}
	}
	if len(hs) > 0 {
		s += " on " + strings.Join(hs, ", ")
	}
	if p := strings.TrimSpace(program); p != "" {
		s += " (" + p + ")"
	}
	return s
}

// seenKey is what makes two seen-on-server rows the same change.
func seenKey(c Change) string {
	return strings.Join([]string{c.Database, c.ChangedAt.UTC().Format(time.RFC3339Nano), c.Object, c.ChangeType, c.Description, c.Statement}, "\x00")
}

// DiffSeenChanges is the per-hour replace of a harvest: existing are the seen-on-server rows the
// change log holds from the window's start on, fresh the rows recomputed for the same hours. It
// returns the ids of the existing rows to delete and the fresh rows to insert; a row in both stays as
// it is (its id with it), so a page opened twice adds nothing twice.
func DiffSeenChanges(existing, fresh []Change) (stale []int64, add []Change) {
	left := map[string][]int64{}
	for _, c := range existing {
		k := seenKey(c)
		left[k] = append(left[k], c.ID)
	}
	add = []Change{}
	for _, c := range fresh {
		k := seenKey(c)
		if ids := left[k]; len(ids) > 0 {
			left[k] = ids[1:]
			continue
		}
		add = append(add, c)
	}
	stale = []int64{}
	for _, c := range existing {
		k := seenKey(c)
		if ids := left[k]; len(ids) > 0 && ids[0] == c.ID {
			left[k] = ids[1:]
			stale = append(stale, c.ID)
		}
	}
	return stale, add
}

// SchemaChangesSeenKey is the setting that keeps, per server, the newest trace event the previous
// harvest saw (UTC, RFC 3339): a trace whose oldest event is newer has turned over in between.
func SchemaChangesSeenKey(serverID int64) string {
	return fmt.Sprintf("schemachanges.seen.%d", serverID)
}

// --- the window line under Recent changes ---

// States of the window line.
const (
	ChangeWindowRunning = "running"
	ChangeWindowGap     = "gap"
	ChangeWindowOff     = "off"
	ChangeWindowUnread  = "unread"
)

// ChangeWindow is the line under Recent changes: how far back kakapo sees changes made outside it.
// Copy is the grant to copy when the trace could not be read.
type ChangeWindow struct {
	State string `json:"state"`
	Text  string `json:"text"`
	Days  int    `json:"days"`
	Copy  string `json:"copy,omitempty"`
}

// TraceDays is how many whole days the default trace holds: from its oldest event to now.
func (f UpkeepFacts) TraceDays() int {
	if f.TraceOldestUTC == nil {
		return 0
	}
	d := f.AsOf().Sub(f.TraceOldestUTC.UTC())
	if d < 0 {
		return 0
	}
	return int(d / (24 * time.Hour))
}

func daysWords(n int) string {
	switch {
	case n < 1:
		return "less than a day"
	case n == 1:
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}

// serverClock writes a UTC time on the server's clock: "2 Oct 2026 05:47".
func (f UpkeepFacts) serverClock(t time.Time, withTime bool) string {
	s := naiveUTC(t.UTC()).Add(time.Duration(f.UTCOffsetMinutes) * time.Minute)
	if withTime {
		return s.Format("2 Jan 2006 15:04")
	}
	return s.Format("2 Jan 2006")
}

// traceGrant is the grant that lets the login read the default trace.
func traceGrant(login string) string {
	if strings.TrimSpace(login) == "" {
		return "GRANT ALTER TRACE TO [the login kakapo connects with];"
	}
	return "GRANT ALTER TRACE TO " + QuoteName(login) + ";"
}

// chg01OffText is CHG01's finding and the window line when the trace is off.
const chg01OffText = "SQL Server's default trace is off, so kakapo cannot see changes made outside it, nor shrinks, cache clears and growths (MNT05, MNT06)."

// BuildChangeWindow is the window line from the facts the harvest read and the newest event the
// previous harvest saw (nil before the first). nil when the instance could not be read at all.
func BuildChangeWindow(f UpkeepFacts, previousNewest *time.Time) *ChangeWindow {
	switch {
	case f.Error != "":
		return nil
	case f.TraceError != "":
		return &ChangeWindow{State: ChangeWindowUnread, Text: traceNotRead + f.TraceError, Copy: traceGrant(f.LoginName)}
	case !f.TraceRunning:
		return &ChangeWindow{State: ChangeWindowOff, Text: "SQL Server's default trace is off, so changes made outside kakapo are not seen."}
	case f.TraceOldestUTC == nil:
		return &ChangeWindow{State: ChangeWindowRunning, Text: "Changes made outside kakapo are read from SQL Server's default trace, which holds nothing yet here."}
	}
	n := f.TraceDays()
	w := &ChangeWindow{State: ChangeWindowRunning, Days: n,
		Text: fmt.Sprintf("Changes made outside kakapo are read from SQL Server's default trace, which holds %s here (since %s). kakapo keeps what it reads, so changes older than that stay here as long as this page was opened within %s of them.",
			daysWords(n), f.serverClock(*f.TraceOldestUTC, false), daysWords(n))}
	if previousNewest != nil && f.TraceOldestUTC.After(*previousNewest) {
		w.State = ChangeWindowGap
		w.Text = fmt.Sprintf("Changes made between %s and %s were not seen: the trace turned over before kakapo was opened again. ",
			f.serverClock(*previousNewest, true), f.serverClock(*f.TraceOldestUTC, true)) + w.Text
	}
	return w
}

// --- CHG01 ---

// CHG01's bar: the tuning report's "before" window is five working days, a calendar week.
const chg01MinDays = 7

const (
	chg01OffWhy   = "kakapo reads the changes made outside it (a release deployed, an index added in SSMS, a procedure altered) from SQL Server's default trace, and the tuning report measures them; the same trace is where MNT05 and MNT06 find shrinks, cache clears, growths and severe errors. The default trace is on when SQL Server is installed."
	chg01OffDo    = "Switch the default trace back on (the fix): sp_configure 'default trace enabled', 1 with RECONFIGURE. It takes effect at once, without a restart, and keeps at most 100 MB on disk beside the error log. Ask first whoever switched it off."
	chg01ShortWhy = "The tuning report compares a change against the five working days before it. A change the trace turned over before kakapo read it is missing from the change log, so \"did it help?\" has nothing to centre on."
)

// chg01: the default trace off (Medium, enable-default-trace), or holding fewer than 7 days (Low).
func (b *upkeepBuild) chg01() {
	f := b.f
	switch {
	case f.TraceError != "":
		b.skip(traceNotRead+f.TraceError, "CHG01")
	case !f.TraceRunning:
		b.add("CHG01", 3, "Online", EffortQuickWin, f.ServerName, chg01OffText, chg01OffWhy, chg01OffDo,
			"sys.traces has no running trace with is_default = 1.", FixEnableDefaultTrace)
	case f.TraceOldestUTC != nil && f.TraceDays() < chg01MinDays:
		n := f.TraceDays()
		b.add("CHG01", 4, "Online", EffortQuickWin, f.ServerName,
			fmt.Sprintf("The default trace holds only %s here, less than the week the tuning report compares against.", daysWords(n)),
			chg01ShortWhy,
			fmt.Sprintf("Open this database's page at least every %s so kakapo keeps the changes; a busy server turns its trace over fast.", daysWords(max(n, 1))),
			"Oldest event in the default trace: "+f.serverClock(*f.TraceOldestUTC, true)+" (server time).", "")
	}
}

// --- enable-default-trace (CHG01) ---

// FixEnableDefaultTrace is the id of CHG01's fix; DefaultTraceSetting the sp_configure name.
const (
	FixEnableDefaultTrace = "enable-default-trace"
	DefaultTraceSetting   = "default trace enabled"
)

// DefaultTraceFacts is what enable-default-trace reads: the option's value in use, whether the
// default trace runs (sys.traces, is_default = 1), and the settings waiting for RECONFIGURE.
type DefaultTraceFacts struct {
	ServerName     string
	ProductVersion string
	ValueInUse     int64
	Running        bool
	TraceError     string // sys.traces could not be read (ALTER TRACE)
	Pending        []PendingConfig
}

func defaultTraceValue(v int64) string {
	if v == 1 {
		return "1 (on)"
	}
	return fmt.Sprintf("%d (off)", v)
}

const warnDefaultTrace = "The default trace is SQL Server's own, on by default, at most 100 MB on disk (5 files of 20 MB) beside the error log; whoever switched it off may have had a reason, so ask first."

// BuildEnableDefaultTraceFix builds enable-default-trace's preview: sp_configure 'default trace
// enabled', 1 with RECONFIGURE, as the server fixes write it; the rollback sets 0. ErrFixNotApplicable
// when the option is on and the trace runs.
func BuildEnableDefaultTraceFix(f DefaultTraceFacts) (FixPreview, Fix, error) {
	if f.ValueInUse == 1 && (f.Running || f.TraceError != "") {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := suFix(FixEnableDefaultTrace)
	fix.Current, fix.Target = defaultTraceValue(f.ValueInUse), defaultTraceValue(1)
	p := FixPreview{EngineVersion: f.ProductVersion, Warnings: []string{},
		Changes:   []Drift{{Setting: fix.Setting, Current: fix.Current, Target: fix.Target}},
		Statement: spConfigure(DefaultTraceSetting, 1),
		Rollback:  spConfigure(DefaultTraceSetting, 0),
	}
	p.Warnings = append(p.Warnings, warnServer(orQ(f.ServerName)), warnDefaultTrace)
	p.Needs = append(p.Needs, RoomNeed{Path: RoleErrorLog, Part: RoomPart{Label: "the default trace", MB: 100, Basis: "SQL Server keeps at most 5 files of 20 MB beside the error log"}})
	if pend := pendingWithout(f.Pending, DefaultTraceSetting); len(pend) > 0 {
		p.Warnings = append(p.Warnings, warnPending(pend))
	}
	p.Warnings = append(p.Warnings, permission(ScopeServer, ""))
	return p, fix, nil
}

// DefaultTraceReadBack is what was read after the fix: verified when the option is 1 in use and the
// default trace runs.
func DefaultTraceReadBack(f DefaultTraceFacts) (string, bool) {
	s := DefaultTraceSetting + " " + defaultTraceValue(f.ValueInUse)
	switch {
	case f.TraceError != "":
		s += ", the trace could not be read (" + f.TraceError + ")"
	case f.Running:
		s += ", the default trace runs"
	default:
		s += ", no default trace runs"
	}
	return s, f.ValueInUse == 1 && f.Running
}

// EnableDefaultTraceDescription is the change log line, naming what its RECONFIGURE also activated.
func EnableDefaultTraceDescription(fix Fix, f DefaultTraceFacts) string {
	return FixChangeDescription(fix, orQ(f.ServerName), "", pendingWithout(f.Pending, DefaultTraceSetting)...)
}
