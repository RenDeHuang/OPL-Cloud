import React from "react";
import { ArrowRight, Database, Headphones, KeyRound, Server, ShieldCheck, WalletCards } from "lucide-react";
import OplAppLogo from "./shared/OplAppLogo.tsx";

export default function HomePage({ session }: any) {
  const target = session ? "/console/overview" : "/login";
  return (
    <div className="publicShell">
      <header className="publicNav">
        <a className="wordmark" href="/">
          <OplAppLogo />
          <strong>OPL Console</strong>
        </a>
        <nav className="publicLinks">
          <a href="/console/workspaces">工作区</a>
          <a href="/console/billing">账单</a>
          <a href="/console/support">支持</a>
          <a className="navButton" href={target}>{session ? "打开 Console" : "登录"}</a>
        </nav>
      </header>

      <main>
        <section className="publicConsole">
          <div className="publicConsoleCopy">
            <p className="eyebrow">OPL Console</p>
            <h1>OPL Console</h1>
            <p>创建工作区，查看余额、费用明细和支持记录。</p>
            <a className="primaryLink" href={target}>进入控制台 <ArrowRight size={16} /></a>
          </div>

          <div className="publicConsolePanel" aria-label="OPL Console product surface">
            <div className="publicPanelTop">
              <strong>业务链</strong>
              <span>生产控制台</span>
            </div>
            <div className="publicMetrics">
              <PublicMetric icon={<WalletCards />} label="钱包" value="余额与冻结" />
              <PublicMetric icon={<Server />} label="工作区" value="计算与存储" />
              <PublicMetric icon={<KeyRound />} label="访问入口" value="授权 URL" />
              <PublicMetric icon={<Database />} label="账本" value="用量证据" />
            </div>
            <div className="publicFlow">
              <span>充值</span>
              <span>开通</span>
              <span>分发 URL</span>
              <span>计费</span>
            </div>
          </div>
        </section>

        <section className="homeBand">
          <article>
            <ShieldCheck />
            <h2>用户</h2>
            <p>余额、工作区、URL、工单。</p>
          </article>
          <article>
            <WalletCards />
            <h2>账单</h2>
            <p>充值、冻结、小时扣费。</p>
          </article>
          <article>
            <Headphones />
            <h2>运维</h2>
            <p>用户、充值、资源状态。</p>
          </article>
        </section>
      </main>
    </div>
  );
}

function PublicMetric({ icon, label, value }: any) {
  return (
    <article className="publicMetric">
      {icon}
      <div>
        <strong>{label}</strong>
        <span>{value}</span>
      </div>
    </article>
  );
}
