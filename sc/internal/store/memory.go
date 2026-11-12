package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// Memory screen snapshots (docs/phase5v-contract.md, decision 4): one row per read, kept for ever. An
// instance snapshot holds the facts of panels 2 to 4 (domain.MemorySnapshot); a database snapshot holds
// Database detail as built (domain.DatabaseMemory).

const (
	MemoryKindInstance = "instance"
	MemoryKindDatabase = "database"
)

// InsertMemorySnapshot keeps one snapshot and returns its id. dbName is "" for an instance snapshot.
func (s *Store) InsertMemorySnapshot(ctx context.Context, serverID int64, kind, dbName string, takenAt time.Time, v any) (int64, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return 0, fmt.Errorf("store the memory snapshot: %w", err)
	}
	var db any
	if dbName != "" {
		db = dbName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO memory_snapshot (server_id, taken_at, kind, db_name, snapshot_json)
		VALUES (?, ?, ?, ?, ?) RETURNING id`,
		serverID, takenAt.UTC(), kind, db, string(body)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store the memory snapshot: %w", err)
	}
	return id, nil
}

// ListMemorySnapshots lists the snapshots of one kind (and database, for kind database), newest first, at
// most limit.
func (s *Store) ListMemorySnapshots(ctx context.Context, serverID int64, kind, dbName string, limit int) ([]domain.MemorySnapshotInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, taken_at, kind, coalesce(db_name, ''), snapshot_json LIKE '%"buffers":{%'
		FROM memory_snapshot
		WHERE server_id = ? AND kind = ? AND coalesce(db_name, '') = ?
		ORDER BY taken_at DESC, id DESC
		LIMIT ?`, serverID, kind, dbName, limit)
	if err != nil {
		return nil, fmt.Errorf("list the memory snapshots: %w", err)
	}
	defer rows.Close()
	out := []domain.MemorySnapshotInfo{}
	for rows.Next() {
		var m domain.MemorySnapshotInfo
		if err := rows.Scan(&m.ID, &m.TakenAt, &m.Kind, &m.Database, &m.Databases); err != nil {
			return nil, fmt.Errorf("list the memory snapshots: %w", err)
		}
		m.TakenAt = m.TakenAt.UTC()
		if kind == MemoryKindDatabase {
			m.Databases = false
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MemorySnapshot reads one snapshot of a server into v; ErrNotFound when there is none of that kind.
func (s *Store) MemorySnapshot(ctx context.Context, serverID, id int64, kind string, v any) (domain.MemorySnapshotInfo, error) {
	var m domain.MemorySnapshotInfo
	var body string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, taken_at, kind, coalesce(db_name, ''), snapshot_json
		FROM memory_snapshot WHERE server_id = ? AND id = ? AND kind = ?`, serverID, id, kind).
		Scan(&m.ID, &m.TakenAt, &m.Kind, &m.Database, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, fmt.Errorf("read the memory snapshot: %w", err)
	}
	m.TakenAt = m.TakenAt.UTC()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		return m, fmt.Errorf("read the memory snapshot %d: %w", id, err)
	}
	return m, nil
}
