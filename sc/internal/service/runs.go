package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// Long fixes run in the background (docs/phase4f-contract.md, part 1). ApplyFixWithOptions prepares
// a long fix as any other, then hands the prepared fix (and its connection) to a goroutine that runs
// the fix's own apply function unchanged and keeps the outcome on a run here; the request answers
// at once with a pending FixResult. The registry is in memory, per process: at most maxRuns runs,
// an ended run kept for runKeep. Nothing is persisted here: the stored CHECKDB outcome, the change
// rows and the read-backs already are.
//
// While the apply has not returned, the run watches the server: when kakapo's own request has been
// seen and then gone for runQuietAfter, the connection is taken as lost (the apply would wait for a
// message that never comes), and the fix's completion check reads the outcome back. The run then
// writes the change row (and CHECKDB's stored outcome) as the apply would, with lostConnectionNote,
// and cancels the apply so the dead connection is closed.

const (
	maxRuns       = 20
	runKeep       = 24 * time.Hour
	runPollEvery  = 10 * time.Second
	runQuietAfter = 60 * time.Second
	// runCloseWait is how long a run waits for a cancelled apply to return before it stops waiting
	// to close the connection (the driver gives up on a dead connection within seconds).
	runCloseWait = 30 * time.Second
	// runReadMargin is added to a long fix's statement timeout for the reads and the logging around
	// it: the run's outer bound.
	runReadMargin = 10 * time.Minute
)

// lostConnectionNote is what a result read back after a lost connection says.
const lostConnectionNote = "the connection was lost during the run; the result was read back afterwards"

// Busy is told when a long fix starts running in the background and when it ends, so the app stays
// up while one runs (presence counts a pending run as a window).
type Busy interface {
	Busy()
	Idle()
}

// SetBusy sets what is told about runs starting and ending; nil tells nothing.
func (s *Service) SetBusy(b Busy) {
	s.runs.mu.Lock()
	defer s.runs.mu.Unlock()
	s.runs.busy = b
}

// FixRun is one long fix's run: pending until the apply returns (or its outcome is read back after a
// lost connection), then its Result or its Err (a *Error, as the apply would have answered it).
type FixRun struct {
	RunID     string
	FixID     string
	Database  string
	StartedAt time.Time
	EndedAt   *time.Time
	Pending   bool
	Result    *domain.FixResult
	Err       error
	// Items are the parts of a batch run (reorganize-indexes), each with its state, set by the fix as it
	// goes; Stopping says a stop was asked for. Both are empty for a fix that is one statement.
	Items    []domain.RunItem
	Stopping bool
}

// completionCheck reads back, after the connection of a long fix's run was lost, whether its
// statement finished on the server: done with what was read back, or not done with the reason in
// rec.after. An error means nothing could be read: the run tries again at its next poll.
type completionCheck func(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (rec recovered, done bool, err error)

// recovered is what a completion check read back: the result's after and verified, the change row as
// the apply writes it (Object, ChangeType, Description, Statement, Rollback; the run fills the rest),
// result for the change row when it is not "{after}, verified|differs", before when the apply's
// before is not the fix's current value, and for a clean CHECKDB the run to store.
type recovered struct {
	after    string
	verified bool
	before   string
	result   string
	change   domain.Change
	checkdb  *domain.CheckdbRun
}

type fixRun struct {
	FixRun
	serverID int64
	cancel   context.CancelFunc // stops the run (docs/phase5t-contract.md); set when the run starts
}

// runRegistry keeps the runs of this process. now, the poll and quiet periods and the progress read
// are fields so a test can drive a run without a SQL Server.
type runRegistry struct {
	mu         sync.Mutex
	runs       map[string]*fixRun
	busy       Busy
	now        func() time.Time
	pollEvery  time.Duration
	quietAfter time.Duration
	closeWait  time.Duration
	progress   func(ctx context.Context, g *genericFix) ([]collect.Progress, error)
}

func newRunRegistry() *runRegistry {
	return &runRegistry{
		runs:       map[string]*fixRun{},
		now:        time.Now,
		pollEvery:  runPollEvery,
		quietAfter: runQuietAfter,
		closeWait:  runCloseWait,
		progress: func(ctx context.Context, g *genericFix) ([]collect.Progress, error) {
			return collect.ReadProgress(ctx, g.db, g.database.Name)
		},
	}
}

// add starts a pending run, making room by dropping expired runs and then the oldest ended one;
// KindConflict when maxRuns are all pending.
func (r *runRegistry) add(serverID int64, fixID, database string) (*fixRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.pruneLocked(now)
	if len(r.runs) >= maxRuns {
		var oldest *fixRun
		for _, x := range r.runs {
			if !x.Pending && (oldest == nil || x.EndedAt.Before(*oldest.EndedAt)) {
				oldest = x
			}
		}
		if oldest == nil {
			return nil, newError(KindConflict, "%d fixes are already running from this kakapo: wait for one to finish", maxRuns)
		}
		delete(r.runs, oldest.RunID)
	}
	run := &fixRun{serverID: serverID, FixRun: FixRun{RunID: newRunID(), FixID: fixID, Database: database,
		StartedAt: now.UTC().Truncate(time.Microsecond), Pending: true}}
	r.runs[run.RunID] = run
	if r.busy != nil {
		r.busy.Busy()
	}
	return run, nil
}

// finish ends a pending run with its result or error, once: false when it had already ended.
func (r *runRegistry) finish(id string, res domain.FixResult, err error) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok || !run.Pending {
		return false
	}
	ended := r.now().UTC().Truncate(time.Microsecond)
	run.Pending, run.EndedAt = false, &ended
	if err != nil {
		run.Err = err
	} else {
		run.Result = &res
	}
	if r.busy != nil {
		r.busy.Idle()
	}
	return true
}

// get returns a copy of a run of this server and database.
func (r *runRegistry) get(serverID int64, database, id string) (FixRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(r.now())
	run, ok := r.runs[id]
	if !ok || run.serverID != serverID || !strings.EqualFold(run.Database, database) {
		return FixRun{}, false
	}
	out := run.FixRun
	out.Items = append([]domain.RunItem(nil), run.Items...)
	return out, true
}

// pendingFor says whether a run of this fix is pending on this server and database.
func (r *runRegistry) pendingFor(serverID int64, fixID, database string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range r.runs {
		if x.Pending && x.serverID == serverID && x.FixID == fixID && strings.EqualFold(x.Database, database) {
			return true
		}
	}
	return false
}

// attach gives a run the function that stops it.
func (r *runRegistry) attach(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if x, ok := r.runs[id]; ok {
		x.cancel = cancel
	}
}

// setItems replaces a run's items (a batch fix reports each part as it goes); no run, no effect.
func (r *runRegistry) setItems(id string, items []domain.RunItem) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if x, ok := r.runs[id]; ok {
		x.Items = append([]domain.RunItem(nil), items...)
	}
}

// stopRequested says whether a stop was asked for.
func (r *runRegistry) stopRequested(id string) bool {
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	x, ok := r.runs[id]
	return ok && x.Stopping
}

// stop asks a pending run to stop: its context is cancelled, which stops the statement in flight.
func (r *runRegistry) stop(serverID int64, database, id string) (FixRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[id]
	if !ok || run.serverID != serverID || !strings.EqualFold(run.Database, database) {
		return FixRun{}, newError(KindNotFound, "there is no run %q of a fix on %s", id, database)
	}
	if !run.Pending {
		return FixRun{}, newError(KindConflict, "this run has already ended")
	}
	run.Stopping = true
	if run.cancel != nil {
		run.cancel()
	}
	out := run.FixRun
	out.Items = append([]domain.RunItem(nil), run.Items...)
	return out, nil
}

// pruneLocked drops the runs that ended more than runKeep ago.
func (r *runRegistry) pruneLocked(now time.Time) {
	for id, x := range r.runs {
		if !x.Pending && now.Sub(*x.EndedAt) > runKeep {
			delete(r.runs, id)
		}
	}
}

func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// StopFixRun asks a pending run to stop (POST .../fixes/runs/{runId}/stop): the statement in flight is
// cancelled, which keeps the work it did, and a batch marks what it had not started as stopped.
// KindNotFound for an unknown run, KindConflict for one that has ended.
func (s *Service) StopFixRun(ctx context.Context, serverID int64, dbName, runID string) (FixRun, error) {
	return s.runs.stop(serverID, dbName, runID)
}

// FixRunStatus returns one run of a long fix on this server and database. KindNotFound for an
// unknown run (or one of another server or database, one that ended more than 24 hours ago, or one
// from before the exe restarted).
func (s *Service) FixRunStatus(ctx context.Context, serverID int64, dbName, runID string) (FixRun, error) {
	run, ok := s.runs.get(serverID, dbName, runID)
	if !ok {
		return FixRun{}, newError(KindNotFound, "there is no run %q of a fix on %s (runs are kept for 24 hours, and not across a restart)", runID, dbName)
	}
	return run, nil
}

// runsInBackground says whether a prepared fix goes to the background: a long fix whose preview can
// be applied. One that cannot (no changes, an Express script) runs as before, and its apply refuses
// at once.
func runsInBackground(r registeredFix, g *genericFix) bool {
	p := g.preview
	return r.long != nil && len(p.Changes) > 0 && p.Statement != "" && !p.Express
}

// startRun starts a prepared long fix in the background and answers with the pending result. The
// run owns g and closes its connection.
func (s *Service) startRun(ctx context.Context, serverID int64, fixID string, g *genericFix, apply fixApplier, long longFix) (domain.FixResult, error) {
	run, err := s.runs.add(serverID, fixID, g.database.Name)
	if err != nil {
		if g.db != nil {
			g.db.Close()
		}
		return domain.FixResult{}, err
	}
	timeout := long.timeout
	if long.timeoutFor != nil {
		timeout = long.timeoutFor(g)
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	s.runs.attach(run.RunID, cancel)
	g.runID = run.RunID
	go s.runFix(runCtx, cancel, run.FixRun, serverID, g, apply, long)
	started := run.StartedAt
	return domain.FixResult{Preview: g.preview, Before: g.fix.Current, Pending: true, RunID: run.RunID, StartedAt: &started}, nil
}

// runFix runs the apply and keeps its outcome on the run; while the apply has not returned it
// watches for a lost connection (watchRun).
func (s *Service) runFix(ctx context.Context, cancel context.CancelFunc, run FixRun, serverID int64, g *genericFix, apply fixApplier, long longFix) {
	defer cancel()
	type outcome struct {
		res domain.FixResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := apply(ctx, s, serverID, g)
		done <- outcome{res, err}
	}()
	closeDB := func() {
		if g.db != nil {
			g.db.Close()
		}
	}

	w := runWatch{}
	tick := time.NewTicker(s.runs.pollEvery)
	defer tick.Stop()
	for {
		select {
		case o := <-done:
			stopped := s.runs.stopRequested(run.RunID)
			s.runs.finish(run.RunID, o.res, o.err)
			s.endAction(ctx, g.db, g.action, o.res, o.err, stopped)
			closeDB()
			return
		case <-tick.C:
			res, err, ended := s.watchRun(ctx, run, serverID, g, long, &w)
			if !ended {
				continue
			}
			s.runs.finish(run.RunID, res, err)
			// The connection is dead: the ended row's snapshot after is read on a fresh one.
			s.endAction(ctx, nil, g.action, res, err, false)
			// Stop the apply waiting on the dead connection, then close it once the apply returns
			// (sql.DB.Close waits for the queries in progress).
			cancel()
			select {
			case <-done:
				closeDB()
			case <-time.After(s.runs.closeWait):
			}
			return
		}
	}
}

// runWatch is what a run has seen of its own request on the server.
type runWatch struct {
	seen     bool      // kakapo's own request was seen at least once
	lastSeen time.Time // when it was last seen
}

// watchRun reads the requests in progress once. When kakapo's own request was seen and has been gone
// for quietAfter, the fix's completion check decides: ended with the result read back (and logged),
// or with an error saying the statement did not finish. A failed read, or a check that could not
// read, decides nothing. A run whose request was never seen (a login without VIEW SERVER STATE sees
// only its own session) is never ended here: the fix's timeout is its bound.
func (s *Service) watchRun(ctx context.Context, run FixRun, serverID int64, g *genericFix, long longFix, w *runWatch) (domain.FixResult, error, bool) {
	rows, err := s.runs.progress(ctx, g)
	if err != nil {
		return domain.FixResult{}, nil, false
	}
	now := s.runs.now()
	for _, p := range rows {
		if p.Mine {
			w.seen, w.lastSeen = true, now
			return domain.FixResult{}, nil, false
		}
	}
	if !w.seen || now.Sub(w.lastSeen) < s.runs.quietAfter {
		return domain.FixResult{}, nil, false
	}
	rec, done, err := long.completed(ctx, s, g, run.StartedAt)
	if err != nil {
		return domain.FixResult{}, nil, false
	}
	if !done {
		return domain.FixResult{}, newError(KindUnreachable, "the connection to %s was lost during the run, and %s", g.srv.Name, rec.after), true
	}
	res, err := s.logRecovered(ctx, serverID, g, run, rec)
	return res, err, true
}

// logRecovered writes what a completion check read back as the apply would have: CHECKDB's stored
// outcome when it is known, and the change row, both saying the connection was lost.
func (s *Service) logRecovered(ctx context.Context, serverID int64, g *genericFix, run FixRun, rec recovered) (domain.FixResult, error) {
	keep := context.WithoutCancel(ctx)
	name := g.database.Name
	if rec.checkdb != nil {
		if err := s.store.InsertCheckdbRun(keep, serverID, name, *rec.checkdb); err != nil {
			return domain.FixResult{}, err
		}
	}
	change := rec.change
	change.ServerID, change.Database, change.ChangedAt = serverID, name, run.StartedAt
	result := rec.result
	if result == "" {
		result = rec.after + ", differs"
		if rec.verified {
			result = rec.after + ", verified"
		}
	}
	change.Result = result + " (" + lostConnectionNote + ")"
	id, err := s.insertChange(keep, change)
	if err != nil {
		return domain.FixResult{}, err
	}
	before := rec.before
	if before == "" {
		before = g.fix.Current
	}
	return domain.FixResult{Preview: g.preview, Before: before, After: rec.after + " (" + lostConnectionNote + ")",
		Verified: rec.verified, ChangeID: id, AppliedAt: run.StartedAt}, nil
}

// serverStart is a run's start on the server's clock, from a server time read now: it takes in the
// difference between the clocks (and the server's time zone, as kakapo keeps server times naive).
func serverStart(startedAt, serverNow, localNow time.Time) time.Time {
	return startedAt.Add(serverNow.Sub(localNow))
}
