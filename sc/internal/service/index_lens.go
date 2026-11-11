package service

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// IndexLens builds the Indexes page of a database (docs/phase5t-contract.md): the indexes that the
// slowest queries read in full, with the queries as the evidence, from Query Store. Reads only; it
// measures nothing (that is IndexShape, on request). KindNotFound for an unknown server or database,
// KindConflict for one that is not online, KindUnreachable when the server cannot be read.
func (s *Service) IndexLens(ctx context.Context, serverID int64, dbName string) (domain.IndexLens, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.IndexLens{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.IndexLens{}, err
	}
	defer db.Close()
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.IndexLens{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.IndexLens{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.IndexLens{}, newError(KindConflict, "%s is %s, not online, so its indexes cannot be read", d.Name, d.State)
	}
	readCtx, cancel := context.WithTimeout(ctx, shapeReadTimeout)
	defer cancel()
	f, err := collect.IndexCandidates(readCtx, db, d.Name)
	if err != nil {
		return domain.IndexLens{}, newError(KindUnreachable, "could not read Query Store on %s: %v", srv.Name, err)
	}
	f.CanChange = d.CanFix()
	if f.Error == "" && (f.QueryStoreState == "READ_WRITE" || f.QueryStoreState == "READ_ONLY") {
		if err := readLensIndexes(readCtx, db, d.Name, &f); err != nil {
			f.Error = err.Error()
		}
		if f.Changes, err = s.store.ListChangesSince(ctx, serverID, d.Name, time.Now().UTC().Add(-30*24*time.Hour)); err != nil {
			return domain.IndexLens{}, err
		}
	}
	return domain.BuildIndexLens(f), nil
}

// readLensIndexes reads, for the indexes the plans scan, their size from the catalogue, and the
// statistics of the tables that are big enough to be listed.
func readLensIndexes(ctx context.Context, db *sql.DB, database string, f *domain.LensFacts) error {
	scans := domain.LensScans(database, f.Queries)
	if len(scans) == 0 {
		return nil
	}
	items := make([]domain.BatchIndex, 0, len(scans))
	for _, sc := range scans {
		items = append(items, domain.BatchIndex{Schema: sc.Schema, Table: sc.Table, Index: sc.Index})
	}
	bf, err := collect.IndexBasics(ctx, db, database, items)
	if err != nil {
		return err
	}
	f.Basics = bf.Basics
	seen := map[string]bool{}
	var tables []domain.ShowplanObject
	for _, b := range f.Basics {
		key := strings.ToLower(b.Schema + "." + b.Table)
		if !b.TableFound || !b.IndexFound || b.Pages < domain.LensMinPages || seen[key] {
			continue
		}
		seen[key] = true
		tables = append(tables, domain.ShowplanObject{Schema: b.Schema, Table: b.Table})
	}
	if len(tables) == 0 {
		return nil
	}
	if f.Tables, f.ServerNow, err = collect.ReadTableStatistics(ctx, db, database, tables); err != nil {
		f.TablesError = err.Error()
	}
	return nil
}
