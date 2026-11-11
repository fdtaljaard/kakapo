import { Link } from "react-router-dom"

import type { Card, CardStatus } from "@/api"
import { cardPath } from "@/lib/paths"

// The database page's own answer, under its title (DESIGN.md, The Sentence-First Rule): how many things
// there are to do across the six cards, whether any is to act on today, and which card to open first,
// with that card's verdict. "The Field Report" opens with this sentence; the cards are its sections.

// Act today first, then to do; a card that could not be read, then the healthy ones, keep their place.
const statusOrder: Record<CardStatus, number> = { red: 0, amber: 1, green: 2, grey: 3 }

// The cards in the order the page shows them: the ones that need something first, the usual order kept
// within each colour.
export function cardsByStatus(cards: Card[]): Card[] {
  return cards
    .map((c, i) => ({ c, i }))
    .sort((a, b) => statusOrder[a.c.status] - statusOrder[b.c.status] || a.i - b.i)
    .map((x) => x.c)
}

// The verdict's first sentence: the page answers in one line, the card keeps the rest. A full stop
// inside a number ("1.4 million") does not end it.
export function firstSentence(text: string): string {
  const m = /^.*?[.!?](?=\s+[A-Z0-9(]|$)/.exec(text.trim())
  return m ? m[0] : text
}

// "1 thing", "14 things".
const things = (n: number) => `${n} ${n === 1 ? "thing" : "things"}`

export function PageAnswer({ cards, serverId, database }: { cards: Card[]; serverId: number; database: string }) {
  const toDo = cards.reduce((n, c) => n + c.toDo, 0)
  const red = cards.filter((c) => c.status === "red")
  // Grey with a verdict is "too early to tell" (Design), not a card that could not be read.
  const unread = cards.filter((c) => c.status === "grey" && !c.verdict).length
  const first = cardsByStatus(cards).find((c) => c.status === "red" || c.status === "amber")

  let lead: string
  if (red.length > 0) {
    lead = `${things(Math.max(toDo, red.length))} to do, ${red.length === 1 ? "one" : red.length} to act on today.`
  } else if (first) {
    lead = `${things(Math.max(toDo, 1))} to do, nothing to act on today.`
  } else {
    lead = "No problems found."
  }
  const unreadText =
    unread > 0 ? ` ${unread === 1 ? "One card" : `${unread} cards`} could not be checked.` : ""

  return (
    <p className="text-base break-words">
      <span className="font-medium">{lead}</span>
      {first && (
        <>
          {" "}
          Start with{" "}
          <Link to={cardPath(serverId, database, first.id)} className="font-medium underline underline-offset-4 hover:text-foreground/80">
            {first.title}
          </Link>
          {first.verdict && <>: {firstSentence(first.verdict)}</>}
        </>
      )}
      {unreadText && <span className="text-muted-foreground">{unreadText}</span>}
    </p>
  )
}
