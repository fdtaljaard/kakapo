# What to adopt from the DarlingData scripts

Reviewed 2026-11-03 against the five built cards and the planned Design card, live sample and tuning report.
Source: `External References/DarlingData-main` (MIT, Copyright 2026 Darling Data, LLC). Two detailed reviews with
line numbers sit in the session scratchpad; this file is the decision.

## The rule on embedding (revised 2026-11-03)

Writing to the monitored server is allowed through the fix pattern (SCOPE section 3, item 9: preview, confirm,
apply, verify, log, with a remove). "No install" was about kakapo itself: one exe, nothing installed on the
machine that runs it. So the question is not permission but fit, and the answer has two halves.

**The cards reimplement each adopted rule as a small SELECT in `collect` plus a pure rule in `domain`.** The
cards need the finding model (a sentence, a why, a Do this, a severity, a check status, evidence). Erik's
procedures return DBA-shaped grids, hundreds of columns across many result sets, whose shape changes between
versions; calling them and translating their output is more code and more fragile than taking the rule and the
DMV read, which is what every item below does. Where a fragment is copied closely (the wait-category map, the
`set_options` decode, the Query Store noise list, the blocked-process-report XPaths, the trace-flag and
readonly_reason texts, the scoped-configuration defaults), it carries a source comment and an entry in
`THIRD_PARTY_NOTICES.md`.

**Installed helpers are a later phase, each a fix with its remove and an "Installed helpers" line on the
instance page.** Three things become possible that reads alone cannot do:

1. A kakapo Extended Events session per server (blocked process reports and deadlocks to a file target, the way
   Lite's sessions work), with the blocked process threshold as the server fix that feeds it. This is the blocking
   history that `system_health` misses.
2. An opt-in utility database holding Erik's procedures as shipped (MIT, versioned) behind an **Expert** page:
   run sp_QuickieStore, sp_PressureDetector, sp_IndexCleanup from the app and show their grids as they are. The
   cards stay the product; this is the DBA view beside them.
3. Fixes that create things: the missing Agent alerts and notifications once an operator is chosen, the nightly
   statistics job when Ola's IndexOptimize exists.

A site that refuses installs loses the XE history and the Expert page, nothing else.

## Adopt now (one batch, "3f", before the Design card)

Correctness first: these fix things the built cards get wrong.

| # | What | Card | Why it is a must | Source |
|---|---|---|---|---|
| 1 | QRY01's spill and memory-grant warnings never fire: `SpillToTempDb` and `MemoryGrantWarning` exist only in actual plans. Read `dm_exec_query_stats.total_spills`, `max_grant_kb`, `max_used_grant_kb` (plan cache) and `avg_tempdb_space_used`, `max_query_max_used_memory` (Query Store) instead, checking the columns exist. | Performance | A built check that cannot fire | sp_QuickieCache 283-308, 806-894 |
| 2 | QRY01 ranks maintenance and the app's own reads as costliest queries. Exclude the noise patterns (`%FROM sys.%`, `ALTER INDEX%`, `UPDATE STATISTICS%`, `%SELECT StatMan%`, `DBCC%`, `(@_msparam%`) and tag every kakapo batch `/* kakapo */` and exclude it. | Performance | Today's Laylax top queries include our own statistics update | sp_QueryStoreCleanup 647-664 |
| 3 | Pending RECONFIGURE: our server fixes run RECONFIGURE, which activates **every** pending value, not just ours. The fix preview must list pending values (`value <> value_in_use`) and the Setup card gets the check (Medium). | Setup, fixes | A safety gap in our own fix pattern | sp_PerfCheck 4146-4166 |
| 4 | Full backups written to NUL (type D, not copy-only, `backupmediafamily.physical_device_name = 'NUL'`) reset the differential base, so later differentials cannot restore. New BAK06, High. | Backups | Silent restore failure | TestBackupPerformance 942 |
| 5 | Delayed durability on: committed transactions can be lost on a crash. New DBC12 on the Backups card, Medium. | Backups | Data loss the Backups card does not see | sp_PerfCheck 5145-5158 |
| 6 | WRK01 judges waits as a share of all waits, which always sums to 100%, so an idle server scores Medium. Add a floor: a wait fires only when its time is at least 10% of uptime. Laylax showed exactly this today (LCK_M_S 65% from one test). | Performance | A false Medium | sp_PerfCheck 2280-2410 |
| 7 | MNT05 calls `DBCC FREEPROCCACHE (0x…)` a cache clear. Evicting one plan is Info, not Medium. Also add auto-shrink events 94/95 to the trace read. | Upkeep | A false positive | sp_PerfCheck 1984, 1915-1935 |
| 8 | MNT07 checks that alerts and an operator exist but not that any alert **notifies** someone (`sysnotifications`). | Upkeep | An alert that tells no one is the same as none | Alerts.sql 55-190 |
| 9 | Query Store `readonly_reason` texts: decode 1, 2, 4, 131072, 262144, 524288 as well as 8 and 65536. | Setup | Better words for a state we already show | sp_PerfCheck 4805-4820 |

Then the gaps that are cheap because the reads already exist:

| # | What | Card | Source |
|---|---|---|---|
| 10 | CPU history for the last four hours from the scheduler-monitor ring buffer: SQL Server versus other processes. Fire when other processes average >= 25% (Client IT: Granite services, antivirus, a web shop) or SQL averages >= 80%. "SQL is hot" and "something else on the box is hot" are two different answers. | Performance | sp_PressureDetector 3951, 4012 |
| 11 | Memory grant timeouts (`timeout_error_count` > 10 Medium, > 100 High) on the semaphore read PRS01 already does; a timeout is a failed query (error 8645). Plus the top three current grant holders as PRS01 evidence (`dm_exec_query_memory_grants`). | Performance | sp_PerfCheck 1140-1170; sp_PressureDetector 3654 |
| 12 | Database not MULTI_USER (High: the application cannot connect). | Setup | sp_PerfCheck 4663-4676 |
| 13 | Non-default database scoped configurations (a database-level MAXDOP or PARAMETER_SNIFFING overrides INS04 unseen), Info with the values. | Setup | sp_PerfCheck 4975-5110 |
| 14 | Global trace flags: 1211/3608/3609 Medium; 1224/834 Low; 4199, 8048, 1117, 1118, 2371 Info, with the one-line texts. | Setup | sp_PerfCheck 1486-1615 |
| 15 | Min server memory >= 90% of max (Low); lightweight pooling or affinity masks set (one Low finding). | Setup | sp_PerfCheck 3877, 4023-4126 |
| 16 | A single autogrowth slower than one second from the default trace as evidence on STO02/STO03 and MNT05 ("the last growth of GraniteLive_log took 4.2 s"). | Storage, Upkeep | sp_PerfCheck 1860-1910 |
| 17 | STO09 grouped per volume ("three files on D: read slowly"), one finding for Client IT instead of one per file. | Storage | sp_PerfCheck 3275-3315 |
| 18 | TMP03 evidence: what fills tempdb (user objects, internal objects, version store; top sessions; oldest snapshot transaction), because the Do this differs for each. TMP01 severity bump when PAGELATCH waits are >= 1% of uptime and files < min(CPUs, 8), worded "likely". | Storage | sp_PressureDetector 2716-2811; sp_PerfCheck 2446 |
| 19 | QRY01 workload note: the top five's share of CPU as "concentrated (>= 50%)", "moderate (>= 25%)" or "flat", and a "rare but expensive" sentence for <= 10 runs with >= 5% share. Plan instability flag: more than one plan with under five runs each and no RECOMPILE. The parameter-sensitivity wording: same rows but CPU swings (sniffing) versus CPU follows rows (volume) versus duration-only swings (waits). | Performance | sp_QuickieCache 2240-3075, 2900-2915; sp_QuickieStore 7128-7140, help 492-545 |
| 20 | Text only: MNT03's Do this suggests nightly statistics (Ola `@StatisticsModificationLevel = 5`) with index work weekly or not at all; STO02's Do this says log growth up to 64 MB is instant on SQL Server 2022+; QRY01's Do this adds "run SET ARITHABORT OFF in SSMS or you get a different plan"; the two-branch RCSI wording for blocking ("you might need RCSI" / "you already have RCSI, but…"); ACT01 counts a session blocked only by itself (parallel waits) as a lead blocker. | Upkeep, Storage, Performance | Ola job 107; sp_PerfCheck 5338; sp_QueryReproBuilder; BlockViewer 3687-3725; WhoIsActive views 146-156 |
| 21 | DLK01's file reader filters `object_name = 'xml_deadlock_report'` in the `fn_xe_file_target_read_file` call (seconds instead of a full pass), and the same read also returns the SYSTEM component (non-yielding tasks, access violations, dump requests, bad pages detected) and the IO_SUBSYSTEM long I/Os, as on-request evidence for Upkeep MNT06, Backups BAK04 and Storage STO09. | Performance, Upkeep, Backups, Storage | sp_HealthParser 387-442, 1777-1800, 3090-3135, 4700-4750 |

## Adopt into the Design card contract (not built yet)

| What | Source |
|---|---|
| Restore-aware index usage window: the latest of create date, server start and the last restore from `msdb.dbo.restorehistory`, named in the sentence ("since the restore on 12 Oct, 21 days"). Copied Granite databases arrive exactly this way and the triage overstates their history. | sp_IndexCleanup 1850-2005 |
| Unused-index rule skipped under 7 days of history, a warning under 14, and held while uptime <= 7 days. | 4052-4080 |
| Readable-secondary guard: in an AG with a readable secondary, reads on the secondary are not counted, so IDX03 drops to Info. | 1918-1930 |
| Writes saved per day in the IDX03 sentence instead of totals. | 9530-9560 |
| Same keys in a different order: a fourth IDX04 verdict, Info, never applied. | 5367-5470 |
| Keep the triage where it is safer: IDX08's busy guard, IDX02 and IDX09 (no counterpart), the "fix the query first" twist. Do not take rules 3/4/6/7 (key-subset merges, unique-constraint replacement). | |

## Adopt later, by phase

| Phase | What | Source |
|---|---|---|
| Live sample | The delta shape (two snapshots, WAITFOR, difference); the perfmon shortlist (forwarded records, page splits, full scans, grants pending, free list stalls, workfiles, version store, longest transaction); memory clerks and stolen memory; token cache; head blocker's lock counts by index (escalation at >= 5,000). | sp_PressureDetector 4665, 2294-2330, 2881; Helper Views WhatsUpLocks |
| Performance step two | Blocking history for 7 days from system_health's `sp_server_diagnostics_component_result` blocked-process reports, parsed with the viewer's XPaths, grouped by blocker procedure and object; sleeping, implicit-transaction, serializable and compile-lock blocking classified. Check on a real site how much it catches: it misses short blocks. | sp_HumanEventsBlockViewer 1355-1410, 2092-2160, 3746-4160 |
| Installed helpers | A `kakapo` Extended Events session (blocked_process_report, xml_deadlock_report, file target beside the error log, 50 MB rollover, STARTUP_STATE ON) as an install fix with its remove, plus the `blocked process threshold (s)` = 5 server fix that feeds it; the blocking history above then reads kakapo's own files first and `system_health` second. An opt-in utility database (`kakapo_tools`, or one the user names) with sp_PerfCheck, sp_PressureDetector, sp_QuickieStore, sp_QuickieCache, sp_IndexCleanup, sp_HealthParser, sp_LogHunter, sp_HumanEventsBlockViewer installed from the embedded copies (MIT notice kept, version recorded), with an Expert page that runs one and shows its grids as they are, and a remove fix that drops the database. The instance page shows what is installed under "Installed helpers". | Lite's sessions; sp_HumanEvents' session DDL |
| Tuning report | Per-query regressed/improved list around a change with execution-weighted averages, at least 20 runs in both windows, a separate "new since the change" list, business hours, and "when it runs" (business / off-hours / weekend). | sp_QuickieStore 12415-12920, 6400-6430 |
| Upkeep | On-request error log for 7 days with the high-value phrases (I/O taking longer than, autogrow of file, 9002, paged out, 20 physical cores, SQL Server is starting, Server halted) and the noise list; a fix that creates the missing alerts and notifications once an operator is chosen; a fix that creates the nightly statistics job when Ola's IndexOptimize exists. The backup schedule fix (SCOPE 3b) belongs to the same family: Agent jobs created from the app, Ola's DatabaseBackup when present or installed. | sp_LogHunter 684-700, 911-935; Alerts.sql; Ola job |
| Setup | Single-use plans per database as SRV06 evidence; forced-plan failures; Query Store noise share; plan age ("the cache holds 6 hours"). | sp_QuickieCache 1100-1262; sp_QuickieStore 1427 |
| Storage | Page-compression candidates (Info, with the 20-60% estimate); buffer pool by object on request with a timeout. | sp_IndexCleanup 2555-2610, 8683-8720; WhatsUpMemory |
| Investigate a procedure | "Copy as repro": `sp_executesql` with the compiled values and SET options; plan-count reasons (literals, temp tables, ARITHABORT, modules). | sp_QueryReproBuilder 4589-5330 |
| Share | Hide query text must also strip parameter values, plan literals, input buffers and deadlock/blocking statements. | |

## Skip, with the reason

- sp_HumanEvents as shipped: its sessions observe heavy events (showplan, statement completion) and its tables and views log into the client's server. The kakapo XE session above takes the two cheap events only. The blocked process threshold fix comes with that session, not alone (setting it only raises the event; nothing records it without a session).
- sp_WhoIsActive Logging, every `@log_to_table`, sp_QueryStoreCleanup's removal mode: they log history into the client's server; kakapo's history is the DuckDB case file, which is the point of the product. TestBackupPerformance runs real backups. Helper Views as objects: the utility database carries procedures only.
- sp_IndexCleanup rules 3/4/6 (less safe than IDX08) and 7 (changes vendor constraints); `impact_score` (opaque to a non-DBA); the sniffing ±30% rule (the triage's is stricter).
- sp_PerfCheck items already covered or not for a Granite site: ADR, Ledger, Resource Governor, odd tempdb file counts, growth over 10 GB, CTFP between 6 and 49.
- Per-event storage latency thresholds of 500/1000 ms: the scan's averages are the right bar for a card.

## Where the toolkit stays better

The triage's "varies with input" (>= 10,000 reads and >= 20x), "waits more than it works" (>= 3x CPU and >= 1 s per
run), IDX08's busy guard, SRV06 by megabytes, IDX02 and IDX09, our offline-scheduler check (it ignores a
deliberate affinity mask), our deadlock thresholds, and the Granite code checks, which nothing in the repository has.
