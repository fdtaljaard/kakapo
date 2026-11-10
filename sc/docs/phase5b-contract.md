# Phase 5b contract: the maintenance plan

From the notes (number 4): schedules for general maintenance, statistics updates and the rest, by best
practice. Most pieces already exist as fixes (backup schedule, CHECKDB job, statistics job, operator and
alerts, archives). This phase adds the two missing pieces, puts them all on one row of the Upkeep card as
"the maintenance plan for this server", and offers to create what is missing in one go with a schedule that
does not overlap. SCOPE rules 1, 2 (every number below has its source), 4 and 5.

## The plan, and where each line comes from

| Piece | Cadence kakapo proposes | Why, and the source |
|---|---|---|
| Full backup | daily 22:00 | The backup schedule fix (phase 3g/4b). Ola Hallengren runs a weekly full with daily differentials; kakapo keeps daily fulls because the sites it serves are small enough and a differential chain is one more thing to get wrong. Source: Ola Hallengren, FAQ, "Full backup: one day per week; differential: all other days". |
| Log backup | every 15 minutes, FULL recovery only | Phase 3g. Ola: "transaction log backup: every hour"; 15 minutes is the data-loss window the Backups card recommends (BAK05). |
| Integrity check | weekly, Sunday 01:00; PHYSICAL_ONLY from 200 GB | Phase 4d. "Some consistency check at least once a week" (Paul Randal, as reported in Microsoft's CSS blog by Cindy Gross; Ola: "integrity check: one day per week"). PHYSICAL_ONLY with CHECKSUM page verification for very large databases (Microsoft, "CHECKDB part 6: consistency checking options for a VLDB"). |
| Statistics update | nightly 01:00, Monday to Saturday | Phase 4d. Microsoft: "the same benefit [as an index rebuild] can often be achieved at a much lower resource cost by updating statistics"; Brent Ozar: "if the issue is out-of-date statistics, update statistics daily"; Erik Darling's nightly job with `@StatisticsModificationLevel = 5`. Source: Microsoft, "Maintain indexes optimally"; Ozar, "Stop worrying about index fragmentation"; DarlingData, "Ola Stats Only Job". |
| Index maintenance | weekly, Sunday 03:00, **off by default** | Microsoft (2026): "Don't assume that index maintenance always noticeably improves your workload… don't assume that maintenance must be performed on a fixed schedule"; reorganise preferred over rebuild. Offered as an option with Brent Ozar's thresholds (reorganise from 50%; from 80% rebuild online where the edition can, reorganise elsewhere, never offline; "How to Configure Ola Hallengren's IndexOptimize Maintenance Script") and Ola's floor (pages >= 1000), only when IndexOptimize exists. Changed in phase 5q: the first version called 30% / 50% "Ola's thresholds", which they are not (his defaults are 5% / 30%), and rebuilt offline on editions without online rebuilds. The plan does not count it as missing. |
| History cleanup | weekly, Sunday 05:00, keep 30 days | New (`history-cleanup`). Microsoft: "we recommend that you periodically execute sp_delete_backuphistory" (msdb grows with every backup); Ola's installation runs `sp_delete_backuphistory`, `sp_purge_jobhistory`, CommandLog and output-file cleanup weekly with 30 days kept. |
| Alerts and an operator | once | Phase 4d (operator-alerts). Severity 19 to 25 and errors 823, 824, 825 (Alerts.sql, DarlingData). |
| Archives | nightly 02:30 per table | Phase 5a; listed, never created from the plan. |

Ordering, from Ola's FAQ: "perform the full backup after the integrity check" and after index maintenance,
so Sunday runs check (01:00), index work when on (03:00), cleanup (05:00), and the daily full backup at
22:00 follows them; statistics run on the other nights at 01:00; log backups run through everything. The
times are defaults (rule 5) and move together when the user changes the maintenance window.

## New check MNT08: msdb history kept for ever

`UpkeepFacts` gains `BackupHistoryOldest *time.Time` (`MIN(backup_finish_date)` in `msdb.dbo.backupset`),
`BackupHistoryRows int64`, `JobHistoryRows int64` (`sysjobhistory`), `JobHistoryOldest *time.Time`
(`run_date`), `CleanupJob string` (an enabled job whose step command contains `sp_delete_backuphistory` or
`sp_purge_jobhistory` or `CommandLog`), each in the msdb TRY. MNT08 (Low; Medium when history rows exceed
1,000,000 or the oldest is over 2 years): "msdb keeps {n} backup history rows going back to {date} and {m} job
history rows; nothing prunes them." Why: every backup and every job run adds rows; a bloated msdb slows the
Agent, the backup dialogs and the Backups card's own reads, and is in every msdb backup. Do this: the fix.
Owner Us. Then check: re-run after the first cleanup: MNT08 gone. Silent when `CleanupJob` is set.

## New fix `history-cleanup` (server, MNT08)

Inputs: `keepDays` (30; 7 to 365), `time` (05:00, advanced), `day` (Sunday, advanced). Creates
"kakapo: history cleanup" weekly, owner sa, category Database Maintenance, one tagged step:

```sql
DECLARE @oldest datetime = DATEADD(DAY, -{keepDays}, GETDATE());
EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;
EXEC msdb.dbo.sp_purge_jobhistory @oldest_date = @oldest;
IF OBJECT_ID(N'master.dbo.CommandLog') IS NOT NULL DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;
```

Preview: Changes rows per table with the rows that would go now (counted); warning that the physical backup
files are untouched and that the Backups card reads 7 days of history, so 30 days is the floor; a blocking
warning when `keepDays` is under 7; the Agent-stopped warning; sysadmin. Read-back: the job. Rollback:
delete the job (the pruned rows are gone and the preview says so). Express: the script.

## The row: "Maintenance plan" on the Upkeep card

`collect.MaintenancePlan(ctx, db, database)` reads, in one msdb batch with its own TRY areas, every piece's
state: kakapo's jobs by name (`kakapo: {db} full backup`, `… log backup`, `… integrity check`, `kakapo:
statistics ({db})`, `kakapo: indexes ({db})`, `kakapo: history cleanup`, `kakapo: archive {db}.…`), and
for each piece whether another enabled job covers it (the detection the single fixes already use: backup
commands naming the database or USER_DATABASES, CHECKDB or DatabaseIntegrityCheck, sp_updatestats or
IndexOptimize with statistics, IndexOptimize with fragmentation settings, the cleanup procedures), the
schedule in words, the last run (outcome, when, duration) from `sysjobhistory`, and the next run. Plus the
operator and alerts state from the Upkeep facts.

`domain.BuildMaintenancePlan` gives one `Piece` per line: `id`, `title`, `cadence` (words), `status`
(`in-place`, `other-job`, `missing`, `disabled`, `failing`, `never-ran`, `not-needed` for the log backup in
SIMPLE, `optional` for index work), `job` (name), `schedule`, `lastRun`, `lastOutcome`, `nextRun`, `fixId`
with `options` to create it, `source` (the sentence and source of the table above, for the row's "Why
these times" fold and the hand-over). The row shows a line per piece with its status word in colour, and
a Create button where `fixId` is set; a "Copy for IT" hand-over of the whole plan; and the button **Set up
the maintenance plan** when anything is missing.

`CardDetail` gains `MaintenancePlan *MaintenancePlan` on the Upkeep card (JSON `maintenancePlan`).

## The composite fix `maintenance-plan` (server, MNT03)

Inputs: one bool per missing piece (default on, index work default off and only offered when IndexOptimize
exists), `folder` (the backup folder, required when the backup piece is on, defaulted like the backup fix),
`email` and `operator` (when the alerts piece is on and no operator exists), and advanced: `fullTime` 22:00,
`statsTime` 01:00, `checkDay` Sunday, `checkTime` 01:00, `indexTime` 03:00, `cleanupTime` 05:00, `keepDays`
30, `logMinutes` 15. The preview is built by calling each piece's existing preview with its options and
stitching them: one Changes section per piece headed by its title, the warnings deduplicated, the statement
the pieces' statements in order with a comment line between. A piece whose own preview blocks blocks the
plan with that reason. Apply runs the pieces in order (alerts, cleanup, check, statistics, index, backup, so
the backup job is last and the first full backup can run tonight after the rest are in place) by calling
the registered fixes' apply functions one after another on the same connection; it stops at the first
failure, and the result names what was created and what was not (each piece writes its own change row as
today, and the plan writes one summary change row, type PLAN). Read-back: the plan read again. Rollback: the
pieces' rollbacks in reverse order. No `run now` from the plan.

## What the user sees

Upkeep card page, under the findings: the Maintenance plan row. For a server with nothing: seven lines
reading "missing" or "optional", and one button. For a healthy server: seven lines reading "in place" with
when they last ran, green. Hand-over block: the plan as a table with the sources, for the client's IT.

## Tests

Domain: the plan's status words for every case; MNT08's thresholds and silence; the cleanup statement and
blocks; the composite preview's stitching from fixture previews; the apply order and stop-at-failure; the
non-overlap of the default times. Collect: the plan read and the cleanup read tagged and read-only.
Service: both fixes registered; the composite calls the pieces (fakes) in order. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022` with a throwaway database: the row shows
everything missing; "Set up the maintenance plan" with the backup piece off (no folder) creates the check,
statistics, cleanup and alerts jobs in one apply and the row turns green for them; the cleanup job runs by
hand and MNT08 goes; everything created is removed afterwards through the rollbacks.
