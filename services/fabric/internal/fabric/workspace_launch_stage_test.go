package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type workspaceLaunchStageHashGoldenVector struct {
	Stage   string `json:"stage"`
	Payload struct {
		LaunchRequestHash string                   `json:"launchRequestHash"`
		Action            string                   `json:"action"`
		PackageID         string                   `json:"packageId"`
		SizeGB            int                      `json:"sizeGb"`
		ImageDigest       string                   `json:"imageDigest"`
		Resources         WorkspaceLaunchResources `json:"resources"`
	} `json:"payload"`
	SHA256 string `json:"sha256"`
}

func workspaceLaunchStageHashGoldenVectors(t *testing.T) []workspaceLaunchStageHashGoldenVector {
	t.Helper()
	raw, err := os.ReadFile("../../../../packages/contracts/opl-cloud-fabric-launch-binding-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		StageRequestHash struct {
			GoldenVectors []workspaceLaunchStageHashGoldenVector `json:"goldenVectors"`
		} `json:"stageRequestHash"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.StageRequestHash.GoldenVectors) != 5 {
		t.Fatalf("golden vectors=%d, want 5", len(contract.StageRequestHash.GoldenVectors))
	}
	return contract.StageRequestHash.GoldenVectors
}

type workspaceLaunchRecordingProvider struct {
	testProvider
	resolveCalls int
	ensureCalls  int
	readCalls    int
	resolvedPlan json.RawMessage
	requestPlan  json.RawMessage
	ensureErr    error
	readErr      error
	ensureResult *WorkspaceLaunchProviderResult
	mutateOnRead bool
}

func (p *workspaceLaunchRecordingProvider) ResolveWorkspacePlan(_ context.Context, input WorkspaceLaunchPlanInput) (json.RawMessage, error) {
	p.resolveCalls++
	if len(p.resolvedPlan) != 0 {
		return p.resolvedPlan, nil
	}
	return json.Marshal(map[string]any{
		"compute": map[string]any{"cpu": 2, "memoryGb": 4},
		"storage": map[string]any{"sizeGb": input.SizeGB},
	})
}

func (p *workspaceLaunchRecordingProvider) EnsureWorkspaceLaunchStage(_ context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	p.ensureCalls++
	p.requestPlan = append(json.RawMessage(nil), request.ProviderPlan...)
	if p.ensureErr != nil {
		return WorkspaceLaunchProviderResult{}, p.ensureErr
	}
	if p.ensureResult != nil {
		return *p.ensureResult, nil
	}
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func (p *workspaceLaunchRecordingProvider) ReadWorkspaceLaunchStage(ctx context.Context, request WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error) {
	p.readCalls++
	if p.mutateOnRead {
		_, err := beginProviderMutation(ctx, "read_must_not_mutate", "workspace_launch_stage", request.Input.Binding.FabricOperationID, "")
		return WorkspaceLaunchProviderResult{}, err
	}
	if p.readErr != nil {
		return WorkspaceLaunchProviderResult{}, p.readErr
	}
	if p.ensureResult != nil {
		return *p.ensureResult, nil
	}
	return WorkspaceLaunchProviderResult{Resources: request.Input.Resources}, nil
}

func TestWorkspaceLaunchStageReadContextRejectsProviderMutation(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{})
	operation, _, err := newWorkspaceLaunchStageOperation(input, "tencent-tke", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	before, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	provider.mutateOnRead = true
	if _, err := service.ReadWorkspaceLaunchStage(context.Background(), input); err == nil || err.Error() != "provider_mutation_forbidden_in_read" {
		t.Fatalf("provider read mutation error=%v", err)
	}
	after, err := store.List(context.Background())
	if err != nil || len(after) != len(before) {
		t.Fatalf("provider read changed operations before=%d after=%d err=%v", len(before), len(after), err)
	}
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
	if err != nil || !preflight.Available || preflight.ProviderBindingRef == "" || !validWorkspaceLaunchHash(preflight.SpecDigest) {
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
		ProviderProfileRef: "tencent-tke", ProviderBindingRef: preflight.ProviderBindingRef, SpecDigest: preflight.SpecDigest,
		PackageID: "basic", SizeGB: 10, WorkspaceImageDigest: image, Resources: resources,
	}
	input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
	return input
}

func TestWorkspaceLaunchPreflightIsDurableAndPointReadBeforeStageWrite(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	readback, readErr := service.ReadWorkspaceLaunchPreflight(context.Background(), WorkspaceLaunchPreflightReadInput{ProviderBindingRef: preflight.ProviderBindingRef})
	if readErr != nil || readback.SchemaVersion != 1 || readback.LaunchOperationID != "launch-alpha" || readback.AccountID != "acct-alpha" ||
		readback.WorkspaceID != "ws-alpha" || readback.PackageID != "basic" || readback.SizeGB != 10 || readback.WorkspaceImageDigest != image ||
		readback.RequestHash != launchHash || readback.ProviderProfileRef != "tencent-tke" || readback.ProviderBindingRef != preflight.ProviderBindingRef ||
		readback.SpecDigest != preflight.SpecDigest || provider.resolveCalls != 1 || provider.readCalls != 0 || provider.ensureCalls != 0 {
		t.Fatalf("preflight readback=%#v provider resolves=%d reads=%d writes=%d err=%v", readback, provider.resolveCalls, provider.readCalls, provider.ensureCalls, readErr)
	}
	operation, err := store.Get(context.Background(), preflight.ProviderBindingRef)
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if err != nil || !ok || operation.Status != "succeeded" || admission.ProviderBindingRef != preflight.ProviderBindingRef || admission.SpecDigest != preflight.SpecDigest ||
		admission.Input.LaunchOperationID != "launch-alpha" || admission.Input.AccountID != "acct-alpha" ||
		admission.Input.WorkspaceID != "ws-alpha" || admission.Input.PackageID != "basic" || admission.Input.SizeGB != 10 ||
		admission.Input.WorkspaceImageDigest != image || admission.Input.RequestHash != launchHash || admission.ProviderProfileRef != "tencent-tke" {
		t.Fatalf("operation=%#v admission=%#v/%v err=%v", operation, admission, ok, err)
	}

	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "storage", "ensure_storage", WorkspaceLaunchResources{})
	input.ProviderBindingRef = "fabric-provider-binding:" + strings.Repeat("0", 64)
	if _, err := service.EnsureWorkspaceLaunchStage(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingNotFound) {
		t.Fatalf("forged preflight error=%v", err)
	}
	operations, err := store.List(context.Background())
	if err != nil || len(operations) != 1 || operations[0].ID != preflight.ProviderBindingRef || provider.ensureCalls != 0 {
		t.Fatalf("forged preflight crossed stage write: operations=%#v providerCalls=%d err=%v", operations, provider.ensureCalls, err)
	}
}

func TestWorkspaceLaunchPreflightReadbackStrictlyValidatesPersistedBinding(t *testing.T) {
	service, store, _, preflight, _, _ := workspaceLaunchStageFixture(t)
	before, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadWorkspaceLaunchPreflight(context.Background(), WorkspaceLaunchPreflightReadInput{}); !errors.Is(err, ErrWorkspaceLaunchInputInvalid) {
		t.Fatalf("empty binding error=%v", err)
	}
	if _, err := service.ReadWorkspaceLaunchPreflight(context.Background(), WorkspaceLaunchPreflightReadInput{ProviderBindingRef: "not-a-binding"}); !errors.Is(err, ErrWorkspaceLaunchInputInvalid) {
		t.Fatalf("malformed binding error=%v", err)
	}
	if _, err := service.ReadWorkspaceLaunchPreflight(context.Background(), WorkspaceLaunchPreflightReadInput{ProviderBindingRef: "fabric-provider-binding:" + strings.Repeat("0", 64)}); !errors.Is(err, ErrLaunchStageBindingNotFound) {
		t.Fatalf("missing binding error=%v", err)
	}
	operation, err := store.Get(context.Background(), preflight.ProviderBindingRef)
	if err != nil {
		t.Fatal(err)
	}
	operation.RequestHash = strings.Repeat("0", 64)
	store.mu.Lock()
	for index := range store.operation {
		if store.operation[index].ID == operation.ID {
			store.operation[index] = operation
		}
	}
	store.mu.Unlock()
	if _, err := service.ReadWorkspaceLaunchPreflight(context.Background(), WorkspaceLaunchPreflightReadInput{ProviderBindingRef: preflight.ProviderBindingRef}); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("corrupt binding error=%v", err)
	}
	after, err := store.List(context.Background())
	if err != nil || len(after) != len(before) {
		t.Fatalf("readback mutated operations before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestWorkspaceLaunchPreflightPersistsCanonicalProviderPlanAcrossProfileDrift(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	operation, err := store.Get(context.Background(), preflight.ProviderBindingRef)
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if err != nil || !ok {
		t.Fatalf("provider binding read=%#v/%v err=%v", admission, ok, err)
	}
	wantPlan := `{"packageId":"basic","providerProfileRef":"tencent-tke","schemaVersion":1,"spec":{"compute":{"cpu":2,"memoryGb":4},"storage":{"sizeGb":10}}}`
	if string(admission.CanonicalProviderPlan) != wantPlan || admission.SpecDigest != providerPlanDigest(admission.CanonicalProviderPlan) {
		t.Fatalf("provider plan=%s digest=%s", admission.CanonicalProviderPlan, admission.SpecDigest)
	}

	replayed, replayErr := service.PreflightWorkspaceLaunch(context.Background(), admission.Input)
	if replayErr != nil || replayed.ProviderBindingRef != preflight.ProviderBindingRef || replayed.SpecDigest != preflight.SpecDigest {
		t.Fatalf("preflight replay=%#v err=%v", replayed, replayErr)
	}

	provider.resolvedPlan = json.RawMessage(`{"compute":{"cpu":8,"memoryGb":16},"packageId":"basic","storage":{"sizeGb":100}}`)
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{})
	current := workspaceLaunchStageRecord{SchemaVersion: workspaceLaunchStageRecordSchemaVersion, ProviderProfileRef: input.ProviderProfileRef, ProviderBindingRef: input.ProviderBindingRef, SpecDigest: input.SpecDigest}
	request, requestErr := service.WorkspaceLaunchProviderRequest(context.Background(), input, current)
	if requestErr != nil || string(request.ProviderPlan) != wantPlan {
		t.Fatalf("stage plan=%s err=%v", request.ProviderPlan, requestErr)
	}

	input.SpecDigest = strings.Repeat("0", 64)
	input.Binding.RequestHash = workspaceLaunchStageRequestHash(input, launchHash)
	if err := service.validateWorkspaceLaunchStageInput(context.Background(), input); !errors.Is(err, ErrLaunchStageBindingConflict) {
		t.Fatalf("spec digest drift error=%v", err)
	}
}

func TestWorkspaceLaunchStageReadbackOwnsReplayDisposition(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{})

	absent, err := service.ReadWorkspaceLaunchStage(context.Background(), input)
	if err != nil || absent.State != "absent" || absent.Reason != "no_stage_record" || absent.Binding != input.Binding || provider.readCalls != 0 {
		t.Fatalf("missing record disposition=%#v reads=%d err=%v", absent, provider.readCalls, err)
	}

	for _, tc := range []struct {
		name, operationStatus, wantState, wantReason string
		readErr                                      error
	}{
		{name: "started provisioning", operationStatus: "started", readErr: ErrWorkspaceLaunchPending, wantState: "pending", wantReason: "provider_provisioning"},
		{name: "failed no resource", operationStatus: "failed", readErr: ErrWorkspaceLaunchResourceAbsent, wantState: "absent", wantReason: "failed_no_resource"},
		{name: "failed absence unproven", operationStatus: "failed", readErr: ErrWorkspaceLaunchPending, wantState: "unknown", wantReason: "failed_no_resource_unproven"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stageInput := input
			stageInput.Binding.FabricOperationID += "-" + strings.ReplaceAll(tc.name, " ", "-")
			stageInput.Binding.IdempotencyKey = stageInput.Binding.FabricOperationID
			stageInput.Binding.RequestHash = workspaceLaunchStageRequestHash(stageInput, launchHash)
			operation, _, buildErr := newWorkspaceLaunchStageOperation(stageInput, "tencent-tke", time.Now)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			operation.Status = tc.operationStatus
			if tc.operationStatus == "failed" {
				operation.FinishedAt = time.Now().UTC()
			}
			if appendErr := store.Append(context.Background(), operation); appendErr != nil {
				t.Fatal(appendErr)
			}
			provider.readErr = tc.readErr
			got, readErr := service.ReadWorkspaceLaunchStage(context.Background(), stageInput)
			if readErr != nil || got.State != tc.wantState || got.Reason != tc.wantReason || got.Binding != stageInput.Binding {
				t.Fatalf("disposition=%#v err=%v", got, readErr)
			}
		})
	}
}

func TestWorkspaceLaunchEnsureExistingRecordReadsBeforeIdempotentReplay(t *testing.T) {
	service, store, provider, preflight, image, launchHash := workspaceLaunchStageFixture(t)
	input := workspaceLaunchStageFixtureInput(preflight, image, launchHash, "ensure_compute_allocation", "ensure_compute_allocation", WorkspaceLaunchResources{})
	operation, _, err := newWorkspaceLaunchStageOperation(input, "tencent-tke", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	operation.Status, operation.FinishedAt = "failed", time.Now().UTC()
	if err := store.Append(context.Background(), operation); err != nil {
		t.Fatal(err)
	}

	provider.readErr = ErrWorkspaceLaunchPending
	blocked, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || blocked.State != "unknown" || blocked.Reason != "failed_no_resource_unproven" || provider.ensureCalls != 0 {
		t.Fatalf("unproven failed record reached mutation: result=%#v ensures=%d err=%v", blocked, provider.ensureCalls, err)
	}

	provider.readErr = ErrWorkspaceLaunchResourceAbsent
	provider.ensureResult = &WorkspaceLaunchProviderResult{Resources: WorkspaceLaunchResources{
		ComputeAllocationID: workspaceLaunchComputeID(input.Binding), ComputeBindingRef: input.Binding.FabricOperationID,
	}}
	replayed, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || replayed.State != "ready" || replayed.Resources.ComputeBindingRef != input.Binding.FabricOperationID || provider.ensureCalls != 1 {
		t.Fatalf("typed absent did not permit one same-key replay: result=%#v ensures=%d err=%v", replayed, provider.ensureCalls, err)
	}
	provider.readErr = nil
	again, err := service.EnsureWorkspaceLaunchStage(context.Background(), input)
	if err != nil || again.State != "ready" || provider.ensureCalls != 1 {
		t.Fatalf("ready replay repeated provider mutation: result=%#v ensures=%d err=%v", again, provider.ensureCalls, err)
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

func TestWorkspaceLaunchStageRequestHashMatchesOwnerGoldenVectors(t *testing.T) {
	for _, vector := range workspaceLaunchStageHashGoldenVectors(t) {
		t.Run(vector.Stage, func(t *testing.T) {
			input := WorkspaceLaunchStageInput{
				Binding:              WorkspaceLaunchStageBinding{Stage: vector.Stage, Action: vector.Payload.Action},
				PackageID:            vector.Payload.PackageID,
				SizeGB:               vector.Payload.SizeGB,
				WorkspaceImageDigest: vector.Payload.ImageDigest,
				Resources:            vector.Payload.Resources,
			}
			if got := workspaceLaunchStageRequestHash(input, vector.Payload.LaunchRequestHash); got != vector.SHA256 {
				t.Fatalf("workspaceLaunchStageRequestHash()=%s, owner golden=%s", got, vector.SHA256)
			}
		})
	}
}

func TestWorkspaceLaunchStageRejectsRequestHashAndResourceDrift(t *testing.T) {
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
