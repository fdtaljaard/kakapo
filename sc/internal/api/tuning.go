package api

import (
	"net/http"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// tuningRoutes registers the tuning report route and the record-a-change route of
// docs/phase5c-contract.md.
func tuningRoutes(mux *http.ServeMux, svc *service.Service) {
	// The tuning report: Query Store hour by hour with the change log, compared around a change.
	// ?days= 1 to 30 (default 7), ?change= a change id or none (default the latest change in the
	// window), ?hours= HH-HH business hours (default 08-17). Reads only. 400 for an option that is
	// not right, 404 for an unknown server, database or a change not in the window, 409 when the
	// database is not online or the server is older than SQL Server 2016, 502 when the server cannot
	// be read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/tuning", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		q := r.URL.Query()
		days, err := domain.ParseTuningDays(q.Get("days"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		change, err := domain.ParseTuningChange(q.Get("change"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		hours, err := domain.ParseBusinessHours(q.Get("hours"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		rep, err := svc.TuningReport(r.Context(), id, name, days, change, hours)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, rep)
	})

	// Record a change made by hand: {changedAt, object, changeType, description}. changedAt is
	// server time ("2026-11-03T14:20"), RFC 3339 with an offset, or empty for now. Writes the change
	// log only. 201 with the change; 400 for a field that is not right, 404 for an unknown server,
	// 502 when a server time cannot be placed because the server's clock cannot be read.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/changes", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var in domain.RecordChangeInput
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := domain.ParseRecordChange(in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		c, err := svc.RecordChange(r.Context(), id, name, in)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	})
}
