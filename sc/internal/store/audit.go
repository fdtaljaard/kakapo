package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
)

// The change audit's tables (docs/phase5w-contract.md, A3 and B4): baseline, baseline_read and action_log.
// Append-only: nothing here, or anywhere in the store, updates or deletes their rows.

// RecordBaseline keeps one read of a server's baseline: a new baseline row only when the items differ from
// the server's latest one (by hash), and always a baseline_read row pointing at the snapshot in force. A
// server's first read is reason "first" whatever was asked. It returns the snapshot's id and whether a new
// one was stored.
func (s *Store) RecordBaseline(ctx context.Context, serverID int64, f domain.BaselineFacts, readAt time.Time, reason, actionID string) (int64, bool, error) {
	hash := f.Hash()
	values, err := json.Marshal(f.Items)
	if err != nil {
		return 0, false, fmt.Errorf("store the baseline: %w", err)
	}
	errs, err := json.Marshal(f.Errors)
	if err != nil {
		return 0, false, fmt.Errorf("store the baseline: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("store the baseline: %w", err)
	}
	defer tx.Rollback()
	var latestID int64
	var latestHash string
	err = tx.QueryRowContext(ctx, `SELECT id, hash FROM baseline WHERE server_id = ? ORDER BY taken_at DESC, id DESC LIMIT 1`, serverID).
		Scan(&latestID, &latestHash)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return 0, false, fmt.Errorf("store the baseline: %w", err)
	}
	if first && reason == domain.BaselineRead {
		reason = domain.BaselineFirst
	}
	id, stored := latestID, false
	if first || latestHash != hash {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO baseline (server_id, taken_at, hash, values_json, errors_json) VALUES (?, ?, ?, ?, ?) RETURNING id`,
			serverID, readAt.UTC(), hash, string(values), string(errs)).Scan(&id); err != nil {
			return 0, false, fmt.Errorf("store the baseline: %w", err)
		}
		stored = true
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO baseline_read (server_id, read_at, baseline_id, reason, action_id, sql_memory_mb, target_memory_mb)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		serverID, readAt.UTC(), id, reason, nullIfEmpty(actionID), f.SQLMemoryMB, f.TargetMemoryMB); err != nil {
		return 0, false, fmt.Errorf("store the baseline read: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("store the baseline: %w", err)
	}
	return id, stored, nil
}

// BaselineReads lists every read of a server's baseline, oldest first.
func (s *Store) BaselineReads(ctx context.Context, serverID int64) ([]domain.BaselineReadRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT server_id, read_at, baseline_id, reason, coalesce(action_id, ''), sql_memory_mb, target_memory_mb
		FROM baseline_read WHERE server_id = ? ORDER BY read_at, baseline_id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the baseline reads: %w", err)
	}
	defer rows.Close()
	out := []domain.BaselineReadRow{}
	for rows.Next() {
		var r domain.BaselineReadRow
		var mem, target sql.NullInt64
		if err := rows.Scan(&r.ServerID, &r.ReadAt, &r.BaselineID, &r.Reason, &r.ActionID, &mem, &target); err != nil {
			return nil, fmt.Errorf("list the baseline reads: %w", err)
		}
		r.ReadAt = asUTC(r.ReadAt)
		if mem.Valid {
			r.SQLMemoryMB = &mem.Int64
		}
		if target.Valid {
			r.TargetMemoryMB = &target.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Baselines reads the snapshots of a server with these ids.
func (s *Store) Baselines(ctx context.Context, serverID int64, ids []int64) (map[int64]domain.Baseline, error) {
	out := map[int64]domain.Baseline{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{serverID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, server_id, taken_at, hash, values_json, errors_json FROM baseline
		WHERE server_id = ? AND id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read the baselines: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var b domain.Baseline
		var values, errs string
		if err := rows.Scan(&b.ID, &b.ServerID, &b.TakenAt, &b.Hash, &values, &errs); err != nil {
			return nil, fmt.Errorf("read the baselines: %w", err)
		}
		b.TakenAt = asUTC(b.TakenAt)
		if err := json.Unmarshal([]byte(values), &b.Items); err != nil {
			return nil, fmt.Errorf("read the baseline %d: %w", b.ID, err)
		}
		if err := json.Unmarshal([]byte(errs), &b.Errors); err != nil {
			return nil, fmt.Errorf("read the baseline %d: %w", b.ID, err)
		}
		if b.Items == nil {
			b.Items = map[string]*string{}
		}
		if b.Errors == nil {
			b.Errors = map[string]string{}
		}
		out[b.ID] = b
	}
	return out, rows.Err()
}

// InsertActionLog writes one row of kakapo's own log. It never updates one.
func (s *Store) InsertActionLog(ctx context.Context, r domain.ActionLogRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ended any
	if r.EndedAt != nil {
		ended = r.EndedAt.UTC()
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO action_log (action_id, phase, server_id, logged_at, started_at, ended_at, duration_ms, windows_user, sql_login,
		  kakapo_host, kakapo_version, db_name, object, change_type, fix_id, description, statement, rollback_statement,
		  value_before, value_after, outcome, result, change_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ActionID, r.Phase, r.ServerID, r.LoggedAt.UTC(), r.StartedAt.UTC(), ended, r.DurationMs, r.WindowsUser, r.SQLLogin,
		r.KakapoHost, r.KakapoVersion, nullIfEmpty(r.Database), r.Object, r.ChangeType, r.FixID, r.Description, r.Statement,
		nullIfEmpty(r.Rollback), nullIfEmpty(r.ValueBefore), nullIfEmpty(r.ValueAfter), nullIfEmpty(r.Outcome),
		nullIfEmpty(r.Result), nullIfEmpty(r.ChangeIDs)); err != nil {
		return fmt.Errorf("write kakapo's log: %w", err)
	}
	return nil
}

// ListActionLog lists every row of kakapo's own log for a server, oldest first.
func (s *Store) ListActionLog(ctx context.Context, serverID int64) ([]domain.ActionLogRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, action_id, phase, server_id, logged_at, started_at, ended_at, duration_ms, windows_user, sql_login, kakapo_host,
		       kakapo_version, coalesce(db_name, ''), object, change_type, fix_id, description, statement,
		       coalesce(rollback_statement, ''), coalesce(value_before, ''), coalesce(value_after, ''), coalesce(outcome, ''),
		       coalesce(result, ''), coalesce(change_ids, '')
		FROM action_log WHERE server_id = ? ORDER BY started_at, id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list kakapo's log: %w", err)
	}
	defer rows.Close()
	out := []domain.ActionLogRow{}
	for rows.Next() {
		var r domain.ActionLogRow
		var ended sql.NullTime
		var dur sql.NullInt64
		if err := rows.Scan(&r.ID, &r.ActionID, &r.Phase, &r.ServerID, &r.LoggedAt, &r.StartedAt, &ended, &dur, &r.WindowsUser,
			&r.SQLLogin, &r.KakapoHost, &r.KakapoVersion, &r.Database, &r.Object, &r.ChangeType, &r.FixID, &r.Description,
			&r.Statement, &r.Rollback, &r.ValueBefore, &r.ValueAfter, &r.Outcome, &r.Result, &r.ChangeIDs); err != nil {
			return nil, fmt.Errorf("list kakapo's log: %w", err)
		}
		r.LoggedAt, r.StartedAt = asUTC(r.LoggedAt), asUTC(r.StartedAt)
		if ended.Valid {
			t := asUTC(ended.Time)
			r.EndedAt = &t
		}
		if dur.Valid {
			r.DurationMs = &dur.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAuditChanges lists every change of a server that is not one of kakapo's own fixes (those are in
// action_log): seen on the server and recorded by hand, oldest first. No limit: the screen pages.
func (s *Store) ListAuditChanges(ctx context.Context, serverID int64) ([]domain.Change, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, server_id, coalesce(db_name, ''), changed_at, object, change_type, description, statement,
		       coalesce(rollback_statement, ''), coalesce(result, '')
		FROM change
		WHERE server_id = ? AND result IN (?, ?)
		ORDER BY changed_at, id`, serverID, domain.ChangeSeenOnServer, domain.ChangeRecordedByHand)
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

// QueryStoreReadOnlyChanges are the times a database's Query Store went read-only between two of the stored
// db_query_store snapshots (databases whose page was opened), as setting changes for C3.
func (s *Store) QueryStoreReadOnlyChanges(ctx context.Context, serverID int64) ([]domain.SettingChange, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT db_name, prev_at, captured_at, prev_state, actual_state FROM (
		  SELECT db_name, captured_at, actual_state,
		         lag(actual_state) OVER (PARTITION BY db_name ORDER BY captured_at) AS prev_state,
		         lag(captured_at) OVER (PARTITION BY db_name ORDER BY captured_at) AS prev_at
		  FROM db_query_store WHERE server_id = ?)
		WHERE actual_state = 'READ_ONLY' AND prev_state IS NOT NULL AND prev_state <> 'READ_ONLY'
		ORDER BY captured_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("read the Query Store snapshots: %w", err)
	}
	defer rows.Close()
	out := []domain.SettingChange{}
	for rows.Next() {
		var db, from, to string
		var before, after time.Time
		if err := rows.Scan(&db, &before, &after, &from, &to); err != nil {
			return nil, fmt.Errorf("read the Query Store snapshots: %w", err)
		}
		out = append(out, domain.SettingChange{Item: "db/" + db + "/query_store_state", Kind: domain.SettingChanged, From: from, To: to,
			Before: asUTC(before), After: asUTC(after)})
	}
	return out, rows.Err()
}

// HasActions says whether kakapo's own log holds an action on a server (CHG02): a started or a copied row.
func (s *Store) HasActions(ctx context.Context, serverID int64) (bool, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM action_log WHERE server_id = ? AND phase IN ('started', 'copied')`, serverID).Scan(&n); err != nil {
		return false, fmt.Errorf("read kakapo's log: %w", err)
	}
	return n > 0, nil
}
