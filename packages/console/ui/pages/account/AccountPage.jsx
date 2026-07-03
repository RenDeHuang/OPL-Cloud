import React from "react";
import { ShieldCheck, UserRound, WalletCards } from "lucide-react";
import {
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit,
  TimelineList
} from "../shared/commercial-console.jsx";
import { available, money } from "../shared/formatters.js";

export function AccountPage({ state, wallet, session }) {
  return (
    <ConsoleSurface title="Account & Lab" eyebrow="Identity" subtitle="User, wallet owner, lab policy">
      <MetricStrip
        items={[
          { label: "角色", value: session.user.role === "admin" ? "Admin" : "Lab Owner", caption: "permission boundary", icon: <ShieldCheck size={16} />, tone: session.user.role === "admin" ? "info" : "good" },
          { label: "账号", value: state.account.id, caption: "billing account", icon: <UserRound size={16} />, tone: "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: `${money(wallet.frozen)} frozen`, icon: <WalletCards size={16} />, tone: available(wallet) > 0 ? "good" : "warn" },
          { label: "Workspace", value: state.workspaces.length, caption: "owned by this account", tone: "info" },
          { label: "工单", value: state.supportTickets?.length || 0, caption: "account scoped", tone: "neutral" }
        ]}
      />

      <div className="consoleGrid equal">
        <InsightPanel title="账户身份" eyebrow="User">
          <ResourceSplit
            items={[
              { label: "邮箱", value: session.user.email, meta: "login identity", status: "active", tone: "good" },
              { label: "角色", value: session.user.role === "admin" ? "Admin" : "Lab Owner", meta: "route and action scope", status: "scoped", tone: "info" },
              { label: "账号", value: state.account.id, meta: "wallet owner", status: "billing", tone: "neutral" },
              { label: "会话", value: "Secure", meta: "cookie + CSRF", status: "protected", tone: "good" }
            ]}
          />
        </InsightPanel>

        <InsightPanel title="实验室策略" eyebrow="Policy">
          <TimelineList
            items={[
              { title: "Workspace URL 可分发", description: "每个 Workspace 使用独立 URL token", meta: "access", tone: "good" },
              { title: "计算和存储分开管理", description: "先开通资源，再挂载存储并创建 Workspace URL", meta: "lifecycle", tone: "info" },
              { title: "7 天资源预冻结", description: "开通前冻结计算和存储预算", meta: "billing", tone: "warn" },
              { title: "账单按小时解释", description: "Wallet、Usage、Ledger 保持同一账户口径", meta: "ledger", tone: "info" }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
