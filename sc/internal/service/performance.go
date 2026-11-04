package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// deadlockReadTimeout bounds the deadlock report: reading the system_health files can take a
// minute or more on a busy server.
const deadlockReadTimeout = 5 * time.Minute

// readPerformanceFacts reads what the Performance card needs (docs/phase3e-contract.md). When
// nothing can be read, the facts say why, so the card turns grey instead of failing the page.
func readPerformanceFacts(ctx context.Context, db *sql.DB, d domain.DatabaseStatus) domain.PerformanceFacts {
	readCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	f, err := collect.PerformanceFacts(readCtx, db, d.Name)
	if err != nil {
		return domain.PerformanceFacts{Database: d.Name, State: d.State, Error: err.Error()}
	}
	return f
}

// performanceDetail reads the Performance card of one database and keeps the read. The detail
// carries the last stored deadlock report, or null; reading the card never reads the deadlocks.
func (s *Service) performanceDetail(ctx context.Context, db *sql.DB, serverID int64, d domain.DatabaseStatus, def domain.CardDef) (domain.CardDetail, error) {
	facts := readPerformanceFacts(ctx, db, d)
	last, err := s.store.LastDeadlockReport(ctx, serverID, d.Name)
	if err != nil {
		return domain.CardDetail{}, err
	}
	card, findings, checks := domain.BuildPerformanceCard(facts, def, last)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.CardDetail{}, err
	}
	// The create-index fix of each WRK03 finding, with its options (docs/phase4c-contract.md, part 3).
	return domain.CardDetail{Card: card, Findings: findings, Checks: checks, Deadlocks: last,
		Fixes: domain.AvailablePerformanceFixes(facts, findings)}, nil
}

// DeadlockReport runs DLK01 now (docs/phase3e-contract.md): reads the deadlocks of the last 7 days
// from the system_health session, groups this database's by the pair of procedures, keeps the
// server's SYSTEM and IO_SUBSYSTEM health warnings of the same window (docs/phase3f-contract.md,
// item 21), stores the report and returns it. KindNotFound for an unknown server or database,
// KindUnreachable when the server or the session cannot be read (the message says why).
func (s *Service) DeadlockReport(ctx context.Context, serverID int64, dbName string) (domain.DeadlockReport, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.DeadlockReport{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.DeadlockReport{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.DeadlockReport{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.DeadlockReport{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}

	readCtx, cancel := context.WithTimeout(ctx, deadlockReadTimeout)
	defer cancel()
	start := time.Now()
	events, health, source, err := collect.ReadDeadlocks(readCtx, db, domain.DeadlockDays)
	if err != nil {
		msg := err.Error()
		if lower := strings.ToLower(msg); strings.Contains(lower, "permission") || strings.Contains(lower, "denied") {
			msg += " (reading the system_health session needs VIEW SERVER STATE)"
		}
		return domain.DeadlockReport{}, newError(KindUnreachable, "could not read the deadlocks from the system_health session on %s: %s", srv.Name, msg)
	}
	r := domain.BuildDeadlockReport(domain.DeadlockInputs{
		Database: d.Name, CapturedAt: time.Now().UTC().Truncate(time.Microsecond), Duration: time.Since(start),
		Days: domain.DeadlockDays, Source: source, Events: events, Health: health,
	})
	if err := s.store.InsertDeadlockReport(ctx, serverID, d.Name, r); err != nil {
		return domain.DeadlockReport{}, err
	}
	return r, nil
}
