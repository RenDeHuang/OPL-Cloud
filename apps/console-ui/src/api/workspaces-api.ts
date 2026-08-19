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
  WorkspaceGatewayBudgetDTO,
  WorkspaceGatewayBudgetUpdateRequest,
  WorkspaceListData,
  WorkspaceDTO,
  WorkspaceRenewalRequest,
  WorkspaceRenewalResponse,
  WorkspaceRuntimeDTO
} from "./dtos.ts";
import { deleteJson, postJson, getJson, patchJson, type ApiError } from "./console-api.ts";

const terminalLaunchStatuses = new Set(["succeeded", "failed", "refunded"]);
const workspaceGatewayBudgetStatuses = new Set(["active", "disabled", "quota_exhausted", "expired"]);
const workspaceGatewayBudgetFields = [
  "workspaceId", "keyId", "status", "quotaUsdMicros", "quotaUsedUsdMicros",
  "rateLimit5hUsdMicros", "rateLimit1dUsdMicros", "rateLimit7dUsdMicros",
  "usage5hUsdMicros", "usage1dUsdMicros", "usage7dUsdMicros", "enabled", "updatedAt"
] as const;
const workspaceGatewayBudgetMicrosFields = [
  "quotaUsdMicros", "quotaUsedUsdMicros", "rateLimit5hUsdMicros", "rateLimit1dUsdMicros",
  "rateLimit7dUsdMicros", "usage5hUsdMicros", "usage1dUsdMicros", "usage7dUsdMicros"
] as const;

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

function isInt64DecimalString(value: unknown, positive = false): value is string {
  if (typeof value !== "string" || !/^(0|[1-9]\d*)$/.test(value) || positive && value === "0") return false;
  return value.length < 19 || value.length === 19 && value <= "9223372036854775807";
}

function hasExactFields(value: Record<string, unknown>, fields: readonly string[]) {
  const actual = Object.keys(value).sort();
  const expected = [...fields].sort();
  return actual.length === expected.length && actual.every((field, index) => field === expected[index]);
}

async function workspaceGatewayBudgetSourceRequest(
  request: () => Promise<unknown>,
  workspaceId: string,
  keyId: string
): Promise<SourceEnvelope<WorkspaceGatewayBudgetDTO>> {
  if (!workspaceId || !isInt64DecimalString(keyId, true)) throw new Error("invalid_workspace_gateway_budget_identity");
  const source = await sourceRequest<Record<string, unknown>>(request);
  if (source.source !== "sub2api") throw new Error("invalid_workspace_gateway_budget_source");
  if (source.available === false) return source;
  const data = source.data;
  if (source.status !== "available" || !data || !hasExactFields(data, workspaceGatewayBudgetFields)
    || data.workspaceId !== workspaceId || data.keyId !== keyId
    || !isInt64DecimalString(data.keyId, true)
    || typeof data.status !== "string" || !workspaceGatewayBudgetStatuses.has(data.status)
    || typeof data.enabled !== "boolean"
    || data.updatedAt !== null && (typeof data.updatedAt !== "string" || !data.updatedAt.trim())
    || workspaceGatewayBudgetMicrosFields.some((field) => !isInt64DecimalString(data[field]))) {
    throw new Error("invalid_workspace_gateway_budget_source");
  }
  return { ...source, data: data as unknown as WorkspaceGatewayBudgetDTO };
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

export function getWorkspaceGatewayBudget(workspaceId: string, keyId: string): Promise<SourceEnvelope<WorkspaceGatewayBudgetDTO>> {
  return workspaceGatewayBudgetSourceRequest(
    () => getJson<unknown>(`/api/workspaces/${encodeURIComponent(workspaceId)}/gateway-budget`),
    workspaceId,
    keyId
  );
}

export function updateWorkspaceGatewayBudget(
  workspaceId: string,
  keyId: string,
  input: WorkspaceGatewayBudgetUpdateRequest,
  csrfToken: string,
  idempotencyKey: string
): Promise<SourceEnvelope<WorkspaceGatewayBudgetDTO>> {
  const payload: WorkspaceGatewayBudgetUpdateRequest = {};
  if (input.quotaUsdMicros !== undefined) payload.quotaUsdMicros = input.quotaUsdMicros;
  if (input.rateLimit5hUsdMicros !== undefined) payload.rateLimit5hUsdMicros = input.rateLimit5hUsdMicros;
  if (input.rateLimit1dUsdMicros !== undefined) payload.rateLimit1dUsdMicros = input.rateLimit1dUsdMicros;
  if (input.rateLimit7dUsdMicros !== undefined) payload.rateLimit7dUsdMicros = input.rateLimit7dUsdMicros;
  if (input.enabled !== undefined) payload.enabled = input.enabled;
  if (input.resetQuota !== undefined) payload.resetQuota = input.resetQuota;
  if (input.resetRateLimitUsage !== undefined) payload.resetRateLimitUsage = input.resetRateLimitUsage;
  return workspaceGatewayBudgetSourceRequest(
    () => patchJson<unknown>(
      `/api/workspaces/${encodeURIComponent(workspaceId)}/gateway-budget`,
      payload,
      csrfToken,
      idempotencyKey
    ),
    workspaceId,
    keyId
  );
}

export function revealWorkspaceCredentials(
  workspaceId: string,
  csrfToken: string,
  idempotencyKey = `runtime-credential-reveal:${crypto.randomUUID()}`
): Promise<RuntimeCredentialResponse> {
  return postJson<unknown>(
    `/api/workspaces/${encodeURIComponent(workspaceId)}/runtime-credentials/reveal`,
    {},
    csrfToken,
    idempotencyKey
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
