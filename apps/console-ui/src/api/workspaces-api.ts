import { decodeDto, decodeSource } from "./dtos.ts";
import type {
  AvailableSource,
  RuntimeCredentialResponse,
  SourceEnvelope,
  WorkspaceLaunchRequest,
  WorkspaceLaunchListResponse,
  WorkspaceLaunchResponse,
  WorkspaceDeleteCommandResult,
  WorkspaceDeleteResponse,
  WorkspaceListData,
  WorkspaceDTO,
  WorkspaceRenewalRequest,
  WorkspaceRenewalResponse,
  WorkspaceRuntimeDTO
} from "./dtos.ts";
import { deleteJson, postJson, getJson, type ApiError } from "./console-api.ts";

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

export function workspaceDeleteIdempotencyKey(workspaceId: string): string {
  return `workspace-delete:${workspaceId}:${crypto.randomUUID()}`;
}

export async function launchWorkspace(
  input: WorkspaceLaunchRequest,
  csrfToken: string,
  idempotencyKey: string
): Promise<WorkspaceLaunchResponse> {
  try {
    return decodeDto<WorkspaceLaunchResponse>(await postJson<unknown>("/api/workspace-launches", input, csrfToken, idempotencyKey, 60_000));
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

function workspaceDeleteUnavailable(error: ApiError): boolean {
  if (error.status === 405 || error.status === 501) return true;
  if (error.status !== 404) return false;
  const payload = error.payload && typeof error.payload === "object"
    ? error.payload as Record<string, unknown>
    : null;
  return payload?.error !== "workspace_not_found";
}

export async function deleteWorkspace(
  workspaceId: string,
  csrfToken: string,
  idempotencyKey: string
): Promise<WorkspaceDeleteCommandResult> {
  try {
    const dto = decodeDto<Record<string, unknown>>(await deleteJson<unknown>(
      `/api/workspaces/${encodeURIComponent(workspaceId)}`,
      csrfToken,
      idempotencyKey
    ));
    if (dto.workspaceId !== workspaceId || typeof dto.status !== "string" || !dto.status.trim()) {
      throw new Error("invalid_workspace_delete_response");
    }
    return {
      available: true,
      data: {
        workspaceId,
        status: dto.status,
        ...(typeof dto.operationId === "string" && dto.operationId ? { operationId: dto.operationId } : {})
      } satisfies WorkspaceDeleteResponse
    };
  } catch (error) {
    if (workspaceDeleteUnavailable(error as ApiError)) {
      return { available: false, reasonCode: "workspace_delete_unavailable" };
    }
    throw error;
  }
}

export function getWorkspaces(page = 1, pageSize = 20): Promise<SourceEnvelope<WorkspaceListData>> {
  const query = new URLSearchParams({ page: String(page), pageSize: String(pageSize) });
  return sourceRequest<WorkspaceListData>(() => getJson<unknown>(`/api/workspaces?${query}`));
}

export async function findWorkspaceInPages(
  workspaceId: string,
  pageSize = 50
): Promise<SourceEnvelope<WorkspaceDTO | null>> {
  if (!Number.isSafeInteger(pageSize) || pageSize < 1) {
    throw new Error("workspace_list_page_size_invalid");
  }

  let page = 1;
  let total: number | null = null;
  let inspected = 0;
  let firstPage: AvailableSource<WorkspaceListData> | null = null;

  while (true) {
    const result = await getWorkspaces(page, pageSize);
    if (result.available === false) return result;
    if (result.data.page !== page || result.data.pageSize !== pageSize) {
      throw new Error("workspace_list_page_mismatch");
    }

    if (!firstPage) {
      firstPage = result;
      total = result.data.total;
      if (!Number.isSafeInteger(total) || total < 0) throw new Error("workspace_list_total_invalid");
    } else if (result.data.total !== total) {
      throw new Error("workspace_list_total_mismatch");
    }

    const workspace = result.data.items.find((item) => item.id === workspaceId);
    if (workspace) {
      return {
        ...result,
        status: "available",
        data: workspace
      };
    }

    inspected += result.data.items.length;
    if (inspected > total) throw new Error("workspace_list_page_overflow");
    if (inspected === total) {
      return {
        ...firstPage,
        status: "empty",
        data: null
      };
    }
    if (result.data.items.length === 0) throw new Error("workspace_list_page_incomplete");
    page += 1;
  }
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
