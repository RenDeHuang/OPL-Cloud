package clients

import (
	"context"
	"errors"
)

const WorkspaceLaunchFabricSchemaVersion = 1

type FabricWorkspaceLaunchClient interface {
	PreflightWorkspaceLaunch(context.Context, WorkspaceLaunchPreflightInput) (WorkspaceLaunchPreflight, error)
	ReadWorkspaceLaunchStage(context.Context, WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error)
	EnsureWorkspaceLaunchStage(context.Context, WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error)
}

type FabricLegacyWorkspaceLaunchClient interface {
	ReadLegacyWorkspaceLaunchBinding(context.Context, LegacyWorkspaceLaunchBindingInput) (LegacyWorkspaceLaunchBindingResult, error)
}

type LegacyWorkspaceLaunchStageIdentity struct {
	Stage                 string `json:"stage"`
	ResourceRef           string `json:"resourceRef"`
	PersistedOperationRef string `json:"persistedOperationRef,omitempty"`
}

type LegacyWorkspaceLaunchStageReadback struct {
	Stage                    string `json:"stage"`
	State                    string `json:"state"`
	OperationRef             string `json:"operationRef,omitempty"`
	IdempotencyIdentity      string `json:"idempotencyIdentity,omitempty"`
	ResourceBindingRef       string `json:"resourceBindingRef,omitempty"`
	AuthoritativeReadbackRef string `json:"authoritativeReadbackRef,omitempty"`
}

type LegacyWorkspaceLaunchBindingInput struct {
	SchemaVersion           int                                  `json:"schemaVersion"`
	LaunchOperationID       string                               `json:"launchOperationId"`
	AccountID               string                               `json:"accountId"`
	WorkspaceID             string                               `json:"workspaceId"`
	RequestHash             string                               `json:"requestHash"`
	PackageID               string                               `json:"packageId"`
	SizeGB                  int                                  `json:"sizeGb"`
	WorkspaceImageDigest    string                               `json:"workspaceImageDigest"`
	WorkspaceAPIKeyID       int64                                `json:"workspaceApiKeyId"`
	WorkspaceKeyFingerprint string                               `json:"workspaceKeyFingerprint"`
	Stages                  []LegacyWorkspaceLaunchStageIdentity `json:"stages"`
}

type LegacyWorkspaceLaunchBindingResult struct {
	SchemaVersion       int                                  `json:"schemaVersion"`
	State               string                               `json:"state"`
	Reason              string                               `json:"reason"`
	LaunchOperationID   string                               `json:"launchOperationId"`
	AccountID           string                               `json:"accountId"`
	WorkspaceID         string                               `json:"workspaceId"`
	ProviderProfileRef  string                               `json:"providerProfileRef,omitempty"`
	PreflightBindingRef string                               `json:"preflightBindingRef,omitempty"`
	Resources           WorkspaceLaunchResources             `json:"resources,omitempty"`
	Stages              []LegacyWorkspaceLaunchStageReadback `json:"stages,omitempty"`
}

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

type WorkspaceLaunchStageBinding struct {
	SchemaVersion           int    `json:"schemaVersion"`
	LaunchOperationID       string `json:"launchOperationId"`
	AccountID               string `json:"accountId"`
	WorkspaceID             string `json:"workspaceId"`
	Stage                   string `json:"stage"`
	Action                  string `json:"action"`
	FabricOperationID       string `json:"fabricOperationId"`
	IdempotencyKey          string `json:"idempotencyKey"`
	RequestHash             string `json:"requestHash"`
	ExpectedResourceBinding string `json:"expectedResourceBinding"`
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

func (c *fabricHTTPClient) PreflightWorkspaceLaunch(ctx context.Context, input WorkspaceLaunchPreflightInput) (WorkspaceLaunchPreflight, error) {
	var result WorkspaceLaunchPreflight
	err := c.post(ctx, "/fabric/workspace-launches/preflight", input, "", &result)
	return result, err
}

func (c *fabricHTTPClient) ReadWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	var result WorkspaceLaunchStageResult
	err := c.post(ctx, "/fabric/workspace-launches/stages/read", input, "", &result)
	return result, err
}

func (c *fabricHTTPClient) EnsureWorkspaceLaunchStage(ctx context.Context, input WorkspaceLaunchStageInput) (WorkspaceLaunchStageResult, error) {
	if input.Binding.IdempotencyKey == "" {
		return WorkspaceLaunchStageResult{}, errors.New("workspace launch stage idempotency key is required")
	}
	var result WorkspaceLaunchStageResult
	err := c.postMutation(ctx, "/fabric/workspace-launches/stages/ensure", input, input.Binding.IdempotencyKey, fabricMutationScope{
		AccountID: input.Binding.AccountID, WorkspaceID: input.Binding.WorkspaceID,
		ResourceKind: "workspace_launch_stage", ResourceID: input.Binding.ExpectedResourceBinding, Action: input.Binding.Action,
	}, &result)
	return result, err
}

func (c *fabricHTTPClient) ReadLegacyWorkspaceLaunchBinding(ctx context.Context, input LegacyWorkspaceLaunchBindingInput) (LegacyWorkspaceLaunchBindingResult, error) {
	var result LegacyWorkspaceLaunchBindingResult
	err := c.post(ctx, "/fabric/workspace-launches/legacy-bindings/read", input, "", &result)
	return result, err
}
