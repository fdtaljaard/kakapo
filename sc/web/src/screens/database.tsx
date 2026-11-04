import { useParams } from "react-router-dom"
import { XCircle } from "lucide-react"

import { DatabaseHeader } from "@/components/database-header"
import { InstanceCard, InstanceCardSkeleton } from "@/components/instance-card"
import { PageCrumbs } from "@/components/page-crumbs"
import { NotFound, PageShell } from "@/components/page-shell"
import { RecentChanges } from "@/components/recent-changes"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useOverview } from "@/hooks/use-overview"
import { useServers } from "@/hooks/use-servers"
import { instancePath } from "@/lib/paths"

// "/instances/:id/databases/:db": the header, the six cards and Recent changes for one database.
export function DatabaseScreen() {
  const params = useParams()
  const id = Number(params.id)
  const database = params.db ?? ""
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  if (!database) return <NotFound what="There is no such database." />
  return <DatabasePage key={`${id}/${database}`} serverId={id} database={database} />
}

function DatabasePage({ serverId, database }: { serverId: number; database: string }) {
  const { load } = useServers()
  const { overview, loading, error, refresh } = useOverview(serverId, database)

  const server = load.kind === "ready" ? load.servers.find((s) => s.id === serverId) : undefined
  if (load.kind === "ready" && !server) return <NotFound what="This instance is not registered (any more)." />

  return (
    <PageShell>
      <div className="grid gap-3">
        <PageCrumbs
          crumbs={[
            { label: server?.name ?? overview?.instance.name ?? null, to: instancePath(serverId) },
            { label: database },
          ]}
        />
        <DatabaseHeader
          database={database}
          host={server?.host ?? overview?.instance.host ?? null}
          instance={overview?.instance ?? null}
          checkedAt={overview?.checkedAt ?? null}
          loading={loading}
          onRefresh={refresh}
        />
      </div>

      {error && (
        <Alert variant="destructive">
          <XCircle />
          <AlertTitle>Could not read the database</AlertTitle>
          <AlertDescription className="break-words">
            <p>{error}</p>
            {overview && <p>The cards below are from {new Date(overview.checkedAt).toLocaleString()}.</p>}
            <Button variant="outline" size="sm" className="mt-2" onClick={refresh}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      )}

      {overview ? (
        <>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {overview.cards.map((card) => (
              <InstanceCard key={card.id} card={card} serverId={serverId} database={database} checking={loading} />
            ))}
          </div>
          <RecentChanges changes={overview.recentChanges} />
        </>
      ) : (
        loading && (
          <>
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3" aria-busy>
              {Array.from({ length: 6 }, (_, i) => (
                <InstanceCardSkeleton key={i} />
              ))}
            </div>
            <Skeleton className="h-24 rounded-xl" />
          </>
        )
      )}
    </PageShell>
  )
}
