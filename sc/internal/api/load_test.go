package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The load route's codes and its days and refresh validation against a real store; nothing here dials
// a SQL Server.
func TestLoadRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server any, db, query string) string {
		return fmt.Sprintf("/api/servers/%v/databases/%s/load%s", server, db, query)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{path("abc", "Shop", ""), http.StatusBadRequest},
		{path(id, "%20", ""), http.StatusBadRequest},
		{path(id, "Shop", "?days=1"), http.StatusBadRequest},
		{path(id, "Shop", "?days=8"), http.StatusBadRequest},
		{path(id, "Shop", "?days=31"), http.StatusBadRequest},
		{path(id, "Shop", "?days=week"), http.StatusBadRequest},
		{path(id, "Shop", "?refresh=yes"), http.StatusBadRequest},
		{path(id, "Shop", "?refresh=2"), http.StatusBadRequest},
		{path(99, "Shop", ""), http.StatusNotFound},                                               // unknown server
		{path(id, "Shop", ""), http.StatusBadGateway},                                             // no stored password: cannot connect
		{path(id, "Shop", "?days=14&refresh=1"), http.StatusBadGateway},                           // valid options
		{path(id, "Shop", "?days=30&refresh=0"), http.StatusBadGateway},                           // valid options
		{"/api/servers/1/databases/" + strings.Repeat("x", 129) + "/load", http.StatusBadRequest}, // name too long
	} {
		if rec := getPath(h, c.path); rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("GET %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
}
