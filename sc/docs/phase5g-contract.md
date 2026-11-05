# Phase 5g contract: the change session (a second installed helper)

**Build only when a real client needs it**: CHG01 (phase 5f) shows a default trace holding fewer than 7 days,
or the site keeps the trace off and will not switch it on, or a SQL Server version without the default trace
(Microsoft marks it for removal; see 5f's sources). On `localhost\SQL2022` the trace holds 34 days.

**kakapo runs on demand; the session does the recording.** Like 5e's, it is installed once, starts with SQL
Server (`STARTUP_STATE = ON`), records while kakapo is closed, and is removed only by the user through
`remove-change-session`, never on exit or by any cleanup. It cannot see changes from before it was installed.

Phase 5f reads schema changes from the default trace, which holds 5 x 20 MB. A busy server turns that over in
days, and the case file only keeps what kakapo read in time. This helper keeps the same changes for months in
a small Extended Events file, and adds the batch that made each change. Sources: Microsoft Learn on the
`object_created`, `object_altered` and `object_deleted` events; the live catalog (`sys.dm_xe_object_columns`,
`sys.dm_xe_map_values`), verified in the 5f review on 2026-11-05.

## The session

A **separate** session from 5e's `kakapo`: DDL events are few and must last months, while blocking reports on
a busy server turn 5e's files over in days. Adding the events to 5e's session would also break
`HasKakapoShape`, which expects exactly two events, for sessions already installed.

```sql
CREATE EVENT SESSION [kakapo_changes] ON SERVER
    ADD EVENT sqlserver.object_created (SET collect_database_name = 1
        ACTION (sqlserver.server_principal_name, sqlserver.client_hostname, sqlserver.client_app_name, sqlserver.sql_text)
        WHERE database_id > 4 AND ddl_phase = 1 AND object_type <> 21587 AND sqlserver.client_app_name <> N'SQLMonitor'),
    ADD EVENT sqlserver.object_altered (...same...),
    ADD EVENT sqlserver.object_deleted (...same...)
    ADD TARGET package0.event_file (SET filename = N'{folder}\kakapo_changes.xel', max_file_size = {fileMB}, max_rollover_files = {files})
    WITH (MAX_MEMORY = 1024 KB, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, MAX_DISPATCH_LATENCY = 30 SECONDS,
          STARTUP_STATE = ON, TRACK_CAUSALITY = OFF);
ALTER EVENT SESSION [kakapo_changes] ON SERVER STATE = START;
```

Verified facts: `ddl_phase` 1 is Commit and `object_type` 21587 is STATISTICS (from the maps);
`client_app_name` works as a predicate source; the event's `database_name` field is empty unless
`collect_database_name = 1`; `sql_text` is the whole batch, not the single statement, so the UI says "batch".

## The fix `install-change-session` (server, CHG01, title "Keep every change made on the server for months")

Inputs, advanced only: `folder` (5e's rule: the error log's folder), `fileMB` (10; 5 to 100), `files` (4; 2 to
10). Preview, warnings, blocking cases, read-back and the remove fix `remove-change-session` follow 5e's
`install-xe-session` exactly, without `sp_configure` (so no RECONFIGURE warning). Warning specific to this one:
"Each change keeps the batch that made it, up to 4,000 characters; a deploy script's text lands in the file."
Change row type HELPER.

## Where it shows

- **5e's file pattern is tightened first.** 5e reads and lists `{folder}\kakapo*.xel`, which would also match
  `kakapo_changes_0_*.xel`. Its pattern becomes `{folder}\kakapo_0_*.xel` (the rollover names SQL Server
  writes), so the blocking and deadlock reads and `remove-xe-session`'s file list see only 5e's files.
- **`ReadHelpers` takes the session name** instead of the six hard-coded `N'kakapo'`; the Installed helpers
  row lists both sessions, each with its own Create or Remove.
- **The harvest** (5f) reads `{folder}\kakapo_changes_0_*.xel` first when the session exists and falls back
  to the default trace for the hours before the session started; same filters, same UTC conversion, same
  per-hour grouping. A single row's `statement` gains the batch (first 4,000 characters); a DEPLOY row keeps
  the names.
- **The window line** (5f) names each source and the hours it covers: "Changes since {installed} come from
  kakapo's change session (running, newest {latest}); before that from the default trace (since {oldest});
  earlier changes are not available." When the session is stopped: say so, and since when, so the gap is visible.
- **CHG01** with a short trace gains Apply: the fix, after the Do this of 5f.

## Tests

Domain: the DDL with defaults and with options; the blocking cases; the rollback; 5e's tightened pattern
(matches `kakapo_0_1336...xel`, not `kakapo_changes_0_...xel`). Collect: `ReadHelpers` for both names; the
harvest's file-first source selection. Service: both fixes registered. Web build.

## Done when

`go test ./...` and the web build pass. Live: install the session; alter a procedure in SSMS; the change row
carries the batch; the Installed helpers row shows both sessions; 5e's blocking read still finds only its own
files; remove the session: its files are named and the harvest falls back to the default trace.
