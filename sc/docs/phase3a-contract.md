# Phase 3a contract: the Backups and data safety card

The first real card. It also introduces the **finding model** and the **card detail** endpoint that every later
card reuses. Source of the checks: `External References/granite-sql/SqlHealthCheck/01-QuickScan.sql` sections 5
(BAK01 to BAK05) and 2 (DBC08), `Checks.csv` rows BAK01 to BAK05 and DBC08, and `DESIGN.md` section 4 (the
finding model). Read `docs/landing-page.md` for the card anatomy and `docs/phase2-contract.md` for the Card shape.
Field names exactly as written. Dates RFC 3339 UTC.

## The finding model (new, generic)

One row, same columns for every card, same words as the HTML reports (`DESIGN.md` section 4):

```ts
type Severity = "Critical" | "High" | "Medium" | "Low" | "Info"   // scan ranks 1..5
type Owner = "Us" | "Client IT" | "Vendor"
type When = "Online" | "Window" | "Decide first" | "None"          // scan ChangeType: Online, Offline -> Window, Planned -> Decide first, None
type Effort = "Quick win" | "Moderate" | "Project" | "None"

interface Finding {
  checkId: string      // "BAK05"
  title: string        // the check's title from the scan's catalogue, e.g. "Worst-case data loss"
  severity: Severity
  owner: Owner
  when: When
  effort: Effort
  object: string       // the database name here
  finding: string      // one sentence with the number that proves it
  why: string          // one sentence
  doThis: string       // the statement, or one sentence naming who does what
  thenCheck: string    // from Checks.csv
  evidence: string     // the raw numbers
}

interface CheckStatus {
  checkId: string
  title: string
  status: "passed" | "finding" | "skipped"
  note: string         // why skipped; "" otherwise
}

interface CardDetail {
  card: Card           // the same object the overview carries for this card
  findings: Finding[]  // ranked: severity, then effort (quick win first), then when (Online first), then checkId
  checks: CheckStatus[]// every check behind the card, in catalogue order
}
```

The catalogue of checks the app implements lives in `internal/domain` as data (id, card, title, question, owner,
thenCheck), starting with the six below. The reference `Checks.csv` is not shipped.

## Endpoints

| Method and path | Response |
|---|---|
| `GET /api/servers/{id}/databases/{db}/overview` | unchanged shape; the `backups` card is now live. |
| `GET /api/servers/{id}/databases/{db}/cards/{card}` | `200` `CardDetail`, read live. `404` unknown server, database or card id. For `setup` in this phase: the Setup card with `findings: []` and `checks: []` (its checks come with the full Setup card later). For the still-grey cards: the grey card with empty lists. |

## What the Backups card reads (`collect`, one database, SELECTs only)

In one or two round trips, from the instance (not inside the database):

| Item | Source |
|---|---|
| state, recovery model, create date, availability-group member | `sys.databases`: `state_desc`, `recovery_model_desc`, `create_date`, `replica_id IS NOT NULL` |
| last full, last differential, last log backup finish times | `msdb.dbo.backupset` where `database_name = @db` and `backup_finish_date >= create_date`, `type` D / I / L (copy-only included, as the scan does) |
| restorable points in the last 7 days | `msdb.dbo.backupset` where `is_copy_only = 0` and (type in D, I or (type = L and recovery model in FULL, BULK_LOGGED)) and finish date >= now minus 7 days: the list of finish times |
| counts in the last 7 days | fulls, differentials, logs (all rows, as the scan's evidence does) |
| last good CHECKDB | `DATABASEPROPERTYEX(@db, 'LastGoodCheckDbTime')`: NULL on versions before 2016 SP2 (unknown), `1900-01-01` means never |
| damaged pages | `msdb.dbo.suspect_pages` for this database: count and max `last_update_date` |
| log size MB | `sys.master_files` for this database, `type_desc = 'LOG'`, sum of `size * 8 / 1024` |
| msdb readable | if reading `msdb.dbo.backupset` fails (permissions), the facts carry `msdbError` and the card is grey with that reason |

The server's clock: read `GETDATE()` in the same batch and do every "ago" and "gap" calculation against that, not
the laptop's clock. Return it as `serverNow`.

## Building the card (`domain`, pure, unit tested)

Severity ranks follow the scan exactly; +1 for an availability-group member where the scan adds it, never above 5.

| Check | Fires when | Severity | When / Effort | Finding sentence (as the scan, with the numbers) |
|---|---|---|---|---|
| **BAK01** A full backup exists | no full backup ever | created less than 1 day ago: Medium; else Critical (+1 AG) | Online / Quick win | "No full backup of this database has been recorded." |
| **BAK02** Transaction log backups | recovery FULL or BULK_LOGGED and (no log backup ever, or last one older than 24 h) | High (+1 AG) | Online / Quick win | "Database uses FULL recovery but its log has never been backed up." or "Last log backup was N hours ago." Evidence: log size MB. |
| **BAK05** Worst-case data loss | always, when a full backup exists | worst gap: >= 7 days Critical; >= 2 days High; > 25 h Medium; else Info (+1 AG when > 25 h) | > 25 h: Decide first / Moderate; else None / None | ">= 7 days: "No backup of any kind in the last 7 days: everything since the last backup would be lost." else "You could lose up to {N days / N hours / N minutes} of data: the longest gap between backups in the last 7 days." Do this differs for SIMPLE (switch to FULL and log backups every 15 minutes, or add a midday differential) and FULL (log backups every 15 minutes close this gap; if scheduled, check the job runs). Evidence: counts in 7 days, last full, last log, recovery model. |
| **BAK03** Integrity check | last good CHECKDB known and (never, or older than 14 days) | never: High; else Medium (+1 AG) | Online / Quick win | "No successful integrity check (DBCC CHECKDB) has ever been recorded." or "Last successful integrity check was N days ago." Skipped with a note when unknown on this version. |
| **BAK04** Suspect pages | any suspect page recorded for this database | Critical | None / None | "SQL Server has recorded N damaged (suspect) page(s)." |
| **DBC08** Recovery model | recovery model SIMPLE | Info | Decide first / Moderate | "Database uses the SIMPLE recovery model." |

Gap calculation (as the scan): points = restorable backup finish times in the last 7 days, plus the later of
(create date, now minus 7 days), plus now; the worst gap is the largest difference between consecutive points,
in minutes. Format: >= 2 days "N.N days", >= 120 minutes "N hours", else "N minutes".

Why, Do this and Evidence texts: take them from the scan verbatim (they were written for consultants and
clients); Owner and Then check from `Checks.csv` (all six are Client IT). Availability-group note appended to
evidence as the scan does.

**Card from the findings:**

- Actionable = severity Critical, High, Medium or Low.
- **status**: any Critical: `red`, "Needs attention today"; else any actionable: `amber`, "N things to do"
  ("1 thing to do"); else `green`, "No problem found"; facts unreadable (msdb) or database not online: `grey`,
  "Could not be read" with the reason as the note.
- **verdict**: the top-ranked finding's sentence when there is an actionable one; otherwise the BAK05 sentence
  ("You could lose up to 15 minutes of data: ..."), so a green card still states the number the client cares about.
- **facts** (four): `Worst-case data loss` = the formatted gap or "no backup" (tone: Critical/High `worse`, Medium
  `bad`, else normal); `Last full backup` = "N days ago" / "today HH:MM" (server time) / "never" (bad when > 1 day,
  worse when never); `Last CHECKDB` = "N days ago" / "never" / "unknown on this version" (bad when > 14 days,
  worse when never); the fourth is `Damaged pages` = "N" (worse) when any, otherwise `Log backups` = "every N min
  on average" over the 7 days when FULL with logs, "none in FULL recovery" (bad), or "not needed (SIMPLE)".
- **toDo** = number of actionable findings; **forClientIT** = the same (all six are Client IT); **fixable** = 0
  (there is no fix the app applies for backups; the Do this names what the client's IT schedules).
- **asOf** = serverNow as read.

## Storage (`store`, new migration)

```sql
CREATE SEQUENCE IF NOT EXISTS seq_card_read START 1;
CREATE TABLE IF NOT EXISTS card_read (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_card_read'),
  server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, card VARCHAR NOT NULL,
  captured_at TIMESTAMP NOT NULL, status VARCHAR NOT NULL, status_text VARCHAR, verdict VARCHAR, to_do INTEGER
);
CREATE TABLE IF NOT EXISTS finding (
  card_read_id INTEGER NOT NULL, check_id VARCHAR NOT NULL, severity VARCHAR NOT NULL, owner VARCHAR, when_ VARCHAR,
  effort VARCHAR, object VARCHAR, finding VARCHAR, why VARCHAR, do_this VARCHAR, then_check VARCHAR, evidence VARCHAR
);
CREATE TABLE IF NOT EXISTS check_status (
  card_read_id INTEGER NOT NULL, check_id VARCHAR NOT NULL, status VARCHAR NOT NULL, note VARCHAR
);
```

Every read of a live card (overview or detail) stores one `card_read` with its findings and check statuses, UTC
from Go. History is kept; nothing is deleted.

## Frontend: the card page for a card with findings

Under the frame (title, question, status line, verdict), in this order:

1. **Findings**, ranked, each as a card: severity badge (red Critical, amber High and Medium, grey Low and Info),
   check id and title, the finding sentence, "Why it matters", "Do this" in a block with a copy button, "Then
   check", and the evidence in muted small text. An Info finding is shown after the actionable ones, visually
   quieter.
2. **What was checked**: one line per check: green tick "Passed", amber "Finding", grey "Skipped: note".
3. Nothing else. No charts.

The card page calls the detail endpoint on load and on Refresh; the Setup card page keeps its Query Store row
and shows "What was checked" only when `checks` is non-empty. A grey card page shows the note and the checks list
if any were skipped with reasons.

The mock gets a Backups card for each of its three databases: one green (15-minute log backups, CHECKDB 3 days
ago), one amber (FULL without log backups, CHECKDB 20 days ago), one red (never backed up, created a month ago).

## Addendum (2026-11-03): the backup schedule fix

When BAK01 or BAK02 fires and no enabled Agent job backs this database up, the card offers **Create a backup
schedule** (SCOPE section 3b, phase 3g). Not built yet; the decisions it needs (Ola or native, Express, the
operator, the folder) are listed there.
