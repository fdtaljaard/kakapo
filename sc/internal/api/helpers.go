package api

import (
	"net/http"

	"sqlmonitor/internal/service"
)

// helpersRoutes registers the installed-helpers route of docs/phase5e-contract.md.
func helpersRoutes(mux *http.ServeMux, svc *service.Service) {
	// The instance page's "Installed helpers" row: where kakapo's event session stands (running or
	// stopped, since when, its files, the events captured, the threshold, or nothing installed), the
	// fix the row offers (install-xe-session or remove-xe-session) and the database its preview and
	// apply go through. Read live; nothing is stored. 400 for a bad server id, 404 for an unknown
	// server, 502 when the server cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/helpers", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		h, err := svc.Helpers(r.Context(), id)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	})
}
