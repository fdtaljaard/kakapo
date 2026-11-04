package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// The run route against a real store; nothing here dials a SQL Server.
func TestRunRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/0123456789abcdef", id), http.StatusNotFound}, // unknown run
		{fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/0123456789abcdef", 99), http.StatusNotFound}, // unknown server
		{fmt.Sprintf("/api/servers/%d/databases/%%20/fixes/runs/x", id), http.StatusBadRequest},             // empty database name
		{"/api/servers/abc/databases/WMS/fixes/runs/x", http.StatusBadRequest},
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	// POST is the fix routes' method: a run is not applied again through this path.
	if rec := postPath(h, fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/x", id)); rec.Code == http.StatusOK {
		t.Errorf("POST answered %d", rec.Code)
	}
}

// A run answers the contract's fields: pending without result or error, then the result, or the
// error with the status code the apply would have answered.
func TestRunJSON(t *testing.T) {
	started := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	decode := func(run service.FixRun) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		writeJSON(rec, http.StatusOK, runJSON(run))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v", rec.Body, err)
		}
		return m
	}
	pending := decode(service.FixRun{RunID: "ab12", FixID: "checkdb-now", Database: "WMS", StartedAt: started, Pending: true})
	for _, k := range []string{"runId", "fixId", "database", "startedAt", "pending"} {
		if _, ok := pending[k]; !ok {
			t.Errorf("pending run lacks %q: %v", k, pending)
		}
	}
	if pending["pending"] != true || pending["result"] != nil || pending["error"] != nil || pending["endedAt"] != nil {
		t.Errorf("pending run %v", pending)
	}

	ended := started.Add(time.Hour)
	done := decode(service.FixRun{RunID: "ab12", FixID: "checkdb-now", Database: "WMS", StartedAt: started, EndedAt: &ended,
		Result: &domain.FixResult{After: domain.CheckdbOutcomeClean, Verified: true, ChangeID: 9, AppliedAt: started}})
	result, _ := done["result"].(map[string]any)
	if done["pending"] != false || result == nil || result["after"] != domain.CheckdbOutcomeClean || done["error"] != nil || done["endedAt"] == nil {
		t.Errorf("ended run %v", done)
	}
	if _, ok := result["runId"]; ok {
		t.Errorf("the run's result carries the pending fields: %v", result)
	}

	for _, c := range []struct {
		err  error
		code float64
	}{
		{&service.Error{Kind: service.KindUnreachable, Msg: "DBCC CHECKDB on WMS did not complete: permission denied"}, http.StatusBadGateway},
		{&service.Error{Kind: service.KindConflict, Msg: "kakapo does not create this index"}, http.StatusConflict},
		{errors.New("the case file is locked"), http.StatusInternalServerError},
	} {
		failed := decode(service.FixRun{RunID: "ab12", FixID: "create-index", Database: "WMS", StartedAt: started, EndedAt: &ended, Err: c.err})
		e, _ := failed["error"].(map[string]any)
		if e == nil || e["status"] != c.code || e["message"] != c.err.Error() || failed["result"] != nil {
			t.Errorf("%v: %v", c.err, failed)
		}
	}
}

// Presence: a long fix running in the background counts as a window (docs/phase4f-contract.md).
func TestPresenceBusyKeepsTheAppUp(t *testing.T) {
	const grace = 20 * time.Millisecond
	p := &Presence{}
	p.add()
	p.Busy()
	p.remove()
	time.Sleep(2 * grace)
	if p.Gone(grace) {
		t.Fatal("the app stops while a run is pending")
	}
	p.Idle()
	if p.Gone(grace) {
		t.Error("the app stops at once when the run ends: the grace starts then")
	}
	time.Sleep(2 * grace)
	if !p.Gone(grace) {
		t.Error("the app stays up after the run and the grace")
	}

	// A window still open when the run ends keeps the app up as before.
	q := &Presence{}
	q.add()
	q.Busy()
	q.Idle()
	time.Sleep(2 * grace)
	if q.Gone(grace) {
		t.Error("the app stops with a window open")
	}
	q.remove()
	time.Sleep(2 * grace)
	if !q.Gone(grace) {
		t.Error("the app stays up after the last window")
	}
}
