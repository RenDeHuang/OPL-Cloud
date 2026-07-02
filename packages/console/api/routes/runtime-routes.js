export function buildRuntimeRoutes({ appService, body, requireAdmin }) {
  return {
    "GET /api/runtime/readiness": () => {
      requireAdmin();
      return appService.runtimeReadiness();
    },
    "GET /api/production/readiness": () => {
      requireAdmin();
      return appService.productionReadiness();
    },
    "POST /api/workspaces/runtime-status": () => {
      requireAdmin();
      return appService.runtimeStatus(body);
    }
  };
}
