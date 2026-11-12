import { formatAgo, formatWhen } from "@/lib/format"

// Sizes and times as the Memory screen says them (docs/phase5v-contract.md).

// A size as people say it: "97.9 GB", "850 MB" under 1 GB.
export function gbText(gb: number): string {
  if (gb < 1) return `${Math.round(gb * 1024)} MB`
  return `${gb.toFixed(1)} GB`
}

export function mbText(mb: number): string {
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
  return `${mb < 10 ? mb.toFixed(1) : Math.round(mb)} MB`
}

// "+1.2 GB", "−300 MB", "no change".
export function diffText(now: number, then: number): string {
  const d = now - then
  if (Math.abs(d) < 0.0005) return "no change"
  return `${d > 0 ? "+" : "−"}${gbText(Math.abs(d))}`
}

// "Read 2 hours ago (14:05), took 12 s".
export function readWhen(iso: string, seconds?: number): string {
  const took = seconds !== undefined && seconds >= 1 ? `, took ${Math.round(seconds)} s` : ""
  const fresh = Date.now() - new Date(iso).getTime() < 60_000
  return `Read ${fresh ? "just now" : formatAgo(iso)} (${formatWhen(iso)})${took}`
}
