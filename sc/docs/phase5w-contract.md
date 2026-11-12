# Phase 5w contract: the change audit, "did we cause this?"

Decided on 2026-11-12 from Francois's case note "Kakapo: Where is SQL Server memory going?" (pages 14-22, "Kakapo
feature: change audit" and "Kakapo principle: no setting disappears behind a green tick", and the "Build it into
Kakapo" list on page 24) and from `change-history-today.md`, which answers where kakapo's change history comes
from today. He gave full authority to decide while away; the decisions below are taken, not proposals.

The case: kakapo had just been set up on a Granite production instance whose memory looked full, and nobody could
quickly prove kakapo was not the cause. The question this phase answers, on one screen, for an IT admin who is
not a DBA: **"Did anything kakapo did, or anything anyone else did, line up with this problem?"**

Three sources, one timeline: (A) a settings baseline and the difference between snapshots, which catches changes
whoever made them; (B) kakapo's own log of every action, written before and after, kept for good, locally and on
the server; (C) SQL Server's own history, which 5f already reads, for who and the exact time while it still holds
them. Built in that order: A, then B, then C, the **What changed?** screen.

**Built in two steps** (Francois, 2026-11-12). **Step one**: A (all of it), B1-B4 (the local log, before and
after, seeded from the `change` table) and C (the screen), with nothing written to any server. **Step two**: the
server copy, B5-B7 (`install-change-log`, `remove-change-log`, the catch-up and lost-rows check, the preview
warning, CHG02) and C3's row "kakapo's log on this server removed or emptied". Step one leaves the
`kakapo/change_log` baseline item in (it reads "absent") so step two only adds. Done when below is split the
same way: step one's live test runs the outside-change and kakapo-fix parts only.

SCOPE section 1a rules 1, 2, 4 and 5; section 3 item 9 (this phase makes "recorded in the change log" mean before
and after, with who). The only things written to a server are the log's table, installed by a named fix, and
the log rows of actions the user confirmed.

## What it is not

- **Nothing is purged.** No row this phase adds, locally or on the server, is ever updated or deleted by kakapo.
  The local DuckDB file stays the primary copy.
- **Not a monitor.** Nothing polls. Snapshots are taken when kakapo reads the server (decision A2). A change made
  between two visits is found at the next visit and dated between the two snapshots; SQL Server's trace adds the
  exact time and who, while it still holds them.
- **Not the setting principle across the app** (pages 20-22: current value, recommendation, Change and Revert on
  every setting, and the undo table). That is the next phase, starting with the memory cap. This phase gives it
  the data it needs: who set a value, when, what it was before, and "Changed outside kakapo".
- **Not a schema baseline.** Indexes (keys, INCLUDE, filter, compression, fill factor), lock escalation and
  triggers wait (see "What waits"). 5f already turns DDL from the default trace into change rows.
- **Not the error log.** It holds which database option changed, but not who; reading it needs securityadmin; a
  restore writes dozens of lines. The baseline already says what changed. It waits.

## Decisions

### A. The settings baseline

**A1. What is captured.** One read, `collect.ReadBaseline`, SELECTs only, tagged, each area in its own TRY so a
missing right empties that area and says which right, as every read does. All of it was run read-only on
`localhost\SQL2022` on 2026-11-12.

| Level | Items | Source |
|---|---|---|
| Instance | Every row of `sys.configurations` (name, `value`, `value_in_use`): max and min server memory, MAXDOP, cost threshold, optimize for ad hoc and the rest, 95 rows on 2022. Global trace flags. Memory model (`sql_memory_model_desc`: Lock Pages in Memory). Version and CU (`ProductVersion`, `ProductUpdateLevel`), edition. Start time (`sqlserver_start_time`). | `sys.configurations`; `DBCC TRACESTATUS(-1)` through the existing `traceFlagQuery` (`collect/setup_actions.go:122`); `sys.dm_os_sys_info`; `SERVERPROPERTY` |
| Database, every one | Present or not, state, user access, read only, recovery model, compatibility level, RCSI, snapshot isolation, auto create and auto update statistics and async, auto close, auto shrink, page verify, Query Store on, delayed durability, target recovery time. | `sys.databases`, one query from master |
| Files | Per file: growth, percent or MB, max size; and tempdb's data file count. **Not** sizes or VLF counts: they move by themselves (the Storage card's samples keep sizes). | `sys.master_files` |
| Agent jobs | Per job: present or not, enabled, `date_modified`, `version_number`, enabled schedules, and whether it takes backups (a step with `BACKUP DATABASE`, `BACKUP LOG`, `DatabaseBackup` or `kakapo_Maintenance`, or a job name with "backup"). | `msdb.dbo.sysjobs`, `sysjobsteps`, `sysjobschedules`, `sysschedules` (SQLAgentReaderRole or sysadmin) |
| kakapo's own | Whether `master.dbo.kakapo_ChangeLog` and its trigger exist. | `sys.objects` in master |

Also read on every baseline, but **not** part of the snapshot (they change all the time): SQL Server's memory in
use and its target (`sys.dm_os_process_memory.physical_memory_in_use_kb`, `sys.dm_os_sys_info.committed_target_kb`)
for the memory line (C2).

```sql
SELECT name, CONVERT(bigint, value), CONVERT(bigint, value_in_use) FROM sys.configurations;
SELECT d.name, d.state_desc, d.user_access_desc, d.is_read_only, d.recovery_model_desc, d.compatibility_level,
       d.is_read_committed_snapshot_on, d.snapshot_isolation_state_desc, d.is_auto_create_stats_on,
       d.is_auto_update_stats_on, d.is_auto_update_stats_async_on, d.is_auto_close_on, d.is_auto_shrink_on,
       d.page_verify_option_desc, d.is_query_store_on, d.delayed_durability_desc, d.target_recovery_time_in_seconds
FROM sys.databases AS d;
SELECT DB_NAME(f.database_id), f.name, f.type_desc, f.is_percent_growth, f.growth, f.max_size FROM sys.master_files AS f;
```

`is_query_store_on` is 2016 and later; `delayed_durability_desc` 2014 and later: older versions read NULL. An
AUTO_CLOSE database that is closed reads NULL for some columns (seen live: `delayed_durability_desc` and the log
stats came back NULL for one); NULL is "not read", never a value.

**A2. When it is taken.**
1. **First connect**: the first read of a server that has no baseline. A server kakapo already knows (the Granite
   instance) gets its first one on the first read after the upgrade, and the screen says "kakapo has watched this
   server's settings since {date}; changes before that come only from SQL Server's own history".
2. **Every read**: the instance page, every database page load (beside the Upkeep read), the Memory screen
   (`/instances/:id/memory`, phase 5v) and the What changed? screen. A failed baseline never fails the page.
3. **Before and after every action** (B2), on the action's connection, so a change kakapo made sits between two
   of kakapo's own snapshots and is never mistaken for one made outside it.

**A3. Kept permanently, stored only when something changed.** Migration **14** (13 is phase 5v's):

```sql
CREATE SEQUENCE IF NOT EXISTS seq_baseline START 1;
CREATE TABLE IF NOT EXISTS baseline (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_baseline'),
  server_id INTEGER NOT NULL, taken_at TIMESTAMP NOT NULL, hash VARCHAR NOT NULL,
  values_json VARCHAR NOT NULL,   -- {"item": "value"}, null for not read
  errors_json VARCHAR NOT NULL    -- {"area": "the right that is missing, or the error"}
);
CREATE TABLE IF NOT EXISTS baseline_read (
  server_id INTEGER NOT NULL, read_at TIMESTAMP NOT NULL, baseline_id INTEGER NOT NULL,
  reason VARCHAR NOT NULL,        -- 'first' | 'read' | 'before-action' | 'after-action'
  action_id VARCHAR, sql_memory_mb BIGINT, target_memory_mb BIGINT
);
```

A new `baseline` row is written only when the items differ from the server's latest one (the hash of the sorted
items); every read writes one small `baseline_read` row pointing at the snapshot in force. So "unchanged at
10:42" is proven without storing 600 items per page load: on the test instance a snapshot is about 600 items
(25 KB), a read row about 60 bytes. Item keys are flat and stable: `config/max server memory (MB)`,
`config.pending/max server memory (MB)` (only when `value` differs from `value_in_use`), `db/{name}/recovery_model`,
`file/{db}/{logical name}/growth`, `job/{name}/enabled`, `kakapo/change_log`. The store has no update or delete
for these tables; a test asserts it.

**A4. The difference between two snapshots** (`domain.DiffBaselines`, pure): one `SettingChange` per item whose
value differs, with from, to and the bracket (the read before, the read after). Rules:
- NULL on either side is not a change ("not read"), so a closed AUTO_CLOSE database or a missing right never
  invents one.
- A database or job that appears is "added"; one that goes is "removed"; its items are not listed one by one.
- A new start time is "SQL Server restarted at {time}"; a new version or CU is said as such.
- A `config.pending` item says "set but not yet in use (RECONFIGURE not run)"; the change itself is dated when
  `value_in_use` moves.

**A5. Who made it, in this order.**
1. **kakapo**: an action of B whose item matches (the fix's setting and database), between the two snapshots. As
   kakapo snapshots before and after each action, this bracket is seconds wide. Shown as "Set by kakapo:
   {Windows user} on {host}, {date}".
2. **SQL Server's trace**: a harvested row (A6) or a 5f row for the database (Object:Altered on the database,
   object type 16964) inside the bracket: who (login, host, program) and the exact time.
3. Otherwise: "Changed outside kakapo, between {a} and {b}. SQL Server's trace no longer holds who." Never blank.

**A6. The harvest keeps setting changes too.** 5f's trace read (`collect/upkeep.go:184-262`) already scans the
whole default trace on each database page load. It also keeps event 22 (ErrorLog) rows whose text matches
`Configuration option '%' changed from % to %`, at any age the trace holds (not only MNT06's 7 days), not
kakapo's own program (`kakapo`, `SQLMonitor`), and where from differs from to (every `sp_configure 'show advanced
options', 1` writes "from 1 to 1"). Verified live: the 2022 test instance's trace holds these rows with login,
host and program filled in. They become `seen on the server` rows of the change log through the existing per-hour
replace, with `db_name` NULL (server level), `change_type` SETTING, `object` the option's name, and the
description "max server memory (MB) changed from 2147483647 to 98304 by DOMAIN\jane on APP-SRV01 (Microsoft SQL
Server Management Studio)". The text says the value was set; the baseline says when it came into use. Same
`@trace` table, no second `fn_trace_gettable`; mind the CASE order there, since recent event 22 rows already
match MNT06's branch: a row needed by both is kept for both.

### B. kakapo's own log: permanent, append-only, before and after

**B1. Two rows per action, never an update.** A `started` row before the statement runs, an `ended` row after it
was read back, failed or was stopped, joined by an action id (a UUID). An action with `started` and no `ended`
shows as "outcome unknown: kakapo stopped while it ran". That is the case today's single after-row loses.

**B2. One hook, not forty.** Every action enters through `ApplyFixWithOptions` (`service/setup.go:206`) or
`ApplyQueryStoreFix` (`service/querystore.go:188`). The `started` row is written there, after the preview checks
and before dispatch; the `ended` row when the apply returns, or, for a long fix (4f), when its run ends, is stopped
or is recovered (`service/runs.go`). Each fix keeps writing its `change` row as today, so the tuning report, the
query page, the indexes page and Recent changes are unchanged; the `ended` row carries the ids of the `change`
rows the action wrote (one, or a batch's several).

**B3. What each row records** (the case note's list, page 16):

| Field | From |
|---|---|
| Started, ended (UTC), duration in ms | kakapo's clock |
| Windows user | `os/user.Current()` on the machine running kakapo |
| SQL login | `SUSER_SNAME()` on the action's connection (the server row also defaults to `ORIGINAL_LOGIN()`) |
| Host | `os.Hostname()`, as the overview already does |
| kakapo version | `api.Version`, stamped by `build.ps1`; `main` passes it to `service.New` |
| Server, database, object, fix id, change type, description | The fix |
| Statement, rollback | The preview, exactly as run |
| Value before, value after | The preview's current value; `FixResult.Before` and `After` |
| Outcome, result | verified, differs, failed, stopped or unknown; the result text or the error |

**B4. The local copy is the primary.** Migration 14 also adds:

```sql
CREATE SEQUENCE IF NOT EXISTS seq_action_log START 1;
CREATE TABLE IF NOT EXISTS action_log (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_action_log'),
  action_id VARCHAR NOT NULL, phase VARCHAR NOT NULL,   -- 'started' | 'ended' | 'copied'
  server_id INTEGER NOT NULL, logged_at TIMESTAMP NOT NULL, started_at TIMESTAMP NOT NULL,
  ended_at TIMESTAMP, duration_ms BIGINT,
  windows_user VARCHAR NOT NULL, sql_login VARCHAR NOT NULL, kakapo_host VARCHAR NOT NULL, kakapo_version VARCHAR NOT NULL,
  db_name VARCHAR, object VARCHAR NOT NULL, change_type VARCHAR NOT NULL, fix_id VARCHAR NOT NULL,
  description VARCHAR NOT NULL, statement VARCHAR NOT NULL, rollback_statement VARCHAR,
  value_before VARCHAR, value_after VARCHAR, outcome VARCHAR, result VARCHAR, change_ids VARCHAR
);
```

The same migration seeds it with every earlier action of kakapo's own: one `copied` row per `change` row whose
result is neither "recorded by hand" nor "seen on the server", with the fields the old row has and empty strings
for who, host and version ("before the change audit"). So the Granite instance's earlier actions are in the
audit. Never purged; the store has no update or delete for it.

**B5. The server copy: `master.dbo.kakapo_ChangeLog`** (decided). It sits beside `kakapo_Maintenance` and Ola's
`CommandLog`, which kakapo already installs in master. Not a kakapo schema in each database: it would travel with a
restore onto another server and vanish with a dropped database, the very event the log must outlive. Not a new
database: a bigger footprint on a client's server (files, backups, a name to explain) for one table. Installing
it is a named fix with a preview and a remove fix, as every helper:

- **`install-change-log`** (server, check CHG02, title "Keep kakapo's change log on this server"). Creates the
  table, the trigger and an extended property, then copies every local `action_log` row of this server into it
  (B6's catch-up), so the server shows what kakapo did before the log was there. Warnings: a server object in
  master (warnServer); "one table and one trigger in master; a few KB per action; kakapo never deletes from it, and
  kakapo's maintenance clean-up never touches it"; the permission (CREATE TABLE in master, which sysadmin has).
  Read back: the table, the trigger and the property exist, and the row count matches the local copy.
- **`remove-change-log`**. Drops the trigger and the table. Warning: "Anyone on the server loses the record of what
  kakapo did here. kakapo's own copy on {host} stays complete." Its own action is logged locally (the table is
  gone).
- The maintenance procedure's history clean-up (`maintproc_sql.go:275-287`) and `HistoryCleanupCommand` must never
  name `kakapo_ChangeLog`; a test asserts it.

Verified on `localhost\SQL2022` with `SET NOEXEC ON` (the table, the property, an insert and the reads) and
`SET PARSEONLY ON` (the trigger); nothing was created:

```sql
CREATE TABLE dbo.kakapo_ChangeLog (
    ID                bigint IDENTITY(1, 1) NOT NULL CONSTRAINT PK_kakapo_ChangeLog PRIMARY KEY CLUSTERED,
    ActionID          uniqueidentifier NOT NULL,
    Phase             varchar(8)       NOT NULL CONSTRAINT CK_kakapo_ChangeLog_Phase CHECK (Phase IN ('started', 'ended', 'copied')),
    LoggedAtUtc       datetime2(3)     NOT NULL CONSTRAINT DF_kakapo_ChangeLog_LoggedAtUtc DEFAULT SYSUTCDATETIME(),
    StartedAtUtc      datetime2(3)     NOT NULL,
    EndedAtUtc        datetime2(3)     NULL,
    DurationMs        bigint           NULL,
    WindowsUser       nvarchar(256)    NOT NULL,
    SqlLogin          nvarchar(256)    NOT NULL CONSTRAINT DF_kakapo_ChangeLog_SqlLogin DEFAULT ORIGINAL_LOGIN(),
    KakapoHost        nvarchar(256)    NOT NULL,
    KakapoVersion     varchar(64)      NOT NULL,
    DatabaseName      sysname          NULL,
    ObjectName        nvarchar(512)    NOT NULL,
    ChangeType        varchar(32)      NOT NULL,
    FixID             varchar(64)      NOT NULL,
    Description       nvarchar(2000)   NOT NULL,
    Statement         nvarchar(max)    NOT NULL,
    RollbackStatement nvarchar(max)    NULL,
    ValueBefore       nvarchar(4000)   NULL,
    ValueAfter        nvarchar(4000)   NULL,
    Outcome           varchar(16)      NULL CONSTRAINT CK_kakapo_ChangeLog_Outcome
                      CHECK (Outcome IN ('verified', 'differs', 'failed', 'stopped', 'unknown')),
    Result            nvarchar(4000)   NULL,
    CONSTRAINT UQ_kakapo_ChangeLog_Action UNIQUE (ActionID, Phase)
);
GO
CREATE TRIGGER dbo.kakapo_ChangeLog_AppendOnly ON dbo.kakapo_ChangeLog
AFTER UPDATE, DELETE
AS
BEGIN
    SET NOCOUNT ON;
    THROW 50001, N'kakapo_ChangeLog is append-only: kakapo never changes or deletes its rows. Use kakapo''s remove fix to take the log off this server.', 1;
END;
GO
EXEC sys.sp_addextendedproperty @name = N'kakapo', @value = N'kakapo''s change log: every action kakapo took on this server, before and after',
     @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'kakapo_ChangeLog';
```

**B6. Write order, and what happens when a write fails.**
- **Before**: the local `started` row first; if it cannot be written, the action is refused (nothing runs
  unlogged). Then, when the table is installed, the server `started` row on the action's own connection; if that
  fails, the action is refused with the reason and the way out ("kakapo could not write its log on the server:
  {error}. Fix the permission, or remove kakapo's log from this server"). Always audited, as page 21 says.
- **After**: the local `ended` row always; the server `ended` row as well, and if that fails (the connection
  dropped) the next read puts it there.
- **Catch-up on every baseline read** where the table is installed: kakapo reads the `(ActionID, Phase)` pairs it
  holds locally for this server that the table lacks and inserts them, oldest first. The unique key makes a
  repeat harmless.
- **Rows that disappear.** UPDATE and DELETE are refused by the trigger; TRUNCATE and DROP bypass triggers, and a
  sysadmin can always disable one. So: rows the local copy holds that the table no longer has are put back by the
  catch-up, and the What changed? screen gets a red line "kakapo's log on this server lost {n} rows between {a}
  and {b}; kakapo put back the ones it holds". The table going away outside `remove-change-log` is a red baseline
  change ("kakapo's log on this server was removed").
- **Two kakapo installs on one server** (two consultants, two data files): both write to the same table. The What
  changed? screen also reads the server table and shows the actions it has that the local copy lacks, as "by
  kakapo on {host} ({Windows user})". Those rows are shown, not copied into the local file.

**B7. Before kakapo changes a server that has no log on it** (page 24, first item). Not a block: kakapo's local
log is complete either way, and a client may refuse objects in master. Every fix's preview carries a warning
while the table is not installed: "kakapo's log is not on this server yet, so this change is recorded only in
kakapo's own file on {host}. Install it (one table in master) so anyone on the server can see what kakapo did."
And a check, **CHG02** (Upkeep, Low, owner Us): "kakapo has changed this server, and its log is not on it."
Apply: `install-change-log`. Fires only when `action_log` holds an action for the server and the baseline says
the table is absent.

### C. The What changed? screen

**C1. Where it lives.** `/instances/:id/changes`, titled "What changed?". Linked from the instance page (beside
Memory), from the Memory screen's verdict line ("What changed on this server?"), and from the database page's
Recent changes ("All changes on this server"). Opening it is a read: it takes a baseline (A2), runs the catch-up
(B6) and reads the server table; when the server cannot be reached it shows the local record and says so.

**C2. Top to bottom.**
1. **"When did the problem start?"** An optional date and time, in server time, as Record a change takes it (5c).
   **The verdict line**, one sentence, first:
   - With a time: "No kakapo action and no setting change in the 7 days before {time}." or "{n} changes in the 7
     days before {time}, {r} of them red: {the red ones, by name}." The list then shows those 7 days, with the
     problem's time drawn as a line.
   - Without: "Since kakapo started watching this server on {date}: {n} changes, {k} of them by kakapo."
2. **The memory line**, above the list on the same time axis, so cause and effect line up by date (the case
   note's example: kakapo's rebuild on 28 Oct did not move memory because the 10 GB cap held; removing the cap on
   31 Oct did). SQL Server's memory in use and its target from every `baseline_read` (A1), the cap as a second
   line from the baselines, and 5v's saved memory snapshots as points that open them. Where there is no reading,
   a gap, said in words ("kakapo was not opened between 2 and 9 Nov"). A link to the Memory screen.
3. **The list**, newest first, all sources merged: kakapo's actions (`action_log`, plus the server table's rows of
   other installs), settings differences (A4, attributed by A5), and the change log's rows seen on the server
   (DDL from 5f, settings from A6) and recorded by hand. Each row: when (exact, or "between {a} and {b}"), the flag
   colour, one plain sentence (C3's wording, else the change log's description), who (A5), and the screens it
   affects as links. A fold holds the statement, before and after, the source, and Copy. Filters: All, By kakapo,
   Outside kakapo; Red and amber only. **No cap**: the list is read by time range, 100 rows a page with "Older",
   never `LIMIT n` alone. Server settings show as server settings, whatever database page they were applied from
   (the classification is the fix's scope, `domain/fix.go`, not the row's `db_name`; existing rows are not
   rewritten).
4. **Copy** for a ticket: the verdict, then every listed change as one line each.

**C3. Changes that always light up** (pages 18-19), whoever made them. One table of named rules in `domain`,
with the flag, the sentence and the screens; tested row by row:

| Change | Flag | What to tell the reader | Screens | Seen from |
|---|---|---|---|---|
| max server memory raised or removed | Red | SQL Server can take more RAM, and Windows and other services can be squeezed. | Memory | `config/max server memory (MB)` |
| max server memory lowered | Amber | Less cache, so more disk reads. Also clears the plan cache. | Memory, Performance | same |
| min server memory raised | Amber | SQL Server won't give that memory back to Windows. | Memory | `config/min server memory (MB)` |
| Lock Pages in Memory turned on or off | Amber | Changes how SQL Server's memory shows in Task Manager. | Memory | memory model |
| MAXDOP or cost threshold for parallelism changed | Amber | Query plans change, so CPU and speed can move. | Performance | `config/...` |
| Recovery model changed to full | Red until a log backup has run after it, then grey | The log grows until it's backed up. | Backups, Storage | `db/{name}/recovery_model`; the screen reads that database's last log backup (`msdb.dbo.backupset`, type L) when it opens |
| A backup job disabled or deleted | Red | No safe restore point. | Backups | `job/...` where the job takes backups |
| Auto shrink or auto close turned on | Red | Constant shrinking and fragmentation, slow first queries. | Storage, Performance | `db/{name}/...` |
| Compatibility level changed | Amber | Query plans can change for the whole database. | Performance | `db/{name}/compatibility_level` |
| RCSI or snapshot isolation turned on | Amber | tempdb grows with the version store. | Storage | `db/{name}/...` |
| Query Store turned off, or full (read-only) | Amber | Query history for troubleshooting stops. | Performance | `db/{name}/is_query_store_on`; read-only from the stored `db_query_store` snapshots (databases whose page was opened) |
| A database added to the instance | Amber; red when its name looks like test or dev (5v's rule) | Competes for memory and disk. | Memory | a new `db/{name}` |
| Trace flags changed | Amber | Server-wide behaviour changes. | Performance | trace flags |
| SQL Server version or CU changed | Amber | New behaviour; compare before and after. | Performance | version, CU |
| Service restarted | Grey | The cache starts empty and refills, and usage stats reset. | Memory | start time |
| kakapo index rebuild or reorganise, full-scan statistics, archiving | Grey | Fills the cache with that table. Normal, unless pressure follows. | Memory | `action_log` fix ids |
| kakapo's log on this server removed or emptied | Red | The server no longer shows what kakapo did; kakapo's own copy is complete. | (none) | B6 |

Screens: Memory is `/instances/:id/memory` (5v). The case note's "Log" is the database's Storage card.
Performance and Backups are that database's cards. A server-level change links to Memory only.

**C4. The API.** `GET /api/servers/{id}/changes?from=&to=&page=` returns the verdict, the memory line's points and
one page of merged rows; `problemAt` sets the 7-day window. The baseline, the catch-up and the server-table
read run in it; each failure is a sentence in the answer, never a failed page.

## What waits

- The setting principle across the app (pages 20-22): current value, recommendation with its reasoning, status
  in words, Change and Revert on every setting kakapo applies, starting with the memory cap. Next phase, from the
  fix-by-fix review in `settings-review.md`; it reads this phase's `action_log` and `DiffBaselines`.
- The undo table (page 22) and the review of every action against it.
- The schema baseline: indexes with keys, INCLUDE, filter, compression and fill factor; lock escalation per table;
  triggers. It needs each database's context and is heavy on a database with thousands of objects.
- Query Store size and state per database in the baseline (needs each database's context); this phase uses the
  `db_query_store` snapshots already kept.
- A backup job **failing** (a state, not a change; the Backups card flags it today), and the last full,
  differential and log backup per database.
- The error log (why: "What it is not").
- An extended property on every object kakapo creates (page 16). This phase puts one on `kakapo_ChangeLog`; the
  rest are already named `kakapo`.

## Done when

- Unit tests (pure `domain`): `DiffBaselines` (NULL is never a change, a database or job added and removed, a
  restart, a version change, a pending value); A5's attribution order; A6's parse (from = to skipped, kakapo's own
  skipped, the server-level row); C3 row by row, with recovery to full going grey after a later log backup; the
  verdict with and without a problem time; the outcome of each kind of action end.
- Store: migration 14 creates the three tables and seeds `action_log` from kakapo's own `change` rows only; a read
  with the same items adds a `baseline_read` row and no `baseline` row; no update or delete exists for the three
  tables.
- Service: the `started` row is written before the statement (a fix whose statement fails has `started` and a
  failed `ended`); a local `started` failure refuses the action; a server `started` failure refuses it; a server
  `ended` failure is put there by the next read; a long run's stop and recovery write `ended`; the preview warns
  when the server log is absent; CHG02.
- Collect: the baseline read is tagged and SELECT-only, an area per TRY; the event 22 rows come from the existing
  `@trace` (no second `fn_trace_gettable`); the maintenance clean-up never names `kakapo_ChangeLog`.
- A live test (`SQLMONITOR_TEST_HOST`, `SQLMONITOR_TEST_5W=1`) on `.\SQLEXPRESS`, the shared test instance where
  writes are allowed: install the log; change cost threshold for parallelism in sqlcmd (outside kakapo); open the
  screen: the change is listed with the login, host and program from the trace and dated between two reads; set
  it back with kakapo's fix: `started` and `ended` locally and in `master.dbo.kakapo_ChangeLog`, with before and
  after; a DELETE on the table is refused by the trigger; a TRUNCATE is put back by the next read and flagged red;
  remove the log: the local record is complete. On `localhost\SQL2022`, read-only: the baseline read, the diff of
  two reads, and the screen.
- Checked in the app (and the mock): the screen at desktop and phone width (no horizontal scroll), the memory line,
  a problem time, Copy.
- `go vet`, `go test ./...` and the web build pass.

## Built

**Step one, 2026-11-12.** A, B1-B4 and C as above; nothing is written to any server's master (the
`kakapo/change_log` item reads "absent"). Step two (B5-B7, CHG02, C3's "log removed" row) is below.

Where it is: `collect/baseline.go` (`ReadBaseline`, `ReadTraceHarvest`, `LastLogBackups`, `SQLLogin`), the event 22
branch in `collect/upkeep.go`; `domain/baseline.go` (items, `DiffBaselines`, headlines) and `domain/audit.go` (A6's
parse, A5's `AttributeSettingChange`, C3's `AuditRules`, the rows, the verdict, the memory line); `store/audit.go`
and migration 14 in `store/store.go`; `service/audit.go` (baselines on reads, `startAction` and `endAction`),
`service/changes.go` (the screen); `api/changes.go`; `web/src/screens/changes.tsx`, `api-changes.ts` and its mock.

Decided while building:
- **Item keys.** A database, file or job is a group with a `present` item (`db/{name}/present`, ...), so a name
  that holds "/" still parses (the prop is always the last part). `server/cu` is `ProductUpdateLevel`, else
  `ProductLevel` ("RTM"). File growth and maximum size are said in words ("64 MB", "10%", "none", "unlimited").
  `server/start_time` is SQL Server's own (server-local) value, stable between restarts. The hash covers the
  items and the names of the areas not read, so an area that starts or stops failing is a new snapshot.
- **The trace's text starts with the error log's time and spid** ("2026-11-12 12:49:16.95 spid85 Configuration
  option ..."), seen live on 2025 Express: the `LIKE` and the parse are not anchored at the start.
- **The What changed? screen also harvests the trace** (the instance part of the Upkeep read, as master's: still
  one `fn_trace_gettable`), so who made a setting change is known without opening a database page first.
- **Baselines on reads**: the instance page's in the installed-helpers read (it always loads), the database
  page's beside the Upkeep read, the Memory screen's live read, and the What changed? screen. Not the database
  picker (the instance page already takes one).
- **Order of an action**: the snapshot before (reason before-action, with the action id), the started row, the
  statement, the ended row, the snapshot after, on the action's connection, or a fresh one when it is gone (a lost
  long run). A started row that cannot be written is a 409, "kakapo could not write its own log of this change, so
  nothing was run". An ended row that cannot be written is logged; the action has run. Stopped wins over failed
  and verified.
- **The change rows of an action** are collected through the request's context: every fix's `change` row now goes
  through `Service.insertChange`, which notes the id on the action in progress; `FixResult.ChangeID` is added if
  missing. The rows themselves are unchanged.
- **A5's kakapo match** is the bracket (the action started between the two reads, or the later read is its
  after-action snapshot) and the fix's items (`actionItems`: the setting fixes name their item; index, plan,
  statistics, session and CHECKDB fixes never take a setting; an unknown fix is matched by the bracket alone). A
  trace row may sit 2 minutes outside the bracket (two clocks). A difference attributed to an action or a trace
  row carries its time and who, and that action or row is not listed again.
- **Who for a restart or version change** is "Outside kakapo, between the two reads". A row seen in the trace says
  what changed in its headline and who on its own line ("Deploy: 32 objects (...)", "By {login} on {host}
  (SQLCMD), from SQL Server's trace").
- **action_log.db_name** keeps the page's database; a server setting is shown as one by the fix's scope (a copied
  row without a fix id: by its object naming a server fix's setting). Copied rows take `logged_at` = the old
  change's time (no time-zone-safe "now" in the migration's plain SQL), the outcome from the result's text, and the
  change's id.
- **The screen**: a problem time is server time (as Record a change), placed with the offset the harvest read
  (UTC, with a note, when the server cannot be read); the window is the 7 days before it. Filters and pages are
  server-side, 100 rows a page, newest first, Newer and Older; the query lives in the URL. Copy holds the verdict
  and every row of the window under the filter (all pages), times in UTC. The verdict's tone is red or amber by
  its rows, green for "No kakapo action and no setting change". Each database's last log backup is read when the
  screen opens; when msdb cannot be read, recovery to full stays red and a note says why. Query Store going
  read-only comes from consecutive `db_query_store` snapshots.
- **The memory line**: at most 600 readings, evenly kept; a reading is a dot while there are 60 or fewer (one
  reading draws no line); a gap of more than 48 hours is said in words; a Memory screen snapshot opens that screen
  at `?snapshot=N` (added there). The change ticks under the line take their row's flag colour.
- `service.New` takes the version; `main` passes the build stamp.

Proven live (2026-11-12):
- `.\SQLEXPRESS` (2025 Express), `TestPhase5wLive`: cost threshold changed 5 to 4 in sqlcmd; the screen listed it
  as "By {login} on {host} (SQLCMD), from SQL Server's trace", at the trace's time, between kakapo's two reads,
  amber; kakapo's fix then set 4 to 50 with a started and an ended row (Windows user, SQL login, host, version,
  before 4, after 50, verified, its change id, the duration), and the reads were first, read, before-action,
  after-action, read. The value was put back to 5 (and show advanced options to 0) with sqlcmd.
  `TestPhase5wReadOnlyLive` passed there too.
- `localhost\SQL2022` (2022 Developer), read-only: `TestPhase5wReadOnlyLive` (every area read, 499 items; two reads
  on one snapshot and an empty diff; the screen with and without a problem time). The running app's screen there,
  at desktop width and at 375 px (no horizontal scroll with every fold open), with a problem time, Copy, paging to
  101-200 of 226, the instance page's button and the Memory screen's link.

Not proven: the upgrade of a data file with real fix rows (the seed is unit-tested on a migrated file; the app's
own data file here had none); a closed AUTO_CLOSE database's NULLs, a login without msdb or VIEW SERVER STATE,
versions before 2016 (the column fallbacks); a long run's ended row against a live server (unit-tested only); a
Query Store going read-only.

**Step two, 2026-11-12.** B5-B7, CHG02 and C3's "log removed" row, as above.

Where it is: `domain/changelog.go` (the contract's DDL, `BuildInstallChangeLogFix`, `BuildRemoveChangeLogFix`, the
read-backs, B7's warning, the catch-up's arithmetic `ChangeLogCatchUp`, `ServerOnlyActions`, CHG02 and the Installed
helpers row); `collect/changelog.go` (`ReadChangeLog`, `ReadChangeLogKeys`, `ReadChangeLogRows`, `WriteChangeLogRow`);
`service/changelog.go` (the two fixes, `catchUpChangeLog`, `writeServerRow`, `warnNoChangeLog`, `chg02Facts`), with the
hooks in `service/audit.go` (`takeBaseline`, `startAction`, `endAction`); C3's rule and the rows in `domain/audit.go`.

Decided while building:
- **A server `started` row that cannot be written refuses the action** (409), with "kakapo could not write its log on
  the server: {error}. Fix the permission, or remove kakapo's log from this server". The refused attempt stays on
  record locally: its `started` row and an `ended` row, outcome failed, with that sentence. Where the table is not
  installed nothing changes. A failed server `ended` row never blocks: it is logged, tried once more on a fresh
  connection, and put there by the next read.
- **Phases keep their names on the server.** The catch-up copies each row with its own phase (`started`, `ended`,
  `copied`), not all as `copied`: the unique key (ActionID, Phase) would refuse an action's second `copied` row.
  `LoggedAtUtc` is the table's default, when the row reached the server, so a row the catch-up put there shows a
  `LoggedAtUtc` later than its `StartedAtUtc`.
- **The write** is one batch: nothing when `OBJECT_ID` finds no table, `WHERE NOT EXISTS` on the unique key (a
  duplicate-key error from a race is taken as "already there"), `SqlLogin` from the row (`ORIGINAL_LOGIN()` only when
  kakapo recorded none), times sent as text (style 126), text cut to the column sizes. It is the one write beside
  `ExecFix`, and touches nothing else.
- **The catch-up runs inside every baseline** whose `kakapo/change_log` reads present (page reads, the screen, before
  and after every action). Per server, this process's catch-ups and direct writes take turns. kakapo keeps, in the
  settings table (`changelog.sync.{server id}`), how many of its own rows it knows the table holds and when it last
  saw them all. A catch-up that finds fewer records a loss: n rows, between that time and now. The screen lists each
  loss red ("kakapo's log on this server lost {n} rows between {a} and {b}; kakapo put back the ones it holds").
  Only kakapo's own rows are counted: another install's lost rows are not.
- **C3's "log removed"**: `kakapo/change_log` going from present to absent is red, with no screens. When A5
  attributes it to kakapo's `remove-change-log`, it is grey: "Removed with kakapo's remove fix; kakapo's own copy is
  complete."
- **install-change-log** creates only what is missing: the table, the trigger (or `ENABLE TRIGGER` when it is
  disabled), the property. Then it copies kakapo's log of the server and reads back. It is verified when all three
  exist, the trigger is on, and the table holds every one of kakapo's rows. Its rollback is the remove statement.
  Warnings: the server object, the size and "never deleted" line, that a failed `started` write refuses a change
  from then on, and the permission. **remove-change-log** drops the trigger and the table, with the contract's
  warning, naming kakapo's machine. Its `started` row reaches the table before the drop; its `ended` row is local only.
- **The baseline's item** also reads "present, its trigger disabled" (`master.sys.triggers.is_disabled`).
- **B7's warning** goes last on every preview: the generic fixes, and the Query Store fix's own preview. It is not
  on the change log's own fixes, and not added when the table's state cannot be read.
- **CHG02** (Low, rank 4, Us) fires when `action_log` holds a `started` or `copied` row for the server and one
  `OBJECT_ID` read beside the Upkeep read finds no table. When either cannot be read it stays quiet. Its fix is
  offered on the Upkeep card.
- **The instance page's Installed helpers** has a row for the log: Create when absent; Repair when the trigger or
  the property is missing or the trigger is disabled; Remove when whole.
- **Two installs**: the screen reads the whole table and lists the actions the local log lacks as "By kakapo on
  {host} ({Windows user}), from kakapo's log on the server" (source "server log"). They are shown, not copied.

Proven live (2026-11-12):
- `.\SQLEXPRESS`, `TestPhase5wStepTwoLive`, run twice:
  - A fix's preview carried B7's warning while the log was absent.
  - `install-change-log` was verified: the table, the trigger (on) and the property were created, and the table
    held kakapo's rows.
  - With the log installed, the preview no longer warned.
  - cost threshold was set 5 to 4 in sqlcmd and listed from the trace. kakapo's fix set it 4 to 50, and its
    `started` and `ended` rows were in kakapo's log and in `master.dbo.kakapo_ChangeLog`: before 4, after 50,
    verified, with the Windows user, login, host, version and duration.
  - A `DELETE` and an `UPDATE` were refused by the trigger (error 50001).
  - After a `TRUNCATE`, the next read put back every row and the screen flagged "lost 4 rows" red.
  - `remove-change-log` was verified: the table is gone, kakapo's log holds all three actions with `ended` rows,
    the removal is listed grey as kakapo's, and CHG02's facts read "acted, absent".
  - The test put cost threshold back to 5 (show advanced options 0). `.\SQLEXPRESS` is left as found: no log
    installed.
- `localhost\SQL2022`, read-only:
  - `TestPhase5wReadOnlyLive` with `install-change-log`'s preview: 4 changes and 4 warnings. Nothing was created
    (checked afterwards with sqlcmd).
  - In the running app: the Installed helpers row "not installed" with Create, its preview dialog (not applied),
    and the cost threshold preview's last warning (B7).
- The mock screen at 375 px with a loss row and another install's action: no horizontal scroll with every fold
  open.

Not proven live (unit-tested only):
- A server `started` write refused by a missing permission.
- A server `ended` row put back by the next read.
- Repairing a missing or disabled trigger.
- Two kakapo installs on one server.
- A long run's `ended` row on the server.
- CHG02 on the Upkeep card in the app (its facts were read live).
- An install whose catch-up copies many rows (live, it copied one).
