package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// checkdbEstimateTimeout bounds the estimate; it reads metadata only, but on a big database it can
// take a while.
const checkdbEstimateTimeout = 10 * time.Minute

// CheckdbRoom runs STO10 now (docs/phase3b-contract.md): asks SQL Server for its CHECKDB estimate,
// compares it with the room in tempdb and on the data volume, stores the result and returns it.
// KindNotFound for an unknown server or database, KindConflict when the database is not online,
// KindUnreachable when the server cannot be read or the DBCC fails (the message says why).
func (s *Service) CheckdbRoom(ctx context.Context, serverID int64, dbName string) (domain.CheckdbRoom, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.CheckdbRoom{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.CheckdbRoom{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.CheckdbRoom{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.CheckdbRoom{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.CheckdbRoom{}, newError(KindConflict, "%s is %s, not online, so CHECKDB cannot be estimated", d.Name, d.State)
	}

	facts := readStorageFacts(ctx, db, *d)
	in, err := domain.CheckdbRoomInputsFrom(facts)
	if err != nil {
		return domain.CheckdbRoom{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}

	estCtx, cancel := context.WithTimeout(ctx, checkdbEstimateTimeout)
	defer cancel()
	kb, took, err := collect.RunCheckdbEstimate(estCtx, db, d.Name)
	if err != nil {
		msg := err.Error()
		if lower := strings.ToLower(msg); strings.Contains(lower, "permission") || strings.Contains(lower, "denied") {
			msg += " (the estimate needs db_owner of the database or sysadmin)"
		}
		return domain.CheckdbRoom{}, newError(KindUnreachable, "DBCC CHECKDB WITH ESTIMATEONLY on %s failed: %s", d.Name, msg)
	}
	in.EstimateKB, in.Duration, in.CapturedAt = kb, took, time.Now().UTC().Truncate(time.Microsecond)

	room := domain.BuildCheckdbRoom(in)
	if err := s.store.InsertCheckdbRoom(ctx, serverID, d.Name, room); err != nil {
		return domain.CheckdbRoom{}, err
	}
	return room, nil
}
