# Phase 4d contract: the remaining actions on Setup, Storage, Upkeep and Backups

SCOPE section 3 item 10 applied to the other cards: every finding that has a mechanical remedy gets Apply.
Every fix goes through the registry of `docs/phase4c-contract.md` section 0 (`registerFix` from its own
file), the preview/confirm/apply/read-back/change-row pattern, inputs where there is a choice, Copy script in
the dialog. A finding keeps its Copy for the rest. Two parts, each one agent, with their own files.

Shared rule for Agent jobs: created as the backup schedule creates them (`docs/phase3g-contract.md`: owner sa,
category Database Maintenance, description "Created by kakapo…", `/* kakapo */` in each step, inside a
transaction, a remove in the rollback), reusing the job-building helpers of `domain/backupschedule.go` where
they fit (extract a `jobbuild.go` if that keeps both readable).

## Part 1: Setup and Upkeep

| Fix | Finding | Scope | Options | Preview and apply |
|---|---|---|---|---|
| `multi-user` | DBC13 | database | none | `ALTER DATABASE [db] SET MULTI_USER WITH ROLLBACK IMMEDIATE`. Warning: whoever set single-user mode may be mid-restore or repair; ask first. Read back `user_access_desc`. Rollback: the mode it had. |
| `reconfigure` | INS14 | server | `action` select: `activate` (default) or `revert` | activate: Changes one row per pending setting "{name} | {in use} -> {pending}", statement `RECONFIGURE` (`RECONFIGURE WITH OVERRIDE` never), warning that it activates all of them at once and clears the plan cache for some. revert: `EXEC sp_configure '{name}', {in use}` per setting, statement with no RECONFIGURE needed (a value set back equals the running value), Changes rows "{name} | pending {x} | stays {y}". Read back `value <> value_in_use` empty. |
| `trace-flag-off` | INS15 | server | `flag` (the finding's flag, passed in the fix's options) | `DBCC TRACEOFF({n}, -1)`. Warning: "If the flag is in the startup parameters (-T{n}), it comes back at the next restart: copy this for IT to remove it in Configuration Manager." Read back `DBCC TRACESTATUS`. Rollback `DBCC TRACEON({n}, -1)`. Unknown flags too. |
| `scoped-config-reset` | DBC14 | database | `name` (the configuration) | `ALTER DATABASE SCOPED CONFIGURATION SET {name} = {default}` with the defaults table of INS/DBC14; MAXDOP to 0. Read back the row. Rollback the previous value. |
| `min-server-memory` | INS16 | server | none | `sp_configure 'min server memory (MB)', 0` + RECONFIGURE (through `spConfigure`), the pending warning of INS14 applies. Lightweight pooling and affinity stay Copy (a restart and a decision). |
| `operator-alerts` | MNT07 | server | `operator` (name, default "Granite support"), `email` (required, validated as an address), `alerts` bool default true | Creates the operator when none has an email (`sp_add_operator` with the email, enabled), then each missing alert of the scan's list (severity 19 to 25, errors 823, 824, 825: `sp_add_alert` with `@include_event_description_in = 1`, `@delay_between_responses = 60`) and its notification (`sp_add_notification … @notification_method = 1`), and adds the notification to the silent alerts (MNT07's `SilentAlerts`). Warning when Database Mail is not configured (`msdb.dbo.sysmail_profile` empty or the Agent's alert system has no profile: `msdb.dbo.sp_get_sqlagent_properties` email_profile): "Alerts are created but no mail leaves this server until Database Mail is set up: copy this for IT." Read back operators, alerts and notifications. Rollback deletes what was created (names recorded). Source: Alerts.sql (MIT, notice). |
| `maintenance-job` | MNT03 | server (jobs) | `time` (HH:MM, default 01:00), `statisticsOnly` bool default true | When Ola's `IndexOptimize` exists in master: a daily job "kakapo: statistics ({db})" running `EXECUTE master.dbo.IndexOptimize @Databases = N'{db}', @FragmentationLow = NULL, @FragmentationMedium = NULL, @FragmentationHigh = NULL, @UpdateStatistics = 'ALL', @OnlyModifiedStatistics = 'Y', @StatisticsModificationLevel = 5` (the Ola Stats Only Job, with the notice). Without Ola: the same job running `EXEC [db].sys.sp_updatestats` with a warning that it samples and that Ola's solution is better for large tables (Copy a link). `statisticsOnly` false adds a weekly Sunday index job only when Ola exists (`@FragmentationMedium = 'INDEX_REORGANIZE', @FragmentationHigh = 'INDEX_REBUILD_ONLINE,INDEX_REBUILD_OFFLINE', @FragmentationLevel1 = 30, @FragmentationLevel2 = 50, @MinNumberOfPages = 1000`); otherwise the option is absent. Read back the jobs; rollback deletes them. Express: the script, as the backup schedule does. |
| `remove-shrink-step` | MNT05 (job) | server | `job`, `step` (from the finding) | `sp_delete_jobstep @job_name, @step_id` after reading the step; when it is the job's only step, `sp_update_job @enabled = 0` instead and say so. Warning: "The job's other steps renumber; a step that jumps to this one by number breaks (shown below if any)". Rollback recreates the step from what was read (`sp_add_jobstep` with the command). |
| `auto-shrink` on MNT05's AUTO_SHRINK finding | already wired in 3f | | | no change |

MNT01 (Agent not running or not automatic) stays Copy: a service change needs Configuration Manager.

## Part 2: Storage and Backups

| Fix | Finding | Scope | Options | Preview and apply |
|---|---|---|---|---|
| `autogrowth` | STO02, STO03 | database | `file` (logical name, from the finding), `growthMB` (default 256 for data, 64 for log; 1 to 4096) | `ALTER DATABASE [db] MODIFY FILE (NAME = N'{file}', FILEGROWTH = {n}MB)` (and `MAXSIZE = UNLIMITED` when STO03 found growth off and the file is not capped on purpose: a second option `unlimited` bool, default true, shown only then). Warning: on SQL Server 2022+ log growth up to 64 MB is instant; data file growth benefits from instant file initialization (INS08 says whether it is on). Read back `sys.master_files`. Rollback the previous growth. |
| `add-tempdb-files` | TMP01 | server | `count` (default the gap to min(CPUs, 8)) | `ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev{n}', FILENAME = N'{same folder as tempdev}\tempdev{n}.ndf', SIZE = {size of tempdev}, FILEGROWTH = {growth of tempdev})` per file. Warnings: the existing files should be the same size (if not: "make the existing files {size} first or the new ones are used unevenly"), the folder must have room ({n} x size), takes effect at once. Read back the file count. Rollback: `REMOVE FILE` per added file (works only while empty: say so). |
| `recovery-model` | DBC08, BAK02 (as BAK02's second Apply, "Switch to SIMPLE", next to the backup schedule: `docs/phase3g-contract.md`, revision 3) | database | `model` select `FULL` / `SIMPLE` (default the other one) | `ALTER DATABASE [db] SET RECOVERY {model}`. To FULL: warning "The log grows until it is backed up: create the log backup job (the Backup schedule row) right after this, or the disk fills." and the log-backup chain starts at the next full backup. To SIMPLE: warning "Point-in-time restore is lost; agree the data-loss window with the business (BAK05)". Read back `recovery_model_desc`. Rollback the previous model. |
| `checkdb-job` | BAK03 | server (jobs) | `day` select Sun..Sat (default Sunday), `time` (default 01:00), `physicalOnly` bool (default true when the database is over 200 GB, else false) | A weekly job "kakapo: {db} integrity check" running `DBCC CHECKDB ([db]) WITH NO_INFOMSGS, ALL_ERRORMSGS{, PHYSICAL_ONLY}` and notifying the operator when one exists (on failure, like the backup jobs). Warning: the CHECKDB room estimate (STO10) when stored, "run the estimate first" when not; duration unknown on the first run. Read back; rollback deletes the job. |
| `checkdb-now` | BAK03, BAK04 | database, a **run** not a change | `physicalOnly` bool | Runs `DBCC CHECKDB ([db]) WITH NO_INFOMSGS, ALL_ERRORMSGS{, PHYSICAL_ONLY}` with a 4-hour timeout, in the fix pattern (preview: the statement, the STO10 room warning, "runs online but loads the disk and tempdb; a quiet hour is better"; apply runs it; the result is the messages: "No errors found" or the error lines; change row type "CHECK" with the outcome). Stored also as the latest CHECKDB outcome on the Backups card (`checkdb_run` table: when, duration, outcome, messages), shown where the room estimate is on Storage and under BAK03 on Backups. BAK03 reads `LastGoodCheckDbTime` after a clean run, so the finding clears on the next read. |
| `suspect-pages-clear` | BAK04 | server | none | After a clean CHECKDB on this database only (the preview refuses otherwise): `DELETE msdb.dbo.suspect_pages WHERE database_id = DB_ID('{db}')`, Changes one row per page record, read back none left, no rollback (the rows are in the change row's read-back for the record). |

IDX01/07/10/13 on Storage stay Copy (space and design decisions). STO12's forecast stays a read.

## Frontend

Nothing new beyond the existing fix dialog (inputs, select, bool, Copy script) and the finding's Apply. The
card page passes a finding's fix options (`Fix.Options` from the detail's `fixes`, matched by `fixId` and the
finding's object) to the preview, as part 3 of 4c does for WRK03. The CHECKDB outcome panel on Backups is a
small card like the room estimate.

## Tests

Domain: every statement builder with its options; every blocking warning; the operator-alerts batch against
the scan's alert list; the maintenance job with and without Ola; the job removal rollback. Collect: the new
reads tagged and read-only; the fix statements only in domain. Service: a preview with a bad option per fix
returns no Changes. Store: the `checkdb_run` round trip.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: on a throwaway database with a 10%
growth file, `autogrowth` previews and applies and the file shows 256 MB; `recovery-model` to FULL warns about
the log job; `checkdb-now` runs and the Backups card shows "No errors found" with the time; `operator-alerts`
creates the operator and alerts on the local instance (removed afterwards through the rollback);
`maintenance-job` creates the sp_updatestats job (removed afterwards); `trace-flag-off` turns 1117 off after it
was staged on; `reconfigure` with a staged pending value activates it.
