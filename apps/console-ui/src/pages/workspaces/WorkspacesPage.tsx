import React from "react";
import { Button, Empty, Typography } from "antd";
import { Link as LinkIcon, Plus, Settings2, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  StatusPill
} from "../shared/commercial-console.tsx";
import { available, money, moneyValue, resourceDebitEvents, statusColor, statusLabel } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function statusTone(value) {
  const color = statusColor(value);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  if (color === "orange") return "warn";
  return "info";
}

function workspaceCredential(workspace: AnyRecord = {}) {
  const account = workspace.access?.account
    || workspace.access?.username
    || workspace.login?.username
    || workspace.id
    || "-";
  const ready = Boolean(workspace.access?.password);
  return { account, ready };
}

function workspaceHourlyEstimate({ workspace, compute, storage }: any) {
  const computeHourly = Number(compute?.hourlyPrice || compute?.hourlyEstimate || 0);
  const storageHourly = Number(storage?.hourlyEstimate || workspace?.disk?.hourlyEstimate || 0);
  return computeHourly + storageHourly;
}

function workspaceChargeTotal(state: AnyRecord = {}, workspaceId = "") {
  return resourceDebitEvents(state)
    .filter((item) => item.workspaceId === workspaceId)
    .reduce((sum, item) => sum + Math.abs(moneyValue(item)), 0);
}

export function WorkspacesPage({ state, wallet }: any) {
  const computeById = new Map((state.computeAllocations || []).map((compute) => [compute.id, compute]));
  const storageById = new Map((state.storageVolumes || []).map((storage) => [storage.id, storage]));
  const running = state.workspaces.filter((workspace) => workspace.state === "running").length;
  const billedTotal = state.workspaces.reduce((sum, workspace) => sum + workspaceChargeTotal(state, workspace.id), 0);
  const hourlyTotal = state.workspaces.reduce((sum, workspace) => {
    const compute = computeById.get(workspace.currentComputeAllocationId);
    const storage = storageById.get(workspace.storageId);
    return sum + workspaceHourlyEstimate({ workspace, compute, storage });
  }, 0);

  return (
    <ConsoleSurface
      title="工作区"
      eyebrow="工作区"
      subtitle="创建、访问和管理工作区"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建工作区</Button>}
    >
      <MetricStrip
        items={[
          { label: "工作区", value: state.workspaces.length, caption: "访问入口", tone: state.workspaces.length ? "info" : "neutral" },
          { label: "运行中", value: running, caption: "可直接访问", tone: running ? "good" : "neutral" },
          { label: "当前费用", value: money(billedTotal), caption: "按工作区汇总", icon: <WalletCards size={16} />, tone: billedTotal > 0 ? "info" : "neutral" },
          { label: "预计每小时", value: money(hourlyTotal), caption: "当前活跃资源", tone: hourlyTotal > 0 ? "warn" : "neutral" },
          { label: "可用余额", value: money(available(wallet)), caption: "扣除冻结后", tone: available(wallet) > 0 ? "good" : "warn" }
        ]}
      />

      <InsightPanel title="工作区列表" eyebrow="访问入口、状态、费用">
        <div className="mobileWorkspaceList">
          {state.workspaces.length ? state.workspaces.map((workspace) => {
            const compute = computeById.get(workspace.currentComputeAllocationId);
            const storage = storageById.get(workspace.storageId);
            return (
              <article className="mobileWorkspaceCard" key={workspace.id}>
                <div className="mobileWorkspaceHeader">
                  <strong>{workspace.name}</strong>
                  <StatusPill label={statusLabel(workspace)} tone={statusTone(workspace.state)} />
                </div>
                <div className="mobileWorkspaceFacts">
                  <span>当前费用 <b>{money(workspaceChargeTotal(state, workspace.id))}</b></span>
                  <span>预计每小时 <b>{money(workspaceHourlyEstimate({ workspace, compute, storage }))}</b></span>
                  <span>账号 <b>{workspaceCredential(workspace).account}</b></span>
                  <span>状态 <b>{workspace.access?.tokenStatus === "active" ? "可用" : "未启用"}</b></span>
                </div>
                <ActionGroup
                  actions={[
                    { label: "打开", icon: <LinkIcon size={14} />, disabled: workspace.access?.tokenStatus !== "active", onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") },
                    { label: "详情", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: workspace.id })) }
                  ]}
                />
              </article>
            );
          }) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无工作区" />}
        </div>
        <ObjectTable
          className="objectTable workspaceTable desktopWorkspaceTable"
          rowKey="id"
          data={state.workspaces}
          tableLayout="fixed"
          scroll={{ x: 820 }}
          emptyText="暂无工作区"
          columns={[
            {
              title: "名称",
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
              title: "访问入口",
              width: 125,
              render: (_, row) => <StatusPill label={row.access?.tokenStatus === "active" ? "可用" : "未启用"} tone={row.access?.tokenStatus === "active" ? "good" : "warn"} />
            },
            {
              title: "账号",
              width: 100,
              render: (_, row) => <Typography.Text copyable className="inlineCode credentialCell">{workspaceCredential(row).account}</Typography.Text>
            },
            {
              title: "操作",
              valueType: "option",
              width: 150,
              render: (_, row) => (
                <ActionGroup
                  actions={[
                    { label: "打开", icon: <LinkIcon size={14} />, disabled: row.access?.tokenStatus !== "active", onClick: () => window.open(row.url, "_blank", "noopener,noreferrer") },
                    { label: "详情", icon: <Settings2 size={14} />, onClick: () => navigate(routeTo("workspace.detail", { id: row.id })) }
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
