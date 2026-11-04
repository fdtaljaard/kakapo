import { useEffect, useRef, useState } from "react"
import { Loader2 } from "lucide-react"

import { api, type Progress } from "@/api"
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"

type ProgressFn = (serverId: number, database: string) => Promise<Progress[]>

// The fixes that can run for minutes (docs/phase4e-contract.md, part 2), with the commands
// sys.dm_exec_requests shows while each runs. Only these poll the progress route; the backup schedule
// only when it takes the first full backup now.
const longFixCommands: Record<string, string[]> = {
  "checkdb-now": ["DBCC CHECKDB", "DBCC TABLE CHECK", "DBCC"],
  "create-index": ["CREATE INDEX"],
  "drop-index": ["DROP INDEX"],
  "update-statistics": ["UPDATE STATISTICS"],
  "update-statistics-table": ["UPDATE STATISTICS"],
  "backup-schedule": ["BACKUP DATABASE"],
  "tempdb-files": ["ALTER DATABASE"],
}

function isLongFix(fixId: string, options?: Record<string, string>): boolean {
  if (!(fixId in longFixCommands)) return false
  return fixId !== "backup-schedule" || options?.runNow === "true"
}

// The row that is this fix: kakapo's own session running the fix's command, else any of kakapo's own
// sessions, else someone's request with the fix's command in the fix's database (tempdb-files runs in
// tempdb, from master).
function pickProgress(rows: Progress[], fixId: string, database: string): Progress | undefined {
  const commands = longFixCommands[fixId] ?? []
  const fits = (r: Progress) => commands.includes(r.command)
  const databases = fixId === "tempdb-files" ? ["tempdb", "master", ""] : [database]
  return (
    rows.find((r) => r.mine && fits(r)) ??
    rows.find((r) => r.mine) ??
    rows.find((r) => fits(r) && databases.includes(r.database))
  )
}

// The line a page shows after its fix dialog was closed while a long fix still runs, until dismissed.
export function StillRunningNotice({ notice, onDismiss }: { notice: string; onDismiss: () => void }) {
  return (
    <Alert role="status" aria-live="polite">
      <Loader2 className="animate-spin text-muted-foreground" />
      <AlertDescription className="text-foreground break-words">{notice}.</AlertDescription>
      <AlertAction>
        <Button variant="ghost" size="sm" onClick={onDismiss}>
          Dismiss
        </Button>
      </AlertAction>
    </Alert>
  )
}

// "45 s", "3 min 12 s", "1 h 4 min".
function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s} s`
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`
  return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`
}

// While a fix applies: nothing for most fixes; for a long one, its progress (below).
export function FixProgress(props: {
  serverId: number
  database: string
  fixId: string
  options?: Record<string, string> // the options the preview was built with (runNow)
  startedAt: number // Date.now() when Apply was pressed
  pending?: boolean // phase 4f: the apply answered with a run that is still pending
  load?: ProgressFn
}) {
  return isLongFix(props.fixId, props.options) ? <LongFixProgress {...props} /> : null
}

// The done state of a long fix keeps the time its apply took; nothing for the other fixes.
export function FixTook({
  fixId,
  options,
  seconds,
}: {
  fixId: string
  options?: Record<string, string>
  seconds?: number
}) {
  if (seconds === undefined || !isLongFix(fixId, options)) return null
  return <p className="text-muted-foreground">Took {formatDuration(seconds)}.</p>
}

// While a long fix applies: polls the progress route every 2 seconds (each read after the last one
// answered) and shows the command with a bar. With a percentage (CHECKDB, backups, restores, shrinks):
// the bar fills and "about {m} min to go" when SQL Server estimates it. Without one (index builds,
// statistics): the bar pulses and the line counts the time since Apply. "Starting…" until the first row
// that matches. A failed read changes nothing shown and never touches the apply; unmounting (the apply
// returned or failed) stops the polling. Phase 4f: while the run is pending, once kakapo's own session
// was seen and a read no longer shows it, SQL Server has finished the statement and the run is reading
// the result back (or waiting for it on a lost connection): "Finishing" with a pulsing bar, so the bar
// never freezes on its last row.
function LongFixProgress({
  serverId,
  database,
  fixId,
  startedAt,
  pending = false,
  load = api.progress,
}: {
  serverId: number
  database: string
  fixId: string
  startedAt: number
  pending?: boolean
  load?: ProgressFn
}) {
  const [row, setRow] = useState<Progress | undefined>()
  const [now, setNow] = useState(() => Date.now())
  // Kakapo's own session was in a read, and is not in the last one.
  const seenMine = useRef(false)
  const [mineGone, setMineGone] = useState(false)

  useEffect(() => {
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const rows = await load(serverId, database)
        const found = pickProgress(rows, fixId, database)
        // Between two statements of a batch nothing matches: keep the last row shown.
        if (!cancelled && found) setRow(found)
        const mine = rows.some((r) => r.mine)
        if (mine) seenMine.current = true
        if (!cancelled) setMineGone(seenMine.current && !mine)
      } catch {
        /* a failed read leaves the spinner as it is */
      }
      if (!cancelled) timer = setTimeout(poll, 2000)
    }
    void poll()
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [serverId, database, fixId, load])

  // The elapsed time without a percentage ticks every second between the reads.
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(tick)
  }, [])

  const finishing = pending && mineGone
  const percent = !finishing && row && row.percent > 0 ? Math.min(100, row.percent) : undefined
  let label = "Starting…"
  if (finishing) {
    label = "Finishing: waiting for SQL Server's result…"
  } else if (row && percent !== undefined) {
    label = `${row.command} ${Math.floor(percent)}%`
    if (row.remainingSeconds > 0) label += `, about ${Math.max(1, Math.round(row.remainingSeconds / 60))} min to go`
  } else if (row) {
    label = `${row.command} running for ${formatDuration(Math.max(row.elapsedSeconds, (now - startedAt) / 1000))}`
  }

  return (
    <div className="grid gap-2 rounded-md border bg-muted/40 p-3 min-w-0" role="status" aria-live="polite">
      <div className="flex items-start gap-2 text-sm min-w-0">
        <Loader2 className="size-4 shrink-0 translate-y-0.5 animate-spin text-muted-foreground" />
        <span className="break-words min-w-0">{label}</span>
      </div>
      <div
        className="h-2 w-full overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent !== undefined ? Math.floor(percent) : undefined}
      >
        {percent !== undefined ? (
          <div className="h-full rounded-full bg-primary transition-[width] duration-500" style={{ width: `${percent}%` }} />
        ) : (
          <div className="h-full w-full rounded-full bg-primary/40 animate-pulse" />
        )}
      </div>
    </div>
  )
}
