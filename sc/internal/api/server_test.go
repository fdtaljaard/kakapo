package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
	"sqlmonitor/internal/store"
)

// GET and PUT /api/servers/{id}, and the test route's id, against a real store. No path here
// dials a SQL Server: each fails on input, on the id, or on the missing stored password.
func TestGetAndEditServerRoutes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := st.InsertServer(ctx, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"},
		domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(service.New(st, noCreds{}, "test"), fstest.MapFS{"index.html": {Data: []byte("app")}})
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	path := fmt.Sprintf("/api/servers/%d", srv.ID)

	rec := do(http.MethodGet, path, "")
	var got domain.Server
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || got.ID != srv.ID || got.Name != "local" {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Errorf("GET carries a password field: %s", rec.Body.String())
	}
	if rec := do(http.MethodGet, "/api/servers/99", ""); rec.Code != 404 {
		t.Errorf("GET unknown: %d %s", rec.Code, rec.Body.String())
	}

	valid := `{"name":"renamed","host":"nowhere.invalid","auth":"sql","username":"m","password":"","database":"","encrypt":false,"trustCert":false}`
	for _, body := range []string{``, `not json`, `{"host":"x","auth":"windows","other":1}`,
		`{"name":"x","host":"","auth":"windows"}`, `{"host":"x","auth":"kerberos"}`, `{"host":"x","auth":"sql","username":""}`} {
		if rec := do(http.MethodPut, path, body); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("PUT %q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := do(http.MethodPut, "/api/servers/99", valid); rec.Code != 404 {
		t.Errorf("PUT unknown: %d %s", rec.Code, rec.Body.String())
	}
	// Empty password means "keep the stored one"; there is none, so 502 before any dial.
	if rec := do(http.MethodPut, path, valid); rec.Code != 502 || !strings.Contains(rec.Body.String(), "no stored password") {
		t.Errorf("PUT without a stored password: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := st.GetServer(ctx, srv.ID); got.Name != "local" {
		t.Errorf("row changed after a failed PUT: %+v", got)
	}

	// The test route accepts id and uses the stored password, here missing.
	body := fmt.Sprintf(`{"id":%d,"name":"x","host":"nowhere.invalid","auth":"sql","username":"m","password":""}`, srv.ID)
	if rec := do(http.MethodPost, "/api/servers/test", body); rec.Code != 502 || !strings.Contains(rec.Body.String(), "no stored password") {
		t.Errorf("test with id: %d %s", rec.Code, rec.Body.String())
	}
}
