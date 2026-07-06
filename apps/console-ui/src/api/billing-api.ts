import { postJson } from "./console-api.ts";

export function manualTopUp(input, csrfToken) {
  return postJson("/api/billing/topups", input, csrfToken);
}

export function recordBillingReconciliation(input, csrfToken) {
  return postJson("/api/billing/reconciliation", input, csrfToken);
}

export function settleResourceBilling(input, csrfToken) {
  return postJson("/api/billing/resource-settlements", input, csrfToken);
}
