import { postJson } from "./console-api.js";

export function creditAccount(input, csrfToken) {
  return postJson("/api/accounts/credit", input, csrfToken);
}

export function settleBilling(input, csrfToken) {
  return postJson("/api/billing/settle", input, csrfToken);
}

export function recordRequestUsage(input, csrfToken) {
  return postJson("/api/billing/request-usage", input, csrfToken);
}

export function recordBillingReconciliation(input, csrfToken) {
  return postJson("/api/billing/reconciliation", input, csrfToken);
}
