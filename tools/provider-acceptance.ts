import { pathToFileURL } from "node:url";

import {
  assertPublicHttpsUrl,
  writeVerificationManifest
} from "./production-verifier.ts";

export const PROVIDER_ACCEPTANCE_CONFIRMATION = "I_UNDERSTAND_THIS_BUYS_ONE_PREPAID_CVM_AND_CBS";
export const PROVIDER_ACCEPTANCE_SLOTS = Object.freeze({
  "verification-slot-basic-01": Object.freeze({
    accountId: "acct-verification-slot-basic-01",
    idempotencyKey: "provider-acceptance:verification-slot-basic-01"
  }),
  "verification-slot-pro-01": Object.freeze({
    accountId: "acct-verification-slot-pro-01",
    idempotencyKey: "provider-acceptance:verification-slot-pro-01"
  })
});

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function safeResult(value) {
  if (Array.isArray(value)) return value.map(safeResult);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !/(cookie|password|secret|csrf|apiKey)/i.test(key))
    .map(([key, nested]) => [key, safeResult(nested)]));
}

async function postAcceptance({ fetchImpl, origin, operatorToken, slotId, slot, accountId, confirmation, environmentApproved, purchaseBudget, maxApprovedProviderCost, signal, timeoutMs }) {
  const response = await fetchImpl(`${origin}/api/operator/provider-acceptance`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-opl-operator-token": operatorToken,
      "idempotency-key": slot.idempotencyKey
    },
    body: JSON.stringify({ accountId, confirmation, slotId, environmentApproved, purchaseBudget, maxApprovedProviderCost }),
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
  operatorToken,
  slotId,
  accountId,
  confirmation,
  environmentApproved,
  purchaseBudget,
  maxApprovedProviderCost,
  attempts = 90,
  retryDelayMs = 10_000,
  requestTimeoutMs = 30_000,
  manifestPath = "",
  signal,
  fetchImpl = globalThis.fetch
} = {}) {
  if (confirmation !== PROVIDER_ACCEPTANCE_CONFIRMATION) throw new Error("provider_acceptance_confirmation_required");
  if (!String(operatorToken || "").trim()) throw new Error("provider_acceptance_operator_token_required");
  const slot = PROVIDER_ACCEPTANCE_SLOTS[slotId];
  if (!slot) throw new Error("provider_acceptance_slot_fixed");
  if (accountId !== slot.accountId) throw new Error("provider_acceptance_account_fixed");
  if (environmentApproved !== true) throw new Error("provider_acceptance_environment_approval_required");
  if (purchaseBudget !== 1) throw new Error("provider_acceptance_purchase_budget_invalid");
  if (!Number.isFinite(maxApprovedProviderCost) || maxApprovedProviderCost <= 0) throw new Error("provider_acceptance_provider_cost_approval_required");
  if (!Number.isInteger(attempts) || attempts < 1 || attempts > 120 || !Number.isFinite(retryDelayMs) || retryDelayMs < 0) throw new Error("provider_acceptance_retry_config_invalid");
  if (!Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) throw new Error("provider_acceptance_request_timeout_invalid");

  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const payload = await postAcceptance({ fetchImpl, origin: normalizedOrigin, operatorToken, slotId, slot, accountId, confirmation, environmentApproved, purchaseBudget, maxApprovedProviderCost, signal, timeoutMs: requestTimeoutMs });
    const result = safeResult({ ...payload, attempt, slotId });
    if (["ready", "reused"].includes(payload.status) && payload.slot?.id === slotId) {
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
      operatorToken: env.OPL_PROVIDER_ACCEPTANCE_OPERATOR_TOKEN,
      slotId: env.OPL_PROVIDER_ACCEPTANCE_SLOT_ID,
      accountId: env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID,
      confirmation: env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION,
      environmentApproved: env.OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED === "true",
      purchaseBudget: Number(env.OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET),
      maxApprovedProviderCost: Number(env.OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST),
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
