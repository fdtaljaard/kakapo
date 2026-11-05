# Phase 5h contract: did my change help? (the changed object's own numbers)

The people who use kakapo are the people who change the database: mostly procedures and views, and from now
on indexes and tuning too. When they ask "did it help?" they mean the thing they changed. The tuning report
(phase 5c) answers for the whole database and lists the 15 queries that cost the most CPU, so a procedure
outside that top 15 gets no answer. This phase puts the changed object first: its own numbers before and
after, hour by hour, in one sentence and one small chart. It also adds the database's CPU load to the chart.

Builds after 5f (changes made in SSMS reach the change log); works without it on kakapo's fixes and on
changes recorded by hand. Nothing is written to the server; the read is SELECTs only, as 5c.

Sources: Microsoft Learn, "sys.query_store_query" (`object_id`), "sys.query_store_plan" (`query_plan`),
"sys.dm_db_index_usage_stats" (counters reset when SQL Server restarts); the 5c rules (windows, sums not
averages, `TuningVerdict` and its 20-run floor). Facts below verified live on `localhost\SQL2022` on
2026-11-05 in a scratch database with Query Store on (dropped afterwards):
- `ALTER PROCEDURE` keeps the object's `object_id`. The altered statement becomes a new query (new
  `query_hash`); the unchanged statements keep theirs. So the object's history runs across an ALTER when it is
  read by `object_id`, and not when it is read by query.
- `DROP` and `CREATE` give the object a new `object_id`: Query Store's history of the old one cannot be tied to it.
- A query that selects from a view stores the view's name in its text; its `object_id` is the calling
  procedure's (or 0), never the view's.
- A plan that uses an index names it in its XML (`Index="[IX_T_V]"`), also the plan of the `CREATE INDEX`
  statement itself. The same query has an old plan without the index and a new one with it.
- `sys.dm_db_index_usage_stats` counted 60 seeks on the new index after 60 runs.

## 1. The object the report follows

The centred change (5c's select) has an `object` and a `change_type`. The report follows it when:

| Change type | What is followed | How it is found |
|---|---|---|
| PROCEDURE, FUNCTION, TRIGGER | the object's statements together | `query_store_query.object_id = OBJECT_ID(@object)` |
| VIEW | the queries that name the view | query text contains the view's name as a whole word, with or without schema or brackets |
| INDEX | the queries whose plan uses the index | plan XML contains `Index="[{index}]"` and `Table="[{table}]"`; the `CREATE INDEX` and `ALTER INDEX` statements left out |
| DEPLOY (5f) | each procedure, function and trigger named in the row's `statement` | as PROCEDURE, up to 15 names |

`object` is `schema.name`, or `schema.table.index` for an index. **5f's index rows must use that three-part
form** (one line to add to 5f's "Columns per row"). A row recorded by hand is parsed the same way; a bare name
gets schema `dbo`. Any other type (SETTING, STATISTICS, PLAN, …) is not followed and the report stays as today.

## 2. The read: one more area in 5c's batch

`tuningReportQuery` gains parameters `@focusKind` (`""`, `module`, `view`, `index`) and `@o1` to `@o15` (the
names, passed as parameters, never concatenated), and one more area in its own TRY (`@focusErr`), run only when
`@focusKind <> ''`, from the `#rs`, `#int` and `#plans` already filled. Two result sets more:

- **Focus items:** per name: the name as given, `object_id` (NULL when not found), the object's
  `create_date` and `modify_date` (`sys.objects`). For an index, from `sys.indexes` and
  `sys.dm_db_index_usage_stats`: seeks, scans, lookups, updates, the last use, and `sqlserver_start_time`
  (since when the counters run).
- **Focus hours:** per item and hour: CPU (µs), failed runs (`execution_type` 3 and 4) and runs. For a module,
  runs is **the executions of its most-run statement in that hour**, which counts calls of a procedure whose
  statements all run once a call. For a view or an index, runs is the matched queries' executions summed.

The index match reads plan XML (`CONVERT(nvarchar(max), query_plan) LIKE …`) of the window's plans only, and
only when the centre is an index. `#plans` keeps every plan of the window (it already does).

## 3. The comparison (`domain/tuning.go`)

`TuningFacts` gains `FocusError`, `FocusItems` and `FocusHours`. `TuningReport` gains `Focus *TuningFocus`:
the object, the kind, found or not, before and now (CPU per run, runs per business hour, CPU per business
hour, failed runs), the verdict, the hours for its chart, the index use, and for a DEPLOY one row per object.

- **The same Before and Now as the report** (5c's `tWindows`), and the same sums-not-averages rule.
- **The verdict is `TuningVerdict`** on CPU per run (faster, slower, same, new, gone, too few), so the object
  and the per-query table never disagree.
- **A DEPLOY** lists its objects, sorted by CPU added or saved per business hour, with the verdict per row;
  the sentence names the faster and slower ones by name (at most 3 each), as 5c does for queries.

**The sentence comes first** in Explain, before 5c's database headline. Only one applies:

- Faster or slower: "Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses 8.6 ms of CPU per
  run instead of 12.4 ms (31% less) over 9 business hours, and it runs about as often (3,150 runs an hour
  against 3,200): the change made it cheaper." When slower: "…18 ms instead of 12.4 ms (45% more)…: Investigate
  it." Investigate opens the query page of its costliest statement; Copy carries the numbers for the vendor.
- Same: "…uses about the same CPU per run as before (12.1 ms against 12.4 ms): the change made no
  difference that Query Store can see."
- Runs changed a lot (the 5c `tExecsClause` rule): the clause says the object runs more or less often, so its
  load changed even if each run did not.
- Index used: "Since index IX_T_V was created on dbo.T on Tue 3 Nov 14:20, the 4 queries that use it take 0.9 ms
  of CPU per run instead of 22 ms. It was used 60 times (60 seeks); SQL Server also updates it on every
  write to dbo.T (1,200 updates so far)."
- Index not used: "Index IX_T_V on dbo.T has not been used since it was created on Tue 3 Nov 14:20; SQL Server
  still updates it on every write to dbo.T (1,200 updates). The counters start again when SQL Server restarts
  (last on {date})." Copy. No advice to drop: kakapo has no sourced rule for that yet.
- Too early: 5c's "too early to say" rule (Now under two complete business hours), with the time to look again.
- Not found: "kakapo cannot find {object} in {database}, so the report compares the whole database." When the
  object's `create_date` is at or after the change and Query Store holds nothing under it before: "dbo.GetOrders was dropped and
  created again on {when}, so Query Store sees a new object and holds nothing before it. ALTER (or CREATE OR
  ALTER) keeps an object's history."
- Nothing before: "Query Store holds no runs of dbo.GetOrders before the change in the last {n} days."
- A DEPLOY: "Of the 40 objects in the deploy by DOMAIN\build on Tue 3 Nov 14:20, 3 got slower and 2 got faster;
  the rest ran too little to say. Slowest: dbo.GetOrders, 18 ms per run instead of 12.4 ms."

## 4. The load on the chart

`TuningHour` gains `CoresBusy` = the hour's CPU (µs, already read) / 3,600,000,000: the cores this database
kept busy on average in that hour. The chart gains a third row, **"CPU used by this database (cores busy)"**,
under Executions, with the same shading and change markers. The tooltip: "0.4 cores busy". The legend says it
is this database's queries only: other databases and SQL Server's own work are not in it, and SQL Server keeps
only about four hours of server-wide CPU (the scheduler monitor ring buffer, as the live sample reads it), so
a week of server CPU is not available without collecting around the clock, which kakapo does not do.

## 5. The page (`screens/tuning.tsx`)

- The Explain sentences: the object's sentence first (section 3).
- **A new section before "Is it better than before?"**, titled "Did it help {object}?": three numbers (CPU per
  run, runs per hour, CPU per hour), before and now, with the verdict word in colour; under them the object's
  own chart, CPU per run per hour with the change marker, reusing `HourChart`'s bars; for an index, the use line;
  for a DEPLOY, the table of its objects, Investigate per row. Hidden when nothing is followed.
- Copy for IT (`TuningHandover`) starts with the object's block.
- The mock gains a procedure altered mid-week that gets faster and a new index that is used.

## Tests

Domain: the name parse (two and three parts, brackets, bare name); which types are followed; the module's runs
as its most-run statement; Before and Now from 5c's windows; each sentence (faster, slower, same, index used,
index not used, too early, not found, dropped and created, nothing before, deploy); a DEPLOY's sort and its
15-name cap; `CoresBusy`. Collect: the focus area is tagged, read-only, in its own TRY, uses `#rs`, `#plans` and
`#int` (no second scan of Query Store), names passed as parameters, plan XML read only for an index. API: the
report with and without a followed change. Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in a user database with Query Store on: run
a procedure in a loop for an hour, ALTER it to be cheaper, run it for two more business hours; the report
centred on the ALTER says it got cheaper, with its own chart, and the procedure need not be in the top 15.
Create an index that a query uses: the report centred on it names the queries that use it, before and after,
and the seeks. Create one that nothing uses: the "not used" sentence. The chart shows the cores-busy row.
