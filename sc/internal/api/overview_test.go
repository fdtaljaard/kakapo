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
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
	"sqlmonitor/internal/store"
)

type noCreds struct{}

func (noCreds) Set(int64, string) error   { return nil }
func (noCreds) Delete(int64) error        { return nil }
func (noCreds) Get(int64) (string, error) { return "", fmt.Errorf("no stored password") }

// The revision 2 routes against a real store; nothing here dials a SQL Server.
func TestDatabaseChangesOverviewAndRememberRoutes(t *testing.T) {
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
	base := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		if _, err := st.InsertChange(ctx, domain.Change{ServerID: srv.ID, Database: "WarehouseLive", ChangedAt: base.Add(time.Duration(i) * time.Minute),
			Object: "Query Store", ChangeType: "SETTING", Description: fmt.Sprint(i), Statement: "s", Result: "On"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertChange(ctx, domain.Change{ServerID: srv.ID, Database: "Warehouse Test", ChangedAt: base.Add(time.Hour),
		Object: "Query Store", ChangeType: "SETTING", Description: "test db", Statement: "s"}); err != nil {
		t.Fatal(err)
	}
	h := Handler(service.New(st, noCreds{}), fstest.MapFS{"index.html": {Data: []byte("app")}})

	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	get := func(path string) *httptest.ResponseRecorder { return do(http.MethodGet, path, "") }
	changesPath := func(db, query string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/changes%s", srv.ID, db, query)
	}

	rec := get(changesPath("WarehouseLive", ""))
	var changes []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &changes); rec.Code != 200 || err != nil {
		t.Fatalf("changes: %d %s", rec.Code, rec.Body.String())
	}
	if len(changes) != 10 || changes[0]["description"] != "11" || changes[9]["description"] != "2" {
		t.Errorf("default limit 10, newest first, this database only: %d rows, first %v", len(changes), changes[0])
	}
	for _, k := range []string{"id", "serverId", "database", "changedAt", "object", "changeType", "description", "result"} {
		if _, ok := changes[0][k]; !ok {
			t.Errorf("change JSON misses %q: %v", k, changes[0])
		}
	}
	if _, ok := changes[0]["statement"]; ok {
		t.Errorf("change JSON carries the statement: %v", changes[0])
	}
	if rec := get(changesPath("WarehouseLive", "?limit=3")); !jsonLen(t, rec, 3) {
		t.Errorf("limit 3: %s", rec.Body.String())
	}
	if rec := get(changesPath("WarehouseLive", "?limit=500")); !jsonLen(t, rec, 12) {
		t.Errorf("limit 500 (capped at 100): %s", rec.Body.String())
	}
	if rec := get(changesPath("Warehouse%20Test", "")); !jsonLen(t, rec, 1) || !strings.Contains(rec.Body.String(), `"test db"`) {
		t.Errorf("URL-encoded database name: %s", rec.Body.String())
	}
	if rec := get(changesPath("Nothing", "")); !jsonLen(t, rec, 0) || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("database without changes: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("/api/servers/99/databases/WarehouseLive/changes"); rec.Code != 404 {
		t.Errorf("unknown server changes: %d %s", rec.Code, rec.Body.String())
	}

	if rec := get("/api/servers/99/databases/WarehouseLive/overview"); rec.Code != 404 {
		t.Errorf("unknown server overview: %d %s", rec.Code, rec.Body.String())
	}
	// A SQL login with no stored password cannot connect: 502, before any dialling.
	if rec := get(fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/overview", srv.ID)); rec.Code != 502 {
		t.Errorf("unreachable overview: %d %s", rec.Code, rec.Body.String())
	}

	// Remember a database on the registration.
	put := func(id int64, body string) *httptest.ResponseRecorder {
		return do(http.MethodPut, fmt.Sprintf("/api/servers/%d/database", id), body)
	}
	rec = put(srv.ID, `{"database": "Warehouse Test"}`)
	var got domain.Server
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || got.ID != srv.ID || got.Database != "Warehouse Test" {
		t.Fatalf("remember: %d %s", rec.Code, rec.Body.String())
	}
	var servers []domain.Server
	if rec := get("/api/servers"); json.Unmarshal(rec.Body.Bytes(), &servers) != nil || len(servers) != 1 || servers[0].Database != "Warehouse Test" {
		t.Errorf("servers after remember: %s", rec.Body.String())
	}
	if rec := put(99, `{"database": "WarehouseLive"}`); rec.Code != 404 {
		t.Errorf("remember on unknown server: %d %s", rec.Code, rec.Body.String())
	}
	for _, body := range []string{``, `{}`, `{"database": ""}`, `{"database": "   "}`,
		`{"database": "` + strings.Repeat("x", 129) + `"}`, `{"database": "x", "other": 1}`, `not json`} {
		if rec := put(srv.ID, body); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("remember %q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := put(srv.ID, `{"database": "`+strings.Repeat("é", 128)+`"}`); rec.Code != 200 {
		t.Errorf("remember a 128-character name: %d %s", rec.Code, rec.Body.String())
	}
}

func jsonLen(t *testing.T, rec *httptest.ResponseRecorder, want int) bool {
	t.Helper()
	var v []any
	return rec.Code == 200 && json.Unmarshal(rec.Body.Bytes(), &v) == nil && len(v) == want
}
