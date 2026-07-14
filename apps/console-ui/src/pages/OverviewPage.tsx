import React from "react";
import { Button, Empty } from "antd";
import { Cable, HardDrive, Headphones, Link as LinkIcon, Plus, Server, Settings2, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  StatusPill
} from "./shared/commercial-console.tsx";
import {
  statusColor,
  statusLabel,
  usdBalance,
  workspaceAccessLabel,
  workspaceAccessTone,
  workspaceUrlReady
} from "./shared/formatters.ts";

export function OverviewPage({ state, balance, tickets }: any) {
  const activeTickets = tickets.tickets.filter((ticket) => ticket.status !== "closed").length;
  const workspaces = state.workspaces || [];
  const computeResources = (state.computeAllocations || []).filter((item) => item.status !== "destroyed");
  const storageResources = (state.storageVolumes || []).filter((item) => item.status !== "destroyed");
  const latestWorkspaces = workspaces.slice(-5).reverse();
  const computeCount = computeResources.filter((item) => item.status !== "failed").length;
  const storageCount = storageResources.length;
  const attachmentCount = (state.storageAttachments || []).filter((item) => item.status === "attached").length;
  const activeWorkspaceCount = workspaces.filter((item) => item.openable).length;
  const activeEntitlements = [...computeResources, ...storageResources].filter((item) => item.billingStatus === "active").length;

  return (
    <ConsoleSurface
      title="概览"
      eyebrow="OPL Console"
      subtitle="工作区、余额与支持"
      extra={(
        <ActionGroup actions={[
          { label: "创建工作区", type: "primary", icon: <Plus size={15} />, onClick: () => navigate(routeTo("workspace.create")) },
          { label: "费用明细", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) },
          { label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(routeTo("support.create")) }
        ]} />
      )}
    >
      <MetricStrip
        items={[
          { label: "计算节点", value: computeCount, caption: "可运行工作空间", icon: <Server size={16} />, tone: computeCount ? "info" : "neutral" },
          { label: "云硬盘", value: storageCount, caption: "可挂载存储", icon: <HardDrive size={16} />, tone: storageCount ? "info" : "neutral" },
          { label: "工作空间", value: workspaces.length, caption: `${activeWorkspaceCount} 个可打开`, icon: <Plus size={16} />, tone: workspaces.length ? "info" : "neutral" },
          { label: "Sub2API 余额", value: usdBalance(balance), caption: "gflabtoken.cn · USD", icon: <WalletCards size={16} />, tone: Number(balance.usdMicros || 0) > 0 ? "good" : "warn" },
          { label: "月度权益", value: activeEntitlements, caption: "已付费资源", tone: activeEntitlements ? "good" : "neutral" },
          { label: "工单", value: activeTickets, caption: `共 ${tickets.tickets.length} 个`, icon: <Headphones size={16} />, tone: activeTickets ? "warn" : "good" }
        ]}
      />

      <InsightPanel title="开通流程" eyebrow="业务链">
        <div className="businessChain">
          {[
            { label: "余额", value: usdBalance(balance), meta: "Sub2API USD", action: "费用明细", icon: <WalletCards size={14} />, route: routeTo("billing.overview") },
            { label: "计算", value: `${computeCount} 个`, meta: "先开通计算", action: "开通计算", icon: <Server size={14} />, route: routeTo("compute-allocations.create") },
            { label: "存储", value: `${storageCount} 个`, meta: "再开通存储", action: "开通存储", icon: <HardDrive size={14} />, route: routeTo("storage.create") },
            { label: "挂载", value: `${attachmentCount} 个`, meta: "把存储挂到计算", action: "挂载存储", icon: <Cable size={14} />, route: routeTo("attachment.create") },
            { label: "工作区", value: `${activeWorkspaceCount} 个`, meta: "生成 URL 后打开", action: "创建工作区", icon: <Plus size={14} />, route: routeTo("workspace.create") },
            { label: "权益/工单", value: `${activeEntitlements} 项`, meta: "包月资源和支持", action: "提交工单", icon: <Headphones size={14} />, route: routeTo("support.create") }
          ].map((item, index, list) => (
            <React.Fragment key={item.label}>
              <article className="businessStep">
                <span>{item.label}</span>
                <strong>{item.value}</strong>
                <small>{item.meta}</small>
                <Button size="small" icon={item.icon} onClick={() => navigate(item.route)}>{item.action}</Button>
              </article>
              {index < list.length - 1 && <span className="businessArrow">→</span>}
            </React.Fragment>
          ))}
        </div>
      </InsightPanel>

      <InsightPanel title="工作区列表" eyebrow="最近访问">
        {latestWorkspaces.length ? (
          <div className="overviewWorkspaceList">
            {latestWorkspaces.map((workspace) => (
              <article className="overviewWorkspaceRow" key={workspace.id}>
                <div className="overviewWorkspaceMain">
                  <strong>{workspace.name}</strong>
                  <small>{workspace.id}</small>
                </div>
                <div className="overviewWorkspaceStatus">
                  <StatusPill label={statusLabel(workspace)} tone={statusColor(workspace.state) === "green" ? "good" : statusColor(workspace.state) === "red" ? "danger" : "warn"} />
                  <StatusPill label={workspaceAccessLabel(workspace)} tone={workspaceAccessTone(workspace)} />
                </div>
                <div className="overviewWorkspaceActions">
                  <Button icon={<LinkIcon size={14} />} disabled={!workspaceUrlReady(workspace)} onClick={() => window.open(workspace.url, "_blank", "noopener,noreferrer")}>{workspaceUrlReady(workspace) ? "打开" : "分发中"}</Button>
                  <Button icon={<Settings2 size={14} />} onClick={() => navigate(routeTo("workspace.detail", { id: workspace.id }))}>详情</Button>
                </div>
              </article>
            ))}
          </div>
        ) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无工作区" />}
      </InsightPanel>
    </ConsoleSurface>
  );
}
