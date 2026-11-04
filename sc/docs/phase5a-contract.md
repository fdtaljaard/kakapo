# Phase 5a contract: archive or delete the rows of a table that only grows

`docs/archiving.md` (revisions 1 to 3) made precise, with the decisions taken: the archive name is an option
(schema default); the destination is an option (same database default, an archive database when one exists);
delete without archive is allowed with a red warning; the table comes from IDX10 or from a picker; foreign keys
pointing at the table block. SCOPE rules 1 (sentence first), 4 (generic), 5 (options with defaults, the "More
options" fold). The fix pattern and registry of `docs/phase4c-contract.md` section 0; Agent jobs as the backup
schedule creates them (`domain/jobbuild_backups.go`).

## Seams (in place before the build)

- `domain.FixDefs`: `archive-table` ("Archive or delete old rows", database, IDX10, "Archive") and
  `remove-archive` ("Stop archiving a table", database, IDX10, "Archive").
- `internal/api/tables.go`: `tablesRoutes(mux, svc)` stub wired from `api.go`.
- `FixInput` gains `Advanced bool` (`json:"advanced,omitempty"`): the dialog shows advanced inputs under a
  collapsed "More options" fold (open when any advanced input carries a warning).

## The reads (`collect/archive.go`, SELECTs only, each area in its own TRY)

`collect.ArchiveFacts(ctx, db, database, table)` for one table (`schema.table` validated and quoted):

| Area | Read |
|---|---|
| Table | exists, is a user table, `is_memory_optimized`, `temporal_type`, rows (`dm_db_partition_stats` index 0/1), MB |
| Columns | name, type, `is_identity`, `is_computed`, rowversion/timestamp, nullable; the datetime/datetime2/date columns with whether each is the leading key of an enabled, unfiltered index |
| Keys in | foreign keys that reference this table (`sys.foreign_keys` where `referenced_object_id` = table): name, from table |
| Keys out | foreign keys from this table (count, for the preview's note) |
| Triggers | DML triggers on the table (name, which events, enabled) |
| Features | `sys.tables.is_tracked_by_cdc`, change tracking (`sys.change_tracking_tables`), replication (`is_published`, `is_merge_published`, `is_replicated`), temporal |
| Archive target | whether the target table exists (per naming option) and, when it does, whether its columns match the source's non-computed columns; whether a database named `{db}_Archive` exists on the server |
| Jobs | kakapo's archive jobs for this database: name `kakapo: archive {db}.{schema}.{table}`, enabled, schedule, the step command (the options are read back from a `/* kakapo-archive {json} */` line the step carries), last run outcome and `run_duration` from `sysjobhistory`, rows moved last night from the step's output (the step prints `rows moved: n` and history keeps the message) |
| Room | the data volume's free MB (as the storage facts know it) |

A second read, `collect.ArchiveEstimate(ctx, db, database, table, dateColumn, cutoff, condition)`: the count of
rows older than the cutoff (and matching the condition), as `COUNT_BIG(*)` when the date column is indexed,
else from `TABLESAMPLE SYSTEM (1 PERCENT)` scaled up and marked "estimated"; 10-second timeout; unknown on
timeout.

`collect.ListTables(ctx, db, database)`: every user table with schema, name, rows, MB, its date columns, and
whether kakapo archives it (by job name), for the picker.

## The condition

`domain.ValidateCondition(cond string, columns []string) error`: a WHERE fragment allowed to contain column
names of the table (quoted or bare), comparison operators, `AND OR NOT IN LIKE IS NULL BETWEEN`, string and
number literals, parentheses, and the functions `GETDATE SYSDATETIME DATEADD DATEDIFF CONVERT CAST`. Anything
else fails: semicolons, comments, `SELECT`, `EXEC`, `@`, `xp_`, `sp_`, subqueries, unknown identifiers. Pure,
tested with an allow list and a deny list of twenty cases each. The condition is never interpolated unvalidated.

## The fix `archive-table`

Options and inputs (SCOPE rule 5; the first four up front, the rest advanced):

| Name | Kind | Default | Rule |
|---|---|---|---|
| `table` | text (filled by the finding or the picker; read-only in the dialog) | | must pass the table read |
| `dateColumn` | select of the date columns, indexed ones first and marked | the first indexed date column | required |
| `keepMonths` | text | 12 | 1 to 120 |
| `action` | select `archive` / `delete` | archive | delete adds the red warning |
| `archiveName` | select `schema` / `suffix` / `prefix` | schema | advanced |
| `archiveTo` | select `same database` / `{db}_Archive` | same database, or the archive database when it exists | advanced; the archive database must exist (a later fix creates one) |
| `condition` | text | empty | advanced; validated; shown in the Changes row |
| `schedule` | text HH:MM | 02:30 | advanced |
| `batchRows` | text | 5000 | advanced; 500 to 50000 |
| `maxMinutes` | text | 30 | advanced; 5 to 240 |
| `tidyUp` | bool | false | advanced; adds the index/statistics step |
| `firstRun` | select `nothing` / `move one batch now` | nothing | advanced |

Preview:

- Changes rows: "Table | {rows} rows, {MB} MB, oldest {date} | rows older than {n} months leave nightly";
  "Destination | none | archive.{table} in {db}" (or "deleted, not kept" in the delete case); "Rows affected
  now | | about {n} ({estimated}), {nights} nights at {batch} rows per statement and {m} minutes a night";
  "Job | none | kakapo: archive {db}.{schema}.{table}, daily {HH:MM}".
- Blocking (no Changes): foreign keys referencing the table (named); CDC, change tracking, replication,
  temporal, memory-optimised; no date column; the target table exists with different columns; the archive
  database chosen but missing; a bad option.
- Warnings: triggers (named, "a delete trigger fires for every batch; the application may be counting on
  it"); the date column not indexed ("each batch scans the table: create an index on {col} first", with the
  create-index fix named); identity kept with `IDENTITY_INSERT`; computed and rowversion columns left out of
  the archive; the room the copy needs against the free space; the log the deletes write in FULL recovery and
  the log backup job (BAK02); the Agent stopped; and last, in the delete case, "This deletes application data
  and keeps no copy. Confirm with whoever owns the application that rows older than {n} months are not needed
  (reports, integrations, audits by law)." The archive case ends with the same sentence without "and keeps no
  copy".
- Statement: the create of the archive table (when missing; `SELECT TOP (0) … INTO`, then `ArchivedAt
  datetime2 NOT NULL DEFAULT SYSDATETIME()`, a clustered index on the date column, PAGE compression where the
  edition allows), the job batch (owner sa, category Database Maintenance, description, the step tagged, a
  `/* kakapo-archive {json options} */` line in the step so the Tables row reads the options back), and,
  when `firstRun` is "move one batch now", one batch statement run after the read-back with its rows moved in
  the result.
- The job step:

```sql
SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;
DECLARE @cutoff datetime2 = DATEADD(MONTH, -{keep}, SYSDATETIME()), @stop datetime2 = DATEADD(MINUTE, {max}, SYSDATETIME()), @moved bigint = 0, @n int;
WHILE SYSDATETIME() < @stop
BEGIN
    BEGIN TRANSACTION;
    SET IDENTITY_INSERT [archive].[T] ON;   -- only when the table has an identity column and action is archive
    WITH batch AS (SELECT TOP ({batch}) * FROM [s].[T] WITH (READPAST, UPDLOCK, ROWLOCK) WHERE [date] < @cutoff {AND (condition)})
    DELETE FROM batch OUTPUT deleted.[c1], deleted.[c2], … INTO [archive].[T] ([c1], [c2], …);   -- OUTPUT left out for delete
    SET @n = @@ROWCOUNT;
    SET IDENTITY_INSERT [archive].[T] OFF;
    COMMIT;
    SET @moved += @n;
    IF @n = 0 BREAK;
    WAITFOR DELAY '00:00:01';
END;
PRINT 'rows moved: ' + CONVERT(varchar(20), @moved);
```

  The column list is the source's columns minus computed and rowversion ones; names come from the catalogue
  read and are quoted. A second step, when `tidyUp`: `ALTER INDEX ALL ON [s].[T] REORGANIZE` (REBUILD when
  fragmentation is not readable) and `UPDATE STATISTICS [s].[T]`.
- Rollback: delete the job; the archive table and its rows stay (the rollback text says so).
- Read-back: the job exists and is scheduled; the archive table exists; when a first batch ran, the rows moved.
- Change row: object `{schema}.{table}`, type ARCHIVE, description with the retention, the destination, the
  schedule and the condition.

## The fix `remove-archive`

Options `table`. Preview: the job as it is (schedule, keep, destination, last run, rows moved so far from the
archive table's count), Changes "Job | daily 02:30 | deleted", warning "The archive table archive.{table} and
its {n} rows stay; drop it by hand when nobody needs them." Apply: `sp_delete_job`; read back; change row type
ARCHIVE; rollback the creation batch rebuilt from the options line in the step.

## Where it appears

- IDX10 findings carry `fixId: archive-table` with options `{table, dateColumn}`; IDX10 does not fire for a
  table kakapo archives and says so in its evidence ("archived nightly, {n} months kept, last run {when}").
- The Storage card page gains a standing **Tables** row: the tables kakapo archives (rows kept, rows archived,
  last run, Remove), and a button "Archive a table…" that opens a picker (`GET
  /api/servers/{id}/databases/{db}/tables`: name, rows, MB, date columns; search by name; sorted by MB) and then
  the fix dialog with `{table}`.
- The fix dialog: inputs marked advanced sit under "More options", collapsed unless one of them carries a
  warning; the `table` input is read-only.

## Tests

Domain: the condition validator (allow and deny lists); the batch statement for archive and delete, with and
without identity, with a condition, with the tidy-up; every blocking case; every warning; the options read
back from the step line; the remove fix's rollback round trip; IDX10 silent for an archived table; the Changes
rows and the nights estimate. Collect: every read tagged and read-only; the estimate's two paths. Service: both
fixes registered; a bad option gives no Changes; the picker route. API: the tables route's codes. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022` on a throwaway database with a log table of
200,000 rows across three years and an indexed date column: the picker lists it; the preview estimates the rows
older than 12 months and shows the nights; Apply with "move one batch now" creates `archive.{table}`, the job,
and moves one batch; running the job by hand moves the rest within the time limit and the archive count plus
the source count equals 200,000; Remove deletes the job and keeps the archive; the delete action on a second
table shows the red warning. Everything staged is removed afterwards.
