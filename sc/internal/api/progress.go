package api

import (
	"net/http"

	"sqlmonitor/internal/collect"
	"sqlmonitor/internal/service"
)

// progressRoutes registers the progress route of docs/phase4e-contract.md part 2.
func progressRoutes(mux *http.ServeMux, svc *service.Service) {
	// The long operations in progress on the instance (CHECKDB, backups, restores, index builds,
	// statistics updates, shrinks, database changes) for this database, tempdb, no database or
	// kakapo's own sessions; read live, kept nowhere (the fix dialog polls it while a long fix
	// applies). [] when nothing runs. 400 for a bad server id or database name, 404 for an unknown
	// server, 502 when the server cannot be reached or read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/progress", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		rows, err := svc.Progress(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, progressList(rows))
	})
}

// progressList answers [] rather than null when nothing runs.
func progressList(rows []collect.Progress) []collect.Progress {
	if rows == nil {
		return []collect.Progress{}
	}
	return rows
}
