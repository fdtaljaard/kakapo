package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var perfStart = time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC) // server clock: ten days before setupNow

// goodPerf is a quiet SQL Server 2022 Standard instance: up ten days, the waits spread over five
// groups with none above 25%, a few signal waits, no deadlocks, few compiles, no blocking, no
// pressure, this database the busy one, no missing index, and five plain queries in the plan cache.
func goodPerf() PerformanceFacts {
	return PerformanceFacts{
		ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", EngineEdition: 2,
		Database: "WarehouseLive", State: "ONLINE", StartedAt: perfStart, UptimeHours: 240,
		Waits: []WaitStat{
			{"PAGEIOLATCH_SH", 200000, 10000, 5000, 900},
			{"CXPACKET", 240000, 40000, 8000, 1200},
			{"WRITELOG", 200000, 10000, 90000, 50},
			{"LAZYWRITER_SLEEP", 9000000000, 0, 1, 1}, // benign
			{"SLEEP_TASK", 9000000000, 0, 1, 1},       // SLEEP_%
			{"ASYNC_NETWORK_IO", 180000, 20000, 7000, 300},
			{"SOS_SCHEDULER_YIELD", 180000, 20000, 400000, 10},
		},
		Batches: 50000, Compiles: 9000, Recompiles: 100,
		Blockers: []Blocker{}, OpenTransactions: []OpenTransaction{}, IdleTransactions: []IdleTransaction{},
		Schedulers: &SchedulerLoad{MaxWorkers: 576, CurrentWorkers: 120, OnlineSchedulers: 8, RunnableTasks: 2, BusySchedulers: 1, MaxRunnable: 2},
		Grants:     &MemoryGrants{Granted: 3},
		Loads: []DatabaseLoad{{1, "master", 100, 1 << 30}, {5, "WarehouseLive", 600, 4 << 30}, {6, "WebShop", 200, 1 << 30},
			{2, "tempdb", 100, 2 << 30}},
		MissingIndexes: []MissingIndex{},
		QuerySource:    QuerySourcePlanCache,
		TotalCPU:       1000000000,
		TopQueries: []TopQuery{
			{QueryHash: "0x01", ObjectName: "dbo.USP_PICKING", Text: "SELECT TOP (1) *\r\n\tFROM dbo.Pick", CPU: 300000000, Execs: 1000,
				Reads: 5000000, MinReads: 4000, MaxReads: 6000, MaxCPU: 900000, Elapsed: 400000000, Variants: 1, Minutes: 14400},
		},
		CPUHistory: cpuHistory(240, 20, 5),
		ServerNow:  setupNow, UTCOffsetMinutes: 120,
	}
}

// cpuHistory is n one-minute samples ending at setupNow, SQL Server and other programs at a steady
// share of the CPU.
func cpuHistory(n, sqlPct, otherPct int) []CPUSample {
	out := []CPUSample{}
	for i := n - 1; i >= 0; i-- {
		out = append(out, CPUSample{At: setupNow.Add(-time.Duration(i) * time.Minute), SQL: sqlPct, Other: otherPct, Idle: 100 - sqlPct - otherPct})
	}
	return out
}

func perfDef() CardDef { d, _ := FindCardDef(CardPerformance); return d }

func runPerf(f PerformanceFacts) (Card, []Finding, []CheckStatus) {
	return BuildPerformanceCard(f, perfDef(), nil, nil)
}

func TestPerformanceCatalogue(t *testing.T) {
	checks := ChecksFor(CardPerformance)
	want := []string{"PRS01", "PRS02", "ACT01", "ACT02", "WRK01", "WRK02", "WRK03", "WRK05", "WRK06", "WRK08", "SRV04", "SRV07", "QRY01", "DLK01", "BLK01"}
	if len(checks) != len(want) {
		t.Fatalf("performance checks: %d", len(checks))
	}
	owners := map[string]string{"PRS01": OwnerUs, "PRS02": OwnerUs, "ACT01": OwnerUs, "ACT02": OwnerVendor, "WRK01": OwnerUs, "WRK02": OwnerClientIT,
		"WRK03": OwnerUs, "WRK05": OwnerUs, "WRK06": OwnerUs, "WRK08": OwnerClientIT, "SRV04": OwnerUs, "SRV07": OwnerVendor, "QRY01": OwnerUs,
		"DLK01": OwnerUs, "BLK01": OwnerUs}
	for i, c := range checks {
		if c.ID != want[i] || c.Title == "" || c.Question == "" || c.ThenCheck == "" || c.Owner != owners[c.ID] {
			t.Errorf("check %+v", c)
		}
	}
	for id, then := range map[string]string{"PRS01": "Re-run: PRS01 gone", "QRY01": "Re-run after a business day: the query's share is lower",
		"DLK01": "Re-run after a week: fewer deadlocks for the top pair"} {
		if c, _ := CheckByID(id); c.ThenCheck != then {
			t.Errorf("%s then check %q", id, c.ThenCheck)
		}
	}
}

func TestPerformanceGreen(t *testing.T) {
	c, fs, cs := runPerf(goodPerf())
	if c.Status != StatusGreen || c.StatusText != "No problem found" || c.ToDo+c.ForClientIT+c.Fixable != 0 || c.Note != "" {
		t.Errorf("green: %+v", c)
	}
	if want := "Nothing stands out: the top wait is CXPACKET at 24%, no blocking, no memory or thread pressure."; c.Verdict != want {
		t.Errorf("verdict %q", c.Verdict)
	}
	if c.AsOf == nil || !c.AsOf.Equal(time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)) {
		t.Errorf("asOf %v", c.AsOf)
	}
	wantFacts(t, c, "Biggest wait=CXPACKET 24% (parallelism)/normal", "CPU pressure=10% signal waits/normal",
		"Blocking now=none/normal", "Pressure now=none/normal")
	// Info findings only: the five small waits in one line (phase 5k), SRV04, the query.
	for _, x := range fs {
		if x.Severity != SeverityInfo || x.Clock == "" {
			t.Errorf("finding %+v", x)
		}
	}
	if len(findingsOf(fs, "WRK01")) != 1 || len(findingsOf(fs, "SRV04")) != 1 || len(findingsOf(fs, "QRY01")) != 1 || len(fs) != 3 {
		t.Errorf("findings %+v", fs)
	}
	if len(cs) != 15 {
		t.Fatalf("checks %+v", cs)
	}
	for _, x := range cs {
		switch x.CheckID {
		case "DLK01":
			if x.Status != CheckSkipped || x.Note != Dlk01NotRun {
				t.Errorf("DLK01 %+v", x)
			}
		case "BLK01":
			if x.Status != CheckSkipped || x.Note != Blk01NotRun {
				t.Errorf("BLK01 %+v", x)
			}
		case "QRY01":
			if x.Status != CheckPassed || x.Note != "From the plan cache since restart; turn Query Store on for history. The top query takes 30.0% of the CPU: moderate." {
				t.Errorf("QRY01 %+v", x)
			}
		default:
			if x.Status != CheckPassed || x.Note != "" {
				t.Errorf("check %+v", x)
			}
		}
	}
}

func TestPerformanceRestartNote(t *testing.T) {
	f := goodPerf()
	f.UptimeHours = 6
	if c, _, _ := runPerf(f); c.Note != "SQL Server restarted 6 hours ago: the since-restart figures may not reflect a normal day" {
		t.Errorf("note %q", c.Note)
	}
	f.UptimeHours = 24
	if c, _, _ := runPerf(f); c.Note != "" {
		t.Errorf("a day up: %q", c.Note)
	}
}

func TestWaitGroupAndBenign(t *testing.T) {
	for w, g := range map[string]string{
		"CXPACKET": "PAR", "CXSYNC_PORT": "PAR", "CXSYNC_CONSUMER": "PAR", "PAGEIOLATCH_SH": "IO", "WRITELOG": "LOG", "LCK_M_X": "LOCK",
		"SOS_SCHEDULER_YIELD": "CPU", "RESOURCE_SEMAPHORE": "MEM", "RESOURCE_SEMAPHORE_QUERY_COMPILE": "MEM", "ASYNC_NETWORK_IO": "NET",
		"PAGELATCH_EX": "PLATCH", "THREADPOOL": "THREAD", "HADR_SYNC_COMMIT": "AG", "BACKUPIO": "OTHERIO", "IO_COMPLETION": "OTHERIO",
		"PREEMPTIVE_OS_FILEOPS": "EXT", "OLEDB": "EXT", "LATCH_EX": "LATCH", "CMEMTHREAD": "OTHER", "LCK_X": "OTHER",
	} {
		if got := WaitGroup(w); got != g {
			t.Errorf("%s: %s, want %s", w, got, g)
		}
	}
	if len(BenignWaits) != 79 {
		t.Errorf("benign list: %d", len(BenignWaits))
	}
	for _, w := range []string{"CXCONSUMER", "XE_LIVE_TARGET_TVF", "SLEEP_ANYTHING", "sleep_task", "BROKER_EVENTHANDLER"} {
		if !IsBenignWait(w) {
			t.Errorf("%s should be benign", w)
		}
	}
	for _, w := range []string{"CXPACKET", "PAGEIOLATCH_SH", "SLEEPY"} {
		if IsBenignWait(w) {
			t.Errorf("%s is not benign", w)
		}
	}
}

func TestPerformanceWRK01(t *testing.T) {
	f := goodPerf()
	_, fs, _ := runPerf(f)
	w := findingsOf(fs, "WRK01")
	// Ten days up: the floor is a day of waiting, so every wait here is small against uptime: one Info
	// line (phase 5k), by wait time then type, each wait's time in minutes, no benign wait.
	if len(w) != 1 || w[0].Finding != "Waits since restart are small against uptime: CXPACKET 24.0%, PAGEIOLATCH_SH 20.0%, WRITELOG 20.0%, ASYNC_NETWORK_IO 18.0%, SOS_SCHEDULER_YIELD 18.0%." ||
		w[0].Severity != SeverityInfo || w[0].When != WhenNone || w[0].Effort != EffortNone || w[0].Clock != ClockSinceRestart ||
		w[0].Object != `SQL01\WMS` || w[0].Why != wrk01SmallWhy || w[0].DoThis != wrk01SmallDo ||
		!strings.HasPrefix(w[0].Evidence, "CXPACKET: 4 min over 8,000 waits. PAGEIOLATCH_SH: 3 min over 5,000 waits.") {
		t.Errorf("small waits %+v", w)
	}

	// Severities at the thresholds, on a day of uptime (floor 8,640,000 ms) with a billion ms of
	// waiting so every wait here is above the floor.
	for _, c := range []struct {
		wait       string
		ms         int64 // of 1,000,000,000 in all
		sev        Severity
		when, effo string
	}{
		{"THREADPOOL", 10000000, SeverityHigh, WhenDecideFirst, EffortModerate},             // 1%
		{"THREADPOOL", 9999000, SeverityInfo, WhenDecideFirst, EffortModerate},              // under 1%, still planned
		{"RESOURCE_SEMAPHORE", 100000000, SeverityHigh, WhenDecideFirst, EffortModerate},    // 10%
		{"RESOURCE_SEMAPHORE", 99999000, SeverityInfo, WhenDecideFirst, EffortModerate},     // under 10%
		{"CXPACKET", 250000000, SeverityMedium, WhenDecideFirst, EffortModerate},            // 25%
		{"LCK_M_U", 250000000, SeverityMedium, WhenDecideFirst, EffortModerate},             // 25%
		{"CXPACKET", 249999000, SeverityInfo, WhenNone, EffortNone},                         // under 25%
		{"ASYNC_NETWORK_IO", 400000000, SeverityInfo, WhenDecideFirst, EffortModerate},      // NET is never Medium
		{"PREEMPTIVE_OS_FILEOPS", 400000000, SeverityInfo, WhenDecideFirst, EffortModerate}, // EXT neither
	} {
		f := goodPerf()
		f.UptimeHours = 24
		f.Waits = []WaitStat{{c.wait, c.ms, 0, 10, 1}, {"BACKUPIO", 1000000000 - c.ms, 0, 10, 1}}
		_, fs, _ := runPerf(f)
		var got *Finding
		for i, x := range findingsOf(fs, "WRK01") {
			if strings.Contains(x.Finding, " "+c.wait+" ") {
				got = &findingsOf(fs, "WRK01")[i]
			}
		}
		if got == nil || got.Severity != c.sev || got.When != c.when || got.Effort != c.effo {
			t.Errorf("%s %d: %+v", c.wait, c.ms, got)
		}
	}

	// A Medium top wait turns the fact bad and makes the card amber.
	f = goodPerf()
	f.Waits = []WaitStat{{"CXPACKET", 600000000, 0, 10, 1}, {"WRITELOG", 400000000, 0, 10, 1}}
	c, _, _ := runPerf(f)
	if c.Status != StatusAmber || !strings.HasPrefix(c.Verdict, "Wait #1: CXPACKET (") || c.Facts[0].Tone != ToneBad ||
		c.Facts[0].Value != "CXPACKET 60% (parallelism)" || c.StatusText != "2 things to do" {
		t.Errorf("medium wait: %+v", c)
	}
}

// WRK01's floor (docs/phase3f-contract.md, item 6): a 65% LCK_M_S wait of 200 seconds on a
// 30-hour uptime is small against uptime and Info; four hours of it is Medium.
func TestPerformanceWRK01Floor(t *testing.T) {
	f := goodPerf()
	f.UptimeHours = 30 // floor: 3 hours, 10,800,000 ms
	f.Waits = []WaitStat{{"LCK_M_S", 200000, 0, 40, 9000}, {"WRITELOG", 107692, 0, 900, 10}}
	c, fs, cs := runPerf(f)
	x := findingsOf(fs, "WRK01")[0]
	if x.Finding != "Waits since restart are small against uptime: LCK_M_S 65.0%, WRITELOG 35.0%." || x.Severity != SeverityInfo ||
		x.When != WhenNone || x.Effort != EffortNone || x.Why != wrk01SmallWhy ||
		x.Evidence != "LCK_M_S: 3 min over 40 waits. WRITELOG: 2 min over 900 waits. Since 2026-10-24 11:00." {
		t.Errorf("small %+v", x)
	}
	if c.Status != StatusGreen || c.Verdict != "Nothing stands out: the top wait is LCK_M_S at 65%, no blocking, no memory or thread pressure." ||
		c.Facts[0].Tone != ToneNormal || statusOf(cs, "WRK01").Status != CheckPassed {
		t.Errorf("small card %+v", c)
	}

	f.Waits = []WaitStat{{"LCK_M_S", 4 * 3600000, 0, 40, 9000}, {"WRITELOG", 7753846, 0, 900, 10}}
	c, fs, _ = runPerf(f)
	x = findingsOf(fs, "WRK01")[0]
	if x.Finding != "Wait #1: LCK_M_S (65.0% of all waiting)." || x.Severity != SeverityMedium || x.When != WhenDecideFirst {
		t.Errorf("four hours %+v", x)
	}
	if c.Status != StatusAmber || c.Facts[0].Tone != ToneBad {
		t.Errorf("four hours card %+v", c)
	}

	// Under a day up the real hours count, at least one.
	f.UptimeHours = 0
	f.Waits = []WaitStat{{"LCK_M_S", 360000, 0, 1, 1}, {"WRITELOG", 359999, 0, 1, 1}}
	_, fs, _ = runPerf(f)
	if w := findingsOf(fs, "WRK01"); w[0].Severity != SeverityMedium || w[1].Finding != "Waits since restart are small against uptime: WRITELOG 50.0%." {
		t.Errorf("an hour's floor %+v", w)
	}
}

func TestPerformanceWRK02(t *testing.T) {
	f := goodPerf()
	f.Waits = []WaitStat{{"SOS_SCHEDULER_YIELD", 1000000, 250000, 10, 1}}
	c, fs, _ := runPerf(f)
	x := one(t, fs, "WRK02")
	if x.Severity != SeverityMedium || x.Owner != OwnerClientIT || x.When != WhenDecideFirst || x.Effort != EffortModerate ||
		x.Finding != "Queries spend 25% of their waiting time queuing for a CPU." || x.Evidence != "Signal wait 250 s of 1000 s total wait." ||
		x.Clock != ClockSinceRestart || x.Why != wrk02Why || x.DoThis != wrk02Do {
		t.Errorf("WRK02 %+v", x)
	}
	if c.ForClientIT != 1 || c.Facts[1].Value != "25% signal waits" || c.Facts[1].Tone != ToneBad {
		t.Errorf("card %+v", c)
	}
	f.Waits[0].SignalMs = 249999
	_, fs, _ = runPerf(f)
	none(t, fs, "WRK02")
}

func TestPerformanceWRK05(t *testing.T) {
	f := goodPerf() // ten days up
	f.Waits = append(f.Waits,
		WaitStat{"THREADPOOL", 600000, 0, 100, 4000},        // 60 s a day: High
		WaitStat{"RESOURCE_SEMAPHORE", 599999, 0, 10, 4000}, // just under: Medium
		WaitStat{"IO_QUEUE_LIMIT", 5000, 0, 3, 2000},        // 5 s: Medium
		WaitStat{"RESMGR_THROTTLED", 4999, 0, 3, 2000},      // under 5 s: nothing
		WaitStat{"LCK_M_X", 99999999, 0, 3, 2000})           // not poison
	_, fs, _ := runPerf(f)
	w := findingsOf(fs, "WRK05")
	if len(w) != 3 {
		t.Fatalf("WRK05 %+v", w)
	}
	sev := map[string]Severity{}
	for _, x := range w {
		sev[strings.Fields(x.Finding)[2]] = x.Severity
		if x.When != WhenDecideFirst || x.Effort != EffortModerate || x.Clock != ClockSinceRestart {
			t.Errorf("WRK05 %+v", x)
		}
	}
	if sev["THREADPOOL:"] != SeverityHigh || sev["RESOURCE_SEMAPHORE:"] != SeverityMedium || sev["IO_QUEUE_LIMIT:"] != SeverityMedium {
		t.Errorf("severities %v", sev)
	}
	tp := w[0]
	if tp.Finding != "Poison wait THREADPOOL: 60 seconds a day (10 waits a day)." ||
		tp.Evidence != "Since 2026-10-24 11:00: 600 s over 100 waits, longest single wait 4,000 ms." ||
		!strings.HasPrefix(tp.Why, "SQL Server ran out of worker threads") {
		t.Errorf("THREADPOOL %+v", tp)
	}
	// Under a day up counts as one day.
	f = goodPerf()
	f.UptimeHours = 5
	f.Waits = []WaitStat{{"THREADPOOL", 60000, 0, 1, 1}}
	_, fs, _ = runPerf(f)
	if x := one(t, fs, "WRK05"); x.Severity != SeverityHigh {
		t.Errorf("a short day %+v", x)
	}
}

func TestPerformanceWRK06(t *testing.T) {
	for _, c := range []struct {
		deadlocks int64
		sev       Severity
		when      string
		text      string
	}{
		{100, SeverityHigh, WhenDecideFirst, "100 deadlock(s) since the last restart (10.0 a day)."},
		{99, SeverityMedium, WhenDecideFirst, "99 deadlock(s) since the last restart (9.9 a day)."},
		{10, SeverityMedium, WhenDecideFirst, "10 deadlock(s) since the last restart (1.0 a day)."},
		{9, SeverityInfo, WhenNone, "9 deadlock(s) since the last restart (0.9 a day)."},
	} {
		f := goodPerf()
		f.Deadlocks = c.deadlocks
		_, fs, _ := runPerf(f)
		x := one(t, fs, "WRK06")
		if x.Severity != c.sev || x.When != c.when || x.Finding != c.text || x.Evidence != "Counter since 2026-10-24 11:00." ||
			x.Why != wrk06Why || x.Clock != ClockSinceRestart {
			t.Errorf("%d: %+v", c.deadlocks, x)
		}
		if c.sev == SeverityInfo && x.Effort != EffortNone || c.sev != SeverityInfo && x.Effort != EffortModerate {
			t.Errorf("%d effort %s", c.deadlocks, x.Effort)
		}
	}
	_, fs, _ := runPerf(goodPerf())
	none(t, fs, "WRK06")
}

func TestPerformanceSRV07(t *testing.T) {
	f := goodPerf()
	f.Batches, f.Compiles, f.Recompiles = 100000, 25000, 1200
	_, fs, _ := runPerf(f)
	x := one(t, fs, "SRV07")
	if x.Severity != SeverityMedium || x.Owner != OwnerVendor || x.When != WhenDecideFirst ||
		x.Finding != "25% of all batches compile a new plan (0.0 compiles a second)." ||
		x.Evidence != "Since restart: 100,000 batches, 25,000 compiles, 1,200 recompiles." {
		t.Errorf("SRV07 %+v", x)
	}
	f.Compiles = 24999
	if _, fs, _ := runPerf(f); one(t, fs, "SRV07").Severity != SeverityLow {
		t.Error("under 25% is Low")
	}
	f.Compiles = 9999
	_, fs, _ = runPerf(f)
	none(t, fs, "SRV07")
	f.Batches, f.Compiles = 99999, 99999
	_, fs, _ = runPerf(f)
	none(t, fs, "SRV07")
}

func TestPerformanceSRV04(t *testing.T) {
	_, fs, _ := runPerf(goodPerf())
	x := one(t, fs, "SRV04")
	if x.Severity != SeverityInfo || x.Object != "WarehouseLive" || x.When != WhenNone ||
		x.Finding != "This database used 60.0% of the query CPU and did 50.0% of the disk reads on this server since the last restart." ||
		x.DoThis != srv04DoMine || x.Evidence != "CPU share from plans still in cache; disk reads from file statistics. Busiest: WarehouseLive 60.0% CPU, 4.0 GB read; WebShop 20.0% CPU, 1.0 GB read; tempdb 10.0% CPU, 2.0 GB read; master 10.0% CPU, 1.0 GB read" {
		t.Errorf("SRV04 %+v", x)
	}
	f := goodPerf()
	f.Loads[2].CPU = 500 // WebShop: 500 of 1300
	_, fs, _ = runPerf(f)
	x = one(t, fs, "SRV04")
	if x.Severity != SeverityLow || x.DoThis != srv04DoOther || !strings.HasSuffix(x.Finding, " WebShop used 38.5% of the CPU.") {
		t.Errorf("busy neighbour %+v", x)
	}
	f = goodPerf()
	f.Loads = []DatabaseLoad{{6, "WebShop", 0, 1 << 30}}
	_, fs, _ = runPerf(f)
	if x := one(t, fs, "SRV04"); x.Finding != "This database did 0% of the disk reads on this server since the last restart. (No query CPU figures are in the plan cache yet.)" {
		t.Errorf("no CPU %q", x.Finding)
	}
}

func TestPerformanceWRK03(t *testing.T) {
	f := goodPerf()
	last := setupNow.Add(-time.Hour)
	f.MissingIndexes = []MissingIndex{
		{Statement: "[WarehouseLive].[dbo].[Pick]", Equality: "[Status]", Included: "[Qty]", Uses: 500, Impact: 91.4, Score: 1000000, LastSeek: &last},
		{Statement: "[WarehouseLive].[dbo].[Stock]", Inequality: "[Date]", Uses: 20, Impact: 60, Score: 100000},
		{Statement: "[WarehouseLive].[dbo].[Low]", Uses: 20, Impact: 49.9, Score: 900000},
		{Statement: "[WarehouseLive].[dbo].[Small]", Uses: 20, Impact: 90, Score: 99999},
	}
	_, fs, _ := runPerf(f)
	w := findingsOf(fs, "WRK03")
	if len(w) != 2 {
		t.Fatalf("WRK03 %+v", w)
	}
	x := w[0]
	if x.Severity != SeverityMedium || x.When != WhenWindow || x.Effort != EffortModerate || x.Object != "[WarehouseLive].[dbo].[Pick]" ||
		x.Finding != "SQL Server suggests an index on [WarehouseLive].[dbo].[Pick] (estimated 91% faster for 500 queries)." ||
		x.DoThis != wrk03Do+wrk03DoOffline || x.FixID != FixCreateIndex ||
		x.Evidence != "Equality: [Status]; inequality: -; include: [Qty]; last wanted: 2026-11-03 10:21." {
		t.Errorf("WRK03 %+v", x)
	}
	if w[1].Severity != SeverityLow || w[1].Evidence != "Equality: -; inequality: [Date]; include: -; last wanted: -." {
		t.Errorf("WRK03 low %+v", w[1])
	}
	f.EngineEdition = 3
	_, fs, _ = runPerf(f)
	if x := findingsOf(fs, "WRK03")[0]; x.When != WhenOnline || x.DoThis != wrk03Do+wrk03DoOnline {
		t.Errorf("enterprise %+v", x)
	}
}

func TestPerformanceACT01(t *testing.T) {
	f := goodPerf()
	f.Blockers = []Blocker{
		{Session: 57, Blocked: 3, LongestSec: 30, Database: "WarehouseLive", Login: "appsvc", Host: "APP01", Program: "AppScheduler",
			Status: "sleeping", OpenTran: 1, LastStatement: "UPDATE dbo.Pick\r\nSET Done = 1"},
		{Session: 61, Blocked: 1, LongestSec: 5, Database: "WebShop", Status: "running"},
		{Session: 62, Blocked: 1, LongestSec: 4, Status: "running"},
	}
	c, fs, cs := runPerf(f)
	w := findingsOf(fs, "ACT01")
	if len(w) != 2 {
		t.Fatalf("ACT01 %+v", w)
	}
	x := w[0]
	if x.Severity != SeverityHigh || x.Object != "Session 57 (WarehouseLive)" || x.When != WhenNone || x.Effort != EffortNone || x.Clock != ClockNow ||
		x.Finding != "Session 57 (appsvc on APP01) is blocking 3 session(s); the longest has waited 30 s." ||
		x.Evidence != "Program: AppScheduler. Status: sleeping. Open transactions: 1. Last statement: UPDATE dbo.Pick  SET Done = 1" {
		t.Errorf("ACT01 %+v", x)
	}
	if w[1].Severity != SeverityMedium || w[1].Object != "Session 61 (WebShop)" || w[1].Finding != "Session 61 (? on ?) is blocking 1 session(s); the longest has waited 5 s." {
		t.Errorf("ACT01 medium %+v", w[1])
	}
	if c.Facts[2].Value != "4 sessions blocked, longest 30 s" || c.Facts[2].Tone != ToneWorse || statusOf(cs, "ACT01").Status != CheckFinding {
		t.Errorf("blocking fact %+v", c.Facts[2])
	}
	f.Blockers = f.Blockers[1:]
	c, _, _ = runPerf(f)
	if c.Facts[2].Value != "1 session blocked, longest 5 s" || c.Facts[2].Tone != ToneBad {
		t.Errorf("medium fact %+v", c.Facts[2])
	}
	f.Blockers = f.Blockers[1:]
	c, fs, _ = runPerf(f)
	none(t, fs, "ACT01")
	if c.Facts[2].Value != "none" {
		t.Errorf("under 5 s %+v", c.Facts[2])
	}
}

func TestPerformanceACT02(t *testing.T) {
	f := goodPerf()
	started := setupNow.Add(-31 * time.Minute)
	f.OpenTransactions = []OpenTransaction{
		{Session: 70, Login: "appsvc", Host: "APP01", Program: "AppScheduler", Status: "sleeping", Minutes: 31, Started: started, LogBytes: 3 << 20, LastStatement: "BEGIN TRAN"},
		{Session: 71, Status: "running", Minutes: 6, Started: setupNow.Add(-6 * time.Minute)},
	}
	f.IdleTransactions = []IdleTransaction{
		{Session: 70, Login: "appsvc", Host: "APP01", Program: "AppScheduler", Minutes: 31}, // already reported for this database
		{Session: 80, Login: "erp", Host: "ERP01", Program: "ODBC", Minutes: 12, Implicit: true},
		{Session: 81, Login: "sa", Host: "PC7", Program: "SSMS", Minutes: 7},
	}
	_, fs, _ := runPerf(f)
	w := findingsOf(fs, "ACT02")
	if len(w) != 3 {
		t.Fatalf("ACT02 %+v", w)
	}
	byObject := map[string]Finding{}
	for _, x := range w {
		byObject[x.Object] = x
	}
	x := byObject["Session 70"]
	if x.Severity != SeverityHigh || x.Owner != OwnerVendor || x.When != WhenNone || x.Effort != EffortNone || x.Clock != ClockNow ||
		x.Finding != "Session 70 (appsvc on APP01) has had a transaction open for 31 minutes and is doing nothing." ||
		x.Evidence != "Program: AppScheduler. Started: 2026-11-03 10:50:00. Log used: 3.0 MB. Last statement: BEGIN TRAN" || x.Why != act02Why {
		t.Errorf("ACT02 %+v", x)
	}
	if x := byObject["Session 71"]; x.Severity != SeverityMedium || x.Finding != "Session 71 (? on ?) has had a transaction open for 6 minutes." {
		t.Errorf("ACT02 medium %+v", x)
	}
	x = byObject[`SQL01\WMS`]
	if x.Severity != SeverityMedium || x.When != WhenDecideFirst || x.Effort != EffortModerate || x.Why != wrk07Why || x.DoThis != wrk07Do ||
		x.Finding != "2 session(s) are idle with a transaction open, 1 of them implicit transactions opened by the driver; the oldest for 12 minutes." ||
		x.Evidence != "session 80 erp on ERP01 (ODBC), 12 min, implicit; session 81 sa on PC7 (SSMS), 7 min" {
		t.Errorf("WRK07 %+v", x)
	}
	f.IdleTransactions[1].Minutes = 30
	_, fs, _ = runPerf(f)
	for _, x := range findingsOf(fs, "ACT02") {
		if x.Object == `SQL01\WMS` && x.Severity != SeverityHigh {
			t.Errorf("idle 30 min %+v", x)
		}
	}
}

func TestPerformancePRS01(t *testing.T) {
	for _, c := range []struct {
		name   string
		edit   func(*PerformanceFacts)
		sev    Severity
		text   string
		phrase string
		tone   string
	}{
		{"threads 80%", func(f *PerformanceFacts) { f.Schedulers.CurrentWorkers = 461 }, SeverityHigh,
			"461 of 576 worker threads are in use (80%).", "threads 80%", ToneBad},
		{"threads 94%", func(f *PerformanceFacts) { f.Schedulers.CurrentWorkers = 542 }, SeverityCritical,
			"542 of 576 worker threads are in use (94%).", "threads 94%", ToneWorse},
		{"runnable 2", func(f *PerformanceFacts) { f.Schedulers.RunnableTasks = 16 }, SeverityHigh,
			"16 tasks are waiting for a CPU on 8 schedulers (2.0 each on average).", "CPU queue 2.0 per scheduler", ToneBad},
		{"memory", func(f *PerformanceFacts) { f.Grants.Waiting, f.Grants.Forced = 12, 3 }, SeverityHigh,
			"12 queries are waiting for memory right now; 3 memory grants were forced since restart.", "12 waiting for memory", ToneBad},
		{"forced only", func(f *PerformanceFacts) { f.Grants.Forced = 1 }, SeverityHigh,
			"1 memory grant was forced since restart.", "forced memory grants", ToneBad},
		{"offline", func(f *PerformanceFacts) { f.Schedulers.OfflineSchedulers = 4 }, SeverityCritical,
			"4 of 12 schedulers are offline.", "4 schedulers offline", ToneWorse},
		{"memory and runnable", func(f *PerformanceFacts) { f.Grants.Waiting, f.Schedulers.RunnableTasks = 1, 20 }, SeverityHigh,
			"1 query is waiting for memory right now; 20 tasks are waiting for a CPU on 8 schedulers (2.5 each on average).", "1 waiting for memory", ToneBad},
	} {
		f := goodPerf()
		c.edit(&f)
		card, fs, _ := runPerf(f)
		x := one(t, fs, "PRS01")
		if x.Severity != c.sev || x.Finding != c.text || x.Clock != ClockNow || x.When != WhenNone || x.Effort != EffortModerate || x.Owner != OwnerUs {
			t.Errorf("%s: %+v", c.name, x)
		}
		if card.Facts[3].Value != c.phrase || card.Facts[3].Tone != c.tone {
			t.Errorf("%s fact %+v", c.name, card.Facts[3])
		}
		if c.sev == SeverityCritical && (card.Status != StatusRed || card.Verdict != c.text) {
			t.Errorf("%s card %+v", c.name, card)
		}
	}
	// Under the thresholds, and offline schedulers someone chose with an affinity mask.
	f := goodPerf()
	f.Schedulers.CurrentWorkers, f.Schedulers.RunnableTasks = 460, 15
	f.Schedulers.OfflineSchedulers, f.Schedulers.AffinityManual = 4, true
	_, fs, _ := runPerf(f)
	none(t, fs, "PRS01")
	// Why and do this name each signal that fired.
	f = goodPerf()
	f.Schedulers.CurrentWorkers, f.Grants.Waiting = 542, 2
	_, fs, _ = runPerf(f)
	x := one(t, fs, "PRS01")
	if x.Why != prs01ThreadsWhy+" "+prs01MemoryWhy || x.DoThis != prs01ThreadsDo+" "+prs01MemoryDo ||
		x.Evidence != "Worker threads: 542 in use of 576; 0 tasks waiting for a thread. Runnable tasks: 2 on 8 online schedulers (1 busy, at most 2 on one); 0 offline. Memory grants: 2 waiting, 3 granted, 0 forced since restart." {
		t.Errorf("texts %+v", x)
	}
	// One of the two reads failing keeps the other and notes it; both failing skips.
	f = goodPerf()
	f.Grants, f.GrantsError = nil, "denied"
	f.Schedulers.CurrentWorkers = 542
	_, fs, cs := runPerf(f)
	if one(t, fs, "PRS01").Severity != SeverityCritical || statusOf(cs, "PRS01").Status != CheckFinding || !strings.HasSuffix(statusOf(cs, "PRS01").Note, "denied") {
		t.Errorf("partial %+v", statusOf(cs, "PRS01"))
	}
	f.Schedulers, f.SchedulersError = nil, "denied too"
	c, fs, cs := runPerf(f)
	none(t, fs, "PRS01")
	if statusOf(cs, "PRS01").Status != CheckSkipped || c.Facts[3].Value != "could not be read" {
		t.Errorf("both failing %+v %+v", statusOf(cs, "PRS01"), c.Facts[3])
	}
}

// qsFacts reads the top queries from Query Store.
func qsFacts(qs ...TopQuery) PerformanceFacts {
	f := goodPerf()
	f.QuerySource, f.TotalCPU, f.TopQueries = QuerySourceQueryStore, 2000000000, qs
	return f
}

func TestPerformanceQRY01(t *testing.T) {
	// The plan cache: since restart, PerformanceTriage.sql's sentence.
	_, fs, cs := runPerf(goodPerf())
	x := one(t, fs, "QRY01")
	if x.Severity != SeverityInfo || x.Clock != ClockSinceRestart || x.Object != "dbo.USP_PICKING" || x.When != WhenDecideFirst ||
		x.Effort != EffortModerate ||
		x.Finding != "Uses 30.0% of all query CPU on the server: 1,000 runs, 300 ms CPU and 5,000 page reads per run. About 100 runs a day." ||
		x.DoThis != qry01Do+" To see every statement in dbo.USP_PICKING, open the query: its detail lists them all, most reads first."+qry01Arithabort || x.Why != qry01Why ||
		x.Evidence != "Page reads per run: min 4,000, avg 5,000, max 6,000. Max CPU: 900 ms. Query hash: 0x01. Query: SELECT TOP (1) *   FROM dbo.Pick" {
		t.Errorf("plan cache %+v", x)
	}
	if statusOf(cs, "QRY01").Note != qry01FromCache+" The top query takes 30.0% of the CPU: moderate." {
		t.Errorf("note %+v", statusOf(cs, "QRY01"))
	}

	// Query Store: the last 7 days, and each of ACT03's rules makes it Low.
	plain := TopQuery{QueryHash: "0xAA", Text: "SELECT 1", CPU: 200000000, Execs: 100000, Reads: 1000000, MinReads: 1, MaxReads: 100,
		MaxCPU: 50000, Elapsed: 300000000, Variants: 1, Minutes: 10080, TopWait: "Lock"}
	_, fs, cs = runPerf(qsFacts(plain))
	x = one(t, fs, "QRY01")
	if x.Severity != SeverityInfo || x.Clock != ClockWindow || x.Object != "WarehouseLive" ||
		x.Finding != "Uses 10.0% of the query CPU in this database over the last 7 days: 100,000 runs, 2 ms CPU and 10 page reads per run. About 14,286 runs a day (one every 6 s): small per run, large in total." ||
		x.DoThis != qry01Do+" Also ask whether it needs to run this often (a job or integration polling on a timer?)."+qry01Arithabort ||
		x.Evidence != "Page reads per run: min 1, avg 10, max 100. Max CPU: 50 ms. Top wait: Lock. Query hash: 0xAA. Query: SELECT 1" {
		t.Errorf("query store %+v", x)
	}
	if statusOf(cs, "QRY01").Note != qry01FromQS+" The top query takes 10.0% of the CPU: flat, no single query dominates; look at the waits and the server first." ||
		statusOf(cs, "QRY01").Status != CheckPassed {
		t.Errorf("note %+v", statusOf(cs, "QRY01"))
	}

	varies := plain
	varies.MinReads, varies.MaxReads = 500, 10000
	waits := plain
	waits.Execs, waits.CPU, waits.Elapsed = 10, 1000000, 30000000
	variants := plain
	variants.Variants = 50
	warned := plain
	warned.Plan = `<StmtSimple><Warnings><PlanAffectingConvert ConvertIssue="Seek Plan" Expression="CONVERT_IMPLICIT(nvarchar(50),[t].[Code],0)=[@p]"/></Warnings><SpillToTempDb SpillLevel="1"/><IndexScan Lookup="true"/></StmtSimple>`
	big := plain
	big.Plan, big.PlanTooBig = "", true
	for _, c := range []struct {
		name     string
		q        TopQuery
		sev      Severity
		sentence string
		do       string
		evidence string
	}{
		{"varies", varies, SeverityLow, " Its cost depends heavily on the input: from 500 to 10,000 page reads per run. The work follows the rows: some inputs simply ask for more.",
			" Test with the values users actually enter, including one that matches nothing: some inputs cost far more than others.", ""},
		{"waits", waits, SeverityLow, " It spends most of its time waiting, not working: 3,000 ms elapsed against 100 ms CPU per run.",
			" Waiting rather than working means blocking (ACT01, IDX11) or reading from disk: fix that before tuning the query itself.", ""},
		{"variants", variants, SeverityLow, " It arrives as 50 separate statements with the values typed into the SQL text.",
			" The code should pass values as parameters so SQL Server reuses one plan (see SRV06).", ""},
		{"warnings", warned, SeverityLow,
			" Its plan has an implicit conversion that can stop SQL Server using an index. Its plan shows: key lookups (the index used does not hold every column needed).",
			" Implicit conversion: usually a variable, parameter or join column whose type differs from the column it is compared with.",
			" Conversion: CONVERT_IMPLICIT(nvarchar(50),[t].[Code],0)=[@p]."},
		{"too big", big, SeverityInfo, "", "", " Plan too large to inspect."},
	} {
		_, fs, _ := runPerf(qsFacts(c.q))
		x := one(t, fs, "QRY01")
		if x.Severity != c.sev || !strings.Contains(x.Finding, c.sentence) || !strings.Contains(x.DoThis, c.do) || !strings.Contains(x.Evidence, c.evidence) {
			t.Errorf("%s: %+v", c.name, x)
		}
	}

	// At most five, the query text cut at 1,500 characters.
	long := plain
	long.Text = strings.Repeat("x", 2000)
	qs := []TopQuery{long, plain, plain, plain, plain, plain}
	_, fs, _ = runPerf(qsFacts(qs...))
	q := findingsOf(fs, "QRY01")
	if len(q) != 5 || !strings.HasSuffix(q[0].Evidence, "Query: "+strings.Repeat("x", 1500)) {
		t.Errorf("five, cut: %d", len(q))
	}

	// Neither source readable: skipped with the reason.
	f := goodPerf()
	f.TopQueries, f.QueriesError = nil, "denied"
	_, fs, cs = runPerf(f)
	none(t, fs, "QRY01")
	if st := statusOf(cs, "QRY01"); st.Status != CheckSkipped || st.Note != queriesNotRead+"denied" {
		t.Errorf("skipped %+v", st)
	}
}

func TestPlanWarnings(t *testing.T) {
	plan := `<ShowPlanXML><StmtSimple StatementOptmEarlyAbortReason="TimeOut"><QueryPlan><MissingIndexes></MissingIndexes>` +
		`<MemoryGrantWarning GrantWarningKind="Excessive Grant"/><RelOp><NestedLoops><IndexScan Lookup="true"/></NestedLoops>` +
		`<UserDefinedFunction FunctionName="dbo.f"/><Object Table="[@t]"/><Hash NoJoinPredicate="true"/></RelOp></QueryPlan></StmtSimple></ShowPlanXML>`
	conv, flags := PlanWarnings(plan)
	// MemoryGrantWarning and SpillToTempDb exist only in actual plans: no flag for them.
	if conv != "" || len(flags) != 6 || flags[0] != "a join with no join condition (cross join)" ||
		flags[5] != "SQL Server suggests an index for it (check IDX02 and IDX09 before creating one)" {
		t.Errorf("warnings %q %q", conv, flags)
	}
	if conv, flags := PlanWarnings(""); conv != "" || len(flags) != 0 {
		t.Errorf("empty %q %q", conv, flags)
	}
}

func TestPerformanceGrey(t *testing.T) {
	// The waits cannot be read: grey with the reason; the other checks still run.
	f := goodPerf()
	f.Waits, f.WaitsError = nil, "the login does not have VIEW SERVER STATE."
	f.Blockers = []Blocker{{Session: 57, Blocked: 1, LongestSec: 40}}
	c, fs, cs := runPerf(f)
	if c.Status != StatusGrey || c.StatusText != "Could not be read" || c.Note != waitsNotRead+"the login does not have VIEW SERVER STATE." ||
		c.Verdict != "" || c.AsOf != nil {
		t.Errorf("grey %+v", c)
	}
	for _, id := range []string{"WRK01", "WRK02", "WRK05"} {
		if st := statusOf(cs, id); st.Status != CheckSkipped || !strings.HasPrefix(st.Note, waitsNotRead) {
			t.Errorf("%s %+v", id, st)
		}
	}
	one(t, fs, "ACT01")

	// Counters, blocking, transactions, load, missing indexes: each skipped on its own.
	f = goodPerf()
	f.CountersError, f.BlockingError, f.TransactionsError, f.LoadError, f.MissingIndexError = "a", "b", "c", "d", "e"
	c, _, cs = runPerf(f)
	for id, note := range map[string]string{"WRK06": countersNotRead + "a", "SRV07": countersNotRead + "a", "ACT01": blockingNotRead + "b",
		"ACT02": transNotRead + "c", "SRV04": loadNotRead + "d", "WRK03": missingNotRead + "e"} {
		if st := statusOf(cs, id); st.Status != CheckSkipped || st.Note != note {
			t.Errorf("%s %+v", id, st)
		}
	}
	if c.Status != StatusGreen || c.Facts[2].Value != "could not be read" {
		t.Errorf("card with skipped checks %+v", c)
	}

	// Nothing read, or the database not online.
	c, fs, cs = runPerf(PerformanceFacts{Database: "WarehouseLive", Error: "login failed"})
	if c.Status != StatusGrey || c.Note != perfNotRead+"login failed" || len(fs) != 0 || len(cs) != 15 ||
		statusOf(cs, "DLK01").Status != CheckSkipped || statusOf(cs, "BLK01").Status != CheckSkipped {
		t.Errorf("unread %+v %+v", c, cs)
	}
	c, _, _ = runPerf(PerformanceFacts{Database: "WarehouseLive", State: "RESTORING"})
	if c.Status != StatusGrey || c.Note != "WarehouseLive is RESTORING, not online, so its performance was not checked." {
		t.Errorf("restoring %+v", c)
	}
}

func deadlockEvent(at time.Time, db, victimProc, winnerProc, object string) DeadlockEvent {
	return DeadlockEvent{Time: at, Database: db,
		Victim:  DeadlockProcess{ID: "v", Procedure: victimProc, Statement: "UPDATE dbo." + object + " SET x = 1", Database: db},
		Winner:  DeadlockProcess{ID: "w", Procedure: winnerProc, Statement: "SELECT 1", Database: db},
		Objects: []string{"dbo." + object}, LockModes: []string{"keylock dbo." + object + ": held X, wanted U"}}
}

func TestBuildDeadlockReport(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	events := []DeadlockEvent{}
	// Ten between picking and integration, whichever was the victim.
	for i := 0; i < 10; i++ {
		v, w := "dbo.USP_PICKING", "dbo.IntegrationPost"
		if i%2 == 1 {
			v, w = w, v
		}
		obj := "Stock"
		if i == 9 {
			obj = "Pick"
		}
		events = append(events, deadlockEvent(now.Add(-time.Duration(i+1)*time.Hour), "WarehouseLive", v, w, obj))
	}
	// Three between the scheduler and an ad hoc statement; one alone; others outside the report.
	adhoc := deadlockEvent(now.Add(-2*24*time.Hour), "WarehouseLive", "dbo.Scheduler", "", "Job")
	adhoc.Winner.Statement = "DELETE  FROM dbo.Job\r\n WHERE Id = 4"
	events = append(events, adhoc, adhoc, adhoc,
		deadlockEvent(now.Add(-3*24*time.Hour), "WarehouseLive", "dbo.A", "dbo.B", "T"),
		deadlockEvent(now.Add(-8*24*time.Hour), "WarehouseLive", "dbo.A", "dbo.B", "T"), // outside the window
		deadlockEvent(now.Add(-time.Hour), "WebShop", "dbo.A", "dbo.B", "T"))            // another database
	r := BuildDeadlockReport(DeadlockInputs{Database: "warehouselive", CapturedAt: now, Duration: 1234567 * time.Microsecond,
		Days: 7, Source: DeadlockSourceFile, Events: events})

	if r.Total != 14 || r.Days != 7 || r.Source != "file" || r.DurationSeconds != 1.235 || !r.CapturedAt.Equal(now) || len(r.Pairs) != 3 {
		t.Fatalf("report %+v", r)
	}
	p := r.Pairs[0]
	if p.Procedures != [2]string{"dbo.IntegrationPost", "dbo.USP_PICKING"} || p.Count != 10 || !p.Last.Equal(now.Add(-time.Hour)) ||
		strings.Join(p.Objects, ",") != "dbo.Pick,dbo.Stock" || p.LastVictimStatement != "UPDATE dbo.Stock SET x = 1" {
		t.Errorf("top pair %+v", p)
	}
	if r.Pairs[1].Procedures != [2]string{"ad hoc: DELETE FROM dbo.Job WHERE Id = 4", "dbo.Scheduler"} || r.Pairs[1].Count != 3 ||
		r.Pairs[2].Count != 1 {
		t.Errorf("pairs %+v", r.Pairs[1:])
	}
	if len(r.Findings) != 3 {
		t.Fatalf("findings %+v", r.Findings)
	}
	x := r.Findings[0]
	if x.CheckID != "DLK01" || x.Severity != SeverityHigh || x.Clock != ClockWindow || x.When != WhenDecideFirst || x.Effort != EffortModerate ||
		x.Owner != OwnerUs || x.Why != wrk06Why || x.DoThis != dlk01Do || x.Object != "dbo.IntegrationPost / dbo.USP_PICKING" ||
		x.Finding != "10 deadlocks in 7 days between dbo.IntegrationPost and dbo.USP_PICKING, on dbo.Pick, dbo.Stock; the last was 2026-11-03 08:30 UTC." ||
		x.Evidence != "Lock types: keylock dbo.Stock: held X, wanted U. Last victim statement: UPDATE dbo.Stock SET x = 1" {
		t.Errorf("DLK01 %+v", x)
	}
	if r.Findings[1].Severity != SeverityMedium || r.Findings[2].Severity != SeverityLow {
		t.Errorf("severities %+v", r.Findings[1:])
	}

	// Nothing in the window: no pairs, no findings, [] in JSON.
	empty := BuildDeadlockReport(DeadlockInputs{Database: "Other", CapturedAt: now, Source: DeadlockSourceRingBuffer, Events: events})
	b, _ := json.Marshal(empty)
	if empty.Total != 0 || empty.Days != 7 || !strings.Contains(string(b), `"pairs":[]`) || !strings.Contains(string(b), `"findings":[]`) ||
		!strings.Contains(string(b), `"source":"ring-buffer"`) {
		t.Errorf("empty %s", b)
	}
	b, _ = json.Marshal(r.Pairs[0])
	if string(b) != `{"procedures":["dbo.IntegrationPost","dbo.USP_PICKING"],"count":10,"last":"2026-11-03T08:30:00Z","objects":["dbo.Pick","dbo.Stock"],"lastVictimStatement":"UPDATE dbo.Stock SET x = 1"}` {
		t.Errorf("pair JSON %s", b)
	}
}

func TestPerformanceDLK01Check(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	r := BuildDeadlockReport(DeadlockInputs{Database: "WarehouseLive", CapturedAt: now, Days: 7, Source: DeadlockSourceFile,
		Events: []DeadlockEvent{deadlockEvent(now.Add(-time.Hour), "WarehouseLive", "dbo.A", "dbo.B", "T")}})
	c, fs, cs := BuildPerformanceCard(goodPerf(), perfDef(), &r, nil)
	if st := statusOf(cs, "DLK01"); st.Status != CheckFinding || st.Note != "" {
		t.Errorf("DLK01 with deadlocks %+v", st)
	}
	none(t, fs, "DLK01") // the findings live in the report, not on the card
	if c.Status != StatusGreen {
		t.Errorf("the report never changes the card %+v", c)
	}
	clean := BuildDeadlockReport(DeadlockInputs{Database: "WarehouseLive", CapturedAt: now, Days: 7, Source: DeadlockSourceFile})
	_, _, cs = BuildPerformanceCard(goodPerf(), perfDef(), &clean, nil)
	if st := statusOf(cs, "DLK01"); st.Status != CheckPassed || st.Note != "No deadlocks in this database in the last 7 days." {
		t.Errorf("DLK01 clean %+v", st)
	}
}

func TestPerformanceDetailJSON(t *testing.T) {
	f := goodPerf()
	f.Deadlocks = 100
	c, fs, cs := runPerf(f)
	b, _ := json.Marshal(CardDetail{Card: c, Findings: fs, Checks: cs})
	s := string(b)
	if !strings.Contains(s, `"deadlocks":null`) || !strings.Contains(s, `"clock":"since-restart"`) || strings.Contains(s, "checkdbRoom") ||
		strings.Contains(s, `"fixes"`) || strings.Contains(s, "fixId") {
		t.Errorf("performance detail JSON: %s", s)
	}
	now := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	r := BuildDeadlockReport(DeadlockInputs{Database: "WarehouseLive", CapturedAt: now, Days: 7, Source: DeadlockSourceFile})
	b, _ = json.Marshal(CardDetail{Card: c, Findings: fs, Checks: cs, Deadlocks: &r})
	if !strings.Contains(string(b), `"deadlocks":{"capturedAt":"2026-11-03T09:30:00Z","durationSeconds":0,"days":7,"total":0,"source":"file","pairs":[],"findings":[]}`) {
		t.Errorf("with a report: %s", b)
	}
	// Other cards carry neither deadlocks nor a clock.
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardBackups}, Findings: []Finding{{CheckID: "BAK01"}}, Checks: []CheckStatus{}})
	if strings.Contains(string(b), "deadlocks") || strings.Contains(string(b), "clock") {
		t.Errorf("backups detail JSON: %s", b)
	}
}

func TestBuildCardsWithPerformance(t *testing.T) {
	f := goodPerf()
	cards := BuildCards(nil, nil, nil, nil, nil, &f)
	if cards[4].ID != CardPerformance || cards[4].Status != StatusGreen || len(cards[4].Facts) != 4 {
		t.Errorf("performance card %+v", cards[4])
	}
	if cards := BuildCards(nil, nil, nil, nil, nil, nil); cards[4].Status != StatusGrey || cards[4].StatusText != "Not checked yet" {
		t.Errorf("no facts %+v", cards[4])
	}
}

// QRY01's spills and memory grants from the runtime statistics (docs/phase3f-contract.md, item 1).
func TestPerformanceQRY01SpillsAndGrants(t *testing.T) {
	base := TopQuery{QueryHash: "0xBB", Text: "SELECT * FROM dbo.Pick ORDER BY Code", CPU: 200000000, Execs: 100, Reads: 1000000, MinReads: 9000,
		MaxReads: 11000, MaxCPU: 2100000, Elapsed: 300000000, Variants: 1, Minutes: 10080, Spills: -1, MaxGrantKB: -1, MaxUsedGrantKB: -1, TempdbKB: -1}
	for _, c := range []struct {
		name     string
		edit     func(*TopQuery)
		sev      Severity
		sentence string
		do       string
		evidence string
	}{
		{"spills 1,000 pages a run", func(q *TopQuery) { q.Spills = 100000 }, SeverityLow, " It spills to TempDB (about 7.8 MB per run).", qry01SpillDo,
			" Spills: 100,000 pages in total, 7.8 MB per run."},
		{"spills under 1,000 pages a run", func(q *TopQuery) { q.Spills = 99999 }, SeverityInfo, "", "", " Spills: 99,999 pages in total"},
		{"no spills", func(q *TopQuery) { q.Spills = 0 }, SeverityInfo, "", "", ""},
		{"Query Store TempDB", func(q *TopQuery) { q.TempdbKB = 100 * 81920 }, SeverityLow, " It spills to TempDB (about 80 MB per run).", qry01SpillDo,
			" TempDB used: 80 MB per run (Query Store counts spills and temporary tables together)."},
		{"grant 100 MB, used under 10%", func(q *TopQuery) { q.MaxGrantKB, q.MaxUsedGrantKB = 102400, 10239 }, SeverityLow,
			" It asks for 100 MB of memory and uses 10.0 MB.", qry01GrantDo, " Largest memory grant: 100 MB, of which 10.0 MB used."},
		{"grant used 10%", func(q *TopQuery) { q.MaxGrantKB, q.MaxUsedGrantKB = 102400, 10240 }, SeverityInfo, "", "", " Largest memory grant: 100 MB"},
		{"grant under 100 MB", func(q *TopQuery) { q.MaxGrantKB, q.MaxUsedGrantKB = 102399, 0 }, SeverityInfo, "", "", ""},
		{"Query Store memory used", func(q *TopQuery) { q.MaxUsedGrantKB = 204800 }, SeverityInfo, "", "", " Largest memory used: 200 MB."},
	} {
		q := base
		c.edit(&q)
		_, fs, _ := runPerf(qsFacts(q))
		x := one(t, fs, "QRY01")
		if x.Severity != c.sev || !strings.Contains(x.Finding, c.sentence) || !strings.Contains(x.DoThis, c.do) || !strings.Contains(x.Evidence, c.evidence) {
			t.Errorf("%s: %+v", c.name, x)
		}
		if c.sev == SeverityInfo && (strings.Contains(x.Finding, "spills to TempDB") || strings.Contains(x.Finding, "asks for")) {
			t.Errorf("%s fired: %s", c.name, x.Finding)
		}
		if !strings.HasSuffix(x.DoThis, qry01Arithabort) {
			t.Errorf("%s: Do this must end with the ARITHABORT advice: %s", c.name, x.DoThis)
		}
	}
	// The plan XML's spill and grant warnings no longer count.
	q := base
	q.Plan = `<StmtSimple><SpillToTempDb SpillLevel="1"/><MemoryGrantWarning GrantWarningKind="Excessive Grant"/></StmtSimple>`
	_, fs, _ := runPerf(qsFacts(q))
	if x := one(t, fs, "QRY01"); x.Severity != SeverityInfo || strings.Contains(x.Finding, "Its plan shows") {
		t.Errorf("plan XML %+v", x)
	}
}

// QRY01's workload note, rare-but-expensive, plan instability and the parameter-sensitivity
// wording (docs/phase3f-contract.md, item 19).
func TestPerformanceQRY01Workload(t *testing.T) {
	q := TopQuery{QueryHash: "0xCC", Text: "SELECT 1", CPU: 200000000, Execs: 100000, Reads: 1000000, MinReads: 1, MaxReads: 100,
		MaxCPU: 50000, Elapsed: 300000000, Variants: 1, Minutes: 10080}
	five := func(cpu int64) PerformanceFacts {
		x := q
		x.CPU = cpu
		return qsFacts(x, x, x, x, x)
	}
	for cpu, want := range map[int64]string{
		200000000: " The top five queries take 50.0% of the CPU: concentrated, so tuning them pays.",
		100000000: " The top five queries take 25.0% of the CPU: moderate.",
		99999999:  " The top five queries take 25.0% of the CPU: flat, no single query dominates; look at the waits and the server first.",
	} {
		_, _, cs := runPerf(five(cpu))
		if st := statusOf(cs, "QRY01"); st.Note != qry01FromQS+want {
			t.Errorf("%d: %q", cpu, st.Note)
		}
	}
	two := qsFacts(q, q)
	if _, _, cs := runPerf(two); statusOf(cs, "QRY01").Note != qry01FromQS+" The top 2 queries take 20.0% of the CPU: flat, no single query dominates; look at the waits and the server first." {
		t.Errorf("two: %q", statusOf(cs, "QRY01").Note)
	}

	// Rare but expensive: ten runs or fewer with 5% or more of the CPU.
	rare := q
	rare.Execs, rare.CPU, rare.Elapsed, rare.Minutes = 10, 100000000, 100000000, 0
	_, fs, _ := runPerf(qsFacts(rare))
	if x := one(t, fs, "QRY01"); !strings.Contains(x.Finding, " Rare but expensive: 10 runs took 5.0% of the CPU.") || x.Severity != SeverityInfo {
		t.Errorf("rare %+v", x)
	}
	rare.Execs = 11
	_, fs, _ = runPerf(qsFacts(rare))
	if x := one(t, fs, "QRY01"); strings.Contains(x.Finding, "Rare but expensive") {
		t.Errorf("11 runs %+v", x)
	}

	// A new plan nearly every run, unless the query asks for it with RECOMPILE.
	unstable := q
	unstable.Plans, unstable.LowRunPlans = 3, 3
	_, fs, _ = runPerf(qsFacts(unstable))
	x := one(t, fs, "QRY01")
	if x.Severity != SeverityLow || !strings.Contains(x.Finding, " It gets a new plan almost every time it runs (3 plans, none used five times).") ||
		!strings.Contains(x.DoThis, qry01PlansDo+qry01Arithabort) || !strings.Contains(x.Evidence, " Plans: 3, 3 of them run under five times.") {
		t.Errorf("unstable %+v", x)
	}
	for _, edit := range []func(*TopQuery){
		func(q *TopQuery) { q.Text = "SELECT 1 OPTION (RECOMPILE)" },
		func(q *TopQuery) { q.LowRunPlans = 2 },
		func(q *TopQuery) { q.Plans, q.LowRunPlans = 1, 1 },
	} {
		y := unstable
		edit(&y)
		_, fs, _ = runPerf(qsFacts(y))
		if x := one(t, fs, "QRY01"); strings.Contains(x.Finding, "new plan") || x.Severity != SeverityInfo {
			t.Errorf("stable %+v", x)
		}
	}

	// What moves when the cost varies: CPU with the reads, or the reads alone.
	sniffed := q
	sniffed.MinReads, sniffed.MaxReads, sniffed.MaxCPU = 500, 10000, 5999 // CPU per run 2,000 µs: max under 3x
	_, fs, _ = runPerf(qsFacts(sniffed))
	if x := one(t, fs, "QRY01"); !strings.Contains(x.Finding, " Same rows, different work: a sniffed parameter") || strings.Contains(x.Finding, "follows the rows") {
		t.Errorf("sniffed %+v", x)
	}
	sniffed.MaxCPU = 6000
	_, fs, _ = runPerf(qsFacts(sniffed))
	if x := one(t, fs, "QRY01"); !strings.Contains(x.Finding, " The work follows the rows: some inputs simply ask for more.") {
		t.Errorf("volume %+v", x)
	}
	// The time varies, the reads do not.
	slow := q
	slow.MaxElapsed = 1000000 // avg elapsed 3,000 µs a run
	_, fs, _ = runPerf(qsFacts(slow))
	if x := one(t, fs, "QRY01"); !strings.Contains(x.Finding, " The time varies while the work does not: it waits (blocking or disk) on some runs (up to 1,000 ms against 3 ms on average).") ||
		x.Severity != SeverityInfo || !strings.Contains(x.Evidence, " Max elapsed: 1,000 ms.") {
		t.Errorf("time swings %+v", x)
	}
	slow.MaxElapsed = 999999
	_, fs, _ = runPerf(qsFacts(slow))
	if x := one(t, fs, "QRY01"); strings.Contains(x.Finding, "The time varies") {
		t.Errorf("under a second %+v", x)
	}
}

// The two-branch READ COMMITTED SNAPSHOT wording and the self-blocked lead blocker
// (docs/phase3f-contract.md, item 20).
func TestPerformanceRCSIAndSelfBlocking(t *testing.T) {
	f := goodPerf()
	f.Blockers = []Blocker{
		{Session: 57, Blocked: 2, LongestSec: 12, Database: "WarehouseLive", Login: "appsvc", Host: "APP01", Status: "running", SelfBlocked: true},
		{Session: 61, Blocked: 1, LongestSec: 6, Database: "WebShop", Status: "running"},
	}
	waits := TopQuery{QueryHash: "0xDD", Text: "SELECT 1", CPU: 1000000, Execs: 10, Reads: 100, MinReads: 1, MaxReads: 10, Elapsed: 30000000, Variants: 1}
	f.TopQueries = []TopQuery{waits}
	_, fs, _ := runPerf(f)
	a := findingsOf(fs, "ACT01")
	if a[0].Finding != "Session 57 (appsvc on APP01) is blocking 2 session(s) (waiting on its own parallel threads); the longest has waited 12 s." ||
		a[0].DoThis != act01Do+act01RCSIOff || a[1].DoThis != act01Do {
		t.Errorf("ACT01 %+v", a)
	}
	if x := one(t, fs, "QRY01"); !strings.Contains(x.DoThis, " fix that before tuning the query itself."+qry01RCSIOff) {
		t.Errorf("QRY01 %+v", x)
	}
	f.RCSI = true
	_, fs, _ = runPerf(f)
	if a := findingsOf(fs, "ACT01"); a[0].DoThis != act01Do+rcsiOn || a[1].DoThis != act01Do {
		t.Errorf("ACT01 with RCSI %+v", a)
	}
	if x := one(t, fs, "QRY01"); !strings.Contains(x.DoThis, " fix that before tuning the query itself."+rcsiOn) {
		t.Errorf("QRY01 with RCSI %+v", x)
	}
}

// PRS02: memory grant timeouts since the restart (docs/phase3f-contract.md, item 11).
func TestPerformancePRS02(t *testing.T) {
	for _, c := range []struct {
		timeouts int64
		sev      Severity
	}{{101, SeverityHigh}, {100, SeverityMedium}, {11, SeverityMedium}, {10, ""}} {
		f := goodPerf()
		f.Grants.Timeouts = c.timeouts
		_, fs, cs := runPerf(f)
		if c.sev == "" {
			none(t, fs, "PRS02")
			if statusOf(cs, "PRS02").Status != CheckPassed {
				t.Errorf("10 timeouts %+v", statusOf(cs, "PRS02"))
			}
			continue
		}
		x := one(t, fs, "PRS02")
		if x.Severity != c.sev || x.Clock != ClockSinceRestart || x.Owner != OwnerUs || x.When != WhenDecideFirst || x.Effort != EffortModerate ||
			x.Why != prs02Why || x.DoThis != prs02Do || x.ThenCheck != "Re-run after a business day: no new timeouts in PRS02." {
			t.Errorf("%d: %+v", c.timeouts, x)
		}
	}
	f := goodPerf()
	f.Grants.Timeouts = 1234
	_, fs, _ := runPerf(f)
	if x := one(t, fs, "PRS02"); x.Finding != "1,234 queries gave up waiting for memory since the restart (error 8645)." ||
		x.Evidence != "Memory grant timeouts since 2026-10-24 11:00: 1,234. Right now: 0 waiting for memory, 3 granted; 0 forced grants since restart." {
		t.Errorf("texts %+v", x)
	}
	f.Grants, f.GrantsError = nil, "denied"
	_, _, cs := runPerf(f)
	if st := statusOf(cs, "PRS02"); st.Status != CheckSkipped || st.Note != grantsNotRead+"denied" {
		t.Errorf("skipped %+v", st)
	}

	// PRS01's evidence names the largest grants held while queries wait.
	f = goodPerf()
	f.Grants.Waiting = 2
	f.Grants.Holders = []GrantHolder{{Session: 57, GrantedKB: 1048576, RequestedKB: 2097152, WaitMs: 12000, Statement: "SELECT *\r\nFROM dbo.Pick ORDER BY Code"},
		{Session: 61, GrantedKB: 51200, RequestedKB: 51200}}
	_, fs, _ = runPerf(f)
	if x := one(t, fs, "PRS01"); !strings.HasSuffix(x.Evidence, " Largest grants now: session 57 holds 1,024 MB (asked for 2,048 MB, waited 12.0 s): SELECT *  FROM dbo.Pick ORDER BY Code; session 61 holds 50 MB (asked for 50 MB, waited 0.0 s): ?.") {
		t.Errorf("holders %q", x.Evidence)
	}
}

// WRK08: SQL Server against other programs over the last four hours (docs/phase3f-contract.md,
// item 10).
func TestPerformanceWRK08(t *testing.T) {
	f := goodPerf()
	f.CPUHistory = cpuHistory(240, 20, 30)
	f.CPUHistory[100].Other, f.CPUHistory[100].Idle = 70, 10
	c, fs, _ := runPerf(f)
	x := one(t, fs, "WRK08")
	if x.Severity != SeverityMedium || x.Owner != OwnerClientIT || x.Clock != ClockSinceRestart || x.Object != "SQL01" ||
		x.Finding != "Other programs on SQL01 used 30% of the CPU on average over the last 4 hours; SQL Server used 20%." ||
		x.Why != wrk08OtherWhy || x.DoThis != wrk08OtherDo ||
		x.Evidence != "Busiest minute for other programs: 70% at 2026-11-03 09:02 (SQL Server 20%). 240 one-minute samples from 2026-11-03 07:22 to 2026-11-03 11:21: SQL Server 20%, other programs 30% on average." {
		t.Errorf("other %+v", x)
	}
	if c.ForClientIT != 1 {
		t.Errorf("for client IT %+v", c)
	}

	f.CPUHistory = cpuHistory(60, 85, 5)
	_, fs, _ = runPerf(f)
	x = one(t, fs, "WRK08")
	if x.Owner != OwnerUs || x.Finding != "SQL Server used 85% of the CPU on average over the last 60 minutes." || x.DoThis != wrk08SQLDo ||
		!strings.HasPrefix(x.Evidence, "Busiest minute for SQL Server: 85% at ") {
		t.Errorf("SQL %+v", x)
	}

	f.CPUHistory = cpuHistory(240, 80, 25) // both, at the thresholds
	_, fs, _ = runPerf(f)
	if w := findingsOf(fs, "WRK08"); len(w) != 2 {
		t.Errorf("both %+v", w)
	}
	f.CPUHistory = cpuHistory(240, 79, 24)
	_, fs, _ = runPerf(f)
	none(t, fs, "WRK08")

	f.CPUHistory = cpuHistory(4, 10, 90)
	_, fs, cs := runPerf(f)
	none(t, fs, "WRK08")
	if st := statusOf(cs, "WRK08"); st.Status != CheckSkipped || st.Note != "The CPU history holds 4 sample(s): too soon after restart." {
		t.Errorf("four samples %+v", st)
	}
	f.CPUHistory, f.CPUHistoryError = nil, "denied"
	_, _, cs = runPerf(f)
	if st := statusOf(cs, "WRK08"); st.Status != CheckSkipped || st.Note != cpuNotRead+"denied" {
		t.Errorf("unread %+v", st)
	}
}

// The deadlock report keeps the server's health warnings of the window (docs/phase3f-contract.md,
// item 21).
func TestDeadlockReportHealth(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	health := []HealthEvent{
		{At: now.Add(-8 * 24 * time.Hour), Component: "SYSTEM", State: "warning", Detail: "old"},
		{At: now.Add(-time.Hour), Component: "IO_SUBSYSTEM", State: "WARNING", Detail: "newest"},
		{At: now.Add(-2 * time.Hour), Component: "SYSTEM", State: "clean", Detail: "clean"},
		{At: now.Add(-3 * time.Hour), Component: "SYSTEM", State: "error", Detail: "error"},
	}
	for i := 0; i < 60; i++ {
		health = append(health, HealthEvent{At: now.Add(-time.Duration(4*60+i) * time.Minute), Component: "SYSTEM", State: "warning", Detail: "more"})
	}
	r := BuildDeadlockReport(DeadlockInputs{Database: "WarehouseLive", CapturedAt: now, Days: 7, Source: DeadlockSourceFile, Health: health})
	if len(r.Health) != 50 || r.Health[0].Detail != "newest" || r.Health[0].State != "warning" || r.Health[1].Detail != "error" {
		t.Fatalf("health %+v", r.Health[:2])
	}
	for _, h := range r.Health {
		if h.Detail == "old" || h.Detail == "clean" {
			t.Errorf("kept %+v", h)
		}
	}
	b, _ := json.Marshal(r.Health[0])
	if string(b) != `{"at":"2026-11-03T08:30:00Z","component":"IO_SUBSYSTEM","state":"warning","detail":"newest"}` {
		t.Errorf("JSON %s", b)
	}
	// None: left out of the JSON.
	empty := BuildDeadlockReport(DeadlockInputs{Database: "WarehouseLive", CapturedAt: now, Days: 7, Source: DeadlockSourceFile})
	if b, _ := json.Marshal(empty); strings.Contains(string(b), "health") {
		t.Errorf("empty %s", b)
	}
}

func TestHealthDetail(t *testing.T) {
	sys := map[string]string{"nonyieldingtasksreported": "2", "totaldumprequests": "3", "intervaldumprequests": "1", "badpagesdetected": "1",
		"isaccessviolationoccurred": "0", "writeaccessviolationcount": "0", "sickspinlocktype": "none", "latchwarnings": "4"}
	if got := HealthDetail("SYSTEM", sys, nil); got != "2 non-yielding tasks (a worker that would not give up its CPU); 3 memory dumps since startup (1 in this interval); 1 bad page detected (corruption: run DBCC CHECKDB); 4 latch warnings." {
		t.Errorf("system %q", got)
	}
	if got := HealthDetail("SYSTEM", map[string]string{"isaccessviolationoccurred": "1", "sickspinlocktype": "SOS_CACHESTORE"}, nil); got != "Access violations; sick spinlock SOS_CACHESTORE." {
		t.Errorf("access violation %q", got)
	}
	io := map[string]string{"iolatchtimeouts": "5", "totallongios": "12"}
	pending := []PendingIO{{File: `D:\Data\WarehouseLive.mdf`, DurationMs: 20500}, {File: `E:\Log\WarehouseLive_log.ldf`, DurationMs: 900}}
	if got := HealthDetail("IO_SUBSYSTEM", io, pending); got != `5 I/O latch timeouts; 12 long I/Os (over 15 s) since startup; the longest pending request has waited 20.5 s on D:\Data\WarehouseLive.mdf.` {
		t.Errorf("io %q", got)
	}
	if got := HealthDetail("SYSTEM", map[string]string{}, nil); got != "No counter set; SQL Server reported the state only." {
		t.Errorf("empty %q", got)
	}
}

// Phase 4c: each WRK03 finding carries the create-index fix, and the card lists one fix per finding
// with the suggestion's columns and the finding's object in its options.
func TestAvailablePerformanceFixes(t *testing.T) {
	f := goodPerf()
	f.MissingIndexes = []MissingIndex{
		{Statement: "[WarehouseLive].[dbo].[Pick]", Equality: "[Status]", Included: "[Qty]", Uses: 500, Impact: 91.4, Score: 1000000},
		{Statement: "[WarehouseLive].[dbo].[Pick]", Inequality: "[Date]", Uses: 20, Impact: 60, Score: 200000},
		{Statement: "[WarehouseLive].[dbo].[Low]", Uses: 20, Impact: 49.9, Score: 900000},
	}
	_, fs, _ := runPerf(f)
	fixes := AvailablePerformanceFixes(f, fs)
	if len(fixes) != 2 {
		t.Fatalf("fixes %+v", fixes)
	}
	want := []map[string]string{
		{OptionTable: "[dbo].[Pick]", OptionEquality: "[Status]", OptionInequality: "", OptionIncluded: "[Qty]", OptionObject: "[WarehouseLive].[dbo].[Pick]"},
		{OptionTable: "[dbo].[Pick]", OptionEquality: "", OptionInequality: "[Date]", OptionIncluded: "", OptionObject: "[WarehouseLive].[dbo].[Pick]"},
	}
	for i, x := range fixes {
		if x.ID != FixCreateIndex || x.CheckID != "WRK03" || x.Scope != ScopeDatabase || len(x.Options) != len(want[i]) {
			t.Errorf("fix %d %+v", i, x)
		}
		for k, v := range want[i] {
			if x.Options[k] != v {
				t.Errorf("fix %d option %s = %q, want %q", i, k, x.Options[k], v)
			}
		}
	}
	if got := AvailablePerformanceFixes(goodPerf(), nil); got == nil || len(got) != 0 {
		t.Errorf("no findings: %+v", got)
	}
}

// Phase 5j: a query whose plan in use is faster than the runs before it began stays as Info, said
// first; one that did not get faster keeps its severity.
func TestPerformanceQRY01Faster(t *testing.T) {
	since := time.Date(2026, 11, 4, 13, 34, 0, 0, time.UTC)
	q := TopQuery{QueryHash: "0xDE", Text: "SELECT * FROM v ORDER BY Barcode", CPU: 24400000000, Execs: 343, Reads: 605000000,
		MinReads: 8504, MaxReads: 3416696, MaxCPU: 193000000, Elapsed: 4100000000, Variants: 4, Minutes: 9200,
		Since: &since, BeforeRuns: 255, BeforeCPU: 255 * 95600000, NowRuns: 88, NowCPU: 88 * 10000}
	_, fs, _ := runPerf(qsFacts(q))
	x := one(t, fs, "QRY01")
	if x.Severity != SeverityInfo || !strings.HasPrefix(x.Finding, "Faster since 4 Nov 15:34: 10 ms of CPU a run, was 96 s. Nothing to do. Uses ") ||
		x.DoThis != "Nothing: it got faster. Open the query to see what changed." {
		t.Errorf("faster %+v", x)
	}
	q.NowCPU = 88 * 95000000
	_, fs, _ = runPerf(qsFacts(q))
	if x := one(t, fs, "QRY01"); x.Severity != SeverityLow || strings.HasPrefix(x.Finding, "Faster") {
		t.Errorf("same %+v", x)
	}
}
