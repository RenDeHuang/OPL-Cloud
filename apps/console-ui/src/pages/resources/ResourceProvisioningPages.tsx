import React from "react";
import { Alert, Button, Empty, Form, Input, InputNumber, Select } from "antd";
import { Cable, Database, Plus, Server, Trash2 } from "lucide-react";
import {
  attachStorage,
  createComputeAllocation,
  createStorageVolume,
  destroyComputeAllocation,
  destroyStorageVolume,
  detachStorage
} from "../../api/resources-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
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
  StatusPill,
  WalletRiskPanel
} from "../shared/commercial-console.tsx";
import { available, money } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

const computeAllocationStages = Object.freeze(["已提交", "冻结余额", "云资源创建中", "Runtime 部署中", "存储挂载中", "URL 可用"]);
const storageCreateStages = Object.freeze(["已提交", "冻结余额", "存储创建中", "可挂载"]);
const storageDestroyStages = Object.freeze(["已提交", "释放冻结", "销毁存储", "已删除"]);
const attachmentCreateStages = Object.freeze(["已提交", "挂载中", "可创建入口"]);
const attachmentDetachStages = Object.freeze(["已提交", "解除挂载", "存储保留"]);

function resourceStatus(value) {
  const normalized = String(value || "pending");
  if (["running", "bound", "attached", "ready", "active"].includes(normalized)) return "good";
  if (["destroyed", "failed", "detached"].includes(normalized)) return "danger";
  if (["creating", "attaching", "pending"].includes(normalized)) return "warn";
  return "info";
}

function selectedResource(path, items) {
  const id = path.split("/").at(-1);
  return items.find((item) => item.id === id);
}

function computeHourlyPrice(plan) {
  return Number(plan?.price?.computeHourly || 0);
}

function computeHoldAmount(plan) {
  return computeHourlyPrice(plan) * 24 * 7;
}

function storageGbMonthPrice(plan) {
  return Number(plan?.price?.storageGbMonth || 0);
}

function storageHourlyEstimate(plan, sizeGb) {
  return storageGbMonthPrice(plan) * Number(sizeGb || plan?.diskGb || 0) / 30 / 24;
}

function storageHoldAmount(plan, sizeGb) {
  return storageHourlyEstimate(plan, sizeGb) * 24 * 7;
}

function balanceAfterHold(wallet, holdAmount) {
  return Math.max(0, available(wallet) - Number(holdAmount || 0));
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
    active: "计费中",
    released: "已释放",
    stopped: "已停止",
    pending: "等待中"
  }[value] || value || "等待中";
}

function supportContextPath(resource: AnyRecord = {}, resourceType = "resource") {
  const params = new URLSearchParams();
  params.set("category", "Workspace");
  params.set("priority", resource.safeMessage || resource.status === "failed" ? "high" : "normal");
  params.set("resourceType", resourceType);
  if (resource.id) params.set("resourceId", resource.id);
  if (resource.operationId) params.set("operationId", resource.operationId);
  if (resource.safeMessage) params.set("failureReason", resource.safeMessage);
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
            { title: "计费状态", dataIndex: "billingStatus", render: (value) => <StatusPill label={billingStatusLabel(value)} tone={value === "active" ? "good" : "warn"} /> }
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
  const availablePackages = (state.packages || []).filter((plan) => plan.available);
  const initialPackageId = availablePackages[0]?.id || "basic";
  const [form] = Form.useForm();
  const selectedPackageId = Form.useWatch("packageId", form) || initialPackageId;
  const selectedPlan = availablePackages.find((plan) => plan.id === selectedPackageId) || availablePackages[0];
  const selectedComputeHold = computeHoldAmount(selectedPlan);
  return (
    <ConsoleSurface title="开通计算资源" eyebrow="资源" subtitle="选择规格后提交开通" compact>
      <InsightPanel title="开通计算" eyebrow="计算">
        <Form
          form={form}
          layout="vertical"
          initialValues={{ packageId: initialPackageId }}
          onFinish={async (values) => {
            const created = await runOperation(() => runAction(
              () => createComputeAllocation(values, session.csrfToken),
              "计算资源开通请求已提交",
              { returnFailure: true }
            )
            );
            if (created) navigate(routeTo("compute-allocations.detail", { id: created.id }));
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
              value: `${money(computeHourlyPrice(plan))}/小时`,
              meta: `${plan.server} · ${plan.cpu} CPU / ${plan.memoryGb}GB · 冻结 ${money(computeHoldAmount(plan))}`,
              status: "可用",
              tone: "good"
            }))}
          />
          <PriceImpactPanel
            items={[
              { label: "每小时价格", value: `${money(computeHourlyPrice(selectedPlan))}/小时`, meta: selectedPlan?.server || "-", status: "计费", tone: "info" },
              { label: "预冻结", value: money(selectedComputeHold), meta: "7 天", status: "冻结", tone: "warn" },
              { label: "冻结后可用", value: money(balanceAfterHold(state.wallet, selectedComputeHold)), meta: "可用余额", status: "余额", tone: balanceAfterHold(state.wallet, selectedComputeHold) > 0 ? "good" : "warn" },
              { label: "预计等待", value: "3-5 分钟", meta: "扩容节点并部署 Runtime", status: "冷启动", tone: "info" }
            ]}
          />
          <WalletRiskPanel wallet={state.wallet} requiredHold={selectedComputeHold} resourceLabel="计算资源" />
          <OperationResultPanel pending={operationPending} result={operationResult} />
          <Form.Item>
            <OperationConfirmButton
              label="开通计算"
              title="确认开通计算资源"
              description={`将按 ${money(computeHourlyPrice(selectedPlan))}/小时计费，并预冻结 ${money(selectedComputeHold)}。`}
              type="primary"
              icon={<Server size={15} />}
              disabled={!availablePackages.length}
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
            { label: "规格", value: resource.spec || "-", meta: resource.packageId },
            { label: "计费状态", value: billingStatusLabel(resource.billingStatus), meta: `${money(resource.hourlyPrice)}/小时`, status: resource.billingStatus || "pending", tone: resource.billingStatus === "active" ? "good" : "warn" },
            { label: "绑定入口", value: workspace?.name || workspaceId || "-", meta: workspaceId || "尚未创建工作区入口" },
            { label: "失败原因", value: resource.safeMessage || "-", meta: "如需帮助可提交工单", status: resource.safeMessage ? "异常" : "正常", tone: resource.safeMessage ? "danger" : "neutral" }
          ]}
        />
        <ActionGroup
          actions={[
            <OperationConfirmButton
              key="destroy-compute"
              label="销毁计算资源"
              title="确认销毁计算资源"
              description="销毁后停止后续计算计费；已保留的存储资源不会删除。"
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
            { title: "计费状态", dataIndex: "billingStatus", render: (value) => <StatusPill label={billingStatusLabel(value)} tone={value === "active" ? "good" : "warn"} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function CreateStorageVolumePage({ state, session, runAction }: any) {
  const { operationPending, operationResult, runOperation } = useOperationFeedback();
  const availablePackages = (state.packages || []).filter((plan) => plan.available);
  const initialPackageId = availablePackages[0]?.id || "basic";
  const [form] = Form.useForm();
  const selectedPackageId = Form.useWatch("packageId", form) || initialPackageId;
  const selectedSizeGb = Form.useWatch("sizeGb", form) || availablePackages[0]?.diskGb || 10;
  const selectedPlan = availablePackages.find((plan) => plan.id === selectedPackageId) || availablePackages[0];
  const selectedStorageHold = storageHoldAmount(selectedPlan, selectedSizeGb);
  return (
    <ConsoleSurface title="开通存储资源" eyebrow="资源" subtitle="创建可独立保留的数据盘" compact>
      <InsightPanel title="开通存储" eyebrow="存储">
        <Form
          form={form}
          layout="vertical"
          initialValues={{ packageId: initialPackageId, sizeGb: availablePackages[0]?.diskGb || 10 }}
          onFinish={async (values) => {
            const created = await runOperation(() => runAction(
              () => createStorageVolume(values, session.csrfToken),
              "存储资源开通请求已提交",
              { returnFailure: true }
            ));
            if (created) navigate(routeTo("storage.list"));
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
            <InputNumber min={1} max={4096} style={{ width: "100%" }} />
          </Form.Item>
          <PriceImpactPanel
            items={[
              { label: "存储单价", value: `${money(storageGbMonthPrice(selectedPlan))}/GB月`, meta: "按容量计费", status: "计费", tone: "info" },
              { label: "每小时估算", value: `${money(storageHourlyEstimate(selectedPlan, selectedSizeGb))}/小时`, meta: "当前容量", status: `${selectedSizeGb}GB`, tone: "info" },
              { label: "预冻结", value: money(selectedStorageHold), meta: "7 天", status: "冻结", tone: "warn" },
              { label: "冻结后可用", value: money(balanceAfterHold(state.wallet, selectedStorageHold)), meta: "可用余额", status: "余额", tone: balanceAfterHold(state.wallet, selectedStorageHold) > 0 ? "good" : "warn" }
            ]}
          />
          <WalletRiskPanel wallet={state.wallet} requiredHold={selectedStorageHold} resourceLabel="存储资源" />
          <ResourceSplit items={[{ label: "数据目录", value: "/data", meta: "用户文件保存位置", status: "可挂载", tone: "info" }]} />
          <OperationTimeline operations={[]} stages={storageCreateStages} emptyText="提交后开始创建存储" />
          <OperationResultPanel pending={operationPending} result={operationResult} />
          <Form.Item>
            <OperationConfirmButton
              label="开通存储"
              title="确认开通存储资源"
              description={`将按容量计费，并预冻结 ${money(selectedStorageHold)}。`}
              type="primary"
              icon={<Database size={15} />}
              disabled={!availablePackages.length}
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
            { label: "容量", value: `${resource.sizeGb || 0}GB`, meta: "当前存储容量" },
            { label: "计费状态", value: billingStatusLabel(resource.billingStatus), meta: `${money(resource.hourlyEstimate)}/小时`, status: resource.billingStatus || "pending", tone: resource.billingStatus === "active" ? "good" : "warn" },
            { label: "绑定入口", value: workspace?.name || workspaceId || "-", meta: workspaceId || "尚未创建工作区入口" },
            { label: "失败原因", value: resource.safeMessage || "-", meta: "如需帮助可提交工单", status: resource.safeMessage ? "异常" : "正常", tone: resource.safeMessage ? "danger" : "neutral" }
          ]}
        />
        <ActionGroup
          actions={[
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
            const created = await runOperation(() => runAction(
              () => attachStorage(values, session.csrfToken),
              "挂载请求已提交",
              { returnFailure: true }
            ));
            if (created) navigate(routeTo("workspace.create"));
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
