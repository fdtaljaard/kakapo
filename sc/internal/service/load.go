package service

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The load strip (docs/phase5i-contract.md): the load now and today against the usual range of the
// selected days, and the live red flags. The days compared are kept in memory per server, database,
// days and server date, so the minute refresh reads only today, the last 30 minutes and the live
// checks. Nothing is stored in the case file.

type loadKey struct {
	server   int64
	database string
	days     int
}

// loadCache keeps the comparison (domain.LoadBase) of each server, database and days.
type loadCache struct {
	mu    sync.Mutex
	bases map[loadKey]domain.LoadBase
}

func newLoadCache() *loadCache { return &loadCache{bases: map[loadKey]domain.LoadBase{}} }

func (c *loadCache) get(k loadKey) (domain.LoadBase, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.bases[k]
	return b, ok
}

func (c *loadCache) put(k loadKey, b domain.LoadBase) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bases[k] = b
}

// loadPlan says what a read needs: with refresh and a kept comparison, only today (from its server
// date); otherwise the whole comparison (full, from the server's date less the days).
func loadPlan(base domain.LoadBase, kept, refresh bool) (from time.Time, full bool) {
	if refresh && kept && !base.Date.IsZero() {
		return base.Date, false
	}
	return time.Time{}, true
}

// Load reads the load strip of one database: Query Store's hours (all the selected days, or with
// refresh and a kept comparison only today), the last 30 minutes and the live checks (ACT01 and
// PRS01, the Performance card's own read and rules). It changes nothing. KindInvalid for days other
// than 7, 14 or 30, KindNotFound for an unknown server or database, KindConflict for a database that is
// not online, KindUnreachable when the server cannot be read.
func (s *Service) Load(ctx context.Context, serverID int64, dbName string, days int, refresh bool) (domain.Load, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.Load{}, newError(KindNotFound, "a database name is needed")
	}
	if _, err := domain.ParseLoadDays(strconv.Itoa(days)); err != nil {
		return domain.Load{}, newError(KindInvalid, "%v", err)
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.Load{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.Load{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.Load{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.Load{}, newError(KindConflict, "%s is %s, not online, so its load cannot be read", d.Name, d.State)
	}

	key := loadKey{server: serverID, database: d.Name, days: days}
	base, kept := s.loads.get(key)
	from, full := loadPlan(base, kept, refresh)
	if full {
		now, _, err := collect.ServerClock(ctx, db)
		if err != nil {
			return domain.Load{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
		}
		from = domain.LoadFrom(now, days)
	}
	f, err := collect.LoadHours(ctx, db, d.Name, from)
	if err != nil {
		return domain.Load{}, newError(KindUnreachable, "could not read Query Store of %s on %s: %v", d.Name, srv.Name, err)
	}
	if !full && !base.For(f.ServerNow, days) {
		// The server's date turned since the comparison was kept: read it again.
		full = true
		if f, err = collect.LoadHours(ctx, db, d.Name, domain.LoadFrom(f.ServerNow, days)); err != nil {
			return domain.Load{}, newError(KindUnreachable, "could not read Query Store of %s on %s: %v", d.Name, srv.Name, err)
		}
	}
	if full {
		base = domain.LoadBaseline(f, days)
		if f.AboutError == "" && f.HoursError == "" {
			s.loads.put(key, base)
		}
	}
	l := domain.BuildLoad(f, base, domain.LoadOptions{CanChange: d.CanFix()})

	// The live red flags, with the Performance card's own fragment and rules.
	live, err := collect.LiveFacts(ctx, db, d.Name)
	switch {
	case err != nil:
		l.LiveError = err.Error()
	case live.BlockingError != "" || live.SchedulersError != "" || live.GrantsError != "":
		l.LiveError = strings.TrimSpace(strings.Join([]string{live.BlockingError, live.SchedulersError, live.GrantsError}, " "))
	default:
		l.LiveFlags = domain.LiveRedFlags(domain.LiveFindings(live))
	}
	return l, nil
}
