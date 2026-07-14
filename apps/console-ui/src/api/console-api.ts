type JsonRecord = Record<string, any>;
type ApiError = Error & { payload?: JsonRecord };

export function customerSafeMessage(payload: JsonRecord = {}, fallback = "request_failed") {
  const raw = String(payload.safeMessage || payload.error || fallback);
  if (/upstream_unavailable|bad gateway|workspace_url_failed|workspace_runtime_not_ready|workspace_url_not_ready|502|503/i.test(raw)) {
    return "正在分发 Docker，预计 3-5 分钟，请稍后再打开 URL。";
  }
  return raw;
}

export async function postJson(path: string, body: JsonRecord = {}, csrfToken = "", idempotencyKey = "") {
  const headers: Record<string, string> = {
    "content-type": "application/json"
  };
  if (csrfToken) headers["x-opl-csrf"] = csrfToken;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  const response = await fetch(path, {
    method: "POST",
    headers,
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) {
    const error: ApiError = new Error(customerSafeMessage(payload));
    error.payload = payload;
    throw error;
  }
  return payload;
}

export const api = postJson;

export function operationEnvelope(payload: JsonRecord = {}, defaults: JsonRecord = {}) {
  const resourceId = payload?.id || payload?.workspaceId || payload?.resourceId || defaults.resourceId || "";
  const failureReason = payload?.safeMessage || payload?.failureReason || payload?.error
    ? customerSafeMessage(payload)
    : "";
  return {
    ok: !failureReason,
    status: failureReason ? "failed" : defaults.status || payload?.operationStatus || "completed",
    operationId: payload?.operationId || defaults.operationId || "",
    resourceId,
    failureReason,
    costImpact: {
      monthlyPriceCnyCents: payload?.monthlyPriceCnyCents,
      chargeUsdMicros: payload?.chargeUsdMicros,
      paidThrough: payload?.paidThrough,
      autoRenew: payload?.autoRenew
    },
    next: defaults.next || {},
    ...payload
  };
}

export async function getJson(path: string) {
  const response = await fetch(path);
  const payload = await response.json();
  if (!response.ok || payload.ok === false) {
    const error: ApiError = new Error(customerSafeMessage(payload));
    error.payload = payload;
    throw error;
  }
  return payload;
}
