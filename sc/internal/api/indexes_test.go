package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// The Indexes page's routes (docs/phase5t-contract.md) against a real store; nothing here dials a
// SQL Server.
func TestIndexesRoutes(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/servers/%d/databases/%%20/indexes/candidates", id), http.StatusBadRequest}, // empty database name
		{"/api/servers/abc/databases/WMS/indexes/candidates", http.StatusBadRequest},
		{fmt.Sprintf("/api/servers/%d/databases/WMS/indexes/candidates", 99), http.StatusNotFound}, // unknown server
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	// Stop: an unknown run (or server) is 404, a bad id 400, and GET is not how a run is stopped.
	for _, c := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/0123456789abcdef/stop", id), http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/0123456789abcdef/stop", 99), http.StatusNotFound},
		{fmt.Sprintf("/api/servers/%d/databases/%%20/fixes/runs/x/stop", id), http.StatusBadRequest},
		{"/api/servers/abc/databases/WMS/fixes/runs/x/stop", http.StatusBadRequest},
	} {
		if rec := postPath(h, c.path); rec.Code != c.code {
			t.Errorf("POST %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	if rec := getPath(h, fmt.Sprintf("/api/servers/%d/databases/WMS/fixes/runs/x/stop", id)); rec.Code == http.StatusOK {
		t.Errorf("GET stop answered %d", rec.Code)
	}
}

// A batch run answers its items and whether a stop was asked for.
func TestRunJSONItems(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, runJSON(service.FixRun{RunID: "ab12", FixID: domain.FixReorganizeIndexes, Database: "WMS", Pending: true, Stopping: true,
		Items: []domain.RunItem{{Label: "dbo.Lines · IX_A", State: domain.ItemRunning, Text: "reorganising 12400 pages"}}}))
	body := rec.Body.String()
	for _, want := range []string{`"items":[{"label":"dbo.Lines · IX_A","state":"running","text":"reorganising 12400 pages"}]`, `"stopping":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, runJSON(service.FixRun{RunID: "ab12", FixID: "checkdb-now", Database: "WMS", Pending: true}))
	if strings.Contains(rec.Body.String(), "items") || strings.Contains(rec.Body.String(), "stopping") {
		t.Errorf("a one-statement run carries batch fields: %s", rec.Body)
	}
}
