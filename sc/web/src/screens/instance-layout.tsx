import { useEffect, useState } from "react"
import { matchPath, Outlet, useLocation } from "react-router-dom"
import { ChevronDown, ListTree } from "lucide-react"

import type { Server } from "@/api"
import { NavTree } from "@/components/nav-tree"
import { InLayoutContext, TopBar } from "@/components/page-shell"
import { Button } from "@/components/ui/button"
import { SharedOverviewProvider, useSharedOverview } from "@/hooks/use-shared-overview"
import { useServers } from "@/hooks/use-servers"
import { byResponsibility, navCards, type NavCardId } from "@/lib/nav-topics"
import { cn } from "@/lib/utils"

// Phase 5x (docs/phase5x-contract.md): every page under "/instances/:id" sits in this layout, the top bar
// across the window, the navigation panel on the left and the page's column beside it. The layout stays
// mounted while the user moves between those pages, so the tree keeps what is expanded, and it owns the one
// overview read of the database being worked on.
export function InstanceLayout() {
  const location = useLocation()
  const id = Number(matchPath("/instances/:id/*", location.pathname)?.params.id)
  // matchPath leaves the name as it is in the address; the screens' useParams decodes it, so decode here too.
  const fromAddress = decodeName(matchPath("/instances/:id/databases/:db/*", location.pathname)?.params.db)
  const { load, reload } = useServers()
  // The registration's name and remembered database. Read again after the edit form, which may rename it;
  // the last one read stays in the tree meanwhile, so the tree does not blink.
  const [server, setServer] = useState<Server | undefined>(undefined)
  const read = load.kind === "ready" ? load.servers.find((s) => s.id === id) : server
  if (read !== server) setServer(read)
  const [previous, setPrevious] = useState(location.pathname)
  if (previous !== location.pathname) {
    setPrevious(location.pathname)
    if (previous.endsWith("/edit")) reload()
  }
  // The tree stays where it is while the page scrolls, so a click in it opens the next page at its top,
  // unless it names a part of the page ("#maintenance-plan"), which the page scrolls to itself.
  useEffect(() => {
    if (!location.hash) window.scrollTo(0, 0)
  }, [location.pathname, location.hash])

  // The database the tree shows: the one in the address, else the last one opened in this window, else
  // the one the registration remembers.
  const [lastOpened, setLastOpened] = useState<string | null>(null)
  if (fromAddress && fromAddress !== lastOpened) setLastOpened(fromAddress)
  const database = fromAddress ?? lastOpened ?? server?.database ?? null
  const live = fromAddress !== null

  const page = (
    <InLayoutContext.Provider value={true}>
      <Outlet />
    </InLayoutContext.Provider>
  )

  return (
    <main className="min-h-svh bg-background">
      <TopBar wide />
      <div className="md:flex">
        {database && Number.isInteger(id) && id > 0 ? (
          <SharedOverviewProvider key={`${id}/${database}/${live}`} serverId={id} database={database} live={live}>
            <Panel server={server} database={database} />
            <div className="min-w-0 flex-1">
              <NarrowNav server={server} database={database} />
              {page}
            </div>
          </SharedOverviewProvider>
        ) : (
          <>
            <Panel server={server} database={null} />
            <div className="min-w-0 flex-1">
              <NarrowNav server={server} database={null} />
              {page}
            </div>
          </>
        )}
      </div>
    </main>
  )
}

interface TreeProps {
  server?: { id: number; name: string }
  database: string | null
}

function Tree({ server, database, onNavigate }: TreeProps & { onNavigate?: () => void }) {
  const shared = useSharedOverview()
  if (!server) return null
  const cards = database ? (shared?.cards ?? null) : null
  const title = (id: NavCardId) => cards?.find((c) => c.id === id)?.title ?? navCards.find((c) => c.id === id)!.title
  return (
    <NavTree
      root={byResponsibility(server, database, title)}
      cards={cards}
      plan={database ? (shared?.plan ?? null) : null}
      loading={!!shared?.loading}
      onNavigate={onNavigate}
    />
  )
}

// From the medium breakpoint up: the panel on the left, the full height under the top bar, scrolling on
// its own. 18rem, so a category's name, its count and its dot fit beside a thin scrollbar.
function Panel(props: TreeProps) {
  return (
    <aside className="sticky top-0 hidden max-h-svh w-72 shrink-0 self-start overflow-x-hidden overflow-y-auto border-r px-2 py-4 [scrollbar-width:thin] md:block">
      <Tree {...props} />
    </aside>
  )
}

// Below the medium breakpoint the panel folds into "Navigate" above the page; a click in the tree closes it.
function NarrowNav(props: TreeProps) {
  const [open, setOpen] = useState(false)
  if (!props.server) return null
  return (
    <div className="border-b px-4 py-2 md:hidden">
      <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen((o) => !o)} className="-ml-2">
        <ListTree />
        Navigate
        <ChevronDown className={cn("transition-transform duration-150", open && "rotate-180")} />
      </Button>
      {open && (
        <div className="pt-2 pb-1">
          <Tree {...props} onNavigate={() => setOpen(false)} />
        </div>
      )}
    </div>
  )
}

function decodeName(raw: string | undefined): string | null {
  if (!raw) return null
  try {
    return decodeURIComponent(raw)
  } catch {
    return raw
  }
}
