// Phase 4f: what the card page says when the dialog is closed while a long fix still runs in the
// background; the outcome lands where the line says.
const stillRunning: Record<string, string> = {
  "checkdb-now": "CHECKDB is still running on {db}; the result appears on the Backups card",
  "create-index": "The index is still being created on {db}; the result appears in the recent changes",
  "drop-index": "The index is still being dropped on {db}; the result appears in the recent changes",
  "update-statistics": "The statistics update is still running on {db}; the result appears on the Upkeep card",
  "update-statistics-table": "The statistics update is still running on {db}; the result appears in the recent changes",
  "tempdb-files": "The TempDB files are still being set up; the result appears on the Storage card",
}

export function stillRunningNotice(fixId: string, database: string): string {
  return (stillRunning[fixId] ?? "The fix is still running on {db}; the result appears in the recent changes").replace(
    "{db}",
    database,
  )
}
