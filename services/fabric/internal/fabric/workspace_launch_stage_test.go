package fabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type workspaceLaunchRecordingProvider struct {
	testProvider
	ensureCalls int
}

func (p *workspaceLaunchRecordingProvider) EnsureWorkspaceLaunchStage(_ context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	p.ensureCalls++
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func (p *workspaceLaunchRecordingProvider) ReadWorkspaceLaunchStage(_ context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func workspaceLaunchStageFixture(t *testing.T) (*Service, *MemoryOperationStore, *workspaceLaunchRecordingProvider, WorkspaceLaunchPreflight, string, string) {
	t.Helper()
	store := NewMemoryOperationStore()
	provider := &workspaceLaunchRecordingProvider{}
	service := NewServiceWithOperationStore(provider, store)
	image := "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("a", 64)
	launchHash := strings.Repeat("b", 64)
	preflight, err := service.PreflightWorkspaceLaunch(context.Background(), WorkspaceLaunchPreflightInput{
		SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, RequestHash: launchHash,
	})
	if err != nil || !preflight.Available || preflight.BindingRef == "" {
		t.Fatalf("preflight=%#v err=%v", preflight, err)
	}
	return service, store, provider, preflight, image, launchHash
}

func TestWorkspaceLaunchAdaptersImplementSameProviderNeutralPort(t *testing.T) {
	for name, provider := range map[string]Provider{
		"local-docker": NewLocalDockerProvider(),
		"tencent-tke":  NewTencentProvider(),
	} {
		if _, ok := provider.(workspaceLaunchProvider); !ok {
			t.Fatalf("provider %s does not implement workspaceLaunchProvider", name)
		}
	}
}

func workspaceLaunchStageFixtureInput(preflight WorkspaceLaunchPreflight, image, launchHash, stage, action string, resources WorkspaceLaunchResources) WorkspaceLaunchStageInput {
	input := WorkspaceLaunchStageInput{
		Binding: WorkspaceLaunchStageBinding{
			SchemaVersion: 1, LaunchOperationID: "launch-alpha", AccountID: "acct-alpha", WorkspaceID: "ws-alpha",
			Stage: stage, Action: action, FabricOperationID: "launch-alpha:" + stage, IdempotencyKey: "launch-alpha:" + stage,
		},
		ProviderProfileRef: "tencent-tke", PreflightBindingRef: preflight.BindingRef,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, Resources: resources,
	}
	input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
	return input
}

func TestWorkspaceLaunchPreflightIsDurableAndPointReadBeforeStageWrite(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	operation, err := store.Get(context.Background(), preflight.BindingRef)
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if err != nil || !ok || operation.Status != "succeeded" || admission.BindingRef != preflight.BindingRef ||
		admission.Input.LaunchOperationID != "launch-alpha" || admission.Input.AccountID != "acct-alpha" ||
		admission.Input.WorkspaceID != "ws-alpha" || admission.Input.PackageID != "basic" || admission.Input.SizeGB != 10 ||
		admission.Input.WorkspaceImageDigest != image || admission.Input.RequestHash != launchHash || admission.ProviderProfileRef != "tencent-tke" {
		t.Fatalf("operation=%#v admission=%#v/%v err=%v", operation, admission, ok, err)
	}

	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", WorkspaceLaunchResources{})
	input.PreflightBindingRef = "fabric-preflight:" + strings.Repeat("0", 64)
	if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingNotFound) {
		t.Fatalf("forged preflight error=%v", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].ID != preflight.BindingRef || provider.ensureCalls != 0 {
		t.Fatalf("forged preflight crossed stage write: operations=%#v providerCalls=%d err=%v", operations, provider.ensureCalls, err)
	}
}

func TestWorkspaceLaunchStageRejectsEveryPreflightIdentityDrift(t *testing.T) {
	service, _, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	tests := []struct {
		name   string
		mutate func(*WorkspaceLaunchStageInput)
	}{
		{name: "launch", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.LaunchOperationID = "launch-other" }},
		{name: "account", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.AccountID = "acct-other" }},
		{name: "workspace", mutate: func(input *WorkspaceLaunchStageInput) { input.Binding.WorkspaceID = "ws-other" }},
		{name: "provider", mutate: func(input *WorkspaceLaunchStageInput) { input.ProviderProfileRef = "local-docker" }},
		{name: "package", mutate: func(input *WorkspaceLaunchStageInput) { input.PackageID = "pro" }},
		{name: "size", mutate: func(input *WorkspaceLaunchStageInput) { input.SizeGB = 20 }},
		{name: "image", mutate: func(input *WorkspaceLaunchStageInput) {
			input.WorkspaceImageDigest = "uswccr.ccs.tencentyun.com/oplcloud/one-person-lab-app@sha256:" + strings.Repeat("c", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", WorkspaceLaunchResources{})
			test.mutate(&input)
			input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("drift error=%v input=%#v", err, input)
			}
			if provider.ensureCalls != 0 {
				t.Fatalf("identity drift reached provider: calls=%d", provider.ensureCalls)
			}
		})
	}
}

func TestWorkspaceLaunchStageRecomputesFiveCanonicalRequestHashesAndRejectsResourceDrift(t *testing.T) {
	service, _, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	stages := []struct {
		stage     string
		action    string
		resources WorkspaceLaunchResources
	}{
		{stage: "ensure_compute_allocation", action: "ensure_compute_allocation"},
		{stage: "storage", action: "ensure_storage", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: "launch-alpha:ensure_compute_allocation"}},
		{stage: "attachment", action: "ensure_attachment", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: "launch-alpha:ensure_compute_allocation", StorageID: "vol-alpha", StorageBindingRef: "launch-alpha:storage"}},
		{stage: "secret", action: "ensure_gateway_secret", resources: WorkspaceLaunchResources{AttachmentID: "att-alpha", AttachmentBindingRef: "launch-alpha:attachment", GatewaySecretFingerprint: "sha256:" + strings.Repeat("d", 64)}},
		{stage: "runtime", action: "ensure_runtime", resources: WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", StorageID: "vol-alpha", AttachmentID: "att-alpha", GatewaySecretRef: "secret-alpha"}},
	}
	for _, stage := range stages {
		t.Run(stage.stage, func(t *testing.T) {
			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, stage.stage, stage.action, stage.resources)
			input.Binding.RequestHash = workspaceLaunchCallerRequestHash(input, launchHash)
			if got := workspaceLaunchStageRequestHash(input, launchHash); got != input.Binding.RequestHash {
				t.Fatalf("Fabric hash=%s caller hash=%s", got, input.Binding.RequestHash)
			}
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), input); err != nil {
				t.Fatalf("canonical input rejected: %v", err)
			}
			driftedHash := input
			driftedHash.Binding.RequestHash = strings.Repeat("e", 64)
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), driftedHash); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("request hash drift error=%v", err)
			}
			driftedResource := input
			driftedResource.Resources.RuntimeURL = "http://drift.invalid"
			if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), driftedResource); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("resource drift error=%v", err)
			}
			if provider.ensureCalls != 0 {
				t.Fatalf("hash or resource drift reached provider: calls=%d", provider.ensureCalls)
			}
		})
	}
}

func workspaceLaunchCallerRequestHash(input WorkspaceLaunchStageInput, launchRequestHash string) string {
	payload, _ := json.Marshal(struct {
		LaunchRequestHash string                   `json:"launchRequestHash"`
		Action            string                   `json:"action"`
		PackageID         string                   `json:"packageId"`
		SizeGB            int                      `json:"sizeGb"`
		ImageDigest       string                   `json:"imageDigest"`
		Resources         WorkspaceLaunchResources `json:"resources"`
	}{launchRequestHash, input.Binding.Action, input.PackageID, input.SizeGB, input.WorkspaceImageDigest, input.Resources})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func TestWorkspaceLaunchExpectedBindingRequiresExactAuthoritativeRecordForFiveStages(t *testing.T) {
	service, store, _, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	tests := []struct {
		stage, action string
		resources     func(string) WorkspaceLaunchResources
		drift         func(*WorkspaceLaunchResources)
	}{
		{stage: "ensure_compute_allocation", action: "ensure_compute_allocation", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{ComputeAllocationID: "ca-alpha", ComputeBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.ComputeAllocationID = "ca-other" }},
		{stage: "storage", action: "ensure_storage", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{StorageID: "vol-alpha", StorageBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.StorageID = "vol-other" }},
		{stage: "attachment", action: "ensure_attachment", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{AttachmentID: "att-alpha", AttachmentBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.AttachmentID = "att-other" }},
		{stage: "secret", action: "ensure_gateway_secret", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{GatewaySecretRef: "secret-alpha", GatewaySecretVersion: "v1", GatewaySecretFingerprint: "sha256:" + strings.Repeat("d", 64), SecretBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.GatewaySecretRef = "secret-other" }},
		{stage: "runtime", action: "ensure_runtime", resources: func(ref string) WorkspaceLaunchResources {
			return WorkspaceLaunchResources{RuntimeID: "rt-alpha", RuntimeServiceName: "runtime-alpha", RuntimeURL: "http://runtime.invalid", RuntimeBindingRef: ref}
		}, drift: func(value *WorkspaceLaunchResources) { value.RuntimeID = "rt-other" }},
	}
	for _, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			previousInput := workspaceLaunchStageFixtureInput(preflight, image, launchHash, test.stage, test.action, WorkspaceLaunchResources{})
			previousInput.Binding.FabricOperationID = "launch-alpha:" + test.stage + "-previous"
			previousInput.Binding.IdempotencyKey = previousInput.Binding.FabricOperationID
			previousInput.Binding.RequestHash = workspaceLaunchStageRequestHash(previousInput, launchHash)
			operation, record, err := newWorkspaceLaunchStageOperation(previousInput, "tencent-tke", time.Now)
			if err != nil {
				t.Fatal(err)
			}
			record.Resources = test.resources(previousInput.Binding.FabricOperationID)
			setWorkspaceLaunchStageRecord(&operation, record)
			operation.Status, operation.FinishedAt = "succeeded", time.Now().UTC()
			if err := store.Append(context.Background(), operation); err != nil {
				t.Fatal(err)
			}

			input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, test.stage, test.action, record.Resources)
			input.Binding.FabricOperationID = "launch-alpha:" + test.stage + "-retry"
			input.Binding.IdempotencyKey = input.Binding.FabricOperationID
			input.Binding.ExpectedResourceBinding = previousInput.Binding.FabricOperationID
			input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), input); err != nil {
				t.Fatalf("exact expected binding rejected: %v", err)
			}

			driftedBinding := input
			driftedBinding.Binding.ExpectedResourceBinding = "launch-alpha:" + test.stage + "-other"
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), driftedBinding); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("expected binding drift error=%v", err)
			}
			driftedResource := input
			test.drift(&driftedResource.Resources)
			driftedResource.Binding.RequestHash = workspaceLaunchStageRequestHash(driftedResource, launchHash)
			if err := service.validateWorkspaceLaunchStageInput(context.Background(), driftedResource); !errors.Is(err, ErrLaunchStageBindingConflict) {
				t.Fatalf("expected resource identity drift error=%v", err)
			}
		})
	}
}
