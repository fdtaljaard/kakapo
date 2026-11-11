package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Phase 5p (docs/phase5p-contract.md): the maintenance plan as one stored procedure, for SQL Server
// Express, which has no SQL Server Agent. install-maintenance-proc creates master.dbo.kakapo_Maintenance
// with its settings and log tables (maintproc_sql.go) and adds the database it is applied from;
// remove-maintenance-proc takes them away, or only one database's row. Any scheduler calls the procedure
// every 15 minutes and it does what is due. On Express the Upkeep card's maintenance plan row reads each
// piece's last run from the log, and the instance page's Installed helpers row lists the procedure.

// Ids of the procedure's fixes.
const (
	FixInstallMaintenanceProc = "install-maintenance-proc"
	FixRemoveMaintenanceProc  = "remove-maintenance-proc"
)

// MaintObject is the change row object of the procedure's fixes.
const MaintObject = "Maintenance procedure"

// The options of install-maintenance-proc and remove-maintenance-proc.
const (
	OptionProcFolder          = "folder"
	OptionProcKeepBackupDays  = "keepBackupDays"
	OptionProcFullTime        = "fullTime"
	OptionProcLogMinutes      = "logMinutes"
	OptionProcDay             = "checkDay"
	OptionProcCheckTime       = "checkTime"
	OptionProcStatsTime       = "statsTime"
	OptionProcIndex           = "index"
	OptionProcIndexTime       = "indexTime"
	OptionProcCleanupTime     = "cleanupTime"
	OptionProcKeepHistoryDays = "keepHistoryDays"
	OptionProcRunNow          = "runNow"
	// OptionProcRemove is what remove-maintenance-proc takes away: MaintRemoveAll, or one database's name.
	OptionProcRemove = "remove"
	MaintRemoveAll   = "everything"
)

// maintGrace is how long after its time a piece may wait before the plan row says it has not run: two
// calls of a 15-minute scheduler.
const maintGrace = 30 * time.Minute

// MaintSettings is one row of kakapo_MaintenanceSettings: what the procedure does for one database.
// Times are HH:MM, server time; days are Sunday .. Saturday.
type MaintSettings struct {
	Database        string     `json:"database"`
	Folder          string     `json:"folder"`
	KeepBackupDays  int        `json:"keepBackupDays"`
	FullTime        string     `json:"fullTime"`
	LogMinutes      int        `json:"logMinutes"`
	CheckDay        string     `json:"checkDay"`
	CheckTime       string     `json:"checkTime"`
	StatsTime       string     `json:"statsTime"`
	IndexWork       bool       `json:"indexWork"`
	IndexTime       string     `json:"indexTime"`
	CleanupDay      string     `json:"cleanupDay"`
	CleanupTime     string     `json:"cleanupTime"`
	KeepHistoryDays int        `json:"keepHistoryDays"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       *time.Time `json:"createdAt,omitempty"`
	LastCall        *time.Time `json:"lastCall,omitempty"`
}

// MaintRun is one row of kakapo_MaintenanceLog.
type MaintRun struct {
	Database    string     `json:"database"`
	Task        string     `json:"task"`
	Start       time.Time  `json:"start"`
	End         *time.Time `json:"end,omitempty"`
	Outcome     string     `json:"outcome"`
	ErrorNumber int        `json:"errorNumber,omitempty"`
	Message     string     `json:"message,omitempty"`
	File        string     `json:"file,omitempty"`
}

// MaintTaskLog is what the log says about one task of one database: its latest row, and when its
// last successful and last failed runs started.
type MaintTaskLog struct {
	Database   string
	Task       string
	Latest     MaintRun
	LastOK     *time.Time
	LastFailed *time.Time
}

// MaintProcFacts is what collect.ReadMaintProc reads: the server, the database the fix or the plan is
// for, the procedure and its tables, Ola Hallengren's procedures, every settings row, and the log by
// database and task. Error says what could not be read.
type MaintProcFacts struct {
	ServerName       string
	ProductVersion   string
	EngineEdition    int
	Edition          string
	ServerNow        time.Time
	Database         string
	RecoveryModel    string
	DefaultBackupDir string
	ProcExists       bool
	ProcOurs         bool // its header carries kakapo's marker
	ProcVersion      int
	SettingsExists   bool
	LogExists        bool
	OlaBackup        bool // DatabaseBackup and CommandExecute
	OlaCheck         bool // DatabaseIntegrityCheck and CommandExecute
	OlaIndex         bool // IndexOptimize and CommandExecute
	Settings         []MaintSettings
	Tasks            []MaintTaskLog
	LastFailure      *MaintRun
	Signing          MaintSigning
	Error            string
}

// MaintSigning is how the procedure is signed (maintproc_sql.go): the certificate, whether it signs the
// procedure (sys.crypt_properties) and still has its private key, its login and whether that is sysadmin;
// and the scheduled task's account, SYSTEM: its login, its master user (kakapo's or not), its EXECUTE.
type MaintSigning struct {
	CertExists        bool `json:"certExists"`
	CertPrivateKey    bool `json:"certPrivateKey"`
	Signed            bool `json:"signed"`
	CertLogin         bool `json:"certLogin"`
	CertSysadmin      bool `json:"certSysadmin"`
	SchedulerLogin    bool `json:"schedulerLogin"`
	SchedulerUser     bool `json:"schedulerUser"`
	SchedulerUserOurs bool `json:"schedulerUserOurs"`
	SchedulerExecute  bool `json:"schedulerExecute"`
}

// Ready says the signature lets the scheduled task run: signed, the private key gone, the login
// sysadmin, and SYSTEM may execute the procedure (when SYSTEM has a login at all).
func (s MaintSigning) Ready() bool {
	return s.Signed && !s.CertPrivateKey && s.CertLogin && s.CertSysadmin && (s.SchedulerExecute || !s.SchedulerLogin)
}

// Words says where the signature stands in a line.
func (s MaintSigning) Words() string {
	parts := []string{}
	switch {
	case s.Signed && !s.CertPrivateKey:
		parts = append(parts, "signed by the certificate "+MaintCertName+" (its private key removed)")
	case s.Signed:
		parts = append(parts, "signed by the certificate "+MaintCertName+", which still has its private key")
	default:
		parts = append(parts, "not signed")
	}
	switch {
	case s.CertLogin && s.CertSysadmin:
		parts = append(parts, "its login "+MaintCertLogin+" is sysadmin")
	case s.CertLogin:
		parts = append(parts, "its login "+MaintCertLogin+" is not sysadmin")
	default:
		parts = append(parts, "no certificate login")
	}
	switch {
	case !s.SchedulerLogin:
		parts = append(parts, MaintSchedulerLogin+" has no login on this server")
	case s.SchedulerExecute:
		parts = append(parts, MaintSchedulerLogin+" may EXECUTE it")
	default:
		parts = append(parts, MaintSchedulerLogin+" may not execute it")
	}
	return strings.Join(parts, "; ")
}

// Installed says kakapo's procedure is there with its tables.
func (f MaintProcFacts) Installed() bool {
	return f.ProcExists && f.ProcOurs && f.SettingsExists && f.LogExists
}

// Row is a database's settings row.
func (f MaintProcFacts) Row(db string) (MaintSettings, bool) {
	for _, s := range f.Settings {
		if strings.EqualFold(s.Database, db) {
			return s, true
		}
	}
	return MaintSettings{}, false
}

// Task is what the log says about a task of a database (a zero log when it has no row).
func (f MaintProcFacts) Task(db, task string) MaintTaskLog {
	for _, t := range f.Tasks {
		if strings.EqualFold(t.Database, db) && t.Task == task {
			return t
		}
	}
	return MaintTaskLog{Database: db, Task: task}
}

// cleanupLastOK is the last successful history cleanup for any database: it is server-wide.
func (f MaintProcFacts) cleanupLastOK() *time.Time {
	var last *time.Time
	for _, t := range f.Tasks {
		if t.Task == PieceCleanup && t.LastOK != nil && (last == nil || t.LastOK.After(*last)) {
			last = t.LastOK
		}
	}
	return last
}

// LastCall is the latest call the settings rows recorded; nil before the first.
func (f MaintProcFacts) LastCall() *time.Time {
	var last *time.Time
	for _, s := range f.Settings {
		if s.LastCall != nil && (last == nil || s.LastCall.After(*last)) {
			last = s.LastCall
		}
	}
	return last
}

// CanCompress says whether the edition can compress backups: not Express, not Web.
func (f MaintProcFacts) CanCompress() bool {
	return f.EngineEdition != 4 && !strings.HasPrefix(strings.TrimSpace(f.Edition), "Web")
}

// --- the rules: when a task is due (the procedure's own rules, mirrored) ---

// heavyTask says whether a task is heavy work: it runs only within the window, once per time.
func heavyTask(task string) bool {
	return task == PieceIntegrity || task == PieceStatistics || task == PieceIndexes
}

// Latest is the latest scheduled start of a task at or before now. The log backup has no time of its
// own (every call); Latest is now for it.
func (s MaintSettings) Latest(task string, now time.Time) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	clock := func(c string) time.Duration { return time.Duration(minutesOf(c)) * time.Minute }
	daily := func(c string) time.Time {
		t := day.Add(clock(c))
		if t.After(now) {
			t = t.AddDate(0, 0, -1)
		}
		return t
	}
	weekly := func(d, c string) time.Time {
		back := (int(now.Weekday()) - weekdayOf(d) + 7) % 7
		t := day.AddDate(0, 0, -back).Add(clock(c))
		if t.After(now) {
			t = t.AddDate(0, 0, -7)
		}
		return t
	}
	switch task {
	case PieceFullBackup:
		return daily(s.FullTime)
	case PieceIntegrity:
		return weekly(s.CheckDay, s.CheckTime)
	case PieceIndexes:
		return weekly(s.CheckDay, s.IndexTime)
	case PieceStatistics:
		t := daily(s.StatsTime)
		if int(t.Weekday()) == weekdayOf(s.CheckDay) {
			t = t.AddDate(0, 0, -1)
		}
		return t
	case PieceCleanup:
		return weekly(s.CleanupDay, s.CleanupTime)
	}
	return now
}

// MaintDue says what a call at now does with a task of one database, as the procedure decides it:
// due, and why it is skipped instead (outside the window) when it is. logReady says the database is in
// FULL or BULK_LOGGED recovery and has had a full backup; cleanupOK is the last successful cleanup of
// any database.
func MaintDue(s MaintSettings, task string, l MaintTaskLog, logReady bool, cleanupOK *time.Time, now time.Time) (bool, string) {
	after := func(t *time.Time, at time.Time) bool { return t != nil && !t.Before(at) }
	latest := s.Latest(task, now)
	switch task {
	case PieceIntegrity, PieceStatistics, PieceIndexes:
		if task == PieceIndexes && !s.IndexWork {
			return false, ""
		}
		if after(l.LastOK, latest) || after(l.LastFailed, latest) {
			return false, ""
		}
		if !now.Before(latest.Add(maintWindowHours * time.Hour)) {
			return true, fmt.Sprintf("Skipped: outside the window (%s to %s); it waits for the next one.", sqlTime(latest), sqlTime(latest.Add(maintWindowHours*time.Hour)))
		}
		return true, ""
	case PieceFullBackup:
		return !after(l.LastOK, latest), ""
	case PieceLogBackup:
		if !logReady {
			return false, ""
		}
		return l.LastOK == nil || now.Sub(*l.LastOK) >= time.Duration(s.LogMinutes)*time.Minute-maintLogSlackSecs*time.Second, ""
	case PieceCleanup:
		return !after(cleanupOK, latest), ""
	}
	return false, ""
}

// --- the schedule in words ---

// maintDaysExcept is "Monday to Saturday" for the days other than one.
func maintDaysExcept(day string) string {
	w := weekdayOf(day)
	return CheckdbDays[(w+1)%7] + " to " + CheckdbDays[(w+6)%7]
}

// MaintTaskWords is a task's schedule in words, as the preview and the plan row show it.
func (s MaintSettings) MaintTaskWords(task string) string {
	switch task {
	case PieceIntegrity:
		return fmt.Sprintf("weekly, %s from %s (until %s)", s.CheckDay, s.CheckTime, clockPlus(s.CheckTime, maintWindowHours))
	case PieceStatistics:
		return fmt.Sprintf("%s from %s (until %s)", maintDaysExcept(s.CheckDay), s.StatsTime, clockPlus(s.StatsTime, maintWindowHours))
	case PieceIndexes:
		if !s.IndexWork {
			return "off"
		}
		return fmt.Sprintf("weekly, %s from %s (until %s)", s.CheckDay, s.IndexTime, clockPlus(s.IndexTime, maintWindowHours))
	case PieceFullBackup:
		return fmt.Sprintf("daily from %s, or at the next call after it", s.FullTime)
	case PieceLogBackup:
		return fmt.Sprintf("at each call, %d minutes or more after the last", s.LogMinutes)
	case PieceCleanup:
		return fmt.Sprintf("weekly, %s from %s, keeps %d days", s.CleanupDay, s.CleanupTime, s.KeepHistoryDays)
	}
	return ""
}

// clockPlus is an HH:MM clock some hours later.
func clockPlus(clock string, hours int) string {
	m := (minutesOf(clock) + hours*60) % (24 * 60)
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// --- the plan row on Express ---

// maintPlanPiece fills a line of the maintenance plan from the procedure's settings row and log: its
// status, the last run and its outcome, and "Not run since ..." when a time has passed without a row.
func maintPlanPiece(p *PlanPiece, f MaintProcFacts, s MaintSettings, now time.Time) {
	task := p.ID
	l := f.Task(s.Database, task)
	p.Job = "master.dbo.kakapo_Maintenance"
	p.Schedule = s.MaintTaskWords(task)
	if !l.Latest.Start.IsZero() {
		p.LastRun = sqlTime(l.Latest.Start)
		p.LastOutcome = maintOutcomeWords(l.Latest)
		if l.Latest.End != nil {
			p.LastDuration = durationWords(l.Latest.End.Sub(l.Latest.Start).Seconds())
		}
	}
	since := "it was set up"
	if s.CreatedAt != nil {
		since = "it was set up on " + sqlTime(*s.CreatedAt)
	}
	if l.LastOK != nil {
		since = sqlTime(*l.LastOK)
	}
	notRun := func(due time.Time) {
		p.Status = PlanOverdue
		p.Note = fmt.Sprintf("Not run since %s: it was due at %s. Is the scheduled task calling it?", since, sqlTime(due))
	}
	switch {
	case !s.Enabled:
		p.Status, p.Note = PlanDisabled, "This database's row in kakapo_MaintenanceSettings is disabled (Enabled = 0): the procedure skips it."
		return
	case task == PieceIndexes && !s.IndexWork:
		p.Status, p.Note = PlanOptional, "Off: kakapo's procedure does index work only when it is turned on (and Ola Hallengren's IndexOptimize is installed); statistics matter more."
		return
	case l.Latest.Outcome == MaintRunning:
		p.Status, p.Note = PlanInPlace, "Running now (started "+sqlTime(l.Latest.Start)+")."
		return
	case l.Latest.Outcome == MaintFailed:
		p.Status, p.Note = PlanFailing, "Failed: "+orQ(l.Latest.Message)
		return
	}
	var due time.Time
	switch task {
	case PieceLogBackup:
		if !fullOrBulk(f.RecoveryModel) {
			p.Status, p.Note = PlanNotNeeded, "The database is in SIMPLE recovery: the log empties itself and there is no log to back up (DBC08 decides the model)."
			return
		}
		switch {
		case l.LastOK != nil:
			due = l.LastOK.Add(time.Duration(s.LogMinutes) * time.Minute)
		case s.CreatedAt != nil:
			// The first log backup follows the first full backup, at the call after it.
			due = s.CreatedAt.Add(time.Duration(s.LogMinutes) * time.Minute)
		default:
			due = now
		}
	case PieceFullBackup:
		due = s.Latest(task, now)
		if s.CreatedAt != nil && due.Before(*s.CreatedAt) {
			// Backups catch up: the first call after the install takes the first one.
			due = *s.CreatedAt
		}
	default:
		due = s.Latest(task, now)
		if s.CreatedAt != nil && due.Before(*s.CreatedAt) {
			p.Status = PlanNeverRan
			p.Note = "Not run yet: the first time is " + sqlTime(s.nextAfter(task, now)) + "."
			if l.LastOK != nil {
				p.Status, p.Note = PlanInPlace, ""
			}
			return
		}
	}
	ranSince := !l.Latest.Start.IsZero() && !l.Latest.Start.Before(due)
	if task == PieceCleanup {
		ranSince = f.cleanupLastOK() != nil && !f.cleanupLastOK().Before(due)
	}
	switch {
	case ranSince && l.Latest.Outcome == MaintSkipped:
		p.Status, p.Note = PlanOverdue, l.Latest.Message
	case ranSince || (l.LastOK != nil && now.Before(due.Add(maintGrace))):
		p.Status = PlanInPlace
	case now.Before(due.Add(maintGrace)):
		p.Status = PlanNeverRan
		p.Note = "Not run yet: due at " + sqlTime(due) + "."
	default:
		notRun(due)
	}
}

// nextAfter is the next scheduled start of a task after now.
func (s MaintSettings) nextAfter(task string, now time.Time) time.Time {
	l := s.Latest(task, now)
	switch task {
	case PieceIntegrity, PieceIndexes, PieceCleanup:
		return l.AddDate(0, 0, 7)
	case PieceStatistics:
		t := l.AddDate(0, 0, 1)
		for !t.After(now) || int(t.Weekday()) == weekdayOf(s.CheckDay) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	return l.AddDate(0, 0, 1)
}

// maintOutcomeWords is a log row's outcome as the plan row says it.
func maintOutcomeWords(r MaintRun) string {
	switch r.Outcome {
	case MaintOK:
		return "succeeded"
	case MaintFailed:
		return "failed"
	case MaintSkipped:
		return "skipped"
	case MaintRunning:
		return "running"
	}
	return strings.ToLower(r.Outcome)
}

// --- the instance page's Installed helpers line ---

// maintHelperLine is the procedure's line on the Installed helpers row; false when there is nothing to
// show (not installed, and not Express, where kakapo would not offer it).
func maintHelperLine(f *MaintProcFacts, edition int) (InstalledHelper, bool) {
	h := InstalledHelper{ID: "maintenance-proc", Title: "kakapo's maintenance procedure (Express)", Details: []string{}}
	switch {
	case f == nil:
		return h, false
	case !f.ProcExists && f.Error != "" && edition == 4:
		h.State, h.Sentence = HelperUnknown, "kakapo could not see whether its maintenance procedure is installed: "+f.Error
	case !f.ProcExists && edition == 4:
		h.State = HelperNotInstalled
		h.Sentence = "Nothing installed. SQL Server Express has no SQL Server Agent: kakapo installs the maintenance plan as one procedure in master, which a scheduled task calls every 15 minutes. Install it from a database's Upkeep card (Maintenance plan)."
	case !f.ProcExists:
		return h, false
	case !f.ProcOurs:
		h.State = HelperForeign
		h.Sentence = "A procedure named master.dbo.kakapo_Maintenance exists on this server, but kakapo did not create it: kakapo leaves it alone."
	default:
		h.State, h.FixID, h.FixLabel, h.FixTitle = HelperInstalled, FixRemoveMaintenanceProc, "Remove", "Remove kakapo's maintenance procedure"
		dbs := []string{}
		for _, s := range f.Settings {
			name := s.Database
			if !s.Enabled {
				name += " (disabled)"
			}
			dbs = append(dbs, name)
		}
		serves := "no database yet"
		if len(dbs) > 0 {
			serves = strings.Join(dbs, ", ")
		}
		call := "It has not been called yet: is the scheduled task in place?"
		if last := f.LastCall(); last != nil {
			call = "Last call " + sqlTime(*last) + "."
			if f.ServerNow.Sub(*last) > maintGrace {
				call = "Last call " + sqlTime(*last) + ": nothing has called it since. Is the scheduled task running?"
			}
		}
		failure := "No failure in its log."
		if r := f.LastFailure; r != nil {
			failure = fmt.Sprintf("Last failure: %s of %s at %s (%s).", r.Task, r.Database, sqlTime(r.Start), orQ(r.Message))
		}
		h.Sentence = fmt.Sprintf("kakapo's maintenance procedure serves %s. %s %s", serves, call, failure)
		h.Details = append(h.Details, fmt.Sprintf("Procedure master.dbo.kakapo_Maintenance, version %d; settings in master.dbo.kakapo_MaintenanceSettings, its log in master.dbo.kakapo_MaintenanceLog", f.ProcVersion))
		for _, s := range f.Settings {
			h.Details = append(h.Details, fmt.Sprintf("%s: backups to %s, kept %d days; full %s; log every %d min; check %s %s; statistics %s",
				s.Database, s.Folder, s.KeepBackupDays, s.FullTime, s.LogMinutes, s.CheckDay, s.CheckTime, s.StatsTime))
		}
		h.Details = append(h.Details, "Signature: "+f.Signing.Words())
		h.Details = append(h.Details, "Called by a scheduled task kakapo cannot see: "+MaintSchtasks(f.ServerName))
		if !f.Signing.Ready() {
			h.Sentence += " It is not ready for the scheduled task (" + f.Signing.Words() + "): a call as SYSTEM fails until it is installed again, which signs it anew."
		}
	}
	if f.Error != "" && h.State == HelperInstalled {
		h.Details = append(h.Details, "Not everything could be read: "+f.Error)
	}
	return h, true
}

// --- install-maintenance-proc ---

// MaintSchtasks is the Windows Task Scheduler command that calls the procedure every 15 minutes.
func MaintSchtasks(server string) string {
	return fmt.Sprintf(`schtasks /Create /TN "kakapo maintenance" /SC MINUTE /MO 15 /RU SYSTEM /TR "sqlcmd -S %s -E -b -Q \"EXEC master.dbo.kakapo_Maintenance\""`, orQ(server))
}

// MaintSchtasksDelete is the command that deletes the scheduled task.
const MaintSchtasksDelete = `schtasks /Delete /TN "kakapo maintenance" /F`

// MaintInstallFacts is what install-maintenance-proc is built from: what was read, the folder the
// backups go to and whether it exists as the SQL Server service sees it.
type MaintInstallFacts struct {
	MaintProcFacts
	FolderChecked bool
	FolderExists  bool
	FolderError   string
	// Sizes are the database's backup sizes in msdb, for the room line (docs/phase5l-contract.md).
	Sizes BackupSizes
}

// MaintFolder is the folder the install writes to: the option when given, else this database's row,
// else the instance's default backup folder, normalised. The service checks it before the preview.
func MaintFolder(f MaintProcFacts, options map[string]string) string {
	if v, ok := options[OptionProcFolder]; ok {
		return normalizeFolder(v)
	}
	if s, ok := f.Row(f.Database); ok {
		return normalizeFolder(s.Folder)
	}
	return normalizeFolder(f.DefaultBackupDir)
}

const (
	maintKeepBackupHelp  = "Backup files older than this are deleted after each verified backup: 1 to 60 days."
	maintKeepHistoryHelp = "msdb's backup and job history and the procedure's own log older than this are deleted weekly: 7 to 365 days."
	maintRunNowHelp      = "Call the procedure once for this database as soon as it is installed, so the first full backup is taken now. kakapo waits while it runs."
	maintIndexHelp       = "Weekly on the maintenance day: reorganise from 50% fragmentation; from 80%, rebuild online where the edition can and reorganise elsewhere, never offline; tables of 1,000 pages or more; no new step after the 6-hour window, each step logged in Ola's CommandLog (Ola Hallengren's IndexOptimize, Brent Ozar's thresholds). Off by default: statistics matter more."
	maintDayHelp         = "The integrity check, index work and the history cleanup run on this day; statistics on the other nights."
)

// maintDefaults are the settings a new row gets: the 5b plan's times.
func maintDefaults(db, folder string) MaintSettings {
	return MaintSettings{Database: db, Folder: folder, KeepBackupDays: DefaultKeepDays, FullTime: DefaultFullTime, LogMinutes: DefaultLogMinutes,
		CheckDay: DefaultPlanDay, CheckTime: DefaultPlanCheckTime, StatsTime: DefaultPlanStatsTime, IndexTime: DefaultPlanIndexTime,
		CleanupDay: DefaultPlanDay, CleanupTime: DefaultPlanCleanupTime, KeepHistoryDays: DefaultHistoryKeepDays, Enabled: true}
}

// BuildInstallMaintenanceProcFix builds install-maintenance-proc's preview for the facts' database with
// the options: the three objects, this database's settings row and one row per task in words, the
// warnings in the contract's order, and the statement (tables, procedure, settings row) with its
// rollback. A folder that is missing, a kakapo_Maintenance kakapo did not create, or a bad option leave
// the preview without changes.
// maintChoice is what install-maintenance-proc's options choose: the settings row, the inputs, why
// nothing can be applied (a bad value or folder), and Run now.
type maintChoice struct {
	settings MaintSettings
	inputs   []FixInput
	blocked  []string
	runNow   bool
}

// MaintSettingsFor is the settings row install-maintenance-proc writes for the facts' database with the
// options: the row as it is (or the defaults) with the values given.
func MaintSettingsFor(f MaintProcFacts, options map[string]string) (MaintSettings, bool) {
	c := maintChoose(MaintInstallFacts{MaintProcFacts: f, FolderChecked: false}, options)
	return c.settings, len(c.blocked) == 0
}

func maintChoose(f MaintInstallFacts, options map[string]string) maintChoice {
	db := f.Database
	old, had := f.Row(db)
	s := maintDefaults(db, MaintFolder(f.MaintProcFacts, options))
	if had {
		// The row as it is, for the values not given.
		s = old
		s.Folder, s.Enabled = MaintFolder(f.MaintProcFacts, options), true
	}
	blocked := []string{}
	inputs := []FixInput{}
	folderIn := FixInput{Name: OptionProcFolder, Label: "Backup folder", Value: s.Folder, Kind: "text", Help: folderHelp}
	switch {
	case s.Folder == "":
		folderIn.Warning = "Type the folder the backups go to, as the SQL Server service on " + orQ(f.ServerName) + " sees it."
	case !ValidBackupFolder(s.Folder):
		folderIn.Warning = "The folder " + s.Folder + " is not a full Windows path (like D:\\Backup or \\\\server\\share): type another."
	case f.FolderChecked && !f.FolderExists:
		folderIn.Warning = "The folder " + s.Folder + " does not exist on " + orQ(f.ServerName) + " (as the SQL Server service sees it): create it, or type another."
	}
	if folderIn.Warning != "" {
		blocked = append(blocked, folderIn.Warning)
	}
	inputs = append(inputs, folderIn)
	keep, keepIn, w := xeIntInput(options, OptionProcKeepBackupDays, "Keep backups (days)", maintKeepBackupHelp, s.KeepBackupDays, 1, maxKeepDays, false)
	if w != "" {
		blocked = append(blocked, w)
	}
	s.KeepBackupDays = keep
	inputs = append(inputs, keepIn)
	runNow := strings.EqualFold(strings.TrimSpace(options[OptionProcRunNow]), "true")
	inputs = append(inputs, FixInput{Name: OptionProcRunNow, Label: "Take the first full backup now", Value: fmt.Sprint(runNow), Kind: "bool", Help: maintRunNowHelp})
	if f.OlaIndex {
		v := s.IndexWork
		if typed, given := options[OptionProcIndex]; given {
			v = strings.EqualFold(strings.TrimSpace(typed), "true")
		}
		s.IndexWork = v
		inputs = append(inputs, FixInput{Name: OptionProcIndex, Label: "Weekly index work (optional)", Value: fmt.Sprint(v), Kind: "bool", Help: maintIndexHelp})
	} else {
		s.IndexWork = false
	}
	clock := func(name, label, help string, v *string) {
		in := FixInput{Name: name, Label: label, Value: *v, Kind: "text", Help: help, Advanced: true}
		if typed, given := options[name]; given {
			if x, ok := parseClock(typed); ok {
				*v, in.Value = x, x
			} else {
				in.Value, in.Warning = strings.TrimSpace(typed), planTimeWarn
				blocked = append(blocked, label+": "+planTimeWarn)
			}
		}
		inputs = append(inputs, in)
	}
	clock(OptionProcFullTime, "Full backup from", fullTimeHelp, &s.FullTime)
	logIn := FixInput{Name: OptionProcLogMinutes, Label: "Log backup every (minutes)", Value: strconv.Itoa(s.LogMinutes), Kind: "select", Options: LogMinuteChoices,
		Help: logMinutesHelp + " The procedure backs the log up at the first call this long after the last.", Advanced: true}
	if typed, given := options[OptionProcLogMinutes]; given {
		if v, ok := parseLogMinutes(typed); ok {
			s.LogMinutes, logIn.Value = v, strconv.Itoa(v)
		} else {
			logIn.Value, logIn.Warning = strings.TrimSpace(typed), logMinutesWarn
			blocked = append(blocked, logMinutesWarn)
		}
	}
	inputs = append(inputs, logIn)
	dayIn := FixInput{Name: OptionProcDay, Label: "Maintenance day", Value: s.CheckDay, Kind: "select", Options: CheckdbDays, Help: maintDayHelp, Advanced: true}
	if typed, given := options[OptionProcDay]; given {
		if v, ok := checkdbDay(typed); ok {
			s.CheckDay, s.CleanupDay, dayIn.Value = v, v, v
		} else {
			dayIn.Value, dayIn.Warning = strings.TrimSpace(typed), checkdbDayWarn
			blocked = append(blocked, checkdbDayWarn)
		}
	}
	inputs = append(inputs, dayIn)
	clock(OptionProcCheckTime, "Integrity check from", checkdbTimeHelp, &s.CheckTime)
	clock(OptionProcStatsTime, "Statistics from", "Server time, every night except the maintenance day.", &s.StatsTime)
	if f.OlaIndex {
		clock(OptionProcIndexTime, "Index work from", "Server time, on the maintenance day, after the integrity check.", &s.IndexTime)
	}
	clock(OptionProcCleanupTime, "History cleanup from", historyTimeHelp, &s.CleanupTime)
	hist, histIn, w := xeIntInput(options, OptionProcKeepHistoryDays, "Keep history (days)", maintKeepHistoryHelp, s.KeepHistoryDays, minHistoryKeepDays, maxHistoryKeepDays, true)
	if w != "" {
		blocked = append(blocked, w)
	}
	s.KeepHistoryDays = hist
	inputs = append(inputs, histIn)
	return maintChoice{settings: s, inputs: inputs, blocked: blocked, runNow: runNow}
}

func BuildInstallMaintenanceProcFix(f MaintInstallFacts, options map[string]string) (FixPreview, Fix, error) {
	db := f.Database
	old, had := f.Row(db)
	c := maintChoose(f, options)
	s, inputs, blocked, runNow := c.settings, c.inputs, c.blocked, c.runNow

	if f.ProcExists && !f.ProcOurs {
		blocked = append(blocked, fmt.Sprintf("A procedure named master.dbo.kakapo_Maintenance already exists on %s and kakapo did not create it: kakapo leaves it alone. Drop or rename it by hand if nobody needs it.", orQ(f.ServerName)))
	}
	if f.ProcOurs && f.ProcVersion > MaintProcVersion {
		blocked = append(blocked, fmt.Sprintf("The procedure on %s is version %d, newer than this kakapo's %d: update kakapo before changing it.", orQ(f.ServerName), f.ProcVersion, MaintProcVersion))
	}
	if m := EngineMajor(f.ProductVersion); m > 0 && m < 11 {
		blocked = append(blocked, fmt.Sprintf("kakapo's procedure needs SQL Server 2012 or later; this server runs %s.", f.ProductVersion))
	}
	if f.EngineEdition == 5 || f.EngineEdition == 8 {
		blocked = append(blocked, "Azure SQL takes its own backups and has no master procedures to schedule: kakapo does not install its procedure there.")
	}

	opts := map[string]string{}
	for _, in := range inputs {
		opts[in.Name] = in.Value
	}
	installed := f.Installed()
	fix := suFix(FixInstallMaintenanceProc)
	fix.Current = "not installed"
	if installed {
		fix.Current = fmt.Sprintf("installed, serving %d database(s)", len(f.Settings))
	}
	fix.Target = "master.dbo.kakapo_Maintenance serving " + db
	fix.Options = opts
	p := FixPreview{Database: db, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: inputs, Options: opts}
	p.Lead = fmt.Sprintf("Installs the whole maintenance plan of %s as one procedure in master. Nothing runs until a scheduled task calls it: the first warning gives the command.", db)
	p.Warnings = append(p.Warnings, blocked...)
	if f.FolderError != "" && len(blocked) == 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("kakapo could not check that %s exists (%s): make sure it does, or every backup fails.", s.Folder, f.FolderError))
	}
	p.Warnings = append(p.Warnings,
		"Nothing runs until something calls the procedure. Create the scheduled task on the server, in an administrator command prompt: "+MaintSchtasks(f.ServerName)+
			" It runs as "+MaintSchedulerLogin+", which may EXECUTE this one procedure and nothing else: kakapo grants SYSTEM nothing more.")
	if !f.Signing.SchedulerLogin {
		p.Warnings = append(p.Warnings, MaintSchedulerLogin+" has no login on "+orQ(f.ServerName)+
			", so a task running as SYSTEM cannot connect: create the login (CREATE LOGIN [NT AUTHORITY\\SYSTEM] FROM WINDOWS) and apply again, or run the task as an account that may EXECUTE the procedure.")
	}
	if fullOrBulk(f.RecoveryModel) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s is in %s recovery: call the procedure every 15 minutes, or the log is backed up less often and a failure can lose more work.", db, f.RecoveryModel))
	}
	p.Warnings = append(p.Warnings, "The backups run as the SQL Server service account; the folder must let it write.")
	if f.EngineEdition == 4 {
		p.Warnings = append(p.Warnings, "SQL Server Express cannot compress backups: the files are full size.")
	} else if !f.CanCompress() {
		p.Warnings = append(p.Warnings, "This edition cannot compress backups: the files are full size.")
	}
	if f.OlaBackup || f.OlaCheck || f.OlaIndex {
		p.Warnings = append(p.Warnings, "Ola Hallengren's procedures are in master: the procedure calls them where they are there (DatabaseBackup, DatabaseIntegrityCheck, IndexOptimize).")
	}
	if runNow {
		p.Warnings = append(p.Warnings, "Run now waits while the first full backup (and anything else due) runs: on a large database that takes minutes.")
	}
	p.Warnings = append(p.Warnings,
		"The procedure is signed with a certificate, "+MaintCertName+", whose login "+MaintCertLogin+" is sysadmin: while it runs, the procedure (and only the procedure) has sysadmin's rights, because every task reaches other databases, msdb and xp_delete_file. "+
			"The certificate's private key is removed after signing, so nothing else can ever be signed with it. "+
			"Pattern: Erland Sommarskog, \"Packaging Permissions in Stored Procedures\"; Microsoft Learn, \"Module signing\".",
		"Changing the procedure by hand removes its signature, and the scheduled task then fails: apply this fix again to sign it anew (a new certificate each time).")
	p.Warnings = append(p.Warnings, "Needs sysadmin on "+orQ(f.ServerName)+" to create the procedure and its tables in master.")
	p.Needs = BackupScheduleNeeds(BackupSchedule{Folder: s.Folder, Full: true, Log: fullOrBulk(f.RecoveryModel), KeepDays: s.KeepBackupDays,
		Compress: f.CanCompress()}, f.Sizes)
	if len(blocked) > 0 {
		return p, fix, nil
	}

	verb := "created"
	if f.ProcExists {
		verb = fmt.Sprintf("updated in place (version %d to %d)", f.ProcVersion, MaintProcVersion)
	}
	table := func(name string, exists bool) Drift {
		d := Drift{Setting: "Table master.dbo." + name, Current: "none", Target: "created"}
		if exists {
			d.Current, d.Target = "exists", "kept as it is"
		}
		return d
	}
	procCur := "none"
	if f.ProcExists {
		procCur = fmt.Sprintf("version %d", f.ProcVersion)
	}
	rowCur := "none"
	if had {
		rowCur = maintRowWords(old)
	}
	p.Changes = append(p.Changes,
		Drift{Setting: "Procedure master.dbo.kakapo_Maintenance", Current: procCur, Target: verb},
		table(MaintSettingsName, f.SettingsExists), table(MaintLogName, f.LogExists),
		Drift{Setting: "Settings for " + db, Current: rowCur, Target: maintRowWords(s)})
	for _, task := range maintTasks {
		target := s.MaintTaskWords(task)
		if task == PieceLogBackup && !fullOrBulk(f.RecoveryModel) {
			target = logNotNeeded
		}
		p.Changes = append(p.Changes, Drift{Setting: planPieceDefFor(task).Title, Target: target})
	}
	signCur := "none"
	if f.Signing.CertExists {
		signCur = f.Signing.Words()
	}
	schedCur := "no master user"
	switch {
	case !f.Signing.SchedulerLogin:
		schedCur = "no login"
	case f.Signing.SchedulerExecute:
		schedCur = "may EXECUTE it"
	case f.Signing.SchedulerUser:
		schedCur = "master user, no EXECUTE"
	}
	schedTarget := "EXECUTE on dbo.kakapo_Maintenance only"
	if !f.Signing.SchedulerUser {
		schedTarget = "a master user (kakapo's), with EXECUTE on dbo.kakapo_Maintenance only"
	}
	if !f.Signing.SchedulerLogin {
		schedTarget = "nothing: there is no login to grant to"
	}
	p.Changes = append(p.Changes,
		Drift{Setting: "Signature", Current: signCur,
			Target: "signed by a new certificate " + MaintCertName + " (throwaway password, private key removed); its login " + MaintCertLogin + " in sysadmin"},
		Drift{Setting: MaintSchedulerLogin + " in master", Current: schedCur, Target: schedTarget})
	if runNow {
		p.Changes = append(p.Changes, Drift{Setting: "Run now", Target: "the procedure is called once for " + db + " after the install"})
	}
	p.Statement = MaintInstallStatement(f.CanCompress(), s)
	switch {
	case !f.ProcExists && !f.SettingsExists && !f.LogExists:
		p.Rollback = MaintDropStatement()
	case had:
		p.Rollback = MaintRestoreRowStatement(old)
		p.Warnings = append(p.Warnings, "The rollback puts this database's settings back; the procedure stays at this version.")
	default:
		p.Rollback = MaintDeleteRowStatement(db)
		p.Warnings = append(p.Warnings, "The rollback takes this database off the procedure; the procedure and the other databases' rows stay.")
	}
	return p, fix, nil
}

// maintRowWords is a settings row in a line: "to D:\Backup, keep 7 days, full 22:00, log 15 min, Sunday".
func maintRowWords(s MaintSettings) string {
	w := fmt.Sprintf("backups to %s, keep %d days; full %s; log every %d min; check %s %s; statistics %s; cleanup %s %s, keep %d days",
		s.Folder, s.KeepBackupDays, s.FullTime, s.LogMinutes, s.CheckDay, s.CheckTime, s.StatsTime, s.CleanupDay, s.CleanupTime, s.KeepHistoryDays)
	if s.IndexWork {
		w += "; index work " + s.CheckDay + " " + s.IndexTime
	}
	if !s.Enabled {
		w += " (disabled)"
	}
	return w
}

// MaintInstallDescription is install-maintenance-proc's change log line.
func MaintInstallDescription(server string, p FixPreview, f MaintProcFacts) string {
	verb := "Installed"
	if f.ProcExists {
		verb = "Updated"
	}
	return fmt.Sprintf("%s kakapo's maintenance procedure master.dbo.kakapo_Maintenance on %s, serving %s", verb, orQ(server), p.Database)
}

// MaintInstallReadBack says what was read after the install: verified when the procedure is kakapo's
// at this version with both tables, and the database's row matches. runs are the log rows of Run now.
func MaintInstallReadBack(after MaintProcFacts, want MaintSettings, runNow bool, runErr string, runs []MaintRun) (string, bool) {
	parts := []string{}
	ok := after.Installed() && after.ProcVersion == MaintProcVersion && after.Signing.Ready()
	switch {
	case after.Installed():
		parts = append(parts, fmt.Sprintf("procedure version %d and both tables in master", after.ProcVersion))
	case after.ProcExists:
		parts = append(parts, "the procedure exists but a table is missing")
	default:
		parts = append(parts, "no procedure kakapo_Maintenance")
	}
	got, has := after.Row(want.Database)
	switch {
	case !has:
		parts, ok = append(parts, "no settings row for "+want.Database), false
	case maintRowWords(got) != maintRowWords(want):
		parts, ok = append(parts, "settings for "+want.Database+": "+maintRowWords(got)), false
	default:
		parts = append(parts, "settings for "+want.Database+" as previewed")
	}
	if after.ProcExists {
		parts = append(parts, after.Signing.Words())
	}
	if runNow {
		r := []string{}
		for _, x := range runs {
			w := x.Task + " " + strings.ToLower(x.Outcome)
			switch {
			case x.Outcome == MaintFailed && x.Message != "":
				w += " (" + x.Message + ")"
			case x.File != "":
				w += " (" + x.File + ")"
			}
			r = append(r, w)
		}
		switch {
		case len(r) > 0:
			parts = append(parts, "run now: "+strings.Join(r, ", "))
		case runErr != "":
			parts = append(parts, "run now failed: "+runErr)
		default:
			parts = append(parts, "run now: nothing was due")
		}
		if runErr != "" && len(r) > 0 {
			parts = append(parts, "the call raised: "+runErr)
		}
	}
	return strings.Join(parts, "; "), ok
}

// --- remove-maintenance-proc ---

// BuildRemoveMaintenanceProcFix builds remove-maintenance-proc's preview: everything (the procedure and
// both tables, the log with them) or, with the remove option naming a database, only its settings row.
// ErrFixNotApplicable when no kakapo_Maintenance is there. One kakapo did not create leaves the preview
// without changes.
func BuildRemoveMaintenanceProcFix(f MaintProcFacts, options map[string]string) (FixPreview, Fix, error) {
	if !f.ProcExists && !f.SettingsExists && !f.LogExists {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	blocked := []string{}
	choices := []string{MaintRemoveAll}
	for _, s := range f.Settings {
		choices = append(choices, s.Database)
	}
	what := MaintRemoveAll
	in := FixInput{Name: OptionProcRemove, Label: "Remove", Value: what, Kind: "select", Options: choices,
		Help: "Everything: the procedure, its settings and its log. A database: only that database's row, so the procedure stops serving it."}
	if typed, given := options[OptionProcRemove]; given && strings.TrimSpace(typed) != "" {
		v := strings.TrimSpace(typed)
		found := false
		for _, c := range choices {
			if strings.EqualFold(c, v) {
				what, found = c, true
			}
		}
		if !found {
			in.Value, in.Warning = v, "Pick everything, or one of the databases the procedure serves."
			blocked = append(blocked, in.Warning)
		} else {
			in.Value = what
		}
	}
	if f.ProcExists && !f.ProcOurs {
		blocked = append(blocked, fmt.Sprintf("The procedure master.dbo.kakapo_Maintenance on %s was not created by kakapo: kakapo does not remove it.", orQ(f.ServerName)))
	}
	opts := map[string]string{OptionProcRemove: in.Value}
	fix := suFix(FixRemoveMaintenanceProc)
	fix.Current = fmt.Sprintf("installed, serving %d database(s)", len(f.Settings))
	fix.Target = "none"
	if what != MaintRemoveAll {
		fix.Target = "not serving " + what
	}
	fix.Options = opts
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: []FixInput{in}, Options: opts}
	p.Warnings = append(p.Warnings, blocked...)
	p.Warnings = append(p.Warnings,
		"The backup files stay on disk; delete them by hand when you no longer need them.",
		"The scheduled task stays: delete it by hand on the server ("+MaintSchtasksDelete+"), or it fails every 15 minutes once the procedure is gone.")
	if what == MaintRemoveAll {
		p.Warnings = append(p.Warnings, "The log of what ran (master.dbo.kakapo_MaintenanceLog) is dropped with it; the rollback creates everything again with the settings as they are now, but not the log.")
	}
	p.Warnings = append(p.Warnings, "Needs sysadmin on "+orQ(f.ServerName)+".")
	if len(blocked) > 0 {
		return p, fix, nil
	}
	if what == MaintRemoveAll {
		dbs := []string{}
		for _, s := range f.Settings {
			dbs = append(dbs, s.Database)
		}
		p.Changes = append(p.Changes,
			Drift{Setting: "Procedure master.dbo.kakapo_Maintenance", Current: fmt.Sprintf("version %d", f.ProcVersion), Target: "dropped"},
			Drift{Setting: "Table master.dbo." + MaintSettingsName, Current: orNone(dbs), Target: "dropped"},
			Drift{Setting: "Table master.dbo." + MaintLogName, Current: "exists", Target: "dropped, with what it recorded"},
			Drift{Setting: "Signature", Current: f.Signing.Words(), Target: "the certificate " + MaintCertName + " and its login " + MaintCertLogin + " dropped"})
		if f.Signing.SchedulerUserOurs {
			p.Changes = append(p.Changes, Drift{Setting: MaintSchedulerLogin + " in master", Current: "kakapo's master user, EXECUTE on the procedure", Target: "dropped"})
		} else if f.Signing.SchedulerExecute {
			p.Changes = append(p.Changes, Drift{Setting: MaintSchedulerLogin + " in master", Current: "EXECUTE on the procedure", Target: "gone with the procedure; its master user was not kakapo's and stays"})
		}
		p.Statement = MaintDropStatement()
		p.Rollback = MaintInstallStatement(f.CanCompress(), f.Settings...)
		return p, fix, nil
	}
	row, _ := f.Row(what)
	p.Changes = append(p.Changes, Drift{Setting: "Settings for " + what, Current: maintRowWords(row), Target: "deleted: the procedure stops serving " + what})
	p.Statement = MaintDeleteRowStatement(what)
	p.Rollback = MaintRestoreRowStatement(row)
	return p, fix, nil
}

// MaintRemoveDescription is remove-maintenance-proc's change log line.
func MaintRemoveDescription(server, what string) string {
	if what == MaintRemoveAll || what == "" {
		return "Removed kakapo's maintenance procedure and its tables from master on " + orQ(server) + "; the backup files and the scheduled task stay"
	}
	return "Took " + what + " off kakapo's maintenance procedure on " + orQ(server)
}

// MaintRemoveReadBack says what is left after the remove: verified when nothing of kakapo's is left
// (everything) or the database has no row (one database).
func MaintRemoveReadBack(after MaintProcFacts, what string) (string, bool) {
	if what == MaintRemoveAll || what == "" {
		left := []string{}
		if after.ProcExists {
			left = append(left, "the procedure")
		}
		if after.SettingsExists {
			left = append(left, MaintSettingsName)
		}
		if after.LogExists {
			left = append(left, MaintLogName)
		}
		if after.Signing.CertExists {
			left = append(left, "the certificate "+MaintCertName)
		}
		if after.Signing.CertLogin {
			left = append(left, "the login "+MaintCertLogin)
		}
		if after.Signing.SchedulerUserOurs {
			left = append(left, "kakapo's master user for "+MaintSchedulerLogin)
		}
		if len(left) == 0 {
			return "nothing of kakapo's maintenance procedure left in master", true
		}
		return "still there: " + strings.Join(left, ", "), false
	}
	if _, has := after.Row(what); has {
		return what + " still has a settings row", false
	}
	return fmt.Sprintf("%s has no settings row; the procedure serves %d database(s)", what, len(after.Settings)), true
}
