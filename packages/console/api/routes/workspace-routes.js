export function buildWorkspaceRoutes({ appService, body, scopedWorkspaceInput }) {
  return {
    "POST /api/workspaces": () => appService.createWorkspace(scopedWorkspaceInput(body)),
    "POST /api/workspaces/stop-server": () => appService.stopServer(scopedWorkspaceInput(body)),
    "POST /api/workspaces/restart-server": () => appService.restartServer(scopedWorkspaceInput(body)),
    "POST /api/workspaces/destroy-server": () => appService.destroyServer(scopedWorkspaceInput(body)),
    "POST /api/workspaces/destroy-disk": () => appService.destroyDisk(scopedWorkspaceInput(body)),
    "POST /api/workspaces/storage-backups": () => appService.createStorageBackup(scopedWorkspaceInput(body)),
    "POST /api/workspaces/restore-storage-backup": () => appService.restoreWorkspaceFromBackup(scopedWorkspaceInput(body)),
    "POST /api/workspaces/prune-storage-backups": () => appService.pruneStorageBackups(scopedWorkspaceInput(body)),
    "POST /api/workspaces/reset-token": () => appService.resetWorkspaceToken(scopedWorkspaceInput(body)),
    "POST /api/workspaces/delete-token": () => appService.deleteWorkspaceToken(scopedWorkspaceInput(body))
  };
}
