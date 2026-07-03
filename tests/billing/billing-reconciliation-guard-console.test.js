import assert from "node:assert/strict";
import test from "node:test";

import {
  createCurrentTestService,
  provisionWorkspace
} from "../helpers/current-resource-chain.js";

test("Console blocks new resource provisioning while billing reconciliation guard is active", async () => {
  const service = createCurrentTestService();
  await service.manualTopUp({ accountId: "pi-alpha", amount: 300, reason: "owner_credit" });

  const failedReport = await service.recordBillingReconciliation({
    report: {
      ok: false,
      generatedAt: "2026-07-02T00:00:00.000Z",
      mismatches: [{ workspaceId: "ws-alpha", serverDelta: -1.5, storageDelta: 0 }]
    }
  });

  assert.equal(failedReport.guard.blockNewWorkspaces, true);
  await assert.rejects(
    service.createComputeResource({
      accountId: "pi-alpha",
      userId: "usr-alpha",
      name: "Blocked node",
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

  const { workspace } = await provisionWorkspace(service, {
    accountId: "pi-alpha",
    userId: "usr-alpha",
    workspaceName: "Unblocked Lab",
    packageId: "basic"
  });
  assert.equal(workspace.state, "running");
});
