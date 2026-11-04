package api

import (
	"fmt"
	"net/http"
	"testing"

	"sqlmonitor/internal/domain"
)

// The table picker route against a real store; nothing here dials a SQL Server.
func TestTablesRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server string, db string) string {
		return fmt.Sprintf("/api/servers/%s/databases/%s/tables", server, db)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{path("abc", "Shop"), http.StatusBadRequest},          // bad server id
		{path("0", "Shop"), http.StatusBadRequest},            // not positive
		{path(fmt.Sprint(id), "%20"), http.StatusBadRequest},  // empty database name
		{path("99", "Shop"), http.StatusNotFound},             // unknown server
		{path(fmt.Sprint(id), "Shop"), http.StatusBadGateway}, // no stored password: cannot connect
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := postPath(h, path(fmt.Sprint(id), "Shop")); rec.Code == http.StatusOK {
		t.Errorf("POST answered %d", rec.Code)
	}
}
