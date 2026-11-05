# Phase 5f contract: the change log fills itself from the server

kakapo's change log holds what kakapo applied (fixes) and what the consultant typed in by hand (phase 5c).
The changes that matter most to "did it help?" are often neither: a vendor deploys a release, a DBA adds an
index in SSMS, someone alters a procedure. SQL Server already records those in the **default trace**
(Object:Created 46, Object:Deleted 47, Object:Altered 164, the events SSMS's "Schema Changes History" report
reads). This phase reads them into the change log and keeps them in the case file, so they outlive the
trace's rollover. When the trace is off it offers to switch it back on. The Extended Events helper for
servers whose trace holds too few days is phase 5g, built once a real client shows one.

Sources: Microsoft Learn, "Default trace", "sys.fn_trace_gettable", "Object:Altered Event Class",
"ObjectType Trace Event Column"; the live catalog (`sys.trace_subclass_values`, `sys.fn_trace_geteventinfo`).
Microsoft marks the default trace for removal in a future version, with Extended Events as the replacement
(verify the wording on the "default trace enabled" option's page and quote it); that is phase 5g's second
trigger. Facts below were verified live on `localhost\SQL2022` in the review of 2026-11-05.

## 1. The read: one more result set from MNT05's trace read

`fn_trace_gettable` scans every trace file whatever the WHERE says, and the Upkeep read
(`collect/upkeep.go`, the MNT05/MNT06 block) already reads the whole trace on every page load. So the DDL
rows come back as **one more result set of that same query**, from the same `@trace` table (widen its event
list and columns; no second scan). Nothing new is read on the tuning report.

Kept:
- Event classes 46, 47, 164 with `EventSubClass = 1` (commit). Every DDL writes a Begin (0) and a Commit (1)
  row; a rolled-back one writes Begin and Rollback (2), so commit leaves out DDL that never happened.
- User databases only: `DatabaseID > 4`. This drops tempdb (#temp tables and table variables live there),
  master (SQL Server's own telemetry session, `SQLServerCEIP`, writes hundreds of rows there) and msdb.
- Mapped object types only, codes from Microsoft's ObjectType table, each verified against the live trace:
  stored procedure, view, the function types, table, trigger, index (created and dropped only), and the
  database itself (16964, ALTER DATABASE) as a setting. Everything else, statistics (21587, auto-created or
  manual) included, is left out.
- Not kakapo's own connections (`ApplicationName = 'SQLMonitor'`): those are already in the log as fixes.

Left out on purpose:
- **Index alters.** `ALTER INDEX ... REBUILD` and `REORGANIZE` both fire Object:Altered on every index, every
  night; that is upkeep the maintenance plan (phase 5b) already knows, not a design change.
- **The primary key's index of a new table.** CREATE TABLE with a key writes an index row in the same
  transaction: drop index rows that share a `TransactionID` with a table create.

Columns per row: time, database, schema and name, object type, event (created, altered, dropped), login, host,
program. The trace carries `ObjectID` and `ObjectName` but no schema, and for an index `ObjectName` is the
index's name: resolve `OBJECT_SCHEMA_NAME(ObjectID, DatabaseID)` and the parent table's name at read time for
objects that still exist; dropped objects keep the bare name. An index's `object` is `schema.table.index`
(phase 5h follows an index by its table and name). The result set also returns the trace's oldest
event time and whether it runs (already read for MNT05).

**Times are UTC in the change log.** `StartTime` is server local; convert with the server's offset as 5e's
reads do (`DATEADD(MINUTE, @offset, ...)`), so a row lands in the right tuning hour.

## 2. Into the change log

`change` table, no migration: the description carries login, host and program; `result` is the new constant
`ChangeSeenOnServer = "seen on the server"` beside `ChangeRecordedByHand`; `statement` empty.

**One row per person-hour, so a deploy cannot flood the log.** Rows are grouped by (database, login,
program, clock hour in UTC):
- 5 events or fewer: one change row each. `object` = schema.name, `change_type` = PROCEDURE, VIEW, FUNCTION,
  TABLE, TRIGGER, INDEX or SETTING, `description` = "Procedure dbo.GetOrders altered by DOMAIN\jane on
  APP-SRV01 (Microsoft SQL Server Management Studio)".
- More than 5: one row, `change_type` DEPLOY, `object` = "{n} objects", `description` = "Deploy by
  DOMAIN\build on BUILD01 (DacFx): 40 objects (12 procedures, 28 tables)", the names in `statement` one per
  line so Copy carries them.

**Idempotent by hour, no cursor.** Each harvest recomputes every complete clock hour the trace still holds
(the hours after the hour of its oldest event) and, per database and hour, replaces the `seen on the server`
rows with the recomputed ones in one transaction. Hours older than the trace are never touched: that is how
the case file keeps history the trace has rolled off. A deploy that spans a page load comes out as one row
the next time.

**When:** on the database page's load, after the Upkeep read, for every user database in the result (not
only the page's database).

**Shown:**
- Recent changes marks these rows "(on the server)" beside "(by hand)".
- The tuning report lists them in its change select; `performanceChangeTypes` (`domain/tuning.go`) gains
  DEPLOY, FUNCTION and TRIGGER. TABLE stays out (a table created or altered alone is rarely a performance
  change; it is inside a DEPLOY when it comes with a release). "Did it help?" works on them unchanged.
- **The window line** under Recent changes:
  - Trace running: "Changes made outside kakapo are read from SQL Server's default trace, which holds {n}
    days here (since {oldest}). kakapo keeps what it reads, so changes older than that stay here as long as
    this page was opened within {n} days of them."
  - **A gap**: the trace's oldest event is newer than the newest event the previous harvest saw (kept per
    server in `setting`, key `schemachanges.seen.{serverId}`, written after each harvest): "Changes made
    between {previous newest} and {oldest} were not seen: the trace turned over before kakapo was opened
    again." The gap is never silent: kakapo runs on demand and only SQL Server records while it is closed.
  - Trace off: "SQL Server's default trace is off, so changes made outside kakapo are not seen." (CHG01)
  - No ALTER TRACE: the line MNT05 already gives, with the grant to copy.

## 3. CHG01 and the fix `enable-default-trace`

**CHG01** (new check, Upkeep, owner Us). The 7-day bar is the tuning report's "before" window: five working
days, a calendar week (`domain/tuning.go`), so a shorter trace can lose the change the report compares
against.
- Trace off, Medium: "SQL Server's default trace is off, so kakapo cannot see changes made outside it, nor
  shrinks, cache clears and growths (MNT05, MNT06)." Apply: `enable-default-trace`. Copy.
- Trace holds fewer than 7 days, Low: "The default trace holds only {n} days here, less than the week the
  tuning report compares against." Do this: "Open this database's page at least every {n} days so kakapo
  keeps the changes; a busy server turns its trace over fast." Copy. (Phase 5g adds the helper as Apply.)

`enable-default-trace` (server, CHG01, title "Switch SQL Server's default trace back on"):
`EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'default trace enabled', 1;
RECONFIGURE;`. Preview reads the current value and the pending RECONFIGURE values (INS14's warning, as 5e).
Warnings: a server setting (warnServer); "The default trace is SQL Server's own, on by default, at most 100 MB
on disk (5 files of 20 MB) beside the error log; whoever switched it off may have had a reason, so ask
first"; the permission (ALTER SETTINGS). Read back `value_in_use` and `sys.traces WHERE is_default = 1`.
Rollback sets 0. Change row type SETTING.

## Tests

Domain: trace rows to change rows for each mapped type and event; left out: subclass 0 and 2, `DatabaseID`
<= 4, statistics, index alters, kakapo's own, the primary key index of a new table; the UTC conversion; the
group of 5 (single rows) and 6 (one DEPLOY row with its counts and names); two logins in one hour stay apart;
recomputing an hour replaces its rows and leaves older hours alone; the window line in its four states (the gap from a stored newest event later than the trace's oldest);
CHG01's two cases; the fix's statement, read-back and rollback; the new performance types. Collect: the extra
result set is tagged, read-only and comes from the existing `@trace` (no second `fn_trace_gettable`). Store:
the per-hour replace in one transaction. Service: the harvest runs on page load, a second load adds nothing,
the fix is registered. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in SSMS (not kakapo), in a user
database: create an index, drop it, create a table with a primary key, rebuild every index of one table, then
create a procedure and alter it. Open the database page: Recent changes shows the index created and dropped,
the table created (no separate key index row), the procedure created and altered, each with login, host and
program, and nothing for the rebuild; times match the clock. The window line names the trace's days.
Reopening the page adds nothing twice. Run a script of 8 CREATE PROCEDURE statements: one DEPLOY row, Copy
carries the 8 names. The tuning report's change select offers the altered procedure and the deploy; "Did it
help?" centres on the one chosen. CHG01 does not fire (the trace runs and holds 34 days); its fix is checked
in tests only, since switching the trace off on the test server would empty MNT05.
