import { ApiError, type FixPreview, type FixResult, type FixRun, type RunItem } from "./api"
import type { IndexLens, LensRow } from "./api-indexes"
import type { IndexShape } from "./api-queries"
import type { FeatureMock } from "./api.mock"

// Phase 5t (docs/phase5t-contract.md): fixture answers for the Indexes page. In WarehouseLive the page
// lists four indexes the slowest queries read in full (invented names): StockMove's primary key (the
// table itself, 510,928 pages), an index of Orders, an index of StockBalance whose table has old statistics
// (unticked, with Update statistics), and a small clustered key that ALLOW_PAGE_LOCKS OFF blocks. Measuring
// takes a second each. "Reorganise selected" runs as a batch whose items advance every 2.5 seconds;
// Stop ends it after the one in flight; one index fails when "mock.indexFails" is "1". WarehouseTest has
// no candidate; Warehouse_Archive has Query Store off. Reorganised indexes turn up as "Reorganised by
// kakapo" until the page is reloaded.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()

const lensPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/indexes\/candidates$/
const shapePath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/indexes\/shape\?(.*)$/
const fixPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/reorganize-indexes\/(preview|apply)$/
const runPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/runs\/(mockbatch\d+)(\/stop)?$/

function lensQuery(hash: string, text: string, runs: number, minutes: number) {
  return { hash, text, runs, totalMs: minutes * 60_000, lastRun: hoursAgo(2), summary: `${runs.toLocaleString("en-US")} runs, ${minutes} min in 7 days` }
}

const statsAction = (table: string) => ({
  fixId: "update-statistics-table",
  label: "Update statistics",
  title: `Update statistics on dbo.${table}`,
  options: { table: `[dbo].[${table}]` },
})

const rows: LensRow[] = [
  {
    schema: "dbo", table: "StockMove", name: "dbo.StockMove", index: "PK_StockMove", kind: "clustered", pages: 510_928, sizeMB: 3_990, totalMs: 38 * 60_000 + 9 * 60_000,
    queries: [
      lensQuery("0x8F3A2C11D07B9E45", "SELECT ItemId, SUM(Qty) FROM dbo.StockMove WHERE MoveDate >= @1 GROUP BY ItemId", 4310, 38),
      lensQuery("0x51C0D7A2E94B3F18", "SELECT COUNT(*) FROM dbo.StockMove WHERE Reference LIKE @1", 910, 9),
    ],
    queryCount: 2, more: 0, stale: false, statsAction: statsAction("StockMove"), options: { table: "[dbo].[StockMove]", index: "PK_StockMove" },
  },
  {
    schema: "dbo", table: "Orders", name: "dbo.Orders", index: "IX_Orders_Date", kind: "nonclustered", pages: 12_400, sizeMB: 97, totalMs: 21 * 60_000,
    queries: [lensQuery("0x2B7E4D90C1A35F68", "SELECT OrderId, Total FROM dbo.Orders WHERE OrderDate > @1", 6200, 14), lensQuery("0x9D14A6E37F2C0B51", "SELECT TOP (50) * FROM dbo.Orders ORDER BY OrderDate DESC", 410, 5), lensQuery("0x44E8B1F20D6A9C73", "SELECT COUNT(*) FROM dbo.Orders WHERE OrderDate < @1", 120, 2)],
    queryCount: 5, more: 2, stale: false, statsAction: statsAction("Orders"), options: { table: "[dbo].[Orders]", index: "IX_Orders_Date" },
  },
  {
    schema: "dbo", table: "StockBalance", name: "dbo.StockBalance", index: "IX_StockBalance_Item", kind: "nonclustered", pages: 3_050, sizeMB: 24, totalMs: 6 * 60_000,
    queries: [lensQuery("0x6C03F9A2E85D1B47", "SELECT ItemId, Qty FROM dbo.StockBalance WHERE Qty <> 0", 2800, 6)],
    queryCount: 1, more: 0, stale: true,
    statsNote: "Statistics are 12 days old and 22% of rows have changed. Update its statistics first: it is cheaper than index work, and often all a slow query needs.",
    statsAction: statsAction("StockBalance"), options: { table: "[dbo].[StockBalance]", index: "IX_StockBalance_Item" },
  },
  {
    schema: "dbo", table: "AuditTrail", name: "dbo.AuditTrail", index: "PK_AuditTrail", kind: "clustered", pages: 1_800, sizeMB: 14, totalMs: 90_000,
    queries: [lensQuery("0xA0B1C2D3E4F50617", "SELECT TOP (1) * FROM dbo.AuditTrail ORDER BY At DESC", 300, 1)],
    queryCount: 1, more: 0, stale: false, blocked: "PK_AuditTrail has ALLOW_PAGE_LOCKS OFF, and an index cannot be reorganised then (Microsoft).",
    statsAction: statsAction("AuditTrail"), options: { table: "[dbo].[AuditTrail]", index: "PK_AuditTrail" },
  },
]

// What each index measures to: pages now, how full, how out of order.
const shapes: Record<string, [number, number, number, number]> = {
  PK_StockMove: [510_928, 3_990, 72, 60],
  IX_Orders_Date: [12_400, 97, 58, 41],
  IX_StockBalance_Item: [3_050, 24, 97, 3],
  PK_AuditTrail: [1_800, 14, 99, 1],
}

const done = new Set<string>() // `${db}/${index}`

function shapeOf(db: string, schema: string, table: string, index: string): IndexShape {
  const s = shapes[index]
  if (!s) return { schema, table, index, measured: false, pages: 0, sizeMB: 0, density: 0, fragmentation: 0, estimatedPages: 0, savedPct: 0, gain: false, text: `There is no index ${index} on ${schema}.${table}.` }
  const reorganised = done.has(`${db}/${index}`)
  const [pages, sizeMB, density, fragmentation] = reorganised ? [Math.round(s[0] * (s[2] / 100)), s[1] * (s[2] / 100), 99, 1] : s
  const estimated = Math.round((pages * density) / 100)
  const saved = ((pages - estimated) * 100) / pages
  const gain = !reorganised && pages >= 1000 && saved >= 5
  const reads = `The plan reads all of ${index} (${pages.toLocaleString("en-US")} pages, ${sizeMB >= 1024 ? (sizeMB / 1024).toFixed(1) + " GB" : Math.round(sizeMB) + " MB"}). Its pages are ${density}% full and ${fragmentation}% out of order.`
  return {
    schema, table, index, measured: true, pages, sizeMB, density, fragmentation, estimatedPages: estimated, savedPct: Math.round(saved * 10) / 10, gain,
    text: gain
      ? `${reads} Reorganising it would pack it into about ${estimated.toLocaleString("en-US")} pages, so each scan would read about ${Math.round(saved)}% fewer.`
      : `${reads} Index work would not help this query.`,
  }
}

interface Batch {
  db: string
  items: RunItem[]
  step: number // the item in flight
  startedAt: number
  stopping: boolean
  options: Record<string, string>
}
const batches = new Map<string, Batch>()
let nextBatch = 1

function chosenFrom(options: Record<string, string>): { table: string; index: string }[] {
  return (options.indexes ?? "")
    .split("\n")
    .filter((l) => l.trim() !== "")
    .map((l) => {
      const [table, index] = l.split("\t")
      return { table, index }
    })
}

function previewFor(db: string, options: Record<string, string>): FixPreview {
  const chosen = chosenFrom(options)
  const known = chosen.map((c) => ({ ...c, row: rows.find((r) => r.index === c.index) })).filter((c) => c.row && !c.row.blocked)
  const pages = known.reduce((n, c) => n + c.row!.pages, 0)
  const mb = known.reduce((n, c) => n + c.row!.sizeMB, 0)
  const size = mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`
  const p: FixPreview = {
    database: db,
    engineVersion: "16.0.4175.1",
    changes: known.map((c) => ({ setting: `${c.row!.name} · ${c.index}`, current: `${c.row!.pages.toLocaleString("en-US")} pages`, target: "reorganised" })),
    statement: known.map((c) => `-- ${c.row!.name} · ${c.index}\nALTER INDEX [${c.index}] ON [${db}].[dbo].[${c.row!.table}] REORGANIZE;`).join("\n"),
    rollback: "",
    warnings: [],
    options: { ...options, maxGB: options.maxGB ?? "20" },
    inputs: [
      {
        name: "maxGB",
        label: "Warn above (GB of indexes)",
        value: options.maxGB ?? "20",
        help: "A reorganise logs about the pages it moves. Above this size the preview warns more strongly. 0 means no bound.",
        kind: "text",
        advanced: true,
      },
    ],
    lead: `${known.length} ${known.length === 1 ? "index" : "indexes"} to reorganise, one after another: ${pages.toLocaleString("en-US")} pages (${size}).`,
  }
  if (known.length === 0) {
    p.warnings = ["Tick at least one index."]
    p.statement = ""
    return p
  }
  const cap = Number(p.options?.maxGB ?? "20")
  p.warnings = [
    "Reorganising is online on every edition: queries and changes to the tables carry on while it runs. Each index is reorganised in turn; stop the run at any time and the work done so far is kept.",
    "If one index fails, kakapo goes on to the next. An index that is already full and in order when its turn comes is skipped.",
    `In FULL recovery every page it moves is logged: the next log backups carry roughly ${size} more, and the log file grows if the log backups cannot keep up.`,
    ...(cap > 0 && mb > cap * 1024 ? [`This is ${size} of indexes, more than the ${cap} GB kakapo starts a batch with. Take the log backups and the free space into account, or tick fewer.`] : []),
    "It needs a little free space in the data file that holds the index; when that file is full it stops with error 1105.",
    "Reorganising does not update statistics (Microsoft).",
    "kakapo must stay open until this finishes: it runs from kakapo, not from SQL Server Agent. Closing kakapo stops the run (the index in progress keeps its progress), and it does not resume.",
    "Each index gets its own row in the change log, so a query's page compares its runs before and after, index by index.",
    "Needs the ALTER permission on each table.",
  ]
  return p
}

// The run as the poll sees it: each item takes 2.5 seconds, the first measured, then reorganised.
function advance(id: string, b: Batch): FixRun {
  const elapsed = Date.now() - b.startedAt
  const stepNow = Math.floor(elapsed / 2500)
  for (let i = 0; i < b.items.length; i++) {
    const it = b.items[i]
    if (it.state === "stopped" || it.state === "failed" || it.state === "done" || it.state === "skipped") continue
    if (b.stopping && i >= b.step) {
      it.state = "stopped"
      it.text = i === b.step && stepNow === b.step ? "stopped part-way: the pages moved so far stay moved" : "not started: the run was stopped"
      continue
    }
    if (i < stepNow) {
      const index = it.label.split(" · ")[1]
      if (localStorage.getItem("mock.indexFails") === "1" && i === 1) {
        it.state = "failed"
        it.text = `SQL Server did not reorganise the index ${index}: the connection was closed`
      } else {
        it.state = "done"
        it.text = `${index}: reorganised, ${(shapes[index]?.[0] ?? 0).toLocaleString("en-US")} pages to ${Math.round(((shapes[index]?.[0] ?? 0) * (shapes[index]?.[2] ?? 100)) / 100).toLocaleString("en-US")}, verified`
        done.add(`${b.db}/${index}`)
      }
    } else if (i === stepNow) {
      it.state = "running"
      it.text = "reorganising"
    }
  }
  b.step = Math.min(stepNow, b.items.length)
  const pending = b.items.some((it) => it.state === "waiting" || it.state === "running")
  const run: FixRun = { runId: id, fixId: "reorganize-indexes", database: b.db, startedAt: new Date(b.startedAt).toISOString(), pending, items: structuredClone(b.items), stopping: b.stopping }
  if (!pending) {
    const n = (s: string) => b.items.filter((it) => it.state === s).length
    const parts = [n("done") && `${n("done")} reorganised`, n("failed") && `${n("failed")} failed`, n("stopped") && `${n("stopped")} not done (stopped)`].filter(Boolean)
    run.endedAt = new Date().toISOString()
    run.result = {
      preview: previewFor(b.db, b.options),
      before: `${b.items.length} indexes`,
      after: parts.join(", "),
      verified: n("failed") === 0 && n("stopped") === 0,
      changeId: 9100 + b.items.length,
      appliedAt: run.startedAt,
    } satisfies FixResult
  }
  return run
}

export const mockIndexes: FeatureMock = async (method, path, init) => {
  const lens = lensPath.exec(path)
  if (method === "GET" && lens) {
    const db = decodeURIComponent(lens[2])
    await delay(1500)
    const base: IndexLens = {
      database: db, canChange: true, queryStore: "READ_WRITE", queries: 100, answer: "", rows: [], utcOffsetMinutes: 120,
      measureBatch: 20, gainPct: 5, capGB: 20, handover: "",
    }
    if (db === "Warehouse_Archive") {
      const reason = "Query Store is off in Warehouse_Archive, so kakapo cannot tell which indexes your queries read in full. The Setup card turns it on."
      return { ...base, queryStore: "OFF", queries: 0, answer: reason, reason, handover: reason }
    }
    if (db === "WarehouseTest") {
      const answer = "No query in the last 7 days reads an index of 1,000 pages or more in full. Index work would not help your queries."
      return { ...base, answer, handover: answer }
    }
    const shown = structuredClone(rows).map((r) => {
      if (done.has(`${db}/${r.index}`)) r.last = { at: new Date().toISOString(), result: `${r.index}: reorganised, verified` }
      return r
    })
    const answer = `Your slowest queries read ${shown.length} big indexes in full. Measure them to see which are worth reorganising.`
    return { ...base, rows: shown, answer, handover: answer }
  }

  const shape = shapePath.exec(path)
  if (method === "GET" && shape) {
    const q = new URLSearchParams(shape[3])
    if (!shapes[q.get("index") ?? ""]) return undefined // phase 5q's fixture
    await delay(1000)
    const s = shapeOf(decodeURIComponent(shape[2]), q.get("schema") ?? "dbo", q.get("table") ?? "", q.get("index") ?? "")
    return s
  }

  const fix = fixPath.exec(path)
  if (method === "POST" && fix) {
    const db = decodeURIComponent(fix[2])
    const options = (JSON.parse(String(init?.body ?? "{}")) as { options?: Record<string, string> }).options ?? {}
    await delay(600)
    const preview = previewFor(db, options)
    if (fix[3] === "preview") return preview
    if (preview.changes.length === 0) throw new ApiError(preview.warnings[0] ?? "nothing to reorganise", 409)
    const id = `mockbatch${nextBatch++}`
    const items: RunItem[] = preview.changes.map((c) => ({ label: c.setting, state: "waiting", text: "" }))
    batches.set(id, { db, items, step: 0, startedAt: Date.now(), stopping: false, options })
    const startedAt = new Date().toISOString()
    return { preview, before: `${items.length} indexes`, after: "", verified: false, changeId: 0, appliedAt: startedAt, pending: true, runId: id, startedAt } satisfies FixResult
  }

  const run = runPath.exec(path)
  if (run) {
    const b = batches.get(run[3])
    if (!b) throw new ApiError(`there is no run "${run[3]}"`, 404)
    if (method === "POST" && run[4]) {
      const now = advance(run[3], b)
      if (!now.pending) throw new ApiError("this run has already ended", 409)
      b.stopping = true
      return advance(run[3], b)
    }
    if (method === "GET") return advance(run[3], b)
  }
  return undefined
}
