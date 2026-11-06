import { useState } from "react"
import { Loader2, ScanSearch, XCircle } from "lucide-react"

import { api, type DeadlockReport } from "@/api"
import { FindingCard } from "@/components/finding-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen, plural } from "@/lib/format"
import { cn } from "@/lib/utils"

const seconds = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

const sourceText: Record<DeadlockReport["source"], string> = {
  file: "from the system_health files",
  "ring-buffer": "from the system_health ring buffer (no file target)",
  // Phase 5e: kakapo's event session first (docs/phase5e-contract.md).
  kakapo: "from kakapo's event session files",
  "kakapo+file": "from kakapo's event session files, and the system_health files before it existed",
  "kakapo+ring-buffer": "from kakapo's event session files, and the system_health ring buffer before it existed",
}

// DLK01 on the Performance card page (docs/phase3e-contract.md): the last stored deadlock report, or the
// note that it has not been run, and a button that reads it now; under the pairs, the server's health
// events of the same read (docs/phase3f-contract.md, item 21). Read on request only, because reading
// the system_health files can take a minute on a busy server.
export function DeadlocksPanel({
  serverId,
  database,
  report,
  onResult,
}: {
  serverId: number
  database: string
  report: DeadlockReport | null | undefined
  onResult: (report: DeadlockReport) => void
}) {
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function run() {
    setRunning(true)
    setError(null)
    try {
      onResult(await api.runDeadlockReport(serverId, database))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRunning(false)
    }
  }

  return (
    <section className="grid gap-3" aria-label="Deadlocks" aria-busy={running}>
      <div className="grid gap-0.5">
        <h2 className="font-medium">Deadlocks</h2>
        <p className="text-sm text-muted-foreground">
          Reads the deadlock graphs SQL Server kept in its system_health session for the last 7 days and groups them
          by the pair of procedures involved. Can take a minute on a busy server.
        </p>
      </div>

      {report ? (
        <ReportBody report={report} className={cn("transition-opacity", running && "opacity-50")} />
      ) : (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          Not run yet. Read deadlocks reads the last 7 days of deadlock graphs from SQL Server; it can take a minute
          on a busy server.
        </p>
      )}

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the deadlocks</AlertTitle>
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button variant="outline" onClick={run} disabled={running}>
          {running ? <Loader2 className="animate-spin" /> : <ScanSearch />}
          Read deadlocks (7 days)
        </Button>
        {running && (
          <span className="text-sm text-muted-foreground" role="status">
            Reading the system_health session…
          </span>
        )}
      </div>
    </section>
  )
}

function ReportBody({ report: r, className }: { report: DeadlockReport; className?: string }) {
  return (
    <div className={cn("grid gap-3", className)}>
      <p className="text-xs text-muted-foreground">
        as of {formatWhen(r.capturedAt)}, took {seconds.format(r.durationSeconds)} s ·{" "}
        {plural(r.total, "deadlock", "deadlocks")} in {r.days} days · {sourceText[r.source] ?? r.source}
      </p>
      {r.pairs.length === 0 ? (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          No deadlocks in the last {r.days} days.
        </p>
      ) : (
        <div className="rounded-xl border">
          <Table className="text-xs sm:text-sm">
            <TableHeader>
              <TableRow>
                <TableHead>Pair</TableHead>
                <TableHead className="text-right">Count</TableHead>
                <TableHead>Last</TableHead>
                <TableHead>Objects</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {r.pairs.map((p) => (
                <TableRow key={p.procedures.join("|")} className="hover:bg-transparent">
                  <TableCell className="align-top whitespace-normal">
                    <div className="grid gap-0.5 font-mono text-xs break-all">
                      <span>{p.procedures[0]}</span>
                      <span>{p.procedures[1]}</span>
                    </div>
                    {/* Phase 5m: the kind READ COMMITTED SNAPSHOT removes. */}
                    {(p.withReader ?? 0) > 0 && (
                      <span className="mt-1 inline-block rounded bg-amber-100 px-1.5 py-0.5 text-xs text-amber-800 dark:bg-amber-950 dark:text-amber-300">
                        with a reader{p.withReader !== p.count ? ` (${p.withReader} of ${p.count})` : ""}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="align-top text-right tabular-nums">{p.count}</TableCell>
                  <TableCell className="align-top whitespace-normal">{formatWhen(p.last)}</TableCell>
                  <TableCell className="align-top whitespace-normal">
                    <div className="grid gap-0.5 font-mono text-xs break-all">
                      {p.objects.map((o) => (
                        <span key={o}>{o}</span>
                      ))}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      {r.findings.map((f) => (
        <FindingCard key={`${f.checkId}/${f.object}/${f.finding}`} finding={f} />
      ))}
      <HealthEvents events={r.health} days={r.days} />
    </div>
  )
}

const componentText: Record<string, string> = { SYSTEM: "System", IO_SUBSYSTEM: "Disk I/O" }

// Phase 3f, item 21: the server's own health warnings from the same system_health read (non-yielding
// tasks, memory dumps, bad pages, long I/Os). Nothing when there are none.
function HealthEvents({ events, days }: { events: DeadlockReport["health"]; days: number }) {
  if (!events || events.length === 0) return null
  return (
    <div className="grid gap-2">
      <h3 className="text-sm font-medium">Server health events ({days} days)</h3>
      <div className="rounded-xl border">
        <Table className="text-xs sm:text-sm">
          <TableHeader>
            <TableRow>
              <TableHead>Time</TableHead>
              <TableHead>Component</TableHead>
              <TableHead>Detail</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.map((e, i) => (
              <TableRow key={`${e.at}/${e.component}/${i}`} className="hover:bg-transparent">
                <TableCell className="align-top whitespace-normal">{formatWhen(e.at)}</TableCell>
                <TableCell className="align-top whitespace-normal">
                  {componentText[e.component] ?? e.component}
                  {e.state === "error" && <span className="text-destructive"> (error)</span>}
                </TableCell>
                <TableCell className="align-top whitespace-normal break-words">{e.detail}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}
