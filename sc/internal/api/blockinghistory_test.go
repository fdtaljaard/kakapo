package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The blocking history route against a real store; nothing here dials a SQL Server.
func TestBlockingHistoryRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/performance/blocking-history", server, db)
	}
	post := func(p, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, strings.NewReader(body)))
		return rec
	}
	for _, c := range []struct {
		path, body string
		code       int
	}{
		{path(99, "WarehouseLive"), "", 404}, // unknown server
		{path(id, "%20"), "", 400},           // empty database name
		{"/api/servers/abc/databases/x/cards/performance/blocking-history", "", 400},
		{path(id, "WarehouseLive"), `{"days": 8}`, 400}, // 1 to 7
		{path(id, "WarehouseLive"), `{"days": -1}`, 400},
		{path(id, "WarehouseLive"), `{"days": "seven"}`, 400},
		{path(id, "WarehouseLive"), `{"hours": 7}`, 400},
		{path(id, "WarehouseLive"), "", 502},            // a SQL login with no stored password cannot connect
		{path(id, "WarehouseLive"), `{"days": 3}`, 502}, // the same, with a window
		{path(id, "WarehouseLive"), `{"days": 0}`, 502}, // 0 is the default, 7
		// Only performance has it.
		{fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/cards/storage/blocking-history", id), "", 404},
	} {
		rec := post(c.path, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("POST %s %s: %d %s, want %d", c.path, c.body, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := post(path(id, "WarehouseLive"), `{"days": 8}`); !strings.Contains(rec.Body.String(), "days must be 1 to 7") {
		t.Errorf("days message: %s", rec.Body.String())
	}
	// GET is not the route: it reads as an unknown card.
	if rec := getPath(h, path(id, "WarehouseLive")); rec.Code != 404 && rec.Code != 405 {
		t.Errorf("GET blocking-history: %d %s", rec.Code, rec.Body.String())
	}
}
