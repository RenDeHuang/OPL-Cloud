package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"opl-cloud/services/control-plane/internal/controlplane"
)

type announcementFixture struct {
	server   http.Handler
	store    *memoryTableStore
	operator *httptest.ResponseRecorder
	user     *httptest.ResponseRecorder
}

func newAnnouncementFixture(t *testing.T) announcementFixture {
	t.Helper()
	store := newMemoryTableStore()
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, &testSub2APIClient{balance: 1_000_000, charges: map[string]int64{}}), store)
	if err != nil {
		t.Fatal(err)
	}
	return announcementFixture{
		server: server, store: store, operator: operatorSessionForTest(t, server),
		user: loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!"),
	}
}

func decodeAnnouncementResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAnnouncementDraftPublishActiveReadWithdraw(t *testing.T) {
	fixture := newAnnouncementFixture(t)
	created := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Pilot maintenance","body":"Workspace access remains available."}`, "announcement-create")
	if created.Code != http.StatusCreated {
		t.Fatalf("create announcement status=%d body=%s", created.Code, created.Body.String())
	}
	draft := decodeAnnouncementResponse(t, created)
	id := stringValue(draft["id"])
	if id == "" || draft["status"] != "draft" || draft["read"] != false {
		t.Fatalf("draft announcement=%#v", draft)
	}

	startsAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	endsAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	published := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{"startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`, "announcement-publish")
	if published.Code != http.StatusOK || decodeAnnouncementResponse(t, published)["status"] != "published" {
		t.Fatalf("publish announcement status=%d body=%s", published.Code, published.Body.String())
	}

	active := requestWithSession(t, fixture.server, fixture.user, http.MethodGet, "/api/announcements", "")
	activeBody := decodeAnnouncementResponse(t, active)
	data := mapField(activeBody, "data")
	items, _ := data["items"].([]any)
	if active.Code != http.StatusOK || activeBody["source"] != "control-plane" || activeBody["status"] != "available" || len(items) != 1 {
		t.Fatalf("active announcements status=%d body=%#v", active.Code, activeBody)
	}
	item, ok := items[0].(map[string]any)
	if !ok || stringValue(item["id"]) != id || item["read"] != false {
		t.Fatalf("active announcements status=%d body=%#v", active.Code, activeBody)
	}
	if activeBody["sourceUpdatedAt"] != item["updatedAt"] {
		t.Fatalf("active announcements source timestamp=%#v item=%#v", activeBody["sourceUpdatedAt"], item)
	}

	firstRead := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/announcements/"+id+"/read", `{}`, "announcement-read")
	replayedRead := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/announcements/"+id+"/read", `{}`, "announcement-read")
	firstReadBody, replayedReadBody := decodeAnnouncementResponse(t, firstRead), decodeAnnouncementResponse(t, replayedRead)
	if firstRead.Code != http.StatusOK || replayedRead.Code != http.StatusOK || firstReadBody["announcementId"] != id || firstReadBody["readAt"] == "" || replayedReadBody["readAt"] != firstReadBody["readAt"] {
		t.Fatalf("read responses first=%d %#v replay=%d %#v", firstRead.Code, firstReadBody, replayedRead.Code, replayedReadBody)
	}

	withdrawn := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/withdraw", `{}`, "announcement-withdraw")
	withdrawnBody := decodeAnnouncementResponse(t, withdrawn)
	if withdrawn.Code != http.StatusOK || withdrawnBody["status"] != "withdrawn" {
		t.Fatalf("withdraw announcement status=%d body=%s", withdrawn.Code, withdrawn.Body.String())
	}
	readAfterWithdraw := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/announcements/"+id+"/read", `{}`, "announcement-read")
	readAfterWithdrawBody := decodeAnnouncementResponse(t, readAfterWithdraw)
	if readAfterWithdraw.Code != http.StatusOK || readAfterWithdrawBody["readAt"] != firstReadBody["readAt"] {
		t.Fatalf("read replay after withdraw status=%d first=%#v replay=%#v", readAfterWithdraw.Code, firstReadBody, readAfterWithdrawBody)
	}
	empty := requestWithSession(t, fixture.server, fixture.user, http.MethodGet, "/api/announcements", "")
	emptyBody := decodeAnnouncementResponse(t, empty)
	if empty.Code != http.StatusOK || emptyBody["status"] != "empty" || len(mapField(emptyBody, "data")["items"].([]any)) != 0 || emptyBody["sourceUpdatedAt"] != withdrawnBody["updatedAt"] {
		t.Fatalf("empty announcements status=%d body=%#v", empty.Code, emptyBody)
	}

	audits, err := fixture.store.ListAuditEvents(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if countRecords(audits, "action", "announcement.publish") != 1 || countRecords(audits, "action", "announcement.withdraw") != 1 {
		t.Fatalf("announcement audits=%#v", audits)
	}
}

func TestAnnouncementPublishReplaySurvivesServerRestart(t *testing.T) {
	store := NewTestEntStateStore(t, t.TempDir()+"/announcement-publish-replay.sqlite")
	service := controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, &testSub2APIClient{balance: 1_000_000, charges: map[string]int64{}})
	server, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	operator := operatorSessionForTest(t, server)
	created := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/announcements", `{"title":"Replay","body":"Same publish request"}`, "announcement-replay-create")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	id := stringValue(decodeAnnouncementResponse(t, created)["id"])
	startsAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	body := `{"startsAt":"` + startsAt + `"}`

	first := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", body, "announcement-replay-publish")
	serial := requestWithMutationKeyForTest(t, server, operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", body, "announcement-replay-publish")
	firstBody := decodeAnnouncementResponse(t, first)
	assertReplay := func(label string, response *httptest.ResponseRecorder) {
		t.Helper()
		replayed := decodeAnnouncementResponse(t, response)
		if response.Code != http.StatusOK || replayed["publishedAt"] != firstBody["publishedAt"] || replayed["updatedAt"] != firstBody["updatedAt"] {
			t.Fatalf("%s replay status=%d first=%#v replay=%#v", label, response.Code, firstBody, replayed)
		}
	}
	assertReplay("serial", serial)

	restarted, err := NewPersistentServer(service, store)
	if err != nil {
		t.Fatal(err)
	}
	assertReplay("restart", requestWithMutationKeyForTest(t, restarted, operatorSessionForTest(t, restarted), http.MethodPost, "/api/operator/announcements/"+id+"/publish", body, "announcement-replay-publish"))
}

func TestAnnouncementScheduleEditAndValidation(t *testing.T) {
	fixture := newAnnouncementFixture(t)
	created := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Initial title","body":"Initial body"}`, "announcement-schedule-create")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	id := stringValue(decodeAnnouncementResponse(t, created)["id"])

	edited := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPut, "/api/operator/announcements/"+id, `{"title":"Edited title","body":"Edited body"}`, "announcement-edit")
	editedBody := decodeAnnouncementResponse(t, edited)
	if edited.Code != http.StatusOK || editedBody["title"] != "Edited title" || editedBody["status"] != "draft" {
		t.Fatalf("edit status=%d body=%#v", edited.Code, editedBody)
	}
	unknownField := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPut, "/api/operator/announcements/"+id, `{"title":"Edited title","body":"Edited body","status":"published"}`, "announcement-edit-unknown")
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("edit accepted caller-owned status: status=%d body=%s", unknownField.Code, unknownField.Body.String())
	}
	missingStart := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{}`, "announcement-schedule-missing-start")
	if missingStart.Code != http.StatusBadRequest {
		t.Fatalf("publish accepted missing startsAt: status=%d body=%s", missingStart.Code, missingStart.Body.String())
	}

	startsAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	endsAt := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339Nano)
	scheduled := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{"startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`, "announcement-schedule")
	if scheduled.Code != http.StatusOK || decodeAnnouncementResponse(t, scheduled)["status"] != "scheduled" {
		t.Fatalf("schedule status=%d body=%s", scheduled.Code, scheduled.Body.String())
	}
	active := requestWithSession(t, fixture.server, fixture.user, http.MethodGet, "/api/announcements", "")
	activeBody := decodeAnnouncementResponse(t, active)
	if active.Code != http.StatusOK || activeBody["status"] != "empty" {
		t.Fatalf("future announcement became active: status=%d body=%#v", active.Code, activeBody)
	}

	conflictingReplay := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{"startsAt":"`+startsAt+`","endsAt":"`+time.Now().UTC().Add(3*time.Hour).Format(time.RFC3339Nano)+`"}`, "announcement-schedule")
	if conflictingReplay.Code != http.StatusConflict {
		t.Fatalf("same key accepted a different schedule: status=%d body=%s", conflictingReplay.Code, conflictingReplay.Body.String())
	}
	malformed := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{"startsAt":"tomorrow","endsAt":"later"}`, "announcement-schedule-malformed")
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed schedule status=%d body=%s", malformed.Code, malformed.Body.String())
	}

	withdrawn := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/withdraw", `{}`, "announcement-schedule-withdraw")
	if withdrawn.Code != http.StatusOK {
		t.Fatalf("withdraw status=%d body=%s", withdrawn.Code, withdrawn.Body.String())
	}
	withdrawnEdit := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPut, "/api/operator/announcements/"+id, `{"title":"Still withdrawn","body":"Edited without publishing"}`, "announcement-withdrawn-edit")
	withdrawnEditBody := decodeAnnouncementResponse(t, withdrawnEdit)
	if withdrawnEdit.Code != http.StatusOK || withdrawnEditBody["status"] != "withdrawn" {
		t.Fatalf("ordinary edit republished withdrawn announcement: status=%d body=%#v", withdrawnEdit.Code, withdrawnEditBody)
	}
}

func TestAnnouncementDraftAcceptsOptionalScheduleFields(t *testing.T) {
	fixture := newAnnouncementFixture(t)
	startsAt := "2026-07-20T09:00:00+08:00"
	endsAt := "2026-07-20T12:00:00+08:00"
	created := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Scheduled draft","body":"Draft schedule","startsAt":"`+startsAt+`","endsAt":"`+endsAt+`"}`, "announcement-draft-schedule-create")
	createdBody := decodeAnnouncementResponse(t, created)
	if created.Code != http.StatusCreated || createdBody["startsAt"] != "2026-07-20T01:00:00Z" || createdBody["endsAt"] != "2026-07-20T04:00:00Z" {
		t.Fatalf("scheduled draft create status=%d body=%#v", created.Code, createdBody)
	}
	id := stringValue(createdBody["id"])
	updated := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPut, "/api/operator/announcements/"+id, `{"title":"Updated draft","body":"Updated schedule","startsAt":"2026-07-21T01:00:00Z","endsAt":"2026-07-21T02:00:00Z"}`, "announcement-draft-schedule-update")
	updatedBody := decodeAnnouncementResponse(t, updated)
	if updated.Code != http.StatusOK || updatedBody["startsAt"] != "2026-07-21T01:00:00Z" || updatedBody["endsAt"] != "2026-07-21T02:00:00Z" {
		t.Fatalf("scheduled draft update status=%d body=%#v", updated.Code, updatedBody)
	}
}

func TestAnnouncementAuthorizationIdempotencyAndReadIsolation(t *testing.T) {
	fixture := newAnnouncementFixture(t)
	seedTenantMember(t, fixture.store, "acct-beta", "org-beta", "usr-beta", "beta@example.com")
	beta := loginForTest(t, fixture.server, "beta@example.com", "CorrectHorseBatteryStaple!")

	forbidden := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/operator/announcements", `{"title":"No","body":"No"}`, "announcement-forbidden")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("customer created operator announcement: status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
	first := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Shared","body":"Pilot notice"}`, "announcement-idempotent")
	replayed := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Shared","body":"Pilot notice"}`, "announcement-idempotent")
	firstBody, replayedBody := decodeAnnouncementResponse(t, first), decodeAnnouncementResponse(t, replayed)
	if first.Code != http.StatusCreated || replayed.Code != http.StatusCreated || firstBody["id"] != replayedBody["id"] {
		t.Fatalf("create replay first=%d %#v replay=%d %#v", first.Code, firstBody, replayed.Code, replayedBody)
	}
	conflict := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements", `{"title":"Changed","body":"Pilot notice"}`, "announcement-idempotent")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("same key accepted different draft: status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	id := stringValue(firstBody["id"])
	startsAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	published := requestWithMutationKeyForTest(t, fixture.server, fixture.operator, http.MethodPost, "/api/operator/announcements/"+id+"/publish", `{"startsAt":"`+startsAt+`"}`, "announcement-isolation-publish")
	if published.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	invalidRead := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/announcements/"+id+"/read", `null`, "announcement-isolation-invalid-read")
	if invalidRead.Code != http.StatusBadRequest {
		t.Fatalf("mark read accepted JSON null: status=%d body=%s", invalidRead.Code, invalidRead.Body.String())
	}
	read := requestWithMutationKeyForTest(t, fixture.server, fixture.user, http.MethodPost, "/api/announcements/"+id+"/read", `{}`, "announcement-isolation-read")
	if read.Code != http.StatusOK {
		t.Fatalf("mark read status=%d body=%s", read.Code, read.Body.String())
	}
	betaList := requestWithSession(t, fixture.server, beta, http.MethodGet, "/api/announcements", "")
	betaItems, _ := mapField(decodeAnnouncementResponse(t, betaList), "data")["items"].([]any)
	if betaList.Code != http.StatusOK || len(betaItems) != 1 || betaItems[0].(map[string]any)["read"] != false {
		t.Fatalf("read state crossed users: status=%d body=%s", betaList.Code, betaList.Body.String())
	}
}

type unavailableAnnouncementStore struct{ *memoryTableStore }

func (s *unavailableAnnouncementStore) ListAnnouncements(context.Context) ([]map[string]any, error) {
	return nil, errors.New("database unavailable")
}

func TestAnnouncementSourceUnavailableHasNoFallbackData(t *testing.T) {
	store := &unavailableAnnouncementStore{memoryTableStore: newMemoryTableStore()}
	seedTenantMember(t, store, "acct-alpha", "org-alpha", "usr-alpha", "alpha@example.com")
	server, err := NewPersistentServer(controlplane.NewService(fakeLedgerClient{}, &fakeFabricClient{}, &testSub2APIClient{balance: 1_000_000, charges: map[string]int64{}}), store)
	if err != nil {
		t.Fatal(err)
	}
	user := loginForTest(t, server, "alpha@example.com", "CorrectHorseBatteryStaple!")
	response := requestWithSession(t, server, user, http.MethodGet, "/api/announcements", "")
	body := decodeAnnouncementResponse(t, response)
	if response.Code != http.StatusInternalServerError || body["source"] != "control-plane" || body["status"] != "unavailable" || body["available"] != false {
		t.Fatalf("unavailable response status=%d body=%#v", response.Code, body)
	}
	if _, ok := body["data"]; ok {
		t.Fatalf("unavailable announcement response carried fallback data: %#v", body)
	}
	if _, ok := body["sourceUpdatedAt"]; ok {
		t.Fatalf("unavailable announcement response carried source timestamp: %#v", body)
	}
}

func countRecords(rows []map[string]any, field, value string) int {
	count := 0
	for _, row := range rows {
		if stringValue(row[field]) == value {
			count++
		}
	}
	return count
}
