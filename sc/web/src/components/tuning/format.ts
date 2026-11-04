// Times and numbers of the tuning report. The report's times are the server's clock without a zone
// ("2026-11-03T14:00:00"): new Date() reads them as local wall-clock time, so formatting them here shows
// exactly what the server's clock said, whatever the browser's zone.

const dayMonth = new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short" })
const pad = (n: number) => String(n).padStart(2, "0")

export const serverDate = (s: string) => new Date(s)

// "14:00"
export function serverClock(s: string): string {
  const d = serverDate(s)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// "Tue 3 Nov"
export function serverDay(s: string): string {
  return dayMonth.format(serverDate(s)).replace(",", "")
}

// "Tue 3 Nov 14:00", as the backend's sentences write it.
export function serverWhen(s: string): string {
  return s ? `${serverDay(s)} ${serverClock(s)}` : "—"
}

// The value of a datetime-local field for a server time: "2026-11-03T14:20".
export function localInputValue(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const whole = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 })
const one = new Intl.NumberFormat("en-US", { minimumFractionDigits: 1, maximumFractionDigits: 1 })

export const formatCount = (n: number) => whole.format(n)

// CPU in milliseconds as the report's sentences write it: "0.04 ms", "12.4 ms", "450 ms", "12.5 s".
export function tuningMs(ms: number | null): string {
  if (ms === null) return "—"
  if (ms < 0.1) return `${ms.toFixed(2)} ms`
  if (ms < 100) return `${one.format(ms)} ms`
  if (ms < 10_000) return `${whole.format(ms)} ms`
  return `${one.format(ms / 1000)} s`
}

// "+12%", "−33%".
export function signedPct(p: number | null): string {
  if (p === null) return ""
  if (p === 0) return "0%"
  return `${p > 0 ? "+" : "−"}${Math.abs(p)}%`
}
