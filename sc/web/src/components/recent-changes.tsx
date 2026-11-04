import type { Change } from "@/api"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatWhen } from "@/lib/format"

// The "prove it" panel under the cards: what kakapo changed on this database, newest first.
// Every row is about the page's database, so there is no database column. On a phone the result
// folds under the description so nothing scrolls sideways.
export function RecentChanges({ changes }: { changes: Change[] }) {
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>Recent changes</CardTitle>
      </CardHeader>
      <CardContent>
        {changes.length === 0 ? (
          <p className="text-muted-foreground">No changes made through kakapo yet.</p>
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
              {changes.map((c) => (
                <TableRow key={c.id} className="hover:bg-transparent">
                  <TableCell className="pl-0 align-top">{formatWhen(c.changedAt, "row")}</TableCell>
                  <TableCell className="align-top whitespace-normal break-words min-w-48">
                    <div>{c.description}</div>
                    <div className="md:hidden text-xs text-muted-foreground">{c.result}</div>
                  </TableCell>
                  <TableCell className="hidden md:table-cell align-top whitespace-normal text-muted-foreground max-w-72">
                    {c.result}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
