import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Link, useParams } from "react-router-dom"
import { Check, Copy, Database, Loader2, RotateCw, Ruler, Square } from "lucide-react"

import { ApiError } from "@/api"
import { indexesApi, type IndexLens, type LensRow } from "@/api-indexes"
import { queriesApi, type IndexShape, type QueryAction } from "@/api-queries"
import { FixDialog } from "@/components/fix-dialog"
import { StillRunningNotice } from "@/components/fix-progress"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { formatWhole } from "@/components/query/format"
import { CopyFallback } from "@/components/statement-block"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { formatWhen } from "@/lib/format"
import { databasePagePath, instancePath, queryPath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"

// "/instances/:id/databases/:db/indexes" (docs/phase5t-contract.md): the indexes that the slowest queries
// read in full, in one list. The sentence first; then one row per index with the slow queries that read it
// (the evidence), a Measure button (phase 5q's read, never on page load), the estimated saving once
// measured, and a tick. "Reorganise selected" runs the fix reorganize-indexes through the fix dialog.
export function IndexesScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return <IndexesPage key={`${id}/${database}`} serverId={id} database={database} />
}

type Load =
  | { kind: "loading" }
  | { kind: "ready"; lens: IndexLens }
  | { kind: "error"; message: string; notFound: boolean }

type Measure = { busy: boolean; shape?: IndexShape; error?: string }

const keyOf = (r: LensRow) => `${r.schema}.${r.table}.${r.index}`

const bracket = (s: string) => `[${s.replace(/]/g, "]]")}]`

// The fix's `indexes` option: one line per index, the table name, a tab, the index name.
const indexesOption = (rows: LensRow[]) => rows.map((r) => `${bracket(r.schema)}.${bracket(r.table)}\t${r.index}`).join("\n")

const sizeText = (mb: number) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`)

const pagesSaved = (s: IndexShape) => Math.max(s.pages - s.estimatedPages, 0)

type Fixing = { fixId: string; title: string; options: Record<string, string> }

function IndexesPage({ serverId, database }: { serverId: number; database: string }) {
  const { load: servers } = useServers()
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [measures, setMeasures] = useState<Record<string, Measure>>({})
  // What the user ticked or unticked by hand; the rest follows the pre-tick rule.
  const [override, setOverride] = useState<Record<string, boolean>>({})
  const [measuringAll, setMeasuringAll] = useState(false)
  const [fixing, setFixing] = useState<Fixing | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const stopRef = useRef(false)
  const { status, fallback, copy, dismiss } = useCopy()

  const read = useCallback(() => {
    let cancelled = false
    indexesApi
      .candidates(serverId, database)
      .then((lens) => !cancelled && setLoad({ kind: "ready", lens }))
      .catch(
        (e: Error) =>
          !cancelled &&
          setLoad({ kind: "error", message: e.message, notFound: e instanceof ApiError && (e.status === 404 || e.status === 400) }),
      )
    return () => {
      cancelled = true
    }
  }, [serverId, database])

  useEffect(() => {
    setLoad({ kind: "loading" })
    return read()
  }, [read])

  const server = servers.kind === "ready" ? servers.servers.find((s) => s.id === serverId) : undefined

  const lens = load.kind === "ready" ? load.lens : null
  const rows = lens?.rows ?? []

  // Measured rows first, by the pages a reorganise would save; the rest in the server's order.
  const ordered = useMemo(() => {
    const measured = rows.filter((r) => measures[keyOf(r)]?.shape?.measured)
    const rest = rows.filter((r) => !measures[keyOf(r)]?.shape?.measured)
    measured.sort((a, b) => pagesSaved(measures[keyOf(b)].shape!) - pagesSaved(measures[keyOf(a)].shape!))
    return [...measured, ...rest]
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load, measures])

  // The pre-ticks: measured, with a gain, statistics not stale, in this order until the size bound.
  const autoTicked = useMemo(() => {
    const set = new Set<string>()
    if (!lens) return set
    let mb = 0
    for (const r of ordered) {
      const m = measures[keyOf(r)]?.shape
      if (!m?.gain || r.stale || r.blocked || override[keyOf(r)] === false) continue
      if (lens.capGB > 0 && mb + r.sizeMB > lens.capGB * 1024) continue
      mb += r.sizeMB
      set.add(keyOf(r))
    }
    return set
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ordered, measures, override, lens])

  const ticked = (r: LensRow) => (keyOf(r) in override ? override[keyOf(r)] : autoTicked.has(keyOf(r)))
  const chosen = ordered.filter((r) => ticked(r) && !r.blocked)

  const measureOne = async (r: LensRow) => {
    const k = keyOf(r)
    setMeasures((m) => ({ ...m, [k]: { busy: true } }))
    try {
      const shape = await queriesApi.indexShape(serverId, database, r.schema, r.table, r.index)
      setMeasures((m) => ({ ...m, [k]: { busy: false, shape } }))
    } catch (e) {
      setMeasures((m) => ({ ...m, [k]: { busy: false, error: e instanceof Error ? e.message : String(e) } }))
    }
  }

  // One click measures at most measureBatch indexes, one after another; Stop ends it after the one in flight.
  const measureNext = async () => {
    if (!lens) return
    stopRef.current = false
    setMeasuringAll(true)
    const todo = ordered.filter((r) => !measures[keyOf(r)]?.shape).slice(0, lens.measureBatch)
    for (const r of todo) {
      if (stopRef.current) break
      await measureOne(r)
    }
    setMeasuringAll(false)
  }

  const reorganise = () =>
    setFixing({
      fixId: "reorganize-indexes",
      title: `Reorganise ${chosen.length === 1 ? "1 index" : `${chosen.length} indexes`}`,
      options: { indexes: indexesOption(chosen) },
    })
  const updateStats = (a: QueryAction) => setFixing({ fixId: a.fixId, title: a.title, options: a.options })

  const afterFix = () => {
    // What was measured is out of date once indexes moved or statistics changed.
    setMeasures({})
    setOverride({})
    setLoad({ kind: "loading" })
    read()
  }

  const measured = rows.filter((r) => measures[keyOf(r)]?.shape?.measured)
  const gains = measured.filter((r) => measures[keyOf(r)].shape!.gain)
  const unmeasured = rows.filter((r) => !measures[keyOf(r)]?.shape)
  const stale = rows.filter((r) => r.stale)
  const savedTotal = gains.reduce((n, r) => n + pagesSaved(measures[keyOf(r)].shape!), 0)

  if (servers.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (load.kind === "error" && load.notFound) return <NotFound what={load.message} />

  return (
    <PageShell>
      <div className="grid gap-4">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? null, to: instancePath(serverId) },
            { label: database, to: databasePagePath(serverId, database) },
            { label: "Indexes worth reorganising" },
          ]}
        />
        <header className="grid gap-2">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="grid gap-0.5 min-w-0">
              <h1 className="text-xl font-semibold">Indexes worth reorganising</h1>
              <p className="text-muted-foreground">
                The big indexes your slowest queries of the last 7 days read in full, from Query Store.
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {lens && (
                <Button variant="outline" onClick={() => copy(lens.handover)}>
                  {status === "copied" ? <Check /> : <Copy />}
                  {status === "copied" ? "Copied" : "Copy"}
                </Button>
              )}
              <Button
                variant="outline"
                disabled={load.kind === "loading" || measuringAll}
                onClick={() => {
                  setMeasures({})
                  setOverride({})
                  setLoad({ kind: "loading" })
                  read()
                }}
              >
                {load.kind === "loading" ? <Loader2 className="animate-spin" /> : <RotateCw />}
                Refresh
              </Button>
            </div>
          </div>
          {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
        </header>

        {notice && <StillRunningNotice notice={notice} onDismiss={() => setNotice(null)} />}

        {load.kind === "loading" && (
          <div className="grid gap-3" aria-busy="true">
            <p className="text-muted-foreground">Reading the plans of your slowest queries…</p>
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-24 w-full" />
          </div>
        )}

        {load.kind === "error" && (
          <Alert variant="destructive">
            <AlertTitle>Could not read the indexes</AlertTitle>
            <AlertDescription className="break-words">{load.message}</AlertDescription>
          </Alert>
        )}

        {lens && (
          <>
            <section className="grid gap-2">
              <p className="text-base font-medium break-words" aria-live="polite">
                {measured.length === 0 ? lens.answer : answerAfterMeasuring(rows.length, measured.length, gains.length, savedTotal, lens.gainPct, stale.length)}
              </p>
              {rows.length > 0 && (
                <div className="flex flex-wrap items-center gap-2">
                  {measuringAll ? (
                    <Button
                      variant="outline"
                      onClick={() => {
                        stopRef.current = true
                      }}
                    >
                      <Square />
                      Stop
                    </Button>
                  ) : (
                    unmeasured.length > 0 && (
                      <Button onClick={measureNext}>
                        <Ruler />
                        {measured.length === 0 && unmeasured.length <= lens.measureBatch
                          ? "Measure all"
                          : `Measure the next ${Math.min(unmeasured.length, lens.measureBatch)}`}
                      </Button>
                    )
                  )}
                  <span className="text-sm text-muted-foreground">
                    Measuring reads a sample of each index (all of it under 10,000 pages), so it costs some I/O.
                  </span>
                </div>
              )}
            </section>

            {rows.length > 0 && (
              <ul className="grid gap-3" aria-label="Indexes">
                {ordered.map((r) => (
                  <IndexRow
                    key={keyOf(r)}
                    serverId={serverId}
                    database={database}
                    row={r}
                    measure={measures[keyOf(r)]}
                    ticked={ticked(r)}
                    canChange={lens.canChange}
                    onTick={(v) => setOverride((o) => ({ ...o, [keyOf(r)]: v }))}
                    onMeasure={() => void measureOne(r)}
                    onStats={updateStats}
                  />
                ))}
              </ul>
            )}

            {rows.length > 0 && lens.canChange && (
              <div className="sticky bottom-0 -mx-4 flex flex-wrap items-center justify-between gap-3 border-t bg-background/95 px-4 py-3 backdrop-blur sm:mx-0 sm:rounded-xl sm:border">
                <p className="text-sm" aria-live="polite">
                  {chosen.length === 0
                    ? "Tick the indexes to reorganise."
                    : `${chosen.length} selected, ${formatWhole(chosen.reduce((n, r) => n + r.pages, 0))} pages (${sizeText(chosen.reduce((n, r) => n + r.sizeMB, 0))})`}
                </p>
                <Button disabled={chosen.length === 0} onClick={reorganise}>
                  Reorganise selected
                </Button>
              </div>
            )}
          </>
        )}
      </div>

      {fixing && (
        <FixDialog
          serverId={serverId}
          database={database}
          fixId={fixing.fixId}
          title={fixing.title}
          options={fixing.options}
          onClose={(n) => {
            setFixing(null)
            if (n) setNotice(n)
          }}
          onApplied={afterFix}
        />
      )}
    </PageShell>
  )
}

// "3 of them would shrink by 5% or more: about 18,400 pages fewer to read. The other 3 are already full and in
// order. Statistics on 1 table are old: update them first." While some are not measured yet, it says so.
function answerAfterMeasuring(total: number, measured: number, gains: number, saved: number, gainPct: number, stale: number): string {
  const parts: string[] = []
  if (measured < total) parts.push(`${measured} of ${total} measured.`)
  if (gains > 0) {
    parts.push(
      `${gains === measured && measured === total ? `All ${gains}` : gains} ${gains === 1 ? "would" : "would"} shrink by ${gainPct}% or more: about ${formatWhole(saved)} pages fewer to read.`,
    )
    if (measured === total && gains < total) {
      parts.push(`The other ${total - gains} ${total - gains === 1 ? "is" : "are"} already about as full and in order as they can be.`)
    }
  } else {
    parts.push(measured === total ? "None of them would shrink by " + gainPct + "% or more: index work would not help these queries." : `None so far would shrink by ${gainPct}% or more.`)
  }
  if (stale > 0) parts.push(`Statistics on ${stale} ${stale === 1 ? "table are" : "tables are"} old: update them first.`)
  return parts.join(" ")
}

function IndexRow({
  serverId,
  database,
  row,
  measure,
  ticked,
  canChange,
  onTick,
  onMeasure,
  onStats,
}: {
  serverId: number
  database: string
  row: LensRow
  measure?: Measure
  ticked: boolean
  canChange: boolean
  onTick: (v: boolean) => void
  onMeasure: () => void
  onStats: (a: QueryAction) => void
}) {
  const shape = measure?.shape
  const id = `tick-${row.schema}-${row.table}-${row.index}`.replace(/[^A-Za-z0-9_-]/g, "_")
  return (
    <li className="grid gap-2 rounded-xl border px-4 py-3">
      <div className="flex min-w-0 items-start gap-3">
        {canChange && (
          <Checkbox
            id={id}
            className="mt-1"
            checked={ticked}
            disabled={!!row.blocked}
            onCheckedChange={(v) => onTick(v === true)}
            aria-label={`Reorganise ${row.index} on ${row.name}`}
          />
        )}
        <div className="grid min-w-0 flex-1 gap-1.5">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
            <label htmlFor={id} className="min-w-0 break-words font-medium">
              {row.name}
              <span className="font-normal text-muted-foreground"> · </span>
              <span className="font-mono text-xs font-normal break-all">{row.index}</span>
            </label>
            <span className="text-sm text-muted-foreground">
              {formatWhole(row.pages)} pages · {sizeText(row.sizeMB)}
              {row.kind === "clustered" ? " · the table itself" : ""}
            </span>
          </div>

          {shape?.measured && shape.gain ? (
            <p className="break-words">
              <span className="font-medium">
                About {formatWhole(pagesSaved(shape))} pages fewer ({Math.round(shape.savedPct)}% less to read).
              </span>{" "}
              <span className="text-muted-foreground">
                Pages are {Math.round(shape.density)}% full and {Math.round(shape.fragmentation)}% out of order; reorganised, about{" "}
                {formatWhole(shape.estimatedPages)} pages.
              </span>
            </p>
          ) : shape ? (
            <p className="break-words text-muted-foreground">{shape.text}</p>
          ) : null}
          {measure?.error && <p className="break-words text-destructive">{measure.error}</p>}
          {!shape && (
            <div>
              <Button size="sm" variant="outline" disabled={measure?.busy} onClick={onMeasure}>
                {measure?.busy ? (
                  <>
                    <Loader2 className="animate-spin" />
                    Measuring…
                  </>
                ) : (
                  "Measure"
                )}
              </Button>
            </div>
          )}

          {row.blocked && <p className="break-words text-sm text-amber-700 dark:text-amber-400">{row.blocked}</p>}

          <div className="grid gap-0.5 text-sm">
            <span className="text-muted-foreground">Read in full by:</span>
            <ul className="grid gap-0.5">
              {row.queries.map((q) => (
                <li key={q.hash} className="min-w-0 break-words">
                  <Link className="underline underline-offset-2" to={queryPath(serverId, database, q.hash)}>
                    {q.text || q.hash}
                  </Link>
                  <span className="text-muted-foreground">
                    {" "}
                    · {q.summary}, last ran {formatWhen(q.lastRun)}
                  </span>
                </li>
              ))}
              {row.more > 0 && <li className="text-muted-foreground">and {row.more} more</li>}
            </ul>
          </div>

          {row.last && (
            <p className="break-words text-sm text-muted-foreground">
              Reorganised by kakapo on {formatWhen(row.last.at)}: {row.last.result}. The slowest query's page compares its runs before and after.
            </p>
          )}

          {(row.statsNote || row.statsAction) && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              {row.statsNote && <p className="min-w-0 break-words text-sm text-amber-700 dark:text-amber-400">{row.statsNote}</p>}
              {row.statsAction && canChange && (
                <Button size="sm" variant="outline" onClick={() => onStats(row.statsAction!)}>
                  <Database />
                  Update statistics
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </li>
  )
}
