import React, { useEffect, useMemo, useState } from "react";
import {
  App as AntApp,
  Alert,
  Badge,
  Button,
  ConfigProvider,
  Descriptions,
  Empty,
  Flex,
  Space,
  Tag,
  Tooltip,
  Typography,
  theme
} from "antd";
import {
  PageContainer,
  ProCard,
  ProFormDigit,
  ProForm,
  ProFormDependency,
  ProFormRadio,
  ProFormText,
  ProLayout,
  ProTable,
  StatisticCard,
  StepsForm
} from "@ant-design/pro-components";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  Boxes,
  CheckCircle2,
  ClipboardCheck,
  ClipboardList,
  Cloud,
  Copy,
  CreditCard,
  KeyRound,
  Lock,
  LogOut,
  Play,
  RefreshCw,
  RotateCw,
  Server,
  Settings,
  ShieldCheck,
  Square,
  Trash2,
  WalletCards
} from "lucide-react";
import "antd/dist/reset.css";
import enUS from "antd/locale/en_US";
import "./styles.css";
import {
  canUseOperatorControls,
  commercialLoginMethods,
  eventRows,
  identityUserRows,
  landingFeatureCards,
  money,
  packageHoldPreview,
  packageSummary,
  publicNavigationItems,
  readinessStatus,
  unauthenticatedViewForPath,
  visibleConsoleMenuItems,
  workspaceActionState,
  workspaceStatus,
  workspaceTableRows
} from "./console-model.js";

const iconByPath = {
  "/overview": <Cloud size={16} />,
  "/account": <Settings size={16} />,
  "/workspaces": <Server size={16} />,
  "/create": <Play size={16} />,
  "/billing": <CreditCard size={16} />,
  "/alerts": <AlertTriangle size={16} />,
  "/audit": <ClipboardList size={16} />,
  "/users": <KeyRound size={16} />,
  "/runtime": <Activity size={16} />
};

const tagColor = {
  success: "success",
  warning: "warning",
  error: "error",
  processing: "processing",
  default: "default"
};

async function postJson(path, body) {
  const csrfToken = globalThis.window?.__OPL_CSRF_TOKEN || "";
  const headers = { "content-type": "application/json" };
  if (csrfToken) headers["x-opl-csrf-token"] = csrfToken;
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers,
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  const nextCsrf = response.headers.get("x-opl-csrf-token") || payload.csrfToken || "";
  if (nextCsrf && globalThis.window) globalThis.window.__OPL_CSRF_TOKEN = nextCsrf;
  if (nextCsrf) payload.__csrfToken = nextCsrf;
  return payload;
}

async function getJson(path) {
  const response = await fetch(path, { credentials: "same-origin" });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  const nextCsrf = response.headers.get("x-opl-csrf-token") || payload.csrfToken || "";
  if (nextCsrf && globalThis.window) globalThis.window.__OPL_CSRF_TOKEN = nextCsrf;
  return payload;
}

function StatusTag({ tone, children }) {
  return <Tag color={tagColor[tone] || "default"}>{children}</Tag>;
}

function Shell({ initialSession = null, onSignedOut }) {
  const { message, modal } = AntApp.useApp();
  const [activePath, setActivePath] = useState("/overview");
  const [publicView, setPublicView] = useState(() => unauthenticatedViewForPath(globalThis.window?.location?.pathname || "/"));
  const [session, setSession] = useState(initialSession);
  const [state, setState] = useState(null);
  const [readiness, setReadiness] = useState(null);
  const [productionReadiness, setProductionReadiness] = useState(null);
  const [selectedId, setSelectedId] = useState("");
  const [runtimeStatus, setRuntimeStatus] = useState(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  async function refresh() {
    const sessionResponse = await fetch("/api/session", { credentials: "same-origin" });
    if (sessionResponse.status === 401) {
      setAuthRequired(true);
      setPublicView(unauthenticatedViewForPath(globalThis.window?.location?.pathname || "/"));
      setSession(null);
      setState(null);
      onSignedOut?.();
      return;
    }
    const nextSession = await sessionResponse.json();
    if (!sessionResponse.ok || nextSession.ok === false) throw new Error(nextSession.error || "session_failed");
    const nextCsrf = sessionResponse.headers.get("x-opl-csrf-token");
    if (nextCsrf) globalThis.window.__OPL_CSRF_TOKEN = nextCsrf;

    const [stateResponse, readinessResponse, productionReadinessResponse] = await Promise.all([
      fetch("/api/state", { credentials: "same-origin" }),
      fetch("/api/runtime/readiness", { credentials: "same-origin" }),
      fetch("/api/production/readiness", { credentials: "same-origin" })
    ]);
    if (stateResponse.status === 401) {
      setAuthRequired(true);
      setSession(null);
      setState(null);
      return;
    }
    const next = await stateResponse.json();
    const nextReadiness = await readinessResponse.json();
    const nextProductionReadiness = await productionReadinessResponse.json();
    if (!stateResponse.ok || next.ok === false) throw new Error(next.error || "state_failed");
    setSession(nextSession);
    setState(next);
    setReadiness(nextReadiness);
    setProductionReadiness(nextProductionReadiness);
    setAuthRequired(false);
    setSelectedId((current) => current || next.workspaces[0]?.id || "");
  }

  async function run(action, successText) {
    try {
      setLoading(true);
      setError("");
      const result = await action();
      if (result?.__csrfToken) globalThis.window.__OPL_CSRF_TOKEN = result.__csrfToken;
      await refresh();
      if (successText) message.success(successText);
      return result;
    } catch (err) {
      setError(err.message);
      message.error(err.message);
      return null;
    } finally {
      setLoading(false);
    }
  }

  function confirmAction({ title, content, okText, danger = false, action, successText }) {
    modal.confirm({
      title,
      content,
      okText,
      okButtonProps: { danger },
      onOk: () => run(action, successText)
    });
  }

  async function logout() {
    try {
      setLoading(true);
      setError("");
      await postJson("/api/auth/logout", {});
      if (globalThis.window) {
        globalThis.window.__OPL_CSRF_TOKEN = "";
        globalThis.window.history.replaceState({}, "", "/");
      }
    setSession(null);
    setState(null);
    setAuthRequired(true);
    setPublicView("home");
    onSignedOut?.();
    message.success("Signed out");
    } catch (err) {
      setError(err.message);
      message.error(err.message);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    refresh().catch((err) => setError(err.message));
  }, []);

  useEffect(() => {
    function syncPublicRoute() {
      if (authRequired) {
        setPublicView(unauthenticatedViewForPath(globalThis.window?.location?.pathname || "/"));
      }
    }
    globalThis.window?.addEventListener("popstate", syncPublicRoute);
    return () => globalThis.window?.removeEventListener("popstate", syncPublicRoute);
  }, [authRequired]);

  function showPublicView(nextView) {
    const nextPath = nextView === "login" ? "/login" : "/";
    setPublicView(nextView);
    globalThis.window?.history.pushState({}, "", nextPath);
  }

  const selected = useMemo(
    () => state?.workspaces.find((item) => item.id === selectedId) || state?.workspaces[0],
    [state, selectedId]
  );

  const operator = canUseOperatorControls(session);
  const accountId = session?.accountId || state?.account?.id || "";
  const menuItems = useMemo(() => visibleConsoleMenuItems(session), [session]);
  const route = useMemo(() => ({
    path: "/",
    routes: menuItems.map((item) => ({
      path: item.path,
      name: item.label,
      icon: iconByPath[item.path]
    }))
  }), [menuItems]);

  const title = menuItems.find((item) => item.path === activePath)?.label || "Overview";

  useEffect(() => {
    if (menuItems.length && !menuItems.some((item) => item.path === activePath)) {
      setActivePath("/overview");
    }
  }, [activePath, menuItems]);

  if (authRequired) {
    if (publicView !== "login") {
      return <PublicHome onLoginClick={() => showPublicView("login")} />;
    }
    return (
      <LoginPage
        error={error}
        loading={loading}
        onBackHome={() => showPublicView("home")}
        onLogin={async (values) => {
          const result = await run(
            () => postJson(values.path, values.body),
            "Signed in"
          );
          if (result) globalThis.window?.history.replaceState({}, "", "/");
          return result;
        }}
      />
    );
  }

  return (
    <ProLayout
      title="OPL Console"
      logo={<div className="brandMark">OPL</div>}
      route={route}
      location={{ pathname: activePath }}
      layout="mix"
      fixedHeader
      fixSiderbar
      token={{
        header: { colorBgHeader: "#ffffff" },
        sider: { colorMenuBackground: "#111827", colorTextMenu: "#d1d5db", colorTextMenuSelected: "#ffffff" }
      }}
      actionsRender={() => [
        <Button key="refresh" icon={<RefreshCw size={16} />} loading={loading} onClick={refresh}>
          Refresh
        </Button>,
        <Button key="logout" icon={<LogOut size={16} />} loading={loading} onClick={logout}>
          Logout
        </Button>
      ].filter(Boolean)}
      avatarProps={{ icon: <Settings size={16} />, title: session?.displayName || accountId || "OPL" }}
      menuItemRender={(item, dom) => (
        <a
          href={item.path}
          onClick={(event) => {
            event.preventDefault();
            setActivePath(item.path);
          }}
        >
          {dom}
        </a>
      )}
    >
      <PageContainer title={title} subTitle={operator ? "Workspace distribution, billing holds, and operator evidence for OPL Cloud" : "Workspace distribution, billing holds, and access for OPL Cloud"}>
        {error && <Alert className="pageAlert" type="error" showIcon message={error} />}
        {!state ? <Empty description="Loading OPL Console" /> : (
          <>
            {activePath === "/overview" && (
              <OverviewPage
                state={state}
                readiness={readiness}
                productionReadiness={productionReadiness}
                selected={selected}
                setActivePath={setActivePath}
                operator={operator}
              />
            )}
            {activePath === "/account" && (
              <AccountPage session={session} state={state} run={run} />
            )}
            {activePath === "/workspaces" && (
              <WorkspacesPage
                state={state}
                selected={selected}
                selectedId={selectedId}
                setSelectedId={setSelectedId}
                accountId={accountId}
                operator={operator}
                run={run}
                confirmAction={confirmAction}
              />
            )}
            {activePath === "/create" && (
              <CreateWorkspacePage state={state} accountId={accountId} run={run} setSelectedId={setSelectedId} setActivePath={setActivePath} />
            )}
            {activePath === "/billing" && <BillingPage state={state} />}
            {activePath === "/alerts" && <EventsPage title="Alerts" events={state.notifications} />}
            {activePath === "/audit" && <EventsPage title="Audit receipts" events={state.audit} />}
            {operator && activePath === "/users" && <UsersPage run={run} />}
            {operator && activePath === "/runtime" && (
              <RuntimePage
                selected={selected}
                accountId={accountId}
                readiness={readiness}
                productionReadiness={productionReadiness}
                runtimeStatus={runtimeStatus}
                run={run}
                setRuntimeStatus={setRuntimeStatus}
              />
            )}
          </>
        )}
      </PageContainer>
    </ProLayout>
  );
}

function PublicHeader({ onLoginClick, onHomeClick }) {
  return (
    <header className="publicHeader">
      <a
        className="publicBrand"
        href="/"
        onClick={(event) => {
          event.preventDefault();
          onHomeClick?.();
        }}
      >
        <span className="brandMark">OPL</span>
        <span>OPL Cloud</span>
      </a>
      <nav className="publicNav" aria-label="Public navigation">
        {publicNavigationItems.map((item) => (
          <a
            key={item.path}
            href={item.path}
            onClick={(event) => {
              event.preventDefault();
              item.path === "/login" ? onLoginClick?.() : onHomeClick?.();
            }}
          >
            {item.label}
          </a>
        ))}
      </nav>
    </header>
  );
}

function FeatureIcon({ title }) {
  if (title.includes("billing")) return <WalletCards size={20} />;
  if (title.includes("runtime")) return <ShieldCheck size={20} />;
  return <Boxes size={20} />;
}

function PublicHome({ onLoginClick }) {
  return (
    <div className="publicShell">
      <PublicHeader onLoginClick={onLoginClick} onHomeClick={() => {}} />
      <main>
        <section className="homeHero">
          <div className="homeHeroPreview" aria-hidden="true">
            <div className="previewTopbar">
              <span />
              <span />
              <span />
            </div>
            <div className="previewGrid">
              <div className="previewSidebar">
                <span />
                <span />
                <span />
                <span />
              </div>
              <div className="previewMain">
                <div className="previewStats">
                  <span />
                  <span />
                  <span />
                </div>
                <div className="previewTable">
                  <span />
                  <span />
                  <span />
                  <span />
                </div>
              </div>
            </div>
          </div>
          <div className="homeHeroContent">
            <Tag color="blue">Commercial pilot console</Tag>
            <Typography.Title level={1}>OPL Cloud</Typography.Title>
            <Typography.Paragraph>
              Give each PI a private OPL Workspace with prepaid balance control, managed runtime, and clean audit receipts.
            </Typography.Paragraph>
            <div className="homeProofs">
              <span><CheckCircle2 size={16} /> PI-safe console</span>
              <span><CheckCircle2 size={16} /> Manual recharge</span>
              <span><CheckCircle2 size={16} /> Audit receipts</span>
            </div>
            <Space wrap>
              <Button type="primary" size="large" icon={<ArrowRight size={18} />} onClick={onLoginClick}>
                Login to Console
              </Button>
              <Button size="large" href="https://github.com/gaofeng21cn/one-person-lab-cloud/blob/main/README.zh-CN.md" target="_blank">
                OPL modules
              </Button>
            </Space>
          </div>
        </section>

        <section className="homeFeatures" aria-label="OPL Cloud capabilities">
          {landingFeatureCards.map((feature) => (
            <div className="homeFeature" key={feature.title}>
              <div className="homeFeatureIcon"><FeatureIcon title={feature.title.toLowerCase()} /></div>
              <Typography.Title level={3}>{feature.title}</Typography.Title>
              <Typography.Paragraph>{feature.description}</Typography.Paragraph>
            </div>
          ))}
        </section>
      </main>
    </div>
  );
}

function LoginPage({ error, loading, onLogin, onBackHome }) {
  return (
    <div className="publicShell loginPublicShell">
      <PublicHeader onLoginClick={() => {}} onHomeClick={onBackHome} />
      <main className="loginShell">
        <ProCard
          className="loginPanel"
          title={<Space><span className="brandMark">OPL</span><span>Login to OPL Console</span></Space>}
          extra={<Lock size={18} />}
        >
          <Space direction="vertical" size={16} className="fullWidth">
            <Typography.Text type="secondary">{commercialLoginMethods[0].label}</Typography.Text>
            {error && <Alert className="pageAlert" type="error" showIcon message={error} />}
            <ProForm
              layout="vertical"
              submitter={{
                searchConfig: { submitText: "Sign in" },
                submitButtonProps: { loading, type: "primary", block: true },
                resetButtonProps: false
              }}
              onFinish={async (values) => Boolean(await onLogin({
                path: "/api/auth/login",
                body: values
              }))}
            >
              <ProFormText
                name="email"
                label="Email"
                fieldProps={{ autoComplete: "email" }}
                rules={[
                  { required: true, message: "Email is required" },
                  { type: "email", message: "Use a valid email" }
                ]}
              />
              <ProFormText
                name="password"
                label="Password"
                fieldProps={{ type: "password", autoComplete: "current-password" }}
                rules={[{ required: true, message: "Password is required" }]}
              />
            </ProForm>
          </Space>
        </ProCard>
      </main>
    </div>
  );
}

function UsersPage({ run }) {
  const { message, modal } = AntApp.useApp();
  const [users, setUsers] = useState([]);
  const [loadingUsers, setLoadingUsers] = useState(false);
  const [auditState, setAuditState] = useState(null);
  const rows = useMemo(() => identityUserRows(users), [users]);

  async function refreshUsers() {
    setLoadingUsers(true);
    try {
      const payload = await getJson("/api/operator/users");
      setUsers(Array.isArray(payload) ? payload : []);
    } finally {
      setLoadingUsers(false);
    }
  }

  async function openUserAudit(row) {
    try {
      setLoadingUsers(true);
      const userState = await getJson(`/api/state?accountId=${encodeURIComponent(row.accountId)}`);
      setAuditState({
        user: row,
        audit: userState.audit || [],
        billingLedger: userState.billingLedger || []
      });
    } catch (err) {
      message.error(err.message);
    } finally {
      setLoadingUsers(false);
    }
  }

  function confirmDisable(row) {
    modal.confirm({
      title: "Disable user?",
      content: `${row.email} will lose access immediately; existing sessions and new logins will fail.`,
      okText: "Disable",
      okButtonProps: { danger: true },
      onOk: async () => {
        const result = await run(
          () => postJson("/api/operator/users/disable", {
            email: row.email,
            reason: "manual_operator_disable"
          }),
          "User disabled"
        );
        if (result) await refreshUsers();
      }
    });
  }

  useEffect(() => {
    refreshUsers().catch((err) => message.error(err.message));
  }, []);

  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <ProCard title="Identity users">
        <ProTable
          rowKey="key"
          search={false}
          options={false}
          loading={loadingUsers}
          dataSource={rows}
          pagination={{ pageSize: 8 }}
          locale={{ emptyText: <Empty description="No users yet" /> }}
          columns={[
            { title: "Email", dataIndex: "email", ellipsis: true },
            { title: "Account", dataIndex: "accountId", width: 160, ellipsis: true },
            { title: "Display name", dataIndex: "displayName", ellipsis: true },
            { title: "Tenant", dataIndex: "tenantId", width: 140, ellipsis: true },
            { title: "Role", dataIndex: "role", width: 110, render: (_, row) => <Tag>{row.role}</Tag> },
            { title: "Status", dataIndex: "status", width: 120, render: (_, row) => <StatusTag tone={row.statusTone}>{row.status}</StatusTag> },
            { title: "Updated", dataIndex: "updatedAt", width: 210 },
            {
              title: "Actions",
              width: 190,
              render: (_, row) => (
                <Space>
                  <Button size="small" onClick={() => openUserAudit(row)}>Audit</Button>
                  <Button
                    size="small"
                    danger
                    disabled={row.status === "disabled"}
                    onClick={() => confirmDisable(row)}
                  >
                    Disable
                  </Button>
                </Space>
              )
            }
          ]}
        />
      </ProCard>

      <div className="operatorForms">
        <ProCard title="Create PI account">
          <ProForm
            layout="vertical"
            initialValues={{ tenantId: "default" }}
            submitter={{
              searchConfig: { submitText: "Create account" },
              resetButtonProps: false
            }}
            onFinish={async (values) => {
              const result = await run(
                () => postJson("/api/operator/users", values),
                "Account created"
              );
              if (result) await refreshUsers();
              return Boolean(result);
            }}
          >
            <ProFormText
              name="email"
              label="Email"
              fieldProps={{ autoComplete: "off" }}
              rules={[
                { required: true, message: "Email is required" },
                { type: "email", message: "Use a valid email" }
              ]}
            />
            <ProFormText name="accountId" label="Account ID" rules={[{ required: true, message: "Account ID is required" }]} />
            <ProFormText name="displayName" label="Display name" rules={[{ required: true, message: "Display name is required" }]} />
            <ProFormText name="tenantId" label="Tenant" rules={[{ required: true, message: "Tenant is required" }]} />
            <ProFormText
              name="password"
              label="Initial password"
              fieldProps={{ type: "password", autoComplete: "new-password" }}
              rules={[
                { required: true, message: "Initial password is required" },
                { min: 12, message: "Use at least 12 characters" }
              ]}
            />
          </ProForm>
        </ProCard>

        <ProCard title="Manual recharge">
          <ProForm
            layout="vertical"
            initialValues={{ reason: "manual_top_up" }}
            submitter={{
              searchConfig: { submitText: "Add credit" },
              resetButtonProps: false
            }}
            onFinish={async (values) => {
              const result = await run(
                () => postJson("/api/accounts/credit", {
                  accountId: values.accountId,
                  amount: values.amount,
                  reason: values.reason || "manual_top_up"
                }),
                "Manual credit added"
              );
              if (auditState?.user?.accountId === values.accountId) await openUserAudit(auditState.user);
              return Boolean(result);
            }}
          >
            <ProFormText name="accountId" label="Account ID" rules={[{ required: true, message: "Account ID is required" }]} />
            <ProFormDigit
              name="amount"
              label="Amount"
              min={1}
              fieldProps={{ precision: 2, prefix: "¥" }}
              rules={[{ required: true, message: "Amount is required" }]}
            />
            <ProFormText name="reason" label="Reason" rules={[{ required: true, message: "Reason is required" }]} />
          </ProForm>
        </ProCard>
      </div>

      <ProCard
        title="User audit"
        extra={auditState?.user ? <Tag>{auditState.user.accountId}</Tag> : null}
      >
        {auditState?.user ? <EventsTable events={auditState.audit} /> : <Empty description="Select a user to inspect audit receipts" />}
      </ProCard>
    </Space>
  );
}

function OverviewPage({ state, readiness, productionReadiness, selected, setActivePath, operator }) {
  const runtime = readinessStatus(readiness);
  const production = readinessStatus(productionReadiness);
  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <StatisticCard.Group direction="row">
        <StatisticCard statistic={{ title: "Balance", value: money(state.account.balance) }} />
        <StatisticCard statistic={{ title: "Frozen holds", value: money(state.account.frozen) }} />
        <StatisticCard statistic={{ title: "Workspaces", value: state.workspaces.length }} />
        <StatisticCard statistic={{ title: "Alerts", value: state.notifications.length }} />
      </StatisticCard.Group>

      <ProCard split="vertical" gutter={16}>
        <ProCard title={operator ? "Runtime readiness" : "Workspace service"} colSpan={operator ? "50%" : "100%"}>
          <Space direction="vertical">
            <Badge status={runtime.tone === "success" ? "success" : "warning"} text={operator ? `${readiness?.provider || "runtime"}: ${runtime.label}` : runtime.label} />
            {operator && runtime.detail && <Typography.Text type="secondary">{runtime.detail}</Typography.Text>}
          </Space>
        </ProCard>
        {operator && (
          <ProCard title="Production launch">
            <Space direction="vertical">
              <Badge status={production.tone === "success" ? "success" : "warning"} text={production.label} />
              {production.detail && <Typography.Text type="secondary">{production.detail}</Typography.Text>}
            </Space>
          </ProCard>
        )}
      </ProCard>

      <ProCard
        title="Current Workspace"
        extra={<Button type="primary" onClick={() => setActivePath(selected ? "/workspaces" : "/create")}>{selected ? "Manage" : "Create"}</Button>}
      >
        {selected ? <WorkspaceDescriptions workspace={selected} operator={operator} /> : <Empty description="No Workspace yet" />}
      </ProCard>
    </Space>
  );
}

function AccountPage({ session, state, run }) {
  if (!session) return <ProCard><Empty description="Loading account" /></ProCard>;
  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <ProCard
        title="Account profile"
        extra={<StatusTag tone={session.role === "operator" ? "processing" : "success"}>{session.role}</StatusTag>}
      >
        <ProForm
          key={session.accountId}
          layout="vertical"
          initialValues={session}
          submitter={{
            searchConfig: { submitText: "Save profile" },
            resetButtonProps: false
          }}
          onFinish={async (values) => {
            await run(
              () => postJson("/api/session", {
                accountId: session.accountId,
                role: session.role,
                displayName: values.displayName,
                email: values.email,
                tenantId: values.tenantId
              }),
              "Account profile saved"
            );
            return true;
          }}
        >
          <ProFormText name="accountId" label="Account ID" disabled />
          <ProFormText name="displayName" label="Display name" rules={[{ required: true, message: "Display name is required" }]} />
          <ProFormText name="email" label="Email" rules={[{ type: "email", message: "Use a valid email" }]} />
          <ProFormText name="tenantId" label="Tenant" disabled />
          <ProFormText name="role" label="Role" disabled />
        </ProForm>
      </ProCard>

      <ProCard title="Account balance">
        <Descriptions bordered size="small" column={{ xs: 1, sm: 2, md: 3 }}>
          <Descriptions.Item label="Balance">{money(state.account.balance)}</Descriptions.Item>
          <Descriptions.Item label="Frozen holds">{money(state.account.frozen)}</Descriptions.Item>
          <Descriptions.Item label="Workspaces">{state.workspaces.length}</Descriptions.Item>
        </Descriptions>
      </ProCard>
    </Space>
  );
}

function WorkspacesPage({ state, selected, selectedId, setSelectedId, accountId, operator, run, confirmAction }) {
  const rows = workspaceTableRows(state.workspaces);
  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <ProTable
        rowKey="id"
        search={false}
        dataSource={rows}
        options={false}
        pagination={{ pageSize: 8 }}
        rowClassName={(row) => row.id === selectedId ? "selectedRow" : ""}
        onRow={(row) => ({ onClick: () => setSelectedId(row.id) })}
        columns={[
          { title: "Workspace", dataIndex: "name", ellipsis: true },
          { title: "Package", dataIndex: "packageId", width: 110 },
          {
            title: "Status",
            dataIndex: "status",
            width: 210,
            render: (_, row) => <StatusTag tone={row.statusTone}>{row.status}</StatusTag>
          },
          { title: "Compute", dataIndex: "compute", width: 160 },
          { title: "Storage", dataIndex: "storage", width: 190 },
          { title: "Token", dataIndex: "tokenStatus", width: 120, render: (_, row) => <Tag>{row.tokenStatus}</Tag> },
          {
            title: "URL",
            dataIndex: "url",
            ellipsis: true,
            render: (_, row) => <Typography.Text copyable={{ text: row.url }} ellipsis>{row.url}</Typography.Text>
          }
        ]}
      />
      <WorkspaceDetail workspace={selected} accountId={accountId} operator={operator} run={run} confirmAction={confirmAction} />
    </Space>
  );
}

function WorkspaceDetail({ workspace, accountId, operator, run, confirmAction }) {
  if (!workspace) return <ProCard><Empty description="Select or create a Workspace" /></ProCard>;
  const actions = workspaceActionState(workspace);
  return (
    <ProCard title="Workspace detail" extra={<StatusTag tone={workspaceStatus(workspace).tone}>{workspaceStatus(workspace).label}</StatusTag>}>
      <Space direction="vertical" size={16} className="fullWidth">
        <WorkspaceDescriptions workspace={workspace} operator={operator} />
        <Flex wrap="wrap" gap={8}>
          <Tooltip title={actions.canCopyUrl ? "Copy Workspace URL" : "Token is inactive"}>
            <Button
              icon={<Copy size={16} />}
              disabled={!actions.canCopyUrl}
              onClick={() => navigator.clipboard?.writeText(workspace.url)}
            >
              Copy URL
            </Button>
          </Tooltip>
          <Button
            icon={<RefreshCw size={16} />}
            disabled={!actions.canResetToken}
            onClick={() => run(
              () => postJson("/api/workspaces/reset-token", { accountId, workspaceId: workspace.id }),
              "Workspace token reset"
            )}
          >
            Reset token
          </Button>
          <Button
            danger
            icon={<Trash2 size={16} />}
            disabled={!actions.canDeleteToken}
            onClick={() => confirmAction({
              title: "Delete Workspace token?",
              content: "The current shared URL stops working until a new token is issued.",
              okText: "Delete token",
              danger: true,
              action: () => postJson("/api/workspaces/delete-token", { accountId, workspaceId: workspace.id }),
              successText: "Workspace token deleted"
            })}
          >
            Delete token
          </Button>
          <Button
            icon={<Square size={16} />}
            disabled={!actions.canStopCompute}
            onClick={() => confirmAction({
              title: "Stop compute?",
              content: "Compute billing stops. Persistent storage and storage billing continue.",
              okText: "Stop compute",
              action: () => postJson("/api/workspaces/stop-server", { accountId, workspaceId: workspace.id, confirm: true }),
              successText: "Compute stopped"
            })}
          >
            Stop compute
          </Button>
          <Button
            type="primary"
            icon={<RotateCw size={16} />}
            disabled={!actions.canRestartCompute}
            onClick={() => run(
              () => postJson("/api/workspaces/restart-server", { accountId, workspaceId: workspace.id }),
              "Compute restarted"
            )}
          >
            Restart
          </Button>
          <Button
            danger
            icon={<Trash2 size={16} />}
            disabled={!actions.canDestroyCompute}
            onClick={() => confirmAction({
              title: "Destroy compute?",
              content: "This removes runtime compute but keeps persistent storage for later recreation.",
              okText: "Destroy compute",
              danger: true,
              action: () => postJson("/api/workspaces/destroy-server", { accountId, workspaceId: workspace.id, confirm: true }),
              successText: "Compute destroyed"
            })}
          >
            Destroy compute
          </Button>
          <Button
            danger
            type="primary"
            icon={<Trash2 size={16} />}
            disabled={!actions.canDestroyStorage}
            onClick={() => confirmAction({
              title: "Destroy storage?",
              content: "This permanently deletes Workspace data from OPL Console's point of view and stops storage billing.",
              okText: "Destroy storage",
              danger: true,
              action: () => postJson("/api/workspaces/destroy-disk", { accountId, workspaceId: workspace.id, confirmDataLoss: true }),
              successText: "Storage destroyed"
            })}
          >
            Destroy storage
          </Button>
          <Button
            icon={<CreditCard size={16} />}
            onClick={() => run(
              () => postJson("/api/billing/settle", {
                accountId,
                workspaceId: workspace.id,
                hours: 1,
                sourceEventId: `console_billing_tick_${Date.now()}`
              }),
              "One billing hour settled"
            )}
          >
            Settle 1h
          </Button>
        </Flex>
      </Space>
    </ProCard>
  );
}

function WorkspaceDescriptions({ workspace, operator = false }) {
  const status = workspaceStatus(workspace);
  return (
    <Descriptions bordered size="small" column={{ xs: 1, sm: 1, md: 2, lg: 3 }}>
      <Descriptions.Item label="Name">{workspace.name}</Descriptions.Item>
      <Descriptions.Item label="State"><StatusTag tone={status.tone}>{status.label}</StatusTag></Descriptions.Item>
      <Descriptions.Item label="Package">{workspace.packageId}</Descriptions.Item>
      <Descriptions.Item label="Workspace URL" span={3}>
        <Typography.Text copyable={{ text: workspace.url }} ellipsis>{workspace.url}</Typography.Text>
      </Descriptions.Item>
      <Descriptions.Item label="Compute">{workspace.server.spec} / {workspace.server.status}</Descriptions.Item>
      <Descriptions.Item label="Storage">{workspace.disk.sizeGb}GB / {workspace.disk.status}</Descriptions.Item>
      {operator && (
        <Descriptions.Item label="Image">
          <Typography.Text ellipsis>{workspace.docker.image}</Typography.Text>
        </Descriptions.Item>
      )}
    </Descriptions>
  );
}

function CreateWorkspacePage({ state, accountId, run, setSelectedId, setActivePath }) {
  return (
    <ProCard title="Create OPL Workspace" className="createCard">
      <StepsForm
        onFinish={async (values) => {
          const workspace = await run(
            () => postJson("/api/workspaces", {
              accountId,
              workspaceName: values.workspaceName,
              packageId: values.packageId
            }),
            "Workspace created"
          );
          if (workspace?.id) {
            setSelectedId(workspace.id);
            setActivePath("/workspaces");
          }
          return true;
        }}
      >
        <StepsForm.StepForm title="Name">
          <ProFormText
            name="workspaceName"
            label="Workspace name"
            placeholder="Grant Lab"
            rules={[{ required: true, message: "Workspace name is required" }]}
          />
        </StepsForm.StepForm>
        <StepsForm.StepForm title="Package">
          <ProFormRadio.Group
            name="packageId"
            label="Compute and storage package"
            rules={[{ required: true, message: "Choose a package" }]}
            options={state.packages.map((plan) => ({
              label: `${plan.name} - ${packageSummary(plan)}`,
              value: plan.id
            }))}
          />
        </StepsForm.StepForm>
        <StepsForm.StepForm title="Confirm">
          <ProFormDependency name={["packageId", "workspaceName"]}>
            {({ packageId, workspaceName }) => {
              const plan = state.packages.find((item) => item.id === packageId);
              if (!plan) return <Alert type="info" showIcon message="Choose a package to review billing." />;
              const hold = packageHoldPreview(plan, state.billingPolicy);
              return (
                <Alert
                  type="warning"
                  showIcon
                  message={`Open ${workspaceName || "Workspace"} with ${plan.name}`}
                  description={`${money(plan.price.computeHourly)}/compute hour plus ${money(plan.price.storageGbMonth)}/GB-month storage. Opening requires a ${hold.holdDays}-day hold: ${money(hold.compute)} compute + ${money(hold.storage)} storage = ${money(hold.total)}.`}
                />
              );
            }}
          </ProFormDependency>
        </StepsForm.StepForm>
      </StepsForm>
    </ProCard>
  );
}

function BillingPage({ state }) {
  const policy = state.billingPolicy;
  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <StatisticCard.Group direction="row">
        <StatisticCard statistic={{ title: "Hold window", value: `${policy.prepaidHoldDays} days` }} />
        <StatisticCard statistic={{ title: "Minimum charge", value: `${policy.minimumBillableHours}h` }} />
        <StatisticCard statistic={{ title: "Markup", value: `${Math.round(policy.markup * 100)}%` }} />
        <StatisticCard statistic={{ title: "Storage exhaustion", value: policy.storageHoldExhaustion }} />
      </StatisticCard.Group>
      <EventsTable events={state.billingLedger} />
    </Space>
  );
}

function EventsPage({ title, events }) {
  return (
    <ProCard title={title}>
      <EventsTable events={events} />
    </ProCard>
  );
}

function EventsTable({ events }) {
  return (
    <ProTable
      rowKey="id"
      search={false}
      options={false}
      pagination={{ pageSize: 10 }}
      dataSource={eventRows(events)}
      locale={{ emptyText: <Empty description="No events yet" /> }}
      columns={[
        { title: "Type", dataIndex: "type", width: 240, ellipsis: true },
        { title: "Account", dataIndex: "accountId", width: 160, ellipsis: true },
        { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
        { title: "Source", dataIndex: "sourceEventId", ellipsis: true },
        {
          title: "Amount",
          dataIndex: "amount",
          width: 110,
          align: "right",
          render: (_, row) => row.amount !== undefined ? money(row.amount) : ""
        },
        { title: "Message", dataIndex: "message", ellipsis: true },
        { title: "Time", dataIndex: "createdAt", width: 210 }
      ]}
    />
  );
}

function RuntimePage({ selected, accountId, readiness, productionReadiness, runtimeStatus, run, setRuntimeStatus }) {
  const runtime = readinessStatus(readiness);
  const production = readinessStatus(productionReadiness);
  return (
    <Space direction="vertical" size={16} className="fullWidth">
      <ProCard split="vertical">
        <ProCard title="Runtime provider">
          <Space direction="vertical">
            <Badge status={runtime.tone === "success" ? "success" : "warning"} text={`${readiness?.provider || "runtime"}: ${runtime.label}`} />
            {runtime.detail && <Typography.Text type="secondary">{runtime.detail}</Typography.Text>}
          </Space>
        </ProCard>
        <ProCard title="Production gate">
          <Space direction="vertical">
            <Badge status={production.tone === "success" ? "success" : "warning"} text={production.label} />
            {production.detail && <Typography.Text type="secondary">{production.detail}</Typography.Text>}
          </Space>
        </ProCard>
      </ProCard>

      <ProCard
        title="Selected Workspace runtime evidence"
        extra={<Button
          icon={<ClipboardCheck size={16} />}
          disabled={!selected}
          onClick={() => run(async () => {
            const status = await postJson("/api/workspaces/runtime-status", { accountId, workspaceId: selected.id });
            setRuntimeStatus(status);
            return status;
          }, "Runtime status checked")}
        >
          Check runtime
        </Button>}
      >
        {!selected && <Empty description="Select a Workspace first" />}
        {selected && !runtimeStatus && <WorkspaceDescriptions workspace={selected} operator />}
        {runtimeStatus && (
          <Space direction="vertical" size={12} className="fullWidth">
            <StatusTag tone={runtimeStatus.ready ? "success" : "error"}>{runtimeStatus.ready ? "Runtime ready" : "Runtime blocked"}</StatusTag>
            <ProTable
              rowKey="name"
              search={false}
              options={false}
              pagination={false}
              dataSource={(runtimeStatus.checks || []).map((check) => ({ ...check, key: check.name }))}
              columns={[
                { title: "Check", dataIndex: "name" },
                { title: "Result", dataIndex: "ok", width: 130, render: (_, row) => <StatusTag tone={row.ok ? "success" : "error"}>{row.ok ? "Pass" : "Fail"}</StatusTag> }
              ]}
            />
            <Typography.Paragraph>
              <Typography.Text code>{JSON.stringify(runtimeStatus.resources || {}, null, 2)}</Typography.Text>
            </Typography.Paragraph>
          </Space>
        )}
      </ProCard>
    </Space>
  );
}

function Root({ initialSession = null, onSignedOut }) {
  return (
    <ConfigProvider
      locale={enUS}
      theme={{
        algorithm: theme.defaultAlgorithm,
        token: {
          colorPrimary: "#1677ff",
          borderRadius: 6,
          fontFamily: "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
        }
      }}
    >
      <AntApp>
        <Shell initialSession={initialSession} onSignedOut={onSignedOut} />
      </AntApp>
    </ConfigProvider>
  );
}

export default Root;
