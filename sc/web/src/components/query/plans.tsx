import { Download, TriangleAlert } from "lucide-react"

import type { QueryAction, QueryDetail, QueryPlan } from "@/api-queries"
import { ActionButton } from "@/components/query/action-button"
import { formatMs, formatWhole, plainColumns } from "@/components/query/format"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen } from "@/lib/format"

function planLabel(p: QueryPlan): string {
  return p.planId > 0 ? `Plan ${p.planId}` : "Cached plan"
}

function PlanBadges({ plan: p }: { plan: QueryPlan }) {
  return (
    <span className="flex flex-wrap gap-1">
      {p.active && <Badge className="bg-primary/10 text-primary">in use</Badge>}
      {p.forced && <Badge className="bg-amber-500/15 text-amber-700 dark:text-amber-400">forced</Badge>}
      {p.fastest && <Badge className="bg-emerald-500/15 text-emerald-700 dark:text-emerald-400">fastest</Badge>}
      {p.source === "plan-cache" && <Badge variant="outline">plan cache only</Badge>}
    </span>
  )
}

// Save .sqlplan: the plan XML as a file SSMS opens as a graphical plan (no plan viewer in kakapo).
function savePlan(p: QueryPlan, hash: string) {
  if (!p.planXml) return
  const url = URL.createObjectURL(new Blob([p.planXml], { type: "application/xml" }))
  const a = document.createElement("a")
  a.href = url
  a.download = `${hash}_${p.planId > 0 ? `plan${p.planId}` : "cached"}.sqlplan`
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// "Plans": one row per plan, the one in use first, forced and fastest marked; the actions sit under the
// plan's name so the table fits a phone (the less important columns appear from wider screens).
export function PlansTable({
  detail,
  onAction,
}: {
  detail: QueryDetail
  onAction: (a: QueryAction) => void
}) {
  if (detail.plans.length === 0) {
    return <p className="rounded-xl border border-dashed p-4 text-muted-foreground">No plan of this query was found.</p>
  }
  return (
    <div className="rounded-xl border">
      <Table className="text-xs sm:text-sm">
        <TableHeader>
          <TableRow>
            <TableHead>Plan</TableHead>
            <TableHead className="text-right">Runs</TableHead>
            <TableHead className="text-right">Avg time</TableHead>
            <TableHead className="hidden sm:table-cell text-right">Slowest</TableHead>
            <TableHead className="hidden md:table-cell text-right">Avg CPU</TableHead>
            <TableHead className="hidden sm:table-cell text-right">Avg reads</TableHead>
            <TableHead className="hidden lg:table-cell">Last run</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {detail.plans.map((p) => (
            <TableRow key={`${p.planId}/${p.planHandle}`} className="hover:bg-transparent">
              <TableCell className="align-top whitespace-normal">
                <div className="grid gap-1.5 min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="font-medium">{planLabel(p)}</span>
                    <PlanBadges plan={p} />
                  </div>
                  <span className="text-muted-foreground sm:hidden">
                    slowest {formatMs(p.maxDurationMs)}, {formatWhole(p.avgReads)} reads
                  </span>
                  {p.forceFailures > 0 && (
                    <span className="flex items-start gap-1 text-amber-700 dark:text-amber-400">
                      <TriangleAlert className="size-3.5 shrink-0 translate-y-0.5" />
                      forcing failed {p.forceFailures}× ({p.forceFailureReason.toLowerCase() || "no reason given"})
                    </span>
                  )}
                  {(p.actions.length > 0 || p.planXml) && (
                    <div className="flex flex-wrap gap-1.5">
                      {p.actions.map((a) => (
                        <ActionButton key={a.fixId} action={a} onAction={onAction} />
                      ))}
                      {p.planXml && (
                        <Button variant="ghost" size="sm" onClick={() => savePlan(p, detail.queryHash)}>
                          <Download />
                          Save .sqlplan
                        </Button>
                      )}
                    </div>
                  )}
                </div>
              </TableCell>
              <TableCell className="align-top text-right tabular-nums">{formatWhole(p.runs)}</TableCell>
              <TableCell className="align-top text-right tabular-nums whitespace-nowrap">{formatMs(p.avgDurationMs)}</TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {formatMs(p.maxDurationMs)}
              </TableCell>
              <TableCell className="hidden md:table-cell align-top text-right tabular-nums whitespace-nowrap">
                {formatMs(p.avgCpuMs)}
              </TableCell>
              <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{formatWhole(p.avgReads)}</TableCell>
              <TableCell className="hidden lg:table-cell align-top whitespace-nowrap">
                {p.lastRun ? formatWhen(p.lastRun, "row") : "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function hasSomethingToSay(p: QueryPlan): boolean {
  return p.conversion !== "" || p.warnings.length > 0 || p.parameters.length > 0 || p.missingIndexes.length > 0 || p.planTooBig
}

const unescape = (s: string) => s.replace(/&gt;/g, ">").replace(/&lt;/g, "<").replace(/&amp;/g, "&")

// "What the plan says", per plan that says something, the plan in use first: the warnings in words,
// "Compiled with" the parameters, and "SQL Server suggests an index" with the create-index action.
export function PlanSays({ detail, onAction }: { detail: QueryDetail; onAction: (a: QueryAction) => void }) {
  const plans = detail.plans.filter(hasSomethingToSay)
  if (plans.length === 0) {
    return (
      <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
        The plans read here carry no warnings, parameters or index suggestions.
      </p>
    )
  }
  return (
    <div className="grid gap-3">
      {plans.map((p) => (
        <div key={`${p.planId}/${p.planHandle}`} className="grid gap-3 rounded-xl border p-3 sm:p-4 min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{planLabel(p)}</span>
            <PlanBadges plan={p} />
          </div>
          {p.planTooBig && <p className="text-muted-foreground">The plan is too large to inspect here (over 2 MB): save it from SSMS.</p>}
          {(p.conversion || p.warnings.length > 0) && (
            <ul className="list-disc pl-5 grid gap-1">
              {p.conversion && (
                <li className="break-words">
                  An implicit conversion that can stop SQL Server using an index:{" "}
                  <code className="font-mono text-xs break-all">{unescape(p.conversion)}</code>
                </li>
              )}
              {p.warnings
                .filter((w) => p.missingIndexes.length === 0 || !w.startsWith("SQL Server suggests an index"))
                .map((w) => (
                <li key={w} className="break-words">
                  {w}
                </li>
              ))}
            </ul>
          )}
          {p.parameters.length > 0 && (
            <div className="grid gap-1.5 min-w-0">
              <div className="text-sm font-medium">Compiled with</div>
              <dl className="grid gap-1 text-sm">
                {p.parameters.map((x) => (
                  <div key={x.name} className="flex flex-wrap gap-x-2 min-w-0">
                    <dt className="font-mono text-xs leading-5">{x.name}</dt>
                    {x.dataType && <dd className="text-muted-foreground text-xs leading-5">{x.dataType}</dd>}
                    <dd className="font-mono text-xs leading-5 break-all">= {x.compiledValue || "—"}</dd>
                    {x.runtimeValue && x.runtimeValue !== x.compiledValue && (
                      <dd className="text-xs leading-5 text-muted-foreground break-all">
                        last run with <span className="font-mono">{x.runtimeValue}</span>
                      </dd>
                    )}
                  </div>
                ))}
              </dl>
            </div>
          )}
          {p.missingIndexes.map((mi) => (
            <div key={`${mi.table}/${mi.equality}/${mi.inequality}/${mi.included}`} className="grid gap-2 rounded-lg bg-muted/50 p-3 min-w-0">
              <div className="text-sm font-medium">SQL Server suggests an index</div>
              <p className="text-sm break-words">
                On <span className="font-mono text-xs">{mi.tableName}</span>, keys{" "}
                <span className="font-mono text-xs">{plainColumns([mi.equality, mi.inequality].filter(Boolean).join(", "))}</span>
                {mi.included && (
                  <>
                    , including <span className="font-mono text-xs">{plainColumns(mi.included)}</span>
                  </>
                )}
                . Estimated to cut this query's cost by {formatWhole(mi.impact)}%.
              </p>
              <p className="text-xs text-muted-foreground">
                Never create every suggestion blindly: the preview checks the indexes the table already has.
              </p>
              {mi.action && (
                <div>
                  <ActionButton action={mi.action} onAction={onAction} />
                </div>
              )}
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}
