# Phase 5t contract: one lens over the indexes worth reorganising

Draft 2026-11-11, for agreement before building. Contract only: nothing built.

5q put a **Measure** button and the `reorganize-index` fix on the query page, for the indexes that one
query's plan reads in full. On a client it gave a large, measured speed-up. But finding those indexes is
manual: open a slow query, measure, apply, open the next. This phase adds one page for the whole database:
the indexes that real queries read in full, with a measured gain for each, and **Reorganise selected**.

It reuses 5q end to end. What 5q measures, what it applies and how it judges the result are not changed.
New: the list across queries, a stoppable batch, and one place to start.

SCOPE section 1a, rules 1, 2, 4 and 5. Index work is never offline.

## What it is not

- **Not "every fragmented index".** That is the home-grown job MNT09 finds and the 5q review warns about
  (Microsoft: "It's often sufficient to only reorganize or rebuild specific indexes used by queries with
  degraded performance"). An index is a candidate only when a query reads it in full.
- **Not scheduled.** The scheduled version is the intended follow-up (last section). Nothing here runs
  unattended.
- **Not a new judgement.** No new threshold on fragmentation or density. The facts and the estimate are 5q's;
  the verdict is still Query Store's before and after.

## Decisions to confirm

1. **Where it starts: the Upkeep card.** A link in the card's header beside the others, and one sentence in
   MNT09's *Do this* and the maintenance plan's index line ("kakapo lists the indexes your queries read in
   full"). The page itself is `/instances/:id/databases/:db/indexes`, "Indexes worth reorganising".
   - Why Upkeep: it is index *maintenance*; MNT09 already says "leave index work off unless a query shows the
     need", and this page is where the query shows it. Performance has its Tuning link already.
   - Alternative: the Performance card, where the evidence (slow queries) lives. The page is the same.
2. **Candidates come from Query Store only.** No Query Store, no list: the page says so and points to the Setup
   card's Query Store fix. There is no fallback to the plan cache: it forgets at a restart, and the evidence
   must be the queries' own history (the 5q verdict needs it too).
3. **The pick of queries: the top 100 query shapes by total duration over the last 7 days** (QRY01's window,
   grouped by `query_hash`), each with its most recently run plan of 2 MB or less (`planLimitChars`, as every
   plan read). "100" is kakapo's bound on how much plan XML is read, said as such; it limits the work, not
   the advice. An index that only a query outside the top 100 reads is not listed.
4. **A candidate** is an index that one of those plans reads with an `Index Scan` or `Clustered Index Scan`
   (5q's `Scan`, via `ShowplanTables`) and that has **1,000 pages or more** (Ola Hallengren's
   `MinNumberOfPages`). The page count is screened from `sys.dm_db_partition_stats` (cheap); the exact leaf
   count comes with the measurement. Heaps, columnstore and disabled indexes are left out (nothing to
   reorganise, as 5q). At most 50 rows, ordered by the total duration of the queries that read them.
5. **Measuring is explicit, and uses 5q's read.** **Measure all** runs `collect.IndexShape` (SAMPLED) one index
   after another, in the order of the list; each row also has its own Measure. Nothing is measured on page
   load. Measure all is driven by the page, one request per index, and **Stop** cancels the request in
   flight. Measurements are not stored: they are shown for this visit (they age the moment anything changes).
6. **Pre-ticked rows: measured, with a gain, statistics not stale.** A *gain* is 5q's: the estimated page
   saving is 5% or more (Ola's low level, used by 5q for the saving). A row whose index is only out of order
   (5% or more) but already about as full as it can be is listed, unticked, in 5q's words. Unmeasured rows are
   unticked. Pre-ticking stops once the ticked rows add up to the batch cap (decision 8).
7. **Where statistics are stale, statistics come first.** The table's statistics are tested with 5j's
   `qStatsStale` (the same rule the query page uses, exported for this). Such a row is unticked and shows
   "Statistics are 12 days old and 22% of rows have changed. Update them first: it is cheaper, and often all a
   slow query needs." with **Update statistics**, the existing `update-statistics-table` fix. The user may
   still tick it.
8. **A cap on the batch: 20 GB of indexes, an option.** In FULL recovery a reorganise logs about the pages it
   moves, so a batch carries roughly its total size to the next log backups (Microsoft; Brent Ozar, index work
   "inflates log backups"). No source gives a safe size, so the number is kakapo's, said as such: it stops the
   pre-ticking, and a larger selection gets a stronger warning. It does not block (More options: `maxGB`,
   default 20; 0 means no cap).
   - Alternative: no number; the preview only shows the total and the log estimate.
9. **The batch is a new long fix, `reorganize-indexes`, built on `reorganize-index`.** See the next section for
   why the maintenance plan's composite cannot carry it as it is.
10. **A row that was reorganised by kakapo says so.** From the change log: "Reorganised 12 Nov 21:04: 12,400
    pages to 7,250." The row is unticked and links to the slowest query's page, where 5q's verdict is. The page
    does not compute a verdict of its own.

## Can the maintenance plan's composite carry "apply selected"?

`maintenance-plan` (5b) is the only existing multi-step fix. Checked in `service/maintenance.go` and
`service/runs.go`:

| Needed | Composite | What this phase does |
|---|---|---|
| Prepare each piece through its own fix, on one connection | Yes (`preparePlanPart`) | Same pattern: a child `genericFix` per index. |
| Each piece writes its own change row | Yes | Same: `reorganize-index`'s apply already writes one INDEX row per index, so the 5q verdict works per index. |
| One failure must not block the rest | **No**: stops at the first failure | The loop continues and records the failure. |
| Runs for hours | **No**: a plain fix, synchronous | Registered as a long fix (5l/4f): it answers at once with a pending run. |
| Stop it | **No**: runs have no cancel | A run gains a stop. |
| Progress per piece | **No**: a run is pending or ended | A run gains items. |

So the registry, the preview pattern and `reorganize-index` carry it; the composite's loop does not. Two small
additions to the run registry (below) are the only change to shared code.

## 1. The read: `collect.IndexCandidates` (inside the database; SELECTs only, tagged, in a TRY per area)

1. **Query Store's state** (`sys.database_query_store_options`). Not READ_WRITE or READ_ONLY: the page says why
   and stops.
2. **The queries.** From `sys.query_store_runtime_stats` joined to the interval, plan, query and text, the last
   7 days, grouped by `query_hash`: runs, total duration, average logical reads, last run. The top 100 by
   total duration (decision 3). For each, the plan with the latest `last_execution_time` and its XML when
   `DATALENGTH(query_plan) / 2 <= @limit`. The query text is cut to 200 characters, kakapo's own statements
   (the `/* kakapo */` tag) left out.
3. **Parsing, in Go.** `ShowplanTables` over each plan gives the `Scanned` indexes; a plan compiled before a
   restore names the old database as 5j handles. Per index: the queries that read it, their total duration and
   runs.
4. **Sizes and statistics of what was found**, in one batch (the XML list read that `queryTablesQuery` already
   uses): per index, pages (`in_row_data_page_count` and `used_page_count`), type, disabled, `ALLOW_PAGE_LOCKS`,
   fill factor; per table, rows and statistics age and share changed (`sys.dm_db_stats_properties`).
5. **The last reorganise by kakapo** per index: the change log (`ListChangesSince`, 30 days), Object =
   `IndexObject(schema, table, index)`.

Cost: one read of plan XML for at most 100 queries (the same plans the Performance card already reads for
QRY01's top 5, twenty times as many) and one catalogue batch. No physical-stats read: that is Measure. The
page says "Reading the plans of your 100 slowest queries…" while it loads.

## 2. The page (`IndexLens` in the API, `screens/indexes.tsx`)

Answer first (PRODUCT.md; the answer-first rule). Before measuring:

> **Your slowest queries read 6 big indexes in full.** Measure them to see which are worth reorganising.
> [Measure all]

After Measure all:

> **3 of them would shrink by 5% or more: about 18,400 pages fewer to read.** The other 3 are already full and
> in order. Statistics on 1 table are old: update them first.

Then the rows, one per index, largest saving first once measured (otherwise by the queries' duration):

- a checkbox (disabled when the index cannot be reorganised: the reason in words);
- `schema.table` · index name, pages and MB, **page density, fragmentation, estimated saving "23% fewer
  pages"** (5q's sentence, `BuildIndexShape`), after measuring;
- **the evidence: the slow queries that read it.** The top three by total duration, each "Orders by date, 4,310
  runs, 38 min in 7 days, last ran 10 Nov", linking to its query page; "and 7 more" folded. This is why the
  index is on the list;
- a statistics note and **Update statistics** where decision 7 applies;
- the note from decision 10, where it applies.

Below the rows, a bar: "3 selected, 21,400 pages (167 MB)". **Reorganise selected** opens the fix dialog
(preview, confirm). **Copy** gives the finding block for whoever owns it, as every page does (SCOPE 3.10).
Phone width: each row stacks, no horizontal scroll.

When there are no candidates: "No query in the last 7 days reads an index of 1,000 pages or more in full. Index
work would not help your queries." That sentence is the answer, and the page ends there.

## 3. The fix `reorganize-indexes` (database; check QRY01, as `reorganize-index`)

**Options.** `indexes` (the chosen indexes, one `[schema].[table]|index` per line), `maxGB` (More options,
decision 8).

**Preview.** Built from one cheap catalogue read of the chosen indexes (not a SAMPLED read; the run measures
each index just before it works on it).
- *Changes*: one row per index that can be reorganised: name, table, pages.
- *Refused*, listed under the preview and not in the statement: not found, disabled, not rowstore,
  `ALLOW_PAGE_LOCKS` OFF (5q's refusals, from `reorgRefusal`). If none can run, Apply is blocked.
- *Statement* (for Copy): the `ALTER INDEX … REORGANIZE;` lines in order, one per index, with a comment line
  naming each, tagged `/* kakapo */`.
- *Warnings*, in this order:
  1. online on every edition; stopped at any time, keeping its progress; one index after another;
  2. one failure does not stop the others; a stop leaves the rest untouched;
  3. FULL recovery: the next log backups carry about the total size, "about 21,400 pages (167 MB)"; other
     models: the log empties at the next checkpoint (5q's wording);
  4. over the cap, the stronger sentence: "This is 34 GB of indexes, more than the 20 GB kakapo starts a batch
     with. Take the log backups into account, or tick fewer.";
  5. the data files need free space for working pages (error 1105), as 5q;
  6. it updates no statistics; selected tables with stale statistics are named (decision 7);
  7. **kakapo must stay open until it finishes** (the batch runs from kakapo, not from SQL Server Agent):
     closing it stops the batch (the index in flight keeps its progress), and a restart does not resume it;
  8. each index gets its own change-log row, so the query page compares the runs before and after per index;
  9. the ALTER permission on each table (`indexPermission`).
- Blocks when: nothing selected; no index can run; another `reorganize-indexes` run is pending for this
  database.

**Apply** (long fix; timeout = indexes × `indexApplyTimeout`, bounded by the run's own stop). For each index in
order:
1. prepare it through `prepareReorganizeIndex` into a child `genericFix` (one SAMPLED read);
2. if the preview is refused now (it changed since the page was read): *skipped*, with 5q's sentence;
3. if it is in order and full (fragmentation under 5% and saving under 5%, 5q's "nothing to gain"): *skipped:
   already in order*;
4. otherwise apply it with the facts just measured (the batch passes them on, so there is no third read; this
   needs `applyReorganizeIndex` split into "measure" and "reorganise and log"). Its own read-back (measure again)
   and its own INDEX change row;
5. record the outcome as the run's item; on a failure, record it and go on.

Between indexes the loop checks whether it was stopped. There is no summary change row: the per-index rows are
what the verdict reads.

The result: Before "6 indexes, 21,400 pages"; After "4 reorganised, 1 skipped (already in order), 1 failed";
Verified when none failed and it was not stopped.

**Run registry additions** (`service/runs.go`, `api/runs.go`): `FixRun` gains `Items []RunItem` (label, state
`waiting|running|done|differs|skipped|failed|stopped`, text, change id, set by the fix as it goes) and a
`cancel`. New route `POST /api/servers/{id}/databases/{db}/fixes/runs/{runId}/stop`: cancels the run's context
(SQL Server stops the ALTER in flight, which keeps its progress) and marks what was not started as *stopped*.
`GET …/fixes/runs/{runId}` returns the items. Existing long fixes have no items and no stop button. The lost
connection watch applies to the batch as to any run; its completion check marks the item in flight as unknown
and does not resume.

**Rollback.** None: the preview says a reorganise only moves pages and there is nothing to undo.

## 4. The query page, unchanged

5q's Measure and Reorganise stay as they are. The Measure sentence gains one line when the index is on this
list: "kakapo lists this index with others your queries read in full: Indexes worth reorganising." It is a link
to the page. (The only change to the query page.)

## Sources

All as 5q (to be re-read when building, each sentence the app shows citing its page):

- **Microsoft Learn, "Maintain indexes optimally to improve performance and reduce resource utilization"**
  (2026-10-27): reorganise only the indexes used by queries with degraded performance; measure the effect with
  Query Store; reorganise is preferred, always online, packs pages to the fill factor, updates no statistics,
  keeps its progress when interrupted; page density often matters more than fragmentation; the `SAMPLED` mode
  "to return actionable results quickly".
- **Ola Hallengren, "SQL Server Index and Statistics Maintenance"**: `MinNumberOfPages` 1,000;
  `FragmentationLevel1` 5.
- **Brent Ozar, "How to Configure Ola Hallengren's IndexOptimize Maintenance Script"**: index work inflates log
  backups, differentials and replicas.
- `docs/phase5q-contract.md`, section 4 and decisions 3 and 4; `docs/phase5j-contract.md` (stale statistics);
  `docs/phase5b-contract.md` and `docs/phase5l-contract.md` (the fix pattern, the room check).

kakapo's own bounds, said as such: the top 100 queries (decision 3), 50 rows (decision 4), the 20 GB batch cap
(decision 8).

## Tests

- **Domain:**
  - the candidate rule on fixtures: a scan of 999 and of 1,000 pages; a seek; a heap's table scan; a columnstore
    and a disabled index; two queries reading one index (totals add up); a query outside the top 100;
  - the pre-tick rule: gain 4.9% and 5%; out of order but full; unmeasured; stale statistics; the cap with 0
    meaning none;
  - the page's sentences, before and after measuring, and with no candidates;
  - `reorganize-indexes`' preview: refusals listed, statement order, every warning, the stronger cap sentence,
    each block;
  - the apply loop with fakes: a skipped item, a failed item that does not stop the next, a stop between two
    items and during one, the result text.
- **Collect:** `IndexCandidates` tagged and read only, each area in its TRY, Query Store off, the 2 MB limit, the
  kakapo-tag exclusion; the catalogue batch.
- **Service:** `reorganize-indexes` registered as a long fix; a second batch on the same database refused; the
  run's items and stop.
- **API:** the candidates route, the stop route, 404 and 409 cases.
- **Web:** the build; the page against the mock (Measure all, Stop, ticks, the bar, the dialog, phone width).

## Done when

`go test ./...` and the web build pass. Live, in a throwaway database with made-up names on
`localhost\SQL2022` (Developer), and again on the Express test instance with its test database only
(`SQLMONITOR_TEST_MAINT_DB`, as 5p); Query Store on, `READ_WRITE`:

1. Four tables, each run by a query about twenty times: **A** a fragmented 4,000-page index scanned in full;
   **B** a second such index; **C** a 300-page index
   scanned in full; **D** a large index only ever sought; **E** a table with stale statistics and a scanned big
   index. The page lists A, B and E, not C or D; E carries the statistics note and is not ticked.
2. **Measure all** fills the facts and the estimated saving, in order; **Stop** halfway leaves the measured rows
   measured and the rest unmeasured; nothing was measured on page load.
3. **Reorganise selected** with A, B and a third index made to fail (`ALLOW_PAGE_LOCKS = OFF` set after the page
   was read): A and B reorganised, the third failed, the batch carries on past it; two INDEX change rows, each
   with its read-back; the page shows A and B as "Reorganised … pages to …".
4. **Stop** during a batch of a large index followed by another: the one in flight stops (its progress kept,
   measured again afterwards), the next is *stopped*, not started; no change row for what did not run.
5. The query page of a query that reads A: its verdict splits at the change (5q's decision 3), for A and for B
   separately. (Needs a few more runs after the change; unit-tested before, proven now.)
6. No candidates (a database with no big scanned index) shows the plain sentence. Query Store off shows its
   sentence and the Setup card's fix.
7. The page at phone width, no horizontal scroll.

Not provable here, and said so when built: a client-sized batch (hours), and a real Task Scheduler or Agent run
(there is none in this phase).

## Follow-up, not in this phase: the same selection, scheduled

The intended next step, so that index work for the queries that need it runs without anyone ticking rows. It
must not be built as a copy of the logic in Go: it has to run **inside SQL Server**, so that it works when
kakapo is closed.

- **Where:** a step in `master.dbo.kakapo_Maintenance` (5p), which Express calls from Task Scheduler, and an
  Agent job step elsewhere that calls the same procedure. One implementation, in T-SQL.
- **The selection in T-SQL:** the same candidate rule (Query Store's scanned indexes, 1,000 pages or more, not
  stale statistics), with the plan search done in T-SQL (`sys.query_store_plan` XML with XQuery for
  `Index Scan` / `Clustered Index Scan`), and the measurement from `sys.dm_db_index_physical_stats`.
- **Open points, to settle in that phase's contract:**
  1. **The saving floor needs a source.** Candidates: Ola's 5% (`FragmentationLevel1`, used here for the
     estimate) and Microsoft's page-density advice. A scheduled run has no human to judge a borderline row, so
     the floor matters more than it does here.
  2. **Cap the work per run** (pages or minutes), so a night's window is respected and the log is not flooded;
     the 20 GB of decision 8 is not a source for that.
  3. **Do not repeat an index that did not gain.** If the last reorganise by kakapo (the change log's result,
     or the verdict) showed no gain, leave the index alone until a measured saving returns.
  4. **Off by default**, as 5b's index piece is, and offered only when the plan is installed.
  5. **MNT09's wording and the 5p procedure's index step** (today Ola's `IndexOptimize` with Brent Ozar's
     50% / 80%) are reconciled with this selection: one index rule, not two.
