import { ChevronRight } from "lucide-react"

import type { DesignForeignKeys } from "@/api"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"

// Phase 5n: the Design card's fold (docs/phase5n-contract.md, section 3): the foreign keys without an index
// on tables of 10,000 rows or more where no delete was seen in the window, largest table first, one line
// each ("Lines (1,376,845 rows): no index on OrderId, its key to Orders") and no button: an index there
// costs writes for a benefit not yet seen. The heading says "too early to tell" while the window is 7 days or less. Nothing
// when there is no such key.
export function ForeignKeysFold({ fold }: { fold: DesignForeignKeys }) {
  if (fold.keys.length === 0) return null
  const since = fold.window || "SQL Server started counting"
  const what =
    "These foreign keys have no index. That costs work only when a row is deleted from the table the key points to: SQL Server then reads the whole table listed here to check that no row still points to it."
  const why = fold.tooEarly
    ? `${what} Deletes are counted only since ${since}, which is too short to tell whether that happens here.`
    : `${what} Nothing was deleted from those tables since ${since}, so these indexes have not been needed. Adding them now would slow every insert and update on these tables for no gain.`
  return (
    <Collapsible className="rounded-xl border px-4 py-3">
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="group flex w-full min-w-0 items-start gap-2 text-left"
          aria-label={`${fold.heading}: ${fold.keys.length} foreign ${fold.keys.length === 1 ? "key" : "keys"}`}
        >
          <ChevronRight className="mt-0.5 size-4 flex-none text-muted-foreground transition-transform group-data-[state=open]:rotate-90" />
          <span className="min-w-0 flex-1 break-words font-medium">
            {fold.heading}{" "}
            <span className="font-normal text-muted-foreground">
              ({fold.keys.length})
            </span>
          </span>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="grid gap-2 pt-2 pl-6">
        <p className="text-sm text-muted-foreground break-words">{why}</p>
        <ul className="grid gap-1 text-sm" aria-label="Foreign keys without an index">
          {fold.keys.map((k) => (
            <li key={`${k.table}/${k.name}`} className="min-w-0 break-words" title={k.name}>
              {k.text}
            </li>
          ))}
        </ul>
      </CollapsibleContent>
    </Collapsible>
  )
}
