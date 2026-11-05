import type { QueryAction } from "@/api-queries"
import type { TuningFailure, TuningQuery, TuningRegression, TuningReport, TuningSameHour } from "@/api-tuning"
import { ActionButton } from "@/components/query/action-button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { cn } from "@/lib/utils"

import { CopyButton, InvestigateLink } from "./explain"
import { formatCount, serverWhen, signedPct, tuningMs, verdictColour } from "./format"

// "Same hour, before and after": each business hour of the day of the change beside the same hour on the
// working days before (their CPU summed over their runs, executions per day).
export function SameHourTable({ rows, day }: { rows: TuningSameHour[]; day: string }) {
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Hour</TableHead>
            <TableHead className="text-right">{day}</TableHead>
            <TableHead className="text-right">Days before</TableHead>
            <TableHead className="text-right">Change</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => {
            const pct =
              r.day.cpuPerExecMs !== null && r.previous.cpuPerExecMs
                ? Math.round(((r.day.cpuPerExecMs - r.previous.cpuPerExecMs) / r.previous.cpuPerExecMs) * 100)
                : null
            return (
              <TableRow key={r.hour} className="hover:bg-transparent">
                <TableCell className="align-top tabular-nums">{r.hour}</TableCell>
                <TableCell className="align-top text-right whitespace-normal">
                  <div className="grid">
                    <span className="tabular-nums">{r.day.cpuPerExecMs === null ? "—" : tuningMs(r.day.cpuPerExecMs)}</span>
                    <span className="text-muted-foreground">
                      {r.day.note || (r.day.executions !== null ? `${formatCount(r.day.executions)} runs` : "")}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="align-top text-right whitespace-normal">
                  <div className="grid">
                    <span className="tabular-nums">{r.previous.cpuPerExecMs === null ? "—" : tuningMs(r.previous.cpuPerExecMs)}</span>
                    <span className="text-muted-foreground">
                      {r.previous.executions !== null
                        ? `${formatCount(r.previous.executions)} runs, ${r.previous.days} ${r.previous.days === 1 ? "day" : "days"}`
                        : r.previous.note}
                    </span>
                  </div>
                </TableCell>
                <TableCell
                  className={cn(
                    "align-top text-right tabular-nums",
                    pct !== null && pct <= -10 && "text-emerald-700 dark:text-emerald-400",
                    pct !== null && pct >= 10 && "text-red-700 dark:text-red-400",
                  )}
                >
                  {r.day.note === "change" ? "not compared" : signedPct(pct)}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

function impactText(q: TuningQuery): string {
  if (q.verdict === "too few" || q.impactMsPerHour === 0) return ""
  const v = tuningMs(Math.abs(q.impactMsPerHour))
  return q.impactMsPerHour < 0 ? `saves ${v} of CPU an hour` : `adds ${v} of CPU an hour`
}

// "What is expensive": the top query shapes with CPU per run before -> now (execution-weighted), runs and
// the verdict in colour; Investigate on each row.
export function QueriesTable({ report, serverId }: { report: TuningReport; serverId: number }) {
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Query</TableHead>
            <TableHead className="hidden sm:table-cell text-right">CPU per run</TableHead>
            <TableHead className="hidden md:table-cell text-right">Runs</TableHead>
            <TableHead className="text-right">Verdict</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {report.queries.map((q) => (
            <TableRow key={q.queryHash} className="hover:bg-transparent">
              <TableCell className="align-top whitespace-normal">
                <div className="grid gap-1 min-w-0">
                  <span className="font-medium break-all">{q.name}</span>
                  {q.objectName && <code className="font-mono text-[0.7rem] text-muted-foreground break-all line-clamp-2">{q.text}</code>}
                  <span className="text-muted-foreground sm:hidden tabular-nums">
                    {tuningMs(q.cpuBeforeMs)} → {tuningMs(q.cpuNowMs)} per run
                  </span>
                  <span className="text-muted-foreground md:hidden tabular-nums">
                    {formatCount(q.runsBefore)} → {formatCount(q.runsNow)} runs
                  </span>
                  <div>
                    <InvestigateLink serverId={serverId} database={report.database} hash={q.queryHash} />
                  </div>
                </div>
              </TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {tuningMs(q.cpuBeforeMs)} → {tuningMs(q.cpuNowMs)}
              </TableCell>
              <TableCell className="hidden md:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {formatCount(q.runsBefore)} → {formatCount(q.runsNow)}
              </TableCell>
              <TableCell className="align-top text-right whitespace-normal">
                <div className="grid">
                  <span className={cn("font-medium", verdictColour[q.verdict])}>{q.verdict}</span>
                  <span className="text-muted-foreground">{impactText(q)}</span>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

// "Did a change break anything?": queries whose newest plan costs much more CPU per run than the one
// before (the toolkit's rule), and queries with failed runs, each with when it appeared against the change.
export function BreaksList({
  report,
  serverId,
  onAction,
}: {
  report: TuningReport
  serverId: number
  onAction: (a: QueryAction) => void
}) {
  if (report.regressions.length === 0 && report.failures.length === 0) {
    return (
      <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
        No plan got much more expensive and no query failed in this window.
      </p>
    )
  }
  return (
    <ul className="grid gap-2">
      {report.regressions.map((g: TuningRegression) => (
        <li key={`r${g.queryId}`} className={cn("grid gap-2 rounded-xl border p-3 min-w-0", g.afterChange && "border-amber-500/60")}>
          <div className="grid gap-0.5">
            <span className="font-medium break-all">{g.name}: a new plan costs {g.ratio} x the CPU per run</span>
            <span className="text-sm text-muted-foreground tabular-nums break-words">
              plan {g.oldPlanId} {tuningMs(g.oldCpuMs)} per run ({formatCount(g.oldRuns)} runs) → plan {g.newPlanId}{" "}
              {tuningMs(g.newCpuMs)} per run ({formatCount(g.newRuns)} runs); first seen {serverWhen(g.firstSeen)}
              {g.relation.includes("change") ? `, ${g.relation}` : ""}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {g.action && <ActionButton action={g.action} onAction={onAction} />}
            <InvestigateLink serverId={serverId} database={report.database} hash={g.queryHash} />
            <CopyButton text={g.copy} />
          </div>
        </li>
      ))}
      {report.failures.map((f: TuningFailure) => (
        <li key={`f${f.queryId}`} className={cn("grid gap-2 rounded-xl border p-3 min-w-0", f.afterChange && "border-amber-500/60")}>
          <div className="grid gap-0.5">
            <span className="font-medium break-all">
              {f.name}: {formatCount(f.aborted + f.exceptions)} failed runs
            </span>
            <span className="text-sm text-muted-foreground break-words">
              {formatCount(f.aborted)} timed out or cancelled, {formatCount(f.exceptions)} ended in an error; first {serverWhen(f.firstFailed)}
              {f.relation.includes("change") ? ` (${f.relation})` : ""}, last {serverWhen(f.lastFailed)}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <InvestigateLink serverId={serverId} database={report.database} hash={f.queryHash} />
            <CopyButton text={f.copy} />
          </div>
        </li>
      ))}
    </ul>
  )
}
