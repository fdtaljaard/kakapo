// Phase 5c (docs/phase5c-contract.md): the tuning report, "is the database better than before, and did
// the change I made do it?". Times are the server's clock without a zone ("2026-11-03T14:00:00"), so
// new Date() reads them as local wall-clock time and the page shows them as the server does.

import { request } from "./api"
import type { QueryAction } from "./api-queries"

export type HourClass = "business" | "night" | "weekend"
export type TileTone = "better" | "worse" | "same" | "more" | "fewer"
export type Verdict = "faster" | "slower" | "same" | "new" | "gone" | "too few"

export interface TuningReport {
  database: string
  serverName: string
  capturedAt: string // server clock
  utcOffsetMinutes: number
  days: number
  businessHours: string // "08-17"
  queryStoreState: string
  intervalMinutes: number
  oldestHour: string // "" when Query Store holds nothing
  waitsRead: boolean
  changes: TuningChange[] // in the window, newest first
  centre: TuningCentre
  before: TuningWindow
  now: TuningWindow
  tiles: TuningTile[] // CPU per execution, executions per hour, parallel and lock threads waiting
  hours: TuningHour[] // oldest first, one per hour up to the hour in progress
  sameHour: TuningSameHour[]
  queries: TuningQuery[] // sorted by the CPU saved or added
  regressions: TuningRegression[]
  failures: TuningFailure[]
  explain: TuningLine[] // the plain sentences, the first thing on the page
  notes: string[] // what could not be read, and why
  handover: string // Copy for IT
}

export interface TuningChange {
  id: number
  at: string // server clock
  object: string
  changeType: string
  description: string
  byHand: boolean
  serverLevel: boolean
  performance: boolean // can affect performance; only these are the default centre
}

export interface TuningCentre {
  changeId: number // 0: no change, the latest complete business hour
  at: string
  label: string
}

export interface TuningWindow {
  label: string
  hours: number
}

export interface TuningTile {
  id: "cpu-per-execution" | "executions-per-hour" | "parallel-threads" | "lock-threads"
  label: string
  unit: string // ms | per hour | threads
  before: number | null
  now: number | null
  pct: number | null
  tone: TileTone
  words: string
  beforeText?: string // stands for the value: "none" (zero, or rounds to zero), "no runs"
  nowText?: string
}

export interface TuningHour {
  hour: string
  class: HourClass
  complete: boolean
  inProgress: boolean
  change: boolean
  noData: boolean
  window: "before" | "now" | ""
  executions: number
  cpuPerExecMs: number
  readsPerExec: number
  durationPerExecMs: number
  parallelThreads: number
  lockThreads: number
  failed: number
}

export interface TuningHourCell {
  cpuPerExecMs: number | null
  executions: number | null
  days: number
  note: string // "change" | "in progress" | "not yet" | "no data" | ""
}

export interface TuningSameHour {
  hour: string // "08:00"
  day: TuningHourCell
  previous: TuningHourCell
}

export interface TuningQuery {
  queryHash: string
  name: string
  objectName: string
  text: string
  runsBefore: number
  runsNow: number
  cpuBeforeMs: number | null
  cpuNowMs: number | null
  execsPerHourBefore: number
  execsPerHourNow: number
  verdict: Verdict
  impactMsPerHour: number // CPU added (+) or saved (-) per business hour
  copy: string
}

export interface TuningRegression {
  queryId: number
  queryHash: string
  name: string
  text: string
  oldPlanId: number
  oldCpuMs: number
  oldRuns: number
  newPlanId: number
  newCpuMs: number
  newRuns: number
  firstSeen: string
  ratio: number
  relation: string // "40 minutes after the change"
  afterChange: boolean
  action?: QueryAction // force the old plan
  copy: string
}

export interface TuningFailure {
  queryId: number
  queryHash: string
  name: string
  text: string
  aborted: number
  exceptions: number
  firstFailed: string
  lastFailed: string
  relation: string
  afterChange: boolean
  copy: string
}

// One sentence; a sentence that names a query carries its hash (Investigate) and a Copy block for the
// vendor; action is a fix; recordChange opens the Record a change form.
export interface TuningLine {
  text: string
  action?: QueryAction
  queryHash?: string
  copy?: string
  recordChange?: boolean
}

export interface TuningOptions {
  days?: number // 1 to 30, default 7
  change?: number | "none" // a change id; default the latest in the window
  hours?: string // "08-17"
}

const databasePath = (serverId: number, database: string) =>
  `/api/servers/${serverId}/databases/${encodeURIComponent(database)}`

export function tuningQuery(o: TuningOptions): string {
  const q = new URLSearchParams()
  if (o.days) q.set("days", String(o.days))
  if (o.change !== undefined) q.set("change", String(o.change))
  if (o.hours) q.set("hours", o.hours)
  const s = q.toString()
  return s ? `?${s}` : ""
}

export const tuningApi = {
  // Reads only: 400 for an option that is not right, 404 for an unknown server, database or a change not
  // in the window, 409 when the database is not online or the server is older than SQL Server 2016, 502
  // when the server cannot be read.
  report: (serverId: number, database: string, o: TuningOptions = {}) =>
    request<TuningReport>(`${databasePath(serverId, database)}/tuning${tuningQuery(o)}`),
}
