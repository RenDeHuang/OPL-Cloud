import {
  AlertCircle,
  ArrowRight,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  Copy,
  ExternalLink,
  Eye,
  EyeOff,
  Plus,
  RefreshCw,
  Server,
  WalletCards
} from "lucide-react";
import type { ReactNode } from "react";

import type { ConsoleController } from "../app/use-console-controller.ts";
import type {
  AnnouncementDTO,
  BillingReceipt,
  GatewayUsageItem,
  PricingPlan,
  SourceEnvelope,
  WorkspaceDTO
} from "../api/dtos.ts";
import { KeysPanel } from "../components/keys/KeysPanel.tsx";
import { SourceState } from "../components/source/SourceState.tsx";
import { Badge, Button, Checkbox, SegmentedControl, Select } from "../components/ui/index.ts";
import { apiMenu, apiPage, formatCount, formatDate, formatUsdMicros, workspacePage, workspaceStatusLabel } from "../console-model.ts";

const launchPhases = [
  ["validate", "校验报价与余额"],
  ["debit", "确认单次扣款"],
  ["workspace_key", "准备 Workspace Key"],
  ["compute", "准备计算资源"],
  ["storage", "准备存储资源"],
  ["attachment", "挂载存储"],
  ["secret", "写入访问 Secret"],
  ["runtime", "启动 Runtime"],
  ["activate", "激活 Workspace"],
  ["receipt", "写入 Receipt"]
] as const;

function sourceData<T>(source: SourceEnvelope<T> | null | undefined): T | null {
  return source?.available ? source.data : null;
}

function statusLabel(status?: string) {
  return ({
    active: "正常",
    available: "正常",
    disabled: "已停用",
    empty: "暂无数据",
    expired: "已到期",
    failed: "已失败",
    manual_review: "人工复核",
    pending: "处理中",
    preparing: "开通中",
    quota_exhausted: "配额已用尽",
    ready: "已就绪",
    refunded: "已退款",
    running: "运行中",
    succeeded: "已完成",
    unavailable: "暂不可用",
    unknown: "结果待确认"
  } as Record<string, string>)[status || ""] || (status || "暂不可用");
}

function workspaceLifecycleLabel(state?: string) {
  return ({
    active: "已激活",
    creating: "开通中",
    expired: "已到期",
    failed: "已失败",
    pending: "待开通",
    running: "运行中",
    suspended: "已暂停"
  } as Record<string, string>)[state || ""] || (state || "暂不可用");
}

function receiptLabel(type: string) {
  if (type === "billing.workspace_purchased.v1" || type.includes("created")) return "Workspace 开通";
  if (type === "billing.workspace_expired.v1" || type.includes("expired")) return "Workspace 到期";
  if (type.includes("renew")) return "Workspace 续费";
  if (type.includes("refund")) return "Workspace 退款";
  return type ? "账单记录" : "暂不可用";
}

function receiptAmount(receipt: BillingReceipt) {
  return receipt.refundUsdMicros ?? receipt.chargeUsdMicros ?? receipt.totalUsdMicros;
}

function Metric({ label, value, note, emphasis }: { label: string; value: string; note: string; emphasis?: boolean }) {
  return <article className={`band-metric ${emphasis ? "available-metric" : ""}`}><span>{label}</span><strong>{value}</strong><small>{note}</small></article>;
}

function PageLink({ children, controller, path, className = "" }: { children: ReactNode; controller: ConsoleController; path: string; className?: string }) {
  return <a className={className} href={path} onClick={(event) => { event.preventDefault(); controller.navigate(path); }}>{children}</a>;
}

function Pagination({ current, pages, onChange, label }: { current: number; pages: number; onChange: (page: number) => void; label: string }) {
  if (pages <= 1) return null;
  return (
    <nav aria-label={label} className="pagination">
      <Button disabled={current <= 1} onClick={() => onChange(current - 1)} size="sm" variant="outline"><ChevronLeft aria-hidden size={16} />上一页</Button>
      <span>第 {current} / {pages} 页</span>
      <Button disabled={current >= pages} onClick={() => onChange(current + 1)} size="sm" variant="outline">下一页<ChevronRight aria-hidden size={16} /></Button>
    </nav>
  );
}

function OverviewPage({ controller }: { controller: ConsoleController }) {
  const workspaces = sourceData(controller.sources.workspaces.value);
  const wallet = sourceData(controller.sources.wallet.value);
  const usage = sourceData(controller.sources.accountUsage.value);
  const receipts = sourceData(controller.sources.receipts.value)?.receipts || [];
  const announcements = sourceData(controller.sources.announcements.value)?.items || [];
  const primaryWorkspace = workspaces?.items[0];
  const primaryPath = primaryWorkspace ? `/console/workspaces/${encodeURIComponent(primaryWorkspace.id)}` : "/console/workspaces/new";
  const workspacesUnavailable = controller.sources.workspaces.value?.status === "unavailable" || Boolean(controller.sources.workspaces.error);
  const workspacesPending = !controller.sources.workspaces.value || controller.sources.workspaces.loading;

  return (
    <section className="overview-page" data-slide="C-OV-01">
      <section className="account-band">
        <div className="account-band-copy">
          <p className="eyebrow">C-OV-01</p>
          <h2>账户概览</h2>
          <p>余额、API 实际费用和 Workspace 生命周期分别来自各自权威来源。</p>
          <Button color="primary" disabled={workspacesPending && !workspacesUnavailable} onClick={() => workspacesUnavailable ? void controller.refreshCurrentPage() : controller.navigate(primaryPath)}>
            {workspacesUnavailable ? "重试读取 Workspace" : primaryWorkspace ? "查看 Workspace" : workspacesPending ? "正在读取 Workspace" : "新建 Workspace"}
            {workspacesUnavailable ? <RefreshCw aria-hidden size={16} /> : <ArrowRight aria-hidden size={16} />}
          </Button>
        </div>
        <div className="overview-metrics">
          <Metric emphasis label="可用余额" note="API 服务余额" value={wallet ? formatUsdMicros(wallet.usdMicros) : "暂不可用"} />
          <Metric label="本月实际费用" note="API 请求实际消费" value={usage ? formatUsdMicros(usage.totalActualCostUsdMicros) : "暂不可用"} />
          <Metric label="Workspace" note="当前账户总数" value={workspaces ? formatCount(workspaces.total) : "暂不可用"} />
        </div>
      </section>

      <div className="overview-grid">
        <section className="panel overview-workspaces">
          <div className="panel-title"><h2>Workspace 摘要</h2><Button onClick={() => controller.navigate("/console/workspaces")} size="sm" variant="ghost">全部</Button></div>
          <SourceState
            emptyTitle="暂无 Workspace"
            error={controller.sources.workspaces.error}
            loading={controller.sources.workspaces.loading}
            onRetry={() => void controller.refreshCurrentPage()}
            source={controller.sources.workspaces.value}
          >
            {(data) => (
              <div className="overview-workspace-table table-wrap">
                <table><thead><tr><th>Workspace</th><th>套餐</th><th>生命周期状态</th><th>已付至</th><th /></tr></thead><tbody>
                  {data.items.map((workspace) => <WorkspaceSummaryRow controller={controller} key={workspace.id} workspace={workspace} />)}
                </tbody></table>
              </div>
            )}
          </SourceState>
        </section>

        <section className="panel overview-receipts">
          <div className="panel-title"><h2>最近账单</h2><Button onClick={() => controller.navigate("/console/billing")} size="sm" variant="ghost">全部</Button></div>
          <SourceState
            empty={receipts.length === 0}
            emptyTitle="暂无账单收据"
            error={controller.sources.receipts.error}
            loading={controller.sources.receipts.loading}
            onRetry={() => void controller.refreshCurrentPage()}
            source={controller.sources.receipts.value}
            unavailableTitle="账单收据暂不可用"
          >
            {() => <div className="overview-receipt-list">{receipts.map((receipt) => (
              <button key={receipt.receiptId} onClick={() => { controller.setBillingView("receipts"); controller.navigate("/console/billing"); }} type="button">
                <span><strong>{receiptLabel(receipt.type)}</strong><small>{formatDate(receipt.createdAt, true)}</small></span>
                <span><strong>{formatUsdMicros(receiptAmount(receipt))}</strong><small>{statusLabel(receipt.status)}</small></span>
                <ChevronRight aria-hidden size={17} />
              </button>
            ))}</div>}
          </SourceState>
        </section>

        <section className="panel overview-announcements">
          <div className="panel-title"><h2>公告</h2><Button onClick={() => controller.navigate("/console/announcements")} size="sm" variant="ghost">全部</Button></div>
          <SourceState
            empty={announcements.length === 0}
            emptyTitle="暂无公告"
            error={controller.sources.announcements.error}
            loading={controller.sources.announcements.loading}
            onRetry={() => void controller.refreshCurrentPage()}
            source={controller.sources.announcements.value}
          >
            {() => <AnnouncementRows announcements={announcements} controller={controller} compact />}
          </SourceState>
        </section>
      </div>
    </section>
  );
}

function WorkspaceSummaryRow({ controller, workspace }: { controller: ConsoleController; workspace: WorkspaceDTO }) {
  const path = `/console/workspaces/${encodeURIComponent(workspace.id)}`;
  return <tr><td><PageLink controller={controller} path={path}><strong>{workspace.name || workspace.id}</strong><small>{workspace.id}</small></PageLink></td><td>{workspace.packageId?.toUpperCase() || "暂不可用"}</td><td><Badge color="secondary">{workspaceLifecycleLabel(workspace.state)}</Badge></td><td>{formatDate(workspace.paidThrough)}</td><td><PageLink controller={controller} path={path}><ChevronRight aria-label="查看" size={17} /></PageLink></td></tr>;
}

function WorkspaceListPage({ controller }: { controller: ConsoleController }) {
  const workspacesUnavailable = controller.sources.workspaces.value?.status === "unavailable" || Boolean(controller.sources.workspaces.error);
  const workspacesPending = !controller.sources.workspaces.value || controller.sources.workspaces.loading;
  return (
    <section className="workspace-list-page" data-slide="C-WS-01">
      <div className="page-toolbar"><p>Workspace 总数：{controller.sources.workspaces.value?.available ? formatCount(controller.sources.workspaces.value.data.total) : "暂不可用"}</p><Button color="primary" disabled={workspacesPending && !workspacesUnavailable} onClick={() => workspacesUnavailable ? void controller.refreshCurrentPage() : controller.navigate("/console/workspaces/new")}>{workspacesUnavailable ? <RefreshCw aria-hidden size={16} /> : <Plus aria-hidden size={16} />}{workspacesUnavailable ? "重试读取" : workspacesPending ? "正在读取" : "新建 Workspace"}</Button></div>
      {controller.launchOperation && !["succeeded", "failed", "refunded"].includes(controller.launchOperation.status) ? <LaunchOperation controller={controller} compact /> : null}
      <section className="panel workspace-list-panel">
        <div className="workspace-list-head"><span>Workspace</span><span>套餐</span><span>生命周期状态</span><span>已付至</span><span /></div>
        <SourceState
          emptyTitle="暂无 Workspace"
          emptyDescription="当前账号尚未开通 Workspace。"
          error={controller.sources.workspaces.error}
          loading={controller.sources.workspaces.loading}
          onRetry={() => void controller.refreshCurrentPage()}
          source={controller.sources.workspaces.value}
        >
          {(data) => <div className="workspace-list" role="list">{data.items.map((workspace) => (
            <PageLink className="workspace-list-row" controller={controller} key={workspace.id} path={`/console/workspaces/${encodeURIComponent(workspace.id)}`}>
              <span className="workspace-list-name"><strong>{workspace.name || workspace.id}</strong><small>{workspace.id}</small></span>
              <span><strong>{workspace.packageId?.toUpperCase() || "暂不可用"}</strong><small>{workspace.storageGb ? `${workspace.storageGb} GB` : "规格暂不可用"}</small></span>
              <span><strong>{workspaceLifecycleLabel(workspace.state)}</strong><small>生命周期状态</small></span>
              <span><strong>{formatDate(workspace.paidThrough)}</strong><small>权益截止</small></span>
              <ChevronRight aria-hidden size={18} />
            </PageLink>
          ))}</div>}
        </SourceState>
        <Pagination current={controller.workspacePageNumber} label="Workspace 分页" onChange={(page) => void controller.changeWorkspacePage(page)} pages={controller.workspacePages} />
      </section>
    </section>
  );
}

function PlanOption({ controller, plan }: { controller: ConsoleController; plan: PricingPlan }) {
  const preview = controller.previews[plan.id];
  return (
    <label className={`plan-option ${plan.available ? "" : "unavailable"}`}>
      <input checked={controller.launchPlan === plan.id} disabled={!plan.available} name="workspace-plan" onChange={() => controller.setLaunchPlan(plan.id)} type="radio" />
      <span className="plan-name"><strong>{plan.name}</strong><Badge color={plan.available ? "success" : "secondary"}>{plan.available ? "可售" : "暂不可用"}</Badge></span>
      <span>{plan.cpu} vCPU / {plan.memoryGb} GB</span>
      <span>{plan.diskGb} GB 持久存储</span>
      <strong>{!plan.available ? "价格暂不可用" : preview ? formatUsdMicros(preview.totalChargeUsdMicros) : "报价读取中"}{plan.available ? <small> / 自然月</small> : null}</strong>
    </label>
  );
}

function WorkspaceLaunchPage({ controller }: { controller: ConsoleController }) {
  const catalog = controller.sources.catalog.value;
  const wallet = sourceData(controller.sources.wallet.value);
  if (controller.launchOperation && !["succeeded", "failed", "refunded"].includes(controller.launchOperation.status)) {
    return <section className="workspace-launch-page" data-slide="C-WS-04"><Button onClick={() => controller.navigate("/console/workspaces")} size="sm" variant="ghost"><ChevronLeft aria-hidden size={16} />Workspace 列表</Button><LaunchOperation controller={controller} /></section>;
  }

  return (
    <section className="workspace-launch-page" data-slide={controller.launchStep === "confirm" ? "C-WS-03" : "C-WS-02"}>
      <Button onClick={() => controller.navigate("/console/workspaces")} size="sm" variant="ghost"><ChevronLeft aria-hidden size={16} />Workspace 列表</Button>
      <section className="panel launch-panel">
        {controller.launchStep === "configure" ? (
          <form className="workspace-launch-form" onSubmit={(event) => { event.preventDefault(); controller.reviewWorkspaceLaunch(); }}>
            <div className="panel-title"><h2>新建 Workspace</h2><span>一个自然月，自动续费关闭</span></div>
            <label>Workspace 名称<input className="native-control" maxLength={80} onChange={(event) => controller.setLaunchName(event.currentTarget.value)} placeholder="例如：产品研发" required value={controller.launchName} /></label>
            <fieldset><legend>套餐</legend>
              {controller.sources.catalog.loading && !catalog ? <div className="source-loading"><span className="spinner" />正在读取计划与价格</div> : null}
              {controller.sources.catalog.error ? <div className="inline-error"><AlertCircle aria-hidden size={16} />计划与价格暂不可用<Button onClick={() => void controller.refreshCurrentPage()} size="sm" variant="ghost">重试</Button></div> : null}
              {catalog ? <div className="plan-grid">{catalog.packages.filter((plan) => plan.id === "basic" || plan.id === "pro").map((plan) => <PlanOption controller={controller} key={plan.id} plan={plan} />)}</div> : null}
            </fieldset>
            <dl className="workspace-launch-balance">
              <div><dt>当前可用余额</dt><dd>{wallet ? formatUsdMicros(wallet.usdMicros) : "暂不可用"}</dd></div>
              <div><dt>Workspace 月度总额</dt><dd>{controller.selectedPrice !== null ? formatUsdMicros(controller.selectedPrice) : "暂不可用"}</dd></div>
            </dl>
            {controller.balanceSufficient === false ? <p className="launch-balance-warning">余额不足，请联系管理员处理余额。Console 不提供在线充值入口。</p> : null}
            <p className="source-note">计算月费、存储月费和 Workspace 月度总额均来自服务端报价；浏览器不会自行计算扣款或扣款后余额。</p>
            <footer><Button color="primary" disabled={!controller.launchName.trim() || !controller.selectedPlan || controller.selectedPrice === null || controller.balanceSufficient !== true} type="submit">下一步：确认<ArrowRight aria-hidden size={16} /></Button></footer>
          </form>
        ) : <WorkspaceLaunchConfirm controller={controller} />}
      </section>
    </section>
  );
}

function WorkspaceLaunchConfirm({ controller }: { controller: ConsoleController }) {
  const plan = controller.selectedPlan;
  const preview = plan ? controller.previews[plan.id] : undefined;
  const wallet = sourceData(controller.sources.wallet.value);
  if (!plan || !preview) return <div className="empty-panel">计划与价格暂不可用</div>;
  return (
    <div className="workspace-launch-confirm">
      <header><p className="eyebrow">C-WS-03</p><h2 tabIndex={-1}>确认开通信息</h2><p>本次只扣除一次 Workspace 月度总额，计算和存储均包含在内。</p></header>
      <dl className="launch-confirm-list">
        <div><dt>Workspace 名称</dt><dd>{controller.launchName.trim()}</dd></div>
        <div><dt>套餐</dt><dd>{plan.name}</dd></div>
        <div><dt>计算规格</dt><dd>{plan.cpu} vCPU / {plan.memoryGb} GB</dd></div>
        <div><dt>持久存储</dt><dd>{plan.diskGb} GB</dd></div>
        <div><dt>计算组成金额</dt><dd>{preview.compute ? formatUsdMicros(preview.compute.chargeUsdMicros) : "暂不可用"}</dd></div>
        <div><dt>存储组成金额</dt><dd>{preview.storage ? formatUsdMicros(preview.storage.chargeUsdMicros) : "暂不可用"}</dd></div>
        <div><dt>服务端权威总价</dt><dd className="confirm-price">{formatUsdMicros(preview.totalChargeUsdMicros)}</dd></div>
        <div><dt>当前可用余额</dt><dd>{wallet ? formatUsdMicros(wallet.usdMicros) : "暂不可用"}</dd></div>
        <div><dt>价格版本</dt><dd>{preview.priceVersion}</dd></div>
        <div><dt>权益期</dt><dd>一个自然月</dd></div>
        <div><dt>自动续费</dt><dd>关闭</dd></div>
      </dl>
      <div className="launch-confirm-check"><Checkbox checked={controller.launchConfirmed} label="我确认一次性预付 Workspace 月度总额并开通" onChange={controller.setLaunchConfirmed} /></div>
      <footer><Button onClick={() => { controller.setLaunchStep("configure"); controller.setLaunchConfirmed(false); }} variant="outline">返回修改</Button><Button busy={controller.commandBusy} color="primary" disabled={!controller.launchConfirmed || controller.balanceSufficient !== true} onClick={() => void controller.submitWorkspaceLaunch()}>确认预付并开通</Button></footer>
    </div>
  );
}

function LaunchOperation({ compact, controller }: { compact?: boolean; controller: ConsoleController }) {
  const operation = controller.launchOperation;
  if (!operation) return null;
  const phaseIndex = launchPhases.findIndex(([code]) => operation.phase?.includes(code));
  return (
    <section className="panel launch-operation" data-slide="C-WS-04">
      <div className="launch-operation-head"><div><p className="eyebrow">C-WS-04</p><h2>开通进度与结果</h2><p>{statusLabel(operation.status)} · {operation.status}</p></div><Badge color={operation.status === "succeeded" ? "success" : operation.status === "manual_review" ? "warning" : "secondary"}>{statusLabel(operation.status)}</Badge></div>
      <dl className="operation-readback">
        <div><dt>operation ID</dt><dd><code>{operation.operationId}</code></dd></div>
        <div><dt>Workspace</dt><dd>{operation.name}</dd></div>
        <div><dt>套餐</dt><dd>{operation.packageId?.toUpperCase()}</dd></div>
        <div><dt>当前 phase</dt><dd>{operation.phase || "暂不可用"}</dd></div>
        {!compact ? <><div><dt>创建时间</dt><dd>{formatDate(operation.createdAt, true)}</dd></div><div><dt>最后更新</dt><dd>{formatDate(operation.updatedAt, true)}</dd></div><div><dt>errorCode</dt><dd>{operation.errorCode || "暂不可用"}</dd></div></> : null}
      </dl>
      {!compact ? <ol className="workspace-progress">{launchPhases.map(([code, label], index) => <li className={phaseIndex >= index || operation.status === "succeeded" ? "complete" : ""} key={code}><span>{label}</span><small>{phaseIndex === index ? "当前阶段" : phaseIndex > index || operation.status === "succeeded" ? "已完成" : "等待"}</small></li>)}</ol> : null}
      {controller.launchPollIssue ? <p className="inline-error">结果待确认。请刷新同一 operation，禁止重复购买。</p> : null}
      <div className="launch-operation-actions">
        {operation.status === "succeeded" && operation.workspaceId ? <Button color="primary" onClick={() => controller.navigate(`/console/workspaces/${encodeURIComponent(operation.workspaceId!)}`)}>查看 Workspace</Button> : null}
        <Button onClick={() => void controller.refreshCurrentPage()} variant="outline"><RefreshCw aria-hidden size={16} />刷新 operation</Button>
        {["failed", "refunded"].includes(operation.status) ? <Button onClick={() => controller.navigate("/console/workspaces")} variant="outline">返回列表</Button> : null}
      </div>
    </section>
  );
}

function SecretRow({ busy, label, onCopy, onHide, onReveal, revealed, value }: { busy: boolean; label: string; onCopy: () => void; onHide: () => void; onReveal: () => void; revealed: boolean; value?: string }) {
  return <div><dt>{label}</dt><dd className="credential-actions"><code>{revealed ? value || "暂不可用" : "••••••••••••"}</code>{revealed ? <><Button aria-label="隐藏" onClick={onHide} size="sm" uniform variant="ghost"><EyeOff aria-hidden size={16} /></Button><Button aria-label="复制" onClick={onCopy} size="sm" uniform variant="ghost"><Copy aria-hidden size={16} /></Button></> : <Button aria-label="显示" busy={busy} onClick={onReveal} size="sm" variant="outline"><Eye aria-hidden size={16} />显示</Button>}</dd></div>;
}

function WorkspaceDetailPage({ controller }: { controller: ConsoleController }) {
  const workspaceSource = controller.sources.workspaceDetail.value;
  const runtime = sourceData(controller.sources.runtime.value);
  if (workspaceSource?.available && workspaceSource.data === null) return <section className="workspace-detail-page"><div className="empty-panel"><AlertCircle /><h2>Workspace 不存在</h2><p>该 Workspace 不存在或当前账号无权访问。</p><Button onClick={() => controller.navigate("/console/workspaces")} variant="outline">返回列表</Button></div></section>;
  return (
    <section className="workspace-detail-page" data-slide="C-WS-05">
      <Button onClick={() => controller.navigate("/console/workspaces")} size="sm" variant="ghost"><ChevronLeft aria-hidden size={16} />Workspace 列表</Button>
      <SourceState error={controller.sources.workspaceDetail.error} loading={controller.sources.workspaceDetail.loading} onRetry={() => void controller.refreshCurrentPage()} source={workspaceSource} unavailableTitle="Workspace 详情暂不可用">
        {(detail) => detail ? <>
          <section className="panel workspace-identity-panel"><div className="workspace-heading"><div><p className="eyebrow">C-WS-05</p><h2>{detail.name || detail.id}</h2><span>{detail.id}</span></div><Button onClick={() => void controller.refreshCurrentPage()} variant="outline"><RefreshCw aria-hidden size={16} />刷新</Button></div><dl className="data-list"><div><dt>生命周期状态</dt><dd>{workspaceLifecycleLabel(detail.state)}</dd></div><div><dt>运行状态</dt><dd>{runtime ? workspaceStatusLabel(runtime) : "暂不可用"}</dd></div></dl></section>
          <section className="panel workspace-access-panel"><div className="panel-title"><h2>访问与凭据</h2><span>Secret 60 秒后自动隐藏</span></div>
            <SourceState error={controller.sources.runtime.error} loading={controller.sources.runtime.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.runtime.value} unavailableTitle="Runtime 状态暂不可用">
              {(runtimeData) => {
                const mount = runtimeData.checks.find((check) => check.name === "ready_pod_uses_retained_pvc");
                const service = runtimeData.checks.find((check) => check.name !== "ready_pod_uses_retained_pvc" && check.name.includes("ready"));
                const canOpen = runtimeData.status === "running" && runtimeData.ready && Boolean(runtimeData.url);
                return <dl className="data-list"><div><dt>Runtime ready</dt><dd>{runtimeData.ready ? "是" : "否"}</dd></div><div><dt>挂载检查</dt><dd>{mount ? (mount.ok ? "通过" : "未通过") : "暂不可用"}</dd></div><div><dt>服务健康</dt><dd>{service ? (service.ok ? "通过" : "未通过") : runtimeData.ready ? "通过" : "暂不可用"}</dd></div><div><dt>Workspace URL</dt><dd>{runtimeData.url ? <a href={runtimeData.url} rel="noreferrer" target="_blank">{runtimeData.url}<ExternalLink aria-hidden size={14} /></a> : "暂不可用"}</dd></div><div><dt>用户名</dt><dd>{runtimeData.access?.username || controller.secrets.workspace?.username || "暂不可用"}</dd></div>
                  <SecretRow busy={controller.workspaceSecretBusy} label="密码" onCopy={() => void controller.copyText(controller.secrets.workspace?.password, "Workspace 密码已复制")} onHide={controller.clearSecrets} onReveal={() => void controller.revealWorkspacePassword()} revealed={Boolean(controller.secrets.workspace)} value={controller.secrets.workspace?.password} />
                  <SecretRow busy={controller.gatewaySecretBusy} label="Workspace Key" onCopy={() => void controller.copyText(controller.secrets.apiKey?.value, "Workspace Key 已复制")} onHide={controller.clearSecrets} onReveal={() => void controller.revealWorkspaceKey()} revealed={Boolean(controller.secrets.apiKey)} value={controller.secrets.apiKey?.value} />
                  <div><dt>操作</dt><dd className="workspace-actions"><Button busy={controller.workspaceSecretBusy} onClick={() => void controller.rotateWorkspacePassword()} variant="outline">轮换密码</Button><Button color="primary" disabled={!canOpen} onClick={() => runtimeData.url && window.open(runtimeData.url, "_blank", "noopener,noreferrer")}>打开 Workspace<ExternalLink aria-hidden size={16} /></Button></dd></div>
                </dl>;
              }}
            </SourceState>
          </section>
          <section className="panel workspace-facts-panel"><div className="panel-title"><h2>套餐与条款</h2></div><dl className="data-list"><div><dt>套餐</dt><dd>{detail.packageId?.toUpperCase() || "暂不可用"}</dd></div><div><dt>CPU / 内存规格</dt><dd>暂不可用</dd></div><div><dt>持久存储</dt><dd>{detail.storageGb ? `${detail.storageGb} GB` : "暂不可用"}</dd></div><div><dt>Workspace 月度总价</dt><dd>{formatUsdMicros(detail.totalUsdMicros)}</dd></div><div><dt>价格版本</dt><dd>{detail.priceVersion || "暂不可用"}</dd></div><div><dt>创建时间</dt><dd>{formatDate(detail.createdAt, true)}</dd></div><div><dt>权益期</dt><dd>{detail.periodStart && detail.paidThrough ? `${formatDate(detail.periodStart)} 至 ${formatDate(detail.paidThrough)}` : "暂不可用"}</dd></div><div><dt>续费状态</dt><dd>{detail.renewalStatus || "暂不可用"}</dd></div><div><dt>自动续费</dt><dd>{detail.autoRenew ? "开启" : "关闭"}</dd></div></dl><p className="source-note">CPU / 内存未由当前 Workspace DTO 投影，因此不按套餐 ID 推导；自动续费启用路径未开放。</p></section>
        </> : null}
      </SourceState>
    </section>
  );
}

function ApiTabs({ controller }: { controller: ConsoleController }) {
  return <nav aria-label="API 服务导航" className="gateway-tabs">{apiMenu.map((item) => <PageLink className={controller.path === item.path ? "active" : ""} controller={controller} key={item.path} path={item.path}>{item.label}</PageLink>)}</nav>;
}

function ApiOverview({ controller }: { controller: ConsoleController }) {
  const wallet = sourceData(controller.sources.wallet.value);
  const usage = sourceData(controller.sources.accountUsage.value);
  const endpoint = sourceData(controller.sources.endpoint.value);
  return <div className="api-overview" data-slide="C-API-01">
    <section className="spend-strip"><div><WalletCards aria-hidden size={19} /><span>可用余额</span><strong>{wallet ? formatUsdMicros(wallet.usdMicros) : "暂不可用"}</strong></div><div><CircleDollarSign aria-hidden size={19} /><span>本月实际费用</span><strong>{usage ? formatUsdMicros(usage.totalActualCostUsdMicros) : "暂不可用"}</strong></div><div><Server aria-hidden size={19} /><span>本月请求次数</span><strong>{usage ? formatCount(usage.totalRequests) : "暂不可用"}</strong></div></section>
    <section className="panel gateway-detail"><div className="panel-title"><h2>API 服务</h2><span>{endpoint?.baseUrl || "Endpoint 暂不可用"}</span></div><p className="source-note">这里展示 API 服务钱包与真实请求消费，不展示 Workspace Receipt，也不提供充值按钮。</p></section>
    <section className="panel"><div className="panel-title"><h2>余额历史</h2></div><SourceState emptyTitle="暂无余额历史" error={controller.sources.balanceHistory.error} loading={controller.sources.balanceHistory.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.balanceHistory.value} unavailableTitle="余额历史暂不可用">{(data) => <><div className="table-wrap"><table><thead><tr><th>时间</th><th>类型</th><th>金额</th><th>状态</th></tr></thead><tbody>{data.items.map((item, index) => <tr key={`${item.createdAt}-${index}`}><td>{formatDate(item.usedAt || item.createdAt, true)}</td><td>{item.type}</td><td>{formatUsdMicros(item.valueUsdMicros)}</td><td>{statusLabel(item.status)}</td></tr>)}</tbody></table></div><Pagination current={data.page} label="余额历史分页" onChange={(page) => void controller.changeBalancePage(page)} pages={data.pages} /></>}</SourceState></section>
  </div>;
}

function RequestRows({ items }: { items: GatewayUsageItem[] }) {
  return <>
    <div className="table-wrap request-table-desktop"><table className="gateway-usage-table"><thead><tr><th>时间</th><th>模型</th><th>端点</th><th>实际金额</th><th>请求编号</th></tr></thead><tbody>{items.map((item) => <tr key={item.requestId}><td>{formatDate(item.createdAt, true)}</td><td>{item.model}</td><td><code>{item.inboundEndpoint}</code></td><td>{formatUsdMicros(item.actualCostUsdMicros)}</td><td><code>{item.requestId}</code></td></tr>)}</tbody></table></div>
    <div className="request-list-mobile" role="list">{items.map((item) => <article key={item.requestId} role="listitem"><span><strong>{item.model}</strong><small>{formatDate(item.createdAt, true)}</small></span><span><strong>{formatUsdMicros(item.actualCostUsdMicros)}</strong><small>{item.inboundEndpoint}</small></span><code>{item.requestId}</code></article>)}</div>
  </>;
}

function UsagePage({ controller }: { controller: ConsoleController }) {
  const keys = sourceData(controller.sources.usageKeys.value)?.items || [];
  const usage = sourceData(controller.sources.usage.value);
  return <section className="panel" data-slide="C-API-02"><div className="panel-title"><h2>使用记录</h2><span>请求级事实来自 API 服务</span></div><div className="gateway-usage-toolbar">
    <Select label="API Key" onChange={(value) => void controller.chooseUsageKey(value)} options={keys.map((key) => ({ label: `${key.name} · ${key.id}`, value: key.id }))} placeholder="选择 API Key" value={controller.selectedUsageKeyId} />
    <SegmentedControl ariaLabel="统计周期" onChange={(value) => void controller.chooseUsagePeriod(value)} options={[{ value: "day", label: "今日" }, { value: "week", label: "本周" }, { value: "month", label: "本月" }]} value={controller.usagePeriod as "day" | "week" | "month"} />
  </div>
    <SourceState empty={keys.length === 0} emptyTitle="暂无 API Key" error={controller.sources.usageKeys.error} loading={controller.sources.usageKeys.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.usageKeys.value} unavailableTitle="API Key 暂不可用">{() => <>
      <SourceState error={controller.sources.usageSummary.error} loading={controller.sources.usageSummary.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.usageSummary.value} unavailableTitle="使用汇总暂不可用">{(summary) => <dl className="usage-summary-strip"><div><dt>汇总请求次数</dt><dd>{formatCount(summary.totalRequests)}</dd></div><div><dt>汇总总 Token</dt><dd>{formatCount(summary.totalTokens)}</dd></div><div><dt>汇总实际金额</dt><dd>{formatUsdMicros(summary.totalActualCostUsdMicros)}</dd></div></dl>}</SourceState>
      <SourceState empty={Boolean(usage && usage.items.length === 0)} emptyTitle="暂无请求记录" error={controller.sources.usage.error} loading={controller.sources.usage.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.usage.value} unavailableTitle="使用记录暂不可用">{(data) => <><RequestRows items={data.items} /><Pagination current={data.page} label="请求记录分页" onChange={(page) => void controller.changeUsagePage(page)} pages={data.pages} /></>}</SourceState>
    </>}</SourceState>
  </section>;
}

function ApiPage({ controller }: { controller: ConsoleController }) {
  const page = apiPage(controller.path);
  return <section className="gateway-page api-page"><ApiTabs controller={controller} />{page === "overview" ? <ApiOverview controller={controller} /> : page === "usage" ? <UsagePage controller={controller} /> : <KeysPanel csrfToken={controller.session?.csrfToken || ""} />}</section>;
}

function BillingPage({ controller }: { controller: ConsoleController }) {
  const receipts = sourceData(controller.sources.receipts.value)?.receipts || [];
  const receipt = sourceData(controller.sources.receiptDetail.value);
  return <section className="billing-page">
    <SegmentedControl ariaLabel="账单视图" block onChange={(value) => controller.setBillingView(value)} options={[{ value: "terms", label: "Workspace 条款" }, { value: "receipts", label: "账单收据" }]} value={controller.billingView} />
    {controller.billingView === "terms" ? <section className="panel billing-surface" data-slide="C-BIL-01"><div className="panel-title"><h2>Workspace 条款</h2><span>Control Plane 当前商业条款</span></div><SourceState emptyTitle="暂无 Workspace 条款" error={controller.sources.workspaces.error} loading={controller.sources.workspaces.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.workspaces.value} unavailableTitle="Workspace 条款暂不可用">{(data) => <><div className="table-wrap billing-table-desktop"><table><thead><tr><th>Workspace</th><th>套餐</th><th>月度总价</th><th>计费周期</th><th>续费状态</th><th>自动续费</th></tr></thead><tbody>{data.items.map((item) => <tr key={item.id}><td><PageLink controller={controller} path={`/console/workspaces/${encodeURIComponent(item.id)}`}>{item.name || item.id}</PageLink></td><td>{item.packageId?.toUpperCase() || "暂不可用"}</td><td>{formatUsdMicros(item.totalUsdMicros)}</td><td>{item.periodStart && item.paidThrough ? `${formatDate(item.periodStart)} 至 ${formatDate(item.paidThrough)}` : "暂不可用"}</td><td>{item.renewalStatus || "暂不可用"}</td><td>{item.autoRenew ? "开启" : "关闭"}</td></tr>)}</tbody></table></div><div className="billing-list-mobile" role="list">{data.items.map((item) => <PageLink controller={controller} key={item.id} path={`/console/workspaces/${encodeURIComponent(item.id)}`}><span><strong>{item.name || item.id}</strong><small>{item.packageId?.toUpperCase() || "暂不可用"}</small></span><span><strong>{formatUsdMicros(item.totalUsdMicros)}</strong><small>已付至 {formatDate(item.paidThrough)}</small></span><ChevronRight aria-hidden size={18} /></PageLink>)}</div><Pagination current={controller.workspacePageNumber} label="Workspace 条款分页" onChange={(page) => void controller.changeWorkspacePage(page)} pages={controller.workspacePages} /></>}</SourceState></section> : <>
      <section className="panel billing-surface" data-slide="C-BIL-02"><div className="panel-title"><h2>账单收据</h2><span>按时间顺序分页</span></div><SourceState empty={receipts.length === 0} emptyTitle="暂无账单收据" error={controller.sources.receipts.error} loading={controller.sources.receipts.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.receipts.value} unavailableTitle="账单收据暂不可用">{() => <><div className="table-wrap billing-table-desktop"><table><thead><tr><th>时间</th><th>类型</th><th>Workspace</th><th>金额</th><th>状态</th><th>操作</th></tr></thead><tbody>{receipts.map((item) => <tr key={item.receiptId}><td>{formatDate(item.createdAt, true)}</td><td>{receiptLabel(item.type)}</td><td>{item.workspaceId || "暂不可用"}</td><td>{formatUsdMicros(receiptAmount(item))}</td><td>{statusLabel(item.status)}</td><td><Button onClick={() => void controller.selectReceipt(item.receiptId)} size="sm" variant="ghost">查看</Button></td></tr>)}</tbody></table></div><div className="billing-list-mobile" role="list">{receipts.map((item) => <button key={item.receiptId} onClick={() => void controller.selectReceipt(item.receiptId)} role="listitem"><span><strong>{receiptLabel(item.type)}</strong><small>{formatDate(item.createdAt, true)}</small></span><span><strong>{formatUsdMicros(receiptAmount(item))}</strong><small>{statusLabel(item.status)}</small></span><ChevronRight aria-hidden size={18} /></button>)}</div><ReceiptCursorNotice controller={controller} /></>}</SourceState></section>
      {controller.selectedReceiptId ? <ReceiptDetail controller={controller} receipt={receipt} /> : null}
    </>}
  </section>;
}

function ReceiptCursorNotice({ controller }: { controller: ConsoleController }) {
  const page = sourceData(controller.sources.receipts.value);
  if (!page || (!controller.receiptCursorStack.length && !page.hasMore)) return null;
  return <nav aria-label="账单收据分页" className="pagination"><Button disabled={controller.sources.receipts.loading || controller.receiptCursorStack.length === 0} onClick={() => void controller.previousReceiptPage()} size="sm" variant="outline"><ChevronLeft aria-hidden size={16} />上一页</Button><span>第 {controller.receiptCursorStack.length + 1} 页</span><Button disabled={controller.sources.receipts.loading || !page.hasMore || !page.nextCursor} onClick={() => void controller.nextReceiptPage()} size="sm" variant="outline">下一页<ChevronRight aria-hidden size={16} /></Button></nav>;
}

function ReceiptDetail({ controller, receipt }: { controller: ConsoleController; receipt: BillingReceipt | null }) {
  const components = receipt?.components;
  return <section className="panel receipt-detail" data-slide="C-BIL-03"><div className="panel-title"><h2>收据详情</h2><Button aria-label="关闭收据详情" onClick={() => { controller.clearReceiptDetail(); controller.clearSecrets(); }} size="sm" variant="ghost">关闭</Button></div><SourceState error={controller.sources.receiptDetail.error} loading={controller.sources.receiptDetail.loading} onRetry={() => controller.selectedReceiptId && void controller.selectReceipt(controller.selectedReceiptId)} source={controller.sources.receiptDetail.value} unavailableTitle="收据详情暂不可用">{(detail) => <dl className="data-list"><div><dt>Receipt ID</dt><dd>{detail.receiptId}</dd></div><div><dt>类型</dt><dd>{receiptLabel(detail.type)}</dd></div><div><dt>状态</dt><dd>{statusLabel(detail.status)}</dd></div><div><dt>创建时间</dt><dd>{formatDate(detail.createdAt, true)}</dd></div><div><dt>Workspace ID</dt><dd>{detail.workspaceId || "暂不可用"}</dd></div><div><dt>总额</dt><dd>{formatUsdMicros(detail.totalUsdMicros ?? detail.chargeUsdMicros)}</dd></div>{detail.refundUsdMicros !== undefined ? <div><dt>退款额</dt><dd>{formatUsdMicros(detail.refundUsdMicros)}</dd></div> : null}<div><dt>计费周期</dt><dd>{detail.periodStart && detail.paidThrough ? `${formatDate(detail.periodStart)} 至 ${formatDate(detail.paidThrough)}` : "暂不可用"}</dd></div><div><dt>价格版本</dt><dd>{detail.priceVersion || "暂不可用"}</dd></div><div><dt>计算组成金额</dt><dd>{components?.compute ? formatUsdMicros(components.compute.chargeUsdMicros) : "暂不可用"}</dd></div><div><dt>存储组成金额和容量</dt><dd>{components?.storage ? `${formatUsdMicros(components.storage.chargeUsdMicros)} · ${components.storage.sizeGb} GB` : "暂不可用"}</dd></div><div><dt>扣款引用</dt><dd>{detail.chargeReference || "暂不可用"}</dd></div></dl>}</SourceState></section>;
}

function AnnouncementRows({ announcements, compact, controller }: { announcements: AnnouncementDTO[]; compact?: boolean; controller: ConsoleController }) {
  return <div className={compact ? "compact-announcement-list" : "announcement-list"}>{announcements.map((announcement) => <article className="announcement-item" key={announcement.id}><header><div><h3>{announcement.title}</h3><Badge color={announcement.read ? "secondary" : "info"}>{announcement.read ? "已读" : "未读"}</Badge></div><span>{formatDate(announcement.publishedAt || announcement.startsAt || announcement.updatedAt, true)}</span></header><p>{announcement.body}</p>{announcement.read ? null : <Button busy={controller.announcementBusy === announcement.id} onClick={() => void controller.markRead(announcement.id)} size="sm" variant="outline">标记已读</Button>}</article>)}</div>;
}

function AnnouncementsPage({ controller }: { controller: ConsoleController }) {
  const announcements = sourceData(controller.sources.announcements.value)?.items || [];
  return <section className="announcements-page" data-slide="C-ANN-01"><div className="page-toolbar"><p>只展示当前有效发布时间窗内的已发布公告。</p><Button onClick={() => void controller.refreshCurrentPage()} variant="outline"><RefreshCw aria-hidden size={16} />刷新</Button></div><section className="panel"><div className="panel-title"><h2>公告列表</h2><span>{announcements.length ? `${announcements.length} 条` : ""}</span></div><SourceState empty={announcements.length === 0} emptyTitle="暂无公告" error={controller.sources.announcements.error} loading={controller.sources.announcements.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.announcements.value} unavailableTitle="公告暂不可用">{() => <AnnouncementRows announcements={announcements} controller={controller} />}</SourceState></section></section>;
}

export function CustomerPages({ controller }: { controller: ConsoleController }) {
  if (controller.path === "/console" || controller.path === "/console/overview") return <OverviewPage controller={controller} />;
  if (workspacePage(controller.path) === "list") return <WorkspaceListPage controller={controller} />;
  if (workspacePage(controller.path) === "new") return <WorkspaceLaunchPage controller={controller} />;
  if (workspacePage(controller.path) === "detail") return <WorkspaceDetailPage controller={controller} />;
  if (controller.path.startsWith("/console/api")) return <ApiPage controller={controller} />;
  if (controller.path === "/console/billing") return <BillingPage controller={controller} />;
  if (controller.path === "/console/announcements") return <AnnouncementsPage controller={controller} />;
  return null;
}
