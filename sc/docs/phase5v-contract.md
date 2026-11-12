# Phase 5v contract: the Memory screen, version 1

Decided on 2026-11-12 from Francois's case note "Kakapo: Where is SQL Server memory going?" (a Granite
production instance holding 97.9 GB with no cap). He gave full authority to decide while away; the
decisions below are taken, not proposals. Build is V1 of that note; V2 (health checks with sampled
counters, top readers) and V3 (change history panel, other Windows processes) come later.

The question: **"where is the memory going, and is something abnormal?"**, answered on one screen in under
a minute by a technical consultant who is not a DBA. Today it took about ten handwritten queries, three of
them wrong. Full is SQL Server's normal state: high memory use alone is never the finding.

SCOPE section 1a rules 1, 2 and 4. Read-only: the screen changes nothing on the server.

## What it is not

- **Not live.** Nothing polls. The buffer pool scan is heavy (about 12.5 million rows on a 100 GB cache),
  so it runs only when someone clicks.
- **Not a new fix.** "Set max server memory" already exists (`max-server-memory`, INS02/INS03 on the Setup
  card). This screen links to it; it does not apply anything.
- **Not other Windows processes.** T-SQL cannot see them. RAM minus SQL Server minus free is shown as
  "everything else", labelled as such. No xp_cmdshell, ever.

## Decisions

1. **Where it lives.** An instance page, `/instances/:id/memory`, titled "Memory". Linked from the instance
   page (a row or link beside the other instance-level entries) and from INS02/INS03/INS12's *Do this*.
2. **Four panels, top to bottom, each one layer deeper** (the note's wireframe, solid panels only):
   1. **Verdict line** (decision 6), first, before any number.
   2. **Memory at a glance**: one bar of physical RAM split into SQL Server, everything else, and free.
      Under it: SQL Server holds (`sys.dm_os_process_memory.physical_memory_in_use_kb`, plus
      `locked_page_allocations_kb` said separately), target (`sys.dm_os_sys_info.committed_target_kb`),
      cap (`max server memory (MB)`, "no cap" for 2147483647), min server memory, uptime
      (`sqlserver_start_time`), memory model (`sql_memory_model_desc`: Lock Pages in Memory on or off,
      with the sentence "With Lock Pages in Memory on, Task Manager shows SQL Server far smaller than it
      is" when on), page life expectancy per NUMA node (Buffer Node counter, as a plain fact, no threshold
      in V1), Memory Grants Pending. On Express, say the edition caps the buffer pool (1,410 MB per
      instance before 2025; read the version as `collect` already does) and that "no cap" is not a worry.
   3. **Inside SQL Server**: `sys.dm_os_memory_clerks` grouped into Data cache
      (MEMORYCLERK_SQLBUFFERPOOL), Plan cache (CACHESTORE_SQLCP, CACHESTORE_OBJCP, CACHESTORE_PHDR),
      Locks (OBJECTSTORE_LOCK_MANAGER), Query workspace (MEMORYCLERK_SQLQUERYEXEC plus granted workspace
      from the Memory Manager counter), Other. GB and share. Expandable to the top 10 clerks by type. Plan
      cache line also shows single-use ad hoc MB (`sys.dm_exec_cached_plans`, `usecounts = 1`,
      `objtype = 'Adhoc'`, `SUM(CAST(size_in_bytes AS bigint))`) and whether optimize for ad hoc
      workloads is on.
   4. **Databases**: buffer pool by database (`sys.dm_os_buffer_descriptors` grouped by `database_id`
      only, `COUNT_BIG(*)`), largest first, GB and share, with a flag column (decision 6). Each row opens
      Database detail. Run only on a click of **Read the cache by database** (or **Refresh snapshot**).
3. **Database detail**, `/instances/:id/memory/databases/:db`: one pass over the buffer descriptors of that
   database, in that database's context (the note's query, page 12-13, is the reference and is known to
   work), joined to `sys.dm_db_partition_stats` (total size) and `sys.dm_db_index_usage_stats`. Per index:
   table, index, type, cached MB, total MB, % cached, seeks, scans, lookups, updates, last read,
   temperature (Hot: read in the last hour; Warm: last 24 h; Cold: not read in 24 h), key and INCLUDE
   columns. Above the table: the database's split into hot, warm and cold GB, with "cold is the number to
   trust: one seek marks a whole index as read". When the instance has been up less than 24 hours, say the
   temperatures are not reliable yet (usage stats reset at a restart). Top 50 indexes by cached MB, with
   the rest summed in one line. Run only on a click.
4. **Snapshots are saved and never deleted.** Migration **13** (and only 13; another session may add 14):
   `memory_snapshot (id, server_id, taken_at TIMESTAMP, kind VARCHAR /* 'instance' | 'database' */,
   db_name VARCHAR NULL, snapshot_json VARCHAR)`. The instance snapshot holds panels 2-4 (panel 4 only
   when it was read); the database snapshot holds decision 3's rows. Opening the screen shows the latest
   saved snapshot with its time ("Read 2 hours ago") and **Refresh snapshot**; nothing is read on page
   load except panels 2 and 3, which are instant. A list of earlier snapshots lets you open any one, and
   **Compare with** puts the chosen earlier snapshot's GB beside each database and clerk group, with the
   difference. That is how "is this new?" is answered in V1.
5. **Totals reconcile, and the gap is shown.** Databases must add up to the Data cache clerk, and clerk
   groups to the committed memory, within 5%. Outside that, show one line with both numbers and the gap
   ("Databases add up to 71.2 GB; the data cache clerk says 79.1 GB; 7.9 GB unaccounted"), never hide it.
   Every sum is bigint; every page count is `COUNT_BIG`; never join plans to sessions.
6. **Flags and the verdict.** Thresholds are starting points, named constants in `domain`, to be tuned
   against the 10 client databases already profiled.
   - Red (pressure): Windows available memory under 2 GB or `system_memory_state_desc` low;
     `process_physical_memory_low = 1`; Memory Grants Pending above 0.
   - Amber (worth a look): no cap (2147483647) or a cap above physical RAM; `value <> value_in_use` for
     max or min server memory (sp_configure without RECONFIGURE); lock memory above 1 GB; single-use ad
     hoc plans above 1 GB with optimize for ad hoc workloads off; tempdb above 2 GB and above 10% of the
     data cache; a database whose name looks like test, dev, UAT, QA, training, copy or old holding more
     than 1 GB of cache ("looks like a test database; does it belong on this instance?").
   - Database detail flags: mostly cached (90% or more of an index of 100 MB or more: "probably scanned");
     a nonclustered index 70% or more of its table's clustered size ("a wide INCLUDE list, close to a
     second copy"); two or more nonclustered indexes on one table sharing a leading key column; a heap
     with 100 MB or more in cache ("a heap is read by scanning").
   - The verdict line, one sentence: any red → "Under pressure: …" naming the red signals; else any
     amber → "Full but healthy, with things worth a look: …"; else "Full but healthy: SQL Server is
     caching data and nothing is short of memory." When SQL Server holds well under its target (for
     example after a restart), say "Still filling: started N hours ago" instead of "Full".
7. **Permissions first.** Check `VIEW SERVER STATE` (or `VIEW SERVER PERFORMANCE STATE` on 2022 and later)
   before reading; without it, say which right is missing and show nothing else. Database detail needs
   `VIEW DATABASE STATE` in that database; same rule.
8. **Slow reads are bounded and cancellable.** The two buffer-pool reads run with their own context
   (10-minute bound), the page shows the elapsed seconds and **Stop**, and Stop cancels the request so
   the statement ends on the server. The connection must not carry a socket deadline (see the
   go-mssqldb pitfalls in the code comments).
9. **Copy.** One **Copy** button gives the snapshot as plain text for a support ticket (the existing Copy
   hand-over pattern): verdict, at-a-glance numbers, clerk groups, top 10 databases, reconciliation line.

## Done when

- Unit tests for the verdict, flags, clerk grouping and reconciliation (pure `domain` functions).
- A live test (`SQLMONITOR_TEST_HOST`, `SQLMONITOR_TEST_5V=1`) reads panels 2-4 and one database detail on
  a local instance and checks the reconciliation within 5%.
- Checked live in the app against `localhost\SQL2022` (read-only use) and `.\SQLEXPRESS` if TCP is on.
- `go vet`, `go test ./...`, and the web build pass.

## Built (2026-11-12)

Decisions 1 to 9 are built. `go vet ./...`, `go test ./...` and the web build (`build.ps1`) pass.

**Where it is.** `domain/memory.go` (clerk groups, verdict, signals, reconciliation, the test-name rule, Copy
text; every threshold a named constant at the top) and `domain/memory_detail.go` (temperature, the index
flags, the top 50 and the rest); `collect/memory.go` (three reads: the instant overview, the buffer pool by
database, one database's cache); `store/memory.go` and migration 13; `service/memory.go`; `api/memory.go`
(`GET /api/servers/{id}/memory[?snapshot=N]`, `POST …/memory/snapshot`, `GET …/memory/databases/{db}[?snapshot=N]`,
`POST …/memory/databases/{db}/snapshot`); `screens/memory.tsx`, `screens/memory-database.tsx`,
`components/memory-parts.tsx`, `hooks/use-slow-read.ts`, `lib/memory-format.ts`. Links: a **Memory** button on
the instance page, and a **Memory** link beside INS02, INS03 and INS12 on the Setup card (the "Investigate"
place, next to *Do this*, rather than inside its text).

**Decided while building (left open above):**
- **Clerk memory** is `pages_kb + virtual_memory_committed_kb` per type. Pages alone fell 60% short of the
  committed memory on the test instance; with the virtual memory they come within 5%. `awe_allocated_kb` is
  left out (with Lock Pages in Memory it may count the same pages twice; not provable here, no instance has
  it on). The **Data cache** line is the buffer pool clerk's pages only (its virtual memory counts under
  Other), so it is the very number the databases reconcile to.
- **A sixth line, "Free inside SQL Server"**, from the Memory Manager's Free Memory counter: memory committed
  and not yet handed to any clerk. Without it the clerks missed the committed memory by about 5% on a quiet
  server (958 MB against 1,009 MB); with it they agree within 0.5% on both test instances. It is part of the
  check: clerk groups plus free against committed.
- **Query workspace** is the `MEMORYCLERK_SQLQUERYEXEC` clerk; the granted workspace counter is said beside it
  ("Granted to running queries now"), not added: grants are reserved inside memory already counted, and
  adding them would count it twice in the reconciliation.
- **The databases reconcile to the clerk's pages read in the same batch**, straight after the scan.
- **Snapshots.** Page load reads panels 2 and 3 and shows panel 4 from the latest instance snapshot ("Read 5
  minutes ago"); the verdict counts that snapshot's database flags too. Only a click on **Read the cache by
  database** / **Refresh snapshot** saves an instance snapshot, so every saved one holds panel 4 (the field
  stays optional). An instance snapshot keeps the **facts** and is built again when opened (one code path; the
  thresholds of the build that opens it). A database snapshot keeps Database detail **as built** (top 50 and
  the rest), because the facts of a large database run to thousands of indexes. A login without the right
  sees the sentence and nothing is saved. Lists show the latest 100.
- **Compare with** works on the instance screen (clerk groups and databases: "then 2 MB · +949 MB"). Database
  detail lists and opens its snapshots but has no compare in V1.
- **Still filling** is committed below 80% of the target (`MemoryFillingShare`), and never on Express (its
  target is out of reach under the edition's cap). A dev box with no cap and a small workload stays "still
  filling" for good; true, and harmless.
- **Express 2025** gets the sentence without the 1,410 MB figure (the figure is sourced for 2022 only).
- **min server memory** reads 0 configured and 16 MB in use by default: not treated as a pending change.
- **The test-name rule** looks for test, testing, dev, development, uat, qa, training, train, copy or old as
  a word of the name (split at non-letters and at lower-to-capital), or an all-capitals name ending in TEST,
  DEV, UAT or COPY. Only user databases over 1 GB are flagged.
- **Database detail flags**: "close to a second copy" needs the index to be 100 MB or more as well
  (`MemoryWideIndexMinMB`): without it every small table's indexes, system tables included, were "wide". The
  sentence says "a wide INCLUDE list" only when the index has INCLUDE columns. System base tables are listed
  (they are part of the database's cache) and judged by the same rules.
- **The per-index read** groups the buffer descriptors by allocation unit first and then joins the few
  thousand rows to the partitions, instead of the note's join on every buffer row; same result.
- **Stop** aborts the request; Go cancels the statement on the server through the request's context. Both
  slow reads have a 10-minute bound of their own. A stopped read says "Stopped. SQL Server ended the read and
  nothing was kept."
- No mock for the web's mock mode: the screens were checked against the live app only.

## Proven live (2026-11-12)

`TestPhase5vLive` (`SQLMONITOR_TEST_HOST`, `SQLMONITOR_TEST_5V=1`) passed on `localhost\SQL2022` (Developer) and
on `.\SQLEXPRESS` (2025 Express, TCP on). It makes a throwaway database (a 110 MB table with a wide index and a
second index on the same leading key, a 105 MB heap, all read into cache), then: panels 2 and 3 with the
clerks within 5% of the committed memory (983 MB against 980 MB); the cache by database within 5% of the data
cache clerk (335 MB against 336 MB) and kept as a snapshot; the screen reopened from it; Database detail with
every flag (probably scanned, a wide INCLUDE list, a shared leading key, a heap) and the temperatures; a read
cancelled mid-way returning the cancellation and keeping nothing. The database is dropped afterwards.

In the app (`kakapo.exe` against `localhost\SQL2022`): the screen, the verdict, the bar and the clerks;
**Read the cache by database** on an almost empty cache showed the gap line in amber ("Databases add up to
39 MB; the data cache clerk says 42 MB…"); after reading two tables of a client database it showed "within
5%" (973 MB against 974 MB); **Compare with** an earlier snapshot; Database detail of that database (949 MB,
937 hot, 12 cold, the two scanned tables flagged "probably scanned"); **Stop** during a read (nothing kept);
phone width with no horizontal scroll; the Memory links on the instance page and beside INS02.

**Not proven live:** a cache of client size (the 12.5 million row scan, and Stop ending a statement that has
run for minutes), Lock Pages in Memory (no instance has it), NUMA with more than one node, a login without
`VIEW SERVER STATE` (the permission sentences are unit-tested only), and any red signal (no instance here is
under pressure).
