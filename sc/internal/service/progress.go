package service

import (
	"context"
	"strings"

	"sqlmonitor/internal/collect"
)

// Progress reads the long operations in progress on the instance for one database's page
// (docs/phase4e-contract.md, part 2): the fix dialog polls it every 2 seconds while a long fix
// applies. It changes nothing and keeps nothing, and reads in one SELECT (the database is not looked
// up first: an unknown name only leaves its own requests out). Empty, never nil, when nothing runs.
func (s *Service) Progress(ctx context.Context, serverID int64, dbName string) ([]collect.Progress, error) {
	if strings.TrimSpace(dbName) == "" {
		return nil, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := collect.ReadProgress(ctx, db, dbName)
	if err != nil {
		return nil, newError(KindUnreachable, "could not read the operations in progress on %s: %v", srv.Name, err)
	}
	return rows, nil
}
