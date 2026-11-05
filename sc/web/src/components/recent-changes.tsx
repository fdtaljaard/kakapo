import { useState } from "react"
import { Link, useParams } from "react-router-dom"
import { Check, Copy, PenLine } from "lucide-react"

import { recordedByHand, seenOnServer, type Change, type ChangeWindow } from "@/api"
import { CopyFallback } from "@/components/statement-block"
import { RecordChangeForm } from "@/components/tuning/record-change-form"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen } from "@/lib/format"
import { tuningPath } from "@/lib/paths"
import { useCopy } from "@/lib/use-copy"

// The "prove it" panel under the cards: what changed on this database, newest first. Every row is about the
// page's database, so there is no database column. On a phone the result folds under the description so
// nothing scrolls sideways. Phase 5c (docs/phase5c-contract.md): each change opens the tuning report centred
// on it ("Did it help?"), and Record a change adds one made outside kakapo, marked "(by hand)". The server
// and database come from the page's route. Phase 5f (docs/phase5f-contract.md): the changes kakapo read from
// SQL Server's default trace are marked "(on the server)", a deploy's Copy carries its objects, and the line
// under the table says how far back those changes are seen.
export function RecentChanges({ changes, window }: { changes: Change[]; window?: ChangeWindow }) {
  const params = useParams()
  const serverId = Number(params.id)
  const database = params.db ?? ""
  const [recording, setRecording] = useState(false)
  // Recorded here since the page was read; the next read of the page carries them itself.
  const [added, setAdded] = useState<Change[]>([])
  const rows = [...added.filter((a) => !changes.some((c) => c.id === a.id)), ...changes]
  const canLink = Number.isInteger(serverId) && serverId > 0 && database !== ""
  const { status, fallback, copy, dismiss } = useCopy()
  const [copied, setCopied] = useState<string | null>(null)
  const copyText = (key: string, text: string) => {
    setCopied(key)
    void copy(text)
  }

  return (
    <Card size="sm">
      <CardHeader className="flex flex-wrap items-center justify-between gap-2">
        <CardTitle>Recent changes</CardTitle>
        {canLink && !recording && (
          <Button variant="outline" size="sm" onClick={() => setRecording(true)}>
            <PenLine />
            Record a change
          </Button>
        )}
      </CardHeader>
      <CardContent className="grid gap-3">
        {recording && (
          <RecordChangeForm
            serverId={serverId}
            database={database}
            onRecorded={(c) => {
              setAdded((a) => [c, ...a])
              setRecording(false)
            }}
            onCancel={() => setRecording(false)}
          />
        )}
        {rows.length === 0 ? (
          <p className="text-muted-foreground">No changes made through kakapo, recorded by hand or seen on the server yet.</p>
        ) : (
          <Table className="text-[0.8125rem]">
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="pl-0 text-xs text-muted-foreground">When</TableHead>
                <TableHead className="text-xs text-muted-foreground">What</TableHead>
                <TableHead className="hidden md:table-cell text-xs text-muted-foreground">Result</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((c) => {
                const byHand = c.result === recordedByHand
                const onServer = c.result === seenOnServer
                const noResult = byHand || onServer
                const key = `change-${c.id}`
                return (
                  <TableRow key={c.id} className="hover:bg-transparent">
                    <TableCell className="pl-0 align-top">{formatWhen(c.changedAt, "row")}</TableCell>
                    <TableCell className="align-top whitespace-normal wrap-anywhere min-w-48">
                      <div>
                        {c.description}
                        {byHand && <span className="text-muted-foreground"> (by hand)</span>}
                        {onServer && <span className="text-muted-foreground"> (on the server)</span>}
                      </div>
                      {!noResult && <div className="md:hidden text-xs text-muted-foreground">{c.result}</div>}
                      <div className="flex flex-wrap items-center gap-3">
                        {canLink && (
                          <Link
                            to={tuningPath(serverId, database, c.id)}
                            className="text-xs text-primary underline-offset-2 hover:underline"
                          >
                            Did it help?
                          </Link>
                        )}
                        {c.copy && (
                          <button
                            type="button"
                            className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
                            onClick={() => copyText(key, c.copy ?? "")}
                          >
                            {status === "copied" && copied === key ? <Check className="size-3" /> : <Copy className="size-3" />}
                            {status === "copied" && copied === key ? "Copied" : "Copy"}
                          </button>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell align-top whitespace-normal wrap-anywhere text-muted-foreground w-72 max-w-72">
                      {noResult ? "" : c.result}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
        {window && (
          <div className="flex flex-wrap items-start gap-2 text-xs text-muted-foreground">
            <p className="min-w-0 flex-1">{window.text}</p>
            {window.copy && (
              <Button variant="outline" size="sm" onClick={() => copyText("grant", window.copy ?? "")}>
                {status === "copied" && copied === "grant" ? <Check /> : <Copy />}
                {status === "copied" && copied === "grant" ? "Copied" : "Copy the grant"}
              </Button>
            )}
          </div>
        )}
        {fallback && <CopyFallback text={fallback} onClose={dismiss} />}
      </CardContent>
    </Card>
  )
}
