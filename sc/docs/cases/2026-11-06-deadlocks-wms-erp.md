# Case: deadlocks between a WMS and its ERP, and what row versioning would fix (2026-11-06)

A client's production server (name withheld: this repository is public). One SQL Server instance holds the
Granite WMS database and the client's SAP Business One company database. They are tied together three ways:

- a trigger on SAP's item table (OITM) that writes Granite's MasterItem;
- a SQL Agent job, every 2 minutes, that runs an item import and then the integration procedures (orders,
  receipts, transfers), all in one step;
- Granite's integration service posting into SAP through the DI API, next to SAP users, the DI Server, an
  add-on and a print add-on.

Worked through by Francois with Claude, read-only on the client's server. Facts were checked against
Microsoft Learn and on a local SQL Server 2022 on 2026-11-06. Neither the client nor the server is named here;
procedure names that are the client's own are described rather than named.

## What kakapo said

Build 2026-11-06.1129.d14f897, Granite database.

**Decisions, row versioning (DEC01): "Not enough evidence".**

> kakapo cannot tell yet whether readers and writers wait for each other here: the blocking history has not
> been read in the last 7 days.

The cost side was complete: version store 0.4 MB in tempdb, no transaction open in the database, 19 other
sessions, tempdb about 67 GB free on a drive with 151 GB free.

**Blocking history (BLK01, Medium), after the history was read.**

> The item import procedure held an open transaction for under a second while waiting (3 sessions were recorded
> behind it), 5 times in 7 days. Do: The application opens a transaction and waits for the user or a web call:
> commit sooner, or ask the vendor.

The evidence showed the same text twice: the blocker statement and the blocked statement were both the Agent
job's batch.

## What the evidence showed

### Deadlocks

The deadlocks came from system_health's `xml_deadlock_report` events, one row per side and one per contested
resource:

| Read | Value |
|---|---|
| `Number of Deadlocks/sec` (`_Total`, cumulative) | 1,662 since the restart 57 days earlier, about 29 a day |
| Deadlocks system_health still kept | 34, the oldest 6 hours old: busy servers roll the files over fast |
| Burst | 14 in under 3 minutes in the morning |

The 34, by kind:

| Kind | Count | Isolation level | RCSI removes it |
|---|---|---|---|
| SAP posting against SAP posting in the ERP database: the document numbering row (ONNM, one key every time) against GL balances (OACT), item committed quantity (OITM), series numbers (NNM1) against orders (ORDR), a delete on CRD5 | 24 | SERIALIZABLE on both sides | No: RCSI changes READ COMMITTED only |
| A print add-on reading invoices and quotes (S) against an Agent job that updates SAP's invoice table directly, or against DI API postings | 3 | READ COMMITTED reader | Only with RCSI in the ERP database |
| Granite readers (cycle-count prescripts, a picking query, a replenishment report) against the trigger's writes to MasterItem, or the Granite app's writes to TrackingEntity | 5 | READ COMMITTED reader | Yes, with RCSI in the Granite database |
| Granite writer against writer: a barcode counter row; a prescript running SERIALIZABLE | 2 | SERIALIZABLE | No |

Victims: SAP users 14, the DI Server 8, Granite operators 7, the print add-on 3, the add-on and Granite's
integration service 1 each. Granite's integration service took part in 11 of the 34.

Two of the deadlocks show the trigger at work. In each, the session was SAP's own `UPDATE OITM SET IsCommited`,
and the deadlock graph's frame was a procedure in the ERP database writing Granite's MasterItem (description,
UOM, category, `AuditDate = GETDATE()`) at transaction depth 3. In other words, a trigger fires on every
committed-quantity change and does a cross-database write inside SAP's SERIALIZABLE transaction.

### The blocking finding

| Read | Value |
|---|---|
| Item import procedure, `sys.dm_exec_procedure_stats` over 50 hours | 1,498 runs (every 2 minutes), 1.16 s average, 3.9 s at most, 1.05 s CPU, 209,000 reads per run, 34 s an hour |
| Its statements, `sys.dm_exec_query_stats` | `max_dop` 1 for every statement |
| Its body | an "item sequence" section: a ROW_NUMBER over every item with a pickface into a table variable, then a cursor that fetches each row and does nothing (its UPDATE is commented out). 8,092 items, so 8,091 fetches a run at 17 reads each, about 0.47 s and 147,000 reads of each run |
| The rest of each run | an INSERT of new divisions that reads all of SAP's OITM (about 0.5 s, 54,000 reads), the INSERT of new items (0.19 s, 7,600 reads) |

## Root causes

1. **SAP B1 postings deadlock each other.** Several writers post at once: users, the DI Server, Granite's
   integration service and an add-on. SAP's transactions run SERIALIZABLE and take the numbering row and the GL
   balances in different orders. No database setting changes this. What helps:
   - fewer concurrent posters (one posting thread in the integration);
   - a retry on deadlock in every integration;
   - bulk runs moved out of peak time.
2. **The trigger on SAP's item table** stretches SAP's transactions and drags Granite's MasterItem into them.
   That is where the Granite-reader deadlocks come from. (Its definition was still to be read when this was
   written.)
3. **Granite readers at READ COMMITTED** meet those writers. RCSI in the Granite database removes this kind.
   Tested locally: the setting follows the database of the table read, not the session's database. A query
   run from database B reading a table in RCSI database A used versions; the reverse waited and timed out.
4. **The BLK01 finding is not blocking between sessions.** The import procedure never ran in parallel
   (`max_dop` 1), so a session blocked by its own parallel threads is ruled out for it. The raw
   system_health reports (blocked and blocking spid, ecid, wait resource) were still to be read when this was
   written. Whatever they show, kakapo's wording was wrong:
   - it counted the head as a session behind itself;
   - it gave the "waits for the user" advice to an Agent job.

## What was done or recommended

- **The item import, rewritten (for the client's test system first).**
  - Changes: the dead section removed, NOT IN replaced by NOT EXISTS, and an `IS NOT NULL` on the group name so
    the result is the same as before.
  - Test: on a local copy built from another client's Granite (about 25,000 items, 19,000 with a pickface),
    4 runs each. Both versions inserted the same items, categories and types. The original took 2.7 s and
    425,000 reads a run; the rewrite took 0.18 s and 4,000 reads.
  - No transaction around the three INSERTs, and no XACT_ABORT. Each INSERT adds only what is missing, so a
    failed one is retried by the next run. An error here must not abort the batch: the job's integration
    procedures run after it in the same step. Tested: a statement error left the next statement in the batch
    running.
  - Two risks the original shares, both tested locally:
    - one NULL MasterItem.Code makes NOT IN insert nothing, ever;
    - one SAP item name longer than Granite's column (200 characters in SAP B1 10.0 against 100 in Granite)
      fails the whole item INSERT, so no new item arrives until it is fixed.
  - Next: building new divisions from Granite's own MasterItem instead of scanning SAP's OITM, after checking
    that it gives the same divisions.
- **RCSI in the Granite database, not the ERP database.**
  - `ALTER DATABASE … SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE` in a quiet minute, with the
    Agent job's schedule disabled.
  - First, ask the vendor and check custom code that reads a counter or queue row and then updates it at READ
    COMMITTED (`READCOMMITTEDLOCK` where it must wait).
  - Tested locally: ROLLBACK IMMEDIATE also killed a session whose current database was the other one but
    which held locks in this one, and its whole transaction, other database included, rolled back. At the
    client, a SAP posting running the trigger at that second would be rolled back.
- **For the ERP side (the SAP partner and the integrators):**
  - fewer concurrent posters and a retry on deadlock;
  - the trigger limited to the columns Granite uses, or replaced by the scheduled sync;
  - the Agent job's direct updates of SAP's invoice table moved to the DI API.

## How to verify

- `SELECT name, is_read_committed_snapshot_on FROM sys.databases WHERE name = N'<granite db>';` returns 1.
- A few working days later:
  - the deadlock read shows no Granite reader against MasterItem or TrackingEntity;
  - TMP03 shows the version store small;
  - DEC01 reads On.
- After the import rewrite: `sys.dm_exec_procedure_stats` for it is under 0.3 s a run, and BLK01 no longer
  names it.

## What it teaches kakapo

Written down 2026-11-06; nothing in kakapo was changed for this case.

1. **DLK01/DEC01 classify a reader by the session's database** (`currentdb`), but RCSI follows the database of
   the resource it waited on. Use the wait resource's database id.
2. **"In 7 days" hid that system_health kept 6 hours.** The deadlock report should show the oldest deadlock it
   still has next to the since-restart counter (34 against 1,662).
3. **Deadlocks with three or more processes** are reduced to the victim and one other, so the third
   participant can drop out (here the Agent job writing SAP's invoices).
4. **BLK01 keys episodes by spid and never uses ecid.**
   - A report whose blocked and blocking spid are the same counts the head as its own victim and shows its
     own statement as the blocked statement.
   - Such a report is not blocking between sessions: drop it, or label it parallelism.
5. **A head with an open transaction gets "waits for the user or a web call"** whatever its status. Say that
   only when the status was sleeping; an Agent job batch that was running gets the wait resource instead.
6. **Severity counts sightings only:** five sub-second sightings, with the head itself as the only "victim",
   made Medium.
7. **The RCSI preview's "n sessions in the database"** counts sessions by their current database. Sessions from
   other databases holding locks in this one are also killed by ROLLBACK IMMEDIATE and are not named. Read
   `sys.dm_tran_locks` by `resource_database_id` too.
8. **DEC01 offers the RCSI fix only at "Worth considering"**, judged per pair of procedures (DLK01's 3). Five
   reader deadlocks spread over several pairs never reach it, so the evidence across pairs is not counted.
9. **WMS/ERP pairs on one instance are common for kakapo's users.** Cross-database blocking and deadlocks, and
   the ERP's own SERIALIZABLE postings, deserve their own wording: say plainly what no setting can fix, and
   who owns it.

## Facts checked

- **Enabling RCSI:** "only the connection executing the ALTER DATABASE command is allowed in the database"
  ([Transaction locking and row versioning guide](https://learn.microsoft.com/sql/relational-databases/sql-server-transaction-locking-and-row-versioning-guide)).
  The local test above showed that "in the database" includes sessions from another database holding locks in
  it.
- **Scope:** RCSI is how the READ COMMITTED isolation level reads (same page, "Isolation levels in the Database
  Engine"). SERIALIZABLE keeps its range locks ("Key-range locking"), which is why the SAP postings are
  untouched.
- **Cost:** up to 14 bytes are added to a row the first time it is changed after row versioning is on, and the
  version store's size is about 2 × version data per minute × the longest transaction in minutes (same page).
  It cannot be measured for a database before the change.
- **system_health** keeps deadlocks in rolling files; on this server they held 6 hours. The cumulative counter
  `Number of Deadlocks/sec` in `sys.dm_os_performance_counters` gives the total since the restart.
- **SAP B1 10.0 column sizes** (SAP's REFDB, via the bundled dictionary): OITM.ItemName nvarchar(200),
  InvntryUom nvarchar(100), OITB.ItmsGrpNam nvarchar(100), unique.
