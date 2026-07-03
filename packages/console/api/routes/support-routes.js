export function buildSupportRoutes({ appService, request, body, session, isAdminSession = false, scopedAccountId, scopedWorkspaceInput }) {
  return {
    "GET /api/support/tickets": () => {
      const url = new URL(request.url, "http://localhost");
      const all = isAdminSession && url.searchParams.get("scope") === "all";
      return appService.supportTickets({
        accountId: all ? null : scopedAccountId(url.searchParams.get("accountId") || "")
      }).then((tickets) => ({ tickets }));
    },
    "POST /api/support/tickets": () => {
      const input = scopedWorkspaceInput(body);
      return appService.createSupportTicket({
        ...input,
        userId: input.userId || session?.user?.id || "",
        author: session?.user?.email || input.author || ""
      });
    }
  };
}
