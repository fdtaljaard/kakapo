import { useEffect, useRef, useState, type ReactNode } from "react"
import { Check, ChevronRight, CircleCheck, Copy, Loader2, TriangleAlert, XCircle } from "lucide-react"

import { api, ApiError, type FixInput, type FixPreview, type FixResult, type FixRun } from "@/api"
import { FixProgress, FixTook } from "@/components/fix-progress"
import { RoomLines } from "@/components/room-line"
import { CopyFallback, StatementBlock } from "@/components/statement-block"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { scriptText } from "@/lib/handover"
import { stillRunningNotice } from "@/lib/long-fix"
import { useCopy, type CopyStatus } from "@/lib/use-copy"

type Step =
  | { kind: "loading" }
  | { kind: "failed"; message: string }
  // refreshing: the preview is being rebuilt with changed inputs; previewError: that rebuild failed.
  // applyStartedAt: when Apply was pressed, for a long fix's progress (phase 4e).
  | {
      kind: "preview"
      preview: FixPreview
      applying: boolean
      refreshing?: boolean
      previewError?: string
      error?: string
      applyStartedAt?: number
    }
  // tookSeconds: how long the apply took, shown for a long fix (phase 4e); options: what was applied.
  | { kind: "done"; result: FixResult; tookSeconds?: number; options?: Options }

type Options = Record<string, string>
type PreviewFn = (serverId: number, database: string, fixId: string, options?: Options) => Promise<FixPreview>
type ApplyFn = (serverId: number, database: string, fixId: string, options?: Options) => Promise<FixResult>
type RunFn = (serverId: number, database: string, runId: string) => Promise<FixRun>

// Phase 4f: a long fix whose apply answered with a pending run: the run to poll, the preview it runs and
// when Apply was pressed.
type PendingRun = { runId: string; preview: FixPreview; startedAt: number }

// Preview, confirm and apply one fix (docs/phase3c-contract.md): a server setting, a database option,
// Query Store included ("query-store"), or the backup schedule (phase 3g), whose preview carries inputs.
// Mount it to open it; it loads the preview at once. Changing an input and leaving the field (or pressing
// Enter) rebuilds the preview with the current options; Apply sends the options the preview was built
// with. Cancel calls nothing; Apply runs the previewed statement and reports what was read back. An
// Express preview cannot be applied: its statement is a script, and Copy script replaces Apply. While the
// statement runs the dialog cannot be closed. A long fix (CHECKDB, an index, statistics, the first backup,
// the TempDB files: docs/phase4e-contract.md part 2) shows its progress while it applies and the time it
// took when done. Phase 4f: such a fix's apply answers at once with a pending run, which the dialog polls
// every 2 seconds until it ends (done or error, as before). While it is pending Cancel is not offered but
// Close is: the run goes on on the server, and onClose gets the line the page shows meanwhile.
export function FixDialog({
  serverId,
  database,
  fixId,
  title,
  options: initialOptions,
  onClose,
  onApplied,
  preview: previewFix = api.previewFix,
  apply: applyFix = api.applyFix,
  run: loadRun = api.fixRun,
}: {
  serverId: number
  database: string
  fixId: string
  title: string
  // The values this particular fix needs before any input (a finding's table, flag, columns, session id):
  // sent with the first preview, which echoes them in preview.options (docs/phase4c-contract.md).
  options?: Options
  // notice: closed while a long fix still runs in the background, what the page shows meanwhile.
  onClose: (notice?: string) => void
  onApplied: (result: FixResult) => void
  preview?: PreviewFn
  apply?: ApplyFn
  run?: RunFn
}) {
  const [step, setStep] = useState<Step>({ kind: "loading" })
  const [pendingRun, setPendingRun] = useState<PendingRun | null>(null)
  // Phase 5a (SCOPE rule 5): the "More options" fold, kept here so a rebuilt preview does not close it.
  const [moreOpen, setMoreOpen] = useState(false)
  // The page's callback, read when the run ends (callers pass a new function on every render).
  const appliedRef = useRef(onApplied)
  useEffect(() => {
    appliedRef.current = onApplied
  })
  // Copy script (docs/phase4a-contract.md): the statement and, commented out, its rollback.
  const script = useCopy()
  const copyScript = (preview: FixPreview) => script.copy(scriptText(preview.statement, preview.rollback))

  useEffect(() => {
    let cancelled = false
    previewFix(serverId, database, fixId, initialOptions)
      .then((preview) => !cancelled && setStep({ kind: "preview", preview, applying: false }))
      .catch((e: Error) => !cancelled && setStep({ kind: "failed", message: e.message }))
    return () => {
      cancelled = true
    }
    // initialOptions is read once, when the dialog opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serverId, database, fixId, previewFix])

  // Phase 4f: poll a pending run every 2 seconds (each read after the last answered) until it ends. A
  // failed read tries again; a run kakapo no longer knows (404: the exe restarted) ends the wait.
  useEffect(() => {
    if (!pendingRun) return
    const { runId, preview, startedAt } = pendingRun
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const poll = async () => {
      try {
        const r = await loadRun(serverId, database, runId)
        if (cancelled) return
        if (!r.pending) {
          setPendingRun(null)
          if (r.result) {
            const took = r.endedAt
              ? (Date.parse(r.endedAt) - Date.parse(r.startedAt)) / 1000
              : (Date.now() - startedAt) / 1000
            setStep({ kind: "done", result: r.result, tookSeconds: took, options: preview.options })
            appliedRef.current(r.result)
          } else {
            const error = r.error?.message ?? "The fix ended without a result: refresh the card to see where it stands."
            setStep({ kind: "preview", preview, applying: false, error })
          }
          return
        }
      } catch (e) {
        if (cancelled) return
        if (e instanceof ApiError && e.status === 404) {
          setPendingRun(null)
          setStep({
            kind: "preview",
            preview,
            applying: false,
            error: "kakapo no longer knows this run (it was restarted): refresh the card to see the outcome.",
          })
          return
        }
        /* a failed read: try again */
      }
      if (!cancelled) timer = setTimeout(poll, 2000)
    }
    timer = setTimeout(poll, 2000)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [pendingRun, serverId, database, loadRun])

  const applying = step.kind === "preview" && step.applying
  const pending = pendingRun !== null
  // Closing while a run is pending leaves it running and tells the page.
  const close = () => onClose(pending ? stillRunningNotice(fixId, database) : undefined)
  const refreshing = step.kind === "preview" && !!step.refreshing
  const shown = step.kind === "preview" ? step.preview : step.kind === "done" ? step.result.preview : null

  // Rebuild the preview with one input changed; the other options stay as the preview echoed them.
  async function changeInput(preview: FixPreview, name: string, value: string) {
    const options = { ...(preview.options ?? {}), [name]: value }
    setStep({ kind: "preview", preview, applying: false, refreshing: true })
    try {
      const next = await previewFix(serverId, database, fixId, options)
      setStep({ kind: "preview", preview: next, applying: false })
    } catch (e) {
      setStep({ kind: "preview", preview, applying: false, previewError: (e as Error).message })
    }
  }

  async function run(preview: FixPreview) {
    const applyStartedAt = Date.now()
    setStep({ kind: "preview", preview, applying: true, applyStartedAt })
    try {
      const result = await applyFix(serverId, database, fixId, preview.options)
      if (result.pending && result.runId) {
        // The fix runs in the background: stay in the applying state and poll the run.
        setPendingRun({ runId: result.runId, preview, startedAt: applyStartedAt })
        return
      }
      setStep({ kind: "done", result, tookSeconds: (Date.now() - applyStartedAt) / 1000, options: preview.options })
      onApplied(result)
    } catch (e) {
      setStep({ kind: "preview", preview, applying: false, error: (e as Error).message })
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && (!applying || pending) && close()}>
      <DialogContent className="sm:max-w-2xl max-h-[90svh] overflow-y-auto" showCloseButton={!applying || pending}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {shown ? (shown.database ? `Database ${shown.database}` : "Server setting") : database}
            {shown?.engineVersion && <>, SQL Server {shown.engineVersion}</>}
          </DialogDescription>
        </DialogHeader>

        {step.kind === "loading" && (
          <div className="flex items-center gap-2 text-muted-foreground py-4">
            <Loader2 className="size-4 animate-spin" />
            Reading the current value and building the statement…
          </div>
        )}

        {step.kind === "failed" && (
          <Alert variant="destructive">
            <XCircle className="size-4" />
            <AlertTitle>Could not build the preview</AlertTitle>
            <AlertDescription className="break-words">{step.message}</AlertDescription>
          </Alert>
        )}

        {/* Phase 5n: what the change does for this database, the preview's first line (docs/phase5n-contract.md). */}
        {step.kind === "preview" && !refreshing && step.preview.lead && (
          <p className="font-medium break-words">{step.preview.lead}</p>
        )}

        {/* Phase 5l: the room on each drive, first, rebuilt with the preview (docs/phase5l-contract.md). */}
        {step.kind === "preview" && !refreshing && <RoomLines room={step.preview.room} />}

        {step.kind === "preview" && step.preview.inputs && step.preview.inputs.length > 0 && (
          <FixInputs
            // A new preview resets the fields to the values it was built with (the folder as normalised).
            key={JSON.stringify(step.preview.options ?? {})}
            inputs={step.preview.inputs}
            disabled={applying || refreshing}
            onCommit={(name, value) => changeInput(step.preview, name, value)}
            moreOpen={moreOpen}
            onMoreOpen={setMoreOpen}
          />
        )}

        {step.kind === "preview" && step.previewError && (
          <Alert variant="destructive">
            <XCircle className="size-4" />
            <AlertTitle>Could not build the preview</AlertTitle>
            <AlertDescription className="break-words">{step.previewError}</AlertDescription>
          </Alert>
        )}

        {step.kind === "preview" &&
          (refreshing ? (
            <div className="flex items-center gap-2 text-muted-foreground py-2">
              <Loader2 className="size-4 animate-spin" />
              Checking and building the statement again…
            </div>
          ) : (
            <PreviewBody preview={step.preview} />
          ))}

        {/* Phase 5m: something the preview names holds Apply back (a transaction left open); Copy script stays. */}
        {step.kind === "preview" && !refreshing && step.preview.held && (
          <Alert className="border-amber-400 dark:border-amber-800">
            <TriangleAlert className="size-4" />
            <AlertTitle>Apply is held back</AlertTitle>
            <AlertDescription className="break-words">{step.preview.held}</AlertDescription>
          </Alert>
        )}

        {step.kind === "preview" && step.error && (
          <Alert variant="destructive">
            <XCircle className="size-4" />
            <AlertTitle>The statement did not apply</AlertTitle>
            <AlertDescription className="break-words">{step.error}</AlertDescription>
          </Alert>
        )}

        {step.kind === "preview" && step.applying && step.applyStartedAt !== undefined && (
          <FixProgress
            serverId={serverId}
            database={database}
            fixId={fixId}
            options={step.preview.options}
            startedAt={step.applyStartedAt}
            pending={pending}
          />
        )}

        {step.kind === "done" && (
          <ResultBody
            result={step.result}
            took={<FixTook fixId={fixId} options={step.options} seconds={step.tookSeconds} />}
          />
        )}

        {script.fallback && <CopyFallback text={script.fallback} onClose={script.dismiss} />}

        <DialogFooter>
          {step.kind === "done" ? (
            <>
              {step.result.preview.statement && (
                <CopyScriptButton
                  status={script.status}
                  variant="outline"
                  onCopy={() => copyScript(step.result.preview)}
                />
              )}
              <Button onClick={close}>Close</Button>
            </>
          ) : pending ? (
            <>
              <Button variant="outline" onClick={close}>
                Close
              </Button>
              <Button disabled>
                <Loader2 className="size-4 animate-spin" />
                Applying…
              </Button>
            </>
          ) : (
            <>
              <Button variant="outline" onClick={close} disabled={applying}>
                Cancel
              </Button>
              {step.kind === "preview" &&
                step.preview.changes.length > 0 &&
                !roomHeld(step.preview) &&
                (step.preview.express ? (
                  <CopyScriptButton
                    status={script.status}
                    disabled={refreshing}
                    onCopy={() => copyScript(step.preview)}
                  />
                ) : (
                  <>
                    <CopyScriptButton
                      status={script.status}
                      variant="outline"
                      disabled={refreshing}
                      onCopy={() => copyScript(step.preview)}
                    />
                    {!step.preview.held && (
                      <Button onClick={() => run(step.preview)} disabled={applying || refreshing}>
                        {applying && <Loader2 className="size-4 animate-spin" />}
                        {applying ? "Applying…" : "Apply"}
                      </Button>
                    )}
                  </>
                ))}
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// The values the user may set (the backup folder, take the first backup now). A text field commits when
// it is left or on Enter, a checkbox and a select at once; each commit rebuilds the preview. A field's
// warning (the folder does not exist) shows under it in red. Phase 5a (SCOPE rule 5): a read-only input (a
// table the finding or the picker chose) is a line, not a field; advanced inputs sit under "More options",
// collapsed unless one of them carries a warning (moreOpen is the parent's, so a rebuilt preview keeps it).
function FixInputs({
  inputs,
  disabled,
  onCommit,
  moreOpen,
  onMoreOpen,
}: {
  inputs: FixInput[]
  disabled: boolean
  onCommit: (name: string, value: string) => void
  moreOpen: boolean
  onMoreOpen: (open: boolean) => void
}) {
  const [draft, setDraft] = useState<Record<string, string>>(() =>
    Object.fromEntries(inputs.map((i) => [i.name, i.value])),
  )
  const commit = (input: FixInput, value: string) => {
    if (value !== input.value) onCommit(input.name, value)
  }
  const readOnly = inputs.filter((i) => i.readOnly)
  const main = inputs.filter((i) => !i.readOnly && !i.advanced)
  const advanced = inputs.filter((i) => !i.readOnly && i.advanced)
  const warned = advanced.some((i) => !!i.warning)

  const field = (input: FixInput) => {
    const id = `fix-input-${input.name}`
    if (input.kind === "bool") {
      return (
        <div key={input.name} className="flex items-start gap-2">
          <Checkbox
            id={id}
            className="mt-0.5"
            checked={draft[input.name] === "true"}
            disabled={disabled}
            onCheckedChange={(checked) => {
              const value = checked === true ? "true" : "false"
              setDraft({ ...draft, [input.name]: value })
              commit(input, value)
            }}
          />
          <div className="grid gap-0.5">
            <Label htmlFor={id}>{input.label}</Label>
            {input.help && <p className="text-xs text-muted-foreground">{input.help}</p>}
          </div>
        </div>
      )
    }
    // Phase 4b: a select (the log backup interval) commits at once, like a checkbox.
    if (input.kind === "select") {
      return (
        <div key={input.name} className="grid gap-1.5">
          <Label htmlFor={id}>{input.label}</Label>
          <Select
            value={draft[input.name] ?? ""}
            disabled={disabled}
            onValueChange={(value) => {
              setDraft({ ...draft, [input.name]: value })
              commit(input, value)
            }}
          >
            <SelectTrigger id={id} className="w-fit min-w-40 max-w-full" aria-invalid={!!input.warning}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(input.options ?? []).map((option) => (
                <SelectItem key={option} value={option}>
                  {option}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {input.warning && <p className="text-sm text-destructive break-words">{input.warning}</p>}
          {input.help && <p className="text-xs text-muted-foreground">{input.help}</p>}
        </div>
      )
    }
    return (
      <div key={input.name} className="grid gap-1.5">
        <Label htmlFor={id}>{input.label}</Label>
        <Input
          id={id}
          className="font-mono"
          value={draft[input.name] ?? ""}
          disabled={disabled}
          aria-invalid={!!input.warning && !input.warning.startsWith("This folder is on the same drive")}
          onChange={(e) => setDraft({ ...draft, [input.name]: e.target.value })}
          onBlur={(e) => commit(input, e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              commit(input, e.currentTarget.value)
            }
          }}
        />
        {input.warning && <p className="text-sm text-destructive break-words">{input.warning}</p>}
        {input.help && <p className="text-xs text-muted-foreground">{input.help}</p>}
      </div>
    )
  }

  return (
    <div className="grid gap-3">
      {readOnly.map((input) => (
        <p key={input.name} className="break-words">
          <span className="text-muted-foreground">{input.label}:</span>{" "}
          <span className="font-mono">{input.value}</span>
        </p>
      ))}
      {main.map(field)}
      {advanced.length > 0 && (
        <Collapsible open={moreOpen || warned} onOpenChange={(open) => onMoreOpen(open)}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="group -ml-2.5 text-muted-foreground" disabled={warned}>
              <ChevronRight className="transition-transform group-data-[state=open]:rotate-90" />
              More options
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="grid gap-3 pt-2">{advanced.map(field)}</CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}

// Copy script, to run the statement by hand: beside Apply in the preview and after an apply; on Express
// (nothing is applied, the script runs from Task Scheduler and sqlcmd) it is the primary button. When the
// clipboard is refused, the script shows selected above the buttons.
function CopyScriptButton({
  status,
  onCopy,
  variant = "default",
  disabled = false,
}: {
  status: CopyStatus
  onCopy: () => void
  variant?: "default" | "outline"
  disabled?: boolean
}) {
  return (
    <Button variant={variant} onClick={onCopy} disabled={disabled}>
      {status === "copied" ? <Check /> : <Copy />}
      {status === "copied" ? "Copied" : "Copy script"}
    </Button>
  )
}

function PreviewBody({ preview }: { preview: FixPreview }) {
  const warnings =
    preview.warnings.length > 0 ? (
      <div className="grid gap-1.5">
        <div className="font-medium">Before you apply</div>
        <ul className="list-disc pl-5 grid gap-1 text-muted-foreground">
          {preview.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      </div>
    ) : null

  if (preview.changes.length === 0) {
    // With inputs, an empty preview means an input is not usable yet (the folder does not exist), or (phase
    // 5a) something about the object itself stops the fix (a foreign key pointing at the table): the first
    // warning says which.
    if (preview.inputs && preview.inputs.length > 0) {
      // The archive fix's red "delete keeps no copy" warning on its action does not block.
      const inputWarned = preview.inputs.some((i) => !!i.warning && i.name !== "action")
      return (
        <div className="grid gap-4 min-w-0">
          <p className="text-muted-foreground">
            {inputWarned
              ? "Nothing can be applied until the value above is fixed."
              : "Nothing can be applied: the first point below says why."}
          </p>
          {warnings}
        </div>
      )
    }
    return (
      <p className="text-muted-foreground">
        {preview.note || "Nothing to change: the setting already has the recommended value."}
      </p>
    )
  }
  return (
    <div className="grid gap-4 min-w-0">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Setting</TableHead>
            <TableHead>Current</TableHead>
            <TableHead>Target</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {preview.changes.map((c) => (
            <TableRow key={c.setting}>
              <TableCell className="font-mono text-xs whitespace-normal break-words">{c.setting}</TableCell>
              <TableCell className="whitespace-normal break-words">
                {c.current || <span className="text-muted-foreground">{preview.inputs ? "" : "not set"}</span>}
              </TableCell>
              <TableCell className="whitespace-normal break-words">{c.target}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      {warnings}

      <StatementBlock label={preview.express ? "Script to run by hand" : "Statement"} text={preview.statement} />

      {preview.rollback && (
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="group -ml-2.5 text-muted-foreground">
              <ChevronRight className="transition-transform group-data-[state=open]:rotate-90" />
              Rollback statement
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-2">
            <StatementBlock text={preview.rollback} />
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}

// What was read back after the statement ran. Not verified means the value read back is not the
// target: the statement ran but the setting did not take (or something set it back). For the backup
// jobs it means a job was not read back enabled.
function ResultBody({ result, took }: { result: FixResult; took?: ReactNode }) {
  // The backup jobs, and (phase 5a) the archive and remove-archive fixes' "Job" row.
  const jobs = result.preview.changes.some((c) => c.setting.endsWith("backup job") || c.setting === "Job")
  const target = result.preview.changes.map((c) => c.target).join(", ")
  return (
    <div className="grid gap-2 min-w-0">
      {result.verified ? (
        <p className="flex items-start gap-1.5 font-medium text-emerald-600 dark:text-emerald-400">
          <CircleCheck className="size-4 shrink-0 translate-y-0.5" />
          <span className="break-words">Applied and read back: {result.after}</span>
        </p>
      ) : (
        <>
          <p className="font-medium break-words">Applied and read back: {result.after}</p>
          <p className="flex items-start gap-1.5 text-amber-600 dark:text-amber-400">
            <TriangleAlert className="size-4 shrink-0 translate-y-0.5" />
            <span className="break-words">
              {jobs ? (
                <>
                  Not verified: the jobs read back are not as created. Refresh the card and check the jobs in SSMS
                  (SQL Server Agent, Jobs) before relying on them.
                </>
              ) : (
                <>
                  Not verified: the value read back is not the target{target && <> ({target})</>}. Refresh the card
                  and check the setting before relying on it.
                </>
              )}
            </span>
          </p>
        </>
      )}
      <p className="text-muted-foreground break-words">Before: {result.before}</p>
      {took}
      <p className="text-xs text-muted-foreground">
        Logged as change {result.changeId} at {new Date(result.appliedAt).toLocaleString()}.
      </p>
    </div>
  )
}

// Phase 5l: a change that does not fit on one of its drives is not offered (the server refuses it too).
function roomHeld(preview: FixPreview): boolean {
  return (preview.room ?? []).some((l) => l.state === "nofit")
}
