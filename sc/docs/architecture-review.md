# Architecture and design review, 2026-11-04

A pause after phases 0 to 4f, before anything else is built. The question: does what we have fit the vision and
the three grounding rules of SCOPE section 1a, and what must change so it keeps fitting as it grows. Numbers
below are from the tree at commit `4f6f46b`.

## What fits and should stay

- **The product shape.** Instances, one database, six questions, a finding that ends in Apply, Investigate or
  Copy. This is the simplification the vision asks for; the sandbox round confirms it reads right.
- **The five-part architecture.** `collect` reads, `domain` judges with no I/O, `service` wires, `store` keeps,
  the UI shows. Every check and fix follows it; agents could build 30 fixes without touching the dispatch
  because of the registry (`service/fixreg.go`). Keep this exactly.
- **The fix pattern** (preview, confirm, apply, read back, change row, rollback) is already the human-in-the-loop
  mechanism rule 3 needs. An AI that proposes a change only has to produce a preview; the human click and the
  log are already there.
- **The case file.** One DuckDB file per machine holds every read and every change; DuckDB is directly
  queryable, which is the cheapest possible memory for an AI.
- **Tests and live checks.** 269 files, every rule unit-tested against fixtures, every SQL read tagged and
  checked read-only, live tests gated by environment variables.

## What needs attention

Ordered by how much the vision depends on it, not by how hard it is.

### 1. The knowledge is scattered; it must become one body (rule 2)

Every check's why, do-this, thresholds and sources exist, but as about 170 Go string constants spread over
48 domain files, with the sources in comments and in the phase contracts. Twenty-three do-this texts point at
other checks by id ("see INS04", "MNT02"), which only an expert can follow. There is no place where a
person, or an AI, can read "the rule for TMP02, its thresholds, why, what to do, and where that comes from"
as one record.

**Change:** a knowledge base, one record per check and per fix, as data embedded in the exe (one YAML or JSON
file per card, loaded once): id, title, question, the rule in words, thresholds, why, do-this (with its
variants), then-check, owner, sources (document, script and lines, URL), related fixes, glossary terms. The
domain code keeps the judgement (the thresholds stay in Go where they are tested) but takes its texts from the
record by id. The same records render the glossary, the hand-over blocks, the Copy-all documents, and the AI
reference. Cross-references by check id become the check's title in plain words. Done incrementally, one card
at a time, with a test that every check id in the catalogue has a record and every record a source. This is
the single most valuable change: it is what makes "factually backed" and "shared with AI" true by
construction instead of by discipline.

### 2. "What the user is looking at" has no server-side form (rule 3)

The hand-over text is built in the browser (`web/src/lib/handover.ts`); the server never produces the page as
text. An AI inside kakapo needs the page the way the user sees it: the card, its findings and checks, the
facts behind them, the query page's sentences, the blocking tree. Today it would have to re-derive that from
JSON.

**Change:** one renderer in `domain` that turns a card detail, a query detail, a blocking tree or a sample into
a plain-text document (the hand-over block is a section of it), used by Copy, by the later HTML export, and by
the AI's "context" tool. The browser's `handoverText` moves to Go and the UI copies what the server renders, so
there is one wording. Cheap, and it removes a duplicate.

### 3. There is no read-only query path for an investigator (rule 3)

Every read is a fixed SQL constant. An AI investigating a live database, or a consultant wanting "run this
read and show me", has no door. Giving an AI a raw connection is not acceptable.

**Change:** `collect.ReadOnlyQuery`: one statement, validated in `domain` (pure, tested: a single SELECT or a
whitelisted DMV function, no EXEC except an allow-list, no DDL/DML, no `xp_`), run under the safe session
(`READ UNCOMMITTED`, `LOCK_TIMEOUT 10 s`, `DEADLOCK_PRIORITY LOW`, a row cap, a time cap), tagged `/* kakapo
ai */`, and every call written to a `query_log` table in the case file with who asked (user or AI), the
statement, rows and duration. The validator is the security boundary and must be as plain as possible.

### 4. An AI-built change needs its own fix kind (rule 3)

An investigation ends in a statement nobody wrote in advance ("rebuild this one index", "update these three
statistics", "set this scoped configuration"). The registry can hold it as one more fix: `proposed-change`,
whose preview carries the statement, a mandatory rollback, the reasoning, and a dry check (`SET PARSEONLY ON`
or `SHOWPLAN_XML` to prove it compiles without running it), with the human's Apply as the only way to run it.
The AI, or the consultant, authors the statement; the fix pattern owns everything after. Nothing else in the
service changes.

### 5. Simplicity is drifting on the investigation pages (rule 1)

The card pages hold the line: sentence first, numbers second. The three new pages do not yet. The query page
shows plan ids, hashes, compile times and forced-plan flags on first sight; the blocking page shows isolation
levels and lock modes; the sample lists wait types. The sentences are there, but the tables sit beside them
at equal weight.

**Change:** a written UI rule and one pass over the three pages: level one is sentences with their buttons;
level two, the numbers behind each sentence, opens on request ("Show the numbers"); level three, the raw
evidence (plan XML, wait list, lock modes), under "Details for a DBA". No check ids in prose; no SQL Server
vocabulary without the plain words beside it. The same rule goes into every future contract.

### 6. The case file grows without bound

Every page view inserts a card read with its findings and checks; nothing is ever deleted. A consultant who
opens kakapo daily for a year has a file in the hundreds of MB, most of it identical reads. Export and share
(phase 5) is not built, and the AI's memory is this file.

**Change:** a retention rule in `store`: keep every read of the last 7 days, then one per card per day for 90
days, then one per card per month; changes, CHECKDB runs, samples and deadlock reports are kept whole. Plus
the export of one server's slice to a case file, which phase 5 already names and the vision now needs for
"share with a colleague or an AI".

### 7. The AI itself: shape it as tools, not as a feature

The AI capability should be a thin layer over things the service already does: context (item 2), read-only
query (item 3), the knowledge (item 1), the fixes' previews (the registry), the case file's history. Those
five are the tool set; whether the caller is an in-app assistant talking to a model, an agent skill, or an MCP
server for Claude Code is an adapter question, and all three can share the one tool set. SCOPE's "no MCP
server in v1" stays, but the tool layer is designed so that adding one is a file, not a redesign. Provider keys
go to Credential Manager like SQL passwords; AI calls are logged in the case file like queries.

### 8. Maintainability of the code as it is

- **Five copies of the finding builder.** Each card has its own `add`, `skip`, `skipAll`, `checks` helpers
  (18 functions doing the same five things). One builder in `domain/finding.go` removes a class of drift
  between cards. Mechanical, low risk, high value for every future card.
- **Files are large.** `performance.go` 1,626 lines, `storage.go` 1,562, `setup.go` 1,448, `query.go` 1,430.
  Item 1 removes the text constants, which is a third of each; the rest splits by check group (one file per
  group, as the actions already are).
- **The card page** (`web/src/screens/card.tsx`, 638 lines) holds every card's panels and rows. One file per
  card's extras, and the page becomes a frame. The mock (`api.mock.ts`, 2,451 lines) should split per feature
  the way the feature mocks already do.
- **The Design card** is a placeholder tile with no plan behind it since the decision to fold index work into
  actions. Either give it a real purpose (the database's tables, indexes and their health, which the triage's
  IDX checks would fill) or remove the tile; a grey card on the landing page contradicts "simple".
- **Naming.** The exe, module, data file and log say `sqlmonitor`; the product and repository say kakapo. Kept
  on purpose so nothing stored is orphaned; worth doing once, deliberately, before a client sees the folder.

### 9. The documentation has grown by accretion

SCOPE.md is scope, stack, architecture, phases, status and decisions in one file; the status line is a
paragraph; the "Out" list still says "no MCP server" and "no Extended Events" without the later nuances; the
phase contracts are the only place many design rules live. An agent brief today has to read eight documents.

**Change:** four documents with one job each, the contracts kept as history:

| Document | Holds |
|---|---|
| `SCOPE.md` | purpose, vision and grounding rules, what is in and out, non-functional requirements, decisions log |
| `docs/DESIGN.md` | the product: the pages, the card anatomy, the three endings, the UI rules (sentence first, three levels), the words we use and do not use |
| `docs/ARCHITECTURE.md` | the code: the five parts and their contracts, the data flow, the registry, the store and its retention, the knowledge base, the AI tool layer, the extension points ("to add a check", "to add a fix", "to add a page") |
| `docs/ROADMAP.md` | what is built, what is next, by phase; the status line moves here |

## What I would do first, and in what order

1. Documents (item 9) and the UI rule (item 5): a few hours, and every later brief starts from them.
2. The knowledge base (item 1), one card at a time, with the builder unification (item 8) done on each card as
   its texts move. Two to three days of agent work; each card is a verifiable step.
3. The server-side renderer (item 2) and the read-only query path (item 3), each a day.
4. The proposed-change fix (item 4) and retention plus export (item 6).
5. The AI tool layer (item 7), then the first assistant on top of it.

None of this changes what the user sees except item 5, which makes it simpler. Everything built so far
stays; the work is moving texts, unifying helpers, splitting files and adding four doors.

## What I would not do

- Rewrite the collectors into a generic query engine. The fixed, tested, tagged reads are a strength.
- Introduce a plugin system, a rules DSL, or a configuration language for checks. Go with data files is enough.
- Build the AI as a chat box first. The tool layer and the knowledge come first, or the assistant will be
  confidently wrong.
- Reopen the scope: one consultant, one exe, one database at a time, no service, no central store.
