// Small date and text helpers for the instance pages. Times are shown in the browser's time zone.

const time = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" })
const dayMonth = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" })
const dayMonthYear = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" })
const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

function startOfDay(d: Date) {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

// Inside a sentence ("as of ..."): "09:21", "yesterday 16:40", "28 Oct 11:00", "28 Oct 2025 11:00".
// At the start of a table row: "Today 09:21", "Yesterday 16:40", then the same.
export function formatWhen(iso: string, style: "inline" | "row" = "inline"): string {
  const now = new Date()
  const d = new Date(iso)
  const days = Math.round((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (days === 0) return style === "row" ? `Today ${time.format(d)}` : time.format(d)
  if (days === 1) return `${style === "row" ? "Yesterday" : "yesterday"} ${time.format(d)}`
  const date = d.getFullYear() === now.getFullYear() ? dayMonth.format(d) : dayMonthYear.format(d)
  return `${date} ${time.format(d)}`
}

// "5 days ago", "yesterday", "3 hours ago".
export function formatAgo(iso: string): string {
  const hours = (Date.now() - new Date(iso).getTime()) / 3_600_000
  if (hours < 1) return relative.format(-Math.max(1, Math.round(hours * 60)), "minute")
  if (hours < 24) return relative.format(-Math.round(hours), "hour")
  return relative.format(-Math.round(hours / 24), "day")
}

// How long SQL Server has been up: "40 minutes", "9 hours", "5 days".
export function formatUptime(startedAt: string): string {
  const hours = (Date.now() - new Date(startedAt).getTime()) / 3_600_000
  if (hours < 1) return plural(Math.max(1, Math.round(hours * 60)), "minute", "minutes")
  if (hours < 24) return plural(Math.round(hours), "hour", "hours")
  return plural(Math.round(hours / 24), "day", "days")
}

// "Standard Edition (64-bit)" -> "Standard".
export function shortEdition(edition: string): string {
  return edition.replace(/\s*Edition\b.*$/i, "").trim() || edition
}

export function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}
