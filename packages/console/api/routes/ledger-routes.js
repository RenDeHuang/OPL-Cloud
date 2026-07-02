export function buildLedgerRoutes({ appService, body, request, scopedAccountId, scopedWorkspaceInput }) {
  return {
    "GET /api/ledger/task-receipts": () => {
      const url = new URL(request.url, "http://localhost");
      return appService.taskEvidenceReceipts({
        accountId: scopedAccountId(url.searchParams.get("accountId") || ""),
        workspaceId: url.searchParams.get("workspaceId") || null,
        taskId: url.searchParams.get("taskId") || null
      });
    },
    "POST /api/ledger/task-receipts": () => appService.recordTaskEvidenceReceipt(scopedWorkspaceInput(body))
  };
}
