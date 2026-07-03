import React from "react";
import { ArrowRight, Database, Headphones, KeyRound, Server, ShieldCheck, WalletCards } from "lucide-react";

export default function HomePage({ session }) {
  const target = session ? "/console/overview" : "/login";
  return (
    <div className="publicShell">
      <header className="publicNav">
        <a className="wordmark" href="/">
          <span>OPL</span>
          <strong>OPL Console</strong>
        </a>
        <nav className="publicLinks">
          <a href="/console/workspaces">Workspace</a>
          <a href="/console/billing">Billing</a>
          <a href="/console/support">Support</a>
          <a className="navButton" href={target}>{session ? "打开 Console" : "登录"}</a>
        </nav>
      </header>

      <main>
        <section className="publicConsole">
          <div className="publicConsoleCopy">
            <p className="eyebrow">OPL Console</p>
            <h1>OPL Console</h1>
            <p>开通 Workspace，分发访问 URL，按计算、存储和 Gateway 请求扣费。</p>
            <a className="primaryLink" href={target}>进入控制台 <ArrowRight size={16} /></a>
          </div>

          <div className="publicConsolePanel" aria-label="OPL Console product surface">
            <div className="publicPanelTop">
              <strong>Business chain</strong>
              <span>Live Console</span>
            </div>
            <div className="publicMetrics">
              <PublicMetric icon={<WalletCards />} label="Wallet" value="Balance + holds" />
              <PublicMetric icon={<Server />} label="Workspace" value="Compute + storage" />
              <PublicMetric icon={<KeyRound />} label="URL" value="Scoped access" />
              <PublicMetric icon={<Database />} label="Ledger" value="Usage evidence" />
            </div>
            <div className="publicFlow">
              <span>Top up</span>
              <span>Create</span>
              <span>Share URL</span>
              <span>Meter</span>
            </div>
          </div>
        </section>

        <section className="homeBand">
          <article>
            <ShieldCheck />
            <h2>Lab Owner</h2>
            <p>余额、Workspace、URL、工单。</p>
          </article>
          <article>
            <WalletCards />
            <h2>Billing</h2>
            <p>充值、冻结、小时扣费。</p>
          </article>
          <article>
            <Headphones />
            <h2>Admin</h2>
            <p>用户、充值、运行证据。</p>
          </article>
        </section>
      </main>
    </div>
  );
}

function PublicMetric({ icon, label, value }) {
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
