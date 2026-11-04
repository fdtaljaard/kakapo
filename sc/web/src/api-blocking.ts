// Phase 4c, part 2: the blocking page's client (docs/phase4c-contract.md). Its two fixes, kill-session
// (options {sessionId}) and rcsi, go through api.previewFix / api.applyFix.

import { request } from "./api"

// How a session in a chain is classified (sp_HumanEventsBlockViewer's rules, plus a long statement and a
// kill rolling back).
export type BlockKind =
  | "sleeping"
  | "implicit"
  | "serializable"
  | "repeatable-read"
  | "compile"
  | "long-running"
  | "rolling-back"

// One session in the tree: its victims under it.
export interface BlockingNode {
  session: number
  blockedBy: number // 0 when not blocked
  login: string
  host: string
  program: string
  jobName?: string // the SQL Agent job, when the program is a job step
  status: string // running, runnable, suspended, sleeping, background
  isUser: boolean
  command?: string
  database: string
  statement: string
  objectName?: string // the procedure the statement belongs to
  statementIsLast: boolean // the last batch it sent; nothing runs now
  waitType?: string
  waitMs: number
  waitResource?: string // as SQL Server writes it
  resource?: string // the table (and index) of the lock it waits on
  resourceTable?: string
  lockMode?: string // "wants U, held X"
  isolation: string // "READ COMMITTED"
  openTran: number
  tranName?: string
  logBytes: number
  percentComplete?: number
  tranSeconds: number // how long its oldest transaction has been open; 0 without one
  requestSeconds: number // how long its statement has run; 0 when sleeping
  sleeping: boolean
  kinds: BlockKind[]
  behind: number // sessions waiting behind it, at every level
  longestWaitSec: number
  canKill: boolean
  killRefusal?: string // why kakapo does not kill it (a system session, kakapo's own)
  victims: BlockingNode[]
}

// GET .../blocking: the chain now, across the instance. Read live; nothing is kept.
export interface BlockingTree {
  capturedAt: string
  serverName: string
  database: string
  engineVersion: string
  rcsi: boolean
  offerRcsi: boolean // READ COMMITTED SNAPSHOT is off for the database: the rcsi fix applies
  blocked: number // sessions waiting, in every chain
  heads: BlockingNode[] // head blockers, most sessions behind first
  explain: string[] // the plain sentences, shown first
  notes: string[] // what could not be read
  handover: string // the chain as text for the vendor
}

export const blockingApi = {
  // 404 for an unknown server or database, 502 when the server cannot be reached or read.
  read: (serverId: number, database: string) =>
    request<BlockingTree>(`/api/servers/${serverId}/databases/${encodeURIComponent(database)}/blocking`),
}
