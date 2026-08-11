export type JsonObject = Record<string, unknown>;

export type ApiError = Error & { payload?: unknown };

export function controlPlaneApiPath(path: string): string {
  const base = "https://opl-cloud.invalid";
  const resolved = new URL(path, base);
  if (!path.startsWith("/api/") || resolved.origin !== base || !resolved.pathname.startsWith("/api/")) {
    throw new Error("control_plane_api_path_required");
  }
  return path;
}

function asObject(value: unknown): JsonObject {
  return value && typeof value === "object" ? value as JsonObject : {};
}

export function customerSafeMessage(payload: unknown = {}, fallback = "request_failed") {
  const object = asObject(payload);
  const raw = String(object.safeMessage || object.error || fallback);
  if (/workspace_url_failed|workspace_runtime_not_ready|workspace_url_not_ready/i.test(raw)) {
    return "正在分发 Docker，预计 3-5 分钟，请稍后再打开 URL。";
  }
  return raw;
}

async function responsePayload(response: Response): Promise<unknown> {
  return response.json().catch(() => null);
}

function throwApiError(payload: unknown): never {
  const error: ApiError = new Error(customerSafeMessage(payload));
  error.payload = payload;
  throw error;
}

async function writeJson<T>(method: "POST" | "PUT" | "PATCH" | "DELETE", path: string, body: unknown, csrfToken: string, idempotencyKey: string, timeoutMs: number, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (csrfToken) headers["x-opl-csrf"] = csrfToken;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  const timeout = AbortSignal.timeout(timeoutMs);
  const requestSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
  const response = await fetch(controlPlaneApiPath(path), {
    method,
    headers,
    body: JSON.stringify(body),
    signal: requestSignal
  });
  const payload = await responsePayload(response);
  if (!response.ok || asObject(payload).ok === false) throwApiError(payload);
  return payload as T;
}

export function postJson<T>(path: string, body: unknown = {}, csrfToken = "", idempotencyKey = "", timeoutMs = 10_000, signal?: AbortSignal): Promise<T> {
  return writeJson<T>("POST", path, body, csrfToken, idempotencyKey, timeoutMs, signal);
}

export function patchJson<T>(path: string, body: unknown, csrfToken = "", idempotencyKey = ""): Promise<T> {
  return writeJson<T>("PATCH", path, body, csrfToken, idempotencyKey, 10_000);
}

export function putJson<T>(path: string, body: unknown, csrfToken = "", idempotencyKey = ""): Promise<T> {
  return writeJson<T>("PUT", path, body, csrfToken, idempotencyKey, 10_000);
}

export function deleteJson<T>(path: string, csrfToken = "", idempotencyKey = ""): Promise<T> {
  return writeJson<T>("DELETE", path, {}, csrfToken, idempotencyKey, 10_000);
}

export async function getJson<T>(path: string, { signal }: { signal?: AbortSignal } = {}): Promise<T> {
  const timeout = AbortSignal.timeout(10_000);
  const requestSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
  const response = await fetch(controlPlaneApiPath(path), { signal: requestSignal });
  const payload = await responsePayload(response);
  if (!response.ok || asObject(payload).ok === false) throwApiError(payload);
  return payload as T;
}
