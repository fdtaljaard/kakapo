import { Link } from "react-router-dom"

import { Button } from "@/components/ui/button"
import { useBuildVersion } from "@/lib/build-version"

// The page column every screen after setup uses: 16 px gutters on a phone, at most 72rem wide, under
// the top bar that names the app and the build that is running.
export function PageShell({ children }: { children: React.ReactNode }) {
  return (
    <main className="min-h-svh bg-background">
      <TopBar />
      <div className="mx-auto grid max-w-6xl gap-6 px-4 py-6 sm:px-6">{children}</div>
    </main>
  )
}

// kakapo and its version on every page, so "am I on the new build" is never a guess.
function TopBar() {
  const version = useBuildVersion()
  return (
    <div className="border-b bg-muted/40">
      <div className="mx-auto flex max-w-6xl items-center justify-between gap-3 px-4 py-2 text-sm sm:px-6">
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
