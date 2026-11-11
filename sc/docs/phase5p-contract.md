# Phase 5p contract: the maintenance plan as one procedure (SQL Server Express)

Draft 2026-11-10, for agreement before building.

SQL Server Express has no SQL Server Agent. Today the maintenance plan row (phase 5b) says so, and each
line's Create only gives a script to schedule by hand. This phase installs the whole plan on the server as
**one stored procedure**. Any scheduler then calls it the same way, again and again:

```sql
EXEC master.dbo.kakapo_Maintenance;
```

The procedure decides for itself what is due. It does not depend on how often or exactly when it is
called, so Windows Task Scheduler, the application's own scheduler, or anything else that can run a SQL
statement will do. It is an installed helper (SCOPE section 3 item 9, as phase 5e): created through the fix
pattern, listed under Installed helpers, and taken away by a remove fix.

## Decisions to confirm

1. **What calls it.** Recommended: something that calls it **every 15 minutes**. A call does whatever is
   due, so a 15-minute call gives 15-minute log backups and still runs the nightly work once a night. A
   once-a-night call also works, but then the log is backed up only once a day. That is fine in SIMPLE
   recovery, but in FULL it is a day of data you could lose: the preview says so.
2. **Where it lives.** Recommended: in `master`, as Ola Hallengren's procedures do. Not in the
   application's database: it would travel with every restore of that database, and a vendor upgrade may
   object to objects it did not create.
3. **One database or all of them.** One settings row per database (the fix adds the database it is applied
   from), so one procedure serves every database on the server.

## What gets installed (all in master, all named kakapo_*)

| Object | What it is |
|---|---|
| `dbo.kakapo_MaintenanceSettings` | One row per database: the backup folder, backup retention (days), the full-backup time, log minutes, the integrity-check day and time, the statistics time, index work on or off, the cleanup day and time, how many days of history to keep, and `Enabled`. |
| `dbo.kakapo_MaintenanceLog` | One row per task run: database, task, started, ended, outcome (`OK` / `Failed` / `Skipped`), error number and message, the command, and the file for a backup. This is what kakapo reads to show what ran. It is also where the planned nightly summary email (`docs/nightly-summary-email.md`) would read last night's results. |
| `dbo.kakapo_Maintenance` | The procedure, signed by the certificate below. |
| Certificate `kakapo_MaintenanceCert` | Signs the procedure. Made with a throwaway password; its private key is removed after signing, so nothing else can ever be signed with it. A new one at every apply. |
| Login `kakapo_MaintenanceLogin` | Made from the certificate, in `sysadmin`. Nobody can log in with it: it only lends its rights to the signed procedure while it runs. |
| Master user for `NT AUTHORITY\SYSTEM` | Made (and marked as kakapo's with an extended property) only when SYSTEM has none; it gets `EXECUTE` on the procedure and nothing else. |

## Signing: the scheduled task runs as SYSTEM, which is not sysadmin (added 2026-11-10)

The Task Scheduler command runs `sqlcmd -E` as `NT AUTHORITY\SYSTEM`. SYSTEM has not been sysadmin since SQL
Server 2012, so a call as SYSTEM could not back up, check or read another database. The procedure is
therefore **signed with a certificate** whose login is sysadmin, and SYSTEM is given `EXECUTE` on the
procedure only. While the signed procedure runs, the caller's security context carries the certificate
login's token, so the procedure (and its dynamic SQL) has sysadmin's rights and the caller has none.

Sources: Erland Sommarskog, "Packaging Permissions in Stored Procedures" (https://www.sommarskog.se/grantperm.html):
the certificate, the login from it, `ADD SIGNATURE`, and `ALTER CERTIFICATE ... REMOVE PRIVATE KEY`; Microsoft
Learn, "Module signing (Database Engine)" and "ADD SIGNATURE (Transact-SQL)".

- **Why sysadmin.** Every task reaches other databases (BACKUP, DBCC CHECKDB, statistics), msdb (the history
  cleanup) and `xp_create_subdir` / `xp_delete_file`, which only sysadmin may run together.
- **Re-applying.** `ALTER PROCEDURE` drops the signature, and with its private key gone the old certificate
  cannot sign again. So every apply drops the old signature, login and certificate and makes new ones, in
  one transaction, so a sysadmin login without a signature never stays.
- **Nested calls, proven live as SYSTEM** (a signed probe, then the procedure itself, on SQL Server 2022 and
  2025 Express): `xp_create_subdir`, `BACKUP DATABASE`, `BACKUP LOG`, `RESTORE VERIFYONLY`, `xp_delete_file`,
  `sp_updatestats`, `DBCC CHECKDB`, `msdb.dbo.sp_delete_backuphistory`, `sp_getapplock`, and reads of msdb
  and `sys.database_recovery_status` all carry the certificate's token. **`msdb.dbo.sp_purge_jobhistory`
  does not:** it checks the caller's role itself and refuses (error 14392). The cleanup therefore deletes the
  old rows of `msdb.dbo.sysjobhistory` itself, in the signed procedure's dynamic SQL, instead of calling it.
  Ola Hallengren's procedures were not there to prove: unproven.
- **What SYSTEM can do**, proven as SYSTEM: execute the procedure. A plain `BACKUP`, `DBCC CHECKDB`, reading
  the database, `xp_delete_file`, reading the procedure's log, changing its settings and altering the
  procedure are all refused.
- **A procedure changed by hand** loses its signature: the read-back, the plan row and the Installed
  helpers row say it is not ready for the scheduled task, and applying the fix again signs it anew.

## What the procedure does on each call

For each enabled settings row (each database), in this order, the task that is due:

| Task | Due when | Command |
|---|---|---|
| Integrity check | weekly on the check day, from the check time | `DBCC CHECKDB ([db]) WITH NO_INFOMSGS, ALL_ERRORMSGS` (PHYSICAL_ONLY from 200 GB, as the CHECKDB fix does); Ola's `DatabaseIntegrityCheck` when installed |
| Statistics | daily except the check day, from the statistics time | `sp_updatestats` in the database; Ola's `IndexOptimize` (statistics only) when installed |
| Index work | weekly, **off by default** as in 5b | only when Ola's `IndexOptimize` is installed, with 5b's thresholds (as changed in phase 5q: Brent Ozar's 50% / 80%, never an offline rebuild) |
| Full backup | daily, from the full-backup time | the backup schedule fix's native statement (CHECKSUM, verify, and old files deleted with `xp_delete_file` past the retention); Ola's `DatabaseBackup` when installed. No COMPRESSION: Express cannot compress. |
| Log backup | every call, at least `logMinutes` after the last one, in FULL or BULK_LOGGED recovery, once a full backup exists | the same, as a log backup |
| History cleanup | weekly, from the cleanup time | 5b's `history-cleanup` statement (msdb backup history, and CommandLog when Ola's is there), plus kakapo's own log beyond the kept days |

The schedule is the one 5b proposes, so the same times read the same on every edition: check Sunday 01:00,
statistics 01:00 on the other nights, index work Sunday 03:00, cleanup Sunday 05:00, full backup 22:00, and
the log every 15 minutes.

Rules:

- **"Due"** means: the latest scheduled time is in the past, and the log has no successful run that started
  after it. A call that comes late still does the work, so a server that was off at 22:00 backs up at its
  next call.
- **Heavy work waits for the night.** The check, statistics and index work run only within 6 hours of their
  scheduled time. Missed ones wait for the next night, not the working day, and their row in the log says
  "Skipped: outside the window". Backups always catch up.
- **No two calls do the same work.** `sp_getapplock` takes one lock per database for the heavy work and the
  full backup, and another for the log backup. A log backup can still run while a long check is busy, and a
  second call that finds the heavy lock taken skips that work.
- **A failed task does not stop the rest.** Its error is written to the log, and the next task runs. At the
  end, when any task failed, the procedure raises an error (severity 16) naming the failed tasks, so the
  scheduler sees a failure (`sqlcmd -b` returns 1).
- **Every statement is tagged** `/* kakapo */`, as every fix is.

## The fix `install-maintenance-proc` (server, MNT03; offered on Express)

**Inputs.** The 5b plan's inputs:

- the backup `folder` (required, checked with `xp_fileexist`, defaulted as the backup fix does);
- `keepBackupDays` (7);
- the times, under Advanced;
- index work (off, and offered only when Ola's IndexOptimize is installed).

**Preview.**

- Changes rows:
  - the three objects;
  - the settings row for this database;
  - one row per task, with its schedule in words.
- Warnings, in this order:
  1. Nothing runs until something calls the procedure. The preview gives the Windows Task Scheduler command
     to copy:

     ```
     schtasks /Create /TN "kakapo maintenance" /SC MINUTE /MO 15 /RU SYSTEM /TR "sqlcmd -S {server} -E -b -Q \"EXEC master.dbo.kakapo_Maintenance\""
     ```

     It adds that the task runs as `NT AUTHORITY\SYSTEM`, which may EXECUTE this one procedure and nothing
     else (see Signing). When SYSTEM has no login on the server, a warning says so: the task could not connect.
  2. In FULL recovery, call it every 15 minutes, or the log is backed up less often.
  3. The backup folder must let the SQL Server service account write.
  4. Express cannot compress backups.
  5. The free-space warning before a change that uses space (5l), for the backups.
  6. The signature: the procedure runs with a certificate login that is sysadmin, whose private key is
     gone; changing the procedure by hand removes the signature, and applying again signs it anew.
  7. The permission needed: sysadmin.
- Two more changes rows: **Signature** (a new certificate, its login in sysadmin) and **`NT AUTHORITY\SYSTEM`
  in master** (EXECUTE on the procedure only).
- It blocks when:
  - the folder is missing;
  - a `kakapo_Maintenance` exists that kakapo did not create;
  - an option is bad.

**Statement.** The CREATE TABLE and CREATE PROCEDURE batches, the signing batch, SYSTEM's EXECUTE, then the
settings row. On a server where it
is already installed, the same fix updates the procedure in place and upserts this database's settings row:
adding a second database is the same Apply.

**Run now** (an option, off by default): calls the procedure once after the install, so the first full
backup is taken at once.

**Read-back:**
- the objects exist;
- the settings row matches;
- the procedure is signed by the certificate (`sys.crypt_properties`), the certificate has no private key,
  its login is in sysadmin (`sys.server_role_members`), and SYSTEM may EXECUTE the procedure;
- with Run now, the log's rows.

**Change row:** object "Maintenance procedure", type HELPER.

**Rollback** (and the fix `remove-maintenance-proc`):
- drop the procedure and both tables, the certificate's login and the certificate, and SYSTEM's master user
  when kakapo made it (else only its EXECUTE goes, with the procedure);
- with an "only this database" option, delete just its settings row instead;
- warn that the backup files stay on disk and that the scheduled task must be deleted by hand (the
  command is given).

## Where it shows

**The Upkeep card's maintenance plan row on Express.**

- Today it shows seven "missing" lines with copy scripts. Now:
  - one button: **Install the maintenance procedure**;
  - once installed, each line reads its last run and outcome from `kakapo_MaintenanceLog`, where other
    servers read the Agent's job history.
- A piece that should have run and has no log row says so: "Not run since {time}: is the scheduled task
  calling it?". This is the warning that matters most, because on Express nothing else notices.
- BAK01, BAK02 and BAK03 see the procedure's backups and checks as covered.

**Installed helpers** on the instance page:
- the procedure, the databases it serves, its last call and its last failure;
- with Remove.

## Tests

- **Domain:**
  - the generated T-SQL for each task, native and Ola;
  - the "due" and "window" rules on fixtures, written as a table of times and log rows;
  - the preview's warnings and blocks;
  - the plan row's status words from log rows, including "not run since".
- **Collect:** the read of the settings and log tables, tagged and read-only.
- **Service:** both fixes registered.
- **Web:** the build.

## Done when

`go test ./...` and the web build pass. Live (`TestMaintProcLive`, `internal/service/maintproc_live_test.go`)
on `localhost\SQL2022` with a throwaway database, and on `.\SQLEXPRESS` (SQL Server 2025 Express) with its
test database only (`SQLMONITOR_TEST_MAINT_DB`). After the install, every call is made as the scheduler makes
it: `EXECUTE AS LOGIN = N'NT AUTHORITY\SYSTEM'`. The checks:

1. Install it, with Run now: a full backup and a CHECKDB-or-statistics row in the log; the procedure signed.
2. A second call within 15 minutes does nothing heavy and takes a log backup.
3. A call with the heavy lock held by another session skips the heavy work; the next call runs it.
4. Applying again signs it with a new certificate; then a full backup and statistics run as SYSTEM.
5. A failing task (the backup folder on a drive that is not there; a read-only flag does not stop SQL Server
   writing) writes Failed, the call raises, and the other tasks (the cleanup) still run.
6. SYSTEM can do nothing else (a plain BACKUP, CHECKDB, reading the database, `xp_delete_file`, the
   procedure's tables, altering it: all refused).
7. The plan row (built as on Express) shows the last runs; the Installed helpers row lists the procedure.
8. Remove leaves nothing in master: no procedure, tables, certificate, login, or SYSTEM user kakapo made.

All passed on both instances on 2026-11-10, so Express's backups without compression and `xp_delete_file` on
Express are proven. Still unproven: Ola Hallengren's branches (not installed on either instance), the
physical-only check from 200 GB, a real Windows Task Scheduler task (the calls were made with `EXECUTE AS
LOGIN`, which is how SYSTEM's rights were tested), and kakapo's own screens against Express, which it reaches
only over TCP (the live test reaches a TCP-less Express through the driver's shared-memory package).
