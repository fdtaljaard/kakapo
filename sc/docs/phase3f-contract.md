# Phase 3f contract: the DarlingData adopt-now batch

The 21 items of `docs/darlingdata-review.md` "Adopt now", made precise. Sources are files under
`../research/DarlingData-main/DarlingData-main` with the line numbers the review gives; the rule is
taken, the code is ours (review, "The rule on embedding"). Every closely copied fragment carries a comment
`// Source: DarlingData <file> <lines> (MIT)` and the file is listed in `THIRD_PARTY_NOTICES.md` (created in
this phase, root of the repository, with the MIT notice).

Rules that hold for every item:

- Reads stay SELECTs in their own TRY, as the collectors do today: a failed area gives that area's error field
  and the check is `skipped` with the reason, never a failed card.
- A column that does not exist on an older version is checked with `COL_LENGTH` (or `OBJECT_ID` for a view)
  and the read uses `sp_executesql` for the version-dependent part, as `readQueryStoreTop` already does for
  `sys.query_store_wait_stats`.
- Every batch kakapo sends starts with the tag `/* kakapo */` (item 2). `collect` has one place that prefixes
  it; nothing bypasses it.
- Finding texts: the sentence states the fact with its numbers, the why says what it costs the business, the
  Do this says who does what. No DBA jargon without the plain words next to it. Owner and Then check per the
  catalogue rows this document adds.
- New catalogue ids are already in `internal/domain/finding.go` (added with this contract): BAK06, DBC12,
  INS14, INS15, INS16, DBC13, DBC14, PRS02, WRK08. Tests in `*_test.go` for every new rule and every changed
  threshold, in the style of the existing tables.

## A. Performance (`domain/performance.go`, `collect/performance.go`, `collect/deadlock.go`)

**1. QRY01 spills and memory grants (sp_QuickieCache 283-308, 806-894).** `TopQuery` gains `Spills int64`
(total), `MaxGrantKB int64`, `MaxUsedGrantKB int64`, `TempdbKB int64` (Query Store: `avg_tempdb_space_used`
in pages times 8 times executions, max over plans), each -1 when the column does not exist. Plan cache:
`dm_exec_query_stats.total_spills`, `max_grant_kb`, `max_used_grant_kb` (2016 SP2+, check with
`COL_LENGTH`). Query Store: `query_store_runtime_stats.avg_tempdb_space_used`, `max_query_max_used_memory`
(2017+). Rules, replacing the plan-XML `SpillToTempDb` and `MemoryGrantWarning` flags which never fire on
cached plans (keep the XML for the other warnings):
- spills: `Spills > 0` and spills per run >= 1,000 pages (8 MB): the sentence adds "It spills to TempDB
  (about {MB} MB per run)". Do this adds "A spill means the memory estimate was wrong: stale statistics
  (MNT04) or a bad row estimate; update statistics first."
- grant: `MaxGrantKB >= 102400` (100 MB) and `MaxUsedGrantKB*10 < MaxGrantKB` (used under 10%): the
  sentence adds "It asks for {MB} MB of memory and uses {MB} MB". Do this adds "An oversized grant blocks
  other queries' memory (PRS01, RESOURCE_SEMAPHORE): the estimate is too high, usually a stale statistic or a
  wide SELECT."
Both make the finding Low as the other warnings do. Evidence names the numbers.

**2. QRY01 noise (sp_QueryStoreCleanup 647-664).** The plan-cache read excludes statements matching any of
`%FROM sys.%`, `ALTER INDEX%`, `UPDATE STATISTICS%`, `%SELECT StatMan%`, `DBCC%`, `(@_msparam%`, and any
text containing `/* kakapo */`; the Query Store read already excludes the tag and now excludes the same
patterns. The tag prefix goes on every batch in `collect` (one helper, used by every query string, including
the fix statements through `ExecFix`). Test: the helper tags, and a query with the tag is not among the top
queries (unit test on the SQL text filter function if the filter is built in Go; otherwise the SQL contains
the six patterns).

**6. WRK01 floor (sp_PerfCheck 2280-2410).** A wait becomes a finding only when its wait time is at least
10% of uptime (`WaitMs >= UptimeHours * 3600000 / 10`; under 24 hours of uptime use the real hours, minimum
one). Below the floor the wait still appears in the evidence of the top-five list but as Info with the
sentence "Wait #n: X (p% of all waiting; small against uptime)." and When/Effort None. The card's verdict
sentence uses the same floor to choose "Nothing stands out". Test: a 65% LCK_M_S wait of 200 seconds on a
30-hour uptime is Info; 4 hours of it is Medium.

**10. WRK08 CPU history (sp_PressureDetector 3951, 4012).** New check, clock `since-restart` with its own
heading text "last 4 hours" in the sentence. Read the `SchedulerMonitor` ring buffer
(`sys.dm_os_ring_buffers`, `RING_BUFFER_SCHEDULER_MONITOR`, records with `SystemHealth`), last 256 records
(about 4 hours at one a minute): `SQLProcessUtilization`, `SystemIdle`, other = 100 - both. Facts: `CPUHistory
[]CPUSample{At, SQL, Other, Idle}` and `CPUHistoryError`. Rule: average over the samples; fires Medium when
other >= 25% ("Other programs on {server} used {n}% of the CPU on average over the last {h} hours; SQL Server
used {m}%."; owner Client IT; why: everything SQL Server does waits behind them; Do this: name the usual
suspects: Granite services, antivirus scanning data files, a web shop, backup software; move them or exclude
the SQL files from scanning), or when SQL >= 80% ("SQL Server used {m}% of the CPU on average over the last
{h} hours."; owner Us; Do this: the costliest queries (QRY01) and missing indexes (WRK03) first, then CPU
count/MAXDOP (INS04)). Both can fire. Evidence: the peak minute of each. Skipped with its reason when the
ring buffer cannot be read or holds fewer than 5 samples ("too soon after restart").

**11. PRS02 memory grant timeouts (sp_PerfCheck 1140-1170; sp_PressureDetector 3654).** New check, clock
`since-restart`: `SUM(timeout_error_count)` over `sys.dm_exec_query_resource_semaphores` (read in the same
batch as the grants). > 100 High, > 10 Medium: "{n} queries gave up waiting for memory since the restart
(error 8645)." Why: each is a failed query the user saw as an error or a timeout. Do this: the queries with
the largest grants (PRS01 evidence, QRY01's grant warning) and max server memory (INS02). Plus PRS01 evidence
gains the top three current grant holders from `sys.dm_exec_query_memory_grants` (session, granted MB,
requested MB, wait time, the statement's first 200 characters), read only when `Waiting > 0`.

**19. QRY01 workload note (sp_QuickieCache 2240-3075; sp_QuickieStore 7128-7140).** The QRY01 card note
(the `notes["QRY01"]` text) gains one sentence: the top five's share of the source's CPU as "The top five
queries take {p}% of the CPU: concentrated (>= 50%): tuning them pays" / "moderate (>= 25%)" / "flat (under
25%): no single query dominates; look at the waits and the server first". Per query: when `Execs <= 10` and
share >= 5%, the sentence adds "Rare but expensive: {n} runs took {p}% of the CPU." Plan instability:
`TopQuery` gains `Plans int` (Query Store: `COUNT(DISTINCT plan_id)`; plan cache: distinct `plan_handle`) and
`LowRunPlans int` (plans with under five runs); when `Plans > 1` and `LowRunPlans == Plans` and the text has
no `RECOMPILE`, the sentence adds "It gets a new plan almost every time it runs ({n} plans, none used five
times)." with Do this "Plans that never get reused usually mean the values are typed into the SQL text
(SRV06) or the options differ per connection (ARITHABORT, see below)." Parameter sensitivity wording: when
`varies` fires, choose the sentence by what moves: reads vary but CPU per run does not (max CPU under 3x avg
CPU per run) => "same rows, different work: a sniffed parameter"; CPU follows reads => "the work follows the
rows: some inputs simply ask for more"; elapsed varies (max elapsed >= 3x avg) but reads do not => "the time
varies while the work does not: it waits (blocking or disk) on some runs". Facts needed: add `MaxElapsed
int64` to `TopQuery` from both sources.

**20 (Performance part).** QRY01's Do this ends with "In SSMS run SET ARITHABORT OFF before testing a query
from the application, or you get a different plan than the application does." ACT01 counts a session blocked
only by itself (`blocking_session_id = session_id`, parallel waits) as a lead blocker and says so: "(waiting
on its own parallel threads)". The blocking Do this (ACT01 and the waits-more-than-it-works branch) takes the
two-branch RCSI wording: when `is_read_committed_snapshot_on` is off for the database: "…or READ COMMITTED
SNAPSHOT for the database (decide first: it uses TempDB for row versions)"; when on: "The database already
has READ COMMITTED SNAPSHOT, so this blocking is writers against writers or a lock hint; the fix is in the
code or the index." `PerformanceFacts` gains `RCSI bool` (read with the database state).

**21. system_health components (sp_HealthParser 387-442, 1777-1800, 3090-3135, 4700-4750).** The deadlock
reader (`collect/deadlock.go`) already filters `object_name = 'xml_deadlock_report'`. The same on-request read
now also reads `sp_server_diagnostics_component_result` events of the last 7 days for `component = SYSTEM`
(non-yielding tasks `nonYieldingTasksReported`, `totalDumpRequests`, `intervalDumpRequests`, `badPagesDetected`,
access violations `sickSpinlockType`/`latchWarnings`) and `component = IO_SUBSYSTEM` (`ioLatchTimeouts`,
`totalLongIos`, `longestPendingRequests_filePath` and duration), keeping only state `warning` or `error`
rows. `DeadlockReport` gains `health []HealthEvent{at, component, state, detail}` (at most 50, newest first),
stored in the same JSON. The deadlocks panel (`web/src/components/deadlocks-panel.tsx`) shows them under the
pairs as "Server health events (7 days)" with time, component and detail; nothing when empty.

## B. Setup and fixes (`domain/setup.go`, `domain/fix.go`, `domain/querystore.go`, `collect/setup.go`,
`service/setup.go`)

**3. Pending RECONFIGURE (sp_PerfCheck 4146-4166).** `SetupFacts` gains `PendingConfig []PendingConfig{Name,
Value, ValueInUse}` from `sys.configurations WHERE value <> value_in_use`. INS14 (Medium, owner Us, clock not
used): "{n} server settings have a changed value that has not taken effect: {name} ({in use} -> {pending}),
…" Why: the next RECONFIGURE, from anyone, activates all of them at once. Do this: decide each one, then run
RECONFIGURE in a quiet moment, or set the value back. Every server-scope fix preview (`buildFix` in
`fix.go`) adds the warning "RECONFIGURE also activates these pending changes: {name} {in use} -> {pending}"
when any pending value other than the fix's own setting exists; `FixPreview` is built from facts that now
carry the list. The apply path records those names in the change description.

**9. readonly_reason (sp_PerfCheck 4805-4820).** `ReadonlyReasonText` decodes, by bit, 1 "the database is in
read-only mode", 2 "the database is in single-user mode", 4 "the database is in emergency mode", 8
"secondary replica", 65536 "storage full", 131072 "the runtime statistics limit was reached", 262144
"internal error", 524288 "user shutdown"; several bits join with ", ". Tests for each and for a combination.

**12. DBC13 not MULTI_USER (sp_PerfCheck 4663-4676).** `user_access_desc` from `sys.databases` in the database
facts. SINGLE_USER or RESTRICTED_USER: High: "{db} is in {mode} mode: the application cannot connect." Owner
Us, Do this: `ALTER DATABASE [db] SET MULTI_USER` once whoever set it is done (a restore or a repair usually).
Not a fix (someone set it on purpose).

**13. DBC14 scoped configurations (sp_PerfCheck 4975-5110).** `sys.database_scoped_configurations` (2016+):
read every row and compare with the defaults (MAXDOP 0, LEGACY_CARDINALITY_ESTIMATION OFF, PARAMETER_SNIFFING
ON, QUERY_OPTIMIZER_HOTFIXES OFF, and for 2019+: the rest as the source lists them; take the defaults list
from the source into a Go table with its comment). Info, one finding listing each non-default as
"{name} = {value} (default {default})", with a Medium when MAXDOP is set here and differs from the server's
(INS04 then reads wrong). Owner Us.

**14. INS15 global trace flags (sp_PerfCheck 1486-1615).** `DBCC TRACESTATUS(-1) WITH NO_INFOMSGS` into a table
variable, global flags only. One finding per flag with the source's one-line text: 1211 and 1224 (lock
escalation), 3608/3609 (startup), 834 (large pages), 4199, 8048, 1117, 1118, 2371. Severity: 1211/3608/3609
Medium; 1224/834 Low; the rest Info. Owner Us. Unknown flags: one Info "Trace flags {list} are on; look each
one up before a restart removes or keeps it." Skipped when the login lacks permission (the DBCC needs
sysadmin on older versions: catch and skip).

**15. INS16 min memory, lightweight pooling, affinity (sp_PerfCheck 3877, 4023-4126).** From
`sys.configurations`: `min server memory (MB)` >= 90% of `max server memory (MB)` (and max is not the default
2147483647): Low "Min server memory is set to {n} MB, {p}% of max: SQL Server cannot give memory back to
Windows." `lightweight pooling` = 1 or `affinity mask` / `affinity64 mask` / `affinity I/O mask` <> 0: one Low
finding naming which. Owner Us. Not fixes.

## C. Backups, Upkeep, Storage (`domain/backups.go`, `domain/upkeep.go`, `domain/storage.go` and their
collectors)

**4. BAK06 backups to NUL (TestBackupPerformance 942).** `BackupFacts` gains `NulFulls []time.Time` (type D,
not copy-only, `backupmediafamily.physical_device_name = 'NUL'`, last 30 days) and `LastRealFull *time.Time`.
High when any: "{n} full backups in the last 30 days were written to NUL (thrown away), the last on {when}:
every differential since then cannot be restored." Why: a backup to NUL resets the differential base; the
differentials in msdb look fine but need a full that does not exist. Do this: stop the job or script that
does it (usually a test); take a real full backup now so the differentials have a base. Owner Client IT.
BAK05's restore points must exclude NUL fulls and the differentials that depend on them (differentials whose
`differential_base_guid` matches a NUL full).

**5. DBC12 delayed durability (sp_PerfCheck 5145-5158).** `delayed_durability_desc` from `sys.databases`.
FORCED: Medium; ALLOWED: Low: "{db} has delayed durability {mode}: a committed transaction can be lost if
SQL Server crashes." Owner Us, Do this: `ALTER DATABASE [db] SET DELAYED_DURABILITY = DISABLED` unless the
vendor asked for it. On the Backups card (it is a data-safety fact). Not a fix in this phase.

**7. MNT05 corrections (sp_PerfCheck 1984, 1915-1935).** The trace read splits cache clears: a
`DBCC FREEPROCCACHE` with a plan handle argument (`FREEPROCCACHE (0x` or `FREEPROCCACHE(0x`) is `TraceKind
"evict"`, Info: "One plan was evicted from the cache {n} time(s) in the last 7 days." (why: harmless, it
is how a DBA fixes one bad plan; no Do this beyond "no action"). Bare FREEPROCCACHE, FREESYSTEMCACHE,
DROPCLEANBUFFERS stay "cache" Medium. The trace read also takes event classes 94 and 95 (auto shrink data /
log) as `TraceKind "autoshrink"`, Medium: "AUTO_SHRINK ran {n} times in the last 7 days on {db}." with DBC
auto-shrink's why and the fix id `auto-shrink` when the database is this one.

**8. MNT07 notifies no one (Alerts.sql 55-190).** The alerts read joins `msdb.dbo.sysnotifications` (and
`sysoperators` enabled with an email): `UpkeepFacts` gains `SilentAlerts []string` (enabled alerts in the
scan's list with no notification to an enabled operator with an email). Finding when any: "{n} alerts exist
but tell no one: {list}." same rank as the missing-alert finding, Do this: add the operator to each alert
(Alerts > Properties > Response) or recreate them with notifications.

**16. Slow autogrowth evidence (sp_PerfCheck 1860-1910).** `TraceGrowth` gains `SlowestMs int64`,
`SlowestFile string`, `SlowestAt time.Time` (the growth event with the largest `Duration`; Duration is in
microseconds). When `SlowestMs >= 1000`, MNT05's growth finding evidence adds "The slowest growth took {s} s
({file}, {when})." and STO02/STO03 evidence for that file adds the same sentence (storage facts gain the same
three fields from the trace, read only when the default trace is on; `StorageFacts.SlowGrowth *SlowGrowth`).

**17. STO09 per volume (sp_PerfCheck 3275-3315).** Group the files that fire by volume (`volumesOf`): one
finding per volume and kind: "Data reads average {ms} ms on {n} files on {volume} ({worst file} is the
slowest at {ms} ms)." / "Log writes average …". Rank is the worst file's. Evidence lists each file with its
reads, writes and averages. A volume with one file reads as before.

**18. TMP03 and TMP01 (sp_PressureDetector 2716-2811; sp_PerfCheck 2446).** `StorageFacts` tempdb use gains
`UserObjectsMB`, `InternalObjectsMB`, `VersionStoreMB` (from `sys.dm_db_file_space_usage` in tempdb), the
top three sessions by pages (`sys.dm_db_session_space_usage` + `sys.dm_db_task_space_usage`, login, program,
MB) and the oldest snapshot transaction (`sys.dm_tran_active_snapshot_database_transactions`: elapsed
seconds, session). TMP03's sentence adds what fills it: "Mostly {user objects (temp tables) | internal objects
(sorts, hashes, spools) | the version store (a transaction has held row versions for {m} minutes)}." and
the Do this branches: user objects => the top sessions (named in the evidence); internal objects => the
spilling queries (QRY01); version store => the oldest snapshot transaction (session {n}, {m} minutes).
TMP01: when the PAGELATCH_% waits (`PerformanceFacts` is not available here; read the sum of
`sys.dm_os_wait_stats` for `PAGELATCH_UP` and `PAGELATCH_EX` in the storage instance read) are >= 1% of
uptime and files < min(CPUs, 8), the rank rises one step and the sentence adds "and the server is likely
already queuing on TempDB allocation pages ({p}% of uptime in PAGELATCH waits)".

**20 (Upkeep and Storage part).** MNT03's Do this: "Nightly statistics updates (Ola Hallengren's IndexOptimize
with @StatisticsModificationLevel = 5 and no index work) matter most; rebuild or reorganise indexes weekly or
not at all." STO02's Do this: "On SQL Server 2022 and later log growth up to 64 MB is instant, so 64 MB steps
cost nothing." (appended only when the engine major >= 16; `StorageFacts` has the version).

## What does not change

The API, the routes, the card page and the finding card. The deadlocks panel gains one list (item 21). The
`Finding` and `CardDetail` shapes are unchanged; new facts live in the facts structs and the stored card
reads (JSON) as before. No new tables. No new fixes.

## Done when

`go build ./... && go test ./...` pass; `.\build.ps1` produces the exe; on `localhost\SQL2022` with Laylax:
QRY01 no longer lists kakapo's own statements or UPDATE STATISTICS; WRK01's LCK_M_S from today's test reads
"small against uptime" as Info; the Setup card shows INS14 when `sp_configure 'cost threshold for parallelism',
50` is run without RECONFIGURE and the max-server-memory preview warns about it; INS15 shows a trace flag after
`DBCC TRACEON(1117, -1)` (and the flag is turned off again); the Backups card shows DBC12 after `ALTER DATABASE
[throwaway] SET DELAYED_DURABILITY = ALLOWED` on a throwaway database; every other item has a unit test.
