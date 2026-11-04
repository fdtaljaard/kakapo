import { ApiError, changeTypes, recordedByHand, type Change, type RecordChangeInput } from "./api"
import type {
  HourClass,
  TileTone,
  TuningChange,
  TuningHour,
  TuningLine,
  TuningQuery,
  TuningReport,
  TuningSameHour,
  TuningTile,
} from "./api-tuning"
import type { FeatureMock } from "./api.mock"

// Phase 5c (tuning): fixture answers for this part's routes; undefined means "not mine".
// GET .../tuning: seven (up to 30) days of hours for any database, with one change (the statistics update
// on dbo.StockUnit on the last working day before today at 14:20) that took CPU per execution from 12.4 ms
// to 8.6 ms and parallel waiting from 19 threads to about one; dbo.USP_PICKING got faster but one of its
// queries got a new plan 40 minutes after the change; dbo.USP_AUDIT started failing. The browser's clock is
// the server's. POST .../changes records a change by hand, which then shows as a marker and in Recent
// changes until the page is reloaded. Set localStorage "mock.tuningFails" to "1" to see the 502.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const reportPath = /^\/api\/servers\/(\d+)\/databases\/([^/?]+)\/tuning(?:\?(.*))?$/
const changesPath = /^\/api\/servers\/(\d+)\/databases\/([^/?]+)\/changes$/
const HOUR = 3_600_000

const pad = (n: number) => String(n).padStart(2, "0")
const stamp = (d: Date) =>
  `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:00`
const when = (d: Date) =>
  `${d.toLocaleDateString("en-GB", { weekday: "short", day: "numeric", month: "short" }).replace(",", "")} ${pad(d.getHours())}:${pad(d.getMinutes())}`
const floorHour = (d: Date) => {
  const x = new Date(d)
  x.setMinutes(0, 0, 0)
  return x
}
const dateOf = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
const weekend = (d: Date) => d.getDay() === 0 || d.getDay() === 6

// The fixture's change: the last working day before today, 14:20.
function fixtureChangeAt(): Date {
  const d = new Date()
  d.setHours(14, 20, 0, 0)
  do d.setDate(d.getDate() - 1)
  while (weekend(d))
  return d
}

interface MockChange {
  id: number
  serverId: number
  database: string
  at: Date
  object: string
  changeType: string
  description: string
  byHand: boolean
}

const recorded: MockChange[] = []
let nextId = 7000

function changesFor(serverId: number, database: string): MockChange[] {
  const fixture: MockChange = {
    id: 501,
    serverId,
    database,
    at: fixtureChangeAt(),
    object: "dbo.StockUnit",
    changeType: "STATISTICS",
    description: "Statistics on dbo.StockUnit updated with FULLSCAN",
    byHand: false,
  }
  return [fixture, ...recorded.filter((c) => c.serverId === serverId && c.database === database)].sort(
    (a, b) => b.at.getTime() - a.at.getTime(),
  )
}

// The overview's Recent changes with the fixture's change and the changes recorded by hand in this session
// (api.mock.ts).
export function withRecordedChanges(list: Change[], serverId: number, database: string, limit = 10): Change[] {
  const mine = changesFor(serverId, database).map(asChange)
  return [...mine, ...list].sort((a, b) => b.changedAt.localeCompare(a.changedAt)).slice(0, limit)
}

function asChange(c: MockChange): Change {
  return {
    id: c.id,
    serverId: c.serverId,
    database: c.database,
    changedAt: c.at.toISOString(),
    object: c.object,
    changeType: c.changeType,
    description: c.description,
    result: c.byHand ? recordedByHand : "Updated, statistics now 0 days old",
  }
}

function recordChange(serverId: number, database: string, body: RecordChangeInput): Change {
  const object = body.object?.trim() ?? ""
  const description = body.description?.trim() ?? ""
  if (!object || object.length > 200) throw new ApiError("object must be 1 to 200 characters", 400)
  if (!(changeTypes as readonly string[]).includes(body.changeType)) throw new ApiError(`changeType must be one of ${changeTypes.join(", ")}`, 400)
  if (!description || description.length > 1000) throw new ApiError("description must be 1 to 1,000 characters", 400)
  const at = body.changedAt ? new Date(body.changedAt) : new Date()
  if (Number.isNaN(at.getTime())) throw new ApiError(`changedAt must be server time like 2026-11-03T14:20, not "${body.changedAt}"`, 400)
  if (at.getTime() > Date.now() + 5 * 60_000) throw new ApiError("the change is in the future; record it once it is made", 400)
  const c: MockChange = { id: nextId++, serverId, database, at, object, changeType: body.changeType, description, byHand: true }
  recorded.push(c)
  return asChange(c)
}

interface Hour {
  at: Date
  cls: HourClass
  complete: boolean
  inProgress: boolean
  change: boolean
  execs: number
  cpuMs: number // total
  parSec: number
  lockSec: number
}

function buildHours(days: number, from: number, to: number, changeHours: Set<number>, effectAt: Date): Hour[] {
  const now = new Date()
  const current = floorHour(now)
  const out: Hour[] = []
  for (let t = current.getTime() - days * 24 * HOUR; t <= current.getTime(); t += HOUR) {
    const at = new Date(t)
    const cls: HourClass = weekend(at) ? "weekend" : at.getHours() >= from && at.getHours() < to ? "business" : "night"
    const after = t >= floorHour(effectAt).getTime()
    const wobble = 1 + 0.06 * Math.sin(t / HOUR / 3)
    let execs = 60
    let ms = 5
    let par = 0.3
    if (cls === "business") {
      execs = Math.round(1000 * wobble)
      ms = (after ? 8.6 : 12.4) * (2 - wobble)
      par = after ? 1.2 : 19
    } else if (cls === "night") {
      execs = Math.round(120 * wobble)
    }
    const inProgress = t === current.getTime()
    if (inProgress) execs = Math.round(execs * (now.getMinutes() / 60))
    out.push({
      at,
      cls,
      complete: t + HOUR <= now.getTime(),
      inProgress,
      change: changeHours.has(t),
      execs,
      cpuMs: execs * ms,
      parSec: par * 3600,
      lockSec: (cls === "business" ? 0.4 : 0.05) * 3600,
    })
  }
  return out
}

const usable = (h: Hour) => h.cls === "business" && h.complete && !h.change && h.execs > 0

function workingDaysBefore(day: Date, n: number): number[] {
  const out: number[] = []
  const d = new Date(day.getFullYear(), day.getMonth(), day.getDate())
  while (out.length < n) {
    d.setDate(d.getDate() - 1)
    if (!weekend(d)) out.push(d.getTime())
  }
  return out
}

function sum(hs: Hour[]) {
  return hs.reduce(
    (s, h) => ({ hours: s.hours + 1, execs: s.execs + h.execs, cpu: s.cpu + h.cpuMs, par: s.par + h.parSec, lock: s.lock + h.lockSec }),
    { hours: 0, execs: 0, cpu: 0, par: 0, lock: 0 },
  )
}

const round = (n: number, d: number) => Math.round(n * 10 ** d) / 10 ** d

function tile(id: TuningTile["id"], label: string, unit: string, before: number | null, now: number | null, lowerBetter: boolean, d: number, words: string): TuningTile {
  let pct: number | null = null
  let tone: TileTone = "same"
  if (before && now !== null) {
    const p = ((now - before) / before) * 100
    pct = Math.round(p)
    if (lowerBetter) tone = p <= -10 ? "better" : p >= 10 ? "worse" : "same"
    else tone = p >= 10 ? "more" : p <= -10 ? "fewer" : "same"
  }
  return { id, label, unit, before: before === null ? null : round(before, d), now: now === null ? null : round(now, d), pct, tone, words }
}

function queries(centred: boolean): TuningQuery[] {
  const q = (p: Partial<TuningQuery> & Pick<TuningQuery, "queryHash" | "name" | "verdict">): TuningQuery => ({
    objectName: p.name.startsWith("dbo.") ? p.name : "",
    text: "SELECT TOP (1) su.ID, su.Qty FROM dbo.StockUnit su JOIN dbo.Location l ON l.ID = su.LocationID WHERE su.Barcode = @code",
    runsBefore: 0,
    runsNow: 0,
    cpuBeforeMs: null,
    cpuNowMs: null,
    execsPerHourBefore: 0,
    execsPerHourNow: 0,
    impactMsPerHour: 0,
    copy: "",
    ...p,
  })
  const list = [
    q({ queryHash: "0x8F3A2C11D07B9E45", name: "dbo.USP_PICKING", verdict: "faster", runsBefore: 2400, runsNow: 1600, cpuBeforeMs: 20.1, cpuNowMs: 8.2, execsPerHourBefore: 400, execsPerHourNow: 400, impactMsPerHour: -4760 }),
    q({ queryHash: "0x51C0D7A2E94B3F18", name: "dbo.USP_SLOW_LABEL", verdict: "slower", runsBefore: 600, runsNow: 400, cpuBeforeMs: 4, cpuNowMs: 6.1, execsPerHourBefore: 100, execsPerHourNow: 100, impactMsPerHour: 210, text: "UPDATE dbo.Label SET Printed = 1 WHERE ID = @id" }),
    q({ queryHash: "0x2B7E19A0C4D35F61", name: 'the ad hoc query "SELECT COUNT(*) FROM dbo.Movement WHERE CreatedDate > @d"', verdict: "new", runsNow: 120, cpuNowMs: 5, execsPerHourNow: 30, impactMsPerHour: 150, text: "SELECT COUNT(*) FROM dbo.Movement WHERE CreatedDate > @d" }),
    q({ queryHash: "0x7D04E8B1A9C2F355", name: "dbo.USP_RECEIVE", verdict: "same", runsBefore: 900, runsNow: 600, cpuBeforeMs: 3.1, cpuNowMs: 3, execsPerHourBefore: 150, execsPerHourNow: 150, impactMsPerHour: -15, text: "INSERT dbo.Receipt (...) VALUES (...)" }),
    q({ queryHash: "0x0C9F3E27B6A18D40", name: "dbo.USP_AUDIT", verdict: "too few", runsBefore: 18, runsNow: 12, cpuBeforeMs: 50, cpuNowMs: 52, execsPerHourBefore: 3, execsPerHourNow: 3, text: "INSERT dbo.Audit (...) VALUES (...)" }),
  ]
  if (!centred) for (const x of list) x.verdict = x.verdict === "new" ? "same" : x.verdict
  for (const x of list) {
    x.copy = `${x.name} in WarehouseLive\nQuery hash: ${x.queryHash}\n\nCPU per run: before ${x.cpuBeforeMs ?? "-"} ms (${x.runsBefore} runs), now ${x.cpuNowMs ?? "-"} ms (${x.runsNow} runs); verdict: ${x.verdict}.\n\nQuery text (first 300 characters)\n${x.text}`
  }
  return list
}

function forcePlan(planId: number) {
  return { fixId: "force-plan", label: "Force the old plan", title: `Force plan ${planId}`, options: { planId: String(planId) } }
}

function buildReport(serverId: number, database: string, params: URLSearchParams): TuningReport {
  const days = Number(params.get("days") ?? 7)
  if (!Number.isInteger(days) || days < 1 || days > 30) throw new ApiError(`days must be a whole number from 1 to 30, not "${params.get("days")}"`, 400)
  const hoursOpt = params.get("hours") ?? "08-17"
  const m = /^(\d{2})-(\d{2})$/.exec(hoursOpt)
  if (!m || Number(m[1]) >= Number(m[2]) || Number(m[2]) > 24) throw new ApiError(`business hours are HH-HH, like 08-17, not "${hoursOpt}"`, 400)
  const from = Number(m[1])
  const to = Number(m[2])

  const now = new Date()
  const windowStart = floorHour(now).getTime() - days * 24 * HOUR
  const all = changesFor(serverId, database).filter((c) => c.at.getTime() >= windowStart)
  const changeParam = params.get("change")
  let centre: MockChange | undefined
  if (changeParam === "none") centre = undefined
  else if (changeParam) {
    centre = all.find((c) => c.id === Number(changeParam))
    if (!centre) throw new ApiError(`change ${changeParam} is not in the last ${days} days of ${database}`, 404)
  } else centre = all[0]

  const fixtureAt = fixtureChangeAt()
  const hours = buildHours(days, from, to, new Set(all.map((c) => floorHour(c.at).getTime())), fixtureAt)

  let before: Hour[] = []
  let nowHours: Hour[] = []
  let beforeLabel = ""
  let nowLabel = ""
  let latest: Hour | undefined
  if (centre) {
    const cH = floorHour(centre.at).getTime()
    const next = all.filter((c) => c.at > centre!.at).map((c) => floorHour(c.at).getTime())
    const bound = next.length ? Math.min(...next) : Infinity
    nowHours = hours.filter((h) => usable(h) && h.at.getTime() > cH && h.at.getTime() < bound)
    const sameDay = hours.filter((h) => usable(h) && h.at.getTime() < cH && dateOf(h.at) === dateOf(centre!.at))
    nowLabel = `${nowHours.length} complete business hours since the change`
    if (sameDay.length >= 2) {
      before = sameDay
      beforeLabel = `${before.length} business hours earlier the same day`
    } else {
      const ds = new Set(workingDaysBefore(centre.at, 5))
      before = hours.filter((h) => usable(h) && ds.has(dateOf(h.at)))
      beforeLabel = `${before.length} business hours on the five working days before`
    }
  } else {
    latest = [...hours].reverse().find(usable)
    if (latest) {
      nowHours = [latest]
      const ds = new Set(workingDaysBefore(latest.at, 5))
      before = hours.filter((h) => usable(h) && ds.has(dateOf(h.at)) && h.at.getHours() === latest!.at.getHours())
      nowLabel = `the latest complete business hour (${when(latest.at)})`
      beforeLabel = `the same hour on the five working days before (${before.length} hours with history)`
    }
  }
  const bs = sum(before)
  const ns = sum(nowHours)
  const cpuB = bs.execs ? bs.cpu / bs.execs : null
  const cpuN = ns.execs ? ns.cpu / ns.execs : null
  const exB = bs.hours ? bs.execs / bs.hours : null
  const exN = ns.hours ? ns.execs / ns.hours : null
  const parB = bs.hours ? bs.par / 3600 / bs.hours : null
  const parN = ns.hours ? ns.par / 3600 / ns.hours : null
  const lockB = bs.hours ? bs.lock / 3600 / bs.hours : null
  const lockN = ns.hours ? ns.lock / 3600 / ns.hours : null
  const tiles: TuningTile[] = [
    tile("cpu-per-execution", "CPU per execution", "ms", cpuB, cpuN, true, 2, cpuN === null ? "" : `${cpuN.toFixed(1)} ms of CPU per execution`),
    tile("executions-per-hour", "Executions per hour", "per hour", exB, exN, false, 0, exN === null ? "" : `${Math.round(exN)} executions an hour`),
    tile("parallel-threads", "Parallel threads waiting", "threads", parB, parN, true, 1, parN === null ? "" : `${parN.toFixed(1)} threads waiting on parallelism all the time`),
    tile("lock-threads", "Lock threads waiting", "threads", lockB, lockN, true, 1, lockN === null ? "" : `${lockN.toFixed(1)} threads waiting on locks all the time`),
  ]

  const windowOf = new Map<number, "before" | "now">()
  for (const h of before) windowOf.set(h.at.getTime(), "before")
  for (const h of nowHours) windowOf.set(h.at.getTime(), "now")
  const outHours: TuningHour[] = hours.map((h) => ({
    hour: stamp(h.at),
    class: h.cls,
    complete: h.complete,
    inProgress: h.inProgress,
    change: h.change,
    noData: false,
    window: windowOf.get(h.at.getTime()) ?? "",
    executions: h.execs,
    cpuPerExecMs: h.execs ? round(h.cpuMs / h.execs, 3) : 0,
    readsPerExec: 41,
    durationPerExecMs: h.execs ? round((h.cpuMs / h.execs) * 2.2, 3) : 0,
    parallelThreads: round(h.parSec / 3600, 2),
    lockThreads: round(h.lockSec / 3600, 2),
    failed: h.cls === "business" && h.at > fixtureAt ? 2 : 0,
  }))

  // The same hour, before and after.
  const sameHour: TuningSameHour[] = []
  const dayAnchor = centre ? new Date(centre.at) : latest ? latest.at : undefined
  if (dayAnchor) {
    while (weekend(dayAnchor)) dayAnchor.setDate(dayAnchor.getDate() + 1)
    const prev = workingDaysBefore(dayAnchor, 5)
    const byTime = new Map(hours.map((h) => [h.at.getTime(), h]))
    for (let hr = from; hr < to; hr++) {
      const at = new Date(dayAnchor.getFullYear(), dayAnchor.getMonth(), dayAnchor.getDate(), hr).getTime()
      const h = byTime.get(at)
      const prevHours = prev.map((d) => byTime.get(d + hr * HOUR)).filter((x): x is Hour => !!x && x.complete && !x.change)
      const ps = sum(prevHours)
      sameHour.push({
        hour: `${pad(hr)}:00`,
        day: {
          cpuPerExecMs: h && h.execs ? round(h.cpuMs / h.execs, 2) : null,
          executions: h ? h.execs : null,
          days: h ? 1 : 0,
          note: !h ? (at > now.getTime() ? "not yet" : "no data") : h.inProgress ? "in progress" : h.change ? "change" : "",
        },
        previous: {
          cpuPerExecMs: ps.execs ? round(ps.cpu / ps.execs, 2) : null,
          executions: ps.hours ? Math.round(ps.execs / ps.hours) : null,
          days: ps.hours,
          note: ps.hours ? "" : "no data",
        },
      })
    }
  }

  const tuningChanges: TuningChange[] = all.map((c) => ({
    id: c.id,
    at: stamp(c.at),
    object: c.object,
    changeType: c.changeType,
    description: c.description,
    byHand: c.byHand,
    serverLevel: false,
    performance: c.byHand || !["CHECK", "KILL", "JOB", "ALERT", "CLEAR", "ARCHIVE"].includes(c.changeType),
  }))
  const regAfter = !!centre && centre.id === 501
  const firstSeen = new Date(fixtureAt.getTime() + 40 * 60_000)
  firstSeen.setMinutes(0)
  firstSeen.setHours(firstSeen.getHours() + 1)
  const regressions = [
    {
      queryId: 1873,
      queryHash: "0x8F3A2C11D07B9E45",
      name: "dbo.USP_PICKING",
      text: "SELECT TOP (1) su.ID, su.Qty FROM dbo.StockUnit su JOIN dbo.Location l ON l.ID = su.LocationID WHERE su.Barcode = @code",
      oldPlanId: 15,
      oldCpuMs: 5,
      oldRuns: 4000,
      newPlanId: 22,
      newCpuMs: 10.5,
      newRuns: 300,
      firstSeen: stamp(firstSeen),
      ratio: 2.1,
      relation: centre ? (regAfter ? "40 minutes after the change" : "before the change") : `first seen ${when(firstSeen)}`,
      afterChange: regAfter,
      action: forcePlan(15),
      copy: "dbo.USP_PICKING in WarehouseLive\nQuery hash: 0x8F3A2C11D07B9E45\n\nPlan 22 uses 2.1 x the CPU per run of plan 15.",
    },
  ]
  const failures = [
    {
      queryId: 3001,
      queryHash: "0x0C9F3E27B6A18D40",
      name: "dbo.USP_AUDIT",
      text: "INSERT dbo.Audit (...) VALUES (...)",
      aborted: 12,
      exceptions: 3,
      firstFailed: stamp(firstSeen),
      lastFailed: stamp(new Date(now.getTime() - 2 * HOUR)),
      relation: centre ? (regAfter ? "40 minutes after the change" : "before the change") : `first seen ${when(firstSeen)}`,
      afterChange: regAfter,
      copy: "dbo.USP_AUDIT in WarehouseLive\nQuery hash: 0x0C9F3E27B6A18D40\n\n12 aborted (timeouts, cancels) and 3 ended in an error.",
    },
  ]

  const qs = queries(!!centre)
  const explain: TuningLine[] = []
  const since = centre ? "since the change" : "in the latest hour"
  if (cpuB !== null && cpuN !== null) {
    const p = Math.round(((cpuN - cpuB) / cpuB) * 100)
    const cpuText =
      p <= -10
        ? `is down ${-p}% (from ${cpuB.toFixed(1)} ms to ${cpuN.toFixed(1)} ms)`
        : p >= 10
          ? `is up ${p}% (from ${cpuB.toFixed(1)} ms to ${cpuN.toFixed(1)} ms)`
          : `is about the same (${cpuB.toFixed(1)} ms before, ${cpuN.toFixed(1)} ms now)`
    const tail =
      p <= -10 ? "executions per hour are the same, so the work got cheaper, not rarer." : "executions per hour are the same, so there is no clear difference yet."
    explain.push({
      text: centre
        ? `Since ${centre.changeType === "STATISTICS" ? "the statistics update on" : "the change to"} ${centre.object} on ${when(centre.at)}, CPU per execution in business hours ${cpuText} over ${ns.hours} hours; ${tail}`
        : `In the latest complete business hour (${latest ? when(latest.at) : ""}), CPU per execution ${cpuText} against the same hour on the five working days before; ${tail}`,
    })
  }
  for (const q of qs.filter((x) => x.verdict === "faster" || x.verdict === "slower")) {
    const text = `${q.name} got ${q.verdict} ${since}: ${q.cpuBeforeMs} ms of CPU per run before, ${q.cpuNowMs} ms now, over ${q.runsNow.toLocaleString("en-US")} runs.${q.verdict === "slower" ? " Investigate it." : ""}`
    explain.push({ text, queryHash: q.queryHash, copy: q.copy })
  }
  if (!centre || regAfter) {
    const r = regressions[0]
    const text = centre
      ? `The new plan of ${r.name} uses 2.1 x the CPU per run of the one before and appeared 40 minutes after the change: force the old plan, or revert.`
      : `The new plan of ${r.name}, first seen ${when(firstSeen)}, uses 2.1 x the CPU per run of the one before: force the old plan.`
    explain.push({ text, action: r.action, queryHash: r.queryHash, copy: r.copy })
  }
  if (regAfter) {
    const f = failures[0]
    explain.push({
      text: `${f.name} has failed 15 times since the change (12 timed out or were cancelled, 3 ended in an error), first at ${when(firstSeen)}.`,
      queryHash: f.queryHash,
      copy: f.copy,
    })
  }
  if (centre && ns.hours < 2) {
    explain.push({ text: `It is too early to say: ${ns.hours === 0 ? "there is no complete business hour since the change yet" : "only one complete business hour has passed since the change"}.` })
  }
  if (all.length === 0) {
    explain.push({
      text: `No change is in the log for the last ${days} days, so this compares the latest complete business hour with the same hour on the five working days before. Record a change made by hand to measure it.`,
      recordChange: true,
    })
  }

  const report: TuningReport = {
    database,
    serverName: "CLIENTSQL01\\WMS",
    capturedAt: stamp(now),
    utcOffsetMinutes: -now.getTimezoneOffset(),
    days,
    businessHours: hoursOpt,
    queryStoreState: "READ_WRITE",
    intervalMinutes: 15,
    oldestHour: stamp(new Date(now.getTime() - 30 * 24 * HOUR)),
    waitsRead: true,
    changes: tuningChanges,
    centre: centre
      ? { changeId: centre.id, at: stamp(centre.at), label: `${centre.object} on ${when(centre.at)}` }
      : { changeId: 0, at: latest ? stamp(latest.at) : "", label: latest ? `the latest complete business hour (${when(latest.at)})` : "no complete business hour yet" },
    before: { label: beforeLabel, hours: before.length },
    now: { label: nowLabel, hours: nowHours.length },
    tiles,
    hours: outHours,
    sameHour,
    queries: qs,
    regressions,
    failures,
    explain,
    notes: [],
    handover: "",
  }
  report.handover = [
    `Tuning report for ${database} on ${report.serverName}`,
    `Centred on ${report.centre.label}.`,
    `Now: ${nowLabel}.\nBefore: ${beforeLabel}.`,
    "",
    "What we see",
    ...explain.map((l) => `- ${l.text}`),
    "",
    "Is it better than before?",
    ...tiles.map((t) => `- ${t.label}: ${t.before ?? "-"} -> ${t.now ?? "-"}${t.pct !== null ? ` (${t.pct > 0 ? "+" : ""}${t.pct}%, ${t.tone})` : ""}`),
    "",
    "What is expensive (CPU per run before -> now; runs before -> now; verdict)",
    ...qs.map((q) => `- ${q.name} (${q.queryHash}): ${q.cpuBeforeMs ?? "-"} ms -> ${q.cpuNowMs ?? "-"} ms; ${q.runsBefore} -> ${q.runsNow} runs; ${q.verdict}`),
  ].join("\n")
  return report
}

export const mockTuning: FeatureMock = async (method, path, init) => {
  const rep = reportPath.exec(path)
  if (method === "GET" && rep) {
    await delay(1500) // the real read connects and reads Query Store over the window
    if (localStorage.getItem("mock.tuningFails") === "1") {
      throw new ApiError("could not read Query Store of WarehouseLive on CLIENTSQL01\\WMS: the login does not have VIEW DATABASE STATE", 502)
    }
    return structuredClone(buildReport(Number(rep[1]), decodeURIComponent(rep[2]), new URLSearchParams(rep[3] ?? "")))
  }
  const ch = changesPath.exec(path)
  if (method === "POST" && ch) {
    await delay(400)
    return recordChange(Number(ch[1]), decodeURIComponent(ch[2]), JSON.parse(String(init?.body ?? "{}")) as RecordChangeInput)
  }
  return undefined
}
