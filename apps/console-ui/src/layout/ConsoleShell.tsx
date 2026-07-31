import {
  Activity,
  ChevronDown,
  CircleHelp,
  CircleDollarSign,
  Database,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Megaphone,
  Menu,
  ReceiptText,
  RefreshCw,
  Server,
  Settings,
  UserRound,
  UsersRound,
  X
} from "lucide-react";
import { useState, type ComponentType, type FormEvent, type ReactNode } from "react";

import type { ConsoleController } from "../app/use-console-controller.ts";
import { adminMenu, apiMenu, customerMenu } from "../console-model.ts";
import { Button, Field, Tooltip } from "../components/ui/index.ts";

const icons: Record<string, ComponentType<{ size?: number; "aria-hidden"?: boolean }>> = {
  Activity,
  CircleDollarSign,
  Database,
  LayoutDashboard,
  Megaphone,
  ReceiptText,
  Server,
  UsersRound
};

function isActive(path: string, target: string) {
  if (target === "/console/api") return path === target;
  if (target === "/console/workspaces") return path === target || /^\/console\/workspaces\/[^/]+$/.test(path);
  return path === target;
}

function NavigationLink({ controller, item }: { controller: ConsoleController; item: (typeof customerMenu)[number] | (typeof adminMenu)[number] }) {
  const Icon = icons[item.icon] || LayoutDashboard;
  return (
    <a
      aria-current={isActive(controller.path, item.path) ? "page" : undefined}
      className={isActive(controller.path, item.path) ? "active" : ""}
      href={item.path}
      onClick={(event) => {
        event.preventDefault();
        controller.navigate(item.path);
      }}
    >
      <Icon aria-hidden size={18} />
      <span>{item.label}</span>
    </a>
  );
}

function SupportSlide({ controller }: { controller: ConsoleController }) {
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ externalSystem: "", externalTicketId: "", externalUrl: "", title: "", description: "", workspaceId: "", resourceIds: "", operationId: "" });
  const tickets = controller.supportTickets?.tickets || [];
  const updateForm = (field: keyof typeof form, value: string) => setForm((current) => ({ ...current, [field]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const ok = await controller.createSupportMapping({
      externalTicketId: form.externalTicketId.trim(),
      title: form.title.trim(),
      ...(form.externalSystem.trim() ? { externalSystem: form.externalSystem.trim() } : {}),
      ...(form.externalUrl.trim() ? { externalUrl: form.externalUrl.trim() } : {}),
      ...(form.description.trim() ? { description: form.description.trim() } : {}),
      ...(form.workspaceId.trim() ? { workspaceId: form.workspaceId.trim() } : {}),
      ...(form.operationId.trim() ? { operationId: form.operationId.trim() } : {}),
      ...(form.resourceIds.trim() ? { resourceIds: form.resourceIds.split(/[\n,]+/).map((value) => value.trim()).filter(Boolean) } : {})
    });
    if (ok) {
      setForm({ externalSystem: "", externalTicketId: "", externalUrl: "", title: "", description: "", workspaceId: "", resourceIds: "", operationId: "" });
      setAdding(false);
    }
  };

  return (
    <div className="support-slide-content">
      <div className="support-slide-toolbar">
        <p>这里只映射已存在的外部工单，不会在 Console 内创建工单。</p>
        <Button onClick={() => setAdding((value) => !value)} size="sm" variant="outline">{adding ? "取消" : "新增映射"}</Button>
      </div>
      {adding ? (
        <form className="support-mapping-form" onSubmit={submit}>
          <Field label="外部工单号" onChange={(event) => updateForm("externalTicketId", event.currentTarget.value)} required value={form.externalTicketId} />
          <Field label="标题" onChange={(event) => updateForm("title", event.currentTarget.value)} required value={form.title} />
          <Field label="外部工单系统" onChange={(event) => updateForm("externalSystem", event.currentTarget.value)} placeholder="例如 Zammad" value={form.externalSystem} />
          <Field label="外部工单链接" onChange={(event) => updateForm("externalUrl", event.currentTarget.value)} type="url" value={form.externalUrl} />
          <Field label="问题说明" multiline onChange={(event) => updateForm("description", event.currentTarget.value)} rows={3} value={form.description} />
          <Field label="Workspace ID" onChange={(event) => updateForm("workspaceId", event.currentTarget.value)} value={form.workspaceId} />
          <Field label="资源 ID" multiline onChange={(event) => updateForm("resourceIds", event.currentTarget.value)} placeholder="多个 ID 用逗号或换行分隔" rows={2} value={form.resourceIds} />
          <Field label="operation ID" onChange={(event) => updateForm("operationId", event.currentTarget.value)} value={form.operationId} />
          <Button busy={controller.commandBusy} color="primary" disabled={!form.externalTicketId.trim() || !form.title.trim()} type="submit">保存外部映射</Button>
        </form>
      ) : null}
      {controller.supportLoading ? <div className="source-loading"><span className="spinner" />正在读取外部工单映射</div> : null}
      {controller.supportError ? <div className="inline-error"><span>{controller.supportError}</span><Button onClick={() => void controller.loadSupportTickets()} size="sm" variant="ghost">重试</Button></div> : null}
      {!controller.supportLoading && !controller.supportError && tickets.length === 0 ? <div className="empty-copy"><p>暂无外部工单映射。</p></div> : null}
      {tickets.length ? <div className="support-ticket-list">{tickets.map((ticket) => (
        <article className="support-ticket" key={ticket.id}>
          <header><div><strong>{ticket.title}</strong><span>{ticket.externalSystem} · {ticket.externalTicketId}</span></div><span>{ticket.status}</span></header>
          <dl className="data-list">
            <div><dt>外部工单链接</dt><dd>{ticket.externalUrl ? <a href={ticket.externalUrl} rel="noreferrer" target="_blank">{ticket.externalUrl}</a> : "暂不可用"}</dd></div>
            <div><dt>分类 / 优先级</dt><dd>{ticket.category} / {ticket.priority}</dd></div>
            <div><dt>Workspace / operation</dt><dd>{ticket.workspaceId || "暂不可用"} / {ticket.operationId || "暂不可用"}</dd></div>
            <div><dt>资源</dt><dd>{ticket.resourceIds.length ? ticket.resourceIds.join(", ") : "暂不可用"}</dd></div>
            <div><dt>创建 / 更新</dt><dd>{ticket.createdAt} / {ticket.updatedAt}</dd></div>
            <div><dt>已有消息</dt><dd>{ticket.messages.length ? ticket.messages.map((message) => `${message.author}: ${message.text}`).join("；") : "暂无消息"}</dd></div>
          </dl>
        </article>
      ))}</div> : null}
    </div>
  );
}

export function ConsoleShell({ children, controller }: { children: ReactNode; controller: ConsoleController }) {
  const adminSurface = controller.isAdminRoute;
  const mobileItems = adminSurface ? adminMenu : customerMenu;

  return (
    <div className="app-shell" data-visual-direction="quiet-ledger">
      {controller.sidebarOpen ? <button aria-label="关闭导航" className="sidebar-scrim" onClick={() => controller.setSidebarOpen(false)} /> : null}
      <aside className={`sidebar ${controller.sidebarOpen ? "open" : ""}`} aria-label="产品导航">
        <div className="sidebar-head">
          <a className="brand" href="/console/overview" onClick={(event) => { event.preventDefault(); controller.navigate("/console/overview"); }}>
            <img alt="OPL Cloud" src="/opl-app-icon.png" />
            <strong>OPL Cloud</strong>
          </a>
          <Tooltip content="关闭导航">
            <button aria-label="关闭导航" className="sidebar-close" onClick={() => controller.setSidebarOpen(false)}><X aria-hidden size={18} /></button>
          </Tooltip>
        </div>

        <nav className="side-nav">
          {customerMenu.map((item) => <NavigationLink controller={controller} item={item} key={item.path} />)}
          {controller.path.startsWith("/console/api") ? (
            <div className="side-subnav" aria-label="API 服务页面">
              {apiMenu.map((item) => (
                <a
                  aria-current={controller.path === item.path ? "page" : undefined}
                  className={controller.path === item.path ? "active" : ""}
                  href={item.path}
                  key={item.path}
                  onClick={(event) => { event.preventDefault(); controller.navigate(item.path); }}
                >
                  {item.path.endsWith("/keys") ? <KeyRound aria-hidden size={14} /> : null}
                  <span>{item.label}</span>
                </a>
              ))}
            </div>
          ) : null}

          {controller.session?.isOperator ? (
            <div className="operator-nav">
              <div className="nav-section-label">Admin</div>
              {adminMenu.map((item) => <NavigationLink controller={controller} item={item} key={item.path} />)}
            </div>
          ) : null}
        </nav>

        <div className="sidebar-account">
          <UserRound aria-hidden size={18} />
          <span>
            <strong>{controller.session?.user.email || "-"}</strong>
            <small>{controller.session?.isOperator ? "管理员" : "客户"}</small>
          </span>
          <Tooltip content="Account Settings 与 Support">
            <button aria-label="打开账号菜单" onClick={() => controller.setGlobalSlide(controller.globalSlide ? "" : "account")}>
              <ChevronDown aria-hidden size={17} />
            </button>
          </Tooltip>
        </div>
      </aside>

      <div className="main-column">
        <header className={`topbar ${controller.path === "/console/overview" ? "topbar-overview" : ""}`}>
          <div className="topbar-title">
            <button aria-label="打开导航" className="mobile-menu" onClick={() => controller.setSidebarOpen(true)}><Menu aria-hidden size={19} /></button>
            <div>
              <span className="eyebrow">{adminSurface ? "Admin" : "Console"}</span>
              <h1>{controller.pageTitle}</h1>
            </div>
          </div>
          <div className="topbar-actions">
            <Tooltip content="刷新当前页面">
              <Button aria-label="刷新" onClick={() => void controller.refreshCurrentPage()} size="sm" uniform variant="ghost">
                <RefreshCw aria-hidden size={17} />
              </Button>
            </Tooltip>
            <Tooltip content="Account Settings">
              <Button aria-label="Account Settings" onClick={() => controller.setGlobalSlide("account")} size="sm" uniform variant="ghost">
                <Settings aria-hidden size={17} />
              </Button>
            </Tooltip>
            <Tooltip content="Support">
              <Button aria-label="Support" onClick={() => controller.setGlobalSlide("support")} size="sm" uniform variant="ghost">
                <CircleHelp aria-hidden size={17} />
              </Button>
            </Tooltip>
            <Tooltip content="退出登录">
              <Button aria-label="退出登录" onClick={() => void controller.signOut()} size="sm" uniform variant="ghost"><LogOut aria-hidden size={17} /></Button>
            </Tooltip>
          </div>
        </header>
        <main className="page-content">{children}</main>
      </div>

      <nav aria-label="移动端导航" className={`mobile-bottom-nav ${adminSurface ? "admin-mobile-nav" : ""}`}>
        {mobileItems.map((item) => {
          const Icon = icons[item.icon] || LayoutDashboard;
          return (
            <a className={isActive(controller.path, item.path) ? "active" : ""} href={item.path} key={item.path} onClick={(event) => { event.preventDefault(); controller.navigate(item.path); }}>
              <Icon aria-hidden size={18} />
              <span>{item.label}</span>
            </a>
          );
        })}
      </nav>

        {controller.globalSlide ? (
          <div className="account-band global-slide" role="complementary" aria-label={controller.globalSlide === "account" ? "Account Settings" : "Support"}>
            <div className="account-band-copy">
              <h2>{controller.globalSlide === "account" ? "Account Settings" : "Support"}</h2>
            {controller.globalSlide === "account" ? (
              <dl className="data-list">
                <div><dt>邮箱</dt><dd>{controller.session?.user.email || "-"}</dd></div>
                <div><dt>Account ID</dt><dd>{controller.session?.user.accountId || "-"}</dd></div>
                <div><dt>Console User ID</dt><dd>{controller.session?.user.consoleUserId || controller.session?.user.id || "-"}</dd></div>
                <div><dt>角色</dt><dd>{controller.session?.user.role || "暂不可用"}</dd></div>
                <div><dt>Sub2API User ID</dt><dd>{controller.session?.user.sub2apiUserId || "暂不可用"}</dd></div>
                <div><dt>账户状态</dt><dd>{controller.session?.user.status || "暂不可用"}</dd></div>
                <div><dt>Session 到期</dt><dd>{controller.session?.expiresAt || "暂不可用"}</dd></div>
              </dl>
            ) : (
              <SupportSlide controller={controller} />
            )}
          </div>
          <div className="account-band-action">
            {controller.globalSlide === "account" ? <Button onClick={() => void controller.signOut()} variant="outline"><LogOut aria-hidden size={16} />退出登录</Button> : null}
            <Button aria-label="关闭" onClick={() => controller.setGlobalSlide("")} uniform variant="ghost"><X aria-hidden size={18} /></Button>
          </div>
        </div>
      ) : null}

      {controller.toast.text ? <div className={`toast ${controller.toast.tone}`} role="status">{controller.toast.text}</div> : null}
    </div>
  );
}
