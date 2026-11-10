package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The maintenance plan (docs/phase5b-contract.md): every piece of regular maintenance a database
// needs, on one row of the Upkeep card, each line with where it stands (kakapo's job, another job,
// missing ...) and the fix that creates it; and the composite fix that creates the missing pieces in
// one go, through the pieces' own fixes, with times that do not overlap.

// Pieces of the plan, in the order the row lists them.
const (
	PieceFullBackup = "full-backup"
	PieceLogBackup  = "log-backup"
	PieceIntegrity  = "integrity-check"
	PieceStatistics = "statistics"
	PieceIndexes    = "indexes"
	PieceCleanup    = "history-cleanup"
	PieceAlerts     = "alerts"
	PieceArchive    = "archive"
)

// Where a piece stands.
const (
	PlanInPlace   = "in-place"   // kakapo's job, enabled, last run succeeded
	PlanOtherJob  = "other-job"  // another enabled job covers it and last succeeded
	PlanMissing   = "missing"    // nothing covers it
	PlanDisabled  = "disabled"   // the job that would cover it is disabled, or has no enabled schedule
	PlanFailing   = "failing"    // the job that covers it failed (or was cancelled) last time
	PlanNeverRan  = "never-ran"  // the job exists and is enabled but has no history yet
	PlanNotNeeded = "not-needed" // the log backup in SIMPLE recovery
	PlanOptional  = "optional"   // index maintenance: offered, not counted as missing
	PlanUnknown   = "unknown"    // what it stands on could not be read
	PlanOverdue   = "overdue"    // kakapo's procedure (Express): its time passed and the log has no run since
)

// The default times of the plan (SCOPE rule 5): Sunday runs the integrity check, then index work
// when on, then the cleanup; the daily full backup follows them in the evening; statistics run on
// the other nights. Source: Ola Hallengren, FAQ, "perform the full backup after the integrity
// check" (and after index maintenance).
const (
	DefaultPlanDay         = "Sunday"
	DefaultPlanCheckTime   = "01:00"
	DefaultPlanIndexTime   = "03:00"
	DefaultPlanCleanupTime = "05:00"
	DefaultPlanStatsTime   = "01:00"
)

// planPieceDef is what the plan says about a piece before anything is read: its title, the cadence
// kakapo proposes and why, with the source (rule 2).
type planPieceDef struct {
	ID, Title, Cadence, Source string
}

var planPieceDefs = []planPieceDef{
	{PieceFullBackup, "Full backup", "daily 22:00",
		"A full backup every evening, after the day's work and after the weekly maintenance. Ola Hallengren runs a weekly full backup with daily differentials; kakapo keeps a daily full because the sites it serves are small enough and a differential chain is one more thing to get wrong. Source: Ola Hallengren, SQL Server Maintenance Solution FAQ (\"Full backup: one day per week; differential: all other days\")."},
	{PieceLogBackup, "Log backup", "every 15 minutes, FULL recovery only",
		"The log backup empties the transaction log (the file of every change since the last backup) and sets how much work a failure can lose. Ola Hallengren's FAQ backs the log up every hour; 15 minutes is the data-loss window the Backups card recommends (BAK05). Not needed in SIMPLE recovery, where the log empties itself. Source: Ola Hallengren, FAQ (\"transaction log backup: every hour\"); kakapo BAK05."},
	{PieceIntegrity, "Integrity check", "weekly, Sunday 01:00; physical only from 200 GB",
		"DBCC CHECKDB (the check that finds corruption) at least once a week, so damage is found while a clean backup still exists. Source: Paul Randal, as reported in Microsoft's CSS SQL Server Engineers blog by Cindy Gross (\"some consistency check at least once a week\"); Ola Hallengren, FAQ (\"integrity check: one day per week\"). From 200 GB of data the check is WITH PHYSICAL_ONLY (pages and their checksums only): Microsoft, \"CHECKDB part 6: consistency checking options for a VLDB\"."},
	{PieceStatistics, "Statistics update", "nightly 01:00, Monday to Saturday",
		"Statistics (the row counts the optimizer plans queries with) updated every night the integrity check does not run. Source: Microsoft, \"Maintain indexes optimally\" (the benefit of an index rebuild \"can often be achieved at a much lower resource cost by updating statistics\"); Brent Ozar, \"Stop worrying about index fragmentation\" (update statistics daily); Erik Darling's nightly statistics-only job with @StatisticsModificationLevel = 5 (DarlingData, \"Ola Stats Only Job\")."},
	{PieceIndexes, "Index maintenance", "weekly, Sunday 03:00, off by default",
		"Rebuilding or reorganising fragmented indexes. Microsoft (2026): do not assume that index maintenance always noticeably improves the workload, or that it must run on a fixed schedule; reorganise is preferred over rebuild (\"Maintain indexes optimally\"). Offered with Ola Hallengren's thresholds (reorganise from 30% fragmentation, rebuild from 50%, tables of 1,000 pages or more) when his IndexOptimize is installed; the plan does not count it as missing."},
	{PieceCleanup, "History cleanup", "weekly, Sunday 05:00, keep 30 days",
		"msdb (SQL Server's own database of jobs and backups) grows with every backup and every job run. Source: Microsoft, \"sp_delete_backuphistory\" (\"we recommend that you periodically execute sp_delete_backuphistory\"); Ola Hallengren's installation prunes the backup history, the job history, its CommandLog and its output files weekly, keeping 30 days."},
	{PieceAlerts, "Alerts and an operator", "once",
		"An operator (a mailbox SQL Server Agent can mail) and alerts for severity 19 to 25 and errors 823, 824 and 825 (corruption), so damage and fatal errors are reported when they happen, not when users complain. Source: DarlingData, Alerts.sql; kakapo MNT07."},
	{PieceArchive, "Archive", "nightly 02:30 per table",
		"Tables that only grow have their old rows moved (or deleted) every night in small batches. Set up from the Storage card's Tables row, never from the plan (docs/phase5a-contract.md)."},
}

func planPieceDefFor(id string) planPieceDef {
	for _, d := range planPieceDefs {
		if d.ID == id {
			return d
		}
	}
	return planPieceDef{ID: id, Title: id}
}

// planOrder is the ordering sentence, with its source, for the row's "Why these times" fold.
const planOrder = "Ola Hallengren's FAQ says to \"perform the full backup after the integrity check\", and after index maintenance. So on Sunday the integrity check runs at 01:00, index work (when on) at 03:00 and the history cleanup at 05:00; the daily full backup at 22:00 follows them; statistics run on the other nights at 01:00; log backups run through everything. The times are defaults: change them under More options when you set the plan up."

// --- what the plan read returns ---

// PlanSchedule is one schedule of a job as msdb.dbo.sysschedules holds it.
type PlanSchedule struct {
	FreqType       int // 1 once, 4 daily, 8 weekly, 16 monthly, 32 monthly relative, 64 Agent start, 128 idle
	FreqInterval   int // daily: every n days; weekly: the day bits (Sunday 1 .. Saturday 64)
	SubdayType     int // 1 at a time, 2 seconds, 4 minutes, 8 hours
	SubdayInterval int
	StartTime      int // HHMMSS
	Enabled        bool
}

// PlanJobStep is one step of a job: its command and the database it runs in.
type PlanJobStep struct {
	Command  string
	Database string
}

// Job outcomes (sysjobhistory run_status of the job's outcome row, step 0); PlanNoRun when it never ran.
const (
	PlanRunFailed    = 0
	PlanRunSucceeded = 1
	PlanRunRetry     = 2
	PlanRunCancelled = 3
	PlanRunRunning   = 4
	PlanNoRun        = -1
)

// PlanJob is one Agent job the plan read found: kakapo's by name, or one whose steps do maintenance.
type PlanJob struct {
	Name        string
	Enabled     bool
	Steps       []PlanJobStep
	Schedules   []PlanSchedule
	NextRun     *time.Time // sysjobschedules next_run_date/time, server clock; nil when Agent has not set one
	LastRun     *time.Time // when the last run started, server clock; nil when it never ran
	LastStatus  int        // PlanRunFailed .. PlanRunRunning, or PlanNoRun
	LastSeconds int        // how long the last run took
}

// MaintenancePlanFacts is what collect.MaintenancePlan reads, in one batch, each area in its own TRY.
type MaintenancePlanFacts struct {
	ServerName     string
	ProductVersion string
	EngineEdition  int // 4 is Express: no SQL Server Agent
	Database       string
	RecoveryModel  string // FULL, BULK_LOGGED, SIMPLE
	// LastGoodCheckDb is DATABASEPROPERTYEX(db, 'LastGoodCheckDbTime'): nil when this version does not
	// know it, 1900-01-01 when it never ran (BAK03 offers the integrity check job from it).
	LastGoodCheckDb *time.Time
	CheckError      string
	IndexOptimize   bool // master.dbo.IndexOptimize and master.dbo.CommandExecute exist
	Jobs            []PlanJob
	JobsError       string
	ServerNow       time.Time
	// Proc is kakapo's maintenance procedure as read (phase 5p, Express only); nil when not read.
	Proc *MaintProcFacts
}

// PlanAlerts is the operator and alerts state the plan's alerts line shows (from the Upkeep facts,
// or the alert facts operator-alerts reads).
type PlanAlerts struct {
	Operators int      // enabled operators with an email address
	Missing   []string // the severity 19-25 / error 823-825 alerts missing
	Silent    []string // the alerts that mail no one
	Error     string   // could not be read
}

// PlanAlerts is the alerts state of the Upkeep facts.
func (f UpkeepFacts) PlanAlerts() PlanAlerts {
	return PlanAlerts{Operators: f.Operators, Missing: f.MissingAlerts, Silent: f.SilentAlerts, Error: f.AlertsError}
}

// PlanAlerts is the alerts state of the alert facts.
func (f AlertFacts) PlanAlerts() PlanAlerts {
	missing := []string{}
	for _, s := range f.missingAlerts() {
		missing = append(missing, s.Label)
	}
	return PlanAlerts{Operators: len(f.mailOperators()), Missing: missing, Silent: f.SilentAlerts()}
}

// --- the plan ---

// MaintenancePlan is the Upkeep card's Maintenance plan row (CardDetail.MaintenancePlan).
type MaintenancePlan struct {
	Pieces []PlanPiece `json:"pieces"`
	// Missing is how many pieces the composite fix can create (missing, or covered only by a
	// disabled job of someone else's); index maintenance is optional and not counted.
	Missing int `json:"missing"`
	// FixID is the composite fix ("maintenance-plan") when it has something to create; "" otherwise
	// (nothing missing, Express, or msdb could not be read).
	FixID   string `json:"fixId,omitempty"`
	Express bool   `json:"express,omitempty"`
	Order   string `json:"order"`          // why these times, with the source
	Note    string `json:"note,omitempty"` // what could not be read, or why nothing can be created
}

// PlanPiece is one line of the plan.
type PlanPiece struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Cadence      string            `json:"cadence"`
	Status       string            `json:"status"`
	Job          string            `json:"job,omitempty"`
	Schedule     string            `json:"schedule,omitempty"`
	LastRun      string            `json:"lastRun,omitempty"`     // "2026-11-02 01:00", server time
	LastOutcome  string            `json:"lastOutcome,omitempty"` // "succeeded", "failed", ...
	LastDuration string            `json:"lastDuration,omitempty"`
	NextRun      string            `json:"nextRun,omitempty"`
	FixID        string            `json:"fixId,omitempty"`
	Options      map[string]string `json:"options,omitempty"`
	Source       string            `json:"source"`
	Note         string            `json:"note,omitempty"`
}

// --- reading jobs: schedules, outcomes and what a step does ---

var planDays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Words is a schedule in words: "daily 22:00", "every 15 minutes", "Sunday 01:00", "Monday to
// Saturday 01:00", "monthly on day 1 at 03:00".
func (s PlanSchedule) Words() string {
	at := AgentClock(s.StartTime)
	when := at
	switch s.SubdayType {
	case 2:
		when = fmt.Sprintf("every %d seconds", s.SubdayInterval)
	case 4:
		when = fmt.Sprintf("every %d minutes", s.SubdayInterval)
	case 8:
		when = fmt.Sprintf("every %d hours", s.SubdayInterval)
	}
	sub := s.SubdayType == 2 || s.SubdayType == 4 || s.SubdayType == 8
	switch s.FreqType {
	case 1:
		return "once at " + at
	case 4:
		switch {
		case s.FreqInterval > 1:
			return fmt.Sprintf("every %d days, %s", s.FreqInterval, when)
		case sub:
			return when
		}
		return "daily " + when
	case 8:
		days := s.FreqInterval & jobAllWeekday
		if days == jobAllWeekday && sub {
			return when
		}
		return KakapoJobSchedule{Freq: JobWeekly, Days: days, Clock: when}.words()
	case 16:
		return fmt.Sprintf("monthly on day %d at %s", s.FreqInterval, at)
	case 32:
		return "monthly at " + at
	case 64:
		return "when SQL Server Agent starts"
	case 128:
		return "when the server is idle"
	}
	return "on a schedule kakapo does not read"
}

// schedule is the job's schedule in words: the first enabled one, else the first with
// "(schedule disabled)", else "no schedule"; and whether one is enabled.
func (j PlanJob) schedule() (string, bool) {
	for _, s := range j.Schedules {
		if s.Enabled {
			return s.Words(), true
		}
	}
	if len(j.Schedules) > 0 {
		return j.Schedules[0].Words() + " (schedule disabled)", false
	}
	return "no schedule", false
}

// outcomeWords is a run_status in words.
func outcomeWords(status int) string {
	switch status {
	case PlanRunFailed:
		return "failed"
	case PlanRunSucceeded:
		return "succeeded"
	case PlanRunRetry:
		return "retrying"
	case PlanRunCancelled:
		return "cancelled"
	case PlanRunRunning:
		return "running"
	}
	return "never ran"
}

// status is where a job leaves the piece it covers: disabled (the job, or every schedule), failing
// (failed or cancelled last time), never ran, else in place.
func (j PlanJob) status() string {
	_, scheduled := j.schedule()
	switch {
	case !j.Enabled || !scheduled:
		return PlanDisabled
	case j.LastStatus == PlanRunFailed || j.LastStatus == PlanRunCancelled:
		return PlanFailing
	case j.LastStatus == PlanNoRun || j.LastRun == nil:
		return PlanNeverRan
	}
	return PlanInPlace
}

var (
	olaFullRe        = regexp.MustCompile(`(?i)@BackupType\s*=\s*N?'FULL'`)
	olaLogRe         = regexp.MustCompile(`(?i)@BackupType\s*=\s*N?'LOG'`)
	olaNoMediumRe    = regexp.MustCompile(`(?i)@FragmentationMedium\s*=\s*NULL`)
	olaNoHighRe      = regexp.MustCompile(`(?i)@FragmentationHigh\s*=\s*NULL`)
	identifierCharRe = regexp.MustCompile(`[0-9A-Za-z_@#$]`)
)

// namesDatabase says whether a step works on the database, as the single fixes' reads decide it:
// the command names it ([db], 'db', or the bare name as a word) or every user database
// (USER_DATABASES, ALL_DATABASES), or the step runs in it.
func (s PlanJobStep) namesDatabase(db string) bool {
	if db == "" {
		return false
	}
	up, name := strings.ToUpper(s.Command), strings.ToUpper(db)
	if strings.Contains(up, "USER_DATABASES") || strings.Contains(up, "ALL_DATABASES") ||
		strings.Contains(up, strings.ToUpper(QuoteName(db))) || strings.Contains(up, "'"+name+"'") ||
		strings.EqualFold(s.Database, db) {
		return true
	}
	for i := 0; ; {
		k := strings.Index(up[i:], name)
		if k < 0 {
			return false
		}
		at := i + k
		end := at + len(name)
		before := at == 0 || !identifierCharRe.MatchString(up[at-1:at])
		after := end >= len(up) || !identifierCharRe.MatchString(up[end:end+1])
		if before && after {
			return true
		}
		i = at + 1
	}
}

// covers says whether a step does a piece's work for the database (the cleanup is server-wide).
func (s PlanJobStep) covers(piece, db string) bool {
	up := strings.ToUpper(s.Command)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(up, w) {
				return true
			}
		}
		return false
	}
	everyDB := has("SP_MSFOREACHDB")
	switch piece {
	case PieceFullBackup:
		return (has("BACKUP DATABASE") || (has("DATABASEBACKUP") && olaFullRe.MatchString(s.Command))) && s.namesDatabase(db)
	case PieceLogBackup:
		return (has("BACKUP LOG") || (has("DATABASEBACKUP") && olaLogRe.MatchString(s.Command))) && s.namesDatabase(db)
	case PieceIntegrity:
		return (has("CHECKDB") && (s.namesDatabase(db) || everyDB)) || (has("DATABASEINTEGRITYCHECK") && s.namesDatabase(db))
	case PieceStatistics:
		return (has("SP_UPDATESTATS", "UPDATE STATISTICS") || (has("INDEXOPTIMIZE") && has("@UPDATESTATISTICS"))) &&
			(s.namesDatabase(db) || everyDB)
	case PieceIndexes:
		olaIndex := has("INDEXOPTIMIZE") && !(olaNoMediumRe.MatchString(s.Command) && olaNoHighRe.MatchString(s.Command))
		return (olaIndex || has("ALTER INDEX", "DBREINDEX")) && (s.namesDatabase(db) || everyDB)
	case PieceCleanup:
		return has("SP_DELETE_BACKUPHISTORY", "SP_PURGE_JOBHISTORY", "COMMANDLOG")
	}
	return false
}

func (j PlanJob) covers(piece, db string) bool {
	// kakapo's own jobs are matched by name, never by what their steps contain: an archive job's
	// tidy-up step runs UPDATE STATISTICS and ALTER INDEX for its one table and covers neither
	// piece (seen in the sandbox).
	if strings.HasPrefix(j.Name, "kakapo: ") {
		return false
	}
	for _, s := range j.Steps {
		if s.covers(piece, db) {
			return true
		}
	}
	return false
}

// kakapoJobFor is the name of the job kakapo creates for a piece.
func kakapoJobFor(piece, db string) string {
	switch piece {
	case PieceFullBackup:
		return BackupJobName(db, JobFull)
	case PieceLogBackup:
		return BackupJobName(db, JobLog)
	case PieceIntegrity:
		return CheckdbJobName(db)
	case PieceStatistics:
		return StatisticsJobName(db)
	case PieceIndexes:
		return IndexJobName(db)
	case PieceCleanup:
		return HistoryCleanupJobName
	}
	return ""
}

func (f MaintenancePlanFacts) job(name string) (PlanJob, bool) {
	for _, j := range f.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return PlanJob{}, false
}

// statusRank orders the jobs that cover a piece: the one that shows it best in place first.
var statusRank = map[string]int{PlanInPlace: 0, PlanNeverRan: 1, PlanFailing: 2, PlanDisabled: 3}

// coverage is where a piece stands on the jobs: kakapo's own job when it exists, else the best of
// the other jobs that cover it, else missing. own says the job is kakapo's.
func (f MaintenancePlanFacts) coverage(piece string) (status string, job *PlanJob, own bool) {
	if j, ok := f.job(kakapoJobFor(piece, f.Database)); ok {
		return j.status(), &j, true
	}
	var best *PlanJob
	for i := range f.Jobs {
		j := f.Jobs[i]
		if !j.covers(piece, f.Database) {
			continue
		}
		if best == nil || statusRank[j.status()] < statusRank[best.status()] {
			best = &f.Jobs[i]
		}
	}
	if best == nil {
		return PlanMissing, nil, false
	}
	st := best.status()
	if st == PlanInPlace {
		st = PlanOtherJob
	}
	return st, best, false
}

// fill sets the job, schedule and runs of a piece from its job.
func (p *PlanPiece) fill(j PlanJob) {
	p.Job = j.Name
	p.Schedule, _ = j.schedule()
	if j.LastRun != nil {
		p.LastRun = sqlTime(*j.LastRun)
		p.LastOutcome = outcomeWords(j.LastStatus)
		p.LastDuration = durationWords(float64(j.LastSeconds))
	}
	if j.NextRun != nil {
		p.NextRun = sqlTime(*j.NextRun)
	}
}

// --- building the plan ---

// BuildMaintenancePlan is the plan of one database from what was read (docs/phase5b-contract.md):
// one line per piece with its status, its job, schedule and runs, the fix that creates it where one
// can (with the options that make it fit the plan's times) and its source; the composite fix when
// anything can be created.
func BuildMaintenancePlan(f MaintenancePlanFacts, a PlanAlerts) MaintenancePlan {
	plan := MaintenancePlan{Pieces: []PlanPiece{}, Order: planOrder, Express: f.EngineEdition == 4}
	db := f.Database
	piece := func(id string) PlanPiece {
		d := planPieceDefFor(id)
		return PlanPiece{ID: id, Title: d.Title, Cadence: d.Cadence, Source: d.Source}
	}
	_, ownFull := f.job(BackupJobName(db, JobFull))
	_, ownLog := f.job(BackupJobName(db, JobLog))
	simple := strings.EqualFold(f.RecoveryModel, "SIMPLE")
	olaIndex := f.IndexOptimize && OlaDatabaseNameOK(db)

	for _, id := range []string{PieceFullBackup, PieceLogBackup, PieceIntegrity, PieceStatistics, PieceIndexes, PieceCleanup} {
		p := piece(id)
		switch {
		case id == PieceLogBackup && simple:
			p.Status, p.Note = PlanNotNeeded, "The database is in SIMPLE recovery: the log empties itself and there is no log to back up (DBC08 decides the model)."
			plan.Pieces = append(plan.Pieces, p)
			continue
		case plan.Express:
			// Phase 5p: the plan is kakapo's procedure, which reads its last runs from its own log.
			if row, ok := expressRow(f); ok {
				maintPlanPiece(&p, *f.Proc, row, f.ServerNow)
			} else {
				p.Status, p.Note = PlanMissing, expressPieceNote(f)
				if id == PieceIndexes {
					p.Status = PlanOptional
				}
			}
			plan.Pieces = append(plan.Pieces, p)
			continue
		case f.JobsError != "":
			p.Status, p.Note = PlanUnknown, "The Agent jobs in msdb could not be read: "+f.JobsError
			plan.Pieces = append(plan.Pieces, p)
			continue
		default:
			st, j, own := f.coverage(id)
			p.Status = st
			if j != nil {
				p.fill(*j)
				switch {
				case own && st == PlanDisabled:
					p.Note = "kakapo's job is disabled: enable it, or its schedule, in SSMS (SQL Server Agent > Jobs)."
				case !own && st == PlanDisabled:
					p.Note = "The only job that does this is disabled."
				case st == PlanFailing:
					p.Note = "The job failed last time: its history says why (MNT02)."
				case st == PlanNeverRan:
					p.Note = "The job has not run yet."
				}
			}
			if id == PieceIndexes && j == nil {
				p.Status = PlanOptional
				if !olaIndex {
					p.Note = "kakapo offers index maintenance only with Ola Hallengren's IndexOptimize installed in master (https://ola.hallengren.com); statistics matter more."
				}
			}
		}
		// Only a missing piece, or one only someone else's disabled job does, can be created.
		_, _, own := f.coverage(id)
		creatable := p.Status == PlanMissing || p.Status == PlanOptional || (p.Status == PlanDisabled && !own)
		if creatable {
			p.FixID, p.Options = planPieceFix(id, f, ownFull || ownLog, olaIndex)
			if p.FixID == "" && p.Note == "" {
				p.Note = planPieceNoFix(id, f, ownFull || ownLog)
			}
		}
		plan.Pieces = append(plan.Pieces, p)
	}

	alerts := piece(PieceAlerts)
	switch {
	case plan.Express:
		alerts.Status, alerts.Note = PlanNotNeeded, "SQL Server Express has no SQL Server Agent, so it has no alerts or operators."
	case a.Error != "":
		alerts.Status, alerts.Note = PlanUnknown, "The operators and alerts in msdb could not be read: "+a.Error
	case a.Operators > 0 && len(a.Missing) == 0 && len(a.Silent) == 0:
		alerts.Status, alerts.Note = PlanInPlace, fmt.Sprintf("%d operator(s) with an email address; every alert mails one.", a.Operators)
	default:
		alerts.Status = PlanMissing
		switch {
		case a.Operators == 0:
			alerts.Note = "No operator with an email address: no alert or job failure reaches anyone."
		case len(a.Missing) > 0:
			alerts.Note = "No alert for: " + strings.Join(a.Missing, ", ") + "."
		default:
			alerts.Note = "Alerts that mail no one: " + strings.Join(a.Silent, ", ") + "."
		}
		alerts.FixID, alerts.Options = FixOperatorAlerts, map[string]string{OptionCreateAlerts: "true"}
	}
	plan.Pieces = append(plan.Pieces, alerts)

	// kakapo's archive jobs on this database: listed, never created from the plan.
	prefix := archiveJobPrefix + db + "."
	for _, j := range f.Jobs {
		if !strings.HasPrefix(j.Name, prefix) {
			continue
		}
		p := piece(PieceArchive)
		p.Title = "Archive " + strings.TrimPrefix(j.Name, prefix)
		p.Status = j.status()
		p.fill(j)
		plan.Pieces = append(plan.Pieces, p)
	}

	for _, p := range plan.Pieces {
		if (p.FixID != "" || plan.Express) && p.ID != PieceIndexes && (p.Status == PlanMissing || p.Status == PlanDisabled) {
			plan.Missing++
		}
	}
	switch {
	case plan.Express:
		plan.Note, plan.FixID = expressPlanNote(f)
	case f.JobsError != "":
		plan.Note = "The Agent jobs in msdb could not be read, so the plan is not known: " + f.JobsError
	case plan.Missing > 0:
		plan.FixID = FixMaintenancePlan
	}
	return plan
}

// planPieceFix is the fix that creates a piece and the options that make it fit the plan's times:
// "" when no fix of kakapo's can.
func planPieceFix(id string, f MaintenancePlanFacts, ownBackup, olaIndex bool) (string, map[string]string) {
	switch id {
	case PieceFullBackup, PieceLogBackup:
		// The backup schedule creates both jobs, and only while neither of kakapo's exists.
		if !ownBackup {
			return FixBackupSchedule, nil
		}
	case PieceIntegrity:
		// A recent check by hand is not a schedule: the weekly job is offered whenever no job
		// covers the database.
		return FixCheckdbJob, nil
	case PieceStatistics:
		return FixMaintenanceJob, map[string]string{OptionStatsOnly: "true", OptionMaintDay: DefaultPlanDay}
	case PieceIndexes:
		if olaIndex {
			return FixMaintenanceJob, map[string]string{OptionStatsOnly: "false", OptionMaintIndexOnly: "true",
				OptionMaintDay: DefaultPlanDay, OptionMaintIndexTime: DefaultPlanIndexTime}
		}
	case PieceCleanup:
		return FixHistoryCleanup, nil
	}
	return "", nil
}

// planPieceNoFix says why a missing piece has no Create.
func planPieceNoFix(id string, f MaintenancePlanFacts, ownBackup bool) string {
	switch id {
	case PieceFullBackup, PieceLogBackup:
		if ownBackup {
			return "kakapo's backup schedule exists without this job: remove it on the Backups card and create it again."
		}
	}
	return ""
}

// MaintenancePlanOffer is the composite fix as the Upkeep card lists it: when the plan has a piece
// to create.
func MaintenancePlanOffer(plan *MaintenancePlan) (Fix, bool) {
	if plan == nil || plan.FixID == "" {
		return Fix{}, false
	}
	if plan.FixID == FixInstallMaintenanceProc {
		fix := suFix(FixInstallMaintenanceProc)
		fix.Current = "nothing schedules the maintenance (SQL Server Express has no SQL Server Agent)"
		fix.Target = "master.dbo.kakapo_Maintenance, called every 15 minutes by a scheduled task"
		return fix, true
	}
	fix := suFix(FixMaintenancePlan)
	titles := []string{}
	for _, p := range plan.Pieces {
		if p.FixID != "" && p.ID != PieceIndexes && (p.Status == PlanMissing || p.Status == PlanDisabled) {
			titles = append(titles, strings.ToLower(p.Title))
		}
	}
	fix.Current = fmt.Sprintf("%d piece(s) missing: %s", plan.Missing, strings.Join(titles, ", "))
	fix.Target = "every piece scheduled, times that do not overlap"
	return fix, true
}

// piece is a line of the plan by id; ok false when the plan has none.
func (plan MaintenancePlan) piece(id string) (PlanPiece, bool) {
	for _, p := range plan.Pieces {
		if p.ID == id {
			return p, true
		}
	}
	return PlanPiece{}, false
}

// --- the composite preview, stitched from the pieces' own previews ---

// PlanPart is one piece of the composite fix after its own fix prepared it: the call, and its
// preview, or why it could not be prepared.
type PlanPart struct {
	Call    PlanCall
	Preview FixPreview
	Err     string
}

// Blocks says why a part cannot be applied as prepared: "" when it can.
func (p PlanPart) Blocks() string {
	switch {
	case p.Err != "":
		return p.Err
	case p.Preview.Express:
		return "SQL Server Express has no SQL Server Agent: kakapo cannot create the job."
	case len(p.Preview.Changes) == 0 || p.Preview.Statement == "":
		if len(p.Preview.Warnings) > 0 {
			return p.Preview.Warnings[0]
		}
		return "it cannot be created as previewed."
	}
	return ""
}

// CreatesOperator says whether the part creates the operator (operator-alerts with none there).
func (p PlanPart) CreatesOperator() bool {
	for _, c := range p.Preview.Changes {
		if c.Setting == "Operator" && c.Current == "none with an email address" {
			return true
		}
	}
	return false
}

const (
	planOrderWarn    = "kakapo creates the pieces one after another, each through its own fix: %s. If one fails, the pieces before it stay (each has its own change log entry and rollback) and the rest are not created."
	planOperatorWarn = "The operator is created first; the jobs after it are prepared again just before they are created, so that they mail it when they fail (the rows above show them before the operator exists)."
	planNothing      = "Tick at least one piece to create."
)

// StitchMaintenancePlan is the composite preview built from the pieces' previews (in apply order):
// the inputs (the folder and the operator from the pieces that own them), one section of changes per
// piece headed by its title, the warnings without repeats, the statement of every piece in order
// with a comment line and GO between, and the pieces' rollbacks in reverse order. A part that blocks
// blocks the plan with its reason: no changes then.
func StitchMaintenancePlan(choice PlanChoice, parts []PlanPart) FixPreview {
	p := FixPreview{Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}
	for k, v := range choice.Options {
		p.Options[k] = v
	}
	inputs := make([]FixInput, len(choice.Inputs))
	copy(inputs, choice.Inputs)
	blocked := append([]string{}, choice.Blocked...)
	// The inputs a piece owns: its value as the piece built it, and its warning.
	for _, part := range parts {
		if p.EngineVersion == "" {
			p.EngineVersion = part.Preview.EngineVersion
		}
		for _, in := range part.Preview.Inputs {
			name, ok := planInputOf(part.Call.Step, in.Name)
			if !ok {
				continue
			}
			for i := range inputs {
				if inputs[i].Name != name {
					continue
				}
				if _, given := choice.Options[name]; !given || inputs[i].Value == "" {
					inputs[i].Value = in.Value
					p.Options[name] = in.Value
				}
				if in.Warning != "" && inputs[i].Warning == "" {
					inputs[i].Warning = in.Warning
				}
			}
		}
	}
	p.Inputs = inputs
	if len(choice.Calls) == 0 && len(choice.Blocked) == 0 {
		blocked = append(blocked, planNothing)
	}
	for _, part := range parts {
		if why := part.Blocks(); why != "" {
			blocked = append(blocked, part.Call.Title+": "+why)
		}
	}
	seen := map[string]bool{}
	warn := func(w string) {
		if w != "" && !seen[w] {
			seen[w] = true
			p.Warnings = append(p.Warnings, w)
		}
	}
	for _, w := range blocked {
		warn(w)
	}
	for _, w := range choice.Warnings {
		warn(w)
	}
	if len(parts) > 1 {
		titles := []string{}
		for _, part := range parts {
			titles = append(titles, strings.ToLower(part.Call.Title))
		}
		warn(fmt.Sprintf(planOrderWarn, strings.Join(titles, ", then ")))
	}
	if len(parts) > 1 && parts[0].Call.Step == PlanStepAlerts && parts[0].CreatesOperator() {
		warn(planOperatorWarn)
	}
	for _, part := range parts {
		for _, w := range part.Preview.Warnings {
			warn(w)
		}
		// The room line (docs/phase5l-contract.md): every part's needs, one line per drive.
		p.Needs = append(p.Needs, part.Preview.Needs...)
	}
	if len(blocked) > 0 {
		return p
	}
	p.Statement, p.Rollback = PlanStatements(parts)
	for _, part := range parts {
		for _, c := range part.Preview.Changes {
			p.Changes = append(p.Changes, Drift{Setting: part.Call.Title + ": " + c.Setting, Current: c.Current, Target: c.Target})
		}
	}
	return p
}

// PlanStatements are the parts' statements in order, each after a comment line naming it and
// followed by GO, and their rollbacks in reverse order: the composite's statement and rollback,
// and the summary change row's.
func PlanStatements(parts []PlanPart) (string, string) {
	var stmt, rb []string
	for i, part := range parts {
		stmt = append(stmt, fmt.Sprintf("-- %d. %s (%s)\n%s\nGO", i+1, part.Call.Title, part.Call.FixID, part.Preview.Statement))
	}
	for i := len(parts) - 1; i >= 0; i-- {
		if r := parts[i].Preview.Rollback; r != "" {
			rb = append(rb, fmt.Sprintf("-- Undo %d. %s\n%s\nGO", i+1, parts[i].Call.Title, r))
		}
	}
	return strings.Join(stmt, "\n"), strings.Join(rb, "\n")
}

// PlanOutcome is what an apply of the composite did: the parts created in order, the part it
// stopped at with why, and the parts after it that were not created.
type PlanOutcome struct {
	Created    []PlanPart
	Failed     *PlanPart
	Why        string
	NotCreated []PlanPart
}

// RunMaintenancePlan applies the parts one after another in their order with apply (each piece's
// own fix) and stops at the first failure: the parts before it stay created, the rest are not
// applied.
func RunMaintenancePlan(parts []PlanPart, apply func(i int, part PlanPart) error) PlanOutcome {
	var o PlanOutcome
	for i, part := range parts {
		if o.Failed != nil {
			o.NotCreated = append(o.NotCreated, part)
			continue
		}
		if err := apply(i, part); err != nil {
			failed := part
			o.Failed, o.Why = &failed, err.Error()
			continue
		}
		o.Created = append(o.Created, part)
	}
	return o
}

// Titles are the parts' titles, lower case.
func (o PlanOutcome) titles(parts []PlanPart) []string {
	out := []string{}
	for _, p := range parts {
		out = append(out, strings.ToLower(p.Call.Title))
	}
	return out
}

// Description is the summary change log line of the outcome.
func (o PlanOutcome) Description(server string) string {
	stopped := ""
	if o.Failed != nil {
		stopped = strings.ToLower(o.Failed.Call.Title)
	}
	return PlanAppliedDescription(server, o.titles(o.Created), stopped, o.Why, o.titles(o.NotCreated))
}

// CreatedPieces are the plan's lines the created parts made.
func (o PlanOutcome) CreatedPieces() []string {
	out := []string{}
	for _, p := range o.Created {
		out = append(out, p.Call.Pieces...)
	}
	return out
}

// planInputOf maps a piece's input to the composite input it shows as.
func planInputOf(step, name string) (string, bool) {
	switch step + "/" + name {
	case PlanStepBackup + "/" + OptionFolder:
		return OptionPlanFolder, true
	case PlanStepBackup + "/" + OptionFullTime:
		return OptionPlanFullTime, true
	case PlanStepBackup + "/" + OptionLogMinutes:
		return OptionPlanLogMinutes, true
	case PlanStepAlerts + "/" + OptionOperatorName:
		return OptionPlanOperator, true
	case PlanStepAlerts + "/" + OptionOperatorEmail:
		return OptionPlanEmail, true
	case PlanStepCheck + "/" + OptionCheckdbTime:
		return OptionPlanCheckTime, true
	case PlanStepCleanup + "/" + OptionHistoryKeepDays:
		return OptionPlanKeepDays, true
	case PlanStepCleanup + "/" + OptionHistoryTime:
		return OptionPlanCleanupTime, true
	case PlanStepMaintenance + "/" + OptionMaintTime:
		return OptionPlanStatsTime, true
	}
	return "", false
}

// --- the times ---

// PlanSlot is when one piece of the plan starts: on the days (bits, Sunday 1 .. Saturday 64) at
// Clock (HH:MM, server time).
type PlanSlot struct {
	Piece string
	Days  int
	Clock string
}

// minutesOf is an HH:MM clock as minutes after midnight.
func minutesOf(clock string) int {
	t := agentStartTime(clock)
	return t/10000*60 + t/100%100
}

// PlanOverlaps are the pairs of slots that start within the same hour on a common day, in words:
// two pieces that would compete for the disk and the log.
func PlanOverlaps(slots []PlanSlot) []string {
	out := []string{}
	for i := 0; i < len(slots); i++ {
		for j := i + 1; j < len(slots); j++ {
			a, b := slots[i], slots[j]
			common := a.Days & b.Days
			if common == 0 {
				continue
			}
			d := minutesOf(a.Clock) - minutesOf(b.Clock)
			if d < 0 {
				d = -d
			}
			if d > 12*60 {
				d = 24*60 - d
			}
			if d >= 60 {
				continue
			}
			day := ""
			for k, name := range planDays {
				if common&(1<<k) != 0 {
					day = name
					break
				}
			}
			out = append(out, fmt.Sprintf("%s (%s) and %s (%s) start within the same hour on %s: they compete for the disk and the log; move one under More options.",
				strings.ToLower(planPieceDefFor(a.Piece).Title), a.Clock, strings.ToLower(planPieceDefFor(b.Piece).Title), b.Clock, day))
		}
	}
	return out
}

// expressRow is the database's row of kakapo's procedure when the procedure is installed and serves it.
func expressRow(f MaintenancePlanFacts) (MaintSettings, bool) {
	if f.Proc == nil || !f.Proc.Installed() {
		return MaintSettings{}, false
	}
	return f.Proc.Row(f.Database)
}

// expressPieceNote is why a piece is missing on Express.
func expressPieceNote(f MaintenancePlanFacts) string {
	switch {
	case f.Proc != nil && f.Proc.ProcExists && !f.Proc.ProcOurs:
		return "A procedure named master.dbo.kakapo_Maintenance exists that kakapo did not create, so kakapo cannot install its own."
	case f.Proc != nil && f.Proc.Installed():
		return "kakapo's maintenance procedure is installed but does not serve this database: Install adds it."
	}
	return "SQL Server Express has no SQL Server Agent: nothing schedules this. Install the maintenance procedure below does it with the rest."
}

// expressPlanNote is the plan's note on Express and the fix it offers: the install while the procedure
// does not serve this database.
func expressPlanNote(f MaintenancePlanFacts) (string, string) {
	if _, ok := expressRow(f); ok {
		call := "It has not been called yet: is the scheduled task in place?"
		if last := f.Proc.LastCall(); last != nil {
			call = "Its last call was " + sqlTime(*last) + "."
		}
		return "kakapo's maintenance procedure (master.dbo.kakapo_Maintenance) does this database's maintenance; each line reads its last run from its log. " + call, ""
	}
	switch {
	case f.Proc != nil && f.Proc.ProcExists && !f.Proc.ProcOurs:
		return "SQL Server Express has no SQL Server Agent, and a procedure named master.dbo.kakapo_Maintenance that kakapo did not create is in the way: kakapo cannot install its maintenance procedure.", ""
	case f.Proc != nil && f.Proc.Error != "" && !f.Proc.ProcExists:
		return "SQL Server Express has no SQL Server Agent, and kakapo could not read whether its maintenance procedure is installed: " + f.Proc.Error, ""
	case f.Proc != nil && f.Proc.Installed():
		return "kakapo's maintenance procedure is installed on this server but does not serve this database. Install the maintenance procedure adds it (the same Apply).", FixInstallMaintenanceProc
	}
	return "SQL Server Express has no SQL Server Agent. kakapo installs the whole plan as one procedure in master, and a scheduled task calls it every 15 minutes; it decides itself what is due.", FixInstallMaintenanceProc
}
