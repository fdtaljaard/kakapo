import { useCallback, useEffect, useState, type ReactNode } from "react"
import { useParams, useSearchParams } from "react-router-dom"
import { Check, Copy, Info, Loader2, RotateCw, XCircle } from "lucide-react"

import { ApiError } from "@/api"
import type { QueryAction } from "@/api-queries"
import { tuningApi, type TuningReport } from "@/api-tuning"
import { FixDialog } from "@/components/fix-dialog"
import { StillRunningNotice } from "@/components/fix-progress"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { CopyFallback } from "@/components/statement-block"
import { ExplainList, Tiles } from "@/components/tuning/explain"
import { FocusSection } from "@/components/tuning/focus"
import { serverDate, serverDay, serverWhen } from "@/components/tuning/format"
import { HourChart } from "@/components/tuning/hour-chart"
import { RecordChangeForm } from "@/components/tuning/record-change-form"
import { BreaksList, QueriesTable, SameHourTable } from "@/components/tuning/tables"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { useServers } from "@/hooks/use-servers"
import { useBuildVersion } from "@/lib/build-version"
import { cardPath, databasePagePath, instancePath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"
import { cn } from "@/lib/utils"

// "/instances/:id/databases/:db/tuning?days=7&change={id}&hours=08-17" (docs/phase5c-contract.md): is the
// database better than before, and did the change do it? Query Store's hour by hour history compared around
// a change of the log (the latest by default, or none: the latest complete business hour). The sentences
// first, each with its buttons; then the four tiles, the hours as CSS bars, the same hour before and after,
// the costly queries with a verdict each, what may have broken, and what could not be read. Reads only;
// Copy for IT hands the tiles, sentences and per-query table over as text. Phase 5h: when the centred change
// touched a procedure, view, index or deploy, "Did it help {object}?" comes before the tiles, with the object's
// own numbers and chart, and its sentence and block lead the sentences and Copy for IT.
export function TuningScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return <TuningPage key={`${id}/${database}`} serverId={id} database={database} />
}

const dayChoices = [7, 14, 30]
const hourChoices = ["06-15", "07-16", "08-17", "09-18", "00-24"]

// The last answer and which request it answers: the page is loading while that is not the current one.
type Load = { key: string; report: TuningReport | null; error: string | null; status: number }

function useTuningReport(serverId: number, database: string, days: number, change: string, hours: string) {
  const [attempt, setAttempt] = useState(0)
  const key = `${days}|${change}|${hours}|${attempt}`
  const [load, setLoad] = useState<Load>({ key: "", report: null, error: null, status: 0 })
  useEffect(() => {
    let cancelled = false
    tuningApi
      .report(serverId, database, {
        days,
        change: change === "" ? undefined : change === "none" ? "none" : Number(change),
        hours,
      })
      .then((report) => !cancelled && setLoad({ key, report, error: null, status: 200 }))
      .catch(
        (e: Error) =>
          !cancelled && setLoad((l) => ({ ...l, key, error: e.message, status: e instanceof ApiError ? e.status : 0 })),
      )
    return () => {
      cancelled = true
    }
  }, [serverId, database, days, change, hours, key])
  const refresh = useCallback(() => setAttempt((n) => n + 1), [])
  const loading = load.key !== key
  return { report: load.report, error: loading ? null : load.error, status: load.status, loading, refresh }
}

function TuningPage({ serverId, database }: { serverId: number; database: string }) {
  const { load } = useServers()
  const [search, setSearch] = useSearchParams()
  const dayParam = Number(search.get("days") ?? 7)
  const days = Number.isInteger(dayParam) && dayParam >= 1 && dayParam <= 30 ? dayParam : 7
  const change = search.get("change") ?? ""
  const hours = search.get("hours") ?? "08-17"
  const t = useTuningReport(serverId, database, days, change, hours)
  const [fixing, setFixing] = useState<QueryAction | null>(null)
  const [stillRunning, setStillRunning] = useState<string | null>(null)
  const [recording, setRecording] = useState(false)
  const version = useBuildVersion()
  const { status, fallback, copy, dismiss } = useCopy()

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />

  const r = t.report
  const set = (name: string, value: string | null) => {
    const next = new URLSearchParams(search)
    if (value === null) next.delete(name)
    else next.set(name, value)
    setSearch(next)
  }
  const handover = r ? `${r.handover}${version ? `\n\nFrom kakapo build ${version}` : ""}` : ""
  const changeNotInWindow = !r && t.status === 404 && change !== "" && change !== "none"

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? null, to: instancePath(serverId) },
            { label: database, to: databasePagePath(serverId, database) },
            { label: "Performance", to: cardPath(serverId, database, "performance") },
            { label: "Tuning report" },
          ]}
        />
        <header className="grid gap-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="grid gap-0.5 min-w-0">
              <h1 className="text-xl font-semibold break-words">Is {database} better than before?</h1>
              <p className="text-muted-foreground break-words">
                {r ? `Centred on ${r.centre.label}.` : "Query Store hour by hour, with the change log."}
              </p>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {r && (
                <Button variant="outline" onClick={() => copy(handover)}>
                  {status === "copied" ? <Check /> : <Copy />}
                  {status === "copied" ? "Copied" : "Copy for IT"}
                </Button>
              )}
              <Button variant="outline" onClick={t.refresh} disabled={t.loading}>
                {t.loading ? <Loader2 className="animate-spin" /> : <RotateCw />}
                Refresh
              </Button>
            </div>
          </div>
          {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
          <Controls
            report={r}
            change={change}
            days={days}
            hours={hours}
            onChange={(v) => set("change", v)}
            onDays={(v) => set("days", v === 7 ? null : String(v))}
            onHours={(v) => set("hours", v === "08-17" ? null : v)}
          />
          {r && (
            <p className="text-xs text-muted-foreground">
              Server time, read {serverWhen(r.capturedAt)}
              {r.serverName ? ` on ${r.serverName}` : ""} · business hours {r.businessHours}, Monday to Friday · Query Store{" "}
              {r.queryStoreState.toLowerCase().replace("_", " ") || "not read"}
              {r.intervalMinutes ? `, ${r.intervalMinutes}-minute intervals` : ""}
            </p>
          )}
        </header>
      </div>

      {stillRunning && <StillRunningNotice notice={stillRunning} onDismiss={() => setStillRunning(null)} />}

      {t.error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the tuning report</AlertTitle>
          <AlertDescription className="break-words">
            <p>{t.error}</p>
            <div className="mt-2 flex flex-wrap gap-2">
              {changeNotInWindow && days < 30 && (
                <Button variant="outline" size="sm" onClick={() => set("days", "30")}>
                  Show 30 days
                </Button>
              )}
              {changeNotInWindow && (
                <Button variant="outline" size="sm" onClick={() => set("change", null)}>
                  The latest change
                </Button>
              )}
              <Button variant="outline" size="sm" onClick={t.refresh} disabled={t.loading}>
                Try again
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      )}

      {recording && (
        <RecordChangeForm
          serverId={serverId}
          database={database}
          onRecorded={(c) => {
            setRecording(false)
            set("change", String(c.id))
          }}
          onCancel={() => setRecording(false)}
        />
      )}

      {r ? (
        <Body
          report={r}
          serverId={serverId}
          loading={t.loading}
          onAction={setFixing}
          onRecordChange={() => setRecording(true)}
        />
      ) : (
        t.loading && <BodySkeleton />
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
          onApplied={t.refresh}
        />
      )}
    </PageShell>
  )
}

// The page's choices: the change it is centred on ("none" is the latest complete business hour), the days
// read and the business hours. Each is in the URL, so a link carries them.
function Controls({
  report,
  change,
  days,
  hours,
  onChange,
  onDays,
  onHours,
}: {
  report: TuningReport | null
  change: string
  days: number
  hours: string
  onChange: (v: string | null) => void
  onDays: (v: number) => void
  onHours: (v: string) => void
}) {
  const centre = change !== "" ? change : report ? (report.centre.changeId ? String(report.centre.changeId) : "none") : ""
  const hourOptions = hourChoices.includes(hours) ? hourChoices : [...hourChoices, hours]
  return (
    <div className="flex flex-wrap items-end gap-3">
      <div className="grid gap-1.5 min-w-0 max-w-full">
        <Label htmlFor="tuning-change">Centred on</Label>
        <Select value={centre} onValueChange={(v) => onChange(v)} disabled={!report}>
          <SelectTrigger id="tuning-change" className="w-72 max-w-full">
            <SelectValue placeholder="The latest change" />
          </SelectTrigger>
          <SelectContent>
            {report?.changes.map((c) => (
              <SelectItem key={c.id} value={String(c.id)}>
                {serverWhen(c.at)} · {c.object}
                {c.byHand ? " (by hand)" : ""}
                {c.onServer ? " (on the server)" : ""}
                {c.serverLevel ? " (server)" : ""}
                {c.performance ? "" : " (not a performance change)"}
              </SelectItem>
            ))}
            <SelectItem value="none">None: the latest business hour</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="grid gap-1.5">
        <span className="text-sm font-medium leading-none">Days</span>
        <div className="flex rounded-lg border p-0.5" role="group" aria-label="Days of history">
          {dayChoices.map((d) => (
            <Button
              key={d}
              size="sm"
              variant={d === days ? "secondary" : "ghost"}
              aria-pressed={d === days}
              onClick={() => onDays(d)}
            >
              {d}
            </Button>
          ))}
        </div>
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="tuning-hours">Business hours</Label>
        <Select value={hours} onValueChange={onHours}>
          <SelectTrigger id="tuning-hours" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {hourOptions.map((h) => (
              <SelectItem key={h} value={h}>
                {h.replace("-", ":00–")}:00
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  )
}

function Section({ title, note, children }: { title: string; note?: ReactNode; children: ReactNode }) {
  return (
    <section className="grid gap-3 min-w-0" aria-label={title}>
      <div className="grid gap-0.5">
        <h2 className="font-medium">{title}</h2>
        {note && <p className="text-sm text-muted-foreground break-words">{note}</p>}
      </div>
      {children}
    </section>
  )
}

function Body({
  report: r,
  serverId,
  loading,
  onAction,
  onRecordChange,
}: {
  report: TuningReport
  serverId: number
  loading: boolean
  onAction: (a: QueryAction) => void
  onRecordChange: () => void
}) {
  // The day of the change, or the next working day for a change at a weekend (as domain tSameHour).
  let sameDay = "The day"
  if (r.centre.at) {
    const d = serverDate(r.centre.at)
    while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() + 1)
    sameDay = serverDay(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}T00:00:00`)
  }
  return (
    <div className={cn("grid gap-6", loading && "opacity-60 transition-opacity")}>
      <Section title="What it means">
        <ExplainList report={r} serverId={serverId} onAction={onAction} onRecordChange={onRecordChange} />
      </Section>

      <FocusSection report={r} serverId={serverId} />

      <Section
        title="Is it better than before?"
        note={
          r.now.hours > 0 ? (
            <>
              Now: {r.now.label}. Before: {r.before.label}. Every figure is a total divided by a total, never an average of hours.
            </>
          ) : (
            "Nothing to compare yet."
          )
        }
      >
        <Tiles tiles={r.tiles} />
      </Section>

      <Section
        title="Hour by hour"
        note={`The last ${r.days} days. An hour that holds a change, and the hour in progress, are never compared.`}
      >
        <HourChart hours={r.hours} changes={r.changes} />
      </Section>

      {r.sameHour.length > 0 && (
        <Section title="Same hour, before and after" note="CPU per execution in each business hour, against the same hour on the five working days before.">
          <SameHourTable rows={r.sameHour} day={sameDay} />
        </Section>
      )}

      {r.queries.length > 0 && (
        <Section
          title="What is expensive"
          note="The costliest query shapes in the window, CPU per run before and now. Under 20 runs in either window gets no verdict."
        >
          <QueriesTable report={r} serverId={serverId} />
        </Section>
      )}

      <Section title="Did a change break anything?">
        <BreaksList report={r} serverId={serverId} onAction={onAction} />
      </Section>

      {r.notes.length > 0 && (
        <Alert>
          <Info />
          <AlertTitle>Not everything could be read</AlertTitle>
          <AlertDescription>
            <ul className="list-disc pl-5 grid gap-1">
              {r.notes.map((n) => (
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
      <Skeleton className="h-20 w-full rounded-xl" />
      <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-24 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-44 w-full rounded-xl" />
    </div>
  )
}
