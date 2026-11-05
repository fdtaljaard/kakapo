// Phase 5i (docs/phase5i-contract.md): the load strip at the top of the database page. How loaded the
// database is now and today, hour by hour, against the usual range of the selected days at that hour,
// and the live red flags. Times are the server's clock without a zone ("2026-11-05T10:41:00").

import { request } from "./api"
import type { RedFlag, RedFlags } from "./api"
import type { QueryAction } from "./api-queries"

export type LoadDays = 7 | 14 | 30

export const loadDayChoices: LoadDays[] = [7, 14, 30]

// One measure with the usual range at its hour; null when not known (no band, or not yet).
export interface LoadMeasure {
  value: number | null
  low: number | null
  high: number | null
  median: number | null
}

export interface Load {
  database: string
  days: LoadDays
  serverNow: string // server clock
  kind: "working" | "weekend" // today's kind
  daysCompared: number
  daysLeftOut: number
  now: {
    cores: LoadMeasure // cores busy over the last 15 to 30 minutes
    queries: LoadMeasure // queries an hour
    threads: LoadMeasure | null // threads waiting; null before SQL Server 2017
    since: string // the start of the two intervals read
    showSince: boolean // hour-long intervals: the line says "since {start}"
    cpuCount: number // 0 when not read
  }
  hours: LoadHour[] // 24, one per hour of today
  queryStore: { state: string; intervalMinutes: number; oldest: string; newest: string; recording: boolean }
  message?: string // in place of the load line when the load cannot be shown
  chart: boolean
  key: string // the band's line under the chart, or why there is no band
  action?: QueryAction // the query-store fix
  notes: string[]
  liveFlags: RedFlag[] // the live ACT01 and PRS01 flags
  liveError?: string // the live checks could not be read: keep the overview's
}

export interface LoadHour {
  hour: number
  cores: LoadMeasure
  queries: LoadMeasure
  threads: LoadMeasure | null
  above: boolean // cores busy above the usual range
  inProgress: boolean
  future: boolean
}

export const loadApi = {
  // refresh: the minute refresh, which reads only today when the comparison is kept. 400 for days other
  // than 7, 14 or 30; 409 when the database is not online; 502 when the server cannot be read.
  load: (serverId: number, database: string, days: LoadDays, refresh = false) =>
    request<Load>(
      `/api/servers/${serverId}/databases/${encodeURIComponent(database)}/load?days=${days}${refresh ? "&refresh=1" : ""}`,
    ),
}

// The checks the minute refresh reads again (domain.LiveChecks).
const liveChecks = ["ACT01", "PRS01"]
const shown = 3

// withLive puts the live ACT01 and PRS01 flags in place of the overview's and keeps the rest, in the
// order the backend ranked them (domain.RedFlags.WithLive does the same).
export function withLive(r: RedFlags, live: RedFlag[]): RedFlags {
  const all = [...r.flags, ...r.rest].filter((f) => !liveChecks.includes(f.checkId)).concat(live)
  all.sort((a, b) => a.rank - b.rank)
  return { flags: all.slice(0, shown), more: Math.max(0, all.length - shown), rest: all.slice(shown) }
}
