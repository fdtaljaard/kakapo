package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// sampleSlack is how much longer than the sample itself its reads may take: two snapshots and the
// query texts on a busy server.
const sampleSlack = 2 * time.Minute

// PerformanceSample runs the live sample now (docs/phase4c-contract.md, part 3): two snapshots
// seconds apart on one connection (0 means the default 30; 10 to 120 allowed), the difference in
// DESIGN 5.3's sentences, stored and returned. KindInvalid for seconds out of range, KindNotFound
// for an unknown server or database, KindUnreachable when the server cannot be read (the message
// says why). It changes nothing on the server.
func (s *Service) PerformanceSample(ctx context.Context, serverID int64, dbName string, seconds int) (domain.PerfSample, error) {
	n, err := domain.ClampSampleSeconds(seconds)
	if err != nil {
		return domain.PerfSample{}, newError(KindInvalid, "%v", err)
	}
	if strings.TrimSpace(dbName) == "" {
		return domain.PerfSample{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.PerfSample{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.PerfSample{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.PerfSample{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}

	readCtx, cancel := context.WithTimeout(ctx, time.Duration(n)*time.Second+sampleSlack)
	defer cancel()
	in, err := collect.Sample(readCtx, db, d.Name, n)
	if err != nil {
		msg := err.Error()
		if lower := strings.ToLower(msg); strings.Contains(lower, "permission") || strings.Contains(lower, "denied") {
			msg += " (the sample needs VIEW SERVER STATE)"
		}
		return domain.PerfSample{}, newError(KindUnreachable, "could not take the sample on %s: %s", srv.Name, msg)
	}
	p := domain.BuildPerfSample(in, time.Now().UTC().Truncate(time.Microsecond))
	if err := s.store.InsertPerfSample(ctx, serverID, d.Name, p); err != nil {
		return domain.PerfSample{}, err
	}
	return p, nil
}

// LastPerformanceSample is the last stored live sample of a database, or nil when none was run.
// It reads the case file only. KindNotFound for an unknown server.
func (s *Service) LastPerformanceSample(ctx context.Context, serverID int64, dbName string) (*domain.PerfSample, error) {
	if _, err := s.GetServer(ctx, serverID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dbName) == "" {
		return nil, newError(KindNotFound, "a database name is needed")
	}
	return s.store.LastPerfSample(ctx, serverID, dbName)
}
