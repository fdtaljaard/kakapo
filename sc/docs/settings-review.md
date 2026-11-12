# Settings review: every fix against "no setting disappears behind a green tick"

Reviewed 2026-11-12 against the principle and rules on pages 20 to 22 of the memory case note
("Kakapo principle: no setting disappears behind a green tick", "Rules for changing a setting from
Kakapo", "How each kind of Kakapo action is undone"). Scope: all 52 fixes in `domain.FixDefs`
([fix.go:66-133](../internal/domain/fix.go)); 39 of them register their own prepare and apply from an
`init()` in `internal/service` ([fixreg.go:38-64](../internal/service/fixreg.go)); the other 13 are
the ten plain setting fixes, `query-store` (own apply), and the two backup-schedule fixes (Backups card branch,
[setup.go:244-249](../internal/service/setup.go)). Read-only review: no code was changed.

The principle asks every setting to show: the current value read live, the recommended value and how
it was worked out, a status in words (Set by kakapo / Matches / Differs / Changed outside kakapo), who
set it, when and what it was before, what changing it does, and the actions Change, Revert, Show
script. The rules: preview first, always audited, reversible where possible, re-read after applying,
drift shown.

## What is true for every fix (read this before the table)

| Question | The mechanism | Evidence |
|---|---|---|
| Preview first | Every fix builds a `FixPreview` with statement, rollback, warnings and the permission needed before anything runs. Apply runs the previewed statement, never client input. | [setup.go:154-175](../internal/service/setup.go), [fix.go:284-312](../internal/domain/fix.go) |
| Re-read after applying | The generic apply reads the setting before and after (`collect.ReadSetting`, `value_in_use` for server settings) and answers `Before`, `After`, `Verified`. Registered fixes read their own object back and log through `suLog`, which writes "`<after>`, verified" or ", differs". | [setup.go:256-294](../internal/service/setup.go), [setup_actions.go:98-132](../internal/service/setup_actions.go), [collect/setup.go:538-569](../internal/collect/setup.go) |
| Logged | Every apply, generic or registered, writes one `change` row: server, database, time, object, type, description, statement, rollback statement, result. There are **no before and after columns and no actor column**; before and after live in the description text ("setting on server: current -> target", [fix.go:437-447](../internal/domain/fix.go)) and in the result text. kakapo has no user concept, so "who" is always "kakapo". | [store.go:82-93](../internal/store/store.go), [querystore.go:54-73](../internal/store/querystore.go) |
| The log row is not where you look | A **server** fix is logged under the database it was applied from (`Database: name`, [setup.go:268](../internal/service/setup.go)). The page lists `WHERE db_name = ?` ([change.go:20](../internal/store/change.go)). A server setting therefore shows in Recent changes only on one database's page. | [overview.go:137](../internal/service/overview.go) |
| Revert is a script, not an action | The rollback statement is shown in the preview and Copy script ([fix-dialog.tsx:106-108, 652-661](../web/src/components/fix-dialog.tsx)) and stored in `rollback_statement`, but the entry sent to the page leaves out statement and rollback ([overview.go:144-148](../internal/domain/overview.go), `Change` in [api.ts:712-724](../web/src/api.ts)). After the dialog is closed the page cannot show or run it. Only the pairs named "Revert fix" in the table below have a fix that undoes them. | |
| Drift ("changed outside kakapo") | Two things exist, neither is the principle's. (a) A finding fires again when the live value breaks its rule (not a statement that someone changed it, and silent while the value is still acceptable). (b) CHG01 reads SQL Server's default trace into the change log: DDL on objects in user databases, and `ALTER DATABASE` as a bare "database altered" row without the option ([schemachange.go:124-137](../internal/domain/schemachange.go), [overview.go:92](../internal/service/overview.go)). **Server configuration changes (`sp_configure`) are not read from anywhere**: the default trace does not hold them, and nothing calls `xp_readerrorlog` (the only mentions are advice text, [setup.go:1069-1070](../internal/domain/setup.go)). | |
| `show advanced options` | The server `sp_configure` statement sets `show advanced options` to 1 first and never sets it back; neither the rollback nor the change log mentions it ([fix.go:183-185](../internal/domain/fix.go)). A setting kakapo changes that then disappears. | |

Legend for the table. **Live** = the setting's value is read live and shown somewhere after the
fix (not only in the apply answer). **Why** = a recommendation with reasoning is shown (every fix
comes from a finding with Why, Do this and Evidence, [finding.go:92-104](../internal/domain/finding.go);
"value" means the number itself is explained). **Revert fix** = a fix id that undoes it; "script"
means the rollback statement exists in the preview and the log row but no fix runs it. **Log** =
change row with before and after in text. **Drift** = see above: **find** = finding re-fires,
**trace** = default-trace row, **none** = nothing.

## max-server-memory (INS02 / INS03)

The case this review came from. The fix sets `max server memory (MB)` to
`SuggestedMaxMemoryMB(RAM)`: RAM minus a quarter up to 16 GB, else minus the larger of RAM/10 and
4 GB ([setup.go:298-308](../internal/domain/setup.go)). It is offered only while a finding fires:
INS02 when the cap is still 2147483647, or INS03 when Windows has less than 2 GB or the cap is over
95% of RAM ([setup.go:604-632](../internal/domain/setup.go), [fix.go:304-310](../internal/domain/fix.go)).
The "SQL Server may use less than half" INS03 variant carries no fix. What exists: the current value
is read live on the Setup card as "X of Y GB cap" or "no cap of Y GB" ([setup.go:1533-1541](../internal/domain/setup.go)),
from `value_in_use` ([collect/setup.go:87](../internal/collect/setup.go)); the preview shows
statement, rollback to the value read a moment before, the plan-cache and "leaves room for Windows
only" warnings, and any pending changes the RECONFIGURE would also activate ([fix.go:331-358](../internal/domain/fix.go));
Apply re-reads `value_in_use` and writes a change row "max server memory (MB) on SERVER: unlimited
-> 96 GB" with the rollback. What falls out of view: **the moment the cap is in a sensible place the
finding is gone, and with it the fix, the recommendation and every action**. After that the card
shows a bare number; there is no recommended value or how it was worked out (the formula is only in
a code comment; the Why text is generic, [setup.go:492-494](../internal/domain/setup.go)), no status
in words, no "set by kakapo on 4 Nov, previously unlimited" beside the setting (that sentence exists
only as a change row under one database's page), no Change and no Revert. The suggestion cannot
take a value of your own either: `buildFix` has no options for this fix, so the Granite case (RAM
minus Windows minus SSRS and services, 96 GB) has to be done by hand with the warning as the only
nod ([fix.go:154](../internal/domain/fix.go)). If someone later moves the cap from 96 GB to 120 GB on
a 128 GB box, INS03 stays silent (more than 2 GB and under 95%), nothing says it changed, and the
`sp_configure` change is in neither the default trace nor kakapo's log. The fix's sibling
`min-server-memory` ([setup_actions.go:307-347, 388-400](../internal/service/setup_actions.go)) is the one
place a memory setting can be set back, and only for min, only when it is at 90% of max.

## The table

| Fix id | What it changes | Live after | Why | Revert fix | Log (before, after) | Drift |
|---|---|---|---|---|---|---|
| `max-server-memory` | sp_configure max server memory to RAM-based suggestion ([fix.go:331](../internal/domain/fix.go)) | Yes: Setup card Memory fact, no history | Finding Why only; value not explained; no custom value | No (script); fix disappears once set | Yes, under the applying database only | None |
| `maxdop` | sp_configure MAXDOP to suggestion ([fix.go:337](../internal/domain/fix.go)) | Yes: Setup card Parallelism fact ([setup.go:1544](../internal/domain/setup.go)) | Finding Why; suggestion rule not shown | No (script); fix disappears | Yes, under applying database | None |
| `cost-threshold` | sp_configure cost threshold to 50 ([fix.go:339](../internal/domain/fix.go)) | Yes: Parallelism fact | Finding Why (ins05Why) | No (script) | Yes | None |
| `optimize-adhoc` | sp_configure optimize for ad hoc workloads to 1 | **No**: finding vanishes, no fact | Finding Why | No (script) | Yes | find (only if set back to 0) |
| `backup-compression` | sp_configure backup compression default to 1 | **No** | Finding Why plus third-party warning ([fix.go:155](../internal/domain/fix.go)) | No (script) | Yes | find (only if set back to 0) |
| `auto-close` | ALTER DATABASE AUTO_CLOSE OFF ([fix.go:365](../internal/domain/fix.go)) | **No**: finding only | Finding Why | No (script) | Yes | trace ("database altered", no option) |
| `auto-shrink` | AUTO_SHRINK OFF | **No** | Finding Why | No (script) | Yes | trace |
| `page-verify` | PAGE_VERIFY CHECKSUM; rollback only if the old value is plain SQL ([fix.go:373-381](../internal/domain/fix.go)) | **No** | Finding Why plus "protected as written" warning | No (script, sometimes none) | Yes | trace |
| `auto-statistics` | AUTO_CREATE/UPDATE_STATISTICS ON ([fix.go:382-397](../internal/domain/fix.go)) | **No** | Finding Why plus vendor warning | No (script) | Yes, one description for both options | trace |
| `indirect-checkpoints` | TARGET_RECOVERY_TIME 60 s ([fix.go:398](../internal/domain/fix.go)) | **No** | Finding Why | No (script) | Yes | trace |
| `query-store` | Query Store options to recommended ([querystore.go:435-475](../internal/domain/querystore.go), apply [service/querystore.go](../internal/service/querystore.go)) | **Yes**: Query Store fact, `Summary` per read ([setup.go:1550](../internal/domain/setup.go)) | Finding Why; per-option Drift list vs recommendation | No (script); closest to the principle | Yes (Before and After summaries) | **Yes, against the recommendation** (`VerdictDrift`, [querystore.go:70-79](../internal/domain/querystore.go)); not "outside kakapo" |
| `update-statistics` | UPDATE STATISTICS on stale statistics ([upkeep.go:899-927](../internal/domain/upkeep.go), apply [upkeep.go:81-116](../internal/service/upkeep.go)) | n/a (action): count of stale statistics on next read | Finding Why | None needed ("no rollback", [upkeep.go:208](../internal/domain/upkeep.go)) | Yes | n/a |
| `create-index` | CREATE INDEX ([index_fix.go:671](../internal/domain/index_fix.go), [index_fix.go:95-146](../internal/service/index_fix.go)) | Yes: the new index is read on the next Design and Indexes read | Finding Why plus benefit estimate | Yes: `drop-index` (rollback is a DROP) | Yes | trace (index created or dropped outside) |
| `drop-index` | DROP INDEX ([index_fix.go:850](../internal/domain/index_fix.go)) | Index gone from lens | Finding Why plus usage evidence | Script only: rollback re-creates it ([index_fix.go:895](../internal/domain/index_fix.go)) | Yes | trace |
| `disable-index` | Disables a duplicate index so it can be rebuilt ([index_fix.go:1266, 1328](../internal/domain/index_fix.go)) | Yes: the index lens reads `IsDisabled` ([index_fix.go:63](../internal/domain/index_fix.go)) | Finding Why plus duplicate evidence | Script only (rebuild); no "re-enable" fix | Yes | trace |
| `reorganize-index` | ALTER INDEX REORGANIZE ([reorganize_fix.go:183](../internal/domain/reorganize_fix.go)) | n/a (action); Indexes page shows effect | Finding Why plus pages saved | None needed (maintenance, matches page 22) | Yes | n/a |
| `reorganize-indexes` | The same, batch from the Indexes page ([reorganize_batch.go:112, 177](../internal/domain/reorganize_batch.go)) | n/a | Ranked by pages saved | None needed | One row per batch ([reorganize_batch.go:200](../internal/service/reorganize_batch.go)) | n/a |
| `kill-session` | KILL one blocking session ([blocking_fix.go:76](../internal/domain/blocking_fix.go)) | n/a (action) | Blocking evidence | None possible | Yes | n/a |
| `rcsi` | READ_COMMITTED_SNAPSHOT ON ([blocking_fix.go:254-300](../internal/domain/blocking_fix.go)) | Yes: the RCSI decision ([decision_rcsi.go](../internal/domain/decision_rcsi.go)) is rebuilt from a fresh read | Decision with reasoning (blocking history) | Script only (OFF) | Yes | trace |
| `force-plan` | Query Store plan forcing ([query_fix.go:191-260](../internal/domain/query_fix.go)) | Yes: plans carry `Forced` ([query.go:277](../internal/domain/query.go)) | Faster-plan comparison | Yes: `unforce-plan` | Yes | none (a plan forced or unforced outside is not read) |
| `unforce-plan` | Stops forcing | Yes | Plan comparison | Yes: `force-plan` | Yes | none |
| `evict-plan` | DBCC FREEPROCCACHE for one plan ([query_fix.go:326](../internal/domain/query_fix.go)) | n/a | Slow-plan evidence | None needed | Yes | n/a |
| `update-statistics-table` | UPDATE STATISTICS on one table ([query_fix.go:417](../internal/domain/query_fix.go)) | n/a | Estimate-skew evidence | None needed | Yes | n/a |
| `multi-user` | SET MULTI_USER ([setup_actions.go:100-135](../internal/domain/setup_actions.go)) | **No** (finding only) | Finding Why | Script only (back to old mode) | Yes | trace |
| `reconfigure` | RECONFIGURE of pending sp_configure values, or revert them ([setup_actions.go:259-331](../internal/domain/setup_actions.go)) | Pending list only until applied | Pending diff shown (value vs in use) | Itself the undo of a pending change; rollback restores old values | Yes | **Detects only the pending state** (value != value_in_use), not who changed it |
| `trace-flag-off` | DBCC TRACEOFF(-1) for a flag ([setup_actions.go:459-513](../internal/domain/setup_actions.go)) | Finding lists active flags until off; nothing after | Finding Why | Script only: `DBCC TRACEON(n, -1)`; **not persisted across restart, nor is a startup `-T` removed** | Yes | find (flag turned on again); no "when/who" |
| `scoped-config-reset` | ALTER DATABASE SCOPED CONFIGURATION to default ([setup_actions.go:572-619](../internal/domain/setup_actions.go)) | **No** | Finding Why | Script only | Yes | find |
| `compat-level` | Raises compatibility level ([compat_fix.go:64-103](../internal/domain/compat_fix.go)) | **No**: only inside findings | Finding Why plus feature notes | Script only (old level) | Yes | trace |
| `min-server-memory` | min server memory back to 0 ([setup_actions.go:388-400](../internal/domain/setup_actions.go)) | **No**: only INS16 evidence | Finding Why (min at 90% of max) | Script only | Yes | find |
| `operator-alerts` | Creates the operator and alerts ([upkeep_actions.go:468-495](../internal/domain/upkeep_actions.go)) | Yes: "Told about errors" fact ([upkeep.go:810](../internal/domain/upkeep.go)) | Finding Why | Script (plan.rollback) | Yes | find (alert disabled or removed) |
| `maintenance-job` | Creates the statistics job ([upkeep_actions.go:762-828](../internal/domain/upkeep_actions.go)) | Yes: "Maintenance job" fact | Finding Why | Script (delete job); no remove fix | Yes | find |
| `remove-shrink-step` | Removes a shrink step from an Agent job ([upkeep_actions.go:932-993](../internal/domain/upkeep_actions.go)) | Finding clears; step gone | Finding Why (shrink hurts) | Script (re-adds or re-enables) | Yes | find |
| `autogrowth` | ALTER DATABASE MODIFY FILE FILEGROWTH ([storage_actions.go:176-286](../internal/domain/storage_actions.go)) | Partly: Log and Database size facts, not the growth setting | Finding Why plus sizing rule | Script (old growth) | Yes | trace |
| `add-tempdb-files` | Adds TempDB data files ([storage_actions.go:376-458](../internal/domain/storage_actions.go)) | Yes: TempDB fact | Finding Why plus CPU rule | Script (remove files; fails if used) | Yes | trace not for tempdb (system database) |
| `tempdb-files` | Sets the whole TempDB file set ([storage_actions.go:714-896](../internal/domain/storage_actions.go)) | Yes: TempDB fact | Finding Why plus sizing | Script | Yes | none for tempdb |
| `recovery-model` | ALTER DATABASE SET RECOVERY ([backups_actions.go:264-348](../internal/domain/backups_actions.go)) | **No**: not shown as a value; finding only | Finding Why plus log-backup requirement | Script (old model) | Yes | trace |
| `checkdb-job` | Creates the weekly integrity job ([backups_actions.go:604-703](../internal/domain/backups_actions.go)) | Yes: Last CHECKDB fact | Finding Why | Script (delete job); no remove fix | Yes | find |
| `checkdb-now` | DBCC CHECKDB (long) ([backups_actions.go:739](../internal/domain/backups_actions.go)) | Yes: last run stored, shown | Finding Why plus room check | None needed | Yes (run result) | n/a |
| `suspect-pages-clear` | DELETE from msdb.dbo.suspect_pages ([backups_actions.go:815-852](../internal/domain/backups_actions.go)) | Damaged pages fact | Finding Why | **None, said so**; the result keeps the rows ([backups_actions.go:70](../internal/domain/backups_actions.go)) | Yes, rows in the result | n/a |
| `archive-table` | Archive or delete old rows via a job ([archive_fix.go:527-546](../internal/domain/archive_fix.go)) | Yes: table picker "archived by kakapo" | Finding Why plus row count and size | Yes: `remove-archive` stops the job; moving rows back is not offered | Yes | none |
| `remove-archive` | Drops the archive job ([archive_fix.go:610-659](../internal/domain/archive_fix.go)) | Picker updates | Why shown | Script (re-create job) | Yes | none |
| `history-cleanup` | Creates the msdb history pruning job ([maintenance_fix.go:116-223](../internal/domain/maintenance_fix.go)) | Via plan state | Finding Why | Script (delete job); no remove fix | Yes | find |
| `maintenance-plan` | Composite: backups, stats, history, alerts ([maintenance.go:477-819](../internal/domain/maintenance.go)) | Yes: plan state read each time | Per-piece Why | Script (`PlanStatements` rollback); no single remove | Yes (one row) | find per piece |
| `install-xe-session` | Creates kakapo's Extended Events session and sets the blocked process threshold ([helpers.go:337-562](../internal/domain/helpers.go)) | **Yes: Installed helpers panel** ([helpers.go:640-700](../internal/domain/helpers.go)) | Sentence says what it keeps and why | **Yes: `remove-xe-session`** (rollback restores the threshold) | Yes | Panel flags "not created by kakapo" and a stopped session |
| `remove-xe-session` | Drops the session | Panel | Why shown | Yes: `install-xe-session` | Yes | as above |
| `enable-default-trace` | sp_configure default trace enabled 1 ([schemachange.go:545-559](../internal/domain/schemachange.go)) | Yes: line under Recent changes says what the trace holds ([schemachange.go:468-474](../internal/domain/schemachange.go)) | Finding Why; warns someone may have switched it off | Script (set 0) | Yes | find (CHG01 when off) |
| `backup-schedule` | Agent backup jobs ([backupschedule.go:666-799](../internal/domain/backupschedule.go)) | Partly: the Backups card shows the last backups, not the schedule | Finding Why plus worst-case data loss | Yes: `remove-backup-schedule` | Yes | find |
| `remove-backup-schedule` | Removes kakapo's backup jobs ([backupschedule.go:974-997](../internal/domain/backupschedule.go)) | Card updates | Why shown | Rollback is the old batch; **none if the folder was changed by hand** | Yes | find |
| `install-maintenance-proc` | Installs `master.dbo.kakapo_Maintenance` for Express ([maintproc.go:680-816](../internal/domain/maintproc.go)) | **Yes: Installed helpers panel** | Sentence | **Yes: `remove-maintenance-proc`** | Yes (HELPER row) | Panel flags foreign proc |
| `remove-maintenance-proc` | Drops it, restores rows ([maintproc.go:902-974](../internal/domain/maintproc.go)) | Panel | Why shown | Yes: install | Yes | Panel |
| `install-ola-index` | Installs Ola Hallengren's IndexOptimize in master ([ola_fix.go:84-104](../internal/domain/ola_fix.go), apply [ola_fix.go:35-65](../internal/service/ola_fix.go)) | **No**: not in the helpers panel (only the XE session and kakapo_Maintenance are); the Indexes page uses it | Why shown in preview | **No**: rollback is two DROP PROCEDUREs; no remove fix | Yes (HELPER row) | None (a changed or dropped procedure is not noticed) |
| `add-primary-key` | Adds a primary key ([primarykey_fix.go:236-323](../internal/domain/primarykey_fix.go)) | Yes: the No primary key fact shrinks | Finding Why plus key choice | Script (DROP CONSTRAINT) | Yes | trace for the table, not the key |

Not fixes, but they set something and live beside them: **Record a change** by hand
([tuning.go:288-310](../internal/domain/tuning.go)) is how an outside change gets into the log, and it
is manual; the **Express maintenance rows** in `master` (`kakapo_Maintenance` settings) are shown in
the helpers panel only as a sentence.

## Gaps worth fixing first, ranked as an expert would

1. **A "Settings kakapo applied" page (or card) per server that lists, for every setting kakapo has
   ever changed, the current live value, recommended value and how it was worked out, a status in
   words, who/when/previously, and Change / Revert / Show script, whether or not a finding
   fires.** This is the root cause of the memory case: the fix and its recommendation exist only
   while the finding fires ([fix.go:304-310](../internal/domain/fix.go)), and it is the principle on
   page 20. Build it from the `change` table: `max-server-memory` first, then every sp_configure and
   database-option fix (13 of the 52 rows have no live value at all after the fix).
2. **Detect and show "changed outside kakapo" for server configuration.** Compare the live
   `sys.configurations` value to the last value kakapo logged for that setting, and read the error
   log's "Configuration option 'X' changed from A to B" lines (all retained logs) into the change log
   next to the default-trace rows. Today a cap moved from 96 to 120 GB leaves no trace anywhere
   kakapo looks, and INS03 stays silent. The same comparison covers database options (the trace only
   says "database altered").
3. **Make Revert a real action from the change log.** Keep the rollback in the entry sent to the
   page ([overview.go:144-148](../internal/domain/overview.go)), add Revert to a setting row that
   previews the stored rollback, runs it and logs the revert as its own row (rule "Revert is itself
   logged"). Today only six fix pairs have a named undo fix (XE session, maintenance procedure, backup schedule, archive, plan forcing, create and drop index); every other setting's undo is a script
   inside a closed dialog.
4. **Let `max-server-memory` take your value and show its sum.** Options for the number (and a
   separate "leave for other services" figure), the arithmetic in the preview ("128 GB, minus 12.8 GB
   for Windows, minus 14 GB for SSRS"), so the Granite case is not a hand edit.
5. **Log server fixes at server level and show them everywhere.** Write `db_name` as NULL for a server
   fix (the tuning-report query already allows it, [change.go:48](../internal/store/change.go)) and
   list server changes on every database page and on the instance page.
6. **Show the value for the settings that only exist inside a finding:** optimize for ad hoc,
   backup compression, recovery model, compatibility level, AUTO_CLOSE/SHRINK, PAGE_VERIFY, statistics
   options, scoped configuration, min server memory, autogrowth. One line each on a database or
   instance settings table would do.
7. **Put `install-ola-index` and the Agent jobs kakapo created (CHECKDB, statistics, history
   cleanup, backup schedule, archive jobs) in the Installed helpers panel with a Remove action,** as
   the Extended Events session and `kakapo_Maintenance` already are. Ola's procedures have no remove
   fix at all.
8. Small: restore `show advanced options` to its previous value or log it, and say that
   `trace-flag-off` is not durable across a restart.
