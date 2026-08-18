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
	"os"
	"path/filepath"
	"runtime"
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

func TestFabricLaunchBindingContractCarriesOpaqueProviderBinding(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	contractPath := filepath.Join(filepath.Dir(sourceFile), "../../../../packages/contracts/opl-cloud-fabric-launch-binding-contract.json")
	data, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read launch binding contract: %v", err)
	}
	var contract map[string]any
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode launch binding contract: %v", err)
	}
	if got := int(contract["schemaVersion"].(float64)); got != 3 {
		t.Fatalf("launch binding contract schemaVersion=%d, want 3", got)
	}

	preflight := contract["preflight"].(map[string]any)
	responseIdentity := stringSet(preflight["responseIdentityFields"])
	for _, field := range []string{"providerProfileRef", "providerBindingRef", "specDigest"} {
		if !responseIdentity[field] {
			t.Fatalf("preflight response identity misses %q", field)
		}
	}
	if responseIdentity["bindingRef"] {
		t.Fatal("preflight contract retains ambiguous bindingRef alias")
	}
	for _, field := range []string{"canonicalProviderPlan", "providerPlan", "cpu", "memoryGb", "diskGb", "instanceType", "nodePoolId", "zone", "diskType", "chargeType", "renewFlag"} {
		if !stringSet(preflight["responseForbiddenFields"])[field] {
			t.Fatalf("preflight response does not forbid provider field %q", field)
		}
	}

	stageInput := contract["stageInput"].(map[string]any)
	stageFields := stringSet(stageInput["fields"])
	for _, field := range []string{"providerProfileRef", "providerBindingRef", "specDigest"} {
		if !stageFields[field] {
			t.Fatalf("stage input misses %q", field)
		}
	}
	for _, field := range []string{"canonicalProviderPlan", "providerPlan", "cpu", "memoryGb", "diskGb", "instanceType", "nodePoolId", "zone", "diskType", "chargeType", "renewFlag"} {
		if !stringSet(stageInput["forbiddenFields"])[field] {
			t.Fatalf("stage input does not forbid provider field %q", field)
		}
	}

	binding := contract["providerBinding"].(map[string]any)
	if binding["identityField"] != "providerBindingRef" || binding["digestField"] != "specDigest" {
		t.Fatalf("provider binding identity=%#v digest=%#v", binding["identityField"], binding["digestField"])
	}
	if !strings.Contains(binding["canonicalProviderPlan"].(string), "never submitted by Control Plane or Console") {
		t.Fatalf("canonical plan boundary=%q", binding["canonicalProviderPlan"])
	}
	digest := binding["digest"].(map[string]any)
	if digest["algorithm"] != "sha256" || digest["encoding"] != "lowercase_hex" {
		t.Fatalf("provider digest=%#v", digest)
	}
	canonical := binding["goldenVectors"].([]any)[0].(map[string]any)
	canonicalJSON := canonical["canonicalJson"].(string)
	hash := sha256.Sum256([]byte(canonicalJSON))
	if got, want := hexEncode(hash[:]), canonical["specDigest"].(string); got != want {
		t.Fatalf("provider specDigest=%s, want %s", got, want)
	}

	excluded := stringSet(contract["stageRequestHash"].(map[string]any)["excludedStageInputFields"])
	for _, field := range []string{"providerProfileRef", "providerBindingRef", "specDigest", "gatewayCredential"} {
		if !excluded[field] {
			t.Fatalf("stage request hash must exclude %q", field)
		}
	}
}

func stringSet(value any) map[string]bool {
	set := map[string]bool{}
	for _, item := range value.([]any) {
		if field, ok := item.(string); ok {
			set[field] = true
		}
	}
	return set
}

func hexEncode(value []byte) string {
	const hex = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for i, b := range value {
		encoded[i*2], encoded[i*2+1] = hex[b>>4], hex[b&0x0f]
	}
	return string(encoded)
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
			if input.Binding.FabricOperationID != "launch-1:ensure_compute_allocation" || input.Binding.LaunchOperationID != "launch-1" || input.Binding.AccountID != "acct-1" || input.Binding.WorkspaceID != "ws-1" ||
				input.Binding.Stage != "ensure_compute_allocation" || input.Binding.Action != "ensure_compute_allocation" || input.Binding.RequestHash != "stage-request" || input.Binding.ExpectedResourceBinding != "" {
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
				payload, err := base64.RawURLEncoding.DecodeString(parts[0])
				if err != nil {
					t.Fatal(err)
				}
				var claims fabricCapabilityClaims
				if err := json.Unmarshal(payload, &claims); err != nil || claims.ResourceKind != "workspace_launch_stage" || claims.ResourceID != input.Binding.FabricOperationID {
					t.Fatalf("ensure capability owner identity=%#v err=%v", claims, err)
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
	stageInput := WorkspaceLaunchStageInput{Binding: WorkspaceLaunchStageBinding{SchemaVersion: 1, LaunchOperationID: "launch-1", AccountID: "acct-1", WorkspaceID: "ws-1", Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-1:ensure_compute_allocation", IdempotencyKey: "launch-1:ensure_compute_allocation", RequestHash: "stage-request"}}
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
