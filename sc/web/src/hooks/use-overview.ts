import { useCallback, useEffect, useState } from "react"

import { api, type Overview } from "@/api"

// The last overview read per server and database, so going from the database page to a card page
// and back shows something at once while the fresh one loads.
const cache = new Map<string, Overview>()
const cacheKey = (serverId: number, database: string) => `${serverId}/${database}`

// Drops the cached overviews of one server, after its registration was edited (name, host, ...),
// so no page shows the old instance block while the new one loads.
export function forgetOverviews(serverId: number) {
  for (const key of cache.keys()) if (key.startsWith(`${serverId}/`)) cache.delete(key)
}

interface OverviewState {
  overview: Overview | null
  loading: boolean
  error: string | null
}

// Reads GET /api/servers/{id}/databases/{db}/overview on mount and on refresh(). With reuse, a cached
// overview is used as is and nothing is read until refresh(). With cacheOnly, only the cached overview
// is returned and nothing is read until refresh() (a card page that reads its own detail). Mount the
// caller with a key of server and database.
export function useOverview(serverId: number, database: string, { reuse = false, cacheOnly = false } = {}) {
  const key = cacheKey(serverId, database)
  const [skipFirst] = useState(() => cacheOnly || (reuse && cache.has(key)))
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<OverviewState>(() => ({
    overview: cache.get(key) ?? null,
    loading: !skipFirst,
    error: null,
  }))

  useEffect(() => {
    if (skipFirst && attempt === 0) return
    let cancelled = false
    api
      .overview(serverId, database)
      .then((overview) => {
        cache.set(cacheKey(serverId, database), overview)
        if (!cancelled) setState({ overview, loading: false, error: null })
      })
      .catch((e: Error) => !cancelled && setState((s) => ({ ...s, loading: false, error: e.message })))
    return () => {
      cancelled = true
    }
  }, [serverId, database, attempt, skipFirst])

  const refresh = useCallback(() => {
    setState((s) => ({ ...s, loading: true, error: null }))
    setAttempt((n) => n + 1)
  }, [])

  return { ...state, refresh }
}
