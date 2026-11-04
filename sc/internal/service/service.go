// Package service holds the use cases. It is the only entry point for the API,
// and later for the MCP server and the in-app assistant.
package service

import (
	"context"
	"errors"
	"fmt"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/cred"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Service wires the store, the credential store and the SQL Server side together.
type Service struct {
	store *store.Store
	creds cred.Store
	runs  *runRegistry // the long fixes running in the background (docs/phase4f-contract.md)
}

// New builds the service.
func New(st *store.Store, creds cred.Store) *Service {
	return &Service{store: st, creds: creds, runs: newRunRegistry()}
}

// ListServers returns the registered servers. An empty list means first run.
func (s *Service) ListServers(ctx context.Context) ([]domain.Server, error) {
	return s.store.ListServers(ctx)
}

// GetServer returns one registration; KindNotFound when there is none.
func (s *Service) GetServer(ctx context.Context, id int64) (domain.Server, error) {
	srv, err := s.store.GetServer(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Server{}, newError(KindNotFound, "server %d is not registered", id)
	}
	return srv, err
}

// TestConnection validates the input and connects once. A SQL login with an empty password and
// an id is tested with that server's stored password, so the edit form can test unchanged
// credentials.
func (s *Service) TestConnection(ctx context.Context, in domain.ServerInput) (domain.ConnectionInfo, error) {
	if err := in.Validate(); err != nil {
		return domain.ConnectionInfo{}, err
	}
	if err := s.resolvePassword(&in); err != nil {
		return domain.ConnectionInfo{}, err
	}
	return collect.Test(ctx, in)
}

// resolvePassword fills in the stored password of server in.ID when the input is a SQL login
// with an empty password. The stored password never leaves the service.
func (s *Service) resolvePassword(in *domain.ServerInput) error {
	if in.Auth != domain.AuthSQL || in.Password != "" || in.ID <= 0 {
		return nil
	}
	pw, err := s.creds.Get(in.ID)
	if err != nil || pw == "" {
		return newError(KindUnreachable, "no stored password for %s; type the password", in.Username)
	}
	in.Password = pw
	return nil
}

// UpdateServer edits a registration. It tests the resulting credentials first and changes
// nothing when that fails. An empty password on a SQL login keeps the stored one; a typed one
// replaces it; switching to Windows authentication deletes it. The id, and so every snapshot
// and change recorded under it, stays.
func (s *Service) UpdateServer(ctx context.Context, id int64, in domain.ServerInput) (domain.Server, error) {
	if err := in.Validate(); err != nil {
		return domain.Server{}, newError(KindInvalid, "%v", err)
	}
	if _, err := s.GetServer(ctx, id); err != nil {
		return domain.Server{}, err
	}
	in.ID = id
	typed := in.Password != ""
	if err := s.resolvePassword(&in); err != nil {
		return domain.Server{}, err
	}
	info, err := collect.Test(ctx, in)
	if err != nil {
		return domain.Server{}, newError(KindUnreachable, "%v", err)
	}
	srv, err := s.store.UpdateServer(ctx, id, in, info)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Server{}, newError(KindNotFound, "server %d is not registered", id)
	}
	if err != nil {
		return domain.Server{}, err
	}
	switch {
	case in.Auth == domain.AuthSQL && typed:
		if err := s.creds.Set(id, in.Password); err != nil {
			return srv, fmt.Errorf("server saved, but the password could not be stored: %w", err)
		}
	case in.Auth == domain.AuthWindows:
		if err := s.creds.Delete(id); err != nil {
			return srv, fmt.Errorf("server saved, but the old password could not be removed: %w", err)
		}
	}
	return srv, nil
}

// RegisterServer tests the connection, then saves the server. A server that cannot be
// reached is not saved, so the registry never holds an entry that was never proven.
func (s *Service) RegisterServer(ctx context.Context, in domain.ServerInput) (domain.Server, error) {
	if err := in.Validate(); err != nil {
		return domain.Server{}, err
	}
	if in.Auth == domain.AuthSQL && in.Password == "" {
		return domain.Server{}, errors.New("a SQL login needs a password")
	}
	info, err := collect.Test(ctx, in)
	if err != nil {
		return domain.Server{}, err
	}
	srv, err := s.store.InsertServer(ctx, in, info)
	if err != nil {
		return domain.Server{}, err
	}
	if in.Auth == domain.AuthSQL && in.Password != "" {
		if err := s.creds.Set(srv.ID, in.Password); err != nil {
			return srv, fmt.Errorf("server saved, but the password could not be stored: %w", err)
		}
	}
	return srv, nil
}
