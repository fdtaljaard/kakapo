package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"sqlmonitor/internal/service"
)

// The database name comes URL-encoded; PathValue must hand it back decoded, once.
func TestServerAndDatabaseDecodesName(t *testing.T) {
	mux := http.NewServeMux()
	var gotID int64
	var gotName string
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/query-store/preview", func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotID, gotName, err = serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
		}
	})
	cases := map[string]string{
		"/api/servers/3/databases/WarehouseLive/query-store/preview":    "WarehouseLive",
		"/api/servers/3/databases/Warehouse%20Live/query-store/preview": "Warehouse Live",
		"/api/servers/3/databases/a%2Fb%5D%25c/query-store/preview":     "a/b]%c",
		"/api/servers/3/databases/%5BWMS%5D%20Test/query-store/preview": "[WMS] Test",
		"/api/servers/3/databases/caf%C3%A9%2520x/query-store/preview":  "café%20x",
	}
	for path, want := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusOK || gotID != 3 || gotName != want {
			t.Errorf("%s: code %d id %d name %q, want %q", path, rec.Code, gotID, gotName, want)
		}
	}
}

func TestRoutesRejectBadIDAndKeepSPA(t *testing.T) {
	ui := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}
	h := Handler(nil, ui) // the 400 paths never reach the service

	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/servers/abc/databases"},
		{http.MethodPost, "/api/servers/0/databases/x/query-store/preview"},
		{http.MethodPost, "/api/servers/x/databases/x/query-store/apply"},
		{http.MethodPost, "/api/servers/1/databases/%20/query-store/apply"},
		{http.MethodGet, "/api/servers/abc/databases/x/overview"},
		{http.MethodGet, "/api/servers/-1/databases/x/overview"},
		{http.MethodGet, "/api/servers/1/databases/%20/overview"},
		{http.MethodGet, "/api/servers/1/databases/" + strings.Repeat("x", 129) + "/overview"},
		{http.MethodGet, "/api/servers/0/databases/x/changes"},
		{http.MethodGet, "/api/servers/1/databases/%20/changes"},
		{http.MethodGet, "/api/servers/1/databases/x/changes?limit=abc"},
		{http.MethodGet, "/api/servers/1/databases/x/changes?limit=0"},
		{http.MethodPut, "/api/servers/abc/database"},
		{http.MethodGet, "/api/servers/abc"},
		{http.MethodPut, "/api/servers/0"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}

	// The server-level overview and changes of the skeleton are gone, and an unknown API path is a
	// JSON 404, not the UI's index.html.
	for _, path := range []string{"/api/servers/1/overview", "/api/servers/1/changes", "/api/nothing"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/servers/1", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("SPA fallback: %d %s", rec.Code, rec.Body.String())
	}
}

func TestStatusFor(t *testing.T) {
	for kind, want := range map[service.Kind]int{
		service.KindNotFound: 404, service.KindConflict: 409, service.KindUnreachable: 502, service.KindInvalid: 400,
	} {
		if got := statusFor(&service.Error{Kind: kind, Msg: "x"}); got != want {
			t.Errorf("kind %d: %d, want %d", kind, got, want)
		}
	}
	if got := statusFor(errors.New("disk full")); got != 500 {
		t.Errorf("plain error: %d", got)
	}
}
