import { postJson } from "./console-api.js";

export function createWorkspace(input, csrfToken) {
  return postJson("/api/workspaces", input, csrfToken);
}

export function resetWorkspaceToken(input, csrfToken) {
  return postJson("/api/workspaces/reset-token", input, csrfToken);
}

export function deleteWorkspaceToken(input, csrfToken) {
  return postJson("/api/workspaces/delete-token", input, csrfToken);
}

export function getWorkspaceRuntimeStatus(input, csrfToken) {
  return postJson("/api/workspaces/runtime-status", input, csrfToken);
}
