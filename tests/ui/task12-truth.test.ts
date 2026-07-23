import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";
import * as workspaceApi from "../../apps/console-ui/src/api/workspaces-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Task 12 exposes typed source adapters for the customer truth surfaces", async () => {
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
  assert.equal(typeof workspaceApi.getWorkspaceRuntimeStatus, "function");
  assert.equal(typeof workspaceApi.revealWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.rotateWorkspaceCredentials, "function");
  assert.equal(typeof workspaceApi.updateWorkspaceRenewal, "function");
});

test("customer UI has one launch entry and no internal service or resource vocabulary", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const template = app.slice(app.indexOf("<template>"));
  assert.match(app, /launchWorkspace/);
  assert.doesNotMatch(app, /createComputeAllocation|createStorageVolume|attachStorage|buyCompute|buyStorage|mountStorage/);
  assert.doesNotMatch(app, /getGatewaySummary|summary\?reveal=true|gflabtoken\.cn|iframe/);
  assert.doesNotMatch(template, /Sub2API|Gateway|Fabric|Ledger|CVM|CBS|ComputeAllocation|StorageVolume|StorageAttachment|Mount/);
  assert.doesNotMatch(app, /fixedMonthlySpend|workspaceMonthlyPrice|renewalSummary|state\.value\?\.balance/);
  assert.doesNotMatch(app, /receipt\.status\s*\|\||未知.*处理中|\.find\([^\n]+\)\s*\|\|[^\n]*\[0\]/);
  assert.match(app, /API 服务/);
  assert.match(app, /暂不可用/);
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

test("historical design evidence is explicitly superseded", async () => {
  const designQA = await source("design-qa.md");
  assert.match(designQA, /historical|superseded/i);
  assert.match(designQA, /task12-freeze-v2/);
});

test("Workspace launch requires the authoritative total price and fixed SKU size pair", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /selectedPlanPrice/);
  assert.match(app, /plan\.id === "basic" \? 10 : 100/);
  assert.doesNotMatch(app, /plan\.diskGb === 10 \? 10 : 100/);
  assert.match(app, /typeof workspace\.totalUsdMicros === "number"/);
});

test("an unavailable launch catalog is explicit and retryable", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /errors\.catalog && !workspace/);
  assert.match(app, /计划与价格暂不可用/);
  assert.match(app, /@click="loadCatalog"/);
});

test("operator account rows do not render the raw internal source identifier", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.doesNotMatch(app, /\{\{\s*accountsSource\.source\s*\}\}/);
});
