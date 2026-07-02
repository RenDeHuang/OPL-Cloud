export function buildAdminRoutes({ appService, request, operatorSummaryToken, requireAdmin, scopedAccountId, body, isAdminSession = false }) {
  return {
    "GET /api/state": async () => {
      const url = new URL(request.url, "http://localhost");
      const requestedAccountId = url.searchParams.get("accountId") || "";
      return appService.getState(scopedAccountId(requestedAccountId));
    },
    "GET /api/operator/summary": async () => {
      const url = new URL(request.url, "http://localhost");
      const providedToken = request.headers["x-opl-operator-token"] || url.searchParams.get("operatorToken") || "";
      if (!isAdminSession) {
        if (!operatorSummaryToken) {
          const error = new Error("operator_summary_token_not_configured");
          error.status = 403;
          throw error;
        }
        if (providedToken !== operatorSummaryToken) {
          const error = new Error("operator_summary_token_invalid");
          error.status = 403;
          throw error;
        }
      }
      return appService.operatorSummary({
        accountId: url.searchParams.get("accountId") || null
      });
    },
    "GET /api/management/state": () => {
      requireAdmin();
      const url = new URL(request.url, "http://localhost");
      return appService.managementState({
        organizationId: url.searchParams.get("organizationId") || ""
      });
    },
    "POST /api/organizations": () => {
      requireAdmin();
      return appService.createOrganization(body);
    },
    "POST /api/users": () => {
      requireAdmin();
      return appService.createUser(body);
    },
    "POST /api/organizations/members": () => {
      requireAdmin();
      return appService.addOrganizationMember(body);
    }
  };
}
