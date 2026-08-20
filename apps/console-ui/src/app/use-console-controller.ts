import { useEffect, useMemo, useRef, useState } from "react";

import { currentSession, login as loginRequest, logoutAndConfirm } from "../api/auth-api.ts";
import {
  createSupportTicketMapping,
  createOperatorAnnouncement,
  createWalletAdjustment,
  disableOperatorAccount as disableOperatorAccountCommand,
  getAnnouncements,
  getBillingReceipt,
  getBillingReceipts,
  getGatewayAccountUsageSummary,
  getGatewayBalanceHistory,
  getGatewayEndpoint,
  getGatewayKeys,
  getGatewayKeyUsage,
  getGatewayKeyUsageSummary,
  getGatewayWallet,
  getOperatorAccountsPage,
  getOperatorAnnouncements,
  getOperatorHealth,
  getOperatorOverview,
  getOperatorReconciliation,
  getOperatorWorkspace,
  getOperatorWorkspaces,
  getPricingCatalog,
  getSupportTickets,
  getWalletAdjustment,
  markAnnouncementRead,
  previewPricing,
  provisionOperatorAccount,
  publishOperatorAnnouncement,
  recoverWalletAdjustment,
  revealGatewayKey,
  setOperatorWorkspacePurchaseEligibility as setOperatorWorkspacePurchaseEligibilityCommand,
  withdrawOperatorAnnouncement
} from "../api/console-read-api.ts";
import type {
  AnnouncementDraftRequest,
  AnnouncementScheduleRequest,
  AuthSession,
  CreateSupportTicketMappingRequest,
  GatewayUsagePeriod,
  OperatorAccountDTO,
  OperatorAccountCommandDTO,
  PlanId,
  ProvisionAccountRequest,
  SourceEnvelope,
  WalletAdjustmentRecoveryRequest,
  WalletAdjustmentRequest,
  WorkspaceGatewayBudgetDTO,
  WorkspaceGatewayBudgetUpdateRequest,
  WorkspaceLaunchRequest,
  WorkspacePricePreview
} from "../api/dtos.ts";
import {
  deleteWorkspace as deleteWorkspaceCommand,
  findWorkspaceInPages,
  getWorkspaceLaunch,
  getWorkspaceLaunches,
  getWorkspaceGatewayBudget,
  getWorkspaces,
  getWorkspaceRuntimeStatus,
  isTerminalWorkspaceLaunch,
  launchWorkspace,
  revealWorkspaceCredentials,
  rotateWorkspaceCredentials,
  updateWorkspaceGatewayBudget,
  updateWorkspaceRenewal,
  workspaceDeleteIdempotencyKey,
  workspaceLaunchIdempotencyKey
} from "../api/workspaces-api.ts";
import { defaultAuthenticatedRoute, hasSufficientWorkspaceLaunchBalance, needsSession, workspaceIdFromPath, workspacePage } from "../console-model.ts";
import { isKnownConsoleRoute, isSensitiveConsoleRoute, useConsoleRouter } from "./console-router.ts";
import type { AuthStatus, BillingView, ConsoleSecrets, ConsoleSources, GlobalSlide, RemoteState, WorkspaceLaunchStep } from "./console-controller-types.ts";

const secretLifetimeMs = 60_000;
const workspaceLaunchPollIntervalMs = 10_000;
const workspaceLaunchPollAttempts = 30;
const operatorPageSize = 20;

type WorkspaceBudgetIntent = {
  keyId: string;
  signature: string;
  input: WorkspaceGatewayBudgetUpdateRequest;
  idempotencyKey: string;
};

type WorkspaceRenewalIntent = {
  autoRenew: boolean;
  idempotencyKey: string;
};

const emptyRemote = <T,>(): RemoteState<T> => ({ value: null, loading: false, error: "" });

function initialSources(): ConsoleSources {
  return {
    workspaces: emptyRemote(),
    workspaceDetail: emptyRemote(),
    runtime: emptyRemote(),
    workspaceBudget: emptyRemote(),
    wallet: emptyRemote(),
    accountUsage: emptyRemote(),
    balanceHistory: emptyRemote(),
    receipts: emptyRemote(),
    receiptDetail: emptyRemote(),
    announcements: emptyRemote(),
    catalog: emptyRemote(),
    usageKeys: emptyRemote(),
    usage: emptyRemote(),
    usageSummary: emptyRemote(),
    endpoint: emptyRemote(),
    operatorOverview: emptyRemote(),
    operatorAccounts: emptyRemote(),
    operatorWorkspaces: emptyRemote(),
    operatorWorkspaceDetail: emptyRemote(),
    operatorReconciliation: emptyRemote(),
    operatorHealth: emptyRemote(),
    operatorAnnouncements: emptyRemote()
  };
}

export function unavailableSource<T>(source: string): SourceEnvelope<T> {
  const normalizedSource = source.trim().toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "unknown";
  return {
    source,
    status: "unavailable",
    available: false,
    fetchedAt: new Date().toISOString(),
    reasonCode: `${normalizedSource}_unavailable`
  };
}

function friendlyError(error: unknown) {
  const raw = String(error && typeof error === "object" && "message" in error ? error.message : error || "request_failed");
  const messages: Record<string, string> = {
    not_authenticated: "登录已失效，请重新登录",
    account_scope_forbidden: "没有权限访问该资源",
    workspace_purchase_not_enabled: "该账户尚未获得 Workspace 新购资格",
    insufficient_balance: "可用余额不足",
    gateway_key_missing: "API Key 尚未就绪",
    gateway_key_ambiguous: "API Key 状态异常，请联系管理员",
    monthly_account_unmapped: "API 服务尚未开通",
    authentication_unavailable: "身份服务暂不可用，请稍后重试",
    workspace_not_found: "Workspace 不存在或无权访问",
    workspace_credentials_unavailable: "Workspace 凭证暂不可用",
    workspace_not_running: "Workspace 尚未就绪",
    workspace_reactivation_required: "Workspace 已到期，需先完成重新激活",
    upstream_unavailable: "服务暂不可用，请稍后重试"
  };
  return messages[raw] || (raw.includes("failed") || raw.includes("_") ? "请求失败，请重试" : raw);
}

function apiErrorCode(error: unknown) {
  const payload = error && typeof error === "object" && "payload" in error
    ? (error as { payload?: unknown }).payload
    : null;
  return payload && typeof payload === "object" ? String((payload as { error?: unknown }).error || "") : "";
}

function mutationError(error: unknown) {
  const code = apiErrorCode(error);
  return code ? friendlyError(code) : "结果待确认，请刷新操作状态，不要重复提交";
}

function sameLaunchRequest(left: WorkspaceLaunchRequest, right: WorkspaceLaunchRequest) {
  return left.name === right.name && left.packageId === right.packageId && left.autoRenew === right.autoRenew;
}

function workspaceBudgetRequestSignature(input: WorkspaceGatewayBudgetUpdateRequest) {
  return JSON.stringify([
    input.quotaUsdMicros ?? null,
    input.rateLimit5hUsdMicros ?? null,
    input.rateLimit1dUsdMicros ?? null,
    input.rateLimit7dUsdMicros ?? null,
    input.enabled ?? null,
    input.resetQuota ?? null,
    input.resetRateLimitUsage ?? null
  ]);
}

function workspaceBudgetResultMatchesInput(
  result: WorkspaceGatewayBudgetDTO,
  input: WorkspaceGatewayBudgetUpdateRequest
) {
  return (input.quotaUsdMicros === undefined || result.quotaUsdMicros === String(input.quotaUsdMicros))
    && (input.rateLimit5hUsdMicros === undefined || result.rateLimit5hUsdMicros === String(input.rateLimit5hUsdMicros))
    && (input.rateLimit1dUsdMicros === undefined || result.rateLimit1dUsdMicros === String(input.rateLimit1dUsdMicros))
    && (input.rateLimit7dUsdMicros === undefined || result.rateLimit7dUsdMicros === String(input.rateLimit7dUsdMicros))
    && (input.enabled === undefined || result.enabled === input.enabled);
}

function walletRecoveryIdempotencyKey(operationId: string) {
  const suffix = /^wallet-adjustment-([0-9a-f]{18})$/.exec(operationId)?.[1] || "";
  return suffix ? `wallet-recovery-${suffix.slice(0, 16)}` : "";
}

export function useConsoleController() {
  const { path, navigate } = useConsoleRouter();
  const [session, setSession] = useState<AuthSession | null>(null);
  const [authStatus, setAuthStatus] = useState<AuthStatus>(needsSession(path) ? "checking" : "public");
  const [authError, setAuthError] = useState("");
  const [sources, setSources] = useState<ConsoleSources>(initialSources);
  const [toast, setToast] = useState<{ text: string; tone: "good" | "danger" }>({ text: "", tone: "good" });
  const [globalSlide, setGlobalSlide] = useState<GlobalSlide>("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [workspacePageNumber, setWorkspacePageNumber] = useState(1);
  const [balanceHistoryPage, setBalanceHistoryPage] = useState(1);
  const [receiptCursor, setReceiptCursor] = useState("");
  const [receiptCursorStack, setReceiptCursorStack] = useState<string[]>([]);
  const [operatorAccountPage, setOperatorAccountPage] = useState(1);
  const [operatorWorkspacePage, setOperatorWorkspacePage] = useState(1);
  const [selectedOperatorWorkspaceId, setSelectedOperatorWorkspaceId] = useState("");
  const [billingView, setBillingView] = useState<BillingView>("terms");
  const [selectedReceiptId, setSelectedReceiptId] = useState("");
  const [selectedUsageKeyId, setSelectedUsageKeyId] = useState("");
  const [usagePeriod, setUsagePeriod] = useState<GatewayUsagePeriod>("month");
  const [usagePage, setUsagePage] = useState(1);
  const [launchName, setLaunchName] = useState("");
  const [launchPlan, setLaunchPlan] = useState<PlanId>("basic");
  const [launchAutoRenew, setLaunchAutoRenew] = useState(false);
  const [launchStep, setLaunchStep] = useState<WorkspaceLaunchStep>("configure");
  const [launchConfirmed, setLaunchConfirmed] = useState(false);
  const [previews, setPreviews] = useState<Partial<Record<PlanId, WorkspacePricePreview>>>({});
  const [launchOperation, setLaunchOperation] = useState<Awaited<ReturnType<typeof getWorkspaceLaunch>> | null>(null);
  const [launchPollIssue, setLaunchPollIssue] = useState<"" | "error" | "timeout" | "readback">("");
  const [workspaceDeleteIssue, setWorkspaceDeleteIssue] = useState<"" | "unavailable" | "unconfirmed">("");
  const [secrets, setSecrets] = useState<ConsoleSecrets>({ apiKey: null, workspace: null });
  const [workspaceSecretBusy, setWorkspaceSecretBusy] = useState(false);
  const [gatewaySecretBusy, setGatewaySecretBusy] = useState(false);
  const [workspaceBudgetBusy, setWorkspaceBudgetBusy] = useState(false);
  const [commandBusy, setCommandBusy] = useState(false);
  const [announcementBusy, setAnnouncementBusy] = useState("");
  const [walletAdjustmentOperation, setWalletAdjustmentOperation] = useState<Awaited<ReturnType<typeof getWalletAdjustment>> | null>(null);
  const [operatorProvisionOperation, setOperatorProvisionOperation] = useState<OperatorAccountCommandDTO | null>(null);
  const [supportTickets, setSupportTickets] = useState<Awaited<ReturnType<typeof getSupportTickets>> | null>(null);
  const [supportLoading, setSupportLoading] = useState(false);
  const [supportError, setSupportError] = useState("");

  const requestGeneration = useRef(0);
  const sessionGeneration = useRef(0);
  const secretRequestGeneration = useRef(0);
  const loginAttemptGeneration = useRef(0);
  const loginAbortController = useRef<AbortController | null>(null);
  const logoutAttemptGeneration = useRef(0);
  const logoutInFlight = useRef(false);
  const logoutState = useRef<"idle" | "pending" | "unconfirmed">("idle");
  const sessionRef = useRef<AuthSession | null>(null);
  const selectedReceiptIdRef = useRef("");
  const receiptCursorRef = useRef("");
  const receiptRequestGeneration = useRef(0);
  const receiptDetailRequestGeneration = useRef(0);
  const selectedUsageKeyIdRef = useRef("");
  const usageRequestGeneration = useRef(0);
  const workspaceDetailRequestGeneration = useRef(0);
  const selectedOperatorWorkspaceIdRef = useRef("");
  const secretTimer = useRef<number | undefined>(undefined);
  const toastTimer = useRef<number | undefined>(undefined);
  const workspaceLaunchIntent = useRef<{ input: WorkspaceLaunchRequest; idempotencyKey: string } | null>(null);
  const workspaceDeleteIntent = useRef<{ workspaceId: string; idempotencyKey: string } | null>(null);
  const runtimeRotationIntent = useRef<{ workspaceId: string; idempotencyKey: string } | null>(null);
  const workspaceRenewalIntents = useRef(new Map<string, WorkspaceRenewalIntent>());
  const workspaceBudgetIntents = useRef(new Map<string, WorkspaceBudgetIntent>());
  const workspaceBudgetBusyClaim = useRef<symbol | null>(null);
  const walletAdjustmentIntent = useRef<{ accountId: string; input: WalletAdjustmentRequest; idempotencyKey: string } | null>(null);
  const walletAdjustmentRecoveryIntent = useRef<{ operationId: string; input: WalletAdjustmentRecoveryRequest; idempotencyKey: string } | null>(null);
  const operatorProvisionIntent = useRef<{ input: ProvisionAccountRequest; idempotencyKey: string } | null>(null);
  const supportCreateIntent = useRef<{ input: CreateSupportTicketMappingRequest; idempotencyKey: string } | null>(null);
  const operatorDisableIntents = useRef(new Map<string, string>());
  const operatorWorkspaceEligibilityIntents = useRef(new Map<string, { enabled: boolean; idempotencyKey: string }>());
  const announcementCreateIntent = useRef<{ input: AnnouncementDraftRequest; idempotencyKey: string } | null>(null);
  const announcementPublishIntents = useRef(new Map<string, { input: AnnouncementScheduleRequest; idempotencyKey: string }>());
  const announcementWithdrawIntents = useRef(new Map<string, string>());

  const updateSource = <K extends keyof ConsoleSources>(key: K, patch: Partial<RemoteState<ConsoleSources[K]["value"]>>) => {
    setSources((current) => ({ ...current, [key]: { ...current[key], ...patch } } as ConsoleSources));
  };

  const beginSource = <K extends keyof ConsoleSources>(key: K) => updateSource(key, { loading: true, error: "" });

  const failSource = <K extends keyof ConsoleSources>(key: K, error: unknown, fallback?: ConsoleSources[K]["value"]) => {
    updateSource(key, { loading: false, error: friendlyError(error), ...(fallback !== undefined ? { value: fallback } : {}) });
  };

  const flash = (text: string, tone: "good" | "danger" = "good") => {
    setToast({ text, tone });
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast({ text: "", tone: "good" }), 3200);
  };

  const clearSecrets = () => {
    secretRequestGeneration.current += 1;
    if (secretTimer.current) window.clearTimeout(secretTimer.current);
    secretTimer.current = undefined;
    setSecrets({ apiKey: null, workspace: null });
  };

  const armSecretTimeout = () => {
    if (secretTimer.current) window.clearTimeout(secretTimer.current);
    secretTimer.current = window.setTimeout(clearSecrets, secretLifetimeMs);
  };

  const clearReceiptDetail = () => {
    receiptDetailRequestGeneration.current += 1;
    selectedReceiptIdRef.current = "";
    setSelectedReceiptId("");
    updateSource("receiptDetail", { value: null, loading: false, error: "" });
  };

  const resetConsoleState = () => {
    clearSecrets();
    setSources(initialSources());
    setGlobalSlide("");
    setLaunchOperation(null);
    setLaunchPollIssue("");
    setWorkspaceDeleteIssue("");
    setLaunchStep("configure");
    setLaunchConfirmed(false);
    setLaunchName("");
    setLaunchPlan("basic");
    setLaunchAutoRenew(false);
    setPreviews({});
    usageRequestGeneration.current += 1;
    setSelectedUsageKeyId("");
    selectedUsageKeyIdRef.current = "";
    setUsagePage(1);
    setBillingView("terms");
    clearReceiptDetail();
    setReceiptCursor("");
    receiptCursorRef.current = "";
    setReceiptCursorStack([]);
    receiptRequestGeneration.current += 1;
    setWorkspacePageNumber(1);
    setBalanceHistoryPage(1);
    setOperatorAccountPage(1);
    setOperatorWorkspacePage(1);
    setSelectedOperatorWorkspaceId("");
    selectedOperatorWorkspaceIdRef.current = "";
    setWalletAdjustmentOperation(null);
    setOperatorProvisionOperation(null);
    setSupportTickets(null);
    setSupportLoading(false);
    setSupportError("");
    setCommandBusy(false);
    setWorkspaceBudgetBusy(false);
    setAnnouncementBusy("");
    workspaceLaunchIntent.current = null;
    workspaceDeleteIntent.current = null;
    runtimeRotationIntent.current = null;
    workspaceRenewalIntents.current.clear();
    workspaceBudgetBusyClaim.current = null;
    workspaceBudgetIntents.current.clear();
    workspaceDetailRequestGeneration.current += 1;
    walletAdjustmentIntent.current = null;
    walletAdjustmentRecoveryIntent.current = null;
    operatorProvisionIntent.current = null;
    supportCreateIntent.current = null;
    announcementCreateIntent.current = null;
    operatorDisableIntents.current.clear();
    announcementPublishIntents.current.clear();
    announcementWithdrawIntents.current.clear();
  };

  const invalidateLoginAttempt = () => {
    loginAttemptGeneration.current += 1;
    loginAbortController.current?.abort();
    loginAbortController.current = null;
  };

  const replaceSession = (next: AuthSession | null) => {
    invalidateLoginAttempt();
    logoutAttemptGeneration.current += 1;
    logoutInFlight.current = false;
    logoutState.current = "idle";
    sessionGeneration.current += 1;
    requestGeneration.current += 1;
    resetConsoleState();
    sessionRef.current = next;
    setSession(next);
  };

  const isRequestCurrent = (generation: number, userId?: string) => generation === requestGeneration.current
    && (!userId || sessionRef.current?.user.id === userId);

  const currentMutationRequest = () => {
    const generation = sessionGeneration.current;
    const userId = sessionRef.current?.user.id;
    return () => generation === sessionGeneration.current && userId === sessionRef.current?.user.id;
  };

  const loadWorkspaces = async (generation: number, activeSession: AuthSession, page = workspacePageNumber, pageSize = 10) => {
    beginSource("workspaces");
    try {
      const result = await getWorkspaces(page, pageSize);
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      if (result.available && (result.data.page !== page || result.data.pageSize !== pageSize)) throw new Error("workspace_page_mismatch");
      updateSource("workspaces", { value: result, loading: false, error: "" });
      if (result.available) setWorkspacePageNumber(result.data.page);
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("workspaces", error, unavailableSource("control-plane"));
    }
  };

  const loadWallet = async (generation: number, activeSession: AuthSession) => {
    beginSource("wallet");
    try {
      const result = await getGatewayWallet();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("wallet", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("wallet", error, unavailableSource("sub2api"));
    }
  };

  const loadAccountUsage = async (generation: number, activeSession: AuthSession) => {
    beginSource("accountUsage");
    try {
      const result = await getGatewayAccountUsageSummary("month");
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("accountUsage", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("accountUsage", error, unavailableSource("sub2api"));
    }
  };

  const loadBalanceHistory = async (generation: number, activeSession: AuthSession, page = balanceHistoryPage) => {
    beginSource("balanceHistory");
    try {
      const result = await getGatewayBalanceHistory(page, 20);
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      updateSource("balanceHistory", { value: result, loading: false, error: "" });
      if (result.available) setBalanceHistoryPage(result.data.page);
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("balanceHistory", error, unavailableSource("sub2api"));
    }
  };

  const loadReceipts = async (generation: number, activeSession: AuthSession, limit = 20, cursor = receiptCursorRef.current) => {
    const receiptGeneration = ++receiptRequestGeneration.current;
    clearReceiptDetail();
    beginSource("receipts");
    try {
      const result = await getBillingReceipts(cursor, limit);
      if (isRequestCurrent(generation, activeSession.user.id) && receiptGeneration === receiptRequestGeneration.current && cursor === receiptCursorRef.current) {
        updateSource("receipts", { value: result, loading: false, error: "" });
      }
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id) && receiptGeneration === receiptRequestGeneration.current && cursor === receiptCursorRef.current) {
        failSource("receipts", error, unavailableSource("ledger"));
      }
    }
  };

  const loadAnnouncements = async (generation: number, activeSession: AuthSession, pageSize = 20) => {
    beginSource("announcements");
    try {
      const result = await getAnnouncements(1, pageSize);
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("announcements", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("announcements", error, unavailableSource("control-plane"));
    }
  };

  const loadCatalog = async (generation: number, activeSession: AuthSession) => {
    beginSource("catalog");
    setPreviews({});
    try {
      const catalog = await getPricingCatalog();
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      updateSource("catalog", { value: catalog, loading: false, error: "" });
      if (catalog.resourceBillingMode === "none") setLaunchAutoRenew(false);
      if (!catalog.packages.some((plan) => plan.id === launchPlan && plan.available)) {
        const firstAvailablePlan = catalog.packages.find((plan) => plan.available);
        if (firstAvailablePlan) setLaunchPlan(firstAvailablePlan.id);
      }
      const entries = await Promise.all(catalog.packages.filter((plan) => plan.available).map(async (plan) => {
        const preview = await previewPricing({ resourceType: "workspace", packageId: plan.id }, activeSession.csrfToken);
        return [plan.id, preview] as const;
      }));
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      const next: Partial<Record<PlanId, WorkspacePricePreview>> = {};
      for (const [planId, preview] of entries) {
        if (typeof preview.totalChargeUsdMicros === "number") next[planId] = preview as WorkspacePricePreview;
      }
      setPreviews(next);
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("catalog", error, null);
    }
  };

  const loadWorkspaceDetail = async (generation: number, activeSession: AuthSession, workspaceId: string) => {
    const workspaceDetailGeneration = ++workspaceDetailRequestGeneration.current;
    const readStillCurrent = () => isRequestCurrent(generation, activeSession.user.id)
      && workspaceDetailGeneration === workspaceDetailRequestGeneration.current
      && workspaceIdFromPath(window.location.pathname) === workspaceId;
    beginSource("workspaceDetail");
    updateSource("runtime", { value: null, loading: false, error: "" });
    updateSource("workspaceBudget", { value: null, loading: false, error: "" });
    let workspaceKeyId = "";
    try {
      const detail = await findWorkspaceInPages(workspaceId);
      if (!readStillCurrent()) return;
      updateSource("workspaceDetail", { value: detail, loading: false, error: "" });
      if (!detail.available || detail.data === null) {
        updateSource("runtime", { value: unavailableSource("fabric"), loading: false, error: "" });
        updateSource("workspaceBudget", { value: unavailableSource("sub2api"), loading: false, error: "" });
        return;
      }
      const renewalIntent = workspaceRenewalIntents.current.get(workspaceId);
      if (renewalIntent?.autoRenew === detail.data.autoRenew) {
        workspaceRenewalIntents.current.delete(workspaceId);
      }
      workspaceKeyId = detail.data.workspaceApiKeyId || "";
    } catch (error) {
      if (!readStillCurrent()) return;
      failSource("workspaceDetail", error, unavailableSource("control-plane"));
      updateSource("runtime", { value: unavailableSource("fabric"), loading: false, error: "" });
      updateSource("workspaceBudget", { value: unavailableSource("sub2api"), loading: false, error: "" });
      return;
    }
    beginSource("runtime");
    beginSource("workspaceBudget");
    const [runtimeResult, budgetResult] = await Promise.allSettled([
      getWorkspaceRuntimeStatus(workspaceId),
      getWorkspaceGatewayBudget(workspaceId, workspaceKeyId)
    ]);
    if (!readStillCurrent()) return;
    if (runtimeResult.status === "fulfilled") {
      updateSource("runtime", { value: runtimeResult.value, loading: false, error: "" });
    } else failSource("runtime", runtimeResult.reason, unavailableSource("fabric"));
    if (budgetResult.status === "fulfilled") {
      updateSource("workspaceBudget", { value: budgetResult.value, loading: false, error: "" });
    } else failSource("workspaceBudget", budgetResult.reason, unavailableSource("sub2api"));
  };

  const loadUsage = async (generation: number, activeSession: AuthSession, keyId: string, page = 1, period: GatewayUsagePeriod = usagePeriod) => {
    if (!keyId) return;
    const usageGeneration = ++usageRequestGeneration.current;
    beginSource("usage");
    beginSource("usageSummary");
    const [usageResult, summaryResult] = await Promise.allSettled([
      getGatewayKeyUsage(keyId, page, 20, period),
      getGatewayKeyUsageSummary(keyId, period)
    ]);
    if (!isRequestCurrent(generation, activeSession.user.id)
      || usageGeneration !== usageRequestGeneration.current
      || selectedUsageKeyIdRef.current !== keyId) return;
    if (usageResult.status === "fulfilled") {
      updateSource("usage", { value: usageResult.value, loading: false, error: "" });
      setUsagePage(page);
    } else failSource("usage", usageResult.reason, unavailableSource("sub2api"));
    if (summaryResult.status === "fulfilled") {
      updateSource("usageSummary", { value: summaryResult.value, loading: false, error: "" });
    } else failSource("usageSummary", summaryResult.reason, unavailableSource("sub2api"));
  };

  const loadUsageKeys = async (generation: number, activeSession: AuthSession) => {
    beginSource("usageKeys");
    try {
      const keys = await getGatewayKeys({ page: 1, pageSize: 20 });
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      updateSource("usageKeys", { value: keys, loading: false, error: "" });
      if (keys.available && keys.data.items.length === 0) {
        usageRequestGeneration.current += 1;
        selectedUsageKeyIdRef.current = "";
        setSelectedUsageKeyId("");
        setUsagePage(1);
        updateSource("usage", { value: null, loading: false, error: "" });
        updateSource("usageSummary", { value: null, loading: false, error: "" });
        return;
      }
      if (!keys.available) return;
      const keyId = keys.data.items.some((key) => key.id === selectedUsageKeyIdRef.current) ? selectedUsageKeyIdRef.current : keys.data.items[0].id;
      selectedUsageKeyIdRef.current = keyId;
      setSelectedUsageKeyId(keyId);
      await loadUsage(generation, activeSession, keyId, 1, usagePeriod);
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("usageKeys", error, unavailableSource("sub2api"));
    }
  };

  const loadEndpoint = async (generation: number, activeSession: AuthSession) => {
    beginSource("endpoint");
    try {
      const endpoint = await getGatewayEndpoint();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("endpoint", { value: endpoint, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("endpoint", error, unavailableSource("sub2api"));
    }
  };

  const recoverWorkspaceLaunch = async (generation: number, activeSession: AuthSession) => {
    setLaunchPollIssue("");
    try {
      const launches = await getWorkspaceLaunches();
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      const pending = launches.filter((operation) => !isTerminalWorkspaceLaunch(operation.status));
      if (pending.length === 0) {
        setLaunchOperation(null);
        return;
      }
      if (pending.length !== 1) {
        setLaunchPollIssue("error");
        return;
      }
      setLaunchOperation(pending[0]);
      if (pending[0].status !== "manual_review") void pollWorkspaceLaunch(pending[0].operationId, generation, activeSession);
    } catch {
      if (isRequestCurrent(generation, activeSession.user.id)) setLaunchPollIssue("error");
    }
  };

  const confirmWorkspaceLaunchReadback = async (workspaceId: string, generation: number, activeSession: AuthSession) => {
    try {
      const detail = await findWorkspaceInPages(workspaceId);
      if (!isRequestCurrent(generation, activeSession.user.id)) return false;
      if (!detail.available || detail.data === null) {
        setLaunchPollIssue("readback");
        flash("开通操作已完成，但 Workspace 权威回读尚未确认", "danger");
        return false;
      }
      setLaunchPollIssue("");
      flash("Workspace 已开通");
      navigate(`/console/workspaces/${encodeURIComponent(workspaceId)}`);
      return true;
    } catch {
      if (isRequestCurrent(generation, activeSession.user.id)) {
        setLaunchPollIssue("readback");
        flash("开通操作已完成，但 Workspace 权威回读尚未确认", "danger");
      }
      return false;
    }
  };

  const pollWorkspaceLaunch = async (operationId: string, generation: number, activeSession: AuthSession) => {
    setLaunchPollIssue("");
    for (let attempt = 0; attempt < workspaceLaunchPollAttempts; attempt += 1) {
      await new Promise<void>((resolve) => window.setTimeout(resolve, workspaceLaunchPollIntervalMs));
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      try {
        const operation = await getWorkspaceLaunch(operationId);
        if (!isRequestCurrent(generation, activeSession.user.id)) return;
        setLaunchOperation(operation);
        if (operation.status === "manual_review") return;
        if (isTerminalWorkspaceLaunch(operation.status)) {
          if (operation.status === "succeeded" && operation.workspaceId) {
            await confirmWorkspaceLaunchReadback(operation.workspaceId, generation, activeSession);
          } else if (operation.status === "refunded") {
            flash("Workspace 未完成，已退款", "danger");
          }
          return;
        }
      } catch (error) {
        if (isRequestCurrent(generation, activeSession.user.id)) {
          setLaunchPollIssue("error");
          flash(friendlyError(error), "danger");
        }
        return;
      }
    }
    if (isRequestCurrent(generation, activeSession.user.id)) setLaunchPollIssue("timeout");
  };

  const loadOperatorOverview = async (generation: number, activeSession: AuthSession) => {
    beginSource("operatorOverview");
    try {
      const result = await getOperatorOverview();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("operatorOverview", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorOverview", error, unavailableSource("control-plane"));
    }
  };

  const loadOperatorAccounts = async (generation: number, activeSession: AuthSession, page = operatorAccountPage) => {
    beginSource("operatorAccounts");
    try {
      const result = await getOperatorAccountsPage(page, operatorPageSize);
      if (isRequestCurrent(generation, activeSession.user.id)) {
        updateSource("operatorAccounts", { value: result, loading: false, error: "" });
        setOperatorAccountPage(page);
      }
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorAccounts", error, unavailableSource("control-plane+sub2api"));
    }
  };

  const findOperatorAccountByEmail = async (email: string, accountId: string, requestStillCurrent: () => boolean) => {
    const normalizedEmail = email.trim().toLowerCase();
    for (let page = 1; ; page += 1) {
      const result = await getOperatorAccountsPage(page, 50);
      if (!requestStillCurrent()) return null;
      if (!result.available) return null;
      if (result.data.page !== page || result.data.pageSize !== 50) throw new Error("operator_account_page_mismatch");
      const authoritativeAccount = result.data.items.find((account) => account.accountId === accountId
        && account.email.trim().toLowerCase() === normalizedEmail
        && Boolean(account.consoleUserId)
        && account.gatewayIdentity.available
        && account.gatewayIdentity.data.userId === account.sub2apiUserId
        && account.gatewayIdentity.data.email.trim().toLowerCase() === normalizedEmail);
      if (authoritativeAccount) {
        updateSource("operatorAccounts", { value: result, loading: false, error: "" });
        setOperatorAccountPage(page);
        return authoritativeAccount;
      }
      const pages = Math.max(1, Math.ceil(result.data.total / result.data.pageSize));
      if (page >= pages) return null;
    }
  };

  const loadOperatorWorkspaces = async (generation: number, activeSession: AuthSession, page = operatorWorkspacePage) => {
    beginSource("operatorWorkspaces");
    try {
      const result = await getOperatorWorkspaces(page, operatorPageSize);
      if (isRequestCurrent(generation, activeSession.user.id)) {
        updateSource("operatorWorkspaces", { value: result, loading: false, error: "" });
        setOperatorWorkspacePage(page);
      }
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorWorkspaces", error, unavailableSource("control-plane+fabric+sub2api"));
    }
  };

  const loadOperatorReconciliation = async (generation: number, activeSession: AuthSession) => {
    beginSource("operatorReconciliation");
    try {
      const result = await getOperatorReconciliation();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("operatorReconciliation", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorReconciliation", error, unavailableSource("control-plane"));
    }
  };

  const loadOperatorHealth = async (generation: number, activeSession: AuthSession) => {
    beginSource("operatorHealth");
    try {
      const result = await getOperatorHealth();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("operatorHealth", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorHealth", error, unavailableSource("control-plane"));
    }
  };

  const loadOperatorAnnouncements = async (generation: number, activeSession: AuthSession) => {
    beginSource("operatorAnnouncements");
    try {
      const result = await getOperatorAnnouncements();
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("operatorAnnouncements", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("operatorAnnouncements", error, unavailableSource("control-plane"));
    }
  };

  const loadRoute = async (generation: number, activeSession: AuthSession, routePath: string) => {
    if (routePath === "/console" || routePath === "/console/overview") {
      receiptCursorRef.current = "";
      setReceiptCursor("");
      setReceiptCursorStack([]);
      await Promise.all([loadWorkspaces(generation, activeSession, 1, 1), loadWallet(generation, activeSession), loadAccountUsage(generation, activeSession), loadReceipts(generation, activeSession, 3, ""), loadAnnouncements(generation, activeSession, 3)]);
      return;
    }
    if (routePath === "/console/workspaces") {
      await Promise.all([loadWorkspaces(generation, activeSession, workspacePageNumber, 10), recoverWorkspaceLaunch(generation, activeSession)]);
      return;
    }
    if (routePath === "/console/workspaces/new") {
      await Promise.all([loadWallet(generation, activeSession), loadCatalog(generation, activeSession), recoverWorkspaceLaunch(generation, activeSession)]);
      return;
    }
    if (workspacePage(routePath) === "detail") {
      await loadWorkspaceDetail(generation, activeSession, workspaceIdFromPath(routePath));
      return;
    }
    if (routePath === "/console/api") {
      await Promise.all([loadWallet(generation, activeSession), loadAccountUsage(generation, activeSession), loadBalanceHistory(generation, activeSession, balanceHistoryPage), loadEndpoint(generation, activeSession)]);
      return;
    }
    if (routePath === "/console/api/usage") {
      await Promise.all([loadUsageKeys(generation, activeSession), loadEndpoint(generation, activeSession)]);
      return;
    }
    if (routePath === "/console/billing") {
      await Promise.all([loadWorkspaces(generation, activeSession, 1, 10), loadReceipts(generation, activeSession, 20, receiptCursorRef.current)]);
      return;
    }
    if (routePath === "/console/announcements") {
      await loadAnnouncements(generation, activeSession);
      return;
    }
    if (routePath === "/admin" || routePath === "/admin/overview") {
      await Promise.all([loadOperatorOverview(generation, activeSession), loadOperatorAnnouncements(generation, activeSession)]);
      return;
    }
    if (routePath === "/admin/accounts") {
      await loadOperatorAccounts(generation, activeSession);
      return;
    }
    if (routePath === "/admin/billing") {
      await loadOperatorReconciliation(generation, activeSession);
      return;
    }
    if (routePath === "/admin/resources") {
      await loadOperatorWorkspaces(generation, activeSession);
      return;
    }
    if (routePath === "/admin/announcements") {
      await loadOperatorAnnouncements(generation, activeSession);
      return;
    }
    if (routePath === "/admin/system") await loadOperatorHealth(generation, activeSession);
  };

  useEffect(() => {
    if (path !== "/login") invalidateLoginAttempt();
    const generation = ++requestGeneration.current;
    clearSecrets();
    setWorkspaceDeleteIssue("");
    setSidebarOpen(false);
    setGlobalSlide("");
    if (logoutState.current !== "idle") return;
    if (!needsSession(path)) {
      setAuthStatus("public");
      setAuthError("");
      return;
    }
    setAuthStatus("checking");
    setAuthError("");
    const run = async () => {
      let activeSession = session;
      try {
        if (!activeSession) {
          activeSession = await currentSession();
          if (generation !== requestGeneration.current) return;
          if (!activeSession) {
            navigate(`/login?redirect=${encodeURIComponent(window.location.pathname + window.location.search)}`);
            return;
          }
          sessionRef.current = activeSession;
          setSession(activeSession);
        }
        if (path.startsWith("/admin") && activeSession.isOperator !== true) {
          navigate("/403");
          return;
        }
        setAuthStatus("ready");
        if (isKnownConsoleRoute(path)) await loadRoute(generation, activeSession, path);
      } catch (error) {
        if (generation === requestGeneration.current) {
          setAuthStatus("error");
          setAuthError(friendlyError(error));
        }
      }
    };
    void run();
    return () => {
      if (path === "/login") invalidateLoginAttempt();
    };
  }, [path]);

  useEffect(() => () => {
    invalidateLoginAttempt();
    logoutAttemptGeneration.current += 1;
    logoutInFlight.current = false;
    requestGeneration.current += 1;
    sessionGeneration.current += 1;
    clearSecrets();
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
  }, []);

  const submitLogin = async (email: string, password: string) => {
    invalidateLoginAttempt();
    const attempt = loginAttemptGeneration.current;
    const abortController = new AbortController();
    loginAbortController.current = abortController;
    setAuthError("");
    setAuthStatus("checking");
    try {
      const next = await loginRequest({ email, password }, abortController.signal);
      if (attempt !== loginAttemptGeneration.current || abortController.signal.aborted || logoutState.current !== "idle" || path !== "/login") return;
      replaceSession(next);
      setAuthStatus("ready");
      const requested = new URLSearchParams(window.location.search).get("redirect");
      const allowed = requested?.startsWith("/console") || (next.isOperator && requested?.startsWith("/admin"));
      navigate(allowed && requested ? requested : defaultAuthenticatedRoute(next.isOperator));
    } catch (error) {
      if (attempt !== loginAttemptGeneration.current || abortController.signal.aborted) return;
      setAuthStatus("public");
      setAuthError(friendlyError(error));
    } finally {
      if (attempt === loginAttemptGeneration.current) loginAbortController.current = null;
    }
  };

  const signOut = async () => {
    const activeSession = sessionRef.current;
    if (!activeSession || logoutInFlight.current) return;
    invalidateLoginAttempt();
    logoutInFlight.current = true;
    const attempt = ++logoutAttemptGeneration.current;
    logoutState.current = "pending";
    requestGeneration.current += 1;
    sessionGeneration.current += 1;
    resetConsoleState();
    setAuthError("");
    setAuthStatus("logout_pending");

    const result = await logoutAndConfirm(activeSession.csrfToken);
    if (attempt !== logoutAttemptGeneration.current) return;
    logoutInFlight.current = false;
    if (result.state === "confirmed") {
      replaceSession(null);
      setAuthStatus("public");
      navigate("/", true);
      return;
    }

    logoutState.current = "unconfirmed";
    if (result.session) {
      sessionRef.current = result.session;
      setSession(result.session);
    }
    setAuthError(result.reason === "session_still_active"
      ? "服务器仍报告 Session 有效。受保护内容已隐藏，请重试退出。"
      : "无法确认服务器 Session 是否已撤销。受保护内容已隐藏，请重试退出。");
    setAuthStatus("logout_unconfirmed");
  };

  const refreshCurrentPage = async () => {
    if (!session) return;
    const generation = ++requestGeneration.current;
    clearSecrets();
    await loadRoute(generation, session, path);
  };

  const changeWorkspacePage = async (page: number) => {
    if (!session || page < 1) return;
    const generation = requestGeneration.current;
    clearSecrets();
    await loadWorkspaces(generation, session, page, 10);
  };

  const reviewWorkspaceLaunch = () => {
    if (!launchName.trim() || !selectedPlan || selectedPrice === null || balanceSufficient !== true) return;
    setLaunchConfirmed(false);
    setLaunchStep("confirm");
  };

  const submitWorkspaceLaunch = async () => {
    if (!session || commandBusy || launchStep !== "confirm" || !launchConfirmed || !selectedPlan || selectedPrice === null || balanceSufficient !== true || !launchName.trim()) return;
    const requestStillCurrent = currentMutationRequest();
    const input: WorkspaceLaunchRequest = { name: launchName.trim(), packageId: selectedPlan.id, autoRenew: customerOwned ? false : launchAutoRenew };
    if (workspaceLaunchIntent.current && !sameLaunchRequest(workspaceLaunchIntent.current.input, input)) {
      flash("上次 Workspace 开通结果待确认，请按原配置重试", "danger");
      return;
    }
    if (!workspaceLaunchIntent.current) {
      workspaceLaunchIntent.current = { input, idempotencyKey: workspaceLaunchIdempotencyKey() };
    }
    setCommandBusy(true);
    try {
      const operation = await launchWorkspace(input, session.csrfToken, workspaceLaunchIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return;
      workspaceLaunchIntent.current = null;
      setLaunchOperation(operation);
      if (operation.status === "succeeded" && operation.workspaceId) {
        await confirmWorkspaceLaunchReadback(operation.workspaceId, requestGeneration.current, session);
      } else if (operation.status === "refunded") {
        flash("Workspace 未完成，已退款", "danger");
      } else if (!isTerminalWorkspaceLaunch(operation.status) && operation.status !== "manual_review") {
        void pollWorkspaceLaunch(operation.operationId, requestGeneration.current, session);
      }
    } catch (error) {
      if (!requestStillCurrent()) return;
      const payload = error && typeof error === "object" && "payload" in error ? (error as { payload?: unknown }).payload : null;
      const unknown = Boolean(payload && typeof payload === "object" && (payload as { status?: string }).status === "unknown");
      if (!unknown) workspaceLaunchIntent.current = null;
      flash(friendlyError(error), "danger");
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const openLaunchedWorkspace = async () => {
    if (!session || !launchOperation?.workspaceId) return;
    await confirmWorkspaceLaunchReadback(launchOperation.workspaceId, requestGeneration.current, session);
  };

  const confirmWorkspaceDeleteReadback = async (workspaceId: string, requestStillCurrent: () => boolean) => {
    try {
      const readback = await findWorkspaceInPages(workspaceId);
      if (!requestStillCurrent()) return false;
      if (!readback.available || readback.data !== null) {
        setWorkspaceDeleteIssue("unconfirmed");
        flash("删除结果尚未获得权威回读确认", "danger");
        return false;
      }
      workspaceDeleteIntent.current = null;
      setWorkspaceDeleteIssue("");
      flash("Workspace 已删除");
      navigate("/console/workspaces");
      return true;
    } catch {
      if (requestStillCurrent()) {
        setWorkspaceDeleteIssue("unconfirmed");
        flash("删除结果尚未获得权威回读确认", "danger");
      }
      return false;
    }
  };

  const deleteCurrentWorkspace = async () => {
    if (!session || commandBusy) return;
    const detailSource = sources.workspaceDetail.value;
    const workspace = detailSource?.available ? detailSource.data : null;
    if (!workspace || !window.confirm(`确认删除 Workspace “${workspace.name || workspace.id}”？`)) return;
    const mutationStillCurrent = currentMutationRequest();
    const requestStillCurrent = () => mutationStillCurrent()
      && workspaceIdFromPath(window.location.pathname) === workspace.id;
    if (!workspaceDeleteIntent.current || workspaceDeleteIntent.current.workspaceId !== workspace.id) {
      workspaceDeleteIntent.current = {
        workspaceId: workspace.id,
        idempotencyKey: workspaceDeleteIdempotencyKey(workspace.id)
      };
    }
    setCommandBusy(true);
    setWorkspaceDeleteIssue("");
    try {
      const result = await deleteWorkspaceCommand(
        workspace.id,
        session.csrfToken,
        workspaceDeleteIntent.current.idempotencyKey
      );
      if (!requestStillCurrent()) return;
      if (!result.available) {
        workspaceDeleteIntent.current = null;
        setWorkspaceDeleteIssue("unavailable");
        flash("Workspace 删除暂不可用", "danger");
        return;
      }
      await confirmWorkspaceDeleteReadback(workspace.id, requestStillCurrent);
    } catch (error) {
      if (!requestStillCurrent()) return;
      if (apiErrorCode(error) === "workspace_not_found") {
        await confirmWorkspaceDeleteReadback(workspace.id, requestStillCurrent);
        return;
      }
      const status = error && typeof error === "object" && "status" in error
        ? Number((error as { status?: number }).status)
        : 0;
      if (status > 0 && status < 500) workspaceDeleteIntent.current = null;
      setWorkspaceDeleteIssue("unconfirmed");
      flash(friendlyError(error), "danger");
    } finally {
      if (mutationStillCurrent()) setCommandBusy(false);
    }
  };

  const updateCurrentWorkspaceRenewal = async (autoRenew: boolean) => {
    const detailSource = sources.workspaceDetail.value;
    const workspace = detailSource?.available ? detailSource.data : null;
    if (!session || !workspace || commandBusy || workspace.renewalStatus !== "active") return false;
    const requestStillCurrent = currentMutationRequest();
    const workspaceDetailGeneration = workspaceDetailRequestGeneration.current;
    const workspaceStillCurrent = () => workspaceDetailGeneration === workspaceDetailRequestGeneration.current
      && workspaceIdFromPath(window.location.pathname) === workspace.id;
    let intent = workspaceRenewalIntents.current.get(workspace.id);
    if (intent && intent.autoRenew !== autoRenew) {
      flash("上次自动续费更新结果待确认，请按原设置重试", "danger");
      return false;
    }
    if (!intent) {
      intent = {
        autoRenew,
        idempotencyKey: `workspace-renewal:${workspace.id}:${crypto.randomUUID()}`
      };
      workspaceRenewalIntents.current.set(workspace.id, intent);
    }
    setCommandBusy(true);
    try {
      const response = await updateWorkspaceRenewal(workspace.id, { autoRenew }, session.csrfToken, intent.idempotencyKey);
      if (!requestStillCurrent() || !workspaceStillCurrent()) return false;
      if (response.autoRenew !== autoRenew || !response.renewalStatus.trim()
        || [response.effectiveAfter, response.nextRenewalAt, response.paidThrough].some((value) => Number.isNaN(Date.parse(value)))) {
        throw new Error("workspace_renewal_response_mismatch");
      }
      const readback = await findWorkspaceInPages(workspace.id);
      if (!requestStillCurrent() || !workspaceStillCurrent()) return false;
      if (!readback.available || !readback.data || readback.data.id !== workspace.id
        || readback.data.autoRenew !== response.autoRenew || readback.data.paidThrough !== response.paidThrough
        || readback.data.nextRenewalAt !== response.nextRenewalAt) {
        throw new Error("workspace_renewal_readback_mismatch");
      }
      workspaceRenewalIntents.current.delete(workspace.id);
      setSources((current) => {
        const currentList = current.workspaces.value;
        const workspaces = currentList?.available
          ? {
              ...current.workspaces,
              value: {
                ...currentList,
                data: {
                  ...currentList.data,
                  items: currentList.data.items.map((item) => item.id === workspace.id ? readback.data : item)
                }
              }
            }
          : current.workspaces;
        return {
          ...current,
          workspaces,
          workspaceDetail: { value: readback, loading: false, error: "" }
        };
      });
      flash(autoRenew ? "自动续费已开启" : "自动续费已关闭");
      return true;
    } catch (error) {
      if (!requestStillCurrent() || !workspaceStillCurrent()) return false;
      const status = error && typeof error === "object" && "status" in error
        ? Number((error as { status?: number }).status)
        : 0;
      if (status > 0 && status < 500) workspaceRenewalIntents.current.delete(workspace.id);
      flash(mutationError(error), "danger");
      return false;
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const updateWorkspaceBudget = async (input: WorkspaceGatewayBudgetUpdateRequest) => {
    const workspace = sources.workspaceDetail.value?.available ? sources.workspaceDetail.value.data : null;
    const budget = sources.workspaceBudget.value?.available ? sources.workspaceBudget.value.data : null;
    if (!session || !workspace || !budget || budget.workspaceId !== workspace.id) return false;
    if (workspaceBudgetBusyClaim.current !== null) return false;
    const requestStillCurrent = currentMutationRequest();
    const workspaceDetailGeneration = workspaceDetailRequestGeneration.current;
    const workspaceStillCurrent = () => workspaceDetailGeneration === workspaceDetailRequestGeneration.current
      && workspaceIdFromPath(window.location.pathname) === workspace.id;
    const signature = workspaceBudgetRequestSignature(input);
    let intent = workspaceBudgetIntents.current.get(workspace.id);
    if (intent && intent.keyId !== budget.keyId) {
      workspaceBudgetIntents.current.delete(workspace.id);
      intent = undefined;
    }
    if (intent && intent.signature !== signature) {
      flash("上次模型预算更新结果待确认，请使用相同设置重试", "danger");
      return false;
    }
    if (!intent) {
      intent = {
        keyId: budget.keyId,
        signature,
        input: { ...input },
        idempotencyKey: `workspace-gateway-budget:${workspace.id}:${crypto.randomUUID()}`
      };
      workspaceBudgetIntents.current.set(workspace.id, intent);
    }
    const busyClaim = Symbol("workspace-budget");
    workspaceBudgetBusyClaim.current = busyClaim;
    setWorkspaceBudgetBusy(true);
    try {
      const result = await updateWorkspaceGatewayBudget(
        workspace.id,
        budget.keyId,
        intent.input,
        session.csrfToken,
        intent.idempotencyKey
      );
      if (!requestStillCurrent()) return false;
      if (!workspaceStillCurrent()) return false;
      if (!result.available || result.data.workspaceId !== workspace.id || result.data.keyId !== budget.keyId) {
        throw new Error("workspace_gateway_budget_identity_mismatch");
      }
      if (!workspaceBudgetResultMatchesInput(result.data, intent.input)) {
        if (workspaceBudgetIntents.current.get(workspace.id) === intent) {
          workspaceBudgetIntents.current.delete(workspace.id);
        }
        updateSource("workspaceBudget", { value: result, loading: false, error: "" });
        flash("模型预算已变化，请按最新状态重新提交", "danger");
        return false;
      }
      if (workspaceBudgetIntents.current.get(workspace.id) === intent) {
        workspaceBudgetIntents.current.delete(workspace.id);
      }
      updateSource("workspaceBudget", { value: result, loading: false, error: "" });
      flash("模型预算已更新");
      return true;
    } catch (error) {
      if (!requestStillCurrent()) return false;
      if (!workspaceStillCurrent()) return false;
      const status = error && typeof error === "object" && "status" in error
        ? Number((error as { status?: number }).status)
        : 0;
      if (status > 0 && status < 500 && workspaceBudgetIntents.current.get(workspace.id) === intent) {
        workspaceBudgetIntents.current.delete(workspace.id);
      }
      flash(mutationError(error), "danger");
      return false;
    } finally {
      if (workspaceBudgetBusyClaim.current === busyClaim && requestStillCurrent()) {
        workspaceBudgetBusyClaim.current = null;
        setWorkspaceBudgetBusy(false);
      }
    }
  };

  const revealWorkspacePassword = async () => {
    const workspace = sources.workspaceDetail.value?.available ? sources.workspaceDetail.value.data : null;
    if (!session || !workspace || workspaceSecretBusy) return;
    clearSecrets();
    const activeGeneration = ++secretRequestGeneration.current;
    setWorkspaceSecretBusy(true);
    try {
      const response = await revealWorkspaceCredentials(workspace.id, session.csrfToken);
      if (activeGeneration !== secretRequestGeneration.current || path !== window.location.pathname || workspace.id !== workspaceIdFromPath(window.location.pathname)) return;
      setSecrets({ apiKey: null, workspace: response.access });
      armSecretTimeout();
    } catch (error) {
      flash(friendlyError(error), "danger");
    } finally {
      if (activeGeneration === secretRequestGeneration.current) setWorkspaceSecretBusy(false);
    }
  };

  const revealWorkspaceKey = async () => {
    const workspace = sources.workspaceDetail.value?.available ? sources.workspaceDetail.value.data : null;
    if (!session || !workspace?.workspaceApiKeyId || gatewaySecretBusy) return;
    clearSecrets();
    const activeGeneration = ++secretRequestGeneration.current;
    setGatewaySecretBusy(true);
    try {
      const response = await revealGatewayKey(workspace.workspaceApiKeyId, session.csrfToken);
      if (activeGeneration !== secretRequestGeneration.current || path !== window.location.pathname) return;
      if (!response.available) throw new Error("gateway_key_unavailable");
      setSecrets({ apiKey: response.data, workspace: null });
      armSecretTimeout();
    } catch (error) {
      flash(friendlyError(error), "danger");
    } finally {
      if (activeGeneration === secretRequestGeneration.current) setGatewaySecretBusy(false);
    }
  };

  const rotateWorkspacePassword = async () => {
    const workspace = sources.workspaceDetail.value?.available ? sources.workspaceDetail.value.data : null;
    if (!session || !workspace || workspaceSecretBusy) return;
    const requestStillCurrent = currentMutationRequest();
    if (!runtimeRotationIntent.current || runtimeRotationIntent.current.workspaceId !== workspace.id) {
      runtimeRotationIntent.current = { workspaceId: workspace.id, idempotencyKey: `runtime-credential:${crypto.randomUUID()}` };
    }
    clearSecrets();
    const activeGeneration = ++secretRequestGeneration.current;
    setWorkspaceSecretBusy(true);
    try {
      const response = await rotateWorkspaceCredentials(workspace.id, session.csrfToken, runtimeRotationIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return;
      runtimeRotationIntent.current = null;
      if (activeGeneration !== secretRequestGeneration.current || workspace.id !== workspaceIdFromPath(window.location.pathname)) return;
      setSecrets({ apiKey: null, workspace: response.access });
      armSecretTimeout();
      flash("Workspace 凭证已轮换");
      await loadWorkspaceDetail(requestGeneration.current, session, workspace.id);
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    } finally {
      if (requestStillCurrent() && activeGeneration === secretRequestGeneration.current) setWorkspaceSecretBusy(false);
    }
  };

  const copyText = async (value: string | undefined, message: string) => {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
      flash(message);
    } catch {
      flash("复制失败，请重试", "danger");
    }
  };

  const loadSupportTickets = async () => {
    if (!session) return;
    const generation = requestGeneration.current;
    const userId = session.user.id;
    setSupportLoading(true);
    setSupportError("");
    try {
      const result = await getSupportTickets();
      if (!isRequestCurrent(generation, userId)) return;
      setSupportTickets(result);
    } catch (error) {
      if (!isRequestCurrent(generation, userId)) return;
      setSupportTickets(null);
      setSupportError(friendlyError(error));
    } finally {
      if (isRequestCurrent(generation, userId)) setSupportLoading(false);
    }
  };

  const createSupportMapping = async (input: Omit<CreateSupportTicketMappingRequest, "accountId">) => {
    if (!session || commandBusy) return false;
    const requestStillCurrent = currentMutationRequest();
    const request: CreateSupportTicketMappingRequest = { ...input, accountId: session.user.accountId };
    if (!supportCreateIntent.current || JSON.stringify(supportCreateIntent.current.input) !== JSON.stringify(request)) {
      supportCreateIntent.current = { input: request, idempotencyKey: `support-map:${crypto.randomUUID()}` };
    }
    setCommandBusy(true);
    try {
      await createSupportTicketMapping(supportCreateIntent.current.input, session.csrfToken, supportCreateIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return false;
      supportCreateIntent.current = null;
      await loadSupportTickets();
      if (!requestStillCurrent()) return false;
      flash("外部工单映射已保存");
      return true;
    } catch (error) {
      if (!requestStillCurrent()) return false;
      flash(mutationError(error), "danger");
      return false;
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const selectReceipt = async (receiptId: string) => {
    if (!session) return;
    clearReceiptDetail();
    const detailGeneration = ++receiptDetailRequestGeneration.current;
    const generation = requestGeneration.current;
    const userId = session.user.id;
    selectedReceiptIdRef.current = receiptId;
    setSelectedReceiptId(receiptId);
    beginSource("receiptDetail");
    try {
      const result = await getBillingReceipt(receiptId);
      if (!isRequestCurrent(generation, userId)
        || detailGeneration !== receiptDetailRequestGeneration.current
        || selectedReceiptIdRef.current !== receiptId) return;
      if (result.available && result.data.receiptId !== receiptId) throw new Error("billing_receipt_identity_mismatch");
      updateSource("receiptDetail", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, userId)
        && detailGeneration === receiptDetailRequestGeneration.current
        && selectedReceiptIdRef.current === receiptId) {
        failSource("receiptDetail", error, unavailableSource("ledger"));
      }
    }
  };

  const nextReceiptPage = async () => {
    const page = sources.receipts.value?.available ? sources.receipts.value.data : null;
    if (!session || !page?.hasMore || !page.nextCursor) return;
    const nextCursor = page.nextCursor;
    setReceiptCursorStack((current) => [...current, receiptCursorRef.current]);
    receiptCursorRef.current = nextCursor;
    setReceiptCursor(nextCursor);
    await loadReceipts(requestGeneration.current, session, 20, nextCursor);
  };

  const previousReceiptPage = async () => {
    if (!session || receiptCursorStack.length === 0) return;
    const previousCursor = receiptCursorStack[receiptCursorStack.length - 1] || "";
    setReceiptCursorStack((current) => current.slice(0, -1));
    receiptCursorRef.current = previousCursor;
    setReceiptCursor(previousCursor);
    await loadReceipts(requestGeneration.current, session, 20, previousCursor);
  };

  const markRead = async (announcementId: string) => {
    if (!session || announcementBusy) return;
    const requestStillCurrent = currentMutationRequest();
    setAnnouncementBusy(announcementId);
    try {
      await markAnnouncementRead(announcementId, session.csrfToken, `announcement-read:${crypto.randomUUID()}`);
      if (!requestStillCurrent()) return;
      await loadAnnouncements(requestGeneration.current, session, path === "/console/overview" ? 3 : 20);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(friendlyError(error), "danger");
    } finally {
      if (requestStillCurrent()) setAnnouncementBusy("");
    }
  };

  const chooseUsageKey = async (keyId: string) => {
    if (!session) return;
    clearSecrets();
    selectedUsageKeyIdRef.current = keyId;
    setSelectedUsageKeyId(keyId);
    await loadUsage(requestGeneration.current, session, keyId, 1, usagePeriod);
  };

  const chooseUsagePeriod = async (period: GatewayUsagePeriod) => {
    if (!session || !selectedUsageKeyId) return;
    setUsagePeriod(period);
    await loadUsage(requestGeneration.current, session, selectedUsageKeyId, 1, period);
  };

  const changeUsagePage = async (page: number) => {
    if (!session || !selectedUsageKeyId || page < 1) return;
    await loadUsage(requestGeneration.current, session, selectedUsageKeyId, page, usagePeriod);
  };

  const changeBalancePage = async (page: number) => {
    if (!session || page < 1) return;
    await loadBalanceHistory(requestGeneration.current, session, page);
  };

  const openOperatorWorkspace = async (workspaceId: string) => {
    if (!session) return;
    selectedOperatorWorkspaceIdRef.current = workspaceId;
    setSelectedOperatorWorkspaceId(workspaceId);
    beginSource("operatorWorkspaceDetail");
    try {
      const result = await getOperatorWorkspace(workspaceId);
      if (selectedOperatorWorkspaceIdRef.current !== workspaceId) return;
      updateSource("operatorWorkspaceDetail", { value: result, loading: false, error: "" });
    } catch (error) {
      if (selectedOperatorWorkspaceIdRef.current === workspaceId) failSource("operatorWorkspaceDetail", error, unavailableSource("control-plane+fabric+ledger"));
    }
  };

  const changeOperatorAccountPage = async (page: number) => {
    if (!session || page < 1) return;
    await loadOperatorAccounts(requestGeneration.current, session, page);
  };

  const changeOperatorWorkspacePage = async (page: number) => {
    if (!session || page < 1) return;
    await loadOperatorWorkspaces(requestGeneration.current, session, page);
  };

  const disableOperatorAccount = async (accountId: string) => {
    if (!session || !window.confirm("确认停用该客户？账号会立即停用；历史账单、收据和审计记录会保留。")) return;
    const requestStillCurrent = currentMutationRequest();
    const idempotencyKey = operatorDisableIntents.current.get(accountId) || `account-disable:${accountId}:${crypto.randomUUID()}`;
    operatorDisableIntents.current.set(accountId, idempotencyKey);
    try {
      await disableOperatorAccountCommand(accountId, "operator_requested", session.csrfToken, idempotencyKey);
      if (!requestStillCurrent()) return;
      operatorDisableIntents.current.delete(accountId);
      flash("客户已停用");
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    }
  };

  const setOperatorWorkspacePurchaseEligibility = async (accountId: string, enabled: boolean) => {
    const confirmation = enabled
      ? "确认授予该账户新购 Workspace 的资格？"
      : "确认撤销该账户新购 Workspace 的资格？已有 Workspace 不会受影响。";
    if (!session || !window.confirm(confirmation)) return;
    const requestStillCurrent = currentMutationRequest();
    const current = operatorWorkspaceEligibilityIntents.current.get(accountId);
    const intent = current?.enabled === enabled
      ? current
      : { enabled, idempotencyKey: `workspace-purchase-eligibility:${accountId}:${crypto.randomUUID()}` };
    operatorWorkspaceEligibilityIntents.current.set(accountId, intent);
    try {
      await setOperatorWorkspacePurchaseEligibilityCommand(accountId, enabled, "operator_requested", session.csrfToken, intent.idempotencyKey);
      if (!requestStillCurrent()) return;
      operatorWorkspaceEligibilityIntents.current.delete(accountId);
      flash(enabled ? "已授予 Workspace 新购资格" : "已撤销 Workspace 新购资格");
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    }
  };

  const submitWalletAdjustment = async (accountId: string, input: WalletAdjustmentRequest) => {
    if (!session || commandBusy || input.confirmationAccountId !== accountId || !input.amountUsd || !input.reason.trim()) return;
    if (!window.confirm("请再次确认这笔余额操作：提交后会写入客户账户并保留操作记录。")) return;
    const requestStillCurrent = currentMutationRequest();
    if (!walletAdjustmentIntent.current || walletAdjustmentIntent.current.accountId !== accountId || JSON.stringify(walletAdjustmentIntent.current.input) !== JSON.stringify(input)) {
      walletAdjustmentIntent.current = { accountId, input, idempotencyKey: `wallet-adjustment:${crypto.randomUUID()}` };
    }
    setCommandBusy(true);
    try {
      const result = await createWalletAdjustment(accountId, walletAdjustmentIntent.current.input, session.csrfToken, walletAdjustmentIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return null;
      setWalletAdjustmentOperation(result);
      if (result.status === "manual_review") flash("结果待确认，已进入人工复核", "danger");
      else {
        walletAdjustmentIntent.current = null;
        flash("余额操作已提交");
      }
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      if (!requestStillCurrent()) return null;
      return result;
    } catch (error) {
      if (!requestStillCurrent()) return null;
      flash(mutationError(error), "danger");
      return null;
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const refreshWalletOperation = async () => {
    if (!session || !walletAdjustmentOperation?.operationId) return;
    const requestStillCurrent = currentMutationRequest();
    try {
      const result = await getWalletAdjustment(walletAdjustmentOperation.operationId);
      if (!requestStillCurrent()) return;
      setWalletAdjustmentOperation(result);
      if (result.status === "succeeded") walletAdjustmentIntent.current = null;
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(friendlyError(error), "danger");
    }
  };

  const recoverWalletOperation = async () => {
    const operation = walletAdjustmentOperation;
    if (!session || !operation || operation.status !== "manual_review" || !operation.allowedActions?.includes("recover_wallet_adjustment")) return;
    const requestStillCurrent = currentMutationRequest();
    if (!walletAdjustmentRecoveryIntent.current || walletAdjustmentRecoveryIntent.current.operationId !== operation.operationId) {
      const evidenceRef = (window.prompt("请输入 case-YYYYMMDD-xxx 证据引用") || "").trim();
      if (!evidenceRef) return;
      walletAdjustmentRecoveryIntent.current = {
        operationId: operation.operationId,
        input: { accountId: operation.accountId, evidenceRef },
        idempotencyKey: walletRecoveryIdempotencyKey(operation.operationId)
      };
    }
    setCommandBusy(true);
    try {
      const result = await recoverWalletAdjustment(operation.operationId, walletAdjustmentRecoveryIntent.current.input, session.csrfToken, walletAdjustmentRecoveryIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return;
      setWalletAdjustmentOperation(result);
      if (result.status === "succeeded") {
        walletAdjustmentRecoveryIntent.current = null;
        walletAdjustmentIntent.current = null;
        flash("余额操作已确认");
      } else flash("恢复结果仍待人工确认", "danger");
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const provisionAccount = async (input: ProvisionAccountRequest) => {
    if (!session || commandBusy) return false;
    const requestStillCurrent = currentMutationRequest();
    if (!operatorProvisionIntent.current || JSON.stringify(operatorProvisionIntent.current.input) !== JSON.stringify(input)) {
      operatorProvisionIntent.current = { input, idempotencyKey: `account-provision:${crypto.randomUUID()}` };
    }
    setCommandBusy(true);
    try {
      const result = await provisionOperatorAccount(operatorProvisionIntent.current.input, session.csrfToken, operatorProvisionIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return null;
      setOperatorProvisionOperation(result);
      const authoritativeAccount = await findOperatorAccountByEmail(input.email, result.accountId, requestStillCurrent);
      if (!requestStillCurrent()) return null;
      if (!authoritativeAccount) {
        flash("开户命令已返回，但账户映射读回暂不可用，请重试", "danger");
        return { operation: result, account: null as OperatorAccountDTO | null };
      }
      operatorProvisionIntent.current = null;
      flash("用户已开通");
      return { operation: result, account: authoritativeAccount };
    } catch (error) {
      if (!requestStillCurrent()) return null;
      flash(mutationError(error), "danger");
      return null;
    } finally {
      if (requestStillCurrent()) setCommandBusy(false);
    }
  };

  const createAnnouncement = async (input: AnnouncementDraftRequest) => {
    if (!session) return false;
    const requestStillCurrent = currentMutationRequest();
    if (!announcementCreateIntent.current || JSON.stringify(announcementCreateIntent.current.input) !== JSON.stringify(input)) {
      announcementCreateIntent.current = { input, idempotencyKey: `announcement-create:${crypto.randomUUID()}` };
    }
    try {
      await createOperatorAnnouncement(announcementCreateIntent.current.input, session.csrfToken, announcementCreateIntent.current.idempotencyKey);
      if (!requestStillCurrent()) return false;
      announcementCreateIntent.current = null;
      flash("公告草稿已创建");
      await loadOperatorAnnouncements(requestGeneration.current, session);
      if (!requestStillCurrent()) return false;
      return true;
    } catch (error) {
      if (!requestStillCurrent()) return false;
      flash(mutationError(error), "danger");
      return false;
    }
  };

  const publishAnnouncement = async (announcementId: string) => {
    if (!session || !window.confirm("确认发布公告？")) return;
    const requestStillCurrent = currentMutationRequest();
    const announcement = sources.operatorAnnouncements.value?.available
      ? sources.operatorAnnouncements.value.data.items.find((item) => item.id === announcementId)
      : null;
    if (!announcement) return;
    let intent = announcementPublishIntents.current.get(announcementId);
    if (!intent) {
      intent = { input: { startsAt: announcement.startsAt || new Date().toISOString(), endsAt: announcement.endsAt || "" }, idempotencyKey: `announcement-publish:${announcementId}:${crypto.randomUUID()}` };
      announcementPublishIntents.current.set(announcementId, intent);
    }
    try {
      await publishOperatorAnnouncement(announcementId, intent.input, session.csrfToken, intent.idempotencyKey);
      if (!requestStillCurrent()) return;
      announcementPublishIntents.current.delete(announcementId);
      flash("公告已发布");
      await loadOperatorAnnouncements(requestGeneration.current, session);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    }
  };

  const withdrawAnnouncement = async (announcementId: string) => {
    if (!session || !window.confirm("确认撤下公告？")) return;
    const requestStillCurrent = currentMutationRequest();
    const idempotencyKey = announcementWithdrawIntents.current.get(announcementId) || `announcement-withdraw:${announcementId}:${crypto.randomUUID()}`;
    announcementWithdrawIntents.current.set(announcementId, idempotencyKey);
    try {
      await withdrawOperatorAnnouncement(announcementId, session.csrfToken, idempotencyKey);
      if (!requestStillCurrent()) return;
      announcementWithdrawIntents.current.delete(announcementId);
      flash("公告已撤下");
      await loadOperatorAnnouncements(requestGeneration.current, session);
      if (!requestStillCurrent()) return;
    } catch (error) {
      if (!requestStillCurrent()) return;
      flash(mutationError(error), "danger");
    }
  };

  const selectedPlan = sources.catalog.value?.packages.find((plan) => plan.id === launchPlan && plan.available) || null;
  const customerOwned = sources.catalog.value?.resourceBillingMode === "none";
  const selectedPrice = selectedPlan ? (customerOwned ? 0 : previews[selectedPlan.id]?.totalChargeUsdMicros ?? null) : null;
  const wallet = sources.wallet.value?.available ? sources.wallet.value.data : null;
  const balanceSufficient = customerOwned ? true : wallet && selectedPrice !== null
    ? hasSufficientWorkspaceLaunchBalance(wallet.usdMicros, selectedPrice)
    : wallet ? false : null;
  const workspaceRows = sources.workspaces.value?.available ? sources.workspaces.value.data.items : [];
  const workspacePages = sources.workspaces.value?.available ? Math.ceil(sources.workspaces.value.data.total / sources.workspaces.value.data.pageSize) : 0;
  const operatorAccountPages = sources.operatorAccounts.value?.available ? Math.ceil(sources.operatorAccounts.value.data.total / sources.operatorAccounts.value.data.pageSize) : 0;
  const operatorWorkspacePages = sources.operatorWorkspaces.value?.available ? Math.ceil(sources.operatorWorkspaces.value.data.total / sources.operatorWorkspaces.value.data.pageSize) : 0;
  const isAdminRoute = path === "/admin" || path.startsWith("/admin/");
  const isKnownRoute = isKnownConsoleRoute(path);

  const pageTitle = useMemo(() => {
    if (path === "/console" || path === "/console/overview") return "概览";
    if (path.startsWith("/console/workspaces")) return "Workspace";
    if (path.startsWith("/console/api")) return "API 服务";
    if (path === "/console/billing") return "账单";
    if (path === "/console/announcements") return "公告";
    if (path === "/admin" || path === "/admin/overview") return "运维概览";
    if (path === "/admin/accounts") return "客户与计费账户";
    if (path === "/admin/billing") return "计费复核";
    if (path === "/admin/resources") return "资源状态";
    if (path === "/admin/system") return "系统状态";
    if (path === "/admin/announcements") return "公告管理";
    return "页面不存在";
  }, [path]);

  return {
    path,
    navigate,
    session,
    authStatus,
    authError,
    sources,
    toast,
    pageTitle,
    isAdminRoute,
    isKnownRoute,
    isSensitiveRoute: isSensitiveConsoleRoute(path),
    sidebarOpen,
    setSidebarOpen,
    globalSlide,
    setGlobalSlide: (slide: GlobalSlide) => {
      setGlobalSlide(slide);
      if (slide === "support") void loadSupportTickets();
    },
    submitLogin,
    signOut,
    refreshCurrentPage,
    workspaceRows,
    workspacePageNumber,
    workspacePages,
    changeWorkspacePage,
    launchName,
    setLaunchName,
    launchPlan,
    setLaunchPlan,
    launchAutoRenew,
    setLaunchAutoRenew,
    launchStep,
    setLaunchStep,
    launchConfirmed,
    setLaunchConfirmed,
    previews,
    selectedPlan,
    selectedPrice,
    balanceSufficient,
    customerOwned,
    launchOperation,
    launchPollIssue,
    openLaunchedWorkspace,
    reviewWorkspaceLaunch,
    submitWorkspaceLaunch,
    commandBusy,
    workspaceDeleteIssue,
    deleteCurrentWorkspace,
    updateCurrentWorkspaceRenewal,
    supportTickets,
    supportLoading,
    supportError,
    loadSupportTickets,
    createSupportMapping,
    secrets,
    clearSecrets,
    workspaceSecretBusy,
    gatewaySecretBusy,
    workspaceBudgetBusy,
    updateWorkspaceBudget,
    revealWorkspacePassword,
    revealWorkspaceKey,
    rotateWorkspacePassword,
    copyText,
    billingView,
    setBillingView,
    selectedReceiptId,
    setSelectedReceiptId,
    clearReceiptDetail,
    selectReceipt,
    receiptCursor,
    receiptCursorStack,
    nextReceiptPage,
    previousReceiptPage,
    markRead,
    announcementBusy,
    selectedUsageKeyId,
    usagePeriod,
    usagePage,
    chooseUsageKey,
    chooseUsagePeriod,
    changeUsagePage,
    balanceHistoryPage,
    changeBalancePage,
    operatorAccountPage,
    operatorAccountPages,
    changeOperatorAccountPage,
    operatorWorkspacePage,
    operatorWorkspacePages,
    changeOperatorWorkspacePage,
    selectedOperatorWorkspaceId,
    setSelectedOperatorWorkspaceId,
    openOperatorWorkspace,
    disableOperatorAccount,
    setOperatorWorkspacePurchaseEligibility,
    walletAdjustmentOperation,
    setWalletAdjustmentOperation,
    submitWalletAdjustment,
    refreshWalletOperation,
    recoverWalletOperation,
    operatorProvisionOperation,
    setOperatorProvisionOperation,
    provisionAccount,
    createAnnouncement,
    publishAnnouncement,
    withdrawAnnouncement
  };
}

export type ConsoleController = ReturnType<typeof useConsoleController>;
