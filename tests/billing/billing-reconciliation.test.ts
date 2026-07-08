import assert from "node:assert/strict";
import test from "node:test";

import { normalizeTencentBillRows, reconcileTencentBills } from "../../services/ledger/ops/reconcile-tencent-bills.ts";

test("reconciles OPL resource debits against internal provider cost evidence", () => {
  const report = reconcileTencentBills({
    tolerance: 0.01,
    ledgerEntries: [
      { workspaceId: "ws-alpha", type: "compute_debit", amount: -10, currency: "CNY" },
      { workspaceId: "ws-alpha", type: "storage_debit", amount: -2, currency: "CNY" },
      { workspaceId: "ws-alpha", type: "credit", amount: 50, currency: "CNY" },
      { workspaceId: "ws-beta", type: "compute_debit", amount: -20, currency: "CNY" },
      { workspaceId: "ws-beta", type: "storage_debit", amount: -4, currency: "CNY" }
    ],
    tencentBills: [
      { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY" },
      { workspaceId: "ws-alpha", resourceType: "storage", amount: 2, currency: "CNY" },
      { workspaceId: "ws-beta", resourceType: "server", amount: 20, currency: "CNY" },
      { workspaceId: "ws-beta", resourceType: "storage", amount: 4, currency: "CNY" }
    ]
  });

  assert.equal(report.ok, true);
  assert.equal(report.currency, "CNY");
  assert.deepEqual(report.totals, {
    ledgerServer: 30,
    ledgerStorage: 6,
    tencentServer: 30,
    tencentStorage: 6,
    expectedServer: 30,
    expectedStorage: 6,
    serverDelta: 0,
    storageDelta: 0
  });
  assert.deepEqual(report.workspaces.map((workspace) => ({
    workspaceId: workspace.workspaceId,
    ok: workspace.ok,
    serverDelta: workspace.serverDelta,
    storageDelta: workspace.storageDelta
  })), [
    { workspaceId: "ws-alpha", ok: true, serverDelta: 0, storageDelta: 0 },
    { workspaceId: "ws-beta", ok: true, serverDelta: 0, storageDelta: 0 }
  ]);
});

test("reports mismatches without treating credits or lifecycle entries as billable resource settlements", () => {
  const report = reconcileTencentBills({
    tolerance: 0.01,
    ledgerEntries: [
      { workspaceId: "ws-alpha", type: "compute_debit", amount: -10.5, currency: "CNY" },
      { workspaceId: "ws-alpha", type: "storage_debit", amount: -2.4, currency: "CNY" },
      { workspaceId: "ws-alpha", type: "server_billing_stopped", amount: 0, currency: "CNY" },
      { workspaceId: "account", type: "credit", amount: 100, currency: "CNY" }
    ],
    tencentBills: [
      { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY" },
      { workspaceId: "ws-alpha", resourceType: "storage", amount: 2, currency: "CNY" }
    ]
  });

  assert.equal(report.ok, false);
  assert.equal(report.totals.serverDelta, 0.5);
  assert.deepEqual(report.mismatches, [
    {
      workspaceId: "ws-alpha",
      serverDelta: 0.5,
      storageDelta: 0.4,
      ledgerServer: 10.5,
      expectedServer: 10,
      ledgerStorage: 2.4,
      expectedStorage: 2
    }
  ]);
});

test("fails closed on mixed currencies", () => {
  assert.throws(
    () => reconcileTencentBills({
      ledgerEntries: [{ workspaceId: "ws-alpha", type: "compute_debit", amount: -11, currency: "CNY" }],
      tencentBills: [{ workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "USD" }]
    }),
    /mixed_currency_not_supported/
  );
});

test("normalizes Tencent billing export rows into Workspace resource bills", () => {
  const rows = normalizeTencentBillRows([
    {
      ProductName: "Tencent Kubernetes Engine",
      Cost: "10.00",
      Currency: "CNY",
      Tags: "product:opl-cloud,opl_account_id:acct-alpha,opl_workspace_id:ws-alpha,opl_resource_id:ca-alpha,opl_operation_id:op-alpha",
      ResourceId: "pod-alpha"
    },
    {
      ProductName: "Cloud Block Storage",
      RealTotalCost: "2.00",
      Currency: "CNY",
      Tag: { product: "opl-cloud", workspace_id: "ws-alpha" },
      ResourceId: "disk-alpha"
    },
    {
      productName: "DNSPod",
      realTotalCost: "99",
      currency: "CNY",
      tags: "product:opl-cloud,workspace_id:ws-alpha"
    }
  ]);

  assert.deepEqual(rows, [
    { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY", sourceResourceId: "ca-alpha", accountId: "acct-alpha", resourceId: "ca-alpha", operationId: "op-alpha" },
    { workspaceId: "ws-alpha", resourceType: "storage", amount: 2, currency: "CNY", sourceResourceId: "disk-alpha" }
  ]);
});

test("normalizing Tencent billing rows fails closed when Workspace identity is missing", () => {
  assert.throws(
    () => normalizeTencentBillRows([
      { ProductName: "Tencent Kubernetes Engine", Cost: "10.00", Currency: "CNY", ResourceId: "pod-alpha" }
    ]),
    /tencent_bill_workspace_id_missing:pod-alpha/
  );
});
