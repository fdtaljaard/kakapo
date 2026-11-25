import { createContext, useCallback, useContext, useMemo, useState } from "react"

import type { Card, Overview } from "@/api"
import { rememberCard, useOverview } from "@/hooks/use-overview"

// Phase 5x (docs/phase5x-contract.md, "One reading of the database"): the instance's layout owns the one
// overview read of the database being worked on, and every page under it uses that read. A card page hands
// back the card object its detail carries, so the tree and the database page follow a card's new colour
// after an Apply without a second overview read.
export interface SharedOverview {
  serverId: number
  database: string
  // The overview as read. A page that compares against the read itself (Setup's Query Store row) uses this.
  overview: Overview | null
  // The six cards, each replaced by a newer copy from a card's detail where there is one.
  cards: Card[] | null
  loading: boolean
  error: string | null
  refresh: () => void
  updateCard: (card: Card) => void
}

const SharedOverviewContext = createContext<SharedOverview | null>(null)

// live: read the overview on mount (the database is in the address). Otherwise only the overview cached
// in this window is shown and nothing is read until refresh(). Mount with a key of server, database and live.
export function SharedOverviewProvider({
  serverId,
  database,
  live,
  children,
}: {
  serverId: number
  database: string
  live: boolean
  children: React.ReactNode
}) {
  const ov = useOverview(serverId, database, live ? {} : { cacheOnly: true })
  // Newer cards from details, kept only for the overview they were taken against: a fresh read replaces them.
  const [newer, setNewer] = useState<{ base: Overview | null; cards: Record<string, Card> }>({ base: null, cards: {} })

  const updateCard = useCallback(
    (card: Card) => {
      rememberCard(serverId, database, card)
      setNewer((n) => {
        const cards = n.base === ov.overview ? n.cards : {}
        return { base: ov.overview, cards: { ...cards, [card.id]: card } }
      })
    },
    [ov.overview, serverId, database],
  )

  const cards = useMemo(() => {
    if (!ov.overview) return null
    const patch = newer.base === ov.overview ? newer.cards : {}
    return ov.overview.cards.map((c) => patch[c.id] ?? c)
  }, [ov.overview, newer])

  const value = useMemo<SharedOverview>(
    () => ({
      serverId,
      database,
      overview: ov.overview,
      cards,
      loading: ov.loading,
      error: ov.error,
      refresh: ov.refresh,
      updateCard,
    }),
    [serverId, database, ov.overview, cards, ov.loading, ov.error, ov.refresh, updateCard],
  )

  return <SharedOverviewContext.Provider value={value}>{children}</SharedOverviewContext.Provider>
}

// The shared read, when the page sits under an instance's layout for this database; null otherwise.
export function useSharedOverview(serverId?: number, database?: string): SharedOverview | null {
  const shared = useContext(SharedOverviewContext)
  if (!shared) return null
  if (serverId !== undefined && shared.serverId !== serverId) return null
  if (database !== undefined && shared.database !== database) return null
  return shared
}
