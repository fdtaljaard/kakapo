import { useState } from "react"
import { Wrench } from "lucide-react"

import type { RedFlags } from "@/api"
import { loadDayChoices, withLive, type Load, type LoadDays, type LoadHour, type LoadMeasure } from "@/api-load"
import type { QueryAction } from "@/api-queries"
import { FixDialog } from "@/components/fix-dialog"
import { RedFlagList } from "@/components/red-flags"
import { serverClock } from "@/components/tuning/format"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { cn } from "@/lib/utils"

// The load strip and the red flags at the top of the database page (docs/phase5i-contract.md): the
// load now against the usual range at this hour, today hour by hour against the selected days, and the
// red flags. One card, about 200 px high without flags; it refreshes every minute (useLoad).

// "0.15", "1.7", "12"
function decimal(v: number): string {
  return v < 1 ? v.toFixed(2) : v < 10 ? v.toFixed(1) : Math.round(v).toString()
}

const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 0 })
// "171k", "600"
const count = (v: number) => compact.format(v).toLowerCase()

function usual(m: LoadMeasure, fmt: (v: number) => string): string {
  return m.low !== null && m.high !== null ? `usual ${fmt(m.low)}–${fmt(m.high)}` : ""
}

function Figure({ m, fmt, unit, title }: { m: LoadMeasure; fmt: (v: number) => string; unit: string; title?: string }) {
  const u = usual(m, fmt)
  return (
    <span className="inline-flex items-baseline gap-1.5" title={title}>
      <b className="text-base font-semibold">{m.value === null ? "—" : fmt(m.value)}</b>
      <span>{unit}</span>
      {u && <span className="text-xs text-muted-foreground">{u}</span>}
    </span>
  )
}

export function LoadStrip({
  serverId,
  database,
  days,
  onDays,
  load,
  loading,
  error,
  stale,
  overviewFlags,
  onApplied,
}: {
  serverId: number
  database: string
  days: LoadDays
  onDays: (d: LoadDays) => void
  load: Load | null
  loading: boolean
  error: string | null
  stale: boolean
  overviewFlags: RedFlags | null
  onApplied: () => void
}) {
  const [fixing, setFixing] = useState<QueryAction | null>(null)
  // The live ACT01 and PRS01 flags in place of the overview's; the rest stay until Check again.
  const flags = overviewFlags && load && !load.liveError ? withLive(overviewFlags, load.liveFlags) : overviewFlags

  return (
    <Card size="sm" className="grid gap-2.5 px-4 py-3" aria-label="Load and red flags">
      <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1.5 text-sm">
        <span className="font-semibold">Load now</span>
        {load && !load.message ? (
          <>
            <Figure
              m={load.now.cores}
              fmt={decimal}
              unit={load.now.cpuCount > 0 ? `of ${load.now.cpuCount} cores` : "cores busy"}
              title="CPU this database's queries used over the last 15 to 30 minutes"
            />
            <Figure m={load.now.queries} fmt={count} unit="queries an hour" />
            {load.now.threads && (
              <Figure m={load.now.threads} fmt={decimal} unit="threads waiting" title="Threads waiting on average, from Query Store's waits" />
            )}
          </>
        ) : load?.message ? (
          <span className="text-muted-foreground">{load.message}</span>
        ) : error ? (
          <span className="text-muted-foreground break-words">Could not read the load: {error}</span>
        ) : (
          loading && <Skeleton className="h-5 w-72" />
        )}
        {load?.action && (
          <Button variant="outline" size="sm" className="h-7" onClick={() => setFixing(load.action ?? null)}>
            <Wrench />
            {load.action.label}
          </Button>
        )}
        <span className="flex-1" />
        {load && (
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {stale ? (
              <>not refreshed since {serverClock(load.serverNow)}</>
            ) : (
              <>
                <span className="size-[7px] rounded-full bg-[#0ca30c] motion-safe:animate-pulse" aria-hidden />
                {load.now.showSince && load.now.since ? `since ${serverClock(load.now.since)}, ` : ""}
                {serverClock(load.serverNow)} · every minute
              </>
            )}
          </span>
        )}
        <div className="inline-flex overflow-hidden rounded-lg border" role="group" aria-label="Compare with">
          {loadDayChoices.map((d) => (
            <button
              key={d}
              type="button"
              aria-pressed={d === days}
              onClick={() => onDays(d)}
              className={cn(
                "px-2.5 py-0.5 text-xs",
                d === days ? "bg-foreground text-background" : "text-muted-foreground hover:bg-muted",
              )}
            >
              {d} days
            </button>
          ))}
        </div>
      </div>

      {load?.chart ? <TodayChart load={load} /> : !load && loading ? <Skeleton className="h-[84px]" /> : null}
      {load && load.notes.length > 0 && <p className="text-xs text-muted-foreground break-words">{load.notes.join(" ")}</p>}

      <div className="border-t pt-2.5">
        <RedFlagList flags={flags} serverId={serverId} database={database} />
      </div>

      {fixing && (
        <FixDialog
          serverId={serverId}
          database={database}
          fixId={fixing.fixId}
          title={fixing.title}
          options={fixing.options}
          onClose={() => setFixing(null)}
          onApplied={onApplied}
        />
      )}
    </Card>
  )
}

// Today against the selected days: 24 plain CSS columns (as the tuning report's HourChart, no chart
// library). In each, the usual range as a grey band with the median as a hairline, and today's cores
// busy on top: full blue above the band, light blue inside or below it, hollow while in progress; hours
// to come show the band only. A dashed line marks now. Colours validated with the dataviz skill.
function TodayChart({ load }: { load: Load }) {
  const hours = load.hours
  const top =
    Math.max(
      0.01,
      ...hours.map((h) => h.cores.high ?? 0),
      ...hours.map((h) => h.cores.value ?? 0),
    ) * 1.08
  const pct = (v: number) => `${Math.min(100, (v / top) * 100)}%`
  const now = new Date(load.serverNow)
  const nowAt = ((now.getHours() + now.getMinutes() / 60) / 24) * 100
  const band = load.daysCompared >= 2
  const kind = load.kind === "weekend" ? "weekend days" : "working days"

  return (
    <div className="grid gap-1 min-w-0">
      <div className="relative flex h-16 items-stretch border-b border-foreground/25">
        {hours.map((h) => (
          <div key={h.hour} className="relative min-w-0 flex-1" title={tip(h)}>
            {h.cores.low !== null && h.cores.high !== null && (
              <div
                className="absolute inset-x-0 bg-[#ecebe6] dark:bg-[#2a2a28]"
                style={{ bottom: pct(h.cores.low), height: `max(1px, ${pct(h.cores.high - h.cores.low)})` }}
              />
            )}
            {h.cores.median !== null && (
              <div className="absolute inset-x-0 border-t border-[#a3a29c] dark:border-[#6b6a65]" style={{ bottom: pct(h.cores.median) }} />
            )}
            {h.cores.value !== null && h.cores.value > 0 && (
              <div
                className={cn(
                  "absolute inset-x-px bottom-0 rounded-t-[2px]",
                  h.inProgress
                    ? "border-[1.5px] border-b-0 border-[#2a78d6] bg-transparent dark:border-[#3987e5]"
                    : h.above
                      ? "bg-[#2a78d6] dark:bg-[#3987e5]"
                      : "bg-[#86b6ef] dark:bg-[#1c5cab]",
                )}
                style={{ height: `max(1px, ${pct(h.cores.value)})` }}
              />
            )}
          </div>
        ))}
        <div className="pointer-events-none absolute inset-y-0 border-l border-dashed border-muted-foreground" style={{ left: `${nowAt}%` }} aria-hidden />
      </div>
      <div className="relative h-4 text-[0.7rem] text-muted-foreground" aria-hidden>
        {[0, 6, 12, 18].map((h) => (
          <span key={h} className={cn("absolute top-0.5 whitespace-nowrap", h > 0 && "-translate-x-1/2")} style={{ left: `${(h / 24) * 100}%` }}>
            {String(h).padStart(2, "0")}:00
          </span>
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-muted-foreground">
        <Key className="bg-[#86b6ef] dark:bg-[#1c5cab]" text="today" />
        <Key className="bg-[#2a78d6] dark:bg-[#3987e5]" text="today, above the usual range" />
        {band ? <Key className="bg-[#ecebe6] dark:bg-[#2a2a28]" text={load.key} /> : <li>{load.key}</li>}
      </ul>
      <p className="sr-only">
        Cores busy by this database's queries, hour by hour today, against the {kind} compared.
      </p>
    </div>
  )
}

// "10:00: 1.7 cores busy · usual 0.8–1.2, median 1.0"
function tip(h: LoadHour): string {
  const hh = `${String(h.hour).padStart(2, "0")}:00`
  const c = h.cores
  const range = c.low !== null && c.high !== null ? `usual ${decimal(c.low)}–${decimal(c.high)}` : ""
  const med = c.median !== null ? `, median ${decimal(c.median)}` : ""
  if (h.future || c.value === null) return `${hh}: ${h.future ? "not yet" : "nothing recorded"}${range ? ` · ${range} cores` : ""}`
  return `${hh}${h.inProgress ? " (so far)" : ""}: ${decimal(c.value)} cores busy${range ? ` · ${range}${med}` : ""}`
}

function Key({ className, text }: { className: string; text: string }) {
  return (
    <li className="flex items-center gap-1.5">
      <span className={cn("inline-block h-[9px] w-[11px] rounded-[2px]", className)} />
      {text}
    </li>
  )
}
