# Phase 5e contract: the kakapo Extended Events session (the first installed helper)

SCOPE section 3 item 9 allows a fix to create something on the server when that is the best way to get what
a card needs, with a remove fix and an "Installed helpers" line on the instance page. The first such helper
is an Extended Events session that keeps every block over a threshold and every deadlock to a file, the way
Erik Darling's Performance Monitor Lite does, so the blocking history of phase 5d stops depending on
system_health's five-minute snapshot. Source for the session's shape: sp_HumanEvents' blocking session
(`research/DarlingData-main/DarlingData-main/sp_HumanEvents/sp_HumanEvents.sql`, the `blocking` event
session DDL) and Lite's sessions; take the shape, write our own DDL, mark the fragment MIT. Microsoft:
"blocked process threshold (s)" (`sp_configure`), `blocked_process_report` and `xml_deadlock_report` events.

## The fix `install-xe-session` (server, check BLK01, title "Keep every block and deadlock on the server")

Inputs: `thresholdSeconds` (5; 1 to 60: "a block shorter than this is not recorded; 5 is what users notice"),
advanced: `folder` (the server's error log folder, `SERVERPROPERTY('ErrorLogFileName')`'s directory, checked
with `xp_fileexist`; the file target lands beside the error log where every DBA looks), `fileMB` (50; 10 to
500), `files` (4; 2 to 10), `dispatchSeconds` (30; 5 to 60).

Preview reads: whether a session named `kakapo` exists (`sys.server_event_sessions`) and runs
(`sys.dm_xe_sessions`), its target file path and size; the current `blocked process threshold (s)`; the
folder's existence; pending RECONFIGURE values (INS14's warning applies, because the threshold is a server
setting activated by RECONFIGURE); the server's version (file targets and these events exist on 2012+; the
session is refused on Express? no: Express supports Extended Events, so offered).

Changes rows: "Event session kakapo | none | blocked_process_report and xml_deadlock_report to
{folder}\kakapo*.xel, {fileMB} MB x {files}, starts with SQL Server"; "Blocked process threshold | 0 (off) |
{n} s". Warnings, in order: the threshold is a server setting (warnServer) and RECONFIGURE's pending
values; "Every block over {n} s is written to the file with both statements; on a server with constant
blocking the files turn over faster (at most {fileMB x files} MB on disk, never more)"; "The session costs
almost nothing while nothing blocks; the blocked process report is produced by the lock monitor, which
already runs"; "Deadlocks are also kept here, so the deadlock report no longer depends on system_health's
retention"; the permission ("Needs ALTER ANY EVENT SESSION and ALTER SETTINGS: sysadmin"). Blocking: the
folder missing; a session named kakapo that kakapo did not create (its description differs); a bad option.

Statement:

```sql
EXEC sp_configure 'show advanced options', 1; RECONFIGURE;
EXEC sp_configure 'blocked process threshold (s)', {n}; RECONFIGURE;
CREATE EVENT SESSION [kakapo] ON SERVER
    ADD EVENT sqlserver.blocked_process_report,
    ADD EVENT sqlserver.xml_deadlock_report
    ADD TARGET package0.event_file (SET filename = N'{folder}\kakapo.xel', max_file_size = {fileMB}, max_rollover_files = {files})
    WITH (MAX_MEMORY = 4096 KB, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, MAX_DISPATCH_LATENCY = {dispatch} SECONDS,
          STARTUP_STATE = ON, TRACK_CAUSALITY = OFF);
ALTER EVENT SESSION [kakapo] ON SERVER STATE = START;
```

Read back: the session exists and is running, the threshold's `value_in_use`. Rollback: `ALTER EVENT
SESSION [kakapo] ON SERVER STATE = STOP; DROP EVENT SESSION [kakapo] ON SERVER; EXEC sp_configure 'blocked
process threshold (s)', {previous}; RECONFIGURE;`. Change row object "Event session kakapo", type HELPER.
The remove fix `remove-xe-session` runs the rollback and warns that the .xel files stay on disk (names them)
and that the blocking history read falls back to system_health.

## Where it shows

- **The instance page** gains an "Installed helpers" row: the kakapo session (running or stopped, since
  when, the file path, events captured so far from `sys.dm_xe_session_targets`' execution count, the
  threshold), with Remove; or "nothing installed" with the fix's Create. The instance read (`InstanceInfo`)
  gains the helper's state in its own TRY.
- **The blocking history read** (phase 5d) reads kakapo's files first when the session exists:
  `fn_xe_file_target_read_file(N'{folder}\kakapo*.xel')` with `object_name = 'blocked_process_report'`, every
  report in the window, which carries a real duration per block (the report fires at every threshold
  interval while a block lasts, so occurrences become "{n} blocks, the longest {s}"); then system_health's
  snapshots for the days before the session existed. The report says which source each day came from and
  the `missedNote` becomes "kakapo's session keeps every block over {n} s since {date}".
- **The deadlock read** reads kakapo's files first for `xml_deadlock_report`, then system_health.
- **BLK01's Do this** on a server without the session ends with "Install kakapo's session to keep every block
  over 5 seconds (Installed helpers on the instance page)" and carries the fix.

## Tests

Domain: the DDL with defaults and with options, each blocking case, the rollback with the previous threshold,
the read-back. Collect: the reads tagged and read-only; the file-first source selection in the blocking
history. Service: both fixes registered; a bad option gives no Changes. Store: nothing new. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: the instance page shows "nothing
installed"; the fix previews with the error-log folder and applies (session running, threshold 5, pending
warning shown if any); a staged block of 20 seconds appears in the blocking history read with its duration
from kakapo's file; a staged deadlock (two sqlcmd sessions) appears in the deadlock report from kakapo's
file; Remove drops the session and resets the threshold; the files are named in the warning.
