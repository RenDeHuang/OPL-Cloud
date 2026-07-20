import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { afterEach, test } from "node:test";

import * as authApi from "../../apps/console-ui/src/api/auth-api.ts";
import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import type { GatewayKeySummaryDTO } from "../../apps/console-ui/src/api/dtos.ts";
import { maskGatewayKey } from "../../apps/console-ui/src/console-model.ts";

const originalFetch = globalThis.fetch;
const appSource = () => readFile(new URL("../../apps/console-ui/src/App.vue", import.meta.url), "utf8");

function appFunction(source: string, name: string): string {
  const match = new RegExp(`(?:async )?function ${name}\\(`).exec(source);
  const marker = match?.[0] || "";
  const start = match?.index ?? -1;
  assert.notEqual(start, -1, `${name} must exist`);
  const rest = source.slice(start + marker.length);
  const next = rest.search(/\n(?:async )?function /);
  return source.slice(start, next === -1 ? source.length : start + marker.length + next);
}

afterEach(() => {
  globalThis.fetch = originalFetch;
});

test("logout clears the local session before the remote request settles", async () => {
  assert.equal(typeof authApi.logoutLocalFirst, "function");

  let settle: (response: Response) => void = () => {};
  const remote = new Promise<Response>((resolve) => { settle = resolve; });
  globalThis.fetch = async () => remote;
  const events: string[] = [];

  const pending = authApi.logoutLocalFirst(
    "csrf-alpha",
    () => events.push("local-cleared"),
    () => events.push("navigated")
  );
  assert.deepEqual(events, ["local-cleared", "navigated"]);

  settle(new Response(JSON.stringify({ ok: true }), {
    status: 200,
    headers: { "content-type": "application/json" }
  }));
  await pending;
});

test("API Key cleanup removes the raw value", () => {
  const revealed = { id: "41", name: "opl-workspace", status: "active" as const, value: "sk-raw" };
  assert.deepEqual(maskGatewayKey(revealed), { ...revealed, value: "" });
});

test("API, Gateway, and Workspace route changes clear secrets for direct and popstate navigation", async () => {
  const app = await appSource();
  assert.match(app, /let secretRequestGeneration = 0/);
  assert.match(app, /function clearSecrets\(\) \{\s*secretRequestGeneration \+= 1;/);
  assert.equal((app.match(/if \(!secretResponseStillCurrent\([^)]+\)\) return;/g) || []).length, 3);
  assert.match(app, /function isSensitiveRoute\(route: string\)/);
  for (const prefix of ["/console/api", "/console/gateway", "/console/workspace"]) {
    assert.match(app, new RegExp(prefix.replaceAll("/", "\\/")));
  }
  const watcher = app.slice(app.indexOf("\nwatch(path"), app.indexOf("\nonMounted(()"));
  assert.match(watcher, /if \(previous !== next\)[\s\S]*isSensitiveRoute\(previous \|\| ""\)\) clearSecrets\(\);/);
  assert.match(app, /const path = ref\(window\.location\.pathname\)/);
  assert.match(app, /const onPopState = \(\) => \{ path\.value = window\.location\.pathname; \}/);
});

test("general API Key create retries reuse a full-input intent until authoritative readback", async () => {
  const app = await appSource();
  const submit = appFunction(app, "submitKey");
  const compare = app.slice(app.indexOf("function sameGatewayKeyCreateRequest"), app.indexOf("async function submitKey"));

  assert.match(app, /let gatewayKeyCreateIntent: \{ input: CreateGatewayKeyRequest; idempotencyKey: string \} \| null = null/);
  for (const field of ["name", "quotaUsdMicros", "expiresInDays"]) assert.match(compare, new RegExp(`left\\.${field} === right\\.${field}`));
  assert.match(submit, /const input: CreateGatewayKeyRequest = \{/);
  assert.match(submit, /!gatewayKeyCreateIntent \|\| !sameGatewayKeyCreateRequest\(gatewayKeyCreateIntent\.input, input\)/);
  assert.match(submit, /createGatewayKey\(input,[\s\S]+gatewayKeyCreateIntent\.idempotencyKey\)/);
  assert.match(submit, /const readback = await getGatewayKey\(created\.data\.id\);[\s\S]+gatewayKeyCreateIntent = null;/);
  assert.doesNotMatch(submit.slice(submit.indexOf("catch")), /gatewayKeyCreateIntent = null/);
  assert.doesNotMatch(submit, /updateGatewayKey\(/);
});

test("session generation prevents late customer and admin reads from repopulating state", async () => {
  const app = await appSource();
  assert.match(app, /let sessionGeneration = 0/);
  assert.match(app, /function currentSessionRequest\(\)[\s\S]+const generation = sessionGeneration;[\s\S]+const userId = session\.value\?\.user\.id[\s\S]+generation === sessionGeneration && userId === session\.value\?\.user\.id/);
  assert.match(app, /function replaceSession\([^)]+\) \{\s*sessionGeneration \+= 1;\s*clearSessionState\(\);\s*session\.value = next;/);
  for (const name of [
    "loadWorkspaces", "loadWorkspaceStatus", "loadWallet", "loadKeys", "loadUsage", "loadStats",
    "loadAccountUsage", "loadHistory", "loadReceipts", "loadAnnouncements", "loadCatalog", "loadCustomer", "loadAdmin",
    "recoverWorkspaceLaunch"
  ]) assert.match(appFunction(app, name), /currentSessionRequest\(\)/, `${name} must bind reads to the current session`);
  assert.match(appFunction(app, "ensureSession"), /replaceSession\(next\)/);
  assert.match(appFunction(app, "submitLogin"), /replaceSession\(next\)[\s\S]+navigate\(/);
  assert.match(appFunction(app, "signOut"), /replaceSession\(null\)/);
});

test("session replacement clears login password and receipt refresh returns to the first page", async () => {
  const app = await appSource();
  const clearSession = appFunction(app, "clearSessionState");
  const loadReceipts = appFunction(app, "loadReceipts");
  assert.match(clearSession, /loginForm\.password\s*=\s*""/);
  assert.match(clearSession, /loginForm\.email\s*=\s*""/);
  assert.match(loadReceipts, /if \(!cursor\)\s*receiptCursorStack\.value\s*=\s*\[\]/);
});

test("leaving Login clears the password without waiting for a session replacement", async () => {
  const app = await appSource();
  const watcher = app.slice(app.indexOf("\nwatch(path"), app.indexOf("\nonMounted(()"));
  assert.match(watcher, /if \(previous === "\/login"\)[\s\S]*loginForm\.email\s*=\s*""/);
  assert.match(watcher, /if \(previous === "\/login"\)[\s\S]*loginForm\.password\s*=\s*""/);
  assert.match(watcher, /if \(previous !== next\)[\s\S]*closeModal\(\)/);
});

test("Workspace reads preserve confirmed Runtime unless authority proves empty or changes identity", async () => {
  const app = await appSource();
  const source = appFunction(app, "loadWorkspaces").replaceAll("unavailableSource<WorkspaceListData>", "unavailableSource");
  const statusSource = appFunction(app, "loadWorkspaceStatus").replaceAll("unavailableSource<WorkspaceRuntimeDTO>", "unavailableSource");
  const confirmedRuntime = {
    source: "fabric",
    status: "available",
    available: true,
    fetchedAt: "2026-07-20T00:00:00Z",
    data: { workspaceId: "workspace-a", status: "running", ready: true, checks: [] }
  };

  async function runtimeAfter(result: unknown, rejects = false) {
    const workspace = { value: { id: "workspace-a" } };
    const workspaceSource = { value: { available: true, data: { items: [workspace.value] } } as unknown };
    const workspaceStatusSource = { value: confirmedRuntime as unknown };
    const loadWorkspaces = new Function(
      "currentSessionRequest",
      "loading",
      "resetSource",
      "workspace",
      "workspaceSource",
      "workspaceStatusSource",
      "getWorkspaces",
      "unavailableSource",
      "errors",
      "friendlyError",
      `${source}\nreturn loadWorkspaces;`
    )(
      () => () => true,
      { workspace: false },
      () => {},
      workspace,
      workspaceSource,
      workspaceStatusSource,
      async () => {
        if (rejects) throw new Error("workspace_unavailable");
        return result;
      },
      (owner: string) => ({ source: owner, status: "unavailable", available: false, fetchedAt: "" }),
      { workspace: "" },
      (error: Error) => error.message
    ) as () => Promise<void>;

    await loadWorkspaces();
    return workspaceStatusSource.value;
  }

  const unavailable = { source: "control-plane", status: "unavailable", available: false, fetchedAt: "" };
  async function statusAfter(workspaceState: unknown) {
    const workspaceStatusSource = { value: confirmedRuntime as unknown };
    const loadWorkspaceStatus = new Function(
      "currentSessionRequest",
      "workspace",
      "workspaceSource",
      "workspaceStatusSource",
      `${statusSource}\nreturn loadWorkspaceStatus;`
    )(
      () => () => true,
      { value: null },
      { value: workspaceState },
      workspaceStatusSource
    ) as () => Promise<void>;
    await loadWorkspaceStatus();
    return workspaceStatusSource.value;
  }

  assert.strictEqual(await statusAfter(unavailable), confirmedRuntime);
  assert.equal(await statusAfter({ source: "control-plane", status: "empty", available: true, fetchedAt: "", data: { items: [] } }), null);
  assert.strictEqual(await runtimeAfter(unavailable), confirmedRuntime);
  assert.strictEqual(await runtimeAfter(null, true), confirmedRuntime);
  assert.strictEqual(await runtimeAfter({ source: "control-plane", status: "available", available: true, fetchedAt: "", data: { items: [{ id: "workspace-a" }] } }), confirmedRuntime);
  assert.strictEqual(await runtimeAfter({ source: "control-plane", status: "available", available: true, fetchedAt: "", data: { items: [{ id: "workspace-a" }, { id: "workspace-b" }] } }), confirmedRuntime);
  assert.equal(await runtimeAfter({ source: "control-plane", status: "empty", available: true, fetchedAt: "", data: { items: [] } }), null);
  assert.equal(await runtimeAfter({ source: "control-plane", status: "available", available: true, fetchedAt: "", data: { items: [{ id: "workspace-b" }] } }), null);
});

test("customer routes load only their page-owned sources and dispatch on every navigation", async () => {
  const app = await appSource();
  const loadCustomerSource = appFunction(app, "loadCustomer");
  const loaderNames = [
    "loadWorkspaces", "loadWorkspaceStatus", "loadWallet", "loadKeys", "loadUsage", "loadStats",
    "loadAccountUsage", "loadHistory", "loadReceipts", "loadAnnouncements", "loadCatalog", "recoverWorkspaceLaunch"
  ];
  const calls: string[] = [];
  const loaderFunctions = loaderNames.map((name) => async () => { calls.push(name); });
  const path = { value: "" };
  const apiRoute = { value: false };
  const activeApiPage = { value: "overview" };
  const loadCustomer = new Function(
    "path",
    "apiRoute",
    "activeApiPage",
    "currentSessionRequest",
    ...loaderNames,
    `${loadCustomerSource}\nreturn loadCustomer;`
  )(
    path,
    apiRoute,
    activeApiPage,
    () => () => true,
    ...loaderFunctions
  ) as () => Promise<void>;

  async function callsFor(route: string, apiPage: "overview" | "usage" | "keys" = "overview") {
    calls.length = 0;
    path.value = route;
    apiRoute.value = route === "/console/api" || route.startsWith("/console/api/");
    activeApiPage.value = apiPage;
    await loadCustomer();
    return [...calls].sort();
  }

  const overviewCalls = [
    "loadWorkspaces", "loadWallet", "loadAccountUsage", "loadReceipts", "loadCatalog", "loadAnnouncements",
    "loadWorkspaceStatus", "recoverWorkspaceLaunch"
  ].sort();
  assert.deepEqual(await callsFor("/console"), overviewCalls);
  assert.deepEqual(await callsFor("/console/overview"), overviewCalls);
  assert.deepEqual(await callsFor("/console/workspace"), [
    "loadWorkspaces", "loadReceipts", "loadCatalog", "loadWorkspaceStatus", "recoverWorkspaceLaunch"
  ].sort());
  assert.deepEqual(await callsFor("/console/billing"), [
    "loadWorkspaces", "loadWallet", "loadAccountUsage", "loadHistory", "loadReceipts"
  ].sort());
  assert.deepEqual(await callsFor("/console/announcements"), ["loadAnnouncements"]);
  assert.deepEqual(await callsFor("/console/api", "overview"), [
    "loadWallet", "loadAccountUsage", "loadHistory"
  ].sort());
  assert.deepEqual(await callsFor("/console/api/usage", "usage"), ["loadKeys"]);
  assert.deepEqual(await callsFor("/console/api/keys", "keys"), ["loadKeys"]);

  const handleRoute = appFunction(app, "handleRoute");
  assert.doesNotMatch(handleRoute, /!workspaceSource/);
  assert.match(handleRoute, /if \(isAdminRoute\.value\) \{\s*await loadAdmin\(\);\s*\} else \{\s*await loadCustomer\(\);\s*\}/);
  assert.match(appFunction(app, "refreshCurrentPage"), /clearSecrets\(\);\s*if \(isAdminRoute\.value\) return void loadAdmin\(\);\s*void loadCustomer\(\);/);
});

test("closing a modal or replacing the session clears every modal draft", async () => {
  const app = await appSource();
  const closeModalSource = appFunction(app, "closeModal");
  const clearSession = appFunction(app, "clearSessionState");

  assert.match(closeModalSource, /Object\.assign\(launchForm, \{ name: "", packageId: "basic" \}\)/);
  assert.match(closeModalSource, /Object\.assign\(keyForm, \{ name: "", quotaUsd: 10, expiresInDays: 30 \}\)/);
  assert.match(closeModalSource, /Object\.assign\(adminUserForm, \{ email: "", password: "", name: "" \}\)/);
  assert.match(closeModalSource, /modal\.value = ""/);
  assert.match(clearSession, /closeModal\(\)/);
  assert.equal((app.match(/modal\.value = ""/g) || []).length, 1, "every modal close path must reset drafts");

  const modalTemplate = app.slice(app.indexOf("<div v-if=\"modal\" class=\"modal-backdrop\""));
  assert.doesNotMatch(modalTemplate, /@click(?:\.self)?="modal = ''"/);
  assert.ok((modalTemplate.match(/@click(?:\.self)?="closeModal"/g) || []).length >= 7);

  const launchForm = { name: "secret workspace", packageId: "pro" };
  const keyForm = { name: "secret key", quotaUsd: 99, expiresInDays: 365 };
  const adminUserForm = { email: "owner@example.com", password: "secret password", name: "Owner" };
  const walletAdjustmentForm = { kind: "debit", amountUsd: "9", reason: "secret reason", confirmationAccountId: "acct-secret", relatedOperationId: "op-secret" };
  const announcementForm = { title: "secret title", body: "secret body", startsAt: "start", endsAt: "end" };
  const selectedOperatorAccountId = { value: "acct-secret" };
  const selectedReview = { value: { resourceType: "workspace", id: "review-secret" } };
  const modal = { value: "admin-user" };
  const closeModal = new Function(
    "launchForm",
    "keyForm",
    "adminUserForm",
    "walletAdjustmentForm",
    "announcementForm",
    "selectedOperatorAccountId",
    "selectedReview",
    "modal",
    `${closeModalSource}\nreturn closeModal;`
  )(launchForm, keyForm, adminUserForm, walletAdjustmentForm, announcementForm, selectedOperatorAccountId, selectedReview, modal) as () => void;

  closeModal();
  modal.value = "admin-user";
  assert.deepEqual(launchForm, { name: "", packageId: "basic" });
  assert.deepEqual(keyForm, { name: "", quotaUsd: 10, expiresInDays: 30 });
  assert.deepEqual(adminUserForm, { email: "", password: "", name: "" });
  assert.deepEqual(walletAdjustmentForm, { kind: "recharge", amountUsd: "", reason: "", confirmationAccountId: "", relatedOperationId: "" });
  assert.deepEqual(announcementForm, { title: "", body: "", startsAt: "", endsAt: "" });
  assert.equal(selectedOperatorAccountId.value, "");
  assert.equal(selectedReview.value, null);
});

test("per-Key usage ignores late key, period, and page responses including finalizers", async () => {
  const app = await appSource();
  const usage = appFunction(app, "loadUsage");
  const stats = appFunction(app, "loadStats");

  assert.match(app, /let usageRequestGeneration = 0/);
  assert.match(app, /let usageStatsRequestGeneration = 0/);
  assert.match(usage, /const generation = \+\+usageRequestGeneration/);
  assert.match(usage, /generation === usageRequestGeneration[\s\S]+keyId === selectedUsageKeyId\.value[\s\S]+page === gatewayPageNumber\.page/);
  assert.match(usage, /result\.available && result\.data\.page !== page/);
  assert.doesNotMatch(usage, /gatewayPageNumber\.page = usageSource/);
  assert.ok((usage.match(/requestStillCurrent\(\)/g) || []).length >= 3);
  assert.match(stats, /const generation = \+\+usageStatsRequestGeneration/);
  assert.match(stats, /generation === usageStatsRequestGeneration[\s\S]+keyId === selectedUsageKeyId\.value[\s\S]+period === gatewayPeriod\.value/);
  assert.ok((stats.match(/requestStillCurrent\(\)/g) || []).length >= 3);
});

test("Key source failures preserve confirmed Usage while authoritative Key changes reset selection", async () => {
  const app = await appSource();
  const loadKeys = appFunction(app, "loadKeys");
  const selectKey = appFunction(app, "selectUsageKey");

  assert.match(loadKeys, /keySource\.value = result;\s*if \(!result\.available\) return;/);
  assert.match(loadKeys, /if \(!result\.data\.items\.some\(\(key\) => key\.id === selectedUsageKeyId\.value\)\) \{\s*selectUsageKey\(result\.data\.items\[0\]\?\.id \|\| ""\);\s*return;\s*\}/);
  assert.match(loadKeys, /if \(activeApiPage\.value === "usage"\) void Promise\.all\(\[loadUsage\(\), loadStats\(\)\]\)/);
  const catchBody = loadKeys.slice(loadKeys.indexOf("catch"), loadKeys.indexOf("finally"));
  assert.doesNotMatch(catchBody, /selectUsageKey|selectedUsageKeyId|usageSource|usageStatsSource|gatewayPageNumber/);
  assert.match(selectKey, /usageRequestGeneration \+= 1/);
  assert.match(selectKey, /usageStatsRequestGeneration \+= 1/);
  assert.match(selectKey, /Object\.assign\(gatewayPageNumber, \{ page: 1, pages: 0, total: 0 \}\)/);
  assert.match(selectKey, /usageSource\.value = null/);
  assert.match(selectKey, /usageStatsSource\.value = null/);
  assert.match(selectKey, /resetSource\("usage"\)[\s\S]+resetSource\("stats"\)/);
  assert.match(selectKey, /if \(!keyId \|\| activeApiPage\.value !== "usage"\) return;[\s\S]+Promise\.all\(\[loadUsage\(1\), loadStats\(\)\]\)/);

  const selectedKey = { value: "" };
  const selectedPage = { value: "keys" };
  const usageCalls: string[] = [];
  const selectUsageKeyForPage = new Function(
    "selectedUsageKeyId",
    "gatewayPageNumber",
    "usageSource",
    "usageStatsSource",
    "loading",
    "resetSource",
    "clearSecrets",
    "activeApiPage",
    "loadUsage",
    "loadStats",
    `let usageRequestGeneration = 0; let usageStatsRequestGeneration = 0; ${selectKey.replace("keyId: string", "keyId")}\nreturn selectUsageKey;`
  )(
    selectedKey,
    { page: 4, pages: 8, total: 80 },
    { value: "confirmed-usage" },
    { value: "confirmed-stats" },
    { usage: false, stats: false },
    () => {},
    () => {},
    selectedPage,
    (page: number) => { usageCalls.push(`usage:${page}`); },
    () => { usageCalls.push("stats"); }
  ) as (keyId: string) => void;

  selectUsageKeyForPage("41");
  assert.deepEqual(usageCalls, []);
  selectedPage.value = "usage";
  selectUsageKeyForPage("41");
  assert.deepEqual(usageCalls.sort(), ["stats", "usage:1"]);

  const resultBlock = loadKeys.slice(loadKeys.indexOf("    keySource.value = result;"), loadKeys.indexOf("  }\n  catch"));
  const applyKeyResult = new Function(
    "result",
    "keySource",
    "selectedUsageKeyId",
    "selectUsageKey",
    "loadUsage",
    "loadStats",
    resultBlock
  ) as (
    result: { available: boolean; data?: { items: Array<{ id: string }> } },
    keySource: { value: unknown },
    selectedUsageKeyId: { value: string },
    selectUsageKey: (keyId: string) => void,
    loadUsage: () => void,
    loadStats: () => void
  ) => void;
  const keySource = { value: null as unknown };
  const selectedUsageKeyId = { value: "41" };
  const confirmed = { usage: "usage-41" as string | null, stats: "stats-41" as string | null, page: 4 };
  const selections: string[] = [];
  const selectUsageKey = (keyId: string) => {
    selections.push(keyId);
    selectedUsageKeyId.value = keyId;
    confirmed.usage = null;
    confirmed.stats = null;
    confirmed.page = 1;
  };

  applyKeyResult({ available: false }, keySource, selectedUsageKeyId, selectUsageKey, () => {}, () => {});
  assert.deepEqual({ selected: selectedUsageKeyId.value, ...confirmed, selections }, {
    selected: "41", usage: "usage-41", stats: "stats-41", page: 4, selections: []
  });

  applyKeyResult({ available: true, data: { items: [] } }, keySource, selectedUsageKeyId, selectUsageKey, () => {}, () => {});
  assert.deepEqual({ selected: selectedUsageKeyId.value, ...confirmed }, { selected: "", usage: null, stats: null, page: 1 });

  selectedUsageKeyId.value = "41";
  confirmed.usage = "usage-41";
  confirmed.stats = "stats-41";
  confirmed.page = 4;
  selections.length = 0;
  applyKeyResult({ available: true, data: { items: [{ id: "42" }] } }, keySource, selectedUsageKeyId, selectUsageKey, () => {}, () => {});
  assert.deepEqual({ selected: selectedUsageKeyId.value, ...confirmed, selections }, {
    selected: "42", usage: null, stats: null, page: 1, selections: ["42"]
  });

  for (const name of ["loadCustomer", "refreshCurrentPage", "removeKey"]) {
    assert.doesNotMatch(appFunction(app, name), /loadUsage\(|loadStats\(/, `${name} must let loadKeys own automatic Usage refresh`);
  }

  const usagePageStart = app.indexOf("<section v-else-if=\"activeApiPage === 'usage'\"");
  const usagePageEnd = app.indexOf("<section v-else class=\"panel\">", usagePageStart);
  const usagePage = app.slice(usagePageStart, usagePageEnd);
  const readyStart = usagePage.indexOf("<template v-else>");
  assert.notEqual(readyStart, -1, "Usage must render records only after the Key source is available and non-empty");
  const keyStates = usagePage.slice(0, readyStart);
  assert.match(keyStates, /loading\.keys[\s\S]+正在读取 API Key/);
  assert.match(keyStates, /errors\.keys[\s\S]+loadKeys/);
  assert.match(keyStates, /keySource\?\.status === 'unavailable'[\s\S]+loadKeys/);
  assert.match(keyStates, /keySource\?\.status === 'empty'[\s\S]+暂无 API Key/);
  assert.doesNotMatch(keyStates, /暂无使用记录/);
  assert.match(usagePage.slice(readyStart), /暂无使用记录/);
});

test("account aggregate remains monthly when the per-Key period changes", async () => {
  const app = await appSource();
  const accountUsage = appFunction(app, "loadAccountUsage");
  const selectPeriod = appFunction(app, "selectPeriod");

  assert.match(accountUsage, /getGatewayAccountUsageSummary\("month"\)/);
  assert.doesNotMatch(accountUsage, /getGatewayAccountUsageSummary\(gatewayPeriod\.value\)/);
  assert.match(selectPeriod, /void loadStats\(\)/);
  assert.doesNotMatch(selectPeriod, /loadAccountUsage\(/);
});

test("Billing receipt pages preserve opaque cursor history and reject late responses", async () => {
  const app = await appSource();
  const load = appFunction(app, "loadReceipts");
  const next = appFunction(app, "nextReceiptPage");
  const previous = appFunction(app, "previousReceiptPage");
  const clearSession = appFunction(app, "clearSessionState");
  const billingStart = app.indexOf("<section v-else class=\"billing-page\"");
  const billingEnd = app.indexOf("</template>", billingStart);
  const billing = app.slice(billingStart, billingEnd);

  assert.match(app, /const receiptCursor = ref\(""\)/);
  assert.match(app, /const receiptCursorStack = ref<string\[\]>\(\[\]\)/);
  assert.match(app, /let receiptRequestGeneration = 0/);
  assert.match(load, /const generation = \+\+receiptRequestGeneration/);
  assert.match(load, /cursor === receiptCursor\.value/);
  assert.match(load, /getBillingReceipts\(cursor\)/);
  assert.ok((load.match(/requestStillCurrent\(\)/g) || []).length >= 3);
  assert.match(next, /receiptsSource\.value\.data\.nextCursor/);
  assert.match(next, /receiptCursorStack\.value\.push\(receiptCursor\.value\)/);
  assert.match(next, /loadReceipts\(nextCursor\)/);
  assert.match(previous, /receiptCursorStack\.value\.pop\(\)/);
  assert.match(previous, /loadReceipts\(previousCursor\)/);
  assert.match(clearSession, /receiptRequestGeneration \+= 1/);
  assert.match(clearSession, /receiptCursor\.value = ""/);
  assert.match(clearSession, /receiptCursorStack\.value = \[\]/);
  assert.match(billing, /aria-label="交易记录分页"/);
  assert.match(billing, /@click="previousReceiptPage"/);
  assert.match(billing, /@click="nextReceiptPage"/);
  assert.match(billing, /receiptsSource\.data\.hasMore/);
});

test("Billing receipt detail adapter encodes the opaque receipt identity", async () => {
  let requestedUrl = "";
  globalThis.fetch = async (input) => {
    requestedUrl = String(input);
    return new Response(JSON.stringify({
      source: "ledger", status: "available", available: true, fetchedAt: "2026-07-20T00:00:00Z",
      data: {
        receiptId: "receipt / alpha", type: "billing.workspace_purchased.v1", status: "completed",
        workspaceId: "workspace-alpha", createdAt: "2026-07-20T00:00:00Z", resourceType: "workspace",
        resourceId: "workspace-alpha", priceVersion: "pilot-usd-2026-07-v1", currency: "USD",
        periodStart: "2026-07-20T00:00:00Z", paidThrough: "2026-08-20T00:00:00Z", totalUsdMicros: 52_580_000
      }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  const result = await readApi.getBillingReceipt("receipt / alpha");

  assert.equal(requestedUrl, "/api/billing/receipts/receipt%20%2F%20alpha");
  assert.equal(result.available && result.data.receiptId, "receipt / alpha");
});

test("Billing receipt detail ignores late selections and mismatched readback", async () => {
  const app = await appSource();
  const detailSource = appFunction(app, "loadReceiptDetail");
  const clearDetail = appFunction(app, "clearReceiptDetail");
  const loadReceipts = appFunction(app, "loadReceipts");
  const clearSession = appFunction(app, "clearSessionState");

  assert.match(app, /let receiptDetailRequestGeneration = 0/);
  assert.match(detailSource, /const generation = \+\+receiptDetailRequestGeneration/);
  assert.match(detailSource, /generation === receiptDetailRequestGeneration[\s\S]+receiptId === selectedReceiptId\.value/);
  assert.match(detailSource, /result\.available && result\.data\.receiptId !== receiptId/);
  assert.ok((detailSource.match(/requestStillCurrent\(\)/g) || []).length >= 3);
  assert.match(clearDetail, /receiptDetailRequestGeneration \+= 1/);
  assert.match(clearDetail, /selectedReceiptId\.value = ""/);
  assert.match(clearDetail, /receiptDetailSource\.value = null/);
  assert.match(loadReceipts, /clearReceiptDetail\(\)/);
  assert.match(clearSession, /clearReceiptDetail\(\)/);

  const selectedReceiptId = { value: "" };
  const receiptDetailSource = { value: null as unknown };
  const loading = { receiptDetail: false };
  const errors = { receiptDetail: "" };
  const pending = new Map<string, (value: unknown) => void>();
  const getBillingReceipt = (receiptId: string) => new Promise((resolve) => pending.set(receiptId, resolve));
  const loadReceiptDetail = new Function(
    "selectedReceiptId",
    "receiptDetailSource",
    "loading",
    "errors",
    "currentSessionRequest",
    "resetSource",
    "unavailableSource",
    "friendlyError",
    "getBillingReceipt",
    `let receiptDetailRequestGeneration = 0; ${detailSource
      .replace("receiptId: string", "receiptId")
      .replaceAll("unavailableSource<BillingReceipt>", "unavailableSource")}\nreturn loadReceiptDetail;`
  )(
    selectedReceiptId,
    receiptDetailSource,
    loading,
    errors,
    () => () => true,
    (key: "receiptDetail") => { errors[key] = ""; },
    (owner: string) => ({ source: owner, status: "unavailable", available: false, fetchedAt: "" }),
    (error: Error) => error.message,
    getBillingReceipt
  ) as (receiptId: string) => Promise<void>;

  const first = loadReceiptDetail("receipt-a");
  const second = loadReceiptDetail("receipt-b");
  pending.get("receipt-b")?.({ source: "ledger", status: "available", available: true, fetchedAt: "", data: { receiptId: "receipt-b" } });
  await second;
  pending.get("receipt-a")?.({ source: "ledger", status: "available", available: true, fetchedAt: "", data: { receiptId: "receipt-a" } });
  await first;
  assert.deepEqual(receiptDetailSource.value, { source: "ledger", status: "available", available: true, fetchedAt: "", data: { receiptId: "receipt-b" } });
  assert.equal(loading.receiptDetail, false);

  const mismatched = loadReceiptDetail("receipt-c");
  pending.get("receipt-c")?.({ source: "ledger", status: "available", available: true, fetchedAt: "", data: { receiptId: "receipt-other" } });
  await mismatched;
  assert.deepEqual(receiptDetailSource.value, { source: "ledger", status: "unavailable", available: false, fetchedAt: "" });
  assert.equal(errors.receiptDetail, "billing_receipt_identity_mismatch");
});

test("customer mutations cannot write shared state after their session is replaced", async () => {
  const app = await appSource();
  const minimumChecks: Record<string, number> = {
    submitWorkspaceLaunch: 5,
    revealWorkspace: 3,
    rotateWorkspace: 4,
    revealKey: 3,
    toggleKey: 5,
    removeKey: 5,
    submitKey: 5,
    readAnnouncement: 4,
    provisionOperatorUser: 4
  };

  for (const [name, count] of Object.entries(minimumChecks)) {
    const body = appFunction(app, name);
    assert.match(body, /const requestStillCurrent = currentSessionRequest\(\)/, `${name} must bind the mutation to its session`);
    assert.ok((body.match(/requestStillCurrent\(\)/g) || []).length >= count, `${name} must guard success, catch, and finally writes`);
    assert.match(body, /catch[\s\S]+requestStillCurrent\(\)/, `${name} catch must ignore an old session`);
    assert.match(body, /finally[\s\S]+requestStillCurrent\(\)/, `${name} finally must not clear the new session busy state`);
  }

  const submit = appFunction(app, "submitWorkspaceLaunch");
  assert.match(submit, /await launchWorkspace\([\s\S]+if \(!requestStillCurrent\(\)\) return;\s*workspaceLaunchIntent = null;/);
  assert.match(submit, /await Promise\.all\([\s\S]+if \(!requestStillCurrent\(\)\) return;\s*await loadWorkspaceStatus\(\);\s*if \(!requestStillCurrent\(\)\) return;/);

  const rotate = appFunction(app, "rotateWorkspace");
  assert.match(rotate, /await rotateWorkspaceCredentials\([\s\S]+if \(!requestStillCurrent\(\)\) return;\s*runtimeRotationIntent = null;\s*if \(!secretResponseStillCurrent/);

  for (const [name, calls] of Object.entries({
    toggleKey: ["updateGatewayKey", "getGatewayKey", "loadKeys"],
    removeKey: ["deleteGatewayKey", "loadKeys"]
  })) {
    const body = appFunction(app, name);
    for (const call of calls) assert.match(body, new RegExp(`await ${call.replace(".", "\\.")}\\([\\s\\S]+if \\(!requestStillCurrent\\(\\)\\) return;`));
  }
});

test("Key toggle keeps only the current target intent and replaces it when direction changes", async () => {
  const app = await appSource();
  const clearSession = appFunction(app, "clearSessionState");
  const toggle = appFunction(app, "toggleKey");

  assert.match(app, /const gatewayKeyToggleIntents = new Map<string, \{ targetStatus: GatewayKeySummaryDTO\["status"\]; idempotencyKey: string \}>\(\)/);
  assert.match(clearSession, /gatewayKeyToggleIntents\.clear\(\)/);
  assert.match(toggle, /let intent = gatewayKeyToggleIntents\.get\(key\.id\)/);
  assert.match(toggle, /if \(!intent \|\| intent\.targetStatus !== expectedStatus\) \{[\s\S]+targetStatus: expectedStatus[\s\S]+key-toggle:\$\{crypto\.randomUUID\(\)\}[\s\S]+gatewayKeyToggleIntents\.set\(key\.id, intent\)/);
  assert.match(toggle, /updateGatewayKey\([^;]+intent\.idempotencyKey\)[\s\S]+getGatewayKey\(key\.id\)/);
  assert.equal((toggle.match(/updateGatewayKey\(/g) || []).length, 1);
  assert.match(toggle, /if \(!readback\.available \|\| readback\.data\.status !== expectedStatus[^)]*\) throw[\s\S]+gatewayKeyToggleIntents\.delete\(key\.id\)/);
  assert.doesNotMatch(toggle, /intentKey|gatewayKeyToggleIntents\.get\(expectedStatus\)/);
  const finalCatch = toggle.slice(toggle.lastIndexOf("catch (error)"));
  assert.doesNotMatch(finalCatch, /updateGatewayKey\(|gatewayKeyToggleIntents\.delete\(/);

  const intentSelection = toggle.slice(toggle.indexOf("  const expectedStatus ="), toggle.indexOf("  gatewayBusy.value = true;"));
  const selectIntent = new Function(
    "key",
    "gatewayKeyToggleIntents",
    "crypto",
    `${intentSelection}\nreturn intent.idempotencyKey;`
  ) as (
    key: { id: string; status: GatewayKeySummaryDTO["status"] },
    intents: Map<string, { targetStatus: GatewayKeySummaryDTO["status"]; idempotencyKey: string }>,
    crypto: { randomUUID(): string }
  ) => string;
  const intents = new Map<string, { targetStatus: GatewayKeySummaryDTO["status"]; idempotencyKey: string }>();
  let nextUuid = 0;
  const fakeCrypto = { randomUUID: () => `uuid-${++nextUuid}` };

  assert.deepEqual([
    selectIntent({ id: "41", status: "active" }, intents, fakeCrypto),
    selectIntent({ id: "41", status: "active" }, intents, fakeCrypto),
    selectIntent({ id: "41", status: "disabled" }, intents, fakeCrypto),
    selectIntent({ id: "41", status: "active" }, intents, fakeCrypto)
  ], ["key-toggle:uuid-1", "key-toggle:uuid-1", "key-toggle:uuid-2", "key-toggle:uuid-3"]);
  assert.equal(intents.size, 1);
  assert.deepEqual(intents.get("41"), { targetStatus: "disabled", idempotencyKey: "key-toggle:uuid-3" });
});

test("Key delete retries reuse a per-resource intent and only GET 404 confirms an unknown write", async () => {
  const app = await appSource();
  const clearSession = appFunction(app, "clearSessionState");
  const remove = appFunction(app, "removeKey");
  const errorCode = appFunction(app, "apiErrorCode");

  assert.match(app, /const gatewayKeyDeleteIntents = new Map<string, string>\(\)/);
  assert.match(clearSession, /gatewayKeyDeleteIntents\.clear\(\)/);
  assert.match(remove, /let idempotencyKey = gatewayKeyDeleteIntents\.get\(key\.id\)/);
  assert.match(remove, /if \(!idempotencyKey\) \{[\s\S]+key-delete:\$\{crypto\.randomUUID\(\)\}[\s\S]+gatewayKeyDeleteIntents\.set\(key\.id, idempotencyKey\)/);
  assert.match(remove, /deleteGatewayKey\([^;]+idempotencyKey\)/);
  assert.equal((remove.match(/deleteGatewayKey\(/g) || []).length, 1);
  assert.match(remove, /if \(deleteError\) \{[\s\S]+getGatewayKey\(key\.id\)[\s\S]+apiErrorCode\(readError\) === "gateway_key_not_found"[\s\S]+gatewayKeyDeleteIntents\.delete\(key\.id\)/);
  assert.equal((remove.match(/gatewayKeyDeleteIntents\.delete\(/g) || []).length, 1);
  assert.match(errorCode, /payload[\s\S]+\.error/);
  const finalCatch = remove.slice(remove.lastIndexOf("catch (error)"));
  assert.doesNotMatch(finalCatch, /deleteGatewayKey\(|gatewayKeyDeleteIntents\.delete\(/);
});

test("general API Key writes carry CSRF and opaque idempotency keys", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init });
    const status = init?.method === "DELETE" ? "deleted" : "active";
    return new Response(JSON.stringify({
      source: "sub2api", status: "available", available: true, fetchedAt: "2026-07-20T00:00:00Z",
      data: init?.method === "DELETE"
        ? { operationId: "op-delete", status }
        : { id: "41", name: "personal", kind: "general", status, quotaUsdMicros: 1_000_000,
            quotaUsedUsdMicros: 0, usage5hUsdMicros: 0, usage1dUsdMicros: 0, usage7dUsdMicros: 0,
            lastUsedAt: null, expiresAt: "2026-08-19T00:00:00Z", manageable: true, deletable: true }
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  await readApi.createGatewayKey({ name: "personal", quotaUsdMicros: 1_000_000, expiresInDays: 30 }, "csrf-key", "key-create:opaque");
  await readApi.updateGatewayKey("41", { enabled: false }, "csrf-key", "key-toggle:opaque");
  await readApi.deleteGatewayKey("41", "csrf-key", "key-delete:opaque");

  assert.deepEqual(requests.map(({ url }) => url), ["/api/gateway/keys", "/api/gateway/keys/41", "/api/gateway/keys/41"]);
  assert.deepEqual(requests.map(({ init }) => init?.method), ["POST", "PATCH", "DELETE"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("x-opl-csrf")), ["csrf-key", "csrf-key", "csrf-key"]);
  assert.deepEqual(requests.map(({ init }) => new Headers(init?.headers).get("Idempotency-Key")), ["key-create:opaque", "key-toggle:opaque", "key-delete:opaque"]);
  assert.deepEqual(JSON.parse(String(requests[0]?.init?.body)), { name: "personal", quotaUsdMicros: 1_000_000, expiresInDays: 30 });
});
