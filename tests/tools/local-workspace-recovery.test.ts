import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  collectLocalJ1RecoveryAuthority,
  createLocalJ1RecoveryArtifact,
  createLocalJ1RecoveryReadbackPendingArtifact,
  localJ1CleanupPlan,
  localJ1FailureDisposition,
  localJ1RecoveryArtifactPath,
  noLocalJ1ExternalWrites,
  recoveryIdentityDigest,
  summarizeLocalJ1RecoveryReadback,
  writeLocalJ1RecoveryArtifact
} from "../../tools/local-workspace-recovery.ts";

const source = { sha: "a".repeat(40), tree: "b".repeat(40) };
const operationId = "workspace-launch-sensitive";
const workspaceId = "ws-sensitive";

function attempt(overrides = {}) {
  return {
    attempted: 0,
    confirmed: 0,
    unknown: 0,
    max: 1,
    status: "",
    idempotencyKey: "",
    pendingReadbacks: 0,
    maxPendingReadbacks: 3,
    ...overrides
  };
}

function untouchedAttempt() {
  return { attempted: 0, confirmed: 0, unknown: 0, max: 1 };
}

function launchReadback(stage, overrides = {}) {
  const continuationAttemptBudgets = Object.fromEntries([
    "key", "debit", "ensure_compute_allocation", "storage", "attachment", "secret", "runtime", "activation", "receipt"
  ].map((name) => [name, untouchedAttempt()]));
  Object.assign(continuationAttemptBudgets, overrides);
  return {
    operationId,
    workspaceId,
    schemaVersion: 3,
    version: 7,
    status: "pending",
    stage,
    continuationAttemptBudgets,
    accountId: "acct-owner-field-is-not-artifact-authority",
    name: "ignored owner projection"
  };
}

const noWrite = Object.freeze({ attempted: 0, confirmed: 0, unknown: 0 });
const confirmedWrite = Object.freeze({ attempted: 1, confirmed: 1, unknown: 0 });
const unknownWrite = Object.freeze({ attempted: 1, confirmed: 0, unknown: 1 });
const zeroWrites = Object.freeze({ workspaceCreates: noWrite, keyCreates: noWrite, debits: noWrite, providerMutations: noWrite, purchaseReceipts: noWrite });

test("local J1 cleanup is permitted only before Create acceptance or after READY", () => {
  assert.deepEqual(localJ1FailureDisposition({ launchSubmitted: false, externalWrites: zeroWrites }), {
    cleanup: true, preserveRecoveryAuthority: false, reason: "pre_create_failure"
  });
  assert.deepEqual(localJ1FailureDisposition({ ready: true, launchSubmitted: true, externalWrites: { ...zeroWrites, workspaceCreates: confirmedWrite, debits: confirmedWrite } }), {
    cleanup: true, preserveRecoveryAuthority: false, reason: "ready"
  });
  for (const scenario of [
    { name: "Create response unknown", writes: zeroWrites },
    { name: "post Create", writes: { ...zeroWrites, workspaceCreates: confirmedWrite } },
    { name: "post Key", writes: { ...zeroWrites, workspaceCreates: confirmedWrite, keyCreates: confirmedWrite } },
    { name: "post Debit", writes: { ...zeroWrites, workspaceCreates: confirmedWrite, keyCreates: confirmedWrite, debits: unknownWrite } },
    { name: "provider pending", writes: { ...zeroWrites, workspaceCreates: confirmedWrite, keyCreates: confirmedWrite, debits: confirmedWrite, providerMutations: unknownWrite } },
    { name: "receipt failure", writes: { ...zeroWrites, workspaceCreates: confirmedWrite, keyCreates: confirmedWrite, debits: confirmedWrite, providerMutations: { attempted: 5, confirmed: 5, unknown: 0 }, purchaseReceipts: unknownWrite } }
  ]) {
    assert.deepEqual(localJ1FailureDisposition({ launchSubmitted: true, externalWrites: scenario.writes }), {
      cleanup: false, preserveRecoveryAuthority: true, reason: "durable_launch_or_external_write"
    }, scenario.name);
  }
});

test("cleanup plan preserves running service and volume authority after Create submission", () => {
  assert.deepEqual(localJ1CleanupPlan({ launchSubmitted: false, authority: { externalWrites: noLocalJ1ExternalWrites() } }), {
    cleanup: true,
    preserveRecoveryAuthority: false,
    reason: "pre_create_failure",
    stopServices: ["control-plane", "fabric", "ledger", "postgres"],
    removeComposeVolumes: true,
    removeRecoveryRoot: true
  });
  assert.deepEqual(localJ1CleanupPlan({ launchSubmitted: true, authority: { externalWrites: { ...zeroWrites, workspaceCreates: unknownWrite } } }), {
    cleanup: false,
    preserveRecoveryAuthority: true,
    reason: "durable_launch_or_external_write",
    stopServices: [],
    removeComposeVolumes: false,
    removeRecoveryRoot: false
  });
});

test("recovery artifact path is deterministic beside the qualification receipt", () => {
  assert.equal(localJ1RecoveryArtifactPath("/tmp/opl-local-ready.json"), "/tmp/opl-local-ready.recovery.json");
  assert.throws(() => localJ1RecoveryArtifactPath("relative.json"), /absolute JSON path/);
});

test("owner-authoritative recovery readback is GET-only and scoped to the original operation", async () => {
  const calls = [];
  const authority = await collectLocalJ1RecoveryAuthority({
    http: { json: async (path, init, auth) => { calls.push({ path, init, auth }); return { payload: launchReadback("debit") }; } },
    auth: { cookie: "session", csrf: "csrf" },
    operationId,
    launchSubmitted: true
  });
  assert.equal(authority.operationIdDigest, recoveryIdentityDigest(operationId));
  assert.deepEqual(calls, [{ path: `/api/workspace-launches/${encodeURIComponent(operationId)}`, init: {}, auth: { cookie: "session", csrf: "csrf" } }]);
  assert.equal(await collectLocalJ1RecoveryAuthority({ http: {}, auth: null, operationId, launchSubmitted: false }), null);
});

test("recovery readback preserves budgets and counts without raw identities", () => {
  const summary = summarizeLocalJ1RecoveryReadback(launchReadback("debit", {
    key: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "raw-key-write" }),
    debit: attempt({ attempted: 1, unknown: 1, status: "unknown", idempotencyKey: "raw-debit-write", pendingReadbacks: 2 })
  }));
  assert.deepEqual(summary.externalWrites, {
    workspaceCreates: confirmedWrite,
    keyCreates: confirmedWrite,
    debits: unknownWrite,
    providerMutations: noWrite,
    purchaseReceipts: noWrite
  });
  assert.equal(summary.operationVersion, 7);
  assert.equal(summary.stage, "debit");
  assert.equal(summary.attempts.debit.pendingReadbacks, 2);
  assert.equal(summary.attempts.debit.maxPendingReadbacks, 3);
  assert.equal(summary.operationIdDigest, recoveryIdentityDigest(operationId));
  const serialized = JSON.stringify(summary);
  for (const forbidden of [operationId, workspaceId, "raw-key-write", "raw-debit-write"]) assert.equal(serialized.includes(forbidden), false);
});

test("recovery readback rejects missing, conflicting, or exhausted counter shapes", () => {
  const missing = launchReadback("key");
  delete missing.continuationAttemptBudgets.runtime;
  assert.throws(() => summarizeLocalJ1RecoveryReadback(missing), /missing runtime attempt/);
  assert.throws(() => summarizeLocalJ1RecoveryReadback(launchReadback("debit", {
    debit: attempt({ attempted: 2, max: 1, idempotencyKey: "invalid" })
  })), /counters are invalid/);
  assert.throws(() => summarizeLocalJ1RecoveryReadback(launchReadback("debit", {
    debit: attempt({ pendingReadbacks: 4, maxPendingReadbacks: 3 })
  })), /counters are invalid/);
  assert.throws(() => summarizeLocalJ1RecoveryReadback(launchReadback("debit", {
    debit: attempt({ attempted: 1, confirmed: 1, unknown: 1, idempotencyKey: "conflict" })
  })), /counters are invalid/);
});

test("0600 recovery artifact binds exact source, recovery refs, and redacted authority", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-j1-recovery-test-"));
  const path = join(root, "recovery.json");
  try {
    const authority = summarizeLocalJ1RecoveryReadback(launchReadback("runtime", {
      key: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "key-secret" }),
      debit: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "debit-secret" }),
      ensure_compute_allocation: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "compute-secret" }),
      storage: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "storage-secret" }),
      attachment: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "attachment-secret" }),
      secret: attempt({ attempted: 1, confirmed: 1, status: "confirmed", idempotencyKey: "fabric-secret" }),
      runtime: attempt({ attempted: 1, unknown: 1, status: "pending", idempotencyKey: "runtime-secret", pendingReadbacks: 1 })
    }));
    const artifact = createLocalJ1RecoveryArtifact({
      source,
      failure: { stage: "workspace_launch", errorCode: "local_workspace_qualification_failed" },
      authority,
      compose: {
        project: "opl-local-qualification-123",
        recoveryRoot: "/tmp/opl-local-qualification-123",
        fabricSecretRoot: "/tmp/opl-local-qualification-123/fabric-secrets",
        postgresVolumeRefs: ["opl-local-qualification-123_postgres-data"],
        providerVolumeRefs: ["opl-workspace-volume-123"]
      }
    });
    await writeLocalJ1RecoveryArtifact(path, artifact);
    assert.equal((await stat(path)).mode & 0o777, 0o600);
    const serialized = await readFile(path, "utf8");
    const parsed = JSON.parse(serialized);
    assert.equal(parsed.status, "RECOVERY_REQUIRED");
    assert.equal(parsed.cleanup.performed, false);
    assert.deepEqual(parsed.source, source);
    assert.deepEqual(parsed.authority.externalWrites.providerMutations, { attempted: 5, confirmed: 4, unknown: 1 });
    for (const forbidden of [operationId, workspaceId, "key-secret", "debit-secret", "runtime-secret", "password", "token"]) {
      assert.equal(serialized.includes(forbidden), false, forbidden);
    }
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("recovery artifact fails closed without owner-authoritative launch readback", () => {
  assert.throws(() => createLocalJ1RecoveryArtifact({
    source,
    failure: { stage: "workspace_launch", errorCode: "local_workspace_qualification_failed" },
    authority: null,
    compose: {
      project: "opl-local-qualification-123",
      recoveryRoot: "/tmp/opl-local-qualification-123",
      fabricSecretRoot: "/tmp/opl-local-qualification-123/fabric-secrets",
      postgresVolumeRefs: ["opl-local-qualification-123_opl-cloud-postgres"],
      providerVolumeRefs: []
    }
  }), /owner-authoritative launch readback/);
});

test("readback-unavailable recovery artifact still preserves exact refs and only identity digests", () => {
  const artifact = createLocalJ1RecoveryReadbackPendingArtifact({
    source,
    failure: { stage: "workspace_launch", errorCode: "local_workspace_qualification_failed" },
    operationId,
    workspaceId,
    compose: {
      project: "opl-local-qualification-123",
      recoveryRoot: "/tmp/opl-local-qualification-123",
      fabricSecretRoot: "/tmp/opl-local-qualification-123/fabric-secrets",
      postgresVolumeRefs: ["opl-local-qualification-123_opl-cloud-postgres"],
      providerVolumeRefs: []
    }
  });
  assert.equal(artifact.status, "RECOVERY_READBACK_REQUIRED");
  assert.deepEqual(artifact.authority.externalWrites, {
    workspaceCreates: { attempted: null, confirmed: null, unknown: null },
    keyCreates: { attempted: null, confirmed: null, unknown: null },
    debits: { attempted: null, confirmed: null, unknown: null },
    providerMutations: { attempted: null, confirmed: null, unknown: null },
    purchaseReceipts: { attempted: null, confirmed: null, unknown: null }
  });
  assert.equal(artifact.cleanup.performed, false);
  assert.equal(JSON.stringify(artifact).includes(operationId), false);
  assert.equal(JSON.stringify(artifact).includes(workspaceId), false);
});
