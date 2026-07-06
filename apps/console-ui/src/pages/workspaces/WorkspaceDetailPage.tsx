import React from "react";
import { Button, Empty, Typography } from "antd";
import { Cable, CreditCard, HardDrive, Link as LinkIcon, RefreshCw, Server, Trash2 } from "lucide-react";
import {
  deleteWorkspaceToken,
  resetWorkspaceToken
} from "../../api/workspaces-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  DataRetentionPolicyPanel,
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

function tokenFromUrl(url = "") {
  try {
    return new URL(url).searchParams.get("token") || "";
  } catch {
    return "";
  }
}

function workspaceCredential(workspace: AnyRecord = {}) {
  return {
    account: workspace.access?.account
      || workspace.access?.username
      || workspace.login?.username
      || workspace.id
      || "-",
    password: workspace.access?.password
      || workspace.access?.token
      || tokenFromUrl(workspace.url)
      || "等待生成"
  };
}

function workspaceChargeTotal(state: AnyRecord = {}, workspaceId = "") {
  return (state.resourceUsageLogs || [])
    .filter((item) => item.workspaceId === workspaceId)
    .reduce((sum, item) => sum + Math.abs(Number(item.amount || item.charge || 0)), 0);
}

function workspaceHourlyEstimate({ workspace, compute, storage }: any) {
  return Number(compute?.hourlyPrice || compute?.hourlyEstimate || 0)
    + Number(storage?.hourlyEstimate || workspace?.disk?.hourlyEstimate || 0);
}

function evidenceValue(...values) {
  const value = values.find((item) => item !== undefined && item !== null && String(item).trim() !== "");
  return value === undefined || value === null ? "-" : String(value);
}

function failureEvidence(...resources) {
  return resources.find((resource) => resource?.safeMessage || resource?.error || resource?.failureReason || resource?.providerRequestId || resource?.operationId) || {};
}

function WorkspaceLifecyclePanel({ workspace, compute, storage, attachment }: any) {
  return (
    <InsightPanel title="访问生命周期" eyebrow="生命周期">
      <ResourceSplit
        items={[
          { label: "URL 状态", value: workspace.access?.tokenStatus || "-", meta: "访问状态", status: workspace.access?.tokenStatus || "未知", tone: workspace.access?.tokenStatus === "active" ? "good" : "warn" },
          { label: "当前计算", value: compute?.status || "暂停", meta: workspace.currentComputeAllocationId || "待重建", status: "计算", tone: toneForStatus(compute?.status || workspace.runtime?.status) },
          { label: "存储资源", value: storage?.status || "缺失", meta: workspace.storageId, status: "存储", tone: toneForStatus(storage?.status) },
          { label: "当前挂载", value: attachment?.status || "未挂载", meta: workspace.currentAttachmentId || "待重建", status: "挂载", tone: toneForStatus(attachment?.status || workspace.runtime?.status) }
        ]}
      />
    </InsightPanel>
  );
}

export function WorkspaceDetailPage({ selected, selectedPlan, state, session, runAction }: any) {
  if (!selected) {
    return (
      <ConsoleSurface title="OPL Workspace" eyebrow="工作区">
        <Empty description="暂无工作区" />
      </ConsoleSurface>
    );
  }
  const computeAllocationId = selected.currentComputeAllocationId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeAllocationId);
  const storage = (state.storageVolumes || []).find((item) => item.id === selected.storageId);
  const attachment = (state.storageAttachments || []).find((item) => item.id === selected.currentAttachmentId);
  const credential = workspaceCredential(selected);
  const currentCost = workspaceChargeTotal(state, selected.id);
  const hourlyEstimate = workspaceHourlyEstimate({ workspace: selected, compute, storage });
  const issue = failureEvidence(compute, storage, attachment, selected);
  return (
    <ConsoleSurface
      title={selected.name}
      eyebrow="OPL Workspace"
      subtitle="Workspace 即 UI 子账号：URL、账号、密码、计费口径都在这里"
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
              <Typography.Text copyable className="inlineCode">{credential.password}</Typography.Text>
            </div>
            <ActionGroup
              actions={[
                { label: "打开", icon: <LinkIcon size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => window.open(selected.url, "_blank", "noopener,noreferrer") },
                { label: "重置", icon: <RefreshCw size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已重置", { actionKey: `workspace-reset-${selected.id}` }) },
                { label: "停用", danger: true, icon: <Trash2 size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => deleteWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已停用", { actionKey: `workspace-delete-${selected.id}` }) }
              ]}
            />
          </div>
        </InsightPanel>

        <InsightPanel title="Workspace 计费" eyebrow="按工作区">
          <ResourceSplit
            items={[
              { label: "当前费用", value: money(currentCost), meta: "已记录资源与请求用量", status: "计费", tone: currentCost > 0 ? "info" : "neutral" },
              { label: "预计每小时", value: money(hourlyEstimate), meta: "计算 + 存储", status: "预估", tone: hourlyEstimate > 0 ? "warn" : "neutral" },
              { label: "归属账号", value: selected.ownerAccountId || "-", meta: "钱包和账本归属", status: "钱包", tone: "info" },
              { label: "套餐", value: selectedPlan?.name || "-", meta: packageText(selectedPlan), status: "套餐", tone: "info" },
              { label: "状态", value: statusLabel(selected), meta: selected.state, status: "Workspace", tone: toneForStatus(selected.state) },
              { label: "账单入口", value: "概览 + 详情", meta: "不再作为一级菜单", status: "已收敛", tone: "good" }
            ]}
          />
        </InsightPanel>
      </div>

      <div className="consoleGrid equal">
        <InsightPanel title="二级资源" eyebrow="计算、存储、挂载">
          <ResourceSplit
            items={[
              { label: "当前计算", value: compute?.name || computeAllocationId || "待重建", meta: valueLabel(compute?.status || selected.server?.status), status: "计算", tone: toneForStatus(compute?.status || selected.server?.status) },
              { label: "存储", value: storage?.name || selected.storageId || "-", meta: `${selected.disk?.sizeGb || storage?.sizeGb || 0}GB`, status: "存储", tone: toneForStatus(storage?.status || selected.disk?.status) },
              { label: "当前挂载", value: attachment?.id || selected.currentAttachmentId || "待重建", meta: attachment?.mountPath || selected.disk?.mountPath || "/data", status: "挂载", tone: toneForStatus(attachment?.status || selected.runtime?.status) },
              { label: "运行时", value: selected.runtime?.status || "待检查", meta: selected.currentAttachmentId || selected.currentComputeAllocationId || "-", status: "Runtime", tone: toneForStatus(selected.runtime?.status) }
            ]}
          />
          <ActionGroup
            actions={[
              { label: "查看当前计算", icon: <Server size={15} />, disabled: !computeAllocationId, onClick: () => navigate(routeTo("compute-allocations.detail", { id: computeAllocationId })) },
              { label: "查看存储资源", icon: <HardDrive size={15} />, disabled: !selected.storageId, onClick: () => navigate(routeTo("storage.detail", { id: selected.storageId })) },
              { label: "查看当前挂载", icon: <Cable size={15} />, disabled: !selected.currentAttachmentId, onClick: () => navigate(routeTo("attachment.detail", { id: selected.currentAttachmentId })) },
              { label: "账单", icon: <CreditCard size={15} />, onClick: () => navigate(routeTo("billing.overview")) }
            ]}
          />
        </InsightPanel>
        <WorkspaceLifecyclePanel workspace={selected} compute={compute} storage={storage} attachment={attachment} />
      </div>

      <InsightPanel title="运维归因证据" eyebrow="Owner、CVM、存储、URL">
        <ResourceSplit
          items={[
            { label: "URL owner", value: evidenceValue(selected.ownerAccountId), meta: evidenceValue(selected.id), status: "Workspace", tone: "info" },
            { label: "URL 状态", value: evidenceValue(selected.access?.tokenStatus), meta: evidenceValue(selected.currentAttachmentId, "无当前挂载"), status: "访问入口", tone: selected.access?.tokenStatus === "active" ? "good" : "warn" },
            { label: "CVM owner", value: evidenceValue(compute?.ownerAccountId, selected.ownerAccountId), meta: evidenceValue(computeAllocationId), status: "计算归属", tone: "info" },
            { label: "CVM ID", value: evidenceValue(compute?.cvmInstanceId, compute?.providerResourceId, "CVM ID 未返回"), meta: evidenceValue(compute?.nodeName, compute?.machineName, compute?.nodePoolId), status: "TKE/CVM", tone: compute?.cvmInstanceId ? "good" : "warn" },
            { label: "存储 owner", value: evidenceValue(storage?.ownerAccountId, selected.ownerAccountId), meta: evidenceValue(selected.storageId), status: "存储归属", tone: "info" },
            { label: "存储 provider ID", value: evidenceValue(storage?.providerResourceId, storage?.id), meta: evidenceValue(storage?.storageClassId, selected.disk?.storageClass), status: "PVC/CBS", tone: storage?.providerResourceId ? "good" : "warn" },
            { label: "挂载 owner", value: evidenceValue(attachment?.ownerAccountId, selected.ownerAccountId), meta: evidenceValue(attachment?.id, selected.currentAttachmentId), status: "挂载", tone: toneForStatus(attachment?.status || selected.runtime?.status) },
            { label: "问题依据", value: evidenceValue(issue.safeMessage, issue.error, issue.failureReason, "暂无失败"), meta: evidenceValue(issue.providerRequestId, issue.operationId), status: "故障定位", tone: issue.safeMessage || issue.error || issue.failureReason ? "danger" : "good" }
          ]}
        />
      </InsightPanel>
      <DataRetentionPolicyPanel />
    </ConsoleSurface>
  );
}
