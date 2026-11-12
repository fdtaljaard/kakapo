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

// The Memory screen (docs/phase5v-contract.md). Read-only on the server. Panels 2 and 3 are read on every
// page load (instant); the two buffer pool reads run only on a click, each bounded by memoryScanTimeout,
// and stop when the request is cancelled (the page's Stop), which ends the statement on the server.
const (
	memoryOverviewTimeout = time.Minute
	memoryScanTimeout     = 10 * time.Minute
	memorySnapshotsListed = 100
)

// Memory builds the Memory screen. With snapshotID 0: panels 2 and 3 read now, and panel 4 from the latest
// saved snapshot, if any. With a snapshot id: that snapshot as it was read. KindNotFound for an unknown
// server or snapshot; KindUnreachable when the server cannot be read.
func (s *Service) Memory(ctx context.Context, serverID, snapshotID int64) (domain.MemoryPage, error) {
	srv, err := s.GetServer(ctx, serverID)
	if err != nil {
		return domain.MemoryPage{}, err
	}
	page := domain.MemoryPage{ServerName: srv.Name}
	if page.Snapshots, err = s.store.ListMemorySnapshots(ctx, serverID, store.MemoryKindInstance, "", memorySnapshotsListed); err != nil {
		return page, err
	}
	if snapshotID > 0 {
		var snap domain.MemorySnapshot
		if _, err := s.store.MemorySnapshot(ctx, serverID, snapshotID, store.MemoryKindInstance, &snap); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return page, newError(KindNotFound, "there is no memory snapshot %d of %s", snapshotID, srv.Name)
			}
			return page, err
		}
		page.View, page.SnapshotID = domain.BuildMemory(snap.Facts, snap.Buffers), snapshotID
		return page, nil
	}

	db, _, err := s.connect(ctx, serverID)
	if err != nil {
		return page, err
	}
	defer db.Close()
	// The Memory screen's read takes a settings baseline (docs/phase5w-contract.md, A2); it never fails the page.
	s.takeBaseline(ctx, db, serverID, domain.BaselineRead, "")
	readCtx, cancel := context.WithTimeout(ctx, memoryOverviewTimeout)
	defer cancel()
	f, err := collect.MemoryOverview(readCtx, db)
	if err != nil {
		return page, newError(KindUnreachable, "could not read the memory of %s: %v", srv.Name, err)
	}
	var buffers *domain.BufferFacts
	if f.Permission == "" {
		for _, info := range page.Snapshots {
			if !info.Databases {
				continue
			}
			var snap domain.MemorySnapshot
			if _, err := s.store.MemorySnapshot(ctx, serverID, info.ID, store.MemoryKindInstance, &snap); err != nil {
				return page, err
			}
			buffers, page.SnapshotID = snap.Buffers, info.ID
			break
		}
	}
	page.View, page.Live = domain.BuildMemory(f, buffers), true
	return page, nil
}

// TakeMemorySnapshot reads panels 2 to 4 now (the buffer pool by database: the slow read), keeps them as a
// snapshot and returns the screen showing it. A login without the right gets the screen saying so, and
// nothing is kept.
func (s *Service) TakeMemorySnapshot(ctx context.Context, serverID int64) (domain.MemoryPage, error) {
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.MemoryPage{}, err
	}
	defer db.Close()
	page := domain.MemoryPage{ServerName: srv.Name, Live: true}
	overCtx, cancel := context.WithTimeout(ctx, memoryOverviewTimeout)
	f, err := collect.MemoryOverview(overCtx, db)
	cancel()
	if err != nil {
		return page, newError(KindUnreachable, "could not read the memory of %s: %v", srv.Name, err)
	}
	if f.Permission == "" {
		scanCtx, cancel := context.WithTimeout(ctx, memoryScanTimeout)
		b, perm, err := collect.MemoryBuffers(scanCtx, db)
		cancel()
		switch {
		case err != nil && ctx.Err() != nil:
			return page, ctx.Err() // stopped from the page
		case err != nil:
			return page, newError(KindUnreachable, "could not read the buffer pool of %s: %v", srv.Name, scanError(err))
		case perm != "":
			f.Permission = perm
		default:
			id, err := s.store.InsertMemorySnapshot(ctx, serverID, store.MemoryKindInstance, "", b.ReadAt,
				domain.MemorySnapshot{Facts: f, Buffers: &b})
			if err != nil {
				return page, err
			}
			page.View, page.SnapshotID = domain.BuildMemory(f, &b), id
		}
	}
	if f.Permission != "" {
		page.View = domain.BuildMemory(f, nil)
	}
	if page.Snapshots, err = s.store.ListMemorySnapshots(ctx, serverID, store.MemoryKindInstance, "", memorySnapshotsListed); err != nil {
		return page, err
	}
	return page, nil
}

// scanError says what a timeout means for a buffer pool read.
func scanError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") {
		return errors.New("the read took longer than 10 minutes and was stopped")
	}
	return err
}

// DatabaseMemory returns Database detail as last read (or the snapshot asked for), with the list of its
// snapshots. Nothing is read from the server. KindNotFound for an unknown server or snapshot.
func (s *Service) DatabaseMemory(ctx context.Context, serverID int64, dbName string, snapshotID int64) (domain.DatabaseMemoryPage, error) {
	srv, err := s.GetServer(ctx, serverID)
	if err != nil {
		return domain.DatabaseMemoryPage{}, err
	}
	page := domain.DatabaseMemoryPage{ServerName: srv.Name}
	if page.Snapshots, err = s.store.ListMemorySnapshots(ctx, serverID, store.MemoryKindDatabase, dbName, memorySnapshotsListed); err != nil {
		return page, err
	}
	id := snapshotID
	if id == 0 {
		if len(page.Snapshots) == 0 {
			return page, nil
		}
		id = page.Snapshots[0].ID
	}
	var d domain.DatabaseMemory
	info, err := s.store.MemorySnapshot(ctx, serverID, id, store.MemoryKindDatabase, &d)
	if errors.Is(err, store.ErrNotFound) || (err == nil && info.Database != dbName) {
		return page, newError(KindNotFound, "there is no memory snapshot %d of %s", id, dbName)
	}
	if err != nil {
		return page, err
	}
	if d.Rows == nil {
		d.Rows = []domain.IndexCacheRow{}
	}
	page.Detail, page.SnapshotID = &d, id
	return page, nil
}

// TakeDatabaseMemory reads one database's cache now, index by index (the slow read), keeps it as a snapshot
// and returns it. KindNotFound for an unknown server or database, KindConflict for one that is not online,
// KindUnreachable when the server cannot be read. A login without a right gets the page saying so, and
// nothing is kept.
func (s *Service) TakeDatabaseMemory(ctx context.Context, serverID int64, dbName string) (domain.DatabaseMemoryPage, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.DatabaseMemoryPage{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.DatabaseMemoryPage{}, err
	}
	defer db.Close()
	page := domain.DatabaseMemoryPage{ServerName: srv.Name}
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return page, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return page, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return page, newError(KindConflict, "%s is %s, not online, so its cache cannot be read", d.Name, d.State)
	}
	scanCtx, cancel := context.WithTimeout(ctx, memoryScanTimeout)
	defer cancel()
	f, err := collect.DatabaseCache(scanCtx, db, d.Name)
	if err != nil {
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		return page, newError(KindUnreachable, "could not read the cache of %s on %s: %v", d.Name, srv.Name, scanError(err))
	}
	built := domain.BuildDatabaseMemory(f)
	page.Detail = &built
	if f.Permission == "" {
		if page.SnapshotID, err = s.store.InsertMemorySnapshot(ctx, serverID, store.MemoryKindDatabase, d.Name, f.ReadAt, built); err != nil {
			return page, err
		}
	}
	if page.Snapshots, err = s.store.ListMemorySnapshots(ctx, serverID, store.MemoryKindDatabase, d.Name, memorySnapshotsListed); err != nil {
		return page, err
	}
	return page, nil
}
