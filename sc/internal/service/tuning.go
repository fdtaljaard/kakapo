package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// TuningReport reads the tuning report of one database (docs/phase5c-contract.md): Query Store's
// hour by hour history over the last days (1 to 30) and the change log's rows for the database and
// the whole server in that window, compared around the chosen change (0: the latest; TuningChangeNone:
// none) in the given business hours. It changes nothing. KindInvalid for days out of range,
// KindNotFound for an unknown server, database or a change not in the window, KindConflict for a
// database that is not online or a server before SQL Server 2016, KindUnreachable when the server
// cannot be read.
func (s *Service) TuningReport(ctx context.Context, serverID int64, dbName string, days int, changeID int64, business domain.BusinessHours) (domain.TuningReport, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.TuningReport{}, newError(KindNotFound, "a database name is needed")
	}
	if days < domain.TuningMinDays || days > domain.TuningMaxDays {
		return domain.TuningReport{}, newError(KindInvalid, "days must be from %d to %d, not %d", domain.TuningMinDays, domain.TuningMaxDays, days)
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.TuningReport{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.TuningReport{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.TuningReport{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.TuningReport{}, newError(KindConflict, "%s is %s, not online, so its Query Store cannot be read", d.Name, d.State)
	}
	version, err := collect.EngineVersion(ctx, db)
	if err != nil {
		return domain.TuningReport{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if major := domain.EngineMajor(version); major > 0 && major < 13 {
		return domain.TuningReport{}, newError(KindConflict, "the tuning report reads Query Store, which needs SQL Server 2016 or later; %s runs %s",
			srv.Name, domain.ProductName(major))
	}
	// The changes come first, so the read can follow the object of the change it centres on
	// (docs/phase5h-contract.md): read with a day to spare on this machine's clock, chosen on it, then
	// kept to the window on the server's clock once the read has it.
	window := time.Duration(days) * 24 * time.Hour
	all, err := s.store.ListChangesSince(ctx, serverID, d.Name, time.Now().UTC().Add(-window-24*time.Hour))
	if err != nil {
		return domain.TuningReport{}, err
	}
	focus := domain.TuningFocusTarget{}
	if c := domain.TuningCentreChange(changesSince(all, time.Now().UTC().Add(-window)), changeID); c != nil {
		focus = domain.TuningFocusFor(*c)
	}
	f, err := collect.TuningReport(ctx, db, d.Name, days, focus)
	if err != nil {
		return domain.TuningReport{}, newError(KindUnreachable, "could not read Query Store of %s on %s: %v", d.Name, srv.Name, err)
	}

	// The window in UTC, as the change log keeps its times: the server's clock less its offset.
	since := f.ServerNow.Add(-time.Duration(f.UTCOffsetMinutes) * time.Minute).Add(-window)
	changes := changesSince(all, since)
	if changeID > 0 && !hasChange(changes, changeID) {
		return domain.TuningReport{}, newError(KindNotFound, "change %d is not in the last %d days of %s", changeID, days, d.Name)
	}
	return domain.BuildTuningReport(f, changes, domain.TuningOptions{ChangeID: changeID, Business: business, CanChange: d.CanFix()}), nil
}

// changesSince are the changes at or after since (UTC).
func changesSince(changes []domain.Change, since time.Time) []domain.Change {
	out := []domain.Change{}
	for _, c := range changes {
		if !c.ChangedAt.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

func hasChange(changes []domain.Change, id int64) bool {
	for _, c := range changes {
		if c.ID == id {
			return true
		}
	}
	return false
}

// recordFuture is how far ahead of now a recorded change may be, for clocks a little apart.
const recordFuture = 5 * time.Minute

// RecordChange writes a change made outside kakapo to the change log (docs/phase5c-contract.md,
// "Record a change made by hand"): no statement, Result "recorded by hand". A time in server time is
// placed with the server's UTC offset, read from the server; no time is now, and a time with an
// offset is exact, both without connecting. KindInvalid for a field that is not right or a time in
// the future, KindNotFound for an unknown server, KindUnreachable when the server's clock cannot be
// read.
func (s *Service) RecordChange(ctx context.Context, serverID int64, dbName string, in domain.RecordChangeInput) (domain.ChangeEntry, error) {
	rc, err := domain.ParseRecordChange(in)
	if err != nil {
		return domain.ChangeEntry{}, newError(KindInvalid, "%v", err)
	}
	if strings.TrimSpace(dbName) == "" {
		return domain.ChangeEntry{}, newError(KindNotFound, "a database name is needed")
	}
	srv, err := s.store.GetServer(ctx, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.ChangeEntry{}, newError(KindNotFound, "server %d is not registered", serverID)
	}
	if err != nil {
		return domain.ChangeEntry{}, err
	}
	now := time.Now().UTC()
	at := now
	switch {
	case rc.Exact:
		at = rc.At
	case !rc.At.IsZero():
		db, _, err := s.connect(ctx, serverID)
		if err != nil {
			return domain.ChangeEntry{}, newError(KindUnreachable, "the time is server time, and %s's clock could not be read to place it: %v; leave the time empty for now",
				srv.Name, err)
		}
		_, offset, err := collect.ServerClock(ctx, db)
		db.Close()
		if err != nil {
			return domain.ChangeEntry{}, newError(KindUnreachable, "could not read %s's clock: %v", srv.Name, err)
		}
		at = rc.At.Add(-time.Duration(offset) * time.Minute)
	}
	if at.After(now.Add(recordFuture)) {
		return domain.ChangeEntry{}, newError(KindInvalid, "the change is in the future (%s UTC); record it once it is made", at.Format("2006-01-02 15:04"))
	}
	c := rc.Change(serverID, dbName, at)
	id, err := s.insertChange(ctx, c)
	if err != nil {
		return domain.ChangeEntry{}, err
	}
	c.ID = id
	return c.Entry(), nil
}
