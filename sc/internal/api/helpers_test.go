package api

import (
	"fmt"
	"net/http"
	"testing"

	"sqlmonitor/internal/domain"
)

// The installed-helpers route against a real store; nothing here dials a SQL Server.
func TestHelpersRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/servers/abc/helpers", http.StatusBadRequest},                 // bad server id
		{"/api/servers/0/helpers", http.StatusBadRequest},                   // not positive
		{"/api/servers/99/helpers", http.StatusNotFound},                    // unknown server
		{fmt.Sprintf("/api/servers/%d/helpers", id), http.StatusBadGateway}, // no stored password: cannot connect
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := postPath(h, fmt.Sprintf("/api/servers/%d/helpers", id)); rec.Code == http.StatusOK {
		t.Errorf("POST answered %d", rec.Code)
	}
}
