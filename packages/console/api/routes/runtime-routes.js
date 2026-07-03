export function buildRuntimeRoutes({ appService, body, requireAdmin }) {
  return {
    "GET /api/runtime/readiness": () => appService.runtimeReadiness(),
    "GET /api/production/readiness": () => appService.productionReadiness(),
    "POST /api/workspaces/runtime-status": () => {
      requireAdmin();
      return appService.runtimeStatus(body);
    }
  };
}
