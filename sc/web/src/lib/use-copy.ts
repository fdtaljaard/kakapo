import { useCallback, useEffect, useRef, useState } from "react"

export type CopyStatus = "idle" | "copied" | "refused"

// Copy text to the clipboard. status is "copied" (or "refused" when the browser blocks the clipboard) for
// 2.5 s, then "idle" again. fallback keeps the refused text until dismiss() or a copy that works, so the
// caller can show it selected for Ctrl+C. copy() resolves to whether the clipboard took the text.
export function useCopy(ms = 2500) {
  const [status, setStatus] = useState<CopyStatus>("idle")
  const [fallback, setFallback] = useState<string | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => () => clearTimeout(timer.current), [])

  const copy = useCallback(
    async (text: string) => {
      clearTimeout(timer.current)
      let ok = true
      try {
        await navigator.clipboard.writeText(text)
        setFallback(null)
      } catch {
        ok = false
        setFallback(text)
      }
      setStatus(ok ? "copied" : "refused")
      timer.current = setTimeout(() => setStatus("idle"), ms)
      return ok
    },
    [ms],
  )

  const dismiss = useCallback(() => setFallback(null), [])

  return { status, fallback, copy, dismiss }
}
