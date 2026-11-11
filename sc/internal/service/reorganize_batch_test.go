package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The batch's registration and the run additions of docs/phase5t-contract.md (items, stop, one at a
// time), driven with a fake apply as runs_test.go does; nothing here dials a SQL Server.

func TestReorganizeIndexesRegistered(t *testing.T) {
	r, ok := registeredFixFor(domain.FixReorganizeIndexes)
	if !ok || r.long == nil || r.long.completed == nil || r.long.timeoutFor == nil {
		t.Fatal("reorganize-indexes is not registered as a long fix with its own bound")
	}
	// One index's bound for each index, and the margin: a batch is not cut off at the single-index bound.
	one := r.long.timeoutFor(&genericFix{preview: domain.FixPreview{Changes: make([]domain.Drift, 1)}})
	ten := r.long.timeoutFor(&genericFix{preview: domain.FixPreview{Changes: make([]domain.Drift, 10)}})
	if ten-one != 9*indexApplyTimeout || one != indexApplyTimeout+runReadMargin {
		t.Errorf("bounds %v and %v", one, ten)
	}
	// After a lost connection it does not resume.
	rec, done, err := r.long.completed(context.Background(), nil, nil, time.Now())
	if err != nil || done || rec.after == "" {
		t.Errorf("completion check %+v %v %v", rec, done, err)
	}
}

func TestRunItemsAndStop(t *testing.T) {
	s, serverID, g := runService(t)
	started := make(chan struct{})
	apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
		items := []domain.RunItem{{Label: "dbo.A · IX_A", State: domain.ItemRunning}, {Label: "dbo.B · IX_B", State: domain.ItemWaiting}}
		s.runs.setItems(g.runID, items)
		close(started)
		<-ctx.Done()
		// A stop is a request, not a timeout: the batch can tell the difference and still write its rows.
		if !s.runs.stopRequested(g.runID) {
			t.Error("the context ended without a stop")
		}
		items[0].State, items[1].State = domain.ItemStopped, domain.ItemStopped
		s.runs.setItems(g.runID, items)
		summary, ok := domain.BatchSummary(items)
		return domain.FixResult{Preview: g.preview, After: summary, Verified: ok}, nil
	}
	res, err := s.startRun(context.Background(), serverID, "fake-batch", g, apply, longFix{timeout: time.Minute, completed: neverCompleted})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	run, err := s.FixRunStatus(context.Background(), serverID, "WMS", res.RunID)
	if err != nil || !run.Pending || len(run.Items) != 2 || run.Items[0].State != domain.ItemRunning || run.Stopping {
		t.Fatalf("while running: %+v %v", run, err)
	}
	// What the caller got is a copy: changing it does not change the run.
	run.Items[0].State = "tampered"
	if again, _ := s.FixRunStatus(context.Background(), serverID, "WMS", res.RunID); again.Items[0].State != domain.ItemRunning {
		t.Error("the run's items are shared")
	}
	if !s.runs.pendingFor(serverID, "fake-batch", "wms") || s.runs.pendingFor(serverID, "other", "WMS") || s.runs.pendingFor(serverID+1, "fake-batch", "WMS") {
		t.Error("pendingFor")
	}

	// Another database or an unknown id cannot stop it.
	var se *Error
	for _, c := range []struct{ db, id string }{{"Other", res.RunID}, {"WMS", "nope"}} {
		if _, err := s.StopFixRun(context.Background(), serverID, c.db, c.id); !errors.As(err, &se) || se.Kind != KindNotFound {
			t.Errorf("%+v: want KindNotFound, got %v", c, err)
		}
	}
	stopped, err := s.StopFixRun(context.Background(), serverID, "WMS", res.RunID)
	if err != nil || !stopped.Stopping {
		t.Fatalf("stop %+v %v", stopped, err)
	}
	ended := waitRun(t, s, serverID, res.RunID)
	if ended.Err != nil || ended.Result == nil || ended.Result.After != "2 not done (stopped)" || ended.Result.Verified || !ended.Stopping ||
		len(ended.Items) != 2 || ended.Items[1].State != domain.ItemStopped {
		t.Errorf("ended %+v", ended)
	}
	// A run that has ended cannot be stopped again, and is no longer pending.
	if _, err := s.StopFixRun(context.Background(), serverID, "WMS", res.RunID); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("stop of an ended run: %v", err)
	}
	if s.runs.pendingFor(serverID, "fake-batch", "WMS") {
		t.Error("an ended run is pending")
	}
	if s.runs.stopRequested("") || s.runs.stopRequested("nope") {
		t.Error("stop requested for no run")
	}
	s.runs.setItems("", nil) // no run, no effect
}
