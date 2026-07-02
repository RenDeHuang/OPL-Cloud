export const publicNavigationItems = [
  { path: "/", label: "Home" },
  { path: "/login", label: "Login" }
];

export const commercialLoginMethods = [
  { key: "email", label: "Email Login" }
];

export const commercialUserRoleOptions = [
  { label: "PI", value: "pi" }
];

export const landingFeatureCards = [
  {
    title: "Workspace distribution",
    description: "Issue isolated OPL Workspaces for each PI without exposing cloud plumbing."
  },
  {
    title: "Prepaid billing control",
    description: "Keep balances, holds, and usage receipts clear before compute is opened."
  },
  {
    title: "Managed runtime operations",
    description: "Keep provisioning, recovery, and evidence handled behind the console."
  }
];

export function unauthenticatedViewForPath(pathname = "/") {
  return pathname === "/login" ? "login" : "home";
}

export const consoleMenuItems = [
  { path: "/overview", label: "Overview", audience: "pi" },
  { path: "/account", label: "Account", audience: "pi" },
  { path: "/workspaces", label: "Workspaces", audience: "pi" },
  { path: "/create", label: "Create Workspace", audience: "pi" },
  { path: "/billing", label: "Billing", audience: "pi" },
  { path: "/alerts", label: "Alerts", audience: "pi" },
  { path: "/audit", label: "Audit", audience: "pi" },
  { path: "/users", label: "Users", audience: "operator" },
  { path: "/runtime", label: "Runtime", audience: "operator" }
];

export function canUseOperatorControls(session) {
  return session?.role === "operator";
}

export function visibleConsoleMenuItems(session) {
  const operator = canUseOperatorControls(session);
  return consoleMenuItems.filter((item) => item.audience !== "operator" || operator);
}

export function money(value) {
  return `¥${Number(value || 0).toFixed(2)}`;
}

export function packageSummary(plan) {
  return `${plan.cpu} CPU / ${plan.memoryGb}GB + ${plan.diskGb}GB storage`;
}

function ledgerAmount(value) {
  return Number(Number(value || 0).toFixed(4));
}

export function packageHoldPreview(plan, policy = {}) {
  const holdDays = Number(policy.prepaidHoldDays || 7);
  const compute = ledgerAmount(Number(plan?.price?.computeHourly || 0) * 24 * holdDays);
  const storage = ledgerAmount((Number(plan?.diskGb || 0) * Number(plan?.price?.storageGbMonth || 0) / 30) * holdDays);
  return {
    holdDays,
    compute,
    storage,
    total: ledgerAmount(compute + storage)
  };
}

export function workspaceStatus(workspace) {
  if (!workspace) return { label: "No workspace", tone: "default" };
  const labels = {
    running: { label: "Running", tone: "success" },
    stopped_server_disk_retained: { label: "Stopped, disk retained", tone: "warning" },
    server_destroyed_disk_retained: { label: "Compute destroyed, storage retained", tone: "warning" },
    storage_hold_exhausted: { label: "Storage hold exhausted", tone: "error" },
    stopped_storage_hold_exhausted: { label: "Stopped, storage hold exhausted", tone: "error" },
    destroyed: { label: "Destroyed", tone: "default" },
    failed: { label: "Failed", tone: "error" }
  };
  return labels[workspace.state] || { label: workspace.state, tone: "processing" };
}

export function workspaceActionState(workspace) {
  if (!workspace || workspace.state === "destroyed" || workspace.disk?.status === "destroyed") {
    return {
      canCopyUrl: false,
      canResetToken: false,
      canDeleteToken: false,
      canStopCompute: false,
      canRestartCompute: false,
      canDestroyCompute: false,
      canDestroyStorage: false
    };
  }

  const tokenActive = workspace.access?.tokenStatus === "active";
  const computeRunning = workspace.server?.status === "running";
  const computeDestroyed = workspace.server?.status === "destroyed" || workspace.state === "server_destroyed_disk_retained";
  const computeStopped = workspace.server?.status === "stopped" || workspace.state === "stopped_server_disk_retained";
  return {
    canCopyUrl: tokenActive,
    canResetToken: tokenActive,
    canDeleteToken: tokenActive,
    canStopCompute: computeRunning,
    canRestartCompute: computeStopped || computeDestroyed,
    canDestroyCompute: computeRunning || computeStopped,
    canDestroyStorage: workspace.disk?.status !== "destroyed"
  };
}

export function workspaceTableRows(workspaces = []) {
  return workspaces.map((workspace) => {
    const status = workspaceStatus(workspace);
    return {
      key: workspace.id,
      id: workspace.id,
      name: workspace.name,
      packageId: workspace.packageId,
      status: status.label,
      statusTone: status.tone,
      url: workspace.url,
      compute: `${workspace.server?.spec || "unknown"} / ${workspace.server?.status || "unknown"}`,
      storage: `${workspace.disk?.sizeGb || 0}GB / ${workspace.disk?.status || "unknown"}`,
      computeBilling: workspace.server?.billingStatus || "unknown",
      storageBilling: workspace.disk?.billingStatus || "unknown",
      tokenStatus: workspace.access?.tokenStatus || "unknown"
    };
  });
}

export function userStatusTone(status) {
  if (status === "active") return "success";
  if (status === "invited") return "warning";
  if (status === "disabled") return "error";
  return "default";
}

export function identityUserRows(users = []) {
  return users.map((user) => ({
    key: user.email || user.id,
    id: user.id || "",
    email: user.email || "",
    accountId: user.accountId || "",
    tenantId: user.tenantId || "",
    displayName: user.displayName || "",
    role: user.role || "pi",
    status: user.status || "active",
    statusTone: userStatusTone(user.status || "active"),
    createdAt: user.createdAt || "",
    updatedAt: user.updatedAt || ""
  }));
}

export function readinessStatus(readiness) {
  if (!readiness) return { label: "Unknown", tone: "default", detail: "" };
  const missing = [
    ...(readiness.failedChecks || []),
    ...(readiness.missingEnv || []),
    ...((readiness.missingTools || []).map((tool) => `tool:${tool}`))
  ];
  return {
    label: readiness.ready ? "Ready" : "Blocked",
    tone: readiness.ready ? "success" : "warning",
    detail: missing.join(" / ")
  };
}

export function eventRows(events = []) {
  return events.slice().reverse().map((event) => ({
      key: event.id,
      id: event.id,
      type: event.type,
      accountId: event.accountId || "",
      workspaceId: event.workspaceId || "",
      sourceEventId: event.sourceEventId || "",
      severity: event.severity || "",
    message: event.message || "",
    amount: event.amount,
    createdAt: event.createdAt || event.updatedAt || ""
  }));
}
