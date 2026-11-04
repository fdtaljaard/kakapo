# Phase 1 contract: database setup and the Query Store fix

The backend (Go, `internal/`) and the frontend (`web/`) are built against this file at the same time.
Read `SCOPE.md` section 3 item 9, section 3a and section 7 first; this file only fixes the shapes.
JSON field names are exactly as written here (camelCase). Dates are RFC 3339 UTC strings.

## Endpoints

| Method and path | Request | Response |
|---|---|---|
| `GET /api/servers/{id}/databases` | | `200` `DatabaseStatus[]`, user databases only (no master, model, msdb, tempdb), ordered by name. Reads the server now; also stores one `db_query_store` snapshot per database. |
| `POST /api/servers/{id}/databases/{db}/query-store/preview` | no body | `200` `FixPreview`. Never changes anything. |
| `POST /api/servers/{id}/databases/{db}/query-store/apply` | no body | `200` `FixResult`. Runs the preview's statement, reads the options back, writes a `change` row. |

Errors: `{"error": "..."}` with `400` (bad id or name), `404` (server or database not found), `409` (nothing to
apply, or the database cannot be fixed here, e.g. secondary replica), `502` (SQL Server unreachable or refused).
`{db}` is the database name URL-encoded.

## Types

```ts
// One row per user database
interface DatabaseStatus {
  name: string
  state: string                  // ONLINE, RESTORING, RECOVERING, OFFLINE, ...
  readOnly: boolean              // DATABASEPROPERTYEX(name, 'Updateability') = 'READ_ONLY'
  isSecondaryReplica: boolean    // from sys.dm_hadr_database_replica_states; false when no AG
  queryStore: QueryStoreStatus | null   // null when it could not be read; then `error` says why
  error?: string
}

interface QueryStoreStatus {
  capturedAt: string
  actualState: string            // OFF | READ_ONLY | READ_WRITE | ERROR
  desiredState: string
  readonlyReason: number
  readonlyReasonText: string     // "" when 0; "secondary replica" for 8; "storage full" for 65536; otherwise "database state (<n>)"
  currentStorageMb: number
  maxStorageMb: number
  storageUsedPercent: number     // 0..100, one decimal, 0 when maxStorageMb is 0
  captureMode: string            // ALL | AUTO | NONE | CUSTOM
  waitStatsCaptureMode: string   // ON | OFF; "" on SQL Server 2016 (column absent)
  intervalLengthMinutes: number
  staleQueryThresholdDays: number
  flushIntervalSeconds: number
  maxPlansPerQuery: number
  sizeBasedCleanupMode: string   // OFF | AUTO
  verdict: "ok" | "drift" | "off" | "unavailable"
  drift: Drift[]                 // empty when verdict is "ok"; every target setting when "off"
  canFix: boolean                // false when readOnly, isSecondaryReplica, or state is not ONLINE
  summary: string                // "On, 23% full, 15-min intervals, 30 days" | "Off" | "Read-only: storage full"
}

interface Drift {
  setting: string                // the T-SQL option name, e.g. INTERVAL_LENGTH_MINUTES
  current: string                // as text, e.g. "60"
  target: string                 // e.g. "15"
}

interface FixPreview {
  database: string
  engineVersion: string          // e.g. "16.0.1000.6"
  changes: Drift[]               // what the statement changes; empty means nothing to do
  statement: string              // the exact T-SQL; "" when changes is empty
  rollback: string               // SET QUERY_STORE (...) with the previous values; "" when nothing to do
  warnings: string[]             // plain sentences, e.g. the interval-length reset, the 2 GB growth, old build
}

interface FixResult {
  preview: FixPreview
  before: QueryStoreStatus
  after: QueryStoreStatus
  changeId: number
  appliedAt: string
}
```

## Target and rules (from SCOPE.md 3a, repeated so nothing drifts)

OPERATION_MODE = READ_WRITE, QUERY_CAPTURE_MODE = AUTO, MAX_STORAGE_SIZE_MB = 2048, SIZE_BASED_CLEANUP_MODE = AUTO,
INTERVAL_LENGTH_MINUTES = 15, STALE_QUERY_THRESHOLD_DAYS = 30, DATA_FLUSH_INTERVAL_SECONDS = 900,
MAX_PLANS_PER_QUERY = 200, WAIT_STATS_CAPTURE_MODE = ON (omit below engine major version 14).

- Off: `ALTER DATABASE [db] SET QUERY_STORE = ON (<all target settings>);`
- On with drift: `ALTER DATABASE [db] SET QUERY_STORE (<only the drifted settings>);` and if `actualState` is not
  READ_WRITE include `OPERATION_MODE = READ_WRITE`.
- On and matching: no statement, `changes` empty.
- Statements are built in `internal/domain` with no I/O and unit tests for: off on 2016, off on 2022, on with the
  default 60-minute interval, on and matching, read-only because storage is full (raise the limit and set READ_WRITE).
- Always `QUOTENAME`-style bracket quoting of the database name with `]` doubled.

## DuckDB tables (added by a new migration in `internal/store`)

```sql
CREATE TABLE IF NOT EXISTS db_query_store (
  server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
  actual_state VARCHAR, desired_state VARCHAR, readonly_reason INTEGER,
  current_storage_mb INTEGER, max_storage_mb INTEGER, capture_mode VARCHAR, wait_stats_capture_mode VARCHAR,
  interval_length_minutes INTEGER, stale_query_threshold_days INTEGER, flush_interval_seconds INTEGER,
  max_plans_per_query INTEGER, size_based_cleanup_mode VARCHAR, verdict VARCHAR
);
CREATE SEQUENCE IF NOT EXISTS seq_change START 1;
CREATE TABLE IF NOT EXISTS change (
  id INTEGER PRIMARY KEY DEFAULT nextval('seq_change'),
  server_id INTEGER NOT NULL, db_name VARCHAR, changed_at TIMESTAMP NOT NULL,
  object VARCHAR NOT NULL, change_type VARCHAR NOT NULL, description VARCHAR NOT NULL,
  statement VARCHAR NOT NULL, rollback_statement VARCHAR, result VARCHAR
);
```

All timestamps written from Go as UTC (see `internal/store/server.go` for the pattern).

## Frontend behaviour

- The servers screen (`web/src/screens/servers.tsx`) gets, per server card, a **Databases** section that loads
  `GET /api/servers/{id}/databases` when the card is expanded (or on load when there is one server).
- Per database one line: name, then the Query Store `summary`, coloured by `verdict`: green tick for `ok`,
  amber for `drift`, red for `off`, grey for `unavailable` (show `error` or `readonlyReasonText`).
- When `canFix` and verdict is `drift` or `off`: an **Apply recommended settings** button. It calls `preview`,
  opens a dialog with the `changes` as a small table (setting, current, target), the `warnings` as a list, and the
  `statement` in a monospace block with a copy button. **Apply** calls `apply`; the dialog then shows `after.summary`
  and `after.verdict`, and the line in the card updates. **Cancel** closes without calling anything.
- When `canFix` is false: no button; the line says why (secondary replica, read-only, not online).
- Loading, error and empty states are visible, not silent.
