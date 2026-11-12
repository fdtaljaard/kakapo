import { useCallback, useEffect, useState } from "react"
import { Link, useParams, useSearchParams } from "react-router-dom"
import { ChevronLeft, ChevronRight, Loader2, MemoryStick, RotateCw, X } from "lucide-react"

import { ApiError } from "@/api"
import { changesApi, type ChangeFlag, type ChangeRow, type ChangeScreen, type ChangesPage, type MemoryPoint } from "@/api-changes"
import { CopyButton, Verdict } from "@/components/memory-parts"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { StatementBlock } from "@/components/statement-block"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { formatWhen } from "@/lib/format"
import { mbText } from "@/lib/memory-format"
import { cardPath, instancePath, memoryPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// "/instances/:id/changes" (docs/phase5w-contract.md, section C): "Did anything kakapo did, or anything anyone
// else did, line up with this problem?" The problem's time and the verdict first; then SQL Server's memory on
// the same time axis as the changes; then every change, newest first, from kakapo's own log, the settings
// baseline and SQL Server's history. Opening it reads the server; it never changes anything. The query (problem
// time, filter, page) lives in the URL so the page can be linked and refreshed.
export function ChangesScreen() {
  const id = Number(useParams().id)
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  return <ChangesPageView key={id} serverId={id} />
}

type Load =
  | { kind: "loading" }
  | { kind: "ready"; key: string; page: ChangesPage }
  | { kind: "error"; key: string; message: string; notFound: boolean }

function ChangesPageView({ serverId }: { serverId: number }) {
  const { load: servers } = useServers()
  const server = servers.kind === "ready" ? servers.servers.find((s) => s.id === serverId) : undefined
  const [params, setParams] = useSearchParams()
  const problemAt = params.get("problemAt") ?? ""
  const who = (params.get("who") ?? "") as "" | "kakapo" | "outside"
  const flagged = params.get("flagged") === "1"
  const pageNo = Math.max(0, Number(params.get("page") ?? 0) || 0)
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)
  const [typed, setTyped] = useState(problemAt)

  const set = useCallback(
    (next: Record<string, string | undefined>) => {
      const p = new URLSearchParams(params)
      for (const [k, v] of Object.entries(next)) {
        if (v) p.set(k, v)
        else p.delete(k)
      }
      setParams(p)
    },
    [params, setParams],
  )

  // The query this page shows; while a newer one reads, the last answer stays on screen.
  const key = JSON.stringify([problemAt, who, flagged, pageNo, attempt])
  useEffect(() => {
    let cancelled = false
    changesApi
      .page(serverId, { problemAt: problemAt || undefined, who: who || undefined, flagged, page: pageNo })
      .then((page) => !cancelled && setLoad({ kind: "ready", key, page }))
      .catch(
        (e: Error) =>
          !cancelled &&
          setLoad({ kind: "error", key, message: e.message, notFound: e instanceof ApiError && e.status === 404 }),
      )
    return () => {
      cancelled = true
    }
  }, [serverId, problemAt, who, flagged, pageNo, attempt, key])
  const reading = load.kind === "loading" || load.key !== key

  if (servers.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (load.kind === "error" && load.notFound) return <NotFound what={load.message} />
  const page = load.kind === "ready" ? load.page : null

  return (
    <PageShell>
      <div className="grid gap-4">
        <PageCrumbs crumbs={[{ label: server?.name ?? page?.serverName ?? null, to: instancePath(serverId) }, { label: "What changed?" }]} />
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div className="grid min-w-0 gap-0.5">
            <h1 className="text-xl font-semibold">What changed?</h1>
            <p className="text-muted-foreground">
              Everything kakapo did on this server, and every change it saw others make, newest first. This page only reads.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {page && <CopyButton text={page.copy} />}
            <Button variant="outline" disabled={reading} onClick={() => setAttempt((n) => n + 1)}>
              {reading ? <Loader2 className="animate-spin" /> : <RotateCw />}
              Refresh
            </Button>
          </div>
        </header>

        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            set({ problemAt: typed || undefined, page: undefined })
          }}
        >
          <div className="grid gap-1">
            <Label htmlFor="problem-at">When did the problem start? (server time, optional)</Label>
            <Input id="problem-at" type="datetime-local" className="w-56 max-w-full" value={typed} onChange={(e) => setTyped(e.target.value)} />
          </div>
          <Button type="submit" variant="outline" disabled={!typed || typed === problemAt}>
            Show the 7 days before
          </Button>
          {problemAt && (
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setTyped("")
                set({ problemAt: undefined, page: undefined })
              }}
            >
              <X />
              Clear
            </Button>
          )}
        </form>

        {load.kind === "loading" && (
          <div className="grid gap-3" aria-busy="true">
            <p className="text-muted-foreground">Reading the server's settings and SQL Server's history…</p>
            <Skeleton className="h-6 w-3/4" />
            <Skeleton className="h-40 w-full" />
            <Skeleton className="h-64 w-full" />
          </div>
        )}
        {load.kind === "error" && (
          <Alert variant="destructive">
            <AlertTitle>Could not read what changed</AlertTitle>
            <AlertDescription className="break-words">{load.message}</AlertDescription>
          </Alert>
        )}

        {page && (
          <>
            <Verdict tone={page.tone} text={page.verdict} />
            {page.notes.length > 0 && (
              <ul className="grid gap-1 text-sm text-muted-foreground">
                {page.notes.map((n) => (
                  <li key={n} className="break-words">
                    {n}
                  </li>
                ))}
              </ul>
            )}
            <MemoryLine serverId={serverId} page={page} />
            <ChangeList
              serverId={serverId}
              page={page}
              who={who}
              flagged={flagged}
              onWho={(w) => set({ who: w || undefined, page: undefined })}
              onFlagged={(f) => set({ flagged: f ? "1" : undefined, page: undefined })}
              onPage={(n) => set({ page: n > 0 ? String(n) : undefined })}
            />
          </>
        )}
      </div>
    </PageShell>
  )
}

const flagDot: Record<string, string> = {
  red: "bg-destructive",
  amber: "bg-amber-500",
  grey: "bg-muted-foreground",
}

const flagStroke: Record<string, string> = {
  red: "stroke-destructive",
  amber: "stroke-amber-500",
  grey: "stroke-muted-foreground",
}

// The memory line (C2.2): SQL Server's memory in use and its target at every read, the cap from the baseline,
// the changes as ticks and the problem's time as a line, all on one time axis; the Memory screen's snapshots
// as points that open them.
function MemoryLine({ serverId, page }: { serverId: number; page: ChangesPage }) {
  const pts = page.memory.points
  const changeTimes = page.rows.map((r) => ({ t: when(r), flag: r.flag ?? "" })).filter((c) => c.t > 0)
  const times = [
    ...pts.map((p) => Date.parse(p.at)),
    ...changeTimes.map((c) => c.t),
    ...(page.problemAt ? [Date.parse(page.problemAt)] : []),
    ...page.memory.snapshots.map((s) => Date.parse(s.at)),
  ]
  const sub = "SQL Server's memory at each of kakapo's reads, on the same time line as the changes below."
  if (pts.length === 0) {
    return (
      <Panel title="SQL Server's memory" sub={sub}>
        <p className="text-sm text-muted-foreground">
          No reading of SQL Server's memory in this window yet. kakapo reads it every time it opens this server.
        </p>
        <MemoryLink serverId={serverId} />
      </Panel>
    )
  }
  const t0 = Math.min(...times)
  const t1 = Math.max(...times, t0 + 60_000)
  const maxMB = Math.max(1, ...pts.flatMap((p) => [p.sqlMB ?? 0, p.targetMB ?? 0, p.capMB ?? 0]))
  const W = 1000
  const H = 160
  const x = (t: number) => ((t - t0) / (t1 - t0)) * W
  const y = (mb: number) => H - (mb / maxMB) * (H - 8)
  const path = (pick: (p: MemoryPoint) => number | undefined, step = false) => {
    let d = ""
    let prev: number | undefined
    for (const p of pts) {
      const v = pick(p)
      if (v === undefined) {
        prev = undefined
        continue
      }
      const px = x(Date.parse(p.at))
      if (prev === undefined) d += `M${px},${y(v)}`
      else if (step) d += `H${px}V${y(v)}`
      else d += `L${px},${y(v)}`
      prev = v
    }
    return d
  }
  const last = pts[pts.length - 1]
  return (
    <Panel title="SQL Server's memory" sub={sub}>
      <div className="grid gap-1">
        <div className="flex flex-wrap justify-between gap-x-4 text-xs text-muted-foreground">
          <span>{mbText(maxMB)}</span>
          <span>
            Last read: in use {last.sqlMB !== undefined ? mbText(last.sqlMB) : "not read"}, target{" "}
            {last.targetMB !== undefined ? mbText(last.targetMB) : "not read"}, cap {last.capMB !== undefined ? mbText(last.capMB) : "none"}
          </span>
        </div>
        <svg
          viewBox={`0 0 ${W} ${H + 14}`}
          preserveAspectRatio="none"
          className="h-44 w-full overflow-visible rounded-md border bg-muted/20"
          role="img"
          aria-label={`SQL Server's memory from ${formatWhen(new Date(t0).toISOString())} to ${formatWhen(new Date(t1).toISOString())}`}
        >
          <path d={path((p) => p.capMB, true)} className="fill-none stroke-muted-foreground" strokeDasharray="2 4" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          <path d={path((p) => p.targetMB)} className="fill-none stroke-foreground/40" strokeDasharray="6 4" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          <path d={path((p) => p.sqlMB)} className="fill-none stroke-foreground/80" strokeWidth={2} vectorEffect="non-scaling-stroke" />
          {/* Each reading as a dot while there are few: a single reading draws no line. */}
          {pts.length <= 60 &&
            pts.map((p) =>
              p.sqlMB === undefined ? null : (
                <line
                  key={p.at}
                  x1={x(Date.parse(p.at))}
                  x2={x(Date.parse(p.at))}
                  y1={y(p.sqlMB)}
                  y2={y(p.sqlMB)}
                  className="stroke-foreground/80"
                  strokeWidth={6}
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              ),
            )}
          {changeTimes.map((c, i) => (
            <line
              key={i}
              x1={x(c.t)}
              x2={x(c.t)}
              y1={H + 2}
              y2={H + 12}
              className={flagStroke[c.flag] ?? "stroke-foreground/50"}
              strokeWidth={2}
              vectorEffect="non-scaling-stroke"
            />
          ))}
          {page.problemAt && (
            <line
              x1={x(Date.parse(page.problemAt))}
              x2={x(Date.parse(page.problemAt))}
              y1={0}
              y2={H + 14}
              className="stroke-foreground"
              strokeWidth={1.5}
              vectorEffect="non-scaling-stroke"
            />
          )}
        </svg>
        <div className="flex flex-wrap justify-between gap-x-4 text-xs text-muted-foreground">
          <span>{formatWhen(new Date(t0).toISOString(), "row")}</span>
          <span>{formatWhen(new Date(t1).toISOString(), "row")}</span>
        </div>
      </div>
      <ul className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
        <Key cls="border-t-2 border-foreground/80" label="In use" />
        <Key cls="border-t-2 border-dashed border-foreground/40" label="Target" />
        <Key cls="border-t-2 border-dotted border-muted-foreground" label="Cap (max server memory)" />
        <li className="flex items-center gap-1.5">
          <span aria-hidden className="h-3 w-0.5 bg-foreground/50" />
          A change below
        </li>
        {page.problemAt && (
          <li className="flex items-center gap-1.5">
            <span aria-hidden className="h-3 w-px bg-foreground" />
            The problem, {page.problemLabel}
          </li>
        )}
      </ul>
      {page.memory.gaps.map((g) => (
        <p key={g} className="text-sm text-muted-foreground">
          {g}
        </p>
      ))}
      {page.memory.snapshots.length > 0 && (
        <p className="text-sm">
          <span className="text-muted-foreground">Memory screen snapshots in this window: </span>
          {page.memory.snapshots.map((s, i) => (
            <span key={s.id}>
              {i > 0 && ", "}
              <Link className="underline underline-offset-2" to={`${memoryPath(serverId)}?snapshot=${s.id}`}>
                {formatWhen(s.at)}
              </Link>
            </span>
          ))}
        </p>
      )}
      <MemoryLink serverId={serverId} />
    </Panel>
  )
}

function Key({ cls, label }: { cls: string; label: string }) {
  return (
    <li className="flex items-center gap-1.5">
      <span aria-hidden className={cn("w-5", cls)} />
      {label}
    </li>
  )
}

function MemoryLink({ serverId }: { serverId: number }) {
  return (
    <p className="text-sm">
      <Link className="inline-flex items-center gap-1 underline underline-offset-2" to={memoryPath(serverId)}>
        <MemoryStick className="size-4" />
        Where SQL Server's memory goes now
      </Link>
    </p>
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

function when(r: ChangeRow): number {
  if (r.at) return Date.parse(r.at)
  if (r.to) return Date.parse(r.to)
  return 0
}

// The list (C2.3): the filters, then one row per change, 100 a page, with Older and Newer.
function ChangeList({
  serverId,
  page,
  who,
  flagged,
  onWho,
  onFlagged,
  onPage,
}: {
  serverId: number
  page: ChangesPage
  who: "" | "kakapo" | "outside"
  flagged: boolean
  onWho: (w: "" | "kakapo" | "outside") => void
  onFlagged: (f: boolean) => void
  onPage: (n: number) => void
}) {
  const c = page.counts
  const filters: { key: "" | "kakapo" | "outside"; label: string; n: number }[] = [
    { key: "", label: "All", n: c.all },
    { key: "kakapo", label: "By kakapo", n: c.kakapo },
    { key: "outside", label: "Outside kakapo", n: c.outside },
  ]
  const first = page.page * page.pageSize
  return (
    <Panel
      title="The changes"
      sub={
        page.problemAt
          ? `The 7 days before ${page.problemLabel}, newest first.`
          : "Every change kakapo knows of on this server, newest first."
      }
    >
      <div className="flex flex-wrap items-center gap-2">
        {filters.map((f) => (
          <Button key={f.key} size="sm" variant={who === f.key ? "default" : "outline"} aria-pressed={who === f.key} onClick={() => onWho(f.key)}>
            {f.label} <span className="tabular-nums opacity-70">{f.n}</span>
          </Button>
        ))}
        <Button size="sm" variant={flagged ? "default" : "outline"} aria-pressed={flagged} onClick={() => onFlagged(!flagged)}>
          Red and amber only <span className="tabular-nums opacity-70">{c.flagged}</span>
        </Button>
      </div>
      {page.rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">No change in this window{who || flagged ? " with this filter" : ""}.</p>
      ) : (
        <ul className="divide-y">
          {page.rows.map((r) => (
            <Row key={r.key} serverId={serverId} r={r} />
          ))}
        </ul>
      )}
      {(page.page > 0 || page.more) && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-muted-foreground">
          <span>
            {first + 1}–{first + page.rows.length} of {page.listed}
          </span>
          <span className="flex gap-2">
            <Button size="sm" variant="outline" disabled={page.page === 0} onClick={() => onPage(page.page - 1)}>
              <ChevronLeft />
              Newer
            </Button>
            <Button size="sm" variant="outline" disabled={!page.more} onClick={() => onPage(page.page + 1)}>
              Older
              <ChevronRight />
            </Button>
          </span>
        </div>
      )}
    </Panel>
  )
}

const screenWords: Record<ChangeScreen, string> = {
  memory: "Memory",
  performance: "Performance",
  backups: "Backups",
  storage: "Storage",
}

const outcomeWords: Record<string, string> = {
  verified: "Done and read back",
  differs: "Done, but the value read back differs from the target",
  failed: "Failed",
  stopped: "Stopped",
  unknown: "Outcome unknown: kakapo stopped while it ran",
}

const sourceWords: Record<ChangeRow["source"], string> = {
  kakapo: "kakapo's own log",
  baseline: "kakapo's settings baseline (two reads of the server compared)",
  trace: "SQL Server's default trace",
  "by hand": "Recorded by hand in kakapo",
}

function Row({ serverId, r }: { serverId: number; r: ChangeRow }) {
  const flag: ChangeFlag = r.flag ?? ""
  const whenText = r.at ? formatWhen(r.at, "row") : r.from && r.to ? `Between ${formatWhen(r.from)} and ${formatWhen(r.to)}` : ""
  const links = r.screens
    .map((s) => (s === "memory" ? { s, to: memoryPath(serverId) } : r.database ? { s, to: cardPath(serverId, r.database, s) } : null))
    .filter((l): l is { s: ChangeScreen; to: string } => l !== null)
  const hasFold = Boolean(r.statement || r.before || r.after || r.outcome || r.result)
  return (
    <li className="grid grid-cols-1 gap-1 py-3 sm:grid-cols-[11rem_minmax(0,1fr)] sm:gap-x-4">
      <div className="text-sm text-muted-foreground tabular-nums">{whenText}</div>
      <div className="grid min-w-0 gap-1">
        <p className="flex min-w-0 items-start gap-2">
          <span
            aria-label={flag ? `${flag} flag` : undefined}
            className={cn("mt-1.5 size-2 shrink-0 rounded-full", flag ? flagDot[flag] : "bg-transparent")}
          />
          <span className="min-w-0 wrap-anywhere">
            {r.headline}
            {r.outcome === "unknown" || r.outcome === "failed" || r.outcome === "stopped" ? (
              <span className={r.outcome === "failed" ? "text-destructive" : "text-amber-700 dark:text-amber-400"}>
                {" "}
                · {outcomeWords[r.outcome]}
              </span>
            ) : null}
          </span>
        </p>
        <div className="grid min-w-0 gap-0.5 pl-4 text-sm">
          {r.tell && <p className="wrap-anywhere">{r.tell}</p>}
          <p className="wrap-anywhere text-muted-foreground">
            {r.who}
            <span> · {r.database ? r.database : "server setting"}</span>
          </p>
          {links.length > 0 && (
            <p className="flex flex-wrap gap-x-3 text-xs">
              {links.map((l) => (
                <Link key={l.s} className="text-primary underline-offset-2 hover:underline" to={l.to}>
                  {screenWords[l.s]}
                </Link>
              ))}
            </p>
          )}
          {hasFold && (
            <details className="min-w-0">
              <summary className="cursor-pointer text-xs text-muted-foreground">Details</summary>
              <dl className="mt-2 grid gap-2 text-sm">
                {(r.before || r.after) && (
                  <Fact label="Before and after">
                    {r.before || "not read"} → {r.after || "not read"}
                  </Fact>
                )}
                {r.outcome && <Fact label="Outcome">{outcomeWords[r.outcome] ?? r.outcome}</Fact>}
                {r.result && <Fact label="Result">{r.result}</Fact>}
                <Fact label="Source">{sourceWords[r.source]}</Fact>
                {r.statement && (
                  <div className="min-w-0">
                    <StatementBlock label="Statement" text={r.statement} />
                  </div>
                )}
                {r.rollback && (
                  <div className="min-w-0">
                    <StatementBlock label="Rollback" text={r.rollback} />
                  </div>
                )}
              </dl>
            </details>
          )}
        </div>
      </div>
    </li>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="wrap-anywhere">{children}</dd>
    </div>
  )
}
