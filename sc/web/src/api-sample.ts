// Phase 4c, part 3: the Performance card's live sample (docs/phase4c-contract.md). The create-index and
// drop-index fixes go through api.previewFix / api.applyFix with their options.

import { request } from "./api"

export const sampleDefaultSeconds = 30
export const sampleMinSeconds = 10
export const sampleMaxSeconds = 120

// Where a sentence belongs; "queries" and "blocking" link to their investigation.
export type SampleArea =
  | "blocking"
  | "running"
  | "growth"
  | "poison"
  | "waits"
  | "cpu"
  | "files"
  | "memory"
  | "queries"

export interface SampleLine {
  area: SampleArea
  text: string
  queryHash?: string // a "queries" line: the query it names
}

export interface SampleWait {
  type: string
  group: string // WRK01's group: LOCK, IO, CPU ...
  words: string // "blocking", "reading from disk"
  waitMs: number
  signalMs: number
  tasks: number
  avgMs: number
  poison: boolean
}

export interface SampleCPU {
  sql: number // average % used by SQL Server
  other: number // average % used by other programs
  idle: number
  records: number
  inWindow: boolean // false: no ring buffer record fell inside; the minute before the sample
  runnableStart: number
  runnableEnd: number
  schedulers: number
  signalWaitPct: number
  hasSignalShare: boolean
}

export interface SampleQuery {
  queryHash: string
  database: string
  objectName: string
  text: string
  cpuMs: number
  runs: number
  reads: number
  durationMs: number
}

export interface SampleFile {
  database: string
  file: string
  type: string // ROWS, LOG
  reads: number
  writes: number
  readMb: number
  writeMb: number
  msPerRead: number
  msPerWrite: number
}

export interface SampleBlocker {
  session: number
  blocked: number
  longestSec: number
  database: string
  login: string
  host: string
  program: string
  status: string
  statement: string
  bothEnds: boolean // seen in both snapshots
}

export interface SampleRunning {
  session: number
  command: string
  database: string
  percentDone: number
  elapsedSeconds: number
  leftSeconds: number
}

export interface SampleGrowth {
  database: string
  file: string
  type: string
  beforeMb: number
  afterMb: number
}

export interface SampleMemory {
  pleStart: number // -1 when not read
  pleEnd: number
  pendingStart: number
  pendingEnd: number
}

// One live sample: two snapshots `seconds` apart and the difference, in DESIGN 5.3's sentences.
export interface PerfSample {
  database: string
  capturedAt: string // the end
  seconds: number // asked for
  elapsedSeconds: number // as the server's clock measured it
  serverName: string
  verdict: string
  lines: SampleLine[] // empty when nothing fired
  waits: SampleWait[]
  waitsError?: string
  cpu: SampleCPU | null
  cpuError?: string
  queries: SampleQuery[]
  queriesError?: string
  files: SampleFile[]
  filesError?: string
  blocking: { headBlockers: SampleBlocker[]; blocked: number; longestSec: number }
  blockingError?: string
  running: SampleRunning[]
  runningError?: string
  growth: SampleGrowth[]
  memory: SampleMemory | null
  memoryError?: string
}

function samplePath(serverId: number, database: string) {
  return `/api/servers/${serverId}/databases/${encodeURIComponent(database)}/cards/performance/sample`
}

export const sampleApi = {
  // The last stored sample, or null when none was run (reads the case file only).
  lastSample: (serverId: number, database: string) => request<PerfSample | null>(samplePath(serverId, database)),
  // Runs a sample now and returns when it is done (seconds + a few). 400 for seconds out of range; 502 when
  // the server cannot be read (e.g. the login needs VIEW SERVER STATE).
  runSample: (serverId: number, database: string, seconds: number) =>
    request<PerfSample>(samplePath(serverId, database), { method: "POST", body: JSON.stringify({ seconds }) }),
}
