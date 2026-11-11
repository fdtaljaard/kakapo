package api

import (
	"net/http"

	"sqlmonitor/internal/service"
)

// queriesRoutes registers the query page of docs/phase4c-contract.md, part 1. Its fixes
// (force-plan, unforce-plan, evict-plan, update-statistics-table) go through the generic fix routes
// with their options.
func queriesRoutes(mux *http.ServeMux, svc *service.Service) {
	// One query of a database by its query hash (0x and 16 hex digits, as a QRY01 finding's
	// evidence shows it): plans, what they say, history, waits, the procedure's statements, the
	// tables, the plain sentences and the hand-over. Reads only. 400 for a hash that is not one, 404
	// for an unknown server or database, 409 when the database is not online, 502 when the server
	// cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/queries/{hash}", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		d, err := svc.QueryDetail(r.Context(), id, name, r.PathValue("hash"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	// The Indexes page (docs/phase5t-contract.md): the indexes that the slowest queries of the last 7 days
	// read in full, from Query Store, with those queries as the evidence. Reads only; nothing is
	// measured. 404 for an unknown server or database; 409 when the database is not online; 502 when the
	// server cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/indexes/candidates", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		l, err := svc.IndexLens(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	})
	// Measure one index a plan reads in full (docs/phase5q-contract.md, section 4): its pages, how full
	// and how out of order, and the reorganize-index action when there is something to gain. Reads
	// only. 400 without schema, table and index; 404 for an unknown server or database; 409 when the
	// database is not online; 502 when the server cannot be read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/indexes/shape", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		q := r.URL.Query()
		x, err := svc.IndexShape(r.Context(), id, name, q.Get("schema"), q.Get("table"), q.Get("index"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, x)
	})
}
