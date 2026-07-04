import React from "react";
import { Button, Typography } from "antd";
import { Link as LinkIcon, Plus, RefreshCw, Settings2, Trash2 } from "lucide-react";
import { deleteWorkspaceToken, resetWorkspaceToken } from "../../api/workspaces-api.js";
import { navigate, routeTo } from "../../consoleRoutes.js";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  StatusPill
} from "../shared/commercial-console.jsx";
import { available, money, packageText, statusColor, statusLabel } from "../shared/formatters.js";

function statusTone(value) {
  const color = statusColor(value);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  if (color === "orange") return "warn";
  return "info";
}

export function WorkspacesPage({ state, wallet, runAction, session }) {
  const planById = Object.fromEntries((state.packages || []).map((plan) => [plan.id, plan]));
  const running = state.workspaces.filter((workspace) => workspace.state === "running").length;
  const activeUrls = state.workspaces.filter((workspace) => workspace.access?.tokenStatus === "active").length;
  const attachedEntries = state.workspaces.filter((workspace) => workspace.attachmentId).length;

  return (
    <ConsoleSurface
      title="Workspaces"
      eyebrow="Delivery"
      subtitle="Compute allocation, storage volume, URL token, billing hold"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建 Workspace</Button>}
    >
      <MetricStrip
        items={[
          { label: "总数", value: state.workspaces.length, caption: "owned by this lab", tone: state.workspaces.length ? "info" : "neutral" },
          { label: "运行中", value: running, caption: "Workspace URL entries", tone: running ? "good" : "neutral" },
          { label: "URL 可用", value: activeUrls, caption: "shareable links", tone: activeUrls ? "good" : "warn" },
          { label: "已挂载", value: attachedEntries, caption: "attachment-backed entries", tone: attachedEntries ? "info" : "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: "after frozen hold", tone: available(wallet) > 0 ? "good" : "warn" }
        ]}
      />

      <InsightPanel title="Workspace 对象" eyebrow="Current">
        <ObjectTable
          rowKey="id"
          data={state.workspaces}
          emptyText="暂无 Workspace"
          columns={[
            {
              title: "名称",
              dataIndex: "name",
              render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("workspace.detail", { id: row.id }))}>{row.name}</Button>
            },
            {
              title: "状态",
              dataIndex: "state",
              render: (_, row) => <StatusPill label={statusLabel(row)} tone={statusTone(row.state)} />
            },
            { title: "套餐", dataIndex: "packageId", render: (value) => packageText(planById[value]) },
            { title: "计算分配", render: (_, row) => <Typography.Text ellipsis>{row.computeAllocationId || row.server?.id || "-"}</Typography.Text> },
            { title: "存储卷", render: (_, row) => <Typography.Text ellipsis>{row.storageId || row.disk?.id || "-"}</Typography.Text> },
            {
              title: "Workspace URL",
              dataIndex: "url",
              ellipsis: true,
              render: (_, row) => <Typography.Text copyable={row.access?.tokenStatus === "active"} className="inlineCode">{row.url}</Typography.Text>
            },
            {
              title: "操作",
              valueType: "option",
              render: (_, row) => (
                <ActionGroup
                  actions={[
                    { label: "资源", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: row.id })) },
                    { label: "打开", icon: <LinkIcon size={14} />, disabled: row.access?.tokenStatus !== "active", onClick: () => window.open(row.url, "_blank", "noopener,noreferrer") },
                    { label: "重置", icon: <RefreshCw size={14} />, disabled: row.access?.tokenStatus !== "active", onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: row.id }, session.csrfToken), "URL 已重置") },
                    { label: "停用", danger: true, icon: <Trash2 size={14} />, disabled: row.access?.tokenStatus !== "active", onClick: () => runAction(() => deleteWorkspaceToken({ workspaceId: row.id }, session.csrfToken), "URL 已停用") }
                  ]}
                />
              )
            }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
