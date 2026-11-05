import { mockBlocking } from "./api-blocking.mock"
import { mockBlockingHistory, withBlockingHistory } from "./api-blocking-history.mock"
import { mockMaintenance, withMaintenancePlan } from "./api-maintenance.mock"
import { mockQueries } from "./api-queries.mock"
import { mockProgress, startMockRun } from "./api-progress.mock"
import { mockSample } from "./api-sample.mock"
import { mockTables, withArchives } from "./api-tables.mock"
import { mockTuning, withRecordedChanges } from "./api-tuning.mock"
import { mockHelpers } from "./api-helpers.mock"

// Dev-only fixture backend. Loaded by api.ts only when import.meta.env.DEV and VITE_MOCK_API=1,
// so it never reaches a production build. One server, three databases: Query Store ok, ok, off;
// Backups green, amber (the backup schedule fix for the log job), red (the fix for the full job); Setup green, amber (two fixes), amber (two fixes, Query Store one of them);
// Storage green, amber (with a stored Room for CHECKDB), amber; Upkeep green, amber (a failed backup job,
// stale statistics with a fix), amber (Agent stopped, no operator); Performance green, amber (with a stored
// deadlock report), red (restarted 9 hours ago, so its overview says so too). The server
// remembers WarehouseLive, so Home opens straight into it; /instances/1 shows "Database: WarehouseLive", and
// the picker only once the database is cleared in Edit.
// A server added through the form gets the same databases; one whose host contains "down" is unreachable,
// and an edit (PUT /api/servers/{id}) to such a host fails with 502 and changes nothing.

import {
  ApiError,
  type Card,
  type CardDetail,
  type CheckdbRoom,
  type CheckStatus,
  type Change,
  type ConnectionInfo,
  type DatabaseStatus,
  type DeadlockPair,
  type DeadlockReport,
  type HealthEvent,
  type Drift,
  type Fact,
  type Finding,
  type Fix,
  type FixPreview,
  type FixResult,
  type InstanceInfo,
  type Overview,
  type QueryStoreStatus,
  type Server,
  type ServerInput,
} from "./api"

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const now = () => new Date().toISOString()
const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()

const server: Server = {
  id: 1,
  name: "Client A production",
  host: "CLIENTSQL01\\WMS",
  auth: "windows",
  database: "WarehouseLive",
  encrypt: false,
  trustCert: false,
  createdAt: "2026-11-03T06:00:00Z",
  lastVersion: "16.0.4255.2",
  lastEdition: "Standard Edition (64-bit)",
  lastTestedAt: "2026-11-03T06:00:05Z",
}

const servers: Server[] = [server]

const target: Record<string, string> = {
  OPERATION_MODE: "READ_WRITE",
  QUERY_CAPTURE_MODE: "AUTO",
  MAX_STORAGE_SIZE_MB: "2048",
  SIZE_BASED_CLEANUP_MODE: "AUTO",
  INTERVAL_LENGTH_MINUTES: "15",
  STALE_QUERY_THRESHOLD_DAYS: "30",
  DATA_FLUSH_INTERVAL_SECONDS: "900",
  MAX_PLANS_PER_QUERY: "200",
  WAIT_STATS_CAPTURE_MODE: "ON",
}

function okStatus(currentStorageMb: number): QueryStoreStatus {
  const pct = Math.round((currentStorageMb / 2048) * 1000) / 10
  return {
    capturedAt: now(),
    actualState: "READ_WRITE",
    desiredState: "READ_WRITE",
    readonlyReason: 0,
    readonlyReasonText: "",
    currentStorageMb,
    maxStorageMb: 2048,
    storageUsedPercent: pct,
    captureMode: "AUTO",
    waitStatsCaptureMode: "ON",
    intervalLengthMinutes: 15,
    staleQueryThresholdDays: 30,
    flushIntervalSeconds: 900,
    maxPlansPerQuery: 200,
    sizeBasedCleanupMode: "AUTO",
    verdict: "ok",
    drift: [],
    canFix: true,
    summary: `On, ${Math.round(pct)}% full, 15-min intervals, 30 days`,
  }
}

const offStatus: QueryStoreStatus = {
  ...okStatus(0),
  actualState: "OFF",
  desiredState: "OFF",
  currentStorageMb: 0,
  maxStorageMb: 100,
  storageUsedPercent: 0,
  captureMode: "ALL",
  waitStatsCaptureMode: "OFF",
  intervalLengthMinutes: 60,
  staleQueryThresholdDays: 367,
  sizeBasedCleanupMode: "OFF",
  verdict: "off",
  drift: Object.entries(target).map(([setting, value]) => ({
    setting,
    current: setting === "OPERATION_MODE" ? "OFF" : "",
    target: value,
  })),
  summary: "Off",
}

// Mutable so that apply turns the line green (and the Setup card with it) until the page is reloaded.
const databases: DatabaseStatus[] = [
  { name: "WarehouseLive", state: "ONLINE", readOnly: false, isSecondaryReplica: false, queryStore: okStatus(471) },
  { name: "WarehouseTest", state: "ONLINE", readOnly: false, isSecondaryReplica: false, queryStore: okStatus(588) },
  { name: "Warehouse_Archive", state: "ONLINE", readOnly: false, isSecondaryReplica: false, queryStore: offStatus },
]

// Newest first. Apply adds a row at the top.
const changes: Change[] = [
  {
    id: 2,
    serverId: 1,
    database: "WarehouseLive",
    changedAt: hoursAgo(20),
    object: "Query Store",
    changeType: "SETTING",
    description:
      "Query Store on WarehouseLive: OPERATION_MODE OFF -> READ_WRITE, QUERY_CAPTURE_MODE ALL -> AUTO, MAX_STORAGE_SIZE_MB 100 -> 2048, INTERVAL_LENGTH_MINUTES 60 -> 15",
    result: "On, 0% full, 15-min intervals, 30 days",
  },
  {
    id: 1,
    serverId: 1,
    database: "WarehouseTest",
    changedAt: hoursAgo(24 * 6 + 3),
    object: "Query Store",
    changeType: "SETTING",
    description: "Query Store on WarehouseTest: MAX_PLANS_PER_QUERY 1000 -> 200",
    result: "On, 28% full, 60-min intervals, 30 days; still differs: INTERVAL_LENGTH_MINUTES 60 (target 15)",
  },
]

let nextChangeId = 3

function settingsList(changes: Drift[]) {
  return changes.map((c) => `    ${c.setting} = ${c.target}`).join(",\n")
}

function buildPreview(db: DatabaseStatus): FixPreview {
  const qs = db.queryStore!
  const changes = qs.drift
  const quoted = `[${db.name.replace(/]/g, "]]")}]`
  const warnings: string[] = []
  if (changes.some((c) => c.setting === "INTERVAL_LENGTH_MINUTES") && qs.verdict !== "off") {
    warnings.push("Changing INTERVAL_LENGTH_MINUTES clears the runtime statistics Query Store has collected so far.")
  }
  warnings.push("Query Store can grow to 2 GB inside the database (PRIMARY filegroup), and that adds to its backups.")
  const statement =
    qs.verdict === "off"
      ? `ALTER DATABASE ${quoted} SET QUERY_STORE = ON (\n${settingsList(changes)}\n);`
      : `ALTER DATABASE ${quoted} SET QUERY_STORE (\n${settingsList(changes)}\n);`
  const rollback =
    qs.verdict === "off"
      ? `ALTER DATABASE ${quoted} SET QUERY_STORE = OFF;`
      : `ALTER DATABASE ${quoted} SET QUERY_STORE (\n${changes.map((c) => `    ${c.setting} = ${c.current}`).join(",\n")}\n);`
  return { database: db.name, engineVersion: server.lastVersion!, changes, statement, rollback, warnings }
}

// Warehouse_Archive's instance restarted 9 hours ago (the Performance card's restart note); the others 5.2
// days ago. Mock simplification: the instance is kept per database.
function instanceInfo(srv: Server, db?: DatabaseStatus): InstanceInfo {
  const hours = db?.name === "Warehouse_Archive" ? 9.4 : 24 * 5.2
  return {
    serverId: srv.id,
    name: srv.name,
    host: srv.host,
    serverName: srv.host.toUpperCase(),
    edition: "Standard Edition (64-bit)",
    productVersion: "16.0.4255.2",
    productLevel: "CU25",
    productName: "SQL Server 2022",
    startedAt: hoursAgo(hours),
    uptimeDays: Math.round((hours / 24) * 10) / 10,
    databaseCount: databases.length,
  }
}

// Phase 3c: the Setup and configuration card and its detail per database (phase3c-contract.md), with the
// generic fixes. WarehouseLive green (only the INS01 Info summary; Query Store ok); WarehouseTest amber
// (INS05 cost threshold 5, DBC02 AUTO_CLOSE on, CPY04 an orphaned user); Warehouse_Archive amber (DBC10
// Query Store off, INS06 optimize for ad hoc off). Texts are the scan's (01-QuickScan.sql section 1 and
// 2, PerformanceTriage.sql CPY04, Checks.csv). Mock simplification: the instance settings are kept per
// database, so each database shows its own spread; on a real server an INS finding shows on every
// database page of the instance. Apply changes the state, so the finding goes until the page reloads.

const setupChecks: { checkId: string; title: string; owner: Finding["owner"]; thenCheck: string }[] = [
  { checkId: "INS01", title: "SQL Server version and support status", owner: "Client IT", thenCheck: "Re-run the health check: INS01 shows the new build." },
  { checkId: "INS02", title: "Max server memory is configured", owner: "Us", thenCheck: "Re-run tomorrow: INS02 gone and no new paging on the host." },
  { checkId: "INS03", title: "Max server memory is sized sensibly", owner: "Us", thenCheck: "Re-run tomorrow: INS03 gone." },
  { checkId: "INS04", title: "Max degree of parallelism (MAXDOP)", owner: "Us", thenCheck: "Re-run after a business day: parallelism waits in WRK01 lower." },
  { checkId: "INS05", title: "Cost threshold for parallelism", owner: "Us", thenCheck: "Re-run after a business day: CXPACKET share in WRK01 lower." },
  { checkId: "INS06", title: "Plan cache bloat (optimize for ad hoc workloads)", owner: "Us", thenCheck: "Re-run after a day: single-use MB in INS06 lower." },
  { checkId: "INS07", title: "Instant file initialization", owner: "Client IT", thenCheck: "Re-run: INS07 gone (needs a SQL Server restart to take effect)." },
  { checkId: "INS08", title: "Lock pages in memory", owner: "Client IT", thenCheck: "Re-run after restart: INS08 gone." },
  { checkId: "INS09", title: "Priority boost is off", owner: "Us", thenCheck: "Re-run after restart: INS09 gone." },
  { checkId: "INS10", title: "Backup compression default", owner: "Us", thenCheck: "Next backup is smaller in msdb.dbo.backupset (compressed_backup_size)." },
  { checkId: "INS11", title: "Server-wide index fill factor", owner: "Us", thenCheck: "Re-run: INS11 gone." },
  { checkId: "INS12", title: "Windows low on memory", owner: "Client IT", thenCheck: "Re-run after a day: no new low-memory signal in INS12." },
  { checkId: "INS13", title: "Windows power plan", owner: "Client IT", thenCheck: "Re-run: INS13 gone. CPU-bound queries should be measurably faster." },
  { checkId: "DBC02", title: "AUTO_CLOSE is off", owner: "Us", thenCheck: "Re-run: DBC02 gone." },
  { checkId: "DBC03", title: "AUTO_SHRINK is off", owner: "Us", thenCheck: "Re-run: DBC03 gone." },
  { checkId: "DBC04", title: "Page verify uses CHECKSUM", owner: "Us", thenCheck: "Re-run: DBC04 gone." },
  { checkId: "DBC05", title: "Automatic statistics are on", owner: "Us", thenCheck: "Re-run: DBC05 gone." },
  { checkId: "DBC06", title: "Compatibility level", owner: "Vendor", thenCheck: "Re-run after the change and a business day: no regression in the Tuning Report." },
  { checkId: "DBC07", title: "Database owner is a valid login", owner: "Us", thenCheck: "Re-run: DBC07 gone." },
  { checkId: "DBC09", title: "Indirect checkpoints (target recovery time)", owner: "Us", thenCheck: "Re-run: DBC09 gone." },
  { checkId: "DBC10", title: "Query Store is on and recording", owner: "Us", thenCheck: "Re-run: DBC10 gone. The Tuning Report can run after a few days." },
  { checkId: "DBC11", title: "TRUSTWORTHY is off", owner: "Us", thenCheck: "Re-run: DBC11 gone and CLR code still runs." },
  { checkId: "CFG01", title: "Legacy cardinality estimator forced", owner: "Us", thenCheck: "Re-run: CFG01 gone." },
  { checkId: "CPY01", title: "Where this database came from", owner: "Us", thenCheck: "Context only." },
  { checkId: "CPY02", title: "Collation matches the server", owner: "Us", thenCheck: "Decide first: collation is a project." },
  { checkId: "CPY04", title: "Users without a matching login (orphaned)", owner: "Us", thenCheck: "Re-run: CPY04 gone." },
  { checkId: "CPY06", title: "CLR code can run", owner: "Us", thenCheck: "A procedure that uses the CLR code runs without error." },
]

const INS13_SKIPPED = "The login is not sysadmin, so the power plan could not be read (xp_regread)."

// What each database's Setup card reads, apart from Query Store (DatabaseStatus). Mutable: Apply sets it.
interface SetupState {
  costThreshold: number
  optimizeAdhoc: boolean
  autoClose: boolean
  orphanedUsers: string[]
}

const setupStates: Record<string, SetupState> = {
  WarehouseLive: { costThreshold: 50, optimizeAdhoc: true, autoClose: false, orphanedUsers: [] },
  WarehouseTest: { costThreshold: 5, optimizeAdhoc: true, autoClose: true, orphanedUsers: ["wms_report"] },
  Warehouse_Archive: { costThreshold: 50, optimizeAdhoc: false, autoClose: false, orphanedUsers: [] },
}

const setupStateOf = (db: string): SetupState =>
  (setupStates[db] ??= { costThreshold: 50, optimizeAdhoc: true, autoClose: false, orphanedUsers: [] })

const severityRank: Record<Finding["severity"], number> = { Critical: 1, High: 2, Medium: 3, Low: 4, Info: 5 }
const effortRank: Record<Finding["effort"], number> = { "Quick win": 1, Moderate: 2, Project: 3, None: 4 }
const whenRank: Record<Finding["when"], number> = { Online: 1, Window: 2, "Decide first": 3, None: 4 }

// Ranked as the contract: severity, then effort (quick win first), then when (Online first), then checkId.
function rankFindings(findings: Finding[]): Finding[] {
  return [...findings].sort(
    (a, b) =>
      severityRank[a.severity] - severityRank[b.severity] ||
      effortRank[a.effort] - effortRank[b.effort] ||
      whenRank[a.when] - whenRank[b.when] ||
      a.checkId.localeCompare(b.checkId),
  )
}

function setupFinding(
  checkId: string,
  object: string,
  f: Pick<Finding, "severity" | "when" | "effort" | "finding" | "why" | "doThis" | "evidence"> & { fixId?: string },
): Finding {
  const check = setupChecks.find((c) => c.checkId === checkId)!
  return { checkId, title: check.title, owner: check.owner, object, thenCheck: check.thenCheck, ...f }
}

const quoteName = (name: string) => `[${name.replace(/]/g, "]]")}]`

function instanceSummary(info: InstanceInfo): string {
  return `${info.productName} ${info.edition}, build ${info.productVersion} (RTM ${info.productLevel}). 8 logical CPUs, 32 GB RAM, running for ${Math.floor(info.uptimeDays)} days.`
}

function setupFindings(info: InstanceInfo, db: DatabaseStatus): Finding[] {
  const st = setupStateOf(db.name)
  const server = info.serverName
  const findings: Finding[] = [
    setupFinding("INS01", server, {
      severity: "Info",
      when: "None",
      effort: "None",
      finding: "Instance summary.",
      why: "",
      doThis: "Up to date with the latest known cumulative update (CU25 (May 2026)).",
      evidence: instanceSummary(info),
    }),
  ]
  if (st.costThreshold <= 5) {
    findings.push(
      setupFinding("INS05", server, {
        severity: "Medium",
        when: "Online",
        effort: "Quick win",
        finding: "Cost threshold for parallelism is still the default of 5.",
        why: "Even small, cheap queries go parallel, which wastes CPU and shows up as CXPACKET waits. The default dates from the 1990s.",
        doThis: "Start at 50 and adjust later: EXEC sp_configure 'cost threshold for parallelism', 50; RECONFIGURE;",
        evidence: `Current value: ${st.costThreshold}.`,
        fixId: "cost-threshold",
      }),
    )
  }
  if (!st.optimizeAdhoc) {
    findings.push(
      setupFinding("INS06", server, {
        severity: "Low",
        when: "Online",
        effort: "Quick win",
        finding: "Optimize for ad hoc workloads is off.",
        why: "Queries that run only once still store a full plan in memory. That memory could be caching data instead.",
        doThis:
          "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'optimize for ad hoc workloads', 1; RECONFIGURE;",
        evidence: "Single-use plans: 61.4 MB of 412.0 MB plan cache.",
        fixId: "optimize-adhoc",
      }),
    )
  }
  if (st.autoClose) {
    findings.push(
      setupFinding("DBC02", db.name, {
        severity: "High",
        when: "Online",
        effort: "Quick win",
        finding: "AUTO_CLOSE is on.",
        why: "The database shuts down when the last user disconnects and has to start up again on the next connection. This adds delays, empties memory caches and floods the error log.",
        doThis: `ALTER DATABASE ${quoteName(db.name)} SET AUTO_CLOSE OFF;`,
        evidence: "",
        fixId: "auto-close",
      }),
    )
  }
  const qs = db.queryStore
  if (qs && (qs.verdict === "off" || qs.verdict === "drift")) {
    const off = qs.verdict === "off"
    findings.push(
      setupFinding("DBC10", db.name, {
        severity: off ? "Low" : "Medium",
        when: "Online",
        effort: "Quick win",
        finding: off
          ? "Query Store is off."
          : `Query Store is on but differs from the recommended settings: ${qs.drift.map((d) => `${d.setting} ${d.current || "not set"} (target ${d.target})`).join(", ")}.`,
        why: "Query Store is the flight recorder for query performance. Without it, it is much harder to find out why a query got slower after a release, upgrade or plan change.",
        doThis: off
          ? `ALTER DATABASE ${quoteName(db.name)} SET QUERY_STORE = ON (OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = AUTO);`
          : `ALTER DATABASE ${quoteName(db.name)} SET QUERY_STORE (${qs.drift.map((d) => `${d.setting} = ${d.target}`).join(", ")});`,
        evidence: `Storage used: ${qs.currentStorageMb} MB of ${qs.maxStorageMb} MB.`,
        fixId: "query-store",
      }),
    )
  }
  for (const user of st.orphanedUsers) {
    findings.push(
      setupFinding("CPY04", db.name, {
        severity: "Medium",
        when: "Online",
        effort: "Quick win",
        finding: `Database user ${user} has no matching login on this server.`,
        why: "After a restore, users are linked to logins of the old server. Anything connecting as this user fails to log in or gets the wrong permissions.",
        doThis:
          "Create the login (with the client's password) and link it with ALTER USER ... WITH LOGIN, or drop the user if it is no longer needed.",
        evidence: "Type: SQL user. Default schema: dbo.",
      }),
    )
  }
  return rankFindings(findings)
}

// The fixes available right now: one per fired finding with a fixId (phase3c-contract.md).
function setupFixes(info: InstanceInfo, db: DatabaseStatus): Fix[] {
  const st = setupStateOf(db.name)
  const all: Fix[] = [
    {
      id: "cost-threshold",
      title: "Cost threshold for parallelism",
      scope: "server",
      checkId: "INS05",
      setting: "cost threshold for parallelism",
      current: String(st.costThreshold),
      target: "50",
      needsRestart: false,
    },
    {
      id: "optimize-adhoc",
      title: "Optimize for ad hoc workloads",
      scope: "server",
      checkId: "INS06",
      setting: "optimize for ad hoc workloads",
      current: st.optimizeAdhoc ? "1" : "0",
      target: "1",
      needsRestart: false,
    },
    {
      id: "auto-close",
      title: "AUTO_CLOSE",
      scope: "database",
      checkId: "DBC02",
      setting: "AUTO_CLOSE",
      current: st.autoClose ? "ON" : "OFF",
      target: "OFF",
      needsRestart: false,
    },
    {
      id: "query-store",
      title: "Query Store",
      scope: "database",
      checkId: "DBC10",
      setting: "QUERY_STORE",
      current: db.queryStore?.summary ?? "",
      target: "On with the recommended settings",
      needsRestart: false,
    },
  ]
  const fired = new Set(setupFindings(info, db).map((f) => f.fixId))
  return all.filter((f) => fired.has(f.id))
}

function setupDetail(info: InstanceInfo, db: DatabaseStatus): CardDetail {
  const st = setupStateOf(db.name)
  const findings = setupFindings(info, db)
  const actionable = findings.filter((f) => f.severity !== "Info")
  const qs = db.queryStore
  const qsBad = !!qs && (qs.verdict === "off" || qs.verdict === "drift")
  const facts: Card["facts"] = [
    { label: "Version", value: `${info.productName} ${info.productLevel}, up to date`, tone: "normal" },
    { label: "Memory", value: "26 of 32 GB cap", tone: "normal" },
    {
      label: "Parallelism",
      value: `MAXDOP 8, cost threshold ${st.costThreshold}`,
      tone: actionable.some((f) => f.checkId === "INS05") ? "bad" : "normal",
    },
    { label: "Query Store", value: qs ? qs.summary : "could not be read", tone: qsBad ? "bad" : "normal" },
  ]
  let status: Card["status"] = "green"
  let statusText = "No problem found"
  if (actionable.some((f) => f.severity === "Critical")) {
    status = "red"
    statusText = "Needs attention today"
  } else if (actionable.length > 0) {
    status = "amber"
    statusText = actionable.length === 1 ? "1 thing to do" : `${actionable.length} things to do`
  }
  const checks: CheckStatus[] = setupChecks.map(({ checkId, title }) => {
    if (checkId === "INS13") return { checkId, title, status: "skipped", note: INS13_SKIPPED }
    const status = findings.some((f) => f.checkId === checkId) ? "finding" : "passed"
    return { checkId, title, status, note: "" }
  })
  return {
    card: {
      id: "setup",
      title: "Setup and configuration",
      question: "Is it set up the way it should be?",
      status,
      statusText,
      verdict: actionable.length
        ? actionable[0].finding
        : `Set up as recommended: ${info.productName} ${info.productLevel} is up to date and Query Store is on with the recommended settings.`,
      facts,
      toDo: actionable.length,
      forClientIT: actionable.filter((f) => f.owner === "Client IT").length,
      fixable: actionable.filter((f) => f.fixId).length,
      asOf: now(),
      note: "",
    },
    findings,
    checks,
    fixes: setupFixes(info, db),
  }
}

const SERVER_WARNING = (server: string) => `This is a server setting: it changes every database on ${server}.`
const PLAN_CACHE_WARNING = "SQL Server clears its plan cache: queries recompile for a few minutes."

const spConfigure = (setting: string, value: string) =>
  `EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure '${setting}', ${value}; RECONFIGURE;`

// POST .../fixes/{fixId}/preview: built from the values read now, as the backend's domain does.
function fixPreview(info: InstanceInfo, db: DatabaseStatus, fix: Fix): FixPreview {
  if (fix.id === "query-store") return buildPreview(db)
  const changes = [{ setting: fix.setting, current: fix.current, target: fix.target }]
  if (fix.scope === "server") {
    return {
      database: "",
      engineVersion: info.productVersion,
      changes,
      statement: spConfigure(fix.setting, fix.target),
      rollback: spConfigure(fix.setting, fix.current),
      warnings: [SERVER_WARNING(info.serverName), PLAN_CACHE_WARNING],
    }
  }
  const quoted = quoteName(db.name)
  return {
    database: db.name,
    engineVersion: info.productVersion,
    changes,
    statement: `ALTER DATABASE ${quoted} SET ${fix.setting} ${fix.target};`,
    rollback: `ALTER DATABASE ${quoted} SET ${fix.setting} ${fix.current};`,
    warnings: [],
  }
}

function findFix(info: InstanceInfo, db: DatabaseStatus, fixId: string): Fix {
  const fix = [...setupFixes(info, db), ...upkeepFixes(db), ...backupFixes(db)].find((f) => f.id === fixId)
  if (!fix) throw new ApiError(`nothing to apply: ${fixId} did not fire on ${db.name}`, 404)
  return fix
}

// POST .../fixes/{fixId}/apply: runs the statement, reads the value back and logs a change. Set
// localStorage "mock.fixFails" to "1" to see the 502 a login without the permission gets, and
// "mock.fixUnverified" to "1" to see a value read back that is not the target.
async function applyFix(srv: Server, info: InstanceInfo, db: DatabaseStatus, fix: Fix): Promise<FixResult> {
  const preview = fixPreview(info, db, fix)
  await delay(1000) // the statement and the read-back
  if (localStorage.getItem("mock.fixFails") === "1") {
    throw new ApiError(
      fix.scope === "server"
        ? `SQL Server refused the change: the login CLIENTA\\svc-sqlmonitor needs ALTER SETTINGS on ${info.serverName} (sysadmin or serveradmin).`
        : `SQL Server refused the change: the login CLIENTA\\svc-sqlmonitor needs ALTER on the database ${db.name}.`,
      502,
    )
  }
  const unverified = localStorage.getItem("mock.fixUnverified") === "1"
  let before = fix.current
  let after = unverified ? fix.current : fix.target
  if (!unverified) {
    const st = setupStateOf(db.name)
    if (fix.id === "cost-threshold") st.costThreshold = 50
    if (fix.id === "optimize-adhoc") st.optimizeAdhoc = true
    if (fix.id === "auto-close") st.autoClose = false
    if (fix.id === "query-store") {
      before = db.queryStore!.summary
      db.queryStore = okStatus(db.queryStore!.currentStorageMb)
      after = db.queryStore.summary
    }
  }
  const result: FixResult = {
    preview,
    before,
    after,
    verified: !unverified,
    changeId: nextChangeId++,
    appliedAt: now(),
  }
  changes.unshift({
    id: result.changeId,
    serverId: srv.id,
    database: db.name,
    changedAt: result.appliedAt,
    object: fix.id === "query-store" ? "Query Store" : fix.setting,
    changeType: "SETTING",
    description:
      fix.id === "query-store"
        ? `Query Store on ${db.name}: ` +
          preview.changes.map((c) => `${c.setting} ${c.current || "not set"} -> ${c.target}`).join(", ")
        : `${fix.setting} on ${fix.scope === "server" ? info.serverName : db.name}: ${fix.current} -> ${fix.target}`,
    result: after,
  })
  return result
}

// Phase 3d: the Upkeep card and its detail per database (phase3d-contract.md), as the backend builds them
// from the scan. WarehouseLive green (Agent running, a maintenance job, no failed job, operator and alerts);
// WarehouseTest amber (MNT02 High: the full backup job failed; MNT04 Medium: 6 stale statistics on 4
// tables, with the update-statistics fix); Warehouse_Archive amber (MNT01 High: Agent stopped; MNT07 Low:
// no operator). Texts are the scan's (01-QuickScan.sql sections 6 and 8, Checks.csv). Mock
// simplification, as Setup: the instance-level upkeep is kept per database. Apply updates the
// statistics, so MNT04 goes until the page reloads.

const upkeepChecks: { checkId: string; title: string; owner: Finding["owner"]; thenCheck: string }[] = [
  { checkId: "MNT01", title: "SQL Server Agent is running", owner: "Client IT", thenCheck: "Re-run: MNT01 gone." },
  { checkId: "MNT02", title: "Failed Agent jobs (last 7 days)", owner: "Us", thenCheck: "Re-run after 7 days: MNT02 gone." },
  {
    checkId: "MNT03",
    title: "Index / statistics maintenance job exists",
    owner: "Us",
    thenCheck: "Re-run after the first run: MNT03 gone and MNT04 empty.",
  },
  { checkId: "MNT04", title: "Out-of-date statistics", owner: "Us", thenCheck: "Re-run after the job: MNT04 empty." },
  {
    checkId: "MNT05",
    title: "Shrinks, cache clears and file growth events (last 7 days)",
    owner: "Us",
    thenCheck: "Re-run after 7 days: MNT05 gone. STO08 and WRK01 should not get worse again.",
  },
  {
    checkId: "MNT06",
    title: "Crashes (memory dumps) and severe errors",
    owner: "Client IT",
    thenCheck: "Re-run after 7 days: MNT06 gone.",
  },
  {
    checkId: "MNT07",
    title: "Alerts and an operator for corruption and severe errors",
    owner: "Client IT",
    thenCheck: "RAISERROR('Alert test' 18 1) WITH LOG reaches the operator's mailbox.",
  },
]

// A table with stale statistics: schema, table, rows, the most modifications, how many of its statistics.
type StaleTable = [schema: string, table: string, rows: number, modifications: number, stats: number]

// What each database's Upkeep card reads. Mutable: the statistics fix empties staleStats.
interface UpkeepState {
  agent: "running" | "stopped"
  failedJob: { name: string; fails: number; step: string } | null
  operators: number
  alertsOn: number
  staleStats: StaleTable[]
}

const upkeepStates: Record<string, UpkeepState> = {
  WarehouseLive: { agent: "running", failedJob: null, operators: 1, alertsOn: 10, staleStats: [] },
  WarehouseTest: {
    agent: "running",
    failedJob: { name: "DatabaseBackup - FULL", fails: 2, step: "DatabaseBackup" },
    operators: 1,
    alertsOn: 10,
    staleStats: [
      ["dbo", "Transaction", 39_120_644, 11_804_377, 2],
      ["dbo", "StockUnit", 10_012_377, 3_410_926, 2],
      ["dbo", "SalesOrderLine", 6_244_091, 1_733_502, 1],
      ["dbo", "PickLine", 1_941_006, 612_330, 1],
    ],
  },
  Warehouse_Archive: { agent: "stopped", failedJob: null, operators: 0, alertsOn: 0, staleStats: [] },
}

const upkeepStateOf = (db: string): UpkeepState =>
  (upkeepStates[db] ??= { agent: "running", failedJob: null, operators: 1, alertsOn: 10, staleStats: [] })

const staleCount = (st: UpkeepState) => st.staleStats.reduce((n, t) => n + t[4], 0)

function upkeepFinding(
  checkId: string,
  object: string,
  f: Pick<Finding, "severity" | "when" | "effort" | "finding" | "why" | "doThis" | "evidence"> & { fixId?: string },
): Finding {
  const check = upkeepChecks.find((c) => c.checkId === checkId)!
  return { checkId, title: check.title, owner: check.owner, object, thenCheck: check.thenCheck, ...f }
}

const scanDate = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
const fileStamp = (d: Date) =>
  `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}_${pad(d.getHours())}${pad(d.getMinutes())}00`

function upkeepFindings(info: InstanceInfo, db: DatabaseStatus): Finding[] {
  const st = upkeepStateOf(db.name)
  const server = info.serverName
  const findings: Finding[] = []
  if (st.agent === "stopped") {
    findings.push(
      upkeepFinding("MNT01", server, {
        severity: "High",
        when: "Online",
        effort: "Quick win",
        finding: "SQL Server Agent is not running.",
        why: "Agent runs scheduled backups, integrity checks and maintenance. While it is stopped, none of them run.",
        doThis:
          "Start the SQL Server Agent service and set its startup type to Automatic (SQL Server Configuration Manager).",
        evidence: "Status: Stopped, startup: Manual.",
      }),
    )
  }
  if (st.failedJob) {
    const job = st.failedJob
    const last = daysAgoAt(1, 1, 0)
    findings.push(
      upkeepFinding("MNT02", server, {
        severity: /backup/i.test(job.name) ? "High" : "Medium",
        when: "Online",
        effort: "Moderate",
        finding: `Agent job "${job.name}" failed ${job.fails} time(s) in the last 7 days.`,
        why: "Failing jobs usually mean backups, maintenance or business processes are silently not happening.",
        doThis:
          "Open the job history in SSMS (SQL Server Agent > Jobs > right-click > View History), fix the cause, and set up failure notifications (Database Mail and an operator).",
        evidence:
          `Last failure: ${scanDate(last)}. Step "${job.step}": Executed as user: NT SERVICE\\SQLSERVERAGENT. ` +
          `Cannot open backup device '\\\\NAS01\\SQLBackup\\${db.name}\\FULL\\${db.name}_FULL_${fileStamp(last)}.bak'. ` +
          "Operating system error 5(Access is denied.). [SQLSTATE 42000] (Error 3201)  BACKUP DATABASE is terminating abnormally. [SQLSTATE 42000] (Error 3013).  The step failed.",
      }),
    )
  }
  const stale = staleCount(st)
  if (stale > 0) {
    const top = [...st.staleStats].sort((a, b) => b[3] - a[3]).slice(0, 5)
    findings.push(
      upkeepFinding("MNT04", db.name, {
        severity: "Medium",
        when: "Online",
        effort: "Quick win",
        finding: `${stale} statistics on large tables are out of date (over 20% of rows changed, not updated for 7+ days).`,
        why: "Out-of-date statistics make the optimizer guess row counts wrongly, which leads to slow plans: scans instead of seeks, wrong join types, memory spills.",
        doThis: `Update them outside peak hours: USE ${quoteName(db.name)}; EXEC sp_updatestats;  Then schedule regular statistics maintenance (MNT03).`,
        evidence: `Most changed tables: ${top.map(([s, t, rows, mods]) => `${s}.${t} (${mods} changes / ${rows} rows)`).join(", ")}`,
        fixId: "update-statistics",
      }),
    )
  }
  if (st.operators === 0 || st.alertsOn < 10) {
    findings.push(
      upkeepFinding("MNT07", server, {
        severity: "Low",
        when: "Online",
        effort: "Quick win",
        finding:
          st.operators === 0
            ? "No operator with an email address exists, so no alert or job failure can reach anyone."
            : "No alert for: severity 19, 20, 21, 22, 23, 24, 25, error 823, 824, 825.",
        why: "Corruption (errors 823, 824, 825) and fatal errors (severity 19 to 25) are only noticed when users complain, unless an alert mails someone. By then the corruption is in the backups too.",
        doThis:
          "Set up Database Mail and an operator for the client's IT mailbox, then one alert per severity 19 to 25 and per error 823, 824 and 825, each notifying the operator (sp_add_alert, sp_add_notification). Test it from a throwaway SSMS window: RAISERROR('Alert test', 20, 1) WITH LOG (it closes that window).",
        evidence: `Operators with email: ${st.operators}. Alerts enabled: ${st.alertsOn}.`,
      }),
    )
  }
  return rankFindings(findings)
}

function upkeepFixes(db: DatabaseStatus): Fix[] {
  const st = upkeepStateOf(db.name)
  const stale = staleCount(st)
  if (stale === 0) return []
  return [
    {
      id: "update-statistics",
      title: "Update out-of-date statistics",
      scope: "database",
      checkId: "MNT04",
      setting: "Statistics",
      current: `${stale} statistics on ${st.staleStats.length} tables out of date`,
      target: "none out of date",
      needsRestart: false,
    },
  ]
}

function upkeepDetail(info: InstanceInfo, db: DatabaseStatus): CardDetail {
  const st = upkeepStateOf(db.name)
  const findings = upkeepFindings(info, db)
  const actionable = findings.filter((f) => f.severity !== "Info")
  const job = st.failedJob
  const facts: Card["facts"] = [
    st.agent === "running"
      ? { label: "SQL Agent", value: "running, automatic", tone: "normal" }
      : { label: "SQL Agent", value: "stopped", tone: "worse" },
    {
      label: "Failed jobs, 7 days",
      value: job ? "1" : "0",
      tone: !job ? "normal" : /backup/i.test(job.name) ? "worse" : "bad",
    },
    { label: "Maintenance job", value: "found", tone: "normal" },
    st.operators === 0
      ? { label: "Told about errors", value: "no operator", tone: "bad" }
      : st.alertsOn < 10
        ? { label: "Told about errors", value: `${10 - st.alertsOn} alerts missing`, tone: "bad" }
        : { label: "Told about errors", value: `operator and ${st.alertsOn} alerts`, tone: "normal" },
  ]
  let status: Card["status"] = "green"
  let statusText = "No problem found"
  if (actionable.some((f) => f.severity === "Critical")) {
    status = "red"
    statusText = "Needs attention today"
  } else if (actionable.length > 0) {
    status = "amber"
    statusText = actionable.length === 1 ? "1 thing to do" : `${actionable.length} things to do`
  }
  const checks: CheckStatus[] = upkeepChecks.map(({ checkId, title }) => {
    const fired = findings.some((f) => f.checkId === checkId)
    const note = checkId === "MNT07" && !fired ? `${st.alertsOn} alerts, ${st.operators} operator(s).` : ""
    return { checkId, title, status: fired ? "finding" : "passed", note }
  })
  return {
    card: {
      id: "upkeep",
      title: "Upkeep",
      question: "Is someone looking after it?",
      status,
      statusText,
      verdict: actionable.length
        ? actionable[0].finding
        : "SQL Server Agent is running, maintenance runs and no job failed in 7 days.",
      facts,
      toDo: actionable.length,
      forClientIT: actionable.filter((f) => f.owner === "Client IT").length,
      fixable: actionable.filter((f) => f.fixId).length,
      asOf: now(),
      note: "",
    },
    findings,
    checks,
    fixes: upkeepFixes(db),
  }
}

const NO_ROLLBACK = "No rollback: updated statistics are never worse than stale ones."
const STATISTICS_WARNINGS = [
  "Updating statistics reads each table; on large tables this takes minutes and competes with users. Run it outside peak hours.",
  "This does not create a maintenance job (MNT03); it fixes today's statistics once.",
  "Needs ALTER on the tables (db_owner or db_ddladmin).",
]

// POST .../fixes/update-statistics/preview: one line per table, smallest table first; no rollback.
function statisticsPreview(info: InstanceInfo, db: DatabaseStatus, fix: Fix): FixPreview {
  const tables = [...upkeepStateOf(db.name).staleStats].sort((a, b) => a[2] - b[2])
  return {
    database: db.name,
    engineVersion: info.productVersion,
    changes: [{ setting: fix.setting, current: fix.current, target: fix.target }],
    statement: tables.map(([s, t]) => `UPDATE STATISTICS ${quoteName(s)}.${quoteName(t)};`).join("\n"),
    rollback: "",
    warnings: [NO_ROLLBACK, ...STATISTICS_WARNINGS],
  }
}

// POST .../fixes/update-statistics/apply: the batch takes a while, then the stale count is read back.
// The same localStorage switches as the other fixes ("mock.fixFails", "mock.fixUnverified"). Phase 4f: it
// is the work of a background run (startMockRun), so the apply answers pending at once and the run ends
// with this result (or this error) 6 seconds after Apply.
async function applyStatistics(srv: Server, info: InstanceInfo, db: DatabaseStatus, fix: Fix): Promise<FixResult> {
  const preview = statisticsPreview(info, db, fix)
  // With the apply request's 500 ms the run takes 6 seconds, so the dialog's progress bar (phase 4e) moves a
  // few times and then shows "Finishing" (phase 4f).
  await delay(5500)
  if (localStorage.getItem("mock.fixFails") === "1") {
    throw new ApiError(
      `SQL Server refused the change: the login CLIENTA\\svc-sqlmonitor needs ALTER on the tables in ${db.name} (db_owner or db_ddladmin).`,
      502,
    )
  }
  const st = upkeepStateOf(db.name)
  const tables = st.staleStats.length
  if (localStorage.getItem("mock.fixUnverified") !== "1") st.staleStats = []
  const left = staleCount(st)
  const after = left === 0 ? "0 out of date" : `${left} still out of date`
  const result: FixResult = {
    preview,
    before: fix.current,
    after,
    verified: left === 0,
    changeId: nextChangeId++,
    appliedAt: now(),
  }
  changes.unshift({
    id: result.changeId,
    serverId: srv.id,
    database: db.name,
    changedAt: result.appliedAt,
    object: "Statistics",
    changeType: "SETTING",
    description: `Statistics on ${db.name}: ${tables} tables updated`,
    result: `${after}, ${left === 0 ? "verified" : "differs"}`,
  })
  return result
}

// Phase 3a: the Backups card and its detail per database (phase3a-contract.md), as the backend builds
// them from the scan: WarehouseLive green (15-minute log backups, CHECKDB 3 days ago), WarehouseTest amber
// (FULL without log backups, CHECKDB 20 days ago), Warehouse_Archive red (SIMPLE, never backed up,
// created a month ago). Texts are the scan's (01-QuickScan.sql section 5, Checks.csv).

const backupChecks: { checkId: string; title: string; thenCheck: string }[] = [
  { checkId: "BAK01", title: "A full backup exists", thenCheck: "Re-run: BAK01 gone and BAK05 shows the data-loss window." },
  { checkId: "BAK02", title: "Transaction log backups", thenCheck: "Re-run: BAK02 gone and the log stops growing (STO07)." },
  { checkId: "BAK03", title: "Integrity check (DBCC CHECKDB)", thenCheck: "Re-run after the job runs: BAK03 gone." },
  { checkId: "BAK04", title: "Suspect (damaged) pages recorded", thenCheck: "CHECKDB clean and the DBA cleared msdb.dbo.suspect_pages." },
  {
    checkId: "BAK05",
    title: "Worst-case data loss",
    thenCheck: "Re-run after a week: the window in BAK05 matches what the client agreed.",
  },
  {
    checkId: "DBC08",
    title: "Recovery model matches data-loss tolerance",
    thenCheck: "BAK05 shows the agreed data-loss window.",
  },
]

const pad = (n: number) => String(n).padStart(2, "0")

// The scan's evidence format in the server's clock (the mock's server runs on the laptop's clock).
const scanTime = (d: Date) =>
  `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`

// The most recent HH:MM that has passed, today or yesterday.
function lastAt(hour: number, minute = 0): Date {
  const d = new Date()
  d.setHours(hour, minute, 0, 0)
  if (d.getTime() > Date.now()) d.setDate(d.getDate() - 1)
  return d
}

function daysAgoAt(days: number, hour: number, minute: number): Date {
  const d = new Date()
  d.setDate(d.getDate() - days)
  d.setHours(hour, minute, 0, 0)
  return d
}

// "today 02:00" or "N days ago", as the contract's Last full backup fact.
function lastFullFact(d: Date): string {
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((start(new Date()) - start(d)) / 86_400_000)
  if (days === 0) return `today ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return days === 1 ? "1 day ago" : `${days} days ago`
}

function backupFinding(
  checkId: string,
  database: string,
  f: Pick<Finding, "severity" | "when" | "effort" | "finding" | "why" | "doThis" | "evidence"> & { fixId?: string },
): Finding {
  const check = backupChecks.find((c) => c.checkId === checkId)!
  return { checkId, title: check.title, owner: "Client IT", object: database, thenCheck: check.thenCheck, ...f }
}

// Every check passed except those named; skipped ones carry their note.
function backupCheckStatuses(findings: Finding[], skipped: Record<string, string> = {}): CheckStatus[] {
  return backupChecks.map(({ checkId, title }) => {
    if (skipped[checkId]) return { checkId, title, status: "skipped", note: skipped[checkId] }
    const status = findings.some((f) => f.checkId === checkId) ? "finding" : "passed"
    return { checkId, title, status, note: "" }
  })
}

const BAK05_WHY =
  "If the disk fails or someone deletes the wrong rows, everything entered since the last backup is gone. This is the number to agree with the client."
const BAK05_DO_FULL =
  "Log backups every 15 minutes close this gap. If they are already scheduled, check that the job runs (MNT02)."

function backupsDetail(db: DatabaseStatus): CardDetail {
  const base: Card = {
    id: "backups",
    title: "Backups and data safety",
    question: "Could you lose data?",
    status: "green",
    statusText: "No problem found",
    verdict: "",
    facts: [],
    toDo: 0,
    forClientIT: 0,
    fixable: 0,
    asOf: now(),
    note: "",
  }

  if (db.name === "Warehouse_Archive") {
    const checkDb = daysAgoAt(10, 3, 5)
    const findings: Finding[] = [
      backupFinding("BAK01", db.name, {
        severity: "Critical",
        when: "Online",
        effort: "Quick win",
        finding: "No full backup of this database has been recorded.",
        why: "Without a recent backup, a disk failure, corruption or a mistake can mean permanent data loss.",
        ...scheduleDoThis(
          db,
          "full",
          "Schedule daily full backups (or weekly full plus daily differential), keep copies off this server, and test a restore regularly. If another tool takes backups (e.g. VM snapshots), confirm it is SQL-aware and restores are tested.",
        ),
        evidence: "Last full: never. Last differential: never.",
      }),
      backupFinding("DBC08", db.name, {
        severity: "Info",
        when: "Decide first",
        effort: "Moderate",
        finding: "Database uses the SIMPLE recovery model.",
        why: "A restore can only go back to the last full or differential backup; everything since then is lost. Point-in-time restore is not possible.",
        doThis:
          "Confirm with the business how much data loss is acceptable (RPO). If it is less than the time between backups, switch to FULL recovery and schedule log backups.",
        evidence: "recovery_model = SIMPLE",
      }),
    ]
    return {
      card: {
        ...base,
        status: "red",
        statusText: "Needs attention today",
        verdict: findings[0].finding,
        facts: [
          { label: "Worst-case data loss", value: "no backup", tone: "worse" },
          { label: "Last full backup", value: "never", tone: "worse" },
          { label: "Last CHECKDB", value: `${Math.round((Date.now() - checkDb.getTime()) / 86_400_000)} days ago`, tone: "normal" },
          { label: "Log backups", value: "not needed (SIMPLE)", tone: "normal" },
        ],
        toDo: 1,
        forClientIT: 1,
        fixable: findings.filter((f) => f.fixId).length,
      },
      findings,
      checks: backupCheckStatuses(findings, {
        BAK05: "No full backup yet, so there is no data-loss window to measure (BAK01).",
      }),
      fixes: backupFixes(db),
    }
  }

  if (db.name === "WarehouseTest") {
    const lastFull = lastAt(1, 0)
    const checkDb = daysAgoAt(20, 3, 12)
    const findings: Finding[] = [
      backupFinding("BAK02", db.name, {
        severity: "High",
        when: "Online",
        effort: "Quick win",
        finding: "Database uses FULL recovery but its log has never been backed up.",
        why: "In FULL recovery the log keeps growing until it is backed up. It can fill the disk and stop the database, and it limits how recent a restore can be.",
        ...scheduleDoThis(
          db,
          "log",
          "Schedule log backups (commonly every 15-60 minutes). If point-in-time restore is not needed, the business can decide to switch to SIMPLE recovery instead (DBC08).",
        ),
        evidence: "Log file size: 18432 MB.",
      }),
      backupFinding("BAK03", db.name, {
        severity: "Medium",
        when: "Online",
        effort: "Quick win",
        finding: "Last successful integrity check was 20 days ago.",
        why: "CHECKDB finds corruption early, while good backups still exist to recover from. Unnoticed corruption ends up in the backups too.",
        doThis:
          "Schedule DBCC CHECKDB weekly outside peak hours (on very large databases: WITH PHYSICAL_ONLY weekly and a full check monthly). Ola Hallengren's free maintenance solution is a common way to set this up.",
        evidence: `Last good CHECKDB: ${scanTime(checkDb)}.`,
      }),
      backupFinding("BAK05", db.name, {
        severity: "Info",
        when: "None",
        effort: "None",
        finding: "You could lose up to 24 hours of data: the longest gap between backups in the last 7 days.",
        why: BAK05_WHY,
        doThis: BAK05_DO_FULL,
        evidence: `Backups in the last 7 days: 7 full, 0 differential, 0 log. Last full ${scanTime(lastFull)}, last log never. Recovery model FULL.`,
      }),
    ]
    return {
      card: {
        ...base,
        status: "amber",
        statusText: "2 things to do",
        verdict: findings[0].finding,
        facts: [
          { label: "Worst-case data loss", value: "24 hours", tone: "normal" },
          { label: "Last full backup", value: lastFullFact(lastFull), tone: "normal" },
          { label: "Last CHECKDB", value: "20 days ago", tone: "bad" },
          { label: "Log backups", value: "none in FULL recovery", tone: "bad" },
        ],
        toDo: 2,
        forClientIT: 2,
        fixable: findings.filter((f) => f.fixId).length,
      },
      findings,
      checks: backupCheckStatuses(findings),
      fixes: backupFixes(db),
    }
  }

  // WarehouseLive: FULL recovery, a full every night at 02:00 and the log every 15 minutes.
  const lastFull = lastAt(2, 0)
  const lastLog = new Date()
  lastLog.setMinutes(lastLog.getMinutes() - (lastLog.getMinutes() % 15), 0, 0)
  const findings: Finding[] = [
    backupFinding("BAK05", db.name, {
      severity: "Info",
      when: "None",
      effort: "None",
      finding: "You could lose up to 15 minutes of data: the longest gap between backups in the last 7 days.",
      why: BAK05_WHY,
      doThis: BAK05_DO_FULL,
      evidence: `Backups in the last 7 days: 7 full, 0 differential, 665 log. Last full ${scanTime(lastFull)}, last log ${scanTime(lastLog)}. Recovery model FULL.`,
    }),
  ]
  return {
    card: {
      ...base,
      verdict: findings[0].finding,
      facts: [
        { label: "Worst-case data loss", value: "15 minutes", tone: "normal" },
        { label: "Last full backup", value: lastFullFact(lastFull), tone: "normal" },
        { label: "Last CHECKDB", value: "3 days ago", tone: "normal" },
        { label: "Log backups", value: "every 15 min on average", tone: "normal" },
      ],
    },
    findings,
    checks: backupCheckStatuses(findings),
  }
}

// Phase 3g: the backup schedule fix on the Backups card (phase3g-contract.md). Warehouse_Archive (SIMPLE,
// never backed up: BAK01 offers the full job; Agent stopped, no operator) and WarehouseTest (FULL without
// log backups: BAK02 offers the log job only, as the nightly "DatabaseBackup - FULL" job takes the fulls).
// The folder defaults to the instance's B:\SQLBackup; X:\nowhere does not exist (no changes, Apply
// disabled); a folder on the database's data drive warns. Applying creates the jobs (kept here); then the
// finding points at kakapo's job and the card offers Remove. Set localStorage "mock.fixFails" to "1" to see
// the 502 a login without sysadmin gets.

const MOCK_BACKUP_DIR = "B:\\SQLBackup"
const kakapoJobs: Record<string, string[]> = {}
const dataDrive: Record<string, string> = { WarehouseLive: "D:", WarehouseTest: "E:", Warehouse_Archive: "F:" }
type JobKind = "full" | "log"
const jobName = (db: string, kind: JobKind) => `kakapo: ${db} ${kind} backup`
const nstr = (v: string) => `N'${v.replace(/'/g, "''")}'`

// The jobs the fix creates on a database: none where backups are fine.
function scheduleKinds(db: DatabaseStatus): JobKind[] {
  if (db.name === "Warehouse_Archive") return ["full"]
  if (db.name === "WarehouseTest") return ["log"]
  return []
}

// A BAK01/BAK02 finding's Do this and fixId: the fix while kakapo's job does not exist, else the job.
function scheduleDoThis(db: DatabaseStatus, kind: JobKind, doThis: string): { doThis: string; fixId?: string } {
  const jobs = kakapoJobs[db.name]
  if (jobs?.includes(jobName(db.name, kind))) {
    return { doThis: `The job ${jobName(db.name, kind)} should be doing this; check why it has not produced a backup (MNT02).` }
  }
  return scheduleKinds(db).includes(kind) ? { doThis, fixId: "backup-schedule" } : { doThis }
}

function backupFixes(db: DatabaseStatus): Fix[] {
  const jobs = kakapoJobs[db.name]
  if (jobs?.length) {
    return [
      {
        id: "remove-backup-schedule",
        title: "Remove kakapo's backup jobs",
        scope: "database",
        checkId: "BAK01",
        setting: "Backup schedule",
        current: jobs.join(", "),
        target: "deleted",
        needsRestart: false,
      },
    ]
  }
  const kinds = scheduleKinds(db)
  if (kinds.length === 0) return []
  return [
    {
      id: "backup-schedule",
      title: "Create a backup schedule",
      scope: "database",
      checkId: kinds.includes("full") ? "BAK01" : "BAK02",
      setting: "Backup schedule",
      current: kinds.includes("full") ? "no backup job" : "no log backup job",
      target: kinds
        .map((k) => `${jobName(db.name, k)} (${k === "full" ? "daily 22:00" : "every 15 minutes"})`)
        .join(" and "),
      needsRestart: false,
    },
  ]
}

function normalizeFolder(v: string): string {
  const t = v.trim().replace(/[\\/]+$/, "")
  return /^[A-Za-z]:$/.test(t) ? `${t}\\` : t
}

// The job creation batch, the shape the backend builds (native BACKUP, compressed, verified).
function scheduleBatch(db: string, dir: string, kinds: JobKind[], operator: string): string {
  const lines = ["DECLARE @owner sysname = SUSER_SNAME(0x01);", "BEGIN TRY"]
  for (const k of kinds) {
    lines.push(`    IF EXISTS (SELECT 1 FROM msdb.dbo.sysjobs WHERE name = ${nstr(jobName(db, k))})`)
    lines.push(`        THROW 50000, ${nstr(`A job named ${jobName(db, k)} already exists`)}, 1;`)
  }
  lines.push("    BEGIN TRANSACTION;")
  for (const k of kinds) {
    const name = nstr(jobName(db, k))
    const [stmt, suffix, ext] = k === "full" ? ["BACKUP DATABASE", "_FULL_", ".bak"] : ["BACKUP LOG", "_LOG_", ".trn"]
    const backup = [
      "/* kakapo */",
      `DECLARE @dir nvarchar(400) = ${nstr(dir)};`,
      "EXEC master.dbo.xp_create_subdir @dir;",
      `DECLARE @file nvarchar(600) = @dir + ${nstr(`\\${db}${suffix}`)} + CONVERT(nvarchar(8), GETDATE(), 112) + N'_' + REPLACE(CONVERT(nvarchar(5), GETDATE(), 108), N':', N'') + ${nstr(ext)};`,
      `${stmt} ${quoteName(db)} TO DISK = @file WITH CHECKSUM, INIT, COMPRESSION;`,
      "RESTORE VERIFYONLY FROM DISK = @file WITH CHECKSUM;",
    ].join("\n")
    const cleanup = [
      "/* kakapo */",
      "DECLARE @cutoff datetime = DATEADD(DAY, -7, GETDATE());",
      `EXEC master.dbo.xp_delete_file 0, ${nstr(dir)}, ${nstr(k === "full" ? "bak" : "trn")}, @cutoff, 0;`,
    ].join("\n")
    lines.push(`    EXEC msdb.dbo.sp_add_job @job_name = ${name}, @enabled = 1,`)
    lines.push(`        @description = ${nstr(`Created by kakapo on ${new Date().toISOString().slice(0, 10)} for ${db}`)},`)
    lines.push(
      `        @category_name = N'Database Maintenance', @owner_login_name = @owner` +
        (operator ? `,\n        @notify_level_email = 2, @notify_email_operator_name = ${nstr(operator)};` : ";"),
    )
    lines.push(`    EXEC msdb.dbo.sp_add_jobstep @job_name = ${name}, @step_id = 1, @step_name = N'Back up and verify',`)
    lines.push("        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 3, @on_fail_action = 2,")
    lines.push(`        @command = ${nstr(backup)};`)
    lines.push(`    EXEC msdb.dbo.sp_add_jobstep @job_name = ${name}, @step_id = 2, @step_name = N'Delete backups older than 7 days',`)
    lines.push("        @subsystem = N'TSQL', @database_name = N'master', @on_success_action = 1, @on_fail_action = 2,")
    lines.push(`        @command = ${nstr(cleanup)};`)
    lines.push(
      k === "full"
        ? `    EXEC msdb.dbo.sp_add_jobschedule @job_name = ${name}, @name = ${nstr(`${jobName(db, k)}, daily 22:00`)}, @enabled = 1,\n        @freq_type = 4, @freq_interval = 1, @active_start_time = 220000;`
        : `    EXEC msdb.dbo.sp_add_jobschedule @job_name = ${name}, @name = ${nstr(`${jobName(db, k)}, every 15 minutes`)}, @enabled = 1,\n        @freq_type = 4, @freq_interval = 1, @freq_subday_type = 4, @freq_subday_interval = 15;`,
    )
    lines.push(`    EXEC msdb.dbo.sp_add_jobserver @job_name = ${name}, @server_name = N'(local)';`)
  }
  lines.push("    COMMIT TRANSACTION;", "END TRY", "BEGIN CATCH", "    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;", "    THROW;", "END CATCH;")
  return lines.join("\n")
}

const deleteJobs = (names: string[]) =>
  names.map((n) => `EXEC msdb.dbo.sp_delete_job @job_name = ${nstr(n)};`).join("\n")

// POST .../fixes/backup-schedule/preview, built with the options the dialog sent.
function backupSchedulePreview(info: InstanceInfo, db: DatabaseStatus, options?: Record<string, string>): FixPreview {
  const kinds = scheduleKinds(db)
  const full = kinds.includes("full")
  const folder = normalizeFolder(options?.folder ?? MOCK_BACKUP_DIR)
  const runNow = full && (options?.runNow ?? "true") === "true"
  const dir = `${folder.replace(/\\+$/, "")}\\${db.name}`
  const server = info.serverName
  const operator = db.name === "WarehouseTest" ? "DBA Team" : ""
  let warning = ""
  if (!folder) warning = `Type the folder the backups go to, as the SQL Server service on ${server} sees it.`
  else if (!/^([A-Za-z]:\\|\\\\[^\\]+\\[^\\]+)/.test(folder))
    warning = `The folder ${folder} is not a full Windows path (like D:\\Backup or \\\\server\\share): type another.`
  else if (/^x:\\nowhere$/i.test(folder))
    warning = `The folder ${folder} does not exist on ${server} (as the SQL Server service sees it): create it, or type another.`
  const usable = !warning
  if (usable && folder.toUpperCase().startsWith(dataDrive[db.name] ?? "?"))
    warning = `This folder is on the same drive as the data files (${dataDrive[db.name]}): a disk failure loses the database and its backups together. Prefer another drive or a share.`

  const inputs: FixPreview["inputs"] = [
    {
      name: "folder",
      label: "Backup folder",
      value: folder,
      help: "A folder on the database server, as the SQL Server service sees it: a local drive (D:\\Backup) or a share (\\\\nas\\sql). kakapo makes a folder for the database inside it.",
      kind: "text",
      ...(warning ? { warning } : {}),
    },
  ]
  const opts: Record<string, string> = { folder }
  if (full) {
    inputs.push({
      name: "runNow",
      label: "Take the first full backup now",
      value: String(runNow),
      help: "Start the full backup job as soon as the jobs are created, so the database has a backup now rather than tonight.",
      kind: "bool",
    })
    opts.runNow = String(runNow)
  }
  const warnings = [
    ...(warning ? [warning] : []),
    ...(db.name === "Warehouse_Archive"
      ? ["SQL Server Agent is stopped (Stopped): the jobs will not run until it starts (MNT01)."]
      : []),
    "The backup job runs as the SQL Server service account; the folder must let it write.",
    `Needs sysadmin on ${server} (creating Agent jobs owned by sa).`,
  ]
  const preview: FixPreview = {
    database: db.name,
    engineVersion: info.productVersion,
    changes: [],
    statement: "",
    rollback: "",
    warnings,
    inputs,
    options: opts,
  }
  if (!usable) return preview
  const names = kinds.map((k) => jobName(db.name, k))
  preview.changes = [
    full
      ? { setting: "Full backup job", current: "none", target: `daily 22:00 to ${dir}, compressed, verified` }
      : { setting: "Full backup job", current: "DatabaseBackup - FULL", target: "kept as it is" },
    kinds.includes("log")
      ? { setting: "Log backup job", current: "none", target: `every 15 minutes to ${dir}` }
      : { setting: "Log backup job", current: "none", target: "not needed (SIMPLE recovery; DBC08 decides)" },
    { setting: "Keep", current: "", target: "7 days of files" },
    { setting: "Tell on failure", current: "", target: operator || "no one (MNT07: no operator with an email)" },
    ...(full ? [{ setting: "First full backup", current: "", target: runNow ? "now, when you apply" : "tonight at 22:00" }] : []),
  ]
  preview.statement = scheduleBatch(db.name, dir, kinds, operator)
  preview.rollback = deleteJobs(names)
  return preview
}

// POST .../fixes/backup-schedule/apply: creates the jobs, reads them back, starts the full backup.
async function applyBackupSchedule(
  srv: Server,
  info: InstanceInfo,
  db: DatabaseStatus,
  fix: Fix,
  options?: Record<string, string>,
): Promise<FixResult> {
  const preview = backupSchedulePreview(info, db, options)
  if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
  await delay(1000)
  if (localStorage.getItem("mock.fixFails") === "1") {
    throw new ApiError(
      `SQL Server refused to create the backup jobs on ${info.serverName}: CREATE PROCEDURE permission denied in database 'msdb'. Needs sysadmin on ${info.serverName} (creating Agent jobs owned by sa).`,
      502,
    )
  }
  const kinds = scheduleKinds(db)
  const names = kinds.map((k) => jobName(db.name, k))
  kakapoJobs[db.name] = names
  const runNow = preview.options?.runNow === "true"
  const at = (h: number, m: number) => {
    const d = new Date()
    if (h >= 0) {
      d.setHours(h, m, 0, 0)
      if (d.getTime() < Date.now()) d.setDate(d.getDate() + 1)
    } else d.setMinutes(Math.floor(d.getMinutes() / 15) * 15 + 15, 0, 0)
    return scanTime(d)
  }
  const after =
    kinds.map((k) => `${jobName(db.name, k)}: enabled, next run ${k === "full" ? at(22, 0) : at(-1, 0)}`).join("; ") +
    (runNow
      ? "; full backup started: it runs under SQL Server Agent, and the Backups card shows it when it finishes."
      : ".")
  const dir = preview.changes[0].target.match(/to (.+?)(,|$)/)?.[1] ?? ""
  const result: FixResult = { preview, before: fix.current, after, verified: true, changeId: nextChangeId++, appliedAt: now() }
  const words = kinds.map((k) => `${jobName(db.name, k)} (${k === "full" ? "daily 22:00" : "every 15 minutes"})`)
  changes.unshift({
    id: result.changeId,
    serverId: srv.id,
    database: db.name,
    changedAt: result.appliedAt,
    object: "Backup schedule",
    changeType: "JOB",
    description: `Created the ${words.length === 1 ? "job" : "jobs"} ${words.join(" and ")} writing to ${dir || preview.options?.folder}, 7 days kept`,
    result: `${after} Verified.`,
  })
  return result
}

// POST .../fixes/remove-backup-schedule/preview and /apply.
function removeSchedulePreview(info: InstanceInfo, db: DatabaseStatus): FixPreview {
  const names = kakapoJobs[db.name] ?? []
  return {
    database: db.name,
    engineVersion: info.productVersion,
    changes: names.map((n) => ({
      setting: n.endsWith("full backup") ? "Full backup job" : "Log backup job",
      current: "exists",
      target: "deleted",
    })),
    statement: deleteJobs(names),
    rollback: scheduleBatch(db.name, `${MOCK_BACKUP_DIR}\\${db.name}`, scheduleKinds(db), db.name === "WarehouseTest" ? "DBA Team" : ""),
    warnings: [
      `The backup files in ${MOCK_BACKUP_DIR}\\${db.name} are not deleted.`,
      `Needs sysadmin on ${info.serverName} (deleting Agent jobs owned by sa).`,
    ],
  }
}

async function applyRemoveSchedule(srv: Server, info: InstanceInfo, db: DatabaseStatus, fix: Fix): Promise<FixResult> {
  const preview = removeSchedulePreview(info, db)
  await delay(800)
  const names = kakapoJobs[db.name] ?? []
  delete kakapoJobs[db.name]
  const result: FixResult = {
    preview,
    before: fix.current,
    after: "no kakapo backup job",
    verified: true,
    changeId: nextChangeId++,
    appliedAt: now(),
  }
  changes.unshift({
    id: result.changeId,
    serverId: srv.id,
    database: db.name,
    changedAt: result.appliedAt,
    object: "Backup schedule",
    changeType: "JOB",
    description: `Deleted the ${names.length === 1 ? "job" : "jobs"} ${names.join(" and ")}`,
    result: "no kakapo backup job, verified",
  })
  return result
}

// Phase 3b: the Storage and space card and its detail per database (phase3b-contract.md), as the backend
// builds them from the scan. One SQL Server 2022 Standard instance: tempdb on T:\, logs on L:\.
// WarehouseLive green (data on D:\); WarehouseTest amber (data on E:\ at 86% full: STO01 Medium, and an
// StockUnitAudit and an IntegrationLog that only grow: IDX10 Medium and Low) with a stored Room for
// CHECKDB result (Medium: tempdb would have to grow); Warehouse_Archive amber (data on F:\, an int
// identity at 92%: IDX13 High, and its StockUnitAudit only grows: IDX10 Medium). WarehouseLive's log
// and history tables are cleaned up nightly: their oldest rows are recent, so no IDX10.
// Texts are the scan's (01-QuickScan.sql section 3, PerformanceTriage.sql IDX01/IDX13, Checks.csv; IDX10 is kakapo's own).

const storageChecks: { checkId: string; title: string; owner: Finding["owner"]; thenCheck: string }[] = [
  { checkId: "STO01", title: "Disk free space", owner: "Client IT", thenCheck: "Re-run: free space above the threshold in STO01." },
  { checkId: "STO02", title: "Percentage autogrowth", owner: "Us", thenCheck: "Re-run: STO02 gone." },
  {
    checkId: "STO03",
    title: "Autogrowth too small or disabled",
    owner: "Us",
    thenCheck: "Re-run: STO03 gone. Fewer growth events in MNT05.",
  },
  { checkId: "STO04", title: "Files close to a size limit", owner: "Us", thenCheck: "Re-run: STO04 gone." },
  {
    checkId: "STO05",
    title: "Database files on the system drive (C:)",
    owner: "Client IT",
    thenCheck: "Re-run after the move: STO05 gone.",
  },
  {
    checkId: "STO06",
    title: "Data and log on the same volume",
    owner: "Client IT",
    thenCheck: "Re-run after the move: STO06 gone and log write latency in STO09 lower.",
  },
  { checkId: "STO07", title: "Log file larger than the data", owner: "Us", thenCheck: "Re-run after the next log backup: STO07 gone." },
  { checkId: "STO08", title: "Virtual log file (VLF) count", owner: "Us", thenCheck: "Re-run: VLF count in STO08 under the threshold." },
  { checkId: "STO09", title: "Storage latency", owner: "Client IT", thenCheck: "Re-run after a business day: latency in STO09 lower." },
  {
    checkId: "TMP01",
    title: "Number of TempDB data files",
    owner: "Us",
    thenCheck: "Re-run after restart: TMP01 gone and PAGELATCH waits in WRK01 lower.",
  },
  { checkId: "TMP02", title: "TempDB data files equal in size and growth", owner: "Us", thenCheck: "Re-run: TMP02 gone." },
  {
    checkId: "TMP03",
    title: "TempDB more than half full",
    owner: "Us",
    thenCheck: "Re-run: TMP03 gone. Find the query that filled it in ACT03 or the live check.",
  },
  { checkId: "IDX01", title: "Where the space goes", owner: "Us", thenCheck: "Context only." },
  { checkId: "IDX07", title: "Oversized or mostly empty files", owner: "Us", thenCheck: "Re-run: IDX07 gone." },
  {
    checkId: "IDX10",
    title: "Application tables that only grow",
    owner: "Vendor",
    thenCheck: "Re-run after the cleanup: the row count stopped growing.",
  },
  {
    checkId: "IDX13",
    title: "Identity columns running out of numbers",
    owner: "Us",
    thenCheck: "Re-run: percent used in IDX13 reset after the change.",
  },
  { checkId: "STO10", title: "Room to run CHECKDB", owner: "Client IT", thenCheck: "Run the estimate again: STO10 passed." },
]

const STO10_NOT_RUN =
  "Not run yet. Check room for CHECKDB reads an estimate from SQL Server; it can take a minute on a large database."

const n0 = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 })
const gb = (mb: number) => `${(mb / 1024).toFixed(1)} GB`

// monthsSince counts the whole months from d to now, as IDX10 does.
function monthsSince(d: Date): number {
  const now = new Date()
  return (now.getFullYear() - d.getFullYear()) * 12 + now.getMonth() - d.getMonth() - (now.getDate() < d.getDate() ? 1 : 0)
}

function storageFinding(
  checkId: string,
  object: string,
  f: Pick<Finding, "severity" | "when" | "effort" | "finding" | "why" | "doThis" | "evidence">,
): Finding {
  const check = storageChecks.find((c) => c.checkId === checkId)!
  return { checkId, title: check.title, owner: check.owner, object, thenCheck: check.thenCheck, ...f }
}

// IDX01: one Info finding whose evidence lists the 10 biggest tables: name, data MB, index MB, rows.
type TableSize = [name: string, dataMB: number, indexMB: number, rows: number]

function biggestTables(database: string, tables: TableSize[]): Finding {
  const top = [...tables].sort((a, b) => b[1] + b[2] - (a[1] + a[2])).slice(0, 10)
  const total = top.reduce((s, t) => s + t[1] + t[2], 0)
  const [name, data, index] = top[0]
  return storageFinding("IDX01", database, {
    severity: "Info",
    when: "None",
    effort: "None",
    finding: `The 10 biggest tables hold ${n0.format(total)} MB; the biggest is ${name} with ${n0.format(data + index)} MB.`,
    why: "The biggest tables in this database. Tables where indexes are much bigger than the data are flagged in IDX02.",
    doThis: "",
    evidence: top
      .map(
        ([t, d, i, r]) =>
          `${t}: ${n0.format(d + i)} MB (${n0.format(d)} MB data, ${n0.format(i)} MB indexes), ${n0.format(r)} rows`,
      )
      .join("; "),
  })
}

// The per-database inputs of STO10: the estimate and the data volume's free space and the data size.
const checkdbInputs: Record<string, { estimateKB: number; dataVolume: string; dataFreeMB: number; dataSizeMB: number }> = {
  WarehouseLive: { estimateKB: 6_291_456, dataVolume: "D:\\", dataFreeMB: 217_088, dataSizeMB: 28_934 },
  WarehouseTest: { estimateKB: 9_175_040, dataVolume: "E:\\", dataFreeMB: 42_086, dataSizeMB: 46_212 },
  Warehouse_Archive: { estimateKB: 13_107_200, dataVolume: "F:\\", dataFreeMB: 192_512, dataSizeMB: 109_322 },
}

// The stored STO10 results by database. WarehouseTest was estimated two days ago, while tempdb was busy.
const checkdbRooms: Record<string, CheckdbRoom | null> = {
  WarehouseTest: checkdbRoomResult("WarehouseTest", daysAgoAt(2, 22, 40).toISOString(), 38.4, 11_264, 98_304),
}

// The verdict of the contract's STO10 table, for the tempdb free and room given.
function checkdbRoomResult(
  database: string,
  capturedAt: string,
  durationSeconds: number,
  tempdbFreeMB: number,
  tempdbRoomMB: number,
): CheckdbRoom {
  const { estimateKB, dataVolume, dataFreeMB, dataSizeMB } = checkdbInputs[database]
  const needMB = Math.round((estimateKB / 1024) * 2)
  let severity: Finding["severity"] = "Info"
  let finding = `CHECKDB on ${database} needs about ${gb(needMB)} of tempdb; tempdb has ${gb(tempdbFreeMB)} free and the data volume has ${gb(dataFreeMB)} free.`
  if (needMB > tempdbRoomMB) {
    severity = "High"
    finding = `CHECKDB on ${database} needs about ${gb(needMB)} of tempdb, but tempdb can only reach ${gb(tempdbRoomMB)}.`
  } else if (needMB > tempdbFreeMB) {
    severity = "Medium"
    finding = `CHECKDB on ${database} needs about ${gb(needMB)} of tempdb; tempdb has ${gb(tempdbFreeMB)} free and would have to grow by ${gb(needMB - tempdbFreeMB)}.`
  }
  const passed = severity === "Info"
  return {
    capturedAt,
    durationSeconds,
    estimateKB,
    needMB,
    tempdbFreeMB,
    tempdbRoomMB,
    dataVolume,
    dataFreeMB,
    dataSizeMB,
    finding: {
      ...storageFinding("STO10", database, {
        severity,
        when: passed ? "None" : "Decide first",
        effort: passed ? "None" : "Moderate",
        finding,
        why: "CHECKDB sorts its work in tempdb and takes an internal snapshot of the database on the data volume; if either runs out of room the check fails part-way, and on a busy server it can fill tempdb for every other database too.",
        doThis: passed
          ? ""
          : `Free space on T:\\ or let tempdb grow to ${gb(needMB)}. Alternatives: run DBCC CHECKDB WITH PHYSICAL_ONLY (needs far less), or run the full check on a restored copy of the last backup, which also tests the backup.`,
        evidence:
          `Estimate: ${n0.format(estimateKB)} KB. Need: ${n0.format(needMB)} MB. TempDB free: ${n0.format(tempdbFreeMB)} MB, ` +
          `room: ${n0.format(tempdbRoomMB)} MB. Data volume ${dataVolume}: ${n0.format(dataFreeMB)} MB free. ` +
          `Data size: ${n0.format(dataSizeMB)} MB. Took ${durationSeconds.toFixed(1)} s.`,
      }),
      ...(passed && { thenCheck: "" }), // a passed check has nothing to check again
    },
  }
}

// POST .../cards/storage/checkdb-room: tempdb has room now, so every database passes.
async function runCheckdbRoom(db: DatabaseStatus): Promise<CheckdbRoom> {
  const started = Date.now()
  await delay(1500) // with the request's 500 ms: the estimate takes 2 seconds
  if (localStorage.getItem("mock.checkdbRoomFails") === "1") {
    throw new ApiError(
      `DBCC CHECKDB WITH ESTIMATEONLY failed on ${db.name}: User 'CLIENTA\\svc-sqlmonitor' does not have permission to run DBCC checkdb for database '${db.name}'. The login needs db_owner in the database or sysadmin.`,
      502,
    )
  }
  const seconds = Math.round((Date.now() - started + 500) / 100) / 10
  const room = checkdbRoomResult(db.name, now(), seconds, 26_880, 113_664)
  checkdbRooms[db.name] = room
  return room
}

function storageCheckStatuses(db: string, findings: Finding[]): CheckStatus[] {
  const room = checkdbRooms[db]
  return storageChecks.map(({ checkId, title }) => {
    if (checkId === "STO10") {
      if (!room) return { checkId, title, status: "skipped", note: STO10_NOT_RUN }
      return { checkId, title, status: room.finding.severity === "Info" ? "passed" : "finding", note: "" }
    }
    const status = findings.some((f) => f.checkId === checkId) ? "finding" : "passed"
    return { checkId, title, status, note: "" }
  })
}

function storageDetail(db: DatabaseStatus): CardDetail {
  const base: Card = {
    id: "storage",
    title: "Storage and space",
    question: "Will it run out of room?",
    status: "green",
    statusText: "No problem found",
    verdict: "",
    facts: [],
    toDo: 0,
    forClientIT: 0,
    fixable: 0,
    asOf: now(),
    note: "",
  }
  const checkdbRoom = checkdbRooms[db.name] ?? null

  if (db.name === "Warehouse_Archive") {
    const findings: Finding[] = [
      storageFinding("IDX13", "[dbo].[StockUnitAudit].ID", {
        severity: "High",
        when: "Decide first",
        effort: "Project",
        finding: "Identity column has used 92.0% of its int range (last value 1,975,684,955 of 2,147,483,647).",
        why: "When the identity reaches the end of its range, every insert into this table fails with an arithmetic overflow error. There is no warning before it happens.",
        doThis:
          "Plan the change to bigint now (the column, every foreign key that references it, and the code that holds the value). Deleting old rows (IDX10) does not reset the identity.",
        evidence: "Type int, seed 1, increment 1.",
      }),
      storageFinding("IDX10", "[dbo].[StockUnitAudit]", {
        severity: "Medium",
        when: "Decide first",
        effort: "Moderate",
        finding: `dbo.StockUnitAudit has 412,906,118 rows (55,016 MB) and looks like a log or history table; its oldest row is from 2018-06-01 (${monthsSince(new Date(2018, 5, 1))} months ago), so nothing seems to remove old rows.`,
        why: "Tables that only grow slow every backup, every integrity check and every query that reads them, and fill the disk.",
        doThis:
          "Agree a retention period with the application's owner (how many months must stay) and have old rows deleted in batches in a quiet hour, or archived; then a scheduled cleanup keeps it that way. Never delete without the owner's say-so.",
        evidence: `Rows: 412,906,118. Size: 55,016 MB. Oldest AuditDate: ${scanTime(new Date(2018, 5, 1, 6, 0))}.`,
      }),
      biggestTables(db.name, [
        ["[dbo].[StockUnitAudit]", 48_912, 6_104, 412_906_118],
        ["[dbo].[Transaction]", 31_440, 4_810, 160_212_885],
        ["[dbo].[SalesOrderLine]", 9_806, 2_114, 31_877_402],
        ["[dbo].[SalesOrder]", 3_120, 990, 7_604_331],
        ["[dbo].[StockUnit]", 2_904, 1_622, 8_940_110],
        ["[dbo].[PickLine]", 1_210, 402, 5_770_214],
        ["[dbo].[CountLine]", 980, 318, 4_812_660],
        ["[dbo].[ReceiptLine]", 702, 210, 3_120_447],
        ["[dbo].[IntegrationLog]", 188, 31, 610_004],
        ["[dbo].[MailLog]", 41, 6, 120_331],
      ]),
    ]
    return {
      card: {
        ...base,
        status: "amber",
        statusText: "2 things to do",
        verdict: findings[0].finding,
        facts: [
          { label: "Tightest drive", value: "F:\\ 47% free (188 GB)", tone: "normal" },
          { label: "Database size", value: "142.7 GB, 22% indexes", tone: "normal" },
          { label: "Disk latency", value: "14 ms read, 2 ms write", tone: "normal" },
          { label: "Identity range", value: "92% used (StockUnitAudit)", tone: "worse" },
        ],
        toDo: 2,
        forClientIT: 0,
      },
      findings,
      checks: storageCheckStatuses(db.name, findings),
      checkdbRoom,
    }
  }

  if (db.name === "WarehouseTest") {
    const oldest = new Date(2023, 1, 14, 8, 12)
    const rows = 7_412_880
    const findings: Finding[] = [
      storageFinding("STO01", "E:\\", {
        severity: "Medium",
        when: "Online",
        effort: "Moderate",
        finding: "Volume E:\\ is 86% full.",
        why: "When a disk fills up, databases cannot grow, transactions and backups fail, and applications stop working.",
        doThis:
          "Free up or add space with the infrastructure team. Look for old backup files, oversized log files (STO07) and files that could move to another volume.",
        evidence: "Free: 41 GB of 300 GB.",
      }),
      storageFinding("IDX10", "[dbo].[StockUnitAudit]", {
        severity: "Medium",
        when: "Decide first",
        effort: "Moderate",
        finding: "dbo.StockUnitAudit has 21,904,018 rows (4,152 MB) and looks like a log or history table.",
        why: "Tables that only grow slow every backup, every integrity check and every query that reads them, and fill the disk.",
        doThis:
          "Agree a retention period with the application's owner (how many months must stay) and have old rows deleted in batches in a quiet hour, or archived; then a scheduled cleanup keeps it that way. Never delete without the owner's say-so.",
        evidence: "Rows: 21,904,018. Size: 4,152 MB. Its date column AuditDate is not indexed, so how far back its rows go was not read.",
      }),
      storageFinding("IDX10", "[dbo].[IntegrationLog]", {
        severity: "Low",
        when: "Decide first",
        effort: "Moderate",
        finding: `dbo.IntegrationLog has ${n0.format(rows)} rows (2,318 MB) and looks like a log or history table; its oldest row is from 2023-02-14 (${monthsSince(oldest)} months ago), so nothing seems to remove old rows.`,
        why: "Tables that only grow slow every backup, every integrity check and every query that reads them, and fill the disk.",
        doThis:
          "Agree a retention period with the application's owner (how many months must stay) and have old rows deleted in batches in a quiet hour, or archived; then a scheduled cleanup keeps it that way. Never delete without the owner's say-so.",
        evidence: `Rows: ${n0.format(rows)}. Size: 2,318 MB. Oldest LoggedAt: ${scanTime(oldest)}.`,
      }),
      biggestTables(db.name, [
        ["[dbo].[Transaction]", 8_104, 5_742, 39_120_644],
        ["[dbo].[IntegrationLog]", 2_071, 247, rows],
        ["[dbo].[StockUnit]", 3_488, 2_940, 10_012_377],
        ["[dbo].[StockUnitAudit]", 3_021, 1_131, 21_904_018],
        ["[dbo].[SalesOrderLine]", 1_860, 1_418, 6_244_091],
        ["[dbo].[SalesOrder]", 616, 522, 1_431_502],
        ["[dbo].[CountLine]", 506, 240, 2_349_870],
        ["[dbo].[PickLine]", 410, 294, 1_941_006],
        ["[dbo].[JobHistory]", 141, 25, 671_233],
        ["[dbo].[MailLog]", 88, 12, 214_960],
      ]),
    ]
    return {
      card: {
        ...base,
        status: "amber",
        statusText: "3 things to do",
        verdict: findings[0].finding,
        facts: [
          { label: "Tightest drive", value: "E:\\ 14% free (41 GB)", tone: "bad" },
          { label: "Database size", value: "61.2 GB, 37% indexes", tone: "normal" },
          { label: "Disk latency", value: "11 ms read, 3 ms write", tone: "normal" },
          { label: "Tables that only grow", value: "2, biggest dbo.StockUnitAudit 4,152 MB", tone: "bad" },
        ],
        toDo: 3,
        forClientIT: 1,
      },
      findings,
      checks: storageCheckStatuses(db.name, findings),
      checkdbRoom,
    }
  }

  // WarehouseLive: data on D:\ with room to spare, the log on L:\, log and history tables cleaned up
  // nightly (their oldest rows are a few weeks old, so IDX10 finds nothing).
  const findings: Finding[] = [
    biggestTables(db.name, [
      ["[dbo].[Transaction]", 7_912, 5_604, 38_214_907],
      ["[dbo].[StockUnit]", 3_410, 2_872, 9_804_112],
      ["[dbo].[StockUnitAudit]", 2_966, 1_108, 21_470_355],
      ["[dbo].[SalesOrderLine]", 1_822, 1_390, 6_120_448],
      ["[dbo].[SalesOrder]", 604, 512, 1_402_977],
      ["[dbo].[CountLine]", 498, 236, 2_311_064],
      ["[dbo].[PickLine]", 402, 288, 1_903_215],
      ["[dbo].[IntegrationLog]", 212, 34, 288_410],
      ["[dbo].[ItemAlias]", 96, 71, 402_118],
      ["[dbo].[JobHistory]", 128, 22, 610_902],
    ]),
  ]
  return {
    card: {
      ...base,
      verdict: "WarehouseLive is 38.4 GB on D:\\, which has 212 GB free.",
      facts: [
        { label: "Tightest drive", value: "D:\\ 42% free (212 GB)", tone: "normal" },
        { label: "Database size", value: "38.4 GB, 41% indexes", tone: "normal" },
        { label: "Disk latency", value: "4 ms read, 1 ms write", tone: "normal" },
        { label: "TempDB", value: "18% full of 32 GB", tone: "normal" },
      ],
    },
    findings,
    checks: storageCheckStatuses(db.name, findings),
    checkdbRoom,
  }
}

// Phase 3e: the Performance card and its detail per database (phase3e-contract.md), as the backend builds
// them. WarehouseLive green (the top wait is disk reads at 22%, nothing else stands out); WarehouseTest amber
// (ACT02 Medium right now, WRK01 CXPACKET 41% Medium, WRK02 Medium, QRY01 Low with an implicit conversion,
// WRK06 Info, SRV04 Info, and a stored deadlock report with two pairs); Warehouse_Archive red (restarted 9
// hours ago: PRS01 Critical, threads at 94%, and ACT01 High, a scheduler session blocking 14 others). Texts are the scan's and the
// triage's (01-QuickScan.sql section 7 and WRK05 to WRK07, PerformanceTriage.sql SRV04, ACT01, ACT03,
// Checks.csv). Mock simplification, as Setup: the instance-level figures are kept per database.

const performanceChecks: { checkId: string; title: string; owner: Finding["owner"]; thenCheck: string }[] = [
  { checkId: "PRS01", title: "Pressure right now", owner: "Us", thenCheck: "Re-run: PRS01 gone." },
  { checkId: "ACT01", title: "Blocking right now", owner: "Us", thenCheck: "Blocking gone and IDX11 shows which table it was on." },
  { checkId: "ACT02", title: "Transactions left open", owner: "Us", thenCheck: "Re-run: ACT02 gone." },
  {
    checkId: "WRK07",
    title: "Sessions idle with an open transaction",
    owner: "Vendor",
    thenCheck: "Re-run: WRK07 gone. Blocking (ACT01) and LCK waits (WRK01) lower.",
  },
  { checkId: "WRK01", title: "Top wait types", owner: "Us", thenCheck: "Re-run after a business day: the top wait's share is lower." },
  {
    checkId: "WRK02",
    title: "CPU pressure (signal waits)",
    owner: "Client IT",
    thenCheck: "Re-run after a business day: signal wait share in WRK02 lower.",
  },
  {
    checkId: "WRK05",
    title: "Poison waits since restart",
    owner: "Us",
    thenCheck: "Re-run after a business day: no new poison wait seconds in WRK05.",
  },
  { checkId: "WRK06", title: "Deadlocks", owner: "Us", thenCheck: "Re-run after 7 days: deadlocks per day in WRK06 lower." },
  {
    checkId: "SRV07",
    title: "Plans compiled per batch",
    owner: "Us",
    thenCheck: "Re-run after a day: compile share in SRV07 lower once the code uses parameters.",
  },
  {
    checkId: "SRV04",
    title: "Which databases use the server",
    owner: "Us",
    thenCheck: "Context: if another database is busier, choose that database and check it too.",
  },
  { checkId: "QRY01", title: "Costliest queries", owner: "Us", thenCheck: "Re-run after a business day: the query's share is lower." },
  {
    checkId: "WRK03",
    title: "Missing index suggestions",
    owner: "Us",
    thenCheck: "Never create a suggested index as it is: check the table's existing indexes first.",
  },
  { checkId: "DLK01", title: "Deadlock report", owner: "Us", thenCheck: "Re-run after a week: fewer deadlocks for the top pair." },
]

const DLK01_NOT_RUN =
  "Not run yet. Read deadlocks (7 days) reads the deadlock graphs SQL Server kept in its system_health session."

function perfFinding(
  checkId: string,
  object: string,
  clock: NonNullable<Finding["clock"]>,
  f: Pick<Finding, "severity" | "when" | "effort" | "finding" | "why" | "doThis" | "evidence">,
): Finding {
  const check = performanceChecks.find((c) => c.checkId === checkId)!
  return { checkId, title: check.title, owner: check.owner, object, thenCheck: check.thenCheck, clock, ...f }
}

// WRK01's group words, why and do-this per wait group, as the scan writes them.
const waitGroups = {
  PAR: {
    word: "parallelism",
    why: "Parallelism: queries split across several CPUs and wait for each other to finish.",
    doThis:
      "Usually tuned with MAXDOP and cost threshold for parallelism (INS04, INS05). If those are fine, look at the biggest queries (WRK04) and missing indexes (WRK03).",
  },
  IO: {
    word: "disk reads",
    why: "Reading data from disk: the data a query needs is not in memory.",
    doThis:
      "Often caused by large scans from missing indexes (WRK03) or too little memory (INS02/INS03), not only slow disks. Compare with storage latency (STO09).",
  },
  LOCK: {
    word: "blocking",
    why: "Blocking: sessions waiting for locks held by other sessions.",
    doThis:
      "Find the blocking queries (a later phase covers this). Long transactions, missing indexes and large updates are the usual causes.",
  },
} as const

function topWait(
  info: InstanceInfo,
  rank: number,
  waitType: string,
  group: keyof typeof waitGroups,
  pct: number,
  severity: Finding["severity"],
  hours: number,
  waits: number,
): Finding {
  const g = waitGroups[group]
  const acts = severity !== "Info"
  return perfFinding("WRK01", info.serverName, "since-restart", {
    severity,
    when: acts ? "Decide first" : "None",
    effort: acts ? "Moderate" : "None",
    finding: `Wait #${rank}: ${waitType} (${pct.toFixed(1)}% of all waiting).`,
    why: g.why,
    doThis: g.doThis,
    evidence: `Waited ${hours} hours over ${n0.format(waits)} waits since ${scanTime(new Date(info.startedAt))}.`,
  })
}

const ACT03_WHY =
  "A handful of queries usually cause most of the load. Many page reads per run means the query reads far more data than it returns."
const ACT03_DO =
  "Give this to someone who can read execution plans. Do not add an index for each query: check IDX02 first, as this table may already have too many."

function databaseLoad(database: string, myCpu: number, myReads: number, busiest: string): Finding {
  return perfFinding("SRV04", database, "since-restart", {
    severity: "Info",
    when: "None",
    effort: "None",
    finding: `This database used ${myCpu.toFixed(1)}% of the query CPU and did ${myReads.toFixed(1)}% of the disk reads on this server since the last restart.`,
    why: "Not every slowness comes from this database. Other systems on the same server compete for the same CPU, memory and disk.",
    doThis:
      myCpu >= 50
        ? "This database is the main user of the server's CPU, so its own queries are the place to look (QRY01)."
        : "No other database stands out. See the busiest databases below.",
    evidence: `CPU share from plans still in cache; disk reads from file statistics. Busiest: ${busiest}`,
  })
}

// A deadlock pair as the report reads it, with the lock types for the DLK01 evidence.
type MockPair = DeadlockPair & { locks: string }

// WarehouseTest's two pairs: picking against the integration post (count and last vary), and a transfer
// against the replenishment job.
function warehouseTestPairs(topCount: number, topLast: string): MockPair[] {
  return [
    {
      procedures: ["dbo.USP_PICKING_CONFIRM", "dbo.IntegrationPostPickList"],
      count: topCount,
      last: topLast,
      objects: ["dbo.StockUnit", "dbo.PickLine"],
      lastVictimStatement:
        "UPDATE dbo.PickLine SET QtyPicked = QtyPicked + @Qty, Status = 'PICKED', ModifiedDate = GETDATE() WHERE ID = @LineID AND SalesOrderID = @SalesOrderID",
      locks: "KEY U on PK_PickLine (victim), KEY X on IX_StockUnit_Barcode (winner)",
    },
    {
      procedures: ["dbo.USP_TRANSFER_MOVE", "dbo.Job_Replenishment"],
      count: 1,
      last: daysAgoAt(4, 10, 5).toISOString(),
      objects: ["dbo.StockUnit"],
      lastVictimStatement:
        "UPDATE dbo.StockUnit SET LocationID = @ToLocationID, ModifiedDate = GETDATE() WHERE ID = @StockUnitID",
      locks: "KEY X on PK_StockUnit (victim), PAGE IX on PK_StockUnit (winner)",
    },
  ]
}

// The server's health warnings the same read finds (phase 3f, item 21): long I/Os on the data disk two
// nights ago and a non-yielding scheduler four days ago, newest first.
function serverHealth(): HealthEvent[] {
  return [
    {
      at: daysAgoAt(2, 2, 15).toISOString(),
      component: "IO_SUBSYSTEM",
      state: "warning",
      detail:
        "7 long I/Os (over 15 s) since startup; the longest pending request has waited 16.2 s on D:\\Data\\WarehouseTest.mdf.",
    },
    {
      at: daysAgoAt(4, 10, 3).toISOString(),
      component: "SYSTEM",
      state: "warning",
      detail: "1 non-yielding task (a worker that would not give up its CPU); 1 memory dump since startup (1 in this interval).",
    },
  ]
}

// The stored DLK01 reports by database. WarehouseTest was read yesterday afternoon.
const deadlockReports: Record<string, DeadlockReport | null> = {
  WarehouseTest: deadlockReport(
    "WarehouseTest",
    daysAgoAt(1, 16, 40).toISOString(),
    41.7,
    "file",
    warehouseTestPairs(4, daysAgoAt(1, 14, 22).toISOString()),
    serverHealth(),
  ),
}


function deadlockReport(
  database: string,
  capturedAt: string,
  durationSeconds: number,
  source: DeadlockReport["source"],
  pairs: MockPair[],
  health: HealthEvent[] = [],
): DeadlockReport {
  const findings = pairs.map(({ procedures: [a, b], count, last, objects, lastVictimStatement, locks }) =>
    perfFinding("DLK01", database, "window", {
      severity: count >= 10 ? "High" : count >= 3 ? "Medium" : "Low",
      when: "Decide first",
      effort: "Moderate",
      finding: `${count} ${count === 1 ? "deadlock" : "deadlocks"} in 7 days between ${a} and ${b}, on ${objects.join(", ")}; the last was ${scanTime(new Date(last))}.`,
      why: "In a deadlock two sessions each wait for the other, and SQL Server kills one. The killed one gets an error and its work is rolled back: a failed scan, a failed integration post.",
      doThis:
        "The fix is ours: an index so each side touches fewer rows, the same order of touching the tables in both procedures, or READ COMMITTED SNAPSHOT for the database (decide first). Start with the victim's statement in the evidence.",
      evidence: `Lock types: ${locks}. The last victim's statement. Query: ${lastVictimStatement}`,
    }),
  )
  return {
    capturedAt,
    durationSeconds,
    days: 7,
    total: pairs.reduce((n, p) => n + p.count, 0),
    source,
    pairs: pairs.map((p) => ({
      procedures: p.procedures,
      count: p.count,
      last: p.last,
      objects: p.objects,
      lastVictimStatement: p.lastVictimStatement,
    })),
    findings: rankFindings(findings),
    ...(health.length > 0 ? { health } : {}),
  }
}

// POST .../cards/performance/deadlocks: WarehouseTest has had one more deadlock on its top pair since the
// stored report; the other two databases have none.
async function runDeadlockReport(info: InstanceInfo, db: DatabaseStatus): Promise<DeadlockReport> {
  const started = Date.now()
  await delay(1500) // with the request's 500 ms: the read takes 2 seconds
  if (localStorage.getItem("mock.deadlocksFails") === "1") {
    throw new ApiError(
      `cannot read the system_health session on ${info.serverName}: VIEW SERVER STATE permission was denied on object 'server', database 'master'. The login needs VIEW SERVER STATE.`,
      502,
    )
  }
  const seconds = Math.round((Date.now() - started + 500) / 100) / 10
  const pairs = db.name === "WarehouseTest" ? warehouseTestPairs(5, hoursAgo(2.3)) : []
  const report = deadlockReport(
    db.name,
    now(),
    seconds,
    db.name === "Warehouse_Archive" ? "ring-buffer" : "file",
    pairs,
    db.name === "Warehouse_Archive" ? [] : serverHealth(),
  )
  deadlockReports[db.name] = report
  return report
}

function performanceCheckStatuses(db: string, findings: Finding[]): CheckStatus[] {
  const report = deadlockReports[db]
  return performanceChecks.map(({ checkId, title }) => {
    if (checkId === "DLK01") {
      if (!report) return { checkId, title, status: "skipped", note: DLK01_NOT_RUN }
      return { checkId, title, status: report.findings.length ? "finding" : "passed", note: "" }
    }
    return { checkId, title, status: findings.some((f) => f.checkId === checkId) ? "finding" : "passed", note: "" }
  })
}

// The phase 3a rules: red with a Critical, amber with anything to do, green otherwise.
function performanceCard(info: InstanceInfo, findings: Finding[], facts: Fact[], greenVerdict: string): Card {
  const actionable = findings.filter((f) => f.severity !== "Info")
  const hours = (Date.now() - new Date(info.startedAt).getTime()) / 3_600_000
  let status: Card["status"] = "green"
  let statusText = "No problem found"
  if (actionable.some((f) => f.severity === "Critical")) {
    status = "red"
    statusText = "Needs attention today"
  } else if (actionable.length > 0) {
    status = "amber"
    statusText = actionable.length === 1 ? "1 thing to do" : `${actionable.length} things to do`
  }
  return {
    id: "performance",
    title: "Performance",
    question: "Is it slow, and why?",
    status,
    statusText,
    verdict: actionable.length ? actionable[0].finding : greenVerdict,
    facts,
    toDo: actionable.length,
    forClientIT: actionable.filter((f) => f.owner === "Client IT").length,
    fixable: 0,
    asOf: now(),
    note:
      hours < 24
        ? `SQL Server restarted ${Math.round(hours)} hours ago: the since-restart figures may not reflect a normal day`
        : "",
  }
}

function performanceDetail(info: InstanceInfo, db: DatabaseStatus): CardDetail {
  const deadlocks = deadlockReports[db.name] ?? null

  if (db.name === "Warehouse_Archive") {
    const findings = rankFindings([
      perfFinding("PRS01", info.serverName, "now", {
        severity: "Critical",
        when: "None",
        effort: "Moderate",
        finding:
          "602 of 640 worker threads are in use right now (94%); 6 of 8 schedulers have 3 or more runnable tasks each.",
        why: "When SQL Server runs out of worker threads, new queries and even new connections wait for a thread (THREADPOOL) and the application hangs, although the CPU may not be busy.",
        doThis:
          "Find what holds the threads: almost always blocking (ACT01 below) or too much parallelism (INS04, INS05). Clear the blocking first. Never raise max worker threads: it hides the cause and costs memory.",
        evidence:
          "Worker threads: 602 of 640 (max_workers_count). Runnable tasks per online scheduler: 2.6 on average. Memory grants waiting: 0. Offline schedulers: 0.",
      }),
      perfFinding("ACT01", "Session 83", "now", {
        severity: "High",
        when: "None",
        effort: "None",
        finding: "Session 83 (AppScheduler on WMS-APP01) is blocking 14 sessions; the longest has waited 361 s.",
        why: 'Blocked users see the application hang. A blocker that is "sleeping" with an open transaction is usually a forgotten transaction in an application or an SSMS window.',
        doThis:
          "Find out what the blocking session is doing and who owns it before acting. Do not kill sessions without checking: rolling back can take as long again.",
        evidence:
          "Program: App Scheduler Service. Status: sleeping. Open transactions: 1. Database: Warehouse_Archive. Last statement: UPDATE dbo.StockUnitAudit SET Archived = 1 WHERE AuditDate < @Cutoff",
      }),
      topWait(info, 1, "LCK_M_U", "LOCK", 38.2, "Medium", 2, 9_412),
      databaseLoad(
        db.name,
        11.4,
        23.9,
        "WarehouseLive 61.2% CPU, 48.1 GB read; WarehouseTest 22.5% CPU, 19.7 GB read; Warehouse_Archive 11.4% CPU, 20.4 GB read; msdb 0.4% CPU, 0.2 GB read",
      ),
      perfFinding("QRY01", "dbo.ArchiveStockUnitAudit", "since-restart", {
        severity: "Info",
        when: "None",
        effort: "None",
        finding:
          "Uses 6.8% of all query CPU on the server: 9 runs, 18,240 ms CPU and 2,904,118 page reads per run. From the plan cache since restart; turn Query Store on for history.",
        why: ACT03_WHY,
        doThis: `${ACT03_DO} To see every statement in dbo.ArchiveStockUnitAudit, open the query: its detail lists them all, most reads first.`,
        evidence:
          "Page reads per run: min 2,881,402, avg 2,904,118, max 2,931,775. Max CPU: 19,604 ms. Top wait: Lock. Query hash: 0x51C0D7A2E94B3F18. Query: UPDATE dbo.StockUnitAudit SET Archived = 1 WHERE AuditDate < @Cutoff AND Archived = 0",
      }),
    ])
    return {
      card: performanceCard(
        info,
        findings,
        [
          { label: "Biggest wait", value: "LCK_M_U 38% (blocking)", tone: "bad" },
          { label: "CPU pressure", value: "11% signal waits", tone: "normal" },
          { label: "Blocking now", value: "14 sessions blocked, longest 361 s", tone: "worse" },
          { label: "Pressure now", value: "threads 94%", tone: "worse" },
        ],
        "",
      ),
      findings,
      checks: performanceCheckStatuses(db.name, findings),
      deadlocks,
    }
  }

  if (db.name === "WarehouseTest") {
    const findings = rankFindings([
      perfFinding("ACT02", "Session 112", "now", {
        severity: "Medium",
        when: "None",
        effort: "None",
        finding:
          "Session 112 (CLIENTA\\jvdmerwe on RPT-SRV02) has had a transaction open for 12 minutes and is doing nothing.",
        why: "An open transaction keeps its locks, which blocks other users, and stops the log from being emptied so it keeps growing.",
        doThis:
          "Ask the owner of the session or program to commit or close it. A sleeping session with an open transaction is usually a bug in the application or a forgotten SSMS window.",
        evidence: `Program: Microsoft SQL Server Management Studio - Query. Started: ${scanTime(new Date(Date.now() - 12 * 60_000))}. Log used: 3.2 MB. Last statement: UPDATE dbo.SalesOrder SET Status = 'HOLD' WHERE Number = 'SO-104233'`,
      }),
      topWait(info, 1, "CXPACKET", "PAR", 41.0, "Medium", 51, 18_204_311),
      perfFinding("WRK02", info.serverName, "since-restart", {
        severity: "Medium",
        when: "Decide first",
        effort: "Moderate",
        finding: "Queries spend 27% of their waiting time queuing for a CPU.",
        why: "Signal wait is time a query was ready to run but had no free CPU. A high share means the server is short of CPU.",
        doThis:
          "Tune the most CPU-expensive queries (WRK04), check MAXDOP and cost threshold (INS04, INS05), and confirm the VM has enough CPU and is not over-committed on its host.",
        evidence: "Signal wait 120,394 s of 445,903 s total wait.",
      }),
      perfFinding("WRK06", info.serverName, "since-restart", {
        severity: "Info",
        when: "None",
        effort: "None",
        finding: "4 deadlock(s) since the last restart (0.8 a day).",
        why: "In a deadlock two sessions each wait for the other, and SQL Server kills one. The killed one gets an error and its work is rolled back: a failed scan, a failed integration post.",
        doThis:
          "Read deadlocks (7 days) below groups the deadlock graphs in the system_health session by the two procedures involved: the fix is usually an index, or the order in which the code touches the tables.",
        evidence: `Counter since ${scanTime(new Date(info.startedAt))}.`,
      }),
      databaseLoad(
        db.name,
        68.0,
        57.3,
        "WarehouseTest 68.0% CPU, 412.6 GB read; WarehouseLive 19.2% CPU, 198.3 GB read; Warehouse_Archive 3.1% CPU, 96.0 GB read; msdb 0.2% CPU, 0.4 GB read",
      ),
      perfFinding("QRY01", "dbo.USP_PICKING_SCAN", "window", {
        severity: "Low",
        when: "Decide first",
        effort: "Moderate",
        finding:
          "Uses 18.4% of this database's query CPU over the last 7 days: 64,771 runs, 212 ms CPU and 48,211 page reads per run. About 9,253 runs a day (one every 9 s): small per run, large in total. Its plan has an implicit conversion that can stop SQL Server using an index.",
        why: ACT03_WHY,
        doThis: `${ACT03_DO} Also ask whether it needs to run this often (a job or integration polling on a timer?). Implicit conversion: usually a variable, parameter or join column whose type differs from the column it is compared with. To see every statement in dbo.USP_PICKING_SCAN, open the query: its detail lists them all, most reads first.`,
        evidence:
          "Page reads per run: min 41,880, avg 48,211, max 61,870. Max CPU: 1,480 ms. Top wait: CPU. Conversion: CONVERT_IMPLICIT(nvarchar(50),[te].[Barcode],0)=[@Barcode]. Query hash: 0x8F3A2C11D07B9E45. Query: SELECT TOP (1) te.ID, te.Barcode, te.Qty, te.LocationID, l.Name AS Location FROM dbo.StockUnit te JOIN dbo.Location l ON l.ID = te.LocationID WHERE te.Barcode = @Barcode AND te.InStock = 1 ORDER BY te.ID DESC",
      }),
    ])
    return {
      card: performanceCard(
        info,
        findings,
        [
          { label: "Biggest wait", value: "CXPACKET 41% (parallelism)", tone: "bad" },
          { label: "CPU pressure", value: "27% signal waits", tone: "bad" },
          { label: "Blocking now", value: "none", tone: "normal" },
          { label: "Pressure now", value: "none", tone: "normal" },
        ],
        "",
      ),
      findings,
      checks: performanceCheckStatuses(db.name, findings),
      deadlocks,
    }
  }

  // WarehouseLive: the top wait is reading from disk at 22%, no blocking and no pressure.
  const findings = rankFindings([
    topWait(info, 1, "PAGEIOLATCH_SH", "IO", 22.4, "Info", 19, 6_120_877),
    databaseLoad(
      db.name,
      74.2,
      61.0,
      "WarehouseLive 74.2% CPU, 198.3 GB read; WarehouseTest 14.9% CPU, 96.1 GB read; Warehouse_Archive 2.2% CPU, 31.4 GB read; msdb 0.3% CPU, 0.4 GB read",
    ),
    perfFinding("QRY01", "dbo.USP_RECEIVE_LOOKUP", "window", {
      severity: "Info",
      when: "None",
      effort: "None",
      finding:
        "Uses 12.6% of this database's query CPU over the last 7 days: 1,204,331 runs, 3 ms CPU and 212 page reads per run. About 172,047 runs a day (one every 1 s): small per run, large in total.",
      why: ACT03_WHY,
      doThis: `${ACT03_DO} Also ask whether it needs to run this often (a job or integration polling on a timer?). To see every statement in dbo.USP_RECEIVE_LOOKUP, open the query: its detail lists them all, most reads first.`,
      evidence:
        "Page reads per run: min 188, avg 212, max 260. Max CPU: 41 ms. Top wait: CPU. Query hash: 0x2D94E0B7A1C35F60. Query: SELECT d.ID, d.Number, d.Status FROM dbo.SalesOrder d WHERE d.Number = @Number AND d.Type = 'RECEIPT'",
    }),
  ])
  return {
    card: performanceCard(
      info,
      findings,
      [
        { label: "Biggest wait", value: "PAGEIOLATCH_SH 22% (disk reads)", tone: "normal" },
        { label: "CPU pressure", value: "9% signal waits", tone: "normal" },
        { label: "Blocking now", value: "none", tone: "normal" },
        { label: "Pressure now", value: "none", tone: "normal" },
      ],
      "Nothing stands out: the top wait is PAGEIOLATCH_SH at 22%, no blocking, no memory or thread pressure.",
    ),
    findings,
    checks: performanceCheckStatuses(db.name, findings),
    deadlocks,
  }
}

function greyCard(id: Card["id"], title: string, question: string, note: string): Card {
  return {
    id,
    title,
    question,
    status: "grey",
    statusText: "Not checked yet",
    verdict: "",
    facts: [],
    toDo: 0,
    forClientIT: 0,
    fixable: 0,
    asOf: null,
    note,
  }
}

// The six cards in their order; the overview carries them and the card detail returns one of them.
function buildCards(info: InstanceInfo, db: DatabaseStatus): Card[] {
  return [
    backupsDetail(db).card,
    setupDetail(info, db).card,
    storageDetail(db).card,
    upkeepDetail(info, db).card,
    performanceDetail(info, db).card,
    greyCard(
      "design",
      "Database design",
      "Is the database built well?",
      "Indexes, heaps, the tables everyone waits on, and the application's stored procedures, per database.",
    ),
  ]
}

function buildOverview(srv: Server, db: DatabaseStatus): Overview {
  const info = instanceInfo(srv, db)
  return {
    instance: info,
    database: db,
    cards: buildCards(info, db),
    // Phase 5c: the tuning fixture's change and the changes recorded by hand (api-tuning.mock.ts).
    recentChanges: withRecordedChanges(changesFor(srv, db.name, 10), srv.id, db.name),
    // Phase 5i: the mock has no load route, so no red flags either.
    redFlags: { flags: [], more: 0, rest: [] },
    checkedAt: now(),
  }
}

function changesFor(srv: Server, database: string, limit: number): Change[] {
  return changes.filter((c) => c.serverId === srv.id && c.database === database).slice(0, limit)
}

function findDatabase(name: string): DatabaseStatus {
  const db = databases.find((d) => d.name === decodeURIComponent(name))
  if (!db) throw new ApiError("database not found", 404)
  return db
}

function findServer(id: string): Server {
  const srv = servers.find((s) => s.id === Number(id))
  if (!srv) throw new ApiError("server not found", 404)
  if (srv.host.toLowerCase().includes("down")) {
    throw new ApiError(`cannot reach ${srv.host}: the server was not found or was not accessible`, 502)
  }
  return srv
}

function testConnection(input: ServerInput): ConnectionInfo {
  if (input.host.toLowerCase().includes("down")) throw new ApiError(`cannot reach ${input.host}`, 502)
  return {
    serverName: input.host.toUpperCase(),
    productVersion: "16.0.4255.2",
    productLevel: "RTM",
    edition: "Standard Edition (64-bit)",
    version: "Microsoft SQL Server 2022 (RTM-CU25) - 16.0.4255.2 (X64)",
  }
}

function registerServer(input: ServerInput): Server {
  const added: Server = {
    id: Math.max(...servers.map((s) => s.id)) + 1,
    name: input.name.trim() || input.host,
    host: input.host,
    auth: input.auth,
    username: input.auth === "sql" ? input.username : undefined,
    database: input.database || undefined,
    encrypt: input.encrypt,
    trustCert: input.trustCert,
    createdAt: now(),
  }
  servers.push(added)
  return added
}

// PUT /api/servers/{id}: test first, then update the registration. The mock keeps no passwords.
function updateServer(id: string, input: ServerInput): Server {
  const srv = servers.find((s) => s.id === Number(id))
  if (!srv) throw new ApiError("server not found", 404)
  if (!input.host.trim()) throw new ApiError("host is required", 400)
  if (input.auth === "sql" && !input.username.trim()) throw new ApiError("user name is required for a SQL login", 400)
  const info = testConnection(input)
  Object.assign(srv, {
    name: input.name.trim() || input.host.trim(),
    host: input.host.trim(),
    auth: input.auth,
    username: input.auth === "sql" ? input.username.trim() : undefined,
    database: input.database.trim() || undefined,
    encrypt: input.encrypt,
    trustCert: input.encrypt && input.trustCert,
    lastVersion: info.productVersion,
    lastEdition: info.edition,
    lastTestedAt: now(),
  } satisfies Partial<Server>)
  return srv
}

/** A feature mock answers its own routes and returns undefined for the rest. */
export type FeatureMock = (method: string, path: string, init?: RequestInit) => Promise<unknown | undefined>
const featureMocks: FeatureMock[] = [
  mockQueries,
  mockBlocking,
  mockBlockingHistory,
  mockSample,
  mockProgress,
  mockTables,
  mockMaintenance,
  mockTuning,
  mockHelpers,
]

export async function mockRequest<T>(path: string, init?: RequestInit): Promise<T> {
  await delay(500)
  const method = init?.method ?? "GET"

  // Phase 4c feature mocks, one file each; the first that answers wins (docs/phase4c-contract.md).
  for (const feature of featureMocks) {
    const answer = await feature(method, path, init)
    if (answer !== undefined) return answer as T
  }
  const body = () => JSON.parse(String(init?.body)) as ServerInput

  // The build stamp (Home's "kakapo build ...", the last line of a Copy block, phase 4a).
  if (method === "GET" && path === "/api/health") return { ok: true, time: now(), version: "dev-mock" } as T
  if (method === "GET" && path === "/api/servers") return structuredClone(servers) as T
  if (method === "POST" && path === "/api/servers/test") return testConnection(body()) as T
  if (method === "POST" && path === "/api/servers") return registerServer(body()) as T

  const one = path.match(/^\/api\/servers\/(\d+)$/)
  if (one && method === "GET") {
    const srv = servers.find((s) => s.id === Number(one[1]))
    if (!srv) throw new ApiError("server not found", 404)
    return structuredClone(srv) as T
  }
  if (one && method === "PUT") return structuredClone(updateServer(one[1], body())) as T

  if (method === "GET") {
    const list = path.match(/^\/api\/servers\/(\d+)\/databases$/)
    if (list) {
      findServer(list[1])
      return structuredClone(databases) as T
    }
    const read = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/?]+)\/(overview|changes)(?:\?limit=(\d+))?$/)
    if (read) {
      const srv = findServer(read[1])
      const db = findDatabase(read[2])
      if (read[3] === "changes") {
        const limit = Number(read[4] ?? 10)
        return withRecordedChanges(changesFor(srv, db.name, limit), srv.id, db.name, limit) as T
      }
      await delay(1500) // the real overview connects and reads the database: a few seconds
      srv.lastVersion = "16.0.4255.2"
      srv.lastEdition = "Standard Edition (64-bit)"
      srv.lastTestedAt = now()
      return structuredClone(buildOverview(srv, db)) as T
    }
  }

  // POST .../cards/storage/checkdb-room: a new STO10 result after 2 seconds. Set localStorage
  // "mock.checkdbRoomFails" to "1" to see the 502 a login without db_owner or sysadmin gets.
  const room = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/cards\/storage\/checkdb-room$/)
  if (method === "POST" && room) {
    findServer(room[1])
    return (await runCheckdbRoom(findDatabase(room[2]))) as T
  }

  // POST .../cards/performance/deadlocks: a new DLK01 report after 2 seconds. Set localStorage
  // "mock.deadlocksFails" to "1" to see the 502 a login without VIEW SERVER STATE gets.
  const deadlocks = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/cards\/performance\/deadlocks$/)
  if (method === "POST" && deadlocks) {
    const srv = findServer(deadlocks[1])
    const db = findDatabase(deadlocks[2])
    return structuredClone(await runDeadlockReport(instanceInfo(srv, db), db)) as T
  }

  // GET .../cards/{card}: the Backups, Setup, Storage, Upkeep and Performance details; the grey cards with
  // empty lists. Set
  // localStorage "mock.cardDetailFails" to "1" to see the error state.
  const detail = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/cards\/([^/]+)$/)
  if (method === "GET" && detail) {
    const srv = findServer(detail[1])
    const db = findDatabase(detail[2])
    await delay(1000) // read live, like the overview
    if (localStorage.getItem("mock.cardDetailFails") === "1") {
      throw new ApiError(`cannot reach ${srv.host}: the server was not found or was not accessible`, 502)
    }
    if (detail[3] === "backups") return structuredClone(backupsDetail(db)) as T
    // Phase 5a: the archive fix on IDX10's findings and the Tables row's entries (api-tables.mock.ts).
    if (detail[3] === "storage") return structuredClone(withArchives(storageDetail(db), db.name)) as T
    if (detail[3] === "setup") return structuredClone(setupDetail(instanceInfo(srv, db), db)) as T
    // Phase 5b: the Maintenance plan row and its fixes (api-maintenance.mock.ts).
    if (detail[3] === "upkeep") return structuredClone(withMaintenancePlan(upkeepDetail(instanceInfo(srv, db), db), db.name)) as T
    // Phase 5d: the stored blocking history and BLK01 in the checks (api-blocking-history.mock.ts).
    if (detail[3] === "performance") return structuredClone(withBlockingHistory(performanceDetail(instanceInfo(srv, db), db), db.name)) as T
    const card = buildCards(instanceInfo(srv, db), db).find((c) => c.id === detail[3])
    if (!card) throw new ApiError("card not found", 404)
    return structuredClone({ card, findings: [], checks: [] } satisfies CardDetail) as T
  }

  const remember = path.match(/^\/api\/servers\/(\d+)\/database$/)
  if (method === "PUT" && remember) {
    const srv = servers.find((s) => s.id === Number(remember[1]))
    if (!srv) throw new ApiError("server not found", 404)
    const name = (JSON.parse(String(init?.body)) as { database?: string }).database?.trim()
    if (!name) throw new ApiError("database is required", 400)
    srv.database = name
    return structuredClone(srv) as T
  }

  // POST .../fixes/{fixId}/preview and /apply (phase 3c), Query Store included ("query-store"), the
  // Upkeep card's "update-statistics" (phase 3d), and the Backups card's "backup-schedule" with its
  // options body and "remove-backup-schedule" (phase 3g).
  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/([^/]+)\/(preview|apply)$/)
  if (method === "POST" && fix) {
    const srv = findServer(fix[1])
    const db = findDatabase(fix[2])
    const info = instanceInfo(srv, db)
    const found = findFix(info, db, decodeURIComponent(fix[3]))
    const options = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }).options : undefined
    if (found.id === "backup-schedule") {
      if (fix[4] === "preview") return structuredClone(backupSchedulePreview(info, db, options)) as T
      return structuredClone(await applyBackupSchedule(srv, info, db, found, options)) as T
    }
    if (found.id === "remove-backup-schedule") {
      if (fix[4] === "preview") return structuredClone(removeSchedulePreview(info, db)) as T
      return structuredClone(await applyRemoveSchedule(srv, info, db, found)) as T
    }
    if (found.id === "update-statistics") {
      if (fix[4] === "preview") return structuredClone(statisticsPreview(info, db, found)) as T
      // Phase 4f: a long fix answers at once with a pending run, which the dialog polls.
      const pending = startMockRun(found.id, db.name, statisticsPreview(info, db, found), found.current, () =>
        applyStatistics(srv, info, db, found),
      )
      return structuredClone(pending) as T
    }
    if (fix[4] === "preview") return structuredClone(fixPreview(info, db, found)) as T
    return structuredClone(await applyFix(srv, info, db, found)) as T
  }

  throw new ApiError(`mock: ${method} ${path} is not mocked`, 501)
}
