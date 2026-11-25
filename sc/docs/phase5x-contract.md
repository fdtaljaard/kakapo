# Phase 5x contract: a navigation tree on the left

Decided on 2026-11-25 with Francois. kakapo's screens are reached today through cards: Home tiles, the database
page's six cards, and buttons on a card's page that lead further (Tuning report, Indexes worth reorganising,
Memory, What changed?). This phase adds a navigation panel on the left: a tree, in more than one arrangement,
that the user can switch between. This contract records the idea and the arrangements, and specifies the first
one in full. The pages themselves do not change.

## Why

It is still hard for a novice to find their way. The questions they ask are **what needs my attention, and why?**
and **what do I need to do?** The cards answer them one database at a time, but what sits behind a card is only
found by opening it, and some topics sink out of sight. The maintenance plan is the example: a database needs a
good one, yet it is a row near the bottom of the Upkeep card's page and is easily overlooked.

Many novices already know SQL Server Management Studio, and with it a picture of how SQL Server fits together:
SQL Server Agent and its jobs sit outside the databases, a database holds tables, tables hold indexes. kakapo's
topics live at those same levels, even where a card mixes them. A tree on the left lets the user see every topic
at once, see which ones need something, and see where each one sits.

The point is not the navigation itself but the view of SQL Server as a whole: what there is, where it lives,
and what needs looking at.

## What it is not

- **Not a copy of SSMS's Object Explorer.** No list of every table, index, login or job. The tree holds kakapo's
  topics, arranged in a way someone who knows SQL Server will recognise. A node exists because there is
  something to read or do there.
- **Not a new set of pages.** Every node opens a page or a part of a page that exists today. Routes stay as
  they are (`lib/paths.ts`), and so do the breadcrumbs.
- **Not a dashboard.** The tree carries the One Meaning colours as small dots and nothing else (DESIGN.md).

## The panel

- On every page under an instance (`/instances/:id/...`), a panel on the left holds the tree, and the page's
  column sits to its right. Home and the setup screen have no panel. The panel is the same tree on every page,
  so moving between pages never rebuilds it or loses what is expanded.
- **The tree's root is the instance**, under it the database being worked on. On an instance page with no
  database in the address (Memory, What changed?, Edit, the picker), the database is the one the registration
  remembers. With none, the instance shows its own topics and "Pick a database".
- **Two targets per row.** The chevron expands or collapses a node; the label opens its page. A category is a
  link to its page and a folder at the same time.
- **Status dots** come from what the database page's read already holds: a card's colour and its "to do" count,
  and the maintenance plan's colour. A node with no status of its own has no dot. Grey keeps its meaning, "not
  checked".
- **Why, under the label** (review of 2026-11-25). A row that needs something says why in a quiet line under its
  label, at most two lines: a red or amber category its card's verdict sentence, the Maintenance plan row its
  own few words ("2 pieces missing, 1 failing"). A green row has none, so a healthy tree stays short.
- **The current page is marked** (`aria-current="page"`), and its category opens by itself.
- **What is expanded is remembered** on this computer (browser local storage), per arrangement.
- **Narrow screens.** Below the medium breakpoint the panel folds into a "Navigate" control above the page; the
  tree opens under it and closes again after a click.
- **Accessibility.** A nested list inside `<nav aria-label="kakapo navigation">`, each folder's chevron a
  button with `aria-expanded`, each label a link. Keyboard: Tab through links and chevrons; Enter opens.

## One reading of the database

Today the database page reads the overview live on every visit, and a card's page only borrows a cached one.
With a tree on every page, one owner reads it: the instance's layout reads the overview once when a database is
opened (from its address), and every page under it uses that one read. Refresh on the database page reads it
again. A card's detail carries the same card object, so when a card page reads its detail the tree takes that
card's new colour from it, without a second overview read. The overview also carries the maintenance plan's
colour and few words (`maintenancePlan`: the same read the Upkeep card's row makes, summarised as that row counts
it), and the Upkeep card's detail carries the same summary, so the plan's row follows a Create there. Opening Memory or What changed? with a remembered
database shows the last overview read in this window, if there is one, and never reads one only to colour the
tree.

## Topics, and arrangements over them

Each topic is defined once: its id, its label, where it opens, the level it lives at in SQL Server (instance or
database) and the card it belongs to. An arrangement is a grouping over those topics. Adding the second
arrangement is a new grouping, not a new tree. The list of topics is `web/src/lib/nav-topics.ts`.

Where a topic is part of a card's page rather than a page of its own, it opens the card's page at that part
(`/cards/upkeep#maintenance-plan`). The card's page reads its detail first, so it scrolls to the part once the
detail is on screen.

### Arrangement 1: by responsibility (built in this phase)

The six cards are the categories, in the database page's order, with their topics under them. A category opens
the card's page as it is today.

```
Instance name                                  instance page
  Database name                                database page (the answer, the load strip, the cards)
    ● Backups and data safety          2       card page
        Backup schedule                        card page, the schedule row
        CHECKDB from kakapo                    card page, the last run and Run CHECKDB now
    ● Setup and configuration                  card page
        Query Store                            card page, the Query Store row
        Memory                                 /memory (instance)
    ● Storage and space                        card page
        Drives                                 card page, one bar per drive
        TempDB files                           card page, the TempDB row
        Tables kakapo archives                 card page, the Tables row
        Room for CHECKDB                       card page, the room check
    ● Upkeep                           3       card page
        Indexes worth reorganising             /indexes
    ● Maintenance plan                         card page, the Maintenance plan row (its own dot)
      2 pieces missing, 1 failing              why, in Quiet Ink
    ● Performance                              card page
        Better or worse than usual             card page, the trend
        Tuning report                          /tuning
        Blocking right now                     /blocking
        Live sample                            card page, the 30-second sample
        Deadlocks                              card page, the deadlock report
        Blocking history                       card page, the week of blocking
    ● Database design                          card page
        Foreign keys without an index          card page, the fold
        Tables without a primary key           card page, the fold
    Decisions                                  database page, the Decisions card
    Recent changes                             database page, Recent changes
  What changed?                                /changes (instance)
```

The number after a category is its "to do" count, in Quiet Ink; none when there is nothing to do. Under a red or
amber category, its verdict sentence in Quiet Ink.

**The Maintenance plan is a row of its own**, right after Upkeep and not inside it (review of 2026-11-25): a
database needs a good one, and as a topic under a collapsed Upkeep it was two clicks away and easy to miss. It is
the one topic with a status of its own in this arrangement, from the overview.

There is no arrangement switch in this phase: a control with one choice is not a choice. It comes with
arrangement 2.

### Arrangement 2: as SQL Server is built (intent, not yet specified)

The same topics, placed where SQL Server keeps them, so the tree also teaches the shape of SQL Server. A
similar idea to SSMS, not a copy of it. The levels below are taken from what each topic reads today:

| Level | Topics |
|---|---|
| **Instance** | Server settings (INS: version and updates, memory, parallelism, power plan), Memory, TempDB files, Drives, Installed helpers (the event session, the change log, the maintenance procedure), What changed? |
| **Instance › SQL Server Agent** (or Windows Task Scheduler on Express) | **Maintenance plan**, as a topic of its own and not under a card: backups, integrity check, statistics, index work, history cleanup. Failed jobs (MNT02), alerts and operator (MNT07). |
| **Database** | Backups and CHECKDB, files and log (STO), Query Store, database options (DBC), Decisions, Recent changes |
| **Database › Tables** | Tables kakapo archives, tables without a primary key, foreign keys without an index |
| **Database › Indexes** | Indexes worth reorganising, duplicate indexes |
| **Database › Queries and activity** | Costliest queries and the query page, Tuning report, Blocking right now, Blocking history, Deadlocks, Live sample |

Status in this arrangement cannot come from a card's colour alone, because a card mixes levels. Before it is
built, each topic needs its own status from the checks behind it (which checks belong to which topic). That is
the first decision of the phase that builds it.

### The switch (with arrangement 2)

A small control at the top of the panel, "By responsibility" and "As SQL Server is built". The choice is
remembered on this computer. Both arrangements open the same pages, so switching never moves the page the user
is on.

## Open

- **The six-card grid on the database page.** With the tree on the left, the cards repeat its first level. They
  stay in this phase: the Page Answer, the load strip, the decisions and Recent changes stay whatever is decided.
  Whether the grid gives way to something smaller is Francois's call once he has used the tree.

## Done when

- Every page under an instance shows the panel with arrangement 1, and moving between those pages keeps it
  without a flicker or a reset of what is expanded.
- Each category opens its card's page; each topic opens its page, or its card's page scrolled to its part.
- A database open costs exactly one overview read (the network tab shows one `/overview` request); a card page
  opened directly from its address reads the overview once, through the layout, and its own detail.
- After an Apply on a card's page, the category's dot follows the card's new status.
- The maintenance plan is one click from every page under the instance, with its own dot and, when it is not
  green, what is wrong.
- A red or amber category says why under its label.
- Below the medium breakpoint the panel folds into "Navigate"; light and dark both work; no shadows.
