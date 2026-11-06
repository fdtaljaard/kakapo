import { Link, useNavigate } from "react-router-dom"
import { ChevronRight, Database, Loader2, Plus, XCircle } from "lucide-react"

import { type Server } from "@/api"
import { PageShell } from "@/components/page-shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useServers } from "@/hooks/use-servers"
import { formatWhen } from "@/lib/format"
import { instancePath, type FromHome } from "@/lib/paths"
import { SetupScreen } from "@/screens/setup"

// Set on the way from Home, so the picker goes straight into the remembered database.
const fromHome: FromHome = { fromHome: true }

// "/": one tile per registered instance, or the setup form on first run.
export function HomeScreen() {
  const { load, reload } = useServers()
  const navigate = useNavigate()

  if (load.kind === "loading") {
    return (
      <main className="min-h-svh flex items-center justify-center text-muted-foreground">
        <Loader2 className="size-5 animate-spin" />
      </main>
    )
  }
  if (load.kind === "error") {
    return (
      <main className="min-h-svh flex items-center justify-center p-4">
        <Alert variant="destructive" className="max-w-lg">
          <XCircle />
          <AlertTitle>Cannot reach the kakapo backend</AlertTitle>
          <AlertDescription className="break-words">
            <p>{load.message}</p>
            <Button variant="outline" size="sm" className="mt-2" onClick={reload}>
              Try again
            </Button>
          </AlertDescription>
        </Alert>
      </main>
    )
  }
  if (load.servers.length === 0) {
    // First run: nothing in the database yet.
    return <SetupScreen onSaved={(server) => navigate(instancePath(server.id), { state: fromHome })} />
  }

  return (
    <PageShell>
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <Database className="size-5" />
          <h1 className="text-lg font-semibold">Instances</h1>
          <span className="text-sm text-muted-foreground">{load.servers.length}</span>
        </div>
        <Button asChild>
          <Link to="/servers/new">
            <Plus />
            Add server
          </Link>
        </Button>
      </header>
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        {load.servers.map((s) => (
          <ServerTile key={s.id} server={s} />
        ))}
      </div>
    </PageShell>
  )
}

function ServerTile({ server }: { server: Server }) {
  return (
    <Link
      to={instancePath(server.id)}
      state={fromHome}
      className="group rounded-xl outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <Card className="h-full transition-colors group-hover:bg-muted/40">
        <CardHeader>
          <CardTitle className="flex items-center justify-between gap-2">
            <span className="truncate">{server.name}</span>
            <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
          </CardTitle>
          <CardDescription className="grid gap-0.5">
            <span className="font-mono text-xs break-all">{server.host}</span>
            {server.database && <span className="break-all">{server.database}</span>}
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-0.5 text-muted-foreground">
          {server.lastEdition || server.lastVersion ? (
            <div>
              {[server.lastEdition, server.lastVersion && `build ${server.lastVersion}`].filter(Boolean).join(", ")}
            </div>
          ) : null}
          <div className="text-xs">
            {server.lastTestedAt ? `checked ${formatWhen(server.lastTestedAt)}` : "never checked"}
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
