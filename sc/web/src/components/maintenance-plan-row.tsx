import { Check, ChevronRight, Copy } from "lucide-react"

import type { Fix, MaintenancePlan, PlanPiece, PlanStatus } from "@/api"
import { CopyFallback } from "@/components/statement-block"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import type { HandoverContext } from "@/lib/handover"
import { useCopy } from "@/lib/use-copy"

// The status word of a line and its colour: green when the piece runs, amber when it needs doing, red when it
// fails, quiet when nothing is asked.
const statusWords: Record<PlanStatus, { word: string; tone: string }> = {
  "in-place": { word: "in place", tone: "text-emerald-600 dark:text-emerald-400" },
  "other-job": { word: "in place (another job)", tone: "text-emerald-600 dark:text-emerald-400" },
  "never-ran": { word: "not run yet", tone: "text-sky-600 dark:text-sky-400" },
  missing: { word: "missing", tone: "text-amber-600 dark:text-amber-400" },
  disabled: { word: "disabled", tone: "text-amber-600 dark:text-amber-400" },
  failing: { word: "failing", tone: "text-destructive" },
  "not-needed": { word: "not needed", tone: "text-muted-foreground" },
  optional: { word: "optional", tone: "text-muted-foreground" },
  unknown: { word: "not read", tone: "text-muted-foreground" },
}

// The Upkeep card's Maintenance plan row (docs/phase5b-contract.md): one line per piece of regular maintenance
// with its status word in colour, the job and its last and next run, and Create where kakapo can make it (the
// piece's own fix, with the options that fit the plan's times); "Set up the maintenance plan" when anything is
// missing (the composite fix); "Copy for IT", the whole plan as a table with its sources; and a "Why these
// times" fold.
export function MaintenancePlanRow({
  plan,
  fixes,
  context,
  onCreate,
  onSetUp,
}: {
  plan?: MaintenancePlan
  fixes?: Fix[]
  context: HandoverContext
  onCreate: (piece: PlanPiece) => void
  onSetUp: (fix: Fix) => void
}) {
  const { status, fallback, copy, dismiss } = useCopy()
  if (!plan) return null
  const setUp = plan.fixId ? fixes?.find((f) => f.id === plan.fixId) : undefined
  const inPlace = plan.pieces.filter((p) => p.status === "in-place" || p.status === "other-job").length
  const description = plan.note
    ? plan.note
    : plan.missing > 0
      ? `${plan.missing === 1 ? "One piece" : `${plan.missing} pieces`} of the regular maintenance this server needs ${plan.missing === 1 ? "is" : "are"} missing. Create previews one piece; Set up the maintenance plan previews creating every missing piece in one go, at times that do not overlap.`
      : `${inPlace} of ${plan.pieces.length} pieces run on a schedule; nothing is missing.`
  return (
    <Card>
      <CardHeader>
        <CardTitle>Maintenance plan</CardTitle>
        <CardDescription className="break-words">{description}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <ul className="grid divide-y rounded-xl border" aria-label="Pieces of the maintenance plan">
          {plan.pieces.map((p) => (
            <PieceLine key={`${p.id}/${p.title}`} piece={p} onCreate={() => onCreate(p)} />
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          {setUp && (
            <Button size="sm" onClick={() => onSetUp(setUp)}>
              Set up the maintenance plan
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => copy(planHandover(plan, context))}>
            {status === "copied" ? <Check /> : <Copy />}
            {status === "copied" ? "Copied" : "Copy for IT"}
          </Button>
        </div>
        {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="group -ml-2.5 w-fit text-muted-foreground">
              <ChevronRight className="transition-transform group-data-[state=open]:rotate-90" />
              Why these times
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="grid gap-2 pt-1 text-sm">
            <p className="break-words">{plan.order}</p>
            <dl className="grid gap-2">
              {sourcesOf(plan).map((p) => (
                <div key={p.id} className="grid gap-0.5">
                  <dt className="font-medium">
                    {p.title} <span className="font-normal text-muted-foreground">({p.cadence})</span>
                  </dt>
                  <dd className="text-muted-foreground break-words">{p.source}</dd>
                </div>
              ))}
            </dl>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  )
}

function PieceLine({ piece: p, onCreate }: { piece: PlanPiece; onCreate: () => void }) {
  const st = statusWords[p.status] ?? { word: p.status, tone: "text-muted-foreground" }
  const creatable = !!p.fixId && (p.status === "missing" || p.status === "disabled" || p.status === "optional")
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className="font-medium break-words">{p.title}</span>
          <span className={`text-sm font-medium ${st.tone}`}>{st.word}</span>
          <span className="text-xs text-muted-foreground break-words">kakapo proposes {p.cadence}</span>
        </div>
        {p.job && <p className="text-sm break-words">{jobWords(p)}</p>}
        {p.note && <p className="text-xs text-muted-foreground break-words">{p.note}</p>}
      </div>
      {creatable && (
        <Button variant="outline" size="sm" onClick={onCreate}>
          Create
        </Button>
      )}
    </li>
  )
}

// "kakapo: Shop full backup, daily 22:00; last run 2026-11-03 22:00 (succeeded, 4 min); next 2026-11-04 22:00".
function jobWords(p: PlanPiece): string {
  const parts = [`${p.job}${p.schedule ? `, ${p.schedule}` : ""}`]
  if (p.lastRun) {
    const how = [p.lastOutcome, p.lastDuration].filter(Boolean).join(", ")
    parts.push(`last run ${p.lastRun}${how ? ` (${how})` : ""}`)
  } else {
    parts.push("not run yet")
  }
  if (p.nextRun) parts.push(`next ${p.nextRun}`)
  return parts.join("; ")
}

// The sources once per kind of piece (the archive lines share one).
function sourcesOf(plan: MaintenancePlan): PlanPiece[] {
  const seen = new Set<string>()
  return plan.pieces.filter((p) => {
    if (seen.has(p.id)) return false
    seen.add(p.id)
    return true
  })
}

// The hand-over block of the whole plan for the client's IT: where it was read, the plan as a table (piece,
// status, what kakapo proposes, the job and its runs), why these times, and the sources.
function planHandover(plan: MaintenancePlan, ctx: HandoverContext): string {
  const where = [ctx.server, ctx.database].filter(Boolean).join(" / ")
  const head = [`Maintenance plan${where ? ` for ${where}` : ""}`]
  if (ctx.asOf) head.push(`As of ${ctx.asOf}`)
  const rows = plan.pieces.map((p) => [
    p.title,
    statusWords[p.status]?.word ?? p.status,
    p.cadence,
    p.job ? jobWords(p) : (p.note ?? ""),
  ])
  const header = ["Piece", "Status", "Proposed", "Job, or why not"]
  const widths = [0, 1, 2].map((i) => Math.max(header[i].length, ...rows.map((r) => r[i].length)))
  const line = (cells: string[]) => [...cells.slice(0, 3).map((c, i) => c.padEnd(widths[i])), cells[3]].join(" | ").trimEnd()
  const rule = [...widths.map((w) => "-".repeat(w)), "-".repeat(header[3].length)].join("-|-")
  const table = [line(header), rule, ...rows.map(line)]
  const sources = sourcesOf(plan).map((p) => `- ${p.title} (${p.cadence}): ${p.source}`)
  return [
    head.join("\n"),
    plan.note ?? "",
    table.join("\n"),
    `Why these times\n${plan.order}`,
    `Sources\n${sources.join("\n")}`,
    ctx.version ? `From kakapo build ${ctx.version}` : "",
  ]
    .filter(Boolean)
    .join("\n\n")
}
