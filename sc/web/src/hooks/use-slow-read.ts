import { useCallback, useEffect, useRef, useState } from "react"

// A slow read the user started: elapsed seconds while it runs, and Stop, which aborts the request (the
// server cancels the statement). run() resolves to the result, or null when it was stopped.
export function useSlowRead() {
  const [started, setStarted] = useState<number | null>(null)
  const [elapsed, setElapsed] = useState(0)
  const abort = useRef<AbortController | null>(null)

  useEffect(() => {
    if (started === null) return
    const t = setInterval(() => setElapsed(Math.round((Date.now() - started) / 1000)), 1000)
    return () => clearInterval(t)
  }, [started])
  useEffect(() => () => abort.current?.abort(), [])

  const run = useCallback(async <T,>(read: (signal: AbortSignal) => Promise<T>): Promise<T | null> => {
    const ctrl = new AbortController()
    abort.current = ctrl
    setElapsed(0)
    setStarted(Date.now())
    try {
      return await read(ctrl.signal)
    } catch (e) {
      if (ctrl.signal.aborted) return null
      throw e
    } finally {
      abort.current = null
      setStarted(null)
    }
  }, [])

  const stop = useCallback(() => abort.current?.abort(), [])
  return { reading: started !== null, elapsed, run, stop }
}
