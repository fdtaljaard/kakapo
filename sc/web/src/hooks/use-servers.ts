import { useCallback, useEffect, useState } from "react"

import { api, type Server } from "@/api"

export type ServersLoad =
  | { kind: "loading" }
  | { kind: "ready"; servers: Server[] }
  | { kind: "error"; message: string }

// The registered servers from GET /api/servers, read on mount and on reload().
export function useServers() {
  const [load, setLoad] = useState<ServersLoad>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let cancelled = false
    api
      .listServers()
      .then((servers) => !cancelled && setLoad({ kind: "ready", servers }))
      .catch((e: Error) => !cancelled && setLoad({ kind: "error", message: e.message }))
    return () => {
      cancelled = true
    }
  }, [attempt])

  const reload = useCallback(() => {
    setLoad({ kind: "loading" })
    setAttempt((n) => n + 1)
  }, [])

  return { load, reload }
}
