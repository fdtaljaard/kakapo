import { ApiError, type CardDetail, type Fix, type FixInput, type FixPreview, type FixResult } from "./api"
import type { FeatureMock } from "./api.mock"
import { archiveTableFixId, removeArchiveFixId, tableLabel, tableOption, type TableList, type TableListing } from "./api-tables"

// Phase 5a (docs/phase5a-contract.md): fixture answers for the table picker (GET .../tables) and the two
// archive fixes' previews and applies; undefined means "not mine". WarehouseLive's IntegrationLog is archived
// nightly already (the Tables row lists it). dbo.Transaction has foreign keys pointing at it, so its preview is
// blocked. withArchives puts the archive fix on the Storage card's IDX10 findings and the Tables row's entries
// on its fixes. Set localStorage "mock.tablesFails" to "1" for the picker's 502.

interface MockArchive {
  schema: string
  name: string
  keepMonths: number
  action: "archive" | "delete"
  target: string // archive.IntegrationLog
  schedule: string
  rowsKept: number
  rowsArchived: number
  lastRun: string
}

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
const n0 = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 })

const archives = new Map<string, MockArchive[]>([
  [
    "WarehouseLive",
    [
      {
        schema: "dbo",
        name: "IntegrationLog",
        keepMonths: 3,
        action: "archive",
        target: "archive.IntegrationLog",
        schedule: "02:30",
        rowsKept: 288_410,
        rowsArchived: 1_904_331,
        lastRun: `${new Date().toISOString().slice(0, 10)} 02:30 (succeeded, 4,120 rows moved)`,
      },
    ],
  ],
])

// The user tables of every mock database, biggest first: [name, rows, MB, date columns ("!" marks indexed)].
const tableRows: [string, number, number, string[]][] = [
  ["StockUnitAudit", 21_904_018, 4_152, ["AuditDate"]],
  ["Transaction", 39_120_644, 13_846, ["!CreatedOn", "PostedOn"]],
  ["StockUnit", 10_012_377, 6_428, ["!ReceivedOn"]],
  ["IntegrationLog", 7_412_880, 2_318, ["!LoggedAt"]],
  ["SalesOrderLine", 6_244_091, 3_278, []],
  ["JobHistory", 671_233, 166, ["!RunAt"]],
  ["MailLog", 214_960, 100, ["!SentAt", "ReadAt"]],
  ["ItemAlias", 402_118, 167, []],
]

function tablesFor(database: string): TableList {
  const archived = archives.get(database) ?? []
  const tables: TableListing[] = tableRows.map(([name, rows, mb, dates]) => ({
    schema: "dbo",
    name,
    rows,
    mb,
    dateColumns: dates.map((d) => ({ name: d.replace("!", ""), indexed: d.startsWith("!") })),
    archived: archived.some((a) => a.name === name),
  }))
  tables.sort((a, b) => b.mb - a.mb)
  return { database, tables }
}

function findTable(option: string): TableListing | undefined {
  const label = tableLabel(option)
  return tablesFor("").tables.find((t) => `${t.schema}.${t.name}` === label)
}

// The Storage card with the archive entries: IDX10's findings carry the archive fix, and each archived table
// gets its remove entry (the Tables row).
export function withArchives(detail: CardDetail, database: string): CardDetail {
  const findings = detail.findings.map((f) => (f.checkId === "IDX10" ? { ...f, fixId: archiveTableFixId } : f))
  const fixes: Fix[] = [...(detail.fixes ?? [])]
  for (const f of findings) {
    if (f.checkId !== "IDX10") continue
    const t = findTable(f.object)
    const indexed = t?.dateColumns.find((c) => c.indexed)
    fixes.push({
      id: archiveTableFixId,
      title: "Archive or delete old rows",
      scope: "database",
      checkId: "IDX10",
      setting: "Archive",
      current: "no archive job",
      target: "old rows archived nightly",
      needsRestart: false,
      options: indexed ? { table: f.object, dateColumn: indexed.name } : { table: f.object },
    })
  }
  for (const a of archives.get(database) ?? []) {
    fixes.push({
      id: removeArchiveFixId,
      title: "Stop archiving a table",
      scope: "database",
      checkId: "IDX10",
      setting: "Archive",
      current: `daily ${a.schedule}, ${a.keepMonths} months kept`,
      target: "job deleted",
      needsRestart: false,
      options: {
        table: tableOption(a.schema, a.name),
        keepMonths: String(a.keepMonths),
        destination: a.action === "archive" ? `${a.target} in ${database}` : "deleted, not kept",
        schedule: `daily ${a.schedule}`,
        lastRun: a.lastRun,
        rowsKept: String(a.rowsKept),
        ...(a.action === "archive" ? { rowsArchived: String(a.rowsArchived) } : {}),
      },
    })
  }
  return { ...detail, findings, fixes }
}

const keepHelp = "Rows older than this many months leave the table each night (1 to 120)."
const ownerSentence = (months: number, deletes: boolean) =>
  `This deletes application data${deletes ? " and keeps no copy" : ""}. Confirm with whoever owns the application that rows older than ${months} months are not needed (reports, integrations, audits by law).`

function wholeOption(options: Record<string, string>, name: string, def: number, lo: number, hi: number) {
  const typed = options[name]
  if (typed === undefined) return { value: def, text: String(def), ok: true }
  const n = Number(typed.trim())
  const ok = /^\d+$/.test(typed.trim()) && n >= lo && n <= hi
  return { value: ok ? n : def, text: ok ? String(n) : typed.trim(), ok }
}

function archivePreview(database: string, options: Record<string, string> = {}): FixPreview {
  const t = findTable(options.table ?? "")
  if (!t) throw new ApiError(`there is no user table ${tableLabel(options.table ?? "")} in ${database}`, 404)
  const label = `${t.schema}.${t.name}`
  const dates = [...t.dateColumns].sort((a, b) => Number(b.indexed) - Number(a.indexed))
  const bad: string[] = []
  const inputs: FixInput[] = [
    { name: "table", label: "Table", value: tableOption(t.schema, t.name), help: "", kind: "text", readOnly: true },
  ]
  const echo: Record<string, string> = { table: tableOption(t.schema, t.name) }
  let date = dates[0]
  if (dates.length > 0) {
    const typed = options.dateColumn?.replace(" (indexed)", "")
    const found = typed === undefined ? date : dates.find((d) => d.name === typed)
    const input: FixInput = {
      name: "dateColumn",
      label: "Date column",
      help: "The column that says how old a row is. Indexed columns are listed first and marked.",
      kind: "select",
      options: dates.map((d) => (d.indexed ? `${d.name} (indexed)` : d.name)),
      value: "",
    }
    if (found) {
      date = found
      input.value = found.indexed ? `${found.name} (indexed)` : found.name
    } else {
      input.value = options.dateColumn ?? ""
      input.warning = "Pick one of the table's date columns."
      bad.push(input.warning)
    }
    inputs.push(input)
    echo.dateColumn = date.name
  }
  const keep = wholeOption(options, "keepMonths", 12, 1, 120)
  const keepInput: FixInput = { name: "keepMonths", label: "Months to keep", value: keep.text, help: keepHelp, kind: "text" }
  if (!keep.ok) bad.push((keepInput.warning = "Keep between 1 and 120 months."))
  inputs.push(keepInput)
  const deletes = options.action === "delete"
  inputs.push({
    name: "action",
    label: "Archive or delete",
    value: deletes ? "delete" : "archive",
    help: "archive copies each row into the archive table and deletes it in the same statement; delete removes it and keeps no copy.",
    kind: "select",
    options: ["archive", "delete"],
    ...(deletes ? { warning: ownerSentence(keep.value, true) } : {}),
  })
  const archiveName = options.archiveName ?? "schema"
  inputs.push({
    name: "archiveName",
    label: "Archive table name",
    value: archiveName,
    help: "schema puts it in an archive schema under the same name (archive.MailLog), suffix adds _Archive, prefix puts Archive_ in front.",
    kind: "select",
    options: ["schema", "suffix", "prefix"],
    advanced: true,
  })
  const archiveTo = options.archiveTo ?? "same database"
  const toInput: FixInput = {
    name: "archiveTo",
    label: "Archive to",
    value: archiveTo,
    help: "Where the archive table lives. A separate archive database keeps the live database small.",
    kind: "select",
    options: ["same database", `${database}_Archive`],
    advanced: true,
  }
  if (archiveTo !== "same database" && !deletes) {
    bad.push(
      (toInput.warning = `There is no database named ${database}_Archive on this server: choose the same database, or create the archive database first.`),
    )
  }
  inputs.push(toInput)
  const condition = (options.condition ?? "").trim()
  const condInput: FixInput = {
    name: "condition",
    label: "Only rows where (optional)",
    value: condition,
    help: "Optional: only the old rows that also match this, for example Status = 'Closed'.",
    kind: "text",
    advanced: true,
  }
  if (/;|--|\/\*|@|\bselect\b|\bexec\b/i.test(condition)) {
    bad.push((condInput.warning = "The condition: it may only compare the table's own columns."))
  }
  inputs.push(condInput)
  const schedule = (options.schedule ?? "02:30").trim()
  const schedInput: FixInput = { name: "schedule", label: "Daily at", value: schedule, help: "Daily, server time.", kind: "text", advanced: true }
  if (!/^([01]?\d|2[0-3]):[0-5]\d$/.test(schedule)) bad.push((schedInput.warning = "The time must be HH:MM, for example 02:30."))
  inputs.push(schedInput)
  const batch = wholeOption(options, "batchRows", 5000, 500, 50000)
  const batchInput: FixInput = { name: "batchRows", label: "Rows per statement", value: batch.text, help: "500 to 50,000.", kind: "text", advanced: true }
  if (!batch.ok) bad.push((batchInput.warning = "Type a whole number of rows from 500 to 50000."))
  inputs.push(batchInput)
  const minutes = wholeOption(options, "maxMinutes", 30, 5, 240)
  const minutesInput: FixInput = { name: "maxMinutes", label: "Minutes a night", value: minutes.text, help: "5 to 240.", kind: "text", advanced: true }
  if (!minutes.ok) bad.push((minutesInput.warning = "Type a whole number of minutes from 5 to 240."))
  inputs.push(minutesInput)
  const tidy = options.tidyUp === "true"
  inputs.push({
    name: "tidyUp",
    label: "Tidy up the indexes and statistics afterwards",
    value: String(tidy),
    help: "Adds a second step that reorganises the table's indexes and updates its statistics.",
    kind: "bool",
    advanced: true,
  })
  const firstRun = options.firstRun === "move one batch now" ? "move one batch now" : "nothing"
  inputs.push({
    name: "firstRun",
    label: "When the job is created",
    value: firstRun,
    help: "nothing: the first batch runs tonight. move one batch now: one batch runs at once.",
    kind: "select",
    options: ["nothing", "move one batch now"],
    advanced: true,
  })
  for (const i of inputs) if (i.name !== "dateColumn" && i.name !== "table") echo[i.name] = i.value

  const blocks: string[] = []
  if (t.name === "Transaction") {
    blocks.push(
      "Other rows point at dbo.Transaction through foreign keys (a column there must match a row here): dbo.TransactionLine (FK_TransactionLine_Transaction). Rows that are pointed at cannot be deleted, and archiving a parent table is a design question for the application's owner: kakapo does not archive it.",
    )
  }
  if (!date) blocks.push(`${label} has no date column (datetime, datetime2 or date), so kakapo cannot tell how old a row is.`)
  if (archives.get(database)?.some((a) => a.name === t.name)) {
    blocks.push(`kakapo already archives ${label} (the job kakapo: archive ${database}.${label}): stop it first ("Stop archiving a table") to change how.`)
  }
  blocks.push(...bad)

  const target = archiveName === "suffix" ? `dbo.${t.name}_Archive` : archiveName === "prefix" ? `dbo.Archive_${t.name}` : `archive.${t.name}`
  const due = Math.round(t.rows * 0.62)
  const perNight = (minutes.value * 60) / (date?.indexed ? 2 : 10) * batch.value
  const nights = Math.max(1, Math.ceil(due / perNight))
  const warnings = [
    ...blocks,
    ...(date && !date.indexed
      ? [`${date.name} is not the first column of an index, so each batch scans the table: create an index on ${date.name} first ("Create the suggested index" when the Performance card suggests one, or by hand).`]
      : []),
    ...(deletes ? [] : ["The identity column Id keeps its values in the archive (IDENTITY_INSERT), so an archived row stays traceable by its id."]),
    ...(deletes
      ? []
      : [`The archive copy needs about ${n0.format(t.mb * 0.62)} MB in ${database} (182,340 MB free on its drive). The space the deletes free in ${database} is reused by new rows, not given back to the disk.`]),
    `${database} is in FULL recovery: each night's batches write about ${n0.format((Math.min(due, perNight) / t.rows) * t.mb * (deletes ? 1 : 2))} MB to the log, which only a log backup frees. The last log backup was at ${new Date().toISOString().slice(0, 10)} 09:45; the log backup job must keep running (BAK02).`,
    "Needs sysadmin on WMS-SQL01 (creating Agent jobs owned by sa).",
    ownerSentence(keep.value, deletes),
  ]
  const preview: FixPreview = {
    database,
    engineVersion: "16.0.4255.2",
    changes: [],
    statement: "",
    rollback: "",
    warnings,
    inputs,
    options: echo,
  }
  if (blocks.length > 0) return preview
  const job = `kakapo: archive ${database}.${label}`
  preview.changes = [
    {
      setting: "Table",
      current: `${n0.format(t.rows)} rows, ${n0.format(t.mb)} MB, ${date.indexed ? "oldest 2021-03-02" : `oldest not read (${date.name} is not indexed)`}`,
      target: `rows older than ${keep.value} months${condition ? ` where ${condition}` : ""} leave nightly`,
    },
    { setting: "Destination", current: "none", target: deletes ? "deleted, not kept" : `${target} in ${database}` },
    {
      setting: "Rows affected now",
      current: "",
      target: `about ${n0.format(due)} (${date.indexed ? "counted" : "estimated from a 1% sample"}), ${nights === 1 ? "1 night" : `${n0.format(nights)} nights`} at ${n0.format(batch.value)} rows per statement and ${minutes.value} minutes a night`,
    },
    { setting: "Job", current: "none", target: `${job}, daily ${schedule}${firstRun === "move one batch now" ? ", one batch now" : ""}` },
  ]
  const src = `[${database}].[dbo].[${t.name}]`
  const dst = `[${database}].[${target.split(".")[0]}].[${target.split(".")[1]}]`
  const where = `[${date.name}] < @cutoff${condition ? ` AND (${condition})` : ""}`
  const body = deletes
    ? `    WITH batch AS (SELECT TOP (${batch.value}) * FROM ${src} WITH (READPAST, UPDLOCK, ROWLOCK) WHERE ${where})\n    DELETE FROM batch;\n`
    : `    SET IDENTITY_INSERT ${dst} ON;\n    WITH batch AS (SELECT TOP (${batch.value}) * FROM ${src} WITH (READPAST, UPDLOCK, ROWLOCK) WHERE ${where})\n    DELETE FROM batch OUTPUT deleted.[Id], deleted.[${date.name}], deleted.[Message] INTO ${dst} ([Id], [${date.name}], [Message]);\n`
  const step = [
    "/* kakapo */",
    `/* kakapo-archive {"database":"${database}","schema":"dbo","table":"${t.name}","keepMonths":${keep.value},"action":"${deletes ? "delete" : "archive"}"} */`,
    "SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW;",
    `DECLARE @cutoff datetime2 = DATEADD(MONTH, -${keep.value}, SYSDATETIME()), @stop datetime2 = DATEADD(MINUTE, ${minutes.value}, SYSDATETIME()), @moved bigint = 0, @n int;`,
    "WHILE SYSDATETIME() < @stop",
    "BEGIN",
    "    BEGIN TRANSACTION;",
    body.trimEnd(),
    "    SET @n = @@ROWCOUNT;",
    ...(deletes ? [] : [`    SET IDENTITY_INSERT ${dst} OFF;`]),
    "    COMMIT;",
    "    SET @moved += @n;",
    "    IF @n = 0 BREAK;",
    "    WAITFOR DELAY '00:00:01';",
    "END;",
    "PRINT 'rows moved: ' + CONVERT(varchar(20), @moved);",
  ].join("\n")
  preview.statement = [
    "DECLARE @owner sysname = SUSER_SNAME(0x01);",
    "BEGIN TRY",
    "    BEGIN TRANSACTION;",
    ...(deletes ? [] : [`    SELECT TOP (0) [Id], [${date.name}], [Message] INTO ${dst} FROM ${src};`]),
    `    EXEC msdb.dbo.sp_add_job @job_name = N'${job}', @enabled = 1, @owner_login_name = @owner;`,
    `    EXEC msdb.dbo.sp_add_jobstep @job_name = N'${job}', @step_id = 1, @step_name = N'${deletes ? "Delete" : "Archive"} old rows', @command = N'${step.replaceAll("'", "''")}';`,
    "    COMMIT TRANSACTION;",
    "END TRY",
    "BEGIN CATCH",
    "    IF @@TRANCOUNT > 0 ROLLBACK TRANSACTION;",
    "    THROW;",
    "END CATCH;",
  ].join("\n")
  preview.rollback = `${deletes ? "" : `-- The archive table ${target} in ${database} and its rows stay: drop it by hand when nobody needs them.\n`}EXEC msdb.dbo.sp_delete_job @job_name = N'${job}';`
  return preview
}

function removePreview(database: string, options: Record<string, string> = {}): FixPreview {
  const label = tableLabel(options.table ?? "")
  const a = archives.get(database)?.find((x) => `${x.schema}.${x.name}` === label)
  if (!a) throw new ApiError(`nothing to remove: kakapo does not archive ${label} in ${database}`, 404)
  const job = `kakapo: archive ${database}.${label}`
  return {
    database,
    engineVersion: "16.0.4255.2",
    changes: [
      { setting: "Job", current: `${job}, daily ${a.schedule}, rows older than ${a.keepMonths} months to ${a.target} in ${database}`, target: "deleted" },
      { setting: "Last run", current: a.lastRun, target: "no more runs" },
      { setting: "Archive table", current: `${a.target} in ${database}, ${n0.format(a.rowsArchived)} rows`, target: "kept" },
    ],
    statement: `EXEC msdb.dbo.sp_delete_job @job_name = N'${job}';`,
    rollback: `DECLARE @owner sysname = SUSER_SNAME(0x01);\n-- the job ${job} as it was created, rebuilt from its options line`,
    warnings: [
      `The archive table ${a.target} and its ${n0.format(a.rowsArchived)} rows stay; drop it by hand when nobody needs them.`,
      `Rows already moved stay where they are; ${label} simply stops losing old rows.`,
      "Needs sysadmin on WMS-SQL01 (deleting Agent jobs owned by sa).",
    ],
    options: { table: options.table ?? "" },
  }
}

function result(preview: FixPreview, before: string, after: string): FixResult {
  return {
    preview,
    before,
    after,
    verified: true,
    changeId: 900 + Math.floor(Math.random() * 99),
    appliedAt: new Date().toISOString(),
  }
}

export const mockTables: FeatureMock = async (method, path, init) => {
  const list = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/tables$/)
  if (list && method === "GET") {
    await delay(800)
    if (localStorage.getItem("mock.tablesFails") === "1") {
      throw new ApiError("could not list the tables of the database on WMS-SQL01: VIEW DEFINITION permission was denied", 502)
    }
    return tablesFor(decodeURIComponent(list[2]))
  }

  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/(archive-table|remove-archive)\/(preview|apply)$/)
  if (!fix || method !== "POST") return undefined
  const database = decodeURIComponent(fix[2])
  const options = init?.body ? ((JSON.parse(String(init.body)) as { options?: Record<string, string> }).options ?? {}) : {}
  if (fix[3] === archiveTableFixId) {
    const preview = archivePreview(database, options)
    if (fix[4] === "preview") return preview
    if (preview.changes.length === 0) throw new ApiError(preview.warnings[0], 409)
    await delay(1500)
    const t = findTable(options.table ?? "")!
    const o = preview.options ?? {}
    const deletes = o.action === "delete"
    const target = o.archiveName === "suffix" ? `dbo.${t.name}_Archive` : o.archiveName === "prefix" ? `dbo.Archive_${t.name}` : `archive.${t.name}`
    const first = o.firstRun === "move one batch now"
    archives.set(database, [
      ...(archives.get(database) ?? []),
      {
        schema: t.schema,
        name: t.name,
        keepMonths: Number(o.keepMonths),
        action: deletes ? "delete" : "archive",
        target,
        schedule: o.schedule ?? "02:30",
        rowsKept: t.rows - (first ? Number(o.batchRows) : 0),
        rowsArchived: first && !deletes ? Number(o.batchRows) : 0,
        lastRun: "not yet",
      },
    ])
    const job = `kakapo: archive ${database}.${t.schema}.${t.name}`
    const after = `${job}: enabled, next run not set by Agent yet${deletes ? "" : `; ${target} in ${database} exists`}${first ? `; the first batch moved ${n0.format(Number(o.batchRows))} rows` : ""}.`
    return result(preview, "no archive job", after)
  }
  const preview = removePreview(database, options)
  if (fix[4] === "preview") return preview
  await delay(1000)
  const label = tableLabel(options.table ?? "")
  archives.set(database, (archives.get(database) ?? []).filter((a) => `${a.schema}.${a.name}` !== label))
  const job = `kakapo: archive ${database}.${label}`
  return result(preview, job, `the job ${job} is gone`)
}
