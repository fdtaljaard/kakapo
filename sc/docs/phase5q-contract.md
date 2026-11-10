# Phase 5q contract: index work only where a query shows the need

Agreed with Francois on 2026-11-10: all four decisions as recommended. Built in part the same day (see Built).

A review of a home-grown index procedure found that kakapo already gives the right advice on index
maintenance, but cannot see the thing that most often goes wrong. Its advice: statistics every night; index
work off by default; reorganise in preference to rebuild. What goes wrong is a scheduled procedure that
rebuilds every index above 5% fragmentation, offline, and refreshes few statistics. The procedure reviewed
was a common one, found on many sites. It also failed part-way in two ways (see Facts).

This phase closes four gaps:

1. kakapo sees a home-grown index job (MNT09).
2. MNT03 asks for statistics, not "any maintenance".
3. kakapo's own index job never rebuilds offline.
4. The query page offers index work for one index, when the query's plan reads all of that index, and
   measures the effect.

Section 4 follows Microsoft's strategy step by step:

- "It's often sufficient to only reorganize or rebuild specific indexes used by queries with degraded
  performance."
- "Measure the specific impact … Query Store is a good way to measure the 'before maintenance' and 'after
  maintenance' performance."

SCOPE rules 1, 2, 4 and 5.

## Decisions (agreed 2026-11-10)

1. **The fragmentation thresholds: Brent Ozar's 50% / 80%.** The 30% / 50% that 5b calls "Ola Hallengren's
   thresholds" are not his. Ola's defaults are `FragmentationLevel1 = 5`, `FragmentationLevel2 = 30`,
   `MinNumberOfPages = 1000`. 30/50 has no source, and rule 2 does not allow a number without one.
   - Agreed: Brent's 50/80 ("I raised the fragmentation thresholds by a lot"). They are conservative,
     which fits "rarely".
   - Not taken: Ola's 5/30, which would make the index work run on most nights it is on.
   - The page floor stays at Ola's 1,000 pages.
2. **Reorganise only, unless the edition can rebuild online.** The high level becomes
   `INDEX_REBUILD_ONLINE,INDEX_REORGANIZE` on every edition. Ola tries the listed operations in order, so
   Enterprise rebuilds online and everyone else reorganises. Nothing in kakapo rebuilds offline unattended.
   - Brent drops offline rebuilds for the same reason. His high level is `INDEX_REBUILD_ONLINE` alone, which
     on Standard would skip the worst indexes, so the reorganise fallback is kakapo's addition.
3. **The query page splits Before and Now at kakapo's own change.** Today, Since is where the plan in use
   began (5j). A reorganise or a statistics update does not always start a new plan, so its effect would
   fall inside Now. Agreed: when kakapo's change log has a change on a table the plans read, and it is
   later than the plan's start, Since is that change. The verdict then says "Since you reorganised IX_… on
   10 Nov 21:04". Changes made outside kakapo do not move Since; they stay in What changed.
4. **When the query page offers a reorganise (section 4).** Agreed: when the plan scans the index and
   the index has 1,000 pages or more.
   - The page shows the measured facts and an estimate of the pages saved. It does not judge them against a
     new number of kakapo's own.
   - It says "nothing to gain" only below Ola's low level: fragmentation under 5% and the estimate under 5%.
   - Then the before/after verdict decides. That is Microsoft's own test.

## Facts verified on `localhost\SQL2022` on 2026-11-10 (scratch databases, created and dropped)

- **A home-grown procedure in the common shape.** It reads `dm_db_index_physical_stats`, reorganises
  5–30% and rebuilds over 30%. It names the table without its schema and puts each ALTER in a transaction
  checked with `@@ERROR`. Run as written:
  - A table outside `dbo` fails with error 1088. The error ends the procedure: the `@@ERROR` check never
    runs, and every index after it is skipped without a message.
  - Fragmentation is fetched into `decimal(4,2)`. A value of 100 fails with error 8115 (arithmetic
    overflow).
  - `REORGANIZE` inside `BEGIN TRAN` held 33 X/IX locks and 15.8 MB of log until the transaction ended,
    and ROLLBACK did not undo it: fragmentation went from 99% to 0.2%.
  - For a heap, the NULL index name makes the statement NULL. `sp_executesql NULL` does nothing, with no
    error.
- **The rebuild fingerprint.** After `ALTER INDEX … REBUILD`, each index's statistics had
  `rows_sampled = rows` (200,000 of 200,000).
  - After `sp_updatestats`, the same 22,000-page indexes had about 10% sampled (20,844 of 200,000).
  - A 336-page table was read in full by `sp_updatestats` too. So a full-scan update is a rebuild's
    fingerprint only on indexes too big for the default sample to read whole.
  - Measured while building: `sp_updatestats` read every row of a clustered index of 804, 1,005 and 1,105
    pages, and sampled from 1,507 pages (13,090 of 15,000 rows). The fingerprint's floor is 2,000 pages.
- **REORGANIZE packs the leaf to the fill factor.** A 1,214-page index at 99% fragmentation became 830
  pages at 0.2%.

## Sources (each sentence the app shows cites its page; to be re-read when building)

- **Microsoft Learn, "Maintain indexes optimally to improve performance and reduce resource utilization"**
  (updated 2026-10-27):
  - the maintenance strategy quoted above;
  - "Reorganizing an index is less resource intensive… it should be your preferred index maintenance
    method";
  - "Reorganize is always an online operation";
  - an offline rebuild "holds object-level locks for the duration";
  - a rebuild updates the index's statistics as with FULLSCAN, and "Statistics aren't updated when an
    index is reorganized";
  - reorganising "compacts index pages to make page density equal to the fill factor";
  - "In many workloads, increasing page density results in a greater positive performance impact than
    reducing fragmentation";
  - an interrupted reorganise keeps its progress;
  - REORGANIZE needs free space in the index's own data file (error 1105), and "An index can't be
    reorganized when ALLOW_PAGE_LOCKS is set to OFF";
  - its example measures with `SAMPLED` "to return actionable results quickly".
- **Microsoft Learn, "Editions and supported features of SQL Server 2022"**: "Online index create and
  rebuild" is Enterprise only (Standard, Web and Express: No).
- **Ola Hallengren, "SQL Server Index and Statistics Maintenance"**: the defaults above. "If none of the
  specified operations are supported for an index, then that index is not maintained."
- **Brent Ozar, "How to Configure Ola Hallengren's IndexOptimize Maintenance Script"** (2014):
  - 50/80, and offline rebuilds removed: "I would rather not take tables offline when there's not a human
    being around to watch";
  - index work inflates log backups, differentials and replicas.

## 1. MNT09: a home-grown index job (Upkeep, owner Us)

**The read.** It is added to the Upkeep read, inside the database, in its own TRY.

- **Procedures.** The procedures of this database whose definition (`sys.sql_modules`) contains
  `ALTER INDEX`, `DBREINDEX` or `INDEXDEFRAG`, and also `dm_db_index_physical_stats` or `showcontig`. That
  is, procedures that decide for themselves what to maintain. kakapo's own (`kakapo_%`) and Ola's
  `IndexOptimize` are left out. For each: schema, name, and whether it rebuilds (`REBUILD` or `DBREINDEX`
  in the text).
- **Jobs.** In the msdb read, outside Express: the enabled jobs whose step command names one of those
  procedures.
- **The fingerprint.** The index statistics updated in the last 8 days with `rows_sampled = rows`, on
  indexes above the measured floor (see Facts), grouped by the hour they were updated: the hour with the
  most, and how many. This is the read 5j already does for a query's tables, done for the whole database.

**The rule.** MNT09 fires when such a procedure exists and either:

- an enabled job names it; or
- there is no Agent (Express), or no job names it, and the fingerprint shows three or more such indexes in
  one hour, and no kakapo or Ola index piece of the plan is in place. "Three in one hour" is kakapo's
  detection rule, said as such. It is not advice.

Rank 3 when the procedure's rebuilds are offline: its code has no `ONLINE = ON` (a plain `REBUILD` is
offline on every edition), or the edition is not Enterprise-class (EngineEdition 3 is Enterprise,
Developer and Evaluation). Rank 4 otherwise. (As built: the contract first ranked by edition alone.)

**Finding.** "`dbo.RebuildIndexes` rebuilds and reorganises indexes on a schedule (job "Nightly
indexes"; last seen at work 10 Nov 02:00, 41 indexes)."

- **Why:** "A rebuild is offline unless it asks for ONLINE = ON, and on Standard and Express it always
  is: each table is locked while its indexes are rebuilt. Rebuilding or reorganising every index that crosses a small threshold costs log, I/O and time,
  and Microsoft advises against it on a schedule. The speed-up people see afterwards usually comes from the
  statistics a rebuild refreshes, which a statistics update gives for much less."
- **Do this:** "Stop it (the job or the scheduled task that calls it), and use the maintenance plan's
  statistics job instead. On Express, use the maintenance procedure. Leave index work off unless a query
  shows the need: its page then offers to reorganise the index it reads."
- **Then check:** "after a week without it, MNT09 is gone and MNT04 stays quiet."
- **Detail:** the procedure's name, the job and its schedule, and the fingerprint's hour and count. When
  the hour falls outside the plan's night (5b's default times, 22:00 to 05:00), it says so: an offline rebuild
  then locks tables while people work.

No fix in this phase. Stopping another tool's job is the owner's decision, and a scheduled task is outside
SQL Server. The hand-over gives the steps.

## 2. MNT03 asks for statistics

Today MNT03 is satisfied by any enabled job whose name or command looks like maintenance, including an
index-only job (and a job that EXECs a procedure is not even seen). From this phase it asks for a
statistics job. As built, the Upkeep read finds one itself, by the same commands the 5b plan's
classification uses: a step with `sp_updatestats`, `UPDATE STATISTICS`, or `IndexOptimize` with
`@UpdateStatistics`, or "statistic" in the job's name. A maintenance plan (SSIS) still counts, as today.
MNT03 fires when there is no such job, whatever maintains the indexes. Titles:

- with nothing at all: "No enabled index or statistics maintenance job was found." (as today);
- with an index piece covered, or MNT09's procedure: "Indexes are maintained by "{job}", but nothing
  updates statistics, which is what keeps query plans good."

The fix stays `maintenance-job` (statistics only by default). On Express, MNT03 should fire too, from the
5p procedure's settings, with `install-maintenance-proc` as its fix. **Not built yet:** it reads 5p's
tables, so it waits until 5p is committed.

## 3. kakapo's own index job: decisions 1 and 2

- `olaIndexCommand` (`upkeep_actions.go`) and the 5p procedure's index step (`maintproc_sql.go`) change to:
  `@FragmentationMedium = 'INDEX_REORGANIZE', @FragmentationHigh = 'INDEX_REBUILD_ONLINE,INDEX_REORGANIZE'`,
  with the thresholds of decision 1.
- Every text that says "Ola Hallengren's thresholds (reorganise from 30%…)" is corrected to name its
  sources:
  - `maintenance.go` (the plan's source line);
  - `maintenance_fix.go`;
  - `maintproc.go`;
  - `upkeep_actions.go`;
  - the web mocks;
  - `docs/phase5b-contract.md` line 17;
  - `docs/phase5p-contract.md`.
- Existing kakapo index jobs keep their old command until they are re-applied. The plan row reads an
  `INDEX_REBUILD_OFFLINE` in a kakapo job as "rebuilds offline: re-apply to reorganise instead".

**As built (2026-11-10):** `olaIndexCommand` and its texts in `upkeep_actions.go` only. The 5p procedure's
index step, the other texts (`maintenance.go`, `maintenance_fix.go`, `maintproc.go`, `maintproc_sql.go`, the
web mocks), the two contracts and the plan row's "rebuilds offline" are **not built yet**. They are in 5p's
uncommitted files, so they wait until 5p is committed.

## 4. The query page: the index the plan reads in full

**The plan.** `ParseShowplan` keeps a `PhysicalOp` stack beside its `LogicalOp` stack. Each `ShowplanObject`
gains `Scan`: true when the `RelOp` around it is `Clustered Index Scan` or `Index Scan`. `QueryTable` gains
`Scanned`, the indexes a plan reads in full, beside `Indexes` (as built). Heaps (`Table Scan`) stay out,
since an index operation cannot change them.

**The measurement, on demand.** For each scanned index, the table row shows **Measure**. Its size is
known only once measured, and an index under 1,000 pages gets a sentence saying it is too small to matter
(Ola's floor). It is a button, not part of the page load: SAMPLED reads part of every big index.

- A new read, `collect.IndexShape`, runs `sys.dm_db_index_physical_stats(DB_ID(), object, index, NULL,
  'SAMPLED')` for that one index. It keeps the leaf level and `IN_ROW_DATA`, plus `sys.indexes`
  (`fill_factor`, `allow_page_locks`, `type`).
- It returns pages, page density, fragmentation, and the estimate: pages after ≈ pages × density ÷ fill
  factor (100 when 0). Reorganise packs to the fill factor (Microsoft).
- It needs the permission the DMV needs (to be read when building). Without it, Measure says what is
  missing.

**The sentence.**

- "The plan reads all of `IX_Orders_Date` (12,400 pages, 97 MB). Its pages are 58% full and 41% out of
  order. Reorganising it would pack it into about 7,200 pages, so each scan would read about 40% fewer."
- Or, under Ola's low level: "Its pages are full and in order: index work would not help this query."
- When the table's statistics are stale (5j), the statistics action comes first: "Update its statistics
  first. It is cheaper, and often all a slow query needs."

**The fix `reorganize-index`** (database; from the query page).

- **Statement:** `ALTER INDEX [ix] ON [db].[schema].[table] REORGANIZE;` with no transaction around it
  (three-part, as the other index fixes write it: the connection is in master).
- **No "also update statistics" option** (as built, simpler): a warning says reorganise updates none, and
  points to the table's own Update statistics action on the same page.
- **Preview:** the measured facts and the estimate. Warnings:
  - online on every edition, and can be stopped, keeping its progress;
  - in FULL recovery, the next log backup carries roughly the index's size;
  - the data file needs free space;
  - the measure-again and before/after line;
  - the ALTER permission.
  (The 5l free-space check is not wired: a reorganise needs only working pages in the index's own file.)
- **Blocks when:**
  - `allow_page_locks` is off;
  - the index is disabled;
  - the index is a columnstore index (out of scope).
- **Read-back:** Measure again, after. **Change row:** as `create-index`'s, object the index.
- **Rollback:** none; the preview says there is nothing to undo.

**The effect.** The change row lands in kakapo's change log. With decision 3, the query's verdict compares
the runs before it with the runs after: pages per run first, since that is what a reorganise changes, then
time.

## Tests

- **Domain:**
  - MNT09's rule on fixtures: procedure plus job; procedure plus fingerprint on Express; procedure with an
    Ola index piece in place (silent); no procedure (silent); the rank by edition and REBUILD.
  - MNT03's two titles, and Express.
  - The new IndexOptimize commands in both places.
  - `ParseShowplan`'s `Scan` on plan fixtures: scan, seek, key lookup, heap.
  - The estimate and the "nothing to gain" rule.
  - `reorganize-index`'s statement, warnings and blocks.
  - The verdict's Since moving to a kakapo change, and not moving for a change outside kakapo.
- **Collect:** MNT09's reads and `IndexShape`, tagged and read-only.
- **Service:** `reorganize-index` registered.
- **Web:** the build.

## Built (2026-11-10)

Sections 1 and 4 and decision 3 are built. Sections 2 and 3 are built in part (see each). `go test ./...`
and the web build pass. `TestPhase5qLive` (`SQLMONITOR_TEST_HOST`, `SQLMONITOR_TEST_5Q=1`) passed on
`localhost\SQL2022` (Developer):
- Measure on a fragmented 4,055-page index: 66% full, 99% out of order, about 2,687 pages after.
- `reorganize-index` applied through the real fix. Read back: 4,055 pages to 2,736, 99% out of order to
  1%; one INDEX change row.
- The procedure run once: three indexes rebuilt in one hour. MNT09 fired from the fingerprint ("called
  from outside SQL Server Agent"), then from an enabled job naming it. MNT03 fired with its index-only title.

The browser check used the mock: Measure, the sentence, Reorganise index and its preview.

**Not proven live:** the verdict's split at kakapo's own change needs Query Store history around a change
(unit-tested only), and Express. "The job disabled: MNT09 silent" is unit-tested only.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022` with a throwaway database:

1. **MNT09.** The procedure from the Facts above, scheduled as an Agent job and run once: MNT09 fires,
   naming it, the job and the fingerprint. The job disabled and the run a week old (`last_updated` faked
   by restoring a copy, or by the fingerprint test alone): MNT09 is silent.
2. **MNT03.** With only an index job present, MNT03 fires with its new title.
3. **The query page.** A query that scans a fragmented 1,000+ page index:
   - its page shows the index as scanned;
   - Measure gives the facts and the estimate;
   - `reorganize-index` applies, and the read-back shows the pages packed;
   - after more runs, the verdict compares before and after the change.
4. **The fingerprint floor.** The page size at which the default sample stops reading every row is
   measured and written into this contract.
