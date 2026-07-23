import { decodeDto, decodeSource } from "./dtos.ts";
import type {
  AnnouncementPageDTO,
  AnnouncementDTO,
  AnnouncementReadDTO,
  BalanceHistoryData,
  BillingReceipt,
  BillingReceiptPage,
  CreateGatewayKeyRequest,
  GatewayAccountUsageSummaryDTO,
  GatewayEndpointDTO,
  GatewayGroupPageDTO,
  GatewayKeyListQuery,
  GatewayKeyPageDTO,
  GatewayKeySecretDTO,
  GatewayKeySummaryDTO,
  GatewayKeyUsagePageDTO,
  GatewayUsageSummaryDTO,
  GatewayWallet,
  ManagementState,
  AnnouncementDraftRequest,
  AnnouncementScheduleRequest,
  BillingReviewResolutionRequest,
  ProvisionAccountRequest,
  OperatorAccountCommandDTO,
  OperatorAccountPageDTO,
  OperatorAnnouncementPageDTO,
  OperatorHealthDTO,
  OperatorOverviewDTO,
  OperatorReconciliationPageDTO,
  OperatorWorkspaceDTO,
  OperatorWorkspacePageDTO,
  WalletAdjustmentOperationDTO,
  WalletAdjustmentRecoveryRequest,
  WalletAdjustmentRequest,
  OperatorAccountsData,
  OperationStatusDTO,
  PricingCatalogResponse,
  PricingPreviewRequest,
  PricingPreviewResponse,
  ReadinessFact,
  SourceEnvelope,
  UpdateGatewayKeyRequest,
  WorkspaceLaunchRecoveryRequest
} from "./dtos.ts";
import { deleteJson, getJson, patchJson, postJson, putJson, type ApiError } from "./console-api.ts";

async function sourceGet<T>(path: string, signal?: AbortSignal): Promise<SourceEnvelope<T>> {
  try {
    return decodeSource<T>(await getJson<unknown>(path, { signal }));
  } catch (error) {
    const payload = (error as ApiError).payload;
    if (payload !== undefined) {
      try {
        return decodeSource<T>(payload);
      } catch {
        // Preserve the transport error when the server did not return a source envelope.
      }
    }
    throw error;
  }
}

async function sourceWrite<T>(request: () => Promise<unknown>): Promise<SourceEnvelope<T>> {
  try {
    return decodeSource<T>(await request());
  } catch (error) {
    const payload = (error as ApiError).payload;
    if (payload !== undefined) {
      try {
        return decodeSource<T>(payload);
      } catch {
        // Preserve the transport error when the server did not return a source envelope.
      }
    }
    throw error;
  }
}

function sourcePost<T>(path: string, body: unknown, csrfToken: string, idempotencyKey = ""): Promise<SourceEnvelope<T>> {
  return sourceWrite<T>(() => postJson<unknown>(path, body, csrfToken, idempotencyKey));
}

function sourcePatch<T>(path: string, body: unknown, csrfToken: string, idempotencyKey: string): Promise<SourceEnvelope<T>> {
  return sourceWrite<T>(() => patchJson<unknown>(path, body, csrfToken, idempotencyKey));
}

function sourceDelete<T>(path: string, csrfToken: string, idempotencyKey: string): Promise<SourceEnvelope<T>> {
  return sourceWrite<T>(() => deleteJson<unknown>(path, csrfToken, idempotencyKey));
}

export function getConsoleState(): Promise<unknown> {
  return getJson<unknown>("/api/state");
}

export function getGatewayWallet(signal?: AbortSignal): Promise<SourceEnvelope<GatewayWallet>> {
  return sourceGet<GatewayWallet>("/api/gateway/wallet", signal);
}

export function getGatewayKeys(query: GatewayKeyListQuery = {}, signal?: AbortSignal): Promise<SourceEnvelope<GatewayKeyPageDTO>> {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const suffix = params.size > 0 ? `?${params}` : "";
  return sourceGet<GatewayKeyPageDTO>(`/api/gateway/keys${suffix}`, signal);
}

export function getGatewayGroups(signal?: AbortSignal): Promise<SourceEnvelope<GatewayGroupPageDTO>> {
  return sourceGet<GatewayGroupPageDTO>("/api/gateway/groups", signal);
}

export function getGatewayEndpoint(signal?: AbortSignal): Promise<SourceEnvelope<GatewayEndpointDTO>> {
  return sourceGet<GatewayEndpointDTO>("/api/gateway/endpoint", signal);
}

export function getGatewayKey(keyId: string, signal?: AbortSignal): Promise<SourceEnvelope<GatewayKeySummaryDTO>> {
  return sourceGet<GatewayKeySummaryDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}`, signal);
}

export function createGatewayKey(input: CreateGatewayKeyRequest, csrfToken: string, idempotencyKey: string): Promise<SourceEnvelope<GatewayKeySummaryDTO>> {
  return sourcePost<GatewayKeySummaryDTO>("/api/gateway/keys", input, csrfToken, idempotencyKey);
}

export function updateGatewayKey(keyId: string, input: UpdateGatewayKeyRequest, csrfToken: string, idempotencyKey: string): Promise<SourceEnvelope<GatewayKeySummaryDTO>> {
  return sourcePatch<GatewayKeySummaryDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}`, input, csrfToken, idempotencyKey);
}

export function deleteGatewayKey(keyId: string, csrfToken: string, idempotencyKey: string): Promise<SourceEnvelope<OperationStatusDTO>> {
  return sourceDelete<OperationStatusDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}`, csrfToken, idempotencyKey);
}

export function getGatewayBalanceHistory(signal?: AbortSignal): Promise<SourceEnvelope<BalanceHistoryData>> {
  return sourceGet<BalanceHistoryData>("/api/gateway/balance-history", signal);
}

export function revealGatewayKey(keyId: string, csrfToken: string): Promise<SourceEnvelope<GatewayKeySecretDTO>> {
  return sourcePost<GatewayKeySecretDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}/reveal`, {}, csrfToken);
}

export function getGatewayKeyUsage(keyId: string, page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<GatewayKeyUsagePageDTO>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<GatewayKeyUsagePageDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}/usage?${params}`, signal);
}

export function getGatewayKeyUsageSummary(keyId: string, period = "month", signal?: AbortSignal): Promise<SourceEnvelope<GatewayUsageSummaryDTO>> {
  return sourceGet<GatewayUsageSummaryDTO>(`/api/gateway/keys/${encodeURIComponent(keyId)}/usage-summary?${new URLSearchParams({ period })}`, signal);
}

export function getGatewayAccountUsageSummary(period = "month", signal?: AbortSignal): Promise<SourceEnvelope<GatewayAccountUsageSummaryDTO>> {
  return sourceGet<GatewayAccountUsageSummaryDTO>(`/api/gateway/usage-summary?${new URLSearchParams({ period })}`, signal);
}

export function getBillingReceipts(cursor = "", limit = 20, signal?: AbortSignal): Promise<SourceEnvelope<BillingReceiptPage>> {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set("cursor", cursor);
  return sourceGet<BillingReceiptPage>(`/api/billing/receipts?${params}`, signal);
}

export function getBillingReceipt(receiptId: string, signal?: AbortSignal): Promise<SourceEnvelope<BillingReceipt>> {
  return sourceGet<BillingReceipt>(`/api/billing/receipts/${encodeURIComponent(receiptId)}`, signal);
}

export function getAnnouncements(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<AnnouncementPageDTO>> {
  return sourceGet<AnnouncementPageDTO>(`/api/announcements?${new URLSearchParams({ page: String(page), pageSize: String(pageSize) })}`, signal);
}

export function markAnnouncementRead(announcementId: string, csrfToken: string, idempotencyKey: string): Promise<AnnouncementReadDTO> {
  return postJson<unknown>(`/api/announcements/${encodeURIComponent(announcementId)}/read`, {}, csrfToken, idempotencyKey).then(decodeDto<AnnouncementReadDTO>);
}

export function getPricingCatalog(): Promise<PricingCatalogResponse> {
  return getJson<unknown>("/api/pricing/catalog").then(decodeDto<PricingCatalogResponse>);
}

export function previewPricing(input: PricingPreviewRequest, csrfToken: string): Promise<PricingPreviewResponse> {
  return postJson<unknown>("/api/pricing/preview", input, csrfToken).then(decodeDto<PricingPreviewResponse>);
}

export function getOperatorAccounts(): Promise<SourceEnvelope<OperatorAccountsData>> {
  return sourceGet<OperatorAccountsData>("/api/operator/accounts");
}

export function getOperatorOverview(signal?: AbortSignal): Promise<SourceEnvelope<OperatorOverviewDTO>> {
  return sourceGet<OperatorOverviewDTO>("/api/operator/overview", signal);
}

export function getOperatorAccountsPage(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<OperatorAccountPageDTO>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<OperatorAccountPageDTO>(`/api/operator/accounts?${params}`, signal);
}

export function getOperatorWorkspaces(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<OperatorWorkspacePageDTO>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<OperatorWorkspacePageDTO>(`/api/operator/workspaces?${params}`, signal);
}

export function getOperatorWorkspace(workspaceId: string, signal?: AbortSignal): Promise<SourceEnvelope<OperatorWorkspaceDTO>> {
  return sourceGet<OperatorWorkspaceDTO>(`/api/operator/workspaces/${encodeURIComponent(workspaceId)}`, signal);
}

export function getOperatorReconciliation(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<OperatorReconciliationPageDTO>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<OperatorReconciliationPageDTO>(`/api/operator/reconciliation?${params}`, signal);
}

export function getOperatorHealth(signal?: AbortSignal): Promise<SourceEnvelope<OperatorHealthDTO>> {
  return sourceGet<OperatorHealthDTO>("/api/operator/health", signal);
}

export function getOperatorAnnouncements(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<OperatorAnnouncementPageDTO>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<OperatorAnnouncementPageDTO>(`/api/operator/announcements?${params}`, signal);
}

export function createWalletAdjustment(accountId: string, input: WalletAdjustmentRequest, csrfToken: string, idempotencyKey: string): Promise<WalletAdjustmentOperationDTO> {
  return postJson<unknown>(`/api/operator/accounts/${encodeURIComponent(accountId)}/wallet-adjustments`, input, csrfToken, idempotencyKey).then(decodeDto<WalletAdjustmentOperationDTO>);
}

export function getWalletAdjustment(operationId: string, signal?: AbortSignal): Promise<WalletAdjustmentOperationDTO> {
  return getJson<unknown>(`/api/operator/wallet-adjustments/${encodeURIComponent(operationId)}`, { signal }).then(decodeDto<WalletAdjustmentOperationDTO>);
}

export function recoverWalletAdjustment(operationId: string, input: WalletAdjustmentRecoveryRequest, csrfToken: string, idempotencyKey: string): Promise<WalletAdjustmentOperationDTO> {
  return postJson<unknown>(`/api/operator/wallet-adjustments/${encodeURIComponent(operationId)}/recover`, input, csrfToken, idempotencyKey).then(decodeDto<WalletAdjustmentOperationDTO>);
}

export function provisionOperatorAccount(input: ProvisionAccountRequest, csrfToken: string, idempotencyKey: string): Promise<OperatorAccountCommandDTO> {
  return postJson<unknown>("/api/operator/accounts", input, csrfToken, idempotencyKey).then(decodeDto<OperatorAccountCommandDTO>);
}

export function disableOperatorAccount(accountId: string, reason: string, csrfToken: string, idempotencyKey: string): Promise<OperatorAccountCommandDTO> {
  return postJson<unknown>(`/api/operator/accounts/${encodeURIComponent(accountId)}/disable`, { confirmationAccountId: accountId, reason }, csrfToken, idempotencyKey).then(decodeDto<OperatorAccountCommandDTO>);
}

export function resolveBillingReview(resourceType: string, resourceId: string, input: BillingReviewResolutionRequest, csrfToken: string, idempotencyKey: string): Promise<OperationStatusDTO> {
  return postJson<unknown>(`/api/operator/billing-reviews/${encodeURIComponent(resourceType)}/${encodeURIComponent(resourceId)}/resolve`, input, csrfToken, idempotencyKey).then(decodeDto<OperationStatusDTO>);
}

export function recoverWorkspaceLaunch(operationId: string, input: WorkspaceLaunchRecoveryRequest, csrfToken: string, idempotencyKey: string): Promise<OperationStatusDTO> {
  return postJson<unknown>(`/api/operator/workspace-launches/${encodeURIComponent(operationId)}/recover`, input, csrfToken, idempotencyKey).then(decodeDto<OperationStatusDTO>);
}

export function createOperatorAnnouncement(input: AnnouncementDraftRequest, csrfToken: string, idempotencyKey: string): Promise<AnnouncementDTO> {
  return postJson<unknown>("/api/operator/announcements", input, csrfToken, idempotencyKey).then(decodeDto<AnnouncementDTO>);
}

export function updateOperatorAnnouncement(announcementId: string, input: AnnouncementDraftRequest, csrfToken: string, idempotencyKey: string): Promise<AnnouncementDTO> {
  return putJson<unknown>(`/api/operator/announcements/${encodeURIComponent(announcementId)}`, input, csrfToken, idempotencyKey).then(decodeDto<AnnouncementDTO>);
}

export function publishOperatorAnnouncement(announcementId: string, input: AnnouncementScheduleRequest, csrfToken: string, idempotencyKey: string): Promise<AnnouncementDTO> {
  return postJson<unknown>(`/api/operator/announcements/${encodeURIComponent(announcementId)}/publish`, input, csrfToken, idempotencyKey).then(decodeDto<AnnouncementDTO>);
}

export function withdrawOperatorAnnouncement(announcementId: string, csrfToken: string, idempotencyKey: string): Promise<AnnouncementDTO> {
  return postJson<unknown>(`/api/operator/announcements/${encodeURIComponent(announcementId)}/withdraw`, {}, csrfToken, idempotencyKey).then(decodeDto<AnnouncementDTO>);
}

export function getManagementState(): Promise<ManagementState> {
  return getJson<unknown>("/api/management/state").then(decodeDto<ManagementState>);
}

export function getRuntimeReadiness(): Promise<ReadinessFact> {
  return getJson<unknown>("/api/runtime/readiness").then(decodeDto<ReadinessFact>);
}

export function getProductionReadiness(): Promise<ReadinessFact> {
  return getJson<unknown>("/api/production/readiness").then(decodeDto<ReadinessFact>);
}
