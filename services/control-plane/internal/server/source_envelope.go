package server

import (
	"net/http"
	"time"
)

func writeSourceEnvelope(w http.ResponseWriter, httpStatus int, source, status string, data any, sourceUpdatedAt ...string) {
	updatedAt := ""
	if len(sourceUpdatedAt) != 0 {
		updatedAt = sourceUpdatedAt[0]
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, httpStatus, sourceEnvelope(source, status, data, updatedAt))
}

func sourceEnvelope(source, status string, data any, sourceUpdatedAt string) map[string]any {
	body := map[string]any{
		"source": source, "status": status, "available": status != "unavailable",
		"fetchedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if sourceUpdatedAt != "" {
		body["sourceUpdatedAt"] = sourceUpdatedAt
	}
	if status != "unavailable" {
		body["data"] = data
	}
	return body
}
