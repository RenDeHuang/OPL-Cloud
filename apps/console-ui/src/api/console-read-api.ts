import { decodeDto, decodeSource } from "./dtos.ts";
import type {
  BalanceHistoryData,
  BillingReceiptPage,
  CreateGatewayKeyRequest,
  CreateCustomerUserRequest,
  GatewayAccountUsageSummaryDTO,
  GatewayEndpointDTO,
  GatewayKeyPageDTO,
  GatewayKeyReveal,
  GatewayKeySummaryDTO,
  GatewayKeyUsagePageDTO,
  GatewayUsageData,
  GatewayUsageStats,
  GatewayUsageSummaryDTO,
  GatewayWallet,
  ManagementState,
  OperatorAccountsData,
  OperationStatusDTO,
  OperatorSummary,
  PricingCatalogResponse,
  PricingPreviewRequest,
  PricingPreviewResponse,
  ReadinessFact,
  SourceEnvelope,
  UpdateGatewayKeyRequest
} from "./dtos.ts";
import { deleteJson, getJson, patchJson, postJson, type ApiError } from "./console-api.ts";

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

export function getGatewayEndpoint(signal?: AbortSignal): Promise<SourceEnvelope<GatewayEndpointDTO>> {
  return sourceGet<GatewayEndpointDTO>("/api/gateway/endpoint", signal);
}

export function getGatewayKeys(signal?: AbortSignal): Promise<SourceEnvelope<GatewayKeyPageDTO>> {
  return sourceGet<GatewayKeyPageDTO>("/api/gateway/keys", signal);
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

export function getGatewayUsage(page = 1, pageSize = 20, signal?: AbortSignal): Promise<SourceEnvelope<GatewayUsageData>> {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceGet<GatewayUsageData>(`/api/gateway/usage?${params}`, signal);
}

export function getGatewayUsageStats(period = "month", signal?: AbortSignal): Promise<SourceEnvelope<GatewayUsageStats>> {
  return sourceGet<GatewayUsageStats>(`/api/gateway/usage/stats?${new URLSearchParams({ period })}`, signal);
}

export function getGatewayBalanceHistory(signal?: AbortSignal): Promise<SourceEnvelope<BalanceHistoryData>> {
  return sourceGet<BalanceHistoryData>("/api/gateway/balance-history", signal);
}

export function revealGatewayKey(keyIdOrCsrf: string, csrfToken?: string): Promise<SourceEnvelope<GatewayKeyReveal>> {
  const legacy = csrfToken === undefined;
  return sourcePost<GatewayKeyReveal>(legacy ? "/api/gateway/keys/opl-workspace/reveal" : `/api/gateway/keys/${encodeURIComponent(keyIdOrCsrf)}/reveal`, {}, legacy ? keyIdOrCsrf : csrfToken);
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

export function getPricingCatalog(): Promise<PricingCatalogResponse> {
  return getJson<unknown>("/api/pricing/catalog").then(decodeDto<PricingCatalogResponse>);
}

export function previewPricing(input: PricingPreviewRequest, csrfToken: string): Promise<PricingPreviewResponse> {
  return postJson<unknown>("/api/pricing/preview", input, csrfToken).then(decodeDto<PricingPreviewResponse>);
}

export function getOperatorAccounts(): Promise<SourceEnvelope<OperatorAccountsData>> {
  return sourceGet<OperatorAccountsData>("/api/operator/accounts");
}

export function getManagementState(): Promise<ManagementState> {
  return getJson<unknown>("/api/management/state").then(decodeDto<ManagementState>);
}

export function getOperatorSummary(): Promise<OperatorSummary> {
  return getJson<unknown>("/api/operator/summary").then(decodeDto<OperatorSummary>);
}

export function getRuntimeReadiness(): Promise<ReadinessFact> {
  return getJson<unknown>("/api/runtime/readiness").then(decodeDto<ReadinessFact>);
}

export function getProductionReadiness(): Promise<ReadinessFact> {
  return getJson<unknown>("/api/production/readiness").then(decodeDto<ReadinessFact>);
}

export function createUser(input: CreateCustomerUserRequest, csrfToken: string): Promise<unknown> {
  return postJson<unknown>("/api/users", input, csrfToken);
}
