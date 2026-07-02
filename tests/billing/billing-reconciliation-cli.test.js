import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { runReconciliationCli } from "../../tools/reconcile-tencent-bills.js";

test("Tencent reconciliation CLI writes JSON to stdout and returns non-zero on mismatches", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-reconcile-"));
  const ledgerPath = join(root, "ledger.json");
  const tencentPath = join(root, "tencent.json");
  try {
    await writeFile(ledgerPath, JSON.stringify([
      { workspaceId: "ws-alpha", type: "compute_debit", amount: -10.5, currency: "CNY" }
    ]));
    await writeFile(tencentPath, JSON.stringify([
      { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY" }
    ]));
    let stdout = "";
    let stderr = "";

    const code = await runReconciliationCli({
      argv: ["--ledger", ledgerPath, "--tencent", tencentPath],
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } }
    });

    const report = JSON.parse(stdout);
    assert.equal(code, 1);
    assert.equal(report.ok, false);
    assert.equal(report.guard.blockNewWorkspaces, true);
    assert.equal(report.mismatches[0].serverDelta, -1.5);
    assert.equal(stderr, "tencent_bill_reconciliation_failed\n");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Tencent reconciliation CLI can normalize raw Tencent export rows before reconciling", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-reconcile-"));
  const ledgerPath = join(root, "ledger.json");
  const tencentPath = join(root, "tencent-raw.json");
  try {
    await writeFile(ledgerPath, JSON.stringify([
      { workspaceId: "ws-alpha", type: "compute_debit", amount: -12, currency: "CNY" },
      { workspaceId: "ws-alpha", type: "storage_debit", amount: -2.4, currency: "CNY" }
    ]));
    await writeFile(tencentPath, JSON.stringify({
      rows: [
        { ProductName: "Tencent Kubernetes Engine", RealTotalCost: "10", Currency: "CNY", Tags: "workspace_id:ws-alpha", ResourceId: "pod-alpha" },
        { ProductName: "Cloud Block Storage", RealTotalCost: "2", Currency: "CNY", Tags: "workspace_id:ws-alpha", ResourceId: "disk-alpha" }
      ]
    }));
    let stdout = "";
    let stderr = "";

    const code = await runReconciliationCli({
      argv: ["--ledger", ledgerPath, "--tencent", tencentPath, "--tencent-format", "raw"],
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } }
    });

    const report = JSON.parse(stdout);
    assert.equal(code, 0);
    assert.equal(report.ok, true);
    assert.equal(report.guard.blockNewWorkspaces, false);
    assert.deepEqual(report.totals, {
      ledgerServer: 12,
      ledgerStorage: 2.4,
      tencentServer: 10,
      tencentStorage: 2,
      expectedServer: 12,
      expectedStorage: 2.4,
      serverDelta: 0,
      storageDelta: 0
    });
    assert.equal(stderr, "");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Tencent reconciliation CLI can read the OPL ledger from a deployed Console state endpoint", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-reconcile-"));
  const tencentPath = join(root, "tencent.json");
  try {
    await writeFile(tencentPath, JSON.stringify([
      { workspaceId: "ws-alpha", resourceType: "server", amount: 10, currency: "CNY" },
      { workspaceId: "ws-alpha", resourceType: "storage", amount: 2, currency: "CNY" }
    ]));
    let stdout = "";
    let stderr = "";
    const requestedUrls = [];

    const code = await runReconciliationCli({
      argv: [
        "--console-origin",
        "https://console.oplcloud.example",
        "--account",
        "pi-alpha",
        "--tencent",
        tencentPath
      ],
      stdout: { write: (chunk) => { stdout += chunk; } },
      stderr: { write: (chunk) => { stderr += chunk; } },
      fetchImpl: async (url) => {
        requestedUrls.push(String(url));
        return {
          ok: true,
          status: 200,
          json: async () => ({
            billingLedger: [
              { workspaceId: "ws-alpha", type: "compute_debit", amount: -12, currency: "CNY" },
              { workspaceId: "ws-alpha", type: "storage_debit", amount: -2.4, currency: "CNY" }
            ]
          })
        };
      }
    });

    const report = JSON.parse(stdout);
    assert.equal(code, 0);
    assert.equal(report.ok, true);
    assert.deepEqual(requestedUrls, ["https://console.oplcloud.example/api/state?accountId=pi-alpha"]);
    assert.equal(stderr, "");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("Tencent reconciliation CLI fails closed when no ledger source is provided", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-reconcile-"));
  const tencentPath = join(root, "tencent.json");
  try {
    await writeFile(tencentPath, JSON.stringify([]));
    await assert.rejects(
      () => runReconciliationCli({
        argv: ["--tencent", tencentPath],
        stdout: { write: () => {} },
        stderr: { write: () => {} }
      }),
      /ledger_or_console_origin_required/
    );
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
