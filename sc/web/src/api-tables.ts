// Phase 5a: archiving the old rows of a table that only grows (docs/phase5a-contract.md). The table picker's
// route lives here; the two fixes ("archive-table", "remove-archive") go through api.previewFix / api.applyFix
// with their options, and the Storage card's Tables row reads the remove-archive entries of the card's fixes.

import { request, type Fix } from "./api"

export const archiveTableFixId = "archive-table"
export const removeArchiveFixId = "remove-archive"

// A date column of a listed table, and whether it is the first column of an index (each batch is then a seek).
export interface TableDateColumn {
  name: string
  indexed: boolean
}

// One user table for the picker (GET .../tables), biggest first.
export interface TableListing {
  schema: string
  name: string
  rows: number
  mb: number
  dateColumns: TableDateColumn[]
  archived: boolean // kakapo already archives it (its job exists)
}

export interface TableList {
  database: string
  tables: TableListing[]
  jobsError?: string // the archive jobs could not be read: archived is false everywhere
}

export const tablesApi = {
  // Every user table of the database, read live. 404 for an unknown server or database, 409 when the database
  // is not online, 502 when the server cannot be read.
  listTables: (serverId: number, database: string) =>
    request<TableList>(`/api/servers/${serverId}/databases/${encodeURIComponent(database)}/tables`),
}

// The table option of the archive fixes: [schema].[name], with ] doubled as SQL Server quotes it.
export function tableOption(schema: string, name: string): string {
  const q = (s: string) => `[${s.replaceAll("]", "]]")}]`
  return `${q(schema)}.${q(name)}`
}

// A table kakapo archives, as its remove-archive entry on the Storage card carries it.
export interface ArchivedTable {
  fix: Fix
  table: string // [dbo].[MailLog]
  label: string // dbo.MailLog
  keepMonths: string
  destination: string // "archive.MailLog in Shop" or "deleted, not kept"
  schedule: string // "daily 02:30"
  lastRun: string // "2026-11-04 02:30 (succeeded, 4,000 rows moved)" or "not yet"
  rowsKept?: number
  rowsArchived?: number
  disabled: boolean
}

// "[dbo].[Mail]]Log]" -> "dbo.Mail]Log".
export function tableLabel(option: string): string {
  const parts = option.match(/\[((?:[^\]]|\]\])*)\]/g) ?? []
  return parts.length > 0 ? parts.map((p) => p.slice(1, -1).replaceAll("]]", "]")).join(".") : option
}

// The tables kakapo archives, from the card's fixes.
export function archivedTables(fixes?: Fix[]): ArchivedTable[] {
  return (fixes ?? [])
    .filter((f) => f.id === removeArchiveFixId)
    .map((fix) => {
      const o = fix.options ?? {}
      const count = (v?: string) => (v === undefined || v === "" ? undefined : Number(v))
      return {
        fix,
        table: o.table ?? "",
        label: tableLabel(o.table ?? ""),
        keepMonths: o.keepMonths ?? "",
        destination: o.destination ?? "",
        schedule: o.schedule ?? "",
        lastRun: o.lastRun ?? "not yet",
        rowsKept: count(o.rowsKept),
        rowsArchived: count(o.rowsArchived),
        disabled: o.disabled === "true",
      }
    })
}
