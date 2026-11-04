# The knowledge base: a proposal for discussion (2026-11-04)

Item 1 of `docs/architecture-review.md`. Rule 2 of SCOPE section 1a says every recommendation rests on a
fact and a source, kept as one body that the app and any AI share. This is what that body looks like, how the
code uses it, and what it deliberately is not.

## One record per check

Everything kakapo says about a check lives in one record. The record is data, embedded in the exe, one file
per card (`internal/knowledge/storage.yaml` and so on), loaded once at start. TMP02 as it would read:

```yaml
- id: TMP02
  card: storage
  title: TempDB data files equal in size and growth
  question: Are TempDB files the same size and growth?
  owner: us
  rule: >
    Every TempDB data file should have the same size and the same fixed growth in MB. SQL Server spreads
    work over the files in proportion to their free space, so unequal files are used unevenly and the
    allocation contention the extra files were meant to remove comes back. TempDB that grew 20 or more
    times in a week starts too small for its workload.
  thresholds:
    growths_per_week_low: 20
    growths_per_week_medium: 100
  why: >
    Each growth pauses whoever needs the space. TempDB is recreated at its configured size on every
    restart, so a size it grows to every day should be its starting size.
  do:
    default: >
      Apply "Set up the TempDB files": it makes the size TempDB has grown to the starting size of every file
      and gives each a fixed growth, online and at once.
    unequal_only: >
      Make the data files the same size and growth. Apply does it in one step; the preview shows each file.
  then_check: Re-run after the change: TMP02 passed and no growth events in the next week.
  fixes: [tempdb-files, autogrowth]
  related: [TMP01, TMP03, STO02]
  sources:
    - kind: microsoft
      title: tempdb database
      url: https://learn.microsoft.com/sql/relational-databases/databases/tempdb-database
      note: equal file sizes and the proportional fill algorithm
    - kind: practitioner
      title: Brent Ozar, TempDB data files
      url: https://www.brentozar.com/blitz/tempdb-data-files/
    - kind: script
      title: sp_PerfCheck (DarlingData, MIT)
      where: sp_PerfCheck.sql 2446
      note: the PAGELATCH share of uptime used by TMP01
  glossary: [tempdb, autogrowth, allocation contention]
```

A fix has a record too (`id`, `title`, what it changes, its inputs and their help, the warnings' texts, the
permission it needs, sources), and a glossary term has one (`term`, `plain`, `more`).

## What the code does with it

- **`domain` keeps the judgement, the record supplies the words.** The thresholds stay in Go, where they are
  unit-tested; the record repeats them under `thresholds`, and a test asserts the two agree, so the number a
  reader sees is the number the code uses. A finding is built as today, but `why`, `do` and `then_check` come
  from `knowledge.Check("TMP02")` instead of a constant, with a chosen variant (`do.unequal_only`) and the
  numbers filled in by the domain (`{n}` placeholders, never arithmetic in the record).
- **One loader, one lookup.** `internal/knowledge`: load the embedded files at start, validate once, expose
  `Check(id)`, `Fix(id)`, `Term(name)`, `All()`. No templating language beyond `{name}` placeholders; no logic
  in the data.
- **A test that keeps it honest.** Every check id in the catalogue has a record, every record has at least one
  source, every `do` variant the domain asks for exists, every fix in `FixDefs` has a record, every cross
  reference points at a real id. The build fails otherwise.
- **The same records render four things:** the finding texts; the glossary page (today missing); the hand-over
  blocks and the Copy-all document (sources attached, so what the consultant sends to IT carries its
  backing); the AI reference.

## What it changes for the user

Nothing on the cards except that a do-this never says "see INS04" again: cross references render as the
related check's title in words ("see Max degree of parallelism on the Setup card"). Copy blocks gain a short
"Based on" line with the sources. A glossary becomes possible without writing it twice.

## What it gives the AI

The AI reference is the knowledge base exported as it is: the YAML files plus a generated `SKILL.md` that
explains the cards, the finding model and the three endings. The same export packages as an agent skill for
Claude Code or as the system context of an in-app assistant. There is no second copy to keep in step; a
release of kakapo is a release of its knowledge.

## Migration, card by card

Each card is one step an agent can do and I can verify: move its constants into records, point the domain at
the loader, run the card's tests (the texts are asserted there, so the tests prove the move changed nothing),
then the builder unification for that card (review item 8) in the same step while its file is open. Order:
Storage (the tempdb work is fresh), Backups, Upkeep, Setup, Performance, then the fixes and the glossary.
About half a day of agent work per card.

## Decisions for you

1. **Format.** YAML (readable, comments allowed, what the example uses) or JSON (no extra dependency; Go
   reads it natively; less pleasant to edit). I lean YAML with one small, well-known library.
2. **Where the thresholds live.** In Go with the record repeating them (my proposal, numbers tested where they
   are used), or only in the record with the domain reading them (one place, but logic starts to drift into
   data). I would keep them in Go.
3. **Sources policy.** Microsoft documentation is the authority; practitioners (Ozar, Darling, Hallengren,
   the original toolkit) for practice; a check with no Microsoft or practitioner source gets `kind: ours` with a
   sentence of reasoning, and the test accepts that. Agree?
4. **Scope of the first pass.** Checks and fixes only, or the glossary too? I would include the glossary from
   the start: the terms are already in the texts and collecting them while moving is cheap.
5. **No application-specific checks.** kakapo is generic (SCOPE rule 4): a check that only makes sense for one
   application is made generic or left out; the Granite log-table check (IDX10) is the one to redo.

## What it is not

Not a rules engine: no conditions, no expressions, no scoring in the data. Not a translation system. Not a
place for SQL. Not editable from the UI in version 1. A record is a page of words with its sources; the Go
code decides, the record explains.

## Revision 2 (2026-11-04, after discussion): kinds of knowledge, and portability

Two points from the discussion change the proposal.

**Knowledge comes in three kinds, and the record says which.** `kind: fact` (what a trace flag does, what a
wait means, SQL Server's limits; changes only when Microsoft changes it; no review date), `kind: practice` (a
threshold, a recommended setting, an order of work; taken from named experts on a date; carries `reviewed` and
`review_every`, and drifts), `kind: ours` (numbers and orders of work we chose; reasoning stated; reviewed like
practice). A **knowledge review** report lists the records past their review date and the sources whose
version differs from the one we read. Revising a rule is editing one file; its history is git's.

**The knowledge is its own thing; the application is one consumer of it.** So the format is what an AI skill
reads natively: one Markdown file per record with a short front matter (id, card, kind, owner, thresholds,
fixes, related, sources with what was taken and when, reviewed, review_every) and the words under fixed
headings (Rule, Why, Do this with its variants, Then check). The files live at the top of the repository in
`knowledge/{card}/{id}.md`, not inside the Go code; the exe embeds the folder at build; packaging it as an
agent skill is copying the folder and adding `SKILL.md`. The app parses the front matter and the headings,
nothing else. Decision 1 above (YAML) is replaced by this.

Example, the same TMP02 as a file:

```markdown
---
id: TMP02
card: storage
kind: practice
title: TempDB data files equal in size and growth
question: Are TempDB files the same size and growth?
owner: us
thresholds: { growths_per_week_low: 20, growths_per_week_medium: 100 }
fixes: [tempdb-files, autogrowth]
related: [TMP01, TMP03, STO02]
reviewed: 2026-11-04
review_every: 12 months
sources:
  - { kind: microsoft, title: tempdb database, url: https://learn.microsoft.com/sql/relational-databases/databases/tempdb-database, taken: equal sizes and proportional fill }
  - { kind: practitioner, title: Brent Ozar, TempDB data files, url: https://www.brentozar.com/blitz/tempdb-data-files/, taken: why unequal files defeat the extra files, read: 2026-11-04 }
glossary: [tempdb, autogrowth, allocation contention]
---

## Rule
Every TempDB data file should have the same size and the same fixed growth in MB. ...

## Why
Each growth pauses whoever needs the space. TempDB is recreated at its configured size on every restart, ...

## Do this
Apply "Set up the TempDB files": it makes the size TempDB has grown to the starting size of every file ...

### unequal_only
Make the data files the same size and growth. Apply does it in one step; the preview shows each file.

## Then check
Re-run after the change: TMP02 passed and no growth events in the next week.
```

Everything else in the proposal stands: judgement in Go with the record repeating the thresholds and a test
keeping them equal; the four uses; migration one card at a time; no rules engine, no logic in the data.
