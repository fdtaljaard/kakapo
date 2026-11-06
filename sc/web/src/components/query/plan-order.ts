import type { QueryDetail, QueryPlan } from "@/api-queries"
import { serverWhen, type ServerClock } from "@/components/query/format"

// Phase 5j: which plans the query page shows and in which colour, shared by the chart and the timeline.

export function chartPlans(d: QueryDetail): { inUse: number; costliest: number } {
  const inUse = d.plans.find((p) => p.active && p.source === "query-store")?.planId ?? 0
  let costliest = 0
  let most = -1
  for (const p of d.plans) {
    const cpu = p.avgCpuMs * p.runs
    if (p.planId > 0 && p.planId !== inUse && cpu > most) {
      most = cpu
      costliest = p.planId
    }
  }
  return { inUse, costliest }
}


const MANY = 5

export function whenPlan(c: ServerClock, p: QueryPlan): string {
  if (p.active && p.firstRun) return `since ${serverWhen(c, p.firstRun, "row")}`
  const to = p.lastRun ? serverWhen(c, p.lastRun, "row") : ""
  return p.firstRun ? `${serverWhen(c, p.firstRun, "row")} to ${to}` : `to ${to}`
}

export function shownPlans(d: QueryDetail): QueryPlan[] {
  return d.plans
    .filter((p) => p.source === "query-store" && p.runs > 0 && (p.runs >= MANY || p.active))
    .sort((a, b) => (a.firstRun ?? "").localeCompare(b.firstRun ?? ""))
}

