import { ApiError, type FixPreview, type FixResult, type FixRun, type Progress } from "./api"
import type { FeatureMock } from "./api.mock"

// Phase 4e, part 2: the progress route's fixture. One DBCC CHECKDB on kakapo's own session whose
// percentage advances 20% per call (20, 40, 60, 80, 100, then again from 20), so a long fix's dialog
// shows the bar moving while its apply runs. "mock.progressNone" = "1" answers [] (the dialog stays on
// "Starting…"); "mock.progressNoPercent" = "1" answers a CREATE INDEX with no percentage (elapsed time).
//
// Phase 4f: the runs of long fixes (GET .../fixes/runs/{runId}). startMockRun answers an apply with a
// pending result and keeps the run until its work settles. While a run is pending the progress row is
// its fix's command (UPDATE STATISTICS without a percentage for update-statistics), and after 3.5 seconds
// kakapo's session is gone from the rows, so the dialog shows "Finishing: waiting for SQL Server's
// result…" until the run ends.

let calls = 0

const progressPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/progress$/
const runPath = /^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/runs\/([^/]+)$/

interface MockRun {
  run: FixRun
  started: number // Date.now() at the start
}

const runs = new Map<string, MockRun>()
let nextRun = 1

// Starts a long fix's run: work is the apply as it was (it takes its time, then resolves with the result
// or throws the ApiError the apply would). Answers the pending result the real apply returns at once.
export function startMockRun(
  fixId: string,
  database: string,
  preview: FixPreview,
  before: string,
  work: () => Promise<FixResult>,
): FixResult {
  const runId = `mockrun${nextRun++}`
  const startedAt = new Date().toISOString()
  const entry: MockRun = { run: { runId, fixId, database, startedAt, pending: true }, started: Date.now() }
  runs.set(runId, entry)
  work()
    .then((result) => {
      entry.run = { ...entry.run, pending: false, endedAt: new Date().toISOString(), result }
    })
    .catch((e: unknown) => {
      const status = e instanceof ApiError ? e.status : 500
      entry.run = {
        ...entry.run,
        pending: false,
        endedAt: new Date().toISOString(),
        error: { status, message: (e as Error).message },
      }
    })
  return { preview, before, after: "", verified: false, changeId: 0, appliedAt: startedAt, pending: true, runId, startedAt }
}

export const mockProgress: FeatureMock = async (method, path) => {
  const run = path.match(runPath)
  if (method === "GET" && run) {
    const found = runs.get(decodeURIComponent(run[3]))
    if (!found || found.run.database !== decodeURIComponent(run[2])) {
      throw new ApiError(`there is no run "${decodeURIComponent(run[3])}" of a fix on ${decodeURIComponent(run[2])}`, 404)
    }
    return structuredClone(found.run)
  }

  const m = path.match(progressPath)
  if (method !== "GET" || !m) return undefined
  if (localStorage.getItem("mock.progressNone") === "1") return []
  const database = decodeURIComponent(m[2])
  const active = [...runs.values()].find((r) => r.run.pending && r.run.database === database)
  // The statement has finished on the server; the run is reading the result back.
  if (active && Date.now() - active.started > 3500) return []
  calls = (calls % 5) + 1
  const statistics = active?.run.fixId === "update-statistics"
  const noPercent = statistics || localStorage.getItem("mock.progressNoPercent") === "1"
  const elapsedSeconds = calls * 2
  const row: Progress = {
    sessionId: 71,
    command: statistics ? "UPDATE STATISTICS" : noPercent ? "CREATE INDEX" : "DBCC CHECKDB",
    database,
    percent: noPercent ? 0 : calls * 20,
    elapsedSeconds,
    remainingSeconds: noPercent ? 0 : (5 - calls) * 75,
    startedAt: new Date(Date.now() - elapsedSeconds * 1000).toISOString(),
    waitType: noPercent ? "" : "CXPACKET",
    program: "SQLMonitor",
    mine: true,
  }
  return [row]
}
