import React from "react";
import { Button, Empty, Typography } from "antd";
import { Cable, HardDrive, Link as LinkIcon, Plus, Server, Settings2, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { paidThrough, statusColor, statusLabel, usdBalance, workspaceAccessLabel, workspaceAccessTone, workspaceOpenActionLabel, workspaceUrlReady } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function workspaceCredential(workspace: AnyRecord = {}) {
  return workspace.access?.account || workspace.access?.username || workspace.login?.username || workspace.id || "-";
}

function workspaceEntitlements(state: AnyRecord, workspace: AnyRecord) {
  const computeId = workspace.currentComputeAllocationId || workspace.computeAllocationId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeId) || {};
  const storage = (state.storageVolumes || []).find((item) => item.id === workspace.storageId) || {};
  return { compute, storage };
}

export function WorkspacesPage({ state, balance }: any) {
  const workspaces = state.workspaces || [];
  const running = workspaces.filter((workspace) => workspace.state === "running").length;
  const computeResources = (state.computeAllocations || []).filter((item) => item.status !== "destroyed");
  const storageResources = (state.storageVolumes || []).filter((item) => item.status !== "destroyed");
  const activeAttachments = (state.storageAttachments || []).filter((item) => item.status === "attached");

  return (
    <ConsoleSurface
      title="工作区"
      eyebrow="工作区"
      subtitle="创建、访问和管理工作区"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建工作区</Button>}
    >
      <MetricStrip
        items={[
          { label: "工作区", value: workspaces.length, caption: "访问入口", tone: workspaces.length ? "info" : "neutral" },
          { label: "运行中", value: running, caption: "可直接访问", tone: running ? "good" : "neutral" },
          { label: "有效计算", value: computeResources.filter((item) => item.billingStatus === "active").length, caption: "月度权益", tone: "good" },
          { label: "有效存储", value: storageResources.filter((item) => item.billingStatus === "active").length, caption: "月度权益", tone: "good" },
          { label: "Sub2API 余额", value: usdBalance(balance), caption: "gflabtoken.cn · USD", icon: <WalletCards size={16} />, tone: Number(balance.usdMicros || 0) > 0 ? "good" : "warn" }
        ]}
      />

      <InsightPanel title="资源管理" eyebrow="工作区下的计算、存储、挂载">
        <ResourceSplit
          items={[
            { label: "计算资源", value: `${computeResources.length} 个`, meta: "开通和查看计算节点", status: computeResources.length ? "可查看" : "未开通", tone: computeResources.length ? "info" : "neutral" },
            { label: "云硬盘", value: `${storageResources.length} 个`, meta: "开通和查看数据盘", status: storageResources.length ? "可查看" : "未开通", tone: storageResources.length ? "info" : "neutral" },
            { label: "挂载关系", value: `${activeAttachments.length} 个`, meta: "存储挂到计算后才能创建入口", status: activeAttachments.length ? "已挂载" : "待挂载", tone: activeAttachments.length ? "good" : "warn" },
            { label: "资源关系", value: "链路图", meta: "账号、计算、存储、挂载、工作区", status: "可查看", tone: "info" }
          ]}
        />
        <ActionGroup actions={[
          { label: "计算资源", icon: <Server size={15} />, onClick: () => navigate(routeTo("compute-allocations.list")) },
          { label: "云硬盘", icon: <HardDrive size={15} />, onClick: () => navigate(routeTo("storage.list")) },
          { label: "挂载关系", icon: <Cable size={15} />, onClick: () => navigate(routeTo("attachment.list")) },
          { label: "资源关系", icon: <Settings2 size={15} />, onClick: () => navigate(routeTo("resources.relationships")) }
        ]} />
      </InsightPanel>

      <InsightPanel title="工作区列表" eyebrow="访问入口与月度权益">
        <div className="mobileWorkspaceList">
          {workspaces.length ? workspaces.map((workspace) => {
            const { compute, storage } = workspaceEntitlements(state, workspace);
            return (
              <article className="mobileWorkspaceCard" key={workspace.id}>
                <div className="mobileWorkspaceHeader">
                  <strong>{workspace.name}</strong>
                  <StatusPill label={statusLabel(workspace)} tone={statusColor(workspace.state) === "green" ? "good" : statusColor(workspace.state) === "red" ? "danger" : "warn"} />
                </div>
                <div className="mobileWorkspaceFacts">
                  <span>计算有效期 <b>{paidThrough(compute.paidThrough)}</b></span>
                  <span>存储有效期 <b>{paidThrough(storage.paidThrough)}</b></span>
                  <span>账号 <b>{workspaceCredential(workspace)}</b></span>
                  <span>访问入口 <b>{workspaceAccessLabel(workspace)}</b></span>
                </div>
                <ActionGroup actions={[
                  { label: workspaceOpenActionLabel(workspace), icon: <LinkIcon size={14} />, disabled: !workspaceUrlReady(workspace), onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") },
                  { label: "详情", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: workspace.id })) }
                ]} />
              </article>
            );
          }) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无工作区" />}
        </div>
        <ObjectTable
          className="objectTable workspaceTable desktopWorkspaceTable"
          rowKey="id"
          data={workspaces}
          tableLayout="fixed"
          scroll={{ x: 900 }}
          emptyText="暂无工作区"
          columns={[
            { title: "名称", dataIndex: "name", width: 150, render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("workspace.detail", { id: row.id }))}>{row.name}</Button> },
            { title: "状态", dataIndex: "state", width: 90, render: (_, row) => <StatusPill label={statusLabel(row)} tone={statusColor(row.state) === "green" ? "good" : statusColor(row.state) === "red" ? "danger" : "warn"} /> },
            { title: "计算有效期", width: 120, render: (_, row) => paidThrough(workspaceEntitlements(state, row).compute.paidThrough) },
            { title: "存储有效期", width: 120, render: (_, row) => paidThrough(workspaceEntitlements(state, row).storage.paidThrough) },
            { title: "访问入口", width: 110, render: (_, row) => <StatusPill label={workspaceAccessLabel(row)} tone={workspaceAccessTone(row)} /> },
            { title: "账号", width: 110, render: (_, row) => <Typography.Text copyable className="inlineCode credentialCell">{workspaceCredential(row)}</Typography.Text> },
            { title: "操作", valueType: "option", width: 150, render: (_, row) => <ActionGroup actions={[
              { label: workspaceOpenActionLabel(row), icon: <LinkIcon size={14} />, disabled: !workspaceUrlReady(row), onClick: () => window.open(row.url, "_blank", "noopener,noreferrer") },
              { label: "详情", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: row.id })) }
            ]} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
