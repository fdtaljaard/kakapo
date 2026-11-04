import { CircleCheck, CircleMinus, Info } from "lucide-react"

import type { CheckStatus, Finding } from "@/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"

// "What was checked": every check behind the card, one line each, in catalogue order. A check whose
// only findings are Info (BAK05 on a healthy database) is a note, not something to do, so it is not
// marked amber.
export function ChecksList({ checks, findings = [] }: { checks: CheckStatus[]; findings?: Finding[] }) {
  const actionable = new Set(findings.filter((f) => f.severity !== "Info").map((f) => f.checkId))
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>What was checked</CardTitle>
      </CardHeader>
      <CardContent>
        <ul className="grid divide-y">
          {checks.map((c) => (
            <CheckLine key={c.checkId} check={c} infoOnly={c.status === "finding" && !actionable.has(c.checkId)} />
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

function CheckLine({ check: c, infoOnly }: { check: CheckStatus; infoOnly: boolean }) {
  return (
    <li className="flex items-start gap-2.5 py-2 first:pt-0 last:pb-0">
      <StatusIcon check={c} infoOnly={infoOnly} />
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-mono text-xs text-muted-foreground">{c.checkId}</span>
          <span className="min-w-0">{c.title}</span>
        </div>
        {c.status === "skipped" && c.note && (
          <p className="text-xs text-muted-foreground break-words">{c.note}</p>
        )}
      </div>
      <StatusWord check={c} infoOnly={infoOnly} />
    </li>
  )
}

function StatusIcon({ check, infoOnly }: { check: CheckStatus; infoOnly: boolean }) {
  if (check.status === "passed") return <CircleCheck className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" aria-hidden />
  if (check.status === "skipped") return <CircleMinus className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
  if (infoOnly) return <Info className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
  return (
    <span className="flex size-4 shrink-0 items-center justify-center mt-0.5" aria-hidden>
      <span className="size-2 rounded-full bg-amber-500" />
    </span>
  )
}

function StatusWord({ check, infoOnly }: { check: CheckStatus; infoOnly: boolean }) {
  if (check.status === "passed") return <span className="text-xs whitespace-nowrap text-emerald-600 dark:text-emerald-400">Passed</span>
  if (check.status === "skipped") return <span className="text-xs whitespace-nowrap text-muted-foreground">Skipped</span>
  if (infoOnly) return <span className="text-xs whitespace-nowrap text-muted-foreground">For information</span>
  return <span className="text-xs whitespace-nowrap text-amber-600 dark:text-amber-400">Finding</span>
}
