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

export function getRuntimeReadiness() {
  return getJson("/api/runtime/readiness");
}

export function getProductionReadiness() {
  return getJson("/api/production/readiness");
}

export function getManagementState(organizationId = "") {
  const params = new URLSearchParams();
  if (organizationId) params.set("organizationId", organizationId);
  params.set("includeDeleted", "true");
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

export function cleanupWorkspaceAccess(input, csrfToken) {
  return postJson("/api/operator/cleanup-workspace-access", input, csrfToken);
}
