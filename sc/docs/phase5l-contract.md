# Phase 5l contract: room before change, the free space on the drive in every preview that uses it

Francois, reviewing the Backups and Storage cards on 6 Nov 2026: "when you look at storage and you are setting up
backups, you are doing anything that will impact storage, I cannot visually see if I actually have the space to
turn on the settings that I need to turn on. Any place where we have to configure, set up, anything that we know
will consume more space, we need to see the available space." Designed and approved as mockups in that review,
on `OldSchool` (localhost\SQL2022).

Nothing new is written to the server; the new reads are SELECTs and `xp_fixeddrives`. No thresholds of our own
beyond the two agreed here (10 % of the drive left, 30 days at the drive's trend) and STO12's existing trend.

Facts measured on `OldSchool` (6 Nov 2026):
- Every file is on C: (974,484 MB, 224,845 MB free, 23 %). The default backup folder
  (`...\MSSQL16.SQL2022\MSSQL\Backup`) is on C: too. kakapo reads only the volumes of this database's files and
  tempdb's (`collect/storage.go:82`): never the backup folder's drive, never the event session folder's.
- msdb has no backup of OldSchool. It was restored twice on 5 Nov from full backups of 3,693 MB (921 MB
  compressed, 25 %) and 5,016 MB (1,010 MB). Its data file has 3,688 MB used: **used data ≈ the uncompressed full
  backup**, to 0.2 %.
- This server's compressed full backups of the last 30 days: 4, 2,206 MB of 10,270 MB, **21.5 %**. Used data x
  that ratio estimates OldSchool's backup at 793 MB against a real 921 MB: 14 % low. The estimate says so.
- kakapo reads no backup sizes anywhere (`backupset` is read for dates and counts only).
- Of the fourteen fixes that use space, three already compare need with free space: `add-tempdb-files` and
  `tempdb-files` (block when it does not fit) and `archive-table` (warn or block at 90 %). The backup schedule,
  the maintenance plan, Query Store, create index, the event session and the default trace do not.
- `xp_fixeddrives` answers on this server (C, 224,803 MB free, no total).

## 1. The room line

One component, the same in every preview that uses space and on the Storage card (part 5).

- **Where**: the first thing in the preview, above the inputs. The dialog rebuilds the preview when an input
  changes (blur or Enter, already so), so changing the folder, the days kept or compression redraws the line.
- **One line per drive**, never per part: a change that puts backups and CHECKDB's snapshot on C: shows one bar for
  C:, its parts named under it. A bar: in use now (grey), this change (colour of the state), free after.
- **The sentence**: "220 GB free now, this schedule uses about 6.2 GB, 213 GB left (22 % of 952 GB)."
- **The basis**, small, under it: what was counted and where each number came from, and "weak" when it is.
- **States**:
  - **Fits** (green): none of the below.
  - **Tight** (amber): less than 10 % of the drive free after the change, or STO12's trend (when it has two samples)
    fills the drive within 30 days of the change: "At the drive's current rate that is about 18 days."
  - **Does not fit** (red): the change needs more than the drive has free. Apply is held back (as
    `add-tempdb-files` does today), with what to do: "Keep fewer days, choose another folder, or free space first."
  - **Not readable** (grey): a share, or a drive kakapo cannot read. "SQL Server cannot report the free space of a
    share. This schedule needs about 6.2 GB there: check it before you apply." Apply stays available.
- **Domain**: `RoomLine{Drive, TotalMB (0 when unknown), FreeMB, NeedMB, Parts []RoomPart{Label, MB, Basis,
  Weak}, State, Sentence}` on `FixPreview.Room []RoomLine`; one builder, `BuildRoom(needs, volumes, trend)`,
  sums the needs per drive and judges. The existing tempdb and archive space warnings move into it (their texts
  go; their numbers stay).

## 2. The drive behind a folder

`collect.FolderVolume(folder)`: the volumes of every database file on the server (`sys.master_files` cross apply
`sys.dm_os_volume_stats`); the folder's volume is the longest mount point it starts with (total and free). Else
`xp_fixeddrives` for its drive letter (free only: no percentage, so Tight is judged on the trend alone). Else a
share or unknown: Not readable. Read for the backup folder (backup schedule, maintenance plan) and the event
session and default trace folder (the error log folder).

## 3. Backup sizes from msdb

`collect.BackupSizes(db)` from `msdb.dbo.backupset`: this database's last full (`backup_size`,
`compressed_backup_size`), the server's compression ratio over the compressed fulls of 30 days, and this
database's log backup MB a day over 7 days. One full backup is, in order:
1. the last full of this database (compressed size when the schedule compresses);
2. used data MB x the server's ratio (uncompressed: used data MB): "estimate, about 14 % low on the server
   where it was measured";
3. no compressed backup on the server: used data x 25 %, weak.

## 4. Each fix

| Fix | Drive(s) | Need | Basis, and where it is weak |
|-----|----------|------|-----------------------------|
| backup-schedule | backup folder | (days kept + 1) fulls (the job backs up first, deletes after); FULL recovery: + days kept x log MB a day | log MB a day from 7 days of log backups; none yet: "not known until a week of log backups", weak |
| maintenance-plan | backup folder; tempdb; data | its backup part as above; CHECKDB from STO10; index part: the largest index it would rebuild | per drive, summed |
| recovery-model to FULL | log file's drive | no number | "the log grows without limit until log backups run": Tight at least while no log backup job exists |
| checkdb-now, checkdb-job | tempdb; data drive (snapshot) | STO10: the estimate x 2 | STO10 not run: its "Check room" button inside the preview |
| autogrowth | the file's drive | one growth step | exact |
| add-tempdb-files, tempdb-files | tempdb's drives | as computed today | exact |
| query-store | data files' drive | max size - current size - free space inside the data files (never below 0) | upper bound; "backups grow by as much" |
| create-index | data drive; tempdb (`SORT_IN_TEMPDB`); log | `estimateIndexMB` each | rows x row width |
| archive-table | archive's drive; log | as computed today | as today |
| install-xe-session | its folder (error log folder) | file MB x files (200 MB by default) | exact cap |
| enable-default-trace | error log folder | 100 MB | exact cap |
| rcsi | tempdb | no number | "row versions live in tempdb; TMP03 shows how much", weak |

## 5. The Storage card answers first

The card asks "Will it run out of room?"; today the answer is one "STO01 Disk free space: Passed" line at the
bottom. The card leads with it:
- **A sentence**: "No. C: has 220 GB free, and this database uses 7.6 GB of it." (Tight or does not fit: STO01's
  or STO12's finding sentence.)
- **One bar per drive** this database's data, log, tempdb and backup folder use (part 1's bar: everything else
  grey, this database coloured), labelled with what is on it, free and total, and STO12's days left when known.
- Then the findings, as today.

## 6. Found in the same review, fixed in this phase

1. **BAK05 shows Passed when there has never been a full backup** (`bak05` returns, `domain/backups.go:338`).
   It is skipped instead: "Not judged: no full backup yet (BAK01)."
2. **IDX10 flags kakapo's own archive tables.** On OldSchool three of its four findings are `archive.IntegrationLog`,
   `archive.IntegrationDocumentQueue` and `archive.ScheduledJobsHistory`: an archive only grows by design. IDX10
   leaves out the archive schema and the `{db}_Archive` database.
3. **An archive kakapo made is invisible without its job.** The fourth finding, `dbo.Audit`, was already archived
   (rows up to 2025-11-05 are in `archive.Audit`; dbo.Audit starts 2025-11-06). kakapo knows its archives only
   from Agent jobs in this server's msdb (`collect/archive.go:203`), so a restored copy, or a server whose jobs
   were removed, reads as "kakapo archives no table of this database yet". An archive table whose source table
   exists counts as archived; with no job: "archive.Audit holds rows up to 2025-11-05, but no archive job runs on
   this server."
4. **IDX01 leaves out Query Store**: the tables add to 3,055 MB of a data file with 3,688 MB used; the other
   576 MB is Query Store. A line: "Query Store: 576 MB of the 2,048 MB it may use."
5. **IDX01 wording**: "973 MB data and 0 MB in 0 index(es)" reads as "no index"; the table has a clustered
   index (counted as data). "No nonclustered index."
6. **BAK01 on a restored database**: "No backup has been taken here since it was restored on 5 Nov" when
   `restorehistory` holds a restore and `backupset` no full since.
7. **DBC08 twice**: shown under "For information" and as "Passed" in the checked list; the list says Info.

## Done when

- Every fix of part 4 shows the room line; a test per fix builds its preview with a drive that fits, one that is
  tight and one that does not fit, and the last holds Apply back.
- On OldSchool the backup schedule preview says about 6.2 GB on C: (8 x 793 MB), 213 GB left, Fits, with the
  basis "used data 3.6 GB x 21.5 %, this server's compression; estimate".
- The Storage card leads with C:'s bar; IDX10 no longer lists the archive tables nor dbo.Audit as unarchived.
