package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"opl-cloud/services/control-plane/internal/clients"
	"opl-cloud/services/control-plane/internal/domain"
)

type Service struct {
	ledger  clients.LedgerClient
	fabric  clients.FabricClient
	sub2API clients.Sub2APIClient
}

var (
	ErrWorkspaceRuntimeIdentityMismatch = errors.New("workspace_runtime_identity_mismatch")
	ErrWorkspaceRuntimeReadbackInvalid  = errors.New("workspace_runtime_readback_invalid")
)

type CreateWorkspaceInput struct {
	WorkspaceID         string `json:"workspaceId"`
	AccountID           string `json:"accountId"`
	Sub2APIUserID       int64  `json:"-"`
	WorkspaceAPIKeyID   int64  `json:"workspaceApiKeyId"`
	WorkspaceAPIKeyName string `json:"-"`
	OwnerID             string `json:"ownerId"`
	Name                string `json:"name"`
	PackageID           string `json:"packageId"`
	AttachmentID        string `json:"attachmentId"`
	ComputeID           string `json:"computeAllocationId"`
	VolumeID            string `json:"storageId"`
	GatewaySecretRef    string `json:"-"`
}

type RotateWorkspaceCredentialInput struct {
	WorkspaceID      string
	AccountID        string
	OwnerID          string
	ComputeID        string
	VolumeID         string
	GatewaySecretRef string
}

type ReconciliationInput struct {
	Report map[string]any `json:"report"`
}

type StorageAttachmentInput struct {
	WorkspaceID string `json:"workspaceId"`
	ComputeID   string `json:"computeId"`
	VolumeID    string `json:"volumeId"`
}

func NewService(ledger clients.LedgerClient, fabric clients.FabricClient, sub2API ...clients.Sub2APIClient) *Service {
	service := &Service{ledger: ledger, fabric: fabric}
	if len(sub2API) > 0 {
		service.sub2API = sub2API[0]
	}
	return service
}

func (s *Service) Sub2APIWorkspaceKey(ctx context.Context, userID int64) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIWorkspaceKeyClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_workspace_key_unavailable")
	}
	return client.WorkspaceKey(ctx, userID)
}

func (s *Service) GatewayKeys(ctx context.Context, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIKeyListClient)
	if !ok {
		return nil, errors.New("sub2api_key_list_unavailable")
	}
	return client.Keys(ctx, userID)
}

func (s *Service) Sub2APIWorkspaceKeyByID(ctx context.Context, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	if userID <= 0 || keyID <= 0 {
		return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyMissing
	}
	keys, err := s.GatewayKeys(ctx, userID)
	if err != nil {
		return clients.Sub2APIWorkspaceKey{}, err
	}
	for _, key := range keys {
		if key.ID == keyID && key.UserID == userID {
			return key, nil
		}
	}
	return clients.Sub2APIWorkspaceKey{}, clients.ErrSub2APIWorkspaceKeyMissing
}

func (s *Service) GatewayUserKeys(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyReadClient)
	if !ok {
		return nil, errors.New("sub2api_user_key_unavailable")
	}
	return client.UserKeys(ctx, credential, userID)
}

func (s *Service) GatewayUserKeyPage(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64, query clients.Sub2APIKeyPageQuery) (clients.Sub2APIKeyPage, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyPageClient)
	if !ok {
		return clients.Sub2APIKeyPage{}, errors.New("sub2api_user_key_page_unavailable")
	}
	return client.UserKeyPage(ctx, credential, userID, query)
}

func (s *Service) GatewayUserGroups(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64) ([]clients.Sub2APIGroup, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserGroupClient)
	if !ok {
		return nil, errors.New("sub2api_user_groups_unavailable")
	}
	return client.UserGroups(ctx, credential, userID)
}

func (s *Service) GatewayPublicEndpoint() (string, error) {
	client, ok := s.sub2API.(clients.Sub2APIPublicEndpointClient)
	if !ok || strings.TrimSpace(client.PublicEndpoint()) == "" {
		return "", errors.New("sub2api_public_endpoint_unavailable")
	}
	return client.PublicEndpoint(), nil
}

func (s *Service) GatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyReadClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.UserKey(ctx, credential, userID, keyID)
}

func (s *Service) CreateGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID int64, input clients.Sub2APICreateKeyInput, idempotencyKey string) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.CreateUserKey(ctx, credential, userID, input, idempotencyKey)
}

func (s *Service) UpdateGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, input clients.Sub2APIUpdateKeyInput) (clients.Sub2APIWorkspaceKey, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return clients.Sub2APIWorkspaceKey{}, errors.New("sub2api_user_key_unavailable")
	}
	return client.UpdateUserKey(ctx, credential, userID, keyID, input)
}

func (s *Service) DeleteGatewayUserKey(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64) error {
	client, ok := s.sub2API.(clients.Sub2APIUserKeyMutationClient)
	if !ok {
		return errors.New("sub2api_user_key_unavailable")
	}
	return client.DeleteUserKey(ctx, credential, userID, keyID)
}

func (s *Service) Sub2APIUser(ctx context.Context, userID int64) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIUserReadClient)
	if !ok {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_user_read_unavailable")
	}
	identity, err := client.User(ctx, userID)
	if err != nil {
		return clients.Sub2APIIdentity{}, err
	}
	identity.Email = strings.ToLower(strings.TrimSpace(identity.Email))
	if identity.ID != userID || identity.Email == "" || (identity.Status != "active" && identity.Status != "disabled") {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_user_read_invalid")
	}
	return identity, nil
}

func (s *Service) Sub2APIAdminUsers(ctx context.Context, query clients.Sub2APIUserPageQuery) (clients.Sub2APIUserPage, error) {
	client, ok := s.sub2API.(clients.Sub2APIAdminUsersClient)
	if !ok {
		return clients.Sub2APIUserPage{}, errors.New("sub2api_admin_users_unavailable")
	}
	return client.AdminUsers(ctx, query)
}

func (s *Service) Sub2APIAdminUser(ctx context.Context, userID int64) (clients.Sub2APIUser, error) {
	client, ok := s.sub2API.(clients.Sub2APIAdminUserClient)
	if !ok {
		return clients.Sub2APIUser{}, errors.New("sub2api_admin_user_unavailable")
	}
	user, err := client.AdminUser(ctx, userID)
	if err != nil {
		return clients.Sub2APIUser{}, err
	}
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	if user.ID != userID || user.Email == "" || user.Status != "active" && user.Status != "disabled" || user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() || user.UpdatedAt.Before(user.CreatedAt) {
		return clients.Sub2APIUser{}, errors.New("sub2api_admin_user_invalid")
	}
	return user, nil
}

func (s *Service) Sub2APIBatchUsersUsage(ctx context.Context, userIDs []int64) (map[int64]clients.Sub2APIBatchUserUsage, error) {
	client, ok := s.sub2API.(clients.Sub2APIBatchUsersUsageClient)
	if !ok {
		return nil, errors.New("sub2api_batch_users_usage_unavailable")
	}
	return client.BatchUsersUsage(ctx, userIDs)
}

func (s *Service) Sub2APIBatchKeysUsage(ctx context.Context, keyIDs []int64) (map[int64]clients.Sub2APIBatchKeyUsage, error) {
	client, ok := s.sub2API.(clients.Sub2APIBatchKeysUsageClient)
	if !ok {
		return nil, errors.New("sub2api_batch_keys_usage_unavailable")
	}
	return client.BatchKeysUsage(ctx, keyIDs)
}

func (s *Service) Sub2APIVersion(ctx context.Context) (string, error) {
	if s.sub2API == nil {
		return "", errors.New("sub2api_unavailable")
	}
	return s.sub2API.Version(ctx)
}

func (s *Service) ResolveOrCreateSub2APIUser(ctx context.Context, email, password string) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIIdentityClient)
	if !ok {
		return clients.Sub2APIIdentity{}, errors.New("sub2api_identity_unavailable")
	}
	return client.ResolveOrCreateUser(ctx, email, password)
}

func (s *Service) AuthenticateSub2APIUser(ctx context.Context, email, password string) (clients.Sub2APIUserAuthentication, error) {
	client, ok := s.sub2API.(clients.Sub2APIIdentityClient)
	if !ok {
		return clients.Sub2APIUserAuthentication{}, clients.ErrSub2APIAuthUnavailable
	}
	return client.AuthenticateUser(ctx, email, password)
}

func (s *Service) Sub2APIAdminIdentity(ctx context.Context) (clients.Sub2APIIdentity, error) {
	client, ok := s.sub2API.(clients.Sub2APIAdminIdentityClient)
	if !ok {
		return clients.Sub2APIIdentity{}, clients.ErrSub2APIAuthUnavailable
	}
	return client.AdminIdentity(ctx)
}

func (s *Service) GatewayKeyUsage(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, page, pageSize int) (clients.Sub2APIUsagePage, error) {
	if _, err := s.GatewayUserKey(ctx, credential, userID, keyID); err != nil {
		return clients.Sub2APIUsagePage{}, err
	}
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsagePage{}, errors.New("sub2api_usage_unavailable")
	}
	return client.Usage(ctx, clients.Sub2APIUsageQuery{UserID: userID, APIKeyID: keyID, Page: page, PageSize: pageSize})
}

func (s *Service) GatewayKeyUsageStats(ctx context.Context, credential clients.SessionDelegatedCredential, userID, keyID int64, period string) (clients.Sub2APIUsageStats, error) {
	if _, err := s.GatewayUserKey(ctx, credential, userID, keyID); err != nil {
		return clients.Sub2APIUsageStats{}, err
	}
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsageStats{}, errors.New("sub2api_usage_unavailable")
	}
	return client.UsageStats(ctx, clients.Sub2APIUsageStatsQuery{UserID: userID, APIKeyID: keyID, Period: period})
}

func (s *Service) GatewayAccountUsageStats(ctx context.Context, userID int64, period string) (clients.Sub2APIUsageStats, error) {
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return clients.Sub2APIUsageStats{}, errors.New("sub2api_usage_unavailable")
	}
	return client.UsageStats(ctx, clients.Sub2APIUsageStatsQuery{UserID: userID, Period: period})
}

func (s *Service) Sub2APIBalanceHistory(ctx context.Context, userID int64) ([]clients.Sub2APIBalanceHistoryEntry, error) {
	client, ok := s.sub2API.(clients.Sub2APIUsageClient)
	if !ok {
		return nil, errors.New("sub2api_balance_history_unavailable")
	}
	return client.BalanceHistory(ctx, userID)
}

func (s *Service) BillingReceipt(ctx context.Context, receiptID string) (clients.Receipt, error) {
	if receiptID == "" {
		return clients.Receipt{}, fmt.Errorf("receipt_id_required")
	}
	return s.ledger.Receipt(ctx, receiptID)
}

func (s *Service) BillingReceipts(ctx context.Context, query clients.ReceiptQuery) (clients.ReceiptPage, error) {
	client, ok := s.ledger.(clients.LedgerReceiptListClient)
	if !ok {
		return clients.ReceiptPage{}, errors.New("ledger_receipt_list_unavailable")
	}
	if query.AccountID == "" {
		return clients.ReceiptPage{}, errors.New("billing_account_id_required")
	}
	return client.ListReceipts(ctx, query)
}

func (s *Service) RecordReconciliation(ctx context.Context, input ReconciliationInput, idempotencyKey string) (clients.ReconciliationResult, error) {
	return s.ledger.RecordReconciliation(ctx, clients.ReconciliationInput{Report: input.Report}, idempotencyKey)
}

func (s *Service) WorkspaceRuntimeStatus(ctx context.Context, workspaceID string) (clients.WorkspaceRuntime, error) {
	return s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
}

func (s *Service) RuntimeReadiness(ctx context.Context) (map[string]any, error) {
	return s.fabric.Readiness(ctx)
}

func (s *Service) FabricOperations(ctx context.Context) ([]clients.FabricOperation, error) {
	return s.fabric.ListOperations(ctx)
}

func (s *Service) ProviderFactsBatch(ctx context.Context, input clients.ProviderFactsBatchInput) (clients.ProviderFactsBatch, error) {
	client, ok := s.fabric.(clients.FabricProviderFactsClient)
	if !ok {
		return clients.ProviderFactsBatch{}, errors.New("fabric_provider_facts_unavailable")
	}
	return client.ProviderFactsBatch(ctx, input)
}

func (s *Service) FabricCatalog(ctx context.Context) (clients.FabricCatalog, error) {
	return s.fabric.Catalog(ctx)
}

func (s *Service) CreateStorageAttachment(ctx context.Context, input StorageAttachmentInput, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.CreateStorageAttachment(ctx, clients.StorageAttachmentInput{WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID}, idempotencyKey)
}

func (s *Service) DetachStorageAttachment(ctx context.Context, id string, idempotencyKey string) (clients.StorageAttachment, error) {
	return s.fabric.DetachStorageAttachment(ctx, id, idempotencyKey)
}

func (s *Service) PrepareWorkspace(ctx context.Context, input CreateWorkspaceInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	if input.WorkspaceID == "" || input.ComputeID == "" || input.VolumeID == "" || input.AttachmentID == "" {
		return domain.WorkspaceProjection{}, fmt.Errorf("attached_compute_storage_required")
	}
	workspaceID := input.WorkspaceID
	gatewaySecretRef := input.GatewaySecretRef
	if gatewaySecretRef == "" {
		var err error
		if input.WorkspaceAPIKeyID > 0 {
			gatewaySecretRef, err = s.gatewaySecretRefByID(ctx, input.AccountID, workspaceID, input.Sub2APIUserID, input.WorkspaceAPIKeyID, input.WorkspaceAPIKeyName, idempotencyKey)
		} else {
			gatewaySecretRef, err = s.gatewaySecretRef(ctx, input.AccountID, workspaceID, input.Sub2APIUserID, idempotencyKey)
		}
		if err != nil {
			return domain.WorkspaceProjection{}, err
		}
	}
	runtime, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{WorkspaceID: workspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID, ImageID: "one-person-lab-app", GatewaySecretRef: gatewaySecretRef}, idempotencyKey+":runtime")
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	readback, err := s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	runtime, err = mergeWorkspaceRuntimeReadback(runtime, readback, workspaceID)
	if err != nil {
		return domain.WorkspaceProjection{}, err
	}
	status := workspaceRuntimeState(runtime.Status, runtime.Ready)
	workspace := domain.WorkspaceProjection{
		ID:                  workspaceID,
		AccountID:           input.AccountID,
		OwnerID:             input.OwnerID,
		Name:                input.Name,
		PackageID:           input.PackageID,
		Provider:            "tencent-tke",
		URL:                 runtime.URL,
		Status:              status,
		ComputeID:           input.ComputeID,
		VolumeID:            input.VolumeID,
		AttachmentID:        input.AttachmentID,
		RuntimeID:           runtime.ID,
		RuntimeServiceName:  runtime.ServiceName,
		WorkspaceAPIKeyID:   input.WorkspaceAPIKeyID,
		RuntimeReady:        runtime.Ready,
		RuntimeUsername:     runtime.Access.Username,
		CredentialStatus:    runtime.Access.CredentialStatus,
		CredentialVersion:   runtime.Access.CredentialVersion,
		CredentialSecretRef: runtime.Access.SecretRef,
	}
	return workspace, nil
}

func mergeWorkspaceRuntimeReadback(created, readback clients.WorkspaceRuntime, workspaceID string) (clients.WorkspaceRuntime, error) {
	if workspaceID == "" || created.WorkspaceID != "" && created.WorkspaceID != workspaceID || readback.WorkspaceID != workspaceID ||
		created.ID != "" && readback.ID != "" && created.ID != readback.ID ||
		created.ServiceName != "" && readback.ServiceName != "" && created.ServiceName != readback.ServiceName {
		return clients.WorkspaceRuntime{}, ErrWorkspaceRuntimeIdentityMismatch
	}
	if readback.Ready || readback.Status == "running" {
		if !readback.Ready || readback.Status != "running" || readback.ID == "" || readback.URL == "" || readback.ServiceName == "" ||
			readback.Access.Username == "" || readback.Access.CredentialStatus != "configured" || readback.Access.CredentialVersion == "" || readback.Access.SecretRef == "" {
			return clients.WorkspaceRuntime{}, ErrWorkspaceRuntimeReadbackInvalid
		}
	}
	return readback, nil
}

func (s *Service) RecordWorkspaceCreatedReceipt(ctx context.Context, workspace domain.WorkspaceProjection, idempotencyKey string) (domain.WorkspaceProjection, error) {
	input := clients.ReceiptInput{Type: "workspace.created", Status: "completed", Surface: "workspace", AccountID: workspace.AccountID, WorkspaceID: workspace.ID, JobID: workspace.RuntimeID, Execution: map[string]any{"providerRequestId": workspace.RuntimeID}, OutputRefs: map[string]any{"redactedUrl": workspace.URL}}
	return s.recordWorkspaceReceipt(ctx, workspace, input, idempotencyKey)
}

func (s *Service) recordWorkspaceReceipt(ctx context.Context, workspace domain.WorkspaceProjection, input clients.ReceiptInput, idempotencyKey string) (domain.WorkspaceProjection, error) {
	receipt, err := s.ledger.RecordReceipt(ctx, input, idempotencyKey+":receipt")
	if err != nil {
		return workspace, err
	}
	workspace.ReceiptID = receipt.ReceiptID
	return workspace, nil
}

func (s *Service) gatewaySecretRef(ctx context.Context, accountID, workspaceID string, userID int64, idempotencyKey string) (string, error) {
	secret, err := s.SyncWorkspaceGatewaySecret(ctx, accountID, workspaceID, userID, idempotencyKey)
	return secret.SecretRef, err
}

func (s *Service) gatewaySecretRefByID(ctx context.Context, accountID, workspaceID string, userID, keyID int64, keyName, idempotencyKey string) (string, error) {
	secret, err := s.SyncWorkspaceGatewaySecretByID(ctx, accountID, workspaceID, userID, keyID, keyName, idempotencyKey)
	return secret.SecretRef, err
}

func (s *Service) SyncWorkspaceGatewaySecretByID(ctx context.Context, accountID, workspaceID string, userID, keyID int64, keyName, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	key, err := s.Sub2APIWorkspaceKeyByID(ctx, userID, keyID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	if keyName == "" || key.Name != keyName {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	return s.writeGatewaySecretValue(ctx, accountID, workspaceID, userID, key, idempotencyKey)
}

func (s *Service) SyncWorkspaceGatewayReplacementSecret(ctx context.Context, credential clients.SessionDelegatedCredential, accountID, workspaceID string, userID, keyID int64, replacementName, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	key, err := s.GatewayUserKey(ctx, credential, userID, keyID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	if replacementName == "" || key.Name != replacementName || !strings.HasPrefix(replacementName, "opl-workspace-") || replacementName == "opl-workspace" {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	return s.writeGatewaySecretValue(ctx, accountID, workspaceID, userID, key, idempotencyKey)
}

func (s *Service) SyncWorkspaceGatewaySecret(ctx context.Context, accountID, workspaceID string, userID int64, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if accountID == "" || workspaceID == "" || userID <= 0 || idempotencyKey == "" {
		return clients.GatewaySecretWriteResult{}, errors.New("gateway_secret_write_failed")
	}
	key, err := s.Sub2APIWorkspaceKey(ctx, userID)
	if err != nil {
		return clients.GatewaySecretWriteResult{}, err
	}
	return s.writeWorkspaceGatewaySecret(ctx, accountID, workspaceID, userID, key, idempotencyKey)
}

func (s *Service) writeWorkspaceGatewaySecret(ctx context.Context, accountID, workspaceID string, userID int64, key clients.Sub2APIWorkspaceKey, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if key.Name != "opl-workspace" {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	return s.writeGatewaySecretValue(ctx, accountID, workspaceID, userID, key, idempotencyKey)
}

func (s *Service) writeGatewaySecretValue(ctx context.Context, accountID, workspaceID string, userID int64, key clients.Sub2APIWorkspaceKey, idempotencyKey string) (clients.GatewaySecretWriteResult, error) {
	if accountID == "" || workspaceID == "" || userID <= 0 || idempotencyKey == "" || key.ID <= 0 || key.UserID != userID || key.Status != "active" || key.Key == "" {
		return clients.GatewaySecretWriteResult{}, errors.New("invalid_sub2api_workspace_key")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key.Key)))
	secret, err := s.fabric.WriteGatewaySecret(ctx, clients.GatewaySecretWriteInput{
		AccountID: accountID, WorkspaceID: workspaceID, WorkspaceAPIKeyID: key.ID,
		Fingerprint: "sha256:" + digest, GatewayAPIKey: key.Key,
	}, idempotencyKey+":gateway-secret")
	if err != nil {
		return clients.GatewaySecretWriteResult{}, fmt.Errorf("gateway_secret_write_failed: %w", err)
	}
	if secret.SecretRef == "" || secret.Fingerprint != "sha256:"+digest {
		return clients.GatewaySecretWriteResult{}, errors.New("gateway_secret_write_failed")
	}
	return secret, nil
}

func (s *Service) BindWorkspaceRuntimeGatewaySecret(ctx context.Context, input clients.WorkspaceRuntimeGatewaySecretInput, idempotencyKey string) (clients.WorkspaceRuntimeGatewaySecretBinding, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceRuntimeGatewaySecretClient)
	if !ok || input.WorkspaceID == "" || input.WorkspaceAPIKeyID <= 0 || input.SecretRef == "" || input.Fingerprint == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntimeGatewaySecretBinding{}, errors.New("workspace_runtime_gateway_secret_unavailable")
	}
	return client.BindWorkspaceRuntimeGatewaySecret(ctx, input, idempotencyKey)
}

func (s *Service) WorkspaceRuntimeGatewaySecret(ctx context.Context, workspaceID string) (clients.WorkspaceRuntimeGatewaySecretBinding, error) {
	client, ok := s.fabric.(clients.FabricWorkspaceRuntimeGatewaySecretClient)
	if !ok || workspaceID == "" {
		return clients.WorkspaceRuntimeGatewaySecretBinding{}, errors.New("workspace_runtime_gateway_secret_unavailable")
	}
	return client.WorkspaceRuntimeGatewaySecret(ctx, workspaceID)
}

func (s *Service) ReapplyWorkspaceRuntime(ctx context.Context, workspaceID, computeID, volumeID, secretRef, idempotencyKey string) (clients.WorkspaceRuntime, error) {
	if workspaceID == "" || computeID == "" || volumeID == "" || secretRef == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_apply_input_required")
	}
	applied, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{
		WorkspaceID: workspaceID, ComputeID: computeID, VolumeID: volumeID,
		ImageID: "one-person-lab-app", GatewaySecretRef: secretRef,
	}, idempotencyKey+":runtime")
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	runtime, err := s.fabric.WorkspaceRuntimeStatus(ctx, workspaceID)
	if err != nil {
		return clients.WorkspaceRuntime{}, err
	}
	if runtime.ID == "" {
		runtime.ID = applied.ID
	}
	if runtime.WorkspaceID == "" {
		runtime.WorkspaceID = applied.WorkspaceID
	}
	if runtime.ServiceName == "" {
		runtime.ServiceName = applied.ServiceName
	}
	if runtime.WorkspaceID != workspaceID || runtime.ID == "" || runtime.Status == "not_found" || !runtime.Ready {
		return clients.WorkspaceRuntime{}, errors.New("workspace_runtime_readback_invalid")
	}
	return runtime, nil
}

func (s *Service) RecordWorkspaceGatewayKeyRotation(ctx context.Context, accountID, workspaceID, ownerID, operationID string, oldKeyID, newKeyID int64, fingerprint string) (clients.Receipt, error) {
	if accountID == "" || workspaceID == "" || ownerID == "" || operationID == "" || oldKeyID <= 0 || newKeyID <= 0 || oldKeyID == newKeyID || fingerprint == "" {
		return clients.Receipt{}, errors.New("workspace_gateway_key_rotation_evidence_invalid")
	}
	return s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type: "workspace.gateway_key_rotated.v1", Status: "completed", Surface: "control_plane",
		AccountID: accountID, WorkspaceID: workspaceID,
		Execution:  map[string]any{"operationId": operationID, "oldKeyId": oldKeyID, "newKeyId": newKeyID},
		OutputRefs: map[string]any{"secretFingerprint": fingerprint},
		Owner:      map[string]any{"userId": ownerID},
	}, operationID+":receipt")
}

func (s *Service) RotateWorkspaceCredential(ctx context.Context, input RotateWorkspaceCredentialInput, idempotencyKey string) (clients.WorkspaceRuntime, clients.Receipt, error) {
	if input.WorkspaceID == "" || input.AccountID == "" || input.OwnerID == "" || input.ComputeID == "" || input.VolumeID == "" || input.GatewaySecretRef == "" || idempotencyKey == "" {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, errors.New("runtime_credential_rotation_input_required")
	}
	operationKey := "runtime-credential-rotate:" + input.WorkspaceID + ":" + idempotencyKey
	applied, err := s.fabric.CreateWorkspaceRuntime(ctx, clients.WorkspaceRuntimeInput{
		WorkspaceID: input.WorkspaceID, ComputeID: input.ComputeID, VolumeID: input.VolumeID,
		ImageID: "one-person-lab-app", GatewaySecretRef: input.GatewaySecretRef,
	}, operationKey+":runtime")
	if err != nil {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, err
	}
	runtime, err := s.fabric.WorkspaceRuntimeStatus(ctx, input.WorkspaceID)
	if err != nil {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, err
	}
	if runtime.ID == "" {
		runtime.ID = applied.ID
	}
	if runtime.WorkspaceID == "" {
		runtime.WorkspaceID = input.WorkspaceID
	}
	if runtime.ServiceName == "" {
		runtime.ServiceName = applied.ServiceName
	}
	if runtime.Access.Password == "" {
		return clients.WorkspaceRuntime{}, clients.Receipt{}, errors.New("workspace_credentials_unavailable")
	}
	receipt, err := s.ledger.RecordReceipt(ctx, clients.ReceiptInput{
		Type: "workspace.access_token_reset", Status: "completed", Surface: "workspace",
		AccountID: input.AccountID, WorkspaceID: input.WorkspaceID, JobID: runtime.ID,
		Execution: map[string]any{
			"runtimeId": runtime.ID, "computeAllocationId": input.ComputeID, "storageId": input.VolumeID,
		},
		OutputRefs: map[string]any{
			"runtimeId": runtime.ID, "credentialVersion": runtime.Access.CredentialVersion, "credentialSecretRef": runtime.Access.SecretRef,
		},
		Owner: map[string]any{"userId": input.OwnerID},
	}, operationKey)
	if err != nil {
		return runtime, clients.Receipt{}, err
	}
	return runtime, receipt, nil
}

func workspaceRuntimeState(status string, ready bool) string {
	if ready {
		if status == "" {
			return "running"
		}
		return status
	}
	if status == "" || status == "running" {
		return "unready"
	}
	return status
}
