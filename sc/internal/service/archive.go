package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The archive fixes (docs/phase5a-contract.md), registered with the fix registry: "Archive or delete
// old rows" reads the table, its archive table and the rows due, and builds the preview in domain;
// Apply creates the archive table and the Agent job in one batch, reads them back, runs one batch
// when asked and writes the change row (type ARCHIVE). "Stop archiving a table" deletes the job and
// keeps the archive table. The table picker's list is ListTables.
func init() {
	registerFix(domain.FixArchiveTable, prepareArchiveTable, applyArchiveTable)
	registerFix(domain.FixRemoveArchive, prepareRemoveArchive, applyRemoveArchive)
}

const (
	archiveReadTimeout = 60 * time.Second
	// archiveBatchTimeout bounds the one batch run after the job is created: its lock timeout is 10
	// seconds and a batch is at most 50,000 rows.
	archiveBatchTimeout = 2 * time.Minute
)

// archiveTableTarget reads the table option of the archive fixes: [schema].[table], in this database.
func archiveTableTarget(options map[string]string, dbName string) (schema, table string, err error) {
	if strings.TrimSpace(options[domain.OptionTable]) == "" {
		return "", "", newError(KindInvalid, "the archive fixes need the table (table), for example [dbo].[MailLog]")
	}
	db, schema, table, perr := domain.SplitTableName(options[domain.OptionTable])
	if perr != nil {
		return "", "", newError(KindInvalid, "the table to archive: %v", perr)
	}
	if db != "" && !strings.EqualFold(db, dbName) {
		return "", "", newError(KindConflict, "the table is in the database %s, not %s", db, dbName)
	}
	return schema, table, nil
}

// readArchiveFacts reads the table and the archive table the options name. A table that does not
// exist is KindNotFound; a read that cannot run is KindUnreachable.
func readArchiveFacts(ctx context.Context, g *genericFix, schema, table string, options map[string]string) (domain.ArchiveFacts, error) {
	readCtx, cancel := context.WithTimeout(ctx, archiveReadTimeout)
	defer cancel()
	f, err := collect.ArchiveFacts(readCtx, g.db, g.database.Name, schema, table)
	switch {
	case err != nil:
		return f, newError(KindUnreachable, "could not read the table %s.%s in %s: %v", schema, table, g.database.Name, err)
	case !f.Found:
		return f, newError(KindNotFound, "there is no user table %s.%s in %s", schema, table, g.database.Name)
	}
	target := domain.ArchiveTargetFor(f, options)
	if strings.EqualFold(target.Database, f.Database) || f.ArchiveDBExists {
		t, err := collect.ArchiveTarget(readCtx, g.db, target.Database, target.Schema, target.Table)
		if err != nil {
			t = target
			t.Read, t.Error = true, err.Error()
		}
		f.Target = t
	} else {
		f.Target = target
	}
	return f, nil
}

func prepareArchiveTable(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	schema, table, err := archiveTableTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	f, err := readArchiveFacts(ctx, g, schema, table, options)
	if err != nil {
		return err
	}
	// The preview resolves the date column, the months and the condition; when it can be applied,
	// the rows due are counted with them and the preview built again with the count.
	g.preview, g.fix = domain.BuildArchiveFix(f, options)
	if len(g.preview.Changes) > 0 {
		o := g.preview.Options
		keep, _ := strconv.Atoi(o[domain.OptionArchiveKeepMonths])
		f.Estimate = collect.ArchiveEstimate(ctx, g.db, f, o[domain.OptionArchiveDateColumn], keep, o[domain.OptionArchiveCondition])
		g.preview, g.fix = domain.BuildArchiveFix(f, options)
	}
	return nil
}

// notApplicable is the conflict an apply answers for a preview without changes: its first warning.
func notApplicable(p domain.FixPreview, fallback string) error {
	msg := fallback
	if len(p.Warnings) > 0 {
		msg = p.Warnings[0]
	}
	return newError(KindConflict, "%s", msg)
}

func applyArchiveTable(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p, server := g.database.Name, g.preview, g.srv.Name
	if len(p.Changes) == 0 || p.Statement == "" {
		return domain.FixResult{}, notApplicable(p, "the archive cannot be created as previewed")
	}
	plan, err := domain.ArchivePlanFromPreview(name, p)
	if err != nil {
		return domain.FixResult{}, newError(KindInvalid, "the archive as previewed: %v", err)
	}
	create, first := domain.SplitArchiveStatement(p.Statement)
	if err := collect.ExecFix(ctx, g.db, create); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to create the archive of %s.%s on %s: %v. %s",
			plan.Schema, plan.Table, server, err, domain.BackupFixPermission(server))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.ArchiveTableObject(plan.Schema, plan.Table),
		ChangeType:  "ARCHIVE",
		Description: plan.ChangeDescription(),
		Statement:   create,
		Rollback:    p.Rollback,
	}
	states, err := collect.ReadJobsByName(ctx, g.db, []string{plan.JobName()})
	if err != nil {
		// The archive was created; log it even though we could not prove it.
		change.Result = "applied, but the job could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the archive of %s.%s was created on %s, but could not be read back: %v", plan.Schema, plan.Table, server, err)
	}
	var target domain.ArchiveTarget
	if plan.Archives() {
		if target, err = collect.ArchiveTarget(ctx, g.db, plan.Target.Database, plan.Target.Schema, plan.Target.Table); err != nil {
			target.Error = err.Error()
		}
	}
	// One batch now, after the read-back and as its own statement: it moves rows, which no rollback
	// puts back. The rows it moved come back in an output parameter.
	var moved *int64
	firstErr := ""
	if first != "" {
		var n int64
		batchCtx, cancel := context.WithTimeout(ctx, archiveBatchTimeout)
		err := collect.ExecFix(batchCtx, g.db, first+"\nSET @rowsMoved = @n;", sql.Named("rowsMoved", sql.Out{Dest: &n}))
		cancel()
		if err != nil {
			firstErr = err.Error()
		} else {
			moved = &n
			change.Statement += "\n" + first
		}
	}
	after, verified := domain.ArchiveReadBack(plan, states, target, moved, firstErr)
	change.Result = after + " Differs from what was created."
	if verified {
		change.Result = after + " Verified."
	}
	id, err := s.insertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: g.fix.Current, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// readArchiveJobs reads kakapo's archive jobs on the database, with the rows on each side.
func readArchiveJobs(ctx context.Context, g *genericFix) ([]domain.ArchiveJob, error) {
	readCtx, cancel := context.WithTimeout(ctx, archiveReadTimeout)
	defer cancel()
	jobs, jobsErr, err := collect.ArchiveJobs(readCtx, g.db, g.database.Name, true)
	switch {
	case err != nil:
		return nil, newError(KindUnreachable, "could not read kakapo's archive jobs on %s: %v", g.srv.Name, err)
	case jobsErr != "":
		return nil, newError(KindUnreachable, "could not read the Agent jobs in msdb on %s: %s", g.srv.Name, jobsErr)
	}
	return jobs, nil
}

func prepareRemoveArchive(ctx context.Context, s *Service, g *genericFix, def domain.FixDef, options map[string]string) error {
	schema, table, err := archiveTableTarget(options, g.database.Name)
	if err != nil {
		return err
	}
	jobs, err := readArchiveJobs(ctx, g)
	if err != nil {
		return err
	}
	g.preview, g.fix, err = domain.BuildRemoveArchiveFix(jobs, g.database.Name, schema, table, g.srv.Name, g.srv.LastVersion)
	if errors.Is(err, domain.ErrNoArchiveJob) {
		return newError(KindNotFound, "nothing to remove: kakapo does not archive %s.%s in %s", schema, table, g.database.Name)
	}
	return err
}

func applyRemoveArchive(ctx context.Context, s *Service, serverID int64, g *genericFix) (domain.FixResult, error) {
	name, p, server := g.database.Name, g.preview, g.srv.Name
	if p.Statement == "" {
		return domain.FixResult{}, notApplicable(p, "the job cannot be deleted as previewed")
	}
	schema, table, err := archiveTableTarget(p.Options, name)
	if err != nil {
		return domain.FixResult{}, err
	}
	jobName := g.fix.Current
	if err := collect.ExecFix(ctx, g.db, p.Statement); err != nil {
		return domain.FixResult{}, newError(KindUnreachable, "SQL Server refused to delete the job %s on %s: %v. %s",
			jobName, server, err, domain.RemoveFixPermission(server))
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)
	change := domain.Change{
		ServerID:    serverID,
		Database:    name,
		ChangedAt:   appliedAt,
		Object:      domain.ArchiveTableObject(schema, table),
		ChangeType:  "ARCHIVE",
		Description: domain.RemoveArchiveDescription(jobName, schema, table),
		Statement:   p.Statement,
		Rollback:    p.Rollback,
	}
	states, err := collect.ReadJobsByName(ctx, g.db, []string{jobName})
	if err != nil {
		change.Result = "applied, but the job could not be read back: " + err.Error()
		if _, logErr := s.insertChange(ctx, change); logErr != nil {
			return domain.FixResult{}, logErr
		}
		return domain.FixResult{}, newError(KindUnreachable, "the job %s was deleted on %s, but that could not be read back: %v", jobName, server, err)
	}
	after, verified := domain.RemoveArchiveReadBack(jobName, states)
	change.Result = after + ", differs"
	if verified {
		change.Result = after + ", verified"
	}
	id, err := s.insertChange(ctx, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	return domain.FixResult{Preview: p, Before: jobName, After: after, Verified: verified, ChangeID: id, AppliedAt: appliedAt}, nil
}

// ListTables lists the user tables of a database for the archive picker (docs/phase5a-contract.md):
// rows, MB, date columns and whether kakapo archives each, biggest first. An unknown server or
// database is KindNotFound; a server that cannot be read is KindUnreachable.
func (s *Service) ListTables(ctx context.Context, serverID int64, dbName string) (domain.TableList, error) {
	if strings.TrimSpace(dbName) == "" {
		return domain.TableList{}, newError(KindNotFound, "a database name is needed")
	}
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return domain.TableList{}, err
	}
	defer db.Close()
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return domain.TableList{}, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	if d == nil {
		return domain.TableList{}, newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name)
	}
	if d.State != "ONLINE" {
		return domain.TableList{}, newError(KindConflict, "%s is %s, not online, so its tables cannot be listed", d.Name, d.State)
	}
	readCtx, cancel := context.WithTimeout(ctx, archiveReadTimeout)
	defer cancel()
	list, err := collect.ListTables(readCtx, db, d.Name)
	if err != nil {
		return domain.TableList{}, newError(KindUnreachable, "could not list the tables of %s on %s: %v", d.Name, srv.Name, err)
	}
	return list, nil
}
