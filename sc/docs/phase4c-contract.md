# Phase 4c contract: Performance investigation and actions

SCOPE section 3 item 10 applied to the Performance card: the finding is where the work starts, and the work
is a read (Investigate), a change (Apply) or a hand-over (Copy). Three parts, each one agent, each with its own
files (the seams they share are created before they start, section 0). Sources: `docs/phase3e-contract.md`
(the card), `DESIGN.md` 5.3 (the live check), `InvestigateProcedure.sql` (the per-statement read),
`PerformanceTriage.sql` IDX02/IDX08/IDX09 (the index guards), sp_QuickieStore (plan history and forcing),
sp_PressureDetector (the delta shape), sp_HumanEventsBlockViewer (the blocking tree). Rules as always:
`collect` reads in their own TRY, every batch tagged, `domain` pure with tests, writes only through the fix
pattern with preview, confirm, apply, read-back, change row.

## 0. Seams (made before the agents start)

- `internal/service/fixreg.go`: a registry. `type fixPreparer func(ctx, s *Service, g *genericFix, def
  domain.FixDef, options map[string]string) error` and `type fixApplier func(ctx, s *Service, serverID int64, g
  *genericFix) (domain.FixResult, error)`, with `registerFix(id string, prepare fixPreparer, apply fixApplier)`
  called from each agent's own file in `init()`. `prepareGenericFix` and `ApplyFix` consult the registry
  before their existing branches. Options reach the preparer (the API already parses them).
- `internal/domain/fix.go` `FixDefs` gains, under the Performance card: `create-index` (WRK03), `kill-session`
  (ACT01), `rcsi` (ACT01), `force-plan` (QRY01), `evict-plan` (QRY01), `update-statistics-table` (QRY01).
- `internal/api/api.go` calls `a.queryRoutes(mux)`, `a.blockingRoutes(mux)`, `a.sampleRoutes(mux)` from
  `internal/api/queries.go`, `blocking.go`, `sample.go` (stubs).
- `web/src/App.tsx` routes `/instances/:id/databases/:db/queries/:hash` -> `screens/query.tsx`,
  `/instances/:id/databases/:db/blocking` -> `screens/blocking.tsx` (stubs). The sample lives on the card page.
- `web/src/api.ts` stays the base client; each part adds its calls in `web/src/api-queries.ts`,
  `api-blocking.ts`, `api-sample.ts` (and mocks in `api-queries.mock.ts` etc., selected the way `api.ts`
  selects its mock).
- `components/finding-card.tsx` gains an optional `investigate?: {label: string; to: string}` prop rendered as
  a button "Investigate" (link) next to Apply/Copy; the card page fills it per finding (section per part).

## Part 1: investigate a query (`queries`)

**Route** `GET /api/servers/{id}/databases/{db}/queries/{hash}` where `{hash}` is the `0x…` query hash of a
QRY01 finding (the finding's evidence carries it; the card page passes it as `investigate.to`). Reads (one
`collect.QueryDetail(ctx, db, database, hash)` in `collect/queries.go`, SELECTs only, each area in its own
TRY):

| Area | Read | Shown as |
|---|---|---|
| The query | text, object name (procedure) if any, query hash | header |
| Plans | every plan of this hash: Query Store `query_store_plan` (plan_id, is_forced_plan, last_compile, engine_version, compatibility_level) with its runtime stats over 7 days (runs, avg/max CPU, duration, reads, spills) and the plan XML; plan cache when Query Store is off (`dm_exec_query_stats` by `query_hash`, distinct plan_handle) | "Plans" table, one row per plan, the active one marked, a forced one marked; the fastest by avg duration marked "fastest" |
| Plan warnings | `domain.PlanWarnings` on each plan XML, plus the compiled parameter values from `<ParameterList>` (name, compiled value, runtime value when present), and the missing-index hint `<MissingIndexes>` (table, equality, inequality, included columns, impact) | "What the plan says": warnings in words; "Compiled with" the parameters; "SQL Server suggests an index" with the create-index Apply (part 3's fix, option `source=plan`, `planId`) |
| History | Query Store: runs, CPU ms, duration ms, reads per hour over the last 7 days (`runtime_stats` joined to intervals, summed per hour) | a plain table by hour, newest first, with the plan id that ran; the UI draws a simple bar (CSS width) for CPU per hour, no chart library |
| Waits | Query Store `query_store_wait_stats` by category over 7 days | "Waits" list with sp_QuickieStore's category words |
| Statements of the procedure | when the query belongs to a procedure: `InvestigateProcedure.sql` part 2, every statement of the procedure from the plan cache (`dm_exec_query_stats` + `dm_exec_sql_text` by `objectid`), runs, CPU and reads per run (min/avg/max), the statement text | "Every statement in {procedure}", most reads first |
| Tables it touches | from the plan XML `<Object Database= Schema= Table= Index=>` nodes, distinct tables of this database, with rows (`dm_db_partition_stats`) and the statistics age (`dm_db_stats_properties` max modification % and oldest update) | "Tables" with the update-statistics-table Apply per table |

`domain/query.go`: `QueryDetail` (JSON) built from the facts, pure, with `Explain(d QueryDetail) []string`:
plain sentences a non-DBA can read ("It ran 11,240 times in 7 days, 0.9 s each on average; the slowest run took
41 s", "Two plans: the one used since 10:15 is 6x slower than the one before it", "Its parameter @FromDate was
compiled with 2019-01-01, which may not be typical", "It reads dbo.TrackingEntity (4.2 M rows) whose statistics
are 31 days old with 48% of rows changed"). Each sentence that has an action names it.

**Actions** (fixes, registered in `service/queries_fix.go`, previews in `domain/query_fix.go`):

| Fix | Scope | Options | Preview | Apply |
|---|---|---|---|---|
| `force-plan` | database | `planId` | Query Store on and the plan not forced: "Force plan {id} for this query: avg {ms} ms vs {ms} ms for the plan now in use". Warnings: forcing hides regressions; un-force when the code changes; `sys.sp_query_store_force_plan` needs ALTER on the database | `EXEC sys.sp_query_store_force_plan @query_id, @plan_id`; read back `is_forced_plan`; rollback `sp_query_store_unforce_plan` |
| `unforce-plan` | database | `planId` | the reverse, offered when a plan is forced | |
| `evict-plan` | server | `planHandle` | "Remove this plan from the cache so SQL Server compiles a fresh one. Harmless; the next run compiles." | `DBCC FREEPROCCACHE (0x…)`; read back the handle gone; no rollback |
| `update-statistics-table` | database | `table` (schema.table) | `UPDATE STATISTICS [s].[t] WITH FULLSCAN` (SAMPLE 25 PERCENT when rows > 10 M), the rows and age in the Changes row; warning on size and that it runs online | run inside the database (as the Upkeep fix does), read back `stats_date`, change row |

`Investigate` button on each QRY01 finding (label "Investigate", to the query route). The query page has
Copy: a hand-over block for the vendor with the query text, the numbers, the warnings and the parameter values
(`domain.QueryHandover`).

## Part 2: blocking (`blocking`)

**Route** `GET /api/servers/{id}/databases/{db}/blocking` reads the chain now (`collect/blocking.go`): every
session that blocks or is blocked across the instance (`dm_exec_requests`, `dm_exec_sessions`,
`dm_tran_session_transactions`, `dm_exec_sql_text`, `dm_tran_locks` for the head blocker's lock on the waited
resource: object name through `partitions`/`objects`), as a tree: head blockers at the top, each with its
victims, each victim with its own victims. Per session: id, login, host, program, status, database, the
statement (current or last), wait type and time, the resource (table or index name), transaction age and log
bytes, isolation level, open transaction count, `sleeping` flagged. `domain/blocking.go` builds
`BlockingTree` (JSON) and `Explain`: "Session 83 (GraniteScheduler on WMS-APP01) has held a lock on
dbo.MasterItem for 6 minutes in a transaction that is still open and is doing nothing (sleeping); 14 sessions
wait behind it." Classification as sp_HumanEventsBlockViewer: sleeping blocker, implicit transaction,
serializable, compile lock, long-running statement.

**Watch**: the page polls the route every 3 seconds for 30 seconds on "Watch for 30 seconds" (client-side; no
SSE) and keeps the chains seen, so a short block is caught; "Still blocking" vs "Cleared at 10:14:05" per head
blocker.

**Actions**:

| Fix | Scope | Options | Preview | Apply |
|---|---|---|---|---|
| `kill-session` | server | `sessionId` | reads the session again: who, what it runs, transaction age, log bytes written (the rollback will take about as long as the work did), whether it is a system session (refused), SQL Agent job name if it is one. Changes: "Session {n} | running {statement} | killed". Warnings: "Its work is rolled back: everything that transaction changed is undone", the rollback estimate, "Tell whoever owns it (the login and host above)". Needs ALTER ANY CONNECTION. | `KILL {n}`; read back the session gone (or `KILL … WITH STATUSONLY` while rolling back); change row object "Session {n}" type "KILL"; no rollback statement |
| `rcsi` | database | none | `is_read_committed_snapshot_on` off: Changes "READ_COMMITTED_SNAPSHOT | OFF | ON". Warnings: `WITH ROLLBACK IMMEDIATE` disconnects every user of the database for a moment, so apply in a quiet minute; tempdb holds row versions (STO/TMP cards); readers stop waiting for writers, writers still wait for writers; the vendor's code that relies on blocking for locking (rare) must be checked. Needs ALTER on the database. | `ALTER DATABASE [db] SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE`; read back; rollback the OFF statement |

`Investigate` on ACT01 and ACT02 findings (to the blocking route). Copy on the page: the chain as text for the
vendor.

## Part 3: the live sample and the create-index fix

**Sample.** `POST /api/servers/{id}/databases/{db}/cards/performance/sample` with `{"seconds": 30}` (10 to
120) takes two snapshots that many seconds apart (the Go side sleeps between two reads; one connection) and
returns the difference (`collect/sample.go`, `domain/sample.go`), stored in a new table `perf_sample` (one
row per run, JSON) and shown on the Performance card page like the deadlocks panel: the last sample with
"Watch for 30 seconds" and the seconds input. What a sample holds, each with the plain sentence from
`DESIGN.md` 5.3:

| Area | Delta of |
|---|---|
| Waits | `dm_os_wait_stats` (benign list out), top 5 by wait time in the sample, with the group words of WRK01; poison waits named |
| CPU | the scheduler-monitor ring buffer records that fell inside the window (SQL, other, idle) and `runnable_tasks_count` |
| Top queries | `dm_exec_query_stats` by `query_hash`, top 5 by CPU delta, with the text and the object, runs in the sample; `Investigate` link to part 1's page |
| Files | `dm_io_virtual_file_stats` per file of this database and tempdb: reads, writes, ms per read/write in the sample |
| Blocking | head blockers seen in either snapshot (count, longest), `Investigate` link to part 2 |
| Running | backup, restore, CHECKDB, index rebuild in progress (`dm_exec_requests` command with percent_complete) |
| Growth | files whose size changed in the window |
| Memory | page life expectancy at both ends, grants pending |

`domain.SampleVerdict`: one of the sentences of DESIGN 5.3, including "No problem found in this {n}-second
sample. If users still see slowness, the cause is in the code or the indexes: see the costliest queries."

**The create-index fix** (`create-index`, scope database, check WRK03; also reachable from a plan's hint in
part 1 with `source=plan`). Options: `table`, `equality`, `inequality`, `included` (the suggestion's columns as
WRK03 holds them, passed back), `name` (default `IX_{table}_{first key columns}_kakapo`), `online` (bool,
default true on Enterprise/Developer, absent otherwise). Preview (`domain/index_fix.go`):

1. Builds `CREATE NONCLUSTERED INDEX [name] ON [s].[t] ([equality cols], [inequality cols]) INCLUDE ([included])
   WITH (ONLINE = ON|OFF, SORT_IN_TEMPDB = ON, DATA_COMPRESSION = PAGE when supported)`.
2. **Guards, each a warning that can block Apply** (reads in `collect/index.go`): an existing index with the
   same leading keys (IDX04/IDX08: "Index {x} already starts with these columns; widen it instead of adding one"
   blocks); an index whose keys are these keys in another order (Info); the table has >= 10 indexes or index
   bytes >= 2x data (IDX02: warning, does not block); the suggestion's included columns are >= 5 and the
   result would hold >= half the table's columns (IDX09: blocks, "this would be a second copy of the table:
   include only the columns the query needs", with the option to edit `included`); estimated size (rows x key
   and included widths from `sys.columns`) over 1 GB (warning: time and log); the table is being written
   heavily (writes > 10x reads on its clustered index: warning).
3. Changes: "Index | none | {name} on {table} ({keys}) include ({cols}), about {MB} MB, online". Warnings as
   above plus "Creating an index locks the table while it builds (not online on this edition)", "Test on the
   sandbox first when the table is large", and the permission (ALTER on the table).
4. Apply runs with a long timeout (30 minutes), reads back `sys.indexes`, writes the change row (object the
   index, type "INDEX") with the rollback `DROP INDEX`. A `drop-index` fix (option `index`) is the remove, offered
   on the Storage card's IDX findings later; build it here with the registry so the rollback is a real fix.

The WRK03 finding carries `fixId: create-index` with its columns in the fix's options (the card detail's
`Fixes` entries carry `Options map[string]string` so the UI passes them to the preview; add that field to
`Fix`).

## Frontend

- `screens/query.tsx`: header (query or procedure, hash, Copy for the vendor), Explain sentences first, then
  the sections of part 1 as plain tables, each action a button opening the fix dialog with its options.
- `screens/blocking.tsx`: Explain sentences, the tree (indent per level), Watch button with a 30-second
  countdown and the chains seen, Kill on each head blocker, "Turn on READ COMMITTED SNAPSHOT" at the bottom when
  it is off, Copy.
- The Performance card page: the sample panel; `Investigate` on QRY01, ACT01, ACT02; Apply on WRK03.
- All three screens on phone width without horizontal scroll (the existing layout rules).

## Tests

Domain: Explain sentences for a query with two plans and a regressed one; the parameter extraction and the
missing-index hint from a plan XML fixture; the blocking tree from a fixture with two levels and a sleeping
blocker; the sample delta and verdict from two fixtures; the create-index statement and each guard; the fix
previews' warnings. Collect: the queries are tagged and read-only. Store: the sample round trip.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`: a QRY01 finding opens its page with plans,
history and tables and the update-statistics action applies; a staged block (an open transaction in sqlcmd)
shows the chain, the watch catches it, Kill previews with the rollback warning and applies; the sample runs for
15 seconds and shows the staged query at the top; the create-index preview on a WRK03 suggestion shows the
guards and applies on a throwaway table, and the change row holds the DROP INDEX rollback.
