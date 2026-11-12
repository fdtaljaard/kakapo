import { useCallback, useEffect, useState } from "react"
import { useParams } from "react-router-dom"
import { Database, RotateCw } from "lucide-react"

import { ApiError } from "@/api"
import { memoryApi, type DatabaseMemory, type DatabaseMemoryPage, type IndexCacheRow } from "@/api-memory"
import { CopyButton, ReadingLine, SnapshotList } from "@/components/memory-parts"
import { useSlowRead } from "@/hooks/use-slow-read"
import { gbText, mbText, readWhen } from "@/lib/memory-format"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { formatWhen } from "@/lib/format"
import { instancePath, memoryPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// "/instances/:id/memory/databases/:db" (docs/phase5v-contract.md, decision 3): one database's share of the
// cache, index by index, hot or cold. Shown from the latest saved snapshot; read again only on a click.
export function MemoryDatabaseScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return <DatabaseView key={`${id}/${database}`} serverId={id} database={database} />
}

type Load =
  | { kind: "loading" }
  | { kind: "ready"; page: DatabaseMemoryPage }
  | { kind: "error"; message: string; notFound: boolean }

function DatabaseView({ serverId, database }: { serverId: number; database: string }) {
  const { load: servers } = useServers()
  const server = servers.kind === "ready" ? servers.servers.find((s) => s.id === serverId) : undefined
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [notice, setNotice] = useState<string | null>(null)
  const [readError, setReadError] = useState<string | null>(null)
  const slow = useSlowRead()

  const open = useCallback(
    (snapshotId?: number) => {
      setLoad({ kind: "loading" })
      memoryApi
        .database(serverId, database, snapshotId)
        .then((page) => setLoad({ kind: "ready", page }))
        .catch((e: Error) =>
          setLoad({ kind: "error", message: e.message, notFound: e instanceof ApiError && (e.status === 404 || e.status === 400) }),
        )
    },
    [serverId, database],
  )
  useEffect(() => open(), [open])

  const read = async () => {
    setNotice(null)
    setReadError(null)
    try {
      const page = await slow.run((signal) => memoryApi.readDatabase(serverId, database, signal))
      if (page) setLoad({ kind: "ready", page })
      else setNotice("Stopped. SQL Server ended the read and nothing was kept.")
    } catch (e) {
      setReadError(e instanceof Error ? e.message : String(e))
    }
  }

  if (servers.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (load.kind === "error" && load.notFound) return <NotFound what={load.message} />

  const page = load.kind === "ready" ? load.page : null
  const d = page?.detail ?? null
  const latest = page?.snapshots[0]?.id ?? 0
  const old = !!d && page!.snapshotId !== 0 && page!.snapshotId !== latest

  return (
    <PageShell>
      <div className="grid gap-4">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? page?.serverName ?? null, to: instancePath(serverId) },
            { label: "Memory", to: memoryPath(serverId) },
            { label: database },
          ]}
        />
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="grid min-w-0 gap-0.5">
            <h1 className="text-xl font-semibold break-all">{database} in memory</h1>
            <p className="text-muted-foreground">Which of its tables and indexes SQL Server keeps in cache, and whether anyone still reads them.</p>
          </div>
          {d && !d.permission && (
            <div className="flex flex-wrap items-center gap-2">
              <CopyButton text={d.handover} />
            </div>
          )}
        </header>

        {load.kind === "loading" && (
          <div className="grid gap-3" aria-busy="true">
            <Skeleton className="h-16 w-full" />
            <Skeleton className="h-40 w-full" />
          </div>
        )}
        {load.kind === "error" && (
          <Alert variant="destructive">
            <AlertTitle>Could not open the snapshot</AlertTitle>
            <AlertDescription className="break-words">{load.message}</AlertDescription>
          </Alert>
        )}

        {page && (
          <>
            <section className="grid gap-2">
              {d ? (
                <>
                  <p className="text-base font-medium break-words">{d.answer}</p>
                  {!d.permission && (
                    <p className="text-sm text-muted-foreground">
                      {readWhen(d.takenAt, d.seconds)}. {d.coldNote}
                    </p>
                  )}
                  {d.usageNote && <p className="text-sm text-amber-700 dark:text-amber-400">{d.usageNote}</p>}
                </>
              ) : (
                <p className="text-base font-medium">
                  This database's cache has not been read yet. Reading goes once over every page of it in cache: seconds on a small
                  server, minutes on a large one.
                </p>
              )}
              {old && (
                <div role="note" className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-muted/40 px-3 py-2 text-sm">
                  <span>Showing an earlier snapshot, read {formatWhen(d!.takenAt)}.</span>
                  <Button size="xs" variant="outline" onClick={() => open()}>
                    Show the latest
                  </Button>
                </div>
              )}
              {slow.reading ? (
                <ReadingLine what={`Reading ${database}'s cache`} elapsed={slow.elapsed} onStop={slow.stop} />
              ) : (
                <div>
                  <Button variant={d ? "outline" : "default"} onClick={read}>
                    {d ? <RotateCw /> : <Database />}
                    {d ? "Refresh snapshot" : "Read this database's cache"}
                  </Button>
                </div>
              )}
              {notice && <p className="text-sm text-muted-foreground">{notice}</p>}
              {readError && <p className="text-sm break-words text-destructive">{readError}</p>}
            </section>

            {d && !d.permission && d.indexes > 0 && <Detail d={d} />}

            <SnapshotList snapshots={page.snapshots} shown={page.snapshotId} onOpen={(id) => open(id)} />
          </>
        )}
      </div>
    </PageShell>
  )
}

const tempClass: Record<IndexCacheRow["temperature"], string> = {
  hot: "bg-foreground/80",
  warm: "bg-foreground/40",
  cold: "bg-muted",
}

const whole = new Intl.NumberFormat("en-US")

function Detail({ d }: { d: DatabaseMemory }) {
  const total = Math.max(d.cachedGB, 0.0001)
  const parts = [
    { label: "Hot", gb: d.hotGB, cls: tempClass.hot, help: "read in the last hour" },
    { label: "Warm", gb: d.warmGB, cls: tempClass.warm, help: "read in the last 24 hours" },
    { label: "Cold", gb: d.coldGB, cls: tempClass.cold, help: "not read in 24 hours" },
  ]
  return (
    <>
      <section className="grid gap-2 rounded-xl border px-4 py-4">
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
              <span className="text-muted-foreground">· {p.help}</span>
            </li>
          ))}
        </ul>
      </section>

      <section className="grid gap-2">
        <h2 className="font-medium">
          Largest in cache <span className="font-normal text-muted-foreground">· {d.rows.length} of {whole.format(d.indexes)} indexes</span>
        </h2>
        <ul className="grid gap-2" aria-label="Indexes in cache">
          {d.rows.map((r, i) => (
            <IndexRow key={`${r.table}.${r.index}.${i}`} r={r} />
          ))}
        </ul>
        {d.rest && (
          <p className="text-sm text-muted-foreground">
            And {whole.format(d.rest.indexes)} more indexes holding {mbText(d.rest.cachedMB)} between them.
          </p>
        )}
      </section>
    </>
  )
}

function IndexRow({ r }: { r: IndexCacheRow }) {
  const reads = [
    `${whole.format(r.seeks)} ${r.seeks === 1 ? "seek" : "seeks"}`,
    `${whole.format(r.scans)} ${r.scans === 1 ? "scan" : "scans"}`,
    `${whole.format(r.lookups)} ${r.lookups === 1 ? "lookup" : "lookups"}`,
    `${whole.format(r.updates)} ${r.updates === 1 ? "update" : "updates"}`,
  ].join(", ")
  return (
    <li className="grid gap-1 rounded-xl border px-4 py-3">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <span className="min-w-0 break-words font-medium">
          {r.table}
          <span className="font-normal text-muted-foreground"> · </span>
          <span className="font-mono text-xs font-normal break-all">{r.index}</span>
        </span>
        <span className="text-sm tabular-nums">
          <span className="font-medium">{mbText(r.cachedMB)}</span> cached of {mbText(r.totalMB)}
          <span className="text-muted-foreground"> ({Math.round(r.cachedPct)}%)</span>
        </span>
      </div>
      <p className="text-sm text-muted-foreground break-words">
        <span className="inline-flex items-center gap-1.5 text-foreground">
          <span aria-hidden className={cn("size-2 rounded-full border", tempClass[r.temperature])} />
          {r.temperature[0].toUpperCase() + r.temperature.slice(1)}
        </span>
        {" · "}
        {r.lastRead ? `last read ${formatWhen(r.lastRead)}` : "not read since the restart"} · {r.type} · {reads} since the restart
      </p>
      {(r.keys || r.includes) && (
        <p className="text-xs text-muted-foreground break-words">
          {r.keys && (
            <>
              Keys: <span className="font-mono text-foreground">{r.keys}</span>
            </>
          )}
          {r.includes && (
            <>
              {r.keys && " · "}Include: <span className="font-mono text-foreground">{r.includes}</span>
            </>
          )}
        </p>
      )}
      {r.flags.length > 0 && (
        <ul className="grid gap-0.5 text-sm text-amber-700 dark:text-amber-400">
          {r.flags.map((f) => (
            <li key={f}>{f}</li>
          ))}
        </ul>
      )}
    </li>
  )
}
