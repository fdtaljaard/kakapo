package api

import (
	"net/http"

	"sqlmonitor/internal/service"
)

// tablesRoutes registers the table picker route of docs/phase5a-contract.md.
func tablesRoutes(mux *http.ServeMux, svc *service.Service) {
	// The user tables of a database for "Archive a table…": schema, name, rows, MB, the date columns
	// (indexed or not) and whether kakapo archives each, biggest first. Read live; nothing is
	// stored. 400 for a bad server id or database name, 404 for an unknown server or database, 409
	// for a database that is not online, 502 when the server cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/tables", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		list, err := svc.ListTables(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}
