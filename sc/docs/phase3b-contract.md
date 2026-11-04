# Phase 3b contract: the Storage and space card

Second real card, built on the finding model, card detail endpoint, storage and card page from
`docs/phase3a-contract.md` (read it first). Sources: `01-QuickScan.sql` section 3 (lines 540-690: #Files, STO01 to
STO07, STO09), STO08 (lines 957-1031), TMP01/TMP02 (705-727), TMP03 (1317-1344); `PerformanceTriage.sql` IDX01
(486-497), IDX07 (707-728), IDX10 (1409-1468), IDX13 (1531-1554); `Checks.csv` for Owner and Then check. Plus one
new check, **STO10**, that the toolkit does not have. Field names exactly as written.

## Scope: one database, with the instance facts it depends on

The card answers "Will it run out of room?" for the chosen database. Instance-level items appear because they
decide the answer: the volumes this database's files are on, and tempdb (every query sorts there).

| Check | Scope here | Fires when (as the scan, numbers included) | Severity | When / Effort |
|---|---|---|---|---|
| **STO01** Disk free space | each volume holding one of this database's files | free < 20% or < 5 GB | < 10% or < 5 GB: High; else Medium | Online / Moderate |
| **STO02** Percentage autogrowth | this database's files, and tempdb's | `is_percent_growth` and growth > 0 | Medium (tempdb: Medium) | Online / Quick win. Do this = the scan's `ALTER DATABASE ... MODIFY FILE (FILEGROWTH = N MB)` with the scan's suggested size (1024 >= 10 GB, 256 >= 1 GB, else 64) |
| **STO03** Growth off or tiny | this database's files, and tempdb's | growth 0, or (< 64 MB and file >= 1 GB) | Low | Online / Quick win, the scan's statement |
| **STO04** Near max size, Express limit | this database's files | size >= 90% of a set max size; on Express, data >= 8 GB | High (Express: >= 9 GB High else Medium) | Online / Quick win; Express: Decide first / Project |
| **STO05** Files on C: | this database's files | any file on the system drive | Medium | Window / Moderate |
| **STO06** Data and log on one volume | this database | a data and a log file share a volume | Low | Window / Project |
| **STO07** Log larger than data | this database | log MB > data MB and log >= 1 GB | Medium | Online / Moderate, Do this by `log_reuse_wait_desc` as the scan |
| **STO08** VLF count | this database | VLFs > 500 (sys.dm_db_log_info, 2016 SP2+; skipped before) | > 1000: Medium; else Low | Window / Moderate |
| **STO09** Storage latency | this database's files, since restart | data reads avg >= 20 ms with >= 1000 reads; log writes avg >= 10 ms with >= 1000 writes | >= 50 ms read or >= 20 ms write: High; else Medium | Decide first / Project |
| **TMP01** TempDB file count | instance | files < min(CPUs, 8) | 1 file: Medium; else Low | Online / Quick win |
| **TMP02** TempDB files equal | instance | sizes or growth differ | Low | Online / Quick win |
| **TMP03** TempDB more than half full | instance | as the scan (lines 1329-1342) | High when >= 80% (or log >= 90%) and cannot grow or volume free < a quarter; else Medium | None / Moderate |
| **IDX01** Where the space goes | this database | always: the 10 biggest tables | Info | None / None. One finding whose evidence lists the top 10 (name, MB total, data MB, index MB, rows); the card's "Database size" fact comes from here |
| **IDX07** Mostly empty data files; log bigger than all data | this database | data file >= 5 GB and used < 50%; log >= 2 GB and > data used | Low; Medium | Window / Moderate; Online / Moderate |
| **IDX10** Granite log tables | this database | ScheduledJobsHistory, IntegrationLog, EmailLog at >= 1,000,000 rows or >= 500 MB | >= 1 GB or >= 5M rows: Medium; else Low | Decide first / Moderate. Do this = the scan's batched delete text, with the existing-cleanup-code note when found |
| **IDX13** Identity range | this database | identity column >= 50% used | >= 99%: Critical; >= 90%: High; >= 70%: Medium; else Low | Decide first / Project |
| **STO10** Room to run CHECKDB | this database, **on request** | see below | see below | Decide first / Moderate |

Severity words, When mapping, Owner and Then check as in phase 3a (`Checks.csv`: STO01/05/06/09 Client IT, the
rest Us; STO10 is Client IT). Why, Do this and Evidence texts verbatim from the scripts with the numbers filled in.
Where the scan makes one finding per file, keep one finding per file, object = the logical file name.

## STO10: is there room to run DBCC CHECKDB on this database? (new)

CHECKDB needs space in two places. The check reads both and compares.

**Read on request** (`POST .../cards/storage/checkdb-room`), because the estimate can take a while on a big
database:

1. `DBCC CHECKDB (@db) WITH ESTIMATEONLY, NO_INFOMSGS` (read only). It returns result sets with
   "Estimated TEMPDB space (in KB) needed for CHECKALLOC" and "... for CHECKTABLES". Sum them = `estimateKB`.
   Treat the estimate as low: `needMB = estimateKB / 1024 * 2`.
2. TempDB room: `tempdbFreeMB` = unallocated extents inside its data files (`tempdb.sys.dm_db_file_space_usage`);
   `tempdbRoomMB` = that plus what autogrowth can add: for each data file with growth > 0, (max size when set, else
   the free space on its volume) minus current size, summed, capped by the volume's free space.
3. Data volume: `dataFreeMB` = the least free space among the volumes holding this database's data files;
   `dataSizeMB` = the database's data files' used MB. The internal snapshot CHECKDB takes lives on those volumes
   and grows with every page changed during the check.

**Verdict** (store it; the card page shows the last one with its time and a button to run it again):

| Case | Severity | Finding sentence |
|---|---|---|
| needMB <= tempdbFreeMB and dataFreeMB >= 10% of dataSizeMB | Info (passed) | "CHECKDB on {db} needs about {need} of tempdb; tempdb has {free} free and the data volume has {dataFree} free." |
| needMB > tempdbFreeMB but <= tempdbRoomMB | Medium | "CHECKDB on {db} needs about {need} of tempdb; tempdb has {free} free and would have to grow by {need - free}." |
| needMB > tempdbRoomMB | High | "CHECKDB on {db} needs about {need} of tempdb, but tempdb can only reach {room}." |
| dataFreeMB < 10% of dataSizeMB | Medium (High when < 2%) | "The data volume has {dataFree} free for a database of {dataSize}; the snapshot CHECKDB takes needs room to grow while it runs." |

When both fire, one finding with the worse severity and both sentences joined. Why: "CHECKDB sorts its work in
tempdb and takes an internal snapshot of the database on the data volume; if either runs out of room the check
fails part-way, and on a busy server it can fill tempdb for every other database too." Do this: "Free space on
{volume} or let tempdb grow to {need}. Alternatives: run DBCC CHECKDB WITH PHYSICAL_ONLY (needs far less), or run
the full check on a restored copy of the last backup, which also tests the backup." Then check: "Run the estimate
again: STO10 passed." Evidence: the raw numbers: estimate KB, need MB, tempdb free and room MB, data volume, free
MB, data size MB, the time it took.

Before it is run, the checks list shows STO10 as `skipped` with note "Not run yet. Check room for CHECKDB reads an
estimate from SQL Server; it can take a minute on a large database." After it is run, its status follows the
verdict and the detail carries the result.

## Endpoints

| Method and path | Response |
|---|---|
| `GET .../databases/{db}/overview` | unchanged shape; the `storage` card is now live. The overview does **not** run STO10. |
| `GET .../databases/{db}/cards/storage` | `CardDetail` as phase 3a, plus `checkdbRoom: CheckdbRoom | null` (the last stored result). |
| `POST .../databases/{db}/cards/storage/checkdb-room` | runs STO10 now, stores it, returns `200` `CheckdbRoom`. `409` when the database is not online. `502` when the DBCC fails (the message says why, e.g. permission: it needs `db_owner` or `sysadmin`). |

```ts
interface CheckdbRoom {
  capturedAt: string
  durationSeconds: number
  estimateKB: number
  needMB: number
  tempdbFreeMB: number
  tempdbRoomMB: number
  dataVolume: string        // "D:\"
  dataFreeMB: number
  dataSizeMB: number
  finding: Finding          // STO10 as a finding row (Info when passed)
}
```

`CardDetail` gains the optional field `checkdbRoom` (omit or null for every other card).

## The card

- **status / statusText / verdict / toDo / forClientIT**: the phase 3a rules over the findings (Critical red;
  High, Medium, Low amber; Info only green). Green verdict: "{db} is {size} on {volume}, which has {free} free."
- **facts** (four): `Tightest drive` = "{volume} {pct}% free ({GB} GB)" over the volumes holding this database's
  files (worse when STO01 High, bad when Medium); `Database size` = "{data+index GB} GB, {index share}% indexes";
  `Disk latency` = "{read} ms read, {write} ms write" for this database's files (bad/worse as STO09);
  the fourth: `Identity range` = "{pct}% used ({table})" when IDX13 fires at >= 70% (worse at >= 90%); else
  `Log tables` = "{n} without cleanup, {biggest} {MB}" when IDX10 fires (bad); else `TempDB` = "{pct}% full of
  {GB} GB" (bad when TMP03 fires).
- **fixable** = 0 in this phase (the STO02/STO03 statements become fixes later).
- Grey with the reason when the database is not online, or when `sys.master_files` / `dm_os_volume_stats` cannot
  be read (VIEW SERVER STATE); checks that need a permission the login lacks are `skipped` with the reason, the
  rest still run.
- One `card_read` per read as in phase 3a. STO10 results go in a new table:

```sql
CREATE SEQUENCE IF NOT EXISTS seq_checkdb_room START 1;
CREATE TABLE IF NOT EXISTS checkdb_room (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_checkdb_room'),
  server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
  duration_seconds DOUBLE, estimate_kb BIGINT, need_mb BIGINT, tempdb_free_mb BIGINT, tempdb_room_mb BIGINT,
  data_volume VARCHAR, data_free_mb BIGINT, data_size_mb BIGINT,
  severity VARCHAR NOT NULL, finding VARCHAR NOT NULL, evidence VARCHAR
);
```

## What the collector reads (`collect`, SELECTs plus the one DBCC on request)

In as few batches as sensible, from the instance: `sys.master_files` with `sys.dm_os_volume_stats` for this
database and tempdb (tempdb's current sizes from `tempdb.sys.database_files`); `sys.dm_io_virtual_file_stats` for
this database's files; `sys.databases` `log_reuse_wait_desc`, `recovery_model_desc`; `SERVERPROPERTY('EngineEdition')`
for Express; `cpu_count` from `sys.dm_os_sys_info`; `tempdb.sys.dm_db_file_space_usage` and `sys.dm_db_log_space_usage`
in tempdb; `sys.dm_db_log_info(db_id)` for VLFs (2016 SP2+). Inside the database (via `EXEC [db].sys.sp_executesql`):
`FILEPROPERTY(name, 'SpaceUsed')` per data file, the table sizes (`sys.dm_db_partition_stats` grouped by object:
rows, data MB, index MB, index count), the three Granite log tables' row counts and MB and their oldest/newest
`[Date]` by first and last id as the triage does (seek only, never a scan), and `sys.identity_columns` with types.
Nothing writes. The DBCC runs only from the POST.

## Frontend

- The card page for `storage` is the phase 3a findings page plus one panel above "What was checked":
  **Room for CHECKDB** with the last result (its finding card and "as of") or the not-run note, and a button
  **Check room for CHECKDB** that calls the POST, shows a spinner with "Asking SQL Server for its estimate…",
  then the result; errors shown with the backend's message.
- The database page's Storage card needs no change beyond rendering what the overview sends.
- Mock: a Storage card and detail for the three databases (one green, one amber with STO01 Medium and IDX10,
  one amber with IDX13 High at 92%), a stored CheckdbRoom on one of them, and the POST returning a new result
  after a 2-second delay.

## Addendum (2026-11-03): four points taken from an outside review

### A. Free space inside the files

- The `Database size` fact becomes "{allocated} GB allocated, {used} GB used" over this database's data files
  (`FILEPROPERTY(name, 'SpaceUsed')`, already read). The index share stays in IDX01's evidence.
- **STO03** gains a severe case: a file with autogrowth off (growth 0) that is **90% or more used** is **High**, not
  Low, with the sentence "File {name} has autogrowth switched off and is {pct}% full." Why: the scan's "Once the file
  is full, inserts and updates fail." Do this: the scan's `ALTER DATABASE ... MODIFY FILE (... FILEGROWTH = N MB)`.
  Data files use SpaceUsed; the log file uses `used_log_space_in_percent`.

### B. STO11: transaction log usage (new)

Read inside the database: `sys.dm_db_log_space_usage` (`total_log_size_in_bytes`, `used_log_space_in_percent`),
and `log_reuse_wait_desc` from `sys.databases` (already read). "Can grow" = the log file has growth > 0, is not
at its max size, and the log's volume has free space of at least one growth step (a percentage step is computed
on the current size).

| Fires when | Severity | Sentence |
|---|---|---|
| used >= 80% and (reuse wait is not NOTHING, or the log cannot grow) | Medium | "The transaction log is {pct}% full ({used GB} of {size GB}) and is waiting on {reason}." |
| used >= 90% and the log cannot grow | High | same sentence plus " It cannot grow." |

Reason text by `log_reuse_wait_desc`, as STO07's Do this already maps it: LOG_BACKUP "a log backup",
ACTIVE_TRANSACTION "a transaction that is still open", REPLICATION "replication or CDC", AVAILABILITY_REPLICA "an
Availability Group replica", NOTHING "nothing: the space is reused at the next checkpoint", anything else the raw
value in lower case. Why: "When the log is full and can neither be reused nor grow, every insert, update and
delete in this database fails with error 9002 until it is." Do this: the STO07 Do this for that reason, and when
it cannot grow: "Free space on {volume} or raise the file's growth or maximum size." Owner Us, When Online,
Effort Quick win, Then check "Re-run: STO11 gone and the log under 50% used." Evidence: size, used, reuse wait,
growth, max size, volume free. Catalogue title: "Transaction log usage"; question "Is the log about to fill up?".

The fact `Disk latency` is replaced by **`Log`** = "{size GB} GB, {pct}% used" plus ", waiting on {reason}" when
the reason is not NOTHING. Tone bad at >= 80%, worse at >= 90%. Latency stays as the STO09 finding.

### C. Samples and a forecast (STO12, new)

Every storage read stores samples, UTC from Go, in a new migration:

```sql
CREATE TABLE IF NOT EXISTS volume_sample (
  server_id INTEGER NOT NULL, captured_at TIMESTAMP NOT NULL, volume VARCHAR NOT NULL,
  free_mb BIGINT NOT NULL, total_mb BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS storage_sample (
  server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
  data_alloc_mb BIGINT, data_used_mb BIGINT, log_size_mb BIGINT, log_used_pct DOUBLE,
  tempdb_alloc_mb BIGINT, tempdb_used_mb BIGINT
);
```

One `volume_sample` row per volume this database's files are on (instance level, keyed by server), one
`storage_sample` row per database read. Nothing is deleted.

**STO12 Disk growth forecast**, for the tightest volume of this database: take the newest sample and the oldest
sample within the last 30 days that is **at least 24 hours older** than the newest. Rate = (old free - new free)
per day. If no such pair exists: `skipped` with the note "Needs samples a day or more apart; the first was kept
{when}." If rate <= 0: passed, evidence "{GB} freed per day over {N} days." Otherwise days left = new free / rate:
**< 7 days High, < 30 days Medium**, else passed with the evidence "At {GB}/day, full in about {N} days." Sentence:
"At the rate of the last {N} days ({GB} a day), {volume} will be full in about {days} days." Why: "When the disk
fills, the database cannot grow, transactions and backups fail, and the application stops." Do this: "Free or add
space with the infrastructure team before then, and find what is growing: the log (STO07, STO11), the Granite log
tables (IDX10) or backup files on the same drive." Owner Client IT, When Decide first, Effort Moderate, Then
check "Re-run after a week: the forecast is longer." Catalogue title "Disk growth forecast"; question "When will
the disk be full?".

The `Tightest drive` fact gains ", full in ~{N} days" when STO12 fires (tone from its severity).

### D. STO10 wording

Why gains: "The snapshot grows with every change made while the check runs, so the figure is an estimate; run
CHECKDB in a quiet period." Do this gains the final sentence "Run it in a quiet period."
