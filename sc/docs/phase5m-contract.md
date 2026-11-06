# Phase 5m contract: Database decisions, with row versioning (READ COMMITTED SNAPSHOT) as the first

Some settings are not wrong or right on their own: they are decisions, made for one database after looking at
the client's workload, what SQL Server recorded and what the change costs, and reviewed later. READ COMMITTED
SNAPSHOT (RCSI) is the clearest one. Today kakapo has the pieces for it on three cards (ACT01, ACT02, BLK01,
DLK01, WRK06 on Performance; TMP03 on Storage; the `rcsi` fix behind the blocking page), and none of them says
whether this database should have it. This phase adds one level above the cards, **Database decisions**, and
puts the row versioning decision in it: an assessment from the evidence kakapo already keeps, the cost before
the change, the change itself through the existing previewed fix, and a record of what was decided, by whom,
why, and when to look again.

Agreed with Francois on 2026-11-06, after an outside advisor's review of a scope for an "RCSI assessment":
RCSI is recommended only from measured blocking between readers and writers, never from deadlocks or open
transactions alone; a transaction left open is fixed first; the headline number is the time writes and reads
spend waiting for each other per day; the decision sits at database level, not inside a card. The scope's
"advisory only, never enables RCSI" is met by kakapo's rules as they stand: nothing changes without a preview
and a click (SCOPE section 3 item 9, rule 3). Its "configurable thresholds" are not taken: the rules reuse
BLK01's and DLK01's thresholds (rule 2). Its "live DMV sampling" is not a source here: kakapo runs on demand,
and the history comes from event files and stored reports.

Nothing new is written to the server except through the `rcsi` fix that exists since 4c. No new collector,
no new Extended Events session: kakapo's own session (5e) is what makes the evidence complete, and the
decision says so when it is not installed.

**Sources.**
- Microsoft Learn: "Transaction locking and row versioning guide" (readers under READ COMMITTED SNAPSHOT take
  no S locks but still take Sch-S; versions kept in tempdb's version store; 14 bytes added to each row
  modified once versioning is on; the version store sized as about twice the version data generated per
  minute times the longest transaction in minutes; update conflicts, error 3960, are SNAPSHOT isolation only;
  setting the option needs no other connection in the database); "Accelerated database recovery" (with ADR
  the versions are kept in the database's persistent version store, not tempdb);
  "sys.dm_tran_version_store_space_usage"; "sys.dm_tran_persistent_version_store_stats";
  "sys.dm_tran_active_snapshot_database_transactions"; "blocked process threshold (server configuration
  option)"; "Use the system_health session". To be re-read when building: each sentence the app shows cites
  its page.
- DarlingData: sp_HumanEventsBlockViewer's "You Might Need RCSI" on blocked lock mode S or IS, and "You
  already enabled RCSI, but..." (`research/DarlingData-main/DarlingData-main/sp_HumanEvents/sp_HumanEventsBlockViewer.sql`
  3694-3729); it reads `lockMode`, `waitresource` and `isolationlevel` of both sides (2138-2146, 2284-2292).
  sp_PressureDetector: version store cleanup is held instance-wide by the oldest transaction touching
  row-versioned data (`sp_PressureDetector/sp_PressureDetector.sql` 2745-2811). sp_PerfCheck 7009: row
  versioning on with ADR off (`sp_PerfCheck/sp_PerfCheck.sql` 5185-5196). Erik Darling's isolation levels
  talk: RCSI ends reader/writer blocking and deadlocks, not foreign key or cascade locking (`Presentations/
  Isolation Levels/Isolation Levels.sql` 728-763); code that relies on readers being blocked is rare (queues,
  hand-rolled sequences) and keeps its behaviour with `READCOMMITTEDLOCK` (1446-1451, 875-887, 1709).
- kakapo: BLK01's thresholds (Medium from 2 episodes; High from 10, a wait of 60 s, or 10 sessions at once,
  `domain/blockinghistory.go`), DLK01's (Medium from 3 deadlocks of a pair, High from 10), ACT02's (Medium
  from 5 minutes, High from 30).

**Facts verified on `localhost\SQL2022` on 2026-11-06** (read only):
- `sys.databases` has `is_read_committed_snapshot_on`, `snapshot_isolation_state_desc` and
  `is_accelerated_database_recovery_on`; every user database there has all three off, master and msdb have
  snapshot isolation on.
- `sys.dm_tran_version_store_space_usage` returns a row per database; `sys.dm_tran_persistent_version_store_stats`
  returns `persistent_version_store_size_kb` per database.
- `Version Generation rate (KB/s)` is a cumulative counter (cntr_type 272696576) for the whole instance, and
  is not zero on an instance where no database has row versioning (triggers and online index builds also
  version). It cannot say what one database would generate before RCSI is on, so it is not used.
- The blocked process threshold is 0 (off) and kakapo's session is not installed: the blocking history there
  comes from system_health's five-minute snapshots only.
- system_health also records lock waits over 30 seconds as `wait_info` (predicate `duration > 30000 AND
  wait_type <= LCK_M_RX_X`). Not read in this phase (section 8).

## 1. Database decisions (`components/decisions.tsx`)

One card, full width, between the load strip (5i) and the six cards in `screens/database.tsx`. Heading
"Decisions for this database", and under it one row per decision. This phase has one row, **Row versioning
(READ COMMITTED SNAPSHOT)**. Phone width works.

A row, closed (the default), is one line: the decision's name, its **verdict** as a short tag, the verdict
sentence, and the record when there is one ("Kept off on 2026-11-06: waiting for the vendor; review by
2027-02-04", or "Review due" in amber once the date has passed). Opened, it shows, in this order:

1. **What kakapo found**: the evidence (section 3), with the window and the source of each line.
2. **Do first**: the findings that must be dealt with before the change (section 4), each with its own button.
3. **What it costs**: the measured values and what cannot be measured (section 5).
4. **The buttons**: the verdict's action (section 4), **Record a decision** (section 6), **Copy** (the
   whole row as a hand-over block, as 4a).

A decision is not a finding: it does not appear on a card, in the red flags, or in the cards' counts. The
findings it uses stay where they are; their Do this points to the decision (section 7). It still has a
catalogue entry, so its rule, sources and fix are kept the one way (rule 2): `DEC01`, card `decisions` (a new
`CardID` that no card lists), "Row versioning (READ COMMITTED SNAPSHOT)", question "Should this database
let readers use row versions instead of waiting for writers?", owner Us, then check "After a few working
days: the row says On and TMP03 is not raised; the tuning report's lock threads waiting went down."

## 2. Reading readers and writers apart (`domain/blockinghistory.go`, `domain/deadlock.go`)

**Blocking.** Every blocked process report BLK01 already reads (kakapo's session files first, system_health
for the days before) is put in one class, from the waiting side's lock mode and isolation level and the
blocking side's statement and isolation level, all of which collect already reads:

| Class | When | RCSI |
|---|---|---|
| Reads waiting for writes | waiting side wants `S` or `IS`, at "read committed (2)" | removes it |
| Writes waiting for reads | waiting side wants `U`, `X`, `IX`, `SIX`, `UIX`; blocking side at "read committed (2)" and its statement is a SELECT (the input buffer's first keyword after comments, `WITH` and a parameter list) | removes it |
| Readers that keep their locks | either side at REPEATABLE READ or SERIALIZABLE, or a `RangeS-*` mode | does not (BLK01 already names it: ask the vendor for READ COMMITTED) |
| Schema locks | `Sch-S` or `Sch-M` wanted | does not |
| Writes waiting for writes | everything else with both sides known | does not |
| Unknown | a head-only report (system_health), or a blocking statement kakapo cannot read (a procedure call: "Proc [Database Id = …]") | not counted either way |

A wait is one **episode**: one waiting session (spid and its `lastbatchstarted`) behind one blocker
transaction; its length is the longest `waittime` reported for it. kakapo's session reports a wait at every
threshold interval while it lasts, so its length is right to within the threshold; a system_health snapshot
sees a wait once every five minutes, so its lengths are "at least".

`BlockingHistory` gains `classes`: per class, the episodes, the total and longest wait, the server days with
one, and per server day the total wait; and for the two classes RCSI removes, the three programs that wait
most and the three that block most (program, login, host as the report wrote them). Each `BlockingLeader`
gains its episodes per class. Stored in `blocking_report.report_json` as today; no new table. A report stored
before 5m has no `classes`: the decision says "Read the blocking history again to tell readers from writers."

**Deadlocks.** `DeadlockProcess` gains `Isolation` (the process's `isolationlevel` attribute in the deadlock
graph). A deadlock is **with a reader** when a process wants `S` or `IS` at read committed: the kind RCSI
removes. `DeadlockReport` gains, per pair, `withReader`, and the totals of each kind. Stored in
`deadlock_report.pairs_json` as today.

Kinds are named by what waits on what, never by the program: kakapo does not keep a list of reporting tools
(rule 4). The programs are evidence, so the consultant sees "Report Server" or a BI tool's driver by name.

## 3. The evidence and the verdict (`domain/decision_rcsi.go`, pure)

`BuildRowVersioningDecision(in RowVersioningInputs) Decision` from: the settings and the cost read (section 5),
the last stored blocking history and deadlock report for this database, the Performance card's ACT02 and the
Storage card's TMP03 from the overview, and the decision record (section 6).

**What kakapo found**, each a line with its window ("last 7 days, kakapo's event session" / "system_health
snapshots, every five minutes"):
- **Writes waiting for reads**: "{h} h {m} min a day on average, on {d} of {n} days; longest {s}" (the
  headline, first, because it is the operational work that waits), with the programs.
- **Reads waiting for writes**: the same line.
- **Not removed by RCSI**: writes waiting for writes, locking readers, schema locks, each as a count and a
  total, one line.
- **Deadlocks with a reader**: "{k} of {t} deadlocks in {n} days had a reader in them", and the pair with the
  most.
- **Evidence**: which source covered which days, the blocked process threshold, and "unknown" as a count.

**The verdict**, the first that applies:

| Verdict | When | Sentence (example) | Action |
|---|---|---|---|
| **On** | RCSI is on | "Readers do not wait for writers here. What still blocks is writers against writers or a lock hint." + the version store line (section 5) | Investigate: TMP03 when it is raised; else none |
| **Fix first** | RCSI is off and section 4 has an item | "Before deciding on row versioning, {the first item}." | the item's own button |
| **Not enough evidence** | RCSI is off and: no blocking history read in the last 7 days, or the stored one has no `classes`, or only system_health covered the window and nothing reached Worth considering | "kakapo cannot tell yet whether readers and writers wait for each other here: {what is missing}." | Apply `install-xe-session` (5e) when it is not installed; Read blocking history when it is |
| **Worth considering** | RCSI is off and the two classes RCSI removes together reach BLK01's Medium (2 episodes or more), or deadlocks with a reader reach DLK01's Medium (3 or more of a pair) | "Writes waited {t} for reads and reads {t2} for writes over {n} days; READ COMMITTED SNAPSHOT would remove those waits." | Apply `rcsi` (section 4) |
| **Not needed now** | RCSI is off, the window is covered by kakapo's session (or system_health with nothing found), and neither class reaches Medium | "Readers and writers did not wait for each other enough to change this: {counts}." | none |

**Worth considering** is "strongly" (the tag in orange, not amber) when either class reaches BLK01's High (10
episodes, a 60-second wait, or 10 sessions at once) or deadlocks with a reader reach DLK01's High (10 of a
pair). With only system_health behind it, it stays amber and adds "kakapo's event session would show every
wait over 5 seconds" with Apply `install-xe-session`. Writes waiting for writes and deadlocks without a
reader never raise it: they are named under "Not removed by RCSI", and BLK01 and DLK01 keep their own
findings.

## 4. Do first, and the `rcsi` fix

**Do first** lists, in this order, what must be dealt with before the change:
1. **A transaction left open in this database** for 30 minutes or more (ACT02's High): with row versioning
   on, it would keep every version made since it began, so tempdb would grow for as long as it stays open;
   and the change's `WITH ROLLBACK IMMEDIATE` would roll it back, which ACT02's Do this says not to do
   without knowing what it is. Button: ACT02's Investigate.
2. **TempDB cannot hold more** (TMP03 raised with "cannot grow" or "the disk has only…"): Button: TMP03's.

The `rcsi` fix (`domain/blocking_fix.go`) keeps its statement, rollback and read-back. Its preview gains:
- **The evidence line** of the decision, first ("Writes waited 1 h 10 min a day for reads over 7 days…"), or
  "kakapo has no evidence that readers and writers wait for each other here" when the verdict is not Worth
  considering.
- **The cost lines** of section 5.
- **Apply held back** while Do first item 1 holds: `FixPreview` gains `Held` ("Apply is held back: session
  {n} ({program} on {host}) has had a transaction open in {db} for {m} minutes. Deal with it first (ACT02)."),
  and the dialog shows it in place of Apply, the way a room line that does not fit does (5l). Copy script
  still works: the consultant may run it by hand, knowingly.
- Warnings, added to the four of 4c: "Each row changed after this grows by 14 bytes; tables whose pages are
  full will split pages for a while." "Readers that use NOLOCK or READ UNCOMMITTED read as before; readers
  with REPEATABLE READ, SERIALIZABLE or lock hints keep locking." With ADR on: "Row versions are kept in this
  database's own file (ADR), not in tempdb."
- `FixDef` for `rcsi` moves from check ACT01 to the decision (`DEC01`), so the change log row and the tuning
  report link to it. Applying writes a decision record "Turned on" with the change id (section 6).

## 5. What it costs (`collect/rowversioning.go`)

One small read, SELECTs only, tagged, each area in its own TRY, run with the overview:

| Area | Read |
|---|---|
| Settings | `is_read_committed_snapshot_on`, `snapshot_isolation_state_desc`, `is_accelerated_database_recovery_on` (`COL_LENGTH` guard: 2019+), `compatibility_level` |
| Version store | tempdb's version store MB (as TMP03), this database's share from `sys.dm_tran_version_store_space_usage` (`OBJECT_ID` guard) |
| PVS | `persistent_version_store_size_kb` for this database (2019+, when ADR is on) |
| Open transactions | the oldest transaction open in this database: minutes, session, status, program, host (ACT02's query, its database-filtered half, moved into one fragment both use) |
| Sessions | the other sessions in the database now (the `rcsi` fix's count) |

TempDB's size, free space and whether it can grow, and the drive's free space, come from the Storage card's
facts and 5l's room; nothing is read twice.

The section shows **measured** values with their time, then one line for what is not measured: "How much
version data this database would write cannot be read before it is on. Microsoft sizes the version store as
about twice the version data written per minute times the longest transaction in minutes; the longest here
now is {m} minutes. After the change, TMP03 shows what the version store holds: look again after a few
working days." Snapshot isolation on (without RCSI) and ADR are shown as facts with their sentence:
snapshot isolation lets code ask for versions and costs versions only when it does; ADR moves the versions
into this database and needs its free space (STO01, STO04) instead of tempdb's.

## 6. The decision record (`store/decision.go`, `components/decision-dialog.tsx`)

New table, migration 12:

```sql
CREATE SEQUENCE IF NOT EXISTS seq_decision START 1;
CREATE TABLE IF NOT EXISTS decision (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_decision'),
  server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, decision VARCHAR NOT NULL, -- 'row-versioning'
  decided_at TIMESTAMP NOT NULL, decided_by VARCHAR NOT NULL,                       -- the Windows user
  choice VARCHAR NOT NULL,                                                           -- keep, change, later
  verdict VARCHAR NOT NULL, reason VARCHAR, review_on DATE, change_id INTEGER
);
```

**Record a decision** opens a small dialog: the choice (**Keep it as it is** / **Decide later**; **Change it**
is the fix's Apply, which records itself), the reason (required for Keep), and **Review by** (default 90 days
from today, under "More options" with the date picker, rule 5). The verdict at that moment is stored with
it, so a later reader sees what the decision was made on. The row shows the latest record; the history of
records is in the row's opened view, newest first. A record is never edited: a new one replaces it.
`GET/POST /api/servers/{id}/databases/{db}/decisions`.

## 7. Joining up the cards

- ACT01's, QRY01's and BLK01's "or READ COMMITTED SNAPSHOT for the database (decide first: it uses TempDB
  for row versions)" become "or READ COMMITTED SNAPSHOT: see Decisions at the top of the page"
  (`performance.go` `act01RCSIOff`, `qry01RCSIOff`; `blockinghistory.go` `blk01DoIndex`). The "already has
  READ COMMITTED SNAPSHOT" branch stays.
- The blocking page's "Turn on READ COMMITTED SNAPSHOT below" opens the same preview (section 4).
- BLK01's panel gains one line under its summary: "Waits RCSI would remove: {t} ({writes} writes behind
  reads, {reads} reads behind writes). Waits it would not: {t2}."
- WRK01's lock Do this drops "the Design card's lock waits per table" (there is none since 2026-11-03) for
  "BLK01 shows where it has been all week".
- `Overview` gains `decisions` (built from what the overview already reads, the stored reports and the
  section 5 read).

## 8. Not in this phase

Other decisions (compatibility level DBC06, recovery model DBC08, Query Store DBC10, snapshot isolation on
its own) keep their checks on the Setup and Backups cards; the Decisions card is built so each can move into
it later as a row. A list of every database's row versioning verdict on the instance page. system_health's
`wait_info` lock waits and Query Store's lock waits per statement as further evidence. An estimate of
version volume from write counts. Optimized locking (SQL Server 2025). A reporting replica: when reads
dominate, that is a Copy line for the client's IT, not a fix.

## Tests

Domain: each class from fixture reports (S at read committed; S at repeatable read; RangeS-S; Sch-S; U behind
a SELECT at read committed; U behind a SELECT at serializable; X behind an UPDATE; a head-only report; a
procedure call as blocker); episodes from kakapo's repeated passes and from system_health snapshots; per-day
totals across midnight on the server clock; programs ranked. Deadlocks with and without a reader. Every
verdict and its precedence: On; Fix first (ACT02 at 30 minutes in this database; at 29 not; in another
database not); Not enough evidence (no history, history older than 7 days, history without classes,
system_health only with nothing); Worth considering at 2 episodes, strongly at 10 / 60 s / 10 sessions, by
deadlocks with a reader at 3 and 10; writes behind writes alone never; Not needed now. The rcsi preview: the
evidence line first, `Held` with an open transaction, the new warnings, ADR's line. Store: migration 12, a
record per choice, latest first. Collect: tagged, read-only, each area in its TRY, the 2019 and `OBJECT_ID`
guards. API: decisions GET and POST (Keep without a reason is 400). Web build.

## Done when

`go test ./...` and the web build pass. Live on `localhost\SQL2022`, in a scratch database (dropped
afterwards):
- **Without kakapo's session**: the row says Not enough evidence, with Apply "Keep every block and deadlock
  on the server" (5e).
- **With the session installed** (threshold 5 s): session A holds an UPDATE open on a row; session B selects
  it for over 10 seconds. After Read blocking history, the row is Worth considering by the second such wait,
  with "reads waiting for writes" and the two programs; the BLK01 line shows the split.
- **Writers only**: two sessions update the same row in turn, over 10 seconds each, twice: the row stays Not
  needed now and the waits show under "Not removed by RCSI".
- **A locking reader**: session A at REPEATABLE READ selects a row in an open transaction; session B updates
  it: counted as "readers that keep their locks", not as removable.
- **A deadlock with a reader** (two sessions each update one table and then select the other's row): DLK01's
  pair is marked with a reader.
- **An open transaction**: a session holds a transaction open in the database; at 5 minutes the preview shows
  it first; with the fixture at 30 minutes, Apply is held back and Copy script still works.
- **Apply**: RCSI on, read back, a change row and a decision record "Turned on"; the row says On with the
  version store line. Rollback turns it off again.
- **Record a decision**: Keep with a reason and the default review date; the closed row shows it; a record
  with a past review date shows "Review due".
- Phone width: the card and its opened row fit without horizontal scroll.
