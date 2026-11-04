package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"sqlmonitor/internal/service"
)

// sampleRoutes registers the live sample of the Performance card (docs/phase4c-contract.md, part 3).
func sampleRoutes(mux *http.ServeMux, svc *service.Service) {
	// Runs the sample now: {"seconds": 30} (10 to 120; no body or 0 for 30). The request returns when
	// the sample is done. 400 for seconds out of range, 404 for an unknown server or database, 502
	// when the server cannot be read.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/cards/performance/sample", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		seconds, err := sampleSeconds(w, r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		sample, err := svc.PerformanceSample(r.Context(), id, name, seconds)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, sample)
	})

	// The last stored sample of the database, or null when none was run; reads the case file only.
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/cards/performance/sample", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		last, err := svc.LastPerformanceSample(r.Context(), id, name)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, last)
	})
}

// sampleSeconds reads the optional body {"seconds": n}: 0 when there is none.
func sampleSeconds(w http.ResponseWriter, r *http.Request) (int, error) {
	if r.Body == nil {
		return 0, nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		return 0, errors.New("invalid request body: " + err.Error())
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}
	var body struct {
		Seconds int `json:"seconds"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return 0, errors.New("invalid request body: " + err.Error())
	}
	return body.Seconds, nil
}
