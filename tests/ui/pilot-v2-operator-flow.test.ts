import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Pilot V2 operator surfaces use the named read adapters", async () => {
  for (const name of [
    "getOperatorOverview",
    "getOperatorAccountsPage",
    "getOperatorWorkspaces",
    "getOperatorWorkspace",
    "getOperatorReconciliation",
    "getOperatorHealth",
    "getOperatorAnnouncements",
  ] as const) {
    assert.equal(typeof readApi[name], "function", `${name} adapter is required`);
  }
});

test("operator UI renders owner-scoped resource facts and safe health states", async () => {
  const [app, dtos] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  const template = app.slice(app.indexOf("<template>"));
  const operatorView = template.slice(template.indexOf('<div v-if="isAdminRoute" class="page-content">'), template.indexOf('<div v-else class="page-content"'));
  for (const label of [
    "owner account", "owner user", "Workspace", "资源类型", "套餐/规格",
    "provider ID", "Zone", "创建时间", "到期时间", "最近读回时间", "operation", "Receipt",
    "健康", "公告"
  ]) assert.match(template, new RegExp(label, "i"), `${label} must be visible`);
  assert.match(dtos, /interface OperatorResourceDTO\b/);
  assert.match(dtos, /resources: OperatorResourceDTO\[\]/);
  assert.doesNotMatch(operatorView, /rawKey|password|token|Sub2API|Fabric|Ledger|Runtime/);
});

test("operator workspace rows expose available product and lifecycle facts", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const operatorView = app.slice(app.indexOf('<div v-if="isAdminRoute" class="page-content">'), app.indexOf('<div v-else class="page-content"'));
  for (const label of ["套餐", "月价", "创建时间", "有效期", "续费状态", "URL"]) {
    assert.match(operatorView, new RegExp(`<th>${label}</th>`));
  }
  for (const field of ["packageId", "totalUsdMicros", "createdAt", "paidThrough", "renewalStatus", "url"]) {
    assert.match(operatorView, new RegExp(`data\\.${field}`));
  }
});

test("operator wallet adjustment is confirmed, idempotent, and reviewable", async () => {
  const [app, readApiSource] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  for (const token of ["wallet-adjustments", "Idempotency-Key", "confirmationAccountId", "manual_review"]) {
    assert.match(`${app}\n${readApiSource}`, new RegExp(token));
  }
  assert.match(app, /二次确认/);
  assert.match(app, /结果待确认/);
  assert.doesNotMatch(app, /createUser\([^)]*sub2apiUserId/);
});

test("wallet adjustment readback shows non-secret audit facts", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const modal = app.slice(app.indexOf('<div v-if="modal"'), app.indexOf('<div v-if="toast.text"'));
  for (const label of ["调整前余额", "调整后余额", "原因", "关联操作", "余额记录引用", "执行人"]) {
    assert.match(modal, new RegExp(label));
  }
  assert.match(modal, /walletAdjustmentOperation\.beforeBalance/);
  assert.match(modal, /walletAdjustmentOperation\.afterBalance/);
});

test("operator mutations retain stable intents across unknown retries", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  for (const intent of [
    "operatorProvisionIntent",
    "operatorDisableIntents",
    "billingReviewIntent",
    "workspaceLaunchRecoveryIntent",
    "announcementCreateIntent",
    "announcementPublishIntents",
    "announcementWithdrawIntents"
  ]) assert.match(app, new RegExp(intent), `${intent} must preserve one mutation identity`);
  assert.match(app, /结果待确认[^\n]+不要重复提交/);
});

test("operator announcement publish preserves the saved schedule", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /announcement\.startsAt \|\| new Date\(\)\.toISOString\(\)/);
  assert.match(app, /endsAt: announcement\.endsAt \|\| ""/);
});

test("operator accounts provision and disable without delete", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  assert.match(app, /开通用户/);
  assert.doesNotMatch(app, /邀请用户/);
  assert.match(app, /禁用/);
  assert.doesNotMatch(app, /删除账号|deleteAccount|\/api\/operator\/accounts\/[^"']+\/delete/);
});

test("operator accounts and workspaces expose server-side pagination", async () => {
  const [app, readApi] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  assert.match(readApi, /getOperatorAccountsPage\(page = 1, pageSize = 20/);
  assert.match(readApi, /getOperatorWorkspaces\(page = 1, pageSize = 20/);
  assert.match(app, /operatorAccountPage/);
  assert.match(app, /operatorWorkspacePage/);
  assert.match(app, /changeOperatorAccountPage/);
  assert.match(app, /changeOperatorWorkspacePage/);
  assert.match(app, /aria-label="账号分页"/);
  assert.match(app, /aria-label="Workspace 分页"/);
});

test("operator launch review uses the dedicated recovery command", async () => {
  const [app, readApiSource, dtos] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  assert.equal(typeof readApi.recoverWorkspaceLaunch, "function");
  assert.match(readApiSource, /\/api\/operator\/workspace-launches\/.*\/recover/);
  for (const field of ["accountId", "billingOperationId", "phase", "errorCode", "allowedActions"]) {
    assert.match(dtos, new RegExp(`${field}[?]?:`));
  }
  assert.match(app, /allowedActions\.includes\("recover_workspace_launch"\)/);
  assert.match(app, /recoverWorkspaceLaunch\(/);
  assert.match(app, /idempotencyKey: `recover-\$\{crypto\.randomUUID\(\)\}`/);
  assert.match(app, /item\.status === ['"]manual_review['"]/);
});
