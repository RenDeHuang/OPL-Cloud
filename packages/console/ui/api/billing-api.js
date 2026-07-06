import { postJson } from "./console-api.js";

export function manualTopUp(input, csrfToken) {
  return postJson("/api/billing/topups", input, csrfToken);
}

export function recordBillingReconciliation(input, csrfToken) {
  return postJson("/api/billing/reconciliation", input, csrfToken);
}
