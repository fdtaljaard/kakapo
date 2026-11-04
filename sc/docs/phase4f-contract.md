# Phase 4f contract: long fixes run in the background

Found in the sandbox (2026-11-04): a CHECKDB of 6.4 GB finished on the server (LastGoodCheckDbTime moved)
but the dialog stayed at "DBCC TABLE CHECK 99%, Applying…". The apply holds one HTTP request and one SQL
connection for the whole run; over a client network a long idle connection can drop without an error, the
Go side then waits for a message that never comes (up to the 4-hour timeout), and the dialog keeps the last
progress row it saw. Two changes.

## 1. Long fixes return at once and run in the background

The long set: `checkdb-now`, `create-index`, `drop-index`, `update-statistics`, `update-statistics-table`,
`tempdb-files`, and `backup-schedule` when `runNow` is on (the job start is quick; the Agent job is what
runs long, so this one stays synchronous but its result says the backup is running under the Agent).

`service/runs.go`: an in-memory run registry. `ApplyFixWithOptions` for a fix in the long set (the registry
entry declares `long: true`; add that flag to `registerFix` or a parallel set) prepares the fix as today,
then starts the apply in a goroutine with `context.WithoutCancel` plus the fix's own timeout, keeps the
`genericFix` (its connection) for the goroutine, and returns a `FixResult` with `Pending: true`, `RunID`,
`StartedAt` and the preview. The goroutine runs the existing apply function unchanged and stores the
`FixResult` or the error on the run. Runs are kept for 24 hours after they end, 20 at most per exe;
nothing is persisted (the stored outcome of CHECKDB, the change rows and the read-backs already are).

`GET /api/servers/{id}/databases/{db}/fixes/runs/{runId}` (`api/runs.go`) returns `{runId, fixId,
database, startedAt, pending, result?, error?}` with the error as the apply would have answered it (its
status code in `error.status`). 404 for an unknown run.

**Completion detection when the connection dies.** The goroutine's apply may itself hang on a dead
connection. Each long fix gets a `completed(ctx, db, g) (bool, string)` check the run uses when the apply has
not returned and the progress read shows no request of kakapo's own for that database for 60 seconds:
CHECKDB: `LastGoodCheckDbTime` later than the run's start (clean) or an error 8989 in the error log is out
of reach, so a later last-good means clean and no last-good after the request vanished means "finished
with a result kakapo could not read: look at the SQL Server error log"; create-index/drop-index: the index
exists / is gone; statistics: `stats_date` after the start on the table(s); tempdb-files: the files at the
target. On completion by that path the run writes the change row and the stored outcome as the apply would,
with the note "the connection was lost during the run; the result was read back afterwards", and the dead
connection is closed. `backupActionTimeout` and the 4-hour limit stay as the outer bound.

**Connection keepalive.** `collect.ConnString` adds `keepAlive=30` explicitly (go-mssqldb's TCP keepalive
in seconds) so an idle TCP connection through a firewall or VPN is kept open during a long run.

## 2. The dialog

- On a `Pending` result the dialog keeps the applying state, polls the run every 2 seconds (the progress
  route stays as it is for the bar) and switches to the done state with the run's result, or to the error
  state with its error, when the run ends. Cancel is not offered while a run is pending; Close is: the run
  continues on the server and the card shows the outcome on the next read. Closing the dialog while pending
  shows a toast-like line on the card page "CHECKDB is still running on {db}; the result appears on the
  Backups card" (a simple inline notice is enough).
- The bar never freezes: when the progress row of kakapo's own session disappears while the run is still
  pending, the line reads "Finishing: waiting for SQL Server's result…" with an indeterminate bar.
- The exe exits 10 seconds after the last window closes (presence), which would kill a run: while a run is
  pending, presence counts it as a window (`Presence` gains a "busy" count the service raises and lowers), so
  the exe stays up until the run ends, then follows the normal rule.

## Tests

Service: a fake long fix that blocks on a channel: Apply returns pending at once; the run route reports
pending, then the result once released; the error path; the registry's 20-run cap and expiry; the
completion-detection path with a fake `completed`. API: the run route's codes. Presence: busy keeps the
exe alive. Web build and lint.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: Run CHECKDB now returns pending at
once, the dialog polls and ends with "No errors found" and the time; closing the dialog mid-run and reopening
the Backups card shows the outcome when it lands; the exe stays up with the window closed while a run is
pending (checked with a staged slow statement: `WAITFOR DELAY '00:00:40'` behind a fake long fix in a test,
or a CHECKDB on a copy of a bigger database).

## Root cause, found live (2026-11-04)

Two defects, both in kakapo, neither in the network:

1. `connection timeout=30` in the connection string. go-mssqldb applies it as a deadline on every socket read
   and write, so any statement that sends nothing for 30 seconds dies with an i/o timeout. A CHECKDB of a few GB
   does exactly that. Now `connection timeout=0`: contexts govern every read and fix, and the connection test
   bounds itself (20 s).
2. The message-queue loop (`sqlexp.ReturnMessage`) for the CHECKDB run. The driver reads the response on a
   goroutine that hands tokens over a channel of five; CHECKDB sends more than five tokens without a message
   before its DONE, the reader stalls on the full channel, and a loop waiting for a message waits for ever. A
   short check slips under five tokens, which hid it locally. CHECKDB now runs `WITH TABLERESULTS` and its
   findings come back as rows through the normal driver path.

Also: the last-good read used by completion detection opens an AUTO_CLOSE database first, or it reads NULL.
Verified: a 46-second CHECKDB on Laylax returns pending at once, the bar climbs, and the result is stored.
