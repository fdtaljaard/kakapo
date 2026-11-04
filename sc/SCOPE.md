# SQLMonitor: high-level scope (draft for agreement)

Date: 2026-11-03. Product name: **kakapo** (UI, window title, log); working name in code, exe and data file: `sqlmonitor`. Status: scope and stack agreed 2026-11-03; phase 0 built and tested the same day (see `README.md`); phases 1 (Query Store fix), 2 (database page skeleton, `docs/landing-page.md`) 3a (Backups card, `docs/phase3a-contract.md`) and 3b (Storage card with the new STO10 room-for-CHECKDB check, STO11 log usage and STO12 disk forecast from stored samples, `docs/phase3b-contract.md`) 3c (Setup card in full with the general fix pattern, ten fixes, `docs/phase3c-contract.md`) 3d (Upkeep card with the statistics fix, `docs/phase3d-contract.md`) and 3e (Performance card: two clocks, Query Store top queries, deadlock report on request, `docs/phase3e-contract.md`) and 3f (the DarlingData adopt-now batch: nine corrections and twelve additions across five cards, `docs/phase3f-contract.md`, `docs/darlingdata-review.md`) built and tested the same day against a local SQL Server 2022. Phase 3g (the backup schedule fix: Agent jobs created from the Backups card, with a remove fix, `docs/phase3g-contract.md`) built and tested the same day. Phase 4 (section 3 item 10, built the same evening): 4a Copy on every finding and every preview, 4b the editable backup schedule, 4c the Performance investigation pages (a query, the blocking chain with a 30-second watch, the live sample) and their fixes (create and drop an index behind the triage's guards, kill a session, READ COMMITTED SNAPSHOT, force, unforce and evict a plan, statistics on one table), `docs/phase4a-contract.md` to `phase4d-contract.md`. 4d (fourteen more fixes on Setup, Storage, Upkeep and Backups, each verified live) built the same evening. 4e (2026-11-04: the TempDB file set as one fix with the server's own sizes as defaults, TempDB growth history as TMP02 evidence, and a progress bar for the long fixes, `docs/phase4e-contract.md`) built and verified live. 4f (long fixes run in the background with a pending run the dialog polls; two root causes of the sandbox's frozen CHECKDB fixed: the socket deadline from `connection timeout=30` and the driver's message queue, `docs/phase4f-contract.md`) built and verified live. 5a (archive or delete the old rows of a table that only grows: a fix from IDX10 or a table picker, a nightly batched job that moves rows atomically, options with defaults under "More options", `docs/phase5a-contract.md`) built and verified live 2026-11-04. 5b (the maintenance plan on the Upkeep card: every piece with its status, cadence and source; the history-cleanup fix and MNT08; the composite that creates what is missing in one apply with non-overlapping times, `docs/phase5b-contract.md`) built and verified live the same day. 5c (the tuning report, "did it help?": Query Store hour by hour with the change log, the toolkit's comparison rules, four tiles, per-query verdicts, plan regressions and failures, Record a change made by hand, `docs/phase5c-contract.md`) built and verified live 2026-11-04. Then the review's items in order: the four documents, the knowledge base, the page renderer, the read-only query path.

## 1. One line

A portable desktop app that connects to a client's Microsoft SQL Server, runs its read-only checks and live
samples, keeps everything it finds in one DuckDB **case file**, and lets you read, share and re-open that case file
anywhere, with or without the server.

## 1a. Vision and grounding rules (2026-11-04)

Where kakapo is going, and the three rules every design decision and every agent brief is checked against.
Written after phases 0 to 4f, before the architecture review (`docs/architecture-review.md`).

**What it is for.** One technical consultant per client, one portable exe, one database at a time: see what is
wrong, understand why in plain words, and act from the same place. The user may not be a SQL specialist.

**Rule 1: simplify complexity, in the product and in the code.** The product shows a sentence first and the
numbers second; a screen that needs a DBA to read it has drifted. The code follows the same rule: a small number
of plain parts (collect reads, domain judges, service wires, store keeps, the UI shows), one way to add a check,
one way to add a fix, one way to add a page. Clever means fewer moving parts doing more, never more parts.

**Rule 2: every recommendation is backed by a fact and a source.** kakapo gives advice. Each check has a rule
with its thresholds, a why, a do-this, a then-check and the sources it rests on (Microsoft's documentation, the
original toolkit, the practitioners named in `THIRD_PARTY_NOTICES.md`). That knowledge is one body, kept in one
place, used by the app's texts, by the hand-over blocks, and by any AI that works inside kakapo. A number without
a source is a guess and does not ship.

**Rule 3: AI is part of the application, with the human in the loop.** Whatever the user is looking at, an AI
must be able to see the same thing and reason about it: the page's findings, the facts behind them, the case
file's history. It must be able to read the live database on the user's behalf (read-only, bounded, logged),
because many actions can only be decided after an investigation, not before. When an investigation ends in a
change, the AI builds the change and hands it to the fix pattern of section 3 item 9: preview, the human
confirms, apply, read back, log. The AI never applies anything itself. The same knowledge of rule 2 is what an
AI agent or an agent skill works from, so the knowledge base and the AI's reference are one and the same thing.

**Rule 4: kakapo is a generic product.** Built by Granite, for any SQL Server. Nothing a user reads names
Granite, its products, its tables or its scripts: a check that only makes sense for one application (IDX10's
log tables as built) is made generic or removed, defaults are neutral ("Database support", not "Granite
support"), fixtures and examples use neutral names. The toolkit the checks came from stays named in section 2
and in the notices as provenance, nowhere else.

**Rule 5: options with defaults.** Whatever could reasonably differ between sites is an option of the fix; every
option has a default that works unseen; a preview shows up front only the options that change the decision
and keeps the rest under "More options" (agreed 2026-11-04 on the archiving design).

**What this rules out.** A console for experts (grids first, sentences never); advice without a source; an AI
that can change a server without a human click; a second copy of the knowledge for the AI; a service, a central
store, or a team product (the scope of section 3 stands).

## 2. Where it comes from

| Source | What we take |
|---|---|
| `../research/granite-sql` (ours) | The checks themselves. `01-QuickScan.sql` (55 server checks), `PerformanceTriage.sql` (database checks), `TuningReport.sql` (Query Store hour by hour), `InvestigateProcedure.sql`, `Checks.csv` (the catalogue with owner and action), the finding model and report layout in `DESIGN.md`, `known.csv` and `changes.csv`. These are plain T-SQL that return result sets, so the app runs them as they are. |
| Erik Darling's Performance Monitor **Lite** ([page](https://erikdarling.com/free-sql-server-performance-monitoring/), source under `References/PerformanceMonitor-main`) | The shape: one portable executable, nothing installed on the server, `VIEW SERVER STATE` only, DuckDB locally, collectors that sample DMVs on a timer and store deltas. We do not copy its 24/7 collection, alerting, PostgreSQL, plan viewer or MCP server. |
| First Responder Kit, sp_QuickieStore | Reference for new checks only, as `DESIGN.md` already says. Not shipped. |

What we add that neither has: the **case file** as the unit of work and sharing, and the finding model
(Do this, When, Then check, Owner) as the centre of the UI instead of raw grids.

## 3. Scope of version 1

**In**

1. **Connect**: add a server (Windows auth or SQL login, optional encryption), test, remember it. Passwords in Windows Credential Manager, never in the case file.
2. **Health check and triage**: run the existing scripts against the server or one database, land every result set in DuckDB, show findings as cards ranked the toolkit's way (Start here, For the client's IT, everything else by category, Accepted, What was checked, glossary). Same words as the HTML reports today.
3. **Live sample** ("why is it slow right now"): the `SqlLiveCheck` from `DESIGN.md` section 5.3, run as timed collectors for a chosen duration (default 30 seconds, up to an hour while you are on a call): waits, blocking chain, CPU, file latency, tempdb, top queries by delta. Stored as time series in the case file.
4. **Tuning report**: Query Store hour by hour with the change log, the before and after comparison, as the current dashboard does, drawn from the case file.
5. **Investigate a procedure**: statement-level cost for one procedure, plan XML stored (open in SSMS; no plan viewer in v1).
6. **Case file**: one DuckDB file per client/server. Every run appends with a timestamp, so a case holds history (the triage from last month next to today's). `known.csv` and `changes.csv` live inside it as tables, editable in the app.
7. **Share**: zip the case file and send it. Anyone with the app opens it **offline** and sees exactly what you saw. Also export a standalone HTML report (today's format) for people without the app, and CSV from any grid. A "hide query text" option for client data.
8. **AI-ready by construction**: because the case is DuckDB, Claude Code can query it directly with the DuckDB CLI. No MCP server needed in v1.
9. **Reads freely, writes only through a named fix.** Checks and samples never change anything. The app changes a
   server setting only through a **fix**: a setting with a target value, a current value read from the server, the
   exact statement it will run, shown as a preview, confirmed by the user, executed, read back to prove it took, and
   recorded in the `change` table in DuckDB (the change log the tuning report uses). The first fix is Query Store
   (section 3a). The other Apply-DoNow settings (max server memory, cost threshold, optimize for ad hoc, statistics)
   follow the same pattern. Procedure, view and index changes stay outside the app (`fixes/TEMPLATE.sql`).
   A fix may also **create** something on the server (an Agent alert, a job, an Extended Events session, a helper
   procedure) when that is the best way to get what a card needs, under the same preview, confirm, verify, log
   rules, and with a matching remove fix. "Portable" (section 4) is about the machine that runs kakapo, never a
   limit on what kakapo may do to the monitored server with the user's say-so. Every card works with reads alone;
   what a server refuses to have installed costs it only the features that need it (clarified 2026-11-03).
10. **Every finding ends in a button** (added 2026-11-03). The consultant using kakapo is the database's admin and
    may not be a SQL specialist, so a finding ends in one of three ways, never in a description alone:
    - **Apply**: a fix through item 9, with inputs where there is a choice (a schedule time, a memory value).
    - **Investigate**: the next read, inside the app, from the finding itself: a costly query opens its statements,
      plan warnings, parameters and its history over the last days; a blocking finding opens the chain now and
      watches it for 30 seconds; a tempdb finding shows who fills it; a failed job shows its history and error
      text; a backup finding shows the restore chain. Every investigation step ends in Apply, Copy or another
      Investigate. The live sample, "investigate a procedure" and the tuning report (items 3 to 5) are the big
      ones; the on-request reads (CHECKDB room, deadlocks) are the first built.
    - **Copy**: the finding, why, do this and evidence as one block ready for whoever owns it (the infrastructure
      team, the vendor, a DBA). Every fix preview also offers its statement to copy and run by hand.
    A finding that only describes is a gap to close, not a design choice; the toolkit's "never a fix" rules
    (index creation, kill a session) become fixes behind the triage's guards and a preview.

### 3a. Database setup: the Query Store fix

When a server is added, or whenever its card is opened, the app lists the user databases and shows for each one
whether it is set up for monitoring. Query Store is the first and most important setting, because the tuning
report and most of the triage depend on it.

**Target** (agreed 2026-11-03, matching `Apply-DoNow.sql` where it overlaps):

| Setting | Target | Why |
|---|---|---|
| OPERATION_MODE | READ_WRITE | |
| QUERY_CAPTURE_MODE | AUTO | Application databases run much single-use dynamic SQL; ALL floods the store. |
| MAX_STORAGE_SIZE_MB | 2048 | Lives in the PRIMARY filegroup, so it adds to the database and its backups. |
| SIZE_BASED_CLEANUP_MODE | AUTO | Cleans at 90% to 80%; stops the store going read-only. |
| INTERVAL_LENGTH_MINUTES | 15 | Four points per hour for the tuning report. Four times the runtime rows of the default 60; the reason for 2 GB. |
| STALE_QUERY_THRESHOLD_DAYS | 30 | One month-end. |
| DATA_FLUSH_INTERVAL_SECONDS | 900 | Default. |
| MAX_PLANS_PER_QUERY | 200 | Default. |
| WAIT_STATS_CAPTURE_MODE | ON | SQL Server 2017+. The clause does not exist on 2016 and must be left out. |

**Rules for the fix:**

1. Read everything first: `sys.database_query_store_options` (all columns above plus `readonly_reason`,
   `flush_interval_seconds`, `size_based_cleanup_mode_desc`, `max_plans_per_query`), the engine version, and whether
   the database is a readable secondary (`sys.dm_hadr_database_replica_states` or `DATABASEPROPERTYEX(...,
   'Updateability')`). Store the snapshot in DuckDB with a timestamp; history is kept.
2. Build the statement per version (no wait-stats clause below 14) and **only for the settings that differ**:
   `SET QUERY_STORE = ON (...)` when off, `SET QUERY_STORE (...)` with the drifted settings when on. Never a blind
   full re-enable.
3. The preview says what will happen in plain words, including: changing `INTERVAL_LENGTH_MINUTES` clears the
   runtime statistics collected so far; the store can grow to 2 GB inside the database.
4. On a secondary replica the card says "set on the primary" and offers no button. On SQL Server 2016 and 2017 the
   card shows the build next to the state, because early cumulative updates had Query Store bugs (INS01 covers the
   "old build" finding itself).
5. After the statement runs, read the options again and show `actual_state_desc` next to `desired_state_desc`.
   Decode `readonly_reason` into words (8: secondary replica; 65536: storage full; others: database state).
6. Write a `change` row: server, database, when, object (`Query Store`), type `SETTING`, description, the statement,
   the rollback statement (the previous values as a `SET QUERY_STORE (...)`), and the read-back result.

**The card** shows, per database, one line: `Query Store: On, 23% full, 15-min intervals, 30 days` with a green tick
when every setting matches, or the drift in amber with an **Apply recommended settings** button that opens the
preview. `Off` is red. The card is designed for "a setting with a target, a current value and a fix", so the next
settings reuse it.

### 3b. Backups: the backup schedule fix (added 2026-11-03; built the same day, `docs/phase3g-contract.md`)

When the Backups card finds no backup schedule (BAK01 no full backup, BAK02 no log backup, or no enabled Agent
job that backs this database up), the card offers **Create a backup schedule** as a fix, under the rules of
item 9: preview, confirm, apply, verify, log, with a remove fix. What it creates:

| Piece | Default | Why |
|---|---|---|
| Full backup job | Daily 22:00, `BACKUP DATABASE ... WITH COMPRESSION, CHECKSUM, INIT`, one file per day named `{db}_FULL_{yyyyMMdd_HHmm}.bak` | Compression halves the file; CHECKSUM makes BAK04 meaningful. |
| Log backup job | Every 15 minutes in FULL or BULK_LOGGED recovery; not created in SIMPLE (DBC08 decides that first) | Closes BAK05's data-loss window to 15 minutes. |
| Retention | Delete files older than 7 days from the folder, in the job's last step | The scan's BAK01 window. |
| Folder | Typed by the user in the preview, checked with `xp_dirtree` before apply, never the data drive when another is visible | The one decision only the site can make. |
| Verify | `RESTORE VERIFYONLY` as the full job's second step | Proves the file, not just the write. |

Decisions to make before building:

- **Ola Hallengren or native.** When `dbo.DatabaseBackup` exists (any database), the jobs call it with
  `@Compress = 'Y', @Verify = 'Y', @CheckSum = 'Y', @CleanupTime = 168`. When it does not, the fix either installs
  Ola's `DatabaseBackup` and `CommandExecute` (MIT, versioned, under the installed-helpers rule) or writes plain
  `BACKUP` steps. Preference: install Ola, because most sites that have a DBA already have it and the
  consultant knows its options; plain `BACKUP` only when the site refuses installs.
- **Express** has no Agent. The fix then writes a `.sql` file and a Windows Task Scheduler command for the
  consultant to run on the server, and the card says so; kakapo cannot create the schedule itself there.
- **Operator**: when MNT07 found one, the jobs notify it on failure; otherwise the preview says no one will be told.
- **Scope**: one database per fix, as every fix. A later "all user databases" option can reuse the same jobs.

The preview shows the two job definitions in words and the exact `sp_add_job`/`sp_add_jobschedule` statements;
the read-back checks the jobs exist, are enabled and have a next run time; the change row records the job names
so the remove fix can delete exactly those. Owner Client IT on the card, as BAK01 and BAK02 are.

**Out (for now)**

- 24/7 collection, a service, alerts, email, webhooks.
- PostgreSQL, Azure SQL Database (Managed Instance works because it is a normal instance).
- A graphical plan viewer, an MCP server, user accounts, a central store.
- Any write to the monitored server other than a named fix (section 3, item 9). Installed helpers (a kakapo Extended Events session for blocking and deadlocks, an opt-in utility database with Erik Darling's procedures for an Expert page) come later, each as a fix with its remove, listed on the instance page under "Installed helpers"; the live sample starts from DMVs and the existing `system_health` session.

## 4. Non-functional requirements

| Requirement | Meaning |
|---|---|
| Portable | One folder, one `.exe`, no installer, no admin rights, no .NET or runtime to install. Runs from a USB stick or a client jump box. |
| Self-contained | The checks catalogue, the SQL scripts and the UI are inside the binary. Data goes next to it (or a chosen folder), never in `%AppData%` by default. |
| Safe in production | Same rules as the scripts: `LOCK_TIMEOUT` 10 s, `DEADLOCK_PRIORITY LOW`, read uncommitted where it fits, a check that fails is reported and skipped. A fix runs only after a preview and an explicit confirmation, one database at a time, and is logged. |
| Works on a jump box | Windows Server 2016+ often has no WebView2. The app must also run as `sqlmonitor --serve` and open the same UI in whatever browser is there. |
| Shareable | A case file opened on another machine needs nothing but the app. No absolute paths, no credentials inside it. |
| Supported targets | SQL Server 2016 SP2 to 2025, Express included. Query Store features degrade gracefully where they are off. |

## 5. Stack (agreed 2026-11-03)

| Layer | Choice | Why |
|---|---|---|
| Backend | **Go** | One static exe, goroutines for samplers, fast builds, readable for the team. |
| SQL Server driver | `microsoft/go-mssqldb` | Microsoft-maintained, pure Go, Windows auth over SSPI, Entra ID, TLS. |
| Storage | **DuckDB** via `duckdb/duckdb-go` | One file to share, analytical SQL, Parquet export, anyone can open it with the DuckDB CLI. Pre-built Windows static libs; CGO and MinGW gcc on the build machine only. |
| Backend to UI | HTTP JSON + **Server-Sent Events** | Live samples pushed to the screen; plain HTTP so any tool can read a running instance. |
| Frontend | **React + TypeScript**, Vite, Tailwind, **shadcn/ui** | Strongest ecosystem for AI-agent development; shadcn components are source in the repo, not a dependency; TanStack Table and shadcn charts for grids and charts; React-first AI chat libraries when an in-app assistant comes. No Next.js: nothing is server-rendered. |
| Desktop shell | Edge in app mode (`msedge --app=http://localhost:<port>`) | A window with no address bar and zero runtime dependency. Wails can wrap the same UI later if a native window is wanted. |

Rejected: Rust + Tauri (slow DuckDB source build, thinner SQL Server driver), .NET (heavy binary, WPF/Avalonia is
more code than HTML for the same screens), htmx (fine for pages, weak for a live screen), Vue/PrimeVue (chosen
on the basis of agent competence and AI-UI ecosystem, not the team's current stack).

Rule: a modern frontend means a build step on the dev machine (Node, Vite). The runtime stays one exe because
the built files are embedded with `go:embed`.

## 6. Architecture

One binary, one service layer, three faces (UI now, MCP and an in-app assistant later).

```
sqlmonitor.exe
  web/        embedded React build (static files)
  api/        HTTP JSON handlers + SSE stream
  service/    use cases: register server, run check, start sample, open case, export
  collect/    SQL Server side: script runner, timed samplers, deltas
  store/      DuckDB: schema, migrations, the single writer, queries
  domain/     finding model, check catalogue, severity rules; no I/O
```

Rules:

1. **Collectors publish, the store persists, the UI subscribes.** Samplers put rows on an in-process channel. One
   writer goroutine appends to DuckDB (DuckDB wants one writer). The API fans the same rows out over SSE. Live view
   and history view read the same tables, so a case opened offline is the live screen with the stream off.
2. **The service layer is the only entry point.** REST handlers, a later MCP server and a later assistant call the
   same functions. The AI interface is a second face on existing code, not a separate design.
3. **Domain has no I/O.** Finding model and ranking are plain Go, tested without a server or a database.
4. **Portability is the file plus the API.** DuckDB file to share, Parquet/CSV export, JSON over HTTP.

Deliberately not there: microservices, a message broker, an ORM, a DI framework, user accounts, a plugin system,
Electron, Wails. Optional until it hurts: an OpenAPI contract with generated TypeScript types (start with
hand-written types); the `mcp/` package (a reserved name, not code).

Data layout, phase 0 decision: one `sqlmonitor.duckdb` beside the exe holds the server registry and all collected
data, keyed by server. "Share" later means exporting one server's slice into its own DuckDB file (ATTACH and
copy), which is how the case file of section 3 comes about. Passwords never go in DuckDB: Windows auth needs
none, SQL logins go to Windows Credential Manager.

## 7. Phases

| Phase | Done when |
|---|---|
| **0. First run** | `sqlmonitor.exe` starts, opens (or creates) `sqlmonitor.duckdb` beside itself, serves the embedded React UI on localhost and opens Edge in app mode. It sees the `server` table is empty and shows a setup screen: server, auth (Windows or SQL login), optional database, encrypt. **Test** tests the connection with go-mssqldb and shows the SQL Server version. **Save** writes the row to DuckDB (password to Credential Manager). Restart the exe: it finds the row, skips setup, shows the server. That is all. It proves Go + CGO DuckDB + go-mssqldb + embedded React + app-mode window on Windows. |
| **1. Database setup: Query Store** | The server card lists the user databases with their Query Store state as in section 3a. **Apply recommended settings** previews the exact statement, runs it on confirmation, reads the options back, and writes a `change` row. Done when, on a database with Query Store off, the card goes from red to green in one click and the `change` table holds the statement and its rollback. Proves: reading a stored SQL password back from Credential Manager, a per-database connection, the fix pattern (target, current, preview, apply, verify, log), and the first write to DuckDB from a collector. |
| **2. Database page skeleton** | The four levels from `docs/landing-page.md`: home tiles, the instance's database picker (skipped when the registration remembers a database), the database page with the header, six cards and Recent changes, and the card page. Only Setup is live (Query Store from phase 1); the other five are grey. Contract in `docs/phase2-contract.md` (revision 2: database first). |
| **3. One card at a time** | Backups, Setup in full, Storage, Upkeep, Performance, Design: each card built with its reference script and README open, taking only the checks that card needs (`Checks.csv` gets a Card column). The quick scan is mined, never run whole. Done when a DBA agrees with each card's verdict on a real site. |
| **3g. Backup schedule fix** (built 2026-11-03) | Section 3b: on a database with no backup job, the Backups card's fix creates the full and log jobs (Ola when present or installed, native otherwise), reads them back, logs them, and the card goes from red to green by the next day. The remove fix deletes them. |
| **4a to 4e. Every finding ends in a button** (2026-11-03/04) | Section 3 item 10. 4a: Copy on every finding (a hand-over block by owner) and Copy script on every preview. 4b: the backup schedule's time, interval, retention and compression as inputs. 4c: Investigate on QRY01 (the query page: plans, history, waits, parameters, the procedure's statements, the tables and their statistics), on ACT01/ACT02 (the blocking chain, watched for 30 seconds) and the live sample on the Performance card; fixes create-index (guards from IDX02/04/08/09), drop-index, kill-session, rcsi, force-plan, unforce-plan, evict-plan, update-statistics-table. 4d: multi-user, reconfigure, trace-flag-off, scoped-config-reset, min-server-memory, operator-alerts, maintenance-job, remove-shrink-step, autogrowth, add-tempdb-files, recovery-model, checkdb-job, checkdb-now, suspect-pages-clear. 4e: tempdb-files (equalise and pre-size every TempDB file, growth evidence from the default trace on TMP02) and a progress bar in the dialog for CHECKDB, index builds, statistics, a backup started now and the TempDB fix. |
| **4. Right now and did it help** | The 30-second live sample on the Performance card (samplers with deltas over SSE); the tuning report behind Recent changes. |
| **5a. Archive old rows** (2026-11-04) | `docs/phase5a-contract.md`: `archive-table` from an IDX10 finding or the Storage card's Tables row and picker; the preview reads size, age, keys, triggers, features, identity and room before anything runs; an Agent job moves rows nightly in batches with `DELETE … OUTPUT INTO` (or deletes, with the red warning), options with defaults, a first batch on request; `remove-archive` deletes the job and keeps the archive. The fix dialog's "More options" fold (rule 5). |
| **5b. Maintenance plan** (2026-11-04) | `docs/phase5b-contract.md`: the Upkeep card's Maintenance plan row (full and log backups, integrity check, statistics, index work as optional, history cleanup, alerts, archives) with status, cadence, last and next run and the source of each cadence; MNT08 and the `history-cleanup` fix; `maintenance-plan`, a composite that previews and creates the missing pieces through their own fixes in one apply, Sunday check 01:00, index 03:00, cleanup 05:00, full backup 22:00, statistics 01:00 on the other nights. |
| **5c. Tuning report** (2026-11-04) | `docs/phase5c-contract.md`: `/tuning` from Recent changes ("Did it help?") and the Performance card; the toolkit's windows (business hours after a change against the same day or the five working days before, sums not averages, change hours and the hour in progress never compared), four tiles, per-query faster/slower/new/gone with the 20-run floor, the toolkit's plan-regression rule, failures, idle hours shown as a timeline; Record a change made by hand. |
| **5. Share and polish** | Export a server's slice to a case file, open a case offline, standalone HTML export, hide query text. |

Phase 1 pieces, each small enough for one agent with this document as its brief:

| Piece | Where | Done when |
|---|---|---|
| Credentials back | `service`, `cred` | `service.connect(serverID)` returns an open `*sql.DB` for a registered server, reading the SQL password from Credential Manager. |
| Read Query Store | `collect`, `store`, `domain` | `collect.QueryStoreOptions(db, database)` returns every column in section 3a; `domain` compares it to the target and names the drift; the snapshot lands in `db_query_store`. |
| Build and apply the fix | `domain` (statement builder, version-aware, drift-only, with tests), `collect` (execute, read back), `store` (`change` table) | The builder's tests cover: off on 2016, off on 2022, on with the default interval, on and matching (no statement). |
| API | `api` | `GET /api/servers/{id}/databases`, `POST .../databases/{db}/query-store/preview`, `POST .../apply`. |
| Card and preview | `web` | The server card expands to its databases; the line, the colours, the button, the preview dialog with the statement and the warnings, the result. |

## 8. Decisions log

| Date | Decision |
|---|---|
| 2026-11-04 | kakapo is generic (rule 4). Every Granite reference leaves the product: texts, defaults, fixtures, the IDX10 check as built (made generic or removed). The toolkit stays as provenance in section 2 and the notices. Sweep to be done with the knowledge migration. |
| 2026-11-04 | Pause for review. The vision and three grounding rules are section 1a; the review of what must change is `docs/architecture-review.md` (knowledge base as one body, a server-side page renderer, a read-only query path, a proposed-change fix, the three-level UI rule, case-file retention and export, the AI as a tool layer, builder unification, four documents). Nothing built from it yet. |
| 2026-11-03 | Go + DuckDB agreed. Wails and Vue/PrimeVue dropped; React + TypeScript + shadcn/ui chosen for agent-driven development and the AI-UI ecosystem. Edge app mode instead of a native shell. |
| 2026-11-03 | Version 1 is read only and on demand; no service, no alerts. |
| 2026-11-03 | Phase 0 is first-run setup only (section 7). Nothing from the checks until the chain is proven. |
| 2026-11-03 | "Read only" relaxed to "writes only through a named fix" (section 3, item 9): preview, confirm, apply, verify, log to the `change` table. Query Store is the first fix and becomes phase 1; the health check moves to phase 2. Target settings in section 3a, matching `Apply-DoNow.sql` and adding the 15-minute interval, 30-day retention and wait-stats capture. |
| 2026-11-03 | Implementation is delegated: Fable (this session) writes scope, briefs and reviews; Opus subagents implement; Sonnet subagents do read-only lookups. |
| 2026-11-03 | Purpose restated: one consultant per client, one portable exe (unchanged), and every finding ends in Apply or Copy (section 3, item 10). The Design card's index work becomes actions on the Performance and Storage cards rather than a card of its own. |
| 2026-11-03 | A missing backup schedule is something kakapo creates, not only reports: the backup schedule fix (section 3b, phase 3g). The first fix that creates Agent jobs; Ola Hallengren's DatabaseBackup preferred, installed under the installed-helpers rule. |
| 2026-11-03 | "No install" means the app: one portable exe on the consultant's machine. It is not a ban on writing to the monitored server. Fixes may create objects (alerts, jobs, an XE session, helper procedures) with a remove fix and a change-log row; the card checks stay reimplemented reads, not calls to installed procedures (`docs/darlingdata-review.md`). |
