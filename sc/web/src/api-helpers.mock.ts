import { ApiError, type FixPreview, type FixResult, type InstalledHelpers, type XESession } from "./api"
import type { FeatureMock } from "./api.mock"
import { installXESessionFixId, removeXESessionFixId } from "./api-helpers"

// Phase 5e (docs/phase5e-contract.md): fixture answers for the Installed helpers row (GET .../helpers) and
// the two helper fixes' previews and applies; undefined means "not mine". Every server starts with nothing
// installed; Create installs kakapo's session (the row then shows it running), Remove drops it. Set
// localStorage "mock.helpersFails" to "1" for the row's 502, "mock.helpersFolderMissing" to "1" for a
// preview blocked by a missing folder.

const LOG = String.raw`C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Log`
const installed = new Map<number, XESession>()

function flag(name: string): boolean {
  try {
    return localStorage.getItem(name) === "1"
  } catch {
    return false
  }
}

function session(threshold: number, folder: string, fileMB: number, files: number): XESession {
  return {
    exists: true,
    running: true,
    ours: true,
    events: ["sqlserver.blocked_process_report", "sqlserver.xml_deadlock_report"],
    targets: ["package0.event_file"],
    fileName: `${folder}\\kakapo.xel`,
    currentFile: `${folder}\\kakapo_0_134070000000000000.xel`,
    maxFileMB: fileMB,
    maxFiles: files,
    dispatchSeconds: 30,
    startupState: true,
    startedAt: new Date().toISOString(),
    eventsCaptured: 0,
    thresholdSeconds: threshold,
  }
}

function utc(iso: string): string {
  return `${iso.slice(0, 16).replace("T", " ")} UTC`
}

function helpers(serverId: number): InstalledHelpers {
  const s = installed.get(serverId)
  if (!s) {
    const sentence =
      "Nothing installed. kakapo's event session would keep every block over 5 seconds and every deadlock on the server, so the blocking history and the deadlock report no longer depend on system_health's five-minute snapshots."
    return {
      serverName: "SQL01\\SQL2022",
      database: "WarehouseLive",
      sentence,
      helpers: [
        {
          id: "xe-session",
          title: "kakapo's event session (blocks and deadlocks)",
          state: "not installed",
          sentence,
          details: [],
          fixId: installXESessionFixId,
          fixLabel: "Create",
          session: { exists: false, running: false, ours: false, events: [], targets: [], startupState: false, eventsCaptured: 0, thresholdSeconds: 0 },
        },
      ],
    }
  }
  s.eventsCaptured += 7
  const pattern = s.fileName!.replace(/kakapo\.xel$/, "kakapo*.xel")
  const sentence = `kakapo's event session is running since ${utc(s.startedAt!)}, keeping every block over ${s.thresholdSeconds} s and every deadlock in ${pattern}; ${s.eventsCaptured} events captured so far.`
  return {
    serverName: "SQL01\\SQL2022",
    database: "WarehouseLive",
    sentence: `1 helper installed by kakapo: ${sentence}`,
    helpers: [
      {
        id: "xe-session",
        title: "kakapo's event session (blocks and deadlocks)",
        state: "running",
        sentence,
        details: [
          `Files: ${pattern}, ${s.maxFileMB} MB x ${s.maxFiles}`,
          `Writing now: ${s.currentFile}`,
          `Blocked process threshold: ${s.thresholdSeconds} s`,
          "Starts with SQL Server",
        ],
        fixId: removeXESessionFixId,
        fixLabel: "Remove",
        session: structuredClone(s),
      },
    ],
  }
}

function intOption(options: Record<string, string>, name: string, def: number, lo: number, hi: number, label: string) {
  const typed = options[name]
  if (typed === undefined) return { value: def, text: String(def) }
  const v = typed.trim()
  const n = Number(v)
  if (!/^\d+$/.test(v) || n < lo || n > hi) return { value: def, text: v, warning: `${label} must be a whole number from ${lo} to ${hi}.` }
  return { value: n, text: String(n) }
}

function installPreview(options: Record<string, string>): FixPreview {
  const th = intOption(options, "thresholdSeconds", 5, 1, 60, "Keep blocks longer than (seconds)")
  const mb = intOption(options, "fileMB", 50, 10, 500, "File size (MB)")
  const files = intOption(options, "files", 4, 2, 10, "Files kept")
  const disp = intOption(options, "dispatchSeconds", 30, 5, 60, "Write to the file within (seconds)")
  const folder = (options.folder ?? LOG).trim().replace(/[\\/]+$/, "")
  const folderWarning = flag("mock.helpersFolderMissing")
    ? `The folder ${folder} does not exist on SQL01\\SQL2022 as the SQL Server service sees it: choose one that does.`
    : !/^([A-Za-z]:\\|\\\\)/.test(folder)
      ? "The folder must be a full path on the server, for example D:\\SQLLogs."
      : undefined
  const blocked = [th.warning, folderWarning, mb.warning, files.warning, disp.warning].filter((w): w is string => !!w)
  const pattern = `${folder}\\kakapo*.xel`
  const preview: FixPreview = {
    database: "",
    engineVersion: "16.0.4135.4",
    changes: [],
    statement: "",
    rollback: "",
    warnings: [
      ...blocked,
      "This is a server setting: it changes every database on SQL01\\SQL2022.",
      `Every block over ${th.value} s is written to the file with both statements; on a server with constant blocking the files turn over faster (at most ${mb.value * files.value} MB on disk, never more).`,
      "The session costs almost nothing while nothing blocks; the blocked process report is produced by the lock monitor, which already runs.",
      "Deadlocks are also kept here, so the deadlock report no longer depends on system_health's retention.",
      "Needs ALTER ANY EVENT SESSION and ALTER SETTINGS: sysadmin.",
    ],
    inputs: [
      { name: "thresholdSeconds", label: "Keep blocks longer than (seconds)", value: th.text, kind: "text", warning: th.warning,
        help: "A block shorter than this is not recorded; 5 is what users notice. 1 to 60." },
      { name: "folder", label: "Folder", value: folder, kind: "text", warning: folderWarning, advanced: true,
        help: "Where the session's files go, as the SQL Server service sees it. By default the error log's folder, where every DBA looks." },
      { name: "fileMB", label: "File size (MB)", value: mb.text, kind: "text", warning: mb.warning, advanced: true,
        help: "Each file's size before the session starts the next one. 10 to 500." },
      { name: "files", label: "Files kept", value: files.text, kind: "text", warning: files.warning, advanced: true,
        help: "How many files the session keeps; the oldest is deleted when a new one starts. 2 to 10." },
      { name: "dispatchSeconds", label: "Write to the file within (seconds)", value: disp.text, kind: "text", warning: disp.warning, advanced: true,
        help: "How long events may wait in memory before they are written. 5 to 60." },
    ],
    options: { thresholdSeconds: th.text, folder, fileMB: mb.text, files: files.text, dispatchSeconds: disp.text },
  }
  if (blocked.length > 0) return preview
  preview.changes = [
    { setting: "Event session kakapo", current: "none", target: `blocked_process_report and xml_deadlock_report to ${pattern}, ${mb.value} MB x ${files.value}, starts with SQL Server` },
    { setting: "Blocked process threshold", current: "0 (off)", target: `${th.value} s` },
  ]
  preview.statement = [
    "SET XACT_ABORT ON;",
    `EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'blocked process threshold (s)', ${th.value}; RECONFIGURE;`,
    "CREATE EVENT SESSION [kakapo] ON SERVER",
    "    ADD EVENT sqlserver.blocked_process_report,",
    "    ADD EVENT sqlserver.xml_deadlock_report",
    `    ADD TARGET package0.event_file (SET filename = N'${folder}\\kakapo.xel', max_file_size = ${mb.value}, max_rollover_files = ${files.value})`,
    `    WITH (MAX_MEMORY = 4096 KB, EVENT_RETENTION_MODE = ALLOW_SINGLE_EVENT_LOSS, MAX_DISPATCH_LATENCY = ${disp.value} SECONDS,`,
    "          STARTUP_STATE = ON, TRACK_CAUSALITY = OFF);",
    "ALTER EVENT SESSION [kakapo] ON SERVER STATE = START;",
  ].join("\n")
  preview.rollback = removeStatement(0)
  return preview
}

function removeStatement(threshold: number): string {
  return [
    "IF EXISTS (SELECT 1 FROM sys.dm_xe_sessions WHERE name = N'kakapo') ALTER EVENT SESSION [kakapo] ON SERVER STATE = STOP;",
    "IF EXISTS (SELECT 1 FROM sys.server_event_sessions WHERE name = N'kakapo') DROP EVENT SESSION [kakapo] ON SERVER;",
    `EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'blocked process threshold (s)', ${threshold}; RECONFIGURE;`,
  ].join("\n")
}

function removePreview(s: XESession, options: Record<string, string>): FixPreview {
  const th = intOption(options, "thresholdSeconds", 0, 0, 86400, "Blocked process threshold after (seconds)")
  const pattern = s.fileName!.replace(/kakapo\.xel$/, "kakapo*.xel")
  const preview: FixPreview = {
    database: "",
    engineVersion: "16.0.4135.4",
    changes: [],
    statement: "",
    rollback: "",
    warnings: [
      ...(th.warning ? [th.warning] : []),
      `The .xel files stay on disk: ${s.currentFile}. Delete them by hand when you no longer need them (at most ${(s.maxFileMB ?? 0) * (s.maxFiles ?? 0)} MB).`,
      "The blocking history falls back to system_health's five-minute snapshots, so shorter blocks are no longer kept; the deadlock report keeps deadlocks only as long as system_health does.",
      "This is a server setting: it changes every database on SQL01\\SQL2022.",
      "Needs ALTER ANY EVENT SESSION and ALTER SETTINGS: sysadmin.",
    ],
    inputs: [
      { name: "thresholdSeconds", label: "Blocked process threshold after (seconds)", value: th.text, kind: "text", warning: th.warning, advanced: true,
        help: "0 turns the blocked process report off, as SQL Server ships. Keep the value when another tool's session uses it." },
    ],
    options: { thresholdSeconds: th.text },
  }
  if (th.warning) return preview
  preview.changes = [
    { setting: "Event session kakapo", current: `running, ${pattern}, ${s.maxFileMB} MB x ${s.maxFiles}`, target: "none (the files stay)" },
    { setting: "Blocked process threshold", current: `${s.thresholdSeconds} s`, target: th.value === 0 ? "0 (off)" : `${th.value} s` },
  ]
  preview.statement = removeStatement(th.value)
  preview.rollback = "-- creates kakapo's session again with its settings as read"
  return preview
}

function result(preview: FixPreview, before: string, after: string): FixResult {
  return { preview, before, after, verified: true, changeId: 990 + Math.floor(Math.random() * 9), appliedAt: new Date().toISOString() }
}

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

export const mockHelpers: FeatureMock = async (method, path, init) => {
  const row = path.match(/^\/api\/servers\/(\d+)\/helpers$/)
  if (row && method === "GET") {
    if (flag("mock.helpersFails")) throw new ApiError("SQL01\\SQL2022: login failed for user 'monitor'", 502)
    return helpers(Number(row[1]))
  }
  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/[^/]+\/fixes\/(install-xe-session|remove-xe-session)\/(preview|apply)$/)
  if (!fix) return undefined
  const serverId = Number(fix[1])
  const options = (init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }).options : undefined) ?? {}
  const current = installed.get(serverId)
  if (fix[2] === installXESessionFixId) {
    if (current) throw new ApiError("kakapo's event session already exists on SQL01\\SQL2022: the Installed helpers row on the instance page shows it", 404)
    const preview = installPreview(options)
    if (fix[3] === "preview") return preview
    if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
    await delay(1200)
    installed.set(serverId, session(Number(preview.options!.thresholdSeconds), preview.options!.folder, Number(preview.options!.fileMB), Number(preview.options!.files)))
    return result(preview, "none", `session kakapo running, blocked process threshold ${preview.options!.thresholdSeconds} s`)
  }
  if (!current) throw new ApiError("there is no event session named kakapo on SQL01\\SQL2022: nothing to remove", 404)
  const preview = removePreview(current, options)
  if (fix[3] === "preview") return preview
  if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
  await delay(800)
  installed.delete(serverId)
  return result(preview, preview.changes[0].current, `no session kakapo, blocked process threshold ${preview.options!.thresholdSeconds === "0" ? "0 (off)" : `${preview.options!.thresholdSeconds} s`}`)
}
