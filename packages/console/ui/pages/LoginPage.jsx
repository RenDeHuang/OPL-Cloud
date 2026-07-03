import React, { useState } from "react";
import { ArrowLeft, LogIn, ShieldCheck, UserRound } from "lucide-react";
import { login } from "../api/auth-api.js";
import { runtimeConfig } from "../config/runtime-config.js";

function errorLabel(value) {
  const labels = {
    login_failed: "登录失败",
    invalid_credentials: "邮箱或密码不正确",
    not_authenticated: "请先登录",
    csrf_token_invalid: "页面安全令牌已失效，请重新登录"
  };
  return labels[value] || value;
}

export default function LoginPage({ route, onLogin }) {
  const config = runtimeConfig();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      const payload = await login({ email, password });
      onLogin(payload);
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  const mode = route?.path || "/login";
  if (mode !== "/login" && mode !== "/logout") {
    return (
      <div className="loginShell">
        <a className="backLink" href="/"><ArrowLeft size={16} /> OPL Console</a>
        <main className="loginPanel compactAuth">
          <div className="loginBrand">
            <div className="brandIcon">OPL</div>
            <div>
              <p className="eyebrow">OPL Console</p>
              <h1>无法访问</h1>
            </div>
          </div>
          <div className="emptyState">
            <strong>当前入口不可用</strong>
            <span>请使用已开通的 Console 账号登录。</span>
          </div>
          <a className="primaryLink" href="/login">返回登录</a>
        </main>
      </div>
    );
  }

  return (
    <div className="loginShell">
      <a className="backLink" href="/"><ArrowLeft size={16} /> OPL Console</a>
      <main className="loginPanel">
        <div className="loginBrand">
          <div className="brandIcon">OPL</div>
          <div>
            <p className="eyebrow">OPL Console</p>
            <h1>登录</h1>
          </div>
        </div>
        <form onSubmit={submit}>
          <label>
            邮箱
            <input value={email} onChange={(event) => setEmail(event.target.value)} type="email" autoComplete="email" required />
          </label>
          <label>
            密码
            <input value={password} onChange={(event) => setPassword(event.target.value)} type="password" autoComplete="current-password" required />
          </label>
          {error && <div className="error">{errorLabel(error)}</div>}
          <button className="primary wide" disabled={submitting}>
            <LogIn size={16} /> {submitting ? "登录中..." : "登录"}
          </button>
        </form>
        {config.demoAccounts.length > 0 && (
          <div className="demoLoginPanel" aria-label="Demo accounts">
            {config.demoAccounts.map((account) => (
              <button
                key={account.email}
                type="button"
                onClick={() => {
                  setEmail(account.email);
                  setPassword(account.password);
                  setError("");
                }}
              >
                <UserRound size={15} />
                <span>{account.label}</span>
              </button>
            ))}
          </div>
        )}
        <div className="securityNote">
          <ShieldCheck size={16} />
          <span>Secure cookie + CSRF</span>
        </div>
      </main>
    </div>
  );
}
