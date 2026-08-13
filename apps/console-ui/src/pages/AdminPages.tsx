import {
  Activity,
  Ban,
  ChevronLeft,
  ChevronRight,
  CircleDollarSign,
  Database,
  ExternalLink,
  Plus,
  RefreshCw,
  Server,
  ShieldAlert,
  WalletCards
} from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";

import type { ConsoleController } from "../app/use-console-controller.ts";
import type {
  AnnouncementDTO,
  AnnouncementDraftRequest,
  OperatorAccountDTO,
  OperatorHealthDTO,
  OperatorReconciliationItemDTO,
  OperatorResourceDTO,
  OperatorWorkspaceDTO,
  ReadinessFact,
  SourceEnvelope,
  WalletAdjustmentRequest
} from "../api/dtos.ts";
import { SourceState } from "../components/source/SourceState.tsx";
import { Badge, Button, Field, Modal, Select } from "../components/ui/index.ts";
import { formatCount, formatDate, formatUsdMicros } from "../console-model.ts";

type BadgeTone = "danger" | "info" | "secondary" | "success" | "warning";

function sourceData<T>(source: SourceEnvelope<T> | null | undefined): T | null {
  return source?.available ? source.data : null;
}

function sourceStatusLabel(status?: string) {
  return ({ available: "可用", empty: "暂无数据", unavailable: "暂不可用" } as Record<string, string>)[status || ""] || "暂不可用";
}

function sourceTone(status?: string): BadgeTone {
  if (status === "available") return "success";
  if (status === "empty") return "secondary";
  return "warning";
}

function statusLabel(status?: string) {
  return ({
    active: "正常",
    disabled: "已停用",
    draft: "草稿",
    failed: "失败",
    manual_review: "待人工确认",
    processing: "处理中",
    published: "已发布",
    queued: "等待处理",
    scheduled: "已排期",
    succeeded: "已完成",
    withdrawn: "已撤下"
  } as Record<string, string>)[status || ""] || (status || "暂不可用");
}

function statusTone(status?: string): BadgeTone {
  if (["active", "published", "succeeded", "ready", "running"].includes(status || "")) return "success";
  if (["disabled", "failed"].includes(status || "")) return "danger";
  if (["manual_review", "scheduled", "processing", "queued"].includes(status || "")) return "warning";
  return "secondary";
}

function SourceBadge({ source }: { source: SourceEnvelope<unknown> | null | undefined }) {
  return <Badge color={sourceTone(source?.status)}>{sourceStatusLabel(source?.status)}</Badge>;
}

function SourceValue<T>({ source, children }: { source: SourceEnvelope<T> | null | undefined; children: (data: T) => ReactNode }) {
  if (!source?.available) return <span className="source-value source-value--unavailable">暂不可用</span>;
  return (
    <span className="source-value">
      <strong>{children(source.data)}</strong>
      <small>{source.source} · {sourceStatusLabel(source.status)}{source.fetchedAt ? ` · ${formatDate(source.fetchedAt, true)}` : ""}</small>
    </span>
  );
}

function Pagination({ current, label, onChange, pages }: { current: number; label: string; onChange: (page: number) => void; pages: number }) {
  if (pages <= 1) return null;
  return (
    <nav aria-label={label} className="pagination">
      <Button disabled={current <= 1} onClick={() => onChange(current - 1)} size="sm" variant="outline"><ChevronLeft aria-hidden size={16} />上一页</Button>
      <span>第 {current} / {pages} 页</span>
      <Button disabled={current >= pages} onClick={() => onChange(current + 1)} size="sm" variant="outline">下一页<ChevronRight aria-hidden size={16} /></Button>
    </nav>
  );
}

function Metric({ label, note, value }: { label: string; note: string; value: string }) {
  return <article className="band-metric"><span>{label}</span><strong>{value}</strong><small>{note}</small></article>;
}

function healthStatus(source: SourceEnvelope<ReadinessFact> | null | undefined) {
  if (!source?.available || source.status === "empty") return { label: "暂不可用", tone: "warning" as BadgeTone };
  if (source.data.ready === true) return { label: "正常", tone: "success" as BadgeTone };
  if (source.data.ready === false) return { label: "需处理", tone: "danger" as BadgeTone };
  return { label: "暂不可用", tone: "warning" as BadgeTone };
}

function overallHealth(source: SourceEnvelope<OperatorHealthDTO> | null | undefined) {
  if (!source?.available || source.status === "empty") return { label: "暂不可用", tone: "warning" as BadgeTone };
  const services = [source.data.controlPlane, source.data.gateway, source.data.fabric, source.data.runtime, source.data.ledger];
  for (const service of services) {
    if (!service.available || service.status === "empty" || service.data.ready === undefined) {
      return { label: "暂不可用", tone: "warning" as BadgeTone };
    }
    if (service.data.ready === false) return { label: "需处理", tone: "danger" as BadgeTone };
  }
  return { label: "正常", tone: "success" as BadgeTone };
}

function SourceTable({ rows }: { rows: Array<{ label: string; source: SourceEnvelope<unknown> }> }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>来源</th><th>状态</th><th>Console 读回</th><th>权威更新时间</th></tr></thead>
        <tbody>{rows.map(({ label, source }) => (
          <tr key={label}>
            <td><strong>{label}</strong><small>{source.source}</small></td>
            <td><SourceBadge source={source} /></td>
            <td>{source.fetchedAt ? formatDate(source.fetchedAt, true) : "暂不可用"}</td>
            <td>{source.sourceUpdatedAt ? formatDate(source.sourceUpdatedAt, true) : "-"}</td>
          </tr>
        ))}</tbody>
      </table>
    </div>
  );
}

function AnnouncementList({ announcements, busy, controller, onBusy }: {
  announcements: AnnouncementDTO[];
  busy: string;
  controller: ConsoleController;
  onBusy: (id: string) => void;
}) {
  const run = async (id: string, action: () => Promise<void>) => {
    onBusy(id);
    try { await action(); } finally { onBusy(""); }
  };
  return (
    <div className="announcement-list">
      {announcements.map((announcement) => (
        <article className="announcement-item" key={announcement.id}>
          <header>
            <div><h3>{announcement.title}</h3><Badge color={statusTone(announcement.status)}>{statusLabel(announcement.status)}</Badge></div>
            <span>{formatDate(announcement.updatedAt, true)}</span>
          </header>
          <p>{announcement.body}</p>
          <dl className="data-list">
            <div><dt>开始时间</dt><dd>{announcement.startsAt ? formatDate(announcement.startsAt, true) : "未设置"}</dd></div>
            <div><dt>结束时间</dt><dd>{announcement.endsAt ? formatDate(announcement.endsAt, true) : "未设置"}</dd></div>
          </dl>
          <footer className="table-actions">
            {["draft", "scheduled"].includes(announcement.status) ? <Button busy={busy === announcement.id} onClick={() => void run(announcement.id, () => controller.publishAnnouncement(announcement.id))} size="sm" variant="outline">发布</Button> : null}
            {announcement.status === "published" ? <Button busy={busy === announcement.id} color="danger" onClick={() => void run(announcement.id, () => controller.withdrawAnnouncement(announcement.id))} size="sm" variant="ghost">撤下</Button> : null}
          </footer>
        </article>
      ))}
    </div>
  );
}

function AnnouncementDraftModal({ controller, onClose, open }: { controller: ConsoleController; onClose: () => void; open: boolean }) {
  const [form, setForm] = useState<AnnouncementDraftRequest>({ title: "", body: "", startsAt: "", endsAt: "" });
  const [busy, setBusy] = useState(false);
  const updateForm = (field: keyof AnnouncementDraftRequest, value: string) => setForm((current) => ({ ...current, [field]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!form.title.trim() || !form.body.trim()) return;
    setBusy(true);
    const ok = await controller.createAnnouncement({
      title: form.title.trim(),
      body: form.body.trim(),
      ...(form.startsAt?.trim() ? { startsAt: form.startsAt.trim() } : {}),
      ...(form.endsAt?.trim() ? { endsAt: form.endsAt.trim() } : {})
    });
    setBusy(false);
    if (ok) {
      setForm({ title: "", body: "", startsAt: "", endsAt: "" });
      onClose();
    }
  };
  return (
    <Modal
      className="modal"
      description="草稿由 Control Plane 保存；发布和撤下会单独确认并写入审计。"
      footer={<><Button disabled={busy} onClick={onClose} variant="outline">取消</Button><Button busy={busy} color="primary" form="announcement-draft-form" type="submit">保存草稿</Button></>}
      onClose={onClose}
      open={open}
      title="新建公告草稿"
    >
      <form id="announcement-draft-form" onSubmit={submit}>
        <Field autoFocus label="标题" maxLength={120} onChange={(event) => updateForm("title", event.currentTarget.value)} required value={form.title} />
        <Field label="正文" maxLength={4000} multiline onChange={(event) => updateForm("body", event.currentTarget.value)} required rows={7} value={form.body} />
        <Field label="开始时间" onChange={(event) => updateForm("startsAt", event.currentTarget.value)} optional placeholder="2026-07-30T09:00:00Z" value={form.startsAt} />
        <Field label="结束时间" onChange={(event) => updateForm("endsAt", event.currentTarget.value)} optional placeholder="2026-08-01T09:00:00Z" value={form.endsAt} />
      </form>
    </Modal>
  );
}

function OverviewPage({ controller }: { controller: ConsoleController }) {
  const [draftOpen, setDraftOpen] = useState(false);
  const [announcementBusy, setAnnouncementBusy] = useState("");
  const overviewSource = controller.sources.operatorOverview.value;
  const overview = sourceData(overviewSource);
  const announcements = sourceData(controller.sources.operatorAnnouncements.value)?.items || [];
  const health = overallHealth(overview?.health);
  const reconciliationCount = overview?.reconciliation.available ? overview.reconciliation.data.total : null;
  const attentionPath = reconciliationCount && reconciliationCount > 0 ? "/admin/billing" : health.label !== "正常" ? "/admin/system" : "";
  const rows = overview ? [
    { label: "Control Plane 账户", source: overview.accounts },
    { label: "Gateway 汇总余额", source: overview.wallet },
    { label: "Gateway Key", source: overview.keys },
    { label: "Gateway API 用量", source: overview.usage },
    { label: "Control Plane Workspace", source: overview.workspaces },
    { label: "Fabric 资源", source: overview.resources },
    { label: "Ledger 计费复核", source: overview.reconciliation },
    { label: "系统健康", source: overview.health }
  ] : [];

  return (
    <section className="admin-dashboard" data-slide="A-OV-01 A-OV-02">
      <section className="account-band">
        <div className="account-band-copy"><h2>运营总览</h2></div>
        <div className="band-metrics operator-metrics">
          <Metric label="计费账户" note={overview?.accounts.available ? `正常 ${formatCount(overview.accounts.data.active)} · 停用 ${formatCount(overview.accounts.data.disabled)}` : "来源暂不可用"} value={overview?.accounts.available ? formatCount(overview.accounts.data.total) : "暂不可用"} />
          <Metric label="Workspace" note="Control Plane" value={overview?.workspaces.available ? formatCount(overview.workspaces.data.total) : "暂不可用"} />
          <Metric label="资源" note="Fabric 聚合" value={overview?.resources.available ? formatCount(overview.resources.data.total) : "暂不可用"} />
          <Metric label="待复核" note="Ledger / Control Plane" value={reconciliationCount === null ? "暂不可用" : formatCount(reconciliationCount)} />
        </div>
      </section>

      <SourceState error={controller.sources.operatorOverview.error} loading={controller.sources.operatorOverview.loading} onRetry={() => void controller.refreshCurrentPage()} source={overviewSource} unavailableTitle="运营概览暂不可用">
        {(data) => (
          <>
            <section className="metric-row">
              <article><WalletCards aria-hidden size={22} /><span>汇总余额<strong>{data.wallet.available ? formatUsdMicros(data.wallet.data.usdMicros) : "暂不可用"}</strong><small>Gateway 权威聚合</small></span></article>
              <article><Server aria-hidden size={22} /><span>Key 总数<strong>{data.keys.available ? formatCount(data.keys.data.total) : "暂不可用"}</strong><small>Gateway 权威聚合</small></span></article>
              <article><CircleDollarSign aria-hidden size={22} /><span>累计 API 实际费用<strong>{data.usage.available ? formatUsdMicros(data.usage.data.totalActualCostUsdMicros) : "暂不可用"}</strong><small>{data.usage.available ? `今日 ${formatUsdMicros(data.usage.data.todayActualCostUsdMicros)}` : "来源暂不可用"}</small></span></article>
              <article><Activity aria-hidden size={22} /><span>总体健康状态<strong>{health.label}</strong><small>五个服务域的最差真实状态</small></span></article>
            </section>

            <section className="panel">
              <div className="panel-title"><div><h2>注意事项</h2><span>按复核、健康和来源状态排序</span></div>{attentionPath ? <Button color="primary" onClick={() => controller.navigate(attentionPath)} size="sm">进入处理</Button> : null}</div>
              {reconciliationCount && reconciliationCount > 0 ? <div className="inline-notice"><span>{formatCount(reconciliationCount)} 个项目等待计费复核。</span><Button onClick={() => controller.navigate("/admin/billing")} size="sm" variant="ghost">查看</Button></div> : null}
              {health.label !== "正常" ? <div className="inline-notice"><span>系统健康状态：{health.label}。</span><Button onClick={() => controller.navigate("/admin/system")} size="sm" variant="ghost">查看</Button></div> : null}
              {rows.some(({ source }) => source.status === "unavailable") ? <div className="inline-notice"><span>存在权威来源暂不可用，相关值未显示为零。</span></div> : null}
              {!attentionPath && !rows.some(({ source }) => source.status === "unavailable") ? <div className="empty-panel">当前没有待处理事项。</div> : null}
            </section>

            <section className="panel">
              <div className="panel-title"><h2>来源状态</h2></div>
              <SourceTable rows={rows} />
            </section>
          </>
        )}
      </SourceState>

      <section className="panel" data-slide="A-OV-02">
        <div className="panel-title"><div><h2>公告管理</h2></div><Button color="primary" onClick={() => setDraftOpen(true)} size="sm"><Plus aria-hidden size={16} />新建草稿</Button></div>
        <SourceState empty={announcements.length === 0} emptyTitle="暂无公告" error={controller.sources.operatorAnnouncements.error} loading={controller.sources.operatorAnnouncements.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.operatorAnnouncements.value} unavailableTitle="公告暂不可用">
          {() => <AnnouncementList announcements={announcements} busy={announcementBusy} controller={controller} onBusy={setAnnouncementBusy} />}
        </SourceState>
      </section>
      <AnnouncementDraftModal controller={controller} onClose={() => setDraftOpen(false)} open={draftOpen} />
    </section>
  );
}

type AccountDialog = "detail" | "provision" | "wallet" | "";

function AccountSourceSummary({ account }: { account: OperatorAccountDTO }) {
  const sources = [
    { label: "身份", source: account.gatewayIdentity },
    { label: "余额", source: account.wallet },
    { label: "Key", source: account.keyCount },
    { label: "API", source: account.usage },
    { label: "Workspace", source: account.workspaceCount }
  ];
  return <div className="account-source-summary">{sources.map(({ label, source }) => <div className="account-source-summary__item" key={label}><span>{label}</span><SourceBadge source={source} /><small>{source.source} · {formatDate(source.fetchedAt, true)}</small></div>)}</div>;
}

function AccountFact<T>({ source, children }: { source: SourceEnvelope<T>; children: (data: T) => ReactNode }) {
  if (!source.available) return <span className="account-fact-unavailable">暂不可用</span>;
  return children(source.data);
}

function AccountActions({ account, controller, openAccount }: {
  account: OperatorAccountDTO;
  controller: ConsoleController;
  openAccount: (account: OperatorAccountDTO, next: AccountDialog) => void;
}) {
  const reservedAdmin = account.accountId === "acct-admin" || account.role === "admin";
  return (
    <div className="operator-card-actions">
      <Button onClick={() => openAccount(account, "detail")} size="sm" variant="ghost">查看账户</Button>
      <Button onClick={() => openAccount(account, "wallet")} size="sm" variant="outline"><WalletCards aria-hidden size={15} />余额操作</Button>
      {reservedAdmin
        ? <span className="account-read-only">保留管理员账户仅查看</span>
        : account.status === "active"
          ? <Button color="danger" onClick={() => void controller.disableOperatorAccount(account.accountId)} size="sm" variant="ghost"><Ban aria-hidden size={15} />停用</Button>
          : <span className="account-read-only">账户已停用</span>}
    </div>
  );
}

function OperatorAccountMobileCard({ account, controller, openAccount }: {
  account: OperatorAccountDTO;
  controller: ConsoleController;
  openAccount: (account: OperatorAccountDTO, next: AccountDialog) => void;
}) {
  return (
    <article className="operator-object-card operator-account-mobile-card">
      <header className="operator-object-card__header">
        <span><strong>{account.email}</strong><small>{account.role === "admin" ? "管理员" : account.role}</small></span>
      </header>
      <dl className="operator-object-card__facts operator-account-mobile-facts">
        <div className="operator-object-card__wide"><dt>账户映射</dt><dd><span className="account-mapping-stack"><span><small>OPL Account</small><code>{account.accountId}</code></span><span><small>Console User</small><code>{account.consoleUserId}</code></span><span><small>Sub2API User</small><code>{account.sub2apiUserId}</code></span></span></dd></div>
        <div><dt>余额</dt><dd><AccountFact source={account.wallet}>{(wallet) => <span className="account-balance-stack"><strong>{formatUsdMicros(wallet.usdMicros)}</strong><small>{statusLabel(wallet.status)}</small></span>}</AccountFact></dd></div>
        <div><dt>API 费用</dt><dd><AccountFact source={account.usage}>{(usage) => <span className="account-cost-stack"><span><small>今日</small><strong>{formatUsdMicros(usage.todayActualCostUsdMicros)}</strong></span><span><small>累计</small><strong>{formatUsdMicros(usage.totalActualCostUsdMicros)}</strong></span></span>}</AccountFact></dd></div>
        <div><dt>资源</dt><dd><span className="account-resource-stack"><span><small>Key</small><strong><AccountFact source={account.keyCount}>{formatCount}</AccountFact></strong></span><span><small>Workspace</small><strong><AccountFact source={account.workspaceCount}>{formatCount}</AccountFact></strong></span></span></dd></div>
        <div><dt>状态</dt><dd><Badge color={statusTone(account.status)}>{statusLabel(account.status)}</Badge></dd></div>
      </dl>
      <AccountActions account={account} controller={controller} openAccount={openAccount} />
    </article>
  );
}

function ProvisionAccountModal({ controller, onClose, open }: { controller: ConsoleController; onClose: () => void; open: boolean }) {
  const [form, setForm] = useState({ email: "", password: "", name: "" });
  const [submittedEmail, setSubmittedEmail] = useState("");
  const [completed, setCompleted] = useState(false);
  const [account, setAccount] = useState<OperatorAccountDTO | null>(null);
  const updateForm = (field: keyof typeof form, value: string) => setForm((current) => ({ ...current, [field]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!form.email.trim() || !form.password) return;
    const email = form.email.trim();
    controller.setOperatorProvisionOperation(null);
    const operation = await controller.provisionAccount({ email, password: form.password, ...(form.name.trim() ? { name: form.name.trim() } : {}) });
    if (operation) {
      setSubmittedEmail(email);
      setAccount(operation.account);
      setCompleted(Boolean(operation.account));
      setForm((value) => ({ ...value, password: "" }));
    }
  };
  const close = () => {
    setForm({ email: "", password: "", name: "" });
    setSubmittedEmail("");
    setCompleted(false);
    setAccount(null);
    controller.setOperatorProvisionOperation(null);
    onClose();
  };
  return (
    <Modal
      className="modal"
      description="将创建 Account、Console User、Sub2API 身份及一对一映射；成功后以账户列表读回为准。"
      footer={completed ? <Button onClick={close} variant="outline">完成</Button> : <><Button disabled={controller.commandBusy} onClick={close} variant="outline">取消</Button><Button busy={controller.commandBusy} color="primary" form="provision-account-form" type="submit">开通用户</Button></>}
      onClose={close}
      open={open}
      title="开通用户"
    >
      {completed ? (
        <section className="wallet-adjustment-readback" data-slide="A-ACC-02">
          <div className="inline-notice"><span>账户映射已完成权威读回</span></div>
          <dl className="data-list">
            <div><dt>登录邮箱</dt><dd>{submittedEmail}</dd></div>
            <div><dt>Account ID</dt><dd>{account?.accountId || "暂不可用"}</dd></div>
            <div><dt>Console User ID</dt><dd>{account?.consoleUserId || "暂不可用"}</dd></div>
            <div><dt>Sub2API User ID</dt><dd>{account?.gatewayIdentity.available ? account.gatewayIdentity.data.userId : "暂不可用"}</dd></div>
            <div><dt>operation ID</dt><dd>{controller.operatorProvisionOperation?.operationId || "暂不可用"}</dd></div>
            <div><dt>状态</dt><dd>{controller.operatorProvisionOperation?.status || "暂不可用"}</dd></div>
            <div><dt>phase</dt><dd>{controller.operatorProvisionOperation?.phase || "暂不可用"}</dd></div>
            <div><dt>errorCode</dt><dd>{controller.operatorProvisionOperation?.errorCode || "暂不可用"}</dd></div>
          </dl>
        </section>
      ) : (
        <form id="provision-account-form" onSubmit={submit}>
          <Field autoComplete="email" autoFocus label="登录邮箱" onChange={(event) => updateForm("email", event.currentTarget.value)} required type="email" value={form.email} />
          <Field autoComplete="new-password" label="初始密码" onChange={(event) => updateForm("password", event.currentTarget.value)} required type="password" value={form.password} />
          <Field label="姓名" onChange={(event) => updateForm("name", event.currentTarget.value)} optional value={form.name} />
        </form>
      )}
    </Modal>
  );
}

function AccountDetailModal({ account, onClose }: { account: OperatorAccountDTO | null; onClose: () => void }) {
  return (
    <Modal className="modal" footer={<Button onClick={onClose} variant="outline">关闭</Button>} onClose={onClose} open={Boolean(account)} title="账户详情">
      {account ? (
        <div data-slide="A-ACC-03">
          <section className="data-section"><h2>Control Plane 映射</h2><dl className="data-list">
            <div><dt>Account</dt><dd>{account.accountId}</dd></div>
            <div><dt>Console User</dt><dd>{account.consoleUserId}</dd></div>
            <div><dt>Sub2API User 记录</dt><dd>{account.sub2apiUserId}</dd></div>
            <div><dt>登录邮箱</dt><dd>{account.email}</dd></div>
            <div><dt>角色</dt><dd>{account.role}</dd></div>
            <div><dt>状态</dt><dd><Badge color={statusTone(account.status)}>{statusLabel(account.status)}</Badge></dd></div>
          </dl></section>
          <section className="data-section"><h2>Gateway 与 Workspace 权威读回</h2><dl className="data-list">
            <div><dt>Sub2API 实时映射</dt><dd><SourceValue source={account.gatewayIdentity}>{(data) => `${data.userId} · ${data.email} · ${data.status}`}</SourceValue></dd></div>
            <div><dt>钱包余额</dt><dd><SourceValue source={account.wallet}>{(data) => `${formatUsdMicros(data.usdMicros)} · ${data.status}`}</SourceValue></dd></div>
            <div><dt>Key 汇总</dt><dd><SourceValue source={account.keyCount}>{formatCount}</SourceValue></dd></div>
            <div><dt>API Usage</dt><dd><SourceValue source={account.usage}>{(data) => `今日 ${formatUsdMicros(data.todayActualCostUsdMicros)} · 累计 ${formatUsdMicros(data.totalActualCostUsdMicros)}`}</SourceValue></dd></div>
            <div><dt>Workspace 汇总</dt><dd><SourceValue source={account.workspaceCount}>{formatCount}</SourceValue></dd></div>
            <div><dt>Support 工单映射</dt><dd>暂不可用</dd></div>
          </dl></section>
          <section className="data-section account-source-detail"><h2>来源状态与读回时间</h2><AccountSourceSummary account={account} /></section>
        </div>
      ) : null}
    </Modal>
  );
}

function WalletOperationReadback({ controller }: { controller: ConsoleController }) {
  const operation = controller.walletAdjustmentOperation;
  if (!operation) return null;
  const recoverable = operation.status === "manual_review" && operation.allowedActions?.includes("recover_wallet_adjustment");
  return (
    <section className="wallet-adjustment-readback">
      <div className="inline-notice"><span>操作结果：{statusLabel(operation.status)}</span><Button onClick={() => void controller.refreshWalletOperation()} size="sm" variant="ghost"><RefreshCw aria-hidden size={15} />刷新</Button></div>
      <dl className="data-list">
        <div><dt>operation ID</dt><dd>{operation.operationId}</dd></div>
        <div><dt>phase</dt><dd>{operation.phase || "暂不可用"}</dd></div>
        <div><dt>调整前余额</dt><dd><SourceValue source={operation.beforeBalance}>{(data) => formatUsdMicros(data.usdMicros)}</SourceValue></dd></div>
        <div><dt>调整后余额</dt><dd><SourceValue source={operation.afterBalance}>{(data) => formatUsdMicros(data.usdMicros)}</SourceValue></dd></div>
        <div><dt>原因</dt><dd>{operation.reason}</dd></div>
        <div><dt>关联操作</dt><dd>{operation.relatedOperationId || "暂不可用"}</dd></div>
        <div><dt>余额历史引用</dt><dd>{operation.balanceHistoryRef || "暂不可用"}</dd></div>
        <div><dt>Receipt ID</dt><dd>{operation.receiptId || "暂不可用"}</dd></div>
        <div><dt>actor</dt><dd>{operation.actor || "暂不可用"}</dd></div>
        <div><dt>errorCode</dt><dd>{operation.errorCode || "暂不可用"}</dd></div>
        <div><dt>上游 phase / HTTP</dt><dd>{operation.upstreamFailure ? `${operation.upstreamFailure.phase} / ${operation.upstreamFailure.httpStatus ?? "暂不可用"}` : "暂不可用"}</dd></div>
        <div><dt>上游 errorCode / requestId</dt><dd>{operation.upstreamFailure ? `${operation.upstreamFailure.errorCode} / ${operation.upstreamFailure.requestId || "暂不可用"}` : "暂不可用"}</dd></div>
        <div><dt>allowedActions</dt><dd>{operation.allowedActions?.length ? operation.allowedActions.join(", ") : "无"}</dd></div>
      </dl>
      {recoverable ? <div className="page-actions"><span>恢复时会要求 evidenceRef，并复用原 operation。</span><Button busy={controller.commandBusy} color="primary" onClick={() => void controller.recoverWalletOperation()}>恢复确认</Button></div> : null}
    </section>
  );
}

function WalletAdjustmentModal({ account, controller, onClose }: { account: OperatorAccountDTO | null; controller: ConsoleController; onClose: () => void }) {
  const [form, setForm] = useState<WalletAdjustmentRequest>({ kind: "recharge", amountUsd: "", reason: "", relatedOperationId: "", confirmationAccountId: "" });
  const open = Boolean(account);
  const updateForm = (field: keyof WalletAdjustmentRequest, value: string) => setForm((current) => ({ ...current, [field]: value }));
  const reset = () => {
    setForm({ kind: "recharge", amountUsd: "", reason: "", relatedOperationId: "", confirmationAccountId: "" });
    controller.setWalletAdjustmentOperation(null);
    onClose();
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!account) return;
    const input: WalletAdjustmentRequest = {
      kind: form.kind,
      amountUsd: form.amountUsd.trim(),
      reason: form.reason.trim(),
      confirmationAccountId: form.confirmationAccountId.trim(),
      ...(form.kind === "business_refund" && form.relatedOperationId?.trim() ? { relatedOperationId: form.relatedOperationId.trim() } : {})
    };
    void controller.submitWalletAdjustment(account.accountId, input);
  };
  const valid = Boolean(account && form.confirmationAccountId.trim() === account.accountId && form.amountUsd.trim() && form.reason.trim() && (form.kind !== "business_refund" || form.relatedOperationId?.trim()));
  return (
    <Modal
      className="modal wallet-adjustment-modal"
      description={account ? `目标 Account ID：${account.accountId}` : undefined}
      footer={<><Button disabled={controller.commandBusy} onClick={reset} variant="outline">关闭</Button>{controller.walletAdjustmentOperation ? null : <Button busy={controller.commandBusy} color="primary" disabled={!valid} form="wallet-adjustment-form" type="submit">确认操作</Button>}</>}
      onClose={reset}
      open={open}
      title="余额操作"
    >
      {account ? (
        <div data-slide="A-ACC-03">
          <form id="wallet-adjustment-form" onSubmit={submit}>
            <Field autoFocus description="必须与目标 Account ID 完全一致。" label="再次确认 Account ID" onChange={(event) => updateForm("confirmationAccountId", event.currentTarget.value)} required value={form.confirmationAccountId} />
            <Select label="操作类型" onChange={(kind) => setForm((value) => ({ ...value, kind: kind as WalletAdjustmentRequest["kind"] }))} options={[{ value: "recharge", label: "充值" }, { value: "debit", label: "扣减" }, { value: "business_refund", label: "业务退款" }]} value={form.kind} />
            <Field inputMode="decimal" label="金额（USD）" min="0.000001" onChange={(event) => updateForm("amountUsd", event.currentTarget.value)} required step="0.000001" type="number" value={form.amountUsd} />
            <Field label="业务原因" maxLength={200} multiline onChange={(event) => updateForm("reason", event.currentTarget.value)} required rows={4} value={form.reason} />
            {form.kind === "business_refund" ? <Field label="关联 operation ID" onChange={(event) => updateForm("relatedOperationId", event.currentTarget.value)} required value={form.relatedOperationId || ""} /> : null}
          </form>
          <WalletOperationReadback controller={controller} />
        </div>
      ) : null}
    </Modal>
  );
}

function AccountsPage({ controller }: { controller: ConsoleController }) {
  const [dialog, setDialog] = useState<AccountDialog>("");
  const [selectedAccountId, setSelectedAccountId] = useState("");
  const accounts = sourceData(controller.sources.operatorAccounts.value)?.items || [];
  const selectedAccount = accounts.find((account) => account.accountId === selectedAccountId) || null;
  const openAccount = (account: OperatorAccountDTO, next: AccountDialog) => {
    controller.setWalletAdjustmentOperation(null);
    setSelectedAccountId(account.accountId);
    setDialog(next);
  };
  return (
    <section className="panel" data-slide="A-ACC-01 A-ACC-02 A-ACC-03">
      <div className="panel-title operator-accounts-panel-title"><div className="operator-accounts-title"><h2>客户与计费账户</h2></div><Button color="primary" onClick={() => setDialog("provision")}><Plus aria-hidden size={16} />开通用户</Button></div>
      <SourceState empty={accounts.length === 0} emptyTitle="暂无用户" error={controller.sources.operatorAccounts.error} loading={controller.sources.operatorAccounts.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.operatorAccounts.value} unavailableTitle="账户数据暂不可用">
        {(data) => <>
          <div className="table-wrap operator-account-table"><table><thead><tr><th>用户</th><th>账户映射</th><th>余额</th><th>API 费用</th><th>资源</th><th>状态</th><th>操作</th></tr></thead><tbody>{data.items.map((account) => (
            <tr key={account.accountId}>
              <td>
                <span className="operator-account-identity">
                  <strong>{account.email}</strong>
                  <small>{account.role === "admin" ? "管理员" : account.role}</small>
                </span>
              </td>
              <td><span className="account-mapping-stack"><span><small>OPL Account</small><code>{account.accountId}</code></span><span><small>Console User</small><code>{account.consoleUserId}</code></span><span><small>Sub2API User</small><code>{account.sub2apiUserId}</code></span></span></td>
              <td><AccountFact source={account.wallet}>{(wallet) => <span className="account-balance-stack"><strong>{formatUsdMicros(wallet.usdMicros)}</strong><small>{statusLabel(wallet.status)}</small></span>}</AccountFact></td>
              <td><AccountFact source={account.usage}>{(usage) => <span className="account-cost-stack"><span><small>今日</small><strong>{formatUsdMicros(usage.todayActualCostUsdMicros)}</strong></span><span><small>累计</small><strong>{formatUsdMicros(usage.totalActualCostUsdMicros)}</strong></span></span>}</AccountFact></td>
              <td><span className="account-resource-stack"><span><small>Key</small><strong><AccountFact source={account.keyCount}>{formatCount}</AccountFact></strong></span><span><small>Workspace</small><strong><AccountFact source={account.workspaceCount}>{formatCount}</AccountFact></strong></span></span></td>
              <td><Badge color={statusTone(account.status)}>{statusLabel(account.status)}</Badge></td>
              <td className="table-actions"><AccountActions account={account} controller={controller} openAccount={openAccount} /></td>
            </tr>
          ))}</tbody></table></div>
          <div className="operator-account-mobile-list">{data.items.map((account) => <OperatorAccountMobileCard account={account} controller={controller} key={account.accountId} openAccount={openAccount} />)}</div>
        </>}
      </SourceState>
      <Pagination current={controller.operatorAccountPage} label="账号分页" onChange={(page) => void controller.changeOperatorAccountPage(page)} pages={controller.operatorAccountPages} />
      <ProvisionAccountModal controller={controller} onClose={() => setDialog("")} open={dialog === "provision"} />
      <AccountDetailModal account={dialog === "detail" ? selectedAccount : null} onClose={() => setDialog("")} />
      <WalletAdjustmentModal account={dialog === "wallet" ? selectedAccount : null} controller={controller} onClose={() => setDialog("")} />
    </section>
  );
}

function ReviewDetails({ review }: { review: OperatorReconciliationItemDTO }) {
  return (
    <div>
      <section className="data-section"><h2>Control Plane</h2><dl className="data-list">
        <div><dt>Account</dt><dd>{review.accountId || "暂不可用"}</dd></div>
        <div><dt>Workspace</dt><dd>暂不可用</dd></div>
        <div><dt>billing operation</dt><dd>{review.billingOperationId || "暂不可用"}</dd></div>
        <div><dt>报价 / 扣款意图</dt><dd>暂不可用</dd></div>
        <div><dt>phase</dt><dd>{review.phase || "暂不可用"}</dd></div>
        <div><dt>errorCode</dt><dd>{review.errorCode || "暂不可用"}</dd></div>
        <div><dt>operation reference</dt><dd>{review.operationRef || "暂不可用"}</dd></div>
      </dl></section>
      <section className="data-section"><h2>Gateway</h2><dl className="data-list"><div><dt>权威余额</dt><dd>暂不可用</dd></div><div><dt>余额历史证据</dt><dd>暂不可用</dd></div></dl></section>
      <section className="data-section"><h2>Fabric</h2><dl className="data-list"><div><dt>Compute / Storage / Attachment</dt><dd>暂不可用</dd></div><div><dt>provider ID / Zone / 最近读回</dt><dd>暂不可用</dd></div></dl></section>
      <section className="data-section"><h2>Ledger</h2><dl className="data-list"><div><dt>Receipt reference</dt><dd>{review.receiptRef || "暂不可用"}</dd></div><div><dt>reconciliation exception</dt><dd>{review.status || "暂不可用"}</dd></div></dl></section>
    </div>
  );
}

function ReviewModal({ onClose, review }: { onClose: () => void; review: OperatorReconciliationItemDTO | null }) {
  return (
    <Modal
      className="modal"
      description="服务端复核队列项目与证据，操作由 Control Plane 权威决定。"
      footer={<Button onClick={onClose} variant="outline">关闭</Button>}
      onClose={onClose}
      open={Boolean(review)}
      title="复核详情"
    >
      {review ? <div data-slide="A-REC-02"><ReviewDetails review={review} /><section className="data-section"><h2>服务端允许动作</h2><dl className="data-list"><div><dt>allowedActions</dt><dd>{review.allowedActions.length ? review.allowedActions.join(", ") : "无自动修复动作"}</dd></div></dl></section><div className="inline-notice"><ShieldAlert aria-hidden size={17} /><span>该项目只展示阻断和证据，不提供自动修复。</span></div></div> : null}
    </Modal>
  );
}

function ReconciliationPage({ controller }: { controller: ConsoleController }) {
  const [selectedReview, setSelectedReview] = useState<OperatorReconciliationItemDTO | null>(null);
  const reviews = sourceData(controller.sources.operatorReconciliation.value)?.items || [];
  return (
    <section className="panel" data-slide="A-REC-01 A-REC-02">
      <div className="panel-title"><div><h2>计费复核</h2></div><span>服务端队列</span></div>
      <SourceState empty={reviews.length === 0} emptyTitle="暂无待复核项目" error={controller.sources.operatorReconciliation.error} loading={controller.sources.operatorReconciliation.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.operatorReconciliation.value} unavailableTitle="复核数据暂不可用">
        {(data) => <div className="table-wrap"><table><thead><tr><th>Account</th><th>资源类型</th><th>状态</th><th>billing operation</th><th>phase</th><th>errorCode</th><th>operation reference</th><th>Receipt reference</th><th>allowedActions</th><th>操作</th></tr></thead><tbody>{data.items.map((review) => {
          return <tr key={review.id}><td>{review.accountId || "暂不可用"}</td><td>{review.resourceType}</td><td><Badge color={statusTone(review.status)}>{statusLabel(review.status)}</Badge></td><td>{review.billingOperationId || "暂不可用"}</td><td>{review.phase || "暂不可用"}</td><td>{review.errorCode || "暂不可用"}</td><td>{review.operationRef || "暂不可用"}</td><td>{review.receiptRef || "暂不可用"}</td><td>{review.allowedActions.length ? review.allowedActions.join(", ") : "无"}</td><td><Button onClick={() => setSelectedReview(review)} size="sm" variant="ghost">查看证据</Button></td></tr>;
        })}</tbody></table></div>}
      </SourceState>
      <ReviewModal onClose={() => setSelectedReview(null)} review={selectedReview} />
    </section>
  );
}

function ResourceRow({ resource }: { resource: OperatorResourceDTO }) {
  return (
    <tr>
      <td><SourceValue source={resource.ownerAccount}>{(data) => data.id}</SourceValue></td>
      <td><SourceValue source={resource.ownerUser}>{(data) => `${data.email} · ${data.id}`}</SourceValue></td>
      <td><SourceValue source={resource.workspace}>{(data) => data.name || data.id}</SourceValue></td>
      <td><SourceValue source={resource.resourceType}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.packageOrSpec}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.providerId}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.zone}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.status}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.createdAt}>{(data) => formatDate(data, true)}</SourceValue></td>
      <td><SourceValue source={resource.expiresAt}>{(data) => formatDate(data, true)}</SourceValue></td>
      <td><SourceValue source={resource.lastReadAt}>{(data) => formatDate(data, true)}</SourceValue></td>
      <td><SourceValue source={resource.operationRef}>{(data) => data}</SourceValue></td>
      <td><SourceValue source={resource.receiptRef}>{(data) => data}</SourceValue></td>
    </tr>
  );
}

function OperatorResourceMobileCard({ resource }: { resource: OperatorResourceDTO }) {
  const workspace = sourceData(resource.workspace);
  return (
    <article className="operator-object-card operator-resource-mobile-card">
      <header className="operator-object-card__header">
        <span><strong>{workspace?.name || workspace?.id || "Workspace 暂不可用"}</strong><small>{workspace?.id || "身份暂不可用"}</small></span>
        <SourceValue source={resource.status}>{(value) => statusLabel(value)}</SourceValue>
      </header>
      <dl className="operator-object-card__facts">
        <div><dt>资源类型</dt><dd><SourceValue source={resource.resourceType}>{(data) => data}</SourceValue></dd></div>
        <div><dt>套餐 / 规格</dt><dd><SourceValue source={resource.packageOrSpec}>{(data) => data}</SourceValue></dd></div>
        <div className="operator-object-card__wide"><dt>provider ID</dt><dd><SourceValue source={resource.providerId}>{(data) => data}</SourceValue></dd></div>
        <div><dt>owner Account</dt><dd><SourceValue source={resource.ownerAccount}>{(data) => data.id}</SourceValue></dd></div>
        <div><dt>owner User</dt><dd><SourceValue source={resource.ownerUser}>{(data) => data.email}</SourceValue></dd></div>
        <div><dt>Zone</dt><dd><SourceValue source={resource.zone}>{(data) => data}</SourceValue></dd></div>
        <div><dt>创建时间</dt><dd><SourceValue source={resource.createdAt}>{(data) => formatDate(data, true)}</SourceValue></dd></div>
        <div><dt>到期时间</dt><dd><SourceValue source={resource.expiresAt}>{(data) => formatDate(data, true)}</SourceValue></dd></div>
        <div><dt>最近 provider 读回</dt><dd><SourceValue source={resource.lastReadAt}>{(data) => formatDate(data, true)}</SourceValue></dd></div>
        <div><dt>operation reference</dt><dd><SourceValue source={resource.operationRef}>{(data) => data}</SourceValue></dd></div>
        <div className="operator-object-card__wide"><dt>Receipt reference</dt><dd><SourceValue source={resource.receiptRef}>{(data) => data}</SourceValue></dd></div>
      </dl>
    </article>
  );
}

function ResourceDetail({ controller }: { controller: ConsoleController }) {
  const selected = controller.selectedOperatorWorkspaceId;
  if (!selected) return <section className="panel" data-slide="A-RES-02"><div className="panel-title"><div><h2>资源详情</h2></div></div><div className="empty-panel">请选择 Workspace 查看资源详情。</div></section>;
  return (
    <section className="panel" data-slide="A-RES-02">
      <div className="panel-title"><div><h2>资源详情</h2></div><span>{selected}</span></div>
      <SourceState error={controller.sources.operatorWorkspaceDetail.error} loading={controller.sources.operatorWorkspaceDetail.loading} onRetry={() => void controller.openOperatorWorkspace(selected)} source={controller.sources.operatorWorkspaceDetail.value} unavailableTitle="资源详情暂不可用">
        {(detail) => <>
          <dl className="data-list">
            <div><dt>owner Account</dt><dd><SourceValue source={detail.ownerAccount}>{(data) => data.id}</SourceValue></dd></div>
            <div><dt>owner User</dt><dd><SourceValue source={detail.ownerUser}>{(data) => `${data.email} · ${data.id}`}</SourceValue></dd></div>
            <div><dt>Workspace</dt><dd><SourceValue source={detail.workspace}>{(data) => `${data.name || data.id} · ${data.id}`}</SourceValue></dd></div>
            <div><dt>Ledger Receipt</dt><dd><SourceValue source={detail.receipt}>{(data) => data.receiptId}</SourceValue></dd></div>
            <div><dt>Workspace Key 累计实际费用</dt><dd><SourceValue source={detail.workspaceKeyUsage}>{(data) => `${formatUsdMicros(data.totalActualCostUsdMicros)} · ${data.keyId}`}</SourceValue></dd></div>
          </dl>
          {detail.resources.length ? <>
            <div className="table-wrap operator-resource-detail-table"><table><thead><tr><th>owner Account</th><th>owner User</th><th>Workspace</th><th>资源类型</th><th>套餐 / 规格</th><th>provider ID</th><th>Zone</th><th>实时状态</th><th>创建时间</th><th>到期时间</th><th>最近 provider 读回</th><th>operation reference</th><th>Receipt reference</th></tr></thead><tbody>{detail.resources.map((resource, index) => <ResourceRow key={`${index}-${resource.providerId.source}-${resource.operationRef.source}`} resource={resource} />)}</tbody></table></div>
            <div className="operator-resource-mobile-list">{detail.resources.map((resource, index) => <OperatorResourceMobileCard key={`${index}-${resource.providerId.source}-${resource.operationRef.source}`} resource={resource} />)}</div>
          </> : <div className="empty-panel">暂无资源</div>}
        </>}
      </SourceState>
    </section>
  );
}

function OperatorWorkspaceMobileCard({ controller, item }: { controller: ConsoleController; item: OperatorWorkspaceDTO }) {
  const workspace = sourceData(item.workspace);
  const id = workspace?.id || "";
  return (
    <article className="operator-object-card operator-workspace-mobile-card">
      <header className="operator-object-card__header">
        <span><strong>{workspace?.name || id || "Workspace 暂不可用"}</strong><small>{id || "身份暂不可用"}</small></span>
        <SourceValue source={item.workspace}>{(value) => statusLabel(value.state)}</SourceValue>
      </header>
      <dl className="operator-object-card__facts">
        <div><dt>owner Account</dt><dd><SourceValue source={item.ownerAccount}>{(value) => value.id}</SourceValue></dd></div>
        <div><dt>owner User</dt><dd><SourceValue source={item.ownerUser}>{(value) => value.email}</SourceValue></dd></div>
        <div><dt>套餐 / 月度总价</dt><dd><SourceValue source={item.workspace}>{(value) => `${value.packageId?.toUpperCase() || "暂不可用"} · ${value.totalUsdMicros === undefined ? "暂不可用" : formatUsdMicros(value.totalUsdMicros)}`}</SourceValue></dd></div>
        <div><dt>创建时间</dt><dd><SourceValue source={item.workspace}>{(value) => formatDate(value.createdAt, true)}</SourceValue></dd></div>
        <div><dt>权益截止</dt><dd><SourceValue source={item.workspace}>{(value) => value.paidThrough ? formatDate(value.paidThrough) : "暂不可用"}</SourceValue></dd></div>
        <div><dt>续费状态</dt><dd><SourceValue source={item.workspace}>{(value) => value.renewalStatus || "暂不可用"}</SourceValue></dd></div>
        <div><dt>生命周期状态</dt><dd><SourceValue source={item.workspace}>{(value) => value.state || "暂不可用"}</SourceValue></dd></div>
        <div><dt>Receipt ID</dt><dd><SourceValue source={item.receipt}>{(value) => value.receiptId}</SourceValue></dd></div>
        <div className="operator-object-card__wide"><dt>Key 累计实际费用</dt><dd><SourceValue source={item.workspaceKeyUsage}>{(value) => formatUsdMicros(value.totalActualCostUsdMicros)}</SourceValue></dd></div>
      </dl>
      <div className="operator-card-actions">
        {workspace?.url ? <a className="operator-object-link" href={workspace.url} rel="noreferrer" target="_blank">打开 Workspace<ExternalLink aria-hidden size={14} /></a> : <span className="account-read-only">URL 暂不可用</span>}
        <Button disabled={!id} onClick={() => id && void controller.openOperatorWorkspace(id)} size="sm" variant="outline">查看资源</Button>
      </div>
    </article>
  );
}

function ResourcesPage({ controller }: { controller: ConsoleController }) {
  const workspaces = sourceData(controller.sources.operatorWorkspaces.value)?.items || [];
  return (
    <section className="admin-dashboard" data-slide="A-RES-01 A-RES-02">
      <section className="panel">
        <div className="panel-title"><div><h2>Workspace 资源列表</h2></div><span>当前页资源状态</span></div>
        <SourceState empty={workspaces.length === 0} emptyTitle="暂无 Workspace" error={controller.sources.operatorWorkspaces.error} loading={controller.sources.operatorWorkspaces.loading} onRetry={() => void controller.refreshCurrentPage()} source={controller.sources.operatorWorkspaces.value} unavailableTitle="Workspace 资源暂不可用">
          {(data) => <>
            <div className="table-wrap operator-workspace-table"><table><thead><tr><th>Workspace</th><th>owner Account</th><th>owner User</th><th>套餐 / 月度总价</th><th>创建时间</th><th>paidThrough</th><th>续费状态</th><th>生命周期状态</th><th>URL</th><th>Receipt ID</th><th>Key 累计实际费用</th><th>操作</th></tr></thead><tbody>{data.items.map((item, index) => {
              const workspace = sourceData(item.workspace);
              const id = workspace?.id || "";
              return <tr key={id || index}><td><SourceValue source={item.workspace}>{(value) => `${value.name || value.id} · ${value.id}`}</SourceValue></td><td><SourceValue source={item.ownerAccount}>{(value) => value.id}</SourceValue></td><td><SourceValue source={item.ownerUser}>{(value) => value.email}</SourceValue></td><td><SourceValue source={item.workspace}>{(value) => `${value.packageId?.toUpperCase() || "暂不可用"} · ${value.totalUsdMicros === undefined ? "暂不可用" : formatUsdMicros(value.totalUsdMicros)}`}</SourceValue></td><td><SourceValue source={item.workspace}>{(value) => formatDate(value.createdAt, true)}</SourceValue></td><td><SourceValue source={item.workspace}>{(value) => value.paidThrough ? formatDate(value.paidThrough) : "暂不可用"}</SourceValue></td><td><SourceValue source={item.workspace}>{(value) => value.renewalStatus || "暂不可用"}</SourceValue></td><td><SourceValue source={item.workspace}>{(value) => value.state || "暂不可用"}</SourceValue></td><td>{workspace?.url ? <a href={workspace.url} rel="noreferrer" target="_blank">打开<ExternalLink aria-hidden size={14} /></a> : "暂不可用"}</td><td><SourceValue source={item.receipt}>{(value) => value.receiptId}</SourceValue></td><td><SourceValue source={item.workspaceKeyUsage}>{(value) => formatUsdMicros(value.totalActualCostUsdMicros)}</SourceValue></td><td><Button disabled={!id} onClick={() => id && void controller.openOperatorWorkspace(id)} size="sm" variant="outline">查看资源</Button></td></tr>;
            })}</tbody></table></div>
            <div className="operator-workspace-mobile-list">{data.items.map((item, index) => <OperatorWorkspaceMobileCard controller={controller} item={item} key={sourceData(item.workspace)?.id || index} />)}</div>
          </>}
        </SourceState>
        <Pagination current={controller.operatorWorkspacePage} label="Workspace 分页" onChange={(page) => void controller.changeOperatorWorkspacePage(page)} pages={controller.operatorWorkspacePages} />
      </section>
      <ResourceDetail controller={controller} />
    </section>
  );
}

const healthServices = [
  { key: "controlPlane", name: "Control Plane", icon: Activity },
  { key: "gateway", name: "API 服务 / Sub2API Gateway", icon: Server },
  { key: "fabric", name: "Fabric 资源服务", icon: Database },
  { key: "runtime", name: "Workspace Runtime 服务", icon: Activity },
  { key: "ledger", name: "Ledger 账单记录", icon: CircleDollarSign }
] as const;

function OperatorHealthMobileCard({ controller, name, service, icon: Icon }: {
  controller: ConsoleController;
  name: string;
  service: SourceEnvelope<ReadinessFact>;
  icon: typeof Activity;
}) {
  const state = healthStatus(service);
  return (
    <article className="operator-object-card operator-health-mobile-card">
      <header className="operator-object-card__header">
        <span className="resource-type"><Icon aria-hidden size={16} /><strong>{name}</strong></span>
        <Badge color={state.tone}>{state.label}</Badge>
      </header>
      <dl className="operator-object-card__facts">
        <div><dt>readiness 生成时间</dt><dd>{service.available ? formatDate(service.data.generatedAt || service.data.updatedAt, true) : "暂不可用"}</dd></div>
        <div><dt>Console 读回时间</dt><dd>{service.fetchedAt ? formatDate(service.fetchedAt, true) : "暂不可用"}</dd></div>
        <div className="operator-object-card__wide"><dt>客户影响范围</dt><dd>暂不可用</dd></div>
      </dl>
      <div className="operator-card-actions"><Button aria-label={`刷新 ${name}`} onClick={() => void controller.refreshCurrentPage()} size="sm" variant="outline"><RefreshCw aria-hidden size={15} />刷新</Button></div>
    </article>
  );
}

function SystemPage({ controller }: { controller: ConsoleController }) {
  const healthSource = controller.sources.operatorHealth.value;
  const summary = overallHealth(healthSource);
  return (
    <section className="admin-dashboard" data-slide="A-SYS-01">
      <section className="account-band">
        <div className="account-band-copy"><h2>系统状态</h2></div>
        <div className="band-metrics"><Metric label="总体状态" note="按最差真实状态" value={summary.label} /><Metric label="服务域" note="固定展示" value="5" /><Metric label="Console 读回" note="最近读取" value={healthSource?.fetchedAt ? formatDate(healthSource.fetchedAt, true) : "暂不可用"} /></div>
      </section>
      <section className="panel">
        <div className="panel-title"><div><h2>服务健康</h2><Badge color={summary.tone}>{summary.label}</Badge></div><Button onClick={() => void controller.refreshCurrentPage()} size="sm" variant="outline"><RefreshCw aria-hidden size={16} />刷新</Button></div>
        <SourceState error={controller.sources.operatorHealth.error} loading={controller.sources.operatorHealth.loading} onRetry={() => void controller.refreshCurrentPage()} source={healthSource} unavailableTitle="系统状态暂不可用">
          {(health) => <>
            <div className="table-wrap operator-health-table"><table><thead><tr><th>服务</th><th>状态</th><th>readiness 生成时间</th><th>Console 读回时间</th><th>客户影响范围</th><th>操作</th></tr></thead><tbody>{healthServices.map(({ key, name, icon: Icon }) => {
              const service = health[key];
              const state = healthStatus(service);
              return <tr key={key}><td><span className="resource-type"><Icon aria-hidden size={16} />{name}</span></td><td><Badge color={state.tone}>{state.label}</Badge></td><td>{service.available ? formatDate(service.data.generatedAt || service.data.updatedAt, true) : "暂不可用"}</td><td>{service.fetchedAt ? formatDate(service.fetchedAt, true) : "暂不可用"}</td><td>暂不可用</td><td><Button aria-label={`刷新 ${name}`} onClick={() => void controller.refreshCurrentPage()} size="sm" uniform variant="ghost"><RefreshCw aria-hidden size={15} /></Button></td></tr>;
            })}</tbody></table></div>
            <div className="operator-health-mobile-list">{healthServices.map(({ key, name, icon }) => <OperatorHealthMobileCard controller={controller} icon={icon} key={key} name={name} service={health[key]} />)}</div>
          </>}
        </SourceState>
      </section>
    </section>
  );
}

export function AdminPages({ controller }: { controller: ConsoleController }) {
  if (controller.path === "/admin/accounts") return <AccountsPage controller={controller} />;
  if (controller.path === "/admin/billing") return <ReconciliationPage controller={controller} />;
  if (controller.path === "/admin/resources") return <ResourcesPage controller={controller} />;
  if (controller.path === "/admin/system") return <SystemPage controller={controller} />;
  return <OverviewPage controller={controller} />;
}
