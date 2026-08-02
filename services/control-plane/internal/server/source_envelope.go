package server

import (
	"net/http"
	"strings"
	"time"
	"unicode"
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
	if status == "unavailable" {
		body["reasonCode"] = unavailableSourceReasonCode(source)
	}
	if status != "unavailable" {
		body["data"] = data
	}
	return body
}

func unavailableSourceReasonCode(source string) string {
	var code strings.Builder
	separator := false
	for _, character := range strings.ToLower(strings.TrimSpace(source)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			if separator && code.Len() > 0 {
				code.WriteByte('_')
			}
			code.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
	}
	if code.Len() == 0 {
		return "source_unavailable"
	}
	return code.String() + "_unavailable"
}
