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

// InsertStorageSamples keeps what one storage read saw: a volume_sample row per volume and a
// storage_sample row for the database (when sample is not nil), in one transaction, all at the
// same capturedAt (UTC). Nothing is deleted.
func (s *Store) InsertStorageSamples(ctx context.Context, serverID int64, dbName string, capturedAt time.Time,
	volumes []domain.VolumeSample, sample *domain.StorageSample) error {
	if len(volumes) == 0 && sample == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	at := capturedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store the storage samples of %s: %w", dbName, err)
	}
	defer tx.Rollback()

	for _, v := range volumes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO volume_sample (server_id, captured_at, volume, free_mb, total_mb) VALUES (?, ?, ?, ?, ?)`,
			serverID, at, v.Volume, v.FreeMB, v.TotalMB); err != nil {
			return fmt.Errorf("store the sample of volume %s: %w", v.Volume, err)
		}
	}
	if sample != nil {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO storage_sample (server_id, db_name, captured_at, data_alloc_mb, data_used_mb, log_size_mb, log_used_pct,
			                            tempdb_alloc_mb, tempdb_used_mb)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serverID, dbName, at, nullInt(sample.DataAllocMB), nullInt(sample.DataUsedMB), nullInt(sample.LogSizeMB),
			nullFloat(sample.LogUsedPct), nullInt(sample.TempdbAllocMB), nullInt(sample.TempdbUsedMB)); err != nil {
			return fmt.Errorf("store the storage sample of %s: %w", dbName, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store the storage samples of %s: %w", dbName, err)
	}
	return nil
}

// DiskTrend returns the pair of samples of one volume of a server that STO12 forecasts from: the
// newest, and the oldest within the 30 days before it (the newest itself when it is the only one).
// nil when the volume has no samples.
func (s *Store) DiskTrend(ctx context.Context, serverID int64, volume string) (*domain.DiskTrend, error) {
	t := domain.DiskTrend{Volume: volume}
	err := s.db.QueryRowContext(ctx, `
		SELECT captured_at, free_mb FROM volume_sample
		WHERE server_id = ? AND volume = ?
		ORDER BY captured_at DESC LIMIT 1`, serverID, volume).Scan(&t.NewestAt, &t.NewestFreeMB)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the samples of volume %s: %w", volume, err)
	}
	t.NewestAt = asUTC(t.NewestAt)
	if err := s.db.QueryRowContext(ctx, `
		SELECT captured_at, free_mb FROM volume_sample
		WHERE server_id = ? AND volume = ? AND captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at LIMIT 1`, serverID, volume, t.NewestAt.Add(-30*24*time.Hour), t.NewestAt).
		Scan(&t.OldestAt, &t.OldestFreeMB); err != nil {
		return nil, fmt.Errorf("read the samples of volume %s: %w", volume, err)
	}
	t.OldestAt = asUTC(t.OldestAt)
	return &t, nil
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// InsertPerfSample keeps one live sample of the Performance card (docs/phase4c-contract.md, part 3)
// as JSON. History is kept; nothing is deleted.
func (s *Store) InsertPerfSample(ctx context.Context, serverID int64, dbName string, p domain.PerfSample) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("store the sample of %s: %w", dbName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO perf_sample (server_id, db_name, captured_at, seconds, verdict, sample_json) VALUES (?, ?, ?, ?, ?, ?)`,
		serverID, dbName, p.CapturedAt.UTC(), p.Seconds, p.Verdict, string(raw)); err != nil {
		return fmt.Errorf("store the sample of %s: %w", dbName, err)
	}
	return nil
}

// LastPerfSample returns the latest live sample of a database, or nil when none was run.
func (s *Store) LastPerfSample(ctx context.Context, serverID int64, dbName string) (*domain.PerfSample, error) {
	var raw string
	var at time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT captured_at, sample_json FROM perf_sample
		WHERE server_id = ? AND db_name = ?
		ORDER BY captured_at DESC, id DESC
		LIMIT 1`, serverID, dbName).Scan(&at, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the sample of %s: %w", dbName, err)
	}
	var p domain.PerfSample
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("read the sample of %s: %w", dbName, err)
	}
	p.CapturedAt = asUTC(at)
	return &p, nil
}
