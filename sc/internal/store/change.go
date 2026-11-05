package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// ListChanges returns the latest changes made on one database of a server through the app,
// newest first. The name is compared exactly, as the change log stores it.
func (s *Store) ListChanges(ctx context.Context, serverID int64, dbName string, limit int) ([]domain.Change, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, server_id, coalesce(db_name, ''), changed_at, object, change_type, description, statement,
		       coalesce(rollback_statement, ''), coalesce(result, '')
		FROM change
		WHERE server_id = ? AND db_name = ?
		ORDER BY changed_at DESC, id DESC
		LIMIT ?`, serverID, dbName, limit)
	if err != nil {
		return nil, fmt.Errorf("list changes: %w", err)
	}
	defer rows.Close()
	out := []domain.Change{}
	for rows.Next() {
		var c domain.Change
		if err := rows.Scan(&c.ID, &c.ServerID, &c.Database, &c.ChangedAt, &c.Object, &c.ChangeType,
			&c.Description, &c.Statement, &c.Rollback, &c.Result); err != nil {
			return nil, fmt.Errorf("list changes: %w", err)
		}
		c.ChangedAt = asUTC(c.ChangedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListChangesSince returns the changes of one database of a server and the server-level ones (no
// database) made at or after since (UTC), newest first, at most 200: the tuning report's markers
// (docs/phase5c-contract.md).
func (s *Store) ListChangesSince(ctx context.Context, serverID int64, dbName string, since time.Time) ([]domain.Change, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, server_id, coalesce(db_name, ''), changed_at, object, change_type, description, statement,
		       coalesce(rollback_statement, ''), coalesce(result, '')
		FROM change
		WHERE server_id = ? AND (db_name = ? OR db_name IS NULL) AND changed_at >= ?
		ORDER BY changed_at DESC, id DESC
		LIMIT 200`, serverID, dbName, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("list changes: %w", err)
	}
	defer rows.Close()
	out := []domain.Change{}
	for rows.Next() {
		var c domain.Change
		if err := rows.Scan(&c.ID, &c.ServerID, &c.Database, &c.ChangedAt, &c.Object, &c.ChangeType,
			&c.Description, &c.Statement, &c.Rollback, &c.Result); err != nil {
			return nil, fmt.Errorf("list changes: %w", err)
		}
		c.ChangedAt = asUTC(c.ChangedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateServerTested records the build and edition seen at the last successful read of a server,
// so the home tiles stay current. ErrNotFound when the server is not registered.
func (s *Store) UpdateServerTested(ctx context.Context, serverID int64, version, edition string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	res, err := s.db.ExecContext(ctx, `UPDATE server SET last_version = ?, last_edition = ?, last_tested_at = ? WHERE id = ?`,
		version, edition, at.UTC(), serverID)
	if err != nil {
		return fmt.Errorf("update server %d: %w", serverID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// Setting reads one value of the setting table; "" when it is not set.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", key, err)
	}
	return v, nil
}

// SetSetting writes one value of the setting table, replacing what was there.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO setting (key, value) VALUES (?, ?)`, key, value); err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}

// ReplaceSeenChanges is a harvest's per-hour replace (docs/phase5f-contract.md): in one transaction,
// the server's changes seen on the server from since (UTC) on, in every database, become fresh
// (domain.DiffSeenChanges: a row already there stays with its id, one no longer recomputed goes, a new
// one is added). Rows before since, and every other row of the change log, are never touched. It
// returns how many rows were added and deleted.
func (s *Store) ReplaceSeenChanges(ctx context.Context, serverID int64, since time.Time, fresh []domain.Change) (added, deleted int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("replace seen changes: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, server_id, coalesce(db_name, ''), changed_at, object, change_type, description, statement,
		       coalesce(rollback_statement, ''), coalesce(result, '')
		FROM change
		WHERE server_id = ? AND result = ? AND changed_at >= ?`, serverID, domain.ChangeSeenOnServer, since.UTC())
	if err != nil {
		return 0, 0, fmt.Errorf("replace seen changes: %w", err)
	}
	existing := []domain.Change{}
	for rows.Next() {
		var c domain.Change
		if err = rows.Scan(&c.ID, &c.ServerID, &c.Database, &c.ChangedAt, &c.Object, &c.ChangeType,
			&c.Description, &c.Statement, &c.Rollback, &c.Result); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("replace seen changes: %w", err)
		}
		c.ChangedAt = asUTC(c.ChangedAt)
		existing = append(existing, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("replace seen changes: %w", err)
	}
	stale, add := domain.DiffSeenChanges(existing, fresh)
	for _, id := range stale {
		if _, err = tx.ExecContext(ctx, `DELETE FROM change WHERE id = ?`, id); err != nil {
			return 0, 0, fmt.Errorf("replace seen changes: %w", err)
		}
	}
	for _, c := range add {
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO change (server_id, db_name, changed_at, object, change_type, description, statement, rollback_statement, result)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
			serverID, nullIfEmpty(c.Database), c.ChangedAt.UTC(), c.Object, c.ChangeType, c.Description, c.Statement,
			domain.ChangeSeenOnServer); err != nil {
			return 0, 0, fmt.Errorf("replace seen changes: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("replace seen changes: %w", err)
	}
	return len(add), len(stale), nil
}
