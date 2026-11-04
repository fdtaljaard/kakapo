import { useEffect, useState } from "react"

import { api } from "@/api"

const minimumMs = 1400 // long enough to read, short enough not to annoy
const fadeMs = 450

// The welcome screen shown while the app comes up: until the backend has answered its first
// call and at least minimumMs have passed, then it fades out and unmounts.
export function Splash() {
  const [phase, setPhase] = useState<"shown" | "fading" | "gone">("shown")

  useEffect(() => {
    let cancelled = false
    const ready = api.health().catch(() => undefined)
    const waited = new Promise<void>((resolve) => setTimeout(resolve, minimumMs))
    Promise.all([ready, waited]).then(() => {
      if (cancelled) return
      setPhase("fading")
      setTimeout(() => !cancelled && setPhase("gone"), fadeMs)
    })
    return () => {
      cancelled = true
    }
  }, [])

  if (phase === "gone") return null
  return (
    <div
      aria-hidden
      className="fixed inset-0 z-50 flex items-center justify-center bg-white transition-opacity"
      style={{ opacity: phase === "fading" ? 0 : 1, transitionDuration: `${fadeMs}ms` }}
    >
      <img src="/welcome.png" alt="" className="w-[min(88vw,520px)] select-none" draggable={false} />
    </div>
  )
}
