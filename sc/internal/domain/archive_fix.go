package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The two archive fixes (docs/phase5a-contract.md): "Archive or delete old rows" (archive-table),
// whose preview is the whole decision, and "Stop archiving a table" (remove-archive).

// ErrNoArchiveJob: kakapo does not archive the table (the remove fix has nothing to remove).
var ErrNoArchiveJob = errors.New("kakapo does not archive this table")

// archiveFirstBatchMarker separates, in the archive preview's statement, the creation batch from the
// one batch run after the read-back (firstRun "move one batch now").
const archiveFirstBatchMarker = "\n\n-- After the job is created and read back, one batch now:\n"

// archiveIndexedMark marks an indexed date column in the dateColumn select.
const archiveIndexedMark = " (indexed)"

// Texts of the archive fixes.
const (
	archiveTableHelp      = "The table whose old rows leave it, chosen from the finding or the table picker."
	archiveDateHelp       = "The column that says how old a row is. Indexed columns are listed first and marked: on those each batch finds its rows at once instead of reading the whole table."
	archiveKeepHelp       = "Rows older than this many months leave the table each night (1 to 120)."
	archiveKeepWarn       = "Keep between 1 and 120 months."
	archiveActionHelp     = "archive copies each row into the archive table and deletes it in the same statement; delete removes it and keeps no copy."
	archiveActionWarn     = "Choose archive or delete."
	archiveNameHelp       = "How the archive table is named: schema puts it in an archive schema under the same name (archive.MailLog), suffix adds _Archive (MailLog_Archive), prefix puts Archive_ in front (Archive_MailLog)."
	archiveNameWarn       = "Choose schema, suffix or prefix."
	archiveToHelp         = "Where the archive table lives. A separate archive database keeps the live database, its backups and its integrity checks small; kakapo picks it when it exists."
	archiveConditionHelp  = "Optional: only the old rows that also match this, for example Status = 'Closed'. The table's columns, comparisons (= <> < > <= >=), AND, OR, NOT, IN, LIKE, IS NULL, BETWEEN, text and numbers, and the functions GETDATE, SYSDATETIME, DATEADD, DATEDIFF, CONVERT and CAST."
	archiveScheduleHelp   = "Daily, server time: a quiet hour, after the night's backup."
	archiveScheduleWarn   = "The time must be HH:MM, for example 02:30."
	archiveBatchHelp      = "Rows per statement (500 to 50,000): small enough that each delete holds its locks only briefly."
	archiveBatchWarn      = "Type a whole number of rows from 500 to 50000."
	archiveMinutesHelp    = "The job stops after this many minutes each night and carries on the next (5 to 240), so a backlog is worked off over several nights."
	archiveMinutesWarn    = "Type a whole number of minutes from 5 to 240."
	archiveTidyHelp       = "Adds a second step that reorganises the table's indexes and updates its statistics after each night's run. Worth it when the first catch-up is large."
	archiveFirstRunHelp   = "nothing: the first batch runs tonight. move one batch now: one batch runs as soon as the job is created, and the result says how many rows it moved."
	archiveFirstRunWarn   = "Choose nothing or move one batch now."
	archiveTriggerWarn    = "a delete trigger fires for every batch; the application may be counting on it."
	archiveOwnerConfirm   = "Confirm with whoever owns the application that rows older than %d months are not needed (reports, integrations, audits by law)."
	archiveDeleteSentence = "This deletes application data and keeps no copy. " + archiveOwnerConfirm
	archiveKeepSentence   = "This deletes application data. " + archiveOwnerConfirm
)

// archivePlan resolves the options against the table read: the plan, the inputs as the dialog shows
// them, the options echoed (canonical), and the warnings of the options that are not usable (each
// blocks the preview).
func (f ArchiveFacts) archivePlan(options map[string]string) (ArchivePlan, []FixInput, map[string]string, []string) {
	plan := ArchivePlan{Database: f.Database, Schema: f.Schema, Table: f.Table, KeepMonths: archiveDefaultKeepMonths,
		Action: ArchiveActionArchive, ArchiveName: ArchiveNameSchema, ArchiveTo: defaultArchiveTo(f), Schedule: archiveDefaultSchedule,
		BatchRows: archiveDefaultBatchRows, MaxMinutes: archiveDefaultMaxMinutes, Created: f.ServerNow.Format("2006-01-02")}
	echo := map[string]string{OptionTable: archiveTableOption(f.Schema, f.Table)}
	inputs := []FixInput{{Name: OptionTable, Label: "Table", Value: echo[OptionTable], Help: archiveTableHelp, Kind: "text", ReadOnly: true}}
	bad := []string{}
	// add appends an input; a warning on it that is not usable blocks.
	add := func(in FixInput, blocking bool) {
		if in.Warning != "" && blocking {
			bad = append(bad, in.Warning)
		}
		inputs = append(inputs, in)
		echo[in.Name] = in.Value
	}
	typed := func(name string) (string, bool) {
		v, ok := options[name]
		return strings.TrimSpace(v), ok
	}

	// The date column: the first indexed one by default.
	if dates := f.DateColumns(); len(dates) > 0 {
		in := FixInput{Name: OptionArchiveDateColumn, Label: "Date column", Help: archiveDateHelp, Kind: "select"}
		for _, c := range dates {
			in.Options = append(in.Options, dateChoice(c))
		}
		chosen := dates[0]
		if v, given := typed(OptionArchiveDateColumn); given {
			if c, ok := f.column(strings.TrimSuffix(v, archiveIndexedMark)); ok && c.IsDate() {
				chosen = c
			} else {
				in.Warning = "Pick one of the table's date columns."
			}
		}
		plan.DateColumn, plan.DateIndexed = chosen.Name, chosen.DateIndexed
		in.Value = dateChoice(chosen)
		if in.Warning != "" {
			in.Value = strings.TrimSpace(options[OptionArchiveDateColumn])
		}
		add(in, true)
		echo[OptionArchiveDateColumn] = plan.DateColumn
		if in.Warning != "" {
			echo[OptionArchiveDateColumn] = in.Value
		}
	}

	whole := func(name, label, help, warn string, def, lo, hi int, advanced bool) int {
		in := FixInput{Name: name, Label: label, Help: help, Kind: "text", Value: strconv.Itoa(def), Advanced: advanced}
		v := def
		if s, given := typed(name); given {
			if n, ok := parseWholeNumber(s, lo, hi); ok {
				v, in.Value = n, strconv.Itoa(n)
			} else {
				in.Value, in.Warning = s, warn
			}
		}
		add(in, true)
		return v
	}
	choice := func(name, label, help, warn, def string, choices []string, advanced bool) string {
		in := FixInput{Name: name, Label: label, Help: help, Kind: "select", Options: choices, Value: def, Advanced: advanced}
		v := def
		if s, given := typed(name); given {
			found := false
			for _, c := range choices {
				if strings.EqualFold(s, c) {
					v, in.Value, found = c, c, true
				}
			}
			if !found {
				in.Value, in.Warning = s, warn
			}
		}
		add(in, true)
		return v
	}

	plan.KeepMonths = whole(OptionArchiveKeepMonths, "Months to keep", archiveKeepHelp, archiveKeepWarn, archiveDefaultKeepMonths, 1, archiveMaxKeepMonths, false)

	// The action: delete is allowed, with the red warning under the field (it does not block).
	action := FixInput{Name: OptionArchiveAction, Label: "Archive or delete", Help: archiveActionHelp, Kind: "select",
		Options: []string{ArchiveActionArchive, ArchiveActionDelete}, Value: ArchiveActionArchive}
	if s, given := typed(OptionArchiveAction); given {
		switch strings.ToLower(s) {
		case ArchiveActionArchive, ArchiveActionDelete:
			action.Value = strings.ToLower(s)
		default:
			action.Value, action.Warning = s, archiveActionWarn
		}
	}
	if action.Warning != "" {
		add(action, true)
	} else {
		plan.Action = action.Value
		if plan.Action == ArchiveActionDelete {
			action.Warning = fmt.Sprintf(archiveDeleteSentence, plan.KeepMonths)
		}
		add(action, false)
	}

	plan.ArchiveName = choice(OptionArchiveName, "Archive table name", archiveNameHelp, archiveNameWarn, ArchiveNameSchema,
		[]string{ArchiveNameSchema, ArchiveNameSuffix, ArchiveNamePrefix}, true)

	archiveDB := ArchiveDatabaseName(f.Database)
	to := FixInput{Name: OptionArchiveTo, Label: "Archive to", Help: archiveToHelp, Kind: "select",
		Options: []string{ArchiveToSame, archiveDB}, Value: plan.ArchiveTo, Advanced: true}
	if s, given := typed(OptionArchiveTo); given {
		switch {
		case strings.EqualFold(s, ArchiveToSame):
			to.Value = ArchiveToSame
		case strings.EqualFold(s, archiveDB):
			to.Value = archiveDB
		default:
			to.Value, to.Warning = s, "Choose the same database or "+archiveDB+"."
		}
	}
	if to.Warning == "" {
		plan.ArchiveTo = to.Value
		if to.Value == archiveDB && !f.ArchiveDBExists && plan.Archives() {
			to.Warning = "There is no database named " + archiveDB + " on this server: choose the same database, or create the archive database first."
		}
	}
	add(to, plan.Archives() || to.Value != archiveDB && to.Value != ArchiveToSame)

	cond := FixInput{Name: OptionArchiveCondition, Label: "Only rows where (optional)", Help: archiveConditionHelp, Kind: "text", Advanced: true}
	if s, given := typed(OptionArchiveCondition); given && s != "" {
		if c, err := CanonicalCondition(s, f.ColumnNames()); err != nil {
			cond.Value, cond.Warning = s, "The condition: "+err.Error()+"."
		} else {
			cond.Value, plan.Condition = c, c
		}
	}
	add(cond, true)

	sched := FixInput{Name: OptionArchiveSchedule, Label: "Daily at", Help: archiveScheduleHelp, Kind: "text", Value: archiveDefaultSchedule, Advanced: true}
	if s, given := typed(OptionArchiveSchedule); given {
		if c, ok := parseClock(s); ok {
			sched.Value, plan.Schedule = c, c
		} else {
			sched.Value, sched.Warning = s, archiveScheduleWarn
		}
	}
	add(sched, true)

	plan.BatchRows = whole(OptionArchiveBatchRows, "Rows per statement", archiveBatchHelp, archiveBatchWarn, archiveDefaultBatchRows, archiveMinBatchRows, archiveMaxBatchRows, true)
	plan.MaxMinutes = whole(OptionArchiveMaxMinutes, "Minutes a night", archiveMinutesHelp, archiveMinutesWarn, archiveDefaultMaxMinutes, archiveMinMaxMinutes, archiveMaxMaxMinutes, true)

	tidy := FixInput{Name: OptionArchiveTidyUp, Label: "Tidy up the indexes and statistics afterwards", Help: archiveTidyHelp, Kind: "bool", Value: "false", Advanced: true}
	if s, given := typed(OptionArchiveTidyUp); given && strings.EqualFold(s, "true") {
		tidy.Value, plan.TidyUp = "true", true
	}
	add(tidy, false)

	first := choice(OptionArchiveFirstRun, "When the job is created", archiveFirstRunHelp, archiveFirstRunWarn, ArchiveFirstRunNothing,
		[]string{ArchiveFirstRunNothing, ArchiveFirstRunOneBatch}, true)
	plan.FirstRun = first == ArchiveFirstRunOneBatch

	plan.Target = archiveTargetRef(f.Database, f.Schema, f.Table, plan.ArchiveName, plan.ArchiveTo)
	for _, c := range f.Columns {
		if c.Copied() {
			plan.Columns = append(plan.Columns, c.Name)
			plan.Identity = plan.Identity || c.Identity
		}
		plan.HasArchived = plan.HasArchived || strings.EqualFold(c.Name, archivedAtColumn)
	}
	plan.CreateTarget = !f.targetIs(plan.Target) || !f.Target.Exists
	return plan, inputs, echo, bad
}

// dateChoice is a date column as the select offers it: marked when it is indexed.
func dateChoice(c ArchiveColumn) string {
	if c.DateIndexed {
		return c.Name + archiveIndexedMark
	}
	return c.Name
}

// targetIs says whether the target read is the archive table the plan names.
func (f ArchiveFacts) targetIs(t ArchiveTarget) bool {
	return f.Target.Read && strings.EqualFold(f.Target.Database, t.Database) &&
		strings.EqualFold(f.Target.Schema, t.Schema) && strings.EqualFold(f.Target.Table, t.Table)
}

// targetMismatch says how an existing archive table differs from the columns the copy carries: a
// copied column missing or of another type, or an extra column other than ArchivedAt. "" when it
// matches.
func (f ArchiveFacts) targetMismatch(plan ArchivePlan) string {
	have := map[string]ArchiveColumn{}
	for _, c := range f.Target.Columns {
		have[strings.ToLower(c.Name)] = c
	}
	diffs := []string{}
	want := map[string]bool{}
	for _, name := range plan.Columns {
		src, _ := f.column(name)
		want[strings.ToLower(name)] = true
		t, ok := have[strings.ToLower(name)]
		switch {
		case !ok:
			diffs = append(diffs, name+" is missing")
		case !strings.EqualFold(t.Type, src.Type):
			diffs = append(diffs, fmt.Sprintf("%s is %s, not %s", name, t.Type, src.Type))
		case t.Identity != src.Identity && plan.Archives():
			diffs = append(diffs, name+" differs in its identity property")
		}
	}
	for _, c := range f.Target.Columns {
		if !want[strings.ToLower(c.Name)] && !strings.EqualFold(c.Name, archivedAtColumn) {
			diffs = append(diffs, c.Name+" is extra")
		}
	}
	return strings.Join(diffs, ", ")
}

// pageCompression says whether the edition can PAGE-compress the archive table: every edition from
// SQL Server 2016 SP1 (13.0.4001), Enterprise and Developer before that.
func (f ArchiveFacts) pageCompression() bool {
	if f.EngineEdition == 3 {
		return true
	}
	parts := strings.Split(f.ProductVersion, ".")
	major := EngineMajor(f.ProductVersion)
	if major >= 14 {
		return true
	}
	if major == 13 && len(parts) >= 3 {
		build, _ := strconv.Atoi(parts[2])
		return build >= 4001
	}
	return false
}

// archiveBlocks are the reasons kakapo does not archive the table as asked, in the order the
// preview names them; any one leaves the preview without changes.
func (f ArchiveFacts) archiveBlocks(plan ArchivePlan) []string {
	t := tableLabel(f.Schema, f.Table)
	out := []string{}
	for _, e := range []struct{ what, err string }{
		{"the table", f.TableError}, {"the columns", f.ColumnsError}, {"the foreign keys", f.KeysError},
		{"the triggers", f.TriggersError}, {"the features (CDC, change tracking, replication, temporal)", f.FeaturesError},
	} {
		if e.err != "" {
			out = append(out, fmt.Sprintf("Could not read %s of %s, which the checks need: %s", e.what, t, e.err))
		}
	}
	if len(f.KeysIn) > 0 {
		names := []string{}
		for _, k := range f.KeysIn {
			names = append(names, k.FromTable+" ("+k.Name+")")
		}
		out = append(out, fmt.Sprintf("Other rows point at %s through foreign keys (a column there must match a row here): %s. Rows that are pointed at cannot be deleted, and archiving a parent table is a design question for the application's owner: kakapo does not archive it.",
			t, strings.Join(names, ", ")))
	}
	if f.CDC {
		out = append(out, "Change data capture (CDC: SQL Server records every change for another system to read) is on for "+t+": every deleted row would be passed on as a change. Decide with the owner first; kakapo does not archive it.")
	}
	if f.ChangeTracking {
		out = append(out, "Change tracking (SQL Server notes which rows changed, for a sync) is on for "+t+": the deletes would reach whatever syncs from it. Decide with the owner first; kakapo does not archive it.")
	}
	if f.Published || f.MergePublished || f.Replicated {
		out = append(out, t+" is replicated (copied to another server by SQL Server replication): the deletes would be copied too. Decide with the owner first; kakapo does not archive it.")
	}
	switch {
	case strings.Contains(strings.ToUpper(f.Temporal), "HISTORY"):
		out = append(out, t+" is the history table of a temporal table (SQL Server keeps it itself): kakapo does not archive it.")
	case f.Temporal != "" && !strings.EqualFold(f.Temporal, "NON_TEMPORAL_TABLE"):
		out = append(out, t+" is a temporal table (SQL Server keeps every old version of its rows in a history table): deleting rows has effects the owner must decide on. kakapo does not archive it.")
	}
	if f.MemoryOptimized {
		out = append(out, t+" is memory-optimised (held in memory, In-Memory OLTP): kakapo does not archive it.")
	}
	for _, tr := range f.Triggers {
		if tr.Enabled && tr.InsteadOf && tr.firesOnDelete() {
			out = append(out, fmt.Sprintf("The trigger %s replaces every delete on %s (INSTEAD OF DELETE): the batches would not remove rows. kakapo does not archive it.", tr.Name, t))
		}
	}
	if f.ColumnsError == "" && len(f.DateColumns()) == 0 {
		out = append(out, t+" has no date column (datetime, datetime2 or date), so kakapo cannot tell how old a row is.")
	}
	if j, ok := f.JobFor(); ok {
		out = append(out, fmt.Sprintf("kakapo already archives %s (the job %s): stop it first (\"Stop archiving a table\") to change how.", t, j.Name))
	}
	if f.EngineEdition == 4 {
		out = append(out, "SQL Server Express has no SQL Server Agent: kakapo cannot schedule the archive here.")
	}
	if plan.Archives() && plan.DateColumn != "" && f.targetIs(plan.Target) {
		switch {
		case f.Target.Error != "":
			out = append(out, fmt.Sprintf("Could not read the archive table %s: %s", plan.TargetWords(), f.Target.Error))
		case f.Target.Exists:
			if diff := f.targetMismatch(plan); diff != "" {
				out = append(out, fmt.Sprintf("The table %s already exists with other columns (%s): kakapo does not write into it. Choose another archive table name, or rename it.",
					plan.TargetWords(), diff))
			}
		}
	}
	if plan.DateColumn != "" && f.Estimate.Error != "" && !f.Estimate.TimedOut && plan.Condition != "" {
		out = append(out, "SQL Server did not accept the condition: "+f.Estimate.Error)
	}
	return out
}

// archiveWarnings are what the preview says before Apply, ending with the owner's confirmation.
func (f ArchiveFacts) archiveWarnings(plan ArchivePlan) []string {
	t := tableLabel(f.Schema, f.Table)
	out := []string{}
	deleteTriggers := []string{}
	for _, tr := range f.Triggers {
		if tr.Enabled && !tr.InsteadOf && tr.firesOnDelete() {
			deleteTriggers = append(deleteTriggers, tr.Name)
		}
	}
	if len(deleteTriggers) > 0 {
		word, verb := "The trigger ", " fires"
		if len(deleteTriggers) > 1 {
			word, verb = "The triggers ", " fire"
		}
		out = append(out, word+strings.Join(deleteTriggers, ", ")+" on "+t+verb+" on DELETE: "+archiveTriggerWarn)
	}
	if plan.DateColumn != "" && !plan.DateIndexed {
		out = append(out, fmt.Sprintf("%s is not the first column of an index, so each batch scans the table: create an index on %s first (\"Create the suggested index\" when the Performance card suggests one, or by hand).",
			plan.DateColumn, plan.DateColumn))
	}
	if plan.Archives() {
		for _, c := range f.Columns {
			if c.Identity {
				out = append(out, "The identity column "+c.Name+" keeps its values in the archive (IDENTITY_INSERT), so an archived row stays traceable by its id.")
			}
		}
		left := []string{}
		for _, c := range f.Columns {
			if !c.Copied() {
				left = append(left, c.Name)
			}
		}
		if len(left) > 0 {
			out = append(out, "Left out of the archive: "+strings.Join(left, ", ")+" (computed or rowversion columns: SQL Server works them out or stamps them itself, so they cannot be copied).")
		}
	}
	if f.KeysOut > 0 {
		out = append(out, fmt.Sprintf("%s has %d foreign key(s) to other tables: deleting its rows is fine, and the archive copy does not carry them.", t, f.KeysOut))
	}
	due := f.Rows
	if f.Estimate.Known {
		due = f.Estimate.Rows
	}
	rowMB := 0.0
	if f.Rows > 0 {
		rowMB = f.MB / float64(f.Rows)
	}
	if plan.Archives() && due > 0 {
		need := rowMB * float64(due)
		free := f.FreeMB
		if !strings.EqualFold(plan.Target.Database, f.Database) {
			free = f.ArchiveFreeMB
		}
		switch {
		case free == nil:
			msg := fmt.Sprintf("The archive copy needs about %s MB in %s before the deletes free the same in %s; the drive's free space could not be read", n0(need), plan.Target.Database, f.Database)
			if f.RoomError != "" {
				msg += " (" + f.RoomError + ")"
			}
			out = append(out, msg+".")
		case need > float64(*free)*0.9:
			out = append(out, fmt.Sprintf("The archive copy needs about %s MB in %s, more than the %s MB free on its drive: archive to another database, archive fewer months at a time, or free space first. The space the deletes free in %s is reused by new rows, not given back to the disk.",
				n0(need), plan.Target.Database, n0int(*free), f.Database))
		default:
			out = append(out, fmt.Sprintf("The archive copy needs about %s MB in %s (%s MB free on its drive). The space the deletes free in %s is reused by new rows, not given back to the disk.",
				n0(need), plan.Target.Database, n0int(*free), f.Database))
		}
	}
	if fullOrBulk(f.RecoveryModel) && due > 0 {
		night := min(due, plan.rowsPerNight())
		factor := 1.0
		if plan.Archives() && strings.EqualFold(plan.Target.Database, f.Database) {
			factor = 2 // the copy and the delete, both in this database's log
		}
		msg := fmt.Sprintf("%s is in %s recovery: each night's batches write about %s MB to the log, which only a log backup frees.",
			f.Database, f.RecoveryModel, n0(rowMB*float64(night)*factor))
		switch {
		case f.LogBackupError != "":
			msg += " The log backup history could not be read (" + f.LogBackupError + "): check that the log backup job runs (BAK02)."
		case f.LastLogBackup == nil:
			msg += " There is no log backup in the history: create the log backup job first (BAK02), or the log grows every night."
		default:
			msg += " The last log backup was at " + sqlTime(*f.LastLogBackup) + "; the log backup job must keep running (BAK02)."
		}
		out = append(out, msg)
	}
	if f.EngineEdition != 4 && f.AgentError == "" && f.AgentStatus != "" && f.AgentStatus != "Running" {
		out = append(out, "SQL Server Agent is stopped ("+f.AgentStatus+"): the job will not run until it starts (MNT01).")
	}
	out = append(out, BackupFixPermission(f.ServerName))
	if plan.Archives() {
		out = append(out, fmt.Sprintf(archiveKeepSentence, plan.KeepMonths))
	} else {
		out = append(out, fmt.Sprintf(archiveDeleteSentence, plan.KeepMonths))
	}
	return out
}

// archiveChanges are the preview's rows: the table, the destination, the rows due now and the
// nights they take, the job.
func (f ArchiveFacts) archiveChanges(plan ArchivePlan) []Drift {
	oldest := "oldest unknown"
	switch {
	case f.Estimate.Oldest != nil:
		oldest = "oldest " + f.Estimate.Oldest.Format("2006-01-02")
	case !plan.DateIndexed:
		oldest = "oldest not read (" + plan.DateColumn + " is not indexed)"
	}
	leave := fmt.Sprintf("rows older than %d months leave nightly", plan.KeepMonths)
	if plan.Condition != "" {
		leave = fmt.Sprintf("rows older than %d months where %s leave nightly", plan.KeepMonths, plan.Condition)
	}
	out := []Drift{{Setting: "Table", Current: fmt.Sprintf("%s rows, %s MB, %s", n0int(f.Rows), n0(f.MB), oldest), Target: leave}}

	dest := Drift{Setting: "Destination", Current: "none", Target: "deleted, not kept"}
	if plan.Archives() {
		dest.Target = plan.TargetWords()
		if !plan.CreateTarget {
			dest.Current = fmt.Sprintf("%s (exists, %s rows)", plan.TargetWords(), n0int(f.Target.Rows))
			dest.Target = plan.TargetWords() + ", added to"
		}
	}
	out = append(out, dest)

	pace := fmt.Sprintf("%s rows per statement and %d minutes a night", n0int(int64(plan.BatchRows)), plan.MaxMinutes)
	rows := Drift{Setting: "Rows affected now"}
	switch e := f.Estimate; {
	case e.Known:
		how := "counted"
		if e.Estimated {
			how = "estimated from a 1% sample"
		}
		n := plan.nights(e.Rows)
		nights := "1 night"
		if n != 1 {
			nights = fmt.Sprintf("%s nights", n0int(n))
		}
		rows.Target = fmt.Sprintf("about %s (%s), %s at %s", n0int(e.Rows), how, nights, pace)
	case e.TimedOut:
		rows.Target = "unknown (counting took longer than 10 seconds), at " + pace
	default:
		rows.Target = "unknown, at " + pace
	}
	out = append(out, rows)

	job := plan.JobName() + ", " + plan.scheduleWords()
	if plan.FirstRun {
		job += ", one batch now"
	}
	return append(out, Drift{Setting: "Job", Current: "none", Target: job})
}

// BuildArchiveFix builds the preview of "Archive or delete old rows" from the table read and the
// options (docs/phase5a-contract.md). A blocking reason (foreign keys pointing at the table, CDC,
// change tracking, replication, temporal, memory-optimised, no date column, an archive table with
// other columns, a missing archive database, a bad option) leaves it without changes: the warnings
// say why, first.
func BuildArchiveFix(f ArchiveFacts, options map[string]string) (FixPreview, Fix) {
	d, _ := FindFixDef(FixArchiveTable)
	plan, inputs, echo, bad := f.archivePlan(options)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: "no archive job", Target: plan.JobName() + ", " + plan.scheduleWords(),
		Options: map[string]string{OptionTable: echo[OptionTable]}}
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{},
		Inputs: inputs, Options: echo}
	blocks := append(f.archiveBlocks(plan), bad...)
	p.Warnings = append(append(p.Warnings, blocks...), f.archiveWarnings(plan)...)
	if len(blocks) > 0 {
		return p, fix
	}
	p.Changes = f.archiveChanges(plan)
	p.Statement = plan.Batch(f.pageCompression())
	if plan.FirstRun {
		p.Statement += archiveFirstBatchMarker + plan.FirstBatch()
	}
	p.Rollback = plan.Rollback()
	return p, fix
}

// SplitArchiveStatement splits the archive preview's statement into the creation batch and the one
// batch run after the read-back ("" when firstRun is nothing).
func SplitArchiveStatement(stmt string) (create, firstBatch string) {
	create, firstBatch, _ = strings.Cut(stmt, archiveFirstBatchMarker)
	return create, firstBatch
}

// ArchivePlanFromPreview is the plan a preview was built with, from its echoed options (canonical,
// built by BuildArchiveFix): enough for the apply to name the job and the archive table, read them
// back and describe the change. The columns are not in it.
func ArchivePlanFromPreview(database string, p FixPreview) (ArchivePlan, error) {
	o := p.Options
	_, schema, table, err := SplitTableName(o[OptionTable])
	if err != nil {
		return ArchivePlan{}, fmt.Errorf("the table: %w", err)
	}
	plan := ArchivePlan{Database: database, Schema: schema, Table: table, DateColumn: o[OptionArchiveDateColumn],
		Action: o[OptionArchiveAction], ArchiveName: o[OptionArchiveName], ArchiveTo: o[OptionArchiveTo],
		Condition: o[OptionArchiveCondition], Schedule: o[OptionArchiveSchedule],
		TidyUp: o[OptionArchiveTidyUp] == "true", FirstRun: o[OptionArchiveFirstRun] == ArchiveFirstRunOneBatch}
	plan.KeepMonths, _ = strconv.Atoi(o[OptionArchiveKeepMonths])
	plan.BatchRows, _ = strconv.Atoi(o[OptionArchiveBatchRows])
	plan.MaxMinutes, _ = strconv.Atoi(o[OptionArchiveMaxMinutes])
	if plan.Action != ArchiveActionArchive && plan.Action != ArchiveActionDelete {
		return ArchivePlan{}, errors.New("the action is neither archive nor delete")
	}
	plan.Target = archiveTargetRef(database, schema, table, plan.ArchiveName, plan.ArchiveTo)
	return plan, nil
}

// ArchiveReadBack says what was read back after the archive was created: the job (exists, enabled,
// scheduled), the archive table, and the rows the first batch moved when it ran (moved nil and
// firstErr "" when it was not asked for). Verified when the job and the archive table are there.
func ArchiveReadBack(plan ArchivePlan, states []BackupJobState, target ArchiveTarget, moved *int64, firstErr string) (string, bool) {
	text, verified := KakapoJobsReadBack([]string{plan.JobName()}, states)
	text = strings.TrimSuffix(text, ".")
	if plan.Archives() {
		switch {
		case target.Error != "":
			text += "; the archive table could not be read: " + target.Error
			verified = false
		case target.Exists:
			text += "; " + plan.TargetWords() + " exists"
		default:
			text += "; " + plan.TargetWords() + " was not found"
			verified = false
		}
	}
	switch {
	case moved != nil:
		text += "; the first batch moved " + n0int(*moved) + " rows"
	case firstErr != "":
		text += "; the first batch did not run: " + firstErr
	}
	return text + ".", verified
}

// BuildRemoveArchiveFix builds the preview of "Stop archiving a table" from kakapo's archive jobs on
// the database: the job as it is, its deletion, and the rollback that creates it again, rebuilt
// from the options line its step carries. ErrNoArchiveJob when kakapo does not archive the table.
func BuildRemoveArchiveFix(jobs []ArchiveJob, db, schema, table, server, engineVersion string) (FixPreview, Fix, error) {
	j, ok := findArchiveJob(jobs, db, schema, table)
	if !ok {
		return FixPreview{}, Fix{}, ErrNoArchiveJob
	}
	d, _ := FindFixDef(FixRemoveArchive)
	t := tableLabel(schema, table)
	opt := archiveTableOption(schema, table)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting, Current: j.Name, Target: "deleted",
		Options: map[string]string{OptionTable: opt}}
	p := FixPreview{Database: db, EngineVersion: engineVersion, Changes: []Drift{}, Warnings: []string{},
		Options: map[string]string{OptionTable: opt}}

	what := j.Name
	if j.Clock != "" {
		what += ", daily " + j.Clock
	}
	if j.OptionsOK {
		what += fmt.Sprintf(", rows older than %d months", j.Options.KeepMonths)
		if j.Options.Condition != "" {
			what += " where " + j.Options.Condition
		}
		if j.Options.Action == ArchiveActionArchive {
			what += " to " + j.destinationWords()
		} else {
			what += ", deleted"
		}
	}
	if !j.Enabled {
		what += " (disabled)"
	}
	p.Changes = append(p.Changes, Drift{Setting: "Job", Current: what, Target: "deleted"},
		Drift{Setting: "Last run", Current: j.LastRunWords(), Target: "no more runs"})
	archived := j.OptionsOK && j.Options.Action == ArchiveActionArchive
	if archived {
		rows := "rows not counted"
		if j.RowsArchived != nil {
			rows = n0int(*j.RowsArchived) + " rows"
		}
		p.Changes = append(p.Changes, Drift{Setting: "Archive table", Current: j.destinationWords() + ", " + rows, Target: "kept"})
		n := "its"
		if j.RowsArchived != nil {
			n = "its " + n0int(*j.RowsArchived)
		}
		p.Warnings = append(p.Warnings, fmt.Sprintf("The archive table %s and %s rows stay; drop it by hand when nobody needs them.",
			tableLabel(j.Options.TargetSchema, j.Options.TargetTable), n))
	}
	if j.OptionsOK {
		plan := planFromOptions(j.Options)
		p.Rollback = plan.batchWith(plan.Job(j.Command), false)
	} else {
		p.Warnings = append(p.Warnings, "The job's options line could not be read, so the rollback cannot create it again: script the job in SSMS first if it may be wanted back.")
	}
	p.Warnings = append(p.Warnings, "Rows already moved stay where they are; "+t+" simply stops losing old rows.", RemoveFixPermission(server))
	p.Statement = KakapoJobsDelete([]string{j.Name})
	return p, fix, nil
}

// RemoveArchiveDescription is the change log line of the remove fix.
func RemoveArchiveDescription(jobName, schema, table string) string {
	return fmt.Sprintf("Stopped archiving %s: deleted the job %s; rows already moved stay where they are", tableLabel(schema, table), jobName)
}

// RemoveArchiveReadBack says whether the job is gone after the remove fix ran.
func RemoveArchiveReadBack(jobName string, states []BackupJobState) (string, bool) {
	for _, s := range states {
		if s.Name == jobName {
			return "the job " + jobName + " is still there", false
		}
	}
	return "the job " + jobName + " is gone", true
}
