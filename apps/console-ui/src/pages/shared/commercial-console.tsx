// @ts-nocheck
// ponytail: migrated JS first; replace with domain DTO types when these pages change behavior.
import React from "react";
import { PageContainer, ProTable } from "@ant-design/pro-components";
import { Alert, Button, Empty, List, Popconfirm, Space, Tag, Typography } from "antd";
import { available, money } from "./formatters.ts";

function toneClass(tone = "neutral") {
  return ["good", "warn", "danger", "info"].includes(tone) ? tone : "neutral";
}

function tagColor(tone = "neutral") {
  return {
    good: "green",
    warn: "orange",
    danger: "red",
    info: "blue",
    neutral: "default"
  }[toneClass(tone)];
}

function statusText(value = "") {
  return {
    submitted: "已提交",
    running: "处理中",
    provisioning: "创建中",
    creating: "创建中",
    attaching: "挂载中",
    detaching: "解除中",
    destroying: "删除中",
    completed: "已完成",
    available: "可用",
    attached: "已挂载",
    detached: "已解除",
    destroyed: "已删除",
    failed: "失败",
    pending: "等待中"
  }[value] || value || "等待中";
}

const resourceOperationStages = Object.freeze([
  "已提交",
  "冻结余额",
  "云资源创建中",
  "Runtime 部署中",
  "存储挂载中",
  "URL 可用"
]);

export function ConsoleSurface({ title, eyebrow, subtitle, extra, children, compact = false }: any) {
  return (
    <PageContainer
      title={(
        <div className="surfaceTitle">
          {eyebrow && <span>{eyebrow}</span>}
          <strong>{title}</strong>
        </div>
      )}
      subTitle={subtitle}
      extra={extra}
    >
      <div className={compact ? "consoleSurface compact" : "consoleSurface"}>
        {children}
      </div>
    </PageContainer>
  );
}

export function MetricStrip({ items = [] }: any) {
  return (
    <section className="metricStrip" aria-label="Console metrics">
      {items.map((item) => (
        <article className={`metricTile ${toneClass(item.tone)}`} key={item.label}>
          <div className="metricTopline">
            <span>{item.label}</span>
            {item.icon && <span className="metricIcon">{item.icon}</span>}
          </div>
          <strong>{item.value}</strong>
          {item.caption && <small>{item.caption}</small>}
        </article>
      ))}
    </section>
  );
}

export function InsightPanel({ title, eyebrow, actions, children, tone = "neutral", className = "" }: any) {
  return (
    <section className={`insightPanel ${toneClass(tone)} ${className}`.trim()}>
      <header>
        <div>
          {eyebrow && <span>{eyebrow}</span>}
          <h2>{title}</h2>
        </div>
        {actions && <div className="panelActions">{actions}</div>}
      </header>
      <div className="panelBody">{children}</div>
    </section>
  );
}

export function StatusPill({ label, tone = "neutral" }: any) {
  return <Tag color={tagColor(tone)} className="statusPill">{label}</Tag>;
}

export function ResourceSplit({ items = [] }: any) {
  return (
    <div className="resourceSplit">
      {items.map((item) => (
        <article key={item.label}>
          <div className="resourceLabel">
            <span>{item.label}</span>
            {item.status && <StatusPill label={item.status} tone={item.tone} />}
          </div>
          <strong>{item.value}</strong>
          {item.meta && <small>{item.meta}</small>}
        </article>
      ))}
    </div>
  );
}

export function ActionGroup({ actions = [] }: any) {
  return (
    <Space wrap size={8} className="actionGroup">
      {actions.map((action) => {
        if (React.isValidElement(action)) return action;
        return (
          <Button
            key={action.key || action.label}
            type={action.type}
            danger={action.danger}
            icon={action.icon}
            disabled={action.disabled}
            onClick={action.onClick}
          >
            {action.label}
          </Button>
        );
      })}
    </Space>
  );
}

export function OperationConfirmButton({
  label,
  title,
  description,
  confirmText,
  strong = false,
  danger = false,
  type,
  icon,
  disabled = false,
  loading = false,
  onConfirm
}: any) {
  const body = strong && confirmText
    ? `${description || "请确认此操作。"} 需要确认：${confirmText}`
    : description || "请确认此操作。";
  return (
    <Popconfirm
      title={title || label}
      description={body}
      okText="确认"
      cancelText="取消"
      okButtonProps={{ danger }}
      disabled={disabled || loading}
      onConfirm={onConfirm}
    >
      <Button type={type} danger={danger} icon={icon} disabled={disabled} loading={loading}>
        {label}
      </Button>
    </Popconfirm>
  );
}

export function OperationResultPanel({ result, pending = false }: any) {
  if (pending) {
    return (
      <Alert
        type="info"
        showIcon
        message="操作已提交"
        description="正在冻结余额、创建云资源并部署 Runtime，通常需要 3-5 分钟。"
      />
    );
  }
  if (!result) return null;
  const failed = result.ok === false || result.status === "failed" || Boolean(result.failureReason);
  return (
    <Alert
      type={failed ? "error" : "success"}
      showIcon
      message={failed ? "操作失败" : "操作完成"}
      description={failed ? result.failureReason || "请查看失败原因后重试。" : `资源 ${result.resourceId || result.id || "-"} 已更新。`}
    />
  );
}

export function TimelineList({ items = [], emptyText = "暂无记录" }: any) {
  if (!items.length) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} />;
  }

  return (
    <List
      className="timelineList"
      dataSource={items}
      renderItem={(item) => (
        <List.Item>
          <div className={`timelineDot ${toneClass(item.tone)}`} />
          <div className="timelineContent">
            <strong>{item.title}</strong>
            {item.description && <Typography.Text type="secondary">{item.description}</Typography.Text>}
          </div>
          {item.meta && <Typography.Text type="secondary" className="timelineMeta">{item.meta}</Typography.Text>}
        </List.Item>
      )}
    />
  );
}

export function ObjectTable({ rowKey = "id", data = [], columns = [], emptyText = "暂无数据", ...rest }: any) {
  return (
    <ProTable
      className="objectTable"
      rowKey={rowKey}
      search={false}
      options={false}
      pagination={false}
      size="small"
      scroll={{ x: "max-content" }}
      dataSource={data}
      columns={columns}
      locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} /> }}
      {...rest}
    />
  );
}

export function PriceImpactPanel({ items = [], emptyText = "暂无价格信息" }: any) {
  return <ResourceSplit items={items.length ? items : [{ label: "价格", value: "-", meta: emptyText, status: "pending", tone: "neutral" }]} />;
}

export function WalletRiskPanel({ wallet = {}, requiredHold = 0, resourceLabel = "资源" }: any) {
  const currentAvailable = available(wallet);
  const afterHold = Math.max(0, currentAvailable - Number(requiredHold || 0));
  const blocked = Number(requiredHold || 0) > currentAvailable;
  const lowAfter = !blocked && afterHold < Math.max(10, Number(requiredHold || 0) * 0.2);
  const message = blocked ? "余额不足" : lowAfter ? "冻结后余额偏低" : "余额充足";
  const description = blocked
    ? `${resourceLabel} 需要冻结 ${money(requiredHold)}，当前可用 ${money(currentAvailable)}，可能无法开通。`
    : `${resourceLabel} 冻结后预计可用 ${money(afterHold)}。`;
  return (
    <Alert
      type={blocked ? "error" : lowAfter ? "warning" : "success"}
      showIcon
      message={message}
      description={description}
    />
  );
}

export function DataRetentionPolicyPanel({ compact = false }: any) {
  const items = [
    { label: "销毁计算", value: "只删除计算", meta: "存储资源和 /data 数据保留", status: "数据保留", tone: "good" },
    { label: "销毁存储", value: "删除数据", meta: "会删除 /data 用户文件，需要强确认", status: "高风险", tone: "danger" },
    { label: "迁移 / rollout", value: "不删数据", meta: "用户、账单在后端账本；用户文件在持久存储", status: "持久化", tone: "info" }
  ];
  if (compact) return <ResourceSplit items={items} />;
  return (
    <InsightPanel title="数据保留策略" eyebrow="数据安全">
      <ResourceSplit items={items} />
    </InsightPanel>
  );
}

export function ResourceRelationshipGraph({ state = {}, title = "资源关系" }: any) {
  const compute = state.computeAllocations || [];
  const storage = state.storageVolumes || [];
  const attachments = state.storageAttachments || [];
  const workspaces = state.workspaces || [];
  const nodes = [
    { label: "账号", value: state.account?.id || state.wallet?.accountId || state.user?.accountId || "-", tone: "info" },
    { label: "计算", value: `${compute.length} 个`, tone: compute.some((item) => item.status === "failed") ? "danger" : compute.length ? "good" : "neutral" },
    { label: "存储", value: `${storage.length} 个`, tone: storage.some((item) => item.status === "failed") ? "danger" : storage.length ? "good" : "neutral" },
    { label: "挂载", value: `${attachments.length} 个`, tone: attachments.some((item) => item.status === "failed") ? "danger" : attachments.length ? "good" : "neutral" },
    { label: "工作区入口", value: `${workspaces.length} 个`, tone: workspaces.some((item) => item.state === "failed") ? "danger" : workspaces.length ? "good" : "neutral" }
  ];
  return (
    <InsightPanel title={title} eyebrow="账号 -> 计算 -> 存储 -> 挂载 -> 工作区入口">
      <div className="relationshipGraph" aria-label="账号 计算 存储 挂载 工作区入口">
        {nodes.map((node, index) => (
          <React.Fragment key={node.label}>
            <article className={`relationshipNode ${toneClass(node.tone)}`}>
              <span>{node.label}</span>
              <strong>{node.value}</strong>
            </article>
            {index < nodes.length - 1 && <span className="relationshipArrow">→</span>}
          </React.Fragment>
        ))}
      </div>
    </InsightPanel>
  );
}

export function ProductionE2EPanel({ summary = {} }: any) {
  const recent = summary.recent || [];
  return (
    <InsightPanel title="真实 E2E 记录" eyebrow="上线验证">
      <MetricStrip
        items={[
          { label: "记录", value: summary.total || 0, caption: "生产验证", tone: summary.total ? "info" : "neutral" },
          { label: "通过", value: summary.passed || 0, caption: "最近记录", tone: summary.passed ? "good" : "neutral" },
          { label: "失败", value: summary.failed || 0, caption: "需处理", tone: summary.failed ? "danger" : "good" }
        ]}
      />
      <TimelineList
        emptyText="暂无 E2E 记录"
        items={recent.map((run) => ({
          title: run.runId,
          description: `${run.accountId || "-"} · ${run.workspaceId || "-"}`,
          meta: `${run.status === "passed" ? "通过" : run.status} · ${(run.checks || []).join(", ")}`,
          tone: run.status === "passed" ? "good" : run.status === "failed" ? "danger" : "warn"
        }))}
      />
    </InsightPanel>
  );
}

export function OperationTimeline({ operations = [], resourceId = "", emptyText = "暂无操作记录", stages = resourceOperationStages }: any) {
  const scopedOperations = operations
    .filter((operation) => !resourceId || operation.resourceId === resourceId || operation.workspaceId === resourceId)
    .slice(-8)
    .reverse();
  const scoped = scopedOperations.length
    ? scopedOperations.map((operation) => ({
        title: {
          create_compute_allocation: "开通计算资源",
          create_storage_volume: "开通存储资源",
          attach_storage: "挂载存储资源",
          create_workspace: "创建工作区入口"
        }[operation.operationType || operation.type] || "资源操作",
        description: operation.safeMessage || operation.error || operation.resourceId || operation.workspaceId,
        meta: statusText(operation.status) || operation.updatedAt || operation.createdAt,
        tone: operation.status === "failed" ? "danger" : operation.status === "completed" ? "good" : "info"
      }))
    : stages.map((stage, index) => ({
        title: stage,
        description: index === 0 ? emptyText : "等待上一步完成",
        meta: index === 0 ? "等待中" : "未开始",
        tone: index === 0 ? "info" : "neutral"
      }));
  return <TimelineList items={scoped} emptyText={emptyText} />;
}

export function FailureRecoveryPanel({ resource, supportAction, cleanupAction }: any) {
  const failed = Boolean(resource?.safeMessage || resource?.error || resource?.status === "failed");
  if (!failed) {
    return (
      <Alert
        type="success"
        showIcon
        message="资源状态正常"
        description="失败原因、重试入口和工单入口会在异常时显示。"
      />
    );
  }
  return (
    <div className="stackList">
      <Alert
        type="error"
        showIcon
        message={resource.safeMessage || resource.error || "资源操作失败"}
        description={resource.providerRequestId || resource.operationId || resource.id}
      />
      <ActionGroup actions={[
        cleanupAction && { label: "清理无效入口", danger: true, onClick: cleanupAction },
        supportAction && { label: "提交工单", type: "primary", onClick: supportAction }
      ].filter(Boolean)} />
    </div>
  );
}

export function CleanupResourceTable({ workspaces = [], computeAllocations = [], storageVolumes = [], storageAttachments = [], onCleanup }: any) {
  const computeById = new Map(computeAllocations.map((item) => [item.id, item]));
  const storageById = new Map(storageVolumes.map((item) => [item.id, item]));
  const attachmentById = new Map(storageAttachments.map((item) => [item.id, item]));
  const rows = workspaces
    .filter((workspace) => workspace.access?.tokenStatus === "active")
    .map((workspace) => {
      const compute = computeById.get(workspace.currentComputeAllocationId);
      const storage = storageById.get(workspace.storageId);
      const attachment = attachmentById.get(workspace.currentAttachmentId);
      const unavailable = [
        !storage || storage.status === "destroyed" ? "storage" : "",
      ].filter(Boolean);
      return {
        id: workspace.id,
        name: workspace.name,
        accountId: workspace.ownerAccountId,
        tokenStatus: workspace.access?.tokenStatus,
        computeStatus: compute?.status || workspace.runtime?.status || "suspended",
        storageStatus: storage?.status || "missing",
        attachmentStatus: attachment?.status || workspace.runtime?.status || "suspended",
        cleanupNeeded: unavailable.length > 0,
        unavailable: unavailable.join(", ") || "none"
      };
    });
  return (
    <ObjectTable
      data={rows}
      emptyText="暂无需要检查的 Workspace URL"
      columns={[
        { title: "Workspace", dataIndex: "name", render: (_, row) => <Typography.Text className="inlineCode">{row.name || row.id}</Typography.Text> },
        { title: "账号", dataIndex: "accountId", ellipsis: true },
        { title: "URL", dataIndex: "tokenStatus", render: (value) => <StatusPill label={value} tone={value === "active" ? "good" : "warn"} /> },
        { title: "计算", dataIndex: "computeStatus", render: (value) => <StatusPill label={value} tone={value === "destroyed" || value === "missing" ? "danger" : "good"} /> },
        { title: "存储", dataIndex: "storageStatus", render: (value) => <StatusPill label={value} tone={value === "destroyed" || value === "missing" ? "danger" : "good"} /> },
        { title: "挂载", dataIndex: "attachmentStatus", render: (value) => <StatusPill label={value} tone={value === "detached" || value === "missing" ? "danger" : "good"} /> },
        {
          title: "操作",
          valueType: "option",
          render: (_, row) => (
            <Button danger disabled={!row.cleanupNeeded} onClick={() => onCleanup?.(row)}>
              清理 URL
            </Button>
          )
        }
      ]}
    />
  );
}
