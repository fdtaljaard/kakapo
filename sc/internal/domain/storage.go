package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StorageFile is one file of the chosen database or of tempdb, as the Storage card reads it
// (docs/phase3b-contract.md).
type StorageFile struct {
	DatabaseID      int
	Database        string
	FileID          int
	Type            string // ROWS or LOG
	LogicalName     string
	PhysicalName    string
	SizeMB          int64 // the current size; tempdb's from tempdb.sys.database_files
	Growth          int64 // in 8 KB pages, or a percentage when IsPercentGrowth
	IsPercentGrowth bool
	MaxSizePages    int64  // -1 unlimited; 268435456 is the 2 TB "unlimited" of a log file
	UsedMB          *int64 // FILEPROPERTY(name, 'SpaceUsed') in MB, this database only; nil when not read
	// StartupSizeMB is a tempdb file's size in sys.master_files: what a restart recreates it at
	// (docs/phase4e-contract.md); 0 when not read, and for the database's own files.
	StartupSizeMB int64

	Volume        string // sys.dm_os_volume_stats volume_mount_point; "" when not read
	VolumeTotalMB int64
	VolumeFreeMB  int64

	// I/O since the last restart (sys.dm_io_virtual_file_stats), this database only.
	HasIO        bool
	Reads        int64
	Writes       int64
	ReadStallMs  int64
	WriteStallMs int64
}

// AvgReadMs is the average stall per read since the restart, whole milliseconds as the scan's
// integer division gives it; 0 without any reads.
func (x StorageFile) AvgReadMs() int64 {
	if x.Reads <= 0 {
		return 0
	}
	return x.ReadStallMs / x.Reads
}

// AvgWriteMs is the average stall per write since the restart; 0 without any writes.
func (x StorageFile) AvgWriteMs() int64 {
	if x.Writes <= 0 {
		return 0
	}
	return x.WriteStallMs / x.Writes
}

func (x StorageFile) isData() bool { return x.Type == "ROWS" }
func (x StorageFile) isLog() bool  { return x.Type == "LOG" }

// TableSize is one table of the database: data (heap or clustered index), the other indexes,
// how many indexes it has and its rows.
type TableSize struct {
	Name       string // [schema].[table]
	DataMB     float64
	IndexMB    float64
	IndexCount int
	Rows       int64
}

// TempdbSession is a session holding tempdb pages now.
type TempdbSession struct {
	SessionID int
	Login     string
	Program   string
	MB        float64
}

// SnapshotTransaction is the oldest transaction keeping row versions in tempdb's version store.
type SnapshotTransaction struct {
	SessionID      int
	ElapsedSeconds int64
}

// SlowGrowth is the slowest autogrowth event of a file of this database: how long it took in
// milliseconds (the trace's Duration is in microseconds), the logical file name and when.
type SlowGrowth struct {
	Ms   int64
	File string
	At   time.Time
}

// GrowingTable is a user table whose name suggests a log, history or queue table (it contains one
// of GrowingTableWords) and that holds a million rows or 500 MB.
type GrowingTable struct {
	Schema  string
	Name    string
	Rows    int64
	MB      float64
	DateCol string // its first datetime, datetime2 or date column; "" when it has none
	// DateIndexed: DateCol leads an index (key_ordinal 1). Then its MIN is read, a seek on the index;
	// on any other column a MIN would be a scan of millions of rows.
	DateIndexed bool
	// KeyCol is the clustered index's first key column when it is an ascending identity: the row with
	// the lowest key is the oldest row, so when DateCol is not indexed, Oldest is that row's DateCol,
	// read with TOP (1) ORDER BY KeyCol (a seek on the clustered index). "" when there is none.
	KeyCol string
	Oldest *time.Time // the lowest value of DateCol (or the oldest row's); nil when not read, not readable or empty
}

// growingMinMonths is how old the oldest row of a table that only grows must be before IDX10
// says nothing removes old rows: a table whose oldest row is younger is being cleaned up.
const growingMinMonths = 12

// GrowingTableWords are the words, any case, in the name of a table that looks like a log, history
// or queue table (a queue that keeps its processed rows grows like a log). The collector's read
// filters on the same words.
var GrowingTableWords = []string{"log", "history", "audit", "trace", "event", "journal", "archive", "queue"}

// growingKind is what a table that only grows looks like from its name: "queue" when only that word
// matched (a queue that keeps its processed rows), else "log or history".
func growingKind(name string) string {
	lower := strings.ToLower(name)
	for _, w := range GrowingTableWords {
		if w != "queue" && strings.Contains(lower, w) {
			return "log or history"
		}
	}
	if strings.Contains(lower, "queue") {
		return "queue"
	}
	return "log or history"
}

// LooksLikeLogTable says whether a table's name contains one of GrowingTableWords, in any case.
func LooksLikeLogTable(name string) bool {
	lower := strings.ToLower(name)
	for _, w := range GrowingTableWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// IdentityColumn is an identity column that has used half its range or more.
type IdentityColumn struct {
	Table     string // [schema].[table]
	Column    string
	Type      string // tinyint, smallint, int, bigint
	LastValue int64
	Seed      int64
	Increment int64
	PctUsed   float64 // decimal(9, 2), as PerformanceTriage.sql computes it
}

// MaxValue is the largest value of the column's type.
func (c IdentityColumn) MaxValue() int64 {
	switch c.Type {
	case "tinyint":
		return 255
	case "smallint":
		return 32767
	case "int":
		return 2147483647
	}
	return math.MaxInt64
}

// StorageFacts is what the Storage card reads about one database and the instance facts its answer
// depends on (docs/phase3b-contract.md, "What the collector reads"). Every area that could not be
// read carries its reason; the checks that need it are skipped, the rest still run.
type StorageFacts struct {
	Database      string
	State         string // sys.databases state_desc
	RecoveryModel string
	LogReuseWait  string // log_reuse_wait_desc
	EngineEdition int    // SERVERPROPERTY('EngineEdition'): 4 is Express
	EngineVersion string
	CPUCount      int // sys.dm_os_sys_info cpu_count; 0 when not read

	Files []StorageFile // this database's files, then tempdb's

	VLFCount *int // sys.dm_db_log_info; nil when not read

	TempdbAllocMB    *int64   // tempdb.sys.dm_db_file_space_usage total pages, in MB
	TempdbFreeMB     *int64   // its unallocated extents, in MB
	TempdbLogUsedPct *float64 // tempdb's sys.dm_db_log_space_usage

	// What fills tempdb (TMP03), read with TempdbAllocMB from tempdb.sys.dm_db_file_space_usage:
	// the reserved pages of user objects (temp tables), internal objects (sorts, hashes, spools)
	// and the version store, in MB.
	UserObjectsMB     int64
	InternalObjectsMB int64
	VersionStoreMB    int64
	// The three sessions holding the most tempdb pages now (sys.dm_db_session_space_usage plus
	// sys.dm_db_task_space_usage) and the oldest snapshot transaction
	// (sys.dm_tran_active_snapshot_database_transactions); TempdbSessionsError when not read.
	TempdbSessions      []TempdbSession
	OldestSnapshot      *SnapshotTransaction
	TempdbSessionsError string

	// TMP01: PAGELATCH_UP and PAGELATCH_EX wait time since the restart (sys.dm_os_wait_stats) and the
	// hours since the restart; PageLatchMs nil when not read (PageLatchError says why).
	PageLatchMs    *int64
	UptimeHours    float64
	PageLatchError string

	// The slowest autogrowth of this database's files in the default trace in the last 7 days, read
	// only when the default trace runs; nil when none or not read (SlowGrowthError says why).
	SlowGrowth      *SlowGrowth
	SlowGrowthError string
	// TempdbGrowth is tempdb's file growth events in the default trace in the last 7 days (TMP02,
	// docs/phase4e-contract.md): Count 0 when the trace holds none; nil when the trace is off or
	// could not be read (SlowGrowthError says why).
	TempdbGrowth *TraceGrowth

	LogSizeMB  *int64   // this database's sys.dm_db_log_space_usage total_log_size_in_bytes, in MB
	LogUsedPct *float64 // its used_log_space_in_percent

	TableCount    int
	TotalDataMB   float64        // every user table's data (heap or clustered index)
	TotalIndexMB  float64        // every user table's other indexes
	Tables        []TableSize    // the 10 biggest of 1 MB or more, biggest first
	Heaps         []TableSize    // the tables without a clustered index of 1 MB or more, biggest first (IDX06)
	GrowingTables []GrowingTable // the 10 biggest that look like log or history tables
	Identities    []IdentityColumn

	// Archives are kakapo's archive jobs on this database (docs/phase5a-contract.md, read by
	// collect.ArchiveJobs beside the storage facts): IDX10 does not fire for a table they archive,
	// and the card's Tables row lists them. ArchivesError says why they could not be read.
	Archives      []ArchiveJob
	ArchivesError string

	// ArchiveTables are the names of the tables in this database's archive schema, where kakapo's
	// archive writes (docs/archiving.md). A table with one of the same name was archived, even when
	// no job is left to show it (a restored copy).
	ArchiveTables []string
	// QueryStoreMB and QueryStoreMaxMB are Query Store's size and what it may use; nil unless Query
	// Store is on and its size was read.
	QueryStoreMB, QueryStoreMaxMB *int64

	ServerNow        time.Time // GETDATE()
	UTCOffsetMinutes int

	Error              string // nothing could be read at all
	FilesError         string // sys.master_files shows nothing of this database
	VolumeError        string // sys.dm_os_volume_stats
	LatencyError       string // sys.dm_io_virtual_file_stats
	CPUError           string // sys.dm_os_sys_info
	VLFError           string // sys.dm_db_log_info failed
	VLFUnsupported     bool   // sys.dm_db_log_info does not exist (before 2016 SP2)
	TempdbError        string // tempdb space usage
	SpaceUsedError     string // FILEPROPERTY inside the database
	TablesError        string // sys.dm_db_partition_stats inside the database
	GrowingTablesError string
	IdentityError      string
	LogSpaceError      string // sys.dm_db_log_space_usage inside the database
}

// AsOf is ServerNow in UTC.
func (f StorageFacts) AsOf() time.Time {
	t := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// DatabaseFiles are the chosen database's files.
func (f StorageFacts) DatabaseFiles() []StorageFile {
	out := []StorageFile{}
	for _, x := range f.Files {
		if x.DatabaseID != 2 {
			out = append(out, x)
		}
	}
	return out
}

// TempdbFiles are tempdb's files.
func (f StorageFacts) TempdbFiles() []StorageFile {
	out := []StorageFile{}
	for _, x := range f.Files {
		if x.DatabaseID == 2 {
			out = append(out, x)
		}
	}
	return out
}

// volume is one disk holding files.
type volume struct {
	Name    string
	TotalMB int64
	FreeMB  int64
}

func (v volume) freePct() float64 {
	if v.TotalMB <= 0 {
		return 0
	}
	return float64(v.FreeMB) * 100 / float64(v.TotalMB)
}

// volumesOf groups files by volume, ordered by name.
func volumesOf(files []StorageFile) []volume {
	byName := map[string]volume{}
	for _, x := range files {
		if x.Volume == "" {
			continue
		}
		v := byName[x.Volume]
		v.Name = x.Volume
		v.TotalMB = max(v.TotalMB, x.VolumeTotalMB)
		v.FreeMB = max(v.FreeMB, x.VolumeFreeMB)
		byName[x.Volume] = v
	}
	out := make([]volume, 0, len(byName))
	for _, v := range byName {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Texts from the scan (01-QuickScan.sql sections 3 and 4, STO08, TMP03) and
// PerformanceTriage.sql (IDX01, IDX07, IDX13), verbatim. IDX10 is kakapo's own.
const (
	sto01Why      = "When a disk fills up, databases cannot grow, transactions and backups fail, and applications stop working."
	sto01Do       = "Free up or add space with the infrastructure team. Look for old backup files, oversized log files (STO07) and files that could move to another volume."
	sto02Why      = "Percentage growth gets bigger as the file grows, so each growth takes longer and pauses activity. On log files it also creates too many VLFs (STO08)."
	sto03WhyOff   = "Once the file is full, inserts and updates fail. Fine only if it is deliberate and space is monitored."
	sto03WhySmall = "Many tiny growths fragment the file, create too many VLFs on log files, and each growth briefly pauses writes."
	sto03WhyFull  = "Once the file is full, inserts and updates fail."
	sto04Why      = "When the file reaches its maximum size it cannot grow, and inserts and updates start failing."
	sto04Express  = "Database is close to the 10 GB limit of SQL Server Express."
	sto04WhyExp   = "Express databases cannot grow past 10 GB of data. When the limit is reached, inserts fail and the application stops."
	sto04DoExp    = "Archive or purge old data, or plan an upgrade to Standard edition."
	sto05Finding  = "Database files are on the system drive (C:)."
	sto05Why      = "C: holds Windows. If a database fills it, the whole server can become unstable. It is usually also the smallest and slowest disk."
	sto05Do       = "Move the files to a dedicated data or log volume in a maintenance window (ALTER DATABASE ... MODIFY FILE, take the database offline, move the files, bring it online). For TempDB, change the path and restart SQL Server."
	sto06Finding  = "1 database(s) have data and log files on the same volume."
	sto06Why      = "Data files do random reads and writes while the log needs fast sequential writes, so on one disk they compete. Matters less on modern SAN or SSD storage."
	sto06Do       = "Low priority unless WRITELOG waits (WRK01) or log write latency (STO09) are high. When storage is next reviewed, place log files on a separate fast volume."
	sto07Why      = "An oversized log usually means log space is not being reused: missing log backups, a transaction left open, or replication falling behind. It wastes disk and slows restores."
	sto08Why      = "A log in many small pieces slows down database startup, restores, log backups and replication. It comes from many small autogrowths."
	sto08Do       = "In a quiet period: take a log backup, shrink the log file (DBCC SHRINKFILE), grow it back to the size it needs in one or a few large steps, and set a fixed autogrowth (STO02/STO03)."
	sto09WhyData  = "Every time a query needs data that is not in memory, it waits this long. Healthy storage is usually under 10 ms; above 20 ms users notice."
	sto09WhyLog   = "Every transaction commit waits for the log write. Healthy is under 5 ms; slow log writes make every insert and update slower."
	sto09Do       = "Share with the infrastructure/storage team (these are averages since the last restart; peaks are worse). Also check memory (INS02/INS03) and missing indexes (WRK03): less reading means less waiting."
	tmp01Why      = "With too few files, sessions that create temp tables or sort data queue for the same internal allocation pages (PAGELATCH waits), which slows everyone down under load."
	tmp02Finding  = "TempDB data files are not all the same size and growth."
	tmp02Why      = "SQL Server spreads work across TempDB files by free space. If one file is bigger it gets most of the work, which defeats the point of having several files."
	tmp02Do       = "Apply \"Set up the TempDB files\": it grows the smaller files to the size of the largest and gives every file the same fixed growth, online and at once. Shrinking a larger file is best done by hand in a restart window."
	tmp03Why      = "When TempDB is full, every query that sorts, hashes or uses a temp table fails, in every database on the server."
	tmp03Do       = "Find what is using it right now (sys.dm_db_session_space_usage shows the pages per session: a runaway report, a huge sort, or a session holding a temp table) and deal with that query. If this is its normal size, pre-size TempDB larger in a maintenance window and make sure it can grow."
	idx01Why      = "The biggest tables in this database. Tables where indexes are much bigger than the data are flagged in IDX02."
	idx06Why      = "Without a clustered index, an updated row that no longer fits is moved and leaves a forwarding pointer behind, so reads get slower over time, and the space of deleted rows is not always reused."
	idx06WhyNone  = " With no other index either, every query on it reads the whole table."
	idx06Do       = "Add a clustered index in a maintenance window (it rewrites the whole table and needs about its size free in the data files). Choose the key with the application's owner: usually the primary key or an ever-increasing ID column."
	idx07WhyFile  = "Usually a copy of a bigger database, or data that was deleted. The empty space costs disk and backup storage at every copy."
	idx07WhyLog   = "The log is not being emptied, usually because of missing log backups or a transaction left open (ACT02). It keeps growing until the disk is full."
	idx10Why      = "Tables that only grow slow every backup, every integrity check and every query that reads them, and fill the disk."
	idx10Do       = "Agree a retention period with the application's owner (how many months must stay) and have old rows deleted in batches in a quiet hour, or archived; then a scheduled cleanup keeps it that way. Never delete without the owner's say-so."
	idx13Why      = "When the identity reaches the end of its range, every insert into this table fails with an arithmetic overflow error. There is no warning before it happens."
	idx13DoHigh   = "Plan the change to bigint now (the column, every foreign key that references it, and the code that holds the value). Deleting old rows (IDX10) does not reset the identity."
	idx13DoLow    = "Plan a change to bigint with the next major maintenance. If the table is purged regularly and the old values are never referenced, a DBCC CHECKIDENT reseed is an alternative: decide first."

	// STO11 and STO12, from the contract's addendum.
	sto11Why      = "When the log is full and can neither be reused nor grow, every insert, update and delete in this database fails with error 9002 until it is."
	sto11DoNoGrow = "Free space on %s or raise the file's growth or maximum size."

	// From docs/phase3f-contract.md items 17, 18 and 20.
	sto02Log2022   = " On SQL Server 2022 and later log growth up to 64 MB is instant, so 64 MB steps cost nothing."
	tmp03NormSize  = " If this is its normal size, pre-size TempDB larger in a maintenance window and make sure it can grow."
	tmp03DoUser    = "Mostly temp tables: the sessions holding the most pages are in the evidence. Find what they run (a report, an import, a session that keeps temp tables open) and have it finish or clean up." + tmp03NormSize
	tmp03DoIntern  = "Mostly sorts, hashes and spools of queries that spill to TempDB: tune the costliest queries (QRY01 on the Performance card), usually by updating statistics first (MNT04)." + tmp03NormSize
	sto12Why       = "When the disk fills, the database cannot grow, transactions and backups fail, and the application stops."
	sto12Do        = "Free or add space with the infrastructure team before then, and find what is growing: the log (STO07, STO11), tables that only grow (IDX10) or backup files on the same drive."
	sto12NeedsPair = "Needs samples a day or more apart"

	// Sto10NotRun is STO10's note in the checks until it is run on request.
	Sto10NotRun = "Not run yet. Check room for CHECKDB reads an estimate from SQL Server; it can take a minute on a large database."
	vlfOldNote  = "Needs SQL Server 2016 SP2 or later."
)

// BuildStorageCard answers "Will it run out of room?" for one database from what was read: the
// card, its findings ranked, and what became of each check behind it, in catalogue order.
// lastRoom is the last stored STO10 result, or nil when it was never run; it decides only STO10's
// line in the checks (the card detail carries the result itself). trend is the pair of stored
// volume samples STO12 forecasts from (the service reads them from the store), or nil when there
// are none.
func BuildStorageCard(f StorageFacts, def CardDef, lastRoom *CheckdbRoom, trend *DiskTrend) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	b := &storageBuild{f: f, trend: trend, findings: []Finding{}, skipped: map[string]string{}}

	switch {
	case f.Error != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", f.Error
		b.skipAll(def.ID, f.Error)
		return c, b.findings, b.checks(def.ID, nil)
	case f.State != "ONLINE":
		note := fmt.Sprintf("%s is %s, not online, so its storage was not checked.", f.Database, f.State)
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID, nil)
	}

	greyNote, filesNote, volNote := "", "", ""
	if f.FilesError != "" {
		filesNote = "Could not read the database files: " + f.FilesError
		greyNote = filesNote
	}
	if f.VolumeError != "" {
		volNote = "Could not read the volumes (sys.dm_os_volume_stats): " + f.VolumeError
		if greyNote == "" {
			greyNote = volNote
		}
	}

	if filesNote != "" {
		for _, id := range []string{"STO01", "STO02", "STO03", "STO04", "STO05", "STO06", "STO07", "STO09", "STO11", "STO12"} {
			b.skipped[id] = filesNote
		}
	} else {
		if volNote != "" {
			for _, id := range []string{"STO01", "STO05", "STO06", "STO12"} {
				b.skipped[id] = volNote
			}
		} else {
			b.sto01()
			b.sto05()
			b.sto06()
			b.sto12()
		}
		b.sto02()
		b.sto03()
		b.sto04()
		b.sto07()
		b.sto11()
		if f.LatencyError != "" {
			b.skipped["STO09"] = "Could not read file latency (sys.dm_io_virtual_file_stats): " + f.LatencyError
		} else {
			b.sto09()
		}
	}
	b.sto08()
	b.tmp01()
	b.tmp02()
	b.tmp03()
	b.idx01()
	b.idx06()
	b.idx07(filesNote)
	b.idx10()
	b.idx13()
	b.skipped["STO10"] = Sto10NotRun
	RankFindings(b.findings)
	checks := b.checks(def.ID, lastRoom)

	if greyNote != "" {
		// Without the files or the volumes the card cannot answer its question. The checks that
		// do not need them still ran, and their findings are kept for the detail page.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", greyNote
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

type storageBuild struct {
	f        StorageFacts
	trend    *DiskTrend
	findings []Finding
	skipped  map[string]string
}

func (b *storageBuild) add(id string, rank int, changeType, effort, object, finding, why, doThis, evidence string) {
	chk, _ := CheckByID(id)
	b.findings = append(b.findings, Finding{
		CheckID: id, Title: chk.Title, Severity: SeverityFromRank(min(rank, 5)), Owner: chk.Owner,
		When: WhenFromChangeType(changeType), Effort: effort, Object: object,
		Finding: finding, Why: why, DoThis: doThis, ThenCheck: chk.ThenCheck, Evidence: evidence,
	})
}

// withFix sets the fix the app can apply on the finding just added (docs/phase4d-contract.md).
func (b *storageBuild) withFix(fixID string) { b.findings[len(b.findings)-1].FixID = fixID }

func (b *storageBuild) all(id string) []Finding {
	out := []Finding{}
	for _, x := range b.findings {
		if x.CheckID == id {
			out = append(out, x)
		}
	}
	return out
}

func (b *storageBuild) skipAll(card CardID, note string) {
	for _, c := range ChecksFor(card) {
		b.skipped[c.ID] = note
	}
}

// checks says what became of each check: skipped with its note, a finding when it found
// something to act on, passed otherwise. STO10 follows the last stored result when there is one.
func (b *storageBuild) checks(card CardID, lastRoom *CheckdbRoom) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(card) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
		note, skipped := b.skipped[c.ID]
		switch {
		case c.ID == "STO10" && lastRoom != nil && note == Sto10NotRun:
			if lastRoom.Finding.Severity.Actionable() {
				st.Status = CheckFinding
			}
		case skipped:
			st.Status, st.Note = CheckSkipped, note
		default:
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

// alterFile is the scan's ALTER DATABASE ... MODIFY FILE statement.
func alterFile(db, logical, setting string) string {
	return "ALTER DATABASE " + QuoteName(db) + " MODIFY FILE (NAME = N'" + strings.ReplaceAll(logical, "'", "''") + "', " + setting + ");"
}

// sto01: each volume holding a file of this database with under 20% or under 5 GB free.
func (b *storageBuild) sto01() {
	for _, v := range volumesOf(b.f.DatabaseFiles()) {
		if v.TotalMB <= 0 || (v.freePct() >= 20 && v.FreeMB >= 5120) {
			continue
		}
		rank := 3
		if v.freePct() < 10 || v.FreeMB < 5120 {
			rank = 2
		}
		b.add("STO01", rank, "Online", EffortModerate, v.Name,
			fmt.Sprintf("Volume %s is %d%% full.", v.Name, int(100-v.freePct())), sto01Why, sto01Do,
			fmt.Sprintf("Free: %d GB of %d GB.", v.FreeMB/1024, v.TotalMB/1024))
	}
}

// growthFiles are the files STO02 and STO03 look at: this database's and tempdb's.
func (b *storageBuild) growthFiles() []StorageFile {
	return append(b.f.DatabaseFiles(), b.f.TempdbFiles()...)
}

// slowGrowthOf is the slow-growth evidence sentence for a file of this database, with its
// leading space, when the slowest growth in the default trace was this file and took a second or
// more; "" otherwise.
func (b *storageBuild) slowGrowthOf(x StorageFile) string {
	g := b.f.SlowGrowth
	if g == nil || x.DatabaseID == 2 || !strings.EqualFold(g.File, x.LogicalName) {
		return ""
	}
	return SlowGrowthSentence(g.Ms, g.File, g.At)
}

// sto02: percentage autogrowth.
func (b *storageBuild) sto02() {
	for _, x := range b.growthFiles() {
		if !x.IsPercentGrowth || x.Growth <= 0 {
			continue
		}
		suggest := int64(64)
		switch {
		case x.SizeMB >= 10240:
			suggest = 1024
		case x.SizeMB >= 1024:
			suggest = 256
		}
		doThis := "Use a fixed growth: " + alterFile(x.Database, x.LogicalName, fmt.Sprintf("FILEGROWTH = %dMB", suggest))
		if x.isLog() && EngineMajor(b.f.EngineVersion) >= 16 {
			doThis += sto02Log2022
		}
		b.add("STO02", 3, "Online", EffortQuickWin, x.LogicalName,
			fmt.Sprintf("File %s (%s) grows by %d%%.", x.LogicalName, strings.ToLower(x.Type), x.Growth), sto02Why,
			doThis, fmt.Sprintf("Current size: %d MB.", x.SizeMB)+b.slowGrowthOf(x))
		b.withFix(FixAutogrowth)
	}
}

// sto03: autogrowth switched off, or under 64 MB on a file of 1 GB or more. A file with
// autogrowth off that is 90% or more used is High.
func (b *storageBuild) sto03() {
	for _, x := range b.growthFiles() {
		growthMB := x.Growth * 8 / 1024
		if x.IsPercentGrowth || !(x.Growth == 0 || (growthMB < 64 && x.SizeMB >= 1024)) {
			continue
		}
		rank, evidence := 4, fmt.Sprintf("Current size: %d MB.", x.SizeMB)
		finding, why := fmt.Sprintf("File %s grows in small steps of %d MB.", x.LogicalName, growthMB), sto03WhySmall
		if x.Growth == 0 {
			finding, why = "File "+x.LogicalName+" has autogrowth switched off.", sto03WhyOff
			if pct, ok := b.f.usedPct(x); ok && pct >= 90 {
				rank = 2
				finding = fmt.Sprintf("File %s has autogrowth switched off and is %d%% full.", x.LogicalName, int(pct))
				why = sto03WhyFull
				evidence = fmt.Sprintf("Current size: %d MB, %s%% used.", x.SizeMB, n1(pct))
			}
		}
		suggest := 256
		if x.SizeMB >= 10240 {
			suggest = 1024
		}
		b.add("STO03", rank, "Online", EffortQuickWin, x.LogicalName, finding, why,
			alterFile(x.Database, x.LogicalName, fmt.Sprintf("FILEGROWTH = %dMB", suggest)), evidence+b.slowGrowthOf(x))
		b.withFix(FixAutogrowth)
	}
}

// usedPct is how full a file is: a data file of this database from FILEPROPERTY(name,
// 'SpaceUsed'), a log file from used_log_space_in_percent (tempdb's log from tempdb's). false when
// that was not read.
func (f StorageFacts) usedPct(x StorageFile) (float64, bool) {
	switch {
	case x.isLog() && x.DatabaseID == 2:
		if f.TempdbLogUsedPct != nil {
			return *f.TempdbLogUsedPct, true
		}
	case x.isLog():
		if f.LogUsedPct != nil && f.LogSpaceError == "" {
			return *f.LogUsedPct, true
		}
	case x.isData() && x.UsedMB != nil && x.SizeMB > 0:
		return float64(*x.UsedMB) * 100 / float64(x.SizeMB), true
	}
	return 0, false
}

// maxSizeSet says whether a file has a maximum size of its own: not unlimited (-1), not the 2 TB
// "unlimited" of a log file, and not 0 (no growth at all, which STO03 reports).
func maxSizeSet(pages int64) bool { return pages != -1 && pages != 0 && pages != 268435456 }

// sto04: a file at 90% of its maximum size; on Express, data of 8 GB or more.
func (b *storageBuild) sto04() {
	dataMB := int64(0)
	for _, x := range b.f.DatabaseFiles() {
		if x.isData() {
			dataMB += x.SizeMB
		}
		maxMB := x.MaxSizePages * 8 / 1024
		if !maxSizeSet(x.MaxSizePages) || float64(x.SizeMB) < float64(maxMB)*0.9 {
			continue
		}
		pct := 0
		if maxMB > 0 {
			pct = int(float64(x.SizeMB) * 100 / float64(maxMB))
		}
		b.add("STO04", 2, "Online", EffortQuickWin, x.LogicalName,
			fmt.Sprintf("File %s is at %d%% of its maximum size.", x.LogicalName, pct), sto04Why,
			"Raise or remove the limit (check disk space first): "+alterFile(x.Database, x.LogicalName, "MAXSIZE = UNLIMITED"),
			fmt.Sprintf("Size: %d MB, max: %d MB.", x.SizeMB, maxMB))
	}
	if b.f.EngineEdition == 4 && dataMB >= 8192 {
		rank := 3
		if dataMB >= 9216 {
			rank = 2
		}
		b.add("STO04", rank, "Planned", EffortProject, b.f.Database, sto04Express, sto04WhyExp, sto04DoExp,
			fmt.Sprintf("Data size: %d MB.", dataMB))
	}
}

// sto05: any file of this database on C:.
func (b *storageBuild) sto05() {
	paths := []string{}
	for _, x := range b.f.DatabaseFiles() {
		if strings.EqualFold(x.Volume, `C:\`) {
			paths = append(paths, x.PhysicalName)
		}
	}
	if len(paths) == 0 {
		return
	}
	b.add("STO05", 3, "Offline", EffortModerate, b.f.Database, sto05Finding, sto05Why, sto05Do,
		"Files: "+strings.Join(paths, ", "))
}

// sto06: a data file and a log file of this database on one volume.
func (b *storageBuild) sto06() {
	files := b.f.DatabaseFiles()
	for _, d := range files {
		for _, l := range files {
			if d.isData() && l.isLog() && d.Volume != "" && d.Volume == l.Volume {
				b.add("STO06", 4, "Offline", EffortProject, b.f.Database, sto06Finding, sto06Why, sto06Do,
					"Databases: "+b.f.Database)
				return
			}
		}
	}
}

// dataLogMB are the sizes of this database's data and log files.
func (b *storageBuild) dataLogMB() (data, log int64) {
	for _, x := range b.f.DatabaseFiles() {
		switch {
		case x.isData():
			data += x.SizeMB
		case x.isLog():
			log += x.SizeMB
		}
	}
	return data, log
}

// sto07: the log is bigger than the data and at least 1 GB.
func (b *storageBuild) sto07() {
	data, log := b.dataLogMB()
	if log <= data || log < 1024 {
		return
	}
	b.add("STO07", 3, "Online", EffortModerate, b.f.Database,
		fmt.Sprintf("The log file (%d MB) is larger than the data (%d MB).", log, data), sto07Why, sto07Do(b.f.LogReuseWait),
		"Log reuse is waiting on: "+b.f.LogReuseWait)
}

// sto07Do is the scan's STO07 Do this for what the log is waiting on.
func sto07Do(reuse string) string {
	switch reuse {
	case "LOG_BACKUP":
		return "The log is waiting for a log backup. Schedule regular log backups (BAK02), then shrink the log once to a sensible size in a quiet period."
	case "ACTIVE_TRANSACTION":
		return "A long-running open transaction is holding the log. Find it with DBCC OPENTRAN and have it finished, then shrink the log once."
	case "REPLICATION":
		return "Replication or CDC is behind. Ask a DBA to check the log reader agent."
	case "AVAILABILITY_REPLICA":
		return "An Availability Group replica is behind. Check the AG dashboard and the secondary replica."
	}
	return "Find out why the log grew (often a one-off large operation). Once resolved, shrink the log once to a sensible size in a quiet period."
}

// LogReuseReason is what the log is waiting on, in words, as STO07's Do this maps
// log_reuse_wait_desc; anything else is the raw value in lower case.
func LogReuseReason(reuse string) string {
	switch reuse {
	case "LOG_BACKUP":
		return "a log backup"
	case "ACTIVE_TRANSACTION":
		return "a transaction that is still open"
	case "REPLICATION":
		return "replication or CDC"
	case "AVAILABILITY_REPLICA":
		return "an Availability Group replica"
	case "NOTHING":
		return "nothing: the space is reused at the next checkpoint"
	}
	return strings.ToLower(reuse)
}

// logGrowth says whether this database's log can grow: a log file with growth > 0, not at its
// max size, on a volume with free space of at least one growth step (a percentage step on the
// current size). When the volume was not read, its free space is not held against it. It also
// returns the log file it looked at, for the evidence and the Do this.
func (f StorageFacts) logGrowth() (bool, StorageFile, bool) {
	var first StorageFile
	found := false
	for _, x := range f.DatabaseFiles() {
		if !x.isLog() {
			continue
		}
		if !found {
			first, found = x, true
		}
		if x.Growth <= 0 {
			continue
		}
		step := x.Growth * 8 / 1024
		if x.IsPercentGrowth {
			step = x.SizeMB * x.Growth / 100
		}
		if x.MaxSizePages != -1 && x.SizeMB >= x.MaxSizePages*8/1024 {
			continue
		}
		if x.Volume != "" && x.VolumeFreeMB < step {
			continue
		}
		return true, x, true
	}
	return false, first, found
}

// sto11: the transaction log is 80% or more used and waiting on something, or cannot grow.
func (b *storageBuild) sto11() {
	f := b.f
	switch {
	case f.LogSpaceError != "":
		b.skipped["STO11"] = "Could not read the log space used (sys.dm_db_log_space_usage): " + f.LogSpaceError
		return
	case f.LogUsedPct == nil || f.LogSizeMB == nil:
		b.skipped["STO11"] = "Could not read the log space used (sys.dm_db_log_space_usage)."
		return
	}
	pct, size := *f.LogUsedPct, *f.LogSizeMB
	canGrow, x, _ := f.logGrowth()
	rank := 0
	switch {
	case pct >= 90 && !canGrow:
		rank = 2
	case pct >= 80 && (f.LogReuseWait != "NOTHING" || !canGrow):
		rank = 3
	default:
		return
	}
	usedMB := float64(size) * pct / 100
	finding := fmt.Sprintf("The transaction log is %d%% full (%s GB of %s GB) and is waiting on %s.",
		int(pct), n1(usedMB/1024), n1(float64(size)/1024), LogReuseReason(f.LogReuseWait))
	if rank == 2 {
		finding += " It cannot grow."
	}
	doThis := sto07Do(f.LogReuseWait)
	if !canGrow {
		vol := x.Volume
		if vol == "" {
			vol = "the log's volume"
		}
		doThis += " " + fmt.Sprintf(sto11DoNoGrow, vol)
	}
	growth := fmt.Sprintf("%d MB", x.Growth*8/1024)
	if x.IsPercentGrowth {
		growth = fmt.Sprintf("%d%%", x.Growth)
	}
	maxSize := "unlimited"
	switch {
	case maxSizeSet(x.MaxSizePages):
		maxSize = n0int(x.MaxSizePages*8/1024) + " MB"
	case x.MaxSizePages == 0:
		maxSize = "none (no growth)"
	}
	volFree := "not read"
	if x.Volume != "" {
		volFree = x.Volume + " " + n0int(x.VolumeFreeMB) + " MB"
	}
	b.add("STO11", rank, "Online", EffortQuickWin, f.Database, finding, sto11Why, doThis,
		fmt.Sprintf("Size: %s MB. Used: %s%% (%s MB). Reuse wait: %s. Growth: %s. Max size: %s. Volume free: %s.",
			n0int(size), n1(pct), n0(usedMB), f.LogReuseWait, growth, maxSize, volFree))
}

// sto08: more than 500 virtual log files.
func (b *storageBuild) sto08() {
	f := b.f
	switch {
	case f.VLFUnsupported:
		b.skipped["STO08"] = vlfOldNote
		return
	case f.VLFError != "":
		b.skipped["STO08"] = "Could not count VLFs for this database: " + f.VLFError
		return
	case f.VLFCount == nil:
		b.skipped["STO08"] = "Could not count VLFs for this database."
		return
	case *f.VLFCount <= 500:
		return
	}
	rank := 4
	if *f.VLFCount > 1000 {
		rank = 3
	}
	b.add("STO08", rank, "Offline", EffortModerate, f.Database,
		fmt.Sprintf("The transaction log is split into %d virtual log files (VLFs).", *f.VLFCount), sto08Why, sto08Do,
		fmt.Sprintf("VLF count: %d.", *f.VLFCount))
}

// sto09: slow data reads or log writes since the restart, one finding per volume and kind (a
// volume with one slow file reads as that file); the rank is the worst file's.
func (b *storageBuild) sto09() {
	type group struct {
		volume string
		log    bool
		files  []StorageFile
	}
	groups := []*group{}
	byKey := map[string]*group{}
	for _, x := range b.f.DatabaseFiles() {
		if !x.HasIO {
			continue
		}
		slowData := x.isData() && x.Reads >= 1000 && x.AvgReadMs() >= 20
		slowLog := x.isLog() && x.Writes >= 1000 && x.AvgWriteMs() >= 10
		if !slowData && !slowLog {
			continue
		}
		key := fmt.Sprintf("%s|%v", x.Volume, slowLog)
		if x.Volume == "" {
			key += "|" + x.LogicalName // a file whose volume was not read stands alone
		}
		g, ok := byKey[key]
		if !ok {
			g = &group{volume: x.Volume, log: slowLog}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.files = append(g.files, x)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].volume != groups[j].volume {
			return groups[i].volume < groups[j].volume
		}
		return !groups[i].log && groups[j].log
	})
	for _, g := range groups {
		// The worst file decides the rank and is named; the average is over the group's I/O.
		worst := g.files[0]
		var ops, stall int64
		parts := []string{}
		for _, x := range g.files {
			avg, worstAvg := x.AvgReadMs(), worst.AvgReadMs()
			if g.log {
				avg, worstAvg = x.AvgWriteMs(), worst.AvgWriteMs()
				ops, stall = ops+x.Writes, stall+x.WriteStallMs
			} else {
				ops, stall = ops+x.Reads, stall+x.ReadStallMs
			}
			if avg > worstAvg {
				worst = x
			}
			parts = append(parts, fmt.Sprintf("%s: reads %d (avg %d ms), writes %d (avg %d ms)", x.PhysicalName, x.Reads, x.AvgReadMs(), x.Writes, x.AvgWriteMs()))
		}
		worstMs, kind, why, high := worst.AvgReadMs(), "Data reads", sto09WhyData, int64(50)
		if g.log {
			worstMs, kind, why, high = worst.AvgWriteMs(), "Log writes", sto09WhyLog, 20
		}
		rank := 3
		if worstMs >= high {
			rank = 2
		}
		if len(g.files) == 1 {
			x := g.files[0]
			b.add("STO09", rank, "Planned", EffortProject, x.LogicalName,
				fmt.Sprintf("%s average %d ms on %s.", kind, worstMs, x.PhysicalName), why, sto09Do,
				fmt.Sprintf("Reads: %d (avg %d ms). Writes: %d (avg %d ms).", x.Reads, x.AvgReadMs(), x.Writes, x.AvgWriteMs()))
			continue
		}
		b.add("STO09", rank, "Planned", EffortProject, g.volume,
			fmt.Sprintf("%s average %d ms on %d files on %s (%s is the slowest at %d ms).", kind, stall/ops, len(g.files), g.volume, worst.LogicalName, worstMs),
			why, sto09Do, strings.Join(parts, "; ")+".")
	}
}

// tempdbData are tempdb's data files.
func (f StorageFacts) tempdbData() []StorageFile {
	out := []StorageFile{}
	for _, x := range f.TempdbFiles() {
		if x.isData() {
			out = append(out, x)
		}
	}
	return out
}

// tmp01: fewer tempdb data files than min(CPUs, 8).
func (b *storageBuild) tmp01() {
	files := b.f.tempdbData()
	switch {
	case len(files) == 0:
		b.skipped["TMP01"] = "Could not read the TempDB files."
		return
	case b.f.CPUCount <= 0:
		note := "Could not read the number of CPUs (sys.dm_os_sys_info needs VIEW SERVER STATE)."
		if b.f.CPUError != "" {
			note = "Could not read the number of CPUs: " + b.f.CPUError
		}
		b.skipped["TMP01"] = note
		return
	}
	target := min(b.f.CPUCount, 8)
	n := len(files)
	if n >= target {
		return
	}
	maxMB, folder := int64(0), ""
	for i, x := range files {
		maxMB = max(maxMB, x.SizeMB)
		dir := x.PhysicalName[:strings.LastIndex(x.PhysicalName, `\`)+1]
		if i == 0 || dir < folder {
			folder = dir
		}
	}
	if folder == "" {
		folder = `<folder>\`
	}
	rank := 4
	if n == 1 {
		rank = 3
	}
	finding := fmt.Sprintf("TempDB has %d data file(s); %d is recommended for this server.", n, target)
	evidence := fmt.Sprintf("Data files: %d. Logical CPUs: %d.", n, b.f.CPUCount)
	if pct, ok := b.f.pageLatchPct(); ok {
		evidence += fmt.Sprintf(" PAGELATCH waits: %s%% of uptime.", n1(pct))
		if pct >= 1 {
			// Likely, not certain: PAGELATCH waits are not only on TempDB's allocation pages.
			rank--
			finding = fmt.Sprintf("TempDB has %d data file(s); %d is recommended for this server, and the server is likely already queuing on TempDB allocation pages (%s%% of uptime in PAGELATCH waits).",
				n, target, n1(pct))
		}
	}
	b.add("TMP01", rank, "Online", EffortQuickWin, "tempdb", finding, tmp01Why,
		fmt.Sprintf("Add data files until there are %d, all the same size and growth. Example: ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev%d', FILENAME = N'%stempdb%d.ndf', SIZE = %dMB, FILEGROWTH = 256MB);  No restart needed.",
			target, n+1, folder, n+1, maxMB),
		evidence)
	b.withFix(FixAddTempdbFiles)
}

// pageLatchPct is the PAGELATCH_UP and PAGELATCH_EX wait time as a share of the time since the
// restart (at least one hour); false when it was not read.
func (f StorageFacts) pageLatchPct() (float64, bool) {
	if f.PageLatchMs == nil || f.PageLatchError != "" {
		return 0, false
	}
	hours := max(f.UptimeHours, 1)
	return float64(*f.PageLatchMs) * 100 / (hours * 3600000), true
}

// TMP02's second trigger (docs/phase4e-contract.md): TempDB grows often.
const (
	tmp02GrowFinding = "TempDB grew %d times in the last 7 days (%d data, %d log growths; the slowest took %s s): its files start smaller than the workload needs, so users wait while they grow."
	tmp02GrowWhy     = "Each growth pauses whoever needs the space. TempDB is recreated at its configured size on every restart, so a size it grows to every day should be its starting size."
	tmp02GrowDo      = "Apply \"Set up the TempDB files\": it makes the size TempDB has grown to the starting size of every file and gives each a fixed growth, online and at once."
	tmp02BothFinding = " Its data files are also not all the same size and growth."
	tmp02BothDo      = "Apply \"Set up the TempDB files\": it makes the data files equal at the size TempDB has grown to, makes that their starting size and gives every file a fixed growth, online and at once."
)

// tmp02: tempdb data files of different size or growth, or tempdb growing 20 or more times in 7
// days (docs/phase4e-contract.md; 100 or more is Medium). One finding for either or both, with the
// growth numbers in its evidence whenever the trace was read; both carry the tempdb-files fix.
func (b *storageBuild) tmp02() {
	files := b.f.tempdbData()
	if len(files) == 0 {
		b.skipped["TMP02"] = "Could not read the TempDB files."
		return
	}
	differ := false
	parts := []string{}
	for _, x := range files {
		if x.SizeMB != files[0].SizeMB || x.Growth != files[0].Growth || x.IsPercentGrowth != files[0].IsPercentGrowth {
			differ = true
		}
		growth := fmt.Sprintf("%d MB", x.Growth*8/1024)
		if x.IsPercentGrowth {
			growth = fmt.Sprintf("%d%%", x.Growth)
		}
		parts = append(parts, fmt.Sprintf("%s %d MB / growth %s", x.LogicalName, x.SizeMB, growth))
	}
	g := b.f.TempdbGrowth
	grows := g != nil && g.Count >= 20
	if !differ && !grows {
		return
	}
	rank, finding, why, doThis := 4, tmp02Finding, tmp02Why, tmp02Do
	if grows {
		if g.Count >= 100 {
			rank = 3
		}
		finding = fmt.Sprintf(tmp02GrowFinding, g.Count, g.Count-g.LogGrows, g.LogGrows, n1(float64(g.SlowestMs)/1000))
		why, doThis = tmp02GrowWhy, tmp02GrowDo
		if differ {
			finding += tmp02BothFinding
			why += " " + tmp02Why
			doThis = tmp02BothDo
		}
	}
	b.add("TMP02", rank, "Online", EffortQuickWin, "tempdb", finding, why, doThis,
		"Files: "+strings.Join(parts, ", ")+"."+b.f.tempdbGrowthEvidence())
	b.withFix(FixTempdbFiles)
}

// tempdbGrowthEvidence is TempDB's growth history for TMP02's evidence, with a leading space: the
// numbers when the trace holds growths, "none" when it holds none, the reason when it was not read.
func (f StorageFacts) tempdbGrowthEvidence() string {
	g := f.TempdbGrowth
	switch {
	case g == nil && f.SlowGrowthError != "":
		return " TempDB growth history not read: " + strings.TrimSuffix(f.SlowGrowthError, ".") + "."
	case g == nil:
		return ""
	case g.Count == 0:
		return " No TempDB growth in the default trace in the last 7 days."
	}
	return fmt.Sprintf(" TempDB growths in the last 7 days: %d (%d data, %d log), %s s spent growing, the last at %s; the slowest took %s s (%s, %s).",
		g.Count, g.Count-g.LogGrows, g.LogGrows, n0int(g.Seconds), sqlTime(g.LastAt),
		n1(float64(g.SlowestMs)/1000), orQ(g.SlowestFile), sqlTime(g.SlowestAt))
}

// tempdbUse is how full tempdb is, as TMP03 reads it.
type tempdbUse struct {
	AllocMB, FreeMB int64
	UsedPct         int64
	LogPct          *float64
	VolFreeMB       *int64
	CanGrow         bool
}

func (f StorageFacts) tempdbUse() (tempdbUse, bool) {
	if f.TempdbAllocMB == nil || f.TempdbFreeMB == nil {
		return tempdbUse{}, false
	}
	u := tempdbUse{AllocMB: *f.TempdbAllocMB, FreeMB: *f.TempdbFreeMB, LogPct: f.TempdbLogUsedPct}
	if u.AllocMB > 0 {
		u.UsedPct = (u.AllocMB - u.FreeMB) * 100 / u.AllocMB
	}
	for _, x := range f.tempdbData() {
		if x.Volume != "" && (u.VolFreeMB == nil || x.VolumeFreeMB < *u.VolFreeMB) {
			free := x.VolumeFreeMB
			u.VolFreeMB = &free
		}
		if x.Growth > 0 && (x.MaxSizePages == -1 || x.MaxSizePages > x.SizeMB*128) {
			u.CanGrow = true
		}
	}
	return u, true
}

// raised says whether TMP03 fires: tempdb half full (and 512 MB or more, or 80 % full), or its log 70 %
// full.
func (u tempdbUse) raised() bool {
	logPct := -1.0
	if u.LogPct != nil {
		logPct = *u.LogPct
	}
	return (u.UsedPct >= 50 && (u.AllocMB >= 512 || u.UsedPct >= 80)) || logPct >= 70
}

// short says whether the drive under tempdb has less free than a quarter of its size.
func (u tempdbUse) short() bool {
	volFree := int64(0)
	if u.VolFreeMB != nil {
		volFree = *u.VolFreeMB
	}
	return volFree < u.AllocMB/4
}

// TempdbRoom is tempdb's size and room as the Storage card read it (TMP03's facts), for the row
// versioning decision's cost (docs/phase5m-contract.md, section 5): nothing is read twice.
type TempdbRoom struct {
	AllocMB      int64  `json:"allocMB"`
	FreeMB       int64  `json:"freeMB"`
	CanGrow      bool   `json:"canGrow"`
	VolumeFreeMB *int64 `json:"volumeFreeMB,omitempty"`
	// NoRoom: TMP03 is raised and says it cannot grow or the disk is short ("cannot grow", "the disk has
	// only ..."): TempDB cannot hold more.
	NoRoom bool `json:"noRoom"`
}

// TempdbRoomOf is tempdb's room from the storage facts; nil when it was not read.
func TempdbRoomOf(f StorageFacts) *TempdbRoom {
	u, ok := f.tempdbUse()
	if !ok {
		return nil
	}
	return &TempdbRoom{AllocMB: u.AllocMB, FreeMB: u.FreeMB, CanGrow: u.CanGrow, VolumeFreeMB: u.VolFreeMB,
		NoRoom: u.raised() && (!u.CanGrow || u.short())}
}

// tmp03: tempdb more than half full, and whether it has room to grow.
func (b *storageBuild) tmp03() {
	u, ok := b.f.tempdbUse()
	if !ok {
		note := "Could not read how full TempDB is (needs VIEW SERVER STATE)."
		if b.f.TempdbError != "" {
			note = "Could not read how full TempDB is: " + b.f.TempdbError
		}
		b.skipped["TMP03"] = note
		return
	}
	logPct := -1.0
	if u.LogPct != nil {
		logPct = *u.LogPct
	}
	if !u.raised() {
		return
	}
	short := u.short()
	rank := 3
	if (u.UsedPct >= 80 || logPct >= 90) && (!u.CanGrow || short) {
		rank = 2
	}
	logText := "?"
	if u.LogPct != nil {
		logText = fmt.Sprintf("%.1f", *u.LogPct)
	}
	finding := fmt.Sprintf("The TempDB log is %s%% full", logText)
	if u.UsedPct >= 50 {
		finding = fmt.Sprintf("TempDB is %d%% full (%s MB of %s MB)", u.UsedPct, n0(float64(u.AllocMB-u.FreeMB)), n0(float64(u.AllocMB)))
	}
	volText := "?"
	if u.VolFreeMB != nil {
		volText = n0(float64(*u.VolFreeMB))
	}
	switch {
	case !u.CanGrow:
		finding += " and its files cannot grow."
	case short:
		finding += " and the disk has only " + volText + " MB free."
	default:
		finding += "."
	}
	canGrow := "no"
	if u.CanGrow {
		canGrow = "yes"
	}
	doThis := tmp03Do
	evidence := fmt.Sprintf("Data %d%% used, log %s%% used. Volume free: %s MB. Can grow: %s.", u.UsedPct, logText, volText, canGrow)
	if u.UsedPct >= 50 {
		if mostly, do, ok := b.f.tempdbFiller(); ok {
			finding += " Mostly " + mostly + "."
			doThis = do
		}
	}
	evidence += b.f.tempdbFillerEvidence()
	b.add("TMP03", rank, "None", EffortModerate, "tempdb", finding, tmp03Why, doThis, evidence)
}

// tempdbFiller says what fills tempdb most, in words for the TMP03 sentence, and the Do this for
// it; false when the breakdown was not read.
func (f StorageFacts) tempdbFiller() (string, string, bool) {
	user, internal, version := f.UserObjectsMB, f.InternalObjectsMB, f.VersionStoreMB
	if user+internal+version <= 0 {
		return "", "", false
	}
	switch {
	case version >= user && version >= internal:
		mostly := "the version store"
		do := "The version store keeps row versions for the oldest open snapshot transaction and is emptied only when it ends: find that transaction and have it finish."
		if t := f.OldestSnapshot; t != nil {
			mins := t.ElapsedSeconds / 60
			mostly = fmt.Sprintf("the version store (a transaction has held row versions for %d minutes)", mins)
			do = fmt.Sprintf("The version store is held by the oldest snapshot transaction: session %d, open for %d minutes. Have it finish (or end the session) and TempDB empties.", t.SessionID, mins)
		}
		return mostly, do + tmp03NormSize, true
	case user >= internal:
		return "user objects (temp tables)", tmp03DoUser, true
	}
	return "internal objects (sorts, hashes, spools)", tmp03DoIntern, true
}

// tempdbFillerEvidence lists what fills tempdb, the top sessions and the oldest snapshot
// transaction, with a leading space; "" when none of it was read.
func (f StorageFacts) tempdbFillerEvidence() string {
	out := ""
	if f.UserObjectsMB+f.InternalObjectsMB+f.VersionStoreMB > 0 {
		out += fmt.Sprintf(" User objects %s MB, internal objects %s MB, version store %s MB.",
			n0int(f.UserObjectsMB), n0int(f.InternalObjectsMB), n0int(f.VersionStoreMB))
	}
	if len(f.TempdbSessions) > 0 {
		parts := []string{}
		for _, x := range f.TempdbSessions {
			parts = append(parts, fmt.Sprintf("session %d (%s, %s) %s MB", x.SessionID, orQ(x.Login), orQ(x.Program), n0(x.MB)))
		}
		out += " Top sessions: " + strings.Join(parts, "; ") + "."
	}
	if t := f.OldestSnapshot; t != nil {
		out += fmt.Sprintf(" Oldest snapshot transaction: session %d, %d minutes.", t.SessionID, t.ElapsedSeconds/60)
	}
	return out
}

// idx01: the 10 biggest tables, as one Info finding whose evidence lists them.
func (b *storageBuild) idx01() {
	f := b.f
	if f.TablesError != "" {
		b.skipped["IDX01"] = "Could not read the table sizes: " + f.TablesError
		return
	}
	finding := "No table in this database uses 1 MB or more."
	parts := []string{}
	for i, t := range f.Tables {
		if i == 0 {
			// The clustered index is counted as data; IndexCount counts the nonclustered ones.
			idx := "no nonclustered index"
			if t.IndexCount > 0 {
				idx = fmt.Sprintf("%d nonclustered index(es)", t.IndexCount)
			}
			finding = fmt.Sprintf("The biggest table, %s, has %s MB in total: %s MB data and %s MB in %s.",
				t.Name, n0(t.DataMB+t.IndexMB), n0(t.DataMB), n0(t.IndexMB), idx)
		}
		parts = append(parts, fmt.Sprintf("%s: %s MB total, %s MB data, %s MB index, %s rows",
			t.Name, n0(t.DataMB+t.IndexMB), n0(t.DataMB), n0(t.IndexMB), n0(float64(t.Rows))))
	}
	evidence := fmt.Sprintf("All %d tables: %s MB data, %s MB indexes.", f.TableCount, n0(f.TotalDataMB), n0(f.TotalIndexMB))
	if len(parts) > 0 {
		evidence = fmt.Sprintf("Top %d: %s. %s", len(parts), strings.Join(parts, "; "), evidence)
	}
	if f.QueryStoreMB != nil && f.QueryStoreMaxMB != nil {
		evidence += fmt.Sprintf(" Query Store: %s MB of the %s MB it may use.", n0(float64(*f.QueryStoreMB)), n0(float64(*f.QueryStoreMaxMB)))
	}
	b.add("IDX01", 5, "None", EffortNone, f.Database, finding, idx01Why, "", evidence)
}

// idx07: data files of 5 GB or more that are less than half used, one finding per file; a log of
// 2 GB or more that is bigger than all the data used.
func (b *storageBuild) idx07(filesNote string) {
	f := b.f
	switch {
	case filesNote != "":
		b.skipped["IDX07"] = filesNote
		return
	case f.SpaceUsedError != "":
		b.skipped["IDX07"] = "Could not read the space used inside the files: " + f.SpaceUsedError
		return
	}
	var dataUsed, logAlloc, logUsed int64
	for _, x := range f.DatabaseFiles() {
		used := int64(0)
		if x.UsedMB != nil {
			used = *x.UsedMB
		}
		switch {
		case x.isData():
			dataUsed += used
			if x.SizeMB >= 5120 && float64(used) < float64(x.SizeMB)*0.5 {
				b.add("IDX07", 4, "Offline", EffortModerate, x.LogicalName,
					fmt.Sprintf("Data file is %s GB but only %s GB is used.", n1(float64(x.SizeMB)/1024), n1(float64(used)/1024)),
					idx07WhyFile,
					fmt.Sprintf("Only if the space is needed: shrink once in a maintenance window, leaving about 20%% free, then rebuild the indexes (shrinking fragments them). DBCC SHRINKFILE (N'%s', %d);",
						strings.ReplaceAll(x.LogicalName, "'", "''"), int64(float64(used)*1.25)),
					fmt.Sprintf("Allocated %s MB, used %s MB.", n0(float64(x.SizeMB)), n0(float64(used))))
			}
		case x.isLog():
			logAlloc += x.SizeMB
			logUsed += used
		}
	}
	if logAlloc < 2048 || logAlloc <= dataUsed {
		return
	}
	var doThis string
	switch f.LogReuseWait {
	case "LOG_BACKUP":
		doThis = "The log is waiting for a log backup. Schedule log backups, or if point-in-time restore is not needed, ask the business about SIMPLE recovery. Then shrink the log once to a sensible size."
	case "ACTIVE_TRANSACTION":
		doThis = "A transaction is still open (see ACT02). Once it finishes, shrink the log once to a sensible size."
	default:
		doThis = "Find out why the log grew (often one large operation). Once resolved, shrink the log once to a sensible size in a quiet period."
	}
	b.add("IDX07", 3, "Online", EffortModerate, f.Database,
		fmt.Sprintf("The log file (%s MB) is bigger than all the data in the database (%s MB).", n0(float64(logAlloc)), n0(float64(dataUsed))),
		idx07WhyLog, doThis,
		fmt.Sprintf("Log used: %s MB. Waiting on: %s. Recovery model: %s.", n0(float64(logUsed)), f.LogReuseWait, f.RecoveryModel))
}

// idx10: user tables that look like log or history tables (by name) of a million rows or 500 MB,
// one finding per table: Low, Medium at 10 million rows or 5 GB. A table whose oldest row is known
// and younger than 12 months is being cleaned up: no finding. When the age is unknown (no date
// column, not indexed so not read, or not readable) the finding says nothing about removal. A
// finding on a table with a date column carries the archive fix (docs/phase5a-contract.md). A table
// kakapo archives does not fire: it is a note for information whose evidence says how it is
// archived.
func (b *storageBuild) idx10() {
	f := b.f
	if f.GrowingTablesError != "" {
		b.skipped["IDX10"] = "Could not read the tables that only grow: " + f.GrowingTablesError
		return
	}
	candidates := GrowingCandidates(f.GrowingTables, f.Archives, f.Database, f.ServerNow)
	for _, t := range f.GrowingTables {
		if !growingSized(t) {
			continue
		}
		if j, ok := findArchiveJob(f.Archives, f.Database, t.Schema, t.Name); ok {
			b.add("IDX10", 5, "Planned", EffortModerate, QuoteName(t.Schema)+"."+QuoteName(t.Name),
				fmt.Sprintf("%s.%s has %s rows (%s MB); kakapo archives its old rows nightly.", t.Schema, t.Name, n0(float64(t.Rows)), n0(t.MB)),
				idx10Why, "Nothing to do: the job "+j.Name+" keeps it in check; the Tables row on this card shows its last run.",
				fmt.Sprintf("Rows: %s. Size: %s MB. %s.", n0(float64(t.Rows)), n0(t.MB), upperFirst(j.ArchivedWords())))
			continue
		}
		if hasArchiveTable(f.ArchiveTables, t.Name) {
			b.add("IDX10", 5, "Planned", EffortModerate, QuoteName(t.Schema)+"."+QuoteName(t.Name),
				fmt.Sprintf("%s.%s has %s rows (%s MB). %s.%s holds older rows, but no archive job runs on this server.",
					t.Schema, t.Name, n0(float64(t.Rows)), n0(t.MB), archiveSchema, t.Name),
				idx10Why, "Nothing to do unless the table should keep being archived: archive it again to start a job.",
				fmt.Sprintf("Rows: %s. Size: %s MB. The table %s.%s exists.", n0(float64(t.Rows)), n0(t.MB), archiveSchema, t.Name))
			continue
		}
		if !growingIn(candidates, t) {
			continue // its oldest row is younger than growingMinMonths: something cleans it up
		}
		months := 0
		if t.Oldest != nil {
			months = wholeMonths(*t.Oldest, f.ServerNow)
		}
		rank := 4
		if t.Rows >= 10000000 || t.MB >= 5120 {
			rank = 3
		}
		finding := fmt.Sprintf("%s.%s has %s rows (%s MB) and looks like a %s table", t.Schema, t.Name, n0(float64(t.Rows)), n0(t.MB), growingKind(t.Name))
		evidence := fmt.Sprintf("Rows: %s. Size: %s MB.", n0(float64(t.Rows)), n0(t.MB))
		switch {
		case t.Oldest != nil:
			finding += fmt.Sprintf("; its oldest row is from %s (%d months ago), so nothing seems to remove old rows.",
				t.Oldest.Format("2006-01-02"), months)
			evidence += fmt.Sprintf(" Oldest %s: %s.", t.DateCol, sqlTime(*t.Oldest))
			if !t.DateIndexed && t.KeyCol != "" {
				evidence = strings.TrimSuffix(evidence, ".") + fmt.Sprintf(" (the row with the lowest %s).", t.KeyCol)
			}
		case t.DateCol == "":
			finding += "."
			evidence += " It has no date column, so how far back its rows go is unknown."
		case !t.DateIndexed && t.KeyCol == "":
			finding += "."
			evidence += fmt.Sprintf(" Its date column %s is not indexed, so how far back its rows go was not read.", t.DateCol)
		default:
			finding += "."
			evidence += fmt.Sprintf(" Its date column %s could not be read, so how far back its rows go is unknown.", t.DateCol)
		}
		b.add("IDX10", rank, "Planned", EffortModerate, QuoteName(t.Schema)+"."+QuoteName(t.Name), finding, idx10Why, idx10Do, evidence)
		if t.DateCol != "" {
			b.withFix(FixArchiveTable)
		}
	}
}

// growingSized says whether a table that only grows is named like one (GrowingTableWords) and holds
// a million rows or 500 MB: IDX10's bar.
func growingSized(t GrowingTable) bool {
	if strings.EqualFold(t.Schema, archiveSchema) {
		return false // kakapo's own archive tables
	}
	return LooksLikeLogTable(t.Name) && (t.Rows >= 1000000 || t.MB >= 500)
}

// GrowingCandidates are the tables IDX10 fires on as "nothing removes old rows": named like a log,
// history or queue table, a million rows or 500 MB, the oldest row growingMinMonths old or more (or
// its age unknown), and not archived by kakapo (findArchiveJob). They are one rule for IDX10 and for
// QRY01's and the query page's "archive before an index". now is the server's clock, as Oldest is.
func GrowingCandidates(tables []GrowingTable, archives []ArchiveJob, database string, now time.Time) []GrowingTable {
	out := []GrowingTable{}
	for _, t := range tables {
		if !growingSized(t) {
			continue
		}
		if _, ok := findArchiveJob(archives, database, t.Schema, t.Name); ok {
			continue
		}
		if t.Oldest != nil && wholeMonths(*t.Oldest, now) < growingMinMonths {
			continue
		}
		out = append(out, t)
	}
	return out
}

// archiveSchema is the schema kakapo archives into by default.
const archiveSchema = "archive"

// hasArchiveTable says whether the archive schema holds a table of this name.
func hasArchiveTable(names []string, name string) bool {
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

func growingIn(tables []GrowingTable, t GrowingTable) bool {
	for _, x := range tables {
		if strings.EqualFold(x.Schema, t.Schema) && strings.EqualFold(x.Name, t.Name) {
			return true
		}
	}
	return false
}

// wholeMonths counts the whole months from a to now: 2025-11-03 to 2026-11-03 is 12, to
// 2026-11-02 is 11.
func wholeMonths(a, now time.Time) int {
	n := monthsBetween(a, now)
	if now.Day() < a.Day() {
		n--
	}
	return n
}

// idx13: identity columns at half their range or more, one finding per column.
func (b *storageBuild) idx13() {
	f := b.f
	if f.IdentityError != "" {
		b.skipped["IDX13"] = "Could not read the identity columns: " + f.IdentityError
		return
	}
	for _, c := range f.Identities {
		if c.PctUsed < 50 {
			continue
		}
		rank := 4
		switch {
		case c.PctUsed >= 99:
			rank = 1
		case c.PctUsed >= 90:
			rank = 2
		case c.PctUsed >= 70:
			rank = 3
		}
		doThis := idx13DoLow
		if c.PctUsed >= 90 {
			doThis = idx13DoHigh
		}
		b.add("IDX13", rank, "Planned", EffortProject, c.Table+"."+c.Column,
			fmt.Sprintf("Identity column has used %s%% of its %s range (last value %s of %s).",
				n1(c.PctUsed), c.Type, n0int(c.LastValue), n0int(c.MaxValue())),
			idx13Why, doThis,
			fmt.Sprintf("Type %s, seed %d, increment %d.", c.Type, c.Seed, c.Increment))
	}
}

// dataVolume is the volume with the least free space among those holding this database's data
// files.
func (f StorageFacts) dataVolume() (volume, bool) {
	data := []StorageFile{}
	for _, x := range f.DatabaseFiles() {
		if x.isData() {
			data = append(data, x)
		}
	}
	vs := volumesOf(data)
	if len(vs) == 0 {
		return volume{}, false
	}
	best := vs[0]
	for _, v := range vs[1:] {
		if v.FreeMB < best.FreeMB {
			best = v
		}
	}
	return best, true
}

// sizeMB is the database's size for the card: its tables' data and indexes, or, when those could
// not be read, its data files.
func (b *storageBuild) sizeMB() float64 {
	if b.f.TablesError == "" {
		return b.f.TotalDataMB + b.f.TotalIndexMB
	}
	data, _ := b.dataLogMB()
	return float64(data)
}

// greenVerdict: "{db} is {size} on {volume}, which has {free} free."
func (b *storageBuild) greenVerdict() string {
	v, ok := b.f.dataVolume()
	if !ok {
		return fmt.Sprintf("%s is %s GB.", b.f.Database, n1(b.sizeMB()/1024))
	}
	return fmt.Sprintf("%s is %s GB on %s, which has %s GB free.", b.f.Database, n1(b.sizeMB()/1024), v.Name, n0int(v.FreeMB/1024))
}

// worstTone is the tone of the worst of these findings: Critical and High worse, Medium bad.
func worstTone(fs []Finding) string {
	t := ToneNormal
	for _, x := range fs {
		switch x.Severity {
		case SeverityCritical, SeverityHigh:
			return ToneWorse
		case SeverityMedium:
			t = ToneBad
		}
	}
	return t
}

// facts are the four lines on the card.
func (b *storageBuild) facts() []Fact {
	f := b.f

	drive := Fact{Label: "Tightest drive", Value: "could not be read", Tone: ToneNormal}
	if tight, ok := f.tightestVolume(); ok {
		drive.Value = fmt.Sprintf("%s %d%% free (%s GB)", tight.Name, int(tight.freePct()), n0int(tight.FreeMB/1024))
		tones := []Finding{}
		for _, x := range b.all("STO01") {
			if x.Object == tight.Name {
				tones = append(tones, x)
			}
		}
		if days, x, ok := b.sto12Days(); ok && x.Object == tight.Name {
			drive.Value += fmt.Sprintf(", full in ~%d days", days)
			tones = append(tones, x)
		}
		drive.Tone = worstTone(tones)
	}

	size := Fact{Label: "Database size", Value: "could not be read", Tone: ToneNormal}
	if f.FilesError == "" {
		var alloc, used int64
		allUsed, seen := f.SpaceUsedError == "", false
		for _, x := range f.DatabaseFiles() {
			if !x.isData() {
				continue
			}
			seen = true
			alloc += x.SizeMB
			if x.UsedMB == nil {
				allUsed = false
			} else {
				used += *x.UsedMB
			}
		}
		if seen {
			size.Value = n1(float64(alloc)/1024) + " GB allocated"
			if allUsed {
				size.Value += ", " + n1(float64(used)/1024) + " GB used"
			}
		}
	}

	log := Fact{Label: "Log", Value: "could not be read", Tone: ToneNormal}
	if f.LogSpaceError == "" && f.LogUsedPct != nil && f.LogSizeMB != nil {
		pct := *f.LogUsedPct
		log.Value = fmt.Sprintf("%s GB, %d%% used", n1(float64(*f.LogSizeMB)/1024), int(pct))
		if f.LogReuseWait != "" && f.LogReuseWait != "NOTHING" {
			log.Value += ", waiting on " + LogReuseReason(f.LogReuseWait)
		}
		switch {
		case pct >= 90:
			log.Tone = ToneWorse
		case pct >= 80:
			log.Tone = ToneBad
		}
	}

	return []Fact{drive, size, log, b.fourthFact()}
}

// fourthFact: the identity range when IDX13 fires at 70% or more, else the tables that only grow
// when IDX10 fires, else how full tempdb is.
func (b *storageBuild) fourthFact() Fact {
	f := b.f
	var top *IdentityColumn
	if _, skipped := b.skipped["IDX13"]; !skipped {
		for i, c := range f.Identities {
			if c.PctUsed >= 70 && (top == nil || c.PctUsed > top.PctUsed) {
				top = &f.Identities[i]
			}
		}
	}
	if top != nil {
		t := ToneBad
		if top.PctUsed >= 90 {
			t = ToneWorse
		}
		return Fact{Label: "Identity range", Value: fmt.Sprintf("%d%% used (%s)", int(top.PctUsed), top.Table), Tone: t}
	}
	grows := []Finding{}
	for _, x := range b.all("IDX10") {
		if x.Severity.Actionable() {
			grows = append(grows, x) // not a table kakapo archives
		}
	}
	if len(grows) > 0 {
		var biggest *GrowingTable
		for i, t := range f.GrowingTables {
			for _, x := range grows {
				if x.Object == QuoteName(t.Schema)+"."+QuoteName(t.Name) && (biggest == nil || t.MB > biggest.MB) {
					biggest = &f.GrowingTables[i]
				}
			}
		}
		value := strconv.Itoa(len(grows))
		if biggest != nil {
			value += fmt.Sprintf(", biggest %s.%s %s MB", biggest.Schema, biggest.Name, n0(biggest.MB))
		}
		return Fact{Label: "Tables that only grow", Value: value, Tone: ToneBad}
	}
	tempdb := Fact{Label: "TempDB", Value: "could not be read", Tone: ToneNormal}
	if u, ok := f.tempdbUse(); ok {
		tempdb.Value = fmt.Sprintf("%d%% full of %s GB", u.UsedPct, n1(float64(u.AllocMB)/1024))
		if len(b.all("TMP03")) > 0 {
			tempdb.Tone = ToneBad
		}
	}
	return tempdb
}

// n0 writes a number as FORMAT(x, 'N0') does in English: rounded half away from zero, with
// thousands separators.
func n0(x float64) string { return groupThousands(fmt.Sprintf("%.0f", roundHalfAway(x, 0))) }

// n1 is FORMAT(x, 'N1').
func n1(x float64) string { return groupThousands(fmt.Sprintf("%.1f", roundHalfAway(x, 1))) }

// n0int is FORMAT(x, 'N0') of a whole number, exact for every int64.
func n0int(x int64) string { return groupThousands(fmt.Sprint(x)) }

func roundHalfAway(x float64, decimals int) float64 {
	p := math.Pow(10, float64(decimals))
	return math.Round(x*p) / p
}

func groupThousands(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i:]
	}
	var out strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(r)
	}
	return sign + out.String() + frac
}

// IDX06's size bar: a heap of 100 MB of data or a million rows, the triage's own (PerformanceTriage.sql
// 705). sp_BlitzIndex check 44 fires only on heaps over 10,240 MB.
const (
	idx06MinMB   = 100
	idx06MinRows = 1000000
)

// idx06: large tables without a clustered index (heaps), one finding per table, the biggest first.
// A heap with no nonclustered index either says that every query reads all of it. Sources:
// PerformanceTriage.sql IDX06; sp_BlitzIndex check 44; Microsoft Learn, "Heaps (tables without
// clustered indexes)" (forwarding pointers, space reuse, when a heap is the wrong choice).
func (b *storageBuild) idx06() {
	f := b.f
	if f.TablesError != "" {
		b.skipped["IDX06"] = "Could not read the table sizes: " + f.TablesError
		return
	}
	for _, t := range f.Heaps {
		if t.DataMB < idx06MinMB && t.Rows < idx06MinRows {
			continue
		}
		size := fmt.Sprintf("%s, %s rows", mbText(int64(math.Round(t.DataMB))), n0int(t.Rows))
		finding := "Table has no clustered index (a heap): " + size + "."
		why := idx06Why
		indexes := "no nonclustered index"
		if t.IndexCount > 0 {
			indexes = plural(int64(t.IndexCount), "nonclustered index", "nonclustered indexes") + ", " + mbText(int64(math.Round(t.IndexMB)))
		} else {
			finding = "Table has no index at all (a heap with no nonclustered index): " + size + "."
			why += idx06WhyNone
		}
		b.add("IDX06", 3, "Offline", EffortModerate, t.Name, finding, why, idx06Do,
			fmt.Sprintf("Data %s MB, %s rows, %s.", n0(t.DataMB), n0int(t.Rows), indexes))
	}
}
