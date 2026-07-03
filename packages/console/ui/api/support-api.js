import { getJson, postJson } from "./console-api.js";

export function getSupportTickets({ all = false } = {}) {
  const params = all ? "?scope=all" : "";
  return getJson(`/api/support/tickets${params}`);
}

export function createSupportTicket(input, csrfToken) {
  return postJson("/api/support/tickets", input, csrfToken);
}
