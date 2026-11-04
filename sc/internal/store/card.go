package store

import (
	"context"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// InsertCardRead keeps one read of a live card with its findings and check statuses, in one
// transaction, and returns its id. captured_at is now, in UTC. History is kept; nothing is deleted.
func (s *Store) InsertCardRead(ctx context.Context, serverID int64, dbName string, card domain.Card,
	findings []domain.Finding, checks []domain.CheckStatus) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store the %s card of %s: %w", card.ID, dbName, err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO card_read (server_id, db_name, card, captured_at, status, status_text, verdict, to_do)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		serverID, dbName, string(card.ID), time.Now().UTC(), card.Status, card.StatusText, nullIfEmpty(card.Verdict), card.ToDo).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store the %s card of %s: %w", card.ID, dbName, err)
	}
	for _, f := range findings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO finding (card_read_id, check_id, severity, owner, when_, effort, object, finding, why, do_this, then_check, evidence, clock)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.CheckID, string(f.Severity), f.Owner, f.When, f.Effort, f.Object, f.Finding, f.Why, f.DoThis, f.ThenCheck, f.Evidence,
			nullIfEmpty(f.Clock)); err != nil {
			return 0, fmt.Errorf("store finding %s of %s: %w", f.CheckID, dbName, err)
		}
	}
	for _, c := range checks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO check_status (card_read_id, check_id, status, note) VALUES (?, ?, ?, ?)`,
			id, c.CheckID, c.Status, nullIfEmpty(c.Note)); err != nil {
			return 0, fmt.Errorf("store check %s of %s: %w", c.CheckID, dbName, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store the %s card of %s: %w", card.ID, dbName, err)
	}
	return id, nil
}
