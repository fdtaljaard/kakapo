# Phase 2 contract: the instance page skeleton

> **Revision 2 (2026-11-03): database first.** See the section "Revision 2" at the end. It supersedes the
> routes, the overview endpoint and the Setup card rules below where they conflict. Everything else stands.

Backend (`internal/`) and frontend (`web/`) are built against this file at the same time. Read
`docs/landing-page.md` first (the design, the six cards, the card anatomy, the colours) and look at
`docs/mockups/instance-overview.html` (the look). This file fixes the shapes. JSON field names exactly as written.

## What the skeleton is

Three screens and no deeper:

1. **Home** `/`: one tile per registered instance (name, host, edition and build, last checked) and an
   **Add server** button that opens the existing setup form at `/servers/new`. With no servers at all, `/` shows
   the setup form directly (today's first-run behaviour).
2. **Instance page** `/instances/{id}`: the header, the six cards, Recent changes. Only the **Setup and
   configuration** card is live, fed by the Query Store state from phase 1. The other five are grey, "Not checked
   yet", with a one-line note of what they will show and no button.
3. **Card page** `/instances/{id}/cards/{card}`: the frame (title, question, status line, back link). For `setup`
   it shows the phase 1 database list with the Query Store fix dialog (the existing `DatabaseList` component).
   For the other five it shows the frame with the same "Not checked yet" note.

Routing is client-side (react-router-dom); the Go SPA fallback already serves `index.html` for unknown paths.

## Endpoints

| Method and path | Response |
|---|---|
| `GET /api/servers/{id}/overview` | `200` `Overview`. Connects to the server now, reads the instance facts and the Query Store state of every user database (storing snapshots as phase 1 does), and builds the cards. `404` unknown server, `502` unreachable. Takes a few seconds; the UI shows a loading state. |
| `GET /api/servers/{id}/changes?limit=10` | `200` `Change[]`, newest first. |

Existing endpoints stay as they are (`/api/servers`, `/api/servers/{id}/databases`, the Query Store preview and apply).

## Types

```ts
interface Overview {
  instance: InstanceInfo
  cards: Card[]                 // always exactly six, in this order: backups, setup, storage, upkeep, performance, design
  recentChanges: Change[]       // newest first, at most 10
  checkedAt: string             // when this overview was read
}

interface InstanceInfo {
  serverId: number
  name: string                  // the registered display name
  host: string
  serverName: string            // @@SERVERNAME
  edition: string               // "Standard Edition (64-bit)"
  productVersion: string        // "15.0.4382.1"
  productLevel: string          // "RTM", "CU25"
  productName: string           // "SQL Server 2019"
  startedAt: string             // sys.dm_os_sys_info.sqlserver_start_time, as UTC
  uptimeDays: number            // one decimal
  databaseCount: number         // user databases
}

type CardID = "backups" | "setup" | "storage" | "upkeep" | "performance" | "design"
type CardStatus = "green" | "amber" | "red" | "grey"

interface Card {
  id: CardID
  title: string                 // "Setup and configuration"
  question: string              // "Is it set up the way it should be?"
  status: CardStatus
  statusText: string            // "No problem found" | "3 things to do" | "Needs attention today" | "Not checked yet" | "Could not be read"
  verdict: string               // one sentence with the number; "" when grey
  facts: Fact[]                 // 0 to 4
  toDo: number                  // findings that need an action
  forClientIT: number           // of those, owner = Client IT (0 in the skeleton)
  fixable: number               // of those, the app can apply (Query Store: off + drift databases with canFix)
  asOf: string | null           // when the card's data was read; null when grey
  note: string                  // grey cards: one line of what the card will show. Live cards: "" or a caveat such as "3 databases could not be read"
}

interface Fact {
  label: string                 // "Query Store"
  value: string                 // "on 5 of 12"
  tone: "normal" | "bad" | "worse"
}

interface Change {              // one row of the change table
  id: number
  serverId: number
  database: string
  changedAt: string
  object: string                // "Query Store"
  changeType: string            // "SETTING"
  description: string
  result: string
}
```

## The Setup card in the skeleton (the only live one)

Built in `internal/domain` from the `DatabaseStatus[]` of phase 1 and the instance info, no I/O, unit tested:

- Count per verdict over the databases that could be read: `ok`, `drift`, `off`, `unavailable`; `unread` = those
  with `queryStore == null`.
- **status**: `red` never in the skeleton (Query Store is not Critical); `amber` when off + drift > 0; `green`
  when every readable database is ok and at least one was read; `grey` when none could be read.
- **statusText**: "No problem found" / "N things to do" (N = off + drift; "1 thing to do") / "Could not be read".
- **verdict**, pick the first that applies:
  - off > 0: "Query Store is off on {off} of {total} databases, so their query history is lost at every restart."
  - drift > 0: "Query Store is on but differs from the recommended settings on {drift} of {total} databases."
  - else: "Query Store is on with the recommended settings on all {total} databases."
  where total = databases read (not counting unread).
- **facts** (in this order): `Query Store` = "on {ok+drift} of {total}" (tone bad when off > 0);
  `Recommended settings` = "{ok} of {total}" (bad when drift > 0); `Version` = "{productName} {productLevel}, build {productVersion}";
  `Fixes available here` = "{fixable}" (normal).
- **toDo** = off + drift; **fixable** = off + drift where `canFix`; **forClientIT** = 0.
- **note**: "" or "{unread} databases could not be read" when unread > 0.

The other five cards: `status: "grey"`, `statusText: "Not checked yet"`, `verdict: ""`, `facts: []`, counts 0,
`asOf: null`, and these notes:

| id | title | question | note |
|---|---|---|---|
| backups | Backups and data safety | Could you lose data? | Full and log backups, CHECKDB, damaged pages and the worst-case data loss per database. |
| setup | Setup and configuration | Is it set up the way it should be? | (live) |
| storage | Storage and space | Will it run out of room? | Free disk, file growth, log size, tempdb and the log tables that need cleanup. |
| upkeep | Upkeep | Is someone looking after it? | SQL Agent, failed jobs, index and statistics maintenance, and who is told about errors. |
| performance | Performance | Is it slow, and why? | Waits in plain English, CPU pressure, blocking, deadlocks and the costliest queries. |
| design | Database design | Is the database built well? | Indexes, heaps, the tables everyone waits on, and Granite PreScripts, per database. |

## Backend notes

- `collect.InstanceInfo(ctx, db)` reads `@@SERVERNAME`, `SERVERPROPERTY` Edition/ProductVersion/ProductLevel,
  `sqlserver_start_time` from `sys.dm_os_sys_info`, and the user database count, in one round trip.
- `service.Overview(ctx, serverID)` = connect, instance info, the phase 1 database list (reuse `ListDatabases`
  internals so snapshots are stored once), `domain.BuildCards(...)`, `store.ListChanges(serverID, 10)`.
- `store.ListChanges(ctx, serverID, limit)` added; the `change` table exists from phase 1.
- Also update the `server` row's `last_version`, `last_edition`, `last_tested_at` on every successful overview, so
  the home tiles stay current.
- Card titles, questions and notes are data in `domain` (one slice), so the frontend never hard-codes them.

## Frontend notes

- Add `react-router-dom`. Routes: `/` (home or first-run setup), `/servers/new` (setup form; on save go to the
  new instance page), `/instances/:id`, `/instances/:id/cards/:card`.
- The instance page calls `overview` once on load and on **Refresh**. While loading: the header shows the
  registered name and host at once (from `/api/servers`), the cards show a skeleton. On error: an alert with the
  message and Try again.
- Card component: exactly the anatomy in `docs/landing-page.md`; colours from the theme as in phase 1
  (`text-emerald-600`, `text-amber-600`, `text-destructive`, `text-muted-foreground`, with dark variants).
  A grey card shows its note where the verdict would be and no link; a live card links to its card page.
- Recent changes: a small table (when, database, what, result) or "No changes made through SQLMonitor yet."
- Home tile: name, host (monospace), edition and build, "checked {time}" or "never"; click opens the instance.
- Keep the mock (`VITE_MOCK_API=1`) working for the new endpoints with one instance and realistic card data,
  so the screens can be seen without the backend.
- Do not move or rewrite the phase 1 components; compose them.

## Revision 2: database first

The six cards belong to **one database**. The instance page becomes a database picker. See
`docs/landing-page.md` (Navigation, Header, The database picker).

### Routes

| Route | Screen |
|---|---|
| `/` | Home tiles, or the setup form when there are no servers. |
| `/servers/new` | Setup form. |
| `/instances/:id` | **Database picker**: the user databases of the instance, one line each (name, state, Query Store summary as a muted second line). Picking one navigates to the database page and remembers it (see `PUT`). If the server has a remembered database and the user arrived from Home, navigate straight to it (replace); the breadcrumb's instance crumb always shows the picker. |
| `/instances/:id/databases/:db` | **Database page**: header, six cards, Recent changes for this database. |
| `/instances/:id/databases/:db/cards/:card` | **Card page** for this database. For `setup`: this one database's Query Store line with the Apply button and the existing fix dialog (not the list of all databases). |

Breadcrumb on every page below Home: `Instances › {instance name} › {database} › {card title}`.

### Endpoints

| Method and path | Response |
|---|---|
| `GET /api/servers/{id}/databases` | unchanged (the picker uses it). |
| `GET /api/servers/{id}/databases/{db}/overview` | `200` `Overview` for this database. `404` unknown server or database, `502` unreachable. Replaces `GET /api/servers/{id}/overview`, which is removed. |
| `GET /api/servers/{id}/databases/{db}/changes?limit=10` | `200` `Change[]` for this database, newest first. Replaces the server-level changes route. |
| `PUT /api/servers/{id}/database` with body `{"database": "GraniteLive"}` | `200` `Server`. Remembers the database on the registration (the existing `database` column; it is also the connection's initial catalog, which is fine). `404` unknown server. `400` empty name. |

`Overview` gains one field and the instance block stays:

```ts
interface Overview {
  instance: InstanceInfo        // as before
  database: DatabaseStatus      // the chosen database, with its Query Store state (phase 1 shape)
  cards: Card[]                 // six, same order, now about this database
  recentChanges: Change[]       // this database only
  checkedAt: string
}
```

`Server` (from `/api/servers`) already carries `database`; the frontend uses it for the straight-through navigation.

### The Setup card for one database (replaces the per-instance rules)

Inputs: the database's `QueryStoreStatus` (or null with `error`) and the instance info. Pure function, unit tested.

- `queryStore == null`: grey, "Could not be read", note = the error.
- verdict `off`: **amber**, statusText "1 thing to do", verdict "Query Store is off on {db}, so its query history is lost at every restart."
- verdict `drift`: **amber**, "1 thing to do", verdict "Query Store is on but differs from the recommended settings on {db}: {first drift setting} is {current}, recommended {target}."
- verdict `unavailable`: **grey**, "Cannot be changed here", verdict "" , note = "{readonlyReasonText or the reason from readOnly/isSecondaryReplica/state}".
- verdict `ok`: **green**, "No problem found", verdict "Query Store is on with the recommended settings: {summary}."
- facts: `Query Store` = summary (bad when off or drift); `Recommended settings` = "yes" | "no, {n} differ" (bad when drift) | "no" (bad when off); `Version` = "{productName} {productLevel}, build {productVersion}"; `Compatibility level` is **not** in the skeleton (comes with the full Setup card).
- toDo = 1 for off or drift else 0; fixable = 1 when toDo = 1 and canFix; forClientIT = 0.

The other five cards: grey with their notes, unchanged.

### Frontend notes for revision 2

- The database page caches its overview per server+database as the instance page did.
- The picker remembers the choice by calling `PUT /api/servers/{id}/database` before navigating; a failure to
  remember is logged, not blocking.
- The Setup card page shows one `DatabaseLine`-style row for this database (reuse the phase 1 line and dialog by
  giving `DatabaseList` an optional `database` prop that restricts it to that one name, or by extracting the line).
  After an Apply, the card page re-reads the overview so the status line at the top is current.
- Home tiles show "{database}" under the host when the registration has one.

## Revision 3: edit an instance; the list only when no database is set

Two rules from the user (2026-11-03):

1. A registration can be **edited**: name, host, authentication, user name, password, database, encryption.
2. When a registration names a database, the user **never sees the list of all databases**. The picker exists
   only for a registration without a database, and picking one sets it (as revision 2 does).

### Routes

| Route | Screen |
|---|---|
| `/instances/:id` | **Instance page.** Header: name, host, auth ("Windows authentication" or "SQL login as sa"), encryption, last tested, edition and build. Buttons: **Edit**, and when a database is set, **Open {database}**. Below: when a database is set, one line "Database: {name}" and nothing else; when no database is set, the revision 2 picker. From Home with a database set, still straight through to the database page (replace). |
| `/instances/:id/edit` | **Edit form.** The setup form pre-filled from the registration. Password field empty with placeholder "unchanged" for a SQL login; typing one replaces it. Buttons: Test connection, Save, Cancel (back to the instance page). On Save go to the instance page. |

The breadcrumb's instance crumb opens the instance page (not the picker directly).

### Endpoints

| Method and path | Request | Response |
|---|---|---|
| `GET /api/servers/{id}` | | `200` `Server`; `404`. |
| `PUT /api/servers/{id}` | `ServerInput` (phase 0 shape). `password` empty with `auth: "sql"` means keep the stored password. | `200` `Server`. Tests the connection first with the resulting credentials; on failure `502` and nothing is changed. On success updates the row (name, host, auth, username, database, encrypt, trustCert, last_version, last_edition, last_tested_at), then stores the new password when one was given, or deletes the stored one when auth is now Windows. `400` invalid input; `404` unknown server. |
| `POST /api/servers/test` | unchanged, but accepts an optional `id` field: when `auth` is `sql`, `password` is empty and `id` is given, the test uses that server's stored password. | |

`PUT /api/servers/{id}/database` from revision 2 stays (the picker uses it).

### Rules

- Editing never drops data: snapshots and changes stay keyed by server id.
- An edit that changes the host still keeps the id; the header's "since restart" and the cards simply re-read.
- The stored password is never returned by any endpoint.
