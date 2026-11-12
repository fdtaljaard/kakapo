package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The Memory screen's routes (docs/phase5v-contract.md) against a real store; nothing here dials a SQL
// Server except the GET of a registered server's live panels, which cannot reach nowhere.invalid (502).
func TestMemoryRoutes(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/servers/abc/memory", http.StatusBadRequest},
		{fmt.Sprintf("/api/servers/%d/memory?snapshot=x", id), http.StatusBadRequest},
		{fmt.Sprintf("/api/servers/%d/memory?snapshot=0", id), http.StatusBadRequest},
		{"/api/servers/99/memory", http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/memory?snapshot=7", id), http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/memory/databases/%%20", id), http.StatusBadRequest},
		{"/api/servers/99/memory/databases/WMS", http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/memory/databases/WMS?snapshot=7", id), http.StatusNotFound},
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	// A database never read: no detail, an empty list.
	rec := getPath(h, fmt.Sprintf("/api/servers/%d/memory/databases/WMS", id))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"detail":null`) || !strings.Contains(rec.Body.String(), `"snapshots":[]`) {
		t.Errorf("never read: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/servers/abc/memory/snapshot", http.StatusBadRequest},
		{"/api/servers/99/memory/snapshot", http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/memory/databases/%%20/snapshot", id), http.StatusBadRequest},
		{"/api/servers/99/memory/databases/WMS/snapshot", http.StatusNotFound},
	} {
		if rec := postPath(h, c.path); rec.Code != c.code {
			t.Errorf("POST %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
}
