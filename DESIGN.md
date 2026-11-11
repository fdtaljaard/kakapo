---
name: kakapo
description: A portable SQL Server monitor that reports in plain words, verdict first.
colors:
  paper: "oklch(1 0 0)"
  ink: "oklch(0.145 0 0)"
  primary: "oklch(0.205 0 0)"
  primary-ink: "oklch(0.985 0 0)"
  quiet-surface: "oklch(0.97 0 0)"
  quiet-ink: "oklch(0.556 0 0)"
  hairline: "oklch(0.922 0 0)"
  focus-ring: "oklch(0.708 0 0)"
  act-today-red: "oklch(0.577 0.245 27.325)"
  to-do-amber: "oklch(0.666 0.179 58.318)"
  to-do-amber-deep: "oklch(0.555 0.163 48.998)"
  all-clear-green: "oklch(0.596 0.145 163.225)"
typography:
  page-title:
    fontFamily: "Geist Variable, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.4
  section-title:
    fontFamily: "Geist Variable, sans-serif"
    fontSize: "1rem"
    fontWeight: 500
    lineHeight: 1.5
  body:
    fontFamily: "Geist Variable, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.43
  label:
    fontFamily: "Geist Variable, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 500
    lineHeight: 1.33
  literal:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: 1.33
rounded:
  sm: "6px"
  md: "8px"
  lg: "10px"
  xl: "14px"
  pill: "26px"
spacing:
  tight: "8px"
  card-sm: "12px"
  card: "16px"
  page-gap: "24px"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.primary-ink}"
    rounded: "{rounded.lg}"
    padding: "0 10px"
    height: "32px"
    typography: "{typography.body}"
  button-outline:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "0 10px"
    height: "32px"
  button-outline-hover:
    backgroundColor: "{colors.quiet-surface}"
  button-ghost:
    textColor: "{colors.ink}"
    rounded: "{rounded.md}"
    padding: "0 8px"
    height: "24px"
    typography: "{typography.label}"
  card:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.xl}"
    padding: "{spacing.card}"
  badge-severity-high:
    textColor: "{colors.to-do-amber-deep}"
    rounded: "{rounded.pill}"
    padding: "2px 8px"
    height: "20px"
    typography: "{typography.label}"
  badge-severity-critical:
    textColor: "{colors.act-today-red}"
    rounded: "{rounded.pill}"
    padding: "2px 8px"
    height: "20px"
  input:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "4px 10px"
    height: "32px"
  statement-block:
    backgroundColor: "{colors.quiet-surface}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "12px"
    typography: "{typography.literal}"
---

# Design System: kakapo

## Overview

**Creative North Star: "The Field Report"**

Every screen in kakapo reads like a short report from someone who has just checked the server: the verdict
first, in one plain sentence, then why it matters, then what to do, then the evidence in small print. A
consultant who is not a SQL specialist should get the answer from the first line, and be able to hand the
whole report to IT as it stands. The interface is the paper the report is written on; it does not compete
with what the report says.

The look is calm and friendly. It is a neutral, grey shadcn interface (Geist, small type, white or near-black
surfaces that follow the operating system's theme) where colour is spent almost entirely on status. Calm means
nothing shouts until it needs to: a healthy database is mostly grey text with a few green dots. Friendly means
sentences, not codes, lead; numbers and identifiers follow in a quieter voice. Density is that of a well-set
report, not a console: one column of cards and findings at most 72rem wide, with room between them.

kakapo must never look like a DBA console (grids and DMV columns first, sentences never), a monitoring
dashboard (gauges, big charts, walls of red and green tiles), or a marketing site (hero gradients, display
type, illustrations, glossy cards).

**Key Characteristics:**
- A sentence leads every card, finding and dialog; the numbers come after it, smaller and greyer.
- Neutral greys everywhere; red, amber and green carry status and nothing else.
- Flat surfaces separated by hairlines and a faint ring, never by shadows.
- Literal values (hosts, builds, paths, SQL) are set in monospace and can be copied.
- Light and dark both, following the operating system.

## Colors

A colourless report on white (or near-black) paper, with three status inks that each mean one thing.

### Primary
- **Report Black** (`primary`): the default button, the few solid controls. Near-black on the light theme and
  near-white on the dark one; it is a weight, not a hue.

### Neutral
- **Paper** (`paper`): page and card background.
- **Ink** (`ink`): body text and titles.
- **Quiet Surface** (`quiet-surface`): the top bar's band, statement blocks (Do this, SQL), hover fills.
- **Quiet Ink** (`quiet-ink`): secondary text: evidence, help lines, "checked 5 minutes ago", labels.
- **Hairline** (`hairline`): borders, dividers, input strokes.
- **Focus Ring** (`focus-ring`): the 3px ring at half strength around a focused control.

### Status
- **Act-Today Red** (`act-today-red`): a red card status, a Critical finding, an error, a destructive action
  (shadcn's destructive token).
- **To-Do Amber** (`to-do-amber`, deeper `to-do-amber-deep` for text on a tint): something to do: an amber
  card status, High and Medium findings, notices (Express, held-back Apply, "One more step"), unmet
  requirements.
- **All-Clear Green** (`all-clear-green`): no problem found: a green card status, a met requirement, a better
  number.
- Grey (Quiet Ink) is the fourth status: not checked, or could not be read.

On the dark theme the status inks step up a level (amber 400, emerald 400, the dark destructive) and tints sit
at 15 to 20% behind them.

### Named Rules
**The One Meaning Rule.** Red is act today, amber is something to do, green is no problem found, grey is not
checked. A colour never decorates, and never means two things.

**The Grey Default Rule.** A screen with nothing wrong is almost entirely grey and black. If a healthy page
looks colourful, something is spending status colour on decoration.

## Typography

**Body Font:** Geist Variable (with sans-serif)
**Literal Font:** the system monospace stack (ui-monospace, Consolas)

**Character:** one neutral, legible sans for everything a person reads, and monospace only for what a person
copies or types: hosts, build stamps, file paths, SQL, commands. There is no display face.

### Hierarchy
- **Page Title** (600, 1.25rem): the database or page name at the top of a screen; one per page.
- **Section Title** (500, 1rem to 1.125rem): a card's title, a dialog title, a section heading.
- **Body** (400, 0.875rem): findings, verdicts, explanations, table cells. The default size of the product.
- **Label** (500, 0.75rem): badges, "Why it matters" style labels, meta lines (owner, when, effort), small
  buttons. Often Quiet Ink.
- **Literal** (monospace, 0.75rem): build stamps, hosts, paths, SQL in statement blocks.

### Named Rules
**The Sentence-First Rule.** The first line of any card, finding or dialog is a sentence in Body or larger;
the evidence under it is Label size in Quiet Ink. Never lead with a number or a code.

**The Copyable Literal Rule.** Anything set in monospace is something the user may copy, and statement blocks
carry their own Copy button.

## Layout

One centred column, at most 72rem wide, with 16px gutters on a phone and 24px from the small breakpoint up,
under a thin top bar that names kakapo and the build. Pages stack their parts with a 24px gap: header, notices,
load strip, decisions, then the cards. Cards sit in a responsive grid of one, two (medium screens) or three
(extra large) columns; findings and dialogs are a single column. Inside a card, parts are spaced 16px apart
(12px for the small card). Text wraps and breaks long identifiers rather than overflowing.

## Elevation & Depth

Flat. Depth comes from tone, not shadow: cards are separated from the page by a faint ring (the ink at 10%),
the top bar by a quiet band and a hairline, statement blocks by the Quiet Surface fill. Dialogs are the only
layer above the page, on shadcn's overlay.

### Named Rules
**The No-Shadow Rule.** Surfaces never cast shadows at rest or on hover. If something needs to stand apart,
use a ring, a hairline or a tint.

## Shapes

Soft, consistent corners from one 10px radius: controls and statement blocks at 10px (8px for small buttons),
cards at 14px, badges as full pills. Notices are bordered boxes with an 8px corner and a status tint. No
sharp corners, no decorative shapes.

## Components

### Buttons
Quiet and compact; the action is in the words, not the button's size.
- **Shape:** gently rounded (10px; small buttons 8px), 32px tall (28px small, 24px extra small).
- **Primary:** Report Black with white text, for the one main action of a dialog (Apply).
- **Outline:** paper with a hairline border, for the actions on a finding (Apply, a second way out,
  Investigate) and Refresh.
- **Ghost:** no fill until hover, for Copy and Copy for IT.
- **Destructive:** a red tint with red text, never a solid red block.
- **Hover / Focus:** a Quiet Surface fill on hover; a 3px half-strength focus ring; a 1px press.

### Badges
- **Style:** pills 20px tall in Label type. Severity badges are tinted: red tint for Critical, amber tint for
  High and Medium, grey for Low.

### Cards / Containers
- **Corner Style:** 14px.
- **Background:** Paper; Info findings drop to a transparent background with a dashed hairline.
- **Shadow Strategy:** none (see Elevation & Depth); a 1px ring of ink at 10%.
- **Internal Padding:** 16px, or 12px for the small card.

### Inputs / Fields
- **Style:** 32px tall, hairline stroke, transparent fill, 10px corners.
- **Focus:** the border turns Focus Ring with the 3px half-strength ring.
- **Error / Disabled:** a red stroke and ring; disabled at half opacity.

### Notices
- **Style:** a bordered box with an 8px corner. Amber notices (Express, "One more step", Apply held back) use
  an amber border and a 10 to 15% amber tint with amber text and a warning icon; neutral notices (kakapo runs
  on another computer) use a hairline border, Body text and a grey icon.

### Finding Card (signature)
The report itself. A severity badge, the check ID in Label type, the title, and the meta line (owner, when,
effort) with Copy for IT on the right; then the finding as a Body sentence (larger when it is not Info);
then "Why it matters"; then "Do this" as a statement block with its own Copy (labelled "Do this on the SQL
Server machine, NAME" when the step needs hands on the server); then "Then check"; then the evidence in Label
type and Quiet Ink. Apply, a second way out, and Investigate sit with the title as outline buttons.

### Status Line (signature)
A coloured dot and a short phrase ("3 things to do", "No problems found") under a card's title, the dot in
the One Meaning colours; a spinner replaces the dot while the card is read.

## Do's and Don'ts

### Do:
- **Do** open every card, finding and dialog with a plain sentence, then put numbers and identifiers under it
  in Label type and Quiet Ink.
- **Do** keep status colour to its one meaning: red act today, amber to do, green no problem, grey not checked.
- **Do** set hosts, build stamps, paths, SQL and commands in monospace, with a Copy button on statement blocks.
- **Do** separate surfaces with the 10% ring, hairlines and the Quiet Surface tint.
- **Do** support light and dark through the existing tokens, following the operating system's theme.
- **Do** fold expert detail (plans, per-file tables, raw counters) under the answer instead of removing it.

### Don't:
- **Don't** make it look like a DBA console: no grid or DMV columns as the first thing on a screen.
- **Don't** make it look like a monitoring dashboard: no gauges, no wall of red and green tiles, no
  auto-refreshing charts as the lead.
- **Don't** make it look like a marketing site: no gradients, display type, illustrations or glossy cards.
- **Don't** add shadows to cards, buttons or notices.
- **Don't** use solid red or amber fills for whole blocks; status lives in text, dots, borders and light tints.
- **Don't** introduce a second sans or a decorative typeface.
