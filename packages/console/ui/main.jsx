import React, { Suspense, lazy, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "antd/dist/reset.css";
import "./styles.css";
import { currentSession } from "./api/auth-api.js";
import { findRoute, navigate, routeTo } from "./consoleRoutes.js";

const HomePage = lazy(() => import("./pages/HomePage.jsx"));
const LoginPage = lazy(() => import("./pages/LoginPage.jsx"));
const ConsolePage = lazy(() => import("./pages/ConsolePage.jsx"));

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
  const [session, setSession] = useState(null);
  const [authChecked, setAuthChecked] = useState(false);

  useEffect(() => {
    const onRouteChange = () => setRoute(currentRoute());
    window.addEventListener("popstate", onRouteChange);
    return () => window.removeEventListener("popstate", onRouteChange);
  }, []);

  useEffect(() => {
    let cancelled = false;
    currentSession()
      .then((payload) => {
        if (cancelled) return;
        if (payload?.user) setSession(payload);
      })
      .finally(() => {
        if (!cancelled) setAuthChecked(true);
      });
    return () => {
      cancelled = true;
      };
  }, []);

  useEffect(() => {
    if (!authChecked) return;
    if (route.redirect) {
      navigate(route.redirect);
      return;
    }
    if (route.requiresAuth && !session) {
      redirectToLogin(window.location.pathname);
      return;
    }
    if (route.requiresAdmin && session?.user?.role !== "admin") {
      navigate(routeTo("error.forbidden"));
    }
  }, [authChecked, route, session]);

  const page = useMemo(() => {
    if (route.area === "auth" && route.path !== "/logout") {
      return <LoginPage route={route} onLogin={(payload) => {
        setSession(payload);
        navigate(authRedirectTarget());
      }} />;
    }
    if (route.path === "/logout") {
      return <LoginPage route={route} onLogin={(payload) => {
        setSession(payload);
        navigate(routeTo("console.overview"));
      }} />;
    }
    if (route.area === "console" || route.area === "admin") {
      if (!session) return null;
      return <ConsolePage route={route} session={session} onLogout={() => setSession(null)} />;
    }
    return <HomePage route={route} session={session} />;
  }, [route, session]);

  if (!authChecked) return <div className="loading">Loading OPL Cloud...</div>;

  return (
    <Suspense fallback={<div className="loading">Loading OPL Cloud...</div>}>
      {page}
    </Suspense>
  );
}

createRoot(document.getElementById("root")).render(<App />);
