# Backups on SQL Server Express: what to know, what kakapo covers, where the gaps are

Written 2026-11-11 after a client server warned that a database's log was filling up. It is a note to plan from,
not a contract: the plain explanation first, then what kakapo does today on Express, then the gaps.

## 1. The log, and the two recovery models

Every database has a log file: a running journal of every change. SQL Server writes each change there first. The
recovery model decides **when the journal's space is freed**.

| | SIMPLE | FULL |
|---|---|---|
| Who frees log space | SQL Server, by itself, all the time | Only a **log backup** |
| The log's size | Stays small | Grows until a log backup runs |
| A restore can go back to | The last full backup (last night) | Any minute: the full backup plus every log backup since |
| What must be scheduled | A full backup (nightly) | A full backup (nightly) **and** a log backup every 15 to 60 minutes |

**Turning on FULL is not set-and-forget.** Without log backups, the log keeps every change since the switch, grows
until the disk is full, and then SQL Server stops accepting changes to that database (error 9002;
`sys.databases.log_reuse_wait_desc` says `LOG_BACKUP`). That is the warning the client saw, and kakapo's BAK02
("FULL recovery but its log has never been backed up").

Things that surprise people:

- **A full backup does not free the log.** Only a log backup does.
- **FULL starts only after the first full backup.** Until then the database still behaves as SIMPLE.
- **A log backup frees space inside the file; the file stays its size.** A log that already grew to 40 GB is shrunk
  once, after a log backup (`DBCC SHRINKFILE`, STO08), and then left alone.
- **Old backup files must be deleted on a schedule too.** A restore needs the last full backup plus every log backup
  after it, unbroken; keep a few days and delete older files.
- **Backups on the same disk as the database protect against mistakes, not against losing the server.** A copy must
  leave the machine.

**Which one to choose is a business question:** how much work can the client lose?

- A day is acceptable: SIMPLE and a nightly full backup. Nothing grows.
- Only minutes: FULL, log backups every 15 to 60 minutes, old files cleaned up.

## 2. What SQL Server Express lacks for this

| Missing on Express | What it means for backups |
|---|---|
| SQL Server Agent | Nothing runs on a schedule unless something outside SQL Server starts it |
| Database Mail | Nobody is emailed when a backup fails |
| Backup compression | Backup files are as large as the data: more disk for the same days kept |
| (Size limit: 10 GB to 2022, 50 GB from 2025) | Counts data only: a runaway log is not stopped by it, it fills the disk |

## 3. What kakapo covers on Express today

- **The maintenance procedure (phase 5p)**, installed from the Upkeep card and called every 15 minutes by Windows
  Task Scheduler as SYSTEM (the procedure is signed, so SYSTEM needs only EXECUTE). For each database it takes:
  the nightly full backup, **the log backup every N minutes when the database is in FULL or BULK_LOGGED** (it
  starts by itself once a full backup exists), `RESTORE VERIFYONLY` after each backup, and deletes backup files
  older than the days kept. Each task is written to `master.dbo.kakapo_MaintenanceLog`, and a failed task makes the
  call end in an error.
- **The findings**: BAK02 (FULL without log backups), DBC08 (SIMPLE, consider FULL), STO07 (the log keeps growing),
  STO08 (a log much larger than it needs to be), STO04 (data size against the Express limit).
- **The recovery-model fix**, both ways, from DBC08 and BAK02, with preview and rollback.

So on Express, with the maintenance procedure installed: **switch to FULL, and the next calls take the log backups.**
The log stays the size of the work between two log backups.

## 4. The gaps

1. **Closed 2026-11-11: on Express, the backup findings now point to the maintenance procedure.** They used to offer
   an Agent job and fall back to "copy the script and schedule it in Windows Task Scheduler". Now, on Express:
   - BAK01, BAK02 and BAK05 (no backup in 7 days) offer **Install the maintenance procedure** while it does not serve
     the database (BAK02 keeps **Switch to SIMPLE** beside it).
   - When it serves the database, they offer no Apply and say the procedure takes the full backup at its time and the
     log backup every N minutes, and to check the scheduled task calls it (the Upkeep card shows its last call).
     BAK02 then offers no switch to SIMPLE either, as with an Agent job that should be taking the log backups.
   - The recovery-model fix's preview: to FULL says to install the procedure, or that it takes the log backups every N
     minutes from its next call; to SIMPLE says the procedure stops the log backups by itself.
2. **No one is told when a backup fails.** No Agent alerts, no Database Mail, and kakapo is not running at night. A
   failed call shows only in Task Scheduler's "Last Run Result" and in kakapo's maintenance log the next time it is
   opened. Ties to the nightly summary email (`nightly-summary-email.md`), whose sending path on Express is open.
3. **Backups stay on the server.** The procedure writes to one folder. A network share works only if the machine
   account (`DOMAIN\SERVER$`, as the task runs as SYSTEM) may write to it; kakapo neither checks that nor copies files
   off the machine.
4. **Disk room for backups is not checked against what will be kept.** Without compression, a 35 GB database with 7
   days kept needs about 245 GB for full backups alone, plus the log backups. The Storage card answers for the data
   and log disks, not the backup folder.
5. **A log that has already grown has no fix.** STO08 tells the user to shrink it by hand; there is no Apply.
6. **No restore is ever tested.** `RESTORE VERIFYONLY` proves a file is readable, not that the database comes back.
   A periodic test restore (to a scratch name, then dropped) is the only proof.

## 5. To decide

- Which gaps to close, and in what order. Gap 2 is the nightly email's design
  question; gaps 3 and 4 are checks in the install preview; gaps 5 and 6 are new fixes.
- Whether kakapo should recommend SIMPLE by default on Express (the usual small-business case) and FULL only when the
  client says minutes matter.
