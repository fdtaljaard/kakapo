import { clockTime, duration, sessionsWord, whoText } from "@/components/blocking/blocking-text"
import type { SeenChain } from "@/components/blocking/use-blocking-watch"
import { cn } from "@/lib/utils"

// The chains the watch has seen, kept after they clear, newest first.
export function ChainsSeen({ seen, watching }: { seen: SeenChain[]; watching: boolean }) {
  const rows = [...seen].sort((a, b) => b.firstSeen.getTime() - a.firstSeen.getTime())
  return (
    <section className="grid gap-2" aria-label="Chains seen">
      <div className="grid gap-0.5">
        <h2 className="font-medium">Chains seen</h2>
        <p className="text-sm text-muted-foreground">
          Every head blocker read since this page opened, kept after it clears so a short block is not missed.
        </p>
      </div>
      {rows.length === 0 ? (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          {watching ? "Nothing blocking so far." : "No blocking seen while watching."}
        </p>
      ) : (
        <ul className="grid gap-2">
          {rows.map((c) => (
            <li key={c.key} className="rounded-xl border p-3 grid gap-1 min-w-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-medium">Session {c.head.session}</span>
                <span
                  className={cn(
                    "text-sm font-medium",
                    c.clearedAt ? "text-emerald-700 dark:text-emerald-400" : "text-amber-700 dark:text-amber-400",
                  )}
                >
                  {c.clearedAt ? `Cleared at ${clockTime(c.clearedAt)}` : "Still blocking"}
                </span>
              </div>
              <p className="text-sm text-muted-foreground break-words">
                {whoText(c.head)} · first seen {clockTime(c.firstSeen)} · up to{" "}
                {sessionsWord(c.mostBehind)} behind it · longest wait{" "}
                {duration(c.longestWaitSec)}
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
