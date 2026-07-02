import { getJson } from "./console-api.js";

export function getConsoleState() {
  return getJson("/api/state");
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

export function getManagementState(organizationId) {
  const params = new URLSearchParams({ organizationId });
  return getJson(`/api/management/state?${params.toString()}`);
}
