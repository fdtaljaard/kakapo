# Phase 5s contract: tables without a primary key (DES01) and the add-primary-key fix

Agreed with Francois on 2026-11-10: every decision below as recommended.

Nothing in kakapo says that a table has no primary key. IDX06 (Storage) flags large heaps, but a heap can have a
primary key, and a clustered table can lack one. On the shared test database, 8 of 59 user tables have no primary
key: 7 are heaps, the eighth is clustered; all 8 have an identity column; none has a unique index on columns that
are all NOT NULL; 2 have 10,000 rows or more, the biggest 1.7 million.

This phase adds, end to end:

1. **DES01, tables without a primary key**, on the Design card: one finding per table that names it, its rows and
   size, whether it is a heap, and the key kakapo would add.
2. **The `add-primary-key` fix**: preview (the key chosen from what the table already has, the statement, the
   rollback, the cost and the locking), Apply as a long fix, read-back, change row with the rollback.

5r is IDX02 and IDX08 (contract only). 5p and 5q are untouched.

## Decisions (agreed 2026-11-10, as recommended)

1. **Apply, not Copy only: this overturns part of 5n's owner rule.** 5n (agreed 2026-11-08) puts "a missing
   primary key" and "a clustered index on a heap" in the Vendor row: Copy for the vendor only, never Apply from
   design review. This phase asks for an Apply. Two different changes hide in it:
   - **(a) A key on columns the table already has** (a unique index, or an identity column): the application
     sees no new column; it only gains a constraint. Close to "invisible to the application".
   - **(b) A new `bigint IDENTITY` column**: the table changes shape. An `INSERT INTO t VALUES (...)` without a
     column list, or an `INSERT ... SELECT *`, fails afterwards (error 213), and the vendor's next upgrade may
     fail when it meets a column or a key it did not create.

   Recommended: owner Vendor stays (the finding says "tell the vendor"), but the fix is Apply for (a) and (b),
   with (b) **held back** (5m's `Held`) until the user ticks "The application names its columns when it inserts
   into this table", and held back regardless when kakapo finds code that would break (section 4, guards).
   - Alternative 1: Apply for (a) only; (b) is Copy only.
   - Alternative 2: Copy only for both, as 5n says. Then this phase is a finding and a script.
2. **Which tables fire.** No source on disk gives a threshold for a missing primary key (the triage, sp_Blitz,
   sp_BlitzIndex and sp_IndexCleanup have no such rule; the rule catalogue of 2026-11-08 confirms it). The source
   is Microsoft Learn's (section "Sources"), with no number. Recommended: reuse IDX14's bars, which are the
   triage's measure of a large table: **a finding per table of 10,000 rows or more; rank 3 (Medium) from
   1,000,000 rows, else 4 (Low)**. Smaller tables with rows go in a fold, "Smaller tables without a primary key",
   names and rows only, no Apply (Copy). Empty tables are not listed.
   On the test database: 2 findings, 6 in the fold.
   - Alternative: every table with rows is a finding (8 on the test database, 24 on the restored client copy of
     5n): the card fills with Low findings.
3. **The check id: DES01**, kakapo's own (like CHG01, DEC01), because the triage's IDX series has no such rule.
   - Alternative: IDX15, to sit beside IDX04 and IDX14.
4. **The order kakapo picks the key in** (section 3): an existing unique index on NOT NULL columns, then the
   identity column, then a new `bigint IDENTITY` column. The user can pick any of the three that exist from a
   select. Columns that are NOT NULL and *might* be unique but have no unique index are **not** offered: only a
   full scan could tell, and the add fails on the first duplicate anyway.
5. **An identity column is not proof of uniqueness** (`DBCC CHECKIDENT ... RESEED` and `IDENTITY_INSERT` can
   repeat values). Recommended: no duplicate scan in the preview; the preview says "SQL Server refuses the key if
   two rows share a value, and then nothing changes", and the add is one atomic statement.
   - Alternative: the preview counts duplicates (`GROUP BY ... HAVING COUNT(*) > 1`), which reads the whole
     table on every preview.
6. **A clustered table whose clustered index is already unique on NOT NULL columns** has a key in all but name.
   Recommended: still a finding, rank 5 (Info) whatever its size, Do this "it already has a unique key; a
   primary key on the same columns costs a second copy of the key and only matters for tools that need one".
   The fix offers a nonclustered primary key on those columns. Turning the clustered index itself into the
   primary key (drop and re-create) rewrites the table and its indexes twice: not offered.
7. **Clustered or nonclustered on a heap: clustered**, which also ends IDX06 for that table; a nonclustered
   primary key on a heap is itself a warning in sp_BlitzIndex (check 47). On a clustered table: always
   nonclustered (a table has one clustered index). The heap's choice is a select under "More options".
8. **The new column's name: `kakapo_id`**, and the key's `PK_{table}`, both editable; both refused if taken.
   - Alternative: `{table}_id` or `Id`, which look like the application's own and invite the vendor to reuse them.
9. **The apply's bound: 30 minutes, as create-index**, run as a long fix (4f). A table whose rewrite would take
   longer is better done by hand in a maintenance window. Recommended: no size cap; the preview says "test on the
   sandbox first" from 1,000,000 rows or 1 GB (create-index's `largeTable`), and Copy script is always there.
   - Alternative: hold Apply back above a size (none is sourced).
10. **IDX06 points to it.** When a heap that IDX06 flags also has no primary key, IDX06's Do this adds "The Design
    card's DES01 adds a clustered primary key." IDX06 gets no fix of its own.

## Sources (each sentence the app shows cites its page; to be re-read when building)

- Microsoft Learn, "Primary and foreign key constraints": a primary key's column or columns uniquely identify each
  row and enforce the table's entity integrity; it creates a unique index, clustered unless a clustered index
  already exists or NONCLUSTERED is named; its columns must be NOT NULL.
- Microsoft Learn, "Create primary keys": adding a key to an existing table checks every row; a duplicate or a
  NULL fails it.
- Microsoft Learn, "ALTER TABLE (Transact-SQL)": `ADD ... IDENTITY` on a table with rows fills every row (a
  size-of-data operation under a schema-modification lock); `WITH (ONLINE = ON)` for a constraint's index;
  the values the existing rows get follow no particular order.
- Microsoft Learn, "Guidelines for online index operations": online index operations are an Enterprise (and
  Developer) feature, not Standard or Express; an online build still takes a short shared lock at the start and
  a schema-modification lock at the end.
- Microsoft Learn, "Temporal table considerations and limitations": the history table cannot have constraints
  (primary key, foreign key, table or column constraints). "Publish data and database objects": tables published
  for transactional replication must have a primary key. "Enable and disable change tracking": a table must have
  a primary key to be tracked. These say what a missing key blocks; they are the finding's Why.
- sp_BlitzIndex check 47 (heap with a nonclustered primary key), for decision 7.
- kakapo's own: `domain/index_fix.go` (`OnlineIndexEdition`, `estimateIndexMB`, `largeTable`, the 10-second lock
  wait of `safeSession`), 5l's room line, 5m's `Held`, 5o's name search.

## 1. The read (`collect/design.go`)

The Design card already reads every user table's indexes (`AllIndexFacts`: `IsPrimaryKey`, unique, filtered,
disabled, keys, rows, size). One small query is added to `ReadDesignFacts`, in its own TRY like the others,
for the tables with no primary key only: each table's kind flags and the candidates.

- Flags from `sys.tables`: `temporal_type`, `is_memory_optimized`, `is_external`, `is_node`, `is_edge`,
  `is_replicated` or `is_merge_published`, `is_tracked_by_cdc`, `ledger_type` (2022 and later, read only where
  the column exists), and whether it is partitioned (`sys.partitions` with more than one partition) or has a
  columnstore index.
- The identity column, if any (`sys.identity_columns`).
- Which columns of the unique indexes are nullable or not persisted computed (`sys.columns`), so a unique index
  is a candidate only when every key column is NOT NULL.

`DesignFacts` gains `NoKeyTables []NoKeyTableFact` and `NoKeyError`. The fix's preview reads one table again
with `collect.IndexFacts` plus the same query for that table and the code search of section 4.

## 2. DES01 (`domain/design.go`, pure)

**Not a finding, never**: a table with a primary key; a temporal history table or a ledger history table
(they cannot have one); a memory-optimized table; an external, node or edge table; kakapo's own tables.

**Finding** per table of 10,000 rows or more (decision 2), largest first:
- Title "Tables without a primary key", question "Which tables have no primary key?", owner Vendor, When
  Window (decision 1 a/b both lock the table), Effort Moderate, Object `[schema].[table]`.
- **Finding** (answer first): "dbo.StockMoves has no primary key: nothing stops two identical rows among its
  1,732,636, and a row cannot be named by a key." Heap: "...; it is also a heap (IDX06 on the Storage card)."
- **Why**: "A primary key makes SQL Server refuse a second row with the same key, and replication, change tracking
  and many tools need one (Microsoft Learn, 'Primary and foreign key constraints')."
- **Do this**, by the key kakapo would pick: "Add a primary key on its identity column OrderId (clustered, which
  also ends the heap). Tell the vendor first: their next upgrade must expect it." / "...on its unique index
  UX_Moves_Ref's columns (Ref, Line)." / "...on a new bigint identity column: this writes every row and adds a
  column, so check with the vendor that the application names its columns when it inserts."
- **Then check**: "Re-run: DES01 gone for that table."
- **Evidence**: "Table [dbo].[StockMoves]: 1,732,636 rows, 210 MB, heap, no unique index; identity column
  [MoveId]; 3 nonclustered indexes (rebuilt with the clustered key)." Refusals (section 4) are named here too,
  so a refused table says why before the dialog opens.
- **FixID** `add-primary-key`, unless refused, then no fix (Copy only).

**Card**: the verdict gains a clause after IDX14's and IDX04's: "2 tables have no primary key, the biggest
dbo.StockMoves (1.7 million rows)." A fact line "No primary key: 8 tables (2 large)". The fold of decision 2 under
the findings, as IDX14's fold. `CheckStatus` for DES01 as for the others (skipped with `NoKeyError`).

## 3. The key (`domain/primarykey_fix.go`, pure)

`FixDef{"add-primary-key", "Add a primary key", ScopeDatabase, "DES01", "Primary key"}`, appended to `FixDefs`
(and to `TestFixDefsMatchTheCatalogue`). Options: `table`, `key` (the candidate), `name`, `column`, `clustered`,
`online`, `confirm`.

**Candidates**, in order (decision 4), the first is the default:
1. `unique:{index}`: an enabled, unfiltered unique index (or unique constraint) whose key columns are all NOT
   NULL; the clustered one first, then the narrowest key. The key is a **new** index on the same columns (SQL
   Server cannot turn an index into a primary key): the existing unique index stays (it may be named in hints and
   foreign keys may use it), and is listed as "now a copy of the key; disable it later if nothing uses it" (5o's
   fix can, once 5o's guards pass).
2. `identity:{column}`: the identity column. Not proven unique (decision 5).
3. `new`: a new column `bigint IDENTITY(1,1) NOT NULL`, named by the `column` input.

**Clustered**: heap, clustered by default (decision 7, input under More options); clustered table,
nonclustered. **Online** (bool input, as create-index): only where `OnlineIndexEdition` (Enterprise, Developer);
on Express and Standard the preview says "offline: the table can be neither read nor written while it runs".
Adding a column is always offline, whatever the edition.

**Statement** (three-part names, as the index fixes):
- Candidates 1 and 2: `ALTER TABLE [db].[s].[t] ADD CONSTRAINT [PK_t] PRIMARY KEY CLUSTERED ([A], [B]) WITH
  (ONLINE = OFF, SORT_IN_TEMPDB = ON);`
- Candidate 3: `ALTER TABLE [db].[s].[t] ADD [kakapo_id] bigint IDENTITY(1,1) NOT NULL CONSTRAINT [PK_t] PRIMARY
  KEY CLUSTERED;` One statement: it fails or succeeds whole.

**Rollback**: `ALTER TABLE [db].[s].[t] DROP CONSTRAINT [PK_t];`, then for candidate 3 `ALTER TABLE ... DROP
COLUMN [kakapo_id];`. The preview says what undoing costs: a clustered key dropped turns the table back into a
heap and rewrites it again; the dropped column's 8 bytes a row stay until the table is rebuilt; a foreign key
created later that points at the key stops the rollback.

**The cost the preview shows** (warnings, in this order):
- What it rewrites: heap to clustered, "rewrites the whole table (210 MB) and rebuilds its 3 nonclustered indexes,
  which then carry the key instead of the row's address"; nonclustered, "builds one index of about {size}"
  (`estimateIndexMB`); candidate 3 also "writes every row to fill the new column".
- Locking: offline, "dbo.StockMoves can be neither read nor written until it finishes; users who touch it wait";
  online, create-index's line. Always: "kakapo gives up after 10 seconds of waiting for its lock rather than queue
  in front of users."
- Room (5l's `Needs`): the data files the new structure (the table's size for a clustered key, the index's
  estimate otherwise), tempdb the sort, the log about as much in FULL recovery. On Express, the database's size
  cap (10 GB, 50 GB from SQL Server 2025) is a line: the table's copy must fit under it.
- `largeTable`: "test on the sandbox first".
- Candidate 2: "SQL Server refuses the key if two rows share an identity value, and then nothing changes."
- Candidate 3: the shape warning of decision 1 (b), and "the existing rows get numbers in no particular order;
  new rows get the next ones".
- "Tell the vendor: their next upgrade must expect this key{ and column}." The permission line: ALTER on the table.

## 4. Refusals and guards (`domain/primarykey_fix.go`)

**Refused** (no changes, the first warning says why; Copy script still shows the statement where one can be
built): the table has a primary key now (the card was read before someone added one); it is replicated or
merge-published, or tracked by change data capture (a new key or column changes what is published or captured:
the replication or CDC owner decides); it is partitioned (the key must include the partitioning column: a
design question); it has a columnstore index; the name or the column name is taken; the chosen candidate no
longer exists, is disabled, filtered or has a nullable key column.

**Held back** (5m's `Held`, candidate 3 only):
- until `confirm` is ticked: "The application names its columns when it inserts into dbo.StockMoves";
- when the database's code (`sys.sql_modules` of the procedures, functions, views and triggers that reference the
  table, `sys.sql_expression_dependencies`) or a Query Store query text of the last 30 days has an `INSERT` into
  the table without a column list, or a `SELECT *` from it inside an `INSERT`: "dbo.ImportMoves inserts into
  dbo.StockMoves without a column list: a new column makes it fail (error 213). Change the code first, or ask the
  vendor." Up to three places named. As 5o says: code outside SQL Server cannot be searched, and the confirm says
  so.

## 5. Apply, read-back, change row (`service/primarykey_fix.go`)

`registerLongFix("add-primary-key", ...)` with create-index's bound (decision 9) and a completion check that
reads whether the key exists (an app restarted mid-run finds the outcome). Apply re-checks the refusals and
`Held`, runs the previewed statement with `collect.ExecFix`, then reads the table back with `collect.IndexFacts`:

- **Verified** when a primary key named as previewed exists, clustered or nonclustered as previewed, with the
  previewed columns in order, enabled; for candidate 3 also the column, `bigint`, identity, NOT NULL.
- **After**: "PK_StockMoves on dbo.StockMoves (kakapo_id), clustered, 224 MB".
- **Change row**: type INDEX (the tuning report already groups index changes), object `schema.table.PK_name`,
  description "Added primary key PK_StockMoves on dbo.StockMoves (kakapo_id, new bigint identity column),
  clustered, in {db}, for DES01", the statement and the rollback.
- A statement that ran and could not be read back is logged as "applied, but could not be read back", as the
  index fixes do.

## 6. Web (`sc/web`)

No new component: the Design card page lists DES01 like IDX14 and IDX04, the generic fix dialog shows the
inputs (key select, name, column, clustered under More options, online on Enterprise, the confirm), the warnings,
the room lines and `Held`. Mock mode (`VITE_MOCK_API=1`) gains a DES01 finding on a heap with an identity column,
one on a table with nothing but a new column to offer (held until confirmed), and their previews, so the wording
can be read without a server.

## 7. Not in this phase

Making a nullable column NOT NULL to make it a candidate (changes the column: Vendor); checking columns for
duplicates; turning an existing clustered or unique index into the key; a heap with a nonclustered primary key
(sp_BlitzIndex 47) as its own finding; telling the vendor's tables from the client's (5n's D5).

## Tests

- Domain: DES01 fires and does not fire (has a key; history, memory-optimized, external, graph tables; under
  10,000 rows goes to the fold; empty not listed; rank 3 from 1,000,000, 5 for a unique clustered index); the
  verdict clause and the fact line; candidates in order; the statements, three-part and quoted, for each candidate
  and clustered or not; online only on edition 3; every refusal; `Held` for candidate 3 until confirmed and when
  code inserts without a column list (Go matcher over sample module texts); read-back verified and not.
- `TestFixDefsMatchTheCatalogue` gains the id.
- Live, gated on `SQLMONITOR_TEST_PK_DB` (as `TestCompatLevelLive`): creates a scratch heap with an identity column
  and a scratch heap with nothing, previews and applies each (candidates 2 and 3), reads back, runs the rollback,
  reads back again, drops the scratch tables.

## Done when

The contract is agreed; DES01 and `add-primary-key` built and tested as above; `go test ./...` and
`npm run build` green; the wording checked in mock mode; a short note of how Francois tests it in the app.

## Built (2026-11-10)

- `domain/primarykey.go` (DES01, the fold, the fact line), `domain/primarykey_fix.go` (the preview, the code
  matcher, the read-back), `collect/primarykey.go` (`ReadNoKeyTables`, `PrimaryKeyFacts`, `SearchTableCode`),
  `service/primarykey_fix.go` (a long fix), `web/src/components/no-key-tables-fold.tsx`, the mock in
  `web/src/api-design.mock.ts`. IDX06's Do this adds the DES01 line when its heap has no primary key (the
  Storage read gains `has_pk`).
- Found while building, beyond the contract: a ledger table (updatable or append-only) is refused, and a ledger
  history table is never a finding, like a temporal history table; kakapo's own archive tables (clustered index
  `CX_kakapo_archive`, phase 5a) are never a finding. The fact line counts tables that hold rows: "3 tables (2
  large)". The Express size cap is a warning, not a room line (the room line does not know the cap). A refused
  table's dialog answers with the refusal (409), as other refused fixes do; the card names it in the evidence first.
- The Design card's note, shown while grey, names primary keys.
- Verified on SQL Server 2025 Express (`TestPrimaryKeyLive`, scratch tables in a scratch database, over TCP): the
  identity column's clustered key on a 12,000-row heap added, read back verified, rolled back to a heap; the new
  column held by a procedure that inserts without a column list, then by the missing confirm, then added on 500
  rows, verified and rolled back (key and column gone). The shared test database's Design card (read only): 2
  findings (1.7 million and 10,619 rows, both heaps with an identity column), 1 table in the fold.

## How to test it in the app

1. Mock: `VITE_MOCK_API=1` (launch config `web-mock`), WarehouseLive, Design card. Two DES01 findings and the
   fold "Smaller tables without a primary key". Apply on StockMoves (identity key) applies; on Notes_Import the
   new column is held until the confirm is ticked, then held by dbo.ImportNotes (set localStorage
   `mock.noInsertProc` to `1` to let it apply).
2. Live: the shared test database on `.\SQLEXPRESS`, Design card. Apply on the 10,619-row table first (small,
   offline for a moment): the result says "Applied and read back: PK_... (ID), clustered". The change log holds
   the rollback (`ALTER TABLE ... DROP CONSTRAINT`); run it from there, or keep the key. The 1.7-million-row table
   rewrites about 200 MB and rebuilds 5 indexes: do it when a few minutes of locking do not matter.
3. Live test again: `SQLMONITOR_TEST_HOST=localhost\SQLEXPRESS SQLMONITOR_TEST_PK_DB=kakapo_express_test go test
   ./internal/collect/ -run TestPrimaryKeyLive -v`.
