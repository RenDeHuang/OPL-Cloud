export function buildBillingRoutes({ appService, body, requireAdmin, session, scopedWorkspaceInput }) {
  return {
    "POST /api/accounts/credit": () => {
      requireAdmin();
      return appService.creditAccount(session
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
