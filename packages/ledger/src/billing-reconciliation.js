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
  if (entry.type !== "server_debit" && entry.type !== "compute_debit" && entry.type !== "storage_debit") return;
  const row = ensureWorkspace(workspaces, entry.workspaceId);
  if (entry.type === "server_debit" || entry.type === "compute_debit") row.ledgerServer = money(row.ledgerServer + absDebit(entry));
  if (entry.type === "storage_debit") row.ledgerStorage = money(row.ledgerStorage + absDebit(entry));
}

function addTencentBill(workspaces, bill) {
  const row = ensureWorkspace(workspaces, bill.workspaceId);
  const amount = money(Number(bill.amount || 0));
  if (bill.resourceType === "server") row.tencentServer = money(row.tencentServer + amount);
  if (bill.resourceType === "storage") row.tencentStorage = money(row.tencentStorage + amount);
}

function firstPresent(row, keys) {
  for (const key of keys) {
    if (row[key] !== undefined && row[key] !== null && row[key] !== "") return row[key];
  }
  return undefined;
}

function parseTagString(tags) {
  return String(tags || "")
    .split(/[;,]/)
    .map((part) => part.trim())
    .filter(Boolean)
    .reduce((acc, part) => {
      const separator = part.includes(":") ? ":" : "=";
      const [key, ...rest] = part.split(separator);
      if (key && rest.length > 0) acc[key.trim()] = rest.join(separator).trim();
      return acc;
    }, {});
}

function tagsFrom(row) {
  const raw = firstPresent(row, ["Tag", "Tags", "tag", "tags"]);
  if (!raw) return {};
  if (typeof raw === "object" && !Array.isArray(raw)) return raw;
  return parseTagString(raw);
}

function workspaceIdFrom(row) {
  const direct = firstPresent(row, ["workspaceId", "WorkspaceId", "workspace_id", "WorkspaceID"]);
  if (direct) return String(direct);
  const tags = tagsFrom(row);
  return tags.workspace_id || tags.workspaceId || tags.WorkspaceId || tags.WorkspaceID || "";
}

function resourceIdFrom(row) {
  return String(firstPresent(row, ["sourceResourceId", "ResourceId", "resourceId", "InstanceId", "DiskId"]) || "");
}

function productNameFrom(row) {
  return String(firstPresent(row, ["resourceType", "ProductName", "productName", "InstanceType", "BusinessCode", "businessCode"]) || "").toLowerCase();
}

function resourceTypeFrom(row) {
  const product = productNameFrom(row);
  if (row.resourceType === "server" || product.includes("cvm") || product.includes("virtual machine") || product.includes("compute")) return "server";
  if (row.resourceType === "storage" || product.includes("block storage") || product.includes("cbs") || product.includes("disk")) return "storage";
  return "";
}

function amountFrom(row) {
  return Number(firstPresent(row, ["amount", "Amount", "RealTotalCost", "realTotalCost", "Cost", "cost", "CashPayAmount", "cashPayAmount"]) || 0);
}

export function normalizeTencentBillRows(rows = []) {
  const normalized = [];
  for (const row of rows) {
    const resourceType = resourceTypeFrom(row);
    if (!resourceType) continue;

    const workspaceId = workspaceIdFrom(row);
    const sourceResourceId = resourceIdFrom(row);
    if (!workspaceId) {
      throw new Error(`tencent_bill_workspace_id_missing:${sourceResourceId || "unknown_resource"}`);
    }

    normalized.push({
      workspaceId,
      resourceType,
      amount: money(amountFrom(row)),
      currency: String(firstPresent(row, ["currency", "Currency"]) || "CNY"),
      sourceResourceId
    });
  }
  return normalized;
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
  markup = 0.2,
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
