export function buildWorkspaceRoutes({ appService, body, scopedWorkspaceInput }) {
  return {
    "POST /api/workspaces": () => appService.createWorkspace(scopedWorkspaceInput(body)),
    "POST /api/workspaces/reset-token": () => appService.resetWorkspaceToken(scopedWorkspaceInput(body)),
    "POST /api/workspaces/delete-token": () => appService.deleteWorkspaceToken(scopedWorkspaceInput(body))
  };
}
