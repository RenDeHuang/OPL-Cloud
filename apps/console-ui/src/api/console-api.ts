export type JsonObject = Record<string, unknown>;

export type ApiError = Error & { payload?: unknown };

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

export async function postJson<T>(path: string, body: unknown = {}, csrfToken = "", idempotencyKey = ""): Promise<T> {
  const headers: Record<string, string> = { "content-type": "application/json" };
  if (csrfToken) headers["x-opl-csrf"] = csrfToken;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  const response = await fetch(path, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(10_000)
  });
  const payload = await responsePayload(response);
  if (!response.ok || asObject(payload).ok === false) throwApiError(payload);
  return payload as T;
}

export const api = postJson;

export function operationEnvelope(payload: unknown = {}, defaults: JsonObject = {}) {
  const object = asObject(payload);
  const resourceId = String(object.id || object.workspaceId || object.resourceId || defaults.resourceId || "");
  const failureReason = object.safeMessage || object.failureReason || object.error
    ? customerSafeMessage(object)
    : "";
  return {
    ok: !failureReason,
    status: failureReason ? "failed" : String(defaults.status || object.operationStatus || "completed"),
    operationId: String(object.operationId || defaults.operationId || ""),
    resourceId,
    failureReason,
    costImpact: {
      monthlyPriceCnyCents: object.monthlyPriceCnyCents,
      chargeUsdMicros: object.chargeUsdMicros,
      paidThrough: object.paidThrough,
      autoRenew: object.autoRenew
    },
    next: defaults.next || {},
    ...object
  };
}

export async function getJson<T>(path: string, { signal }: { signal?: AbortSignal } = {}): Promise<T> {
  const timeout = AbortSignal.timeout(10_000);
  const requestSignal = signal ? AbortSignal.any([signal, timeout]) : timeout;
  const response = await fetch(path, { signal: requestSignal });
  const payload = await responsePayload(response);
  if (!response.ok || asObject(payload).ok === false) throwApiError(payload);
  return payload as T;
}
