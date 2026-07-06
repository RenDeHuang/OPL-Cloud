import { getJson, postJson } from "./console-api.ts";

export function getSupportTickets({ all = false }: any = {}) {
  const params = all ? "?scope=all" : "";
  return getJson(`/api/support/tickets${params}`);
}

export function createSupportTicket(input, csrfToken) {
  return postJson("/api/support/tickets", input, csrfToken);
}
