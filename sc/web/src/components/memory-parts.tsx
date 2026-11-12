import { Check, Copy, Loader2, Square } from "lucide-react"

import type { MemorySnapshotInfo, MemoryTone } from "@/api-memory"
import { CopyFallback } from "@/components/statement-block"
import { Button } from "@/components/ui/button"
import { formatAgo, formatWhen } from "@/lib/format"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// Shared parts of the Memory screen and Database detail (docs/phase5v-contract.md).

// "Reading the cache… 12 s  [Stop]" while a slow read runs.
export function ReadingLine({ what, elapsed, onStop }: { what: string; elapsed: number; onStop: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-3" aria-live="polite">
      <span className="flex items-center gap-2 text-sm">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
        {what}… {elapsed} s
      </span>
      <Button size="sm" variant="outline" onClick={onStop}>
        <Square />
        Stop
      </Button>
    </div>
  )
}

const toneDot: Record<MemoryTone, string> = {
  red: "bg-destructive",
  amber: "bg-amber-500",
  green: "bg-emerald-600 dark:bg-emerald-400",
  grey: "bg-muted-foreground",
}

// The verdict sentence, with its status dot (DESIGN.md, The One Meaning Rule).
export function Verdict({ tone, text }: { tone: MemoryTone; text: string }) {
  return (
    <p className="flex items-start gap-2 text-base font-medium break-words">
      <span aria-hidden className={cn("mt-2 size-2 shrink-0 rounded-full", toneDot[tone])} />
      <span>{text}</span>
    </p>
  )
}

export function CopyButton({ text }: { text: string }) {
  const { status, fallback, copy, dismiss } = useCopy()
  return (
    <>
      <Button variant="outline" onClick={() => copy(text)}>
        {status === "copied" ? <Check /> : <Copy />}
        {status === "copied" ? "Copied" : "Copy"}
      </Button>
      {fallback && (
        <div className="basis-full">
          <CopyFallback text={fallback} onClose={dismiss} />
        </div>
      )}
    </>
  )
}

// The list of saved snapshots, newest first: Open shows one as it was read; Compare puts its numbers beside.
export function SnapshotList({
  snapshots,
  shown,
  compared,
  onOpen,
  onCompare,
}: {
  snapshots: MemorySnapshotInfo[]
  shown: number
  compared?: number
  onOpen: (id: number) => void
  onCompare?: (id: number) => void
}) {
  if (snapshots.length === 0) return null
  return (
    <details className="rounded-xl border px-4 py-3 min-w-0">
      <summary className="cursor-pointer text-sm font-medium">
        Saved snapshots <span className="font-normal text-muted-foreground">· {snapshots.length}, kept for good</span>
      </summary>
      <ul className="mt-2 divide-y text-sm">
        {snapshots.map((s) => (
          <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 py-2">
            <span>
              {formatWhen(s.takenAt, "row")}
              <span className="text-muted-foreground"> · {formatAgo(s.takenAt)}</span>
              {s.id === shown && <span className="text-muted-foreground"> · shown</span>}
              {s.id === compared && <span className="text-muted-foreground"> · compared</span>}
            </span>
            <span className="flex gap-2">
              <Button size="xs" variant="ghost" disabled={s.id === shown} onClick={() => onOpen(s.id)}>
                Open
              </Button>
              {onCompare && (
                <Button size="xs" variant="ghost" disabled={s.id === shown || s.id === compared} onClick={() => onCompare(s.id)}>
                  Compare with
                </Button>
              )}
            </span>
          </li>
        ))}
      </ul>
    </details>
  )
}
