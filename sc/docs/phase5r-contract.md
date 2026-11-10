# Phase 5r contract: over-indexed tables (IDX02) and covered indexes (IDX08)

Draft 2026-11-10, for agreement before building. Contract only: nothing built.

On a test copy, one table carried 21 GB of nonclustered indexes for 5.4 GB of data. kakapo showed it only as
context, in the Storage card's IDX01 "Where the space goes": rank 5, no Do this, no button. Nothing said "this
is too much, look at it".

This phase makes kakapo flag it. The Design card gains two checks:

1. **IDX02, over-indexed tables.** One finding per table, with what its indexes cost on this database: the
   disk, every full backup and every CHECKDB that reads their pages, and the writes they take. It ends in
   **Investigate** (each index with its keys, includes, size, reads and writes, and on request the queries
   that use it) and Copy. It has no Apply of its own.
2. **IDX08, covered indexes.** A plain index whose keys are the start of a wider index that holds all its
   columns, and that is rarely read. It ends in 5o's `disable-index` fix. Never a drop.

Where IDX02's table also has IDX04 copies or IDX08 covered indexes, its evidence marks them, so the reader goes
from "too much" to the indexes kakapo can prove redundant.

**Not 5q.** `docs/phase5q-contract.md` is about how indexes are maintained (fragmentation, reorganise or
rebuild). This phase is about whether they should exist. Neither reads the other's facts. 5p (the maintenance
procedure) is untouched.

**Where it sits in design review** (`docs/phase5n-contract.md`). 5n was D1 (IDX14), 5o was D2 (IDX04 and the
`disable-index` fix). This is **D2b**, before D3 (unused indexes, IDX03), which is not built. D3 will add its
mark and its Apply to the same per-index rows this phase builds.

SCOPE rules 1, 2 and 4; section 3 item 10 (every finding ends in Apply, Investigate or Copy).

## Decisions to confirm

1. **The card: Design, not Storage.** Index-level rules live on the Design card (5n's owner rule, the design
   review proposal). Storage keeps IDX01 as context; its line for a table that has IDX02 adds "over-indexed:
   see the Design card". The triage's own IDX01 says so: "Tables where indexes are much bigger than the data
   are flagged in IDX02."
   - Alternative: IDX02 on the Storage card, next to IDX01. It splits index rules over two cards.
2. **IDX02's count test: more than 10 enabled nonclustered indexes, as the triage has it.** Three numbers exist
   on disk:
   - The triage: `IndexCount > 10`, enabled and not hypothetical (PerformanceTriage.sql 522).
   - sp_BlitzIndex 20: `COUNT(*) >= 10`, with the comment "10 is an arbitrary number" (4767, 4794).
   - kakapo's create-index guard: `count >= 10`, counting disabled indexes too (`index_fix.go` 580-592).

   Recommended: the triage's, for the finding and for the guard, through one function, so the card and the
   create-index preview never disagree. The size test is unchanged (below). On the test copy the biggest
   table has exactly 10 enabled indexes: it is flagged by size either way.
3. **IDX02's rank: the triage's 2, 3 or 4, taken as it is.** Rank 2 (High) puts a table near the top of the
   database page with no Apply. Recommended: keep it. 21 GB of indexes on 5.4 GB of data is a high cost
   whether or not kakapo can fix it, and the Investigate is the next step.
   - Alternative: cap it at 3 while the finding has no Apply.
4. **Near-copies (IDX09) are a mark in IDX02's evidence, not a finding of their own.** The test is the
   triage's (636-649, below). Narrowing an index's includes needs the queries that use it, which is what
   IDX02's Investigate shows.
   - Alternative: IDX09 as its own finding now, Copy only.
5. **IDX08 while the counters are 7 days old or less: listed, never applied.** The busy guard reads reads and
   writes. Straight after a restart, restore or creation they are near 0, so every narrow index would look
   unused. Recommended: 5n's rule. An IDX08 index goes in a "too early to tell" fold with no Apply until the
   window is over 7 days; with a warning under 14 (sp_IndexCleanup 2017-2023, 4054-4061).
6. **A per-index window after a rebuild.** On some versions an index rebuild clears that index's row in
   `sys.dm_db_index_usage_stats`. A nightly rebuild could then make a busy index look unread. Recommended: an
   index whose statistics date (`STATS_DATE`) is later than the database's window start counts its reads from
   that date for IDX08. That is conservative: an `UPDATE STATISTICS` moves it too. Whether SQL Server 2022
   and 2025 clear the row on a rebuild is checked live when building (Done when).
7. **"Which queries use this index?" reads Query Store's plans, on request only.** It searches the plan text
   of queries run in the last 30 days for the index's name, `TOP (10)`, each linking to its query page. It is
   a `LIKE` over plan XML, so it runs only when the user asks, for one index at a time.
   - Alternative: no plan search. Investigate then shows only sizes, reads and writes.
8. **Backups and CHECKDB shown as a share, never as minutes saved.** kakapo shows the index pages' share of
   the database's used pages, and beside it the last full backup's size and the last CHECKDB job's duration
   when it has them. It does not divide the duration by the share: a backup's or CHECKDB's time is not
   proportional to pages, and there is no source for a split.

## Sources

- `research/granite-sql/SqlPerformanceTriage/PerformanceTriage.sql`:
  - **IDX02 (499-522).** A table is over-indexed when
    `IndexMB >= 100 AND (IndexMB / DataMB >= 2 OR DataMB = 0)`, or when it has more than 10 enabled,
    non-hypothetical indexes other than the heap or clustered one and `DataMB + IndexMB >= 10`. IndexMB
    counts every index id above 1; DataMB is the heap or clustered index, from
    `sys.dm_db_partition_stats.used_page_count`. Rank 2 when `IndexMB >= 1024 AND ratio >= 3` or
    `IndexMB >= 500 AND ratio >= 5`; 3 when `IndexMB >= 100 AND ratio >= 2`; else 4. Its Why: every index is
    updated on every insert, update and delete, and indexes "take disk, memory and backup time". Its Do this:
    add no more indexes; use IDX03, IDX04, IDX08 and IDX09 to choose; "disable rather than drop so the change
    can be undone". Its evidence lists every index by size with reads and writes.
  - **IDX08 (459-474 `#Wider`, 618-634).** Prefix test:
    `LEFT(b.KeyCols, LEN(a.KeyCols) + 2) = a.KeyCols + ', '`. Key lists carry ` DESC`, so direction counts.
    Further conditions: same filter; `b` enabled, not hypothetical, type 1 or 2; `b` clustered or holding
    every column of `a`; the smallest such `b`. The target `a` is a nonclustered index that is not unique,
    not a primary key, not disabled and not hypothetical. **Busy guard**: left out when
    `Reads >= 100 AND Reads * 10 >= Writes`. Also left out when IDX04 already marks it safe. Rank 4. Do this:
    "If IDX03 or the reads below confirm it is rarely used over a full business cycle", disable it.
  - **IDX09 (636-649).** A nonclustered index of 50 MB or more with 5 or more included columns, on a table of 8
    or more columns, holding half the table's columns or more, or 60% of the data's size.
- `research/granite-sql/Checks.csv`:
  - 58 (IDX02): owner Us; then check "index MB in IDX01 lower and no new IDX03 entries".
  - 64 (IDX08): owner Us; then check "IDX08 gone and no query in ACT03 got slower".
  - 65 (IDX09): owner Us.
- **sp_BlitzIndex.** Check 20, "many NC indexes on a table" (4767-4794). Check 2, approximate duplicate keys
  (4583-4620): the triage's IDX08 is the stricter form.
- **sp_IndexCleanup.**
  - Rule 3, key subset (4246-4376): the same prefix test. It refuses a unique narrow index: "a unique index on
    (A) enforces A is unique; a wider (A,B) only enforces (A,B) unique".
  - The usage window (1990-2023) and the 14-day warning (4054-4061).
  - Writes per day (`daily_write_ops_saved`, README; `docs/darlingdata-review.md` 76).
  - Its rules 4 and 6 (merge includes) stay out (`docs/darlingdata-review.md` 77-78).
- **Microsoft Learn**, to be re-read when building, each sentence the app shows citing its page:
  - "SQL Server and Azure SQL index architecture and design guide": many indexes on a table slow down
    INSERT, UPDATE, DELETE and MERGE, because every index must change with the data.
  - "DBCC CHECKDB", the `NOINDEX` argument: skipping the intensive checks of nonclustered indexes on user
    tables shortens the run. That is Microsoft saying those checks are part of every full CHECKDB.
  - "Full database backups": a full backup holds the whole database. Every page in use is read and written,
    index pages included.
  - "sys.dm_db_index_usage_stats": `user_updates` counts write operations, not rows. The counters start
    empty when SQL Server starts, and a database's rows go when it is detached or closed (AUTO_CLOSE).
  - "Disable indexes and constraints": as 5o.
- `docs/phase5o-contract.md`: the `disable-index` fix, its refusals, its hold on a name found in code, plan
  guides or Query Store, and its foreign-key guard.

**The test copy that prompted this** (anonymised as `WarehouseLive`, SQL Server 2025 Express, counts only,
not read again for this phase):
- 33 GB used: 8.1 GB of table data and 24.8 GB of nonclustered indexes.
- `dbo.StockMove`: 5.4 GB of data and 10 enabled nonclustered indexes using 21 GB (3.9 times), plus 2
  disabled. IDX02 rank 2 by size.
- One of those indexes, call it `IX_StockMove_Item_Date`, is keyed on two columns and includes 32 of the
  table's 34 columns (5 GB). Almost a second copy of the table: an IDX09 mark.
- Another, `IX_StockMove_Item`, is keyed on the first of those columns alone with 9 includes (1.6 GB). If
  the wider index holds those 9 columns, it is an IDX08 candidate.
- `dbo.StockBalance`: 0.8 GB of data and 2.7 GB of indexes (3.4 times). IDX02 rank 2.
- Restored the same day, so the window is too short. IDX02 shows its sizes and "too early to tell" for the
  writes. IDX08 would sit in the fold with no Apply.

## 1. The read

No new query. The Design card already reads every index of every user table (`collect.AllIndexFacts`, 5o):
type, unique, primary key, disabled, hypothetical, filter, keys with direction, includes, MB, reads and
writes, and the foreign keys that name an index. It also reads the window (`UsageWindowFacts`, 5n).

Added to the same batch, each in the TRY of its area:
- per table, its **column count** (`COUNT(*)` from `sys.columns`). The all-tables path reads no columns
  today; IDX09's mark needs the count only.
- per index, its **statistics date** (`STATS_DATE(object_id, index_id)`), for decision 6.
- for the database, the **used pages of the data files**, as `backupSizesQuery` reads them (`room.go`).
- the **last full backup's** size and compressed size (the same msdb read).
- the **last CHECKDB job's** duration when kakapo knows the job (the Upkeep card's job read).

The plan search for Investigate (section 4) is a separate on-request read.

## 2. IDX02, over-indexed tables (`domain/design.go`, pure)

Catalogue entry: `IDX02`, card `design`, "Over-indexed tables", question "Which tables carry more index than
data?", owner Us. Then check: "Re-run after disabling: IDX02's index MB for that table lower; no query on the
table slower in the tuning report."

The sentences below use the test copy's sizes; the reads, writes, dates, backup size and CHECKDB time
are illustrations (the copy's window was too short to have any).

One finding per table that meets the triage's test (decision 2). Rank as the triage (decision 3). Ordered by
index MB, largest first.

- **Object**: `[schema].[table]`.
- **Finding**: "StockMove has 10 indexes using 21 GB for 5.4 GB of data: 3.9 times the table."
- **Cost**, three lines, each left out when its fact was not read:
  - Disk and copies: "Every full backup and every CHECKDB reads these 21 GB: 64% of the database's 33 GB in
    use. The last full backup was 9.8 GB compressed; the last CHECKDB took 41 minutes." (Decision 8: a
    share, not minutes saved.)
  - Each change: "Every row added to StockMove is written to the table and to each of its 10 indexes."
    Filtered indexes are counted with "when the row matches the filter".
  - Measured writes: "4.1 million writes to its indexes since 27 Oct, about 290,000 a day." Writes per day as
    sp_IndexCleanup has them. While the window is 7 days or less: "Writes counted since the restore on 10
    Nov: too early to tell."
- **Why**: "SQL Server keeps every index up to date on each insert, update and delete, and stores, backs up
  and checks each one. A table with more index than data pays that on every change."
- **Do this**: "Add no more indexes to StockMove. Disable the ones kakapo shows as copies or covered (Apply on
  their rows). For the rest, open Investigate to see which queries use each index. An index no query used
  over a full business cycle, month-end included, is a candidate to disable. Tell the vendor before you do."
  A table with no copy and no covered index leaves out the first Apply sentence.
- **Evidence** (Copy, and the Investigate table of section 4): one line per index, largest first: name, keys,
  included columns (count, names folded), MB, reads, writes, and its marks.
- **Action**: Investigate; Copy. No Apply on the finding itself.

**Marks**, each from a rule already on disk, at most two per index, in this order:
- "clustered" or "heap"; "primary key", "unique", "a foreign key uses it". These are never offered for disable.
- "copy of IX_A (IDX04)", with 5o's Apply.
- "covered by IX_B (IDX08)", with this phase's Apply (section 3); "covered, too early to tell" while the
  window is short.
- "near-copy of the table: 34 of 34 columns (IDX09)".
- "disabled: no space, no writes" (sp_BlitzIndex 42 lists these too).

There is no "unused" mark until D3.

**The card.** IDX02 joins IDX14 and IDX04.
- Verdict clause, after IDX04's: "{n} tables carry more index than data: StockMove has 21 GB of indexes for 5.4
  GB."
- New fact **Index space**: "24.8 GB of 33 GB in use (75%)".
- New fact **Over-indexed tables**: "2, 23.7 GB", or "none".
- With no finding, the checks list shows IDX02 passed.

## 3. IDX08, covered indexes (`domain/design.go`, pure)

Catalogue entry: `IDX08`, card `design`, "Covered indexes", question "Which indexes does a wider one already
cover?", owner Us. Then check: Checks.csv 64, worded "Re-run: IDX08 gone for that index; no query on the table
slower in the tuning report."

The triage's rule as it is (Sources), and two additions:
- **The window.** Over 7 days, the busy guard decides. 7 days or less, the index goes in the fold "Covered
  indexes, too early to tell", with no Apply (decision 5). Its reads count from its statistics date when that
  is later than the window start (decision 6).
- **Unique narrow indexes are never targets**, as in the triage and sp_IndexCleanup rule 3. A foreign key can
  only name a unique index, so the foreign-key guard is never reached; the fix keeps it anyway (5o).

One finding per covered index. Rank 4. Ordered by MB.
- **Finding**: "IX_StockMove_Item (ItemId) is read 40 times since 1 Oct and written 2.1 million times.
  IX_StockMove_Item_Date starts with the same column and holds all of its columns."
- **Why**: "The wider index can do the same lookups. The narrow one mostly adds writes and space."
- **Do this**: "Disable IX_StockMove_Item. Queries that used it can use IX_StockMove_Item_Date. Tell the
  vendor, so their next upgrade does not create it again."
- **Evidence**: both indexes' keys, includes, MB, reads and writes, the window, and the busy guard's numbers
  ("read 40 times, under 100").
- **Action**: Apply `disable-index`; Copy.

**The fix.** 5o's `disable-index`, with IDX08 as a second check. The option `keeper` names the wider index.
The preview re-reads the table, then:
- **Refused**, besides 5o's refusals: the wider index no longer starts with the index's keys in the same
  order and direction, has another filter, is disabled, or no longer holds every column.
- **Held**, besides 5o's hold on a name found in code: the window is 7 days or less ("Reads counted since the
  restore on 10 Nov: too early to know whether IX_StockMove_Item is needed"); or the fresh reads now pass the
  busy guard ("now read 2,300 times since 1 Oct: kept"). Copy script still works.
- **Warnings**, besides 5o's:
  - When the wider index is itself an IDX09 near-copy: "IX_StockMove_Item_Date holds most of the table. If it
    is ever narrowed, keep ItemId first and these 9 columns in it, or IX_StockMove_Item is needed again."
  - "A query that scans IX_StockMove_Item today will scan IX_StockMove_Item_Date instead: 5 GB instead of
    1.6 GB. Seeks on ItemId cost about the same in either."
- **Change row**: "Disabled index IX_StockMove_Item on dbo.StockMove (covered by IX_StockMove_Item_Date),
  1.6 GB, in WarehouseLive, for IDX08". Read back as 5o.

The card treats an index both IDX04-safe and covered as IDX04's only (the triage's rule). The create-index
guard's IDX08 sentence (`index_fix.go` 568-576) names the Design card when the table has an IDX08 finding, as 5o
did for IDX04.

## 4. Investigate: the table's indexes

From an IDX02 finding, Investigate opens a fold on the Design card's page. No new route.

- **The answer first**: the finding's sentence and the three cost lines.
- **One row per index**, largest first: name, keys, includes (count, the names on a click), MB, share of the
  table's index MB, reads and writes since the window's start, marks, and the row's action (Apply for IDX04 or
  IDX08, nothing otherwise).
- **"Which queries use it?"** on each nonclustered index (decision 7). It searches the plans of queries Query
  Store ran in the last 30 days for the index's name, using 5o's search for a name as a word. `TOP (10)`, most
  executions first; each query links to its query page. Leaves out kakapo's own statements.
  - When Query Store is off or unreadable: "Query Store is off: kakapo cannot tell which queries use it." The
    reads number stands alone.
  - No query found: "No query in Query Store's last 30 days used IX_…. Queries not in Query Store (it keeps
    only what its capture mode keeps) and code outside SQL Server cannot be searched." That sentence ends in
    nothing applied: unused-index Apply is D3's.

Every step ends in Apply (the marked rows), Copy (the finding and the table), or another Investigate (a query
page).

## 5. Joining up

- Storage's IDX01: a top-10 table with an IDX02 finding gets "over-indexed: see the Design card" on its line.
- The create-index guard's IDX02 sentence uses IDX02's own test (decision 2), and names the Design card when
  the table has the finding.
- Performance's QRY01 Do this and the plan's missing-index words already say "check IDX02 first"
  (`performance.go` 426, 1417). They now point at a check that exists.
- `docs/phase5n-contract.md`'s roadmap gets a D2b row; `docs/landing-page.md`'s Design row; SCOPE.md's phases
  row and decisions log.

## 6. Not in this phase

- Unused indexes (IDX03, D3).
- Merging includes into the wider index (sp_IndexCleanup rules 4 and 6), and narrowing a near-copy as a fix.
- Dropping instead of disabling.
- Memory used by index pages (`sys.dm_os_buffer_descriptors`): too costly to read on a large server.
- Fragmentation and maintenance (5q).
- XML, spatial and columnstore indexes as IDX08 targets; indexed views.
- Telling the vendor's indexes from the client's own (D5).

## Tests

**Domain, IDX02:**
- The size test at its edges: 99 and 100 MB, ratio 1.9 and 2, DataMB 0.
- The count test: 10 and 11 enabled indexes; disabled and hypothetical ones not counted; the 10 MB floor.
- Rank 2 by either branch (1,024 MB at 3x; 500 MB at 5x), rank 3, rank 4.
- The cost lines, each left out when its fact is missing; writes per day; the too-early sentence.
- The marks and their order; the near-copy test at 50 MB, 5 includes, 8 table columns, half the columns, 60%
  of the data.
- Disabled indexes shown with 0 MB.
- The card's facts and verdict clause; the create-index guard using the same test.

**Domain, IDX08:**
- The prefix test: `(A)` under `(A, B)` yes; `(A)` under `(B, A)` no; `(A)` under `(A DESC, B)` no;
  `(A, B)` under `(A, B)` no (that is IDX04); `(AB)` under `(A, B)` no (the `', '`).
- A different filter: no. A wider index missing one included column: no; the clustered index as the wider
  one: yes.
- Unique, primary key, disabled or hypothetical targets: no.
- The busy guard at 99 and 100 reads, and at reads × 10 equal to writes.
- An index IDX04-safe is not repeated.
- The window: 7 days in the fold, 8 days with Apply, under 14 with the warning; the statistics date later
  than the window start moving the index's own window.

**The fix:**
- Each new refusal (prefix gone, filter changed, wider disabled or narrower) and each new hold (window,
  busy now).
- The near-copy warning.
- The change description with IDX08.

**Collect:** the column count and statistics date on the all-tables path; each area in its TRY; the plan
search tagged, read only, `TOP (10)`, kakapo's own statements left out; the plan search when Query Store is
off. API and web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in a throwaway database created for the
test and dropped afterwards (never a client copy), with made-up names (`WarehouseLive_5r`, `dbo.StockMove`):

- `dbo.StockMove` with a clustered key, about 30 columns, enough rows for 200 MB of data, and 11 nonclustered
  indexes: `IX_StockMove_Item_Date (ItemId, MoveDate)` including most columns; `IX_StockMove_Item (ItemId)`
  with includes it covers; a plain index whose wider index lacks one of its includes; a unique narrow
  index; one disabled index. The card shows IDX02 at the triage's rank with the three cost lines (the backup
  line after a `BACKUP DATABASE … TO DISK` to a file deleted afterwards, the CHECKDB line after a
  `DBCC CHECKDB`). The evidence marks the near-copy, the covered index, the unique one and the disabled one.
- IDX08 lists `IX_StockMove_Item` in "too early to tell" (the database was created today): no Apply. Through
  the API, the fix is held with the window sentence, and Copy script works.
- The window over 7 days is covered by domain tests only. kakapo has no clock override, and none is added
  for this test.
- "Which queries use it?": with Query Store on, a query that seeks on `ItemId` is found for the index its
  plan uses and links to its query page. With Query Store off, the sentence says so.
- A table with 11 tiny indexes and under 10 MB in all: no IDX02.
- `ALTER INDEX IX_StockMove_Item_Date ON dbo.StockMove REBUILD`: whether its row in
  `sys.dm_db_index_usage_stats` is cleared on SQL Server 2022 is recorded in this contract's facts. The same
  check on SQL Server 2025 Express if one is at hand; decision 6 is kept or dropped on that result.
- Storage's IDX01 line for StockMove says "over-indexed: see the Design card".
- Phone width: the card, the IDX02 finding, the Investigate table and the IDX08 preview fit without horizontal
  scroll.
