import { AlertCircle, ArrowLeft, Cloud, LockKeyhole } from "lucide-react";
import { useState, type FormEvent } from "react";

import type { ConsoleController } from "../app/use-console-controller.ts";
import { Button, Field } from "../components/ui/index.ts";

function PublicBrand() {
  return <a className="brand" href="/"><img alt="OPL Cloud" src="/opl-app-icon.png" /><strong>OPL Cloud</strong></a>;
}

export function PublicHome({ controller }: { controller: ConsoleController }) {
  return (
    <div className="access-page">
      <nav className="public-nav"><PublicBrand /><Button onClick={() => controller.navigate("/login")} variant="outline">Console 登录</Button></nav>
      <main className="access-main">
        <div>
          <p className="kicker">OPL Cloud Console</p>
          <h1>工作区、API 服务与账单，在一个权威控制面里。</h1>
          <p>管理员预配置账户后登录。公开注册、在线充值和浏览器端业务推导不属于当前产品边界。</p>
          <Button color="primary" onClick={() => controller.navigate("/login")}>进入 Console</Button>
        </div>
        <img alt="OPL Cloud" className="access-mark" src="/opl-app-icon.png" />
      </main>
    </div>
  );
}

export function LoginPage({ controller }: { controller: ConsoleController }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (email.trim() && password) void controller.submitLogin(email.trim(), password);
  };

  return (
    <main className="login-page">
      <button className="back-button" onClick={() => controller.navigate("/")}><ArrowLeft aria-hidden size={17} />返回</button>
      <section className="login-panel" aria-labelledby="login-heading">
        <div className="login-brand"><img alt="OPL Cloud" src="/opl-app-icon.png" /><div><strong id="login-heading">Console 登录</strong><span>管理员预配置账户</span></div></div>
        <form onSubmit={submit}>
          <Field autoComplete="email" label="邮箱" onChange={(event) => setEmail(event.currentTarget.value)} required type="email" value={email} />
          <Field autoComplete="current-password" label="密码" onChange={(event) => setPassword(event.currentTarget.value)} required type="password" value={password} />
          {controller.authError ? <p className="form-error" role="alert">{controller.authError}</p> : null}
          <Button busy={controller.authStatus === "checking"} color="primary" type="submit">登录</Button>
        </form>
      </section>
    </main>
  );
}

export function SessionRecovery({ controller }: { controller: ConsoleController }) {
  const failed = controller.authStatus === "error";
  return (
    <main className="message-page">
      {failed ? <AlertCircle aria-hidden size={28} /> : <span className="spinner" />}
      <h1>{failed ? "无法恢复登录" : "正在恢复登录"}</h1>
      <p>{failed ? controller.authError || "身份服务暂不可用。" : "正在从 Control Plane 读取当前 Session。"}</p>
      {failed ? <Button onClick={() => controller.navigate("/login")} variant="outline">重新登录</Button> : null}
    </main>
  );
}

export function ForbiddenPage({ controller }: { controller: ConsoleController }) {
  return (
    <main className="message-page">
      <LockKeyhole aria-hidden size={28} />
      <h1>无权访问</h1>
      <p>当前 Session 没有 operator 权限。</p>
      <Button onClick={() => controller.navigate("/console/overview")} variant="outline">返回概览</Button>
    </main>
  );
}

export function NotFoundPage({ controller }: { controller: ConsoleController }) {
  return (
    <main className="message-page">
      <Cloud aria-hidden size={28} />
      <h1>页面不存在</h1>
      <p>这个路由不属于已冻结的 Console 展示合同。</p>
      <Button onClick={() => controller.navigate(controller.session ? "/console/overview" : "/")} variant="outline">返回</Button>
    </main>
  );
}
