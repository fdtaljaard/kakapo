package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The sample routes against a real store; nothing here dials a SQL Server.
func TestSampleRoutes(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/performance/sample", server, db)
	}
	post := func(p, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{path(99, "WarehouseLive"), ``, 404},                 // unknown server
		{path(id, "%20"), ``, 400},                           // empty database name
		{path(id, "WarehouseLive"), `{"seconds": 5}`, 400},   // too short
		{path(id, "WarehouseLive"), `{"seconds": 121}`, 400}, // too long
		{path(id, "WarehouseLive"), `{"secs": 30}`, 400},     // unknown field
		{path(id, "WarehouseLive"), `{"seconds": 30}`, 502},  // no stored password: cannot connect
		{path(id, "WarehouseLive"), ``, 502},                 // default 30 seconds
	} {
		if rec := post(c.path, c.body); rec.Code != c.code {
			t.Errorf("POST %s %s: %d %s, want %d", c.path, c.body, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := getPath(h, path(id, "WarehouseLive")); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Errorf("GET last: %d %s", rec.Code, rec.Body.String())
	}
	if rec := getPath(h, path(99, "WarehouseLive")); rec.Code != 404 {
		t.Errorf("GET unknown server: %d", rec.Code)
	}
	// The card route still answers other cards; the sample route is not taken for a card id.
	if rec := getPath(h, fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/cards/nope", id)); rec.Code == 200 {
		t.Errorf("unknown card: %d", rec.Code)
	}
}
