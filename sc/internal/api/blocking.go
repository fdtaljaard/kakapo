package api

import (
	"net/http"

	"sqlmonitor/internal/service"
)

// blockingRoutes registers the blocking page's route (docs/phase4c-contract.md, part 2). Its two
// fixes, kill-session and rcsi, go through the generic fix routes.
func blockingRoutes(mux *http.ServeMux, svc *service.Service) {
	// The blocking chain now, across the instance, as a tree with its sentences; read live, kept
	// nowhere (the page polls it while it watches). 404 for an unknown server or database, 502 when
	// the server cannot be reached or read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/blocking", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		tree, err := svc.Blocking(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, tree)
	})
}
