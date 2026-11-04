import { Link } from "react-router-dom"

import { Button } from "@/components/ui/button"

// The page column every screen after setup uses: 16 px gutters on a phone, at most 72rem wide.
export function PageShell({ children }: { children: React.ReactNode }) {
  return (
    <main className="min-h-svh bg-background">
      <div className="mx-auto grid max-w-6xl gap-6 px-4 py-6 sm:px-6">{children}</div>
    </main>
  )
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
