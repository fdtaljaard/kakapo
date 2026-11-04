import { Link } from "react-router-dom"
import { Check, Copy, PenLine, Search } from "lucide-react"

import type { QueryAction } from "@/api-queries"
import type { TuningLine, TuningReport, TuningTile } from "@/api-tuning"
import { ActionButton } from "@/components/query/action-button"
import { CopyFallback } from "@/components/statement-block"
import { Button } from "@/components/ui/button"
import { queryPath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

import { formatCount, signedPct, tuningMs } from "./format"

// A Copy button with its own clipboard state; when the browser refuses, the text shows selected below.
export function CopyButton({ text, label = "Copy for the vendor" }: { text: string; label?: string }) {
  const { status, fallback, copy, dismiss } = useCopy()
  return (
    <>
      <Button variant="ghost" size="sm" onClick={() => copy(text)}>
        {status === "copied" ? <Check /> : <Copy />}
        {status === "copied" ? "Copied" : label}
      </Button>
      {fallback && (
        <div className="basis-full min-w-0">
          <CopyFallback text={fallback} onClose={dismiss} />
        </div>
      )}
    </>
  )
}

// Investigate: the query page of a query hash.
export function InvestigateLink({ serverId, database, hash }: { serverId: number; database: string; hash: string }) {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link to={queryPath(serverId, database, hash)}>
        <Search />
        Investigate
      </Link>
    </Button>
  )
}

// The report's plain sentences first (domain tExplain), each with what it offers: the fix it names, the
// query page and Copy for the vendor when it names a query, Record a change when the log is empty.
export function ExplainList({
  report,
  serverId,
  onAction,
  onRecordChange,
}: {
  report: TuningReport
  serverId: number
  onAction: (a: QueryAction) => void
  onRecordChange: () => void
}) {
  if (report.explain.length === 0) return <p className="text-muted-foreground">Nothing to say yet.</p>
  return (
    <ul className="grid gap-2">
      {report.explain.map((l: TuningLine) => (
        <li key={l.text} className="grid gap-2 rounded-xl border p-3 min-w-0">
          <p className="break-words">{l.text}</p>
          {(l.action || l.queryHash || l.copy || l.recordChange) && (
            <div className="flex flex-wrap items-center gap-2">
              {l.action && <ActionButton action={l.action} onAction={onAction} variant="default" />}
              {l.recordChange && (
                <Button size="sm" onClick={onRecordChange}>
                  <PenLine />
                  Record a change
                </Button>
              )}
              {l.queryHash && <InvestigateLink serverId={serverId} database={report.database} hash={l.queryHash} />}
              {l.copy && <CopyButton text={l.copy} />}
            </div>
          )}
        </li>
      ))}
    </ul>
  )
}

const toneText: Record<TuningTile["tone"], string> = {
  better: "text-emerald-700 dark:text-emerald-400",
  worse: "text-red-700 dark:text-red-400",
  same: "text-muted-foreground",
  more: "text-sky-700 dark:text-sky-400",
  fewer: "text-sky-700 dark:text-sky-400",
}

function tileValue(t: TuningTile, v: number | null, text?: string): string {
  if (text) return text
  if (v === null) return "—"
  if (t.unit === "ms") return tuningMs(v)
  if (t.unit === "threads") return v.toFixed(1)
  return formatCount(v)
}

// "Is it better than before?": the four tiles, before -> now with the % change and its tone.
export function Tiles({ tiles }: { tiles: TuningTile[] }) {
  return (
    <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
      {tiles.map((t) => (
        <div key={t.id} className="grid gap-1 rounded-xl border p-3 min-w-0">
          <span className="text-xs text-muted-foreground">{t.label}</span>
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-lg font-semibold tabular-nums">{tileValue(t, t.now, t.nowText)}</span>
            {t.pct !== null && <span className={cn("text-sm font-medium tabular-nums", toneText[t.tone])}>{signedPct(t.pct)}</span>}
          </div>
          <span className="text-xs text-muted-foreground tabular-nums">was {tileValue(t, t.before, t.beforeText)}</span>
          {t.words && <span className={cn("text-xs break-words", t.now === null && "text-muted-foreground")}>{t.words}</span>}
          <span className={cn("text-xs font-medium", toneText[t.tone])}>{t.pct === null ? "" : t.tone}</span>
        </div>
      ))}
    </div>
  )
}
