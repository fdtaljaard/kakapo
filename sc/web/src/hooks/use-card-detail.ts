import { useCallback, useEffect, useState } from "react"

import { ApiError, api, type CardDetail } from "@/api"

interface CardDetailState {
  detail: CardDetail | null
  loading: boolean
  error: string | null
  notFound: boolean
}

// Reads GET /api/servers/{id}/databases/{db}/cards/{card} on mount and on refresh(). The detail is
// read live, so it is never cached; the last one stays on screen while the next one loads. Mount the
// caller with a key of server, database and card.
export function useCardDetail(serverId: number, database: string, card: string) {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<CardDetailState>({ detail: null, loading: true, error: null, notFound: false })

  useEffect(() => {
    let cancelled = false
    api
      .cardDetail(serverId, database, card)
      .then((detail) => !cancelled && setState({ detail, loading: false, error: null, notFound: false }))
      .catch((e: Error) => {
        if (cancelled) return
        const notFound = e instanceof ApiError && e.status === 404
        setState((s) => ({ ...s, loading: false, error: e.message, notFound }))
      })
    return () => {
      cancelled = true
    }
  }, [serverId, database, card, attempt])

  const refresh = useCallback(() => {
    setState((s) => ({ ...s, loading: true, error: null }))
    setAttempt((n) => n + 1)
  }, [])

  return { ...state, refresh }
}
