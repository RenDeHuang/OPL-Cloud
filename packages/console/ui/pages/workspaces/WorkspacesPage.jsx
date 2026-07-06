import React from "react";
import { Button, Typography } from "antd";
import { Link as LinkIcon, Plus, RefreshCw, Settings2, Trash2, WalletCards } from "lucide-react";
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
import { available, money, statusColor, statusLabel } from "../shared/formatters.js";

function statusTone(value) {
  const color = statusColor(value);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  if (color === "orange") return "warn";
  return "info";
}

function tokenFromUrl(url = "") {
  try {
    return new URL(url).searchParams.get("token") || "";
  } catch {
    return "";
  }
}

function workspaceCredential(workspace = {}) {
  const account = workspace.access?.account
    || workspace.access?.username
    || workspace.login?.username
    || workspace.id
    || "-";
  const password = workspace.access?.password
    || workspace.access?.token
    || tokenFromUrl(workspace.url)
    || "等待生成";
  return { account, password };
}

function workspaceHourlyEstimate({ workspace, compute, storage }) {
  const computeHourly = Number(compute?.hourlyPrice || compute?.hourlyEstimate || 0);
  const storageHourly = Number(storage?.hourlyEstimate || workspace?.disk?.hourlyEstimate || 0);
  return computeHourly + storageHourly;
}

function workspaceChargeTotal(state = {}, workspaceId = "") {
  return [
    ...(state.resourceUsageLogs || []),
    ...(state.requestUsageLogs || [])
  ]
    .filter((item) => item.workspaceId === workspaceId)
    .reduce((sum, item) => sum + Math.abs(Number(item.amount || item.charge || 0)), 0);
}

export function WorkspacesPage({ state, wallet, runAction, session }) {
  const computeById = new Map((state.computeAllocations || []).map((compute) => [compute.id, compute]));
  const storageById = new Map((state.storageVolumes || []).map((storage) => [storage.id, storage]));
  const running = state.workspaces.filter((workspace) => workspace.state === "running").length;
  const activeUrls = state.workspaces.filter((workspace) => workspace.access?.tokenStatus === "active").length;
  const billedTotal = state.workspaces.reduce((sum, workspace) => sum + workspaceChargeTotal(state, workspace.id), 0);
  const hourlyTotal = state.workspaces.reduce((sum, workspace) => {
    const compute = computeById.get(workspace.currentComputeAllocationId);
    const storage = storageById.get(workspace.storageId);
    return sum + workspaceHourlyEstimate({ workspace, compute, storage });
  }, 0);

  return (
    <ConsoleSurface
      title="OPL Workspace"
      eyebrow="工作区"
      subtitle="每个 Workspace 独立暴露 URL、账号、密码，并按 Workspace 计费"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建工作区</Button>}
    >
      <MetricStrip
        items={[
          { label: "Workspace", value: state.workspaces.length, caption: "等同 UI 子账号", tone: state.workspaces.length ? "info" : "neutral" },
          { label: "运行中", value: running, caption: "可直接访问", tone: running ? "good" : "neutral" },
          { label: "URL 可用", value: activeUrls, caption: "可复制分发", tone: activeUrls ? "good" : "warn" },
          { label: "当前费用", value: money(billedTotal), caption: "按 Workspace 汇总", icon: <WalletCards size={16} />, tone: billedTotal > 0 ? "info" : "neutral" },
          { label: "预计每小时", value: money(hourlyTotal), caption: "计算 + 存储", tone: hourlyTotal > 0 ? "warn" : "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: "扣除冻结后", tone: available(wallet) > 0 ? "good" : "warn" }
        ]}
      />

      <InsightPanel title="工作区" eyebrow="URL、账号、密码、计费">
        <ObjectTable
          className="objectTable workspaceTable"
          rowKey="id"
          data={state.workspaces}
          tableLayout="fixed"
          scroll={{ x: 1280 }}
          emptyText="暂无工作区"
          columns={[
            {
              title: "OPL Workspace",
              dataIndex: "name",
              width: 150,
              render: (_, row) => (
                <div className="workspaceNameCell">
                  <Button type="link" onClick={() => navigate(routeTo("workspace.detail", { id: row.id }))}>{row.name}</Button>
                  <Typography.Text type="secondary" ellipsis>{row.id}</Typography.Text>
                </div>
              )
            },
            {
              title: "状态",
              dataIndex: "state",
              width: 88,
              render: (_, row) => <StatusPill label={statusLabel(row)} tone={statusTone(row.state)} />
            },
            {
              title: "当前费用",
              width: 105,
              render: (_, row) => money(workspaceChargeTotal(state, row.id))
            },
            {
              title: "预计每小时",
              width: 105,
              render: (_, row) => {
                const compute = computeById.get(row.currentComputeAllocationId);
                const storage = storageById.get(row.storageId);
                return money(workspaceHourlyEstimate({ workspace: row, compute, storage }));
              }
            },
            {
              title: "归属",
              dataIndex: "ownerAccountId",
              width: 130,
              render: (value) => <Typography.Text copyable className="inlineCode credentialCell">{value || "-"}</Typography.Text>
            },
            {
              title: "URL",
              dataIndex: "url",
              ellipsis: true,
              width: 250,
              render: (_, row) => <Typography.Text copyable={row.access?.tokenStatus === "active"} className="inlineCode credentialCell">{row.url}</Typography.Text>
            },
            {
              title: "账号",
              width: 100,
              render: (_, row) => <Typography.Text copyable className="inlineCode credentialCell">{workspaceCredential(row).account}</Typography.Text>
            },
            {
              title: "密码",
              width: 140,
              render: (_, row) => <Typography.Text copyable className="inlineCode credentialCell">{workspaceCredential(row).password}</Typography.Text>
            },
            {
              title: "操作",
              valueType: "option",
              width: 180,
              render: (_, row) => (
                <ActionGroup
                  actions={[
                    { label: "详情", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: row.id })) },
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
