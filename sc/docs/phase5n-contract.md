# Phase 5n contract: Design review starts, with foreign keys without an index (IDX14)

The Design card has been grey since phase 2. This phase gives it a purpose and its first check, end to end:
the foreign keys whose referencing table has no index for them, read for the whole database, ranked by what
they cost this database now, ending in the `create-index` fix that exists since 4c, or in Copy for the vendor.

Agreed with Francois on 2026-11-08: design review lives on the **Design card**, which comes back to life (this
replaces the 2026-11-03 decision that folded index work into Performance and Storage, and answers
`docs/architecture-review.md`'s "give it a real purpose or remove the tile"). Design review answers **"Does
the way this database is built cost you anything, and who can change it?"** It is not a style review: a design
rule ships only when kakapo can show what it costs on this database (rows read, writes spent, risk) and has a
source with a threshold (rule 2). Changing for a measurable benefit only, never because a design breaks
someone's preferred style.

## Design review in phases

**Who owns a design finding.** The consultant is the database's admin and often cannot change the vendor's
schema. Two kinds of change, one rule:

| Kind | Examples | Owner | Ends in |
|---|---|---|---|
| Invisible to the application, can be undone | add an index, disable a duplicate or unused index, re-check a constraint SQL Server no longer trusts | Us | Apply (with Copy for the vendor) |
| Changes a table's shape | data types, keys, a missing primary key, a clustered index on a heap, MAX columns, renames, splitting tables | Vendor | Copy for the vendor only; never Apply from design review |

An object whose owner is not known is the vendor's. Telling the vendor's objects from the client's own is a
later phase (D5); no source on disk has a rule for it (`is_ms_shipped = 0` is the only filter any of them use).

| Phase | What | Sources |
|---|---|---|
| **5n (D1)** | Foreign keys without an index, database-wide, ranked by deletes on the parent: this contract | Microsoft Learn; PerformanceTriage.sql IDX14; sp_BlitzIndex 72 |
| D2 | Exact duplicate indexes (IDX04) and Tuning Advisor leftovers (IDX05), with a new `disable-index` fix that refuses an index a foreign key depends on (`sys.foreign_keys.key_index_id`: disabling it disables the key) | triage IDX04/IDX05; sp_IndexCleanup rule 2 and its foreign-key guard (6210-6260); sp_BlitzIndex 1, 41 |
| D3 | Unused indexes (IDX03), counted from the latest of server start, last restore and database creation; held at 7 days or less, warned under 14; writes saved per day | sp_IndexCleanup rule 1 (4064-4085, 1990-2023, 4054-4061); `docs/darlingdata-review.md` |
| D4 | Foreign keys and check constraints SQL Server no longer trusts, with a `WITH CHECK CHECK CONSTRAINT` fix whose preview says it reads the whole table and fails if rows break the rule | sp_Blitz 48 and 56; Microsoft Learn |
| D5 | "Which objects are yours?" as a Database decision (the pattern of 5m), with the install batch as evidence; one Copy that gathers every vendor finding | kakapo |
| D6 | Code notes on views and procedures (`SELECT *`, `@@IDENTITY`, predicates that cannot seek, `LIKE` with a leading wildcard), only for code that already shows as costly on the query page; Vendor, Copy | Microsoft's SQL code analysis rules |

Not planned, because no source on disk gives a threshold: clustering on a GUID, MAX columns, nullable columns
that never hold NULL, oversized data types. They come back only with a Microsoft or practitioner source and a
cost kakapo can show.

## Sources

- Microsoft Learn, "Primary and foreign key constraints", section on indexes on foreign key constraints: an
  index on the foreign key columns is useful because changes to the referenced key are checked against the
  referencing table, and because the columns are often used in joins. Already cited by the query page
  (`domain/query.go` `qForeignKeyLines`). To be re-read when building: each sentence the card shows cites it.
- Microsoft Learn, "sys.dm_db_index_operational_stats": `leaf_delete_count` and `leaf_ghost_count` (a delete that marks the row as a ghost first, as every delete from an index does, counts only in `leaf_ghost_count`; found when building: 10 deletes from a clustered table left `leaf_delete_count` at 0 and `leaf_ghost_count` at 10, and it stayed 10 after ghost cleanup); its data "exists only as long as
  the metadata cache object that represents the heap or index is available" and is neither persistent nor
  transactionally consistent, so the counts are "at least" and the window is never older than the restart.
- `research/granite-sql/.../PerformanceTriage.sql` IDX14 (1556-1580): foreign keys with no index leading on the
  column, on referencing tables of 10,000 rows or more; rank 3 from 1,000,000 rows, else 4; owner Us
  (`Checks.csv` 70, "then check: IDX14 gone. Deletes on the parent table faster"). The triage reads single-column
  keys only; kakapo's query (below) already covers keys of several columns.
- sp_BlitzIndex check 72 (`sp_BlitzIndex.sql` 6386-6415): "Unindexed Foreign Keys", reported only when the
  referencing table has reads. kakapo uses a more direct cost: deletes on the referenced table, which is what
  the foreign key check costs (Microsoft's first reason). The join reason is the query page's: it already names
  the key from the plans.
- The ChatGPT review Francois captured (2026-11-08): "don't index every foreign key", "measurable benefit, not
  style", "reversible is not safe". Taken as framing; no rule or number comes from it.

**Facts verified on `localhost\SQL2022` on 2026-11-08** (read only, counts only), on a restored copy of an
application database (95 tables, restored three days earlier):
- The query page's foreign-key query, run for every referenced table instead of a list, finds 37 of the 39
  enabled foreign keys with no index; none has several columns; 20 have a referencing table of 10,000 rows or
  more (the largest 1.4 million); none cascades.
- `sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)` runs with kakapo's rights and returns
  `leaf_delete_count` per heap or clustered index; on this copy no referenced table had a delete since the
  restart, so the card would show no finding, and 20 keys under "would cost work if…" with "too early to tell".
- `msdb.dbo.restorehistory` holds the restore (the Backups card already reads `LastRestore`).

## 1. The Design card

`CardDefs`' Design entry becomes: title "Database design", question **"Does the way it is built cost you
anything?"**, note (shown only while grey) "Foreign keys without an index, and later duplicate and unused
indexes and constraints SQL Server no longer trusts." The card is read with the overview and on its card page,
the way Storage is (`service/overview.go`, `service/card.go` gains `case domain.CardDesign`), and stored with
`InsertCardRead` as every card. No new table.

**Verdict**, the first that applies:

| When | Status | Verdict (example) |
|---|---|---|
| The foreign keys could not be read | grey | "Could not read the foreign keys: {error}" |
| One or more IDX14 findings | amber | "3 foreign keys make deletes read whole tables, the biggest Orders.Lines (1.4 million rows)." |
| No finding, the window is 7 days or less, and keys without an index exist | green | "Too early to tell which foreign keys cost work: deletes counted for 3 days, since the restore on 5 Nov. 20 foreign keys on large tables have no index." |
| No finding, keys without an index exist | green | "No foreign key cost work since 1 Oct. 20 would if rows were deleted from their parent tables." |
| Every foreign key has an index | green | "Every foreign key has an index." |

**Facts**: Foreign keys ("39, 37 without an index"); Costing work now (the count of findings, tone bad when one
or more); Counted since (the window's start and what it is: "the restart on 1 Oct" or "the restore on 5 Nov");
Tables ("95").

The 7 days follow sp_IndexCleanup's rule for usage windows (`sp_IndexCleanup.sql` 2017-2023): an absence of
deletes in a shorter window is not evidence.

## 2. The read (`collect/design.go`)

`ReadDesignFacts(ctx, db, database)`: SELECTs only, tagged, the safe session, each area in its own TRY, run in
the database with `sp_executesql` as the query page's read:

| Area | Read |
|---|---|
| Foreign keys without an index | `unindexedForeignKeysQuery` as it is, with `@tables` NULL meaning every referenced table. One query for the query page and the card, never two copies: the table filter becomes `(@tables IS NULL OR n.object_id …)`. Adds per key: the referenced table's object id, `delete_referential_action_desc`, `update_referential_action_desc`. |
| Deletes on the referenced tables | `SUM(leaf_delete_count + leaf_ghost_count)` per object from `sys.dm_db_index_operational_stats(DB_ID(), NULL, NULL, NULL)`, `index_id IN (0, 1)`, for the referenced tables of the keys above only |
| Counts | enabled foreign keys, user tables |
| Window | `sqlserver_start_time`, the last restore into this database (the Backups read's `LastRestore`, not read twice when the overview has it) and the database's `create_date`; the window starts at the latest of the three (`sp_IndexCleanup.sql` 1990-2005) |

`domain.DesignFacts` holds them with an error per area. `UnindexedForeignKeyFact` gains `RefDeletes int64`
(-1 when not read), `OnDelete` and `OnUpdate` (`NO_ACTION`, `CASCADE`, `SET_NULL`, `SET_DEFAULT`).

## 3. IDX14 (`domain/design.go`, pure)

Catalogue entry: `IDX14`, card `design`, "Foreign keys without an index", question "Which foreign keys make
deletes read a whole table?", owner Us, then check "Re-run: IDX14 gone for that key; deletes on the parent
table faster (the tuning report, when the deletes are in Query Store)."

**Fires** for an enabled foreign key with no index leading on its columns (any order), a referencing table of
10,000 rows or more, and one or more rows deleted from the referenced table in the window. Rank 3 when the
referencing table has 1,000,000 rows or more, else 4 (triage IDX14). Findings are ordered by referencing rows
times deletes, largest first.

- **Object**: `[schema].[child]` (the key's name in the finding).
- **Finding**: "Deleting from Orders (812 rows since 1 Oct) makes SQL Server read the whole of Lines
  (1,376,845 rows) to check foreign key FK_Lines_Orders: no index on Lines starts with OrderId." With ON DELETE
  CASCADE: "…to find and delete the matching rows (ON DELETE CASCADE)…".
- **Why**: "Every delete from the parent table, and every change to its key, checks this foreign key. Without
  an index the check reads the whole referencing table and holds its locks while it does (Microsoft Learn,
  'Primary and foreign key constraints')."
- **Do this**: "Create an index on Lines (OrderId). It does not change how the application works; tell the
  vendor, so their next upgrade keeps it or adds its own."
- **Evidence**: the key's columns, the referencing rows, the deletes and the window, the referential actions;
  "Deletes are counted since {window} and may be more: SQL Server forgets them when it unloads the table's
  metadata."
- **Action**: Apply `create-index` (section 4), and Copy (4a's hand-over block, addressed to the vendor).

**Would cost work if rows were deleted** (no finding): keys without an index on referencing tables of 10,000
rows or more and no delete seen. Listed on the card page under a fold with that heading, largest referencing
table first, each one line ("Lines (1,376,845 rows) → Orders, on OrderId"), no button: an index there costs
writes for a benefit not yet seen. When the window is 7 days or less the fold's heading says "Too early to
tell". Keys on smaller tables are counted in the facts and not listed.

`CardDetail` gains `foreignKeys` (the fold's rows, the window), left out for every other card, as `drives` is
for Storage.

## 4. The fix

`create-index` as built in 4c, with the options the query page already sends for a foreign key (`source:
foreign-key`, the table, the key's columns as equality, nothing included). Its guards stand (IDX02 over-indexed,
IDX04/IDX08 overlap, IDX09 too wide), and so do its online option by edition, its progress bar and its
read-back. Changes:

- `AvailableDesignFixes(facts, findings)`: one `create-index` per IDX14 finding, as `performanceIndexFixes`
  does for WRK03.
- The change row links to the finding's check: `FixDef` keeps WRK03 as its default check, and a fix built from
  a finding carries that finding's check (`Fix.CheckID` = `IDX14`), so the change log and the tuning report
  name the right check. The same holds for the query page's foreign-key action.
- The preview gains one line first: "Deletes from Orders read all 1,376,845 rows of Lines today; with this
  index they read only the matching rows."

## 5. Joining up

- The query page keeps its foreign-key sentence (it is the same rule seen from one query's plans) and its
  action; the sentence ends with "IDX14 on the Design card lists every such key."
- Storage keeps IDX06 (heaps), IDX10 and IDX13; they are not moved.
- `docs/landing-page.md`'s Design row is rewritten to this phase and the roadmap; SCOPE.md gets a phases row
  and a decisions-log row (2026-11-08: the Design card returns; the 2026-11-03 fold is replaced).
- `storage.go`'s IDX06 comment is corrected: the 100 MB / 1 million rows bar is the triage's own
  (PerformanceTriage.sql 705); sp_BlitzIndex check 44 fires only on heaps over 10,240 MB.

## 6. Not in this phase

D2 to D6 above. Foreign keys that are disabled or not trusted (D4). Joins from the parent side as a cost (the
query page has them per query). Updates to the referenced key (not counted: `leaf_update_count` counts every
update, not key changes). Telling vendor objects from the client's (D5). A list across every database on the
instance page.

## Tests

Domain: fires with deletes and 10,000 rows; not at 9,999 rows; not with no deletes (in the fold instead); rank
3 at 1,000,000 rows and 4 below; ordering by rows times deletes; the CASCADE sentence; a key of two columns in
the key's order; every verdict and its precedence, with the 7-day window from a restart and from a restore;
deletes not read (-1) makes the card say so and raises nothing; the fix's options and its `CheckID` IDX14.
Collect: tagged, read only, each area in its TRY, the `@tables` NULL path and the list path give the same rows
for the same tables. Service: the overview and the card page build the Design card; the stored card read. API:
the card detail carries `foreignKeys`. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in a scratch database (dropped afterwards):

- A parent table and a child of 20,000 rows with a foreign key and no index on it, and a second child of 20,000
  rows with a key to another parent: before any delete, the card is green "Too early to tell" (the database was
  just created), both keys in the fold.
- Delete 10 parent rows of the first key (rows with no children): the card turns amber with one IDX14 finding
  naming the 10 deletes and the 20,000 rows; the second key stays in the fold.
- Apply: the preview shows the line of section 4 and the guards; the index is created, read back, a change row
  names IDX14; re-reading the card shows no finding for that key and the Foreign keys fact drops from "2 without an index" to "1".
- A key of two columns covered by an index on the same columns in the other order: not listed.
- An ON DELETE CASCADE key with deletes: the cascade sentence.
- On the restored copy of an application database: the card is green, "Too early to tell", 20 keys in the fold,
  and no number from it leaves the machine.
- Phone width: the card and the fold fit without horizontal scroll.
