// Phase 5v (docs/phase5v-contract.md): the Memory screen. "Where is the memory going, and is something
// abnormal?" for one instance, and Database detail for one database. Read-only on the server. Panels 2 and
// 3 are read on every page load; the buffer pool reads run only on a click, are kept as snapshots, and stop
// when the request is aborted (the page's Stop), which ends the statement on the server.

import { request } from "./api"

export type MemoryTone = "red" | "amber" | "green" | "grey"

export interface MemorySignal {
  level: "red" | "amber"
  text: string
}

export interface NodePLE {
  node: string
  seconds: number
}

export interface MemoryGlance {
  totalGB: number
  sqlGB: number
  otherGB: number // RAM minus SQL Server minus free: "everything else"
  freeGB: number
  lockedGB: number
  committedGB: number
  targetGB: number
  cap: string // "no cap" | "48.0 GB"
  capMB: number
  min: string
  startedAt: string
  uptime: string
  memoryModel: string
  lockPages: boolean
  lockPagesNote?: string
  windowsState: string
  nodePLE: NodePLE[]
  grantsPending: number
  counterError?: string
  express?: string
  filling: boolean
}

export interface MemoryClerkGroup {
  name: string
  gb: number
  share: number
}

export interface MemoryClerk {
  type: string
  group: string
  gb: number
}

export interface MemoryClerks {
  groups: MemoryClerkGroup[]
  top: MemoryClerk[]
  totalGB: number
  singleUseAdhocGB: number
  adHocOn: boolean
  planCacheError?: string
  grantedWorkspaceGB: number
  reconcile?: string // the gap, when outside 5%
  reconcileText: string
}

export interface MemoryDBRow {
  id: number
  name: string
  gb: number
  share: number
  flag?: string
  flagNote?: boolean // a note, not a memory signal: shown muted
  user: boolean // a user database: it opens Database detail
}

export interface MemoryDBs {
  readAt: string
  seconds: number
  rows: MemoryDBRow[]
  totalGB: number
  dataCacheGB: number
  reconcile?: string
  reconcileText: string
}

export interface MemoryView {
  takenAt: string
  permission?: string
  tone: MemoryTone
  verdict: string
  signals: MemorySignal[]
  glance: MemoryGlance
  clerks: MemoryClerks
  databases: MemoryDBs | null
  handover: string
}

export interface MemorySnapshotInfo {
  id: number
  takenAt: string
  kind: "instance" | "database"
  database?: string
  databases: boolean
}

export interface MemoryPage {
  view: MemoryView
  snapshotId: number // the snapshot shown; 0 when none
  live: boolean // panels 2 and 3 were read now
  snapshots: MemorySnapshotInfo[]
  serverName: string
}

export interface IndexCacheRow {
  table: string
  index: string
  type: string
  cachedMB: number
  totalMB: number
  cachedPct: number
  seeks: number
  scans: number
  lookups: number
  updates: number
  lastRead?: string
  temperature: "hot" | "warm" | "cold"
  keys: string
  includes: string
  flags: string[]
}

export interface DatabaseMemory {
  database: string
  takenAt: string
  seconds: number
  permission?: string
  answer: string
  cachedGB: number
  hotGB: number
  warmGB: number
  coldGB: number
  coldNote: string
  usageNote?: string
  indexes: number
  rows: IndexCacheRow[]
  rest?: { indexes: number; cachedMB: number }
  handover: string
}

export interface DatabaseMemoryPage {
  detail: DatabaseMemory | null
  snapshotId: number
  snapshots: MemorySnapshotInfo[]
  serverName: string
}

const memoryPath = (serverId: number) => `/api/servers/${serverId}/memory`
const snap = (id?: number) => (id ? `?snapshot=${id}` : "")

export const memoryApi = {
  // Panels 2 and 3 now, panel 4 from the latest snapshot; or one snapshot as it was read.
  page: (serverId: number, snapshotId?: number) => request<MemoryPage>(`${memoryPath(serverId)}${snap(snapshotId)}`),
  // The slow read: the cache by database, kept as a snapshot. Abort the signal to stop it.
  readDatabases: (serverId: number, signal?: AbortSignal) =>
    request<MemoryPage>(`${memoryPath(serverId)}/snapshot`, { method: "POST", signal }),
  // Database detail as last read, or one snapshot; reads nothing from the server.
  database: (serverId: number, database: string, snapshotId?: number) =>
    request<DatabaseMemoryPage>(`${memoryPath(serverId)}/databases/${encodeURIComponent(database)}${snap(snapshotId)}`),
  // The slow read of one database's cache, index by index, kept as a snapshot.
  readDatabase: (serverId: number, database: string, signal?: AbortSignal) =>
    request<DatabaseMemoryPage>(`${memoryPath(serverId)}/databases/${encodeURIComponent(database)}/snapshot`, { method: "POST", signal }),
}
