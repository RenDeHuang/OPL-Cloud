<script setup lang="ts">
import {
  Activity,
  AlertCircle,
  ArrowUpRight,
  CalendarDays,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  Copy,
  Database,
  Eye,
  EyeOff,
  LayoutDashboard,
  LogOut,
  Megaphone,
  Menu,
  Plus,
  ReceiptText,
  RefreshCw,
  Server,
  ShieldCheck,
  UserRound,
  UsersRound,
  WalletCards,
  X
} from "@lucide/vue";
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch, type Component } from "vue";

import { currentSession, login, logoutLocalFirst } from "./api/auth-api.ts";
import {
  createGatewayKey,
  createOperatorAnnouncement,
  createWalletAdjustment,
  disableOperatorAccount as disableOperatorAccountCommand,
  deleteGatewayKey,
  getAnnouncements,
  getBillingReceipt,
  getBillingReceipts,
  getGatewayAccountUsageSummary,
  getGatewayBalanceHistory,
  getGatewayKey,
  getGatewayKeyUsage,
  getGatewayKeyUsageSummary,
  getGatewayKeys,
  getGatewayWallet,
  getOperatorAccountsPage,
  getOperatorAnnouncements,
  getOperatorHealth,
  getOperatorOverview,
  getOperatorReconciliation,
  getOperatorWorkspaces,
  getOperatorWorkspace,
  getWalletAdjustment,
  provisionOperatorAccount,
  getPricingCatalog,
  markAnnouncementRead,
  publishOperatorAnnouncement,
  previewPricing,
  recoverWorkspaceLaunch as recoverOperatorWorkspaceLaunch,
  resolveBillingReview,
  revealGatewayKey,
  updateGatewayKey,
  withdrawOperatorAnnouncement
} from "./api/console-read-api.ts";
import {
  getWorkspaceLaunch,
  getWorkspaceLaunches,
  getWorkspaces,
  getWorkspaceRuntimeStatus,
  isTerminalWorkspaceLaunch,
  launchWorkspace,
  revealWorkspaceCredentials,
  rotateWorkspaceCredentials,
  workspaceLaunchIdempotencyKey
} from "./api/workspaces-api.ts";
import type {
  AnnouncementPageDTO,
  AnnouncementDraftRequest,
  AnnouncementScheduleRequest,
  AuthSession,
  BalanceHistoryData,
  BillingReceipt,
  BillingReceiptPage,
  BillingReviewResolutionRequest,
  CreateGatewayKeyRequest,
  GatewayAccountUsageSummaryDTO,
  GatewayKeySecretDTO,
  GatewayKeyPageDTO,
  GatewayKeySummaryDTO,
  GatewayKeyUsagePageDTO,
  GatewayUsageSummaryDTO,
  GatewayWallet,
  ProvisionAccountRequest,
  OperatorAccountPageDTO,
  OperatorAnnouncementPageDTO,
  OperatorHealthDTO,
  OperatorOverviewDTO,
  OperatorReconciliationItemDTO,
  OperatorReconciliationPageDTO,
  OperatorWorkspacePageDTO,
  OperatorWorkspaceDTO,
  PlanId,
  PricingCatalogResponse,
  SourceEnvelope,
  WalletAdjustmentOperationDTO,
  WalletAdjustmentRequest,
  WorkspaceCredentialAccess,
  WorkspaceDTO,
  WorkspaceLaunchRequest,
  WorkspaceLaunchResponse,
  WorkspaceListData,
  WorkspaceLaunchRecoveryRequest,
  WorkspaceRuntimeDTO,
  WorkspacePricePreview
} from "./api/dtos.ts";
import {
  adminMenu,
  apiMenu,
  apiPage,
  customerMenu,
  defaultAuthenticatedRoute,
  formatAvailableBalance,
  formatCount,
  formatDate,
  formatUsdMicros,
  maskGatewayKey,
  needsSession,
  workspaceStatusLabel
} from "./console-model.ts";

const menuIcons: Record<string, Component> = { Activity, CircleDollarSign, LayoutDashboard, Database, Megaphone, ReceiptText, Server, UsersRound };
const terminalStatuses = new Set(["succeeded", "failed", "refunded"]);
const workspaceLaunchPollIntervalMs = 10_000;
const workspaceLaunchPollAttempts = 30;
const secretLifetimeMs = 60_000;

const path = ref(window.location.pathname);
const session = ref<AuthSession | null>(null);
const authStatus = ref(needsSession(path.value) ? "checking" : "public");
const authError = ref("");
const workspaceSource = ref<SourceEnvelope<WorkspaceListData> | null>(null);
const workspaceStatusSource = ref<SourceEnvelope<WorkspaceRuntimeDTO> | null>(null);
const walletSource = ref<SourceEnvelope<GatewayWallet> | null>(null);
const keySource = ref<SourceEnvelope<GatewayKeyPageDTO> | null>(null);
const usageSource = ref<SourceEnvelope<GatewayKeyUsagePageDTO> | null>(null);
const usageStatsSource = ref<SourceEnvelope<GatewayUsageSummaryDTO> | null>(null);
const accountUsageSource = ref<SourceEnvelope<GatewayAccountUsageSummaryDTO> | null>(null);
const balanceHistorySource = ref<SourceEnvelope<BalanceHistoryData> | null>(null);
const receiptsSource = ref<SourceEnvelope<BillingReceiptPage> | null>(null);
const receiptDetailSource = ref<SourceEnvelope<BillingReceipt> | null>(null);
const announcementsSource = ref<SourceEnvelope<AnnouncementPageDTO> | null>(null);
const catalog = ref<PricingCatalogResponse | null>(null);
const previews = reactive<Partial<Record<PlanId, WorkspacePricePreview>>>({});
const operatorOverviewSource = ref<SourceEnvelope<OperatorOverviewDTO> | null>(null);
const operatorAccountsPageSource = ref<SourceEnvelope<OperatorAccountPageDTO> | null>(null);
const operatorWorkspacesSource = ref<SourceEnvelope<OperatorWorkspacePageDTO> | null>(null);
const operatorReconciliationSource = ref<SourceEnvelope<OperatorReconciliationPageDTO> | null>(null);
const operatorHealthSource = ref<SourceEnvelope<OperatorHealthDTO> | null>(null);
const operatorAnnouncementsSource = ref<SourceEnvelope<OperatorAnnouncementPageDTO> | null>(null);
const walletAdjustmentOperation = ref<WalletAdjustmentOperationDTO | null>(null);
const operatorWorkspaceDetailSource = ref<SourceEnvelope<OperatorWorkspaceDTO> | null>(null);
const selectedOperatorWorkspaceId = ref("");
const launchOperation = ref<WorkspaceLaunchResponse | null>(null);
const revealedApiKey = ref<GatewayKeySecretDTO | null>(null);
const revealedWorkspaceCredentials = ref<WorkspaceCredentialAccess | null>(null);
const gatewayPageNumber = reactive({ page: 1, pages: 0, total: 0, pageSize: 20 });
const operatorAccountPage = ref(1);
const operatorWorkspacePage = ref(1);
const operatorPageSize = 20;
const gatewayPeriod = ref("month");
const selectedUsageKeyId = ref("");
const receiptCursor = ref("");
const receiptCursorStack = ref<string[]>([]);
const selectedReceiptId = ref("");
const sidebarOpen = ref(false);
const modal = ref<"workspace" | "api-key" | "admin-user" | "wallet-adjustment" | "announcement" | "">("");
const mutationBusy = ref(false);
const gatewayBusy = ref(false);
const announcementBusy = ref("");
const runtimeBusy = ref(false);
const launchBusy = ref(false);
const launchPollIssue = ref<"" | "error" | "timeout">("");
const toast = reactive({ text: "", tone: "good" });
const loginForm = reactive({ email: "", password: "" });
const loginBusy = ref(false);
const loginError = ref("");
const launchForm = reactive<{ name: string; packageId: PlanId }>({ name: "", packageId: "basic" });
const keyForm = reactive({ name: "", quotaUsd: 10, expiresInDays: 30 });
const adminUserForm = reactive({ email: "", password: "", name: "" });
const walletAdjustmentForm = reactive<WalletAdjustmentRequest>({ kind: "recharge", amountUsd: "", reason: "", confirmationAccountId: "", relatedOperationId: "" });
const announcementForm = reactive<AnnouncementDraftRequest>({ title: "", body: "", startsAt: "", endsAt: "" });
const selectedOperatorAccountId = ref("");
const selectedReview = ref<OperatorReconciliationItemDTO | null>(null);
const loading = reactive({ workspace: false, runtime: false, wallet: false, keys: false, usage: false, stats: false, accountStats: false, history: false, receipts: false, receiptDetail: false, announcements: false, catalog: false, accounts: false, admin: false, readiness: false, operatorOverview: false, operatorAccounts: false, operatorWorkspaces: false, operatorWorkspaceDetail: false, operatorReconciliation: false, operatorHealth: false, operatorAnnouncements: false, walletAdjustment: false, review: false });
const errors = reactive({ workspace: "", runtime: "", wallet: "", keys: "", usage: "", stats: "", accountStats: "", history: "", receipts: "", receiptDetail: "", announcements: "", catalog: "", accounts: "", admin: "", readiness: "", operatorOverview: "", operatorAccounts: "", operatorWorkspaces: "", operatorWorkspaceDetail: "", operatorReconciliation: "", operatorHealth: "", operatorAnnouncements: "", walletAdjustment: "", review: "" });
let toastTimer: number | undefined;
let secretTimer: number | undefined;
let secretRequestGeneration = 0;
let sessionGeneration = 0;
let usageRequestGeneration = 0;
let usageStatsRequestGeneration = 0;
let receiptRequestGeneration = 0;
let receiptDetailRequestGeneration = 0;
let launchPollGeneration = 0;
let workspaceLaunchIntent: { input: WorkspaceLaunchRequest; idempotencyKey: string } | null = null;
let runtimeRotationIntent: { workspaceId: string; idempotencyKey: string } | null = null;
let gatewayKeyCreateIntent: { input: CreateGatewayKeyRequest; idempotencyKey: string } | null = null;
let walletAdjustmentIntent: { accountId: string; input: WalletAdjustmentRequest; idempotencyKey: string } | null = null;
let operatorProvisionIntent: { input: ProvisionAccountRequest; idempotencyKey: string } | null = null;
let billingReviewIntent: { resourceType: string; resourceId: string; input: BillingReviewResolutionRequest; idempotencyKey: string } | null = null;
let workspaceLaunchRecoveryIntent: { operationId: string; input: WorkspaceLaunchRecoveryRequest; idempotencyKey: string } | null = null;
let announcementCreateIntent: { input: AnnouncementDraftRequest; idempotencyKey: string } | null = null;
const gatewayKeyToggleIntents = new Map<string, { targetStatus: GatewayKeySummaryDTO["status"]; idempotencyKey: string }>();
const gatewayKeyDeleteIntents = new Map<string, string>();
const operatorDisableIntents = new Map<string, string>();
const announcementPublishIntents = new Map<string, { input: AnnouncementScheduleRequest; idempotencyKey: string }>();
const announcementWithdrawIntents = new Map<string, string>();

const isAdminRoute = computed(() => path.value === "/admin" || path.value.startsWith("/admin/"));
const isLoginRoute = computed(() => path.value === "/login");
const isForbidden = computed(() => path.value === "/403");
const isPublicRoute = computed(() => !needsSession(path.value) && !isLoginRoute.value && !isForbidden.value);
const isOperator = computed(() => session.value?.isOperator === true);
const apiRoute = computed(() => path.value === "/console/api" || path.value.startsWith("/console/api/") || path.value.startsWith("/console/gateway"));
const activeApiPage = computed(() => apiPage(path.value.replace("/console/gateway", "/console/api")));
const plans = computed(() => (catalog.value?.packages || []).filter((plan) => plan.available && (plan.id === "basic" || plan.id === "pro")));
const selectedPlan = computed(() => plans.value.find((plan) => plan.id === launchForm.packageId) || null);
const selectedPlanPrice = computed(() => {
  const value = selectedPlan.value ? previews[selectedPlan.value.id]?.totalChargeUsdMicros : undefined;
  return typeof value === "number" && Number.isSafeInteger(value) ? value : null;
});
const workspace = computed<WorkspaceDTO | null>(() => {
  if (!workspaceSource.value?.available || workspaceSource.value.data.items.length !== 1) return null;
  return workspaceSource.value.data.items[0];
});
const workspacePlan = computed(() => catalog.value?.packages.find((plan) => plan.id === workspace.value?.packageId) || null);
const runtime = computed(() => workspaceStatusSource.value?.available ? workspaceStatusSource.value.data : null);
const mountCheck = computed(() => runtime.value?.checks.find((check) => check.name === "ready_pod_uses_retained_pvc") || null);
const wallet = computed(() => walletSource.value?.available ? walletSource.value.data : null);
const keys = computed(() => keySource.value?.available ? keySource.value.data.items : []);
const workspaceKeyId = computed(() => workspace.value?.workspaceApiKeyId || "");
const usage = computed(() => usageSource.value?.available ? usageSource.value.data : null);
const keyStats = computed(() => usageStatsSource.value?.available ? usageStatsSource.value.data : null);
const stats = computed(() => accountUsageSource.value?.available ? accountUsageSource.value.data : null);
const history = computed(() => balanceHistorySource.value?.available ? balanceHistorySource.value.data.items : []);
const receipts = computed(() => receiptsSource.value?.available ? receiptsSource.value.data.receipts : []);
const receiptDetail = computed(() => receiptDetailSource.value?.available ? receiptDetailSource.value.data : null);
const announcements = computed(() => announcementsSource.value?.available ? announcementsSource.value.data.items : []);
const announcementsUnavailable = computed(() => announcementsSource.value?.status === "unavailable");
const announcementsEmpty = computed(() => announcementsSource.value?.status === "empty");
const operatorOverview = computed(() => operatorOverviewSource.value?.available ? operatorOverviewSource.value.data : null);
const operatorAccountRows = computed(() => operatorAccountsPageSource.value?.available ? operatorAccountsPageSource.value.data.items : []);
const operatorWorkspaceRows = computed(() => operatorWorkspacesSource.value?.available ? operatorWorkspacesSource.value.data.items : []);
const operatorReconciliationRows = computed(() => operatorReconciliationSource.value?.available ? operatorReconciliationSource.value.data.items : []);
const operatorAnnouncementRows = computed(() => operatorAnnouncementsSource.value?.available ? operatorAnnouncementsSource.value.data.items : []);
const operatorAccountPages = computed(() => operatorAccountsPageSource.value?.available
  ? Math.max(1, Math.ceil(operatorAccountsPageSource.value.data.total / operatorPageSize))
  : 0);
const operatorWorkspacePages = computed(() => operatorWorkspacesSource.value?.available
  ? Math.max(1, Math.ceil(operatorWorkspacesSource.value.data.total / operatorPageSize))
  : 0);
const operatorResourceRows = computed(() => operatorWorkspaceDetailSource.value?.available
  ? operatorWorkspaceDetailSource.value.data.resources
  : operatorWorkspaceRows.value.flatMap((item) => item.resources || []));
const operatorHealthRows = computed(() => {
  const health = operatorHealthSource.value?.available ? operatorHealthSource.value.data : null;
  if (!health) return [];
  return [
    ["控制面", health.controlPlane],
    ["API 服务", health.gateway],
    ["资源服务", health.fabric],
    ["Workspace 服务", health.runtime],
    ["账单记录", health.ledger]
  ] as const;
});
const pageTitle = computed(() => {
  if (path.value.startsWith("/console/workspace")) return "Workspace";
  if (apiRoute.value) return "API 服务";
  if (path.value.startsWith("/console/billing")) return "账单";
  if (path.value.startsWith("/console/announcements")) return "公告";
  if (path.value.startsWith("/admin/accounts")) return "用户与计费账户";
  if (path.value.startsWith("/admin/billing")) return "计费复核";
  if (path.value.startsWith("/admin/resources")) return "资源状态";
  if (path.value.startsWith("/admin/system")) return "系统状态";
  if (isAdminRoute.value) return "运维概览";
  return "概览";
});
const workspaceCanOpen = computed(() => runtime.value?.status === "running" && runtime.value.ready === true && Boolean(runtime.value.url));
const launchStatusText = computed(() => {
  if (launchPollIssue.value === "error") return "Workspace 状态读取失败";
  if (launchPollIssue.value === "timeout") return "Workspace 仍在处理中，请稍后重试";
  const status = launchOperation.value?.status;
  if (!status) return "";
  if (status === "succeeded") return "Workspace 已开通";
  if (status === "failed") return "Workspace 开通失败";
  if (status === "refunded") return "Workspace 开通未完成，已退款";
  if (status === "manual_review") return "Workspace 正在人工复核";
  if (status === "preparing") return "Workspace 正在处理";
  return "暂不可用";
});

function navigate(next: string) {
  const normalized = next.startsWith("/console/gateway") ? next.replace("/console/gateway", "/console/api") : next;
  window.history.pushState({}, "", normalized);
  path.value = window.location.pathname;
  sidebarOpen.value = false;
}

function isSensitiveRoute(route: string) {
  return route.startsWith("/console/api") || route.startsWith("/console/gateway") || route.startsWith("/console/workspace");
}

function openWorkspace() {
  if (workspaceCanOpen.value && runtime.value?.url) window.open(runtime.value.url, "_blank", "noopener,noreferrer");
}

function flash(text: string, tone = "good") {
  toast.text = text;
  toast.tone = tone;
  if (toastTimer) window.clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => { toast.text = ""; }, 3200);
}

function friendlyError(error: unknown): string {
  const raw = String(error && typeof error === "object" && "message" in error ? error.message : error || "操作失败");
  const messages: Record<string, string> = {
    not_authenticated: "登录已失效，请重新登录",
    account_scope_forbidden: "没有权限访问该资源",
    insufficient_balance: "可用余额不足",
    gateway_key_missing: "API Key 尚未就绪",
    gateway_key_ambiguous: "API Key 状态异常，请联系管理员",
    monthly_account_unmapped: "API 服务尚未开通",
    authentication_unavailable: "身份服务暂不可用，请稍后重试",
    workspace_credentials_unavailable: "Workspace 凭证暂不可用",
    workspace_not_running: "Workspace 尚未就绪",
    upstream_unavailable: "服务暂不可用，请稍后重试"
  };
  return messages[raw] || (raw.includes("failed") || raw.includes("_") ? "请求失败，请重试" : raw);
}

function apiErrorCode(error: unknown): string {
  const payload = error && typeof error === "object" && "payload" in error
    ? (error as { payload?: unknown }).payload
    : null;
  return payload && typeof payload === "object" ? String((payload as { error?: unknown }).error || "") : "";
}

function mutationError(error: unknown) {
  const code = apiErrorCode(error);
  return code ? friendlyError(code) : "结果待确认，请刷新操作状态，不要重复提交";
}

function clearSecrets() {
  secretRequestGeneration += 1;
  if (secretTimer) window.clearTimeout(secretTimer);
  secretTimer = undefined;
  revealedApiKey.value = maskGatewayKey(revealedApiKey.value);
  revealedApiKey.value = null;
  revealedWorkspaceCredentials.value = null;
}

function armSecretTimeout() {
  if (secretTimer) window.clearTimeout(secretTimer);
  secretTimer = window.setTimeout(clearSecrets, secretLifetimeMs);
}

function secretResponseStillCurrent(generation: number, requestPath: string, userId: string, workspaceId = "") {
  return generation === secretRequestGeneration && path.value === requestPath && session.value?.user.id === userId
    && (!workspaceId || workspace.value?.id === workspaceId);
}

function resetSource<K extends keyof typeof errors>(key: K) {
  errors[key] = "";
}

function unavailableSource<T>(source: string): SourceEnvelope<T> {
  return { source, status: "unavailable", available: false, fetchedAt: "" };
}

function currentSessionRequest() {
  const generation = sessionGeneration;
  const userId = session.value?.user.id;
  return () => generation === sessionGeneration && userId === session.value?.user.id;
}

function closeModal() {
  if (modal.value === "admin-user") operatorProvisionIntent = null;
  if (modal.value === "announcement") announcementCreateIntent = null;
  Object.assign(launchForm, { name: "", packageId: "basic" });
  Object.assign(keyForm, { name: "", quotaUsd: 10, expiresInDays: 30 });
  Object.assign(adminUserForm, { email: "", password: "", name: "" });
  Object.assign(walletAdjustmentForm, { kind: "recharge", amountUsd: "", reason: "", confirmationAccountId: "", relatedOperationId: "" });
  Object.assign(announcementForm, { title: "", body: "", startsAt: "", endsAt: "" });
  selectedOperatorAccountId.value = "";
  selectedReview.value = null;
  modal.value = "";
}

function clearReceiptDetail() {
  receiptDetailRequestGeneration += 1;
  selectedReceiptId.value = "";
  receiptDetailSource.value = null;
  loading.receiptDetail = false;
  errors.receiptDetail = "";
}

function clearSessionState() {
  clearSecrets();
  closeModal();
  clearReceiptDetail();
  launchPollGeneration += 1;
  usageRequestGeneration += 1;
  usageStatsRequestGeneration += 1;
  receiptRequestGeneration += 1;
  workspaceSource.value = null;
  workspaceStatusSource.value = null;
  walletSource.value = null;
  keySource.value = null;
  usageSource.value = null;
  usageStatsSource.value = null;
  accountUsageSource.value = null;
  balanceHistorySource.value = null;
  receiptsSource.value = null;
  announcementsSource.value = null;
  catalog.value = null;
  for (const id of ["basic", "pro"] as const) delete previews[id];
  operatorOverviewSource.value = null;
  operatorAccountsPageSource.value = null;
  operatorWorkspacesSource.value = null;
  operatorReconciliationSource.value = null;
  operatorHealthSource.value = null;
  operatorAnnouncementsSource.value = null;
  operatorWorkspaceDetailSource.value = null;
  selectedOperatorWorkspaceId.value = "";
  walletAdjustmentOperation.value = null;
  launchOperation.value = null;
  launchPollIssue.value = "";
  selectedUsageKeyId.value = "";
  Object.assign(gatewayPageNumber, { page: 1, pages: 0, total: 0 });
  operatorAccountPage.value = 1;
  operatorWorkspacePage.value = 1;
  receiptCursor.value = "";
  receiptCursorStack.value = [];
  workspaceLaunchIntent = null;
  runtimeRotationIntent = null;
  gatewayKeyCreateIntent = null;
  walletAdjustmentIntent = null;
  operatorProvisionIntent = null;
  billingReviewIntent = null;
  workspaceLaunchRecoveryIntent = null;
  announcementCreateIntent = null;
  gatewayKeyToggleIntents.clear();
  gatewayKeyDeleteIntents.clear();
  operatorDisableIntents.clear();
  announcementPublishIntents.clear();
  announcementWithdrawIntents.clear();
  selectedOperatorAccountId.value = "";
  selectedReview.value = null;
  mutationBusy.value = false;
  gatewayBusy.value = false;
  announcementBusy.value = "";
  runtimeBusy.value = false;
  launchBusy.value = false;
  loginBusy.value = false;
  loginError.value = "";
  loginForm.email = "";
  loginForm.password = "";
  for (const key of Object.keys(loading) as Array<keyof typeof loading>) loading[key] = false;
  for (const key of Object.keys(errors) as Array<keyof typeof errors>) errors[key] = "";
}

function replaceSession(next: AuthSession | null) {
  sessionGeneration += 1;
  clearSessionState();
  session.value = next;
}

async function loadWorkspaces() {
  const requestStillCurrent = currentSessionRequest();
  const currentWorkspaceId = workspace.value?.id
    || (workspaceStatusSource.value?.available ? workspaceStatusSource.value.data.workspaceId : "");
  loading.workspace = true;
  resetSource("workspace");
  workspaceSource.value = null;
  try {
    const result = await getWorkspaces();
    if (!requestStillCurrent()) return;
    workspaceSource.value = result;
    const nextWorkspaceId = result.available && result.data.items.length === 1 ? result.data.items[0]?.id || "" : "";
    if (result.status === "empty" || (nextWorkspaceId && nextWorkspaceId !== currentWorkspaceId)) workspaceStatusSource.value = null;
    if (workspaceSource.value.available && workspaceSource.value.data.items.length > 1) errors.workspace = "账号存在多个 Workspace，暂不可用";
  } catch (error) {
    if (!requestStillCurrent()) return;
    workspaceSource.value = unavailableSource<WorkspaceListData>("control-plane");
    errors.workspace = friendlyError(error);
  } finally {
    if (requestStillCurrent()) loading.workspace = false;
  }
}

async function loadWorkspaceStatus() {
  const requestStillCurrent = currentSessionRequest();
  const current = workspace.value;
  if (!current) {
    if (workspaceSource.value?.status === "empty") workspaceStatusSource.value = null;
    return;
  }
  loading.runtime = true;
  resetSource("runtime");
  workspaceStatusSource.value = unavailableSource<WorkspaceRuntimeDTO>("fabric");
  try {
    const result = await getWorkspaceRuntimeStatus(current.id);
    if (!requestStillCurrent()) return;
    workspaceStatusSource.value = result;
  } catch (error) {
    if (!requestStillCurrent()) return;
    workspaceStatusSource.value = unavailableSource<WorkspaceRuntimeDTO>("fabric");
    errors.runtime = friendlyError(error);
  } finally { if (requestStillCurrent()) loading.runtime = false; }
}

async function loadWallet() {
  const requestStillCurrent = currentSessionRequest();
  loading.wallet = true;
  resetSource("wallet");
  walletSource.value = unavailableSource<GatewayWallet>("sub2api");
  try {
    const result = await getGatewayWallet();
    if (!requestStillCurrent()) return;
    walletSource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; walletSource.value = unavailableSource<GatewayWallet>("sub2api"); errors.wallet = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.wallet = false; }
}

async function loadKeys() {
  const requestStillCurrent = currentSessionRequest();
  clearSecrets();
  loading.keys = true;
  resetSource("keys");
  keySource.value = unavailableSource<GatewayKeyPageDTO>("sub2api");
  try {
    const result = await getGatewayKeys();
    if (!requestStillCurrent()) return;
    keySource.value = result;
    if (!result.available) return;
    if (!result.data.items.some((key) => key.id === selectedUsageKeyId.value)) {
      selectUsageKey(result.data.items[0]?.id || "");
      return;
    }
    if (activeApiPage.value === "usage") void Promise.all([loadUsage(), loadStats()]);
  }
  catch (error) {
    if (!requestStillCurrent()) return;
    keySource.value = unavailableSource<GatewayKeyPageDTO>("sub2api");
    errors.keys = friendlyError(error);
  }
  finally { if (requestStillCurrent()) loading.keys = false; }
}

async function loadUsage(pageOrEvent: number | Event = gatewayPageNumber.page) {
  const sessionRequestStillCurrent = currentSessionRequest();
  const generation = ++usageRequestGeneration;
  const page = typeof pageOrEvent === "number" ? pageOrEvent : gatewayPageNumber.page;
  const keyId = selectedUsageKeyId.value;
  gatewayPageNumber.page = page;
  const requestStillCurrent = () => sessionRequestStillCurrent() && generation === usageRequestGeneration
    && keyId === selectedUsageKeyId.value && page === gatewayPageNumber.page;
  if (!keyId) { usageSource.value = null; loading.usage = false; resetSource("usage"); return; }
  loading.usage = true;
  resetSource("usage");
  usageSource.value = unavailableSource<GatewayKeyUsagePageDTO>("sub2api");
  gatewayPageNumber.pages = 0;
  gatewayPageNumber.total = 0;
  try {
    const result = await getGatewayKeyUsage(keyId, page, gatewayPageNumber.pageSize);
    if (!requestStillCurrent()) return;
    if (result.available && result.data.page !== page) throw new Error("gateway_usage_page_mismatch");
    usageSource.value = result;
    if (usageSource.value.available) {
      gatewayPageNumber.pages = usageSource.value.data.pages;
      gatewayPageNumber.total = usageSource.value.data.total;
    }
  } catch (error) {
    if (!requestStillCurrent()) return;
    usageSource.value = unavailableSource<GatewayKeyUsagePageDTO>("sub2api");
    errors.usage = friendlyError(error);
  }
  finally { if (requestStillCurrent()) loading.usage = false; }
}

async function loadStats() {
  const sessionRequestStillCurrent = currentSessionRequest();
  const generation = ++usageStatsRequestGeneration;
  const keyId = selectedUsageKeyId.value;
  const period = gatewayPeriod.value;
  const requestStillCurrent = () => sessionRequestStillCurrent() && generation === usageStatsRequestGeneration
    && keyId === selectedUsageKeyId.value && period === gatewayPeriod.value;
  if (!keyId) { usageStatsSource.value = null; loading.stats = false; resetSource("stats"); return; }
  loading.stats = true;
  resetSource("stats");
  usageStatsSource.value = unavailableSource<GatewayUsageSummaryDTO>("sub2api");
  try {
    const result = await getGatewayKeyUsageSummary(keyId, period);
    if (!requestStillCurrent()) return;
    usageStatsSource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; usageStatsSource.value = unavailableSource<GatewayUsageSummaryDTO>("sub2api"); errors.stats = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.stats = false; }
}

async function loadAccountUsage() {
  const requestStillCurrent = currentSessionRequest();
  loading.accountStats = true;
  resetSource("accountStats");
  accountUsageSource.value = unavailableSource<GatewayAccountUsageSummaryDTO>("sub2api");
  try {
    const result = await getGatewayAccountUsageSummary("month");
    if (!requestStillCurrent()) return;
    accountUsageSource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; accountUsageSource.value = unavailableSource<GatewayAccountUsageSummaryDTO>("sub2api"); errors.accountStats = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.accountStats = false; }
}

async function loadHistory() {
  const requestStillCurrent = currentSessionRequest();
  loading.history = true;
  resetSource("history");
  balanceHistorySource.value = unavailableSource<BalanceHistoryData>("sub2api");
  try {
    const result = await getGatewayBalanceHistory();
    if (!requestStillCurrent()) return;
    balanceHistorySource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; balanceHistorySource.value = unavailableSource<BalanceHistoryData>("sub2api"); errors.history = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.history = false; }
}

async function loadReceipts(cursorOrEvent: string | Event = "") {
  const sessionRequestStillCurrent = currentSessionRequest();
  const cursor = typeof cursorOrEvent === "string" ? cursorOrEvent : receiptCursor.value;
  const generation = ++receiptRequestGeneration;
  receiptCursor.value = cursor;
  if (!cursor) receiptCursorStack.value = [];
  const requestStillCurrent = () => sessionRequestStillCurrent() && generation === receiptRequestGeneration && cursor === receiptCursor.value;
  clearReceiptDetail();
  loading.receipts = true;
  resetSource("receipts");
  receiptsSource.value = unavailableSource<BillingReceiptPage>("ledger");
  try {
    const result = await getBillingReceipts(cursor);
    if (!requestStillCurrent()) return;
    receiptsSource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; receiptsSource.value = unavailableSource<BillingReceiptPage>("ledger"); errors.receipts = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.receipts = false; }
}

async function loadReceiptDetail(receiptId: string) {
  if (!receiptId) { clearReceiptDetail(); return; }
  const sessionRequestStillCurrent = currentSessionRequest();
  const generation = ++receiptDetailRequestGeneration;
  selectedReceiptId.value = receiptId;
  const requestStillCurrent = () => sessionRequestStillCurrent() && generation === receiptDetailRequestGeneration
    && receiptId === selectedReceiptId.value;
  loading.receiptDetail = true;
  resetSource("receiptDetail");
  receiptDetailSource.value = unavailableSource<BillingReceipt>("ledger");
  try {
    const result = await getBillingReceipt(receiptId);
    if (!requestStillCurrent()) return;
    if (result.available && result.data.receiptId !== receiptId) throw new Error("billing_receipt_identity_mismatch");
    receiptDetailSource.value = result;
  } catch (error) {
    if (!requestStillCurrent()) return;
    receiptDetailSource.value = unavailableSource<BillingReceipt>("ledger");
    errors.receiptDetail = friendlyError(error);
  } finally { if (requestStillCurrent()) loading.receiptDetail = false; }
}

function nextReceiptPage() {
  if (!receiptsSource.value?.available || !receiptsSource.value.data.hasMore) return;
  const nextCursor = receiptsSource.value.data.nextCursor;
  if (!nextCursor) return;
  receiptCursorStack.value.push(receiptCursor.value);
  void loadReceipts(nextCursor);
}

function previousReceiptPage() {
  const previousCursor = receiptCursorStack.value.pop();
  if (previousCursor === undefined) return;
  void loadReceipts(previousCursor);
}

async function loadAnnouncements() {
  const requestStillCurrent = currentSessionRequest();
  loading.announcements = true;
  resetSource("announcements");
  announcementsSource.value = unavailableSource<AnnouncementPageDTO>("control-plane");
  try {
    const result = await getAnnouncements();
    if (!requestStillCurrent()) return;
    announcementsSource.value = result;
  }
  catch (error) { if (!requestStillCurrent()) return; announcementsSource.value = unavailableSource<AnnouncementPageDTO>("control-plane"); errors.announcements = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.announcements = false; }
}

async function loadCatalog() {
  const requestStillCurrent = currentSessionRequest();
  loading.catalog = true;
  resetSource("catalog");
  catalog.value = null;
  for (const id of ["basic", "pro"] as const) delete previews[id];
  try {
    const nextCatalog = await getPricingCatalog();
    if (!requestStillCurrent()) return;
    catalog.value = nextCatalog;
    await Promise.all(plans.value.map(async (plan) => {
      const preview = await previewPricing({ resourceType: "workspace", packageId: plan.id, sizeGb: plan.diskGb }, session.value?.csrfToken || "");
      if (requestStillCurrent() && typeof preview.totalChargeUsdMicros === "number") previews[plan.id] = preview as WorkspacePricePreview;
    }));
  } catch (error) { if (!requestStillCurrent()) return; catalog.value = null; errors.catalog = friendlyError(error); }
  finally { if (requestStillCurrent()) loading.catalog = false; }
}

async function loadCustomer() {
  const requestStillCurrent = currentSessionRequest();
  if (apiRoute.value) {
    if (activeApiPage.value === "overview") await Promise.all([loadWallet(), loadAccountUsage(), loadHistory()]);
    else await loadKeys();
    return;
  }
  if (path.value.startsWith("/console/announcements")) {
    await loadAnnouncements();
    return;
  }
  if (path.value.startsWith("/console/billing")) {
    await Promise.all([loadWorkspaces(), loadWallet(), loadAccountUsage(), loadHistory(), loadReceipts()]);
    return;
  }
  const overview = path.value === "/console" || path.value === "/console/overview";
  await Promise.all(overview
    ? [loadWorkspaces(), loadWallet(), loadAccountUsage(), loadReceipts(), loadCatalog(), loadAnnouncements()]
    : [loadWorkspaces(), loadReceipts(), loadCatalog()]);
  if (!requestStillCurrent()) return;
  await Promise.all([loadWorkspaceStatus(), recoverWorkspaceLaunch()]);
}

async function loadAdmin() {
  const requestStillCurrent = currentSessionRequest();
  loading.admin = true;
  loading.operatorOverview = true;
  loading.operatorAccounts = true;
  loading.operatorWorkspaces = true;
  loading.operatorReconciliation = true;
  loading.operatorHealth = true;
  loading.operatorAnnouncements = true;
  resetSource("admin");
  resetSource("operatorOverview");
  resetSource("operatorAccounts");
  resetSource("operatorWorkspaces");
  resetSource("operatorReconciliation");
  resetSource("operatorHealth");
  resetSource("operatorAnnouncements");
  operatorOverviewSource.value = unavailableSource<OperatorOverviewDTO>("control-plane");
  operatorAccountsPageSource.value = unavailableSource<OperatorAccountPageDTO>("control-plane+sub2api");
  operatorWorkspacesSource.value = unavailableSource<OperatorWorkspacePageDTO>("control-plane+fabric+sub2api");
  operatorReconciliationSource.value = unavailableSource<OperatorReconciliationPageDTO>("control-plane");
  operatorHealthSource.value = unavailableSource<OperatorHealthDTO>("control-plane");
  operatorAnnouncementsSource.value = unavailableSource<OperatorAnnouncementPageDTO>("control-plane");
  const [overviewResult, accountsResult, workspacesResult, reconciliationResult, healthResult, announcementsResult] = await Promise.allSettled([
    getOperatorOverview(), getOperatorAccountsPage(operatorAccountPage.value, operatorPageSize), getOperatorWorkspaces(operatorWorkspacePage.value, operatorPageSize), getOperatorReconciliation(), getOperatorHealth(), getOperatorAnnouncements()
  ]);
  if (!requestStillCurrent()) return;
  if (overviewResult.status === "fulfilled") operatorOverviewSource.value = overviewResult.value;
  else errors.operatorOverview = friendlyError(overviewResult.reason);
  if (accountsResult.status === "fulfilled") operatorAccountsPageSource.value = accountsResult.value;
  else errors.operatorAccounts = friendlyError(accountsResult.reason);
  if (workspacesResult.status === "fulfilled") operatorWorkspacesSource.value = workspacesResult.value;
  else errors.operatorWorkspaces = friendlyError(workspacesResult.reason);
  if (reconciliationResult.status === "fulfilled") operatorReconciliationSource.value = reconciliationResult.value;
  else errors.operatorReconciliation = friendlyError(reconciliationResult.reason);
  if (healthResult.status === "fulfilled") operatorHealthSource.value = healthResult.value;
  else errors.operatorHealth = friendlyError(healthResult.reason);
  if (announcementsResult.status === "fulfilled") operatorAnnouncementsSource.value = announcementsResult.value;
  else errors.operatorAnnouncements = friendlyError(announcementsResult.reason);
  loading.admin = false;
  loading.operatorOverview = false;
  loading.operatorAccounts = false;
  loading.operatorWorkspaces = false;
  loading.operatorReconciliation = false;
  loading.operatorHealth = false;
  loading.operatorAnnouncements = false;
}

async function changeOperatorAccountPage(page: number) {
  if (page < 1 || (operatorAccountPages.value > 0 && page > operatorAccountPages.value) || page === operatorAccountPage.value) return;
  operatorAccountPage.value = page;
  await loadAdmin();
}

async function changeOperatorWorkspacePage(page: number) {
  if (page < 1 || (operatorWorkspacePages.value > 0 && page > operatorWorkspacePages.value) || page === operatorWorkspacePage.value) return;
  operatorWorkspacePage.value = page;
  await loadAdmin();
}

async function loadOperatorWorkspaceDetail(workspaceId: string) {
  const requestStillCurrent = currentSessionRequest();
  selectedOperatorWorkspaceId.value = workspaceId;
  loading.operatorWorkspaceDetail = true;
  errors.operatorWorkspaceDetail = "";
  operatorWorkspaceDetailSource.value = unavailableSource<OperatorWorkspaceDTO>("control-plane+fabric+ledger");
  try {
    const result = await getOperatorWorkspace(workspaceId);
    if (!requestStillCurrent() || selectedOperatorWorkspaceId.value !== workspaceId) return;
    operatorWorkspaceDetailSource.value = result;
  } catch (error) {
    if (requestStillCurrent() && selectedOperatorWorkspaceId.value === workspaceId) errors.operatorWorkspaceDetail = friendlyError(error);
  } finally {
    if (requestStillCurrent() && selectedOperatorWorkspaceId.value === workspaceId) loading.operatorWorkspaceDetail = false;
  }
}

async function ensureSession(): Promise<boolean> {
  const requestStillCurrent = currentSessionRequest();
  authStatus.value = "checking";
  authError.value = "";
  try {
    const next = await currentSession();
    if (!requestStillCurrent()) return false;
    if (!next) {
      replaceSession(null);
      navigate(`/login?redirect=${encodeURIComponent(window.location.pathname + window.location.search)}`);
      return false;
    }
    replaceSession(next);
    if (isAdminRoute.value && next.isOperator !== true) { navigate("/403"); return false; }
    authStatus.value = "ready";
    return true;
  } catch (error) {
    if (!requestStillCurrent()) return false;
    authStatus.value = "error";
    authError.value = friendlyError(error);
    return false;
  }
}

async function handleRoute() {
  if (!needsSession(path.value)) { authStatus.value = "public"; return; }
  if (!session.value && !(await ensureSession())) return;
  if (isAdminRoute.value && !isOperator.value) { navigate("/403"); return; }
  authStatus.value = "ready";
  if (isAdminRoute.value) {
    await loadAdmin();
  } else {
    await loadCustomer();
  }
}

async function submitLogin() {
  const requestStillCurrent = currentSessionRequest();
  loginBusy.value = true;
  loginError.value = "";
  try {
    const next = await login(loginForm);
    if (!requestStillCurrent()) return;
    replaceSession(next);
    loginForm.password = "";
    authStatus.value = "ready";
    const requested = new URLSearchParams(window.location.search).get("redirect");
    navigate(requested?.startsWith("/") ? requested : defaultAuthenticatedRoute());
  } catch (error) { if (requestStillCurrent()) loginError.value = friendlyError(error); }
  finally { if (requestStillCurrent()) loginBusy.value = false; }
}

async function signOut() {
  const csrf = session.value?.csrfToken || "";
  clearSecrets();
  launchPollGeneration += 1;
  try {
    await logoutLocalFirst(csrf, () => {
      replaceSession(null);
    }, () => navigate("/"));
  } catch {
    // Local logout and navigation have already completed.
  }
}

function openModal(next: "workspace" | "api-key" | "admin-user" | "wallet-adjustment" | "announcement") {
  modal.value = next;
  if (next === "workspace") launchForm.name = workspace.value?.name || "";
  if (next === "wallet-adjustment") walletAdjustmentForm.confirmationAccountId = selectedOperatorAccountId.value;
}

function sleep(milliseconds: number) {
  return new Promise<void>((resolve) => { window.setTimeout(resolve, milliseconds); });
}

async function pollWorkspaceLaunch(operationId: string) {
  const requestStillCurrent = currentSessionRequest();
  const generation = ++launchPollGeneration;
  launchPollIssue.value = "";
  for (let attempt = 0; attempt < workspaceLaunchPollAttempts; attempt += 1) {
    await sleep(workspaceLaunchPollIntervalMs);
    if (generation !== launchPollGeneration || !requestStillCurrent()) return;
    try {
      const next = await getWorkspaceLaunch(operationId);
      if (generation !== launchPollGeneration || !requestStillCurrent()) return;
      launchOperation.value = next;
      if (next.status === "manual_review") return;
      if (isTerminalWorkspaceLaunch(next.status)) {
        await Promise.all([loadWorkspaces(), loadReceipts()]);
        await loadWorkspaceStatus();
        if (next.status === "succeeded") flash("Workspace 已开通");
        else if (next.status === "refunded") flash("Workspace 未完成，已退款", "danger");
        return;
      }
    } catch (error) {
      if (generation === launchPollGeneration && requestStillCurrent()) {
        launchPollIssue.value = "error";
        flash(friendlyError(error), "danger");
      }
      return;
    }
  }
  if (generation === launchPollGeneration && requestStillCurrent()) launchPollIssue.value = "timeout";
}

async function recoverWorkspaceLaunch() {
  const requestStillCurrent = currentSessionRequest();
  launchPollGeneration += 1;
  launchPollIssue.value = "";
  try {
    const launches = await getWorkspaceLaunches();
    if (!requestStillCurrent()) return;
    if (launches.length === 0) { launchOperation.value = null; return; }
    if (launches.length !== 1 || !launches[0]?.operationId) {
      launchPollIssue.value = "error";
      return;
    }
    launchOperation.value = launches[0];
    if (!isTerminalWorkspaceLaunch(launches[0].status) && launches[0].status !== "manual_review") {
      void pollWorkspaceLaunch(launches[0].operationId);
    }
  } catch {
    if (requestStillCurrent()) launchPollIssue.value = "error";
  }
}

function retryWorkspaceLaunchPoll() {
  const operation = launchOperation.value;
  if (operation?.operationId && !isTerminalWorkspaceLaunch(operation.status) && operation.status !== "manual_review") {
    void pollWorkspaceLaunch(operation.operationId);
  } else {
    void recoverWorkspaceLaunch();
  }
}

function sameWorkspaceLaunchRequest(left: WorkspaceLaunchRequest, right: WorkspaceLaunchRequest) {
  return left.name === right.name && left.packageId === right.packageId && left.sizeGb === right.sizeGb && left.autoRenew === right.autoRenew;
}

function unknownWorkspaceLaunchResult(error: unknown) {
  const payload = error && typeof error === "object" && "payload" in error
    ? (error as { payload?: unknown }).payload
    : null;
  return Boolean(payload && typeof payload === "object" && "status" in payload && payload.status === "unknown");
}

async function submitWorkspaceLaunch() {
  const requestStillCurrent = currentSessionRequest();
  const plan = selectedPlan.value;
  const name = launchForm.name.trim();
  if (!plan || selectedPlanPrice.value === null || !name || mutationBusy.value) return;
  mutationBusy.value = true;
  launchBusy.value = true;
  launchOperation.value = null;
  try {
    const input: WorkspaceLaunchRequest = {
      name,
      packageId: plan.id,
      sizeGb: plan.id === "basic" ? 10 : 100,
      autoRenew: false
    };
    if (!workspaceLaunchIntent || !sameWorkspaceLaunchRequest(workspaceLaunchIntent.input, input)) {
      workspaceLaunchIntent = { input, idempotencyKey: workspaceLaunchIdempotencyKey() };
    }
    const created = await launchWorkspace(input, session.value?.csrfToken || "", workspaceLaunchIntent.idempotencyKey);
    if (!requestStillCurrent()) return;
    workspaceLaunchIntent = null;
    launchOperation.value = created;
    launchPollIssue.value = "";
    closeModal();
    if (!terminalStatuses.has(created.status) && created.status !== "manual_review") void pollWorkspaceLaunch(created.operationId);
    await Promise.all([loadWorkspaces(), loadReceipts()]);
    if (!requestStillCurrent()) return;
    await loadWorkspaceStatus();
    if (!requestStillCurrent()) return;
    if (launchOperation.value?.status === "succeeded") flash("Workspace 已开通");
    else if (launchOperation.value?.status === "refunded") flash("Workspace 未完成，已退款", "danger");
  } catch (error) {
    if (!requestStillCurrent()) return;
    if (!unknownWorkspaceLaunchResult(error)) workspaceLaunchIntent = null;
    flash(friendlyError(error), "danger");
  }
  finally { if (requestStillCurrent()) { mutationBusy.value = false; launchBusy.value = false; } }
}

async function revealWorkspace() {
  const requestStillCurrent = currentSessionRequest();
  if (!workspace.value || runtimeBusy.value) return;
  const workspaceId = workspace.value.id;
  const requestPath = path.value;
  const userId = session.value?.user.id || "";
  clearSecrets();
  const requestGeneration = secretRequestGeneration;
  runtimeBusy.value = true;
  try {
    const response = await revealWorkspaceCredentials(workspaceId, session.value?.csrfToken || "");
    if (!requestStillCurrent()) return;
    if (!secretResponseStillCurrent(requestGeneration, requestPath, userId, workspaceId)) return;
    revealedWorkspaceCredentials.value = response.access;
    armSecretTimeout();
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) runtimeBusy.value = false; }
}

function toggleWorkspaceCredentials() {
  if (revealedWorkspaceCredentials.value) clearSecrets();
  else void revealWorkspace();
}

async function rotateWorkspace() {
  const requestStillCurrent = currentSessionRequest();
  if (!workspace.value || runtimeBusy.value) return;
  const workspaceId = workspace.value.id;
  const requestPath = path.value;
  const userId = session.value?.user.id || "";
  clearSecrets();
  const requestGeneration = secretRequestGeneration;
  if (!runtimeRotationIntent || runtimeRotationIntent.workspaceId !== workspaceId) {
    runtimeRotationIntent = { workspaceId, idempotencyKey: `runtime-credential:${crypto.randomUUID()}` };
  }
  runtimeBusy.value = true;
  try {
    const response = await rotateWorkspaceCredentials(workspaceId, session.value?.csrfToken || "", runtimeRotationIntent.idempotencyKey);
    if (!requestStillCurrent()) return;
    runtimeRotationIntent = null;
    if (!secretResponseStillCurrent(requestGeneration, requestPath, userId, workspaceId)) return;
    revealedWorkspaceCredentials.value = response.access;
    armSecretTimeout();
    await loadWorkspaceStatus();
    if (!requestStillCurrent()) return;
    flash("Workspace 凭证已轮换");
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) runtimeBusy.value = false; }
}

async function revealKey(key?: GatewayKeySummaryDTO) {
  const requestStillCurrent = currentSessionRequest();
  if ((!key && !workspaceKeyId.value) || gatewayBusy.value) return;
  const requestPath = path.value;
  const userId = session.value?.user.id || "";
  clearSecrets();
  const requestGeneration = secretRequestGeneration;
  gatewayBusy.value = true;
  try {
    const response = key
      ? await revealGatewayKey(key.id, session.value?.csrfToken || "")
      : await revealGatewayKey(workspaceKeyId.value, session.value?.csrfToken || "");
    if (!requestStillCurrent()) return;
    if (!secretResponseStillCurrent(requestGeneration, requestPath, userId)) return;
    revealedApiKey.value = response.available ? response.data : null;
    if (response.available) armSecretTimeout();
    else flash("API Key 暂不可用", "danger");
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) gatewayBusy.value = false; }
}

function hideKey() { clearSecrets(); }

async function copySecret(value: string | undefined, success: string) {
  if (!value) return;
  try { await navigator.clipboard.writeText(value); flash(success); }
  catch { flash("复制失败，请重试", "danger"); }
}

function copyKey(key: GatewayKeySummaryDTO) {
  return copySecret(revealedApiKey.value?.id === key.id ? revealedApiKey.value.value : undefined, "API Key 已复制");
}

function copyWorkspaceKey() {
  return copySecret(revealedApiKey.value?.id === workspaceKeyId.value ? revealedApiKey.value.value : undefined, "Workspace Key 已复制");
}

function copyWorkspacePassword() {
  return copySecret(revealedWorkspaceCredentials.value?.password, "Workspace 密码已复制");
}

async function toggleKey(key: GatewayKeySummaryDTO) {
  const requestStillCurrent = currentSessionRequest();
  if (!key.manageable || gatewayBusy.value) return;
  const expectedStatus = key.status === "active" ? "disabled" : "active";
  let intent = gatewayKeyToggleIntents.get(key.id);
  if (!intent || intent.targetStatus !== expectedStatus) {
    intent = { targetStatus: expectedStatus, idempotencyKey: `key-toggle:${crypto.randomUUID()}` };
    gatewayKeyToggleIntents.set(key.id, intent);
  }
  gatewayBusy.value = true;
  clearSecrets();
  try {
    let updateError: unknown = null;
    try {
      const updated = await updateGatewayKey(key.id, { enabled: key.status !== "active" }, session.value?.csrfToken || "", intent.idempotencyKey);
      if (!requestStillCurrent()) return;
      if (!updated.available || updated.data.status !== expectedStatus) updateError = new Error("gateway_key_unavailable");
    } catch (error) {
      if (!requestStillCurrent()) return;
      updateError = error;
    }
    let readback: SourceEnvelope<GatewayKeySummaryDTO>;
    try {
      readback = await getGatewayKey(key.id);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      throw updateError || error;
    }
    if (!readback.available || readback.data.status !== expectedStatus || readback.data.id !== key.id) throw updateError || new Error("gateway_key_unavailable");
    gatewayKeyToggleIntents.delete(key.id);
    await loadKeys();
    if (!requestStillCurrent()) return;
    flash(key.status === "active" ? "API Key 已停用" : "API Key 已启用");
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) gatewayBusy.value = false; }
}

async function removeKey(key: GatewayKeySummaryDTO) {
  const requestStillCurrent = currentSessionRequest();
  if (!key.deletable || gatewayBusy.value || !window.confirm(`删除 API Key「${key.name}」？`)) return;
  let idempotencyKey = gatewayKeyDeleteIntents.get(key.id);
  if (!idempotencyKey) {
    idempotencyKey = `key-delete:${crypto.randomUUID()}`;
    gatewayKeyDeleteIntents.set(key.id, idempotencyKey);
  }
  gatewayBusy.value = true;
  clearSecrets();
  try {
    let deleteError: unknown = null;
    try {
      const deleted = await deleteGatewayKey(key.id, session.value?.csrfToken || "", idempotencyKey);
      if (!requestStillCurrent()) return;
      if (!deleted.available || deleted.data.status !== "deleted") deleteError = new Error("gateway_key_unavailable");
    } catch (error) {
      if (!requestStillCurrent()) return;
      deleteError = error;
    }
    if (deleteError) {
      let missing = false;
      try {
        await getGatewayKey(key.id);
        if (!requestStillCurrent()) return;
      } catch (readError) {
        if (!requestStillCurrent()) return;
        missing = apiErrorCode(readError) === "gateway_key_not_found";
      }
      if (!missing) throw deleteError;
    }
    gatewayKeyDeleteIntents.delete(key.id);
    await loadKeys();
    if (!requestStillCurrent()) return;
    flash("API Key 已删除");
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) gatewayBusy.value = false; }
}

function sameGatewayKeyCreateRequest(left: CreateGatewayKeyRequest, right: CreateGatewayKeyRequest) {
  return left.name === right.name && left.quotaUsdMicros === right.quotaUsdMicros && left.expiresInDays === right.expiresInDays;
}

async function submitKey() {
  const requestStillCurrent = currentSessionRequest();
  const quotaUsdMicros = keyForm.quotaUsd * 1_000_000;
  if (!keyForm.name.trim() || !Number.isSafeInteger(quotaUsdMicros) || quotaUsdMicros <= 0 || gatewayBusy.value) return;
  const input: CreateGatewayKeyRequest = {
    name: keyForm.name.trim(),
    quotaUsdMicros,
    expiresInDays: keyForm.expiresInDays
  };
  if (!gatewayKeyCreateIntent || !sameGatewayKeyCreateRequest(gatewayKeyCreateIntent.input, input)) {
    gatewayKeyCreateIntent = { input, idempotencyKey: `key-create:${crypto.randomUUID()}` };
  }
  gatewayBusy.value = true;
  try {
    const created = await createGatewayKey(input, session.value?.csrfToken || "", gatewayKeyCreateIntent.idempotencyKey);
    if (!requestStillCurrent()) return;
    if (!created.available) throw new Error("gateway_key_unavailable");
    const readback = await getGatewayKey(created.data.id);
    if (!requestStillCurrent()) return;
    if (!readback.available || readback.data.id !== created.data.id || readback.data.status !== created.data.status) throw new Error("gateway_key_unavailable");
    gatewayKeyCreateIntent = null;
    await loadKeys();
    if (!requestStillCurrent()) return;
    closeModal();
    flash("API Key 已创建");
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) gatewayBusy.value = false; }
}

async function readAnnouncement(announcementId: string) {
  const requestStillCurrent = currentSessionRequest();
  if (announcementBusy.value) return;
  announcementBusy.value = announcementId;
  try {
    const readback = await markAnnouncementRead(announcementId, session.value?.csrfToken || "", `announcement-read:${crypto.randomUUID()}`);
    if (!requestStillCurrent()) return;
    if (readback.announcementId !== announcementId) throw new Error("announcement_read_failed");
    await loadAnnouncements();
    if (!requestStillCurrent()) return;
  } catch (error) { if (requestStillCurrent()) flash(friendlyError(error), "danger"); }
  finally { if (requestStillCurrent()) announcementBusy.value = ""; }
}

function operatorSourceText<T>(value: SourceEnvelope<T> | undefined, formatter: (data: T) => string = (data) => String(data)) {
  return value?.available ? formatter(value.data) : "暂不可用";
}

function operatorSourceDate(value: SourceEnvelope<string> | undefined) {
  return value?.available ? formatDate(value.data, true) : "暂不可用";
}

function operatorWorkspaceId(value: SourceEnvelope<WorkspaceDTO> | undefined) {
  return value?.available ? value.data.id : "";
}

function operatorHealthText(value: SourceEnvelope<{ ready?: boolean }> | undefined) {
  if (!value?.available) return "暂不可用";
  if (value.data.ready === true) return "正常";
  if (value.data.ready === false) return "需处理";
  return "暂不可用";
}

function operatorAnnouncementStatus(status: string) {
  return ({ draft: "草稿", scheduled: "已排期", published: "已发布", withdrawn: "已撤下" } as Record<string, string>)[status] || "暂不可用";
}

async function submitWalletAdjustment() {
  const requestStillCurrent = currentSessionRequest();
  const accountId = selectedOperatorAccountId.value || walletAdjustmentForm.confirmationAccountId;
  if (!accountId || walletAdjustmentForm.confirmationAccountId !== accountId || !walletAdjustmentForm.amountUsd || !walletAdjustmentForm.reason.trim()) {
    flash("请确认账号、金额和原因", "danger");
    return;
  }
  if (!window.confirm("请再次确认该钱包调整：提交后将写入权威钱包并记录审计。")) return;
  const input = { ...walletAdjustmentForm, confirmationAccountId: accountId };
  if (!walletAdjustmentIntent || walletAdjustmentIntent.accountId !== accountId || JSON.stringify(walletAdjustmentIntent.input) !== JSON.stringify(input)) {
    walletAdjustmentIntent = { accountId, input, idempotencyKey: `wallet-adjustment:${crypto.randomUUID()}` };
  }
  loading.walletAdjustment = true;
  errors.walletAdjustment = "";
  try {
    const result = await createWalletAdjustment(accountId, walletAdjustmentIntent.input, session.value?.csrfToken || "", walletAdjustmentIntent.idempotencyKey);
    if (!requestStillCurrent()) return;
    walletAdjustmentOperation.value = result;
    if (result.status === "manual_review") flash("结果待确认，已进入人工复核", "danger");
    else {
      walletAdjustmentIntent = null;
      flash("钱包调整已提交");
    }
    await loadAdmin();
    if (result.status !== "manual_review") closeModal();
  } catch (error) {
    if (requestStillCurrent()) {
      errors.walletAdjustment = friendlyError(error);
      flash(mutationError(error), "danger");
    }
  } finally { if (requestStillCurrent()) loading.walletAdjustment = false; }
}

async function refreshWalletAdjustment() {
  const operationId = walletAdjustmentOperation.value?.operationId;
  if (!operationId) return;
  try {
    walletAdjustmentOperation.value = await getWalletAdjustment(operationId);
    if (walletAdjustmentOperation.value.status === "succeeded") walletAdjustmentIntent = null;
    await loadAdmin();
  } catch (error) { flash(friendlyError(error), "danger"); }
}

async function disableOperatorAccount(accountId: string) {
  if (!window.confirm("确认禁用该账号？账号与历史账单仍会保留。")) return;
  const idempotencyKey = operatorDisableIntents.get(accountId) || `account-disable:${accountId}:${crypto.randomUUID()}`;
  operatorDisableIntents.set(accountId, idempotencyKey);
  try {
    await disableOperatorAccountCommand(accountId, "operator_requested", session.value?.csrfToken || "", idempotencyKey);
    operatorDisableIntents.delete(accountId);
    flash("账号已禁用");
    await loadAdmin();
  } catch (error) { flash(mutationError(error), "danger"); }
}

async function submitOperatorAnnouncement() {
  if (!announcementForm.title.trim() || !announcementForm.body.trim()) {
    flash("请填写公告标题和正文", "danger");
    return;
  }
  const input = { ...announcementForm };
  if (!announcementCreateIntent || JSON.stringify(announcementCreateIntent.input) !== JSON.stringify(input)) {
    announcementCreateIntent = { input, idempotencyKey: `announcement-create:${crypto.randomUUID()}` };
  }
  try {
    const result = await createOperatorAnnouncement(announcementCreateIntent.input, session.value?.csrfToken || "", announcementCreateIntent.idempotencyKey);
    if (result.id) flash("公告草稿已创建");
    announcementCreateIntent = null;
    await loadAdmin();
    closeModal();
  } catch (error) { flash(mutationError(error), "danger"); }
}

async function publishOperatorAnnouncementAction(announcementId: string) {
  if (!window.confirm("确认发布公告？")) return;
  const announcement = operatorAnnouncementRows.value.find((item) => item.id === announcementId);
  if (!announcement) {
    flash("公告暂不可用，请刷新后重试", "danger");
    return;
  }
  let intent = announcementPublishIntents.get(announcementId);
  if (!intent) {
    intent = { input: { startsAt: announcement.startsAt || new Date().toISOString(), endsAt: announcement.endsAt || "" }, idempotencyKey: `announcement-publish:${announcementId}:${crypto.randomUUID()}` };
    announcementPublishIntents.set(announcementId, intent);
  }
  try {
    await publishOperatorAnnouncement(announcementId, intent.input, session.value?.csrfToken || "", intent.idempotencyKey);
    announcementPublishIntents.delete(announcementId);
    flash("公告已发布");
    await loadAdmin();
  } catch (error) { flash(mutationError(error), "danger"); }
}

async function withdrawOperatorAnnouncementAction(announcementId: string) {
  if (!window.confirm("确认撤下公告？")) return;
  const idempotencyKey = announcementWithdrawIntents.get(announcementId) || `announcement-withdraw:${announcementId}:${crypto.randomUUID()}`;
  announcementWithdrawIntents.set(announcementId, idempotencyKey);
  try {
    await withdrawOperatorAnnouncement(announcementId, session.value?.csrfToken || "", idempotencyKey);
    announcementWithdrawIntents.delete(announcementId);
    flash("公告已撤下");
    await loadAdmin();
  } catch (error) { flash(mutationError(error), "danger"); }
}

async function resolveOperatorReview() {
  const review = selectedReview.value;
  if (!review) return;
  const evidenceRef = (window.prompt("请输入 case-YYYYMMDD-xxx 证据引用") || "").trim();
  if (!evidenceRef) return;
  const accountId = review.accountId;
  const billingOperationId = review.billingOperationId;
  if (!accountId) {
    flash("缺少账号来源，暂不可处理", "danger");
    return;
  }
  try {
    const launchRecovery = review.allowedActions.includes("recover_workspace_launch");
    if (launchRecovery) {
      const input: WorkspaceLaunchRecoveryRequest = { accountId, billingOperationId, evidenceRef };
      if (!workspaceLaunchRecoveryIntent || workspaceLaunchRecoveryIntent.operationId !== billingOperationId || JSON.stringify(workspaceLaunchRecoveryIntent.input) !== JSON.stringify(input)) {
        workspaceLaunchRecoveryIntent = { operationId: billingOperationId, input, idempotencyKey: `recover-${crypto.randomUUID()}` };
      }
      await recoverOperatorWorkspaceLaunch(billingOperationId, workspaceLaunchRecoveryIntent.input, session.value?.csrfToken || "", workspaceLaunchRecoveryIntent.idempotencyKey);
      workspaceLaunchRecoveryIntent = null;
    } else {
      const input: BillingReviewResolutionRequest = { accountId, billingOperationId, decision: "activate_charged_resource", evidenceRef };
      if (!billingReviewIntent || billingReviewIntent.resourceType !== review.resourceType || billingReviewIntent.resourceId !== review.id || JSON.stringify(billingReviewIntent.input) !== JSON.stringify(input)) {
        billingReviewIntent = { resourceType: review.resourceType, resourceId: review.id, input, idempotencyKey: `billing-review:${review.resourceType}:${review.id}:${crypto.randomUUID()}` };
      }
      await resolveBillingReview(review.resourceType, review.id, billingReviewIntent.input, session.value?.csrfToken || "", billingReviewIntent.idempotencyKey);
      billingReviewIntent = null;
    }
    flash("复核命令已提交");
    selectedReview.value = null;
    await loadAdmin();
  } catch (error) { flash(mutationError(error), "danger"); }
}

async function provisionOperatorUser() {
  const requestStillCurrent = currentSessionRequest();
  if (mutationBusy.value) return;
  const input: ProvisionAccountRequest = { email: adminUserForm.email, password: adminUserForm.password, name: adminUserForm.name || undefined };
  if (!operatorProvisionIntent || JSON.stringify(operatorProvisionIntent.input) !== JSON.stringify(input)) {
    operatorProvisionIntent = { input, idempotencyKey: `account-provision:${crypto.randomUUID()}` };
  }
  mutationBusy.value = true;
  try {
    await provisionOperatorAccount(operatorProvisionIntent.input, session.value?.csrfToken || "", operatorProvisionIntent.idempotencyKey);
    if (!requestStillCurrent()) return;
    operatorProvisionIntent = null;
    await loadAdmin();
    if (!requestStillCurrent()) return;
    closeModal();
    flash("用户已开通");
  } catch (error) { if (requestStillCurrent()) flash(mutationError(error), "danger"); }
  finally { if (requestStillCurrent()) mutationBusy.value = false; }
}

function changeUsagePage(page: number) {
  if (page < 1 || (gatewayPageNumber.pages > 0 && page > gatewayPageNumber.pages)) return;
  void loadUsage(page);
}

function selectUsageKey(keyId: string) {
  usageRequestGeneration += 1;
  usageStatsRequestGeneration += 1;
  selectedUsageKeyId.value = keyId;
  Object.assign(gatewayPageNumber, { page: 1, pages: 0, total: 0 });
  usageSource.value = null;
  usageStatsSource.value = null;
  loading.usage = false;
  loading.stats = false;
  resetSource("usage");
  resetSource("stats");
  clearSecrets();
  if (!keyId || activeApiPage.value !== "usage") return;
  void Promise.all([loadUsage(1), loadStats()]);
}

function selectPeriod(period: string) {
  if (gatewayPeriod.value === period) return;
  gatewayPeriod.value = period;
  void loadStats();
}

function refreshCurrentPage() {
  clearSecrets();
  if (isAdminRoute.value) return void loadAdmin();
  void loadCustomer();
}

function receiptLabel(type: string) {
  if (type === "billing.workspace_purchased.v1") return "Workspace 开通";
  if (type === "billing.workspace_expired.v1") return "Workspace 到期";
  if (type.includes("renew")) return "Workspace 续费";
  if (type.includes("refund")) return "Workspace 退款";
  if (type.includes("created")) return "Workspace 开通";
  return type ? "账单记录" : "暂不可用";
}

watch(path, (next, previous) => {
  if (previous !== next) {
    closeModal();
    if (isSensitiveRoute(previous || "")) clearSecrets();
    if (previous === "/login") {
      loginForm.email = "";
      loginForm.password = "";
    }
  }
  void handleRoute();
});

onMounted(() => {
  const onPopState = () => { path.value = window.location.pathname; };
  window.addEventListener("popstate", onPopState);
  (window as unknown as { __oplPopState?: () => void }).__oplPopState = onPopState;
  void handleRoute();
});

onBeforeUnmount(() => {
  clearSecrets();
  launchPollGeneration += 1;
  window.removeEventListener("popstate", (window as unknown as { __oplPopState?: () => void }).__oplPopState || (() => {}));
  if (toastTimer) window.clearTimeout(toastTimer);
});
</script>

<template>
  <main v-if="isPublicRoute" class="access-page">
    <nav class="public-nav"><a href="/" class="brand" @click.prevent="navigate('/')"><img src="/opl-app-icon.png" alt="" /><strong>OPL Cloud</strong></a><button class="button secondary" type="button" @click="navigate('/login')">登录</button></nav>
    <section class="access-main"><div><p class="kicker">One Person Lab</p><h1>OPL Cloud</h1><p>邀请制 Workspace 与 API 服务。</p><button class="button primary" type="button" @click="navigate('/login')">进入 Console <ArrowUpRight :size="17" /></button></div><img class="access-mark" src="/opl-app-icon.png" alt="OPL Cloud" /></section>
  </main>

  <main v-else-if="isLoginRoute" class="login-page">
    <button class="back-button" type="button" @click="navigate('/')">返回</button>
    <section class="login-panel"><div class="login-brand"><img src="/opl-app-icon.png" alt="" /><div><strong>OPL Cloud</strong><span>Console 登录</span></div></div><form @submit.prevent="submitLogin"><label>邮箱<input v-model.trim="loginForm.email" type="email" autocomplete="username" required /></label><label>密码<input v-model="loginForm.password" type="password" autocomplete="current-password" required /></label><p v-if="loginError" class="form-error" role="alert">{{ loginError }}</p><button class="button primary wide" type="submit" :disabled="loginBusy">{{ loginBusy ? "登录中..." : "登录" }}</button></form></section>
  </main>

  <main v-else-if="isForbidden" class="message-page"><ShieldCheck :size="34" /><h1>无权访问</h1><p>此页面仅对 OPL Cloud 管理员开放。</p><button class="button primary" type="button" @click="navigate('/console/overview')">返回 Console</button></main>
  <main v-else-if="authStatus === 'checking'" class="message-page" aria-live="polite"><span class="spinner" /><p>正在恢复登录...</p></main>
  <main v-else-if="authStatus === 'error'" class="message-page"><AlertCircle :size="34" /><h1>无法恢复登录</h1><p>{{ authError }}</p><button class="button primary" type="button" @click="ensureSession">重试</button></main>

  <div v-else class="app-shell">
    <button class="mobile-menu" type="button" aria-label="打开导航" @click="sidebarOpen = true"><Menu /></button>
    <aside class="sidebar" :class="{ open: sidebarOpen }">
      <div class="sidebar-head"><a href="/console/overview" class="brand" @click.prevent="navigate('/console/overview')"><img src="/opl-app-icon.png" alt="" /><strong>OPL Console</strong></a><button class="sidebar-close" type="button" aria-label="关闭导航" @click="sidebarOpen = false"><X /></button></div>
      <nav class="side-nav" aria-label="主导航">
        <template v-for="item in customerMenu" :key="item.path">
          <a :href="item.path" :class="{ active: item.id === 'api' ? apiRoute : path === item.path || (item.id === 'overview' && path === '/console') }" @click.prevent="navigate(item.path)"><component :is="menuIcons[item.icon]" :size="19" />{{ item.label }}</a>
          <div v-if="item.id === 'api' && apiRoute" class="side-subnav"><a v-for="child in apiMenu" :key="child.path" :href="child.path" :class="{ active: activeApiPage === child.id }" @click.prevent="navigate(child.path)">{{ child.label }}</a></div>
        </template>
        <div v-if="isOperator" class="operator-nav"><a href="/admin/overview" class="operator-root" :class="{ active: isAdminRoute }" @click.prevent="navigate('/admin/overview')"><ShieldCheck :size="19" />运维<ChevronRight :size="15" /></a><div v-if="isAdminRoute" class="side-subnav"><a v-for="item in adminMenu" :key="item.path" :href="item.path" :class="{ active: path === item.path || (item.id === 'overview' && path === '/admin') }" @click.prevent="navigate(item.path)"><component :is="menuIcons[item.icon]" :size="16" />{{ item.label }}</a></div></div>
      </nav>
      <div class="sidebar-account"><UserRound :size="18" /><span><strong>{{ session?.user.email }}</strong><small>{{ isOperator ? "管理员" : "用户" }}</small></span><button type="button" aria-label="退出登录" title="退出登录" @click="signOut"><LogOut :size="17" /></button></div>
    </aside>
    <button v-if="sidebarOpen" class="sidebar-scrim" type="button" aria-label="关闭导航" @click="sidebarOpen = false" />

    <section class="main-column"><header class="topbar"><h1>{{ pageTitle }}</h1><button class="icon-button" type="button" title="刷新" aria-label="刷新" @click="refreshCurrentPage"><RefreshCw :size="17" /></button></header>
      <div v-if="isAdminRoute" class="page-content">
        <div v-if="loading.admin && !operatorOverviewSource" class="loading-panel"><span class="spinner" />正在加载运维数据...</div>
        <div v-else-if="errors.admin && !operatorOverviewSource" class="empty-panel"><AlertCircle /><p>{{ errors.admin }}</p><button class="button secondary" type="button" @click="loadAdmin">重试</button></div>
        <template v-else>
          <div v-if="errors.admin" class="inline-error"><AlertCircle :size="17" />{{ errors.admin }}<button type="button" @click="loadAdmin">重试</button></div>
          <section v-if="path === '/admin' || path === '/admin/overview'" class="admin-dashboard">
            <div class="metric-row operator-metrics">
              <article><UsersRound /><span>计费账户<strong>{{ operatorSourceText(operatorOverview?.accounts, (data) => formatCount(data.total)) }}</strong></span></article>
              <article><WalletCards /><span>钱包余额<strong>{{ operatorSourceText(operatorOverview?.wallet, (data) => formatUsdMicros(data.usdMicros)) }}</strong></span></article>
              <article><Activity /><span>API 用量<strong>{{ operatorSourceText(operatorOverview?.usage, (data) => formatUsdMicros(data.totalActualCostUsdMicros)) }}</strong></span></article>
              <article><AlertCircle /><span>待复核<strong>{{ operatorSourceText(operatorOverview?.reconciliation, (data) => formatCount(data.total)) }}</strong></span></article>
            </div>
            <section class="panel"><div class="panel-title"><h2>运维概览</h2><button class="button primary" type="button" @click="openModal('wallet-adjustment')"><WalletCards :size="16" />钱包调整</button></div><p class="source-note">每个指标保留权威来源状态；不可用不会被当作零值。</p><div v-if="errors.operatorOverview" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorOverview }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorOverviewSource?.status === 'unavailable'" class="empty-panel">概览暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else class="table-wrap"><table><thead><tr><th>来源</th><th>状态</th><th>最近读回</th></tr></thead><tbody><tr><td>账号与映射</td><td>{{ operatorOverview?.accounts?.status || "暂不可用" }}</td><td>{{ operatorOverview?.accounts?.fetchedAt ? formatDate(operatorOverview.accounts.fetchedAt, true) : "暂不可用" }}</td></tr><tr><td>资源与 Workspace</td><td>{{ operatorOverview?.resources?.status || "暂不可用" }}</td><td>{{ operatorOverview?.resources?.fetchedAt ? formatDate(operatorOverview.resources.fetchedAt, true) : "暂不可用" }}</td></tr><tr><td>健康</td><td>{{ operatorOverview?.health?.status || "暂不可用" }}</td><td>{{ operatorOverview?.health?.fetchedAt ? formatDate(operatorOverview.health.fetchedAt, true) : "暂不可用" }}</td></tr></tbody></table></div></section>
            <section class="panel"><div class="panel-title"><h2>公告</h2><button class="button secondary" type="button" @click="openModal('announcement')"><Plus :size="16" />新建草稿</button></div><div v-if="loading.operatorAnnouncements" class="loading-panel"><span class="spinner" />正在读取公告...</div><div v-else-if="errors.operatorAnnouncements" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorAnnouncements }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAnnouncementsSource?.status === 'unavailable'" class="empty-panel">公告暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAnnouncementsSource?.status === 'empty'" class="empty-panel">暂无公告</div><div v-else class="announcement-list"><article v-for="announcement in operatorAnnouncementRows" :key="announcement.id" class="announcement-item"><header><div><h3>{{ announcement.title }}</h3><span>{{ operatorAnnouncementStatus(announcement.status) }}</span></div><span>{{ formatDate(announcement.updatedAt, true) }}</span></header><p>{{ announcement.body }}</p><footer class="table-actions"><button v-if="announcement.status === 'draft' || announcement.status === 'scheduled'" class="text-button" type="button" @click="publishOperatorAnnouncementAction(announcement.id)">发布</button><button v-if="announcement.status === 'published'" class="text-button danger-text" type="button" @click="withdrawOperatorAnnouncementAction(announcement.id)">撤下</button></footer></article></div></section>
          </section>
          <section v-else-if="path.startsWith('/admin/accounts')" class="panel"><div class="panel-title"><h2>用户与计费账户</h2><button class="button primary" type="button" @click="openModal('admin-user')"><Plus :size="16" />开通用户</button></div><div v-if="loading.operatorAccounts" class="loading-panel"><span class="spinner" />正在读取账号...</div><div v-else-if="errors.operatorAccounts" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorAccounts }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAccountsPageSource?.status === 'unavailable'" class="empty-panel">账号暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAccountsPageSource?.status === 'empty'" class="empty-panel">暂无用户</div><div v-else class="table-wrap"><table><thead><tr><th>邮箱</th><th>计费账户编号</th><th>映射</th><th>余额</th><th>Key 数</th><th>用量</th><th>Workspace</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="account in operatorAccountRows" :key="account.accountId"><td>{{ account.email }}</td><td>{{ account.accountId }}</td><td>{{ operatorSourceText(account.gatewayIdentity, (data) => data.userId) }}</td><td>{{ operatorSourceText(account.wallet, (data) => formatUsdMicros(data.usdMicros)) }}</td><td>{{ operatorSourceText(account.keyCount, formatCount) }}</td><td>{{ operatorSourceText(account.usage, (data) => formatUsdMicros(data.totalActualCostUsdMicros)) }}</td><td>{{ operatorSourceText(account.workspaceCount, formatCount) }}</td><td>{{ account.status }}</td><td class="table-actions"><button class="text-button" type="button" @click="selectedOperatorAccountId = account.accountId; walletAdjustmentForm.confirmationAccountId = account.accountId; openModal('wallet-adjustment')">调整钱包</button><button v-if="account.status === 'active'" class="text-button danger-text" type="button" @click="disableOperatorAccount(account.accountId)">禁用</button></td></tr></tbody></table></div></section>
          <section v-else-if="path.startsWith('/admin/billing')" class="panel"><div class="panel-title"><h2>计费复核</h2></div><div v-if="loading.operatorReconciliation" class="loading-panel"><span class="spinner" />正在读取复核项...</div><div v-else-if="errors.operatorReconciliation" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorReconciliation }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorReconciliationSource?.status === 'unavailable'" class="empty-panel">复核数据暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorReconciliationSource?.status === 'empty'" class="empty-panel">暂无待复核项目</div><div v-else class="table-wrap"><table><thead><tr><th>资源类型</th><th>状态</th><th>operation</th><th>Receipt</th><th>操作</th></tr></thead><tbody><tr v-for="item in operatorReconciliationRows" :key="item.id"><td>{{ item.resourceType }}</td><td>{{ item.status }}</td><td>{{ item.operationRef || "暂不可用" }}</td><td>{{ item.receiptRef || "暂不可用" }}</td><td><button v-if="item.status === 'manual_review' && item.allowedActions.includes('recover_workspace_launch')" class="text-button" type="button" @click="selectedReview = item; resolveOperatorReview()">恢复 Launch</button><button v-else-if="item.status === 'manual_review' && item.allowedActions.includes('resolve_billing_review')" class="text-button" type="button" @click="selectedReview = item; resolveOperatorReview()">人工复核</button><span v-else>暂不可用</span></td></tr></tbody></table></div></section>
          <section v-else-if="path.startsWith('/admin/resources')" class="admin-dashboard">
            <section class="panel">
              <div class="panel-title"><h2>Workspace 与资源</h2></div>
              <div v-if="loading.operatorWorkspaces" class="loading-panel"><span class="spinner" />正在读取 Workspace...</div>
              <div v-else-if="errors.operatorWorkspaces" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorWorkspaces }}<button type="button" @click="loadAdmin">重试</button></div>
              <div v-else-if="operatorWorkspacesSource?.status === 'unavailable'" class="empty-panel">Workspace 暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div>
              <div v-else-if="operatorWorkspacesSource?.status === 'empty'" class="empty-panel">暂无 Workspace</div>
              <div v-else class="table-wrap">
                <table>
                  <thead><tr><th>Workspace</th><th>owner account</th><th>owner user</th><th>套餐</th><th>月价</th><th>创建时间</th><th>有效期</th><th>续费状态</th><th>Workspace 状态</th><th>URL</th><th>账单引用</th><th>Key 用量</th><th>操作</th></tr></thead>
                  <tbody><tr v-for="item in operatorWorkspaceRows" :key="operatorSourceText(item.workspace)">
                    <td>{{ operatorSourceText(item.workspace, (data) => data.name || data.id) }}</td>
                    <td>{{ operatorSourceText(item.ownerAccount, (data) => data.id) }}</td>
                    <td>{{ operatorSourceText(item.ownerUser, (data) => data.email) }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => data.packageId || "暂不可用") }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => typeof data.totalUsdMicros === "number" ? formatUsdMicros(data.totalUsdMicros) : "暂不可用") }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => data.createdAt ? formatDate(data.createdAt, true) : "暂不可用") }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => data.paidThrough ? formatDate(data.paidThrough) : "暂不可用") }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => data.renewalStatus || "暂不可用") }}</td>
                    <td>{{ operatorSourceText(item.workspace, (data) => data.state || "暂不可用") }}</td>
                    <td><a v-if="item.workspace.available && item.workspace.data.url" :href="item.workspace.data.url" target="_blank" rel="noopener noreferrer">{{ item.workspace.data.url }}</a><span v-else>暂不可用</span></td>
                    <td>{{ operatorSourceText(item.receipt, (data) => data.receiptId) }}</td>
                    <td>{{ operatorSourceText(item.workspaceKeyUsage, (data) => formatUsdMicros(data.totalActualCostUsdMicros)) }}</td>
                    <td><button class="text-button" type="button" :disabled="loading.operatorWorkspaceDetail && selectedOperatorWorkspaceId === operatorWorkspaceId(item.workspace)" @click="operatorWorkspaceId(item.workspace) && loadOperatorWorkspaceDetail(operatorWorkspaceId(item.workspace))">{{ loading.operatorWorkspaceDetail && selectedOperatorWorkspaceId === operatorWorkspaceId(item.workspace) ? "读取中..." : "查看资源" }}</button></td>
                  </tr></tbody>
                </table>
              </div>
              <div v-if="loading.operatorWorkspaceDetail" class="loading-panel"><span class="spinner" />正在读取资源详情...</div>
              <div v-else-if="errors.operatorWorkspaceDetail" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorWorkspaceDetail }}<button type="button" @click="selectedOperatorWorkspaceId && loadOperatorWorkspaceDetail(selectedOperatorWorkspaceId)">重试</button></div>
            </section>
            <section class="panel">
              <div class="panel-title"><h2>资源字段</h2></div>
              <div v-if="operatorWorkspaceDetailSource?.status === 'unavailable' && !operatorResourceRows.length" class="empty-panel">资源详情暂不可用；请先选择 Workspace</div>
              <div v-else-if="!operatorResourceRows.length" class="empty-panel">暂无资源</div>
              <div v-else class="table-wrap"><table><thead><tr><th>owner account</th><th>owner user</th><th>Workspace</th><th>资源类型</th><th>套餐/规格</th><th>provider ID</th><th>Zone</th><th>状态</th><th>创建时间</th><th>到期时间</th><th>最近读回时间</th><th>operation</th><th>Receipt</th></tr></thead><tbody><tr v-for="resource in operatorResourceRows" :key="operatorSourceText(resource.operationRef) + operatorSourceText(resource.providerId)"><td>{{ operatorSourceText(resource.ownerAccount, (data) => data.id) }}</td><td>{{ operatorSourceText(resource.ownerUser, (data) => data.email) }}</td><td>{{ operatorSourceText(resource.workspace, (data) => data.name || data.id) }}</td><td>{{ operatorSourceText(resource.resourceType) }}</td><td>{{ operatorSourceText(resource.packageOrSpec) }}</td><td>{{ operatorSourceText(resource.providerId) }}</td><td>{{ operatorSourceText(resource.zone) }}</td><td>{{ operatorSourceText(resource.status) }}</td><td>{{ operatorSourceDate(resource.createdAt) }}</td><td>{{ operatorSourceDate(resource.expiresAt) }}</td><td>{{ operatorSourceDate(resource.lastReadAt) }}</td><td>{{ operatorSourceText(resource.operationRef) }}</td><td>{{ operatorSourceText(resource.receiptRef) }}</td></tr></tbody></table></div>
            </section>
          </section>
          <section v-else-if="path.startsWith('/admin/announcements')" class="panel"><div class="panel-title"><h2>公告管理</h2><button class="button primary" type="button" @click="openModal('announcement')"><Plus :size="16" />新建草稿</button></div><div v-if="loading.operatorAnnouncements" class="loading-panel"><span class="spinner" />正在读取公告...</div><div v-else-if="errors.operatorAnnouncements" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorAnnouncements }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAnnouncementsSource?.status === 'unavailable'" class="empty-panel">公告暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorAnnouncementsSource?.status === 'empty'" class="empty-panel">暂无公告</div><div v-else class="announcement-list"><article v-for="announcement in operatorAnnouncementRows" :key="announcement.id" class="announcement-item"><header><h3>{{ announcement.title }}</h3><span>{{ operatorAnnouncementStatus(announcement.status) }}</span></header><p>{{ announcement.body }}</p><footer class="table-actions"><button v-if="announcement.status !== 'published'" class="text-button" type="button" @click="publishOperatorAnnouncementAction(announcement.id)">发布</button><button v-if="announcement.status === 'published'" class="text-button danger-text" type="button" @click="withdrawOperatorAnnouncementAction(announcement.id)">撤下</button></footer></article></div></section>
          <section v-else class="admin-dashboard"><section class="panel"><div class="panel-title"><h2>系统健康</h2></div><div v-if="loading.operatorHealth" class="loading-panel"><span class="spinner" />正在读取健康状态...</div><div v-else-if="errors.operatorHealth" class="inline-error"><AlertCircle :size="17" />{{ errors.operatorHealth }}<button type="button" @click="loadAdmin">重试</button></div><div v-else-if="operatorHealthSource?.status === 'unavailable'" class="empty-panel">健康数据暂不可用 <button class="text-button" type="button" @click="loadAdmin">重试</button></div><div v-else class="table-wrap"><table><thead><tr><th>检查</th><th>状态</th><th>最近读回</th></tr></thead><tbody><tr v-for="row in operatorHealthRows" :key="row[0]"><td>{{ row[0] }}</td><td>{{ operatorHealthText(row[1]) }}</td><td>{{ row[1]?.fetchedAt ? formatDate(row[1].fetchedAt, true) : "暂不可用" }}</td></tr></tbody></table></div></section></section>
        </template>
        <nav v-if="path.startsWith('/admin/accounts') && operatorAccountsPageSource?.available && operatorAccountPages > 1" class="pagination" aria-label="账号分页"><button class="button secondary" type="button" :disabled="loading.operatorAccounts || operatorAccountPage <= 1" @click="changeOperatorAccountPage(operatorAccountPage - 1)"><ChevronLeft :size="16" />上一页</button><span>第 {{ operatorAccountPage }} / {{ operatorAccountPages }} 页</span><button class="button secondary" type="button" :disabled="loading.operatorAccounts || operatorAccountPage >= operatorAccountPages" @click="changeOperatorAccountPage(operatorAccountPage + 1)">下一页<ChevronRight :size="16" /></button></nav>
        <nav v-if="path.startsWith('/admin/resources') && operatorWorkspacesSource?.available && operatorWorkspacePages > 1" class="pagination" aria-label="Workspace 分页"><button class="button secondary" type="button" :disabled="loading.operatorWorkspaces || operatorWorkspacePage <= 1" @click="changeOperatorWorkspacePage(operatorWorkspacePage - 1)"><ChevronLeft :size="16" />上一页</button><span>第 {{ operatorWorkspacePage }} / {{ operatorWorkspacePages }} 页</span><button class="button secondary" type="button" :disabled="loading.operatorWorkspaces || operatorWorkspacePage >= operatorWorkspacePages" @click="changeOperatorWorkspacePage(operatorWorkspacePage + 1)">下一页<ChevronRight :size="16" /></button></nav>
      </div>

      <div v-else class="page-content">
          <div v-if="errors.catalog && !workspace && (path === '/console' || path === '/console/overview' || path.startsWith('/console/workspace'))" class="inline-error"><AlertCircle :size="17" />计划与价格暂不可用<button type="button" @click="loadCatalog">重试</button></div>
          <section v-if="path === '/console' || path === '/console/overview'" class="overview-layout">
            <div class="overview-main">
              <section class="panel workspace-panel"><div class="workspace-heading"><div><span class="section-label">Workspace</span><h2>{{ workspace?.name || (workspaceSource?.status === 'empty' ? "尚未开通" : "暂不可用") }}<span v-if="runtime" class="status-pill" :class="{ good: workspaceCanOpen }">{{ workspaceStatusLabel(runtime) }}</span></h2></div><button v-if="workspace" class="button primary" type="button" :disabled="!workspaceCanOpen" @click="openWorkspace">打开 Workspace <ArrowUpRight :size="16" /></button><button v-else class="button primary" type="button" :disabled="loading.workspace || !workspaceSource || workspaceSource.status === 'unavailable' || !plans.length" @click="openModal('workspace')"><Plus :size="16" />开通 Workspace</button></div><div v-if="loading.workspace" class="loading-panel"><span class="spinner" />正在加载 Workspace...</div><div v-else-if="errors.workspace" class="inline-error"><AlertCircle :size="17" />{{ errors.workspace }}<button type="button" @click="loadWorkspaces">重试</button></div><div v-if="launchStatusText" class="inline-notice"><span>{{ launchStatusText }}</span><button v-if="launchPollIssue" class="text-button" type="button" @click="retryWorkspaceLaunchPoll">重试</button></div></section>
              <div v-if="loading.wallet" class="loading-panel"><span class="spinner" />正在读取余额...</div><div v-else-if="errors.wallet" class="inline-error"><AlertCircle :size="17" />{{ errors.wallet }}<button type="button" @click="loadWallet">重试</button></div><div v-else-if="walletSource?.status === 'unavailable'" class="empty-panel">余额暂不可用 <button class="text-button" type="button" @click="loadWallet">重试</button></div>
              <section class="metric-row"><article><WalletCards /><span>可用余额<strong>{{ wallet ? formatAvailableBalance({ ...wallet, available: true }) : "暂不可用" }}</strong></span></article><article><Activity /><span>AI 用量<strong>{{ stats ? formatUsdMicros(stats.totalActualCostUsdMicros) : "暂不可用" }}</strong></span></article><article><ReceiptText /><span>交易记录<strong>{{ receiptsSource?.available ? formatCount(receipts.length) : "暂不可用" }}</strong></span></article></section>
              <section class="panel overview-announcements"><div class="panel-title"><h2>公告</h2><button class="icon-button" type="button" title="刷新" aria-label="刷新公告" :disabled="loading.announcements" @click="loadAnnouncements"><RefreshCw :size="16" /></button></div><div v-if="loading.announcements" class="loading-panel"><span class="spinner" />正在读取公告...</div><div v-else-if="errors.announcements" class="inline-error"><AlertCircle :size="17" />{{ errors.announcements }}<button type="button" @click="loadAnnouncements">重试</button></div><div v-else-if="announcementsUnavailable" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadAnnouncements">重试</button></div><div v-else-if="announcementsEmpty" class="empty-panel">暂无公告</div><div v-else class="announcement-list"><article v-for="announcement in announcements" :key="announcement.id" class="announcement-item"><header><div><h3>{{ announcement.title }}</h3><span>{{ formatDate(announcement.publishedAt || announcement.startsAt, true) }}</span></div><span class="status-pill" :class="{ good: announcement.read }">{{ announcement.read ? "已读" : "未读" }}</span></header><p>{{ announcement.body }}</p><button v-if="!announcement.read" class="button secondary" type="button" :disabled="announcementBusy === announcement.id" @click="readAnnouncement(announcement.id)">{{ announcementBusy === announcement.id ? "处理中..." : "标记已读" }}</button></article></div></section>
            </div>
            <aside class="overview-rail panel"><div><ShieldCheck /><span>Workspace 状态<strong>{{ runtime ? workspaceStatusLabel(runtime) : "暂不可用" }}</strong></span></div><div><CircleDollarSign /><span>Workspace 月费<strong>{{ typeof workspace?.totalUsdMicros === "number" ? formatUsdMicros(workspace.totalUsdMicros) : "暂不可用" }}</strong></span></div><div><CalendarDays /><span>计费周期<strong>{{ workspace?.periodStart && workspace?.paidThrough ? `${formatDate(workspace.periodStart)} 至 ${formatDate(workspace.paidThrough)}` : "暂不可用" }}</strong></span></div><button type="button" @click="navigate('/console/api')"><Server /><span>查看 API 服务</span><ChevronRight /></button></aside>
          </section>

          <section v-else-if="path.startsWith('/console/workspace')" class="workspace-page">
            <section class="panel">
              <div class="panel-title"><h2>Workspace</h2><button v-if="workspace && workspaceCanOpen" class="button primary" type="button" @click="openWorkspace">打开 Workspace <ArrowUpRight :size="16" /></button><button v-else-if="!workspace" class="button primary" type="button" :disabled="loading.workspace || !workspaceSource || workspaceSource.status === 'unavailable' || !plans.length" @click="openModal('workspace')"><Plus :size="16" />开通 Workspace</button></div>
              <div v-if="launchStatusText" class="inline-notice"><span>{{ launchStatusText }}</span><button v-if="launchPollIssue" class="text-button" type="button" @click="retryWorkspaceLaunchPoll">重试</button></div>
              <div v-if="loading.workspace" class="loading-panel"><span class="spinner" />正在加载 Workspace...</div>
              <div v-else-if="errors.workspace" class="inline-error"><AlertCircle :size="17" />{{ errors.workspace }}<button type="button" @click="loadWorkspaces">重试</button></div>
              <div v-else-if="workspaceSource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadWorkspaces">重试</button></div>
              <div v-else-if="workspaceSource?.status === 'empty'" class="empty-panel">尚未开通 Workspace</div>
              <div v-else-if="workspace" class="workspace-details">
                <dl class="data-list">
                  <div><dt>名称</dt><dd>{{ workspace.name || "暂不可用" }}</dd></div><div><dt>计划</dt><dd>{{ workspace.packageId ? workspace.packageId.toUpperCase() : "暂不可用" }}</dd></div><div><dt>套餐规格</dt><dd>{{ workspacePlan ? `${workspacePlan.cpu}C / ${workspacePlan.memoryGb}GB` : "暂不可用" }}</dd></div><div><dt>月价</dt><dd>{{ typeof workspace.totalUsdMicros === "number" ? formatUsdMicros(workspace.totalUsdMicros) : "暂不可用" }}</dd></div><div><dt>创建时间</dt><dd>{{ formatDate(workspace.createdAt, true) }}</dd></div><div><dt>已付至</dt><dd>{{ formatDate(workspace.paidThrough) }}</dd></div><div><dt>续费状态</dt><dd>{{ workspace.renewalStatus || "暂不可用" }}</dd></div><div><dt>存储容量</dt><dd>{{ typeof workspace.storageGb === "number" ? `${workspace.storageGb} GB` : "暂不可用" }}</dd></div>
                  <div><dt>自动续费</dt><dd class="renewal-control"><input type="checkbox" :checked="workspace.autoRenew === true" disabled aria-describedby="auto-renew-reason" /><span>{{ workspace.autoRenew === false ? "已关闭" : "暂不可用" }}</span><small id="auto-renew-reason">真实续费验证完成前不可启用</small></dd></div>
                </dl>
                <div v-if="loading.runtime" class="loading-panel"><span class="spinner" />正在读取访问状态...</div>
                <div v-else-if="errors.runtime" class="inline-error"><AlertCircle :size="17" />{{ errors.runtime }}<button type="button" @click="loadWorkspaceStatus">重试</button></div>
                <div v-else-if="workspaceStatusSource?.status === 'unavailable'" class="empty-panel">访问状态暂不可用 <button class="text-button" type="button" @click="loadWorkspaceStatus">重试</button></div>
                <dl v-else class="data-list access-list">
                  <div><dt>状态</dt><dd>{{ runtime ? workspaceStatusLabel(runtime) : "暂不可用" }}</dd></div>
                  <div><dt>挂载状态</dt><dd>{{ mountCheck ? (mountCheck.ok ? "正常" : "需处理") : "暂不可用" }}</dd></div>
                  <div><dt>服务健康</dt><dd>{{ runtime ? (runtime.ready ? "正常" : "需处理") : "暂不可用" }}</dd></div>
                  <div><dt>Workspace URL</dt><dd><a v-if="runtime?.url" :href="runtime.url" target="_blank" rel="noreferrer">{{ runtime.url }} <ArrowUpRight :size="14" /></a><span v-else>暂不可用</span></dd></div>
                  <div><dt>用户名</dt><dd>{{ runtime?.access?.username || "暂不可用" }}</dd></div>
                  <div><dt>密码</dt><dd class="secret-value"><code>{{ revealedWorkspaceCredentials?.password || "已隐藏" }}</code><button class="text-button" type="button" :disabled="runtimeBusy || !workspaceCanOpen" @click="toggleWorkspaceCredentials">{{ revealedWorkspaceCredentials ? "隐藏" : "显示" }}</button><button class="text-button" type="button" :disabled="!revealedWorkspaceCredentials?.password" @click="copyWorkspacePassword"><Copy :size="15" />复制</button></dd></div>
                  <div><dt>Workspace Key</dt><dd class="secret-value"><code>{{ revealedApiKey?.id === workspaceKeyId ? revealedApiKey.value : workspaceKeyId ? "已隐藏" : "暂不可用" }}</code><button class="text-button" type="button" :disabled="gatewayBusy || !workspaceKeyId" @click="revealedApiKey?.id === workspaceKeyId ? hideKey() : revealKey()">{{ revealedApiKey?.id === workspaceKeyId ? "隐藏" : "显示" }}</button><button class="text-button" type="button" :disabled="revealedApiKey?.id !== workspaceKeyId || !revealedApiKey?.value" @click="copyWorkspaceKey"><Copy :size="15" />复制</button></dd></div>
                </dl>
                <div class="credential-actions"><button class="button secondary" type="button" :disabled="runtimeBusy || !workspaceCanOpen" @click="rotateWorkspace"><RefreshCw :size="16" />轮换密码</button></div>
              </div>
            </section>
          </section>

          <section v-else-if="apiRoute" class="api-page">
            <nav class="gateway-tabs" aria-label="API 服务导航"><a v-for="item in apiMenu" :key="item.path" :href="item.path" :class="{ active: activeApiPage === item.id }" @click.prevent="navigate(item.path)">{{ item.label }}</a></nav>
            <div v-if="activeApiPage === 'overview'" class="api-overview">
              <div v-if="loading.accountStats" class="loading-panel"><span class="spinner" />正在读取用量汇总...</div><div v-else-if="errors.accountStats" class="inline-error"><AlertCircle :size="17" />{{ errors.accountStats }}<button type="button" @click="loadAccountUsage">重试</button></div><div v-else-if="accountUsageSource?.status === 'unavailable'" class="empty-panel">用量汇总暂不可用 <button class="text-button" type="button" @click="loadAccountUsage">重试</button></div>
              <div v-if="loading.wallet" class="loading-panel"><span class="spinner" />正在读取余额...</div><div v-else-if="errors.wallet" class="inline-error"><AlertCircle :size="17" />{{ errors.wallet }}<button type="button" @click="loadWallet">重试</button></div><div v-else-if="walletSource?.status === 'unavailable'" class="empty-panel">余额暂不可用 <button class="text-button" type="button" @click="loadWallet">重试</button></div>
              <section class="metric-row"><article><WalletCards /><span>可用余额<strong>{{ wallet ? formatAvailableBalance({ ...wallet, available: true }) : "暂不可用" }}</strong></span></article><article><CircleDollarSign /><span>本月费用<strong>{{ stats ? formatUsdMicros(stats.totalActualCostUsdMicros) : "暂不可用" }}</strong></span></article><article><Activity /><span>请求次数<strong>{{ stats ? formatCount(stats.totalRequests) : "暂不可用" }}</strong></span></article></section>
              <section class="panel"><div class="panel-title"><h2>余额记录</h2></div><div v-if="loading.history" class="loading-panel"><span class="spinner" />正在读取余额记录...</div><div v-else-if="errors.history" class="inline-error"><AlertCircle :size="17" />{{ errors.history }}<button type="button" @click="loadHistory">重试</button></div><div v-else-if="balanceHistorySource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadHistory">重试</button></div><div v-else-if="balanceHistorySource?.status === 'empty'" class="empty-panel">暂无余额记录</div><div v-else class="table-wrap"><table><thead><tr><th>时间</th><th>类型</th><th>金额</th><th>状态</th></tr></thead><tbody><tr v-for="item in history" :key="`${item.createdAt}-${item.type}`"><td>{{ formatDate(item.createdAt, true) }}</td><td>{{ item.type }}</td><td>{{ formatUsdMicros(item.valueUsdMicros) }}</td><td>{{ item.status }}</td></tr></tbody></table></div></section>
            </div>
            <section v-else-if="activeApiPage === 'usage'" class="panel">
              <div v-if="loading.keys" class="loading-panel"><span class="spinner" />正在读取 API Key...</div>
              <div v-else-if="errors.keys" class="inline-error"><AlertCircle :size="17" />{{ errors.keys }}<button type="button" @click="loadKeys">重试</button></div>
              <div v-else-if="keySource?.status === 'unavailable'" class="empty-panel">API Key 暂不可用 <button class="text-button" type="button" @click="loadKeys">重试</button></div>
              <div v-else-if="keySource?.status === 'empty'" class="empty-panel">暂无 API Key</div>
              <template v-else>
                <div class="gateway-usage-toolbar"><label>API Key<select v-model="selectedUsageKeyId" @change="selectUsageKey(selectedUsageKeyId)"><option v-for="key in keys" :key="key.id" :value="key.id">{{ key.name }}</option></select></label><div class="segmented-control" aria-label="用量周期"><button v-for="item in [{ id: 'today', label: '今日' }, { id: 'week', label: '本周' }, { id: 'month', label: '本月' }]" :key="item.id" type="button" :class="{ active: gatewayPeriod === item.id }" @click="selectPeriod(item.id)">{{ item.label }}</button></div></div>
                <div v-if="errors.stats" class="inline-error"><AlertCircle :size="17" />{{ errors.stats }}<button type="button" @click="loadStats">重试</button></div><div v-else-if="usageStatsSource?.status === 'unavailable'" class="empty-panel">用量汇总暂不可用 <button class="text-button" type="button" @click="loadStats">重试</button></div><section v-else-if="keyStats" class="metric-row"><article><Activity /><span>请求次数<strong>{{ formatCount(keyStats.totalRequests) }}</strong></span></article><article><CircleDollarSign /><span>实际金额<strong>{{ formatUsdMicros(keyStats.totalActualCostUsdMicros) }}</strong></span></article></section>
                <div v-if="loading.usage" class="loading-panel"><span class="spinner" />正在读取使用记录...</div><div v-else-if="errors.usage" class="inline-error"><AlertCircle :size="17" />{{ errors.usage }}<button type="button" @click="loadUsage">重试</button></div><div v-else-if="usageSource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadUsage">重试</button></div><div v-else-if="!selectedUsageKeyId || usageSource?.status === 'empty'" class="empty-panel">暂无使用记录</div><div v-else class="table-wrap"><table class="gateway-usage-table"><thead><tr><th>时间</th><th>模型</th><th>端点</th><th>输入 Token</th><th>输出 Token</th><th>实际金额</th><th>请求编号</th></tr></thead><tbody><tr v-for="item in usage?.items || []" :key="item.requestId"><td>{{ formatDate(item.createdAt, true) }}</td><td>{{ item.model }}</td><td>{{ item.inboundEndpoint }}</td><td>{{ formatCount(item.inputTokens) }}</td><td>{{ formatCount(item.outputTokens) }}</td><td>{{ formatUsdMicros(item.actualCostUsdMicros) }}</td><td><code>{{ item.requestId }}</code></td></tr></tbody></table></div><div class="pagination"><button class="icon-button" type="button" aria-label="上一页" :disabled="gatewayPageNumber.page <= 1 || loading.usage" @click="changeUsagePage(gatewayPageNumber.page - 1)"><ChevronLeft :size="16" /></button><span>{{ gatewayPageNumber.page }}</span><button class="icon-button" type="button" aria-label="下一页" :disabled="gatewayPageNumber.pages === 0 || gatewayPageNumber.page >= gatewayPageNumber.pages || loading.usage" @click="changeUsagePage(gatewayPageNumber.page + 1)"><ChevronRight :size="16" /></button></div>
              </template>
            </section>
            <section v-else class="panel"><div class="panel-title"><h2>API Key</h2><button class="button primary" type="button" @click="openModal('api-key')"><Plus :size="16" />创建 Key</button></div><div v-if="loading.keys" class="loading-panel"><span class="spinner" />正在读取 API Key...</div><div v-else-if="errors.keys" class="inline-error"><AlertCircle :size="17" />{{ errors.keys }}<button type="button" @click="loadKeys">重试</button></div><div v-else-if="keySource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadKeys">重试</button></div><div v-else-if="keySource?.status === 'empty'" class="empty-panel">暂无 API Key</div><div v-else class="table-wrap"><table><thead><tr><th>名称</th><th>类型</th><th>状态</th><th>限额</th><th>已用</th><th>到期时间</th><th>最近使用</th><th>操作</th></tr></thead><tbody><tr v-for="key in keys" :key="key.id"><td>{{ key.name }}</td><td>{{ key.kind === "workspace" ? "Workspace Key" : "普通 Key" }}</td><td>{{ key.status }}</td><td>{{ formatUsdMicros(key.quotaUsdMicros) }}</td><td>{{ formatUsdMicros(key.quotaUsedUsdMicros) }}</td><td>{{ key.expiresAt ? formatDate(key.expiresAt) : "-" }}</td><td>{{ key.lastUsedAt ? formatDate(key.lastUsedAt, true) : "-" }}</td><td class="table-actions"><button class="text-button" type="button" :disabled="gatewayBusy" @click="revealedApiKey?.id === key.id ? hideKey() : revealKey(key)"><EyeOff v-if="revealedApiKey?.id === key.id" :size="15" /><Eye v-else :size="15" />{{ revealedApiKey?.id === key.id ? "隐藏" : "显示" }}</button><button class="text-button" type="button" :disabled="revealedApiKey?.id !== key.id || !revealedApiKey?.value" @click="copyKey(key)"><Copy :size="15" />复制</button><button v-if="key.manageable" class="text-button" type="button" :disabled="gatewayBusy" @click="toggleKey(key)">{{ key.status === 'active' ? '停用' : '启用' }}</button><button v-if="key.deletable" class="text-button danger-text" type="button" :disabled="gatewayBusy" @click="removeKey(key)">删除</button></td></tr><tr v-if="revealedApiKey"><td colspan="8"><div class="secret-panel"><span>{{ revealedApiKey.name }}</span><code>{{ revealedApiKey.value }}</code></div></td></tr></tbody></table></div></section>
          </section>

          <section v-else-if="path.startsWith('/console/announcements')" class="announcements-page">
            <section class="panel"><div class="panel-title"><h2>公告</h2><button class="icon-button" type="button" title="刷新" aria-label="刷新公告" :disabled="loading.announcements" @click="loadAnnouncements"><RefreshCw :size="16" /></button></div><div v-if="loading.announcements" class="loading-panel"><span class="spinner" />正在读取公告...</div><div v-else-if="errors.announcements" class="inline-error"><AlertCircle :size="17" />{{ errors.announcements }}<button type="button" @click="loadAnnouncements">重试</button></div><div v-else-if="announcementsUnavailable" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadAnnouncements">重试</button></div><div v-else-if="announcementsEmpty" class="empty-panel">暂无公告</div><div v-else class="announcement-list"><article v-for="announcement in announcements" :key="announcement.id" class="announcement-item"><header><div><h3>{{ announcement.title }}</h3><span>{{ formatDate(announcement.publishedAt || announcement.startsAt, true) }}</span></div><span class="status-pill" :class="{ good: announcement.read }">{{ announcement.read ? "已读" : "未读" }}</span></header><p>{{ announcement.body }}</p><button v-if="!announcement.read" class="button secondary" type="button" :disabled="announcementBusy === announcement.id" @click="readAnnouncement(announcement.id)">{{ announcementBusy === announcement.id ? "处理中..." : "标记已读" }}</button></article></div></section>
          </section>

          <section v-else class="billing-page">
            <div v-if="loading.wallet" class="loading-panel"><span class="spinner" />正在读取余额...</div><div v-else-if="errors.wallet" class="inline-error"><AlertCircle :size="17" />{{ errors.wallet }}<button type="button" @click="loadWallet">重试</button></div><div v-else-if="walletSource?.status === 'unavailable'" class="empty-panel">余额暂不可用 <button class="text-button" type="button" @click="loadWallet">重试</button></div>
            <div class="metric-row"><article><WalletCards /><span>可用余额<strong>{{ wallet ? formatAvailableBalance({ ...wallet, available: true }) : "暂不可用" }}</strong></span></article><article><CircleDollarSign /><span>固定月支出<strong>{{ workspace ? formatUsdMicros(workspace.totalUsdMicros) : "暂不可用" }}</strong></span></article><article><Activity /><span>AI 用量<strong>{{ stats ? formatUsdMicros(stats.totalActualCostUsdMicros) : "暂不可用" }}</strong></span></article></div>
            <section class="panel"><div class="panel-title"><h2>Workspace 账单</h2></div><div v-if="workspaceSource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadWorkspaces">重试</button></div><div v-else-if="workspace" class="table-wrap"><table><thead><tr><th>Workspace</th><th>计划</th><th>金额</th><th>有效期至</th><th>续费状态</th></tr></thead><tbody><tr><td>{{ workspace.name || "暂不可用" }}</td><td>{{ workspace.packageId || "暂不可用" }}</td><td>{{ formatUsdMicros(workspace.totalUsdMicros) }}</td><td>{{ formatDate(workspace.paidThrough) }}</td><td>{{ workspace.renewalStatus || "暂不可用" }}</td></tr></tbody></table></div><div v-else class="empty-panel">暂无 Workspace</div></section>
            <section class="panel"><div class="panel-title"><h2>余额记录</h2></div><div v-if="loading.history" class="loading-panel"><span class="spinner" />正在读取余额记录...</div><div v-else-if="errors.history" class="inline-error"><AlertCircle :size="17" />{{ errors.history }}<button type="button" @click="loadHistory">重试</button></div><div v-else-if="balanceHistorySource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadHistory">重试</button></div><div v-else-if="balanceHistorySource?.status === 'empty'" class="empty-panel">暂无余额记录</div><div v-else class="table-wrap"><table><thead><tr><th>时间</th><th>类型</th><th>金额</th><th>状态</th></tr></thead><tbody><tr v-for="item in history" :key="`${item.createdAt}-${item.type}`"><td>{{ formatDate(item.createdAt, true) }}</td><td>{{ item.type }}</td><td>{{ formatUsdMicros(item.valueUsdMicros) }}</td><td>{{ item.status }}</td></tr></tbody></table></div></section>
            <section class="panel">
              <div class="panel-title"><h2>交易记录</h2></div>
              <div v-if="loading.receipts" class="loading-panel"><span class="spinner" />正在读取交易记录...</div><div v-else-if="errors.receipts" class="inline-error"><AlertCircle :size="17" />{{ errors.receipts }}<button type="button" @click="loadReceipts">重试</button></div><div v-else-if="receiptsSource?.status === 'unavailable'" class="empty-panel">暂不可用 <button class="text-button" type="button" @click="loadReceipts">重试</button></div><div v-else-if="receiptsSource?.status === 'empty'" class="empty-panel">暂无交易记录</div><div v-else class="table-wrap"><table><thead><tr><th>时间</th><th>交易</th><th>金额</th><th>有效期至</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-for="receipt in receipts" :key="receipt.receiptId"><td>{{ formatDate(receipt.createdAt, true) }}</td><td>{{ receiptLabel(receipt.type) }}</td><td>{{ formatUsdMicros(receipt.chargeUsdMicros ?? receipt.totalUsdMicros) }}</td><td>{{ formatDate(receipt.paidThrough) }}</td><td>{{ receipt.status }}</td><td><button class="text-button" type="button" :disabled="loading.receiptDetail && selectedReceiptId === receipt.receiptId" @click="loadReceiptDetail(receipt.receiptId)">{{ loading.receiptDetail && selectedReceiptId === receipt.receiptId ? "读取中..." : "查看" }}</button></td></tr></tbody></table></div>
              <nav v-if="receiptsSource?.available && (receiptCursorStack.length || receiptsSource.data.hasMore)" class="pagination" aria-label="交易记录分页"><button class="button secondary" type="button" :disabled="loading.receipts || receiptCursorStack.length === 0" @click="previousReceiptPage"><ChevronLeft :size="16" />上一页</button><span>第 {{ receiptCursorStack.length + 1 }} 页</span><button class="button secondary" type="button" :disabled="loading.receipts || !receiptsSource.data.hasMore || !receiptsSource.data.nextCursor" @click="nextReceiptPage">下一页<ChevronRight :size="16" /></button></nav>
            </section>
            <section v-if="selectedReceiptId" class="panel receipt-detail">
              <div class="panel-title"><h2>交易详情</h2><button class="icon-button" type="button" title="关闭" aria-label="关闭交易详情" @click="clearReceiptDetail"><X :size="18" /></button></div>
              <div v-if="loading.receiptDetail" class="loading-panel"><span class="spinner" />正在读取交易详情...</div><div v-else-if="errors.receiptDetail" class="inline-error"><AlertCircle :size="17" />{{ errors.receiptDetail }}<button type="button" @click="loadReceiptDetail(selectedReceiptId)">重试</button></div><div v-else-if="receiptDetailSource?.status === 'unavailable'" class="empty-panel">交易详情暂不可用 <button class="text-button" type="button" @click="loadReceiptDetail(selectedReceiptId)">重试</button></div>
              <dl v-else-if="receiptDetail" class="data-list"><div><dt>交易</dt><dd>{{ receiptLabel(receiptDetail.type) }}</dd></div><div><dt>状态</dt><dd>{{ receiptDetail.status || "暂不可用" }}</dd></div><div><dt>时间</dt><dd>{{ formatDate(receiptDetail.createdAt, true) }}</dd></div><div><dt>Workspace</dt><dd>{{ workspace?.id === receiptDetail.workspaceId ? workspace.name : receiptDetail.workspaceId || "暂不可用" }}</dd></div><div><dt>金额</dt><dd>{{ formatUsdMicros(receiptDetail.refundUsdMicros ?? receiptDetail.chargeUsdMicros ?? receiptDetail.totalUsdMicros) }}</dd></div><div><dt>计费周期</dt><dd>{{ receiptDetail.periodStart && receiptDetail.paidThrough ? `${formatDate(receiptDetail.periodStart)} 至 ${formatDate(receiptDetail.paidThrough)}` : "暂不可用" }}</dd></div><div><dt>价格版本</dt><dd>{{ receiptDetail.priceVersion || "暂不可用" }}</dd></div></dl>
              <div v-else class="empty-panel">交易详情暂不可用 <button class="text-button" type="button" @click="loadReceiptDetail(selectedReceiptId)">重试</button></div>
            </section>
          </section>
      </div>
    </section>

        <div v-if="modal" class="modal-backdrop" role="presentation" @click.self="closeModal"><section class="modal" role="dialog" aria-modal="true" :aria-label="modal"><header><h2>{{ modal === "workspace" ? "开通 Workspace" : modal === "api-key" ? "创建 API Key" : modal === "wallet-adjustment" ? "钱包调整" : modal === "announcement" ? "新建公告草稿" : "开通用户" }}</h2><button class="icon-button" type="button" aria-label="关闭" @click="closeModal"><X :size="18" /></button></header><form v-if="modal === 'workspace'" @submit.prevent="submitWorkspaceLaunch"><label>Workspace 名称<input v-model.trim="launchForm.name" required maxlength="80" /></label><fieldset><legend>计划</legend><label v-for="plan in plans" :key="plan.id" class="plan-option" :class="{ selected: launchForm.packageId === plan.id }"><input v-model="launchForm.packageId" type="radio" :value="plan.id" /><span><strong>{{ plan.name }}</strong><small>{{ plan.cpu }}C / {{ plan.memoryGb }}GB · {{ plan.diskGb }}GB</small></span><b>{{ typeof previews[plan.id]?.totalChargeUsdMicros === "number" ? `${formatUsdMicros(previews[plan.id]?.totalChargeUsdMicros)}/月` : "暂不可用" }}</b></label></fieldset><p class="source-note">自动续费默认关闭。</p><footer><button class="button secondary" type="button" @click="closeModal">取消</button><button class="button primary" type="submit" :disabled="launchBusy || !selectedPlan || selectedPlanPrice === null">{{ launchBusy ? "处理中..." : "确认开通" }}</button></footer></form><form v-else-if="modal === 'api-key'" @submit.prevent="submitKey"><label>名称<input v-model.trim="keyForm.name" required maxlength="80" /></label><label>限额（USD）<input v-model.number="keyForm.quotaUsd" type="number" min="1" step="1" required /></label><label>有效天数<input v-model.number="keyForm.expiresInDays" type="number" min="1" max="365" step="1" required /></label><footer><button class="button secondary" type="button" @click="closeModal">取消</button><button class="button primary" type="submit" :disabled="gatewayBusy">{{ gatewayBusy ? "创建中..." : "创建" }}</button></footer></form><form v-else-if="modal === 'wallet-adjustment'" @submit.prevent="submitWalletAdjustment"><p class="source-note">二次确认会锁定目标账号、金额和原因；同一 Idempotency-Key 不会重复调整。</p><label>目标账号<input v-model.trim="walletAdjustmentForm.confirmationAccountId" required /></label><label>类型<select v-model="walletAdjustmentForm.kind"><option value="recharge">充值</option><option value="debit">扣款</option><option value="business_refund">业务退款</option></select></label><label>金额（USD）<input v-model.trim="walletAdjustmentForm.amountUsd" inputmode="decimal" required /></label><label>原因<textarea v-model.trim="walletAdjustmentForm.reason" required maxlength="200" /></label><label v-if="walletAdjustmentForm.kind === 'business_refund'">关联操作<input v-model.trim="walletAdjustmentForm.relatedOperationId" required /></label><p v-if="errors.walletAdjustment" class="form-error" role="alert">{{ errors.walletAdjustment }}</p><section v-if="walletAdjustmentOperation" class="wallet-adjustment-readback"><div class="inline-notice">操作 {{ walletAdjustmentOperation.operationId }}：{{ walletAdjustmentOperation.status }} <button class="text-button" type="button" @click="refreshWalletAdjustment">读取最新状态</button></div><dl class="data-list"><div><dt>调整前余额</dt><dd>{{ operatorSourceText(walletAdjustmentOperation.beforeBalance, (data) => formatUsdMicros(data.usdMicros)) }}</dd></div><div><dt>调整后余额</dt><dd>{{ operatorSourceText(walletAdjustmentOperation.afterBalance, (data) => formatUsdMicros(data.usdMicros)) }}</dd></div><div><dt>原因</dt><dd>{{ walletAdjustmentOperation.reason || "暂不可用" }}</dd></div><div><dt>关联操作</dt><dd>{{ walletAdjustmentOperation.relatedOperationId || "暂不可用" }}</dd></div><div><dt>余额记录引用</dt><dd>{{ walletAdjustmentOperation.balanceHistoryRef || "暂不可用" }}</dd></div><div><dt>执行人</dt><dd>{{ walletAdjustmentOperation.actor || "暂不可用" }}</dd></div></dl></section><footer><button class="button secondary" type="button" @click="closeModal">取消</button><button class="button primary" type="submit" :disabled="loading.walletAdjustment">{{ loading.walletAdjustment ? "处理中..." : "确认调整" }}</button></footer></form><form v-else-if="modal === 'announcement'" @submit.prevent="submitOperatorAnnouncement"><label>标题<input v-model.trim="announcementForm.title" required maxlength="120" /></label><label>正文<textarea v-model.trim="announcementForm.body" required maxlength="4000" /></label><label>开始时间（可选）<input v-model.trim="announcementForm.startsAt" placeholder="2026-07-20T00:00:00Z" /></label><label>结束时间（可选）<input v-model.trim="announcementForm.endsAt" placeholder="2026-07-21T00:00:00Z" /></label><footer><button class="button secondary" type="button" @click="closeModal">取消</button><button class="button primary" type="submit">保存草稿</button></footer></form><form v-else @submit.prevent="provisionOperatorUser"><label>登录邮箱<input v-model.trim="adminUserForm.email" type="email" required /></label><label>初始密码<input v-model="adminUserForm.password" type="password" required minlength="12" /></label><label>姓名<input v-model.trim="adminUserForm.name" /></label><footer><button class="button secondary" type="button" @click="closeModal">取消</button><button class="button primary" type="submit" :disabled="mutationBusy">{{ mutationBusy ? "处理中..." : "开通用户" }}</button></footer></form></section></div>
    <div v-if="toast.text" class="toast" :class="toast.tone" role="status">{{ toast.text }}</div>
  </div>
</template>
