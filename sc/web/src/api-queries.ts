// Phase 4c, part 1 (docs/phase4c-contract.md): investigate one query of a database by its query hash.
// The query page's fixes (force-plan, unforce-plan, evict-plan, update-statistics-table, and part 3's
// create-index from a plan's hint) go through api.previewFix / api.applyFix with their options.

import { request } from "./api"

export type QueryDetailSource = "query-store" | "plan-cache" | "none"

export interface QueryDetail {
  database: string
  queryHash: string // 0x...
  serverName: string
  capturedAt: string // UTC
  utcOffsetMinutes: number // the server's offset: the page shows every time on the server's clock
  text: string
  objectName: string // schema.procedure; "" for ad hoc
  source: QueryDetailSource
  queryStoreState: string // READ_WRITE | READ_ONLY | OFF | ERROR | "" when not read
  summary: QuerySummary
  verdict: QueryVerdict | null // phase 5j: the plan in use against the runs before; null when it ran one way all week
  events: QueryEvent[] // plans begun and statistics updated in the window, oldest first
  explain: ExplainLine[] // plain sentences
  plans: QueryPlan[] // the plan in use first
  history: QueryHour[] // newest first; Query Store only
  waits: QueryWait[] // Query Store, SQL Server 2017 and later
  statements: ProcStatement[] // the procedure's statements, most reads first
  tables: QueryTable[] // largest first
  notes: string[] // what could not be read, and why
  handover: string // Copy for the vendor
}

export interface QuerySummary {
  window: string // "7 days" | "since 09:00"
  runs: number
  avgDurationMs: number
  maxDurationMs: number
  avgCpuMs: number
  avgReads: number
  maxReads: number
  lastRun: string | null
}

// Phase 5j (docs/phase5j-contract.md): is it still a problem? The plan in use since it began against
// the runs before, with 5c's verdict words.
export interface QueryVerdict {
  verdict: "faster" | "slower" | "same" | "too few"
  headline: string
  since: string // UTC
  planId: number
  before: QueryPeriod
  now: QueryPeriod
  changed: QueryEvent[]
  toDo: string
}

export interface QueryPeriod {
  label: string
  runs: number
  avgDurationMs: number
  avgCpuMs: number
  avgReads: number
  cpuMs: number
  durationMs: number
}

export interface QueryEvent {
  at: string // UTC
  kind: "plan" | "statistics"
  planId?: number
  text: string
}

export interface ExplainLine {
  text: string
  action?: QueryAction // the fix the sentence names
}

// Opens the fix dialog: the fix, the dialog's title and the options its first preview needs.
export interface QueryAction {
  fixId: string // "force-plan" | "unforce-plan" | "evict-plan" | "update-statistics-table" | "create-index" | "query-store" | "archive-table"
  label: string // the button
  title: string // the dialog
  options: Record<string, string>
}

export interface QueryPlan {
  planId: number // 0 when only in the plan cache
  queryId: number
  planHash: string
  planHandle: string // "" when not in the plan cache
  cachedCopies?: number // a plan only in the plan cache: its entries there (one per set of SET options)
  source: "query-store" | "plan-cache"
  forced: boolean
  active: boolean // used most recently
  fastest: boolean // the lowest average duration, of two or more plans that ran
  lastCompile: string | null
  lastRun: string | null
  firstRun?: string | null // its first run in the window (Query Store)
  engineVersion: string
  compatibilityLevel: number
  runs: number
  avgCpuMs: number
  maxCpuMs: number
  avgDurationMs: number
  maxDurationMs: number
  avgReads: number
  maxReads: number
  tempdbMbPerRun: number | null
  forceFailures: number
  forceFailureReason: string
  planTooBig: boolean
  conversion: string // the expression of an implicit conversion that stops a seek
  warnings: string[]
  parameters: QueryPlanParameter[]
  missingIndexes: QueryMissingIndex[]
  planXml?: string // the first plans only: Save .sqlplan
  actions: QueryAction[]
}

export interface QueryPlanParameter {
  name: string
  dataType: string
  compiledValue: string
  runtimeValue: string
}

export interface QueryMissingIndex {
  table: string // [db].[schema].[table]
  tableName: string // schema.table
  equality: string // "[A], [B]"
  inequality: string
  included: string
  impact: number // percent
  action?: QueryAction // create-index, source=plan
}

export interface QueryHour {
  hour: string // UTC, on the hour
  runs: number
  cpuMs: number
  durationMs: number
  avgDurationMs: number
  reads: number
  plans: number[]
}

export interface QueryWait {
  category: string
  words: string
  waitMs: number
  pct: number
}

export interface ProcStatement {
  line: number
  text: string
  runs: number
  minCpuMs: number
  avgCpuMs: number
  maxCpuMs: number
  minReads: number
  avgReads: number
  maxReads: number
  pctReads: number
  thisQuery: boolean
  lookAt: string[]
  lastRun: string
}

export interface QueryTable {
  schema: string
  table: string
  name: string // schema.table
  found: boolean
  rows: number
  statistics: number
  oldestUpdate: string | null // server time
  ageDays: number | null
  maxModifiedPct: number
  stale: boolean
  indexes: string[]
  action?: QueryAction // update-statistics-table
}

const databasePath = (serverId: number, database: string) =>
  `/api/servers/${serverId}/databases/${encodeURIComponent(database)}`

export const queriesApi = {
  // Reads only: 400 for a hash that is not one, 404 for an unknown server or database, 409 when the
  // database is not online, 502 when the server cannot be read.
  queryDetail: (serverId: number, database: string, hash: string) =>
    request<QueryDetail>(`${databasePath(serverId, database)}/queries/${encodeURIComponent(hash)}`),
}
