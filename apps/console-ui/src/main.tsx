import { Suspense, lazy, useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import { currentSession } from "./api/auth-api.ts";
import { findRoute, navigate, routeTo } from "./consoleRoutes.ts";

const HomePage = lazy(() => import("./pages/HomePage.tsx"));
const LoginPage = lazy(() => import("./pages/LoginPage.tsx"));
const ConsolePage = lazy(() => import("./pages/ConsolePage.tsx"));

function currentRoute() {
  return findRoute(window.location.pathname);
}

function redirectToLogin(pathname) {
  const redirect = encodeURIComponent(pathname || "/console/overview");
  navigate(`${routeTo("auth.login")}?redirect=${redirect}`);
}

function authRedirectTarget() {
  const params = new URLSearchParams(window.location.search);
  const redirect = params.get("redirect");
  return redirect && redirect.startsWith("/") ? redirect : routeTo("console.overview");
}

function App() {
  const [route, setRoute] = useState(currentRoute());
  const [session, setSession] = useState<any>(null);
  const [authStatus, setAuthStatus] = useState("checking");
  const [authError, setAuthError] = useState("");

  const checkSession = useCallback(async () => {
    setAuthStatus("checking");
    setAuthError("");
    try {
      const payload = await currentSession();
      setSession(payload?.user ? payload : null);
      setAuthStatus("ready");
    } catch (err) {
      setAuthError(err.message || "session_check_failed");
      setAuthStatus("error");
    }
  }, []);

  useEffect(() => {
    const onRouteChange = () => setRoute(currentRoute());
    window.addEventListener("popstate", onRouteChange);
    return () => window.removeEventListener("popstate", onRouteChange);
  }, []);

  useEffect(() => {
    void checkSession();
  }, [checkSession]);

  useEffect(() => {
    if (authStatus !== "ready") return;
    if (route.redirect) {
      navigate(route.redirect);
      return;
    }
    if (route.requiresAuth && !session) {
      redirectToLogin(window.location.pathname + window.location.search + window.location.hash);
      return;
    }
    if (route.requiresAdmin && session?.isOperator !== true) {
      navigate(routeTo("error.forbidden"));
    }
  }, [authStatus, route, session]);

  const page = useMemo(() => {
    if (route.area === "auth" && route.path !== "/logout") {
      return <LoginPage route={route} onLogin={(payload) => {
        setSession(payload);
        setAuthStatus("ready");
        navigate(authRedirectTarget());
      }} />;
    }
    if (route.path === "/logout") {
      return <LoginPage route={route} onLogin={(payload) => {
        setSession(payload);
        setAuthStatus("ready");
        navigate(routeTo("console.overview"));
      }} />;
    }
    if (route.area === "console" || route.area === "admin") {
      if (!session) return null;
      return <ConsolePage route={route} session={session} onLogout={() => setSession(null)} />;
    }
    return <HomePage route={route} session={session} />;
  }, [route, session]);

  if (authStatus === "checking") return <div className="loading" role="status" aria-live="polite">正在验证登录...</div>;

  if (authStatus === "error") {
    return (
      <div className="loading">
        <div className="loadFailure" role="alert">
          <strong>无法验证登录状态</strong>
          <span>{authError}</span>
          <button className="primary" type="button" onClick={() => void checkSession()}>重试</button>
        </div>
      </div>
    );
  }

  return (
    <Suspense fallback={<div className="loading" role="status" aria-live="polite">正在加载 Console 界面...</div>}>
      {page}
    </Suspense>
  );
}

createRoot(document.getElementById("root")!).render(<App />);
