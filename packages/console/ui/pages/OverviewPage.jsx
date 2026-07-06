import React from "react";
import { Button, Typography } from "antd";
import { AlertTriangle, HardDrive, Headphones, Link as LinkIcon, Plus, Server, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../consoleRoutes.js";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill,
  TimelineList
} from "./shared/commercial-console.jsx";
import { available, money, statusColor, statusLabel } from "./shared/formatters.js";

export function OverviewPage({ state, wallet, tickets }) {
  const needsAttention = state.notifications?.length || 0;
  const computeAllocations = state.computeAllocations || [];
  const storageVolumes = state.storageVolumes || [];
  const computeRunning = computeAllocations.filter((compute) => compute.status === "running").length;
  const storageAvailable = storageVolumes.filter((storage) => storage.status !== "destroyed").length;
  const activeTickets = tickets.tickets.filter((ticket) => ticket.status !== "closed").length;
  const usable = available(wallet);
  const freezeRatio = Number(wallet.balance || 0) > 0
    ? Math.min(100, Math.max(0, (Number(wallet.frozen || 0) / Number(wallet.balance || 1)) * 100))
    : 0;
  const latestWorkspaces = state.workspaces.slice(-5).reverse();
  const recentSignals = [
    ...(state.notifications || []).slice(-5).reverse().map((event) => ({
      title: event.type || "alert",
      description: event.workspaceId || event.accountId,
      meta: event.severity || "signal",
      tone: event.severity === "error" ? "danger" : "warn"
    })),
    ...tickets.tickets.slice(-3).reverse().map((ticket) => ({
      title: ticket.title,
      description: ticket.category,
      meta: ticket.status,
      tone: "info"
    }))
  ].slice(0, 6);

  return (
    <ConsoleSurface
      title="概览"
      eyebrow="OPL Console"
      subtitle="钱包账单、OPL Workspace、网关请求、工单"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建工作区入口</Button>}
    >
      <MetricStrip
        items={[
          { label: "可用余额", value: money(usable), caption: `${money(wallet.frozen)} 已冻结`, icon: <WalletCards size={16} />, tone: usable > 0 ? "good" : "warn" },
          { label: "工作区入口", value: state.workspaces.length, caption: `${computeRunning} 个计算运行中`, icon: <Server size={16} />, tone: computeRunning ? "good" : "neutral" },
          { label: "存储资源", value: storageAvailable, caption: "可保留数据盘", icon: <HardDrive size={16} />, tone: storageAvailable ? "info" : "neutral" },
          { label: "Gateway 请求", value: state.requestUsageLogs?.length || 0, caption: "gflabtoken.cn", icon: <LinkIcon size={16} />, tone: "info" },
          { label: "工单", value: activeTickets, caption: `共 ${tickets.tickets.length} 个`, icon: <Headphones size={16} />, tone: activeTickets ? "warn" : "neutral" },
          { label: "告警", value: needsAttention, caption: "用户可见", icon: <AlertTriangle size={16} />, tone: needsAttention ? "danger" : "good" }
        ]}
      />

      <div className="consoleGrid">
        <InsightPanel
          title="账单与工作区"
          eyebrow="概览"
          actions={<ActionGroup actions={[
            { label: "新建工作区", type: "primary", icon: <Plus size={15} />, onClick: () => navigate(routeTo("workspace.create")) },
            { label: "账单", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) },
            { label: "工单", icon: <Headphones size={15} />, onClick: () => navigate(routeTo("support.create")) }
          ]} />}
        >
          <ResourceSplit
            items={[
              { label: "充值与冻结", value: `${money(wallet.balance)} / ${money(wallet.frozen)}`, meta: "余额 / 冻结", status: `${Math.round(freezeRatio)}% 已冻结`, tone: freezeRatio > 70 ? "warn" : "info" },
              { label: "计算交付", value: `${computeRunning}/${computeAllocations.length}`, meta: "运行中的计算资源", status: computeRunning ? "可用" : "空闲", tone: computeRunning ? "good" : "neutral" },
              { label: "存储资源", value: storageAvailable, meta: "持久数据盘", status: storageAvailable ? "可用" : "空", tone: storageAvailable ? "good" : "neutral" },
              { label: "URL 分发", value: `${state.workspaces.filter((workspace) => workspace.access?.tokenStatus === "active").length}`, meta: "可用工作区入口", status: "当前账号", tone: "info" },
              { label: "支持闭环", value: activeTickets, meta: "处理中工单", status: activeTickets ? "处理中" : "清空", tone: activeTickets ? "warn" : "good" }
            ]}
          />
        </InsightPanel>

        <InsightPanel title="最近信号" eyebrow="关注">
          <TimelineList items={recentSignals} emptyText="当前没有告警或待处理工单" />
        </InsightPanel>
      </div>

      <InsightPanel title="最近工作区入口" eyebrow="交付">
        <ObjectTable
          rowKey="id"
          data={latestWorkspaces}
          emptyText="暂无工作区入口"
          columns={[
            {
              title: "名称",
              dataIndex: "name",
              render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("workspace.detail", { id: row.id }))}>{row.name}</Button>
            },
            {
              title: "状态",
              dataIndex: "state",
              render: (_, row) => <StatusPill label={statusLabel(row)} tone={statusColor(row.state) === "green" ? "good" : statusColor(row.state) === "red" ? "danger" : "warn"} />
            },
            { title: "URL", dataIndex: "url", render: (value) => <Typography.Text ellipsis className="inlineCode">{value}</Typography.Text> },
            { title: "挂载", render: (_, row) => row.attachmentId || "-" }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
