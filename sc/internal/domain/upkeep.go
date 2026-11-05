package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// UpkeepFacts is what the Upkeep card reads about one database and the instance it runs on
// (docs/phase3d-contract.md, "Reads"). Every time is in the server's own clock, as GETDATE()
// returns it, carried in a time.Time with the UTC location. Each area that could not be read
// carries its reason; the checks that need it are skipped, the rest still run.
type UpkeepFacts struct {
	ServerName     string // @@SERVERNAME
	ProductVersion string
	EngineEdition  int // 4 is Express: no SQL Server Agent

	Database string
	State    string // sys.databases state_desc
	// CanChange says whether a fix may be applied to this database from here (online, writable,
	// primary). The service fills it in from the database as listed.
	CanChange bool
	// AutoShrinkOn is sys.databases is_auto_shrink_on: an AUTO_SHRINK event on this database
	// offers the auto-shrink fix only while the option is still on.
	AutoShrinkOn bool

	// MNT01: sys.dm_server_services (VIEW SERVER STATE). AgentStatus "" when the service is not
	// visible; AgentError when the read failed. Not read on Express.
	AgentStatus  string // status_desc: Running, Stopped, ...
	AgentStartup string // startup_type_desc: Automatic, Manual, ...
	AgentError   string

	// msdb, each area in its own TRY; none read on Express.
	FailedJobs       []FailedJob // MNT02
	JobsError        string
	MaintenanceJob   string // MNT03: an enabled job whose name or command matches the scan's patterns; "" none
	MaintenancePlans int    // MNT03: rows in msdb.dbo.sysmaintplan_subplans
	MaintenanceError string
	ShrinkJobs       []ShrinkJob // MNT05: enabled job steps that shrink
	ShrinkJobsError  string
	MissingAlerts    []string // MNT07: the scan's labels of the severity 19-25 / error 823-825 alerts missing
	Operators        int      // enabled operators with an email address
	AlertsOn         int      // enabled alerts
	AlertsError      string
	// SilentAlerts are the enabled severity 19-25 / error 823-825 alerts that notify no enabled
	// operator with an email address by email (msdb.dbo.sysnotifications), by name (MNT07).
	SilentAlerts []string
	// MNT08 (docs/phase5b-contract.md): the history msdb keeps and whether anything prunes it, in
	// its own TRY. BackupHistoryOldest is MIN(backup_finish_date) in msdb.dbo.backupset and
	// JobHistoryOldest the oldest run_date in msdb.dbo.sysjobhistory (nil when none). CleanupJob is
	// an enabled job whose step runs sp_delete_backuphistory, sp_purge_jobhistory or cleans
	// CommandLog; "" none.
	BackupHistoryOldest *time.Time
	BackupHistoryRows   int64
	JobHistoryRows      int64
	JobHistoryOldest    *time.Time
	CleanupJob          string
	HistoryError        string
	// MaintenancePlan is the plan read (docs/phase5b-contract.md), filled in by the service after
	// the card is built; nil when it was not read. AvailableUpkeepFixes offers the maintenance plan
	// fix from it.
	MaintenancePlan *MaintenancePlan

	// MNT05 and MNT06: the default trace, last 7 days. TraceRunning false when no default trace
	// runs; TraceError when it could not be read (needs ALTER TRACE).
	TraceRunning  bool
	TraceError    string
	TraceCommands []TraceCommand // shrinks, cache clears, plan evictions and auto-shrinks, one row per kind per database
	Growth        *TraceGrowth   // this database's file growth events; nil when none
	SevereErrors  []SevereErrors // severity 20+, one row per database
	// CHG01 and the change log (docs/phase5f-contract.md), from the same read of the whole trace:
	// its oldest and newest event (UTC) and its DDL events in user databases, for every database.
	TraceOldestUTC *time.Time
	TraceNewestUTC *time.Time
	SchemaChanges  []SchemaChangeFact
	// LoginName is SUSER_SNAME(): the login kakapo reads with, for the ALTER TRACE grant.
	LoginName string

	// MNT06: sys.dm_server_memory_dumps (VIEW SERVER STATE).
	Dumps      int        // in the last 30 days
	DumpLast   *time.Time // the latest in the last 30 days
	DumpFile   string     // the latest dump's file
	DumpsError string

	// MNT04: inside the database.
	StaleStats []StaleStat
	StatsError string

	ServerNow        time.Time // GETDATE()
	UTCOffsetMinutes int

	Error string // nothing could be read at all
}

// FailedJob is one Agent job that failed in the last 7 days (MNT02).
type FailedJob struct {
	Name     string
	Fails    int
	LastDate time.Time // the run_date of the last failure
	Step     string    // the last failing step; "" when the history holds none
	Message  string
	HasStep  bool
}

// ShrinkJob is an enabled Agent job step that shrinks a database or file (MNT05).
type ShrinkJob struct {
	Job     string
	Step    int
	Command string
}

// TraceCommand is the shrinks, the cache clears, the single-plan evictions or the AUTO_SHRINK
// runs the default trace recorded on one database.
type TraceCommand struct {
	Kind     string // TraceShrink, TraceCache, TraceEvict or TraceAutoShrink
	Database string // "" when the trace has none
	Count    int
	LastAt   time.Time
	Login    string
	App      string
	Sample   string
}

// TraceGrowth is this database's file growth events in the default trace.
type TraceGrowth struct {
	Count    int
	Seconds  int64 // SUM(Duration) / 1000000
	LogGrows int
	LastAt   time.Time
	// The slowest growth event: its Duration (microseconds in the trace) in milliseconds, its
	// logical file name and when it happened.
	SlowestMs   int64
	SlowestFile string
	SlowestAt   time.Time
}

// SevereErrors is the errors of severity 20 and up the default trace recorded on one database.
type SevereErrors struct {
	Database string // "" for an instance-wide error
	Count    int
	LastAt   time.Time
	Sample   string
}

// StaleStat is one out-of-date statistic on a table of this database (MNT04, Apply-DoNow step 5).
type StaleStat struct {
	Schema        string
	Table         string
	Stats         string
	Rows          int64 // sys.dm_db_stats_properties rows
	Modifications int64
	LastUpdated   time.Time
	TableRows     int64 // sys.dm_db_partition_stats row count of the heap or clustered index
}

// Kinds of trace commands.
const (
	TraceShrink     = "shrink"
	TraceCache      = "cache"      // a bare FREEPROCCACHE, FREESYSTEMCACHE or DROPCLEANBUFFERS
	TraceEvict      = "evict"      // FREEPROCCACHE with a plan handle: one plan evicted
	TraceAutoShrink = "autoshrink" // event classes 94 and 95: AUTO_SHRINK at work
)

// AsOf is ServerNow in UTC.
func (f UpkeepFacts) AsOf() time.Time {
	t := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// IsExpress says whether this is Express (EngineEdition 4), which has no SQL Server Agent.
func (f UpkeepFacts) IsExpress() bool { return f.EngineEdition == 4 }

// instanceUnread says whether every instance-level read failed: the card cannot answer.
func (f UpkeepFacts) instanceUnread() bool {
	return !f.IsExpress() && f.AgentError != "" && f.JobsError != "" && f.MaintenanceError != "" &&
		f.ShrinkJobsError != "" && f.AlertsError != "" && f.TraceError != "" && f.DumpsError != ""
}

// Notes for checks that were skipped, from the scan where it has one.
const (
	expressSkipped   = "Not applicable to Express edition (no SQL Server Agent)."
	agentNotVisible  = "Agent service status is not visible on this platform."
	agentNotRead     = "Could not read the SQL Server Agent service (sys.dm_server_services): "
	msdbNotReadable  = "msdb is not readable with this login. "
	traceNotRunning  = "The default trace is not running."
	traceNotRead     = "Could not read the default trace (needs ALTER TRACE): "
	dumpsNotRead     = "Could not read the memory dumps (sys.dm_server_memory_dumps): "
	statsNotRead     = "Could not read statistics for this database: "
	upkeepInstNotRd  = "Could not read the instance's upkeep (needs VIEW SERVER STATE, ALTER TRACE and msdb): "
	upkeepNoneFound  = "No upkeep problem found."
	updateStatsNoRb  = "No rollback: updated statistics are never worse than stale ones."
	updateStatsWarn1 = "Updating statistics reads each table; on large tables this takes minutes and competes with users. Run it outside peak hours."
	updateStatsWarn2 = "This does not create a maintenance job (MNT03); it fixes today's statistics once."
	updateStatsWarn3 = "Needs ALTER on the tables (db_owner or db_ddladmin)."
)

// FixUpdateStatistics is the id of the Upkeep card's statistics fix (MNT04).
const FixUpdateStatistics = "update-statistics"

// FixAutoShrink is the id of the Setup card's AUTO_SHRINK fix (DBC03), which MNT05 also offers when
// the default trace shows AUTO_SHRINK running on this database while the option is still on.
const FixAutoShrink = "auto-shrink"

// UpdateStatisticsPermission is what a refused statistics update names.
const UpdateStatisticsPermission = updateStatsWarn3

// BuildUpkeepCard answers "Is someone looking after it?" for one database from what was read: the
// card, its findings ranked (MNT04 with the update-statistics fix when the database can be
// changed), and what became of each check behind it, in catalogue order.
func BuildUpkeepCard(f UpkeepFacts, def CardDef) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	b := &upkeepBuild{f: f, findings: []Finding{}, skipped: map[string]string{}}

	switch {
	case f.Error != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", f.Error
		b.skipAll(def.ID, f.Error)
		return c, b.findings, b.checks(def.ID)
	case f.State != "ONLINE":
		note := fmt.Sprintf("%s is %s, not online, so its upkeep was not checked.", f.Database, f.State)
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID)
	}

	b.mnt01()
	if f.IsExpress() {
		b.skip(expressSkipped, "MNT02", "MNT03", "MNT07", "MNT08")
	} else {
		b.mnt02()
		b.mnt03()
	}
	b.mnt04()
	b.mnt05()
	b.mnt06()
	b.chg01()
	if !f.IsExpress() {
		b.mnt07()
		b.mnt08()
	}
	RankFindings(b.findings)
	checks := b.checks(def.ID)

	if f.instanceUnread() {
		// Without the instance the card cannot answer its question. MNT04 still ran, and its
		// finding is kept for the detail page.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", upkeepInstNotRd+f.AgentError
		return c, b.findings, checks
	}

	actionable, critical, clientIT, fixable := 0, false, 0, 0
	for _, x := range b.findings {
		if x.Severity.Actionable() {
			actionable++
			if x.Owner == OwnerClientIT {
				clientIT++
			}
		}
		if x.FixID != "" {
			fixable++
		}
		critical = critical || x.Severity == SeverityCritical
	}
	switch {
	case critical:
		c.Status, c.StatusText = StatusRed, "Needs attention today"
	case actionable == 1:
		c.Status, c.StatusText = StatusAmber, "1 thing to do"
	case actionable > 1:
		c.Status, c.StatusText = StatusAmber, fmt.Sprintf("%d things to do", actionable)
	default:
		c.Status, c.StatusText = StatusGreen, "No problem found"
	}
	if actionable > 0 {
		c.Verdict = b.findings[0].Finding
	} else {
		c.Verdict = b.greenVerdict()
	}
	c.ToDo, c.ForClientIT, c.Fixable = actionable, clientIT, fixable
	asOf := f.AsOf()
	c.AsOf = &asOf
	c.Facts = b.facts()
	return c, b.findings, checks
}

type upkeepBuild struct {
	f        UpkeepFacts
	findings []Finding
	skipped  map[string]string
}

func (b *upkeepBuild) add(id string, rank int, changeType, effort, object, finding, why, doThis, evidence, fixID string) {
	chk, _ := CheckByID(id)
	b.findings = append(b.findings, Finding{
		CheckID: id, Title: chk.Title, Severity: SeverityFromRank(min(rank, 5)), Owner: chk.Owner,
		When: WhenFromChangeType(changeType), Effort: effort, Object: object,
		Finding: finding, Why: why, DoThis: doThis, ThenCheck: chk.ThenCheck, Evidence: evidence, FixID: fixID,
	})
}

// ins adds an instance finding: its object is @@SERVERNAME.
func (b *upkeepBuild) ins(id string, rank int, changeType, effort, finding, why, doThis, evidence string) {
	b.add(id, rank, changeType, effort, b.f.ServerName, finding, why, doThis, evidence, "")
}

// on is the object of a trace finding: the database it names, or the server when it names none.
func (b *upkeepBuild) on(database string) string {
	if database == "" {
		return b.f.ServerName
	}
	return database
}

func (b *upkeepBuild) actionable(ids ...string) bool {
	for _, x := range b.findings {
		for _, id := range ids {
			if x.CheckID == id && x.Severity.Actionable() {
				return true
			}
		}
	}
	return false
}

func (b *upkeepBuild) skip(note string, ids ...string) {
	for _, id := range ids {
		if _, ok := b.skipped[id]; !ok {
			b.skipped[id] = note
		}
	}
}

func (b *upkeepBuild) skipAll(card CardID, note string) {
	for _, c := range ChecksFor(card) {
		b.skipped[c.ID] = note
	}
}

// checks says what became of each check: a finding when it found something to act on (even when
// part of it could not run, whose note it keeps), skipped with its note, passed otherwise.
func (b *upkeepBuild) checks(card CardID) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(card) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
		note, skipped := b.skipped[c.ID]
		switch {
		case b.actionable(c.ID):
			st.Status, st.Note = CheckFinding, note
		case skipped:
			st.Status, st.Note = CheckSkipped, note
		}
		out = append(out, st)
	}
	return out
}

// Texts from 01-QuickScan.sql sections 6 and 8 and the per-database MNT04, verbatim.
const (
	mnt01ExpressFinding = "SQL Server Express has no SQL Server Agent."
	mnt01ExpressWhy     = "Backups, integrity checks and statistics maintenance do not run on their own."
	mnt01ExpressDo      = "Make sure backups and maintenance are scheduled another way, e.g. Windows Task Scheduler running sqlcmd."
	mnt01StoppedWhy     = "Agent runs scheduled backups, integrity checks and maintenance. While it is stopped, none of them run."
	mnt01StoppedDo      = "Start the SQL Server Agent service and set its startup type to Automatic (SQL Server Configuration Manager)."
	mnt01ManualWhy      = "After the next server restart, scheduled backups and maintenance silently stop."
	mnt01ManualDo       = "Set the SQL Server Agent startup type to Automatic (SQL Server Configuration Manager)."
	mnt02Why            = "Failing jobs usually mean backups, maintenance or business processes are silently not happening."
	mnt02Do             = "Open the job history in SSMS (SQL Server Agent > Jobs > right-click > View History), fix the cause, and set up failure notifications (Database Mail and an operator)."
	mnt03Why            = "Statistics and indexes degrade as data changes. Without regular maintenance, queries slowly get worse over weeks and months."
	mnt03Do             = "Nightly statistics updates (Ola Hallengren's IndexOptimize with @StatisticsModificationLevel = 5 and no index work) matter most; rebuild or reorganise indexes weekly or not at all. (Detection is based on job names and commands: if another tool does this, mark it as not applicable.)"
	mnt04Why            = "Out-of-date statistics make the optimizer guess row counts wrongly, which leads to slow plans: scans instead of seeks, wrong join types, memory spills."
	mnt05ShrinkWhy      = "Shrinking fragments every index in the file and the file grows straight back, pausing queries while it does. Regular shrinking is the most common self-inflicted performance problem."
	mnt05CacheWhy       = "Clearing the cache makes every query compile again (a CPU spike) and empties the memory SQL Server had filled with data, so everything reads from disk for a while."
	mnt05ShrinkDo       = "Stop the shrink (find the job or maintenance plan step, or the person). If space must be reclaimed once, shrink once in a window and rebuild the indexes afterwards."
	mnt05CacheDo        = "Find the job or person running DBCC FREEPROCCACHE / DROPCLEANBUFFERS and stop it. If it was a workaround for a query that is fast for some values and slow for others, fix that query instead: its statistics, an index or the query itself."
	mnt05GrowthWhy      = "Every growth pauses the queries that need the space while the file is extended (and zeroed, without instant file initialization). Many small growths mean the growth setting is too small."
	mnt05GrowthDo       = "Grow the file once to the size it needs for the next year, and set autogrowth to a fixed 256 MB (data) or 128 MB (log): see STO02 and STO03."
	mnt05JobWhy         = "A scheduled shrink fragments every index each time it runs, and the file grows straight back while users wait."
	mnt05JobDo          = "Remove the shrink step from the job. If the job is a Maintenance Plan, delete its Shrink Database task."
	mnt06ErrorsWhy      = "Severity 20 and up means SQL Server hit a fatal error: corruption (823, 824, 825), a crashed session, or a resource it could not get."
	mnt06ErrorsDo       = "Read the SQL Server error log around the time in the evidence (SSMS: Management > SQL Server Logs). Errors 823/824/825 are corruption: run DBCC CHECKDB and involve a DBA today."
	mnt06DumpsWhy       = "A memory dump means SQL Server hit an error serious enough to record its memory: a bug, corruption, or failing hardware. Some dumps come with an outage."
	mnt06DumpsDo        = "Ask the client's IT to open a case with Microsoft with the dump and the error log, check the Windows event log for hardware errors, and install the latest cumulative update (INS01)."
	mnt07NoOperator     = "No operator with an email address exists, so no alert or job failure can reach anyone."
	mnt07Why            = "Corruption (errors 823, 824, 825) and fatal errors (severity 19 to 25) are only noticed when users complain, unless an alert mails someone. By then the corruption is in the backups too."
	mnt05EvictWhy       = "Harmless: evicting one plan is how a DBA makes one badly planned query compile again; the rest of the cache is untouched."
	mnt05EvictDo        = "No action."
	mnt05AutoShrinkDo   = "Switch AUTO_SHRINK off (ALTER DATABASE ... SET AUTO_SHRINK OFF, online and instant) and size the files for the data they hold."
	mnt07SilentWhy      = "An alert that notifies no one is the same as no alert: corruption and fatal errors are still only noticed when users complain."
	mnt07SilentDo       = "Add the operator to each alert (SSMS: SQL Server Agent > Alerts > the alert > Properties > Response, tick E-mail for the operator), or recreate them with notifications (sp_add_notification)."
	mnt07Do             = "Set up Database Mail and an operator for the client's IT mailbox, then one alert per severity 19 to 25 and per error 823, 824 and 825, each notifying the operator (sp_add_alert, sp_add_notification). Test it from a throwaway SSMS window: RAISERROR('Alert test', 20, 1) WITH LOG (it closes that window)."
)

// left is T-SQL LEFT: the first n characters.
func left(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func sqlDay(t time.Time) string { return t.Format("2006-01-02") }

// mnt01: Express has no Agent (Info); otherwise the Agent not running (High) or not automatic (Low).
func (b *upkeepBuild) mnt01() {
	f := b.f
	switch {
	case f.IsExpress():
		b.ins("MNT01", 5, "None", EffortNone, mnt01ExpressFinding, mnt01ExpressWhy, mnt01ExpressDo, "")
	case f.AgentError != "":
		b.skip(agentNotRead+f.AgentError, "MNT01")
	case f.AgentStatus == "":
		b.skip(agentNotVisible, "MNT01")
	case f.AgentStatus != "Running":
		startup := f.AgentStartup
		if startup == "" {
			startup = "?"
		}
		b.ins("MNT01", 2, "Online", EffortQuickWin, "SQL Server Agent is not running.", mnt01StoppedWhy, mnt01StoppedDo,
			"Status: "+f.AgentStatus+", startup: "+startup+".")
	case f.AgentStartup != "" && f.AgentStartup != "Automatic":
		b.ins("MNT01", 4, "Online", EffortQuickWin, "SQL Server Agent will not start automatically after a reboot.",
			mnt01ManualWhy, mnt01ManualDo, "Startup type: "+f.AgentStartup+".")
	}
}

// isBackupJob is the scan's j.name LIKE N'%backup%' under a case-insensitive collation.
func isBackupJob(name string) bool { return strings.Contains(strings.ToLower(name), "backup") }

// mnt02: one finding per job that failed in the last 7 days; a backup job High, else Medium.
func (b *upkeepBuild) mnt02() {
	f := b.f
	if f.JobsError != "" {
		b.skip(msdbNotReadable+f.JobsError, "MNT02")
		return
	}
	for _, j := range f.FailedJobs {
		rank := 3
		if isBackupJob(j.Name) {
			rank = 2
		}
		evidence := "Last failure: " + sqlDay(j.LastDate) + ". "
		if j.HasStep {
			evidence += "Step \"" + j.Step + "\": " + j.Message
		}
		b.ins("MNT02", rank, "Online", EffortModerate,
			fmt.Sprintf("Agent job \"%s\" failed %d time(s) in the last 7 days.", j.Name, j.Fails), mnt02Why, mnt02Do,
			strings.TrimSpace(left(evidence, 4000)))
	}
}

// mnt03: no enabled maintenance job and no maintenance plan subplan.
func (b *upkeepBuild) mnt03() {
	f := b.f
	if f.MaintenanceError != "" {
		b.skip(msdbNotReadable+f.MaintenanceError, "MNT03")
		return
	}
	if f.MaintenanceJob == "" && f.MaintenancePlans == 0 {
		b.add("MNT03", 3, "Online", EffortModerate, f.ServerName, "No enabled index or statistics maintenance job was found.", mnt03Why, mnt03Do, "",
			FixMaintenanceJob)
	}
}

// staleTable is one table with out-of-date statistics.
type staleTable struct {
	Schema, Table string
	Mods, Rows    int64 // the most changed statistic's modifications and rows
	TableRows     int64
}

// staleTables groups the stale statistics by table, in the order the facts list them.
func (f UpkeepFacts) staleTables() []staleTable {
	out := []staleTable{}
	at := map[[2]string]int{}
	for _, s := range f.StaleStats {
		k := [2]string{s.Schema, s.Table}
		i, ok := at[k]
		if !ok {
			i = len(out)
			at[k] = i
			out = append(out, staleTable{Schema: s.Schema, Table: s.Table, TableRows: s.TableRows})
		}
		out[i].Mods = max(out[i].Mods, s.Modifications)
		out[i].Rows = max(out[i].Rows, s.Rows)
		out[i].TableRows = max(out[i].TableRows, s.TableRows)
	}
	return out
}

// mnt04: one finding with the count of out-of-date statistics and the five most changed tables.
func (b *upkeepBuild) mnt04() {
	f := b.f
	if f.StatsError != "" {
		b.skip(statsNotRead+f.StatsError, "MNT04")
		return
	}
	if len(f.StaleStats) == 0 {
		return
	}
	tables := f.staleTables()
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Mods != tables[j].Mods {
			return tables[i].Mods > tables[j].Mods
		}
		return tables[i].Schema+"."+tables[i].Table < tables[j].Schema+"."+tables[j].Table
	})
	parts := []string{}
	for i, t := range tables {
		if i == 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s.%s (%d changes / %d rows)", t.Schema, t.Table, t.Mods, t.Rows))
	}
	fix := FixUpdateStatistics
	if !f.CanChange {
		fix = ""
	}
	b.add("MNT04", 3, "Online", EffortQuickWin, f.Database,
		fmt.Sprintf("%d statistics on large tables are out of date (over 20%% of rows changed, not updated for 7+ days).", len(f.StaleStats)),
		mnt04Why,
		"Update them outside peak hours: USE "+QuoteName(f.Database)+"; EXEC sp_updatestats;  Then schedule regular statistics maintenance (MNT03).",
		left("Most changed tables: "+strings.Join(parts, ", "), 4000), fix)
}

// mnt05: shrinks and cache clears from the default trace, this database's file growths, and the
// Agent job steps that shrink.
func (b *upkeepBuild) mnt05() {
	f := b.f
	switch {
	case f.TraceError != "":
		b.skip(traceNotRead+f.TraceError, "MNT05")
	case !f.TraceRunning:
		b.skip(traceNotRunning, "MNT05")
	default:
		for _, x := range f.TraceCommands {
			rank, changeType, effort, fix := 3, "Online", EffortQuickWin, ""
			finding, why, do := "", mnt05CacheWhy, mnt05CacheDo
			switch x.Kind {
			case TraceShrink:
				finding = fmt.Sprintf("A database or file was shrunk %d time(s) in the last 7 days.", x.Count)
				why, do = mnt05ShrinkWhy, mnt05ShrinkDo
			case TraceEvict:
				// Informational, as DarlingData sp_PerfCheck.sql 1984 ranks a plan-handle FREEPROCCACHE.
				rank, changeType, effort = 5, "None", EffortNone
				finding = fmt.Sprintf("One plan was evicted from the cache %d time(s) in the last 7 days.", x.Count)
				why, do = mnt05EvictWhy, mnt05EvictDo
			case TraceAutoShrink:
				finding = fmt.Sprintf("AUTO_SHRINK ran %d times in the last 7 days on %s.", x.Count, b.on(x.Database))
				why, do = dbc03Why, mnt05AutoShrinkDo
				if strings.EqualFold(x.Database, f.Database) && f.AutoShrinkOn && f.CanChange {
					fix = FixAutoShrink
				}
			default:
				finding = fmt.Sprintf("The plan cache or buffer pool was cleared %d time(s) in the last 7 days.", x.Count)
			}
			evidence := "Last: " + sqlTime(x.LastAt) + " by " + orQ(x.Login) + " from " + orQ(x.App) + "."
			if x.Sample != "" {
				evidence += " Example: " + x.Sample
			}
			b.add("MNT05", rank, changeType, effort, b.on(x.Database), finding, why, do, left(evidence, 1000), fix)
		}
		if g := f.Growth; g != nil && g.Count >= 20 {
			rank := 4
			if g.Count >= 100 {
				rank = 3
			}
			b.add("MNT05", rank, "Online", EffortQuickWin, f.Database,
				fmt.Sprintf("Files grew %d times in the last 7 days (%s seconds spent growing).", g.Count, n0int(g.Seconds)),
				mnt05GrowthWhy, mnt05GrowthDo,
				fmt.Sprintf("Data file growths: %d, log growths: %d. Last: %s.", g.Count-g.LogGrows, g.LogGrows, sqlTime(g.LastAt))+
					SlowGrowthSentence(g.SlowestMs, g.SlowestFile, g.SlowestAt), "")
		}
	}
	if f.IsExpress() {
		return
	}
	if f.ShrinkJobsError != "" {
		b.skip(msdbNotReadable+f.ShrinkJobsError, "MNT05")
		return
	}
	// The object of a job step finding is the job, which the remove-shrink-step fix's options name.
	for _, j := range f.ShrinkJobs {
		b.add("MNT05", 3, "Online", EffortQuickWin, j.Job,
			fmt.Sprintf("Agent job \"%s\" (step %d) shrinks a database or file.", j.Job, j.Step), mnt05JobWhy, mnt05JobDo, left(j.Command, 300),
			FixRemoveShrinkStep)
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// mnt06: severe errors in the default trace (High) and memory dumps in 30 days (High within 7
// days, else Medium).
func (b *upkeepBuild) mnt06() {
	f := b.f
	traceOK := f.TraceError == "" && f.TraceRunning
	if traceOK {
		for _, e := range f.SevereErrors {
			b.add("MNT06", 2, "None", EffortModerate, b.on(e.Database),
				fmt.Sprintf("The error log recorded %d error(s) of severity 20 or higher in the last 7 days.", e.Count),
				mnt06ErrorsWhy, mnt06ErrorsDo, left("Last: "+sqlTime(e.LastAt)+". Example: "+e.Sample, 1000), "")
		}
	}
	if f.DumpsError != "" {
		if !traceOK {
			note := dumpsNotRead + f.DumpsError
			if f.TraceError != "" {
				note = traceNotRead + f.TraceError + " " + note
			}
			b.skip(note, "MNT06")
		}
		return
	}
	if f.Dumps > 0 {
		rank := 3
		if f.DumpLast != nil && !f.DumpLast.Before(f.ServerNow.AddDate(0, 0, -7)) {
			rank = 2
		}
		b.ins("MNT06", rank, "None", EffortModerate,
			fmt.Sprintf("SQL Server wrote %d memory dump(s) in the last 30 days.", f.Dumps), mnt06DumpsWhy, mnt06DumpsDo,
			"Last dump: "+sqlTimeOr(f.DumpLast, "?")+". File: "+orQ(f.DumpFile)+".")
	}
}

// mnt07: no operator with an email, or any of the severity 19-25 / error 823-825 alerts missing.
func (b *upkeepBuild) mnt07() {
	f := b.f
	if f.AlertsError != "" {
		b.skip(msdbNotReadable+f.AlertsError, "MNT07")
		return
	}
	evidence := fmt.Sprintf("Operators with email: %d. Alerts enabled: %d.", f.Operators, f.AlertsOn)
	// With no operator every alert is silent, and the no-operator finding says so.
	if f.Operators > 0 && len(f.SilentAlerts) > 0 {
		b.add("MNT07", 4, "Online", EffortQuickWin, f.ServerName,
			fmt.Sprintf("%d alert(s) exist but tell no one: %s.", len(f.SilentAlerts), strings.Join(f.SilentAlerts, ", ")),
			mnt07SilentWhy, mnt07SilentDo, evidence, FixOperatorAlerts)
	}
	if f.Operators > 0 && len(f.MissingAlerts) == 0 {
		return
	}
	finding := mnt07NoOperator
	if f.Operators > 0 {
		finding = "No alert for: " + strings.Join(f.MissingAlerts, ", ") + "."
	}
	b.add("MNT07", 4, "Online", EffortQuickWin, f.ServerName, finding, mnt07Why, mnt07Do, evidence, FixOperatorAlerts)
}

// MNT08's texts and thresholds (docs/phase5b-contract.md). Sources: Microsoft, "sp_delete_backuphistory"
// ("we recommend that you periodically execute sp_delete_backuphistory": msdb grows with every
// backup); Ola Hallengren's installation prunes backup and job history weekly, keeping 30 days. The
// thresholds of Medium (a million rows, two years) are the contract's.
const (
	mnt08Why = "msdb is SQL Server's own database of Agent jobs and backups. Every backup and every job run adds rows to its history and nothing deletes them: a bloated msdb slows SQL Server Agent, the backup and restore dialogs and the Backups card's own reads, and it is carried in every backup of msdb. Microsoft recommends pruning the backup history periodically (sp_delete_backuphistory)."
	mnt08Do  = "Create the weekly history cleanup (the fix): every Sunday at 05:00 it deletes the backup and job history older than 30 days (sp_delete_backuphistory, sp_purge_jobhistory), as Ola Hallengren's installation does. The backup files themselves are not touched."
	// mnt08MediumRows: more history rows than this (backup and job history together) is Medium.
	mnt08MediumRows  = 1_000_000
	mnt08MediumYears = 2
)

// mnt08: msdb keeps backup and job history and no enabled job prunes it. Low; Medium from a million
// rows or history over two years old. Silent when a cleanup job exists, or when msdb holds no
// history at all.
func (b *upkeepBuild) mnt08() {
	f := b.f
	if f.HistoryError != "" {
		b.skip(msdbNotReadable+f.HistoryError, "MNT08")
		return
	}
	rows := f.BackupHistoryRows + f.JobHistoryRows
	if f.CleanupJob != "" || rows == 0 {
		return
	}
	rank := 4
	old := f.ServerNow.AddDate(-mnt08MediumYears, 0, 0)
	if rows > mnt08MediumRows || (f.BackupHistoryOldest != nil && f.BackupHistoryOldest.Before(old)) ||
		(f.JobHistoryOldest != nil && f.JobHistoryOldest.Before(old)) {
		rank = 3
	}
	backups := n0int(f.BackupHistoryRows) + " backup history rows"
	if f.BackupHistoryOldest != nil {
		backups += " going back to " + sqlDay(*f.BackupHistoryOldest)
	}
	finding := "msdb keeps " + backups + " and " + n0int(f.JobHistoryRows) + " job history rows; nothing prunes them."
	evidence := "Backup history (msdb.dbo.backupset): " + n0int(f.BackupHistoryRows) + " rows" + sinceDay(f.BackupHistoryOldest) +
		". Job history (msdb.dbo.sysjobhistory): " + n0int(f.JobHistoryRows) + " rows" + sinceDay(f.JobHistoryOldest) +
		". No enabled job runs sp_delete_backuphistory, sp_purge_jobhistory or a CommandLog cleanup."
	b.add("MNT08", rank, "Online", EffortQuickWin, f.ServerName, finding, mnt08Why, mnt08Do, evidence, FixHistoryCleanup)
}

func sinceDay(t *time.Time) string {
	if t == nil {
		return ""
	}
	return " since " + sqlDay(*t)
}

// SlowGrowthSentence is the evidence sentence for an autogrowth that took a second or more, with
// its leading space: " The slowest growth took 4.2 s (WarehouseLive_log, 2026-11-02 22:05)."; ""
// for a quicker one.
func SlowGrowthSentence(ms int64, file string, at time.Time) string {
	if ms < 1000 {
		return ""
	}
	return fmt.Sprintf(" The slowest growth took %s s (%s, %s).", n1(float64(ms)/1000), orQ(file), sqlTime(at))
}

// greenVerdict: "SQL Server Agent is running, {maintenance} and no job failed in 7 days.", with
// the parts that could be read.
func (b *upkeepBuild) greenVerdict() string {
	f := b.f
	if f.IsExpress() {
		return mnt01ExpressFinding
	}
	parts := []string{}
	if f.AgentError == "" && f.AgentStatus == "Running" {
		parts = append(parts, "SQL Server Agent is running")
	}
	if f.MaintenanceError == "" {
		if f.MaintenanceJob != "" {
			parts = append(parts, "\""+f.MaintenanceJob+"\" runs")
		} else {
			parts = append(parts, "maintenance runs")
		}
	}
	if f.JobsError == "" {
		parts = append(parts, "no job failed in 7 days")
	}
	switch len(parts) {
	case 0:
		return upkeepNoneFound
	case 1:
		return upperFirst(parts[0]) + "."
	}
	return upperFirst(strings.Join(parts[:len(parts)-1], ", ")) + " and " + parts[len(parts)-1] + "."
}

func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// facts are the four lines on the card: SQL Agent, Failed jobs, Maintenance job, Told about errors.
func (b *upkeepBuild) facts() []Fact {
	f := b.f
	agent := Fact{Label: "SQL Agent", Value: "could not be read", Tone: ToneNormal}
	jobs := Fact{Label: "Failed jobs, 7 days", Value: "could not be read", Tone: ToneNormal}
	maint := Fact{Label: "Maintenance job", Value: "could not be read", Tone: ToneNormal}
	told := Fact{Label: "Told about errors", Value: "could not be read", Tone: ToneNormal}
	if f.IsExpress() {
		agent.Value = "none (Express)"
		jobs.Value, maint.Value, told.Value = "not checked (Express)", "not checked (Express)", "not checked (Express)"
		return []Fact{agent, jobs, maint, told}
	}

	switch {
	case f.AgentError != "" || f.AgentStatus == "":
	case f.AgentStatus != "Running":
		agent.Value, agent.Tone = "stopped", ToneWorse
		if f.AgentStatus != "Stopped" {
			agent.Value = strings.ToLower(f.AgentStatus)
		}
	case f.AgentStartup != "" && f.AgentStartup != "Automatic":
		agent.Value, agent.Tone = "not automatic", ToneBad
	default:
		agent.Value = "running, automatic"
	}

	if f.JobsError == "" {
		jobs.Value = fmt.Sprint(len(f.FailedJobs))
		for _, j := range f.FailedJobs {
			jobs.Tone = ToneBad
			if isBackupJob(j.Name) {
				jobs.Tone = ToneWorse
				break
			}
		}
	}

	if f.MaintenanceError == "" {
		maint.Value = "found"
		if f.MaintenanceJob == "" && f.MaintenancePlans == 0 {
			maint.Value, maint.Tone = "none found", ToneBad
		}
	}

	if f.AlertsError == "" {
		switch {
		case f.Operators == 0:
			told.Value, told.Tone = "no operator", ToneBad
		case len(f.MissingAlerts) > 0:
			told.Value, told.Tone = fmt.Sprintf("%d alerts missing", len(f.MissingAlerts)), ToneBad
		case len(f.SilentAlerts) > 0:
			told.Value, told.Tone = fmt.Sprintf("%d alerts tell no one", len(f.SilentAlerts)), ToneBad
		default:
			told.Value = fmt.Sprintf("operator and %d alerts", f.AlertsOn)
		}
	}
	return []Fact{agent, jobs, maint, told}
}

// UpdateStatisticsStatement is Apply-DoNow step 5 scoped to one database: one UPDATE STATISTICS
// line per table with a stale statistic, smallest table first (by sys.dm_db_partition_stats row
// count). It runs inside the database.
func (f UpkeepFacts) UpdateStatisticsStatement() string {
	tables := f.staleTables()
	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].TableRows != tables[j].TableRows {
			return tables[i].TableRows < tables[j].TableRows
		}
		return tables[i].Schema+"."+tables[i].Table < tables[j].Schema+"."+tables[j].Table
	})
	lines := make([]string, 0, len(tables))
	for _, t := range tables {
		lines = append(lines, "UPDATE STATISTICS "+QuoteName(t.Schema)+"."+QuoteName(t.Table)+";")
	}
	return strings.Join(lines, "\n")
}

// StaleStatsValue is how the statistics fix shows the count of out-of-date statistics read back:
// "none out of date", or "{n} still out of date".
func StaleStatsValue(n int) string {
	if n == 0 {
		return "none out of date"
	}
	return fmt.Sprintf("%d still out of date", n)
}

// UpdateStatisticsDescription is the change log line of the statistics fix.
func UpdateStatisticsDescription(dbName string, tables int) string {
	return fmt.Sprintf("Statistics on %s: %d tables updated", dbName, tables)
}

// BuildUpkeepFix builds the preview and the fix for one of the Upkeep card's fixes from what was
// read, never from client input. ErrUnknownFix for an id the Upkeep card does not offer,
// ErrFixNotApplicable when the finding behind it did not fire (or the database cannot be changed
// from here, so the finding carries no fix).
func BuildUpkeepFix(fixID string, f UpkeepFacts) (FixPreview, Fix, error) {
	d, ok := FindFixDef(fixID)
	if !ok || d.Card() != CardUpkeep {
		return FixPreview{}, Fix{}, ErrUnknownFix
	}
	def, _ := FindCardDef(CardUpkeep)
	_, findings, _ := BuildUpkeepCard(f, def)
	fired := false
	for _, x := range findings {
		fired = fired || x.FixID == fixID
	}
	if !fired {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	return buildUpkeepFix(d, f)
}

func buildUpkeepFix(d FixDef, f UpkeepFacts) (FixPreview, Fix, error) {
	if d.ID != FixUpdateStatistics || len(f.StaleStats) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	tables := len(f.staleTables())
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: fmt.Sprintf("%d statistics on %d tables out of date", len(f.StaleStats), tables), Target: StaleStatsValue(0)}
	p := FixPreview{
		Database: f.Database, EngineVersion: f.ProductVersion,
		Changes:   []Drift{{Setting: fix.Setting, Current: fix.Current, Target: fix.Target}},
		Statement: f.UpdateStatisticsStatement(),
		Rollback:  updateStatsNoRb,
		Warnings:  []string{updateStatsWarn1, updateStatsWarn2, updateStatsWarn3},
	}
	return p, fix, nil
}

// AvailableUpkeepFixes are the fixes of the Upkeep findings that fired on this read, in the order
// the findings rank (MNT08 offers history-cleanup), then the maintenance plan when the plan read
// (f.MaintenancePlan) has a piece to create (docs/phase5b-contract.md).
func AvailableUpkeepFixes(f UpkeepFacts, findings []Finding) []Fix {
	out := []Fix{}
	seen := map[string]bool{}
	for _, x := range findings {
		if x.FixID == "" {
			continue
		}
		if isUpkeepAction(x.FixID) || x.FixID == FixEnableDefaultTrace {
			// The registered actions (docs/phase4d-contract.md): one per finding (per shrink step),
			// with the options the preview needs.
			for _, fix := range upkeepActionFixes(f, x) {
				key := fix.ID + "\x00" + fix.Setting
				if !seen[key] {
					seen[key] = true
					out = append(out, fix)
				}
			}
			continue
		}
		if seen[x.FixID] {
			continue
		}
		seen[x.FixID] = true
		if x.FixID == FixHistoryCleanup {
			out = append(out, historyCleanupOffer(f))
			continue
		}
		if x.FixID == FixAutoShrink {
			// The Setup card's fix (DBC03), offered here by an AUTO_SHRINK event and applied
			// through the Setup path.
			if d, ok := FindFixDef(FixAutoShrink); ok {
				out = append(out, Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
					Current: onOff(true), Target: onOff(false)})
			}
			continue
		}
		if d, ok := FindFixDef(x.FixID); ok {
			if _, fix, err := buildUpkeepFix(d, f); err == nil {
				out = append(out, fix)
			}
		}
	}
	if fix, ok := MaintenancePlanOffer(f.MaintenancePlan); ok {
		out = append(out, fix)
	}
	return out
}

// UpdateStatisticsTables is how many tables the statistics fix updates.
func (f UpkeepFacts) UpdateStatisticsTables() int { return len(f.staleTables()) }
