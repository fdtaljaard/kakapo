import { useState } from "react"
import { Loader2, ScanSearch, XCircle } from "lucide-react"

import { api, type CheckdbRoom } from "@/api"
import { FindingCard } from "@/components/finding-card"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { formatWhen } from "@/lib/format"
import { cn } from "@/lib/utils"

const seconds = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })

// STO10 on the Storage card page (docs/phase3b-contract.md): the last stored result, or the note that
// it has not been run, and a button that runs it now. The estimate is read on request only, because
// DBCC CHECKDB WITH ESTIMATEONLY can take a minute on a large database.
export function CheckdbRoomPanel({
  serverId,
  database,
  room,
  onResult,
}: {
  serverId: number
  database: string
  room: CheckdbRoom | null | undefined
  onResult: (room: CheckdbRoom) => void
}) {
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function run() {
    setRunning(true)
    setError(null)
    try {
      onResult(await api.runCheckdbRoom(serverId, database))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setRunning(false)
    }
  }

  return (
    <section className="grid gap-3" aria-label="Room for CHECKDB" aria-busy={running}>
      <div className="grid gap-0.5">
        <h2 className="font-medium">Room for CHECKDB</h2>
        <p className="text-sm text-muted-foreground">
          Asks SQL Server how much tempdb a full DBCC CHECKDB would need and checks the data volume has room for
          the snapshot it takes.
        </p>
      </div>

      {room ? (
        <div className={cn("grid gap-1.5 transition-opacity", running && "opacity-50")}>
          <p className="text-xs text-muted-foreground">
            as of {formatWhen(room.capturedAt)}, took {seconds.format(room.durationSeconds)} s
          </p>
          <FindingCard finding={room.finding} />
        </div>
      ) : (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          Not run yet. Check room for CHECKDB reads an estimate from SQL Server; it can take a minute on a large
          database.
        </p>
      )}

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not check room for CHECKDB</AlertTitle>
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button variant="outline" onClick={run} disabled={running}>
          {running ? <Loader2 className="animate-spin" /> : <ScanSearch />}
          Check room for CHECKDB
        </Button>
        {running && (
          <span className="text-sm text-muted-foreground" role="status">
            Asking SQL Server for its estimate…
          </span>
        )}
      </div>
    </section>
  )
}
