package server

import (
	"net/http"
	"time"
)

func writeSourceEnvelope(w http.ResponseWriter, httpStatus int, source, status string, data any) {
	w.Header().Set("Cache-Control", "private, no-store")
	body := map[string]any{
		"source": source, "status": status, "available": status != "unavailable",
		"fetchedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if status != "unavailable" {
		body["data"] = data
	}
	writeJSON(w, httpStatus, body)
}
