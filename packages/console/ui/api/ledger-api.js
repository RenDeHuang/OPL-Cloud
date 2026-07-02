import { getJson, postJson } from "./console-api.js";

export function getTaskEvidenceReceipts(params = {}) {
  const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== null && value !== undefined && value !== ""));
  const queryString = query.toString();
  return getJson(`/api/ledger/task-receipts${queryString ? `?${queryString}` : ""}`);
}

export function recordTaskEvidenceReceipt(input, csrfToken) {
  return postJson("/api/ledger/task-receipts", input, csrfToken);
}
