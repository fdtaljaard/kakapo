# Phase 5d contract: a week of blocking history

The Performance card's blocking page shows the chain right now and for 30 seconds. The client's question is
"what blocked us on Tuesday". SQL Server's own `system_health` session records, every five minutes, the
blocking it sees at that moment inside `sp_server_diagnostics_component_result` (the QUERY_PROCESSING
component carries `blockingTasks/blocked-process-report` nodes). That is a week of history nobody has to
install, read the way Erik Darling's block viewer reads it (`sp_HumanEventsBlockViewer.sql` 1355-1412 for the
file read and 2092-2160 for the XPaths; take the rule, write our own SQL, mark the fragments MIT). Its limit is
honest and stated: a block shorter than the five-minute snapshot is missed; the kakapo Extended Events
session of phase 5e fills that gap when a site allows it.

## The read, on request

`POST /api/servers/{id}/databases/{db}/cards/performance/blocking-history` (`{days: 7}`, 1 to 7) reads the
`system_health` file target (falling back to the ring buffer as the deadlock read does) for
`sp_server_diagnostics_component_result` events in the window whose data holds a `blocked-process-report`,
and for each report extracts, as the viewer does: event time (server local), the blocked process (spid,
login, host, program, database, wait time ms, wait resource, lock mode, isolation level, transaction count,
status, the input buffer) and the blocking process (spid, login, host, program, status, isolation level,
transaction count, last transaction started, the input buffer), the object behind the wait resource when it
is a KEY, PAGE, RID or OBJECT resource in this database (resolved through `sys.partitions`/`sys.objects`
with the hobt id), and the monitor loop. Same `fn_xe_file_target_read_file` filter on `object_name` as the
deadlock read, 5-minute timeout, tagged, read-only.

`domain/blockinghistory.go` builds `BlockingHistory` (JSON) and stores it in a new table
`blocking_report` (one row per run, JSON; migration 11, additive):

```
{ capturedAt, days, source: "file"|"ring-buffer", snapshots, reportsTotal, missedNote,
  thresholdSeconds,                       // blocked process threshold (s) from sys.configurations; 0 means only the 5-minute snapshots exist
  leaders: [ { key, blockerStatement, blockerProgram, blockerLogin, blockerHost, object, database,
               occurrences, snapshots, longestWaitSec, totalBlockedSessions, kinds: [sleeping|implicit|serializable|long-running|cross-database],
               first, last, byDay: [{day, count}], sampleVictimStatement, sampleBlockerStatement, queryHash? } ],
  byHour: [ { hour, reports, sessionsBlocked } ],   // the week's heat line
  findings: [ BLK01 per leader, ranked ],
  notes: [] }
```

Grouping: a **leader** is the blocking process's statement (its input buffer normalised: whitespace
collapsed, literals replaced by `?`) plus the object it held, in this database; occurrences count distinct
monitor loops; `kinds` as the live blocking page classifies (a sleeping blocker with an open transaction,
implicit transactions, SERIALIZABLE or REPEATABLE READ, a statement running over 30 seconds, a blocker in
another database). `queryHash` when the statement's hash can be matched in Query Store (`query_store_query_text`
by the normalised text's hash is not reliable; match on the exact text when it exists, else leave empty).

**BLK01** (new check, Performance, clock `window`, owner Us; Vendor when the kind is implicit, serializable or
sleeping, because that is code): one finding per leader with occurrences >= 2 in the window, Medium; High
when the longest wait is >= 60 s or sessions blocked >= 10 in one snapshot or occurrences >= 10. Sentence:
"{statement or procedure} blocked {n} sessions {k} times in 7 days on {object}, the longest for {s};
{kind sentence}, the last on {when}." Why: WRK01's lock why. Do this by kind: sleeping ("the application
opens a transaction and waits for the user or a web call: commit sooner, or ask the vendor"), implicit ("the
connection runs with IMPLICIT_TRANSACTIONS on, so every statement leaves a transaction open until the
application commits: a connection-string or driver setting, for the vendor"), serializable ("TransactionScope's
default; ask the vendor for READ COMMITTED"), long-running ("the statement itself is slow: open it"), else
the index-or-RCSI wording of the live page. Evidence: the sample victim and blocker statements, the lock
mode, the by-day counts. Investigate: the query page when `queryHash` is set, else the blocking page. Copy
for the vendor.

`missedNote`: "system_health looks every five minutes, so blocks shorter than that are not in this history;
the longest and most repeated ones are. With the blocked process threshold set and kakapo's Extended Events
session (phase 5e) every block over {n} seconds is kept." when `thresholdSeconds` is 0, else "the blocked
process threshold is {n} s, but nothing records those reports yet; kakapo's session (phase 5e) will."

## Where it shows

The Performance card page gets a **Blocking history (7 days)** panel beside the deadlocks panel: the last
report with its time, the week's heat line (CSS bars per hour: reports per hour, weekends shaded), the leaders
table (statement or procedure, object, times, sessions, longest, kinds as words, last), each row with
Investigate and the BLK01 finding under it, and a "Read blocking history (7 days)" button with the note that
reading the session files can take a minute. `CardDetail` gains `Blocking *BlockingHistory` for the
Performance card (JSON `blockingHistory`), carried like the deadlock report; the card's BLK01 check status
reads `skipped` "Not run yet…" before any run, as DLK01 does. The sample's and the live page's blocking
sentences gain "see the week's history on the card" when a report exists.

The hand-over block of a BLK01 finding is the one for the vendor: the statements, the object, the counts.

## Tests

Domain: parsing two fixture reports (a sleeping blocker, a serializable one) into leaders with the right
kinds and counts; normalisation of statements; BLK01 thresholds and owner by kind; the by-hour line; the
missed note by threshold. Collect: the read is tagged, read-only and filters on `object_name`. Store: the
round trip. Service: the route's codes and the skipped status before a run. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: stage a block that lasts over five
minutes (an open transaction in sqlcmd and a reader behind it) so one `system_health` snapshot catches it,
run the read, and the panel shows the leader with its kind "sleeping" and one occurrence (under the finding
bar, since one occurrence is Info); the stored report reloads with the card; the hand-over block carries both
statements. Clean up the staged sessions.
