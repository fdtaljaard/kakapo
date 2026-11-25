// Phase 5x (docs/phase5x-contract.md): the topics of the navigation tree, each defined once, and the
// arrangements over them. An arrangement is a grouping of these topics; a second one is a new grouping,
// not a new tree.

import {
  blockingPath,
  cardPath,
  changesPath,
  databasePagePath,
  indexesPath,
  instancePath,
  memoryPath,
  tuningPath,
} from "@/lib/paths"

// The six cards, in the database page's order, with their titles for before the overview has been read.
export const navCards = [
  { id: "backups", title: "Backups and data safety" },
  { id: "setup", title: "Setup and configuration" },
  { id: "storage", title: "Storage and space" },
  { id: "upkeep", title: "Upkeep" },
  { id: "performance", title: "Performance" },
  { id: "design", title: "Database design" },
] as const

export type NavCardId = (typeof navCards)[number]["id"]

// Where a topic lives in SQL Server: on the instance, or in the database. Arrangement 2 groups by it.
export type TopicLevel = "instance" | "database"

export interface Topic {
  id: string
  label: string
  level: TopicLevel
  card?: NavCardId // the card it belongs to, by responsibility
  to: (serverId: number, database: string) => string
}

// A part of a card's page: the card's page, scrolled to the part once its detail is on screen.
const part = (card: NavCardId, anchor: string) => (serverId: number, database: string) =>
  `${cardPath(serverId, database, card)}#${anchor}`

export const topics: Topic[] = [
  { id: "backup-schedule", label: "Backup schedule", level: "database", card: "backups", to: part("backups", "backup-schedule") },
  { id: "checkdb-run", label: "CHECKDB from kakapo", level: "database", card: "backups", to: part("backups", "checkdb-run") },
  { id: "query-store", label: "Query Store", level: "database", card: "setup", to: part("setup", "query-store") },
  { id: "memory", label: "Memory", level: "instance", card: "setup", to: (s) => memoryPath(s) },
  { id: "drives", label: "Drives", level: "instance", card: "storage", to: part("storage", "drives") },
  { id: "tempdb", label: "TempDB files", level: "instance", card: "storage", to: part("storage", "tempdb") },
  { id: "tables", label: "Tables kakapo archives", level: "database", card: "storage", to: part("storage", "tables") },
  { id: "checkdb-room", label: "Room for CHECKDB", level: "database", card: "storage", to: part("storage", "checkdb-room") },
  { id: "maintenance-plan", label: "Maintenance plan", level: "instance", card: "upkeep", to: part("upkeep", "maintenance-plan") },
  { id: "indexes", label: "Indexes worth reorganising", level: "database", card: "upkeep", to: indexesPath },
  { id: "trend", label: "Better or worse than usual", level: "database", card: "performance", to: part("performance", "trend") },
  { id: "tuning", label: "Tuning report", level: "database", card: "performance", to: (s, d) => tuningPath(s, d) },
  { id: "blocking", label: "Blocking right now", level: "database", card: "performance", to: blockingPath },
  { id: "sample", label: "Live sample", level: "database", card: "performance", to: part("performance", "sample") },
  { id: "deadlocks", label: "Deadlocks", level: "database", card: "performance", to: part("performance", "deadlocks") },
  { id: "blocking-history", label: "Blocking history", level: "database", card: "performance", to: part("performance", "blocking-history") },
  { id: "foreign-keys", label: "Foreign keys without an index", level: "database", card: "design", to: part("design", "foreign-keys") },
  { id: "no-key-tables", label: "Tables without a primary key", level: "database", card: "design", to: part("design", "no-key-tables") },
  { id: "decisions", label: "Decisions", level: "database", to: (s, d) => `${databasePagePath(s, d)}#decisions` },
  { id: "recent-changes", label: "Recent changes", level: "database", to: (s, d) => `${databasePagePath(s, d)}#recent-changes` },
  { id: "changes", label: "What changed?", level: "instance", to: (s) => changesPath(s) },
]

const topic = (id: string): Topic => {
  const t = topics.find((x) => x.id === id)
  if (!t) throw new Error(`no navigation topic ${id}`)
  return t
}

// One row of the tree. A node with children is a folder: its chevron expands it, its label opens `to`.
// `card` names the card whose colour and "to do" count the row carries; a row without one has no dot.
export interface NavNode {
  id: string
  label: string
  to: string
  card?: NavCardId
  children?: NavNode[]
}

const leaf = (t: Topic, serverId: number, database: string): NavNode => ({
  id: t.id,
  label: t.label,
  to: t.to(serverId, database),
})

// Arrangement 1, by responsibility: the instance, the database, the six cards as categories with their
// topics below, then Decisions and Recent changes; What changed? on the instance. Without a database the
// instance shows its own topics and "Pick a database".
export function byResponsibility(
  server: { id: number; name: string },
  database: string | null,
  cardTitle: (id: NavCardId) => string,
): NavNode {
  const s = server.id
  if (!database) {
    return {
      id: "instance",
      label: server.name,
      to: instancePath(s),
      children: [
        { id: "pick-database", label: "Pick a database", to: `${instancePath(s)}#databases` },
        leaf(topic("memory"), s, ""),
        leaf(topic("changes"), s, ""),
      ],
    }
  }
  const categories: NavNode[] = navCards.map((c) => ({
    id: `card:${c.id}`,
    label: cardTitle(c.id),
    to: cardPath(s, database, c.id),
    card: c.id,
    children: topics.filter((t) => t.card === c.id).map((t) => leaf(t, s, database)),
  }))
  return {
    id: "instance",
    label: server.name,
    to: instancePath(s),
    children: [
      {
        id: "database",
        label: database,
        to: databasePagePath(s, database),
        children: [...categories, leaf(topic("decisions"), s, database), leaf(topic("recent-changes"), s, database)],
      },
      leaf(topic("changes"), s, database),
    ],
  }
}
