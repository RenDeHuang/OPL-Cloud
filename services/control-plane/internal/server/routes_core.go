package server

import (
	"net/http"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerCoreRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("/w/", app.proxyWorkspace)
	mux.HandleFunc("/api/", app.proxyWorkspaceRoot)
	mux.HandleFunc("/ws", app.proxyWorkspaceRoot)
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/runtime/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeUpstreamError(w)
			return
		}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/production/readiness", func(w http.ResponseWriter, r *http.Request) {
		readiness, err := service.RuntimeReadiness(r.Context())
		if err != nil {
			writeUpstreamError(w)
			return
		}
		readiness["checks"] = []any{}
		writeJSON(w, http.StatusOK, readiness)
	})
	mux.HandleFunc("GET /api/overview", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"service": "control-plane", "workspaces": 0})
	}))
	mux.HandleFunc("/", app.consoleStatic)
}
