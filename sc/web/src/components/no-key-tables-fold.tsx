import { ChevronRight } from "lucide-react"

import type { DesignNoKeyTables } from "@/api"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"

// Phase 5s: the Design card's fold of the smaller tables without a primary key (docs/phase5s-contract.md,
// decision 2): tables with rows but under 10,000, largest first, one line each ("Codes (2,410 rows, heap)")
// and no button. Nothing when there is no such table.
export function NoKeyTablesFold({ fold }: { fold: DesignNoKeyTables }) {
  if (fold.tables.length === 0) return null
  const why =
    "These tables have no primary key either, so nothing stops two identical rows in them. They are small, so kakapo does not raise them as findings: when one matters to the application, ask the vendor to add a key."
  return (
    <Collapsible className="rounded-xl border px-4 py-3">
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="group flex w-full min-w-0 items-start gap-2 text-left"
          aria-label={`${fold.heading}: ${fold.tables.length} ${fold.tables.length === 1 ? "table" : "tables"}`}
        >
          <ChevronRight className="mt-0.5 size-4 flex-none text-muted-foreground transition-transform group-data-[state=open]:rotate-90" />
          <span className="min-w-0 flex-1 break-words font-medium">
            {fold.heading} <span className="font-normal text-muted-foreground">({fold.tables.length})</span>
          </span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="grid gap-2 pt-2 pl-6">
        <p className="text-sm text-muted-foreground break-words">{why}</p>
        <ul className="grid gap-1 text-sm" aria-label="Smaller tables without a primary key">
          {fold.tables.map((t) => (
            <li key={t.table} className="min-w-0 break-words">
              {t.text}
            </li>
          ))}
        </ul>
      </CollapsibleContent>
    </Collapsible>
  )
}
