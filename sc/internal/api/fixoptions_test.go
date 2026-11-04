package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The fix routes take an optional {"options": {...}} body (docs/phase3g-contract.md); a malformed
// one is 400 before anything is dialled, a good one reaches the service like no body does.
func TestFixRoutesOptionsBody(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, action := range []string{"preview", "apply"} {
		path := fmt.Sprintf("/api/servers/%d/databases/GraniteLive/fixes/backup-schedule/%s", id, action)
		for _, c := range []struct {
			body string
			code int
		}{
			{`{"options": {"folder": "X:\\nowhere", "runNow": "false"}}`, 502}, // reaches the dial
			{``, 502},
			{`   `, 502},
			{`{}`, 502},
			{`{"options": {"folder": 5}}`, 400},
			{`{"folder": "X:\\nowhere"}`, 400}, // options go under "options"
			{`not json`, 400},
		} {
			rec := post(path, c.body)
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("POST %s %q: %d %s, want %d", path, c.body, rec.Code, rec.Body.String(), c.code)
			}
		}
		// The remove fix and an unknown server: 404 as for every fix.
		if rec := post(fmt.Sprintf("/api/servers/99/databases/GraniteLive/fixes/remove-backup-schedule/%s", action), ``); rec.Code != 404 {
			t.Errorf("unknown server: %d %s", rec.Code, rec.Body.String())
		}
	}
}
