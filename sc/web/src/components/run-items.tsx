import { CircleCheck, CircleDashed, CircleSlash, Loader2, TriangleAlert, XCircle } from "lucide-react"

import type { RunItem } from "@/api"

// Phase 5t (docs/phase5t-contract.md): the indexes of a batch run, one line each with where it stands:
// waiting, running, reorganised, skipped (with why), failed (with the error) or stopped.
const icons: Record<RunItem["state"], { icon: typeof Loader2; className: string; word: string }> = {
  waiting: { icon: CircleDashed, className: "text-muted-foreground", word: "waiting" },
  running: { icon: Loader2, className: "animate-spin text-muted-foreground", word: "working" },
  done: { icon: CircleCheck, className: "text-emerald-600 dark:text-emerald-400", word: "reorganised" },
  differs: { icon: TriangleAlert, className: "text-amber-600 dark:text-amber-400", word: "reorganised, not as expected" },
  skipped: { icon: CircleSlash, className: "text-muted-foreground", word: "skipped" },
  failed: { icon: XCircle, className: "text-destructive", word: "failed" },
  stopped: { icon: CircleSlash, className: "text-amber-600 dark:text-amber-400", word: "stopped" },
}

export function RunItems({ items }: { items: RunItem[] }) {
  return (
    <ul className="grid gap-1.5 text-sm" aria-label="Indexes in this run">
      {items.map((it) => {
        const { icon: Icon, className, word } = icons[it.state]
        return (
          <li key={it.label} className="flex min-w-0 items-start gap-2">
            <Icon className={`mt-0.5 size-4 shrink-0 ${className}`} aria-label={word} />
            <span className="min-w-0 break-words">
              <span className="font-medium">{it.label}</span>
              {it.text && <span className="text-muted-foreground"> · {it.text}</span>}
            </span>
          </li>
        )
      })}
    </ul>
  )
}
