import { useContext } from "react"
import { Check, Copy, Search } from "lucide-react"
import { Link } from "react-router-dom"

import type { Finding, Severity } from "@/api"
import { CopyFallback, StatementBlock } from "@/components/statement-block"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { FindingContext } from "@/lib/finding-context"
import { copyLabel, findingObject, handoverText, type HandoverContext } from "@/lib/handover"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// The badge colours follow the card colours (docs/landing-page.md): red act today, amber something
// to do, grey for the rest.
const severityColour: Record<Severity, string> = {
  Critical: "bg-destructive/10 text-destructive dark:bg-destructive/20",
  High: "bg-amber-500/15 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400",
  Medium: "bg-amber-500/15 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400",
  Low: "bg-muted text-muted-foreground",
  Info: "bg-muted text-muted-foreground",
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  return <Badge className={severityColour[severity]}>{severity}</Badge>
}

// "Do this" is either a T-SQL statement (monospace) or a sentence naming who does what (prose).
const statementStart = /^\s*(ALTER|BACKUP|CREATE|DBCC|DECLARE|DELETE|DROP|EXEC|EXECUTE|INSERT|RESTORE|SELECT|SET|UPDATE|USE)\b/i
const isStatement = (text: string) => statementStart.test(text)

// "Client IT · Online · Quick win"; "None" says nothing, so it is left out.
function metaText(f: Finding): string {
  return [f.owner, f.when, f.effort].filter((v) => v && v !== "None").join(" · ")
}

// One finding, as in the HTML reports: what was found, why it matters, what to do and how to see it
// worked. An Info finding is a note, not a task, so it is drawn quieter. With onApply, a finding the
// app can fix (fixId) gets an Apply button that opens the fix's preview. Every finding gets Copy
// (docs/phase4a-contract.md): the whole finding as one block for whoever owns it, labelled by owner. context
// says where it was read; without it the card page's FindingContext is used.
export function FindingCard({
  finding: f,
  onApply,
  choices,
  investigate,
  context,
}: {
  finding: Finding
  onApply?: (f: Finding, options?: Record<string, string>, fixId?: string) => void
  // Phase 5o: one Apply per entry when a finding has several fixes of its kind (two safe copies on one
  // table), each labelled and sending its own options; otherwise the one Apply. A choice with a fixId
  // applies that fix instead of the finding's (BAK02: schedule the log backups, or switch to SIMPLE).
  choices?: { label: string; options?: Record<string, string>; fixId?: string }[]
  // Where the next read lives (docs/phase4c-contract.md): an Investigate link next to Apply.
  investigate?: { label?: string; to: string }
  context?: HandoverContext
}) {
  const inherited = useContext(FindingContext)
  const { status, fallback, copy, dismiss } = useCopy()
  const info = f.severity === "Info"
  const meta = metaText(f)
  const canApply = !!f.fixId && !!onApply
  const ctx = context ?? inherited ?? noContext
  // The table, file or column the finding is about, unless the sentence already names it.
  const object = findingObject(f, ctx)
  const showObject = !!object && !f.finding.includes(object)
  return (
    <Card size="sm" className={cn(info && "bg-transparent shadow-none ring-0 border border-dashed")}>
      <CardContent className={cn("grid gap-3 min-w-0", info && "text-muted-foreground")}>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <SeverityBadge severity={f.severity} />
          <span className="font-mono text-xs text-muted-foreground">{f.checkId}</span>
          <span className={cn("font-medium", !info && "text-foreground")}>{f.title}</span>
          {showObject && <span className="font-mono text-xs text-foreground break-all">{object}</span>}
          <div className="ml-auto flex max-w-full flex-wrap items-center justify-end gap-2">
            {meta && <span className="text-xs text-muted-foreground">{meta}</span>}
            <Button variant="ghost" size="sm" onClick={() => copy(handoverText(f, ctx))}>
              {status === "copied" ? <Check /> : <Copy />}
              {status === "copied" ? "Copied" : copyLabel(f.owner)}
            </Button>
            {investigate && (
              <Button variant="outline" size="sm" asChild>
                <Link to={investigate.to}>
                  <Search />
                  {investigate.label ?? "Investigate"}
                </Link>
              </Button>
            )}
            {canApply && !choices && (
              <Button variant="outline" size="sm" onClick={() => onApply?.(f)}>
                Apply
              </Button>
            )}
            {canApply &&
              choices?.map((c) => (
                <Button key={c.label} variant="outline" size="sm" className="max-w-full" onClick={() => onApply?.(f, c.options, c.fixId)}>
                  <span className="truncate">{c.label}</span>
                </Button>
              ))}
          </div>
        </div>

        <p className={cn(!info && "text-base font-medium")}>{f.finding}</p>

        {f.why && <Section label="Why it matters" text={f.why} />}
        {f.doThis && <StatementBlock label="Do this" text={f.doThis} prose={!isStatement(f.doThis)} />}
        {f.thenCheck && <Section label="Then check" text={f.thenCheck} />}
        {f.evidence && <Evidence text={f.evidence} />}
        {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
      </CardContent>
    </Card>
  )
}

const noContext: HandoverContext = { server: "", database: "", asOf: "", version: "" }

// The evidence as raw numbers; a query finding (QRY01) ends with "Query: " and the statement text,
// which is what a consultant pastes into SSMS, so that part gets a monospace block with its own Copy.
function Evidence({ text }: { text: string }) {
  const at = text.indexOf(QUERY_MARK)
  const rest = (at < 0 ? text : text.slice(0, at)).trim()
  const statement = at < 0 ? "" : text.slice(at + QUERY_MARK.length).trim()
  return (
    <>
      {rest && <p className="text-xs text-muted-foreground break-words">{rest}</p>}
      {statement && <StatementBlock label="Query" text={statement} />}
    </>
  )
}

const QUERY_MARK = "Query: "

function Section({ label, text }: { label: string; text: string }) {
  return (
    <div className="grid gap-0.5">
      <div className="font-medium">{label}</div>
      <p className="break-words">{text}</p>
    </div>
  )
}
