import type { QueryDetail, QueryEvent, QueryHour } from "@/api-queries"
import { chartPlans } from "@/components/query/plan-order"
import { formatMs, formatWhole, serverWhen } from "@/components/query/format"

// Phase 5j, "Over time": one bar per hour of the window. Top row: the CPU the query used, coloured by
// plan (the plan in use blue, the plan that cost the most orange, any other light blue). Bottom row:
// how many times it ran. Statistics updates are marked with a dashed line. Hover a bar for its hour.
// Drawn with plain elements, as the load strip is; no chart library.

const HOUR = 3_600_000
const IN_USE = "bg-[#2a78d6] dark:bg-[#3987e5]"
const COSTLIEST = "bg-[#ec835a] dark:bg-[#e0784f]"
const OTHER = "bg-[#86b6ef] dark:bg-[#1c5cab]"
const RUNS = "bg-[#b5b4ac] dark:bg-[#55544f]"

function barColour(h: QueryHour, inUse: number, costliest: number): string {
  if (costliest > 0 && h.plans.includes(costliest)) return COSTLIEST
  if (inUse > 0 && h.plans.includes(inUse)) return IN_USE
  return OTHER
}

function eventLabel(e: QueryEvent): string {
  return e.text.startsWith("Index ") ? "index" : "statistics"
}

export function HistoryChart({ detail: d }: { detail: QueryDetail }) {
  if (d.history.length === 0) return null
  const byHour = new Map(d.history.map((h) => [new Date(h.hour).getTime(), h]))
  const first = Math.min(...byHour.keys())
  const last = Math.max(Math.floor(new Date(d.capturedAt).getTime() / HOUR) * HOUR, ...byHour.keys())
  const hours: number[] = []
  for (let t = first; t <= last; t += HOUR) hours.push(t)
  const span = hours.length * HOUR
  const maxCpu = Math.max(1, ...d.history.map((h) => h.cpuMs))
  const maxRuns = Math.max(1, ...d.history.map((h) => h.runs))
  const { inUse, costliest } = chartPlans(d)
  const at = (iso: string) => ((new Date(iso).getTime() - first) / span) * 100

  // Day starts in the browser's clock, labelled "Mon 2".
  const days = hours.filter((t) => new Date(t).getHours() === 0)
  const dayName = new Intl.DateTimeFormat(undefined, { weekday: "short", day: "numeric" })

  // Statistics updates as markers; a label only when it does not crowd the one before.
  const marks = d.events.filter((e) => e.kind === "statistics" && new Date(e.at).getTime() >= first)
  const labelled = marks.reduce<{ e: QueryEvent; x: number; show: boolean; last: number }[]>((out, e) => {
    const x = at(e.at)
    const last = out.length > 0 ? out[out.length - 1].last : -100
    const show = x - last >= 8
    out.push({ e, x, show, last: show ? x : last })
    return out
  }, [])

  const tip = (h: QueryHour) =>
    `${serverWhen(d, h.hour, "row")}: ${formatWhole(h.runs)} runs, ${formatMs(h.avgDurationMs)} a run, ${formatMs(h.cpuMs)} of CPU, plan ${h.plans.join(", ")}`

  return (
    <div className="grid gap-2 min-w-0">
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {costliest > 0 && (
          <span className="inline-flex items-center gap-1.5">
            <i className={`size-2.5 rounded-sm ${COSTLIEST}`} aria-hidden />
            plan {costliest}, which cost the most
          </span>
        )}
        {inUse > 0 && (
          <span className="inline-flex items-center gap-1.5">
            <i className={`size-2.5 rounded-sm ${IN_USE}`} aria-hidden />
            plan {inUse}, in use
          </span>
        )}
        <span className="inline-flex items-center gap-1.5">
          <i className={`size-2.5 rounded-sm ${OTHER}`} aria-hidden />
          other plans
        </span>
        <span className="inline-flex items-center gap-1.5">
          <i className={`size-2.5 rounded-sm ${RUNS}`} aria-hidden />
          runs
        </span>
      </div>
      <div className="relative pt-4">
        {labelled.map(({ e, x, show }) => (
          <div
            key={`${e.at}${e.text}`}
            className="absolute top-0 bottom-5 border-l border-dashed border-muted-foreground/60"
            style={{ left: `${x}%` }}
            title={`${serverWhen(d, e.at, "row")}: ${e.text}`}
          >
            {show && <span className="absolute -top-0.5 left-1 whitespace-nowrap text-[11px] text-muted-foreground">{eventLabel(e)}</span>}
          </div>
        ))}
        <div className="flex h-28 items-end border-b" aria-label="CPU per hour">
          {hours.map((t) => {
            const h = byHour.get(t)
            return (
              <div key={t} className="flex h-full min-w-0 flex-1 items-end px-[0.5px]" title={h ? tip(h) : undefined}>
                {h && h.cpuMs > 0 && (
                  <div className={`w-full rounded-t-[1px] ${barColour(h, inUse, costliest)}`} style={{ height: `max(1px, ${(h.cpuMs / maxCpu) * 100}%)` }} />
                )}
              </div>
            )
          })}
        </div>
        <div className="mt-2 flex h-8 items-end" aria-label="Runs per hour">
          {hours.map((t) => {
            const h = byHour.get(t)
            return (
              <div key={t} className="flex h-full min-w-0 flex-1 items-end px-[0.5px]" title={h ? tip(h) : undefined}>
                {h && h.runs > 0 && <div className={`w-full rounded-t-[1px] ${RUNS}`} style={{ height: `${(h.runs / maxRuns) * 100}%` }} />}
              </div>
            )
          })}
        </div>
        <div className="relative h-5">
          {days.map((t) => (
            <span key={t} className="absolute top-1 whitespace-nowrap text-[11px] text-muted-foreground" style={{ left: `${((t - first) / span) * 100}%` }}>
              {dayName.format(new Date(t))}
            </span>
          ))}
        </div>
      </div>
      <p className="text-xs text-muted-foreground">Busiest hour: {formatMs(maxCpu)} of CPU. Most runs in an hour: {formatWhole(maxRuns)}.</p>
    </div>
  )
}
