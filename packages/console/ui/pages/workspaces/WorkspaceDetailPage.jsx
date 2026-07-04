import React from "react";
import { Button, Empty, Typography } from "antd";
import { Cable, HardDrive, Link as LinkIcon, RefreshCw, Server, Trash2 } from "lucide-react";
import {
  deleteWorkspaceToken,
  resetWorkspaceToken
} from "../../api/workspaces-api.js";
import { navigate, routeTo } from "../../consoleRoutes.js";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.jsx";
import { packageText, statusColor, statusLabel, valueLabel } from "../shared/formatters.js";

function toneForStatus(value) {
  const color = statusColor(value);
  if (color === "green") return "good";
  if (color === "red") return "danger";
  if (color === "orange") return "warn";
  return "info";
}

export function WorkspaceDetailPage({ selected, selectedPlan, state, session, runAction }) {
  if (!selected) {
    return (
      <ConsoleSurface title="Workspace" eyebrow="Delivery">
        <Empty description="暂无 Workspace" />
      </ConsoleSurface>
    );
  }
  const computeAllocationId = selected.computeAllocationId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeAllocationId);
  const storage = (state.storageVolumes || []).find((item) => item.id === selected.storageId);
  const attachment = (state.storageAttachments || []).find((item) => item.id === selected.attachmentId);
  return (
    <ConsoleSurface
      title={selected.name}
      eyebrow="Workspace detail"
      subtitle="URL entry linked to compute, storage, and attachment"
      extra={<Button onClick={() => navigate(routeTo("workspace.list"))}>返回列表</Button>}
    >
      <div className="consoleGrid equal">
        <InsightPanel
          title="Workspace URL"
          eyebrow="Access"
          actions={<StatusPill label={valueLabel(selected.access?.tokenStatus)} tone={selected.access?.tokenStatus === "active" ? "good" : "warn"} />}
        >
          <div className="stackList">
            <Typography.Text copyable={selected.access?.tokenStatus === "active"} className="inlineCode">{selected.url}</Typography.Text>
            <ActionGroup
              actions={[
                { label: "打开", icon: <LinkIcon size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => window.open(selected.url, "_blank", "noopener,noreferrer") },
                { label: "重置", icon: <RefreshCw size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => resetWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已重置") },
                { label: "停用", danger: true, icon: <Trash2 size={15} />, disabled: selected.access?.tokenStatus !== "active", onClick: () => runAction(() => deleteWorkspaceToken({ workspaceId: selected.id }, session.csrfToken), "URL 已停用") }
              ]}
            />
          </div>
        </InsightPanel>

        <InsightPanel title="计算与存储" eyebrow="Resources">
          <ResourceSplit
            items={[
              { label: "状态", value: statusLabel(selected), meta: selected.state, status: "Workspace", tone: toneForStatus(selected.state) },
              { label: "套餐", value: selectedPlan?.name || "-", meta: packageText(selectedPlan), status: "plan", tone: "info" },
              { label: "计算", value: compute?.name || computeAllocationId || "-", meta: valueLabel(compute?.status || selected.server?.status), status: "ComputeAllocation", tone: toneForStatus(compute?.status || selected.server?.status) },
              { label: "存储", value: storage?.name || selected.storageId || "-", meta: `${selected.disk?.sizeGb || storage?.sizeGb || 0}GB`, status: "StorageVolume", tone: toneForStatus(storage?.status || selected.disk?.status) },
              { label: "挂载关系", value: attachment?.id || selected.attachmentId || "-", meta: attachment?.mountPath || selected.disk?.mountPath || "/data", status: "StorageAttachment", tone: toneForStatus(attachment?.status) }
            ]}
          />
        </InsightPanel>
      </div>

      <div className="consoleGrid equal">
        <InsightPanel title="资源对象" eyebrow="Allocation, storage, attachment">
          <ActionGroup
            actions={[
              { label: "查看计算分配", icon: <Server size={15} />, disabled: !computeAllocationId, onClick: () => navigate(routeTo("compute-allocations.detail", { id: computeAllocationId })) },
              { label: "查看存储资源", icon: <HardDrive size={15} />, disabled: !selected.storageId, onClick: () => navigate(routeTo("storage.detail", { id: selected.storageId })) },
              { label: "查看挂载关系", icon: <Cable size={15} />, disabled: !selected.attachmentId, onClick: () => navigate(routeTo("attachment.detail", { id: selected.attachmentId })) }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
