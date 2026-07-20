import { pathToFileURL } from "node:url";

import {
  assertPublicHttpsUrl,
  mutationApprovalFromJson,
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

const PROVIDER_ACCEPTANCE_SLOT_KEYS = Object.freeze([
  "id", "accountId", "workspaceId", "workspaceUrl", "computeAllocationId", "computeProviderId",
  "nodePoolId", "storageId", "storageProviderId", "persistentVolumeId", "attachmentId"
]);
const PROVIDER_ACCEPTANCE_MANUAL_REVIEW_REASONS = new Set([
  "provider_acceptance_compute_result_unknown",
  "provider_acceptance_compute_state_ambiguous",
  "provider_acceptance_storage_result_unknown",
  "provider_acceptance_storage_state_ambiguous",
  "provider_acceptance_attachment_result_unknown",
  "provider_acceptance_attachment_state_ambiguous",
  "provider_acceptance_workspace_state_ambiguous",
  "provider_acceptance_runtime_result_unknown",
  "provider_acceptance_receipt_failed",
  "provider_acceptance_audit_failed",
  "provider_acceptance_state_ambiguous"
]);

function sleep(ms) {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function hasExactKeys(value, keys) {
  return value !== null && typeof value === "object" && !Array.isArray(value) &&
    Object.keys(value).length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

async function postAcceptance({ fetchImpl, origin, acceptanceToken, slotId, slot, accountId, confirmation, environmentApproved, purchaseBudget, maxApprovedProviderCost, signal, timeoutMs }) {
  const response = await fetchImpl(`${origin}/api/operator/provider-acceptance`, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-opl-provider-acceptance-token": acceptanceToken,
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

function validatedAcceptanceResponse(payload, slotId, accountId) {
  const status = payload?.status;
  const manualReview = status === "manual_review";
  const responseKeys = manualReview ? ["ok", "status", "slot", "reason"] : ["ok", "status", "slot"];
  if (!hasExactKeys(payload, responseKeys) || !hasExactKeys(payload.slot, PROVIDER_ACCEPTANCE_SLOT_KEYS)) return null;

  const slot = Object.fromEntries(PROVIDER_ACCEPTANCE_SLOT_KEYS.map((key) => [key, payload.slot[key]]));
  if (slot.id !== slotId || slot.accountId !== accountId || PROVIDER_ACCEPTANCE_SLOT_KEYS.some((key) => typeof slot[key] !== "string")) return null;
  if (["ready", "reused"].includes(status)) {
    if (payload.ok !== true || PROVIDER_ACCEPTANCE_SLOT_KEYS.slice(2).some((key) => slot[key].trim() === "")) return null;
    return { ok: true, status, slot };
  }
  if (status === "in_progress") return payload.ok === false ? { ok: false, status, slot } : null;
  if (manualReview && payload.ok === false && PROVIDER_ACCEPTANCE_MANUAL_REVIEW_REASONS.has(payload.reason)) {
    return { ok: false, status, slot, reason: payload.reason };
  }
  return null;
}

export async function runProviderAcceptance({
  origin,
  acceptanceToken,
  slotId,
  accountId,
  confirmation,
  environmentApproved,
  purchaseBudget,
  maxApprovedProviderCost,
  gatewayWriteAllowed = false,
  providerWriteAllowed = false,
  mutationApprovalJson = "",
  mutationApprovalId = "",
  attempts = 90,
  retryDelayMs = 10_000,
  requestTimeoutMs = 30_000,
  manifestPath = "",
  signal,
  fetchImpl = globalThis.fetch
} = {}) {
  if (confirmation !== PROVIDER_ACCEPTANCE_CONFIRMATION) throw new Error("provider_acceptance_confirmation_required");
  if (!gatewayWriteAllowed || !providerWriteAllowed) throw new Error("provider_acceptance_write_allow_flags_required");
  if (!String(acceptanceToken || "").trim()) throw new Error("provider_acceptance_token_required");
  const slot = PROVIDER_ACCEPTANCE_SLOTS[slotId];
  if (!slot) throw new Error("provider_acceptance_slot_fixed");
  if (accountId !== slot.accountId) throw new Error("provider_acceptance_account_fixed");
  mutationApprovalFromJson(mutationApprovalJson, {
    approvalId: mutationApprovalId,
    accountId,
    workspaceId: `primary:${accountId}`,
    resourceIds: [slotId]
  }, "provider_acceptance");
  if (environmentApproved !== true) throw new Error("provider_acceptance_environment_approval_required");
  if (purchaseBudget !== 1) throw new Error("provider_acceptance_purchase_budget_invalid");
  if (!Number.isFinite(maxApprovedProviderCost) || maxApprovedProviderCost <= 0) throw new Error("provider_acceptance_provider_cost_approval_required");
  if (!Number.isInteger(attempts) || attempts < 1 || attempts > 120 || !Number.isFinite(retryDelayMs) || retryDelayMs < 0) throw new Error("provider_acceptance_retry_config_invalid");
  if (!Number.isInteger(requestTimeoutMs) || requestTimeoutMs < 1 || requestTimeoutMs > 300_000) throw new Error("provider_acceptance_request_timeout_invalid");

  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const payload = await postAcceptance({ fetchImpl, origin: normalizedOrigin, acceptanceToken, slotId, slot, accountId, confirmation, environmentApproved, purchaseBudget, maxApprovedProviderCost, signal, timeoutMs: requestTimeoutMs });
    if (!["ready", "reused", "in_progress", "manual_review"].includes(payload?.status)) throw new Error("provider_acceptance_invalid_status");
    const response = validatedAcceptanceResponse(payload, slotId, accountId);
    if (!response) throw new Error("provider_acceptance_invalid_response");
    const result = { ...response, attempt, slotId };
    if (["ready", "reused"].includes(response.status)) {
      await writeVerificationManifest(manifestPath, result);
      return result;
    }
    if (response.status === "manual_review") {
      await writeVerificationManifest(manifestPath, result);
      throw new Error("provider_acceptance_manual_review");
    }
    if (attempt < attempts) await sleep(retryDelayMs);
  }
  throw new Error("provider_acceptance_timeout");
}

export async function runProviderAcceptanceCli({
  argv = process.argv.slice(2),
  env = process.env,
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl = globalThis.fetch
} = {}) {
  try {
    const args = Object.fromEntries(argv.flatMap((item, index) => item.startsWith("--")
      ? [[item.slice(2), argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[index + 1] : "true"]]
      : []));
    if (args["read-only"] === "true") {
      if (args["allow-gateway-write"] || args["allow-provider-write"] || args["approval-id"]) throw new Error("provider_acceptance_read_only_conflict");
      stdout.write(`${JSON.stringify({ ok: true, mode: "read-only", evidenceLevel: "read-only", writesPerformed: 0 }, null, 2)}\n`);
      return 0;
    }
    if (args["allow-gateway-write"] || args["allow-provider-write"] || args["approval-id"]) {
      if (args["allow-gateway-write"] !== "true" || args["allow-provider-write"] !== "true") throw new Error("provider_acceptance_write_allow_flags_required");
      const accountId = env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID || "";
      const slotId = env.OPL_PROVIDER_ACCEPTANCE_SLOT_ID || "";
      mutationApprovalFromJson(env.OPL_VERIFY_MUTATION_APPROVAL_JSON, {
        approvalId: args["approval-id"] || "",
        accountId,
        workspaceId: accountId ? `primary:${accountId}` : "",
        resourceIds: slotId ? [slotId] : []
      }, "provider_acceptance");
    }
    const result = await runProviderAcceptance({
      origin: env.OPL_CONSOLE_ORIGIN,
      acceptanceToken: env.OPL_PROVIDER_ACCEPTANCE_TOKEN,
      slotId: env.OPL_PROVIDER_ACCEPTANCE_SLOT_ID,
      accountId: env.OPL_PROVIDER_ACCEPTANCE_ACCOUNT_ID,
      confirmation: env.OPL_PROVIDER_ACCEPTANCE_CONFIRMATION,
      environmentApproved: env.OPL_PROVIDER_ACCEPTANCE_ENVIRONMENT_APPROVED === "true",
      purchaseBudget: Number(env.OPL_PROVIDER_ACCEPTANCE_PURCHASE_BUDGET),
      maxApprovedProviderCost: Number(env.OPL_PROVIDER_ACCEPTANCE_MAX_APPROVED_PROVIDER_COST),
      gatewayWriteAllowed: args["allow-gateway-write"] === "true",
      providerWriteAllowed: args["allow-provider-write"] === "true",
      mutationApprovalJson: env.OPL_VERIFY_MUTATION_APPROVAL_JSON,
      mutationApprovalId: args["approval-id"] || "",
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
