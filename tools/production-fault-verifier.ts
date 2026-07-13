import { spawn } from "node:child_process";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { dirname } from "node:path";

import {
  assertProductionVerificationResourceOwnership,
  productionVerificationMutationKey,
  requestOwnerSession,
  verificationOwnerFromSeed
} from "./production-verifier.ts";

export const FAULT_SCENARIOS = [
  "lost-response-replay",
  "workspace-pod-recovery",
  "storage-detach-reattach",
  "machine-external-delete",
  "browser-failure-cleanup"
];

function faultError(name, details = undefined) {
  const error = new Error(name);
  if (details !== undefined) error.details = details;
  return error;
}

function exact(rows, predicate, errorName) {
  const matches = (rows || []).filter(predicate);
  if (matches.length !== 1) throw faultError(errorName);
  return matches[0];
}

function exactKubernetes(items, kind, labels) {
  return exact(items, (item) => (
    item?.kind === kind && Object.entries(labels).every(([key, value]) => item?.metadata?.labels?.[key] === value)
  ), "production_fault_provider_resources_invalid");
}

export async function enrichProductionFaultManifest({ manifest, namespace, kubectlJson, persistManifest }) {
  if (!manifest?.ids?.workspaceId || !manifest.ids.computeAllocationId || !manifest.ids.storageId || !namespace) {
    throw faultError("production_fault_provider_resources_invalid");
  }
  const workspace = await kubectlJson([
    "get", "deployment,service,secret,pod", "-n", namespace,
    "-l", `oplcloud.cn/workspace-id=${manifest.ids.workspaceId}`, "-o", "json"
  ]);
  const workspaceLabels = { "oplcloud.cn/workspace-id": manifest.ids.workspaceId };
  const deployment = exactKubernetes(workspace?.items, "Deployment", {
    ...workspaceLabels,
    "oplcloud.cn/resource-id": manifest.ids.computeAllocationId
  });
  const service = exactKubernetes(workspace?.items, "Service", workspaceLabels);
  const secret = exactKubernetes(workspace?.items, "Secret", workspaceLabels);
  const pod = exactKubernetes(workspace?.items, "Pod", {
    ...workspaceLabels,
    "oplcloud.cn/resource-id": manifest.ids.computeAllocationId
  });
  const volumes = await kubectlJson([
    "get", "pvc", "-n", namespace,
    "-l", `oplcloud.cn/storage-id=${manifest.ids.storageId}`, "-o", "json"
  ]);
  const pvc = exactKubernetes(volumes?.items, "PersistentVolumeClaim", {
    "oplcloud.cn/storage-id": manifest.ids.storageId
  });
  const pvcName = pvc?.metadata?.name;
  const pvName = pvc?.spec?.volumeName;
  if (!pvcName || !pvName || pvc?.metadata?.namespace !== namespace) throw faultError("production_fault_provider_resources_invalid");
  const pv = await kubectlJson(["get", `pv/${pvName}`, "-o", "json"]);
  if (
    pv?.metadata?.name !== pvName || pv?.spec?.claimRef?.name !== pvcName ||
    pv?.spec?.claimRef?.namespace !== namespace || !pv?.spec?.csi?.volumeHandle ||
    !pv?.spec?.persistentVolumeReclaimPolicy
  ) throw faultError("production_fault_provider_resources_invalid");
  manifest.providerResources = {
    namespace,
    deploymentName: deployment.metadata.name,
    serviceName: service.metadata.name,
    secretName: secret.metadata.name,
    podName: pod.metadata.name,
    pvcName,
    pvName,
    cbsVolumeHandle: pv.spec.csi.volumeHandle,
    pvReclaimPolicy: pv.spec.persistentVolumeReclaimPolicy
  };
  await persistManifest(manifest);
  return manifest.providerResources;
}

function computeFor(state, manifest) {
  return exact(
    state.computeAllocations,
    (row) => row?.id === manifest.ids?.computeAllocationId && row?.accountId === manifest.accountId,
    "production_fault_ownership_mismatch"
  );
}

function storageFor(state, manifest) {
  return exact(
    state.storageVolumes,
    (row) => row?.id === manifest.ids?.storageId && row?.accountId === manifest.accountId,
    "production_fault_ownership_mismatch"
  );
}

function assertProductionFaultMachineOwnership(state, manifest) {
  const resourceId = manifest.ids.computeAllocationId;
  const identity = manifest.machineIdentities?.[resourceId] || {};
  const compute = computeFor(state, manifest);
  const ownership = exact(
    state.fabricOwnerships,
    (row) => row?.resourceId === resourceId,
    "production_fault_ownership_mismatch"
  );
  if (
    compute.provider !== "tencent-tke" || compute.providerData?.machineType !== "NativeCVM" ||
    !identity.instanceId?.startsWith("ins-") ||
    !identity.privateIp || compute.privateIp !== identity.privateIp ||
    ownership.accountId !== manifest.accountId || ownership.status !== "active" ||
    ownership.machineType !== "NativeCVM" ||
    ownership.machineId !== identity.machineId || ownership.instanceId !== identity.instanceId ||
    ownership.nodeName !== identity.nodeName
  ) throw faultError("production_fault_ownership_mismatch");
  return { compute, ownership };
}

function assertProductionFaultOwnership(state, manifest) {
  try {
    assertProductionVerificationResourceOwnership(state, manifest);
  } catch {
    throw faultError("production_fault_ownership_mismatch");
  }
  return assertProductionFaultMachineOwnership(state, manifest);
}

export function assertProductionFaultDetachedOwnership(state, manifest) {
  assertProductionFaultMachineOwnership(state, manifest);
  const storage = storageFor(state, manifest);
  const oldAttachment = exact(
    state.storageAttachments,
    (row) => row?.id === manifest.ids.attachmentId && row?.accountId === manifest.accountId,
    "production_fault_ownership_mismatch"
  );
  const workspace = exact(
    state.workspaces,
    (row) => row?.id === manifest.ids.workspaceId && row?.accountId === manifest.accountId,
    "production_fault_ownership_mismatch"
  );
  if (
    storage.name !== manifest.resourceNames.storage || storage.holdId !== manifest.holdIds.storage ||
    oldAttachment.status !== "detached" || oldAttachment.computeAllocationId !== manifest.ids.computeAllocationId ||
    oldAttachment.storageId !== manifest.ids.storageId ||
    !["suspended", "stopped"].includes(workspace.state || workspace.status) ||
    workspace.storageId !== manifest.ids.storageId || workspace.computeAllocationId !== manifest.ids.computeAllocationId ||
    workspace.attachmentId || workspace.currentAttachmentId
  ) throw faultError("production_fault_ownership_mismatch");
}

export function assertProductionFaultReattachOwnership(state, manifest, newAttachmentId) {
  assertProductionFaultDetachedOwnership(state, manifest);
  const newAttachment = exact(
    state.storageAttachments,
    (row) => row?.id === newAttachmentId && row?.accountId === manifest.accountId,
    "production_fault_ownership_mismatch"
  );
  if (
    newAttachment.status !== "attached" || newAttachment.computeAllocationId !== manifest.ids.computeAllocationId ||
    newAttachment.storageId !== manifest.ids.storageId
  ) throw faultError("production_fault_ownership_mismatch");
}

function assertTerminalManagementState(state, manifest, maximumBalanceCents) {
  const compute = computeFor(state, manifest);
  const storage = storageFor(state, manifest);
  const attachment = exact(
    state.storageAttachments,
    (row) => row?.id === manifest.ids.attachmentId && row?.accountId === manifest.accountId,
    "production_fault_terminal_state_invalid"
  );
  const detachedAttachment = manifest.ids.detachedAttachmentId ? exact(
    state.storageAttachments,
    (row) => row?.id === manifest.ids.detachedAttachmentId && row?.accountId === manifest.accountId,
    "production_fault_terminal_state_invalid"
  ) : null;
  const workspace = exact(
    state.workspaces,
    (row) => row?.id === manifest.ids.workspaceId && row?.accountId === manifest.accountId,
    "production_fault_terminal_state_invalid"
  );
  const releases = (state.billingLedger || []).filter((row) => (
    row?.accountId === manifest.accountId && row?.resourceId === compute.id &&
    row?.type === "compute_hold_released" &&
    row?.id === compute.holdReleaseId
  ));
  const storageReleases = (state.billingLedger || []).filter((row) => (
    row?.accountId === manifest.accountId && row?.resourceId === storage.id &&
    row?.type === "storage_hold_released" &&
    row?.id === storage.holdReleaseId
  ));
  if (
    !["destroyed", "external_deleted"].includes(compute.status) || compute.billingStatus !== "stopped" ||
    compute.holdId !== manifest.holdIds.compute || !compute.holdReleaseId || releases.length !== 1 ||
    !["destroyed", "external_deleted"].includes(storage.status) || storage.billingStatus !== "stopped" ||
    storage.holdId !== manifest.holdIds.storage || !storage.holdReleaseId || storageReleases.length !== 1 ||
    !["detached", "deleted"].includes(attachment.status) ||
    (detachedAttachment && !["detached", "deleted"].includes(detachedAttachment.status)) ||
    workspace.state !== "data_deleted" || workspace.status !== "unrecoverable" ||
    workspace.computeAllocationId || workspace.storageId !== manifest.ids.storageId || workspace.attachmentId ||
    workspace.openable !== false || workspace.accessState !== "disabled" ||
    !Number.isFinite(Number(state.wallet?.balanceCents)) || Number(state.wallet.balanceCents) > maximumBalanceCents
  ) throw faultError("production_fault_terminal_state_invalid");
}

export function assertProductionFaultProviderTruth(truth, manifest) {
  const identity = manifest.machineIdentities?.[manifest.ids?.computeAllocationId] || {};
  const resources = manifest.providerResources || {};
  const kubernetes = truth?.kubernetes || {};
  const terminal = new Set(["NOT_FOUND", "TERMINATED"]);
  if (
    truth?.accountId !== manifest.accountId || truth?.resourceId !== manifest.ids?.computeAllocationId ||
    truth?.ownership?.accountId !== manifest.accountId || truth?.ownership?.resourceId !== manifest.ids.computeAllocationId ||
    truth?.ownership?.status !== "released" || !truth?.ownership?.releasedAt ||
    truth?.ownership?.machineId !== identity.machineId ||
    truth?.ownership?.instanceId !== identity.instanceId ||
    truth?.ownership?.nodeName !== identity.nodeName ||
    truth?.ledger?.compute?.id !== manifest.holdIds.compute || truth?.ledger?.compute?.accountId !== manifest.accountId ||
    truth?.ledger?.compute?.resourceId !== manifest.ids.computeAllocationId || truth?.ledger?.compute?.status !== "released" || truth?.ledger?.compute?.remainingCents !== 0 ||
    truth?.ledger?.storage?.id !== manifest.holdIds.storage || truth?.ledger?.storage?.accountId !== manifest.accountId ||
    truth?.ledger?.storage?.resourceId !== manifest.ids.storageId || truth?.ledger?.storage?.status !== "released" || truth?.ledger?.storage?.remainingCents !== 0 ||
    truth?.tencent?.instanceId !== identity.instanceId || truth?.tencent?.machinePresent !== false ||
    !terminal.has(truth?.tencent?.cvmStatus) || !terminal.has(truth?.tencent?.tkeStatus) ||
    truth?.tencent?.storageVolumeId !== resources.cbsVolumeHandle || truth?.tencent?.storagePresent !== false ||
    truth?.tencent?.cbsStatus !== "NOT_FOUND" ||
    kubernetes.nodeName !== identity.nodeName || kubernetes.nodePresent !== false ||
    kubernetes.namespace !== resources.namespace ||
    kubernetes.deploymentName !== resources.deploymentName || kubernetes.deploymentPresent !== false ||
    kubernetes.serviceName !== resources.serviceName || kubernetes.servicePresent !== false ||
    kubernetes.pvcName !== resources.pvcName || kubernetes.pvcPresent !== false ||
    kubernetes.pvName !== resources.pvName || kubernetes.pvPresent !== false ||
    kubernetes.secretName !== resources.secretName || kubernetes.secretPresent !== false ||
    kubernetes.cbsVolumeHandle !== resources.cbsVolumeHandle ||
    kubernetes.pvReclaimPolicy !== "Delete" || resources.pvReclaimPolicy !== "Delete" ||
    !Array.isArray(kubernetes.podNames) ||
    JSON.stringify(kubernetes.podNames) !== JSON.stringify(resources.podNames || [resources.podName]) ||
    !Array.isArray(kubernetes.podsPresent) || kubernetes.podsPresent.length !== 0
    || !Number.isInteger(truth?.workspaceUrlStatus) || (truth.workspaceUrlStatus >= 200 && truth.workspaceUrlStatus < 300)
  ) throw faultError("production_fault_provider_truth_invalid");
}

function replayEvidence(state, manifest) {
  const resourceId = manifest.ids.computeAllocationId;
  const resources = (state.computeAllocations || []).filter((row) => row?.id === resourceId && row?.accountId === manifest.accountId);
  const holds = (state.billingLedger || []).filter((row) => (
    row?.accountId === manifest.accountId && row?.resourceId === resourceId && row?.type === "compute_hold"
  ));
  const debits = (state.billingLedger || []).filter((row) => (
    row?.accountId === manifest.accountId && row?.resourceId === resourceId && row?.type === "compute_debit"
  ));
  if (resources.length !== 1 || holds.length !== 1 || debits.length !== 1 || holds[0].id !== manifest.holdIds.compute) {
    throw faultError("production_fault_replay_duplicate");
  }
  return { resourceCount: 1, holdCount: 1, firstHourDebitCount: 1 };
}

async function proveThenMutate({ readEvidence, manifest, mutate }) {
  const state = await readEvidence();
  assertProductionFaultOwnership(state, manifest);
  return { state, value: await mutate(state.evidenceVersion) };
}

export async function runProductionFaultDrill({
  scenario,
  manifest,
  readEvidence,
  replayCreate,
  readWorkspaceProof,
  deleteWorkspacePod,
  waitWorkspaceRecovery,
  detachStorage,
  reattachStorage,
  persistManifest,
  deleteMachine,
  syncCompute,
  forceBrowserFailure,
  releaseVerifier,
  awaitVerifierCleanup,
  readProviderTruth
}) {
  if (!FAULT_SCENARIOS.includes(scenario)) throw faultError("production_fault_scenario_invalid");
  let initialBalanceCents;
  let evidence;
  if (scenario === "lost-response-replay") {
    const { state, value } = await proveThenMutate({
      readEvidence,
      manifest,
      mutate: (proofVersion) => replayCreate({
        idempotencyKey: productionVerificationMutationKey(manifest.runId, manifest.slot, "create-compute"),
        computeAllocationId: manifest.ids.computeAllocationId,
        proofVersion
      })
    });
    initialBalanceCents = Number(state.wallet?.balanceCents);
    if (value?.id !== manifest.ids.computeAllocationId || value?.holdId !== manifest.holdIds.compute) {
      throw faultError("production_fault_replay_identity_mismatch");
    }
    evidence = replayEvidence(await readEvidence(), manifest);
  } else if (scenario === "workspace-pod-recovery") {
    const before = await readWorkspaceProof();
    const { state } = await proveThenMutate({
      readEvidence,
      manifest,
      mutate: (proofVersion) => deleteWorkspacePod({
        namespace: manifest.providerResources?.namespace,
        podName: manifest.providerResources?.podName,
        proofVersion
      })
    });
    initialBalanceCents = Number(state.wallet?.balanceCents);
    const after = await waitWorkspaceRecovery();
    if (
      !before?.digest || before.digest !== after?.digest ||
      before.pvcName !== manifest.providerResources?.pvcName || after.pvcName !== before.pvcName ||
      !after.podName || after.podName === before.podName
    ) throw faultError("production_fault_workspace_recovery_invalid");
    manifest.providerResources.podNames = [before.podName, after.podName];
    await persistManifest(manifest);
    evidence = { digest: after.digest, pvcName: after.pvcName, storagePreserved: true };
  } else if (scenario === "storage-detach-reattach") {
    const before = await readWorkspaceProof();
    const detached = await proveThenMutate({
      readEvidence,
      manifest,
      mutate: (proofVersion) => detachStorage({ attachmentId: manifest.ids.attachmentId, proofVersion })
    });
    initialBalanceCents = Number(detached.state.wallet?.balanceCents);
    if (detached.value?.id !== manifest.ids.attachmentId || detached.value?.status !== "detached") {
      throw faultError("production_fault_detach_invalid");
    }
    const reattachState = await readEvidence();
    assertProductionFaultDetachedOwnership(reattachState, manifest);
    const reattached = {
      state: reattachState,
      value: await reattachStorage({ attachmentId: manifest.ids.attachmentId, proofVersion: reattachState.evidenceVersion })
    };
    if (
      !reattached.value?.id || reattached.value.id === manifest.ids.attachmentId || reattached.value?.status !== "attached" ||
      reattached.value?.pvcName !== before.pvcName || reattached.value?.digest !== before.digest
    ) throw faultError("production_fault_reattach_invalid");
    manifest.ids.detachedAttachmentId = manifest.ids.attachmentId;
    manifest.ids.attachmentId = reattached.value.id;
    await persistManifest(manifest);
    evidence = { attachmentId: reattached.value.id, digest: before.digest, pvcName: before.pvcName, storagePreserved: true };
  } else if (scenario === "machine-external-delete") {
    const identity = manifest.machineIdentities[manifest.ids.computeAllocationId];
    const deleted = await proveThenMutate({
      readEvidence,
      manifest,
      mutate: (proofVersion) => deleteMachine({ instanceId: identity.instanceId, proofVersion })
    });
    initialBalanceCents = Number(deleted.state.wallet?.balanceCents);
    if (
      deleted.value?.instanceId !== identity.instanceId || deleted.value?.status !== "destroyed" ||
      deleted.value?.providerData?.deleteMethod !== "DeleteClusterMachines" ||
      deleted.value?.providerData?.deleteMode !== "terminate"
    ) throw faultError("production_fault_machine_delete_invalid");
    const synced = await syncCompute({ computeAllocationId: manifest.ids.computeAllocationId });
    if (
      synced?.status !== "external_deleted" || synced?.billingStatus !== "stopped" ||
      synced?.holdId !== manifest.holdIds.compute || !synced?.holdReleaseId
    ) throw faultError("production_fault_machine_reconcile_invalid");
    const after = await readEvidence();
    const compute = computeFor(after, manifest);
    const reassigned = (after.computeAllocations || []).filter((row) => (
      row?.id !== compute.id &&
      row?.machineName === identity.machineId &&
      (row?.instanceId || row?.cvmInstanceId) === identity.instanceId &&
      row?.nodeName === identity.nodeName
    ));
    if (
      compute.status !== "external_deleted" || compute.billingStatus !== "stopped" ||
      compute.holdId !== manifest.holdIds.compute || compute.holdReleaseId !== synced.holdReleaseId ||
      after.wallet?.balanceCents !== initialBalanceCents || reassigned.length
    ) throw faultError("production_fault_machine_terminal_invalid");
    evidence = {
      billingStatus: compute.billingStatus,
      holdId: compute.holdId,
      holdReleaseId: compute.holdReleaseId,
      balanceCentsBefore: initialBalanceCents,
      balanceCentsAfter: after.wallet.balanceCents,
      allocatorReassignmentCount: 0
    };
  } else {
    const state = await readEvidence();
    assertProductionFaultOwnership(state, manifest);
    initialBalanceCents = Number(state.wallet?.balanceCents);
    try {
      await forceBrowserFailure({ proofVersion: state.evidenceVersion });
      throw faultError("production_fault_browser_failure_missing");
    } catch (error) {
      if (error.message !== "forced_browser_failure") throw error;
      evidence = { browserError: error.message };
    }
  }

  if (!Number.isFinite(initialBalanceCents)) throw faultError("production_fault_wallet_invalid");

  await releaseVerifier({ manifest });
  const verifierResult = await awaitVerifierCleanup({ manifest });
  if (verifierResult?.ok !== true) throw faultError("production_fault_cleanup_failed", verifierResult);
  assertTerminalManagementState(await readEvidence(), manifest, initialBalanceCents);
  assertProductionFaultProviderTruth(await readProviderTruth({ manifest }), manifest);
  return { ok: true, scenario, evidence, cleanupErrors: [] };
}

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    args[item.slice(2)] = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
  }
  return args;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

export async function waitForProductionFaultProviderTruth({ probe, timeoutMs, pollMs, accept = (result) => result?.ok === true }) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() <= deadline) {
    const result = await probe();
    if (accept(result)) return result;
    if (result?.ok !== true && result?.errorCode !== "provider_truth_partial_identity") {
      throw faultError("production_fault_provider_truth_invalid", result);
    }
    if (Date.now() >= deadline) break;
    await sleep(pollMs);
  }
  throw faultError("production_fault_provider_truth_timeout");
}

async function atomicWriteJson(path, value) {
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${process.pid}.${Math.random().toString(36).slice(2)}.tmp`;
  await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  await rename(temporary, path);
}

async function waitForJson(path, { timeoutMs, pollMs }) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      return JSON.parse(await readFile(path, "utf8"));
    } catch (error) {
      if (error?.code !== "ENOENT" && !(error instanceof SyntaxError)) throw error;
    }
    await sleep(pollMs);
  }
  throw faultError("production_fault_ready_timeout");
}

function runCommand(spawnImpl, command, args, { env, input = "" } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawnImpl(command, args, { env, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout?.on("data", (chunk) => { stdout += String(chunk).slice(0, 1024 * 1024 - stdout.length); });
    child.stderr?.on("data", (chunk) => { stderr += String(chunk).slice(0, 1024 * 1024 - stderr.length); });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
    if (input) child.stdin?.write(input);
    child.stdin?.end();
  });
}

async function commandJson(spawnImpl, command, args, options = {}) {
  const result = await runCommand(spawnImpl, command, args, options);
  if (result.code !== 0) throw faultError("production_fault_command_failed", { command, args, stderr: result.stderr.slice(-2000) });
  if (!result.stdout.trim()) return null;
  try {
    return JSON.parse(result.stdout);
  } catch {
    throw faultError("production_fault_command_json_invalid", { command, args });
  }
}

async function requestJson(fetchImpl, url, { method = "GET", auth = null, token = "", idempotencyKey = "", body = undefined } = {}) {
  const headers = {};
  if (auth?.cookie) headers.cookie = auth.cookie;
  if (auth?.csrf && method !== "GET") headers["x-opl-csrf"] = auth.csrf;
  if (token) headers.authorization = `Bearer ${token}`;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  if (body !== undefined) headers["content-type"] = "application/json";
  const response = await fetchImpl(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await response.text();
  let payload = null;
  try { payload = text ? JSON.parse(text) : {}; } catch { payload = { error: text.slice(0, 500) }; }
  if (!response.ok) throw faultError(`production_fault_request_failed:${method}:${new URL(url).pathname}:${response.status}`, payload);
  return payload;
}

function authOwner(env, accountId) {
  const owner = verificationOwnerFromSeed(env.OPL_VERIFY_AUTH_USERS_JSON, accountId);
  if (!owner.accountId || owner.accountId !== accountId) throw faultError("production_fault_owner_required");
  return owner;
}

function exactStateResource(rows, id, accountId) {
  return exact(rows, (row) => row?.id === id && (row?.accountId || row?.ownerAccountId) === accountId, "production_fault_ownership_mismatch");
}

async function podDigest({ kubectlText, namespace, podName, filePath, pvcName }) {
  if (!filePath?.startsWith("/data/") || !podName || !pvcName) throw faultError("production_fault_file_proof_invalid");
  const output = await kubectlText(["exec", "-n", namespace, `pod/${podName}`, "--", "sha256sum", "--", filePath]);
  const digest = output.trim().split(/\s+/)[0];
  if (!/^[a-f0-9]{64}$/.test(digest)) throw faultError("production_fault_file_proof_invalid");
  return { digest: `sha256:${digest}`, pvcName, podName };
}

function verifierChild({ spawnImpl, options, env }) {
  const args = [
    "tools/production-verifier.ts", "--origin", options.origin, "--account", options.accountId,
    "--package", "basic", "--run-id", options.runId, "--slot", options.slot,
    "--manifest-path", options.manifestPath, "--ready-file", options.readyFile,
    "--release-file", options.releaseFile, "--barrier-timeout-ms", String(options.timeoutMs),
    "--fault-ready-only"
  ];
  const child = spawnImpl(process.execPath, args, { env: { ...env, OPL_VERIFY_BROWSER_E2E: "0" }, stdio: ["ignore", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  const completion = new Promise((resolve, reject) => {
    child.stdout?.on("data", (chunk) => { stdout += String(chunk).slice(0, 65536 - stdout.length); });
    child.stderr?.on("data", (chunk) => { stderr += String(chunk).slice(0, 65536 - stderr.length); });
    child.on("error", reject);
    child.on("close", (code) => resolve({ code, stdout, stderr }));
  });
  return { child, completion };
}

function productionFaultAdapters({ options, manifest, auth, fetchImpl, spawnImpl, child, env }) {
  const consoleOrigin = options.origin.replace(/\/$/, "");
  const fabricOrigin = options.fabricOrigin.replace(/\/$/, "");
  const ledgerOrigin = options.ledgerOrigin.replace(/\/$/, "");
  const token = options.internalToken;
  const kubectlJson = (args) => commandJson(spawnImpl, "kubectl", ["--kubeconfig", options.kubeconfig, ...args], { env });
  const kubectlText = async (args) => {
    const result = await runCommand(spawnImpl, "kubectl", ["--kubeconfig", options.kubeconfig, ...args], { env });
    if (result.code !== 0) throw faultError("production_fault_kubectl_failed", { args, stderr: result.stderr.slice(-2000) });
    return result.stdout;
  };
  let proofCounter = 0;
  let currentPodName = manifest.providerResources.podName;
  let released = false;

  const publicMutation = (path, stage, body) => requestJson(fetchImpl, `${consoleOrigin}${path}`, {
    method: "POST", auth,
    idempotencyKey: productionVerificationMutationKey(manifest.runId, manifest.slot, stage),
    body
  });
  const ownership = () => requestJson(fetchImpl, `${fabricOrigin}/fabric/machine-ownerships/${encodeURIComponent(manifest.ids.computeAllocationId)}`, { token });
  const providerTruth = ({ accountId, packageId, nodePoolId, identity, accept }) => waitForProductionFaultProviderTruth({
    timeoutMs: options.timeoutMs,
    pollMs: options.pollMs,
    accept,
    probe: () => commandJson(spawnImpl, options.provisionerBin, [], { env, input: JSON.stringify({
      action: "provider_truth", accountId, packageId,
      storageVolumeId: manifest.providerResources.cbsVolumeHandle,
      pool: { id: "basic", clusterId: options.clusterId, nodePoolId },
      allocation: {
        id: manifest.ids.computeAllocationId, instanceId: identity.instanceId,
        machineName: identity.machineId, nodeName: identity.nodeName, privateIp: identity.privateIp
      }
    }) })
  });
  const readEvidence = async () => {
    const state = await requestJson(fetchImpl, `${consoleOrigin}/api/state?accountId=${encodeURIComponent(manifest.accountId)}`, { auth });
    const exactOwnership = await ownership();
    const compute = exactStateResource(state.computeAllocations, manifest.ids.computeAllocationId, manifest.accountId);
    const provider = await providerTruth({
      accountId: exactOwnership.accountId,
      packageId: exactOwnership.packageId || "basic",
      nodePoolId: exactOwnership.nodePoolId,
      identity: {
        instanceId: exactOwnership.instanceId, machineId: exactOwnership.machineId,
        nodeName: exactOwnership.nodeName, privateIp: compute.privateIp
      }
    });
    if (provider?.machineType !== "NativeCVM") throw faultError("production_fault_ownership_mismatch");
    compute.providerData = { ...(compute.providerData || {}), machineType: provider.machineType };
    return {
      ...state,
      account: state.account || { accountId: manifest.accountId },
      wallet: { ...state.wallet, accountId: manifest.accountId, balanceCents: Number(state.wallet?.balanceCents ?? state.wallet?.balance) },
      fabricOwnerships: [{ ...exactOwnership, machineType: provider.machineType }],
      evidenceVersion: `proof-${++proofCounter}`
    };
  };
  const readWorkspaceProof = async () => {
    const proof = await podDigest({
      kubectlText, namespace: manifest.providerResources.namespace, podName: currentPodName,
      filePath: manifest.fileProof.filePath, pvcName: manifest.providerResources.pvcName
    });
    if (proof.digest !== `sha256:${manifest.fileProof.sha256}`) throw faultError("production_fault_file_proof_invalid");
    return proof;
  };
  const releaseVerifier = async () => {
    if (released) return;
    await writeFile(options.releaseFile, "release\n", { mode: 0o600 });
    released = true;
  };

  return {
    readEvidence,
    replayCreate: ({ idempotencyKey }) => requestJson(fetchImpl, `${consoleOrigin}/api/compute-allocations`, {
      method: "POST", auth, idempotencyKey,
      body: { accountId: manifest.accountId, packageId: "basic", name: manifest.resourceNames.compute }
    }),
    readWorkspaceProof,
    deleteWorkspacePod: ({ namespace, podName }) => kubectlText(["delete", "-n", namespace, `pod/${podName}`, "--wait=true"]),
    waitWorkspaceRecovery: async () => {
      const deadline = Date.now() + options.timeoutMs;
      while (Date.now() < deadline) {
        const pods = await kubectlJson(["get", "pod", "-n", manifest.providerResources.namespace, "-l", `oplcloud.cn/workspace-id=${manifest.ids.workspaceId}`, "-o", "json"]);
        const matches = (pods?.items || []).filter((pod) => (
          pod?.metadata?.name !== currentPodName && pod?.metadata?.labels?.["oplcloud.cn/resource-id"] === manifest.ids.computeAllocationId &&
          pod?.status?.phase === "Running" && (pod?.status?.containerStatuses || []).every((status) => status.ready === true)
        ));
        if (matches.length === 1) {
          currentPodName = matches[0].metadata.name;
          return readWorkspaceProof();
        }
        if (matches.length > 1) throw faultError("production_fault_provider_resources_invalid");
        await sleep(options.pollMs);
      }
      throw faultError("production_fault_workspace_recovery_timeout");
    },
    detachStorage: ({ attachmentId }) => publicMutation("/api/storage-attachments/detach", "fault-detach", {
      accountId: manifest.accountId, attachmentId, confirm: true
    }),
    reattachStorage: async () => {
      const attachment = await publicMutation("/api/storage-attachments", "fault-reattach", {
        accountId: manifest.accountId, workspaceId: manifest.ids.workspaceId,
        computeAllocationId: manifest.ids.computeAllocationId, storageId: manifest.ids.storageId, mountPath: "/data"
      });
      assertProductionFaultReattachOwnership(await readEvidence(), manifest, attachment.id);
      const workspace = await publicMutation("/api/workspaces", "fault-reattach-workspace", {
        accountId: manifest.accountId, workspaceName: manifest.resourceNames.workspace, attachmentId: attachment.id
      });
      if (workspace?.id !== manifest.ids.workspaceId) throw faultError("production_fault_reattach_invalid");
      const proof = await readWorkspaceProof();
      return { ...attachment, status: "attached", ...proof };
    },
    persistManifest: (value) => atomicWriteJson(options.manifestPath, value),
    deleteMachine: async () => {
      const state = await readEvidence();
      const compute = exactStateResource(state.computeAllocations, manifest.ids.computeAllocationId, manifest.accountId);
      const exactOwnership = exact(state.fabricOwnerships, (row) => row.resourceId === manifest.ids.computeAllocationId, "production_fault_ownership_mismatch");
      const request = {
        action: "destroy_compute_allocation", accountId: exactOwnership.accountId, packageId: exactOwnership.packageId || "basic",
        pool: { id: "basic", clusterId: options.clusterId, nodePoolId: exactOwnership.nodePoolId },
        allocation: {
          id: manifest.ids.computeAllocationId, instanceId: exactOwnership.instanceId,
          machineName: exactOwnership.machineId, nodeName: exactOwnership.nodeName, privateIp: compute.privateIp
        }
      };
      const result = await commandJson(spawnImpl, options.provisionerBin, [], { env, input: JSON.stringify(request) });
      if (result?.ok !== true) throw faultError("production_fault_machine_delete_failed", result);
      return { ...result, instanceId: exactOwnership.instanceId };
    },
    syncCompute: async ({ computeAllocationId }) => {
      return publicMutation(`/api/compute-allocations/${encodeURIComponent(computeAllocationId)}/sync`, "fault-sync-compute", {
        accountId: manifest.accountId, computeAllocationId
      });
    },
    forceBrowserFailure: async () => {
      const browserModule = await import("playwright");
      const browser = await browserModule.chromium.launch({ headless: true });
      try {
        const page = await browser.newPage();
        await page.goto(manifest.workspaceUrl, { waitUntil: "domcontentloaded" });
        throw new Error("forced_browser_failure");
      } finally {
        await browser.close();
      }
    },
    releaseVerifier,
    awaitVerifierCleanup: async () => {
      const result = await child.completion;
      if (result.code !== 0) return { ok: false, error: result.stderr.slice(-4000) || result.stdout.slice(-4000) };
      try { return JSON.parse(result.stdout); } catch { return { ok: false, error: "production_fault_verifier_output_invalid" }; }
    },
    readProviderTruth: async () => {
      const releasedOwnership = await ownership();
      const computeHold = await requestJson(fetchImpl, `${ledgerOrigin}/ledger/holds/${encodeURIComponent(manifest.holdIds.compute)}`, { token });
      const storageHold = await requestJson(fetchImpl, `${ledgerOrigin}/ledger/holds/${encodeURIComponent(manifest.holdIds.storage)}`, { token });
      const identity = manifest.machineIdentities[manifest.ids.computeAllocationId];
      const provider = await providerTruth({
        accountId: releasedOwnership.accountId,
        packageId: releasedOwnership.packageId || "basic",
        nodePoolId: releasedOwnership.nodePoolId,
        identity,
        accept: (result) => result?.ok === true && result.machinePresent === false && result.storagePresent === false
      });
      const absent = async (kind, name, namespace = "") => {
        const args = ["get", `${kind}/${name}`];
        if (namespace) args.push("-n", namespace);
        args.push("--ignore-not-found=true", "-o", "json");
        return (await kubectlJson(args)) == null;
      };
      const resources = manifest.providerResources;
      const podNames = resources.podNames || [resources.podName];
      const podsPresent = [];
      for (const podName of podNames) if (!await absent("pod", podName, resources.namespace)) podsPresent.push(podName);
      const workspaceResponse = await fetchImpl(manifest.workspaceUrl, { redirect: "manual" });
      return {
        accountId: releasedOwnership.accountId,
        resourceId: manifest.ids.computeAllocationId,
        ownership: releasedOwnership,
        ledger: { compute: computeHold, storage: storageHold },
        tencent: {
          instanceId: identity.instanceId,
          storageVolumeId: resources.cbsVolumeHandle,
          cvmStatus: provider.cvmStatus,
          tkeStatus: provider.tkeStatus,
          cbsStatus: provider.cbsStatus,
          machinePresent: provider.machinePresent,
          storagePresent: provider.storagePresent
        },
        kubernetes: {
          nodeName: identity.nodeName, nodePresent: !await absent("node", identity.nodeName),
          namespace: resources.namespace,
          deploymentName: resources.deploymentName, deploymentPresent: !await absent("deployment", resources.deploymentName, resources.namespace),
          serviceName: resources.serviceName, servicePresent: !await absent("service", resources.serviceName, resources.namespace),
          secretName: resources.secretName, secretPresent: !await absent("secret", resources.secretName, resources.namespace),
          pvcName: resources.pvcName, pvcPresent: !await absent("pvc", resources.pvcName, resources.namespace),
          pvName: resources.pvName, pvPresent: !await absent("pv", resources.pvName),
          cbsVolumeHandle: resources.cbsVolumeHandle, pvReclaimPolicy: resources.pvReclaimPolicy,
          podNames, podsPresent
        },
        workspaceUrlStatus: workspaceResponse.status
      };
    }
  };
}

export async function runProductionFaultVerifierCli({
  argv = process.argv.slice(2), env = process.env, stdout = process.stdout, stderr = process.stderr,
  fetchImpl = globalThis.fetch, spawnImpl = spawn
} = {}) {
  const args = cliArgs(argv);
  if (argv.includes("--help") || argv.includes("-h")) {
    stdout.write("Usage: node tools/production-fault-verifier.ts --scenario <name> --account <id> --run-id <id> --slot <id> --manifest-path <path> --ready-file <path> --release-file <path>\n");
    return 0;
  }
  let adapters = null;
  let child = null;
  let options = null;
  let manifest = null;
  let baselineBalanceCents = Number.NaN;
  try {
    options = {
      scenario: args.scenario || env.OPL_FAULT_SCENARIO,
      origin: args.origin || env.OPL_CONSOLE_ORIGIN,
      accountId: args.account || env.OPL_VERIFY_ACCOUNT_ID,
      runId: args["run-id"] || env.OPL_VERIFY_RUN_ID,
      slot: args.slot || env.OPL_VERIFY_SLOT || "01",
      manifestPath: args["manifest-path"] || env.OPL_VERIFY_MANIFEST_PATH,
      readyFile: args["ready-file"] || env.OPL_VERIFY_READY_FILE,
      releaseFile: args["release-file"] || env.OPL_VERIFY_RELEASE_FILE,
      namespace: env.OPL_K8S_NAMESPACE || "opl-cloud",
      kubeconfig: env.KUBECONFIG,
      clusterId: env.TENCENT_DEPLOY_CLUSTER_ID,
      provisionerBin: env.OPL_TENCENT_PROVISIONER_BIN,
      fabricOrigin: env.OPL_EXECUTION_FABRIC_ORIGIN || "http://127.0.0.1:18082",
      ledgerOrigin: env.OPL_EXECUTION_LEDGER_ORIGIN || "http://127.0.0.1:18081",
      internalToken: env.OPL_EXECUTION_INTERNAL_SERVICE_TOKEN,
      timeoutMs: Number(args["timeout-ms"] || env.OPL_FAULT_TIMEOUT_MS || 20 * 60 * 1000),
      pollMs: Number(args["poll-ms"] || env.OPL_FAULT_POLL_MS || 1000)
    };
    if (
      options.origin !== "https://cloud.medopl.cn" || !FAULT_SCENARIOS.includes(options.scenario) ||
      !options.accountId || !options.runId || !options.manifestPath || !options.readyFile || !options.releaseFile ||
      !options.kubeconfig || !options.clusterId || !options.provisionerBin || !options.internalToken ||
      !Number.isFinite(options.timeoutMs) || options.timeoutMs <= 0 || !Number.isFinite(options.pollMs) || options.pollMs <= 0
    ) throw faultError("production_fault_cli_invalid");
    child = verifierChild({ spawnImpl, options, env });
    await waitForJson(options.readyFile, { timeoutMs: options.timeoutMs, pollMs: options.pollMs });
    manifest = await waitForJson(options.manifestPath, { timeoutMs: options.timeoutMs, pollMs: options.pollMs });
    if (manifest.runId !== options.runId || manifest.slot !== options.slot || manifest.accountId !== options.accountId) {
      throw faultError("production_fault_manifest_invalid");
    }
    const kubectlJson = (kubectlArgs) => commandJson(spawnImpl, "kubectl", ["--kubeconfig", options.kubeconfig, ...kubectlArgs], { env });
    await enrichProductionFaultManifest({
      manifest, namespace: options.namespace, kubectlJson,
      persistManifest: (value) => atomicWriteJson(options.manifestPath, value)
    });
    const owner = authOwner(env, options.accountId);
    const auth = await requestOwnerSession({ fetchImpl, origin: options.origin, email: owner.email, password: owner.password });
    adapters = productionFaultAdapters({ options, manifest, auth, fetchImpl, spawnImpl, child, env });
    baselineBalanceCents = Number((await adapters.readEvidence()).wallet?.balanceCents);
    const result = await runProductionFaultDrill({ scenario: options.scenario, manifest, ...adapters });
    stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    return 0;
  } catch (error) {
    let finalEvidenceError = null;
    if (adapters) {
      try { await adapters.releaseVerifier(); } catch {}
      try { await adapters.awaitVerifierCleanup(); } catch {}
      try {
        assertTerminalManagementState(await adapters.readEvidence(), manifest, baselineBalanceCents);
        assertProductionFaultProviderTruth(await adapters.readProviderTruth({ manifest }), manifest);
      } catch (cleanupError) {
        finalEvidenceError = cleanupError.message;
      }
    } else if (child && options?.releaseFile) {
      try { await writeFile(options.releaseFile, "release\n", { mode: 0o600 }); } catch {}
      try { await child.completion; } catch {}
    }
    stderr.write(`${JSON.stringify({
      ok: false,
      error: error.message,
      ...(error.details ? { details: error.details } : {}),
      ...(finalEvidenceError ? { finalEvidenceError } : { cleanupVerified: Boolean(adapters) })
    }, null, 2)}\n`);
    return 1;
  }
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runProductionFaultVerifierCli().then((code) => { process.exitCode = code; });
}
