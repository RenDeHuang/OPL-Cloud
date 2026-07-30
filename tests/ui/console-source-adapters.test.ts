import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
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
  assert.equal(typeof workspaceApi.revealWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.rotateWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.updateWorkspaceRenewal, "function");
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

test("API request records expose only the approved five fields", async () => {
  const pages = await source("apps/console-ui/src/pages/CustomerPages.tsx");
  const usageView = pages.slice(pages.indexOf("function RequestRows"), pages.indexOf("function ApiPage"));

  for (const label of ["时间", "模型", "端点", "实际金额", "请求编号"]) assert.match(usageView, new RegExp(label));
  assert.doesNotMatch(usageView, /输入 Token|输出 Token|缓存写入 Token|缓存读取 Token|请求类型|查看详情/);
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

test("Workspace launch requires the authoritative total price and fixed SKU size pair", async () => {
  const [controller, pages] = await Promise.all([
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/pages/CustomerPages.tsx")
  ]);
  assert.match(controller, /selectedPrice/);
  assert.match(controller, /selectedPlan\.id === "basic" \? 10 : 100/);
  assert.doesNotMatch(controller, /selectedPlan\.diskGb === 10 \? 10 : 100/);
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
