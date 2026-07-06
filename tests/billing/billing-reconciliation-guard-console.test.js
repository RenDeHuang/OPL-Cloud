import assert from "node:assert/strict";
import test from "node:test";

import { createOplCloud } from "../../packages/console/src/opl-cloud.js";
import { MemoryStore } from "../../packages/console/src/store.js";
import { createFakeRuntimeProvider } from "../helpers/fake-runtime-provider.js";

const TEST_PRICING = {
  serverHourly: {
    basic: 1,
    pro: 4
  },
  diskGbMonth: 0.2
};

function createService() {
  return createOplCloud({
    store: new MemoryStore(),
    runtimeProvider: createFakeRuntimeProvider({
      name: "test-provider",
      workspaceUrl({ workspaceId, token }) {
        return `http://127.0.0.1:8787/workspaces/${workspaceId}?token=${token}`;
      }
    }),
    pricing: TEST_PRICING
  });
}

async function createWorkspaceEntry(service, { accountId, workspaceName, packageId = "basic" }) {
  const storage = await service.createStorageVolume({ accountId, packageId, name: `${workspaceName} storage` });
  const compute = await service.createComputeAllocation({ accountId, packageId, name: `${workspaceName} compute` });
  await service.processPendingResourceProvisioning({ limit: 1 });
  const attachment = await service.attachStorage({
    accountId,
    computeAllocationId: compute.id,
    storageId: storage.id,
    mountPath: "/data"
  });
  return service.createWorkspace({ accountId, workspaceName, attachmentId: attachment.id });
}

test("Console blocks new resource provisioning while billing reconciliation guard is active", async () => {
  const service = createService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 250, reason: "owner_credit" });

  const failedReport = await service.recordBillingReconciliation({
    report: {
      ok: false,
      generatedAt: "2026-07-02T00:00:00.000Z",
      mismatches: [{ workspaceId: "ws-alpha", serverDelta: 0.5, storageDelta: 0 }]
    }
  });

  assert.equal(failedReport.guard.blockNewWorkspaces, true);
  await assert.rejects(
    service.createComputeAllocation({
      accountId: "pi-alpha",
      packageId: "basic",
      name: "Blocked compute"
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

  const workspace = await createWorkspaceEntry(service, {
    accountId: "pi-alpha",
    workspaceName: "Unblocked Lab"
  });
  assert.equal(workspace.state, "running");
});
