import { ApiError, type FixPreview, type FixResult } from "./api"
import type { FeatureMock } from "./api.mock"
import type { PerfSample } from "./api-sample"

// Phase 4c (sample): fixture answers for this part's routes; undefined means "not mine". The sample runs
// for its seconds (capped at 5 here so the countdown can be watched); set localStorage "mock.sampleQuiet" to
// "1" for the no-problem verdict, "mock.sampleFails" to "1" for the 502 a login without VIEW SERVER STATE
// gets. The create-index fix answers its preview and apply; "mock.indexBlocked" = "1" shows a blocking guard.

const lastSamples = new Map<string, PerfSample>()

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

function busySample(database: string, seconds: number): PerfSample {
  return {
    database,
    capturedAt: new Date().toISOString(),
    seconds,
    elapsedSeconds: seconds,
    serverName: "WMS-SQL01",
    verdict:
      "Session 83 (AppScheduler, host WMS-APP01) has blocked 14 sessions for 6 minutes. It is running EXEC dbo.usp_Packing_Item @id = 5. It is sleeping with its transaction open: the program that owns it has not committed. Do not kill it until you know who owns it.",
    lines: [
      {
        area: "blocking",
        text: "Session 83 (AppScheduler, host WMS-APP01) has blocked 14 sessions for 6 minutes. It is running EXEC dbo.usp_Packing_Item @id = 5. It is sleeping with its transaction open: the program that owns it has not committed. Do not kill it until you know who owns it.",
      },
      {
        area: "waits",
        text: "Most of the waiting in this sample was blocking (LCK_M_X 60.0 s, PAGEIOLATCH_SH 2.0 s). Find the blocking queries: ACT01 shows who blocks whom right now.",
      },
      {
        area: "queries",
        queryHash: "0x8F3A2C11D4E5B607",
        text: `The query hurting now is dbo.PickNext: 60.0 s of CPU in ${seconds} seconds (25% of the server), 12 runs.`,
      },
    ],
    waits: [
      { type: "LCK_M_X", group: "LOCK", words: "blocking", waitMs: 60000, signalMs: 200, tasks: 30, avgMs: 2000, poison: false },
      { type: "PAGEIOLATCH_SH", group: "IO", words: "reading from disk", waitMs: 2000, signalMs: 100, tasks: 200, avgMs: 10, poison: false },
    ],
    cpu: { sql: 70, other: 8, idle: 22, records: 1, inWindow: true, runnableStart: 2, runnableEnd: 6, schedulers: 8, signalWaitPct: 4.6, hasSignalShare: true },
    queries: [
      {
        queryHash: "0x8F3A2C11D4E5B607",
        database,
        objectName: "dbo.PickNext",
        text: "SELECT TOP (1) p.* FROM dbo.Pick AS p WHERE p.Status = @s ORDER BY p.Date",
        cpuMs: 60000,
        runs: 12,
        reads: 900000,
        durationMs: 68000,
      },
      {
        queryHash: "0x11AA22BB33CC44DD",
        database,
        objectName: "",
        text: "UPDATE dbo.Stock SET Qty = Qty - 1 WHERE Id = @id",
        cpuMs: 2000,
        runs: 3,
        reads: 300,
        durationMs: 2500,
      },
    ],
    files: [
      { database, file: database, type: "ROWS", reads: 100, writes: 12, readMb: 8, writeMb: 0.1, msPerRead: 30, msPerWrite: 2 },
      { database, file: `${database}_log`, type: "LOG", reads: 0, writes: 100, readMb: 0, writeMb: 4, msPerRead: 0, msPerWrite: 3 },
      { database: "tempdb", file: "tempdev", type: "ROWS", reads: 4, writes: 20, readMb: 0.1, writeMb: 1.2, msPerRead: 1, msPerWrite: 1 },
    ],
    blocking: {
      headBlockers: [
        {
          session: 83,
          blocked: 14,
          longestSec: 390,
          database,
          login: "appsvc",
          host: "WMS-APP01",
          program: "AppScheduler",
          status: "sleeping",
          statement: "EXEC dbo.usp_Packing_Item @id = 5",
          bothEnds: true,
        },
      ],
      blocked: 14,
      longestSec: 390,
    },
    running: [],
    growth: [],
    memory: { pleStart: 4000, pleEnd: 3900, pendingStart: 0, pendingEnd: 0 },
  }
}

function quietSample(database: string, seconds: number): PerfSample {
  const s = busySample(database, seconds)
  return {
    ...s,
    verdict: `No problem found in this ${seconds}-second sample. If users still see slowness, the cause is in the code or the indexes: see the costliest queries.`,
    lines: [],
    blocking: { headBlockers: [], blocked: 0, longestSec: 0 },
    waits: s.waits.map((w) => ({ ...w, waitMs: w.waitMs / 100 })),
    queries: s.queries.map((q) => ({ ...q, cpuMs: q.cpuMs / 100 })),
  }
}

function indexPreview(database: string, options: Record<string, string> = {}): FixPreview {
  const blocked = localStorage.getItem("mock.indexBlocked") === "1"
  const name = options.name || "IX_Pick_Status_Date_kakapo"
  const included = options.included ?? "[Qty]"
  const cols = included.replace(/[[\]]/g, "")
  const target = `${name} on dbo.Pick (Status, Date)${cols ? ` include (${cols})` : ""}, about 66 MB, offline`
  const guard = "Index IX_Pick_Status already starts with these columns (Status, Date, Code); widen it instead of adding one."
  return {
    database,
    engineVersion: "16.0.4135.4",
    changes: blocked ? [] : [{ setting: "Index", current: "none", target }],
    statement: blocked
      ? ""
      : `CREATE NONCLUSTERED INDEX [${name}] ON [${database}].[dbo].[Pick] ([Status], [Date])${included ? ` INCLUDE (${included})` : ""} WITH (ONLINE = OFF, SORT_IN_TEMPDB = ON, DATA_COMPRESSION = PAGE);`,
    rollback: blocked ? "" : `DROP INDEX [${name}] ON [${database}].[dbo].[Pick];`,
    warnings: [
      ...(blocked ? [guard] : []),
      "On Standard or Web edition an index is built offline (ONLINE = OFF): while it builds, dbo.Pick (2,000,000 rows, 400 MB) is locked against inserts, updates and deletes, and users who write to it wait until it finishes (reads go on). Apply it in a quiet moment.",
      "Test on the sandbox first when the table is large: the build time and the effect on the queries show there.",
      "Needs the ALTER permission on dbo.Pick (db_ddladmin or db_owner in the database).",
    ],
    inputs: [
      { name: "name", label: "Index name", value: name, kind: "text", help: "kakapo's indexes end in _kakapo, so they are easy to find later." },
      {
        name: "included",
        label: "Included columns",
        value: included,
        kind: "text",
        help: "As SQL Server suggested them. Leave out the columns the query does not need; empty for none.",
      },
    ],
    options: { ...options, name, included },
  }
}

export const mockSample: FeatureMock = async (method, path, init) => {
  const sample = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/cards\/performance\/sample$/)
  if (sample) {
    const key = `${sample[1]}/${sample[2]}`
    const database = decodeURIComponent(sample[2])
    if (method === "GET") return lastSamples.get(key) ?? null
    if (method === "POST") {
      const seconds = (JSON.parse(String(init?.body ?? "{}")) as { seconds?: number }).seconds ?? 30
      if (seconds < 10 || seconds > 120) throw new ApiError(`a sample runs 10 to 120 seconds, not ${seconds}`, 400)
      await delay(Math.min(seconds, 5) * 1000)
      if (localStorage.getItem("mock.sampleFails") === "1") {
        throw new ApiError("could not take the sample on WMS-SQL01: VIEW SERVER STATE permission was denied (the sample needs VIEW SERVER STATE)", 502)
      }
      const s = localStorage.getItem("mock.sampleQuiet") === "1" ? quietSample(database, seconds) : busySample(database, seconds)
      lastSamples.set(key, s)
      return structuredClone(s)
    }
  }

  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/create-index\/(preview|apply)$/)
  if (fix && method === "POST") {
    const database = decodeURIComponent(fix[2])
    const options = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }).options : undefined
    const preview = indexPreview(database, options)
    if (fix[3] === "preview") return preview
    if (preview.changes.length === 0) throw new ApiError(`kakapo does not create this index: ${preview.warnings[0]}`, 409)
    await delay(1500)
    return {
      preview,
      before: "none",
      after: `${preview.options?.name} on dbo.Pick (Status, Date) include (Qty), 61.3 MB`,
      verified: true,
      changeId: 900 + Math.floor(Math.random() * 99),
      appliedAt: new Date().toISOString(),
    } satisfies FixResult
  }
  return undefined
}
