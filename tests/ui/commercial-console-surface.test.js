import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const repoRoot = new URL("../../", import.meta.url);

async function source(relativePath) {
  return readFile(new URL(relativePath, repoRoot), "utf8");
}

test("commercial Console UI is built from the maintained surface component layer", async () => {
  const surfaceSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");

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
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx",
    "packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/gateway/GatewayPage.jsx",
    "packages/console/ui/pages/account/AccountPage.jsx",
    "packages/console/ui/pages/support/SupportPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const pageSource = await source(page);
    assert.match(pageSource, /shared\/commercial-console\.jsx/, `${page} must import the commercial Console surface`);
    assert.doesNotMatch(pageSource, /StatisticCard/, `${page} must not use the old metric card layer directly`);
  }
});

test("public entry is Console-first and does not use the retired marketing hero shell", async () => {
  const homeSource = await source("packages/console/ui/pages/HomePage.jsx");
  assert.match(homeSource, /publicConsole/, "public home should present the Console product surface");
  assert.doesNotMatch(homeSource, /homeHero|heroPreview|chainPreview/, "retired marketing hero classes must stay removed");
});

test("public entry visible copy is concise Chinese without old English labels", async () => {
  const homeSource = await source("packages/console/ui/pages/HomePage.jsx");
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
  const shellSource = await source("packages/console/ui/pages/ConsolePage.jsx");
  assert.match(shellSource, /title="OPL Console"/, "authenticated app shell must use OPL Console product naming");
  assert.doesNotMatch(shellSource, /title="OPL Cloud"/, "authenticated app shell must not retain old OPL Cloud naming");
});

test("visible app chrome does not retain old Cloud or reserved backlog copy", async () => {
  for (const page of [
    "packages/console/ui/main.jsx",
    "packages/console/ui/pages/LoginPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const pageSource = await source(page);
    assert.doesNotMatch(pageSource, /Loading OPL Cloud|> OPL Cloud</, `${page} must use OPL Console in visible chrome`);
    assert.doesNotMatch(pageSource, /status: "reserved"|value: "Backlog"|not in current launch/, `${page} must not show reserved/backlog product copy`);
  }
});

test("create Workspace flow is a single commercial submit action", async () => {
  const createSource = await source("packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx");
  const stateSource = await source("packages/console/ui/store/console-state.js");

  assert.doesNotMatch(createSource, /StepsForm/, "create flow must not hide provisioning behind a multi-step wizard");
  assert.match(createSource, /htmlType="submit"/, "create flow must expose one clear submit button");
  assert.match(createSource, /attachmentId/, "Workspace runtime binding must start from an attached compute/storage pair");
  assert.match(createSource, /const created = await runAction/, "create flow must inspect action success before navigating");
  assert.match(createSource, /if \(created\) navigate/, "create flow must not navigate away after failed provisioning");
  assert.match(stateSource, /return result \|\| true/, "runAction must return successful action payloads");
  assert.match(stateSource, /return false/, "runAction must report failed actions");
});

test("shared runAction suppresses duplicate submissions by action key", async () => {
  const stateSource = await source("packages/console/ui/store/console-state.js");
  const workspaceListSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const workspaceDetailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");

  assert.match(stateSource, /pendingActions/, "runAction must track pending action keys");
  assert.match(stateSource, /actionKey/, "runAction must accept an action key");
  assert.match(stateSource, /current\.has\(actionKey\)/, "runAction must suppress duplicate action keys");
  assert.match(workspaceListSource, /actionKey: `workspace-reset-\$\{row\.id\}`/, "Workspace list reset must key duplicate suppression by Workspace");
  assert.match(workspaceListSource, /actionKey: `workspace-delete-\$\{row\.id\}`/, "Workspace list delete must key duplicate suppression by Workspace");
  assert.match(workspaceDetailSource, /actionKey: `workspace-reset-\$\{selected\.id\}`/, "Workspace detail reset must key duplicate suppression by Workspace");
  assert.match(adminSource, /actionKey: "admin-manual-topup"/, "manual top-up must suppress duplicate submit");
});

test("Workspace UI treats URL as stable storage subject with current runtime pointer", async () => {
  const listSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const detailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");
  const routeSource = await source("packages/console/ui/routes/opl-routes.js");
  const cleanupSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");

  assert.match(listSource, /currentComputeAllocationId/, "Workspace list must show current compute pointer");
  assert.match(listSource, /workspaceHourlyEstimate/, "Workspace list must use current resource pointers for per-Workspace billing");
  assert.match(detailSource, /currentAttachmentId/, "Workspace detail must show current attachment pointer");
  assert.match(detailSource, /Workspace 即 UI 子账号/, "Workspace detail must state the simplified UI subaccount model");
  assert.match(routeSource, /currentComputeAllocationId/, "runtime route registry must expose current compute pointer");
  assert.match(routeSource, /currentAttachmentId/, "runtime route registry must expose current attachment pointer");
  assert.doesNotMatch(cleanupSource, /!compute \|\| compute\.status === "destroyed"/, "cleanup must not invalidate stable URL only because compute is suspended");
});

test("resource provisioning pages call resource APIs instead of disabled placeholders", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");

  for (const apiName of ["createComputeAllocation", "createStorageVolume", "attachStorage"]) {
    assert.match(resourceSource, new RegExp(`${apiName}\\(`), `resource UI must call ${apiName}`);
  }
  assert.doesNotMatch(resourceSource, /disabled: true/, "resource creation actions must not remain disabled placeholders");
});

test("create forms do not prefill demo-style resource names", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");
  const workspaceSource = await source("packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx");

  assert.doesNotMatch(resourceSource, /initialValues=\{\{ name:/, "resource creation forms must not prefill demo names");
  assert.doesNotMatch(workspaceSource, /initialValues=\{\{ workspaceName:/, "Workspace creation must not prefill a demo name");
  for (const demoCopy of ["实验工作区", "实验数据盘", "分析计算资源"]) {
    assert.doesNotMatch(resourceSource, new RegExp(demoCopy), `resource forms must not show ${demoCopy}`);
    assert.doesNotMatch(workspaceSource, new RegExp(demoCopy), `workspace forms must not show ${demoCopy}`);
  }
});

test("resource provisioning failures preserve provider details in the visible result", async () => {
  const stateSource = await source("packages/console/ui/store/console-state.js");
  const apiSource = await source("packages/console/ui/api/console-api.js");
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");

  assert.match(apiSource, /payload\.safeMessage \|\| payload\.error/, "API client must prefer safe provider messages");
  assert.match(stateSource, /returnFailure = false/, "runAction must keep false-return behavior by default");
  assert.match(stateSource, /await refresh\(\)/, "failed resource mutations must refresh state so failed resources stay visible");
  assert.match(stateSource, /failureReason: err\.message/, "runAction must expose caught provider errors to operation panels");
  assert.match(resourceSource, /returnFailure: true/g, "resource mutations must opt into visible failure envelopes");
  assert.doesNotMatch(resourceSource, /failureReason: "操作失败，请查看提示后重试。"/, "resource pages must not replace provider failures with generic copy");
});

test("resource provisioning UI shows price, hold, balance impact, and operation state", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");
  const billingSource = await source("packages/console/ui/pages/billing/BillingPage.jsx");

  for (const requiredSignal of [
    "computeHourlyPrice",
    "computeHoldAmount",
    "storageGbMonthPrice",
    "storageHourlyEstimate",
    "balanceAfterHold",
    "operationId",
    "safeMessage",
    "providerRequestId"
  ]) {
    assert.match(resourceSource, new RegExp(requiredSignal), `resource UI must expose ${requiredSignal}`);
  }
  assert.match(resourceSource, /¥\/小时|每小时/, "compute creation must show hourly pricing before submit");
  assert.match(resourceSource, /冻结|hold/i, "resource creation must show prepaid hold impact before submit");
  assert.match(resourceSource, /Form\.useWatch\("packageId"/, "resource creation must update pricing when package selection changes");
  assert.match(resourceSource, /PriceImpactPanel/, "resource creation must use the maintained pricing impact component");
  assert.match(resourceSource, /OperationTimeline/, "resource detail must show operation timeline evidence");
  assert.match(resourceSource, /FailureRecoveryPanel/, "resource detail must expose failed-operation recovery guidance");
  assert.match(resourceSource, /resource\.hourlyEstimate/, "storage detail must show hourlyEstimate instead of compute hourlyPrice");
  assert.match(billingSource, /item\.hourlyEstimate/, "billing hourly estimate must include storage hourlyEstimate");
});

test("resource mutations use confirmation, waiting state, result receipts, and concise Chinese copy", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");
  const surfaceSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");
  const actionsSource = await source("packages/console/ui/routes/opl-actions.js");
  const routesSource = await source("packages/console/ui/routes/opl-routes.js");
  const apiSource = await source("packages/console/ui/api/resources-api.js");

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
  const routesSource = await source("packages/console/ui/routes/opl-routes.js");
  const shellSource = await source("packages/console/ui/pages/ConsolePage.jsx");
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");
  const apiSource = await source("packages/console/ui/api/console-read-api.js");

  assert.match(routesSource, /id: "admin\.cleanup"/, "admin cleanup must be a first-class route id");
  assert.match(routesSource, /path: "\/admin\/cleanup"/, "admin cleanup must have a refreshable route");
  assert.match(routesSource, /"POST \/api\/operator\/cleanup-workspace-access"/, "admin cleanup route must declare its cleanup API");
  assert.match(shellSource, /AdminCleanupPage/, "Console shell must render the cleanup page through the route");
  assert.match(adminSource, /CleanupResourceTable/, "cleanup UI must use the maintained cleanup table component");
  assert.match(adminSource, /cleanupWorkspaceAccess/, "cleanup UI must call the cleanup API client");
  assert.match(apiSource, /"\/api\/operator\/cleanup-workspace-access"/, "cleanup API client must call the operator cleanup route");
});

test("Admin money and cleanup operations require explicit operator safeguards", async () => {
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");

  assert.match(adminSource, /idempotencyKey/, "manual top-up UI must send a backend idempotency key");
  assert.match(adminSource, /manual-topup/, "manual top-up idempotency key must be operation scoped");
  assert.match(adminSource, /cleanupCandidateCount/, "cleanup all UI must compute a candidate count before mutation");
  assert.match(adminSource, /OperationConfirmButton[\s\S]*清理全部无效 URL/, "cleanup all must use the shared confirmation button");
  assert.match(adminSource, /候选|预览|预计/, "cleanup all confirmation must show an operator preview/count");
  assert.doesNotMatch(adminSource, /<Button\s+danger[\s\S]{0,180}cleanupWorkspaceAccess\(\{ reason: "operator_cleanup_all"/, "cleanup all must not remain a direct danger button");
});

test("Billing and Workspace pages explain commercial charging and URL lifecycle", async () => {
  const billingSource = await source("packages/console/ui/pages/billing/BillingPage.jsx");
  const listSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const detailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");

  assert.match(billingSource, /计费规则|billingPolicy/, "billing page must explain holds, release, and resource debit policy");
  assert.doesNotMatch(billingSource, /request_debit|请求扣费|token|tokens|model pricing/i, "OPL Cloud billing must not expose request-level charging");
  assert.match(billingSource, /compute_debit|storage_debit|资源扣费|存储扣费|resourceUsageLogs/, "billing page must expose resource debit evidence");
  for (const signal of ["activeHourlyEstimate", "nextSettlementAt", "runningDuration", "下次结算", "运行时长", "预计每小时"]) {
    assert.match(billingSource, new RegExp(signal), `billing page must show ${signal}`);
  }
  for (const signal of ["workspaceCredential", "workspaceChargeTotal", "workspaceHourlyEstimate", "URL、账号、密码", "按 Workspace 汇总", "密码", "归属"]) {
    assert.match(listSource, new RegExp(signal), `Workspace list must expose first-screen credential and billing signal ${signal}`);
  }
  for (const signal of ["currentAttachmentId", "二级资源", "当前挂载", "归属账号", "运维归因证据", "URL owner", "CVM ID", "存储 provider ID", "问题依据"]) {
    assert.match(detailSource, new RegExp(signal), `Workspace detail must retain secondary resource signal ${signal}`);
  }
  assert.match(detailSource, /WorkspaceLifecyclePanel/, "Workspace detail must expose URL/resource lifecycle state");
  assert.match(detailSource, /tokenStatus/, "Workspace detail must show token lifecycle status");
});

test("resource UI exposes dedicated node identity and cold-start progress without generic cloud-resource copy", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");
  const createWorkspaceSource = await source("packages/console/ui/pages/workspaces/CreateWorkspacePage.jsx");
  const surfaceSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");

  for (const signal of [
    "ownerAccountId",
    "nodePoolId",
    "nodeName",
    "privateIp",
    "cvmInstanceId",
    "billingStatus",
    "workspaceId",
    "节点池",
    "独占节点",
    "内网 IP",
    "公网 IP",
    "计费状态",
    "绑定入口",
    "拥有账号",
    "预计等待"
  ]) {
    assert.match(resourceSource, new RegExp(signal), `resource UI must expose ${signal}`);
  }
  assert.match(resourceSource, /CVM ID 未返回，使用节点身份/, "resource UI must distinguish missing CVM ID from valid node identity");

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
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");
  const stateSource = await source("packages/console/ui/store/console-state.js");
  const apiSource = await source("packages/console/ui/api/console-read-api.js");
  const routesSource = await source("packages/console/ui/routes/opl-routes.js");

  assert.match(stateSource, /getManagementState/, "admin state hook must load the management read model");
  assert.match(apiSource, /createUser/, "admin API client must expose user creation");
  assert.match(apiSource, /disableUser/, "admin API client must expose user disable");
  assert.match(apiSource, /deleteUser/, "admin API client must expose user delete");
  assert.match(apiSource, /"\/api\/users"/, "user creation client must call POST /api/users");
  assert.match(apiSource, /"\/api\/users\/disable"/, "user disable client must call POST /api/users/disable");
  assert.match(apiSource, /"\/api\/users\/delete"/, "user delete client must call POST /api/users/delete");
  assert.match(adminSource, /managementState\.users/, "Admin Users must list management users, not only session.user");
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

test("resource pages expose relationship map, wallet risk, support context, and data retention policy", async () => {
  const resourceSource = await source("packages/console/ui/pages/resources/ResourceProvisioningPages.jsx");
  const workspaceSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const detailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");
  const supportSource = await source("packages/console/ui/pages/support/SupportPage.jsx");
  const routeSource = await source("packages/console/ui/routes/opl-routes.js");
  const surfaceSource = await source("packages/console/ui/pages/shared/commercial-console.jsx");

  assert.match(routeSource, /id: "resources\.relationships"/, "resource relationship map must have a first-class route");
  assert.match(resourceSource, /ResourceRelationshipPage/, "resources module must render the relationship route");
  assert.match(resourceSource, /ResourceRelationshipGraph/, "resource relationship page must use shared graph component");
  assert.match(resourceSource, /WalletRiskPanel/, "resource creation pages must show balance risk before paid mutations");
  assert.match(resourceSource, /DataRetentionPolicyPanel/, "resource details must show compute/storage retention policy");
  assert.match(resourceSource, /supportContextPath/, "failed resource support links must carry operation and resource context");
  assert.match(workspaceSource, /workspaceCredential/, "Workspace list must lead with URL/account/password credentials");
  assert.match(workspaceSource, /workspaceChargeTotal/, "Workspace list must lead with per-Workspace billing");
  assert.match(detailSource, /DataRetentionPolicyPanel/, "Workspace detail must show data retention policy");
  assert.match(supportSource, /URLSearchParams/, "support form must accept failure context from resource pages");
  assert.match(supportSource, /operationId|resourceId/, "support form must carry operationId/resourceId into the ticket description");
  assert.match(surfaceSource, /账号.*计算.*存储.*挂载.*工作区入口/s, "relationship graph must make the account-resource-entry chain visible");
});

test("Admin diagnostics and E2E records are read-only operator surfaces", async () => {
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");
  const shellSource = await source("packages/console/ui/pages/ConsolePage.jsx");
  const routesSource = await source("packages/console/ui/routes/opl-routes.js");

  assert.match(routesSource, /id: "admin\.diagnostics"/, "admin diagnostics must have a refreshable route");
  assert.match(routesSource, /id: "admin\.e2e"/, "admin E2E records must have a refreshable route");
  assert.match(routesSource, /"GET \/api\/operator\/summary"/, "admin read-only surfaces must use operator summary");
  assert.match(shellSource, /AdminDiagnosticsPage/, "Console shell must render admin diagnostics route");
  assert.match(shellSource, /AdminE2EPage/, "Console shell must render admin E2E route");
  assert.match(adminSource, /AdminDiagnosticsPage/, "Admin diagnostics page must exist");
  assert.match(adminSource, /ProductionE2EPanel/, "Admin E2E page must use safe E2E panel");
  assert.match(adminSource, /failedOperations|resourceAnomalies|productionE2E/, "Admin diagnostics must show failed operations, resource anomalies, and E2E records");
  assert.match(adminSource, /adminResourceEvidenceRows/, "Admin diagnostics must derive resource ownership evidence from management state");
  assert.match(adminSource, /AdminDiagnosticsPage\(\{ managementState, adminOps \}\)/, "Admin diagnostics resource evidence must use managementState, not the admin account state");
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
  const adminSource = await source("packages/console/ui/pages/admin/AdminOverviewPage.jsx");
  const readModelSource = await source("packages/console/src/services/console-read-model-service.js");

  for (const signal of [
    "resourceLedgerEvidence",
    "ledgerEntryIds",
    "walletTransactionIds",
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
});

test("Workspace detail links to first-class resources and excludes retired compute lifecycle controls", async () => {
  const listSource = await source("packages/console/ui/pages/workspaces/WorkspacesPage.jsx");
  const detailSource = await source("packages/console/ui/pages/workspaces/WorkspaceDetailPage.jsx");

  assert.match(listSource, /详情/, "Workspace list must expose a secondary detail entry");
  assert.match(listSource, /routeTo\("workspace.detail"/, "Workspace list detail entry must route to Workspace detail");
  assert.match(detailSource, /routeTo\("compute-allocations.detail"/, "detail must link to the attached ComputeAllocation");
  assert.match(detailSource, /routeTo\("storage.detail"/, "detail must link to the attached StorageVolume");
  assert.match(detailSource, /routeTo\("attachment.detail"/, "detail must link to the StorageAttachment");
  assert.match(detailSource, /ownerAccountId/, "detail must show the owner account for maintenance handoff");
  assert.match(detailSource, /归属账号/, "detail must label the owner account in Chinese");
});

test("active UI and docs describe the ComputeAllocation, StorageVolume, attachment, and URL-entry chain", async () => {
  for (const file of [
    "README.md",
    "docs/invariants.md",
    "docs/runtime/production-runbook.md",
    "packages/console/ui/pages/HomePage.jsx",
    "packages/console/ui/pages/OverviewPage.jsx",
    "packages/console/ui/pages/billing/BillingPage.jsx",
    "packages/console/ui/pages/workspaces/WorkspacesPage.jsx",
    "packages/console/ui/pages/admin/AdminOverviewPage.jsx"
  ]) {
    const text = await source(file);
    assert.match(text, /ComputeAllocation|compute allocation|计算分配|计算/, `${file} must describe compute allocation capability`);
    assert.match(text, /StorageVolume|storage volume|存储资源|存储/, `${file} must describe storage volume capability`);
    assert.match(text, /Workspace URL|URL entry|Workspace|工作区|访问入口/, `${file} must describe Workspace as an access entry`);
  }
});
