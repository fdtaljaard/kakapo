import { ApiError, type BlockingHistory, type BlockingLeader, type CardDetail, type Finding } from "./api"
import type { FeatureMock } from "./api.mock"

// Phase 5d (blocking history): fixture answers for this part's route, undefined means "not mine", and the
// Performance detail's stored history. WarehouseTest has a stored history: AppScheduler's UPDATE, sleeping
// with its transaction open, came back 4 times (High, for the vendor) and an integration procedure under
// SERIALIZABLE was seen once (Info). Read blocking history answers after 2 seconds with a fresh one (any
// database); set localStorage "mock.blockingHistoryFails" to "1" for the 502 a login without VIEW SERVER
// STATE gets, "mock.blockingHistoryQuiet" to "1" for a week without blocking.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

const stored = new Map<string, BlockingHistory>()

const BLK01_NOT_RUN =
  "Not run yet. Read blocking history reads the system_health session's files for the last 7 days; it can take a minute on a busy server."
const LOCK_WHY = "Blocking: sessions waiting for locks held by other sessions."
const THEN_CHECK = "Re-run after a week: fewer occurrences for the top blocker"
const MISSED_NOTE =
  "system_health looks every five minutes, so blocks shorter than that are not in this history; the longest and most repeated ones are. kakapo's event session (Installed helpers on the instance page) keeps every block over 5 seconds."

// The server's clock in the fixtures: the browser's own, as "2026-11-03T14:00".
function hourText(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:00`
}

function serverTime(d: Date): string {
  const day = d.toLocaleDateString("en-GB", { weekday: "short" })
  return `${day} ${hourText(d).slice(0, 10)} ${hourText(d).slice(11, 13)}:${String(d.getMinutes()).padStart(2, "0")} (server time)`
}

function history(quiet: boolean): BlockingHistory {
  const now = new Date()
  const hoursAgo = (h: number) => new Date(now.getTime() - h * 3_600_000)
  // Reports per hour: the sleeping blocker's four episodes in working hours, the procedure once.
  const busy = new Map<number, number>(quiet ? [] : [[150, 6], [149, 3], [102, 4], [78, 2], [30, 5], [29, 1], [4, 1]])
  const byHour = Array.from({ length: 7 * 24 + 1 }, (_, i) => {
    const h = 7 * 24 - i
    const reports = busy.get(h) ?? 0
    return { hour: hourText(hoursAgo(h)), reports, sessionsBlocked: Math.min(reports, 4) }
  })
  const leaders: BlockingLeader[] = quiet
    ? []
    : [
        {
          key: "BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?|dbo.Pick",
          blockerStatement: "BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?",
          blockerProgram: "AppScheduler",
          blockerLogin: "appsvc",
          blockerHost: "WMS-APP01",
          object: "dbo.Pick",
          database: "WarehouseTest",
          occurrences: 4,
          snapshots: 6,
          longestWaitSec: 412,
          totalBlockedSessions: 14,
          kinds: ["sleeping"],
          first: hoursAgo(150).toISOString(),
          last: hoursAgo(30).toISOString(),
          byDay: [
            { day: hourText(hoursAgo(150)).slice(0, 10), count: 1 },
            { day: hourText(hoursAgo(102)).slice(0, 10), count: 1 },
            { day: hourText(hoursAgo(78)).slice(0, 10), count: 1 },
            { day: hourText(hoursAgo(30)).slice(0, 10), count: 1 },
          ],
          sampleVictimStatement: "UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042",
          sampleBlockerStatement: "BEGIN TRAN; UPDATE dbo.Pick SET Qty = 7 WHERE Id = 2001",
          lockMode: "U",
        },
        {
          key: "dbo.PostIntegration|dbo.Stock",
          blockerStatement: "dbo.PostIntegration",
          procedure: "dbo.PostIntegration",
          blockerProgram: "Integration",
          blockerLogin: "integration",
          blockerHost: "INT01",
          object: "dbo.Stock",
          database: "WarehouseTest",
          occurrences: 1,
          snapshots: 1,
          longestWaitSec: 12,
          totalBlockedSessions: 1,
          kinds: ["serializable"],
          first: hoursAgo(4).toISOString(),
          last: hoursAgo(4).toISOString(),
          byDay: [{ day: hourText(hoursAgo(4)).slice(0, 10), count: 1 }],
          sampleVictimStatement: "UPDATE dbo.Stock SET Qty = Qty - 1 WHERE Id = 88",
          sampleBlockerStatement: "Proc [Database Id = 7 Object Id = 1234]",
          lockMode: "U",
          queryHash: "0x11AA22BB33CC44DD",
        },
      ]
  const findings: Finding[] = quiet
    ? []
    : [
        {
          checkId: "BLK01",
          title: "Blocking history",
          severity: "High",
          owner: "Vendor",
          when: "Decide first",
          effort: "Moderate",
          object: `"BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" on dbo.Pick`,
          finding: `"BEGIN TRAN; UPDATE dbo.Pick SET Qty = ? WHERE Id = ?" blocked 14 sessions 4 times in 7 days on dbo.Pick, the longest for 7 minutes; the blocker held its transaction open while doing nothing (sleeping), the last on ${serverTime(hoursAgo(30))}.`,
          why: LOCK_WHY,
          doThis: "The application opens a transaction and waits for the user or a web call: commit sooner, or ask the vendor.",
          thenCheck: THEN_CHECK,
          evidence:
            "Blocker statement: BEGIN TRAN; UPDATE dbo.Pick SET Qty = 7 WHERE Id = 2001 (AppScheduler, appsvc, WMS-APP01). Blocked statement: UPDATE dbo.Pick SET Done = 1 WHERE Id = 1042. Lock wanted: U. Object: dbo.Pick. Seen in 6 five-minute snapshots, 14 sessions blocked in all, at most 4 at once. By day: 4 days, 1 each.",
          clock: "window",
        },
        {
          checkId: "BLK01",
          title: "Blocking history",
          severity: "Info",
          owner: "Vendor",
          when: "Decide first",
          effort: "Moderate",
          object: "dbo.PostIntegration on dbo.Stock",
          finding: `dbo.PostIntegration blocked 1 session once in 7 days on dbo.Stock, the longest for 12 seconds; the blocker used the SERIALIZABLE isolation level, the last on ${serverTime(hoursAgo(4))}.`,
          why: LOCK_WHY,
          doThis: "SERIALIZABLE is TransactionScope's default; ask the vendor for READ COMMITTED.",
          thenCheck: THEN_CHECK,
          evidence:
            "Blocker statement: Proc [Database Id = 7 Object Id = 1234] (Integration, integration, INT01). Blocked statement: UPDATE dbo.Stock SET Qty = Qty - 1 WHERE Id = 88. Lock wanted: U. Object: dbo.Stock. Seen in 1 five-minute snapshot, 1 session blocked in all, at most 1 at once. Query hash: 0x11AA22BB33CC44DD.",
          clock: "window",
        },
      ]
  return {
    capturedAt: now.toISOString(),
    durationSeconds: 3.412,
    days: 7,
    source: "file",
    snapshots: quiet ? 0 : 7,
    reportsTotal: byHour.reduce((n, h) => n + h.reports, 0),
    missedNote: MISSED_NOTE,
    thresholdSeconds: 0,
    leaders,
    byHour,
    findings,
    notes: [],
  }
}

// WarehouseTest starts with a stored history; the others have never been read.
stored.set("WarehouseTest", { ...history(false), capturedAt: new Date(Date.now() - 26 * 3_600_000).toISOString() })

// The Performance detail with the stored history and BLK01 in its checks: skipped before a read, then a
// finding when a leader came back, passed otherwise.
export function withBlockingHistory(detail: CardDetail, database: string): CardDetail {
  const h = stored.get(database) ?? null
  const checks = detail.checks.some((c) => c.checkId === "BLK01")
    ? detail.checks
    : [...detail.checks, { checkId: "BLK01", title: "Blocking history", status: "skipped" as const, note: BLK01_NOT_RUN }]
  return {
    ...detail,
    blockingHistory: h,
    checks: checks.map((c) => {
      if (c.checkId !== "BLK01") return c
      if (!h) return { ...c, status: "skipped", note: BLK01_NOT_RUN }
      const finding = h.findings.some((f) => f.severity !== "Info")
      return { ...c, status: finding ? "finding" : "passed", note: finding ? "" : "No blocker came back in the last 7 days." }
    }),
  }
}

export const mockBlockingHistory: FeatureMock = async (method, path, init) => {
  const m = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/cards\/performance\/blocking-history$/)
  if (!m || method !== "POST") return undefined
  const days = (JSON.parse(String(init?.body ?? "{}")) as { days?: number }).days ?? 7
  if (days < 1 || days > 7) throw new ApiError("days must be 1 to 7", 400)
  await delay(2000)
  if (localStorage.getItem("mock.blockingHistoryFails") === "1") {
    throw new ApiError(
      "could not read the blocking history from the system_health session on WMS-SQL01: VIEW SERVER STATE permission was denied (reading the system_health session needs VIEW SERVER STATE)",
      502,
    )
  }
  const h = history(localStorage.getItem("mock.blockingHistoryQuiet") === "1")
  stored.set(decodeURIComponent(m[2]), h)
  return structuredClone(h)
}
