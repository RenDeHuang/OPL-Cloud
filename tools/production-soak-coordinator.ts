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

const RESOURCE_ID_KEYS = [
  "computeAllocationId", "replacementComputeAllocationId", "storageId",
  "attachmentId", "replacementAttachmentId", "workspaceId", "replacementWorkspaceId"
];

function optionalValue(object, key) {
  return Object.hasOwn(object || {}, key) ? value(object[key]) : "";
}

function normalizePartialSoakManifests(manifests, { accountId, slots: expectedSlots }) {
  if (!Array.isArray(manifests) || manifests.length > SOAK_SLOT_COUNT || expectedSlots?.length !== SOAK_SLOT_COUNT) {
    throw new Error("production_soak_manifest_invalid");
  }
  const expectedByRun = new Map(expectedSlots.map((slot) => [value(slot?.runId), value(slot?.slot)]));
  if (expectedByRun.size !== SOAK_SLOT_COUNT) throw new Error("production_soak_manifest_invalid");
  const resources = [];
  const holds = [];
  const machineIds = [];
  const instanceIds = [];
  const nodeNames = [];
  const validated = manifests.map((manifest) => {
    const runId = value(manifest?.runId);
    const slot = value(manifest?.slot);
    if (expectedByRun.get(runId) !== slot || manifest.accountId !== accountId) throw new Error("production_soak_manifest_invalid");
    const names = manifest.resourceNames || {};
    for (const key of ["compute", "storage", "workspace"]) {
      if (!value(names[key]).includes(runId)) throw new Error("production_soak_manifest_invalid");
    }
    for (const key of ["replacementCompute", "replacementWorkspace"]) {
      if (Object.hasOwn(names, key) && !value(names[key]).includes(runId)) throw new Error("production_soak_manifest_invalid");
    }
    const ids = manifest.ids || {};
    const knownIds = new Set();
    for (const key of RESOURCE_ID_KEYS) {
      const id = optionalValue(ids, key);
      if (id) {
        resources.push(id);
        knownIds.add(id);
      }
    }
    for (const key of ["compute", "replacementCompute", "storage"]) {
      const hold = optionalValue(manifest.holdIds, key);
      if (hold) holds.push(hold);
    }
    for (const [resourceId, identity] of Object.entries(manifest.machineIdentities || {})) {
      if (!knownIds.has(resourceId) || ![ids.computeAllocationId, ids.replacementComputeAllocationId].includes(resourceId) || !identity || typeof identity !== "object") {
        throw new Error("production_soak_manifest_invalid");
      }
      const machineId = optionalValue(identity, "machineId");
      const instanceId = optionalValue(identity, "instanceId");
      const nodeName = optionalValue(identity, "nodeName");
      if (machineId) machineIds.push(machineId);
      if (instanceId) instanceIds.push(instanceId);
      if (nodeName) nodeNames.push(nodeName);
    }
    if (Object.hasOwn(manifest, "workspaceId") && manifest.workspaceId !== ids.workspaceId) throw new Error("production_soak_manifest_invalid");
    if (Object.hasOwn(manifest, "workspaceUrl")) {
      if (!ids.workspaceId) throw new Error("production_soak_manifest_invalid");
      return { ...manifest, workspaceUrl: cleanWorkspaceUrl(value(manifest.workspaceUrl), ids.workspaceId) };
    }
    return manifest;
  });
  assertDistinct(validated.map((manifest) => manifest.runId), "runId");
  assertDistinct(validated.map((manifest) => manifest.slot), "slot");
  assertDistinct(resources, "resource");
  assertDistinct(holds, "hold");
  assertDistinct(machineIds, "machineId");
  assertDistinct(instanceIds, "instanceId");
  assertDistinct(nodeNames, "nodeName");
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

function terminalEvidenceError(details) {
  const error = new Error("production_soak_terminal_evidence_invalid");
  error.terminalEvidence = details;
  return error;
}

function terminalRow(rows, id, accountId, field, runId) {
  const matches = (rows || []).filter((row) => row?.id === id);
  if (matches.length !== 1 || resourceAccountId(matches[0]) !== accountId) throw terminalEvidenceError([{ field, id, runId }]);
  return matches[0];
}

function hasExactRunLabel(row, runId) {
  return [row?.runId, row?.name, row?.resourceName, row?.idempotencyKey, row?.sourceEventId]
    .some((candidate) => String(candidate || "").split(/[^A-Za-z0-9._-]+/).includes(runId));
}

function labelledResourceId(field, row) {
  if (["computeAllocations", "storageVolumes", "storageAttachments", "workspaces"].includes(field)) return row?.id || "";
  return row?.resourceId || row?.computeAllocationId || row?.storageId || row?.attachmentId || row?.workspaceId || "";
}

function assertControlPlaneTerminalEvidence(state, manifests) {
  const invalid = [];
  let terminalResources = 0;
  for (const manifest of manifests) {
    const ids = manifest.ids || {};
    const knownIds = new Set(RESOURCE_ID_KEYS.map((key) => ids[key]).filter(Boolean));
    for (const field of ["computeAllocations", "storageVolumes", "storageAttachments", "workspaces", "billingLedger", "runtimeOperations"]) {
      for (const row of state[field] || []) {
        if (
          resourceAccountId(row) === manifest.accountId && hasExactRunLabel(row, manifest.runId) &&
          !knownIds.has(labelledResourceId(field, row))
        ) invalid.push({ field, id: labelledResourceId(field, row), runId: manifest.runId });
      }
    }
    if (manifest.missing) continue;
    for (const [key, holdKey] of [
      ["computeAllocationId", "compute"],
      ["replacementComputeAllocationId", "replacementCompute"]
    ]) {
      const resourceId = ids[key];
      if (!resourceId) continue;
      const row = terminalRow(state.computeAllocations, resourceId, manifest.accountId, "computeAllocations", manifest.runId);
      const expectedName = key === "replacementComputeAllocationId"
        ? (manifest.resourceNames?.replacementCompute || manifest.resourceNames?.compute)
        : manifest.resourceNames?.compute;
      const identity = manifest.machineIdentities?.[resourceId] || {};
      const expectedHoldId = manifest.holdIds?.[holdKey];
      if (
        row.name !== expectedName || row.status !== "destroyed" || row.billingStatus !== "stopped" ||
        !row.holdId || (expectedHoldId && row.holdId !== expectedHoldId) || !row.holdReleaseId ||
        (identity.machineId && row.machineName !== identity.machineId) ||
        (identity.instanceId && (row.instanceId || row.cvmInstanceId) !== identity.instanceId) ||
        (identity.nodeName && row.nodeName !== identity.nodeName)
      ) {
        invalid.push({ field: "computeAllocations", id: resourceId, runId: manifest.runId });
      } else {
        const releases = (state.billingLedger || []).filter((entry) =>
          entry.accountId === manifest.accountId && entry.resourceId === resourceId && entry.type === "compute_hold_released"
        );
        if (releases.length !== 1 || releases[0].id !== row.holdReleaseId) {
          invalid.push({ field: "billingLedger", id: resourceId, runId: manifest.runId });
        }
      }
      terminalResources += 1;
    }
    if (ids.storageId) {
      const storage = terminalRow(state.storageVolumes, ids.storageId, manifest.accountId, "storageVolumes", manifest.runId);
      const expectedHoldId = manifest.holdIds?.storage;
      if (
        storage.name !== manifest.resourceNames?.storage || storage.status !== "destroyed" || storage.billingStatus !== "stopped" ||
        !storage.holdId || (expectedHoldId && storage.holdId !== expectedHoldId) || !storage.holdReleaseId
      ) {
        invalid.push({ field: "storageVolumes", id: ids.storageId, runId: manifest.runId });
      } else {
        const releases = (state.billingLedger || []).filter((entry) =>
          entry.accountId === manifest.accountId && entry.resourceId === ids.storageId &&
          entry.type === "storage_hold_released"
        );
        if (releases.length !== 1 || releases[0].id !== storage.holdReleaseId) {
          invalid.push({ field: "billingLedger", id: ids.storageId, runId: manifest.runId });
        }
      }
      terminalResources += 1;
    }

    for (const [attachmentKey, computeKey] of [
      ["attachmentId", "computeAllocationId"],
      ["replacementAttachmentId", "replacementComputeAllocationId"]
    ]) {
      const attachmentId = ids[attachmentKey];
      if (!attachmentId) continue;
      const attachment = terminalRow(state.storageAttachments, attachmentId, manifest.accountId, "storageAttachments", manifest.runId);
      if (
        !["detached", "deleted"].includes(attachment.status) ||
        (ids[computeKey] && attachment.computeAllocationId !== ids[computeKey]) ||
        (ids.storageId && attachment.storageId !== ids.storageId)
      ) invalid.push({ field: "storageAttachments", id: attachmentId, runId: manifest.runId });
      terminalResources += 1;
    }

    for (const workspaceKey of ["workspaceId", "replacementWorkspaceId"]) {
      const workspaceId = ids[workspaceKey];
      if (!workspaceId) continue;
      const workspace = terminalRow(state.workspaces, workspaceId, manifest.accountId, "workspaces", manifest.runId);
      const expectedName = workspaceKey === "replacementWorkspaceId"
        ? (manifest.resourceNames?.replacementWorkspace || manifest.resourceNames?.workspace)
        : manifest.resourceNames?.workspace;
      if (
        workspace.name !== expectedName || workspace.state !== "data_deleted" || workspace.status !== "unrecoverable" ||
        (ids.storageId && workspace.storageId !== ids.storageId) || workspace.computeAllocationId || workspace.currentComputeAllocationId ||
        workspace.attachmentId || workspace.currentAttachmentId || workspace.access?.tokenStatus !== "disabled" ||
        workspace.openable !== false || workspace.accessState !== "disabled"
      ) invalid.push({ field: "workspaces", id: workspaceId, runId: manifest.runId });
      terminalResources += 1;
    }
  }
  if (invalid.length) throw terminalEvidenceError(invalid);
  return {
    controlPlaneTerminalResources: terminalResources,
    missingSlots: manifests.filter((manifest) => manifest.missing).length
  };
}

export async function productionSoakEvidenceCheck({ phase, manifests, origin, operatorToken, fetchImpl = globalThis.fetch, signal = undefined }) {
  const state = await readProductionManagementState({ fetchImpl, origin, operatorToken, signal });
  return phase === "final" ? assertControlPlaneTerminalEvidence(state, manifests) : assertActiveEvidence(state, manifests);
}

function safeError(error) {
  return {
    error: sanitizeDiagnostic(error?.message || "production_soak_failed"),
    ...(Array.isArray(error?.terminalEvidence) ? { terminalEvidence: error.terminalEvidence } : {})
  };
}

async function evidenceBeforeDeadline(deadline, evidenceCheck, args, { nowImpl, setTimeoutImpl, clearTimeoutImpl }) {
  const remaining = deadline - nowImpl();
  if (remaining <= 0) throw new Error("production_soak_evidence_timeout");
  const controller = new AbortController();
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeoutImpl(() => {
      controller.abort();
      reject(new Error("production_soak_evidence_timeout"));
    }, remaining);
  });
  try {
    return await Promise.race([evidenceCheck({ ...args, signal: controller.signal, deadline }), timeout]);
  } finally {
    clearTimeoutImpl(timer);
  }
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
  nowImpl = Date.now,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
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
  const recordEvidence = async (phase, deadline) => {
    const evidence = await evidenceBeforeDeadline(deadline, evidenceCheck, {
      phase, manifests, origin, accountId, operatorToken: env.OPL_VERIFY_OPERATOR_TOKEN || "", fetchImpl
    }, { nowImpl, setTimeoutImpl, clearTimeoutImpl });
    await atomicWriteJson(join(artifactDir, `evidence-${String(evidenceIndex++).padStart(3, "0")}.json`), { phase, ...evidence });
  };
  try {
    await waitForAllReady(children, { timeoutMs: readyTimeoutMs, pollMs: readyPollMs, sleepImpl });
    manifests = validateSoakManifests(await readManifests(children), { accountId, runIds: children.map((child) => child.runId) });
    const soakDeadline = nowImpl() + soakDurationMs;
    await recordEvidence("barrier", soakDeadline);
    while (nowImpl() < soakDeadline) {
      const delay = Math.min(evidenceIntervalMs, soakDeadline - nowImpl());
      await sleepImpl(delay);
      if (children.some((child) => child.exited)) throw new Error("production_soak_child_exited_during_soak");
      if (nowImpl() < soakDeadline) await recordEvidence("soak", soakDeadline);
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
      manifests = normalizePartialSoakManifests(available, {
        accountId,
        slots: children.map((child) => ({ slot: child.slot, runId: child.runId }))
      });
      await recordEvidence("final", nowImpl() + evidenceIntervalMs);
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

export async function manifestsFromArtifactDir(artifactDir) {
  const result = JSON.parse(await readFile(join(artifactDir, "result.json"), "utf8"));
  const manifests = [];
  for (let index = 1; index <= SOAK_SLOT_COUNT; index += 1) {
    const slot = String(index).padStart(2, "0");
    const path = join(artifactDir, `manifest-${slot}.json`);
    if (await exists(path)) manifests.push(JSON.parse(await readFile(path, "utf8")));
  }
  const validated = normalizePartialSoakManifests(manifests, {
    accountId: result.accountId,
    slots: result.slots
  });
  const present = new Set(validated.map((manifest) => manifest.runId));
  return [...validated, ...result.slots
    .filter((slot) => !present.has(slot.runId))
    .map((slot) => ({ runId: slot.runId, slot: slot.slot, accountId: result.accountId, missing: true }))];
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
      const deadline = Date.now() + DEFAULT_EVIDENCE_INTERVAL_MS;
      const evidence = await productionSoakEvidenceCheck({
        phase: "final",
        manifests,
        origin,
        operatorToken: env.OPL_VERIFY_OPERATOR_TOKEN || "",
        signal: AbortSignal.timeout(DEFAULT_EVIDENCE_INTERVAL_MS),
        deadline
      });
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
