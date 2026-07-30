package server

import (
	"net/http"

	"opl-cloud/services/control-plane/internal/controlplane"
)

func registerCoreRoutes(mux *http.ServeMux, app *controlPlaneServer, service *controlplane.Service) {
	mux.HandleFunc("/w/", func(w http.ResponseWriter, r *http.Request) { app.proxyWorkspace(w, r, service) })
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { app.proxyWorkspaceRoot(w, r, service) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { app.proxyWorkspaceRoot(w, r, service) })
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
		cloudImagesReady := readiness["cloudImagesReady"] == true
		workspaceImagesReady := readiness["workspaceImagesReady"] == true
		immutableImagesReady := readiness["immutableImagesReady"] == true
		writeJSON(w, http.StatusOK, map[string]any{
			"provider": readiness["provider"], "ready": readiness["ready"] == true && cloudImagesReady && workspaceImagesReady && immutableImagesReady,
			"cloudImagesReady": cloudImagesReady, "workspaceImagesReady": workspaceImagesReady, "immutableImagesReady": immutableImagesReady, "checks": []any{},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { app.consoleStatic(w, r, service) })
}
