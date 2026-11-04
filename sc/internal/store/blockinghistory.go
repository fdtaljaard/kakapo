package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sqlmonitor/internal/domain"
)

// InsertBlockingHistory keeps one blocking history (BLK01, docs/phase5d-contract.md) as JSON, one row
// per read. History is kept; nothing is deleted.
func (s *Store) InsertBlockingHistory(ctx context.Context, serverID int64, dbName string, h domain.BlockingHistory) error {
	report, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("store the blocking history of %s: %w", dbName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO blocking_report (server_id, db_name, captured_at, days, source, leaders, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		serverID, dbName, h.CapturedAt.UTC(), h.Days, h.Source, len(h.Leaders), string(report))
	if err != nil {
		return fmt.Errorf("store the blocking history of %s: %w", dbName, err)
	}
	return nil
}

// LastBlockingHistory returns the latest blocking history of a database, or nil when it was never read.
func (s *Store) LastBlockingHistory(ctx context.Context, serverID int64, dbName string) (*domain.BlockingHistory, error) {
	var report string
	err := s.db.QueryRowContext(ctx, `
		SELECT report_json FROM blocking_report
		WHERE server_id = ? AND db_name = ?
		ORDER BY captured_at DESC, id DESC
		LIMIT 1`, serverID, dbName).Scan(&report)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the blocking history of %s: %w", dbName, err)
	}
	var h domain.BlockingHistory
	if err := json.Unmarshal([]byte(report), &h); err != nil {
		return nil, fmt.Errorf("read the blocking history of %s: %w", dbName, err)
	}
	h.CapturedAt = h.CapturedAt.UTC()
	if h.Leaders == nil {
		h.Leaders = []domain.BlockingLeader{}
	}
	if h.ByHour == nil {
		h.ByHour = []domain.BlockingHour{}
	}
	if h.Findings == nil {
		h.Findings = []domain.Finding{}
	}
	if h.Notes == nil {
		h.Notes = []string{}
	}
	return &h, nil
}
