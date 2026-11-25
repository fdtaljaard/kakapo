import { useEffect } from "react"
import { useLocation } from "react-router-dom"

// Phase 5x: a tree topic that is a part of a page opens the page at "#part". The part only exists once the
// page's read has arrived, so the browser cannot scroll to it on load; this scrolls once ready is true, and
// again when the hash changes on the same page.
export function useScrollToHash(ready: boolean) {
  const { hash } = useLocation()
  useEffect(() => {
    if (!ready || !hash) return
    const el = document.getElementById(decodeURIComponent(hash.slice(1)))
    el?.scrollIntoView({ block: "start" })
  }, [ready, hash])
}
