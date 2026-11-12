package domain

import (
	"strings"
	"testing"
	"time"
)

var memNow = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// healthyMemory is a 128 GB server with a 112 GB cap, full and quiet.
func healthyMemory() MemoryFacts {
	return MemoryFacts{
		ReadAt: memNow, ProductVersion: "16.0.4135.4", Edition: "Standard Edition (64-bit)", EngineEdition: 2,
		TotalMB: 131072, AvailableMB: 12000, SystemState: "Available physical memory is high",
		InUseMB: 114000, CommittedMB: 114688, TargetMB: 114688, MemoryModel: "CONVENTIONAL",
		StartedAt: memNow.Add(-10 * 24 * time.Hour), MaxMB: 114688, MaxValueMB: 114688, MinMB: 0, MinValueMB: 0,
		NodePLE: []NodePLE{{"000", 40000}, {"001", 38000}},
		Clerks: []ClerkFact{
			{"MEMORYCLERK_SQLBUFFERPOOL", 100 * 1024 * 1024, 99 * 1024 * 1024}, // 1 GB of it committed virtual memory
			{"CACHESTORE_SQLCP", 6 * 1024 * 1024, 0},
			{"CACHESTORE_OBJCP", 1 * 1024 * 1024, 0},
			{"CACHESTORE_PHDR", 512 * 1024, 0},
			{"OBJECTSTORE_LOCK_MANAGER", 300 * 1024, 0},
			{"MEMORYCLERK_SQLQUERYEXEC", 2 * 1024 * 1024, 0},
			{"MEMORYCLERK_SQLGENERAL", 2 * 1024 * 1024, 0},
			{"USERSTORE_TOKENPERM", 1 * 1024 * 1024, 0},
		},
		SingleUseAdhocMB: 300,
	}
}

func TestClerkGroup(t *testing.T) {
	for typ, want := range map[string]string{
		"MEMORYCLERK_SQLBUFFERPOOL": ClerkDataCache, "CACHESTORE_SQLCP": ClerkPlanCache, "CACHESTORE_OBJCP": ClerkPlanCache,
		"CACHESTORE_PHDR": ClerkPlanCache, "OBJECTSTORE_LOCK_MANAGER": ClerkLocks, "MEMORYCLERK_SQLQUERYEXEC": ClerkWorkspace,
		"MEMORYCLERK_SQLGENERAL": ClerkOther, "cachestore_sqlcp": ClerkPlanCache, "": ClerkOther,
	} {
		if got := ClerkGroup(typ); got != want {
			t.Errorf("ClerkGroup(%q) = %q, want %q", typ, got, want)
		}
	}
}

func TestBuildMemoryHealthy(t *testing.T) {
	v := BuildMemory(healthyMemory(), nil)
	if v.Tone != "green" || v.Verdict != "Full but healthy: SQL Server is caching data and nothing is short of memory." {
		t.Fatalf("verdict %q (%s)", v.Verdict, v.Tone)
	}
	if len(v.Signals) != 0 {
		t.Errorf("signals %v", v.Signals)
	}
	g := v.Glance
	if g.TotalGB != 128 || g.Cap != "112.0 GB" || g.Uptime != "10 days" || g.LockPages || g.Filling {
		t.Errorf("glance %+v", g)
	}
	// everything else = RAM - SQL Server - free
	if want := gbOf(131072 - 114000 - 12000); g.OtherGB != want {
		t.Errorf("other %v, want %v", g.OtherGB, want)
	}
	groups := map[string]MemoryClerkGroup{}
	for _, c := range v.Clerks.Groups {
		groups[c.Name] = c
	}
	if len(v.Clerks.Groups) != 6 || v.Clerks.Groups[0].Name != ClerkDataCache || v.Clerks.Groups[4].Name != ClerkOther || v.Clerks.Groups[5].Name != ClerkFree {
		t.Fatalf("group order %+v", v.Clerks.Groups)
	}
	// The data cache is the clerk's pages; its virtual memory counts under Other.
	if groups[ClerkDataCache].GB != 99 || groups[ClerkPlanCache].GB != 7.5 || groups[ClerkWorkspace].GB != 2 || groups[ClerkOther].GB != 4 {
		t.Errorf("groups %+v", groups)
	}
	if len(v.Clerks.Top) != 8 || v.Clerks.Top[0].Type != "MEMORYCLERK_SQLBUFFERPOOL" {
		t.Errorf("top %+v", v.Clerks.Top)
	}
	// 113.8 GB of clerks against 112 GB committed: within 5%, no gap line.
	if v.Clerks.Reconcile != "" || !strings.Contains(v.Clerks.ReconcileText, "within 5%") {
		t.Errorf("reconcile %q / %q", v.Clerks.Reconcile, v.Clerks.ReconcileText)
	}
	for _, s := range []string{"Full but healthy", "Data cache 99.00 GB", "Page life expectancy, node 001", "Single-use ad hoc plans"} {
		if !strings.Contains(v.Handover, s) {
			t.Errorf("handover lacks %q:\n%s", s, v.Handover)
		}
	}
}

func TestBuildMemoryRed(t *testing.T) {
	f := healthyMemory()
	f.AvailableMB = 1500
	f.ProcessPhysLow = true
	f.GrantsPending = 3
	f.MaxMB, f.MaxValueMB = MemoryNoCap, MemoryNoCap // amber, but red wins the sentence
	v := BuildMemory(f, nil)
	if v.Tone != "red" {
		t.Fatalf("tone %s", v.Tone)
	}
	want := "Under pressure: Windows has only 1.5 GB free; Windows is telling SQL Server to give memory back; 3 queries are waiting for memory to start."
	if v.Verdict != want {
		t.Errorf("verdict\n got %q\nwant %q", v.Verdict, want)
	}
	if v.Signals[len(v.Signals)-1].Level != "amber" {
		t.Errorf("the amber signal is still listed after the red ones: %+v", v.Signals)
	}

	// The state "low" alone is red too.
	f = healthyMemory()
	f.SystemState = "Available physical memory is low"
	if v := BuildMemory(f, nil); v.Tone != "red" || !strings.Contains(v.Verdict, "Windows reports its memory is low") {
		t.Errorf("low state: %q", v.Verdict)
	}
	// One grant pending says "query is".
	f = healthyMemory()
	f.GrantsPending = 1
	if v := BuildMemory(f, nil); !strings.Contains(v.Verdict, "1 query is waiting") {
		t.Errorf("one grant: %q", v.Verdict)
	}
}

func TestBuildMemoryAmber(t *testing.T) {
	cases := []struct {
		name string
		edit func(*MemoryFacts)
		want string
	}{
		{"no cap", func(f *MemoryFacts) { f.MaxMB, f.MaxValueMB = MemoryNoCap, MemoryNoCap }, "SQL Server has no memory cap"},
		{"cap above RAM", func(f *MemoryFacts) { f.MaxMB, f.MaxValueMB = 200000, 200000 }, "the memory cap (195.3 GB) is above the RAM in the machine (128.0 GB)"},
		// The cap leaves under 4 GB once SQL Server reaches it: 128 GB - 121.0 GB cap - 5.0 GB everything else.
		{"cap headroom", func(f *MemoryFacts) { f.MaxMB, f.MaxValueMB = 123952, 123952 },
			"the memory cap (121.0 GB) leaves too little room: everything else on the machine uses 5.0 GB, so SQL Server at its cap would leave Windows only 2.0 GB; a cap of about 115 GB (117965 MB, the Setup card's Apply) leaves room for both"},
		{"max pending", func(f *MemoryFacts) { f.MaxValueMB = 100000 }, "max server memory was set to 97.7 GB but not applied"},
		{"min pending", func(f *MemoryFacts) { f.MinValueMB = 4096 }, "min server memory was set to 4.0 GB but not applied"},
		{"lock memory", func(f *MemoryFacts) {
			f.Clerks = append(f.Clerks, ClerkFact{"OBJECTSTORE_LOCK_MANAGER", 1100 * 1024, 0})
		}, "lock memory is 1.4 GB"},
		{"ad hoc", func(f *MemoryFacts) { f.SingleUseAdhocMB = 1500 }, "single-use ad hoc plans take 1.5 GB and optimize for ad hoc workloads is off"},
	}
	for _, c := range cases {
		f := healthyMemory()
		c.edit(&f)
		v := BuildMemory(f, nil)
		if v.Tone != "amber" || !strings.HasPrefix(v.Verdict, "Full but healthy, with things worth a look: ") || !strings.Contains(v.Verdict, c.want) {
			t.Errorf("%s: %q (%s)", c.name, v.Verdict, v.Tone)
		}
	}

	// min server memory's default reads 0 configured and 16 MB in use: not a pending change.
	f0 := healthyMemory()
	f0.MinValueMB, f0.MinMB = 0, 16
	if v := BuildMemory(f0, nil); v.Tone != "green" {
		t.Errorf("min 0/16: %q", v.Verdict)
	}
	// Under the thresholds: lock memory of exactly 1 GB, ad hoc plans over 1 GB with the option on.
	f := healthyMemory()
	f.Clerks[4] = ClerkFact{"OBJECTSTORE_LOCK_MANAGER", 1024 * 1024, 0}
	f.SingleUseAdhocMB, f.AdHocOn = 5000, true
	if v := BuildMemory(f, nil); v.Tone != "green" {
		t.Errorf("at the thresholds: %q", v.Verdict)
	}
	// A plan cache that could not be read raises nothing about it.
	f = healthyMemory()
	f.SingleUseAdhocMB, f.PlanCacheError = 5000, "denied"
	if v := BuildMemory(f, nil); v.Tone != "green" {
		t.Errorf("plan cache error: %q", v.Verdict)
	}
}

// The production case of 2026-11-12: 127.5 GB of RAM, a cap of RAM minus 10% (the Setup card's own
// suggestion), and 22.4 GB used by other programs. Nothing is short of memory now, but the cap no longer fits.
func TestBuildMemoryCapHeadroom(t *testing.T) {
	f := healthyMemory()
	f.TotalMB, f.InUseMB, f.AvailableMB = 130560, 102195, 5427
	f.MaxMB, f.MaxValueMB = SuggestedMaxMemoryMB(130560), SuggestedMaxMemoryMB(130560)
	f.CommittedMB, f.TargetMB = 102400, 104960
	v := BuildMemory(f, nil)
	want := "Full but healthy, with things worth a look: the memory cap (114.8 GB) leaves too little room: everything else on " +
		"the machine uses 22.4 GB, so SQL Server at its cap would need 9.7 GB more than the RAM; a cap of about 101 GB (103424 MB, the Setup card's Apply) leaves room for both."
	if v.Tone != "amber" || v.Verdict != want {
		t.Errorf("verdict\n got %q\nwant %q", v.Verdict, want)
	}
	// The healthy server's cap leaves 11 GB: nothing said. Express is capped by its edition: nothing said.
	if v := BuildMemory(healthyMemory(), nil); v.Tone != "green" {
		t.Errorf("healthy: %q", v.Verdict)
	}
	f.EngineEdition = 4
	if v := BuildMemory(f, nil); strings.Contains(v.Verdict, "leaves too little room") {
		t.Errorf("express: %q", v.Verdict)
	}
}

func TestBuildMemoryExpress(t *testing.T) {
	f := healthyMemory()
	f.EngineEdition, f.Edition, f.ProductVersion = 4, "Express Edition (64-bit)", "16.0.1000.6"
	f.MaxMB, f.MaxValueMB = MemoryNoCap, MemoryNoCap
	v := BuildMemory(f, nil)
	if v.Tone != "green" {
		t.Errorf("no cap on Express is not a worry: %q", v.Verdict)
	}
	if !strings.Contains(v.Glance.Express, "1,410 MB") || v.Glance.Cap != "no cap" {
		t.Errorf("express %q cap %q", v.Glance.Express, v.Glance.Cap)
	}
	// Express never reaches its target, so it is not "still filling".
	f.CommittedMB = 1000
	if v := BuildMemory(f, nil); v.Glance.Filling || !strings.HasPrefix(v.Verdict, "Full but healthy") {
		t.Errorf("express filling: %q", v.Verdict)
	}
	f.ProductVersion = "17.0.1000.7"
	if v := BuildMemory(f, nil); strings.Contains(v.Glance.Express, "1,410") || v.Glance.Express == "" {
		t.Errorf("2025 express %q", v.Glance.Express)
	}
}

func TestBuildMemoryFillingAndLockPages(t *testing.T) {
	f := healthyMemory()
	f.StartedAt = memNow.Add(-3 * time.Hour)
	f.CommittedMB = 40000
	v := BuildMemory(f, nil)
	if !v.Glance.Filling || v.Verdict != "Still filling: started 3 hours ago, and nothing is short of memory." {
		t.Errorf("filling: %q", v.Verdict)
	}
	f.MaxMB, f.MaxValueMB = MemoryNoCap, MemoryNoCap
	if v := BuildMemory(f, nil); v.Verdict != "Still filling: started 3 hours ago, with things worth a look: SQL Server has no memory cap (max server memory is still the default)." {
		t.Errorf("filling amber: %q", v.Verdict)
	}
	// At 80% of the target it is full.
	f = healthyMemory()
	f.CommittedMB = (f.TargetMB*8 + 9) / 10
	if v := BuildMemory(f, nil); v.Glance.Filling {
		t.Errorf("80%% of target is not filling")
	}

	f = healthyMemory()
	f.MemoryModel, f.LockedMB = "LOCK_PAGES", 110000
	v = BuildMemory(f, nil)
	if !v.Glance.LockPages || !strings.Contains(v.Glance.LockPagesNote, "Task Manager shows SQL Server far smaller") {
		t.Errorf("lock pages %+v", v.Glance)
	}
}

func TestBuildMemoryPermission(t *testing.T) {
	v := BuildMemory(MemoryFacts{ReadAt: memNow, Permission: "VIEW SERVER STATE"}, &BufferFacts{})
	if v.Verdict != "kakapo cannot read SQL Server's memory: the login needs VIEW SERVER STATE." || v.Databases != nil || len(v.Clerks.Groups) != 0 {
		t.Errorf("permission: %+v", v)
	}
}

// Free memory inside SQL Server closes the gap between the clerks and the committed memory.
func TestBuildMemoryFreeReconciles(t *testing.T) {
	f := healthyMemory()
	f.CommittedMB = 130000 // the clerks are 112.8 GB: 13% short
	if v := BuildMemory(f, nil); !strings.HasPrefix(v.Clerks.Reconcile, "These add up to 112.8 GB; SQL Server's committed memory is 127.0 GB") {
		t.Errorf("without free: %q", v.Clerks.Reconcile)
	}
	f.FreeMB = 13000
	if v := BuildMemory(f, nil); v.Clerks.Reconcile != "" || v.Clerks.Groups[5].GB != gbOf(13000) {
		t.Errorf("with free: %q %+v", v.Clerks.Reconcile, v.Clerks.Groups)
	}
	f.CounterError = "denied" // no counter, no free line
	if v := BuildMemory(f, nil); len(v.Clerks.Groups) != 5 {
		t.Errorf("no counter: %+v", v.Clerks.Groups)
	}
}

func TestReconcile(t *testing.T) {
	for _, c := range []struct {
		parts, whole float64
		want         bool
	}{{95, 100, true}, {105, 100, true}, {94.9, 100, false}, {105.1, 100, false}, {0, 0, true}, {1, 0, false}} {
		if got := Reconcile(c.parts, c.whole); got != c.want {
			t.Errorf("Reconcile(%v, %v) = %v", c.parts, c.whole, got)
		}
	}
}

func TestBuildMemoryDatabases(t *testing.T) {
	gbPages := int64(1024 * 1024 / 8) // pages in 1 GB
	b := &BufferFacts{ReadAt: memNow, Seconds: 12, DataCacheKB: 79 * 1024 * 1024, Databases: []DatabaseBuffers{
		{5, "GraniteLive", 60 * gbPages},
		{2, "tempdb", 9 * gbPages},
		{6, "GraniteLive_Test", 4 * gbPages},
		{7, "Training", gbPages / 2}, // under 1 GB: no flag
		{1, "master", gbPages / 100},
		{32767, "(resource database)", gbPages / 50},
	}}
	v := BuildMemory(healthyMemory(), b)
	d := v.Databases
	if d == nil || len(d.Rows) != 6 || d.Rows[0].Name != "GraniteLive" || d.Rows[1].Name != "tempdb" {
		t.Fatalf("rows %+v", d)
	}
	if !d.Rows[0].User || d.Rows[1].User || d.Rows[4].User || d.Rows[5].User {
		t.Errorf("only user databases open detail: %+v", d.Rows)
	}
	if !strings.HasPrefix(d.Rows[1].Flag, "tempdb holds 9.0 GB") || d.Rows[1].FlagNote ||
		!strings.HasPrefix(d.Rows[2].Flag, "Looks like a test database. Not a memory problem") || !d.Rows[2].FlagNote || d.Rows[3].Flag != "" {
		t.Errorf("flags %q / %q / %q", d.Rows[1].Flag, d.Rows[2].Flag, d.Rows[3].Flag)
	}
	// 73.5 GB of databases against a 79 GB clerk: outside 5%, so the gap is said.
	if d.Reconcile != "Databases add up to 73.5 GB; the data cache clerk says 79.0 GB; 5.5 GB unaccounted." {
		t.Errorf("reconcile %q", d.Reconcile)
	}
	// A test database is a note on its row, not a memory signal: it stays out of the verdict.
	if v.Tone != "amber" || !strings.Contains(v.Verdict, "tempdb holds 9.0 GB of the data cache") ||
		strings.Contains(v.Verdict, "GraniteLive_Test") {
		t.Errorf("verdict %q", v.Verdict)
	}
	if !strings.Contains(v.Handover, "Databases in cache") || !strings.Contains(v.Handover, "5.5 GB unaccounted") {
		t.Errorf("handover:\n%s", v.Handover)
	}

	// Within 5%: the check is said, no gap line. tempdb at 2 GB or under 10% of the cache is not flagged.
	b = &BufferFacts{ReadAt: memNow, DataCacheKB: 100 * 1024 * 1024, Databases: []DatabaseBuffers{
		{5, "Sales", 88 * gbPages}, {2, "tempdb", 9 * gbPages}}}
	d = BuildMemory(healthyMemory(), b).Databases
	if d.Reconcile != "" || !strings.Contains(d.ReconcileText, "within 5%") || d.Rows[1].Flag != "" {
		t.Errorf("within: %q / %q / %q", d.Reconcile, d.ReconcileText, d.Rows[1].Flag)
	}
}

func TestLooksLikeTestDatabase(t *testing.T) {
	yes := []string{"GraniteLive_Test", "TestDB", "Sales_DEV", "UAT", "qa-copy", "Training", "SalesCopy", "Old_Sales",
		"SALESTEST", "Sales Dev 2", "dev2", "GraniteUAT"}
	no := []string{"GraniteLive", "Golden", "Household", "Contest", "Development2Go"[:0] + "Devonport", "Latest", "Sales", "QAReports"[:0] + "Quality"}
	for _, n := range yes {
		if !LooksLikeTestDatabase(n) {
			t.Errorf("%q should look like a test database", n)
		}
	}
	for _, n := range no {
		if LooksLikeTestDatabase(n) {
			t.Errorf("%q should not look like a test database", n)
		}
	}
}

func TestTemperature(t *testing.T) {
	at := func(d time.Duration) *time.Time { x := memNow.Add(-d); return &x }
	for _, c := range []struct {
		last *time.Time
		want string
	}{{at(time.Minute), "hot"}, {at(time.Hour), "hot"}, {at(time.Hour + time.Second), "warm"}, {at(24 * time.Hour), "warm"},
		{at(25 * time.Hour), "cold"}, {nil, "cold"}} {
		if got := Temperature(c.last, memNow); got != c.want {
			t.Errorf("Temperature(%v) = %s, want %s", c.last, got, c.want)
		}
	}
}

func TestIndexCacheFlags(t *testing.T) {
	mb := func(n float64) int64 { return int64(n * 1024 / 8) }
	has := func(flags []string, s string) bool {
		for _, f := range flags {
			if strings.Contains(f, s) {
				return true
			}
		}
		return false
	}
	// Mostly cached: 90% of an index of 100 MB or more.
	if f := IndexCacheFlags(IndexCacheFact{Type: "CLUSTERED", CachedPages: mb(90), TotalPages: mb(100)}); !has(f, "probably scanned") {
		t.Errorf("90%% of 100 MB: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "CLUSTERED", CachedPages: mb(89), TotalPages: mb(100)}); has(f, "probably scanned") {
		t.Errorf("89%%: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "CLUSTERED", CachedPages: mb(99), TotalPages: mb(99)}); has(f, "probably scanned") {
		t.Errorf("under 100 MB: %v", f)
	}
	// A nonclustered index 70% of its table.
	if f := IndexCacheFlags(IndexCacheFact{Type: "NONCLUSTERED", TotalPages: mb(700), TablePages: mb(1000), Includes: "a, b"}); !has(f, "70% of its table's size: a wide INCLUDE list") {
		t.Errorf("wide: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "NONCLUSTERED", TotalPages: mb(700), TablePages: mb(1000)}); !has(f, "close to a second copy of the table") {
		t.Errorf("wide keys: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "NONCLUSTERED", TotalPages: mb(690), TablePages: mb(1000), Includes: "a"}); len(f) != 0 {
		t.Errorf("69%%: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "NONCLUSTERED", TotalPages: mb(99), TablePages: mb(100), Includes: "a"}); len(f) != 0 {
		t.Errorf("under 100 MB: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "CLUSTERED", TotalPages: 1000, TablePages: 1000}); has(f, "INCLUDE") {
		t.Errorf("a clustered index is the table: %v", f)
	}
	// Sharing a leading key column.
	if f := IndexCacheFlags(IndexCacheFact{Type: "NONCLUSTERED", Keys: "CustomerID, OrderDate", SameLeading: 2}); !has(f, "Shares its leading key column (CustomerID) with 2 other indexes") {
		t.Errorf("leading: %v", f)
	}
	// A heap with 100 MB or more in cache.
	if f := IndexCacheFlags(IndexCacheFact{Type: "HEAP", CachedPages: mb(100), TotalPages: mb(500)}); !has(f, "A heap") {
		t.Errorf("heap: %v", f)
	}
	if f := IndexCacheFlags(IndexCacheFact{Type: "HEAP", CachedPages: mb(99), TotalPages: mb(500)}); len(f) != 0 {
		t.Errorf("small heap: %v", f)
	}
}

func TestBuildDatabaseMemory(t *testing.T) {
	at := func(d time.Duration) *time.Time { x := memNow.Add(-d); return &x }
	mbPages := int64(1024 / 8)
	f := DatabaseCacheFacts{Database: "GraniteLive", ReadAt: memNow, StartedAt: memNow.Add(-5 * 24 * time.Hour), Seconds: 3}
	// 60 indexes: the first hot (600 MB), one warm (500 MB), one cold (400 MB), then 57 small cold ones.
	f.Indexes = append(f.Indexes,
		IndexCacheFact{Schema: "dbo", Table: "Transaction", Index: "PK_Transaction", Type: "CLUSTERED", CachedPages: 600 * mbPages, TotalPages: 650 * mbPages, LastRead: at(10 * time.Minute), Keys: "ID"},
		IndexCacheFact{Schema: "dbo", Table: "Audit", Type: "HEAP", CachedPages: 500 * mbPages, TotalPages: 800 * mbPages, LastRead: at(5 * time.Hour)},
		IndexCacheFact{Schema: "dbo", Table: "Transaction", Index: "IX_Wide", Type: "NONCLUSTERED", CachedPages: 400 * mbPages, TotalPages: 500 * mbPages, TablePages: 650 * mbPages, LastRead: at(48 * time.Hour)},
	)
	for i := 0; i < 57; i++ {
		f.Indexes = append(f.Indexes, IndexCacheFact{Schema: "dbo", Table: "T", Index: "IX", Type: "NONCLUSTERED", CachedPages: mbPages, TotalPages: 10 * mbPages})
	}
	d := BuildDatabaseMemory(f)
	if d.Indexes != 60 || len(d.Rows) != MemoryDetailRows || d.Rest == nil || d.Rest.Indexes != 10 || d.Rest.CachedMB != 10 {
		t.Fatalf("rows %d rest %+v", len(d.Rows), d.Rest)
	}
	if d.Rows[0].Temperature != "hot" || d.Rows[1].Temperature != "warm" || d.Rows[2].Temperature != "cold" || d.Rows[3].Temperature != "cold" {
		t.Errorf("temperatures %s %s %s %s", d.Rows[0].Temperature, d.Rows[1].Temperature, d.Rows[2].Temperature, d.Rows[3].Temperature)
	}
	if d.Rows[1].Index != "(heap)" || d.Rows[0].CachedPct != 92.3 || d.Rows[0].Table != "dbo.Transaction" {
		t.Errorf("row 0/1: %+v / %+v", d.Rows[0], d.Rows[1])
	}
	if d.HotGB != gbOf(600) || d.WarmGB != gbOf(500) || d.ColdGB != gbOf(457) {
		t.Errorf("split %v %v %v", d.HotGB, d.WarmGB, d.ColdGB)
	}
	if d.Answer != "1.5 GB of GraniteLive is in cache: 600 MB hot, 500 MB warm, 457 MB cold." || d.UsageNote != "" {
		t.Errorf("answer %q / %q", d.Answer, d.UsageNote)
	}
	if len(d.Rows[0].Flags) != 1 || len(d.Rows[1].Flags) != 1 || len(d.Rows[2].Flags) != 1 {
		t.Errorf("flags %v / %v / %v", d.Rows[0].Flags, d.Rows[1].Flags, d.Rows[2].Flags)
	}
	if !strings.Contains(d.Handover, "Cold is the number to trust") || !strings.Contains(d.Handover, "10 more indexes: 10 MB") {
		t.Errorf("handover:\n%s", d.Handover)
	}

	// Up less than a day: the temperatures are not reliable yet.
	f.StartedAt = memNow.Add(-5 * time.Hour)
	if d := BuildDatabaseMemory(f); !strings.Contains(d.UsageNote, "started 5 hours ago") {
		t.Errorf("usage note %q", d.UsageNote)
	}
	// Nothing cached; no permission.
	if d := BuildDatabaseMemory(DatabaseCacheFacts{Database: "Empty", ReadAt: memNow}); d.Answer != "Nothing of Empty is in cache." || d.Rest != nil {
		t.Errorf("empty %q", d.Answer)
	}
	if d := BuildDatabaseMemory(DatabaseCacheFacts{Database: "X", Permission: "VIEW DATABASE STATE in X"}); !strings.Contains(d.Answer, "needs VIEW DATABASE STATE in X") {
		t.Errorf("permission %q", d.Answer)
	}
}
