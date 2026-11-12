package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"sqlmonitor/internal/service"
)

// changesRoutes registers the What changed? screen of docs/phase5w-contract.md (C4). Reads only on the server:
// the baseline, the trace harvest and the last log backups run in it, and each failure is a sentence in the
// answer, never a failed page.
func changesRoutes(mux *http.ServeMux, svc *service.Service) {
	// ?problemAt= (server time, or RFC 3339) sets the 7-day window; else ?from=&to= (RFC 3339, either open).
	// ?page=N is the Nth 100 rows, newest first; ?who=kakapo|outside and ?flagged=1 filter. 400 for a bad
	// parameter, 404 for an unknown server.
	mux.HandleFunc("GET /api/servers/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		id, err := serverID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		q, err := changesQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := svc.Changes(r.Context(), id, q)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, page)
	})
}

func changesQuery(r *http.Request) (service.ChangesQuery, error) {
	v := r.URL.Query()
	q := service.ChangesQuery{ProblemAt: v.Get("problemAt"), Who: v.Get("who"), Flagged: v.Get("flagged") == "1" || v.Get("flagged") == "true"}
	switch q.Who {
	case "", "kakapo", "outside":
	default:
		return q, fmt.Errorf("who must be kakapo or outside, not %q", q.Who)
	}
	if p := v.Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return q, fmt.Errorf("page must be 0 or more, not %q", p)
		}
		q.Page = n
	}
	for name, dst := range map[string]**time.Time{"from": &q.From, "to": &q.To} {
		if s := v.Get(name); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return q, fmt.Errorf("%s must be a time like 2026-11-03T14:20:00Z, not %q", name, s)
			}
			t = t.UTC()
			*dst = &t
		}
	}
	return q, nil
}
