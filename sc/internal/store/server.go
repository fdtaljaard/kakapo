package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"sqlmonitor/internal/domain"
)

const serverColumns = `id, name, host, auth, coalesce(username, ''), coalesce(db_name, ''), encrypt, trust_cert,
	created_at, coalesce(last_version, ''), coalesce(last_edition, ''), last_tested_at`

func scanServer(row interface{ Scan(...any) error }) (domain.Server, error) {
	var s domain.Server
	var tested sql.NullTime
	err := row.Scan(&s.ID, &s.Name, &s.Host, &s.Auth, &s.Username, &s.Database, &s.Encrypt, &s.TrustCert,
		&s.CreatedAt, &s.LastVersion, &s.LastEdition, &tested)
	// DuckDB TIMESTAMP has no zone; we always write UTC, so read it back as UTC.
	s.CreatedAt = asUTC(s.CreatedAt)
	if tested.Valid {
		t := asUTC(tested.Time)
		s.LastTestedAt = &t
	}
	return s, err
}

func asUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// ListServers returns every registered server, oldest first.
func (s *Store) ListServers(ctx context.Context) ([]domain.Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverColumns+` FROM server ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	defer rows.Close()
	out := []domain.Server{}
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("list servers: %w", err)
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// InsertServer registers a server and records the connection test that preceded it.
func (s *Store) InsertServer(ctx context.Context, in domain.ServerInput, info domain.ConnectionInfo) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// All timestamps in the file are UTC, written from Go, so they do not depend on the
	// clock settings of whoever opens the file later.
	now := time.Now().UTC()
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO server (name, host, auth, username, db_name, encrypt, trust_cert, created_at, last_version, last_edition, last_tested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+serverColumns,
		in.Name, in.Host, string(in.Auth), nullIfEmpty(in.Username), nullIfEmpty(in.Database), in.Encrypt, in.TrustCert,
		now, info.ProductVersion, info.Edition, now)
	srv, err := scanServer(row)
	if err != nil {
		return domain.Server{}, fmt.Errorf("insert server: %w", err)
	}
	return srv, nil
}

// UpdateServerDatabase remembers the database last picked on a server, so the next visit opens
// straight into it. It is also the connection's initial catalog. ErrNotFound when the server
// is not registered.
func (s *Store) UpdateServerDatabase(ctx context.Context, serverID int64, dbName string) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx, `UPDATE server SET db_name = ? WHERE id = ? RETURNING `+serverColumns,
		nullIfEmpty(dbName), serverID)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, ErrNotFound
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("update server %d: %w", serverID, err)
	}
	return srv, nil
}

// UpdateServer replaces a registration with an edited one and records the connection test that
// preceded it. The id, created_at and everything keyed by the id stay. ErrNotFound when the
// server is not registered.
func (s *Store) UpdateServer(ctx context.Context, id int64, in domain.ServerInput, info domain.ConnectionInfo) (domain.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRowContext(ctx, `
		UPDATE server SET name = ?, host = ?, auth = ?, username = ?, db_name = ?, encrypt = ?, trust_cert = ?,
			last_version = ?, last_edition = ?, last_tested_at = ?
		WHERE id = ?
		RETURNING `+serverColumns,
		in.Name, in.Host, string(in.Auth), nullIfEmpty(in.Username), nullIfEmpty(in.Database), in.Encrypt, in.TrustCert,
		info.ProductVersion, info.Edition, time.Now().UTC(), id)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Server{}, ErrNotFound
	}
	if err != nil {
		return domain.Server{}, fmt.Errorf("update server %d: %w", id, err)
	}
	return srv, nil
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
