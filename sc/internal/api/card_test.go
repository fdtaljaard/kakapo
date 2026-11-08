package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
	"sqlmonitor/internal/store"
)

func cardsHandler(t *testing.T, in domain.ServerInput) (http.Handler, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := st.InsertServer(ctx, in, domain.ConnectionInfo{})
	if err != nil {
		t.Fatal(err)
	}
	return Handler(service.New(st, noCreds{}), fstest.MapFS{"index.html": {Data: []byte("app")}}), srv.ID
}

func getPath(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postPath(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

// The CHECKDB room route against a real store; nothing here dials a SQL Server.
func TestCheckdbRoomRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/storage/checkdb-room", server, db)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{path(99, "WarehouseLive"), 404}, // unknown server
		{path(id, "%20"), 400},           // empty database name
		{"/api/servers/abc/databases/x/cards/storage/checkdb-room", 400},
		{path(id, "WarehouseLive"), 502}, // a SQL login with no stored password cannot connect
		{fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/cards/backups/checkdb-room", id), 404}, // only storage has it
	} {
		rec := postPath(h, c.path)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("POST %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	// GET is not the route: it reads as an unknown card.
	if rec := getPath(h, path(id, "WarehouseLive")); rec.Code != 404 && rec.Code != 405 {
		t.Errorf("GET checkdb-room: %d %s", rec.Code, rec.Body.String())
	}
}

// The deadlock report route against a real store; nothing here dials a SQL Server.
func TestDeadlocksRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/performance/deadlocks", server, db)
	}
	for _, c := range []struct {
		path string
		code int
	}{
		{path(99, "WarehouseLive"), 404}, // unknown server
		{path(id, "%20"), 400},           // empty database name
		{"/api/servers/abc/databases/x/cards/performance/deadlocks", 400},
		{path(id, "WarehouseLive"), 502}, // a SQL login with no stored password cannot connect
		{fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/cards/storage/deadlocks", id), 404}, // only performance has it
	} {
		rec := postPath(h, c.path)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("POST %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	// GET is not the route: it reads as an unknown card.
	if rec := getPath(h, path(id, "WarehouseLive")); rec.Code != 404 && rec.Code != 405 {
		t.Errorf("GET deadlocks: %d %s", rec.Code, rec.Body.String())
	}
}

// The card detail route against a real store; nothing here dials a SQL Server.
func TestCardDetailRoute(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db, card string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/%s", server, db, card)
	}

	for _, c := range []struct {
		path string
		code int
	}{
		{path(id, "WarehouseLive", "nothing"), 404},   // unknown card id, before any dialling
		{path(id, "WarehouseLive", "Backups"), 404},   // ids are lower case
		{path(99, "WarehouseLive", "backups"), 404},   // unknown server
		{path(id, "WarehouseLive", ""), 404},          // no card: not a route
		{path(id, "WarehouseLive", "backups/x"), 404}, // not a route
		{path(id, "%20", "backups"), 400},             // empty database name
		{"/api/servers/abc/databases/x/cards/backups", 400},
		{path(id, "WarehouseLive", "backups"), 502}, // a SQL login with no stored password cannot connect
		{path(id, "WarehouseLive", "setup"), 502},
		{path(id, "WarehouseLive", "storage"), 502},
		{path(id, "WarehouseLive", "upkeep"), 502},
		{path(id, "WarehouseLive", "performance"), 502},
		{path(id, "WarehouseLive", "design"), 502},
	} {
		rec := getPath(h, c.path)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("GET %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := getPath(h, path(id, "WarehouseLive", "nothing")); !strings.Contains(rec.Body.String(), `there is no card named \"nothing\"`) {
		t.Errorf("unknown card message: %s", rec.Body.String())
	}
}

// TestCardDetailLive reads the card details from a real instance, read-only, when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one.
func TestCardDetailLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	h, id := cardsHandler(t, domain.ServerInput{Name: "live", Host: host, Auth: domain.AuthWindows})
	path := func(db, card string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/cards/%s", id, url.PathEscape(db), card)
	}
	read := func(db, card string) (domain.CardDetail, string) {
		rec := getPath(h, path(db, card))
		var d domain.CardDetail
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
			t.Fatalf("GET %s: %d %s", path(db, card), rec.Code, rec.Body.String())
		}
		return d, rec.Body.String()
	}

	d, body := read(dbName, "setup")
	if d.Card.ID != domain.CardSetup || d.Card.Status == "" || len(d.Checks) != 27 || len(d.Card.Facts) != 4 ||
		!strings.Contains(body, `"fixes":`) || d.Card.Fixable != len(d.Fixes) {
		t.Errorf("setup detail: %s", body)
	}
	d, body = read(dbName, "upkeep")
	if d.Card.ID != domain.CardUpkeep || d.Card.Status == "" || len(d.Checks) != 7 || len(d.Card.Facts) != 4 ||
		!strings.Contains(body, `"fixes":`) || d.Card.Fixable != len(d.Fixes) || strings.Contains(body, "checkdbRoom") {
		t.Errorf("upkeep detail: %s", body)
	}
	for _, x := range d.Findings {
		if x.FixID != "" && x.FixID != domain.FixUpdateStatistics {
			t.Errorf("upkeep finding with another fix: %+v", x)
		}
	}
	t.Logf("upkeep: %s %q %q %+v", d.Card.Status, d.Card.StatusText, d.Card.Verdict, d.Card.Facts)
	d, body = read(dbName, "performance")
	if d.Card.ID != domain.CardPerformance || d.Card.Status == "" || len(d.Checks) != 15 || len(d.Card.Facts) != 4 ||
		!strings.Contains(body, `"deadlocks":null`) || strings.Contains(body, "checkdbRoom") || strings.Contains(body, `"fixes"`) {
		t.Errorf("performance detail: %s", body)
	}
	for _, x := range d.Findings {
		if x.Clock == "" {
			t.Errorf("performance finding without a clock: %+v", x)
		}
	}
	t.Logf("performance: %s %q %q %+v", d.Card.Status, d.Card.StatusText, d.Card.Verdict, d.Card.Facts)

	// DLK01 on request, then the detail carries it.
	rec := postPath(h, path(dbName, "performance/deadlocks"))
	var report domain.DeadlockReport
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &report) != nil || report.Days != 7 || report.Pairs == nil || report.Source == "" {
		t.Fatalf("POST deadlocks: %d %s", rec.Code, rec.Body.String())
	}
	t.Logf("deadlocks: %s", rec.Body.String())
	d, body = read(dbName, "performance")
	if d.Deadlocks == nil || !d.Deadlocks.CapturedAt.Equal(report.CapturedAt) || d.Deadlocks.Total != report.Total {
		t.Errorf("stored deadlocks: %s", body)
	}
	if rec := postPath(h, path("NoSuchDatabase_x9", "performance/deadlocks")); rec.Code != 404 {
		t.Errorf("unknown database: %d %s", rec.Code, rec.Body.String())
	}
	d, body = read(dbName, "storage")
	if d.Card.ID != domain.CardStorage || len(d.Checks) != 19 || len(d.Card.Facts) != 4 || !strings.Contains(body, `"checkdbRoom":`) {
		t.Errorf("storage detail: %s", body)
	}
	t.Logf("storage: %s %q %q %+v", d.Card.Status, d.Card.StatusText, d.Card.Verdict, d.Card.Facts)

	// STO10 on request, then the detail carries it and the check follows its verdict.
	rec = postPath(h, path(dbName, "storage/checkdb-room"))
	var room domain.CheckdbRoom
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &room) != nil || room.EstimateKB <= 0 || room.Finding.CheckID != "STO10" {
		t.Fatalf("POST checkdb-room: %d %s", rec.Code, rec.Body.String())
	}
	t.Logf("checkdb room: %s", rec.Body.String())
	d, body = read(dbName, "storage")
	if d.CheckdbRoom == nil || d.CheckdbRoom.EstimateKB != room.EstimateKB || !d.CheckdbRoom.CapturedAt.Equal(room.CapturedAt) {
		t.Errorf("stored room: %s", body)
	}
	for _, c := range d.Checks {
		if c.CheckID == "STO10" && c.Status == domain.CheckSkipped {
			t.Errorf("STO10 after a run: %+v", c)
		}
	}
	if rec := postPath(h, path("NoSuchDatabase_x9", "storage/checkdb-room")); rec.Code != 404 {
		t.Errorf("unknown database: %d %s", rec.Code, rec.Body.String())
	}
	// The Design card (docs/phase5n-contract.md, docs/phase5o-contract.md): read, its two checks, and the fold
	// as foreignKeys; an IDX14 finding has its create-index fix, an IDX04 finding with Apply a disable-index fix
	// per safe copy.
	d, body = read(dbName, "design")
	if d.Card.ID != domain.CardDesign || d.Card.Status == "" || len(d.Checks) != 2 || d.Checks[0].CheckID != "IDX14" || d.Checks[1].CheckID != "IDX04" ||
		!strings.Contains(body, `"foreignKeys":`) || d.ForeignKeys == nil || strings.Contains(body, "checkdbRoom") {
		t.Errorf("design detail: %s", body)
	}
	if d.Card.Status != domain.StatusGrey && len(d.Card.Facts) != 5 {
		t.Errorf("design facts: %+v", d.Card.Facts)
	}
	applies := 0
	for _, x := range d.Findings {
		switch {
		case x.CheckID == "IDX14" && x.FixID == domain.FixCreateIndex:
			applies++
		case x.CheckID == "IDX04" && x.FixID == domain.FixDisableIndex:
			applies += len(domain.IDX04SafeCopies(x.Evidence))
		case x.CheckID == "IDX04" && x.FixID == "":
		default:
			t.Errorf("design finding: %+v", x)
		}
	}
	if len(d.Fixes) != applies {
		t.Errorf("design fixes %d for %d applies", len(d.Fixes), applies)
	}
	t.Logf("design: %s %q %q %+v, %d keys in the fold (%s)", d.Card.Status, d.Card.StatusText, d.Card.Verdict, d.Card.Facts,
		len(d.ForeignKeys.Keys), d.ForeignKeys.Heading)
	if rec := getPath(h, path(dbName, "storage")); strings.Contains(rec.Body.String(), "foreignKeys") {
		t.Errorf("storage detail carries foreignKeys")
	}

	d, body = read(dbName, "backups")
	if d.Card.ID != domain.CardBackups || len(d.Checks) != 6 || d.Findings == nil {
		t.Errorf("backups detail: %s", body)
	}
	t.Logf("backups: %s %q %q", d.Card.Status, d.Card.StatusText, d.Card.Verdict)
	if rec := getPath(h, path("NoSuchDatabase_x9", "backups")); rec.Code != 404 {
		t.Errorf("unknown database: %d %s", rec.Code, rec.Body.String())
	}
}

// The generic fix routes against a real store; nothing here dials a SQL Server.
func TestFixRoutes(t *testing.T) {
	h, id := cardsHandler(t, domain.ServerInput{Name: "local", Host: "nowhere.invalid", Auth: domain.AuthSQL, Username: "m"})
	path := func(server int64, db, fix, action string) string {
		return fmt.Sprintf("/api/servers/%d/databases/%s/fixes/%s/%s", server, db, fix, action)
	}
	for _, action := range []string{"preview", "apply"} {
		for _, c := range []struct {
			path string
			code int
		}{
			{path(id, "WarehouseLive", "drop-everything", action), 404}, // unknown fix id, before any dialling
			{path(id, "WarehouseLive", "Auto-Close", action), 404},      // ids are lower case
			{path(99, "WarehouseLive", "auto-close", action), 404},      // unknown server
			{path(99, "WarehouseLive", "query-store", action), 404},
			{path(id, "%20", "auto-close", action), 400}, // empty database name
			{fmt.Sprintf("/api/servers/abc/databases/x/fixes/auto-close/%s", action), 400},
			{path(id, "WarehouseLive", "auto-close", action), 502}, // a SQL login with no stored password cannot connect
			{path(id, "WarehouseLive", "max-server-memory", action), 502},
			{path(id, "WarehouseLive", "query-store", action), 502},
			{path(99, "WarehouseLive", "update-statistics", action), 404},
			{path(id, "WarehouseLive", "update-statistics", action), 502},
		} {
			rec := postPath(h, c.path)
			if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("POST %s: %d %s, want %d", c.path, rec.Code, rec.Body.String(), c.code)
			}
		}
	}
	if rec := postPath(h, path(id, "WarehouseLive", "nothing", "preview")); !strings.Contains(rec.Body.String(), `there is no fix named \"nothing\"`) {
		t.Errorf("unknown fix message: %s", rec.Body.String())
	}
	// Not routes: GET, no action, an unknown action.
	for _, p := range []string{path(id, "WarehouseLive", "auto-close", "preview"), path(id, "WarehouseLive", "auto-close", "run")} {
		if rec := getPath(h, p); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("GET %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{fmt.Sprintf("/api/servers/%d/databases/WarehouseLive/fixes/auto-close", id), path(id, "WarehouseLive", "auto-close", "run")} {
		if rec := postPath(h, p); rec.Code != 404 {
			t.Errorf("POST %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

// TestFixPreviewsLive previews every fix the Setup and Upkeep cards offer on a real instance, and
// one that they do not, when SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB
// name one. It never applies anything.
func TestFixPreviewsLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	h, id := cardsHandler(t, domain.ServerInput{Name: "live", Host: host, Auth: domain.AuthWindows})
	fixes := []domain.Fix{}
	for _, card := range []string{"setup", "upkeep"} {
		rec := getPath(h, fmt.Sprintf("/api/servers/%d/databases/%s/cards/%s", id, url.PathEscape(dbName), card))
		var d domain.CardDetail
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
			t.Fatalf("%s detail: %d %s", card, rec.Code, rec.Body.String())
		}
		fixes = append(fixes, d.Fixes...)
	}
	offered := map[string]bool{}
	for _, f := range fixes {
		offered[f.ID] = true
		p := fmt.Sprintf("/api/servers/%d/databases/%s/fixes/%s/preview", id, url.PathEscape(dbName), f.ID)
		rec := postPath(h, p)
		var pv domain.FixPreview
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &pv) != nil || pv.Statement == "" || len(pv.Warnings) == 0 {
			t.Errorf("POST %s: %d %s", p, rec.Code, rec.Body.String())
		}
		t.Logf("%s: %s", f.ID, pv.Statement)
	}
	for _, def := range domain.FixDefs {
		if offered[def.ID] {
			continue
		}
		p := fmt.Sprintf("/api/servers/%d/databases/%s/fixes/%s/preview", id, url.PathEscape(dbName), def.ID)
		if rec := postPath(h, p); rec.Code != 404 {
			t.Errorf("POST %s (not offered): %d %s", p, rec.Code, rec.Body.String())
		}
		break
	}
}
