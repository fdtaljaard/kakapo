package api

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	"sqlmonitor/internal/service"
)

// memoryRoutes registers the Memory screen of docs/phase5v-contract.md. Read-only on the server. The two
// POSTs run the slow buffer pool reads and keep a snapshot; the page's Stop aborts the request, which
// cancels its context, and so the statement on the server.
func memoryRoutes(mux *http.ServeMux, svc *service.Service) {
	// Panels 2 and 3 read now, panel 4 from the latest snapshot; ?snapshot=N shows that snapshot instead.
	// 400 for a bad id, 404 for an unknown server or snapshot, 502 when the server cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/memory", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		snap, err := snapshotParam(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := svc.Memory(r.Context(), id, snap)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})

	// Read the cache by database now (the slow read) and keep a snapshot.
	mux.HandleFunc("POST /api/servers/{id}/memory/snapshot", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := svc.TakeMemorySnapshot(r.Context(), id)
		if err != nil {
			writeMemoryError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})

	// Database detail as last read (or ?snapshot=N); reads nothing from the server.
	mux.HandleFunc("GET /api/servers/{id}/memory/databases/{db}", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		snap, err := snapshotParam(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := svc.DatabaseMemory(r.Context(), id, name, snap)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})

	// Read one database's cache now, index by index (the slow read), and keep a snapshot. 409 for a
	// database that is not online.
	mux.HandleFunc("POST /api/servers/{id}/memory/databases/{db}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := svc.TakeDatabaseMemory(r.Context(), id, name)
		if err != nil {
			writeMemoryError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
}

// snapshotParam reads ?snapshot=N: 0 when absent.
func snapshotParam(r *http.Request) (int64, error) {
	v := r.URL.Query().Get("snapshot")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("snapshot must be a positive number, not %q", v)
	}
	return n, nil
}

// writeMemoryError answers a slow read that failed; one the page stopped has no one left to answer.
func writeMemoryError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		log.Printf("%s %s stopped from the page", r.Method, r.URL.Path)
		return
	}
	writeError(w, statusFor(err), err)
}
