import { createContext, useContext } from "react"
import { Link } from "react-router-dom"

import { Button } from "@/components/ui/button"
import { useBuildVersion } from "@/lib/build-version"
import { cn } from "@/lib/utils"

// The page column every screen after setup uses: 16 px gutters on a phone, at most 72rem wide, under
// the top bar that names the app and the build that is running.
// Under an instance's layout (phase 5x) the top bar and the navigation panel are the layout's, so a page
// draws only its column.
export const InLayoutContext = createContext(false)

export function PageShell({ children }: { children: React.ReactNode }) {
  const inLayout = useContext(InLayoutContext)
  const column = <div className="mx-auto grid max-w-6xl gap-6 px-4 py-6 sm:px-6">{children}</div>
  if (inLayout) return column
  return (
    <main className="min-h-svh bg-background">
      <TopBar />
      {column}
    </main>
  )
}

// kakapo and its version on every page, so "am I on the new build" is never a guess. In the instance's
// layout it runs the full width, so kakapo sits above the navigation panel.
export function TopBar({ wide = false }: { wide?: boolean }) {
  const version = useBuildVersion()
  return (
    <div className="border-b bg-muted/40">
      <div
        className={cn(
          "flex items-center justify-between gap-3 py-2 text-sm",
          wide ? "px-4" : "mx-auto max-w-6xl px-4 sm:px-6",
        )}
      >
        <Link to="/" className="font-semibold tracking-tight hover:underline">
          kakapo
        </Link>
        {version && (
          <span title={`Build ${version}`}>
            <span className="text-muted-foreground">Version </span>
            <span className="font-mono font-medium">{versionLabel(version)}</span>
          </span>
        )}
      </div>
    </div>
  )
}

// versionLabel turns the build stamp "2026-11-06.1129.7cf4a4a" (build.ps1: date, time, commit) into
// "2026-11-06 11:29 · 7cf4a4a"; any other stamp (e.g. "dev") is shown as it is.
function versionLabel(stamp: string): string {
  const m = /^(\d{4}-\d{2}-\d{2})\.(\d{2})(\d{2})(?:\.([0-9a-f]+))?$/i.exec(stamp)
  if (!m) return stamp
  return `${m[1]} ${m[2]}:${m[3]}${m[4] ? ` · ${m[4]}` : ""}`
}

export function NotFound({ what }: { what: string }) {
  return (
    <PageShell>
      <div className="grid gap-3 justify-items-start">
        <p className="text-muted-foreground">{what}</p>
        <Button variant="outline" asChild>
          <Link to="/">All instances</Link>
        </Button>
      </div>
    </PageShell>
  )
}
