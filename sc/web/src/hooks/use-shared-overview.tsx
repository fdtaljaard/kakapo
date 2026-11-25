import { createContext, useCallback, useContext, useMemo, useState } from "react"

import type { Card, Overview, PlanSummary } from "@/api"
import { rememberCard, rememberPlan, useOverview } from "@/hooks/use-overview"

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
  // The maintenance plan's colour and few words, replaced by a newer one from the Upkeep card's detail.
  plan: PlanSummary | null
  loading: boolean
  error: string | null
  refresh: () => void
  updateCard: (card: Card) => void
  updatePlan: (plan: PlanSummary) => void
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
  const [newer, setNewer] = useState<{ base: Overview | null; cards: Record<string, Card>; plan?: PlanSummary }>({
    base: null,
    cards: {},
  })

  const updateCard = useCallback(
    (card: Card) => {
      rememberCard(serverId, database, card)
      setNewer((n) => {
        const same = n.base === ov.overview
        return { base: ov.overview, cards: { ...(same ? n.cards : {}), [card.id]: card }, plan: same ? n.plan : undefined }
      })
    },
    [ov.overview, serverId, database],
  )

  const updatePlan = useCallback(
    (plan: PlanSummary) => {
      rememberPlan(serverId, database, plan)
      setNewer((n) => ({ base: ov.overview, cards: n.base === ov.overview ? n.cards : {}, plan }))
    },
    [ov.overview, serverId, database],
  )

  const cards = useMemo(() => {
    if (!ov.overview) return null
    const patch = newer.base === ov.overview ? newer.cards : {}
    return ov.overview.cards.map((c) => patch[c.id] ?? c)
  }, [ov.overview, newer])

  const plan = (newer.base === ov.overview ? newer.plan : undefined) ?? ov.overview?.maintenancePlan ?? null

  const value = useMemo<SharedOverview>(
    () => ({
      serverId,
      database,
      overview: ov.overview,
      cards,
      plan,
      loading: ov.loading,
      error: ov.error,
      refresh: ov.refresh,
      updateCard,
      updatePlan,
    }),
    [serverId, database, ov.overview, cards, plan, ov.loading, ov.error, ov.refresh, updateCard, updatePlan],
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
