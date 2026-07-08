import React from "react";
import { CreditCard, Headphones, Server, ShieldCheck, UserRound, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit
} from "../shared/commercial-console.tsx";
import { available, money } from "../shared/formatters.ts";

function roleLabel(role = "") {
  return role === "admin" ? "运维" : "用户";
}

export function AccountPage({ state, wallet, session }: any) {
  const displayRole = roleLabel(session.user.role);
  return (
    <ConsoleSurface title="账号" eyebrow="账号" subtitle="登录身份、权限范围和余额">
      <MetricStrip
        items={[
          { label: "权限", value: displayRole, caption: "当前登录", icon: <ShieldCheck size={16} />, tone: session.user.role === "admin" ? "info" : "good" },
          { label: "计费账号", value: state.account.id, caption: "余额归属", icon: <UserRound size={16} />, tone: "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: `${money(wallet.frozen)} 已冻结`, icon: <WalletCards size={16} />, tone: available(wallet) > 0 ? "good" : "warn" },
          { label: "工作区", value: state.workspaces.length, caption: "可打开", tone: "info" },
          { label: "工单", value: state.supportTickets?.length || 0, caption: "支持记录", tone: "neutral" }
        ]}
      />

      <div className="consoleGrid equal">
        <InsightPanel title="账号信息" eyebrow="用户">
          <ResourceSplit
            items={[
              { label: "邮箱", value: session.user.email, meta: "登录身份", status: "可用", tone: "good" },
              { label: "权限", value: displayRole, meta: "可使用的页面和操作", status: "已限定", tone: "info" },
              { label: "计费账号", value: state.account.id, meta: "余额归属", status: "当前", tone: "neutral" },
              { label: "登录状态", value: "已登录", meta: "会话已保护", status: "安全", tone: "good" }
            ]}
          />
        </InsightPanel>

        <InsightPanel title="账户操作" eyebrow="支持">
          <ActionGroup
            actions={[
              { label: "工作区", icon: <Server size={15} />, onClick: () => navigate(routeTo("workspace.list")) },
              { label: "费用明细", icon: <CreditCard size={15} />, onClick: () => navigate(routeTo("billing.overview")) },
              { label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(routeTo("support.create")) }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
