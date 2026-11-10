import { TriangleAlert } from "lucide-react"

import { cn } from "@/lib/utils"

// SQL Server Express changes what can be advised, so it is said plainly wherever the server is shown:
// a badge on the home tile, a notice with the limits under the instance and database headers.
// "Express Edition (64-bit)", "Express Edition with Advanced Services (64-bit)".
export function isExpress(edition: string | null | undefined): boolean {
  return /^\s*Express\b/i.test(edition ?? "")
}

const amber = "border-amber-400 bg-amber-500/10 text-amber-800 dark:border-amber-800 dark:bg-amber-500/15 dark:text-amber-300"

export function ExpressBadge({ className }: { className?: string }) {
  return (
    <span className={cn("inline-flex w-fit items-center gap-1 rounded-md border px-1.5 py-0.5 text-xs font-medium", amber, className)}>
      <TriangleAlert className="size-3.5" />
      SQL Server Express
    </span>
  )
}

// The most data one Express database may hold: 50 GB from SQL Server 2025 (major version 17), 10 GB
// before (Microsoft Learn, "Editions and supported features of SQL Server 2025"; the Go side's
// domain.ExpressLimitMB). An unknown version says 10 GB, the stricter.
export function expressLimitGB(version: string | null | undefined): number {
  const major = parseInt((version ?? "").split(".")[0], 10)
  return major >= 17 ? 50 : 10
}

// The limits that shape the advice (Microsoft Learn, "Editions and supported features of SQL Server":
// compute capacity, maximum memory for the buffer pool, maximum relational database size; and no SQL
// Server Agent in Express).
export function ExpressNotice({ version }: { version?: string | null }) {
  return (
    <div role="note" className={cn("grid gap-1 rounded-md border px-3 py-2 text-sm", amber)}>
      <div className="flex items-center gap-1.5 font-medium">
        <TriangleAlert className="size-4 shrink-0" />
        This is SQL Server Express, the free edition
      </div>
      <ul className="grid gap-0.5 pl-6 list-disc">
        <li>Each database stops at {expressLimitGB(version)} GB of data (the log does not count): inserts fail at the limit.</li>
        <li>It uses at most one CPU socket or four cores, whichever is less, and 1.4 GB of memory for data.</li>
        <li>It has no SQL Server Agent, so backups and maintenance cannot be scheduled on the server itself: the Upkeep card's maintenance procedure does it instead, called by Windows Task Scheduler.</li>
      </ul>
    </div>
  )
}
