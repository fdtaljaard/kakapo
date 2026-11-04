# Phase 3c contract: the Setup and configuration card in full, and the general fix pattern

Builds on `docs/phase3a-contract.md` (finding model, card detail, card_read) and `docs/phase1-contract.md`
(the Query Store fix). Sources: `01-QuickScan.sql` section 1 (lines 159-221 environment, 225-411 INS01 to INS11),
section 2 (414-536 DBC01 to DBC11), INS12 (1261-1292), INS13 (1295-1314); `PerformanceTriage.sql` CPY01 to CPY06
(741-868) and the legacy-estimator part of CFG01 (895-905); `Apply-DoNow.sql` (the house targets and the warnings
for the server settings); `Checks.csv` for Owner and Then check. Field names exactly as written.

## Scope

One database page, as before. The instance settings (INS) appear here because they decide how this database
runs; each INS finding's object is the instance's `@@SERVERNAME`, a DBC/CPY finding's object is the database.
Duplicates between the scan and the triage are folded: CFG01's AUTO_CLOSE, AUTO_SHRINK and statistics are DBC02,
DBC03 and DBC05; CPY03 is DBC06; CPY05 is DBC07. CFG01 keeps only the legacy cardinality estimator; CFG02 is
DBC10 (Query Store, the phase 1 fix). DBC08 stays on the Backups card.

## The checks

| Check | Fires when (as the source) | Severity | When / Effort | Fix id |
|---|---|---|---|---|
| **INS01** Version and update | out of extended support: High, "Decide first / Project"; behind the latest CU: Low when the latest CU is 12+ months old or mainstream support ended, else Info; otherwise an Info "Instance summary" with the scan's summary line | as stated | Window / Moderate for the CU case | |
| **INS02** Max server memory not set | value_in_use >= 2147483647 | High (Express: Low) | Online / Quick win | `max-server-memory` |
| **INS03** Max server memory wrong | leaves < 2 GB or > 95% to Windows: Medium; below half of >= 16 GB RAM (not Express): Low | as stated | Online / Quick win | `max-server-memory` (first case only) |
| **INS04** MAXDOP | 0 with > 8 CPUs: Medium; above suggested: Low; = 1 with > 1 CPU: Info "Decide first" | as stated | Online / Quick win (Info case: Decide first / None) | `maxdop` (first two cases) |
| **INS05** Cost threshold | <= 5, MAXDOP <> 1, > 1 CPU | Medium | Online / Quick win | `cost-threshold` |
| **INS06** Optimize for ad hoc | off; Medium when single-use plans >= 100 MB and > 25% of the cache, else Low | as stated | Online / Quick win | `optimize-adhoc` |
| **INS07** Instant file initialization | off (2016 SP1+; skipped before) | Medium | Window / Quick win | |
| **INS08** Lock pages in memory | CONVENTIONAL with >= 64 GB RAM, not Express (2016 SP1+) | Low | Window / Quick win | |
| **INS09** Priority boost | on | High | Window / Quick win (needs a restart; Do this is the statement) | |
| **INS10** Backup compression default | off, engine edition Standard/Enterprise/MI | Low | Online / Quick win | `backup-compression` |
| **INS11** Server fill factor | not 0 or 100 | Low | Window / Moderate | |
| **INS12** Windows low on memory | state low now or signals in 7 days (ring buffer rule as the scan, system-wide only) | High when now or in 24 h, else Medium | Decide first / Moderate | |
| **INS13** Power plan | not High performance (xp_regread; skipped without sysadmin) | Low | Online / Quick win | |
| **DBC02** AUTO_CLOSE on | | High | Online / Quick win | `auto-close` |
| **DBC03** AUTO_SHRINK on | | High | Online / Quick win | `auto-shrink` |
| **DBC04** Page verify not CHECKSUM | | Medium | Online / Quick win | `page-verify` |
| **DBC05** Automatic statistics off | create or update off | High | Online / Quick win | `auto-statistics` (preview warns: confirm the vendor does not require this) |
| **DBC06** Compatibility level below the server's | < 120: Medium, else Low | Decide first / Moderate | |
| **DBC07** Owner login missing | | Low (Medium when TRUSTWORTHY on, as CPY05) | Online / Quick win | |
| **DBC09** Indirect checkpoints off | target_recovery_time = 0 (2016+) | Low | Online / Quick win | `indirect-checkpoints` |
| **DBC10** Query Store | off: Low; on but differs or not READ_WRITE: Medium | Online / Quick win | `query-store` (phase 1, unchanged) |
| **DBC11** TRUSTWORTHY on | | Low | Decide first / Moderate | |
| **CFG01** Legacy cardinality estimator forced | database-scoped configuration (2016+) | Low | Decide first / Moderate | |
| **CPY01** Where it came from | restored from another name or server (msdb restore history), or file names suggest a copy | Info | None | |
| **CPY02** Collation differs from the server | Medium, or Info when it matches a database this database's code reads | as stated | Decide first / Project (Info: None) | |
| **CPY04** Orphaned SQL users | one finding per user | Medium | Online / Quick win | |
| **CPY06** CLR code | assemblies but CLR off: High; may be blocked (TRUSTWORTHY off, not trusted, not loaded): Medium | as stated | Online / Quick win | |

Severity words, When mapping (Offline -> Window, Planned -> Decide first), Owner and Then check from `Checks.csv`
(CPY rows and CFG01 are Us). Finding, Why, Do this and Evidence verbatim from the sources with the numbers in.
The INS01 build table (scan lines 232-238, refreshed 2026-11-02) ships as data in `domain` with its date; the
Info summary line is the scan's `@Summary`.

## The general fix pattern (replaces the one-off Query Store endpoints for new fixes; the old ones stay)

A **fix** is a named change the app can make, derived from a finding. The finding carries `fixId`; the card
detail lists the fixes available right now; preview and apply are generic.

```ts
interface Finding { ...as before; fixId?: string }   // present when the app can apply the change

interface Fix {
  id: string                 // "max-server-memory", "maxdop", "cost-threshold", "optimize-adhoc",
                             // "backup-compression", "auto-close", "auto-shrink", "page-verify",
                             // "auto-statistics", "indirect-checkpoints", "query-store"
  title: string              // "Max server memory"
  scope: "server" | "database"
  checkId: string
  setting: string            // "max server memory (MB)" / "AUTO_CLOSE"
  current: string            // "2147483647 MB" / "ON"
  target: string             // "26624 MB" / "OFF"
  needsRestart: boolean      // false for every fix the app offers; a restart-needing setting is never a fix
}

interface FixPreview {       // same shape as phase 1
  database: string           // "" for a server-scope fix
  engineVersion: string
  changes: Drift[]           // one row: setting, current, target
  statement: string
  rollback: string
  warnings: string[]
}

interface FixResult {
  preview: FixPreview
  before: string             // the value read before, as text
  after: string              // the value read back after
  verified: boolean          // after == target
  changeId: number
  appliedAt: string
}

interface CardDetail { ...as before; fixes?: Fix[] }   // the fixes available on this card right now
```

| Method and path | Response |
|---|---|
| `GET .../databases/{db}/cards/setup` | `CardDetail` with `fixes` (only fixes whose finding fired) and `findings[].fixId`. |
| `POST .../databases/{db}/fixes/{fixId}/preview` | `200` `FixPreview`; `404` unknown fix id or the finding did not fire (nothing to apply); `409` cannot be applied (secondary replica, database not online). Never writes. |
| `POST .../databases/{db}/fixes/{fixId}/apply` | `200` `FixResult`; same errors; `502` when SQL Server refuses (the message names the permission: `ALTER SETTINGS` for server fixes, `ALTER` on the database for database fixes). Runs the previewed statement, reads the value back, writes a `change` row (object = the setting, type SETTING, description "{setting} on {server or database}: {current} -> {target}"). |

Rules for every fix: the statement is built in `domain` from the read values, never from client input; the
preview carries the rollback (the previous value as the same kind of statement); the apply re-reads and reports
`verified`. The Query Store fix keeps its own preview/apply routes from phase 1 and is also reachable through the
generic routes with id `query-store`.

### The statements and warnings

| Fix | Statement | Rollback | Warnings |
|---|---|---|---|
| `max-server-memory` | `EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'max server memory (MB)', {suggested}; RECONFIGURE;` where suggested = RAM minus (RAM/4 when RAM <= 16 GB; else max(RAM/10, 4 GB)), as the scan and Apply-DoNow | same with the current value | "This is a server setting: it changes every database on {server}." · "SQL Server clears its plan cache: queries recompile for a few minutes." · "The suggestion leaves room for Windows only. If other software runs on this server (another instance, SSRS, a web shop, Granite services), set a lower value by hand." |
| `maxdop` | `... 'max degree of parallelism', {min(cores per NUMA node, 8)}` | current | server · plan cache |
| `cost-threshold` | `... 'cost threshold for parallelism', 50` | current | server · plan cache |
| `optimize-adhoc` | `... 'optimize for ad hoc workloads', 1` | 0 | server · plan cache |
| `backup-compression` | `... 'backup compression default', 1` | 0 | server · "Ignore if a third-party tool takes the backups." |
| `auto-close` | `ALTER DATABASE [db] SET AUTO_CLOSE OFF;` | ON | |
| `auto-shrink` | `ALTER DATABASE [db] SET AUTO_SHRINK OFF;` | ON | |
| `page-verify` | `ALTER DATABASE [db] SET PAGE_VERIFY CHECKSUM;` | the previous option | "Pages are protected as they are next written; an index rebuild protects them all." |
| `auto-statistics` | `ALTER DATABASE [db] SET AUTO_CREATE_STATISTICS ON; ALTER DATABASE [db] SET AUTO_UPDATE_STATISTICS ON;` (only the ones that are off) | the ones that were off | "Check first that the application vendor does not require this (rare)." |
| `indirect-checkpoints` | `ALTER DATABASE [db] SET TARGET_RECOVERY_TIME = 60 SECONDS;` | `= 0 SECONDS` | |

Server fixes need `ALTER SETTINGS` (sysadmin or serveradmin); database fixes need `ALTER` on the database. The
preview says which.

## The card

- Facts (four): `Version` = "{SQL Server 20xx} {level}, {up to date | N months behind | out of support}" (bad
  behind, worse out of support); `Memory` = "{max} of {RAM} GB cap" or "no cap of {RAM} GB" (bad on INS02/INS03);
  `Parallelism` = "MAXDOP {n}, cost threshold {n}" (bad on INS04/INS05); `Query Store` = the phase 1 summary (tone
  as phase 2).
- status / statusText / verdict / toDo / forClientIT / fixable: the phase 3a rules; `fixable` = number of fired
  findings with a `fixId`.
- Reads: the scan's environment block (lines 188-205) plus `sys.dm_os_sys_memory`, the ring buffer for INS12,
  `sys.dm_server_services` and `sys.dm_os_sys_info` columns for INS07/INS08 (2016 SP1+), `xp_regread` for INS13 in
  its own TRY (skipped without sysadmin), `sys.dm_exec_cached_plans` sums for INS06, `sys.databases` columns for
  DBC, and inside the database: `sys.database_scoped_configurations`, `sys.database_principals` with
  `sys.server_principals` for CPY04, `sys.assemblies`, `sys.trusted_assemblies`, `sys.dm_clr_loaded_assemblies`
  for CPY06, `sys.sql_expression_dependencies` and `sys.columns` for CPY02, `msdb.dbo.restorehistory` for CPY01
  (skipped note when msdb cannot be read). SELECTs only; the only writes are the fixes' statements.
- One `card_read` per read, as before.

## Frontend

- `FindingCard` gets an **Apply** button when the finding has a `fixId`; it opens a generic `FixDialog`
  (preview: the changes table, warnings, statement with copy, collapsible rollback; Apply; result with before,
  after, verified and the change id). The phase 1 `QueryStoreFixDialog` is replaced by the generic dialog, which
  the Setup card's Query Store row also uses (its preview shape is identical).
- After an apply, the card page re-reads the detail so the finding disappears or changes and the status line
  updates.
- The Setup card page: findings (with Apply where available), then the Query Store row only if DBC10 did not
  fire (otherwise the DBC10 finding with its Apply covers it), then What was checked.
- Mock: a Setup detail for the three databases with a spread of findings (INS05 with its fix, DBC02 with its fix,
  CPY04, INS01 Info summary), the generic preview/apply for `cost-threshold` and `auto-close` returning results.
