import type { QueryAction, QueryDetail, QueryHour, QueryTable } from "@/api-queries"
import { ActionButton } from "@/components/query/action-button"
import { formatMs, formatRows, formatWhole, serverWhen, type ServerClock } from "@/components/query/format"
import { Badge } from "@/components/ui/badge"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { cn } from "@/lib/utils"

// The plain sentences first (domain.ExplainQuery): each one that names a fix carries its button.
export function ExplainList({ detail, onAction }: { detail: QueryDetail; onAction: (a: QueryAction) => void }) {
  return (
    <ul className="grid gap-2">
      {detail.explain.map((l) => (
        <li key={l.text} className="grid gap-1.5 rounded-xl border p-3 min-w-0">
          <p className="break-words">{l.text}</p>
          {l.action && (
            <div>
              <ActionButton action={l.action} onAction={onAction} variant="default" />
            </div>
          )}
        </li>
      ))}
    </ul>
  )
}

// "History": the runs per hour over the last 7 days, newest first, with the plans that ran; CPU per hour
// drawn as a plain bar (its width against the busiest hour), no chart library.
export function HistoryTable({ hours, clock }: { hours: QueryHour[]; clock: ServerClock }) {
  const most = Math.max(1, ...hours.map((h) => h.cpuMs))
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Hour</TableHead>
            <TableHead className="text-right">Runs</TableHead>
            <TableHead className="w-[40%]">CPU</TableHead>
            <TableHead className="hidden sm:table-cell text-right">Avg time</TableHead>
            <TableHead className="hidden md:table-cell text-right">Reads</TableHead>
            <TableHead className="hidden sm:table-cell">Plans</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {hours.map((h) => (
            <TableRow key={h.hour} className="hover:bg-transparent">
              <TableCell className="align-top whitespace-normal">
                <div className="grid">
                  <span className="whitespace-nowrap">{serverWhen(clock, h.hour, "row")}</span>
                  <span className="text-muted-foreground sm:hidden">plan {h.plans.join(", ") || "—"}</span>
                </div>
              </TableCell>
              <TableCell className="align-top text-right tabular-nums">{formatWhole(h.runs)}</TableCell>
              <TableCell className="align-top">
                <div className="grid gap-0.5">
                  <div className="h-2 rounded-full bg-muted" aria-hidden>
                    <div className="h-2 rounded-full bg-primary/70" style={{ width: `${Math.max(2, (h.cpuMs / most) * 100)}%` }} />
                  </div>
                  <span className="tabular-nums text-muted-foreground">{formatMs(h.cpuMs)}</span>
                </div>
              </TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {formatMs(h.avgDurationMs)}
              </TableCell>
              <TableCell className="hidden md:table-cell align-top text-right tabular-nums">{formatWhole(h.reads)}</TableCell>
              <TableCell className="hidden sm:table-cell align-top">{h.plans.join(", ") || "—"}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

// "Waits": where the query's waiting went over 7 days, in sp_QuickieStore's categories and plain words.
export function WaitsList({ detail }: { detail: QueryDetail }) {
  return (
    <ul className="grid gap-2">
      {detail.waits.map((w) => (
        <li key={w.category} className="grid gap-1 min-w-0">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3">
            <span className="font-medium">{w.category}</span>
            <span className="text-sm tabular-nums text-muted-foreground">
              {formatMs(w.waitMs)} · {w.pct}%
            </span>
          </div>
          <div className="h-2 rounded-full bg-muted" aria-hidden>
            <div className="h-2 rounded-full bg-amber-500/70" style={{ width: `${Math.max(2, w.pct)}%` }} />
          </div>
          <p className="text-sm text-muted-foreground break-words">{w.words}</p>
        </li>
      ))}
    </ul>
  )
}

// "Every statement in {procedure}", most reads first (InvestigateProcedure.sql, part 2): runs, CPU and
// reads per run (lowest, average, highest), what to look at; this query's statement marked.
export function StatementsTable({ detail }: { detail: QueryDetail }) {
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Statement</TableHead>
            <TableHead className="text-right">Reads/run</TableHead>
            <TableHead className="hidden sm:table-cell text-right">CPU/run</TableHead>
            <TableHead className="hidden md:table-cell text-right">Runs</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {detail.statements.map((s) => (
            <TableRow key={`${s.line}/${s.text}`} className={cn("hover:bg-transparent", s.thisQuery && "bg-primary/5")}>
              <TableCell className="align-top whitespace-normal">
                <div className="grid gap-1 min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
                    <span>line {s.line}</span>
                    <span>{s.pctReads}% of the reads</span>
                    {s.thisQuery && <Badge className="bg-primary/10 text-primary">this query</Badge>}
                  </div>
                  <code className="font-mono text-xs break-all line-clamp-3">{s.text}</code>
                  {s.lookAt.length > 0 && <span className="text-amber-700 dark:text-amber-400">{s.lookAt.join("; ")}</span>}
                  <span className="text-muted-foreground sm:hidden">
                    CPU {formatMs(s.avgCpuMs)}/run, {formatWhole(s.runs)} runs
                  </span>
                </div>
              </TableCell>
              <TableCell className="align-top text-right tabular-nums">
                <div className="grid">
                  <span>{formatWhole(s.avgReads)}</span>
                  <span className="text-muted-foreground whitespace-nowrap">
                    {formatWhole(s.minReads)}–{formatWhole(s.maxReads)}
                  </span>
                </div>
              </TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">
                <div className="grid">
                  <span className="whitespace-nowrap">{formatMs(s.avgCpuMs)}</span>
                  <span className="text-muted-foreground whitespace-nowrap">
                    {formatMs(s.minCpuMs)}–{formatMs(s.maxCpuMs)}
                  </span>
                </div>
              </TableCell>
              <TableCell className="hidden md:table-cell align-top text-right tabular-nums">{formatWhole(s.runs)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function statsAge(t: QueryTable): string {
  if (!t.found) return "not found"
  if (t.statistics === 0) return "no statistics"
  if (t.ageDays === null) return "never updated"
  return t.ageDays === 0 ? "today" : t.ageDays === 1 ? "1 day" : `${t.ageDays} days`
}

// "Tables": the tables of this database the plans touch, largest first, with the statistics' age and how
// much of each changed since; update-statistics-table on each.
export function TablesTable({ detail, onAction }: { detail: QueryDetail; onAction: (a: QueryAction) => void }) {
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Table</TableHead>
            <TableHead className="text-right">Rows</TableHead>
            <TableHead className="hidden sm:table-cell text-right">Statistics age</TableHead>
            <TableHead className="hidden sm:table-cell text-right">Changed</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {detail.tables.map((t) => (
            <TableRow key={t.name} className="hover:bg-transparent">
              <TableCell className="align-top whitespace-normal">
                <div className="grid gap-1.5 min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-mono text-xs break-all">{t.name}</span>
                    {t.stale && <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">stale statistics</Badge>}
                  </div>
                  <span className="text-muted-foreground sm:hidden">
                    statistics {statsAge(t)}, {t.maxModifiedPct}% changed
                  </span>
                  {t.indexes.length > 0 && (
                    <span className="text-muted-foreground break-all">via {t.indexes.join(", ")}</span>
                  )}
                  {t.action && (
                    <div>
                      <ActionButton action={t.action} onAction={onAction} />
                    </div>
                  )}
                  {/* Phase 5o: a safe copy from the Design card's IDX04, with its Apply (docs/phase5o-contract.md). */}
                  {t.copies?.map((c) => (
                    <div key={c.text} className="grid gap-1.5">
                      <span className="break-words">{c.text}</span>
                      {c.action && (
                        <div>
                          <ActionButton action={c.action} onAction={onAction} />
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              </TableCell>
              <TableCell className="align-top text-right tabular-nums">{formatRows(t.rows)}</TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right whitespace-nowrap">{statsAge(t)}</TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{t.maxModifiedPct}%</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
