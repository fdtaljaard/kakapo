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

// recentChanges is how many changes the database page shows.
const recentChanges = 10

// Overview reads the database page now: the instance header, the one database and its Query Store
// state (stored as a snapshot, as the database list does), its setup, backup, storage, upkeep and
// performance facts (each card stored as a card read, the storage samples kept; neither the CHECKDB
// room estimate nor the deadlock report is run here), the six cards about it and its latest changes,
// with the changes made outside kakapo harvested from the default trace first.
// A successful read also updates the server's last build, edition and check time.
func (s *Service) Overview(ctx context.Context, serverID int64, dbName string) (domain.Overview, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.Overview{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.Overview{}, err
	}
	defer db.Close()

	info, err := collect.InstanceInfo(ctx, db)
	if err != nil {
		return domain.Overview{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.Overview{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.Overview{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	defer collect.HoldOpen(ctx, db, *d)()
	setup, err := s.readSetupFacts(ctx, db, serverID, d)
	if err != nil {
		return domain.Overview{}, err
	}
	setupDef, _ := domain.FindCardDef(domain.CardSetup)
	card, findings, checks := domain.BuildSetupCard(setup, setupDef)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	backups := readBackupFacts(ctx, db, *d)
	// BAK04's fix follows the last CHECKDB run from the app (docs/phase4d-contract.md).
	if backups.LastCheckdbRun, err = s.store.LastCheckdbRun(ctx, serverID, d.Name); err != nil {
		return domain.Overview{}, err
	}
	backupsDef, _ := domain.FindCardDef(domain.CardBackups)
	card, findings, checks = domain.BuildBackupsCard(backups, backupsDef)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	storage := readStorageFacts(ctx, db, *d)
	lastRoom, err := s.store.LastCheckdbRoom(ctx, serverID, d.Name)
	if err != nil {
		return domain.Overview{}, err
	}
	trend, err := s.storageTrend(ctx, serverID, d.Name, storage)
	if err != nil {
		return domain.Overview{}, err
	}
	storageDef, _ := domain.FindCardDef(domain.CardStorage)
	card, findings, checks = domain.BuildStorageCard(storage, storageDef, lastRoom, trend)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	upkeep := readUpkeepFacts(ctx, db, *d)
	upkeepDef, _ := domain.FindCardDef(domain.CardUpkeep)
	card, findings, checks = domain.BuildUpkeepCard(upkeep, upkeepDef)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	// The changes made outside kakapo, from the same read of the default trace (docs/phase5f-contract.md).
	window, err := s.harvestSchemaChanges(ctx, serverID, upkeep)
	if err != nil {
		return domain.Overview{}, err
	}
	performance := readPerformanceFacts(ctx, db, *d)
	// QRY01's archive before an index, from the storage facts read above (no second read).
	performance.GrowingCandidates = growingCandidatesOf(storage)
	performanceDef, _ := domain.FindCardDef(domain.CardPerformance)
	lastDeadlocks, err := s.store.LastDeadlockReport(ctx, serverID, d.Name)
	if err != nil {
		return domain.Overview{}, err
	}
	lastBlocking, err := s.store.LastBlockingHistory(ctx, serverID, d.Name)
	if err != nil {
		return domain.Overview{}, err
	}
	card, findings, checks = domain.BuildPerformanceCard(performance, performanceDef, lastDeadlocks, lastBlocking)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	checkedAt := time.Now().UTC().Truncate(time.Microsecond)

	info.ServerID, info.Name, info.Host = srv.ID, srv.Name, srv.Host
	info.ProductName = domain.ProductName(domain.EngineMajor(info.ProductVersion))
	info.UptimeDays = domain.UptimeDays(info.StartedAt, checkedAt)

	// d.Name is the name as SQL Server spells it, which is how the change log stores it.
	changes, err := s.ListChanges(ctx, serverID, d.Name, recentChanges)
	if err != nil {
		return domain.Overview{}, err
	}
	if err := s.store.UpdateServerTested(ctx, serverID, info.ProductVersion, info.Edition, checkedAt); err != nil {
		return domain.Overview{}, err
	}
	return domain.Overview{
		Instance:      info,
		Database:      *d,
		Cards:         domain.BuildCards(&setup, &backups, &storage, trend, &upkeep, &performance),
		RecentChanges: changes,
		ChangeWindow:  window,
		CheckedAt:     checkedAt,
	}, nil
}

// ListChanges returns the latest changes made on one database of a server through the app,
// newest first. It reads only the change log, so a database that does not exist has none.
func (s *Service) ListChanges(ctx context.Context, serverID int64, dbName string, limit int) ([]domain.ChangeEntry, error) {
	if _, err := s.store.GetServer(ctx, serverID); errors.Is(err, store.ErrNotFound) {
		return nil, newError(KindNotFound, "server %d is not registered", serverID)
	} else if err != nil {
		return nil, err
	}
	rows, err := s.store.ListChanges(ctx, serverID, dbName, limit)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ChangeEntry, 0, len(rows))
	for _, c := range rows {
		out = append(out, c.Entry())
	}
	return out, nil
}

// RememberDatabase keeps the database last picked on a server's registration, so the next visit
// opens straight into it. It does not connect to the server.
func (s *Service) RememberDatabase(ctx context.Context, serverID int64, dbName string) (domain.Server, error) {
	srv, err := s.store.UpdateServerDatabase(ctx, serverID, strings.TrimSpace(dbName))
	if errors.Is(err, store.ErrNotFound) {
		return domain.Server{}, newError(KindNotFound, "server %d is not registered", serverID)
	}
	return srv, err
}
