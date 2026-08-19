import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import { decodeSource } from "../../apps/console-ui/src/api/dtos.ts";
import { unavailableSource } from "../../apps/console-ui/src/app/use-console-controller.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Console exposes typed source adapters for the customer truth surfaces", async () => {
  assert.equal(typeof readApi.getGatewayWallet, "function");
  assert.equal(typeof readApi.getGatewayEndpoint, "function");
  assert.equal(typeof readApi.getGatewayGroups, "function");
  assert.equal(typeof readApi.getGatewayKeys, "function");
  assert.equal(typeof readApi.getGatewayKey, "function");
  assert.equal(typeof readApi.createGatewayKey, "function");
  assert.equal(typeof readApi.updateGatewayKey, "function");
  assert.equal(typeof readApi.deleteGatewayKey, "function");
  assert.equal(typeof readApi.getGatewayKeyUsage, "function");
  assert.equal(typeof readApi.getGatewayKeyUsageSummary, "function");
  assert.equal(typeof readApi.getGatewayAccountUsageSummary, "function");
  assert.equal(typeof readApi.getGatewayBalanceHistory, "function");
  assert.equal(typeof readApi.revealGatewayKey, "function");
  assert.equal(typeof workspaceApi.launchWorkspace, "function");
  assert.equal(typeof workspaceApi.getWorkspaceLaunch, "function");
  assert.equal(typeof workspaceApi.getWorkspaces, "function");
  assert.equal(typeof workspaceApi.findWorkspaceInPages, "function");
  assert.equal(typeof workspaceApi.getWorkspaceRuntimeStatus, "function");
  assert.equal(typeof workspaceApi.getWorkspaceGatewayBudget, "function");
  assert.equal(typeof workspaceApi.updateWorkspaceGatewayBudget, "function");
  assert.equal(typeof workspaceApi.revealWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.rotateWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.updateWorkspaceRenewal, "function");
});

test("Workspace Gateway budget adapters use the scoped route and exact mutation boundary", async () => {
  const requests: Array<{ path: string; init?: RequestInit }> = [];
  const originalFetch = globalThis.fetch;
  const current = {
    workspaceId: "workspace / alpha",
    keyId: "9223372036854775807",
    status: "active",
    quotaUsdMicros: "9007199254740993",
    quotaUsedUsdMicros: "3000000",
    rateLimit5hUsdMicros: "500000",
    rateLimit1dUsdMicros: "1000000",
    rateLimit7dUsdMicros: "4000000",
    usage5hUsdMicros: "100000",
    usage1dUsdMicros: "200000",
    usage7dUsdMicros: "300000",
    enabled: true,
    updatedAt: "2026-08-19T01:02:03Z"
  };
  globalThis.fetch = async (input, init) => {
    requests.push({ path: String(input), init });
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "available",
      available: true,
      fetchedAt: "2026-08-19T01:02:04Z",
      data: init?.method === "PATCH" ? { ...current, status: "disabled", enabled: false } : current
    }), { status: 200, headers: { "content-type": "application/json" } });
  };

  try {
    const readback = await workspaceApi.getWorkspaceGatewayBudget("workspace / alpha", "9223372036854775807");
    const expectedPayload = {
      quotaUsdMicros: 9_000_000,
      rateLimit5hUsdMicros: 500_000,
      rateLimit1dUsdMicros: 1_000_000,
      rateLimit7dUsdMicros: 4_000_000,
      enabled: false,
      resetQuota: true,
      resetRateLimitUsage: true
    };
    const input = { ...expectedPayload, name: "must-not-pass", groupId: "must-not-pass" };
    const updated = await workspaceApi.updateWorkspaceGatewayBudget(
      "workspace / alpha",
      "9223372036854775807",
      input,
      "csrf-budget",
      "workspace-budget:opaque"
    );

    assert.equal(readback.available && readback.data.quotaUsdMicros, "9007199254740993");
    assert.equal(updated.available && updated.data.enabled, false);
    assert.deepEqual(requests.map(({ path }) => path), [
      "/api/workspaces/workspace%20%2F%20alpha/gateway-budget",
      "/api/workspaces/workspace%20%2F%20alpha/gateway-budget"
    ]);
    assert.equal(requests[0].init?.method, undefined);
    assert.equal(requests[1].init?.method, "PATCH");
    assert.equal(new Headers(requests[1].init?.headers).get("content-type"), "application/json");
    assert.equal(new Headers(requests[1].init?.headers).get("x-opl-csrf"), "csrf-budget");
    assert.equal(new Headers(requests[1].init?.headers).get("Idempotency-Key"), "workspace-budget:opaque");
    assert.deepEqual(JSON.parse(String(requests[1].init?.body)), expectedPayload);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace Gateway budget adapter rejects source, identity, field, and micros drift", async () => {
  const base = {
    workspaceId: "ws-alpha",
    keyId: "19",
    status: "active",
    quotaUsdMicros: "9000000",
    quotaUsedUsdMicros: "3000000",
    rateLimit5hUsdMicros: "500000",
    rateLimit1dUsdMicros: "1000000",
    rateLimit7dUsdMicros: "4000000",
    usage5hUsdMicros: "100000",
    usage1dUsdMicros: "200000",
    usage7dUsdMicros: "300000",
    enabled: true,
    updatedAt: "2026-08-19T01:02:03Z"
  };
  const cases = [
    { source: "control-plane", data: base },
    { source: "sub2api", data: { ...base, workspaceId: "ws-other" } },
    { source: "sub2api", data: { ...base, keyId: "20" } },
    { source: "sub2api", data: { ...base, quotaUsdMicros: 9_000_000 } },
    { source: "sub2api", data: { ...base, quotaUsdMicros: "9223372036854775808" } },
    { source: "sub2api", data: { ...base, unexpected: true } }
  ];

  for (const item of cases) {
    globalThis.fetch = async () => new Response(JSON.stringify({
      source: item.source,
      status: "available",
      available: true,
      fetchedAt: "2026-08-19T01:02:04Z",
      data: item.data
    }), { status: 200, headers: { "content-type": "application/json" } });
    await assert.rejects(
      workspaceApi.getWorkspaceGatewayBudget("ws-alpha", "19"),
      /invalid_workspace_gateway_budget_source/
    );
  }
});

test("unavailable source adapters preserve the authoritative reason code", () => {
  const source = decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    sourceUpdatedAt: "2026-08-01T23:59:59Z",
    reasonCode: "sub2api_unavailable"
  });

  assert.deepEqual(source, {
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    sourceUpdatedAt: "2026-08-01T23:59:59Z",
    reasonCode: "sub2api_unavailable"
  });
});

test("unavailable source adapters reject a missing reason code", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z"
  }), /invalid_source_envelope/);
});

test("source adapters reject contradictory availability states", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "available",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable"
  }), /invalid_source_envelope/);
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: true,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable"
  }), /invalid_source_envelope/);
});

test("unavailable source adapters reject data disguised as an unavailable source", () => {
  assert.throws(() => decodeSource({
    source: "sub2api",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z",
    reasonCode: "sub2api_unavailable",
    data: { total: 0, items: [] }
  }), /invalid_source_envelope/);
});

test("source adapters require non-empty source and fetchedAt fields", () => {
  for (const input of [
    { source: "", status: "empty", available: true, fetchedAt: "2026-08-02T00:00:00Z", data: [] },
    { source: "sub2api", status: "empty", available: true, fetchedAt: "", data: [] }
  ]) {
    assert.throws(() => decodeSource(input), /invalid_source_envelope/);
  }
});

test("local unavailable fallbacks keep a stable reason code and a real fetch timestamp", () => {
  const fallback = unavailableSource("Control Plane + Ledger");
  assert.equal(fallback.status, "unavailable");
  assert.equal(fallback.reasonCode, "control_plane_ledger_unavailable");
  assert.ok(fallback.fetchedAt);
  assert.ok(Number.isFinite(Date.parse(fallback.fetchedAt)));
});

test("Console read adapters normalize a legacy unavailable envelope with a stable reason code", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async () => new Response(JSON.stringify({
    source: "control-plane+fabric+ledger",
    status: "unavailable",
    available: false,
    fetchedAt: "2026-08-02T00:00:00Z"
  }), { status: 502, headers: { "content-type": "application/json" } })) as typeof fetch;

  try {
    assert.deepEqual(await readApi.getBillingReceipts(), {
      source: "control-plane+fabric+ledger",
      status: "unavailable",
      available: false,
      fetchedAt: "2026-08-02T00:00:00Z",
      reasonCode: "control_plane_fabric_ledger_unavailable"
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("per-Key usage list and summary send the same canonical period", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    requested.push(String(input));
    return new Response(JSON.stringify({
      source: "sub2api",
      status: "empty",
      available: true,
      fetchedAt: "2026-08-02T00:00:00Z",
      data: { items: [], total: 0, page: 1, pageSize: 20, pages: 1 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    await readApi.getGatewayKeyUsage("key / 1", 1, 20, "today");
    await readApi.getGatewayKeyUsageSummary("key / 1", "today");
    assert.deepEqual(requested, [
      "/api/gateway/keys/key%20%2F%201/usage?page=1&pageSize=20&period=today",
      "/api/gateway/keys/key%20%2F%201/usage-summary?period=today"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace adapters find an exact ID through real server pagination and stop when found", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const path = String(input);
    requested.push(path);
    const page = Number(new URL(path, "https://console.invalid").searchParams.get("page"));
    const item = (id: string) => ({
      id, ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active",
      createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z"
    });
    const data = {
      items: page === 1 ? [item("ws-1")] : page === 2 ? [item("ws / alpha")] : [item("ws-3")],
      total: 3,
      page,
      pageSize: 1
    };
    return new Response(JSON.stringify({
      source: "control-plane", status: "available",
      available: true, fetchedAt: "2026-07-01T00:00:00Z", data
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    const detail = await workspaceApi.findWorkspaceInPages("ws / alpha", 1);
    assert.equal(detail.available && detail.data.id, "ws / alpha");
    assert.deepEqual(requested, [
      "/api/workspaces?page=1&pageSize=1",
      "/api/workspaces?page=2&pageSize=1"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Workspace adapters return an authoritative not-found result after total is exhausted", async () => {
  const requested: string[] = [];
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: string | URL | Request) => {
    const path = String(input);
    requested.push(path);
    const page = Number(new URL(path, "https://console.invalid").searchParams.get("page"));
    const items = page === 1
      ? [{ id: "ws-1", ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active", createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z" }]
      : [{ id: "ws-2", ownerAccountId: "acct-1", ownerUserId: "user-1", state: "active", createdAt: "2026-07-01T00:00:00Z", updatedAt: "2026-07-01T00:00:00Z" }];
    return new Response(JSON.stringify({
      source: "control-plane", status: "available", available: true,
      fetchedAt: "2026-07-01T00:00:00Z", data: { items, total: 2, page, pageSize: 1 }
    }), { status: 200, headers: { "content-type": "application/json" } });
  }) as typeof fetch;

  try {
    const detail = await workspaceApi.findWorkspaceInPages("ws-missing", 1);
    assert.equal(detail.available, true);
    assert.equal(detail.status, "empty");
    assert.equal(detail.data, null);
    assert.deepEqual(requested, [
      "/api/workspaces?page=1&pageSize=1",
      "/api/workspaces?page=2&pageSize=1"
    ]);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("Console production and browser fixtures do not invent a Workspace detail GET", async () => {
  const [workspaceSource, controller, browserFixture, backendRoutes] = await Promise.all([
    source("apps/console-ui/src/api/workspaces-api.ts"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("tools/console-browser-qa.ts"),
    source("services/control-plane/internal/server/routes_workspace.go")
  ]);

  assert.doesNotMatch(workspaceSource, /getWorkspace\s*\(/);
  assert.doesNotMatch(controller, /\bgetWorkspace\s*\(/);
  assert.doesNotMatch(browserFixture, /path\.match\(\/\^\\\/api\\\/workspaces\\\/\([^)]*\)\\\/\$\//);
  assert.doesNotMatch(backendRoutes, /HandleFunc\("GET \/api\/workspaces\/\{workspaceId\}"/);
});

test("Workspace detail is found before runtime status and renders authoritative not-found", async () => {
  const [controller, pages] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/pages/CustomerPages.tsx")
  ]);

  assert.match(controller, /const detail = await findWorkspaceInPages\(workspaceId\)/);
  assert.match(controller, /if \(!detail\.available \|\| detail\.data === null\)[\s\S]+return;[\s\S]+getWorkspaceRuntimeStatus\(workspaceId\)/);
  assert.match(pages, /workspaceSource\?\.available && workspaceSource\.data === null/);
  assert.match(pages, /Workspace 不存在/);
});

test("API request records expose the approved six-column facts", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const usageView = pages.slice(pages.indexOf("function UsageTokenFacts"), pages.indexOf("function ApiPage"));

  for (const label of ["模型 / 端点", "Token", "费用", "延迟", "时间", "请求 ID", "输入", "输出", "缓存读取", "缓存写入", "首字", "总耗时"]) {
    assert.match(usageView, new RegExp(label));
  }
  for (const field of ["actualCostUsdMicros", "firstTokenMs", "durationMs"]) assert.match(usageView, new RegExp(field));
  assert.doesNotMatch(usageView, /请求类型|查看详情|standardCost|accountCost|costMultiplier/);
});

test("customer UI has one launch entry and no internal service or resource vocabulary", async () => {
  const [pages, shell, controller] = await Promise.all([
    source("apps/console-ui/src/pages/CustomerPages.tsx"),
    source("apps/console-ui/src/layout/ConsoleShell.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts")
  ]);
  const customerSurface = `${pages}\n${shell}`;
  assert.match(controller, /launchWorkspace/);
  assert.doesNotMatch(controller, /createComputeAllocation|createStorageVolume|attachStorage|buyCompute|buyStorage|mountStorage/);
  assert.doesNotMatch(customerSurface, /CVM|CBS|ComputeAllocation|StorageVolume|StorageAttachment|raw Sub2API|raw Ledger/);
  assert.doesNotMatch(customerSurface, /Control Plane 总数|Fabric Runtime 实时状态|Ledger cursor 分页/);
  assert.doesNotMatch(customerSurface, /gflabtoken\.cn|iframe/);
  assert.match(customerSurface, /API 服务/);
  assert.match(customerSurface, /暂不可用/);
});

test("critical frontend contracts use named DTOs instead of AnyRecord", async () => {
  const [dto, readApiSource, workspaceSource] = await Promise.all([
    source("apps/console-ui/src/api/dtos.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/workspaces-api.ts")
  ]);
  for (const name of [
    "WorkspaceLaunchRequest", "WorkspaceLaunchResponse", "WorkspaceRenewalResponse",
    "RuntimeCredentialResponse", "WorkspaceRuntimeDTO", "GatewayWallet", "GatewayKey",
    "GatewayKeySecretDTO", "GatewayUsageItem"
  ]) assert.match(dto, new RegExp(`interface ${name}\\b`));
  assert.match(dto, /type SourceEnvelope\b/);
  assert.doesNotMatch(dto, /interface (GatewayKeyReveal|WorkspaceRuntimeStatus)\b/);
  assert.doesNotMatch(readApiSource, /AnyRecord|Record<string, any>|map\[string\]any/);
  assert.doesNotMatch(workspaceSource, /AnyRecord|Record<string, any>|map\[string\]any/);
});

test("Workspace launch requires the authoritative total price and provider catalog package", async () => {
  const [controller, pages] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/pages/CustomerPages.tsx")
  ]);
  assert.match(controller, /selectedPrice/);
  assert.match(controller, /selectedPlan\.id/);
  assert.doesNotMatch(controller, /selectedPlan\.id === "basic" \? 10 : 100/);
  assert.doesNotMatch(controller, /selectedPlan\.(?:cpu|memoryGb|diskGb|server)/);
  assert.doesNotMatch(controller, /sizeGb:\s*selectedPlan\.id ===/);
  assert.match(pages, /preview\.totalChargeUsdMicros/);
  assert.match(pages, /preview\.compute/);
  assert.match(pages, /preview\.storage/);
});

test("an unavailable launch catalog is explicit and retryable", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const launchView = pages.slice(pages.indexOf("function WorkspaceLaunchPage"), pages.indexOf("function WorkspaceLaunchConfirm"));
  assert.match(launchView, /sources\.catalog\.error/);
  assert.match(launchView, /计划与价格暂不可用/);
  assert.match(launchView, /refreshCurrentPage/);
});

test("operator account rows do not render the raw internal source identifier", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  assert.doesNotMatch(pages, /operatorAccounts\.value\?\.source|accountsSource\.source/);
});
