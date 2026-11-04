import type { TuningChange, TuningHour } from "@/api-tuning"
import { cn } from "@/lib/utils"

import { formatCount, serverClock, serverDate, serverDay, serverWhen, tuningMs } from "./format"

// "Hour by hour": CPU per execution per hour as plain CSS bars (no chart library), executions as a
// second row; nights and weekends shaded, Before and Now coloured, the hour in progress hollow, and every
// change a line with its object. The bars share the width, so a month still fits a phone without
// scrolling sideways.

const barColour = (h: TuningHour) =>
  h.window === "now" ? "bg-emerald-600/80" : h.window === "before" ? "bg-sky-600/70" : h.change ? "bg-amber-500/80" : "bg-foreground/25"

function Bars({
  hours,
  value,
  height,
  label,
}: {
  hours: TuningHour[]
  value: (h: TuningHour) => number
  height: string
  label: (h: TuningHour) => string
}) {
  const most = Math.max(...hours.map(value), 0) || 1
  return (
    <div className={cn("flex items-end border-b border-foreground/15", height, hours.length <= 96 && "gap-px")}>
      {hours.map((h) => {
        const v = value(h)
        return (
          <div
            key={h.hour}
            title={label(h)}
            className={cn("relative flex h-full min-w-0 flex-1 items-end", h.class !== "business" && "bg-muted")}
          >
            {!h.noData && v > 0 && (
              <div
                className={cn("w-full rounded-t-[1px]", h.inProgress ? "border border-b-0 border-foreground/50 bg-transparent" : barColour(h))}
                style={{ height: `${Math.max(2, (v / most) * 100)}%` }}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

export function HourChart({ hours, changes }: { hours: TuningHour[]; changes: TuningChange[] }) {
  if (hours.length === 0) {
    return <p className="rounded-xl border border-dashed p-4 text-muted-foreground">Query Store holds no hours in this window.</p>
  }
  const first = serverDate(hours[0].hour).getTime()
  const span = hours.length * 3_600_000
  const at = (s: string) => ((serverDate(s).getTime() - first) / span) * 100
  const markers = changes.filter((c) => {
    const p = at(c.at)
    return p >= 0 && p <= 100
  })
  // A label under the bars at each midnight, thinned out so they never crowd a phone.
  const midnights = hours.filter((h) => serverDate(h.hour).getHours() === 0)
  const step = Math.max(1, Math.ceil(midnights.length / 6))
  const tip = (h: TuningHour) =>
    h.noData
      ? `${serverWhen(h.hour)}${h.inProgress ? " (in progress)" : ""}: no queries ran`
      : `${serverWhen(h.hour)}${h.inProgress ? " (in progress)" : ""}${h.change ? " (change)" : ""}: ${tuningMs(h.cpuPerExecMs)} per execution, ${formatCount(h.executions)} executions`

  return (
    <div className="grid gap-2 min-w-0">
      <div className={cn("relative grid gap-1", markers.length > 1 ? "pt-9" : "pt-5")}>
        {markers.map((c, i) => {
          const p = at(c.at)
          return (
            <div key={c.id} className="pointer-events-none absolute inset-y-0" style={{ left: `${p}%` }} aria-hidden>
              <div className="absolute inset-y-0 w-px bg-amber-600" />
              <span
                className={cn(
                  "absolute max-w-28 truncate rounded bg-amber-100 px-1 text-[0.65rem] leading-4 text-amber-900 dark:bg-amber-900/60 dark:text-amber-100",
                  p > 60 ? "right-0" : "left-0",
                )}
                style={{ top: `${(i % 2) * 16}px` }}
              >
                {c.object}
              </span>
            </div>
          )
        })}
        <span className="text-xs text-muted-foreground">CPU per execution</span>
        <Bars hours={hours} value={(h) => h.cpuPerExecMs} height="h-28" label={tip} />
        <span className="mt-1 text-xs text-muted-foreground">Executions</span>
        <Bars hours={hours} value={(h) => h.executions} height="h-12" label={tip} />
      </div>
      <div className="relative h-4 text-[0.65rem] text-muted-foreground" aria-hidden>
        {midnights
          .filter((h, i) => i % step === 0 && at(h.hour) < 90)
          .map((h) => (
            <span key={h.hour} className="absolute whitespace-nowrap" style={{ left: `${at(h.hour)}%` }}>
              {serverDay(h.hour).replace(/ \w+$/, "")}
            </span>
          ))}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <Legend className="bg-sky-600/70" text="Before" />
        <Legend className="bg-emerald-600/80" text="Now" />
        <Legend className="bg-muted ring-1 ring-foreground/10" text="Night or weekend" />
        <Legend className="w-px bg-amber-600" text="A change (never compared)" />
        <Legend className="border border-foreground/50" text="In progress" />
        <Legend className="border-b border-foreground/30" text="Idle (no queries)" />
      </ul>
      <p className="sr-only">
        {hours.length} hours from {serverWhen(hours[0].hour)} to {serverClock(hours[hours.length - 1].hour)}.
      </p>
    </div>
  )
}

function Legend({ className, text }: { className: string; text: string }) {
  return (
    <li className="flex items-center gap-1.5">
      <span className={cn("inline-block h-3 w-3 rounded-[2px]", className)} />
      {text}
    </li>
  )
}
