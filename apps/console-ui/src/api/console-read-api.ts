import { decodeDto, decodeSource } from "./dtos.ts";
import type {
  BalanceHistoryData,
  BillingReceiptPage,
  CreateCustomerUserRequest,
  GatewayKeyReveal,
  GatewayKeysData,
  GatewayUsageData,
  GatewayUsageStats,
  GatewayWallet,
  ManagementState,
  OperatorAccountsData,
  OperatorSummary,
  PricingCatalogResponse,
  PricingPreviewRequest,
  PricingPreviewResponse,
  ReadinessFact,
  SourceEnvelope
} from "./dtos.ts";
import { getJson, postJson, type ApiError } from "./console-api.ts";

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

async function sourcePost<T>(path: string, body: unknown, csrfToken: string): Promise<SourceEnvelope<T>> {
  try {
    return decodeSource<T>(await postJson<unknown>(path, body, csrfToken));
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

export function getConsoleState(): Promise<unknown> {
  return getJson<unknown>("/api/state");
}

export function getGatewayWallet(signal?: AbortSignal): Promise<SourceEnvelope<GatewayWallet>> {
  return sourceGet<GatewayWallet>("/api/gateway/wallet", signal);
}

export function getGatewayKeys(signal?: AbortSignal): Promise<SourceEnvelope<GatewayKeysData>> {
  return sourceGet<GatewayKeysData>("/api/gateway/keys", signal);
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

export function revealGatewayKey(csrfToken: string): Promise<SourceEnvelope<GatewayKeyReveal>> {
  return sourcePost<GatewayKeyReveal>("/api/gateway/keys/opl-workspace/reveal", {}, csrfToken);
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
