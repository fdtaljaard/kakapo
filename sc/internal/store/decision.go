package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// InsertDecision keeps one decision record (docs/phase5m-contract.md, section 6) and returns it with its
// id. A record is never edited: a new one replaces it as the latest.
func (s *Store) InsertDecision(ctx context.Context, r domain.DecisionRecord) (domain.DecisionRecord, error) {
	var review any
	if r.ReviewOn != "" {
		t, err := time.Parse("2006-01-02", r.ReviewOn)
		if err != nil {
			return r, fmt.Errorf("store the decision on %s: review date %q: %w", r.Database, r.ReviewOn, err)
		}
		review = t
	}
	var reason, change any
	if r.Reason != "" {
		reason = r.Reason
	}
	if r.ChangeID != nil {
		change = *r.ChangeID
	}
	r.DecidedAt = r.DecidedAt.UTC().Truncate(time.Microsecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO decision (server_id, db_name, decision, decided_at, decided_by, choice, verdict, reason, review_on, change_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		r.ServerID, r.Database, r.Decision, r.DecidedAt, r.DecidedBy, r.Choice, r.Verdict, reason, review, change).Scan(&r.ID)
	if err != nil {
		return r, fmt.Errorf("store the decision on %s: %w", r.Database, err)
	}
	return r, nil
}

// ListDecisions returns the records of one decision on a database, newest first.
func (s *Store) ListDecisions(ctx context.Context, serverID int64, dbName, decision string) ([]domain.DecisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, server_id, db_name, decision, decided_at, decided_by, choice, verdict, reason, review_on, change_id
		FROM decision
		WHERE server_id = ? AND db_name = ? AND decision = ?
		ORDER BY decided_at DESC, id DESC`, serverID, dbName, decision)
	if err != nil {
		return nil, fmt.Errorf("read the decisions on %s: %w", dbName, err)
	}
	defer rows.Close()
	out := []domain.DecisionRecord{}
	for rows.Next() {
		var (
			r      domain.DecisionRecord
			reason sql.NullString
			review sql.NullTime
			change sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.ServerID, &r.Database, &r.Decision, &r.DecidedAt, &r.DecidedBy, &r.Choice, &r.Verdict,
			&reason, &review, &change); err != nil {
			return nil, fmt.Errorf("read the decisions on %s: %w", dbName, err)
		}
		r.DecidedAt = r.DecidedAt.UTC()
		r.Reason = reason.String
		if review.Valid {
			r.ReviewOn = review.Time.Format("2006-01-02")
		}
		if change.Valid {
			id := change.Int64
			r.ChangeID = &id
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the decisions on %s: %w", dbName, err)
	}
	return out, nil
}
