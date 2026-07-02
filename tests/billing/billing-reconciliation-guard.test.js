import assert from "node:assert/strict";
import test from "node:test";

import {
  billingReconciliationGuard,
  createBillingReconciliationReport
} from "../../packages/ledger/src/billing-reconciliation.js";

test("billing reconciliation report blocks new provisioning when OPL debits do not cover Tencent cost plus markup", () => {
  const report = createBillingReconciliationReport({
    generatedAt: "2026-07-02T00:00:00.000Z",
    ledgerEntries: [
      { workspaceId: "ws-alpha", type: "compute_debit", amount: -10.5, currency: "CNY" }
    ],
    tencentBills: [
      { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY" }
    ],
    markup: 0.2
  });

  assert.equal(report.ok, false);
  assert.equal(report.guard.blockNewWorkspaces, true);
  assert.equal(report.guard.reason, "tencent_bill_reconciliation_failed");
  assert.deepEqual(report.guard.mismatchWorkspaceIds, ["ws-alpha"]);
});

test("billing reconciliation guard fails closed when the latest report is missing or stale", () => {
  assert.deepEqual(
    billingReconciliationGuard({
      latestReport: null,
      now: "2026-07-02T12:00:00.000Z",
      requireRecentReport: true
    }),
    {
      status: "blocked",
      blockNewWorkspaces: true,
      reason: "billing_reconciliation_report_missing",
      checkedAt: "2026-07-02T12:00:00.000Z"
    }
  );

  const stale = billingReconciliationGuard({
    latestReport: {
      ok: true,
      generatedAt: "2026-07-01T00:00:00.000Z",
      mismatches: []
    },
    now: "2026-07-02T12:00:00.000Z",
    maxAgeHours: 24,
    requireRecentReport: true
  });

  assert.equal(stale.status, "blocked");
  assert.equal(stale.blockNewWorkspaces, true);
  assert.equal(stale.reason, "billing_reconciliation_report_stale");
  assert.equal(stale.ageHours, 36);
});
