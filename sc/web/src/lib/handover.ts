// The Copy block of a finding (docs/phase4a-contract.md, SCOPE section 3 item 10): the finding, why, do this
// and evidence as one plain-text block ready for whoever owns it. Pure functions with type-only imports, so
// web/scripts/handover.test.ts can run them under plain node.

import type { Card, Finding, Owner } from "@/api"

// Where the finding was read: the server and database, the card's as-of as the card shows it ("09:21",
// "yesterday 16:40"), and the kakapo build stamp. An empty field leaves its part out.
export interface HandoverContext {
  server: string
  database: string
  asOf: string
  version: string
}

const ownerWords: Record<Owner, string> = {
  Us: "ours",
  "Client IT": "the infrastructure team",
  Vendor: "the vendor",
}

// The Copy button on a finding, named after who the block is for.
export function copyLabel(owner: Owner): string {
  if (owner === "Client IT") return "Copy for IT"
  if (owner === "Vendor") return "Copy for the vendor"
  return "Copy"
}

// One finding as a hand-over block; a blank line between sections, a section whose field is empty left out.
// "None" (no window, no effort) says nothing, so it is left out of the "What to do" line, as on the card.
export function handoverText(f: Finding, ctx: HandoverContext): string {
  const where = [ctx.server, ctx.database].filter(Boolean).join(" / ")
  const head = [`${f.title} (${f.checkId}, ${f.severity})${where ? ` on ${where}` : ""}`]
  if (ctx.asOf) head.push(`As of ${ctx.asOf}`)
  const meta = [ownerWords[f.owner] ?? f.owner, f.when, f.effort].filter((v) => v && v !== "None").join("; ")
  return [
    head.join("\n"),
    section("What was found", f.finding),
    section("Why it matters", f.why),
    section(`What to do${meta ? ` (${meta})` : ""}`, f.doThis),
    section("Then check", f.thenCheck),
    section("Evidence", f.evidence),
    ctx.version ? `From kakapo build ${ctx.version}` : "",
  ]
    .filter(Boolean)
    .join("\n\n")
}

function section(label: string, text: string | undefined): string {
  const body = text?.trim()
  return body ? `${label}\n${body}` : ""
}

export const DASHES = "-".repeat(40)

// The findings one owner gets in a "Copy all" block: theirs, without the Info notes.
export function findingsFor(findings: Finding[], owner: Owner): Finding[] {
  return findings.filter((f) => f.owner === owner && f.severity !== "Info")
}

// "Copy all for IT" / "Copy all for the vendor": the card title and question, the verdict, then every
// finding's block, separated by a line of dashes. ctxFor gives each finding its context (a panel's
// finding has its own as-of).
export function handoverAll(
  card: Pick<Card, "title" | "question" | "verdict">,
  findings: Finding[],
  ctxFor: (f: Finding) => HandoverContext,
): string {
  const head = [card.title, card.question, card.verdict].filter(Boolean).join("\n")
  return [head, ...findings.map((f) => handoverText(f, ctxFor(f)))].join(`\n\n${DASHES}\n\n`)
}

// A fix preview's Copy script: the statement, then the rollback when there is one. The rollback lines are
// commented out, so running the whole copied script applies the change and does not undo it at once.
export function scriptText(statement: string, rollback: string): string {
  if (!rollback.trim()) return statement
  const commented = rollback
    .trim()
    .split(/\r?\n/)
    .map((line) => (line ? `-- ${line}` : "--"))
    .join("\n")
  return `${statement}\n\n-- Rollback:\n${commented}`
}
