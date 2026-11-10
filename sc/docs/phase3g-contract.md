# Phase 3g contract: the backup schedule fix

SCOPE section 3b made precise. The first fix that **creates** something on the server (SQL Server Agent jobs),
under the fix rules of SCOPE section 3 item 9: preview, confirm, apply, read back, change-log row, and a
remove fix. Builds on the generic fix pattern of `docs/phase3c-contract.md` and the Backups card of
`docs/phase3a-contract.md`. Native `BACKUP` statements in this version; Ola Hallengren's `DatabaseBackup` is
called when a site already has it. Installing Ola is a later phase.

## When the fix is offered

`BackupFacts` gains, read in the backups batch (each area in its own TRY, as the file does):

| Field | Read | Why |
|---|---|---|
| `HasAgent bool` | `SERVERPROPERTY('EngineEdition') <> 4` | Express has no Agent. |
| `AgentRunning bool`, `AgentError string` | `sys.dm_server_services` (`status_desc = 'Running'` for the Agent row); error when not visible | A stopped Agent runs nothing. |
| `BackupJobs []BackupJob{Name, Enabled bool, Kind string}` | enabled or disabled jobs in `msdb.dbo.sysjobs` with a step whose command (`sysjobsteps.command`) contains `BACKUP DATABASE` or `BACKUP LOG` or `DatabaseBackup`, and either names this database (`QUOTENAME(@db)`, `'@db'` or `N'@db'` or the bare name as a word) or covers all (`USER_DATABASES`, `ALL_DATABASES`); Kind `full`, `log` or `both` from the command text | Whether a schedule already exists. |
| `MaintenancePlans int` | rows in `msdb.dbo.sysmaintplan_subplans` | A plan may back up too; kakapo does not parse plans. |
| `OlaBackup bool` | `OBJECT_ID('master.dbo.DatabaseBackup') IS NOT NULL` | Call Ola when present. |
| `Operator string` | the first enabled operator with an email address in `msdb.dbo.sysoperators`, by name | Who the jobs notify. |
| `DefaultBackupDir string` | `master.dbo.xp_instance_regread N'HKEY_LOCAL_MACHINE', N'Software\Microsoft\MSSQLServer\MSSQLServer', N'BackupDirectory'` in its own TRY (`""` when refused) | The folder the preview suggests. |
| `CompressionOK bool` | `SERVERPROPERTY('Edition')` not starting with `Web` and engine major >= 10 | Web edition cannot compress. |

**The fix `backup-schedule`** (scope database, check BAK01, title "Create a backup schedule") is attached as
`fixId` to the BAK01 finding and to the BAK02 finding when both hold: `HasAgent`, no `BackupJobs` entry that is
enabled and covers the missing kind (a `full` job present and BAK02 firing still offers the fix, for the log
job only; a `both` or `log` job present and BAK02 firing offers nothing), the database is not a secondary
replica. When a covering job exists and the finding still fires, the finding's Do this says "The job {name}
should be doing this; check why it has not produced a backup (MNT02)." and no fix is offered. When
`MaintenancePlans > 0` and no job covers the database, the fix is still offered and the preview warns "A
maintenance plan exists on this server; check it does not already back up {db} before creating a second
schedule." On Express the Do this ends with "SQL Server Express has no Agent: kakapo cannot schedule the
backup here; see the script in the preview" and the fix is offered with `express: true` in the preview (below).

`AvailableBackupFixes(f BackupFacts, findings []Finding) []Fix` fills `CardDetail.Fixes` for the Backups card
(as the Setup and Upkeep cards do), including `remove-backup-schedule` when kakapo's jobs exist (below).

## Inputs: the fix pattern gains options

`POST .../fixes/{fixId}/preview` and `.../apply` accept an optional JSON body `{"options": {"name": "value"}}`.
Existing fixes ignore it. `FixPreview` gains:

```go
Inputs  []FixInput        `json:"inputs,omitempty"`  // what the user may set; the preview was built with Options
Options map[string]string `json:"options,omitempty"` // the values used, echoed so apply sends the same
Express bool              `json:"express,omitempty"` // true: nothing will be applied; Statement is a script to run by hand
type FixInput struct {
    Name  string `json:"name"`  // "folder", "runNow"
    Label string `json:"label"`
    Value string `json:"value"` // the value used
    Help  string `json:"help"`
    Kind  string `json:"kind"`  // "text", "bool"
}
```

Inputs of this fix:

| Name | Default | Rule |
|---|---|---|
| `folder` | `DefaultBackupDir`, else `""` | Required. Checked on the server before the preview is returned: `EXEC master.dbo.xp_fileexist @folder` (third column "Parent Directory Exists" and second "File is a Directory"); when it does not exist the preview carries the warning "The folder {folder} does not exist on {server} (as the SQL Server service sees it): create it, or type another." and `Changes` is empty, so Apply is disabled. A folder on the same volume as the database's data files adds the warning "This folder is on the same drive as the data files ({drive}): a disk failure loses the database and its backups together. Prefer another drive or a share." (the storage facts know the volumes; read `sys.master_files` for the data file paths in the backups batch to compare drive letters or UNC hosts). |
| `runNow` | `true` | After creating the jobs, start the full backup job (`sp_start_job`). |

The service passes `options` to `domain.BuildBackupScheduleFix(f BackupFacts, dbName string, options map[string]string, folderOK bool, sameDrive bool)`; the folder check is a `collect` call the service makes between reading the facts and building the preview, only for this fix.

## What apply creates

Two jobs, owned by the `sa` principal whatever its name (`@owner_login_name = SUSER_SNAME(0x01)`), category
"Database Maintenance", description "Created by kakapo on {date} for {db}". Names:
`kakapo: {db} full backup` and `kakapo: {db} log backup`.

**Full backup job**, schedule daily at 22:00 (`freq_type = 4, freq_interval = 1, active_start_time = 220000`):

- Step 1 "Back up and verify" (T-SQL, database master), native:
  ```sql
  DECLARE @dir nvarchar(400) = N'{folder}\{db}';
  EXEC master.dbo.xp_create_subdir @dir;
  DECLARE @file nvarchar(600) = @dir + N'\{db}_FULL_' + CONVERT(nvarchar(8), GETDATE(), 112) + N'_' + REPLACE(CONVERT(nvarchar(5), GETDATE(), 108), N':', N'') + N'.bak';
  BACKUP DATABASE [{db}] TO DISK = @file WITH CHECKSUM, INIT{, COMPRESSION};
  RESTORE VERIFYONLY FROM DISK = @file WITH CHECKSUM;
  ```
  With Ola present instead: `EXECUTE master.dbo.DatabaseBackup @Databases = N'{db}', @Directory = N'{folder}', @BackupType = 'FULL', @Verify = 'Y', @CheckSum = 'Y', @CleanupTime = 168{, @Compress = 'Y'};` and no step 2 (Ola cleans up).
- Step 2 "Delete backups older than 7 days": `DECLARE @cutoff datetime = DATEADD(DAY, -7, GETDATE()); EXEC master.dbo.xp_delete_file 0, N'{folder}\{db}', N'bak', @cutoff, 0;`
- Each step: `@on_success_action = 3` (go to next) except the last (1), `@on_fail_action = 2` (quit with failure).

**Log backup job**, only when the recovery model is FULL or BULK_LOGGED, every 15 minutes
(`freq_type = 4, freq_interval = 1, freq_subday_type = 4, freq_subday_interval = 15`): the same shape with
`BACKUP LOG [{db}] TO DISK = @file` to `{db}_LOG_{stamp}.trn` (Ola: `@BackupType = 'LOG'`), and the cleanup
with extension `trn`. In SIMPLE recovery the preview's Changes row says "Log backup job: not needed (SIMPLE
recovery; DBC08 decides)".

Both jobs: `sp_add_jobserver @server_name = N'(local)'`; when `Operator != ""`: `@notify_level_email = 2,
@notify_email_operator_name = N'{operator}'`. The statement is one batch; `sp_add_job` ... `sp_add_jobstep`
... `sp_add_jobschedule` ... `sp_add_jobserver` per job, inside `BEGIN TRY ... END TRY BEGIN CATCH; THROW; END
CATCH` with `IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = N'...') THROW 50000, 'A job named ... already
exists', 1;` first. Then, when `runNow`, `EXEC msdb.dbo.sp_start_job @job_name = N'kakapo: {db} full backup';`
as a separate statement after the read-back (a started job cannot be rolled back).

The job step commands carry the `/* kakapo */` tag too (the batches do by the `collect.Tag` rule).

## Preview

`FixPreview.Changes` (the existing Drift rows `Setting / Current / Target`):

| Setting | Current | Target |
|---|---|---|
| Full backup job | none | daily 22:00 to {folder}\{db}, compressed, verified |
| Log backup job | none | every 15 minutes, or "not needed (SIMPLE recovery; DBC08 decides)" |
| Keep | | 7 days of files |
| Tell on failure | | {operator}, or "no one (MNT07: no operator with an email)" |
| First full backup | | "now, when you apply" or "tonight at 22:00" |

`Statement` is the exact batch; `Rollback` is `EXEC msdb.dbo.sp_delete_job @job_name = N'...'` for each job
created. `Warnings`, in this order when they apply: the folder warnings above; "SQL Server Agent is stopped
({status}): the jobs will not run until it starts (MNT01)."; the maintenance-plan warning; "Web edition cannot
compress backups: the files will be full size."; "The backup job runs as the SQL Server service account; the
folder must let it write."; the Ola line "Uses the DatabaseBackup procedure already on this server."; and last
the permission line "Needs sysadmin on {server} (creating Agent jobs owned by sa)."

On Express (`Express: true`): no inputs beyond `folder`, `Changes` as above, `Statement` is a script with the
two BACKUP batches and a comment line saying how to schedule it in Task Scheduler
(`sqlcmd -S {server} -E -i backup-{db}.sql`, daily and every 15 minutes), `Rollback` empty, and Apply is
disabled by the UI; the card page offers "Copy script".

## Apply and read-back

`ApplyFix` for this fix: run the batch through `collect.ExecFix`; read back `msdb.dbo.sysjobs` joined to
`sysjobschedules` and `sysschedules` for the two names (`enabled`, schedule enabled, `next_run_date`); start
the job when `runNow`; write the change row: object "Backup schedule", type "JOB", description "Created the
jobs kakapo: {db} full backup (daily 22:00) and kakapo: {db} log backup (every 15 minutes) writing to
{folder}\{db}, 7 days kept" (one job in SIMPLE), statement, rollback, read-back. `FixResult.Before` "no backup
job", `After` "kakapo: {db} full backup: enabled, next run {date time}; kakapo: {db} log backup: enabled, next
run …; full backup started." `Verified` when every created job exists and is enabled.

## The remove fix

`remove-backup-schedule` (scope database, check BAK01, title "Remove kakapo's backup jobs"): offered in
`CardDetail.Fixes` when a job named `kakapo: {db} full backup` or `kakapo: {db} log backup` exists (read in the
backups batch: `KakapoJobs []string`). Preview: Changes one row per job ("Full backup job | exists | deleted"),
Statement the `sp_delete_job` calls, Rollback the creation batch rebuilt from the job as it is (folder read from
the step command between `N'` and `\{db}'`), warning "The backup files in {folder}\{db} are not deleted."
Apply: run, read back that the jobs are gone, change row type "JOB" description "Deleted the jobs …".

## Frontend

- `FixDialog`: when `preview.inputs` is present, render each input above the Changes table (text field or
  checkbox); changing one and leaving the field (or pressing Enter) re-runs the preview with the current
  options; Apply sends the same options. The folder field shows the folder warning inline in red under the
  field as well as in the warnings list. With `preview.express`, the Apply button is replaced by **Copy
  script**, which copies `statement`.
- Card page (Backups): the BAK01 and BAK02 finding cards get Apply through `fixId` as today. Under the findings,
  when `detail.fixes` holds `remove-backup-schedule`, a small line "kakapo created the backup jobs {names}" with
  a **Remove** button that opens the dialog for it.
- `api.ts`: `previewFix(serverId, db, fixId, options?)` and `applyFix(..., options?)`; the mock gains the fix
  with a folder input and a warning for a folder named `X:\nowhere`.

## Tests

Domain: the batch for a FULL database with Ola absent and present, compression on and off, SIMPLE (one job),
operator present and absent; the folder warnings; `express`; the remove fix's rollback rebuilds the batch;
`AvailableBackupFixes` for the four job-coverage cases; the Do this when a job exists. Collect: the backups
query is tagged, read-only (the fix statements are not in the read query), and the job-coverage SQL contains
the patterns. Service: unit test that preview with a bad folder returns no Changes.

## Done when

`go build ./... && go test ./...` pass and `.\build.ps1` builds. Live on `localhost\SQL2022` with a throwaway
database in FULL recovery and no backup history: BAK01 and BAK02 show Apply; the preview suggests the
instance's backup folder; a bad folder disables Apply with the warning; Apply with run-now creates the two
jobs, starts the full backup, and within a minute the Backups card reads the new full backup (BAK01 gone) and
Recent changes shows the row; the remove fix deletes the two jobs and logs it. The throwaway database and its
files are removed afterwards.

## Revision 2 (2026-11-03, after the first sandbox round)

The fix is offered as a standing **Backup schedule** row on the Backups card page (Create a backup schedule, or
the jobs kakapo created with Remove) whenever it can be created: jobs read, not a secondary replica, no kakapo
jobs yet, and a full or log job would be created. The findings still carry Apply when they fire, and BAK05 carries
it too when nothing was backed up in 7 days (an old full backup and a stopped schedule is the common case, which
BAK01 does not catch). `OffersBackupSchedule` is that rule; `CardDetail.Fixes` lists the fix under the firing
check or BAK01.

## Revision 3 (2026-11-10): BAK02 can be fixed entirely in kakapo

A user who sees BAK02 (FULL recovery, the log not backed up) must not have to hand it to Client IT. BAK02 has
two ways out, each its own **Apply** on the finding with the usual preview, approve, apply, verify and rollback:

| Button | Fix | What it does |
|---|---|---|
| **Schedule log backups** | `backup-schedule` (`fixId`) | The log job (and the full job if none exists), as above. |
| **Switch to SIMPLE** | `recovery-model` (`altFixId`) | `ALTER DATABASE [db] SET RECOVERY SIMPLE`; rollback sets the previous model back. |

- `Finding.altFixId` is new: the second fix of one finding, left out of the JSON when there is none. `fixId` is
  the schedule when it is offered; when it is not (jobs unreadable, covered, a secondary replica), the switch
  to SIMPLE is the `fixId` and there is no `altFixId`. `CardDetail.Fixes` lists both, under BAK02 (the switch)
  and the check carrying the schedule.
- The switch to SIMPLE is not offered where it would be wrong: an availability group member (needs FULL), a
  secondary replica, or an enabled job that takes the log backups (that job is failing; SIMPLE would not fix it,
  and the Do this points at the job). When the jobs could not be read, it is offered with the warning that a job
  taking log backups of this database fails in SIMPLE recovery.
- **Do this** (answer first, no pointer to DBC08): "Back up the log every 15 minutes, or switch to SIMPLE
  recovery if a restore to the last full backup is enough. A full backup must exist first, or the log backup job
  fails."
- **Owner**: Us while kakapo can apply a fix (the card then counts it as ours, not Client IT's); Client IT when
  no fix is offered, or the login is refused every way. The catalogue's owner (Client IT) stays the default.

**Why Apply may not be usable, said on the card.** The schedule creates Agent jobs owned by `sa` (sysadmin) and
the switch needs ALTER on the database. The Backups read now also asks `IS_SRVROLEMEMBER('sysadmin')` and
`HAS_PERMS_BY_NAME(db, 'DATABASE', 'ALTER')` (`BackupFacts.PermsRead`, `IsSysadmin`, `CanAlter`; unread, nothing
is said). The finding's Do this and the first warning of the preview then say so plainly, before the user tries:

- "kakapo's login is not sysadmin on {server}, so Apply for the schedule will be refused: connect with a sysadmin
  login."
- "kakapo's login cannot ALTER the database {db}, so Apply will be refused: connect with a sysadmin login."
- No schedule offered because the Agent jobs could not be read (usually the same cause): "kakapo cannot create the
  schedule: it could not read the Agent jobs in msdb ({reason}). Connect with a sysadmin login."
- A secondary replica: "This database is a secondary replica: schedule the backups on the primary."

Tests: BAK02 offers both fixes (`TestBak02OffersBothFixes`), where the switch is not offered, the reasons above,
and the web card's two buttons (`scripts/handover.test.ts`).
