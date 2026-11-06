// Numbers on the query page, as the backend's sentences write them (domain.QueryDuration, QueryRows).

const whole = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 })
const oneDecimal = new Intl.NumberFormat(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 })

export const formatWhole = (n: number) => whole.format(n)

// "under 1 ms", "45 ms", "2.3 s", "41 s", "3.5 min".
export function formatMs(ms: number): string {
  if (ms <= 0) return "0 ms"
  if (ms < 1) return "under 1 ms"
  if (ms < 1000) return `${whole.format(ms)} ms`
  if (ms < 10_000) return `${oneDecimal.format(ms / 1000)} s`
  if (ms < 120_000) return `${whole.format(ms / 1000)} s`
  return `${oneDecimal.format(ms / 60_000)} min`
}

// "980", "45,210", "4.2 M".
export function formatRows(n: number): string {
  return n < 1_000_000 ? whole.format(n) : `${oneDecimal.format(n / 1_000_000)} M`
}

// "[A], [B]" -> "A, B".
export function plainColumns(list: string): string {
  return list
    .split(",")
    .map((c) => c.trim().replace(/^\[(.*)\]$/, "$1").replace(/\]\]/g, "]"))
    .filter(Boolean)
    .join(", ")
}

// The query page says every time on one clock, the server's, as the backend's sentences do (the plan
// cache's times are the server's own; a browser in another time zone would otherwise show a third).
// The summary line says "server time" once. An ISO time (UTC) is moved by the server's offset and
// written in UTC: "15:10", "yesterday 09:00", "3 Nov 10:15"; at the start of a row "Today 15:10".
export interface ServerClock {
  capturedAt: string
  utcOffsetMinutes: number
}

const utcTime = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: "UTC" })
const utcDayMonth = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", timeZone: "UTC" })
const utcDayMonthYear = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric", timeZone: "UTC" })

export function serverWhen(c: ServerClock, iso: string, style: "inline" | "row" = "inline"): string {
  const shift = (s: string) => new Date(new Date(s).getTime() + (c.utcOffsetMinutes ?? 0) * 60_000)
  const d = shift(iso)
  const now = shift(c.capturedAt)
  const day = (x: Date) => Date.UTC(x.getUTCFullYear(), x.getUTCMonth(), x.getUTCDate())
  const days = Math.round((day(now) - day(d)) / 86_400_000)
  if (days === 0) return style === "row" ? `Today ${utcTime.format(d)}` : utcTime.format(d)
  if (days === 1) return `${style === "row" ? "Yesterday" : "yesterday"} ${utcTime.format(d)}`
  const date = d.getUTCFullYear() === now.getUTCFullYear() ? utcDayMonth.format(d) : utcDayMonthYear.format(d)
  return `${date} ${utcTime.format(d)}`
}
