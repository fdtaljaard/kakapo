import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError } from "@/api"
import { blockingApi, type BlockingNode, type BlockingTree } from "@/api-blocking"

// The watch (docs/phase4c-contract.md, part 2): the route polled every 3 seconds for 30 seconds,
// client-side, with the chains seen kept, so a block that lasts a few seconds is still on screen.
export const WATCH_SECONDS = 30
export const WATCH_EVERY_MS = 3000

// A head blocker seen in any read since the page opened: still blocking, or cleared at the first read
// that no longer had it.
export interface SeenChain {
  key: string
  head: BlockingNode // as last seen
  firstSeen: Date
  lastSeen: Date
  clearedAt: Date | null
  mostBehind: number
  longestWaitSec: number
}

// A session id is reused once it disconnects: the head is the same one while its id, login, host and
// program are.
function chainKey(h: BlockingNode): string {
  return [h.session, h.login, h.host, h.program].join("|")
}

// mergeSeen folds one read into the chains seen. Pure, so the watch's bookkeeping is easy to follow.
export function mergeSeen(seen: SeenChain[], tree: BlockingTree, at: Date): SeenChain[] {
  const now = new Map(tree.heads.map((h) => [chainKey(h), h]))
  const next = seen.map((c) => {
    const h = now.get(c.key)
    if (h) {
      now.delete(c.key)
      return {
        ...c,
        head: h,
        lastSeen: at,
        clearedAt: null,
        mostBehind: Math.max(c.mostBehind, h.behind),
        longestWaitSec: Math.max(c.longestWaitSec, h.longestWaitSec),
      }
    }
    return c.clearedAt ? c : { ...c, clearedAt: at }
  })
  for (const [key, h] of now) {
    next.push({ key, head: h, firstSeen: at, lastSeen: at, clearedAt: null, mostBehind: h.behind, longestWaitSec: h.longestWaitSec })
  }
  return next
}

interface WatchState {
  tree: BlockingTree | null
  loading: boolean
  error: string | null
  notFound: boolean
  seen: SeenChain[]
  // Seconds left while watching; null when not watching.
  secondsLeft: number | null
  watched: boolean // a watch has run since the page opened
}

// Reads the chain on mount and on refresh(); watch() polls it for 30 seconds. A read already in flight
// is not doubled. Mount the caller with a key of server and database.
export function useBlockingWatch(serverId: number, database: string) {
  const [state, setState] = useState<WatchState>({
    tree: null,
    loading: true,
    error: null,
    notFound: false,
    seen: [],
    secondsLeft: null,
    watched: false,
  })
  const inFlight = useRef(false)
  const alive = useRef(true)
  const watchEnds = useRef<number | null>(null)

  // One read: its answer folded into the state. Every call goes through here, the first one included.
  const fetchTree = useCallback(() => {
    inFlight.current = true
    return blockingApi
      .read(serverId, database)
      .then((tree) => {
        if (!alive.current) return
        const at = new Date()
        setState((s) => ({ ...s, tree, loading: false, error: null, notFound: false, seen: mergeSeen(s.seen, tree, at) }))
      })
      .catch((e: Error) => {
        if (!alive.current) return
        const notFound = e instanceof ApiError && e.status === 404
        setState((s) => ({ ...s, loading: false, error: e.message, notFound }))
      })
      .finally(() => {
        inFlight.current = false
      })
  }, [serverId, database])

  // A read after the first: says it is running; skipped while one is in flight.
  const read = useCallback(() => {
    if (inFlight.current) return Promise.resolve()
    setState((s) => ({ ...s, loading: true }))
    return fetchTree()
  }, [fetchTree])

  // The first read; the state starts with loading true.
  useEffect(() => {
    alive.current = true
    void fetchTree()
    return () => {
      alive.current = false
    }
  }, [fetchTree])

  // The watch: one tick a second for the countdown, a read every 3 seconds, a last read at the end.
  const watching = state.secondsLeft !== null
  useEffect(() => {
    if (!watching) return
    let ticks = 0
    const timer = setInterval(() => {
      ticks++
      const ends = watchEnds.current ?? 0
      const left = Math.max(0, Math.ceil((ends - Date.now()) / 1000))
      if (ticks % (WATCH_EVERY_MS / 1000) === 0 || left === 0) void read()
      if (left === 0) watchEnds.current = null
      setState((s) => ({ ...s, secondsLeft: left === 0 ? null : left }))
    }, 1000)
    return () => clearInterval(timer)
  }, [watching, read])

  const watch = useCallback(() => {
    watchEnds.current = Date.now() + WATCH_SECONDS * 1000
    setState((s) => ({ ...s, secondsLeft: WATCH_SECONDS, watched: true }))
    void read()
  }, [read])

  const stop = useCallback(() => {
    watchEnds.current = null
    setState((s) => ({ ...s, secondsLeft: null }))
  }, [])

  return { ...state, refresh: read, watch, stop }
}
