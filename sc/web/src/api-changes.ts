// Phase 5w (docs/phase5w-contract.md, section C): the What changed? screen of an instance. "Did anything
// kakapo did, or anything anyone else did, line up with this problem?" Opening it reads the server's settings
// baseline and SQL Server's trace; every failure comes back as a note, never a failed page.

import { request } from "./api"
import type { MemoryTone } from "./api-memory"

export type ChangeFlag = "red" | "amber" | "grey" | ""
export type ChangeScreen = "memory" | "performance" | "backups" | "storage"

export interface ChangeRow {
  key: string
  at?: string // exact
  from?: string // between from and to (two of kakapo's reads)
  to?: string
  flag?: ChangeFlag
  rule?: string
  headline: string
  tell?: string
  who: string
  byKakapo: boolean
  source: "kakapo" | "baseline" | "trace" | "by hand"
  database?: string // "" for a server setting
  screens: ChangeScreen[]
  statement?: string
  rollback?: string
  before?: string
  after?: string
  outcome?: "verified" | "differs" | "failed" | "stopped" | "unknown"
  result?: string
  item?: string
  line: string
}

export interface MemoryPoint {
  at: string
  sqlMB?: number
  targetMB?: number
  capMB?: number
}

export interface ChangesPage {
  serverName: string
  verdict: string
  tone: MemoryTone
  problemAt?: string
  problemLabel?: string
  from?: string
  to?: string
  watchedSince?: string
  live: boolean
  notes: string[]
  memory: { points: MemoryPoint[]; snapshots: { id: number; at: string }[]; gaps: string[] }
  rows: ChangeRow[]
  page: number
  pageSize: number
  more: boolean
  listed: number
  counts: { all: number; kakapo: number; outside: number; flagged: number }
  copy: string
}

export interface ChangesQuery {
  problemAt?: string // server time, "2026-11-03T14:20"
  page?: number
  who?: "kakapo" | "outside"
  flagged?: boolean
}

export const changesApi = {
  page: (serverId: number, q: ChangesQuery) => {
    const p = new URLSearchParams()
    if (q.problemAt) p.set("problemAt", q.problemAt)
    if (q.page) p.set("page", String(q.page))
    if (q.who) p.set("who", q.who)
    if (q.flagged) p.set("flagged", "1")
    const qs = p.toString()
    return request<ChangesPage>(`/api/servers/${serverId}/changes${qs ? `?${qs}` : ""}`)
  },
}
