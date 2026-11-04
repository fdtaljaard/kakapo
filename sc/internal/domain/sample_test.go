package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var sampleStart = time.Date(2026, 11, 3, 10, 14, 0, 0, time.UTC)

// busySample is two snapshots 30 seconds apart on a server in trouble: a sleeping head blocker,
// a backup running, the log growing, THREADPOOL, CPU busy with other programs, slow data reads,
// memory pressure and one query taking the CPU.
func busySample() SampleInputs {
	blocker := Blocker{Session: 83, Blocked: 14, LongestSec: 360, Database: "WarehouseLive", Login: "appsvc", Host: "WMS-APP01",
		Program: "AppScheduler", Status: "sleeping", OpenTran: 1, LastStatement: "EXEC dbo.usp_Packing_Item @id = 5"}
	later := blocker
	later.LongestSec = 390
	first := SampleSnapshot{
		At: sampleStart, ServerName: "WMS-SQL01",
		Waits: []WaitStat{
			{Type: "LCK_M_X", WaitMs: 1000, SignalMs: 10, Tasks: 10},
			{Type: "PAGEIOLATCH_SH", WaitMs: 500, SignalMs: 50, Tasks: 100},
			{Type: "CXCONSUMER", WaitMs: 100, Tasks: 1},
		},
		Runnable: 2, Schedulers: 8,
		Queries: []SampleQueryStats{
			{QueryHash: "0xAAAA000000000001", CPU: 1000000, Execs: 10, Reads: 5000, Elapsed: 2000000},
			{QueryHash: "0xCCCC000000000003", CPU: 9000000, Execs: 50, Reads: 10, Elapsed: 9000000},
		},
		Files: []SampleFileStats{
			{Database: "WarehouseLive", FileID: 1, File: "WarehouseLive", Type: "ROWS", Reads: 100, ReadStallMs: 1000, SizeBytes: 10 << 30},
			{Database: "WarehouseLive", FileID: 2, File: "WarehouseLive_log", Type: "LOG", Writes: 1000, WriteStallMs: 1000, SizeBytes: 1024 << 20},
		},
		Blockers:           []Blocker{blocker},
		Running:            []RunningCommand{{Session: 61, Command: "BACKUP DATABASE", Database: "WarehouseLive", PercentDone: 40, ElapsedSeconds: 600}},
		PageLifeExpectancy: 4000, GrantsPending: 0,
	}
	second := SampleSnapshot{
		At: sampleStart.Add(30 * time.Second), ServerName: "WMS-SQL01",
		Waits: []WaitStat{
			{Type: "LCK_M_X", WaitMs: 61000, SignalMs: 210, Tasks: 40},
			{Type: "PAGEIOLATCH_SH", WaitMs: 2500, SignalMs: 150, Tasks: 300},
			{Type: "THREADPOOL", WaitMs: 3000, Tasks: 5},
			{Type: "CXCONSUMER", WaitMs: 900000, Tasks: 100},
		},
		CPURecords: []CPUSample{
			{At: sampleStart.Add(20 * time.Second), SQL: 70, Other: 25, Idle: 5},
			{At: sampleStart.Add(-40 * time.Second), SQL: 20, Other: 5, Idle: 75},
		},
		Runnable: 20, Schedulers: 8,
		Queries: []SampleQueryStats{
			{QueryHash: "0xAAAA000000000001", CPU: 61000000, Execs: 22, Reads: 905000, Elapsed: 70000000},
			{QueryHash: "0xBBBB000000000002", CPU: 2000000, Execs: 3, Reads: 300, Elapsed: 2500000},
			{QueryHash: "0xCCCC000000000003", CPU: 1000, Execs: 2, Reads: 1, Elapsed: 1000},  // evicted and back
			{QueryHash: "0xDDDD000000000004", CPU: 90000000, Execs: 1, Reads: 1, Elapsed: 1}, // kakapo's own: no text
		},
		Files: []SampleFileStats{
			{Database: "WarehouseLive", FileID: 1, File: "WarehouseLive", Type: "ROWS", Reads: 200, ReadStallMs: 4000, ReadBytes: 8 << 20, SizeBytes: 10 << 30},
			{Database: "WarehouseLive", FileID: 2, File: "WarehouseLive_log", Type: "LOG", Writes: 1100, WriteStallMs: 1300, WriteBytes: 4 << 20, SizeBytes: 1280 << 20},
		},
		Blockers:           []Blocker{later},
		Running:            []RunningCommand{{Session: 61, Command: "BACKUP DATABASE", Database: "WarehouseLive", PercentDone: 45, ElapsedSeconds: 630, LeftSeconds: 780}},
		PageLifeExpectancy: 120, GrantsPending: 2,
	}
	return SampleInputs{Database: "WarehouseLive", Seconds: 30, First: first, Second: second, QueryTexts: map[string]SampleQueryText{
		"0xAAAA000000000001": {Database: "WarehouseLive", ObjectName: "dbo.PickNext", Text: "SELECT TOP (1) * FROM dbo.Pick WHERE Status = @s"},
		"0xBBBB000000000002": {Database: "WarehouseLive", Text: "UPDATE dbo.Stock\r\nSET Qty = Qty - 1"},
		"0xCCCC000000000003": {Database: "WarehouseLive", Text: "SELECT 1"},
	}}
}

func TestSampleDelta(t *testing.T) {
	end := sampleStart.Add(30*time.Second - 2*time.Hour)
	s := BuildPerfSample(busySample(), end)
	if s.ElapsedSeconds != 30 || s.Seconds != 30 || s.ServerName != "WMS-SQL01" || !s.CapturedAt.Equal(end) {
		t.Errorf("header %+v", s)
	}

	// Waits: benign out, ordered by time in the sample, poison marked.
	if len(s.Waits) != 3 || s.Waits[0].Type != "LCK_M_X" || s.Waits[0].WaitMs != 60000 || s.Waits[0].Tasks != 30 || s.Waits[0].AvgMs != 2000 ||
		s.Waits[0].Words != "blocking" || s.Waits[1].Type != "THREADPOOL" || !s.Waits[1].Poison || s.Waits[2].Type != "PAGEIOLATCH_SH" || s.Waits[2].WaitMs != 2000 {
		t.Errorf("waits %+v", s.Waits)
	}

	// CPU: the record inside the window; runnable at both ends; signal share of the delta.
	c := s.CPU
	if c == nil || !c.InWindow || c.Records != 1 || c.SQL != 70 || c.Other != 25 || c.RunnableStart != 2 || c.RunnableEnd != 20 ||
		c.Schedulers != 8 || !c.HasSignalShare || c.SignalWaitPct != 0.5 {
		t.Errorf("cpu %+v", c)
	}

	// Queries: the delta by CPU; an evicted plan and kakapo's own (no text) left out.
	if len(s.Queries) != 2 || s.Queries[0].QueryHash != "0xAAAA000000000001" || s.Queries[0].CPUMs != 60000 || s.Queries[0].Runs != 12 ||
		s.Queries[0].Reads != 900000 || s.Queries[0].DurationMs != 68000 || s.Queries[1].QueryHash != "0xBBBB000000000002" || s.Queries[1].Runs != 3 {
		t.Errorf("queries %+v", s.Queries)
	}
	if got := TopQueryCandidates(busySample().First, busySample().Second, 2); len(got) != 2 || got[0] != "0xDDDD000000000004" || got[1] != "0xAAAA000000000001" {
		t.Errorf("candidates %v", got)
	}

	// Files: per file in the sample; growth of the log.
	if len(s.Files) != 2 || s.Files[0].File != "WarehouseLive" || s.Files[0].Reads != 100 || s.Files[0].MsPerRead != 30 || s.Files[0].ReadMB != 8 ||
		s.Files[1].Writes != 100 || s.Files[1].MsPerWrite != 3 {
		t.Errorf("files %+v", s.Files)
	}
	if len(s.Growth) != 1 || s.Growth[0].File != "WarehouseLive_log" || s.Growth[0].BeforeMB != 1024 || s.Growth[0].AfterMB != 1280 {
		t.Errorf("growth %+v", s.Growth)
	}

	// Blocking: seen at both ends, the later sight kept.
	b := s.Blocking
	if len(b.HeadBlockers) != 1 || !b.HeadBlockers[0].BothEnds || b.HeadBlockers[0].LongestSec != 390 || b.Blocked != 14 || b.LongestSec != 390 {
		t.Errorf("blocking %+v", b)
	}
	if len(s.Running) != 1 || s.Running[0].PercentDone != 45 {
		t.Errorf("running %+v", s.Running)
	}
	if m := s.Memory; m == nil || m.PLEStart != 4000 || m.PLEEnd != 120 || m.PendingEnd != 2 {
		t.Errorf("memory %+v", m)
	}

	want := []SampleLine{
		{Area: SampleAreaBlocking, Text: "Session 83 (AppScheduler, host WMS-APP01) has blocked 14 sessions for 6 minutes. It is running EXEC dbo.usp_Packing_Item @id = 5. It is sleeping with its transaction open: the program that owns it has not committed. Do not kill it until you know who owns it."},
		{Area: SampleAreaRunning, Text: "BACKUP DATABASE is running on WarehouseLive: 45% done, about 13 minutes to go. It competes with users for disk and CPU while it runs."},
		{Area: SampleAreaGrowth, Text: "The log file WarehouseLive_log of WarehouseLive grew by 256 MB during the sample. Users wait while a file grows: set a fixed growth of 256 MB so it grows rarely and quickly."},
		{Area: SampleAreaPoison, Text: "Poison wait THREADPOOL: 3.0 seconds of waiting in 30 seconds. SQL Server ran out of worker threads: find the blocking or the parallelism that uses them up."},
		{Area: SampleAreaWaits, Text: "Most of the waiting in this sample was blocking (LCK_M_X 60.0 s, THREADPOOL 3.0 s, PAGEIOLATCH_SH 2.0 s). " + wrk01Do[waitLOCK]},
		{Area: SampleAreaCPU, Text: "CPU 95%, 26% of it not SQL Server: look at what else runs on this host (antivirus, backup agent)."},
		{Area: SampleAreaCPU, Text: "20 queries were waiting for a CPU on 8 schedulers: the server is short of CPU right now."},
		{Area: SampleAreaFiles, Text: "Reads from WarehouseLive (WarehouseLive) took 30.0 ms each during the sample (100 reads); over 20 ms is slow for a data file."},
		{Area: SampleAreaMemory, Text: "2 queries were waiting for memory before they could start (memory grants pending): other queries hold it all, usually a few huge sorts or hashes."},
		{Area: SampleAreaMemory, Text: "Page life expectancy fell from 4,000 to 120 seconds during the sample: a query is reading more data than memory holds, pushing everything else out."},
		{Area: SampleAreaQueries, QueryHash: "0xAAAA000000000001", Text: "The query hurting now is dbo.PickNext: 60.0 s of CPU in 30 seconds (25% of the server), 12 runs."},
	}
	if len(s.Lines) != len(want) {
		t.Fatalf("lines %d\n%+v", len(s.Lines), s.Lines)
	}
	for i := range want {
		if s.Lines[i] != want[i] {
			t.Errorf("line %d\n%+v\nwant\n%+v", i, s.Lines[i], want[i])
		}
	}
	if s.Verdict != want[0].Text {
		t.Errorf("verdict %q", s.Verdict)
	}

	// It survives the JSON it is stored as.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back PerfSample
	if err := json.Unmarshal(raw, &back); err != nil || back.Verdict != s.Verdict || len(back.Lines) != len(s.Lines) || back.CPU.Other != 25 {
		t.Errorf("json round trip %v %+v", err, back)
	}
	if !strings.Contains(string(raw), `"queryHash":"0xAAAA000000000001"`) {
		t.Errorf("json %s", raw)
	}
}

func TestSampleQuiet(t *testing.T) {
	in := busySample()
	a, b := &in.First, &in.Second
	a.Blockers, b.Blockers, a.Running, b.Running = nil, nil, nil, nil
	b.Waits = []WaitStat{{Type: "LCK_M_X", WaitMs: 1200, SignalMs: 10, Tasks: 12}, {Type: "PAGEIOLATCH_SH", WaitMs: 600, SignalMs: 50, Tasks: 110}}
	// No CPU record in the window: the last one before it, said so.
	b.CPURecords = []CPUSample{{At: sampleStart.Add(-10 * time.Second), SQL: 12, Other: 3, Idle: 85}, {At: sampleStart.Add(-70 * time.Second), SQL: 50, Other: 0, Idle: 50}}
	b.Runnable = 0
	b.Queries = []SampleQueryStats{{QueryHash: "0xAAAA000000000001", CPU: 1500000, Execs: 15, Reads: 6000, Elapsed: 2600000}}
	b.Files = []SampleFileStats{
		{Database: "WarehouseLive", FileID: 1, File: "WarehouseLive", Type: "ROWS", Reads: 110, ReadStallMs: 1050, SizeBytes: 10 << 30},
		{Database: "WarehouseLive", FileID: 2, File: "WarehouseLive_log", Type: "LOG", Writes: 1050, WriteStallMs: 1100, SizeBytes: 1024 << 20},
	}
	b.PageLifeExpectancy, b.GrantsPending = 4010, 0

	s := BuildPerfSample(in, sampleStart.Add(30*time.Second))
	if len(s.Lines) != 0 {
		t.Errorf("lines %+v", s.Lines)
	}
	if s.Verdict != "No problem found in this 30-second sample. If users still see slowness, the cause is in the code or the indexes: see the costliest queries." {
		t.Errorf("verdict %q", s.Verdict)
	}
	if c := s.CPU; c == nil || c.InWindow || c.SQL != 12 || c.Records != 1 {
		t.Errorf("cpu %+v", c)
	}
	if len(s.Queries) != 1 || s.Queries[0].CPUMs != 500 || len(s.Growth) != 0 {
		t.Errorf("queries %+v growth %+v", s.Queries, s.Growth)
	}

	// An area that could not be read carries its reason, and the rest still answers.
	in.Second.WaitsError = "the login does not have VIEW SERVER STATE."
	s = BuildPerfSample(in, sampleStart)
	if s.WaitsError == "" || len(s.Waits) != 0 || s.CPU == nil || s.CPU.HasSignalShare || !strings.HasPrefix(s.Verdict, "No problem found") {
		t.Errorf("waits error %+v", s)
	}
	for _, x := range []*SampleSnapshot{&in.First, &in.Second} {
		x.WaitsError, x.CPUError, x.QueriesError, x.FilesError, x.BlockingError = "denied", "denied", "denied", "denied", "denied"
	}
	if s = BuildPerfSample(in, sampleStart); s.Verdict != "Nothing could be read in this sample: denied" {
		t.Errorf("nothing read %q", s.Verdict)
	}
}

func TestClampSampleSeconds(t *testing.T) {
	for in, want := range map[int]int{0: 30, 10: 10, 120: 120, 45: 45} {
		if got, err := ClampSampleSeconds(in); err != nil || got != want {
			t.Errorf("%d: %d %v", in, got, err)
		}
	}
	for _, in := range []int{9, 121, -5} {
		if _, err := ClampSampleSeconds(in); err == nil {
			t.Errorf("%d accepted", in)
		}
	}
}
