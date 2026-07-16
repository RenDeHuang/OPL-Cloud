import React from "react";
import { Alert, Button, Empty, Typography } from "antd";
import { Eye, EyeOff, Headphones, KeyRound, Link as LinkIcon, RefreshCw, WalletCards } from "lucide-react";
import { getWorkspaceRuntimeStatus, rotateWorkspaceGatewaySecret } from "../../api/workspaces-api.ts";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  ResourceSplit,
  StatusPill
} from "../shared/commercial-console.tsx";
import { mergeWorkspaceRuntime, moneyCents, packageText, paidThrough, statusColor, statusLabel, usdMicros, valueLabel, workspaceAccessLabel, workspaceAccessTone, workspaceOpenActionLabel, workspaceUrlReady } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

const RUNTIME_POLL_INTERVAL_MS = 10_000;
const RUNTIME_POLL_MAX_ATTEMPTS = 30;
const terminalRuntimeStates = new Set(["failed", "suspended", "data_deleted", "unrecoverable", "storage_missing", "destroyed"]);

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
  const [runtimeStatus, setRuntimeStatus] = React.useState<AnyRecord | null>(null);
  const [showPassword, setShowPassword] = React.useState(false);
  const [pollAttempts, setPollAttempts] = React.useState(0);
  const [pollError, setPollError] = React.useState("");
  const [pollRun, setPollRun] = React.useState(0);
  const [gatewaySyncPending, setGatewaySyncPending] = React.useState(false);
  const [gatewaySyncResult, setGatewaySyncResult] = React.useState<AnyRecord | null>(null);
  const gatewaySyncKey = React.useRef(crypto.randomUUID());
  React.useEffect(() => {
    let active = true;
    let timer: number | undefined;
    let attempts = 0;
    setRuntimeStatus(null);
    setShowPassword(false);
    setPollAttempts(0);
    setPollError("");
    if (!selected?.id) {
      return () => { active = false; };
    }
    if (terminalRuntimeStates.has(selected.state)) {
      setPollError(selected.safeMessage || selected.errorCode || `Runtime 已停止：${selected.state}`);
      return () => { active = false; };
    }
    const poll = async () => {
      attempts += 1;
      setPollAttempts(attempts);
      try {
        const current = await getWorkspaceRuntimeStatus({ workspaceId: selected.id }, session.csrfToken);
        if (!active) return;
        setRuntimeStatus(current);
        if (current.ready === true) return;
        const status = current.status || current.state;
        if (terminalRuntimeStates.has(status)) {
          setPollError(current.safeMessage || current.errorCode || `Runtime 启动失败：${status}`);
          return;
        }
        if (attempts >= RUNTIME_POLL_MAX_ATTEMPTS) {
          setPollError("等待已超过 5 分钟，请检查 Runtime 状态后手动重试。");
          return;
        }
        timer = window.setTimeout(poll, RUNTIME_POLL_INTERVAL_MS);
      } catch (err) {
        if (active) setPollError(err.message || "runtime_status_unavailable");
      }
    };
    void poll();
    return () => {
      active = false;
      if (timer) window.clearTimeout(timer);
    };
  }, [selected?.id, selected?.state, session.csrfToken, pollRun]);

  if (!selected) {
    return (
      <ConsoleSurface title="OPL Workspace" eyebrow="工作区">
        <Empty description="暂无工作区" />
      </ConsoleSurface>
    );
  }
  const workspace = mergeWorkspaceRuntime(selected, runtimeStatus);
  const credential = workspaceCredential(workspace);
  const computeId = workspace.currentComputeAllocationId || workspace.computeAllocationId;
  const compute = (state.computeAllocations || []).find((item) => item.id === computeId) || {};
  const storage = (state.storageVolumes || []).find((item) => item.id === workspace.storageId) || {};
  const canRotateGateway = session.user?.role === "owner"
    && (workspace.ownerUserId || workspace.ownerId) === session.user?.id;
  const supportPath = `${routeTo("support.create")}?category=Workspace&resourceId=${encodeURIComponent(workspace.id)}&operationId=${encodeURIComponent(workspace.currentAttachmentId || workspace.currentComputeAllocationId || "")}`;

  async function syncGatewaySecret() {
    if (gatewaySyncPending) return;
    setGatewaySyncPending(true);
    const result = await runAction(
      () => rotateWorkspaceGatewaySecret(
        { workspaceId: workspace.id, reason: "owner-request" },
        session.csrfToken,
        gatewaySyncKey.current
      ),
      "Gateway Key 已同步",
      { returnFailure: true, actionKey: gatewaySyncKey.current }
    );
    setGatewaySyncResult(result);
    if (result?.ok !== false && result?.status === "succeeded") {
      gatewaySyncKey.current = crypto.randomUUID();
    }
    setGatewaySyncPending(false);
  }

  return (
    <ConsoleSurface
      title={workspace.name}
      eyebrow="OPL Workspace"
      subtitle="访问凭据、费用状态和支持"
      extra={<Button onClick={() => navigate(routeTo("workspace.list"))}>返回列表</Button>}
    >
      <div className="consoleGrid equal">
        <InsightPanel
          title="访问凭据"
          eyebrow="URL、账号、密码"
          actions={<StatusPill label={workspaceAccessLabel(workspace)} tone={workspaceAccessTone(workspace)} />}
        >
          <div className="stackList">
            {pollError ? (
              <Alert
                type="error"
                showIcon
                message="Runtime 尚未就绪"
                description={pollError}
                action={<Button onClick={() => setPollRun((value) => value + 1)}>手动重试</Button>}
              />
            ) : !workspaceUrlReady(workspace) && workspace.accessState === "distributing" && (
              <Alert
                type="info"
                showIcon
                message="Runtime 正在启动"
                description={`每 10 秒检查一次（${pollAttempts}/${RUNTIME_POLL_MAX_ATTEMPTS}），就绪或失败后自动停止。`}
              />
            )}
            <div className="credentialStack">
              <span>URL</span>
              <Typography.Text copyable={workspaceUrlReady(workspace)} className="inlineCode">{workspace.url}</Typography.Text>
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
                { label: workspaceOpenActionLabel(workspace), icon: <LinkIcon size={15} />, disabled: !workspaceUrlReady(workspace), onClick: () => window.open(workspace.url, "_blank", "noopener,noreferrer") },
                { label: showPassword ? "隐藏密码" : "显示密码", icon: showPassword ? <EyeOff size={15} /> : <Eye size={15} />, onClick: () => setShowPassword(!showPassword) },
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
              { label: "状态", value: statusLabel(workspace), meta: workspace.state, status: "Workspace", tone: toneForStatus(workspace.state) },
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

      {canRotateGateway && (
        <InsightPanel
          title="Gateway Key"
          eyebrow="Owner 操作"
          actions={<StatusPill label={gatewaySyncResult?.status || "可同步"} tone={gatewaySyncResult?.status === "succeeded" ? "good" : gatewaySyncResult?.ok === false ? "danger" : "info"} />}
        >
          <ResourceSplit items={[
            { label: "状态", value: gatewaySyncResult?.status || "未同步", meta: "账户范围", status: workspace.openable ? "可操作" : "Workspace 未就绪", tone: workspace.openable ? "good" : "warn" },
            { label: "Fingerprint", value: gatewaySyncResult?.fingerprint || "-", meta: "最近同步结果", status: "SHA-256", tone: "info" }
          ]} />
          {gatewaySyncResult?.ok === false && <Alert type="error" showIcon message="Gateway Key 同步失败" description={gatewaySyncResult.failureReason || gatewaySyncResult.status} />}
          <ActionGroup actions={[
            {
              label: "同步/轮换 Gateway Key",
              icon: <KeyRound size={15} />,
              disabled: !workspace.openable,
              loading: gatewaySyncPending,
              onClick: syncGatewaySecret
            },
            gatewaySyncResult?.ok === false && {
              label: "重试同一请求",
              icon: <RefreshCw size={15} />,
              disabled: !workspace.openable,
              onClick: syncGatewaySecret
            }
          ].filter(Boolean)} />
        </InsightPanel>
      )}
    </ConsoleSurface>
  );
}
