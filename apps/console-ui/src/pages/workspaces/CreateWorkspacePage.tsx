import React from "react";
import { Alert, Button, Form, Input, Select, Steps } from "antd";
import { Link as LinkIcon, Plus, RefreshCw, WalletCards } from "lucide-react";
import { getPricingCatalog } from "../../api/console-read-api.ts";
import { previewPricing } from "../../api/pricing-api.ts";
import { attachStorage, createComputeAllocation, createStorageVolume } from "../../api/resources-api.ts";
import { createWorkspace, createWorkspaceLaunchIntent } from "../../api/workspaces-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  BalanceChargePanel,
  ConsoleSurface,
  InsightPanel,
  OperationConfirmButton,
  OperationResultPanel,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import {
  moneyCents,
  paidThrough,
  statusLabel,
  usdMicros,
  valueLabel,
  workspaceOpenActionLabel,
  workspaceUrlReady
} from "../shared/formatters.ts";

function failedStep(result) {
  return !result || result === false || result.ok === false || ["failed", "unknown", "manual_review", "refunded"].includes(result.status);
}

export function CreateWorkspacePage({ state, session, runAction }: any) {
  const [form] = Form.useForm();
  const [operationPending, setOperationPending] = React.useState(false);
  const [operationResult, setOperationResult] = React.useState<any>(null);
  const [catalog, setCatalog] = React.useState<any>(null);
  const [catalogError, setCatalogError] = React.useState("");
  const [catalogRun, setCatalogRun] = React.useState(0);
  const [quote, setQuote] = React.useState<any>(null);
  const [quoteError, setQuoteError] = React.useState("");
  const launchIntent = React.useRef<any>(null);
  const launchPending = React.useRef(false);

  React.useEffect(() => {
    let active = true;
    setCatalog(null);
    setCatalogError("");
    getPricingCatalog()
      .then((payload) => {
        if (active) setCatalog(payload);
      })
      .catch((err) => {
        if (active) setCatalogError(err.message);
      });
    return () => { active = false; };
  }, [catalogRun]);

  const workspaces = state.workspaces || [];
  const existingWorkspace = workspaces[0];
  const computeAllocations = (state.computeAllocations || []).filter((item) => item.status !== "destroyed" && item.status !== "deleted");
  const storageVolumes = (state.storageVolumes || []).filter((item) => item.status !== "destroyed" && item.status !== "deleted");
  const attachments = (state.storageAttachments || []).filter((item) => item.status === "attached");
  const attachment = attachments.find((item) => item.id === existingWorkspace?.currentAttachmentId || item.id === existingWorkspace?.attachmentId) || attachments[0];
  const compute = computeAllocations.find((item) => item.id === attachment?.computeAllocationId)
    || computeAllocations.find((item) => item.id === existingWorkspace?.currentComputeAllocationId || item.id === existingWorkspace?.computeAllocationId)
    || computeAllocations[0];
  const storage = storageVolumes.find((item) => item.id === attachment?.storageId)
    || storageVolumes.find((item) => item.id === existingWorkspace?.storageId)
    || storageVolumes.find((item) => !compute || item.workspaceId === compute.workspaceId)
    || storageVolumes[0];
  const workspace = existingWorkspace || workspaces.find((item) => item.currentAttachmentId === attachment?.id || item.attachmentId === attachment?.id);
  const plans = (catalog?.packages || []).filter((item) => item.available);
  const watchedPackageId = Form.useWatch("packageId", form);
  const selectedPackageId = watchedPackageId || compute?.packageId || plans[0]?.id;
  const selectedPlan = plans.find((item) => item.id === selectedPackageId) || plans[0];
  const storageSizeGb = Number(storage?.sizeGb || selectedPlan?.diskGb || 0);

  React.useEffect(() => {
    if (selectedPackageId && !form.getFieldValue("packageId")) {
      form.setFieldValue("packageId", selectedPackageId);
    }
  }, [form, selectedPackageId]);

  React.useEffect(() => {
    let active = true;
    setQuote(null);
    setQuoteError("");
    if (!selectedPlan?.id || !storageSizeGb) return () => { active = false; };
    Promise.all([
      previewPricing({ resourceType: "compute", packageId: selectedPlan.id }, session.csrfToken),
      previewPricing({ resourceType: "storage", packageId: selectedPlan.id, sizeGb: storageSizeGb }, session.csrfToken)
    ])
      .then(([computeQuote, storageQuote]) => {
        if (active) setQuote({ compute: computeQuote, storage: storageQuote });
      })
      .catch((err) => {
        if (active) setQuoteError(err.message);
      });
    return () => { active = false; };
  }, [selectedPlan?.id, storageSizeGb, session.csrfToken]);

  const computeCnyCents = Number(quote?.compute?.monthlyPriceCnyCents || 0);
  const storageCnyCents = Number(quote?.storage?.monthlyPriceCnyCents || 0);
  const totalCnyCents = computeCnyCents + storageCnyCents;
  const totalChargeUsdMicros = Number(quote?.compute?.chargeUsdMicros || 0) + Number(quote?.storage?.chargeUsdMicros || 0);
  const paid = compute?.billingStatus === "active" && storage?.billingStatus === "active";
  const providerReady = ["running", "ready", "active"].includes(compute?.status)
    && ["bound", "available", "ready", "active"].includes(storage?.status);
  const completed = [
    Boolean(selectedPlan),
    Boolean(quote),
    paid,
    paid && providerReady,
    Boolean(attachment && workspace),
    workspace?.openable === true
  ];
  const firstIncomplete = completed.findIndex((value) => !value);
  const currentStep = firstIncomplete === -1 ? 5 : firstIncomplete;
  const guideItems = [
    { title: "选择套餐与存储", description: selectedPlan ? `${selectedPlan.name} ${selectedPlan.server} + 固定 ${storageSizeGb}GB` : "等待服务端目录" },
    { title: "确认月度总价", description: quote ? `${moneyCents(computeCnyCents)} + ${moneyCents(storageCnyCents)} = ${moneyCents(totalCnyCents)}/月` : "等待服务端报价" },
    { title: "完成月费扣款", description: paid ? "计算与存储权益均已激活" : "按顺序完成两项月费扣款" },
    { title: "准备 PREPAID 资源", description: providerReady ? "计算与存储已就绪" : "等待腾讯包月资源读回" },
    { title: "启动 Gateway 与 Runtime", description: workspace ? "Gateway Secret 与 Runtime 正在准备" : "资源挂载后继续" },
    { title: "打开 Workspace URL", description: workspace?.openable ? "Runtime 已就绪" : "详情页最多自动等待 5 分钟" }
  ].map((item, index) => ({ ...item, status: completed[index] ? "finish" : index === currentStep ? "process" : "wait" }));

  if (existingWorkspace) {
    return (
      <ConsoleSurface title="Workspace 已开通" eyebrow="Workspace" subtitle="当前账号仅保留一个 Workspace 主入口" compact>
        <InsightPanel title={existingWorkspace.name} eyebrow="已有 Workspace" actions={<StatusPill label={statusLabel(existingWorkspace)} tone={existingWorkspace.openable ? "good" : "warn"} />}>
          <ResourceSplit items={[
            { label: "状态", value: statusLabel(existingWorkspace), meta: existingWorkspace.state || "-", status: existingWorkspace.openable ? "可打开" : "恢复中", tone: existingWorkspace.openable ? "good" : "warn" },
            { label: "计算权益", value: valueLabel(compute?.billingStatus), meta: `有效期至 ${paidThrough(compute?.paidThrough)}`, status: compute?.packageId || "-", tone: compute?.billingStatus === "active" ? "good" : "warn" },
            { label: "存储权益", value: valueLabel(storage?.billingStatus), meta: `有效期至 ${paidThrough(storage?.paidThrough)}`, status: `${storage?.sizeGb || 0}GB`, tone: storage?.billingStatus === "active" ? "good" : "warn" }
          ]} />
          <ActionGroup actions={[
            { label: workspaceOpenActionLabel(existingWorkspace), type: "primary", icon: <LinkIcon size={15} />, disabled: !workspaceUrlReady(existingWorkspace), onClick: () => window.open(existingWorkspace.url, "_blank", "noopener,noreferrer") },
            { label: existingWorkspace.openable ? "查看详情" : "查看恢复", icon: <RefreshCw size={15} />, onClick: () => navigate(routeTo("workspace.detail", { id: existingWorkspace.id })) },
            { label: "查看账单", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) }
          ]} />
        </InsightPanel>
      </ConsoleSurface>
    );
  }

  async function launch(values) {
    if (launchPending.current) return;
    launchPending.current = true;
    setOperationPending(true);
    const requested = launchIntent.current?.input || {
      workspaceName: values.workspaceName,
      packageId: values.packageId,
      sizeGb: selectedPlan.diskGb
    };
    const intent = createWorkspaceLaunchIntent(requested, launchIntent.current);
    launchIntent.current = intent;

    try {
      let nextCompute = compute;
      if (nextCompute && nextCompute.billingStatus !== "active") {
        setOperationResult({ ok: false, status: nextCompute.billingStatus || "pending", failureReason: "计算权益尚未激活，请继续同一开通请求。" });
        return;
      }
      if (!nextCompute) {
        setOperationResult({ ok: true, status: "submitted", nextStepMessage: "正在完成计算扣款与 PREPAID 开通。" });
        nextCompute = await runAction(
          () => createComputeAllocation({ name: `${intent.input.workspaceName}-compute`, packageId: intent.input.packageId }, session.csrfToken, intent.idempotencyKeys.compute),
          "计算权益已开通",
          { returnFailure: true, actionKey: intent.idempotencyKeys.compute }
        );
        if (failedStep(nextCompute)) {
          setOperationResult(nextCompute);
          return;
        }
      }

      let nextStorage = storage;
      if (nextStorage && (nextStorage.computeAllocationId && nextStorage.computeAllocationId !== nextCompute.id)) nextStorage = null;
      if (nextStorage && nextStorage.billingStatus !== "active") {
        setOperationResult({ ok: false, status: nextStorage.billingStatus || "pending", failureReason: "存储权益尚未激活，请继续同一开通请求。" });
        return;
      }
      if (!nextStorage) {
        setOperationResult({ ok: true, status: "submitted", nextStepMessage: "正在完成存储扣款与 PREPAID 开通。" });
        nextStorage = await runAction(
          () => createStorageVolume({
            name: `${intent.input.workspaceName}-storage`,
            packageId: intent.input.packageId,
            sizeGb: intent.input.sizeGb,
            computeAllocationId: nextCompute.id
          }, session.csrfToken, intent.idempotencyKeys.storage),
          "存储权益已开通",
          { returnFailure: true, actionKey: intent.idempotencyKeys.storage }
        );
        if (failedStep(nextStorage)) {
          setOperationResult(nextStorage);
          return;
        }
      }

      let nextAttachment = attachment;
      if (nextAttachment && (nextAttachment.computeAllocationId !== nextCompute.id || nextAttachment.storageId !== nextStorage.id)) nextAttachment = null;
      if (!nextAttachment) {
        setOperationResult({ ok: true, status: "submitted", nextStepMessage: "正在挂载固定存储。" });
        nextAttachment = await runAction(
          () => attachStorage({ computeAllocationId: nextCompute.id, storageId: nextStorage.id, mountPath: "/data" }, session.csrfToken, intent.idempotencyKeys.attachment),
          "存储已挂载",
          { returnFailure: true, actionKey: intent.idempotencyKeys.attachment }
        );
        if (failedStep(nextAttachment)) {
          setOperationResult(nextAttachment);
          return;
        }
      }

      setOperationResult({ ok: true, status: "submitted", nextStepMessage: "正在同步 Gateway Secret 并启动 Runtime。" });
      const created = await runAction(
        () => createWorkspace({
          input: { workspaceName: intent.input.workspaceName, attachmentId: nextAttachment.id },
          idempotencyKey: intent.idempotencyKeys.workspace
        }, session.csrfToken),
        "Workspace 开通请求已完成",
        { returnFailure: true, actionKey: intent.idempotencyKeys.workspace }
      );
      if (failedStep(created)) {
        setOperationResult(created);
        return;
      }
      launchIntent.current = null;
      setOperationResult({ ...created, ok: true, status: created.status || "submitted", nextStepMessage: "Gateway Secret 已同步，Runtime 正在启动。" });
    } finally {
      launchPending.current = false;
      setOperationPending(false);
    }
  }

  const fieldsLocked = operationPending || Boolean(launchIntent.current);
  const completedLaunch = operationResult?.ok === true && Boolean(operationResult?.resourceId);
  return (
    <ConsoleSurface title="开通 Workspace" eyebrow="Workspace" subtitle="套餐、扣款、PREPAID 资源与 Runtime 一次完成" compact>
      {(catalogError || quoteError) && (
        <Alert
          type="error"
          showIcon
          message="月度目录或报价加载失败"
          description={catalogError || quoteError}
          action={<Button onClick={() => setCatalogRun((value) => value + 1)}>重试</Button>}
        />
      )}
      <div className="consoleGrid">
        <InsightPanel title="开通进度" eyebrow="六步向导" actions={<StatusPill label={`第 ${Math.min(currentStep + 1, 6)} 步`} tone="info" />}>
          <Steps className="launchGuide" direction="vertical" current={currentStep} items={guideItems as any} />
        </InsightPanel>

        <InsightPanel title="月度价格" eyebrow="服务端报价" actions={<StatusPill label={quote ? "报价已确认" : "加载中"} tone={quote ? "good" : "warn"} />}>
          <ResourceSplit items={[
            { label: "计算", value: `${moneyCents(computeCnyCents)}/月`, meta: selectedPlan?.server || "-", status: selectedPlan?.name || "-", tone: "info" },
            { label: "固定存储", value: `${moneyCents(storageCnyCents)}/月`, meta: `${storageSizeGb}GB`, status: "独立权益", tone: "info" },
            { label: "合计", value: `${moneyCents(totalCnyCents)}/月`, meta: usdMicros(totalChargeUsdMicros), status: "月付", tone: "good" }
          ]} />
          <BalanceChargePanel balance={state.balance} chargeUsdMicros={totalChargeUsdMicros} resourceLabel="Workspace 月度权益" />
        </InsightPanel>
      </div>

      <InsightPanel title="确认开通" eyebrow="一个连续流程" actions={<StatusPill label={operationPending ? "处理中" : "可提交"} tone={operationPending ? "warn" : "good"} />}>
        <Form form={form} layout="vertical" initialValues={{ workspaceName: "我的 Workspace" }} onFinish={launch}>
          <Form.Item name="workspaceName" label="Workspace 名称" rules={[{ required: true, message: "请输入 Workspace 名称" }]}>
            <Input placeholder="输入 Workspace 名称" disabled={fieldsLocked} />
          </Form.Item>
          <Form.Item name="packageId" label="套餐与固定存储" rules={[{ required: true, message: "请选择套餐" }]}>
            <Select
              disabled={fieldsLocked}
              options={plans.map((plan) => ({ label: `${plan.name} · ${plan.server} · 固定 ${plan.diskGb}GB`, value: plan.id }))}
            />
          </Form.Item>
          <OperationResultPanel pending={operationPending} result={operationResult} />
          {completedLaunch && (
            <ActionGroup actions={[
              { label: "查看 Workspace", icon: <LinkIcon size={15} />, onClick: () => navigate(routeTo("workspace.list")) },
              { label: "费用明细", icon: <WalletCards size={15} />, onClick: () => navigate(routeTo("billing.overview")) }
            ]} />
          )}
          <OperationConfirmButton
            label={launchIntent.current ? "继续同一开通请求" : "开通 Workspace"}
            title="确认开通 Workspace"
            description={`将从 Sub2API 余额依次扣除计算与存储月费，共 ${usdMicros(totalChargeUsdMicros)}（${moneyCents(totalCnyCents)}/月）。`}
            type="primary"
            icon={launchIntent.current ? <RefreshCw size={15} /> : <Plus size={15} />}
            loading={operationPending}
            disabled={!quote || !selectedPlan || state.balance?.available === false || completedLaunch}
            onConfirm={() => form.submit()}
          />
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}
