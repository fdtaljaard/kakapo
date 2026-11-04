import { CircleCheck, CircleMinus, CircleX, TriangleAlert } from "lucide-react"

import type { QueryStoreVerdict } from "@/api"
import { cn } from "@/lib/utils"

const styles: Record<QueryStoreVerdict, { icon: typeof CircleCheck; className: string }> = {
  ok: { icon: CircleCheck, className: "text-emerald-600 dark:text-emerald-400" },
  drift: { icon: TriangleAlert, className: "text-amber-600 dark:text-amber-400" },
  off: { icon: CircleX, className: "text-destructive" },
  unavailable: { icon: CircleMinus, className: "text-muted-foreground" },
}

// A Query Store summary in the colour of its verdict, with the matching icon.
export function VerdictText({
  verdict,
  children,
  className,
}: {
  verdict: QueryStoreVerdict
  children: React.ReactNode
  className?: string
}) {
  const { icon: Icon, className: colour } = styles[verdict]
  return (
    <span className={cn("inline-flex items-start gap-1.5", colour, className)}>
      <Icon className="size-4 shrink-0 translate-y-0.5" />
      <span>{children}</span>
    </span>
  )
}
