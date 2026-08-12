package clients

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceLaunchStageBindingAlwaysSerializesExpectedResourceBinding(t *testing.T) {
	encoded, err := json.Marshal(WorkspaceLaunchStageInput{Binding: WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-1", AccountID: "acct-1", WorkspaceID: "ws-1",
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "fabric-op-1",
		IdempotencyKey: "launch-1:ensure-compute-allocation", RequestHash: "stage-request",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"expectedResourceBinding":""`)) {
		t.Fatalf("expected resource binding omitted: %s", encoded)
	}
}

func TestWorkspaceLaunchStageInputDoesNotProjectContinuationAuthority(t *testing.T) {
	encoded, err := json.Marshal(WorkspaceLaunchStageInput{Binding: WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-1", AccountID: "acct-1", WorkspaceID: "ws-1",
		Stage: "storage", Action: "ensure_storage", FabricOperationID: "fabric-op-1",
		IdempotencyKey: "launch-1:storage", RequestHash: "stage-request",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	if _, found := body["resumeAuthorizationDigest"]; found {
		t.Fatalf("Fabric request contains CP authorization digest: %s", encoded)
	}
	if _, found := body["mutationBudget"]; found {
		t.Fatalf("Fabric request contains CP mutation budget: %s", encoded)
	}
}

func TestFabricWorkspaceLaunchHTTPClientUsesTypedRoutesAndIdentity(t *testing.T) {
	const capabilityKey = "test-capability-key"
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer fabric-token" {
			t.Fatalf("Authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/fabric/workspace-launches/preflight":
			var input WorkspaceLaunchPreflightInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			_ = json.NewEncoder(w).Encode(WorkspaceLaunchPreflight{SchemaVersion: 1, Available: true, Reason: "none", LaunchOperationID: input.LaunchOperationID, RequestHash: input.RequestHash, ProviderProfileRef: "profile", BindingRef: "binding"})
		case "/fabric/workspace-launches/stages/read", "/fabric/workspace-launches/stages/ensure":
			var input WorkspaceLaunchStageInput
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.Binding.FabricOperationID != "launch-1:storage" || input.Binding.LaunchOperationID != "launch-1" || input.Binding.AccountID != "acct-1" || input.Binding.WorkspaceID != "ws-1" ||
				input.Binding.Stage != "storage" || input.Binding.Action != "ensure_storage" || input.Binding.RequestHash != "stage-request" || input.Binding.ExpectedResourceBinding != "binding-storage" {
				t.Fatalf("incomplete stage binding=%#v", input.Binding)
			}
			if r.URL.Path == "/fabric/workspace-launches/stages/ensure" && r.Header.Get("Idempotency-Key") != input.Binding.IdempotencyKey {
				t.Fatalf("Idempotency-Key=%q", r.Header.Get("Idempotency-Key"))
			}
			if r.URL.Path == "/fabric/workspace-launches/stages/ensure" {
				parts := strings.Split(r.Header.Get(FabricCapabilityHeader), ".")
				if len(parts) != 2 {
					t.Fatalf("missing ensure capability")
				}
				mac := hmac.New(sha256.New, []byte(capabilityKey))
				_, _ = mac.Write([]byte(parts[0]))
				signature, err := base64.RawURLEncoding.DecodeString(parts[1])
				if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
					t.Fatalf("ensure capability integrity invalid: %v", err)
				}
			}
			_ = json.NewEncoder(w).Encode(WorkspaceLaunchStageResult{SchemaVersion: 1, State: "pending", Reason: "none", Binding: input.Binding, Resources: input.Resources})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewFabricHTTPClientWithCapability(server.URL, "fabric-token", capabilityKey, server.Client()).(FabricWorkspaceLaunchClient)
	preflightInput := WorkspaceLaunchPreflightInput{SchemaVersion: 1, LaunchOperationID: "launch-1", AccountID: "acct-1", WorkspaceID: "ws-1", PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: "repo@sha256:digest", RequestHash: "request"}
	if _, err := client.PreflightWorkspaceLaunch(context.Background(), preflightInput); err != nil {
		t.Fatal(err)
	}
	stageInput := WorkspaceLaunchStageInput{Binding: WorkspaceLaunchStageBinding{SchemaVersion: 1, LaunchOperationID: "launch-1", AccountID: "acct-1", WorkspaceID: "ws-1", Stage: "storage", Action: "ensure_storage", FabricOperationID: "launch-1:storage", IdempotencyKey: "launch-1:storage", RequestHash: "stage-request", ExpectedResourceBinding: "binding-storage"}}
	if _, err := client.ReadWorkspaceLaunchStage(context.Background(), stageInput); err != nil {
		t.Fatal(err)
	}
	if _, err := client.EnsureWorkspaceLaunchStage(context.Background(), stageInput); err != nil {
		t.Fatal(err)
	}
	want := []string{"/fabric/workspace-launches/preflight", "/fabric/workspace-launches/stages/read", "/fabric/workspace-launches/stages/ensure"}
	if len(paths) != len(want) {
		t.Fatalf("paths=%#v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths=%#v", paths)
		}
	}
}

func TestFabricWorkspaceLaunchHTTPClientReturnsTypedReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fabric/workspace-launches/stages/read" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"fabric_unavailable"}`))
	}))
	defer server.Close()

	client := newFabricHTTPClientForTest(server.URL, "fabric-token", server.Client()).(FabricWorkspaceLaunchClient)
	_, err := client.ReadWorkspaceLaunchStage(context.Background(), WorkspaceLaunchStageInput{})
	var upstream *FabricHTTPError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusServiceUnavailable || upstream.Body != `{"error":"fabric_unavailable"}` {
		t.Fatalf("typed Fabric error=%#v err=%v", upstream, err)
	}
}
