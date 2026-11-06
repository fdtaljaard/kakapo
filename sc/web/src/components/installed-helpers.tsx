import { useEffect, useState } from "react"
import { Loader2, RotateCw, XCircle } from "lucide-react"

import type { HelperState, InstalledHelper, InstalledHelpers as Helpers, Server } from "@/api"
import { helpersApi } from "@/api-helpers"
import { FixDialog } from "@/components/fix-dialog"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"

type Load = { kind: "loading" } | { kind: "ready"; helpers: Helpers } | { kind: "error"; message: string }

const stateBadge: Record<HelperState, { text: string; variant: "default" | "secondary" | "destructive" | "outline" }> = {
  running: { text: "running", variant: "default" },
  stopped: { text: "stopped", variant: "destructive" },
  "not installed": { text: "not installed", variant: "outline" },
  "not kakapo's": { text: "not kakapo's", variant: "secondary" },
  unknown: { text: "could not be read", variant: "secondary" },
}

// The instance page's "Installed helpers" row (docs/phase5e-contract.md; SCOPE section 3 item 9): what kakapo
// has created on this server, one line per helper, the sentence first and the facts under it, with the fix
// the line offers: Create when nothing is installed, Remove when kakapo's event session is there. The fix
// opens the usual preview; a server fix still goes through a database connection, the one the answer names.
// Read live when the page opens and after a fix is applied.
export function InstalledHelpers({ server }: { server: Server }) {
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)
  // The database the fix goes through is kept with the dialog: the reload after an apply clears load, and
  // a dialog that hung on it would close, open again and ask for a fresh preview instead of showing the
  // result.
  const [fixing, setFixing] = useState<{ fixId: string; title: string; database: string } | null>(null)

  useEffect(() => {
    let cancelled = false
    helpersApi
      .read(server.id)
      .then((helpers) => !cancelled && setLoad({ kind: "ready", helpers }))
      .catch((e: Error) => !cancelled && setLoad({ kind: "error", message: e.message }))
    return () => {
      cancelled = true
    }
  }, [server.id, attempt])

  function reload() {
    setLoad({ kind: "loading" })
    setAttempt((n) => n + 1)
  }

  const database = load.kind === "ready" ? load.helpers.database : ""

  return (
    <section className="grid gap-2" aria-label="Installed helpers" aria-busy={load.kind === "loading"}>
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-medium">Installed helpers</h2>
        <Button variant="ghost" size="xs" onClick={reload} disabled={load.kind === "loading"}>
          <RotateCw />
          Refresh
        </Button>
      </div>

      {load.kind === "loading" && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Reading what kakapo installed on the server…
        </div>
      )}

      {load.kind === "error" && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the installed helpers</AlertTitle>
          <AlertDescription className="break-words">
            <p>{load.message}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {load.kind === "ready" && (
        <Card className="py-0 gap-0">
          <ul className="divide-y">
            {load.helpers.helpers.map((h) => (
              <li key={h.id}>
                <HelperLine
                  helper={h}
                  canFix={database !== ""}
                  onFix={() => h.fixId && setFixing({ fixId: h.fixId, title: fixTitle(h), database })}
                />
              </li>
            ))}
          </ul>
        </Card>
      )}

      {fixing && (
        <FixDialog
          serverId={server.id}
          database={fixing.database}
          fixId={fixing.fixId}
          title={fixing.title}
          onClose={() => setFixing(null)}
          onApplied={reload}
        />
      )}
    </section>
  )
}

function fixTitle(h: InstalledHelper): string {
  return h.fixLabel === "Remove" ? "Remove kakapo's event session" : "Keep every block and deadlock on the server"
}

// One helper: its title and state, the sentence, the facts (paths break anywhere so a phone never scrolls
// sideways), and the button.
function HelperLine({ helper: h, canFix, onFix }: { helper: InstalledHelper; canFix: boolean; onFix: () => void }) {
  const badge = stateBadge[h.state] ?? { text: h.state, variant: "secondary" as const }
  return (
    <div className="flex flex-wrap items-start gap-x-3 gap-y-2 px-4 py-3">
      <div className="grid min-w-0 flex-1 basis-64 gap-1">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span className="font-medium break-words">{h.title}</span>
          <Badge variant={badge.variant}>{badge.text}</Badge>
        </div>
        <p className="text-sm break-words">{h.sentence}</p>
        {h.details.length > 0 && (
          <ul className="grid gap-0.5 text-xs text-muted-foreground">
            {h.details.map((d) => (
              <li key={d} className="break-all">
                {d}
              </li>
            ))}
          </ul>
        )}
        {h.fixId && !canFix && (
          <p className="text-xs text-muted-foreground">
            No user database is online, so kakapo has no connection to run the fix through from here.
          </p>
        )}
      </div>
      {h.fixId && (
        <Button variant={h.fixLabel === "Remove" ? "outline" : "default"} size="sm" onClick={onFix} disabled={!canFix}>
          {h.fixLabel === "Remove" ? "Remove…" : "Create…"}
        </Button>
      )}
    </div>
  )
}
