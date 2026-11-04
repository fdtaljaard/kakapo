package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/domain"
)

// The progress route against a real store; nothing here dials a SQL Server.
func TestProgressRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/servers/%d/databases/WMS/progress", 99), http.StatusNotFound},    // unknown server
		{fmt.Sprintf("/api/servers/%d/databases/%%20/progress", id), http.StatusBadRequest}, // empty database name
		{"/api/servers/abc/databases/WMS/progress", http.StatusBadRequest},
		{fmt.Sprintf("/api/servers/%d/databases/WMS/progress", id), http.StatusBadGateway}, // no stored password
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	if rec := postPath(h, fmt.Sprintf("/api/servers/%d/databases/WMS/progress", id)); rec.Code == http.StatusOK {
		t.Errorf("POST answered %d", rec.Code)
	}
}

// Nothing running answers [], and a row carries the contract's field names.
func TestProgressJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, progressList(nil))
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("nothing running: %s, want []", got)
	}
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, progressList([]collect.Progress{{SessionID: 71, Command: "DBCC CHECKDB", Database: "WMS",
		Percent: 40, ElapsedSeconds: 90, RemainingSeconds: 135, StartedAt: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC),
		Program: domain.BlockingAppName, Mine: true}}))
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%s: %v", rec.Body, err)
	}
	for _, k := range []string{"sessionId", "command", "database", "percent", "elapsedSeconds", "remainingSeconds", "startedAt",
		"waitType", "program", "mine"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("row lacks %q: %s", k, rec.Body)
		}
	}
}
