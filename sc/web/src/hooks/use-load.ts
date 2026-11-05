import { useCallback, useEffect, useRef, useState } from "react"

import { loadApi, type Load, type LoadDays } from "@/api-load"

// Every minute while the page is open and the tab is visible (docs/phase5i-contract.md, "The refresh").
const REFRESH_MS = 60_000

interface LoadState {
  key: string // the days and attempt of the last full read answered
  load: Load | null
  error: string | null // the read failed and there is no strip to keep
  stale: boolean // the last refresh failed: the strip kept is from load.serverNow
}

// Reads GET .../load?days= on mount, on a change of days and on reload(); then ?refresh=1 every minute
// while document.visibilityState is "visible", none while hidden, and one at once when the tab comes
// back. A failed refresh keeps the last strip and marks it stale. Mount the caller with a key of server
// and database.
export function useLoad(serverId: number, database: string, days: LoadDays) {
  const [attempt, setAttempt] = useState(0)
  const key = `${days}/${attempt}`
  const [state, setState] = useState<LoadState>({ key: "", load: null, error: null, stale: false })
  // The days of the current read, so a refresh answered after a change of days is dropped.
  const current = useRef(days)

  useEffect(() => {
    current.current = days
    let cancelled = false
    loadApi
      .load(serverId, database, days)
      .then((load) => !cancelled && setState({ key, load, error: null, stale: false }))
      .catch(
        (e: Error) => !cancelled && setState((s) => ({ key, load: s.load, error: s.load ? null : e.message, stale: !!s.load })),
      )
    return () => {
      cancelled = true
    }
  }, [serverId, database, days, key])

  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined
    const tick = () => {
      if (document.visibilityState !== "visible") return
      loadApi
        .load(serverId, database, days, true)
        .then((load) => current.current === days && setState((s) => ({ ...s, load, error: null, stale: false })))
        .catch(() => current.current === days && setState((s) => ({ ...s, stale: !!s.load })))
    }
    const start = () => {
      if (timer) clearInterval(timer)
      timer = setInterval(tick, REFRESH_MS)
    }
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        tick()
        start()
      } else if (timer) {
        clearInterval(timer)
        timer = undefined
      }
    }
    if (document.visibilityState === "visible") start()
    document.addEventListener("visibilitychange", onVisibility)
    return () => {
      if (timer) clearInterval(timer)
      document.removeEventListener("visibilitychange", onVisibility)
    }
  }, [serverId, database, days])

  const reload = useCallback(() => setAttempt((n) => n + 1), [])
  return { load: state.load, error: state.error, stale: state.stale, loading: state.key !== key, reload }
}
