package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sqlmonitor/internal/domain"
)

// InsertCheckdbRun keeps one DBCC CHECKDB run from the app (checkdb-now, docs/phase4d-contract.md).
// History is kept; nothing is deleted.
func (s *Store) InsertCheckdbRun(ctx context.Context, serverID int64, dbName string, r domain.CheckdbRun) error {
	msgs := r.Messages
	if msgs == nil {
		msgs = []string{}
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("store the CHECKDB run of %s: %w", dbName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO checkdb_run (server_id, db_name, ran_at, duration_seconds, physical_only, clean, outcome, messages_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, dbName, r.RanAt.UTC(), r.DurationSeconds, r.PhysicalOnly, r.Clean, r.Outcome, string(b))
	if err != nil {
		return fmt.Errorf("store the CHECKDB run of %s: %w", dbName, err)
	}
	return nil
}

// LastCheckdbRun returns the latest CHECKDB run from the app on a database, or nil when none ran.
func (s *Store) LastCheckdbRun(ctx context.Context, serverID int64, dbName string) (*domain.CheckdbRun, error) {
	var (
		r    domain.CheckdbRun
		msgs string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT ran_at, duration_seconds, physical_only, clean, outcome, messages_json
		FROM checkdb_run
		WHERE server_id = ? AND db_name = ?
		ORDER BY ran_at DESC, id DESC
		LIMIT 1`, serverID, dbName).Scan(&r.RanAt, &r.DurationSeconds, &r.PhysicalOnly, &r.Clean, &r.Outcome, &msgs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the CHECKDB run of %s: %w", dbName, err)
	}
	r.RanAt = r.RanAt.UTC()
	r.Messages = []string{}
	if err := json.Unmarshal([]byte(msgs), &r.Messages); err != nil {
		return nil, fmt.Errorf("read the CHECKDB run of %s: %w", dbName, err)
	}
	return &r, nil
}
