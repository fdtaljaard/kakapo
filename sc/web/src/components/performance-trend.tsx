import { useState } from "react"
import { Link } from "react-router-dom"

import type { PerformanceTrend, TrendDay, TrendFigure, TrendMover } from "@/api"
import { formatMs } from "@/components/query/format"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { formatWhen } from "@/lib/format"
import { queryPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// Phase 5k (docs/phase5k-contract.md): the top of the Performance screen. Better or worse than usual in
// one sentence and three figures, the last days at a glance, and what made the difference: the queries
// that got a new plan, against themselves before, slower first, each with the change kakapo saw just
// before it ("after", never "because of") and Investigate.

const verdictTone: Record<string, string> = {
  better: "border-l-emerald-600 dark:border-l-emerald-500",
  worse: "border-l-red-600 dark:border-l-red-500",
  same: "border-l-muted-foreground/40",
}
const pillTone: Record<string, string> = {
  better: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  worse: "bg-red-500/15 text-red-700 dark:text-red-400",
  same: "bg-muted text-muted-foreground",
}
const changeTone: Record<TrendFigure["tone"], string> = {
  better: "text-emerald-700 dark:text-emerald-400",
  worse: "text-red-700 dark:text-red-400",
  same: "text-muted-foreground",
}

// CPU a day: "2.0 hours", "37 min", "45 s".
function perDay(ms: number): string {
  const a = Math.abs(ms)
  if (a >= 3_600_000) return `${(a / 3_600_000).toFixed(1)} hours`
  if (a >= 60_000) return `${Math.round(a / 60_000)} min`
  return `${Math.max(1, Math.round(a / 1000))} s`
}

export function PerformanceTrendBlock({ trend: t, serverId, database }: { trend: PerformanceTrend; serverId: number; database: string }) {
  return (
    <div className="grid gap-4">
      <section
        className={cn("grid gap-4 rounded-xl border border-l-4 p-4 min-w-0", verdictTone[t.verdict || "same"])}
        aria-label="Better or worse than usual"
      >
        {t.verdict ? (
          <div className="flex flex-wrap items-baseline gap-2">
            <Badge className={pillTone[t.verdict]}>{t.words}</Badge>
            <p className="text-lg font-semibold">{t.sentence}</p>
          </div>
        ) : (
          <p className="text-muted-foreground">{t.message}</p>
        )}
        {t.figures.length > 0 && (
          <div className="grid gap-3 sm:grid-cols-3">
            {t.figures.map((f) => (
              <div key={f.id} className="grid gap-0.5 rounded-lg border p-3">
                <span className="text-xs text-muted-foreground">{f.label}</span>
                <span className="text-xl font-semibold tabular-nums">{f.value}</span>
                <span className={cn("text-sm font-medium", changeTone[f.tone])}>{f.change}</span>
                <span className="text-xs text-muted-foreground tabular-nums">{f.usual}</span>
              </div>
            ))}
          </div>
        )}
        {t.compared && <p className="text-sm text-muted-foreground">{t.compared}</p>}
        <TrendDays days={t.days} />
      </section>
      <Movers trend={t} serverId={serverId} database={database} />
    </div>
  )
}

function TrendDays({ days }: { days: TrendDay[] }) {
  const most = Math.max(1, ...days.map((d) => d.cpuPerQueryMs)) * 1.15
  return (
    <div className="grid gap-2 min-w-0">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-muted-foreground">CPU per query in business hours, day by day</h3>
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1.5">
            <i className="size-2.5 rounded-sm bg-[#2a78d6] dark:bg-[#3987e5]" aria-hidden />
            working day
          </span>
          <span className="inline-flex items-center gap-1.5">
            <i className="size-2.5 rounded-sm bg-[#c3c2b7] dark:bg-[#383835]" aria-hidden />
            weekend or quiet day (left out)
          </span>
          <span>▲ queries that got a new plan</span>
        </div>
      </div>
      <div className="grid gap-1.5" style={{ gridTemplateColumns: `repeat(${days.length}, minmax(0, 1fr))` }}>
        {days.map((d) => {
          const tip =
            d.kind === "none"
              ? `${d.label}: ${d.note}`
              : `${d.label}: ${d.cpuPerQueryMs} ms of CPU per query, ${Math.round(d.queriesPerHour).toLocaleString()} queries an hour${d.note ? ` (${d.note})` : ""}${d.planChanges ? `; ${d.planChanges} queries got a new plan` : ""}`
          return (
            <div key={d.date} className="grid min-w-0 justify-items-center gap-1" title={tip}>
              <span className="h-4 text-[11px] text-muted-foreground whitespace-nowrap">{d.planChanges > 0 ? `▲ ${d.planChanges}` : ""}</span>
              <div
                className={cn(
                  "relative flex h-24 w-full items-end justify-center",
                  d.kind === "none" && "rounded border border-dashed",
                )}
              >
                {d.kind !== "none" && (
                  <>
                    <span
                      className="absolute text-[10px] text-muted-foreground tabular-nums"
                      style={{ bottom: `calc(${(d.cpuPerQueryMs / most) * 100}% + 2px)` }}
                    >
                      {d.cpuPerQueryMs}
                    </span>
                    <i
                      className={cn(
                        "block w-3/4 rounded-t-sm",
                        d.kind === "weekend" || d.kind === "quiet"
                          ? "bg-[#c3c2b7] dark:bg-[#383835]"
                          : "bg-[#2a78d6] dark:bg-[#3987e5]",
                        d.kind === "judged" && "outline-2 outline-offset-1 outline-[#2a78d6] dark:outline-[#3987e5]",
                      )}
                      style={{ height: `max(2px, ${(d.cpuPerQueryMs / most) * 100}%)` }}
                    />
                  </>
                )}
              </div>
              <span className="text-[11px] text-muted-foreground whitespace-nowrap">{d.label}</span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function Movers({ trend: t, serverId, database }: { trend: PerformanceTrend; serverId: number; database: string }) {
  const [allSlower, setAllSlower] = useState(false)
  const m = t.movers
  if (m.faster === 0 && m.slower === 0) {
    return m.error ? <p className="text-sm text-muted-foreground">What made the difference could not be read: {m.error}</p> : null
  }
  const slower = allSlower ? m.slowerList : m.slowerList.slice(0, 3)
  return (
    <section className="grid gap-4 rounded-xl border p-4 min-w-0" aria-label="What made the difference">
      <div className="grid gap-0.5">
        <h2 className="font-medium">What made the difference</h2>
        <p className="text-sm text-muted-foreground">
          Queries that got a new plan in the last 7 days and ran at least 20 times before and after it, each against itself
          before, so the mix of work cannot fool it.
        </p>
      </div>
      <div className="grid gap-0.5">
        {m.faster > 0 && (
          <p>
            <span className="font-medium text-emerald-700 dark:text-emerald-400">{m.faster} got faster</span>: together they
            save <span className="font-medium">{perDay(m.savedMsPerDay)} of CPU a day</span>.
          </p>
        )}
        {m.slower > 0 && (
          <p>
            <span className="font-medium text-red-700 dark:text-red-400">{m.slower} got slower</span>: together they cost{" "}
            <span className="font-medium">{perDay(m.addedMsPerDay)} of CPU a day</span> more.
          </p>
        )}
      </div>
      {m.slowerList.length > 0 && (
        <div className="grid gap-2">
          <h3 className="text-sm font-medium text-muted-foreground">Got slower</h3>
          {slower.map((x) => (
            <MoverRow key={x.queryHash} m={x} serverId={serverId} database={database} />
          ))}
          {m.slowerList.length > 3 && (
            <button type="button" className="justify-self-start text-sm text-primary hover:underline" onClick={() => setAllSlower((v) => !v)}>
              {allSlower ? "Show fewer" : `Show all ${m.slowerList.length}`}
            </button>
          )}
        </div>
      )}
      {m.fasterList.length > 0 && (
        <div className="grid gap-2">
          <h3 className="text-sm font-medium text-muted-foreground">
            Got faster{m.faster > m.fasterList.length ? `: the ${m.fasterList.length} that save the most` : ""}
          </h3>
          {m.fasterList.map((x) => (
            <MoverRow key={x.queryHash} m={x} serverId={serverId} database={database} />
          ))}
        </div>
      )}
    </section>
  )
}

function MoverRow({ m, serverId, database }: { m: TrendMover; serverId: number; database: string }) {
  const slower = m.cpuMsPerDay > 0
  const ratio = m.nowCpuMs > 0 && m.beforeCpuMs > 0 ? (slower ? m.nowCpuMs / m.beforeCpuMs : 0) : 0
  return (
    <div className="grid gap-1 rounded-lg border p-3 min-w-0 sm:grid-cols-[1fr_auto]">
      <p className="min-w-0 break-words font-medium">
        {m.objectName || "An ad hoc query"}{" "}
        <code className="font-mono text-xs font-normal text-muted-foreground">{m.text.length > 90 ? `${m.text.slice(0, 90)}…` : m.text}</code>
      </p>
      <div className="flex items-center gap-2 sm:justify-end">
        <span
          className={cn(
            "whitespace-nowrap text-sm font-medium tabular-nums",
            slower ? "text-red-700 dark:text-red-400" : "text-emerald-700 dark:text-emerald-400",
          )}
        >
          {slower ? "+" : "−"}
          {perDay(m.cpuMsPerDay)} of CPU a day
        </span>
        <Button asChild variant="outline" size="sm">
          <Link to={queryPath(serverId, database, m.queryHash)}>Investigate</Link>
        </Button>
      </div>
      <p className="text-sm text-muted-foreground tabular-nums sm:col-span-2">
        since {formatWhen(m.since, "row")} · {formatMs(m.beforeCpuMs)} → {formatMs(m.nowCpuMs)} of CPU a run
        {ratio >= 2 ? ` (${Math.round(ratio)}x)` : ""} · {m.nowRuns.toLocaleString()} runs since
      </p>
      <p className="text-xs text-muted-foreground sm:col-span-2">
        {m.after && m.afterAt ? `after: ${m.after}, ${formatWhen(m.afterAt, "row")}` : "no change seen just before it"}
      </p>
    </div>
  )
}
