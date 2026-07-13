import { spawn } from "node:child_process";
import { access, mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

import {
  assertProductionVerificationResourceOwnership,
  assertPublicHttpsUrl,
  readProductionManagementState,
  verificationOwnerFromSeed
} from "./production-verifier.ts";

export const SOAK_SLOT_COUNT = 5;
export const DEFAULT_SOAK_DURATION_MS = 15 * 60 * 1000;
const DEFAULT_EVIDENCE_INTERVAL_MS = 60 * 1000;
const DEFAULT_READY_TIMEOUT_MS = 20 * 60 * 1000;
const DEFAULT_READY_POLL_MS = 1000;
const MAX_CHILD_OUTPUT_BYTES = 64 * 1024;

function assertPositiveDuration(value, error, maximum = Number.POSITIVE_INFINITY) {
  if (!Number.isFinite(value) || value <= 0 || value > maximum) throw new Error(error);
}

function value(value, error = "production_soak_manifest_invalid") {
  if (typeof value !== "string" || !value.trim()) throw new Error(error);
  return value;
}

function resourceAccountId(resource) {
  return resource?.accountId || resource?.ownerAccountId || "";
}

function exactResource(rows, id, accountId) {
  const matches = (rows || []).filter((row) => row?.id === id && resourceAccountId(row) === accountId);
  if (matches.length !== 1) throw new Error("production_soak_active_evidence_invalid");
  return matches[0];
}

function cleanWorkspaceUrl(raw, workspaceId) {
  const parsed = assertPublicHttpsUrl(raw, "production_soak_manifest_invalid");
  try {
    if (decodeURIComponent(parsed.pathname) !== `/w/${workspaceId}/`) throw new Error("production_soak_manifest_invalid");
  } catch {
    throw new Error("production_soak_manifest_invalid");
  }
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString();
}

function safeWorkspaceUrl(manifest) {
  try {
    return manifest.workspaceUrl && manifest.ids?.workspaceId
      ? cleanWorkspaceUrl(manifest.workspaceUrl, manifest.ids.workspaceId)
      : "";
  } catch {
    return "";
  }
}

function assertDistinct(values, label) {
  if (new Set(values).size !== values.length) throw new Error(`production_soak_identity_duplicate:${label}`);
}

export function validateSoakManifests(manifests, { accountId, runIds }) {
  if (!Array.isArray(manifests) || manifests.length !== SOAK_SLOT_COUNT || runIds?.length !== SOAK_SLOT_COUNT) {
    throw new Error("production_soak_manifest_invalid");
  }
  const expectedRuns = new Set(runIds);
  const resources = [];
  const holds = [];
  const machineIds = [];
  const instanceIds = [];
  const nodeNames = [];
  const workspaceIds = [];
  const urls = [];
  const slots = [];
  const validated = manifests.map((manifest) => {
    const runId = value(manifest?.runId);
    const slot = value(manifest?.slot);
    if (!expectedRuns.has(runId) || manifest.accountId !== accountId) throw new Error("production_soak_manifest_invalid");
    const ids = manifest.ids || {};
    const computeId = value(ids.computeAllocationId);
    const storageId = value(ids.storageId);
    const attachmentId = value(ids.attachmentId);
    const workspaceId = value(ids.workspaceId);
    const computeHold = value(manifest.holdIds?.compute);
    const storageHold = value(manifest.holdIds?.storage);
    const identity = manifest.machineIdentities?.[computeId] || {};
    const machineId = value(identity.machineId);
    const instanceId = value(identity.instanceId);
    const nodeName = value(identity.nodeName);
    if (
      manifest.workspaceId !== workspaceId ||
      !manifest.resourceNames?.compute?.includes(runId) ||
      !manifest.resourceNames?.storage?.includes(runId) ||
      !manifest.resourceNames?.workspace?.includes(runId)
    ) throw new Error("production_soak_manifest_invalid");
    const workspaceUrl = cleanWorkspaceUrl(value(manifest.workspaceUrl), workspaceId);
    resources.push(computeId, storageId, attachmentId, workspaceId);
    holds.push(computeHold, storageHold);
    machineIds.push(machineId);
    instanceIds.push(instanceId);
    nodeNames.push(nodeName);
    workspaceIds.push(workspaceId);
    urls.push(workspaceUrl);
    slots.push(slot);
    return { ...manifest, workspaceUrl };
  });
  assertDistinct(validated.map((manifest) => manifest.runId), "runId");
  assertDistinct(slots, "slot");
  assertDistinct(resources, "resource");
  assertDistinct(holds, "hold");
  assertDistinct(machineIds, "machineId");
  assertDistinct(instanceIds, "instanceId");
  assertDistinct(nodeNames, "nodeName");
  assertDistinct(workspaceIds, "workspaceId");
  assertDistinct(urls, "workspaceUrl");
  return validated;
}

async function atomicWriteJson(path, payload) {
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${process.pid}.${Math.random().toString(36).slice(2)}.tmp`;
  await writeFile(temporary, `${JSON.stringify(payload, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, path);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function collectOutput(stream, state) {
  stream?.on?.("data", (chunk) => {
    if (state.value.length >= MAX_CHILD_OUTPUT_BYTES) return;
    state.value += String(chunk).slice(0, MAX_CHILD_OUTPUT_BYTES - state.value.length);
  });
}

function sanitizeDiagnostic(raw) {
  return String(raw || "")
    .replace(/https:\/\/[^\s"']+/g, (rawUrl) => {
      try {
        const url = new URL(rawUrl);
        url.search = "";
        url.hash = "";
        return url.toString();
      } catch {
        return "[redacted-url]";
      }
    })
    .replace(/(token|secret|password|credential)=?[^\s|,]*/gi, "$1=[redacted]");
}

function parsedFailure(text) {
  try {
    const payload = JSON.parse(text.trim());
    return {
      error: typeof payload.error === "string" ? sanitizeDiagnostic(payload.error) : "",
      cleanupErrors: Array.isArray(payload.cleanupErrors) ? payload.cleanupErrors.map(sanitizeDiagnostic) : []
    };
  } catch {
    return { error: "", cleanupErrors: [] };
  }
}

function verifierChild({ spawnImpl, origin, accountId, runId, slot, paths, env }) {
  const args = [
    "tools/production-verifier.ts",
    "--origin", origin,
    "--account", accountId,
    "--package", "basic",
    "--run-id", runId,
    "--slot", slot,
    "--manifest-path", paths.manifest,
    "--ready-file", paths.ready,
    "--release-file", paths.release,
    "--barrier-timeout-ms", String(50 * 60 * 1000)
  ];
  const child = spawnImpl(process.execPath, args, {
    env: { ...env, OPL_VERIFY_BROWSER_E2E: "0" },
    stdio: ["ignore", "pipe", "pipe"]
  });
  const stdout = { value: "" };
  const stderr = { value: "" };
  collectOutput(child.stdout, stdout);
  collectOutput(child.stderr, stderr);
  let settled = false;
  let resolveExit;
  const exit = new Promise((resolve) => { resolveExit = resolve; });
  const finish = (exitCode, signal = null, spawnError = "") => {
    if (settled) return;
    settled = true;
    const failure = parsedFailure(stderr.value);
    resolveExit({ slot, runId, exitCode, signal, spawnError: sanitizeDiagnostic(spawnError), ...failure });
  };
  child.once("error", (error) => finish(1, null, error.message));
  child.once("close", (code, signal) => finish(code ?? 1, signal));
  return { child, slot, runId, paths, exit, get exited() { return settled; } };
}

async function exists(path) {
  try {
    await access(path);
    return true;
  } catch (error) {
    if (error?.code === "ENOENT") return false;
    throw error;
  }
}

async function readyChildren(children) {
  const ready = [];
  for (const child of children) {
    if (await exists(child.paths.ready)) ready.push(child);
  }
  return ready;
}

async function waitForAllReady(children, { timeoutMs, pollMs, sleepImpl }) {
  let elapsed = 0;
  while (elapsed <= timeoutMs) {
    const ready = await readyChildren(children);
    if (ready.length === children.length) return ready;
    const failed = children.find((child) => child.exited);
    if (failed) throw new Error(`production_soak_child_failed_before_barrier:${failed.slot}`);
    if (elapsed === timeoutMs) break;
    const delay = Math.min(pollMs, timeoutMs - elapsed);
    await sleepImpl(delay);
    elapsed += delay;
  }
  throw new Error("production_soak_ready_timeout");
}

async function readManifests(children) {
  return Promise.all(children.map(async (child) => JSON.parse(await readFile(child.paths.manifest, "utf8"))));
}

async function release(children) {
  await Promise.all(children.map((child) => atomicWriteJson(child.paths.release, { runId: child.runId, slot: child.slot })));
}

async function drainVerifierCleanup(children, { sleepImpl, pollMs }) {
  const released = new Set();
  while (children.some((child) => !child.exited)) {
    const ready = await readyChildren(children);
    const pendingRelease = ready.filter((child) => !released.has(child.slot));
    await release(pendingRelease);
    pendingRelease.forEach((child) => released.add(child.slot));
    if (children.every((child) => child.exited)) break;
    await Promise.race([
      Promise.all(children.map((child) => child.exit)),
      sleepImpl(pollMs)
    ]);
  }
  return Promise.all(children.map((child) => child.exit));
}

function assertActiveEvidence(state, manifests) {
  for (const manifest of manifests) {
    assertProductionVerificationResourceOwnership(state, manifest);
    const ids = manifest.ids;
    const compute = exactResource(state.computeAllocations, ids.computeAllocationId, manifest.accountId);
    const storage = exactResource(state.storageVolumes, ids.storageId, manifest.accountId);
    const attachment = exactResource(state.storageAttachments, ids.attachmentId, manifest.accountId);
    const workspace = exactResource(state.workspaces, ids.workspaceId, manifest.accountId);
    if (
      compute.status !== "running" || compute.billingStatus !== "active" ||
      storage.status !== "available" || storage.billingStatus !== "active" ||
      attachment.status !== "attached" || workspace.state !== "running" ||
      cleanWorkspaceUrl(workspace.url, ids.workspaceId) !== manifest.workspaceUrl
    ) throw new Error("production_soak_active_evidence_invalid");
  }
  return { activeSlots: manifests.length };
}

function assertZeroResiduals(state, manifests) {
  const fields = [
    ["computeAllocations", ["computeAllocationId", "replacementComputeAllocationId"]],
    ["storageVolumes", ["storageId"]],
    ["storageAttachments", ["attachmentId", "replacementAttachmentId"]],
    ["workspaces", ["workspaceId", "replacementWorkspaceId"]]
  ];
  const residuals = [];
  for (const manifest of manifests) {
    for (const [field, keys] of fields) {
      for (const key of keys) {
        const id = manifest.ids?.[key];
        if (id && (state[field] || []).some((resource) => resource.id === id && resourceAccountId(resource) === manifest.accountId)) {
          residuals.push({ field, id, runId: manifest.runId });
        }
      }
    }
    for (const [resourceType, keys] of [
      ["compute", ["computeAllocationId", "replacementComputeAllocationId"]],
      ["storage", ["storageId"]]
    ]) {
      for (const key of keys) {
        const resourceId = manifest.ids?.[key];
        if (!resourceId) continue;
        const releases = (state.billingLedger || []).filter((entry) =>
          entry.accountId === manifest.accountId && entry.resourceId === resourceId && entry.type === `${resourceType}_hold_released`
        );
        if (releases.length !== 1) residuals.push({ field: "billingLedger", id: resourceId, runId: manifest.runId });
      }
    }
  }
  if (residuals.length) {
    const error = new Error("production_soak_residuals_found");
    error.residuals = residuals;
    throw error;
  }
  return { residuals: 0 };
}

export async function productionSoakEvidenceCheck({ phase, manifests, origin, operatorToken, fetchImpl = globalThis.fetch }) {
  const state = await readProductionManagementState({ fetchImpl, origin, operatorToken });
  return phase === "final" ? assertZeroResiduals(state, manifests) : assertActiveEvidence(state, manifests);
}

function safeError(error) {
  return {
    error: sanitizeDiagnostic(error?.message || "production_soak_failed"),
    ...(Array.isArray(error?.residuals) ? { residuals: error.residuals } : {})
  };
}

export async function runProductionSoak({
  origin,
  accountId,
  baseRunId,
  artifactDir,
  soakDurationMs = DEFAULT_SOAK_DURATION_MS,
  evidenceIntervalMs = DEFAULT_EVIDENCE_INTERVAL_MS,
  readyTimeoutMs = DEFAULT_READY_TIMEOUT_MS,
  readyPollMs = DEFAULT_READY_POLL_MS,
  spawnImpl = spawn,
  sleepImpl = sleep,
  evidenceCheck = productionSoakEvidenceCheck,
  env = process.env,
  fetchImpl = globalThis.fetch
} = {}) {
  assertPositiveDuration(soakDurationMs, "production_soak_duration_invalid", DEFAULT_SOAK_DURATION_MS);
  assertPositiveDuration(evidenceIntervalMs, "production_soak_evidence_interval_invalid", DEFAULT_SOAK_DURATION_MS);
  assertPositiveDuration(readyTimeoutMs, "production_soak_ready_timeout_invalid", 60 * 60 * 1000);
  assertPositiveDuration(readyPollMs, "production_soak_ready_poll_invalid", readyTimeoutMs);
  value(origin, "origin_required");
  value(accountId, "account_id_required");
  value(artifactDir, "artifact_dir_required");
  if (!/^[A-Za-z0-9._-]{1,70}$/.test(String(baseRunId || ""))) throw new Error("production_soak_run_id_invalid");
  await mkdir(artifactDir, { recursive: true });
  const children = Array.from({ length: SOAK_SLOT_COUNT }, (_, index) => {
    const slot = String(index + 1).padStart(2, "0");
    const runId = `${baseRunId}-${slot}`;
    return verifierChild({
      spawnImpl,
      origin,
      accountId,
      runId,
      slot,
      env,
      paths: {
        manifest: join(artifactDir, `manifest-${slot}.json`),
        ready: join(artifactDir, `ready-${slot}.json`),
        release: join(artifactDir, `release-${slot}.json`)
      }
    });
  });
  let manifests = [];
  let failure = null;
  let evidenceIndex = 0;
  const recordEvidence = async (phase) => {
    const evidence = await evidenceCheck({ phase, manifests, origin, accountId, operatorToken: env.OPL_VERIFY_OPERATOR_TOKEN || "", fetchImpl });
    await atomicWriteJson(join(artifactDir, `evidence-${String(evidenceIndex++).padStart(3, "0")}.json`), { phase, ...evidence });
  };
  try {
    await waitForAllReady(children, { timeoutMs: readyTimeoutMs, pollMs: readyPollMs, sleepImpl });
    manifests = validateSoakManifests(await readManifests(children), { accountId, runIds: children.map((child) => child.runId) });
    await recordEvidence("barrier");
    let elapsed = 0;
    while (elapsed < soakDurationMs) {
      const delay = Math.min(evidenceIntervalMs, soakDurationMs - elapsed);
      await sleepImpl(delay);
      elapsed += delay;
      if (children.some((child) => child.exited)) throw new Error("production_soak_child_exited_during_soak");
      await recordEvidence("soak");
    }
  } catch (error) {
    failure = error;
  }
  const exits = await drainVerifierCleanup(children, { sleepImpl, pollMs: readyPollMs });
  try {
    const available = [];
    for (const child of children) {
      if (await exists(child.paths.manifest)) available.push(JSON.parse(await readFile(child.paths.manifest, "utf8")));
    }
    if (available.length) {
      manifests = available;
      await recordEvidence("final");
    }
  } catch (error) {
    failure ||= error;
  }
  if (exits.some((child) => child.exitCode !== 0)) failure ||= new Error("production_soak_child_failed");
  const result = {
    ok: !failure,
    accountId,
    baseRunId,
    slots: children.map(({ slot, runId, paths }) => ({ slot, runId, ...paths })),
    children: exits,
    manifests: manifests.map((manifest) => ({
      runId: manifest.runId,
      slot: manifest.slot,
      workspaceId: manifest.ids?.workspaceId,
      workspaceUrl: safeWorkspaceUrl(manifest)
    })),
    ...(failure ? safeError(failure) : {})
  };
  await atomicWriteJson(join(artifactDir, "result.json"), result);
  if (failure) {
    const error = new Error(result.error);
    error.result = result;
    throw error;
  }
  return result;
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    if (!argv[index].startsWith("--")) continue;
    const key = argv[index].slice(2);
    args[key] = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
  }
  return args;
}

async function manifestsFromArtifactDir(artifactDir) {
  const result = JSON.parse(await readFile(join(artifactDir, "result.json"), "utf8"));
  const manifests = [];
  for (let index = 1; index <= SOAK_SLOT_COUNT; index += 1) {
    manifests.push(JSON.parse(await readFile(join(artifactDir, `manifest-${String(index).padStart(2, "0")}.json`), "utf8")));
  }
  return validateSoakManifests(manifests, {
    accountId: result.accountId,
    runIds: result.slots?.map((slot) => slot.runId)
  });
}

export async function runProductionSoakCli({ argv = process.argv.slice(2), env = process.env, stdout = process.stdout, stderr = process.stderr } = {}) {
  try {
    const args = cliArgs(argv);
    const owner = verificationOwnerFromSeed(env.OPL_VERIFY_AUTH_USERS_JSON, args.account || env.OPL_VERIFY_ACCOUNT_ID || "");
    const accountId = owner.accountId || args.account || env.OPL_VERIFY_ACCOUNT_ID;
    const origin = args.origin || env.OPL_CONSOLE_ORIGIN;
    const artifactDir = args["artifact-dir"] || env.OPL_SOAK_ARTIFACT_DIR || "artifacts/production-soak";
    if (args["verify-residuals"] === "true") {
      const manifests = await manifestsFromArtifactDir(artifactDir);
      const evidence = await productionSoakEvidenceCheck({ phase: "final", manifests, origin, operatorToken: env.OPL_VERIFY_OPERATOR_TOKEN || "" });
      stdout.write(`${JSON.stringify({ ok: true, ...evidence })}\n`);
      return 0;
    }
    const result = await runProductionSoak({
      origin,
      accountId,
      baseRunId: args["run-id"] || env.OPL_SOAK_RUN_ID,
      artifactDir,
      soakDurationMs: Number(args["soak-duration-ms"] || env.OPL_SOAK_DURATION_MS || DEFAULT_SOAK_DURATION_MS),
      evidenceIntervalMs: Number(args["evidence-interval-ms"] || env.OPL_SOAK_EVIDENCE_INTERVAL_MS || DEFAULT_EVIDENCE_INTERVAL_MS),
      env
    });
    stdout.write(`${JSON.stringify({ ok: result.ok, baseRunId: result.baseRunId, slots: result.slots.map(({ slot, runId }) => ({ slot, runId })) })}\n`);
    return 0;
  } catch (error) {
    stderr.write(`${JSON.stringify({ ok: false, error: error.message, cleanupErrors: error.result?.children?.flatMap((child) => child.cleanupErrors || []) || [] })}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionSoakCli().then((code) => { process.exitCode = code; });
}
