package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The query page's index measurement and the reorganize-index fix (docs/phase5q-contract.md,
// section 4). Measuring reads one index in SAMPLED mode; the fix runs ALTER INDEX ... REORGANIZE,
// which on a big index takes a while, so it runs in the background (docs/phase4f-contract.md), then
// measures again and writes the change row (type INDEX).
func init() {
	registerLongFix(domain.FixReorganizeIndex, prepareReorganizeIndex, applyReorganizeIndex,
		longFix{timeout: indexApplyTimeout + runReadMargin, completed: reorganizeIndexCompleted})
}

// shapeReadTimeout bounds one measurement: SAMPLED reads about 1% of a big index's pages.
const shapeReadTimeout = 5 * time.Minute

// IndexShape measures one index of a table for the query page (GET .../indexes/shape). Reads only.
func (s *Service) IndexShape(ctx context.Context, serverID int64, dbName, schema, table, index string) (domain.IndexShape, error) {
	if strings.TrimSpace(schema) == "" || strings.TrimSpace(table) == "" || strings.TrimSpace(index) == "" {
		return domain.IndexShape{}, newError(KindInvalid, "measuring an index needs its schema, table and index")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.IndexShape{}, err
	}
	defer db.Close()
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.IndexShape{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.IndexShape{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.IndexShape{}, newError(KindConflict, "%s is %s, not online, so its indexes cannot be measured", d.Name, d.State)
	}
	readCtx, cancel := context.WithTimeout(ctx, shapeReadTimeout)
	defer cancel()
	f, err := collect.IndexShape(readCtx, db, d.Name, schema, table, index)
	if err != nil {
		return domain.IndexShape{}, newError(KindUnreachable, "could not measure %s on %s.%s: %v", index, schema, table, err)
	}
	return domain.BuildIndexShape(f, d.CanFix()), nil
}

// reorganizeTarget reads the fix's options: the table and the index, as drop-index has them.
func reorganizeTarget(options map[string]string, dbName string) (schema, table, index string, err error) {
	return dropIndexTarget(options, dbName)
}

func readIndexShape(ctx context.Context, g *genericFix, schema, table, index string) (domain.IndexShapeFacts, error) {
	readCtx, cancel := context.WithTimeout(ctx, shapeReadTimeout)
	defer cancel()
	f, err := collect.IndexShape(readCtx, g.db, g.database.Name, schema, table, index)
	if err != nil {
		return f, newError(KindUnreachable, "could not measure %s on %s.%s in %s: %v", index, schema, table, g.database.Name, err)
	}
	return f, nil
}

func prepareReorganizeIndex(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	schema, table, index, err := reorganizeTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readIndexShape(ctx, g, schema, table, index)
	if err != nil {
		return err
	}
	g.preview, g.fix = domain.BuildReorganizeIndexFix(f, options)
	return nil
}

func applyReorganizeIndex(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := "the index cannot be reorganised as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "%s", msg)
	}
	schema, table, index, err := reorganizeTarget(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	before, err := readIndexShape(ctx, g, schema, table, index)
	if err != nil {
		return domain.FixResult{}, err
	}
	return reorganizeAndLog(ctx, s, serverID, g, schema, table, index, before)
}

// reorganizeAndLog runs g's prepared ALTER INDEX ... REORGANIZE on an index measured as before, measures
// it again and writes its change row. The batch (reorganize-indexes) calls it with the facts it just
// measured, so an index is read twice rather than three times.
func reorganizeAndLog(ctx context.Context, s *Service, serverID int64, g *genericFix, schema, table, index string, before domain.IndexShapeFacts) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	applyCtx, cancel := context.WithTimeout(ctx, indexApplyTimeout)
	err := collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not reorganise the index %s on %s.%s: %v. Needs the ALTER permission on %s.%s.",
			index, schema, table, err, schema, table)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.IndexObject(schema, table, before.Index),
		ChangeType:  "INDEX",
		Description: domain.ReorganizeChangeDescription(before, name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	after, err := readIndexShape(ctx, g, schema, table, index)
	if err != nil {
		change.Result = "applied, but the index could not be measured again: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the index %s was reorganised on %s.%s, but could not be measured again: %v", index, schema, table, err)
	}
	text, verified := domain.ReorganizeIndexReadBack(before, after)
	return s.logIndexChange(ctx, change, p, g.fix.Current, text, verified, appliedAt)
}

// reorganizeIndexCompleted reads a reorganise back after its connection was lost: done when the
// index measures as reorganised (the read-back verifies); otherwise it may still be running or have
// stopped part-way, and applying it again carries on where it stopped.
func reorganizeIndexCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	name, p := g.database.Name, g.preview
	schema, table, index, err := reorganizeTarget(p.Options, name)
	if err != nil {
		return recovered{}, false, err
	}
	after, err := readIndexShape(ctx, g, schema, table, index)
	if err != nil {
		return recovered{}, false, err
	}
	text, verified := domain.ReorganizeIndexDone(after)
	if !verified {
		return recovered{after: "the reorganise of " + index + " did not finish: apply it again, it carries on where it stopped"}, false, nil
	}
	return recovered{after: text, verified: true, change: domain.Change{
		Object: domain.IndexObject(schema, table, after.Index), ChangeType: "INDEX",
		Description: domain.ReorganizeChangeDescription(after, name), Statement: p.Statement, Rollback: p.Rollback}}, true, nil
}
