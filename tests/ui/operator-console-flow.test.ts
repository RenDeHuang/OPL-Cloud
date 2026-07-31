import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import * as readApi from "../../apps/console-ui/src/api/console-read-api.ts";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8");

test("Operator Console surfaces use the named read adapters", () => {
  for (const name of [
    "getOperatorOverview",
    "getOperatorAccountsPage",
    "getOperatorWorkspaces",
    "getOperatorWorkspace",
    "getOperatorReconciliation",
    "getOperatorHealth",
    "getOperatorAnnouncements"
  ] as const) assert.equal(typeof readApi[name], "function", `${name} adapter is required`);
});

test("operator UI renders owner-scoped resource facts without secrets", async () => {
  const [pages, dtos] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  for (const label of [
    "owner Account", "owner User", "Workspace", "资源类型", "套餐 / 规格",
    "provider ID", "Zone", "创建时间", "到期时间", "最近 provider 读回",
    "operation reference", "Receipt reference", "系统状态", "公告管理"
  ]) assert.match(pages, new RegExp(label, "i"), `${label} must be visible`);
  assert.match(dtos, /interface OperatorResourceDTO\b/);
  assert.match(dtos, /resources: OperatorResourceDTO\[\]/);
  assert.doesNotMatch(pages, /rawKey|rawResponse|secretRef|providerSecret|resource\.password|workspace\?\.password/);
});

test("operator workspace rows expose authoritative product and lifecycle facts", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  for (const label of ["套餐 / 月度总价", "创建时间", "paidThrough", "续费状态", "生命周期状态", "URL"]) {
    assert.match(pages, new RegExp(label));
  }
  for (const field of ["packageId", "totalUsdMicros", "createdAt", "paidThrough", "renewalStatus", "url"]) {
    assert.match(pages, new RegExp(`value\\.${field}|workspace\\?\\.${field}`));
  }
});

test("operator balance operation is confirmed, idempotent, and reviewable", async () => {
  const [pages, controller, api, consoleApi] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/console-api.ts")
  ]);
  const joined = [pages, controller, api, consoleApi].join("\n");
  for (const token of ["wallet-adjustments", "Idempotency-Key", "confirmationAccountId", "manual_review"]) assert.match(joined, new RegExp(token));
  assert.match(controller, /请再次确认这笔余额操作/);
  assert.match(pages, /title="余额操作"/);
  assert.match(pages, /再次确认 Account ID/);
  assert.match(pages, /结果：/);
  assert.doesNotMatch(joined, /createUser\([^)]*sub2apiUserId/);
});

test("wallet adjustment readback exposes non-secret audit and recovery facts", async () => {
  const [pages, controller, dtos, api] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/dtos.ts"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  for (const label of ["调整前余额", "调整后余额", "原因", "关联操作", "余额历史引用", "Receipt ID", "errorCode", "requestId", "actor", "allowedActions"]) {
    assert.match(pages, new RegExp(label));
  }
  assert.equal(typeof readApi.recoverWalletAdjustment, "function");
  assert.match(api, /\/api\/operator\/wallet-adjustments\/.*\/recover/);
  assert.match(dtos, /interface WalletAdjustmentRecoveryRequest\b/);
  assert.match(controller, /allowedActions\?\.includes\("recover_wallet_adjustment"\)/);
  assert.match(controller, /walletRecoveryIdempotencyKey\(operation\.operationId\)/);
  assert.match(controller, /window\.prompt\("请输入 case-YYYYMMDD-xxx 证据引用"\)/);
  const upstream = dtos.match(/export interface WalletAdjustmentUpstreamFailureDTO[\s\S]*?\n}/)?.[0] || "";
  assert.doesNotMatch(upstream, /(?:message|rawBody):/);
});

test("operator mutations retain stable intents until authoritative success", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  for (const intent of [
    "operatorProvisionIntent", "operatorDisableIntents", "walletAdjustmentIntent",
    "walletAdjustmentRecoveryIntent", "billingReviewIntent", "workspaceLaunchRecoveryIntent",
    "announcementCreateIntent", "announcementPublishIntents", "announcementWithdrawIntents"
  ]) assert.match(controller, new RegExp(intent), `${intent} must preserve one mutation identity`);
  assert.match(controller, /mutationError/);
  assert.match(controller, /结果待确认/);
});

test("account provisioning completes only after authoritative identity readback", async () => {
  const [pages, controller] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts")
  ]);
  const modal = pages.slice(pages.indexOf("function ProvisionAccountModal"), pages.indexOf("function AccountDetailModal"));
  const mutation = controller.slice(controller.indexOf("const provisionAccount"), controller.indexOf("const resolveReview"));
  assert.match(mutation, /findOperatorAccountByEmail/);
  assert.match(mutation, /authoritativeAccount/);
  assert.match(modal, /operation\.account/);
  assert.match(modal, /setCompleted\(Boolean\(operation\.account\)\)/);
  assert.doesNotMatch(modal, /当前分页未返回映射/);
});

test("operator announcement publish preserves the saved schedule", async () => {
  const controller = await source("apps/console-ui/src/app/use-console-controller.ts");
  assert.match(controller, /announcement\.startsAt \|\| new Date\(\)\.toISOString\(\)/);
  assert.match(controller, /endsAt: announcement\.endsAt \|\| ""/);
});

test("operator accounts expose active and disabled states through the disable command", async () => {
  const [pages, controller, api] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  assert.match(pages, /开通用户/);
  assert.doesNotMatch(pages, /邀请用户/);
  assert.match(pages, /active: "正常"/);
  assert.match(pages, /disabled: "已停用"/);
  assert.match(controller, /确认停用该客户/);
  assert.match(controller, /disableOperatorAccountCommand\(accountId, "operator_requested"/);
  assert.match(controller, /客户已停用/);
  assert.match(pages, /const reservedAdmin = account\.accountId === "acct-admin" \|\| account\.role === "admin"/);
  assert.match(pages, /reservedAdmin[\s\S]+保留管理员账户仅查看/);
  assert.match(pages, /account\.status === "active"[\s\S]+disableOperatorAccount\(account\.accountId\)/);
  assert.match(pages, /账户已停用/);
  assert.match(api, /\/api\/operator\/accounts\/\$\{encodeURIComponent\(accountId\)\}\/disable/);
  assert.doesNotMatch(`${pages}\n${controller}\n${api}`, /删除客户|客户已删除|删除账号|deleteAccount|\/api\/operator\/accounts\/[^"']+\/delete/);
});

test("operator accounts use the frozen seven-column scan hierarchy", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  const accountDetail = pages.slice(pages.indexOf("function AccountDetailModal"), pages.indexOf("function WalletOperationReadback"));
  const accountsPage = pages.slice(pages.indexOf("function AccountsPage"), pages.indexOf("function ReviewDetails"));
  const mobileCard = pages.slice(pages.indexOf("function OperatorAccountMobileCard"), pages.indexOf("function ProvisionAccountModal"));

  assert.match(accountsPage, /<th>用户<\/th><th>账户映射<\/th><th>余额<\/th><th>API 费用<\/th><th>资源<\/th><th>状态<\/th><th>操作<\/th>/);
  for (const label of ["OPL Account", "Console User", "Sub2API User", "今日", "累计", "Key", "Workspace"]) {
    assert.match(pages, new RegExp(label));
  }
  assert.match(accountsPage, /className="account-mapping-stack"/);
  assert.match(accountsPage, /className="account-resource-stack"/);
  assert.doesNotMatch(accountsPage, /<AccountSourceSummary/);
  assert.match(accountDetail, /<AccountSourceSummary/);
  assert.match(mobileCard, /账户映射[\s\S]+余额[\s\S]+API 费用[\s\S]+资源/);
  assert.doesNotMatch(accountsPage, /搜索|排序|search|sort/i);
});

test("operator pages do not expose DTO, projection, or browser paging implementation notes", async () => {
  const pages = await source("apps/console-ui/src/pages/AdminPages.tsx");
  assert.doesNotMatch(pages, /["'`][^"'`\n]*(?:DTO|projection|投影|页面不下载其他分页|当前控制器未提供)[^"'`\n]*["'`]/);
});

test("operator accounts and workspaces expose server-side pagination", async () => {
  const [pages, controller, api] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/console-read-api.ts")
  ]);
  assert.match(api, /getOperatorAccountsPage\(page = 1, pageSize = 20/);
  assert.match(api, /getOperatorWorkspaces\(page = 1, pageSize = 20/);
  assert.match(controller, /operatorAccountPage/);
  assert.match(controller, /operatorWorkspacePage/);
  assert.match(pages, /label="账号分页"/);
  assert.match(pages, /label="Workspace 分页"/);
});

test("operator billing review executes only server-allowed actions", async () => {
  const [pages, controller, api, dtos] = await Promise.all([
    source("apps/console-ui/src/pages/AdminPages.tsx"),
    source("apps/console-ui/src/app/use-console-controller.ts"),
    source("apps/console-ui/src/api/console-read-api.ts"),
    source("apps/console-ui/src/api/dtos.ts")
  ]);
  assert.equal(typeof readApi.recoverWorkspaceLaunch, "function");
  assert.match(api, /\/api\/operator\/workspace-launches\/.*\/recover/);
  assert.match(api, /\/api\/operator\/billing-reviews\/.*\/resolve/);
  for (const field of ["accountId", "billingOperationId", "phase", "errorCode", "allowedActions"]) assert.match(dtos, new RegExp(`${field}[?]?:`));
  assert.match(controller, /if \(!review\.allowedActions\.includes\("recover_workspace_launch"\) && !review\.allowedActions\.includes\("resolve_billing_review"\)\) return/);
  assert.match(controller, /review\.allowedActions\.includes\("recover_workspace_launch"\)/);
  assert.match(controller, /review\.allowedActions\.includes\("resolve_billing_review"\)/);
  assert.match(pages, /const actionable = review\.allowedActions\.includes/);
  assert.match(pages, /无自动修复动作/);
  assert.doesNotMatch(controller, /case-20260720-review/);
});
