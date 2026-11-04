import { ApiError, type FixPreview, type FixResult } from "./api"
import type { BlockingNode, BlockingTree } from "./api-blocking"
import type { FeatureMock } from "./api.mock"

// Phase 4c (blocking): fixture answers for this part's routes; undefined means "not mine".
// Two chains: session 83 (GraniteScheduler, sleeping with a transaction open 6 minutes) blocks 91 and 95,
// and 91 blocks 102; session 64, a SQL Agent job under SERIALIZABLE, has run one INSERT for 4 minutes and
// blocks 70. The job's chain clears after the fourth read, so the watch shows "Cleared at"; killing 83
// clears the other; turning READ COMMITTED SNAPSHOT on hides its button. Session 12 (a system session)
// blocks nobody here, but the kill preview for it shows the refusal.

let reads = 0
let killed83 = false
let rcsiOn = false

const blockingPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/blocking$/
const fixPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/(kill-session|rcsi)\/(preview|apply)$/

function node(n: Partial<BlockingNode> & Pick<BlockingNode, "session">): BlockingNode {
  return {
    blockedBy: 0,
    login: "",
    host: "",
    program: "",
    status: "suspended",
    isUser: true,
    database: "",
    statement: "",
    statementIsLast: false,
    waitMs: 0,
    isolation: "READ COMMITTED",
    openTran: 0,
    logBytes: 0,
    tranSeconds: 0,
    requestSeconds: 0,
    sleeping: false,
    kinds: [],
    behind: 0,
    longestWaitSec: 0,
    canKill: true,
    victims: [],
    ...n,
  }
}

function tree(database: string): BlockingTree {
  reads++
  const tick = reads * 3 // the fixture's clock moves with the watch's polls
  const heads: BlockingNode[] = []
  if (!killed83) {
    heads.push(
      node({
        session: 83,
        login: "granite",
        host: "WMS-APP01",
        program: "GraniteScheduler",
        status: "sleeping",
        database,
        statement: "UPDATE dbo.MasterItem SET LastCounted = GETDATE() WHERE MasterItemID = @id",
        objectName: "dbo.Prescript_Packing_MasterItem",
        statementIsLast: true,
        openTran: 1,
        tranName: "user_transaction",
        logBytes: 125829120,
        tranSeconds: 362 + tick,
        sleeping: true,
        kinds: ["sleeping"],
        behind: 3,
        longestWaitSec: 300 + tick,
        victims: [
          node({
            session: 91,
            blockedBy: 83,
            login: "scanner",
            host: "HH12",
            program: "Granite Scanner",
            command: "UPDATE",
            database,
            statement: "UPDATE dbo.MasterItem SET Qty = @q WHERE MasterItemID = @id",
            waitType: "LCK_M_U",
            waitMs: (300 + tick) * 1000,
            waitResource: "KEY: 7:72057594043236352 (8194443284a0)",
            resource: "dbo.MasterItem (PK_MasterItem)",
            resourceTable: "dbo.MasterItem",
            lockMode: "wants U, held X",
            openTran: 1,
            logBytes: 8192,
            tranSeconds: 301 + tick,
            requestSeconds: 300 + tick,
            behind: 1,
            longestWaitSec: 120 + tick,
            victims: [
              node({
                session: 102,
                blockedBy: 91,
                login: "scanner",
                host: "HH07",
                program: "Granite Scanner",
                command: "SELECT",
                database,
                statement: "SELECT Qty FROM dbo.MasterItem WHERE MasterItemID = @id",
                waitType: "LCK_M_S",
                waitMs: (120 + tick) * 1000,
                waitResource: "KEY: 7:72057594043236352 (8194443284a0)",
                resource: "dbo.MasterItem (PK_MasterItem)",
                resourceTable: "dbo.MasterItem",
                lockMode: "wants S, held U",
                requestSeconds: 120 + tick,
              }),
            ],
          }),
          node({
            session: 95,
            blockedBy: 83,
            login: "report",
            host: "BI01",
            program: "Report Server",
            command: "SELECT",
            database,
            statement: "SELECT * FROM dbo.MasterItem",
            waitType: "LCK_M_S",
            waitMs: (240 + tick) * 1000,
            waitResource: "KEY: 7:72057594043236352 (8194443284a0)",
            resource: "dbo.MasterItem (PK_MasterItem)",
            resourceTable: "dbo.MasterItem",
            lockMode: "wants S, held X",
            requestSeconds: 240 + tick,
          }),
        ],
      }),
    )
  }
  const jobRunning = reads <= 4
  if (jobRunning) {
    heads.push(
      node({
        session: 64,
        login: "WMS\\svc-agent",
        host: "SQL01",
        program: "SQLAgent - TSQL JobStep (Job 0x9A3F11C04B6E2D49A1F07E53D1C2B8A4 : Step 1)",
        jobName: "Nightly stock import",
        status: "running",
        command: "INSERT",
        database,
        statement: "INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock",
        isolation: "SERIALIZABLE",
        openTran: 1,
        tranName: "user_transaction",
        logBytes: 52428800,
        tranSeconds: 250 + tick,
        requestSeconds: 240 + tick,
        kinds: ["serializable", "long-running"],
        behind: 1,
        longestWaitSec: 45 + tick,
        victims: [
          node({
            session: 70,
            blockedBy: 64,
            login: "granite",
            host: "WMS-APP02",
            program: "Granite Integration",
            command: "UPDATE",
            database,
            statement: "UPDATE dbo.Stock SET Qty = Qty - 1 WHERE ItemID = @id",
            waitType: "LCK_M_IX",
            waitMs: (45 + tick) * 1000,
            waitResource: "OBJECT: 7:1977058079:0 ",
            resource: "dbo.Stock",
            resourceTable: "dbo.Stock",
            lockMode: "wants IX, held X",
            requestSeconds: 45 + tick,
          }),
        ],
      }),
    )
  }
  const minutes = (s: number) => `${Math.round(s / 60)} minutes`
  const explain: string[] = []
  const blocked = heads.reduce((n, h) => n + h.behind, 0)
  if (heads.length === 0) {
    explain.push("No session is blocking another right now. Blocking often lasts only seconds: Watch for 30 seconds to catch it.")
  } else {
    explain.push(
      `Right now ${blocked === 1 ? "1 session waits" : `${blocked} sessions wait`} in ${
        heads.length === 1 ? "1 blocking chain" : `${heads.length} blocking chains`
      }.`,
    )
  }
  if (!killed83) {
    const h = heads[0]
    explain.push(
      `Session 83 (GraniteScheduler on WMS-APP01) has held a lock on dbo.MasterItem for ${minutes(h.tranSeconds)} in a transaction that is still open and is doing nothing (sleeping); 3 sessions wait behind it, the longest for ${minutes(h.longestWaitSec)}.`,
      `A sleeping session with an open transaction is usually an application that did not commit, or an SSMS window left open. Ask granite on WMS-APP01 to commit or close it; Kill session 83 ends it and undoes its work (the rollback can take up to ${minutes(h.tranSeconds)}).`,
    )
  }
  if (jobRunning) {
    const j = heads[heads.length - 1]
    explain.push(
      `Session 64 (SQL Agent job Nightly stock import on SQL01) has held a lock on dbo.Stock for ${minutes(j.tranSeconds)} while one statement has run for ${minutes(j.requestSeconds)}: "INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock"; 1 session waits behind it.`,
      "Session 64 uses the SERIALIZABLE isolation level, which locks ranges of rows and blocks far more than the default (.NET's TransactionScope picks it unless told otherwise). Copy the chain for the vendor.",
      `Its statement has run for ${minutes(j.requestSeconds)}. If it should be quicker, Copy it for the vendor; Kill session 64 undoes everything it did so far, which can take as long again.`,
    )
  }
  if (!killed83 && !rcsiOn) {
    explain.push(
      `2 sessions are reading and wait for a writer in ${database}. READ COMMITTED SNAPSHOT lets readers see the last committed rows instead of waiting: Turn on READ COMMITTED SNAPSHOT below.`,
    )
  }
  const lines = (n: BlockingNode, depth: number): string[] => [
    `${"  ".repeat(depth)}Session ${n.session}: ${n.login}, ${n.jobName ? `SQL Agent job ${n.jobName}` : n.program} on ${n.host}; ${n.status}, database ${n.database}; ${n.isolation}${
      n.waitType ? `; waits on ${n.waitType} for ${n.resource}` : ""
    }; ${n.statementIsLast ? "last ran" : "runs"} ${n.statement}`,
    ...n.victims.flatMap((v) => lines(v, depth + 1)),
  ]
  const handover = [
    `Blocking chain on SQL01\\WMS (database ${database})`,
    "",
    ...explain,
    ...(heads.length ? ["", "The chain (each session under the one it waits for)", ...heads.flatMap((h) => lines(h, 0))] : []),
  ].join("\n")
  return {
    capturedAt: new Date().toISOString(),
    serverName: "SQL01\\WMS",
    database,
    engineVersion: "16.0.4135.4",
    rcsi: rcsiOn,
    offerRcsi: !rcsiOn,
    blocked,
    heads,
    explain,
    notes: [],
    handover,
  }
}

function killPreview(database: string, sessionId: string): FixPreview {
  if (sessionId === "12") {
    return {
      database: "",
      engineVersion: "16.0.4135.4",
      changes: [],
      statement: "",
      rollback: "",
      warnings: [
        "Session 12 is a SQL Server system session: SQL Server needs it, so kakapo does not kill it.",
        "Session 12: login sa, background.",
      ],
      options: { sessionId },
    }
  }
  if (sessionId === "64") {
    return {
      database,
      engineVersion: "16.0.4135.4",
      changes: [
        {
          setting: "Session 64",
          current: "running INSERT INTO dbo.Stock (ItemID, Qty) SELECT ItemID, Qty FROM staging.Stock",
          target: "killed",
        },
      ],
      statement: "KILL 64;",
      rollback: "",
      warnings: [
        `Session 64: login WMS\\svc-agent on SQL01, program SQLAgent - TSQL JobStep (Job 0x9A3F11C04B6E2D49A1F07E53D1C2B8A4 : Step 1), database ${database}, running. 1 session waits behind it.`,
        "It is the SQL Agent job Nightly stock import: this run of the job fails, and the job's history says so.",
        "Its transaction has been open for 4 minutes and has written 50.0 MB of log.",
        "Its work is rolled back: everything that transaction changed is undone.",
        "The rollback takes about as long as the work did: up to about 4 minutes here, and the sessions behind it keep waiting until it ends.",
        "Tell whoever owns it (WMS\\svc-agent on SQL01) before you kill it.",
        "Needs the ALTER ANY CONNECTION permission (sysadmin or processadmin).",
      ],
      options: { sessionId, loginTime: "2026-11-03T09:14:00.000" },
    }
  }
  return {
    database,
    engineVersion: "16.0.4135.4",
    changes: [
      {
        setting: `Session ${sessionId}`,
        current:
          "sleeping; last ran dbo.Prescript_Packing_MasterItem: UPDATE dbo.MasterItem SET LastCounted = GETDATE() WHERE MasterItemID = @id",
        target: "killed",
      },
    ],
    statement: `KILL ${sessionId};`,
    rollback: "",
    warnings: [
      `Session ${sessionId}: login granite on WMS-APP01, program GraniteScheduler, database ${database}, sleeping. 3 sessions wait behind it.`,
      "Its transaction has been open for 6 minutes and has written 120.0 MB of log.",
      "Its work is rolled back: everything that transaction changed is undone.",
      "The rollback takes about as long as the work did: up to about 6 minutes here, and the sessions behind it keep waiting until it ends.",
      "Tell whoever owns it (granite on WMS-APP01) before you kill it.",
      "Needs the ALTER ANY CONNECTION permission (sysadmin or processadmin).",
    ],
    options: { sessionId, loginTime: "2026-11-03T08:14:00.000" },
  }
}

function rcsiPreview(database: string): FixPreview {
  const db = `[${database.replace(/]/g, "]]")}]`
  return {
    database,
    engineVersion: "16.0.4135.4",
    changes: [{ setting: "READ_COMMITTED_SNAPSHOT", current: "OFF", target: "ON" }],
    statement: `ALTER DATABASE ${db} SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE;`,
    rollback: `ALTER DATABASE ${db} SET READ_COMMITTED_SNAPSHOT OFF WITH ROLLBACK IMMEDIATE;`,
    warnings: [
      `WITH ROLLBACK IMMEDIATE disconnects every user of ${database} for a moment and undoes their open transactions (37 sessions are connected now), so apply it in a quiet minute.`,
      "Row versions are kept in tempdb: watch its size for a few days (the Storage card's tempdb checks).",
      "Readers stop waiting for writers; writers still wait for writers, so blocking between two updates stays.",
      "Check with the vendor that the application does not rely on readers being blocked to keep two users off the same row (rare).",
      `Needs the ALTER permission on the database ${database}.`,
    ],
  }
}

function notFound(message: string): never {
  throw new ApiError(message, 404)
}

export const mockBlocking: FeatureMock = async (method, path, init) => {
  const b = blockingPath.exec(path)
  if (b && method === "GET") return tree(decodeURIComponent(b[2]))

  const f = fixPath.exec(path)
  if (!f || method !== "POST") return undefined
  const database = decodeURIComponent(f[2])
  const [fixId, step] = [f[3], f[4]]
  const body = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }) : {}
  const options = body.options ?? {}

  if (fixId === "rcsi") {
    if (rcsiOn) notFound(`READ COMMITTED SNAPSHOT is already on for ${database}: nothing to apply`)
    const preview = rcsiPreview(database)
    if (step === "preview") return preview
    rcsiOn = true
    const result: FixResult = {
      preview,
      before: "OFF",
      after: "ON",
      verified: true,
      changeId: 900,
      appliedAt: new Date().toISOString(),
    }
    return result
  }

  const sessionId = options.sessionId ?? ""
  if (sessionId === "83" && killed83) notFound("session 83 is no longer connected to SQL01\\WMS: there is nothing to kill")
  const preview = killPreview(database, sessionId)
  if (step === "preview") return preview
  if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
  if (sessionId === "83") killed83 = true
  const result: FixResult = {
    preview,
    before: preview.changes[0]?.current ?? "",
    after: sessionId === "64" ? "Session 64 is rolling its work back: 35% done, about 2 minutes left." : `Session ${sessionId} is gone.`,
    verified: true,
    changeId: 901,
    appliedAt: new Date().toISOString(),
  }
  return result
}
