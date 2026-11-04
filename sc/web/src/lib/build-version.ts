import { useEffect, useState } from "react"

import { api } from "@/api"

// The build stamp of the running exe (GET /api/health), read once per page load and shared, for the
// "From kakapo build ..." line of a Copy block. "" until read, or when it cannot be read.
let read: Promise<string> | null = null
let known = ""

function readVersion(): Promise<string> {
  read ??= api
    .health()
    .then((h) => (known = h.version ?? ""))
    .catch(() => {
      read = null // try again next time
      return ""
    })
  return read
}

export function useBuildVersion(): string {
  const [version, setVersion] = useState(known)
  useEffect(() => {
    let cancelled = false
    readVersion().then((v) => !cancelled && setVersion(v))
    return () => {
      cancelled = true
    }
  }, [])
  return version
}
