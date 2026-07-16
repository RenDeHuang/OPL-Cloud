import assert from "node:assert/strict";
import test from "node:test";

import { attachStorage, createComputeAllocation, createStorageVolume, reactivateStorageVolume, setResourceAutoRenew } from "../../apps/console-ui/src/api/resources-api.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

test("paid resource retries send the caller's stable idempotency key", async () => {
  const originalFetch = globalThis.fetch;
  const requests: RequestInit[] = [];
  globalThis.fetch = async (_input, init = {}) => {
    requests.push(init);
    return new Response(JSON.stringify({ id: "resource-alpha", status: "submitted" }), {
      status: 202,
      headers: { "content-type": "application/json" }
    });
  };
  try {
    await createComputeAllocation({ packageId: "basic" }, "csrf-alpha", "purchase-once");
    await createComputeAllocation({ packageId: "basic" }, "csrf-alpha", "purchase-once");
    await createStorageVolume({ packageId: "basic", sizeGb: 10 }, "csrf-alpha", "storage-once");
    await reactivateStorageVolume({ id: "storage-retained", packageId: "basic", sizeGb: 10 }, "csrf-alpha", "reactivate-once");
    await setResourceAutoRenew({ resourceId: "resource-alpha", autoRenew: false }, "csrf-alpha", "renew-setting-once");
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(requests.map((request) => new Headers(request.headers).get("Idempotency-Key")), [
    "purchase-once",
    "purchase-once",
    "storage-once",
    "reactivate-once",
    "renew-setting-once"
  ]);
  assert.deepEqual(JSON.parse(String(requests[3].body)), {
    id: "storage-retained",
    packageId: "basic",
    sizeGb: 10
  });
});

test("Workspace create retries reconcile one intent with the same request and key", async () => {
  assert.equal(typeof workspaceApi.createWorkspaceIntent, "function");

  const originalFetch = globalThis.fetch;
  const requests: RequestInit[] = [];
  let attempt = 0;
  globalThis.fetch = async (_input, init = {}) => {
    requests.push(init);
    attempt += 1;
    if (attempt === 1) throw new DOMException("timed out", "TimeoutError");
    return new Response(JSON.stringify({ id: "workspace-alpha", status: "submitted" }), {
      status: 201,
      headers: { "content-type": "application/json" }
    });
  };

  try {
    const input = { workspaceName: "Alpha", attachmentId: "attachment-alpha" };
    const intent = workspaceApi.createWorkspaceIntent(input);
    let error: any;
    try {
      await workspaceApi.createWorkspace(intent, "csrf-alpha");
    } catch (caught) {
      error = caught;
    }
    assert.equal(error?.payload?.status, "unknown");
    assert.equal(error?.payload?.retryable, true);

    const retried = workspaceApi.createWorkspaceIntent({ ...input }, intent);
    assert.strictEqual(retried, intent);
    const result = await workspaceApi.createWorkspace(retried, "csrf-alpha");
    assert.equal(result.resourceId, "workspace-alpha");

    const keys = requests.map((request) => new Headers(request.headers).get("Idempotency-Key"));
    assert.ok(keys[0]);
    assert.deepEqual(keys, [keys[0], keys[0]]);
    assert.deepEqual(requests.map((request) => JSON.parse(String(request.body))), [input, input]);

    const changedDuringReconciliation = workspaceApi.createWorkspaceIntent({ ...input, workspaceName: "Beta" }, intent);
    assert.strictEqual(changedDuringReconciliation, intent);
    const nextIntent = workspaceApi.createWorkspaceIntent({ ...input, workspaceName: "Beta" });
    assert.notStrictEqual(nextIntent, intent);
    assert.notEqual(nextIntent.idempotencyKey, intent.idempotencyKey);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace launch retries every existing mutation with one stable intent", async () => {
  assert.equal(typeof workspaceApi.createWorkspaceLaunchIntent, "function");

  const input = { workspaceName: "Alpha", packageId: "basic", sizeGb: 10 };
  const intent = workspaceApi.createWorkspaceLaunchIntent(input);
  const retried = workspaceApi.createWorkspaceLaunchIntent({ ...input }, intent);

  assert.strictEqual(retried, intent);
  assert.deepEqual(Object.keys(intent.idempotencyKeys).sort(), ["attachment", "compute", "storage", "workspace"]);
  assert.equal(new Set(Object.values(intent.idempotencyKeys)).size, 4);
  assert.ok(Object.values(intent.idempotencyKeys).every((key) => String(key).startsWith(`workspace-launch:${intent.id}:`)));
});

test("Workspace launch mutations mark transport failures unknown and keep caller keys", async () => {
  const originalFetch = globalThis.fetch;
  const requests: RequestInit[] = [];
  globalThis.fetch = async (_input, init = {}) => {
    requests.push(init);
    throw new DOMException("timed out", "TimeoutError");
  };

  try {
    for (const action of [
      () => createComputeAllocation({ packageId: "basic" }, "csrf-alpha", "compute-once"),
      () => createStorageVolume({ packageId: "basic", sizeGb: 10, computeAllocationId: "compute-alpha" }, "csrf-alpha", "storage-once"),
      () => attachStorage({ computeAllocationId: "compute-alpha", storageId: "storage-alpha" }, "csrf-alpha", "attachment-once")
    ]) {
      await assert.rejects(action, (error: any) => error?.payload?.status === "unknown" && error?.payload?.retryable === true);
    }
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(requests.map((request) => new Headers(request.headers).get("Idempotency-Key")), [
    "compute-once",
    "storage-once",
    "attachment-once"
  ]);
});

test("Workspace Gateway Secret rotation returns metadata only", async () => {
  const originalFetch = globalThis.fetch;
  let requestUrl = "";
  let request: RequestInit = {};
  globalThis.fetch = async (input, init = {}) => {
    requestUrl = String(input);
    request = init;
    return new Response(JSON.stringify({
      operationId: "rotate-alpha",
      workspaceId: "workspace-alpha",
      status: "succeeded",
      secretRef: "opl-gateway-acct-alpha",
      fingerprint: "sha256:fingerprint",
      value: "raw-key-must-not-cross-the-client-boundary"
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  try {
    const result = await workspaceApi.rotateWorkspaceGatewaySecret(
      { workspaceId: "workspace-alpha", reason: "owner-request" },
      "csrf-alpha",
      "rotate-once"
    );
    assert.deepEqual(result, { status: "succeeded", fingerprint: "sha256:fingerprint" });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.equal(requestUrl, "/api/workspaces/workspace-alpha/gateway-secret/rotate");
  assert.equal(new Headers(request.headers).get("Idempotency-Key"), "rotate-once");
});
