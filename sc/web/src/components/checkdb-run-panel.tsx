import { CircleCheck, ShieldCheck, TriangleAlert } from "lucide-react"

import type { CheckdbRun } from "@/api"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { formatWhen } from "@/lib/format"
import { cn } from "@/lib/utils"

// "42 s", "12 min", "1 h 05 min".
function duration(seconds: number): string {
  const s = Math.round(seconds)
  if (s < 120) return `${s} s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min`
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")} min`
}

// The last DBCC CHECKDB run from kakapo on the Backups card (docs/phase4d-contract.md, part 2): when,
// how long, physical only or full, and the outcome ("No errors found" or what SQL Server reported, with
// its messages), or the note that none ran yet. Run CHECKDB now opens the checkdb-now fix: a preview
// with the statement and the room warning, then the run itself, which can take hours on a large
// database.
export function CheckdbRunPanel({ run, onRun }: { run: CheckdbRun | null | undefined; onRun: () => void }) {
  return (
    <section className="grid gap-3" aria-label="Integrity check">
      <div className="grid gap-0.5">
        <h2 className="font-medium">Integrity check (CHECKDB)</h2>
        <p className="text-sm text-muted-foreground">
          DBCC CHECKDB reads every page of the database and reports damage. A run from here is kept and shown below;
          SQL Server also records the last clean check (BAK03).
        </p>
      </div>

      {run ? (
        <Card className={cn(!run.clean && "border-destructive/50")}>
          <CardContent className="grid gap-2">
            <div className="flex items-start gap-2">
              {run.clean ? (
                <CircleCheck className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
              ) : (
                <TriangleAlert className="mt-0.5 size-4 shrink-0 text-destructive" />
              )}
              <p className={cn("font-medium break-words", !run.clean && "text-destructive")}>{run.outcome}</p>
            </div>
            <p className="text-xs text-muted-foreground">
              {formatWhen(run.ranAt)}, took {duration(run.durationSeconds)},{" "}
              {run.physicalOnly ? "physical only (WITH PHYSICAL_ONLY)" : "full check"}
            </p>
            {!run.clean && (
              <p className="text-sm">
                The damage is real: tell a DBA and IT today (BAK04), make sure good backups exist, and do not clear
                the suspect-page records.
              </p>
            )}
            {run.messages.length > 0 && (
              <pre className="max-h-64 overflow-auto rounded-md bg-muted p-2 text-xs whitespace-pre-wrap break-words">
                {run.messages.join("\n")}
              </pre>
            )}
          </CardContent>
        </Card>
      ) : (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          Not run from kakapo yet. Run CHECKDB now previews the statement first; it runs online but loads the disk
          and tempdb, so a quiet hour is better.
        </p>
      )}

      <div>
        <Button variant="outline" onClick={onRun}>
          <ShieldCheck />
          Run CHECKDB now
        </Button>
      </div>
    </section>
  )
}
