package store

import (
	"context"
	"database/sql"
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

// LastFindings returns the findings of one check in the latest stored read of a card of a database
// (docs/phase5o-contract.md, section 5: the query page reads IDX04 from the Design card's last read,
// never a second read of the server). Empty when the card was never read or the check found nothing.
// Fields the store does not keep (the fix) are left empty.
func (s *Store) LastFindings(ctx context.Context, serverID int64, dbName string, card domain.CardID, checkID string) ([]domain.Finding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.check_id, f.severity, f.owner, f.when_, f.effort, f.object, f.finding, f.why, f.do_this, f.then_check, f.evidence
		FROM finding AS f
		WHERE f.check_id = ? AND f.card_read_id = (
			SELECT id FROM card_read WHERE server_id = ? AND db_name = ? AND card = ? ORDER BY captured_at DESC, id DESC LIMIT 1)`,
		checkID, serverID, dbName, string(card))
	if err != nil {
		return nil, fmt.Errorf("read the last %s findings of %s: %w", checkID, dbName, err)
	}
	defer rows.Close()
	out := []domain.Finding{}
	for rows.Next() {
		var f domain.Finding
		var severity string
		var owner, when, effort, object, finding, why, do, then, evidence sql.NullString
		if err := rows.Scan(&f.CheckID, &severity, &owner, &when, &effort, &object, &finding, &why, &do, &then, &evidence); err != nil {
			return nil, fmt.Errorf("read the last %s findings of %s: %w", checkID, dbName, err)
		}
		f.Severity = domain.Severity(severity)
		f.Owner, f.When, f.Effort, f.Object, f.Finding = owner.String, when.String, effort.String, object.String, finding.String
		f.Why, f.DoThis, f.ThenCheck, f.Evidence = why.String, do.String, then.String, evidence.String
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the last %s findings of %s: %w", checkID, dbName, err)
	}
	return out, nil
}
