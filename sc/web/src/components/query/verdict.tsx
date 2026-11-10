import type { QueryDetail, QueryEvent, QueryPlan, QueryVerdict } from "@/api-queries"
import { formatMs, formatWhole, serverWhen, type ServerClock } from "@/components/query/format"
import { chartPlans, shownPlans, whenPlan } from "@/components/query/plan-order"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"

// Phase 5j (docs/phase5j-contract.md): the top of the query page answers whether it still needs
// attention: the verdict word and one sentence, before and now side by side, what changed, what to do.

const words: Record<QueryVerdict["verdict"], string> = {
  faster: "Faster",
  slower: "Slower",
  same: "No change",
  "too few": "Too few runs",
}

const tones: Record<QueryVerdict["verdict"], string> = {
  faster: "border-l-emerald-600 dark:border-l-emerald-500",
  slower: "border-l-red-600 dark:border-l-red-500",
  same: "border-l-amber-500",
  "too few": "border-l-muted-foreground/40",
}

const pills: Record<QueryVerdict["verdict"], string> = {
  faster: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  slower: "bg-red-500/15 text-red-700 dark:text-red-400",
  same: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  "too few": "bg-muted text-muted-foreground",
}

// Pages of 8 KB as "2.4 million pages (18.5 GB)".
function pages(n: number): string {
  const mb = (n * 8) / 1024
  const size = mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${formatWhole(mb)} MB`
  const count = n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)} million` : formatWhole(n)
  return `${count} pages (${size})`
}

// A total of CPU or time: hours from two hours up ("6.8 hours"), else as a run's time.
function total(ms: number): string {
  return ms >= 7_200_000 ? `${(ms / 3_600_000).toFixed(1)} hours` : formatMs(ms)
}

export function VerdictCard({ verdict: v, text, clock }: { verdict: QueryVerdict; text: string; clock: ServerClock }) {
  const rows: [string, string, string][] = [
    ["Time per run", formatMs(v.before.avgDurationMs), formatMs(v.now.avgDurationMs)],
    ["CPU per run", formatMs(v.before.avgCpuMs), formatMs(v.now.avgCpuMs)],
    ["Data read per run", pages(v.before.avgReads), pages(v.now.avgReads)],
    ["Runs", formatWhole(v.before.runs), formatWhole(v.now.runs)],
    ["CPU in total", total(v.before.cpuMs), total(v.now.cpuMs)],
  ]
  return (
    <section className={cn("grid gap-4 rounded-xl border border-l-4 p-4 min-w-0", tones[v.verdict])} aria-label="Is it still a problem?">
      <div className="flex flex-wrap items-baseline gap-2">
        <Badge className={pills[v.verdict]}>{words[v.verdict]}</Badge>
        <p className="text-lg font-semibold">{v.headline}</p>
      </div>
      {v.verdict !== "too few" && (
        <div className="overflow-x-auto">
          <table className="w-full max-w-2xl text-sm tabular-nums">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="py-1 pr-3 font-normal" />
                <th className="py-1 pr-3 font-normal">Before ({v.before.label})</th>
                <th className="py-1 font-normal">Now ({v.now.label})</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(([label, before, now]) => (
                <tr key={label} className="border-t">
                  <td className="py-1.5 pr-3 text-muted-foreground">{label}</td>
                  <td className="py-1.5 pr-3">{before}</td>
                  <td className="py-1.5 font-medium">{now}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <dl className="grid gap-x-4 gap-y-2 sm:grid-cols-[8rem_1fr]">
        {v.changed.length > 0 && (
          <>
            <dt className="text-muted-foreground">What changed</dt>
            <dd>
              <ul className="grid gap-1">
                {v.changed.map((e) => (
                  <li key={`${e.at}${e.text}`} className="break-words">
                    <span className="tabular-nums text-muted-foreground">{serverWhen(clock, e.at, "row")}</span> {e.text}
                  </li>
                ))}
              </ul>
            </dd>
          </>
        )}
        <dt className="text-muted-foreground">What to do</dt>
        <dd className="break-words">{v.toDo}</dd>
      </dl>
      {text && (
        <pre className="whitespace-pre-wrap break-words rounded-lg bg-muted px-3 py-2 font-mono text-xs">{text}</pre>
      )}
    </section>
  )
}

// "The plans SQL Server used": the plans that ran at least five times (and the one in use), in the order
// they began, with the statistics updates between them; what each plan's XML says about it.
export function PlanTimeline({ detail: d }: { detail: QueryDetail }) {
  const plans = shownPlans(d)
  if (plans.length === 0) return null
  type Step = { at: string; plan?: QueryPlan; event?: QueryEvent }
  const steps: Step[] = [
    ...plans.map((p) => ({ at: p.firstRun ?? "", plan: p })),
    ...d.events
      .filter((e) => (e.kind === "statistics" || e.kind === "change") && plans.some((p) => p.firstRun && p.firstRun < e.at))
      .map((e) => ({ at: e.at, event: e })),
  ].sort((a, b) => a.at.localeCompare(b.at))
  const { inUse, costliest } = chartPlans(d)
  // The first statistics update and the first index say what they are, once each.
  const firstOf = (isIndex: boolean) =>
    steps.find((x) => x.event && x.event.kind === "statistics" && x.event.text.startsWith("Index ") === isIndex)
  return (
    <ol className="relative grid gap-0 pl-5 before:absolute before:left-[5px] before:top-3 before:bottom-3 before:w-0.5 before:bg-border">
      {steps.map((s) => {
        if (s.event) {
          const isIndex = s.event.text.startsWith("Index ")
          const say = firstOf(isIndex) === s
          return (
            <li key={`${s.at}${s.event.text}`} className="relative py-2 text-sm text-muted-foreground">
              <span className="absolute -left-[18px] top-3 size-2.5 rounded-full border-2 border-muted-foreground bg-background" aria-hidden />
              <span className="tabular-nums">{serverWhen(d, s.event.at, "row")}</span> {s.event.text}
              {say && (
                <span className="block text-xs">
                  {isIndex
                    ? "An index lets SQL Server find rows without reading the whole table."
                    : "Statistics are SQL Server's summary of what is in a table; it uses them to choose a plan."}
                </span>
              )}
            </li>
          )
        }
        const p = s.plan!
        const said = p.warnings.length > 0 ? p.warnings.join("; ") : p.active ? "nothing: no warnings in its plan." : ""
        return (
          <li key={p.planId} className="relative py-2">
            <span
              className={cn(
                "absolute -left-[19px] top-4 size-3 rounded-full border-2 border-background",
                p.planId === inUse ? "bg-[#2a78d6] dark:bg-[#3987e5]" : p.planId === costliest ? "bg-[#ec835a] dark:bg-[#e0784f]" : "bg-[#86b6ef] dark:bg-[#1c5cab]",
              )}
              aria-hidden
            />
            <div className="grid gap-1 rounded-xl border p-3 min-w-0">
              <div className="flex flex-wrap items-baseline gap-2">
                <span className="font-medium">Plan {p.planId}</span>
                {p.active && <Badge className="bg-primary/10 text-primary">in use</Badge>}
                {p.forced && <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">forced</Badge>}
                <span className="text-xs text-muted-foreground">{whenPlan(d, p)}</span>
              </div>
              <p className="text-sm text-muted-foreground tabular-nums">
                {formatWhole(p.runs)} runs · {formatMs(p.avgDurationMs)} each · {formatMs(p.avgCpuMs)} of CPU · {formatWhole(p.avgReads)} pages read
              </p>
              {said && <p className="text-sm break-words">SQL Server said: {said}</p>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}
