import { Loader2, RotateCw } from "lucide-react"

import type { InstanceInfo } from "@/api"
import { ExpressNotice, isExpress } from "@/components/express-notice"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { formatAgo, formatWhen, shortEdition } from "@/lib/format"

// The database page header: the database name first, then the instance it lives on. The name comes
// from the URL and the host from the registered server, so they show at once; the rest comes from
// the overview and shows a skeleton until the first one is read.
export function DatabaseHeader({
  database,
  host,
  instance,
  checkedAt,
  loading,
  onRefresh,
}: {
  database: string
  host: string | null
  instance: InstanceInfo | null
  checkedAt: string | null
  loading: boolean
  onRefresh: () => void
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="grid gap-1 min-w-0">
        <h1 className="text-xl font-semibold break-all">{database}</h1>
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-sm text-muted-foreground">
          {host && <code className="font-mono text-xs break-all">{host}</code>}
          {instance ? (
            <>
              <Dot />
              <span>
                {instance.productName} {shortEdition(instance.edition)} {instance.productLevel}, build{" "}
                {instance.productVersion}
              </span>
              <Dot />
              {/* Waits and counters reset at a restart, so say what the figures cover. */}
              <span title={new Date(instance.startedAt).toLocaleString()}>
                figures since restart, {formatAgo(instance.startedAt)}
              </span>
              {checkedAt && (
                <>
                  <Dot />
                  <span>checked {formatWhen(checkedAt)}</span>
                </>
              )}
            </>
          ) : (
            loading && <Skeleton className="h-4 w-64 max-w-full" />
          )}
        </div>
        {isExpress(instance?.edition) && <ExpressNotice version={instance?.productVersion} />}
      </div>
      <Button variant="outline" onClick={onRefresh} disabled={loading}>
        {loading ? <Loader2 className="animate-spin" /> : <RotateCw />}
        Refresh
      </Button>
    </header>
  )
}

function Dot() {
  return <span aria-hidden>·</span>
}
