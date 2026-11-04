// Words for the blocking page: durations as the server's sentences say them, who a session is, the
// classification badges, and the lines of the chains seen. Pure functions.

import type { BlockKind, BlockingNode, BlockingTree } from "@/api-blocking"
import type { SeenChain } from "@/components/blocking/use-blocking-watch"

export const kindText: Record<BlockKind, string> = {
  sleeping: "Sleeping with a transaction open",
  implicit: "Implicit transaction",
  serializable: "Serializable",
  "repeatable-read": "Repeatable read",
  compile: "Compile lock",
  "long-running": "Long-running statement",
  "rolling-back": "Rolling back",
}

// "45 seconds", "6 minutes", "1.5 hours".
export function duration(sec: number): string {
  if (sec < 60) return `${sec} ${sec === 1 ? "second" : "seconds"}`
  if (sec < 5400) {
    const m = Math.round(sec / 60)
    return `${m} ${m === 1 ? "minute" : "minutes"}`
  }
  return `${(sec / 3600).toFixed(1).replace(/\.0$/, "")} hours`
}

// "AppScheduler on WMS-APP01", or the SQL Agent job.
export function whoText(n: BlockingNode): string {
  const what = n.jobName
    ? `SQL Agent job ${n.jobName}`
    : n.program.startsWith("SQLAgent - TSQL JobStep")
      ? "a SQL Agent job"
      : n.program || n.login || "?"
  return n.host ? `${what} on ${n.host}` : what
}

export function sessionsWord(n: number): string {
  return n === 1 ? "1 session" : `${n} sessions`
}

const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" })

export function clockTime(d: Date): string {
  return clock.format(d)
}

// One chain seen: "Session 64 (SQL Agent job Nightly stock import on SQL01): cleared at 10:14:05; ...".
export function seenText(c: SeenChain): string {
  const state = c.clearedAt ? `cleared at ${clockTime(c.clearedAt)}` : "still blocking"
  return `Session ${c.head.session} (${whoText(c.head)}): ${state}; first seen ${clockTime(c.firstSeen)}, up to ${sessionsWord(
    c.mostBehind,
  )} behind it, longest wait ${duration(c.longestWaitSec)}`
}

// Copy for the vendor: the server's chain text, the chains the watch saw, when it was read, the build.
export function copyText(tree: BlockingTree, seen: SeenChain[], watched: boolean, asOf: string, version: string): string {
  const parts = [tree.handover]
  if (watched && seen.length > 0) parts.push(["Seen while watching for 30 seconds", ...seen.map(seenText)].join("\n"))
  parts.push([asOf && `As of ${asOf}`, version && `From kakapo build ${version}`].filter(Boolean).join("\n"))
  return parts.filter(Boolean).join("\n\n")
}
