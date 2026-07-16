import React from "react";
import { Alert, Button, Form, Input, Select, Steps } from "antd";
import { Cable, HardDrive, Link as LinkIcon, Plus, RefreshCw, Server } from "lucide-react";
import { getPricingCatalog } from "../../api/console-read-api.ts";
import { createWorkspace, createWorkspaceIntent } from "../../api/workspaces-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import { ActionGroup, ConsoleSurface, InsightPanel, OperationResultPanel, ResourceSplit, StatusPill } from "../shared/commercial-console.tsx";
import { moneyCents } from "../shared/formatters.ts";

export function CreateWorkspacePage({ state, session, runAction }: any) {
  const [operationPending, setOperationPending] = React.useState(false);
  const [operationResult, setOperationResult] = React.useState<any>(null);
  const [catalog, setCatalog] = React.useState<any>(null);
  const [catalogError, setCatalogError] = React.useState("");
  const [catalogRun, setCatalogRun] = React.useState(0);
  const createIntent = React.useRef<any>(null);

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

  const computeAllocations = (state.computeAllocations || []).filter((item) => item.status !== "destroyed" && item.status !== "deleted");
  const storageVolumes = (state.storageVolumes || []).filter((item) => item.status !== "destroyed" && item.status !== "deleted");
  const attachments = (state.storageAttachments || []).filter((item) =>
    item.status === "attached"
    && computeAllocations.some((compute) => compute.id === item.computeAllocationId)
    && storageVolumes.some((storage) => storage.id === item.storageId)
  );
  const attachment = attachments[0];
  const compute = computeAllocations.find((item) => item.id === attachment?.computeAllocationId) || computeAllocations[0];
  const storage = storageVolumes.find((item) => item.id === attachment?.storageId) || storageVolumes[0];
  const workspace = (state.workspaces || []).find((item) =>
    item.currentAttachmentId === attachment?.id || item.attachmentId === attachment?.id
  );
  const plans = catalog?.packages || [];
  const selectedPlan = plans.find((item) => item.id === compute?.packageId) || plans.find((item) => item.available) || plans[0];
  const storageStepGb = Number(catalog?.storageSize?.stepGb || 10);
  const storageSizeGb = Number(storage?.sizeGb || selectedPlan?.diskGb || storageStepGb);
  const storageBlocks = Math.ceil(storageSizeGb / storageStepGb);
  const computeCnyCents = Number(selectedPlan?.price?.monthlyPriceCnyCents || 0);
  const storageCnyCents = storageBlocks * Number(catalog?.storagePer10GbMonthly?.cnyCents || 0);
  const paid = compute?.billingStatus === "active" && storage?.billingStatus === "active";
  const providerReady = ["running", "ready", "active"].includes(compute?.status)
    && ["bound", "available", "ready", "active"].includes(storage?.status);
  const completed = [
    Boolean(compute && storage),
    Boolean(compute && storage && catalog),
    paid,
    paid && providerReady,
    Boolean(attachment && workspace),
    workspace?.openable === true
  ];
  const firstIncomplete = completed.findIndex((value) => !value);
  const currentStep = firstIncomplete === -1 ? 5 : firstIncomplete;

  const guideItems = [
    { title: "选择套餐与存储", description: selectedPlan ? `${selectedPlan.name} ${selectedPlan.server} + ${storageSizeGb}GB` : "从服务端目录选择套餐和独立存储" },
    { title: "确认月度总价", description: catalog ? `${moneyCents(computeCnyCents)} + ${moneyCents(storageCnyCents)} = ${moneyCents(computeCnyCents + storageCnyCents)}/月` : "等待价格目录" },
    { title: "完成月费扣款", description: paid ? "计算与存储权益均已激活" : "分别通过现有资源开通页完成扣款" },
    { title: "准备 PREPAID 资源", description: providerReady ? "计算与存储已就绪" : "等待腾讯包月资源读回" },
    { title: "启动 Gateway 与 Runtime", description: workspace ? "模型访问与 Runtime 正在准备" : "挂载存储后创建 Workspace" },
    { title: "打开 Workspace URL", description: workspace?.openable ? "Runtime 已就绪" : "创建后最多自动等待 5 分钟" }
  ].map((item, index) => ({ ...item, status: completed[index] ? "finish" : index === currentStep ? "process" : "wait" }));

  return (
    <ConsoleSurface title="开通 Workspace" eyebrow="Workspace" subtitle="按当前资源状态继续，不重复提交已完成步骤" compact>
      {catalogError && (
        <Alert
          type="error"
          showIcon
          message="价格目录加载失败"
          description={catalogError}
          action={<Button onClick={() => setCatalogRun((value) => value + 1)}>重试</Button>}
        />
      )}
      <div className="consoleGrid">
        <InsightPanel title="开通进度" eyebrow="六步向导" actions={<StatusPill label={`第 ${Math.min(currentStep + 1, 6)} 步`} tone="info" />}>
          <Steps className="launchGuide" direction="vertical" current={currentStep} items={guideItems as any} />
          <ActionGroup actions={[
            !compute && { label: "选择套餐并开通计算", icon: <Server size={15} />, onClick: () => navigate(routeTo("compute-allocations.create")) },
            !storage && { label: "开通存储", icon: <HardDrive size={15} />, onClick: () => navigate(routeTo("storage.create")) },
            compute && storage && !attachment && { label: "挂载存储", icon: <Cable size={15} />, onClick: () => navigate(routeTo("attachment.create")) },
            workspace?.openable && { label: "打开 Workspace", icon: <LinkIcon size={15} />, onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") }
          ].filter(Boolean)} />
        </InsightPanel>

        <InsightPanel title="月度价格" eyebrow="服务端目录" actions={<StatusPill label={selectedPlan?.available ? "可购买" : "加载中"} tone={selectedPlan?.available ? "good" : "warn"} />}>
          <ResourceSplit items={[
            { label: "计算", value: `${moneyCents(computeCnyCents)}/月`, meta: selectedPlan?.server || "-", status: selectedPlan?.name || "-", tone: "info" },
            { label: "存储", value: `${moneyCents(storageCnyCents)}/月`, meta: `${storageSizeGb}GB`, status: "独立权益", tone: "info" },
            { label: "合计", value: `${moneyCents(computeCnyCents + storageCnyCents)}/月`, meta: "实际扣款以确认页为准", status: "月付", tone: "good" }
          ]} />
        </InsightPanel>
      </div>

      <InsightPanel title="创建 Workspace" eyebrow="已挂载资源" actions={<StatusPill label={attachment ? "可创建" : "等待挂载"} tone={attachment ? "good" : "warn"} />}>
        {!attachment && <Alert type="warning" showIcon message="完成计算、存储和挂载后即可创建 Workspace。" />}
        {attachment && (
          <Form
            layout="vertical"
            initialValues={{ attachmentId: attachment.id }}
            onFinish={async (values) => {
              const input = { workspaceName: values.workspaceName, attachmentId: values.attachmentId };
              const intent = operationResult?.status === "unknown" ? createIntent.current : createWorkspaceIntent(input);
              createIntent.current = intent;
              setOperationPending(true);
              setOperationResult({ ok: true, status: "submitted", nextStepMessage: "Workspace 创建请求已提交，正在准备 Runtime。" });
              const created = await runAction(
                () => createWorkspace(intent, session.csrfToken),
                "Workspace 创建请求已提交",
                { returnFailure: true }
              );
              if (created?.status !== "unknown") createIntent.current = null;
              setOperationPending(false);
              setOperationResult(created && { ...created, status: created.status || "submitted", nextStepMessage: "Runtime 正在启动；详情页会自动等待最多 5 分钟。" });
            }}
          >
            <Form.Item name="workspaceName" label="名称" rules={[{ required: true, message: "请输入 Workspace 名称" }]}>
              <Input placeholder="输入 Workspace 名称" disabled={operationPending || operationResult?.status === "unknown"} />
            </Form.Item>
            <Form.Item name="attachmentId" label="计算与存储" rules={[{ required: true, message: "请选择挂载关系" }]}>
              <Select disabled={operationPending || operationResult?.status === "unknown"} options={attachments.map((item) => ({ label: `${item.computeAllocationId} + ${item.storageId}`, value: item.id }))} />
            </Form.Item>
            <OperationResultPanel pending={operationPending} result={operationResult} />
            {operationResult && operationResult.ok !== false && (
              <ActionGroup actions={[
                { label: "查看 Workspace", icon: <LinkIcon size={15} />, onClick: () => navigate(routeTo("workspace.list")) },
                { label: "费用明细", onClick: () => navigate(routeTo("billing.overview")) }
              ]} />
            )}
            <Button className="formSubmit" type="primary" htmlType="submit" icon={operationResult?.status === "unknown" ? <RefreshCw size={15} /> : <Plus size={15} />} loading={operationPending}>
              {operationResult?.status === "unknown" ? "重试并确认结果" : "创建 Workspace"}
            </Button>
          </Form>
        )}
      </InsightPanel>
    </ConsoleSurface>
  );
}
