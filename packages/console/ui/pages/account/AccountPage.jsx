import React from "react";
import { Cable, CreditCard, HardDrive, Headphones, KeyRound, Server, ShieldCheck, UserRound, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.js";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit,
  TimelineList
} from "../shared/commercial-console.jsx";
import { available, money } from "../shared/formatters.js";

export function AccountPage({ state, wallet, session }) {
  return (
    <ConsoleSurface title="配置" eyebrow="身份与二级入口" subtitle="账号、钱包归属、实验室策略，以及不占左栏的运维入口">
      <MetricStrip
        items={[
          { label: "角色", value: session.user.role === "admin" ? "管理员" : "实验室负责人", caption: "权限边界", icon: <ShieldCheck size={16} />, tone: session.user.role === "admin" ? "info" : "good" },
          { label: "账号", value: state.account.id, caption: "计费账号", icon: <UserRound size={16} />, tone: "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: `${money(wallet.frozen)} 已冻结`, icon: <WalletCards size={16} />, tone: available(wallet) > 0 ? "good" : "warn" },
          { label: "工作区入口", value: state.workspaces.length, caption: "当前账号", tone: "info" },
          { label: "工单", value: state.supportTickets?.length || 0, caption: "账号范围", tone: "neutral" }
        ]}
      />

      <div className="consoleGrid equal">
        <InsightPanel title="账户身份" eyebrow="用户">
          <ResourceSplit
            items={[
              { label: "邮箱", value: session.user.email, meta: "登录身份", status: "可用", tone: "good" },
              { label: "角色", value: session.user.role === "admin" ? "管理员" : "实验室负责人", meta: "路由和操作范围", status: "已限定", tone: "info" },
              { label: "账号", value: state.account.id, meta: "钱包归属", status: "计费", tone: "neutral" },
              { label: "会话", value: "安全", meta: "Cookie + CSRF", status: "已保护", tone: "good" }
            ]}
          />
        </InsightPanel>

        <InsightPanel title="二级入口" eyebrow="配置">
          <ActionGroup
            actions={[
              { label: "账单", icon: <CreditCard size={15} />, onClick: () => navigate(routeTo("billing.overview")) },
              { label: "计算资源", icon: <Server size={15} />, onClick: () => navigate(routeTo("compute-allocations.list")) },
              { label: "存储资源", icon: <HardDrive size={15} />, onClick: () => navigate(routeTo("storage.list")) },
              { label: "挂载关系", icon: <Cable size={15} />, onClick: () => navigate(routeTo("attachment.list")) },
              { label: "网关", icon: <KeyRound size={15} />, onClick: () => navigate(routeTo("gateway.external")) },
              { label: "工单", icon: <Headphones size={15} />, onClick: () => navigate(routeTo("support.list")) }
            ]}
          />
        </InsightPanel>
      </div>

      <div className="consoleGrid equal">
        <InsightPanel title="实验室策略" eyebrow="策略">
          <TimelineList
            items={[
              { title: "OPL Workspace 即 UI 子账号", description: "先不做复杂子账号体系，工作区承担 URL、账号、密码展示", meta: "Workspace", tone: "good" },
              { title: "左栏保持三项", description: "概览、工作区、配置；其他能力作为二级入口出现", meta: "导航", tone: "info" },
              { title: "按 Workspace 计费", description: "费用在概览和 Workspace 详情中解释，账单页保留为二级入口", meta: "计费", tone: "warn" },
              { title: "资源链不消失", description: "计算、存储、挂载仍可维护，但不是老板第一眼看到的对象", meta: "运维", tone: "info" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="当前口径" eyebrow="Workspace">
          <ResourceSplit
            items={[
              { label: "一级导航", value: "3 项", meta: "概览 / 工作区 / 配置", status: "已简化", tone: "good" },
              { label: "Workspace", value: state.workspaces.length, meta: "等同 UI 子账号", status: "计费对象", tone: "info" },
              { label: "钱包", value: money(available(wallet)), meta: `${money(wallet.frozen)} 已冻结`, status: "可用", tone: available(wallet) > 0 ? "good" : "warn" },
              { label: "账号", value: state.account.id, meta: "账本归属", status: "当前", tone: "neutral" }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
