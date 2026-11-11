// Phase 5t (docs/phase5t-contract.md): the Indexes page. The indexes that the slowest queries of the last
// 7 days read in full, from Query Store, with those queries as the evidence. Measuring one index is
// phase 5q's queriesApi.indexShape; "Reorganise selected" is the fix reorganize-indexes through
// api.previewFix / api.applyFix, a long fix whose run reports each index.

import { request, type RunItem } from "./api"
import type { QueryAction } from "./api-queries"

export interface LensQuery {
  hash: string // 0x...
  text: string
  runs: number
  totalMs: number
  lastRun: string // UTC
  summary: string // "4,310 runs, 38 min in 7 days"
}

export interface LensRow {
  schema: string
  table: string
  name: string // schema.table
  index: string
  kind: "clustered" | "nonclustered"
  pages: number
  sizeMB: number
  queries: LensQuery[] // the slowest three
  queryCount: number
  more: number // queries not named
  totalMs: number
  blocked?: string // why it cannot be reorganised
  stale: boolean
  statsNote?: string
  statsAction?: QueryAction // update-statistics-table, on every row whose table has statistics
  last?: { at: string; result: string } // kakapo's last reorganise of it
  options: Record<string, string>
}

export interface IndexLens {
  database: string
  canChange: boolean
  queryStore: string
  queries: number // query shapes whose plan was read
  answer: string
  reason?: string // the list could not be built
  rows: LensRow[]
  utcOffsetMinutes: number
  measureBatch: number // how many one click of Measure all measures
  gainPct: number // the saving at which a measured row is pre-ticked
  capGB: number // pre-ticking stops at this size; 0 means no bound
  handover: string
}

const databasePath = (serverId: number, database: string) =>
  `/api/servers/${serverId}/databases/${encodeURIComponent(database)}`

export const indexesApi = {
  // Reads only; nothing is measured. 409 when the database is not online; 502 when the server cannot be read.
  candidates: (serverId: number, database: string) => request<IndexLens>(`${databasePath(serverId, database)}/indexes/candidates`),
}

export type { RunItem }
