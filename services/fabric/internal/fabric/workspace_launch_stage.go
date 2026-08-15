package fabric

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const WorkspaceLaunchFabricSchemaVersion = 1

const workspaceLaunchPreflightPayloadKey = "workspaceLaunchPreflight"
const workspaceLaunchStageRecordPayloadKey = "workspaceLaunchStageRecord"

var (
	ErrWorkspaceLaunchInputInvalid   = errors.New("workspace_launch_input_invalid")
	ErrWorkspaceLaunchUnavailable    = errors.New("workspace_launch_unavailable")
	ErrWorkspaceLaunchPending        = errors.New("workspace_launch_pending")
	ErrWorkspaceLaunchResourceAbsent = errors.New("workspace_launch_resource_absent")
)

type WorkspaceLaunchPreflightInput struct {
	SchemaVersion        int    `json:"schemaVersion"`
	LaunchOperationID    string `json:"launchOperationId"`
	AccountID            string `json:"accountId"`
	WorkspaceID          string `json:"workspaceId"`
	PackageID            string `json:"packageId"`
	SizeGB               int    `json:"sizeGb"`
	WorkspaceImageDigest string `json:"workspaceImageDigest"`
	RequestHash          string `json:"requestHash"`
}

type WorkspaceLaunchPreflight struct {
	SchemaVersion      int    `json:"schemaVersion"`
	Available          bool   `json:"available"`
	Reason             string `json:"reason"`
	LaunchOperationID  string `json:"launchOperationId"`
	RequestHash        string `json:"requestHash"`
	ProviderProfileRef string `json:"providerProfileRef"`
	BindingRef         string `json:"bindingRef"`
}

type workspaceLaunchPreflightAdmission struct {
	SchemaVersion      int                           `json:"schemaVersion"`
	Input              WorkspaceLaunchPreflightInput `json:"input"`
	ProviderProfileRef string                        `json:"providerProfileRef"`
	BindingRef         string                        `json:"bindingRef"`
}

type WorkspaceLaunchResources struct {
	ComputeAllocationID        string `json:"computeAllocationId,omitempty"`
	ComputeBindingRef          string `json:"computeBindingRef,omitempty"`
	StorageID                  string `json:"storageId,omitempty"`
	StorageBindingRef          string `json:"storageBindingRef,omitempty"`
	AttachmentID               string `json:"attachmentId,omitempty"`
	AttachmentBindingRef       string `json:"attachmentBindingRef,omitempty"`
	GatewaySecretRef           string `json:"gatewaySecretRef,omitempty"`
	GatewaySecretVersion       string `json:"gatewaySecretVersion,omitempty"`
	GatewaySecretFingerprint   string `json:"gatewaySecretFingerprint,omitempty"`
	SecretBindingRef           string `json:"secretBindingRef,omitempty"`
	RuntimeID                  string `json:"runtimeId,omitempty"`
	RuntimeServiceName         string `json:"runtimeServiceName,omitempty"`
	RuntimeUsername            string `json:"runtimeUsername,omitempty"`
	RuntimeURL                 string `json:"runtimeUrl,omitempty"`
	RuntimeCredentialStatus    string `json:"runtimeCredentialStatus,omitempty"`
	RuntimeCredentialVersion   string `json:"runtimeCredentialVersion,omitempty"`
	RuntimeCredentialSecretRef string `json:"runtimeCredentialSecretRef,omitempty"`
	RuntimeBindingRef          string `json:"runtimeBindingRef,omitempty"`
}

type WorkspaceLaunchGatewayCredential struct {
	KeyID int64  `json:"keyId"`
	Value string `json:"value"`
}

type WorkspaceLaunchStageInput struct {
	Binding              WorkspaceLaunchStageBinding       `json:"binding"`
	ProviderProfileRef   string                            `json:"providerProfileRef"`
	PreflightBindingRef  string                            `json:"preflightBindingRef"`
	PackageID            string                            `json:"packageId"`
	SizeGB               int                               `json:"sizeGb"`
	WorkspaceImageDigest string                            `json:"workspaceImageDigest"`
	Resources            WorkspaceLaunchResources          `json:"resources"`
	GatewayCredential    *WorkspaceLaunchGatewayCredential `json:"gatewayCredential,omitempty"`
}

type WorkspaceLaunchStageResult struct {
	SchemaVersion int                         `json:"schemaVersion"`
	State         string                      `json:"state"`
	Reason        string                      `json:"reason"`
	Binding       WorkspaceLaunchStageBinding `json:"binding"`
	Resources     WorkspaceLaunchResources    `json:"resources"`
}

type workspaceLaunchStageRecord struct {
	SchemaVersion       int                      `json:"schemaVersion"`
	ProviderProfileRef  string                   `json:"providerProfileRef"`
	PreflightBindingRef string                   `json:"preflightBindingRef"`
	RequestResources    WorkspaceLaunchResources `json:"requestResources"`
	Resources           WorkspaceLaunchResources `json:"resources"`
	GatewayKeyID        int64                    `json:"gatewayKeyId,omitempty"`
	ProviderState       json.RawMessage          `json:"providerState,omitempty"`
}

type persistedWorkspaceLaunchStageRecord struct {
	Record workspaceLaunchStageRecord `json:"record"`
	Digest string                     `json:"digest"`
}

type WorkspaceLaunchProviderRequest struct {
	Input   WorkspaceLaunchStageInput
	Current workspaceLaunchStageRecord
	Prior   map[string]workspaceLaunchStageRecord
}

type WorkspaceLaunchProviderResult struct {
	Resources     WorkspaceLaunchResources
	ProviderState json.RawMessage
}

type workspaceLaunchProvider interface {
	EnsureWorkspaceLaunchStage(context.Context, WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error)
	ReadWorkspaceLaunchStage(context.Context, WorkspaceLaunchProviderRequest) (WorkspaceLaunchProviderResult, error)
}

func validWorkspaceLaunchPreflightInput(input WorkspaceLaunchPreflightInput) bool {
	if input.SchemaVersion != WorkspaceLaunchFabricSchemaVersion || input.SizeGB < 10 || input.SizeGB%10 != 0 {
		return false
	}
	for _, value := range []string{input.LaunchOperationID, input.AccountID, input.WorkspaceID, input.PackageID, input.WorkspaceImageDigest, input.RequestHash} {
		if value == "" || value != strings.TrimSpace(value) {
			return false
		}
	}
	return validWorkspaceLaunchHash(input.RequestHash)
}

func (s *Service) PreflightWorkspaceLaunch(ctx context.Context, input WorkspaceLaunchPreflightInput) (WorkspaceLaunchPreflight, error) {
	if !validWorkspaceLaunchPreflightInput(input) {
		return WorkspaceLaunchPreflight{}, ErrWorkspaceLaunchInputInvalid
	}
	if _, ok := providerPlan(s.provider, input.PackageID); !ok || !s.provider.ValidateWorkspaceImageReference(input.WorkspaceImageDigest) {
		return WorkspaceLaunchPreflight{
			SchemaVersion: 1, Available: false, Reason: "provider_profile_unavailable", LaunchOperationID: input.LaunchOperationID,
			RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
		}, nil
	}
	if _, err := s.provider.Readiness(ctx); err != nil {
		return WorkspaceLaunchPreflight{
			SchemaVersion: 1, Available: false, Reason: "provider_unavailable", LaunchOperationID: input.LaunchOperationID,
			RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
		}, nil
	}
	result := WorkspaceLaunchPreflight{
		SchemaVersion: 1, Available: true, Reason: "none", LaunchOperationID: input.LaunchOperationID,
		RequestHash: input.RequestHash, ProviderProfileRef: s.provider.Descriptor().Name,
	}
	admission := workspaceLaunchPreflightAdmission{
		SchemaVersion: 1, Input: input, ProviderProfileRef: result.ProviderProfileRef,
	}
	admission.BindingRef = "fabric-preflight:" + hashInput(admission)
	result.BindingRef = admission.BindingRef
	if err := s.persistWorkspaceLaunchPreflight(ctx, admission); err != nil {
		return WorkspaceLaunchPreflight{}, err
	}
	return result, nil
}

func (s *Service) persistWorkspaceLaunchPreflight(ctx context.Context, admission workspaceLaunchPreflightAdmission) error {
	now := s.now()
	operation := newOperation(
		"admit_workspace_launch", "workspace_launch_preflight", admission.Input.LaunchOperationID,
		admission.Input.AccountID, admission.Input.WorkspaceID, admission.BindingRef, hashInput(admission), now,
	)
	operation.ID, operation.OperationID = admission.BindingRef, admission.BindingRef
	operation.Provider = admission.ProviderProfileRef
	operation.Status, operation.CreatedAt, operation.FinishedAt = "succeeded", now, now
	operation.RedactedProviderPayload = map[string]any{workspaceLaunchPreflightPayloadKey: admission}
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return err
	}
	if persisted, ok := decodeWorkspaceLaunchPreflight(stored); !ok || persisted != admission || (!claimed && stored.RequestHash != operation.RequestHash) {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func decodeWorkspaceLaunchPreflight(operation FabricOperation) (workspaceLaunchPreflightAdmission, bool) {
	value, ok := operation.RedactedProviderPayload[workspaceLaunchPreflightPayloadKey]
	if !ok {
		return workspaceLaunchPreflightAdmission{}, false
	}
	var admission workspaceLaunchPreflightAdmission
	body, err := json.Marshal(value)
	if err != nil || json.Unmarshal(body, &admission) != nil || admission.SchemaVersion != 1 ||
		!validWorkspaceLaunchPreflightInput(admission.Input) || admission.ProviderProfileRef == "" ||
		admission.BindingRef != "fabric-preflight:"+hashInput(workspaceLaunchPreflightAdmission{
			SchemaVersion: admission.SchemaVersion, Input: admission.Input, ProviderProfileRef: admission.ProviderProfileRef,
		}) {
		return workspaceLaunchPreflightAdmission{}, false
	}
	if operation.ID != admission.BindingRef || operation.OperationID != admission.BindingRef || operation.Action != "admit_workspace_launch" ||
		operation.ResourceKind != "workspace_launch_preflight" || operation.ResourceID != admission.Input.LaunchOperationID ||
		operation.AccountID != admission.Input.AccountID || operation.WorkspaceID != admission.Input.WorkspaceID ||
		operation.Provider != admission.ProviderProfileRef || operation.IdempotencyKey != admission.BindingRef ||
		operation.RequestHash != hashInput(admission) || operation.Status != "succeeded" {
		return workspaceLaunchPreflightAdmission{}, false
	}
	return admission, true
}

func (s *Service) workspaceLaunchPreflight(ctx context.Context, ref string) (workspaceLaunchPreflightAdmission, error) {
	operation, err := s.operations.Get(ctx, ref)
	if errors.Is(err, ErrOperationNotFound) {
		return workspaceLaunchPreflightAdmission{}, ErrLaunchStageBindingNotFound
	}
	if err != nil {
		return workspaceLaunchPreflightAdmission{}, err
	}
	admission, ok := decodeWorkspaceLaunchPreflight(operation)
	if !ok {
		return workspaceLaunchPreflightAdmission{}, ErrLaunchStageBindingConflict
	}
	return admission, nil
}

func (s *Service) validateWorkspaceLaunchStageInput(ctx context.Context, input WorkspaceLaunchStageInput) error {
	if !validWorkspaceLaunchStageBinding(input.Binding) || strings.TrimSpace(input.ProviderProfileRef) == "" ||
		strings.TrimSpace(input.PreflightBindingRef) == "" || strings.TrimSpace(input.PackageID) == "" ||
		input.SizeGB < 10 || input.SizeGB%10 != 0 || !s.provider.ValidateWorkspaceImageReference(input.WorkspaceImageDigest) {
		return ErrWorkspaceLaunchInputInvalid
	}
	if _, ok := providerPlan(s.provider, input.PackageID); !ok {
		return ErrWorkspaceLaunchInputInvalid
	}
	admission, err := s.workspaceLaunchPreflight(ctx, input.PreflightBindingRef)
	if err != nil {
		return err
	}
	preflight := admission.Input
	if admission.ProviderProfileRef != input.ProviderProfileRef || preflight.LaunchOperationID != input.Binding.LaunchOperationID ||
		preflight.AccountID != input.Binding.AccountID || preflight.WorkspaceID != input.Binding.WorkspaceID ||
		preflight.PackageID != input.PackageID || preflight.SizeGB != input.SizeGB ||
		preflight.WorkspaceImageDigest != input.WorkspaceImageDigest {
		return ErrLaunchStageBindingConflict
	}
	if admission.ProviderProfileRef != s.provider.Descriptor().Name || input.Binding.RequestHash != workspaceLaunchStageRequestHash(input, preflight.RequestHash) {
		return ErrLaunchStageBindingConflict
	}
	if err := s.validateWorkspaceLaunchExpectedBinding(ctx, input); err != nil {
		return err
	}
	return nil
}

func validWorkspaceLaunchHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func workspaceLaunchStageRequestHash(input WorkspaceLaunchStageInput, launchRequestHash string) string {
	return hashInput(struct {
		LaunchRequestHash string                   `json:"launchRequestHash"`
		Action            string                   `json:"action"`
		PackageID         string                   `json:"packageId"`
		SizeGB            int                      `json:"sizeGb"`
		ImageDigest       string                   `json:"imageDigest"`
		Resources         WorkspaceLaunchResources `json:"resources"`
	}{launchRequestHash, input.Binding.Action, input.PackageID, input.SizeGB, input.WorkspaceImageDigest, input.Resources})
}

func workspaceLaunchCurrentStageBinding(stage string, resources WorkspaceLaunchResources) string {
	return map[string]string{
		"ensure_compute_allocation": resources.ComputeBindingRef,
		"storage":                   resources.StorageBindingRef,
		"attachment":                resources.AttachmentBindingRef,
		"secret":                    resources.SecretBindingRef,
		"runtime":                   resources.RuntimeBindingRef,
	}[stage]
}

func (s *Service) validateWorkspaceLaunchExpectedBinding(ctx context.Context, input WorkspaceLaunchStageInput) error {
	expected := workspaceLaunchCurrentStageBinding(input.Binding.Stage, input.Resources)
	if input.Binding.ExpectedResourceBinding != expected {
		return ErrLaunchStageBindingConflict
	}
	if expected == "" {
		return nil
	}
	operation, err := s.operations.Get(ctx, expected)
	if err != nil {
		return ErrLaunchStageBindingConflict
	}
	persisted, ok := decodeLaunchStageBinding(operation)
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	if !ok || !recordOK || operation.Status != "succeeded" || persisted.LaunchOperationID != input.Binding.LaunchOperationID ||
		persisted.AccountID != input.Binding.AccountID || persisted.WorkspaceID != input.Binding.WorkspaceID ||
		persisted.Stage != input.Binding.Stage || operation.ID != expected || record.ProviderProfileRef != input.ProviderProfileRef ||
		!workspaceLaunchResourcesContain(input.Resources, record.Resources) {
		return ErrLaunchStageBindingConflict
	}
	return nil
}

func workspaceLaunchResourcesContain(actual, expected WorkspaceLaunchResources) bool {
	actualBody, _ := json.Marshal(actual)
	expectedBody, _ := json.Marshal(expected)
	actualFields, expectedFields := map[string]string{}, map[string]string{}
	if json.Unmarshal(actualBody, &actualFields) != nil || json.Unmarshal(expectedBody, &expectedFields) != nil {
		return false
	}
	for field, expectedValue := range expectedFields {
		if expectedValue != "" && actualFields[field] != expectedValue {
			return false
		}
	}
	return true
}

func setWorkspaceLaunchStageRecord(operation *FabricOperation, record workspaceLaunchStageRecord) {
	if operation.RedactedProviderPayload == nil {
		operation.RedactedProviderPayload = map[string]any{}
	}
	operation.RedactedProviderPayload[workspaceLaunchStageRecordPayloadKey] = persistedWorkspaceLaunchStageRecord{
		Record: record, Digest: hashInput(record),
	}
}

func decodeWorkspaceLaunchStageRecord(operation FabricOperation) (workspaceLaunchStageRecord, bool) {
	value, ok := operation.RedactedProviderPayload[workspaceLaunchStageRecordPayloadKey]
	if !ok {
		return workspaceLaunchStageRecord{}, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return workspaceLaunchStageRecord{}, false
	}
	var persisted persistedWorkspaceLaunchStageRecord
	if json.Unmarshal(body, &persisted) != nil || persisted.Record.SchemaVersion != 1 ||
		persisted.Digest == "" || persisted.Digest != hashInput(persisted.Record) ||
		persisted.Record.ProviderProfileRef == "" || persisted.Record.PreflightBindingRef == "" {
		return workspaceLaunchStageRecord{}, false
	}
	return persisted.Record, true
}

func workspaceLaunchStageCredentialKeyID(input WorkspaceLaunchStageInput) int64 {
	if input.GatewayCredential == nil {
		return 0
	}
	return input.GatewayCredential.KeyID
}

func newWorkspaceLaunchStageOperation(input WorkspaceLaunchStageInput, provider string, now func() time.Time) (FabricOperation, workspaceLaunchStageRecord, error) {
	binding := input.Binding
	record := workspaceLaunchStageRecord{
		SchemaVersion: 1, ProviderProfileRef: provider, PreflightBindingRef: input.PreflightBindingRef,
		RequestResources: input.Resources, Resources: input.Resources, GatewayKeyID: workspaceLaunchStageCredentialKeyID(input),
	}
	operation := newOperation(binding.Action, "workspace_launch_stage", binding.FabricOperationID, binding.AccountID, binding.WorkspaceID, binding.IdempotencyKey, binding.RequestHash, now())
	operation.ID, operation.OperationID = binding.FabricOperationID, binding.FabricOperationID
	operation.Provider, operation.Status, operation.CreatedAt = provider, "started", now()
	setWorkspaceLaunchStageRecord(&operation, record)
	if err := bindLaunchStageOperation(&operation, &binding); err != nil {
		return FabricOperation{}, workspaceLaunchStageRecord{}, err
	}
	return operation, record, nil
}

func workspaceLaunchStageOperationMatches(operation FabricOperation, input WorkspaceLaunchStageInput, provider string) (workspaceLaunchStageRecord, bool) {
	binding, bindingOK := decodeLaunchStageBinding(operation)
	record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
	if !bindingOK || !recordOK || binding != input.Binding || operation.ID != input.Binding.FabricOperationID ||
		operation.OperationID != input.Binding.FabricOperationID || operation.Action != input.Binding.Action ||
		operation.ResourceKind != "workspace_launch_stage" || operation.ResourceID != input.Binding.FabricOperationID ||
		operation.Provider != provider || record.ProviderProfileRef != provider || record.PreflightBindingRef != input.PreflightBindingRef ||
		record.RequestResources != input.Resources {
		return workspaceLaunchStageRecord{}, false
	}
	keyID := workspaceLaunchStageCredentialKeyID(input)
	if input.Binding.Stage == "secret" {
		if record.GatewayKeyID <= 0 || keyID > 0 && record.GatewayKeyID != keyID {
			return workspaceLaunchStageRecord{}, false
		}
	} else if record.GatewayKeyID != 0 {
		return workspaceLaunchStageRecord{}, false
	}
	return record, true
}

func workspaceLaunchRequiredPriorStages(stage string) []string {
	switch stage {
	case "storage":
		return []string{"ensure_compute_allocation"}
	case "attachment":
		return []string{"ensure_compute_allocation", "storage"}
	case "secret":
		return []string{"ensure_compute_allocation", "storage", "attachment"}
	case "runtime":
		return []string{"ensure_compute_allocation", "storage", "attachment", "secret"}
	default:
		return nil
	}
}

func workspaceLaunchStageBindingRef(stage string, resources WorkspaceLaunchResources) string {
	return map[string]string{
		"ensure_compute_allocation": resources.ComputeBindingRef,
		"storage":                   resources.StorageBindingRef,
		"attachment":                resources.AttachmentBindingRef,
		"secret":                    resources.SecretBindingRef,
		"runtime":                   resources.RuntimeBindingRef,
	}[stage]
}

func workspaceLaunchComputeID(binding WorkspaceLaunchStageBinding) string {
	return "ca_" + stableSuffix("create_compute_allocation", binding.IdempotencyKey)[:18]
}

func workspaceLaunchStorageID(binding WorkspaceLaunchStageBinding) string {
	return "vol_" + stableSuffix("create_storage_volume", binding.IdempotencyKey)[:16]
}

func workspaceLaunchAttachmentID(binding WorkspaceLaunchStageBinding) string {
	return "att_" + stableSuffix(binding.IdempotencyKey)[:18]
}

func (s *Service) WorkspaceLaunchProviderRequest(ctx context.Context, input WorkspaceLaunchStageInput, current workspaceLaunchStageRecord) (WorkspaceLaunchProviderRequest, error) {
	request := WorkspaceLaunchProviderRequest{Input: input, Current: current, Prior: map[string]workspaceLaunchStageRecord{}}
	for _, stage := range workspaceLaunchRequiredPriorStages(input.Binding.Stage) {
		ref := workspaceLaunchStageBindingRef(stage, input.Resources)
		if ref == "" {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		operation, err := s.operations.Get(ctx, ref)
		if err != nil {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		binding, bindingOK := decodeLaunchStageBinding(operation)
		record, recordOK := decodeWorkspaceLaunchStageRecord(operation)
		if !bindingOK || !recordOK || operation.Status != "succeeded" || operation.ID != ref || binding.Stage != stage ||
			binding.LaunchOperationID != input.Binding.LaunchOperationID || binding.AccountID != input.Binding.AccountID ||
			binding.WorkspaceID != input.Binding.WorkspaceID || record.ProviderProfileRef != input.ProviderProfileRef ||
			workspaceLaunchStageBindingRef(stage, record.Resources) != ref || !workspaceLaunchResourcesContain(input.Resources, record.Resources) {
			return WorkspaceLaunchProviderRequest{}, ErrLaunchStageBindingConflict
		}
		request.Prior[stage] = record
	}
	return request, nil
}

func validWorkspaceLaunchProviderResult(input WorkspaceLaunchStageInput, result WorkspaceLaunchProviderResult) bool {
	if !workspaceLaunchResourcesContain(result.Resources, input.Resources) || workspaceLaunchStageBindingRef(input.Binding.Stage, result.Resources) != input.Binding.FabricOperationID {
		return false
	}
	switch input.Binding.Stage {
	case "ensure_compute_allocation":
		return result.Resources.ComputeAllocationID != ""
	case "storage":
		return result.Resources.StorageID != ""
	case "attachment":
		return result.Resources.AttachmentID != ""
	case "secret":
		return result.Resources.GatewaySecretRef != "" && result.Resources.GatewaySecretVersion != "" &&
			result.Resources.GatewaySecretFingerprint == input.Resources.GatewaySecretFingerprint
	case "runtime":
		return result.Resources.RuntimeID != "" && result.Resources.RuntimeServiceName != "" && result.Resources.RuntimeURL != ""
	default:
		return false
	}
}

func pendingWorkspaceLaunchStageResult(input WorkspaceLaunchStageInput, reason string) WorkspaceLaunchStageResult {
	if reason == "" {
		reason = "operation_pending"
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "pending", Reason: reason, Binding: input.Binding, Resources: input.Resources}
}

func observedWorkspaceLaunchStageResult(input WorkspaceLaunchStageInput, state, reason string) WorkspaceLaunchStageResult {
	return WorkspaceLaunchStageResult{SchemaVersion: WorkspaceLaunchFabricSchemaVersion, State: state, Reason: reason, Binding: input.Binding, Resources: input.Resources}
}

func (s *Service) persistWorkspaceLaunchStageResult(ctx context.Context, current FabricOperation, record workspaceLaunchStageRecord, result WorkspaceLaunchProviderResult) error {
	next := current
	next.Status, next.ErrorCode, next.Retryable, next.FinishedAt = "succeeded", "", false, s.now()
	record.Resources, record.ProviderState = result.Resources, append(json.RawMessage(nil), result.ProviderState...)
	setWorkspaceLaunchStageRecord(&next, record)
	if current.Status == "started" {
		return s.operations.SaveRuntime(ctx, next)
	}
	converger, ok := s.operations.(runtimeReadbackConverger)
	if !ok {
		return ErrRuntimeOperationNotCurrent
	}
	return converger.ConvergeRuntimeReadback(ctx, current, next)
}

func (s *Service) failWorkspaceLaunchStage(ctx context.Context, current FabricOperation, err error) {
	if current.Status != "started" {
		return
	}
	next := current
	next.Status, next.ErrorCode, next.Retryable, next.FinishedAt = "failed", errorCode(err), false, s.now()
	_ = s.operations.SaveRuntime(ctx, next)
}

func (s *Service) EnsureWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	if err := s.validateWorkspaceLaunchStageInput(ctx, input); err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	if input.Binding.Stage == "secret" && (input.GatewayCredential == nil || input.GatewayCredential.KeyID <= 0 || strings.TrimSpace(input.GatewayCredential.Value) == "") {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchInputInvalid
	}
	stageProvider, ok := s.provider.(workspaceLaunchProvider)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	operation, record, err := newWorkspaceLaunchStageOperation(input, s.provider.Descriptor().Name, s.now)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	stored, claimed, err := s.operations.ClaimRuntime(ctx, operation)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	record, ok = workspaceLaunchStageOperationMatches(stored, input, s.provider.Descriptor().Name)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	if stored.Status == "succeeded" {
		return s.readWorkspaceLaunchStage(ctx, input, stored, record)
	}
	if !claimed {
		observed, readErr := s.readWorkspaceLaunchStage(ctx, input, stored, record)
		if readErr != nil || observed.State != "absent" {
			return observed, readErr
		}
	}
	request, err := s.WorkspaceLaunchProviderRequest(ctx, input, record)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	providerResult, err := stageProvider.EnsureWorkspaceLaunchStage(s.providerMutationContext(ctx, stored), request)
	if errors.Is(err, ErrWorkspaceLaunchPending) {
		return pendingWorkspaceLaunchStageResult(input, stored.ErrorCode), nil
	}
	if err != nil {
		s.failWorkspaceLaunchStage(ctx, stored, err)
		return WorkspaceLaunchStageResult{}, err
	}
	if !validWorkspaceLaunchProviderResult(input, providerResult) {
		err = ErrWorkspaceLaunchUnavailable
		s.failWorkspaceLaunchStage(ctx, stored, err)
		return WorkspaceLaunchStageResult{}, err
	}
	if err := s.persistWorkspaceLaunchStageResult(ctx, stored, record, providerResult); err != nil {
		latest, getErr := s.operations.Get(ctx, input.Binding.FabricOperationID)
		if getErr != nil || latest.Status != "succeeded" {
			return WorkspaceLaunchStageResult{}, err
		}
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "ready", Reason: "none", Binding: input.Binding, Resources: providerResult.Resources}, nil
}

func (s *Service) ReadWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	if err := s.validateWorkspaceLaunchStageInput(ctx, input); err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	operation, err := s.operations.Get(ctx, input.Binding.FabricOperationID)
	if errors.Is(err, ErrOperationNotFound) {
		return observedWorkspaceLaunchStageResult(input, "absent", "no_stage_record"), nil
	}
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	record, ok := workspaceLaunchStageOperationMatches(operation, input, s.provider.Descriptor().Name)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	return s.readWorkspaceLaunchStage(ctx, input, operation, record)
}

func (s *Service) readWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput, operation FabricOperation, record workspaceLaunchStageRecord) (WorkspaceLaunchStageResult, error) {
	stageProvider, ok := s.provider.(workspaceLaunchProvider)
	if !ok {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	request, err := s.WorkspaceLaunchProviderRequest(ctx, input, record)
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	providerResult, err := stageProvider.ReadWorkspaceLaunchStage(s.providerReadContext(ctx, operation), request)
	if errors.Is(err, ErrWorkspaceLaunchResourceAbsent) {
		if operation.Status == "started" {
			return observedWorkspaceLaunchStageResult(input, "absent", "started_no_resource"), nil
		}
		if operation.Status == "failed" {
			return observedWorkspaceLaunchStageResult(input, "absent", "failed_no_resource"), nil
		}
		return observedWorkspaceLaunchStageResult(input, "unknown", "resource_absence_status_conflict"), nil
	}
	if errors.Is(err, ErrWorkspaceLaunchPending) {
		if operation.Status == "started" {
			return pendingWorkspaceLaunchStageResult(input, "provider_provisioning"), nil
		}
		return observedWorkspaceLaunchStageResult(input, "unknown", "failed_no_resource_unproven"), nil
	}
	if err != nil {
		return WorkspaceLaunchStageResult{}, err
	}
	if !validWorkspaceLaunchProviderResult(input, providerResult) {
		return WorkspaceLaunchStageResult{}, ErrWorkspaceLaunchUnavailable
	}
	if operation.Status != "succeeded" {
		if err := s.persistWorkspaceLaunchStageResult(ctx, operation, record, providerResult); err != nil {
			return WorkspaceLaunchStageResult{}, err
		}
	} else if !workspaceLaunchResourcesContain(providerResult.Resources, record.Resources) || !workspaceLaunchResourcesContain(record.Resources, providerResult.Resources) {
		return WorkspaceLaunchStageResult{}, ErrLaunchStageBindingConflict
	}
	return WorkspaceLaunchStageResult{SchemaVersion: 1, State: "ready", Reason: "none", Binding: input.Binding, Resources: providerResult.Resources}, nil
}
