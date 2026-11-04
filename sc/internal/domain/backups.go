package domain

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// BackupFacts is what the Backups card reads about one database (docs/phase3a-contract.md). Every
// time is in the server's own clock, as GETDATE() returns it, carried in a time.Time with the UTC
// location; every "ago" and "gap" is worked out against ServerNow, never the laptop's clock.
type BackupFacts struct {
	Database      string
	State         string // sys.databases state_desc
	RecoveryModel string // FULL, BULK_LOGGED, SIMPLE
	CreateDate    time.Time
	IsAgMember    bool // replica_id IS NOT NULL

	// Last backups of each kind since the database was created; nil when none.
	LastFull, LastDiff, LastLog *time.Time
	// RestorePoints are the finish times of the backups a restore can use in the last 7 days:
	// not copy-only, full and differential, and log only in FULL or BULK_LOGGED recovery.
	RestorePoints []time.Time
	// Backups of each kind in the last 7 days, every row (copy-only too).
	Fulls7, Diffs7, Logs7 int

	// LastGoodCheckDb is DATABASEPROPERTYEX(..., 'LastGoodCheckDbTime'): nil when this version
	// does not know it (before SQL Server 2016 SP2), 1900-01-01 when it never ran.
	LastGoodCheckDb *time.Time
	SuspectPages    int        // rows in msdb.dbo.suspect_pages for this database
	SuspectLast     *time.Time // the latest last_update_date of those
	LogSizeMB       *int64     // nil when sys.master_files is not visible to this login

	// BAK06: full backups (type D, not copy-only) written to the NUL device in the last 30 days,
	// oldest first, and the last full backup that went somewhere real (nil when none). The
	// collector already leaves NUL fulls, and the differentials based on them, out of
	// RestorePoints.
	NulFulls     []time.Time
	LastRealFull *time.Time

	// DBC12: sys.databases delayed_durability_desc (DISABLED, ALLOWED, FORCED); "" before SQL
	// Server 2014, which has no delayed durability. DurabilityError when it could not be read.
	DelayedDurability string
	DurabilityError   string

	ServerNow        time.Time // GETDATE(), read in the same batch
	UTCOffsetMinutes int       // the server's offset from UTC, to turn ServerNow into UTC
	EngineVersion    string    // SERVERPROPERTY('ProductVersion'), e.g. 16.0.1000.6; "" when not read

	// Phase 3g, the backup schedule fix (docs/phase3g-contract.md), read in the same batch, each
	// area in its own TRY.
	ServerName string // @@SERVERNAME
	Edition    string // SERVERPROPERTY('Edition')
	HasAgent   bool   // EngineEdition <> 4: Express has no SQL Server Agent
	// The Agent service (sys.dm_server_services): AgentStatus is its status_desc, AgentError says
	// why it could not be read or is not visible. Not read on Express.
	AgentRunning bool
	AgentStatus  string
	AgentError   string
	// JobsRead says the msdb job areas were read (on Express there is nothing to read and they are
	// empty). Facts without them offer no backup schedule fix: kakapo cannot tell whether a job
	// already backs this database up. JobsError says why the jobs could not be read.
	JobsRead   bool
	BackupJobs []BackupJob // jobs, enabled or not, with a step that backs up this database or all of them
	// KakapoJobs are the jobs kakapo created for this database ("kakapo: {db} full backup", "kakapo:
	// {db} log backup"), by name; KakapoCommand is the step commands of the full one (else the log
	// one) in step order, from which the remove fix reads the folder, the days kept and compression
	// back. KakapoFullTime is the full job's schedule time (sysschedules active_start_time as HH:MM)
	// and KakapoLogMinutes the log job's interval (freq_subday_interval); "" and 0 when not read.
	KakapoJobs       []string
	KakapoCommand    string
	KakapoFullTime   string
	KakapoLogMinutes int
	JobsError        string
	MaintenancePlans int    // rows in msdb.dbo.sysmaintplan_subplans
	PlansError       string // the plans could not be read: treated as "there may be one"
	OlaBackup        bool   // OBJECT_ID('master.dbo.DatabaseBackup') IS NOT NULL
	Operator         string // the first enabled operator with an email address, by name; "" none
	OperatorError    string
	DefaultBackupDir string   // the instance's BackupDirectory from the registry; "" when refused
	CompressionOK    bool     // the edition can compress backups (not Web, not Express, 2008 and later)
	DataFiles        []string // physical names of this database's data (ROWS) files
	// IsSecondary says this instance holds the database as an availability group secondary; the
	// service fills it in from the database as listed. No fix is offered on a secondary.
	IsSecondary bool

	// LastCheckdbRun is the last CHECKDB run from the app (checkdb-now) stored for this database, or
	// nil; the service fills it in from the store. BAK04 offers to clear the suspect-page records
	// once a clean run came after the latest of them (docs/phase4d-contract.md).
	LastCheckdbRun *CheckdbRun

	MsdbError string // msdb backup history could not be read; the msdb checks are skipped
	Error     string // nothing could be read at all
}

// AsOf is ServerNow in UTC.
func (f BackupFacts) AsOf() time.Time {
	t := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// Worst-gap thresholds in minutes, as the scan uses them.
const (
	gap7Days   = 10080
	gap2Days   = 2880
	gap25Hours = 1500
)

// Texts from the scan (01-QuickScan.sql sections 2 and 5), verbatim.
const (
	bak01Why      = "Without a recent backup, a disk failure, corruption or a mistake can mean permanent data loss."
	bak01Do       = "Schedule daily full backups (or weekly full plus daily differential), keep copies off this server, and test a restore regularly. If another tool takes backups (e.g. VM snapshots), confirm it is SQL-aware and restores are tested."
	bak02Why      = "In FULL recovery the log keeps growing until it is backed up. It can fill the disk and stop the database, and it limits how recent a restore can be."
	bak02Do       = "Schedule log backups (commonly every 15-60 minutes). If point-in-time restore is not needed, the business can decide to switch to SIMPLE recovery instead (DBC08)."
	bak03Why      = "CHECKDB finds corruption early, while good backups still exist to recover from. Unnoticed corruption ends up in the backups too."
	bak03Do       = "Schedule DBCC CHECKDB weekly outside peak hours (on very large databases: WITH PHYSICAL_ONLY weekly and a full check monthly). Ola Hallengren's free maintenance solution is a common way to set this up."
	bak04Why      = "This is a sign of corruption, usually caused by storage. It can spread and lead to data loss."
	bak04Do       = "Escalate to a DBA today: run DBCC CHECKDB on this database in a quiet period, ask the infrastructure team to check storage health, and confirm good backups exist. If it was already fixed, the DBA can clear the old rows from msdb.dbo.suspect_pages."
	bak05Why      = "If the disk fails or someone deletes the wrong rows, everything entered since the last backup is gone. This is the number to agree with the client."
	bak05DoSimple = "If that is more than the business accepts: switch to FULL recovery and back up the log every 15 minutes (the window then becomes 15 minutes), or at least add a differential backup at midday."
	bak05DoFull   = "Log backups every 15 minutes close this gap. If they are already scheduled, check that the job runs (MNT02)."
	bak05Lost7    = "No backup of any kind in the last 7 days: everything since the last backup would be lost."
	bak05DoApply  = "Apply creates a daily full backup job now (and log backups every 15 minutes in FULL recovery)."
	dbc08Why      = "A restore can only go back to the last full or differential backup; everything since then is lost. Point-in-time restore is not possible."
	dbc08Do       = "Confirm with the business how much data loss is acceptable (RPO). If it is less than the time between backups, switch to FULL recovery and schedule log backups."

	// BAK06 and DBC12, from docs/phase3f-contract.md items 4 and 5.
	bak06Why = "A full backup written to NUL is thrown away, but SQL Server still counts it as the base for the next differential backups. Those differentials look fine in the backup history, yet a restore needs the full backup they were based on, and that file does not exist."
	bak06Do  = "Client IT: find the job or script that backs up to NUL (usually a test of backup speed) and stop it or add COPY_ONLY to it; then take a real full backup now, so the differentials have a base again."
	dbc12Why = "With delayed durability SQL Server tells the application a transaction is saved before it is written to disk. If SQL Server crashes or the server loses power at that moment, the last orders, picks or receipts are gone although the users were told they were saved."

	checkDbUnknownNote = "Last CHECKDB date is not available on this version (needs SQL Server 2016 SP2 or later)."
	checkDbNotGiven    = "SQL Server did not report the last CHECKDB date for this database."
)

// knowsLastCheckDb says whether an engine version reports LastGoodCheckDbTime: SQL Server 2016
// SP2 (13.0.5026) and later. An unknown version is taken as one that does not.
func knowsLastCheckDb(version string) bool {
	var major, minor, build int
	if n, _ := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &build); n < 3 {
		return false
	}
	return major > 13 || (major == 13 && build >= 5026)
}

// BuildBackupsCard answers "Could you lose data?" for one database from what was read: the card,
// its findings ranked, and what became of each check behind it, in catalogue order.
func BuildBackupsCard(f BackupFacts, def CardDef) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	b := &backupBuild{f: f, findings: []Finding{}, skipped: map[string]string{}}

	switch {
	case f.Error != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", f.Error
		b.skipAll(def.ID, f.Error)
		return c, b.findings, b.checks(def.ID)
	case f.State != "ONLINE":
		note := fmt.Sprintf("%s is %s, not online, so its backups were not checked.", f.Database, f.State)
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID)
	}

	msdbNote := ""
	if f.MsdbError == "" {
		b.bak01()
		b.bak02()
		b.bak04()
		b.bak05()
		b.bak06()
	} else {
		msdbNote = "Could not read msdb backup history: " + f.MsdbError
		for _, id := range []string{"BAK01", "BAK02", "BAK04", "BAK05", "BAK06"} {
			b.skipped[id] = msdbNote
		}
	}
	b.bak03()
	b.dbc08()
	b.dbc12()
	RankFindings(b.findings)
	checks := b.checks(def.ID)

	if msdbNote != "" {
		// Without the backup history the card cannot answer its question. The checks that do
		// not need msdb still ran, and their findings are kept for the detail page.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", msdbNote
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
	} else if x, ok := b.find("BAK05"); ok {
		c.Verdict = x.Finding
	}
	c.ToDo, c.ForClientIT, c.Fixable = actionable, clientIT, fixable
	asOf := f.AsOf()
	c.AsOf = &asOf
	c.Facts = b.facts()
	return c, b.findings, checks
}

type backupBuild struct {
	f        BackupFacts
	findings []Finding
	skipped  map[string]string
	worst    int  // worst gap in minutes, when a full backup exists
	hasWorst bool // BAK05 ran
	checkOld bool // BAK03 fired on an old (not never) CHECKDB
}

func (b *backupBuild) add(id string, rank int, changeType, effort, finding, why, doThis, evidence string) {
	b.addFix(id, rank, changeType, effort, finding, why, doThis, evidence, "")
}

func (b *backupBuild) addFix(id string, rank int, changeType, effort, finding, why, doThis, evidence, fixID string) {
	chk, _ := CheckByID(id)
	b.findings = append(b.findings, Finding{
		CheckID: id, Title: chk.Title, Severity: SeverityFromRank(min(rank, 5)), Owner: chk.Owner,
		When: WhenFromChangeType(changeType), Effort: effort, Object: b.f.Database,
		Finding: finding, Why: why, DoThis: doThis, ThenCheck: chk.ThenCheck, Evidence: evidence, FixID: fixID,
	})
}

func (b *backupBuild) find(id string) (Finding, bool) {
	for _, x := range b.findings {
		if x.CheckID == id {
			return x, true
		}
	}
	return Finding{}, false
}

func (b *backupBuild) skipAll(card CardID, note string) {
	for _, c := range ChecksFor(card) {
		b.skipped[c.ID] = note
	}
}

// checks says what became of each check: skipped with its note, a finding when it found
// something to act on, passed otherwise (an Info finding is information, not a problem).
func (b *backupBuild) checks(card CardID) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(card) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
		if note, ok := b.skipped[c.ID]; ok {
			st.Status, st.Note = CheckSkipped, note
		} else {
			for _, x := range b.findings {
				if x.CheckID == c.ID && x.Severity.Actionable() {
					st.Status = CheckFinding
				}
			}
		}
		out = append(out, st)
	}
	return out
}

func (b *backupBuild) agNote(text string) string {
	if b.f.IsAgMember {
		return " " + text
	}
	return ""
}

// agBump lowers a severity one step for an availability group member, where backups and checks
// may run on another replica; never below Info.
func agBump(rank int, ag bool) int {
	if ag && rank < 5 {
		return rank + 1
	}
	return rank
}

func fullOrBulk(model string) bool { return model == "FULL" || model == "BULK_LOGGED" }

// bak01: no full backup ever.
func (b *backupBuild) bak01() {
	f := b.f
	if f.LastFull != nil {
		return
	}
	rank := 1
	if f.CreateDate.After(f.ServerNow.AddDate(0, 0, -1)) {
		rank = 3
	}
	fixID, doThis := f.scheduleFix(JobFull, bak01Do)
	b.addFix("BAK01", agBump(rank, f.IsAgMember), "Online", EffortQuickWin,
		"No full backup of this database has been recorded.", bak01Why, doThis,
		"Last full: never. Last differential: "+sqlTimeOr(f.LastDiff, "never")+"."+
			b.agNote("Database is in an Availability Group: backups may run on another replica."), fixID)
}

// bak02: FULL or BULK_LOGGED recovery without a log backup in the last 24 hours.
func (b *backupBuild) bak02() {
	f := b.f
	if !fullOrBulk(f.RecoveryModel) || (f.LastLog != nil && !f.LastLog.Before(f.ServerNow.Add(-24*time.Hour))) {
		return
	}
	finding := "Database uses " + f.RecoveryModel + " recovery but its log has never been backed up."
	if f.LastLog != nil {
		finding = fmt.Sprintf("Last log backup was %d hours ago.", hoursBetween(*f.LastLog, f.ServerNow))
	}
	size := "unknown"
	if f.LogSizeMB != nil {
		size = fmt.Sprint(*f.LogSizeMB)
	}
	fixID, doThis := f.scheduleFix(JobLog, bak02Do)
	b.addFix("BAK02", agBump(2, f.IsAgMember), "Online", EffortQuickWin, finding, bak02Why, doThis,
		"Log file size: "+size+" MB."+b.agNote("Database is in an Availability Group: log backups may run on another replica."), fixID)
}

// bak05: the worst-case data loss, whenever a full backup exists.
func (b *backupBuild) bak05() {
	f := b.f
	if f.LastFull == nil {
		return
	}
	w := WorstGapMinutes(f)
	b.worst, b.hasWorst = w, true
	rank := 5
	switch {
	case w >= gap7Days:
		rank = 1
	case w >= gap2Days:
		rank = 2
	case w > gap25Hours:
		rank = 3
	}
	if f.IsAgMember && w > gap25Hours {
		rank++
	}
	changeType, effort := "None", EffortNone
	if w > gap25Hours {
		changeType, effort = "Planned", EffortModerate
	}
	finding := bak05Lost7
	if w < gap7Days {
		finding = "You could lose up to " + FormatGap(w) + " of data: the longest gap between backups in the last 7 days."
	}
	doThis := bak05DoFull
	if f.RecoveryModel == "SIMPLE" {
		doThis = bak05DoSimple
	}
	// No backup of any kind in 7 days is a schedule that stopped or never existed: the same fix
	// as BAK01, a daily full backup job (and log backups in FULL recovery).
	fixID := ""
	if w >= gap7Days {
		fixID, doThis = f.scheduleFix(JobFull, doThis)
		if fixID != "" {
			doThis = bak05DoApply + " " + doThis
		}
	}
	evidence := fmt.Sprintf("Backups in the last 7 days: %d full, %d differential, %d log. Last full %s, last log %s. Recovery model %s.",
		f.Fulls7, f.Diffs7, f.Logs7, sqlTimeOr(f.LastFull, "never"), sqlTimeOr(f.LastLog, "never"), f.RecoveryModel) +
		b.agNote("Availability Group: backups may run on another replica.")
	b.addFix("BAK05", rank, changeType, effort, finding, bak05Why, doThis, evidence, fixID)
}

// bak03: last good CHECKDB never, or more than 14 days ago; skipped when this version does not know.
func (b *backupBuild) bak03() {
	f := b.f
	if f.LastGoodCheckDb == nil {
		b.skipped["BAK03"] = checkDbUnknownNote
		if knowsLastCheckDb(f.EngineVersion) {
			b.skipped["BAK03"] = checkDbNotGiven
		}
		return
	}
	last := *f.LastGoodCheckDb
	if !last.Before(f.ServerNow.AddDate(0, 0, -14)) {
		return
	}
	rank, finding, when := 3, "", ""
	if checkDbNever(last) {
		rank, finding, when = 2, "No successful integrity check (DBCC CHECKDB) has ever been recorded.", "never"
	} else {
		finding = fmt.Sprintf("Last successful integrity check was %d days ago.", daysBetween(last, f.ServerNow))
		when = sqlTime(last)
		b.checkOld = true
	}
	fixID := ""
	if f.offersCheckdbJob() {
		fixID = FixCheckdbJob
	}
	b.addFix("BAK03", agBump(rank, f.IsAgMember), "Online", EffortQuickWin, finding, bak03Why, bak03Do,
		"Last good CHECKDB: "+when+"."+b.agNote("Database is in an Availability Group: checks may run on another replica."), fixID)
}

// bak04: suspect pages recorded for this database.
func (b *backupBuild) bak04() {
	f := b.f
	if f.SuspectPages <= 0 {
		return
	}
	b.addFix("BAK04", 1, "None", EffortNone,
		fmt.Sprintf("SQL Server has recorded %d damaged (suspect) page(s).", f.SuspectPages), bak04Why, bak04Do,
		"Most recent: "+sqlTimeOr(f.SuspectLast, "unknown")+".", f.suspectPagesFixID())
}

// dbc08: SIMPLE recovery.
func (b *backupBuild) dbc08() {
	if b.f.RecoveryModel != "SIMPLE" {
		return
	}
	b.addFix("DBC08", 5, "Planned", EffortModerate, "Database uses the SIMPLE recovery model.", dbc08Why, dbc08Do,
		"recovery_model = SIMPLE", FixRecoveryModel)
}

// bak06: full backups written to NUL in the last 30 days. Every differential based on one cannot
// be restored. When a real full backup followed the last NUL one, only the differentials in
// between are lost, and the sentence says so; the job that writes to NUL still exists, so it stays
// High.
func (b *backupBuild) bak06() {
	f := b.f
	if len(f.NulFulls) == 0 {
		return
	}
	last := f.NulFulls[0]
	for _, t := range f.NulFulls[1:] {
		if t.After(last) {
			last = t
		}
	}
	lost := "every differential since then cannot be restored."
	if f.LastRealFull != nil && f.LastRealFull.After(last) {
		lost = "the differentials taken between it and the next real full backup cannot be restored."
	}
	b.add("BAK06", 2, "Online", EffortQuickWin,
		fmt.Sprintf("%d full backup(s) in the last 30 days were written to NUL (thrown away), the last on %s: %s",
			len(f.NulFulls), sqlTime(last), lost),
		bak06Why, bak06Do,
		fmt.Sprintf("Full backups to NUL in 30 days: %d, the last %s. Last real full backup: %s. Differentials based on a NUL full are left out of the worst-case data loss (BAK05).",
			len(f.NulFulls), sqlTime(last), sqlTimeOr(f.LastRealFull, "none")))
}

// dbc12: delayed durability allowed (Low) or forced (Medium). A data-safety fact, so it is on the
// Backups card.
func (b *backupBuild) dbc12() {
	f := b.f
	if f.DurabilityError != "" {
		b.skipped["DBC12"] = "Could not read the delayed durability setting: " + f.DurabilityError
		return
	}
	rank := 0
	switch f.DelayedDurability {
	case "FORCED":
		rank = 3
	case "ALLOWED":
		rank = 4
	default:
		return
	}
	b.add("DBC12", rank, "Online", EffortQuickWin,
		fmt.Sprintf("%s has delayed durability %s: a committed transaction can be lost if SQL Server crashes.", f.Database, f.DelayedDurability),
		dbc12Why,
		"Us: unless the vendor asked for it, run ALTER DATABASE "+QuoteName(f.Database)+" SET DELAYED_DURABILITY = DISABLED; (online, takes effect at once).",
		"delayed_durability = "+f.DelayedDurability)
}

// facts are the four lines on the card.
func (b *backupBuild) facts() []Fact {
	f := b.f
	loss := Fact{Label: "Worst-case data loss", Value: "no backup", Tone: ToneWorse}
	if b.hasWorst {
		loss.Value, loss.Tone = FormatGap(b.worst), ToneNormal
		if x, ok := b.find("BAK05"); ok {
			switch x.Severity {
			case SeverityCritical, SeverityHigh:
				loss.Tone = ToneWorse
			case SeverityMedium:
				loss.Tone = ToneBad
			}
		}
	}

	full := Fact{Label: "Last full backup", Value: "never", Tone: ToneWorse}
	if f.LastFull != nil {
		full.Value = ago(*f.LastFull, f.ServerNow)
		full.Tone = tone(daysBetween(*f.LastFull, f.ServerNow) > 1)
	}

	check := Fact{Label: "Last CHECKDB", Value: "unknown on this version", Tone: ToneNormal}
	if f.LastGoodCheckDb == nil && knowsLastCheckDb(f.EngineVersion) {
		check.Value = "unknown"
	}
	if f.LastGoodCheckDb != nil {
		if checkDbNever(*f.LastGoodCheckDb) {
			check.Value, check.Tone = "never", ToneWorse
		} else {
			check.Value, check.Tone = ago(*f.LastGoodCheckDb, f.ServerNow), tone(b.checkOld)
		}
	}

	fourth := Fact{Label: "Log backups", Value: "not needed (SIMPLE)", Tone: ToneNormal}
	switch {
	case f.SuspectPages > 0:
		fourth = Fact{Label: "Damaged pages", Value: fmt.Sprint(f.SuspectPages), Tone: ToneWorse}
	case fullOrBulk(f.RecoveryModel) && f.Logs7 > 0:
		every := int(math.Round(float64(minutesBetween(windowStart(f), f.ServerNow)) / float64(f.Logs7)))
		fourth.Value = fmt.Sprintf("every %d min on average", max(every, 1))
	case fullOrBulk(f.RecoveryModel):
		fourth.Value, fourth.Tone = "none in "+f.RecoveryModel+" recovery", ToneBad
	}
	return []Fact{loss, full, check, fourth}
}

// windowStart is where the 7-day window begins: 7 days ago, or when the database was created if
// that is later.
func windowStart(f BackupFacts) time.Time {
	start := f.ServerNow.AddDate(0, 0, -7)
	if f.CreateDate.After(start) {
		return f.CreateDate
	}
	return start
}

// WorstGapMinutes is the longest gap, in minutes, between consecutive restorable points in the
// last 7 days, counting the start of the window and now as points, as the scan does.
func WorstGapMinutes(f BackupFacts) int {
	pts := make([]time.Time, 0, len(f.RestorePoints)+2)
	pts = append(pts, f.RestorePoints...)
	pts = append(pts, windowStart(f), f.ServerNow)
	sort.Slice(pts, func(i, j int) bool { return pts[i].Before(pts[j]) })
	worst := 0
	for i := 1; i < len(pts); i++ {
		worst = max(worst, minutesBetween(pts[i-1], pts[i]))
	}
	return worst
}

// FormatGap writes a gap in minutes as the scan does: "3.5 days" from 2 days, "7 hours" from 120
// minutes, else "45 minutes".
func FormatGap(minutes int) string {
	// Rounded in whole numbers, halves away from zero, as FORMAT does with a decimal.
	switch {
	case minutes >= gap2Days:
		tenths := (minutes*20 + 1440) / 2880
		return fmt.Sprintf("%d.%d days", tenths/10, tenths%10)
	case minutes >= 120:
		return fmt.Sprintf("%d hours", (minutes*2+60)/120)
	case minutes == 1:
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

// ago says when something happened, in the server's clock: "today 09:15", "1 day ago",
// "12 days ago".
func ago(t, now time.Time) string {
	switch d := daysBetween(t, now); {
	case d <= 0:
		return "today " + t.Format("15:04")
	case d == 1:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", d)
	}
}

func checkDbNever(t time.Time) bool { return !t.After(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)) }

// sqlTime writes a time as CONVERT(nvarchar(16), t, 120) does: 2026-11-03 09:21.
func sqlTime(t time.Time) string { return t.Format("2006-01-02 15:04") }

func sqlTimeOr(t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return sqlTime(*t)
}

// minutesBetween, hoursBetween and daysBetween count the boundaries crossed, as DATEDIFF does.
func minutesBetween(a, b time.Time) int {
	return int(b.Truncate(time.Minute).Sub(a.Truncate(time.Minute)) / time.Minute)
}

func hoursBetween(a, b time.Time) int {
	return int(b.Truncate(time.Hour).Sub(a.Truncate(time.Hour)) / time.Hour)
}

func daysBetween(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Round(db.Sub(da).Hours() / 24))
}
