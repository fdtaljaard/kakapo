package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// BlockingHistory runs BLK01 now (docs/phase5d-contract.md): reads the blocking of the last days (1 to
// 7; 0 means 7) from the system_health session, groups this database's by the head blocker's statement
// and object, stores the report and returns it. Bounded like the deadlock report (reading the session
// files can take a minute). KindInvalid for days out of range, KindNotFound for an unknown server or
// database, KindUnreachable when the server or the session cannot be read (the message says why).
func (s *Service) BlockingHistory(ctx context.Context, serverID int64, dbName string, days int) (domain.BlockingHistory, error) {
	if days == 0 {
		days = domain.BlockingHistoryDays
	}
	if days < 1 || days > domain.BlockingHistoryDays {
		return domain.BlockingHistory{}, newError(KindInvalid, "days must be 1 to %d", domain.BlockingHistoryDays)
	}
	if strings.TrimSpace(dbName) == "" {
		return domain.BlockingHistory{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.BlockingHistory{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.BlockingHistory{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.BlockingHistory{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}

	readCtx, cancel := context.WithTimeout(ctx, deadlockReadTimeout)
	defer cancel()
	start := time.Now()
	in, err := collect.ReadBlockingHistory(readCtx, db, d.Name, days)
	if err != nil {
		msg := err.Error()
		if lower := strings.ToLower(msg); strings.Contains(lower, "permission") || strings.Contains(lower, "denied") {
			msg += " (reading the system_health session needs VIEW SERVER STATE)"
		}
		return domain.BlockingHistory{}, newError(KindUnreachable, "could not read the blocking history from the system_health session on %s: %s", srv.Name, msg)
	}
	in.CapturedAt, in.Duration = time.Now().UTC().Truncate(time.Microsecond), time.Since(start)
	h := domain.BuildBlockingHistory(in)
	if err := s.store.InsertBlockingHistory(ctx, serverID, d.Name, h); err != nil {
		return domain.BlockingHistory{}, err
	}
	return h, nil
}
