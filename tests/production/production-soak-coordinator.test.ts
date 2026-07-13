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
      await writeFile(manifestPath, JSON.stringify(manifest(Number(slot), { runId })));
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

  const result = await runProductionSoak({
    origin: "https://cloud.medopl.cn",
    accountId: "account-production",
    baseRunId: "soak-run",
    artifactDir: root,
    soakDurationMs: 9,
    evidenceIntervalMs: 3,
    readyPollMs: 1,
    spawnImpl,
    sleepImpl: immediate,
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
  assert.equal(evidenceCalls, 5, "barrier + three soak polls + final zero-residual check");
  assert.equal(result.ok, true);
  assert.deepEqual(result.children.map((child) => child.exitCode), [0, 0, 0, 0, 0]);
  assert.equal(children.length, 5);
  assert.equal(JSON.parse(await readFile(join(root, "result.json"), "utf8")).ok, true);
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
