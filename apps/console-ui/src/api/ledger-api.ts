import { getJson, postJson } from "./console-api.ts";

export function getTaskEvidenceReceipts(params: Record<string, string> = {}) {
  const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== null && value !== undefined && value !== ""));
  const queryString = query.toString();
  return getJson(`/api/ledger/task-receipts${queryString ? `?${queryString}` : ""}`);
}

export function recordTaskEvidenceReceipt(input, csrfToken) {
  return postJson("/api/ledger/task-receipts", input, csrfToken);
}
