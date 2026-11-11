// The Copy block builder against fixtures (docs/phase4a-contract.md). vitest is not set up, so this runs
// under plain node, which strips the types:
//   node --experimental-strip-types --test scripts/handover.test.ts   (from web/; node 23.6+ needs no flag)

import { test } from "node:test"
import assert from "node:assert/strict"

import type { Decision, Finding } from "../src/api.ts"
import { copyLabel, decisionHandoverText, findingObject, findingsFor, handoverAll, handoverText, scriptText } from "../src/lib/handover.ts"

const ctx = { server: "CLIENTSQL01\\WMS", database: "GraniteTest", asOf: "09:21", version: "2026.11.03-abc123" }

const bak02: Finding = {
  checkId: "BAK02",
  title: "Transaction log backups",
  severity: "High",
  owner: "Client IT",
  when: "Online",
  effort: "Quick win",
  object: "GraniteTest",
  finding: "Database uses FULL recovery but its log has never been backed up.",
  why: "The log keeps growing until it is backed up.",
  doThis: "Schedule log backups every 15 minutes.",
  thenCheck: "Re-run: BAK02 gone.",
  evidence: "Log file size: 18432 MB.",
}

test("a finding's block, every section", () => {
  assert.equal(
    handoverText(bak02, ctx),
    [
      "Transaction log backups (BAK02, High) on CLIENTSQL01\\WMS / GraniteTest",
      "As of 09:21",
      "",
      "What was found",
      "Database uses FULL recovery but its log has never been backed up.",
      "",
      "Why it matters",
      "The log keeps growing until it is backed up.",
      "",
      "What to do (the infrastructure team; Online; Quick win)",
      "Schedule log backups every 15 minutes.",
      "",
      "Then check",
      "Re-run: BAK02 gone.",
      "",
      "Evidence",
      "Log file size: 18432 MB.",
      "",
      "From kakapo build 2026.11.03-abc123",
    ].join("\n"),
  )
})

test("empty sections, None and an empty context are left out; Query: stays in Evidence", () => {
  const f: Finding = {
    ...bak02,
    severity: "Info",
    owner: "Us",
    when: "None",
    effort: "None",
    why: "",
    thenCheck: "",
    evidence: "Reads 41,880. Query: SELECT 1",
  }
  assert.equal(
    handoverText(f, { server: "", database: "", asOf: "", version: "" }),
    [
      "Transaction log backups (BAK02, Info): GraniteTest",
      "",
      "What was found",
      f.finding,
      "",
      "What to do (ours)",
      f.doThis,
      "",
      "Evidence",
      "Reads 41,880. Query: SELECT 1",
    ].join("\n"),
  )
})

test("labels by owner", () => {
  assert.equal(copyLabel("Client IT"), "Copy for IT")
  assert.equal(copyLabel("Vendor"), "Copy for the vendor")
  assert.equal(copyLabel("Us"), "Copy")
})

test("copy all: the owner's actionable findings under the card, separated by dashes", () => {
  const info: Finding = { ...bak02, checkId: "BAK05", severity: "Info" }
  const vendor: Finding = { ...bak02, checkId: "DBC06", owner: "Vendor" }
  const mine = findingsFor([bak02, info, vendor], "Client IT")
  assert.deepEqual(mine, [bak02])
  const text = handoverAll({ title: "Backups and data safety", question: "Could you lose data?", verdict: "V." }, [bak02, bak02], () => ctx)
  const parts = text.split(`\n\n${"-".repeat(40)}\n\n`)
  assert.equal(parts.length, 3)
  assert.equal(parts[0], "Backups and data safety\nCould you lose data?\nV.")
  assert.equal(parts[1], handoverText(bak02, ctx))
})

test("copy script: the statement, then the rollback commented out", () => {
  assert.equal(scriptText("ALTER X;", ""), "ALTER X;")
  assert.equal(scriptText("ALTER X;", "ALTER Y;\n\nGO"), "ALTER X;\n\n-- Rollback:\n-- ALTER Y;\n--\n-- GO")
})

test("a decision's row as a block (phase 5m): verdict, found, costs, record, sources", () => {
  const d: Decision = {
    decision: "row-versioning",
    checkId: "DEC01",
    title: "Row versioning (READ COMMITTED SNAPSHOT)",
    question: "Should this database let readers use row versions instead of waiting for writers?",
    verdict: "worth-considering",
    tag: "Worth considering",
    tone: "amber",
    sentence: "Writes waited 1 h 10 min for reads.",
    found: [{ label: "Writes waiting for reads", text: "10 min a day.", window: "last 7 days, kakapo's event session" }],
    doFirst: [],
    costs: [{ label: "Version store", text: "0 MB in tempdb now." }],
    notMeasured: "Cannot be read before it is on.",
    facts: [],
    actions: [],
    thenCheck: "After a few working days: the row says On.",
    sources: ["Microsoft Learn: Transaction locking and row versioning guide"],
    recordLine: "Kept off on 2026-11-06: vendor; review by 2027-02-04",
    reviewDue: true,
    history: [],
  }
  assert.equal(
    decisionHandoverText(d, ctx),
    [
      "Row versioning (READ COMMITTED SNAPSHOT) (DEC01): Worth considering on CLIENTSQL01\\WMS / GraniteTest\nAs of 09:21",
      "Question\nShould this database let readers use row versions instead of waiting for writers?",
      "Verdict\nWrites waited 1 h 10 min for reads.",
      "What kakapo found\n- Writes waiting for reads: 10 min a day. (last 7 days, kakapo's event session)",
      "What it costs\n- Version store: 0 MB in tempdb now.\nCannot be read before it is on.",
      "Decision\nKept off on 2026-11-06: vendor; review by 2027-02-04 (review due)",
      "Then check\nAfter a few working days: the row says On.",
      "Sources\nMicrosoft Learn: Transaction locking and row versioning guide",
      "From kakapo build 2026.11.03-abc123",
    ].join("\n\n"),
  )
})

test("the finding's object is named in the header unless it repeats the database or server", () => {
  const heap: Finding = { ...bak02, checkId: "IDX06", title: "Heaps", severity: "Medium", object: "[dbo].[DocumentLog]" }
  assert.equal(handoverText(heap, ctx).split("\n")[0], "Heaps (IDX06, Medium): [dbo].[DocumentLog] on CLIENTSQL01\\WMS / GraniteTest")
  assert.equal(findingObject({ object: "[GraniteTest]" }, ctx), "")
  assert.equal(findingObject({ object: "clientsql01\\wms" }, ctx), "")
  assert.equal(findingObject({ object: "" }, ctx), "")
  assert.equal(findingObject({ object: "tempdb" }, ctx), "tempdb")
})

// BAK02 has two ways out, each its own Apply (docs/phase3g-contract.md, revision 3).
import { twoWaysChoices } from "../src/lib/two-ways.ts"

test("a finding with two fixes gets one named Apply for each", () => {
  const fixes = [
    { id: "backup-schedule", title: "Create a backup schedule", target: "kakapo: GraniteTest log backup (every 15 minutes)" },
    { id: "recovery-model", title: "Change the recovery model", target: "SIMPLE" },
  ]
  assert.deepEqual(twoWaysChoices({ fixId: "backup-schedule", altFixId: "recovery-model" }, fixes), [
    { label: "Schedule log backups", fixId: "backup-schedule" },
    { label: "Switch to SIMPLE", fixId: "recovery-model" },
  ])
  // On Express the log backups come from kakapo's maintenance procedure.
  assert.deepEqual(twoWaysChoices({ fixId: "install-maintenance-proc", altFixId: "recovery-model" }, fixes), [
    { label: "Install the maintenance procedure", fixId: "install-maintenance-proc" },
    { label: "Switch to SIMPLE", fixId: "recovery-model" },
  ])
  // One fix, or none: the ordinary single Apply.
  assert.equal(twoWaysChoices({ fixId: "backup-schedule" }, fixes), undefined)
  assert.equal(twoWaysChoices({ altFixId: "recovery-model" }, fixes), undefined)
  assert.equal(twoWaysChoices({}), undefined)
})

test("a step done on the SQL Server machine names the machine in What to do", () => {
  const ifi: Finding = { ...bak02, checkId: "INS07", owner: "Client IT", onServerMachine: true, doThis: "Grant the right." }
  const text = handoverText(ifi, { ...ctx, machine: "SQL01", onServer: false })
  assert.match(text, /\n\nWhat to do on the SQL Server machine, SQL01 \(the infrastructure team; /)
  assert.match(handoverText(ifi, ctx), /\n\nWhat to do on the SQL Server machine \(the infrastructure team; /)
  assert.doesNotMatch(handoverText(bak02, { ...ctx, machine: "SQL01" }), /SQL Server machine/)
})
