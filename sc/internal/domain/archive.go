package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Archiving a table that only grows (docs/phase5a-contract.md, docs/archiving.md): the facts the
// archive fixes read, the options they take, the Agent job that moves the old rows in small batches
// every night, and the options line the job step carries so the Storage card's Tables row and the
// remove fix read the choices back. The fixes themselves are in archive_fix.go, the condition's
// check in condition.go.
const (
	FixArchiveTable  = "archive-table"
	FixRemoveArchive = "remove-archive"
)

// The options of the archive fix besides OptionTable ("table": the table, [schema].[table]).
const (
	OptionArchiveDateColumn = "dateColumn"
	OptionArchiveKeepMonths = "keepMonths"
	OptionArchiveAction     = "action"
	OptionArchiveName       = "archiveName"
	OptionArchiveTo         = "archiveTo"
	OptionArchiveCondition  = "condition"
	OptionArchiveSchedule   = "schedule"
	OptionArchiveBatchRows  = "batchRows"
	OptionArchiveMaxMinutes = "maxMinutes"
	OptionArchiveTidyUp     = "tidyUp"
	OptionArchiveFirstRun   = "firstRun"
)

// The values of the select options.
const (
	ArchiveActionArchive = "archive"
	ArchiveActionDelete  = "delete"

	ArchiveNameSchema = "schema" // archive.MailLog
	ArchiveNameSuffix = "suffix" // MailLog_Archive
	ArchiveNamePrefix = "prefix" // Archive_MailLog

	ArchiveToSame = "same database"

	ArchiveFirstRunNothing  = "nothing"
	ArchiveFirstRunOneBatch = "move one batch now"
)

// Defaults and limits of the archive fix.
const (
	ArchiveSchema            = "archive"
	archiveDefaultKeepMonths = 12
	archiveMaxKeepMonths     = 120
	archiveDefaultSchedule   = "02:30"
	archiveDefaultBatchRows  = 5000
	archiveMinBatchRows      = 500
	archiveMaxBatchRows      = 50000
	archiveDefaultMaxMinutes = 30
	archiveMinMaxMinutes     = 5
	archiveMaxMaxMinutes     = 240
	archivedAtColumn         = "ArchivedAt"
	archiveIndexName         = "CX_kakapo_archive"
	archiveJobPrefix         = "kakapo: archive "
	archiveOptionsMarker     = "/* kakapo-archive "
	// archiveIndexedBatchSeconds and archiveScanBatchSeconds are how long the nights estimate takes
	// one batch to be, with its one-second pause: a seek on an indexed date column, or a scan.
	archiveIndexedBatchSeconds = 2
	archiveScanBatchSeconds    = 10
)

// ArchiveJobName is the Agent job kakapo creates for a table: "kakapo: archive Shop.dbo.MailLog".
func ArchiveJobName(db, schema, table string) string {
	return archiveJobPrefix + db + "." + schema + "." + table
}

// ArchiveDatabaseName is the archive database the fix offers: "{db}_Archive" (a later fix creates it).
func ArchiveDatabaseName(db string) string { return db + "_Archive" }

// IsArchiveJobName says whether a job name is one of kakapo's archive jobs for a database.
func IsArchiveJobName(name, db string) bool { return strings.HasPrefix(name, archiveJobPrefix+db+".") }

// ArchiveColumn is one column of the table to archive, as sys.columns describes it.
type ArchiveColumn struct {
	Name     string
	Type     string // the system type name: datetime2, int, timestamp
	Identity bool
	Computed bool
	Nullable bool
	// DateIndexed: a date column that is the first key column of an enabled, unfiltered index, so
	// that "older than" is a seek.
	DateIndexed bool
	// ClusteredKey: an ascending integer identity that leads the clustered index, so the table is
	// stored oldest row first and a batch can read forward from where the last one stopped.
	ClusteredKey bool
}

// IsDate says whether the column can say how old a row is: datetime, datetime2 or date.
func (c ArchiveColumn) IsDate() bool {
	switch strings.ToLower(c.Type) {
	case "datetime", "datetime2", "date":
		return true
	}
	return false
}

// RowVersion says whether the column is a rowversion (timestamp), which SQL Server stamps itself.
func (c ArchiveColumn) RowVersion() bool {
	t := strings.ToLower(c.Type)
	return t == "timestamp" || t == "rowversion"
}

// Copied says whether the archive copy carries the column: computed and rowversion columns cannot
// be inserted, so they are left out.
func (c ArchiveColumn) Copied() bool { return !c.Computed && !c.RowVersion() }

// ArchiveForeignKey is a foreign key that points at the table, from another table (or itself).
type ArchiveForeignKey struct {
	Name      string
	FromTable string // schema.table
}

// ArchiveTrigger is a DML trigger on the table.
type ArchiveTrigger struct {
	Name      string
	Events    []string // INSERT, UPDATE, DELETE
	Enabled   bool
	InsteadOf bool
}

// firesOnDelete says whether the trigger fires for a delete.
func (t ArchiveTrigger) firesOnDelete() bool {
	for _, e := range t.Events {
		if strings.EqualFold(e, "DELETE") {
			return true
		}
	}
	return false
}

// ArchiveTarget is the archive table the options name and what was read of it.
type ArchiveTarget struct {
	Database string
	Schema   string
	Table    string
	Read     bool // its database exists and the table was looked for
	Exists   bool
	Columns  []ArchiveColumn
	Rows     int64
	Error    string
}

// ArchiveEstimate is how many rows are older than the cutoff (and match the condition) now.
type ArchiveEstimate struct {
	Known     bool
	Rows      int64
	Estimated bool       // from a 1 percent sample (the date column is not indexed)
	Oldest    *time.Time // the lowest date, read only when the column is indexed
	TimedOut  bool       // counting took longer than its 10 seconds
	Error     string     // why it is unknown
}

// ArchiveJob is one of kakapo's archive jobs as read from msdb, with its options read back from the
// step's options line and the rows on each side now.
type ArchiveJob struct {
	Name            string
	Enabled         bool
	ScheduleEnabled bool
	Clock           string // the schedule's time, HH:MM; "" without a schedule
	Command         string // step 1, as stored
	Options         ArchiveOptions
	OptionsOK       bool // the options line was found and read
	LastRunAt       *time.Time
	LastOutcome     string // succeeded, failed, retry, cancelled, running; "" before the first run
	LastSeconds     int
	LastRowsMoved   *int64 // from "rows moved: n" in the step's history message
	RowsKept        *int64 // the table's rows now
	RowsArchived    *int64 // the archive table's rows now (archive action)
	CountError      string
}

// ArchiveRunOutcome is sysjobhistory.run_status in words.
func ArchiveRunOutcome(status int) string {
	switch status {
	case 0:
		return "failed"
	case 1:
		return "succeeded"
	case 2:
		return "retry"
	case 3:
		return "cancelled"
	case 4:
		return "running"
	}
	return "unknown"
}

// AgentDuration is a sysjobhistory run_duration (HHMMSS as a number) in seconds.
func AgentDuration(hhmmss int) int { return hhmmss/10000*3600 + hhmmss/100%100*60 + hhmmss%100 }

// ArchiveFacts is what the archive fix reads about one table (collect.ArchiveFacts,
// collect.ArchiveTarget, collect.ArchiveEstimate).
type ArchiveFacts struct {
	Database       string
	Schema         string
	Table          string
	ServerName     string
	ProductVersion string
	EngineEdition  int // 4 Express (no Agent), 3 Enterprise or Developer
	ServerNow      time.Time

	Found bool // a user table of this name exists
	Rows  int64
	MB    float64

	MemoryOptimized bool
	Temporal        string // "" or the temporal_type_desc: SYSTEM_VERSIONED_TEMPORAL_TABLE, HISTORY_TABLE
	CDC             bool
	ChangeTracking  bool
	Published       bool
	MergePublished  bool
	Replicated      bool

	Columns  []ArchiveColumn
	KeysIn   []ArchiveForeignKey
	KeysOut  int
	Triggers []ArchiveTrigger

	TableError    string
	ColumnsError  string
	KeysError     string
	TriggersError string
	FeaturesError string

	ArchiveDBExists bool
	RecoveryModel   string
	LastLogBackup   *time.Time
	LogBackupError  string
	AgentStatus     string // "Running", "Stopped"
	AgentError      string
	FreeMB          *int64 // free MB on the tightest drive of the database's data files
	ArchiveFreeMB   *int64 // the same for the archive database
	RoomError       string

	Jobs      []ArchiveJob
	JobsError string

	Target   ArchiveTarget
	Estimate ArchiveEstimate
}

// DateColumns are the table's date columns, the indexed ones first, each group in column order.
func (f ArchiveFacts) DateColumns() []ArchiveColumn {
	out := []ArchiveColumn{}
	for _, c := range f.Columns {
		if c.IsDate() {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DateIndexed && !out[j].DateIndexed })
	return out
}

func (f ArchiveFacts) column(name string) (ArchiveColumn, bool) {
	for _, c := range f.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return ArchiveColumn{}, false
}

// ColumnNames are the names of every column, for the condition's check.
func (f ArchiveFacts) ColumnNames() []string {
	out := make([]string, 0, len(f.Columns))
	for _, c := range f.Columns {
		out = append(out, c.Name)
	}
	return out
}

// JobFor is kakapo's archive job for this table, if any.
func (f ArchiveFacts) JobFor() (ArchiveJob, bool) {
	return findArchiveJob(f.Jobs, f.Database, f.Schema, f.Table)
}

// findArchiveJob finds the job of one table: by its options line, else by its name.
func findArchiveJob(jobs []ArchiveJob, db, schema, table string) (ArchiveJob, bool) {
	name := ArchiveJobName(db, schema, table)
	for _, j := range jobs {
		if j.OptionsOK && strings.EqualFold(j.Options.Schema, schema) && strings.EqualFold(j.Options.Table, table) {
			return j, true
		}
	}
	for _, j := range jobs {
		if strings.EqualFold(j.Name, name) {
			return j, true
		}
	}
	return ArchiveJob{}, false
}

// tableLabel is a table as the texts name it: dbo.MailLog.
func tableLabel(schema, table string) string { return schema + "." + table }

// ArchiveTargetFor is the archive table the options name (the service reads it before the preview):
// archiveName schema (archive.T, the default), suffix (s.T_Archive) or prefix (s.Archive_T), in the
// same database or in {db}_Archive (the default when that database exists).
func ArchiveTargetFor(f ArchiveFacts, options map[string]string) ArchiveTarget {
	name := strings.TrimSpace(options[OptionArchiveName])
	if !validArchiveName(name) {
		name = ArchiveNameSchema
	}
	to, ok := options[OptionArchiveTo]
	to = strings.TrimSpace(to)
	if !ok || !validArchiveTo(to, f.Database) {
		to = defaultArchiveTo(f)
	}
	return archiveTargetRef(f.Database, f.Schema, f.Table, name, to)
}

func archiveTargetRef(db, schema, table, name, to string) ArchiveTarget {
	t := ArchiveTarget{Database: db, Schema: schema, Table: table}
	if !strings.EqualFold(to, ArchiveToSame) && to != "" {
		t.Database = ArchiveDatabaseName(db)
	}
	switch name {
	case ArchiveNameSuffix:
		t.Table = table + "_Archive"
	case ArchiveNamePrefix:
		t.Table = "Archive_" + table
	default:
		t.Schema = ArchiveSchema
	}
	return t
}

func validArchiveName(v string) bool {
	return v == ArchiveNameSchema || v == ArchiveNameSuffix || v == ArchiveNamePrefix
}

func validArchiveTo(v, db string) bool { return v == ArchiveToSame || v == ArchiveDatabaseName(db) }

func defaultArchiveTo(f ArchiveFacts) string {
	if f.ArchiveDBExists {
		return ArchiveDatabaseName(f.Database)
	}
	return ArchiveToSame
}

// ArchiveOptions are the choices of one archive, as the job step's options line carries them
// (/* kakapo-archive {json} */), so the Tables row and the remove fix read them back.
type ArchiveOptions struct {
	Database       string `json:"database"`
	Schema         string `json:"schema"`
	Table          string `json:"table"`
	DateColumn     string `json:"dateColumn"`
	KeepMonths     int    `json:"keepMonths"`
	Action         string `json:"action"`
	ArchiveName    string `json:"archiveName,omitempty"`
	ArchiveTo      string `json:"archiveTo,omitempty"`
	TargetDatabase string `json:"targetDatabase,omitempty"`
	TargetSchema   string `json:"targetSchema,omitempty"`
	TargetTable    string `json:"targetTable,omitempty"`
	Condition      string `json:"condition,omitempty"`
	Schedule       string `json:"schedule"`
	BatchRows      int    `json:"batchRows"`
	MaxMinutes     int    `json:"maxMinutes"`
	TidyUp         bool   `json:"tidyUp"`
	Created        string `json:"created"` // the server's date when the job was created, yyyy-mm-dd
}

// Line is the options line of the job step. A * inside a value (a name with */ or /* in it) is
// written as the JSON escape jsonStar, so the comment can neither end nor nest early; JSON itself has
// no * outside its strings.
func (o ArchiveOptions) Line() string {
	b, _ := json.Marshal(o)
	return archiveOptionsMarker + strings.ReplaceAll(string(b), "*", jsonStar) + " */"
}

// ParseArchiveOptionsLine reads the options back from a job step's command; false when the line is
// missing or does not describe an archive kakapo can rebuild.
func ParseArchiveOptionsLine(command string) (ArchiveOptions, bool) {
	var o ArchiveOptions
	i := strings.Index(command, archiveOptionsMarker)
	if i < 0 {
		return o, false
	}
	rest := command[i+len(archiveOptionsMarker):]
	end := strings.Index(rest, " */")
	if end < 0 {
		return o, false
	}
	if err := json.Unmarshal([]byte(rest[:end]), &o); err != nil {
		return ArchiveOptions{}, false
	}
	_, clockOK := parseClock(o.Schedule)
	ok := o.Database != "" && o.Schema != "" && o.Table != "" && o.DateColumn != "" && clockOK &&
		o.KeepMonths >= 1 && o.KeepMonths <= archiveMaxKeepMonths &&
		(o.Action == ArchiveActionArchive || o.Action == ArchiveActionDelete)
	if !ok {
		return ArchiveOptions{}, false
	}
	return o, true
}

// ArchivePlan is one archive as the fix builds it: the options, resolved and checked, and what the
// table read says about the columns.
type ArchivePlan struct {
	Database    string
	Schema      string
	Table       string
	DateColumn  string
	DateIndexed bool
	// KeyColumn and KeyType: when DateColumn is not indexed, the ascending integer identity that leads
	// the clustered index (ArchiveColumn.ClusteredKey). Each batch then takes the next due rows after
	// the last batch's highest key, a range seek, instead of scanning the table. "" when there is none.
	KeyColumn   string
	KeyType     string
	KeepMonths  int
	Action      string
	ArchiveName string
	ArchiveTo   string
	Target      ArchiveTarget
	Condition   string // canonical (CanonicalCondition); "" none
	Schedule    string // HH:MM
	BatchRows   int
	MaxMinutes  int
	TidyUp      bool
	FirstRun    bool
	Created     string // yyyy-mm-dd
	// Columns are the columns the archive copy carries (not computed, not rowversion), in column
	// order; Identity says one of them is an identity column (IDENTITY_INSERT around the copy).
	Columns      []string
	Identity     bool
	CreateTarget bool // the archive table does not exist yet
	HasArchived  bool // the source already has a column named ArchivedAt
}

// Archives says whether the plan keeps a copy (archive) rather than only deleting.
func (p ArchivePlan) Archives() bool { return p.Action == ArchiveActionArchive }

// JobName is the plan's Agent job.
func (p ArchivePlan) JobName() string { return ArchiveJobName(p.Database, p.Schema, p.Table) }

func (p ArchivePlan) source() string {
	return QuoteName(p.Database) + "." + QuoteName(p.Schema) + "." + QuoteName(p.Table)
}

func (p ArchivePlan) target() string {
	return QuoteName(p.Target.Database) + "." + QuoteName(p.Target.Schema) + "." + QuoteName(p.Target.Table)
}

// TargetWords is the archive table as the texts name it: "archive.MailLog in Shop".
func (p ArchivePlan) TargetWords() string {
	return tableLabel(p.Target.Schema, p.Target.Table) + " in " + p.Target.Database
}

// Options is the plan as the job step's options line carries it.
func (p ArchivePlan) Options() ArchiveOptions {
	o := ArchiveOptions{Database: p.Database, Schema: p.Schema, Table: p.Table, DateColumn: p.DateColumn,
		KeepMonths: p.KeepMonths, Action: p.Action, Condition: p.Condition, Schedule: p.Schedule,
		BatchRows: p.BatchRows, MaxMinutes: p.MaxMinutes, TidyUp: p.TidyUp, Created: p.Created}
	if p.Archives() {
		o.ArchiveName, o.ArchiveTo = p.ArchiveName, p.ArchiveTo
		o.TargetDatabase, o.TargetSchema, o.TargetTable = p.Target.Database, p.Target.Schema, p.Target.Table
	}
	return o
}

// planFromOptions is the plan an options line describes, without the columns (enough to name the
// job, describe it and rebuild its schedule and tidy-up step).
func planFromOptions(o ArchiveOptions) ArchivePlan {
	p := ArchivePlan{Database: o.Database, Schema: o.Schema, Table: o.Table, DateColumn: o.DateColumn,
		KeepMonths: o.KeepMonths, Action: o.Action, ArchiveName: o.ArchiveName, ArchiveTo: o.ArchiveTo,
		Condition: o.Condition, Schedule: o.Schedule, BatchRows: o.BatchRows, MaxMinutes: o.MaxMinutes,
		TidyUp: o.TidyUp, Created: o.Created}
	p.Target = ArchiveTarget{Database: o.TargetDatabase, Schema: o.TargetSchema, Table: o.TargetTable}
	return p
}

// batchBody is one batch: up to BatchRows rows older than the cutoff (and matching the condition)
// deleted in one statement and, for an archive, copied into the archive table by the same statement
// (DELETE ... OUTPUT INTO), so a failure can neither lose nor double a row. The rows are taken with
// READPAST, UPDLOCK, ROWLOCK so the batch skips rows users hold and yields to them.
func (p ArchivePlan) batchBody(indent string) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(indent + s + "\n") }
	line("BEGIN TRANSACTION;")
	identity := p.Archives() && p.Identity
	if identity {
		line("SET IDENTITY_INSERT " + p.target() + " ON;")
	}
	where := QuoteName(p.DateColumn) + " < @cutoff"
	if p.Condition != "" {
		where += " AND (" + p.Condition + ")"
	}
	if p.KeyColumn != "" {
		// The next BatchRows due rows after the last batch's highest key, read forward on the
		// clustered index; the batch then takes exactly that key range.
		key := QuoteName(p.KeyColumn)
		line(fmt.Sprintf("SELECT @to = MAX(k) FROM (SELECT TOP (%d) %s AS k FROM %s WITH (READPAST) WHERE %s > @from AND %s ORDER BY %s) AS x;",
			p.BatchRows, key, p.source(), key, where, key))
		where = key + " > @from AND " + key + " <= @to AND " + where
		line(fmt.Sprintf("WITH batch AS (SELECT * FROM %s WITH (READPAST, UPDLOCK, ROWLOCK) WHERE %s)", p.source(), where))
	} else {
		line(fmt.Sprintf("WITH batch AS (SELECT TOP (%d) * FROM %s WITH (READPAST, UPDLOCK, ROWLOCK) WHERE %s)", p.BatchRows, p.source(), where))
	}
	if p.Archives() {
		deleted, cols := make([]string, len(p.Columns)), make([]string, len(p.Columns))
		for i, c := range p.Columns {
			deleted[i] = "deleted." + QuoteName(c)
			cols[i] = QuoteName(c)
		}
		line("DELETE FROM batch OUTPUT " + strings.Join(deleted, ", ") + " INTO " + p.target() + " (" + strings.Join(cols, ", ") + ");")
	} else {
		line("DELETE FROM batch;")
	}
	line("SET @n = @@ROWCOUNT;")
	if p.KeyColumn != "" {
		// No due rows left after @from: done, even when the last range's rows were all held by users.
		line("IF @to IS NULL SET @n = 0; ELSE SET @from = @to;")
	}
	if identity {
		line("SET IDENTITY_INSERT " + p.target() + " OFF;")
	}
	line("COMMIT;")
	return b.String()
}

// StepCommand is the job step: tagged, the options line, then the loop that moves batches until
// none is left or the night's time is up, and the rows moved printed for the job history.
func (p ArchivePlan) StepCommand() string {
	var b strings.Builder
	b.WriteString(JobStepTag + "\n")
	b.WriteString(p.Options().Line() + "\n")
	b.WriteString("SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;\n")
	fmt.Fprintf(&b, "DECLARE @cutoff datetime2 = DATEADD(MONTH, -%d, SYSDATETIME()), @stop datetime2 = DATEADD(MINUTE, %d, SYSDATETIME()), @moved bigint = 0, @n int;\n",
		p.KeepMonths, p.MaxMinutes)
	b.WriteString(p.keyDeclare())
	b.WriteString("WHILE SYSDATETIME() < @stop\nBEGIN\n")
	b.WriteString(p.batchBody("    "))
	b.WriteString("    SET @moved += @n;\n")
	b.WriteString("    IF @n = 0 BREAK;\n")
	b.WriteString("    WAITFOR DELAY '00:00:01';\n")
	b.WriteString("END;\n")
	b.WriteString("PRINT 'rows moved: ' + CONVERT(varchar(20), @moved);")
	return b.String()
}

// keyDeclare starts the key range below the table's lowest key (a seek on the clustered index), when
// the batches read forward by KeyColumn.
func (p ArchivePlan) keyDeclare() string {
	if p.KeyColumn == "" {
		return ""
	}
	key := QuoteName(p.KeyColumn)
	return fmt.Sprintf("DECLARE @from %s, @to %s;\nSELECT @from = MIN(%s) - 1 FROM %s;\n", p.KeyType, p.KeyType, key, p.source())
}

// TidyCommand is the optional second step: the table's indexes reorganised (online, in every
// edition) and its statistics updated after a night that moved rows.
func (p ArchivePlan) TidyCommand() string {
	return JobStepTag + "\n" +
		"ALTER INDEX ALL ON " + p.source() + " REORGANIZE;\n" +
		"UPDATE STATISTICS " + p.source() + ";"
}

// FirstBatch is one batch run once, after the job is created and read back, so the first night is
// not the first time it happens. It prints the rows it moved.
func (p ArchivePlan) FirstBatch() string {
	var b strings.Builder
	b.WriteString("SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;\n")
	fmt.Fprintf(&b, "DECLARE @cutoff datetime2 = DATEADD(MONTH, -%d, SYSDATETIME()), @n int;\n", p.KeepMonths)
	b.WriteString(p.keyDeclare())
	b.WriteString(p.batchBody(""))
	b.WriteString("PRINT 'rows moved: ' + CONVERT(varchar(20), @n);")
	return b.String()
}

// scheduleWords is "daily 02:30".
func (p ArchivePlan) scheduleWords() string { return "daily " + p.Schedule }

// jobDescription is what the job's description says after "Created by kakapo on ... for db: ".
func (p ArchivePlan) jobDescription() string {
	what := fmt.Sprintf("rows of %s older than %d months", tableLabel(p.Schema, p.Table), p.KeepMonths)
	if p.Condition != "" {
		what += " where " + p.Condition
	}
	if p.Archives() {
		what += " moved to " + p.TargetWords()
	} else {
		what += " deleted, not kept"
	}
	return what + ", " + p.scheduleWords()
}

// Job is the Agent job of the plan, its first step the command given (the plan's StepCommand when
// created, the command as stored when the remove fix rebuilds it).
func (p ArchivePlan) Job(stepCommand string) KakapoJob {
	stepName := "Archive old rows"
	if !p.Archives() {
		stepName = "Delete old rows"
	}
	steps := []KakapoJobStep{{Name: stepName, Command: stepCommand}}
	if p.TidyUp {
		steps = append(steps, KakapoJobStep{Name: "Tidy up indexes and statistics", Command: p.TidyCommand()})
	}
	return KakapoJob{
		Name:        p.JobName(),
		Description: KakapoJobDescription(p.Created, p.Database, p.jobDescription()),
		Steps:       steps,
		Schedule:    KakapoJobSchedule{Name: p.JobName() + ", " + p.scheduleWords(), Freq: JobDaily, Clock: p.Schedule},
	}
}

// createTarget is the statements that make the archive table: the archive schema when it is
// missing, the source's copied columns (SELECT TOP (0) ... INTO keeps their types and the identity),
// the ArchivedAt column, and a clustered index on the date column, PAGE compressed where the edition
// allows.
func (p ArchivePlan) createTarget(pageCompression bool) string {
	var b strings.Builder
	db := QuoteName(p.Target.Database)
	if p.Target.Schema == ArchiveSchema {
		b.WriteString("    IF NOT EXISTS (SELECT 1 FROM " + db + ".sys.schemas WHERE name = " + nstr(ArchiveSchema) + ")\n")
		b.WriteString("        EXEC " + db + ".sys.sp_executesql " + nstr("CREATE SCHEMA "+QuoteName(ArchiveSchema)+" AUTHORIZATION [dbo];") + ";\n")
	}
	cols := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = QuoteName(c)
	}
	b.WriteString("    SELECT TOP (0) " + strings.Join(cols, ", ") + " INTO " + p.target() + " FROM " + p.source() + ";\n")
	if !p.HasArchived {
		b.WriteString("    ALTER TABLE " + p.target() + " ADD " + QuoteName(archivedAtColumn) + " datetime2 NOT NULL DEFAULT SYSDATETIME();\n")
	}
	with := ""
	if pageCompression {
		with = " WITH (DATA_COMPRESSION = PAGE)"
	}
	b.WriteString("    CREATE CLUSTERED INDEX " + QuoteName(archiveIndexName) + " ON " + p.target() + " (" + QuoteName(p.DateColumn) + ")" + with + ";\n")
	return b.String()
}

// Batch is the statement that creates the archive: one batch in one transaction inside TRY ...
// CATCH, refusing when the job (or, when it is to be created, the archive table) already exists;
// the archive table when it is missing, then the job, owned by the sa principal whatever its name.
func (p ArchivePlan) Batch(pageCompression bool) string {
	return p.batchWith(p.Job(p.StepCommand()), pageCompression)
}

func (p ArchivePlan) batchWith(job KakapoJob, pageCompression bool) string {
	var b strings.Builder
	b.WriteString("DECLARE @owner sysname = SUSER_SNAME(0x01);\n")
	b.WriteString("BEGIN TRY\n")
	b.WriteString("    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = " + nstr(job.Name) + ")\n")
	b.WriteString("        THROW 50000, " + nstr("A job named "+job.Name+" already exists") + ", 1;\n")
	create := p.Archives() && p.CreateTarget
	if create {
		b.WriteString("    IF OBJECT_ID(" + nstr(p.target()) + ", N'U') IS NOT NULL\n")
		b.WriteString("        THROW 50000, " + nstr("The table "+p.TargetWords()+" already exists") + ", 1;\n")
	}
	b.WriteString("    BEGIN TRANSACTION;\n")
	if create {
		b.WriteString(p.createTarget(pageCompression))
	}
	b.WriteString("    IF NOT EXISTS (SELECT 1 FROM msdb.dbo.syscategories WHERE name = N'Database Maintenance' AND category_class = 1)\n")
	b.WriteString("        EXEC msdb.dbo.sp_add_category @class = N'JOB', @type = N'LOCAL', @name = N'Database Maintenance';\n")
	b.WriteString(job.block())
	b.WriteString("    COMMIT TRANSACTION;\n")
	b.WriteString("END TRY\n")
	b.WriteString("BEGIN CATCH\n")
	b.WriteString("    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;\n")
	b.WriteString("    THROW;\n")
	b.WriteString("END CATCH;")
	return b.String()
}

// Rollback deletes the job; the archive table and its rows stay, and the rollback says so.
func (p ArchivePlan) Rollback() string {
	del := KakapoJobsDelete([]string{p.JobName()})
	if !p.Archives() {
		return del
	}
	return "-- The archive table " + p.TargetWords() + " and its rows stay: drop it by hand when nobody needs them.\n" + del
}

// rowsPerNight is how many rows a night moves at most, by the nights estimate: the night's minutes
// over the seconds a batch takes, times the batch size.
func (p ArchivePlan) rowsPerNight() int64 {
	secs := archiveScanBatchSeconds
	if p.DateIndexed || p.KeyColumn != "" {
		secs = archiveIndexedBatchSeconds
	}
	return int64(p.MaxMinutes*60/secs) * int64(p.BatchRows)
}

// nights is how many nights the first catch-up takes for rows due now (at least one).
func (p ArchivePlan) nights(rows int64) int64 {
	per := p.rowsPerNight()
	if per <= 0 || rows <= 0 {
		return 1
	}
	return (rows + per - 1) / per
}

// ChangeDescription is the change log line of the archive fix: the retention, the destination, the
// schedule and the condition.
func (p ArchivePlan) ChangeDescription() string {
	s := fmt.Sprintf("Archiving %s: rows older than %d months", tableLabel(p.Schema, p.Table), p.KeepMonths)
	if p.Condition != "" {
		s += " where " + p.Condition
	}
	if p.Archives() {
		s += " move nightly to " + p.TargetWords()
	} else {
		s += " are deleted nightly, not kept"
	}
	return s + fmt.Sprintf(", by the job %s (%s, %d rows per statement, %d minutes a night)",
		p.JobName(), p.scheduleWords(), p.BatchRows, p.MaxMinutes)
}

// ArchiveTableObject is a table as the change log's object names it: dbo.MailLog.
func ArchiveTableObject(schema, table string) string { return tableLabel(schema, table) }

// ArchivedWords is what IDX10 and the Tables row say about a table kakapo archives: "archived
// nightly, 12 months kept, last run 2026-11-04 02:30".
func (j ArchiveJob) ArchivedWords() string {
	verb := "archived nightly"
	if j.OptionsOK && j.Options.Action == ArchiveActionDelete {
		verb = "old rows deleted nightly"
	}
	s := verb
	if j.OptionsOK {
		s += fmt.Sprintf(", %d months kept", j.Options.KeepMonths)
	}
	return s + ", last run " + j.LastRunWords()
}

// LastRunWords is the last run: "2026-11-04 02:30 (succeeded, 4,000 rows moved)" or "not yet".
func (j ArchiveJob) LastRunWords() string {
	if j.LastRunAt == nil {
		return "not yet"
	}
	parts := []string{}
	if j.LastOutcome != "" {
		parts = append(parts, j.LastOutcome)
	}
	if j.LastRowsMoved != nil {
		parts = append(parts, n0int(*j.LastRowsMoved)+" rows moved")
	}
	s := sqlTime(*j.LastRunAt)
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

// destinationWords is where the job puts the rows: "archive.MailLog in Shop" or "deleted, not kept".
func (j ArchiveJob) destinationWords() string {
	if !j.OptionsOK {
		return "unknown"
	}
	if j.Options.Action == ArchiveActionDelete {
		return "deleted, not kept"
	}
	return tableLabel(j.Options.TargetSchema, j.Options.TargetTable) + " in " + j.Options.TargetDatabase
}

// TableListing is one user table for the archive picker (collect.ListTables).
type TableListing struct {
	Schema      string            `json:"schema"`
	Name        string            `json:"name"`
	Rows        int64             `json:"rows"`
	MB          float64           `json:"mb"`
	DateColumns []TableDateColumn `json:"dateColumns"`
	Archived    bool              `json:"archived"` // kakapo archives it (its job exists)
}

// TableDateColumn is a date column of a listed table, and whether it leads an index.
type TableDateColumn struct {
	Name    string `json:"name"`
	Indexed bool   `json:"indexed"`
}

// TableList is the picker's list: every user table, biggest first. JobsError says the archive jobs
// could not be read (Archived is then false everywhere).
type TableList struct {
	Database  string         `json:"database"`
	Tables    []TableListing `json:"tables"`
	JobsError string         `json:"jobsError,omitempty"`
}

// SortTableList puts the biggest tables first, then by schema and name.
func SortTableList(ts []TableListing) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].MB != ts[j].MB {
			return ts[i].MB > ts[j].MB
		}
		if ts[i].Schema != ts[j].Schema {
			return ts[i].Schema < ts[j].Schema
		}
		return ts[i].Name < ts[j].Name
	})
}

// archiveTableOption is the table option as the fixes and the findings name it: [dbo].[MailLog].
func archiveTableOption(schema, table string) string {
	return QuoteName(schema) + "." + QuoteName(table)
}

// ArchiveFixesFor are the Storage card's archive entries: archive-table for each IDX10 finding that
// carries it (options table and, when that column is indexed, the date column IDX10 read), and
// remove-archive for each table kakapo archives (options table, and the Tables row's values: rows
// kept, rows archived, last run, months kept, destination, schedule).
func ArchiveFixesFor(f StorageFacts, findings []Finding) []Fix {
	out := []Fix{}
	add, _ := FindFixDef(FixArchiveTable)
	for _, x := range findings {
		if x.FixID != FixArchiveTable {
			continue
		}
		fix := Fix{ID: add.ID, Title: add.Title, Scope: add.Scope, CheckID: x.CheckID, Setting: add.Setting,
			Current: "no archive job", Target: "old rows archived nightly", Options: map[string]string{OptionTable: x.Object}}
		for _, t := range f.GrowingTables {
			if archiveTableOption(t.Schema, t.Name) == x.Object && t.DateCol != "" && t.DateIndexed {
				fix.Options[OptionArchiveDateColumn] = t.DateCol
			}
		}
		out = append(out, fix)
	}
	remove, _ := FindFixDef(FixRemoveArchive)
	for _, j := range f.Archives {
		if !j.OptionsOK {
			continue
		}
		o := j.Options
		opts := map[string]string{
			OptionTable:   archiveTableOption(o.Schema, o.Table),
			"keepMonths":  strconv.Itoa(o.KeepMonths),
			"destination": j.destinationWords(),
			"schedule":    "daily " + o.Schedule,
			"lastRun":     j.LastRunWords(),
		}
		if j.RowsKept != nil {
			opts["rowsKept"] = strconv.FormatInt(*j.RowsKept, 10)
		}
		if j.RowsArchived != nil {
			opts["rowsArchived"] = strconv.FormatInt(*j.RowsArchived, 10)
		}
		if !j.Enabled {
			opts["disabled"] = "true"
		}
		out = append(out, Fix{ID: remove.ID, Title: remove.Title, Scope: remove.Scope, CheckID: remove.CheckID, Setting: remove.Setting,
			Current: fmt.Sprintf("daily %s, %d months kept", o.Schedule, o.KeepMonths), Target: "job deleted", Options: opts})
	}
	return out
}

// jsonStar is * as a JSON string escape: a backslash (byte 92), then u002a.
var jsonStar = string([]byte{92, 'u', '0', '0', '2', 'a'})
