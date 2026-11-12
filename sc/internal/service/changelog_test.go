package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"sqlmonitor/internal/domain"
)

// fakeServerLog stands in for master.dbo.kakapo_ChangeLog on a server (docs/phase5w-contract.md, B5-B6).
type fakeServerLog struct {
	mu        sync.Mutex
	installed bool
	rows      map[string]domain.ActionLogRow
	failWrite error
	writes    int
}

func (f *fakeServerLog) truncate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = map[string]domain.ActionLogRow{}
}

func (f *fakeServerLog) has(id, phase string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.rows[domain.ChangeLogKey(id, phase)]
	return ok
}

// useFakeServerLog swaps the server reads and the write for f until the test ends.
func useFakeServerLog(t *testing.T, f *fakeServerLog) {
	t.Helper()
	if f.rows == nil {
		f.rows = map[string]domain.ActionLogRow{}
	}
	w, k, i := changeLogWriter, changeLogKeysReader, changeLogInstalledReader
	t.Cleanup(func() { changeLogWriter, changeLogKeysReader, changeLogInstalledReader = w, k, i })
	changeLogWriter = func(_ context.Context, _ *sql.DB, r domain.ActionLogRow) (bool, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.installed {
			return false, false, nil
		}
		if f.failWrite != nil {
			return false, false, f.failWrite
		}
		key := domain.ChangeLogKey(r.ActionID, r.Phase)
		if _, ok := f.rows[key]; ok {
			return true, false, nil
		}
		f.rows[key] = r
		f.writes++
		return true, true, nil
	}
	changeLogKeysReader = func(context.Context, *sql.DB) (map[string]bool, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.installed {
			return nil, false, nil
		}
		out := map[string]bool{}
		for k := range f.rows {
			out[k] = true
		}
		return out, true, nil
	}
	changeLogInstalledReader = func(context.Context, *sql.DB) (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.installed, nil
	}
}

// While the log is installed, the started row goes to the server too; when that write fails the action is
// refused, with the way out, and the refused attempt is on record locally (started, then a failed ended).
func TestServerStartedFailureRefusesTheAction(t *testing.T) {
	s, serverID, g := runService(t)
	f := &fakeServerLog{installed: true, failWrite: errors.New("INSERT permission was denied")}
	useFakeServerLog(t, f)
	ctx := context.Background()
	_, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
	var se *Error
	if act != nil || !errors.As(err, &se) || se.Kind != KindConflict ||
		err.Error() != "kakapo could not write its log on the server: INSERT permission was denied. Fix the permission, or remove kakapo's log from this server" {
		t.Fatalf("act %v err %v", act, err)
	}
	rows, err := s.store.ListActionLog(ctx, serverID)
	if err != nil || len(rows) != 2 || rows[0].Phase != domain.ActionStarted || rows[1].Phase != domain.ActionEnded ||
		rows[1].Outcome != domain.OutcomeFailed || !strings.Contains(rows[1].Result, "INSERT permission was denied") ||
		rows[0].ActionID != rows[1].ActionID {
		t.Fatalf("local log %+v %v", rows, err)
	}

	// Not installed: nothing is written there, and nothing is refused.
	f.installed, f.failWrite = false, nil
	_, act, err = s.startAction(ctx, nil, auditInfo(serverID, g))
	if err != nil || act == nil || f.writes != 0 {
		t.Fatalf("without the log: %v %v %d", act, err, f.writes)
	}
}

// Installed: the started and ended rows reach the server; an ended row that could not be written (the
// connection is gone) is put there by the next read's catch-up.
func TestServerRowsAndTheCatchUp(t *testing.T) {
	s, serverID, g := runService(t)
	f := &fakeServerLog{installed: true}
	useFakeServerLog(t, f)
	ctx := context.Background()
	actx, act, err := s.startAction(ctx, nil, auditInfo(serverID, g))
	if err != nil {
		t.Fatal(err)
	}
	if !f.has(act.row.ActionID, domain.ActionStarted) {
		t.Fatal("the started row is not on the server")
	}
	// endAction with no connection: its server write cannot happen (the fresh connection fails here).
	s.endAction(actx, nil, act, domain.FixResult{Before: "5", After: "50", Verified: true}, nil, false)
	waitEnded(t, s, serverID, act.row.ActionID)
	if f.has(act.row.ActionID, domain.ActionEnded) {
		t.Fatal("the ended row reached the server without a connection")
	}
	held, local, installed, err := s.catchUpChangeLog(ctx, nil, serverID)
	if err != nil || !installed || held != 2 || local != 2 || !f.has(act.row.ActionID, domain.ActionEnded) {
		t.Fatalf("catch-up: held %d local %d installed %v err %v", held, local, installed, err)
	}
	if st := s.changeLogSync(ctx, serverID); st.Expected != 2 || st.CheckedAt == nil || len(st.Losses) != 0 {
		t.Fatalf("sync %+v", st)
	}

	// A TRUNCATE: the next read puts the rows back and records the loss, red on the screen.
	f.truncate()
	held, _, _, err = s.catchUpChangeLog(ctx, nil, serverID)
	if err != nil || held != 2 || !f.has(act.row.ActionID, domain.ActionStarted) {
		t.Fatalf("after the TRUNCATE: held %d err %v", held, err)
	}
	st := s.changeLogSync(ctx, serverID)
	if len(st.Losses) != 1 || st.Losses[0].N != 2 || st.Expected != 2 {
		t.Fatalf("losses %+v", st)
	}
	rows := domain.BuildAuditRows(domain.AuditInputs{Losses: st.Losses})
	if len(rows) != 1 || rows[0].Flag != domain.FlagRed || rows[0].Rule != "log-removed" ||
		!strings.HasPrefix(rows[0].Headline, "kakapo's log on this server lost 2 rows between ") {
		t.Fatalf("loss row %+v", rows)
	}
	// A repeat finds nothing missing and no new loss.
	if _, _, _, err := s.catchUpChangeLog(ctx, nil, serverID); err != nil || len(s.changeLogSync(ctx, serverID).Losses) != 1 {
		t.Fatalf("repeat: %v %+v", err, s.changeLogSync(ctx, serverID))
	}

	// The table gone: kakapo forgets what it knew of it (the baseline's difference says it went).
	f.installed = false
	if _, _, installed, err := s.catchUpChangeLog(ctx, nil, serverID); installed || err != nil {
		t.Fatalf("absent: %v %v", installed, err)
	}
	if st := s.changeLogSync(ctx, serverID); st.Expected != 0 || len(st.Losses) != 1 {
		t.Fatalf("after removal %+v", st)
	}
}

// B7: every preview warns while the log is not on the server; not the log's own fixes; nothing when the
// state cannot be read.
func TestPreviewWarnsWithoutTheServerLog(t *testing.T) {
	f := &fakeServerLog{}
	useFakeServerLog(t, f)
	ctx := context.Background()
	p := domain.FixPreview{Warnings: []string{"x"}}
	warnNoChangeLog(ctx, nil, "cost-threshold", &p)
	_, host := whoRuns()
	if len(p.Warnings) != 2 || p.Warnings[1] != domain.ChangeLogMissingWarning(host) ||
		!strings.HasPrefix(p.Warnings[1], "kakapo's log is not on this server yet, so this change is recorded only in kakapo's own file on ") {
		t.Fatalf("warnings %q", p.Warnings)
	}
	for _, id := range []string{domain.FixInstallChangeLog, domain.FixRemoveChangeLog} {
		p := domain.FixPreview{}
		warnNoChangeLog(ctx, nil, id, &p)
		if len(p.Warnings) != 0 {
			t.Errorf("%s warns %q", id, p.Warnings)
		}
	}
	f.installed = true
	p = domain.FixPreview{}
	warnNoChangeLog(ctx, nil, "cost-threshold", &p)
	if len(p.Warnings) != 0 {
		t.Fatalf("installed, warnings %q", p.Warnings)
	}
	changeLogInstalledReader = func(context.Context, *sql.DB) (bool, error) { return false, errors.New("timeout") }
	warnNoChangeLog(ctx, nil, "cost-threshold", &p)
	if len(p.Warnings) != 0 {
		t.Fatalf("unread, warnings %q", p.Warnings)
	}
}

// CHG02 reads whether kakapo acted on the server and whether its log is there.
func TestCHG02Facts(t *testing.T) {
	s, serverID, g := runService(t)
	f := &fakeServerLog{}
	useFakeServerLog(t, f)
	ctx := context.Background()
	var u domain.UpkeepFacts
	s.chg02Facts(ctx, nil, serverID, &u)
	if u.KakapoActed || u.ChangeLog != domain.ChangeLogAbsent {
		t.Fatalf("before any action %+v", u)
	}
	if _, _, err := s.startAction(ctx, nil, auditInfo(serverID, g)); err != nil {
		t.Fatal(err)
	}
	s.chg02Facts(ctx, nil, serverID, &u)
	if !u.KakapoActed || u.ChangeLog != domain.ChangeLogAbsent {
		t.Fatalf("after an action %+v", u)
	}
	f.installed = true
	u = domain.UpkeepFacts{}
	s.chg02Facts(ctx, nil, serverID, &u)
	if u.ChangeLog != domain.ChangeLogPresent {
		t.Fatalf("installed %+v", u)
	}
}
