package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"sqlmonitor/internal/domain"
)

// InsertDeadlockReport keeps one deadlock report (DLK01) with its pairs and findings as JSON.
// History is kept; nothing is deleted.
func (s *Store) InsertDeadlockReport(ctx context.Context, serverID int64, dbName string, r domain.DeadlockReport) error {
	pairs, err := json.Marshal(r.Pairs)
	if err != nil {
		return fmt.Errorf("store the deadlock report of %s: %w", dbName, err)
	}
	findings, err := json.Marshal(r.Findings)
	if err != nil {
		return fmt.Errorf("store the deadlock report of %s: %w", dbName, err)
	}
	health, err := json.Marshal(r.Health)
	if err != nil {
		return fmt.Errorf("store the deadlock report of %s: %w", dbName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO deadlock_report (server_id, db_name, captured_at, duration_seconds, days, total, source, pairs_json, findings_json, health_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, dbName, r.CapturedAt.UTC(), r.DurationSeconds, r.Days, r.Total, r.Source, string(pairs), string(findings), string(health))
	if err != nil {
		return fmt.Errorf("store the deadlock report of %s: %w", dbName, err)
	}
	return nil
}

// LastDeadlockReport returns the latest deadlock report of a database, or nil when it was never run.
func (s *Store) LastDeadlockReport(ctx context.Context, serverID int64, dbName string) (*domain.DeadlockReport, error) {
	var (
		r               domain.DeadlockReport
		seconds         sql.NullFloat64
		pairs, findings string
		health          sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT captured_at, duration_seconds, days, total, source, pairs_json, findings_json, health_json
		FROM deadlock_report
		WHERE server_id = ? AND db_name = ?
		ORDER BY captured_at DESC, id DESC
		LIMIT 1`, serverID, dbName).Scan(&r.CapturedAt, &seconds, &r.Days, &r.Total, &r.Source, &pairs, &findings, &health)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the deadlock report of %s: %w", dbName, err)
	}
	r.CapturedAt, r.DurationSeconds = r.CapturedAt.UTC(), seconds.Float64
	r.Pairs, r.Findings = []domain.DeadlockPair{}, []domain.Finding{}
	if err := json.Unmarshal([]byte(pairs), &r.Pairs); err != nil {
		return nil, fmt.Errorf("read the deadlock pairs of %s: %w", dbName, err)
	}
	// A report read before phase 5m has no withReader in its pairs: its readers stay unknown (nil).
	if len(r.Pairs) == 0 || strings.Contains(pairs, `"withReader"`) {
		r.CountReaders()
	}
	if err := json.Unmarshal([]byte(findings), &r.Findings); err != nil {
		return nil, fmt.Errorf("read the deadlock findings of %s: %w", dbName, err)
	}
	if health.Valid && health.String != "" && health.String != "null" {
		if err := json.Unmarshal([]byte(health.String), &r.Health); err != nil {
			return nil, fmt.Errorf("read the health events of %s: %w", dbName, err)
		}
	}
	return &r, nil
}
