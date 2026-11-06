import { Fragment, useContext, useState } from "react"
import { Loader2, ScanSearch, XCircle } from "lucide-react"

import type { BlockingHistory, BlockingHour, BlockingLeader, Finding, HistoryKind } from "@/api"
import { blockingHistoryApi, blockingHistoryDays } from "@/api-blocking-history"
import { FindingCard } from "@/components/finding-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { FindingContext } from "@/lib/finding-context"
import { formatWhen, plural } from "@/lib/format"
import { blockingPath, queryPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

const seconds = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

const sourceText: Record<BlockingHistory["source"], string> = {
  file: "from the system_health files",
  "ring-buffer": "from the system_health ring buffer (no file target)",
  // Phase 5e: kakapo's event session first (docs/phase5e-contract.md).
  kakapo: "from kakapo's event session files",
  "kakapo+file": "from kakapo's event session files, and the system_health files before it existed",
  "kakapo+ring-buffer": "from kakapo's event session files, and the system_health ring buffer before it existed",
}

// The kinds in words, as the live blocking page says them.
const kindText: Record<HistoryKind, string> = {
  sleeping: "idle with a transaction open",
  implicit: "implicit transactions",
  serializable: "SERIALIZABLE",
  "repeatable-read": "REPEATABLE READ",
  "long-running": "long-running statement",
  "cross-database": "from another database",
}

// BLK01 on the Performance card page (docs/phase5d-contract.md): the last stored blocking history, or the
// note that it has not been read, and a button that reads it now. Sentences first (what blocked users and
// what the history cannot show), then the week's heat line, then the leaders, each with its BLK01 finding
// and Investigate (the query page when Query Store knows the statement, else the blocking page). Read on
// request only, because reading the system_health files can take a minute on a busy server. A read shows
// at once; the card's next read carries it (and BLK01 in the checks) from the case file.
export function BlockingHistoryPanel({
  serverId,
  database,
  report,
}: {
  serverId: number
  database: string
  report: BlockingHistory | null | undefined
}) {
  const [read, setRead] = useState<BlockingHistory | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inherited = useContext(FindingContext)
  const shown = newest(report ?? null, read)

  async function run() {
    setRunning(true)
    setError(null)
    try {
      setRead(await blockingHistoryApi.read(serverId, database, blockingHistoryDays))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRunning(false)
    }
  }

  const investigate = (l: BlockingLeader) => ({
    to: l.queryHash ? queryPath(serverId, database, l.queryHash) : blockingPath(serverId, database),
  })
  // A finding's Copy block says when the history was read.
  const context = shown && inherited ? { ...inherited, asOf: formatWhen(shown.capturedAt) } : inherited

  return (
    <section className="grid min-w-0 gap-3" aria-label="Blocking history" aria-busy={running}>
      <div className="grid gap-0.5">
        <h2 className="font-medium">Blocking history ({blockingHistoryDays} days)</h2>
        <p className="text-sm text-muted-foreground">
          What blocked users over the last {blockingHistoryDays} days, from the snapshots SQL Server's system_health
          session takes every five minutes, grouped by the statement that held the locks.
        </p>
      </div>

      {shown ? (
        <FindingContext.Provider value={context}>
          <HistoryBody
            history={shown}
            investigate={investigate}
            className={cn("transition-opacity", running && "opacity-50")}
          />
        </FindingContext.Provider>
      ) : (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          Not run yet. Read blocking history reads the last {blockingHistoryDays} days of blocking SQL Server kept in
          its system_health session; it can take a minute on a busy server.
        </p>
      )}

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the blocking history</AlertTitle>
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button variant="outline" onClick={run} disabled={running}>
          {running ? <Loader2 className="animate-spin" /> : <ScanSearch />}
          Read blocking history ({blockingHistoryDays} days)
        </Button>
        <span className="text-sm text-muted-foreground" role="status">
          {running ? "Reading the system_health session…" : "Reading the session files can take a minute."}
        </span>
      </div>
    </section>
  )
}

function newest(a: BlockingHistory | null, b: BlockingHistory | null): BlockingHistory | null {
  if (!a) return b
  if (!b) return a
  return new Date(b.capturedAt).getTime() > new Date(a.capturedAt).getTime() ? b : a
}

// The sentence first: how much blocking the week held and who led it.
function summary(h: BlockingHistory): string {
  if (h.leaders.length === 0) {
    return `No blocking in this database that system_health saw in the last ${h.days} days.`
  }
  // Lead with what mattered most: the top finding's leader (ranked by severity, then by the longest
  // wait), not the one with the most rows; a 39-minute head outranks a one-second block.
  const rank: Record<string, number> = { Critical: 0, High: 1, Medium: 2, Low: 3, Info: 4 }
  const lead = [...h.findings].sort((a, b) => (rank[a.severity] ?? 9) - (rank[b.severity] ?? 9))[0]
  const top =
    (lead && h.leaders.find((l) => lead.object === l.object || lead.object.startsWith(l.procedure || l.blockerStatement.slice(0, 40)))) ??
    [...h.leaders].sort((a, b) => b.longestWaitSec - a.longestWaitSec)[0]
  const name = top.procedure || `"${short(top.blockerStatement, 60)}"`
  const times = top.occurrences === 1 ? "once" : top.occurrences === 2 ? "twice" : `${top.occurrences} times`
  const others = h.leaders.length - 1
  const where = top.object ? ` on ${top.object}` : ""
  const what = top.heldWhileWaiting
    ? `held a transaction open for ${duration(top.longestWaitSec)} while waiting${where}, ${times}`
    : `blocked ${plural(top.totalBlockedSessions, "session", "sessions")}${where}, ${times}, the longest for ${duration(top.longestWaitSec)}`
  return (
    `${plural(h.reportsTotal, "blocked session", "blocked sessions")} seen in ${plural(h.snapshots, "snapshot", "snapshots")} ` +
    `over ${h.days} days. The one that mattered most: ${name} ${what}.` +
    (others > 0 ? ` ${plural(others, "other blocker", "other blockers")} below.` : "")
  )
}

function short(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1).trimEnd()}…` : s
}

function duration(sec: number): string {
  if (sec < 1) return "under 1 s"
  if (sec < 60) return `${sec} s`
  if (sec < 5400) return `${Math.round(sec / 60)} min`
  return `${seconds.format(sec / 3600)} h`
}

function HistoryBody({
  history: h,
  investigate,
  className,
}: {
  history: BlockingHistory
  investigate: (l: BlockingLeader) => { to: string }
  className?: string
}) {
  return (
    <div className={cn("grid gap-3", className)}>
      <p>{summary(h)}</p>
      {/* Phase 5m: what READ COMMITTED SNAPSHOT would remove (docs/phase5m-contract.md, section 7). */}
      {h.classLine && <p className="text-sm">{h.classLine}</p>}
      <p className="text-sm text-muted-foreground">{h.missedNote}</p>
      <p className="text-xs text-muted-foreground">
        as of {formatWhen(h.capturedAt)}, took {seconds.format(h.durationSeconds)} s · {sourceText[h.source] ?? h.source}
      </p>
      <HeatLine hours={h.byHour} />
      {h.leaders.length > 0 && <LeadersTable history={h} investigate={investigate} />}
      {h.notes.length > 0 && (
        <ul className="grid gap-1 text-sm text-muted-foreground">
          {h.notes.map((n) => (
            <li key={n} className="break-words">
              {n}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// "2026-11-03T14:00" read as the server's clock, without the browser's time zone.
function hourParts(hour: string): { date: Date; weekend: boolean; midnight: boolean } {
  const [d, t] = hour.split("T")
  const [y, m, day] = d.split("-").map(Number)
  const h = Number(t?.slice(0, 2) ?? 0)
  const date = new Date(Date.UTC(y, m - 1, day, h))
  const wd = date.getUTCDay()
  return { date, weekend: wd === 0 || wd === 6, midnight: h === 0 }
}

const dayLabel = new Intl.DateTimeFormat("en-GB", { weekday: "short", timeZone: "UTC" })
const hourLabel = new Intl.DateTimeFormat("en-GB", {
  weekday: "short",
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  timeZone: "UTC",
})

// The week's heat line: one CSS bar per hour, its height the reports in that hour, weekends shaded, a
// label under each day's start. Server time.
function HeatLine({ hours }: { hours: BlockingHour[] }) {
  if (hours.length === 0) return null
  const most = Math.max(1, ...hours.map((h) => h.reports))
  const busiest = hours.reduce((a, b) => (b.reports > a.reports ? b : a), hours[0])
  const label =
    busiest.reports > 0
      ? `Blocked sessions per hour over the week; the busiest hour was ${hourLabel.format(hourParts(busiest.hour).date)} with ${busiest.reports}.`
      : "Blocked sessions per hour over the week: none."
  return (
    <figure className="grid gap-1">
      <div className="flex h-16 items-end rounded-md border px-px" role="img" aria-label={label}>
        {hours.map((h) => {
          const p = hourParts(h.hour)
          return (
            <div
              key={h.hour}
              className={cn("flex h-full min-w-0 flex-1 items-end", p.weekend && "bg-muted", p.midnight && "border-l border-border/60")}
              title={`${hourLabel.format(p.date)}: ${plural(h.reports, "blocked session", "blocked sessions")}${
                h.sessionsBlocked > 0 ? `, at most ${h.sessionsBlocked} at once` : ""
              }`}
            >
              {h.reports > 0 && (
                <div className="w-full rounded-t-sm bg-amber-500" style={{ height: `${Math.max(8, (h.reports / most) * 100)}%` }} />
              )}
            </div>
          )
        })}
      </div>
      <div className="flex text-[10px] text-muted-foreground">
        {hours.map((h) => {
          const p = hourParts(h.hour)
          return (
            <div key={h.hour} className="relative min-w-0 flex-1">
              {p.midnight && <span className="absolute left-0 whitespace-nowrap">{dayLabel.format(p.date)}</span>}
            </div>
          )
        })}
      </div>
      <figcaption className="pt-2 text-xs text-muted-foreground">
        Blocked sessions per hour, server time; weekends shaded.
      </figcaption>
    </figure>
  )
}

function LeadersTable({
  history: h,
  investigate,
}: {
  history: BlockingHistory
  investigate: (l: BlockingLeader) => { to: string }
}) {
  // Each leader's finding: the one whose object names it, "{procedure or statement} on {object}", in the
  // backend's terms (blkLabel in domain/blockinghistory.go).
  const findingFor = (l: BlockingLeader): Finding | undefined => {
    const label = l.procedure || (l.blockerStatement ? `"${shortRunes(l.blockerStatement, 80)}"` : "a statement the report did not record")
    // A head system_health kept on its own, with no object, is named by its statement alone.
    const object = l.object
      ? `${label} on ${l.object}`
      : l.heldWhileWaiting
        ? label
        : `${label} on an object the report did not name`
    return h.findings.find((f) => f.object === object)
  }
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Statement or procedure</TableHead>
            <TableHead className="hidden sm:table-cell">Object</TableHead>
            <TableHead className="text-right">Times</TableHead>
            <TableHead className="hidden text-right sm:table-cell">Sessions</TableHead>
            <TableHead className="hidden text-right sm:table-cell">Longest</TableHead>
            <TableHead className="hidden md:table-cell">Kinds</TableHead>
            <TableHead className="hidden sm:table-cell">Last</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {h.leaders.map((l) => {
            const f = findingFor(l)
            const kinds = [...(l.heldWhileWaiting ? ["held a transaction while waiting"] : []), ...l.kinds.map((k) => kindText[k] ?? k)].join(", ")
            return (
              <Fragment key={l.key}>
                <TableRow className="hover:bg-transparent">
                  <TableCell className="align-top whitespace-normal">
                    <div className="grid gap-0.5">
                      <span className="font-mono text-xs break-all">{l.procedure || short(l.blockerStatement, 160)}</span>
                      <span className="text-xs text-muted-foreground">
                        {[l.blockerProgram, l.blockerLogin, l.blockerHost].filter(Boolean).join(" · ")}
                      </span>
                      {/* On a phone the columns that do not fit say themselves here. */}
                      <span className="text-xs text-muted-foreground sm:hidden">
                        on {l.object || "?"} · {plural(l.totalBlockedSessions, "session", "sessions")} · longest{" "}
                        {duration(l.longestWaitSec)} · last {formatWhen(l.last)}
                        {kinds && ` · ${kinds}`}
                      </span>
                      {kinds && <span className="hidden text-xs text-muted-foreground sm:inline md:hidden">{kinds}</span>}
                    </div>
                  </TableCell>
                  <TableCell className="hidden align-top font-mono text-xs break-all whitespace-normal sm:table-cell">
                    {l.object}
                  </TableCell>
                  <TableCell className="align-top text-right tabular-nums">{l.occurrences}</TableCell>
                  <TableCell className="hidden align-top text-right tabular-nums sm:table-cell">{l.totalBlockedSessions}</TableCell>
                  <TableCell className="hidden align-top text-right tabular-nums whitespace-nowrap sm:table-cell">
                    {duration(l.longestWaitSec)}
                  </TableCell>
                  <TableCell className="hidden align-top whitespace-normal md:table-cell">{kinds || "—"}</TableCell>
                  <TableCell className="hidden align-top whitespace-normal sm:table-cell">{formatWhen(l.last)}</TableCell>
                </TableRow>
                {f && (
                  <TableRow className="hover:bg-transparent">
                    <TableCell colSpan={7} className="px-0 whitespace-normal sm:px-2">
                      {/* w-0 min-w-full: the finding takes the row's width without widening the table. */}
                      <div className="w-0 min-w-full">
                        <FindingCard finding={f} investigate={investigate(l)} />
                      </div>
                    </TableCell>
                  </TableRow>
                )}
              </Fragment>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

// BlockSnippet's rule (domain/blocking.go): at most n characters, the last one an ellipsis.
function shortRunes(s: string, n: number): string {
  const r = Array.from(s)
  return r.length > n ? `${r.slice(0, n - 1).join("").trimEnd()}…` : s
}
