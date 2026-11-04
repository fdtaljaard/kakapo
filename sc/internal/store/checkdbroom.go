package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

// InsertCheckdbRoom keeps one STO10 result. History is kept; nothing is deleted.
func (s *Store) InsertCheckdbRoom(ctx context.Context, serverID int64, dbName string, r domain.CheckdbRoom) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO checkdb_room (server_id, db_name, captured_at, duration_seconds, estimate_kb, need_mb, tempdb_free_mb,
		                          tempdb_room_mb, data_volume, data_free_mb, data_size_mb, severity, finding, evidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, dbName, r.CapturedAt.UTC(), r.DurationSeconds, r.EstimateKB, r.NeedMB, r.TempdbFreeMB,
		r.TempdbRoomMB, r.DataVolume, r.DataFreeMB, r.DataSizeMB, string(r.Finding.Severity), r.Finding.Finding,
		nullIfEmpty(r.Finding.Evidence))
	if err != nil {
		return fmt.Errorf("store the CHECKDB room of %s: %w", dbName, err)
	}
	return nil
}

// LastCheckdbRoom returns the latest STO10 result of a database, or nil when it was never run.
// The finding is built again from the stored numbers; its severity, sentence and evidence are the
// ones stored.
func (s *Store) LastCheckdbRoom(ctx context.Context, serverID int64, dbName string) (*domain.CheckdbRoom, error) {
	var (
		in                domain.CheckdbRoomInputs
		seconds           sql.NullFloat64
		volume, evidence  sql.NullString
		severity, finding string
		est, tFree, tRoom sql.NullInt64
		dFree, dSize      sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT captured_at, duration_seconds, estimate_kb, tempdb_free_mb, tempdb_room_mb, data_volume, data_free_mb,
		       data_size_mb, severity, finding, evidence
		FROM checkdb_room
		WHERE server_id = ? AND db_name = ?
		ORDER BY captured_at DESC, id DESC
		LIMIT 1`, serverID, dbName).Scan(&in.CapturedAt, &seconds, &est, &tFree, &tRoom, &volume, &dFree, &dSize,
		&severity, &finding, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the CHECKDB room of %s: %w", dbName, err)
	}
	in.Database = dbName
	in.CapturedAt = in.CapturedAt.UTC()
	in.Duration = time.Duration(seconds.Float64 * float64(time.Second))
	in.EstimateKB, in.TempdbFreeMB, in.TempdbRoomMB = est.Int64, tFree.Int64, tRoom.Int64
	in.DataVolume, in.DataFreeMB, in.DataSizeMB = volume.String, dFree.Int64, dSize.Int64

	r := domain.BuildCheckdbRoom(in)
	r.DurationSeconds = seconds.Float64
	r.Finding.Severity, r.Finding.Finding, r.Finding.Evidence = domain.Severity(severity), finding, evidence.String
	return &r, nil
}
