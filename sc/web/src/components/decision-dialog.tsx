import { useState } from "react"
import { ChevronRight, Loader2, XCircle } from "lucide-react"

import { api, type DecisionInput, type DecisionRecord, type DecisionVerdict } from "@/api"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

// Record a decision (docs/phase5m-contract.md, section 6): the choice (Keep it as it is, Decide later; Change it
// is the fix's Apply, which records itself), the reason (required for Keep), and Review by under "More options"
// (90 days from today by default, SCOPE rule 5). The verdict the row shows is stored with it. A record is
// never edited: a new one replaces it.

const reviewDays = 90

function defaultReview(): string {
  const d = new Date()
  d.setDate(d.getDate() + reviewDays)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
}

const choices: { value: DecisionInput["choice"]; label: string; help: string }[] = [
  { value: "keep", label: "Keep it as it is", help: "The setting stays; say why, so whoever looks next knows." },
  { value: "later", label: "Decide later", help: "Come back to it by the review date." },
]

export function DecisionDialog({
  serverId,
  database,
  title,
  verdict,
  onClose,
  onRecorded,
}: {
  serverId: number
  database: string
  title: string
  verdict: DecisionVerdict
  onClose: () => void
  onRecorded: (r: DecisionRecord) => void
}) {
  const [choice, setChoice] = useState<DecisionInput["choice"]>("keep")
  const [reason, setReason] = useState("")
  const [reviewOn, setReviewOn] = useState(defaultReview)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const needsReason = choice === "keep" && reason.trim() === ""

  async function save() {
    setSaving(true)
    setError(null)
    try {
      const r = await api.recordDecision(serverId, database, { choice, verdict, reason: reason.trim(), reviewOn })
      onRecorded(r)
      onClose()
    } catch (e) {
      setError((e as Error).message)
      setSaving(false)
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !saving && onClose()}>
      <DialogContent className="sm:max-w-lg max-h-[90svh] overflow-y-auto" showCloseButton={!saving}>
        <DialogHeader>
          <DialogTitle>Record a decision</DialogTitle>
          <DialogDescription>
            {title}, database {database}
          </DialogDescription>
        </DialogHeader>

        <fieldset className="grid gap-2" disabled={saving}>
          <legend className="sr-only">Decision</legend>
          {choices.map((c) => (
            <label
              key={c.value}
              className={cn(
                "flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2",
                choice === c.value && "border-primary bg-muted/50",
              )}
            >
              <input
                type="radio"
                name="decision-choice"
                aria-label={c.label}
                className="mt-1"
                checked={choice === c.value}
                onChange={() => setChoice(c.value)}
              />
              <span className="grid gap-0.5">
                <span className="font-medium">{c.label}</span>
                <span className="text-xs text-muted-foreground">{c.help}</span>
              </span>
            </label>
          ))}
          <p className="text-xs text-muted-foreground">Change it is the preview's Apply: it records itself.</p>
        </fieldset>

        <div className="grid gap-1.5">
          <Label htmlFor="decision-reason">Reason{choice === "keep" ? "" : " (optional)"}</Label>
          <textarea
            id="decision-reason"
            className="min-h-20 w-full rounded-md border bg-transparent px-3 py-2 text-sm shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            value={reason}
            disabled={saving}
            onChange={(e) => setReason(e.target.value)}
            placeholder={choice === "keep" ? "Waiting for the vendor to confirm the code does not rely on blocking" : ""}
          />
        </div>

        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm" className="group -ml-2.5 text-muted-foreground">
              <ChevronRight className="transition-transform group-data-[state=open]:rotate-90" />
              More options
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="grid gap-1.5 pt-2">
            <Label htmlFor="decision-review">Review by</Label>
            <Input
              id="decision-review"
              type="date"
              className="w-fit"
              value={reviewOn}
              disabled={saving}
              onChange={(e) => setReviewOn(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">90 days from today unless you choose another date.</p>
          </CollapsibleContent>
        </Collapsible>

        {error && (
          <Alert variant="destructive">
            <XCircle className="size-4" />
            <AlertTitle>Not recorded</AlertTitle>
            <AlertDescription className="break-words">{error}</AlertDescription>
          </Alert>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={save} disabled={saving || needsReason}>
            {saving && <Loader2 className="size-4 animate-spin" />}
            Record
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
