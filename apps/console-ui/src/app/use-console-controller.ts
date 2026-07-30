import { useEffect, useMemo, useRef, useState } from "react";

import { currentSession, login as loginRequest, logoutLocalFirst } from "../api/auth-api.ts";
import {
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
  getWalletAdjustment,
  markAnnouncementRead,
  previewPricing,
  provisionOperatorAccount,
  publishOperatorAnnouncement,
  recoverWalletAdjustment,
  recoverWorkspaceLaunch as recoverOperatorWorkspaceLaunch,
  resolveBillingReview,
  revealGatewayKey,
  withdrawOperatorAnnouncement
} from "../api/console-read-api.ts";
import type {
  AnnouncementDraftRequest,
  AnnouncementScheduleRequest,
  AuthSession,
  BillingReviewResolutionRequest,
  OperatorReconciliationItemDTO,
  PlanId,
  ProvisionAccountRequest,
  SourceEnvelope,
  WalletAdjustmentRecoveryRequest,
  WalletAdjustmentRequest,
  WorkspaceLaunchRecoveryRequest,
  WorkspaceLaunchRequest,
  WorkspacePricePreview
} from "../api/dtos.ts";
import {
  findWorkspaceInPages,
  getWorkspaceLaunch,
  getWorkspaceLaunches,
  getWorkspaces,
  getWorkspaceRuntimeStatus,
  isTerminalWorkspaceLaunch,
  launchWorkspace,
  revealWorkspaceCredentials,
  rotateWorkspaceCredentials,
  workspaceLaunchIdempotencyKey
} from "../api/workspaces-api.ts";
import { defaultAuthenticatedRoute, needsSession, workspaceIdFromPath, workspacePage } from "../console-model.ts";
import { isKnownConsoleRoute, isSensitiveConsoleRoute, useConsoleRouter } from "./console-router.ts";
import type { BillingView, ConsoleSecrets, ConsoleSources, GlobalSlide, RemoteState, WorkspaceLaunchStep } from "./console-controller-types.ts";

const secretLifetimeMs = 60_000;
const workspaceLaunchPollIntervalMs = 10_000;
const workspaceLaunchPollAttempts = 30;
const operatorPageSize = 20;

const emptyRemote = <T,>(): RemoteState<T> => ({ value: null, loading: false, error: "" });

function initialSources(): ConsoleSources {
  return {
    workspaces: emptyRemote(),
    workspaceDetail: emptyRemote(),
    runtime: emptyRemote(),
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
  return { source, status: "unavailable", available: false, fetchedAt: "" };
}

function friendlyError(error: unknown) {
  const raw = String(error && typeof error === "object" && "message" in error ? error.message : error || "request_failed");
  const messages: Record<string, string> = {
    not_authenticated: "登录已失效，请重新登录",
    account_scope_forbidden: "没有权限访问该资源",
    insufficient_balance: "可用余额不足",
    gateway_key_missing: "API Key 尚未就绪",
    gateway_key_ambiguous: "API Key 状态异常，请联系管理员",
    monthly_account_unmapped: "API 服务尚未开通",
    authentication_unavailable: "身份服务暂不可用，请稍后重试",
    workspace_not_found: "Workspace 不存在或无权访问",
    workspace_credentials_unavailable: "Workspace 凭证暂不可用",
    workspace_not_running: "Workspace 尚未就绪",
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
  return left.name === right.name && left.packageId === right.packageId && left.sizeGb === right.sizeGb && left.autoRenew === right.autoRenew;
}

function walletRecoveryIdempotencyKey(operationId: string) {
  const suffix = /^wallet-adjustment-([0-9a-f]{18})$/.exec(operationId)?.[1] || "";
  return suffix ? `wallet-recovery-${suffix.slice(0, 16)}` : "";
}

export function useConsoleController() {
  const { path, navigate } = useConsoleRouter();
  const [session, setSession] = useState<AuthSession | null>(null);
  const [authStatus, setAuthStatus] = useState<"public" | "checking" | "ready" | "error">(needsSession(path) ? "checking" : "public");
  const [authError, setAuthError] = useState("");
  const [sources, setSources] = useState<ConsoleSources>(initialSources);
  const [toast, setToast] = useState<{ text: string; tone: "good" | "danger" }>({ text: "", tone: "good" });
  const [globalSlide, setGlobalSlide] = useState<GlobalSlide>("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [workspacePageNumber, setWorkspacePageNumber] = useState(1);
  const [balanceHistoryPage, setBalanceHistoryPage] = useState(1);
  const [operatorAccountPage, setOperatorAccountPage] = useState(1);
  const [operatorWorkspacePage, setOperatorWorkspacePage] = useState(1);
  const [selectedOperatorWorkspaceId, setSelectedOperatorWorkspaceId] = useState("");
  const [billingView, setBillingView] = useState<BillingView>("terms");
  const [selectedReceiptId, setSelectedReceiptId] = useState("");
  const [selectedUsageKeyId, setSelectedUsageKeyId] = useState("");
  const [usagePeriod, setUsagePeriod] = useState("month");
  const [usagePage, setUsagePage] = useState(1);
  const [launchName, setLaunchName] = useState("");
  const [launchPlan, setLaunchPlan] = useState<PlanId>("basic");
  const [launchStep, setLaunchStep] = useState<WorkspaceLaunchStep>("configure");
  const [launchConfirmed, setLaunchConfirmed] = useState(false);
  const [previews, setPreviews] = useState<Partial<Record<PlanId, WorkspacePricePreview>>>({});
  const [launchOperation, setLaunchOperation] = useState<Awaited<ReturnType<typeof getWorkspaceLaunch>> | null>(null);
  const [launchPollIssue, setLaunchPollIssue] = useState<"" | "error" | "timeout">("");
  const [secrets, setSecrets] = useState<ConsoleSecrets>({ apiKey: null, workspace: null });
  const [workspaceSecretBusy, setWorkspaceSecretBusy] = useState(false);
  const [gatewaySecretBusy, setGatewaySecretBusy] = useState(false);
  const [commandBusy, setCommandBusy] = useState(false);
  const [announcementBusy, setAnnouncementBusy] = useState("");
  const [walletAdjustmentOperation, setWalletAdjustmentOperation] = useState<Awaited<ReturnType<typeof getWalletAdjustment>> | null>(null);

  const requestGeneration = useRef(0);
  const sessionGeneration = useRef(0);
  const secretRequestGeneration = useRef(0);
  const sessionRef = useRef<AuthSession | null>(null);
  const selectedReceiptIdRef = useRef("");
  const selectedUsageKeyIdRef = useRef("");
  const selectedOperatorWorkspaceIdRef = useRef("");
  const secretTimer = useRef<number | undefined>(undefined);
  const toastTimer = useRef<number | undefined>(undefined);
  const workspaceLaunchIntent = useRef<{ input: WorkspaceLaunchRequest; idempotencyKey: string } | null>(null);
  const runtimeRotationIntent = useRef<{ workspaceId: string; idempotencyKey: string } | null>(null);
  const walletAdjustmentIntent = useRef<{ accountId: string; input: WalletAdjustmentRequest; idempotencyKey: string } | null>(null);
  const walletAdjustmentRecoveryIntent = useRef<{ operationId: string; input: WalletAdjustmentRecoveryRequest; idempotencyKey: string } | null>(null);
  const operatorProvisionIntent = useRef<{ input: ProvisionAccountRequest; idempotencyKey: string } | null>(null);
  const operatorDisableIntents = useRef(new Map<string, string>());
  const billingReviewIntent = useRef<{ resourceType: string; resourceId: string; input: BillingReviewResolutionRequest; idempotencyKey: string } | null>(null);
  const workspaceLaunchRecoveryIntent = useRef<{ operationId: string; input: WorkspaceLaunchRecoveryRequest; idempotencyKey: string } | null>(null);
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

  const resetConsoleState = () => {
    clearSecrets();
    setSources(initialSources());
    setGlobalSlide("");
    setLaunchOperation(null);
    setLaunchPollIssue("");
    setLaunchStep("configure");
    setLaunchConfirmed(false);
    setLaunchName("");
    setLaunchPlan("basic");
    setPreviews({});
    setSelectedUsageKeyId("");
    selectedUsageKeyIdRef.current = "";
    setUsagePage(1);
    setBillingView("terms");
    setSelectedReceiptId("");
    selectedReceiptIdRef.current = "";
    setWorkspacePageNumber(1);
    setBalanceHistoryPage(1);
    setOperatorAccountPage(1);
    setOperatorWorkspacePage(1);
    setSelectedOperatorWorkspaceId("");
    selectedOperatorWorkspaceIdRef.current = "";
    setWalletAdjustmentOperation(null);
    workspaceLaunchIntent.current = null;
    runtimeRotationIntent.current = null;
    walletAdjustmentIntent.current = null;
    walletAdjustmentRecoveryIntent.current = null;
    operatorProvisionIntent.current = null;
    billingReviewIntent.current = null;
    workspaceLaunchRecoveryIntent.current = null;
    announcementCreateIntent.current = null;
    operatorDisableIntents.current.clear();
    announcementPublishIntents.current.clear();
    announcementWithdrawIntents.current.clear();
  };

  const replaceSession = (next: AuthSession | null) => {
    sessionGeneration.current += 1;
    requestGeneration.current += 1;
    resetConsoleState();
    sessionRef.current = next;
    setSession(next);
  };

  const isRequestCurrent = (generation: number, userId?: string) => generation === requestGeneration.current
    && (!userId || sessionRef.current?.user.id === userId);

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

  const loadReceipts = async (generation: number, activeSession: AuthSession, limit = 20) => {
    beginSource("receipts");
    try {
      const result = await getBillingReceipts("", limit);
      if (isRequestCurrent(generation, activeSession.user.id)) updateSource("receipts", { value: result, loading: false, error: "" });
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) failSource("receipts", error, unavailableSource("ledger"));
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
      const entries = await Promise.all(catalog.packages.filter((plan) => plan.available).map(async (plan) => {
        const preview = await previewPricing({ resourceType: "workspace", packageId: plan.id, sizeGb: plan.diskGb }, activeSession.csrfToken);
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
    beginSource("workspaceDetail");
    beginSource("runtime");
    try {
      const detail = await findWorkspaceInPages(workspaceId);
      if (!isRequestCurrent(generation, activeSession.user.id) || workspaceIdFromPath(window.location.pathname) !== workspaceId) return;
      updateSource("workspaceDetail", { value: detail, loading: false, error: "" });
      if (!detail.available || detail.data === null) {
        updateSource("runtime", { value: unavailableSource("fabric"), loading: false, error: "" });
        return;
      }
      const runtime = await getWorkspaceRuntimeStatus(workspaceId);
      if (!isRequestCurrent(generation, activeSession.user.id) || workspaceIdFromPath(window.location.pathname) !== workspaceId) return;
      updateSource("runtime", { value: runtime, loading: false, error: "" });
    } catch (error) {
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      failSource("workspaceDetail", error, unavailableSource("control-plane"));
      failSource("runtime", error, unavailableSource("fabric"));
    }
  };

  const loadUsage = async (generation: number, activeSession: AuthSession, keyId: string, page = 1, period = usagePeriod) => {
    if (!keyId) return;
    beginSource("usage");
    beginSource("usageSummary");
    try {
      const [usage, summary] = await Promise.all([
        getGatewayKeyUsage(keyId, page, 20),
        getGatewayKeyUsageSummary(keyId, period)
      ]);
      if (!isRequestCurrent(generation, activeSession.user.id) || selectedUsageKeyIdRef.current !== keyId) return;
      updateSource("usage", { value: usage, loading: false, error: "" });
      updateSource("usageSummary", { value: summary, loading: false, error: "" });
      setUsagePage(page);
    } catch (error) {
      if (isRequestCurrent(generation, activeSession.user.id)) {
        failSource("usage", error, unavailableSource("sub2api"));
        failSource("usageSummary", error, unavailableSource("sub2api"));
      }
    }
  };

  const loadUsageKeys = async (generation: number, activeSession: AuthSession) => {
    beginSource("usageKeys");
    try {
      const keys = await getGatewayKeys({ page: 1, pageSize: 20 });
      if (!isRequestCurrent(generation, activeSession.user.id)) return;
      updateSource("usageKeys", { value: keys, loading: false, error: "" });
      if (!keys.available || keys.data.items.length === 0) return;
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
            flash("Workspace 已开通");
            navigate(`/console/workspaces/${encodeURIComponent(operation.workspaceId)}`);
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
      await Promise.all([loadWorkspaces(generation, activeSession, 1, 1), loadWallet(generation, activeSession), loadAccountUsage(generation, activeSession), loadReceipts(generation, activeSession, 3), loadAnnouncements(generation, activeSession, 3)]);
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
      await Promise.all([loadWorkspaces(generation, activeSession, 1, 10), loadReceipts(generation, activeSession)]);
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
    const generation = ++requestGeneration.current;
    clearSecrets();
    setSidebarOpen(false);
    setGlobalSlide("");
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
  }, [path]);

  useEffect(() => () => {
    requestGeneration.current += 1;
    sessionGeneration.current += 1;
    clearSecrets();
    if (toastTimer.current) window.clearTimeout(toastTimer.current);
  }, []);

  const submitLogin = async (email: string, password: string) => {
    setAuthError("");
    setAuthStatus("checking");
    try {
      const next = await loginRequest({ email, password });
      replaceSession(next);
      setAuthStatus("ready");
      const requested = new URLSearchParams(window.location.search).get("redirect");
      const allowed = requested?.startsWith("/console") || (next.isOperator && requested?.startsWith("/admin"));
      navigate(allowed && requested ? requested : defaultAuthenticatedRoute(next.isOperator));
    } catch (error) {
      setAuthStatus("public");
      setAuthError(friendlyError(error));
    }
  };

  const signOut = async () => {
    const csrfToken = session?.csrfToken || "";
    clearSecrets();
    try {
      await logoutLocalFirst(csrfToken, () => replaceSession(null), () => navigate("/"));
    } catch {
      // Local state and navigation are already cleared before the remote request.
    }
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
    const input: WorkspaceLaunchRequest = { name: launchName.trim(), packageId: selectedPlan.id, sizeGb: selectedPlan.id === "basic" ? 10 : 100, autoRenew: false };
    if (!workspaceLaunchIntent.current || !sameLaunchRequest(workspaceLaunchIntent.current.input, input)) {
      workspaceLaunchIntent.current = { input, idempotencyKey: workspaceLaunchIdempotencyKey() };
    }
    setCommandBusy(true);
    try {
      const operation = await launchWorkspace(input, session.csrfToken, workspaceLaunchIntent.current.idempotencyKey);
      workspaceLaunchIntent.current = null;
      setLaunchOperation(operation);
      if (operation.status === "succeeded" && operation.workspaceId) {
        flash("Workspace 已开通");
        navigate(`/console/workspaces/${encodeURIComponent(operation.workspaceId)}`);
      } else if (operation.status === "refunded") {
        flash("Workspace 未完成，已退款", "danger");
      } else if (!isTerminalWorkspaceLaunch(operation.status) && operation.status !== "manual_review") {
        void pollWorkspaceLaunch(operation.operationId, requestGeneration.current, session);
      }
    } catch (error) {
      const payload = error && typeof error === "object" && "payload" in error ? (error as { payload?: unknown }).payload : null;
      const unknown = Boolean(payload && typeof payload === "object" && (payload as { status?: string }).status === "unknown");
      if (!unknown) workspaceLaunchIntent.current = null;
      flash(friendlyError(error), "danger");
    } finally {
      setCommandBusy(false);
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
    if (!runtimeRotationIntent.current || runtimeRotationIntent.current.workspaceId !== workspace.id) {
      runtimeRotationIntent.current = { workspaceId: workspace.id, idempotencyKey: `runtime-credential:${crypto.randomUUID()}` };
    }
    clearSecrets();
    const activeGeneration = ++secretRequestGeneration.current;
    setWorkspaceSecretBusy(true);
    try {
      const response = await rotateWorkspaceCredentials(workspace.id, session.csrfToken, runtimeRotationIntent.current.idempotencyKey);
      runtimeRotationIntent.current = null;
      if (activeGeneration !== secretRequestGeneration.current || workspace.id !== workspaceIdFromPath(window.location.pathname)) return;
      setSecrets({ apiKey: null, workspace: response.access });
      armSecretTimeout();
      flash("Workspace 凭证已轮换");
      await loadWorkspaceDetail(requestGeneration.current, session, workspace.id);
    } catch (error) {
      flash(mutationError(error), "danger");
    } finally {
      if (activeGeneration === secretRequestGeneration.current) setWorkspaceSecretBusy(false);
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

  const selectReceipt = async (receiptId: string) => {
    if (!session) return;
    selectedReceiptIdRef.current = receiptId;
    setSelectedReceiptId(receiptId);
    beginSource("receiptDetail");
    try {
      const result = await getBillingReceipt(receiptId);
      if (selectedReceiptIdRef.current !== receiptId) return;
      updateSource("receiptDetail", { value: result, loading: false, error: "" });
    } catch (error) {
      if (selectedReceiptIdRef.current === receiptId) failSource("receiptDetail", error, unavailableSource("ledger"));
    }
  };

  const markRead = async (announcementId: string) => {
    if (!session || announcementBusy) return;
    setAnnouncementBusy(announcementId);
    try {
      await markAnnouncementRead(announcementId, session.csrfToken, `announcement-read:${crypto.randomUUID()}`);
      await loadAnnouncements(requestGeneration.current, session, path === "/console/overview" ? 3 : 20);
    } catch (error) {
      flash(friendlyError(error), "danger");
    } finally {
      setAnnouncementBusy("");
    }
  };

  const chooseUsageKey = async (keyId: string) => {
    if (!session) return;
    clearSecrets();
    selectedUsageKeyIdRef.current = keyId;
    setSelectedUsageKeyId(keyId);
    await loadUsage(requestGeneration.current, session, keyId, 1, usagePeriod);
  };

  const chooseUsagePeriod = async (period: string) => {
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
    const idempotencyKey = operatorDisableIntents.current.get(accountId) || `account-disable:${accountId}:${crypto.randomUUID()}`;
    operatorDisableIntents.current.set(accountId, idempotencyKey);
    try {
      await disableOperatorAccountCommand(accountId, "operator_requested", session.csrfToken, idempotencyKey);
      operatorDisableIntents.current.delete(accountId);
      flash("客户已停用");
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
    } catch (error) {
      flash(mutationError(error), "danger");
    }
  };

  const submitWalletAdjustment = async (accountId: string, input: WalletAdjustmentRequest) => {
    if (!session || commandBusy || input.confirmationAccountId !== accountId || !input.amountUsd || !input.reason.trim()) return;
    if (!window.confirm("请再次确认这笔余额操作：提交后会写入客户账户并保留操作记录。")) return;
    if (!walletAdjustmentIntent.current || walletAdjustmentIntent.current.accountId !== accountId || JSON.stringify(walletAdjustmentIntent.current.input) !== JSON.stringify(input)) {
      walletAdjustmentIntent.current = { accountId, input, idempotencyKey: `wallet-adjustment:${crypto.randomUUID()}` };
    }
    setCommandBusy(true);
    try {
      const result = await createWalletAdjustment(accountId, walletAdjustmentIntent.current.input, session.csrfToken, walletAdjustmentIntent.current.idempotencyKey);
      setWalletAdjustmentOperation(result);
      if (result.status === "manual_review") flash("结果待确认，已进入人工复核", "danger");
      else {
        walletAdjustmentIntent.current = null;
        flash("余额操作已提交");
      }
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      return result;
    } catch (error) {
      flash(mutationError(error), "danger");
      return null;
    } finally {
      setCommandBusy(false);
    }
  };

  const refreshWalletOperation = async () => {
    if (!session || !walletAdjustmentOperation?.operationId) return;
    try {
      const result = await getWalletAdjustment(walletAdjustmentOperation.operationId);
      setWalletAdjustmentOperation(result);
      if (result.status === "succeeded") walletAdjustmentIntent.current = null;
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
    } catch (error) {
      flash(friendlyError(error), "danger");
    }
  };

  const recoverWalletOperation = async () => {
    const operation = walletAdjustmentOperation;
    if (!session || !operation || operation.status !== "manual_review" || !operation.allowedActions?.includes("recover_wallet_adjustment")) return;
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
      setWalletAdjustmentOperation(result);
      if (result.status === "succeeded") {
        walletAdjustmentRecoveryIntent.current = null;
        walletAdjustmentIntent.current = null;
        flash("余额操作已确认");
      } else flash("恢复结果仍待人工确认", "danger");
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
    } catch (error) {
      flash(mutationError(error), "danger");
    } finally {
      setCommandBusy(false);
    }
  };

  const provisionAccount = async (input: ProvisionAccountRequest) => {
    if (!session || commandBusy) return false;
    if (!operatorProvisionIntent.current || JSON.stringify(operatorProvisionIntent.current.input) !== JSON.stringify(input)) {
      operatorProvisionIntent.current = { input, idempotencyKey: `account-provision:${crypto.randomUUID()}` };
    }
    setCommandBusy(true);
    try {
      await provisionOperatorAccount(operatorProvisionIntent.current.input, session.csrfToken, operatorProvisionIntent.current.idempotencyKey);
      operatorProvisionIntent.current = null;
      await loadOperatorAccounts(requestGeneration.current, session, operatorAccountPage);
      flash("用户已开通");
      return true;
    } catch (error) {
      flash(mutationError(error), "danger");
      return false;
    } finally {
      setCommandBusy(false);
    }
  };

  const resolveReview = async (review: OperatorReconciliationItemDTO) => {
    if (!session) return;
    const evidenceRef = (window.prompt("请输入 case-YYYYMMDD-xxx 证据引用") || "").trim();
    if (!evidenceRef) return;
    try {
      if (review.allowedActions.includes("recover_workspace_launch")) {
        const input: WorkspaceLaunchRecoveryRequest = { accountId: review.accountId, billingOperationId: review.billingOperationId, evidenceRef };
        if (!workspaceLaunchRecoveryIntent.current || workspaceLaunchRecoveryIntent.current.operationId !== review.billingOperationId || JSON.stringify(workspaceLaunchRecoveryIntent.current.input) !== JSON.stringify(input)) {
          workspaceLaunchRecoveryIntent.current = { operationId: review.billingOperationId, input, idempotencyKey: `recover-${crypto.randomUUID()}` };
        }
        await recoverOperatorWorkspaceLaunch(review.billingOperationId, workspaceLaunchRecoveryIntent.current.input, session.csrfToken, workspaceLaunchRecoveryIntent.current.idempotencyKey);
        workspaceLaunchRecoveryIntent.current = null;
      } else {
        const input: BillingReviewResolutionRequest = { accountId: review.accountId, billingOperationId: review.billingOperationId, decision: "activate_charged_resource", evidenceRef };
        if (!billingReviewIntent.current || billingReviewIntent.current.resourceType !== review.resourceType || billingReviewIntent.current.resourceId !== review.id || JSON.stringify(billingReviewIntent.current.input) !== JSON.stringify(input)) {
          billingReviewIntent.current = { resourceType: review.resourceType, resourceId: review.id, input, idempotencyKey: `billing-review:${review.resourceType}:${review.id}:${crypto.randomUUID()}` };
        }
        await resolveBillingReview(review.resourceType, review.id, billingReviewIntent.current.input, session.csrfToken, billingReviewIntent.current.idempotencyKey);
        billingReviewIntent.current = null;
      }
      flash("复核命令已提交");
      await loadOperatorReconciliation(requestGeneration.current, session);
    } catch (error) {
      flash(mutationError(error), "danger");
    }
  };

  const createAnnouncement = async (input: AnnouncementDraftRequest) => {
    if (!session) return false;
    if (!announcementCreateIntent.current || JSON.stringify(announcementCreateIntent.current.input) !== JSON.stringify(input)) {
      announcementCreateIntent.current = { input, idempotencyKey: `announcement-create:${crypto.randomUUID()}` };
    }
    try {
      await createOperatorAnnouncement(announcementCreateIntent.current.input, session.csrfToken, announcementCreateIntent.current.idempotencyKey);
      announcementCreateIntent.current = null;
      flash("公告草稿已创建");
      await loadOperatorAnnouncements(requestGeneration.current, session);
      return true;
    } catch (error) {
      flash(mutationError(error), "danger");
      return false;
    }
  };

  const publishAnnouncement = async (announcementId: string) => {
    if (!session || !window.confirm("确认发布公告？")) return;
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
      announcementPublishIntents.current.delete(announcementId);
      flash("公告已发布");
      await loadOperatorAnnouncements(requestGeneration.current, session);
    } catch (error) {
      flash(mutationError(error), "danger");
    }
  };

  const withdrawAnnouncement = async (announcementId: string) => {
    if (!session || !window.confirm("确认撤下公告？")) return;
    const idempotencyKey = announcementWithdrawIntents.current.get(announcementId) || `announcement-withdraw:${announcementId}:${crypto.randomUUID()}`;
    announcementWithdrawIntents.current.set(announcementId, idempotencyKey);
    try {
      await withdrawOperatorAnnouncement(announcementId, session.csrfToken, idempotencyKey);
      announcementWithdrawIntents.current.delete(announcementId);
      flash("公告已撤下");
      await loadOperatorAnnouncements(requestGeneration.current, session);
    } catch (error) {
      flash(mutationError(error), "danger");
    }
  };

  const selectedPlan = sources.catalog.value?.packages.find((plan) => plan.id === launchPlan && plan.available) || null;
  const selectedPrice = selectedPlan ? previews[selectedPlan.id]?.totalChargeUsdMicros ?? null : null;
  const wallet = sources.wallet.value?.available ? sources.wallet.value.data : null;
  const balanceSufficient = wallet && selectedPrice !== null && /^\d+$/.test(wallet.usdMicros)
    ? BigInt(wallet.usdMicros) >= BigInt(selectedPrice)
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
    setGlobalSlide,
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
    launchStep,
    setLaunchStep,
    launchConfirmed,
    setLaunchConfirmed,
    previews,
    selectedPlan,
    selectedPrice,
    balanceSufficient,
    launchOperation,
    launchPollIssue,
    reviewWorkspaceLaunch,
    submitWorkspaceLaunch,
    commandBusy,
    secrets,
    clearSecrets,
    workspaceSecretBusy,
    gatewaySecretBusy,
    revealWorkspacePassword,
    revealWorkspaceKey,
    rotateWorkspacePassword,
    copyText,
    billingView,
    setBillingView,
    selectedReceiptId,
    setSelectedReceiptId,
    selectReceipt,
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
    walletAdjustmentOperation,
    setWalletAdjustmentOperation,
    submitWalletAdjustment,
    refreshWalletOperation,
    recoverWalletOperation,
    provisionAccount,
    resolveReview,
    createAnnouncement,
    publishAnnouncement,
    withdrawAnnouncement
  };
}

export type ConsoleController = ReturnType<typeof useConsoleController>;
