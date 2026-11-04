import { useEffect, useState } from "react"
import { useNavigate, useParams } from "react-router-dom"
import { Loader2, XCircle } from "lucide-react"

import { api, ApiError, type Server } from "@/api"
import { NotFound } from "@/components/page-shell"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { forgetOverviews } from "@/hooks/use-overview"
import { instancePath } from "@/lib/paths"
import { SetupScreen } from "@/screens/setup"

type Load =
  | { kind: "loading" }
  | { kind: "ready"; server: Server }
  | { kind: "missing" }
  | { kind: "error"; message: string }

// "/instances/:id/edit": the setup form pre-filled from GET /api/servers/{id}; Save is a PUT, then
// the instance page. Every page reads the servers when it mounts, so the new name and database show
// on the instance page, the Home tile and the breadcrumb without anything else to refresh.
export function EditInstanceScreen() {
  const id = Number(useParams().id)
  if (!Number.isInteger(id) || id <= 0) return <NotFound what="There is no such instance." />
  return <EditInstance key={id} serverId={id} />
}

function EditInstance({ serverId }: { serverId: number }) {
  const navigate = useNavigate()
  const [load, setLoad] = useState<Load>({ kind: "loading" })
  const [attempt, setAttempt] = useState(0)

  useEffect(() => {
    let cancelled = false
    api
      .getServer(serverId)
      .then((server) => !cancelled && setLoad({ kind: "ready", server }))
      .catch((e: Error) => {
        if (cancelled) return
        setLoad(e instanceof ApiError && e.status === 404 ? { kind: "missing" } : { kind: "error", message: e.message })
      })
    return () => {
      cancelled = true
    }
  }, [serverId, attempt])

  // Replace the form in the history, so Back from the instance page does not reopen it.
  const toInstance = () => navigate(instancePath(serverId), { replace: true })

  if (load.kind === "missing") return <NotFound what="This instance is not registered (any more)." />
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
          <AlertTitle>Could not read the registration</AlertTitle>
          <AlertDescription className="break-words">
            <p>{load.message}</p>
            <div className="mt-2 flex gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setLoad({ kind: "loading" })
                  setAttempt((n) => n + 1)
                }}
              >
                Try again
              </Button>
              <Button variant="ghost" size="sm" onClick={toInstance}>
                Back
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      </main>
    )
  }

  return (
    <SetupScreen
      server={load.server}
      onSaved={() => {
        // The instance block of a cached overview carries the old name and host.
        forgetOverviews(serverId)
        toInstance()
      }}
      onCancel={toInstance}
    />
  )
}
