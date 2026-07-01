import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { runReconciliationCli } from "../tools/reconcile-tencent-bills.js";

test("Tencent reconciliation CLI writes JSON to stdout and returns non-zero on mismatches", async () => {
  const root = await mkdtemp(join(tmpdir(), "opl-cloud-reconcile-"));
  const ledgerPath = join(root, "ledger.json");
  const tencentPath = join(root, "tencent.json");
  try {
    await writeFile(ledgerPath, JSON.stringify([
      { workspaceId: "ws-alpha", type: "server_debit", amount: -10.5, currency: "CNY" }
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
    assert.equal(report.mismatches[0].serverDelta, -0.5);
    assert.equal(stderr, "tencent_bill_reconciliation_failed\n");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
