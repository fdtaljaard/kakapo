package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The create-index and drop-index fixes (docs/phase4c-contract.md, part 3), registered with the
// fix registry: the preview reads the table's columns, indexes, sizes and use and runs the
// PerformanceTriage.sql's index guards (domain.BuildCreateIndexFix); Apply runs the statement with a 30-minute
// timeout, reads sys.indexes back and writes the change row (type INDEX) with the rollback.
func init() {
	// Long fixes (docs/phase4f-contract.md): they run in the background.
	registerLongFix(domain.FixCreateIndex, prepareCreateIndex, applyCreateIndex,
		longFix{timeout: indexApplyTimeout + runReadMargin, completed: createIndexCompleted})
	registerLongFix(domain.FixDropIndex, prepareDropIndex, applyDropIndex,
		longFix{timeout: indexApplyTimeout + runReadMargin, completed: dropIndexCompleted})
	// Phase 5o (docs/phase5o-contract.md, section 3): disabling a copy takes a moment, so it is not long.
	registerFix(domain.FixDisableIndex, prepareDisableIndex, applyDisableIndex)
}

const (
	indexReadTimeout    = 60 * time.Second
	indexApplyTimeout   = 30 * time.Minute
	disableApplyTimeout = 2 * time.Minute
)

// createIndexSpec reads the create-index options; a suggestion for another database is refused.
func createIndexSpec(options map[string]string, dbName string) (domain.CreateIndexSpec, error) {
	spec, err := domain.ParseCreateIndexOptions(options)
	if err != nil {
		return spec, newError(KindInvalid, "the index to create: %v", err)
	}
	if spec.Database != "" && !strings.EqualFold(spec.Database, dbName) {
		return spec, newError(KindConflict, "the suggestion is for the database %s, not %s", spec.Database, dbName)
	}
	return spec, nil
}

// dropIndexTarget reads the drop-index options: the index and its table.
func dropIndexTarget(options map[string]string, dbName string) (schema, table, index string, err error) {
	index = strings.TrimSpace(options[domain.OptionIndex])
	if index == "" || strings.TrimSpace(options[domain.OptionTable]) == "" {
		return "", "", "", newError(KindInvalid, "the index to drop needs its name (index) and its table (table)")
	}
	db, schema, table, perr := domain.SplitTableName(options[domain.OptionTable])
	if perr != nil {
		return "", "", "", newError(KindInvalid, "the index to drop: %v", perr)
	}
	if db != "" && !strings.EqualFold(db, dbName) {
		return "", "", "", newError(KindConflict, "the index is in the database %s, not %s", db, dbName)
	}
	return schema, table, index, nil
}

// readIndexFacts reads the table the index fixes work on. A table that does not exist is
// KindNotFound; columns or indexes that cannot be read are KindUnreachable, because the guards
// need them.
func readIndexFacts(ctx context.Context, g *genericFix, schema, table string) (domain.IndexFacts, error) {
	readCtx, cancel := context.WithTimeout(ctx, indexReadTimeout)
	defer cancel()
	f, err := collect.IndexFacts(readCtx, g.db, g.database.Name, schema, table)
	switch {
	case err != nil:
		return f, newError(KindUnreachable, "could not read the table %s.%s in %s: %v", schema, table, g.database.Name, err)
	case !f.Found:
		return f, newError(KindNotFound, "there is no table %s.%s in %s", schema, table, g.database.Name)
	case f.ColumnsError != "":
		return f, newError(KindUnreachable, "could not read the columns of %s.%s, which the index checks need: %s", schema, table, f.ColumnsError)
	case f.IndexesError != "":
		return f, newError(KindUnreachable, "could not read the indexes of %s.%s, which the index checks need: %s", schema, table, f.IndexesError)
	}
	return f, nil
}

func prepareCreateIndex(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	spec, err := createIndexSpec(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readIndexFacts(ctx, g, spec.Schema, spec.Table)
	if err != nil {
		return err
	}
	g.preview, g.fix = domain.BuildCreateIndexFix(f, spec, options)
	return nil
}

func applyCreateIndex(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := "the index cannot be created as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "kakapo does not create this index: %s", msg)
	}
	spec, err := createIndexSpec(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	table := spec.Schema + "." + spec.Table
	index := p.Options[domain.OptionIndexName]

	applyCtx, cancel := context.WithTimeout(ctx, indexApplyTimeout)
	err = collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not create the index %s on %s: %v. Needs the ALTER permission on %s.",
			index, table, err, table)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.IndexObject(spec.Schema, spec.Table, index),
		ChangeType:  "INDEX",
		Description: domain.CreateIndexChangeDescription(g.fix, name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	f, err := readIndexFacts(ctx, g, spec.Schema, spec.Table)
	if err != nil {
		// The index was created; log it even though we could not prove it.
		change.Result = "applied, but the index could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the index %s was created on %s, but could not be read back: %v", index, table, err)
	}
	var got *domain.ExistingIndex
	if x, ok := f.Index(index); ok {
		got = &x
	}
	after, verified := domain.CreateIndexReadBack(f, spec, got)
	return s.logIndexChange(ctx, change, p, "none", after, verified, appliedAt)
}

func prepareDropIndex(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	schema, table, index, err := dropIndexTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		return err
	}
	g.preview, g.fix, err = domain.BuildDropIndexFix(f, index, options)
	if errors.Is(err, domain.ErrNoSuchIndex) {
		return newError(KindNotFound, "there is no index %s on %s.%s in %s", index, schema, table, g.database.Name)
	}
	return err
}

func applyDropIndex(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := "the index cannot be dropped as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "%s", msg)
	}
	schema, table, index, err := dropIndexTarget(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	applyCtx, cancel := context.WithTimeout(ctx, indexApplyTimeout)
	err = collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not drop the index %s on %s.%s: %v. Needs the ALTER permission on %s.%s.",
			index, schema, table, err, schema, table)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.IndexObject(schema, table, index),
		ChangeType:  "INDEX",
		Description: domain.DropIndexChangeDescription(g.fix, name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	f, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		change.Result = "applied, but the indexes could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the index %s was dropped on %s.%s, but that could not be read back: %v", index, schema, table, err)
	}
	_, still := f.Index(index)
	after, verified := domain.DropIndexReadBack(f, index, still)
	return s.logIndexChange(ctx, change, p, g.fix.Current, after, verified, appliedAt)
}

// createIndexCompleted reads a create-index run back after its connection was lost
// (docs/phase4f-contract.md): done when the index exists, with the apply's read-back.
func createIndexCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	name, p := g.database.Name, g.preview
	spec, err := createIndexSpec(p.Options, name)
	if err != nil {
		return recovered{}, false, err
	}
	index := p.Options[domain.OptionIndexName]
	f, err := readIndexFacts(ctx, g, spec.Schema, spec.Table)
	if err != nil {
		return recovered{}, false, err
	}
	x, ok := f.Index(index)
	if !ok {
		return recovered{after: fmt.Sprintf("the index %s is not on %s.%s: the build did not finish (SQL Server rolls it back when the connection drops); apply it again",
			index, spec.Schema, spec.Table)}, false, nil
	}
	after, verified := domain.CreateIndexReadBack(f, spec, &x)
	return recovered{after: after, verified: verified, before: "none", change: domain.Change{
		Object: domain.IndexObject(spec.Schema, spec.Table, index), ChangeType: "INDEX",
		Description: domain.CreateIndexChangeDescription(g.fix, name), Statement: p.Statement, Rollback: p.Rollback}}, true, nil
}

// dropIndexCompleted reads a drop-index run back after its connection was lost: done when the index
// is gone.
func dropIndexCompleted(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
	name, p := g.database.Name, g.preview
	schema, table, index, err := dropIndexTarget(p.Options, name)
	if err != nil {
		return recovered{}, false, err
	}
	f, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		return recovered{}, false, err
	}
	if _, still := f.Index(index); still {
		return recovered{after: fmt.Sprintf("the index %s is still on %s.%s: the drop did not finish; apply it again", index, schema, table)}, false, nil
	}
	after, verified := domain.DropIndexReadBack(f, index, false)
	return recovered{after: after, verified: verified, change: domain.Change{
		Object: domain.IndexObject(schema, table, index), ChangeType: "INDEX",
		Description: domain.DropIndexChangeDescription(g.fix, name), Statement: p.Statement, Rollback: p.Rollback}}, true, nil
}

// logIndexChange writes the change row of an index fix with what was read back.
func (s *Service) logIndexChange(ctx context.Context, change domain.Change, p domain.FixPreview, before, after string, verified bool, appliedAt time.Time) (domain.FixResult, error) {
	// A read-back that is a sentence ("IX_B is disabled; 4.5 MB freed inside the file.") loses its full stop in the row.
	result := strings.TrimSuffix(after, ".")
	change.Result = result + ", differs"
	if verified {
		change.Result = result + ", verified"
	}
	id, err := s.store.InsertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: before, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// disableIndexTarget reads the disable-index options: the table, the index and the copy that stays.
func disableIndexTarget(options map[string]string, dbName string) (schema, table, index, keeper string, err error) {
	keeper = strings.TrimSpace(options[domain.OptionKeeper])
	if keeper == "" {
		return "", "", "", "", newError(KindInvalid, "the index to disable needs the copy that stays (keeper)")
	}
	schema, table, index, err = dropIndexTarget(options, dbName)
	return schema, table, index, keeper, err
}

// prepareDisableIndex reads the table again (collect.IndexFacts, the read as it is), the use counters'
// window and the search for the index's name in the database's code, plan guides and Query Store, and
// builds the preview (docs/phase5o-contract.md, sections 3 and 4). A search that could not run holds
// Apply back, as an area that failed does.
func prepareDisableIndex(ctx context.Context, s *Service, g *genericFix, _ domain.FixDef, options map[string]string) error {
	schema, table, _, _, err := disableIndexTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, indexReadTimeout)
	defer cancel()
	var window *domain.DesignWindow
	now := time.Time{}
	if u, err := collect.ReadUsageWindow(readCtx, g.db, g.database.Name); err == nil {
		if w, ok := u.Window(); ok {
			window, now = &w, u.ServerNow
		}
	}
	index := strings.TrimSpace(options[domain.OptionIndex])
	if x, ok := f.Index(index); ok {
		index = x.Name
	}
	search, err := collect.SearchIndexName(readCtx, g.db, g.database.Name, index)
	if err != nil {
		search = domain.IndexNameSearch{Index: index, Error: err.Error()}
	}
	g.preview, g.fix = domain.BuildDisableIndexFix(f, &search, window, now, options)
	return nil
}

// applyDisableIndex runs ALTER INDEX ... DISABLE, reads the table back (disabled, no pages left) and
// writes the change row with the REBUILD as its rollback.
func applyDisableIndex(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p := g.database.Name, g.preview
	if len(p.Changes) == 0 || p.Statement == "" {
		msg := "the index cannot be disabled as previewed"
		if len(p.Warnings) > 0 {
			msg = p.Warnings[0]
		}
		return domain.FixResult{}, newError(KindConflict, "%s", msg)
	}
	schema, table, index, keeper, err := disableIndexTarget(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	before, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		return domain.FixResult{}, err
	}
	x, _ := before.Index(index)
	applyCtx, cancel := context.WithTimeout(ctx, disableApplyTimeout)
	err = collect.ExecFix(applyCtx, g.db, p.Statement)
	cancel()
	if err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server did not disable the index %s on %s.%s: %v. Needs the ALTER permission on %s.%s.",
			index, schema, table, err, schema, table)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.IndexObject(schema, table, index),
		ChangeType:  "INDEX",
		Description: domain.DisableIndexChangeDescription(before, index, keeper, x.SizeMB, name),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	f, err := readIndexFacts(ctx, g, schema, table)
	if err != nil {
		change.Result = "applied, but the index could not be read back: " + err.Error()
		if _, logErr := s.store.InsertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the index %s was disabled on %s.%s, but could not be read back: %v", index, schema, table, err)
	}
	after, verified := domain.DisableIndexReadBack(f, index, x.SizeMB)
	return s.logIndexChange(ctx, change, p, g.fix.Current, after, verified, appliedAt)
}
