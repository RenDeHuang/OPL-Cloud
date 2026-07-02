import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  computeHourly: { basic: 1, pro: 4 },
  storageGbMonth: 0.2,
  markup: 0.2
};

function runtimeFixture({ workspaceId, workspaceName, packagePlan, token }) {
  return {
    provider: "test-provider",
    server: {
      id: `server-${workspaceId}`,
      status: "running",
      billingStatus: "active",
      spec: packagePlan.server
    },
    docker: {
      id: `docker-${workspaceId}`,
      image: "test-image",
      status: "running"
    },
    disk: {
      id: `disk-${workspaceId}`,
      status: "attached_retained",
      billingStatus: "active",
      sizeGb: packagePlan.diskGb,
      mountPath: "/data"
    },
    url: `https://workspace.example.com/w/${workspaceName}?token=${token}`,
    slug: workspaceName
  };
}

function createTestService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: {
      name: "test-provider",
      workspaceUrl({ slug, token }) {
        return `https://workspace.example.com/w/${slug}?token=${token}`;
      },
      async createWorkspaceRuntime(input) {
        return runtimeFixture(input);
      }
    },
    pricing: TEST_PRICING
  });
}

test("Workspace access uses a long-lived URL token that can be deleted and reset after leakage", async () => {
  const service = createTestService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });
  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Token Lab",
    packageId: "basic"
  });

  assert.equal(workspace.access.mode, "long_lived_url_token");
  assert.equal(workspace.access.requiresLogin, false);
  assert.equal(workspace.access.tokenStatus, "active");
  assert.equal(workspace.access.rotationPolicy, "reset_or_delete_on_leak");

  const resolved = await service.resolveWorkspaceAccess({
    slug: workspace.slug,
    token: workspace.access.token
  });
  assert.equal(resolved.id, workspace.id);

  const deleted = await service.deleteWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(deleted.access.tokenStatus, "deleted");
  await assert.rejects(
    service.resolveWorkspaceAccess({ slug: workspace.slug, token: workspace.access.token }),
    /workspace_token_inactive/
  );

  const reset = await service.resetWorkspaceToken({
    accountId: "pi-alpha",
    workspaceId: workspace.id
  });
  assert.equal(reset.access.tokenStatus, "active");
  assert.notEqual(reset.access.token, workspace.access.token);
  assert.match(reset.url, new RegExp(`token=${reset.access.token}$`));

  const resolvedAfterReset = await service.resolveWorkspaceAccess({
    slug: reset.slug,
    token: reset.access.token
  });
  assert.equal(resolvedAfterReset.id, workspace.id);

  const state = await service.getState("pi-alpha");
  assert.deepEqual(state.billingLedger.map((entry) => entry.type).filter((type) => type.startsWith("token_")), [
    "token_deleted",
    "token_reset"
  ]);
});
