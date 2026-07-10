package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFabricClientCreatesJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/fabric/jobs" || r.Header.Get("Idempotency-Key") != "job-once" {
			t.Fatalf("unexpected request: %s %s key=%s", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		_ = json.NewEncoder(w).Encode(Job{JobID: "job-alpha", RequestID: "request-alpha", Status: "queued"})
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, upstream.Client())
	job, err := client.CreateJob(context.Background(), JobInput{OrganizationID: "org-alpha", WorkspaceID: "workspace-alpha", ProjectID: "project-alpha", TaskID: "task-alpha", RequestID: "request-alpha", ApprovalID: "approval-alpha"}, "job-once")
	if err != nil || job.JobID != "job-alpha" || job.Status != "queued" {
		t.Fatalf("job = %#v err=%v", job, err)
	}
}

func TestFabricClientReturnsErrorOnUpstreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fabric unavailable", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, upstream.Client())
	if _, err := client.Catalog(context.Background()); err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("expected upstream status error, got %v", err)
	}
}

func TestFabricClientReadsCompletedJobEvidence(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/fabric/jobs/job-alpha" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"jobId":"job-alpha","status":"succeeded","attempt":2,"artifactIds":["artifact-alpha"],"reviewIds":["review-alpha"]}`))
	}))
	defer upstream.Close()

	client := NewFabricHTTPClient(upstream.URL, upstream.Client())
	job, err := client.GetJob(context.Background(), "job-alpha")
	if err != nil || job.Status != "succeeded" || job.Attempt != 2 || len(job.ArtifactIDs) != 1 || len(job.ReviewIDs) != 1 {
		t.Fatalf("job = %#v err=%v", job, err)
	}
}
