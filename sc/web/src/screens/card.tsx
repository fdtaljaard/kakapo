import { useState, type ReactNode } from "react"
import { Link, useParams } from "react-router-dom"
import { ChartColumn, Check, Copy, Loader2, RotateCw, XCircle } from "lucide-react"

import type { CardDetail, Card as CardData, CheckdbRoom, DeadlockReport, Finding, Fix, Overview, Owner } from "@/api"
import { archiveTableFixId } from "@/api-tables"
import { BlockingHistoryPanel } from "@/components/blocking-history-panel"
import { CheckdbRoomPanel } from "@/components/checkdb-room-panel"
import { CheckdbRunPanel } from "@/components/checkdb-run-panel"
import { ChecksList } from "@/components/checks-list"
import { DatabaseLine } from "@/components/database-list"
import { DeadlocksPanel } from "@/components/deadlocks-panel"
import { PerformanceTrendBlock } from "@/components/performance-trend"
import { SamplePanel } from "@/components/sample-panel"
import { FindingCard } from "@/components/finding-card"
import { FixDialog } from "@/components/fix-dialog"
import { MaintenancePlanRow } from "@/components/maintenance-plan-row"
import { StillRunningNotice } from "@/components/fix-progress"
import { CardStatusLine } from "@/components/instance-card"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { CopyFallback } from "@/components/statement-block"
import { TablesRow } from "@/components/tables-row"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { useCardDetail } from "@/hooks/use-card-detail"
import { useOverview } from "@/hooks/use-overview"
import { useServers } from "@/hooks/use-servers"
import { useBuildVersion } from "@/lib/build-version"
import { FindingContext } from "@/lib/finding-context"
import { formatUptime, formatWhen } from "@/lib/format"
import { findingsFor, handoverAll, type HandoverContext } from "@/lib/handover"
import { useCopy } from "@/lib/use-copy"
import { blockingPath, databasePagePath, instancePath, queryPath, tuningPath } from "@/lib/paths"

// "/instances/:id/databases/:db/cards/:card": the frame of one card (title, question, status, verdict)
// and what sits behind it, for one database (docs/phase3a-contract.md). The card detail is read on load
// and on Refresh; the frame shows the cached overview's card until it arrives.
export function CardScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return (
    <CardPage key={`${id}/${database}/${params.card}`} serverId={id} database={database} cardId={params.card ?? ""} />
  )
}

function CardPage({ serverId, database, cardId }: { serverId: number; database: string; cardId: string }) {
  const { load } = useServers()
  const setup = cardId === "setup"
  // Setup needs the overview for its Query Store row (read on a direct visit). The other cards only
  // borrow the cached one for the frame while their detail loads, so their checks are not read twice.
  const ov = useOverview(serverId, database, setup ? { reuse: true } : { cacheOnly: true })
  const det = useCardDetail(serverId, database, cardId)
  // A Room for CHECKDB run shows at once, until a detail read carries that result (or a newer one) itself.
  const [ran, setRan] = useState<CheckdbRoom | null>(null)
  // The same for a deadlock report read on the Performance card.
  const [readDeadlocks, setReadDeadlocks] = useState<DeadlockReport | null>(null)
  let detail = det.detail && ran && !hasRoomSince(det.detail, ran) ? withCheckdbRoom(det.detail, ran) : det.detail
  if (detail && readDeadlocks && !hasDeadlocksSince(detail, readDeadlocks)) detail = withDeadlocks(detail, readDeadlocks)
  // The fix being previewed (a finding's Apply or the Query Store row), and the overview that was on
  // screen when one was applied.
  const [fixing, setFixing] = useState<{ fixId: string; title: string; options?: Record<string, string> } | null>(null)
  const [appliedOn, setAppliedOn] = useState<Overview | null | undefined>(undefined)
  // Phase 4f: the dialog was closed while its long fix still runs in the background.
  const [stillRunning, setStillRunning] = useState<string | null>(null)
  const version = useBuildVersion()

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />
  if (det.notFound) return <NotFound what="There is no such card." />
  const card = det.detail?.card ?? ov.overview?.cards.find((c) => c.id === cardId)

  // Where a Copy block says the finding was read (docs/phase4a-contract.md): @@SERVERNAME from the overview
  // when there is one, else the registered host; a panel's finding (STO10, DLK01) has its own as-of.
  const handover: HandoverContext = {
    server: ov.overview?.instance.serverName || server?.host || "",
    database,
    asOf: card?.asOf ? formatWhen(card.asOf) : "",
    version,
  }
  const roomContext = detail?.checkdbRoom ? { ...handover, asOf: formatWhen(detail.checkdbRoom.capturedAt) } : handover
  const deadlockContext = detail?.deadlocks ? { ...handover, asOf: formatWhen(detail.deadlocks.capturedAt) } : handover
  const copyAll =
    detail && card
      ? copyAllBlocks(card, detail, (f) => contextFor(f, detail, handover, roomContext, deadlockContext))
      : []

  const loading = det.loading || (setup && ov.loading)
  const error = det.error ?? (setup ? ov.error : null)
  const refresh = () => {
    det.refresh()
    if (setup) ov.refresh()
  }

  // After an apply the card is read again, so the finding goes (or changes) and the status line
  // follows; the dialog stays open on its result until closed. The Query Store row waits for the
  // overview read after the apply, so it never shows the old state under a fresh detail.
  const applied = () => {
    if (setup) setAppliedOn(ov.overview)
    refresh()
  }
  const applyFinding = (f: Finding) => {
    if (!f.fixId) return
    // The Backups card's fix is titled by itself ("Create a backup schedule"), and so is a costly query's
    // archive ("Archive or delete old rows"); the others by the finding.
    const ownTitle = cardId === "backups" || (f.checkId === "QRY01" && f.fixId === archiveTableFixId)
    const fix = ownTitle ? detail?.fixes?.find((x) => x.id === f.fixId) : undefined
    setFixing({ fixId: f.fixId, title: fix?.title ?? f.title, options: fixOptionsFor(f, detail?.fixes) })
  }
  // Where a finding's next read lives (docs/phase4c-contract.md): a costly query opens its page, a
  // blocking or open-transaction finding opens the chain.
  const investigateFor = (f: Finding): { to: string } | undefined => {
    if (f.checkId === "QRY01") {
      const hash = /Query hash: (0x[0-9A-Fa-f]+)/.exec(f.evidence ?? "")?.[1]
      return hash ? { to: queryPath(serverId, database, hash) } : undefined
    }
    if (f.checkId === "ACT01" || f.checkId === "ACT02") return { to: blockingPath(serverId, database) }
    return undefined
  }

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? ov.overview?.instance.name ?? null, to: instancePath(serverId) },
            { label: database, to: databasePagePath(serverId, database) },
            { label: card?.title ?? null },
          ]}
        />
        {card ? (
          <CardFrame
            card={card}
            checking={loading}
            onRefresh={refresh}
            copyAll={copyAll}
            tuningTo={cardId === "performance" ? tuningPath(serverId, database) : undefined}
          />
        ) : (
          loading && <FrameSkeleton />
        )}
      </div>

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the card</AlertTitle>
          <AlertDescription className="break-words">
            <p>{error}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={refresh} disabled={loading}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {stillRunning && <StillRunningNotice notice={stillRunning} onDismiss={() => setStillRunning(null)} />}

      {/* Phase 5k: better or worse than usual, and what made the difference (docs/phase5k-contract.md). */}
      {cardId === "performance" && detail?.trend && <PerformanceTrendBlock trend={detail.trend} serverId={serverId} database={database} />}

      <FindingContext.Provider value={handover}>
        {detail ? (
          <DetailBody
            detail={detail}
            onApply={applyFinding}
            investigate={investigateFor}
            startedAt={ov.overview?.instance.startedAt}
            between={
              setup ? (
                // The Query Store row only when DBC10 did not fire: otherwise its finding and Apply cover it.
                !detail.findings.some((f) => f.checkId === "DBC10") && (
                  <QueryStoreRow
                    overview={appliedOn === ov.overview ? null : ov.overview}
                    onFix={() => setFixing({ fixId: "query-store", title: "Apply recommended Query Store settings" })}
                  />
                )
              ) : cardId === "backups" ? (
                <>
                  <BackupScheduleRow
                    database={database}
                    create={detail.fixes?.find((f) => f.id === "backup-schedule")}
                    remove={detail.fixes?.find((f) => f.id === "remove-backup-schedule")}
                    onFix={(fix) => setFixing({ fixId: fix.id, title: fix.title })}
                  />
                  {/* The last CHECKDB run from kakapo, and Run CHECKDB now (docs/phase4d-contract.md). */}
                  <CheckdbRunPanel
                    run={detail.checkdbRun}
                    onRun={() => setFixing({ fixId: "checkdb-now", title: "Run DBCC CHECKDB now" })}
                  />
                </>
              ) : cardId === "storage" ? (
                <>
                  <TempdbRow
                    fix={detail.fixes?.find((f) => f.id === "tempdb-files")}
                    onFix={(fix) => setFixing({ fixId: fix.id, title: fix.title })}
                  />
                  {/* Phase 5a: the tables kakapo archives, and "Archive a table…" (docs/phase5a-contract.md). */}
                  <TablesRow
                    serverId={serverId}
                    database={database}
                    fixes={detail.fixes}
                    onArchive={(table) =>
                      setFixing({ fixId: archiveTableFixId, title: "Archive or delete old rows", options: { table } })
                    }
                    onRemove={(fix) =>
                      setFixing({ fixId: fix.id, title: fix.title, options: { table: fix.options?.table ?? "" } })
                    }
                  />
                  <FindingContext.Provider value={roomContext}>
                    <CheckdbRoomPanel serverId={serverId} database={database} room={detail.checkdbRoom} onResult={setRan} />
                  </FindingContext.Provider>
                </>
              ) : cardId === "upkeep" ? (
                // Phase 5b: the maintenance plan row (docs/phase5b-contract.md).
                <MaintenancePlanRow
                  plan={detail.maintenancePlan}
                  fixes={detail.fixes}
                  context={handover}
                  onCreate={(piece) =>
                    setFixing({ fixId: piece.fixId ?? "", title: `Create: ${piece.title}`, options: piece.options })
                  }
                  onSetUp={(fix) => setFixing({ fixId: fix.id, title: fix.title })}
                />
              ) : (
                cardId === "performance" && (
                  <FindingContext.Provider value={deadlockContext}>
                    <SamplePanel serverId={serverId} database={database} />
                    <DeadlocksPanel
                      serverId={serverId}
                      database={database}
                      report={detail.deadlocks}
                      onResult={setReadDeadlocks}
                    />
                    {/* Phase 5d: a week of blocking from system_health (docs/phase5d-contract.md). */}
                    <BlockingHistoryPanel serverId={serverId} database={database} report={detail.blockingHistory} />
                  </FindingContext.Provider>
                )
              )
            }
          />
        ) : (
          det.loading && <BodySkeleton />
        )}
      </FindingContext.Provider>

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
          onApplied={applied}
        />
      )}
    </PageShell>
  )
}

// "Copy all for IT" and "Copy all for the vendor" (docs/phase4a-contract.md): one block per owner with
// actionable findings on the page, the panels' findings included; none when the owner has none.
interface CopyAll {
  label: string
  text: string
}

const copyAllOwners: { owner: Owner; label: string }[] = [
  { owner: "Client IT", label: "Copy all for IT" },
  { owner: "Vendor", label: "Copy all for the vendor" },
]

// Every finding the page shows: the list (without the one a panel draws) and the panels' findings.
function pageFindings(detail: CardDetail): Finding[] {
  const inPanel = detail.card.id === "storage" ? "STO10" : detail.card.id === "performance" ? "DLK01" : null
  return [
    ...(inPanel ? detail.findings.filter((f) => f.checkId !== inPanel) : detail.findings),
    ...(detail.checkdbRoom ? [detail.checkdbRoom.finding] : []),
    ...(detail.deadlocks?.findings ?? []),
    ...(detail.blockingHistory?.findings ?? []),
  ]
}

function copyAllBlocks(card: CardData, detail: CardDetail, ctxFor: (f: Finding) => HandoverContext): CopyAll[] {
  const findings = pageFindings(detail)
  return copyAllOwners.flatMap(({ owner, label }) => {
    const mine = findingsFor(findings, owner)
    return mine.length > 0 ? [{ label, text: handoverAll(card, mine, ctxFor) }] : []
  })
}

function contextFor(
  f: Finding,
  detail: CardDetail,
  card: HandoverContext,
  room: HandoverContext,
  deadlocks: HandoverContext,
): HandoverContext {
  if (detail.checkdbRoom && f === detail.checkdbRoom.finding) return room
  if (detail.deadlocks?.findings.includes(f)) return deadlocks
  return card
}

function CardFrame({
  card,
  checking,
  onRefresh,
  copyAll = [],
  tuningTo,
}: {
  card: CardData
  checking: boolean
  onRefresh: () => void
  copyAll?: CopyAll[]
  // Phase 5c: the Performance card's "Tuning report" (docs/phase5c-contract.md).
  tuningTo?: string
}) {
  const { status, fallback, copy, dismiss } = useCopy()
  const [copiedLabel, setCopiedLabel] = useState("")
  return (
    <header className="grid gap-2">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid gap-0.5 min-w-0">
          <h1 className="text-xl font-semibold">{card.title}</h1>
          <p className="text-muted-foreground">{card.question}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {copyAll.map(({ label, text }) => {
            const copied = status === "copied" && copiedLabel === label
            return (
              <Button
                key={label}
                variant="outline"
                onClick={() => {
                  setCopiedLabel(label)
                  copy(text)
                }}
              >
                {copied ? <Check /> : <Copy />}
                {copied ? "Copied" : label}
              </Button>
            )
          })}
          {tuningTo && (
            <Button variant="outline" asChild>
              <Link to={tuningTo}>
                <ChartColumn />
                Tuning report
              </Link>
            </Button>
          )}
          <Button variant="outline" onClick={onRefresh} disabled={checking}>
            {checking ? <Loader2 className="animate-spin" /> : <RotateCw />}
            Refresh
          </Button>
        </div>
      </div>
      {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <CardStatusLine status={card.status} text={card.statusText} checking={checking} className="text-sm" />
        {card.asOf && <span className="text-xs text-muted-foreground">as of {formatWhen(card.asOf)}</span>}
      </div>
      {card.verdict && <p>{card.verdict}</p>}
      {/* A grey card's note says what it will show or why it could not be read; the body says that. */}
      {card.note && card.status !== "grey" && (
        <p className="text-sm text-muted-foreground">{card.note}</p>
      )}
    </header>
  )
}

function FrameSkeleton() {
  return (
    <div className="grid gap-2" aria-busy>
      <Skeleton className="h-7 w-72 max-w-full" />
      <Skeleton className="h-5 w-56 max-w-full" />
      <Skeleton className="h-4 w-32" />
    </div>
  )
}

function BodySkeleton() {
  return (
    <div className="grid gap-3" aria-busy>
      <Skeleton className="h-5 w-24" />
      <Skeleton className="h-48 w-full rounded-xl" />
      <Skeleton className="h-32 w-full rounded-xl" />
    </div>
  )
}

// The Storage card after a Room for CHECKDB run: the result, and STO10 in the checks list following
// its verdict (Info passed, anything else a finding).
function withCheckdbRoom(detail: CardDetail, room: CheckdbRoom): CardDetail {
  const status = room.finding.severity === "Info" ? "passed" : "finding"
  return {
    ...detail,
    checkdbRoom: room,
    checks: detail.checks.map((c) => (c.checkId === "STO10" ? { ...c, status, note: "" } : c)),
  }
}

function hasRoomSince(detail: CardDetail, room: CheckdbRoom): boolean {
  const stored = detail.checkdbRoom?.capturedAt
  return !!stored && new Date(stored).getTime() >= new Date(room.capturedAt).getTime()
}

// The Performance card after a deadlock report run: the report, and DLK01 in the checks list following
// it (no findings passed, any finding a finding).
function withDeadlocks(detail: CardDetail, report: DeadlockReport): CardDetail {
  const status = report.findings.length === 0 ? "passed" : "finding"
  return {
    ...detail,
    deadlocks: report,
    checks: detail.checks.map((c) => (c.checkId === "DLK01" ? { ...c, status, note: "" } : c)),
  }
}

function hasDeadlocksSince(detail: CardDetail, report: DeadlockReport): boolean {
  const stored = detail.deadlocks?.capturedAt
  return !!stored && new Date(stored).getTime() >= new Date(report.capturedAt).getTime()
}

// The Performance card's two clocks, kept apart (docs/phase3e-contract.md): what is happening now, the
// counters since the restart, and the 7-day history (Query Store, deadlocks). A finding without a clock
// counts since the restart.
const clockHeadings: { clock: NonNullable<Finding["clock"]>; title: string }[] = [
  { clock: "now", title: "Right now" },
  { clock: "since-restart", title: "Since restart" },
  { clock: "window", title: "Last 7 days" },
]

// A card with findings: the findings as ranked (the actionable ones, then the Info notes, quieter),
// then what was checked. A grey card: its note, and the checks list if any were skipped with a reason.
// `between` sits between the two: the Storage card's Room for CHECKDB panel (STO10's finding shows
// there only), the Performance card's Deadlocks panel (DLK01's findings show there only) or the Setup
// card's Query Store row. A finding with a fixId gets Apply (onApply). The Performance card groups its
// findings by clock; startedAt (from the overview, when there is one) says how long "since restart" is.
function DetailBody({
  detail,
  between,
  onApply,
  investigate,
  startedAt,
}: {
  detail: CardDetail
  between?: ReactNode
  onApply?: (f: Finding) => void
  investigate?: (f: Finding) => { to: string } | undefined
  startedAt?: string
}) {
  const { card, checks } = detail
  const inPanel = card.id === "storage" ? "STO10" : card.id === "performance" ? "DLK01" : null
  const findings = inPanel ? detail.findings.filter((f) => f.checkId !== inPanel) : detail.findings
  // The checks list marks STO10 and DLK01 amber only when it sees an actionable finding for them.
  const checkFindings = [
    ...findings,
    ...(detail.checkdbRoom ? [detail.checkdbRoom.finding] : []),
    ...(detail.deadlocks?.findings ?? []),
    ...(detail.blockingHistory?.findings ?? []),
  ]
  if (card.status === "grey") {
    const skipped = checks.some((c) => c.status === "skipped" && c.note)
    return (
      <>
        <NotCheckedBody card={card} />
        {between}
        {skipped && <ChecksList checks={checks} findings={checkFindings} />}
      </>
    )
  }

  if (card.id === "performance") {
    return (
      <>
        <ClockSections findings={findings} startedAt={startedAt} onApply={onApply} investigate={investigate} />
        {between}
        {checks.length > 0 && <ChecksList checks={checks} findings={checkFindings} />}
      </>
    )
  }

  const toDo = findings.filter((f) => f.severity !== "Info")
  const info = findings.filter((f) => f.severity === "Info")
  return (
    <>
      <section className="grid gap-3" aria-label="Findings">
        <h2 className="font-medium">Findings</h2>
        {toDo.length === 0 && (
          <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
            No findings. Nothing on this card needs doing.
          </p>
        )}
        {toDo.map((f) => (
          <FindingCard key={`${f.checkId}/${f.object}`} finding={f} onApply={onApply} investigate={investigate?.(f)} />
        ))}
        {info.length > 0 && (
          <>
            <h3 className="pt-1 text-sm text-muted-foreground">For information</h3>
            {info.map((f) => (
              <FindingCard key={`${f.checkId}/${f.object}`} finding={f} onApply={onApply} investigate={investigate?.(f)} />
            ))}
          </>
        )}
      </section>
      {between}
      {checks.length > 0 && <ChecksList checks={checks} findings={checkFindings} />}
    </>
  )
}

// The Performance card's findings under "Right now", "Since restart (5 days)" and "Last 7 days", each in
// the ranked order; a heading without findings is left out.
function ClockSections({
  findings,
  startedAt,
  onApply,
  investigate,
}: {
  findings: Finding[]
  startedAt?: string
  onApply?: (f: Finding) => void
  investigate?: (f: Finding) => { to: string } | undefined
}) {
  if (findings.length === 0) {
    return (
      <section className="grid gap-3" aria-label="Findings">
        <h2 className="font-medium">Findings</h2>
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          No findings. Nothing on this card needs doing.
        </p>
      </section>
    )
  }
  return clockHeadings.map(({ clock, title }) => {
    const group = findings.filter((f) => (f.clock ?? "since-restart") === clock)
    if (group.length === 0) return null
    const heading = clock === "since-restart" && startedAt ? `${title} (${formatUptime(startedAt)})` : title
    return (
      <section key={clock} className="grid gap-3" aria-label={title}>
        <h2 className="font-medium">{heading}</h2>
        {group.map((f, i) => (
          <FindingCard key={`${f.checkId}/${f.object}/${i}`} finding={f} onApply={onApply} investigate={investigate?.(f)} />
        ))}
      </section>
    )
  })
}

// This database's Query Store line from the overview, with Apply (the generic fix dialog, "query-store").
// Without an overview (the first read, or the read after an apply) it says it is reading.
function QueryStoreRow({ overview, onFix }: { overview: Overview | null; onFix: () => void }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Query Store</CardTitle>
        <CardDescription>
          Query Store keeps the query history that tuning needs. Apply previews the exact statement first.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {overview ? (
          <ul>
            <DatabaseLine db={overview.database} onFix={onFix} />
          </ul>
        ) : (
          <div className="flex items-center gap-2 text-sm text-muted-foreground py-2">
            <Loader2 className="size-4 animate-spin" />
            Reading the database…
          </div>
        )}
      </CardContent>
    </Card>
  )
}

// The Storage card's standing row (docs/phase4e-contract.md): TempDB's files as they are and the week's
// growth history, whatever TMP02 says, with Set up the TempDB files, which previews equalising and
// pre-sizing the set. Nothing when the files could not be read.
function TempdbRow({ fix, onFix }: { fix?: Fix; onFix: (fix: Fix) => void }) {
  if (!fix) return null
  const summary = fix.options?.summary ?? fix.current
  return (
    <Card>
      <CardHeader>
        <CardTitle>TempDB</CardTitle>
        <CardDescription className="break-words">
          {summary} Set up previews making every file the same size and growth, pre-sized to what the
          workload has reached, before anything runs.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button variant="outline" size="sm" onClick={() => onFix(fix)}>
          Set up the TempDB files
        </Button>
      </CardContent>
    </Card>
  )
}

// The Backups card's standing row (docs/phase3g-contract.md): Create a backup schedule whenever the fix
// can be created on this server, whatever the findings say; while kakapo's jobs exist, their names with
// Remove, which previews deleting them. Nothing when neither applies (a job already covers the database,
// or the jobs could not be read).
function BackupScheduleRow({
  database,
  create,
  remove,
  onFix,
}: {
  database: string
  create?: Fix
  remove?: Fix
  onFix: (fix: Fix) => void
}) {
  if (!create && !remove) return null
  if (remove) {
    const names = remove.current.split(", ")
    return (
      <Card>
        <CardHeader>
          <CardTitle>Backup schedule</CardTitle>
          <CardDescription>
            kakapo created the backup {names.length === 1 ? "job" : "jobs"} {names.join(" and ")} on this server.
            Remove previews deleting them; the backup files stay.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button variant="outline" size="sm" onClick={() => onFix(remove)}>
            Remove
          </Button>
        </CardContent>
      </Card>
    )
  }
  const what = create!.current === "no log backup job" ? "No job takes log backups of" : "No SQL Server Agent job backs up"
  return (
    <Card>
      <CardHeader>
        <CardTitle>Backup schedule</CardTitle>
        <CardDescription>
          {what} {database} from this server. Create previews the jobs, the folder and the exact statements before
          anything runs.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Button size="sm" onClick={() => onFix(create!)}>
          Create a backup schedule
        </Button>
      </CardContent>
    </Card>
  )
}

// A grey card: what it will show (not built yet) or why it could not be read.
function NotCheckedBody({ card }: { card: CardData }) {
  if (!card.note) return null
  return (
    <Card>
      <CardContent className="grid gap-1">
        <p className="font-medium">
          {card.statusText === "Not checked yet" ? "What this card will show" : "Why it could not be read"}
        </p>
        <p className="text-muted-foreground break-words">{card.note}</p>
      </CardContent>
    </Card>
  )
}

// The options a finding's fix needs (docs/phase4c-contract.md): the detail's fixes entry with this id whose
// options name the finding's object (a table, an index suggestion, a flag), else the first with this id.
// A costly query's archive fix also names its query hash, which the finding's evidence holds.
function fixOptionsFor(f: Finding, fixes?: Fix[]): Record<string, string> | undefined {
  const mine = fixes?.filter((x) => x.id === f.fixId) ?? []
  const onObject = mine.filter((x) => Object.values(x.options ?? {}).includes(f.object))
  // Two suggestions on one table (WRK03): the evidence names the columns, so the entry whose column
  // options all appear in it is the finding's own. Two costly queries on one object (QRY01): the hash.
  const text = `${f.finding} ${f.evidence ?? ""}`
  const byColumns = onObject.find((x) =>
    ["equality", "inequality", "included", "queryHash"].every((k) => !x.options?.[k] || text.includes(x.options[k])),
  )
  const match = byColumns ?? onObject[0] ?? mine[0]
  return match?.options
}
