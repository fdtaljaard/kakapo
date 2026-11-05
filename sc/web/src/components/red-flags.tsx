import { Check } from "lucide-react"
import { Link } from "react-router-dom"

import type { CardID, RedFlags } from "@/api"
import { Button } from "@/components/ui/button"
import { cardPath, databasePagePath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// The red flags under the load (docs/phase5i-contract.md, section 3): at most three lines, Critical
// first, each with its icon and the word for screen readers, the finding's own sentence, its source and
// one button; then "and {n} more"; or one line "No red flags".

const cardName: Record<CardID, string> = {
  backups: "Backups",
  setup: "Setup",
  storage: "Storage",
  upkeep: "Upkeep",
  performance: "Performance",
  design: "Design",
}

export function RedFlagList({ flags, serverId, database }: { flags: RedFlags | null; serverId: number; database: string }) {
  if (!flags) return <p className="text-sm text-muted-foreground">Reading the cards for red flags…</p>
  if (flags.flags.length === 0) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <span className="inline-flex size-4 items-center justify-center rounded-full bg-[#0ca30c] text-white" aria-hidden>
          <Check className="size-3" strokeWidth={3} />
        </span>
        No red flags
      </p>
    )
  }
  const next = flags.rest[0]
  return (
    <ul className="grid gap-1.5" aria-label="Red flags">
      {flags.flags.map((f) => (
        <li key={`${f.checkId}/${f.object}/${f.rank}`} className="flex items-start gap-2">
          <span
            className={cn(
              "mt-0.5 inline-flex size-[18px] flex-none items-center justify-center rounded-full text-xs font-bold text-white",
              f.severity === "Critical" ? "bg-[#d03b3b]" : "bg-[#ec835a]",
            )}
            aria-hidden
          >
            !
          </span>
          <span className="min-w-0 flex-1 break-words text-sm">
            <span className="sr-only">{f.severity}: </span>
            {f.sentence} <span className="text-xs text-muted-foreground">{`${cardName[f.card] ?? f.card} · ${f.checkId}`}</span>
          </span>
          <Button variant="outline" size="sm" className="h-7 flex-none" asChild>
            <Link to={`${databasePagePath(serverId, database)}/${f.to}`}>{f.to === "blocking" ? "Open the chain" : "Open"}</Link>
          </Button>
        </li>
      ))}
      {flags.more > 0 && next && (
        <li className="pl-[26px] text-xs text-muted-foreground">
          <Link className="underline underline-offset-2" to={cardPath(serverId, database, next.card)}>
            and {flags.more} more
          </Link>
        </li>
      )}
    </ul>
  )
}
