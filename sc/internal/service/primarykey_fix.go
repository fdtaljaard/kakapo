package service

import (
	"context"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The add-primary-key fix (docs/phase5s-contract.md, sections 3 to 5), registered with the fix registry
// as a long fix: the preview reads the table, its indexes, the names in its schema and the code that
// names it (collect.PrimaryKeyFacts) and builds it in domain; Apply runs the ALTER TABLE with
// create-index's 30-minute bound, reads the table back and writes the change row (type INDEX) with the
// rollback.
func init() {
	registerLongFix(domain.FixAddPrimaryKey, prepareAddPrimaryKey, applyAddPrimaryKey,
		longFix{timeout: indexApplyTimeout + runReadMargin, completed: addPrimaryKeyCompleted})
}

// primaryKeyTarget reads the table from the options; a table of another database is refused.
func primaryKeyTarget(options map[string]string, dbName string) (schema, table string, err error) {
	if strings.TrimSpace(options[domain.OptionTable]) == "" {
		return "", "", newError(KindInvalid, "the primary key needs its table (table)")
	}
	db, schema, table, perr := domain.SplitTableName(options[domain.OptionTable])
	if perr != nil {
		return "", "", newError(KindInvalid, "the table for the primary key: %v", perr)
	}
	if db != "" && !strings.EqualFold(db, dbName) {
		return "", "", newError(KindConflict, "the table is in the database %s, not %s", db, dbName)
	}
	return schema, table, nil
}

// readPrimaryKeyFacts reads the table the fix works on. A table that does not exist is KindNotFound;
// columns or indexes that cannot be read are KindUnreachable.
func readPrimaryKeyFacts(ctx context.Context, g *genericFix, schema, table string) (domain.PrimaryKeyFacts, error) {
	readCtx, cancel := context.WithTimeout(ctx, indexReadTimeout)
	defer cancel()
	f, err := collect.PrimaryKeyFacts(readCtx, g.db, g.database.Name, schema, table)
	switch {
	case err != nil:
		return f, newError(KindUnreachable, "could not read the table %s.%s in %s: %v", schema, table, g.database.Name, err)
	case !f.Index.Found || !f.Found:
		return f, newError(KindNotFound, "there is no table %s.%s in %s", schema, table, g.database.Name)
	case f.Index.ColumnsError != "":
		return f, newError(KindUnreachable, "could not read the columns of %s.%s: %s", schema, table, f.Index.ColumnsError)
	case f.Index.IndexesError != "":
		return f, newError(KindUnreachable, "could not read the indexes of %s.%s: %s", schema, table, f.Index.IndexesError)
	}
	return f, nil
}

func prepareAddPrimaryKey(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	schema, table, err := primaryKeyTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readPrimaryKeyFacts(ctx, g, schema, table)
	if err != nil {
		return err
	}
	p, fix := domain.BuildAddPrimaryKeyFix(f, options)
	return suPreviewed(g, p, fix)
}

// applyAddPrimaryKey runs the previewed ALTER TABLE, reads the table back and logs the change.
func applyAddPrimaryKey(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	if err := suApplyRefusal(g); err != nil {
		return domain.FixResult{}, err
	}
	schema, table, err := primaryKeyTarget(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	applyCtx, cancel := context.WithTimeout(ctx, indexApplyTimeout)
	err = collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not add the primary key to %s.%s: %v. Nothing changed. Needs the ALTER permission on %s.%s.",
			schema, table, err, schema, table)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := primaryKeyChange(serverID, g, schema, table, appliedAt)
	f, err := readPrimaryKeyFacts(ctx, g, schema, table)
	if err != nil {
		change.Result = "applied, but the table could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the primary key was added to %s.%s, but could not be read back: %v", schema, table, err)
	}
	after, verified := domain.PrimaryKeyReadBack(f, p)
	return s.logIndexChange(ctx, change, p, "none", after, verified, appliedAt)
}

func primaryKeyChange(serverID int64, g *genericFix, schema, table string, at time.Time) domain.Change {
	p := g.preview
	return domain.Change{
		ServerID:    serverID,
		Database:    g.database.Name,
		ChangedAt:   at,
		Object:      domain.IndexObject(schema, table, p.Options[domain.OptionIndexName]),
		ChangeType:  "INDEX",
		Description: domain.PrimaryKeyChangeDescription(g.fix, g.database.Name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
}

// addPrimaryKeyCompleted reads the table back after the run's connection was lost: done when a primary
// key is there (the statement is one ALTER TABLE: SQL Server rolls it back whole when it does not finish).
func addPrimaryKeyCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	schema, table, err := primaryKeyTarget(g.preview.Options, g.database.Name)
	if err != nil {
		return recovered{}, false, err
	}
	f, err := readPrimaryKeyFacts(ctx, g, schema, table)
	if err != nil {
		return recovered{}, false, err
	}
	if !f.Table.HasPrimaryKey {
		return recovered{after: "no primary key on " + schema + "." + table + ": the statement did not finish (SQL Server rolls it back whole); apply it again"}, false, nil
	}
	after, verified := domain.PrimaryKeyReadBack(f, g.preview)
	c := primaryKeyChange(0, g, schema, table, time.Time{})
	return recovered{after: after, verified: verified, before: "none", change: domain.Change{
		Object: c.Object, ChangeType: c.ChangeType, Description: c.Description, Statement: c.Statement, Rollback: c.Rollback}}, true, nil
}
