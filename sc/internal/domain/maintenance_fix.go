package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The two fixes of the maintenance plan (docs/phase5b-contract.md): history-cleanup, the weekly job
// that prunes msdb's backup and job history (MNT08), and maintenance-plan, the composite that creates
// every missing piece through the pieces' own fixes, in an order and at times that do not overlap.

// Ids of the plan's fixes.
const (
	FixHistoryCleanup  = "history-cleanup"
	FixMaintenancePlan = "maintenance-plan"
)

// HistoryCleanupJobName is the job history-cleanup creates: one per server.
const HistoryCleanupJobName = "kakapo: history cleanup"

// The options of history-cleanup.
const (
	OptionHistoryKeepDays = "keepDays"
	OptionHistoryTime     = "time"
	OptionHistoryDay      = "day"
)

// The days of history kept: 30 by default (Ola Hallengren's installation keeps 30 days); never under 7,
// because the Backups card reads the last 7 days of backup history; at most a year.
const (
	DefaultHistoryKeepDays = 30
	minHistoryKeepDays     = 7
	maxHistoryKeepDays     = 365
)

// --- history-cleanup (MNT08) ---

// HistoryTable is one history table the cleanup prunes, counted with the days kept.
type HistoryTable struct {
	Label  string // "Backup history"
	Table  string // "msdb.dbo.backupset"
	Rows   int64
	Older  int64      // rows older than the days kept: what the first run deletes
	Oldest *time.Time // nil when the table is empty
}

// HistoryCleanupFacts is what history-cleanup reads (collect.ReadHistoryCleanupFacts), with the
// rows counted against KeepDays.
type HistoryCleanupFacts struct {
	ServerName     string
	ProductVersion string
	EngineEdition  int // 4 is Express: no Agent
	KeepDays       int
	Tables         []HistoryTable // backup, restore and job history, and CommandLog when it exists
	JobExists      bool           // kakapo's job exists already
	OtherJob       string         // another enabled job that prunes the history; "" none
	Operator       string         // the first enabled operator with an email address; "" none
	AgentStatus    string
	AgentError     string
	ServerNow      time.Time
}

// parseHistoryKeep reads the days kept: a whole number from 7 to 365.
func parseHistoryKeep(v string) (int, bool) {
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil || !allDigits(v) || n < minHistoryKeepDays || n > maxHistoryKeepDays {
		return 0, false
	}
	return n, true
}

// HistoryKeepDays is the days the cleanup keeps for these options: the keepDays option when it is
// valid, else the default. The service counts the rows with it before building the preview.
func HistoryKeepDays(options map[string]string) int {
	if v, ok := parseHistoryKeep(options[OptionHistoryKeepDays]); ok {
		return v
	}
	return DefaultHistoryKeepDays
}

// HistoryCleanupCommand is the job's one step (the contract's statement): backup and restore history,
// job history and Ola Hallengren's CommandLog older than the days kept.
func HistoryCleanupCommand(keepDays int) string {
	return fmt.Sprintf("DECLARE @oldest datetime = DATEADD(DAY, -%d, GETDATE());\n", keepDays) +
		"EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;\n" +
		"EXEC msdb.dbo.sp_purge_jobhistory @oldest_date = @oldest;\n" +
		"IF OBJECT_ID(N'master.dbo.CommandLog') IS NOT NULL DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;"
}

const (
	historyKeepHelp  = "Backup, restore and job history older than this is deleted every week: 7 to 365 days."
	historyTimeHelp  = "Weekly, server time. After the integrity check and index work, before the evening's full backup."
	historyDayHelp   = "The day of the week the cleanup runs."
	historyKeepWarn  = "Keep between 7 and 365 days: the Backups card reads the last 7 days of backup history, so fewer cannot be kept."
	historyTimeWarn  = "The time must be HH:MM, for example 05:00."
	historyFiles     = "The backup files themselves are not touched: this deletes only msdb's record of them, of restores and of job runs (sp_delete_backuphistory also deletes their rows in backupfile, backupfilegroup, backupmediafamily, backupmediaset, restorefile and restorefilegroup)."
	historyFloor     = "The Backups card reads the last 7 days of backup history, so fewer than 7 days cannot be kept; 30 days (Ola Hallengren's default) also keeps a month for questions about past backups, restores and job runs."
	historyNoUndo    = "There is no undoing the cleanup itself: the rollback deletes the job, but the history rows it has deleted are gone."
	historyExpress   = "SQL Server Express has no Agent: kakapo cannot create the job. Copy the script and schedule it weekly in Windows Task Scheduler (sqlcmd -E -i)."
	historyFirstRun  = "The first run deletes %s rows: on a large msdb that takes a while and holds locks on the history tables, which is why it runs at a quiet hour."
	historyOtherWarn = "The job %s already prunes history on this server: check what it keeps before adding a second cleanup."
)

// historyWords is "Sunday 05:00, keeps 30 days".
func historyWords(sc KakapoJobSchedule, keep int) string {
	return fmt.Sprintf("%s, keeps %d days", sc.words(), keep)
}

// BuildHistoryCleanupFix builds the history-cleanup preview with the options (keepDays, time, day):
// the weekly job with the one step, the rows each history table would lose now, and the warnings;
// on Express a script to schedule by hand. ErrFixNotApplicable when kakapo's job exists already. A
// keepDays outside 7 to 365, or a wrong time or day, leaves the preview without changes.
func BuildHistoryCleanupFix(f HistoryCleanupFacts, options map[string]string) (FixPreview, Fix, error) {
	if f.JobExists {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	express := f.EngineEdition == 4
	keep, clock, day := DefaultHistoryKeepDays, DefaultPlanCleanupTime, DefaultPlanDay
	blocked := []string{}
	keepIn := FixInput{Name: OptionHistoryKeepDays, Label: "Keep history (days)", Value: strconv.Itoa(keep), Kind: "text", Help: historyKeepHelp}
	if typed, given := options[OptionHistoryKeepDays]; given {
		if v, ok := parseHistoryKeep(typed); ok {
			keep, keepIn.Value = v, strconv.Itoa(v)
		} else {
			keepIn.Value, keepIn.Warning = strings.TrimSpace(typed), historyKeepWarn
			blocked = append(blocked, historyKeepWarn)
		}
	}
	timeIn := FixInput{Name: OptionHistoryTime, Label: "Time", Value: clock, Kind: "text", Help: historyTimeHelp, Advanced: true}
	if typed, given := options[OptionHistoryTime]; given {
		if v, ok := parseClock(typed); ok {
			clock, timeIn.Value = v, v
		} else {
			timeIn.Value, timeIn.Warning = strings.TrimSpace(typed), historyTimeWarn
			blocked = append(blocked, historyTimeWarn)
		}
	}
	dayIn := FixInput{Name: OptionHistoryDay, Label: "Day", Value: day, Kind: "select", Options: CheckdbDays, Help: historyDayHelp, Advanced: true}
	if typed, given := options[OptionHistoryDay]; given {
		if v, ok := checkdbDay(typed); ok {
			day, dayIn.Value = v, v
		} else {
			dayIn.Value, dayIn.Warning = strings.TrimSpace(typed), checkdbDayWarn
			blocked = append(blocked, checkdbDayWarn)
		}
	}
	inputs := []FixInput{keepIn, timeIn, dayIn}
	opts := map[string]string{}
	for _, in := range inputs {
		opts[in.Name] = in.Value
	}

	operator := f.Operator
	if express {
		operator = ""
	}
	sc := KakapoJobSchedule{Freq: JobWeekly, Days: checkdbDayBit(day), Clock: clock}
	sc.Name = HistoryCleanupJobName + ", " + sc.words()
	job := KakapoJob{Name: HistoryCleanupJobName, Operator: operator, Schedule: sc,
		Description: KakapoJobDescription(f.ServerNow.Format("2006-01-02"), f.ServerName, "history cleanup "+historyWords(sc, keep)),
		Steps:       []KakapoJobStep{{Name: fmt.Sprintf("Delete history older than %d days", keep), Command: HistoryCleanupCommand(keep)}}}

	fix := suFix(FixHistoryCleanup)
	fix.Current = historyCurrent(f.Tables)
	fix.Target = HistoryCleanupJobName + " (" + historyWords(sc, keep) + ")"
	fix.Options = opts
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Inputs: inputs, Options: opts, Express: express}
	p.Warnings = append(p.Warnings, blocked...)
	if f.OtherJob != "" {
		p.Warnings = append(p.Warnings, fmt.Sprintf(historyOtherWarn, f.OtherJob))
	}
	p.Warnings = append(p.Warnings, historyFiles, historyFloor, historyNoUndo)
	var older int64
	for _, t := range f.Tables {
		older += t.Older
	}
	if older > mnt08MediumRows {
		p.Warnings = append(p.Warnings, fmt.Sprintf(historyFirstRun, n0int(older)))
	}
	switch {
	case express:
		p.Warnings = append(p.Warnings, historyExpress)
	case f.AgentError == "" && f.AgentStatus != "" && f.AgentStatus != "Running":
		p.Warnings = append(p.Warnings, "SQL Server Agent is "+strings.ToLower(f.AgentStatus)+": the job will not run until it starts (MNT01).")
	}
	if express {
		p.Warnings = append(p.Warnings, "Needs sysadmin on "+f.ServerName+" for the Windows account that runs the scheduled task (sqlcmd -E logs in as that account).")
	} else {
		p.Warnings = append(p.Warnings, BackupFixPermission(f.ServerName))
	}
	if len(blocked) > 0 {
		return p, fix, nil
	}

	p.Changes = append(p.Changes, Drift{Setting: "History cleanup job", Current: "none", Target: HistoryCleanupJobName + ", " + historyWords(sc, keep)})
	for _, t := range f.Tables {
		current := "no rows"
		if t.Rows > 0 {
			current = n0int(t.Rows) + " rows" + sinceDay(t.Oldest)
		}
		target := fmt.Sprintf("nothing older than %d days yet", keep)
		if t.Older > 0 {
			target = fmt.Sprintf("%s rows older than %d days deleted at the first run", n0int(t.Older), keep)
		}
		p.Changes = append(p.Changes, Drift{Setting: t.Label + " (" + t.Table + ")", Current: current, Target: target})
	}
	told := noOperator
	switch {
	case express:
		told = "no one (Express has no Agent to send an email)"
	case operator != "":
		told = operator
	}
	p.Changes = append(p.Changes, Drift{Setting: "Tell on failure", Target: told})
	if express {
		p.Statement = fmt.Sprintf("-- kakapo: history cleanup on %s. SQL Server Express has no SQL Server Agent: schedule this %s in Windows Task Scheduler: sqlcmd -S %s -E -i <this file>\n%s\nGO",
			f.ServerName, sc.words(), f.ServerName, kakapoStepCommand(HistoryCleanupCommand(keep)))
		return p, fix, nil
	}
	p.Statement, p.Rollback = KakapoJobsBatch([]KakapoJob{job}), KakapoJobsDelete([]string{HistoryCleanupJobName})
	return p, fix, nil
}

// historyCurrent is what the cleanup finds: "4,200 backup and 900 job history rows, nothing prunes
// them".
func historyCurrent(tables []HistoryTable) string {
	var backups, jobs int64
	for _, t := range tables {
		switch t.Table {
		case "msdb.dbo.backupset":
			backups = t.Rows
		case "msdb.dbo.sysjobhistory":
			jobs = t.Rows
		}
	}
	return fmt.Sprintf("%s backup and %s job history rows, nothing prunes them", n0int(backups), n0int(jobs))
}

// HistoryCleanupDescription is the change log line of history-cleanup.
func HistoryCleanupDescription(fix Fix) string { return "Created the job " + fix.Target }

// historyCleanupOffer is history-cleanup as the Upkeep card lists it from MNT08.
func historyCleanupOffer(f UpkeepFacts) Fix {
	fix := suFix(FixHistoryCleanup)
	fix.Current = fmt.Sprintf("%s backup and %s job history rows, nothing prunes them", n0int(f.BackupHistoryRows), n0int(f.JobHistoryRows))
	sc := KakapoJobSchedule{Freq: JobWeekly, Days: checkdbDayBit(DefaultPlanDay), Clock: DefaultPlanCleanupTime}
	fix.Target = HistoryCleanupJobName + " (" + historyWords(sc, DefaultHistoryKeepDays) + ")"
	return fix
}

// --- maintenance-plan: the composite ---

// The options of maintenance-plan: one bool per piece it can create, the backup folder, the
// operator and its email, and (under More options) the times.
const (
	OptionPlanBackup      = "backup"
	OptionPlanCheck       = "check"
	OptionPlanStatistics  = "statistics"
	OptionPlanIndex       = "index"
	OptionPlanCleanup     = "cleanup"
	OptionPlanAlerts      = "alerts"
	OptionPlanFolder      = "folder"
	OptionPlanEmail       = "email"
	OptionPlanOperator    = "operator"
	OptionPlanFullTime    = "fullTime"
	OptionPlanStatsTime   = "statsTime"
	OptionPlanCheckDay    = "checkDay"
	OptionPlanCheckTime   = "checkTime"
	OptionPlanIndexTime   = "indexTime"
	OptionPlanCleanupTime = "cleanupTime"
	OptionPlanKeepDays    = "keepDays"
	OptionPlanLogMinutes  = "logMinutes"
)

// The steps of the composite, in apply order (the contract's): alerts, cleanup, check, statistics
// and index (one maintenance-job call), backup, so the backup job is last and the first full backup
// runs tonight after the rest are in place.
const (
	PlanStepAlerts      = "alerts"
	PlanStepCleanup     = "cleanup"
	PlanStepCheck       = "check"
	PlanStepMaintenance = "maintenance"
	PlanStepBackup      = "backup"
)

// PlanCall is one piece of the composite: the fix that creates it and the options it is prepared
// and applied with, and the plan's lines it creates.
type PlanCall struct {
	Step    string
	Title   string
	FixID   string
	Options map[string]string
	Pieces  []string
}

// PlanChoice is what the composite's options choose: the pieces to create in apply order, the
// inputs, the options echoed, why nothing can be applied (a bad value) and the warnings of the
// choice itself (times that overlap).
type PlanChoice struct {
	Calls    []PlanCall
	Inputs   []FixInput
	Options  map[string]string
	Blocked  []string
	Warnings []string
}

// planTimes are the composite's times: the weekly day and the clock of each piece.
type planTimes struct {
	day                                string
	check, index, cleanup, stats, full string
}

func defaultPlanTimes() planTimes {
	return planTimes{day: DefaultPlanDay, check: DefaultPlanCheckTime, index: DefaultPlanIndexTime, cleanup: DefaultPlanCleanupTime,
		stats: DefaultPlanStatsTime, full: DefaultFullTime}
}

// slots are when the pieces that are on start.
func (t planTimes) slots(on map[string]bool) []PlanSlot {
	bit := checkdbDayBit(t.day)
	out := []PlanSlot{}
	add := func(piece string, days int, clock string) {
		if on[piece] {
			out = append(out, PlanSlot{Piece: piece, Days: days, Clock: clock})
		}
	}
	add(PieceIntegrity, bit, t.check)
	add(PieceIndexes, bit, t.index)
	add(PieceCleanup, bit, t.cleanup)
	add(PieceStatistics, jobAllWeekday&^bit, t.stats)
	add(PieceFullBackup, jobAllWeekday, t.full)
	return out
}

// DefaultPlanSlots are when every piece starts with the default times.
func DefaultPlanSlots() []PlanSlot {
	return defaultPlanTimes().slots(map[string]bool{PieceIntegrity: true, PieceIndexes: true, PieceCleanup: true, PieceStatistics: true, PieceFullBackup: true})
}

const (
	planTimeWarn   = "The time must be HH:MM, for example 01:00."
	planFolderHelp = folderHelp
	planDayHelp    = "The day of the weekly work: the integrity check, index work and the history cleanup run on it, in that order; statistics run on the other nights."
)

// ChooseMaintenancePlan reads the composite's options against the plan: which of the creatable
// pieces are on (all of them by default, index work off), the values that go with them, and the
// calls to the pieces' own fixes in apply order with their options. ErrFixNotApplicable when the
// plan has nothing to create. A bad value is an input warning and a reason in Blocked.
func ChooseMaintenancePlan(plan MaintenancePlan, a PlanAlerts, options map[string]string) (PlanChoice, error) {
	if plan.FixID == "" {
		return PlanChoice{}, ErrFixNotApplicable
	}
	c := PlanChoice{Calls: []PlanCall{}, Inputs: []FixInput{}, Options: map[string]string{}, Blocked: []string{}, Warnings: []string{}}
	creatable := func(id string) bool {
		p, ok := plan.piece(id)
		// install-ola-index is the Index row's own step before index work: never part of the composite.
		return ok && p.FixID != "" && p.FixID != FixInstallOlaIndex && (p.Status == PlanMissing || p.Status == PlanDisabled || p.Status == PlanOptional)
	}
	full, logB := creatable(PieceFullBackup), creatable(PieceLogBackup)
	offer := map[string]bool{
		PlanStepBackup: full || logB, PlanStepCheck: creatable(PieceIntegrity), OptionPlanStatistics: creatable(PieceStatistics),
		OptionPlanIndex: creatable(PieceIndexes), PlanStepCleanup: creatable(PieceCleanup), PlanStepAlerts: creatable(PieceAlerts),
	}

	// The values first: the bools' help texts use them.
	t := defaultPlanTimes()
	var advanced []FixInput
	clock := func(name, label, help string, v *string) {
		in := FixInput{Name: name, Label: label, Value: *v, Kind: "text", Help: help, Advanced: true}
		if typed, given := options[name]; given {
			if x, ok := parseClock(typed); ok {
				*v, in.Value = x, x
			} else {
				in.Value, in.Warning = strings.TrimSpace(typed), planTimeWarn
				c.Blocked = append(c.Blocked, label+": "+planTimeWarn)
			}
		}
		advanced = append(advanced, in)
	}
	weekly := offer[PlanStepCheck] || offer[OptionPlanIndex] || offer[PlanStepCleanup] || offer[OptionPlanStatistics]
	if weekly {
		in := FixInput{Name: OptionPlanCheckDay, Label: "Maintenance day", Value: t.day, Kind: "select", Options: CheckdbDays, Help: planDayHelp, Advanced: true}
		if typed, given := options[OptionPlanCheckDay]; given {
			if v, ok := checkdbDay(typed); ok {
				t.day, in.Value = v, v
			} else {
				in.Value, in.Warning = strings.TrimSpace(typed), checkdbDayWarn
				c.Blocked = append(c.Blocked, checkdbDayWarn)
			}
		}
		advanced = append(advanced, in)
	}
	if offer[PlanStepCheck] {
		clock(OptionPlanCheckTime, "Integrity check at", checkdbTimeHelp, &t.check)
	}
	if offer[OptionPlanIndex] {
		clock(OptionPlanIndexTime, "Index maintenance at", "Server time, on the maintenance day, after the integrity check.", &t.index)
	}
	keep := DefaultHistoryKeepDays
	if offer[PlanStepCleanup] {
		clock(OptionPlanCleanupTime, "History cleanup at", historyTimeHelp, &t.cleanup)
		in := FixInput{Name: OptionPlanKeepDays, Label: "Keep history (days)", Value: strconv.Itoa(keep), Kind: "text", Help: historyKeepHelp, Advanced: true}
		if typed, given := options[OptionPlanKeepDays]; given {
			if v, ok := parseHistoryKeep(typed); ok {
				keep, in.Value = v, strconv.Itoa(v)
			} else {
				in.Value, in.Warning = strings.TrimSpace(typed), historyKeepWarn
				c.Blocked = append(c.Blocked, historyKeepWarn)
			}
		}
		advanced = append(advanced, in)
	}
	if offer[OptionPlanStatistics] {
		clock(OptionPlanStatsTime, "Statistics at", "Server time, every night except the maintenance day.", &t.stats)
	}
	logMinutes := DefaultLogMinutes
	if offer[PlanStepBackup] && full {
		clock(OptionPlanFullTime, "Full backup at", fullTimeHelp, &t.full)
	}
	if offer[PlanStepBackup] && logB {
		in := FixInput{Name: OptionPlanLogMinutes, Label: "Log backup every (minutes)", Value: strconv.Itoa(logMinutes), Kind: "select",
			Options: LogMinuteChoices, Help: logMinutesHelp, Advanced: true}
		if typed, given := options[OptionPlanLogMinutes]; given {
			if v, ok := parseLogMinutes(typed); ok {
				logMinutes, in.Value = v, strconv.Itoa(v)
			} else {
				in.Value, in.Warning = strings.TrimSpace(typed), logMinutesWarn
				c.Blocked = append(c.Blocked, logMinutesWarn)
			}
		}
		advanced = append(advanced, in)
	}

	// The pieces, one bool each.
	on := map[string]bool{}
	choose := func(name, label, help string, def bool) bool {
		v := def
		if typed, given := options[name]; given {
			v = strings.EqualFold(strings.TrimSpace(typed), "true")
		}
		c.Inputs = append(c.Inputs, FixInput{Name: name, Label: label, Value: fmt.Sprint(v), Kind: "bool", Help: help})
		c.Options[name] = fmt.Sprint(v)
		return v
	}
	if offer[PlanStepBackup] {
		label, help := "Full and log backups", fmt.Sprintf("A full backup daily at %s and a log backup every %d minutes, to the folder below (the Backups card's backup schedule). No backup is started now: the first full backup runs at %s.", t.full, logMinutes, t.full)
		switch {
		case !logB:
			label, help = "Full backup", fmt.Sprintf("A full backup daily at %s, to the folder below (the Backups card's backup schedule). No backup is started now.", t.full)
		case !full:
			label, help = "Log backup", fmt.Sprintf("A log backup every %d minutes, to the folder below (the Backups card's backup schedule).", logMinutes)
		}
		on[PlanStepBackup] = choose(OptionPlanBackup, label, help, true)
	}
	if offer[PlanStepCheck] {
		on[PlanStepCheck] = choose(OptionPlanCheck, "Weekly integrity check",
			fmt.Sprintf("DBCC CHECKDB every %s at %s; physical only (pages and checksums) from 200 GB of data.", t.day, t.check), true)
	}
	if offer[OptionPlanStatistics] {
		on[OptionPlanStatistics] = choose(OptionPlanStatistics, "Nightly statistics update",
			fmt.Sprintf("Every night except %s, at %s.", t.day, t.stats), true)
	}
	if offer[OptionPlanIndex] {
		on[OptionPlanIndex] = choose(OptionPlanIndex, "Weekly index maintenance (optional)",
			fmt.Sprintf("%s at %s: reorganise from 50%% fragmentation; from 80%%, rebuild online where the edition can and reorganise elsewhere, never offline; tables of 1,000 pages or more (Ola Hallengren's IndexOptimize, Brent Ozar's thresholds). Off by default: Microsoft no longer advises index maintenance on a fixed schedule, and statistics matter more.", t.day, t.index), false)
	}
	if offer[PlanStepCleanup] {
		on[PlanStepCleanup] = choose(OptionPlanCleanup, "Weekly history cleanup",
			fmt.Sprintf("%s at %s: backup and job history older than %d days deleted from msdb; the backup files are not touched.", t.day, t.cleanup, keep), true)
	}
	if offer[PlanStepAlerts] {
		on[PlanStepAlerts] = choose(OptionPlanAlerts, "Alerts and an operator",
			"One alert per severity 19 to 25 and per error 823, 824 and 825 (corruption), each mailing the operator.", true)
	}

	// The values the pieces that are on need, then the times.
	if on[PlanStepBackup] {
		in := FixInput{Name: OptionPlanFolder, Label: "Backup folder", Value: strings.TrimSpace(options[OptionPlanFolder]), Kind: "text", Help: planFolderHelp}
		c.Inputs = append(c.Inputs, in)
		if _, given := options[OptionPlanFolder]; given {
			c.Options[OptionPlanFolder] = in.Value
		}
	}
	newOperator := on[PlanStepAlerts] && a.Operators == 0
	if newOperator {
		name := strings.TrimSpace(options[OptionPlanOperator])
		if name == "" {
			name = DefaultOperatorName
		}
		c.Inputs = append(c.Inputs,
			FixInput{Name: OptionPlanOperator, Label: "Operator name", Value: name, Kind: "text", Help: operatorHelp},
			FixInput{Name: OptionPlanEmail, Label: "Email address", Value: strings.TrimSpace(options[OptionPlanEmail]), Kind: "text", Help: emailHelp})
		c.Options[OptionPlanOperator], c.Options[OptionPlanEmail] = name, strings.TrimSpace(options[OptionPlanEmail])
	}
	for _, in := range advanced {
		c.Inputs = append(c.Inputs, in)
		c.Options[in.Name] = in.Value
	}

	// The calls, in apply order.
	if on[PlanStepAlerts] {
		o := map[string]string{OptionCreateAlerts: "true"}
		if newOperator {
			o[OptionOperatorName] = c.Options[OptionPlanOperator]
			o[OptionOperatorEmail] = c.Options[OptionPlanEmail]
		}
		c.Calls = append(c.Calls, PlanCall{Step: PlanStepAlerts, Title: "Alerts and an operator", FixID: FixOperatorAlerts, Options: o, Pieces: []string{PieceAlerts}})
	}
	if on[PlanStepCleanup] {
		c.Calls = append(c.Calls, PlanCall{Step: PlanStepCleanup, Title: "History cleanup", FixID: FixHistoryCleanup,
			Options: map[string]string{OptionHistoryKeepDays: strconv.Itoa(keep), OptionHistoryTime: t.cleanup, OptionHistoryDay: t.day},
			Pieces:  []string{PieceCleanup}})
	}
	if on[PlanStepCheck] {
		c.Calls = append(c.Calls, PlanCall{Step: PlanStepCheck, Title: "Integrity check", FixID: FixCheckdbJob,
			Options: map[string]string{OptionCheckdbDay: t.day, OptionCheckdbTime: t.check}, Pieces: []string{PieceIntegrity}})
	}
	if stats, index := on[OptionPlanStatistics], on[OptionPlanIndex]; stats || index {
		o := map[string]string{OptionMaintTime: t.stats, OptionStatsOnly: fmt.Sprint(!index), OptionMaintDay: t.day, OptionMaintIndexTime: t.index}
		call := PlanCall{Step: PlanStepMaintenance, FixID: FixMaintenanceJob, Options: o}
		switch {
		case stats && index:
			call.Title, call.Pieces = "Statistics update and index maintenance", []string{PieceStatistics, PieceIndexes}
		case stats:
			call.Title, call.Pieces = "Statistics update", []string{PieceStatistics}
		default:
			call.Title, call.Pieces = "Index maintenance", []string{PieceIndexes}
			o[OptionMaintIndexOnly] = "true"
		}
		c.Calls = append(c.Calls, call)
	}
	if on[PlanStepBackup] {
		o := map[string]string{OptionRunNow: "false"}
		if full {
			o[OptionFullTime] = t.full
		}
		if logB {
			o[OptionLogMinutes] = strconv.Itoa(logMinutes)
		}
		if v, given := c.Options[OptionPlanFolder]; given {
			o[OptionFolder] = v
		}
		pieces := []string{}
		if full {
			pieces = append(pieces, PieceFullBackup)
		}
		if logB {
			pieces = append(pieces, PieceLogBackup)
		}
		title := "Backups"
		if !logB {
			title = "Full backup"
		} else if !full {
			title = "Log backup"
		}
		c.Calls = append(c.Calls, PlanCall{Step: PlanStepBackup, Title: title, FixID: FixBackupSchedule, Options: o, Pieces: pieces})
	}

	// Times that start within the same hour on a common day.
	slotsOn := map[string]bool{PieceIntegrity: on[PlanStepCheck], PieceIndexes: on[OptionPlanIndex], PieceCleanup: on[PlanStepCleanup],
		PieceStatistics: on[OptionPlanStatistics], PieceFullBackup: on[PlanStepBackup] && full}
	c.Warnings = append(c.Warnings, PlanOverlaps(t.slots(slotsOn))...)
	return c, nil
}

// planStatusWords are the statuses as the read-back says them.
var planStatusWords = map[string]string{
	PlanInPlace: "in place", PlanOtherJob: "done by another job", PlanMissing: "still missing", PlanDisabled: "disabled",
	PlanFailing: "failing", PlanNeverRan: "created, not run yet", PlanNotNeeded: "not needed", PlanOptional: "not created",
	PlanUnknown: "could not be read",
}

// PlanReadBack says where the pieces the composite created stand on the plan read again, and
// whether every one is in place (a job created now has not run yet, which is as created).
func PlanReadBack(after MaintenancePlan, pieces []string) (string, bool) {
	parts, ok := []string{}, true
	for _, id := range pieces {
		p, found := after.piece(id)
		if !found {
			parts, ok = append(parts, planPieceDefFor(id).Title+": not in the plan read"), false
			continue
		}
		parts = append(parts, p.Title+": "+planStatusWords[p.Status])
		switch p.Status {
		case PlanInPlace, PlanNeverRan, PlanNotNeeded, PlanOtherJob:
		default:
			ok = false
		}
	}
	if len(parts) == 0 {
		return "nothing was created.", false
	}
	return strings.Join(parts, "; ") + ".", ok
}

// PlanAppliedDescription is the summary change log line of the composite: what was created, and
// where it stopped and what was not created.
func PlanAppliedDescription(server string, created []string, stoppedAt, why string, notCreated []string) string {
	s := "Maintenance plan on " + server + ": created " + orNone(created)
	if stoppedAt != "" {
		s += "; stopped at " + stoppedAt + " (" + why + ")"
		if len(notCreated) > 0 {
			s += "; not created: " + strings.Join(notCreated, ", ")
		}
	}
	return s
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "nothing"
	}
	return strings.Join(xs, ", ")
}
