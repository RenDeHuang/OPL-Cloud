function routeNotFound() {
  const error = new Error("route_not_found");
  error.status = 404;
  return error;
}

export function buildAuthRoutes({ auth, request, response, readJson, operatorSummaryToken }) {
  return {
    "POST /api/auth/login": async () => {
      if (!auth) throw routeNotFound();
      return auth.login(await readJson(request), { request, response });
    },
    "POST /api/auth/operator-login": async () => {
      if (!auth) throw routeNotFound();
      return auth.operatorLogin(await readJson(request), {
        request,
        response,
        expectedToken: operatorSummaryToken
      });
    },
    "POST /api/auth/logout": async () => {
      if (!auth) throw routeNotFound();
      await auth.requireSession(request, { requireCsrf: true });
      return auth.logout(request, response);
    },
    "GET /api/auth/me": async () => {
      if (!auth) throw routeNotFound();
      const session = await auth.requireSession(request);
      return {
        user: session.user,
        csrfToken: session.csrfToken
      };
    }
  };
}
