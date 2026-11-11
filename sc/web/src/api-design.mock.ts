import { ApiError, type Card, type CardDetail, type Finding, type Fix, type FixPreview, type FixResult } from "./api"
import type { FeatureMock } from "./api.mock"

// Phase 5n (docs/phase5n-contract.md): the Design card's fixture; phase 5o adds IDX04 and disable-index.
// WarehouseLive has deleted from Orders since the restart, so IDX14 fires on Lines with its create-index fix;
// two other keys sit in the fold. The other
// databases were restored three days ago: too early to tell, every key in the fold. The create-index preview
// of a foreign key (options.source "foreign-key") answers here with the preview's first line; the plan's
// suggestions stay with api-sample.mock.ts.

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

const IDX14_WHY =
  "Every delete from the parent table, and every change to its key, checks this foreign key. Without an index the check reads the whole referencing table and holds its locks while it does (Microsoft Learn, 'Primary and foreign key constraints')."
const IDX14_THEN =
  "Re-run: IDX14 gone for that key; deletes on the parent table faster (the tuning report, when the deletes are in Query Store)."

const now = () => new Date().toISOString()

function designCard(over: Partial<Card>): Card {
  return {
    id: "design",
    title: "Database design",
    question: "Does the way it is built cost you anything?",
    status: "green",
    statusText: "No problem found",
    verdict: "",
    facts: [],
    toDo: 0,
    forClientIT: 0,
    fixable: 0,
    asOf: now(),
    note: "",
    ...over,
  }
}

const foldKeys = [
  { name: "FK_Notes_Customers", table: "Notes", rows: 412_980, parent: "Customers", columns: ["CustomerId"] },
  { name: "FK_Moves_Batches", table: "Moves", rows: 88_120, parent: "Batches", columns: ["BatchNo", "Site"] },
].map((k) => ({ ...k, text: `${k.table} (${k.rows.toLocaleString("en-US")} rows): no index on ${k.columns.join(", ")}, its key to ${k.parent}` }))

// The Design card's detail for a database, as GET .../cards/design answers it.
export function designDetail(database: string): CardDetail {
  if (database !== "WarehouseLive") {
    const keys = [
      { name: "FK_Lines_Orders", table: "Lines", rows: 1_376_845, parent: "Orders", columns: ["OrderId"], text: "" },
      ...foldKeys,
    ].map((k) => ({ ...k, text: `${k.table} (${k.rows.toLocaleString("en-US")} rows): no index on ${k.columns.join(", ")}, its key to ${k.parent}` }))
    return {
      card: designCard({
        // Not judged yet: grey, as the backend says it (DESIGN.md, The One Meaning Rule).
        status: "grey",
        statusText: "Too early to tell",
        verdict:
          "Too early to tell which foreign keys cost work: deletes counted for 3 days, since the restore on 5 Nov. 3 foreign keys on large tables have no index.",
        facts: [
          { label: "Foreign keys", value: "39, 5 without an index", tone: "normal" },
          { label: "Costing work now", value: "0", tone: "normal" },
          { label: "Duplicate indexes", value: "none", tone: "normal" },
          { label: "No primary key", value: "none", tone: "normal" },
          { label: "Counted since", value: "the restore on 5 Nov", tone: "normal" },
          { label: "Tables", value: "95", tone: "normal" },
        ],
      }),
      findings: [],
      checks: [
        { checkId: "IDX14", title: "Foreign keys without an index", status: "passed", note: "" },
        { checkId: "IDX04", title: "Duplicate indexes", status: "passed", note: "" },
        { checkId: "DES01", title: "Tables without a primary key", status: "passed", note: "" },
      ],
      foreignKeys: { heading: "Too early to tell", tooEarly: true, window: "the restore on 5 Nov", keys },
    }
  }
  const finding: Finding = {
    checkId: "IDX14",
    title: "Foreign keys without an index",
    severity: "Medium",
    owner: "Us",
    when: "Online",
    effort: "Quick win",
    object: "[dbo].[Lines]",
    finding:
      "Deleting from Orders (812 rows since 1 Oct) makes SQL Server read the whole of Lines (1,376,845 rows) to check foreign key FK_Lines_Orders: no index on Lines starts with OrderId.",
    why: IDX14_WHY,
    doThis:
      "Create an index on Lines (OrderId). It does not change how the application works; tell the vendor, so their next upgrade keeps it or adds its own.",
    thenCheck: IDX14_THEN,
    evidence:
      "Foreign key FK_Lines_Orders: [dbo].[Lines] ([OrderId]) references [dbo].[Orders]; ON DELETE NO_ACTION, ON UPDATE NO_ACTION. Lines has 1,376,845 rows; 812 rows deleted from Orders since the restart on 1 Oct (2026-10-01 06:00). Deletes are counted since the restart on 1 Oct and may be more: SQL Server forgets them when it unloads the table's metadata.",
    fixId: "create-index",
  }
  const fix: Fix = {
    id: "create-index",
    title: "Create the suggested index",
    scope: "database",
    checkId: "IDX14",
    setting: "Index",
    current: "none",
    target: "index on Lines (OrderId)",
    needsRestart: false,
    options: {
      source: "foreign-key",
      table: `[${database}].[dbo].[Lines]`,
      equality: "[OrderId]",
      inequality: "",
      included: "",
      parent: "[dbo].[Orders]",
      foreignKey: "FK_Lines_Orders",
      object: "[dbo].[Lines]",
    },
  }
  return {
    card: designCard({
      status: "amber",
      statusText: "5 things to do",
      verdict:
        "1 foreign key makes deletes read a whole table: Lines (1.4 million rows). 2 indexes copy another one: 1.3 GB and 4.3 million writes since 1 Oct for nothing. 2 indexes have the same keys as another but hold different columns: merging them is a job for someone experienced with indexes. 2 tables have no primary key, the biggest StockMoves (1.7 million rows).",
      facts: [
        { label: "Foreign keys", value: "39, 5 without an index", tone: "normal" },
        { label: "Costing work now", value: "1", tone: "bad" },
        { label: "Duplicate indexes", value: "2, 1.3 GB", tone: "bad" },
        { label: "No primary key", value: "3 tables (2 large)", tone: "bad" },
        { label: "Counted since", value: "the restart on 1 Oct", tone: "normal" },
        { label: "Tables", value: "95", tone: "normal" },
      ],
      toDo: 5,
      fixable: 4,
    }),
    findings: [finding, ...duplicateFindings, ...noKeyFindings],
    checks: [
      { checkId: "IDX14", title: "Foreign keys without an index", status: "finding", note: "" },
      { checkId: "IDX04", title: "Duplicate indexes", status: "finding", note: "" },
      { checkId: "DES01", title: "Tables without a primary key", status: "finding", note: "" },
    ],
    fixes: [fix, disableFix("IX_B", "IX_A", "1.2 GB"), disableFix("IX_E", "IX_A", "85 MB"), keyFix("StockMoves"), keyFix("Notes_Import")],
    foreignKeys: { heading: "Missing indexes on foreign keys, not needed yet", tooEarly: false, window: "the restart on 1 Oct", keys: foldKeys },
    noKeyTables: { heading: "Smaller tables without a primary key", tables: [{ table: "Codes", rows: 2410, text: "Codes (2,410 rows, heap)" }] },
  }
}

// Phase 5o (docs/phase5o-contract.md): IDX04 on WarehouseLive. Orders has two safe copies of IX_A (one Apply
// each); Notes has two copies to merge (Copy only).
const IDX04_WHY =
  "Indexes with the same keys do the same job: SQL Server keeps every copy up to date on each insert, update and delete, and stores, backs up and maintains each one."
const IDX04_THEN = "Re-run: IDX04 gone for that table; the keeper's reads went up by what the copy had."

const duplicateFindings: Finding[] = [
  {
    checkId: "IDX04",
    title: "Duplicate indexes",
    severity: "Medium",
    owner: "Us",
    when: "Online",
    effort: "Quick win",
    object: "[dbo].[Orders]",
    finding:
      "IX_B is a copy of IX_A on Orders (CustomerId, OrderDate): 1.2 GB and 4.3 million writes since 1 Oct, for nothing. IX_A holds every column of IX_B. IX_E is a copy of IX_A on Orders (CustomerId, OrderDate): 85 MB and 2,140 writes since 1 Oct, for nothing. IX_A holds every column of IX_E.",
    why: IDX04_WHY,
    doThis:
      "Disable IX_B and IX_E. Queries that used them use the copy named for each, which has the same keys and every column. Tell the vendor, so their next upgrade does not create them again.",
    thenCheck: IDX04_THEN,
    evidence:
      "Safe to disable: [IX_B] (kept in [IX_A]), [IX_E] (kept in [IX_A]). [IX_A] (kept, most reads): keys CustomerId, OrderDate; includes Total; no filter; 1.3 GB; 91,204 reads and 4,302,140 writes since the restart on 1 Oct. [IX_B] (safe to disable, kept in [IX_A]): keys CustomerId, OrderDate; includes nothing; no filter; 1.2 GB; 12 reads and 4,300,000 writes since the restart on 1 Oct. [IX_E] (safe to disable, kept in [IX_A]): keys CustomerId, OrderDate; includes nothing; no filter; 85 MB; 0 reads and 2,140 writes since the restart on 1 Oct.",
    fixId: "disable-index",
  },
  {
    checkId: "IDX04",
    title: "Duplicate indexes",
    severity: "Low",
    owner: "Us",
    when: "Online",
    effort: "Moderate",
    object: "[dbo].[Notes]",
    finding:
      "IX_C and IX_D on Notes have the same keys (CustomerId) but each holds columns the other does not. One index with all their columns would replace them.",
    why: IDX04_WHY,
    doThis: "Copy for someone experienced with indexes, or for the vendor.",
    thenCheck: IDX04_THEN,
    evidence:
      "Needs merging: [IX_C], [IX_D]. [IX_C] (needs merging): keys CustomerId; includes Status; no filter; 40 MB; 310 reads and 52,000 writes since the restart on 1 Oct. [IX_D] (needs merging): keys CustomerId; includes Total; no filter; 38 MB; 95 reads and 52,000 writes since the restart on 1 Oct.",
  },
]

function disableFix(index: string, keeper: string, size: string): Fix {
  return {
    id: "disable-index",
    title: "Disable a duplicate index",
    scope: "database",
    checkId: "IDX04",
    setting: "Index",
    current: `${index} on dbo.Orders (CustomerId, OrderDate), ${size}`,
    target: "disabled",
    needsRestart: false,
    options: { table: "[dbo].[Orders]", index, keeper, object: "[dbo].[Orders]" },
  }
}

// The disable-index preview: IX_E is named in a procedure (Apply held back); the others are shown.
function disablePreview(database: string, options: Record<string, string>): FixPreview {
  const index = options.index || "IX_B"
  const keeper = options.keeper || "IX_A"
  const bracketed = options.table || "[dbo].[Orders]"
  const table = bracketed.split("].[").join(".").split("[").join("").split("]").join("")
  const size = index === "IX_E" ? "85 MB" : index === "IX_Audit_Unit_Copy" ? "640 MB" : "1.2 GB"
  return {
    database,
    engineVersion: "16.0.4135.4",
    lead: `${index} is a copy of ${keeper}: ${size} and 4.3 million writes since 1 Oct, for nothing.`,
    held:
      index === "IX_E"
        ? "IX_E is named in dbo.GetOpenOrders: a query that names a disabled index fails (error 315). Change the code first, or ask the vendor."
        : undefined,
    changes: [{ setting: "Index", current: `${index} on ${table}, ${size}`, target: "disabled" }],
    statement: `ALTER INDEX [${index}] ON [${database}].${bracketed} DISABLE;`,
    rollback: `ALTER INDEX [${index}] ON [${database}].${bracketed} REBUILD WITH (ONLINE = ON);`,
    warnings: [
      `Queries that read ${index} (12 times since 1 Oct) will read ${keeper} instead.`,
      "Disabling needs a short lock on the table.",
      "Undoing it rebuilds the index from the table: it takes as long as creating it, and the space comes back.",
      "The rollback rebuilds it online (ONLINE = ON): users keep working, but it still needs a short lock at the start and at the end.",
      `The ${size} freed stays inside the data file: the file does not shrink; new rows reuse the space.`,
      "kakapo searched the database's code, plan guides and Query Store for the name; code outside SQL Server (an application that names the index in a hint) cannot be searched.",
      `Needs the ALTER permission on ${table} (db_ddladmin or db_owner in the database).`,
    ],
    options,
  }
}

// The create-index preview of a foreign key: the first line, the statement on the key's columns.
function foreignKeyPreview(database: string, options: Record<string, string>): FixPreview {
  const name = options.name || "IX_Lines_OrderId_kakapo"
  return {
    database,
    engineVersion: "16.0.4135.4",
    lead: "Deletes from Orders read all 1,376,845 rows of Lines today; with this index they read only the matching rows.",
    changes: [{ setting: "Index", current: "none", target: `${name} on dbo.Lines (OrderId), about 25 MB, online` }],
    statement: `CREATE NONCLUSTERED INDEX [${name}] ON [${database}].[dbo].[Lines] ([OrderId]) WITH (ONLINE = ON, SORT_IN_TEMPDB = ON, DATA_COMPRESSION = PAGE);`,
    rollback: `DROP INDEX [${name}] ON [${database}].[dbo].[Lines];`,
    warnings: [
      "Built online: users keep working, but it still needs a short lock at the start and at the end.",
      "Test on the sandbox first when the table is large: the build time and the effect on the queries show there.",
      "If the table is busy, kakapo gives up after 10 seconds of waiting for its lock rather than queue in front of users; then try again in a quieter moment.",
      "Each index slows down every insert, update and delete on the table a little; drop it again (the change log holds the DROP INDEX) if it does not help.",
      "Needs the ALTER permission on dbo.Lines (db_ddladmin or db_owner in the database).",
    ],
    inputs: [
      { name: "name", label: "Index name", value: name, kind: "text", help: "kakapo's indexes end in _kakapo, so they are easy to find later." },
      {
        name: "included",
        label: "Included columns",
        value: "",
        kind: "text",
        help: "As SQL Server suggested them. Leave out the columns the query does not need; empty for none.",
      },
    ],
    options: { ...options, name, included: "" },
  }
}

// Phase 5s (docs/phase5s-contract.md): DES01 on WarehouseLive. StockMoves is a heap with an identity column
// (Apply on it); Notes_Import has nothing to key on, so its key is a new column, held back until the user
// ticks the confirm, then held by dbo.ImportNotes (it inserts without a column list). Set localStorage
// "mock.noInsertProc" to "1" to see the confirmed preview apply.
const DES01_WHY =
  "A primary key makes SQL Server refuse a second row with the same key, and replication, change tracking and many tools need one (Microsoft Learn, 'Primary and foreign key constraints')."

const noKeyFindings: Finding[] = [
  {
    checkId: "DES01",
    title: "Tables without a primary key",
    severity: "Medium",
    owner: "Vendor",
    when: "Window",
    effort: "Moderate",
    object: "[dbo].[StockMoves]",
    finding:
      "StockMoves has no primary key: nothing stops two identical rows among its 1,732,636 rows, and no key names a row. It is a heap too (no clustered index).",
    why: DES01_WHY,
    doThis:
      "Add a primary key on its identity column MoveId, clustered, which also ends the heap. Tell the vendor first: their next upgrade must expect it.",
    thenCheck: "Re-run: DES01 gone for that table.",
    evidence:
      "Table [dbo].[StockMoves]: 1,732,636 rows, 204 MB, heap; no unique index on NOT NULL columns; identity column [MoveId]; 5 nonclustered indexes (rebuilt with a clustered key).",
    fixId: "add-primary-key",
  },
  {
    checkId: "DES01",
    title: "Tables without a primary key",
    severity: "Low",
    owner: "Vendor",
    when: "Window",
    effort: "Moderate",
    object: "[dbo].[Notes_Import]",
    finding: "Notes_Import has no primary key: nothing stops two identical rows among its 48,200 rows, and no key names a row. It is a heap too (no clustered index).",
    why: DES01_WHY,
    doThis:
      "Add a primary key on a new bigint identity column, clustered, which also ends the heap. This writes every row and adds a column, so check with the vendor first that the application names its columns when it inserts into Notes_Import.",
    thenCheck: "Re-run: DES01 gone for that table.",
    evidence: "Table [dbo].[Notes_Import]: 48,200 rows, 6.1 MB, heap; no unique index on NOT NULL columns; no identity column.",
    fixId: "add-primary-key",
  },
]

function keyFix(table: string): Fix {
  return {
    id: "add-primary-key",
    title: "Add a primary key",
    scope: "database",
    checkId: "DES01",
    setting: "Primary key",
    current: "none",
    target: `primary key on ${table}`,
    needsRestart: false,
    options: { table: `[dbo].[${table}]`, object: `[dbo].[${table}]` },
  }
}

// The add-primary-key preview on Express: StockMoves on its identity column, Notes_Import on a new column.
function keyPreview(database: string, options: Record<string, string>): FixPreview {
  const notes = (options.table ?? "").includes("Notes_Import")
  const table = notes ? "Notes_Import" : "StockMoves"
  const choices = notes ? ["New column (bigint identity)"] : ["Identity column MoveId", "New column (bigint identity)"]
  const key = options.key && choices.includes(options.key) ? options.key : choices[0]
  const newColumn = key.startsWith("New column")
  const column = options.column || "kakapo_id"
  const name = options.name || `PK_${table}`
  const clustered = options.clustered === "nonclustered" ? "nonclustered" : "clustered"
  const confirmed = options.confirm === "true"
  const cols = newColumn ? column : "MoveId"
  const three = `[${database}].[dbo].[${table}]`
  const kind = clustered.toUpperCase()
  const size = notes ? "6.1 MB" : "204 MB"
  const statement = newColumn
    ? `ALTER TABLE ${three} ADD [${column}] bigint IDENTITY(1, 1) NOT NULL CONSTRAINT [${name}] PRIMARY KEY ${kind};`
    : `ALTER TABLE ${three} ADD CONSTRAINT [${name}] PRIMARY KEY ${kind} ([${cols}]) WITH (ONLINE = OFF, SORT_IN_TEMPDB = ON);`
  const rollback = newColumn
    ? `ALTER TABLE ${three} DROP CONSTRAINT [${name}]; ALTER TABLE ${three} DROP COLUMN [${column}];`
    : `ALTER TABLE ${three} DROP CONSTRAINT [${name}];`
  const target = `${name} on dbo.${table} (${cols}${newColumn ? ", a new bigint identity column" : ""}), ${clustered}`
  const confirmLabel = `The application names its columns when it inserts into ${table}`
  const held = !newColumn
    ? undefined
    : localStorage.getItem("mock.noInsertProc") !== "1" && confirmed
      ? `dbo.ImportNotes inserts into ${table} without a column list (or with SELECT *): a new column makes it fail (error 213). Change the code first, or ask the vendor.`
      : !confirmed
        ? `Tick "${confirmLabel}" first: a new column makes an INSERT without a column list fail.`
        : undefined
  const rewrite =
    clustered === "clustered"
      ? `${newColumn ? "It writes every row to fill the new column. " : ""}This rewrites the whole of ${table} (${size}) as a clustered index${notes ? "" : " and rebuilds its 5 nonclustered indexes, which then carry the key instead of the row's address"}.`
      : `${newColumn ? "It writes every row to fill the new column. " : ""}This builds one new index of about ${notes ? "1.2 MB" : "41 MB"}.`
  const warnings = [
    rewrite,
    `${newColumn ? "Adding a column is always offline" : "On Express edition it runs offline"}: ${table} can be neither read nor written until it finishes, and users who touch it wait. Apply it in a quiet moment.`,
    "If the table is busy, kakapo gives up after 10 seconds of waiting for its lock rather than queue in front of users; then try again in a quieter moment.",
    "On Express a database holds at most 50 GB of data: the table's new copy counts towards it while it is built.",
    ...(notes ? [] : ["Test on the sandbox first when the table is large: the build time and the effect on the queries show there."]),
    ...(newColumn
      ? [
          `A new column changes the table's shape: an INSERT into ${table} without a column list, or one that copies its rows with SELECT *, fails afterwards (error 213).`,
          "The existing rows get numbers in no particular order; new rows get the next ones.",
          "kakapo searched the database's code and the queries Query Store saw in the last 30 days for such inserts; code outside SQL Server (the application's own) cannot be searched.",
        ]
      : ["SQL Server refuses the key if two rows share a value of MoveId (an identity can repeat after a reseed or IDENTITY_INSERT), and then nothing changes."]),
    `Undoing it (the rollback) drops the key${clustered === "clustered" ? ": the table becomes a heap again and is rewritten once more" : ""}${newColumn ? "; the dropped column's 8 bytes a row stay until the table is rebuilt" : ""}.`,
    "A foreign key created later that points at the key stops the rollback until it is dropped.",
    `Tell the vendor: their next upgrade must expect this key${newColumn ? " and column" : ""}.`,
    `Needs the ALTER permission on dbo.${table} (db_ddladmin or db_owner in the database).`,
  ]
  const inputs: FixPreview["inputs"] = [
    {
      name: "key",
      label: "Key",
      kind: "select",
      options: choices,
      value: key,
      help: "kakapo picks a unique index first, then the identity column; a new column is the last resort, because it changes the table's shape.",
    },
    ...(newColumn
      ? [{ name: "column", label: "New column", kind: "text" as const, value: column, help: "kakapo's name, so nobody mistakes it for the application's own column." }]
      : []),
    { name: "name", label: "Key name", kind: "text", value: name, help: "The primary key's name; it must be new in schema dbo." },
    {
      name: "clustered",
      label: "Clustered",
      kind: "select",
      options: ["clustered", "nonclustered"],
      value: clustered,
      advanced: true,
      help: "Clustered turns the heap into a clustered table, which also ends IDX06. A nonclustered key leaves the heap as it is.",
    },
    ...(newColumn
      ? [
          {
            name: "confirm",
            label: confirmLabel,
            kind: "bool" as const,
            value: String(confirmed),
            help: "Ask the vendor if you are not sure: an INSERT without a column list fails once the table has a new column.",
          },
        ]
      : []),
  ]
  return {
    database,
    engineVersion: "17.0.1000.7",
    lead: `${table} gets a primary key on ${cols}: SQL Server will refuse a second row with the same key.`,
    held,
    changes: [{ setting: "Primary key", current: "none", target }],
    statement,
    rollback,
    warnings,
    inputs,
    options: { ...options, key, name, clustered, ...(newColumn ? { column, confirm: String(confirmed) } : {}) },
  }
}

export const mockDesign: FeatureMock = async (method, path, init) => {
  const addKey = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/add-primary-key\/(preview|apply)$/)
  if (addKey && method === "POST") {
    const body = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }) : {}
    const preview = keyPreview(decodeURIComponent(addKey[2]), body.options ?? {})
    if (addKey[3] === "preview") return preview
    if (preview.held) throw new ApiError(preview.held, 409)
    await delay(1500)
    const target = preview.changes[0].target
    return {
      preview,
      before: "none",
      after: target.replace(/\(([^,)]+)(, a new bigint identity column)?\)/, "($1)") + (target.includes("Notes_Import") ? ", 6.5 MB" : ", 218 MB"),
      verified: true,
      changeId: 950 + Math.floor(Math.random() * 49),
      appliedAt: now(),
    } satisfies FixResult
  }
  const disable = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/disable-index\/(preview|apply)$/)
  if (disable && method === "POST") {
    const body = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }) : {}
    const options = body.options ?? {}
    const preview = disablePreview(decodeURIComponent(disable[2]), options)
    if (disable[3] === "preview") return preview
    if (preview.held) throw new ApiError(preview.held, 409)
    await delay(800)
    return {
      preview,
      before: preview.changes[0].current,
      after: `${options.index} is disabled; ${preview.changes[0].current.split(", ").pop()} freed inside the file.`,
      verified: true,
      changeId: 950 + Math.floor(Math.random() * 49),
      appliedAt: now(),
    } satisfies FixResult
  }
  const fix = path.match(/^\/api\/servers\/(\d+)\/databases\/([^/]+)\/fixes\/create-index\/(preview|apply)$/)
  if (!fix || method !== "POST") return undefined
  const options = init?.body ? (JSON.parse(String(init.body)) as { options?: Record<string, string> }).options : undefined
  if (options?.source !== "foreign-key") return undefined // a plan's suggestion: api-sample.mock.ts
  const database = decodeURIComponent(fix[2])
  const preview = foreignKeyPreview(database, options)
  if (fix[3] === "preview") return preview
  if (localStorage.getItem("mock.fixFails") === "1") throw new ApiError("SQL Server did not create the index", 502)
  await delay(1500)
  return {
    preview,
    before: "none",
    after: `${preview.options?.name} on dbo.Lines (OrderId), 24.8 MB`,
    verified: true,
    changeId: 950 + Math.floor(Math.random() * 49),
    appliedAt: now(),
  } satisfies FixResult
}
