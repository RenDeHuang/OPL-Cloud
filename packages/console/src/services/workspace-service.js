export function latestWorkspaceForAccount(state, accountId, workspaceId) {
  const workspace = state.workspaces[workspaceId];
  if (!workspace || workspace.ownerAccountId !== accountId) {
    throw new Error("workspace_not_found");
  }
  return workspace;
}

export function workspaceBySlug(state, slug) {
  return Object.values(state.workspaces).find((workspace) => workspace.slug === slug);
}

export function workspaceByIdOrSlug(state, value) {
  return state.workspaces[value] || workspaceBySlug(state, value);
}

export function storageDestroyed(workspace) {
  return workspace?.state === "destroyed" || workspace?.disk?.status === "destroyed";
}

export function latestBillingReconciliationReport(state) {
  return (state.billingReconciliationReports || []).at(-1) || null;
}

export function operatorNotificationInScope(event, accountId) {
  if (!accountId) return true;
  if (event.accountId === accountId) return true;
  return event.accountId === "billing" && event.workspaceId === "billing";
}
