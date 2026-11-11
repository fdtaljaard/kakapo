// The Copy block of a finding (docs/phase4a-contract.md, SCOPE section 3 item 10): the finding, why, do this
// and evidence as one plain-text block ready for whoever owns it. Pure functions with type-only imports, so
// web/scripts/handover.test.ts can run them under plain node.

import type { Card, Decision, Finding, Owner } from "@/api"

// Where the finding was read: the server and database, the card's as-of as the card shows it ("09:21",
// "yesterday 16:40"), and the kakapo build stamp. An empty field leaves its part out.
export interface HandoverContext {
  server: string
  database: string
  asOf: string
  version: string
  // The machine SQL Server runs on, and whether kakapo runs there too: a step done on the server names it.
  machine?: string
  onServer?: boolean
}

// Where a step that needs hands on the server is done: "on the SQL Server machine, SQL01".
export function machineWords(ctx: Pick<HandoverContext, "machine">): string {
  return `on the SQL Server machine${ctx.machine ? `, ${ctx.machine}` : ""}`
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
  const object = findingObject(f, ctx)
  const head = [`${f.title} (${f.checkId}, ${f.severity})${object ? `: ${object}` : ""}${where ? ` on ${where}` : ""}`]
  if (ctx.asOf) head.push(`As of ${ctx.asOf}`)
  const meta = [ownerWords[f.owner] ?? f.owner, f.when, f.effort].filter((v) => v && v !== "None").join("; ")
  return [
    head.join("\n"),
    section("What was found", f.finding),
    section("Why it matters", f.why),
    section(`What to do${f.onServerMachine ? ` ${machineWords(ctx)}` : ""}${meta ? ` (${meta})` : ""}`, f.doThis),
    section("Then check", f.thenCheck),
    section("Evidence", f.evidence),
    ctx.version ? `From kakapo build ${ctx.version}` : "",
  ]
    .filter(Boolean)
    .join("\n\n")
}

// What the finding is about (the table, file, column or job: Finding.object), or "" when it only repeats
// the database or the server the block already names. Without it "a heap with no index, 409 MB" leaves
// the reader asking which table.
export function findingObject(f: Pick<Finding, "object">, ctx: Pick<HandoverContext, "server" | "database">): string {
  const o = f.object?.trim() ?? ""
  const bare = (s: string) => s.trim().replace(/^\[(.*)\]$/, "$1").toLowerCase()
  if (!o || bare(o) === bare(ctx.database) || bare(o) === bare(ctx.server)) return ""
  return o
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

// Phase 5m: a decision's row as a hand-over block (docs/phase5m-contract.md, section 1): the verdict and its
// sentence, what kakapo found, what to do first, what it costs, the record and the sources.
export function decisionHandoverText(d: Decision, ctx: HandoverContext): string {
  const where = [ctx.server, ctx.database].filter(Boolean).join(" / ")
  const head = [`${d.title} (${d.checkId}): ${d.tag}${where ? ` on ${where}` : ""}`]
  if (ctx.asOf) head.push(`As of ${ctx.asOf}`)
  const lines = (ls: { label: string; text: string; window?: string }[]) =>
    ls.map((l) => `- ${l.label}: ${l.text}${l.window ? ` (${l.window})` : ""}`).join("\n")
  return [
    head.join("\n"),
    section("Question", d.question),
    section("Verdict", d.sentence),
    section("What kakapo found", lines(d.found)),
    section("Do first", d.doFirst.map((s) => `- ${s.text}`).join("\n")),
    section("What it costs", [lines(d.costs), d.notMeasured ?? "", lines(d.facts)].filter(Boolean).join("\n")),
    section("Decision", d.recordLine ? `${d.recordLine}${d.reviewDue ? " (review due)" : ""}` : ""),
    section("Then check", d.thenCheck),
    section("Sources", d.sources.join("\n")),
    ctx.version ? `From kakapo build ${ctx.version}` : "",
  ]
    .filter(Boolean)
    .join("\n\n")
}
