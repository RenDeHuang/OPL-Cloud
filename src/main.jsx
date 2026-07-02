import React, { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";
import {
  commercialLoginMethods,
  landingFeatureCards,
  publicNavigationItems,
  unauthenticatedViewForPath
} from "./console-model.js";

const ConsoleApp = lazy(() => import("./console-app.jsx"));

async function loginRequest(values) {
  const response = await fetch("/api/auth/login", {
    method: "POST",
    credentials: "same-origin",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(values)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "login_failed");
  const csrfToken = response.headers.get("x-opl-csrf-token") || payload.csrfToken || "";
  if (csrfToken) globalThis.window.__OPL_CSRF_TOKEN = csrfToken;
  return payload;
}

async function sessionRequest() {
  const response = await fetch("/api/session", { credentials: "same-origin" });
  if (response.status === 401) return null;
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "session_failed");
  const csrfToken = response.headers.get("x-opl-csrf-token") || payload.csrfToken || "";
  if (csrfToken) globalThis.window.__OPL_CSRF_TOKEN = csrfToken;
  return payload;
}

function FeatureIcon({ title }) {
  const type = title.includes("billing") ? "billing" : title.includes("runtime") ? "runtime" : "workspace";
  return <span className={`cssFeatureIcon ${type}`} aria-hidden="true" />;
}

function PublicHeader({ onLoginClick, onHomeClick }) {
  return (
    <header className="publicHeader">
      <a
        className="publicBrand"
        href="/"
        onClick={(event) => {
          event.preventDefault();
          onHomeClick?.();
        }}
      >
        <span className="brandMark">OPL</span>
        <span>OPL Cloud</span>
      </a>
      <nav className="publicNav" aria-label="Public navigation">
        {publicNavigationItems.map((item) => (
          <a
            key={item.path}
            href={item.path}
            onClick={(event) => {
              event.preventDefault();
              item.path === "/login" ? onLoginClick?.() : onHomeClick?.();
            }}
          >
            {item.label}
          </a>
        ))}
      </nav>
    </header>
  );
}

function PublicHome({ onLoginClick, onHomeClick }) {
  return (
    <div className="publicShell">
      <PublicHeader onLoginClick={onLoginClick} onHomeClick={onHomeClick} />
      <main>
        <section className="homeHero">
          <div className="homeHeroPreview" aria-hidden="true">
            <div className="previewTopbar">
              <span />
              <span />
              <span />
            </div>
            <div className="previewGrid">
              <div className="previewSidebar">
                <span />
                <span />
                <span />
                <span />
              </div>
              <div className="previewMain">
                <div className="previewStats">
                  <span />
                  <span />
                  <span />
                </div>
                <div className="previewTable">
                  <span />
                  <span />
                  <span />
                  <span />
                </div>
              </div>
            </div>
          </div>
          <div className="homeHeroContent">
            <span className="homeTag">Commercial pilot console</span>
            <h1>OPL Cloud</h1>
            <p>
              Give each PI a private OPL Workspace with prepaid balance control, managed runtime, and clean audit receipts.
            </p>
            <div className="homeProofs">
              <span><i className="cssCheckIcon" aria-hidden="true" /> PI-safe console</span>
              <span><i className="cssCheckIcon" aria-hidden="true" /> Manual recharge</span>
              <span><i className="cssCheckIcon" aria-hidden="true" /> Audit receipts</span>
            </div>
            <div className="publicActions">
              <button className="publicButton primary" type="button" onClick={onLoginClick}>
                Login to Console <i className="cssArrowIcon" aria-hidden="true" />
              </button>
              <a
                className="publicButton"
                href="https://github.com/gaofeng21cn/one-person-lab-cloud/blob/main/README.zh-CN.md"
                target="_blank"
                rel="noreferrer"
              >
                OPL modules
              </a>
            </div>
          </div>
        </section>

        <section className="homeFeatures" aria-label="OPL Cloud capabilities">
          {landingFeatureCards.map((feature) => (
            <div className="homeFeature" key={feature.title}>
              <div className="homeFeatureIcon"><FeatureIcon title={feature.title.toLowerCase()} /></div>
              <h3>{feature.title}</h3>
              <p>{feature.description}</p>
            </div>
          ))}
        </section>
      </main>
    </div>
  );
}

function LoginPage({ error, loading, onLogin, onBackHome }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  return (
    <div className="publicShell loginPublicShell">
      <PublicHeader onLoginClick={() => {}} onHomeClick={onBackHome} />
      <main className="loginShell">
        <section className="loginPanel">
          <div className="loginTitle">
            <span className="brandMark">OPL</span>
            <div>
              <h1>Login to OPL Console</h1>
              <p>{commercialLoginMethods[0].label}</p>
            </div>
          </div>
          {error && <div className="loginError">{error}</div>}
          <form
            className="loginForm"
            onSubmit={(event) => {
              event.preventDefault();
              onLogin({ email, password });
            }}
          >
            <label className="formField">
              <span>Email</span>
              <input
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
            </label>
            <label className="formField">
              <span>Password</span>
              <input
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </label>
            <button className="publicButton primary fullWidth" type="submit" disabled={loading}>
              {loading ? "Signing in..." : "Sign in"}
            </button>
          </form>
        </section>
      </main>
    </div>
  );
}

function App() {
  const [session, setSession] = useState(null);
  const [mode, setMode] = useState("checking");
  const [publicView, setPublicView] = useState(() => unauthenticatedViewForPath(globalThis.window?.location?.pathname || "/"));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const onSignedOut = useMemo(() => () => {
    setSession(null);
    setMode("public");
    setPublicView("home");
    setError("");
    globalThis.window?.history.replaceState({}, "", "/");
  }, []);

  function showPublicView(nextView) {
    const nextPath = nextView === "login" ? "/login" : "/";
    setPublicView(nextView);
    globalThis.window?.history.pushState({}, "", nextPath);
  }

  useEffect(() => {
    sessionRequest()
      .then((nextSession) => {
        if (nextSession) {
          setSession(nextSession);
          setMode("console");
          return;
        }
        setMode("public");
        setPublicView(unauthenticatedViewForPath(globalThis.window?.location?.pathname || "/"));
      })
      .catch((err) => {
        setMode("public");
        setError(err.message);
      });
  }, []);

  async function login(values) {
    try {
      setLoading(true);
      setError("");
      const nextSession = await loginRequest(values);
      setSession(nextSession);
      setMode("console");
      globalThis.window?.history.replaceState({}, "", "/");
    } catch (err) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  if (mode === "checking") return <div className="loadingScreen">Loading OPL Cloud...</div>;

  if (mode === "console") {
    return (
      <Suspense fallback={<div className="loadingScreen">Loading OPL Console...</div>}>
        <ConsoleApp initialSession={session} onSignedOut={onSignedOut} />
      </Suspense>
    );
  }

  if (publicView === "login") {
    return (
      <LoginPage
        error={error}
        loading={loading}
        onLogin={login}
        onBackHome={() => showPublicView("home")}
      />
    );
  }

  return <PublicHome onLoginClick={() => showPublicView("login")} onHomeClick={() => showPublicView("home")} />;
}

createRoot(document.getElementById("root")).render(<App />);
