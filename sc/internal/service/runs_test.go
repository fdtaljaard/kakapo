package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/store"
)

// The run registry and the background run of docs/phase4f-contract.md, driven by fake long fixes
// whose apply blocks on a channel; nothing here dials a SQL Server (the genericFix has no
// connection, and the progress read is a fake).

type countBusy struct {
	mu         sync.Mutex
	busy, idle int
}

func (c *countBusy) Busy() { c.mu.Lock(); c.busy++; c.mu.Unlock() }
func (c *countBusy) Idle() { c.mu.Lock(); c.idle++; c.mu.Unlock() }
func (c *countBusy) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.busy, c.idle
}

// runService is a service on a fresh store with one registered server, and the fix that runs.
func runService(t *testing.T) (*Service, int64, *genericFix) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "client", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	g := &genericFix{srv: srv, database: domain.DatabaseStatus{Name: "WMS", State: "ONLINE"},
		preview: domain.FixPreview{Database: "WMS", Changes: []domain.Drift{{Setting: "CHECKDB", Current: "never", Target: "run now"}},
			Statement: "DBCC CHECKDB (N'WMS') WITH NO_INFOMSGS, ALL_ERRORMSGS;", Warnings: []string{}},
		fix: domain.Fix{ID: "fake-long", Current: "never"}}
	return New(st, fakeCreds{}, "test"), srv.ID, g
}

// waitRun polls a run until it ends.
func waitRun(t *testing.T, s *Service, serverID int64, id string) FixRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := s.FixRunStatus(context.Background(), serverID, "WMS", id)
		if err != nil {
			t.Fatal(err)
		}
		if !run.Pending {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run did not end")
	return FixRun{}
}

func TestLongFixRunsInTheBackground(t *testing.T) {
	s, serverID, g := runService(t)
	busy := &countBusy{}
	s.SetBusy(busy)
	release := make(chan struct{})
	var applyCtx context.Context
	var errAtRelease error
	apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
		applyCtx = ctx
		<-release
		errAtRelease = ctx.Err()
		return domain.FixResult{Preview: g.preview, Before: "never", After: domain.CheckdbOutcomeClean, Verified: true, ChangeID: 7}, nil
	}
	ctx, cancelRequest := context.WithCancel(context.Background())
	res, err := s.startRun(ctx, serverID, "fake-long", g, apply, longFix{timeout: time.Minute, completed: neverCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending || res.RunID == "" || res.StartedAt == nil || res.Preview.Statement != g.preview.Statement || res.Before != "never" {
		t.Fatalf("pending result %+v", res)
	}
	// The page that asked goes away: the run goes on.
	cancelRequest()

	run, err := s.FixRunStatus(context.Background(), serverID, "WMS", res.RunID)
	if err != nil || !run.Pending || run.Result != nil || run.Err != nil || run.FixID != "fake-long" || !run.StartedAt.Equal(*res.StartedAt) {
		t.Fatalf("while running: %+v %v", run, err)
	}
	if b, i := busy.counts(); b != 1 || i != 0 {
		t.Errorf("busy %d, idle %d while running", b, i)
	}
	// Another server or database, or an unknown id, is not this run.
	var se *Error
	for _, c := range []struct {
		server int64
		db, id string
	}{{serverID + 1, "WMS", res.RunID}, {serverID, "Other", res.RunID}, {serverID, "WMS", "nope"}} {
		if _, err := s.FixRunStatus(context.Background(), c.server, c.db, c.id); !errors.As(err, &se) || se.Kind != KindNotFound {
			t.Errorf("%+v: want KindNotFound, got %v", c, err)
		}
	}
	if _, err := s.FixRunStatus(context.Background(), serverID, "wms", res.RunID); err != nil {
		t.Errorf("the database name is matched as SQL Server does: %v", err)
	}

	close(release)
	run = waitRun(t, s, serverID, res.RunID)
	if run.Err != nil || run.Result == nil || run.Result.After != domain.CheckdbOutcomeClean || run.Result.ChangeID != 7 || run.EndedAt == nil {
		t.Fatalf("ended: %+v", run)
	}
	if errAtRelease != nil {
		t.Errorf("the request going away reached the apply: %v", errAtRelease)
	}
	if _, ok := applyCtx.Deadline(); !ok {
		t.Error("the apply runs without the fix's own timeout")
	}
	if b, i := busy.counts(); b != 1 || i != 1 {
		t.Errorf("busy %d, idle %d after the run", b, i)
	}
}

func TestLongFixRunError(t *testing.T) {
	s, serverID, g := runService(t)
	apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
		return domain.FixResult{}, newError(KindUnreachable, "DBCC CHECKDB on WMS did not complete: permission denied")
	}
	res, err := s.startRun(context.Background(), serverID, "fake-long", g, apply, longFix{timeout: time.Minute, completed: neverCompleted})
	if err != nil {
		t.Fatal(err)
	}
	run := waitRun(t, s, serverID, res.RunID)
	var se *Error
	if run.Result != nil || !errors.As(run.Err, &se) || se.Kind != KindUnreachable || !strings.Contains(se.Msg, "permission denied") {
		t.Errorf("ended: %+v", run)
	}
}

func neverCompleted(context.Context, *Service, *genericFix, time.Time) (recovered, bool, error) {
	return recovered{}, false, errors.New("not read")
}

func TestRunRegistryCapAndExpiry(t *testing.T) {
	r := newRunRegistry()
	now := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	busy := &countBusy{}
	r.busy = busy

	ids := []string{}
	for i := 0; i < maxRuns; i++ {
		run, err := r.add(1, "checkdb-now", "WMS")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.RunID)
		now = now.Add(time.Second)
	}
	var se *Error
	if _, err := r.add(1, "checkdb-now", "WMS"); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Fatalf("the 21st pending run: want KindConflict, got %v", err)
	}
	// An ended run makes room: the oldest ended one goes.
	if !r.finish(ids[3], domain.FixResult{After: "x"}, nil) || r.finish(ids[3], domain.FixResult{}, nil) {
		t.Error("a run ends once")
	}
	now = now.Add(time.Second)
	if !r.finish(ids[1], domain.FixResult{After: "y"}, nil) {
		t.Fatal("finish")
	}
	if _, err := r.add(1, "create-index", "WMS"); err != nil {
		t.Fatalf("with an ended run: %v", err)
	}
	if _, ok := r.get(1, "WMS", ids[3]); ok {
		t.Error("the oldest ended run was kept")
	}
	if run, ok := r.get(1, "WMS", ids[1]); !ok || run.Result == nil || run.Result.After != "y" {
		t.Errorf("the newer ended run went: %+v %v", run, ok)
	}
	if len(r.runs) != maxRuns {
		t.Errorf("%d runs kept", len(r.runs))
	}
	// An ended run is kept for 24 hours, a pending one as long as it runs.
	now = now.Add(runKeep)
	if _, ok := r.get(1, "WMS", ids[1]); !ok {
		t.Error("an ended run went before 24 hours")
	}
	now = now.Add(time.Minute)
	if _, ok := r.get(1, "WMS", ids[1]); ok {
		t.Error("an ended run was kept past 24 hours")
	}
	if run, ok := r.get(1, "WMS", ids[0]); !ok || !run.Pending {
		t.Error("a pending run expired")
	}
	if b, i := busy.counts(); b != maxRuns+1 || i != 2 {
		t.Errorf("busy %d, idle %d", b, i)
	}
}

// lostConnection is a long fix whose apply hangs until its context ends, as one waiting on a dead
// connection does, with the progress read showing kakapo's request once and then nothing.
func lostConnection(t *testing.T, completed completionCheck) (*Service, int64, FixRun, chan error) {
	t.Helper()
	s, serverID, g := runService(t)
	s.runs.pollEvery, s.runs.quietAfter, s.runs.closeWait = 5*time.Millisecond, 30*time.Millisecond, time.Second
	var mu sync.Mutex
	reads := 0
	s.runs.progress = func(ctx context.Context, g *genericFix) ([]collect.Progress, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		switch {
		case reads == 1:
			return nil, errors.New("the read timed out") // decides nothing
		case reads <= 3:
			return []collect.Progress{{SessionID: 71, Command: "DBCC TABLE CHECK", Database: "WMS", Percent: 99, Mine: true}}, nil
		}
		return []collect.Progress{{SessionID: 80, Command: "BACKUP DATABASE", Database: "WMS", Percent: 10}}, nil // someone else's
	}
	stopped := make(chan error, 1)
	apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
		<-ctx.Done()
		stopped <- ctx.Err()
		return domain.FixResult{}, newError(KindUnreachable, "DBCC CHECKDB on WMS did not complete: %v", ctx.Err())
	}
	res, err := s.startRun(context.Background(), serverID, domain.FixCheckdbNow, g, apply, longFix{timeout: time.Minute, completed: completed})
	if err != nil {
		t.Fatal(err)
	}
	return s, serverID, waitRun(t, s, serverID, res.RunID), stopped
}

func TestLostConnectionReadBack(t *testing.T) {
	var checkedStart time.Time
	completed := func(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
		checkedStart = startedAt
		run, _ := domain.BuildCheckdbRun(startedAt, time.Minute, false, nil, "")
		return recovered{after: run.Outcome, verified: true, result: run.Outcome, checkdb: &run,
			change: domain.Change{Object: "CHECKDB", ChangeType: "CHECK", Description: "DBCC CHECKDB on WMS", Statement: g.preview.Statement}}, true, nil
	}
	s, serverID, run, stopped := lostConnection(t, completed)
	if run.Err != nil || run.Result == nil || !run.Result.Verified || !strings.HasPrefix(run.Result.After, domain.CheckdbOutcomeClean+" (") ||
		!strings.Contains(run.Result.After, lostConnectionNote) || run.Result.ChangeID == 0 || !run.Result.AppliedAt.Equal(run.StartedAt) {
		t.Fatalf("read back: %+v", run.Result)
	}
	if !checkedStart.Equal(run.StartedAt) {
		t.Errorf("the check got %v, the run started %v", checkedStart, run.StartedAt)
	}
	// The hung apply is cancelled, so its dead connection is closed.
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("apply stopped by %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hung apply was not cancelled")
	}
	ctx := context.Background()
	changes, err := s.store.ListChanges(ctx, serverID, "WMS", 10)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes %+v %v", changes, err)
	}
	if c := changes[0]; c.Object != "CHECKDB" || c.ChangeType != "CHECK" || c.Result != domain.CheckdbOutcomeClean+" ("+lostConnectionNote+")" ||
		!c.ChangedAt.Equal(run.StartedAt) {
		t.Errorf("change row %+v", c)
	}
	stored, err := s.store.LastCheckdbRun(ctx, serverID, "WMS")
	if err != nil || stored == nil || !stored.Clean {
		t.Errorf("stored run %+v %v", stored, err)
	}
	// The cancelled apply's own error does not replace the result read back.
	time.Sleep(20 * time.Millisecond)
	if again, _ := s.FixRunStatus(ctx, serverID, "WMS", run.RunID); again.Err != nil || again.Result == nil {
		t.Errorf("after the apply returned: %+v", again)
	}
}

func TestLostConnectionNotFinished(t *testing.T) {
	completed := func(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
		return recovered{after: "the index IX_a is not on dbo.T: the build did not finish; apply it again"}, false, nil
	}
	s, serverID, run, _ := lostConnection(t, completed)
	var se *Error
	if run.Result != nil || !errors.As(run.Err, &se) || se.Kind != KindUnreachable ||
		se.Msg != "the connection to client was lost during the run, and the index IX_a is not on dbo.T: the build did not finish; apply it again" {
		t.Errorf("ended: %+v", run)
	}
	if changes, err := s.store.ListChanges(context.Background(), serverID, "WMS", 10); err != nil || len(changes) != 0 {
		t.Errorf("a statement that did not finish logged %+v %v", changes, err)
	}
}

// A run whose request was never seen (a login without VIEW SERVER STATE sees no other session) is
// left to its own timeout: the check is never asked.
func TestRunNeverSeenIsNotReadBack(t *testing.T) {
	s, serverID, g := runService(t)
	s.runs.pollEvery, s.runs.quietAfter = time.Millisecond, time.Millisecond
	s.runs.progress = func(context.Context, *genericFix) ([]collect.Progress, error) { return []collect.Progress{}, nil }
	asked := make(chan struct{}, 1)
	completed := func(context.Context, *Service, *genericFix, time.Time) (recovered, bool, error) {
		asked <- struct{}{}
		return recovered{after: "x"}, true, nil
	}
	release := make(chan struct{})
	apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
		<-release
		return domain.FixResult{After: "done"}, nil
	}
	res, err := s.startRun(context.Background(), serverID, "fake-long", g, apply, longFix{timeout: time.Minute, completed: completed})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case <-asked:
		t.Error("the check was asked without the request ever seen")
	default:
	}
	close(release)
	if run := waitRun(t, s, serverID, res.RunID); run.Result == nil || run.Result.After != "done" {
		t.Errorf("ended: %+v", run)
	}
}

func TestLongFixSet(t *testing.T) {
	for _, id := range []string{domain.FixCheckdbNow, domain.FixCreateIndex, domain.FixDropIndex, domain.FixUpdateStatistics,
		domain.FixUpdateStatisticsTable, domain.FixTempdbFiles} {
		r, ok := registeredFixFor(id)
		if !ok || r.long == nil || r.long.timeout <= 0 || r.long.completed == nil {
			t.Errorf("%s is not registered as a long fix", id)
		}
	}
	// The backup schedule's run-now starts an Agent job and stays synchronous; the short fixes too.
	for _, id := range []string{domain.FixRecoveryModel, domain.FixCheckdbJob, domain.FixAutogrowth, domain.FixAddTempdbFiles, domain.FixForcePlan} {
		if r, ok := registeredFixFor(id); !ok || r.long != nil {
			t.Errorf("%s: registered %v, long %v", id, ok, ok && r.long != nil)
		}
	}
	if _, ok := registeredFixFor("backup-schedule"); ok {
		t.Error("backup-schedule joined the registry")
	}
	if r, _ := registeredFixFor(domain.FixCheckdbNow); r.long.timeout < domain.CheckdbRunTimeout {
		t.Errorf("the CHECKDB run is bounded by %v, under its 4 hours", r.long.timeout)
	}

	// Only a preview that can be applied goes to the background; one that cannot refuses at once.
	r, _ := registeredFixFor(domain.FixCreateIndex)
	g := &genericFix{preview: domain.FixPreview{Changes: []domain.Drift{{Setting: "x"}}, Statement: "CREATE INDEX ..."}}
	if !runsInBackground(r, g) {
		t.Error("an applicable long fix runs synchronously")
	}
	for _, p := range []domain.FixPreview{{Statement: "CREATE INDEX ..."}, {Changes: []domain.Drift{{Setting: "x"}}},
		{Changes: []domain.Drift{{Setting: "x"}}, Statement: "x", Express: true}} {
		if runsInBackground(r, &genericFix{preview: p}) {
			t.Errorf("%+v goes to the background", p)
		}
	}
	short, _ := registeredFixFor(domain.FixRecoveryModel)
	if runsInBackground(short, g) {
		t.Error("a short fix goes to the background")
	}
}

func TestServerStart(t *testing.T) {
	local := time.Date(2026, 11, 4, 6, 0, 0, 0, time.UTC)
	// The server's clock reads two hours ahead (its time zone, kept naive) and 5 seconds fast.
	server := local.Add(2*time.Hour + 5*time.Second)
	started := local.Add(-10 * time.Minute)
	if got := serverStart(started, server, local); !got.Equal(started.Add(2*time.Hour + 5*time.Second)) {
		t.Errorf("server start %v", got)
	}
}
