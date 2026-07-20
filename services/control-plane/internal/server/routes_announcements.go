package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type AnnouncementDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Status      string `json:"status"`
	StartsAt    string `json:"startsAt,omitempty"`
	EndsAt      string `json:"endsAt,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	Read        bool   `json:"read"`
}

type AnnouncementPageDTO struct {
	Items    []AnnouncementDTO `json:"items"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
}

type AnnouncementReadDTO struct {
	AnnouncementID string `json:"announcementId"`
	ReadAt         string `json:"readAt"`
}

type AnnouncementDraftRequest struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	StartsAt string `json:"startsAt"`
	EndsAt   string `json:"endsAt"`
}

type AnnouncementScheduleRequest struct {
	StartsAt string `json:"startsAt"`
	EndsAt   string `json:"endsAt"`
}

func registerAnnouncementRoutes(mux *http.ServeMux, app *controlPlaneServer) {
	mux.HandleFunc("GET /api/announcements", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.listActiveAnnouncements(w, r)
	}))
	mux.HandleFunc("POST /api/announcements/{announcementId}/read", app.protected(false, func(w http.ResponseWriter, r *http.Request) {
		app.markAnnouncementRead(w, r)
	}))
	mux.HandleFunc("GET /api/operator/announcements", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.listOperatorAnnouncements(w, r)
	}))
	mux.HandleFunc("POST /api/operator/announcements", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.createAnnouncement(w, r)
	}))
	mux.HandleFunc("PUT /api/operator/announcements/{announcementId}", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.updateAnnouncement(w, r)
	}))
	mux.HandleFunc("POST /api/operator/announcements/{announcementId}/publish", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.publishAnnouncement(w, r)
	}))
	mux.HandleFunc("POST /api/operator/announcements/{announcementId}/withdraw", app.protected(true, func(w http.ResponseWriter, r *http.Request) {
		app.withdrawAnnouncement(w, r)
	}))
}

func (app *controlPlaneServer) listActiveAnnouncements(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := operatorPagination(w, r)
	if !ok {
		return
	}
	user, ok := app.sessionUserContext(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not_authenticated")
		return
	}
	rows, err := app.tables.ListAnnouncements(r.Context())
	if err != nil {
		writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
		return
	}
	reads, err := app.tables.ListAnnouncementReads(r.Context(), stringValue(user["id"]))
	if err != nil {
		writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
		return
	}
	readIDs := map[string]bool{}
	for _, read := range reads {
		readIDs[stringValue(read["announcementId"])] = true
	}
	now := time.Now().UTC()
	items := make([]AnnouncementDTO, 0, len(rows))
	for _, row := range rows {
		if announcementIsActive(row, now) {
			items = append(items, announcementDTO(row, readIDs[stringValue(row["id"])]))
		}
	}
	writeAnnouncementPage(w, page, pageSize, items, latestAnnouncementUpdatedAt(rows))
}

func (app *controlPlaneServer) listOperatorAnnouncements(w http.ResponseWriter, r *http.Request) {
	page, pageSize, ok := operatorPagination(w, r)
	if !ok {
		return
	}
	rows, err := app.tables.ListAnnouncements(r.Context())
	if err != nil {
		writeSourceEnvelope(w, http.StatusInternalServerError, "control-plane", "unavailable", nil)
		return
	}
	items := make([]AnnouncementDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, announcementDTO(row, false))
	}
	writeAnnouncementPage(w, page, pageSize, items, latestAnnouncementUpdatedAt(rows))
}

func writeAnnouncementPage(w http.ResponseWriter, page, pageSize int, items []AnnouncementDTO, sourceUpdatedAt string) {
	sort.Slice(items, func(i, j int) bool {
		return firstNonEmpty(items[i].PublishedAt, items[i].CreatedAt, items[i].ID) > firstNonEmpty(items[j].PublishedAt, items[j].CreatedAt, items[j].ID)
	})
	total := len(items)
	start := (page - 1) * pageSize
	if start >= total {
		items = []AnnouncementDTO{}
	} else {
		end := start + pageSize
		if end > total {
			end = total
		}
		items = items[start:end]
	}
	status := "available"
	if total == 0 {
		status = "empty"
	}
	writeSourceEnvelope(w, http.StatusOK, "control-plane", status, AnnouncementPageDTO{Items: items, Total: total, Page: page, PageSize: pageSize}, sourceUpdatedAt)
}

func latestAnnouncementUpdatedAt(rows []map[string]any) string {
	latest := ""
	for _, row := range rows {
		if updatedAt := stringValue(row["updatedAt"]); updatedAt > latest {
			latest = updatedAt
		}
	}
	return latest
}

func (app *controlPlaneServer) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input AnnouncementDraftRequest
	if !decodeAnnouncementBody(w, r, &input) || !validAnnouncementText(input.Title, input.Body) {
		writeError(w, http.StatusBadRequest, "invalid_announcement")
		return
	}
	user, _ := app.sessionUserContext(r)
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	startsAt, startsTime, startsOK := announcementScheduleTime(input.StartsAt, false)
	endsAt, endsTime, endsOK := announcementScheduleTime(input.EndsAt, false)
	if !startsOK || !endsOK || (!startsTime.IsZero() && !endsTime.IsZero() && !endsTime.After(startsTime)) {
		writeError(w, http.StatusBadRequest, "invalid_announcement")
		return
	}
	id := "announcement-" + stableID(stringValue(user["id"]), key)[:18]
	mutation := app.newAnnouncementMutation(r, key, "announcement.create", id, true, nil, map[string]any{
		"title": input.Title, "body": input.Body, "status": "draft", "startsAt": startsAt, "endsAt": endsAt, "publishedAt": "",
		"createdByUserId": stringValue(user["id"]), "updatedByUserId": stringValue(user["id"]),
	}, input.Title, input.Body, startsAt, endsAt)
	app.writeAnnouncementMutation(w, r, mutation, http.StatusCreated)
}

func (app *controlPlaneServer) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input AnnouncementDraftRequest
	if !decodeAnnouncementBody(w, r, &input) || !validAnnouncementText(input.Title, input.Body) {
		writeError(w, http.StatusBadRequest, "invalid_announcement")
		return
	}
	user, _ := app.sessionUserContext(r)
	id := strings.TrimSpace(r.PathValue("announcementId"))
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	startsAt, startsTime, startsOK := announcementScheduleTime(input.StartsAt, false)
	endsAt, endsTime, endsOK := announcementScheduleTime(input.EndsAt, false)
	if !startsOK || !endsOK || (!startsTime.IsZero() && !endsTime.IsZero() && !endsTime.After(startsTime)) {
		writeError(w, http.StatusBadRequest, "invalid_announcement")
		return
	}
	mutation := app.newAnnouncementMutation(r, key, "announcement.update", id, false, []string{"draft", "scheduled", "withdrawn"}, map[string]any{
		"title": input.Title, "body": input.Body, "startsAt": startsAt, "endsAt": endsAt, "updatedByUserId": stringValue(user["id"]),
	}, input.Title, input.Body, startsAt, endsAt)
	app.writeAnnouncementMutation(w, r, mutation, http.StatusOK)
}

func (app *controlPlaneServer) publishAnnouncement(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input AnnouncementScheduleRequest
	if !decodeAnnouncementBody(w, r, &input) {
		writeError(w, http.StatusBadRequest, "invalid_announcement_schedule")
		return
	}
	now := time.Now().UTC()
	startsAt, startsTime, ok := announcementScheduleTime(input.StartsAt, true)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_announcement_schedule")
		return
	}
	endsAt, endsTime, ok := announcementScheduleTime(input.EndsAt, false)
	if !ok || (!endsTime.IsZero() && !endsTime.After(startsTime)) {
		writeError(w, http.StatusBadRequest, "invalid_announcement_schedule")
		return
	}
	status := "published"
	if startsTime.After(now) {
		status = "scheduled"
	}
	user, _ := app.sessionUserContext(r)
	id := strings.TrimSpace(r.PathValue("announcementId"))
	mutation := app.newAnnouncementMutation(r, key, "announcement.publish", id, false, []string{"draft", "scheduled", "withdrawn"}, map[string]any{
		"status": status, "startsAt": startsAt, "endsAt": endsAt, "publishedAt": now.Format(time.RFC3339Nano), "updatedByUserId": stringValue(user["id"]),
	}, startsAt, endsAt)
	app.writeAnnouncementMutation(w, r, mutation, http.StatusOK)
}

func (app *controlPlaneServer) withdrawAnnouncement(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredMutationKey(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decodeAnnouncementBody(w, r, &input) {
		writeError(w, http.StatusBadRequest, "invalid_announcement_withdrawal")
		return
	}
	user, _ := app.sessionUserContext(r)
	id := strings.TrimSpace(r.PathValue("announcementId"))
	mutation := app.newAnnouncementMutation(r, key, "announcement.withdraw", id, false, []string{"scheduled", "published"}, map[string]any{
		"status": "withdrawn", "updatedByUserId": stringValue(user["id"]),
	})
	app.writeAnnouncementMutation(w, r, mutation, http.StatusOK)
}

func (app *controlPlaneServer) markAnnouncementRead(w http.ResponseWriter, r *http.Request) {
	if _, ok := requiredMutationKey(w, r); !ok {
		return
	}
	var input struct{}
	if !decodeAnnouncementBody(w, r, &input) {
		writeError(w, http.StatusBadRequest, "invalid_announcement_read")
		return
	}
	user, _ := app.sessionUserContext(r)
	row, err := app.tables.MarkAnnouncementRead(r.Context(), strings.TrimSpace(r.PathValue("announcementId")), stringValue(user["id"]), time.Now().UTC().Format(time.RFC3339Nano))
	if errors.Is(err, errAnnouncementNotActive) {
		writeError(w, http.StatusNotFound, "announcement_not_active")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "announcement_read_failed")
		return
	}
	writeJSON(w, http.StatusOK, AnnouncementReadDTO{AnnouncementID: stringValue(row["announcementId"]), ReadAt: stringValue(row["readAt"])})
}

func (app *controlPlaneServer) newAnnouncementMutation(r *http.Request, key, action, id string, create bool, allowed []string, patch map[string]any, requestParts ...string) announcementMutation {
	user, _ := app.sessionUserContext(r)
	requestHash := stableID(append([]string{action, id}, requestParts...)...)
	audit := app.auditEvent(r, action, "announcement", id, "", nil, nil, "succeeded")
	audit["id"] = "audit-" + stableID(action, id, stringValue(user["id"]), key)[:18]
	return announcementMutation{AnnouncementID: id, Create: create, AllowedStatuses: allowed, RequestHash: requestHash, Patch: patch, AuditEvent: audit}
}

func (app *controlPlaneServer) writeAnnouncementMutation(w http.ResponseWriter, r *http.Request, mutation announcementMutation, status int) {
	if mutation.AnnouncementID == "" {
		writeError(w, http.StatusBadRequest, "invalid_announcement_id")
		return
	}
	row, err := app.tables.ApplyAnnouncementMutation(r.Context(), mutation)
	if errors.Is(err, errIdempotencyConflict) || errors.Is(err, errAnnouncementStateConflict) {
		writeError(w, http.StatusConflict, "announcement_conflict")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "announcement_write_failed")
		return
	}
	writeJSON(w, status, announcementDTO(row, false))
}

func decodeAnnouncementBody(w http.ResponseWriter, r *http.Request, target any) bool {
	var raw json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil {
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return false
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return false
	}
	return strict.Decode(&extra) == io.EOF
}

func validAnnouncementText(title, body string) bool {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	return title != "" && body != "" && utf8.RuneCountInString(title) <= 160 && utf8.RuneCountInString(body) <= 20_000 &&
		!strings.ContainsRune(title, '\x00') && !strings.ContainsRune(body, '\x00')
}

func announcementScheduleTime(value string, required bool) (string, time.Time, bool) {
	if value == "" {
		if required {
			return "", time.Time{}, false
		}
		return "", time.Time{}, true
	}
	if value != strings.TrimSpace(value) {
		return "", time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", time.Time{}, false
	}
	return parsed.UTC().Format(time.RFC3339Nano), parsed.UTC(), true
}

func announcementDTO(row map[string]any, read bool) AnnouncementDTO {
	return AnnouncementDTO{
		ID: stringValue(row["id"]), Title: stringValue(row["title"]), Body: stringValue(row["body"]), Status: stringValue(row["status"]),
		StartsAt: stringValue(row["startsAt"]), EndsAt: stringValue(row["endsAt"]), PublishedAt: stringValue(row["publishedAt"]),
		CreatedAt: stringValue(row["createdAt"]), UpdatedAt: stringValue(row["updatedAt"]), Read: read,
	}
}
