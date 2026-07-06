import { operationEnvelope, postJson } from "./console-api.ts";

export function createWorkspace(input, csrfToken) {
  return postJson("/api/workspaces", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { next: { detailRouteId: "workspace.detail" } }));
}

export function resetWorkspaceToken(input, csrfToken) {
  return postJson("/api/workspaces/reset-token", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.workspaceId, next: { detailRouteId: "workspace.detail" } }));
}

export function deleteWorkspaceToken(input, csrfToken) {
  return postJson("/api/workspaces/delete-token", input, csrfToken)
    .then((payload) => operationEnvelope(payload, { resourceId: input.workspaceId, next: { detailRouteId: "workspace.detail" } }));
}

export function getWorkspaceRuntimeStatus(input, csrfToken) {
  return postJson("/api/workspaces/runtime-status", input, csrfToken);
}
