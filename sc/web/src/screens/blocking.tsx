import { useState } from "react"
import { useParams } from "react-router-dom"
import { Check, Copy, Eye, Loader2, RotateCw, Square, XCircle } from "lucide-react"

import type { BlockingNode, BlockingTree } from "@/api-blocking"
import { copyText, sessionsWord } from "@/components/blocking/blocking-text"
import { ChainNode } from "@/components/blocking/chain-tree"
import { ChainsSeen } from "@/components/blocking/chains-seen"
import { useBlockingWatch, WATCH_SECONDS } from "@/components/blocking/use-blocking-watch"
import { FixDialog } from "@/components/fix-dialog"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { CopyFallback } from "@/components/statement-block"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { useBuildVersion } from "@/lib/build-version"
import { formatWhen } from "@/lib/format"
import { databasePagePath, instancePath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"

// "/instances/:id/databases/:db/blocking" (docs/phase4c-contract.md, part 2): who blocks whom on the
// whole instance right now. The sentences first, then the chains (each session under the one it waits
// for), Kill on each head blocker, the watch (every 3 seconds for 30 seconds, the chains seen kept),
// READ COMMITTED SNAPSHOT at the bottom when it is off, and Copy for the vendor.
export function BlockingScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return <BlockingPage key={`${id}/${database}`} serverId={id} database={database} />
}

type Fixing = { fixId: "kill-session" | "rcsi"; title: string; options?: Record<string, string> }

function BlockingPage({ serverId, database }: { serverId: number; database: string }) {
  const { load } = useServers()
  const w = useBlockingWatch(serverId, database)
  const [fixing, setFixing] = useState<Fixing | null>(null)
  const version = useBuildVersion()
  const { status, fallback, copy, dismiss } = useCopy()

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (w.notFound && !w.tree) return <NotFound what={w.error ?? "There is no such database."} />

  const tree = w.tree
  const watching = w.secondsLeft !== null
  const kill = (n: BlockingNode) =>
    setFixing({ fixId: "kill-session", title: `Kill session ${n.session}`, options: { sessionId: String(n.session) } })

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? null, to: instancePath(serverId) },
            { label: database, to: databasePagePath(serverId, database) },
            { label: "Blocking" },
          ]}
        />
        <header className="grid gap-2">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="grid gap-0.5 min-w-0">
              <h1 className="text-xl font-semibold">Blocking now</h1>
              <p className="text-muted-foreground">Who is waiting for whom on the whole instance, read live.</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {watching ? (
                <Button variant="outline" onClick={w.stop}>
                  <Square />
                  Stop ({w.secondsLeft} s)
                </Button>
              ) : (
                <Button onClick={w.watch}>
                  <Eye />
                  Watch for {WATCH_SECONDS} seconds
                </Button>
              )}
              <Button variant="outline" onClick={() => void w.refresh()} disabled={w.loading || watching}>
                {w.loading && !watching ? <Loader2 className="animate-spin" /> : <RotateCw />}
                Refresh
              </Button>
              {tree && (
                <Button
                  variant="outline"
                  onClick={() => copy(copyText(tree, w.seen, w.watched, formatWhen(tree.capturedAt), version))}
                >
                  {status === "copied" ? <Check /> : <Copy />}
                  {status === "copied" ? "Copied" : "Copy for the vendor"}
                </Button>
              )}
            </div>
          </div>
          {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
          {tree && <StatusLine tree={tree} watching={watching} secondsLeft={w.secondsLeft} loading={w.loading} />}
        </header>
      </div>

      {w.error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the blocking chain</AlertTitle>
          <AlertDescription className="break-words">
            <p>{w.error}</p>
            {!watching && (
              <Button variant="outline" size="sm" className="mt-2" onClick={() => void w.refresh()} disabled={w.loading}>
                Try again
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}

      {tree ? (
        <>
          <Explain tree={tree} />
          {(w.watched || w.seen.some((c) => c.clearedAt)) && <ChainsSeen seen={w.seen} watching={watching} />}
          {tree.heads.length > 0 && (
            <section className="grid gap-3" aria-label="The chains">
              <div className="grid gap-0.5">
                <h2 className="font-medium">The chains</h2>
                <p className="text-sm text-muted-foreground">
                  Each session sits under the one it waits for. Find out who owns a head blocker before you kill it.
                </p>
              </div>
              {tree.heads.map((h) => (
                <div key={h.session} className="rounded-xl border p-3 sm:p-4">
                  <ChainNode node={h} head onKill={kill} />
                </div>
              ))}
            </section>
          )}
          {tree.offerRcsi && (
            <RcsiRow
              database={tree.database}
              onFix={() => setFixing({ fixId: "rcsi", title: "Turn on READ COMMITTED SNAPSHOT" })}
            />
          )}
        </>
      ) : (
        w.loading && <BodySkeleton />
      )}

      {fixing && (
        <FixDialog
          serverId={serverId}
          database={database}
          fixId={fixing.fixId}
          title={fixing.title}
          options={fixing.options}
          onClose={() => setFixing(null)}
          onApplied={() => void w.refresh()}
        />
      )}
    </PageShell>
  )
}

function StatusLine({
  tree,
  watching,
  secondsLeft,
  loading,
}: {
  tree: BlockingTree
  watching: boolean
  secondsLeft: number | null
  loading: boolean
}) {
  const what =
    tree.heads.length === 0
      ? "Nothing blocking"
      : `${sessionsWord(tree.blocked)} waiting in ${tree.heads.length === 1 ? "1 chain" : `${tree.heads.length} chains`}`
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm" role="status">
      <span className={tree.heads.length > 0 ? "font-medium text-amber-700 dark:text-amber-400" : "text-muted-foreground"}>
        {what}
      </span>
      <span className="text-xs text-muted-foreground">
        as of {formatWhen(tree.capturedAt)}
        {tree.serverName && <> on {tree.serverName}</>}
      </span>
      {watching && (
        <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
          {loading && <Loader2 className="size-3 animate-spin" />}
          Watching, every 3 seconds: {secondsLeft} s left
        </span>
      )}
    </div>
  )
}

// The sentences first (docs/phase4c-contract.md): what is blocked, by whom, why, and what to do; then what
// could not be read.
function Explain({ tree }: { tree: BlockingTree }) {
  return (
    <section className="grid gap-2" aria-label="What it means">
      <ul className="grid gap-2">
        {tree.explain.map((s, i) => (
          <li key={i} className={i === 0 ? "font-medium" : "text-sm sm:text-base"}>
            {s}
          </li>
        ))}
      </ul>
      {tree.notes.map((n) => (
        <p key={n} className="text-sm text-muted-foreground break-words">
          {n}
        </p>
      ))}
    </section>
  )
}

function RcsiRow({ database, onFix }: { database: string; onFix: () => void }) {
  return (
    <section className="grid gap-2 rounded-xl border p-3 sm:p-4" aria-label="READ COMMITTED SNAPSHOT">
      <h2 className="font-medium">READ COMMITTED SNAPSHOT is off for {database}</h2>
      <p className="text-sm text-muted-foreground">
        With it on, a query that reads sees the last committed version of the rows instead of waiting for a query that
        is changing them. Writers still wait for writers. The preview says what it costs before anything changes.
      </p>
      <div>
        <Button variant="outline" onClick={onFix}>
          Turn on READ COMMITTED SNAPSHOT
        </Button>
      </div>
    </section>
  )
}

function BodySkeleton() {
  return (
    <div className="grid gap-3" aria-busy>
      <Skeleton className="h-5 w-full max-w-xl" />
      <Skeleton className="h-5 w-4/5 max-w-lg" />
      <Skeleton className="h-40 w-full rounded-xl" />
    </div>
  )
}
