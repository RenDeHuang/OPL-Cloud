export function buildBillingRoutes({ appService, body, requireAdmin, session }) {
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
    "POST /api/billing/resource-settlements": () => {
      requireAdmin();
      return appService.settleResourceBilling(body);
    },
    "POST /api/billing/reconciliation": () => {
      requireAdmin();
      return appService.recordBillingReconciliation(body);
    }
  };
}
