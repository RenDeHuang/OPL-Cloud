import { pathToFileURL } from "node:url";

import {
  assertPublicHttpsUrl,
  login,
  writeVerificationManifest
} from "./production-verifier.ts";

export const PROVIDER_ACCEPTANCE_CONFIRMATION = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS";
const SLOT_ID = "verification-slot-01";
const IDEMPOTENCY_KEY = `provider-acceptance:${SLOT_ID}`;

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function operatorFromSeed(raw) {
  let users;
  try {
    users = JSON.parse(raw || "null");
  } catch {
    throw new Error("provider_acceptance_operator_credentials_required");
  }
  const operators = Array.isArray(users)
    ? users.filter((user) => ["admin", "operator"].includes(user?.role) && user.email && user.password)
    : [];
  if (operators.length !== 1) throw new Error("provider_acceptance_operator_credentials_required");
  return operators[0];
}

function safeResult(value) {
  if (Array.isArray(value)) return value.map(safeResult);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !/(cookie|password|secret|csrf|apiKey)/i.test(key))
    .map(([key, nested]) => [key, safeResult(nested)]));
}

async function postAcceptance({ fetchImpl, origin, auth, accountId, confirmation, signal, timeoutMs }) {
  const response = await fetchImpl(`${origin}/api/operator/provider-acceptance`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      cookie: auth.cookie,
      "x-opl-csrf": auth.csrfToken,
      "idempotency-key": IDEMPOTENCY_KEY
    },
    body: JSON.stringify({ accountId, confirmation, slotId: SLOT_ID }),
    signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(timeoutMs)]) : AbortSignal.timeout(timeoutMs)
  });
  const text = await response.text();
  let payload;
  try {
    payload = text ? JSON.parse(text) : {};
  } catch {
    throw new Error("provider_acceptance_invalid_response");
  }
  if (!response.ok) throw new Error(`provider_acceptance_request_failed:${response.status}:${payload?.error || "unknown"}`);
  return payload;
}

export async function runProviderAcceptance({
  origin,
  authUsersJson,
  accountId,
  confirmation,
  attempts = 90,
  retryDelayMs = 10_000,
  requestTimeoutMs = 30_000,
  manifestPath = "",
  signal,
  fetchImpl = globalThis.fetch
} = {}) {
  if (confirmation !== PROVIDER_ACCEPTANCE_CONFIRMATION) throw new Error("provider_acceptance_confirmation_required");
  if (!/^[A-Za-z0-9._:-]{1,128}$/.test(String(accountId || ""))) throw new Error("provider_acceptance_account_id_required");
  if (!Number.isInteger(attempts) || attempts < 1 || attempts > 120 || !Number.isFinite(retryDelayMs) || retryDelayMs < 0) throw new Error("provider_acceptance_retry_config_invalid");
  if (!Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) throw new Error("provider_acceptance_request_timeout_invalid");

  const operator = operatorFromSeed(authUsersJson);
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const auth = await login({ fetchImpl, origin: normalizedOrigin, email: operator.email, password: operator.password, signal, timeoutMs: requestTimeoutMs });

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const payload = await postAcceptance({ fetchImpl, origin: normalizedOrigin, auth, accountId, confirmation, signal, timeoutMs: requestTimeoutMs });
    const result = safeResult({ ...payload, attempt, slotId: SLOT_ID });
    if (["ready", "reused"].includes(payload.status) && payload.slot?.id === SLOT_ID) {
      await writeVerificationManifest(manifestPath, result);
      return result;
    }
    if (payload.status === "manual_review") {
      await writeVerificationManifest(manifestPath, result);
      throw new Error("provider_acceptance_manual_review");
    }
    if (payload.status !== "in_progress") throw new Error("provider_acceptance_invalid_status");
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw new Error("provider_acceptance_timeout");
}

export async function runProviderAcceptanceCli({
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  try {
    const result = await runProviderAcceptance({
      origin: env.OPL_CONSOLE_ORIGIN,
      authUsersJson: env.OPL_PROVIDER_ACCEPTANCE_AUTH_USERS_JSON,
      accountId: env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID,
      confirmation: env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION,
      attempts: Number(env.OPL_PROVIDER_ACCEPTANCE_ATTEMPTS || 90),
      retryDelayMs: Number(env.OPL_PROVIDER_ACCEPTANCE_RETRY_DELAY_MS || 10_000),
      requestTimeoutMs: Number(env.OPL_PROVIDER_ACCEPTANCE_REQUEST_TIMEOUT_MS || 30_000),
      manifestPath: env.OPL_PROVIDER_ACCEPTANCE_MANIFEST_PATH || "",
      fetchImpl
    });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify({ ok: false, error: error.message }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  runProviderAcceptanceCli().then((code) => { process.exitCode = code; });
}
