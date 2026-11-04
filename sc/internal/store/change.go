package store

import (
	"context"
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
