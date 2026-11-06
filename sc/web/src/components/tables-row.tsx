import { useState } from "react"

import type { Finding, Fix } from "@/api"
import { archivedTables, type ArchivedTable } from "@/api-tables"
import { TablePicker } from "@/components/table-picker"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"

const n0 = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 })

// The Storage card's standing Tables row (docs/phase5a-contract.md): the tables kakapo archives, each with the
// rows it keeps, the rows archived so far, the last night's run and Remove (which previews deleting the job and
// keeping the archive), and "Archive a table…", which opens the picker and then the archive fix's preview for
// the table chosen.
export function TablesRow({
  serverId,
  database,
  fixes,
  findings,
  onArchive,
  onRemove,
}: {
  serverId: number
  database: string
  fixes?: Fix[]
  findings?: Finding[]
  onArchive: (table: string) => void
  onRemove: (fix: Fix) => void
}) {
  const [picking, setPicking] = useState(false)
  const archived = archivedTables(fixes)
  // Tables with an archive table (archive.<name>) but no archive job on this server, as IDX10 says them.
  const orphans = (findings ?? []).filter((f) => f.checkId === "IDX10" && f.finding.includes("no archive job runs"))
  return (
    <Card>
      <CardHeader>
        <CardTitle>Tables</CardTitle>
        <CardDescription>
          {archived.length === 0
            ? "kakapo archives no table of this database yet. A log or history table that only grows can have its old rows moved to an archive table, or deleted, every night in small batches by a SQL Server Agent job."
            : `kakapo moves the old rows of ${archived.length === 1 ? "this table" : "these tables"} every night in small batches. Remove previews deleting the job; the archived rows stay.`}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {orphans.length > 0 && (
          <ul className="grid gap-1 text-sm" aria-label="Archive tables without a job">
            {orphans.map((f) => (
              <li key={f.object}>{f.finding}</li>
            ))}
          </ul>
        )}
        {archived.length > 0 && (
          <ul className="grid divide-y rounded-xl border" aria-label="Tables kakapo archives">
            {archived.map((t) => (
              <ArchivedLine key={t.table} table={t} onRemove={() => onRemove(t.fix)} />
            ))}
          </ul>
        )}
        <div>
          <Button variant="outline" size="sm" onClick={() => setPicking(true)}>
            Archive a table…
          </Button>
        </div>
      </CardContent>
      {picking && (
        <TablePicker
          serverId={serverId}
          database={database}
          onClose={() => setPicking(false)}
          onPick={(table) => {
            setPicking(false)
            onArchive(table)
          }}
        />
      )}
    </Card>
  )
}

function ArchivedLine({ table: t, onRemove }: { table: ArchivedTable; onRemove: () => void }) {
  const kept = t.rowsKept === undefined ? "rows kept not counted" : `${n0.format(t.rowsKept)} rows kept`
  const deletes = t.destination === "deleted, not kept"
  const moved = deletes
    ? "old rows deleted, not kept"
    : t.rowsArchived === undefined
      ? `archived to ${t.destination}`
      : `${n0.format(t.rowsArchived)} archived to ${t.destination}`
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono text-sm break-all">{t.label}</span>
          {t.disabled && <Badge variant="destructive">job disabled</Badge>}
        </div>
        <p className="text-sm break-words">
          {kept}, {moved}; {t.keepMonths} months kept, {t.schedule}.
        </p>
        <p className="text-xs text-muted-foreground break-words">Last run: {t.lastRun}.</p>
      </div>
      <Button variant="outline" size="sm" onClick={onRemove}>
        Remove
      </Button>
    </li>
  )
}
