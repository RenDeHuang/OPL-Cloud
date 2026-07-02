export const consoleRoutes = [
  { path: "/", label: "Public Home", area: "public", requiresAuth: false },
  { path: "/pricing", label: "Pricing", area: "public", requiresAuth: false },
  { path: "/docs", label: "Docs", area: "public", requiresAuth: false },
  { path: "/status", label: "Service Status", area: "public", requiresAuth: false },
  { path: "/legal/terms", label: "Terms", area: "public", requiresAuth: false, hiddenInMenu: true },
  { path: "/legal/privacy", label: "Privacy", area: "public", requiresAuth: false, hiddenInMenu: true },
  { path: "/setup", label: "Setup", area: "public", requiresAuth: false, hiddenInMenu: true },

  { path: "/login", label: "Login", area: "auth", requiresAuth: false },
  { path: "/register", label: "Register", area: "auth", requiresAuth: false, featureGate: "registration", hiddenInMenu: true },
  { path: "/invite/accept", label: "Accept Invite", area: "auth", requiresAuth: false, featureGate: "invites", hiddenInMenu: true },
  { path: "/email/verify", label: "Email Verification", area: "auth", requiresAuth: false, hiddenInMenu: true },
  { path: "/forgot-password", label: "Forgot Password", area: "auth", requiresAuth: false, hiddenInMenu: true },
  { path: "/reset-password", label: "Reset Password", area: "auth", requiresAuth: false, hiddenInMenu: true },
  { path: "/auth/callback", label: "Auth Callback", area: "auth", requiresAuth: false, featureGate: "sso", hiddenInMenu: true },
  { path: "/logout", label: "Logout", area: "auth", requiresAuth: true, hiddenInMenu: true },

  { path: "/console", redirect: "/console/overview", label: "Console", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/overview", label: "Overview", area: "console", requiresAuth: true, menu: true },
  { path: "/console/workspaces", label: "Workspaces", area: "console", requiresAuth: true, menu: true },
  { path: "/console/workspaces/new", label: "Create Workspace", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/workspaces/:id", label: "Workspace Detail", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/workspaces/:id/access", label: "Workspace URL and Access", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/workspaces/:id/resources", label: "Workspace Compute and Storage", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/workspaces/:id/backups", label: "Workspace Backups", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/workspaces/:id/receipts", label: "Workspace Receipts", area: "console", requiresAuth: true, hiddenInMenu: true },

  { path: "/console/gateway", label: "Gateway", area: "console", requiresAuth: true, menu: true },
  { path: "/console/gateway/keys", label: "Access Keys", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/gateway/usage", label: "Gateway Usage", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/gateway/quotas", label: "Gateway Quotas", area: "console", requiresAuth: true, hiddenInMenu: true },

  { path: "/console/billing", label: "Billing", area: "console", requiresAuth: true, menu: true },
  { path: "/console/billing/wallet", label: "Wallet and Holds", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/billing/usage", label: "Billing Usage", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/billing/orders", label: "Top-up and Order Records", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/billing/invoices", label: "Invoices", area: "console", requiresAuth: true, hiddenInMenu: true },

  { path: "/console/account", label: "Account & Lab", area: "console", requiresAuth: true, menu: true },
  { path: "/console/account/profile", label: "Profile", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/account/security", label: "Login Security", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/account/lab", label: "Lab Ownership and Policy", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/account/alerts", label: "Notification Preferences", area: "console", requiresAuth: true, hiddenInMenu: true },

  { path: "/console/support", label: "Support", area: "console", requiresAuth: true, menu: true },
  { path: "/console/support/new", label: "New Ticket", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/support/:id", label: "Ticket Detail", area: "console", requiresAuth: true, hiddenInMenu: true },

  { path: "/console/resources", label: "Resource Catalog", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/resources/connectors", label: "Approved Connectors", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/resources/environments", label: "Approved Environments", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/resources/agents", label: "Approved Agent Packages", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/approvals", label: "Approvals", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/receipts", label: "Human-readable Receipts", area: "console", requiresAuth: true, hiddenInMenu: true },
  { path: "/console/alerts", label: "Alerts", area: "console", requiresAuth: true, menu: true },

  { path: "/admin", redirect: "/admin/overview", label: "Admin", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/overview", label: "Admin Overview", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/users", label: "Users", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/users/new", label: "Create User", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/users/:id", label: "User Detail", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/users/:id/wallet", label: "User Wallet", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/users/:id/workspaces", label: "User Workspaces", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/users/:id/usage", label: "User Usage", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/users/:id/audit", label: "User Audit", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/governance", label: "Governance", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/governance/organizations", label: "Organizations and Labs", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/governance/teams", label: "Teams", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/governance/roles", label: "Roles and Permissions", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/governance/policies", label: "Quota Approval and Audit Policies", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/workspaces", label: "All Workspaces", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/workspaces/:id", label: "Admin Workspace Detail", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/billing", label: "Billing Ops", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/billing/plans", label: "Plan Management", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/billing/topups", label: "Manual Top-up Records", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/billing/transactions", label: "Wallet Transactions", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/billing/reconciliation", label: "Reconciliation", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/gateway", label: "Gateway Ops", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/fabric", label: "Fabric", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/fabric/compute", label: "Compute Resources", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/fabric/storage", label: "Storage Resources", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/fabric/connectors", label: "Connector Approval", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/fabric/environments", label: "Environment Templates", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/fabric/agents", label: "Agent Package Approval", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/ledger", label: "Ledger", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/ledger/receipts", label: "Receipts", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/ledger/events", label: "Raw Ledger Events", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/ledger/policies", label: "Retention and Review Policies", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/runtime", label: "Runtime", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/runtime/readiness", label: "Runtime Readiness", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/runtime/kubernetes", label: "Kubernetes", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/runtime/images", label: "Workspace Images", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/runtime/domains", label: "Domains and Ingress", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/admin/support", label: "Support Ops", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/support/:id", label: "Support Ticket Handling", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },
  { path: "/admin/audit", label: "Audit", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/settings", label: "Settings", area: "admin", requiresAuth: true, requiresAdmin: true, adminMenu: true },
  { path: "/admin/alerts", label: "Global Alerts", area: "admin", requiresAuth: true, requiresAdmin: true, hiddenInMenu: true },

  { path: "/403", label: "Forbidden", area: "error", requiresAuth: false, hiddenInMenu: true },
  { path: "/404", label: "Not Found", area: "error", requiresAuth: false, hiddenInMenu: true },
  { path: "/500", label: "Error", area: "error", requiresAuth: false, hiddenInMenu: true }
];

export const ownerMenuRoutes = consoleRoutes.filter((route) => route.menu);
export const adminMenuRoutes = consoleRoutes.filter((route) => route.adminMenu);

export function normalizePath(pathname) {
  if (!pathname || pathname === "") return "/";
  return pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
}

function routePattern(path) {
  return new RegExp(`^${path.replace(/:[^/]+/g, "[^/]+")}$`);
}

export function findRoute(pathname) {
  const normalized = normalizePath(pathname);
  return consoleRoutes.find((route) => route.path === normalized)
    || consoleRoutes.find((route) => route.path.includes(":") && routePattern(route.path).test(normalized))
    || { path: "/404", label: "Not Found", area: "error", requiresAuth: false, hiddenInMenu: true };
}

export function navigate(path) {
  window.history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
