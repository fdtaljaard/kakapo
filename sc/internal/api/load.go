package api

import (
	"errors"
	"net/http"
	"strings"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// loadRoutes registers the load strip's route (docs/phase5i-contract.md, section 2).
func loadRoutes(mux *http.ServeMux, svc *service.Service) {
	// The load now and today against the usual range of the selected days, with the live red flags.
	// ?days= 7, 14 or 30 (default 7); ?refresh=1 reads only today, the last 30 minutes and the live
	// checks when the comparison is kept. Reads only. 400 for an option that is not right, 404 for an
	// unknown server or database, 409 when the database is not online, 502 when the server cannot be
	// read.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/load", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		q := r.URL.Query()
		days, err := domain.ParseLoadDays(q.Get("days"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		refresh, err := parseRefresh(q.Get("refresh"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		l, err := svc.Load(r.Context(), id, name, days, refresh)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, l)
	})
}

// parseRefresh reads ?refresh=: empty or 0 for a full read, 1 for the minute refresh.
func parseRefresh(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	}
	return false, errors.New("refresh must be 1, or left out")
}
