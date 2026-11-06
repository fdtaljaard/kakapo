package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// SetupFacts is what the Setup card reads about one database and the instance it runs on
// (docs/phase3c-contract.md, "Reads"). Every time is in the server's own clock, as GETDATE()
// returns it, carried in a time.Time with the UTC location. Each area that could not be read
// carries its reason; the checks that need it are skipped, the rest still run.
type SetupFacts struct {
	// The environment block of the scan.
	ServerName     string // @@SERVERNAME
	ProductVersion string // 16.0.4255.3
	ProductLevel   string // RTM, SP3
	UpdateLevel    string // CU25; "" when SQL Server reports none
	Edition        string // Developer Edition (64-bit)
	EngineEdition  int    // 2 Standard, 3 Enterprise/Developer, 4 Express, 8 Managed Instance
	Collation      string // the server collation

	// sys.dm_os_sys_info and sys.dm_os_schedulers (VIEW SERVER STATE); SysInfoError when not read.
	CPUCount     int
	PhysicalMB   int64
	StartTime    time.Time // sqlserver_start_time, server clock
	CoresPerNode int       // the most visible online schedulers on one NUMA node
	SysInfoError string
	// INS17, in the same area: the schedulers SQL Server can and cannot use, the runnable tasks on
	// the online ones right now, affinity_type_desc (AUTO, MANUAL), whether it runs in a virtual
	// machine, and the processor layout Windows shows it (0 before 2016 SP2, which has no columns).
	OnlineSchedulers  int // VISIBLE ONLINE
	OfflineSchedulers int // VISIBLE OFFLINE
	RunnableNow       int
	AffinityType      string
	VirtualMachine    bool // virtual_machine_type_desc is not NONE (also on a host with Hyper-V on)
	Sockets           int  // socket_count
	CoresPerSocket    int  // cores_per_socket

	// sys.configurations value_in_use; ConfigError when not read.
	MaxServerMemoryMB int64
	MaxDop            int
	CostThreshold     int
	OptimizeAdHoc     int
	PriorityBoost     int
	BackupCompression int
	FillFactor        int
	ClrEnabled        int
	ClrStrict         *int // nil before SQL Server 2017
	// INS16: min server memory, lightweight pooling and the affinity masks (value_in_use).
	MinServerMemoryMB  int64
	LightweightPooling int
	AffinityMask       int64
	Affinity64Mask     int64
	AffinityIOMask     int64
	Affinity64IOMask   int64
	// INS14: settings whose configured value differs from the one in use (value <> value_in_use),
	// read in the same area; empty when none.
	PendingConfig []PendingConfig
	ConfigError   string

	// INS15: the global trace flags (DBCC TRACESTATUS(-1)); TraceFlagError when they could not be
	// read (the DBCC needs sysadmin on older versions).
	TraceFlags     []int
	TraceFlagError string

	// INS06: the plan cache in MB, decimal(18, 1) as the scan computes it.
	PlanCacheMB    float64
	SingleUseMB    float64
	PlanCacheError string

	// INS07 and INS08: nil when not visible on this version (before 2016 SP1) or not readable.
	IFI         *string // instant_file_initialization_enabled: Y or N
	MemoryModel *string // sql_memory_model_desc: CONVENTIONAL, LOCK_PAGES, LARGE_PAGES

	// INS12: sys.dm_os_sys_memory and the resource monitor ring buffer.
	MemoryState string // system_memory_state_desc
	AvailableMB int64
	Low24       int
	Low7        int
	LowLast     *time.Time
	MemoryError string

	// INS13: ActivePowerScheme from the registry; nil when it could not be read, PowerError says why
	// when the read failed with an error.
	PowerScheme *string
	PowerError  string

	// The database, from sys.databases.
	Database          string
	State             string // state_desc
	UserAccess        string // user_access_desc: MULTI_USER, SINGLE_USER, RESTRICTED_USER
	AutoClose         bool
	AutoShrink        bool
	PageVerify        string // page_verify_option_desc
	AutoCreateStats   bool
	AutoUpdateStats   bool
	CompatLevel       int
	OwnerMissing      bool // SUSER_SNAME(owner_sid) IS NULL
	TargetRecoverySec int  // target_recovery_time_in_seconds
	Trustworthy       bool
	DbCollation       string

	// The database as listed, with its Query Store state when it was read (DB.QueryStore nil and
	// DB.Error saying why otherwise). The service fills it in.
	DB DatabaseStatus

	// CPY01: the latest restore of this database recorded in msdb; RestoredAt nil when none.
	RestoredAt     *time.Time
	SourceServer   string
	SourceDb       string
	SourceBackupAt *time.Time
	RestoreError   string // msdb could not be read; only the file names are used
	DataFileName   string // the logical name of the first data file

	// Inside the database.
	LegacyCE      *int // LEGACY_CARDINALITY_ESTIMATION; nil before SQL Server 2016
	LegacyCEError string
	// DBC14: every row of sys.database_scoped_configurations; nil before SQL Server 2016.
	ScopedConfigs      []ScopedConfig
	ScopedConfigsError string
	Orphans            []OrphanUser
	OrphansError       string
	Assemblies         int // user-defined assemblies
	SafeAssemblies     int // of those, SAFE_ACCESS
	Trusted            int // rows in sys.trusted_assemblies (2017+); 0 before
	ClrLoaded          bool
	ClrError           string
	// CPY02: a database this database's code reads from with the same collation, and how many text
	// columns of user tables have yet another collation.
	SameCollationDb  string
	OtherCollationCt int
	CollationError   string

	ServerNow        time.Time // GETDATE()
	UTCOffsetMinutes int

	Error string // nothing could be read at all
}

// OrphanUser is a SQL user of the database without a matching login (CPY04).
type OrphanUser struct {
	Name          string
	DefaultSchema string // "" when none
	LoginExists   bool   // a SQL login with the same name exists
}

// PendingConfig is a server setting changed with sp_configure but not yet activated by
// RECONFIGURE (INS14).
type PendingConfig struct {
	Name       string
	Value      int64 // the configured value, which the next RECONFIGURE activates
	ValueInUse int64
}

// ScopedConfig is one row of sys.database_scoped_configurations (DBC14), its value as text.
type ScopedConfig struct {
	Name  string
	Value string // "" when NULL
}

// AsOf is ServerNow in UTC.
func (f SetupFacts) AsOf() time.Time {
	t := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// Major is the engine major version: 16 for SQL Server 2022.
func (f SetupFacts) Major() int { return EngineMajor(f.ProductVersion) }

// IsExpress says whether this is Express (EngineEdition 4).
func (f SetupFacts) IsExpress() bool { return f.EngineEdition == 4 }

// VersionName is the scan's @VersionName: "2022", or "v18" for a version it does not know.
func VersionName(major int) string {
	switch major {
	case 11:
		return "2012"
	case 12:
		return "2014"
	case 13:
		return "2016"
	case 14:
		return "2017"
	case 15:
		return "2019"
	case 16:
		return "2022"
	case 17:
		return "2025"
	}
	return fmt.Sprintf("v%d", major)
}

// BuildInfo is one row of the INS01 build table: the latest known update of a major version and
// its support dates.
type BuildInfo struct {
	Major         int
	LatestMinor   int
	LatestName    string
	LatestDate    time.Time
	MainstreamEnd time.Time
	ExtendedEnd   time.Time
}

// BuildTableRefreshed is when the build table was last refreshed by hand from the First
// Responder Kit's SqlServerVersions.sql (MIT licence), as in 01-QuickScan.sql.
const BuildTableRefreshed = "2026-11-02"

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// BuildTable is the scan's INS01 build table (01-QuickScan.sql), refreshed BuildTableRefreshed.
var BuildTable = []BuildInfo{
	{17, 4055, "CU6 (June 2026)", day("2026-06-17"), day("2031-01-06"), day("2036-01-06")},
	{16, 4255, "CU25 (May 2026)", day("2026-05-20"), day("2028-01-11"), day("2033-01-11")},
	{15, 4470, "CU32 GDR (May 2026)", day("2026-05-12"), day("2025-01-07"), day("2030-01-08")},
	{14, 3530, "CU31 GDR (May 2026)", day("2026-05-12"), day("2022-10-11"), day("2027-10-12")},
	{13, 7055, "SP3 GDR (July 2025)", day("2025-07-08"), day("2021-07-13"), day("2026-07-14")},
	{12, 6449, "SP3 CU4 GDR (Oct 2023)", day("2023-10-12"), day("2019-07-09"), day("2024-07-09")},
	{11, 7512, "SP4 GDR (Feb 2023)", day("2023-02-16"), day("2017-07-11"), day("2022-07-12")},
}

func buildFor(major int) (BuildInfo, bool) {
	for _, b := range BuildTable {
		if b.Major == major {
			return b, true
		}
	}
	return BuildInfo{}, false
}

// versionPart is PARSENAME(version, n): 1 is the last dotted part, 4 the first; -1 when missing.
func versionPart(version string, n int) int {
	parts := strings.Split(strings.TrimSpace(version), ".")
	i := len(parts) - n
	if i < 0 || i >= len(parts) {
		return -1
	}
	var v int
	if _, err := fmt.Sscanf(parts[i], "%d", &v); err != nil {
		return -1
	}
	return v
}

// monthsBetween counts month boundaries, as DATEDIFF(MONTH, a, b) does.
func monthsBetween(a, b time.Time) int {
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
}

// Support states of the instance's build.
const (
	supportOut     = "out"
	supportBehind  = "behind"
	supportCurrent = "current"
	supportUnknown = "unknown" // newer than the build table
)

// support says where the build stands against the build table, as INS01 decides it.
func (f SetupFacts) support() (string, BuildInfo) {
	major := f.Major()
	b, known := buildFor(major)
	switch {
	case (known && b.ExtendedEnd.Before(f.ServerNow)) || (!known && major <= 13):
		return supportOut, b
	case known && versionPart(f.ProductVersion, 2) < b.LatestMinor:
		return supportBehind, b
	case known:
		return supportCurrent, b
	}
	return supportUnknown, b
}

// Summary is the scan's @Summary line. Without VIEW SERVER STATE it stops after the build.
func (f SetupFacts) Summary() string {
	level := f.ProductLevel
	if f.UpdateLevel != "" {
		level += " " + f.UpdateLevel
	}
	s := fmt.Sprintf("SQL Server %s %s, build %s (%s).", VersionName(f.Major()), f.Edition, f.ProductVersion, level)
	if f.SysInfoError == "" {
		s += fmt.Sprintf(" %d logical CPUs, %d GB RAM, running for %d days.", f.CPUCount, f.PhysicalMB/1024,
			hoursBetween(f.StartTime, f.ServerNow)/24)
	}
	return s
}

// SuggestedMaxMemoryMB is the scan's and Apply-DoNow's suggestion: RAM minus RAM/4 up to 16 GB,
// else minus the larger of RAM/10 and 4 GB.
func SuggestedMaxMemoryMB(physicalMB int64) int64 {
	reserve := int64(4096)
	switch {
	case physicalMB <= 16384:
		reserve = physicalMB / 4
	case physicalMB/10 > 4096:
		reserve = physicalMB / 10
	}
	return physicalMB - reserve
}

// SuggestedMaxDop is min(cores per NUMA node, 8), as the scan suggests it.
func (f SetupFacts) SuggestedMaxDop() int {
	return min(f.CoresPerNode, 8)
}

// Power plans the scan knows.
const (
	powerHigh     = "8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c"
	powerUltimate = "e9a42b02-d5df-448d-aa00-03f14749eb61"
	powerBalanced = "381b4222-f694-41f0-9685-ff5bb260df2e"
	powerSaver    = "a1841308-3541-4fab-bc81-f71556f20b4a"
)

// Notes for checks that were skipped, from the scan where it has one.
const (
	ins07Skipped      = "Not visible on this version or platform (needs SQL Server 2016 SP1 or later)."
	ins08Skipped      = "Not visible on this version (needs SQL Server 2016 SP1 or later)."
	ins13Skipped      = "Could not read the power plan (needs sysadmin, on Windows)."
	dbc09Skipped      = "Indirect checkpoints are checked on SQL Server 2016 or later."
	dbc09NotShown     = "SQL Server did not show the target recovery time of this database."
	cpy02NotShown     = "SQL Server did not show the collation of this database."
	dbc10Old          = "Query Store needs SQL Server 2016 or later."
	cfg01Skipped      = "Database-scoped configurations need SQL Server 2016 or later."
	cpy01MsdbNote     = "Restore history in msdb could not be read; only file names were used."
	sysInfoNotRead    = "Could not read the server's CPUs and memory (sys.dm_os_sys_info): "
	configNotRead     = "Could not read the server configuration (sys.configurations): "
	planCacheNotRd    = "Could not read the plan cache (sys.dm_exec_cached_plans): "
	memoryNotRead     = "The memory pressure check could not run: "
	powerNotRead      = "Could not read the power plan: "
	insideNotRead     = "Could not read inside the database: "
	legacyCENotRead   = "Could not read the database-scoped configurations: "
	traceFlagsNotRead = "Could not read the global trace flags (DBCC TRACESTATUS needs sysadmin on older versions): "
	dbc14Skipped      = "Database scoped configurations need SQL Server 2016 or later."
)

// BuildSetupCard answers "Is it set up the way it should be?" for one database from what was
// read: the card, its findings ranked (with the fix the app can apply where there is one), and
// what became of each check behind it, in catalogue order.
func BuildSetupCard(f SetupFacts, def CardDef) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	b := &setupBuild{f: f, findings: []Finding{}, skipped: map[string]string{}}

	switch {
	case f.Error != "":
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", f.Error
		b.skipAll(def.ID, f.Error)
		return c, b.findings, b.checks(def.ID)
	case f.State != "ONLINE":
		note := fmt.Sprintf("%s is %s, not online, so its setup was not checked.", f.Database, f.State)
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID)
	}

	b.instanceChecks()
	b.databaseChecks()
	RankFindings(b.findings)
	checks := b.checks(def.ID)

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
		c.Verdict = f.Summary()
	}
	c.ToDo, c.ForClientIT, c.Fixable = actionable, clientIT, fixable
	asOf := f.AsOf()
	c.AsOf = &asOf
	c.Facts = b.facts()
	return c, b.findings, checks
}

type setupBuild struct {
	f        SetupFacts
	findings []Finding
	skipped  map[string]string
}

func (b *setupBuild) add(id string, rank int, changeType, effort, object, finding, why, doThis, evidence, fixID string) {
	chk, _ := CheckByID(id)
	b.findings = append(b.findings, Finding{
		CheckID: id, Title: chk.Title, Severity: SeverityFromRank(min(rank, 5)), Owner: chk.Owner,
		When: WhenFromChangeType(changeType), Effort: effort, Object: object,
		Finding: finding, Why: why, DoThis: doThis, ThenCheck: chk.ThenCheck, Evidence: evidence, FixID: fixID,
	})
}

// ins adds an instance finding: its object is @@SERVERNAME.
func (b *setupBuild) ins(id string, rank int, changeType, effort, finding, why, doThis, evidence, fixID string) {
	b.add(id, rank, changeType, effort, b.f.ServerName, finding, why, doThis, evidence, fixID)
}

// dbc adds a database finding: its object is the database. A database fix is offered only when
// the database can be changed from here.
func (b *setupBuild) dbc(id string, rank int, changeType, effort, finding, why, doThis, evidence, fixID string) {
	if fixID != "" && !b.f.DB.CanFix() {
		fixID = ""
	}
	b.add(id, rank, changeType, effort, b.f.Database, finding, why, doThis, evidence, fixID)
}

func (b *setupBuild) all(id string) []Finding {
	out := []Finding{}
	for _, x := range b.findings {
		if x.CheckID == id {
			out = append(out, x)
		}
	}
	return out
}

func (b *setupBuild) actionable(ids ...string) bool {
	for _, id := range ids {
		for _, x := range b.all(id) {
			if x.Severity.Actionable() {
				return true
			}
		}
	}
	return false
}

func (b *setupBuild) skip(note string, ids ...string) {
	for _, id := range ids {
		if _, ok := b.skipped[id]; !ok {
			b.skipped[id] = note
		}
	}
}

func (b *setupBuild) skipAll(card CardID, note string) {
	for _, c := range ChecksFor(card) {
		b.skipped[c.ID] = note
	}
}

// checks says what became of each check: skipped with its note, a finding when it found
// something to act on, passed otherwise (an Info finding is information, not a problem).
func (b *setupBuild) checks(card CardID) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(card) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed}
		if note, ok := b.skipped[c.ID]; ok {
			st.Status, st.Note = CheckSkipped, note
		} else if b.actionable(c.ID) {
			st.Status = CheckFinding
		}
		out = append(out, st)
	}
	return out
}

// Texts from 01-QuickScan.sql section 1 and section 8, verbatim.
const (
	ins01OutWhy    = "Unsupported versions get no security or bug fixes, and newer versions bring built-in performance improvements."
	ins01OutDo     = "Plan an upgrade to a supported version (SQL Server 2022 or later), and confirm the application vendor supports it."
	ins01BehindWhy = "Cumulative updates fix bugs, including performance problems in the optimizer and Query Store. They are cumulative: only the latest one needs installing."
	ins01BehindDo  = "Install the latest cumulative update in a maintenance window (it restarts SQL Server), on a test server first. Download from https://aka.ms/sqlserverupdates."
	ins01NewerDo   = "This version is newer than the build table in this script: refresh the table from the First Responder Kit."
	ins02Why       = "SQL Server will take almost all the RAM on the server and starve Windows and other services. This causes paging and sudden slowdowns."
	ins03LowWhy    = "Windows needs memory of its own. When it runs short it pages SQL Server memory to disk, which makes everything slow."
	ins03HalfWhy   = "Memory is the cheapest way to avoid disk reads. If nothing else runs on this server, the unused RAM is wasted."
	ins04ZeroWhy   = "A single large query can occupy every CPU and make other users wait. Microsoft recommends capping this on servers with more than 8 CPUs."
	ins04AboveWhy  = "Very wide parallel queries rarely run faster and take CPUs away from other users."
	ins04OneWhy    = "Fine if the application vendor requires it. Otherwise large reports, index rebuilds and CHECKDB run slower than they could."
	ins05Why       = "Even small, cheap queries go parallel, which wastes CPU and shows up as CXPACKET waits. The default dates from the 1990s."
	ins05Do        = "Start at 50 and adjust later: EXEC sp_configure 'cost threshold for parallelism', 50; RECONFIGURE;"
	ins06Why       = "Queries that run only once still store a full plan in memory. That memory could be caching data instead."
	ins06Do        = "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'optimize for ad hoc workloads', 1; RECONFIGURE;"
	ins07Why       = "Every data file growth and every restore has to write zeros over the new space first. Growths pause activity and restores take much longer."
	ins07Do        = "Give the SQL Server service account the Windows right \"Perform volume maintenance tasks\" (Local Security Policy > User Rights Assignment), then restart the SQL Server service in a maintenance window."
	ins08Why       = "Without it, Windows can push SQL Server memory out to the page file under pressure, causing sudden severe slowdowns."
	ins08Do        = "Only after max server memory is set correctly (INS02/INS03): give the service account the Windows right \"Lock pages in memory\", then restart SQL Server in a maintenance window."
	ins09Why       = "It raises SQL Server above Windows itself, which can starve the operating system and cause instability. Microsoft deprecated it."
	ins09Do        = "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'priority boost', 0; RECONFIGURE;  Then restart SQL Server in a maintenance window."
	ins10Why       = "Compressed backups are usually 50-80% smaller and finish faster, which means less disk I/O during backup windows. Ignore if a third-party tool takes the backups."
	ins10Do        = "EXEC sp_configure 'backup compression default', 1; RECONFIGURE;"
	ins11Why       = "Every new or rebuilt index leaves empty space in each page, so indexes are bigger and reads are slower across all databases."
	ins11Do        = "Set it back to 0 and use a lower fill factor only on specific indexes with proven page-split problems: EXEC sp_configure 'fill factor (%)', 0; RECONFIGURE;  (takes effect after a restart)."
	ins12Why       = "When Windows runs short, SQL Server gives memory back and reads from disk instead, or Windows pages SQL Server out. Either way everything slows down suddenly, and nothing inside SQL Server shows why."
	ins12Do        = "Lower max server memory (INS02/INS03) if SQL Server takes too much, or find what else on this server uses the memory (another instance, SSRS, antivirus, a web application). On a VM, ask the host administrator whether memory is over-committed or ballooned."
	ins13Why       = "Balanced lets Windows slow the CPUs down when it thinks the server is idle, and it is slow to speed them up again. CPU-bound queries run measurably slower."
	ins13Do        = "Set the plan to High performance: Control Panel > Power Options, or from an administrator prompt: powercfg /setactive 8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c. On a VM, also check the host's power settings."
)

func (b *setupBuild) instanceChecks() {
	f := b.f
	b.ins01()
	if f.ConfigError != "" {
		b.skip(configNotRead+f.ConfigError, "INS02", "INS03", "INS04", "INS05", "INS06", "INS09", "INS10", "INS11")
	}
	if f.SysInfoError != "" {
		b.skip(sysInfoNotRead+f.SysInfoError, "INS02", "INS03", "INS04", "INS05", "INS08", "INS17")
	}
	if f.ConfigError == "" && f.SysInfoError == "" {
		b.ins02and03()
		b.ins04()
		b.ins05()
	}
	if f.ConfigError == "" {
		if f.PlanCacheError != "" {
			b.skip(planCacheNotRd+f.PlanCacheError, "INS06")
		} else {
			b.ins06()
		}
		b.ins09()
		b.ins10()
		b.ins11()
		b.ins14()
		b.ins16()
	} else {
		b.skip(configNotRead+f.ConfigError, "INS14", "INS16")
	}
	if f.TraceFlagError != "" {
		b.skip(traceFlagsNotRead+f.TraceFlagError, "INS15")
	} else {
		b.ins15()
	}
	b.ins07()
	if f.SysInfoError == "" {
		b.ins08()
		b.ins17()
	}
	if f.MemoryError != "" {
		b.skip(memoryNotRead+f.MemoryError, "INS12")
	} else {
		b.ins12()
	}
	b.ins13()
}

// ins01: out of support, behind the latest update, or the instance summary.
func (b *setupBuild) ins01() {
	f := b.f
	name := VersionName(f.Major())
	state, bi := f.support()
	switch state {
	case supportOut:
		finding := "SQL Server " + name + " is out of Microsoft support"
		if !bi.ExtendedEnd.IsZero() {
			finding += " (extended support ended " + bi.ExtendedEnd.Format("2006-01-02") + ")"
		}
		b.ins("INS01", 2, "Planned", EffortProject, finding+".", ins01OutWhy, ins01OutDo, f.Summary(), "")
	case supportBehind:
		rank := 5
		mainstreamEnded := bi.MainstreamEnd.Before(f.ServerNow)
		if monthsBetween(bi.LatestDate, f.ServerNow) >= 12 || mainstreamEnded {
			rank = 4
		}
		finding := fmt.Sprintf("Build %s is behind the latest update for SQL Server %s: %s (build %d.0.%d).",
			f.ProductVersion, name, bi.LatestName, bi.Major, bi.LatestMinor)
		if mainstreamEnded {
			finding += " Mainstream support ended " + bi.MainstreamEnd.Format("2006-01-02") +
				"; only security fixes until " + bi.ExtendedEnd.Format("2006-01-02") + "."
		}
		evidence := f.Summary() + " Latest known update: " + bi.LatestName + ", released " + bi.LatestDate.Format("2006-01-02") +
			" (build table refreshed " + BuildTableRefreshed + ")."
		b.ins("INS01", rank, "Offline", EffortModerate, finding, ins01BehindWhy, ins01BehindDo, evidence, "")
	default:
		do := ins01NewerDo
		if state == supportCurrent {
			do = "Up to date with the latest known cumulative update (" + bi.LatestName + ")."
		}
		b.ins("INS01", 5, "None", EffortNone, "Instance summary.", "", do, f.Summary(), "")
	}
}

const unlimitedMemory = 2147483647

// ins02and03: max server memory not set, or leaving too little or using too little.
func (b *setupBuild) ins02and03() {
	f := b.f
	suggested := SuggestedMaxMemoryMB(f.PhysicalMB)
	fix := "max-server-memory"
	if suggested < 2048 {
		fix = "" // Apply-DoNow skips a value this small; so does the app
	}
	if f.MaxServerMemoryMB >= unlimitedMemory {
		rank := 2
		if f.IsExpress() {
			rank = 4
		}
		b.ins("INS02", rank, "Online", EffortQuickWin,
			"Max server memory is not set (still on the default of unlimited).", ins02Why,
			fmt.Sprintf("Leave room for the operating system: EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', %d; RECONFIGURE;  Use a lower value if other applications run on this server.", suggested),
			fmt.Sprintf("Physical memory: %d MB. Max server memory: unlimited.", f.PhysicalMB), fix)
		return
	}
	evidence := fmt.Sprintf("Physical memory: %d MB. Max server memory: %d MB.", f.PhysicalMB, f.MaxServerMemoryMB)
	if f.PhysicalMB-f.MaxServerMemoryMB < 2048 || float64(f.MaxServerMemoryMB) > float64(f.PhysicalMB)*0.95 {
		b.ins("INS03", 3, "Online", EffortQuickWin, "Max server memory leaves too little memory for the operating system.", ins03LowWhy,
			fmt.Sprintf("Lower max server memory to about %d MB: EXEC sp_configure 'max server memory (MB)', %d; RECONFIGURE;", suggested, suggested),
			evidence, fix)
	}
	if !f.IsExpress() && f.PhysicalMB >= 16384 && f.MaxServerMemoryMB < f.PhysicalMB/2 {
		b.ins("INS03", 4, "Online", EffortQuickWin, "SQL Server may use less than half of the server memory.", ins03HalfWhy,
			fmt.Sprintf("Check what else runs on this server. If it is dedicated to SQL Server, raise max server memory towards %d MB.", suggested),
			evidence, "")
	}
}

// ins04: MAXDOP 0 on more than 8 CPUs, above the suggestion, or 1.
func (b *setupBuild) ins04() {
	f := b.f
	sug := f.SuggestedMaxDop()
	evidence := fmt.Sprintf("Logical CPUs: %d, per NUMA node: %d.", f.CPUCount, f.CoresPerNode)
	do := fmt.Sprintf("EXEC sp_configure 'max degree of parallelism', %d; RECONFIGURE;", sug)
	fix := "maxdop"
	if sug <= 0 {
		fix = ""
	}
	switch {
	case f.MaxDop == 0 && f.CPUCount > 8:
		b.ins("INS04", 3, "Online", EffortQuickWin, fmt.Sprintf("MAXDOP is 0, so one query can use all %d CPUs.", f.CPUCount),
			ins04ZeroWhy, do, evidence, fix)
	case f.MaxDop > sug && sug > 0:
		b.ins("INS04", 4, "Online", EffortQuickWin, fmt.Sprintf("MAXDOP is %d, above the recommended %d.", f.MaxDop, sug),
			ins04AboveWhy, do, evidence, fix)
	case f.MaxDop == 1 && f.CPUCount > 1:
		b.ins("INS04", 5, "Planned", EffortNone, "Parallelism is switched off server-wide (MAXDOP = 1).", ins04OneWhy,
			fmt.Sprintf("Confirm whether a vendor requires MAXDOP 1. If not, consider %d together with a cost threshold of 50 (INS05).", sug),
			fmt.Sprintf("Logical CPUs: %d.", f.CPUCount), "")
	}
}

// ins05: cost threshold still 5 (or less) where queries can go parallel.
func (b *setupBuild) ins05() {
	f := b.f
	if f.CostThreshold <= 5 && f.MaxDop != 1 && f.CPUCount > 1 {
		b.ins("INS05", 3, "Online", EffortQuickWin, "Cost threshold for parallelism is still the default of 5.", ins05Why, ins05Do,
			fmt.Sprintf("Current value: %d.", f.CostThreshold), "cost-threshold")
	}
}

// ins06: optimize for ad hoc workloads off.
func (b *setupBuild) ins06() {
	f := b.f
	if f.OptimizeAdHoc != 0 || f.PlanCacheMB <= 0 {
		return
	}
	rank := 4
	if f.SingleUseMB >= 100 && f.SingleUseMB > f.PlanCacheMB*0.25 {
		rank = 3
	}
	b.ins("INS06", rank, "Online", EffortQuickWin, "Optimize for ad hoc workloads is off.", ins06Why, ins06Do,
		fmt.Sprintf("Single-use plans: %.1f MB of %.1f MB plan cache.", f.SingleUseMB, f.PlanCacheMB), "optimize-adhoc")
}

// ins07: instant file initialization off; skipped when not visible.
func (b *setupBuild) ins07() {
	switch {
	case b.f.IFI == nil:
		b.skip(ins07Skipped, "INS07")
	case *b.f.IFI == "N":
		b.ins("INS07", 3, "Offline", EffortQuickWin, "Instant file initialization is off.", ins07Why, ins07Do,
			"instant_file_initialization_enabled = N", "")
	}
}

// ins08: lock pages in memory not granted on a large-memory server; skipped when not visible.
func (b *setupBuild) ins08() {
	f := b.f
	switch {
	case f.MemoryModel == nil:
		b.skip(ins08Skipped, "INS08")
	case *f.MemoryModel == "CONVENTIONAL" && f.PhysicalMB >= 65536 && !f.IsExpress():
		b.ins("INS08", 4, "Offline", EffortQuickWin, "Lock pages in memory is not enabled on a large-memory server.", ins08Why, ins08Do,
			fmt.Sprintf("Memory model: %s. Physical memory: %d GB.", *f.MemoryModel, f.PhysicalMB/1024), "")
	}
}

func (b *setupBuild) ins09() {
	if b.f.PriorityBoost == 1 {
		b.ins("INS09", 2, "Offline", EffortQuickWin, "Priority boost is on.", ins09Why, ins09Do, "priority boost = 1", "")
	}
}

func (b *setupBuild) ins10() {
	f := b.f
	if f.BackupCompression == 0 && (f.EngineEdition == 2 || f.EngineEdition == 3 || f.EngineEdition == 8) {
		b.ins("INS10", 4, "Online", EffortQuickWin, "Backup compression is not the default.", ins10Why, ins10Do,
			"backup compression default = 0", "backup-compression")
	}
}

func (b *setupBuild) ins11() {
	f := b.f
	if f.FillFactor != 0 && f.FillFactor != 100 {
		b.ins("INS11", 4, "Offline", EffortModerate, fmt.Sprintf("Server-wide fill factor is %d%%.", f.FillFactor), ins11Why, ins11Do,
			fmt.Sprintf("fill factor (%%) = %d", f.FillFactor), "")
	}
}

// ins12: Windows low on physical memory now or in the last 7 days.
func (b *setupBuild) ins12() {
	f := b.f
	lowNow := strings.Contains(strings.ToLower(f.MemoryState), "low")
	if !lowNow && f.Low7 <= 0 {
		return
	}
	rank := 3
	if lowNow || f.Low24 > 0 {
		rank = 2
	}
	finding := ""
	if lowNow {
		finding = fmt.Sprintf("Windows is low on physical memory right now (%s MB free).", n0int(f.AvailableMB))
	} else {
		finding = fmt.Sprintf("Windows signalled low physical memory %d time(s) in the last 7 days", f.Low7)
		if f.Low24 > 0 {
			finding += fmt.Sprintf(", %d of them in the last 24 hours", f.Low24)
		}
		finding += "."
	}
	state := f.MemoryState
	if state == "" {
		state = "?"
	}
	evidence := fmt.Sprintf("Memory state: %s. Available: %s MB. Last low-memory signal: %s.", state, n0int(f.AvailableMB),
		sqlTimeOr(f.LowLast, "none in the last 7 days"))
	b.ins("INS12", rank, "Planned", EffortModerate, finding, ins12Why, ins12Do, evidence, "")
}

// ins13: the Windows power plan is not High performance; skipped when it could not be read.
func (b *setupBuild) ins13() {
	f := b.f
	switch {
	case f.PowerError != "":
		b.skip(powerNotRead+f.PowerError, "INS13")
		return
	case f.PowerScheme == nil:
		b.skip(ins13Skipped, "INS13")
		return
	}
	scheme := strings.ToLower(strings.TrimSpace(*f.PowerScheme))
	if scheme == powerHigh || scheme == powerUltimate {
		return
	}
	name := "not High performance"
	switch scheme {
	case powerBalanced:
		name = "Balanced"
	case powerSaver:
		name = "Power saver"
	}
	b.ins("INS13", 4, "Online", EffortQuickWin, "The Windows power plan is "+name+".", ins13Why, ins13Do,
		"ActivePowerScheme: "+*f.PowerScheme+".", "")
}

// Pending are the server settings waiting for RECONFIGURE, without the one SQL Server always
// shows as pending: min server memory configured 0 shows 16 MB in use.
// Source: DarlingData sp_PerfCheck.sql 4146-4166 (MIT): the same exclusion.
func (f SetupFacts) Pending() []PendingConfig {
	out := []PendingConfig{}
	for _, p := range f.PendingConfig {
		if strings.EqualFold(p.Name, "min server memory (MB)") && p.ValueInUse == 16 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// PendingOtherThan are the pending settings a fix's RECONFIGURE activates besides its own setting
// and 'show advanced options', which every server fix statement sets itself.
func (f SetupFacts) PendingOtherThan(setting string) []PendingConfig {
	out := []PendingConfig{}
	for _, p := range f.Pending() {
		if strings.EqualFold(p.Name, setting) || strings.EqualFold(p.Name, "show advanced options") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// PendingList writes pending settings as "{name} {in use} -> {pending}, …".
func PendingList(ps []PendingConfig) string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, fmt.Sprintf("%s %d -> %d", p.Name, p.ValueInUse, p.Value))
	}
	return strings.Join(parts, ", ")
}

const (
	ins14Why        = "The next RECONFIGURE, from anyone (a script, a maintenance tool, or one of the app's own server fixes), activates all of them at once, so a change someone started and forgot can take effect without warning."
	ins15UnknownWhy = "A global trace flag changes how the whole server behaves. A flag turned on by hand is lost at the next restart, and one in the startup parameters stays for good; either way someone should know why it is there."
	ins15UnknownDo  = "Check the startup parameters in SQL Server Configuration Manager: a flag listed there (-T) survives a restart, one turned on with DBCC TRACEON does not. Keep the ones the application vendor asks for."
	ins16MinWhy     = "SQL Server never shrinks below the minimum, so when Windows or another program needs memory, Windows pages to disk instead and everything slows down."
	ins16MinDo      = "Set min server memory to 0 (the default) or well below max, unless the vendor asked for it: EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'min server memory (MB)', 0; RECONFIGURE;"
)

// ins14: server settings changed with sp_configure that RECONFIGURE has not activated yet.
func (b *setupBuild) ins14() {
	ps := b.f.Pending()
	if len(ps) == 0 {
		return
	}
	items, back, evidence := []string{}, []string{}, []string{}
	for _, p := range ps {
		items = append(items, fmt.Sprintf("%s (%d -> %d)", p.Name, p.ValueInUse, p.Value))
		back = append(back, fmt.Sprintf("EXEC sp_configure '%s', %d;", strings.ReplaceAll(p.Name, "'", "''"), p.ValueInUse))
		evidence = append(evidence, fmt.Sprintf("%s: value_in_use %d, value %d", p.Name, p.ValueInUse, p.Value))
	}
	finding := fmt.Sprintf("%d server settings have a changed value that has not taken effect: %s.", len(ps), strings.Join(items, ", "))
	if len(ps) == 1 {
		finding = "1 server setting has a changed value that has not taken effect: " + items[0] + "."
	}
	do := "Decide each one. If the new value is wanted, run RECONFIGURE in a quiet moment. If not, set it back to the value in use (no RECONFIGURE needed): " +
		strings.Join(back, " ")
	b.ins("INS14", 3, "Planned", EffortQuickWin, finding, ins14Why, do, "sys.configurations: "+strings.Join(evidence, "; ")+".", FixReconfigure)
}

// traceFlag is what the app says about one well-known global trace flag.
type traceFlag struct {
	rank int    // 3 Medium, 4 Low, 5 Info
	text string // the one-line explanation
	kind string // "startup", "review", "redundant"
}

// traceFlags are the global trace flags INS15 names one by one.
// Source: DarlingData sp_PerfCheck.sql 1486-1615 (MIT): the flag list and the one-line texts.
// Severities are one step lower than the source's, as docs/phase3f-contract.md sets them.
var traceFlags = map[int]traceFlag{
	1211: {3, "Disables lock escalation entirely, which can bloat lock memory and hurt concurrency. Almost never recommended; prefer 1224 if you truly must.", "review"},
	1224: {4, "Disables count-based lock escalation (escalation still happens under memory pressure). Rarely necessary.", "review"},
	3608: {3, "A startup-only flag (recover master only) that should not be set on a running production server.", "startup"},
	3609: {3, "A startup-only flag (skip tempdb creation) that should not be set on a running production server.", "startup"},
	834:  {4, "Uses large-page allocations for the buffer pool. Can slow or block startup and interacts badly with columnstore; use deliberately.", "review"},
	4199: {5, "Enables all query optimizer hotfixes globally, which can change plans server-wide. On 2016+ prefer the database-scoped QUERY_OPTIMIZER_HOTFIXES option.", "review"},
	8048: {5, "Partitions memory objects per CPU to reduce spinlock contention. Only relevant on high-core NUMA servers.", "review"},
	1117: {5, "Grows all files in a filegroup together. Redundant for tempdb on 2016+, where this is already the default.", "redundant"},
	1118: {5, "Forces full-extent allocation. Redundant on 2016+, where this is already the default for tempdb.", "redundant"},
	2371: {5, "Lowers the auto-update-statistics threshold. Redundant on 2016+ at compatibility level 130+.", "redundant"},
}

// ins15: one finding per global trace flag, the well-known ones with their text and the others as
// Info. The object of each is the flag, which the trace-flag-off fix's options name; every flag but
// the startup-only ones offers that fix (docs/phase4d-contract.md).
func (b *setupBuild) ins15() {
	flags := append([]int(nil), b.f.TraceFlags...)
	if len(flags) == 0 {
		return
	}
	slices.Sort(flags)
	all := make([]string, 0, len(flags))
	for _, n := range flags {
		all = append(all, fmt.Sprint(n))
	}
	evidence := "Global trace flags (DBCC TRACESTATUS(-1)): " + strings.Join(all, ", ") + "."
	for _, n := range flags {
		flag := func(rank int, changeType, effort, finding, why, do string) {
			fix := ""
			if TraceFlagOffOffered(n) {
				fix = FixTraceFlagOff
			}
			b.add("INS15", rank, changeType, effort, fmt.Sprint(n), finding, why, do, evidence, fix)
		}
		tf, ok := traceFlags[n]
		if !ok {
			flag(5, "None", EffortNone, fmt.Sprintf("Trace flag %d is on; look it up before a restart removes or keeps it.", n),
				ins15UnknownWhy, ins15UnknownDo)
			continue
		}
		finding := fmt.Sprintf("Trace flag %d is on for the whole server.", n)
		switch tf.kind {
		case "startup":
			flag(tf.rank, "Offline", EffortQuickWin, finding, tf.text,
				fmt.Sprintf("This flag is for starting SQL Server in a repair situation. Remove -T%d from the startup parameters (SQL Server Configuration Manager) and restart SQL Server in a maintenance window.", n))
		case "redundant":
			do := "Nothing to do on this version; keep it."
			if b.f.Major() >= 13 {
				do = fmt.Sprintf("Nothing to do now. It can be removed from the startup parameters (-T%d in SQL Server Configuration Manager) at the next planned restart.", n)
			}
			flag(tf.rank, "None", EffortNone, finding, tf.text, do)
		default:
			changeType, effort := "Planned", EffortQuickWin
			if tf.rank >= 5 {
				changeType, effort = "None", EffortNone
			}
			flag(tf.rank, changeType, effort, finding, tf.text,
				fmt.Sprintf("Find out who turned it on and why (the startup parameters in SQL Server Configuration Manager, or a DBCC TRACEON in a startup job). If nobody needs it any more, remove it there and run DBCC TRACEOFF(%d, -1);", n))
		}
	}
}

// ins16: min server memory close to max; lightweight pooling or an affinity mask set.
func (b *setupBuild) ins16() {
	f := b.f
	if f.MaxServerMemoryMB > 0 && f.MaxServerMemoryMB < unlimitedMemory && f.MinServerMemoryMB*10 >= f.MaxServerMemoryMB*9 {
		p := f.MinServerMemoryMB * 100 / f.MaxServerMemoryMB
		b.ins("INS16", 4, "Online", EffortQuickWin,
			fmt.Sprintf("Min server memory is set to %d MB, %d%% of max: SQL Server cannot give memory back to Windows.", f.MinServerMemoryMB, p),
			ins16MinWhy, ins16MinDo,
			fmt.Sprintf("min server memory (MB) = %d, max server memory (MB) = %d.", f.MinServerMemoryMB, f.MaxServerMemoryMB), FixMinServerMemory)
	}
	names, settings, why := []string{}, []string{}, []string{}
	if f.LightweightPooling == 1 {
		names = append(names, "lightweight pooling (fiber mode) is on")
		settings = append(settings, "lightweight pooling")
		why = append(why, "Lightweight pooling is rarely faster and breaks some components (linked servers, CLR code, distributed transactions).")
	}
	masks := []struct {
		name string
		v    int64
	}{{"affinity mask", f.AffinityMask}, {"affinity64 mask", f.Affinity64Mask}, {"affinity I/O mask", f.AffinityIOMask}, {"affinity64 I/O mask", f.Affinity64IOMask}}
	anyMask := false
	for _, m := range masks {
		if m.v != 0 {
			names = append(names, fmt.Sprintf("%s is %d", m.name, m.v))
			settings = append(settings, m.name)
			anyMask = true
		}
	}
	if anyMask {
		why = append(why, "An affinity mask ties SQL Server to some CPUs only: it cannot use the others, and CPUs added to the server later stay unused.")
	}
	if len(names) == 0 {
		return
	}
	set := []string{"EXEC sp_configure 'show advanced options', 1; RECONFIGURE;"}
	for _, s := range settings {
		set = append(set, fmt.Sprintf("EXEC sp_configure '%s', 0;", s))
	}
	set = append(set, "RECONFIGURE;")
	do := "Find out why it was set (a vendor, or an old tuning attempt). If nobody needs it, set it back to 0: " + strings.Join(set, " ")
	changeType := "Planned"
	if f.LightweightPooling == 1 {
		changeType = "Offline"
		do += "  Lightweight pooling takes effect after a restart."
	}
	finding := "Server options that are rarely needed are set: " + strings.Join(names, ", ") + "."
	b.ins("INS16", 4, changeType, EffortQuickWin, finding, strings.Join(why, " "), do, strings.Join(names, "; ")+".", "")
}

// cpuLimit is an edition's compute capacity limit for one instance of the Database Engine: the
// lesser of Sockets processor sockets or Cores cores (on a VM, logical processors). Microsoft Learn,
// "Editions and supported features" 2016 to 2022 and "Compute capacity limits by edition"
// (docs/cases/2026-11-05-offline-schedulers.md).
type cpuLimit struct {
	Edition        string // as the texts name it: "Standard edition"
	Sockets, Cores int
}

// CPULimit is the edition's limit, false for the editions without one below the operating system's
// (Enterprise core-based, Developer, Evaluation). Enterprise under Server + CAL licensing (20 cores)
// is not told apart here: its Edition string does not say so reliably.
func (f SetupFacts) CPULimit() (cpuLimit, bool) {
	switch {
	case f.EngineEdition == 4:
		return cpuLimit{"Express", 1, 4}, true
	case f.EngineEdition == 2 && strings.Contains(strings.ToLower(f.Edition), "web"):
		return cpuLimit{"Web edition", 4, 16}, true
	case f.EngineEdition == 2 && f.Major() >= 17:
		return cpuLimit{"Standard edition", 4, 32}, true // SQL Server 2025 raised it from 24
	case f.EngineEdition == 2:
		return cpuLimit{"Standard edition", 4, 24}, true
	}
	return cpuLimit{}, false
}

// ins17: schedulers SQL Server cannot use (VISIBLE OFFLINE) when nobody chose them with an affinity
// (MANUAL: INS16 shows the masks), with the cause named from the edition and the processor layout:
// more sockets than the edition takes (on a VM, the exact change), more cores than it takes, or not
// known. High whatever the load: CPUs the client pays for that no query can run on.
func (b *setupBuild) ins17() {
	f := b.f
	if f.OfflineSchedulers <= 0 || strings.EqualFold(f.AffinityType, "MANUAL") {
		return
	}
	total := f.OnlineSchedulers + f.OfflineSchedulers
	uses := fmt.Sprintf("SQL Server uses %d of the server's %d CPUs", f.OnlineSchedulers, total)
	lim, limited := f.CPULimit()
	finding, do, changeType := "", "", "Planned"
	switch {
	case limited && f.Sockets > lim.Sockets && f.VirtualMachine:
		finding = fmt.Sprintf("%s: the VM shows %s with %s each, and %s uses at most %s.", uses,
			plural(int64(f.Sockets), "socket", "sockets"), plural(int64(f.CoresPerSocket), "core", "cores"), lim.Edition,
			plural(int64(lim.Sockets), "socket", "sockets"))
		do = fmt.Sprintf("Ask the client's IT to change the VM's processors to %s: the same %d vCPUs, fewer sockets. It needs the VM powered off; nothing changes in SQL Server.",
			ins17Layout(f.CPUCount, lim.Sockets), f.CPUCount)
		if f.CPUCount > lim.Cores {
			do += fmt.Sprintf(" %s uses at most %d cores, so SQL Server will then use %d of them; the other %d can come off the VM.",
				upperFirst(lim.Edition), lim.Cores, lim.Cores, f.CPUCount-lim.Cores)
		}
		changeType = "Offline"
	case limited && f.Sockets > lim.Sockets:
		finding = fmt.Sprintf("%s: the server has %d processor sockets and %s uses at most %s.", uses, f.Sockets, lim.Edition,
			plural(int64(lim.Sockets), "socket", "sockets"))
		do = fmt.Sprintf("This is the hardware, not a setting: only Enterprise edition uses more than %s. Decide with the client whether the unused CPUs are worth Enterprise, or a server with fewer, larger processors.",
			plural(int64(lim.Sockets), "socket", "sockets"))
	case limited && f.CPUCount > lim.Cores:
		finding = fmt.Sprintf("%s: %s uses at most %d cores.", uses, lim.Edition, lim.Cores)
		if f.VirtualMachine {
			do = fmt.Sprintf("Ask the client's IT to take the %d vCPUs SQL Server cannot use off the VM (they are paid for and never used), or move to Enterprise edition if the workload needs them.", f.CPUCount-lim.Cores)
		} else {
			do = fmt.Sprintf("Only Enterprise edition uses more than %d cores. Decide with the client whether the unused cores are worth it.", lim.Cores)
		}
	default:
		finding = fmt.Sprintf("%s: %s offline.", uses, plural(int64(f.OfflineSchedulers), "scheduler is", "schedulers are"))
		do = ins17UnknownDo
	}
	layout := "socket layout not shown before SQL Server 2016 SP2"
	if f.Sockets > 0 {
		layout = fmt.Sprintf("%s with %s", plural(int64(f.Sockets), "socket", "sockets"), plural(int64(f.CoresPerSocket), "core", "cores"))
	}
	where := "physical server"
	if f.VirtualMachine {
		where = "virtual machine"
	}
	now := fmt.Sprintf("Runnable tasks now: %d on the %d online schedulers.", f.RunnableNow, f.OnlineSchedulers)
	if f.RunnableNow == 0 {
		now = fmt.Sprintf("Runnable tasks now: 0 on the %d online schedulers, so this is lost capacity, not pressure now.", f.OnlineSchedulers)
	}
	evidence := fmt.Sprintf("%s %s. CPUs: %d (%s, %s). Schedulers: %d online, %d offline (VISIBLE OFFLINE). Affinity: %s. %s",
		f.Edition, f.ProductVersion, f.CPUCount, layout, where, f.OnlineSchedulers, f.OfflineSchedulers, orDash(f.AffinityType), now)
	b.ins("INS17", 2, changeType, EffortQuickWin, finding, ins17Why, do+" "+ins17Check, evidence, "")
}

// ins17Layout is the VM processor layout to ask for: n cores in one socket, or two sockets of n/2
// when the edition takes two and n splits evenly.
func ins17Layout(n, sockets int) string {
	one := fmt.Sprintf("1 socket with %d cores", n)
	if sockets >= 2 && n >= 4 && n%2 == 0 {
		return fmt.Sprintf("%s (or 2 sockets with %d cores each)", one, n/2)
	}
	return one
}

const (
	ins17Why       = "An offline scheduler is a CPU SQL Server cannot use, whatever the load: the server has it, the client pays for it, and no query ever runs on it. Each edition uses at most a number of processor sockets and of cores, and on a VM every virtual socket counts as a socket, so a VM with many one-core sockets loses CPUs that one socket with many cores would keep."
	ins17UnknownDo = "Ask the client's IT to read the startup line of the SQL Server error log: EXEC sys.xp_readerrorlog 0, 1, N'socket'; says how many sockets and cores SQL Server found and how many logical processors it uses. If it uses fewer than the server has, the edition's limit is the cause (an Enterprise Server + CAL licence takes 20 cores): give the VM fewer sockets with more cores, or move to an edition without the limit."
	ins17Check     = "Then check: SELECT cpu_count, scheduler_count, socket_count, cores_per_socket FROM sys.dm_os_sys_info; (scheduler_count equals cpu_count) and EXEC sys.xp_readerrorlog 0, 1, N'socket'; (the startup line uses every logical processor)."
)

// Texts from 01-QuickScan.sql section 2 and PerformanceTriage.sql sections 2 and 3, verbatim.
const (
	dbc02Why  = "The database shuts down when the last user disconnects and has to start up again on the next connection. This adds delays, empties memory caches and floods the error log."
	dbc03Why  = "The files are shrunk and then have to grow again, over and over. This causes heavy I/O, blocking and badly fragmented indexes."
	dbc04Why  = "CHECKSUM detects corruption caused by storage when a page is read. Without it, damage can go unnoticed until it spreads into backups."
	dbc05Why  = "The query optimizer uses statistics to estimate row counts. Without fresh statistics it picks poor plans and queries slow down."
	dbc06Why  = "The database runs with older query optimizer behaviour and misses newer performance features. Levels below 120 also use the legacy cardinality estimator."
	dbc07Why  = "Often a former employee or a server it was restored from. It can break Agent jobs, CLR code and EXECUTE AS."
	dbc09Why  = "Changed pages are written to disk in large bursts, causing I/O spikes. Indirect checkpoints (the default for new databases since SQL Server 2016) spread the writes out."
	dbc10Why  = "Query Store is the flight recorder for query performance. Without it, it is much harder to find out why a query got slower after a release, upgrade or plan change."
	dbc11Why  = "Security risk: someone who controls this database may be able to gain full control of the server. Often switched on for CLR assemblies."
	cfg01Why  = "Usually switched on to work around one bad query, then forgotten, and carried along in every copy."
	cfg01Do   = "Find out why it was set. Test without it: ALTER DATABASE SCOPED CONFIGURATION SET LEGACY_CARDINALITY_ESTIMATION = OFF;"
	cpy01Why  = "Copies bring along the settings, users, statistics and indexes of the database they came from. The Copied Database checks look for the usual leftovers."
	cpy02Why  = "Queries that compare text with temp tables either fail with \"Cannot resolve the collation conflict\" or have to convert values, which stops indexes being used."
	cpy02Do   = "Confirm the collation the application vendor requires. Changing a database collation is a project, so the usual fix is installing the client SQL instance with the matching collation. If code uses temp tables, it can add COLLATE DATABASE_DEFAULT to text columns."
	cpy04Why  = "After a restore, users are linked to logins of the old server. Anything connecting as this user fails to log in or gets the wrong permissions."
	cpy04NoDo = "Create the login (with the client's password) and link it with ALTER USER ... WITH LOGIN, or drop the user if it is no longer needed."
	cpy06aWhy = "Every procedure or function that uses the CLR code fails with an error."
	cpy06aDo  = "With the server owner's approval: EXEC sp_configure 'clr enabled', 1; RECONFIGURE;"
	cpy06bWhy = "Restoring a database always switches TRUSTWORTHY off, so CLR code that worked on the source server can fail here. If CLR calls work, the assemblies are signed and this can be ignored."
)

func (b *setupBuild) databaseChecks() {
	f := b.f
	db := QuoteName(f.Database)
	if f.AutoClose {
		b.dbc("DBC02", 2, "Online", EffortQuickWin, "AUTO_CLOSE is on.", dbc02Why,
			"ALTER DATABASE "+db+" SET AUTO_CLOSE OFF;", "", "auto-close")
	}
	if f.AutoShrink {
		b.dbc("DBC03", 2, "Online", EffortQuickWin, "AUTO_SHRINK is on.", dbc03Why,
			"ALTER DATABASE "+db+" SET AUTO_SHRINK OFF;", "", "auto-shrink")
	}
	if f.PageVerify != "CHECKSUM" {
		b.dbc("DBC04", 3, "Online", EffortQuickWin, "Page verify is "+f.PageVerify+" instead of CHECKSUM.", dbc04Why,
			"ALTER DATABASE "+db+" SET PAGE_VERIFY CHECKSUM;  (Pages are protected as they are next written; an index rebuild protects them all.)",
			"page_verify_option_desc = "+f.PageVerify, "page-verify")
	}
	if !f.AutoCreateStats || !f.AutoUpdateStats {
		finding := "Automatic updating of statistics is off."
		switch {
		case !f.AutoCreateStats && !f.AutoUpdateStats:
			finding = "Automatic creation and updating of statistics are both off."
		case !f.AutoCreateStats:
			finding = "Automatic creation of statistics is off."
		}
		b.dbc("DBC05", 2, "Online", EffortQuickWin, finding, dbc05Why,
			"Check first that the application vendor does not require this (rare), then: ALTER DATABASE "+db+
				" SET AUTO_CREATE_STATISTICS ON; ALTER DATABASE "+db+" SET AUTO_UPDATE_STATISTICS ON;",
			fmt.Sprintf("auto create = %d, auto update = %d", bit(f.AutoCreateStats), bit(f.AutoUpdateStats)), "auto-statistics")
	}
	serverCompat := f.Major() * 10
	if f.CompatLevel > 0 && f.CompatLevel < serverCompat {
		rank := 4
		if f.CompatLevel < 120 {
			rank = 3
		}
		b.dbc("DBC06", rank, "Planned", EffortModerate,
			fmt.Sprintf("Compatibility level is %d; this server supports %d.", f.CompatLevel, serverCompat), dbc06Why,
			fmt.Sprintf("Check vendor support, then test the application at the higher level (with Query Store on to catch regressions) before: ALTER DATABASE %s SET COMPATIBILITY_LEVEL = %d;", db, serverCompat),
			fmt.Sprintf("compatibility_level = %d", f.CompatLevel), "")
	}
	if f.OwnerMissing {
		rank := 4
		if f.Trustworthy {
			rank = 3 // as CPY05: with TRUSTWORTHY on, CLR code runs with the owner's rights
		}
		b.dbc("DBC07", rank, "Online", EffortQuickWin, "The database owner is a login that no longer exists.", dbc07Why,
			"ALTER AUTHORIZATION ON DATABASE::"+db+" TO [sa];  (or another agreed service login)", "", "")
	}
	if f.Major() >= 13 {
		if f.TargetRecoverySec < 0 {
			b.skip(dbc09NotShown, "DBC09")
		} else if f.TargetRecoverySec == 0 {
			b.dbc("DBC09", 4, "Online", EffortQuickWin, "Database uses old-style automatic checkpoints.", dbc09Why,
				"ALTER DATABASE "+db+" SET TARGET_RECOVERY_TIME = 60 SECONDS;", "target_recovery_time_in_seconds = 0", "indirect-checkpoints")
		}
	} else {
		b.skip(dbc09Skipped, "DBC09")
	}
	b.dbc10()
	if f.Trustworthy {
		b.dbc("DBC11", 4, "Planned", EffortModerate, "TRUSTWORTHY is on.", dbc11Why,
			"Find out why it is on. If CLR code needs it, the safer alternative is signing the assembly with a certificate. Test before: ALTER DATABASE "+db+" SET TRUSTWORTHY OFF;",
			"", "")
	}
	b.dbc13()
	b.dbc14()
	b.cfg01()
	b.cpy01()
	b.cpy02()
	b.cpy04()
	b.cpy06()
}

func bit(v bool) int {
	if v {
		return 1
	}
	return 0
}

// dbc10: Query Store off (Low), or on but differing or not recording (Medium), with the phase 2
// wording and the phase 1 fix.
func (b *setupBuild) dbc10() {
	f := b.f
	q := f.DB.QueryStore
	switch {
	case f.Major() > 0 && f.Major() < 13:
		b.skip(dbc10Old, "DBC10")
		return
	case q == nil:
		note := "Could not read Query Store settings for this database."
		if f.DB.Error != "" {
			note = "Could not read Query Store settings for this database: " + f.DB.Error
		}
		b.skip(note, "DBC10")
		return
	case q.Verdict == VerdictUnavailable:
		b.skip(unavailableReason(f.DB), "DBC10")
		return
	case q.Verdict == VerdictOK:
		return
	}
	finding, rank := QueryStoreFinding(f.Database, *q), 3
	if q.Verdict == VerdictOff {
		rank = 4
	}
	fix := ""
	if q.CanFix {
		fix = "query-store"
	}
	do := "Apply the recommended Query Store settings."
	if p := BuildQueryStoreFix(q.Options(), f.Major(), f.Database); p.Statement != "" {
		do = "Apply the recommended Query Store settings: " + p.Statement
	}
	b.add("DBC10", rank, "Online", EffortQuickWin, f.Database, finding, dbc10Why, do,
		fmt.Sprintf("Storage used: %d MB of %d MB.", q.CurrentStorageMb, q.MaxStorageMb), fix)
}

// QueryStoreFinding is the phase 2 sentence about a database's Query Store that is off or differs.
func QueryStoreFinding(dbName string, q QueryStoreStatus) string {
	if q.Verdict == VerdictOff {
		return fmt.Sprintf("Query Store is off on %s, so its query history is lost at every restart.", dbName)
	}
	s := fmt.Sprintf("Query Store is on but differs from the recommended settings on %s", dbName)
	if len(q.Drift) > 0 {
		d := q.Drift[0]
		s += fmt.Sprintf(": %s is %s, recommended %s", d.Setting, d.Current, d.Target)
	}
	return s + "."
}

// Options are the Query Store options this status was built from.
func (q QueryStoreStatus) Options() QueryStoreOptions {
	return QueryStoreOptions{
		ActualState: q.ActualState, DesiredState: q.DesiredState, ReadonlyReason: q.ReadonlyReason,
		CurrentStorageMb: q.CurrentStorageMb, MaxStorageMb: q.MaxStorageMb, CaptureMode: q.CaptureMode,
		WaitStatsCaptureMode: q.WaitStatsCaptureMode, IntervalLengthMinutes: q.IntervalLengthMinutes,
		StaleQueryThresholdDays: q.StaleQueryThresholdDays, FlushIntervalSeconds: q.FlushIntervalSeconds,
		MaxPlansPerQuery: q.MaxPlansPerQuery, SizeBasedCleanupMode: q.SizeBasedCleanupMode,
	}
}

// dbc13: the database is not open to everyone (SINGLE_USER or RESTRICTED_USER). Someone set it on
// purpose, usually for a restore or a repair: the multi-user fix asks first in its warnings.
func (b *setupBuild) dbc13() {
	f := b.f
	var plain string
	switch strings.ToUpper(f.UserAccess) {
	case "SINGLE_USER":
		plain = "only one connection at a time"
	case "RESTRICTED_USER":
		plain = "only administrators and the database owner can connect"
	default:
		return
	}
	b.dbc("DBC13", 2, "Online", EffortQuickWin,
		fmt.Sprintf("%s is in %s mode (%s): the application cannot connect.", f.Database, strings.ToUpper(f.UserAccess), plain),
		"Every user and the application get a login error for this database until it is set back.",
		"Find out who set it and why (usually a restore or a repair that is still running). Once they are done: ALTER DATABASE "+
			QuoteName(f.Database)+" SET MULTI_USER;",
		"user_access_desc = "+f.UserAccess, FixMultiUser)
}

// scopedDefault is the default value of one database scoped configuration, as text; flag says the
// value is ON/OFF (1/0).
type scopedDefault struct {
	value string
	flag  bool
}

// scopedDefaults are the database scoped configurations DBC14 compares, with their defaults. Rows
// SQL Server has but this table does not name are not compared. DOP_FEEDBACK's default is 0 before
// SQL Server 2025 and 1 from 2025 (scopedDefaultFor).
// Source: DarlingData sp_PerfCheck.sql 4975-5110 (MIT): the list and its defaults (PREVIEW_FEATURES
// and the READABLE_SECONDARY_TEMPORARY_STATS options are left out, as the source leaves them out).
var scopedDefaults = map[string]scopedDefault{
	"MAXDOP":                                      {"0", false},
	"LEGACY_CARDINALITY_ESTIMATION":               {"0", true},
	"PARAMETER_SNIFFING":                          {"1", true},
	"QUERY_OPTIMIZER_HOTFIXES":                    {"0", true},
	"INTERLEAVED_EXECUTION_TVF":                   {"1", true},
	"BATCH_MODE_MEMORY_GRANT_FEEDBACK":            {"1", true},
	"BATCH_MODE_ADAPTIVE_JOINS":                   {"1", true},
	"TSQL_SCALAR_UDF_INLINING":                    {"1", true},
	"OPTIMIZE_FOR_AD_HOC_WORKLOADS":               {"0", true},
	"ROW_MODE_MEMORY_GRANT_FEEDBACK":              {"1", true},
	"ISOLATE_SECURITY_POLICY_CARDINALITY":         {"0", true},
	"BATCH_MODE_ON_ROWSTORE":                      {"1", true},
	"DEFERRED_COMPILATION_TV":                     {"1", true},
	"ACCELERATED_PLAN_FORCING":                    {"1", true},
	"LAST_QUERY_PLAN_STATS":                       {"0", true},
	"EXEC_QUERY_STATS_FOR_SCALAR_FUNCTIONS":       {"1", true},
	"PARAMETER_SENSITIVE_PLAN_OPTIMIZATION":       {"1", true},
	"CE_FEEDBACK":                                 {"1", true},
	"MEMORY_GRANT_FEEDBACK_PERSISTENCE":           {"1", true},
	"MEMORY_GRANT_FEEDBACK_PERCENTILE_GRANT":      {"1", true},
	"OPTIMIZED_PLAN_FORCING":                      {"1", true},
	"DOP_FEEDBACK":                                {"0", true},
	"FORCE_SHOWPLAN_RUNTIME_PARAMETER_COLLECTION": {"0", true},
	"OPTIMIZED_SP_EXECUTESQL":                     {"0", true},
	"FULLTEXT_INDEX_VERSION":                      {"2", false},
	"OPTIONAL_PARAMETER_OPTIMIZATION":             {"1", true},
}

// scopedDefaultFor is the default of one database scoped configuration on this engine version.
func scopedDefaultFor(name string, major int) (scopedDefault, bool) {
	d, ok := scopedDefaults[strings.ToUpper(name)]
	if ok && strings.EqualFold(name, "DOP_FEEDBACK") && major >= 17 {
		d.value = "1"
	}
	return d, ok
}

// scopedValue normalises a scoped configuration value for comparing: ON/OFF become 1/0.
func scopedValue(v string) string {
	switch s := strings.ToUpper(strings.TrimSpace(v)); s {
	case "ON":
		return "1"
	case "OFF":
		return "0"
	default:
		return s
	}
}

// scopedText writes a value as the finding shows it: ON/OFF for a flag, the value otherwise.
func scopedText(v string, flag bool) string {
	if flag {
		switch v {
		case "1":
			return "ON"
		case "0":
			return "OFF"
		}
	}
	if v == "" {
		return "NULL"
	}
	return v
}

const (
	dbc14Why    = "These settings override the server's for this database only, and they travel with every copy and restore. The server checks (INS04, CFG01) do not see them."
	dbc14MaxWhy = "INS04 reads the server's MAXDOP, but queries in this database use their own, so the parallelism they really run with is not what the server says."
)

// dbc14: one finding per database scoped configuration that differs from the default (2016+),
// its object the configuration's name, which the scoped-config-reset fix's options name
// (docs/phase4d-contract.md); Medium when the database sets its own MAXDOP that differs from the
// server's, Info otherwise.
func (b *setupBuild) dbc14() {
	f := b.f
	switch {
	case f.ScopedConfigsError != "":
		b.skip(legacyCENotRead+f.ScopedConfigsError, "DBC14")
		return
	case f.ScopedConfigs == nil:
		b.skip(dbc14Skipped, "DBC14")
		return
	}
	items := []string{}
	for _, c := range NonDefaultScoped(f.ScopedConfigs, f.Major()) {
		d, _ := scopedDefaultFor(c.Name, f.Major())
		items = append(items, fmt.Sprintf("%s = %s (default %s)", c.Name, scopedText(scopedValue(c.Value), d.flag), scopedText(d.value, d.flag)))
	}
	if len(items) == 0 {
		return
	}
	list := strings.Join(items, ", ")
	for i, c := range NonDefaultScoped(f.ScopedConfigs, f.Major()) {
		d, _ := scopedDefaultFor(c.Name, f.Major())
		v := scopedValue(c.Value)
		item := items[i]
		back := fmt.Sprintf("ALTER DATABASE SCOPED CONFIGURATION SET %s = %s;", c.Name, scopedText(d.value, d.flag))
		if c.Name == "MAXDOP" && f.ConfigError == "" && v != fmt.Sprint(f.MaxDop) {
			b.add("DBC14", 3, "Planned", EffortQuickWin, c.Name,
				fmt.Sprintf("%s sets its own MAXDOP of %s, which overrides the server's %d for every query in this database.", f.Database, v, f.MaxDop),
				dbc14MaxWhy,
				"Decide which MAXDOP this database should use (the vendor may have asked for it). To follow the server again, inside the database: ALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 0;",
				"Non-default database scoped configurations: "+list+".", b.dbFix(FixScopedConfigReset))
			continue
		}
		b.add("DBC14", 5, "None", EffortNone, c.Name,
			fmt.Sprintf("%s changes a database scoped configuration from the default: %s.", f.Database, item), dbc14Why,
			"Nothing to do if it was set on purpose. Find out why it was set (often a workaround for one query, then forgotten); to set it back, inside the database: "+back,
			"sys.database_scoped_configurations: "+list+".", b.dbFix(FixScopedConfigReset))
	}
}

// dbFix is a database fix id when the database can be changed from here, "" otherwise (as dbc).
func (b *setupBuild) dbFix(id string) string {
	if !b.f.DB.CanFix() {
		return ""
	}
	return id
}

// cfg01: the legacy cardinality estimator forced on (2016+).
func (b *setupBuild) cfg01() {
	f := b.f
	switch {
	case f.LegacyCEError != "":
		b.skip(legacyCENotRead+f.LegacyCEError, "CFG01")
	case f.LegacyCE == nil:
		b.skip(cfg01Skipped, "CFG01")
	case *f.LegacyCE == 1:
		b.dbc("CFG01", 4, "Planned", EffortModerate, "The legacy cardinality estimator is forced on.", cfg01Why, cfg01Do, "", "")
	}
}

// cpy01: restored from another name or server, or file names that suggest a copy.
func (b *setupBuild) cpy01() {
	f := b.f
	if f.RestoreError != "" {
		b.skip(cpy01MsdbNote, "CPY01")
	}
	switch {
	case f.RestoredAt != nil && (f.SourceDb != f.Database || f.SourceServer != f.ServerName):
		name := f.DataFileName
		if name == "" {
			name = "?"
		}
		b.dbc("CPY01", 5, "None", EffortNone,
			fmt.Sprintf("This database is a copy of %s on %s, restored on %s.", f.SourceDb, f.SourceServer, f.RestoredAt.Format("2006-01-02")),
			cpy01Why, "", "Backup taken: "+sqlTimeOr(f.SourceBackupAt, "?")+". First data file name: "+name+".", "")
	case f.RestoredAt == nil && f.DataFileName != "" && !strings.Contains(strings.ToLower(f.DataFileName), strings.ToLower(f.Database)):
		b.dbc("CPY01", 5, "None", EffortNone, "The file names suggest this database was copied from "+f.DataFileName+".",
			cpy01Why, "", "No restore of this database is recorded on this server.", "")
	}
}

// cpy02: the database collation differs from the server's.
func (b *setupBuild) cpy02() {
	f := b.f
	if f.CollationError != "" {
		b.skip(insideNotRead+f.CollationError, "CPY02")
		return
	}
	if f.DbCollation == "" {
		b.skip(cpy02NotShown, "CPY02")
		return
	}
	if f.Collation == "" || strings.EqualFold(f.DbCollation, f.Collation) {
		return
	}
	finding := fmt.Sprintf("The database collation (%s) is different from the server and TempDB (%s).", f.DbCollation, f.Collation)
	evidence := fmt.Sprintf("Text columns in this database with yet another collation: %d.", f.OtherCollationCt)
	if f.SameCollationDb == "" {
		b.dbc("CPY02", 3, "Planned", EffortProject, finding, cpy02Why, cpy02Do, evidence, "")
		return
	}
	finding += " It matches " + f.SameCollationDb + ", which this database's code reads from, so it is most likely deliberate."
	b.dbc("CPY02", 5, "None", EffortNone, finding, cpy02Why,
		"Keep it: it matches "+f.SameCollationDb+". Only code that compares text with temp tables is at risk; add COLLATE DATABASE_DEFAULT to text columns in those temp tables.",
		evidence, "")
}

// cpy04: one finding per SQL user without a login.
func (b *setupBuild) cpy04() {
	f := b.f
	if f.OrphansError != "" {
		b.skip(insideNotRead+f.OrphansError, "CPY04")
		return
	}
	for _, u := range f.Orphans {
		do := cpy04NoDo
		if u.LoginExists {
			do = "A login with the same name exists. Link them: ALTER USER " + QuoteName(u.Name) + " WITH LOGIN = " + QuoteName(u.Name) + ";"
		}
		schema := u.DefaultSchema
		if schema == "" {
			schema = "-"
		}
		b.dbc("CPY04", 3, "Online", EffortQuickWin, "Database user "+u.Name+" has no matching login on this server.", cpy04Why, do,
			"Type: SQL user. Default schema: "+schema+".", "")
	}
}

// cpy06: CLR code with CLR switched off, or that may be blocked.
func (b *setupBuild) cpy06() {
	f := b.f
	if f.ClrError != "" {
		b.skip(insideNotRead+f.ClrError, "CPY06")
		return
	}
	if f.Assemblies > 0 && f.ConfigError == "" && f.ClrEnabled == 0 {
		b.dbc("CPY06", 2, "Online", EffortQuickWin, "The database has CLR code but CLR is switched off on this server.", cpy06aWhy, cpy06aDo,
			fmt.Sprintf("User CLR assemblies: %d.", f.Assemblies), "")
	}
	nonSafe := f.Assemblies - f.SafeAssemblies
	strict := "n/a"
	if f.ClrStrict != nil {
		strict = fmt.Sprint(*f.ClrStrict)
		if *f.ClrStrict == 1 {
			nonSafe = f.Assemblies
		}
	}
	if nonSafe > 0 && !f.Trustworthy && f.Trusted == 0 && !f.ClrLoaded {
		b.dbc("CPY06", 3, "Online", EffortQuickWin,
			"CLR code may be blocked: TRUSTWORTHY is off, the assemblies are not marked as trusted, and no CLR code has run here since the last restart.",
			cpy06bWhy,
			"Test a CLR call. If it fails, follow the application's install guide: usually ALTER DATABASE "+QuoteName(f.Database)+
				" SET TRUSTWORTHY ON; with a valid database owner (CPY05), or signing the assemblies with a certificate.",
			fmt.Sprintf("Assemblies needing more than SAFE: %d. clr strict security = %s.", nonSafe, strict), "")
	}
}

// facts are the four lines on the card: Version, Memory, Parallelism, Query Store.
func (b *setupBuild) facts() []Fact {
	f := b.f
	level := f.ProductLevel
	if f.UpdateLevel != "" {
		level += " " + f.UpdateLevel
	}
	version := Fact{Label: "Version", Value: ProductName(f.Major()) + " " + level, Tone: ToneNormal}
	state, bi := f.support()
	switch state {
	case supportOut:
		version.Value += ", out of support"
		version.Tone = ToneWorse
	case supportBehind:
		months := monthsBetween(bi.LatestDate, f.ServerNow)
		switch {
		case months <= 0:
			version.Value += ", behind the latest update"
		case months == 1:
			version.Value += ", 1 month behind"
		default:
			version.Value += fmt.Sprintf(", %d months behind", months)
		}
		version.Tone = ToneBad
	case supportCurrent:
		version.Value += ", up to date"
	default:
		version.Value += ", newer than the build table"
	}

	memory := Fact{Label: "Memory", Value: "could not be read", Tone: ToneNormal}
	if f.ConfigError == "" && f.SysInfoError == "" {
		ram := gb(f.PhysicalMB)
		if f.MaxServerMemoryMB >= unlimitedMemory {
			memory.Value = "no cap of " + ram + " GB"
		} else {
			memory.Value = gb(f.MaxServerMemoryMB) + " of " + ram + " GB cap"
		}
		memory.Tone = tone(b.actionable("INS02", "INS03"))
	}

	parallel := Fact{Label: "Parallelism", Value: "could not be read", Tone: ToneNormal}
	if f.ConfigError == "" {
		parallel.Value = fmt.Sprintf("MAXDOP %d, cost threshold %d", f.MaxDop, f.CostThreshold)
		parallel.Tone = tone(b.actionable("INS04", "INS05"))
	}

	qs := Fact{Label: "Query Store", Value: "could not be read", Tone: ToneNormal}
	if q := f.DB.QueryStore; q != nil {
		qs.Value = q.Summary
		qs.Tone = tone(q.Verdict == VerdictOff || q.Verdict == VerdictDrift)
	} else if f.Major() > 0 && f.Major() < 13 {
		qs.Value = "not before SQL Server 2016"
	}
	return []Fact{version, memory, parallel, qs}
}

// gb writes megabytes as gigabytes: whole when exact, else to one decimal.
func gb(mb int64) string {
	if mb%1024 == 0 {
		return n0int(mb / 1024)
	}
	return n1(float64(mb) / 1024)
}
