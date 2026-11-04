import { OctagonX } from "lucide-react"

import type { BlockingNode } from "@/api-blocking"
import { duration, kindText, sessionsWord, whoText } from "@/components/blocking/blocking-text"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

const mb = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1, minimumFractionDigits: 1 })

// One session and, indented under it, the sessions that wait for it. A head blocker gets Kill (or the
// reason kakapo does not kill it).
export function ChainNode({
  node: n,
  head = false,
  onKill,
}: {
  node: BlockingNode
  head?: boolean
  onKill?: (n: BlockingNode) => void
}) {
  const facts = [
    n.status,
    n.login && `login ${n.login}`,
    n.database && `database ${n.database}`,
    n.isolation,
    n.tranSeconds > 0 && `transaction open ${duration(n.tranSeconds)}, ${mb.format(n.logBytes / 1048576)} MB of log`,
    n.tranSeconds === 0 && n.openTran > 0 && `${n.openTran} open transaction${n.openTran === 1 ? "" : "s"}`,
  ].filter(Boolean)
  return (
    <div className="grid gap-2 min-w-0">
      <div className="grid gap-1 min-w-0">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-medium">Session {n.session}</span>
          <span className="text-sm text-muted-foreground break-all">{whoText(n)}</span>
          {n.kinds.map((k) => (
            <Badge
              key={k}
              className={cn(
                "whitespace-normal h-auto",
                k === "rolling-back" ? "bg-muted text-muted-foreground" : "bg-amber-500/15 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400",
              )}
            >
              {kindText[k] ?? k}
            </Badge>
          ))}
        </div>
        <p className="text-xs text-muted-foreground break-words">{facts.join(" · ")}</p>
        {n.waitType && (
          <p className="text-sm break-words">
            Waits {duration(Math.floor(n.waitMs / 1000))} on {n.waitType}
            {n.resource && <> for {n.resource}</>}
            {n.lockMode && <span className="text-muted-foreground"> ({n.lockMode})</span>}
          </p>
        )}
        {head && n.behind > 0 && (
          <p className="text-sm">
            {sessionsWord(n.behind)} {n.behind === 1 ? "waits" : "wait"} behind it
            {n.longestWaitSec > 0 && <>, the longest for {duration(n.longestWaitSec)}</>}.
          </p>
        )}
        {n.statement && (
          <div className="grid gap-0.5 min-w-0">
            <span className="text-xs text-muted-foreground">
              {n.statementIsLast ? "Last ran" : "Running"}
              {n.objectName && <> in {n.objectName}</>}
            </span>
            <pre className="rounded-md bg-muted px-2 py-1.5 font-mono text-xs whitespace-pre-wrap break-words max-h-24 overflow-auto">
              {n.statement}
            </pre>
          </div>
        )}
        {head && onKill && (
          <div className="pt-1">
            {n.canKill ? (
              <Button variant="outline" size="sm" onClick={() => onKill(n)}>
                <OctagonX />
                Kill session {n.session}
              </Button>
            ) : (
              n.killRefusal && <p className="text-xs text-muted-foreground">{n.killRefusal}</p>
            )}
          </div>
        )}
      </div>
      {n.victims.length > 0 && (
        <div className="grid gap-3 border-l-2 border-border pl-2 sm:pl-4 ml-1 sm:ml-2">
          {n.victims.map((v) => (
            <ChainNode key={v.session} node={v} />
          ))}
        </div>
      )}
    </div>
  )
}
