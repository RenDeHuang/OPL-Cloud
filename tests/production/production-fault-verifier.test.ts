import assert from "node:assert/strict";
import test from "node:test";

import {
  FAULT_SCENARIOS,
  assertProductionFaultProviderTruth,
  assertProductionFaultReattachOwnership,
  enrichProductionFaultManifest,
  runProductionFaultDrill,
  runProductionFaultVerifierCli,
  waitForProductionFaultProviderTruth
} from "../../tools/production-fault-verifier.ts";

function manifest() {
  return {
    runId: "fault-run-01",
    slot: "01",
    accountId: "account-production",
    resourceNames: {
      compute: "Production Verification Lab fault-run-01 compute fault-run-01",
      storage: "Production Verification Lab fault-run-01 storage fault-run-01",
      workspace: "Production Verification Lab fault-run-01"
    },
    ids: {
      computeAllocationId: "compute-fault-01",
      storageId: "storage-fault-01",
      attachmentId: "attachment-fault-01",
      workspaceId: "workspace-fault-01"
    },
    holdIds: { compute: "hold-compute-fault-01", storage: "hold-storage-fault-01" },
    machineIdentities: {
      "compute-fault-01": {
        machineId: "machine-fault-01",
        instanceId: "ins-fault-01",
        nodeName: "node-fault-01",
        privateIp: "10.0.0.31"
      }
    },
    providerResources: {
      namespace: "opl-cloud",
      deploymentName: "workspace-fault-01",
      serviceName: "workspace-fault-01",
      secretName: "workspace-fault-01-env",
      podName: "workspace-fault-01-old",
      pvcName: "pvc-fault-01",
      pvName: "pv-fault-01",
      cbsVolumeHandle: "disk-fault-01",
      pvReclaimPolicy: "Delete"
    },
    workspaceUrl: "https://workspace.medopl.cn/w/workspace-fault-01/"
  };
}

function activeEvidence(currentManifest = manifest()) {
  const identity = currentManifest.machineIdentities[currentManifest.ids.computeAllocationId];
  return {
    account: { accountId: currentManifest.accountId },
    wallet: { accountId: currentManifest.accountId, balanceCents: 9900, frozenCents: 16800 },
    computeAllocations: [{
      id: currentManifest.ids.computeAllocationId,
      accountId: currentManifest.accountId,
      name: currentManifest.resourceNames.compute,
      holdId: currentManifest.holdIds.compute,
      ledgerEntryId: "debit-compute-fault-01",
      provider: "tencent-tke",
      providerData: { machineType: "NativeCVM" },
      machineName: identity.machineId,
      instanceId: identity.instanceId,
      nodeName: identity.nodeName,
      privateIp: identity.privateIp,
      status: "running",
      billingStatus: "active"
    }],
    storageVolumes: [{
      id: currentManifest.ids.storageId,
      accountId: currentManifest.accountId,
      name: currentManifest.resourceNames.storage,
      holdId: currentManifest.holdIds.storage,
      provider: "tencent-tke",
      providerResourceId: `pvc/${currentManifest.providerResources.pvcName}`,
      status: "available",
      billingStatus: "active"
    }],
    storageAttachments: [{
      id: currentManifest.ids.attachmentId,
      accountId: currentManifest.accountId,
      computeAllocationId: currentManifest.ids.computeAllocationId,
      storageId: currentManifest.ids.storageId,
      status: "attached"
    }],
    workspaces: [{
      id: currentManifest.ids.workspaceId,
      accountId: currentManifest.accountId,
      name: currentManifest.resourceNames.workspace,
      computeAllocationId: currentManifest.ids.computeAllocationId,
      storageId: currentManifest.ids.storageId,
      attachmentId: currentManifest.ids.attachmentId,
      state: "running"
    }],
    fabricOwnerships: [{
      accountId: currentManifest.accountId,
      resourceId: currentManifest.ids.computeAllocationId,
      machineId: identity.machineId,
      instanceId: identity.instanceId,
      nodeName: identity.nodeName,
      machineType: "NativeCVM",
      status: "active"
    }],
    billingLedger: [
      { id: currentManifest.holdIds.compute, accountId: currentManifest.accountId, resourceId: currentManifest.ids.computeAllocationId, type: "compute_hold", remainingCents: 16800 },
      { id: currentManifest.holdIds.storage, accountId: currentManifest.accountId, resourceId: currentManifest.ids.storageId, type: "storage_hold", remainingCents: 16800 },
      { id: "debit-compute-fault-01", accountId: currentManifest.accountId, resourceId: currentManifest.ids.computeAllocationId, type: "compute_debit" }
    ]
  };
}

function terminalEvidence(currentManifest = manifest()) {
  const state = activeEvidence(currentManifest);
  state.wallet = { ...state.wallet, frozenCents: 0 };
  state.computeAllocations[0] = {
    ...state.computeAllocations[0],
    status: "external_deleted",
    billingStatus: "stopped",
    holdReleaseId: "release-compute-fault-01"
  };
  state.storageVolumes[0] = {
    ...state.storageVolumes[0], status: "destroyed", billingStatus: "stopped", holdReleaseId: "release-storage-fault-01"
  };
  state.storageAttachments[0].status = "detached";
  if (currentManifest.ids.detachedAttachmentId) {
    state.storageAttachments.push({
      id: currentManifest.ids.detachedAttachmentId,
      accountId: currentManifest.accountId,
      computeAllocationId: currentManifest.ids.computeAllocationId,
      storageId: currentManifest.ids.storageId,
      status: "detached"
    });
  }
  state.workspaces[0] = {
    ...state.workspaces[0], state: "data_deleted", status: "unrecoverable",
    computeAllocationId: "", storageId: currentManifest.ids.storageId, attachmentId: "", accessState: "disabled", openable: false
  };
  state.fabricOwnerships[0] = { ...state.fabricOwnerships[0], status: "released" };
  state.billingLedger[0].remainingCents = 0;
  state.billingLedger[1].remainingCents = 0;
  state.billingLedger.push({
    id: "release-compute-fault-01", accountId: currentManifest.accountId,
    resourceId: currentManifest.ids.computeAllocationId, holdId: currentManifest.holdIds.compute, type: "compute_hold_released"
  });
  state.billingLedger.push({
    id: "release-storage-fault-01", accountId: currentManifest.accountId,
    resourceId: currentManifest.ids.storageId, holdId: currentManifest.holdIds.storage, type: "storage_hold_released"
  });
  return state;
}

function detachedEvidence(currentManifest = manifest()) {
  const state = activeEvidence(currentManifest);
  state.storageAttachments[0].status = "detached";
  Object.assign(state.workspaces[0], {
    state: "suspended",
    status: "suspended",
    attachmentId: "",
    currentAttachmentId: ""
  });
  return state;
}

function providerTruth(currentManifest = manifest()) {
  return {
    accountId: currentManifest.accountId,
    resourceId: currentManifest.ids.computeAllocationId,
    ownership: {
      accountId: currentManifest.accountId,
      resourceId: currentManifest.ids.computeAllocationId,
      status: "released",
      releasedAt: "2026-07-13T10:00:00Z",
      ...currentManifest.machineIdentities[currentManifest.ids.computeAllocationId]
    },
    ledger: {
      compute: { id: currentManifest.holdIds.compute, accountId: currentManifest.accountId, resourceId: currentManifest.ids.computeAllocationId, status: "released", remainingCents: 0 },
      storage: { id: currentManifest.holdIds.storage, accountId: currentManifest.accountId, resourceId: currentManifest.ids.storageId, status: "released", remainingCents: 0 }
    },
    tencent: {
      instanceId: currentManifest.machineIdentities[currentManifest.ids.computeAllocationId].instanceId,
      storageVolumeId: currentManifest.providerResources.cbsVolumeHandle,
      cvmStatus: "TERMINATED", tkeStatus: "NOT_FOUND", cbsStatus: "NOT_FOUND",
      machinePresent: false, storagePresent: false
    },
    kubernetes: {
      nodeName: currentManifest.machineIdentities[currentManifest.ids.computeAllocationId].nodeName,
      nodePresent: false,
      namespace: currentManifest.providerResources.namespace,
      deploymentName: currentManifest.providerResources.deploymentName,
      deploymentPresent: false,
      serviceName: currentManifest.providerResources.serviceName,
      servicePresent: false,
      pvcName: currentManifest.providerResources.pvcName,
      pvcPresent: false,
      pvName: currentManifest.providerResources.pvName,
      pvPresent: false,
      secretName: currentManifest.providerResources.secretName,
      secretPresent: false,
      cbsVolumeHandle: currentManifest.providerResources.cbsVolumeHandle,
      pvReclaimPolicy: currentManifest.providerResources.pvReclaimPolicy,
      podNames: currentManifest.providerResources.podNames || [currentManifest.providerResources.podName],
      podsPresent: []
    },
    workspaceUrlStatus: 410
  };
}

function dependencies({ states = [], mutate = null } = {}) {
  const currentManifest = manifest();
  const calls = [];
  let reads = 0;
  let released = false;
  let detached = false;
  let machineDeleted = false;
  const queue = states.length ? states : [activeEvidence(currentManifest), activeEvidence(currentManifest), terminalEvidence(currentManifest)];
  const deps = {
    readEvidence: async () => {
      const evidenceVersion = `proof-${reads + 1}`;
      calls.push(`read-evidence:${evidenceVersion}`);
      if (released) return structuredClone(terminalEvidence(currentManifest));
      if (detached && !states.length) return { ...structuredClone(detachedEvidence(currentManifest)), evidenceVersion };
      if (machineDeleted && !states.length) return { ...structuredClone(terminalEvidence(currentManifest)), evidenceVersion };
      return { ...structuredClone(queue[Math.min(reads++, queue.length - 1)]), evidenceVersion };
    },
    replayCreate: async ({ idempotencyKey, proofVersion }) => {
      calls.push(`replay:${idempotencyKey}:${proofVersion}`);
      mutate?.("replay");
      return { id: currentManifest.ids.computeAllocationId, holdId: currentManifest.holdIds.compute };
    },
    readWorkspaceProof: async () => {
      calls.push("read-workspace-proof");
      return { digest: "sha256:fault-proof", pvcName: currentManifest.providerResources.pvcName, podName: currentManifest.providerResources.podName };
    },
    deleteWorkspacePod: async ({ namespace, podName, proofVersion }) => {
      calls.push(`delete-pod:${namespace}:${podName}:${proofVersion}`);
      mutate?.("delete-pod");
    },
    waitWorkspaceRecovery: async () => {
      calls.push("wait-workspace-recovery");
      return { digest: "sha256:fault-proof", pvcName: currentManifest.providerResources.pvcName, podName: "workspace-fault-01-new" };
    },
    detachStorage: async ({ attachmentId, proofVersion }) => {
      calls.push(`detach:${attachmentId}:${proofVersion}`);
      detached = true;
      mutate?.("detach");
      return { id: attachmentId, status: "detached" };
    },
    reattachStorage: async ({ attachmentId, proofVersion }) => {
      calls.push(`reattach:${attachmentId}:${proofVersion}`);
      mutate?.("reattach");
      return { id: "attachment-fault-02", status: "attached", pvcName: currentManifest.providerResources.pvcName, digest: "sha256:fault-proof" };
    },
    deleteMachine: async ({ instanceId, proofVersion }) => {
      calls.push(`delete-machine:${instanceId}:${proofVersion}`);
      machineDeleted = true;
      mutate?.("delete-machine");
      return { status: "destroyed", instanceId, providerData: { deleteMethod: "DeleteClusterMachines", deleteMode: "terminate" } };
    },
    syncCompute: async ({ computeAllocationId }) => {
      calls.push(`sync:${computeAllocationId}`);
      return terminalEvidence(currentManifest).computeAllocations[0];
    },
    forceBrowserFailure: async ({ proofVersion }) => {
      calls.push(`force-browser-failure:${proofVersion}`);
      mutate?.("browser-failure");
      throw new Error("forced_browser_failure");
    },
    persistManifest: async () => { calls.push(`persist-manifest:${currentManifest.ids.attachmentId}`); },
    releaseVerifier: async () => { calls.push("release-verifier"); released = true; },
    awaitVerifierCleanup: async () => { calls.push("await-verifier-cleanup"); return { ok: true }; },
    readProviderTruth: async () => { calls.push("read-provider-truth"); return providerTruth(currentManifest); }
  };
  return { calls, deps: { manifest: currentManifest, ...deps } };
}

test("fault verifier exposes only the five resource-scoped drills", () => {
  assert.deepEqual(FAULT_SCENARIOS, [
    "lost-response-replay",
    "workspace-pod-recovery",
    "storage-detach-reattach",
    "machine-external-delete",
    "browser-failure-cleanup"
  ]);
});

test("every fault mutation follows a fresh exact ownership proof", async () => {
  for (const scenario of FAULT_SCENARIOS) {
    const { calls, deps } = dependencies();
    const result = await runProductionFaultDrill({ scenario, manifest: manifest(), ...deps });
    const mutationIndexes = calls.flatMap((call, index) => /^(replay|delete-pod|detach|reattach|delete-machine|force-browser-failure)/.test(call) ? [index] : []);
    for (const index of mutationIndexes) {
      assert.match(calls[index - 1], /^read-evidence:(proof-\d+)$/, `${scenario}: ${calls[index]}`);
      assert.ok(calls[index].endsWith(calls[index - 1].slice("read-evidence:".length)), `${scenario}: stale proof for ${calls[index]}`);
    }
    assert.equal(result.ok, true, scenario);
    assert.equal(calls.at(-4), "release-verifier", scenario);
    assert.equal(calls.at(-3), "await-verifier-cleanup", scenario);
    assert.match(calls.at(-2), /^read-evidence:proof-\d+$/, scenario);
    assert.equal(calls.at(-1), "read-provider-truth", scenario);
  }
});

test("wrong account/name, duplicate ownership, missing triple, old CXM, and unknown provider make zero mutations", async () => {
  for (const mutateState of [
    (state) => { state.account.accountId = "account-other"; },
    (state) => { state.computeAllocations[0].name = "unrelated"; },
    (state) => { state.fabricOwnerships.push({ ...state.fabricOwnerships[0] }); },
    (state) => { delete state.computeAllocations[0].nodeName; },
    (state) => { delete state.computeAllocations[0].privateIp; },
    (state) => { state.computeAllocations[0].instanceId = "np-old-cxm"; state.fabricOwnerships[0].instanceId = "np-old-cxm"; },
    (state) => { state.computeAllocations[0].provider = "unknown"; }
  ]) {
    const state = activeEvidence();
    mutateState(state);
    let mutations = 0;
    const { calls, deps } = dependencies({ states: [state], mutate: () => { mutations += 1; } });
    await assert.rejects(
      runProductionFaultDrill({ scenario: "machine-external-delete", manifest: manifest(), ...deps }),
      /production_fault_ownership_mismatch/
    );
    assert.equal(mutations, 0);
    assert.deepEqual(calls, ["read-evidence:proof-1"]);
  }
});

test("lost response replay preserves one resource, Hold, and first-hour debit", async () => {
  const replayed = activeEvidence();
  const { calls, deps } = dependencies({ states: [activeEvidence(), replayed, terminalEvidence()] });
  const result = await runProductionFaultDrill({ scenario: "lost-response-replay", manifest: manifest(), ...deps });
  assert.equal(result.evidence.resourceCount, 1);
  assert.equal(result.evidence.holdCount, 1);
  assert.equal(result.evidence.firstHourDebitCount, 1);
  assert.ok(calls.includes("replay:production-verification:fault-run-01:01:create-compute:proof-1"));
});

test("Pod recovery and detach/reattach preserve the exact PVC digest", async () => {
  for (const scenario of ["workspace-pod-recovery", "storage-detach-reattach"]) {
    const { deps } = dependencies();
    const result = await runProductionFaultDrill({ scenario, manifest: manifest(), ...deps });
    assert.equal(result.evidence.digest, "sha256:fault-proof");
    assert.equal(result.evidence.pvcName, "pvc-fault-01");
    assert.equal(result.evidence.storagePreserved, true);
    if (scenario === "workspace-pod-recovery") assert.deepEqual(deps.manifest.providerResources.podNames, ["workspace-fault-01-old", "workspace-fault-01-new"]);
    if (scenario === "storage-detach-reattach") {
      assert.equal(result.evidence.attachmentId, "attachment-fault-02");
      assert.equal(deps.manifest.ids.detachedAttachmentId, "attachment-fault-01");
    }
  }
});

test("external Machine deletion stops billing, releases the same Hold, preserves balance, and cannot reassign the Machine", async () => {
  const before = activeEvidence();
  const after = terminalEvidence();
  after.wallet.balanceCents = before.wallet.balanceCents;
  const { deps } = dependencies({ states: [before, after, after] });
  const result = await runProductionFaultDrill({ scenario: "machine-external-delete", manifest: manifest(), ...deps });
  assert.deepEqual(result.evidence, {
    billingStatus: "stopped",
    holdId: "hold-compute-fault-01",
    holdReleaseId: "release-compute-fault-01",
    balanceCentsBefore: 9900,
    balanceCentsAfter: 9900,
    allocatorReassignmentCount: 0
  });
});

test("browser failure remains the primary error while exact cleanup still completes", async () => {
  const { calls, deps } = dependencies();
  const result = await runProductionFaultDrill({ scenario: "browser-failure-cleanup", manifest: manifest(), ...deps });
  assert.equal(result.evidence.browserError, "forced_browser_failure");
  assert.ok(calls.indexOf("force-browser-failure") < calls.indexOf("release-verifier"));
  assert.equal(calls.filter((call) => call === "release-verifier").length, 1);
});

test("provider truth requires exact released Fabric and absent or terminal Tencent/Kubernetes resources", () => {
  assert.doesNotThrow(() => assertProductionFaultProviderTruth(providerTruth(), manifest()));
  for (const mutateTruth of [
    (truth) => { truth.ownership.status = "active"; },
    (truth) => { delete truth.ownership.releasedAt; },
    (truth) => { truth.ledger.compute.remainingCents = 1; },
    (truth) => { truth.ledger.storage.status = "active"; },
    (truth) => { truth.tencent.instanceId = "ins-other"; },
    (truth) => { truth.tencent.cvmStatus = "UNKNOWN"; },
    (truth) => { truth.tencent.storagePresent = true; },
    (truth) => { truth.kubernetes.nodePresent = true; },
    (truth) => { truth.kubernetes.pvcPresent = undefined; },
    (truth) => { truth.kubernetes.secretPresent = true; },
    (truth) => { truth.kubernetes.pvReclaimPolicy = "Retain"; },
    (truth) => { truth.kubernetes.deploymentPresent = true; },
    (truth) => { truth.kubernetes.servicePresent = true; },
    (truth) => { truth.kubernetes.podsPresent = ["workspace-fault-01-new"]; }
    ,(truth) => { truth.workspaceUrlStatus = 200; }
  ]) {
    const truth = providerTruth();
    mutateTruth(truth);
    assert.throws(() => assertProductionFaultProviderTruth(truth, manifest()), /production_fault_provider_truth_invalid/);
  }
});

test("provider truth retries only Tencent's partial deletion window", async () => {
  let calls = 0;
  const result = await waitForProductionFaultProviderTruth({
    timeoutMs: 100,
    pollMs: 0,
    probe: async () => (++calls === 1
      ? { ok: false, errorCode: "provider_truth_partial_identity" }
      : { ok: true, status: "absent" })
  });
  assert.equal(result.status, "absent");
  assert.equal(calls, 2);

  await assert.rejects(() => waitForProductionFaultProviderTruth({
    timeoutMs: 100,
    pollMs: 0,
    probe: async () => ({ ok: false, errorCode: "provider_truth_cvm_identity_mismatch" })
  }), /production_fault_provider_truth_invalid/);
});

test("manifest enrichment discovers only exact labeled Workspace, PVC, PV, and CBS resources", async () => {
  const currentManifest = manifest();
  const calls = [];
  let persisted = null;
  const workspaceItems = [
    { kind: "Deployment", metadata: { name: "workspace-fault-01", labels: { "oplcloud.cn/workspace-id": currentManifest.ids.workspaceId, "oplcloud.cn/resource-id": currentManifest.ids.computeAllocationId } } },
    { kind: "Service", metadata: { name: "workspace-fault-01", labels: { "oplcloud.cn/workspace-id": currentManifest.ids.workspaceId } } },
    { kind: "Secret", metadata: { name: "workspace-fault-01-env", labels: { "oplcloud.cn/workspace-id": currentManifest.ids.workspaceId } } },
    { kind: "Pod", metadata: { name: "workspace-fault-01-old", labels: { "oplcloud.cn/workspace-id": currentManifest.ids.workspaceId, "oplcloud.cn/resource-id": currentManifest.ids.computeAllocationId } } }
  ];
  const kubectlJson = async (args) => {
    calls.push(args);
    if (args[1] === "deployment,service,secret,pod") return { items: workspaceItems };
    if (args[1] === "pvc") return { items: [{ kind: "PersistentVolumeClaim", metadata: { name: "pvc-fault-01", namespace: "opl-cloud", labels: { "oplcloud.cn/storage-id": currentManifest.ids.storageId } }, spec: { volumeName: "pv-fault-01" } }] };
    if (args[1] === "pv/pv-fault-01") return { metadata: { name: "pv-fault-01" }, spec: { persistentVolumeReclaimPolicy: "Delete", claimRef: { namespace: "opl-cloud", name: "pvc-fault-01" }, csi: { volumeHandle: "disk-fault-01" } } };
    throw new Error(`unexpected_kubectl:${args.join(" ")}`);
  };

  const resources = await enrichProductionFaultManifest({
    manifest: currentManifest,
    namespace: "opl-cloud",
    kubectlJson,
    persistManifest: async (value) => { persisted = structuredClone(value); }
  });

  assert.deepEqual(resources, currentManifest.providerResources);
  assert.deepEqual(persisted.providerResources, resources);
  assert.deepEqual(calls.map((args) => args.join(" ")), [
    `get deployment,service,secret,pod -n opl-cloud -l oplcloud.cn/workspace-id=${currentManifest.ids.workspaceId} -o json`,
    `get pvc -n opl-cloud -l oplcloud.cn/storage-id=${currentManifest.ids.storageId} -o json`,
    "get pv/pv-fault-01 -o json"
  ]);
});

test("reattach adapter proof accepts only the real detached Workspace intermediate state", () => {
  const currentManifest = manifest();
  const state = activeEvidence(currentManifest);
  state.storageAttachments[0].status = "detached";
  state.storageAttachments.push({
    id: "attachment-fault-02",
    accountId: currentManifest.accountId,
    computeAllocationId: currentManifest.ids.computeAllocationId,
    storageId: currentManifest.ids.storageId,
    status: "attached"
  });
  Object.assign(state.workspaces[0], {
    state: "suspended",
    status: "suspended",
    attachmentId: "",
    currentAttachmentId: ""
  });
  assert.doesNotThrow(() => assertProductionFaultReattachOwnership(state, currentManifest, "attachment-fault-02"));
  for (const mutate of [
    (copy) => { copy.storageAttachments[0].status = "attached"; },
    (copy) => { copy.storageAttachments[1].computeAllocationId = "compute-other"; },
    (copy) => { copy.workspaces[0].attachmentId = currentManifest.ids.attachmentId; },
    (copy) => { copy.workspaces[0].storageId = "storage-other"; }
  ]) {
    const invalid = structuredClone(state);
    mutate(invalid);
    assert.throws(
      () => assertProductionFaultReattachOwnership(invalid, currentManifest, "attachment-fault-02"),
      /production_fault_ownership_mismatch/
    );
  }
});

test("fault CLI help and invalid origins are read-only", async () => {
  let output = "";
  assert.equal(await runProductionFaultVerifierCli({
    argv: ["--help"], env: {}, stdout: { write(value) { output += value; } }, stderr: { write() {} },
    fetchImpl: async () => { throw new Error("unexpected_fetch"); }, spawnImpl: () => { throw new Error("unexpected_spawn"); }
  }), 0);
  assert.match(output, /--scenario/);

  let stderr = "";
  let spawns = 0;
  const code = await runProductionFaultVerifierCli({
    argv: [
      "--scenario", "lost-response-replay", "--origin", "https://attacker.example", "--account", "acct",
      "--run-id", "run", "--manifest-path", "/tmp/manifest", "--ready-file", "/tmp/ready", "--release-file", "/tmp/release"
    ],
    env: { KUBECONFIG: "/tmp/kubeconfig", TENCENT_DEPLOY_CLUSTER_ID: "cls-test", OPL_TENCENT_PROVISIONER_BIN: "/tmp/provisioner", OPL_EXECUTION_INTERNAL_SERVICE_TOKEN: "secret" },
    stdout: { write() {} }, stderr: { write(value) { stderr += value; } },
    fetchImpl: async () => { throw new Error("unexpected_fetch"); }, spawnImpl: () => { spawns += 1; throw new Error("unexpected_spawn"); }
  });
  assert.equal(code, 1);
  assert.equal(JSON.parse(stderr).error, "production_fault_cli_invalid");
  assert.equal(spawns, 0);
});
