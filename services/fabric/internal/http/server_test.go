package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opl-cloud/services/fabric/internal/fabric"
)

func TestMain(m *testing.M) {
	for key, value := range map[string]string{
		"OPL_BASIC_COMPUTE_INSTANCE_TYPE": "SA5.MEDIUM4",
		"OPL_PRO_COMPUTE_INSTANCE_TYPE":   "SA5.2XLARGE16",
	} {
		_ = os.Setenv(key, value)
	}
	os.Exit(m.Run())
}

type runtimeHealthSummaryHTTPProvider struct {
	testProvider
	calls int
}

type workspaceOwnerObservationHTTPProvider struct {
	testProvider
	runtime       fabric.WorkspaceRuntime
	runtimeErr    error
	secretBinding fabric.WorkspaceRuntimeGatewaySecretBinding
	secretErr     error
}

func (p workspaceOwnerObservationHTTPProvider) WorkspaceRuntimeStatus(_ context.Context, _ string) (fabric.WorkspaceRuntime, error) {
	return p.runtime, p.runtimeErr
}

func (p workspaceOwnerObservationHTTPProvider) WorkspaceRuntimeGatewaySecret(_ context.Context, _ string) (fabric.WorkspaceRuntimeGatewaySecretBinding, error) {
	return p.secretBinding, p.secretErr
}

func (p workspaceOwnerObservationHTTPProvider) BindWorkspaceRuntimeGatewaySecret(_ context.Context, input fabric.WorkspaceRuntimeGatewaySecretInput) (fabric.WorkspaceRuntimeGatewaySecretBinding, error) {
	return fabric.WorkspaceRuntimeGatewaySecretBinding{WorkspaceID: input.WorkspaceID, WorkspaceAPIKeyID: input.WorkspaceAPIKeyID, SecretRef: input.SecretRef, Fingerprint: input.Fingerprint, Bound: true}, nil
}

const testFabricCapabilityKey = "test-fabric-capability-key-not-a-transport-token"

func newTestServer(service *fabric.Service, token string) http.Handler {
	want := sha256.Sum256([]byte("Bearer " + token))
	next := newFabricMux(service)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if token == "" || !hmac.Equal(got[:], want[:]) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type fabricCapabilityClaimsForTest struct {
	Version      int    `json:"version"`
	Caller       string `json:"caller"`
	AccountID    string `json:"accountId"`
	WorkspaceID  string `json:"workspaceId"`
	ResourceKind string `json:"resourceKind"`
	ResourceID   string `json:"resourceId"`
	Action       string `json:"action"`
	OperationID  string `json:"operationId"`
	ExpiresAt    int64  `json:"expiresAt"`
	BodySHA256   string `json:"bodySha256"`
}

func fabricCapabilityForTest(t *testing.T, claims fabricCapabilityClaimsForTest, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(body)
	claims.BodySHA256 = hex.EncodeToString(digest[:])
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(testFabricCapabilityKey))
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type capabilityBoundaryProvider struct {
	testProvider
	computeCreates atomic.Int32
	runtimeRepairs atomic.Int32
}

type staticFabricMutationScopeResolver struct {
	authorization fabric.ComputePoolHeadTerminalizationAuthorization
	err           error
}

func (r staticFabricMutationScopeResolver) ComputePoolHeadTerminalizationAuthorization(context.Context, fabric.ComputePoolHeadTerminalizationInput) (fabric.ComputePoolHeadTerminalizationAuthorization, error) {
	return r.authorization, r.err
}

func (p *capabilityBoundaryProvider) CreateComputeAllocation(ctx context.Context, input fabric.ComputeAllocationExecution) (fabric.ComputeAllocation, error) {
	p.computeCreates.Add(1)
	return p.testProvider.CreateComputeAllocation(ctx, input)
}

func (p *capabilityBoundaryProvider) RepairWorkspaceRuntime(_ context.Context, input fabric.WorkspaceRuntimeInput, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.WorkspaceRuntime, error) {
	p.runtimeRepairs.Add(1)
	return fabric.WorkspaceRuntime{WorkspaceID: input.WorkspaceID, OperationID: input.RuntimeOperationID, ImageID: input.ImageID, Status: "running", Ready: true}, nil
}

func TestFabricMutationAuthorizationRejectsBeforeOperationOrProviderSideEffects(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		claims     fabricCapabilityClaimsForTest
		capability bool
		signedBody string
		corrupt    bool
	}{
		{
			name: "missing authorization",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
		},
		{
			name: "account and workspace mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
		},
		{
			name: "expired authorization",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(-time.Second).Unix(),
			},
			capability: true,
		},
		{
			name: "caller mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "runner", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
		},
		{
			name: "resource mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-alpha", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
		},
		{
			name: "action mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "destroy_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
		},
		{
			name: "operation mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-alpha",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
		},
		{
			name: "body mismatch",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
			signedBody: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"pro","nodePoolId":"np-basic"}`,
		},
		{
			name: "invalid integrity",
			body: `{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`,
			claims: fabricCapabilityClaimsForTest{
				Version: 1, Caller: "control-plane", AccountID: "acct-beta", WorkspaceID: "ws-beta",
				ResourceKind: "compute_allocation", ResourceID: "compute-beta", Action: "create_compute_allocation", OperationID: "operation-beta",
				ExpiresAt: time.Now().Add(time.Minute).Unix(),
			},
			capability: true,
			corrupt:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &capabilityBoundaryProvider{}
			store := fabric.NewMemoryOperationStore()
			server := NewServerWithAuth(fabric.NewServiceWithOperationStore(provider, store), ServerAuthConfig{
				ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
			})
			body := []byte(tt.body)
			req := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
			req.Header.Set("Idempotency-Key", "operation-beta")
			if tt.capability {
				signedBody := body
				if tt.signedBody != "" {
					signedBody = []byte(tt.signedBody)
				}
				capability := fabricCapabilityForTest(t, tt.claims, signedBody)
				if tt.corrupt {
					capability += "x"
				}
				req.Header.Set("X-OPL-Fabric-Capability", capability)
			}
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 0 || provider.computeCreates.Load() != 0 {
				t.Fatalf("rejected request changed Fabric state: operations=%#v providerCalls=%d err=%v", operations, provider.computeCreates.Load(), err)
			}
		})
	}
}

func TestFabricMutationAuthorizationAcceptsMatchingControlPlaneRequest(t *testing.T) {
	provider := &capabilityBoundaryProvider{}
	store := fabric.NewMemoryOperationStore()
	server := NewServerWithAuth(fabric.NewServiceWithOperationStore(provider, store), ServerAuthConfig{
		ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
	})
	body := []byte(`{"id":"compute-alpha","accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","nodePoolId":"np-basic"}`)
	claims := fabricCapabilityClaimsForTest{
		Version: 1, Caller: "control-plane", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		ResourceKind: "compute_allocation", ResourceID: "compute-alpha", Action: "create_compute_allocation", OperationID: "operation-alpha",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "operation-alpha")
	req.Header.Set("X-OPL-Fabric-Capability", fabricCapabilityForTest(t, claims, body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].AccountID != "acct-alpha" || operations[0].WorkspaceID != "ws-alpha" {
		t.Fatalf("legal request operation=%#v err=%v", operations, err)
	}
}

func TestWorkspaceRuntimeRepairCapabilityRejectsMismatchedScope(t *testing.T) {
	body := []byte(`{"accountId":"acct-alpha","workspaceId":"workspace-alpha","computeId":"compute-alpha","volumeId":"storage-alpha","attachmentId":"attachment-alpha","attachmentOperationId":"attachment-operation-alpha","runtimeOperationId":"runtime-repair-alpha","previousRuntimeOperationId":"runtime-original-alpha","imageId":"ghcr.io/gaofeng21cn/one-person-lab-app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","gatewaySecretRef":"secret-alpha"}`)
	matching := fabricCapabilityClaimsForTest{
		Version: 1, Caller: "control-plane", AccountID: "acct-alpha", WorkspaceID: "workspace-alpha",
		ResourceKind: "workspace_runtime", ResourceID: "workspace-alpha", Action: "repair_workspace_runtime", OperationID: "runtime-repair-once",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	tests := []struct {
		name   string
		claims fabricCapabilityClaimsForTest
	}{
		{name: "account", claims: func() fabricCapabilityClaimsForTest { value := matching; value.AccountID = "acct-other"; return value }()},
		{name: "workspace", claims: func() fabricCapabilityClaimsForTest {
			value := matching
			value.WorkspaceID = "workspace-other"
			return value
		}()},
		{name: "resource kind", claims: func() fabricCapabilityClaimsForTest {
			value := matching
			value.ResourceKind = "storage_volume"
			return value
		}()},
		{name: "resource id", claims: func() fabricCapabilityClaimsForTest {
			value := matching
			value.ResourceID = "workspace-other"
			return value
		}()},
		{name: "action", claims: func() fabricCapabilityClaimsForTest {
			value := matching
			value.Action = "create_workspace_runtime"
			return value
		}()},
		{name: "operation", claims: func() fabricCapabilityClaimsForTest {
			value := matching
			value.OperationID = "runtime-repair-other"
			return value
		}()},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &capabilityBoundaryProvider{}
			store := fabric.NewMemoryOperationStore()
			server := NewServerWithAuth(fabric.NewServiceWithOperationStore(provider, store), ServerAuthConfig{
				ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
			})
			request := testRequest(http.MethodPost, "/fabric/workspace-runtimes/workspace-alpha/repair", bytes.NewReader(body))
			request.Header.Set("Idempotency-Key", "runtime-repair-once")
			request.Header.Set(fabricCapabilityHeader, fabricCapabilityForTest(t, test.claims, body))
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 0 || provider.runtimeRepairs.Load() != 0 {
				t.Fatalf("rejected repair changed Fabric state: operations=%#v providerCalls=%d err=%v", operations, provider.runtimeRepairs.Load(), err)
			}
		})
	}
}

func TestFabricMutationAuthorizationDerivesRuntimeUpdateAction(t *testing.T) {
	body := []byte(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":"compute-alpha","volumeId":"storage-alpha","attachmentId":"attachment-alpha","attachmentOperationId":"attach-alpha","runtimeOperationId":"runtime-original","imageId":"one-person-lab-app","gatewaySecretRef":"secret-alpha"}`)
	req := httptest.NewRequest(http.MethodPost, "/fabric/workspace-runtimes", nil)
	req.Header.Set("Idempotency-Key", "runtime-credential-rotate:ws-alpha:once:runtime")

	scope, ok := fabricMutationScopeForRequest(context.Background(), nil, req, body)

	if !ok || scope.Action != "update_workspace_runtime" || scope.OperationID != "runtime-credential-rotate:ws-alpha:once:runtime" {
		t.Fatalf("runtime update scope=%#v ok=%v", scope, ok)
	}
}

func TestFabricProviderMutationScopesAreStrict(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		body         string
		operationID  string
		resourceKind string
		resourceID   string
		action       string
		wantOK       bool
	}{
		{
			name: "attachment create", path: "/fabric/storage-attachments",
			body:        `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":"compute-alpha","volumeId":"volume-alpha"}`,
			operationID: "attachment-once", resourceKind: "storage_attachment", resourceID: "compute-alpha:volume-alpha", action: "create_storage_attachment", wantOK: true,
		},
		{
			name: "runtime credential reveal", path: "/fabric/workspace-runtimes/ws-alpha/credentials/reveal",
			body:        `{"accountId":"acct-alpha","workspaceId":"ws-alpha"}`,
			operationID: "reveal-once", resourceKind: "workspace_runtime_credential", resourceID: "ws-alpha", action: "reveal_workspace_runtime_credential", wantOK: true,
		},
		{
			name: "compute pool head terminalization", path: "/fabric/compute-pool-head/terminalization",
			body:        `{"nodePoolId":"np-basic","approvalId":"terminalize-operation","approvalDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			operationID: "terminalize-operation", resourceKind: "compute_pool_head", resourceID: "np-basic", action: "terminalize_compute_pool_head", wantOK: true,
		},
		{
			name: "terminalization approval mismatch", path: "/fabric/compute-pool-head/terminalization",
			body:        `{"nodePoolId":"np-basic","approvalId":"another-operation","approvalDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`,
			operationID: "terminalize-operation",
		},
		{
			name: "attachment missing compute", path: "/fabric/storage-attachments",
			body:        `{"accountId":"acct-alpha","workspaceId":"ws-alpha","volumeId":"volume-alpha"}`,
			operationID: "attachment-once",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("Idempotency-Key", tt.operationID)
			resolver := fabricMutationScopeResolver(nil)
			if tt.path == "/fabric/compute-pool-head/terminalization" {
				resolver = staticFabricMutationScopeResolver{authorization: fabric.ComputePoolHeadTerminalizationAuthorization{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", NodePoolID: "np-basic"}}
			}
			scope, ok := fabricMutationScopeForRequest(context.Background(), resolver, req, []byte(tt.body))
			if ok != tt.wantOK {
				t.Fatalf("scope=%#v ok=%v", scope, ok)
			}
			if !tt.wantOK {
				return
			}
			wantCaller := "control-plane"
			if tt.path == "/fabric/compute-pool-head/terminalization" {
				wantCaller = "operator"
			}
			if scope.Caller != wantCaller || scope.AccountID != "acct-alpha" || scope.WorkspaceID == "" || scope.ResourceKind != tt.resourceKind || scope.ResourceID != tt.resourceID || scope.Action != tt.action || scope.OperationID != tt.operationID {
				t.Fatalf("scope=%#v", scope)
			}
		})
	}
	if !isFabricMutation(httptest.NewRequest(http.MethodPost, "/fabric/workspace-runtimes/ws-alpha/credentials/reveal", nil)) {
		t.Fatal("runtime credential reveal must require capability authorization")
	}
}

func TestFabricProviderMutationsRequireCapabilityBeforeStateChange(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "attachment create", path: "/fabric/storage-attachments", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":"compute-alpha","volumeId":"volume-alpha"}`},
		{name: "runtime credential reveal", path: "/fabric/workspace-runtimes/ws-alpha/credentials/reveal", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha"}`},
		{name: "compute pool head terminalization", path: "/fabric/compute-pool-head/terminalization", body: `{"nodePoolId":"np-basic","approvalId":"operation-alpha","approvalDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := fabric.NewMemoryOperationStore()
			server := NewServerWithAuth(fabric.NewServiceWithOperationStore(testProvider{}, store), ServerAuthConfig{
				ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
			})
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer internal-secret")
			req.Header.Set("Idempotency-Key", "operation-alpha")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			operations, err := store.List(context.Background())
			if err != nil || len(operations) != 0 {
				t.Fatalf("rejected mutation changed Fabric state: operations=%#v err=%v", operations, err)
			}
		})
	}
}

func TestComputePoolHeadTerminalizationRequiresPersistedOwnerCapability(t *testing.T) {
	body := []byte(`{"nodePoolId":"np-basic","approvalId":"terminalize-operation","approvalDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	resolver := staticFabricMutationScopeResolver{authorization: fabric.ComputePoolHeadTerminalizationAuthorization{
		AccountID: "acct-alpha", WorkspaceID: "ws-alpha", NodePoolID: "np-basic",
	}}
	matching := fabricCapabilityClaimsForTest{
		Version: 1, Caller: "operator", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		ResourceKind: "compute_pool_head", ResourceID: "np-basic", Action: "terminalize_compute_pool_head", OperationID: "terminalize-operation",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	for _, test := range []struct {
		name       string
		claims     fabricCapabilityClaimsForTest
		capability bool
		want       int
	}{
		{name: "transport token only", want: http.StatusForbidden},
		{name: "control plane caller", claims: func() fabricCapabilityClaimsForTest { value := matching; value.Caller = "control-plane"; return value }(), capability: true, want: http.StatusForbidden},
		{name: "other tenant", claims: func() fabricCapabilityClaimsForTest { value := matching; value.AccountID = "acct-other"; return value }(), capability: true, want: http.StatusForbidden},
		{name: "persisted owner", claims: matching, capability: true, want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			server := authorizeFabricRequests(next, resolver, ServerAuthConfig{
				ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey, Now: time.Now,
			})
			req := httptest.NewRequest(http.MethodPost, "/fabric/compute-pool-head/terminalization", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer internal-secret")
			req.Header.Set("Idempotency-Key", "terminalize-operation")
			if test.capability {
				req.Header.Set(fabricCapabilityHeader, fabricCapabilityForTest(t, test.claims, body))
			}
			recorder := httptest.NewRecorder()

			server.ServeHTTP(recorder, req)

			if recorder.Code != test.want || called != (test.want == http.StatusNoContent) {
				t.Fatalf("status=%d want=%d called=%v body=%s", recorder.Code, test.want, called, recorder.Body.String())
			}
		})
	}
}

func TestFabricProviderMutationCapabilitiesAcceptExactBodies(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		body   string
		claims fabricCapabilityClaimsForTest
	}{
		{
			name: "attachment create", path: "/fabric/storage-attachments",
			body:   `{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":"compute-alpha","volumeId":"volume-alpha"}`,
			claims: fabricCapabilityClaimsForTest{AccountID: "acct-alpha", WorkspaceID: "ws-alpha", ResourceKind: "storage_attachment", ResourceID: "compute-alpha:volume-alpha", Action: "create_storage_attachment"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})
			server := authorizeFabricRequests(next, nil, ServerAuthConfig{
				ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey, Now: time.Now,
			})
			body := []byte(test.body)
			test.claims.Version = 1
			test.claims.Caller = "control-plane"
			test.claims.OperationID = "operation-alpha"
			test.claims.ExpiresAt = time.Now().Add(time.Minute).Unix()
			req := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer internal-secret")
			req.Header.Set("Idempotency-Key", "operation-alpha")
			req.Header.Set(fabricCapabilityHeader, fabricCapabilityForTest(t, test.claims, body))
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent || !called {
				t.Fatalf("status=%d body=%s called=%v", rec.Code, rec.Body.String(), called)
			}
		})
	}
}

func TestFabricProviderMutationSourceIdentityIsBoundBeforeOperation(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := newTestServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "identity-compute")
	volumeBody := fmt.Sprintf(`{"id":"identity-volume","accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)
	createVolume := testRequest(http.MethodPost, "/fabric/storage-volumes", strings.NewReader(volumeBody))
	createVolume.Header.Set("Idempotency-Key", "identity-volume-once")
	volumeRec := httptest.NewRecorder()
	server.ServeHTTP(volumeRec, createVolume)
	if volumeRec.Code != http.StatusAccepted {
		t.Fatalf("volume status=%d body=%s", volumeRec.Code, volumeRec.Body.String())
	}

	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "attachment account", path: "/fabric/storage-attachments", body: `{"accountId":"acct-other","workspaceId":"ws-alpha","computeId":"` + compute.ID + `","volumeId":"identity-volume"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := testRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Idempotency-Key", "identity-reject-once")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFabricStrictAuthorizationPreservesTransportAuthenticationError(t *testing.T) {
	server := NewServerWithAuth(fabric.NewService(testProvider{}), ServerAuthConfig{
		ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
	})
	req := httptest.NewRequest(http.MethodPost, "/fabric/compute-allocations", strings.NewReader(`{"id":"compute-alpha","accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic"}`))
	req.Header.Set("Idempotency-Key", "operation-alpha")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFabricRunnerAuthorizationIsLimitedToJobLeaseRoutes(t *testing.T) {
	provider := &capabilityBoundaryProvider{}
	store := fabric.NewMemoryOperationStore()
	service := fabric.NewServiceWithOperationStore(provider, store)
	job, err := service.CreateJob(context.Background(), fabric.JobInput{
		OrganizationID: "org-alpha", WorkspaceID: "ws-alpha", ProjectID: "project-alpha", TaskID: "task-alpha",
		RequestID: "request-alpha", ApprovalID: "approval-alpha", IdempotencyKey: "job-alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithAuth(service, ServerAuthConfig{
		ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
	})

	claim := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", strings.NewReader(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Authorization", "Bearer runner-secret")
	claim.Header.Set("Idempotency-Key", "claim-alpha")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	if claimRec.Code != http.StatusAccepted {
		t.Fatalf("runner claim status=%d body=%s", claimRec.Code, claimRec.Body.String())
	}
	var claimed fabric.Job
	if err := json.NewDecoder(claimRec.Body).Decode(&claimed); err != nil || claimed.LeaseToken == "" {
		t.Fatalf("runner claim result=%#v err=%v", claimed, err)
	}
	get := httptest.NewRequest(http.MethodGet, "/fabric/jobs/"+job.JobID, nil)
	get.Header.Set("Authorization", "Bearer runner-secret")
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("runner job read status=%d body=%s", getRec.Code, getRec.Body.String())
	}
	heartbeat := httptest.NewRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", strings.NewReader(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`"}`))
	heartbeat.Header.Set("Authorization", "Bearer runner-secret")
	heartbeat.Header.Set("Idempotency-Key", "heartbeat-alpha")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeat)
	if heartbeatRec.Code != http.StatusAccepted {
		t.Fatalf("runner heartbeat status=%d body=%s", heartbeatRec.Code, heartbeatRec.Body.String())
	}
	baselineOperations, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	body := []byte(`{"id":"compute-beta","accountId":"acct-beta","workspaceId":"ws-beta","packageId":"basic","nodePoolId":"np-basic"}`)
	mutation := httptest.NewRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
	mutation.Header.Set("Authorization", "Bearer runner-secret")
	mutation.Header.Set("Idempotency-Key", "operation-beta")
	mutationRec := httptest.NewRecorder()
	server.ServeHTTP(mutationRec, mutation)
	if mutationRec.Code != http.StatusForbidden {
		t.Fatalf("runner mutation status=%d body=%s", mutationRec.Code, mutationRec.Body.String())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != len(baselineOperations) || provider.computeCreates.Load() != 0 {
		t.Fatalf("runner mutation changed Fabric state: operations=%#v providerCalls=%d err=%v", operations, provider.computeCreates.Load(), err)
	}
	for _, path := range []string{"/fabric/jobs", "/fabric/jobs/" + job.JobID + "/cancel", "/fabric/jobs/" + job.JobID + "/retry"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer runner-secret")
		req.Header.Set("Idempotency-Key", "runner-forbidden")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("runner route %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func (p *runtimeHealthSummaryHTTPProvider) RuntimeHealthSummary(context.Context) (fabric.RuntimeHealthSummary, error) {
	p.calls++
	return fabric.RuntimeHealthSummary{Total: 1000, Ready: 999, Unready: 1}, nil
}

func TestServerAuthenticatesEverythingExceptGetHealthz(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	tests := []struct {
		name          string
		method        string
		path          string
		authorization string
		want          int
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{name: "health wrong method", method: http.MethodPost, path: "/healthz", want: http.StatusUnauthorized},
		{name: "readiness anonymous", method: http.MethodGet, path: "/fabric/readiness", want: http.StatusUnauthorized},
		{name: "unknown anonymous", method: http.MethodGet, path: "/missing", want: http.StatusUnauthorized},
		{name: "wrong scheme", method: http.MethodGet, path: "/fabric/catalog", authorization: "Basic internal-secret", want: http.StatusUnauthorized},
		{name: "wrong token", method: http.MethodGet, path: "/fabric/catalog", authorization: "Bearer wrong", want: http.StatusUnauthorized},
		{name: "authenticated", method: http.MethodGet, path: "/fabric/catalog", authorization: "Bearer internal-secret", want: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", tt.authorization)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

type readinessHTTPProvider struct {
	testProvider
	result map[string]any
	err    error
}

func (p readinessHTTPProvider) Readiness(context.Context) (map[string]any, error) {
	return p.result, p.err
}

func TestServerReadinessPreservesPublicResponseContract(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		want := map[string]any{"provider": "test", "ready": true, "status": "ready"}
		server := newTestServer(fabric.NewService(readinessHTTPProvider{result: want}), "internal-secret")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/readiness", nil))
		var got map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || recorder.Code != http.StatusOK || !reflect.DeepEqual(got, want) {
			t.Fatalf("status=%d readiness=%#v err=%v body=%s", recorder.Code, got, err, recorder.Body.String())
		}
	})

	t.Run("provider error", func(t *testing.T) {
		server := newTestServer(fabric.NewService(readinessHTTPProvider{err: errors.New("provider readiness failed")}), "internal-secret")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/readiness", nil))
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), `"error":"provider readiness failed"`) {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestWorkspaceLaunchTypedEnsureRequiresExactHeaderAndReturnsNeutralDTO(t *testing.T) {
	service := fabric.NewServiceWithOperationStore(workspaceLaunchHTTPProvider{}, fabric.NewMemoryOperationStore())
	imageDigest := "ghcr.io/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	launchRequestHash := strings.Repeat("b", 64)
	preflight, err := service.PreflightWorkspaceLaunch(context.Background(), fabric.WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageDigest, RequestHash: launchRequestHash,
	})
	if err != nil || !preflight.Available {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	binding := fabric.WorkspaceLaunchStageBinding{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-alpha:ensure_compute_allocation",
		IdempotencyKey: "launch-alpha:ensure_compute_allocation",
	}
	input := fabric.WorkspaceLaunchStageInput{
		Binding: binding, ProviderProfileRef: "tencent-tke", ProviderBindingRef: preflight.ProviderBindingRef, SpecDigest: preflight.SpecDigest,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageDigest,
	}
	input.Binding.RequestHash = "ddb1c0c5195c4e04c1d23230a493da582a2ca56af528a7abcf67d781f81c3fe1"
	binding = input.Binding
	server := newTestServer(service, "internal-secret")
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	missingHeader := httptest.NewRecorder()
	server.ServeHTTP(missingHeader, testRequest(http.MethodPost, "/fabric/workspace-launches/stages/ensure", bytes.NewReader(body)))
	if missingHeader.Code != http.StatusBadRequest {
		t.Fatalf("missing header status=%d body=%s", missingHeader.Code, missingHeader.Body.String())
	}

	request := testRequest(http.MethodPost, "/fabric/workspace-launches/stages/ensure", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", binding.IdempotencyKey)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ensure status=%d body=%s", response.Code, response.Body.String())
	}
	var result fabric.WorkspaceLaunchStageResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.State != "ready" || result.Binding != binding || result.Resources.ComputeBindingRef != binding.FabricOperationID {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, forbidden := range []string{"machineName", "nodeName", "cvmInstanceId", "cvmStatus", "nodePoolId", "cbsStatus", "tencentMutationCount", "kubernetesMutationCount", "providerData", "costTags", "tencent-tke"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("typed response leaked legacy provider fact %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestWorkspaceLaunchEnsureCapabilityUsesFabricOperationOwnerIdentity(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	service := fabric.NewServiceWithOperationStore(workspaceLaunchHTTPProvider{}, store)
	imageDigest := "ghcr.io/gaofeng21cn/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	launchRequestHash := strings.Repeat("b", 64)
	preflight, err := service.PreflightWorkspaceLaunch(context.Background(), fabric.WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageDigest, RequestHash: launchRequestHash,
	})
	if err != nil || !preflight.Available {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	input := fabric.WorkspaceLaunchStageInput{
		Binding: fabric.WorkspaceLaunchStageBinding{
			SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			Stage: "ensure_compute_allocation", Action: "ensure_compute_allocation", FabricOperationID: "launch-alpha:ensure_compute_allocation",
			IdempotencyKey: "launch-alpha:ensure_compute_allocation",
			RequestHash:    "ddb1c0c5195c4e04c1d23230a493da582a2ca56af528a7abcf67d781f81c3fe1",
		},
		ProviderProfileRef: "tencent-tke", ProviderBindingRef: preflight.ProviderBindingRef, SpecDigest: preflight.SpecDigest,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: imageDigest,
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithAuth(service, ServerAuthConfig{
		ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
	})
	claims := fabricCapabilityClaimsForTest{
		Version: 1, Caller: "control-plane", AccountID: input.Binding.AccountID, WorkspaceID: input.Binding.WorkspaceID,
		ResourceKind: "workspace_launch_stage", ResourceID: input.Binding.FabricOperationID, Action: input.Binding.Action,
		OperationID: input.Binding.IdempotencyKey, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	request := testRequest(http.MethodPost, "/fabric/workspace-launches/stages/ensure", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", input.Binding.IdempotencyKey)
	request.Header.Set("X-OPL-Fabric-Capability", fabricCapabilityForTest(t, claims, body))
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		operations, listErr := store.List(context.Background())
		t.Fatalf("ensure status=%d body=%s ownerRecords=%d listErr=%v", response.Code, response.Body.String(), len(operations), listErr)
	}
	operations, err := store.List(context.Background())
	var stageOperations []fabric.FabricOperation
	for _, operation := range operations {
		if operation.ID == input.Binding.FabricOperationID {
			stageOperations = append(stageOperations, operation)
		}
	}
	if err != nil || len(stageOperations) != 1 || stageOperations[0].ResourceID != input.Binding.FabricOperationID {
		t.Fatalf("owner records=%#v err=%v", operations, err)
	}
}

type workspaceLaunchHTTPProvider struct {
	testProvider
}

func (workspaceLaunchHTTPProvider) ValidateWorkspaceImageReference(value string) bool {
	const prefix = "ghcr.io/gaofeng21cn/one-person-lab-app@sha256:"
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+64
}

func (testProvider) EnsureWorkspaceLaunchStage(_ context.Context, request fabric.WorkspaceLaunchProviderRequest) (fabric.WorkspaceLaunchProviderResult, error) {
	resources := request.Input.Resources
	resources.ComputeAllocationID = "ca-http-alpha"
	resources.ComputeBindingRef = request.Input.Binding.FabricOperationID
	return fabric.WorkspaceLaunchProviderResult{Resources: resources}, nil
}

func (testProvider) ReadWorkspaceLaunchStage(ctx context.Context, request fabric.WorkspaceLaunchProviderRequest) (fabric.WorkspaceLaunchProviderResult, error) {
	return testProvider{}.EnsureWorkspaceLaunchStage(ctx, request)
}

func TestRuntimeHealthSummaryHTTPIsAuthenticatedAndReadOnly(t *testing.T) {
	provider := &runtimeHealthSummaryHTTPProvider{}
	service := fabric.NewService(provider)
	server := newTestServer(service, "internal-secret")

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/runtime-health-summary", nil))
	if unauthorized.Code != http.StatusUnauthorized || provider.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, provider.calls)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/runtime-health-summary", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var summary fabric.RuntimeHealthSummary
	if err := json.NewDecoder(recorder.Body).Decode(&summary); err != nil || summary.Total != 1000 || summary.Ready != 999 || summary.Unready != 1 || provider.calls != 1 {
		t.Fatalf("summary=%#v err=%v calls=%d", summary, err, provider.calls)
	}
	operations, err := service.ListOperations(context.Background())
	if err != nil || len(operations) != 0 {
		t.Fatalf("read-only summary operations=%#v err=%v", operations, err)
	}
}

func TestProviderFactsBatchHTTPPreservesTypedWireShape(t *testing.T) {
	service := fabric.NewService(testProvider{})
	compute, err := service.CreateComputeAllocation(context.Background(), fabric.ComputeAllocationInput{
		AccountID: "acct-alpha", WorkspaceID: "workspace-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "provider-facts-http",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		operations, listErr := service.ListOperations(context.Background())
		if listErr != nil {
			t.Fatal(listErr)
		}
		for _, operation := range operations {
			if operation.Action == "create_compute_allocation" && operation.ResourceID == compute.ID && operation.Status == "succeeded" {
				deadline = time.Time{}
				break
			}
		}
		if deadline.IsZero() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	server := newTestServer(service, "internal-secret")
	body := fmt.Sprintf(`{"items":[{"accountId":"acct-alpha","workspaceId":"workspace-alpha","resourceType":"compute","resourceId":%q}]}`, compute.ID)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, testRequest(http.MethodPost, "/fabric/provider-facts/batch", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("provider facts status=%d body=%s", response.Code, response.Body.String())
	}
	var batch fabric.ProviderFactsBatch
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil || len(batch.Items) != 1 || !batch.Items[0].Available || batch.Items[0].ResourceID != compute.ID || batch.Items[0].Facts.ProviderID != "machine/"+compute.ID || batch.Items[0].Facts.LastReadAt == "" {
		t.Fatalf("provider facts=%#v err=%v", batch, err)
	}
	for _, field := range []string{`"accountId"`, `"workspaceId"`, `"resourceType"`, `"resourceId"`, `"available"`, `"facts"`, `"packageOrSpec"`, `"providerId"`, `"zone"`, `"status"`, `"expiresAt"`, `"lastReadAt"`} {
		if !strings.Contains(response.Body.String(), field) {
			t.Fatalf("provider facts response lost %s: %s", field, response.Body.String())
		}
	}
}

func TestMachineOwnershipHTTPIsAuthenticatedExactAndNotFound(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	releasedAt := time.Now().UTC().Truncate(time.Second)
	ownership := fabric.MachineOwnership{
		ID: "owner-alpha", ResourceID: "compute-alpha", AccountID: "acct-alpha", PackageID: "basic",
		NodePoolID: "np-basic", MachineID: "machine-alpha", InstanceID: "ins-alpha", NodeName: "node-alpha",
		Status: "released", ClaimedAt: releasedAt.Add(-time.Minute), ReleasedAt: &releasedAt,
	}
	if _, _, err := store.ClaimMachine(context.Background(), ownership); err != nil {
		t.Fatal(err)
	}
	active := fabric.MachineOwnership{
		ID: "owner-active", ResourceID: "compute-active", AccountID: "acct-alpha", PackageID: "basic",
		NodePoolID: "np-basic", MachineID: "machine-active", InstanceID: "ins-active", NodeName: "node-active",
		Status: "active", ClaimedAt: releasedAt,
	}
	if _, _, err := store.ClaimMachine(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(fabric.NewServiceWithOperationStore(testProvider{}, store), "internal-secret")

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/machine-ownerships/compute-alpha", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-alpha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got fabric.MachineOwnership
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ResourceID != ownership.ResourceID || got.AccountID != ownership.AccountID || got.MachineID != ownership.MachineID ||
		got.InstanceID != ownership.InstanceID || got.NodeName != ownership.NodeName || got.Status != "released" ||
		got.ReleasedAt == nil || !got.ReleasedAt.Equal(releasedAt) {
		t.Fatalf("ownership = %#v", got)
	}
	activeRec := httptest.NewRecorder()
	server.ServeHTTP(activeRec, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-active", nil))
	if activeRec.Code != http.StatusOK || !strings.Contains(activeRec.Body.String(), `"status":"active"`) || strings.Contains(activeRec.Body.String(), `"releasedAt"`) {
		t.Fatalf("active status=%d body=%s", activeRec.Code, activeRec.Body.String())
	}

	missing := httptest.NewRecorder()
	server.ServeHTTP(missing, testRequest(http.MethodGet, "/fabric/machine-ownerships/compute-missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestTencentOperatorIdentityEvidenceRouteIsRetainedWithoutLegacyMutationRoutes(t *testing.T) {
	server := newTestServer(fabric.NewServiceWithOperationStore(testProvider{}, fabric.NewMemoryOperationStore()), "internal-secret")
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/fabric/compute-claim-recovery/identity-evidence", bytes.NewBufferString("{}")))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	retained := httptest.NewRecorder()
	server.ServeHTTP(retained, testRequest(http.MethodPost, "/fabric/compute-claim-recovery/identity-evidence", bytes.NewBufferString("{}")))
	if retained.Code != http.StatusConflict {
		t.Fatalf("retained route status=%d body=%s", retained.Code, retained.Body.String())
	}
	for _, path := range []string{
		"/fabric/compute-claim-recovery/proof",
		"/fabric/compute-claim-recovery/claim",
		"/fabric/workspace-launch-stage-readback/proof",
		"/fabric/workspace-launch-stage-readback/converge",
		"/fabric/workspace-activation-truth",
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, testRequest(http.MethodPost, path, bytes.NewBufferString("{}")))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("hard-cut route %s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func testRequest(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Authorization", "Bearer internal-secret")
	return req
}

func createReadyCompute(t *testing.T, service *fabric.Service, server http.Handler, accountID, workspaceID, key string) fabric.ComputeAllocation {
	t.Helper()
	request := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewBufferString(fmt.Sprintf(`{"accountId":%q,"workspaceId":%q,"packageId":"basic","nodePoolId":"np-basic"}`, accountID, workspaceID)))
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("create compute status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created fabric.ComputeAllocation
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if current, ok := service.GetComputeAllocation(context.Background(), created.ID); ok && current.Status == "running" {
			return current
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("compute %s did not become ready", created.ID)
	return fabric.ComputeAllocation{}
}

func TestServerDestroysWorkspaceRuntime(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	req := httptest.NewRequest(http.MethodPost, "/fabric/workspace-runtimes/workspace-alpha/destroy", nil)
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "runtime-destroy-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"destroyed"`) || !strings.Contains(rec.Body.String(), `"workspaceId":"workspace-alpha"`) {
		t.Fatalf("destroy status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServerReturnsTypedWorkspaceOwnerObservations(t *testing.T) {
	provider := workspaceOwnerObservationHTTPProvider{
		runtime: fabric.WorkspaceRuntime{ID: "runtime-alpha", WorkspaceID: "workspace-alpha", Status: "destroying"},
		secretBinding: fabric.WorkspaceRuntimeGatewaySecretBinding{
			WorkspaceID: "workspace-alpha", WorkspaceAPIKeyID: 19, SecretRef: "opl-gateway-a0ba8c07d0462e6b", Fingerprint: "sha256:alpha", Bound: true,
		},
	}
	server := newTestServer(fabric.NewService(provider), "internal-secret")
	for _, testCase := range []struct {
		path      string
		wantState string
		decode    func(*json.Decoder) (string, int, string)
	}{
		{
			path: "/fabric/workspace-runtimes/workspace-alpha/observation", wantState: fabric.WorkspaceOwnerObservationPending,
			decode: func(decoder *json.Decoder) (string, int, string) {
				var observation fabric.WorkspaceRuntimeObservation
				if err := decoder.Decode(&observation); err != nil || observation.Runtime == nil || observation.Runtime.ID != "runtime-alpha" {
					t.Fatalf("runtime observation=%#v err=%v", observation, err)
				}
				return observation.State, observation.SchemaVersion, observation.WorkspaceID
			},
		},
		{
			path: "/fabric/workspace-runtimes/workspace-alpha/gateway-secret/observation", wantState: fabric.WorkspaceOwnerObservationReady,
			decode: func(decoder *json.Decoder) (string, int, string) {
				var observation fabric.WorkspaceRuntimeGatewaySecretObservation
				if err := decoder.Decode(&observation); err != nil || observation.Binding == nil || observation.Binding.WorkspaceAPIKeyID != 19 {
					t.Fatalf("secret observation=%#v err=%v", observation, err)
				}
				return observation.State, observation.SchemaVersion, observation.WorkspaceID
			},
		},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, testRequest(http.MethodGet, testCase.path, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			state, version, workspaceID := testCase.decode(json.NewDecoder(recorder.Body))
			if state != testCase.wantState || version != fabric.WorkspaceOwnerObservationSchemaVersion || workspaceID != "workspace-alpha" {
				t.Fatalf("state=%q version=%d workspaceId=%q", state, version, workspaceID)
			}
		})
	}
}

func TestServerWorkspaceOwnerObservationPreservesAbsentConflictAndError(t *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
		want string
	}{
		{name: "absent", err: fabric.ErrWorkspaceLaunchResourceAbsent, want: fabric.WorkspaceOwnerObservationAbsent},
		{name: "conflict", err: fabric.ErrLaunchStageBindingConflict, want: fabric.WorkspaceOwnerObservationConflict},
		{name: "error", err: errors.New("provider unavailable"), want: fabric.WorkspaceOwnerObservationError},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := newTestServer(fabric.NewService(workspaceOwnerObservationHTTPProvider{runtimeErr: testCase.err, secretErr: testCase.err}), "internal-secret")
			for _, endpoint := range []string{"runtime", "secret"} {
				t.Run(endpoint, func(t *testing.T) {
					path := "/fabric/workspace-runtimes/workspace-alpha/observation"
					if endpoint == "secret" {
						path = "/fabric/workspace-runtimes/workspace-alpha/gateway-secret/observation"
					}
					recorder := httptest.NewRecorder()
					server.ServeHTTP(recorder, testRequest(http.MethodGet, path, nil))
					var observation struct {
						State string `json:"state"`
					}
					if recorder.Code != http.StatusOK || json.NewDecoder(recorder.Body).Decode(&observation) != nil || observation.State != testCase.want {
						t.Fatalf("status=%d observation=%#v body=%s", recorder.Code, observation, recorder.Body.String())
					}
				})
			}
		})
	}
}

func TestServerWritesGatewaySecretWithoutReturningRawKey(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/gateway-secrets", bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","workspaceApiKeyId":19,"fingerprint":"sha256:12982dcaf26b60cde5b6b68b01556e591badb2768ac9b71525619cb4ebc646f0","gatewayApiKey":"raw-gateway-key"}`))
	req.Header.Set("Idempotency-Key", "gateway-once")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), "raw-gateway-key") {
		t.Fatalf("gateway secret status=%d body=%s", rec.Code, rec.Body.String())
	}
	var secret fabric.GatewaySecret
	if err := json.NewDecoder(rec.Body).Decode(&secret); err != nil || secret.SecretRef == "" || secret.Version == "" || secret.Fingerprint == "" {
		t.Fatalf("gateway secret=%#v err=%v", secret, err)
	}
}

func TestServerMonthlyPreflightNeedsNoIdempotencyKeyAndRecordsNoOperation(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	server := newTestServer(fabric.NewServiceWithOperationStore(testProvider{}, store), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/monthly-preflight", bytes.NewBufferString(`{"resourceType":"storage","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result fabric.MonthlyPreflight
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil || result.ResourceType != "storage" || result.PackageID != "basic" || result.SizeGB != 10 || result.Zone != "na-siliconvalley-1" || !result.Available || result.ChargeType != "PREPAID" || result.PeriodMonths != 1 || result.RenewFlag != "NOTIFY_AND_MANUAL_RENEW" || result.ProviderPriceCNY <= 0 || len(result.ProviderRequestIDs) == 0 {
		t.Fatalf("preflight=%#v err=%v", result, err)
	}
	operations := httptest.NewRecorder()
	server.ServeHTTP(operations, testRequest(http.MethodGet, "/fabric/operations", nil))
	var page fabric.FabricOperationPage
	if operations.Code != http.StatusOK || json.NewDecoder(operations.Body).Decode(&page) != nil || len(page.Operations) != 0 || page.NextCursor != "" {
		t.Fatalf("operations status=%d body=%s", operations.Code, operations.Body.String())
	}
}

type unavailablePreflightProvider struct{ testProvider }

func (unavailablePreflightProvider) MonthlyPreflight(context.Context, fabric.MonthlyPreflightInput) (fabric.MonthlyPreflight, error) {
	return fabric.MonthlyPreflight{}, errors.New("private Tencent response")
}

func TestServerMonthlyPreflightFailsClosedWithStableErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider fabric.Provider
		body     string
		want     int
		message  string
	}{
		{name: "invalid compute size", provider: testProvider{}, body: `{"resourceType":"compute","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`, want: http.StatusBadRequest, message: "invalid_monthly_preflight"},
		{name: "provider unavailable", provider: unavailablePreflightProvider{}, body: `{"resourceType":"storage","packageId":"basic","sizeGb":10,"zone":"na-siliconvalley-1"}`, want: http.StatusServiceUnavailable, message: "monthly_preflight_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(fabric.NewService(tc.provider), "internal-secret")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, testRequest(http.MethodPost, "/fabric/monthly-preflight", bytes.NewBufferString(tc.body)))
			if recorder.Code != tc.want || !strings.Contains(recorder.Body.String(), tc.message) || strings.Contains(recorder.Body.String(), "private Tencent response") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type monthlyPreflightReportHTTPProvider struct{ testProvider }

func (monthlyPreflightReportHTTPProvider) MonthlyPreflightReport(context.Context, fabric.MonthlyPreflightReportInput) (fabric.MonthlyPreflightReport, error) {
	items := make([]fabric.MonthlyPreflightStage, 0, 2)
	for _, stage := range []string{"launch_permission", "credentials"} {
		items = append(items, fabric.MonthlyPreflightStage{Stage: stage, Status: "passed", BlockedBy: []string{}, SafeFacts: map[string]any{}, DurationMS: 1})
	}
	return fabric.MonthlyPreflightReport{
		SchemaVersion: 1, Status: "passed", Zone: "na-siliconvalley-1", Items: items,
		Sub2APIMutationCount: 0, TencentMutationCount: 0, KubernetesMutationCount: 0,
	}, nil
}

func TestServerMonthlyPreflightReportIsInternalReadOnlyAndStrictJSON(t *testing.T) {
	server := newTestServer(fabric.NewService(monthlyPreflightReportHTTPProvider{}), "internal-secret")
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/fabric/monthly-preflight-report?zone=na-siliconvalley-1", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, testRequest(http.MethodGet, "/fabric/monthly-preflight-report?zone=na-siliconvalley-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("report status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var report fabric.MonthlyPreflightReport
	decoder := json.NewDecoder(recorder.Body)
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("report has trailing JSON: err=%v body=%s", err, recorder.Body.String())
	}
	if report.Status != "passed" || len(report.Items) != 2 || report.Sub2APIMutationCount != 0 || report.TencentMutationCount != 0 || report.KubernetesMutationCount != 0 {
		t.Fatalf("report=%#v", report)
	}

	legacy := httptest.NewRecorder()
	server.ServeHTTP(legacy, testRequest(http.MethodGet, "/fabric/monthly-preflight-report?packageId=basic&sizeGb=10&zone=na-siliconvalley-1", nil))
	if legacy.Code != http.StatusBadRequest {
		t.Fatalf("legacy diagnostics query status=%d body=%s", legacy.Code, legacy.Body.String())
	}
}

func TestComputePoolHeadTerminalizationHTTPRoutesRemainRegistered(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, "/fabric/compute-pool-head/terminalization", strings.NewReader(`{}`))
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthorized status=%d body=%s", method, recorder.Code, recorder.Body.String())
		}

		recorder = httptest.NewRecorder()
		request = testRequest(method, "/fabric/compute-pool-head/terminalization", strings.NewReader(`{}`))
		server.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("%s terminalization route is not registered", method)
		}
	}
}

func TestServerRenewsComputeAllocation(t *testing.T) {
	service := fabric.NewService(testProvider{})
	allocation, err := service.CreateComputeAllocation(context.Background(), fabric.ComputeAllocationInput{ID: "compute-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha", PackageID: "basic", NodePoolID: "np-basic", IdempotencyKey: "compute-create"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if current, ok := service.GetComputeAllocation(context.Background(), allocation.ID); ok && current.Status == "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	server := newTestServer(service, "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/compute-allocations/compute-alpha/renew", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "compute-renew-once")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("renew status=%d body=%s", rec.Code, rec.Body.String())
	}
	var renewed fabric.ComputeAllocation
	if err := json.NewDecoder(rec.Body).Decode(&renewed); err != nil || renewed.Deadline != "2026-09-16T00:00:00Z" || renewed.ProviderData["renewalResult"] != "renewed" {
		t.Fatalf("renewed allocation=%#v err=%v", renewed, err)
	}
}

func TestRuntimeOperationConflictsAreHTTPConflict(t *testing.T) {
	for _, err := range []error{fabric.ErrRuntimeIdempotencyConflict, fabric.ErrRuntimeOperationInProgress, fabric.ErrRuntimeOperationFailed, fabric.ErrGatewaySecretIdempotencyConflict} {
		recorder := httptest.NewRecorder()
		writeResult(recorder, fabric.WorkspaceRuntime{}, err)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("error %v status = %d, want %d", err, recorder.Code, http.StatusConflict)
		}
	}
}

func TestCatalogHTTP(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodGet, "/fabric/catalog", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var catalog fabric.Catalog
	if err := json.NewDecoder(rec.Body).Decode(&catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(catalog.WorkspacePackages) == 0 {
		t.Fatalf("expected workspace packages")
	}
}

func TestFabricCatalogContractKeepsConsoleProjectionProductOnly(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	contractPath := filepath.Join(filepath.Dir(sourceFile), "../../../../packages/contracts/opl-cloud-fabric-resource-catalog-contract.json")
	data, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read catalog contract: %v", err)
	}
	var contract struct {
		SchemaVersion     int `json:"schemaVersion"`
		ConsoleProjection struct {
			Fields              []string `json:"fields"`
			SelectionInput      string   `json:"selectionInput"`
			ForbiddenFields     []string `json:"forbiddenFields"`
			InfrastructureOwner string   `json:"infrastructureOwnership"`
		} `json:"consoleProjection"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatalf("decode catalog contract: %v", err)
	}
	if contract.SchemaVersion != 7 {
		t.Fatalf("catalog contract schemaVersion=%d, want 7", contract.SchemaVersion)
	}
	if !reflect.DeepEqual(contract.ConsoleProjection.Fields, []string{"id", "name", "available"}) {
		t.Fatalf("console catalog fields=%v", contract.ConsoleProjection.Fields)
	}
	if contract.ConsoleProjection.SelectionInput != "packageId_only" || contract.ConsoleProjection.InfrastructureOwner != "Fabric Provider Profile and adapter only" {
		t.Fatalf("console projection ownership=%#v", contract.ConsoleProjection)
	}
	allowed := map[string]bool{}
	for _, field := range contract.ConsoleProjection.Fields {
		allowed[field] = true
	}
	for _, field := range contract.ConsoleProjection.ForbiddenFields {
		if allowed[field] {
			t.Fatalf("catalog field %q is both allowed and forbidden", field)
		}
	}
	for _, field := range []string{"provider", "cpu", "memoryGb", "diskGb", "instanceType", "nodePoolId", "zone", "diskType", "chargeType", "renewFlag", "canonicalProviderPlan", "providerBindingRef", "specDigest"} {
		found := false
		for _, forbidden := range contract.ConsoleProjection.ForbiddenFields {
			if forbidden == field {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("catalog contract does not forbid provider field %q", field)
		}
	}
}

func TestWriteComputeAllocationResultPreservesTerminalEvidence(t *testing.T) {
	allocation := fabric.ComputeAllocation{
		ID: "compute-fixture", AccountID: "acct-fixture", WorkspaceID: "ws-fixture", PackageID: "basic", Status: "quarantined",
		ClaimTerminalEvidence: &fabric.ComputeClaimTerminalEvidence{Stage: "compute_claim_node", Status: "terminal_unprovable", ErrorCode: "compute_claim_node_unprovable"},
	}
	recorder := httptest.NewRecorder()
	writeComputeAllocationResult(recorder, allocation, fabric.ErrComputeOperationFailed)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var got fabric.ComputeAllocation
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil || got.ID != allocation.ID || got.ClaimTerminalEvidence == nil || got.ClaimTerminalEvidence.Status != "terminal_unprovable" {
		t.Fatalf("allocation=%#v err=%v", got, err)
	}
}

func TestCreateComputeAllocationHTTPRequiresIdempotencyKey(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	body := bytes.NewBufferString(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","dryRun":true}`)
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", body)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestFabricHTTPRejectsOversizedJSONBeforeMutation(t *testing.T) {
	provider := &capabilityBoundaryProvider{}
	store := fabric.NewMemoryOperationStore()
	server := newTestServer(fabric.NewServiceWithOperationStore(provider, store), "internal-secret")
	prefix := `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","padding":"`
	suffix := `"}`
	body := []byte(prefix + strings.Repeat("x", int(maxJSONBodyBytes)-len(prefix)-len(suffix)+1) + suffix)
	if int64(len(body)) != maxJSONBodyBytes+1 {
		t.Fatalf("body length=%d, want %d", len(body), maxJSONBodyBytes+1)
	}
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "oversized-compute")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request_body_too_large") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 0 || provider.computeCreates.Load() != 0 {
		t.Fatalf("oversized request changed Fabric state: operations=%#v providerCalls=%d err=%v", operations, provider.computeCreates.Load(), err)
	}
}

func TestFabricHTTPBodyLimitAllowsExactOneMiBJSON(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	prefix := `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","padding":"`
	suffix := `"}`
	body := []byte(prefix + strings.Repeat("x", int(maxJSONBodyBytes)-len(prefix)-len(suffix)) + suffix)
	if int64(len(body)) != maxJSONBodyBytes {
		t.Fatalf("body length=%d, want %d", len(body), maxJSONBodyBytes)
	}
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "exact-limit-compute")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("exact-limit request rejected: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFabricHTTPBodyLimitCoversDirectJSONRoutes(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	prefix := `{"schemaVersion":1,"launchOperationId":"launch-alpha","accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","sizeGb":10,"workspaceImageDigest":"digest","requestHash":"`
	suffix := `"}`
	body := []byte(prefix + strings.Repeat("x", int(maxJSONBodyBytes)-len(prefix)-len(suffix)+1) + suffix)
	if int64(len(body)) != maxJSONBodyBytes+1 {
		t.Fatalf("body length=%d, want %d", len(body), maxJSONBodyBytes+1)
	}
	req := testRequest(http.MethodPost, "/fabric/workspace-launches/preflight", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request_body_too_large") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFabricAuthenticatedMutationRejectsOversizedBodyBeforeCapabilityOrMutation(t *testing.T) {
	provider := &capabilityBoundaryProvider{}
	store := fabric.NewMemoryOperationStore()
	server := NewServerWithAuth(fabric.NewServiceWithOperationStore(provider, store), ServerAuthConfig{
		ControlPlaneToken: "internal-secret", RunnerToken: "runner-secret", CapabilityKey: testFabricCapabilityKey,
	})
	prefix := `{"id":"compute-oversized","accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"basic","padding":"`
	suffix := `"}`
	body := []byte(prefix + strings.Repeat("x", int(maxJSONBodyBytes)-len(prefix)-len(suffix)+1) + suffix)
	if int64(len(body)) != maxJSONBodyBytes+1 {
		t.Fatalf("body length=%d, want %d", len(body), maxJSONBodyBytes+1)
	}
	claims := fabricCapabilityClaimsForTest{
		Version: 1, Caller: "control-plane", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		ResourceKind: "compute_allocation", ResourceID: "compute-oversized", Action: "create_compute_allocation", OperationID: "oversized-auth",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	req := testRequest(http.MethodPost, "/fabric/compute-allocations", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer internal-secret")
	req.Header.Set("Idempotency-Key", "oversized-auth")
	req.Header.Set(fabricCapabilityHeader, fabricCapabilityForTest(t, claims, body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "request_body_too_large") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 0 || provider.computeCreates.Load() != 0 {
		t.Fatalf("oversized authenticated request changed Fabric state: operations=%#v providerCalls=%d err=%v", operations, provider.computeCreates.Load(), err)
	}
}

func TestResourceBoundaryHTTPReturnsBadRequest(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	for _, tc := range []struct{ name, path, body string }{
		{name: "package", path: "/fabric/compute-allocations", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha","packageId":"enterprise"}`},
		{name: "storage", path: "/fabric/storage-volumes", body: `{"accountId":"acct-alpha","workspaceId":"ws-alpha","sizeGb":15}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Idempotency-Key", "invalid-"+tc.name)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFabricSyncHTTPRoutesAreRetired(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	for _, path := range []string{
		"/fabric/compute-allocations/compute-alpha/sync",
		"/fabric/storage-volumes/storage-alpha/sync",
	} {
		req := testRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestGetStorageVolumeHTTPIsReadOnly(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := newTestServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "get-storage-compute")
	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	create.Header.Set("Idempotency-Key", "get-http-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d: %s", createRec.Code, createRec.Body.String())
	}
	var created fabric.StorageVolume
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	before, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	req := testRequest(http.MethodGet, "/fabric/storage-volumes/"+created.ID, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", rec.Code, rec.Body.String())
	}
	var volume fabric.StorageVolume
	if err := json.NewDecoder(rec.Body).Decode(&volume); err != nil || volume.ID != created.ID || volume.Status != "ready" {
		t.Fatalf("get volume=%#v err=%v", volume, err)
	}
	after, err := service.ListOperations(context.Background())
	if err != nil || len(after) != len(before) {
		t.Fatalf("read-only GET operations before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestOperationsHTTPReturnsFabricAuditFacts(t *testing.T) {
	service := fabric.NewService(testProvider{})
	server := newTestServer(service, "internal-secret")
	compute := createReadyCompute(t, service, server, "acct-alpha", "ws-alpha", "ops-storage-compute")

	create := testRequest(http.MethodPost, "/fabric/storage-volumes", bytes.NewBufferString(fmt.Sprintf(`{"accountId":"acct-alpha","workspaceId":"ws-alpha","computeId":%q,"zone":"ap-guangzhou-3","sizeGb":10}`, compute.ID)))
	create.Header.Set("Idempotency-Key", "http-ops-storage")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}

	req := testRequest(http.MethodGet, "/fabric/operations", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operations status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var page fabric.FabricOperationPage
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode operations: %v", err)
	}
	for _, operation := range page.Operations {
		if operation.Action == "create_storage_volume" && operation.ResourceKind == "storage_volume" && operation.Status == "succeeded" {
			if operation.OperationID == "" || operation.ProviderRequestID != "storage-test" || operation.RequestHash == "" {
				t.Fatalf("operation missing audit identity: %#v", operation)
			}
			return
		}
	}
	t.Fatalf("missing storage operation in %#v", page.Operations)
}

func TestFabricOperationsHTTPRequiresBoundedCursorPages(t *testing.T) {
	store := fabric.NewMemoryOperationStore()
	createdAt := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	for index := 0; index < fabric.MaxFabricOperationPageSize+2; index++ {
		operation := fabric.FabricOperation{
			ID: fmt.Sprintf("fop-page-%03d", index), OperationID: fmt.Sprintf("op-page-%03d", index), CallerService: "control-plane",
			Action: "page_test", ResourceKind: "test", ResourceID: fmt.Sprintf("resource-%03d", index), Status: "succeeded",
			StartedAt: createdAt.Add(time.Duration(index) * time.Second), CreatedAt: createdAt.Add(time.Duration(index) * time.Second),
		}
		if err := store.Append(context.Background(), operation); err != nil {
			t.Fatal(err)
		}
	}
	server := newTestServer(fabric.NewServiceWithOperationStore(testProvider{}, store), "internal-secret")

	firstRequest := testRequest(http.MethodGet, "/fabric/operations?limit=2", nil)
	first := httptest.NewRecorder()
	server.ServeHTTP(first, firstRequest)
	var firstPage fabric.FabricOperationPage
	if first.Code != http.StatusOK || json.NewDecoder(first.Body).Decode(&firstPage) != nil || len(firstPage.Operations) != 2 || firstPage.NextCursor == "" {
		t.Fatalf("first page status=%d page=%#v body=%s", first.Code, firstPage, first.Body.String())
	}

	secondRequest := testRequest(http.MethodGet, "/fabric/operations?limit=2&cursor="+url.QueryEscape(firstPage.NextCursor), nil)
	second := httptest.NewRecorder()
	server.ServeHTTP(second, secondRequest)
	var secondPage fabric.FabricOperationPage
	if second.Code != http.StatusOK || json.NewDecoder(second.Body).Decode(&secondPage) != nil || len(secondPage.Operations) != 2 || secondPage.Operations[0].ID == firstPage.Operations[0].ID {
		t.Fatalf("second page status=%d page=%#v body=%s", second.Code, secondPage, second.Body.String())
	}

	for _, path := range []string{
		fmt.Sprintf("/fabric/operations?limit=%d", fabric.MaxFabricOperationPageSize+1),
		"/fabric/operations?cursor=not-a-cursor",
		"/fabric/operations?limit=2&limit=3",
		"/fabric/operations?unknown=1",
	} {
		invalid := httptest.NewRecorder()
		server.ServeHTTP(invalid, testRequest(http.MethodGet, path, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid page %q status=%d body=%s", path, invalid.Code, invalid.Body.String())
		}
	}
}

func TestJobHTTPLifecycle(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha","environmentRef":"environment-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-job-once")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createRec.Code, http.StatusAccepted, createRec.Body.String())
	}
	var created fabric.Job
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	get := testRequest(http.MethodGet, "/fabric/jobs/"+created.JobID, nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d: %s", getRec.Code, http.StatusOK, getRec.Body.String())
	}

	cancel := testRequest(http.MethodPost, "/fabric/jobs/"+created.JobID+"/cancel", bytes.NewBufferString(`{}`))
	cancel.Header.Set("Idempotency-Key", "http-job-cancel")
	cancelRec := httptest.NewRecorder()
	server.ServeHTTP(cancelRec, cancel)
	if cancelRec.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, want %d: %s", cancelRec.Code, http.StatusAccepted, cancelRec.Body.String())
	}
	var cancelled fabric.Job
	if err := json.NewDecoder(cancelRec.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancelled job: %v", err)
	}
	if cancelled.JobID != created.JobID || cancelled.Status != "cancelled" {
		t.Fatalf("unexpected cancelled job: %#v", cancelled)
	}
}

func TestJobHTTPReturnsNotFound(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodGet, "/fabric/jobs/job-missing", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestJobHTTPRequiresCanonicalIdentity(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	req := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{}`))
	req.Header.Set("Idempotency-Key", "invalid-job")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestRunnerJobHTTPCompletionLifecycle(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-runner-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	if err := json.NewDecoder(createRec.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}

	claim := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Idempotency-Key", "http-claim")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	if claimRec.Code != http.StatusAccepted {
		t.Fatalf("claim status = %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var claimed fabric.Job
	if err := json.NewDecoder(claimRec.Body).Decode(&claimed); err != nil || claimed.LeaseToken == "" {
		t.Fatalf("decode claim: %#v, %v", claimed, err)
	}

	heartbeat := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`"}`))
	heartbeat.Header.Set("Idempotency-Key", "http-heartbeat")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeat)
	if heartbeatRec.Code != http.StatusAccepted {
		t.Fatalf("heartbeat status = %d: %s", heartbeatRec.Code, heartbeatRec.Body.String())
	}

	complete := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/complete", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","artifactIds":["artifact-alpha"],"reviewIds":["review-alpha"]}`))
	complete.Header.Set("Idempotency-Key", "http-complete")
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, complete)
	if completeRec.Code != http.StatusAccepted {
		t.Fatalf("complete status = %d: %s", completeRec.Code, completeRec.Body.String())
	}
	var completed fabric.Job
	if err := json.NewDecoder(completeRec.Body).Decode(&completed); err != nil || completed.Status != "succeeded" {
		t.Fatalf("decode complete: %#v, %v", completed, err)
	}
}

func TestRunnerJobHTTPFailRetryAndConflict(t *testing.T) {
	server := newTestServer(fabric.NewService(testProvider{}), "internal-secret")
	create := testRequest(http.MethodPost, "/fabric/jobs", bytes.NewBufferString(`{"organizationId":"org-alpha","workspaceId":"workspace-alpha","projectId":"project-alpha","taskId":"task-alpha","requestId":"request-alpha","approvalId":"approval-alpha"}`))
	create.Header.Set("Idempotency-Key", "http-fail-job")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	var job fabric.Job
	_ = json.NewDecoder(createRec.Body).Decode(&job)
	claim := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/claim", bytes.NewBufferString(`{"runnerId":"runner-alpha"}`))
	claim.Header.Set("Idempotency-Key", "http-fail-claim")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claim)
	var claimed fabric.Job
	_ = json.NewDecoder(claimRec.Body).Decode(&claimed)

	conflict := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/heartbeat", bytes.NewBufferString(`{"runnerId":"runner-beta","leaseToken":"`+claimed.LeaseToken+`"}`))
	conflict.Header.Set("Idempotency-Key", "http-wrong-runner")
	conflictRec := httptest.NewRecorder()
	server.ServeHTTP(conflictRec, conflict)
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("lease conflict status = %d, want %d: %s", conflictRec.Code, http.StatusConflict, conflictRec.Body.String())
	}

	fail := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/fail", bytes.NewBufferString(`{"runnerId":"runner-alpha","leaseToken":"`+claimed.LeaseToken+`","errorCode":"runner_failed"}`))
	fail.Header.Set("Idempotency-Key", "http-fail")
	failRec := httptest.NewRecorder()
	server.ServeHTTP(failRec, fail)
	if failRec.Code != http.StatusAccepted {
		t.Fatalf("fail status = %d: %s", failRec.Code, failRec.Body.String())
	}

	retry := testRequest(http.MethodPost, "/fabric/jobs/"+job.JobID+"/retry", nil)
	retry.Header.Set("Idempotency-Key", "http-retry")
	retryRec := httptest.NewRecorder()
	server.ServeHTTP(retryRec, retry)
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d: %s", retryRec.Code, retryRec.Body.String())
	}
	var retried fabric.Job
	if err := json.NewDecoder(retryRec.Body).Decode(&retried); err != nil || retried.Status != "queued" || retried.Attempt != 2 {
		t.Fatalf("decode retry: %#v, %v", retried, err)
	}
}

type testProvider struct{}

func (testProvider) Descriptor() fabric.ProviderDescriptor {
	return fabric.ProviderDescriptor{
		Name: "tencent-tke", RequiresMonthlyPricing: true,
		Plans: map[string]fabric.ComputePlan{
			"basic": {ID: "pool-basic-2c4g", Server: "2c4g", CPU: 2, MemoryGB: 4, DiskGB: 10, InstanceType: "SA5.MEDIUM4"},
			"pro":   {ID: "pool-pro-8c16g", Server: "8c16g", CPU: 8, MemoryGB: 16, DiskGB: 100, InstanceType: "SA5.2XLARGE16"},
		},
		Catalog: fabric.Catalog{
			SchemaVersion: 1, Owner: "OPL Fabric",
			WorkspacePackages: []fabric.WorkspacePackage{{ID: "basic", Provider: "tencent-tke", Available: true}},
		},
	}
}

func (testProvider) ResolveWorkspacePlan(_ context.Context, input fabric.WorkspaceLaunchPlanInput) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"compute": map[string]any{"cpu": 2, "memoryGb": 4},
		"storage": map[string]any{"sizeGb": input.SizeGB},
	})
}

func (testProvider) ValidateComputeAllocation(allocation fabric.ComputeAllocation, prepared fabric.ComputeAllocationPreparation) error {
	if allocation.Provider != "tencent-tke" || allocation.PoolID != prepared.PoolID || allocation.NodePoolID != prepared.NodePoolID ||
		allocation.InstanceType != prepared.InstanceType || allocation.MachineName == "" || !strings.HasPrefix(allocation.InstanceID, "ins-") ||
		allocation.NodeName == "" || allocation.PrivateIP == "" || allocation.Zone == "" {
		return errors.New("compute_provider_readback_mismatch")
	}
	return nil
}

func (testProvider) ValidateWorkspaceImageReference(value string) bool {
	const prefix = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:"
	return strings.HasPrefix(value, prefix) && len(value) == len(prefix)+64
}

func (testProvider) ReadComputeProviderFacts(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ProviderResourceFacts, error) {
	return fabric.ProviderResourceFacts{
		PackageOrSpec: allocation.PackageID, ProviderID: allocation.ProviderResourceID, Zone: allocation.Zone, Status: allocation.Status, ExpiresAt: allocation.Deadline,
	}, nil
}

func (testProvider) ReadStorageProviderFacts(_ context.Context, volume fabric.StorageVolume) (fabric.ProviderResourceFacts, error) {
	return fabric.ProviderResourceFacts{PackageOrSpec: volume.StorageClass, ProviderID: volume.ProviderResourceID, Zone: volume.Zone, Status: volume.Status, ExpiresAt: volume.Deadline}, nil
}

func (testProvider) ReadStorageAttachmentProviderFacts(_ context.Context, attachment fabric.StorageAttachment, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.ProviderResourceFacts, error) {
	return fabric.ProviderResourceFacts{PackageOrSpec: "/data", ProviderID: attachment.ProviderAttachmentID, Status: attachment.Status}, nil
}

func (testProvider) WorkspaceRuntimeProviderFacts(runtime fabric.WorkspaceRuntime) fabric.ProviderResourceFacts {
	return fabric.ProviderResourceFacts{ProviderID: runtime.ServiceName, Status: runtime.Status}
}

func (testProvider) PrepareComputeAllocation(_ context.Context, input fabric.ComputeAllocationInput) (fabric.ComputeAllocationPreparation, error) {
	instanceType := "SA5.MEDIUM4"
	poolID := "pool-basic-2c4g"
	if input.PackageID == "pro" {
		instanceType = "SA5.2XLARGE16"
		poolID = "pool-pro-8c16g"
	}
	return fabric.ComputeAllocationPreparation{
		PoolID: poolID, PackageID: input.PackageID, NodePoolID: input.NodePoolID, InstanceType: instanceType,
		MaxReplicas: 10, BaselineReplicas: 0, TargetReplicas: 1, BeforeMachineNames: []string{},
	}, nil
}

func (testProvider) CreateComputeAllocation(_ context.Context, input fabric.ComputeAllocationExecution) (fabric.ComputeAllocation, error) {
	id := input.Allocation.ID
	return fabric.ComputeAllocation{
		ID: id, AccountID: input.Allocation.AccountID, WorkspaceID: input.Allocation.WorkspaceID, PackageID: input.Allocation.PackageID,
		Status: "running", Provider: "tencent-tke", ProviderResourceID: "machine/" + id, ProviderRequestID: "compute-test",
		PoolID: input.Plan.PoolID, NodePoolID: input.Plan.NodePoolID, MachineName: id, InstanceID: "ins-" + id, CVMInstanceID: "ins-" + id,
		NodeName: "10.0.0.11", PrivateIP: "10.0.0.11", InstanceType: input.Plan.InstanceType, Zone: "ap-guangzhou-3",
		ChargeType: "PREPAID", RenewFlag: "NOTIFY_AND_MANUAL_RENEW", Deadline: "2026-08-16T00:00:00Z",
		ProviderData: map[string]string{"instanceType": input.Plan.InstanceType, "zone": "ap-guangzhou-3", "chargeType": "PREPAID", "renewFlag": "NOTIFY_AND_MANUAL_RENEW", "deadline": "2026-08-16T00:00:00Z"},
		CostTags: map[string]string{
			"opl_account_id": input.Allocation.AccountID, "opl_workspace_id": input.Allocation.WorkspaceID,
			"opl_resource_id": id, "opl_operation_id": "owner-" + id,
		},
	}, nil
}

func (testProvider) MonthlyPreflight(_ context.Context, input fabric.MonthlyPreflightInput) (fabric.MonthlyPreflight, error) {
	requestIDs := map[string]string{"nodePool": "req-pool", "subnets": "req-subnets", "availability": "req-capacity"}
	price := 142.91
	if input.ResourceType == "storage" {
		requestIDs = map[string]string{"quota": "req-quota", "price": "req-price"}
		price = 7.5
	}
	return fabric.MonthlyPreflight{
		ResourceType: input.ResourceType, PackageID: input.PackageID, SizeGB: input.SizeGB, Zone: input.Zone,
		Available: true, ChargeType: "PREPAID", PeriodMonths: 1, RenewFlag: "NOTIFY_AND_MANUAL_RENEW", ProviderPriceCNY: price, ProviderRequestIDs: requestIDs,
	}, nil
}

func (testProvider) TagComputeMachine(_ context.Context, _ fabric.ProviderMachine, _ fabric.MachineOwnership) error {
	return nil
}

func (testProvider) SyncComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Status = "running"
	return allocation, nil
}

func (testProvider) RenewComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Deadline = "2026-09-16T00:00:00Z"
	allocation.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	allocation.ChargeType = "PREPAID"
	if allocation.ProviderData == nil {
		allocation.ProviderData = map[string]string{}
	}
	allocation.ProviderData["deadline"] = allocation.Deadline
	allocation.ProviderData["renewFlag"] = allocation.RenewFlag
	allocation.ProviderData["renewalResult"] = "renewed"
	return allocation, nil
}

func (testProvider) DestroyComputeAllocation(_ context.Context, allocation fabric.ComputeAllocation) (fabric.ComputeAllocation, error) {
	allocation.Status = "destroyed"
	return allocation, nil
}

func (testProvider) CreateStorageVolume(_ context.Context, input fabric.StorageVolumeInput) (fabric.StorageVolume, error) {
	return fabric.StorageVolume{ID: "vol-test", AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, Status: "ready", ProviderRequestID: "storage-test"}, nil
}

func (testProvider) SyncStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Status = "external_deleted"
	return volume, nil
}

func (testProvider) RenewStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Deadline = "2026-09-16T00:00:00Z"
	volume.RenewFlag = "NOTIFY_AND_MANUAL_RENEW"
	if volume.ProviderData == nil {
		volume.ProviderData = map[string]string{}
	}
	volume.ProviderData["diskChargeType"] = "PREPAID"
	volume.ProviderData["deadline"] = volume.Deadline
	volume.ProviderData["renewFlag"] = volume.RenewFlag
	volume.ProviderData["renewalResult"] = "renewed"
	return volume, nil
}

func (testProvider) DestroyStorageVolume(_ context.Context, volume fabric.StorageVolume) (fabric.StorageVolume, error) {
	volume.Status = "destroyed"
	return volume, nil
}

func (testProvider) CreateStorageAttachment(_ context.Context, input fabric.StorageAttachmentInput, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.StorageAttachment, error) {
	return fabric.StorageAttachment{ID: "att-test", WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, Status: "attached", ProviderRequestID: "attachment-test"}, nil
}

func (testProvider) DetachStorageAttachment(_ context.Context, attachment fabric.StorageAttachment) (fabric.StorageAttachment, error) {
	attachment.Status = "detached"
	return attachment, nil
}

func (testProvider) CreateWorkspaceRuntime(_ context.Context, input fabric.WorkspaceRuntimeInput, _ fabric.ComputeAllocation, _ fabric.StorageVolume) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{ID: "rt-test", WorkspaceID: input.WorkspaceID, Status: "running", ProviderRequestID: "runtime-test"}, nil
}

func (testProvider) DestroyWorkspaceRuntime(_ context.Context, workspaceID string) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "destroyed"}, nil
}

func (testProvider) WorkspaceRuntimeStatus(_ context.Context, workspaceID string) (fabric.WorkspaceRuntime, error) {
	return fabric.WorkspaceRuntime{WorkspaceID: workspaceID, Status: "not_found"}, nil
}

func (testProvider) UpsertGatewaySecret(_ context.Context, input fabric.GatewaySecretInput) (fabric.GatewaySecret, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(input.GatewayAPIKey)))
	return fabric.GatewaySecret{SecretRef: "opl-gateway-ws-alpha", Version: digest[:16], Fingerprint: "sha256:" + digest}, nil
}

func (testProvider) Readiness(_ context.Context) (map[string]any, error) {
	return map[string]any{"provider": "test", "ready": true}, nil
}
