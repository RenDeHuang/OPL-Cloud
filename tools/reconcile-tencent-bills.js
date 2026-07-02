import { readFile } from "node:fs/promises";

import { normalizeTencentBillRows, reconcileTencentBills } from "../packages/ledger/src/billing-reconciliation.js";

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

async function readConsoleLedger({ consoleOrigin, account, fetchImpl }) {
  if (!account) throw new Error("account_required");
  const fetcher = fetchImpl || globalThis.fetch;
  if (!fetcher) throw new Error("fetch_unavailable");

  const url = new URL("/api/state", consoleOrigin);
  url.searchParams.set("accountId", account);
  const response = await fetcher(url.toString());
  if (!response.ok) throw new Error(`console_state_fetch_failed:${response.status}`);

  const payload = await response.json();
  if (!Array.isArray(payload.billingLedger)) throw new Error("console_state_billing_ledger_missing");
  return payload.billingLedger;
}

async function readLedgerEntries({ args, fetchImpl }) {
  if (args.ledger) {
    const ledgerInput = await readJsonFile(args.ledger, "ledger");
    return Array.isArray(ledgerInput) ? ledgerInput : ledgerInput.billingLedger;
  }

  if (args["console-origin"]) {
    return readConsoleLedger({
      consoleOrigin: args["console-origin"],
      account: args.account,
      fetchImpl
    });
  }

  throw new Error("ledger_or_console_origin_required");
}

export async function runReconciliationCli({
  argv = process.argv.slice(2),
  stdout = process.stdout,
  stderr = process.stderr,
  fetchImpl
} = {}) {
  const args = cliArgs(argv);
  const ledgerEntries = await readLedgerEntries({ args, fetchImpl });
  const tencentInput = await readJsonFile(args.tencent, "tencent");
  const rawTencentRows = Array.isArray(tencentInput) ? tencentInput : tencentInput.tencentBills || tencentInput.rows;
  const tencentBills = args["tencent-format"] === "raw"
    ? normalizeTencentBillRows(rawTencentRows)
    : rawTencentRows;
  const report = reconcileTencentBills({
    ledgerEntries,
    tencentBills,
    markup: args.markup === undefined ? 0.2 : Number(args.markup),
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
