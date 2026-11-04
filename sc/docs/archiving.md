# Archiving tables that only grow: a proposal for discussion (2026-11-04)

From the notes (number 3): tables with logs, audit rows and statuses need maintaining; move old rows into an
archive table, with options on how much to keep and a schedule, or delete them; the user may name the table.

## What it is in kakapo's terms

One fix, `archive-table`, reachable in two ways: from an IDX10 finding ("this table only grows") with the table
filled in, and from a standing **Tables** row on the Storage card where the user picks any user table by name.
The preview is the whole decision; Apply creates the archive table and the Agent job that keeps it; the job does
the work in small batches, every night, until it has caught up and then as rows age. The remove fix deletes the
job and leaves the archive table and its rows where they are.

## The preview

Inputs:

| Input | Default | Rule |
|---|---|---|
| `table` | the finding's table, or typed | must exist, be a user table, not a system or memory-optimised table |
| `dateColumn` | the table's first datetime column, preferably one that is indexed | the column that says how old a row is; select of the table's date columns |
| `keepMonths` | 12 | 1 to 120: rows older than this leave the table |
| `action` | `archive` | `archive` (copy then delete, the default) or `delete` (no copy; the preview says so in red) |
| `archiveTo` | `{schema}.{table}_Archive` in the same database | or a database named `{db}_Archive` on the same server when it exists (created by a separate fix) |
| `schedule` | daily 02:30 | HH:MM, after the backup when kakapo created one |
| `batchRows` | 5,000 | 500 to 50,000 rows per statement, so the delete never holds a lock for long |
| `maxMinutes` | 30 | the job stops after this each night and continues the next; the backlog is worked off over days |

What the preview reads and says, before anything runs (the reads are the safety):

- **Size and age:** rows, MB, the oldest row, how many rows are older than the retention (an estimate from the
  indexed date column when it is indexed, else from a sample), how many nights the first catch-up will take at
  the batch size and time limit.
- **Foreign keys that point at this table.** Rows referenced from elsewhere cannot be deleted; the preview
  lists them and blocks when any exist (an archive of a parent table is a design question for the owner).
- **Foreign keys from this table** to others: fine for delete; the archive copy does not carry them.
- **Triggers on the table.** A delete trigger fires once per batch; the preview names them and warns that the
  application may be counting on them.
- **Identity, computed and rowversion columns.** The archive copy uses `IDENTITY_INSERT` and leaves computed
  and rowversion columns out; the preview says which.
- **The date column's index.** When the column is not the leading key of an index, the preview warns that each
  batch scans the table and offers the create-index fix for it first.
- **Replication, change tracking, CDC, temporal.** Any of these on the table blocks: the delete has effects
  the owner must decide on.
- **Room.** The archive adds the copied rows to the database (or the archive database) before the delete
  frees them; the preview shows the MB and the drive's free space.
- **Backups.** In FULL recovery the deletes are logged; the preview says how much log a night's batches write
  and that the log backup job must exist (BAK02).

Warnings end with the one that matters: "This deletes application data. Confirm with whoever owns the
application that rows older than {n} months are not needed by it (reports, integrations, audits by law)."

## What Apply creates

1. The archive table, created from the source table's columns (`SELECT TOP (0) … INTO`, then the identity and
   a clustered index on the date column), plus an `ArchivedAt` column.
2. An Agent job "kakapo: archive {db}.{table}", daily at the chosen time, owned by sa, one step:

```sql
DECLARE @cutoff datetime2 = DATEADD(MONTH, -@keepMonths, SYSDATETIME()), @stop datetime2 = DATEADD(MINUTE, @maxMinutes, SYSDATETIME());
WHILE SYSDATETIME() < @stop
BEGIN
    BEGIN TRANSACTION;
    DELETE TOP (@batch) src
    OUTPUT deleted.*, SYSDATETIME() INTO [archive] (…)
    FROM [table] AS src WHERE src.[dateColumn] < @cutoff;
    IF @@ROWCOUNT = 0 BEGIN COMMIT; BREAK; END
    COMMIT;
    WAITFOR DELAY '00:00:01';
END
```

   `DELETE … OUTPUT INTO` moves a batch in one atomic statement, so a crash between copy and delete cannot
   lose or double rows. With `action: delete` the OUTPUT clause is left out. `LOCK_TIMEOUT` and
   `DEADLOCK_PRIORITY LOW` in the step, so the job yields to users.
3. The read-back: the job exists and is scheduled, the archive table exists with the same columns.
4. The change row: object the table, type ARCHIVE, the statements, the rollback (delete the job; the archive
   table is kept and the row says so), and the options chosen.

A **Run the first batch now** option in the preview (default off) runs one batch under the user's eyes, with the
rows moved shown in the result, so the first night is not the first time it happens.

## What the card shows afterwards

The Tables row lists each table kakapo archives: rows kept, rows archived to date (from the job's history and a
count of the archive table), the last run, and Remove. IDX10 stops firing for a table with an archive job
(it reads the job) and says "archived nightly, {n} months kept" in its evidence instead.

## Number 4 from the notes, in passing

A general maintenance schedule (statistics, integrity, the backups) already exists piece by piece: the
backup schedule, the CHECKDB job, the statistics job, the alerts. What is missing is one page that shows them
together as "the maintenance plan for this server" with what is scheduled when, which is a presentation of
what the fixes created rather than a new capability. Worth doing after archiving, as part of the Upkeep card.

## Decisions for you

1. **Same database or an archive database?** Same database is simpler (one backup, one restore, nothing to
   create), but the archive rows then stay in every backup and CHECKDB of the live database. A `{db}_Archive`
   database on the same server (SIMPLE recovery, its own backup job) keeps the live database small, which is
   what the archive is for. I would offer both, default to the archive database when it exists, and make
   creating it a separate fix.
2. **Delete without archive.** Offer it (some log tables are worthless after a week) or not? I would offer it
   with the red warning and the owner's confirmation; refusing it only sends the consultant to SSMS.
3. **Who picks the table.** IDX10's candidates plus any table by name, as proposed; or only IDX10's? The notes
   say the user may need to tell us, so both.
4. **Blocked by foreign keys pointing at the table.** Block (my proposal), or allow with a cascading delete of
   the children? Cascading deletes of application data from a monitor is a line I would not cross in version 1.

## What it is not

Not a general ETL or partitioning tool; no partition switching, no compression of the archive beyond PAGE
compression where the edition allows, no cross-server archives. One table, one date column, one retention, one
nightly job, batches that yield to users.

## Revision 2 (2026-11-04): what the existing scheduler jobs taught us

Francois shared the C# archive and delete jobs his scheduler runs today (batched `DELETE … OUTPUT INTO`, a
batch size, a time limit, a dry run, a row estimate first, a WHERE template with parameters, an optional
index and statistics tidy-up afterwards, `READPAST, UPDLOCK, ROWLOCK` on the batch). Taken into the design:

- The archive lives in an `archive` schema under the same table name (`archive.MailLog`), not `MailLog_Archive`.
- Besides the date column, an optional **condition** (`Status = 'Closed'`), validated by the read-only
  statement checker of the architecture review (item 3) and used by the preview's row estimate, so "closed
  orders older than a year" is expressible, which the notes' "statuses" asks for.
- The preview's first-run choice is explicit: estimate only, or move one batch now.
- An optional tidy-up step in the job: reorganise or rebuild the source table's indexes and update its
  statistics after a night that moved many rows; off by default, on when the first catch-up is large.
- The batch select takes `READPAST, UPDLOCK, ROWLOCK` so the job yields to users.

Not taken: dropping the identity column from the archive (kakapo keeps the original ids with
`IDENTITY_INSERT`, so an archived row stays traceable), and names concatenated into SQL text (every name is
quoted through the catalogue and validated first). The loop runs as a SQL Agent job step rather than inside a
service, because kakapo has none.

## Revision 3 (2026-11-04): options, and the rule for options

- The archive's name is an option, `archiveName`: `schema` (default, `archive.MailLog`), `suffix`
  (`MailLog_Archive`) or `prefix` (`Archive_MailLog`), so a site keeps the convention it has.
- The rule, which applies to every fix from here on: **whatever could reasonably differ between sites is an
  option; every option has a default that works unseen; the preview shows up front only the options that
  change the decision** (here: the table, the date column, months to keep, archive or delete) **and the rest
  under "More options"** (name convention, archive database, schedule, batch size, time limit, condition,
  tidy-up, first-run choice). The fix dialog gains that fold once, and every fix with inputs uses it.
