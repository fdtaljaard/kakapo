import { useState } from "react"
import { Link, useParams } from "react-router-dom"
import { PenLine } from "lucide-react"

import { recordedByHand, type Change } from "@/api"
import { RecordChangeForm } from "@/components/tuning/record-change-form"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen } from "@/lib/format"
import { tuningPath } from "@/lib/paths"

// The "prove it" panel under the cards: what changed on this database, newest first. Every row is about the
// page's database, so there is no database column. On a phone the result folds under the description so
// nothing scrolls sideways. Phase 5c (docs/phase5c-contract.md): each change opens the tuning report centred
// on it ("Did it help?"), and Record a change adds one made outside kakapo, marked "(by hand)". The server
// and database come from the page's route.
export function RecentChanges({ changes }: { changes: Change[] }) {
  const params = useParams()
  const serverId = Number(params.id)
  const database = params.db ?? ""
  const [recording, setRecording] = useState(false)
  // Recorded here since the page was read; the next read of the page carries them itself.
  const [added, setAdded] = useState<Change[]>([])
  const rows = [...added.filter((a) => !changes.some((c) => c.id === a.id)), ...changes]
  const canLink = Number.isInteger(serverId) && serverId > 0 && database !== ""

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
          <p className="text-muted-foreground">No changes made through kakapo or recorded by hand yet.</p>
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
                return (
                  <TableRow key={c.id} className="hover:bg-transparent">
                    <TableCell className="pl-0 align-top">{formatWhen(c.changedAt, "row")}</TableCell>
                    <TableCell className="align-top whitespace-normal break-words min-w-48">
                      <div>
                        {c.description}
                        {byHand && <span className="text-muted-foreground"> (by hand)</span>}
                      </div>
                      {!byHand && <div className="md:hidden text-xs text-muted-foreground">{c.result}</div>}
                      {canLink && (
                        <Link
                          to={tuningPath(serverId, database, c.id)}
                          className="text-xs text-primary underline-offset-2 hover:underline"
                        >
                          Did it help?
                        </Link>
                      )}
                    </TableCell>
                    <TableCell className="hidden md:table-cell align-top whitespace-normal text-muted-foreground max-w-72">
                      {byHand ? "" : c.result}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
