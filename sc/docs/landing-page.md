# The instance page: what you see when you open a SQL Server

Draft for agreement, 2026-11-03. Mockup: `docs/mockups/instance-overview.html`.

## The idea

A consultant who is not a DBA opens an instance and gets one page that answers, in plain words, the six questions
they would ask a DBA. Each question is a **card**. A card shows a colour, one verdict sentence with the number
that proves it, three or four facts, and how many things need doing. Opening a card shows the findings behind it
in the Granite layout (Start here, For the client's IT, everything else) with **Do this** and, where the app can
do it, an **Apply** button that previews first (the fix pattern from phase 1).

**The page is for one database** (revised 2026-11-03). A consultant works on one database at a time, as the
triage and the tuning report do, and never manages twelve at once. Cards that are really about the instance
(memory, parallelism, SQL Agent, waits, tempdb, drives) still appear, as instance facts inside the database's
cards, because the person reading the page does not care which level a fact lives at; they care whether their
database is in good shape.

## Navigation

```
Home                    Instance                 Database page              Card page
one tile per instance   pick a database          header + six cards         the findings for this
(name, host, colour)    (skipped when the        + recent changes           database: Start here /
                        registration names one)  for this database          Client IT / rest, Do this, Apply
```

Breadcrumb: `Instances › Client A production › GraniteLive › Setup and configuration`. The database crumb opens
the picker again ("change database"). The database picked is remembered on the server registration, so the next
time the instance opens straight into it. Four levels, no deeper. A finding's Do this is the end of the road.

## The six cards

| Card | The question it answers | What sits behind it (from `Checks.csv` and the references) | Facts on the card |
|---|---|---|---|
| **Backups and data safety** | Could you lose data? | BAK01 to BAK05 (full and log backups, CHECKDB, suspect pages, worst-case data loss), DBC08 (recovery model). Lite: Database Protection. | Worst-case data loss ("up to 26 hours"), last full backup, log backups yes/no, last CHECKDB, damaged pages |
| **Setup and configuration** | Is it set up the way it should be? | INS01 to INS13 (version and update, memory, parallelism, plan cache, instant file init, lock pages, power plan), DBC01 to DBC11 (auto close/shrink, page verify, statistics, compatibility, owner, Query Store, TRUSTWORTHY), CFG01/02, CPY01 to CPY06 (leftovers from a copied database). Lite: Configuration. **Phase 1's Query Store card lives here.** | Build and update state, memory cap, parallelism, Query Store on N of M databases, compatibility level, copied-database leftovers |
| **Storage and space** | Will it run out of room? | STO01 to STO09 (free disk, autogrowth, file limits, drives, log size, VLFs, latency), TMP01 to TMP03, IDX01/IDX07 (where the space goes), IDX10 (log tables), IDX13 (identity range). Lite: Database Sizes, Storage Growth, tempdb. | Free space on the tightest drive, biggest database and its growth, log tables without cleanup, tempdb fill, disk latency |
| **Upkeep** | Is someone looking after it? | MNT01 to MNT07 (Agent running, failed jobs, index and statistics maintenance, stale statistics, shrinks and cache clears, crashes and severe errors, alerts and operator). Lite: Job History, Agent Alerts. | Agent state, failed jobs in 7 days, maintenance job yes/no, stale statistics on big tables, who gets told about errors |
| **Performance** | Is it slow, and why? | WRK01 to WRK07 (waits in plain English, CPU pressure, missing indexes, top queries, poison waits, deadlocks per day, open transactions), ACT01 to ACT03 (blocking now, open transactions, costliest queries), SRV03/04/06/07. Later: the 30-second live sample (DESIGN.md 5.3) and the tuning report. Lite: Overview, Wait Stats, Queries, Blocking. | Biggest wait and what it means, CPU pressure, blocking right now, deadlocks per day, top query by CPU |
| **Database design** | Is the database built well? | IDX02 to IDX06, IDX08, IDX09, IDX11, IDX12, IDX14 (over-indexed, unused, duplicates, heaps, lock waits per table, forwarded records, unindexed foreign keys), COD01 to COD03 (Granite PreScripts and views). Per database. Lite: Index Analysis. | Over-indexed tables, unused indexes writing for nothing, heaps, the table everyone waits on, PreScripts over the read budget |

Below the cards, one slim panel: **Recent changes**, from the `change` table: what was changed, when, by which fix,
and (later, from the tuning report) whether it helped. This is the "prove it" pillar of the toolkit and it must
be visible on the landing page, but it is history, not a question, so it is not a card.

## Card anatomy

```
+--------------------------------------------------------------+
| Backups and data safety                     o Needs attention |   colour: green / amber / red / grey
| Could you lose data?                                          |
|                                                               |
| You could lose up to 26 hours of data on GraniteLive.         |   verdict: one sentence, one number
|                                                               |
| Worst-case data loss     26 h       Last CHECKDB    9 days    |   three or four facts
| Last full backup         02:00      Damaged pages   none      |
|                                                               |
| 2 to do, 1 for the client's IT      as of 09:21   Open  >     |   count, freshness, link
+--------------------------------------------------------------+
```

Colours mean one thing everywhere: **red** act today (Critical), **amber** something to do (High or Medium),
**green** no problem found, **grey** not checked yet or could not be read. The words next to the dot say which.

States every card has: not checked yet (grey, "Run checks"), checking (spinner on the card, the others usable),
checked (colour), could not read (grey with the reason, e.g. permissions).

## How a card gets its data

Every check in `Checks.csv` gets a **Card** column. A card is the sum of its checks: the worst severity gives the
colour, the top-ranked finding gives the verdict sentence, named checks give the facts. Nothing on the page is
computed in the UI; the backend serves each card as one JSON object (`title, question, status, verdict, facts[],
counts, asOf, sinceRestart`). The same object is what a future MCP tool or assistant reads.

The scripts in `External References/granite-sql` are the source of the checks, taken over one card at a time,
not run whole. That is the careful build-out: each card becomes a phase, built with its reference script and
README open, and only what the card needs.

## Header

Database name first, then the instance: host, edition and build, uptime as "figures since restart, 5 days ago"
(because waits and counters reset then), **Refresh** (re-reads everything that is quick) and **Run checks**
(runs the slow ones). The log of what ran and how long it took is one click away for when a card is grey.

## The database picker

The instance page lists the user databases, one line each: name, state, size (later), and a colour once the
cards exist. Pick one and the page remembers it. With a database on the registration, this page is skipped and
reachable from the breadcrumb.

## Out of this page, on purpose

Charts (they belong on the Performance card's page), raw grids, anything that needs a DBA to read. The home
page stays one tile per instance with the same colour and one line, like Lite's Overview.

## Phases from here

| Phase | What | Done when |
|---|---|---|
| **2. Skeleton** | Home tiles, the instance page with six cards and the header, the card page frame, Recent changes. All cards grey except Setup, which shows the Query Store state from phase 1 as its first fact and its findings. | You open your instance and the page looks like the mockup with real header data and a working Setup card. |
| **3. One card at a time** | Backups, then Setup in full, then Storage, Upkeep, Performance, Design. Each with its checks from the reference scripts, its facts, its Do this, its fixes. | Each card: a DBA agrees with its verdict on a real site. |
| **4. Right now and did it help** | The live sample on the Performance card; the tuning report on Recent changes. | As in SCOPE.md. |
