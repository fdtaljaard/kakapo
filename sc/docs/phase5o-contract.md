# Phase 5o contract: duplicate indexes (IDX04) and the disable-index fix

The second step of design review (D2 in `docs/phase5n-contract.md`). The Design card gains IDX04: indexes with
the same key columns as another index on the same table. Every insert and update writes each copy, and queries
need only one. Where another copy holds every column of a plain index, that index ends in a new
**`disable-index`** fix: disabling frees its space and stops its writes, keeps its definition, and one
rebuild brings it back. Where the copies hold different columns, the finding says so and ends in Copy: merging
them is a job for someone experienced with indexes, not a fix.

Agreed with Francois on 2026-11-08 (write D2 after 5n). Owner Us, as every index change that the application
cannot see (5n's owner rule); the Do this says to tell the vendor. Tuning Advisor leftovers (IDX05) are not
taken: they cost almost nothing, so they break "show the cost".

**Sources.**
- `research/granite-sql/.../PerformanceTriage.sql` IDX04 (570-630), with its grouping and keeper (426-458): the
  rule taken as it is. Copies are the enabled clustered and nonclustered, non-hypothetical indexes of one table
  with the same key columns in the same order and direction and the same filter. The keeper is ranked: primary
  key first, clustered before nonclustered, unique first, more reads, larger, lower index id. A copy is **safe
  to disable** when it is a plain nonclustered index (not unique, not a primary key) and another copy holds
  every one of its columns and either ranks higher, is the clustered index, or holds a column it lacks (436-447,
  as built); the copy named as kept is the best-ranked one that holds every column and is not itself safe. The
  rest of a group with two or more plain copies **needs merging**, leaving out a copy a wider index already
  covers (IDX08's). Rank 3 when the safe copies of a group are 50 MB or more, else 4. "Disable rather than
  drop so the change can be undone" (510). `Checks.csv` 60: owner Us, "Re-run: IDX04 gone. Writes per index in
  IDX03 lower."
- sp_BlitzIndex check 1 (duplicate keys) and sp_IndexCleanup rule 2 (`sp_IndexCleanup.sql` 4159, exact
  duplicates, disable never drop, 7051) agree. sp_IndexCleanup's foreign-key guard (6210-6260): never disable
  an index a foreign key names in `sys.foreign_keys.key_index_id`. The triage never disables a unique index, and
  only a unique index or key can be named there, so the triage is not exposed; the fix keeps the guard anyway,
  because what it prevents is silent (below). Its rules 3 to 7 stay out (`docs/darlingdata-review.md`).
- Microsoft Learn, "Disable indexes and constraints" and "ALTER INDEX": a disabled nonclustered index's data is
  removed and its definition kept; `ALTER INDEX … REBUILD` enables it again; disabling an index a foreign key
  uses disables the key. To be re-read when building: each sentence the app shows cites its page.

**Facts verified on `localhost\SQL2022` on 2026-11-08**, in a scratch database (created and dropped):
- Two nonclustered indexes on the same keys: disabling one took its used pages from 120 to 0; `ALTER INDEX …
  REBUILD` enabled it again (`is_disabled` 0).
- A query naming the disabled index in a hint (`WITH (INDEX(IX_B))`) fails with error 315: "Index … is disabled
  or resides in a filegroup which is not online". So does any procedure, view or plan guide that names it.
- Disabling a unique index that a foreign key references disabled the key with only a warning (no error); an
  insert of an orphan row was then accepted; **rebuilding the index did not enable the key again** (still
  disabled and not trusted). This is why the guard stays even though the triage's rule cannot reach it.
- On a restored copy of an application database (read only, counts only): one group of two copies, one of them
  a plain nonclustered index.

## 1. The read (`collect/design.go`)

The Design card's read gains every index of every user table, with the columns `collect.IndexFacts` reads for one
table today (type, unique, primary key, unique constraint, disabled, hypothetical, filter, key columns with their
direction, included columns, size from `sys.dm_db_partition_stats`, reads and writes from
`sys.dm_db_index_usage_stats`). One query for both: the per-table read takes a NULL table to mean every user
table, as 5n did for the foreign keys. Also read: for each table, the indexes a foreign key names
(`sys.foreign_keys.key_index_id`). Its own TRY, its own error (`IndexesError`); usage that cannot be read leaves
reads and writes at -1 and the finding says so.

## 2. IDX04 (`domain/design.go`, pure)

Catalogue entry: `IDX04`, card `design`, "Duplicate indexes", question "Which indexes copy another one?", owner
Us, then check "Re-run: IDX04 gone for that table; the keeper's reads went up by what the copy had."

One finding per group (table, key columns, filter), the triage's grouping, keeper and safe rule above, rank 3
when the safe copies add up to 50 MB or more, else 4. Findings are ordered by the safe copies' MB, largest first.

- **Object**: `[schema].[table]`.
- **Finding** (safe copies): "IX_B is a copy of IX_A on Orders (CustomerId, OrderDate): 1.2 GB and 4.3 million
  writes since 27 Oct, for nothing. IX_A holds every column of IX_B." With two or more safe copies, one sentence
  per copy. Usage not read: the writes part is left out.
- **Finding** (needs merging only): "IX_C and IX_D on Orders have the same keys (CustomerId) but each holds columns
  the other does not. One index with all their columns would replace them." No Apply.
- **Why**: "Indexes with the same keys do the same job: SQL Server keeps every copy up to date on each insert,
  update and delete, and stores, backs up and maintains each one."
- **Do this** (safe): "Disable IX_B. Queries that used it use IX_A, which has the same keys and every column. Tell
  the vendor, so their next upgrade does not create it again." (Merging): "Copy for someone experienced with
  indexes, or for the vendor."
- **Evidence**: per copy, its name, keys, included columns, filter, MB, reads and writes since the window's start,
  and which copy is kept and why ("primary key", "clustered", "unique", "most reads").
- **Action**: Apply `disable-index` per safe copy; Copy.

**The card.** IDX04 joins IDX14. Verdict when there are findings: one clause per check that has findings, IDX14's
from 5n first, then "{n} indexes copy another one: {MB} and {writes} writes since {day} for nothing." When
neither has findings, 5n's green sentences stand; the card's checks list shows IDX04 passed. New fact
**Duplicate indexes**: "2, 1.2 GB" or "none". The card's status, ToDo and Fixable count both checks.

## 3. The `disable-index` fix (`domain/index_fix.go`, `service/index_fix.go`)

`FixDef{"disable-index", "Disable a duplicate index", ScopeDatabase, "IDX04", "Index"}`. Options: `table`,
`index`, `keeper`. The preview re-reads the table with `collect.IndexFacts` (the read as it is) and searches for
the index's name (section 4), then:

**Refused** (a warning, no Apply), each with its sentence: the index no longer exists or is already disabled; it
is not a plain nonclustered index (clustered: disabling it makes the table unreadable; XML, spatial,
columnstore); it is unique, a primary key or a unique constraint; a foreign key uses it (`key_index_id`: "Disabling
it would switch off foreign key {name} without an error, and rebuilding the index does not switch the key back
on"); the keeper no longer exists, is disabled, or no longer holds every column (the group changed since the
card was read).

**Held back** (`FixPreview.Held`, as 5m's): the index's name appears in a procedure, view, function or trigger,
in a plan guide, or in the text of a query Query Store ran in the last 30 days (leaving out kakapo's own
statements and CREATE, ALTER and DROP INDEX, which Query Store keeps and which a disabled index does not make
fail): "IX_B is named in
dbo.GetOrders: a query that names a disabled index fails (error 315). Change the code first, or ask the vendor."
Up to three places named. Copy script still works.

**Shown**, otherwise:
- **The lead line**: "IX_B is a copy of IX_A: 1.2 GB and 4.3 million writes since 27 Oct, for nothing."
- **The statement**: `ALTER INDEX [IX_B] ON [db].[dbo].[Orders] DISABLE;` (three-part, as create-index and drop-index)
- **The rollback**: `ALTER INDEX [IX_B] ON [db].[dbo].[Orders] REBUILD;` (with `ONLINE = ON` on editions that have it,
  as create-index's option).
- **Warnings**: "Queries that read IX_B ({reads} times since {day}) will read IX_A instead." "Disabling needs a short
  lock on the table." "Undoing it rebuilds the index from the table: it takes as long as creating it, and the
  space comes back." "kakapo searched the database's code, plan guides and Query Store for the name; code
  outside SQL Server (an application that names the index in a hint) cannot be searched." The edition and
  permission lines as create-index has them.
- **Read back**: `sys.indexes.is_disabled` = 1 and the used pages 0: "IX_B is disabled; 1.2 GB freed inside the
  file." The change row: object `schema.table.index`, description "Disabled index IX_B on dbo.Orders (a copy of
  IX_A), 1.2 GB, in {db}, for IDX04", the statement and the rollback.

The space freed stays inside the data file: the preview says so ("the file does not shrink; new rows reuse the
space"), and nothing suggests a shrink.

## 4. The name search (`collect/index.go`)

`SearchIndexName(ctx, db, database, index)`: SELECTs only, tagged, the safe session, three areas each in its TRY,
each `TOP (3)`: `sys.sql_modules` joined to `sys.objects` where the definition contains the name; `sys.plan_guides`
where the hints or the statement contain it; Query Store's `query_sql_text` of queries whose last execution is in
the last 30 days and whose text contains it (when Query Store is on and readable). The match is the name as a
word (`LIKE` with the name's `[`, `]`, `_` and `%` escaped, then a check in Go that the characters around it are not
letters, digits or underscores). An area that fails says so in the preview ("Could not search plan guides: …"),
and Apply is held: kakapo does not disable an index it could not search for.

## 5. Joining up

- The create-index guard's IDX04 sentence (`index_fix.go`) stays; it now also names the Design card when the
  table has an IDX04 finding.
- The query page's tables section: an index of a table the query reads that is a safe copy says so in one line,
  with the Design card's Apply. (Read from the card's last stored read; no second read.)
- `docs/landing-page.md`'s Design row and SCOPE.md's phases row and decisions log.
- `docs/phase5n-contract.md`'s roadmap marks D2 as 5o.

## 6. Not in this phase

Overlapping indexes (IDX08), over-indexed tables (IDX02), near-copies (IDX09), unused indexes (IDX03: D3, which
reuses this fix), Tuning Advisor leftovers (IDX05), merging indexes as a fix, dropping instead of disabling, a
"re-enable" fix outside the rollback, indexed views.

## Tests

Domain: the grouping (same keys, same order and direction, same filter; a DESC column makes a different group; a
filtered and an unfiltered copy are different groups); the keeper's order (primary key, clustered, unique, reads,
size, index id); safe when the keeper or the clustered index holds every column, merging when not; rank 3 at 50
MB of safe copies and 4 below; the finding, evidence and fact sentences; usage not read. The fix: each refusal
(missing, disabled, clustered, columnstore, unique, primary key, unique constraint, a foreign key's
`key_index_id`, keeper gone or narrower now), held by a module, a plan guide, a Query Store text, and when a search
area failed; the name match as a word (`IX_B` does not match `IX_B2` or `XIX_B`; `[IX_B]` and `IX_B,` match); the
statement, rollback with and without online, the read-back, the change description with IDX04. Collect: tagged,
read only, each area in its TRY, the NULL-table path and the per-table path give the same indexes for one table.
API and web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in a scratch database (dropped afterwards):

- Orders with IX_A (CustomerId, OrderDate) INCLUDE (Total) and IX_B (CustomerId, OrderDate): the card shows one
  IDX04 finding, IX_B safe, IX_A kept, with sizes; after some inserts, the writes since the database's creation.
- Apply: the preview shows the lead line and the warnings; IX_B is disabled, read back with 0 pages, a change row
  for IDX04; the card re-read shows no IDX04 finding. The rollback statement run by hand enables it again.
- A procedure that names IX_B in a hint: Apply held, the procedure named; Copy script works.
- IX_C (CustomerId) INCLUDE (Status) and IX_D (CustomerId) INCLUDE (Total): a "needs merging" finding, no Apply.
- A unique index that a foreign key references, copied by a plain index on the same keys: the plain copy is safe
  and disabled; asking the fix to disable the unique one (through the API) is refused with the foreign-key sentence.
- On the restored copy of an application database (read only): one IDX04 finding, nothing applied, and no number
  from it leaves the machine.
- Phone width: the card, the finding and the preview fit without horizontal scroll.
