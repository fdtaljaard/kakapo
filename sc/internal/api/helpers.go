package api

import (
	"net/http"

	"sqlmonitor/internal/service"
)

// helpersRoutes registers the installed-helpers routes of docs/phase5e-contract.md. Filled in by
// that phase; empty until then.
func helpersRoutes(mux *http.ServeMux, svc *service.Service) {
	_, _ = mux, svc
}
