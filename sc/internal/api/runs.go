package api

import (
	"net/http"
	"time"

	"sqlmonitor/internal/domain"
	"sqlmonitor/internal/service"
)

// runRoutes registers the run route of docs/phase4f-contract.md part 1.
func runRoutes(mux *http.ServeMux, svc *service.Service) {
	// One run of a long fix (CHECKDB now, an index, statistics, the TempDB files), which the apply
	// answered with pending: still pending, or its result, or its error as the apply would have
	// answered it (the status code in error.status). Read from memory, nothing is dialled. 400 for a
	// bad server id or database name, 404 for an unknown run (or one of another server or database,
	// ended more than 24 hours ago, or from before a restart).
	mux.HandleFunc("GET /api/servers/{id}/databases/{db}/fixes/runs/{runId}", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		run, err := svc.FixRunStatus(r.Context(), id, name, r.PathValue("runId"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, runJSON(run))
	})
	// Stop a pending run (docs/phase5t-contract.md): the statement in flight is cancelled, which keeps
	// the work it did, and a batch marks what it had not started as stopped. Answers the run. 404 for
	// an unknown run, 409 for one that has ended.
	mux.HandleFunc("POST /api/servers/{id}/databases/{db}/fixes/runs/{runId}/stop", func(w http.ResponseWriter, r *http.Request) {
		id, name, err := serverAndDatabase(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		run, err := svc.StopFixRun(r.Context(), id, name, r.PathValue("runId"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, runJSON(run))
	})
}

// fixRunJSON is a run as the route answers it.
type fixRunJSON struct {
	RunID     string            `json:"runId"`
	FixID     string            `json:"fixId"`
	Database  string            `json:"database"`
	StartedAt time.Time         `json:"startedAt"`
	EndedAt   *time.Time        `json:"endedAt,omitempty"`
	Pending   bool              `json:"pending"`
	Result    *domain.FixResult `json:"result,omitempty"`
	Error     *runErrorJSON     `json:"error,omitempty"`
	// Items are the parts of a batch run, each with its state; Stopping says a stop was asked for.
	Items    []domain.RunItem `json:"items,omitempty"`
	Stopping bool             `json:"stopping,omitempty"`
}

// runErrorJSON is a run's error: the status code and message the apply would have answered.
type runErrorJSON struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

func runJSON(run service.FixRun) fixRunJSON {
	out := fixRunJSON{RunID: run.RunID, FixID: run.FixID, Database: run.Database, StartedAt: run.StartedAt,
		EndedAt: run.EndedAt, Pending: run.Pending, Result: run.Result, Items: run.Items, Stopping: run.Stopping}
	if run.Err != nil {
		out.Error = &runErrorJSON{Status: statusFor(run.Err), Message: run.Err.Error()}
	}
	return out
}
