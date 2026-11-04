package api

import (
	"fmt"
	"net/http"
	"testing"

	"sqlmonitor/internal/domain"
)

// The blocking route against a real store; nothing here dials a SQL Server.
func TestBlockingRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	for _, c := range []struct {
		path string
		code int
	}{
		{fmt.Sprintf("/api/servers/%d/databases/WMS/blocking", 99), http.StatusNotFound},    // unknown server
		{fmt.Sprintf("/api/servers/%d/databases/%%20/blocking", id), http.StatusBadRequest}, // empty database name
		{"/api/servers/abc/databases/WMS/blocking", http.StatusBadRequest},
		{fmt.Sprintf("/api/servers/%d/databases/WMS/blocking", id), http.StatusBadGateway}, // no stored password
	} {
		if rec := getPath(h, c.path); rec.Code != c.code {
			t.Errorf("GET %s: %d, want %d (%s)", c.path, rec.Code, c.code, rec.Body)
		}
	}
	if rec := postPath(h, fmt.Sprintf("/api/servers/%d/databases/WMS/blocking", id)); rec.Code == http.StatusOK {
		t.Errorf("POST answered %d", rec.Code)
	}
}
