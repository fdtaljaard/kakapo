package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
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
// performance and design facts (each card stored as a card read, the storage samples kept; neither the CHECKDB
// room estimate nor the deadlock report is run here), the six cards about it and its latest changes,
// with the changes made outside kakapo harvested from the default trace first, and the red flags
// among the cards' findings.
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
	// Every card's findings, for the red flags (docs/phase5i-contract.md, section 3): no new read.
	all := map[domain.CardID][]domain.Finding{domain.CardSetup: findings}
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
	all[domain.CardBackups] = findings
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
	all[domain.CardStorage] = findings
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	upkeep := readUpkeepFacts(ctx, db, *d)
	s.chg02Facts(ctx, db, serverID, &upkeep)
	upkeepDef, _ := domain.FindCardDef(domain.CardUpkeep)
	card, findings, checks = domain.BuildUpkeepCard(upkeep, upkeepDef)
	all[domain.CardUpkeep] = findings
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	// The settings baseline beside the Upkeep read (docs/phase5w-contract.md, A2); it never fails the page.
	s.takeBaseline(ctx, db, serverID, domain.BaselineRead, "")
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
	all[domain.CardPerformance] = withStoredReports(findings, lastDeadlocks, lastBlocking)
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	// The Design card (docs/phase5n-contract.md), with the last restore from the Backups read above.
	design := readDesignFacts(ctx, db, *d, backups.LastRestore)
	designDef, _ := domain.FindCardDef(domain.CardDesign)
	card, findings, checks = domain.BuildDesignCard(design, designDef)
	all[domain.CardDesign] = findings
	if _, err := s.store.InsertCardRead(ctx, serverID, d.Name, card, findings, checks); err != nil {
		return domain.Overview{}, err
	}
	// The decisions (docs/phase5m-contract.md): the row versioning read, the reports read above, kakapo's
	// session from the instance read, and TempDB's room and TMP03 from the Storage card's facts.
	decision, err := s.overviewRowVersioning(ctx, db, serverID, d.Name, info.XESession, storage, all[domain.CardStorage], lastBlocking, lastDeadlocks)
	if err != nil {
		return domain.Overview{}, err
	}
	checkedAt := time.Now().UTC().Truncate(time.Microsecond)

	info.ServerID, info.Name, info.Host = srv.ID, srv.Name, srv.Host
	info.ProductName = domain.ProductName(domain.EngineMajor(info.ProductVersion))
	info.UptimeDays = domain.UptimeDays(info.StartedAt, checkedAt)
	info.KakapoMachine, _ = os.Hostname()
	info.OnServer = domain.SameMachine(info.MachineName, info.KakapoMachine)

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
		Cards:         domain.BuildCards(&setup, &backups, &storage, trend, &upkeep, &performance, &design),
		RecentChanges: changes,
		ChangeWindow:  window,
		RedFlags:      domain.BuildRedFlags(all),
		Decisions:     []domain.Decision{decision},
		CheckedAt:     checkedAt,
	}, nil
}

// overviewRowVersioning builds the row versioning decision of the database page from the row versioning
// read (a failed read is the row's sentence, never the page's error) and what the overview read.
func (s *Service) overviewRowVersioning(ctx context.Context, db *sql.DB, serverID int64, dbName string, helper *domain.XESession,
	storage domain.StorageFacts, storageFindings []domain.Finding, blocking *domain.BlockingHistory, deadlocks *domain.DeadlockReport) (domain.Decision, error) {
	in := domain.RowVersioningInputs{Database: dbName, Now: time.Now(), Blocking: blocking, Deadlocks: deadlocks, Helper: helper,
		Tempdb: domain.TempdbRoomOf(storage)}
	if f, err := collect.ReadRowVersioning(ctx, db, dbName); err != nil {
		in.FactsError = err.Error()
	} else {
		in.Facts = &f
	}
	for i, x := range storageFindings {
		if x.CheckID == "TMP03" {
			in.TMP03 = &storageFindings[i]
			break
		}
	}
	return s.rowVersioningDecision(ctx, serverID, in)
}

// withStoredReports are the Performance card's findings with DLK01's and BLK01's from the stored
// deadlock report and blocking history, as the card page shows them.
func withStoredReports(findings []domain.Finding, deadlocks *domain.DeadlockReport, blocking *domain.BlockingHistory) []domain.Finding {
	out := append([]domain.Finding{}, findings...)
	if deadlocks != nil {
		out = append(out, deadlocks.Findings...)
	}
	if blocking != nil {
		out = append(out, blocking.Findings...)
	}
	return out
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
