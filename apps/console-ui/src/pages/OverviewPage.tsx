import { Alert } from "antd";
import { Headphones, Link as LinkIcon, Plus, RefreshCw, Settings2, WalletCards } from "lucide-react";
import { navigate, routeTo } from "../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ResourceSplit,
  StatusPill
} from "./shared/commercial-console.tsx";
import {
  paidThrough,
  statusColor,
  statusLabel,
  usdBalance,
  valueLabel,
  workspaceAccessLabel,
  workspaceAccessTone,
  workspaceOpenActionLabel,
  workspaceUrlReady
} from "./shared/formatters.ts";

function toneForWorkspace(workspace) {
  const color = statusColor(workspace?.state);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  return "warn";
}

export function OverviewPage({ state, balance, tickets }: any) {
  const workspaces = state.workspaces || [];
  const workspace = workspaces[0];
  const computeId = workspace?.currentComputeAllocationId || workspace?.computeAllocationId;
  const attachmentId = workspace?.currentAttachmentId || workspace?.attachmentId;
  const attachment = (state.storageAttachments || []).find((item) => item.id === attachmentId);
  const compute = (state.computeAllocations || []).find((item) => item.id === computeId || item.id === attachment?.computeAllocationId) || {};
  const storage = (state.storageVolumes || []).find((item) => item.id === workspace?.storageId || item.id === attachment?.storageId) || {};
  const activeTickets = (tickets.tickets || []).filter((ticket) => ticket.status !== "closed");
  const resourceProblems = [compute, storage].filter((item) =>
    ["manual_review", "past_due", "failed", "refunded"].includes(item.billingStatus)
    || ["failed", "quarantined", "external_deleted", "missing"].includes(item.status)
  );
  const workspaceProblem = workspace && !workspace.openable && ["failed", "suspended", "storage_missing", "unrecoverable"].includes(workspace.state);
  const anomalyCount = resourceProblems.length + activeTickets.length + (workspaceProblem ? 1 : 0) + Math.max(0, workspaces.length - 1);
  const activeEntitlements = [compute, storage].filter((item) => item.billingStatus === "active").length;
  const account = workspace?.access?.account || workspace?.access?.username || workspace?.login?.username || workspace?.id || "-";

  return (
    <ConsoleSurface
      title="概览"
      eyebrow="OPL Console"
      subtitle="唯一 Workspace、实时余额、月度权益与异常"
      extra={(
        <ActionGroup actions={[
          !workspace && { label: "开通 Workspace", type: "primary", icon: <Plus size={15} />, onClick: () => navigate(routeTo("workspace.create")) },
          workspace && { label: workspaceOpenActionLabel(workspace), type: "primary", icon: <LinkIcon size={15} />, disabled: !workspaceUrlReady(workspace), onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") },
          workspace && { label: "Workspace 详情", icon: <Settings2 size={15} />, onClick: () => navigate(routeTo("workspace.detail", { id: workspace.id })) },
          { label: "费用明细", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) }
        ].filter(Boolean)} />
      )}
    >
      <MetricStrip
        items={[
          { label: "Workspace", value: workspace ? statusLabel(workspace) : "未开通", caption: workspace ? workspaceAccessLabel(workspace) : "一个连续流程", tone: workspace ? toneForWorkspace(workspace) : "neutral" },
          { label: "Sub2API 余额", value: usdBalance(balance), caption: "gflabtoken.cn · USD", icon: <WalletCards size={16} />, tone: balance.available === false ? "danger" : Number(balance.usdMicros || 0) > 0 ? "good" : "warn" },
          { label: "月度权益", value: `${activeEntitlements}/2`, caption: "计算与存储", tone: activeEntitlements === 2 ? "good" : "warn" },
          { label: "异常", value: anomalyCount, caption: anomalyCount ? "需要处理" : "当前正常", tone: anomalyCount ? "danger" : "good" }
        ]}
      />

      {workspace ? (
        <InsightPanel title={workspace.name} eyebrow="唯一 Workspace" actions={<StatusPill label={workspaceAccessLabel(workspace)} tone={workspaceAccessTone(workspace)} />}>
          <ResourceSplit items={[
            { label: "状态", value: statusLabel(workspace), meta: workspace.state || "-", status: workspace.openable ? "可打开" : "恢复中", tone: toneForWorkspace(workspace) },
            { label: "账号", value: account, meta: workspace.id, status: "Runtime", tone: "info" },
            { label: "计算权益", value: valueLabel(compute.billingStatus), meta: `有效期至 ${paidThrough(compute.paidThrough)}`, status: compute.packageId || workspace.packageId || "-", tone: compute.billingStatus === "active" ? "good" : "warn" },
            { label: "存储权益", value: valueLabel(storage.billingStatus), meta: `有效期至 ${paidThrough(storage.paidThrough)}`, status: `${storage.sizeGb || 0}GB`, tone: storage.billingStatus === "active" ? "good" : "warn" },
            { label: "恢复", value: workspace.openable ? "无需操作" : "进入详情", meta: workspace.openable ? "Runtime 已就绪" : "检查状态并手动重试", status: workspace.openable ? "正常" : "待处理", tone: workspace.openable ? "good" : "warn" }
          ]} />
          <ActionGroup actions={[
            { label: workspaceOpenActionLabel(workspace), icon: <LinkIcon size={15} />, disabled: !workspaceUrlReady(workspace), onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") },
            !workspace.openable && { label: "恢复", icon: <RefreshCw size={15} />, onClick: () => navigate(routeTo("workspace.detail", { id: workspace.id })) },
            { label: "详情", icon: <Settings2 size={15} />, onClick: () => navigate(routeTo("workspace.detail", { id: workspace.id })) }
          ].filter(Boolean)} />
        </InsightPanel>
      ) : (
        <InsightPanel title="开通 Workspace" eyebrow="一个连续流程" actions={<StatusPill label="未开通" tone="neutral" />}>
          <Alert type="info" showIcon message="选择套餐后一次完成月费扣款、PREPAID 资源、Gateway Secret、Runtime 与 URL。" />
          <ActionGroup actions={[
            { label: "开通 Workspace", type: "primary", icon: <Plus size={15} />, onClick: () => navigate(routeTo("workspace.create")) },
            { label: "查看账单", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) }
          ]} />
        </InsightPanel>
      )}

      <InsightPanel title="异常" eyebrow="需要处理">
        {anomalyCount ? (
          <div className="stackList">
            {workspaces.length > 1 && <Alert type="error" showIcon message="检测到多个 Workspace" description="当前主流程只展示第一个 Workspace，请联系支持处理。" />}
            {workspaceProblem && <Alert type="error" showIcon message="Workspace 需要恢复" description={workspace.safeMessage || workspace.state} />}
            {resourceProblems.map((item) => <Alert key={item.id} type="error" showIcon message={`${item.name || item.id} · ${valueLabel(item.billingStatus || item.status)}`} description={item.safeMessage || item.lastBillingError || "请进入详情查看"} />)}
            {activeTickets.length > 0 && <Alert type="warning" showIcon message={`${activeTickets.length} 个处理中工单`} action={<a onClick={() => navigate(routeTo("support.list"))}>查看</a>} />}
          </div>
        ) : <Alert type="success" showIcon message="未发现需要处理的异常" />}
        <ActionGroup actions={[{ label: "提交工单", icon: <Headphones size={15} />, onClick: () => navigate(routeTo("support.create")) }]} />
      </InsightPanel>
    </ConsoleSurface>
  );
}
