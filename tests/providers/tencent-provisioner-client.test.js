import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { TencentProvisionerClient } from "../../packages/fabric/src/tencent-provisioner-client.js";

async function createFixtureProvisioner(root, responseFactorySource) {
  const scriptPath = join(root, "fixture-provisioner.mjs");
  await writeFile(scriptPath, `
    import { readFileSync } from "node:fs";
    const input = JSON.parse(readFileSync(0, "utf8"));
    const responseFactory = ${responseFactorySource};
    process.stdout.write(JSON.stringify(responseFactory(input)) + "\\n");
  `);
  return scriptPath;
}

test("TencentProvisionerClient invokes JSON stdin/stdout provisioner", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-provisioner-client-"));
  try {
    const scriptPath = await createFixtureProvisioner(root, `(input) => ({
      ok: true,
      operationId: "op-test",
      poolId: input.pool.id,
      nodePoolId: input.pool.nodePoolId,
      instanceId: "ins-test",
      nodeName: "node-test",
      status: "provisioning",
      providerData: {
        action: input.action,
        dryRun: String(input.dryRun),
        accountId: input.accountId
      }
    })`);
    const client = new TencentProvisionerClient({
      binPath: process.execPath,
      args: [scriptPath],
      env: { TENCENTCLOUD_REGION: "ap-guangzhou" },
      dryRun: true
    });

    const result = await client.createComputeAllocation({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      packageId: "basic",
      pool: { id: "pool-basic-2c4g", nodePoolId: "np-basic", instanceType: "SA5.LARGE4" },
      allocation: { id: "compute-alpha" }
    });

    assert.equal(result.operationId, "op-test");
    assert.equal(result.instanceId, "ins-test");
    assert.equal(result.providerData.action, "create_compute_allocation");
    assert.equal(result.providerData.dryRun, "true");
    assert.equal(result.providerData.accountId, "pi-alpha");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("TencentProvisionerClient maps provisioner failures to safe errors", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-provisioner-client-failure-"));
  try {
    const scriptPath = await createFixtureProvisioner(root, `() => ({
      ok: false,
      errorCode: "tencent_permission_denied",
      message: "CAM denied CreateClusterInstances",
      providerRequestId: "req-denied",
      retryable: false,
      providerData: { action: "create_compute_allocation" }
    })`);
    const client = new TencentProvisionerClient({
      binPath: process.execPath,
      args: [scriptPath],
      env: { TENCENTCLOUD_REGION: "ap-guangzhou" }
    });

    await assert.rejects(
      client.createComputeAllocation({
        accountId: "pi-alpha",
        userId: "usr-alpha",
        packageId: "basic",
        pool: { id: "pool-basic-2c4g", nodePoolId: "np-basic", instanceType: "SA5.LARGE4" },
        allocation: { id: "compute-alpha" }
      }),
      (error) => {
        assert.equal(error.message, "tencent_permission_denied");
        assert.equal(error.safeMessage, "CAM denied CreateClusterInstances");
        assert.equal(error.providerRequestId, "req-denied");
        assert.equal(error.retryable, false);
        assert.deepEqual(error.providerData, { action: "create_compute_allocation" });
        return true;
      }
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
