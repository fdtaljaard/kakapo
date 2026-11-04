# Phase 3e contract: the Performance card (step one: the card; the live sample is step two)

Builds on phases 3a (finding model, card detail), 3b (on-request checks and samples) and 3c (fixes). Sources,
cited per check below: `01-QuickScan.sql` section 7 (lines 1069-1250: benign wait list, WRK01 to WRK04) and
WRK05 to WRK07 (1471-1550); `PerformanceTriage.sql` SRV04 (1091-1134), SRV07 (1586-1602), ACT01/ACT02
(1184-1223), ACT03 (1232-1270); `../research/sp_PressureDetector.sql` for the right-now pressure reads
(worker threads and schedulers around lines 4131-4161, memory grants around 3433-3436); `sp_QuickieStore.sql`
for the Query Store reading (sort orders and the wait categories near line 255); `DESIGN.md` 5.4 for the deadlock
report; `Checks.csv` for Owner and Then check. Field names exactly as written.

## Two clocks, kept apart on the page

Every finding carries `clock: "since-restart" | "now" | "window"`. The card page groups findings under three
headings in this order: **Right now**, **Since restart ({uptime})**, **Last 7 days** (Query Store, deadlocks).
`Finding` gains the optional field `clock`; findings without it (other cards) are shown as before.

When the server restarted less than 24 hours ago, the card carries the note "SQL Server restarted {n} hours ago:
the since-restart figures may not reflect a normal day" (WRK01's Info in the scan), as `card.note`.

## The checks

| Check | Clock | Fires when (as the source) | Severity | When / Effort |
|---|---|---|---|---|
| **PRS01** Pressure right now (new, sp_PressureDetector) | now | one finding when any of: worker threads in use >= 80% of `max_workers_count` (`sys.dm_os_schedulers` current_workers_count summed vs `sys.dm_os_sys_info`); runnable tasks per online scheduler averaging >= 2; memory grants waiting (`sys.dm_exec_query_resource_semaphores` waiter_count > 0) or forced grants > 0 since restart; any scheduler with `is_online = 0` that should be online | threads >= 90% or an offline scheduler: Critical; else High | None / Moderate. Sentence names each signal with its number: "12 queries are waiting for memory right now; 3 of 20 schedulers have 4 runnable tasks each." Why/Do this: written in the toolkit's voice (memory: find the queries with large grants, fix estimates, statistics; threads: blocking or parallelism, never raise max worker threads; runnable: CPU, tune WRK04/ACT03, check MAXDOP). |
| **ACT01** Blocking now | now | head blockers with the longest wait >= 5 s, scoped as the triage does but across the instance (the object names the database) | >= 30 s: High; else Medium | None / None |
| **ACT02** Open transactions | now | transactions open > 5 minutes in this database (triage) and sessions sleeping with a transaction open > 5 minutes instance-wide (WRK07, implicit ones called out) | >= 30 min: High; else Medium | WRK07: Decide first / Moderate; ACT02: None / None |
| **WRK01** Top waits explained | since-restart | the scan's benign list filtered, SLEEP_% out; top 5 by wait time with the scan's group, plain-English why and do-this; severity as the scan (THREAD >= 1%: High; MEM >= 10%: High; the named groups >= 25%: Medium; else Info) | as stated | as the scan |
| **WRK02** CPU pressure | since-restart | signal waits >= 25% of all waits | Medium | Decide first / Moderate |
| **WRK05** Poison waits | since-restart | the scan's list with >= 5 s total | THREADPOOL/RESOURCE_SEMAPHORE >= 60 s a day: High; else Medium | Decide first / Moderate |
| **WRK06** Deadlocks | since-restart | counter > 0 | >= 10 a day: High; >= 1: Medium; else Info | Decide first / Moderate (Info: None) |
| **SRV07** Compiles per batch | since-restart | >= 100,000 batches and compiles >= 10% | >= 25%: Medium; else Low | Decide first / Moderate |
| **SRV04** Which databases use the server | since-restart | always (Info), Low when another database uses >= 25% of query CPU | as stated | None |
| **QRY01** Costliest queries (new id; ACT03's rules on a better source) | window when Query Store is on, since-restart when not | top 5 query shapes for this database by total CPU over the last 7 days from Query Store (`sys.query_store_query`, `query_store_plan`, `query_store_runtime_stats` joined to `runtime_stats_interval`, grouped by `query_hash`; `query_store_wait_stats` for the top wait category per query as sp_QuickieStore's categories); when Query Store is off or has no data, the plan cache grouped by `query_hash` filtered to this database's plans, as ACT03, and the note says "from the plan cache since restart; turn Query Store on for history" | one finding per query, Low when it has a plan warning, varies with input, waits more than it works, or arrives as 50+ statement variants (ACT03's rules); else Info | Decide first / Moderate. Finding sentence as ACT03 with the numbers (share of CPU, runs, ms CPU and reads per run, runs a day, variants, the warnings). Evidence: min/avg/max reads, max CPU, top wait category, the statement text (first 1500 chars), query hash. Why/Do this verbatim from ACT03. |
| **WRK03** Missing index suggestions | since-restart | top 5 for this database by the scan's score, impact >= 50% and score >= 100,000 | score >= 1,000,000: Medium; else Low | Window (Online on Enterprise) / Moderate. Do this verbatim: review, never create them all blindly. **Never a fix.** |
| **DLK01** Deadlock report (new, DESIGN.md 5.4, on request) | window | see below | per pair: >= 10 in 7 days High, >= 3 Medium, else Low | Decide first / Moderate |

Plan warnings for QRY01 come from the plan XML as the triage reads them in its section 0 (implicit conversion,
spill, key lookup, scalar UDF, table variable, no join predicate, memory grant, optimizer timeout, missing index
hint); read the XML only for the top 5 and give up on a plan over 2 MB with "plan too large to inspect".

Owners from `Checks.csv` (WRK01/03/04/05/06 Us, WRK02 Client IT, WRK07 Vendor, ACT and SRV rows Us); PRS01 Us,
QRY01 Us, DLK01 Us. Then check from the CSV; for new ids: PRS01 "Re-run: PRS01 gone", QRY01 "Re-run after a
business day: the query's share is lower", DLK01 "Re-run after a week: fewer deadlocks for the top pair".

## DLK01: the deadlock report, on request

`POST .../databases/{db}/cards/performance/deadlocks` reads the `system_health` Extended Events session's file
target (`sys.fn_xe_file_target_read_file` on the session's current file, falling back to the ring buffer target
when the file target is missing), events `xml_deadlock_report` in the last 7 days, and for each deadlock
extracts: time, the victim and the winner process, their procedures (or statement text when ad hoc), the object
(the lock's object name or hobt), the lock types, the database. It then groups by the **pair of procedures**
(order-independent) and returns:

```ts
interface DeadlockReport {
  capturedAt: string
  durationSeconds: number
  days: number                 // 7
  total: number
  source: "file" | "ring-buffer"
  pairs: DeadlockPair[]        // by count desc
  findings: Finding[]          // one DLK01 finding per pair with count >= 1, ranked
}
interface DeadlockPair {
  procedures: [string, string] // e.g. ["dbo.PRESCRIPT_PICKING...", "dbo.IntegrationPost..."]
  count: number
  last: string
  objects: string[]            // the tables involved, distinct
  lastVictimStatement: string  // first 500 chars
}
```

DLK01 finding sentence: "{count} deadlocks in 7 days between {proc A} and {proc B}, on {objects}; the last was
{when}." Why: WRK06's why. Do this: "The fix is ours: an index so each side touches fewer rows, the same order of
touching the tables in both procedures, or READ COMMITTED SNAPSHOT for the database (decide first). Start with the
victim's statement in the evidence." Evidence: the last victim statement and the lock types.

Stored in a new table `deadlock_report` (one row per run with the JSON of pairs) and shown on the card page like
Room for CHECKDB: the last result with its time, a button "Read deadlocks (7 days)", and a note that reading the
session files can take a minute on a busy server. Before any run, DLK01 is `skipped` with "Not run yet…".
`CardDetail` gains `deadlocks?: DeadlockReport | null` for the performance card.

## The card

- **Facts** (four): `Biggest wait` = "{WAIT_TYPE} {pct}%" with the group word in brackets, e.g. "CXPACKET 41%
  (parallelism)" (bad when WRK01 fired Medium or worse); `CPU pressure` = "{signal pct}% signal waits" (bad when
  WRK02 fired); `Blocking now` = "none" | "{n} sessions blocked, longest {s} s" (worse when ACT01 High, bad when
  Medium); `Pressure now` = "none" | the shortest PRS01 phrase (worse/bad by its severity). Deadlocks per day sit
  in the WRK06 finding and the verdict when it is the top one.
- status / statusText / verdict / toDo / forClientIT: the phase 3a rules, over every clock together; fixable = 0.
  Green verdict: "Nothing stands out: the top wait is {wait} at {pct}%, no blocking, no memory or thread pressure."
- Grey with the reason when `sys.dm_os_wait_stats` cannot be read (VIEW SERVER STATE).
- Reads (SELECTs only, each area in its own TRY): the scan's wait block with the benign list, the performance
  counters for deadlocks and compiles, `sys.dm_exec_requests` and sessions for blocking, the transaction DMVs,
  `sys.dm_os_schedulers`, `sys.dm_os_sys_info`, `sys.dm_exec_query_resource_semaphores`, `sys.dm_exec_query_stats`
  with plan attributes for SRV04 and the fallback, the missing index DMVs, and inside the database the Query Store
  views for QRY01. Reading plan XML for the top 5 only.
- One `card_read` per read; the sampler of step two is not part of this contract.

## Frontend

- The card page for `performance`: the three clock headings with their findings (an empty heading is omitted),
  then the **Deadlocks** panel (last report or the not-run note, the button, the pairs as a small table: pair,
  count, last, objects, and the DLK01 findings under it), then What was checked.
- Query findings show the statement text in a monospace block inside the evidence with its own copy button, since
  it is what a consultant pastes into SSMS.
- Mock: a Performance detail for the three databases (one green; one amber with WRK01 CXPACKET 41% Medium, WRK02,
  a QRY01 with an implicit-conversion warning and a stored deadlock report with two pairs; one red with PRS01
  Critical "threads 94%" and ACT01 High), and the deadlocks POST returning a report after a 2-second delay.
