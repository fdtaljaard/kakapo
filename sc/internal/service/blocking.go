package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// blockingReadTimeout bounds one read of the chain; the page polls it every 3 seconds while it watches.
const blockingReadTimeout = 30 * time.Second

// Blocking reads the blocking chain now, across the instance, for the blocking page of one database
// (docs/phase4c-contract.md, part 2). It changes nothing and keeps nothing: the page's watch polls it.
func (s *Service) Blocking(ctx context.Context, serverID int64, dbName string) (domain.BlockingTree, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.BlockingTree{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.BlockingTree{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.BlockingTree{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.BlockingTree{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	readCtx, cancel := context.WithTimeout(ctx, blockingReadTimeout)
	defer cancel()
	f, err := collect.ReadBlocking(readCtx, db, d.Name)
	if err != nil {
		return domain.BlockingTree{}, newError(KindUnreachable, "could not read the blocking chain on %s: %v", srv.Name, err)
	}
	if f.ServerName == "" {
		f.ServerName = srv.Name
	}
	return domain.BuildBlockingTree(f, time.Now().UTC().Truncate(time.Millisecond)), nil
}
