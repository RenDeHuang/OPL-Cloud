import React from "react";
import { Button, Empty, Typography } from "antd";
import { Ban, Eye, EyeOff, Headphones, Link as LinkIcon, RefreshCw, WalletCards } from "lucide-react";
import {
  deleteWorkspaceToken,
  resetWorkspaceToken
} from "../../api/workspaces-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { money, packageText, statusColor, statusLabel, valueLabel } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function toneForStatus(value) {
  const color = statusColor(value);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  if (color === "orange") return "warn";
  return "info";
}

function workspaceCredential(workspace: AnyRecord = {}) {
  return {
    account: workspace.access?.account
      || workspace.access?.username
      || workspace.login?.username
      || workspace.id
      || "-",
    password: workspace.access?.password
      || "未返回"
  };
}

export function WorkspaceDetailPage({ selected, selectedPlan, state, session, runAction }: any) {
  if (!selected) {
    return (
      <ConsoleSurface title="OPL Workspace" eyebrow="工作区">
        <Empty description="暂无工作区" />
      </ConsoleSurface>
    );
  }
  const credential = workspaceCredential(selected);
  const currentCost = Number(selected.billing?.currentChargeTotal || 0);
  const hourlyEstimate = Number(selected.billing?.activeHourlyEstimate || 0);
  const supportPath = `${routeTo("support.create")}?category=Workspace&resourceId=${encodeURIComponent(selected.id)}&operationId=${encodeURIComponent(selected.currentAttachmentId || selected.currentComputeAllocationId || "")}`;
  const [showPassword, setShowPassword] = React.useState(false);
  return (
    <ConsoleSurface
      title={selected.name}
      eyebrow="OPL Workspace"
      subtitle="访问凭据、费用状态和支持"
      extra={<Button onClick={() => navigate(routeTo("workspace.list"))}>返回列表</Button>}
    >
      <div className="consoleGrid equal">
        <InsightPanel
          title="访问凭据"
          eyebrow="URL、账号、密码"
          actions={<StatusPill label={valueLabel(selected.access?.tokenStatus)} tone={selected.access?.tokenStatus === "active" ? "good" : "warn"} />}
        >
          <div className="stackList">
            <div className="credentialStack">
              <span>URL</span>
              <Typography.Text copyable={selected.access?.tokenStatus === "active"} className="inlineCode">{selected.url}</Typography.Text>
            </div>
            <div className="credentialStack">
              <span>账号</span>
              <Typography.Text copyable className="inlineCode">{credential.account}</Typography.Text>
            </div>
            <div className="credentialStack">
              <span>密码</span>
              <Typography.Text copyable={showPassword} className="inlineCode">{showPassword ? credential.password : "********"}</Typography.Text>
            </div>
            <ActionGroup
              actions={[
                { label: "打开", icon: <LinkIcon size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => window.open(selected.url, "_blank", "noopener,noreferrer") },
                { label: showPassword ? "隐藏密码" : "显示密码", icon: showPassword ? <EyeOff size={15} /> : <Eye size={15} />, onClick: () => setShowPassword(!showPassword) },
                { label: "重置", icon: <RefreshCw size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已重置", { actionKey: `workspace-reset-${selected.id}` }) },
                { label: "停用访问", danger: true, icon: <Ban size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => deleteWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "访问已停用", { actionKey: `workspace-delete-${selected.id}` }) },
                { label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(supportPath) }
              ]}
            />
          </div>
        </InsightPanel>

        <InsightPanel title="费用和状态" eyebrow="按工作区">
          <ResourceSplit
            items={[
              { label: "当前费用", value: money(currentCost), meta: "最近资源费用", status: "计费", tone: currentCost > 0 ? "info" : "neutral" },
              { label: "预计每小时", value: money(hourlyEstimate), meta: "计算 + 存储", status: "预估", tone: hourlyEstimate > 0 ? "warn" : "neutral" },
              { label: "套餐", value: selectedPlan?.name || "-", meta: packageText(selectedPlan), status: "套餐", tone: "info" },
              { label: "状态", value: statusLabel(selected), meta: selected.state, status: "Workspace", tone: toneForStatus(selected.state) },
              { label: "费用明细", value: "账单页", meta: "打开扣费记录", status: "可查看", tone: "good" }
            ]}
          />
          <ActionGroup
            actions={[
              { label: "查看账单", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) },
              { label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(supportPath) }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
