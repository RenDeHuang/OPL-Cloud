export function buildBillingRoutes({ appService, body, requireAdmin, session, scopedWorkspaceInput }) {
  return {
    "POST /api/billing/topups": () => {
      requireAdmin();
      return appService.manualTopUp(session
        ? {
          ...body,
          operatorUserId: session.user.id,
          operatorAccountId: session.user.accountId
        }
        : body);
    },
    "POST /api/billing/settle": () => appService.settleBilling(scopedWorkspaceInput(body)),
    "POST /api/billing/request-usage": () => appService.recordRequestUsage(scopedWorkspaceInput(body)),
    "POST /api/billing/reconciliation": () => {
      requireAdmin();
      return appService.recordBillingReconciliation(body);
    }
  };
}
