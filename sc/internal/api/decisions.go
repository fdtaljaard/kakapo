package api

import (
	"net/http"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// decisionsRoutes registers the decision record routes of docs/phase5m-contract.md, section 6.
func decisionsRoutes(mux *http.ServeMux, svc *service.Service) {
	// The records of the row versioning decision on a database, newest first, each with its line and
	// whether its review is due. Reads the store only. 404 for an unknown server.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/decisions", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		rs, err := svc.Decisions(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, rs)
	})

	// Record a decision: {choice: keep|later, verdict, reason, reviewOn}. Writes the decision table only;
	// Change it is the rcsi fix's Apply. 201 with the record; 400 for keep without a reason, change, an
	// unknown choice or verdict, or a review date that is not a date; 404 for an unknown server.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/decisions", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var in domain.DecisionInput
		if err := readJSON(w, r, &in); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		rec, err := svc.RecordDecision(r.Context(), id, name, in)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, rec)
	})
}
