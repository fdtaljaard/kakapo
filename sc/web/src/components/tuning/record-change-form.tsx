import { useState, type FormEvent } from "react"
import { Loader2 } from "lucide-react"

import { api, changeTypes, type Change, type RecordChangeInput } from "@/api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"

const typeWords: Record<RecordChangeInput["changeType"], string> = {
  PROCEDURE: "Procedure",
  VIEW: "View",
  INDEX: "Index",
  STATISTICS: "Statistics",
  SETTING: "Setting",
  OTHER: "Other",
}

// Record a change made outside kakapo (docs/phase5c-contract.md): the object, its type, what changed and
// when (server time; empty is now). It writes kakapo's change log only, so the tuning report can measure
// it; nothing is sent to the server.
export function RecordChangeForm({
  serverId,
  database,
  onRecorded,
  onCancel,
}: {
  serverId: number
  database: string
  onRecorded: (c: Change) => void
  onCancel: () => void
}) {
  const [object, setObject] = useState("")
  const [changeType, setChangeType] = useState<RecordChangeInput["changeType"]>("PROCEDURE")
  const [description, setDescription] = useState("")
  const [changedAt, setChangedAt] = useState("")
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      const c = await api.recordChange(serverId, database, { object, changeType, description, changedAt })
      onRecorded(c)
    } catch (err) {
      setError((err as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-3 rounded-xl border p-3 min-w-0" aria-label="Record a change">
      <p className="text-sm text-muted-foreground">
        A change made outside kakapo, such as a procedure deployed or an index created in SSMS. It goes into kakapo's change log
        only, so the tuning report can measure it.
      </p>
      <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
        <div className="grid gap-1.5 min-w-0">
          <Label htmlFor="rc-object">What changed</Label>
          <Input id="rc-object" value={object} onChange={(e) => setObject(e.target.value)} placeholder="dbo.USP_PICKING" maxLength={200} required />
        </div>
        <div className="grid gap-1.5 min-w-0">
          <Label htmlFor="rc-type">Type</Label>
          <Select value={changeType} onValueChange={(v) => setChangeType(v as RecordChangeInput["changeType"])}>
            <SelectTrigger id="rc-type" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {changeTypes.map((t) => (
                <SelectItem key={t} value={t}>
                  {typeWords[t]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="grid gap-1.5 min-w-0">
        <Label htmlFor="rc-description">Description</Label>
        <Input
          id="rc-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="Added OPTION (RECOMPILE) to the barcode lookup"
          maxLength={1000}
          required
        />
      </div>
      <div className="grid gap-1.5 min-w-0 sm:max-w-64">
        <Label htmlFor="rc-when">When, server time</Label>
        <Input id="rc-when" type="datetime-local" value={changedAt} onChange={(e) => setChangedAt(e.target.value)} />
        <span className="text-xs text-muted-foreground">Leave empty for now.</span>
      </div>
      {error && <p className="text-sm text-destructive break-words">{error}</p>}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" disabled={saving || !object.trim() || !description.trim()}>
          {saving && <Loader2 className="animate-spin" />}
          Record the change
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={saving}>
          Cancel
        </Button>
      </div>
    </form>
  )
}
