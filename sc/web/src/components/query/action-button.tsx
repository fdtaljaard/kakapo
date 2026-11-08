import { Archive, Database, Eraser, Pin, PinOff, Plus, PowerOff, Wrench } from "lucide-react"

import type { QueryAction } from "@/api-queries"
import { Button } from "@/components/ui/button"

const icons: Record<string, typeof Wrench> = {
  "force-plan": Pin,
  "unforce-plan": PinOff,
  "evict-plan": Eraser,
  "update-statistics-table": Database,
  "create-index": Plus,
  "archive-table": Archive,
  "disable-index": PowerOff,
}

// One action of the query page: opens the fix dialog for its fix with its options (the preview reads
// everything again before it shows a statement).
export function ActionButton({
  action,
  onAction,
  variant = "outline",
}: {
  action: QueryAction
  onAction: (a: QueryAction) => void
  variant?: "outline" | "default" | "secondary"
}) {
  const Icon = icons[action.fixId] ?? Wrench
  return (
    <Button variant={variant} size="sm" onClick={() => onAction(action)}>
      <Icon />
      {action.label}
    </Button>
  )
}
