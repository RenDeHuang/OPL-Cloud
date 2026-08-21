package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"opl-cloud/services/ledger/internal/ledger"
)

func TestEvidenceIndexRoutesRecordQueryAndExportWithoutSecrets(t *testing.T) {
	store := ledger.NewMemoryStore()
	server := NewServer(store, "internal-secret")
	body := `{"operationId":"launch-alpha","candidateSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","candidateTree":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","imageDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","receiptId":"receipt-alpha","receiptType":"candidate.qualified.v1","status":"admission_ready","actor":"instance-medopl","observedAt":"2026-08-21T04:00:00Z","identityDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","redactedLink":"https://example.invalid/evidence/alpha"}`
	req := httptest.NewRequest(http.MethodPost, "/ledger/evidence-index", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "launch-alpha:instance-admission")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("record status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "launch-alpha:instance-admission") {
		t.Fatal("record response leaked idempotency key")
	}

	req = httptest.NewRequest(http.MethodGet, "/ledger/evidence-index?operationId=launch-alpha", nil)
	req.Header.Set("Authorization", "Bearer internal-secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"receiptId":"receipt-alpha"`) {
		t.Fatalf("query status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/ledger/evidence-index/export?candidateSha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	req.Header.Set("Authorization", "Bearer internal-secret")
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"schemaVersion":1`) || strings.Contains(rec.Body.String(), "launch-alpha:instance-admission") {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvidenceIndexCapabilityBindsOperationAndCandidate(t *testing.T) {
	const capabilityKey = "ledger-capability-key-for-evidence-index-tests-32-chars"
	store := ledger.NewMemoryStore()
	server := NewServerWithAuth(store, "internal-secret", capabilityKey)
	body := []byte(`{"operationId":"launch-capability","candidateSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","candidateTree":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","imageDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","receiptId":"receipt-capability","receiptType":"candidate.qualified.v1","status":"admission_ready","actor":"control-plane","observedAt":"2026-08-21T04:00:00Z","identityDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`)
	req := httptest.NewRequest(http.MethodPost, "/ledger/evidence-index", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "launch-capability:record")
	req.Header.Set(ledgerCapabilityHeader, testLedgerCapability(t, capabilityKey, ledgerCapabilityClaims{
		Version: 1, Caller: "control-plane", ResourceKind: "evidence_index", ResourceID: "launch-capability", Action: "record_evidence_index", OperationID: "launch-capability:record", ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}, body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("capability record status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/ledger/evidence-index?operationId=launch-capability", nil)
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set(ledgerCapabilityHeader, testLedgerCapability(t, capabilityKey, ledgerCapabilityClaims{
		Version: 1, Caller: "control-plane", ResourceKind: "evidence_index", ResourceID: "launch-capability", Action: "read_evidence_index", OperationID: requestOperationID(req), ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}, nil))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capability query status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEvidenceIndexExportIsJSONStable(t *testing.T) {
	store := ledger.NewMemoryStore()
	input := ledger.EvidenceIndexInput{
		OperationID: "stable", CandidateSHA: strings.Repeat("a", 40), CandidateTree: strings.Repeat("b", 40), ImageDigest: "sha256:" + strings.Repeat("c", 64),
		ReceiptID: "receipt", ReceiptType: "candidate.qualified.v1", Status: "completed", Actor: "actor", IdentityDigest: "sha256:" + strings.Repeat("d", 64),
		IdempotencyKey: "stable:once", ObservedAt: testObservedAt(),
	}
	if _, err := store.RecordEvidenceIndex(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	export, err := store.ExportEvidenceIndex(context.Background(), ledger.EvidenceIndexQuery{OperationID: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(export)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("export JSON is not reproducible: first=%s second=%s err=%v", first, second, err)
	}
}

func testObservedAt() time.Time {
	return time.Date(2026, 8, 21, 4, 0, 0, 0, time.UTC)
}
