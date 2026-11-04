package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// The Backups card's remaining fixes (docs/phase4d-contract.md, part 2), all through the fix
// registry: the recovery model (DBC08, BAK02), a weekly integrity check job (BAK03), a CHECKDB run
// now (BAK03, BAK04: a run, not a change) and clearing old suspect-page records after a clean run
// (BAK04).
const (
	FixRecoveryModel     = "recovery-model"
	FixCheckdbJob        = "checkdb-job"
	FixCheckdbNow        = "checkdb-now"
	FixSuspectPagesClear = "suspect-pages-clear"
)

// The inputs of the Backups card's fixes.
const (
	OptionRecoveryModel = "model"
	OptionCheckdbDay    = "day"
	OptionCheckdbTime   = "time"
	OptionPhysicalOnly  = "physicalOnly"
)

// RecoveryModels are the models the recovery-model fix offers.
var RecoveryModels = []string{"FULL", "SIMPLE"}

// CheckdbDays are the days the weekly integrity check may run on, the day select's options, in the
// order of sysschedules' freq_interval bits (Sunday 1 .. Saturday 64).
var CheckdbDays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

const (
	defaultCheckdbDay  = "Sunday"
	defaultCheckdbTime = "01:00"
	// physicalOnlyFromMB: from 200 GB of data the check defaults to WITH PHYSICAL_ONLY.
	physicalOnlyFromMB = 200 * 1024
	// CheckdbRunTimeout bounds a CHECKDB run started from the app.
	CheckdbRunTimeout = 4 * time.Hour
)

// Texts of the Backups card's fixes.
const (
	recoveryModelHelp = "FULL keeps every change in the log until a log backup, so a restore can go back to any minute; SIMPLE empties the log by itself and a restore goes back to the last full or differential backup only."
	recoveryToFull    = "The log grows until it is backed up: create the log backup job (the Backup schedule row) right after this, or the disk fills."
	recoveryChain     = "The log-backup chain starts at the next full backup: until then the log cannot be backed up and a restore still goes back to the last full backup only."
	recoveryToSimple  = "Point-in-time restore is lost; agree the data-loss window with the business (BAK05)."
	checkdbDayHelp    = "The day of the week the check runs, server time."
	checkdbTimeHelp   = "Server time, HH:MM. Pick a quiet hour that does not overlap the full backup."
	physicalOnlyHelp  = "Checks the pages and their checksums only: far quicker and lighter on a large database, but not the logical consistency. Run a full check monthly (or on a restored copy) when you tick this."
	checkdbTimeWarn   = "The time must be HH:MM, for example 01:00."
	checkdbDayWarn    = "Pick a day from Sunday to Saturday."
	physicalOnlyWarn  = "Physical only must be ticked or not (true or false)."
	checkdbLoad       = "Runs online but loads the disk and tempdb; a quiet hour is better."
	checkdbFirstRun   = "How long the check takes is unknown until it has run once: look at the job's history after the first run (MNT02)."
	physicalOnlyNote  = "WITH PHYSICAL_ONLY checks pages and checksums only, not the logical consistency: run a full check monthly, or on a restored copy of the backup."
	checkdbNoOperator = "No operator with an email exists (MNT07): nobody is told when the check fails or finds damage."
	checkdbWait       = "kakapo waits up to 4 hours for the result; keep this page open while it runs."
	suspectNoRollback = "There is no rollback: the records are history only, and the change log keeps them in its result."
	suspectHistory    = "Clearing the records repairs nothing: it only stops BAK04 reporting damage that a clean check has shown is gone."
)

// CheckdbRun is one DBCC CHECKDB run started from the app (checkdb-now), stored in checkdb_run and
// shown on the Backups card: when it started (UTC), how long it took, whether it was physical only,
// and its outcome.
type CheckdbRun struct {
	RanAt           time.Time `json:"ranAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	PhysicalOnly    bool      `json:"physicalOnly"`
	Clean           bool      `json:"clean"`
	Outcome         string    `json:"outcome"`  // "No errors found", or SQL Server's summary of the errors found
	Messages        []string  `json:"messages"` // what SQL Server said, in order; [] when nothing
}

// CheckdbMessage is one message DBCC CHECKDB sent: an error (with its number and severity) or an
// informational line.
type CheckdbMessage struct {
	Number int32
	Class  uint8
	Text   string
	Error  bool
}

// damage25xx are the DBCC errors of the 2500s that report damage (row counts, data purity, index
// rows); the others of that range are about the command itself (2571: no permission, 2520: no such
// database).
var damage25xx = map[int32]bool{2508: true, 2511: true, 2512: true, 2514: true, 2515: true, 2516: true, 2517: true,
	2533: true, 2534: true, 2570: true, 2575: true, 2576: true}

// corruptionError says whether a DBCC error number reports damage: the CHECKDB, CHECKALLOC and
// CHECKTABLE consistency errors (8900s, 7900s, those of the 2500s) and the I/O errors 823 to 825.
func corruptionError(n int32) bool {
	return damage25xx[n] || (n >= 7900 && n <= 7999) || (n >= 8900 && n <= 8999) || (n >= 823 && n <= 825)
}

// CheckdbOutcomeClean is the outcome of a run that found nothing.
const CheckdbOutcomeClean = "No errors found"

// BuildCheckdbRun turns what a CHECKDB run sent into its stored outcome. runErr is how the run
// ended when it did not end cleanly ("" when it did). completed is false when the run did not
// finish its check (cancelled, timed out, refused): it is then not stored and the caller reports
// runErr. A run that found damage completed, with Clean false.
func BuildCheckdbRun(ranAt time.Time, took time.Duration, physicalOnly bool, msgs []CheckdbMessage, runErr string) (CheckdbRun, bool) {
	r := CheckdbRun{RanAt: ranAt.UTC(), DurationSeconds: math.Round(took.Seconds()*1000) / 1000, PhysicalOnly: physicalOnly, Messages: []string{}}
	damage, errorsSeen, summary, first := false, false, "", ""
	for _, m := range msgs {
		text := strings.TrimSpace(m.Text)
		if m.Error {
			errorsSeen = true
			text = fmt.Sprintf("Msg %d, Level %d: %s", m.Number, m.Class, text)
			if first == "" {
				first = strings.TrimSpace(m.Text)
			}
			if corruptionError(m.Number) {
				damage = true
				if m.Number == 8989 || m.Number == 8990 {
					summary = strings.TrimSpace(m.Text)
				}
			}
		}
		if text != "" {
			r.Messages = append(r.Messages, text)
		}
	}
	switch {
	case damage:
		// SQL Server's own summary ("CHECKDB found 0 allocation errors and 2 consistency errors in
		// database 'X'.") says it best; without it, the first error.
		r.Outcome = summary
		if r.Outcome == "" {
			r.Outcome = "CHECKDB found errors: " + first
		}
		return r, true
	case runErr != "" || errorsSeen:
		why := runErr
		if why == "" {
			why = first
		}
		r.Outcome = "Did not complete: " + why
		return r, false
	}
	r.Clean, r.Outcome = true, CheckdbOutcomeClean
	return r, true
}

// CheckdbStatement is the integrity check kakapo runs and schedules on a database.
func CheckdbStatement(db string, physicalOnly bool) string {
	s := "DBCC CHECKDB (" + QuoteName(db) + ") WITH NO_INFOMSGS, ALL_ERRORMSGS"
	if physicalOnly {
		s += ", PHYSICAL_ONLY"
	}
	return s + ";"
}

// checkWords names a check: "full check" or "physical only".
func checkWords(physicalOnly bool) string {
	if physicalOnly {
		return "physical only"
	}
	return "full check"
}

// CheckdbRunChangeDescription is the change log line of a CHECKDB run.
func CheckdbRunChangeDescription(db string, r CheckdbRun) string {
	return fmt.Sprintf("DBCC CHECKDB on %s (%s, %s): %s", db, checkWords(r.PhysicalOnly), durationWords(r.DurationSeconds), r.Outcome)
}

// durationWords writes a run's length: "42 s", "12 min", "1 h 05 min".
func durationWords(seconds float64) string {
	s := int(math.Round(seconds))
	switch {
	case s < 120:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min", (s+30)/60)
	}
	m := (s + 30) / 60
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

// CleanRunCovers says whether a stored clean CHECKDB run took place after the latest suspect-page
// record. suspectLast is in the server's clock; offsetMinutes is the server's offset from UTC.
func CleanRunCovers(run *CheckdbRun, suspectLast *time.Time, offsetMinutes int) bool {
	if run == nil || !run.Clean {
		return false
	}
	if suspectLast == nil {
		return true
	}
	last := suspectLast.Add(-time.Duration(offsetMinutes) * time.Minute)
	return run.RanAt.After(last)
}

// suspectPagesFixID is the fix BAK04 carries: clearing the records once a clean CHECKDB ran after
// the latest of them, else running CHECKDB now.
func (f BackupFacts) suspectPagesFixID() string {
	if CleanRunCovers(f.LastCheckdbRun, f.SuspectLast, f.UTCOffsetMinutes) {
		return FixSuspectPagesClear
	}
	return FixCheckdbNow
}

// offersCheckdbJob says whether BAK03 can offer the weekly integrity check job: the jobs were read
// (so kakapo can tell Express and the jobs that exist) and the database is not a secondary replica.
func (f BackupFacts) offersCheckdbJob() bool {
	return f.JobsRead && f.JobsError == "" && !f.IsSecondary
}

// backupsFindings are the Backups card's findings on this read, with their fixes.
func backupsFindings(f BackupFacts) []Finding {
	def, _ := FindCardDef(CardBackups)
	_, fs, _ := BuildBackupsCard(f, def)
	return fs
}

func findingByID(fs []Finding, id string) (Finding, bool) {
	for _, x := range fs {
		if x.CheckID == id {
			return x, true
		}
	}
	return Finding{}, false
}

// --- recovery-model ---

// otherRecoveryModel is the model the fix suggests: FULL from SIMPLE, SIMPLE otherwise.
func otherRecoveryModel(current string) string {
	if current == "SIMPLE" {
		return "FULL"
	}
	return "SIMPLE"
}

// BuildRecoveryModelFix builds the preview of "Change the recovery model" from the backup facts and
// the model option (default: the other one). readOnly says the database is read-only, which cannot
// change its recovery model. ErrFixNotApplicable when neither DBC08 nor BAK02 fired. The same model
// as now, an unknown model, a read-only database or SIMPLE on an availability group database give
// the warning and no changes.
func BuildRecoveryModelFix(f BackupFacts, readOnly bool, options map[string]string) (FixPreview, Fix, error) {
	fs := backupsFindings(f)
	finding, ok := findingByID(fs, "DBC08")
	if !ok {
		if finding, ok = findingByID(fs, "BAK02"); !ok {
			return FixPreview{}, Fix{}, ErrFixNotApplicable
		}
	}
	d, _ := FindFixDef(FixRecoveryModel)
	current := f.RecoveryModel
	target := otherRecoveryModel(current)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: finding.CheckID, Setting: d.Setting, Current: current}
	p := FixPreview{Database: f.Database, EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}

	usable := true
	in := FixInput{Name: OptionRecoveryModel, Label: "Recovery model", Help: recoveryModelHelp, Kind: "select", Options: RecoveryModels, Value: target}
	if typed, given := options[OptionRecoveryModel]; given {
		v := strings.ToUpper(strings.TrimSpace(typed))
		switch {
		case v != "FULL" && v != "SIMPLE":
			in.Value, in.Warning = strings.TrimSpace(typed), "Pick FULL or SIMPLE."
		case v == current:
			in.Value, in.Warning = v, fmt.Sprintf("%s already uses the %s recovery model: pick the other one.", f.Database, v)
		default:
			in.Value = v
		}
		if in.Warning != "" {
			p.Warnings = append(p.Warnings, in.Warning)
			usable = false
		} else {
			target = v
		}
	}
	p.Inputs = []FixInput{in}
	p.Options[OptionRecoveryModel] = in.Value
	fix.Target = target

	if readOnly {
		p.Warnings = append(p.Warnings, f.Database+" is read-only, so its recovery model cannot be changed.")
		usable = false
	}
	if target == "SIMPLE" && f.IsAgMember {
		p.Warnings = append(p.Warnings, f.Database+" is in an availability group, which needs FULL recovery: it stays in FULL.")
		usable = false
	}
	switch target {
	case "FULL":
		p.Warnings = append(p.Warnings, recoveryToFull, recoveryChain)
		if j, ok := f.CoveringJob(JobLog); ok {
			p.Warnings = append(p.Warnings, "The job "+j.Name+" already takes log backups of this database: check it runs (MNT02).")
		}
	case "SIMPLE":
		p.Warnings = append(p.Warnings, recoveryToSimple)
		if j, ok := f.CoveringJob(JobLog); ok {
			p.Warnings = append(p.Warnings, "The job "+j.Name+" takes log backups of this database: they fail in SIMPLE recovery, so disable or change it (kakapo's own: Remove on the Backup schedule row).")
		}
	}
	p.Warnings = append(p.Warnings, "Takes effect at once and online.", permission(ScopeDatabase, f.Database))
	if !usable {
		return p, fix, nil
	}
	db := QuoteName(f.Database)
	p.Changes = append(p.Changes, Drift{Setting: "Recovery model", Current: current, Target: target})
	p.Statement = "ALTER DATABASE " + db + " SET RECOVERY " + target + ";"
	if sqlWord.MatchString(current) && current != "" {
		p.Rollback = "ALTER DATABASE " + db + " SET RECOVERY " + current + ";"
	}
	return p, fix, nil
}

// RecoveryModelReadBack says what recovery_model_desc reads after the fix, and whether it is the target.
func RecoveryModelReadBack(after, target string) (string, bool) {
	return "recovery model " + after, strings.EqualFold(after, target)
}

// --- checkdb-job ---

// IntegrityJob is an Agent job with a step that runs DBCC CHECKDB on this database (or every
// database) or Ola Hallengren's DatabaseIntegrityCheck.
type IntegrityJob struct {
	Name    string
	Enabled bool
}

// CheckdbJobFacts is what the integrity check fixes read besides the backup facts: the database's
// data size (for the PHYSICAL_ONLY default) and the jobs that already check it.
type CheckdbJobFacts struct {
	DataMB    *int64 // the data files' allocated size (sys.master_files) in MB; nil when not read
	DataError string
	Jobs      []IntegrityJob // enabled or not, kakapo's own included
	JobsError string
}

// CheckdbJobName is the job kakapo creates: "kakapo: {db} integrity check".
func CheckdbJobName(db string) string { return "kakapo: " + db + " integrity check" }

// defaultPhysicalOnly is true when the data is 200 GB or more.
func defaultPhysicalOnly(dataMB *int64) bool { return dataMB != nil && *dataMB >= physicalOnlyFromMB }

// checkdbDayBit is a day's bit in sysschedules freq_interval for a weekly schedule; 0 for an unknown day.
func checkdbDayBit(day string) int {
	for i, d := range CheckdbDays {
		if strings.EqualFold(d, day) {
			return 1 << i
		}
	}
	return 0
}

// checkdbDay reads the day option: one of CheckdbDays, as spelled there.
func checkdbDay(v string) (string, bool) {
	for _, d := range CheckdbDays {
		if strings.EqualFold(d, strings.TrimSpace(v)) {
			return d, true
		}
	}
	return "", false
}

// CheckdbJob is the weekly integrity check job kakapo creates for one database.
type CheckdbJob struct {
	Database     string
	Server       string
	Date         string // yyyy-mm-dd, the server's date, for the description
	Day          string // Sunday .. Saturday
	Time         string // HH:MM, server time
	PhysicalOnly bool
	Operator     string // notified by email on failure; "" no one
	Express      bool   // no Agent: a script to schedule by hand
}

// Name is the job's name.
func (j CheckdbJob) Name() string { return CheckdbJobName(j.Database) }

// when is the schedule in words: "weekly on Sunday at 01:00".
func (j CheckdbJob) when() string { return "weekly on " + j.Day + " at " + j.Time }

// command is the job step's T-SQL, tagged.
func (j CheckdbJob) command() string {
	return JobStepTag + "\n" + CheckdbStatement(j.Database, j.PhysicalOnly)
}

// Batch is the statement that creates the job (the shared rule: owner sa, Database Maintenance, a
// description, a tagged step, one transaction refusing an existing job of that name).
func (j CheckdbJob) Batch() string {
	name := j.Name()
	var b strings.Builder
	agentJobAdd(&b, name, "Created by kakapo on "+j.Date+" for "+j.Database+": integrity check "+j.when()+", "+checkWords(j.PhysicalOnly), j.Operator)
	agentJobStep(&b, name, 1, "Check integrity", j.command(), true)
	b.WriteString("    EXEC msdb.dbo.sp_add_jobschedule @job_name = " + nstr(name) + ", @name = " + nstr(name+", "+j.when()) + ", @enabled = 1,\n")
	fmt.Fprintf(&b, "        @freq_type = 8, @freq_interval = %d, @freq_recurrence_factor = 1, @active_start_time = %d;\n",
		checkdbDayBit(j.Day), agentStartTime(j.Time))
	agentJobServer(&b, name)
	return agentJobBatch([]string{name}, b.String())
}

// Rollback deletes the job.
func (j CheckdbJob) Rollback() string { return agentDeleteJobs([]string{j.Name()}) }

// Script is what Express gets: the check to schedule in Windows Task Scheduler.
func (j CheckdbJob) Script() string {
	file := "checkdb-" + fileSafe(j.Database) + ".sql"
	return fmt.Sprintf("-- kakapo: integrity check of %s on %s. SQL Server Express has no SQL Server Agent: schedule this in Windows Task Scheduler.\n"+
		"-- Save it as %s and run it %s: sqlcmd -S %s -E -b -i %s\n%s\nGO",
		j.Database, j.Server, file, j.when(), j.Server, file, j.command())
}

// Description is the change log line of the fix.
func (j CheckdbJob) Description() string {
	return "Created the job " + j.Name() + " (" + j.when() + ", " + checkWords(j.PhysicalOnly) + ")"
}

// expectedNextRun is when the job runs next by its schedule, in the server's clock.
func (j CheckdbJob) expectedNextRun(now time.Time) time.Time {
	at := agentStartTime(j.Time)
	t := time.Date(now.Year(), now.Month(), now.Day(), at/10000, at/100%100, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		if CheckdbDays[int(t.Weekday())] == j.Day && t.After(now) {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// ReadBack says what the read-back found after the job was created, and whether it exists and is enabled.
func (j CheckdbJob) ReadBack(states []BackupJobState, now time.Time) (string, bool) {
	name := j.Name()
	for _, st := range states {
		if st.Name != name {
			continue
		}
		text, verified := name+": enabled", true
		if !st.Enabled {
			text, verified = name+": disabled", false
		}
		if !st.ScheduleEnabled {
			text += ", schedule disabled"
		}
		if st.NextRun != nil {
			text += ", next run " + sqlTime(*st.NextRun)
		} else {
			text += ", next run " + sqlTime(j.expectedNextRun(now)) + " (expected; Agent has not set it yet)"
		}
		return text + ".", verified
	}
	return name + ": not found.", false
}

// roomWarning is what the last CHECKDB room estimate (STO10) says for a check, or the advice to
// run it first.
func roomWarning(room *CheckdbRoom, physicalOnly bool) string {
	if room == nil {
		return "kakapo does not know yet whether tempdb and the data volume have room for CHECKDB: run Check room for CHECKDB on the Storage card first (STO10)."
	}
	s := "Room for CHECKDB (STO10, " + room.CapturedAt.UTC().Format("2006-01-02 15:04") + " UTC): " + room.Finding.Finding
	if room.Finding.Severity.Actionable() && physicalOnly {
		s += " WITH PHYSICAL_ONLY needs far less."
	}
	return s
}

// physicalOnlyInput is the bool input of the integrity check fixes, with the default from the data
// size. ok is false for a value that is neither true nor false (the input then carries the warning).
func physicalOnlyInput(options map[string]string, dataMB *int64) (in FixInput, v, ok bool) {
	v, ok = defaultPhysicalOnly(dataMB), true
	help := physicalOnlyHelp
	if dataMB != nil {
		help = fmt.Sprintf("%s The data files hold %s; the default is ticked from 200 GB.", physicalOnlyHelp, mbText(*dataMB))
	}
	in = FixInput{Name: OptionPhysicalOnly, Label: "Physical only (WITH PHYSICAL_ONLY)", Help: help, Kind: "bool"}
	if typed, given := options[OptionPhysicalOnly]; given {
		switch t := strings.ToLower(strings.TrimSpace(typed)); t {
		case "true", "false":
			v = t == "true"
		default:
			in.Value, in.Warning = strings.TrimSpace(typed), physicalOnlyWarn
			return in, false, false
		}
	}
	in.Value = fmt.Sprint(v)
	return in, v, true
}

// BuildCheckdbJobFix builds the preview of "Create the weekly integrity check" from the backup
// facts, the integrity facts, the last CHECKDB room estimate (nil when never run) and the options
// (day, time, physicalOnly). On Express the statement is a script to schedule by hand.
// ErrFixNotApplicable when BAK03 does not carry the fix. A bad day or time, or kakapo's job already
// there, gives the warning and no changes.
func BuildCheckdbJobFix(f BackupFacts, jf CheckdbJobFacts, room *CheckdbRoom, options map[string]string) (FixPreview, Fix, error) {
	if x, ok := findingByID(backupsFindings(f), "BAK03"); !ok || x.FixID != FixCheckdbJob {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	d, _ := FindFixDef(FixCheckdbJob)
	j := CheckdbJob{Database: f.Database, Server: f.ServerName, Date: f.ServerNow.Format("2006-01-02"),
		Day: defaultCheckdbDay, Time: defaultCheckdbTime, Operator: f.Operator, Express: !f.HasAgent}
	if j.Express {
		j.Operator = ""
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting, Current: "no integrity check job"}
	p := FixPreview{Database: f.Database, EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}, Express: j.Express}

	usable := true
	day := FixInput{Name: OptionCheckdbDay, Label: "Day", Help: checkdbDayHelp, Kind: "select", Options: CheckdbDays, Value: j.Day}
	if typed, given := options[OptionCheckdbDay]; given {
		if v, ok := checkdbDay(typed); ok {
			j.Day, day.Value = v, v
		} else {
			day.Value, day.Warning = strings.TrimSpace(typed), checkdbDayWarn
			p.Warnings = append(p.Warnings, checkdbDayWarn)
			usable = false
		}
	}
	clock := FixInput{Name: OptionCheckdbTime, Label: "Time", Help: checkdbTimeHelp, Kind: "text", Value: j.Time}
	if typed, given := options[OptionCheckdbTime]; given {
		if v, ok := parseClock(typed); ok {
			j.Time, clock.Value = v, v
		} else {
			clock.Value, clock.Warning = strings.TrimSpace(typed), checkdbTimeWarn
			p.Warnings = append(p.Warnings, checkdbTimeWarn)
			usable = false
		}
	}
	phys, v, ok := physicalOnlyInput(options, jf.DataMB)
	j.PhysicalOnly = v
	if !ok {
		p.Warnings = append(p.Warnings, physicalOnlyWarn)
		usable = false
	}
	p.Inputs = []FixInput{day, clock, phys}
	for _, in := range p.Inputs {
		p.Options[in.Name] = in.Value
	}
	fix.Target = j.Name() + " (" + j.when() + ", " + checkWords(j.PhysicalOnly) + ")"

	if jf.JobsError != "" {
		p.Warnings = append(p.Warnings, "Could not read which jobs already run CHECKDB: "+jf.JobsError)
	}
	for _, x := range jf.Jobs {
		switch {
		case x.Name == j.Name():
			p.Warnings = append(p.Warnings, "The job "+x.Name+" already exists: kakapo created it earlier. Check why it has not succeeded in its history (MNT02) instead of creating it again.")
			usable = false
		case x.Enabled:
			p.Warnings = append(p.Warnings, "The job "+x.Name+" already runs an integrity check here: check why it has not succeeded (MNT02) before adding a second one.")
		}
	}
	if j.Express {
		p.Warnings = append(p.Warnings, "SQL Server Express has no Agent: kakapo cannot create the job. Copy the script and schedule it in Windows Task Scheduler.")
	} else if f.AgentError == "" && !f.AgentRunning {
		status := f.AgentStatus
		if status == "" {
			status = "not running"
		}
		p.Warnings = append(p.Warnings, "SQL Server Agent is stopped ("+status+"): the job will not run until it starts (MNT01).")
	}
	p.Warnings = append(p.Warnings, roomWarning(room, j.PhysicalOnly), checkdbFirstRun)
	if j.PhysicalOnly {
		p.Warnings = append(p.Warnings, physicalOnlyNote)
	}
	if !j.Express && j.Operator == "" {
		p.Warnings = append(p.Warnings, checkdbNoOperator)
	}
	if j.Express {
		p.Warnings = append(p.Warnings, "Needs sysadmin, or db_owner of "+f.Database+", for the Windows account that runs the scheduled task.")
	} else {
		p.Warnings = append(p.Warnings, BackupFixPermission(f.ServerName))
	}
	if !usable {
		return p, fix, nil
	}

	told := noOperator
	switch {
	case j.Express:
		told = "no one (Express has no Agent to send an email)"
	case j.Operator != "":
		told = j.Operator
	}
	p.Changes = []Drift{
		{Setting: "Integrity check job", Current: "none", Target: j.Name() + ", " + j.when()},
		{Setting: "Check", Target: CheckdbStatement(j.Database, j.PhysicalOnly)},
		{Setting: "Tell on failure", Target: told},
	}
	if j.Express {
		p.Statement = j.Script()
	} else {
		p.Statement, p.Rollback = j.Batch(), j.Rollback()
	}
	return p, fix, nil
}

// CheckdbJobFromOptions is the job a checkdb-job preview was built for, from the facts and the
// options it echoed.
func CheckdbJobFromOptions(f BackupFacts, options map[string]string) CheckdbJob {
	j := CheckdbJob{Database: f.Database, Server: f.ServerName, Date: f.ServerNow.Format("2006-01-02"),
		Day: defaultCheckdbDay, Time: defaultCheckdbTime, Operator: f.Operator, Express: !f.HasAgent}
	if v, ok := checkdbDay(options[OptionCheckdbDay]); ok {
		j.Day = v
	}
	if v, ok := parseClock(options[OptionCheckdbTime]); ok {
		j.Time = v
	}
	j.PhysicalOnly = strings.EqualFold(strings.TrimSpace(options[OptionPhysicalOnly]), "true")
	return j
}

// --- checkdb-now ---

// lastGoodWords says when the last good CHECKDB was: "2026-10-14 09:21", "never", "unknown".
func lastGoodWords(f BackupFacts) string {
	switch {
	case f.LastGoodCheckDb == nil:
		return "unknown"
	case checkDbNever(*f.LastGoodCheckDb):
		return "never"
	}
	return sqlTime(*f.LastGoodCheckDb)
}

// BuildCheckdbNowFix builds the preview of "Run DBCC CHECKDB now": a run, not a change, with no
// rollback. dataMB is the data size (the PHYSICAL_ONLY default), room the last STO10 estimate (nil
// when never run). It is offered whatever the findings say: a check is always safe to run.
func BuildCheckdbNowFix(f BackupFacts, dataMB *int64, room *CheckdbRoom, options map[string]string) (FixPreview, Fix) {
	d, _ := FindFixDef(FixCheckdbNow)
	checkID := d.CheckID
	fs := backupsFindings(f)
	if _, ok := findingByID(fs, "BAK03"); !ok {
		if _, ok := findingByID(fs, "BAK04"); ok {
			checkID = "BAK04"
		}
	}
	phys, physicalOnly, ok := physicalOnlyInput(options, dataMB)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: checkID, Setting: d.Setting,
		Current: "last good check: " + lastGoodWords(f), Target: "run now (" + checkWords(physicalOnly) + ")"}
	p := FixPreview{Database: f.Database, EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{},
		Inputs: []FixInput{phys}, Options: map[string]string{OptionPhysicalOnly: phys.Value}}
	if !ok {
		p.Warnings = append(p.Warnings, physicalOnlyWarn)
		return p, fix
	}
	p.Changes = []Drift{{Setting: "Integrity check of " + f.Database, Current: "last good: " + lastGoodWords(f), Target: "run now, " + checkWords(physicalOnly)}}
	p.Statement = CheckdbStatement(f.Database, physicalOnly)
	p.Warnings = append(p.Warnings, roomWarning(room, physicalOnly), checkdbLoad, checkdbWait)
	if physicalOnly {
		p.Warnings = append(p.Warnings, physicalOnlyNote)
	}
	if f.IsSecondary {
		p.Warnings = append(p.Warnings, "This is a secondary replica: the check covers this copy only; run it on the primary too.")
	}
	p.Warnings = append(p.Warnings, "Changes nothing: the result is stored and shown on the Backups card.", "Needs db_owner of "+f.Database+" or sysadmin.")
	return p, fix
}

// PhysicalOnlyOption reads the physicalOnly option a preview echoed.
func PhysicalOnlyOption(options map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(options[OptionPhysicalOnly]), "true")
}

// --- suspect-pages-clear ---

// SuspectPage is one row of msdb.dbo.suspect_pages for this database; LastUpdate in the server's clock.
type SuspectPage struct {
	FileID     int
	PageID     int64
	EventType  int
	ErrorCount int
	LastUpdate time.Time
}

// suspectEventWords says what an event_type means.
func suspectEventWords(t int) string {
	switch t {
	case 1:
		return "823/824 error"
	case 2:
		return "bad checksum"
	case 3:
		return "torn page"
	case 4:
		return "restored"
	case 5:
		return "repaired"
	case 7:
		return "deallocated by DBCC"
	}
	return "event " + strconv.Itoa(t)
}

// pageWords names a suspect page and what was recorded.
func (s SuspectPage) words() string {
	return fmt.Sprintf("file %d page %d (%s, %d error(s), last %s)", s.FileID, s.PageID, suspectEventWords(s.EventType), s.ErrorCount, sqlTime(s.LastUpdate))
}

// BuildSuspectPagesClearFix builds the preview of "Clear the old suspect-page records" from the
// backup facts (with the last CHECKDB run) and the records read now. ErrFixNotApplicable when there
// are none. Without a clean CHECKDB run stored after the latest record, the preview refuses: the
// warning and no changes.
func BuildSuspectPagesClearFix(f BackupFacts, pages []SuspectPage) (FixPreview, Fix, error) {
	if len(pages) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	d, _ := FindFixDef(FixSuspectPagesClear)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: fmt.Sprintf("%d record(s)", len(pages)), Target: "none"}
	p := FixPreview{EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}}
	var latest *time.Time
	for i := range pages {
		if latest == nil || pages[i].LastUpdate.After(*latest) {
			latest = &pages[i].LastUpdate
		}
	}
	run := f.LastCheckdbRun
	switch {
	case run == nil:
		p.Warnings = append(p.Warnings, "Run DBCC CHECKDB on "+f.Database+" first (Run CHECKDB now): kakapo clears the records only after a clean check.")
	case !run.Clean:
		p.Warnings = append(p.Warnings, "The last CHECKDB on "+f.Database+" ("+run.RanAt.Format("2006-01-02 15:04")+" UTC) did not come back clean: "+run.Outcome+". The damage is real: escalate (BAK04), do not clear the records.")
	case !CleanRunCovers(run, latest, f.UTCOffsetMinutes):
		p.Warnings = append(p.Warnings, "The last clean CHECKDB ("+run.RanAt.Format("2006-01-02 15:04")+" UTC) ran before the latest record ("+sqlTime(*latest)+" server time): run it again first.")
	}
	usable := len(p.Warnings) == 0
	p.Warnings = append(p.Warnings, suspectHistory, suspectNoRollback, "Needs DELETE on msdb.dbo.suspect_pages (sysadmin, or db_owner of msdb).")
	if !usable {
		return p, fix, nil
	}
	for _, s := range pages {
		p.Changes = append(p.Changes, Drift{Setting: fmt.Sprintf("File %d, page %d", s.FileID, s.PageID),
			Current: fmt.Sprintf("%s, %d error(s), last %s", suspectEventWords(s.EventType), s.ErrorCount, sqlTime(s.LastUpdate)), Target: "cleared"})
	}
	p.Statement = "DELETE msdb.dbo.suspect_pages WHERE database_id = DB_ID(" + nstr(f.Database) + ");"
	return p, fix, nil
}

// SuspectPagesReadBack is the change row's result of the clear: the records removed (kept here for
// the record, as there is no rollback) and what is left, verified when none is.
func SuspectPagesReadBack(removed, remaining []SuspectPage) (string, bool) {
	words := []string{}
	for _, s := range removed {
		words = append(words, s.words())
	}
	out := fmt.Sprintf("Removed %d record(s): %s.", len(removed), strings.Join(words, "; "))
	if len(remaining) == 0 {
		return out + " None left.", true
	}
	return out + fmt.Sprintf(" Still there: %d record(s).", len(remaining)), false
}

// SuspectPagesChangeDescription is the change log line of the clear.
func SuspectPagesChangeDescription(db string, n int) string {
	return fmt.Sprintf("Cleared %d suspect-page record(s) of %s from msdb.dbo.suspect_pages after a clean CHECKDB", n, db)
}

// availableBackupActionFixes are the Backups card's phase 4d fixes on this read: the recovery model
// when DBC08 fired, the integrity check job when BAK03 carries it, CHECKDB now when BAK03 or BAK04
// fired, and the suspect-page clear when BAK04 carries it.
func availableBackupActionFixes(f BackupFacts, findings []Finding) []Fix {
	out := []Fix{}
	for _, id := range []string{FixRecoveryModel, FixCheckdbJob, FixCheckdbNow, FixSuspectPagesClear} {
		d, _ := FindFixDef(id)
		fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting}
		switch id {
		case FixRecoveryModel:
			x, ok := findingByID(findings, "DBC08")
			if !ok || x.FixID != FixRecoveryModel {
				continue
			}
			fix.CheckID, fix.Current, fix.Target = x.CheckID, f.RecoveryModel, otherRecoveryModel(f.RecoveryModel)
		case FixCheckdbJob:
			if _, ok := hasFix(findings, FixCheckdbJob); !ok {
				continue
			}
			fix.Current, fix.Target = "no integrity check job", CheckdbJobName(f.Database)+" (weekly on "+defaultCheckdbDay+" at "+defaultCheckdbTime+")"
		case FixCheckdbNow:
			b3, ok3 := findingByID(findings, "BAK03")
			_, ok4 := findingByID(findings, "BAK04")
			if !ok3 && !ok4 {
				continue
			}
			if !ok3 {
				fix.CheckID = "BAK04"
			} else {
				fix.CheckID = b3.CheckID
			}
			fix.Current, fix.Target = "last good check: "+lastGoodWords(f), "run now"
		case FixSuspectPagesClear:
			if _, ok := hasFix(findings, FixSuspectPagesClear); !ok {
				continue
			}
			fix.Current, fix.Target = fmt.Sprintf("%d record(s)", f.SuspectPages), "none"
		}
		out = append(out, fix)
	}
	return out
}
