import { useCallback, useEffect, useState } from "react"
import { Link, useParams } from "react-router-dom"
import { Database, Loader2, RotateCw } from "lucide-react"

import { ApiError } from "@/api"
import { memoryApi, type MemoryClerks, type MemoryDBs, type MemoryGlance, type MemoryPage, type MemoryView } from "@/api-memory"
import { CopyButton, ReadingLine, SnapshotList, Verdict } from "@/components/memory-parts"
import { useSlowRead } from "@/hooks/use-slow-read"
import { diffText, gbText, readWhen } from "@/lib/memory-format"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { formatAgo, formatWhen } from "@/lib/format"
import { cardPath, instancePath, memoryDatabasePath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// "/instances/:id/memory" (docs/phase5v-contract.md): where SQL Server's memory goes, and whether anything
// is short of it. The verdict first; then one panel per layer, each one deeper: Windows and SQL Server at a
// glance, the memory clerks inside SQL Server, the databases in the cache. Panels 2 and 3 are read on page
// load (instant); the databases come from the latest saved snapshot, and are read again only on a click.
export function MemoryScreen() {
  const id = Number(useParams().id)
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  return <MemoryPageView key={id} serverId={id} />
}

type Load = { kind: "loading" } | { kind: "ready"; page: MemoryPage } | { kind: "error"; message: string; notFound: boolean }

function MemoryPageView({ serverId }: { serverId: number }) {
  const { load: servers } = useServers()
  const server = servers.kind === "ready" ? servers.servers.find((s) => s.id === serverId) : undefined
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [compare, setCompare] = useState<MemoryPage | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [readError, setReadError] = useState<string | null>(null)
  const slow = useSlowRead()

  const open = useCallback(
    (snapshotId?: number) => {
      setLoad({ kind: "loading" })
      memoryApi
        .page(serverId, snapshotId)
        .then((page) => setLoad({ kind: "ready", page }))
        .catch((e: Error) =>
          setLoad({ kind: "error", message: e.message, notFound: e instanceof ApiError && (e.status === 404 || e.status === 400) }),
        )
    },
    [serverId],
  )
  useEffect(() => open(), [open])

  const readDatabases = async () => {
    setNotice(null)
    setReadError(null)
    try {
      const page = await slow.run((signal) => memoryApi.readDatabases(serverId, signal))
      if (page) setLoad({ kind: "ready", page })
      else setNotice("Stopped. SQL Server ended the read and nothing was kept.")
    } catch (e) {
      setReadError(e instanceof Error ? e.message : String(e))
    }
  }

  const compareWith = (id: number) => {
    memoryApi
      .page(serverId, id)
      .then(setCompare)
      .catch((e: Error) => setReadError(e.message))
  }

  if (servers.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (load.kind === "error" && load.notFound) return <NotFound what={load.message} />

  const page = load.kind === "ready" ? load.page : null
  const view = page?.view
  const then = compare && compare.snapshotId !== page?.snapshotId ? compare.view : null

  return (
    <PageShell>
      <div className="grid gap-4">
        <PageCrumbs crumbs={[{ label: server?.name ?? page?.serverName ?? null, to: instancePath(serverId) }, { label: "Memory" }]} />
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="grid min-w-0 gap-0.5">
            <h1 className="text-xl font-semibold">Memory</h1>
            <p className="text-muted-foreground">
              Where SQL Server's memory goes, and whether anything is short of it. Full is normal: SQL Server keeps what it has
              read. This page only reads.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {view && <CopyButton text={view.handover} />}
            <Button variant="outline" disabled={load.kind === "loading" || slow.reading} onClick={() => open()}>
              {load.kind === "loading" ? <Loader2 className="animate-spin" /> : <RotateCw />}
              Refresh
            </Button>
          </div>
        </header>

        {load.kind === "loading" && (
          <div className="grid gap-3" aria-busy="true">
            <p className="text-muted-foreground">Reading SQL Server's memory…</p>
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        )}
        {load.kind === "error" && (
          <Alert variant="destructive">
            <AlertTitle>Could not read the memory</AlertTitle>
            <AlertDescription className="break-words">{load.message}</AlertDescription>
          </Alert>
        )}

        {page && view && (
          <>
            {!page.live && (
              <div role="note" className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-muted/40 px-3 py-2 text-sm">
                <span>
                  Showing the snapshot read {formatWhen(view.takenAt)} ({formatAgo(view.takenAt)}), as it was then.
                </span>
                <Button size="xs" variant="outline" onClick={() => open()}>
                  Back to now
                </Button>
              </div>
            )}
            <Verdict tone={view.tone} text={view.verdict} />
            {then && (
              <div role="note" className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-muted/40 px-3 py-2 text-sm">
                <span>
                  Compared with the snapshot of {formatWhen(then.takenAt)} ({formatAgo(then.takenAt)}): its numbers are beside
                  each clerk group and database.
                </span>
                <Button size="xs" variant="outline" onClick={() => setCompare(null)}>
                  Stop comparing
                </Button>
              </div>
            )}
            {!view.permission && (
              <>
                <Glance g={view.glance} capTo={server?.database ? cardPath(serverId, server.database, "setup") : undefined} />
                <Clerks c={view.clerks} then={then?.clerks} />
                <Databases
                  serverId={serverId}
                  page={page}
                  then={then}
                  reading={slow.reading}
                  elapsed={slow.elapsed}
                  notice={notice}
                  error={readError}
                  onRead={readDatabases}
                  onStop={slow.stop}
                />
                <SnapshotList
                  snapshots={page.snapshots}
                  shown={page.live ? 0 : page.snapshotId}
                  compared={then ? compare?.snapshotId : undefined}
                  onOpen={(id) => open(id)}
                  onCompare={compareWith}
                />
              </>
            )}
          </>
        )}
      </div>
    </PageShell>
  )
}

function Panel({ title, sub, children }: { title: string; sub?: string; children: React.ReactNode }) {
  return (
    <section className="grid min-w-0 gap-3 rounded-xl border px-4 py-4">
      <div className="grid gap-0.5">
        <h2 className="font-medium">{title}</h2>
        {sub && <p className="text-sm text-muted-foreground">{sub}</p>}
      </div>
      {children}
    </section>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="break-words">{children}</dd>
    </div>
  )
}

const seconds = new Intl.NumberFormat("en-US")

function pleText(s: number): string {
  if (s >= 3600) return `${seconds.format(s)} s (${(s / 3600).toFixed(1)} hours)`
  if (s >= 120) return `${seconds.format(s)} s (${Math.round(s / 60)} minutes)`
  return `${seconds.format(s)} s`
}

// Panel 2: one bar of the machine's RAM, split into SQL Server, everything else and free; the facts under it.
function Glance({ g, capTo }: { g: MemoryGlance; capTo?: string }) {
  const total = Math.max(g.totalGB, g.sqlGB + g.otherGB + g.freeGB, 0.01)
  const parts = [
    { label: "SQL Server", gb: g.sqlGB, cls: "bg-foreground/80" },
    { label: "Everything else", gb: g.otherGB, cls: "bg-foreground/35" },
    { label: "Free", gb: g.freeGB, cls: "bg-muted" },
  ]
  return (
    <Panel title="Memory at a glance" sub={`The machine's ${gbText(g.totalGB)} of RAM.`}>
      <div className="flex h-4 w-full overflow-hidden rounded-md border" role="img" aria-label={parts.map((p) => `${p.label} ${gbText(p.gb)}`).join(", ")}>
        {parts.map((p) => (
          <div key={p.label} className={p.cls} style={{ width: `${(p.gb / total) * 100}%` }} />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
        {parts.map((p) => (
          <li key={p.label} className="flex items-center gap-1.5">
            <span aria-hidden className={cn("size-2.5 rounded-sm border", p.cls)} />
            {p.label} <span className="font-medium">{gbText(p.gb)}</span>
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        "Everything else" is the RAM minus SQL Server minus what is free: Windows and every other program. kakapo cannot see
        which ones from SQL Server.
      </p>
      <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
        <Fact label="SQL Server holds">
          {gbText(g.sqlGB)}
          {g.lockedGB > 0 && <span className="text-muted-foreground"> · of which {gbText(g.lockedGB)} in locked pages</span>}
        </Fact>
        <Fact label="Target">
          {gbText(g.targetGB)} <span className="text-muted-foreground">· committed now {gbText(g.committedGB)}</span>
        </Fact>
        <Fact label="Cap (max server memory)">
          {g.cap}
          {capTo && (
            <>
              {" "}
              <Link className="text-muted-foreground underline underline-offset-2" to={capTo}>
                change it on the Setup card
              </Link>
            </>
          )}
        </Fact>
        <Fact label="Min server memory">{g.min}</Fact>
        <Fact label="Up">
          {g.uptime || "unknown"}
          {g.startedAt && !g.startedAt.startsWith("0001") && <span className="text-muted-foreground"> · since {formatWhen(g.startedAt)}</span>}
        </Fact>
        <Fact label="Memory model">
          {g.memoryModel || "not shown before SQL Server 2016 SP1"}
          {g.lockPages ? " · Lock Pages in Memory on" : " · Lock Pages in Memory off"}
        </Fact>
        <Fact label="Windows says">{g.windowsState || "unknown"}</Fact>
        <Fact label={g.nodePLE.length > 1 ? "Page life expectancy, per NUMA node" : "Page life expectancy"}>
          {g.nodePLE.length === 0
            ? g.counterError
              ? `not read: ${g.counterError}`
              : "not read"
            : g.nodePLE.map((n) => (
                <span key={n.node} className="block">
                  {g.nodePLE.length > 1 && <span className="text-muted-foreground">Node {n.node}: </span>}
                  {pleText(n.seconds)}
                </span>
              ))}
        </Fact>
        <Fact label="Queries waiting for memory">{g.counterError ? `not read: ${g.counterError}` : g.grantsPending}</Fact>
      </dl>
      {g.lockPagesNote && <p className="text-sm">{g.lockPagesNote}</p>}
      {g.express && <p className="rounded-md border bg-muted/40 px-3 py-2 text-sm">{g.express}</p>}
    </Panel>
  )
}

function Row({
  name,
  gb,
  share,
  then,
  children,
}: {
  name: React.ReactNode
  gb: number
  share: number
  then?: number
  children?: React.ReactNode
}) {
  return (
    <li className="grid gap-0.5 py-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-0.5">
        <span className="min-w-0 break-all font-medium">{name}</span>
        <span className="flex flex-wrap items-baseline gap-x-3 text-sm">
          <span className="font-medium tabular-nums">{gbText(gb)}</span>
          <span className="w-12 text-right text-muted-foreground tabular-nums">{share.toFixed(1)}%</span>
          {then !== undefined && (
            <span className="text-muted-foreground tabular-nums">
              then {gbText(then)} · {diffText(gb, then)}
            </span>
          )}
        </span>
      </div>
      {children}
    </li>
  )
}

function ReconcileLine({ gap, text }: { gap?: string; text: string }) {
  return <p className={cn("text-sm", gap ? "text-amber-700 dark:text-amber-400" : "text-muted-foreground")}>{gap || text}</p>
}

const groupNote: Record<string, string> = {
  "Data cache": "Pages of tables and indexes, kept after they are read.",
  "Plan cache": "Compiled query plans.",
  Locks: "The lock manager.",
  "Query workspace": "Memory for sorts and hashes while queries run.",
  Other: "Everything else SQL Server keeps for itself.",
  "Free inside SQL Server": "Committed by SQL Server, not yet handed to any of the above.",
}

// Panel 3: the memory clerks, grouped, with the plan cache's single-use plans and the top 10 clerks folded.
function Clerks({ c, then }: { c: MemoryClerks; then?: MemoryClerks }) {
  const before = new Map(then?.groups.map((g) => [g.name, g.gb]))
  return (
    <Panel title="Inside SQL Server" sub="What SQL Server holds its memory for, by memory clerk.">
      <ul className="divide-y">
        {c.groups.map((g) => (
          <Row key={g.name} name={g.name} gb={g.gb} share={g.share} then={then ? (before.get(g.name) ?? 0) : undefined}>
            <span className="text-xs text-muted-foreground">{groupNote[g.name]}</span>
            {g.name === "Plan cache" && (
              <span className="text-sm">
                Single-use ad hoc plans:{" "}
                {c.planCacheError ? `not read: ${c.planCacheError}` : <span className="font-medium">{gbText(c.singleUseAdhocGB)}</span>}
                <span className="text-muted-foreground"> · optimize for ad hoc workloads is {c.adHocOn ? "on" : "off"}</span>
              </span>
            )}
            {g.name === "Query workspace" && c.grantedWorkspaceGB > 0 && (
              <span className="text-sm">Granted to running queries now: {gbText(c.grantedWorkspaceGB)}</span>
            )}
          </Row>
        ))}
      </ul>
      <ReconcileLine gap={c.reconcile} text={c.reconcileText} />
      {c.top.length > 0 && (
        <details className="min-w-0">
          <summary className="cursor-pointer text-sm">The {c.top.length} largest clerks</summary>
          <ul className="mt-2 grid gap-1 text-sm">
            {c.top.map((k) => (
              <li key={k.type} className="flex flex-wrap items-baseline justify-between gap-x-3">
                <code className="font-mono text-xs break-all">{k.type}</code>
                <span className="tabular-nums">
                  {gbText(k.gb)} <span className="text-muted-foreground">· {k.group}</span>
                </span>
              </li>
            ))}
          </ul>
        </details>
      )}
    </Panel>
  )
}

// Panel 4: the buffer pool by database, from the latest snapshot; read again only on a click.
function Databases({
  serverId,
  page,
  then,
  reading,
  elapsed,
  notice,
  error,
  onRead,
  onStop,
}: {
  serverId: number
  page: MemoryPage
  then: MemoryView | null
  reading: boolean
  elapsed: number
  notice: string | null
  error: string | null
  onRead: () => void
  onStop: () => void
}) {
  const d: MemoryDBs | null = page.view.databases
  const before = new Map(then?.databases?.rows.map((r) => [r.name, r.gb]))
  const action = reading ? (
    <ReadingLine what="Reading the cache by database" elapsed={elapsed} onStop={onStop} />
  ) : (
    page.live && (
      <div>
        <Button variant={d ? "outline" : "default"} onClick={onRead}>
          {d ? <RotateCw /> : <Database />}
          {d ? "Refresh snapshot" : "Read the cache by database"}
        </Button>
      </div>
    )
  )
  return (
    <Panel
      title="Databases"
      sub={
        d
          ? `${readWhen(d.readAt, d.seconds)}. Which databases have data in the cache, largest first.`
          : "Which databases have data in the cache. Reading it goes once over every page in the cache: seconds on a small server, minutes on a large one, so it runs only when you ask. Each read is kept as a snapshot."
      }
    >
      {action}
      {notice && <p className="text-sm text-muted-foreground">{notice}</p>}
      {error && <p className="text-sm break-words text-destructive">{error}</p>}
      {d && (
        <>
          <ul className="divide-y">
            {d.rows.map((r) => (
              <Row
                key={r.id}
                name={
                  r.user ? (
                    <Link className="underline underline-offset-2" to={memoryDatabasePath(serverId, r.name)}>
                      {r.name}
                    </Link>
                  ) : (
                    r.name
                  )
                }
                gb={r.gb}
                share={r.share}
                then={then?.databases ? (before.get(r.name) ?? 0) : undefined}
              >
                {r.flag && (
                  <span className={r.flagNote ? "text-sm text-muted-foreground" : "text-sm text-amber-700 dark:text-amber-400"}>
                    {r.flag}
                  </span>
                )}
              </Row>
            ))}
          </ul>
          <ReconcileLine gap={d.reconcile} text={d.reconcileText} />
          {then && !then.databases && <p className="text-sm text-muted-foreground">The compared snapshot did not read the databases.</p>}
        </>
      )}
    </Panel>
  )
}
