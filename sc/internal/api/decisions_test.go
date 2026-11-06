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

// The decision record routes against a real store (docs/phase5m-contract.md, section 6); nothing here
// dials a SQL Server.
func TestDecisionRoutes(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server any, db string) string {
		return fmt.Sprintf("/api/servers/%v/databases/%s/decisions", server, db)
	}
	post := func(server any, db, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path(server, db), strings.NewReader(body)))
		return rec
	}
	if rec := getPath(h, path(id, "Shop")); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("none yet: %d %s", rec.Code, rec.Body.String())
	}
	keep := `{"choice":"keep","verdict":"worth-considering","reason":"waiting for the vendor"}`
	for _, c := range []struct {
		server any
		db     string
		body   string
		code   int
	}{
		{"abc", "Shop", keep, http.StatusBadRequest},
		{id, "%20", keep, http.StatusBadRequest},
		{id, "Shop", ``, http.StatusBadRequest},
		{id, "Shop", `{"choice":"keep","verdict":"worth-considering"}`, http.StatusBadRequest},                // keep without a reason
		{id, "Shop", `{"choice":"keep","verdict":"worth-considering","reason":"  "}`, http.StatusBadRequest},  // a blank reason
		{id, "Shop", `{"choice":"change","verdict":"worth-considering","reason":"x"}`, http.StatusBadRequest}, // change is the fix's Apply
		{id, "Shop", `{"choice":"later","verdict":"great"}`, http.StatusBadRequest},                           // unknown verdict
		{id, "Shop", `{"choice":"later","verdict":"on","reviewOn":"soon"}`, http.StatusBadRequest},            // not a date
		{id, "Shop", `{"choice":"later","verdict":"on","extra":1}`, http.StatusBadRequest},                    // unknown field
		{99, "Shop", keep, http.StatusNotFound},
	} {
		if rec := post(c.server, c.db, c.body); rec.Code != c.code {
			t.Errorf("POST %v/%s %s: %d %s, want %d", c.server, c.db, c.body, rec.Code, rec.Body.String(), c.code)
		}
	}
	rec := post(id, "Shop", keep)
	if rec.Code != http.StatusCreated {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body.String())
	}
	var r domain.DecisionRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || r.ID == 0 || r.Choice != domain.ChoiceKeep || r.ReviewOn == "" || r.DecidedBy == "" ||
		!strings.HasPrefix(r.Line, "Kept off on ") || !strings.Contains(r.Line, ": waiting for the vendor; review by "+r.ReviewOn) {
		t.Errorf("kept %+v %v", r, err)
	}
	if rec := post(id, "Shop", `{"choice":"later","verdict":"not-enough-evidence","reviewOn":"2020-01-01"}`); rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"reviewDue":true`) {
		t.Errorf("later, review past: %d %s", rec.Code, rec.Body.String())
	}
	list := getPath(h, path(id, "Shop"))
	var rs []domain.DecisionRecord
	if err := json.Unmarshal(list.Body.Bytes(), &rs); err != nil || len(rs) != 2 || rs[0].Choice != domain.ChoiceLater || rs[1].Choice != domain.ChoiceKeep {
		t.Errorf("list %d %s", list.Code, list.Body.String())
	}
	if rec := getPath(h, path(99, "Shop")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown server: %d", rec.Code)
	}
}
