import React from "react";
import { Alert, Button, Empty, Typography } from "antd";
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
import { moneyCents, packageText, paidThrough, statusColor, statusLabel, usdMicros, valueLabel, workspaceAccessLabel, workspaceAccessTone, workspaceOpenActionLabel, workspaceUrlReady } from "../shared/formatters.ts";

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
  const computeId = selected.currentComputeAllocationId || selected.computeAllocationId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeId) || {};
  const storage = (state.storageVolumes || []).find((item) => item.id === selected.storageId) || {};
  const supportPath = `${routeTo("support.create")}?category=Workspace&resourceId=${encodeURIComponent(selected.id)}&operationId=${encodeURIComponent(selected.currentAttachmentId || selected.currentComputeAllocationId || "")}`;
  const [showPassword, setShowPassword] = React.useState(false);
  const accessActive = selected.access?.tokenStatus === "active";
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
          actions={<StatusPill label={workspaceAccessLabel(selected)} tone={workspaceAccessTone(selected)} />}
        >
          <div className="stackList">
            {!workspaceUrlReady(selected) && selected.access?.tokenStatus === "active" && (
              <Alert
                type="info"
                showIcon
                message="正在分发 Docker"
                description="访问 URL 已生成，Runtime 仍在部署。通常需要 3-5 分钟，请稍后再打开。"
              />
            )}
            <div className="credentialStack">
              <span>URL</span>
              <Typography.Text copyable={workspaceUrlReady(selected)} className="inlineCode">{selected.url}</Typography.Text>
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
                { label: workspaceOpenActionLabel(selected), icon: <LinkIcon size={15} />, disabled: !workspaceUrlReady(selected), onClick: () => window.open(selected.url, "_blank", "noopener,noreferrer") },
                { label: showPassword ? "隐藏密码" : "显示密码", icon: showPassword ? <EyeOff size={15} /> : <Eye size={15} />, onClick: () => setShowPassword(!showPassword) },
                accessActive
                  ? { label: "重置 URL", icon: <RefreshCw size={15} />, onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已重置", { actionKey: `workspace-reset-${selected.id}` }) }
                  : { label: "启用访问", type: "primary", icon: <RefreshCw size={15} />, onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "访问已启用", { actionKey: `workspace-reset-${selected.id}` }) },
                { label: "停用访问", danger: true, icon: <Ban size={15} />, disabled: !accessActive, onClick: () => runAction(() => deleteWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "访问已停用", { actionKey: `workspace-delete-${selected.id}` }) },
                { label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(supportPath) }
              ]}
            />
          </div>
        </InsightPanel>

        <InsightPanel title="月度权益" eyebrow="计算与存储">
          <ResourceSplit
            items={[
              { label: "计算权益", value: valueLabel(compute.billingStatus), meta: `有效期至 ${paidThrough(compute.paidThrough)}`, status: compute.autoRenew ? "自动续费" : "到期停止", tone: compute.billingStatus === "active" ? "good" : "warn" },
              { label: "计算月价", value: moneyCents(compute.monthlyPriceCnyCents), meta: `Sub2API ${usdMicros(compute.chargeUsdMicros)}`, status: "月付", tone: "info" },
              { label: "存储权益", value: valueLabel(storage.billingStatus), meta: `有效期至 ${paidThrough(storage.paidThrough)}`, status: storage.autoRenew ? "自动续费" : "到期保留", tone: storage.billingStatus === "active" ? "good" : "warn" },
              { label: "存储月价", value: moneyCents(storage.monthlyPriceCnyCents), meta: `Sub2API ${usdMicros(storage.chargeUsdMicros)}`, status: "月付", tone: "info" },
              { label: "套餐", value: selectedPlan?.name || "-", meta: packageText(selectedPlan), status: "套餐", tone: "info" },
              { label: "状态", value: statusLabel(selected), meta: selected.state, status: "Workspace", tone: toneForStatus(selected.state) },
              { label: "费用明细", value: "账单页", meta: "查看余额与权益", status: "可查看", tone: "good" }
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
