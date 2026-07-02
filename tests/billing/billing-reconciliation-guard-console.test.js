import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2,
  markup: 0.2
};

function runtimeFixture({ workspaceId, workspaceName, packagePlan, token }) {
  return {
    provider: "test-provider",
    server: { id: `server-${workspaceId}`, status: "running", billingStatus: "active", spec: packagePlan.server },
    docker: { id: `docker-${workspaceId}`, image: "test-image", status: "running" },
    disk: { id: `disk-${workspaceId}`, status: "attached_retained", billingStatus: "active", sizeGb: packagePlan.diskGb, mountPath: "/data" },
    url: `http://127.0.0.1:8787/workspaces/${workspaceName}?token=${token}`,
    slug: workspaceName
  };
}

function createService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: {
      name: "test-provider",
      async createWorkspaceRuntime(input) {
        return runtimeFixture(input);
      }
    },
    pricing: TEST_PRICING
  });
}

test("Console blocks new Workspace provisioning while billing reconciliation guard is active", async () => {
  const service = createService();
  await service.creditAccount({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });

  const failedReport = await service.recordBillingReconciliation({
    report: {
      ok: false,
      generatedAt: "2026-07-02T00:00:00.000Z",
      mismatches: [{ workspaceId: "ws-alpha", serverDelta: -1.5, storageDelta: 0 }]
    }
  });

  assert.equal(failedReport.guard.blockNewWorkspaces, true);
  await assert.rejects(
    service.createWorkspace({
      accountId: "pi-alpha",
      workspaceName: "Blocked Lab",
      packageId: "basic"
    }),
    /billing_reconciliation_guard_blocked:tencent_bill_reconciliation_failed/
  );

  const summary = await service.operatorSummary({ accountId: "pi-alpha" });
  assert.equal(summary.billingReconciliation.guard.blockNewWorkspaces, true);
  assert.equal(summary.notifications.error, 1);
  assert.equal(summary.notifications.recent[0].type, "billing.reconciliation_guard_blocked");

  const okReport = await service.recordBillingReconciliation({
    report: {
      ok: true,
      generatedAt: "2026-07-02T01:00:00.000Z",
      mismatches: []
    }
  });
  assert.equal(okReport.guard.blockNewWorkspaces, false);

  const workspace = await service.createWorkspace({
    accountId: "pi-alpha",
    workspaceName: "Unblocked Lab",
    packageId: "basic"
  });
  assert.equal(workspace.state, "running");
});
