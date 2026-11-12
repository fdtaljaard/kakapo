package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// Kind says what went wrong, so a caller (the API) can answer in its own terms.
type Kind int

const (
	KindNotFound    Kind = iota + 1 // the server or database does not exist
	KindConflict                    // nothing to apply, or the database cannot be fixed from here
	KindUnreachable                 // SQL Server could not be reached, or refused
	KindInvalid                     // the input itself is wrong; nothing was tried
)

// Error is a failure the user can act on, with its kind.
type Error struct {
	Kind Kind
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func newError(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// connect opens a connection to a registered server: Windows auth needs nothing more,
// a SQL login gets its password back from the credential store. The caller closes it.
func (s *Service) connect(ctx context.Context, serverID int64) (*sql.DB, domain.Server, error) {
	srv, err := s.store.GetServer(ctx, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, srv, newError(KindNotFound, "server %d is not registered", serverID)
	}
	if err != nil {
		return nil, srv, err
	}
	// The remembered database is where the UI opens, not the connection's initial catalog:
	// every read names its database explicitly, and a connection must still work when that
	// database is offline, dropped or no longer readable by this login.
	in := domain.ServerInput{Name: srv.Name, Host: srv.Host, Auth: srv.Auth, Username: srv.Username,
		Encrypt: srv.Encrypt, TrustCert: srv.TrustCert}
	if srv.Auth == domain.AuthSQL {
		pw, err := s.creds.Get(serverID)
		if err != nil {
			return nil, srv, newError(KindUnreachable,
				"cannot log in to %s as %s: the password could not be read from Windows Credential Manager (%v)", srv.Name, srv.Username, err)
		}
		in.Password = pw
	}
	db, err := collect.Open(ctx, in)
	if err != nil {
		return nil, srv, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	return db, srv, nil
}

// ListDatabases reads the user databases of a server and their Query Store state now,
// and keeps one snapshot per database that could be read.
func (s *Service) ListDatabases(ctx context.Context, serverID int64) ([]domain.DatabaseStatus, error) {
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	version, err := collect.EngineVersion(ctx, db)
	if err != nil {
		return nil, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	return s.readDatabases(ctx, db, srv, version)
}

// readDatabases lists the user databases on an open connection, reads each one's Query Store,
// and stores one snapshot per database that could be read. The database picker uses it.
func (s *Service) readDatabases(ctx context.Context, db *sql.DB, srv domain.Server, version string) ([]domain.DatabaseStatus, error) {
	serverID := srv.ID
	dbs, err := collect.ListDatabases(ctx, db)
	if err != nil {
		return nil, newError(KindUnreachable, "%s: %v", srv.Name, err)
	}
	for i := range dbs {
		readQueryStore(ctx, db, version, &dbs[i])
		if dbs[i].QueryStore == nil {
			continue
		}
		if err := s.store.InsertQueryStoreSnapshot(ctx, serverID, dbs[i].Name, *dbs[i].QueryStore); err != nil {
			return nil, err
		}
	}
	return dbs, nil
}

// readQueryStore fills in d.QueryStore, or d.Error when it cannot be read.
func readQueryStore(ctx context.Context, db *sql.DB, version string, d *domain.DatabaseStatus) {
	if d.State != "ONLINE" {
		d.Error = fmt.Sprintf("the database is %s; Query Store can be read only when it is online", d.State)
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	o, err := collect.QueryStoreOptions(readCtx, db, d.Name, domain.EngineMajor(version))
	if err != nil {
		d.Error = "could not read the Query Store options: " + err.Error()
		return
	}
	st := domain.NewQueryStoreStatus(o, version, time.Now().UTC().Truncate(time.Microsecond), d.CanFix())
	d.QueryStore = &st
}

// fixContext is everything a preview reads before it builds a statement.
type fixContext struct {
	db       *sql.DB
	srv      domain.Server
	version  string
	database domain.DatabaseStatus
	options  domain.QueryStoreOptions
	preview  domain.FixPreview
}

// prepareFix connects, finds the database, reads its options and builds the fix.
// It refuses a database that cannot be fixed from here. The caller closes fc.db.
func (s *Service) prepareFix(ctx context.Context, serverID int64, dbName string) (*fixContext, error) {
	db, srv, err := s.connect(ctx, serverID)
	if err != nil {
		return nil, err
	}
	fc := &fixContext{db: db, srv: srv}
	fail := func(err error) (*fixContext, error) {
		db.Close()
		return nil, err
	}

	if fc.version, err = collect.EngineVersion(ctx, db); err != nil {
		return fail(newError(KindUnreachable, "%s: %v", srv.Name, err))
	}
	d, err := collect.Database(ctx, db, dbName)
	if err != nil {
		return fail(newError(KindUnreachable, "%s: %v", srv.Name, err))
	}
	if d == nil {
		return fail(newError(KindNotFound, "there is no user database named %q on %s", dbName, srv.Name))
	}
	fc.database = *d
	switch {
	case d.IsSecondaryReplica:
		return fail(newError(KindConflict, "%s is a secondary replica here: set Query Store on the primary", d.Name))
	case d.State != "ONLINE":
		return fail(newError(KindConflict, "%s is %s, not online, so Query Store cannot be changed", d.Name, d.State))
	case d.ReadOnly:
		return fail(newError(KindConflict, "%s is read-only, so Query Store cannot be changed", d.Name))
	}

	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if fc.options, err = collect.QueryStoreOptions(readCtx, db, d.Name, domain.EngineMajor(fc.version)); err != nil {
		return fail(newError(KindUnreachable, "could not read the Query Store options of %s: %v", d.Name, err))
	}
	fc.preview = domain.BuildQueryStoreFix(fc.options, domain.EngineMajor(fc.version), d.Name)
	fc.preview.EngineVersion = fc.version
	return fc, nil
}

// PreviewQueryStoreFix says exactly what applying the recommended settings would run.
// It never changes anything.
func (s *Service) PreviewQueryStoreFix(ctx context.Context, serverID int64, dbName string) (domain.FixPreview, error) {
	fc, err := s.prepareFix(ctx, serverID, dbName)
	if err != nil {
		return domain.FixPreview{}, err
	}
	defer fc.db.Close()
	if fc.preview.Statement != "" {
		warnNoChangeLog(ctx, fc.db, domain.FixQueryStore, &fc.preview)
	}
	return fc.preview, nil
}

// ApplyQueryStoreFix runs the statement the preview builds, reads the options back to prove
// it took, keeps both snapshots and writes the change log. This is the only write the app
// makes to a monitored server.
func (s *Service) ApplyQueryStoreFix(ctx context.Context, serverID int64, dbName string) (domain.QueryStoreFixResult, error) {
	fc, err := s.prepareFix(ctx, serverID, dbName)
	if err != nil {
		return domain.QueryStoreFixResult{}, err
	}
	defer fc.db.Close()

	name := fc.database.Name
	if fc.preview.Statement == "" {
		return domain.QueryStoreFixResult{}, newError(KindConflict, "Query Store on %s already has the recommended settings; nothing to apply", name)
	}
	// kakapo's own log (docs/phase5w-contract.md, B2): the started row before the statement, the ended row
	// when it returns.
	def, _ := domain.FindFixDef(domain.FixQueryStore)
	srvName := fc.srv.Name
	beforeSummary := domain.NewQueryStoreStatus(fc.options, fc.version, time.Now().UTC(), true).Summary
	ctx, act, err := s.startAction(ctx, fc.db, actionInfo{serverID: serverID, serverName: srvName, database: name, def: def,
		fix: domain.Fix{Setting: def.Setting}, preview: fc.preview, before: beforeSummary})
	if err != nil {
		return domain.QueryStoreFixResult{}, err
	}
	res, err := s.applyQueryStoreFix(ctx, serverID, fc)
	fr := domain.FixResult{Before: res.Before.Summary, After: res.After.Summary, Verified: res.After.Verdict == domain.VerdictOK, ChangeID: res.ChangeID}
	s.endAction(ctx, fc.db, act, fr, err, false)
	return res, err
}

// applyQueryStoreFix is the phase 1 apply of a prepared Query Store fix.
func (s *Service) applyQueryStoreFix(ctx context.Context, serverID int64, fc *fixContext) (domain.QueryStoreFixResult, error) {
	name := fc.database.Name
	before := domain.NewQueryStoreStatus(fc.options, fc.version, time.Now().UTC().Truncate(time.Microsecond), true)
	if err := s.store.InsertQueryStoreSnapshot(ctx, serverID, name, before); err != nil {
		return domain.QueryStoreFixResult{}, err
	}

	if err := collect.ExecFix(ctx, fc.db, fc.preview.Statement); err != nil {
		return domain.QueryStoreFixResult{}, newError(KindUnreachable, "SQL Server did not apply the change to %s: %v", name, err)
	}
	appliedAt := time.Now().UTC().Truncate(time.Microsecond)

	after, err := readBack(ctx, fc.db, name, domain.EngineMajor(fc.version))
	if err != nil {
		// The statement ran; log it even though we could not prove it took.
		_, logErr := s.insertChange(ctx, s.changeRow(serverID, fc, appliedAt, "applied, but the settings could not be read back: "+err.Error()))
		if logErr != nil {
			return domain.QueryStoreFixResult{}, logErr
		}
		return domain.QueryStoreFixResult{}, newError(KindUnreachable, "the change to %s ran, but the settings could not be read back: %v", name, err)
	}
	afterStatus := domain.NewQueryStoreStatus(after, fc.version, time.Now().UTC().Truncate(time.Microsecond), true)
	if err := s.store.InsertQueryStoreSnapshot(ctx, serverID, name, afterStatus); err != nil {
		return domain.QueryStoreFixResult{}, err
	}

	result := afterStatus.Summary
	if afterStatus.Verdict != domain.VerdictOK {
		parts := []string{}
		for _, d := range afterStatus.Drift {
			parts = append(parts, fmt.Sprintf("%s %s (target %s)", d.Setting, d.Current, d.Target))
		}
		result += "; still differs: " + strings.Join(parts, ", ")
	}
	id, err := s.insertChange(ctx, s.changeRow(serverID, fc, appliedAt, result))
	if err != nil {
		return domain.QueryStoreFixResult{}, err
	}
	return domain.QueryStoreFixResult{Preview: fc.preview, Before: before, After: afterStatus, ChangeID: id, AppliedAt: appliedAt}, nil
}

// readBack reads the options after a fix. The actual state normally follows the desired state at
// once; when it lags, it is read again a few times, half a second apart.
func readBack(ctx context.Context, db *sql.DB, name string, engineMajor int) (domain.QueryStoreOptions, error) {
	for attempt := 1; ; attempt++ {
		o, err := collect.QueryStoreOptions(ctx, db, name, engineMajor)
		if err != nil || o.ActualState == o.DesiredState || attempt == 5 {
			return o, err
		}
		select {
		case <-ctx.Done():
			return o, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Service) changeRow(serverID int64, fc *fixContext, at time.Time, result string) domain.Change {
	return domain.Change{
		ServerID:    serverID,
		Database:    fc.database.Name,
		ChangedAt:   at,
		Object:      "Query Store",
		ChangeType:  "SETTING",
		Description: domain.ChangeDescription(fc.database.Name, fc.preview.Changes),
		Statement:   fc.preview.Statement,
		Rollback:    fc.preview.Rollback,
		Result:      result,
	}
}
