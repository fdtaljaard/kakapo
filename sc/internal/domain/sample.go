package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The live sample (docs/phase4c-contract.md, part 3; DESIGN.md 5.3 "why is it slow right now"):
// two snapshots of the instance some seconds apart on one connection, and the difference between
// them, in the plain sentences of DESIGN 5.3. The delta rule is sp_PressureDetector's
// (@sample_seconds): counters are joined on their key between the two snapshots, only positive
// differences count, and the window is the server clock's difference between the snapshots, not
// the seconds asked for.

// Sample length limits, in seconds.
const (
	SampleDefaultSeconds = 30
	SampleMinSeconds     = 10
	SampleMaxSeconds     = 120
)

// SampleSnapshot is one read of the instance for the live sample (collect.Sample). Times are the
// server's clock carried with the UTC location. Each area that could not be read carries its
// reason.
type SampleSnapshot struct {
	At               time.Time // GETDATE()
	UTCOffsetMinutes int
	ServerName       string

	Waits      []WaitStat // sys.dm_os_wait_stats, the benign list and SLEEP_% left out
	WaitsError string

	// CPU: the scheduler monitor ring buffer's latest records and the runnable tasks now.
	CPURecords []CPUSample
	Runnable   int // runnable_tasks_count summed over the online schedulers
	Schedulers int // VISIBLE ONLINE schedulers
	CPUError   string

	Queries      []SampleQueryStats // sys.dm_exec_query_stats grouped by query_hash
	QueriesError string

	Files      []SampleFileStats // sys.dm_io_virtual_file_stats for this database and tempdb
	FilesError string

	Blockers      []Blocker // head blockers now
	BlockingError string

	Running      []RunningCommand // backup, restore, DBCC, index operations in progress
	RunningError string

	PageLifeExpectancy int64 // seconds; -1 when not read
	GrantsPending      int64 // Memory Grants Pending; -1 when not read
	MemoryError        string
}

// SampleQueryStats is one query shape's cumulative numbers in the plan cache. Times in microseconds.
type SampleQueryStats struct {
	QueryHash string // 0x...
	CPU       int64
	Execs     int64
	Reads     int64
	Elapsed   int64
}

// SampleFileStats is one database file's cumulative I/O and its size.
type SampleFileStats struct {
	Database     string
	FileID       int
	File         string // logical name
	Type         string // ROWS, LOG
	Reads        int64
	Writes       int64
	ReadBytes    int64
	WriteBytes   int64
	ReadStallMs  int64
	WriteStallMs int64
	SizeBytes    int64
}

// RunningCommand is a long operation in progress (dm_exec_requests with percent_complete).
type RunningCommand struct {
	Session        int     `json:"session"`
	Command        string  `json:"command"`
	Database       string  `json:"database"`
	PercentDone    float64 `json:"percentDone"`
	ElapsedSeconds int64   `json:"elapsedSeconds"`
	LeftSeconds    int64   `json:"leftSeconds"` // estimated_completion_time; 0 when SQL Server gives none
}

// SampleQueryText is the text of one query shape the sample names (read after the second snapshot
// for the top candidates only).
type SampleQueryText struct {
	Database   string
	ObjectName string // schema.procedure; "" for ad hoc
	Text       string
}

// SampleInputs is what one run read: the two snapshots and the texts of the top queries.
type SampleInputs struct {
	Database   string
	Seconds    int // asked for
	First      SampleSnapshot
	Second     SampleSnapshot
	QueryTexts map[string]SampleQueryText // by query hash; noise and kakapo's own left out
}

// PerfSample is one live sample as stored and shown (JSON).
type PerfSample struct {
	Database       string    `json:"database"`
	CapturedAt     time.Time `json:"capturedAt"` // the end, UTC
	Seconds        int       `json:"seconds"`    // asked for
	ElapsedSeconds float64   `json:"elapsedSeconds"`
	ServerName     string    `json:"serverName"`
	// Verdict is the sample's one sentence (SampleVerdict): the first line, or "No problem found".
	Verdict string `json:"verdict"`
	// Lines are the sentences that fired, in DESIGN 5.3's order; empty when nothing did.
	Lines []SampleLine `json:"lines"`

	Waits      []SampleWait `json:"waits"`
	WaitsError string       `json:"waitsError,omitempty"`

	CPU      *SampleCPU `json:"cpu"`
	CPUError string     `json:"cpuError,omitempty"`

	Queries      []SampleQuery `json:"queries"`
	QueriesError string        `json:"queriesError,omitempty"`

	Files      []SampleFile `json:"files"`
	FilesError string       `json:"filesError,omitempty"`

	Blocking      SampleBlocking `json:"blocking"`
	BlockingError string         `json:"blockingError,omitempty"`

	Running      []RunningCommand `json:"running"`
	RunningError string           `json:"runningError,omitempty"`

	Growth []SampleGrowth `json:"growth"`

	Memory      *SampleMemory `json:"memory"`
	MemoryError string        `json:"memoryError,omitempty"`
}

// Areas of a sample line, for the links the page puts next to a sentence.
const (
	SampleAreaBlocking = "blocking"
	SampleAreaRunning  = "running"
	SampleAreaGrowth   = "growth"
	SampleAreaPoison   = "poison"
	SampleAreaWaits    = "waits"
	SampleAreaCPU      = "cpu"
	SampleAreaFiles    = "files"
	SampleAreaMemory   = "memory"
	SampleAreaQueries  = "queries"
)

// SampleLine is one sentence that fired. QueryHash names the query of a "queries" line (the page
// links it to its investigation).
type SampleLine struct {
	Area      string `json:"area"`
	Text      string `json:"text"`
	QueryHash string `json:"queryHash,omitempty"`
}

// SampleWait is one wait type's time in the sample.
type SampleWait struct {
	Type     string  `json:"type"`
	Group    string  `json:"group"` // WRK01's group: LOCK, IO, CPU...
	Words    string  `json:"words"` // the group in words: "blocking", "reading from disk"
	WaitMs   int64   `json:"waitMs"`
	SignalMs int64   `json:"signalMs"`
	Tasks    int64   `json:"tasks"`
	AvgMs    float64 `json:"avgMs"`
	Poison   bool    `json:"poison"`
}

// SampleCPU is the CPU in the sample: the ring buffer records that fell inside it (or, when none
// did, the last one before it), the runnable tasks at both ends and the signal wait share.
type SampleCPU struct {
	SQL            int     `json:"sql"`   // average % used by SQL Server
	Other          int     `json:"other"` // average % used by other programs
	Idle           int     `json:"idle"`
	Records        int     `json:"records"`
	InWindow       bool    `json:"inWindow"` // false: no record fell inside; the last minute before it
	RunnableStart  int     `json:"runnableStart"`
	RunnableEnd    int     `json:"runnableEnd"`
	Schedulers     int     `json:"schedulers"`
	SignalWaitPct  float64 `json:"signalWaitPct"` // signal wait / wait in the sample
	HasSignalShare bool    `json:"hasSignalShare"`
}

// SampleQuery is one query shape's work in the sample.
type SampleQuery struct {
	QueryHash  string  `json:"queryHash"`
	Database   string  `json:"database"`
	ObjectName string  `json:"objectName"`
	Text       string  `json:"text"`
	CPUMs      float64 `json:"cpuMs"`
	Runs       int64   `json:"runs"`
	Reads      int64   `json:"reads"`
	DurationMs float64 `json:"durationMs"`
}

// SampleFile is one file's I/O in the sample.
type SampleFile struct {
	Database   string  `json:"database"`
	File       string  `json:"file"`
	Type       string  `json:"type"`
	Reads      int64   `json:"reads"`
	Writes     int64   `json:"writes"`
	ReadMB     float64 `json:"readMb"`
	WriteMB    float64 `json:"writeMb"`
	MsPerRead  float64 `json:"msPerRead"`
	MsPerWrite float64 `json:"msPerWrite"`
}

// SampleBlocking is the head blockers seen in either snapshot.
type SampleBlocking struct {
	HeadBlockers []SampleBlocker `json:"headBlockers"`
	Blocked      int             `json:"blocked"`    // the most sessions waiting behind one head blocker
	LongestSec   int64           `json:"longestSec"` // the longest wait seen
}

// SampleBlocker is one head blocker as last seen.
type SampleBlocker struct {
	Session    int    `json:"session"`
	Blocked    int    `json:"blocked"`
	LongestSec int64  `json:"longestSec"`
	Database   string `json:"database"`
	Login      string `json:"login"`
	Host       string `json:"host"`
	Program    string `json:"program"`
	Status     string `json:"status"`
	Statement  string `json:"statement"`
	BothEnds   bool   `json:"bothEnds"` // seen in both snapshots: it lasted the whole sample
}

// SampleGrowth is a file whose size changed in the sample.
type SampleGrowth struct {
	Database string  `json:"database"`
	File     string  `json:"file"`
	Type     string  `json:"type"`
	BeforeMB float64 `json:"beforeMb"`
	AfterMB  float64 `json:"afterMb"`
}

// SampleMemory is page life expectancy and memory grants pending at both ends.
type SampleMemory struct {
	PLEStart     int64 `json:"pleStart"`
	PLEEnd       int64 `json:"pleEnd"`
	PendingStart int64 `json:"pendingStart"`
	PendingEnd   int64 `json:"pendingEnd"`
}

// SampleNoProblem is DESIGN 5.3's "nothing fired" sentence for a sample of n seconds.
func SampleNoProblem(n int) string {
	return fmt.Sprintf("No problem found in this %d-second sample. If users still see slowness, the cause is in the code or the indexes: see the costliest queries.", n)
}

// SampleVerdict is the sample's one sentence: the first line that fired, the no-problem sentence,
// or why nothing could be read.
func SampleVerdict(s PerfSample) string {
	if len(s.Lines) > 0 {
		return s.Lines[0].Text
	}
	if s.WaitsError != "" && s.CPUError != "" && s.QueriesError != "" && s.FilesError != "" && s.BlockingError != "" {
		return "Nothing could be read in this sample: " + s.WaitsError
	}
	return SampleNoProblem(s.Seconds)
}

// ClampSampleSeconds is the seconds a sample runs: the default for 0, else between the limits.
func ClampSampleSeconds(n int) (int, error) {
	switch {
	case n == 0:
		return SampleDefaultSeconds, nil
	case n < SampleMinSeconds || n > SampleMaxSeconds:
		return 0, fmt.Errorf("a sample runs %d to %d seconds, not %d", SampleMinSeconds, SampleMaxSeconds, n)
	}
	return n, nil
}

// TopQueryCandidates are the query hashes with the most CPU in the sample, most first, at most n:
// the ones whose text is read after the second snapshot.
func TopQueryCandidates(first, second SampleSnapshot, n int) []string {
	ds := queryDeltas(first, second)
	out := []string{}
	for i, d := range ds {
		if i == n {
			break
		}
		out = append(out, d.QueryHash)
	}
	return out
}

func queryDeltas(first, second SampleSnapshot) []SampleQueryStats {
	before := map[string]SampleQueryStats{}
	for _, q := range first.Queries {
		before[q.QueryHash] = q
	}
	out := []SampleQueryStats{}
	for _, q := range second.Queries {
		b := before[q.QueryHash] // a shape first cached during the sample counts from zero
		d := SampleQueryStats{QueryHash: q.QueryHash, CPU: q.CPU - b.CPU, Execs: q.Execs - b.Execs,
			Reads: q.Reads - b.Reads, Elapsed: q.Elapsed - b.Elapsed}
		if d.Execs <= 0 || d.CPU < 0 {
			continue // did not run, or its plan left the cache and came back
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPU != out[j].CPU {
			return out[i].CPU > out[j].CPU
		}
		return out[i].QueryHash < out[j].QueryHash
	})
	return out
}

// sampleNotRead is how an area that could not be read in either snapshot is named.
func sampleNotRead(first, second string) string {
	if second != "" {
		return second
	}
	return first
}

// BuildPerfSample works out the difference between the two snapshots, the sentences that fire and
// the verdict. capturedAt is the end in UTC.
func BuildPerfSample(in SampleInputs, capturedAt time.Time) PerfSample {
	a, b := in.First, in.Second
	s := PerfSample{
		Database: in.Database, CapturedAt: capturedAt, Seconds: in.Seconds, ServerName: b.ServerName,
		Lines: []SampleLine{}, Waits: []SampleWait{}, Queries: []SampleQuery{}, Files: []SampleFile{},
		Blocking: SampleBlocking{HeadBlockers: []SampleBlocker{}}, Running: []RunningCommand{}, Growth: []SampleGrowth{},
	}
	if s.ServerName == "" {
		s.ServerName = a.ServerName
	}
	elapsed := b.At.Sub(a.At).Seconds()
	if elapsed <= 0 {
		elapsed = float64(in.Seconds)
	}
	s.ElapsedSeconds = math.Round(elapsed*10) / 10

	// Waits.
	var totalWait, totalSignal int64
	if e := sampleNotRead(a.WaitsError, b.WaitsError); e != "" {
		s.WaitsError = e
	} else {
		before := map[string]WaitStat{}
		for _, w := range a.Waits {
			before[strings.ToUpper(w.Type)] = w
		}
		all := []SampleWait{}
		for _, w := range b.Waits {
			if IsBenignWait(w.Type) {
				continue
			}
			p := before[strings.ToUpper(w.Type)]
			d := SampleWait{Type: w.Type, WaitMs: w.WaitMs - p.WaitMs, SignalMs: w.SignalMs - p.SignalMs, Tasks: w.Tasks - p.Tasks}
			if d.Tasks <= 0 || d.WaitMs <= 0 {
				continue
			}
			d.Group = WaitGroup(w.Type)
			d.Words = waitWords[d.Group]
			d.AvgMs = math.Round(float64(d.WaitMs)/float64(d.Tasks)*10) / 10
			d.Poison = isPoison(w.Type)
			totalWait += d.WaitMs
			totalSignal += max(d.SignalMs, 0)
			all = append(all, d)
		}
		sort.SliceStable(all, func(i, j int) bool { return all[i].WaitMs > all[j].WaitMs })
		for i, w := range all {
			if i < 5 || w.Poison {
				s.Waits = append(s.Waits, w)
			}
		}
	}

	// CPU.
	if e := sampleNotRead(a.CPUError, b.CPUError); e != "" {
		s.CPUError = e
	} else {
		c := &SampleCPU{RunnableStart: a.Runnable, RunnableEnd: b.Runnable, Schedulers: b.Schedulers}
		inWin, last := []CPUSample{}, (*CPUSample)(nil)
		for i := range b.CPURecords {
			r := b.CPURecords[i]
			if r.At.After(a.At) && !r.At.After(b.At) {
				inWin = append(inWin, r)
			} else if !r.At.After(a.At) && (last == nil || r.At.After(last.At)) {
				last = &b.CPURecords[i]
			}
		}
		c.InWindow = len(inWin) > 0
		if !c.InWindow && last != nil {
			inWin = []CPUSample{*last}
		}
		if len(inWin) > 0 {
			var sq, ot, id int
			for _, r := range inWin {
				sq, ot, id = sq+r.SQL, ot+r.Other, id+r.Idle
			}
			c.SQL, c.Other, c.Idle, c.Records = sq/len(inWin), ot/len(inWin), id/len(inWin), len(inWin)
		}
		if totalWait > 0 {
			c.SignalWaitPct, c.HasSignalShare = math.Round(float64(totalSignal)*1000/float64(totalWait))/10, true
		}
		s.CPU = c
	}

	// Top queries.
	if e := sampleNotRead(a.QueriesError, b.QueriesError); e != "" {
		s.QueriesError = e
	} else {
		for _, d := range queryDeltas(a, b) {
			if len(s.Queries) == 5 {
				break
			}
			t, ok := in.QueryTexts[d.QueryHash]
			if !ok {
				continue // kakapo's own or a noise statement, or gone from the cache
			}
			s.Queries = append(s.Queries, SampleQuery{QueryHash: d.QueryHash, Database: t.Database, ObjectName: t.ObjectName,
				Text: t.Text, CPUMs: float64(d.CPU) / 1000, Runs: d.Execs, Reads: d.Reads, DurationMs: float64(d.Elapsed) / 1000})
		}
	}

	// Files and growth.
	if e := sampleNotRead(a.FilesError, b.FilesError); e != "" {
		s.FilesError = e
	} else {
		type key struct {
			db string
			id int
		}
		before := map[key]SampleFileStats{}
		for _, f := range a.Files {
			before[key{f.Database, f.FileID}] = f
		}
		for _, f := range b.Files {
			p, ok := before[key{f.Database, f.FileID}]
			if !ok {
				continue // a file added during the sample
			}
			x := SampleFile{Database: f.Database, File: f.File, Type: f.Type, Reads: max(f.Reads-p.Reads, 0), Writes: max(f.Writes-p.Writes, 0),
				ReadMB: mb2(f.ReadBytes - p.ReadBytes), WriteMB: mb2(f.WriteBytes - p.WriteBytes)}
			if x.Reads > 0 {
				x.MsPerRead = math.Round(float64(f.ReadStallMs-p.ReadStallMs)/float64(x.Reads)*10) / 10
			}
			if x.Writes > 0 {
				x.MsPerWrite = math.Round(float64(f.WriteStallMs-p.WriteStallMs)/float64(x.Writes)*10) / 10
			}
			s.Files = append(s.Files, x)
			if f.SizeBytes != p.SizeBytes {
				s.Growth = append(s.Growth, SampleGrowth{Database: f.Database, File: f.File, Type: f.Type, BeforeMB: mb2(p.SizeBytes), AfterMB: mb2(f.SizeBytes)})
			}
		}
		sort.SliceStable(s.Files, func(i, j int) bool {
			return s.Files[i].Reads+s.Files[i].Writes > s.Files[j].Reads+s.Files[j].Writes
		})
	}

	// Blocking: the head blockers of either snapshot, the latest sight of each.
	if e := sampleNotRead(a.BlockingError, b.BlockingError); e != "" {
		s.BlockingError = e
	} else {
		seen := map[int]int{}
		for i, snap := range [][]Blocker{a.Blockers, b.Blockers} {
			for _, x := range snap {
				if x.SelfBlocked && x.Blocked == 0 {
					continue
				}
				sb := SampleBlocker{Session: x.Session, Blocked: x.Blocked, LongestSec: x.LongestSec, Database: x.Database, Login: x.Login,
					Host: x.Host, Program: x.Program, Status: x.Status, Statement: oneLine(left(x.LastStatement, 300))}
				if j, ok := seen[x.Session]; ok {
					sb.BothEnds = i == 1
					s.Blocking.HeadBlockers[j] = sb
					continue
				}
				seen[x.Session] = len(s.Blocking.HeadBlockers)
				s.Blocking.HeadBlockers = append(s.Blocking.HeadBlockers, sb)
			}
		}
		sort.SliceStable(s.Blocking.HeadBlockers, func(i, j int) bool {
			return s.Blocking.HeadBlockers[i].LongestSec > s.Blocking.HeadBlockers[j].LongestSec
		})
		for _, x := range s.Blocking.HeadBlockers {
			s.Blocking.Blocked = max(s.Blocking.Blocked, x.Blocked)
			s.Blocking.LongestSec = max(s.Blocking.LongestSec, x.LongestSec)
		}
	}

	// Running: as last seen; an operation that finished during the sample as first seen.
	if e := sampleNotRead(a.RunningError, b.RunningError); e != "" {
		s.RunningError = e
	} else {
		byKey := map[string]int{}
		for _, snap := range [][]RunningCommand{a.Running, b.Running} {
			for _, r := range snap {
				k := fmt.Sprintf("%d|%s", r.Session, r.Command)
				if j, ok := byKey[k]; ok {
					s.Running[j] = r
					continue
				}
				byKey[k] = len(s.Running)
				s.Running = append(s.Running, r)
			}
		}
	}

	// Memory.
	if e := sampleNotRead(a.MemoryError, b.MemoryError); e != "" {
		s.MemoryError = e
	} else {
		s.Memory = &SampleMemory{PLEStart: a.PageLifeExpectancy, PLEEnd: b.PageLifeExpectancy,
			PendingStart: a.GrantsPending, PendingEnd: b.GrantsPending}
	}

	s.Lines = sampleLines(s, totalWait)
	s.Verdict = SampleVerdict(s)
	return s
}

func mb2(bytes int64) float64 {
	return math.Round(float64(bytes)/1024/1024*10) / 10
}

func isPoison(waitType string) bool {
	for _, p := range poisonWaits {
		if strings.EqualFold(p, waitType) {
			return true
		}
	}
	return false
}

// Thresholds of the sample's sentences.
const (
	sampleSlowReadMs   = 20 // STO09: data file reads
	sampleSlowWriteMs  = 10 // STO09: log file writes
	sampleMinIOs       = 20 // reads or writes in the sample before a latency counts
	sampleCPUBusy      = 80 // % CPU in total
	sampleCPUOther     = 25 // % used by other programs
	sampleQueryCPUPart = 25 // % of the server's CPU one query used
	samplePoisonMs     = 1000
	samplePLELow       = 300
)

func duration(sec int64) string {
	switch {
	case sec >= 3600:
		return fmt.Sprintf("%d hours %d minutes", sec/3600, sec%3600/60)
	case sec >= 120:
		return fmt.Sprintf("%d minutes", sec/60)
	case sec == 1:
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", sec)
}

func seconds1(ms float64) string { return n1(ms / 1000) }

// queryLabel names a query in a sentence: the procedure, or the start of its text.
func queryLabel(q SampleQuery) string {
	if q.ObjectName != "" {
		return q.ObjectName
	}
	t := oneLine(q.Text)
	if r := []rune(t); len(r) > 80 {
		t = string(r[:80]) + "…"
	}
	return "\"" + t + "\""
}

// sampleLines are the sentences of DESIGN 5.3 that fire, in its order: blocking, operations
// running, file growth, poison waits, the top waits, CPU, file latency, memory, the query that
// used most CPU.
func sampleLines(s PerfSample, totalWait int64) []SampleLine {
	out := []SampleLine{}
	n := s.Seconds
	window := s.ElapsedSeconds
	if window <= 0 {
		window = float64(n)
	}

	for _, x := range s.Blocking.HeadBlockers {
		if x.Blocked == 0 {
			continue
		}
		who := x.Program
		if who == "" {
			who = x.Login
		}
		if x.Host != "" {
			who += ", host " + x.Host
		}
		t := fmt.Sprintf("Session %d (%s) has blocked %s for %s.", x.Session, who, plural(int64(x.Blocked), "session", "sessions"), duration(x.LongestSec))
		if x.Statement != "" {
			st := x.Statement
			if r := []rune(st); len(r) > 120 {
				st = string(r[:120]) + "…"
			}
			t += " It is running " + st + "."
		}
		if strings.EqualFold(x.Status, "sleeping") {
			t += " It is sleeping with its transaction open: the program that owns it has not committed."
		}
		t += " Do not kill it until you know who owns it."
		out = append(out, SampleLine{Area: SampleAreaBlocking, Text: t})
	}

	for _, r := range s.Running {
		t := fmt.Sprintf("%s is running", r.Command)
		if r.Database != "" {
			t += " on " + r.Database
		}
		if r.PercentDone > 0 {
			t += fmt.Sprintf(": %s%% done", n0(r.PercentDone))
			if r.LeftSeconds > 0 {
				t += ", about " + duration(r.LeftSeconds) + " to go"
			}
		}
		out = append(out, SampleLine{Area: SampleAreaRunning, Text: t + ". It competes with users for disk and CPU while it runs."})
	}

	for _, g := range s.Growth {
		verb, by := "grew", g.AfterMB-g.BeforeMB
		if by < 0 {
			verb, by = "shrank", -by
		}
		what := "data file"
		if g.Type == "LOG" {
			what = "log file"
		}
		t := fmt.Sprintf("The %s %s of %s %s by %s MB during the sample.", what, g.File, g.Database, verb, n0(by))
		if verb == "grew" {
			t += " Users wait while a file grows: set a fixed growth of 256 MB so it grows rarely and quickly."
		} else {
			t += " Something is shrinking it: find the job or setting (AUTO_SHRINK) and stop it."
		}
		out = append(out, SampleLine{Area: SampleAreaGrowth, Text: t})
	}

	for _, w := range s.Waits {
		if w.Poison && w.WaitMs >= samplePoisonMs {
			out = append(out, SampleLine{Area: SampleAreaPoison, Text: fmt.Sprintf("Poison wait %s: %s seconds of waiting in %d seconds. %s",
				w.Type, seconds1(float64(w.WaitMs)), n, poisonDo(w.Type))})
		}
	}

	// The top waits fire when tasks waited, on average, at least one at a time for the whole sample.
	if len(s.Waits) > 0 && float64(totalWait) >= window*1000 {
		top := s.Waits[0]
		names := []string{}
		for i, w := range s.Waits {
			if i == 3 {
				break
			}
			names = append(names, fmt.Sprintf("%s %s s", w.Type, seconds1(float64(w.WaitMs))))
		}
		out = append(out, SampleLine{Area: SampleAreaWaits, Text: fmt.Sprintf("Most of the waiting in this sample was %s (%s). %s",
			top.Words, strings.Join(names, ", "), wrk01Do[top.Group])})
	}

	if c := s.CPU; c != nil && c.Records > 0 {
		total := c.SQL + c.Other
		when := ""
		if !c.InWindow {
			when = " in the minute before the sample"
		}
		switch {
		case total >= sampleCPUBusy && c.Other >= sampleCPUOther:
			out = append(out, SampleLine{Area: SampleAreaCPU, Text: fmt.Sprintf("CPU %d%%%s, %d%% of it not SQL Server: look at what else runs on this host (antivirus, backup agent).", total, when, c.Other*100/max(total, 1))})
		case total >= sampleCPUBusy:
			out = append(out, SampleLine{Area: SampleAreaCPU, Text: fmt.Sprintf("CPU %d%%%s, nearly all of it SQL Server: the costliest queries below are where the time goes.", total, when)})
		case c.Other >= sampleCPUOther:
			out = append(out, SampleLine{Area: SampleAreaCPU, Text: fmt.Sprintf("Other programs used %d%% of the CPU%s: look at what else runs on this host (antivirus, backup agent).", c.Other, when)})
		}
		if c.Schedulers > 0 && max(c.RunnableStart, c.RunnableEnd) >= 2*c.Schedulers {
			out = append(out, SampleLine{Area: SampleAreaCPU, Text: fmt.Sprintf("%d queries were waiting for a CPU on %d schedulers: the server is short of CPU right now.", max(c.RunnableStart, c.RunnableEnd), c.Schedulers)})
		}
	}

	for _, f := range s.Files {
		switch {
		case f.Type != "LOG" && f.Reads >= sampleMinIOs && f.MsPerRead >= sampleSlowReadMs:
			out = append(out, SampleLine{Area: SampleAreaFiles, Text: fmt.Sprintf("Reads from %s (%s) took %s ms each during the sample (%s reads); over %d ms is slow for a data file.",
				f.File, f.Database, n1(f.MsPerRead), n0int(f.Reads), sampleSlowReadMs)})
		case f.Type == "LOG" && f.Writes >= sampleMinIOs && f.MsPerWrite >= sampleSlowWriteMs:
			out = append(out, SampleLine{Area: SampleAreaFiles, Text: fmt.Sprintf("Writes to the log %s (%s) took %s ms each during the sample (%s writes); every commit waits for these, and over %d ms is slow.",
				f.File, f.Database, n1(f.MsPerWrite), n0int(f.Writes), sampleSlowWriteMs)})
		}
	}

	if m := s.Memory; m != nil {
		if pending := max(m.PendingStart, m.PendingEnd); pending > 0 {
			out = append(out, SampleLine{Area: SampleAreaMemory, Text: fmt.Sprintf("%s waiting for memory before they could start (memory grants pending): other queries hold it all, usually a few huge sorts or hashes.",
				plural(pending, "query was", "queries were"))})
		}
		if m.PLEStart > 0 && m.PLEEnd >= 0 && m.PLEEnd < samplePLELow && m.PLEEnd*2 <= m.PLEStart {
			out = append(out, SampleLine{Area: SampleAreaMemory, Text: fmt.Sprintf("Page life expectancy fell from %s to %s seconds during the sample: a query is reading more data than memory holds, pushing everything else out.",
				n0int(m.PLEStart), n0int(m.PLEEnd))})
		}
	}

	if len(s.Queries) > 0 && s.CPU != nil && s.CPU.Schedulers > 0 {
		q := s.Queries[0]
		capacity := window * 1000 * float64(s.CPU.Schedulers)
		if pct := q.CPUMs * 100 / capacity; pct >= sampleQueryCPUPart {
			out = append(out, SampleLine{Area: SampleAreaQueries, QueryHash: q.QueryHash, Text: fmt.Sprintf("The query hurting now is %s: %s s of CPU in %d seconds (%s%% of the server), %s.",
				queryLabel(q), seconds1(q.CPUMs), n, n0(pct), plural(q.Runs, "run", "runs"))})
		}
	}
	return out
}

func poisonDo(waitType string) string {
	switch strings.ToUpper(waitType) {
	case "THREADPOOL":
		return "SQL Server ran out of worker threads: find the blocking or the parallelism that uses them up."
	case "RESOURCE_SEMAPHORE":
		return "Queries waited for memory before they could start: find the queries with large memory grants."
	case "RESOURCE_SEMAPHORE_QUERY_COMPILE":
		return "Too many queries compiled at once: something is clearing the plan cache, or the code compiles for every value."
	}
	return "A wait that normally never appears: escalate to a DBA with this wait type."
}
