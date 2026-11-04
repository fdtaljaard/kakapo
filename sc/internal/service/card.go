package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// CardDetail reads one card of a database live, with its ranked findings and every check behind
// it (docs/phase3a-contract.md). Backups, Setup, Storage, Upkeep and Performance are read and
// stored as card reads (Setup and Upkeep with the fixes available now, docs/phase3c-contract.md and
// phase3d; Performance with the last deadlock report, phase3e); the card still to come is grey with
// empty lists. An unknown server, database or card id is KindNotFound.
func (s *Service) CardDetail(ctx context.Context, serverID int64, dbName, cardID string) (domain.CardDetail, error) {
	def, ok := domain.FindCardDef(domain.CardID(cardID))
	if !ok {
		return domain.CardDetail{}, newError(KindNotFound, "there is no card named %q", cardID)
	}
	if strings.TrimSpace(dbName) == "" {
		return domain.CardDetail{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.CardDetail{}, err
	}
	defer db.Close()

	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.CardDetail{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.CardDetail{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	defer collect.HoldOpen(ctx, db, *d)()

	switch def.ID {
	case domain.CardBackups:
		return s.backupsDetail(ctx, db, serverID, *d, def)
	case domain.CardStorage:
		return s.storageDetail(ctx, db, serverID, *d, def)
	case domain.CardSetup:
		return s.setupDetail(ctx, db, serverID, *d, def)
	case domain.CardUpkeep:
		return s.upkeepDetail(ctx, db, serverID, *d, def)
	case domain.CardPerformance:
		return s.performanceDetail(ctx, db, serverID, *d, def)
	}
	return detailWithoutChecks(def), nil
}

// backupsDetail reads the Backups card of one database, keeps the read, and lists the fixes
// available right now (the backup schedule and its remove, docs/phase3g-contract.md; the recovery
// model, integrity check and suspect-page fixes, docs/phase4d-contract.md), with the last CHECKDB run
// from the app.
func (s *Service) backupsDetail(ctx context.Context, db *sql.DB, serverID int64, d domain.DatabaseStatus, def domain.CardDef) (domain.CardDetail, error) {
	facts := readBackupFacts(ctx, db, d)
	run, err := s.store.LastCheckdbRun(ctx, serverID, d.Name)
	if err != nil {
		return domain.CardDetail{}, err
	}
	facts.LastCheckdbRun = run
	card, findings, checks := domain.BuildBackupsCard(facts, def)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.CardDetail{}, err
	}
	return domain.CardDetail{Card: card, Findings: findings, Checks: checks, Fixes: domain.AvailableBackupFixes(facts, findings), CheckdbRun: run}, nil
}

// storageDetail reads the Storage card of one database and keeps the read and its samples. The detail carries the
// last stored CHECKDB room result, or null (reading the card never runs the estimate), the last CHECKDB run from
// the app, and the fixes of its findings (autogrowth, TempDB files, docs/phase4d-contract.md).
func (s *Service) storageDetail(ctx context.Context, db *sql.DB, serverID int64, d domain.DatabaseStatus, def domain.CardDef) (domain.CardDetail, error) {
	facts := readStorageFacts(ctx, db, d)
	last, err := s.store.LastCheckdbRoom(ctx, serverID, d.Name)
	if err != nil {
		return domain.CardDetail{}, err
	}
	run, err := s.store.LastCheckdbRun(ctx, serverID, d.Name)
	if err != nil {
		return domain.CardDetail{}, err
	}
	trend, err := s.storageTrend(ctx, serverID, d.Name, facts)
	if err != nil {
		return domain.CardDetail{}, err
	}
	card, findings, checks := domain.BuildStorageCard(facts, def, last, trend)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.CardDetail{}, err
	}
	return domain.CardDetail{Card: card, Findings: findings, Checks: checks, CheckdbRoom: last, CheckdbRun: run,
		Fixes: domain.AvailableStorageFixes(facts, findings)}, nil
}

// detailWithoutChecks is the detail of a card whose checks are not built yet: grey, with empty
// findings and checks.
func detailWithoutChecks(def domain.CardDef) domain.CardDetail {
	return domain.CardDetail{Card: domain.GreyCard(def), Findings: []domain.Finding{}, Checks: []domain.CheckStatus{}}
}

// readBackupFacts reads what the Backups card needs. When nothing can be read, the facts say why,
// so the card turns grey instead of failing the page.
func readBackupFacts(ctx context.Context, db *sql.DB, d domain.DatabaseStatus) domain.BackupFacts {
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := collect.BackupFacts(readCtx, db, d.Name)
	if err != nil {
		return domain.BackupFacts{Database: d.Name, State: d.State, Error: "could not read the backup history: " + err.Error()}
	}
	f.IsSecondary = d.IsSecondaryReplica
	return f
}

// readStorageFacts reads what the Storage card needs. When nothing can be read, the facts say why,
// so the card turns grey instead of failing the page.
func readStorageFacts(ctx context.Context, db *sql.DB, d domain.DatabaseStatus) domain.StorageFacts {
	readCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	f, err := collect.StorageFacts(readCtx, db, d.Name)
	if err != nil {
		return domain.StorageFacts{Database: d.Name, State: d.State, Error: "could not read the storage facts: " + err.Error()}
	}
	return f
}

// storageTrend keeps the samples of one storage read (docs/phase3b-contract.md, addendum C) and
// returns the pair of stored samples of the tightest volume STO12 forecasts from, or nil when the
// volumes could not be read.
func (s *Service) storageTrend(ctx context.Context, serverID int64, dbName string, facts domain.StorageFacts) (*domain.DiskTrend, error) {
	volumes, sample := facts.Samples()
	if err := s.store.InsertStorageSamples(ctx, serverID, dbName, time.Now().UTC().Truncate(time.Microsecond), volumes, sample); err != nil {
		return nil, err
	}
	volume, ok := facts.TightestVolume()
	if !ok {
		return nil, nil
	}
	return s.store.DiskTrend(ctx, serverID, volume)
}
