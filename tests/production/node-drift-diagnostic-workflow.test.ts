import assert from "node:assert/strict";
import test from "node:test";

import * as diagnostic from "../../tools/production-node-drift-diagnostic.ts";

test("original Launch owner remains the identity authority", () => {
  assert.equal(typeof diagnostic.assertOriginalLaunchOwner, "function");
  assert.throws(() => diagnostic.assertOriginalLaunchOwner({}, {}), /node_drift_original_launch_owner_mismatch/);
});

test("approved customer identity is enforced by normalized email digest", () => {
  assert.equal(typeof diagnostic.assertApprovedCustomerEmailDigests, "function");
  const approved = "sha256:d241839999cab1dbb0fc96c4dda28f9433ccfa68e12246e1b2ed0726d19ec376";
  assert.doesNotThrow(() => diagnostic.assertApprovedCustomerEmailDigests([approved, approved]));
  assert.throws(() => diagnostic.assertApprovedCustomerEmailDigests([]), /node_drift_approved_customer_identity_mismatch/);
});

test("Fabric operation readback follows cursor pages and rejects a repeated cursor", async () => {
  const paths: string[] = [];
  const operations = await diagnostic.readFabricOperationPages(async (path) => {
    paths.push(path);
    return path.includes("cursor=page-2")
      ? { operations: [{ id: "operation-2" }] }
      : { operations: [{ id: "operation-1" }], nextCursor: "page-2" };
  });
  assert.deepEqual(operations.map((operation) => operation.id), ["operation-1", "operation-2"]);
  assert.deepEqual(paths, ["/fabric/operations?limit=100", "/fabric/operations?limit=100&cursor=page-2"]);

  await assert.rejects(
    diagnostic.readFabricOperationPages(async () => ({ operations: [], nextCursor: "same-cursor" })),
    /node_drift_fabric_operations_cursor_repeated/
  );
});
