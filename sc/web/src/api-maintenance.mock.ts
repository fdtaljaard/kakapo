import { ApiError, type CardDetail, type Fix, type FixInput, type FixPreview, type FixResult, type MaintenancePlan, type PlanPiece } from "./api"
import type { FeatureMock } from "./api.mock"

// Phase 5b (docs/phase5b-contract.md): fixture answers for the Upkeep card's Maintenance plan row and its fixes:
// "maintenance-plan" (the composite), "history-cleanup", and the pieces' own fixes as the row's Create opens
// them (operator-alerts, maintenance-job, checkdb-job; the backup schedule is the main mock's). WarehouseLive is
// looked after but nothing prunes its history; the other databases have nothing scheduled (Warehouse_Archive is in
// SIMPLE recovery). An apply marks the pieces created. Set localStorage "mock.planFails" to "1" to see the
// composite stop at the integrity check.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const today = () => new Date().toISOString().slice(0, 10)

type PieceId = "full-backup" | "log-backup" | "integrity-check" | "statistics" | "indexes" | "history-cleanup" | "alerts"

const defs: Record<PieceId, { title: string; cadence: string; source: string; fixId: string; options?: Record<string, string> }> = {
  "full-backup": {
    title: "Full backup",
    cadence: "daily 22:00",
    fixId: "backup-schedule",
    source:
      'A full backup every evening, after the day\'s work and after the weekly maintenance. Ola Hallengren runs a weekly full backup with daily differentials; kakapo keeps a daily full because the sites it serves are small enough and a differential chain is one more thing to get wrong. Source: Ola Hallengren, SQL Server Maintenance Solution FAQ ("Full backup: one day per week; differential: all other days").',
  },
  "log-backup": {
    title: "Log backup",
    cadence: "every 15 minutes, FULL recovery only",
    fixId: "backup-schedule",
    source:
      'The log backup empties the transaction log (the file of every change since the last backup) and sets how much work a failure can lose. Ola Hallengren\'s FAQ backs the log up every hour; 15 minutes is the data-loss window the Backups card recommends (BAK05). Source: Ola Hallengren, FAQ ("transaction log backup: every hour"); kakapo BAK05.',
  },
  "integrity-check": {
    title: "Integrity check",
    cadence: "weekly, Sunday 01:00; physical only from 200 GB",
    fixId: "checkdb-job",
    source:
      'DBCC CHECKDB (the check that finds corruption) at least once a week, so damage is found while a clean backup still exists. Source: Paul Randal, as reported in Microsoft\'s CSS SQL Server Engineers blog by Cindy Gross ("some consistency check at least once a week"); Ola Hallengren, FAQ ("integrity check: one day per week"). From 200 GB WITH PHYSICAL_ONLY: Microsoft, "CHECKDB part 6: consistency checking options for a VLDB".',
  },
  statistics: {
    title: "Statistics update",
    cadence: "nightly 01:00, Monday to Saturday",
    fixId: "maintenance-job",
    options: { statisticsOnly: "true", day: "Sunday" },
    source:
      'Statistics (the row counts the optimizer plans queries with) updated every night the integrity check does not run. Source: Microsoft, "Maintain indexes optimally"; Brent Ozar, "Stop worrying about index fragmentation" (update statistics daily); DarlingData, "Ola Stats Only Job" (@StatisticsModificationLevel = 5).',
  },
  indexes: {
    title: "Index maintenance",
    cadence: "weekly, Sunday 03:00, off by default",
    fixId: "maintenance-job",
    options: { statisticsOnly: "false", indexOnly: "true", day: "Sunday", indexTime: "03:00" },
    source:
      'Microsoft (2026): do not assume that index maintenance always noticeably improves the workload, or that it must run on a fixed schedule; reorganise is preferred over rebuild ("Maintain indexes optimally"). Offered with Brent Ozar\'s thresholds (reorganise from 50%; from 80%, rebuild online where the edition can and reorganise elsewhere, never offline) and Ola Hallengren\'s floor (1,000 pages or more) when IndexOptimize is installed; not counted as missing.',
  },
  "history-cleanup": {
    title: "History cleanup",
    cadence: "weekly, Sunday 05:00, keep 30 days",
    fixId: "history-cleanup",
    source:
      'msdb (SQL Server\'s own database of jobs and backups) grows with every backup and every job run. Source: Microsoft, "sp_delete_backuphistory" ("we recommend that you periodically execute sp_delete_backuphistory"); Ola Hallengren\'s installation prunes backup and job history weekly, keeping 30 days.',
  },
  alerts: {
    title: "Alerts and an operator",
    cadence: "once",
    fixId: "operator-alerts",
    options: { alerts: "true" },
    source:
      "An operator (a mailbox SQL Server Agent can mail) and alerts for severity 19 to 25 and errors 823, 824 and 825 (corruption), so damage and fatal errors are reported when they happen. Source: DarlingData, Alerts.sql; kakapo MNT07.",
  },
}

const order =
  'Ola Hallengren\'s FAQ says to "perform the full backup after the integrity check", and after index maintenance. So on Sunday the integrity check runs at 01:00, index work (when on) at 03:00 and the history cleanup at 05:00; the daily full backup at 22:00 follows them; statistics run on the other nights at 01:00; log backups run through everything. The times are defaults: change them under More options when you set the plan up.'

// The pieces each database has in place (with their job), and what was created from the mock.
interface MockPlanState {
  jobs: Partial<Record<PieceId, { job: string; schedule: string; other?: boolean; created?: boolean }>>
  simple?: boolean
  operator?: boolean
}

// Phase 5u: whether Ola Hallengren's IndexOptimize is in master, per server (WarehouseLive's server has it).
let olaInstalled = false

const states = new Map<string, MockPlanState>()

function stateOf(db: string): MockPlanState {
  let st = states.get(db)
  if (!st) {
    st =
      db === "WarehouseLive"
        ? {
            operator: true,
            jobs: {
              "full-backup": { job: "kakapo: WarehouseLive full backup", schedule: "daily 22:00" },
              "log-backup": { job: "kakapo: WarehouseLive log backup", schedule: "every 15 minutes" },
              "integrity-check": { job: "DatabaseIntegrityCheck - USER_DATABASES", schedule: "Sunday 00:30", other: true },
              statistics: { job: "kakapo: statistics (WarehouseLive)", schedule: "Monday to Saturday 01:00" },
            },
          }
        : { jobs: {}, simple: db === "Warehouse_Archive" }
    states.set(db, st)
  }
  return st
}

function lastRunOf(schedule: string): string {
  const clock = /(\d\d:\d\d)/.exec(schedule)?.[1] ?? "01:00"
  return `${today()} ${clock}`
}

function planOf(db: string): MaintenancePlan {
  const st = stateOf(db)
  const pieces: PlanPiece[] = (Object.keys(defs) as PieceId[]).map((id) => {
    const d = defs[id]
    const p: PlanPiece = { id, title: d.title, cadence: d.cadence, status: "missing", source: d.source }
    const j = st.jobs[id]
    if (id === "log-backup" && st.simple) {
      return { ...p, status: "not-needed", note: "The database is in SIMPLE recovery: the log empties itself (DBC08 decides the model)." }
    }
    if (id === "alerts") {
      return st.operator
        ? { ...p, status: "in-place", note: "1 operator(s) with an email address; every alert mails one." }
        : { ...p, fixId: d.fixId, options: d.options, note: "No operator with an email address: no alert or job failure reaches anyone." }
    }
    if (j) {
      return j.created
        ? { ...p, status: "never-ran", job: j.job, schedule: j.schedule, note: "The job has not run yet." }
        : {
            ...p,
            status: j.other ? "other-job" : "in-place",
            job: j.job,
            schedule: j.schedule,
            lastRun: lastRunOf(j.schedule),
            lastOutcome: "succeeded",
            lastDuration: id === "integrity-check" ? "18 min" : "3 min",
          }
    }
    if (id === "indexes") {
      if (db !== "WarehouseLive" && !olaInstalled) {
        return {
          ...p,
          status: "optional",
          fixId: "install-ola-index",
          note: "Index work needs Ola Hallengren's IndexOptimize in master: Install puts the copy inside kakapo there (version 2026-09-12 15:02:38; https://ola.hallengren.com). Statistics matter more.",
        }
      }
      return { ...p, status: "optional", fixId: d.fixId, options: d.options }
    }
    const backupMade = !!st.jobs["full-backup"] || !!st.jobs["log-backup"]
    if ((id === "full-backup" || id === "log-backup") && backupMade) {
      return { ...p, note: "kakapo's backup schedule exists without this job: remove it on the Backups card and create it again." }
    }
    return { ...p, fixId: d.fixId, options: d.options }
  })
  const missing = pieces.filter((p) => p.fixId && p.id !== "indexes" && p.status === "missing").length
  return { pieces, missing, order, ...(missing > 0 ? { fixId: "maintenance-plan" } : {}) }
}

// The Upkeep card with the Maintenance plan row and its fixes.
export function withMaintenancePlan(detail: CardDetail, database: string): CardDetail {
  const plan = planOf(database)
  const fixes: Fix[] = [...(detail.fixes ?? [])]
  if (!stateOf(database).jobs["history-cleanup"]) {
    fixes.push({
      id: "history-cleanup",
      title: "Prune msdb history weekly",
      scope: "server",
      checkId: "MNT08",
      setting: "msdb history",
      current: "182,400 backup and 9,420 job history rows, nothing prunes them",
      target: "kakapo: history cleanup (Sunday 05:00, keeps 30 days)",
      needsRestart: false,
    })
  }
  if (plan.fixId) {
    fixes.push({
      id: "maintenance-plan",
      title: "Set up the maintenance plan",
      scope: "server",
      checkId: "MNT03",
      setting: "Maintenance plan",
      current: `${plan.missing} piece(s) missing`,
      target: "every piece scheduled, times that do not overlap",
      needsRestart: false,
    })
  }
  return { ...detail, fixes, maintenancePlan: plan }
}

const sysadmin = "Needs sysadmin on WMS-SQL01 (creating Agent jobs owned by sa)."
const time = (v: string | undefined, def: string) => {
  const t = (v ?? def).trim()
  return { value: t, ok: /^([01]?\d|2[0-3]):[0-5]\d$/.test(t) }
}

function historyPreview(options: Record<string, string> = {}): FixPreview {
  const keepText = (options.keepDays ?? "30").trim()
  const keep = Number(keepText)
  const keepOk = /^\d+$/.test(keepText) && keep >= 7 && keep <= 365
  const at = time(options.time, "05:00")
  const day = options.day ?? "Sunday"
  const inputs: FixInput[] = [
    { name: "keepDays", label: "Keep history (days)", value: keepText, kind: "text", help: "Backup, restore and job history older than this is deleted every week: 7 to 365 days." },
    { name: "time", label: "Time", value: at.value, kind: "text", help: "Weekly, server time.", advanced: true },
    {
      name: "day",
      label: "Day",
      value: day,
      kind: "select",
      options: ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"],
      help: "The day of the week the cleanup runs.",
      advanced: true,
    },
  ]
  const blocked: string[] = []
  if (!keepOk) blocked.push((inputs[0].warning = "Keep between 7 and 365 days: the Backups card reads the last 7 days of backup history, so fewer cannot be kept."))
  if (!at.ok) blocked.push((inputs[1].warning = "The time must be HH:MM, for example 05:00."))
  const preview: FixPreview = {
    database: "",
    engineVersion: "16.0.4255.2",
    changes: [],
    statement: "",
    rollback: "",
    inputs,
    options: { keepDays: keepText, time: at.value, day },
    warnings: [
      ...blocked,
      "The backup files themselves are not touched: this deletes only msdb's record of them, of restores and of job runs.",
      "The Backups card reads the last 7 days of backup history, so fewer than 7 days cannot be kept; 30 days (Ola Hallengren's default) also keeps a month for questions about past backups, restores and job runs.",
      "There is no undoing the cleanup itself: the rollback deletes the job, but the history rows it has deleted are gone.",
      sysadmin,
    ],
  }
  if (blocked.length > 0) return preview
  const n = keepOk ? keep : 30
  preview.changes = [
    { setting: "History cleanup job", current: "none", target: `kakapo: history cleanup, ${day} ${at.value}, keeps ${n} days` },
    { setting: "Backup history (msdb.dbo.backupset)", current: "182,400 rows since 2019-03-02", target: `176,910 rows older than ${n} days deleted at the first run` },
    { setting: "Restore history (msdb.dbo.restorehistory)", current: "12 rows since 2024-05-14", target: `12 rows older than ${n} days deleted at the first run` },
    { setting: "Job history (msdb.dbo.sysjobhistory)", current: `9,420 rows since ${today()}`, target: `nothing older than ${n} days yet` },
    { setting: "Tell on failure", current: "", target: "Database support" },
  ]
  preview.statement = [
    "DECLARE @owner sysname = SUSER_SNAME(0x01);",
    "BEGIN TRY",
    "    BEGIN TRANSACTION;",
    "    EXEC msdb.dbo.sp_add_job @job_name = N'kakapo: history cleanup', @enabled = 1, @owner_login_name = @owner;",
    `    EXEC msdb.dbo.sp_add_jobstep @job_name = N'kakapo: history cleanup', @step_id = 1, @step_name = N'Delete history older than ${n} days', @command = N'/* kakapo */`,
    `DECLARE @oldest datetime = DATEADD(DAY, -${n}, GETDATE());`,
    "EXEC msdb.dbo.sp_delete_backuphistory @oldest_date = @oldest;",
    "EXEC msdb.dbo.sp_purge_jobhistory @oldest_date = @oldest;",
    "IF OBJECT_ID(N''master.dbo.CommandLog'') IS NOT NULL DELETE FROM master.dbo.CommandLog WHERE StartTime < @oldest;';",
    "    COMMIT TRANSACTION;",
    "END TRY",
    "BEGIN CATCH",
    "    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;",
    "    THROW;",
    "END CATCH;",
  ].join("\n")
  preview.rollback = "EXEC msdb.dbo.sp_delete_job @job_name = N'kakapo: history cleanup';"
  return preview
}

// install-ola-index's preview (docs/phase5u-contract.md, section 2), with a shortened statement.
function olaPreview(): FixPreview {
  const v = "version 2026-09-12 15:02:38"
  return {
    database: "",
    engineVersion: "16.0.4255.2",
    lead: "Installs Ola Hallengren's IndexOptimize in master, from the copy inside kakapo, so the maintenance plan can offer weekly index work.",
    changes: [
      { setting: "Table master.dbo.CommandLog", current: "missing", target: "created" },
      { setting: "Procedure master.dbo.CommandExecute", current: "missing", target: v },
      { setting: "Procedure master.dbo.IndexOptimize", current: "missing", target: v },
    ],
    statement: [
      "/* kakapo: Ola Hallengren's SQL Server Maintenance Solution (https://ola.hallengren.com), version 2026-09-12 15:02:38, MIT licence: CommandLog, CommandExecute and IndexOptimize only, no jobs. */",
      "USE master;",
      "GO",
      "-- CommandLog.sql, CommandExecute.sql and IndexOptimize.sql as Ola wrote them (about 200 KB)",
    ].join("\n"),
    rollback: ["USE master;", "GO", "DROP PROCEDURE dbo.IndexOptimize;", "DROP PROCEDURE dbo.CommandExecute;", "DROP TABLE dbo.CommandLog;"].join("\n"),
    warnings: [
      "Index work stays off until you turn it on in the maintenance plan.",
      "The rollback removes them again: a job that calls IndexOptimize fails after it.",
    ],
  }
}

// A piece's own fix as the row's Create opens it: one job (or the operator and alerts).
function piecePreview(fixId: string, options: Record<string, string> = {}): FixPreview {
  const job =
    fixId === "checkdb-job"
      ? "kakapo: {db} integrity check, weekly on Sunday at 01:00"
      : fixId === "operator-alerts"
        ? "operator Database support and 10 alerts"
        : options.indexOnly === "true"
          ? "kakapo: indexes ({db}), Sunday 03:00"
          : "kakapo: statistics ({db}), Monday to Saturday 01:00"
  return {
    database: "",
    engineVersion: "16.0.4255.2",
    changes: [{ setting: fixId === "operator-alerts" ? "Alerts" : "Job", current: "none", target: job }],
    statement: `-- ${fixId}: the statement its own preview builds`,
    rollback: `-- ${fixId}: its rollback`,
    warnings: [sysadmin],
    options,
  }
}

const planPieces: { name: string; ids: PieceId[]; label: string; fixId: string }[] = [
  { name: "alerts", ids: ["alerts"], label: "Alerts and an operator", fixId: "operator-alerts" },
  { name: "cleanup", ids: ["history-cleanup"], label: "Weekly history cleanup", fixId: "history-cleanup" },
  { name: "check", ids: ["integrity-check"], label: "Weekly integrity check", fixId: "checkdb-job" },
  { name: "statistics", ids: ["statistics"], label: "Nightly statistics update", fixId: "maintenance-job" },
  { name: "index", ids: ["indexes"], label: "Weekly index maintenance (optional)", fixId: "maintenance-job" },
  { name: "backup", ids: ["full-backup", "log-backup"], label: "Full and log backups", fixId: "backup-schedule" },
]

function planPreview(db: string, options: Record<string, string> = {}): FixPreview {
  const plan = planOf(db)
  if (!plan.fixId) throw new ApiError(`nothing to apply on ${db}: every piece of the maintenance plan is in place`, 404)
  const creatable = (id: PieceId) => plan.pieces.some((p) => p.id === id && p.fixId && (p.status === "missing" || p.status === "optional"))
  const offered = planPieces.filter((x) => x.ids.some(creatable))
  const on = (name: string) => (options[name] ?? (name === "index" ? "false" : "true")) === "true"
  const echo: Record<string, string> = {}
  const inputs: FixInput[] = offered.map((x) => {
    echo[x.name] = String(on(x.name))
    return { name: x.name, label: x.label, value: echo[x.name], kind: "bool", help: defs[x.ids[0]].cadence }
  })
  const blocked: string[] = []
  if (offered.some((x) => x.name === "backup") && on("backup")) {
    const folder = options.folder ?? "D:\\SQLBackup"
    const input: FixInput = { name: "folder", label: "Backup folder", value: folder, kind: "text", help: "A folder on the database server, as the SQL Server service sees it." }
    if (!/^([A-Za-z]:\\|\\\\)/.test(folder)) blocked.push((input.warning = `Backups: The folder ${folder} is not a full Windows path (like D:\\Backup or \\\\server\\share): type another.`))
    inputs.push(input)
    echo.folder = folder
  }
  if (offered.some((x) => x.name === "alerts") && on("alerts")) {
    const email = (options.email ?? "").trim()
    const operator = options.operator ?? "Database support"
    const emailInput: FixInput = { name: "email", label: "Email address", value: email, kind: "text", help: "The address that receives the alerts, for example dba@client.co.za." }
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email)) {
      blocked.push((emailInput.warning = "Alerts and an operator: Type the email address that receives the alerts, for example dba@client.co.za."))
    }
    inputs.push({ name: "operator", label: "Operator name", value: operator, kind: "text", help: "The operator the alerts mail." }, emailInput)
    echo.operator = operator
    echo.email = email
  }
  for (const [name, label, def] of [
    ["checkTime", "Integrity check at", "01:00"],
    ["indexTime", "Index maintenance at", "03:00"],
    ["cleanupTime", "History cleanup at", "05:00"],
    ["statsTime", "Statistics at", "01:00"],
    ["fullTime", "Full backup at", "22:00"],
  ] as const) {
    const t = time(options[name], def)
    const input: FixInput = { name, label, value: t.value, kind: "text", help: "Server time.", advanced: true }
    if (!t.ok) blocked.push((input.warning = `${label}: The time must be HH:MM, for example 01:00.`))
    inputs.push(input)
    echo[name] = t.value
  }
  const chosen = offered.filter((x) => on(x.name))
  if (chosen.length === 0) blocked.push("Tick at least one piece to create.")
  const preview: FixPreview = {
    database: "",
    engineVersion: "16.0.4255.2",
    changes: [],
    statement: "",
    rollback: "",
    inputs,
    options: echo,
    warnings: [
      ...blocked,
      `kakapo creates the pieces one after another, each through its own fix: ${chosen.map((x) => x.label.toLowerCase()).join(", then ")}. If one fails, the pieces before it stay (each has its own change log entry and rollback) and the rest are not created.`,
      sysadmin,
    ],
  }
  if (blocked.length > 0) return preview
  preview.changes = chosen.map((x) => ({ setting: `${x.label}: Job`, current: "none", target: defs[x.ids[0]].cadence }))
  preview.statement = chosen.map((x, i) => `-- ${i + 1}. ${x.label} (${x.fixId})\n-- the statement ${x.fixId} builds\nGO`).join("\n")
  preview.rollback = [...chosen].reverse().map((x) => `-- Undo ${x.label}\n-- the rollback of ${x.fixId}\nGO`).join("\n")
  return preview
}

function result(preview: FixPreview, before: string, after: string, verified = true): FixResult {
  return { preview, before, after, verified, changeId: 950 + Math.floor(Math.random() * 49), appliedAt: new Date().toISOString() }
}

function create(db: string, id: PieceId) {
  const st = stateOf(db)
  if (id === "alerts") {
    st.operator = true
    return
  }
  const schedule = id === "history-cleanup" ? "Sunday 05:00" : id === "integrity-check" ? "Sunday 01:00" : id === "indexes" ? "Sunday 03:00" : id === "statistics" ? "Monday to Saturday 01:00" : id === "full-backup" ? "daily 22:00" : "every 15 minutes"
  const name =
    id === "history-cleanup"
      ? "kakapo: history cleanup"
      : id === "statistics"
        ? `kakapo: statistics (${db})`
        : id === "indexes"
          ? `kakapo: indexes (${db})`
          : `kakapo: ${db} ${defs[id].title.toLowerCase()}`
  st.jobs[id] = { job: name, schedule, created: true }
}

export const mockMaintenance: FeatureMock = async (method, path, init) => {
  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/(maintenance-plan|history-cleanup|operator-alerts|maintenance-job|checkdb-job|install-ola-index)\/(preview|apply)$/)
  if (!fix || method !== "POST") return undefined
  const db = decodeURIComponent(fix[2])
  const options = init?.body ? ((JSON.parse(String(init.body)) as { options?: Record<string, string> }).options ?? {}) : {}
  if (fix[3] === "history-cleanup") {
    if (stateOf(db).jobs["history-cleanup"]) throw new ApiError("nothing to apply: kakapo's history cleanup job exists already", 404)
    const preview = historyPreview(options)
    if (fix[4] === "preview") return preview
    if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
    await delay(1000)
    create(db, "history-cleanup")
    return result(preview, "182,400 backup and 9,420 job history rows, nothing prunes them", "kakapo: history cleanup: enabled, next run not set by Agent yet.")
  }
  if (fix[3] === "install-ola-index") {
    const preview = olaPreview()
    if (fix[4] === "preview") return preview
    await delay(1500)
    olaInstalled = true
    return result(preview, "missing", "version 2026-09-12 15:02:38 (CommandLog, CommandExecute, IndexOptimize)")
  }
  if (fix[3] !== "maintenance-plan") {
    const preview = piecePreview(fix[3], options)
    if (fix[4] === "preview") return preview
    await delay(1000)
    const ids: PieceId[] = fix[3] === "checkdb-job" ? ["integrity-check"] : fix[3] === "operator-alerts" ? ["alerts"] : options.indexOnly === "true" ? ["indexes"] : options.statisticsOnly === "false" ? ["statistics", "indexes"] : ["statistics"]
    ids.forEach((id) => create(db, id))
    return result(preview, "none", `${preview.changes[0].target}: created.`)
  }
  const preview = planPreview(db, options)
  if (fix[4] === "preview") return preview
  if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
  await delay(2000)
  const before = `${planOf(db).missing} piece(s) missing`
  const o = preview.options ?? {}
  const chosen = planPieces.filter((x) => o[x.name] === "true")
  const fails = localStorage.getItem("mock.planFails") === "1"
  const done: string[] = []
  for (const x of chosen) {
    if (fails && x.name === "check") {
      const rest = chosen.slice(chosen.indexOf(x) + 1).map((y) => y.label.toLowerCase())
      return result(
        preview,
        before,
        `${done.join("; ")}. Stopped at integrity check: SQL Server refused to create the integrity check job: permission denied.${rest.length ? ` Not created: ${rest.join(", ")}.` : ""}`,
        false,
      )
    }
    x.ids.forEach((id) => create(db, id))
    done.push(`${x.label}: created, not run yet`)
  }
  return result(preview, before, `${done.join("; ")}.`)
}
