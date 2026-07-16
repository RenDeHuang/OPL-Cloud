import { operationEnvelope, postJson } from "./console-api.ts";

const unknownCreateResult = "Workspace 创建结果未知，请重试以确认同一请求；不会重复创建。";

export function createWorkspaceIntent(input, previous: any = null) {
  if (previous) return previous;
  return { input: { ...input }, idempotencyKey: crypto.randomUUID() };
}

export function createWorkspaceLaunchIntent(input, previous: any = null) {
  if (previous) return previous;
  const id = crypto.randomUUID();
  return {
    id,
    input: { ...input },
    idempotencyKeys: Object.fromEntries(
      ["compute", "storage", "attachment", "workspace"].map((step) => [step, `workspace-launch:${id}:${step}`])
    )
  };
}

export async function createWorkspace(intent, csrfToken) {
  if (!intent?.idempotencyKey || !intent?.input) throw new Error("workspace_create_intent_required");
  try {
    const payload = await postJson("/api/workspaces", intent.input, csrfToken, intent.idempotencyKey);
    return operationEnvelope(payload, { next: { detailRouteId: "workspace.detail" } });
  } catch (error: any) {
    if (error?.payload) throw error;
    const unknown: any = new Error(unknownCreateResult, { cause: error });
    unknown.payload = { status: "unknown", retryable: true, failureReason: unknownCreateResult };
    throw unknown;
  }
}

export function getWorkspaceRuntimeStatus(input, csrfToken) {
  return postJson("/api/workspaces/runtime-status", input, csrfToken);
}

export async function rotateWorkspaceGatewaySecret(input, csrfToken, idempotencyKey) {
  const payload = await postJson(
    `/api/workspaces/${encodeURIComponent(input.workspaceId)}/gateway-secret/rotate`,
    { reason: input.reason || "owner-request" },
    csrfToken,
    idempotencyKey
  );
  return {
    status: payload.status || "unknown",
    fingerprint: payload.fingerprint || ""
  };
}
