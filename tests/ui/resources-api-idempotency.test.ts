import assert from "node:assert/strict";
import test from "node:test";

import { createComputeAllocation, createStorageVolume, reactivateStorageVolume, setResourceAutoRenew } from "../../apps/console-ui/src/api/resources-api.ts";

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
