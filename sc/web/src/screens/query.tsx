import { useCallback, useEffect, useState, type ReactNode } from "react"
import { useParams } from "react-router-dom"
import { Check, Copy, Info, Loader2, RotateCw, XCircle } from "lucide-react"

import { ApiError } from "@/api"
import { queriesApi, type QueryAction, type QueryDetail } from "@/api-queries"
import { FixDialog } from "@/components/fix-dialog"
import { StillRunningNotice } from "@/components/fix-progress"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { formatMs, formatWhole } from "@/components/query/format"
import { HistoryChart } from "@/components/query/history-chart"
import { PlanSays, PlansTable } from "@/components/query/plans"
import { ExplainList, HistoryTable, StatementsTable, TablesTable, WaitsList } from "@/components/query/sections"
import { shownPlans } from "@/components/query/plan-order"
import { PlanTimeline, VerdictCard } from "@/components/query/verdict"
import { CopyFallback, StatementBlock } from "@/components/statement-block"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { useBuildVersion } from "@/lib/build-version"
import { formatWhen } from "@/lib/format"
import { cardPath, databasePagePath, instancePath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"

// "/instances/:id/databases/:db/queries/:hash" (docs/phase4c-contract.md, part 1; phase5j-contract.md):
// one costly query of a QRY01 finding, read live. First whether it still needs attention (the plan in
// use against the runs before it began, what changed, what to do) and the query text; then the history
// as one chart; then "How it works", folded when the query got faster: the plain sentences, each with the
// fix it names, the plans as a timeline (every plan and what it says behind "Show all plans"), the waits,
// every statement of its procedure and the tables it reads. Every action opens the fix dialog with its
// options; Copy for the vendor hands the whole page over as text.
export function QueryScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  const hash = params.hash ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  if (!hash) return <NotFound what="There is no such query." />
  return <QueryPage key={`${id}/${database}/${hash}`} serverId={id} database={database} hash={hash} />
}

type Load = { loading: boolean; detail: QueryDetail | null; error: string | null; status: number }

function useQueryDetail(serverId: number, database: string, hash: string) {
  const [load, setLoad] = useState<Load>({ loading: true, detail: null, error: null, status: 0 })
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let cancelled = false
    queriesApi
      .queryDetail(serverId, database, hash)
      .then((detail) => !cancelled && setLoad({ loading: false, detail, error: null, status: 200 }))
      .catch(
        (e: Error) =>
          !cancelled &&
          setLoad((l) => ({ ...l, loading: false, error: e.message, status: e instanceof ApiError ? e.status : 0 })),
      )
    return () => {
      cancelled = true
    }
  }, [serverId, database, hash, attempt])
  const refresh = useCallback(() => {
    setLoad((l) => ({ ...l, loading: true, error: null }))
    setAttempt((n) => n + 1)
  }, [])
  return { ...load, refresh }
}

function QueryPage({ serverId, database, hash }: { serverId: number; database: string; hash: string }) {
  const { load } = useServers()
  const q = useQueryDetail(serverId, database, hash)
  const [fixing, setFixing] = useState<QueryAction | null>(null)
  // Phase 4f: the dialog was closed while its long fix (an index, statistics) still runs.
  const [stillRunning, setStillRunning] = useState<string | null>(null)
  const version = useBuildVersion()
  const { status, fallback, copy, dismiss } = useCopy()

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (!q.detail && (q.status === 404 || q.status === 400) && q.error) return <NotFound what={q.error} />

  const d = q.detail
  const handover = d ? `${d.handover}${version ? `\n\nFrom kakapo build ${version}` : ""}` : ""

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? null, to: instancePath(serverId) },
            { label: database, to: databasePagePath(serverId, database) },
            { label: "Performance", to: cardPath(serverId, database, "performance") },
            { label: "Query" },
          ]}
        />
        <header className="grid gap-2">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="grid gap-0.5 min-w-0">
              <h1 className="text-xl font-semibold break-words">
                {d ? (d.objectName ? `A query in ${d.objectName}` : "An ad hoc query") : "A costly query"}
              </h1>
              <p className="font-mono text-xs text-muted-foreground break-all">query hash {hash}</p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {d && (
                <Button variant="outline" onClick={() => copy(handover)}>
                  {status === "copied" ? <Check /> : <Copy />}
                  {status === "copied" ? "Copied" : "Copy for the vendor"}
                </Button>
              )}
              <Button variant="outline" onClick={q.refresh} disabled={q.loading}>
                {q.loading ? <Loader2 className="animate-spin" /> : <RotateCw />}
                Refresh
              </Button>
            </div>
          </div>
          {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
          {d && <SummaryLine detail={d} />}
        </header>
      </div>

      {stillRunning && <StillRunningNotice notice={stillRunning} onDismiss={() => setStillRunning(null)} />}

      {q.error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the query</AlertTitle>
          <AlertDescription className="break-words">
            <p>{q.error}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={q.refresh} disabled={q.loading}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {d ? (
        <QueryBody detail={d} loading={q.loading} onAction={setFixing} />
      ) : (
        q.loading && <BodySkeleton />
      )}

      {fixing && (
        <FixDialog
          serverId={serverId}
          database={database}
          fixId={fixing.fixId}
          title={fixing.title}
          options={fixing.options}
          onClose={(notice) => {
            setFixing(null)
            if (notice) setStillRunning(notice)
          }}
          onApplied={q.refresh}
        />
      )}
    </PageShell>
  )
}

const sourceText: Record<QueryDetail["source"], string> = {
  "query-store": "from Query Store, the last 7 days",
  "plan-cache": "from the plan cache",
  none: "nothing found",
}

function SummaryLine({ detail: d }: { detail: QueryDetail }) {
  const s = d.summary
  return (
    <div className="grid gap-1">
      <p className="text-xs text-muted-foreground">
        {sourceText[d.source]}
        {d.source === "plan-cache" && s.window ? ` (${s.window})` : ""} · as of {formatWhen(d.capturedAt)}
        {d.serverName ? ` · ${d.serverName}` : ""}
      </p>
      {s.runs > 0 && !d.verdict && (
        <dl className="flex flex-wrap gap-x-5 gap-y-1 text-sm">
          <Fact label="Runs" value={formatWhole(s.runs)} />
          <Fact label="Average" value={formatMs(s.avgDurationMs)} />
          <Fact label="Slowest" value={formatMs(s.maxDurationMs)} />
          <Fact label="CPU per run" value={formatMs(s.avgCpuMs)} />
          <Fact label="Reads per run" value={formatWhole(s.avgReads)} />
        </dl>
      )}
    </div>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex gap-1.5">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="font-medium tabular-nums">{value}</dd>
    </div>
  )
}

function Section({ title, note, more, children }: { title: string; note?: string; more?: ReactNode; children: ReactNode }) {
  return (
    <section className="grid gap-3 min-w-0" aria-label={title}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="grid gap-0.5 min-w-0">
          <h2 className="font-medium">{title}</h2>
          {note && <p className="text-sm text-muted-foreground">{note}</p>}
        </div>
        {more}
      </div>
      {children}
    </section>
  )
}

// A link that shows or hides the detail under it ("Show hours", "Show all plans", "Show waits").
function useShow(): [boolean, (label: string) => ReactNode] {
  const [shown, setShown] = useState(false)
  const toggle = (label: string) => (
    <button type="button" className="text-sm text-primary hover:underline" onClick={() => setShown((x) => !x)} aria-expanded={shown}>
      {shown ? `Hide ${label}` : `Show ${label}`}
    </button>
  )
  return [shown, toggle]
}

function QueryBody({
  detail: d,
  loading,
  onAction,
}: {
  detail: QueryDetail
  loading: boolean
  onAction: (a: QueryAction) => void
}) {
  const v = d.verdict
  const [open, setOpen] = useState(!v || v.verdict !== "faster")
  const [hoursShown, hoursToggle] = useShow()
  const [plansShown, plansToggle] = useShow()
  const [waitsShown, waitsToggle] = useShow()
  const timeline = shownPlans(d)
  const hidden = d.plans.length - timeline.length
  return (
    <div className={loading ? "grid gap-6 opacity-60 transition-opacity" : "grid gap-6"}>
      {v ? <VerdictCard verdict={v} text={d.text} /> : d.text && <StatementBlock label="Query text" text={d.text} />}

      {d.history.length > 0 ? (
        <Section
          title="Over time"
          note="Each bar is one hour of the last 7 days. Top: the CPU it used. Bottom: how many times it ran."
          more={hoursToggle("hours")}
        >
          <HistoryChart detail={d} />
          {hoursShown && <HistoryTable hours={d.history} />}
        </Section>
      ) : (
        d.source !== "query-store" && (
          <Section title="Over time">
            <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
              No history: it comes from Query Store, which has nothing on this query in {d.database}.
            </p>
          </Section>
        )
      )}

      <details className="rounded-xl border p-4 min-w-0" open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
        <summary className="flex cursor-pointer flex-wrap items-baseline gap-x-2">
          <h2 className="font-medium">How it works</h2>
          <span className="text-sm text-muted-foreground">what SQL Server did with this query, step by step</span>
        </summary>
        <div className="mt-4 grid gap-6">
      <Section title="What it means">
        <ExplainList detail={d} onAction={onAction} />
      </Section>

      {v && timeline.length > 0 ? (
        <Section
          title="The plans SQL Server used"
          note="A plan is the recipe SQL Server picks to run a query: which tables to read first and which indexes to use. When the data, its statistics or its indexes change, it can pick a new one."
        >
          <PlanTimeline detail={d} />
          <p className="text-sm text-muted-foreground">
            {hidden > 0 ? `${hidden} more ${hidden === 1 ? "plan" : "plans"} ran under five times or not at all this week. ` : ""}
            {plansToggle("all plans")}
          </p>
          {plansShown && (
            <>
              <PlansTable detail={d} onAction={onAction} />
              <PlanSays detail={d} onAction={onAction} />
            </>
          )}
        </Section>
      ) : (
        <>
          <Section
            title="Plans"
            note="Every plan SQL Server kept for this query. Forcing a plan makes SQL Server use it; removing one from the cache makes the next run compile a fresh one."
          >
            <PlansTable detail={d} onAction={onAction} />
          </Section>
          {d.plans.length > 0 && (
            <Section title="What the plan says">
              <PlanSays detail={d} onAction={onAction} />
            </Section>
          )}
        </>
      )}

      {d.waits.length > 0 && (
        <Section
          title="Where it waited"
          note="Its waiting over the last 7 days, added up over every thread, so it can be more than its time. What it means above says what it tells."
          more={waitsToggle("waits")}
        >
          {waitsShown && <WaitsList detail={d} />}
        </Section>
      )}

      {d.objectName && d.statements.length > 0 && (
        <Section
          title={`Every statement in ${d.objectName}`}
          note="From the plan cache since each statement's plan was cached, most page reads first."
        >
          <StatementsTable detail={d} />
        </Section>
      )}

      {d.tables.length > 0 && (
        <Section title="Tables" note="The tables of this database its plans read, with the age of their statistics.">
          <TablesTable detail={d} onAction={onAction} />
        </Section>
      )}

        </div>
      </details>

      {d.notes.length > 0 && (
        <Alert>
          <Info />
          <AlertTitle>Not everything could be read</AlertTitle>
          <AlertDescription>
            <ul className="list-disc pl-5 grid gap-1">
              {d.notes.map((n) => (
                <li key={n} className="break-words">
                  {n}
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function BodySkeleton() {
  return (
    <div className="grid gap-3" aria-busy>
      <Skeleton className="h-5 w-32" />
      <Skeleton className="h-24 w-full rounded-xl" />
      <Skeleton className="h-40 w-full rounded-xl" />
      <Skeleton className="h-32 w-full rounded-xl" />
    </div>
  )
}
