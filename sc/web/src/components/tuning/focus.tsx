import type { TuningFocus, TuningFocusNumbers, TuningHour, TuningReport } from "@/api-tuning"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { cn } from "@/lib/utils"

import { CopyButton, InvestigateLink } from "./explain"
import { formatCount, serverWhen, tuningMs, verdictColour } from "./format"
import { HourChart, type ChartRow } from "./hour-chart"

// Phase 5h (docs/phase5h-contract.md): "Did it help {object}?", the changed object's own numbers. Three numbers
// before and now with the verdict in colour, its own chart (CPU per run per hour, with the change marker), and for
// a deploy the table of its objects (for a view or an index, the queries that name or use it), Investigate per row.
// Hidden when the centred change is not followed.

function focusTitle(f: TuningFocus): string {
  return f.kind === "deploy" ? `Did it help? The deploy of ${f.object}` : `Did it help ${f.object}?`
}

export function FocusSection({ report, serverId }: { report: TuningReport; serverId: number }) {
  const f = report.focus
  if (!f) return null
  const marker = report.changes.filter((c) => c.id === f.changeId)
  const many = f.kind === "view" || f.kind === "index"
  const rows: ChartRow[] = [
    { label: many ? "CPU per run of the queries it follows" : `CPU per run of ${f.object}`, value: (h) => h.cpuPerExecMs, height: "h-24" },
    { label: "Runs", value: (h) => h.executions, height: "h-10" },
  ]
  const tip = (h: TuningHour) =>
    h.noData
      ? `${serverWhen(h.hour)}${h.inProgress ? " (in progress)" : ""}: did not run`
      : `${serverWhen(h.hour)}${h.inProgress ? " (in progress)" : ""}${h.change ? " (change)" : ""}: ${tuningMs(h.cpuPerExecMs)} per run, ${formatCount(h.executions)} runs`
  const showNumbers = f.kind !== "deploy" && f.found
  return (
    <section className="grid gap-3 min-w-0" aria-label={focusTitle(f)}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="grid gap-0.5 min-w-0">
          <h2 className="font-medium break-words">{focusTitle(f)}</h2>
          <p className="text-sm text-muted-foreground break-words">
            Its own numbers in the same business hours as below: now {report.now.label}; before {report.before.label}.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {f.queryHash && f.kind !== "deploy" && <InvestigateLink serverId={serverId} database={report.database} hash={f.queryHash} />}
          <CopyButton text={f.copy} />
        </div>
      </div>

      {showNumbers && (
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <FocusNumber label="CPU per run" before={f.before} now={f.now} pick={(n) => n.cpuPerRunMs} ms verdict={f.verdict} />
          <FocusNumber label="Runs per business hour" before={f.before} now={f.now} pick={(n) => n.runsPerHour} />
          <FocusNumber label="CPU per business hour" before={f.before} now={f.now} pick={(n) => n.cpuPerHourMs} ms />
        </div>
      )}

      {f.index && (
        <p className="text-sm break-words">
          {f.index.used
            ? `Index ${f.index.index} on ${f.index.table}: used ${formatCount(f.index.seeks + f.index.scans + f.index.lookups)} times since ${serverWhen(f.index.since)} (${formatCount(f.index.seeks)} seeks, ${formatCount(f.index.scans)} scans, ${formatCount(f.index.lookups)} lookups), ${formatCount(f.index.updates)} updates.`
            : `Index ${f.index.index} on ${f.index.table}: not used since ${serverWhen(f.index.since)}, ${formatCount(f.index.updates)} updates. SQL Server last started ${serverWhen(f.index.serverStarted)}.`}
        </p>
      )}

      {showNumbers && (
        <HourChart hours={f.hours} changes={marker} rows={rows} tip={tip} databaseLoad={false} />
      )}

      {f.rows.length > 0 && <FocusRows focus={f} report={report} serverId={serverId} />}

      {f.notes.length > 0 && (
        <ul className="grid gap-1 text-xs text-muted-foreground list-disc pl-5">
          {f.notes.map((n) => (
            <li key={n} className="break-words">
              {n}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function FocusNumber({
  label,
  before,
  now,
  pick,
  ms = false,
  verdict,
}: {
  label: string
  before: TuningFocusNumbers
  now: TuningFocusNumbers
  pick: (n: TuningFocusNumbers) => number | null
  ms?: boolean
  verdict?: TuningFocus["verdict"]
}) {
  const show = (v: number | null) => (v === null ? "—" : ms ? tuningMs(v) : formatCount(v))
  return (
    <div className="grid gap-1 rounded-xl border p-3 min-w-0">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-lg font-semibold tabular-nums">{show(pick(now))}</span>
      <span className="text-xs text-muted-foreground tabular-nums">was {show(pick(before))}</span>
      {verdict && <span className={cn("text-xs font-medium", verdictColour[verdict])}>{verdict}</span>}
    </div>
  )
}

// A deploy's objects, or the queries that name a view or use an index: CPU per run and runs per business hour
// before -> now, the verdict in colour, Investigate per row.
function FocusRows({ focus, report, serverId }: { focus: TuningFocus; report: TuningReport; serverId: number }) {
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>{focus.kind === "deploy" ? "Object" : "Query"}</TableHead>
            <TableHead className="hidden sm:table-cell text-right">CPU per run</TableHead>
            <TableHead className="hidden md:table-cell text-right">Runs an hour</TableHead>
            <TableHead className="text-right">Verdict</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {focus.rows.map((r) => (
            <TableRow key={`${r.name}|${r.queryHash}`} className="hover:bg-transparent">
              <TableCell className="align-top whitespace-normal">
                <div className="grid gap-1 min-w-0">
                  <span className="font-medium break-all">{r.name}</span>
                  {r.text && r.name !== r.text && (
                    <code className="font-mono text-[0.7rem] text-muted-foreground break-all line-clamp-2">{r.text}</code>
                  )}
                  <span className="text-muted-foreground sm:hidden tabular-nums">
                    {tuningMs(r.cpuBeforeMs)} → {tuningMs(r.cpuNowMs)} per run
                  </span>
                  {r.queryHash && (
                    <div>
                      <InvestigateLink serverId={serverId} database={report.database} hash={r.queryHash} />
                    </div>
                  )}
                </div>
              </TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {tuningMs(r.cpuBeforeMs)} → {tuningMs(r.cpuNowMs)}
              </TableCell>
              <TableCell className="hidden md:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {formatCount(r.runsPerHourBefore)} → {formatCount(r.runsPerHourNow)}
              </TableCell>
              <TableCell className="align-top text-right whitespace-normal">
                {r.verdict ? (
                  <span className={cn("font-medium", verdictColour[r.verdict])}>{r.verdict}</span>
                ) : (
                  <span className="text-muted-foreground">{r.note}</span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
