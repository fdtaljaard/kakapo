# Phase 5i contract: the load strip and red flags at the top of the database page

The database page opens on six cards. Before reading any of them, or changing anything, the consultant needs
two things at a glance: how loaded is this database now, compared with the last few days, and is anything
badly wrong right now. This phase puts one compact strip between the header and the cards that answers
exactly that, and nothing else. It keeps answering while the page is open: the load and the live flags
refresh every minute.

Agreed with Francois on 2026-11-05, after a first mockup that he found too big: no status words, no
costliest queries, no blocking chains, no "did the change help". Those stay in the tuning report (5c, 5h)
and the Performance card. Mockup: `docs/mockups/database-activity.html` (revision 2). Failed queries as a
flag are left out of this phase (no sourced rule yet; failed jobs are in through MNT02).

Nothing is written to the server. The strip adds no new check: the red flags are findings the cards already
raise, at the severities the cards already give them. The only new rule is the usual range (section 2),
which is the selected days' own lowest and highest values, so it carries no threshold of kakapo's own.

Sources: Microsoft Learn, "sys.query_store_runtime_stats", "sys.query_store_runtime_stats_interval",
"sys.query_store_wait_stats", "sys.dm_os_sys_info" (`cpu_count`); the 5c rules (Query Store hour by hour,
server local hours, sums not averages, Monday to Friday as working days); 5h's "cores busy" (CPU µs in the
hour / 3,600,000,000). Facts below were verified on `localhost\SQL2022` on 2026-11-05: on `OldSchool` (read
only), and in a scratch database (dropped afterwards):
- Query Store's views show the interval still in progress without waiting for the flush
  (DATA_FLUSH_INTERVAL_SECONDS 900): 40 runs were visible right after they ran. So "the last 30 minutes"
  can be read from Query Store, with no sampling of our own.
- On OldSchool, about 100k queries an hour run night and day (background work) and only 160k to 200k in
  business hours, while CPU goes from about 0.15 cores busy at night to 1.0 to 1.7 in business hours. CPU
  is the load and the number of queries is the context, in that order.
- Some days the database is barely used: 15 Oct ran its normal 106k queries an hour until 07:00 and about 600
  an hour after, 32k an hour over the day (0.245 of the median working day's queries per hour held); 16 Oct
  ran about 350 an hour. Wed 4 Nov, quiet but used, ran nine tenths. Those barely used days would pull the
  usual range down to nothing. (Corrected while building, 2026-11-05: the first draft said 15 Oct ran
  "about 600 an hour, a fifth or less"; over the whole day it is just under the quarter.)
- Query Store's 27 days on OldSchool are not continuous: it holds 9 to 16 Oct (60-minute intervals) and
  30 Oct to today (15-minute intervals), nothing from 17 to 29 Oct. So 7 and 14 days compare the same four
  working days (30 Oct, 2, 3 and 4 Nov); 30 days compares eight and leaves 15 and 16 Oct out.
- The hour-by-hour read for 30 days took 160 ms on OldSchool (27 days of 15-minute intervals).

## 1. The strip (`components/load-strip.tsx`, `components/red-flags.tsx`)

One card, full width, between `DatabaseHeader` and the cards in `screens/database.tsx`. Phone width works.
About 200 px high when there are no flags.

1. **Load now**, one line: **CPU used** ("1.7 of 8 cores"), **queries an hour** ("171k"), **threads waiting**
   ("11"), each followed in muted text by "usual {low}–{high}" for the current hour of the day. Then
   "{time} · every minute", and the days select: **7 days / 14 days / 30 days** (default 7, in the URL as
   `?days=` like the tuning report).
2. **Today against the selected days**: a 64 px chart, 24 columns, one per hour of today (server local). In
   each column, a grey band from the usual low to the usual high for that hour, with the median as a
   hairline, and today's bar on top. A bar above the band is full blue; a bar inside or below it is light
   blue; the hour in progress is hollow; hours still to come show only the band. A dashed line marks now.
   Hours under the bars at 00, 06, 12, 18. The key under it says: today; today above the usual range; "range
   of the last {n} working days at that hour" (or weekend days), and, when days were left out, "({k} barely
   used, left out)". Tooltip per column: "10:00: 1.7 cores busy · usual 0.8–1.2, median 1.0".
   Colours (dataviz skill, validated): blue `#2a78d6` / light `#86b6ef` (dark mode `#3987e5` / `#1c5cab`),
   the band neutral grey; plain CSS columns as `HourChart`, no chart library.
3. **Red flags** (section 3): at most three lines, Critical first, each with a red or orange icon and the
   word Critical or High for screen readers, the finding's own sentence, its source in muted text ("Storage
   · STO01") and one button. Then "and {n} more", linking to the first card that has one. When there are
   none: one line with a green check, "No red flags".

## 2. The load (`collect/load.go`, `domain/load.go`)

**The read.** `collect.LoadHours(ctx, db, database, from)` runs inside the database, SELECTs only, tagged,
each area in its own TRY, Query Store views only, the `OBJECT_ID` and 2017 guards as 5c:

| Area | Read |
|---|---|
| About | Query Store's actual state, interval length, the oldest interval held, the server's UTC offset, GETDATE, `cpu_count` |
| Hours | per server-local hour from `from`: executions, CPU µs (sums over plans, as 5c), and the hours held |
| Waits | per hour, wait seconds from `query_store_wait_stats` (2017+), the categories 5c reads, summed |
| Now | the interval in progress and the one before it: executions, CPU µs, wait seconds, and their start |

The 5c batch keeps its own read; the shared SQL (the hour bucket, the server-local conversion) moves into
one fragment that both use, so the hour is bucketed one way only.

**The comparison (`domain/load.go`, pure).**
- **Today** is the server's date. Its hours are complete or in progress. CPU used in an hour is cores busy (5h's
  formula). For the hour in progress, cores busy is CPU so far / time so far.
- **The kind of day**: Monday to Friday is a working day, Saturday and Sunday a weekend day (5c's
  definition). Today is compared only with days of its own kind.
- **The days compared**: the days of today's kind in the selected 7, 14 or 30 calendar days before today
  that Query Store holds, each with the hours it holds (a day Query Store started partway through counts for
  the hours it has).
- **Barely used days are left out**: a day whose queries per hour held are under a quarter of the median
  day's are left out of the range and counted in the key. On OldSchool this leaves out 15 and 16 Oct
  (a fifth or less) and keeps Wed 4 Nov (nine tenths). The quarter is kakapo's own line, set on that
  measurement and written in the code beside it.
- **The usual range** for an hour of the day: the lowest and the highest value of that hour across the days
  compared, and the median. For each of the three measures: cores busy, queries an hour, threads
  waiting (wait seconds / 3,600, 5c's tile).
- **Now**: the interval in progress and the one before it, as rates over their elapsed time, so "now" is
  the last 15 to 30 minutes with 15-minute intervals (the last 60 to 120 with 60-minute ones; the line then says
  "since {start}"). Compared with the usual range of the current hour.
- **Too little to compare**: fewer than 2 days of today's kind are left after the barely used ones: no band
  and no "usual", the bars only, and the key says "Query Store holds {n} days like today here; the usual
  range needs 2."
- **Query Store off** (or never on): the load line says "Query Store is off, so kakapo cannot show the
  load." with Apply: the `query-store` fix (phase 1). The red flags still show. **Read only** (full or
  set): the hours it holds, and the key says since when nothing new was recorded, with the fix.
  **2016**: no threads waiting.

**The routes.** `GET /api/servers/{id}/databases/{db}/load?days=7` (7, 14 or 30; 400 otherwise) returns
`Load` (JSON): `now` (the three measures with their usual low, high and median, `since`, `cpuCount`),
`hours` (24 rows: hour, cores, queries, threads, low/high/median per measure, `inProgress`, `future`),
`kind`, `daysCompared`, `daysLeftOut`, `queryStore` (state, interval, oldest), `notes`, and `liveFlags`
(section 3); as built also `above` per hour, `message` (the off line), `chart`, `key` (the band's line,
written by the domain), `action` (the query-store fix) and `liveError` (the live checks could not be read:
the page keeps the overview's). The live checks are their own small batch at the instance
(`collect.LiveFacts`), beside the Query Store batch that runs inside the database. The service keeps the days compared in memory per server, database, days and server date, so
the minute refresh (`&refresh=1`) reads only today's hours, the Now area and the live flags. Nothing is
stored in the case file in this phase.

**The refresh.** The page asks for `refresh=1` every 60 seconds while it is open and the tab is visible
(`document.visibilityState`), and stops while hidden. A failed refresh keeps the last strip and shows
"not refreshed since {time}" in place of the time.

## 3. The red flags (`domain/redflags.go`)

A red flag is a finding the cards already raise, from this list only, at the severity shown. No other
High finding is a red flag. Any Critical finding of any card is.

| Flag | Checks | At |
|---|---|---|
| Blocking now | ACT01 | High |
| Pressure now (threads, memory grants, schedulers) | PRS01 | High, Critical |
| Deadlocks | WRK06, DLK01, BLK01 (the last two from the stored reports, as the card) | High |
| Waits that stop work (THREADPOOL, RESOURCE_SEMAPHORE) | WRK05 | High |
| Space | STO01, STO04, STO11, STO12, TMP03 | High |
| Backups | BAK01, BAK02, BAK05, MNT02 (a failed backup job) | High |
| Damage, and anything else | any card | Critical |

`BuildRedFlags(findings map[CardID][]Finding) RedFlags` keeps those, sorted Critical first, then in the
table's order, then the cards' own rank. It returns the first three (`flags`), how many more there are
(`more`) and those others in order (`rest`, which the page needs when the live flags replace the overview's).
A RedFlag is `{checkId, card, severity, sentence, object, to, rank}`: `sentence` is the finding's own
sentence; `to` is the investigation route the card already gives the finding (ACT01: the blocking page), else
the card page, relative to the database page; `rank` is the sort key the domain computed, so the page orders
the live flags in without a rule of its own. (Built as `BuildRedFlags`: Go cannot name a function like the
type it returns.)

**Where they come from.** `Overview` already builds every card's findings on page load. It gains
`redFlags` (built from those, no new read). The minute refresh re-reads only ACT01 and PRS01. Their SQL
blocks move out of `collect/performance.go`'s batch into one fragment that the Performance read and the load
read both run, and `domain.BuildPerformanceCard`'s rules for the two are called from one function, so the
strip and the card can never disagree. The page puts the live ACT01 and PRS01 flags in place of the
overview's and keeps the rest until "Check again".

## Tests

Domain: today's kind (a weekday, a Saturday); the days compared for 7, 14 and 30 days, with a gap in Query
Store and a day Query Store started partway through; barely used days left out at under a quarter and kept at
nine tenths (OldSchool's shape as a fixture); the range, the median and "above" per hour; cores busy for a
complete hour and for the hour in progress (CPU so far / time so far); Now from two intervals at 15 and at 60
minutes; fewer than 2 days compared; Query Store off, read only, 2016 (no threads). Red flags: each listed
check at High is a flag and at Medium is not; a High finding of an unlisted check is not; any Critical is;
the order; three shown and "more"; the live ACT01 and PRS01 replace the overview's. Collect: the load read is
tagged, read-only, each area in its own TRY, the 2017 guard; the hour fragment is shared with 5c (5c's tests
still pass); the ACT01 and PRS01 fragment is shared with the Performance read (its tests still pass). API:
`days` validation, `refresh=1` reads only today and the live flags. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`:
- **OldSchool** (Query Store, 27 days, 15-minute intervals; the database has been idle since the restore at
  09:16 today): the strip shows today's bars low after 09:00 and under the band; with 30 days the key says
  "2 barely used, left out" (15 and 16 Oct) and Wed 4 Nov is in the range; the usual range at 10:00 and
  the hour totals match a hand query over `sys.query_store_runtime_stats` for the same days; 7 / 14 / 30
  change the band and the URL. The red flags are the listed High and Critical findings the six cards show
  for OldSchool, the same sentences, at most three; or "No red flags" when there are none.
- **Blocking, in a scratch database**: one session holds an open transaction on a row, a second waits on
  it for over 30 seconds. Within a minute, without "Check again", the strip shows the blocking flag first,
  with its button to the blocking page. Commit, and within a minute it is gone.
- **A database with Query Store off**: the load line says so with the Query Store Apply, and the red flags
  still show.
- Switching to another tab stops the refresh (no load read in the log), and coming back resumes it.
