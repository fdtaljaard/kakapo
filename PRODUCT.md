# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

- **Primary: a technical consultant** who looks after a client's Microsoft SQL Server, one client at a time,
  and is often not a SQL specialist (a novice SQL developer is the reference reader). They run kakapo on
  their own machine against the client's server to see what is wrong, understand why, and act.
- **Also: the client's own IT or developers**, who run kakapo themselves, or receive a finding as a
  "Copy for IT" hand-over block and act on it (a Windows right, the storage, a service) without opening
  kakapo.

## Product Purpose

kakapo connects to a SQL Server, reads it through its own checks and samples, and says in plain words
what is wrong with one database and its server, why it matters and what to do, then lets the user act
from the same place. Success is a non-DBA who can keep a database healthy: they know whether something is
a problem now, and every problem ends in an action they can take or hand over.

## Positioning

- **Plain words, answer first.** Each screen says in a sentence whether something is a problem now
  ("is it still a problem?"), then the numbers; expert detail is folded for learning, not removed.
- **Every finding ends in an action:** Apply (preview, confirm, apply, read back, change log), Investigate,
  or Copy for whoever owns it. Never a description alone.
- **Every recommendation is sourced:** each check has its rule, thresholds, why, do-this, then-check and
  the sources it rests on (Microsoft's documentation, named practitioners). A number without a source does
  not ship.
- **Portable, nothing to install on the consultant's machine:** one exe. Anything kakapo creates on the
  server (a job, an Extended Events session, a maintenance procedure) comes with a remove fix and a
  change-log row.

## Operating Context

- Runs as a local Go executable that serves a React web UI, opened in a browser app window on the
  consultant's machine; data is kept locally in DuckDB. kakapo may run on the SQL Server machine itself or
  on another computer, and says which (steps that need hands on the server name that machine).
- Works per server and per database: a database page with six cards (Backups, Storage, Setup, Upkeep,
  Performance, Design), findings with "What was found, Why it matters, Do this, Then check, Evidence", and
  investigation pages (a query, blocking, the live sample, the tuning report).
- Changes go only through a named fix: preview, the human confirms, apply, read back, log.
- Covers SQL Server Express (no Agent: kakapo's maintenance procedure, called by Windows Task Scheduler,
  takes the backups) as well as editions with SQL Server Agent.
- Hand-over is by copied text (Copy for IT, Copy for the vendor) pasted into email or tickets.

## Capabilities and Constraints

- Checks and fixes are catalogued by ID (BAK, STO, TMP, INS, DBC, MNT, WRK, QRY, IDX, DLK and others);
  each fix has options with working defaults, the decisive ones shown up front and the rest under
  "More options".
- Generic product: built by Granite for any SQL Server. Nothing a user reads names Granite, its products
  or tables; examples and fixtures use neutral names.
- The repository is public: no client server, host, person or database names in it.
- Committed, not yet built: AI inside the app (it sees what the user sees, reads the live server read-only,
  and proposes changes a human applies through the fix pattern; it never applies anything itself); a nightly
  summary email (`sc/docs/nightly-summary-email.md`); one shared knowledge base of check knowledge with
  sources, used by the app's texts, the hand-over blocks and AI skills (`sc/docs/knowledge-base.md`).
- Not committed: the shareable case file (the DuckDB file opened elsewhere without the server), although
  earlier scope text describes it.
- Ruled out: a console for experts (grids first, sentences never), advice without a source, an AI that
  changes a server without a human click, a service, a central store or a team product.

## Brand Commitments

- Name: **kakapo**, lower case in running text. Splash tagline: "You don't need to fly to keep a database
  running."
- Voice: short, plain sentences for a non-specialist; say what is true now, then why, then what to do;
  "after", never "because of", when a change only preceded an effect.
- Existing assets: `sc/docs/brand/logo.jpg`, `sc/docs/brand/WelcomePage.png`,
  `sc/docs/brand/productSound.m4a`.

## Evidence on Hand

- The agreed scope and rules: `sc/SCOPE.md` (section 1a, the five rules); phase contracts in
  `sc/docs/phase*-contract.md`; reviews in `sc/docs/architecture-review.md`,
  `sc/docs/darlingdata-review.md`, `sc/docs/troubleshooting-review.md`.
- Mockups: `sc/docs/mockups/instance-overview.html`, `sc/docs/mockups/database-activity.html`.
- No customer testimonials, case studies, pricing, licensing or usage figures exist; do not invent them.

## Product Principles

1. Simplify complexity, in the product and in the code: a sentence first, the numbers second; a screen
   that needs a DBA to read it has drifted.
2. Answer "is it a problem now?" before listing anything; fold expert detail rather than delete it.
3. Every finding ends in Apply, Investigate or Copy, and every change is previewed, confirmed, read back
   and logged.
4. Every recommendation rests on a fact and a named source.
5. Options with defaults: what can differ between sites is an option that works unseen.
