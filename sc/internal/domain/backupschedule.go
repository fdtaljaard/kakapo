package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The backup schedule fix (docs/phase3g-contract.md, SCOPE section 3b): the first fix that creates
// something on the server, SQL Server Agent jobs, and its remove fix.
const (
	FixBackupSchedule       = "backup-schedule"
	FixRemoveBackupSchedule = "remove-backup-schedule"
)

// JobStepTag starts every job step command kakapo writes: the tag collect.Tag puts on every batch
// (collect.KakapoTag; a collect test checks they agree), so kakapo's own backups are recognisable
// in the plan cache and Query Store reads too.
const JobStepTag = "/* kakapo */"

// Kinds of backup job.
const (
	JobFull = "full"
	JobLog  = "log"
	JobBoth = "both"
)

// BackupJob is one Agent job, enabled or not, with a step that backs up this database or all user
// databases (BACKUP DATABASE, BACKUP LOG or Ola Hallengren's DatabaseBackup).
type BackupJob struct {
	Name    string
	Enabled bool
	Kind    string // JobFull, JobLog, JobBoth; "" when the commands say neither (a differential only)
}

// Covers says whether the job takes backups of a kind (JobFull or JobLog).
func (j BackupJob) Covers(kind string) bool { return j.Kind == kind || j.Kind == JobBoth }

// CoveringJob is the first enabled job that takes backups of a kind (JobFull or JobLog).
func (f BackupFacts) CoveringJob(kind string) (BackupJob, bool) {
	for _, j := range f.BackupJobs {
		if j.Enabled && j.Covers(kind) {
			return j, true
		}
	}
	return BackupJob{}, false
}

// BackupJobName is the name of the job kakapo creates for a database: "kakapo: {db} full backup".
func BackupJobName(db, kind string) string { return "kakapo: " + db + " " + kind + " backup" }

// The inputs of the backup schedule fix (docs/phase3g-contract.md, docs/phase4b-contract.md).
const (
	OptionFolder     = "folder"
	OptionFullTime   = "fullTime"
	OptionLogMinutes = "logMinutes"
	OptionKeepDays   = "keepDays"
	OptionCompress   = "compress"
	OptionRunNow     = "runNow"
)

// The defaults of the schedule's settings.
const (
	DefaultFullTime   = "22:00"
	DefaultLogMinutes = 15
	DefaultKeepDays   = 7
	maxKeepDays       = 60
)

// LogMinuteChoices are the intervals the log backup job may run at, the logMinutes select's options.
var LogMinuteChoices = []string{"5", "10", "15", "30", "60"}

// Texts of the fix (docs/phase3g-contract.md, docs/phase4b-contract.md).
const (
	jobCoversDo = "The job %s should be doing this; check why it has not produced a backup (MNT02)."
	expressDo   = "SQL Server Express has no Agent: kakapo cannot schedule the backup here; see the script in the preview."
	// Why a finding has no Apply for the schedule, or why the one it has will be refused.
	noScheduleJobs      = "kakapo cannot create the schedule: it could not read the Agent jobs in msdb (%s). Connect with a sysadmin login."
	noScheduleSecondary = "This database is a secondary replica: schedule the backups on the primary."
	scheduleNotSysadmin = "kakapo's login is not sysadmin on %s, so Apply for the schedule will be refused: connect with a sysadmin login."
	logNotNeeded        = "not needed (SIMPLE recovery; DBC08 decides)"
	noOperator          = "no one (MNT07: no operator with an email)"
	folderHelp          = "A folder on the database server, as the SQL Server service sees it: a local drive (D:\\Backup) or a share (\\\\nas\\sql). kakapo makes a folder for the database inside it."
	runNowHelp          = "Start the full backup job as soon as the jobs are created, so the database has a backup now rather than tonight."
	fullTimeHelp        = "Daily, server time. Pick a quiet hour after the day's work and before any overnight job that reads the database."
	logMinutesHelp      = "How much work you could lose. 15 minutes is the usual answer; 5 for a busy site."
	keepDaysHelp        = "Files older than this are deleted by the job. Copies kept elsewhere are not affected."
	compressHelp        = "About half the size and faster to write; costs CPU while the backup runs."
	fullTimeWarn        = "The full backup time must be HH:MM, for example 22:00."
	logMinutesWarn      = "The log backup must run every 5, 10, 15, 30 or 60 minutes."
	keepDaysWarn        = "Keep between 1 and 60 days."
)

// scheduleFix decides what a BAK01 (kind JobFull) or BAK02 (JobLog) finding offers: the backup
// schedule fix when kakapo can create the missing job, and its Do this. A job that already covers
// the kind should be producing the backups, so the Do this points at it and no fix is offered.
// Facts without the jobs (or with an error reading them) and a secondary replica offer nothing.
// On Express the fix is offered as a script to run by hand.
func (f BackupFacts) scheduleFix(kind, doThis string) (string, string) {
	if f.JobsError != "" {
		// The usual cause is a login that is not sysadmin (and has no read access to msdb): say so, so the
		// missing Apply is not a mystery.
		return "", doThis + " " + fmt.Sprintf(noScheduleJobs, f.JobsError)
	}
	if !f.JobsRead {
		return "", doThis
	}
	if f.HasAgent {
		if j, ok := f.CoveringJob(kind); ok {
			return "", fmt.Sprintf(jobCoversDo, j.Name)
		}
	}
	if f.IsSecondary {
		return "", doThis + " " + noScheduleSecondary
	}
	if !f.HasAgent {
		return FixBackupSchedule, doThis + " " + expressDo
	}
	if f.PermsRead && !f.IsSysadmin {
		return FixBackupSchedule, doThis + " " + fmt.Sprintf(scheduleNotSysadmin, f.ServerName)
	}
	return FixBackupSchedule, doThis
}

// BackupSchedule is what the backup schedule fix creates for one database, from the facts and the
// options the user set in the preview.
type BackupSchedule struct {
	Database string
	Server   string
	Folder   string // as normalised: no trailing separator except on a bare drive (D:\)
	Full     bool   // create the full backup job (no enabled job takes full backups yet)
	Log      bool   // create the log backup job (FULL or BULK_LOGGED, no enabled job takes log backups yet)
	Ola      bool   // call DatabaseBackup instead of native BACKUP statements
	Compress bool
	Operator string // the operator the jobs notify on failure; "" no one
	Date     string // the server's date, yyyy-mm-dd, for the job description
	RunNow   bool   // start the full backup job after creating it
	Express  bool   // no Agent: a script to run by hand
	// The settings the user chose (docs/phase4b-contract.md): the full backup's daily time (HH:MM,
	// server time), the log backup's interval in minutes, and how many days of files are kept.
	FullTime   string
	LogMinutes int
	KeepDays   int
	// logCovered: no log job is created because an enabled one already takes log backups (not
	// because the database is in SIMPLE recovery).
	logCovered bool
}

// NewBackupSchedule is the schedule the fix would create from what was read and the options the
// preview was built with (folder: the default backup directory when not given; runNow and compress:
// true when not given; the time, interval and days: the defaults when not given or not valid, the
// preview says which is not). On Express the settings are the defaults: there are no inputs there.
func NewBackupSchedule(f BackupFacts, options map[string]string) BackupSchedule {
	s := BackupSchedule{
		Database: f.Database, Server: f.ServerName, Folder: BackupScheduleFolder(f, options),
		Ola: f.OlaBackup, Compress: f.CompressionOK, Operator: f.Operator,
		Date: f.ServerNow.Format("2006-01-02"), Express: !f.HasAgent,
		FullTime: DefaultFullTime, LogMinutes: DefaultLogMinutes, KeepDays: DefaultKeepDays,
	}
	_, fullCovered := f.CoveringJob(JobFull)
	_, logCovered := f.CoveringJob(JobLog)
	if s.Express {
		fullCovered, logCovered = false, false
		s.Ola, s.Operator = false, ""
	}
	s.Full = !fullCovered
	s.Log = fullOrBulk(f.RecoveryModel) && !logCovered
	s.logCovered = fullOrBulk(f.RecoveryModel) && logCovered
	s.RunNow = s.Full && !s.Express && boolOption(options, OptionRunNow)
	if !s.Express {
		if v, ok := parseClock(options[OptionFullTime]); ok {
			s.FullTime = v
		}
		if v, ok := parseLogMinutes(options[OptionLogMinutes]); ok {
			s.LogMinutes = v
		}
		if v, ok := parseKeepDays(options[OptionKeepDays]); ok {
			s.KeepDays = v
		}
		s.Compress = s.Compress && boolOption(options, OptionCompress)
	}
	return s
}

// parseClock reads a 24-hour time, H:MM or HH:MM, as HH:MM.
func parseClock(v string) (string, bool) {
	h, m, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok || len(h) < 1 || len(h) > 2 || len(m) != 2 {
		return "", false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || !allDigits(h) || !allDigits(m) || hh > 23 || mm > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", hh, mm), true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// parseLogMinutes reads the log backup interval: one of LogMinuteChoices.
func parseLogMinutes(v string) (int, bool) {
	v = strings.TrimSpace(v)
	for _, c := range LogMinuteChoices {
		if v == c {
			n, _ := strconv.Atoi(c)
			return n, true
		}
	}
	return 0, false
}

// parseKeepDays reads the days of files kept: a whole number from 1 to 60.
func parseKeepDays(v string) (int, bool) {
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil || !allDigits(v) || n < 1 || n > maxKeepDays {
		return 0, false
	}
	return n, true
}

// AgentClock is a sysschedules active_start_time (HHMMSS as a number) as HH:MM.
func AgentClock(hhmmss int) string { return fmt.Sprintf("%02d:%02d", hhmmss/10000, hhmmss/100%100) }

// agentStartTime is an HH:MM time as sysschedules active_start_time (HHMM00).
func agentStartTime(clock string) int {
	h, m, _ := strings.Cut(clock, ":")
	hh, _ := strconv.Atoi(h)
	mm, _ := strconv.Atoi(m)
	return hh*10000 + mm*100
}

// BackupScheduleFolder is the folder the fix writes to: the folder option when given, else the
// instance's default backup directory, normalised.
func BackupScheduleFolder(f BackupFacts, options map[string]string) string {
	v, ok := options[OptionFolder]
	if !ok {
		v = f.DefaultBackupDir
	}
	return normalizeFolder(v)
}

// boolOption reads a checkbox option: true when not given.
func boolOption(options map[string]string, name string) bool {
	v, ok := options[name]
	if !ok {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(v), "true")
}

// normalizeFolder trims spaces and trailing separators; a bare drive keeps its backslash (D:\).
func normalizeFolder(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, `\/`)
	if len(s) == 2 && s[1] == ':' {
		s += `\`
	}
	return s
}

// ValidBackupFolder says whether a folder is a full Windows path kakapo can put in a statement: a
// drive (D:\Backup) or a share (\\nas\sql), at most 200 characters, without control characters or
// the characters Windows does not allow in a path.
func ValidBackupFolder(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 200 || strings.ContainsAny(s, `*?"<>|`) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	switch {
	case len(s) >= 3 && isDriveLetter(s[0]) && s[1] == ':' && s[2] == '\\':
		return !strings.Contains(s[2:], ":")
	case strings.HasPrefix(s, `\\`):
		parts := strings.Split(s[2:], `\`)
		return len(parts) >= 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(s, ":")
	}
	return false
}

func isDriveLetter(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

// volumeOf is the drive ("D:") or the share's host ("\\NAS01") of a path, upper case; "" when it
// has neither.
func volumeOf(path string) string {
	switch {
	case len(path) >= 2 && isDriveLetter(path[0]) && path[1] == ':':
		return strings.ToUpper(path[:2])
	case strings.HasPrefix(path, `\\`):
		host, _, _ := strings.Cut(path[2:], `\`)
		return `\\` + strings.ToUpper(host)
	}
	return ""
}

// SameVolume says whether a folder is on the drive (or the share host) of one of the data files.
func SameVolume(folder string, dataFiles []string) bool {
	v := volumeOf(folder)
	if v == "" {
		return false
	}
	for _, p := range dataFiles {
		if volumeOf(p) == v {
			return true
		}
	}
	return false
}

// fileSafe is a database name as a folder or file name: the characters Windows does not allow
// become "_".
func fileSafe(name string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
}

// joinFolder is folder\name, without a doubled separator after a bare drive.
func joinFolder(folder, name string) string { return strings.TrimRight(folder, `\`) + `\` + name }

// nstr is a T-SQL Unicode string literal.
func nstr(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Dir is where the backup files go: {folder}\{db} for native backups; Ola's DatabaseBackup makes
// its own folders under {folder}.
func (s BackupSchedule) Dir() string {
	if s.Ola {
		return s.Folder
	}
	return joinFolder(s.Folder, fileSafe(s.Database))
}

// JobNames are the jobs the schedule creates, full first.
func (s BackupSchedule) JobNames() []string {
	out := []string{}
	if s.Full {
		out = append(out, BackupJobName(s.Database, JobFull))
	}
	if s.Log {
		out = append(out, BackupJobName(s.Database, JobLog))
	}
	return out
}

// backupCommand is the T-SQL of the backup step for a kind, tagged.
func (s BackupSchedule) backupCommand(kind string) string {
	if s.Ola {
		typ := "FULL"
		if kind == JobLog {
			typ = "LOG"
		}
		compress := "N"
		if s.Compress {
			compress = "Y"
		}
		cmd := fmt.Sprintf("EXECUTE master.dbo.DatabaseBackup @Databases = %s, @Directory = %s, @BackupType = '%s', @Verify = 'Y', @CheckSum = 'Y', @CleanupTime = %d, @Compress = '%s'",
			nstr(s.Database), nstr(s.Folder), typ, s.KeepDays*24, compress)
		return JobStepTag + "\n" + cmd + ";"
	}
	stmt, suffix, ext := "BACKUP DATABASE", "_FULL_", ".bak"
	if kind == JobLog {
		stmt, suffix, ext = "BACKUP LOG", "_LOG_", ".trn"
	}
	with := "CHECKSUM, INIT"
	if s.Compress {
		with += ", COMPRESSION"
	}
	return JobStepTag + "\n" +
		"DECLARE @dir nvarchar(400) = " + nstr(s.Dir()) + ";\n" +
		"EXEC master.dbo.xp_create_subdir @dir;\n" +
		"DECLARE @file nvarchar(600) = @dir + " + nstr(`\`+fileSafe(s.Database)+suffix) +
		" + CONVERT(nvarchar(8), GETDATE(), 112) + N'_' + REPLACE(CONVERT(nvarchar(5), GETDATE(), 108), N':', N'') + " + nstr(ext) + ";\n" +
		stmt + " " + QuoteName(s.Database) + " TO DISK = @file WITH " + with + ";\n" +
		"RESTORE VERIFYONLY FROM DISK = @file WITH CHECKSUM;"
}

// cleanupCommand is the T-SQL of the step that deletes the files older than the days kept, tagged.
func (s BackupSchedule) cleanupCommand(kind string) string {
	ext := "bak"
	if kind == JobLog {
		ext = "trn"
	}
	return JobStepTag + "\n" +
		fmt.Sprintf("DECLARE @cutoff datetime = DATEADD(DAY, -%d, GETDATE());\n", s.KeepDays) +
		"EXEC master.dbo.xp_delete_file 0, " + nstr(s.Dir()) + ", " + nstr(ext) + ", @cutoff, 0;"
}

// jobBlock is the sp_add_job ... sp_add_jobserver calls of one job.
func (s BackupSchedule) jobBlock(kind string) string {
	name := BackupJobName(s.Database, kind)
	var b strings.Builder
	agentJobAdd(&b, name, "Created by kakapo on "+s.Date+" for "+s.Database+": "+s.settingsWords(), s.Operator)
	agentJobStep(&b, name, 1, "Back up and verify", s.backupCommand(kind), s.Ola)
	if !s.Ola {
		agentJobStep(&b, name, 2, fmt.Sprintf("Delete backups older than %d days", s.KeepDays), s.cleanupCommand(kind), true)
	}
	b.WriteString("    EXEC msdb.dbo.sp_add_jobschedule @job_name = " + nstr(name) + ", @name = " +
		nstr(name+", "+s.when(kind)) + ", @enabled = 1,\n")
	if kind == JobFull {
		fmt.Fprintf(&b, "        @freq_type = 4, @freq_interval = 1, @active_start_time = %d;\n", agentStartTime(s.FullTime))
	} else {
		fmt.Fprintf(&b, "        @freq_type = 4, @freq_interval = 1, @freq_subday_type = 4, @freq_subday_interval = %d;\n", s.LogMinutes)
	}
	agentJobServer(&b, name)
	return b.String()
}

func (s BackupSchedule) kinds() []string {
	out := []string{}
	if s.Full {
		out = append(out, JobFull)
	}
	if s.Log {
		out = append(out, JobLog)
	}
	return out
}

// Batch is the statement that creates the jobs: one batch, in a transaction inside TRY ... CATCH,
// refusing when a job of the same name already exists. The jobs are owned by the sa principal
// whatever its name.
func (s BackupSchedule) Batch() string {
	blocks := ""
	for _, k := range s.kinds() {
		blocks += s.jobBlock(k)
	}
	return agentJobBatch(s.JobNames(), blocks)
}

// Rollback deletes the jobs the schedule creates.
func (s BackupSchedule) Rollback() string { return agentDeleteJobs(s.JobNames()) }

// StartStatement starts the full backup job: run after the read-back, as its own statement (a
// started job cannot be rolled back).
func (s BackupSchedule) StartStatement() string {
	return "EXEC msdb.dbo.sp_start_job @job_name = " + nstr(BackupJobName(s.Database, JobFull)) + ";"
}

// scriptName is the file the Express script is saved as: backup-{db}.sql, backup-{db}-log.sql.
func (s BackupSchedule) scriptName(kind string) string {
	if kind == JobLog {
		return "backup-" + fileSafe(s.Database) + "-log.sql"
	}
	return "backup-" + fileSafe(s.Database) + ".sql"
}

// Script is what Express gets instead of jobs: the backup batches to schedule in Windows Task
// Scheduler with sqlcmd, each with its cleanup.
func (s BackupSchedule) Script() string {
	var b strings.Builder
	fmt.Fprintf(&b, "-- kakapo: backups of %s on %s. SQL Server Express has no SQL Server Agent: schedule this in Windows Task Scheduler.\n", s.Database, s.Server)
	fmt.Fprintf(&b, "-- Save the FULL part as %s and run it %s: sqlcmd -S %s -E -i %s\n", s.scriptName(JobFull), s.scriptWhen(JobFull), s.Server, s.scriptName(JobFull))
	if s.Log {
		fmt.Fprintf(&b, "-- Save the LOG part as %s and run it %s: sqlcmd -S %s -E -i %s\n", s.scriptName(JobLog), s.scriptWhen(JobLog), s.Server, s.scriptName(JobLog))
	}
	for _, k := range s.kinds() {
		fmt.Fprintf(&b, "\n-- %s\n%s\n%s\nGO\n", strings.ToUpper(k), s.backupCommand(k), s.cleanupCommand(k))
	}
	return strings.TrimRight(b.String(), "\n")
}

// when is a job's schedule in words: "daily 22:00", "every 15 minutes".
func (s BackupSchedule) when(kind string) string {
	if kind == JobFull {
		return "daily " + s.FullTime
	}
	return fmt.Sprintf("every %d minutes", s.LogMinutes)
}

// scriptWhen is a job's schedule as the Express script tells Task Scheduler.
func (s BackupSchedule) scriptWhen(kind string) string {
	if kind == JobFull {
		return "daily at " + s.FullTime
	}
	return s.when(kind)
}

// settingsWords are the settings as the job description carries them: "daily 22:00, log every 15
// min, keep 7 days" (the parts of the jobs created).
func (s BackupSchedule) settingsWords() string {
	parts := []string{}
	if s.Full {
		parts = append(parts, "daily "+s.FullTime)
	}
	if s.Log {
		parts = append(parts, fmt.Sprintf("log every %d min", s.LogMinutes))
	}
	parts = append(parts, fmt.Sprintf("keep %d days", s.KeepDays))
	return strings.Join(parts, ", ")
}

// jobWords is a job as the change log and the preview name it: "kakapo: Shop full backup (daily 22:00)".
func (s BackupSchedule) jobWords(kind string) string {
	return BackupJobName(s.Database, kind) + " (" + s.when(kind) + ")"
}

// Description is the change log line of the fix.
func (s BackupSchedule) Description() string {
	words := []string{}
	for _, k := range s.kinds() {
		words = append(words, s.jobWords(k))
	}
	jobs := "the job "
	if len(words) > 1 {
		jobs = "the jobs "
	}
	return fmt.Sprintf("Created %s%s writing to %s, %d days kept", jobs, strings.Join(words, " and "), s.Dir(), s.KeepDays)
}

// Before is what the fix reads before: which job was missing.
func (s BackupSchedule) Before() string {
	switch {
	case s.Full && s.logCovered:
		return "no full backup job"
	case !s.Full && s.Log:
		return "no log backup job"
	}
	return "no backup job"
}

// BackupJobState is a job read back after the fix ran.
type BackupJobState struct {
	Name            string
	Enabled         bool
	ScheduleEnabled bool
	NextRun         *time.Time // sysjobschedules next_run_date/time in the server's clock; nil before Agent sets it
}

// expectedNextRun is when a job of the schedule runs next, from its schedule and the server's
// clock: the full backup time today or tomorrow, or the next multiple of the log interval since
// midnight (every interval offered divides a day).
func (s BackupSchedule) expectedNextRun(kind string, now time.Time) time.Time {
	if kind == JobFull {
		at := agentStartTime(s.FullTime)
		t := time.Date(now.Year(), now.Month(), now.Day(), at/10000, at/100%100, 0, 0, time.UTC)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t
	}
	step := time.Duration(s.LogMinutes) * time.Minute
	return now.Truncate(step).Add(step)
}

// firstFullWords says when the first scheduled full backup runs: "tonight at 22:00", "today at
// 14:00", "tomorrow at 01:30".
func (s BackupSchedule) firstFullWords(now time.Time) string {
	t := s.expectedNextRun(JobFull, now)
	switch {
	case t.YearDay() != now.YearDay() || t.Year() != now.Year():
		return "tomorrow at " + s.FullTime
	case t.Hour() >= 18:
		return "tonight at " + s.FullTime
	}
	return "today at " + s.FullTime
}

// ReadBack says what the read-back found after the jobs were created, and whether every created
// job exists and is enabled. started says the full backup job was started (RunNow); startErr why it
// could not be.
func (s BackupSchedule) ReadBack(states []BackupJobState, now time.Time, started bool, startErr string) (string, bool) {
	parts, verified := []string{}, true
	for _, k := range s.kinds() {
		name := BackupJobName(s.Database, k)
		var st *BackupJobState
		for i := range states {
			if states[i].Name == name {
				st = &states[i]
				break
			}
		}
		if st == nil {
			parts, verified = append(parts, name+": not found"), false
			continue
		}
		text := name + ": enabled"
		if !st.Enabled {
			text, verified = name+": disabled", false
		}
		if !st.ScheduleEnabled {
			text += ", schedule disabled"
		}
		if st.NextRun != nil {
			text += ", next run " + sqlTime(*st.NextRun)
		} else {
			text += ", next run " + sqlTime(s.expectedNextRun(k, now)) + " (expected; Agent has not set it yet)"
		}
		parts = append(parts, text)
	}
	out := strings.Join(parts, "; ")
	switch {
	case started:
		// The job start is quick; the backup itself runs under SQL Server Agent
		// (docs/phase4f-contract.md), so the result says so instead of waiting for it.
		out += "; full backup started: it runs under SQL Server Agent, and the Backups card shows it when it finishes."
	case s.RunNow && startErr != "":
		out += "; the full backup could not be started: " + startErr
	default:
		out += "."
	}
	return out, verified
}

// BackupFixPermission is what the backup fixes need and name when SQL Server refuses.
func BackupFixPermission(server string) string {
	return "Needs sysadmin on " + server + " (creating Agent jobs owned by sa)."
}

// RemoveFixPermission is what the remove fix needs and names when SQL Server refuses.
func RemoveFixPermission(server string) string {
	return "Needs sysadmin on " + server + " (deleting Agent jobs owned by sa)."
}

// hasFix says whether a finding carries a fix.
func hasFix(findings []Finding, fixID string) (string, bool) {
	for _, x := range findings {
		if x.FixID == fixID {
			return x.CheckID, true
		}
	}
	return "", false
}

// OffersBackupSchedule says whether the backup schedule fix can be created on this read, whatever
// the findings say: the backups and jobs were read, the database is not a secondary replica,
// kakapo's own jobs do not exist yet, and a full or log job would be created (no enabled job covers
// everything). The card offers it as a standing row; a finding carries it too when it fired.
func OffersBackupSchedule(f BackupFacts) bool {
	if !f.JobsRead || f.JobsError != "" || f.MsdbError != "" || f.IsSecondary || len(f.KakapoJobs) > 0 {
		return false
	}
	s := NewBackupSchedule(f, nil)
	return s.Full || s.Log
}

// scheduleCheckID is the check the fix is shown under: the finding that carries it, else BAK01.
func scheduleCheckID(findings []Finding) string {
	if id, ok := hasFix(findings, FixBackupSchedule); ok {
		return id
	}
	return "BAK01"
}

// BuildBackupScheduleFix builds the preview and the fix of "Create a backup schedule" from what was
// read and the options the user set (docs/phase3g-contract.md). folderOK says the folder exists on
// the server (the service checks it with xp_fileexist before building); sameDrive that it is on a
// drive holding the database's data files. ErrFixNotApplicable when the schedule cannot be created
// on this read (OffersBackupSchedule).
func BuildBackupScheduleFix(f BackupFacts, dbName string, options map[string]string, folderOK, sameDrive bool) (FixPreview, Fix, error) {
	if dbName != "" {
		f.Database = dbName
	}
	if !OffersBackupSchedule(f) {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	def, _ := FindCardDef(CardBackups)
	_, findings, _ := BuildBackupsCard(f, def)
	p, fix := buildBackupSchedule(f, options, folderOK, sameDrive)
	fix.CheckID = scheduleCheckID(findings)
	return p, fix, nil
}

func buildBackupSchedule(f BackupFacts, options map[string]string, folderOK, sameDrive bool) (FixPreview, Fix) {
	d, _ := FindFixDef(FixBackupSchedule)
	s := NewBackupSchedule(f, options)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting, Current: s.Before()}
	words := []string{}
	for _, k := range s.kinds() {
		words = append(words, s.jobWords(k))
	}
	fix.Target = strings.Join(words, " and ")

	p := FixPreview{Database: f.Database, EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}, Express: s.Express}
	folder := FixInput{Name: OptionFolder, Label: "Backup folder", Value: s.Folder, Help: folderHelp, Kind: "text"}
	p.Options = map[string]string{OptionFolder: s.Folder}

	server := f.ServerName
	switch {
	case s.Folder == "":
		folder.Warning = "Type the folder the backups go to, as the SQL Server service on " + server + " sees it."
	case !ValidBackupFolder(s.Folder):
		folder.Warning = "The folder " + s.Folder + " is not a full Windows path (like D:\\Backup or \\\\server\\share): type another."
	case !folderOK:
		folder.Warning = "The folder " + s.Folder + " does not exist on " + server + " (as the SQL Server service sees it): create it, or type another."
	}
	usable := folder.Warning == ""
	if folder.Warning != "" {
		p.Warnings = append(p.Warnings, folder.Warning)
	} else if sameDrive {
		folder.Warning = "This folder is on the same drive as the data files (" + volumeOf(s.Folder) + "): a disk failure loses the database and its backups together. Prefer another drive or a share."
		p.Warnings = append(p.Warnings, folder.Warning)
	}
	p.Inputs = []FixInput{folder}
	// setting adds an input with the value the schedule uses, or the value as typed and its
	// warning when it is not valid (nothing can be applied then).
	setting := func(in FixInput, valid bool, typed, used, warning string) {
		in.Value = used
		if !valid {
			in.Value, in.Warning = typed, warning
			p.Warnings = append(p.Warnings, warning)
			usable = false
		}
		p.Inputs = append(p.Inputs, in)
		p.Options[in.Name] = in.Value
	}
	if !s.Express {
		if s.Full {
			typed, given := options[OptionFullTime]
			_, ok := parseClock(typed)
			setting(FixInput{Name: OptionFullTime, Label: "Full backup time", Help: fullTimeHelp, Kind: "text"},
				!given || ok, strings.TrimSpace(typed), s.FullTime, fullTimeWarn)
		}
		if s.Log {
			typed, given := options[OptionLogMinutes]
			_, ok := parseLogMinutes(typed)
			setting(FixInput{Name: OptionLogMinutes, Label: "Log backup every (minutes)", Help: logMinutesHelp, Kind: "select", Options: LogMinuteChoices},
				!given || ok, strings.TrimSpace(typed), strconv.Itoa(s.LogMinutes), logMinutesWarn)
		}
		typed, given := options[OptionKeepDays]
		_, ok := parseKeepDays(typed)
		setting(FixInput{Name: OptionKeepDays, Label: "Keep backups (days)", Help: keepDaysHelp, Kind: "text"},
			!given || ok, strings.TrimSpace(typed), strconv.Itoa(s.KeepDays), keepDaysWarn)
		if f.CompressionOK {
			setting(FixInput{Name: OptionCompress, Label: "Compress the backups", Help: compressHelp, Kind: "bool"},
				true, "", fmt.Sprint(s.Compress), "")
		}
		if s.Full {
			setting(FixInput{Name: OptionRunNow, Label: "Take the first full backup now", Help: runNowHelp, Kind: "bool"},
				true, "", fmt.Sprint(s.RunNow), "")
		}
	}

	if s.Express {
		p.Warnings = append(p.Warnings, "SQL Server Express has no Agent: kakapo cannot create the schedule. Copy the script and schedule it in Windows Task Scheduler.")
	} else if f.AgentError == "" && !f.AgentRunning {
		status := f.AgentStatus
		if status == "" {
			status = "not running"
		}
		p.Warnings = append(p.Warnings, "SQL Server Agent is stopped ("+status+"): the jobs will not run until it starts (MNT01).")
	}
	if f.MaintenancePlans > 0 || f.PlansError != "" {
		p.Warnings = append(p.Warnings, "A maintenance plan exists on this server; check it does not already back up "+f.Database+" before creating a second schedule.")
	}
	if !f.CompressionOK {
		edition := "This"
		if w, _, _ := strings.Cut(strings.TrimSpace(f.Edition), " "); w != "" {
			edition = w
		}
		p.Warnings = append(p.Warnings, edition+" edition cannot compress backups: the files will be full size.")
	}
	if !s.Express && s.Log && s.Full && !s.RunNow && f.LastFull == nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Until the first full backup %s, the log backup job fails every %d minutes: SQL Server cannot back up a log before a full backup.",
			s.firstFullWords(f.ServerNow), s.LogMinutes))
	}
	if s.Express {
		p.Warnings = append(p.Warnings, "The backup runs as the SQL Server service account; the folder must let it write.")
	} else {
		p.Warnings = append(p.Warnings, "The backup job runs as the SQL Server service account; the folder must let it write.")
	}
	if s.Ola {
		p.Warnings = append(p.Warnings, "Uses the DatabaseBackup procedure already on this server.")
	}
	if s.Express {
		p.Warnings = append(p.Warnings, "Needs sysadmin on "+server+" for the Windows account that runs the scheduled task (sqlcmd -E logs in as that account).")
	} else {
		p.Warnings = append(p.Warnings, BackupFixPermission(server))
	}
	if !s.Express && f.PermsRead && !f.IsSysadmin {
		// First, so the reason Apply will fail is not buried under the notes.
		p.Warnings = append([]string{fmt.Sprintf(scheduleNotSysadmin, server)}, p.Warnings...)
	}

	if !usable {
		// Nothing can be applied until the folder is right: no changes, no statement.
		return p, fix
	}
	p.Changes = s.changes(f)
	if s.Express {
		p.Statement = s.Script()
	} else {
		p.Statement, p.Rollback = s.Batch(), s.Rollback()
	}
	return p, fix
}

// changes are the preview's rows: the two jobs, what is kept, who is told, the first backup.
func (s BackupSchedule) changes(f BackupFacts) []Drift {
	out := []Drift{}
	full := Drift{Setting: "Full backup job", Current: "none"}
	if s.Full {
		full.Target = s.when(JobFull) + " to " + s.Dir() + ", verified"
	} else if j, ok := f.CoveringJob(JobFull); ok {
		full.Current, full.Target = j.Name, "kept as it is"
	}
	out = append(out, full)

	log := Drift{Setting: "Log backup job", Current: "none"}
	switch {
	case s.Log:
		log.Target = s.when(JobLog) + " to " + s.Dir()
	case !fullOrBulk(f.RecoveryModel):
		log.Target = logNotNeeded
	default:
		if j, ok := f.CoveringJob(JobLog); ok {
			log.Current = j.Name
		}
		log.Target = "kept as it is"
	}
	out = append(out, log)

	out = append(out, Drift{Setting: "Keep", Target: fmt.Sprintf("%d days of files", s.KeepDays)})
	compressed := "yes"
	switch {
	case !f.CompressionOK:
		compressed = "no (this edition cannot compress)"
	case !s.Compress:
		compressed = "no"
	}
	out = append(out, Drift{Setting: "Compressed", Target: compressed})
	told := noOperator
	switch {
	case s.Express:
		told = "no one (Express has no Agent to send an email)"
	case s.Operator != "":
		told = s.Operator
	}
	out = append(out, Drift{Setting: "Tell on failure", Target: told})
	if s.Full {
		first := s.firstFullWords(f.ServerNow)
		switch {
		case s.Express:
			first = "when the scheduled task first runs"
		case s.RunNow:
			first = "now, when you apply"
		}
		out = append(out, Drift{Setting: "First full backup", Target: first})
	}
	return out
}

// ExistingBackupSchedule is the schedule kakapo's jobs on this database make up, as read: which of
// the two exist, their schedules, the folder and the days kept read back from the step commands, Ola
// or native, compression. A setting that cannot be read back is the default. false when the folder
// could not be read back or neither job is this database's.
func ExistingBackupSchedule(f BackupFacts) (BackupSchedule, bool) {
	s := BackupSchedule{Database: f.Database, Server: f.ServerName, Operator: f.Operator, Date: f.ServerNow.Format("2006-01-02"),
		FullTime: DefaultFullTime, LogMinutes: DefaultLogMinutes, KeepDays: DefaultKeepDays}
	if v, ok := parseClock(f.KakapoFullTime); ok {
		s.FullTime = v
	}
	if f.KakapoLogMinutes > 0 {
		s.LogMinutes = f.KakapoLogMinutes
	}
	for _, n := range f.KakapoJobs {
		switch n {
		case BackupJobName(f.Database, JobFull):
			s.Full = true
		case BackupJobName(f.Database, JobLog):
			s.Log = true
		}
	}
	cmd := f.KakapoCommand
	s.Ola = strings.Contains(cmd, "DatabaseBackup")
	s.Compress = strings.Contains(cmd, "COMPRESSION") || strings.Contains(cmd, "@Compress = 'Y'")
	if days, ok := keepDaysFromCommand(cmd, s.Ola); ok {
		s.KeepDays = days
	}
	folder, ok := folderFromCommand(cmd, f.Database, s.Ola)
	s.Folder = folder
	return s, ok && (s.Full || s.Log)
}

// keepDaysFromCommand reads the days kept back from the steps kakapo wrote: "@CleanupTime = {hours}"
// (Ola) or "DATEADD(DAY, -{days}," (the native cleanup step).
func keepDaysFromCommand(cmd string, ola bool) (int, bool) {
	marker := "DATEADD(DAY, -"
	if ola {
		marker = "@CleanupTime = "
	}
	i := strings.Index(cmd, marker)
	if i < 0 {
		return 0, false
	}
	rest := cmd[i+len(marker):]
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	v, err := strconv.Atoi(rest[:n])
	if err != nil || v <= 0 {
		return 0, false
	}
	if ola {
		if v%24 != 0 {
			return 0, false
		}
		v /= 24
	}
	return v, true
}

// folderFromCommand reads the folder back from the backup step kakapo wrote: the literal after
// "@Directory = " (Ola) or "@dir nvarchar(400) = " without its \{db} (native).
func folderFromCommand(cmd, db string, ola bool) (string, bool) {
	marker := "@dir nvarchar(400) = N'"
	if ola {
		marker = "@Directory = N'"
	}
	i := strings.Index(cmd, marker)
	if i < 0 {
		return "", false
	}
	lit, ok := readLiteral(cmd[i+len(marker):])
	if !ok {
		return "", false
	}
	if ola {
		return lit, lit != ""
	}
	suffix := `\` + fileSafe(db)
	if !strings.HasSuffix(lit, suffix) {
		return "", false
	}
	return normalizeFolder(strings.TrimSuffix(lit, suffix)), true
}

// readLiteral reads a T-SQL string literal's text up to its closing quote; a doubled quote is one quote.
func readLiteral(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return b.String(), true
	}
	return "", false
}

// jobLabel is a kakapo job as a preview row names it.
func jobLabel(kind string) string {
	if kind == JobFull {
		return "Full backup job"
	}
	return "Log backup job"
}

// BuildRemoveBackupScheduleFix builds the preview and the fix of "Remove kakapo's backup jobs":
// delete the jobs kakapo created for this database, with the creation batch rebuilt from the jobs as
// they are as the rollback. ErrFixNotApplicable when kakapo's jobs do not exist.
func BuildRemoveBackupScheduleFix(f BackupFacts) (FixPreview, Fix, error) {
	if !f.JobsRead || f.JobsError != "" || len(f.KakapoJobs) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	d, _ := FindFixDef(FixRemoveBackupSchedule)
	s, folderOK := ExistingBackupSchedule(f)
	names := s.JobNames()
	if len(names) == 0 {
		// Jobs named as kakapo's but not this database's exactly: nothing kakapo knows how to remove.
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: strings.Join(names, ", "), Target: "deleted"}
	p := FixPreview{Database: f.Database, EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}}
	for _, k := range s.kinds() {
		p.Changes = append(p.Changes, Drift{Setting: jobLabel(k), Current: fmt.Sprintf("%s, keep %d days", s.when(k), s.KeepDays), Target: "deleted"})
	}
	p.Statement = s.Rollback()
	if folderOK {
		p.Rollback = s.Batch()
		p.Warnings = append(p.Warnings, "The backup files in "+s.Dir()+" are not deleted.")
	} else {
		p.Warnings = append(p.Warnings, "The backup files are not deleted.",
			"kakapo could not read the folder from the job's step (it was changed by hand), so there is no rollback: script the jobs in SSMS first if you may want them back.")
	}
	p.Warnings = append(p.Warnings, RemoveFixPermission(f.ServerName))
	return p, fix, nil
}

// RemoveDescription is the change log line of the remove fix.
func RemoveDescription(names []string) string {
	if len(names) == 1 {
		return "Deleted the job " + names[0]
	}
	return "Deleted the jobs " + strings.Join(names, " and ")
}

// RemoveReadBack says what is left of the jobs the remove fix deleted, and whether none is.
func RemoveReadBack(remaining []BackupJobState) (string, bool) {
	if len(remaining) == 0 {
		return "no kakapo backup job", true
	}
	names := []string{}
	for _, st := range remaining {
		names = append(names, st.Name)
	}
	return "still there: " + strings.Join(names, ", "), false
}

// AvailableBackupFixes are the fixes the Backups card offers on this read: the backup schedule
// whenever it can be created, the remove fix when kakapo's jobs exist, then the phase 4d fixes of the
// findings that fired (recovery model, integrity check job, CHECKDB now, suspect-page clear).
func AvailableBackupFixes(f BackupFacts, findings []Finding) []Fix {
	out := []Fix{}
	if OffersBackupSchedule(f) {
		_, fix := buildBackupSchedule(f, nil, true, false)
		fix.CheckID = scheduleCheckID(findings)
		out = append(out, fix)
	}
	if _, fix, err := BuildRemoveBackupScheduleFix(f); err == nil {
		out = append(out, fix)
	}
	return append(out, availableBackupActionFixes(f, findings)...)
}
