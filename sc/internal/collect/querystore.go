package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// safeSession is what every batch here starts with, as the scripts do: give up on a lock
// after 10 seconds and lose any deadlock rather than a client's session.
const safeSession = "SET LOCK_TIMEOUT 10000; SET DEADLOCK_PRIORITY LOW; "

// Open connects to a server and checks the connection works. The caller closes it.
func Open(ctx context.Context, in domain.ServerInput) (*sql.DB, error) {
	db, err := sql.Open("sqlserver", ConnString(in))
	if err != nil {
		return nil, fmt.Errorf("connection string: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, friendlyError(err)
	}
	return db, nil
}

// EngineVersion returns SERVERPROPERTY('ProductVersion'), e.g. 16.0.1000.6.
func EngineVersion(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, Tag(`SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64))`)).Scan(&v)
	if err != nil {
		return "", friendlyError(err)
	}
	return v, nil
}

const databasesQuery = safeSession + `
SELECT d.name, d.state_desc,
       CAST(CASE WHEN CAST(DATABASEPROPERTYEX(d.name, 'Updateability') AS nvarchar(32)) = N'READ_ONLY' THEN 1 ELSE 0 END AS bit),
       CAST(d.is_auto_close_on AS bit)
FROM sys.databases AS d
WHERE d.database_id > 4            -- not master, tempdb, model, msdb
  AND d.source_database_id IS NULL -- not database snapshots
`

// ListDatabases returns the user databases, ordered by name, with their state, whether they are
// read-only, and whether this instance holds them as an availability group secondary.
// QueryStore is left nil; read it with QueryStoreOptions.
func ListDatabases(ctx context.Context, db *sql.DB) ([]domain.DatabaseStatus, error) {
	return listDatabases(ctx, db, "")
}

// HoldOpen keeps an AUTO_CLOSE database open for the rest of a request: SQL Server closes
// such a database the moment no session uses it, so between one collector's read and the
// next it can be closing or re-opening (state RECOVERING, options NULL). One pinned
// connection sitting inside the database stops that. The returned release gives it back.
// For any other database it does nothing.
func HoldOpen(ctx context.Context, db *sql.DB, d domain.DatabaseStatus) (release func()) {
	release = func() {}
	if !d.AutoClose || d.State != "ONLINE" {
		return release
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return release
	}
	if _, err := conn.ExecContext(ctx, "USE "+domain.QuoteName(d.Name)+";"); err != nil {
		conn.Close()
		return release
	}
	return func() { _ = conn.Close() }
}

// Database returns one user database by name, or nil when there is none by that name.
// The name is compared by SQL Server, so its collation decides case.
//
// An AUTO_CLOSE database shows as RECOVERING for a moment while SQL Server opens it, which
// a read landing in that moment would report as "not online". So when that is what we see,
// open the database with a statement that returns nothing and read its state again.
func Database(ctx context.Context, db *sql.DB, name string) (*domain.DatabaseStatus, error) {
	for attempt := 0; ; attempt++ {
		dbs, err := listDatabases(ctx, db, name)
		if err != nil || len(dbs) == 0 {
			return nil, err
		}
		d := dbs[0]
		if d.State != "RECOVERING" || !d.AutoClose || attempt == 3 {
			return &d, nil
		}
		open := Tag(safeSession + "EXEC " + domain.QuoteName(d.Name) + ".sys.sp_executesql N'/* kakapo */ DECLARE @opened int = 1;';")
		_, _ = db.ExecContext(ctx, open) // best effort; the next read says whether it worked
		select {
		case <-ctx.Done():
			return &d, nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func listDatabases(ctx context.Context, db *sql.DB, name string) ([]domain.DatabaseStatus, error) {
	q, args := databasesQuery+" ORDER BY d.name", []any{}
	if name != "" {
		q, args = databasesQuery+" AND d.name = @name ORDER BY d.name", []any{sql.Named("name", name)}
	}
	rows, err := db.QueryContext(ctx, Tag(q), args...)
	if err != nil {
		return nil, friendlyError(err)
	}
	defer rows.Close()
	out := []domain.DatabaseStatus{}
	for rows.Next() {
		var d domain.DatabaseStatus
		if err := rows.Scan(&d.Name, &d.State, &d.ReadOnly, &d.AutoClose); err != nil {
			return nil, fmt.Errorf("read the database list: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, friendlyError(err)
	}

	secondaries, err := secondaryDatabases(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].IsSecondaryReplica = secondaries[out[i].Name]
	}
	return out, nil
}

// secondaryDatabases names the databases this instance holds as an availability group secondary.
// Without Always On (Express, Standard without AG, or simply not enabled) there are none.
func secondaryDatabases(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	out := map[string]bool{}
	var hadr int
	if err := db.QueryRowContext(ctx, Tag(`SELECT CAST(ISNULL(SERVERPROPERTY('IsHadrEnabled'), 0) AS int)`)).Scan(&hadr); err != nil {
		return nil, friendlyError(err)
	}
	if hadr != 1 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, Tag(safeSession+`
		SELECT d.name
		FROM sys.dm_hadr_database_replica_states AS rs
		JOIN sys.databases AS d ON d.database_id = rs.database_id
		WHERE rs.is_local = 1 AND rs.is_primary_replica = 0`))
	if err != nil {
		return nil, fmt.Errorf("cannot read the availability group state (needs VIEW SERVER STATE): %w", friendlyError(err))
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// QueryStoreOptions reads sys.database_query_store_options in one database. The view only shows
// the current database, so the query runs through that database's sp_executesql. engineMajor
// decides whether wait_stats_capture_mode_desc exists (SQL Server 2017, major 14, and later).
func QueryStoreOptions(ctx context.Context, db *sql.DB, database string, engineMajor int) (domain.QueryStoreOptions, error) {
	wait := "CAST(N'' AS nvarchar(60))" // SQL Server 2016 has no wait-stats capture
	if engineMajor >= 14 {
		wait = "ISNULL(CAST(wait_stats_capture_mode_desc AS nvarchar(60)), N'')"
	}
	inner := `SELECT TOP (1)
	ISNULL(CAST(actual_state_desc AS nvarchar(60)), N''),
	ISNULL(CAST(desired_state_desc AS nvarchar(60)), N''),
	ISNULL(CAST(readonly_reason AS int), 0),
	ISNULL(CAST(current_storage_size_mb AS bigint), 0),
	ISNULL(CAST(max_storage_size_mb AS bigint), 0),
	ISNULL(CAST(query_capture_mode_desc AS nvarchar(60)), N''),
	` + wait + `,
	ISNULL(CAST(interval_length_minutes AS bigint), 0),
	ISNULL(CAST(stale_query_threshold_days AS bigint), 0),
	ISNULL(CAST(flush_interval_seconds AS bigint), 0),
	ISNULL(CAST(max_plans_per_query AS bigint), 0),
	ISNULL(CAST(size_based_cleanup_mode_desc AS nvarchar(60)), N'')
FROM sys.database_query_store_options;`

	q := Tag(safeSession + "EXEC " + domain.QuoteName(database) + ".sys.sp_executesql @stmt;")
	var o domain.QueryStoreOptions
	err := db.QueryRowContext(ctx, q, sql.Named("stmt", TagStatements(inner))).Scan(
		&o.ActualState, &o.DesiredState, &o.ReadonlyReason, &o.CurrentStorageMb, &o.MaxStorageMb,
		&o.CaptureMode, &o.WaitStatsCaptureMode, &o.IntervalLengthMinutes, &o.StaleQueryThresholdDays,
		&o.FlushIntervalSeconds, &o.MaxPlansPerQuery, &o.SizeBasedCleanupMode)
	if errors.Is(err, sql.ErrNoRows) {
		return o, fmt.Errorf("SQL Server returned no Query Store options for %s", database)
	}
	if err != nil {
		return o, friendlyError(err)
	}
	o.ActualState = strings.ToUpper(o.ActualState)
	o.DesiredState = strings.ToUpper(o.DesiredState)
	return o, nil
}

// ExecFix runs one fix statement that a preview built and the user confirmed, with its
// parameters if any. It is the only place the app writes to a monitored server.
func ExecFix(ctx context.Context, db *sql.DB, statement string, args ...any) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return friendlyError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, safeSession); err != nil {
		return friendlyError(err)
	}
	if _, err := conn.ExecContext(ctx, Tag(statement), args...); err != nil {
		return friendlyError(err)
	}
	return nil
}
