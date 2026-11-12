# Where kakapo's change history comes from today, and why older changes fall away

Written on 2026-11-12 from the code on `main` at 349d818, for the "Build it into Kakapo" list of the case note
"Kakapo: Where is SQL Server memory going?" (page 24: "Find out where Kakapo's change history comes from today,
and why older changes fall away"). It is the ground for phase 5w (`phase5w-contract.md`). SQL Server facts were
checked read-only on `localhost\SQL2022` the same day.

## The answer

Nothing kakapo records is ever purged: the `change` table in the local DuckDB file has no retention, and its one
`DELETE` only re-files the default-trace rows of the hours the trace still holds. Changes fall out of **view**
because every reader is capped (the database page shows the newest 10 of that one database, the tuning report
200 in its window, the query page 8 days, the indexes page 30 days, and there is no instance-level list), and
server settings such as the memory cap are filed under the database page they were applied from, so no other
page ever shows them. Changes made **outside** kakapo are lost when SQL Server's own history rolls over before
kakapo reads it, kakapo does not read setting changes from it at all, and nothing about kakapo's actions is
recorded on the server itself: the log exists only in the local DuckDB file.

## 1. Where it is kept

One table, `change`, created by migration 2 (`sc/internal/store/store.go:81-93`):
`id, server_id, db_name, changed_at, object, change_type, description, statement, rollback_statement, result`.

What a row does **not** hold: who ran it (Windows user, SQL login), from which machine, the kakapo version, how
long it took, and the value before. The fixes read the value before (for example `sc/internal/service/setup.go:255`)
and return it to the screen in `FixResult.Before`, but the row stores only the statement, the rollback and the
value after with "verified" or "differs" (`setup.go:265-288`). So the log can say "max server memory set to 98,304,
verified" but not "it was unlimited before" or who did it, from which machine.

## 2. What writes a row

| Writer | Where | When | Notes |
|---|---|---|---|
| Every fix | `store.InsertChange`, `sc/internal/store/querystore.go:54-74`, from about 40 call sites in `sc/internal/service` | **Once, after** the statement ran and was read back | Every fix enters through `ApplyFixWithOptions` (`setup.go:206`) or `ApplyQueryStoreFix` (`querystore.go:188`). Nothing is written **before** the statement runs: if kakapo or the connection dies in the middle, only a long run's completion check can write a row later (`runs.go:400-425`, "lost connection"). |
| Record a change (by hand, phase 5c) | `service/tuning.go:144` | When the consultant types it | `result = "recorded by hand"`. Nothing on the server. |
| Changes seen on the server (phase 5f) | `service/schemachanges.go:25-50` into `store.ReplaceSeenChanges`, `store/change.go:113-168` | On a **database page load** only (`service/overview.go:93`) | DDL from the default trace: Object:Created, Deleted, Altered (46, 47, 164), committed, user databases only (`DatabaseID > 4`), not kakapo's own (`collect/upkeep.go:221-226`). `result = "seen on the server"`. |

**The only delete.** `ReplaceSeenChanges` deletes and re-adds `seen on the server` rows from the hour after the
trace's oldest event on (`change.go:151`, `domain/schemachange.go:174`). Hours older than the trace are never
touched, so a harvested change outlives the trace. It never touches a fix's row or a row typed by hand.

**What the harvest does not keep.** The same trace read sees event 22 (ErrorLog), which carries every
`sp_configure` change with login, host and program, for example "Configuration option 'max server memory (MB)'
changed from 2147483647 to 98304" (checked live: the 2022 test instance's trace holds such rows for 'blocked
process threshold (s)', with login, host and program filled in). kakapo keeps
event 22 only for 7 days and only at severity 20 or above, as MNT06's severe errors (`upkeep.go:220`, `:257`).
So a memory cap or MAXDOP changed in SSMS is **never** in kakapo's change log. An `ALTER DATABASE` is kept, but
the trace says only "database altered" and by whom, never which option (no `TextData` on that event).

## 3. What reads it, and the caps

| Reader | Where | Cap |
|---|---|---|
| Database page, Recent changes | `service/overview.go:17` (`recentChanges = 10`), `:137`; shown by `web/src/components/recent-changes.tsx:22` from `screens/database.tsx:119` | **The newest 10**, `db_name = ?` exactly (`store/change.go:20-22`). No "show more". |
| `GET /api/servers/{id}/databases/{db}/changes` | `api/api.go:247-264`, `changesLimit` `:386-396` | Default 10, at most 100. The web client always asks for 10 (`web/src/api.ts:832`). |
| Tuning report markers | `service/tuning.go:56` | Its window plus a day, at most 200 (`store/change.go:50`). |
| Query page, events | `service/queries.go:58` | 8 days. |
| Indexes worth reorganising, "reorganised by kakapo" | `service/index_lens.go:47` | 30 days. |

**Server settings are filed under a database.** A server-scoped fix (max and min server memory, MAXDOP, cost
threshold, optimize for ad hoc, the default trace) writes `db_name` = the page it was applied from
(`setup.go:268`, `setup_actions.go:101`). `ListChanges` matches the name exactly, so the memory cap set from one
database's page never shows on another database's page, and nowhere at instance level. `ListChangesSince` does
take `db_name IS NULL` rows as server-level (`change.go:48`), but no writer leaves `db_name` empty: every row a
fix writes carries the page's database. After ten newer changes on that page the cap is out of view everywhere.
This is the case note's "set once and then fall out of view".

## 4. SQL Server's own history, and what kakapo does to it

| Source | Holds | Read by kakapo today |
|---|---|---|
| Default trace | 5 files of 20 MB, rolling (`sys.traces`: `max_files = 5`, `max_size = 20`, verified). On the quiet 2022 test instance that is 13 days; a busy server holds hours to days. Has DDL, `sp_configure` text (event 22), server starts, backups and restores, DBCC, memory changes (81), logins, with login, host and program. | Yes, the whole trace on every database page load (`upkeep.go:184-262`): DDL into the change log (above); the rest only as MNT05/MNT06 findings for the last 7 days. |
| Error log | The current file plus 6 archives by default (`sp_enumerrorlogs`; `NumErrorLogs` unset means 6); a new file at every restart. Has "Configuration option ... changed from ... to ...", "Setting database option RECOVERY to FULL for database ...", restarts. No login. Reading it needs securityadmin. | **No.** Nothing in `collect` calls `xp_readerrorlog`. |
| msdb backup history | Every backup with the recovery model in force (`backupset.recovery_model`). | The Backups card's current state only. |
| msdb job history and `sysjobs` | Jobs, `date_created`, `date_modified`, `enabled`. | The Upkeep card's current state only. |

**kakapo itself shortens two of these.** The maintenance plan's history cleanup (`maintenance-plan`, MNT08, and
the `kakapo_Maintenance` procedure's weekly task 6) deletes msdb backup and job history, Ola's `CommandLog` and
`kakapo_MaintenanceLog` older than the days kept, 30 by default (`domain/maintenance_fix.go:33`, `:86-91`;
`domain/maintproc_sql.go:275-287`). That is good practice for msdb's size, but it removes the backup rows that
show when a recovery model changed.

## 5. The case note's claims, checked

| Claim (pages 17-18, 22) | Holds? | Evidence |
|---|---|---|
| "Changes made through Kakapo fall away today" | **They fall out of view; they are not deleted.** | No retention on `change`; the caps of section 3; server settings filed under one database. |
| "The default trace keeps 5 files of 20 MB" | Yes. | `sys.traces` on the test instance. |
| "The error log ... keeps 6 old ones by default" | Yes. | `sp_enumerrorlogs`: current plus archives 1-6. |
| "If Kakapo's history comes from these sources, old changes disappear" | **Only for changes made outside kakapo, and only between visits.** kakapo's own changes do not come from them. What 5f harvests is kept for good; what rolled off before a database page was opened is lost, and the window line says so ("not seen: the trace turned over before kakapo was opened again"). Setting changes made outside kakapo are not harvested at all. | `schemachange.go:174`, `schemachanges.go:25-50`, `upkeep.go:220`. |
| "Kakapo's own changes are written to its log before and after each action, on the server and in Kakapo's local copy" | **Not yet** (it is the target). Today: after only, local only, without who, where, version, before value or duration. | Sections 1 and 2. |

## 6. Is anything recorded on the server today?

Not kakapo's change log. What kakapo leaves on a server is:

- Objects it installs through fixes, each with a remove fix: in master, `kakapo_Maintenance`,
  `kakapo_MaintenanceSettings` and `kakapo_MaintenanceLog` (the maintenance procedure's own run log, purged after
  the days kept), Ola Hallengren's `CommandLog`, `CommandExecute` and `IndexOptimize` (phase 5u); its Extended
  Events session; Agent jobs and alerts; archive tables. Objects carry `kakapo` in their name; the maintenance
  procedure's user also has an extended property `kakapo` (`maintproc_sql.go:478`).
- Traces of its connections in SQL Server's own history: the program name `kakapo` (formerly `SQLMonitor`;
  `collect/mssql.go:25`), and the `/* kakapo */` tag at the head of every statement it runs. Both roll off with the
  trace, the error log and Query Store.

Someone on the server without kakapo can therefore see **that** kakapo was there (objects, program name while the
trace holds it), but not **what** it changed or what the value was before. The local DuckDB file, on the
consultant's machine, is the only record.

## 7. What phase 5w changes

See `phase5w-contract.md`: a settings baseline kept for good, so a change outside kakapo is seen whether or not the
trace still holds it; the trace's `sp_configure` rows harvested beside the DDL; kakapo's own log written before
and after each action with who, where, version, before and after, locally and in `master.dbo.kakapo_ChangeLog`;
and an instance-level What changed? screen with no cap, where server settings are shown as server settings.
