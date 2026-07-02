import React from "react";
import { ArrowRight, CheckCircle2, Cloud, Database, ShieldCheck, WalletCards } from "lucide-react";

export default function HomePage({ session }) {
  return (
    <div className="publicShell">
      <header className="publicNav">
        <a className="wordmark" href="/">
          <span>OPL</span>
          <strong>OPL Cloud</strong>
        </a>
        <nav className="publicLinks">
          <a href="/pricing">套餐</a>
          <a href="/docs">文档</a>
          <a href="/status">状态</a>
          <a className="navButton" href={session ? "/console/overview" : "/login"}>{session ? "打开 Console" : "登录"}</a>
        </nav>
      </header>

      <main>
        <section className="homeHero">
          <div className="heroCopy">
            <p className="eyebrow">OPL Cloud</p>
            <h1>OPL Cloud</h1>
            <p>创建 OPL Workspace，分发 URL，按资源用量计费。</p>
            <div className="heroActions">
              <a className="primaryLink" href={session ? "/console/overview" : "/login"}>打开 OPL Console <ArrowRight size={16} /></a>
              <a className="secondaryLink" href="/pricing">查看套餐</a>
            </div>
          </div>
          <div className="heroPreview" aria-label="OPL Console 预览">
            <div className="previewTop">
              <span />
              <strong>Workspace delivery</strong>
            </div>
            <div className="chainPreview">
              <PreviewStep icon={<WalletCards />} title="钱包" text="余额与冻结" />
              <PreviewStep icon={<Cloud />} title="OPL Fabric" text="计算与存储" />
              <PreviewStep icon={<ShieldCheck />} title="Workspace URL" text="复制与分发" />
              <PreviewStep icon={<Database />} title="OPL Ledger" text="账单与回执" />
            </div>
          </div>
        </section>

        <section className="homeBand">
          <article>
            <CheckCircle2 />
            <h2>Console</h2>
            <p>工作区、账号、账单。</p>
          </article>
          <article>
            <CheckCircle2 />
            <h2>Fabric</h2>
            <p>计算、存储、环境。</p>
          </article>
          <article>
            <CheckCircle2 />
            <h2>Ledger</h2>
            <p>用量、审计、回执。</p>
          </article>
        </section>
      </main>
    </div>
  );
}

function PreviewStep({ icon, title, text }) {
  return (
    <article className="previewStep">
      {icon}
      <div>
        <strong>{title}</strong>
        <span>{text}</span>
      </div>
    </article>
  );
}
