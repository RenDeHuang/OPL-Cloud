import { createHash, randomBytes } from "node:crypto";
import { mkdir, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

const shaPattern = /^[0-9a-f]{40}$/;
const recoveryRefPattern = /^[A-Za-z0-9][A-Za-z0-9._/:-]{0,511}$/;
const launchStages = Object.freeze([
  "key",
  "debit",
  "ensure_compute_allocation",
  "storage",
  "attachment",
  "secret",
  "runtime",
  "activation",
  "receipt",
  "succeeded"
]);
const providerStages = Object.freeze(["ensure_compute_allocation", "storage", "attachment", "secret", "runtime"]);

function requireExactKeys(value, keys, label) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).sort().join("\0") !== [...keys].sort().join("\0")) {
    throw new Error(`${label} shape is invalid`);
  }
}

function requireKeys(value, keys, label) {
  if (!value || typeof value !== "object" || Array.isArray(value) || keys.some((key) => !Object.prototype.hasOwnProperty.call(value, key))) {
    throw new Error(`${label} shape is invalid`);
  }
}

function requireCount(value, label) {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`${label} is invalid`);
  return value;
}

function requireRecoveryRef(value, label) {
  const normalized = String(value || "");
  const candidate = normalized.startsWith("/") ? normalized.slice(1) : normalized;
  if (!recoveryRefPattern.test(candidate) || candidate.split("/").includes("..") || candidate.includes("//")) {
    throw new Error(`${label} is invalid`);
  }
  return normalized;
}

export function recoveryIdentityDigest(value) {
  const normalized = String(value || "");
  if (!normalized) throw new Error("recovery identity is required");
  return `sha256:${createHash("sha256").update(normalized).digest("hex")}`;
}

export function noLocalJ1ExternalWrites() {
  const none = () => ({ attempted: 0, confirmed: 0, unknown: 0 });
  return {
    workspaceCreates: none(),
    keyCreates: none(),
    debits: none(),
    providerMutations: none(),
    purchaseReceipts: none()
  };
}

export function localJ1FailureDisposition({ ready = false, launchSubmitted = false, externalWrites }) {
  requireExactKeys(externalWrites, ["workspaceCreates", "keyCreates", "debits", "providerMutations", "purchaseReceipts"], "external write counts");
  const total = Object.entries(externalWrites).reduce((sum, [name, value]) => {
    requireExactKeys(value, ["attempted", "confirmed", "unknown"], `${name} write count`);
    const attempted = requireCount(value.attempted, `${name} attempted`);
    const confirmed = requireCount(value.confirmed, `${name} confirmed`);
    const unknown = requireCount(value.unknown, `${name} unknown`);
    if (confirmed + unknown > attempted) throw new Error(`${name} write count is invalid`);
    return sum + attempted;
  }, 0);
  if (ready) return Object.freeze({ cleanup: true, preserveRecoveryAuthority: false, reason: "ready" });
  if (!launchSubmitted && total === 0) {
    return Object.freeze({ cleanup: true, preserveRecoveryAuthority: false, reason: "pre_create_failure" });
  }
  return Object.freeze({ cleanup: false, preserveRecoveryAuthority: true, reason: "durable_launch_or_external_write" });
}

function normalizeAttempt(stage, attempt) {
  requireKeys(attempt, ["attempted", "confirmed", "unknown", "max"], `${stage} attempt`);
  const attempted = requireCount(attempt.attempted, `${stage} attempted`);
  const confirmed = requireCount(attempt.confirmed, `${stage} confirmed`);
  const unknown = requireCount(attempt.unknown, `${stage} unknown`);
  const max = requireCount(attempt.max, `${stage} max`);
  const pendingReadbacks = requireCount(attempt.pendingReadbacks ?? 0, `${stage} pending readbacks`);
  const maxPendingReadbacks = requireCount(attempt.maxPendingReadbacks ?? 0, `${stage} max pending readbacks`);
  const status = String(attempt.status ?? "");
  const idempotencyKey = String(attempt.idempotencyKey ?? "");
  if (attempted > max || confirmed + unknown > attempted || pendingReadbacks > maxPendingReadbacks ||
    (attempted > 0 && !idempotencyKey) || (attempt.status !== undefined && typeof attempt.status !== "string") ||
    (attempt.idempotencyKey !== undefined && typeof attempt.idempotencyKey !== "string")) {
    throw new Error(`${stage} attempt counters are invalid`);
  }
  return {
    attempted,
    confirmed,
    unknown,
    max,
    status,
    idempotencyKeyDigest: idempotencyKey ? recoveryIdentityDigest(idempotencyKey) : null,
    pendingReadbacks,
    maxPendingReadbacks
  };
}

function writeCount(attempted, confirmed, unknown) {
  return { attempted, confirmed, unknown };
}

function providerWriteCount(attempts) {
  return providerStages.reduce((count, stage) => ({
    attempted: count.attempted + attempts[stage].attempted,
    confirmed: count.confirmed + attempts[stage].confirmed,
    unknown: count.unknown + attempts[stage].unknown
  }), writeCount(0, 0, 0));
}

export function summarizeLocalJ1RecoveryReadback(launch, { launchSubmitted = true } = {}) {
  requireKeys(launch, [
    "operationId", "workspaceId", "schemaVersion", "version", "status", "stage", "continuationAttemptBudgets"
  ], "launch recovery readback");
  if (!String(launch.operationId || "") || !String(launch.workspaceId || "") ||
    !Number.isSafeInteger(launch.schemaVersion) || launch.schemaVersion <= 0 ||
    !Number.isSafeInteger(launch.version) || launch.version <= 0 ||
    typeof launch.status !== "string" || !launchStages.includes(launch.stage)) {
    throw new Error("launch recovery readback identity is invalid");
  }
  const attempts = {};
  for (const stage of launchStages.filter((candidate) => candidate !== "succeeded")) {
    if (!Object.prototype.hasOwnProperty.call(launch.continuationAttemptBudgets || {}, stage)) {
      throw new Error(`launch recovery readback is missing ${stage} attempt`);
    }
    attempts[stage] = normalizeAttempt(stage, launch.continuationAttemptBudgets[stage]);
  }
  return {
    operationIdDigest: recoveryIdentityDigest(launch.operationId),
    workspaceIdDigest: recoveryIdentityDigest(launch.workspaceId),
    schemaVersion: launch.schemaVersion,
    operationVersion: launch.version,
    status: launch.status,
    stage: launch.stage,
    attempts,
    externalWrites: {
      workspaceCreates: launchSubmitted ? writeCount(1, 1, 0) : writeCount(0, 0, 0),
      keyCreates: writeCount(attempts.key.attempted, attempts.key.confirmed, attempts.key.unknown),
      debits: writeCount(attempts.debit.attempted, attempts.debit.confirmed, attempts.debit.unknown),
      providerMutations: providerWriteCount(attempts),
      purchaseReceipts: writeCount(attempts.receipt.attempted, attempts.receipt.confirmed, attempts.receipt.unknown)
    }
  };
}

export function localJ1CleanupPlan({ ready = false, launchSubmitted = false, authority }) {
  const externalWrites = authority?.externalWrites || noLocalJ1ExternalWrites();
  const disposition = localJ1FailureDisposition({ ready, launchSubmitted, externalWrites });
  return {
    ...disposition,
    stopServices: disposition.preserveRecoveryAuthority ? [] : ["control-plane", "fabric", "ledger", "postgres"],
    removeComposeVolumes: disposition.cleanup,
    removeRecoveryRoot: disposition.cleanup
  };
}

export function createLocalJ1RecoveryArtifact({ source, failure, authority, compose }) {
  requireExactKeys(source, ["sha", "tree"], "recovery source");
  if (!shaPattern.test(source.sha) || !shaPattern.test(source.tree)) throw new Error("recovery source identity is invalid");
  requireExactKeys(failure, ["stage", "errorCode"], "recovery failure");
  if (!String(failure.stage || "") || !/^[a-z0-9_]+$/.test(String(failure.errorCode || ""))) {
    throw new Error("recovery failure classification is invalid");
  }
  requireExactKeys(compose, ["project", "recoveryRoot", "fabricSecretRoot", "postgresVolumeRefs", "providerVolumeRefs"], "recovery compose refs");
  const postgresVolumeRefs = compose.postgresVolumeRefs.map((value) => requireRecoveryRef(value, "PostgreSQL volume ref"));
  const providerVolumeRefs = compose.providerVolumeRefs.map((value) => requireRecoveryRef(value, "provider volume ref"));
  if (postgresVolumeRefs.length === 0) throw new Error("PostgreSQL recovery volume refs are required");
  if (!authority || typeof authority !== "object") throw new Error("owner-authoritative launch readback is required");
  const disposition = localJ1FailureDisposition({ launchSubmitted: true, externalWrites: authority.externalWrites });
  if (!disposition.preserveRecoveryAuthority) throw new Error("recovery artifact requires retained authority");
  return {
    schemaVersion: 1,
    kind: "opl.local-workspace.j1-recovery.v1",
    status: "RECOVERY_REQUIRED",
    source: { sha: source.sha, tree: source.tree },
    failure: { stage: failure.stage, errorCode: failure.errorCode },
    authority,
    compose: {
      project: requireRecoveryRef(compose.project, "Compose project"),
      recoveryRoot: requireRecoveryRef(compose.recoveryRoot, "recovery root"),
      fabricSecretRoot: requireRecoveryRef(compose.fabricSecretRoot, "Fabric Secret root"),
      postgresVolumeRefs,
      providerVolumeRefs
    },
    cleanup: { performed: false, reason: disposition.reason }
  };
}

export function createLocalJ1RecoveryReadbackPendingArtifact({ source, failure, operationId, workspaceId, compose }) {
  requireExactKeys(source, ["sha", "tree"], "recovery source");
  if (!shaPattern.test(source.sha) || !shaPattern.test(source.tree)) throw new Error("recovery source identity is invalid");
  requireExactKeys(failure, ["stage", "errorCode"], "recovery failure");
  if (!String(failure.stage || "") || !/^[a-z0-9_]+$/.test(String(failure.errorCode || ""))) {
    throw new Error("recovery failure classification is invalid");
  }
  requireExactKeys(compose, ["project", "recoveryRoot", "fabricSecretRoot", "postgresVolumeRefs", "providerVolumeRefs"], "recovery compose refs");
  const postgresVolumeRefs = compose.postgresVolumeRefs.map((value) => requireRecoveryRef(value, "PostgreSQL volume ref"));
  const providerVolumeRefs = compose.providerVolumeRefs.map((value) => requireRecoveryRef(value, "provider volume ref"));
  if (postgresVolumeRefs.length === 0) throw new Error("PostgreSQL recovery volume refs are required");
  const unknownWriteCount = () => ({ attempted: null, confirmed: null, unknown: null });
  return {
    schemaVersion: 1,
    kind: "opl.local-workspace.j1-recovery.v1",
    status: "RECOVERY_READBACK_REQUIRED",
    source: { sha: source.sha, tree: source.tree },
    failure: { stage: failure.stage, errorCode: failure.errorCode },
    authority: {
      operationIdDigest: recoveryIdentityDigest(operationId),
      workspaceIdDigest: recoveryIdentityDigest(workspaceId),
      readbackStatus: "unavailable",
      externalWrites: {
        workspaceCreates: unknownWriteCount(),
        keyCreates: unknownWriteCount(),
        debits: unknownWriteCount(),
        providerMutations: unknownWriteCount(),
        purchaseReceipts: unknownWriteCount()
      }
    },
    compose: {
      project: requireRecoveryRef(compose.project, "Compose project"),
      recoveryRoot: requireRecoveryRef(compose.recoveryRoot, "recovery root"),
      fabricSecretRoot: requireRecoveryRef(compose.fabricSecretRoot, "Fabric Secret root"),
      postgresVolumeRefs,
      providerVolumeRefs
    },
    cleanup: { performed: false, reason: "durable_launch_or_external_write" }
  };
}

export function localJ1RecoveryArtifactPath(receiptPath) {
  const normalized = String(receiptPath || "");
  if (!normalized.startsWith("/") || !normalized.endsWith(".json")) {
    throw new Error("qualification receipt path must be an absolute JSON path");
  }
  return normalized.replace(/\.json$/, ".recovery.json");
}

export async function writeLocalJ1RecoveryArtifact(path, artifact) {
  const target = String(path || "");
  if (!target.startsWith("/") || !target.endsWith(".json")) throw new Error("recovery artifact path must be an absolute JSON path");
  await mkdir(dirname(target), { recursive: true });
  const temporary = `${target}.${process.pid}.${randomBytes(4).toString("hex")}.tmp`;
  await writeFile(temporary, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, target);
}

export async function collectLocalJ1RecoveryAuthority({ http, auth, operationId, launchSubmitted }) {
  if (!launchSubmitted) return null;
  if (!http || !auth || !String(operationId || "")) throw new Error("owner-authoritative launch readback inputs are required");
  const launch = (await http.json(`/api/workspace-launches/${encodeURIComponent(operationId)}`, {}, auth)).payload;
  return summarizeLocalJ1RecoveryReadback(launch);
}
