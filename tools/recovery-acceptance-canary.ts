import { createHash } from "node:crypto";

import {
  assertPublicHttpsUrl,
  login,
  requestJson
} from "./production-verifier.ts";

const RECOVERY_ACCEPTANCE_CANARY_MODE = "recovery_acceptance_canary";
const RECOVERY_ACCEPTANCE_CANARY_APPROVAL_KEYS = [
  "schemaVersion", "operationMode", "accountId", "launchOperationId", "mergedMainSha", "cloudImageDigest", "approvalDigest", "nonce"
];

function exactKeys(value: unknown, keys: string[]): boolean {
  return value !== null && typeof value === "object" && !Array.isArray(value) &&
    JSON.stringify(Object.keys(value as Record<string, unknown>).sort()) === JSON.stringify([...keys].sort());
}

function canonicalDigestMaterial(approval: RecoveryAcceptanceCanaryApproval): string {
  return JSON.stringify({
    accountId: approval.accountId,
    launchOperationId: approval.launchOperationId,
    mergedMainSha: approval.mergedMainSha,
    cloudImageDigest: approval.cloudImageDigest,
    nonce: approval.nonce
  });
}

function approvalDigest(approval: RecoveryAcceptanceCanaryApproval): string {
  return createHash("sha256").update(canonicalDigestMaterial(approval)).digest("hex");
}

export interface RecoveryAcceptanceCanaryApproval {
  schemaVersion: number;
  operationMode: string;
  accountId: string;
  launchOperationId: string;
  mergedMainSha: string;
  cloudImageDigest: string;
  approvalDigest: string;
  nonce: string;
}

export interface RecoveryAcceptanceCanaryOptions {
  origin: string;
  adminEmail: string;
  adminPassword: string;
  approvalJson: string | RecoveryAcceptanceCanaryApproval;
  mergedSha?: string;
  cloudImageDigest?: string;
  accountId?: string;
  launchOperationId?: string;
  enabled?: string | boolean;
  accountAllowlist?: string;
  requestTimeoutMs?: number;
  fetchImpl?: typeof fetch;
  now?: Date;
}

function parseApproval(value: string | RecoveryAcceptanceCanaryApproval): RecoveryAcceptanceCanaryApproval {
  let parsed: unknown = value;
  if (typeof value === "string") {
    try {
      parsed = JSON.parse(value);
    } catch {
      throw new Error("recovery_acceptance_canary_approval_invalid");
    }
  }
  if (!exactKeys(parsed, RECOVERY_ACCEPTANCE_CANARY_APPROVAL_KEYS)) {
    throw new Error("recovery_acceptance_canary_approval_invalid");
  }
  const approval = parsed as RecoveryAcceptanceCanaryApproval;
  if (approval.schemaVersion !== 1 || approval.operationMode !== RECOVERY_ACCEPTANCE_CANARY_MODE ||
    !/^acct-[A-Za-z0-9][A-Za-z0-9-]{1,127}$/.test(approval.accountId) ||
    !/^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$/.test(approval.launchOperationId) ||
    !/^[a-f0-9]{40}$/.test(approval.mergedMainSha) || !/^sha256:[a-f0-9]{64}$/.test(approval.cloudImageDigest) ||
    !/^[a-f0-9]{64}$/.test(approval.approvalDigest) || !/^[a-f0-9]{32,128}$/.test(approval.nonce) ||
    approvalDigest(approval) !== approval.approvalDigest) {
    throw new Error("recovery_acceptance_canary_approval_invalid");
  }
  return approval;
}

function enabledValue(value: string | boolean | undefined): boolean {
  if (value === true) return true;
  return ["1", "true"].includes(String(value || "").trim().toLowerCase());
}

function allowlisted(accountID: string, value: string | undefined): boolean {
  return String(value || "").split(",").map((item) => item.trim()).filter(Boolean).includes(accountID);
}

function redactedResult(approval: RecoveryAcceptanceCanaryApproval, response: Record<string, unknown>, reconciled: boolean, now: Date) {
  return {
    schemaVersion: 1,
    operationMode: RECOVERY_ACCEPTANCE_CANARY_MODE,
    status: "proven",
    recoveryEligible: true,
    errorCode: "none",
    release: { mergedSha: approval.mergedMainSha, cloudImageDigest: approval.cloudImageDigest },
    target: {
      accountIdSha256: createHash("sha256").update(approval.accountId).digest("hex"),
      launchOperationIdSha256: createHash("sha256").update(approval.launchOperationId).digest("hex"),
      workspaceIdSha256: typeof response.workspaceId === "string" ? createHash("sha256").update(response.workspaceId).digest("hex") : ""
    },
    approval: { approvalDigest: approval.approvalDigest },
    manualReview: {
      status: response.status,
      phase: response.phase,
      errorCode: response.errorCode,
      reconciled
    },
    controlPlaneMutationCounts: response.controlPlaneMutationCounts || { database: reconciled ? 0 : 1, sub2api: 0, tencent: 0, kubernetes: 0 },
    runnerDirectMutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    verifiedAt: now.toISOString()
  };
}

function validateResponse(response: unknown, approval: RecoveryAcceptanceCanaryApproval): Record<string, unknown> {
  if (!response || typeof response !== "object" || Array.isArray(response)) {
    throw new Error("recovery_acceptance_canary_response_invalid");
  }
  const value = response as Record<string, unknown>;
  const counts = value.controlPlaneMutationCounts as Record<string, unknown> | undefined;
  if (value.schemaVersion !== 1 || value.operationMode !== RECOVERY_ACCEPTANCE_CANARY_MODE || value.status !== "manual_review" ||
    value.phase !== "storage_fulfilling" || value.approvalDigest !== approval.approvalDigest || value.errorCode !== "recovery_acceptance_canary_manual_review" ||
    !String(value.operationId || "") || !String(value.workspaceId || "") ||
    !counts || counts.database !== 1 || counts.sub2api !== 0 || counts.tencent !== 0 || counts.kubernetes !== 0) {
    throw new Error("recovery_acceptance_canary_response_invalid");
  }
  return value;
}

async function readLaunch(requestOptions: { fetchImpl: typeof fetch; origin: string; auth: Awaited<ReturnType<typeof login>>; timeoutMs: number }, launchOperationId: string) {
  const result = await requestJson({
    ...requestOptions,
    auth: requestOptions.auth,
    path: `/api/workspace-launches/${encodeURIComponent(launchOperationId)}`
  });
  const payload = result.payload as Record<string, unknown>;
  if (payload.operationId !== launchOperationId) throw new Error("recovery_acceptance_canary_launch_identity_invalid");
  return payload;
}

export async function verifyRecoveryAcceptanceCanary(options: RecoveryAcceptanceCanaryOptions) {
  const {
    origin,
    adminEmail,
    adminPassword,
    approvalJson,
    mergedSha = "",
    cloudImageDigest = "",
    accountId = "",
    launchOperationId = "",
    enabled = false,
    accountAllowlist = "",
    requestTimeoutMs = 30_000,
    fetchImpl = globalThis.fetch,
    now = new Date()
  } = options;
  if (!enabledValue(enabled)) throw new Error("recovery_acceptance_canary_disabled");
  const approval = parseApproval(approvalJson);
  if (!allowlisted(approval.accountId, accountAllowlist) || (accountId && accountId !== approval.accountId) ||
    (launchOperationId && launchOperationId !== approval.launchOperationId) || (mergedSha && mergedSha !== approval.mergedMainSha) ||
    (cloudImageDigest && cloudImageDigest !== approval.cloudImageDigest)) {
    throw new Error("recovery_acceptance_canary_approval_invalid");
  }
  const normalizedOrigin = assertPublicHttpsUrl(origin, "public_console_origin_required", { hostname: "cloud.medopl.cn" }).origin;
  const requestOptions = { fetchImpl, origin: normalizedOrigin, timeoutMs: requestTimeoutMs };
  const auth = await login({ ...requestOptions, email: adminEmail, password: adminPassword });
  if (auth.user?.role !== "admin" || auth.user?.accountId !== "acct-admin" || !auth.csrfToken) {
    throw new Error("recovery_acceptance_canary_admin_identity_invalid");
  }
  await readLaunch({ ...requestOptions, auth }, approval.launchOperationId);
  const body = {
    accountId: approval.accountId,
    launchOperationId: approval.launchOperationId,
    mergedMainSha: approval.mergedMainSha,
    cloudImageDigest: approval.cloudImageDigest,
    approvalDigest: approval.approvalDigest,
    nonce: approval.nonce
  };
  let response: Record<string, unknown>;
  let reconciled = false;
  try {
    const result = await requestJson({
      ...requestOptions,
      auth,
      path: `/api/operator/workspace-launches/${encodeURIComponent(approval.launchOperationId)}/recovery-acceptance/manual-review`,
      method: "POST",
      headers: { "Idempotency-Key": `recovery-acceptance:${approval.approvalDigest}` },
      body
    });
    response = validateResponse(result.payload, approval);
  } catch (error) {
    const launch = await readLaunch({ ...requestOptions, auth }, approval.launchOperationId);
    const marker = launch.recoveryAcceptance as Record<string, unknown> | undefined;
    if (launch.status === "manual_review" && launch.phase === "storage_fulfilling" && marker?.approvalDigest === approval.approvalDigest) {
      response = validateResponse({
        schemaVersion: 1,
        operationMode: RECOVERY_ACCEPTANCE_CANARY_MODE,
        status: launch.status,
        phase: launch.phase,
        operationId: launch.operationId,
        workspaceId: launch.workspaceId,
        approvalDigest: marker.approvalDigest,
        errorCode: launch.errorCode,
        controlPlaneMutationCounts: { database: 1, sub2api: 0, tencent: 0, kubernetes: 0 }
      }, approval);
      reconciled = true;
    } else {
      throw error;
    }
  }
  return redactedResult(approval, response, reconciled, now);
}

function parseArgs(argv: string[]) {
  const args: Record<string, string> = {};
  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    if (!value.startsWith("--")) continue;
    const [key, inline] = value.slice(2).split("=", 2);
    args[key] = inline ?? argv[index + 1] ?? "";
    if (inline === undefined) index += 1;
  }
  return args;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const args = parseArgs(process.argv.slice(2));
  if (!args["recovery-acceptance-canary"]) {
    process.stderr.write("Usage: node tools/recovery-acceptance-canary.ts --recovery-acceptance-canary\n");
    process.exitCode = 1;
  } else {
    verifyRecoveryAcceptanceCanary({
      origin: process.env.OPL_CONSOLE_ORIGIN || "https://cloud.medopl.cn",
      adminEmail: process.env.OPL_SUB2API_ADMIN_EMAIL || "",
      adminPassword: process.env.OPL_SUB2API_ADMIN_PASSWORD || "",
      approvalJson: process.env.OPL_RECOVERY_ACCEPTANCE_CANARY_APPROVAL_JSON || "",
      mergedSha: process.env.OPL_MERGED_SHA || "",
      cloudImageDigest: process.env.OPL_CLOUD_IMAGE_DIGEST || "",
      accountId: args["account-id"] || process.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_ID || "",
      launchOperationId: args["launch-operation-id"] || process.env.OPL_RECOVERY_ACCEPTANCE_CANARY_LAUNCH_OPERATION_ID || "",
      enabled: process.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ENABLED || "",
      accountAllowlist: process.env.OPL_RECOVERY_ACCEPTANCE_CANARY_ACCOUNT_IDS || "",
      requestTimeoutMs: Number(process.env.OPL_VERIFY_REQUEST_TIMEOUT_MS || "30000")
    }).then((result) => {
      process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    }).catch((error) => {
      process.stderr.write(`${String(error?.message || error)}\n`);
      process.exitCode = 1;
    });
  }
}
