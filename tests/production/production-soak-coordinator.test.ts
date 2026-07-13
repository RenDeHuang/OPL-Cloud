import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { access, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import test from "node:test";

import {
  DEFAULT_SOAK_DURATION_MS,
  SOAK_SLOT_COUNT,
  manifestsFromArtifactDir,
  productionSoakEvidenceCheck,
  runProductionSoak,
  validateSoakManifests
} from "../../tools/production-soak-coordinator.ts";

function manifest(index, overrides = {}) {
  const slot = String(index).padStart(2, "0");
  const runId = `soak-run-${slot}`;
  const computeId = `compute-${slot}`;
  const storageId = `storage-${slot}`;
  const attachmentId = `attachment-${slot}`;
  const workspaceId = `workspace-${slot}`;
  return {
    runId,
    slot,
    accountId: "account-production",
    resourceNames: {
      compute: `Production Verification Lab ${runId} compute ${runId}`,
      storage: `Production Verification Lab ${runId} storage ${runId}`,
      workspace: `Production Verification Lab ${runId}`
    },
    ids: { computeAllocationId: computeId, storageId, attachmentId, workspaceId },
    holdIds: { compute: `hold-compute-${slot}`, storage: `hold-storage-${slot}` },
    machineIdentities: {
      [computeId]: { machineId: `machine-${slot}`, instanceId: `instance-${slot}`, nodeName: `node-${slot}` }
    },
    workspaceId,
    workspaceUrl: `https://workspace.medopl.cn/w/${workspaceId}/`,
    ...overrides
  };
}

class FakeChild extends EventEmitter {
  stdout = new PassThrough();
  stderr = new PassThrough();
}

function argument(args, name) {
  const index = args.indexOf(name);
  assert.notEqual(index, -1, `missing ${name}`);
  return args[index + 1];
}

function immediate() {
  return new Promise((resolve) => setImmediate(resolve));
}

function terminalState(item) {
  const ids = item.ids || {};
  const computeIds = [ids.computeAllocationId, ids.replacementComputeAllocationId].filter(Boolean);
  const attachmentIds = [ids.attachmentId, ids.replacementAttachmentId].filter(Boolean);
  const workspaceIds = [ids.workspaceId, ids.replacementWorkspaceId].filter(Boolean);
  const computeAllocations = computeIds.map((id) => ({
    id,
    accountId: item.accountId,
    name: id === ids.replacementComputeAllocationId ? item.resourceNames.replacementCompute : item.resourceNames.compute,
    status: "destroyed",
    billingStatus: "stopped",
    holdId: id === ids.replacementComputeAllocationId ? item.holdIds?.replacementCompute : item.holdIds?.compute,
    holdReleaseId: `release-${id}`,
    machineName: item.machineIdentities?.[id]?.machineId,
    instanceId: item.machineIdentities?.[id]?.instanceId,
    nodeName: item.machineIdentities?.[id]?.nodeName
  }));
  const storageVolumes = ids.storageId ? [{
    id: ids.storageId,
    accountId: item.accountId,
    name: item.resourceNames.storage,
    status: "destroyed",
    billingStatus: "stopped",
    holdId: item.holdIds?.storage,
    holdReleaseId: `release-${ids.storageId}`
  }] : [];
  return {
    computeAllocations,
    storageVolumes,
    storageAttachments: attachmentIds.map((id) => ({
      id,
      accountId: item.accountId,
      status: "detached",
      computeAllocationId: id === ids.replacementAttachmentId ? ids.replacementComputeAllocationId : ids.computeAllocationId,
      storageId: ids.storageId
    })),
    workspaces: workspaceIds.map((id) => ({
      id,
      accountId: item.accountId,
      name: id === ids.replacementWorkspaceId ? item.resourceNames.replacementWorkspace : item.resourceNames.workspace,
      state: "data_deleted",
      status: "unrecoverable",
      computeAllocationId: "",
      currentComputeAllocationId: "",
      attachmentId: "",
      currentAttachmentId: "",
      storageId: ids.storageId,
      openable: false,
      accessState: "disabled",
      access: { tokenStatus: "disabled" }
    })),
    billingLedger: [...computeAllocations, ...storageVolumes].map((row) => ({
      id: row.holdReleaseId,
      accountId: item.accountId,
      resourceId: row.id,
      type: `${computeIds.includes(row.id) ? "compute" : "storage"}_hold_released`
    })),
    runtimeOperations: []
  };
}

function partialManifest(stage, index = 1) {
  const full = manifest(index);
  const partial = {
    runId: full.runId,
    slot: full.slot,
    accountId: full.accountId,
    resourceNames: full.resourceNames
  };
  if (stage >= 1) {
    partial.ids = { computeAllocationId: full.ids.computeAllocationId };
    partial.holdIds = { compute: full.holdIds.compute };
  }
  if (stage >= 2) {
    partial.ids.storageId = full.ids.storageId;
    partial.holdIds.storage = full.holdIds.storage;
  }
  if (stage >= 3) partial.ids.attachmentId = full.ids.attachmentId;
  return partial;
}

function soakResult() {
  return {
    accountId: "account-production",
    slots: Array.from({ length: 5 }, (_, index) => ({
      slot: String(index + 1).padStart(2, "0"),
      runId: `soak-run-${String(index + 1).padStart(2, "0")}`
    }))
  };
}

function managementFetch(state) {
  return async (url) => String(url).endsWith("/api/auth/operator-login")
    ? jsonResponse({ csrfToken: "csrf" }, { headers: { "set-cookie": "session=operator" } })
    : jsonResponse(state);
}

function jsonResponse(payload, init = {}) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "content-type": "application/json", ...(init.headers || {}) }
  });
}

test("production soak is exactly five slots for a bounded 15 minutes", () => {
  assert.equal(SOAK_SLOT_COUNT, 5);
  assert.equal(DEFAULT_SOAK_DURATION_MS, 15 * 60 * 1000);
});

test("soak manifests require five complete and globally distinct live identities", () => {
  const manifests = Array.from({ length: 5 }, (_, index) => manifest(index + 1));
  const validated = validateSoakManifests(manifests, {
    accountId: "account-production",
    runIds: manifests.map((item) => item.runId)
  });

  assert.equal(validated.length, 5);
  assert.deepEqual(validated.map((item) => item.workspaceUrl), manifests.map((item) => item.workspaceUrl));

  for (const mutate of [
    (items) => { delete items[0].ids.storageId; },
    (items) => { delete items[0].holdIds.compute; },
    (items) => { delete items[0].machineIdentities[items[0].ids.computeAllocationId].nodeName; },
    (items) => { items[1].ids.computeAllocationId = items[0].ids.computeAllocationId; },
    (items) => { items[1].machineIdentities[items[1].ids.computeAllocationId].instanceId = "instance-01"; },
    (items) => { items[0].workspaceUrl = "https://workspace.medopl.cn/w/wrong/?token=secret"; },
    (items) => { items[0].accountId = "other-account"; }
  ]) {
    const invalid = structuredClone(manifests);
    mutate(invalid);
    assert.throws(
      () => validateSoakManifests(invalid, { accountId: "account-production", runIds: manifests.map((item) => item.runId) }),
      /production_soak_manifest_invalid|production_soak_identity_duplicate/
    );
  }
});

test("coordinator releases only after all five are ready, polls evidence, and waits for exact cleanup", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-"));
  const calls = [];
  const ready = new Set();
  const releasedAt = [];
  const exited = new Set();
  const children = [];
  const spawnImpl = (_command, args) => {
    calls.push(args);
    const child = new FakeChild();
    children.push(child);
    const slot = argument(args, "--slot");
    const runId = argument(args, "--run-id");
    const manifestPath = argument(args, "--manifest-path");
    const readyFile = argument(args, "--ready-file");
    const releaseFile = argument(args, "--release-file");
    queueMicrotask(async () => {
      await writeFile(manifestPath, JSON.stringify(manifest(Number(slot), {
        runId,
        resourceNames: {
          compute: `Production Verification Lab ${runId} compute ${runId}`,
          storage: `Production Verification Lab ${runId} storage ${runId}`,
          workspace: `Production Verification Lab ${runId}`
        }
      })));
      await writeFile(readyFile, "{}\n");
      ready.add(slot);
      while (true) {
        try {
          await access(releaseFile);
          break;
        } catch {
          await immediate();
        }
      }
      releasedAt.push(ready.size);
      exited.add(slot);
      child.emit("close", 0, null);
    });
    return child;
  };
  let evidenceCalls = 0;
  let now = 0;

  const result = await runProductionSoak({
    origin: "https://cloud.medopl.cn",
    accountId: "account-production",
    baseRunId: "soak-run",
    artifactDir: root,
    soakDurationMs: 9,
    evidenceIntervalMs: 3,
    readyPollMs: 1,
    spawnImpl,
    nowImpl: () => now,
    sleepImpl: async (ms) => { now += ms; await immediate(); },
    evidenceCheck: async ({ phase, manifests }) => {
      evidenceCalls += 1;
      assert.equal(manifests.length, 5);
      return { phase, active: manifests.length };
    }
  });

  assert.equal(calls.length, 5);
  assert.equal(new Set(calls.map((args) => argument(args, "--run-id"))).size, 5);
  for (const name of ["--manifest-path", "--ready-file", "--release-file"]) {
    assert.equal(new Set(calls.map((args) => argument(args, name))).size, 5);
  }
  assert.deepEqual(releasedAt, [5, 5, 5, 5, 5]);
  assert.equal(exited.size, 5);
  assert.equal(evidenceCalls, 4, "barrier + two in-deadline soak polls + final terminal check");
  assert.equal(result.ok, true);
  assert.deepEqual(result.children.map((child) => child.exitCode), [0, 0, 0, 0, 0]);
  assert.equal(children.length, 5);
  assert.equal(JSON.parse(await readFile(join(root, "result.json"), "utf8")).ok, true);
});

test("soak deadline aborts hung evidence, releases children, and awaits exact cleanup", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-timeout-"));
  const cleaned = new Set();
  const spawnImpl = (_command, args) => {
    const child = new FakeChild();
    const slot = argument(args, "--slot");
    const runId = argument(args, "--run-id");
    const manifestPath = argument(args, "--manifest-path");
    const readyFile = argument(args, "--ready-file");
    const releaseFile = argument(args, "--release-file");
    queueMicrotask(async () => {
      await writeFile(manifestPath, JSON.stringify(manifest(Number(slot), {
        runId,
        resourceNames: {
          compute: `Production Verification Lab ${runId} compute ${runId}`,
          storage: `Production Verification Lab ${runId} storage ${runId}`,
          workspace: `Production Verification Lab ${runId}`
        }
      })));
      await writeFile(readyFile, "{}\n");
      while (true) {
        try {
          await access(releaseFile);
          break;
        } catch {
          await immediate();
        }
      }
      cleaned.add(slot);
      child.emit("close", 0, null);
    });
    return child;
  };
  let now = 100;

  await assert.rejects(
    runProductionSoak({
      origin: "https://cloud.medopl.cn",
      accountId: "account-production",
      baseRunId: "soak-timeout",
      artifactDir: root,
      soakDurationMs: 9,
      evidenceIntervalMs: 3,
      readyPollMs: 1,
      spawnImpl,
      nowImpl: () => now,
      sleepImpl: immediate,
      setTimeoutImpl: (callback, delay) => {
        const timer = { cancelled: false, handle: null };
        timer.handle = setImmediate(() => {
          if (!timer.cancelled) {
            now += delay;
            callback();
          }
        });
        return timer;
      },
      clearTimeoutImpl: (timer) => {
        timer.cancelled = true;
        clearImmediate(timer.handle);
      },
      evidenceCheck: ({ phase, signal, deadline }) => {
        if (phase === "final") return Promise.resolve({ controlPlaneTerminalResources: 0 });
        assert.ok(signal instanceof AbortSignal);
        assert.equal(deadline, 109);
        return new Promise(() => {});
      }
    }),
    /production_soak_evidence_timeout/
  );

  assert.equal(now, 109, "evidence latency must consume, not extend, the hold deadline");
  assert.equal(cleaned.size, 5, "deadline failure must release and await all ready children");
});

test("final evidence accepts exact persisted terminal projections and rejects bad release proof", async () => {
  const primary = manifest(1);
  const item = {
    ...primary,
    resourceNames: {
      ...primary.resourceNames,
      replacementCompute: `${primary.resourceNames.workspace} replacement compute ${primary.runId}`,
      replacementWorkspace: primary.resourceNames.workspace
    },
    ids: {
      ...primary.ids,
      replacementComputeAllocationId: "compute-replacement-01",
      replacementAttachmentId: "attachment-replacement-01",
      replacementWorkspaceId: "workspace-replacement-01"
    },
    holdIds: { ...primary.holdIds, replacementCompute: "hold-compute-replacement-01" },
    machineIdentities: {
      ...primary.machineIdentities,
      "compute-replacement-01": {
        machineId: "machine-replacement-01",
        instanceId: "instance-replacement-01",
        nodeName: "node-replacement-01"
      }
    }
  };
  const state = terminalState(item);
  const seenSignals = [];
  const fetchImpl = async (url, options) => {
    seenSignals.push(options.signal);
    if (String(url).endsWith("/api/auth/operator-login")) {
      return jsonResponse({ csrfToken: "csrf" }, { headers: { "set-cookie": "session=operator", "x-opl-csrf-token": "csrf" } });
    }
    return jsonResponse(state);
  };
  const signal = new AbortController().signal;

  assert.deepEqual(
    await productionSoakEvidenceCheck({
      phase: "final",
      manifests: [item],
      origin: "https://cloud.medopl.cn",
      operatorToken: "operator",
      fetchImpl,
      signal,
      deadline: Date.now() + 1000
    }),
    { controlPlaneTerminalResources: 7, missingSlots: 0 }
  );
  assert.deepEqual(seenSignals, [signal, signal], "operator login and management state must share the deadline signal");

  for (const mutate of [
    (copy) => { copy.computeAllocations[0].name = "other-run"; },
    (copy) => { copy.computeAllocations[0].instanceId = "instance-other"; },
    (copy) => { copy.computeAllocations[0].holdId = "wrong-hold"; },
    (copy) => { copy.billingLedger[0].id = "wrong-release"; },
    (copy) => { copy.billingLedger.push({ ...copy.billingLedger[0] }); },
    (copy) => { copy.billingLedger.push({ ...copy.billingLedger[0], id: "extra-wrong-release" }); },
    (copy) => { copy.workspaces[0].access.tokenStatus = "active"; }
  ]) {
    const invalid = structuredClone(state);
    mutate(invalid);
    await assert.rejects(
      productionSoakEvidenceCheck({
        phase: "final",
        manifests: [item],
        origin: "https://cloud.medopl.cn",
        operatorToken: "operator",
        fetchImpl: async (url) => String(url).endsWith("/api/auth/operator-login")
          ? jsonResponse({ csrfToken: "csrf" }, { headers: { "set-cookie": "session=operator" } })
          : jsonResponse(invalid),
        signal
      }),
      /production_soak_terminal_evidence_invalid/
    );
  }
});

test("residual verification loads partial manifests and checks missing slots by exact run label", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-partial-"));
  const first = manifest(1);
  await writeFile(join(root, "result.json"), JSON.stringify({
    accountId: first.accountId,
    slots: Array.from({ length: 5 }, (_, index) => ({ slot: String(index + 1).padStart(2, "0"), runId: `soak-run-${String(index + 1).padStart(2, "0")}` }))
  }));
  await writeFile(join(root, "manifest-01.json"), JSON.stringify(first));

  const loaded = await manifestsFromArtifactDir(root);
  assert.equal(loaded.length, 5);
  assert.equal(loaded.filter((item) => item.missing).length, 4);
  const state = terminalState(first);
  assert.deepEqual(
    await productionSoakEvidenceCheck({
      phase: "final",
      manifests: loaded,
      origin: "https://cloud.medopl.cn",
      operatorToken: "operator",
      fetchImpl: async (url) => String(url).endsWith("/api/auth/operator-login")
        ? jsonResponse({ csrfToken: "csrf" }, { headers: { "set-cookie": "session=operator" } })
        : jsonResponse(state),
      signal: new AbortController().signal
    }),
    { controlPlaneTerminalResources: 4, missingSlots: 4 }
  );

  state.computeAllocations.push({ id: "unknown", accountId: first.accountId, name: "Production Verification Lab soak-run-03", status: "running", billingStatus: "active" });
  await assert.rejects(
    productionSoakEvidenceCheck({
      phase: "final",
      manifests: loaded,
      origin: "https://cloud.medopl.cn",
      operatorToken: "operator",
      fetchImpl: async (url) => String(url).endsWith("/api/auth/operator-login")
        ? jsonResponse({ csrfToken: "csrf" }, { headers: { "set-cookie": "session=operator" } })
        : jsonResponse(state),
      signal: new AbortController().signal
    }),
    /production_soak_terminal_evidence_invalid/
  );
});

test("residual verification accepts each atomically persisted partial manifest stage", async () => {
  for (const [stage, expectedResources] of [[0, 0], [1, 1], [2, 2], [3, 3]]) {
    const root = await mkdtemp(join(tmpdir(), `production-soak-stage-${stage}-`));
    const partial = partialManifest(stage);
    await writeFile(join(root, "result.json"), JSON.stringify(soakResult()));
    await writeFile(join(root, "manifest-01.json"), JSON.stringify(partial));

    const loaded = await manifestsFromArtifactDir(root);
    assert.equal(loaded[0].missing, undefined, `stage ${stage} must not be treated as a missing manifest`);
    assert.deepEqual(
      await productionSoakEvidenceCheck({
        phase: "final",
        manifests: loaded,
        origin: "https://cloud.medopl.cn",
        operatorToken: "operator",
        fetchImpl: managementFetch(terminalState(partial)),
        signal: new AbortController().signal
      }),
      { controlPlaneTerminalResources: expectedResources, missingSlots: 4 }
    );
  }
});

test("partial manifest verification rejects unknown run-labelled resource, ledger, and runtime rows", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-partial-unknown-"));
  const partial = partialManifest(1);
  await writeFile(join(root, "result.json"), JSON.stringify(soakResult()));
  await writeFile(join(root, "manifest-01.json"), JSON.stringify(partial));
  const loaded = await manifestsFromArtifactDir(root);

  for (const mutate of [
    (state) => state.storageVolumes.push({
      id: "storage-unpersisted", accountId: partial.accountId, name: `Storage ${partial.runId}`, status: "destroyed", billingStatus: "stopped"
    }),
    (state) => state.billingLedger.push({
      id: "ledger-unpersisted", accountId: partial.accountId, resourceId: "storage-unpersisted", sourceEventId: `verify:${partial.runId}:storage`
    }),
    (state) => state.runtimeOperations.push({
      id: "operation-unpersisted", accountId: partial.accountId, resourceId: "storage-unpersisted", idempotencyKey: `verify:${partial.runId}:storage`
    })
  ]) {
    const state = terminalState(partial);
    mutate(state);
    await assert.rejects(
      productionSoakEvidenceCheck({
        phase: "final",
        manifests: loaded,
        origin: "https://cloud.medopl.cn",
        operatorToken: "operator",
        fetchImpl: managementFetch(state),
        signal: new AbortController().signal
      }),
      /production_soak_terminal_evidence_invalid/
    );
  }
});

test("partial manifest verification rejects known resource ownership conflicts", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-partial-ownership-"));
  const partial = partialManifest(1);
  await writeFile(join(root, "result.json"), JSON.stringify(soakResult()));
  await writeFile(join(root, "manifest-01.json"), JSON.stringify(partial));
  const loaded = await manifestsFromArtifactDir(root);

  for (const mutate of [
    (state) => { state.computeAllocations[0].accountId = "account-other"; },
    (state) => { state.computeAllocations[0].name = "other-run"; },
    (state) => { state.computeAllocations[0].holdId = "hold-other"; }
  ]) {
    const state = terminalState(partial);
    mutate(state);
    await assert.rejects(
      productionSoakEvidenceCheck({
        phase: "final",
        manifests: loaded,
        origin: "https://cloud.medopl.cn",
        operatorToken: "operator",
        fetchImpl: managementFetch(state),
        signal: new AbortController().signal
      }),
      /production_soak_terminal_evidence_invalid/
    );
  }
});

test("partial manifest loader rejects duplicate known resource ids across slots", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-partial-duplicate-"));
  const first = partialManifest(1, 1);
  const second = partialManifest(1, 2);
  second.ids.computeAllocationId = first.ids.computeAllocationId;
  await writeFile(join(root, "result.json"), JSON.stringify(soakResult()));
  await writeFile(join(root, "manifest-01.json"), JSON.stringify(first));
  await writeFile(join(root, "manifest-02.json"), JSON.stringify(second));

  await assert.rejects(manifestsFromArtifactDir(root), /production_soak_identity_duplicate:resource/);
});

test("one child failure releases ready peers and waits for every verifier cleanup", async () => {
  const root = await mkdtemp(join(tmpdir(), "production-soak-failure-"));
  const children = [];
  const cleaned = new Set();
  const spawnImpl = (_command, args) => {
    const child = new FakeChild();
    children.push(child);
    const slot = argument(args, "--slot");
    const runId = argument(args, "--run-id");
    const manifestPath = argument(args, "--manifest-path");
    const readyFile = argument(args, "--ready-file");
    const releaseFile = argument(args, "--release-file");
    queueMicrotask(async () => {
      if (slot === "03") {
        child.stderr.end('{"ok":false,"error":"create_failed","cleanupErrors":["destroy_compute:failed"]}\n');
        cleaned.add(slot);
        child.emit("close", 1, null);
        return;
      }
      if (slot === "04" || slot === "05") {
        await new Promise((resolve) => setTimeout(resolve, 5));
      }
      await writeFile(manifestPath, JSON.stringify(manifest(Number(slot), { runId })));
      await writeFile(readyFile, "{}\n");
      while (true) {
        try {
          await access(releaseFile);
          break;
        } catch {
          await immediate();
        }
      }
      cleaned.add(slot);
      child.emit("close", 0, null);
    });
    return child;
  };

  await assert.rejects(
    runProductionSoak({
      origin: "https://cloud.medopl.cn",
      accountId: "account-production",
      baseRunId: "soak-fail",
      artifactDir: root,
      soakDurationMs: 1,
      evidenceIntervalMs: 1,
      readyPollMs: 1,
      spawnImpl,
      sleepImpl: immediate,
      evidenceCheck: async () => ({ active: 0 })
    }),
    (error) => {
      assert.equal(error.result.ok, false);
      assert.deepEqual(error.result.children.map((child) => child.exitCode), [0, 0, 1, 0, 0]);
      assert.deepEqual(error.result.children[2].cleanupErrors, ["destroy_compute:failed"]);
      return true;
    }
  );
  assert.equal(children.length, 5);
  assert.equal(cleaned.size, 5, "coordinator must await existing verifier cleanup instead of killing children");
});

test("soak duration rejects non-finite, non-positive, and over-15-minute values", async () => {
  for (const soakDurationMs of [Number.NaN, Number.POSITIVE_INFINITY, 0, -1, DEFAULT_SOAK_DURATION_MS + 1]) {
    await assert.rejects(
      runProductionSoak({
        origin: "https://cloud.medopl.cn",
        accountId: "account-production",
        baseRunId: "invalid-duration",
        artifactDir: "/tmp/not-used",
        soakDurationMs
      }),
      /production_soak_duration_invalid/
    );
  }
});
