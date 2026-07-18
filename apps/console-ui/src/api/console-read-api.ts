import { getJson, postJson } from "./console-api.ts";

export function getConsoleState(accountId = "") {
  const params = new URLSearchParams();
  if (accountId) params.set("accountId", accountId);
  const query = params.toString();
  return getJson(`/api/state${query ? `?${query}` : ""}`);
}

export function getOperatorSummary() {
  return getJson("/api/operator/summary");
}

export function getArchiveState() {
  return getJson("/api/operator/archive");
}

export function getRuntimeReadiness() {
  return getJson("/api/runtime/readiness");
}

export function getProductionReadiness() {
  return getJson("/api/production/readiness");
}

export function getPricingCatalog() {
  return getJson("/api/pricing/catalog");
}

export function previewPricing(input, csrfToken) {
  return postJson("/api/pricing/preview", input, csrfToken);
}

export function getGatewaySummary(reveal = false, signal?: AbortSignal) {
  return getJson(`/api/gateway/summary${reveal ? "?reveal=true" : ""}`, { signal });
}

export function getGatewayUsage(page = 1, pageSize = 20, signal?: AbortSignal) {
  const params = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return getJson(`/api/gateway/usage?${params}`, { signal });
}

export function getGatewayUsageStats(period = "month", signal?: AbortSignal) {
  const params = new URLSearchParams({ period });
  return getJson(`/api/gateway/usage/stats?${params}`, { signal });
}

export function getBillingReceipts(cursor = "", limit = 20, signal?: AbortSignal) {
  const params = new URLSearchParams({ limit: String(limit) });
  if (cursor) params.set("cursor", cursor);
  return getJson(`/api/billing/receipts?${params}`, { signal });
}

export function getManagementState(organizationId = "", includeDeleted = false) {
  const params = new URLSearchParams();
  if (organizationId) params.set("organizationId", organizationId);
  if (includeDeleted) params.set("includeDeleted", "true");
  const query = params.toString();
  return getJson(`/api/management/state${query ? `?${query}` : ""}`);
}

export function createUser(input, csrfToken) {
  return postJson("/api/users", input, csrfToken);
}

export function createOrganization(input, csrfToken) {
  return postJson("/api/organizations", input, csrfToken);
}

export function addOrganizationMember(input, csrfToken) {
  return postJson("/api/organizations/members", input, csrfToken);
}

export function disableUser(input, csrfToken) {
  return postJson("/api/users/disable", input, csrfToken);
}

export function deleteUser(input, csrfToken) {
  return postJson("/api/users/delete", input, csrfToken);
}

export function archiveTerminalResources(input, csrfToken) {
  return postJson("/api/operator/archive-terminal-resources", input, csrfToken);
}
