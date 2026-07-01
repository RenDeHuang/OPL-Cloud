function money(value) {
  return Number(Number(value).toFixed(4));
}

function absDebit(entry) {
  return money(Math.abs(Number(entry.amount || 0)));
}

function assertSingleCurrency(items) {
  const currencies = new Set(items.map((item) => item.currency || "CNY"));
  if (currencies.size > 1) throw new Error("mixed_currency_not_supported");
  return currencies.values().next().value || "CNY";
}

function ensureWorkspace(workspaces, workspaceId) {
  if (!workspaces.has(workspaceId)) {
    workspaces.set(workspaceId, {
      workspaceId,
      ledgerServer: 0,
      ledgerStorage: 0,
      tencentServer: 0,
      tencentStorage: 0
    });
  }
  return workspaces.get(workspaceId);
}

function addLedgerEntry(workspaces, entry) {
  if (entry.type !== "server_debit" && entry.type !== "storage_debit") return;
  const row = ensureWorkspace(workspaces, entry.workspaceId);
  if (entry.type === "server_debit") row.ledgerServer = money(row.ledgerServer + absDebit(entry));
  if (entry.type === "storage_debit") row.ledgerStorage = money(row.ledgerStorage + absDebit(entry));
}

function addTencentBill(workspaces, bill) {
  const row = ensureWorkspace(workspaces, bill.workspaceId);
  const amount = money(Number(bill.amount || 0));
  if (bill.resourceType === "server") row.tencentServer = money(row.tencentServer + amount);
  if (bill.resourceType === "storage") row.tencentStorage = money(row.tencentStorage + amount);
}

function summarizeWorkspace(row, { markup, tolerance }) {
  const expectedServer = money(row.tencentServer * (1 + markup));
  const expectedStorage = money(row.tencentStorage * (1 + markup));
  const serverDelta = money(row.ledgerServer - expectedServer);
  const storageDelta = money(row.ledgerStorage - expectedStorage);
  return {
    ...row,
    expectedServer,
    expectedStorage,
    serverDelta,
    storageDelta,
    ok: Math.abs(serverDelta) <= tolerance && Math.abs(storageDelta) <= tolerance
  };
}

export function reconcileTencentBills({
  ledgerEntries = [],
  tencentBills = [],
  markup = 0.1,
  tolerance = 0.01
} = {}) {
  const currency = assertSingleCurrency([...ledgerEntries, ...tencentBills]);
  const workspaces = new Map();

  for (const entry of ledgerEntries) addLedgerEntry(workspaces, entry);
  for (const bill of tencentBills) addTencentBill(workspaces, bill);

  const rows = [...workspaces.values()]
    .map((row) => summarizeWorkspace(row, { markup, tolerance }))
    .sort((a, b) => a.workspaceId.localeCompare(b.workspaceId));

  const totals = rows.reduce((acc, row) => ({
    ledgerServer: money(acc.ledgerServer + row.ledgerServer),
    ledgerStorage: money(acc.ledgerStorage + row.ledgerStorage),
    tencentServer: money(acc.tencentServer + row.tencentServer),
    tencentStorage: money(acc.tencentStorage + row.tencentStorage),
    expectedServer: money(acc.expectedServer + row.expectedServer),
    expectedStorage: money(acc.expectedStorage + row.expectedStorage),
    serverDelta: money(acc.serverDelta + row.serverDelta),
    storageDelta: money(acc.storageDelta + row.storageDelta)
  }), {
    ledgerServer: 0,
    ledgerStorage: 0,
    tencentServer: 0,
    tencentStorage: 0,
    expectedServer: 0,
    expectedStorage: 0,
    serverDelta: 0,
    storageDelta: 0
  });
  const mismatches = rows
    .filter((row) => !row.ok)
    .map((row) => ({
      workspaceId: row.workspaceId,
      serverDelta: row.serverDelta,
      storageDelta: row.storageDelta,
      ledgerServer: row.ledgerServer,
      expectedServer: row.expectedServer,
      ledgerStorage: row.ledgerStorage,
      expectedStorage: row.expectedStorage
    }));

  return {
    ok: mismatches.length === 0,
    currency,
    markup,
    tolerance,
    totals,
    workspaces: rows,
    mismatches
  };
}
