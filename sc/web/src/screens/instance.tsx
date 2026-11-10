import { useEffect, useState } from "react"
import { Link, Navigate, useLocation, useNavigate, useParams } from "react-router-dom"
import { ArrowRight, ChevronRight, Loader2, Pencil, RotateCw, XCircle } from "lucide-react"

import { api, type DatabaseStatus, type Server } from "@/api"
import { ExpressNotice, isExpress } from "@/components/express-notice"
import { InstalledHelpers } from "@/components/installed-helpers"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { formatWhen, plural } from "@/lib/format"
import { cameFromHome, databasePagePath, editInstancePath } from "@/lib/paths"

// "/instances/:id": the instance page. Header with the registration and the buttons Edit and Open
// {database}; below it one line "Database: {name}" when the registration names a database, else the
// picker (revision 3). From a Home tile with a database set it goes straight into that database.
export function InstanceScreen() {
  const id = Number(useParams().id)
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  return <InstancePage key={id} serverId={id} />
}

function InstancePage({ serverId }: { serverId: number }) {
  const { load } = useServers()
  const location = useLocation()

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (server?.database && cameFromHome(location.state)) {
    return <Navigate to={databasePagePath(serverId, server.database)} replace />
  }

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs crumbs={[{ label: server?.name ?? null }]} />
        {server ? <InstanceHeader server={server} /> : load.kind === "loading" && <InstanceHeaderSkeleton />}
      </div>

      {load.kind === "error" && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Cannot reach the kakapo backend</AlertTitle>
          <AlertDescription className="break-words">{load.message}</AlertDescription>
        </Alert>
      )}

      {/* A registration that names a database never shows the list; the picker is only for one without.
          Read the databases only once it is clear the page stays, not on the way straight through. */}
      {server &&
        (server.database ? (
          <p className="text-sm break-all">
            <span className="text-muted-foreground">Database:</span>{" "}
            <span className="font-medium">{server.database}</span>
          </p>
        ) : (
          <DatabasePicker server={server} />
        ))}

      {/* Phase 5e: what kakapo created on this server, with Create or Remove (docs/phase5e-contract.md). */}
      {server && <InstalledHelpers server={server} />}
    </PageShell>
  )
}

function InstanceHeader({ server }: { server: Server }) {
  const auth = server.auth === "sql" ? `SQL login as ${server.username ?? ""}` : "Windows authentication"
  const encryption = server.encrypt
    ? server.trustCert
      ? "encryption on, certificate trusted"
      : "encryption on"
    : "encryption off"
  const build = [server.lastEdition, server.lastVersion && `build ${server.lastVersion}`].filter(Boolean).join(", ")

  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="grid gap-1 min-w-0">
        <h1 className="text-xl font-semibold break-words">{server.name}</h1>
        <code className="font-mono text-xs text-muted-foreground break-all">{server.host}</code>
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-sm text-muted-foreground">
          <span className="break-all">{auth}</span>
          <Dot />
          <span>{encryption}</span>
          <Dot />
          <span>{server.lastTestedAt ? `last tested ${formatWhen(server.lastTestedAt)}` : "never tested"}</span>
        </div>
        {build && <div className="text-sm text-muted-foreground">{build}</div>}
        {isExpress(server.lastEdition) && <ExpressNotice />}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button variant="outline" asChild>
          <Link to={editInstancePath(server.id)}>
            <Pencil />
            Edit
          </Link>
        </Button>
        {server.database && (
          <Button asChild className="max-w-full">
            <Link to={databasePagePath(server.id, server.database)}>
              <span className="truncate">Open {server.database}</span>
              <ArrowRight />
            </Link>
          </Button>
        )}
      </div>
    </header>
  )
}

function InstanceHeaderSkeleton() {
  return (
    <header className="grid gap-1 min-w-0">
      <Skeleton className="h-7 w-56 max-w-full" />
      <Skeleton className="h-4 w-40 max-w-full" />
      <Skeleton className="h-4 w-72 max-w-full" />
    </header>
  )
}

function Dot() {
  return <span aria-hidden>·</span>
}

type Load =
  | { kind: "loading" }
  | { kind: "ready"; databases: DatabaseStatus[] }
  | { kind: "error"; message: string }

function DatabasePicker({ server }: { server: Server }) {
  const navigate = useNavigate()
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)
  const [picking, setPicking] = useState<string | null>(null)

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

  // Remember the choice on the registration, then open the database. Not remembering it only
  // means the next visit shows this list again, so a failure is logged and the page opens anyway.
  async function pick(name: string) {
    setPicking(name)
    if (name !== server.database) {
      try {
        await api.rememberDatabase(server.id, name)
      } catch (e) {
        console.error(`Could not remember ${name} on ${server.name}:`, e)
      }
    }
    navigate(databasePagePath(server.id, name))
  }

  if (load.kind === "loading") {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="size-4 animate-spin" />
        Reading databases from the server…
      </div>
    )
  }

  if (load.kind === "error") {
    return (
      <Alert variant="destructive">
        <XCircle />
        <AlertTitle>Could not list the databases</AlertTitle>
        <AlertDescription className="break-words">
          <p>{load.message}</p>
          <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
            Try again
          </Button>
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <section className="grid gap-2">
      <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
        <span>
          {load.databases.length === 0
            ? "No user databases on this server."
            : `Pick a database · ${plural(load.databases.length, "user database", "user databases")}`}
        </span>
        <Button variant="ghost" size="xs" onClick={reload}>
          <RotateCw />
          Refresh
        </Button>
      </div>
      {load.databases.length > 0 && (
        <Card className="py-0 gap-0">
          <ul className="divide-y">
            {load.databases.map((db) => (
              <li key={db.name}>
                <DatabaseRow
                  serverId={server.id}
                  db={db}
                  remembered={db.name === server.database}
                  picking={picking === db.name}
                  disabled={picking !== null}
                  onPick={() => pick(db.name)}
                />
              </li>
            ))}
          </ul>
        </Card>
      )}
    </section>
  )
}

// One line per database: name and state, the Query Store summary as a muted second line.
function DatabaseRow({
  serverId,
  db,
  remembered,
  picking,
  disabled,
  onPick,
}: {
  serverId: number
  db: DatabaseStatus
  remembered: boolean
  picking: boolean
  disabled: boolean
  onPick: () => void
}) {
  const qs = db.queryStore
  const state = [db.state, db.readOnly && "read-only", db.isSecondaryReplica && "secondary replica"]
    .filter(Boolean)
    .join(", ")

  return (
    <Link
      to={databasePagePath(serverId, db.name)}
      aria-disabled={disabled || undefined}
      onClick={(e) => {
        // A plain click remembers the choice first; ctrl- or middle-click opens a tab as a link does.
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
        e.preventDefault()
        if (!disabled) onPick()
      }}
      className="flex items-center gap-3 px-4 py-3 outline-none transition-colors hover:bg-muted/40 focus-visible:bg-muted/40 aria-disabled:cursor-wait"
    >
      <div className="grid gap-0.5 min-w-0 grow">
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
          <span className="font-medium break-all">{db.name}</span>
          <span className="text-xs text-muted-foreground">{state}</span>
          {remembered && <span className="text-xs text-muted-foreground">· last opened</span>}
        </div>
        <div className="text-xs text-muted-foreground break-words">
          {qs ? `Query Store: ${qs.summary}` : `Query Store could not be read${db.error ? `: ${db.error}` : ""}`}
        </div>
      </div>
      {picking ? (
        <Loader2 className="size-4 shrink-0 animate-spin text-muted-foreground" />
      ) : (
        <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
      )}
    </Link>
  )
}
