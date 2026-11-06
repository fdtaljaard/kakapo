package service

import (
	"context"
	"errors"
	"os"
	"os/user"
	"strings"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Database decisions (docs/phase5m-contract.md): the row versioning row is built with the overview and
// with the rcsi fix's preview from what each already reads, the stored blocking history and deadlock
// report, and the decision records; Record a decision keeps a record (keep, later), and the rcsi fix's
// Apply keeps "change" itself.

// rowVersioningDecision fills in the stored records and builds the row versioning decision.
func (s *Service) rowVersioningDecision(ctx context.Context, serverID int64, in domain.RowVersioningInputs) (domain.Decision, error) {
	records, err := s.store.ListDecisions(ctx, serverID, in.Database, domain.DecisionRowVersioning)
	if err != nil {
		return domain.Decision{}, err
	}
	in.Records = records
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	return domain.BuildRowVersioningDecision(in), nil
}

// storedRowVersioningInputs are the decision's inputs from the store: the last blocking history and
// deadlock report of the database.
func (s *Service) storedRowVersioningInputs(ctx context.Context, serverID int64, dbName string) (domain.RowVersioningInputs, error) {
	in := domain.RowVersioningInputs{Database: dbName, Now: time.Now()}
	var err error
	if in.Blocking, err = s.store.LastBlockingHistory(ctx, serverID, dbName); err != nil {
		return in, err
	}
	if in.Deadlocks, err = s.store.LastDeadlockReport(ctx, serverID, dbName); err != nil {
		return in, err
	}
	return in, nil
}

// Decisions returns the records of the row versioning decision on a database, newest first, each with
// its line and whether its review is due. It reads only the store.
func (s *Service) Decisions(ctx context.Context, serverID int64, dbName string) ([]domain.DecisionRecord, error) {
	if err := s.knownServer(ctx, serverID, dbName); err != nil {
		return nil, err
	}
	rs, err := s.store.ListDecisions(ctx, serverID, dbName, domain.DecisionRowVersioning)
	if err != nil {
		return nil, err
	}
	return domain.DescribeRecords(rs, time.Now()), nil
}

// RecordDecision keeps a decision on a database (keep it as it is, decide later), with the verdict the
// row showed, the reason and the review date (90 days from today by default), decided by the Windows
// user running kakapo. KindInvalid for a body that is not right (keep without a reason, change it).
func (s *Service) RecordDecision(ctx context.Context, serverID int64, dbName string, in domain.DecisionInput) (domain.DecisionRecord, error) {
	now := time.Now()
	r, err := domain.ParseDecisionInput(in, now)
	if err != nil {
		return domain.DecisionRecord{}, newError(KindInvalid, "%v", err)
	}
	if err := s.knownServer(ctx, serverID, dbName); err != nil {
		return domain.DecisionRecord{}, err
	}
	r.ServerID, r.Database, r.DecidedAt, r.DecidedBy = serverID, dbName, now.UTC(), windowsUser()
	r, err = s.store.InsertDecision(ctx, r)
	if err != nil {
		return domain.DecisionRecord{}, err
	}
	return domain.DescribeRecords([]domain.DecisionRecord{r}, now)[0], nil
}

// knownServer says KindNotFound for an unregistered server or an empty database name.
func (s *Service) knownServer(ctx context.Context, serverID int64, dbName string) error {
	if strings.TrimSpace(dbName) == "" {
		return newError(KindNotFound, "a database name is needed")
	}
	if _, err := s.store.GetServer(ctx, serverID); errors.Is(err, store.ErrNotFound) {
		return newError(KindNotFound, "server %d is not registered", serverID)
	} else if err != nil {
		return err
	}
	return nil
}

// windowsUser is who runs kakapo, as Windows names them (DOMAIN\user), for the decision record.
func windowsUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USERNAME"); n != "" {
		if d := os.Getenv("USERDOMAIN"); d != "" {
			return d + `\` + n
		}
		return n
	}
	return "unknown"
}
