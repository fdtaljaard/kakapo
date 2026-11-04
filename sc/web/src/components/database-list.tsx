import { useEffect, useState } from "react"
import { Loader2, RotateCw, XCircle } from "lucide-react"

import { api, type DatabaseStatus, type Server } from "@/api"
import { FixDialog } from "@/components/fix-dialog"
import { VerdictText } from "@/components/verdict-text"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"

type Load =
  | { kind: "loading" }
  | { kind: "ready"; databases: DatabaseStatus[] }
  | { kind: "error"; message: string }

// The user databases of one server with their Query Store state. Reads the server when it mounts
// (the card mounts it on expand) and again on Refresh.
export function DatabaseList({ server }: { server: Server }) {
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)
  const [fixing, setFixing] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .listDatabases(server.id)
      .then((databases) => !cancelled && setLoad({ kind: "ready", databases }))
      .catch((e: Error) => !cancelled && setLoad({ kind: "error", message: e.message }))
    return () => {
      cancelled = true
    }
  }, [server.id, attempt])

  function reload() {
    setLoad({ kind: "loading" })
    setAttempt((n) => n + 1)
  }

  return (
    <div className="grid gap-2">
      {load.kind === "loading" && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground py-2">
          <Loader2 className="size-4 animate-spin" />
          Reading databases from the server…
        </div>
      )}

      {load.kind === "error" && (
        <Alert variant="destructive">
          <XCircle className="size-4" />
          <AlertTitle>Could not list the databases</AlertTitle>
          <AlertDescription className="break-words">
            <p>{load.message}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {load.kind === "ready" && (
        <>
          <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
            <span>
              {load.databases.length === 0
                ? "No user databases on this server."
                : `${load.databases.length} user ${load.databases.length === 1 ? "database" : "databases"}`}
            </span>
            <Button variant="ghost" size="xs" onClick={reload}>
              <RotateCw />
              Refresh
            </Button>
          </div>
          {load.databases.length > 0 && (
            <ul className="divide-y">
              {load.databases.map((db) => (
                <DatabaseLine key={db.name} db={db} onFix={() => setFixing(db.name)} />
              ))}
            </ul>
          )}
        </>
      )}

      {fixing && (
        <FixDialog
          serverId={server.id}
          database={fixing}
          fixId="query-store"
          title="Apply recommended Query Store settings"
          onClose={() => setFixing(null)}
          onApplied={reload}
        />
      )}
    </div>
  )
}

// One database's Query Store line: verdict, why (muted) and Apply when the app can fix it. An <li>.
// The Setup card page shows it on its own for the page's database.
export function DatabaseLine({ db, onFix }: { db: DatabaseStatus; onFix: () => void }) {
  const qs = db.queryStore
  const verdict = qs?.verdict ?? "unavailable"
  const fixable = qs !== null && qs.canFix && (verdict === "drift" || verdict === "off")

  // Second line in muted text: why it cannot be fixed here, why it is unavailable, or what drifted.
  let note: string | null = null
  if (qs === null) note = db.error ?? null
  else if (!qs.canFix) note = cannotFixReason(db)
  if (note === null && qs?.verdict === "unavailable" && qs.readonlyReasonText) note = qs.readonlyReasonText
  if (note === null && qs?.verdict === "drift" && qs.drift.length > 0) {
    note = "Differs: " + qs.drift.map((d) => `${d.setting} ${d.current || "not set"} (target ${d.target})`).join(", ")
  }

  return (
    <li className="flex flex-wrap items-start gap-x-4 gap-y-1 py-2.5 text-sm">
      <div className="w-full sm:w-44 shrink-0 font-medium truncate" title={db.name}>
        {db.name}
      </div>
      <div className="grid gap-0.5 grow basis-64 min-w-0">
        {/* The summary already names the build on SQL Server 2016 and 2017 (SCOPE.md 3a rule 4). */}
        <VerdictText verdict={verdict}>Query Store: {qs ? qs.summary : "could not be read"}</VerdictText>
        {note && <div className="text-xs text-muted-foreground pl-5.5 break-words">{note}</div>}
      </div>
      {fixable && (
        <Button variant="outline" size="sm" className="ml-auto" onClick={onFix}>
          Apply recommended settings
        </Button>
      )}
    </li>
  )
}

function cannotFixReason(db: DatabaseStatus): string | null {
  if (db.isSecondaryReplica) return "Secondary replica: set this on the primary."
  if (db.readOnly) return "The database is read-only, so the settings cannot be changed here."
  if (db.state !== "ONLINE") return `The database is not online (${db.state}).`
  return null
}
