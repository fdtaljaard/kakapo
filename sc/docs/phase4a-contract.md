# Phase 4a contract: Copy on every finding and every preview

SCOPE section 3 item 10: a finding ends in Apply, Investigate or Copy. This phase gives every finding its Copy
and every fix preview its Copy script. Frontend only, no API change.

## The hand-over block

`web/src/lib/handover.ts` exports `handoverText(f: Finding, ctx: {server: string; database: string; asOf: string;
version: string}): string`, plain text, built exactly like this (blank line between sections; a section whose
field is empty is left out):

```
{title} ({checkId}, {severity}) on {server} / {database}
As of {asOf, as the card shows it}

What was found
{finding}

Why it matters
{why}

What to do ({owner}; {when}; {effort})
{doThis}

Then check
{thenCheck}

Evidence
{evidence}

From kakapo build {version}
```

Owner is spelled out: `Us` -> "ours", `Client IT` -> "the infrastructure team", `Vendor` -> "the vendor". The
`Query: ` part of an evidence string (the split the finding card already makes) stays inside Evidence as it is.

## Where Copy appears

- **Every finding card** (`components/finding-card.tsx`): a button next to Apply (or alone), labelled by owner:
  "Copy for IT" (Client IT), "Copy for the vendor" (Vendor), "Copy" (Us). It copies the hand-over block. The
  existing per-statement Copy on the `Query:` part stays. After copying the label reads "Copied" for 2.5 s;
  when the clipboard is refused, the block opens selected in a small read-only textarea under the card with
  "Press Ctrl+C" (the fallback `statement-block.tsx` uses, shared as one hook `useCopy` in `lib/use-copy.ts`
  that `statement-block.tsx` also moves to).
- **The card page header** (`screens/card.tsx`): when the card has Client IT findings, a button "Copy all for
  IT" that copies one block: the card title and question, the verdict, then every Client IT finding's block
  separated by a line of dashes. Likewise "Copy all for the vendor" when Vendor findings exist. Info findings
  are left out of "all" blocks.
- **The fix dialog** (`components/fix-dialog.tsx`): "Copy script" in the preview state and the done state,
  copying `statement` followed by a blank line, `-- Rollback:` and the rollback when there is one. On Express
  the existing Copy script stays as the primary button.

`finding-card.tsx` needs the server name, database, as-of and version: `FindingCard` gains an optional
`context` prop filled by the card page from the overview and the health version (the build stamp the home
page shows; read it once through `api.health()` or pass it from where the stamp is).

## Mock and tests

The mock needs nothing new. Add `handover.test.ts` (vitest is not set up; if adding it costs more than a few
lines, put the block builder's expected output as a fixture compared in a plain `node --test` script under
`web/scripts`, or skip the test and state so). Run `npm run build` and `npm run lint`.

## Done when

On the Backups card of the mock, every finding shows its Copy button with the right label; "Copy all for IT"
appears on a card with Client IT findings and is absent on one without; the fix dialog shows Copy script and
the copied text starts with the statement; the build passes.
