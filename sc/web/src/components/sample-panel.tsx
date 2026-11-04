import { useEffect, useRef, useState, type ReactNode } from "react"
import { Activity, Loader2, Search, XCircle } from "lucide-react"
import { Link } from "react-router-dom"

import {
  sampleApi,
  sampleDefaultSeconds,
  sampleMaxSeconds,
  sampleMinSeconds,
  type PerfSample,
  type SampleLine,
} from "@/api-sample"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen, plural } from "@/lib/format"
import { blockingPath, queryPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

const one = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 })
const whole = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 })
const secondsOf = (ms: number) => one.format(ms / 1000)

// The live sample on the Performance card page (docs/phase4c-contract.md, part 3; DESIGN 5.3): the last
// stored sample, or the note that none was run, and "Watch for N seconds", which takes two readings N
// seconds apart on the server and shows the difference. The request returns when the sample is done; the
// panel counts down meanwhile. Read on request only; it changes nothing on the server.
export function SamplePanel({ serverId, database }: { serverId: number; database: string }) {
  const [sample, setSample] = useState<PerfSample | null | undefined>(undefined)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [seconds, setSeconds] = useState(String(sampleDefaultSeconds))
  const [running, setRunning] = useState<{ until: number } | null>(null)
  const [left, setLeft] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    let cancelled = false
    sampleApi
      .lastSample(serverId, database)
      .then((s) => !cancelled && setSample(s))
      .catch((e: Error) => {
        if (cancelled) return
        setSample(null)
        setLoadError(e.message)
      })
    return () => {
      cancelled = true
    }
  }, [serverId, database])

  useEffect(
    () => () => {
      if (timer.current) clearInterval(timer.current)
    },
    [],
  )

  const n = Number(seconds)
  const valid = Number.isInteger(n) && n >= sampleMinSeconds && n <= sampleMaxSeconds

  async function run() {
    if (!valid) return
    setError(null)
    const until = Date.now() + n * 1000
    setRunning({ until })
    setLeft(n)
    timer.current = setInterval(() => setLeft(Math.max(0, Math.ceil((until - Date.now()) / 1000))), 250)
    try {
      setSample(await sampleApi.runSample(serverId, database, n))
    } catch (e) {
      setError((e as Error).message)
    } finally {
      if (timer.current) clearInterval(timer.current)
      timer.current = null
      setRunning(null)
    }
  }

  return (
    <section className="grid gap-3 min-w-0" aria-label="Live sample" aria-busy={!!running}>
      <div className="grid gap-0.5">
        <h2 className="font-medium">Live sample</h2>
        <p className="text-sm text-muted-foreground">
          Why is it slow right now? Takes two readings of the server some seconds apart and shows what happened in
          between: blocking, waits, CPU, the queries using it, file speed and memory. Changes nothing.
        </p>
      </div>

      {sample ? (
        <SampleBody
          sample={sample}
          serverId={serverId}
          database={database}
          className={cn("transition-opacity", running && "opacity-50")}
        />
      ) : sample === null ? (
        <p className="rounded-xl border border-dashed p-4 text-muted-foreground">
          {loadError ? `Could not read the last sample: ${loadError}` : "Not run yet. Watch the server while users report slowness."}
        </p>
      ) : (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Reading the last sample…
        </div>
      )}

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not take the sample</AlertTitle>
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-end gap-x-3 gap-y-2">
        <div className="grid gap-1.5">
          <Label htmlFor="sample-seconds">Seconds</Label>
          <Input
            id="sample-seconds"
            type="number"
            inputMode="numeric"
            min={sampleMinSeconds}
            max={sampleMaxSeconds}
            className="w-24"
            value={seconds}
            disabled={!!running}
            aria-invalid={!valid}
            onChange={(e) => setSeconds(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") run()
            }}
          />
        </div>
        <Button variant="outline" onClick={run} disabled={!!running || !valid}>
          {running ? <Loader2 className="animate-spin" /> : <Activity />}
          Watch for {valid ? n : "…"} seconds
        </Button>
        {running && (
          <span className="text-sm text-muted-foreground" role="status">
            {left > 0 ? `Watching… ${plural(left, "second", "seconds")} left` : "Reading the second snapshot…"}
          </span>
        )}
        {!valid && !running && (
          <span className="text-sm text-destructive">
            {sampleMinSeconds} to {sampleMaxSeconds} seconds.
          </span>
        )}
      </div>
    </section>
  )
}

function SampleBody({
  sample: s,
  serverId,
  database,
  className,
}: {
  sample: PerfSample
  serverId: number
  database: string
  className?: string
}) {
  const linkFor = (line: SampleLine): { to: string; label: string } | undefined => {
    if (line.area === "blocking") return { to: blockingPath(serverId, database), label: "Investigate" }
    if (line.area === "queries" && line.queryHash) {
      const q = s.queries.find((x) => x.queryHash === line.queryHash)
      if (!q || !q.database || q.database === database) return { to: queryPath(serverId, database, line.queryHash), label: "Investigate" }
    }
    return undefined
  }
  const errors = [
    ["Waits", s.waitsError],
    ["CPU", s.cpuError],
    ["Queries", s.queriesError],
    ["Files", s.filesError],
    ["Blocking", s.blockingError],
    ["Operations running", s.runningError],
    ["Memory", s.memoryError],
  ].filter((e): e is [string, string] => !!e[1])

  return (
    <div className={cn("grid gap-4 min-w-0", className)}>
      <p className="text-xs text-muted-foreground">
        as of {formatWhen(s.capturedAt)} · {one.format(s.elapsedSeconds || s.seconds)} seconds
        {s.serverName && <> on {s.serverName}</>}
      </p>

      {s.lines.length === 0 ? (
        <p className="rounded-xl border p-4 break-words">{s.verdict}</p>
      ) : (
        <ul className="grid gap-2">
          {s.lines.map((line, i) => {
            const link = linkFor(line)
            return (
              <li key={`${line.area}/${i}`} className={cn("rounded-xl border p-3 grid gap-2", i === 0 && "border-amber-500/50")}>
                <p className={cn("break-words", i === 0 && "font-medium")}>{line.text}</p>
                {link && (
                  <div>
                    <Button asChild variant="outline" size="sm">
                      <Link to={link.to}>
                        <Search />
                        {link.label}
                      </Link>
                    </Button>
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}

      {s.blocking.headBlockers.length > 0 && (
        <Part title="Head blockers">
          <Table className="text-xs sm:text-sm">
            <TableHeader>
              <TableRow>
                <TableHead>Session</TableHead>
                <TableHead className="text-right">Blocked</TableHead>
                <TableHead>Longest</TableHead>
                <TableHead>Who</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.blocking.headBlockers.map((b) => (
                <TableRow key={b.session} className="hover:bg-transparent">
                  <TableCell className="align-top tabular-nums">
                    <Link className="underline underline-offset-2" to={blockingPath(serverId, database)}>
                      {b.session}
                    </Link>
                  </TableCell>
                  <TableCell className="align-top text-right tabular-nums">{b.blocked}</TableCell>
                  <TableCell className="align-top whitespace-normal">{plural(b.longestSec, "second", "seconds")}</TableCell>
                  <TableCell className="align-top whitespace-normal break-words">
                    {[b.program || b.login, b.host].filter(Boolean).join(" on ")}
                    {b.status === "sleeping" && <span className="text-muted-foreground"> (sleeping)</span>}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Part>
      )}

      {s.queries.length > 0 && (
        <Part title="Queries using the most CPU in the sample">
          <Table className="text-xs sm:text-sm table-fixed">
            <TableHeader>
              <TableRow>
                <TableHead className="w-[55%]">Query</TableHead>
                <TableHead className="text-right">CPU s</TableHead>
                <TableHead className="text-right">Runs</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Reads</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.queries.map((q) => {
                const here = !q.database || q.database === database
                const label = q.objectName || q.text
                return (
                  <TableRow key={q.queryHash} className="hover:bg-transparent">
                    <TableCell className="align-top whitespace-normal">
                      <div className="grid gap-0.5 min-w-0">
                        {here ? (
                          <Link
                            className="font-mono text-xs break-all line-clamp-3 underline underline-offset-2"
                            to={queryPath(serverId, database, q.queryHash)}
                            title={q.text}
                          >
                            {label}
                          </Link>
                        ) : (
                          <span className="font-mono text-xs break-all line-clamp-3" title={q.text}>
                            {label}
                          </span>
                        )}
                        {q.database && q.database !== database && (
                          <span className="text-xs text-muted-foreground">in {q.database}</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="align-top text-right tabular-nums">{secondsOf(q.cpuMs)}</TableCell>
                    <TableCell className="align-top text-right tabular-nums">{whole.format(q.runs)}</TableCell>
                    <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{whole.format(q.reads)}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </Part>
      )}

      {s.waits.length > 0 && (
        <Part title="Waits in the sample">
          <Table className="text-xs sm:text-sm">
            <TableHeader>
              <TableRow>
                <TableHead>Wait</TableHead>
                <TableHead className="text-right">Seconds</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Waits</TableHead>
                <TableHead className="text-right">ms each</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.waits.map((w) => (
                <TableRow key={w.type} className="hover:bg-transparent">
                  <TableCell className="align-top whitespace-normal">
                    <div className="grid gap-0.5 min-w-0">
                      <span className="font-mono text-xs break-all">{w.type}</span>
                      <span className="text-xs text-muted-foreground">
                        {w.words}
                        {w.poison && <span className="text-destructive"> · poison wait</span>}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="align-top text-right tabular-nums">{secondsOf(w.waitMs)}</TableCell>
                  <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{whole.format(w.tasks)}</TableCell>
                  <TableCell className="align-top text-right tabular-nums">{one.format(w.avgMs)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Part>
      )}

      {s.cpu && s.cpu.records > 0 && (
        <Part title="CPU" padded>
          <p className="text-sm break-words">
            SQL Server {s.cpu.sql}%, other programs {s.cpu.other}%, idle {s.cpu.idle}%
            {!s.cpu.inWindow && " (the minute before the sample: no reading fell inside it)"}. Queries waiting for a
            CPU: {s.cpu.runnableStart} at the start, {s.cpu.runnableEnd} at the end, on{" "}
            {plural(s.cpu.schedulers, "scheduler", "schedulers")}
            {s.cpu.hasSignalShare && <>; {one.format(s.cpu.signalWaitPct)}% of the waiting was for a CPU</>}.
          </p>
        </Part>
      )}

      {s.files.length > 0 && (
        <Part title="Files">
          <Table className="text-xs sm:text-sm">
            <TableHeader>
              <TableRow>
                <TableHead>File</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Reads</TableHead>
                <TableHead className="text-right">Read ms</TableHead>
                <TableHead className="hidden sm:table-cell text-right">Writes</TableHead>
                <TableHead className="text-right">Write ms</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {s.files.map((f) => (
                <TableRow key={`${f.database}/${f.file}`} className="hover:bg-transparent">
                  <TableCell className="align-top whitespace-normal">
                    <div className="grid gap-0.5 min-w-0">
                      <span className="break-all">{f.file}</span>
                      <span className="text-xs text-muted-foreground">
                        {f.database} · {f.type === "LOG" ? "log" : "data"}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{whole.format(f.reads)}</TableCell>
                  <TableCell className="align-top text-right tabular-nums">{f.reads > 0 ? one.format(f.msPerRead) : "–"}</TableCell>
                  <TableCell className="hidden sm:table-cell align-top text-right tabular-nums">{whole.format(f.writes)}</TableCell>
                  <TableCell className="align-top text-right tabular-nums">{f.writes > 0 ? one.format(f.msPerWrite) : "–"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Part>
      )}

      {(s.running.length > 0 || s.growth.length > 0 || s.memory) && (
        <Part title="Also seen" padded>
          <ul className="grid gap-1 text-sm">
            {s.running.map((r) => (
              <li key={`${r.session}/${r.command}`} className="break-words">
                {r.command} on {r.database || "the server"} (session {r.session})
                {r.percentDone > 0 && <>: {whole.format(r.percentDone)}% done</>}
              </li>
            ))}
            {s.growth.map((g) => (
              <li key={`${g.database}/${g.file}`} className="break-words">
                {g.file} ({g.database}): {whole.format(g.beforeMb)} MB → {whole.format(g.afterMb)} MB
              </li>
            ))}
            {s.memory && s.memory.pleEnd >= 0 && (
              <li>
                Page life expectancy {whole.format(s.memory.pleStart)} → {whole.format(s.memory.pleEnd)} seconds; memory
                grants pending {Math.max(s.memory.pendingStart, s.memory.pendingEnd)}
              </li>
            )}
          </ul>
        </Part>
      )}

      {errors.length > 0 && (
        <ul className="grid gap-1 text-xs text-muted-foreground">
          {errors.map(([area, msg]) => (
            <li key={area} className="break-words">
              {area} could not be read: {msg}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function Part({ title, padded, children }: { title: string; padded?: boolean; children: ReactNode }) {
  return (
    <div className="grid gap-2 min-w-0">
      <h3 className="text-sm font-medium">{title}</h3>
      <div className={cn("rounded-xl border min-w-0", padded && "p-3")}>{children}</div>
    </div>
  )
}
