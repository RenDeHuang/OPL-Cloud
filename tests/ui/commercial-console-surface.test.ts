import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("commercial Console UI is built from the maintained surface component layer", async () => {
  const surfaceSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");

  for (const exportName of [
    "ConsoleSurface",
    "MetricStrip",
    "InsightPanel",
    "StatusPill",
    "ResourceSplit",
    "ActionGroup",
    "TimelineList",
    "ObjectTable",
    "PriceImpactPanel",
    "OperationTimeline",
    "FailureRecoveryPanel",
    "OperationConfirmButton",
    "OperationResultPanel",
    "CleanupResourceTable",
    "ResourceRelationshipGraph",
    "WalletRiskPanel",
    "DataRetentionPolicyPanel",
    "ProductionE2EPanel"
  ]) {
    assert.match(surfaceSource, new RegExp(`export function ${exportName}\\b`), `${exportName} must be exported by the commercial UI layer`);
  }
});

test("business-chain pages use the commercial surface instead of old card stacks", async () => {
  for (const page of [
    "apps/console-ui/src/pages/OverviewPage.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx",
    "apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx",
    "apps/console-ui/src/pages/billing/BillingPage.tsx",
    "apps/console-ui/src/pages/gateway/GatewayPage.tsx",
    "apps/console-ui/src/pages/account/AccountPage.tsx",
    "apps/console-ui/src/pages/support/SupportPage.tsx",
    "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx"
  ]) {
    const pageSource = await source(page);
    assert.match(pageSource, /shared\/commercial-console\.tsx/, `${page} must import the commercial Console surface`);
    assert.doesNotMatch(pageSource, /StatisticCard/, `${page} must not use the old metric card layer directly`);
  }
});

test("public entry is Console-first and does not use the retired marketing hero shell", async () => {
  const homeSource = await source("apps/console-ui/src/pages/HomePage.tsx");
  assert.match(homeSource, /publicConsole/, "public home should present the Console product surface");
  assert.doesNotMatch(homeSource, /homeHero|heroPreview|chainPreview/, "retired marketing hero classes must stay removed");
});

test("public entry visible copy is concise Chinese without old English labels", async () => {
  const homeSource = await source("apps/console-ui/src/pages/HomePage.tsx");
  for (const chineseCopy of [
    "业务链",
    "钱包",
    "余额与冻结",
    "计算与存储",
    "访问入口",
    "用量证据",
    "充值",
    "开通",
    "分发 URL",
    "计费"
  ]) {
    assert.match(homeSource, new RegExp(chineseCopy), `HomePage must show ${chineseCopy}`);
  }
  assert.doesNotMatch(homeSource, /(?:>|label=|value=|<h2>)(?:"?)(Business chain|Live Console|Wallet|Balance \+ holds|Workspace|Compute \+ storage|Scoped access|Usage evidence|Top up|Create|Share URL|Meter|Lab Owner|Billing|Admin)/, "public home must not retain English product labels");
});

test("authenticated shell is branded as OPL Console", async () => {
  const shellSource = await source("apps/console-ui/src/pages/ConsolePage.tsx");
  assert.match(shellSource, /title="OPL Console"/, "authenticated app shell must use OPL Console product naming");
  assert.doesNotMatch(shellSource, /title="OPL Cloud"/, "authenticated app shell must not retain old OPL Cloud naming");
  assert.doesNotMatch(shellSource, /Lab Owner|>Admin</, "authenticated app shell must not show raw internal role labels");
});

test("Console chrome uses the one-person-lab-app logo mark", async () => {
  const logoSource = await source("apps/console-ui/src/pages/shared/OplAppLogo.tsx");
  for (const page of [
    "apps/console-ui/src/pages/HomePage.tsx",
    "apps/console-ui/src/pages/LoginPage.tsx",
    "apps/console-ui/src/pages/ConsolePage.tsx"
  ]) {
    const pageSource = await source(page);
    assert.match(pageSource, /OplAppLogo/, `${page} must render the shared app logo`);
  }
  assert.match(logoSource, /one-person-lab-app logo/, "logo mark must identify the one-person-lab-app runtime brand");
});

test("visible app chrome does not retain old Cloud or reserved backlog copy", async () => {
  for (const page of [
    "apps/console-ui/src/main.tsx",
    "apps/console-ui/src/pages/LoginPage.tsx",
    "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx"
  ]) {
    const pageSource = await source(page);
    assert.doesNotMatch(pageSource, /Loading OPL Cloud|> OPL Cloud</, `${page} must use OPL Console in visible chrome`);
    assert.doesNotMatch(pageSource, /status: "reserved"|value: "Backlog"|not in current launch/, `${page} must not show reserved/backlog product copy`);
  }
});

test("create Workspace flow is a single commercial submit action", async () => {
  const createSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");
  const stateSource = await source("apps/console-ui/src/store/console-state.ts");

  assert.doesNotMatch(createSource, /StepsForm/, "create flow must not hide provisioning behind a multi-step wizard");
  assert.match(createSource, /htmlType="submit"/, "create flow must expose one clear submit button");
  assert.match(createSource, /attachmentId/, "Workspace runtime binding must start from an attached compute/storage pair");
  assert.match(createSource, /const created = await runAction/, "create flow must inspect action success before navigating");
  assert.match(createSource, /if \(created\) navigate/, "create flow must not navigate away after failed provisioning");
  assert.match(stateSource, /return result \|\| true/, "runAction must return successful action payloads");
  assert.match(stateSource, /return false/, "runAction must report failed actions");
});

test("shared runAction suppresses duplicate submissions by action key", async () => {
  const stateSource = await source("apps/console-ui/src/store/console-state.ts");
  const workspaceListSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const workspaceDetailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");

  assert.match(stateSource, /pendingActions/, "runAction must track pending action keys");
  assert.match(stateSource, /actionKey/, "runAction must accept an action key");
  assert.match(stateSource, /current\.has\(actionKey\)/, "runAction must suppress duplicate action keys");
  assert.doesNotMatch(workspaceListSource, /workspace-reset-|workspace-delete-/, "Workspace list should not expose low-frequency reset/delete actions");
  assert.match(workspaceDetailSource, /actionKey: `workspace-reset-\$\{selected\.id\}`/, "Workspace detail reset must key duplicate suppression by Workspace");
  assert.match(workspaceDetailSource, /actionKey: `workspace-delete-\$\{selected\.id\}`/, "Workspace detail delete must key duplicate suppression by Workspace");
  assert.match(adminSource, /actionKey: "admin-manual-topup"/, "manual top-up must suppress duplicate submit");
  assert.match(adminSource, /actionKey: `admin-create-user-\$\{String\(values\.email \|\| ""\)\.toLowerCase\(\)\.trim\(\)\}`/, "user creation must suppress duplicate submit by email");
});

test("Workspace UI treats URL as stable storage subject with current runtime pointer", async () => {
  const listSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const detailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");
  const routeSource = await source("apps/console-ui/src/routes/opl-routes.ts");
  const cleanupSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");

  assert.match(listSource, /currentComputeAllocationId/, "Workspace list must show current compute pointer");
  assert.match(listSource, /workspace\.billing/, "Workspace list must use backend per-Workspace billing facts");
  assert.match(detailSource, /currentAttachmentId/, "Workspace detail must keep the current attachment pointer in code");
  assert.doesNotMatch(`${listSource}\n${detailSource}`, /UI 子账号|等同 UI 子账号/, "customer-facing Workspace copy must not expose the internal subaccount simplification");
  assert.match(routeSource, /currentComputeAllocationId/, "runtime route registry must expose current compute pointer");
  assert.match(routeSource, /currentAttachmentId/, "runtime route registry must expose current attachment pointer");
  assert.doesNotMatch(cleanupSource, /!compute \|\| compute\.status === "destroyed"/, "cleanup must not invalidate stable URL only because compute is suspended");
});

test("resource provisioning pages call resource APIs instead of disabled placeholders", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");

  for (const apiName of ["createComputeAllocation", "createStorageVolume", "attachStorage"]) {
    assert.match(resourceSource, new RegExp(`${apiName}\\(`), `resource UI must call ${apiName}`);
  }
  assert.doesNotMatch(resourceSource, /disabled: true/, "resource creation actions must not remain disabled placeholders");
});

test("create forms do not prefill demo-style resource names", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const workspaceSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");

  assert.doesNotMatch(resourceSource, /initialValues=\{\{ name:/, "resource creation forms must not prefill demo names");
  assert.doesNotMatch(workspaceSource, /initialValues=\{\{ workspaceName:/, "Workspace creation must not prefill a demo name");
  for (const demoCopy of ["实验工作区", "实验数据盘", "分析计算资源"]) {
    assert.doesNotMatch(resourceSource, new RegExp(demoCopy), `resource forms must not show ${demoCopy}`);
    assert.doesNotMatch(workspaceSource, new RegExp(demoCopy), `workspace forms must not show ${demoCopy}`);
  }
});

test("resource provisioning failures preserve provider details in the visible result", async () => {
  const stateSource = await source("apps/console-ui/src/store/console-state.ts");
  const apiSource = await source("apps/console-ui/src/api/console-api.ts");
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");

  assert.match(apiSource, /payload\.safeMessage \|\| payload\.error/, "API client must prefer safe provider messages");
  assert.match(stateSource, /returnFailure = false/, "runAction must keep false-return behavior by default");
  assert.match(stateSource, /await refresh\(\)/, "failed resource mutations must refresh state so failed resources stay visible");
  assert.match(stateSource, /failureReason: err\.message/, "runAction must expose caught provider errors to operation panels");
  assert.match(resourceSource, /returnFailure: true/g, "resource mutations must opt into visible failure envelopes");
  assert.doesNotMatch(resourceSource, /failureReason: "操作失败，请查看提示后重试。"/, "resource pages must not replace provider failures with generic copy");
});

test("resource provisioning UI shows price, hold, balance impact, and operation state", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const billingSource = await source("apps/console-ui/src/pages/billing/BillingPage.tsx");

  for (const forbiddenFormula of [
    "function computeHourlyPrice",
    "function computeHoldAmount",
    "function storageGbMonthPrice",
    "function storageHoldAmount",
    "price?.computeHourly",
    "price?.storageGbMonth"
  ]) {
    assert.doesNotMatch(resourceSource, new RegExp(forbiddenFormula.replace("?", "\\?")), `resource UI must not own pricing formula ${forbiddenFormula}`);
  }
  for (const requiredSignal of ["pricingPreview", "holdAmountCents", "walletAfterPreview", "operationId", "safeMessage"]) {
    assert.match(resourceSource, new RegExp(requiredSignal), `resource UI must expose ${requiredSignal}`);
  }
  assert.match(resourceSource, /¥\/小时|每小时/, "compute creation must show hourly pricing before submit");
  assert.match(resourceSource, /冻结|hold/i, "resource creation must show prepaid hold impact before submit");
  assert.match(resourceSource, /Form\.useWatch\("packageId"/, "resource creation must update pricing when package selection changes");
  assert.match(resourceSource, /PriceImpactPanel/, "resource creation must use the maintained pricing impact component");
  assert.match(resourceSource, /OperationTimeline/, "resource detail must show operation timeline evidence");
  assert.match(resourceSource, /FailureRecoveryPanel/, "resource detail must expose failed-operation recovery guidance");
  assert.match(resourceSource, /resource\.hourlyEstimate/, "storage detail must show hourlyEstimate instead of compute hourlyPrice");
  assert.match(billingSource, /state\.billingSummary/, "billing page must display backend billing summary");
  assert.doesNotMatch(billingSource, /function activeHourlyEstimate|item\.hourlyEstimate|item\.hourlyPrice/, "billing page must not calculate resource pricing locally");
});

test("Workspace pages display backend billing facts instead of deriving charges locally", async () => {
  const listSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const detailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");

  for (const sourceText of [listSource, detailSource]) {
    assert.match(sourceText, /(?:workspace|selected)\.billing/, "workspace pages must read backend workspace billing facts");
    assert.doesNotMatch(sourceText, /function workspaceChargeTotal|function workspaceHourlyEstimate|resourceDebitEvents\(state\)/, "workspace pages must not derive billing from ledger/resource rows");
  }
});

test("resource mutations use confirmation, waiting state, result receipts, and concise Chinese copy", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const surfaceSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");
  const actionsSource = await source("apps/console-ui/src/routes/opl-actions.ts");
  const routesSource = await source("apps/console-ui/src/routes/opl-routes.ts");
  const apiSource = await source("apps/console-ui/src/api/resources-api.ts");

  assert.match(surfaceSource, /Popconfirm/, "confirmed operation buttons must use Ant Design confirmation");
  assert.match(surfaceSource, /confirmText/, "strong destructive operations must show required confirmation text");
  assert.match(surfaceSource, /操作已提交|操作失败|操作完成/, "operation result copy must be Chinese");

  for (const marker of [
    "OperationConfirmButton",
    "OperationResultPanel",
    "operationPending",
    "operationResult",
    "确认开通计算资源",
    "确认开通存储资源",
    "确认挂载存储资源",
    "确认销毁计算资源",
    "确认销毁存储资源",
    "确认删除数据",
    "/data"
  ]) {
    assert.match(resourceSource, new RegExp(marker.replace("/", "\\/")), `resource page must include ${marker}`);
  }

  assert.doesNotMatch(resourceSource, /title="Create Compute"|title="Create Storage"|title="Attach Storage"|eyebrow="Provision"|eyebrow="Mount"/, "resource pages must not expose English operation titles");
  assert.doesNotMatch(resourceSource, /meta: "operationId"|meta: "providerRequestId"|meta: "safeMessage"|status: "after hold"|status: "billable"|status: "required"|status: "verified"|meta: "one-person-lab-app persistent state"/, "Lab Owner resource pages must not lead with implementation jargon");

  for (const actionId of [
    "compute-allocations.create",
    "compute-allocations.destroy",
    "storage.create",
    "storage.destroy",
    "attachment.create",
    "attachment.detach"
  ]) {
    assert.match(actionsSource, new RegExp(`id: "${actionId}"[\\s\\S]*mutation: true`), `${actionId} must declare mutation`);
    assert.match(actionsSource, new RegExp(`id: "${actionId}"[\\s\\S]*confirmation:`), `${actionId} must declare confirmation`);
    assert.match(actionsSource, new RegExp(`id: "${actionId}"[\\s\\S]*failureVisible: true`), `${actionId} must expose failures`);
  }

  assert.match(actionsSource, /requiredConfirmText: "确认删除数据"/, "storage destroy action must require strong Chinese confirmation");
  assert.match(routesSource, /operationProtocol/, "route registry must expose operation protocol metadata");
  assert.match(apiSource, /operationEnvelope/, "resource API client must normalize mutation responses");
  assert.match(resourceSource, /routeTo\("workspace\.create"/, "successful storage attachment must guide the owner to create the Workspace URL entry");
});

test("Admin cleanup route exposes orphan URL cleanup without redesigning the Console shell", async () => {
  const routesSource = await source("apps/console-ui/src/routes/opl-routes.ts");
  const routeRegistrySource = await source("apps/console-ui/src/routes/route-registry.tsx");
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const apiSource = await source("apps/console-ui/src/api/console-read-api.ts");

  assert.match(routesSource, /id: "admin\.cleanup"/, "admin cleanup must be a first-class route id");
  assert.match(routesSource, /path: "\/admin\/cleanup"/, "admin cleanup must have a refreshable route");
  assert.match(routesSource, /"POST \/api\/operator\/cleanup-workspace-access"/, "admin cleanup route must declare its cleanup API");
  assert.match(routeRegistrySource, /AdminCleanupPage/, "route registry must render the cleanup page through the route");
  assert.match(adminSource, /CleanupResourceTable/, "cleanup UI must use the maintained cleanup table component");
  assert.match(adminSource, /cleanupWorkspaceAccess/, "cleanup UI must call the cleanup API client");
  assert.match(apiSource, /"\/api\/operator\/cleanup-workspace-access"/, "cleanup API client must call the operator cleanup route");
});

test("Admin money and cleanup operations require explicit operator safeguards", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");

  assert.match(adminSource, /idempotencyKey/, "manual top-up UI must send a backend idempotency key");
  assert.match(adminSource, /manual-topup/, "manual top-up idempotency key must be operation scoped");
  for (const signal of ["operatorUserId", "operatorAccountId", "ledgerEntryId", "walletTransactionId", "balanceBefore", "balanceAfter"]) {
    assert.match(adminSource, new RegExp(signal), `manual top-up UI must expose ${signal}`);
  }
  assert.doesNotMatch(adminSource, /amount: 200|reason: "commercial top-up"/, "manual top-up must not prefill demo amount or generic reason");
  assert.match(adminSource, /cleanupCandidateCount/, "cleanup all UI must compute a candidate count before mutation");
  assert.match(adminSource, /OperationConfirmButton[\s\S]*清理全部无效 URL/, "cleanup all must use the shared confirmation button");
  assert.match(adminSource, /候选|预览|预计/, "cleanup all confirmation must show an operator preview/count");
  assert.doesNotMatch(adminSource, /<Button\s+danger[\s\S]{0,180}cleanupWorkspaceAccess\(\{ reason: "operator_cleanup_all"/, "cleanup all must not remain a direct danger button");
});

test("Admin commercial operations cover organizations, resource settlement, reconciliation, and spent metrics", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const billingApiSource = await source("apps/console-ui/src/api/billing-api.ts");
  const readApiSource = await source("apps/console-ui/src/api/console-read-api.ts");
  const routesSource = await source("apps/console-ui/src/routes/opl-routes.ts");

  for (const signal of ["createOrganization", "addOrganizationMember"]) {
    assert.match(readApiSource, new RegExp(signal), `management client must expose ${signal}`);
    assert.match(adminSource, new RegExp(signal), `Admin UI must use ${signal}`);
  }
  for (const route of ["/api/organizations", "/api/organizations/members"]) {
    assert.match(readApiSource, new RegExp(route.replaceAll("/", "\\/")), `management client must call ${route}`);
  }
  for (const signal of ["settleResourceBilling", "recordBillingReconciliation"]) {
    assert.match(billingApiSource, new RegExp(signal), `billing client must expose ${signal}`);
    assert.match(adminSource, new RegExp(signal), `Admin billing UI must use ${signal}`);
  }
  for (const route of ["/api/billing/resource-settlements", "/api/billing/reconciliation"]) {
    assert.match(billingApiSource, new RegExp(route.replaceAll("/", "\\/")), `billing client must call ${route}`);
  }
  for (const route of ["POST /api/organizations", "POST /api/organizations/members", "POST /api/billing/resource-settlements", "POST /api/billing/reconciliation"]) {
    assert.match(routesSource, new RegExp(route.replaceAll("/", "\\/")), `route contract must declare ${route}`);
  }
  for (const signal of ["totalSpent", "debited", "已消费金额", "最近对账", "阻塞原因"]) {
    assert.match(adminSource, new RegExp(signal), `Admin UI must expose ${signal}`);
  }
  assert.match(adminSource, /moneyValue\(row\)/, "Admin Ledger must render Ledger amountCents facts without NaN");
  for (const signal of ["priceSnapshotLabel", "priceSnapshot", "providerCostEvidenceRef", "walletAfterLabel", "balanceCents", "availableCents", "totalSpentCents"]) {
    assert.match(adminSource, new RegExp(signal), `Admin billing evidence UI must expose ${signal}`);
  }
});

test("Admin overview answers operator spend, purchase, and status questions", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");

  for (const signal of ["谁花了钱", "买了什么", "现在怎样", "managementState.accounts", "resourceLedgerEvidence", "resourceAnomalies"]) {
    assert.match(adminSource, new RegExp(signal), `Admin overview must expose ${signal}`);
  }
  assert.doesNotMatch(adminSource, /最近告警|eyebrow="通知"|CVM 分配证据/, "Admin overview should not lead with vague alerts or low-level evidence");
});

test("Admin resource and support views expose operator-grade lookup fields", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");

  for (const signal of ["workspaceUrl", "ownerEmail", "attachmentId", "用户邮箱", "Workspace URL", "挂载 ID"]) {
    assert.match(adminSource, new RegExp(signal), `Admin diagnostics must expose ${signal}`);
  }
  for (const signal of ["accountId", "userId", "workspaceId", "messages", "账号", "用户", "工作区", "反馈"]) {
    assert.match(adminSource, new RegExp(signal), `Admin support table must expose ${signal}`);
  }
  assert.doesNotMatch(adminSource, /eyebrow="信号"/, "notifications must not be labeled as vague signals");
  assert.match(adminSource, /释放冻结.*可用余额|可用余额.*释放冻结/s, "billing copy must explain released holds increase available balance without adding balance");
});

test("Admin workspace detail route is matched before generic admin overview", async () => {
  const routeRegistrySource = await source("apps/console-ui/src/routes/route-registry.tsx");
  const workspaceIndex = routeRegistrySource.indexOf('path.startsWith("/admin/workspaces/")');
  const adminIndex = routeRegistrySource.indexOf('path.startsWith("/admin")');

  assert.ok(workspaceIndex >= 0, "route registry must handle /admin/workspaces/:id");
  assert.ok(adminIndex >= 0, "route registry must handle /admin overview");
  assert.ok(workspaceIndex < adminIndex, "/admin/workspaces/:id must be checked before generic /admin");
});

test("Owner Billing and Workspace pages use production-safe customer copy", async () => {
  const overviewSource = await source("apps/console-ui/src/pages/OverviewPage.tsx");
  const billingSource = await source("apps/console-ui/src/pages/billing/BillingPage.tsx");
  const listSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const detailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");
  const accountSource = await source("apps/console-ui/src/pages/account/AccountPage.tsx");

  assert.doesNotMatch(billingSource, /计费规则|billingPolicy|billingReconciliation|对账|Ledger|compute_debit|storage_debit|资源账本|人工充值证据/i, "owner billing must not expose pricing policy internals, reconciliation guard, or Ledger event names");
  assert.doesNotMatch(billingSource, /request_debit|请求扣费|token|tokens|model pricing/i, "owner billing must not expose request-level charging");
  assert.match(`${overviewSource}\n${billingSource}\n${listSource}`, /开通流程|业务链|开通计算|开通存储|挂载存储|创建工作区|可用余额|费用明细|提交工单/, "owner UI must answer provisioning, create, balance, spend, support, and business-chain questions with production-grade labels");
  assert.doesNotMatch(`${overviewSource}\n${billingSource}\n${listSource}\n${detailSource}\n${accountSource}`, /东西在这里|钱花在哪里|我有多少钱|开工作区|看余额|看花费|提出疑问|常用入口|下一步|最近通知|告警|外部入口|存储资源|运行时长|下次结算|结算说明|钱包流水|访问生命周期|二级入口|二级资源|数据提醒|查看当前计算|查看存储资源|查看当前挂载/, "owner UI must not show colloquial copy, duplicate action panels, or secondary ops panels by default");
  assert.match(accountSource, /用户|运维/, "account page must use product role labels instead of lab-specific role names");
  assert.doesNotMatch(accountSource, /负责人|管理员|实验室/, "account page must not expose lab-specific role framing");
  assert.doesNotMatch(`${billingSource}\n${listSource}\n${detailSource}`, /resourceUsageLogs/, "active UI must not use retired resource usage projection");
  for (const signal of ["activeHourlyEstimate", "预计每小时"]) {
    assert.match(billingSource, new RegExp(signal), `billing page must show ${signal}`);
  }
  for (const signal of ["workspaceCredential", "workspace.billing", "访问入口", "当前费用", "预计每小时"]) {
    assert.match(listSource, new RegExp(signal), `Workspace list must expose customer-safe access and billing signal ${signal}`);
  }
  for (const forbidden of ["归属账号", "运维归因证据", "URL owner", "CVM owner", "CVM ID", "存储 provider ID", "问题依据", "providerRequestId", "ownerAccountId"]) {
    assert.doesNotMatch(detailSource, new RegExp(forbidden), `Workspace detail must not show operator-only ${forbidden}`);
  }
  assert.doesNotMatch(listSource, /title: "归属"|dataIndex: "ownerAccountId"|title: "密码"[\s\S]*credentialCell/, "Workspace list must not expose account ids or plaintext passwords");
  assert.match(detailSource, /showPassword|显示密码|隐藏密码/, "Workspace detail must hide the Workspace password by default with a reveal control");
  assert.match(detailSource, /停用访问|访问已停用/, "Workspace detail must label token removal as access disablement, not Workspace deletion");
  assert.match(detailSource, /提交工单/, "Workspace detail must offer a support path");
  assert.match(detailSource, /tokenStatus/, "Workspace detail must show token lifecycle status");
});

test("owner resource UI hides operator-only resource identity while keeping cold-start progress", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const createWorkspaceSource = await source("apps/console-ui/src/pages/workspaces/CreateWorkspacePage.tsx");
  const surfaceSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");

  for (const signal of ["billingStatus", "workspaceId", "计费状态", "绑定入口", "预计等待"]) {
    assert.match(resourceSource, new RegExp(signal), `resource UI must keep customer-safe ${signal}`);
  }
  for (const forbidden of ["拥有账号", "节点池", "独占节点", "内网 IP", "公网 IP", "CVM ID 未返回", "providerRequestId"]) {
    assert.doesNotMatch(resourceSource, new RegExp(forbidden), `owner resource UI must not expose operator-only ${forbidden}`);
  }

  for (const stage of ["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]) {
    assert.match(surfaceSource, new RegExp(stage), `operation timeline must include ${stage}`);
  }
  for (const stage of ["存储创建中", "可挂载", "释放冻结", "销毁存储", "可创建入口"]) {
    assert.match(resourceSource, new RegExp(stage), `resource pages must pass object-specific stage ${stage}`);
  }
  for (const stage of ["生成 URL", "URL 可用"]) {
    assert.match(createWorkspaceSource, new RegExp(stage), `workspace create page must pass URL-entry stage ${stage}`);
  }
  assert.match(surfaceSource, /stages = resourceOperationStages/, "OperationTimeline must accept route/object-specific stages");

  assert.doesNotMatch(resourceSource, /title: "云资源"|label: "云资源"/, "resource UI must not collapse node identity into generic cloud-resource labels");
});

test("Admin users surface is backed by management state and can create login users", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const stateSource = await source("apps/console-ui/src/store/console-state.ts");
  const apiSource = await source("apps/console-ui/src/api/console-read-api.ts");
  const routesSource = await source("apps/console-ui/src/routes/opl-routes.ts");

  assert.match(stateSource, /getManagementState/, "admin state hook must load the management read model");
  assert.match(apiSource, /createUser/, "admin API client must expose user creation");
  assert.match(apiSource, /disableUser/, "admin API client must expose user disable");
  assert.match(apiSource, /deleteUser/, "admin API client must expose user delete");
  assert.match(apiSource, /"\/api\/users"/, "user creation client must call POST /api/users");
  assert.match(apiSource, /"\/api\/users\/disable"/, "user disable client must call POST /api/users/disable");
  assert.match(apiSource, /"\/api\/users\/delete"/, "user delete client must call POST /api/users/delete");
  assert.match(adminSource, /managementState\.users/, "Admin Users must list management users, not only session.user");
  assert.match(adminSource, /filter\(\(user\) => user\.status !== "deleted"\)/, "Admin Users main table must hide deleted users after refresh");
  assert.match(apiSource, /includeDeleted = false/, "management state must default to hiding deleted users");
  assert.match(apiSource, /if \(includeDeleted\) params\.set\("includeDeleted", "true"\);/, "deleted users must require an explicit includeDeleted request");
  assert.match(adminSource, /createUser\(/, "Admin Users must submit the new user form");
  assert.match(adminSource, /disableUser\(/, "Admin Users must expose user disable action");
  assert.match(adminSource, /deleteUser\(/, "Admin Users must expose user delete action");
  assert.match(adminSource, /资源和账单保留/, "user delete UI must explain resource and billing evidence retention");
  assert.doesNotMatch(adminSource, /data=\{\[\{\s*id: session\.user\.id/, "Admin Users must not render only the current session user");
  assert.doesNotMatch(adminSource, /新建用户<\/Button><\/Tooltip>|disabled>新建用户/, "new user action must not remain disabled");
  assert.match(routesSource, /"POST \/api\/users"/, "admin.users route contract must declare user creation");
  assert.match(routesSource, /"POST \/api\/users\/disable"/, "admin.users route contract must declare user disable");
  assert.match(routesSource, /"POST \/api\/users\/delete"/, "admin.users route contract must declare user delete");
});

test("frontend state reads backend account scope and refreshes support mappings after mutations", async () => {
  const pageSource = await source("apps/console-ui/src/pages/ConsolePage.tsx");
  const storeSource = await source("apps/console-ui/src/store/console-state.ts");
  const apiSource = await source("apps/console-ui/src/api/console-read-api.ts");
  const ticketsSource = await source("apps/console-ui/src/pages/support/useTickets.ts");

  assert.match(pageSource, /accountId: session\.user\?\.accountId/, "Console state must use the authenticated account id");
  assert.match(apiSource, /getConsoleState\(accountId = ""\)/, "state client must accept account scope");
  assert.match(storeSource, /getConsoleState\(accountId\)/, "state hook must request backend state for the current account");
  assert.match(storeSource, /tickets\.refresh\(\)/, "shared mutation path must refresh support mappings from backend");
  assert.doesNotMatch(ticketsSource, /setTickets\(\(current\)/, "support create must not locally prepend unverified ticket state");
  assert.match(ticketsSource, /await refresh\(\)/, "support create must reload persisted mappings after mutation");
});

test("resource pages expose relationship map, wallet risk, and support context without policy dumps", async () => {
  const resourceSource = await source("apps/console-ui/src/pages/resources/ResourceProvisioningPages.tsx");
  const workspaceSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const detailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");
  const supportSource = await source("apps/console-ui/src/pages/support/SupportPage.tsx");
  const routeSource = await source("apps/console-ui/src/routes/opl-routes.ts");
  const surfaceSource = await source("apps/console-ui/src/pages/shared/commercial-console.tsx");

  assert.match(routeSource, /id: "resources\.relationships"/, "resource relationship map must have a first-class route");
  assert.match(resourceSource, /ResourceRelationshipPage/, "resources module must render the relationship route");
  assert.match(resourceSource, /ResourceRelationshipGraph/, "resource relationship page must use shared graph component");
  assert.match(resourceSource, /WalletRiskPanel/, "resource creation pages must show balance risk before paid mutations");
  assert.doesNotMatch(`${resourceSource}\n${detailSource}`, /DataRetentionPolicyPanel|实验室策略|迁移 \/ rollout|先不做复杂子账号体系|左栏保持三项/, "owner pages must not show internal strategy or policy dumps");
  assert.match(resourceSource, /supportContextPath/, "failed resource support links must carry operation and resource context");
  assert.match(workspaceSource, /workspaceCredential/, "Workspace list may derive credential readiness without printing the password");
  assert.match(workspaceSource, /workspace\.billing/, "Workspace list must lead with backend per-Workspace billing");
  assert.match(supportSource, /URLSearchParams/, "support form must accept failure context from resource pages");
  assert.match(supportSource, /operationId|resourceId/, "support form must carry operationId/resourceId into the ticket description");
  assert.match(surfaceSource, /账号.*计算.*存储.*挂载.*工作区入口/s, "relationship graph must make the account-resource-entry chain visible");
});

test("Admin diagnostics and E2E records are read-only operator surfaces", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const routeRegistrySource = await source("apps/console-ui/src/routes/route-registry.tsx");
  const routesSource = await source("apps/console-ui/src/routes/opl-routes.ts");

  assert.match(routesSource, /id: "admin\.diagnostics"/, "admin diagnostics must have a refreshable route");
  assert.match(routesSource, /id: "admin\.e2e"/, "admin E2E records must have a refreshable route");
  assert.match(routesSource, /"GET \/api\/operator\/summary"/, "admin read-only surfaces must use operator summary");
  assert.match(routeRegistrySource, /AdminDiagnosticsPage/, "route registry must render admin diagnostics route");
  assert.match(routeRegistrySource, /AdminE2EPage/, "route registry must render admin E2E route");
  assert.match(adminSource, /AdminDiagnosticsPage/, "Admin diagnostics page must exist");
  assert.match(adminSource, /ProductionE2EPanel/, "Admin E2E page must use safe E2E panel");
  assert.match(adminSource, /failedOperations|resourceAnomalies|productionE2E/, "Admin diagnostics must show failed operations, resource anomalies, and E2E records");
  assert.doesNotMatch(adminSource, /adminResourceEvidenceRows/, "Admin diagnostics must not guess resource ownership evidence in the UI");
  assert.match(adminSource, /managementState\.resourceLedgerEvidence/, "Admin diagnostics must consume backend resource ledger evidence");
  assert.match(adminSource, /AdminDiagnosticsPage\(\{ managementState, adminOps \}(?:: any)?\)/, "Admin diagnostics resource evidence must use managementState, not the admin account state");
  for (const signal of ["资源归属证据", "CVM / 节点", "存储 provider", "providerRequestId", "ownerAccountId"]) {
    assert.match(adminSource, new RegExp(signal.replace("/", "\\/")), `Admin diagnostics must expose ${signal}`);
  }
  const diagnosticsSlice = adminSource.slice(
    adminSource.indexOf("export function AdminDiagnosticsPage"),
    adminSource.indexOf("export function AdminCleanupPage")
  );
  assert.doesNotMatch(diagnosticsSlice, /OPL_CODEX_API_KEY|workspace\.url|access\?\.token/i, "Admin diagnostics UI must not render runtime secrets or Workspace access URLs");
});

test("Admin diagnostics exposes resource ledger evidence chain without operator guesswork", async () => {
  const adminSource = await source("apps/console-ui/src/pages/admin/AdminOverviewPage.tsx");
  const readModelSource = await source("services/control-plane/internal/server/runtime.go");

  for (const signal of [
    "resourceLedgerEvidence",
    "ledgerEntryIds",
    "walletTransactionIds",
    "operationId",
    "costTags",
    "ownerAccountId",
    "ownerUserId",
    "cvmInstanceId",
    "nodeName",
    "storageId",
    "workspaceIds"
  ]) {
    assert.match(adminSource, new RegExp(signal), `Admin diagnostics must render ${signal}`);
    assert.match(readModelSource, new RegExp(signal), `Console read model must produce ${signal}`);
  }
  assert.match(adminSource, /opl_operation_id/, "Admin diagnostics must show provider cost tag identity");
});

test("Workspace detail links to first-class resources and excludes retired compute lifecycle controls", async () => {
  const listSource = await source("apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx");
  const detailSource = await source("apps/console-ui/src/pages/workspaces/WorkspaceDetailPage.tsx");

  assert.match(listSource, /详情/, "Workspace list must expose a secondary detail entry");
  assert.match(listSource, /routeTo\("workspace.detail"/, "Workspace list detail entry must route to Workspace detail");
  assert.doesNotMatch(detailSource, /routeTo\("compute-allocations\.detail"|routeTo\("storage\.detail"|routeTo\("attachment\.detail"/, "owner Workspace detail should not route users into resource internals");
  assert.doesNotMatch(detailSource, /ownerAccountId|归属账号/, "owner account handoff belongs in Admin diagnostics, not owner Workspace detail");
});

test("active UI and docs describe the ComputeAllocation, StorageVolume, attachment, and URL-entry chain", async () => {
  for (const file of [
    "README.md",
    "docs/invariants.md",
    "docs/runtime/production-runbook.md",
    "apps/console-ui/src/pages/HomePage.tsx",
    "apps/console-ui/src/pages/billing/BillingPage.tsx",
    "apps/console-ui/src/pages/workspaces/WorkspacesPage.tsx",
    "apps/console-ui/src/pages/admin/AdminOverviewPage.tsx"
  ]) {
    const text = await source(file);
    assert.match(text, /ComputeAllocation|compute allocation|计算分配|计算/, `${file} must describe compute allocation capability`);
    assert.match(text, /StorageVolume|storage volume|storageVolumes|存储资源|存储/, `${file} must describe storage volume capability`);
    assert.match(text, /Workspace URL|URL entry|Workspace|工作区|访问入口/, `${file} must describe Workspace as an access entry`);
  }
});
