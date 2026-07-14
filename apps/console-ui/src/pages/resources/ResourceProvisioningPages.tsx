import React from "react";
import { Alert, Button, Empty, Form, Input, InputNumber, Select, Switch } from "antd";
import { Cable, Database, Plus, RefreshCw, Server, Trash2 } from "lucide-react";
import {
  attachStorage,
  createComputeAllocation,
  createStorageVolume,
  destroyComputeAllocation,
  destroyStorageVolume,
  detachStorage,
  reactivateStorageVolume,
  setResourceAutoRenew,
  syncComputeAllocation,
  syncStorageVolume
} from "../../api/resources-api.ts";
import { previewPricing } from "../../api/pricing-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  BalanceChargePanel,
  ConsoleSurface,
  FailureRecoveryPanel,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  OperationConfirmButton,
  OperationResultPanel,
  OperationTimeline,
  PriceImpactPanel,
  ResourceRelationshipGraph,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { customerSafeMessage, moneyCents, paidThrough, usdMicros } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

const computeAllocationStages = Object.freeze(["已提交", "云资源准备中", "余额扣款中", "月度权益已激活", "Runtime 部署中", "URL 可用"]);
const storageCreateStages = Object.freeze(["已提交", "存储准备中", "余额扣款中", "月度权益已激活", "可挂载"]);
const storageDestroyStages = Object.freeze(["已提交", "停止续费", "销毁存储", "已删除"]);
const attachmentCreateStages = Object.freeze(["已提交", "挂载中", "可创建入口"]);
const attachmentDetachStages = Object.freeze(["已提交", "解除挂载", "存储保留"]);

function resourceStatus(value) {
  const normalized = String(value || "pending");
  if (["running", "bound", "attached", "ready", "active"].includes(normalized)) return "good";
  if (["destroyed", "failed", "detached", "external_deleted", "deleted", "missing", "quarantined"].includes(normalized)) return "danger";
  if (["creating", "attaching", "pending", "provisioning", "destroying"].includes(normalized)) return "warn";
  return "info";
}

function selectedResource(path, items) {
  const id = path.split("/").at(-1);
  return items.find((item) => item.id === id);
}

function previewAccountId(state: AnyRecord) {
  return state.account?.accountId || state.account?.id || state.user?.accountId || "";
}

function workspaceForResource(state: AnyRecord, resource: AnyRecord = {}) {
  return (state.workspaces || []).find((workspace) =>
    workspace.computeAllocationId === resource.id ||
    workspace.storageId === resource.id ||
    workspace.attachmentId === resource.id
  );
}

function billingStatusLabel(value) {
  return {
    preparing: "创建资源",
    charge_pending: "等待扣款",
    active: "已付费",
    failed: "开通失败",
    renewal_pending: "续费中",
    past_due: "续费失败",
    manual_review: "人工复核",
    retained: "已保留",
    stopped: "已停止",
    stopping: "停止中",
    pending: "等待中"
  }[value] || value || "等待中";
}

function billingStatusTone(value) {
  if (value === "active") return "good";
  if (["past_due", "manual_review", "failed"].includes(value)) return "danger";
  return "warn";
}

function supportContextPath(resource: AnyRecord = {}, resourceType = "resource") {
  const params = new URLSearchParams();
  params.set("category", "Workspace");
  params.set("priority", resource.safeMessage || resource.status === "failed" ? "high" : "normal");
  params.set("resourceType", resourceType);
  if (resource.id) params.set("resourceId", resource.id);
  if (resource.operationId) params.set("operationId", resource.operationId);
  if (resource.safeMessage) params.set("failureReason", customerSafeMessage(resource.safeMessage));
  return `${routeTo("support.create")}?${params.toString()}`;
}

function useOperationFeedback() {
  const [operationPending, setOperationPending] = React.useState(false);
  const [operationResult, setOperationResult] = React.useState(null);

  async function runOperation(action) {
    setOperationPending(true);
    setOperationResult({ ok: true, status: "submitted", resourceId: "" });
    const result = await action();
    setOperationPending(false);
    if (result === false) {
      setOperationResult({ ok: false, status: "failed", failureReason: "操作失败。" });
      return false;
    }
    if (result?.ok === false || result?.status === "failed" || result?.failureReason) {
      setOperationResult(result);
      return false;
    }
    setOperationResult(result);
    return result;
  }

  return { operationPending, operationResult, runOperation };
}

export function ComputeAllocationsPage({ state }: any) {
  const computeAllocations = state.computeAllocations || [];
  return (
    <ConsoleSurface
      title="计算资源"
      eyebrow="资源"
      subtitle="独占云主机资源"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("compute-allocations.create"))}>开通计算</Button>}
    >
      <MetricStrip
        items={[
          { label: "计算分配", value: computeAllocations.length, caption: "当前账号", tone: computeAllocations.length ? "info" : "neutral" },
          { label: "运行中", value: computeAllocations.filter((item) => item.status === "running").length, caption: "正在计费", tone: "good" }
        ]}
      />
      <InsightPanel title="计算分配" eyebrow="计算">
        <ObjectTable
          data={computeAllocations}
          emptyText="暂无计算分配"
          columns={[
            { title: "名称", dataIndex: "name", render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("compute-allocations.detail", { id: row.id }))}>{row.name || row.id}</Button> },
            { title: "规格", dataIndex: "spec" },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "pending"} tone={resourceStatus(value)} /> },
            { title: "月度权益", dataIndex: "billingStatus", render: (value) => <StatusPill label={billingStatusLabel(value)} tone={billingStatusTone(value)} /> },
            { title: "有效期至", dataIndex: "paidThrough", render: (value) => paidThrough(value) }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function ResourceRelationshipPage({ state }: any) {
  return (
    <ConsoleSurface
      title="资源关系"
      eyebrow="资源"
      subtitle="账号、计算、存储、挂载、工作区入口"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("workspace.create"))}>创建工作区入口</Button>}
    >
      <ResourceRelationshipGraph state={state} />
      <div className="consoleGrid equal">
        <InsightPanel title="数据提醒" eyebrow="安全">
          <ResourceSplit
            items={[
              { label: "停止计算", value: "数据保留", meta: "存储资源和 /data 数据仍保留", status: "安全", tone: "good" },
              { label: "删除存储", value: "会删除数据", meta: "删除前需要强确认", status: "高风险", tone: "danger" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="下一步" eyebrow="闭环">
          <ResourceSplit
            items={[
              { label: "没有计算", value: "开通计算", meta: "选择规格后创建独占资源", status: "下一步", tone: "info" },
              { label: "只有存储", value: "数据保留", meta: "开通计算后再挂载", status: "安全", tone: "good" },
              { label: "已挂载", value: "创建入口", meta: "生成可分发 URL", status: "交付", tone: "info" }
            ]}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}

export function CreateComputeAllocationPage({ state, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const purchaseKey = React.useRef(crypto.randomUUID());
  const availablePackages = (state.packages || []).filter((plan) => plan.available);
  const initialPackageId = availablePackages[0]?.id || "basic";
  const [form] = Form.useForm();
  const selectedPackageId = Form.useWatch("packageId", form) || initialPackageId;
  const selectedPlan = availablePackages.find((plan) => plan.id === selectedPackageId) || availablePackages[0];
  const [pricingPreview, setPricingPreview] = React.useState<AnyRecord | null>(null);
  React.useEffect(() => {
    let active = true;
    setPricingPreview(null);
    if (!selectedPackageId) return () => { active = false; };
    previewPricing({
      accountId: previewAccountId(state),
      resourceType: "compute",
      packageId: selectedPackageId
    }, session.csrfToken)
      .then((payload) => {
        if (active) setPricingPreview(payload);
      })
      .catch((error) => {
        if (active) setPricingPreview({ safeMessage: error.message });
      });
    return () => { active = false; };
  }, [selectedPackageId, session.csrfToken, state]);
  return (
    <ConsoleSurface title="开通计算资源" eyebrow="资源" subtitle="选择规格后提交开通" compact>
      <InsightPanel title="开通计算" eyebrow="计算">
        <Form
          form={form}
          layout="vertical"
          initialValues={{ packageId: initialPackageId }}
          onFinish={async (values) => {
            await runOperation(async () => {
              const created = await runAction(
                () => createComputeAllocation(values, session.csrfToken, purchaseKey.current),
                "计算资源开通请求已提交",
                { returnFailure: true }
              );
              return created && {
                ...created,
                status: created.status || "submitted",
                nextStepMessage: "计算资源开通请求已提交，正在创建云资源、完成月费扣款并分发 Docker，通常需要 3-5 分钟。"
              };
            });
          }}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入计算资源名称" }]}>
            <Input placeholder="输入计算资源名称" />
          </Form.Item>
          <Form.Item name="packageId" label="规格" rules={[{ required: true, message: "请选择规格" }]}>
            <Select
              options={availablePackages.map((plan) => ({
                label: `${plan.name} · ${plan.server} · ${plan.cpu} CPU / ${plan.memoryGb}GB`,
                value: plan.id
              }))}
            />
          </Form.Item>
          <ResourceSplit
            items={availablePackages.map((plan) => ({
              label: plan.name,
              value: `${plan.server} · ${plan.cpu} CPU / ${plan.memoryGb}GB`,
              meta: plan.id === selectedPackageId ? `${moneyCents(pricingPreview?.monthlyPriceCnyCents)}/月` : `${moneyCents(plan.price?.monthlyPriceCnyCents)}/月`,
              status: "可用",
              tone: "good"
            }))}
          />
          <PriceImpactPanel
            items={[
              { label: "套餐月价", value: `${moneyCents(pricingPreview?.monthlyPriceCnyCents)}/月`, meta: selectedPlan?.server || "-", status: "固定价", tone: "info" },
              { label: "钱包扣款", value: usdMicros(pricingPreview?.chargeUsdMicros), meta: "Sub2API USD", status: "月付", tone: "warn" },
              { label: "计费周期", value: "1 个日历月", meta: "自动续费可关闭", status: "预付", tone: "info" },
              { label: "预计等待", value: "3-5 分钟", meta: "扩容节点并部署 Runtime", status: "冷启动", tone: "info" }
            ]}
          />
          <BalanceChargePanel balance={state.balance} chargeUsdMicros={pricingPreview?.chargeUsdMicros} resourceLabel="计算资源" />
          <OperationResultPanel pending={operationPending} result={operationResult} />
          {operationResult && operationResult.ok !== false && (
            <ActionGroup actions={[
              { label: "查看计算节点", icon: <Server size={15} />, onClick: () => navigate(routeTo("compute-allocations.list")) },
              { label: "开通云硬盘", icon: <Database size={15} />, onClick: () => navigate(routeTo("storage.create")) }
            ]} />
          )}
          <Form.Item>
            <OperationConfirmButton
              label="开通计算"
              title="确认开通计算资源"
              description={`将从 Sub2API 余额扣除 ${usdMicros(pricingPreview?.chargeUsdMicros)}，开通一个日历月；参考价 ${moneyCents(pricingPreview?.monthlyPriceCnyCents)}/月。`}
              type="primary"
              icon={<Server size={15} />}
              disabled={!availablePackages.length || !pricingPreview || Boolean(pricingPreview.safeMessage)}
              loading={operationPending}
              onConfirm={() => form.submit()}
            />
          </Form.Item>
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function ComputeAllocationDetailPage({ state, path, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const resource = selectedResource(path, state.computeAllocations || []);
  if (!resource) return <ConsoleSurface title="计算资源" eyebrow="资源"><Empty description="未找到计算资源" /></ConsoleSurface>;
  const workspace = workspaceForResource(state, resource);
  const workspaceId = workspace?.id || resource.workspaceId || "";
  return (
    <ConsoleSurface title={resource.name || resource.id} eyebrow="计算详情" extra={<Button onClick={() => navigate(routeTo("compute-allocations.list"))}>返回列表</Button>}>
      <InsightPanel title="计算资源" eyebrow="资源">
        <ResourceSplit
          items={[
            { label: "状态", value: resource.status || "-", status: resource.status || "pending", tone: resourceStatus(resource.status) },
            { label: "云端状态", value: resource.providerStatus || "-", meta: resource.lastProviderSyncAt || "尚未同步", status: resource.providerStatus || "unknown", tone: resource.providerStatus === "missing" ? "danger" : "info" },
            { label: "规格", value: resource.spec || "-", meta: resource.packageId },
            { label: "月度权益", value: billingStatusLabel(resource.billingStatus), meta: `${moneyCents(resource.monthlyPriceCnyCents)}/月`, status: resource.billingStatus || "pending", tone: billingStatusTone(resource.billingStatus) },
            { label: "有效期至", value: paidThrough(resource.paidThrough), meta: resource.autoRenew ? "自动续费已开启" : "自动续费已关闭", status: resource.autoRenew ? "autoRenew" : "到期停止", tone: resource.autoRenew ? "good" : "warn" },
            { label: "绑定入口", value: workspace?.name || workspaceId || "-", meta: workspaceId || "尚未创建工作区入口" },
            { label: "同步错误", value: resource.lastProviderSyncError ? customerSafeMessage(resource.lastProviderSyncError) : "-", meta: "来自后端同步任务", status: resource.lastProviderSyncError ? "异常" : "正常", tone: resource.lastProviderSyncError ? "danger" : "neutral" },
            { label: "失败原因", value: resource.safeMessage ? customerSafeMessage(resource.safeMessage) : "-", meta: "如需帮助可提交工单", status: resource.safeMessage ? "异常" : "正常", tone: resource.safeMessage ? "danger" : "neutral" }
          ]}
        />
        <ActionGroup
          actions={[
            <Switch
              key="auto-renew-compute"
              checked={resource.autoRenew !== false}
              checkedChildren="自动续费"
              unCheckedChildren="到期停止"
              loading={operationPending}
              disabled={resource.status === "destroyed"}
              onChange={(checked) => runOperation(() => runAction(
                () => setResourceAutoRenew({ resourceId: resource.id, autoRenew: checked }, session.csrfToken, `resource-auto-renew:${resource.id}:${checked}`),
                checked ? "自动续费已开启" : "自动续费已关闭",
                { returnFailure: true }
              ))}
            />,
            <Button
              key="sync-compute"
              icon={<RefreshCw size={15} />}
              loading={operationPending}
              onClick={() => runOperation(() => runAction(
                () => syncComputeAllocation({ computeAllocationId: resource.id }, session.csrfToken),
                "云端状态已同步",
                { returnFailure: true }
              ))}
            >
              同步云端状态
            </Button>,
            <OperationConfirmButton
              key="destroy-compute"
              label="销毁计算资源"
              title="确认销毁计算资源"
              description="销毁后不再续费且不退还当前月费用；已保留的存储资源不会删除。"
              danger
              icon={<Trash2 size={15} />}
              disabled={resource.status === "destroyed"}
              loading={operationPending}
              onConfirm={() => runOperation(() => runAction(
                () => destroyComputeAllocation({ computeAllocationId: resource.id, confirm: true }, session.csrfToken),
                "计算资源销毁请求已提交",
                { returnFailure: true }
              ))}
            />
          ]}
        />
        <OperationResultPanel pending={operationPending} result={operationResult} />
      </InsightPanel>
      <div className="consoleGrid equal">
        <InsightPanel title="操作时间线" eyebrow="进度">
          <OperationTimeline operations={state.runtimeOperations || []} resourceId={resource.id} stages={computeAllocationStages} />
        </InsightPanel>
        <InsightPanel title="失败处理" eyebrow="恢复">
          <FailureRecoveryPanel
            resource={resource}
            supportAction={() => navigate(supportContextPath(resource, "compute"))}
          />
        </InsightPanel>
      </div>
      <InsightPanel title="数据提醒" eyebrow="安全">
        <ResourceSplit items={[{ label: "销毁计算", value: "数据保留", meta: "存储资源和 /data 数据不会删除", status: "安全", tone: "good" }]} />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function StorageVolumesPage({ state }: any) {
  const storageVolumes = state.storageVolumes || [];
  return (
    <ConsoleSurface
      title="存储资源"
      eyebrow="资源"
      subtitle="可独立保留的数据盘"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("storage.create"))}>开通存储</Button>}
    >
      <InsightPanel title="存储卷" eyebrow="存储">
        <ObjectTable
          data={storageVolumes}
          emptyText="暂无存储资源"
          columns={[
            { title: "名称", dataIndex: "name", render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("storage.detail", { id: row.id }))}>{row.name || row.id}</Button> },
            { title: "容量", dataIndex: "sizeGb", render: (value) => `${value || 0}GB` },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "pending"} tone={resourceStatus(value)} /> },
            { title: "月度权益", dataIndex: "billingStatus", render: (value) => <StatusPill label={billingStatusLabel(value)} tone={billingStatusTone(value)} /> },
            { title: "有效期至", dataIndex: "paidThrough", render: (value) => paidThrough(value) }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function CreateStorageVolumePage({ state, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const purchaseKey = React.useRef(crypto.randomUUID());
  const availablePackages = (state.packages || []).filter((plan) => plan.available);
  const initialPackageId = availablePackages[0]?.id || "basic";
  const [form] = Form.useForm();
  const selectedPackageId = Form.useWatch("packageId", form) || initialPackageId;
  const selectedSizeGb = Form.useWatch("sizeGb", form) || availablePackages[0]?.diskGb || 10;
  const selectedPlan = availablePackages.find((plan) => plan.id === selectedPackageId) || availablePackages[0];
  const [pricingPreview, setPricingPreview] = React.useState<AnyRecord | null>(null);
  React.useEffect(() => {
    let active = true;
    setPricingPreview(null);
    if (!selectedPackageId) return () => { active = false; };
    previewPricing({
      accountId: previewAccountId(state),
      resourceType: "storage",
      packageId: selectedPackageId,
      sizeGb: selectedSizeGb
    }, session.csrfToken)
      .then((payload) => {
        if (active) setPricingPreview(payload);
      })
      .catch((error) => {
        if (active) setPricingPreview({ safeMessage: error.message });
      });
    return () => { active = false; };
  }, [selectedPackageId, selectedSizeGb, session.csrfToken, state]);
  return (
    <ConsoleSurface title="开通存储资源" eyebrow="资源" subtitle="创建可独立保留的数据盘" compact>
      <InsightPanel title="开通存储" eyebrow="存储">
        <Form
          form={form}
          layout="vertical"
          initialValues={{ packageId: initialPackageId, sizeGb: availablePackages[0]?.diskGb || 10 }}
          onFinish={async (values) => {
            await runOperation(async () => {
              const created = await runAction(
                () => createStorageVolume(values, session.csrfToken, purchaseKey.current),
                "云硬盘开通请求已提交",
                { returnFailure: true }
              );
              return created && {
                ...created,
                status: created.status || "submitted",
                nextStepMessage: "云硬盘开通请求已提交，正在创建并准备挂载。通常需要 3-5 分钟。"
              };
            });
          }}
        >
          <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入存储名称" }]}>
            <Input placeholder="输入存储资源名称" />
          </Form.Item>
          <Form.Item name="packageId" label="计费规格" rules={[{ required: true, message: "请选择计费规格" }]}>
            <Select
              options={availablePackages.map((plan) => ({
                label: `${plan.name} · 默认 ${plan.diskGb}GB`,
                value: plan.id
              }))}
            />
          </Form.Item>
          <Form.Item name="sizeGb" label="容量 GB" rules={[{ required: true, message: "请输入容量" }]}>
            <InputNumber min={10} max={4090} step={10} precision={0} style={{ width: "100%" }} />
          </Form.Item>
          <PriceImpactPanel
            items={[
              { label: "存储月价", value: `${moneyCents(pricingPreview?.monthlyPriceCnyCents)}/月`, meta: "每 10GB ¥18/月", status: "固定价", tone: "info" },
              { label: "容量", value: `${selectedSizeGb}GB`, meta: selectedPlan?.name || selectedPackageId, status: "当前容量", tone: "info" },
              { label: "钱包扣款", value: usdMicros(pricingPreview?.chargeUsdMicros), meta: "Sub2API USD", status: "月付", tone: "warn" },
              { label: "计费周期", value: "1 个日历月", meta: "到期存储保留但不可挂载", status: "预付", tone: "info" }
            ]}
          />
          <BalanceChargePanel balance={state.balance} chargeUsdMicros={pricingPreview?.chargeUsdMicros} resourceLabel="存储资源" />
          <ResourceSplit items={[{ label: "数据目录", value: "/data", meta: "用户文件保存位置", status: "可挂载", tone: "info" }]} />
          <OperationTimeline operations={[]} stages={storageCreateStages} emptyText="提交后开始创建存储" />
          <OperationResultPanel pending={operationPending} result={operationResult} />
          {operationResult && operationResult.ok !== false && (
            <ActionGroup actions={[
              { label: "查看云硬盘", icon: <Database size={15} />, onClick: () => navigate(routeTo("storage.list")) },
              { label: "挂载存储", icon: <Cable size={15} />, onClick: () => navigate(routeTo("attachment.create")) }
            ]} />
          )}
          <Form.Item>
            <OperationConfirmButton
              label="开通存储"
              title="确认开通存储资源"
              description={`将从 Sub2API 余额扣除 ${usdMicros(pricingPreview?.chargeUsdMicros)}，开通一个日历月；参考价 ${moneyCents(pricingPreview?.monthlyPriceCnyCents)}/月。`}
              type="primary"
              icon={<Database size={15} />}
              disabled={!availablePackages.length || !pricingPreview || Boolean(pricingPreview.safeMessage)}
              loading={operationPending}
              onConfirm={() => form.submit()}
            />
          </Form.Item>
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function StorageVolumeDetailPage({ state, path, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const reactivationKey = React.useRef(crypto.randomUUID());
  const resource = selectedResource(path, state.storageVolumes || []);
  if (!resource) return <ConsoleSurface title="存储资源" eyebrow="资源"><Empty description="未找到存储资源" /></ConsoleSurface>;
  const workspace = workspaceForResource(state, resource);
  const workspaceId = workspace?.id || resource.workspaceId || "";
  return (
    <ConsoleSurface title={resource.name || resource.id} eyebrow="存储详情" extra={<Button onClick={() => navigate(routeTo("storage.list"))}>返回列表</Button>}>
      <InsightPanel title="存储资源" eyebrow="资源">
        <ResourceSplit
          items={[
            { label: "状态", value: resource.status || "-", status: resource.status || "pending", tone: resourceStatus(resource.status) },
            { label: "云端状态", value: resource.providerStatus || "-", meta: resource.lastProviderSyncAt || "尚未同步", status: resource.providerStatus || "unknown", tone: resource.providerStatus === "missing" ? "danger" : "info" },
            { label: "容量", value: `${resource.sizeGb || 0}GB`, meta: "当前存储容量" },
            { label: "月度权益", value: billingStatusLabel(resource.billingStatus), meta: `${moneyCents(resource.monthlyPriceCnyCents)}/月`, status: resource.billingStatus || "pending", tone: billingStatusTone(resource.billingStatus) },
            { label: "有效期至", value: paidThrough(resource.paidThrough), meta: resource.autoRenew ? "自动续费已开启" : "自动续费已关闭", status: resource.autoRenew ? "autoRenew" : "到期保留", tone: resource.autoRenew ? "good" : "warn" },
            { label: "绑定入口", value: workspace?.name || workspaceId || "-", meta: workspaceId || "尚未创建工作区入口" },
            { label: "同步错误", value: resource.lastProviderSyncError ? customerSafeMessage(resource.lastProviderSyncError) : "-", meta: "来自后端同步任务", status: resource.lastProviderSyncError ? "异常" : "正常", tone: resource.lastProviderSyncError ? "danger" : "neutral" },
            { label: "失败原因", value: resource.safeMessage ? customerSafeMessage(resource.safeMessage) : "-", meta: "如需帮助可提交工单", status: resource.safeMessage ? "异常" : "正常", tone: resource.safeMessage ? "danger" : "neutral" }
          ]}
        />
        <ActionGroup
          actions={[
            resource.billingStatus === "retained" && <OperationConfirmButton
              key="reactivate-storage"
              label="重新激活存储"
              title="确认重新激活存储"
              description={`将从 Sub2API 余额扣除 ${usdMicros(resource.chargeUsdMicros)}，恢复一个日历月的存储权益。`}
              type="primary"
              icon={<Database size={15} />}
              loading={operationPending}
              onConfirm={() => runOperation(() => runAction(
                () => reactivateStorageVolume({
                  id: resource.id,
                  name: resource.name,
                  packageId: resource.packageId || "basic",
                  sizeGb: resource.sizeGb,
                  workspaceId
                }, session.csrfToken, reactivationKey.current),
                "存储权益重新激活",
                { returnFailure: true }
              ))}
            />,
            <Switch
              key="auto-renew-storage"
              checked={resource.autoRenew !== false}
              checkedChildren="自动续费"
              unCheckedChildren="到期保留"
              loading={operationPending}
              disabled={resource.status === "destroyed" || resource.billingStatus === "retained"}
              onChange={(checked) => runOperation(() => runAction(
                () => setResourceAutoRenew({ resourceId: resource.id, autoRenew: checked }, session.csrfToken, `resource-auto-renew:${resource.id}:${checked}`),
                checked ? "自动续费已开启" : "自动续费已关闭",
                { returnFailure: true }
              ))}
            />,
            <Button
              key="sync-storage"
              icon={<RefreshCw size={15} />}
              loading={operationPending}
              onClick={() => runOperation(() => runAction(
                () => syncStorageVolume({ storageId: resource.id }, session.csrfToken),
                "云端状态已同步",
                { returnFailure: true }
              ))}
            >
              同步云端状态
            </Button>,
            <OperationConfirmButton
              key="destroy-storage"
              label="销毁存储资源"
              title="确认销毁存储资源"
              description="这会删除 /data 中的用户文件；如仍有挂载关系，请先解除挂载。"
              confirmText="确认删除数据"
              strong
              danger
              icon={<Trash2 size={15} />}
              disabled={resource.status === "destroyed" || resource.status === "attached"}
              loading={operationPending}
              onConfirm={() => runOperation(() => runAction(
                () => destroyStorageVolume({ storageId: resource.id, confirmDataLoss: true }, session.csrfToken),
                "存储资源销毁请求已提交",
                { returnFailure: true }
              ))}
            />
          ]}
        />
        <OperationResultPanel pending={operationPending} result={operationResult} />
      </InsightPanel>
      <div className="consoleGrid equal">
        <InsightPanel title="操作时间线" eyebrow="进度">
          <OperationTimeline operations={state.runtimeOperations || []} resourceId={resource.id} stages={storageDestroyStages} />
        </InsightPanel>
        <InsightPanel title="失败处理" eyebrow="恢复">
          <FailureRecoveryPanel
            resource={resource}
            supportAction={() => navigate(supportContextPath(resource, "storage"))}
          />
        </InsightPanel>
      </div>
      <InsightPanel title="删除提醒" eyebrow="安全">
        <ResourceSplit items={[{ label: "销毁存储", value: "会删除数据", meta: "删除 /data 用户文件，需要强确认", status: "高风险", tone: "danger" }]} />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function StorageAttachmentsPage({ state }: any) {
  const attachments = state.storageAttachments || [];
  return (
    <ConsoleSurface
      title="挂载关系"
      eyebrow="资源"
      subtitle="把存储挂载到计算资源"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("attachment.create"))}>挂载存储</Button>}
    >
      <InsightPanel title="挂载关系" eyebrow="挂载">
        <ObjectTable
          data={attachments}
          emptyText="暂无挂载关系"
          columns={[
            { title: "挂载", dataIndex: "id" },
            { title: "计算", dataIndex: "computeAllocationId", render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("attachment.detail", { id: row.id }))}>{row.computeAllocationId}</Button> },
            { title: "存储", dataIndex: "storageId" },
            { title: "路径", dataIndex: "mountPath" },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "pending"} tone={resourceStatus(value)} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function StorageAttachmentDetailPage({ state, path, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const attachment = selectedResource(path, state.storageAttachments || []);
  if (!attachment) return <ConsoleSurface title="挂载关系" eyebrow="资源"><Empty description="未找到挂载关系" /></ConsoleSurface>;
  return (
    <ConsoleSurface title={attachment.id} eyebrow="挂载详情" extra={<Button onClick={() => navigate(routeTo("attachment.list"))}>返回列表</Button>}>
      <InsightPanel title="挂载关系" eyebrow="资源">
        <ResourceSplit
          items={[
            { label: "状态", value: attachment.status || "-", status: attachment.status || "pending", tone: resourceStatus(attachment.status) },
            { label: "计算", value: attachment.computeAllocationId || "-", meta: "计算资源" },
            { label: "存储", value: attachment.storageId || "-", meta: attachment.mountPath || "/data" }
          ]}
        />
        <ActionGroup
          actions={[
            <OperationConfirmButton
              key="detach-storage"
              label="解除挂载"
              title="确认解除挂载"
              description="解除后计算资源不再访问该存储；存储资源和数据保留。"
              danger
              icon={<Trash2 size={15} />}
              disabled={attachment.status !== "attached"}
              loading={operationPending}
              onConfirm={() => runOperation(() => runAction(
                () => detachStorage({ attachmentId: attachment.id, confirm: true }, session.csrfToken),
                "解除挂载请求已提交",
                { returnFailure: true }
              ))}
            />
          ]}
        />
        <OperationResultPanel pending={operationPending} result={operationResult} />
      </InsightPanel>
      <InsightPanel title="操作时间线" eyebrow="进度">
        <OperationTimeline operations={state.runtimeOperations || []} resourceId={attachment.id} stages={attachmentDetachStages} />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function CreateStorageAttachmentPage({ state, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const [form] = Form.useForm();
  const computeAllocations = (state.computeAllocations || []).filter((item) => item.status === "running");
  const storageVolumes = (state.storageVolumes || []).filter((item) => !["destroyed", "attached"].includes(item.status));
  const canAttach = computeAllocations.length > 0 && storageVolumes.length > 0;
  return (
    <ConsoleSurface title="挂载存储" eyebrow="资源" subtitle="选择计算资源和存储资源" compact>
      <InsightPanel title="挂载存储" eyebrow="挂载">
        {!canAttach && <Alert type="warning" showIcon message="需要一个运行中的计算资源和一个未挂载存储卷。" />}
        <Form
          form={form}
          layout="vertical"
          initialValues={{
            computeAllocationId: computeAllocations[0]?.id,
            storageId: storageVolumes[0]?.id,
            mountPath: "/data"
          }}
          onFinish={async (values) => {
            await runOperation(async () => {
              const created = await runAction(
                () => attachStorage(values, session.csrfToken),
                "挂载请求已提交",
                { returnFailure: true }
              );
              return created && {
                ...created,
                status: created.status || "submitted",
                nextStepMessage: "挂载请求已提交，完成后即可创建工作空间 URL。"
              };
            });
          }}
        >
          <Form.Item name="computeAllocationId" label="计算资源" rules={[{ required: true, message: "请选择计算资源" }]}>
            <Select options={computeAllocations.map((item) => ({ label: `${item.name || item.id} · ${item.status || "等待中"}`, value: item.id }))} />
          </Form.Item>
          <Form.Item name="storageId" label="存储资源" rules={[{ required: true, message: "请选择存储资源" }]}>
            <Select options={storageVolumes.map((item) => ({ label: `${item.name || item.id} · ${item.sizeGb}GB`, value: item.id }))} />
          </Form.Item>
          <Form.Item name="mountPath" label="挂载路径" rules={[{ required: true, message: "请输入挂载路径" }]}>
            <Input />
          </Form.Item>
          <ResourceSplit items={[{ label: "数据目录", value: "/data", meta: "用户文件保存位置", status: "必需", tone: "info" }]} />
          <OperationTimeline operations={[]} stages={attachmentCreateStages} emptyText="提交后开始挂载" />
          <OperationResultPanel pending={operationPending} result={operationResult} />
          {operationResult && operationResult.ok !== false && (
            <ActionGroup actions={[
              { label: "查看挂载关系", icon: <Cable size={15} />, onClick: () => navigate(routeTo("attachment.list")) },
              { label: "创建工作空间 URL", icon: <Plus size={15} />, onClick: () => navigate(routeTo("workspace.create")) }
            ]} />
          )}
          <Form.Item>
            <OperationConfirmButton
              label="挂载存储"
              title="确认挂载存储资源"
              description="挂载后工作区会从 /data 读写用户文件。"
              type="primary"
              icon={<Cable size={15} />}
              disabled={!canAttach}
              loading={operationPending}
              onConfirm={() => form.submit()}
            />
          </Form.Item>
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}
