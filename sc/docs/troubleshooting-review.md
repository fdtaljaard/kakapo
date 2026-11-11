# Troubleshooting review: slow storage (STO09)

A review of an outside troubleshooting conversation (a ChatGPT chat, 2026-11-11) that started from a kakapo
finding, against what kakapo already does. Advice only: nothing here is built. Names are anonymised: the
server is SQL01, the databases ClientDB, ClientDB_Test and OtherDB; tempdb, ReportServer and the PolyBase
databases (DWConfiguration, DWDiagnostics, DWQueue) keep their SQL Server names.

## 1. The session in short

- **Symptom.** kakapo raised STO09 (High) on SQL01 / ClientDB_Test: data reads average 75 ms on the .mdf
  (609,243 reads). The user pasted the "Copy for IT" block and asked how to fix it.
- **Checks, in order.**
  1. Every file on the server from `sys.dm_io_virtual_file_stats(NULL, NULL)` with its volume. Result:
     **every file of every database is on C:**. All eight tempdb data files read at about 89 ms and **write at
     about 790 ms**. ClientDB_Test's data file writes at 95 ms, ClientDB reads at 39 ms and writes at 100 ms.
     Even nearly idle system databases write their logs at 40 to 130 ms.
  2. I/O waits since the restart: **WRITELOG is the top wait** (9.6 M waits, about 141 hours added up,
     about 53 ms a wait). Then IO_COMPLETION (45 h) and PAGEIOLATCH_* (about 20 h together).
  3. Pending I/O requests (`sys.dm_io_pending_io_requests`). ChatGPT's first query used a column that does
     not exist. The corrected one showed nothing useful: `io_pending_ms_ticks` is for internal use only.
  4. Log-file latency for every database: 40 to 134 ms average writes everywhere. ClientDB_Test's log
     (10.4 M writes, 220 GB) has spent about 154 hours waiting on log writes.
- **Cause (not proven).** The chat ended on a working hypothesis: a **shared storage bottleneck under C:**
  (the disk, the VM's datastore, contention with other VMs, or antivirus/filter drivers). It did not find a
  single cause. Its open questions were whether SQL01 is a VM or physical, what PerfMon shows on C:
  during business hours, and whether the error log has any 833 messages.
- **Fix.** None applied. The advice was not to change SQL Server settings, not to add tempdb files or
  rebuild indexes, and to send the infrastructure team a request (a drafted email) to investigate C:
  first. Memory and missing indexes were left as secondary checks (fewer reads means less waiting).
- **What the chat missed.** Since the restart, **ClientDB_Test has done nearly all the disk work** on the
  server: 441 GB written to its data file and 220 GB to its log. ClientDB, by name the production
  database, wrote 59 MB and 113 MB. **tempdb has written about 2.5 TB and read about 1.7 TB.** A test
  database and a heavy tempdb user share one C: drive with production. Stopping or moving that workload
  may help sooner than new storage.

## 2. Each step against kakapo

| Step or fact in the chat | kakapo today |
|---|---|
| STO09: slow data reads on the chosen database's file | **Covered** by STO09 (data reads ≥ 20 ms, log writes ≥ 10 ms, grouped by volume, worst file named). |
| The same figures for every database's files and tempdb | **Partly.** STO09 judges only the chosen database's files (`DatabaseFiles()` leaves out tempdb; the storage query reads `sys.dm_io_virtual_file_stats(DB_ID(), NULL)`). SRV04 reads every file's statistics but only adds up bytes read. Nothing says "the whole server is slow on this disk". |
| Slow data-file **writes** (tempdb at 790 ms, ClientDB_Test's .mdf at 95 ms) | **Not covered.** STO09 judges data reads and log writes only. The live sample does the same. |
| tempdb latency | **Partly.** The live sample (30 s by default, run by hand) covers tempdb's data reads and log writes. STO09 never covers tempdb. TMP01/02/03 cover how tempdb is laid out and how full it is, not its speed. |
| Files on C: | **Partly.** STO05 flags only the chosen database's files. tempdb and the other databases on C: are not mentioned. |
| tempdb has 8 equal files: leave them alone | **Covered** by TMP01 and TMP02. |
| Figures are cumulative since the restart | **Covered in the wording** (STO09's "do this" says so). There is **no "is it still slow now"**: the I/O counters are not stored between scans. Only the live sample gives a now figure, and only when someone runs it. |
| I/O wait types (WRITELOG, PAGEIOLATCH, IO_COMPLETION) | **Covered** by WRK01 (log / I/O / other-I/O groups, since restart), which points back to STO09. |
| Memory pressure (max memory, RAM, PLE, grants pending) | **Covered** by INS02, INS03, INS12, PRS01, PRS02 and the live sample's PLE line. |
| Too many reads (missing indexes, scans, stale statistics) | **Covered** by WRK03, QRY01, MNT04 and the tuning report. |
| Which database makes the disk busy | **Partly.** SRV04 ranks databases by CPU and **bytes read**. Writes and time spent waiting are not counted, so ClientDB_Test's 660 GB of writes go unseen. |
| Who uses tempdb | **Partly.** TMP03 shows who holds tempdb right now. QRY01 has Query Store's tempdb use per query (2017+). Neither is linked from a latency finding. |
| VM or physical | **Partly.** Setup already reads `virtual_machine_type_desc` (used by INS12/INS17). Storage findings do not show it. |
| Antivirus scanning the data and log files | **Partly.** WRK08 and the live sample mention antivirus as a CPU cause, not as an I/O cause. |
| PerfMon: Avg. Disk sec/Read and Write, queue length on C: | **Not covered.** These counters cannot be read over a SQL connection (`sys.dm_os_performance_counters` holds only SQL Server's counters). This has to run on the server. |
| Error 833 (I/O took longer than 15 seconds) | **Not covered**, as far as the code shows. MNT06 reads severe errors from the default trace and memory dumps. 833 is an informational message in the error log, so it is probably missed. Worth checking against a real 833. |
| Pending I/O requests | **Not covered, and should not be.** The chat showed the column is internal and the result means little. |
| A ready request for the infrastructure team | **Partly.** "Copy for IT" sends one finding. It does not carry the server-wide picture, the VM answer or what to measure on the machine. |
| "Then check: re-run after a business day, STO09 lower" | **Weak.** An average since the restart barely moves a day after a fix when the server has been up for weeks. The check cannot prove the fix. |

Note: the chat suggests new checks "STO10" and "STO11". Both IDs are taken (room for CHECKDB, log usage).
Any new storage check would be STO13.

## 3. What to add, ranked by value to a non-SQL user

Built (2026-11-11): 3 and 4, in STO09's evidence and "do this" (`sto09Do`, `waitShares` in
`internal/domain/storage.go`; the per-volume read in `internal/collect/storage.go`). 2 built the same day:
every file on the server is read, data-file writes are judged (from 50 ms; High from 200 ms), and a drive
slow for more than one database (TempDB counts) is one server-wide finding, a drive where only TempDB is
slow a TempDB one. The per-file table is in the evidence (eight files at most), not a fold. 1 and 5 are open.

### 1. "Is the disk slow now?" on STO09: latency since the last scan (medium)

- **What the user sees.** The answer comes first: *"Now: reads on C: average 68 ms over the last 4 hours
  (since the previous scan). Since the restart: 75 ms."* Or, after a fix: *"Now: 6 ms. Better than the
  75 ms since the restart."* A finding raised only on the since-restart average, with a healthy now
  figure, drops to "was slow, fine now" (lower severity).
- **Where.** STO09 extended. Store each scan's per-file counters (reads, writes, stall ms, bytes) in
  DuckDB. Judge the difference from the previous scan of the same server when there is one (same
  `sqlserver_start_time`). Fall back to since-restart otherwise. Then-check becomes "the now figure under
  20 ms after the change", which the user can actually verify.
- **Size.** Medium: a new stored table, the difference, and the wording on the card. Same pattern as the
  Performance trend (5k).
- **On the machine?** No. It runs over the connection.

### 2. One server-wide storage finding: "the disk is slow for every database" (medium)

- **What the user sees.** On any database's Storage card: *"Drive C: is slow for the whole server, not
  just this database. 31 files on C: across 12 databases. TempDB writes take 790 ms, ClientDB_Test reads
  take 75 ms."* Then a fold with the per-file table (database, file, reads/writes, average ms), worst
  first. The same finding appears on every database that has files on that volume, so it reads as one
  incident, not twelve tickets.
- **Where.** STO09 widened. Read `sys.dm_io_virtual_file_stats(NULL, NULL)` joined to
  `sys.master_files` and `sys.dm_os_volume_stats` (SRV04 already reads the first). Judge
  **data-file writes** as well as data reads and log writes. Include tempdb. Group by volume, and raise
  the server-wide wording when files of two or more databases on the same volume are slow. STO05's
  evidence can also say "tempdb and N other databases are on C: too".
- **Size.** Medium: a wider query and grouping, with the existing finding shape. It pairs well with #1:
  store all files once.
- **On the machine?** No.

### 3. "Who makes the disk busy": per-database share of disk work and wait (small)

- **What the user sees.** *"Most of the disk work on C: since the restart comes from ClientDB_Test (660 GB
  written, 80% of the time spent waiting on C:) and TempDB. ClientDB wrote under 1 GB."* When the busy one
  is not the chosen database: *"If ClientDB_Test is a test database, moving it off this server (or off
  C:) is the quickest relief."* For tempdb, a link to QRY01 sorted by tempdb use.
- **Where.** SRV04 extended (writes and stall ms beside CPU and reads). One line in STO09's evidence
  naming the top contributor on the volume.
- **Size.** Small. The data is already in the query from #2.
- **On the machine?** No.

### 4. A complete "Copy for IT" for slow storage (small)

- **What the user sees.** The STO09 hand-over becomes the request the chat had to draft by hand:
  1. the server-wide table (#2);
  2. **"This server is a VM"** (or physical), from Setup's existing `virtual_machine_type_desc`, with the
     matching question: "check the datastore and other VMs on it" or "check the controller, RAID and
     drives";
  3. the folders to exclude from antivirus scanning (the data, log and backup paths kakapo already knows);
  4. one command to run on the server for 15 minutes in business hours, with the answer it looks for:
     `typeperf "\LogicalDisk(C:)\Avg. Disk sec/Read" "\LogicalDisk(C:)\Avg. Disk sec/Write" "\LogicalDisk(C:)\Current Disk Queue Length" -si 15 -sc 60`
     ("over 0.020 s means the disk itself is slow").
- **Where.** STO09's "do this" and the hand-over text (`sto09Do`, `web/src/lib/handover.ts`). Mark that
  step as done on the SQL Server machine (`serverMachineChecks`), as STO01 is.
- **Size.** Small: text and data kakapo already has.
- **On the machine?** The typeperf run and the antivirus check are. kakapo only writes the request.

### 5. Error 833: I/O requests that took over 15 seconds (small, permission-dependent)

- **What the user sees.** *"SQL Server logged 14 disk requests that took over 15 seconds in the last 7
  days (last at 10:42 on C:\…\tempdb.mdf)."* This shows stalls, which averages hide, and it is strong
  evidence to give IT.
- **Where.** Folded into STO09's evidence, or MNT06 extended if it reads better as "severe errors". Read
  with `xp_readerrorlog` (search 833 in the current and previous logs). Skip with a note when the login
  may not read the error log.
- **Size.** Small. It needs the error-log read permission (on older versions, securityadmin or
  sysadmin), so it is skipped on least-privilege logins.
- **On the machine?** No, when the login is allowed to read the error log.

### Left out on purpose

- **Pending I/O requests.** Internal column, a momentary snapshot, and it misled the chat.
- **Memory and missing-index advice.** INS02/INS03, PRS01/02, WRK03 and QRY01 already cover it, and
  STO09 already points to them.
- **More tempdb files.** TMP01/TMP02 already say eight equal files are fine.
- **Reading PerfMon disk counters directly.** That would need kakapo running on the server, or remote
  WMI. #4's typeperf line gives the same answer without new machinery.
