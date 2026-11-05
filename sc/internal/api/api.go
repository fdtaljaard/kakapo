// Package api exposes the service over HTTP JSON and serves the embedded UI.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// Version is the build stamp main sets from -ldflags; shown on the Home page and in /api/health
// so "which build am I looking at" is never a guess.
var Version = "dev"

// Handler builds the HTTP routes: /api/* for JSON, everything else is the single-page UI.
func Handler(svc *service.Service, ui fs.FS) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now(), "version": Version})
	})

	mux.HandleFunc("GET /api/servers", func(w http.ResponseWriter, r *http.Request) {
		servers, err := svc.ListServers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, servers)
	})

	mux.HandleFunc("POST /api/servers/test", func(w http.ResponseWriter, r *http.Request) {
		var in domain.ServerInput
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		info, err := svc.TestConnection(r.Context(), in)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})

	mux.HandleFunc("POST /api/servers", func(w http.ResponseWriter, r *http.Request) {
		var in domain.ServerInput
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		srv, err := svc.RegisterServer(r.Context(), in)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusCreated, srv)
	})

	mux.HandleFunc("GET /api/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		srv, err := svc.GetServer(r.Context(), id)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, srv)
	})

	// PUT edits a registration: test first, change nothing on failure. An empty password on a
	// SQL login keeps the stored one.
	mux.HandleFunc("PUT /api/servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var in domain.ServerInput
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		srv, err := svc.UpdateServer(r.Context(), id, in)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, srv)
	})

	mux.HandleFunc("GET /api/servers/{id}/databases", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		dbs, err := svc.ListDatabases(r.Context(), id)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, dbs)
	})

	mux.HandleFunc("PUT /api/servers/{id}/database", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var in struct {
			Database string `json:"database"`
		}
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		name := strings.TrimSpace(in.Database)
		if err := checkDatabaseName(name); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		srv, err := svc.RememberDatabase(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, srv)
	})

	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/overview", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		o, err := svc.Overview(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	})

	// One card with its findings and checks, read live (docs/phase3a-contract.md).
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/cards/{card}", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		detail, err := svc.CardDetail(r.Context(), id, name, r.PathValue("card"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	})

	// STO10 on request: is there room to run DBCC CHECKDB? Runs the estimate, stores and returns it
	// (docs/phase3b-contract.md). 409 when the database is not online, 502 when the DBCC fails.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/cards/storage/checkdb-room", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		room, err := svc.CheckdbRoom(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, room)
	})

	// DLK01 on request: the deadlocks of the last 7 days from the system_health session, grouped by
	// procedure pair; stores and returns the report (docs/phase3e-contract.md). 502 when the session
	// cannot be read.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/cards/performance/deadlocks", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		report, err := svc.DeadlockReport(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	})

	// BLK01 on request: the blocking of the last days ({"days": 1 to 7}, 7 without a body) from the
	// system_health session, grouped by head blocker; stores and returns it (docs/phase5d-contract.md).
	// 400 for days out of range, 502 when the session cannot be read.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/cards/performance/blocking-history", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var body struct {
			Days int `json:"days"`
		}
		if r.Body != nil {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
			if err == nil && len(bytes.TrimSpace(raw)) > 0 {
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				err = dec.Decode(&body)
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, errors.New("invalid request body: "+err.Error()))
				return
			}
		}
		if body.Days == 0 {
			body.Days = domain.BlockingHistoryDays
		}
		if body.Days < 1 || body.Days > domain.BlockingHistoryDays {
			writeError(w, http.StatusBadRequest, fmt.Errorf("days must be 1 to %d", domain.BlockingHistoryDays))
			return
		}
		report, err := svc.BlockingHistory(r.Context(), id, name, body.Days)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, report)
	})

	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/changes", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		limit, err := changesLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		changes, err := svc.ListChanges(r.Context(), id, name, limit)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, changes)
	})

	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/query-store/preview", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		p, err := svc.PreviewQueryStoreFix(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/query-store/apply", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		res, err := svc.ApplyQueryStoreFix(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// The general fix pattern (docs/phase3c-contract.md): preview never writes; apply runs the
	// previewed statement, reads the value back and logs the change. 404 for an unknown fix id or
	// one whose finding did not fire, 409 when the database cannot be changed from here, 502 when
	// SQL Server refuses. Both take an optional body {"options": {"name": "value"}} with what the
	// user set in the preview (docs/phase3g-contract.md); fixes without inputs ignore it.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/fixes/{fixId}/preview", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		options, err := fixOptions(w, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		p, err := svc.PreviewFixWithOptions(r.Context(), id, name, r.PathValue("fixId"), options)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/fixes/{fixId}/apply", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		options, err := fixOptions(w, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		res, err := svc.ApplyFixWithOptions(r.Context(), id, name, r.PathValue("fixId"), options)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// Phase 4c: the investigation and sample routes, one file each (docs/phase4c-contract.md).
	queriesRoutes(mux, svc)
	blockingRoutes(mux, svc)
	sampleRoutes(mux, svc)
	progressRoutes(mux, svc) // docs/phase4e-contract.md
	tablesRoutes(mux, svc)   // docs/phase5a-contract.md
	tuningRoutes(mux, svc)   // docs/phase5c-contract.md
	helpersRoutes(mux, svc)  // docs/phase5e-contract.md
	runRoutes(mux, svc)      // docs/phase4f-contract.md
	loadRoutes(mux, svc)     // docs/phase5i-contract.md

	// Any other /api/ path is an unknown route, not a page of the UI.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such API route: %s %s", r.Method, r.URL.Path))
	})
	mux.Handle("/", spa(ui))
	return logRequests(mux)
}

// spa serves the built frontend and falls back to index.html for client-side routes.
func spa(ui fs.FS) http.Handler {
	files := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(ui, p); err != nil {
			r.URL.Path = "/"
			p = "index.html"
		}
		if p == "index.html" {
			// Never let a browser profile keep an old shell; the hashed assets under /assets/ cache freely.
			w.Header().Set("Cache-Control", "no-store")
		}
		files.ServeHTTP(w, r)
	})
}

func serverID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("server id must be a positive number, not %q", r.PathValue("id"))
	}
	return id, nil
}

// changesLimit reads ?limit=N: 10 when absent, at most 100.
func changesLimit(r *http.Request) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 10, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive number, not %q", v)
	}
	return min(n, 100), nil
}

// serverAndDatabase reads {id} and {db}. The mux matches the escaped path, so a name with
// '/' or '%' in it arrives URL-encoded, and PathValue hands it back decoded.
func serverAndDatabase(r *http.Request) (int64, string, error) {
	id, err := serverID(r)
	if err != nil {
		return 0, "", err
	}
	name := r.PathValue("db")
	if err := checkDatabaseName(name); err != nil {
		return 0, "", err
	}
	return id, name, nil
}

// checkDatabaseName refuses an empty name and one longer than SQL Server allows (sysname).
func checkDatabaseName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 128 {
		return errors.New("database name must be 1 to 128 characters")
	}
	return nil
}

// statusFor maps a service error to the status codes in docs/phase1-contract.md.
func statusFor(err error) int {
	var se *service.Error
	if errors.As(err, &se) {
		switch se.Kind {
		case service.KindNotFound:
			return http.StatusNotFound
		case service.KindConflict:
			return http.StatusConflict
		case service.KindUnreachable:
			return http.StatusBadGateway
		case service.KindInvalid:
			return http.StatusBadRequest
		}
	}
	return http.StatusInternalServerError
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	return nil
}

// fixOptions reads the optional body of a fix preview or apply, {"options": {"name": "value"}}: nil
// when there is no body.
func fixOptions(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return nil, errors.New("invalid request body: " + err.Error())
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var body struct {
		Options map[string]string `json:"options"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, errors.New("invalid request body: " + err.Error())
	}
	return body.Options, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		}
	})
}
