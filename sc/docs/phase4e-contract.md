# Phase 4e contract: the TempDB file set, its growth history, and progress for long fixes

Two parts, two agents. Sources: SCOPE section 3 item 10; `docs/phase3b-contract.md` (TMP01 to TMP03);
`docs/phase4d-contract.md` (autogrowth, add-tempdb-files, checkdb-now); the registry of
`docs/phase4c-contract.md` section 0. The tempdb reference the user supplied (2026-11-04) agrees on the checks
(equal files, fixed growth, headroom, what fills it, contention) and is followed on those; its fixed sizes
(1 GB per file, 8 GB total) are not: the right size is what the site's workload has already reached.

## Part 1: the TempDB file set (`tempdb-files`) and growth evidence

**Reads** (storage instance batch or a new TRY area in `collect/storage_actions.go`): every tempdb file
(logical name, type, size MB, growth and whether percent, max size, physical path, volume free MB as the
storage facts already know volumes), and from the default trace, the last 7 days of growth events for
tempdb's files (count, data vs log, the slowest growth in ms with its file and time, the last growth), the
way `TraceGrowth` does it for the user database. `StorageFacts` gains `TempdbGrowth *TraceGrowth` (nil when the
trace is off or unreadable, with the reason in the existing trace note).

**TMP02** gains a second trigger: tempdb grew 20 or more times in 7 days (rank 4; 100 or more: rank 3):
"TempDB grew {n} times in the last 7 days ({data} data, {log} log growths; the slowest took {s} s): its files
start smaller than the workload needs, so users wait while they grow." Why: each growth pauses whoever
needs the space; TempDB is recreated at its configured size on every restart, so a size it grows to every
day should be its starting size. Do this: the fix below. Both TMP02 triggers carry `fixId: tempdb-files`.
The growth numbers go in TMP02's evidence whenever they exist, even when the finding fires for unequal
files.

**The fix** (`tempdb-files`, scope server, check TMP02, title "Set up the TempDB files"), registered through
`registerFix` in `service/storage_actions.go` (your file) with its domain in `domain/storage_actions.go`:

| Input | Kind | Default | Rule |
|---|---|---|---|
| `dataSizeMB` | text | the largest data file's size, rounded up to 64 MB; never under 64 | 64 to 262144; a file already larger keeps its size and the preview says so |
| `dataGrowthMB` | text | 512 when `dataSizeMB` >= 1024, else 64 | 64 to 4096 |
| `logSizeMB` | text | the log's size rounded up to 64 MB, at least 256 | 64 to 262144 |
| `logGrowthMB` | text | 256 when `logSizeMB` >= 1024, else 64 | 64 to 4096 |

Statement, one batch: `ALTER DATABASE tempdb MODIFY FILE (NAME = N'{f}', FILEGROWTH = {g}MB)` for every
file, with `SIZE = {s}MB` added only for files smaller than the target (SQL Server refuses a smaller SIZE),
and `MAXSIZE = UNLIMITED` where a file is capped. Changes rows: one per file "{name} | {size} MB, growth {g}
| {target} MB, growth {g'}" (a file kept at its larger size says "kept at {size} MB"), plus "Disk room needed
| | {sum of increases} MB on {volume} ({free} MB free)". Warnings: not enough room blocks (no Changes); a
file larger than the target is left alone ("to make them equal, raise the size to {largest}" or shrink it
by hand in a restart window); instant file initialization off (INS07) means the increase is zeroed first;
takes effect at once and online; the new starting sizes apply after every restart too; needs ALTER on
tempdb (sysadmin). Read back `tempdb.sys.database_files`. Rollback: the previous growth values (sizes
cannot be rolled back; say so). Change row object "TempDB files", type SETTING.

`AvailableStorageFixes` lists it once with no options. The add-tempdb-files preview gains a line pointing at
this fix when the existing files are unequal.

## Part 2: progress for long fixes

**Route** `GET /api/servers/{id}/databases/{db}/progress` (`api/progress.go`, stub in place): the long
operations in progress on the instance from `sys.dm_exec_requests` joined to `dm_exec_sessions`: command in
(`DBCC`, `DBCC TABLE CHECK`, `DBCC CHECKDB`, `BACKUP DATABASE`, `BACKUP LOG`, `RESTORE DATABASE`, `RESTORE LOG`,
`CREATE INDEX`, `ALTER INDEX`, `UPDATE STATISTICS`, `DbccFilesCompact`, `DbccSpaceReclaim`, `ALTER DATABASE`)
or `percent_complete > 0`, for this database or tempdb or with no database (a server fix), with:
`sessionId`, `command`, `database`, `percent` (0 when SQL Server reports none), `elapsedSeconds`,
`remainingSeconds` (from `estimated_completion_time`, 0 when none), `startedAt`, `waitType`, `program`
(the application name), `mine` (true when the program name is kakapo's). One tagged SELECT in
`collect/progress.go`, 2-second server-side timeout, returns `[]` when nothing runs. SQL Server reports a
percentage for CHECKDB, backups, restores and shrinks, and none for index creation and statistics: the
JSON says so by `percent` 0, and the UI shows elapsed time then.

**The dialog** (`components/fix-dialog.tsx`, the applying state only): for a fix in the long set
(`checkdb-now`, `create-index`, `drop-index`, `update-statistics`, `update-statistics-table`,
`backup-schedule` with run-now, `tempdb-files`), while Apply runs it polls the route every 2 seconds and
shows, under the spinner, a progress bar with the command, the percentage and "about {m} min to go" when a
percentage exists, else "{command} running for {s} s"; "Starting…" until the first matching row; the bar
disappears when the apply returns. It matches the row by `mine` first, then by command and database. The
done state keeps the time it took. Nothing is polled for the other fixes. `api.ts` gains the `Progress` type
and `api.progress(serverId, db)`; the mock answers with a row that advances 20% per call.

**The Backups card's CHECKDB panel** and the Storage card's room panel need no change: the dialog carries the
bar.

## Tests

Domain: the tempdb statement for equal and unequal sets, the size kept for a larger file, the room block,
the defaults from the files, TMP02's growth trigger and evidence, `AvailableStorageFixes`. Collect: the
reads are tagged and read-only. Service: both registered; a bad size gives no Changes. API: the progress
route's status codes and `[]` on nothing. Web build and lint.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: TMP02 shows the tempdb growth evidence
(stage with a few large sorts in tempdb after shrinking a file, or accept "no growths" when the trace holds
none); the tempdb-files preview proposes the largest file's size and fixed growths, and applies; the CHECKDB
run on a database shows the bar moving in the dialog.
