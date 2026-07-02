import React, { useState } from "react";
import { ArrowLeft, LogIn, ShieldCheck } from "lucide-react";

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
  const [email, setEmail] = useState("pi-demo@opl.local");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);

  async function submit(event) {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    try {
      const response = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email, password })
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "login_failed");
      onLogin(payload);
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  const mode = route?.path || "/login";
  if (mode !== "/login" && mode !== "/logout") {
    const title = {
      "/register": "注册",
      "/invite/accept": "接受邀请",
      "/email/verify": "邮箱验证",
      "/forgot-password": "忘记密码",
      "/reset-password": "重置密码",
      "/auth/callback": "SSO 回调"
    }[mode] || "账号";
    return (
      <div className="loginShell">
        <a className="backLink" href="/"><ArrowLeft size={16} /> OPL Cloud</a>
        <main className="loginPanel compactAuth">
          <div className="loginBrand">
            <div className="brandIcon">OPL</div>
            <div>
              <p className="eyebrow">Account</p>
              <h1>{title}</h1>
            </div>
          </div>
          <div className="emptyState">
            <strong>已预留路由</strong>
            <span>商业账号流程将接入身份服务。</span>
          </div>
          <a className="primaryLink" href="/login">返回登录</a>
        </main>
      </div>
    );
  }

  return (
    <div className="loginShell">
      <a className="backLink" href="/"><ArrowLeft size={16} /> OPL Cloud</a>
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
        <div className="authLinks">
          <a href="/forgot-password">忘记密码</a>
          <a href="/reset-password">重置密码</a>
        </div>
        <div className="securityNote">
          <ShieldCheck size={16} />
          <span>Secure cookie + CSRF</span>
        </div>
      </main>
    </div>
  );
}
