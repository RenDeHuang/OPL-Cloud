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

export function storageDestroyed(workspace) {
  return workspace?.state === "destroyed" || workspace?.disk?.status === "destroyed";
}

export function defaultStorageBackupPolicy() {
  return {
    name: "daily_7_weekly_4",
    retainDaily: 7,
    retainWeekly: 4,
    retainLast: 11
  };
}

export function backupRetentionPolicy(inputPolicy = null) {
  return {
    ...defaultStorageBackupPolicy(),
    ...(inputPolicy || {})
  };
}

export function latestStorageBackupForAccount(state, accountId, backupId) {
  const backup = (state.storageBackups || []).find((item) => item.id === backupId && item.accountId === accountId);
  if (!backup) throw new Error("storage_backup_not_found");
  return backup;
}

export function latestBillingReconciliationReport(state) {
  return (state.billingReconciliationReports || []).at(-1) || null;
}

export function operatorNotificationInScope(event, accountId) {
  if (!accountId) return true;
  if (event.accountId === accountId) return true;
  return event.accountId === "billing" && event.workspaceId === "billing";
}
