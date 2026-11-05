# Phase 5k contract: Performance, better or worse than usual, and what made the difference

Phase 5j answers "is this query still a problem?" on the query page. The Performance screen needs the same for
the whole database: is it better or worse than usual, and what did that, so that the person who tunes sees the
effect of their work, and anyone sees whether the server got better or worse on its own. Approved as a mockup in
the review of 5 Nov 2026 on `OldSchool`.

Nothing is written to the server; every read is a SELECT. No thresholds of our own beyond those kakapo already
has: 5c's verdict ratios (0.8 and 1.25) and 20-run floor, the business hours (08:00 to 17:00), the load strip's
days of the same kind (5i).

Facts measured on `OldSchool` (5 Nov 2026, business hours 08:00 to 17:00):
- Working days: 1.25 to 1.58 million queries, 21.9 to 28.2 ms of CPU per query, 5.2 to 10.2 threads waiting.
  Thu 5 Nov: 1.65 million, 6.3 ms, 0.5. Wed 4 Nov, a public holiday: 1.07 million (69% of the median working
  day's queries per hour) and 18% of its CPU. 15 and 16 Oct: 5 thousand queries (Query Store barely captured).
- CPU per query alone also falls on a holiday (only cheap background work ran): the mix of work changes the
  average. Each query against itself, before and after its new plan, cannot be fooled by the mix.
- In 7 days 133 query shapes got a new plan and ran at least 20 times before and after it: 96 faster, 13
  slower. At their usual number of runs the faster save hours of CPU a day, matching the fall in business-hours
  CPU from about 11 hours to about 3.
- `sys.objects.modify_date` dates the change just before most of them: an index on MasterItemAlias (the view
  API_QueryTrackingEntities reads it, `sys.sql_expression_dependencies`), DocumentDetail and Document changed
  on 3 Nov 20:28 and 20:36, the procedure Utility_Document_PickSequence changed at 17:54 (it got 7x slower).
  A table's modify_date moves with an index created or dropped and an ALTER TABLE.

## 1. Better or worse than usual

- **The day judged**: today, when it has at least two complete business hours that Query Store holds; else the
  last day before that has (said as "Yesterday" or its date).
- **The hours**: its complete business hours. **Usual**: the same hours of the days of the same kind (working
  or weekend) in the 7 days before it, quiet days left out (part 3); at least 2 days, else no verdict and why.
- **Three figures**, each with the usual range (lowest to highest) and its change against the usual median:
  work asked (queries an hour), CPU per query, queries waiting (threads, Query Store's waits; left out when
  the waits are not read).
- **Verdict**: Worse when CPU per query is 1.25x the usual or more, or queries wait 1.25x more and at least one
  thread on average; Better when CPU per query is 0.8x or less and waiting is not worse; else As usual.
- **One sentence**: "As busy as usual today, with 73% less CPU per query and 92% less waiting." (busier or
  quieter at 1.25x and 0.8x of the usual queries an hour).
- **The days**: one bar per day of the 7 before and the day judged, CPU per query in business hours, working
  days blue, weekends and quiet days grey and left out, days without data empty, with how many queries got a
  new plan that day (part 2).

## 2. What made the difference

- From QRY01's batch (the same 7 days of Query Store, kakapo's noise left out): every query shape whose plan in
  use (the one that ran last) began in the window and that ran at least 20 times before and since; 5c's
  `TuningVerdict` on CPU per run says faster or slower.
- **CPU a day**: the CPU per run saved or added, times the query's runs a day over the window (its usual
  number of runs).
- **Totals**: how many got faster and the CPU a day they save; how many got slower and the CPU a day they add.
- **Got slower**: all of them, most CPU first. **Got faster**: the 5 that save the most. Each: the procedure or
  "an ad hoc query" and its text, since when, CPU a run before and now, runs since, and "after: ..." the latest
  change kakapo saw in the 24 hours before it began (up to the end of its first hour): a table, view, procedure,
  function or trigger whose `modify_date` moved, matched by the query's own procedure, a name in its text, or a
  view or module in its text that references the changed object. Never "because of". Investigate opens the
  query page.

## 3. Quiet days

A day of today's kind is left out of the usual range when, in business hours and against the median of the other
days of its kind, its queries per hour are under a quarter (5i: Query Store barely captured), or under three
quarters **and** its CPU per hour under half (a holiday: less work asked and less cost). A day with a good fix
keeps its queries and stays in. The load strip uses the same rule. The key says which days were left out and why.

Found while building: over the whole day the holiday ran 88% of a working day's queries (background work runs
around the clock), and with the holiday inside its own median 76%; in business hours against the other days,
69% of the queries and 18% of the CPU. Hence business hours, and each day against the others.

## 4. The card, and the waits

- The database page's Performance card gains a first fact: "Against usual: Better, 73% less CPU per query".
- WRK01: the waits that are small against uptime are one Info line ("Waits since restart are small: ..."),
  with each wait's time in minutes, not five rows of "0 hours". WRK01, WRK02 and PRS01 point to QRY01, not to
  the scan's WRK04.
