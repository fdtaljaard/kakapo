# Phase 3d contract: the Upkeep card

Builds on phase 3a (finding model, card detail), 3c (the general fix pattern). Sources: `01-QuickScan.sql`
section 6 (lines 865-939: MNT01 to MNT03, Express handling), MNT04 (987-1008 per database, 1046-1059 the
finding), section 8 (1353-1436 MNT05 and MNT06 from the default trace, shrinking jobs, memory dumps; 1438-1469
MNT07); `Apply-DoNow.sql` step 5 (lines 155-193, the statistics update); `Checks.csv` MNT01 to MNT07 and STA01.
Field names exactly as written.

## Scope

One database page. Instance-level upkeep (Agent, jobs, trace events, dumps, alerts) appears because it decides
whether this database is looked after; the object of such a finding is `@@SERVERNAME`. MNT04 and the growth
events are about this database. STA01 is the triage's twin of MNT04 and is folded into it.

## The checks

| Check | Fires when (as the source) | Severity | When / Effort | Fix id |
|---|---|---|---|---|
| **MNT01** SQL Server Agent | Express: Info "SQL Server Express has no SQL Server Agent" and MNT02/03/07 skipped "Not applicable to Express edition". Otherwise `sys.dm_server_services`: not Running: High; running but startup not Automatic: Low; status not visible: skipped | as stated | Online / Quick win | |
| **MNT02** Failed jobs, 7 days | one finding per job with failures (`msdb.dbo.sysjobhistory`, step 0, run_status 0, last 7 days by run_date); name like "%backup%": High, else Medium. Evidence: last failure date and the last failing step's message | as stated | Online / Moderate | |
| **MNT03** Maintenance job | no enabled job whose name or command matches the scan's patterns and no maintenance plan subplan | Medium | Online / Moderate | |
| **MNT04** Out-of-date statistics | statistics on this database's tables with >= 10,000 rows, >= 20% of rows modified, not updated for 7+ days (`sys.dm_db_stats_properties`): one finding with the count, the evidence listing the five most changed tables | Medium | Online / Quick win | `update-statistics` |
| **MNT05** Shrinks, cache clears, growth | default trace (`sys.fn_trace_gettable` on the running default trace, last 7 days, event classes 22, 92, 93, 116): shrinks (TextData like SHRINK) and cache clears (FREEPROCCACHE, DROPCLEANBUFFERS, FREESYSTEMCACHE), one finding per kind per database: Medium; file growths >= 20 in 7 days for this database: Medium when >= 100 else Low; an enabled Agent job step that shrinks: Medium. Default trace not running or unreadable (needs ALTER TRACE): skipped with the note | as stated | Online / Quick win | |
| **MNT06** Crashes and severe errors | errors of severity >= 20 in the default trace, one finding per database (instance-wide ones with no database as object the server): High; memory dumps in 30 days (`sys.dm_server_memory_dumps`): High when within 7 days, else Medium | as stated | None / Moderate | |
| **MNT07** Alerts and operator | no enabled operator with an email, or any of the severity 19-25 / error 823-825 alerts missing (`msdb.dbo.sysalerts`, `sysoperators`) | Low | Online / Quick win | |

Owners and Then check from `Checks.csv` (MNT01, MNT06, MNT07 Client IT; the rest Us). Finding, Why, Do this and
Evidence verbatim from the scan with the numbers in. Every msdb read is in its own TRY: without msdb the MNT02,
MNT03, MNT07 and the shrinking-jobs part are skipped with "msdb is not readable with this login", the rest run.

## The statistics fix (`update-statistics`)

As `Apply-DoNow.sql` step 5, scoped to this database:

- **Current**: "{n} statistics on {m} tables out of date"; **target**: "none out of date".
- **Statement**: one `UPDATE STATISTICS [schema].[table];` line per table with a stale statistic, smallest table
  first (by `sys.dm_db_partition_stats` row count), run inside the database (`EXEC [db].sys.sp_executesql`).
  The preview shows every line.
- **Rollback**: none. The preview says "No rollback: updated statistics are never worse than stale ones."
- **Warnings**: "Updating statistics reads each table; on large tables this takes minutes and competes with
  users. Run it outside peak hours." · "This does not create a maintenance job (MNT03); it fixes today's
  statistics once." · "Needs ALTER on the tables (db_owner or db_ddladmin)."
- **Apply**: runs the lines as one batch; reads back the stale count; `verified` when it is 0; `after` = "{n}
  still out of date" otherwise; the change row's object is "Statistics", description "Statistics on {db}: {m}
  tables updated"; result "{after}, verified|differs". A long run is fine: the apply request waits up to 10
  minutes.
- Through the generic routes from phase 3c; the fix registry gains this entry.

## The card

- **Facts** (four): `SQL Agent` = "running, automatic" | "stopped" (worse) | "not automatic" (bad) | "none
  (Express)"; `Failed jobs, 7 days` = "{n}" (bad when > 0, worse when a backup job); `Maintenance job` = "found"
  or "none found" (bad) or "not checked (Express)"; `Told about errors` = "operator and {n} alerts" | "no
  operator" (bad) | "{n} alerts missing" (bad).
- status / statusText / verdict / toDo / forClientIT / fixable: the phase 3a rules. Green verdict: "SQL Server
  Agent is running, {maintenance job or 'maintenance runs'} and no job failed in 7 days."
- Grey when the instance-level reads all fail (no VIEW SERVER STATE), with the reason.
- Reads: `sys.dm_server_services`, msdb jobs/history/steps/subplans/alerts/operators, the default trace path from
  `sys.traces` and `sys.fn_trace_gettable`, `sys.dm_server_memory_dumps`, and inside the database
  `sys.stats` with `sys.dm_db_stats_properties` and `sys.dm_db_partition_stats`. SELECTs only; the only write is
  the fix.
- One `card_read` per read.

## Frontend

Nothing new beyond the phase 3c pieces: the finding with `fixId: "update-statistics"` shows Apply and uses the
generic dialog. The preview's statement block may be long (one line per table); the dialog's statement area
scrolls. The mock gets an Upkeep detail for the three databases (green; amber with MNT02 on a backup job as High
and MNT04 with the fix; amber with MNT01 stopped as High and MNT07) and the `update-statistics` preview with
four UPDATE STATISTICS lines and the no-rollback note.
