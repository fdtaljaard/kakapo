package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// QueryDetail reads one query of a database by its query hash, as a QRY01 finding names it
// (docs/phase4c-contract.md, part 1): its plans, what they say, its history and waits, the
// statements of its procedure and the tables it touches, with the plain sentences and the fixes
// each one offers. It changes nothing. KindInvalid for a hash that is not one, KindNotFound for
// an unknown server or database, KindConflict for a database that is not online, KindUnreachable
// when the server cannot be read.
func (s *Service) QueryDetail(ctx context.Context, serverID int64, dbName, hash string) (domain.QueryDetail, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.QueryDetail{}, newError(KindNotFound, "a database name is needed")
	}
	norm, _, err := domain.ParseQueryHash(hash)
	if err != nil {
		return domain.QueryDetail{}, newError(KindInvalid, "%q: %v", hash, err)
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.QueryDetail{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.QueryDetail{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.QueryDetail{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.QueryDetail{}, newError(KindConflict, "%s is %s, not online, so its queries cannot be read", d.Name, d.State)
	}
	f, err := collect.QueryDetail(ctx, db, d.Name, norm)
	if errors.Is(err, domain.ErrBadQueryHash) {
		return domain.QueryDetail{}, newError(KindInvalid, "%q: %v", hash, err)
	}
	if err != nil {
		return domain.QueryDetail{}, newError(KindUnreachable, "could not read the query on %s: %v", srv.Name, err)
	}
	// Archive before an index: the tables that only grow that nothing cleans up, in the server's clock.
	f.GrowingCandidates = readGrowingCandidates(ctx, db, d.Name, f.ServerNow.Add(time.Duration(f.UTCOffsetMinutes)*time.Minute))
	// The safe copies of the tables it reads, from the Design card's last stored read (docs/phase5o-contract.md, section 5).
	if f.DuplicateFindings, err = s.store.LastFindings(ctx, serverID, d.Name, domain.CardDesign, "IDX04"); err != nil {
		return domain.QueryDetail{}, err
	}
	// kakapo's own changes to its tables split the verdict (docs/phase5q-contract.md, decision 3): the
	// change log over the query's 7-day window, with a day to spare.
	if f.Changes, err = s.store.ListChangesSince(ctx, serverID, d.Name, f.ServerNow.Add(-8*24*time.Hour)); err != nil {
		return domain.QueryDetail{}, err
	}
	return domain.BuildQueryDetail(f, d.CanFix()), nil
}
