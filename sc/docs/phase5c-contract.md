# Phase 5c contract: the tuning report ("did it help?")

SCOPE section 3 item 4 and phase 4's second half. Source of the rules: the toolkit's own tuning report
(`../research/granite-sql/SqlTuningReport/TuningReport.sql` and its README, "How the comparison
works"), which has run on client sites; sp_QuickieStore's regression mode for the execution-weighted averages
(DarlingData, `sp_QuickieStore.sql` around 12415, MIT; mark the fragment). The report is a page, not a card:
it answers "is the database better than before, and did the change I made do it?" from Query Store's hour by
hour history and kakapo's change log, with Apply, Investigate and Copy at the end of each finding (SCOPE rule
1 and item 10). Nothing is written to the server.

## Two doors

- The database page's **Recent changes** panel: every entry gains "Did it help?", opening the report centred
  on that change. The panel also gains **Record a change** (below).
- The **Performance card** page: a "Tuning report" button in the header, opening the report centred on the
  latest change in the window, or on the latest complete business hour when there is none.

Route: `/instances/:id/databases/:db/tuning?days=7&change={id}` -> `screens/tuning.tsx`.

## Record a change made by hand

The toolkit's change log held changes the consultant made in SSMS (a procedure deployed, an index created by
hand). kakapo's change table holds only what kakapo applied. `POST /api/servers/{id}/databases/{db}/changes`
with `{changedAt, object, changeType, description}` (`changeType` one of PROCEDURE, VIEW, INDEX, STATISTICS,
SETTING, OTHER; `changedAt` server time, defaults to now) writes a change row with `Statement` empty and
`Result` "recorded by hand". The Recent changes panel gets a small form (object, type, description, when) behind
**Record a change**. The row shows "(by hand)" after its description.

## The read

`GET /api/servers/{id}/databases/{db}/tuning?days=7&change={id}`; `days` 1 to 30, default 7; `change`
optional. `collect.TuningReport(ctx, db, database, days)` runs inside the database, SELECTs only, each area in
its own TRY, tagged, Query Store views only (2016+; waits 2017+; `OBJECT_ID` guard):

| Area | Read |
|---|---|
| About | Query Store state, interval length, the oldest interval held, the server's UTC offset, GETDATE |
| Hours | per hour (server local, from `runtime_stats_interval.start_time` rounded down): executions, CPU µs, logical reads, duration µs, aborted and exception executions (`execution_type` 3 and 4), summed over plans; waits by category seconds per hour (`query_store_wait_stats`, 2017+): Parallelism, Lock, CPU, the rest as Other |
| Top shapes | the top 15 `query_hash` by CPU in the window: text (first 300 chars, one line), object, and per hour executions and CPU |
| Plan regressions | the toolkit's rule 4 exactly: queries with more than one plan in the window; newest plan (by first seen, then plan id) with >= 50 runs and CPU per run >= 1.5 x the previous plan's (previous >= 1 ms); old and new plan ids, CPU per run, runs, when the new plan was first seen; top 10 by the extra CPU |
| Failures | per query, aborted and exception counts and the last failure; top 10 |

The service adds the changes: kakapo's change rows for the database and the server-level rows (empty
database) whose `ChangedAt` falls in the window, newest first.

## The comparison (`domain/tuning.go`, pure, tested on fixtures)

Business hours are 08:00 to 17:00 on Monday to Friday, server time, as the toolkit's default; an option on the
page (`hours=08-17`) changes it. Hours are classed Business, Night or Weekend; an hour is "complete" when its
end is before GETDATE; an hour that contains a change is never compared; the hour still in progress is marked
and never compared.

- **Around a change** (the chosen one, default the latest in the window): **Now** is the complete business
  hours after it; **Before** is the complete business hours earlier on the same day when there are at least
  two, else the business hours of the five working days before the change. **Without a change:** Now is the
  latest complete business hour, Before the same hour on the five working days before.
- Every ratio is calculated from sums (CPU per execution = total CPU / total executions), never as an average
  of hourly ratios (the toolkit's rule).
- **Four tiles:** CPU per execution (ms), executions per hour, parallel threads waiting (parallel wait seconds
  / 3,600, "19 threads waiting all the time"), lock threads waiting; each with before, now and the % change,
  and a tone: better (>= 10% improvement), worse (>= 10% worse), same.
- **Same hour before and after:** each business hour of the day of the change beside the same hour on the
  previous working days (sum / count of days).
- **Per query:** the top 15 shapes with before and now CPU per execution (execution-weighted, as QuickieStore)
  and executions; a verdict per query: `faster` (now <= 0.8 x before), `slower` (now >= 1.25 x before), `same`,
  `new` (>= 20 runs now, none before), `gone` (>= 20 runs before, none now), `too few` (under 20 runs in
  either window, no verdict). Sorted by the CPU saved or added.
- **Plan regressions and failures** as read, with the hour they first appear related to the change ("the new
  plan appeared 40 minutes after the change").

`Explain` sentences first, in this order and only when they apply: the headline ("Since {change} on {when},
CPU per execution in business hours is down 31% (from 12.4 ms to 8.6 ms) over {n} hours; executions per hour
are the same, so the work got cheaper, not rarer."); the queries that got faster and slower by name; a new plan
that costs more since the change ("{query}'s new plan uses 2.1 x the CPU per run of the one before and appeared
40 minutes after the change: force the old plan, or revert"), with force-plan as its action when the old plan
still exists; failures that started after the change; "too early to say" when Now holds fewer than two
complete business hours; "no change in the log" with Record a change as the action; "Query Store is off" with
the query-store fix as the action. Each sentence that names a query carries Investigate (the query page) and
Copy for the vendor.

## The page (`screens/tuning.tsx`, phone width, sentences first)

1. Header: the database, the change it is centred on (a select of the changes in the window, "none" allowed),
   the days (7 / 14 / 30), business hours, Copy for IT (the hand-over: tiles, sentences, the per-query table).
2. The Explain sentences with their buttons.
3. The four tiles.
4. Hour by hour: a simple CSS bar chart of CPU per execution per hour (bars, nights and weekends shaded, change
   markers as a line with the change's object), executions as a second row; no chart library.
5. Same hour, before and after: a small table.
6. What is expensive: the per-query table with before -> now and the verdict word in colour, Investigate per row.
7. Did a change break anything: plan regressions and failures.
8. "Not everything could be read" notes.

The mock gains a seven-day fixture with one change and a regression so the page can be seen.

## Tests

Domain: hour classing and completeness; the window choice with a change on a weekday afternoon, on a Monday
morning (five working days before), at a weekend (the five working days rule), without a change; sums not
averages; the four tiles and their tones; per-query verdicts and the 20-run floor; plan regressions
(the toolkit's thresholds exactly); the headline sentences. Collect: the read is tagged and read-only, the
2017 guard. API: the route's codes, `days` and `hours` validation, the change POST's validation. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022` on Laylax (Query Store on): the page opens
from Recent changes on the statistics update of 3 Nov with its window, the hour-by-hour bars show the week,
the tiles read against the five working days before, a change recorded by hand appears as a marker, and the
Copy block carries the tiles and the per-query table.
