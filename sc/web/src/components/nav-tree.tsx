import { useCallback, useState } from "react"
import { Link, useLocation } from "react-router-dom"
import { ChevronRight, Loader2 } from "lucide-react"

import type { Card, CardStatus } from "@/api"
import type { NavNode } from "@/lib/nav-topics"
import { cn } from "@/lib/utils"

// Phase 5x (docs/phase5x-contract.md, "The panel"; DESIGN.md, Navigation Tree): the tree of kakapo's
// topics. A folder row has two targets, the chevron that expands it and the label that opens its page. A
// category carries its card's dot and "to do" count; a row without a card carries no dot.

const dotColour: Record<CardStatus, string> = {
  green: "bg-emerald-600 dark:bg-emerald-400",
  amber: "bg-amber-600 dark:bg-amber-400",
  red: "bg-destructive",
  grey: "bg-muted-foreground",
}

const statusWords: Record<CardStatus, string> = {
  red: "act today",
  amber: "something to do",
  green: "no problem found",
  grey: "not checked",
}

// What is expanded, remembered on this computer per arrangement. Only rows the user toggled are stored;
// the rest follow their default (open on the way to the current page).
function useOpenState(arrangement: string) {
  const key = `kakapo.nav.${arrangement}.open`
  const [open, setOpen] = useState<Record<string, boolean>>(() => {
    try {
      return JSON.parse(localStorage.getItem(key) ?? "{}") as Record<string, boolean>
    } catch {
      return {}
    }
  })
  const toggle = useCallback(
    (id: string, now: boolean) => {
      setOpen((o) => {
        const next = { ...o, [id]: now }
        try {
          localStorage.setItem(key, JSON.stringify(next))
        } catch {
          // A full or blocked storage only means the tree forgets; nothing to tell the user.
        }
        return next
      })
    },
    [key],
  )
  return { open, toggle }
}

// The address a row stands for, compared without the query string: "/…/cards/upkeep#maintenance-plan".
function target(to: string): { path: string; hash: string } {
  const [beforeHash, hash = ""] = to.split("#")
  return { path: beforeHash.split("?")[0], hash: hash ? `#${hash}` : "" }
}

function isCurrent(node: NavNode, path: string, hash: string): boolean {
  const t = target(node.to)
  return t.path === path && t.hash === hash
}

// True when the current page is this row or sits under it, so the row opens by itself.
function holdsCurrent(node: NavNode, path: string, hash: string): boolean {
  if (isCurrent(node, path, hash)) return true
  return (node.children ?? []).some((c) => holdsCurrent(c, path, hash) || target(c.to).path === path)
}

export function NavTree({
  root,
  cards,
  loading,
  arrangement = "responsibility",
  onNavigate,
}: {
  root: NavNode
  cards: Card[] | null
  loading: boolean
  arrangement?: string
  onNavigate?: () => void
}) {
  const location = useLocation()
  const { open, toggle } = useOpenState(arrangement)
  const path = location.pathname
  const hash = location.hash

  return (
    <nav aria-label="kakapo navigation" className="text-sm">
      <ul className="grid gap-px">
        <Row
          node={root}
          depth={0}
          path={path}
          hash={hash}
          open={open}
          toggle={toggle}
          cards={cards}
          loading={loading}
          onNavigate={onNavigate}
        />
      </ul>
    </nav>
  )
}

function Row({
  node,
  depth,
  path,
  hash,
  open,
  toggle,
  cards,
  loading,
  onNavigate,
}: {
  node: NavNode
  depth: number
  path: string
  hash: string
  open: Record<string, boolean>
  toggle: (id: string, now: boolean) => void
  cards: Card[] | null
  loading: boolean
  onNavigate?: () => void
}) {
  const folder = (node.children?.length ?? 0) > 0
  // The instance and the database stand open unless the user closed them; a category opens on the way
  // to the current page.
  const byDefault = depth < 2 || holdsCurrent(node, path, hash)
  const expanded = folder && (open[node.id] ?? byDefault)
  const current = isCurrent(node, path, hash)
  const card = node.card ? cards?.find((c) => c.id === node.card) : undefined
  const childrenId = `nav-${node.id}`

  return (
    <li>
      <div
        className={cn(
          "group flex h-7 items-center gap-1 rounded-md pr-2 transition-colors hover:bg-muted",
          current && "bg-muted font-medium",
        )}
        style={{ paddingLeft: `${depth * 12 + 2}px` }}
      >
        {folder ? (
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={childrenId}
            aria-label={`${expanded ? "Collapse" : "Expand"} ${node.label}`}
            onClick={() => toggle(node.id, !expanded)}
            className="grid size-5 shrink-0 place-items-center rounded-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            <ChevronRight className={cn("size-3.5 transition-transform duration-150", expanded && "rotate-90")} />
          </button>
        ) : (
          <span className="size-5 shrink-0" aria-hidden />
        )}
        <Link
          to={node.to}
          aria-current={current ? "page" : undefined}
          onClick={onNavigate}
          title={node.label}
          className="min-w-0 flex-1 truncate rounded-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          {node.label}
          {card && <span className="sr-only">, {statusWords[card.status]}{card.toDo > 0 ? `, ${card.toDo} to do` : ""}</span>}
        </Link>
        {node.card && <CardMark card={card} loading={loading} />}
      </div>
      {expanded && (
        <ul id={childrenId} className="grid gap-px">
          {node.children!.map((c) => (
            <Row
              key={c.id}
              node={c}
              depth={depth + 1}
              path={path}
              hash={hash}
              open={open}
              toggle={toggle}
              cards={cards}
              loading={loading}
              onNavigate={onNavigate}
            />
          ))}
        </ul>
      )}
    </li>
  )
}

// The "to do" count in Quiet Ink, then the card's dot; a spinner while the card is read; nothing at all
// before there is a card to colour it (grey would claim "not checked").
function CardMark({ card, loading }: { card?: Card; loading: boolean }) {
  if (loading && !card) return <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" aria-hidden />
  if (!card) return null
  return (
    <span className="flex shrink-0 items-center gap-1.5" aria-hidden>
      {card.toDo > 0 && <span className="text-xs tabular-nums text-muted-foreground">{card.toDo}</span>}
      {loading ? (
        <Loader2 className="size-3 animate-spin text-muted-foreground" />
      ) : (
        <span className={cn("size-2 rounded-full", dotColour[card.status])} />
      )}
    </span>
  )
}
