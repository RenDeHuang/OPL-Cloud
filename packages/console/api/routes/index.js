import { buildAdminRoutes } from "./admin-routes.js";
import { buildAuthRoutes } from "./auth-routes.js";
import { buildBillingRoutes } from "./billing-routes.js";
import { buildLedgerRoutes } from "./ledger-routes.js";
import { buildResourceRoutes } from "./resource-routes.js";
import { buildRuntimeRoutes } from "./runtime-routes.js";
import { buildSupportRoutes } from "./support-routes.js";
import { buildWorkspaceRoutes } from "./workspace-routes.js";

export const apiRouteManifest = [
  "GET /api/healthz",
  "POST /api/auth/login",
  "POST /api/auth/operator-login",
  "POST /api/auth/logout",
  "GET /api/auth/me",
  "GET /api/state",
  "GET /api/operator/summary",
  "POST /api/operator/cleanup-workspace-access",
  "GET /api/management/state",
  "POST /api/billing/topups",
  "POST /api/organizations",
  "POST /api/users",
  "POST /api/users/disable",
  "POST /api/users/delete",
  "POST /api/organizations/members",
  "GET /api/compute-pools",
  "GET /api/compute-allocations",
  "GET /api/compute-allocations/:id",
  "POST /api/compute-allocations",
  "POST /api/compute-allocations/:id/destroy",
  "POST /api/storage-volumes",
  "POST /api/storage-volumes/destroy",
  "POST /api/storage-attachments",
  "POST /api/storage-attachments/detach",
  "POST /api/workspaces",
  "POST /api/workspaces/reset-token",
  "POST /api/workspaces/delete-token",
  "POST /api/billing/request-usage",
  "POST /api/billing/resource-settlements",
  "POST /api/billing/reconciliation",
  "GET /api/ledger/task-receipts",
  "POST /api/ledger/task-receipts",
  "GET /api/runtime/readiness",
  "GET /api/production/readiness",
  "POST /api/workspaces/runtime-status",
  "GET /api/support/tickets",
  "POST /api/support/tickets"
];

export function buildApiRoutes(deps) {
  return {
    "GET /api/healthz": () => ({ ok: true, service: "opl-console" }),
    ...buildAuthRoutes(deps),
    ...buildAdminRoutes(deps),
    ...buildBillingRoutes(deps),
    ...buildResourceRoutes(deps),
    ...buildWorkspaceRoutes(deps),
    ...buildLedgerRoutes(deps),
    ...buildRuntimeRoutes(deps),
    ...buildSupportRoutes(deps)
  };
}
