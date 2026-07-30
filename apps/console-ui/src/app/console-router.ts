import { useCallback, useEffect, useState } from "react";

function normalizePath(pathname: string) {
  const normalized = pathname.startsWith("/console/gateway")
    ? pathname.replace("/console/gateway", "/console/api")
    : pathname;
  return normalized.length > 1 ? normalized.replace(/\/+$/, "") : normalized;
}

export function useConsoleRouter() {
  const [path, setPath] = useState(() => normalizePath(window.location.pathname));

  useEffect(() => {
    const onPopState = () => setPath(normalizePath(window.location.pathname));
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const navigate = useCallback((next: string, replace = false) => {
    const normalized = normalizePath(next);
    if (replace) window.history.replaceState({}, "", normalized);
    else window.history.pushState({}, "", normalized);
    setPath(normalizePath(window.location.pathname));
  }, []);

  return { path, navigate };
}

export function isKnownConsoleRoute(path: string) {
  if (["/", "/login", "/403"].includes(path)) return true;
  if (["/console", "/console/overview", "/console/workspaces", "/console/workspaces/new", "/console/api", "/console/api/usage", "/console/api/keys", "/console/billing", "/console/announcements"].includes(path)) return true;
  if (/^\/console\/workspaces\/[^/]+$/.test(path)) return true;
  return ["/admin", "/admin/overview", "/admin/accounts", "/admin/billing", "/admin/resources", "/admin/system", "/admin/announcements"].includes(path);
}

export function isSensitiveConsoleRoute(path: string) {
  return path.startsWith("/console/api") || path.startsWith("/console/workspaces");
}
