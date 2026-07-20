import { decodeDto, decodeSource } from "./dtos.ts";
import type {
  RuntimeCredentialResponse,
  SourceEnvelope,
  WorkspaceLaunchRequest,
  WorkspaceLaunchListResponse,
  WorkspaceLaunchResponse,
  WorkspaceListData,
  WorkspaceRenewalRequest,
  WorkspaceRenewalResponse,
  WorkspaceRuntimeDTO
} from "./dtos.ts";
import { postJson, getJson, type ApiError } from "./console-api.ts";

const terminalLaunchStatuses = new Set(["succeeded", "failed", "refunded"]);

async function sourceRequest<T>(request: () => Promise<unknown>): Promise<SourceEnvelope<T>> {
  try {
    return decodeSource<T>(await request());
  } catch (error) {
    const payload = (error as ApiError).payload;
    if (payload !== undefined) {
      try {
        return decodeSource<T>(payload);
      } catch {
        // Preserve the original error when no valid source envelope was returned.
      }
    }
    throw error;
  }
}

export function isTerminalWorkspaceLaunch(status: string): boolean {
  return terminalLaunchStatuses.has(status);
}

export function workspaceLaunchIdempotencyKey(): string {
  return `workspace-launch:${crypto.randomUUID()}`;
}

export async function launchWorkspace(
  input: WorkspaceLaunchRequest,
  csrfToken: string,
  idempotencyKey: string
): Promise<WorkspaceLaunchResponse> {
  try {
    return decodeDto<WorkspaceLaunchResponse>(await postJson<unknown>("/api/workspace-launches", input, csrfToken, idempotencyKey));
  } catch (error) {
    const apiError = error as ApiError;
    if (apiError.payload !== undefined) throw error;
    const unknown: ApiError = new Error("workspace_launch_unknown", { cause: error });
    unknown.payload = { status: "unknown", retryable: true };
    throw unknown;
  }
}

export function getWorkspaceLaunch(operationId: string): Promise<WorkspaceLaunchResponse> {
  return getJson<unknown>(`/api/workspace-launches/${encodeURIComponent(operationId)}`).then(decodeDto<WorkspaceLaunchResponse>);
}

export function getWorkspaceLaunches(): Promise<WorkspaceLaunchListResponse> {
  return getJson<unknown>("/api/workspace-launches").then((value) => {
    if (!Array.isArray(value)) throw new Error("invalid_workspace_launch_list");
    return value.map(decodeDto<WorkspaceLaunchResponse>);
  });
}

export function getWorkspaces(): Promise<SourceEnvelope<WorkspaceListData>> {
  return sourceRequest<WorkspaceListData>(() => getJson<unknown>("/api/workspaces"));
}

export function getWorkspaceRuntimeStatus(workspaceId: string): Promise<SourceEnvelope<WorkspaceRuntimeDTO>> {
  return sourceRequest<WorkspaceRuntimeDTO>(() => getJson<unknown>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-status`
  ));
}

export function revealWorkspaceCredentials(workspaceId: string, csrfToken: string): Promise<RuntimeCredentialResponse> {
  return postJson<unknown>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-credentials/reveal`,
    {},
    csrfToken
  ).then(decodeDto<RuntimeCredentialResponse>);
}

export function rotateWorkspaceCredentials(
  workspaceId: string,
  csrfToken: string,
  idempotencyKey: string
): Promise<RuntimeCredentialResponse> {
  return postJson<unknown>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-credentials/rotate`,
    {},
    csrfToken,
    idempotencyKey
  ).then(decodeDto<RuntimeCredentialResponse>);
}

export function updateWorkspaceRenewal(
  workspaceId: string,
  input: WorkspaceRenewalRequest,
  csrfToken: string,
  idempotencyKey = `workspace-renewal:${crypto.randomUUID()}`
): Promise<WorkspaceRenewalResponse> {
  return postJson<unknown>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/auto-renew`,
    input,
    csrfToken,
    idempotencyKey
  ).then(decodeDto<WorkspaceRenewalResponse>);
}
