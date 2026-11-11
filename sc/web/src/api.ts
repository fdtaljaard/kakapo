// Typed client for the Go backend. Hand-written for now; generate from OpenAPI when drift hurts.

export type AuthType = "windows" | "sql"

export interface Server {
  id: number
  name: string
  host: string
  auth: AuthType
  username?: string
  database?: string
  encrypt: boolean
  trustCert: boolean
  createdAt: string
  lastVersion?: string
  lastEdition?: string
  lastTestedAt?: string
}

export interface ServerInput {
  // Only on POST /api/servers/test from the edit form: with auth "sql" and an empty password the
  // test uses this server's stored password (phase2-contract.md, revision 3).
  id?: number
  name: string
  host: string
  auth: AuthType
  username: string
  password: string
  database: string
  encrypt: boolean
  trustCert: boolean
}

export interface ConnectionInfo {
  serverName: string
  productVersion: string
  productLevel: string
  edition: string
  version: string
}

// Phase 1: database setup and the Query Store fix (docs/phase1-contract.md).

export type QueryStoreVerdict = "ok" | "drift" | "off" | "unavailable"

// One row per user database.
export interface DatabaseStatus {
  name: string
  state: string // ONLINE, RESTORING, RECOVERING, OFFLINE, ...
  readOnly: boolean
  isSecondaryReplica: boolean
  queryStore: QueryStoreStatus | null // null when it could not be read; then `error` says why
  error?: string
}

export interface QueryStoreStatus {
  capturedAt: string
  actualState: string // OFF | READ_ONLY | READ_WRITE | ERROR
  desiredState: string
  readonlyReason: number
  readonlyReasonText: string
  currentStorageMb: number
  maxStorageMb: number
  storageUsedPercent: number
  captureMode: string // ALL | AUTO | NONE | CUSTOM
  waitStatsCaptureMode: string // ON | OFF; "" on SQL Server 2016
  intervalLengthMinutes: number
  staleQueryThresholdDays: number
  flushIntervalSeconds: number
  maxPlansPerQuery: number
  sizeBasedCleanupMode: string // OFF | AUTO
  verdict: QueryStoreVerdict
  drift: Drift[] // empty when "ok"; every target setting when "off"
  canFix: boolean
  summary: string // "On, 23% full, 15-min intervals, 30 days" | "Off" | "Read-only: storage full"
}

export interface Drift {
  setting: string // the T-SQL option name, e.g. INTERVAL_LENGTH_MINUTES
  current: string
  target: string
}

export interface FixPreview {
  database: string
  engineVersion: string
  changes: Drift[] // empty means nothing to do (or, with inputs, nothing until an input is fixed)
  statement: string // "" when changes is empty
  rollback: string // "" when nothing to do
  warnings: string[]
  // Phase 3g: what the user may set; the preview was built with options, which apply sends back.
  inputs?: FixInput[]
  options?: Record<string, string>
  express?: boolean // true: nothing will be applied; statement is a script to run by hand (Copy script)
  // Phase 5l: the free space on each drive the change uses (docs/phase5l-contract.md); "nofit" holds Apply back.
  room?: RoomLine[]
  // Why there is nothing to do, plainly, when changes is empty and nothing is wrong (kakapo's event session
  // is already installed); shown in place of the dialog's generic "Nothing to change" line.
  note?: string
  // Phase 5m: why Apply is held back although the change could run (a transaction left open in the
  // database); shown in place of Apply, Copy script stays.
  held?: string
  // Phase 5n: the preview's first line, what the change does for this database ("Deletes from Orders read all
  // 1,376,845 rows of Lines today; with this index they read only the matching rows.").
  lead?: string
  // What the change needs in place, each met or not, shown as a checklist; next is the one fix that puts the
  // missing piece in place, before Apply ("before": held says why it waits) or right after it ("after").
  // The switch to FULL recovery needs log backups (docs/sql-express-backups.md).
  requires?: FixRequirement[]
  next?: FixNext
}

export interface FixRequirement {
  label: string
  met: boolean
  detail: string
}

export interface FixNext {
  fixId: string
  label: string
  when: "before" | "after"
  why: string // after the change: why it must follow now
}

// Phase 5l: one drive's room for a change.
export interface RoomLine {
  drive: string // "C:", or the path when not readable
  totalMB: number // 0 when not known
  freeMB: number
  needMB: number
  leftMB: number
  daysLeft?: number
  parts: { label: string; mb: number; basis: string; weak?: boolean; unbounded?: boolean }[]
  state: "fits" | "tight" | "nofit" | "unknown"
  sentence: string
}

// Phase 3g: one value the user may set in a fix's preview (docs/phase3g-contract.md).
export interface FixInput {
  name: string // "folder", "runNow"
  label: string
  value: string // the value the preview was built with; "true"/"false" for a bool
  help: string
  kind: "text" | "bool" | "select"
  warning?: string // what is wrong with this value, shown under the field
  options?: string[] // phase 4b: the values a "select" offers (docs/phase4b-contract.md)
  optionLabels?: Record<string, string> // what an option shows, in plain words ("Any minute (FULL)")
  // SCOPE rule 5: an advanced input sits under the dialog's collapsed "More options"; a read-only one shows
  // its value without a field (docs/phase5a-contract.md).
  advanced?: boolean
  readOnly?: boolean
}

// Phase 3c: the result of a generic fix (docs/phase3c-contract.md). The phase 1 Query Store routes
// returned the before and after QueryStoreStatus; the UI now uses the generic routes for Query Store too.
export interface FixResult {
  preview: FixPreview
  before: string // the value read before, as text
  after: string // the value read back after
  verified: boolean // after == target
  changeId: number
  appliedAt: string
  // Phase 4f: a long fix (CHECKDB now, an index, statistics, the TempDB files) answers at once with
  // pending, its run id and when it started; the outcome is the run's (api.fixRun).
  pending?: boolean
  runId?: string
  startedAt?: string
}

// Phase 4f: one run of a long fix (GET .../fixes/runs/{runId}): pending until it ends, then its result
// or its error as the apply would have answered it (status: the HTTP status code).
export interface FixRun {
  runId: string
  fixId: string
  database: string
  startedAt: string
  endedAt?: string
  pending: boolean
  result?: FixResult
  error?: { status: number; message: string }
  // Phase 5t: a batch run (reorganize-indexes) reports each index as it goes; stopping says a stop was asked for.
  items?: RunItem[]
  stopping?: boolean
}

// One index of a batch run.
export interface RunItem {
  label: string // schema.table · index
  state: "waiting" | "running" | "done" | "differs" | "skipped" | "failed" | "stopped"
  text: string
  changeId?: number
}

// Phase 3c: a named change the app can make, derived from a finding (docs/phase3c-contract.md).
export interface Fix {
  id: string // "max-server-memory", "cost-threshold", "auto-close", "query-store", ...
  title: string // "Max server memory"
  scope: "server" | "database"
  checkId: string
  setting: string // "max server memory (MB)" / "AUTO_CLOSE"
  current: string // "2147483647 MB" / "ON"
  target: string // "26624 MB" / "OFF"
  needsRestart: boolean // false for every fix the app offers
  // The values the preview needs for this particular fix (a table, a flag, index columns, a session id):
  // sent back as the preview's options (docs/phase4c-contract.md).
  options?: Record<string, string>
}

// Phase 2: the database page (docs/phase2-contract.md, revision 2: database first).

export interface Overview {
  instance: InstanceInfo
  database: DatabaseStatus // the chosen database, with its Query Store state
  cards: Card[] // always six, in this order: backups, setup, storage, upkeep, performance, design; about this database
  recentChanges: Change[] // this database only, newest first, at most 10
  // Phase 5f: the line under Recent changes, how far back changes made outside kakapo are seen; absent
  // when the instance could not be read (docs/phase5f-contract.md).
  changeWindow?: ChangeWindow
  // Phase 5i: the cards' findings that are badly wrong right now (docs/phase5i-contract.md, section 3).
  redFlags: RedFlags
  // Phase 5m: the decisions for this database (docs/phase5m-contract.md); the first is row versioning.
  decisions: Decision[]
  checkedAt: string
}

// Phase 5m: one row of the Decisions card (docs/phase5m-contract.md): the verdict, what kakapo found, what to
// do first, what it costs, the buttons and the record.
export type DecisionVerdict = "on" | "fix-first" | "not-enough-evidence" | "worth-considering" | "not-needed"

export interface Decision {
  decision: string // "row-versioning"
  checkId: string // "DEC01"
  title: string
  question: string
  verdict: DecisionVerdict
  tag: string
  tone: "green" | "amber" | "orange" | "grey"
  sentence: string
  found: DecisionLine[]
  doFirst: { text: string; action: DecisionAction }[]
  costs: DecisionLine[]
  notMeasured?: string
  facts: DecisionLine[]
  actions: DecisionAction[]
  thenCheck: string
  sources: string[]
  record?: DecisionRecord // the latest
  recordLine?: string
  reviewDue?: boolean
  history: DecisionRecord[] // newest first
}

export interface DecisionLine {
  label: string
  text: string
  window?: string
}

// Apply a fix (fixId, through its preview), or go to a page under the database (to: "blocking", "cards/storage").
export interface DecisionAction {
  label: string
  fixId?: string
  to?: string
}

export type DecisionChoice = "keep" | "change" | "later"

export interface DecisionRecord {
  id: number
  serverId: number
  database: string
  decision: string
  decidedAt: string
  decidedBy: string
  choice: DecisionChoice
  verdict: DecisionVerdict
  reason?: string
  reviewOn?: string // "2027-02-04"
  changeId?: number
  line?: string
  reviewDue?: boolean
}

export interface DecisionInput {
  choice: "keep" | "later"
  verdict: DecisionVerdict
  reason: string
  reviewOn: string // "" for 90 days from today
}

// Phase 5i: a finding the cards already raise that is a red flag. to is where its button goes, under the
// database page ("blocking", "cards/storage"); rank orders the flags (lower first).
export interface RedFlag {
  checkId: string
  card: CardID
  severity: Severity
  sentence: string
  object: string
  to: string
  rank: number
}

// The first three flags, how many more, and those others in order.
export interface RedFlags {
  flags: RedFlag[]
  more: number
  rest: RedFlag[]
}

// Phase 5f: "running" (the trace's days), "gap" (the trace turned over between two openings), "off" (the
// default trace is off), "unread" (no ALTER TRACE; copy holds the grant).
export interface ChangeWindow {
  state: "running" | "gap" | "off" | "unread"
  text: string
  days: number
  copy?: string
}

export interface InstanceInfo {
  serverId: number
  name: string // the registered display name
  host: string
  serverName: string // @@SERVERNAME
  // The Windows machine SQL Server runs on, the one kakapo runs on, and whether they are the same: steps
  // that need hands on the server (Finding.onServerMachine) are done on machineName. "" when not read.
  machineName: string
  kakapoMachine: string
  onServer: boolean
  edition: string // "Standard Edition (64-bit)"
  productVersion: string // "15.0.4382.1"
  productLevel: string // "RTM", "CU25"
  productName: string // "SQL Server 2019"
  startedAt: string // UTC
  uptimeDays: number // one decimal
  databaseCount: number // user databases
  xeSession?: XESession // phase 5e: kakapo's installed event session, read with the header
}

// Phase 5e: where a history or deadlock report was read from. "kakapo" when kakapo's event session files
// cover the whole window; "kakapo+file" / "kakapo+ring-buffer" when system_health fills the days before
// the session existed (docs/phase5e-contract.md).
export type EventSource = "file" | "ring-buffer" | "kakapo" | "kakapo+file" | "kakapo+ring-buffer"

// Phase 5e: kakapo's Extended Events session as read (docs/phase5e-contract.md).
export interface XESession {
  exists: boolean
  running: boolean
  ours: boolean // the events and target kakapo creates (two events, kakapo.xel)
  events: string[]
  targets: string[]
  fileName?: string // the file target's filename as configured
  currentFile?: string // the file it writes now
  maxFileMB?: number
  maxFiles?: number
  dispatchSeconds?: number
  startupState: boolean
  startedAt?: string // UTC, while it runs
  eventsCaptured: number // the file target's execution_count
  thresholdSeconds: number // the blocked process threshold in use; -1 when not read
  otherSessions?: string[] // other sessions that record the blocked process report
  error?: string // what could not be read
}

// Phase 5e: the instance page's "Installed helpers" row (GET /api/servers/{id}/helpers).
export interface InstalledHelpers {
  serverName: string
  database: string // the database the fix routes go through; "" when no user database is online
  sentence: string // the row's sentence first
  helpers: InstalledHelper[]
}

export type HelperState = "running" | "installed" | "stopped" | "not installed" | "not kakapo's" | "unknown"

export interface InstalledHelper {
  id: string // "xe-session", "maintenance-proc" (phase 5p)
  title: string
  state: HelperState
  sentence: string
  details: string[]
  fixId?: string // install-xe-session (Create), remove-xe-session or remove-maintenance-proc (Remove)
  fixLabel?: string
  fixTitle?: string // the fix dialog's title
  session?: XESession
}

export type CardID = "backups" | "setup" | "storage" | "upkeep" | "performance" | "design"
export type CardStatus = "green" | "amber" | "red" | "grey"

export interface Card {
  id: CardID
  title: string
  question: string
  status: CardStatus
  statusText: string // "No problem found" | "3 things to do" | "Not checked yet" | ...
  verdict: string // one sentence with the number; "" when grey
  facts: Fact[] // 0 to 4
  toDo: number
  forClientIT: number
  fixable: number
  asOf: string | null // null when grey
  note: string // grey cards: what the card will show; live cards: "" or a caveat
}

export interface Fact {
  label: string
  value: string
  tone: "normal" | "bad" | "worse"
}

// Phase 3a: the finding model and the card detail (docs/phase3a-contract.md).

export type Severity = "Critical" | "High" | "Medium" | "Low" | "Info" // scan ranks 1..5
export type Owner = "Us" | "Client IT" | "Vendor"
export type When = "Online" | "Window" | "Decide first" | "None"
export type Effort = "Quick win" | "Moderate" | "Project" | "None"

export interface Finding {
  checkId: string // "BAK05"
  // "Do this" needs hands on the SQL Server machine itself (a Windows right, the power plan, a service, the
  // disk), not a statement from anywhere.
  onServerMachine?: boolean
  title: string // the check's title from the catalogue, e.g. "Worst-case data loss"
  severity: Severity
  owner: Owner
  when: When
  effort: Effort
  object: string // the database name here
  finding: string // one sentence with the number that proves it
  why: string // one sentence
  doThis: string // the statement, or one sentence naming who does what
  thenCheck: string
  evidence: string // the raw numbers
  fixId?: string // present when the app can apply the change (phase 3c)
  // A second way out with its own Apply (BAK02: the schedule is fixId, "recovery-model" the switch to SIMPLE).
  altFixId?: string
  // Phase 3e, the Performance card: which clock the figure is on. The card page groups by it ("Right
  // now", "Since restart", "Last 7 days"); the other cards leave it out.
  clock?: "since-restart" | "now" | "window"
}

export interface CheckStatus {
  checkId: string
  title: string
  status: "passed" | "finding" | "skipped"
  note: string // why skipped; "" otherwise
}

// Phase 5k (docs/phase5k-contract.md): the Performance screen's "better or worse than usual" and what
// made the difference.
export interface PerformanceTrend {
  verdict: "better" | "worse" | "same" | "" // "" when there is nothing to compare with (message says why)
  words: string
  sentence: string
  day: string // "today", "yesterday", "Wed 4 Nov"
  figures: TrendFigure[]
  compared: string
  days: TrendDay[]
  movers: TrendMovers
  message: string
}

export interface TrendFigure {
  id: "queries" | "cpu" | "waiting"
  label: string
  value: string
  change: string
  usual: string
  tone: "better" | "worse" | "same"
  pct: number
}

export interface TrendDay {
  date: string
  label: string
  kind: "working" | "weekend" | "quiet" | "judged" | "none"
  cpuPerQueryMs: number
  queriesPerHour: number
  planChanges: number
  note: string
}

export interface TrendMovers {
  faster: number
  savedMsPerDay: number
  slower: number
  addedMsPerDay: number
  slowerList: TrendMover[]
  fasterList: TrendMover[]
  error: string
}

export interface TrendMover {
  queryHash: string
  objectName: string
  text: string
  since: string // UTC
  beforeCpuMs: number
  nowCpuMs: number
  nowRuns: number
  cpuMsPerDay: number // added (positive) or saved (negative)
  after: string
  afterAt: string | null
}

export interface CardDetail {
  card: Card // the same object the overview carries for this card
  findings: Finding[] // ranked: severity, effort, when, checkId
  checks: CheckStatus[] // every check behind the card, in catalogue order
  checkdbRoom?: CheckdbRoom | null // storage only: the last stored STO10 result; omitted or null otherwise
  fixes?: Fix[] // the fixes available on this card right now (setup, upkeep, backups); only fixes whose finding fired, and on backups the remove fix
  deadlocks?: DeadlockReport | null // performance only: the last stored DLK01 report; omitted or null otherwise
  blockingHistory?: BlockingHistory | null // performance only (phase 5d): the last stored BLK01 history; omitted or null otherwise
  trend?: PerformanceTrend | null // performance only (phase 5k): better or worse than usual; omitted or null otherwise
  checkdbRun?: CheckdbRun | null // backups and storage only: the last DBCC CHECKDB run from kakapo; omitted or null otherwise
  maintenancePlan?: MaintenancePlan // upkeep only (phase 5b): the Maintenance plan row; omitted otherwise
  drives?: StorageDrives // storage only (phase 5l): the answer first, one bar per drive; omitted otherwise
  foreignKeys?: DesignForeignKeys // design only (phase 5n): the keys that would cost work if rows were deleted; omitted otherwise
  noKeyTables?: DesignNoKeyTables // design only (phase 5s): the tables under 10,000 rows without a primary key; omitted otherwise
}

// Phase 5n: the Design card's fold (docs/phase5n-contract.md, section 3): foreign keys without an index on
// tables of 10,000 rows or more with no delete seen in the window, largest first, one line each, no button.
export interface DesignForeignKeys {
  heading: string // "Missing indexes on foreign keys, not needed yet", or "…, too early to tell" while the window is 7 days or less
  tooEarly: boolean
  window: string // "the restart on 1 Oct"; "" when not read
  keys: { name: string; table: string; rows: number; parent: string; columns: string[]; text: string }[]
}

// Phase 5s: the Design card's fold of the smaller tables without a primary key (docs/phase5s-contract.md,
// decision 2): tables with rows but under 10,000, largest first, one line each, no button.
export interface DesignNoKeyTables {
  heading: string // "Smaller tables without a primary key"
  tables: { table: string; rows: number; text: string }[] // text: "Codes (2,410 rows, heap)"
}

// Phase 5l: the Storage card's answer, "Will it run out of room?" (docs/phase5l-contract.md part 5).
export interface StorageDrives {
  sentence: string
  drives: { drive: string; totalMB: number; freeMB: number; databaseMB: number; holds: string[]; daysLeft?: number; state: "fits" | "tight" }[]
}

// Phase 5b: the maintenance plan of a database (docs/phase5b-contract.md): one line per piece of regular
// maintenance with where it stands, the fix that creates it, and why that cadence (with its source).
export type PlanStatus =
  | "in-place"
  | "other-job"
  | "missing"
  | "disabled"
  | "failing"
  | "never-ran"
  | "not-needed"
  | "optional"
  | "unknown"
  | "overdue" // phase 5p, Express: kakapo's procedure has not run it since its time

export interface PlanPiece {
  id: string // "full-backup", "log-backup", "integrity-check", "statistics", "indexes", "history-cleanup", "alerts", "archive"
  title: string // "Full backup", "Archive dbo.MailLog"
  cadence: string // what kakapo proposes: "daily 22:00"
  status: PlanStatus
  job?: string // the job that covers it
  schedule?: string // that job's schedule in words
  lastRun?: string // "2026-11-02 01:00", server time
  lastOutcome?: string // "succeeded", "failed", ...
  lastDuration?: string // "4 min"
  nextRun?: string
  fixId?: string // the fix that creates it (Create)
  options?: Record<string, string> // that fix's options, so it fits the plan's times
  source: string // why this cadence, with the source
  note?: string
}

export interface MaintenancePlan {
  pieces: PlanPiece[]
  missing: number // pieces the composite fix can create (index work is optional and not counted)
  fixId?: string // "maintenance-plan" when it has something to create
  express?: boolean
  order: string // why these times, with the source
  note?: string
}

// Phase 3e: DLK01, the deadlocks in system_health over the last 7 days grouped by the pair of
// procedures, read on request (docs/phase3e-contract.md).
export interface DeadlockReport {
  capturedAt: string
  durationSeconds: number
  days: number // 7
  total: number
  source: EventSource
  pairs: DeadlockPair[] // by count desc
  findings: Finding[] // one DLK01 finding per pair with count >= 1, ranked
  health?: HealthEvent[] // phase 3f: SYSTEM and IO_SUBSYSTEM warnings of the window, newest first, at most 50; omitted when none
  kakapoSince?: string // phase 5e: when kakapo's session files begin (UTC); the deadlocks before are system_health's
  withReader?: number // phase 5m: deadlocks with a reader in them (S or IS at read committed); absent before 5m
  withoutReader?: number
}

// Phase 3f, item 21: one sp_server_diagnostics_component_result event of the system_health session in
// state warning or error, read with the deadlocks (docs/phase3f-contract.md).
export interface HealthEvent {
  at: string
  component: "SYSTEM" | "IO_SUBSYSTEM" | string
  state: "warning" | "error" | string
  detail: string // what it reported, in words
}

// Phase 5d: BLK01, a week of blocking from the system_health session's five-minute snapshots, grouped by
// the head blocker's statement and the object it held, read on request (docs/phase5d-contract.md).
export interface BlockingHistory {
  capturedAt: string
  durationSeconds: number
  days: number // 1 to 7
  source: EventSource
  snapshots: number // snapshots (system_health) and lock monitor passes (kakapo's session) that saw blocking in this database
  reportsTotal: number // blocked-process reports in this database
  missedNote: string // what the history cannot show (blocks shorter than the snapshot)
  thresholdSeconds: number // the blocked process threshold (s); 0 means only the five-minute snapshots exist
  leaders: BlockingLeader[] // most occurrences first, at most 20
  byHour: BlockingHour[] // every hour of the window on the server's clock, oldest first
  findings: Finding[] // one BLK01 finding per leader, ranked (Info for a leader seen once)
  notes: string[] // what could not be read
  kakapoSince?: string // phase 5e: when kakapo's session files begin (UTC); omitted when they were not read
  daySources?: { day: string; source: "kakapo" | "system_health" | "both" }[] // phase 5e: where each server day came from
  classes?: BlockingClass[] // phase 5m: the waits by class; absent in a history read before 5m
  classLine?: string // phase 5m: what READ COMMITTED SNAPSHOT would remove and what not
}

// Phase 5m: the waits of one class (docs/phase5m-contract.md, section 2).
export interface BlockingClass {
  class: "writes-behind-reads" | "reads-behind-writes" | "locking-readers" | "schema-locks" | "writes-behind-writes" | "unknown"
  episodes: number
  totalWaitSec: number
  longestWaitSec: number
  mostAtOnce: number
  days: number
  byDay: { day: string; waitSec: number }[]
  waiters?: { program: string; login: string; host: string; episodes: number; waitSec: number }[]
  blockers?: { program: string; login: string; host: string; episodes: number; waitSec: number }[]
}

// How a head blocker is classified (the live blocking page's kinds, plus a blocker in another database).
export type HistoryKind = "sleeping" | "implicit" | "serializable" | "repeatable-read" | "long-running" | "cross-database"

export interface BlockingLeader {
  key: string
  blockerStatement: string // normalised: whitespace collapsed, literals as ?
  procedure?: string // when the blocker ran a procedure
  blockerProgram: string
  blockerLogin: string
  blockerHost: string
  object: string // the table its victims waited on, or the wait resource when it could not be named
  database: string
  occurrences: number // distinct blocking episodes (one blocker transaction counts once)
  snapshots: number // five-minute snapshots that saw it
  passes?: number // phase 5e: lock monitor passes kakapo's session reported it in (one every threshold interval)
  longestWaitSec: number
  totalBlockedSessions: number
  kinds: HistoryKind[]
  first: string
  last: string
  byDay: { day: string; count: number }[] // server days
  sampleVictimStatement: string
  sampleBlockerStatement: string
  lockMode?: string
  queryHash?: string // when Query Store holds the blocker's exact statement: Investigate opens the query
  heldWhileWaiting?: boolean // system_health wrote it as the head of a chain: an open transaction held while it waited
}

export interface BlockingHour {
  hour: string // "2026-11-03T14:00", the server's clock
  reports: number
  sessionsBlocked: number // the most sessions blocked in one snapshot of the hour
}

export interface DeadlockPair {
  procedures: [string, string] // e.g. ["dbo.USP_PICKING...", "dbo.IntegrationPost..."]
  count: number
  last: string
  objects: string[] // the tables involved, distinct
  lastVictimStatement: string // first 500 chars
  withReader?: number // phase 5m: how many had a reader in them (S or IS at read committed)
}

// Phase 3b: STO10, room to run DBCC CHECKDB on this database, read on request (docs/phase3b-contract.md).
export interface CheckdbRoom {
  capturedAt: string
  durationSeconds: number
  estimateKB: number
  needMB: number
  tempdbFreeMB: number
  tempdbRoomMB: number
  dataVolume: string // "D:\"
  dataFreeMB: number
  dataSizeMB: number
  finding: Finding // STO10 as a finding row (Info when passed)
}

// Phase 4d: one DBCC CHECKDB run from kakapo (the checkdb-now fix), stored and shown on the Backups card
// (docs/phase4d-contract.md, part 2).
export interface CheckdbRun {
  ranAt: string // when it started
  durationSeconds: number
  physicalOnly: boolean // WITH PHYSICAL_ONLY
  clean: boolean
  outcome: string // "No errors found", or SQL Server's summary of the errors found
  messages: string[] // what SQL Server said, in order ("Msg 8928, Level 16: ..."); [] when nothing
}

// One row of the change table.
export interface Change {
  id: number
  serverId: number
  database: string
  changedAt: string
  object: string // "Query Store"
  changeType: string // "SETTING"
  description: string
  result: string // "recorded by hand" for a change recorded by hand (phase 5c); "seen on the server" (phase 5f)
  // Phase 5f: a change seen on the server that names its objects (a deploy), ready to copy: the line and
  // the objects, one per line.
  copy?: string
}

// Phase 5c: a change made outside kakapo, recorded by hand (docs/phase5c-contract.md).
export const changeTypes = ["PROCEDURE", "VIEW", "INDEX", "STATISTICS", "SETTING", "OTHER"] as const
export const recordedByHand = "recorded by hand"
// Phase 5f: a change kakapo read from SQL Server's default trace, made outside kakapo (docs/phase5f-contract.md).
export const seenOnServer = "seen on the server"

export interface RecordChangeInput {
  changedAt: string // server time "2026-11-03T14:20"; "" is now
  object: string
  changeType: (typeof changeTypes)[number]
  description: string
}

// Phase 4e, part 2: one long operation in progress on the instance (GET .../progress), which the fix
// dialog polls while a long fix applies. percent is 0 when SQL Server reports none (index builds,
// statistics updates); remainingSeconds is 0 when it gives no estimate; mine is true on kakapo's own
// sessions.
export interface Progress {
  sessionId: number
  command: string // "DBCC CHECKDB", "CREATE INDEX", "BACKUP DATABASE", ...
  database: string // "" when the request has none
  percent: number
  elapsedSeconds: number
  remainingSeconds: number
  startedAt: string
  waitType: string
  program: string // the application name
  mine: boolean
}

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

// Dev only: `VITE_MOCK_API=1 npm run dev` serves fixture data from api.mock.ts. In a production build
// import.meta.env.DEV is false, so this branch and the mock module are dropped.
const useMock = import.meta.env.DEV && import.meta.env.VITE_MOCK_API === "1"

// Exported for the per-feature clients of phase 4c (api-queries.ts, api-blocking.ts, api-sample.ts).
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  if (useMock) {
    const { mockRequest } = await import("./api.mock")
    return mockRequest<T>(path, init)
  }
  const res = await fetch(path, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    let message = res.statusText
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) message = body.error
    } catch {
      /* no JSON body */
    }
    throw new ApiError(message, res.status)
  }
  return (await res.json()) as T
}

export interface Health {
  ok: boolean
  time: string
  version: string // the build stamp of the running exe
}

export const api = {
  health: () => request<Health>("/api/health"),
  listServers: () => request<Server[]>("/api/servers"),
  getServer: (id: number) => request<Server>(`/api/servers/${id}`),
  // Tests the connection first; 502 and nothing changed when it fails. An empty password with
  // auth "sql" keeps the stored one.
  updateServer: (id: number, input: ServerInput) =>
    request<Server>(`/api/servers/${id}`, { method: "PUT", body: JSON.stringify(input) }),
  testConnection: (input: ServerInput) =>
    request<ConnectionInfo>("/api/servers/test", { method: "POST", body: JSON.stringify(input) }),
  registerServer: (input: ServerInput) =>
    request<Server>("/api/servers", { method: "POST", body: JSON.stringify(input) }),
  listDatabases: (serverId: number) => request<DatabaseStatus[]>(`/api/servers/${serverId}/databases`),
  // Phase 3c, the generic fix routes (Query Store too, with fixId "query-store"). Preview never writes;
  // 404 when the fix id is unknown or its finding did not fire, 409 when it cannot be applied here
  // (secondary replica, not online), 502 when SQL Server refuses the apply (the message names the
  // permission).
  // Phase 3g: options are what the user set in the preview ({folder, runNow} for the backup schedule);
  // apply sends the options the preview echoed. Without options nothing is sent, as before.
  previewFix: (serverId: number, database: string, fixId: string, options?: Record<string, string>) =>
    request<FixPreview>(`${fixPath(serverId, database, fixId)}/preview`, fixInit(options)),
  applyFix: (serverId: number, database: string, fixId: string, options?: Record<string, string>) =>
    request<FixResult>(`${fixPath(serverId, database, fixId)}/apply`, fixInit(options)),
  overview: (serverId: number, database: string) => request<Overview>(`${databasePath(serverId, database)}/overview`),
  // Read live; 404 for an unknown server, database or card id.
  cardDetail: (serverId: number, database: string, card: string) =>
    request<CardDetail>(`${databasePath(serverId, database)}/cards/${encodeURIComponent(card)}`),
  // Runs STO10 now (DBCC CHECKDB WITH ESTIMATEONLY) and stores it. 409 when the database is not online;
  // 502 when the DBCC fails, with the reason (e.g. the login needs db_owner or sysadmin).
  runCheckdbRoom: (serverId: number, database: string) =>
    request<CheckdbRoom>(`${databasePath(serverId, database)}/cards/storage/checkdb-room`, { method: "POST" }),
  // Runs DLK01 now (reads the system_health session) and stores it. 502 when the session cannot be
  // read, with the reason (e.g. the login needs VIEW SERVER STATE).
  runDeadlockReport: (serverId: number, database: string) =>
    request<DeadlockReport>(`${databasePath(serverId, database)}/cards/performance/deadlocks`, { method: "POST" }),
  changes: (serverId: number, database: string, limit = 10) =>
    request<Change[]>(`${databasePath(serverId, database)}/changes?limit=${limit}`),
  // Phase 5c: records a change made by hand; writes the change log only. 400 for a field that is not
  // right, 404 for an unknown server, 502 when a server time cannot be placed (the server's clock).
  recordChange: (serverId: number, database: string, input: RecordChangeInput) =>
    request<Change>(`${databasePath(serverId, database)}/changes`, { method: "POST", body: JSON.stringify(input) }),
  // Phase 4e: the long operations in progress for this database, tempdb, no database or kakapo's own
  // sessions; [] when nothing runs. 404 for an unknown server, 502 when the server cannot be read.
  progress: (serverId: number, database: string) => request<Progress[]>(`${databasePath(serverId, database)}/progress`),
  // Phase 4f: a long fix's run, polled while it is pending; 404 for an unknown run (ended over 24 hours
  // ago, or before the exe restarted).
  fixRun: (serverId: number, database: string, runId: string) =>
    request<FixRun>(`${databasePath(serverId, database)}/fixes/runs/${encodeURIComponent(runId)}`),
  // Phase 5t: stop a pending run; the statement in flight is cancelled and keeps its progress. 409 when it has ended.
  stopRun: (serverId: number, database: string, runId: string) =>
    request<FixRun>(`${databasePath(serverId, database)}/fixes/runs/${encodeURIComponent(runId)}/stop`, { method: "POST" }),
  // Phase 5m: the records of the row versioning decision, newest first; Record a decision (keep, later). 400 for
  // keep without a reason, 404 for an unknown server.
  decisions: (serverId: number, database: string) => request<DecisionRecord[]>(`${databasePath(serverId, database)}/decisions`),
  recordDecision: (serverId: number, database: string, input: DecisionInput) =>
    request<DecisionRecord>(`${databasePath(serverId, database)}/decisions`, { method: "POST", body: JSON.stringify(input) }),
  // Remembers the database on the registration, so the instance opens straight into it next time.
  rememberDatabase: (serverId: number, database: string) =>
    request<Server>(`/api/servers/${serverId}/database`, { method: "PUT", body: JSON.stringify({ database }) }),
}

function databasePath(serverId: number, database: string) {
  return `/api/servers/${serverId}/databases/${encodeURIComponent(database)}`
}

function fixInit(options?: Record<string, string>): RequestInit {
  return options ? { method: "POST", body: JSON.stringify({ options }) } : { method: "POST" }
}

function fixPath(serverId: number, database: string, fixId: string) {
  return `${databasePath(serverId, database)}/fixes/${encodeURIComponent(fixId)}`
}
