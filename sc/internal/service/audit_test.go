package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// waitEnded polls kakapo's log until the action has its ended row.
func waitEnded(t *testing.T, s *Service, serverID int64, actionID string) domain.ActionLogRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := s.store.ListActionLog(context.Background(), serverID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ActionID == actionID && r.Phase == domain.ActionEnded {
				return r
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the action has no ended row")
	return domain.ActionLogRow{}
}

func auditInfo(serverID int64, g *genericFix) actionInfo {
	def, _ := domain.FindFixDef("cost-threshold")
	return actionInfo{serverID: serverID, serverName: "SQL1", database: g.database.Name, def: def,
		fix: domain.Fix{Setting: "cost threshold for parallelism", Current: "5", Target: "50"}, preview: g.preview, before: "5"}
}

// The started row is written before the statement runs; a statement that fails gets a failed ended row with
// the error; the change rows written under the action are on its ended row.
func TestActionStartedBeforeTheStatement(t *testing.T) {
	s, serverID, g := runService(t)
	ctx := context.Background()
	actx, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
	if err != nil {
		t.Fatal(err)
	}
	// "The statement": by now the started row is there, and no ended row.
	rows, err := s.store.ListActionLog(ctx, serverID)
	if err != nil || len(rows) != 1 || rows[0].Phase != domain.ActionStarted || rows[0].ActionID != act.row.ActionID {
		t.Fatalf("before the statement: %+v %v", rows, err)
	}
	r := rows[0]
	if r.FixID != "cost-threshold" || r.KakapoVersion != "test" || r.ValueBefore != "5" || r.Statement != g.preview.Statement ||
		r.Description != "Cost threshold for parallelism: cost threshold for parallelism from 5 to 50 on SQL1" || r.KakapoHost == "" {
		t.Fatalf("started row %+v", r)
	}
	id, err := s.insertChange(actx, domain.Change{ServerID: serverID, Database: "WMS", ChangedAt: time.Now(), Object: "x", ChangeType: "SETTING",
		Description: "d", Statement: "s", Result: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	s.endAction(actx, nil, act, domain.FixResult{}, newError(KindUnreachable, "SQL Server refused the change"), false)
	end := waitEnded(t, s, serverID, act.row.ActionID)
	if end.Outcome != domain.OutcomeFailed || end.Result != "SQL Server refused the change" || end.ChangeIDs != strconv.FormatInt(id, 10) ||
		end.EndedAt == nil || end.DurationMs == nil || end.ValueAfter != "" {
		t.Fatalf("ended row %+v", end)
	}
	// A change row written outside an action is nobody's.
	if _, err := s.insertChange(ctx, domain.Change{ServerID: serverID, ChangedAt: time.Now(), Object: "y", ChangeType: "OTHER", Description: "d",
		Statement: "s"}); err != nil {
		t.Fatal(err)
	}
	if got := act.ids.list(); len(got) != 1 {
		t.Fatalf("ids %v", got)
	}
}

// A started row that cannot be written refuses the action: nothing runs unlogged.
func TestActionRefusedWhenTheLogCannotBeWritten(t *testing.T) {
	s, serverID, g := runService(t)
	s.store.Close()
	_, act, err := s.startAction(context.Background(), nil, auditInfo(serverID, g))
	var se *Error
	if act != nil || !errors.As(err, &se) || se.Kind != KindConflict {
		t.Fatalf("got %v %v", act, err)
	}
}

// A long run writes its ended row when it ends, when it is stopped, and when its outcome is read back after a
// lost connection.
func TestLongRunEndedRows(t *testing.T) {
	ctx := context.Background()
	t.Run("ends", func(t *testing.T) {
		s, serverID, g := runService(t)
		actx, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
		if err != nil {
			t.Fatal(err)
		}
		g.action = act
		apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
			cid, _ := s.insertChange(ctx, domain.Change{ServerID: id, Database: "WMS", ChangedAt: time.Now(), Object: "CHECKDB", ChangeType: "CHECK",
				Description: "d", Statement: "s"})
			return domain.FixResult{Before: "never", After: "clean", Verified: true, ChangeID: cid}, nil
		}
		res, err := s.startRun(actx, serverID, "fake-long", g, apply, longFix{timeout: time.Minute, completed: neverCompleted})
		if err != nil {
			t.Fatal(err)
		}
		waitRun(t, s, serverID, res.RunID)
		end := waitEnded(t, s, serverID, act.row.ActionID)
		if end.Outcome != domain.OutcomeVerified || end.ValueAfter != "clean" || end.ValueBefore != "never" || end.ChangeIDs == "" {
			t.Fatalf("ended row %+v", end)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		s, serverID, g := runService(t)
		actx, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
		if err != nil {
			t.Fatal(err)
		}
		g.action = act
		apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
			<-ctx.Done()
			return domain.FixResult{}, newError(KindUnreachable, "stopped: %v", ctx.Err())
		}
		res, err := s.startRun(actx, serverID, "fake-long", g, apply, longFix{timeout: time.Minute, completed: neverCompleted})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.StopFixRun(ctx, serverID, "WMS", res.RunID); err != nil {
			t.Fatal(err)
		}
		waitRun(t, s, serverID, res.RunID)
		if end := waitEnded(t, s, serverID, act.row.ActionID); end.Outcome != domain.OutcomeStopped {
			t.Fatalf("ended row %+v", end)
		}
	})
	t.Run("recovered", func(t *testing.T) {
		s, serverID, g := runService(t)
		s.runs.pollEvery, s.runs.quietAfter, s.runs.closeWait = 5*time.Millisecond, 30*time.Millisecond, time.Second
		var mu sync.Mutex
		reads := 0
		s.runs.progress = func(ctx context.Context, g *genericFix) ([]collect.Progress, error) {
			mu.Lock()
			defer mu.Unlock()
			reads++
			if reads <= 2 {
				return []collect.Progress{{SessionID: 71, Command: "DBCC TABLE CHECK", Database: "WMS", Mine: true}}, nil
			}
			return nil, nil
		}
		actx, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
		if err != nil {
			t.Fatal(err)
		}
		g.action = act
		apply := func(ctx context.Context, s *Service, id int64, g *genericFix) (domain.FixResult, error) {
			<-ctx.Done()
			return domain.FixResult{}, ctx.Err()
		}
		completed := func(ctx context.Context, s *Service, g *genericFix, startedAt time.Time) (recovered, bool, error) {
			return recovered{after: "clean", verified: true, change: domain.Change{Object: "CHECKDB", ChangeType: "CHECK", Description: "d", Statement: "s"}}, true, nil
		}
		res, err := s.startRun(actx, serverID, domain.FixCheckdbNow, g, apply, longFix{timeout: time.Minute, completed: completed})
		if err != nil {
			t.Fatal(err)
		}
		waitRun(t, s, serverID, res.RunID)
		end := waitEnded(t, s, serverID, act.row.ActionID)
		if end.Outcome != domain.OutcomeVerified || end.ChangeIDs == "" {
			t.Fatalf("ended row %+v", end)
		}
	})
}
