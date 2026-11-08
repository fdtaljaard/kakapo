import { ApiError, type FixPreview, type FixResult } from "./api"
import type { QueryAction, QueryDetail, QueryPlan, QueryTable } from "./api-queries"
import type { FeatureMock } from "./api.mock"

// Phase 4c (queries): fixture answers for this part's routes; undefined means "not mine".
// GET .../queries/{hash}: in WarehouseLive and WarehouseTest, the barcode lookup (0x8F3A2C11D07B9E45, and any
// other hash) has two plans, the one in use six times slower, an implicit conversion, compiled parameters,
// a suggested index and a table with stale statistics, inside dbo.USP_PICKING; the audit update
// (0x51C0D7A2E94B3F18) has one forced plan that keeps failing and waits on locks. In Warehouse_Archive
// (Query Store off) every query comes from the plan cache, with "Turn Query Store on". Forcing, stopping,
// removing a plan and updating statistics change the fixture until the page is reloaded. Set localStorage
// "mock.queryFails" to "1" to see the 502.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
const onTheHour = (h: number) => {
  const d = new Date(Date.now() - h * 3_600_000)
  d.setMinutes(0, 0, 0)
  return d.toISOString()
}

const detailPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/queries\/([^/]+)$/
const fixPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/(force-plan|unforce-plan|evict-plan|update-statistics-table)\/(preview|apply)$/

const AUDIT = "0x51C0D7A2E94B3F18"
const handle15 = "0x060005004A3B2C1D50E1F2A3B4C5D6E7F8091A2B3C4D5E6F000000000000000000"

// What the fixes changed, per database.
const state = {
  forced: new Map<string, number | null>(), // `${db}/${hash}` -> the forced plan
  evicted: new Set<string>(), // plan handles removed
  freshStats: new Set<string>(), // `${db}/${table}`
}
let nextChangeId = 9000

const planXml = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan" Version="1.564" Build="16.0.4255.2"><BatchSequence><Batch><Statements><StmtSimple StatementText="SELECT TOP (1) te.ID ..."><QueryPlan/></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

function action(fixId: string, label: string, title: string, options: Record<string, string>): QueryAction {
  return { fixId, label, title, options }
}

function plan(p: Partial<QueryPlan> & Pick<QueryPlan, "planId">): QueryPlan {
  return {
    queryId: 1873,
    planHash: "0x0000000000000000",
    planHandle: "",
    source: "query-store",
    forced: false,
    active: false,
    fastest: false,
    lastCompile: hoursAgo(30),
    lastRun: hoursAgo(1),
    engineVersion: "16.0.4255.2",
    compatibilityLevel: 160,
    runs: 0,
    avgCpuMs: 0,
    maxCpuMs: 0,
    avgDurationMs: 0,
    maxDurationMs: 0,
    avgReads: 0,
    maxReads: 0,
    tempdbMbPerRun: null,
    forceFailures: 0,
    forceFailureReason: "",
    planTooBig: false,
    conversion: "",
    warnings: [],
    parameters: [],
    missingIndexes: [],
    actions: [],
    ...p,
  }
}

function trackingTable(db: string): QueryTable {
  const fresh = state.freshStats.has(`${db}/[dbo].[StockUnit]`)
  return {
    schema: "dbo",
    table: "StockUnit",
    name: "dbo.StockUnit",
    found: true,
    rows: 4_212_880,
    statistics: 6,
    oldestUpdate: fresh ? new Date().toISOString() : hoursAgo(31 * 24),
    ageDays: fresh ? 0 : 31,
    maxModifiedPct: fresh ? 0 : 48,
    stale: !fresh,
    indexes: ["PK_StockUnit", "IX_StockUnit_Location"],
    action: action("update-statistics-table", "Update statistics", "Update statistics on dbo.StockUnit", {
      table: "[dbo].[StockUnit]",
    }),
  }
}

function locationTable(): QueryTable {
  return {
    schema: "dbo",
    table: "Location",
    name: "dbo.Location",
    found: true,
    rows: 18_430,
    statistics: 3,
    oldestUpdate: hoursAgo(20),
    ageDays: 0,
    maxModifiedPct: 2,
    stale: false,
    indexes: ["PK_Location"],
    action: action("update-statistics-table", "Update statistics", "Update statistics on dbo.Location", {
      table: "[dbo].[Location]",
    }),
  }
}

const createIndex = action("create-index", "Create index", "Create the suggested index on dbo.StockUnit", {
  source: "plan",
  planId: "15",
  table: "[WarehouseLive].[dbo].[StockUnit]",
  equality: "[Barcode], [InStock]",
  inequality: "",
  included: "[Qty], [LocationID]",
})

function hours(plans: { id: number; runs: number; cpu: number; avg: number; reads: number; from: number; to: number }[]) {
  const out: QueryDetail["history"] = []
  for (let h = 0; h < 7 * 24; h += 3) {
    const ran = plans.filter((p) => h >= p.to && h < p.from)
    if (ran.length === 0) continue
    const wave = 0.5 + Math.abs(Math.sin(h / 7))
    const runs = Math.round(ran.reduce((n, p) => n + p.runs * wave, 0))
    const cpuMs = Math.round(ran.reduce((n, p) => n + p.runs * wave * p.cpu, 0))
    const durationMs = Math.round(ran.reduce((n, p) => n + p.runs * wave * p.avg, 0))
    out.push({
      hour: onTheHour(h),
      runs,
      cpuMs,
      durationMs,
      avgDurationMs: Math.round((durationMs / Math.max(1, runs)) * 10) / 10,
      reads: Math.round(ran.reduce((n, p) => n + p.runs * wave * p.reads, 0)),
      plans: ran.map((p) => p.id),
    })
  }
  return out
}

function barcodeDetail(db: string, hash: string): QueryDetail {
  const key = `${db}/${hash}`
  const forced = state.forced.get(key) ?? null
  const evicted = state.evicted.has(handle15)
  const p15 = plan({
    planId: 15,
    planHash: "0x6B2D4E8A10F3C5B7",
    planHandle: evicted ? "" : handle15,
    active: forced !== 12,
    forced: forced === 15,
    lastCompile: hoursAgo(28),
    lastRun: hoursAgo(0.2),
    runs: 1240,
    avgCpuMs: 410,
    maxCpuMs: 9800,
    avgDurationMs: 612,
    maxDurationMs: 41_200,
    avgReads: 48_211,
    maxReads: 61_870,
    tempdbMbPerRun: 2.1,
    conversion: "CONVERT_IMPLICIT(nvarchar(50),[te].[Barcode],0)=[@Barcode]",
    warnings: [
      "key lookups (the index used does not hold every column needed)",
      "SQL Server suggests an index for it (check IDX02 and IDX09 before creating one)",
    ],
    parameters: [
      { name: "@Barcode", dataType: "varchar(50)", compiledValue: "'TE0000001'", runtimeValue: "" },
      { name: "@SiteID", dataType: "int", compiledValue: "1", runtimeValue: "" },
    ],
    missingIndexes: [
      {
        table: "[WarehouseLive].[dbo].[StockUnit]",
        tableName: "dbo.StockUnit",
        equality: "[Barcode], [InStock]",
        inequality: "",
        included: "[Qty], [LocationID]",
        impact: 91.4,
        action: createIndex,
      },
    ],
    planXml,
    actions: [
      forced === 15
        ? action("unforce-plan", "Stop forcing", "Stop forcing plan 15", { planId: "15" })
        : action("force-plan", "Force this plan", "Force plan 15", { planId: "15" }),
      ...(evicted
        ? []
        : [action("evict-plan", "Remove from cache", "Remove the plan from the cache", { planHandle: handle15, queryHash: hash })]),
    ],
  })
  const p12 = plan({
    planId: 12,
    planHash: "0x19C0FFEE2A4B6D8E",
    active: forced === 12,
    forced: forced === 12,
    fastest: true,
    lastCompile: hoursAgo(9 * 24),
    lastRun: forced === 12 ? hoursAgo(0.1) : hoursAgo(29),
    runs: 10_000,
    avgCpuMs: 61,
    maxCpuMs: 950,
    avgDurationMs: 98,
    maxDurationMs: 2_040,
    avgReads: 3_120,
    maxReads: 9_400,
    tempdbMbPerRun: 0,
    parameters: [{ name: "@Barcode", dataType: "varchar(50)", compiledValue: "'P18823'", runtimeValue: "" }],
    planXml,
    actions: [
      forced === 12
        ? action("unforce-plan", "Stop forcing", "Stop forcing plan 12", { planId: "12" })
        : action("force-plan", "Force this plan", "Force plan 12", { planId: "12" }),
    ],
  })
  const plans = forced === 12 ? [p12, p15] : [p15, p12]
  const tables = [trackingTable(db), locationTable()]
  const explain: QueryDetail["explain"] = [
    { text: "It ran 11,240 times in 7 days, 155 ms each on average; the slowest run took 41 s." },
    { text: "Each run uses 99 ms of CPU and reads 7,941 pages (62 MB) from memory or disk." },
  ]
  if (forced === 12) {
    explain.push({ text: "Plan 12 is forced: SQL Server must use it." })
  } else {
    explain.push({
      text: "Two plans: the one in use since yesterday 09:00 is 6.2x slower than plan 12 (612 ms against 98 ms on average). Force plan 12 so SQL Server uses the faster one.",
      action: action("force-plan", "Force this plan", "Force plan 12", { planId: "12" }),
    })
  }
  explain.push(
    {
      text: "Its parameters were compiled with @Barcode = 'TE0000001' and @SiteID = 1, which may not be typical. Its runs swing from 612 ms on average to 41 s: a plan built for one value is reused for values it does not suit (parameter sniffing).".concat(
        evicted ? "" : " Remove the plan from the cache so the next run compiles for its own values.",
      ),
      action: evicted
        ? undefined
        : action("evict-plan", "Remove from cache", "Remove the plan from the cache", { planHandle: handle15, queryHash: hash }),
    },
    {
      text: "Its plan converts a column's type for every row (CONVERT_IMPLICIT(nvarchar(50),[te].[Barcode],0)=[@Barcode]), which can stop SQL Server using an index: a variable, parameter or join column has a different type from the column. The fix is in the code: Copy for the vendor.",
    },
    { text: "Its plan shows key lookups (the index used does not hold every column needed)." },
    {
      text: "SQL Server suggests an index on dbo.StockUnit (Barcode, InStock) that it estimates would cut this query's cost by 91%. Create the index: the preview checks the existing indexes first.",
      action: createIndex,
    },
  )
  if (tables[0].stale) {
    explain.push({
      text: "It reads dbo.StockUnit (4.2 M rows) whose statistics are 31 days old with 48% of rows changed. Update statistics on it.",
      action: tables[0].action,
    })
  }
  explain.push({
    text: "In dbo.USP_PICKING, the statement on line 41 does 81% of the procedure's page reads; it is this query.",
  })
  const detail: QueryDetail = {
    database: db,
    queryHash: hash,
    serverName: "CLIENTSQL01\\WMS",
    capturedAt: new Date().toISOString(),
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
    text: "SELECT TOP (1) te.ID, te.Barcode, te.Qty, te.LocationID, l.Name AS Location\nFROM dbo.StockUnit te\nJOIN dbo.Location l ON l.ID = te.LocationID\nWHERE te.Barcode = @Barcode AND te.InStock = 1\nORDER BY te.ID DESC",
    objectName: "dbo.USP_PICKING",
    source: "query-store",
    queryStoreState: "READ_WRITE",
    summary: { window: "7 days", runs: 11_240, avgDurationMs: 155.2, maxDurationMs: 41_200, avgCpuMs: 99.5, avgReads: 7_941, maxReads: 61_870, lastRun: hoursAgo(0.2) },
    verdict: null,
    events: [],
    explain,
    plans,
    history: hours([
      { id: 15, runs: 22, cpu: 410, avg: 612, reads: 48_211, from: 29, to: 0 },
      { id: 12, runs: 180, cpu: 61, avg: 98, reads: 3_120, from: 7 * 24, to: 29 },
    ]),
    waits: [
      { category: "CPU", words: "waiting for a CPU (the server is busy)", waitMs: 812_000, pct: 61.2 },
      { category: "Buffer IO", words: "reading data pages from disk", waitMs: 401_200, pct: 30.2 },
      { category: "Lock", words: "waiting for locks held by other sessions (blocking)", waitMs: 114_100, pct: 8.6 },
    ],
    statements: [
      {
        line: 41,
        text: "SELECT TOP (1) te.ID, te.Barcode, te.Qty, te.LocationID, l.Name AS Location FROM dbo.StockUnit te JOIN dbo.Location l ON l.ID = te.LocationID WHERE te.Barcode = @Barcode AND te.InStock = 1 ORDER BY te.ID DESC",
        runs: 1_240,
        minCpuMs: 12,
        avgCpuMs: 410,
        maxCpuMs: 9_800,
        minReads: 41_880,
        avgReads: 48_211,
        maxReads: 61_870,
        pctReads: 81.3,
        thisQuery: true,
        lookAt: ["most of the cost of this procedure", "implicit conversion may stop an index being used (a variable or column of a different type)", "reads many pages every run"],
        lastRun: hoursAgo(0.2),
      },
      {
        line: 88,
        text: "UPDATE dbo.PickList SET Status = 'PICKED', PickedQty = @Qty WHERE PickListID = @PickListID",
        runs: 1_240,
        minCpuMs: 0.4,
        avgCpuMs: 1.1,
        maxCpuMs: 30,
        minReads: 9,
        avgReads: 11,
        maxReads: 44,
        pctReads: 0.2,
        thisQuery: false,
        lookAt: [],
        lastRun: hoursAgo(0.2),
      },
      {
        line: 12,
        text: "SELECT @SiteID = SiteID FROM dbo.Users WHERE UserID = @UserID",
        runs: 1_240,
        minCpuMs: 0.1,
        avgCpuMs: 0.2,
        maxCpuMs: 4,
        minReads: 3,
        avgReads: 3,
        maxReads: 3,
        pctReads: 0.1,
        thisQuery: false,
        lookAt: [],
        lastRun: hoursAgo(0.2),
      },
    ],
    tables,
    notes: [],
    handover: "",
  }
  detail.handover = handover(detail)
  return detail
}

function auditDetail(db: string, hash: string): QueryDetail {
  const forced = state.forced.has(`${db}/${hash}`) ? state.forced.get(`${db}/${hash}`) : 31
  const p31 = plan({
    planId: 31,
    queryId: 2204,
    planHash: "0x77AA00BB11CC22DD",
    active: true,
    forced: forced === 31,
    forceFailures: forced === 31 ? 14 : 0,
    forceFailureReason: forced === 31 ? "NO_INDEX" : "",
    runs: 168,
    avgCpuMs: 19_100,
    maxCpuMs: 19_604,
    avgDurationMs: 64_000,
    maxDurationMs: 212_000,
    avgReads: 2_904_118,
    maxReads: 2_931_775,
    parameters: [{ name: "@Cutoff", dataType: "datetime", compiledValue: "'2026-08-01 00:00:00.000'", runtimeValue: "" }],
    actions: [
      forced === 31
        ? action("unforce-plan", "Stop forcing", "Stop forcing plan 31", { planId: "31" })
        : action("force-plan", "Force this plan", "Force plan 31", { planId: "31" }),
    ],
  })
  const explain: QueryDetail["explain"] = [
    { text: "It ran 168 times in 7 days, 64 s each on average; the slowest run took 3.5 min." },
    { text: "Each run uses 19 s of CPU and reads 2,904,118 pages (22,688 MB) from memory or disk." },
    { text: "One plan in 7 days: the plan is stable, so the cost is in the query itself or its indexes." },
  ]
  if (forced === 31) {
    explain.push({
      text: "Plan 31 is forced: SQL Server must use it. Forcing it has failed 14 times (no_index), so SQL Server may be compiling its own plan instead.",
      action: action("unforce-plan", "Stop forcing", "Stop forcing plan 31", { planId: "31" }),
    })
  }
  explain.push({
    text: "Most of its waiting (78%) is spent waiting for locks held by other sessions (blocking). Open Blocking from the Performance card to see who holds them.",
  })
  const detail: QueryDetail = {
    database: db,
    queryHash: hash,
    serverName: "CLIENTSQL01\\WMS",
    capturedAt: new Date().toISOString(),
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
    text: "UPDATE dbo.StockUnitAudit SET Archived = 1 WHERE AuditDate < @Cutoff AND Archived = 0",
    objectName: "",
    source: "query-store",
    queryStoreState: "READ_WRITE",
    summary: { window: "7 days", runs: 168, avgDurationMs: 64_000, maxDurationMs: 212_000, avgCpuMs: 19_100, avgReads: 2_904_118, maxReads: 2_931_775, lastRun: hoursAgo(1) },
    verdict: null,
    events: [],
    explain,
    plans: [p31],
    history: hours([{ id: 31, runs: 3, cpu: 19_100, avg: 64_000, reads: 2_904_118, from: 7 * 24, to: 0 }]),
    waits: [
      { category: "Lock", words: "waiting for locks held by other sessions (blocking)", waitMs: 7_420_000, pct: 78.1 },
      { category: "Tran Log IO", words: "writing to the transaction log", waitMs: 1_610_000, pct: 16.9 },
      { category: "Buffer IO", words: "reading data pages from disk", waitMs: 475_000, pct: 5 },
    ],
    statements: [],
    tables: [
      {
        schema: "dbo",
        table: "StockUnitAudit",
        name: "dbo.StockUnitAudit",
        found: true,
        rows: 61_004_211,
        statistics: 2,
        oldestUpdate: hoursAgo(4 * 24),
        ageDays: 4,
        maxModifiedPct: 6,
        stale: false,
        indexes: ["PK_StockUnitAudit"],
        action: action("update-statistics-table", "Update statistics", "Update statistics on dbo.StockUnitAudit", {
          table: "[dbo].[StockUnitAudit]",
        }),
        // Phase 5o: a safe copy from the Design card's last read (IDX04).
        copies: [
          {
            text: "IX_Audit_Unit_Copy is a copy of IX_Audit_Unit, which has the same keys and every one of its columns (IDX04 on the Design card).",
            action: action("disable-index", "Disable index", "Disable IX_Audit_Unit_Copy", {
              table: "[dbo].[StockUnitAudit]",
              index: "IX_Audit_Unit_Copy",
              keeper: "IX_Audit_Unit",
            }),
          },
        ],
      },
    ],
    notes: [],
    handover: "",
  }
  detail.handover = handover(detail)
  return detail
}

function planCacheDetail(db: string, hash: string): QueryDetail {
  const handle = "0x0600070011AA22BB33CC44DD55EE66FF0000000000000000000000000000000000"
  const evicted = state.evicted.has(handle)
  const detail: QueryDetail = {
    database: db,
    queryHash: hash,
    serverName: "CLIENTSQL01\\WMS",
    capturedAt: new Date().toISOString(),
    utcOffsetMinutes: -new Date().getTimezoneOffset(),
    text: "SELECT a.ID, a.Payload FROM dbo.ArchiveLog a WHERE a.Created >= @From ORDER BY a.Created",
    objectName: "",
    source: evicted ? "none" : "plan-cache",
    queryStoreState: "OFF",
    summary: evicted
      ? { window: "since the plan was cached", runs: 0, avgDurationMs: 0, maxDurationMs: 0, avgCpuMs: 0, avgReads: 0, maxReads: 0, lastRun: null }
      : { window: "since 06:10", runs: 412, avgDurationMs: 840, maxDurationMs: 9_100, avgCpuMs: 610, avgReads: 88_400, maxReads: 120_000, lastRun: hoursAgo(0.5) },
    verdict: null,
    events: [],
    explain: evicted
      ? [
          {
            text: `Nothing is known about this query in ${db} now: it is not in Query Store for the last 7 days, nor in the plan cache. It may not have run since the server restarted.`,
          },
        ]
      : [
          { text: "It ran 412 times since 06:10, 840 ms each on average; the slowest run took 9.1 s." },
          { text: "Each run uses 610 ms of CPU and reads 88,400 pages (691 MB) from memory or disk." },
          {
            text: `Query Store is off for ${db}, so these figures come from the plan cache since 06:10, with no history. Turn Query Store on for 7 days of history.`,
            action: action("query-store", "Turn Query Store on", "Apply recommended Query Store settings", {}),
          },
        ],
    plans: evicted
      ? []
      : [
          plan({
            planId: 0,
            queryId: 0,
            source: "plan-cache",
            planHash: "0x5E5E5E5E00000001",
            planHandle: handle,
            active: true,
            runs: 412,
            avgCpuMs: 610,
            maxCpuMs: 7_400,
            avgDurationMs: 840,
            maxDurationMs: 9_100,
            avgReads: 88_400,
            maxReads: 120_000,
            actions: [action("evict-plan", "Remove from cache", "Remove the plan from the cache", { planHandle: handle, queryHash: hash })],
          }),
        ],
    history: [],
    waits: [],
    statements: [],
    tables: [],
    notes: [],
    handover: "",
  }
  detail.handover = handover(detail)
  return detail
}

function handover(d: QueryDetail): string {
  const lines = [`Query ${d.queryHash} in ${d.database} on ${d.serverName}`]
  if (d.objectName) lines.push(`Procedure: ${d.objectName}`)
  lines.push("", "What we see", ...d.explain.map((l) => `- ${l.text}`))
  if (d.plans.length > 0) {
    lines.push("", "Plans")
    for (const p of d.plans) {
      const marks = [p.active && "in use", p.forced && "forced", p.fastest && "fastest"].filter(Boolean).join(", ")
      lines.push(`- ${p.planId > 0 ? `plan ${p.planId}` : `cached plan ${p.planHash}`}${marks ? ` (${marks})` : ""}: ${p.runs} runs, ${p.avgDurationMs} ms on average`)
    }
    const active = d.plans.find((p) => p.active)
    if (active?.parameters.length) {
      lines.push("", "Compiled with", ...active.parameters.map((x) => `- ${x.name} (${x.dataType}) = ${x.compiledValue}`))
    }
  }
  lines.push("", "Query text", d.text)
  return lines.join("\n")
}

function detailFor(db: string, hash: string): QueryDetail {
  if (db === "Warehouse_Archive") return planCacheDetail(db, hash)
  if (hash === AUDIT) return auditDetail(db, hash)
  return barcodeDetail(db, hash)
}

// The previews and applies of the four fixes, built from the fixture as the backend builds them.
function fixPreview(db: string, fixId: string, options: Record<string, string>): FixPreview {
  const q = (s: string) => `[${s.replace(/]/g, "]]")}]`
  const base: FixPreview = { database: db, engineVersion: "16.0.4255.2", changes: [], statement: "", rollback: "", warnings: [], options }
  switch (fixId) {
    case "force-plan":
    case "unforce-plan": {
      const id = Number(options.planId)
      if (!Number.isInteger(id) || id <= 0) throw new ApiError(`planId must be a Query Store plan id (a positive whole number), not "${options.planId ?? ""}"`, 400)
      const hash = id === 31 ? AUDIT : "0x8F3A2C11D07B9E45"
      const key = `${db}/${hash}`
      const forced = state.forced.has(key) ? state.forced.get(key) : id === 31 ? 31 : null
      const qid = id === 31 ? 2204 : 1873
      const force = `EXEC ${q(db)}.sys.sp_query_store_force_plan @query_id = ${qid}, @plan_id = ${id};`
      const unforce = `EXEC ${q(db)}.sys.sp_query_store_unforce_plan @query_id = ${qid}, @plan_id = ${id};`
      const permission = `sys.sp_query_store_force_plan and sp_query_store_unforce_plan need ALTER on the database ${db}.`
      if (fixId === "force-plan") {
        if (forced === id) throw new ApiError(`nothing to apply: plan ${id} is already forced for query ${qid}`, 404)
        const avg = id === 12 ? "avg 98 ms" : id === 15 ? "avg 612 ms" : "avg 64,000 ms"
        const other = id === 12 ? "plan 15 in use, not forced, avg 612 ms" : "plan 12 in use, not forced, avg 98 ms"
        return {
          ...base,
          changes: [{ setting: `Plan for query ${qid}`, current: id === 31 ? "no plan forced" : other, target: `plan ${id} forced, ${avg}` }],
          statement: force,
          rollback: unforce,
          warnings: [
            id === 12
              ? "Force plan 12 for this query: avg 98 ms against 612 ms for the plan now in use (plan 15)."
              : `Force plan ${id} for this query: ${avg}.`,
            ...(id === 15 ? ["Plan 15 is not faster than plan 12 on average: forcing it will not make the query faster."] : []),
            "Forcing hides regressions: SQL Server keeps this plan even when the data changes so that another plan would be faster. Look at this query's page again after a business day.",
            "Stop forcing it when the code, the indexes or the SQL Server version change (Stop forcing on this query's page).",
            permission,
          ],
        }
      }
      if (forced !== id) throw new ApiError(`nothing to apply: plan ${id} is not forced for query ${qid}`, 404)
      return {
        ...base,
        changes: [{ setting: `Plan for query ${qid}`, current: `plan ${id} forced`, target: "no plan forced" }],
        statement: unforce,
        rollback: force,
        warnings: [
          `Stop forcing plan ${id} for this query.`,
          "SQL Server goes back to choosing the plan itself; its next compile may pick a slower plan again.",
          permission,
        ],
      }
    }
    case "evict-plan": {
      const h = options.planHandle ?? ""
      if (!/^0x[0-9a-f]{2,128}$/i.test(h) || h.length % 2 !== 0) throw new ApiError(`planHandle must be a plan handle, not "${h}"`, 400)
      if (state.evicted.has(h)) {
        throw new ApiError("nothing to apply: this plan is no longer in the plan cache: SQL Server already compiles a fresh one on the next run", 404)
      }
      return {
        ...base,
        database: "",
        options: { planHandle: h, ...(options.queryHash ? { queryHash: options.queryHash } : {}) },
        changes: [{ setting: `Cached plan ${h.slice(0, 18)}…`, current: "in the cache since 2026-11-03 06:10, used 412 times", target: "removed from the cache" }],
        statement: `DBCC FREEPROCCACHE (${h});`,
        warnings: [
          "Remove this plan from the cache so SQL Server compiles a fresh one. Harmless; the next run compiles.",
          ...(h === handle15 ? ["The plan is the whole procedure dbo.USP_PICKING: every statement in it compiles again on its next run."] : []),
          "If the fresh plan is just as slow, the plan was not the problem: look at the query and its indexes.",
          "Only this plan is removed, not the whole plan cache.",
          "No rollback: a removed plan cannot be put back, and none is needed.",
          "DBCC FREEPROCCACHE needs the ALTER SERVER STATE permission on the server (sysadmin or serveradmin).",
        ],
      }
    }
    default: {
      const table = options.table ?? ""
      const m = /^\[(.+)\]\.\[(.+)\]$/.exec(table)
      if (!m) throw new ApiError(`table must name one table as schema.table, not "${table}"`, 400)
      const name = `${m[1]}.${m[2]}`
      const big = name === "dbo.StockUnitAudit"
      const rows = big ? "61.0 M" : name === "dbo.StockUnit" ? "4.2 M" : "18,430"
      const fresh = state.freshStats.has(`${db}/${table}`)
      return {
        ...base,
        changes: [
          {
            setting: `Statistics on ${name}`,
            current: fresh
              ? `6 statistics, ${rows} rows, oldest updated today (0 days ago), 0% of rows changed`
              : `6 statistics, ${rows} rows, oldest updated 2026-10-03 (31 days ago), 48% of rows changed`,
            target: big ? "updated now, 25% sample" : "updated now, full scan",
          },
        ],
        statement: `UPDATE STATISTICS ${table} ${big ? "WITH SAMPLE 25 PERCENT" : "WITH FULLSCAN"};`,
        warnings: [
          big
            ? `${name} has ${rows} rows: over 10 million rows a 25% sample is read instead of the whole table; it still takes minutes and competes with users for the disk, so run it outside peak hours.`
            : name === "dbo.Location"
              ? `${name} has ${rows} rows: this takes seconds.`
              : `${name} has ${rows} rows: a full scan reads the whole table, takes minutes and competes with users for the disk, so run it outside peak hours.`,
          "It runs online: users keep working; queries that use the table compile a new plan on their next run.",
          "No rollback: updated statistics are never worse than stale ones.",
          "Needs ALTER on the table (db_owner or db_ddladmin).",
        ],
      }
    }
  }
}

async function applyFix(db: string, fixId: string, options: Record<string, string>): Promise<FixResult> {
  const preview = fixPreview(db, fixId, options)
  await delay(fixId === "update-statistics-table" ? 2500 : 900)
  let after = ""
  switch (fixId) {
    case "force-plan":
    case "unforce-plan": {
      const id = Number(options.planId)
      const hash = id === 31 ? AUDIT : "0x8F3A2C11D07B9E45"
      state.forced.set(`${db}/${hash}`, fixId === "force-plan" ? id : null)
      after = fixId === "force-plan" ? `plan ${id} forced` : `plan ${id} not forced`
      break
    }
    case "evict-plan":
      state.evicted.add(options.planHandle)
      after = "removed from the plan cache"
      break
    default:
      state.freshStats.add(`${db}/${options.table}`)
      after = `oldest statistics updated ${new Date().toISOString().slice(0, 19).replace("T", " ")}`
  }
  return {
    preview,
    before: preview.changes[0]?.current ?? "",
    after,
    verified: true,
    changeId: nextChangeId++,
    appliedAt: new Date().toISOString(),
  }
}

export const mockQueries: FeatureMock = async (method, path, init) => {
  const detail = detailPath.exec(path)
  if (method === "GET" && detail) {
    const db = decodeURIComponent(detail[2])
    const hash = decodeURIComponent(detail[3])
    await delay(1200) // the real read connects, reads Query Store, the plan cache and the tables
    if (!/^0x[0-9a-f]{16}$/i.test(hash)) {
      throw new ApiError(`"${hash}": a query hash is 0x followed by 16 hexadecimal digits, as the finding shows it`, 400)
    }
    if (localStorage.getItem("mock.queryFails") === "1") {
      throw new ApiError("could not read the query on CLIENTSQL01\\WMS: the login does not have VIEW SERVER STATE", 502)
    }
    return structuredClone(detailFor(db, "0x" + hash.slice(2).toUpperCase()))
  }
  const fix = fixPath.exec(path)
  if (method === "POST" && fix) {
    const db = decodeURIComponent(fix[2])
    const body = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }) : {}
    const options = body.options ?? {}
    if (fix[4] === "preview") return structuredClone(fixPreview(db, fix[3], options))
    return structuredClone(await applyFix(db, fix[3], options))
  }
  return undefined
}
