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
