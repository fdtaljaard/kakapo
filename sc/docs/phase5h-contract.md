# Phase 5h contract: did my change help? (the changed object's own numbers)

The people who use kakapo are the people who change the database: mostly procedures and views, and from now
on indexes and tuning too. When they ask "did it help?" they mean the thing they changed. The tuning report
(phase 5c) answers for the whole database and lists the 15 queries that cost the most CPU, so a procedure
outside that top 15 gets no answer. This phase puts the changed object first: its own numbers before and
after, hour by hour, in one sentence and one small chart. It also adds the database's CPU load to the chart.

Builds on 5f (changes made in SSMS reach the change log as "seen on the server" rows) and 5i (`domain.CoresBusy`,
the shared Query Store hour bucketing); works too on kakapo's fixes and on changes recorded by hand. Nothing is
written to the server; the read is SELECTs only, as 5c.

Sources: Microsoft Learn, "sys.query_store_query" (`object_id`), "sys.query_store_plan" (`query_plan`),
"sys.dm_db_index_usage_stats"; the 5c rules (windows, sums not averages, `TuningVerdict` and its 20-run floor).
Facts verified live on `localhost\SQL2022` on 2026-11-05 (scratch databases with Query Store on, dropped
afterwards, and read-only on `OldSchool`), the second set in the Fable review of the same day:
- `ALTER PROCEDURE` keeps the object's `object_id`. The altered statements become new queries (new
  `query_id`/`query_hash`); the unchanged statements keep theirs. So the object's history runs across an ALTER
  when it is read by `object_id`, and not when it is read by query. (OldSchool:
  `dbo.Utility_GenerateLabelsAndInsertIntoQueue` kept object_id 843150049 across its ALTER of 5 Nov 08:02.)
- `DROP` and `CREATE` give the object a new `object_id`: Query Store's history of the old one cannot be tied to it.
- A query that selects from a view stores the view's name in its text; its `object_id` is the calling
  procedure's (or 0), never the view's.
- A plan that uses an index names it as adjacent attributes:
  `<Object Database="[...]" Schema="[dbo]" Table="[BarcodeMaster]" Index="[UX_BarcodeMaster_Name]" ...>`. The
  Database attribute is the database the plan was compiled in: a restored copy's plans name the original
  database, so it is never matched. The `CREATE INDEX` statement's own plan names the index too.
- Reading the plan XML of every plan in the window is too slow: on OldSchool a LIKE over 11,884 plans of 7 days
  took 483 s, past 5c's 3-minute batch timeout. A LIKE over all 23k query texts took 1.4 s.
- `query_sql_text` carries the server's collation and `query_plan` the database's; on OldSchool they differ
  (server SQL_Latin1_General_CP1_CI_AS, database Latin1_General_CI_AS) and a plain
  `query_sql_text LIKE '%' + @name + '%'` fails with Msg 468.
- `sys.dm_db_index_usage_stats`: `ALTER INDEX ... REBUILD` does not reset an index's counters on 2022 (10 seeks
  before and after); a restart, a restore and taking the database offline (detach, AUTO_CLOSE) drop the
  database's rows.
- Found while building (2026-11-05): the LIKE over every query text in the database's collation is most of the
  area's time (OldSchool: 1.7 s inside the batch); a binary `CHARINDEX` on the upper-cased text first
  (`Latin1_General_BIN2`) brings it to 0.4 s, and the 30-day read centred on the index from 9 to 10.3 s down to
  8 to 9 s. OldSchool's three changes of 5 Nov are not in this server's default trace (they were made before the
  copy was restored; its only DDL rows here are the restore's `ALTER DATABASE` events, one DEPLOY row at 09:14), so
  the check records them by hand at their `modify_date`. The one query whose plan uses `UX_BarcodeMaster_Name` is
  the procedure's `UPDATE BarcodeMaster ... WHERE Name = @BarcodeName`; the `CREATE INDEX` statement is left out.

## 1. The object the report follows

The centred change (5c's select) has an `object` and a `change_type`. The report follows it when:

| Change type | What is followed | How it is found |
|---|---|---|
| PROCEDURE, FUNCTION, TRIGGER | the object's statements together | `query_store_query.object_id = OBJECT_ID(name)` |
| VIEW | the queries that name the view | query text contains the view's name |
| INDEX | the queries whose plan uses the index | the queries whose text names the table, then the plan XML of those queries' plans in the window only: `Table="[{table}]" Index="[{index}]"`; the `CREATE INDEX` and `ALTER INDEX` statements left out |
| DEPLOY (5f) | each procedure, function and trigger in the row's `statement` | as PROCEDURE, every one, no cap |

`object` is `schema.name`, or `schema.table.index` for an index (5f already writes that form). A row recorded by
hand is parsed the same way; a bare name gets schema `dbo`; a by-hand INDEX row without a table matches on the
Index attribute alone, and says so: its table is found by the index's name in `sys.indexes` when exactly one user
table holds an index of that name (that table's name filters the texts), else it is not found. `table.index` is
in `dbo`. A DEPLOY's `statement` holds one line per object as 5f's
`SchemaChangeFact.line()` writes it, "{Procedure|Function|Trigger|View|Table|Index} {schema.name} {created|altered|dropped}";
procedures, functions and triggers created or altered are followed, once each, dropped ones skipped. Any other type (SETTING, STATISTICS,
PLAN, TABLE, …) is not followed and the report stays as today.

Limit, said on the page for an index: a query that reaches the table only through a view or a nested call is not
followed.

## 2. The read: one more area in 5c's batch

`tuningReportQuery` gains the parameters `@focusKind nvarchar(10)` (`''`, `module`, `view`, `index`) and
`@names nvarchar(max)` (one name per line, passed as a parameter, never concatenated; each line is the schema,
the object and the index apart by tabs). The sp_executesql parameter list becomes `@days int, @focusKind
nvarchar(10), @names nvarchar(max)`. The names are split in SQL with a CHARINDEX loop into `#focus` (not
STRING_SPLIT: a client copy can sit under compatibility level 130).

One more area in its own TRY (`@focusErr`), run only when `@focusKind <> ''`, **after** the Top shapes area and
guarded on `@topErr IS NULL`, from the `#rs`, `#int` and `#plans` already filled; it joins `query_store_query`,
`query_store_query_text` and `query_store_plan` for the matches (no second read of `query_store_runtime_stats`).
Every LIKE carries `COLLATE DATABASE_DEFAULT` on both sides. `query_plan` is already nvarchar(max): no CONVERT.
In the LIKE patterns `[` is escaped (`[[]`), and `%` and `_` too. The query texts are read once, with one pattern
(a view or an index is one object), and a binary `CHARINDEX` of the upper-cased name in the upper-cased text
(`Latin1_General_BIN2`) goes before the LIKE, in a CASE so it is evaluated first. The `CREATE INDEX` and `ALTER
INDEX` statements are left out by the first 200 characters of the matched texts. The two DMVs (the index's use,
`sys.dm_os_sys_info` for the start) are read only for an index, inside the area's TRY. Three result sets more:

- **Focus items:** per name: its line (the name as given is the change's, kept by the service), `object_id`
  (NULL when not found), the object's `create_date` and `modify_date` (`sys.objects`, server local), and the
  costliest matched statement's `query_hash` in the window (for Investigate). For an index, from `sys.indexes` and
  `sys.dm_db_index_usage_stats`: its table, seeks, scans, lookups, updates, the last use, and
  `sqlserver_start_time`.
- **Focus hours:** per item and hour: CPU (µs) and runs. For a module, runs is **the executions of its most-run
  statement in that hour**, which counts calls of a procedure whose statements all run once a call (limit,
  written beside the code: a statement in a loop inflates runs). For a view or an index, per matched query
  (`query_hash`), every plan of it in the window; the object's runs are their executions summed.
- **Focus queries** (added in the build: the page names the queries that use an index or name a view, Done when):
  per item, a view's or an index's matched `query_hash`, its object and its text (300 characters, one line).

## 3. The comparison (`domain/tuning.go`)

`TuningFacts` gains `Focus` (what the read followed), `FocusError`, `FocusItems`, `FocusHours` and `FocusQueries`. `TuningReport` gains `Focus *TuningFocus`:
the object, the kind, found or not, before and now (CPU per run, runs per business hour, CPU per business
hour), the verdict, the hours for its chart, the index use, the query hash for Investigate, and for a DEPLOY
one row per object.

- **The same Before and Now as the report** (5c's `tWindows`), and the same sums-not-averages rule. The
  change hour is never compared (5c).
- **The verdict is `TuningVerdict`** on CPU per run (faster, slower, same, new, gone, too few), so the object
  and the per-query table never disagree.
- **A DEPLOY** lists its objects, sorted by CPU added or saved per business hour, with the verdict per row;
  the sentence names the faster and slower ones by name (at most 3 each), as 5c does for queries.
- **Times:** `sys.objects` dates are server local, change rows UTC; compare them on the server's clock as 5c's
  `tcs` does. "At or after the change" allows a minute before it: the default trace's commit row comes a moment
  after the object is created, and a change recorded by hand carries the minute only.
- **Which change:** the service reads the change log before the read (a day to spare on its own clock), chooses
  the centre as 5c does (`TuningCentreChange`), and passes its object to the read; the report follows it only
  when the centre it builds is that change.

**The sentence comes first** in Explain, before 5c's database headline. Only one applies:

- Faster or slower: "Since procedure dbo.GetOrders was altered on Tue 3 Nov 14:20, it uses 8.6 ms of CPU per
  run instead of 12.4 ms (31% less) over 9 business hours, and it runs about as often (3,150 runs an hour
  against 3,200): the change made it cheaper." When slower: "…18 ms instead of 12.4 ms (45% more)…: Investigate
  it." Investigate opens the query page of its costliest statement (the Focus item's query hash); Copy carries
  the numbers for the vendor.
- Same: "…uses about the same CPU per run as before (12.1 ms against 12.4 ms): the change made no
  difference that Query Store can see."
- Runs changed a lot (the 5c `tExecsClause` rule): the clause says the object runs more or less often, so its
  load changed even if each run did not.
- Too few (verdict too few): "dbo.GetOrders ran only {n} times since the change; kakapo needs 20 to compare."
  Too few runs before it instead: "…ran only {n} times in the business hours before the change…"; ran before the
  change but not in the Before window: "dbo.GetOrders ran before the change, but not in the business hours compared
  before it ({Before}); kakapo needs 20 runs there to compare."; none since and
  too few before: "dbo.GetOrders has not run since the change, and ran only {n} times before it; kakapo needs 20
  to compare."
- Gone (ran before, not since): "dbo.GetOrders has not run since the change."
- New / nothing before: "Query Store holds no runs of dbo.GetOrders before the change in the last {n} days." Also
  when it holds no runs of it at all (a fact that time does not change, so it is said even when it is too early).
- Dropped and created (found, `create_date` at or after the change): "dbo.GetOrders was dropped and created
  again on {when}, so Query Store sees a new object and holds nothing before it. ALTER (or CREATE OR ALTER)
  keeps an object's history."
- Not found: "kakapo cannot find {object} in {database}, so the report compares the whole database."
- Index used: "Since index IX_T_V was created on dbo.T on Tue 3 Nov 14:20, the 4 queries that use it take 0.9 ms
  of CPU per run instead of 22 ms (96% less) over 9 business hours, and they run about as often (…): the change
  made them cheaper. It was used 60 times (60 seeks); SQL Server also updates it on every write to dbo.T (1,200
  updates so far)." The other verdicts as for a procedure, with "the 4 queries that use it" as the subject and the
  use sentence after. Used but too early: "Index IX_T_V on dbo.T was used 60 times (60 seeks) since {since}; SQL
  Server also updates it…". Used but no query in Query Store: "Query Store holds no query in the last {n} days
  whose plan uses index IX_T_V." and the use sentence. A view reads the same way with "the 3 queries that name it"
  ("Query Store holds no query in the last {n} days that names view dbo.V." when none).
- Index not used (no usage row, or all zeros): "Index IX_T_V on dbo.T has not been used since {since}; SQL
  Server still updates it on every write to dbo.T (1,200 updates). The counters start again when SQL Server
  restarts or the database is restored or taken offline (SQL Server last started {date})." `{since}` is the
  later of `sqlserver_start_time` and the change's own time. Copy. No advice to drop: kakapo has no sourced rule
  for that yet.
- Too early: 5c's "too early to say" rule (Now under two complete business hours). 5c already prints that
  line, so the object sentence is skipped.
- A DEPLOY: "Of the 40 objects in the deploy by DOMAIN\build on Tue 3 Nov 14:20, 3 got slower and 2 got faster;
  the rest ran too little to say. Slower: dbo.GetOrders, 18 ms per run instead of 12.4 ms; … Faster: …" (at most 3
  each; "{n} stayed about the same" when some did). Not found and dropped-and-created objects are rows with a note
  and no verdict.

## 4. The load on the chart

`TuningHour` gains `CoresBusy` from `domain.CoresBusy(h.f.CPU, time.Hour)` (5i): the cores this database kept
busy on average in that hour. The chart gains a third row, **"CPU used by this database (cores busy)"**, under
Executions, with the same shading and change markers. The tooltip: "0.4 cores busy". The legend says "this
database's queries only".

## 5. The page (`screens/tuning.tsx`)

- The Explain sentences: the object's sentence first (section 3).
- **A new section before "Is it better than before?"**, titled "Did it help {object}?": three numbers (CPU per
  run, runs per hour, CPU per hour), before and now, with the verdict word in colour; under them the object's
  own chart, CPU per run per hour with the change marker, reusing `HourChart`'s bars (export `Bars` from
  `hour-chart.tsx`, or give `HourChart` a rows prop); for a DEPLOY, the table of its objects, Investigate per
  row. Hidden when nothing is followed.
- Copy for IT (`TuningHandover`) starts with the object's block.
- The mock gains a procedure altered mid-week that gets faster and a new index that is used.
- For a view or an index the section's table lists the queries that name or use it, Investigate per row; the
  object's chart is HourChart with a `rows` prop (its CPU per run and runs).

## Tests

Domain: the name parse (two and three parts, brackets, bare name, a by-hand index without a table); the DEPLOY
statement parse (5f's line format, dropped skipped, no cap); which types are followed; the module's runs as its
most-run statement; Before and Now from 5c's windows; each sentence (faster, slower, same, too few, gone, new,
dropped and created, not found, index used, index not used, too early skipped, deploy); "since" for an index;
`CoresBusy` on the hours. Collect: the focus area is tagged, read-only, in its own TRY after Top shapes and
guarded on `@topErr`, names passed as one parameter and split without STRING_SPLIT, every LIKE with
`COLLATE DATABASE_DEFAULT`, plan XML read only for an index and only for the plans of queries whose text names
the table, no second read of `query_store_runtime_stats`. API: the report with and without a followed change.
Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`:

- **In a scratch user database with Query Store on** (dropped afterwards): run a procedure in a loop for an
  hour, ALTER it to be cheaper, run it for two more business hours; the report centred on the ALTER says it got
  cheaper, with its own chart, and the procedure need not be in the top 15. Create an index that a query uses:
  the report centred on it names the queries that use it, before and after, and the seeks. Create one that
  nothing uses: the "not used" sentence. The chart shows the cores-busy row.
- **On OldSchool** (read only; its collation differs from the server's, so the LIKEs are proven there), the
  real changes of 5 Nov give the sentences the data supports, each checked by hand:
  `dbo.Utility_Document_UpdateComment` has no query under its object_id ("Query Store holds no runs…");
  `dbo.Utility_GenerateLabelsAndInsertIntoQueue` ran once after its ALTER, inside the change hour (too few, or
  too early by 5c's rule); `dbo.BarcodeMaster.UX_BarcodeMaster_Name` has no usage row since the restart ("not
  used", since SQL Server last started). The 30-day report centred on that index reads in under 10 seconds.
