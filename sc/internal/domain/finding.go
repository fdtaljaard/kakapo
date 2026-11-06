package domain

import "sort"

// Severity is how bad a finding is, in the words of the HTML reports. The scan ranks 1..5.
type Severity string

const (
	SeverityCritical Severity = "Critical"
	SeverityHigh     Severity = "High"
	SeverityMedium   Severity = "Medium"
	SeverityLow      Severity = "Low"
	SeverityInfo     Severity = "Info"
)

// SeverityFromRank turns the scan's rank (1 most severe .. 5 information) into a Severity.
// A rank outside 1..5 is clamped.
func SeverityFromRank(rank int) Severity {
	switch {
	case rank <= 1:
		return SeverityCritical
	case rank == 2:
		return SeverityHigh
	case rank == 3:
		return SeverityMedium
	case rank == 4:
		return SeverityLow
	}
	return SeverityInfo
}

// Rank is the scan's rank of a severity: 1 for Critical .. 5 for Info.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 1
	case SeverityHigh:
		return 2
	case SeverityMedium:
		return 3
	case SeverityLow:
		return 4
	}
	return 5
}

// Actionable says whether a finding of this severity needs someone to act: all but Info.
func (s Severity) Actionable() bool { return s.Rank() <= 4 }

// Owners.
const (
	OwnerUs       = "Us"
	OwnerClientIT = "Client IT"
	OwnerVendor   = "Vendor"
)

// When a change can be made.
const (
	WhenOnline      = "Online"
	WhenWindow      = "Window"
	WhenDecideFirst = "Decide first"
	WhenNone        = "None"
)

// WhenFromChangeType maps the scan's ChangeType to When: Online, Offline -> Window,
// Planned -> Decide first, None.
func WhenFromChangeType(changeType string) string {
	switch changeType {
	case "Online":
		return WhenOnline
	case "Offline":
		return WhenWindow
	case "Planned":
		return WhenDecideFirst
	}
	return WhenNone
}

// Efforts.
const (
	EffortQuickWin = "Quick win"
	EffortModerate = "Moderate"
	EffortProject  = "Project"
	EffortNone     = "None"
)

// Finding is one row of what a check found, with the same columns for every card and the same
// words as the HTML reports.
type Finding struct {
	CheckID   string   `json:"checkId"`
	Title     string   `json:"title"`
	Severity  Severity `json:"severity"`
	Owner     string   `json:"owner"`
	When      string   `json:"when"`
	Effort    string   `json:"effort"`
	Object    string   `json:"object"`
	Finding   string   `json:"finding"`
	Why       string   `json:"why"`
	DoThis    string   `json:"doThis"`
	ThenCheck string   `json:"thenCheck"`
	Evidence  string   `json:"evidence"`
	// FixID names the fix the app can apply for this finding (docs/phase3c-contract.md); "" when
	// there is none, and then it is left out of the JSON.
	FixID string `json:"fixId,omitempty"`
	// Clock says which clock a Performance finding reads (docs/phase3e-contract.md): ClockNow,
	// ClockSinceRestart or ClockWindow; "" on the other cards, and then it is left out of the JSON.
	Clock string `json:"clock,omitempty"`
}

// Clocks of a Performance finding: right now, since the last restart, or a window (the last 7 days).
const (
	ClockNow          = "now"
	ClockSinceRestart = "since-restart"
	ClockWindow       = "window"
)

// Check statuses.
const (
	CheckPassed  = "passed"
	CheckFinding = "finding"
	CheckSkipped = "skipped"
)

// CheckStatus says what became of one check behind a card.
type CheckStatus struct {
	CheckID string `json:"checkId"`
	Title   string `json:"title"`
	Status  string `json:"status"` // passed, finding, skipped
	Note    string `json:"note"`   // why skipped; "" otherwise
}

// CardDetail is one card with everything behind it.
type CardDetail struct {
	Card     Card          `json:"card"`
	Findings []Finding     `json:"findings"` // ranked
	Checks   []CheckStatus `json:"checks"`   // in catalogue order
	// CheckdbRoom is the last stored STO10 result: written for the Storage card (null when it was
	// never run), left out for every other card (see MarshalJSON).
	CheckdbRoom *CheckdbRoom `json:"checkdbRoom,omitempty"`
	// CheckdbRun is the last stored CHECKDB run from the app (checkdb-now, docs/phase4d-contract.md):
	// written for the Backups and Storage cards (null when none ran), left out for every other card
	// (see MarshalJSON).
	CheckdbRun *CheckdbRun `json:"checkdbRun,omitempty"`
	// Fixes are the fixes available on this card right now: written for the Setup and Upkeep
	// cards ([] when none), left out for every other card (see MarshalJSON).
	Fixes []Fix `json:"fixes,omitempty"`
	// Deadlocks is the last stored deadlock report (DLK01): written for the Performance card (null
	// when it was never run), left out for every other card (see MarshalJSON).
	Deadlocks *DeadlockReport `json:"deadlocks,omitempty"`
	// Blocking is the last stored blocking history (BLK01, docs/phase5d-contract.md): written for the
	// Performance card as blockingHistory (null when it was never read), left out for every other card
	// (see MarshalJSON).
	Blocking *BlockingHistory `json:"blockingHistory,omitempty"`
	// MaintenancePlan is the Upkeep card's Maintenance plan row (docs/phase5b-contract.md): every
	// piece of regular maintenance and where it stands; nil (left out) for every other card, and when
	// the Upkeep card itself could not be read (a plan whose jobs could not be read says so in it).
	MaintenancePlan *MaintenancePlan `json:"maintenancePlan,omitempty"`
	// Trend is the Performance card's "better or worse than usual" (docs/phase5k-contract.md): written
	// for the Performance card (null when it could not be built), left out for every other card.
	Trend *PerformanceTrend `json:"trend,omitempty"`
	// Drives is the Storage card's answer first (docs/phase5l-contract.md part 5): one bar per drive;
	// nil (left out) for every other card and when the volumes could not be read.
	Drives *StorageDrives `json:"drives,omitempty"`
}

// Check is one check the app implements: which card it belongs to, its title, the question it
// answers, who acts on it and how to see that the action worked.
type Check struct {
	ID        string
	Card      CardID
	Title     string
	Question  string
	Owner     string
	ThenCheck string
}

// Checks is the catalogue of the checks the app implements, in catalogue order. Titles come
// from the scan's catalogue, questions, owners and "then check" from Checks.csv.
var Checks = []Check{
	{"BAK01", CardBackups, "A full backup exists", "Has this database ever had a full backup?", OwnerClientIT,
		"Re-run: BAK01 gone and BAK05 shows the data-loss window."},
	{"BAK02", CardBackups, "Transaction log backups", "Is the log backed up in FULL recovery?", OwnerClientIT,
		"Re-run: BAK02 gone and the log stops growing (STO07)."},
	{"BAK03", CardBackups, "Integrity check (DBCC CHECKDB)", "Has CHECKDB run in the last two weeks?", OwnerClientIT,
		"Re-run after the job runs: BAK03 gone."},
	{"BAK04", CardBackups, "Suspect (damaged) pages recorded", "Are there suspect (damaged) pages?", OwnerClientIT,
		"CHECKDB clean and the DBA cleared msdb.dbo.suspect_pages."},
	{"BAK05", CardBackups, "Worst-case data loss", "How much data could be lost in the worst case?", OwnerClientIT,
		"Re-run after a week: the window in BAK05 matches what the client agreed."},
	{"BAK06", CardBackups, "Full backups thrown away (NUL)", "Was a full backup written to NUL, breaking the differentials?", OwnerClientIT,
		"Re-run after a real full backup: BAK06 gone."},
	{"DBC12", CardBackups, "Delayed durability", "Can a committed transaction be lost in a crash?", OwnerUs,
		"Re-run: DBC12 gone."},
	{"DBC08", CardBackups, "Recovery model matches data-loss tolerance",
		"Does the recovery model match what the business can afford to lose?", OwnerClientIT,
		"BAK05 shows the agreed data-loss window."},

	{"STO01", CardStorage, "Disk free space", "Is there enough free disk space?", OwnerClientIT,
		"Re-run: free space above the threshold in STO01."},
	{"STO02", CardStorage, "Percentage autogrowth", "Is autogrowth a percentage?", OwnerUs,
		"Re-run: STO02 gone."},
	{"STO03", CardStorage, "Autogrowth too small or disabled", "Is autogrowth too small or disabled?", OwnerUs,
		"Re-run: STO03 gone. Fewer growth events in MNT05."},
	{"STO04", CardStorage, "Files close to a size limit", "Is a file close to its size limit?", OwnerUs,
		"Re-run: STO04 gone."},
	{"STO05", CardStorage, "Database files on the system drive (C:)", "Are database files on the system drive?", OwnerClientIT,
		"Re-run after the move: STO05 gone."},
	{"STO06", CardStorage, "Data and log on the same volume", "Are data and log on the same volume?", OwnerClientIT,
		"Re-run after the move: STO06 gone and log write latency in STO09 lower."},
	{"STO07", CardStorage, "Log file larger than the data", "Is the log bigger than the data?", OwnerUs,
		"Re-run after the next log backup: STO07 gone."},
	{"STO08", CardStorage, "Virtual log file (VLF) count", "Does the log have too many VLFs?", OwnerUs,
		"Re-run: VLF count in STO08 under the threshold."},
	{"STO09", CardStorage, "Storage latency", "Is storage latency acceptable?", OwnerClientIT,
		"Re-run after a business day: latency in STO09 lower."},
	{"TMP01", CardStorage, "Number of TempDB data files", "Does TempDB have enough data files?", OwnerUs,
		"Re-run after restart: TMP01 gone and PAGELATCH waits in WRK01 lower."},
	{"TMP02", CardStorage, "TempDB data files equal in size and growth", "Are TempDB files the same size and growth?", OwnerUs,
		"Re-run: TMP02 gone."},
	{"TMP03", CardStorage, "TempDB more than half full", "Is TempDB more than half full?", OwnerUs,
		"Re-run: TMP03 gone. Find the query that filled it in ACT03 or the live check."},
	{"IDX01", CardStorage, "Where the space goes", "Where does the space go?", OwnerUs,
		"Context only."},
	{"IDX07", CardStorage, "Oversized or mostly empty files", "Are files mostly empty or is the log bigger than the data?", OwnerUs,
		"Re-run: IDX07 gone."},
	{"IDX10", CardStorage, "Application tables that only grow", "Which tables grow for ever?", OwnerVendor,
		"Re-run after the cleanup: the row count stopped growing."},
	{"IDX13", CardStorage, "Identity columns running out of numbers", "Is an identity column about to run out?", OwnerUs,
		"Re-run: percent used in IDX13 reset after the change."},
	{"STO10", CardStorage, "Room to run CHECKDB", "Is there room to run DBCC CHECKDB on this database?", OwnerClientIT,
		"Run the estimate again: STO10 passed."},
	{"STO11", CardStorage, "Transaction log usage", "Is the log about to fill up?", OwnerUs,
		"Re-run: STO11 gone and the log under 50% used."},
	{"STO12", CardStorage, "Disk growth forecast", "When will the disk be full?", OwnerClientIT,
		"Re-run after a week: the forecast is longer."},

	{"INS01", CardSetup, "SQL Server version and support status",
		"Is this version supported and is the latest cumulative update installed?", OwnerClientIT,
		"Re-run the health check: INS01 shows the new build."},
	{"INS02", CardSetup, "Max server memory is configured", "Is max server memory set at all?", OwnerUs,
		"Re-run tomorrow: INS02 gone and no new paging on the host."},
	{"INS03", CardSetup, "Max server memory is sized sensibly", "Does max server memory leave Windows enough and use enough?", OwnerUs,
		"Re-run tomorrow: INS03 gone."},
	{"INS04", CardSetup, "Max degree of parallelism (MAXDOP)", "Is MAXDOP capped sensibly?", OwnerUs,
		"Re-run after a business day: parallelism waits in WRK01 lower."},
	{"INS05", CardSetup, "Cost threshold for parallelism", "Is cost threshold for parallelism still 5?", OwnerUs,
		"Re-run after a business day: CXPACKET share in WRK01 lower."},
	{"INS06", CardSetup, "Plan cache bloat (optimize for ad hoc workloads)", "Is the plan cache full of single-use plans?", OwnerUs,
		"Re-run after a day: single-use MB in INS06 lower."},
	{"INS07", CardSetup, "Instant file initialization", "Is instant file initialization on?", OwnerClientIT,
		"Re-run: INS07 gone (needs a SQL Server restart to take effect)."},
	{"INS08", CardSetup, "Lock pages in memory", "Is lock pages in memory granted where it should be?", OwnerClientIT,
		"Re-run after restart: INS08 gone."},
	{"INS09", CardSetup, "Priority boost is off", "Is priority boost off?", OwnerUs,
		"Re-run after restart: INS09 gone."},
	{"INS10", CardSetup, "Backup compression default", "Is backup compression the default?", OwnerUs,
		"Next backup is smaller in msdb.dbo.backupset (compressed_backup_size)."},
	{"INS11", CardSetup, "Server-wide index fill factor", "Is the server-wide fill factor at the default?", OwnerUs,
		"Re-run: INS11 gone."},
	{"INS12", CardSetup, "Windows low on memory", "Has Windows signalled low physical memory recently?", OwnerClientIT,
		"Re-run after a day: no new low-memory signal in INS12."},
	{"INS13", CardSetup, "Windows power plan", "Is the Windows power plan High Performance?", OwnerClientIT,
		"Re-run: INS13 gone. CPU-bound queries should be measurably faster."},
	{"INS14", CardSetup, "Server settings waiting for RECONFIGURE", "Is a changed server setting waiting to take effect?", OwnerUs,
		"Re-run: INS14 gone."},
	{"INS15", CardSetup, "Global trace flags", "Which trace flags are on, and should they be?", OwnerUs,
		"Re-run after a restart: the same flags in INS15 (or none)."},
	{"INS16", CardSetup, "Min server memory, lightweight pooling, affinity", "Are rarely-needed server options set?", OwnerUs,
		"Re-run: INS16 gone."},
	{"DBC02", CardSetup, "AUTO_CLOSE is off", "Is AUTO_CLOSE off?", OwnerUs, "Re-run: DBC02 gone."},
	{"DBC03", CardSetup, "AUTO_SHRINK is off", "Is AUTO_SHRINK off?", OwnerUs, "Re-run: DBC03 gone."},
	{"DBC04", CardSetup, "Page verify uses CHECKSUM", "Is page verify CHECKSUM?", OwnerUs, "Re-run: DBC04 gone."},
	{"DBC05", CardSetup, "Automatic statistics are on", "Are automatic statistics on?", OwnerUs, "Re-run: DBC05 gone."},
	{"DBC06", CardSetup, "Compatibility level", "Is the compatibility level current?", OwnerVendor,
		"Re-run after the change and a business day: no regression in the Tuning Report."},
	{"DBC07", CardSetup, "Database owner is a valid login", "Does the database owner exist as a login?", OwnerUs,
		"Re-run: DBC07 gone."},
	{"DBC09", CardSetup, "Indirect checkpoints (target recovery time)", "Are indirect checkpoints in use?", OwnerUs,
		"Re-run: DBC09 gone."},
	{"DBC10", CardSetup, "Query Store is on and recording", "Is Query Store on and recording?", OwnerUs,
		"Re-run: DBC10 gone. The Tuning Report can run after a few days."},
	{"DBC11", CardSetup, "TRUSTWORTHY is off", "Is TRUSTWORTHY off?", OwnerUs,
		"Re-run: DBC11 gone and CLR code still runs."},
	{"DBC13", CardSetup, "Database open to everyone (MULTI_USER)", "Can the application connect?", OwnerUs,
		"Re-run: DBC13 gone and the application connects."},
	{"DBC14", CardSetup, "Database scoped configurations", "Does this database override the server's settings?", OwnerUs,
		"Context: INS04 and CFG01 read the server; DBC14 says what this database changes."},
	{"CFG01", CardSetup, "Legacy cardinality estimator forced", "Are there options that slow the database down?", OwnerUs,
		"Re-run: CFG01 gone."},
	{"CPY01", CardSetup, "Where this database came from", "Where was this database restored from?", OwnerUs,
		"Context only."},
	{"CPY02", CardSetup, "Collation matches the server", "Does the collation match the server?", OwnerVendor,
		"Decide first: collation is a project."},
	{"CPY04", CardSetup, "Users without a matching login (orphaned)", "Are there users without a login?", OwnerUs,
		"Re-run: CPY04 gone."},
	{"CPY06", CardSetup, "CLR code can run", "Can the CLR code run?", OwnerUs, "A procedure that uses the CLR code runs without error."},

	{"MNT01", CardUpkeep, "SQL Server Agent is running", "Is SQL Server Agent running and automatic?", OwnerClientIT,
		"Re-run: MNT01 gone."},
	{"MNT02", CardUpkeep, "Failed Agent jobs (last 7 days)", "Did any Agent job fail in the last 7 days?", OwnerUs,
		"Re-run after 7 days: MNT02 gone."},
	{"MNT03", CardUpkeep, "Index / statistics maintenance job exists", "Is there an index and statistics maintenance job?", OwnerUs,
		"Re-run after the first run: MNT03 gone and MNT04 empty."},
	{"MNT04", CardUpkeep, "Out-of-date statistics", "Are statistics out of date on big tables?", OwnerUs,
		"Re-run after the job: MNT04 empty."},
	{"MNT05", CardUpkeep, "Shrinks, cache clears and file growth events (last 7 days)", "Did a shrink or a cache clear run recently?", OwnerUs,
		"Re-run after 7 days: MNT05 gone. STO08 and WRK01 should not get worse again."},
	{"MNT06", CardUpkeep, "Crashes (memory dumps) and severe errors",
		"Has SQL Server crashed (memory dump) or logged severe errors recently?", OwnerClientIT,
		"Re-run after 7 days: MNT06 gone."},
	{"MNT07", CardUpkeep, "Alerts and an operator for corruption and severe errors",
		"Will anyone be told about corruption or a severe error?", OwnerClientIT,
		"RAISERROR('Alert test' 18 1) WITH LOG reaches the operator's mailbox."},
	{"MNT08", CardUpkeep, "msdb history kept for ever", "Does anything prune the backup and job history?", OwnerUs,
		"Re-run after the first cleanup: MNT08 gone."},
	// Phase 5f (docs/phase5f-contract.md): the default trace that feeds the change log.
	{"CHG01", CardUpkeep, "Changes made outside kakapo are seen", "Can kakapo see the changes made outside it, for a week back?", OwnerUs,
		"Re-run: CHG01 gone, and the line under Recent changes names the trace's days."},

	{"PRS01", CardPerformance, "Pressure right now", "Is the server short of threads, CPU or memory right now?", OwnerUs,
		"Re-run: PRS01 gone"},
	{"PRS02", CardPerformance, "Queries that gave up waiting for memory", "Did queries fail waiting for memory since the restart?", OwnerUs,
		"Re-run after a business day: no new timeouts in PRS02."},
	{"ACT01", CardPerformance, "Blocking right now", "Who is blocking right now?", OwnerUs,
		"Blocking gone and IDX11 shows which table it was on."},
	{"ACT02", CardPerformance, "Transactions left open", "Which transactions are left open?", OwnerVendor,
		"Re-run: ACT02 gone."},
	{"WRK01", CardPerformance, "Top wait types", "What does the server wait on most?", OwnerUs,
		"Re-run after a business day: the top wait's share is lower."},
	{"WRK02", CardPerformance, "CPU pressure (signal waits)", "Is the server short of CPU?", OwnerClientIT,
		"Re-run after a business day: signal wait share in WRK02 lower."},
	{"WRK03", CardPerformance, "Missing index suggestions", "Which missing indexes would help most?", OwnerUs,
		"Never create a suggested index as it is: check the table's existing indexes first."},
	{"WRK05", CardPerformance, "Poison waits since restart", "Have any poison waits occurred since restart?", OwnerUs,
		"Re-run after a business day: no new poison wait seconds in WRK05."},
	{"WRK06", CardPerformance, "Deadlocks", "How often does the server deadlock?", OwnerUs,
		"Re-run after 7 days: deadlocks per day in WRK06 lower."},
	{"WRK08", CardPerformance, "CPU over the last four hours", "Who used the CPU: SQL Server or something else on the box?", OwnerClientIT,
		"Re-run after a business day: other processes under 25% in WRK08."},
	{"SRV04", CardPerformance, "Which databases use the server", "Is this database the busy one on the server?", OwnerUs,
		"Context: if another database is busier, choose that database and check it too."},
	{"SRV07", CardPerformance, "Plans compiled per batch", "Does the server compile a plan for too many batches?", OwnerVendor,
		"Re-run after a day: compile share in SRV07 lower once the code uses parameters."},
	{"QRY01", CardPerformance, "Costliest queries", "Which queries in this database cost the most, and why?", OwnerUs,
		"Re-run after a business day: the query's share is lower"},
	{"DLK01", CardPerformance, "Deadlock report", "Which procedures deadlock with each other?", OwnerUs,
		"Re-run after a week: fewer deadlocks for the top pair"},
	{"BLK01", CardPerformance, "Blocking history", "What blocked users this week, and how often?", OwnerUs,
		"Re-run after a week: fewer occurrences for the top blocker"},
}

// CheckByID returns a check of the catalogue by its id.
func CheckByID(id string) (Check, bool) {
	for _, c := range Checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// ChecksFor returns the checks behind one card, in catalogue order.
func ChecksFor(card CardID) []Check {
	out := []Check{}
	for _, c := range Checks {
		if c.Card == card {
			out = append(out, c)
		}
	}
	return out
}

var effortOrder = map[string]int{EffortQuickWin: 0, EffortModerate: 1, EffortProject: 2, EffortNone: 3}
var whenOrder = map[string]int{WhenOnline: 0, WhenWindow: 1, WhenDecideFirst: 2, WhenNone: 3}

// RankFindings orders findings as every card shows them: severity, then effort (quick win
// first), then when (Online first), then check id.
func RankFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() < b.Severity.Rank()
		}
		if effortOrder[a.Effort] != effortOrder[b.Effort] {
			return effortOrder[a.Effort] < effortOrder[b.Effort]
		}
		if whenOrder[a.When] != whenOrder[b.When] {
			return whenOrder[a.When] < whenOrder[b.When]
		}
		return a.CheckID < b.CheckID
	})
}
