# Phase 5j contract: the query page answers first (is it still a problem?)

Found in the live review of 5 Nov 2026 on `OldSchool` (restored 17:49 that day). The costliest query of the last
7 days, `SELECT * FROM API_QueryTrackingEntities ORDER BY Barcode OFFSET 0 ROWS FETCH NEXT 100 ROWS ONLY`
(hash 0xDE6731249BBE798C), was already fixed: 17 s and 2.4 million pages a run until 3 Nov 17:00, 10 ms and
8,537 pages since 4 Nov 15:34. The Performance card still ranked it first as a thing to do, and the query page
led with 7-day averages (71 s of CPU a run), said "seven plans of similar speed: the plan is not the problem",
offered an index that already existed and a stop-forcing based on a plan of another statement. The reader only
found out in the History table at the bottom. The reader is a novice SQL developer: the page must say first
whether the query still needs attention, then show the story, and keep the detail for learning, folded.

Nothing is written to the server; every read is a SELECT. No thresholds of our own: the verdict is 5c's
`TuningVerdict` (20 runs on each side, 0.8 and 1.25).

Facts verified on `OldSchool` (5 Nov 2026):
- The fix came in two steps. Statistics on MasterItemAlias (17:16, every row) and Location (20:32, every row)
  were updated on 3 Nov; plan 269671 compiled at 22:55 (0.3 s, 51,263 pages a run). The index
  `IX_MasterItemAlias_MasterItem_id_kakapo` has statistics dated 4 Nov 15:00 (its creation); plan 270017
  compiled at 15:34 (10 ms, 8,537 pages). `sys.dm_db_stats_properties.last_updated` dates both.
- One query hash is 5 Query Store queries (OFFSET 0, 100, 200, 300 and one written with "asc"). Plan 268947 is
  forced on the OFFSET 200 query only; plan forcing is per `query_id`.
- The plans name `[GraniteDatabaseLive]`, the database they were compiled in on production; so the page found
  none of the tables its plans read.
- The query ran about as often after the fix (87 runs on Thu 5 Nov) as before (72 to 109 on working days).

## 1. The verdict (query page and QRY01)

- **The plan in use** is the plan that ran most recently. **Since** is when it began: its first hour in the
  history, or its first compile when that falls in that hour. **Before** is every run of the query before Since
  in the window; **Now** is every run from Since on (all plans). No runs before Since: the query ran one way
  all week, no verdict.
- `TuningVerdict(runs before, runs now, CPU per run before, CPU per run now)`: faster, slower, same, too few.
- The page's top block: a word (Faster, Slower, No change, Too few runs), one sentence ("Each run now takes
  10 ms. Before 4 Nov 15:34 it took 16 s."), Before and Now side by side (time, CPU and pages per run, runs, CPU
  in total), what changed, what to do (faster: nothing; slower or same: How it works; too few: wait for more
  runs), and the query text.
- **What changed**: in time order, from the last run of the costliest plan before Since up to Since: each plan
  SQL Server started using (with its time a run), and each statistics update on a table the plans read (grouped
  by table and minute; an index's own statistics alone are said as "created or rebuilt").
- **QRY01**: the same verdict from Query Store's intervals. A faster query stays in the list as Info ("Faster
  since 4 Nov 15:34: 10 ms of CPU a run, was 95 s. Nothing to do.") and is not a thing to do.

## 2. Over time

The history as one chart: CPU per hour (bars coloured by plan, the plan in use in the primary colour) and runs
per hour under it, the window's statistics updates marked. The hour table stays, behind "Show hours".

## 3. How it works (folded when faster)

- **What it means** keeps its sentences, corrected: CPU and pages per run say Now and Before when there is a
  verdict; a parallel query says why its CPU is more than its time; one hash of several queries says so.
- The "N plans of similar speed" sentence only when the plans with runs really are within 2x of each other
  in CPU per run; the forced-plan comparison only between plans of the same `query_id`; suggested indexes only
  from plans that ran since Since.
- **Plans** as a timeline of the plans that ran, in order, with the statistics updates between them. The full
  table (Force, Stop forcing, Save .sqlplan, plans with no runs) behind "Show all plans".
- **Waits**: one sentence; for Parallelism, a symptom of reading much. When Before's time is over 100x Now's,
  "nearly all of it is from before Since". The bars behind "Show waits", without Idle, Unknown and under 1%.
- Plan objects: when no object of a plan names this database and all name one other database, that database is
  this one under its old name (a restored copy).

Found while building (5 Nov 2026):
- QRY01's before and since sums cannot be one CROSS APPLY over `#rs` with `t.since` inside the aggregate
  (Msg 8124: an aggregate with an outer reference must reference only it); they are a join to `#top`. Read on
  OldSchool, four of the five top queries were faster since a new plan: the one above (4 Nov 15:34), two
  `IntegrationDocument` reads (5 Nov 10:30, 2.8 s to 109 ms a run) and `UPDATE DocumentDetail` (3 Nov 20:30, the
  plan forced at 20:36; 73 ms to under 1 ms). The card went from 5 things to do to 1.
