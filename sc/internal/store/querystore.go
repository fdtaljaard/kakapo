package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// ErrNotFound is returned when a row asked for by id does not exist.
var ErrNotFound = errors.New("not found")

// GetServer returns one registered server, or ErrNotFound.
func (s *Store) GetServer(ctx context.Context, id int64) (domain.Server, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM server WHERE id = ?`, id)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, ErrNotFound
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("get server %d: %w", id, err)
	}
	return srv, nil
}

// InsertQueryStoreSnapshot appends one read of a database's Query Store options. History is kept.
func (s *Store) InsertQueryStoreSnapshot(ctx context.Context, serverID int64, dbName string, q domain.QueryStoreStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	captured := q.CapturedAt.UTC()
	if q.CapturedAt.IsZero() {
		captured = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO db_query_store (server_id, db_name, captured_at, actual_state, desired_state, readonly_reason,
		  current_storage_mb, max_storage_mb, capture_mode, wait_stats_capture_mode, interval_length_minutes,
		  stale_query_threshold_days, flush_interval_seconds, max_plans_per_query, size_based_cleanup_mode, verdict)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, dbName, captured, q.ActualState, q.DesiredState, q.ReadonlyReason,
		q.CurrentStorageMb, q.MaxStorageMb, q.CaptureMode, nullIfEmpty(q.WaitStatsCaptureMode), q.IntervalLengthMinutes,
		q.StaleQueryThresholdDays, q.FlushIntervalSeconds, q.MaxPlansPerQuery, q.SizeBasedCleanupMode, q.Verdict)
	if err != nil {
		return fmt.Errorf("store Query Store snapshot for %s: %w", dbName, err)
	}
	return nil
}

// InsertChange writes one row of the change log and returns its id. ChangedAt is set here, in UTC,
// when the caller leaves it zero.
func (s *Store) InsertChange(ctx context.Context, c domain.Change) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := c.ChangedAt.UTC()
	if c.ChangedAt.IsZero() {
		changed = time.Now().UTC()
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO change (server_id, db_name, changed_at, object, change_type, description, statement, rollback_statement, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		c.ServerID, nullIfEmpty(c.Database), changed, c.Object, c.ChangeType, c.Description, c.Statement,
		nullIfEmpty(c.Rollback), nullIfEmpty(c.Result)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("write change log: %w", err)
	}
	return id, nil
}
