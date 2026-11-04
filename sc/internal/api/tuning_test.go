package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The tuning report route and the record-a-change route against a real store; nothing here dials
// a SQL Server.
func TestTuningRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server any, db, query string) string {
		return fmt.Sprintf("/api/servers/%v/databases/%s/tuning%s", server, db, query)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{path("abc", "Shop", ""), http.StatusBadRequest},
		{path(0, "Shop", ""), http.StatusBadRequest},
		{path(id, "%20", ""), http.StatusBadRequest},
		{path(id, "Shop", "?days=0"), http.StatusBadRequest},
		{path(id, "Shop", "?days=31"), http.StatusBadRequest},
		{path(id, "Shop", "?days=week"), http.StatusBadRequest},
		{path(id, "Shop", "?hours=8-17"), http.StatusBadRequest},
		{path(id, "Shop", "?hours=17-08"), http.StatusBadRequest},
		{path(id, "Shop", "?hours=08-25"), http.StatusBadRequest},
		{path(id, "Shop", "?change=latest"), http.StatusBadRequest},
		{path(id, "Shop", "?change=0"), http.StatusBadRequest},
		{path(99, "Shop", ""), http.StatusNotFound},                                                 // unknown server
		{path(id, "Shop", ""), http.StatusBadGateway},                                               // no stored password: cannot connect
		{path(id, "Shop", "?days=30&change=none&hours=07-16"), http.StatusBadGateway},               // valid options
		{path(id, "Shop", "?days=1&change=12&hours=00-24"), http.StatusBadGateway},                  // valid options
		{"/api/servers/1/databases/" + strings.Repeat("x", 129) + "/tuning", http.StatusBadRequest}, // name too long
	} {
		if rec := getPath(h, c.path); rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("GET %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := postPath(h, path(id, "Shop", "")); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST tuning answered %d", rec.Code)
	}
}

func TestRecordChangeRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	post := func(server any, db, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/servers/%v/databases/%s/changes", server, db), strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec
	}
	good := `{"object":"dbo.USP_PICKING","changeType":"PROCEDURE","description":"Added OPTION (RECOMPILE)"}`
	for _, c := range []struct {
		server any
		db     string
		body   string
		code   int
	}{
		{"abc", "Shop", good, http.StatusBadRequest},
		{id, "%20", good, http.StatusBadRequest},
		{id, "Shop", ``, http.StatusBadRequest},
		{id, "Shop", `{"object":"x","changeType":"PROCEDURE","description":"d","extra":1}`, http.StatusBadRequest}, // unknown field
		{id, "Shop", `{"object":"","changeType":"PROCEDURE","description":"d"}`, http.StatusBadRequest},
		{id, "Shop", `{"object":"x","changeType":"TABLE","description":"d"}`, http.StatusBadRequest},
		{id, "Shop", `{"object":"x","changeType":"INDEX","description":""}`, http.StatusBadRequest},
		{id, "Shop", `{"object":"x","changeType":"INDEX","description":"d","changedAt":"yesterday"}`, http.StatusBadRequest},
		{id, "Shop", `{"object":"x","changeType":"INDEX","description":"d","changedAt":"2099-01-01T10:00:00Z"}`, http.StatusBadRequest}, // future
		{99, "Shop", good, http.StatusNotFound},
		{id, "Shop", `{"object":"x","changeType":"INDEX","description":"d","changedAt":"2026-11-03T14:20"}`, http.StatusBadGateway}, // server time: needs the server's clock
	} {
		if rec := post(c.server, c.db, c.body); rec.Code != c.code {
			t.Errorf("POST %v/%s %s: %d %s, want %d", c.server, c.db, c.body, rec.Code, rec.Body.String(), c.code)
		}
	}
	rec := post(id, "Shop", good)
	if rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	var c domain.ChangeEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil || c.ID == 0 || c.Result != domain.ChangeRecordedByHand || c.Database != "Shop" {
		t.Errorf("created %+v %v", c, err)
	}
	list := getPath(h, fmt.Sprintf("/api/servers/%d/databases/Shop/changes", id))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"result":"recorded by hand"`) {
		t.Errorf("changes: %d %s", list.Code, list.Body.String())
	}
}
