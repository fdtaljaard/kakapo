// Package store owns the DuckDB file: schema, the single writer, and queries.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	_ "github.com/duckdb/duckdb-go/v2"
)

// Store is the one handle on the DuckDB file. DuckDB wants a single writer,
// so every write goes through mu. Reads are concurrent.
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open opens or creates the DuckDB file at path and brings the schema up to date.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open duckdb %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// migrations run in order; each one is applied once. Keep them additive.
var migrations = []string{
	// 1: the server registry
	`CREATE SEQUENCE IF NOT EXISTS seq_server START 1;
	 CREATE TABLE IF NOT EXISTS server (
	   id             INTEGER PRIMARY KEY DEFAULT nextval('seq_server'),
	   name           VARCHAR NOT NULL,
	   host           VARCHAR NOT NULL,
	   auth           VARCHAR NOT NULL,
	   username       VARCHAR,
	   db_name        VARCHAR,
	   encrypt        BOOLEAN NOT NULL DEFAULT false,
	   trust_cert     BOOLEAN NOT NULL DEFAULT false,
	   created_at     TIMESTAMP NOT NULL DEFAULT current_timestamp,
	   last_version   VARCHAR,
	   last_edition   VARCHAR,
	   last_tested_at TIMESTAMP
	 );`,
	// 2: Query Store snapshots and the change log (docs/phase1-contract.md)
	`CREATE TABLE IF NOT EXISTS db_query_store (
	   server_id                  INTEGER NOT NULL,
	   db_name                    VARCHAR NOT NULL,
	   captured_at                TIMESTAMP NOT NULL,
	   actual_state               VARCHAR,
	   desired_state              VARCHAR,
	   readonly_reason            INTEGER,
	   current_storage_mb         INTEGER,
	   max_storage_mb             INTEGER,
	   capture_mode               VARCHAR,
	   wait_stats_capture_mode    VARCHAR,
	   interval_length_minutes    INTEGER,
	   stale_query_threshold_days INTEGER,
	   flush_interval_seconds     INTEGER,
	   max_plans_per_query        INTEGER,
	   size_based_cleanup_mode    VARCHAR,
	   verdict                    VARCHAR
	 );
	 CREATE SEQUENCE IF NOT EXISTS seq_change START 1;
	 CREATE TABLE IF NOT EXISTS change (
	   id                 INTEGER PRIMARY KEY DEFAULT nextval('seq_change'),
	   server_id          INTEGER NOT NULL,
	   db_name            VARCHAR,
	   changed_at         TIMESTAMP NOT NULL,
	   object             VARCHAR NOT NULL,
	   change_type        VARCHAR NOT NULL,
	   description        VARCHAR NOT NULL,
	   statement          VARCHAR NOT NULL,
	   rollback_statement VARCHAR,
	   result             VARCHAR
	 );`,
	// 3: settings of this data file, starting with the install id that keys stored passwords
	`CREATE TABLE IF NOT EXISTS setting (
	   key   VARCHAR PRIMARY KEY,
	   value VARCHAR NOT NULL
	 );`,
	// 4: every read of a live card, with its findings and check statuses (docs/phase3a-contract.md)
	`CREATE SEQUENCE IF NOT EXISTS seq_card_read START 1;
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
	 );`,
	// 5: every STO10 result, room to run CHECKDB, read on request (docs/phase3b-contract.md)
	`CREATE SEQUENCE IF NOT EXISTS seq_checkdb_room START 1;
	 CREATE TABLE IF NOT EXISTS checkdb_room (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_checkdb_room'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
	   duration_seconds DOUBLE, estimate_kb BIGINT, need_mb BIGINT, tempdb_free_mb BIGINT, tempdb_room_mb BIGINT,
	   data_volume VARCHAR, data_free_mb BIGINT, data_size_mb BIGINT,
	   severity VARCHAR NOT NULL, finding VARCHAR NOT NULL, evidence VARCHAR
	 );`,
	// 6: samples kept on every storage read, for the disk growth forecast (docs/phase3b-contract.md, addendum C)
	`CREATE TABLE IF NOT EXISTS volume_sample (
	   server_id INTEGER NOT NULL, captured_at TIMESTAMP NOT NULL, volume VARCHAR NOT NULL,
	   free_mb BIGINT NOT NULL, total_mb BIGINT NOT NULL
	 );
	 CREATE TABLE IF NOT EXISTS storage_sample (
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
	   data_alloc_mb BIGINT, data_used_mb BIGINT, log_size_mb BIGINT, log_used_pct DOUBLE,
	   tempdb_alloc_mb BIGINT, tempdb_used_mb BIGINT
	 );`,
	// 7: every DLK01 deadlock report, read on request (docs/phase3e-contract.md); a finding's clock
	`CREATE SEQUENCE IF NOT EXISTS seq_deadlock_report START 1;
	 CREATE TABLE IF NOT EXISTS deadlock_report (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_deadlock_report'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
	   duration_seconds DOUBLE, days INTEGER NOT NULL, total INTEGER NOT NULL, source VARCHAR NOT NULL,
	   pairs_json VARCHAR NOT NULL, findings_json VARCHAR NOT NULL
	 );
	 ALTER TABLE finding ADD COLUMN IF NOT EXISTS clock VARCHAR;`,
	// 8: the system_health SYSTEM and IO_SUBSYSTEM events read with the deadlocks (docs/phase3f-contract.md item 21).
	`ALTER TABLE deadlock_report ADD COLUMN IF NOT EXISTS health_json VARCHAR;`,
	// 9: every live sample of the Performance card, run on request, as JSON (docs/phase4c-contract.md, part 3)
	`CREATE SEQUENCE IF NOT EXISTS seq_perf_sample START 1;
	 CREATE TABLE IF NOT EXISTS perf_sample (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_perf_sample'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
	   seconds INTEGER NOT NULL, verdict VARCHAR NOT NULL, sample_json VARCHAR NOT NULL
	 );`,
	// 10: every DBCC CHECKDB run from the app (checkdb-now), its outcome and messages (docs/phase4d-contract.md, part 2)
	`CREATE SEQUENCE IF NOT EXISTS seq_checkdb_run START 1;
	 CREATE TABLE IF NOT EXISTS checkdb_run (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_checkdb_run'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, ran_at TIMESTAMP NOT NULL,
	   duration_seconds DOUBLE NOT NULL, physical_only BOOLEAN NOT NULL, clean BOOLEAN NOT NULL,
	   outcome VARCHAR NOT NULL, messages_json VARCHAR NOT NULL
	 );`,
	// 11: every BLK01 blocking history from the system_health session, read on request, as JSON (docs/phase5d-contract.md)
	`CREATE SEQUENCE IF NOT EXISTS seq_blocking_report START 1;
	 CREATE TABLE IF NOT EXISTS blocking_report (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_blocking_report'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, captured_at TIMESTAMP NOT NULL,
	   days INTEGER NOT NULL, source VARCHAR NOT NULL, leaders INTEGER NOT NULL, report_json VARCHAR NOT NULL
	 );`,
	// 12: every decision taken on a database, newest wins; never edited (docs/phase5m-contract.md, section 6)
	`CREATE SEQUENCE IF NOT EXISTS seq_decision START 1;
	 CREATE TABLE IF NOT EXISTS decision (
	   id INTEGER PRIMARY KEY DEFAULT nextval('seq_decision'),
	   server_id INTEGER NOT NULL, db_name VARCHAR NOT NULL, decision VARCHAR NOT NULL,
	   decided_at TIMESTAMP NOT NULL, decided_by VARCHAR NOT NULL,
	   choice VARCHAR NOT NULL,
	   verdict VARCHAR NOT NULL, reason VARCHAR, review_on DATE, change_id INTEGER
	 );`,
}

// InstallID returns the id of this data file, made once on first use. Stored passwords are
// keyed by it as well as by server id, so two data files on one machine never share or
// delete each other's passwords.
func (s *Store) InstallID(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var id string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = 'install_id'`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("install id: %w", err)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("install id: %w", err)
	}
	id = hex.EncodeToString(b)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO setting (key, value) VALUES ('install_id', ?)`, id); err != nil {
		return "", fmt.Errorf("install id: %w", err)
	}
	return id, nil
}

func (s *Store) migrate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT current_timestamp)`); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("schema_version: %w", err)
	}
	for i := current; i < len(migrations); i++ {
		if _, err := s.db.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}
