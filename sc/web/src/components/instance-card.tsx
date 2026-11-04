import { Link } from "react-router-dom"
import { ChevronRight, Loader2 } from "lucide-react"

import type { Card as CardData, CardStatus, Fact } from "@/api"
import { Card, CardAction, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { formatWhen } from "@/lib/format"
import { cardPath } from "@/lib/paths"
import { cn } from "@/lib/utils"

// One meaning per colour everywhere (docs/landing-page.md): red act today, amber something to do,
// green no problem found, grey not checked or could not be read.
const statusColour: Record<CardStatus, string> = {
  green: "text-emerald-600 dark:text-emerald-400",
  amber: "text-amber-600 dark:text-amber-400",
  red: "text-destructive",
  grey: "text-muted-foreground",
}

const toneColour: Record<Fact["tone"], string> = {
  normal: "",
  bad: "text-amber-600 dark:text-amber-400",
  worse: "text-destructive",
}

// The dot and the words next to it; a spinner in place of the dot while the card is being read.
export function CardStatusLine({
  status,
  text,
  checking = false,
  className,
}: {
  status: CardStatus
  text: string
  checking?: boolean
  className?: string
}) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-xs whitespace-nowrap", statusColour[status], className)}>
      {checking ? (
        <Loader2 className="size-3 animate-spin" aria-label="Checking" />
      ) : (
        <span className="size-2 rounded-full bg-current" aria-hidden />
      )}
      {text}
    </span>
  )
}

// "2 to do, 1 for the client's IT", "3 to do, 2 can be applied here", "Nothing to do".
function countsText(card: CardData): string {
  if (card.toDo === 0) return "Nothing to do"
  const parts = [`${card.toDo} to do`]
  if (card.forClientIT > 0) parts.push(`${card.forClientIT} for the client's IT`)
  if (card.fixable > 0) parts.push(`${card.fixable} can be applied here`)
  return parts.join(", ")
}

// Two facts to a row, label left and value right. The value stays on one line and a long label wraps;
// a long value (the version line), or a long label with more than a word of value ("Recommended
// settings: no, 2 differ"), takes the whole row instead, so the two never run into each other.
const isWide = (f: Fact) => f.value.length > 14 || f.label.length + f.value.length > 24

export function FactList({ facts }: { facts: Fact[] }) {
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-[0.8125rem]">
      {facts.map((f) => (
        <div
          key={f.label}
          className={cn(
            "flex items-baseline justify-between gap-x-3 border-b border-dashed pb-1 min-w-0",
            isWide(f) && "col-span-2",
          )}
        >
          <dt className="min-w-0 text-muted-foreground">{f.label}</dt>
          <dd className={cn("text-right", isWide(f) ? "min-w-0" : "whitespace-nowrap", toneColour[f.tone])}>
            {f.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}

// One of the six cards on the database page, laid out as the card anatomy in docs/landing-page.md:
// title and question, status, verdict, facts, then counts, freshness and the Open link. A grey card
// shows its note where the verdict would be and has no link.
export function InstanceCard({
  card,
  serverId,
  database,
  checking = false,
}: {
  card: CardData
  serverId: number
  database: string
  checking?: boolean
}) {
  const grey = card.status === "grey"
  return (
    <Card className="h-full">
      <CardHeader>
        <CardTitle>{card.title}</CardTitle>
        <CardDescription>{card.question}</CardDescription>
        <CardAction>
          <CardStatusLine status={card.status} text={card.statusText} checking={checking} />
        </CardAction>
      </CardHeader>

      <CardContent className="grid flex-1 content-start gap-3">
        {grey ? (
          card.note && <p className="text-muted-foreground">{card.note}</p>
        ) : (
          <>
            {card.verdict && <p>{card.verdict}</p>}
            {card.facts.length > 0 && <FactList facts={card.facts} />}
            {card.note && <p className="text-xs text-muted-foreground">{card.note}</p>}
          </>
        )}
      </CardContent>

      {!grey && (
        <CardFooter className="flex-wrap justify-between gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <span>{countsText(card)}</span>
          <span className="ml-auto flex items-center gap-3">
            {card.asOf && <span>as of {formatWhen(card.asOf)}</span>}
            <Link
              to={cardPath(serverId, database, card.id)}
              className="inline-flex items-center font-medium text-foreground hover:underline underline-offset-4"
            >
              Open
              <ChevronRight className="size-3.5" />
            </Link>
          </span>
        </CardFooter>
      )}
    </Card>
  )
}

export function InstanceCardSkeleton() {
  return (
    <Card className="h-full min-h-56" aria-hidden>
      <CardHeader>
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <CardAction>
          <Skeleton className="h-4 w-20" />
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-3">
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-4/5" />
        <div className="grid grid-cols-2 gap-x-4 gap-y-2 pt-1">
          <Skeleton className="h-4" />
          <Skeleton className="h-4" />
          <Skeleton className="h-4" />
          <Skeleton className="h-4" />
        </div>
      </CardContent>
    </Card>
  )
}
