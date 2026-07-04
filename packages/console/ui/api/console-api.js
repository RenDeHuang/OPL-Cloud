export async function postJson(path, body = {}, csrfToken = "") {
  const headers = {
    "content-type": "application/json"
  };
  if (csrfToken) headers["x-opl-csrf"] = csrfToken;
  const response = await fetch(path, {
    method: "POST",
    headers,
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) {
    const error = new Error(payload.safeMessage || payload.error || "request_failed");
    error.payload = payload;
    throw error;
  }
  return payload;
}

export const api = postJson;

export function operationEnvelope(payload, defaults = {}) {
  const resourceId = payload?.id || payload?.workspaceId || payload?.resourceId || defaults.resourceId || "";
  const failureReason = payload?.safeMessage || payload?.failureReason || payload?.error || "";
  return {
    ok: !failureReason,
    status: failureReason ? "failed" : defaults.status || payload?.operationStatus || "completed",
    operationId: payload?.operationId || defaults.operationId || "",
    resourceId,
    failureReason,
    costImpact: {
      holdAmount: payload?.holdAmount,
      hourlyPrice: payload?.hourlyPrice,
      hourlyEstimate: payload?.hourlyEstimate,
      balanceImpact: payload?.balanceImpact
    },
    next: defaults.next || {},
    ...payload
  };
}

export async function getJson(path) {
  const response = await fetch(path);
  const payload = await response.json();
  if (!response.ok || payload.ok === false) {
    const error = new Error(payload.safeMessage || payload.error || "request_failed");
    error.payload = payload;
    throw error;
  }
  return payload;
}
