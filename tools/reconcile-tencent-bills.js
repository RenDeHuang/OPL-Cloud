import { readFile } from "node:fs/promises";

import { reconcileTencentBills } from "../services/api/src/billing-reconciliation.js";

function cliArgs(argv) {
  const args = {};
  for (let index = 0; index < argv.length; index += 1) {
    const item = argv[index];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const value = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
    args[key] = value;
  }
  return args;
}

async function readJsonFile(path, label) {
  if (!path) throw new Error(`${label}_path_required`);
  return JSON.parse(await readFile(path, "utf8"));
}

export async function runReconciliationCli({
  argv = process.argv.slice(2),
  stdout = process.stdout,
  stderr = process.stderr
} = {}) {
  const args = cliArgs(argv);
  const ledgerInput = await readJsonFile(args.ledger, "ledger");
  const tencentInput = await readJsonFile(args.tencent, "tencent");
  const ledgerEntries = Array.isArray(ledgerInput) ? ledgerInput : ledgerInput.billingLedger;
  const tencentBills = Array.isArray(tencentInput) ? tencentInput : tencentInput.tencentBills;
  const report = reconcileTencentBills({
    ledgerEntries,
    tencentBills,
    markup: args.markup === undefined ? 0.1 : Number(args.markup),
    tolerance: args.tolerance === undefined ? 0.01 : Number(args.tolerance)
  });

  stdout.write(`${JSON.stringify(report, null, 2)}\n`);
  if (!report.ok) {
    stderr.write("tencent_bill_reconciliation_failed\n");
    return 1;
  }
  return 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  runReconciliationCli().then((code) => {
    process.exitCode = code;
  }).catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
