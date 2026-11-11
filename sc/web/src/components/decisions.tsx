import { useState } from "react"
import { Link } from "react-router-dom"
import { Check, ChevronRight, Copy } from "lucide-react"

import type { Decision, DecisionAction, DecisionLine, DecisionRecord } from "@/api"
import { DecisionDialog } from "@/components/decision-dialog"
import { FixDialog } from "@/components/fix-dialog"
import { CopyFallback } from "@/components/statement-block"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { useBuildVersion } from "@/lib/build-version"
import { formatWhen } from "@/lib/format"
import { decisionHandoverText } from "@/lib/handover"
import { databasePagePath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// The Decisions card (docs/phase5m-contract.md, section 1): one level above the six cards, one row per
// decision. A row closed is one line: the name, the verdict as a tag, the verdict sentence and the record
// ("Review due" in amber once its date has passed). Opened: what kakapo found, do first, what it costs, then
// the buttons (the verdict's action, Record a decision, Copy). A decision is not a finding: it is on no card
// and in no count.

// The One Meaning Rule (DESIGN.md): green no problem, amber something to do; "orange" (worth considering)
// is something to do too, so it shares amber's tint rather than adding a fifth hue.
const amberTag = "bg-amber-500/15 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400"
const toneTag: Record<Decision["tone"], string> = {
  green: "bg-emerald-500/15 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-400",
  amber: amberTag,
  orange: amberTag,
  grey: "bg-muted text-muted-foreground",
}

export function DecisionsCard({
  decisions,
  serverId,
  database,
  server,
  checkedAt,
  onChanged,
}: {
  decisions: Decision[]
  serverId: number
  database: string
  server: string
  checkedAt: string
  onChanged: () => void
}) {
  if (decisions.length === 0) return null
  return (
    <section className="grid gap-2 rounded-xl border p-3 sm:p-4" aria-label="Decisions for this database">
      <h2 className="font-medium">Decisions for this database</h2>
      <ul className="grid divide-y">
        {decisions.map((d) => (
          <li key={d.decision} className="min-w-0 py-1">
            <DecisionRow
              decision={d}
              serverId={serverId}
              database={database}
              server={server}
              checkedAt={checkedAt}
              onChanged={onChanged}
            />
          </li>
        ))}
      </ul>
    </section>
  )
}

type Fixing = { fixId: string; title: string }

function DecisionRow({
  decision,
  serverId,
  database,
  server,
  checkedAt,
  onChanged,
}: {
  decision: Decision
  serverId: number
  database: string
  server: string
  checkedAt: string
  onChanged: () => void
}) {
  const [open, setOpen] = useState(false)
  const [fixing, setFixing] = useState<Fixing | null>(null)
  const [recording, setRecording] = useState(false)
  // Records made here since the page was read, newest first: the row shows them at once.
  const [recorded, setRecorded] = useState<DecisionRecord[]>([])
  const copy = useCopy()
  const version = useBuildVersion()

  const history = [...recorded, ...decision.history]
  const latest = history[0]
  const recordLine = latest?.line ?? decision.recordLine
  const reviewDue = latest ? !!latest.reviewDue : !!decision.reviewDue
  const shown: Decision = { ...decision, history, recordLine, reviewDue }

  const act = (a: DecisionAction) => (a.fixId ? setFixing({ fixId: a.fixId, title: a.label }) : undefined)

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="group flex w-full min-w-0 items-start gap-2 rounded-md py-1.5 text-left hover:bg-muted/40"
        >
          <ChevronRight className="mt-0.5 size-4 flex-none text-muted-foreground transition-transform group-data-[state=open]:rotate-90" />
          <span className="grid min-w-0 flex-1 gap-1">
            <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-1">
              <span className="font-medium break-words">{decision.title}</span>
              <span className={cn("rounded px-2 py-0.5 text-xs whitespace-nowrap", toneTag[decision.tone])}>{decision.tag}</span>
            </span>
            <span className="text-sm break-words">{decision.sentence}</span>
            {recordLine && (
              <span className="flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs text-muted-foreground">
                {reviewDue && (
                  <span className={cn("rounded px-1.5 py-0.5", amberTag)}>Review due</span>
                )}
                <span className="break-words">{recordLine}</span>
              </span>
            )}
          </span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="grid gap-4 pt-2 pb-2 pl-6">
        <p className="text-sm text-muted-foreground">{decision.question}</p>

        <Lines title="What kakapo found" lines={decision.found} />

        {decision.doFirst.length > 0 && (
          <div className="grid gap-1.5">
            <h3 className="text-sm font-medium">Do first</h3>
            <ul className="grid gap-2">
              {decision.doFirst.map((s) => (
                <li key={s.text} className="grid gap-1.5 rounded-md border border-amber-300 p-2.5 text-sm dark:border-amber-800">
                  <span className="break-words">{s.text}</span>
                  <ActionButton action={s.action} serverId={serverId} database={database} onFix={act} variant="outline" />
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="grid gap-1.5">
          <Lines title="What it costs" lines={decision.costs} />
          {decision.notMeasured && <p className="text-sm text-muted-foreground break-words">{decision.notMeasured}</p>}
          {decision.facts.length > 0 && <Lines lines={decision.facts} />}
        </div>

        <div className="flex flex-wrap gap-2">
          {decision.actions.map((a) => (
            <ActionButton key={a.label} action={a} serverId={serverId} database={database} onFix={act} />
          ))}
          <Button variant="outline" size="sm" onClick={() => setRecording(true)}>
            Record a decision
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() =>
              copy.copy(decisionHandoverText(shown, { server, database, asOf: formatWhen(checkedAt), version }))
            }
          >
            {copy.status === "copied" ? <Check /> : <Copy />}
            {copy.status === "copied" ? "Copied" : "Copy"}
          </Button>
        </div>
        {copy.fallback && <CopyFallback text={copy.fallback} onClose={copy.dismiss} />}

        {history.length > 0 && (
          <div className="grid gap-1">
            <h3 className="text-sm font-medium">Decisions so far</h3>
            <ul className="grid gap-1 text-sm">
              {history.map((r) => (
                <li key={r.id} className="break-words">
                  {r.line}
                  <span className="text-xs text-muted-foreground"> · {r.decidedBy}</span>
                </li>
              ))}
            </ul>
          </div>
        )}

        <p className="text-xs text-muted-foreground break-words">
          {decision.checkId} · Then check: {decision.thenCheck} · Sources: {decision.sources.join("; ")}
        </p>
      </CollapsibleContent>

      {fixing && (
        <FixDialog
          serverId={serverId}
          database={database}
          fixId={fixing.fixId}
          title={fixing.title}
          onClose={() => setFixing(null)}
          onApplied={() => onChanged()}
        />
      )}
      {recording && (
        <DecisionDialog
          serverId={serverId}
          database={database}
          title={decision.title}
          verdict={decision.verdict}
          onClose={() => setRecording(false)}
          onRecorded={(r) => setRecorded((rs) => [r, ...rs])}
        />
      )}
    </Collapsible>
  )
}

function Lines({ title, lines }: { title?: string; lines: DecisionLine[] }) {
  if (lines.length === 0) return null
  return (
    <div className="grid gap-1.5">
      {title && <h3 className="text-sm font-medium">{title}</h3>}
      <ul className="grid gap-1.5 text-sm">
        {lines.map((l) => (
          <li key={l.label + l.text} className="grid min-w-0 gap-0.5">
            <span className="break-words">
              <span className="font-medium">{l.label}:</span> {l.text}
            </span>
            {l.window && <span className="text-xs text-muted-foreground break-words">{l.window}</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}

// An action: Apply opens the fix's preview; any other goes to its page under the database.
function ActionButton({
  action,
  serverId,
  database,
  onFix,
  variant = "default",
}: {
  action: DecisionAction
  serverId: number
  database: string
  onFix: (a: DecisionAction) => void
  variant?: "default" | "outline"
}) {
  if (action.fixId) {
    return (
      <Button size="sm" variant={variant} className="w-fit" onClick={() => onFix(action)}>
        {action.label}
      </Button>
    )
  }
  return (
    <Button size="sm" variant="outline" className="w-fit" asChild>
      <Link to={`${databasePagePath(serverId, database)}/${action.to}`}>{action.label}</Link>
    </Button>
  )
}
