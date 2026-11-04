import { useEffect, useMemo, useState } from "react"
import { Loader2, Search, XCircle } from "lucide-react"

import { tableOption, tablesApi, type TableList, type TableListing } from "@/api-tables"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"

const n0 = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 })
const shownAtMost = 200

type Load = { kind: "loading" } | { kind: "failed"; message: string } | { kind: "ready"; list: TableList }

// "Archive a table…" (docs/phase5a-contract.md): every user table of the database, biggest first, searched by
// name. A table with no date column cannot say how old its rows are, and one kakapo already archives is
// stopped from the Tables row first, so neither can be picked. Picking a table hands its [schema].[name] to
// onPick, which opens the archive fix's preview.
export function TablePicker({
  serverId,
  database,
  onPick,
  onClose,
  list: listTables = tablesApi.listTables,
}: {
  serverId: number
  database: string
  onPick: (table: string) => void
  onClose: () => void
  list?: (serverId: number, database: string) => Promise<TableList>
}) {
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [query, setQuery] = useState("")
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let cancelled = false
    listTables(serverId, database)
      .then((list) => !cancelled && setLoad({ kind: "ready", list }))
      .catch((e: Error) => !cancelled && setLoad({ kind: "failed", message: e.message }))
    return () => {
      cancelled = true
    }
  }, [serverId, database, listTables, attempt])

  const matches = useMemo(() => {
    if (load.kind !== "ready") return []
    const q = query.trim().toLowerCase()
    return q ? load.list.tables.filter((t) => `${t.schema}.${t.name}`.toLowerCase().includes(q)) : load.list.tables
  }, [load, query])

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl max-h-[90svh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Archive a table</DialogTitle>
          <DialogDescription>
            Pick the table whose old rows should leave it. The next step previews everything before anything runs.
          </DialogDescription>
        </DialogHeader>

        {load.kind === "loading" && (
          <div className="flex items-center gap-2 text-muted-foreground py-4">
            <Loader2 className="size-4 animate-spin" />
            Reading the tables of {database}…
          </div>
        )}

        {load.kind === "failed" && (
          <Alert variant="destructive">
            <XCircle className="size-4" />
            <AlertTitle>Could not list the tables</AlertTitle>
            <AlertDescription className="break-words">
              <p>{load.message}</p>
              <Button
                variant="outline"
                size="sm"
                className="mt-2"
                onClick={() => {
                  setLoad({ kind: "loading" })
                  setAttempt(attempt + 1)
                }}
              >
                Try again
              </Button>
            </AlertDescription>
          </Alert>
        )}

        {load.kind === "ready" && (
          <div className="grid gap-3 min-w-0">
            <div className="relative">
              <Search className="absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input
                className="pl-8"
                placeholder="Search by name"
                aria-label="Search tables by name"
                value={query}
                autoFocus
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
            {load.list.jobsError && (
              <p className="text-xs text-muted-foreground break-words">
                kakapo's archive jobs could not be read ({load.list.jobsError}), so tables it already archives are not
                marked.
              </p>
            )}
            {matches.length === 0 ? (
              <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
                {load.list.tables.length === 0 ? `${database} has no user tables.` : "No table matches that name."}
              </p>
            ) : (
              <ul className="grid divide-y rounded-xl border" aria-label="Tables">
                {matches.slice(0, shownAtMost).map((t) => (
                  <TableLine key={`${t.schema}.${t.name}`} table={t} onPick={() => onPick(tableOption(t.schema, t.name))} />
                ))}
              </ul>
            )}
            {matches.length > shownAtMost && (
              <p className="text-xs text-muted-foreground">
                Showing the {shownAtMost} biggest of {n0.format(matches.length)}: search to narrow the list.
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function TableLine({ table: t, onPick }: { table: TableListing; onPick: () => void }) {
  const noDate = t.dateColumns.length === 0
  const disabled = noDate || t.archived
  const dates = t.dateColumns.map((c) => (c.indexed ? `${c.name} (indexed)` : c.name)).join(", ")
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono text-sm break-all">
            {t.schema}.{t.name}
          </span>
          {t.archived && <Badge variant="secondary">archived by kakapo</Badge>}
        </div>
        <p className="text-xs text-muted-foreground break-words">
          {n0.format(t.rows)} rows, {n0.format(t.mb)} MB
          {noDate ? ", no date column: kakapo cannot tell how old a row is" : `, dates: ${dates}`}
        </p>
      </div>
      <Button variant="outline" size="sm" disabled={disabled} onClick={onPick}>
        Choose
      </Button>
    </li>
  )
}
