package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// PerformanceFacts is what the Performance card reads about one database and the instance it runs
// on (docs/phase3e-contract.md, "Reads"). Every time is in the server's own clock, as GETDATE()
// returns it, carried in a time.Time with the UTC location. Each area that could not be read
// carries its reason; the checks that need it are skipped, the rest still run.
type PerformanceFacts struct {
	ServerName     string // @@SERVERNAME
	ProductVersion string
	EngineEdition  int // 3 is Enterprise (and Developer): online index creation

	Database string
	State    string // sys.databases state_desc
	RCSI     bool   // is_read_committed_snapshot_on: decides the blocking Do this (docs/phase3f-contract.md, item 20)

	StartedAt   time.Time // sqlserver_start_time
	UptimeHours int       // DATEDIFF(HOUR, sqlserver_start_time, GETDATE()), as the scan counts it

	// WRK01, WRK02, WRK05: sys.dm_os_wait_stats with the benign list and SLEEP_% left out.
	Waits      []WaitStat
	WaitsError string

	// WRK06 and SRV07: sys.dm_os_performance_counters, cumulative since the restart.
	Deadlocks     int64
	Batches       int64
	Compiles      int64
	Recompiles    int64
	CountersError string

	// ACT01: one row per head blocker across the instance.
	Blockers      []Blocker
	BlockingError string

	// ACT02: transactions open more than 5 minutes in this database; WRK07: user sessions sleeping
	// with a transaction open more than 5 minutes, instance-wide.
	OpenTransactions  []OpenTransaction
	IdleTransactions  []IdleTransaction
	TransactionsError string

	// PRS01: sys.dm_os_schedulers (VISIBLE ONLINE) and sys.dm_os_sys_info.
	Schedulers      *SchedulerLoad
	SchedulersError string
	// PRS01 and PRS02: sys.dm_exec_query_resource_semaphores, and the largest grants held now.
	Grants      *MemoryGrants
	GrantsError string

	// WRK08: the scheduler monitor ring buffer, the last 256 one-minute records, oldest first.
	CPUHistory      []CPUSample
	CPUHistoryError string

	// SRV04: query CPU per database from the plan cache, disk reads from the file statistics.
	Loads     []DatabaseLoad
	LoadError string

	// WRK03: the missing index DMVs for this database, top 5 by the scan's score.
	MissingIndexes    []MissingIndex
	MissingIndexError string

	// QRY01: the top 5 query shapes of this database by total CPU, from Query Store over the last
	// 7 days (QuerySource QuerySourceQueryStore) or the plan cache since the restart
	// (QuerySourcePlanCache); TotalCPU is what their share is taken of, in microseconds.
	QuerySource     string
	TopQueries      []TopQuery
	TotalCPU        int64
	QueryStoreState string // actual_state_desc; "" when not read
	QueryStoreError string // why Query Store could not be read, when it could not
	QueriesError    string // neither Query Store nor the plan cache could be read

	// QRY01's "archive before an index": the tables that only grow that nothing cleans up
	// (GrowingCandidates, the Storage card's IDX10 rule), read beside these facts. nil when they or
	// kakapo's archive jobs could not be read: QRY01 then says nothing about archiving.
	GrowingCandidates []GrowingTable

	ServerNow        time.Time // GETDATE()
	UTCOffsetMinutes int

	// Phase 5k (docs/phase5k-contract.md): the queries that got a new plan in the 7 days and the
	// objects changed in 8 (QRY01's batch, Query Store only), and the hours of the days before (the
	// load strip's read, nil when it was not read).
	Movers      []PlanMoverFact
	Changed     []ChangedObjectFact
	MoversError string
	Load        *LoadFacts

	Error string // nothing could be read at all
}

// WaitStat is one row of sys.dm_os_wait_stats.
type WaitStat struct {
	Type      string
	WaitMs    int64
	SignalMs  int64
	Tasks     int64
	MaxWaitMs int64
}

// Blocker is a head blocker: a session that blocks others and is not blocked itself (ACT01).
type Blocker struct {
	Session       int
	Blocked       int   // requests waiting on it
	LongestSec    int64 // MAX(wait_time) / 1000 of those
	Database      string
	Login         string
	Host          string
	Program       string
	Status        string
	OpenTran      int
	LastStatement string
	SelfBlocked   bool // it waits on its own parallel threads (blocking_session_id = session_id)
}

// OpenTransaction is a transaction open more than 5 minutes in this database (ACT02).
type OpenTransaction struct {
	Session       int
	Login         string
	Host          string
	Program       string
	Status        string
	Minutes       int
	Started       time.Time
	LogBytes      int64
	LastStatement string
}

// IdleTransaction is a user session sleeping with a transaction open more than 5 minutes (WRK07).
type IdleTransaction struct {
	Session  int
	Login    string
	Host     string
	Program  string
	Minutes  int
	Implicit bool
}

// SchedulerLoad is the worker threads and schedulers right now (PRS01).
type SchedulerLoad struct {
	MaxWorkers        int   // sys.dm_os_sys_info max_workers_count
	CurrentWorkers    int   // current_workers_count summed over the online schedulers
	OnlineSchedulers  int   // VISIBLE ONLINE
	RunnableTasks     int   // runnable_tasks_count summed
	BusySchedulers    int   // online schedulers with a runnable task
	MaxRunnable       int   // the most runnable tasks on one scheduler
	OfflineSchedulers int   // VISIBLE OFFLINE
	AffinityManual    bool  // affinity_type_desc MANUAL: someone chose the offline ones
	WorkQueue         int64 // work_queue_count summed: tasks waiting for a thread
}

// MemoryGrants is sys.dm_exec_query_resource_semaphores summed (PRS01, PRS02).
type MemoryGrants struct {
	Waiting  int64 // waiter_count: queries waiting for memory now
	Granted  int64 // grantee_count
	Forced   int64 // forced_grant_count since restart
	Timeouts int64 // timeout_error_count since restart: queries that gave up (error 8645)
	// Holders are the three largest grants held right now, read only when Waiting > 0.
	Holders []GrantHolder
}

// GrantHolder is one query holding a memory grant right now (sys.dm_exec_query_memory_grants).
type GrantHolder struct {
	Session     int
	GrantedKB   int64
	RequestedKB int64
	WaitMs      int64  // how long it waited for the grant
	Statement   string // the first 200 characters
}

// CPUSample is one minute of the scheduler monitor ring buffer (WRK08): CPU percentages used by SQL
// Server, by other programs (100 - SQL Server - idle) and idle.
type CPUSample struct {
	At    time.Time // server clock
	SQL   int
	Other int
	Idle  int
}

// DatabaseLoad is one database's query CPU in the plan cache and disk reads since the restart (SRV04).
type DatabaseLoad struct {
	DatabaseID int
	Name       string // "database {id}" when it has none
	CPU        int64
	ReadBytes  int64
}

// MissingIndex is one of SQL Server's missing index suggestions (WRK03).
type MissingIndex struct {
	Statement  string // [db].[schema].[table]
	Equality   string
	Inequality string
	Included   string
	Uses       int64 // user_seeks + user_scans
	Impact     float64
	Score      float64
	LastSeek   *time.Time
}

// TopQuery is one query shape of this database among the top 5 by total CPU (QRY01). Times are
// in microseconds, as both sources keep them.
type TopQuery struct {
	QueryHash  string // 0x...
	ObjectName string // schema.procedure; "" for ad hoc
	Text       string // the statement
	CPU        int64
	Execs      int64
	Reads      int64
	MinReads   int64
	MaxReads   int64
	MaxCPU     int64
	Elapsed    int64
	Variants   int // separate statements of the same shape
	Minutes    int // from the first run (or first cached) to now
	TopWait    string
	Plan       string // the plan XML; "" when it was not read or too large
	PlanTooBig bool
	MaxElapsed int64 // the longest single run

	// Spills, grants and TempDB (docs/phase3f-contract.md, item 1), each -1 when the source has no
	// such column: Spills is total_spills in pages (plan cache, 2016 SP2 and later); MaxGrantKB and
	// MaxUsedGrantKB are max_grant_kb and max_used_grant_kb (plan cache), MaxUsedGrantKB also
	// max_query_max_used_memory (Query Store); TempdbKB is avg_tempdb_space_used in pages times 8
	// times the runs, summed (Query Store, 2017 and later).
	Spills         int64
	MaxGrantKB     int64
	MaxUsedGrantKB int64
	TempdbKB       int64

	// Plans is how many plans the query had in the window, LowRunPlans how many of them ran under
	// five times (item 19).
	Plans       int
	LowRunPlans int

	// Phase 5j (Query Store only): when the plan in use began (UTC; nil when unknown), and the runs
	// and CPU (microseconds) before and since, every plan together.
	Since      *time.Time
	BeforeRuns int64
	BeforeCPU  int64
	NowRuns    int64
	NowCPU     int64
}

// Query sources of QRY01.
const (
	QuerySourceQueryStore = "query-store"
	QuerySourcePlanCache  = "plan-cache"
)

// AsOf is ServerNow in UTC.
func (f PerformanceFacts) AsOf() time.Time {
	t := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute)
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// uptimeDays is the scan's @UptimeDays: 1 under a day, else hours / 24 as decimal(9, 2).
func (f PerformanceFacts) uptimeDays() float64 {
	if f.UptimeHours < 24 {
		return 1
	}
	return math.Round(float64(f.UptimeHours)/24*100) / 100
}

// BenignWaits is the scan's list of wait types that are normal background noise (01-QuickScan.sql
// section 7); SLEEP_% is left out as well.
var BenignWaits = []string{
	"BROKER_EVENTHANDLER", "BROKER_RECEIVE_WAITFOR", "BROKER_TASK_STOP", "BROKER_TO_FLUSH", "BROKER_TRANSMITTER",
	"CHECKPOINT_QUEUE", "CHKPT", "CLR_AUTO_EVENT", "CLR_MANUAL_EVENT", "CLR_SEMAPHORE", "CXCONSUMER",
	"DBMIRROR_DBM_EVENT", "DBMIRROR_EVENTS_QUEUE", "DBMIRROR_WORKER_QUEUE", "DBMIRRORING_CMD", "DIRTY_PAGE_POLL",
	"DISPATCHER_QUEUE_SEMAPHORE", "EXECSYNC", "FSAGENT", "FT_IFTS_SCHEDULER_IDLE_WAIT", "FT_IFTSHC_MUTEX",
	"HADR_CLUSAPI_CALL", "HADR_FILESTREAM_IOMGR_IOCOMPLETION", "HADR_LOGCAPTURE_WAIT", "HADR_NOTIFICATION_DEQUEUE",
	"HADR_TIMER_TASK", "HADR_WORK_QUEUE", "KSOURCE_WAKEUP", "LAZYWRITER_SLEEP", "LOGMGR_QUEUE",
	"MEMORY_ALLOCATION_EXT", "ONDEMAND_TASK_QUEUE", "PARALLEL_REDO_DRAIN_WORKER", "PARALLEL_REDO_LOG_CACHE",
	"PARALLEL_REDO_TRAN_LIST", "PARALLEL_REDO_WORKER_SYNC", "PARALLEL_REDO_WORKER_WAIT_WORK",
	"PREEMPTIVE_OS_FLUSHFILEBUFFERS", "PREEMPTIVE_XE_GETTARGETSTATE", "PVS_PREALLOCATE",
	"PWAIT_ALL_COMPONENTS_INITIALIZED", "PWAIT_DIRECTLOGCONSUMER_GETNEXT", "PWAIT_EXTENSIBILITY_CLEANUP_TASK",
	"QDS_PERSIST_TASK_MAIN_LOOP_SLEEP", "QDS_ASYNC_QUEUE", "QDS_CLEANUP_STALE_QUERIES_TASK_MAIN_LOOP_SLEEP",
	"QDS_SHUTDOWN_QUEUE", "REDO_THREAD_PENDING_WORK", "REQUEST_FOR_DEADLOCK_SEARCH", "RESOURCE_QUEUE",
	"SERVER_IDLE_CHECK", "SLEEP_BPOOL_FLUSH", "SLEEP_DBSTARTUP", "SLEEP_DCOMSTARTUP", "SLEEP_MASTERDBREADY",
	"SLEEP_MASTERMDREADY", "SLEEP_MASTERUPGRADED", "SLEEP_MSDBSTARTUP", "SLEEP_SYSTEMTASK", "SLEEP_TASK",
	"SLEEP_TEMPDBSTARTUP", "SNI_HTTP_ACCEPT", "SOS_WORK_DISPATCHER", "SP_SERVER_DIAGNOSTICS_SLEEP",
	"SQLTRACE_BUFFER_FLUSH", "SQLTRACE_INCREMENTAL_FLUSH_SLEEP", "SQLTRACE_WAIT_ENTRIES", "VDI_CLIENT_OTHER",
	"WAIT_FOR_RESULTS", "WAITFOR", "WAITFOR_TASKSHUTDOWN", "WAIT_XTP_RECOVERY", "WAIT_XTP_HOST_WAIT",
	"WAIT_XTP_OFFLINE_CKPT_NEW_LOG", "WAIT_XTP_CKPT_CLOSE", "XE_DISPATCHER_JOIN", "XE_DISPATCHER_WAIT",
	"XE_TIMER_EVENT", "XE_LIVE_TARGET_TVF",
}

var benignWaits = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range BenignWaits {
		m[w] = true
	}
	return m
}()

// IsBenignWait says whether a wait type is background noise the scan leaves out: on the benign
// list, or SLEEP_%.
func IsBenignWait(waitType string) bool {
	return benignWaits[strings.ToUpper(waitType)] || strings.HasPrefix(strings.ToUpper(waitType), "SLEEP_")
}

// Wait groups, as the scan names them.
const (
	waitPAR     = "PAR"
	waitIO      = "IO"
	waitLOG     = "LOG"
	waitLOCK    = "LOCK"
	waitCPU     = "CPU"
	waitMEM     = "MEM"
	waitNET     = "NET"
	waitPLATCH  = "PLATCH"
	waitTHREAD  = "THREAD"
	waitAG      = "AG"
	waitOTHERIO = "OTHERIO"
	waitEXT     = "EXT"
	waitLATCH   = "LATCH"
	waitOTHER   = "OTHER"
)

// WaitGroup is the scan's group of a wait type (01-QuickScan.sql, WRK01).
func WaitGroup(waitType string) string {
	w := strings.ToUpper(waitType)
	switch {
	case w == "CXPACKET" || w == "CXSYNC_PORT" || w == "CXSYNC_CONSUMER":
		return waitPAR
	case strings.HasPrefix(w, "PAGEIOLATCH_"):
		return waitIO
	case w == "WRITELOG":
		return waitLOG
	case strings.HasPrefix(w, "LCK_M_"):
		return waitLOCK
	case w == "SOS_SCHEDULER_YIELD":
		return waitCPU
	case strings.HasPrefix(w, "RESOURCE_SEMAPHORE"):
		return waitMEM
	case w == "ASYNC_NETWORK_IO":
		return waitNET
	case strings.HasPrefix(w, "PAGELATCH_"):
		return waitPLATCH
	case w == "THREADPOOL":
		return waitTHREAD
	case w == "HADR_SYNC_COMMIT":
		return waitAG
	case w == "IO_COMPLETION" || w == "ASYNC_IO_COMPLETION" || w == "BACKUPIO" || w == "BACKUPBUFFER":
		return waitOTHERIO
	case strings.HasPrefix(w, "PREEMPTIVE_") || w == "OLEDB":
		return waitEXT
	case strings.HasPrefix(w, "LATCH_"):
		return waitLATCH
	}
	return waitOTHER
}

// waitWords are the plain words of each group for the card's fact.
var waitWords = map[string]string{
	waitPAR: "parallelism", waitIO: "reading from disk", waitLOG: "log writes", waitLOCK: "blocking", waitCPU: "CPU",
	waitMEM: "memory grants", waitNET: "the application", waitPLATCH: "page contention", waitTHREAD: "worker threads",
	waitAG: "availability group", waitOTHERIO: "other I/O", waitEXT: "outside calls", waitLATCH: "internal latches",
	waitOTHER: "other",
}

// WRK01's why and do this per group, verbatim from the scan.
var wrk01Why = map[string]string{
	waitPAR:     "Parallelism: queries split across several CPUs and wait for each other to finish.",
	waitIO:      "Reading data from disk: the data a query needs is not in memory.",
	waitLOG:     "Writing to the transaction log: every commit waits for this.",
	waitLOCK:    "Blocking: sessions waiting for locks held by other sessions.",
	waitCPU:     "CPU: queries waiting their turn on the processors.",
	waitMEM:     "Memory grants: queries waiting for memory before they can even start.",
	waitNET:     "SQL Server waiting for the application to read the results it asked for.",
	waitPLATCH:  "In-memory page contention, often TempDB allocation contention or many inserts into the same page.",
	waitTHREAD:  "Worker thread starvation: SQL Server ran out of threads to serve requests. This is serious.",
	waitAG:      "Waiting for a synchronous Availability Group replica to confirm each commit.",
	waitOTHERIO: "Other I/O: backups, file growth, or sorts and hashes spilling to disk.",
	waitEXT:     "Calls outside SQL Server: operating system, linked servers, CLR or external procedures.",
	waitLATCH:   "Contention on internal SQL Server structures.",
	waitOTHER:   "A less common wait type.",
}

var wrk01Do = map[string]string{
	waitPAR:     "Usually tuned with MAXDOP and cost threshold for parallelism (INS04, INS05). If those are fine, look at the biggest queries (QRY01) and missing indexes (WRK03).",
	waitIO:      "Often caused by large scans from missing indexes (WRK03) or too little memory (INS02/INS03), not only slow disks. Compare with storage latency (STO09).",
	waitLOG:     "Check log write latency (STO09). Fast storage for log files helps, as does the application batching many tiny transactions.",
	waitLOCK:    "Find the blocking queries: ACT01 and the Blocking page show who blocks whom right now, and the blocking history (BLK01) shows who blocked users this week and how often. Long transactions, missing indexes and large updates are the usual causes.",
	waitCPU:     "Find and tune the most CPU-expensive queries (QRY01) and check signal waits (WRK02).",
	waitMEM:     "Usually large sorts or hashes from poor plans or out-of-date statistics (MNT04, QRY01). Also check max server memory.",
	waitNET:     "Usually the application processes rows one at a time or fetches far more rows than it needs. Talk to the developers; it is rarely the network.",
	waitPLATCH:  "Check TempDB data files (TMP01, TMP02).",
	waitTHREAD:  "Usually caused by heavy blocking or too much parallelism. Escalate to a DBA.",
	waitAG:      "Check the network to the secondary replica and the storage speed on the secondary.",
	waitOTHERIO: "Normal during backup windows. Otherwise check instant file initialization (INS07) and autogrowth settings (STO02/STO03).",
	waitEXT:     "Look at linked server queries and other external calls made by the application.",
	waitLATCH:   "Needs a DBA to investigate further.",
	waitOTHER:   "Look the wait type up in the Microsoft documentation or the SQLskills wait types library.",
}

// Texts from 01-QuickScan.sql (WRK02, WRK03, WRK05, WRK06, WRK07) and PerformanceTriage.sql (SRV04,
// SRV07, ACT01, ACT02, ACT03), verbatim; PRS01's, QRY01's source note and DLK01's in the same voice.
const (
	wrk02Why        = "Signal wait is time a query was ready to run but had no free CPU. A high share means the server is short of CPU."
	wrk02Do         = "Tune the most CPU-expensive queries (QRY01), check MAXDOP and cost threshold (INS04, INS05), and confirm the VM has enough CPU and is not over-committed on its host."
	wrk03Why        = "Queries that would use this index are scanning more data than they need, which costs CPU, memory and disk reads."
	wrk03Do         = "Have a developer or DBA review it before creating anything: check whether an existing index can be extended instead, combine overlapping suggestions, and never create them all blindly (each index slows down inserts and updates). If this is a vendor database, check with the vendor. "
	wrk03DoOnline   = "Create it WITH (ONLINE = ON)."
	wrk03DoOffline  = "Index creation locks the table on this edition: create it off-peak."
	wrk05OtherWhy   = "A wait that normally never appears; even a few seconds mean something is wrong."
	wrk05OtherDo    = "Escalate to a DBA with this wait type and the evidence."
	wrk06Why        = "In a deadlock two sessions each wait for the other, and SQL Server kills one. The killed one gets an error and its work is rolled back: a failed scan, a failed integration post."
	wrk06Do         = "The deadlock graphs are in the system_health Extended Events session (SSMS: Management > Extended Events > Sessions > system_health > package0.event_file, filter on xml_deadlock_report). Group them by the two procedures involved: the fix is usually an index, or the order in which the code touches the tables."
	wrk07Why        = "An idle session with an open transaction keeps its locks until it commits, which may be never. Everything that touches those rows waits. Implicit transactions come from a connection setting (ODBC, JDBC, some ERP components), not from the code."
	wrk07Do         = "Find the program and host in the evidence and raise it with the vendor or developer: the connection should commit, or switch off IMPLICIT_TRANSACTIONS. Do not kill the sessions without knowing what they do."
	srv04Why        = "Not every slowness comes from this database. Other systems on the same server compete for the same CPU, memory and disk."
	srv04DoOther    = "Measure before accepting or rejecting blame: look at the busiest other databases below, and ask their owners what runs there during working hours (reports, monitoring tools reading whole tables, web shops)."
	srv04DoMine     = "This database is the main user of the server's CPU, so its own queries are the place to look (ACT03)."
	srv04DoNone     = "No other database stands out. See the busiest databases below."
	srv07Why        = "Compiling a plan costs CPU before the query even starts. Code that builds SQL with the values typed in compiles for every value; SQL Server should reuse one plan per query shape."
	srv07Do         = "ACT03 shows which queries arrive as many separate statements: that code should pass values as parameters. Until then, optimize for ad hoc workloads (SRV06) limits the memory cost but not the CPU cost."
	act01Why        = "Blocked users see the application hang. A blocker that is \"sleeping\" with an open transaction is usually a forgotten transaction in an application or an SSMS window."
	act01Do         = "Find out what the blocking session is doing and who owns it before acting. Do not kill sessions without checking: rolling back can take as long again."
	act02Why        = "An open transaction keeps its locks, which blocks other users, and stops the log from being emptied so it keeps growing."
	act02Do         = "Ask the owner of the session or program to commit or close it. A sleeping session with an open transaction is usually a bug in the application or a forgotten SSMS window."
	qry01Why        = "A handful of queries usually cause most of the load. Many page reads per run means the query reads far more data than it returns."
	qry01Do         = "Give this to someone who can read execution plans. Do not add an index for each query: check IDX02 first, as this table may already have too many."
	qry01SpillDo    = " A spill means the memory estimate was wrong: stale statistics (MNT04) or a bad row estimate; update statistics first."
	qry01GrantDo    = " An oversized grant blocks other queries' memory (PRS01, RESOURCE_SEMAPHORE): the estimate is too high, usually a stale statistic or a wide SELECT."
	qry01PlansDo    = " Plans that never get reused usually mean the values are typed into the SQL text (SRV06) or the options differ per connection (ARITHABORT, see below)."
	qry01Arithabort = " In SSMS run SET ARITHABORT OFF before testing a query from the application, or you get a different plan than the application does."
	act01RCSIOff    = " If the blocking keeps coming back, the lasting fixes are an index so each statement touches fewer rows, shorter transactions, or READ COMMITTED SNAPSHOT: see Decisions at the top of the page."
	qry01RCSIOff    = " For the blocking: an index so it touches fewer rows, or READ COMMITTED SNAPSHOT: see Decisions at the top of the page."
	rcsiOn          = " The database already has READ COMMITTED SNAPSHOT, so this blocking is writers against writers or a lock hint; the fix is in the code or the index."
	prs02Why        = "Each one is a query that failed: the user saw an error or a timeout, and the work was not done."
	prs02Do         = "Find the queries with the largest memory grants (PRS01's evidence while it happens, QRY01's memory grant warning) and fix their estimates; then check max server memory (INS02)."
	wrk08OtherWhy   = "Everything SQL Server does waits behind them: CPU those programs use is CPU no query can have, and nothing inside SQL Server can tune it."
	wrk08OtherDo    = "Find what else runs on the database server. The usual suspects: the application's own services or other services installed on it, antivirus scanning the data and log files, a web shop, backup software. Move them to another machine, or exclude the SQL Server data, log and backup files from antivirus scanning."
	wrk08SQLWhy     = "When SQL Server keeps the CPUs this busy, every query waits its turn for a processor, so the whole application slows down at once."
	wrk08SQLDo      = "Tune the costliest queries first (QRY01) and the missing indexes (WRK03); only then look at the CPU count and MAXDOP (INS04)."
	dlk01Do         = "The fix is ours: an index so each side touches fewer rows, the same order of touching the tables in both procedures, or READ COMMITTED SNAPSHOT for the database (decide first). Start with the victim's statement in the evidence."
	prs01ThreadsWhy = "Every request needs a worker thread. When they run out, new requests queue for one and users see the server freeze."
	prs01ThreadsDo  = "Find the blocking (ACT01) or the parallelism that uses up the threads (MAXDOP and cost threshold, INS04 and INS05). Never raise max worker threads; it hides the cause."
	prs01MemoryWhy  = "Queries waiting for memory cannot even start: other queries hold it all, usually a few huge sorts or hashes from bad estimates. A forced grant gave a query less memory than it asked for, so it spills to tempdb."
	prs01MemoryDo   = "Find the queries with large memory grants (QRY01 shows memory grant warnings in their plans) and fix their estimates: statistics (MNT04), missing indexes, or the query itself."
	prs01CPUWhy     = "Runnable tasks are queries ready to run with no free CPU to run on. Two or more per scheduler means the server is short of CPU right now."
	prs01CPUDo      = "Tune the most CPU-expensive queries (QRY01), check MAXDOP (INS04), and confirm the VM has the CPUs it should and is not over-committed on its host."
	perfNotRead     = "Could not read the performance facts: "
	waitsNotRead    = "Could not read the wait statistics (sys.dm_os_wait_stats, needs VIEW SERVER STATE): "
	countersNotRead = "Could not read the performance counters (sys.dm_os_performance_counters): "
	blockingNotRead = "Could not read the requests and sessions (sys.dm_exec_requests): "
	transNotRead    = "Could not read the open transactions (sys.dm_tran_*): "
	schedNotRead    = "Could not read the schedulers (sys.dm_os_schedulers): "
	grantsNotRead   = "Could not read the memory grants (sys.dm_exec_query_resource_semaphores): "
	cpuNotRead      = "Could not read the CPU history (sys.dm_os_ring_buffers): "
	loadNotRead     = "Could not read the load per database (sys.dm_exec_query_stats): "
	missingNotRead  = "Could not read the missing index suggestions: "
	queriesNotRead  = "Could not read the top queries (Query Store or the plan cache): "
	qry01FromQS     = "From Query Store, the last 7 days."
	qry01FromCache  = "From the plan cache since restart; turn Query Store on for history."
	qry01QSEmpty    = "Query Store is on but holds no application queries in the last 7 days (only maintenance and kakapo's own reads), so this is the plan cache since restart."
	// Dlk01NotRun is DLK01's note in the checks until the deadlock report is run on request.
	Dlk01NotRun = "Not run yet. Read deadlocks reads the system_health session's files for the last 7 days; it can take a minute on a busy server."
)

// RestartNote is the card's note when the server restarted less than 24 hours ago (WRK01's Info in
// the scan).
func RestartNote(hours int) string {
	return fmt.Sprintf("SQL Server restarted %d hours ago: the since-restart figures may not reflect a normal day", hours)
}

// BuildPerformanceCard answers "Is it slow, and why?" for one database from what was read: the
// card, its findings ranked (each with its clock), and what became of each check behind it, in
// catalogue order. lastDeadlocks is the last stored deadlock report, or nil when it was never run;
// it decides only DLK01's line in the checks (the card detail carries the report itself).
// lastBlocking is the last stored blocking history, or nil; it decides only BLK01's line the same way.
func BuildPerformanceCard(f PerformanceFacts, def CardDef, lastDeadlocks *DeadlockReport, lastBlocking *BlockingHistory) (Card, []Finding, []CheckStatus) {
	c := Card{ID: def.ID, Title: def.Title, Question: def.Question, Facts: []Fact{}}
	b := &perfBuild{f: f, findings: []Finding{}, notes: map[string]string{}, skipped: map[string]bool{}}

	switch {
	case f.Error != "":
		note := perfNotRead + f.Error
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID, nil, nil)
	case f.State != "ONLINE":
		note := fmt.Sprintf("%s is %s, not online, so its performance was not checked.", f.Database, f.State)
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", note
		b.skipAll(def.ID, note)
		return c, b.findings, b.checks(def.ID, nil, nil)
	}

	b.live()
	b.prs02()
	b.act02()
	if f.WaitsError != "" {
		b.skip(waitsNotRead+f.WaitsError, "WRK01", "WRK02", "WRK05")
	} else {
		b.wrk01()
		b.wrk02()
		b.wrk05()
	}
	b.wrk03()
	if f.CountersError != "" {
		b.skip(countersNotRead+f.CountersError, "WRK06", "SRV07")
	} else {
		b.wrk06()
		b.srv07()
	}
	b.wrk08()
	b.srv04()
	b.qry01()
	b.skip(Dlk01NotRun, "DLK01")
	b.skip(Blk01NotRun, "BLK01")
	RankFindings(b.findings)
	checks := b.checks(def.ID, lastDeadlocks, lastBlocking)

	if f.WaitsError != "" {
		// Without the waits the card cannot answer its question. The other checks still ran, and
		// their findings are kept for the detail page.
		c.Status, c.StatusText, c.Note = StatusGrey, "Could not be read", waitsNotRead+f.WaitsError
		return c, b.findings, checks
	}

	actionable, critical, clientIT := 0, false, 0
	for _, x := range b.findings {
		if x.Severity.Actionable() {
			actionable++
			if x.Owner == OwnerClientIT {
				clientIT++
			}
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
	c.ToDo, c.ForClientIT, c.Fixable = actionable, clientIT, 0
	asOf := f.AsOf()
	c.AsOf = &asOf
	if f.UptimeHours < 24 {
		c.Note = RestartNote(f.UptimeHours)
	}
	c.Facts = b.facts()
	if x, ok := TrendFact(BuildPerformanceTrend(f)); ok {
		c.Facts = append([]Fact{x}, c.Facts...)
	}
	return c, b.findings, checks
}

// LiveFindings are the live checks' findings, PRS01 and ACT01, ranked: the rules the card runs, from
// the one function the card calls too (docs/phase5i-contract.md, section 3), so the load strip's minute
// refresh and the card never disagree.
func LiveFindings(f PerformanceFacts) []Finding {
	b := &perfBuild{f: f, findings: []Finding{}, notes: map[string]string{}, skipped: map[string]bool{}}
	b.live()
	RankFindings(b.findings)
	return b.findings
}

// live runs the live checks: pressure now (PRS01) and blocking now (ACT01).
func (b *perfBuild) live() {
	b.prs01()
	b.act01()
}

type perfBuild struct {
	f        PerformanceFacts
	findings []Finding
	notes    map[string]string // a check's note: why it was skipped, or where it read from
	skipped  map[string]bool
}

func (b *perfBuild) add(id string, sev Severity, when, effort, clock, object, finding, why, doThis, evidence string) {
	chk, _ := CheckByID(id)
	b.findings = append(b.findings, Finding{
		CheckID: id, Title: chk.Title, Severity: sev, Owner: chk.Owner, When: when, Effort: effort, Object: object,
		Finding: finding, Why: why, DoThis: doThis, ThenCheck: chk.ThenCheck, Evidence: evidence, Clock: clock,
	})
}

func (b *perfBuild) all(id string) []Finding {
	out := []Finding{}
	for _, x := range b.findings {
		if x.CheckID == id {
			out = append(out, x)
		}
	}
	return out
}

func (b *perfBuild) skip(note string, ids ...string) {
	for _, id := range ids {
		if !b.skipped[id] {
			b.skipped[id], b.notes[id] = true, note
		}
	}
}

func (b *perfBuild) skipAll(card CardID, note string) {
	for _, c := range ChecksFor(card) {
		b.skipped[c.ID], b.notes[c.ID] = true, note
	}
}

// checks says what became of each check: a finding when it found something to act on, skipped
// with its note, passed otherwise; a note that is not a skip reason (QRY01's source, PRS01's
// partial read) is kept on any status. DLK01 and BLK01 follow their last stored report when there is one.
func (b *perfBuild) checks(card CardID, last *DeadlockReport, blocking *BlockingHistory) []CheckStatus {
	out := []CheckStatus{}
	for _, c := range ChecksFor(card) {
		st := CheckStatus{CheckID: c.ID, Title: c.Title, Status: CheckPassed, Note: b.notes[c.ID]}
		switch {
		case c.ID == "DLK01" && last != nil && b.notes[c.ID] == Dlk01NotRun:
			st.Note = ""
			for _, x := range last.Findings {
				if x.Severity.Actionable() {
					st.Status = CheckFinding
				}
			}
			if st.Status == CheckPassed {
				st.Note = fmt.Sprintf("No deadlocks in this database in the last %d days.", last.Days)
			}
		case c.ID == "BLK01" && blocking != nil && b.notes[c.ID] == Blk01NotRun:
			st = blk01Check(st, blocking)
		case b.skipped[c.ID]:
			st.Status = CheckSkipped
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

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return n0int(n) + " " + many
}

// prs01Signal is one pressure signal that fired, with its sentence, its short phrase for the card
// and its texts.
type prs01Signal struct {
	critical       bool
	clause, phrase string
	why, do        string
}

// prs01Signals are the signals that fire right now, most severe first: threads, memory, runnable
// tasks. Offline schedulers are not pressure: they hold whatever the load, so the Setup card's INS17
// raises them with their cause; PRS01's evidence still counts them.
func (f PerformanceFacts) prs01Signals() []prs01Signal {
	out := []prs01Signal{}
	if s := f.Schedulers; s != nil {
		if s.MaxWorkers > 0 {
			pct := float64(s.CurrentWorkers) * 100 / float64(s.MaxWorkers)
			if pct >= 80 {
				out = append(out, prs01Signal{critical: pct >= 90,
					clause: fmt.Sprintf("%s of %s worker threads are in use (%d%%)", n0int(int64(s.CurrentWorkers)), n0int(int64(s.MaxWorkers)), int(pct)),
					phrase: fmt.Sprintf("threads %d%%", int(pct)), why: prs01ThreadsWhy, do: prs01ThreadsDo})
			}
		}
	}
	if g := f.Grants; g != nil && (g.Waiting > 0 || g.Forced > 0) {
		parts, phrase := []string{}, ""
		if g.Waiting > 0 {
			verb := "are"
			if g.Waiting == 1 {
				verb = "is"
			}
			parts = append(parts, fmt.Sprintf("%s %s waiting for memory right now", plural(g.Waiting, "query", "queries"), verb))
			phrase = fmt.Sprintf("%s waiting for memory", n0int(g.Waiting))
		}
		if g.Forced > 0 {
			parts = append(parts, fmt.Sprintf("%s forced since restart", plural(g.Forced, "memory grant was", "memory grants were")))
			if phrase == "" {
				phrase = "forced memory grants"
			}
		}
		out = append(out, prs01Signal{clause: strings.Join(parts, "; "), phrase: phrase, why: prs01MemoryWhy, do: prs01MemoryDo})
	}
	if s := f.Schedulers; s != nil && s.OnlineSchedulers > 0 {
		avg := float64(s.RunnableTasks) / float64(s.OnlineSchedulers)
		if avg >= 2 {
			out = append(out, prs01Signal{
				clause: fmt.Sprintf("%s tasks are waiting for a CPU on %d schedulers (%s each on average)", n0int(int64(s.RunnableTasks)), s.OnlineSchedulers, n1(avg)),
				phrase: fmt.Sprintf("CPU queue %s per scheduler", n1(avg)), why: prs01CPUWhy, do: prs01CPUDo})
		}
	}
	return out
}

// prs01OfflineRef points the evidence at INS17 when schedulers are offline and nobody chose them.
func prs01OfflineRef(s *SchedulerLoad) string {
	if s.OfflineSchedulers > 0 && !s.AffinityManual {
		return " (see INS17)"
	}
	return ""
}

// prs01: one finding when any pressure signal fires; Critical for threads at 90%, else High.
func (b *perfBuild) prs01() {
	f := b.f
	notes := []string{}
	if f.SchedulersError != "" {
		notes = append(notes, schedNotRead+f.SchedulersError)
	}
	if f.GrantsError != "" {
		notes = append(notes, grantsNotRead+f.GrantsError)
	}
	if len(notes) == 2 {
		b.skip(strings.Join(notes, " "), "PRS01")
		return
	}
	if len(notes) == 1 {
		b.notes["PRS01"] = notes[0]
	}
	signals := f.prs01Signals()
	if len(signals) == 0 {
		return
	}
	sev := SeverityHigh
	clauses, whys, dos := []string{}, []string{}, []string{}
	for _, s := range signals {
		if s.critical {
			sev = SeverityCritical
		}
		clauses, whys, dos = append(clauses, s.clause), append(whys, s.why), append(dos, s.do)
	}
	ev := []string{}
	if s := f.Schedulers; s != nil {
		ev = append(ev, fmt.Sprintf("Worker threads: %s in use of %s; %s tasks waiting for a thread.", n0int(int64(s.CurrentWorkers)), n0int(int64(s.MaxWorkers)), n0int(s.WorkQueue)),
			fmt.Sprintf("Runnable tasks: %d on %d online schedulers (%d busy, at most %d on one); %d offline%s.", s.RunnableTasks, s.OnlineSchedulers, s.BusySchedulers, s.MaxRunnable, s.OfflineSchedulers, prs01OfflineRef(s)))
	}
	if g := f.Grants; g != nil {
		ev = append(ev, fmt.Sprintf("Memory grants: %s waiting, %s granted, %s forced since restart.", n0int(g.Waiting), n0int(g.Granted), n0int(g.Forced)))
		if g.Waiting > 0 && len(g.Holders) > 0 {
			hs := []string{}
			for i, h := range g.Holders {
				if i == 3 {
					break
				}
				hs = append(hs, fmt.Sprintf("session %d holds %s MB (asked for %s MB, waited %s s): %s", h.Session, n0(float64(h.GrantedKB)/1024),
					n0(float64(h.RequestedKB)/1024), n1(float64(h.WaitMs)/1000), orQ(left(oneLine(h.Statement), 200))))
			}
			ev = append(ev, "Largest grants now: "+strings.Join(hs, "; ")+".")
		}
	}
	b.add("PRS01", sev, WhenNone, EffortModerate, ClockNow, f.ServerName,
		upperFirst(strings.Join(clauses, "; "))+".", strings.Join(whys, " "), strings.Join(dos, " "), strings.Join(ev, " "))
}

// prs02: memory grant timeouts since the restart; more than 100 High, more than 10 Medium.
func (b *perfBuild) prs02() {
	f := b.f
	if f.GrantsError != "" {
		b.skip(grantsNotRead+f.GrantsError, "PRS02")
		return
	}
	g := f.Grants
	if g == nil || g.Timeouts <= 10 {
		return
	}
	sev := SeverityMedium
	if g.Timeouts > 100 {
		sev = SeverityHigh
	}
	b.add("PRS02", sev, WhenDecideFirst, EffortModerate, ClockSinceRestart, f.ServerName,
		fmt.Sprintf("%s queries gave up waiting for memory since the restart (error 8645).", n0int(g.Timeouts)),
		prs02Why, prs02Do,
		fmt.Sprintf("Memory grant timeouts since %s: %s. Right now: %s waiting for memory, %s granted; %s forced grants since restart.",
			sqlTime(f.StartedAt), n0int(g.Timeouts), n0int(g.Waiting), n0int(g.Granted), n0int(g.Forced)))
}

func oneLine(s string) string {
	return strings.TrimLeft(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s), " ")
}

// act01: one finding per head blocker whose longest blocked request has waited 5 s or more;
// High from 30 s.
func (b *perfBuild) act01() {
	f := b.f
	if f.BlockingError != "" {
		b.skip(blockingNotRead+f.BlockingError, "ACT01")
		return
	}
	for _, x := range f.Blockers {
		if x.LongestSec < 5 {
			continue
		}
		sev := SeverityMedium
		if x.LongestSec >= 30 {
			sev = SeverityHigh
		}
		object := fmt.Sprintf("Session %d", x.Session)
		if x.Database != "" {
			object += " (" + x.Database + ")"
		}
		last := "?"
		if x.LastStatement != "" {
			last = oneLine(x.LastStatement)
		}
		self := ""
		if x.SelfBlocked {
			self = " (waiting on its own parallel threads)"
		}
		do := act01Do
		if x.Database == "" || strings.EqualFold(x.Database, f.Database) {
			do += b.rcsi(act01RCSIOff)
		}
		b.add("ACT01", sev, WhenNone, EffortNone, ClockNow, object,
			fmt.Sprintf("Session %d (%s on %s) is blocking %d session(s)%s; the longest has waited %d s.", x.Session, orQ(x.Login), orQ(x.Host), x.Blocked, self, x.LongestSec),
			act01Why, do,
			left(fmt.Sprintf("Program: %s. Status: %s. Open transactions: %d. Last statement: %s", orQ(x.Program), x.Status, x.OpenTran, last), 4000))
	}
}

// rcsi is the blocking Do this's second half (docs/phase3f-contract.md, item 20): READ COMMITTED
// SNAPSHOT as an option when the database does not have it, else what the blocking must be.
func (b *perfBuild) rcsi(off string) string {
	if b.f.RCSI {
		return rcsiOn
	}
	return off
}

// blocking is what ACT01 found: blocked sessions and the longest wait, over the head blockers it reports.
func (b *perfBuild) blocking() (blocked int, longest int64) {
	for _, x := range b.f.Blockers {
		if x.LongestSec >= 5 {
			blocked += x.Blocked
			longest = max(longest, x.LongestSec)
		}
	}
	return blocked, longest
}

// act02: one finding per transaction open more than 5 minutes in this database (PerformanceTriage.sql's
// ACT02), then one for the user sessions sleeping with a transaction open more than 5 minutes
// elsewhere on the instance (the scan's WRK07); High from 30 minutes.
// Act02HighMinutes is ACT02's High: a transaction open 30 minutes or more. The row versioning decision
// holds the rcsi fix back at the same age (docs/phase5m-contract.md, section 4).
const Act02HighMinutes = 30

// Act02Minutes is ACT02's own threshold: a transaction open 5 minutes or more is named (the open
// transactions fragment of the read uses the same 5 minutes).
const Act02Minutes = 5

func (b *perfBuild) act02() {
	f := b.f
	if f.TransactionsError != "" {
		b.skip(transNotRead+f.TransactionsError, "ACT02")
		return
	}
	seen := map[int]bool{}
	for _, x := range f.OpenTransactions {
		seen[x.Session] = true
		sev := SeverityMedium
		if x.Minutes >= Act02HighMinutes {
			sev = SeverityHigh
		}
		tail := "."
		if x.Status == "sleeping" {
			tail = " and is doing nothing."
		}
		last := "?"
		if x.LastStatement != "" {
			last = oneLine(x.LastStatement)
		}
		b.add("ACT02", sev, WhenNone, EffortNone, ClockNow, fmt.Sprintf("Session %d", x.Session),
			fmt.Sprintf("Session %d (%s on %s) has had a transaction open for %d minutes%s", x.Session, orQ(x.Login), orQ(x.Host), x.Minutes, tail),
			act02Why, act02Do,
			left(fmt.Sprintf("Program: %s. Started: %s. Log used: %s MB. Last statement: %s", orQ(x.Program), x.Started.Format("2006-01-02 15:04:05"),
				n1(float64(x.LogBytes)/1048576), last), 4000))
	}

	idle := []IdleTransaction{}
	implicit, oldest := 0, 0
	for _, x := range f.IdleTransactions {
		if seen[x.Session] {
			continue
		}
		idle = append(idle, x)
		if x.Implicit {
			implicit++
		}
		oldest = max(oldest, x.Minutes)
	}
	if len(idle) == 0 {
		return
	}
	sev := SeverityMedium
	if oldest >= 30 {
		sev = SeverityHigh
	}
	finding := fmt.Sprintf("%d session(s) are idle with a transaction open", len(idle))
	if implicit > 0 {
		finding += fmt.Sprintf(", %d of them implicit transactions opened by the driver", implicit)
	}
	finding += fmt.Sprintf("; the oldest for %d minutes.", oldest)
	parts := []string{}
	for i, x := range idle {
		if i == 5 {
			break
		}
		p := fmt.Sprintf("session %d %s on %s (%s), %d min", x.Session, orQ(x.Login), orQ(x.Host), orQ(x.Program), x.Minutes)
		if x.Implicit {
			p += ", implicit"
		}
		parts = append(parts, p)
	}
	b.add("ACT02", sev, WhenDecideFirst, EffortModerate, ClockNow, f.ServerName, finding, wrk07Why, wrk07Do,
		left(strings.Join(parts, "; "), 1000))
}

// topWaits are the waits WRK01 ranks, benign ones left out, most waited first, with the total.
func (f PerformanceFacts) topWaits() ([]WaitStat, int64, int64) {
	out := []WaitStat{}
	var total, signal int64
	for _, w := range f.Waits {
		if w.WaitMs <= 0 || IsBenignWait(w.Type) {
			continue
		}
		out = append(out, w)
		total += w.WaitMs
		signal += w.SignalMs
	}
	sortWaits(out)
	return out, total, signal
}

func sortWaits(ws []WaitStat) {
	for i := 1; i < len(ws); i++ {
		for j := i; j > 0 && (ws[j].WaitMs > ws[j-1].WaitMs || (ws[j].WaitMs == ws[j-1].WaitMs && ws[j].Type < ws[j-1].Type)); j-- {
			ws[j], ws[j-1] = ws[j-1], ws[j]
		}
	}
}

// pct1 is CONVERT(decimal(5, 1), x) written as SQL Server writes it.
func pct1(x float64) string { return fmt.Sprintf("%.1f", roundHalfAway(x, 1)) }

// waitFloor is the least wait time, in ms, that can make a WRK01 finding: 10% of the uptime, the
// real hours under a day too, at least one hour (docs/phase3f-contract.md, item 6). Shares of all
// waiting always add up to 100%, so on an idle server a small wait would otherwise score high.
func (f PerformanceFacts) waitFloor() int64 {
	return int64(max(f.UptimeHours, 1)) * 3600000 / 10
}

// wrk01: the top 5 waits since the restart with the scan's group, why and do this; THREAD from 1%
// and MEM from 10% High, the named groups from 25% Medium, else Info. A wait under the floor (10%
// of uptime) stays in the list as Info, "small against uptime".
func (b *perfBuild) wrk01() {
	f := b.f
	waits, total, _ := f.topWaits()
	if total <= 0 {
		return
	}
	floor := f.waitFloor()
	// Phase 5k: the waits small against uptime are one Info line, each with its time.
	var small, smallEv []string
	for i, w := range waits {
		if i == 5 {
			break
		}
		g := WaitGroup(w.Type)
		pct := float64(w.WaitMs) * 100 / float64(total)
		evidence := fmt.Sprintf("Waited %s over %s waits since %s.", waitTime(w.WaitMs), n0int(w.Tasks), sqlTime(f.StartedAt))
		if w.WaitMs < floor {
			small = append(small, fmt.Sprintf("%s %s%%", w.Type, pct1(pct)))
			smallEv = append(smallEv, fmt.Sprintf("%s: %s over %s waits.", w.Type, waitTime(w.WaitMs), n0int(w.Tasks)))
			continue
		}
		sev := SeverityInfo
		switch {
		case g == waitTHREAD && pct >= 1, g == waitMEM && pct >= 10:
			sev = SeverityHigh
		case pct >= 25 && strings.Contains(" PAR IO LOG LOCK CPU MEM PLATCH AG LATCH ", " "+g+" "):
			sev = SeverityMedium
		}
		when, effort := WhenNone, EffortNone
		if pct >= 25 || g == waitTHREAD || g == waitMEM {
			when, effort = WhenDecideFirst, EffortModerate
		}
		b.add("WRK01", sev, when, effort, ClockSinceRestart, f.ServerName,
			fmt.Sprintf("Wait #%d: %s (%s%% of all waiting).", i+1, w.Type, pct1(pct)),
			wrk01Why[g], wrk01Do[g], evidence)
	}
	if len(small) > 0 {
		b.add("WRK01", SeverityInfo, WhenNone, EffortNone, ClockSinceRestart, f.ServerName,
			"Waits since restart are small against uptime: "+strings.Join(small, ", ")+".",
			wrk01SmallWhy, wrk01SmallDo, strings.Join(smallEv, " ")+" Since "+sqlTime(f.StartedAt)+".")
	}
}

// The folded WRK01 line (phase 5k).
const (
	wrk01SmallWhy = "Shares of all waiting always add up to 100%, so on a server that waited little since its restart they say nothing about slowness."
	wrk01SmallDo  = "Nothing: look again after a busy day."
)

// waitTime writes a wait time: "40 s", "12 min", "3.5 hours".
func waitTime(ms int64) string {
	switch {
	case ms < 60000:
		return n0(float64(ms)/1000) + " s"
	case ms < 3600000:
		return n0(float64(ms)/60000) + " min"
	}
	return n1(float64(ms)/3600000) + " hours"
}

// signalPct is the share of signal waits in all waits, as WRK02 converts it to int.
func (f PerformanceFacts) signalPct() (int, bool) {
	_, total, signal := f.topWaits()
	if total <= 0 {
		return 0, false
	}
	return int(float64(signal) * 100 / float64(total)), true
}

// wrk02: signal waits 25% or more of all waits.
func (b *perfBuild) wrk02() {
	f := b.f
	_, total, signal := f.topWaits()
	pct, ok := f.signalPct()
	if !ok || float64(signal)*100/float64(total) < 25 {
		return
	}
	b.add("WRK02", SeverityMedium, WhenDecideFirst, EffortModerate, ClockSinceRestart, f.ServerName,
		fmt.Sprintf("Queries spend %d%% of their waiting time queuing for a CPU.", pct), wrk02Why, wrk02Do,
		fmt.Sprintf("Signal wait %d s of %d s total wait.", signal/1000, total/1000))
}

// poisonWaits are the scan's WRK05 list.
var poisonWaits = []string{"THREADPOOL", "RESOURCE_SEMAPHORE", "RESOURCE_SEMAPHORE_QUERY_COMPILE", "IO_QUEUE_LIMIT", "IO_RETRY",
	"LOG_RATE_GOVERNOR", "POOL_LOG_RATE_GOVERNOR", "RESMGR_THROTTLED",
	"SE_REPL_CATCHUP_THROTTLE", "SE_REPL_COMMIT_ACK", "SE_REPL_SLOW_SECONDARY_THROTTLE"}

// wrk05: any poison wait with 5 s or more since the restart; THREADPOOL and RESOURCE_SEMAPHORE at
// 60 s a day or more High, else Medium.
func (b *perfBuild) wrk05() {
	f := b.f
	days := f.uptimeDays()
	for _, p := range poisonWaits {
		for _, w := range f.Waits {
			if !strings.EqualFold(w.Type, p) || w.WaitMs < 5000 {
				continue
			}
			sev := SeverityMedium
			if (p == "THREADPOOL" || p == "RESOURCE_SEMAPHORE") && float64(w.WaitMs)/days >= 60000 {
				sev = SeverityHigh
			}
			why, do := wrk05OtherWhy, wrk05OtherDo
			switch p {
			case "THREADPOOL":
				why = "SQL Server ran out of worker threads: new requests queued until a thread was free. Usually heavy blocking or too much parallelism. Users see the server freeze."
				do = "Find the sessions that block the others (the lock waits in WRK01, and ACT01) and have them finish sooner, and cap parallelism (INS04, INS05). Do not raise max worker threads; it hides the cause."
			case "RESOURCE_SEMAPHORE":
				why = "Queries waited for memory before they could start, because other queries held it all. Usually a few huge sorts or hashes from bad estimates."
				do = "Find the queries that ask for large memory grants (their plans carry a memory grant warning) and fix their estimates: statistics (MNT04), missing indexes, or the query itself."
			case "RESOURCE_SEMAPHORE_QUERY_COMPILE":
				why = "Too many queries compiled at once and ran out of compile memory: a plan cache being cleared, or code that compiles for every value (INS06)."
				do = "Stop whatever clears the plan cache (MNT05) and switch on optimize for ad hoc workloads (INS06)."
			case "IO_QUEUE_LIMIT":
				why = "The storage could not keep up and SQL Server throttled its own I/O."
			}
			b.add("WRK05", sev, WhenDecideFirst, EffortModerate, ClockSinceRestart, f.ServerName,
				fmt.Sprintf("Poison wait %s: %s seconds a day (%s waits a day).", w.Type, n0(float64(w.WaitMs)/1000/days), n0(float64(w.Tasks)/days)),
				why, do,
				fmt.Sprintf("Since %s: %s s over %s waits, longest single wait %s ms.", sqlTime(f.StartedAt), n0(float64(w.WaitMs)/1000), n0int(w.Tasks), n0int(w.MaxWaitMs)))
		}
	}
}

// wrk03: the top 5 missing index suggestions for this database by the scan's score, impact 50%
// or more and score 100,000 or more; Medium from a score of 1,000,000. Each carries the
// create-index fix (docs/phase4c-contract.md, part 3), behind PerformanceTriage.sql's index guards and a
// preview; AvailablePerformanceFixes lists its options.
func (b *perfBuild) wrk03() {
	f := b.f
	if f.MissingIndexError != "" {
		b.skip(missingNotRead+f.MissingIndexError, "WRK03")
		return
	}
	n := 0
	for _, x := range f.MissingIndexes {
		if x.Impact < 50 || x.Score < 100000 {
			continue
		}
		if n == 5 {
			break
		}
		n++
		sev := SeverityLow
		if x.Score >= 1000000 {
			sev = SeverityMedium
		}
		when, do := WhenWindow, wrk03Do+wrk03DoOffline
		if f.EngineEdition == 3 {
			when, do = WhenOnline, wrk03Do+wrk03DoOnline
		}
		last := "-"
		if x.LastSeek != nil {
			last = sqlTime(*x.LastSeek)
		}
		b.add("WRK03", sev, when, EffortModerate, ClockSinceRestart, x.Statement,
			fmt.Sprintf("SQL Server suggests an index on %s (estimated %d%% faster for %d queries).", x.Statement, int(x.Impact), x.Uses),
			wrk03Why, do,
			left("Equality: "+orDash(x.Equality)+"; inequality: "+orDash(x.Inequality)+"; include: "+orDash(x.Included)+"; last wanted: "+last+".", 4000))
		b.findings[len(b.findings)-1].FixID = FixCreateIndex
	}
}

// AvailablePerformanceFixes are the fixes of the Performance card's findings (docs/phase4c-contract.md,
// part 3): one create-index fix per WRK03 finding, with the suggestion's table and columns in its
// options and the finding's object, so the card page sends them with the first preview. The
// suggestion is matched to its finding by the table and the evidence's columns. Then one archive
// fix per QRY01 finding that carries it (archive before an index), as ArchiveFixesFor builds IDX10's.
func AvailablePerformanceFixes(f PerformanceFacts, findings []Finding) []Fix {
	out := performanceIndexFixes(f, findings)
	return append(out, qry01ArchiveFixes(f, findings)...)
}

// qry01ArchiveFixes are the archive fixes of the QRY01 findings that carry one: the table the query
// reads most of, its options as ArchiveFixesFor gives them, plus the finding's object and query hash
// (several QRY01 findings can share one object; the hash is in each one's evidence). A finding is
// matched to its query by the evidence: its query hash, else its text.
func qry01ArchiveFixes(f PerformanceFacts, findings []Finding) []Fix {
	out := []Fix{}
	def, _ := FindFixDef(FixArchiveTable)
	for _, x := range findings {
		if x.CheckID != "QRY01" || x.FixID != FixArchiveTable {
			continue
		}
		for i, q := range f.TopQueries {
			if i == 5 {
				break
			}
			if q.QueryHash != "" && !strings.Contains(x.Evidence, " Query hash: "+q.QueryHash+".") ||
				q.QueryHash == "" && !strings.HasSuffix(x.Evidence, " Query: "+left(oneLine(q.Text), 1500)) || q.Execs == 0 || q.PlanTooBig {
				continue
			}
			t, _, ok := archiveFirstTable(f.Database, f.GrowingCandidates, float64(q.Reads)/float64(q.Execs), q.Plan)
			if !ok || t.DateCol == "" {
				continue
			}
			opts := map[string]string{OptionTable: archiveTableOption(t.Schema, t.Name), OptionObject: x.Object}
			if q.QueryHash != "" {
				opts[OptionQueryHash] = q.QueryHash
			}
			if t.DateIndexed {
				opts[OptionArchiveDateColumn] = t.DateCol
			}
			out = append(out, Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: x.CheckID, Setting: def.Setting,
				Current: "no archive job", Target: "old rows archived nightly", Options: opts})
			break
		}
	}
	return out
}

// performanceIndexFixes are the create-index fixes of the WRK03 findings.
func performanceIndexFixes(f PerformanceFacts, findings []Finding) []Fix {
	out := []Fix{}
	def, _ := FindFixDef(FixCreateIndex)
	used := map[int]bool{}
	for _, x := range findings {
		if x.CheckID != "WRK03" || x.FixID != FixCreateIndex {
			continue
		}
		for i, m := range f.MissingIndexes {
			if used[i] || m.Statement != x.Object ||
				!strings.HasPrefix(x.Evidence, "Equality: "+orDash(m.Equality)+"; inequality: "+orDash(m.Inequality)+"; include: "+orDash(m.Included)+";") {
				continue
			}
			used[i] = true
			_, schema, table, err := SplitTableName(m.Statement)
			if err != nil {
				break
			}
			out = append(out, Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting,
				Current: "none", Target: "the suggested index",
				Options: map[string]string{
					OptionTable:      QuoteName(schema) + "." + QuoteName(table),
					OptionEquality:   m.Equality,
					OptionInequality: m.Inequality,
					OptionIncluded:   m.Included,
					OptionObject:     x.Object,
				}})
			break
		}
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// wrk06: deadlocks since the restart from the performance counter; 10 a day High, 1 a day Medium,
// else Info.
func (b *perfBuild) wrk06() {
	f := b.f
	if f.Deadlocks <= 0 {
		return
	}
	perDay := float64(f.Deadlocks) / f.uptimeDays()
	sev, when, effort := SeverityInfo, WhenNone, EffortNone
	switch {
	case perDay >= 10:
		sev, when, effort = SeverityHigh, WhenDecideFirst, EffortModerate
	case perDay >= 1:
		sev, when, effort = SeverityMedium, WhenDecideFirst, EffortModerate
	}
	b.add("WRK06", sev, when, effort, ClockSinceRestart, f.ServerName,
		fmt.Sprintf("%s deadlock(s) since the last restart (%s a day).", n0int(f.Deadlocks), n1(perDay)),
		wrk06Why, wrk06Do, "Counter since "+sqlTime(f.StartedAt)+".")
}

// wrk08: the CPU over the last four hours from the scheduler monitor ring buffer; Medium when
// other programs averaged 25% or more (Client IT), Medium when SQL Server averaged 80% or more (Us).
// Both can fire. Skipped when the ring buffer cannot be read or holds fewer than 5 samples.
func (b *perfBuild) wrk08() {
	f := b.f
	if f.CPUHistoryError != "" {
		b.skip(cpuNotRead+f.CPUHistoryError, "WRK08")
		return
	}
	n := len(f.CPUHistory)
	if n < 5 {
		b.skip(fmt.Sprintf("The CPU history holds %d sample(s): too soon after restart.", n), "WRK08")
		return
	}
	var sqlSum, otherSum int
	peakSQL, peakOther := f.CPUHistory[0], f.CPUHistory[0]
	first, last := f.CPUHistory[0].At, f.CPUHistory[0].At
	for _, s := range f.CPUHistory {
		sqlSum, otherSum = sqlSum+s.SQL, otherSum+s.Other
		if s.SQL > peakSQL.SQL {
			peakSQL = s
		}
		if s.Other > peakOther.Other {
			peakOther = s
		}
		if s.At.Before(first) {
			first = s.At
		}
		if s.At.After(last) {
			last = s.At
		}
	}
	avgSQL, avgOther := float64(sqlSum)/float64(n), float64(otherSum)/float64(n)
	span := cpuSpan(last.Sub(first) + time.Minute)
	host := machineName(f.ServerName)
	samples := fmt.Sprintf(" %d one-minute samples from %s to %s: SQL Server %s%%, other programs %s%% on average.", n, sqlTime(first), sqlTime(last), n0(avgSQL), n0(avgOther))
	if avgOther >= 25 {
		b.add("WRK08", SeverityMedium, WhenDecideFirst, EffortModerate, ClockSinceRestart, host,
			fmt.Sprintf("Other programs on %s used %s%% of the CPU on average over the last %s; SQL Server used %s%%.", host, n0(avgOther), span, n0(avgSQL)),
			wrk08OtherWhy, wrk08OtherDo,
			fmt.Sprintf("Busiest minute for other programs: %d%% at %s (SQL Server %d%%).", peakOther.Other, sqlTime(peakOther.At), peakOther.SQL)+samples)
	}
	if avgSQL >= 80 {
		b.add("WRK08", SeverityMedium, WhenDecideFirst, EffortModerate, ClockSinceRestart, host,
			fmt.Sprintf("SQL Server used %s%% of the CPU on average over the last %s.", n0(avgSQL), span),
			wrk08SQLWhy, wrk08SQLDo,
			fmt.Sprintf("Busiest minute for SQL Server: %d%% at %s (other programs %d%%).", peakSQL.SQL, sqlTime(peakSQL.At), peakSQL.Other)+samples)
		b.findings[len(b.findings)-1].Owner = OwnerUs
	}
}

// cpuSpan is how long the CPU history covers, in words: "4 hours", "45 minutes".
func cpuSpan(d time.Duration) string {
	minutes := int(math.Round(d.Minutes()))
	if minutes < 90 {
		return plural(int64(minutes), "minute", "minutes")
	}
	return plural(int64(math.Round(float64(minutes)/60)), "hour", "hours")
}

// machineName is the computer of an @@SERVERNAME: SQL01 of SQL01\WMS.
func machineName(server string) string {
	host, _, _ := strings.Cut(server, `\`)
	if host == "" {
		return "the server"
	}
	return host
}

// srv07: 100,000 batches or more and 10% or more of them compiled; Medium from 25%, else Low.
func (b *perfBuild) srv07() {
	f := b.f
	if f.Batches < 100000 {
		return
	}
	pct := float64(f.Compiles) * 100 / float64(f.Batches)
	if pct < 10 {
		return
	}
	sev := SeverityLow
	if pct >= 25 {
		sev = SeverityMedium
	}
	b.add("SRV07", sev, WhenDecideFirst, EffortModerate, ClockSinceRestart, f.ServerName,
		fmt.Sprintf("%s%% of all batches compile a new plan (%s compiles a second).", n0(pct), n1(float64(f.Compiles)/(float64(f.UptimeHours)*3600+1))),
		srv07Why, srv07Do,
		fmt.Sprintf("Since restart: %s batches, %s compiles, %s recompiles.", n0int(f.Batches), n0int(f.Compiles), n0int(f.Recompiles)))
}

// srv04: always, as Info; Low when another database used 25% or more of the query CPU.
func (b *perfBuild) srv04() {
	f := b.f
	if f.LoadError != "" {
		b.skip(loadNotRead+f.LoadError, "SRV04")
		return
	}
	var allCPU, allReads int64
	var mine *DatabaseLoad
	var other *DatabaseLoad
	for i, x := range f.Loads {
		if x.DatabaseID == 32767 {
			continue
		}
		allCPU += x.CPU
		allReads += x.ReadBytes
		if strings.EqualFold(x.Name, f.Database) {
			mine = &f.Loads[i]
		} else if x.DatabaseID > 4 && (other == nil || x.CPU > other.CPU) {
			other = &f.Loads[i]
		}
	}
	if allCPU <= 0 && allReads <= 0 {
		return
	}
	var myCPU *float64
	myRead := "0"
	if allCPU > 0 {
		v := 0.0
		if mine != nil {
			v = roundHalfAway(float64(mine.CPU)*100/float64(allCPU), 1)
		}
		myCPU = &v
	}
	if mine != nil && allReads > 0 {
		myRead = pct1(float64(mine.ReadBytes) * 100 / float64(allReads))
	}
	otherPct := -1.0
	if other != nil && allCPU > 0 {
		otherPct = roundHalfAway(float64(other.CPU)*100/float64(allCPU), 1)
	}

	finding := "This database "
	if myCPU != nil {
		finding += "used " + pct1(*myCPU) + "% of the query CPU and did "
	} else {
		finding += "did "
	}
	finding += myRead + "% of the disk reads on this server since the last restart."
	if myCPU == nil {
		finding += " (No query CPU figures are in the plan cache yet.)"
	}
	sev, do := SeverityInfo, srv04DoNone
	switch {
	case otherPct >= 25:
		sev, do = SeverityLow, srv04DoOther
		finding += " " + other.Name + " used " + pct1(otherPct) + "% of the CPU."
	case myCPU != nil && *myCPU >= 50:
		do = srv04DoMine
	}

	busiest := append([]DatabaseLoad{}, f.Loads...)
	for i := 1; i < len(busiest); i++ {
		for j := i; j > 0 && (busiest[j].CPU > busiest[j-1].CPU || (busiest[j].CPU == busiest[j-1].CPU && busiest[j].ReadBytes > busiest[j-1].ReadBytes)); j-- {
			busiest[j], busiest[j-1] = busiest[j-1], busiest[j]
		}
	}
	parts := []string{}
	for _, x := range busiest {
		if x.DatabaseID == 32767 {
			continue
		}
		if len(parts) == 6 {
			break
		}
		p := x.Name + " "
		if allCPU > 0 {
			p += pct1(float64(x.CPU)*100/float64(allCPU)) + "% CPU, "
		}
		parts = append(parts, p+n1(float64(x.ReadBytes)/1073741824)+" GB read")
	}
	b.add("SRV04", sev, WhenNone, EffortNone, ClockSinceRestart, f.Database, finding, srv04Why, do,
		left("CPU share from plans still in cache; disk reads from file statistics. Busiest: "+strings.Join(parts, "; "), 4000))
}

// planMissingIndexWords is PlanWarnings' words for a plan's <MissingIndexes>.
const planMissingIndexWords = "SQL Server suggests an index for it (check IDX02 and IDX09 before creating one)"

// archiveFirstMinShare is the share of a table's pages a query must read per run, on average, for
// "archive its old rows before an index": archiving then shrinks what every run reads. A query that
// seeks a few rows reads the same few pages whatever the table's size, and gains nothing.
const archiveFirstMinShare = 0.25

// archiveFirstTable is the table that only grows (of candidates) that a query's plans touch and that
// it reads the largest share of per run (avgReads pages against the table's MB x 128), when that
// share is archiveFirstMinShare or more.
func archiveFirstTable(database string, candidates []GrowingTable, avgReads float64, plans ...string) (GrowingTable, float64, bool) {
	var (
		best  GrowingTable
		share float64
		found bool
	)
	if len(candidates) == 0 || avgReads <= 0 {
		return best, 0, false
	}
	for _, o := range planTablesHere(database, plans...) {
		for _, c := range candidates {
			pages := c.MB * 128
			if pages <= 0 || !strings.EqualFold(o.Schema, c.Schema) || !strings.EqualFold(o.Table, c.Name) {
				continue
			}
			if s := avgReads / pages; s >= archiveFirstMinShare && s > share {
				best, share, found = c, s, true
			}
		}
	}
	return best, share, found
}

// planTablesHere are the tables of this database the plans touch (no temporary tables, table
// variables or system tables). A plan compiled before the database was restored or renamed names the
// database it had then (Query Store keeps the plan as compiled): when none of a plan's objects names
// this database, its objects are taken as this database's.
func planTablesHere(database string, plans ...string) []ShowplanObject {
	out := []ShowplanObject{}
	for _, p := range plans {
		objects := ParseShowplan(p).Objects
		here := false
		for _, o := range objects {
			if strings.EqualFold(o.Database, database) {
				here = true
				break
			}
		}
		for _, o := range objects {
			if !here && !strings.EqualFold(o.Database, "tempdb") {
				o.Database = ""
			}
			if qShowplanTableOf(database, o) {
				out = append(out, o)
			}
		}
	}
	return out
}

// archiveFirstDo is the Do this of a query that reads much of a table that only grows: archive its
// old rows first, an index only if it is still slow after that; a queue keeps the rows not finished.
func archiveFirstDo(t GrowingTable, share float64) string {
	age := "that keeps every row"
	if t.Oldest != nil {
		age = "whose oldest row is from " + t.Oldest.Format("Jan 2006")
	}
	kind := growingKind(t.Name)
	s := fmt.Sprintf("This query reads %s%% of %s.%s (%s MB) every run, a %s table %s and that nothing cleans up. "+
		"Archive its old rows first (Storage card, Tables row): every run then reads less. Consider an index only if it is still slow after that.",
		n0(math.Min(share*100, 100)), t.Schema, t.Name, n0(t.MB), kind, age)
	if kind == "queue" {
		s += " A queue's old rows may still be needed (failed rows retried): archive only the finished ones with the archive's condition, " +
			"e.g. the status that means done, and confirm which statuses are finished with the vendor first."
	}
	return s
}

// archiveFirstIndexWords is the <MissingIndexes> flag of a query whose table should be archived first.
func archiveFirstIndexWords(t GrowingTable) string {
	return "SQL Server suggests an index for it, but archive " + t.Schema + "." + t.Name + " first"
}

// PlanWarnings reads what the triage reads from a plan's XML (PerformanceTriage.sql section 0): the
// expression of an implicit conversion that stops a seek, and the other warnings, in its words.
// Spills and memory grant warnings exist only in actual plans, never in a cached one, so they come
// from the runtime statistics instead (TopQuery.Spills and the grants, docs/phase3f-contract.md
// item 1).
func PlanWarnings(plan string) (conversion string, flags []string) {
	const convert = `ConvertIssue="Seek Plan" Expression="`
	if i := strings.Index(plan, convert); i >= 0 {
		rest := plan[i+len(convert):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			rest = rest[:j]
		}
		conversion = left(rest, 400)
	}
	flags = []string{}
	for _, w := range []struct{ marker, words string }{
		{`NoJoinPredicate="true"`, "a join with no join condition (cross join)"},
		{`Lookup="true"`, "key lookups (the index used does not hold every column needed)"},
		{"<UserDefinedFunction", "calls a scalar function for every row"},
		{`Table="[@`, "reads a table variable (no statistics, estimated as 1 row)"},
		{`StatementOptmEarlyAbortReason="TimeOut"`, "the optimizer timed out before finding a good plan (the query is too complex)"},
		{"<MissingIndexes>", planMissingIndexWords},
	} {
		if strings.Contains(plan, w.marker) {
			flags = append(flags, w.words)
		}
	}
	return conversion, flags
}

// qry01: one finding per top query; Low when it varies with the input, has a plan warning, spills
// to TempDB, holds an oversized memory grant, gets a new plan nearly every run, waits more than it
// works or arrives as 50 or more statements (ACT03's rules and docs/phase3f-contract.md items 1
// and 19), else Info. The check's note says where it read from and how concentrated the CPU is.
func (b *perfBuild) qry01() {
	f := b.f
	if f.QueriesError != "" {
		b.skip(queriesNotRead+f.QueriesError, "QRY01")
		return
	}
	clock, note := ClockSinceRestart, qry01FromCache
	if f.QueryStoreError == "" && (strings.EqualFold(f.QueryStoreState, "READ_WRITE") || strings.EqualFold(f.QueryStoreState, "READ_ONLY")) {
		note = qry01QSEmpty
	}
	if f.QuerySource == QuerySourceQueryStore {
		clock, note = ClockWindow, qry01FromQS
	}
	b.notes["QRY01"] = note + f.qry01Workload()
	for i, q := range f.TopQueries {
		if i == 5 {
			break
		}
		perRun := func(x int64) int64 {
			if q.Execs == 0 {
				return 0
			}
			return x / q.Execs
		}
		perRunF := func(x int64) float64 {
			if q.Execs == 0 {
				return 0
			}
			return float64(x) / float64(q.Execs)
		}
		varies := q.MaxReads >= 10000 && q.MaxReads >= q.MinReads*20
		waits := q.Execs > 0 && q.Elapsed >= q.CPU*3 && q.Elapsed/q.Execs >= 1000000
		timeSwings := !varies && q.Execs > 1 && q.MaxElapsed >= 1000000 && float64(q.MaxElapsed) >= 3*perRunF(q.Elapsed)
		perDay := -1.0
		if q.Minutes >= 60 {
			perDay = float64(q.Execs) * 1440 / float64(q.Minutes)
		}
		conversion, flags := "", []string{}
		if !q.PlanTooBig {
			conversion, flags = PlanWarnings(q.Plan)
		}
		// Archive before an index (IDX10): the query reads much of a table that only grows.
		archive, archiveShare, archiveFirst := GrowingTable{}, 0.0, false
		if !q.PlanTooBig && q.Execs > 0 {
			archive, archiveShare, archiveFirst = archiveFirstTable(f.Database, f.GrowingCandidates, perRunF(q.Reads), q.Plan)
		}
		if archiveFirst {
			for i, w := range flags {
				if w == planMissingIndexWords {
					flags[i] = archiveFirstIndexWords(archive)
				}
			}
		}
		spillKB := q.spillKBPerRun()
		spills := spillKB >= 8000 // 1,000 pages
		grant := q.MaxGrantKB >= 102400 && q.MaxUsedGrantKB >= 0 && q.MaxUsedGrantKB*10 < q.MaxGrantKB
		unstable := q.Plans > 1 && q.LowRunPlans == q.Plans && !strings.Contains(strings.ToUpper(q.Text), "RECOMPILE")
		sharePct := 0.0
		if f.TotalCPU > 0 {
			sharePct = float64(q.CPU) * 100 / float64(f.TotalCPU)
		}
		rare := q.Execs > 0 && q.Execs <= 10 && sharePct >= 5

		// Phase 5j: the plan in use against the runs before it began (5c's TuningVerdict). A query
		// that got faster stays in the list as Info, not a thing to do.
		faster := ""
		if q.Since != nil && q.BeforeRuns > 0 && q.NowRuns > 0 {
			before, now := float64(q.BeforeCPU)/float64(q.BeforeRuns)/1000, float64(q.NowCPU)/float64(q.NowRuns)/1000
			if TuningVerdict(q.BeforeRuns, q.NowRuns, before, now) == VerdictFaster {
				at := q.Since.Add(time.Duration(f.UTCOffsetMinutes) * time.Minute).Format("2 Jan 15:04")
				faster = fmt.Sprintf("Faster since %s: %s of CPU a run, was %s. Nothing to do. ", at, QueryDuration(now), QueryDuration(before))
			}
		}

		share := pct1(sharePct)
		s := "Uses " + share + "% of all query CPU on the server: "
		if f.QuerySource == QuerySourceQueryStore {
			s = "Uses " + share + "% of the query CPU in this database over the last 7 days: "
		}
		s += fmt.Sprintf("%s runs, %s ms CPU and %s page reads per run.", n0int(q.Execs), n0(float64(perRun(q.CPU))/1000), n0int(perRun(q.Reads)))
		switch {
		case perDay >= 1000:
			s += fmt.Sprintf(" About %s runs a day (one every %s s): small per run, large in total.", n0(perDay), n0(86400/perDay))
		case perDay >= 0:
			s += fmt.Sprintf(" About %s runs a day.", n0(perDay))
		}
		if rare {
			s += fmt.Sprintf(" Rare but expensive: %s runs took %s%% of the CPU.", n0int(q.Execs), share)
		}
		if q.Variants >= 50 {
			s += fmt.Sprintf(" It arrives as %s separate statements with the values typed into the SQL text.", n0int(int64(q.Variants)))
		}
		if varies {
			s += fmt.Sprintf(" Its cost depends heavily on the input: from %s to %s page reads per run.", n0int(q.MinReads), n0int(q.MaxReads))
			if float64(q.MaxCPU) < 3*perRunF(q.CPU) {
				s += " Same rows, different work: a sniffed parameter (the plan built for the first value it saw is reused for values it does not suit)."
			} else {
				s += " The work follows the rows: some inputs simply ask for more."
			}
		}
		if timeSwings {
			s += fmt.Sprintf(" The time varies while the work does not: it waits (blocking or disk) on some runs (up to %s ms against %s ms on average).",
				n0(float64(q.MaxElapsed)/1000), n0(perRunF(q.Elapsed)/1000))
		}
		if conversion != "" {
			s += " Its plan has an implicit conversion that can stop SQL Server using an index."
		}
		if len(flags) > 0 {
			s += " Its plan shows: " + strings.Join(flags, "; ") + "."
		}
		if spills {
			s += fmt.Sprintf(" It spills to TempDB (about %s MB per run).", kbAsMB(spillKB))
		}
		if grant {
			s += fmt.Sprintf(" It asks for %s MB of memory and uses %s MB.", kbAsMB(float64(q.MaxGrantKB)), kbAsMB(float64(q.MaxUsedGrantKB)))
		}
		if unstable {
			s += fmt.Sprintf(" It gets a new plan almost every time it runs (%d plans, none used five times).", q.Plans)
		}
		if waits {
			s += fmt.Sprintf(" It spends most of its time waiting, not working: %s ms elapsed against %s ms CPU per run.",
				n0(float64(perRun(q.Elapsed))/1000), n0(float64(perRun(q.CPU))/1000))
		}

		do := qry01Do
		if archiveFirst {
			do = archiveFirstDo(archive, archiveShare) + " " + qry01Do
		}
		if varies {
			do += " Test with the values users actually enter, including one that matches nothing: some inputs cost far more than others."
		}
		if perDay >= 1000 {
			do += " Also ask whether it needs to run this often (a job or integration polling on a timer?)."
		}
		if q.Variants >= 50 {
			do += " The code should pass values as parameters so SQL Server reuses one plan (see SRV06)."
		}
		if conversion != "" {
			do += " Implicit conversion: usually a variable, parameter or join column whose type differs from the column it is compared with."
		}
		if spills {
			do += qry01SpillDo
		}
		if grant {
			do += qry01GrantDo
		}
		if unstable {
			do += qry01PlansDo
		}
		if waits {
			do += " Waiting rather than working means blocking (ACT01, IDX11) or reading from disk: fix that before tuning the query itself."
			do += b.rcsi(qry01RCSIOff)
		}
		if q.ObjectName != "" {
			do += " To see every statement in " + q.ObjectName + ", open the query: its detail lists them all, most reads first."
		}
		do += qry01Arithabort

		ev := fmt.Sprintf("Page reads per run: min %s, avg %s, max %s. Max CPU: %s ms.", n0int(q.MinReads), n0int(perRun(q.Reads)), n0int(q.MaxReads), n0(float64(q.MaxCPU)/1000))
		if q.MaxElapsed > 0 {
			ev += fmt.Sprintf(" Max elapsed: %s ms.", n0(float64(q.MaxElapsed)/1000))
		}
		switch {
		case q.Spills > 0:
			ev += fmt.Sprintf(" Spills: %s pages in total, %s MB per run.", n0int(q.Spills), kbAsMB(spillKB))
		case q.Spills < 0 && q.TempdbKB > 0:
			ev += fmt.Sprintf(" TempDB used: %s MB per run (Query Store counts spills and temporary tables together).", kbAsMB(spillKB))
		}
		switch {
		case q.MaxGrantKB > 0 && q.MaxUsedGrantKB >= 0:
			ev += fmt.Sprintf(" Largest memory grant: %s MB, of which %s MB used.", kbAsMB(float64(q.MaxGrantKB)), kbAsMB(float64(q.MaxUsedGrantKB)))
		case q.MaxGrantKB < 0 && q.MaxUsedGrantKB > 0:
			ev += fmt.Sprintf(" Largest memory used: %s MB.", kbAsMB(float64(q.MaxUsedGrantKB)))
		}
		if q.Plans > 1 {
			ev += fmt.Sprintf(" Plans: %d, %d of them run under five times.", q.Plans, q.LowRunPlans)
		}
		if q.TopWait != "" {
			ev += " Top wait: " + q.TopWait + "."
		}
		if conversion != "" {
			ev += " Conversion: " + strings.NewReplacer("&gt;", ">", "&lt;", "<").Replace(conversion) + "."
		}
		if q.PlanTooBig {
			ev += " Plan too large to inspect."
		}
		if q.QueryHash != "" {
			ev += " Query hash: " + q.QueryHash + "."
		}
		ev += " Query: " + left(oneLine(q.Text), 1500)

		sev := SeverityInfo
		if varies || conversion != "" || len(flags) > 0 || waits || q.Variants >= 50 || spills || grant || unstable || archiveFirst {
			sev = SeverityLow
		}
		if faster != "" {
			s, sev = faster+s, SeverityInfo
			do = "Nothing: it got faster. Open the query to see what changed."
		}
		object := q.ObjectName
		if object == "" {
			object = f.Database
		}
		b.add("QRY01", sev, WhenDecideFirst, EffortModerate, clock, object, s, qry01Why, do, ev)
		// The archive fix, as IDX10's finding carries it: only for a table with a date column.
		if archiveFirst && archive.DateCol != "" && faster == "" {
			b.findings[len(b.findings)-1].FixID = FixArchiveTable
		}
	}
}

// spillKBPerRun is what a query writes to TempDB per run, in KB: total_spills (pages) from the
// plan cache, or Query Store's TempDB use when the source has no spill count; -1 when neither is
// known.
func (q TopQuery) spillKBPerRun() float64 {
	if q.Execs <= 0 {
		return -1
	}
	switch {
	case q.Spills >= 0:
		return float64(q.Spills) * 8 / float64(q.Execs)
	case q.TempdbKB >= 0:
		return float64(q.TempdbKB) / float64(q.Execs)
	}
	return -1
}

// kbAsMB writes kilobytes as megabytes: whole numbers from 10 MB, one decimal below.
func kbAsMB(kb float64) string {
	if m := kb / 1024; m < 10 {
		return n1(m)
	}
	return n0(kb / 1024)
}

// qry01Workload is the sentence QRY01's note gains (docs/phase3f-contract.md, item 19): how much
// of the source's CPU the top queries take, and what that means for tuning.
func (f PerformanceFacts) qry01Workload() string {
	if f.TotalCPU <= 0 || len(f.TopQueries) == 0 {
		return ""
	}
	var cpu int64
	n := 0
	for i, q := range f.TopQueries {
		if i == 5 {
			break
		}
		cpu += q.CPU
		n++
	}
	p := float64(cpu) * 100 / float64(f.TotalCPU)
	s := fmt.Sprintf(" The top %d queries take %s%% of the CPU: ", n, pct1(p))
	switch n {
	case 1:
		s = fmt.Sprintf(" The top query takes %s%% of the CPU: ", pct1(p))
	case 5:
		s = fmt.Sprintf(" The top five queries take %s%% of the CPU: ", pct1(p))
	}
	switch {
	case p >= 50:
		return s + "concentrated, so tuning them pays."
	case p >= 25:
		return s + "moderate."
	}
	return s + "flat, no single query dominates; look at the waits and the server first."
}

// greenVerdict: "Nothing stands out: the top wait is {wait} at {pct}%, no blocking, no memory or
// thread pressure."
func (b *perfBuild) greenVerdict() string {
	waits, total, _ := b.f.topWaits()
	if len(waits) == 0 || total <= 0 {
		return "Nothing stands out: no blocking, no memory or thread pressure."
	}
	return fmt.Sprintf("Nothing stands out: the top wait is %s at %s%%, no blocking, no memory or thread pressure.",
		waits[0].Type, n0(float64(waits[0].WaitMs)*100/float64(total)))
}

// facts are the four lines on the card: Biggest wait, CPU pressure, Blocking now, Pressure now.
func (b *perfBuild) facts() []Fact {
	f := b.f
	wait := Fact{Label: "Biggest wait", Value: "could not be read", Tone: ToneNormal}
	cpu := Fact{Label: "CPU pressure", Value: "could not be read", Tone: ToneNormal}
	if f.WaitsError == "" {
		waits, total, _ := f.topWaits()
		if len(waits) > 0 && total > 0 {
			w := waits[0]
			wait.Value = fmt.Sprintf("%s %s%% (%s)", w.Type, n0(float64(w.WaitMs)*100/float64(total)), waitWords[WaitGroup(w.Type)])
			for _, x := range b.all("WRK01") {
				if x.Severity.Rank() <= SeverityMedium.Rank() {
					wait.Tone = ToneBad
				}
			}
		} else {
			wait.Value = "none"
		}
		if pct, ok := f.signalPct(); ok {
			cpu.Value = fmt.Sprintf("%d%% signal waits", pct)
		} else {
			cpu.Value = "0% signal waits"
		}
		cpu.Tone = tone(len(b.all("WRK02")) > 0)
	}

	blocking := Fact{Label: "Blocking now", Value: "could not be read", Tone: ToneNormal}
	if f.BlockingError == "" {
		blocked, longest := b.blocking()
		if blocked == 0 {
			blocking.Value = "none"
		} else {
			blocking.Value = fmt.Sprintf("%s blocked, longest %d s", plural(int64(blocked), "session", "sessions"), longest)
			blocking.Tone = worstTone(b.all("ACT01"))
		}
	}

	pressure := Fact{Label: "Pressure now", Value: "could not be read", Tone: ToneNormal}
	if f.SchedulersError == "" || f.GrantsError == "" {
		pressure.Value = "none"
		if signals := f.prs01Signals(); len(signals) > 0 {
			pressure.Value = signals[0].phrase
			pressure.Tone = ToneBad
			for _, x := range b.all("PRS01") {
				if x.Severity == SeverityCritical {
					pressure.Tone = ToneWorse
				}
			}
		}
	}
	return []Fact{wait, cpu, blocking, pressure}
}
