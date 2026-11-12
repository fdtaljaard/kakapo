package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The Memory screen, version 1 (docs/phase5v-contract.md): "where is the memory going, and is something
// abnormal?". Full is SQL Server's normal state; high memory use alone is never the finding. The
// thresholds below are starting points, kakapo's own, to be tuned against the client databases already
// profiled (decision 6); the signals they rest on are the case note's table "Is it abnormal?".
const (
	// MemoryWindowsLowMB: Windows with less free than this is short of memory (red).
	MemoryWindowsLowMB = 2048
	// MemoryLockAmberMB: lock memory above this is worth a look (amber).
	MemoryLockAmberMB = 1024
	// MemoryAdhocAmberMB: single-use ad hoc plans above this, with optimize for ad hoc workloads off (amber).
	MemoryAdhocAmberMB = 1024
	// MemoryTempdbAmberMB and MemoryTempdbShare: tempdb holding more than both of these of the data cache (amber).
	MemoryTempdbAmberMB = 2048
	MemoryTempdbShare   = 0.10
	// MemoryTestDatabaseMB: a database that looks like a test copy holding more cache than this gets a note on
	// its row. Not a memory signal: its pages are reused when other databases need them (a production case,
	// 2026-11-12), so it stays out of the verdict.
	MemoryTestDatabaseMB = 1024
	// MemoryCapHeadroomMB: with SQL Server at its cap and everything else as it is now, Windows should keep at
	// least this much free (amber when it would not). "Everything else" already holds Windows itself, so this
	// is room to move, not the operating system's share.
	MemoryCapHeadroomMB = 4096
	// MemoryReconcileShare: a breakdown must add up to the layer above it within this share (decision 5).
	MemoryReconcileShare = 0.05
	// MemoryFillingShare: SQL Server committed below this share of its target is still filling the cache.
	MemoryFillingShare = 0.80
	// MemoryNoCap is max server memory's default, "no cap".
	MemoryNoCap = unlimitedMemory
	// MemoryExpressBufferPoolMB is Express's buffer pool cap before SQL Server 2025 (Microsoft Learn,
	// "Editions and supported features of SQL Server 2022": maximum memory for buffer pool per instance).
	MemoryExpressBufferPoolMB = 1410
	// MemoryTopClerks is how many clerk types the fold under "Inside SQL Server" lists.
	MemoryTopClerks = 10

	// Database detail (decision 3).
	MemoryHot               = time.Hour      // read in the last hour
	MemoryWarm              = 24 * time.Hour // read in the last 24 hours; older is cold
	MemoryUsageReliable     = 24 * time.Hour // usage stats reset at a restart
	MemoryDetailRows        = 50             // top indexes by cached MB; the rest in one line
	MemoryMostlyCachedPct   = 90.0           // "probably scanned"
	MemoryMostlyCachedMinMB = 100.0
	MemoryWideIndexPct      = 70.0  // a nonclustered index this share of its table's clustered size
	MemoryWideIndexMinMB    = 100.0 // ... and at least this big: a small table's indexes are all "wide"
	MemoryHeapCachedMB      = 100.0
)

// Clerk groups of "Inside SQL Server" (decision 2.3).
const (
	ClerkDataCache = "Data cache"
	ClerkPlanCache = "Plan cache"
	ClerkLocks     = "Locks"
	ClerkWorkspace = "Query workspace"
	ClerkOther     = "Other"
	// ClerkFree is not a clerk: memory SQL Server has committed and not yet handed to one (the Memory
	// Manager's Free Memory counter). Without it the clerks fall short of the committed memory on a server
	// that is not full.
	ClerkFree = "Free inside SQL Server"
)

var clerkGroupOrder = []string{ClerkDataCache, ClerkPlanCache, ClerkLocks, ClerkWorkspace, ClerkOther}

// ClerkGroup names the group a memory clerk type belongs to.
func ClerkGroup(clerkType string) string {
	switch strings.ToUpper(strings.TrimSpace(clerkType)) {
	case "MEMORYCLERK_SQLBUFFERPOOL":
		return ClerkDataCache
	case "CACHESTORE_SQLCP", "CACHESTORE_OBJCP", "CACHESTORE_PHDR":
		return ClerkPlanCache
	case "OBJECTSTORE_LOCK_MANAGER":
		return ClerkLocks
	case "MEMORYCLERK_SQLQUERYEXEC":
		return ClerkWorkspace
	}
	return ClerkOther
}

// MemoryFacts is what collect reads for panels 2 and 3: instant reads, no buffer pool scan.
type MemoryFacts struct {
	ReadAt         time.Time `json:"readAt"` // the server's clock, UTC
	ProductVersion string    `json:"productVersion"`
	Edition        string    `json:"edition"`
	EngineEdition  int       `json:"engineEdition"`
	// Permission names the right the login lacks ("" when it has it); then nothing else is read.
	Permission string `json:"permission,omitempty"`

	TotalMB     int64  `json:"totalMB"`     // physical RAM
	AvailableMB int64  `json:"availableMB"` // free for Windows
	SystemState string `json:"systemState"` // system_memory_state_desc

	InUseMB        int64     `json:"inUseMB"`        // physical_memory_in_use_kb
	LockedMB       int64     `json:"lockedMB"`       // locked_page_allocations_kb
	ProcessPhysLow bool      `json:"processPhysLow"` // process_physical_memory_low
	ProcessVirtLow bool      `json:"processVirtLow"` // process_virtual_memory_low
	CommittedMB    int64     `json:"committedMB"`    // committed_kb
	TargetMB       int64     `json:"targetMB"`       // committed_target_kb
	MemoryModel    string    `json:"memoryModel"`    // sql_memory_model_desc; "" before 2016 SP1
	StartedAt      time.Time `json:"startedAt"`      // UTC

	MaxMB      int64 `json:"maxMB"`      // max server memory, value_in_use
	MaxValueMB int64 `json:"maxValueMB"` // max server memory, value
	MinMB      int64 `json:"minMB"`
	MinValueMB int64 `json:"minValueMB"`
	AdHocOn    bool  `json:"adHocOn"` // optimize for ad hoc workloads, value_in_use

	GrantsPending      int64       `json:"grantsPending"`
	GrantedWorkspaceMB int64       `json:"grantedWorkspaceMB"`
	FreeMB             int64       `json:"freeMB"` // the Memory Manager's Free Memory (KB), committed but unused
	NodePLE            []NodePLE   `json:"nodePLE"`
	CounterError       string      `json:"counterError,omitempty"`
	Clerks             []ClerkFact `json:"clerks"`
	SingleUseAdhocMB   float64     `json:"singleUseAdhocMB"`
	PlanCacheError     string      `json:"planCacheError,omitempty"`
}

// NodePLE is page life expectancy on one NUMA node (the Buffer Node counter).
type NodePLE struct {
	Node    string `json:"node"`
	Seconds int64  `json:"seconds"`
}

// ClerkFact is one memory clerk type: pages_kb plus virtual_memory_committed_kb (KB), and pages_kb alone
// (PagesKB), summed as bigint.
type ClerkFact struct {
	Type    string `json:"type"`
	KB      int64  `json:"kb"`
	PagesKB int64  `json:"pagesKB"`
}

// BufferFacts is the buffer pool by database (panel 4): one pass over sys.dm_os_buffer_descriptors grouped
// by database_id, and the data cache clerk read in the same batch, so the two can be reconciled.
type BufferFacts struct {
	ReadAt      time.Time         `json:"readAt"`
	Seconds     float64           `json:"seconds"`
	DataCacheKB int64             `json:"dataCacheKB"`
	Databases   []DatabaseBuffers `json:"databases"`
}

// DatabaseBuffers is one database's pages in the buffer pool.
type DatabaseBuffers struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Pages int64  `json:"pages"`
}

// MemorySignal is one red or amber finding of decision 6.
type MemorySignal struct {
	Level string `json:"level"` // red | amber
	Text  string `json:"text"`
}

// MemoryView is the Memory screen, panels 1 to 4, built from the facts.
type MemoryView struct {
	TakenAt    time.Time      `json:"takenAt"`
	Permission string         `json:"permission,omitempty"`
	Tone       string         `json:"tone"` // red | amber | green
	Verdict    string         `json:"verdict"`
	Signals    []MemorySignal `json:"signals"`
	Glance     MemoryGlance   `json:"glance"`
	Clerks     MemoryClerks   `json:"clerks"`
	Databases  *MemoryDBs     `json:"databases"` // nil when the cache was not read by database
	Handover   string         `json:"handover"`
}

// MemoryGlance is panel 2.
type MemoryGlance struct {
	TotalGB       float64   `json:"totalGB"`
	SQLGB         float64   `json:"sqlGB"`
	OtherGB       float64   `json:"otherGB"` // RAM minus SQL Server minus free: "everything else"
	FreeGB        float64   `json:"freeGB"`
	LockedGB      float64   `json:"lockedGB"`
	CommittedGB   float64   `json:"committedGB"`
	TargetGB      float64   `json:"targetGB"`
	Cap           string    `json:"cap"` // "no cap" | "48 GB"
	CapMB         int64     `json:"capMB"`
	Min           string    `json:"min"`
	StartedAt     time.Time `json:"startedAt"`
	Uptime        string    `json:"uptime"` // "3 days" | "5 hours"
	MemoryModel   string    `json:"memoryModel"`
	LockPages     bool      `json:"lockPages"`
	LockPagesNote string    `json:"lockPagesNote,omitempty"`
	WindowsState  string    `json:"windowsState"`
	NodePLE       []NodePLE `json:"nodePLE"`
	GrantsPending int64     `json:"grantsPending"`
	CounterError  string    `json:"counterError,omitempty"`
	Express       string    `json:"express,omitempty"` // the edition's cap, said once
	Filling       bool      `json:"filling"`
}

// MemoryClerks is panel 3.
type MemoryClerks struct {
	Groups             []MemoryClerkGroup `json:"groups"`
	Top                []MemoryClerk      `json:"top"`
	TotalGB            float64            `json:"totalGB"`
	SingleUseAdhocGB   float64            `json:"singleUseAdhocGB"`
	AdHocOn            bool               `json:"adHocOn"`
	PlanCacheError     string             `json:"planCacheError,omitempty"`
	GrantedWorkspaceGB float64            `json:"grantedWorkspaceGB"`
	Reconcile          string             `json:"reconcile,omitempty"` // the gap, when outside 5%
	ReconcileText      string             `json:"reconcileText"`       // the check in words, always
}

// MemoryClerkGroup is one line of panel 3.
type MemoryClerkGroup struct {
	Name  string  `json:"name"`
	GB    float64 `json:"gb"`
	Share float64 `json:"share"` // of all clerks, percent
}

// MemoryClerk is one clerk type in the fold.
type MemoryClerk struct {
	Type  string  `json:"type"`
	Group string  `json:"group"`
	GB    float64 `json:"gb"`
}

// MemoryDBs is panel 4.
type MemoryDBs struct {
	ReadAt        time.Time     `json:"readAt"`
	Seconds       float64       `json:"seconds"`
	Rows          []MemoryDBRow `json:"rows"`
	TotalGB       float64       `json:"totalGB"`
	DataCacheGB   float64       `json:"dataCacheGB"`
	Reconcile     string        `json:"reconcile,omitempty"`
	ReconcileText string        `json:"reconcileText"`
}

// MemoryDBRow is one database in panel 4.
type MemoryDBRow struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	GB    float64 `json:"gb"`
	Share float64 `json:"share"` // of the data cache read, percent
	Flag  string  `json:"flag,omitempty"`
	// FlagNote marks a flag that is a note, not a memory signal: it stays out of the verdict and is not amber.
	FlagNote bool `json:"flagNote,omitempty"`
	User     bool `json:"user"` // a user database: it opens Database detail
}

// gbOf is megabytes as gigabytes, to the megabyte (0.001 GB), so a small database or clerk is not shown as 0.
func gbOf(mb float64) float64 { return math.Round(mb/1024*1000) / 1000 }
func kbGB(kb int64) float64   { return gbOf(float64(kb) / 1024) }
func pagesMB(p int64) float64 { return float64(p) * 8 / 1024 }

// gbText writes a size as people say it: "97.9 GB", "850 MB" under 1 GB.
func gbText(mb float64) string {
	if mb < 1024 {
		return fmt.Sprintf("%.0f MB", mb)
	}
	return fmt.Sprintf("%.1f GB", mb/1024)
}

// shown rounds a size as gbText prints it.
func shown(mb float64) float64 {
	if mb < 1024 {
		return math.Round(mb)
	}
	return math.Round(mb/1024*10) / 10 * 1024
}

// gapText is the difference between two sizes as printed, so that the three numbers of a reconciliation
// line add up as read.
func gapText(a, b float64) string { return gbText(math.Abs(shown(a) - shown(b))) }

// uptimeText: "40 minutes", "5 hours", "3 days".
func uptimeText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return plural(int64(d/time.Minute), "minute", "minutes")
	case d < 48*time.Hour:
		return plural(int64(d/time.Hour), "hour", "hours")
	}
	return plural(int64(d/(24*time.Hour)), "day", "days")
}

// IsExpress says whether this is Express (EngineEdition 4).
func (f MemoryFacts) IsExpress() bool { return f.EngineEdition == 4 }

// testWords are the parts of a database name that make it look like a test copy (decision 6).
var testWords = map[string]bool{"test": true, "testing": true, "dev": true, "development": true, "uat": true, "qa": true,
	"training": true, "train": true, "copy": true, "old": true}

// LooksLikeTestDatabase says whether a database name looks like a test, dev, UAT, QA, training, copy or
// old database: one of those words as a part of the name (split at anything not a letter and where a
// lower-case letter meets a capital), or an all-capitals name ending in TEST, DEV, UAT or COPY.
func LooksLikeTestDatabase(name string) bool {
	for _, part := range nameParts(name) {
		if testWords[strings.ToLower(part)] {
			return true
		}
		if part == strings.ToUpper(part) {
			for _, suffix := range []string{"TEST", "DEV", "UAT", "COPY"} {
				if len(part) > len(suffix) && strings.HasSuffix(part, suffix) {
					return true
				}
			}
		}
	}
	return false
}

func nameParts(name string) []string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, string(cur))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		if !unicode.IsLetter(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(rs[i-1]) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return parts
}

// Reconcile says whether a breakdown adds up to the layer above it within MemoryReconcileShare.
func Reconcile(parts, whole float64) bool {
	if whole <= 0 {
		return parts <= 0
	}
	return math.Abs(parts-whole) <= whole*MemoryReconcileShare
}

// BuildMemory builds panels 1 to 4 from the facts. buffers is nil when the cache was not read by database.
func BuildMemory(f MemoryFacts, buffers *BufferFacts) MemoryView {
	v := MemoryView{TakenAt: f.ReadAt, Permission: f.Permission, Signals: []MemorySignal{}}
	if f.Permission != "" {
		v.Tone = "grey"
		v.Verdict = "kakapo cannot read SQL Server's memory: the login needs " + f.Permission + "."
		v.Handover = v.Verdict
		return v
	}
	v.Glance = buildGlance(f)
	v.Clerks = buildClerks(f)
	if buffers != nil {
		dbs := buildDBs(*buffers)
		v.Databases = &dbs
	}
	v.Signals = memorySignals(f, v)
	v.Tone, v.Verdict = memoryVerdict(v.Signals, v.Glance, f)
	v.Handover = memoryHandover(f, v)
	return v
}

func buildGlance(f MemoryFacts) MemoryGlance {
	g := MemoryGlance{
		TotalGB: gbOf(float64(f.TotalMB)), SQLGB: gbOf(float64(f.InUseMB)), FreeGB: gbOf(float64(f.AvailableMB)),
		LockedGB: gbOf(float64(f.LockedMB)), CommittedGB: gbOf(float64(f.CommittedMB)), TargetGB: gbOf(float64(f.TargetMB)),
		CapMB: f.MaxMB, StartedAt: f.StartedAt, MemoryModel: f.MemoryModel, WindowsState: f.SystemState,
		NodePLE: f.NodePLE, GrantsPending: f.GrantsPending, CounterError: f.CounterError,
	}
	if g.NodePLE == nil {
		g.NodePLE = []NodePLE{}
	}
	other := f.TotalMB - f.InUseMB - f.AvailableMB
	if other < 0 {
		other = 0
	}
	g.OtherGB = gbOf(float64(other))
	g.Cap = "no cap"
	if f.MaxMB < MemoryNoCap {
		g.Cap = gbText(float64(f.MaxMB))
	}
	g.Min = gbText(float64(f.MinMB))
	if !f.StartedAt.IsZero() && !f.ReadAt.IsZero() {
		g.Uptime = uptimeText(f.ReadAt.Sub(f.StartedAt))
	}
	g.LockPages = f.MemoryModel == "LOCK_PAGES" || f.LockedMB > 0
	if g.LockPages {
		g.LockPagesNote = "With Lock Pages in Memory on, Task Manager shows SQL Server far smaller than it is."
	}
	if f.IsExpress() {
		if EngineMajor(f.ProductVersion) >= 17 {
			g.Express = "This is SQL Server Express: the edition caps the memory it uses for data, whatever max server memory says, so \"no cap\" is not a worry here."
		} else {
			g.Express = fmt.Sprintf("This is SQL Server Express: the edition caps the buffer pool at %s MB per instance, whatever max server memory says, so \"no cap\" is not a worry here.", thousands(MemoryExpressBufferPoolMB))
		}
	}
	// Express never reaches its target: the edition caps the buffer pool below it, so it is never "filling".
	g.Filling = !f.IsExpress() && f.TargetMB > 0 && float64(f.CommittedMB) < float64(f.TargetMB)*MemoryFillingShare
	return g
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func buildClerks(f MemoryFacts) MemoryClerks {
	c := MemoryClerks{AdHocOn: f.AdHocOn, SingleUseAdhocGB: gbOf(f.SingleUseAdhocMB), PlanCacheError: f.PlanCacheError,
		GrantedWorkspaceGB: gbOf(float64(f.GrantedWorkspaceMB)), Top: []MemoryClerk{}}
	byGroup := map[string]int64{}
	var total int64
	clerks := append([]ClerkFact(nil), f.Clerks...)
	sort.SliceStable(clerks, func(i, j int) bool { return clerks[i].KB > clerks[j].KB })
	for _, k := range clerks {
		// The data cache is its pages, the number the databases add up to; the buffer pool clerk's
		// committed virtual memory is overhead and counts under Other.
		if g := ClerkGroup(k.Type); g == ClerkDataCache && k.PagesKB > 0 && k.PagesKB <= k.KB {
			byGroup[g] += k.PagesKB
			byGroup[ClerkOther] += k.KB - k.PagesKB
		} else {
			byGroup[g] += k.KB
		}
		total += k.KB
		if len(c.Top) < MemoryTopClerks && k.KB > 0 {
			c.Top = append(c.Top, MemoryClerk{Type: k.Type, Group: ClerkGroup(k.Type), GB: kbGB(k.KB)})
		}
	}
	names := clerkGroupOrder
	if f.CounterError == "" {
		byGroup[ClerkFree] = f.FreeMB * 1024
		total += f.FreeMB * 1024
		names = append(append([]string(nil), names...), ClerkFree)
	}
	c.TotalGB = kbGB(total)
	for _, name := range names {
		kb := byGroup[name]
		share := 0.0
		if total > 0 {
			share = math.Round(float64(kb)/float64(total)*1000) / 10
		}
		c.Groups = append(c.Groups, MemoryClerkGroup{Name: name, GB: kbGB(kb), Share: share})
	}
	committedKB := f.CommittedMB * 1024
	totalMB, committedMB := float64(total)/1024, float64(f.CommittedMB)
	if Reconcile(float64(total), float64(committedKB)) {
		c.ReconcileText = fmt.Sprintf("These add up to %s, within %d%% of SQL Server's committed memory (%s).",
			gbText(totalMB), int(MemoryReconcileShare*100), gbText(committedMB))
	} else {
		c.Reconcile = fmt.Sprintf("These add up to %s; SQL Server's committed memory is %s; %s unaccounted.",
			gbText(totalMB), gbText(committedMB), gapText(committedMB, totalMB))
		c.ReconcileText = c.Reconcile
	}
	return c
}

func buildDBs(b BufferFacts) MemoryDBs {
	d := MemoryDBs{ReadAt: b.ReadAt, Seconds: b.Seconds, DataCacheGB: kbGB(b.DataCacheKB), Rows: []MemoryDBRow{}}
	var pages int64
	for _, x := range b.Databases {
		pages += x.Pages
	}
	rows := append([]DatabaseBuffers(nil), b.Databases...)
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Pages != rows[j].Pages {
			return rows[i].Pages > rows[j].Pages
		}
		return rows[i].Name < rows[j].Name
	})
	dataMB := float64(b.DataCacheKB) / 1024
	for _, x := range rows {
		mb := pagesMB(x.Pages)
		share := 0.0
		if pages > 0 {
			share = math.Round(float64(x.Pages)/float64(pages)*1000) / 10
		}
		r := MemoryDBRow{ID: x.ID, Name: x.Name, GB: gbOf(mb), Share: share, User: x.ID > 4 && x.ID != 32767}
		switch {
		case x.ID == 2 && mb > MemoryTempdbAmberMB && mb > dataMB*MemoryTempdbShare:
			r.Flag = fmt.Sprintf("tempdb holds %s of the data cache: check the version store, temporary tables and spills.", gbText(mb))
		case r.User && mb > MemoryTestDatabaseMB && LooksLikeTestDatabase(x.Name):
			r.Flag = "Looks like a test database. Not a memory problem: its cache is reused when other databases need it. Whether it belongs on this instance is a question for other reasons."
			r.FlagNote = true
		}
		d.Rows = append(d.Rows, r)
	}
	totalMB := pagesMB(pages)
	d.TotalGB = gbOf(totalMB)
	if Reconcile(totalMB, dataMB) {
		d.ReconcileText = fmt.Sprintf("The databases add up to %s, within %d%% of the data cache clerk (%s).",
			gbText(totalMB), int(MemoryReconcileShare*100), gbText(dataMB))
	} else {
		d.Reconcile = fmt.Sprintf("Databases add up to %s; the data cache clerk says %s; %s unaccounted.",
			gbText(totalMB), gbText(dataMB), gapText(dataMB, totalMB))
		d.ReconcileText = d.Reconcile
	}
	return d
}

// memorySignals are decision 6's red and amber signals, red first.
func memorySignals(f MemoryFacts, v MemoryView) []MemorySignal {
	var red, amber []MemorySignal
	r := func(s string, a ...any) { red = append(red, MemorySignal{"red", fmt.Sprintf(s, a...)}) }
	a := func(s string, x ...any) { amber = append(amber, MemorySignal{"amber", fmt.Sprintf(s, x...)}) }

	stateLow := strings.Contains(strings.ToLower(f.SystemState), "low")
	switch {
	case f.TotalMB > 0 && f.AvailableMB < MemoryWindowsLowMB:
		r("Windows has only %s free", gbText(float64(f.AvailableMB)))
	case stateLow:
		r("Windows reports its memory is low")
	}
	if f.ProcessPhysLow {
		r("Windows is telling SQL Server to give memory back")
	}
	if f.GrantsPending > 0 {
		r("%s waiting for memory to start", plural(f.GrantsPending, "query is", "queries are"))
	}

	switch {
	case f.MaxMB >= MemoryNoCap && !f.IsExpress():
		a("SQL Server has no memory cap (max server memory is still the default)")
	case f.MaxMB < MemoryNoCap && f.TotalMB > 0 && f.MaxMB > f.TotalMB:
		a("the memory cap (%s) is above the RAM in the machine (%s)", gbText(float64(f.MaxMB)), gbText(float64(f.TotalMB)))
	case f.MaxMB < MemoryNoCap && f.TotalMB > 0 && !f.IsExpress():
		if s := capHeadroom(f); s != "" {
			a("%s", s)
		}
	}
	if f.MaxValueMB != f.MaxMB {
		a("max server memory was set to %s but not applied (RECONFIGURE was not run)", capWords(f.MaxValueMB))
	}
	// min server memory's default shows as 0 configured and 16 MB in use: SQL Server's own floor, not a change.
	if f.MinValueMB != f.MinMB && !(f.MinValueMB == 0 && f.MinMB <= 16) {
		a("min server memory was set to %s but not applied (RECONFIGURE was not run)", gbText(float64(f.MinValueMB)))
	}
	for _, g := range v.Clerks.Groups {
		if g.Name == ClerkLocks && g.GB*1024 > MemoryLockAmberMB {
			a("lock memory is %s: find the session or transaction holding the locks", gbText(g.GB*1024))
		}
	}
	if f.PlanCacheError == "" && f.SingleUseAdhocMB > MemoryAdhocAmberMB && !f.AdHocOn {
		a("single-use ad hoc plans take %s and optimize for ad hoc workloads is off", gbText(f.SingleUseAdhocMB))
	}
	if v.Databases != nil {
		for _, d := range v.Databases.Rows {
			switch {
			case d.Flag == "" || d.FlagNote:
			case d.ID == 2:
				a("tempdb holds %s of the data cache", gbText(d.GB*1024))
			default:
				a("%s looks like a test database and holds %s of the cache", d.Name, gbText(d.GB*1024))
			}
		}
	}
	return append(red, amber...)
}

// capHeadroom is the amber sentence when the cap leaves Windows too little room once SQL Server reaches it,
// with everything else on the machine using what it uses now; "" when the cap leaves enough. A cap of RAM
// minus 10% (the Setup card's suggestion) is not enough when other programs run on the machine: this read
// sees them. The suggested cap is SuggestedCapMB, the one the Setup card's fix applies.
func capHeadroom(f MemoryFacts) string {
	other := f.TotalMB - f.InUseMB - f.AvailableMB
	if other < 0 {
		other = 0
	}
	left := f.TotalMB - f.MaxMB - other
	if left >= MemoryCapHeadroomMB {
		return ""
	}
	suggest := SuggestedCapMB(f.TotalMB, other) // the Setup card's max-server-memory fix gives the same number
	var room string
	if left < 0 {
		room = fmt.Sprintf("would need %s more than the RAM", gbText(float64(-left)))
	} else {
		room = fmt.Sprintf("would leave Windows only %s", gbText(float64(left)))
	}
	s := fmt.Sprintf("the memory cap (%s) leaves too little room: everything else on the machine uses %s, so SQL Server at its cap %s",
		gbText(float64(f.MaxMB)), gbText(float64(other)), room)
	if suggest > 0 && suggest < f.MaxMB {
		s += fmt.Sprintf("; a cap of about %d GB (%d MB, the Setup card's Apply) leaves room for both", suggest/1024, suggest)
	}
	return s
}

func capWords(mb int64) string {
	if mb >= MemoryNoCap {
		return "no cap"
	}
	return gbText(float64(mb))
}

// memoryVerdict is the one sentence of decision 6.
func memoryVerdict(signals []MemorySignal, g MemoryGlance, f MemoryFacts) (tone, verdict string) {
	var red, amber []string
	for _, s := range signals {
		if s.Level == "red" {
			red = append(red, s.Text)
		} else {
			amber = append(amber, s.Text)
		}
	}
	if len(red) > 0 {
		return "red", "Under pressure: " + strings.Join(red, "; ") + "."
	}
	state := "Full but healthy"
	if g.Filling {
		state = "Still filling: started " + g.Uptime + " ago"
		if g.Uptime == "" {
			state = "Still filling"
		}
	}
	if len(amber) > 0 {
		if g.Filling {
			return "amber", state + ", with things worth a look: " + strings.Join(amber, "; ") + "."
		}
		return "amber", state + ", with things worth a look: " + strings.Join(amber, "; ") + "."
	}
	if g.Filling {
		return "green", state + ", and nothing is short of memory."
	}
	return "green", "Full but healthy: SQL Server is caching data and nothing is short of memory."
}

// memoryHandover is the Copy text (decision 9): verdict, the numbers at a glance, the clerk groups, the top
// 10 databases and the reconciliation, as plain text for a support ticket.
func memoryHandover(f MemoryFacts, v MemoryView) string {
	var b strings.Builder
	g := v.Glance
	fmt.Fprintf(&b, "SQL Server memory, read %s UTC\n\n%s\n\n", v.TakenAt.UTC().Format("2006-01-02 15:04"), v.Verdict)
	b.WriteString("At a glance\n")
	fmt.Fprintf(&b, "- RAM %.1f GB: SQL Server %.1f GB, everything else %.1f GB, free %.1f GB\n", g.TotalGB, g.SQLGB, g.OtherGB, g.FreeGB)
	fmt.Fprintf(&b, "- SQL Server committed %.1f GB, target %.1f GB, cap %s, min %s\n", g.CommittedGB, g.TargetGB, g.Cap, g.Min)
	if g.LockedGB > 0 {
		fmt.Fprintf(&b, "- Locked pages %.1f GB\n", g.LockedGB)
	}
	if g.Uptime != "" {
		fmt.Fprintf(&b, "- Up %s (started %s UTC)\n", g.Uptime, g.StartedAt.UTC().Format("2006-01-02 15:04"))
	}
	if g.MemoryModel != "" {
		fmt.Fprintf(&b, "- Memory model %s\n", g.MemoryModel)
	}
	for _, n := range g.NodePLE {
		fmt.Fprintf(&b, "- Page life expectancy, node %s: %s s\n", n.Node, thousands(n.Seconds))
	}
	fmt.Fprintf(&b, "- Memory grants pending %d\n", g.GrantsPending)
	if g.Express != "" {
		fmt.Fprintf(&b, "- %s\n", g.Express)
	}
	b.WriteString("\nInside SQL Server\n")
	for _, c := range v.Clerks.Groups {
		fmt.Fprintf(&b, "- %s %.2f GB (%.1f%%)\n", c.Name, c.GB, c.Share)
	}
	fmt.Fprintf(&b, "- Single-use ad hoc plans %.2f GB; optimize for ad hoc workloads %s\n", v.Clerks.SingleUseAdhocGB, onOffWord(v.Clerks.AdHocOn))
	fmt.Fprintf(&b, "- %s\n", v.Clerks.ReconcileText)
	if d := v.Databases; d != nil {
		fmt.Fprintf(&b, "\nDatabases in cache, read %s UTC\n", d.ReadAt.UTC().Format("2006-01-02 15:04"))
		for i, r := range d.Rows {
			if i == 10 {
				fmt.Fprintf(&b, "- and %d more\n", len(d.Rows)-10)
				break
			}
			line := fmt.Sprintf("- %s %.2f GB (%.1f%%)", r.Name, r.GB, r.Share)
			if r.Flag != "" {
				line += ": " + r.Flag
			}
			b.WriteString(line + "\n")
		}
		fmt.Fprintf(&b, "- %s\n", d.ReconcileText)
	}
	return strings.TrimRight(b.String(), "\n")
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// MemorySnapshotInfo is one line of the list of saved snapshots.
type MemorySnapshotInfo struct {
	ID        int64     `json:"id"`
	TakenAt   time.Time `json:"takenAt"`
	Kind      string    `json:"kind"` // instance | database
	Database  string    `json:"database,omitempty"`
	Databases bool      `json:"databases"` // an instance snapshot that holds panel 4
}

// MemorySnapshot is the instance snapshot as stored: the facts read, so that a saved snapshot is shown
// with the same code as a fresh one.
type MemorySnapshot struct {
	Facts   MemoryFacts  `json:"facts"`
	Buffers *BufferFacts `json:"buffers,omitempty"`
}

// MemoryPage is what the screen gets: the view (live panels 2 and 3, plus panel 4 of the latest snapshot
// when there is one), which snapshot it shows, and the list of saved snapshots.
type MemoryPage struct {
	View       MemoryView           `json:"view"`
	SnapshotID int64                `json:"snapshotId"` // the snapshot the view shows; 0: panels 2 and 3 are live
	Live       bool                 `json:"live"`       // panels 2 and 3 were read now
	Snapshots  []MemorySnapshotInfo `json:"snapshots"`
	ServerName string               `json:"serverName"`
}
