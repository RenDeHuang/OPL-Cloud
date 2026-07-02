import React, { useEffect, useMemo, useState } from "react";
import {
  PageContainer,
  ProCard,
  ProLayout,
  ProTable,
  StatisticCard,
  StepsForm,
  ProFormText,
  ProFormSelect
} from "@ant-design/pro-components";
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  List,
  Modal,
  Space,
  Tag,
  Timeline,
  Typography,
  message
} from "antd";
import {
  Activity,
  AlertTriangle,
  Bell,
  Boxes,
  ClipboardCheck,
  CreditCard,
  Database,
  FileText,
  Gauge,
  HardDrive,
  Headphones,
  KeyRound,
  Layers,
  Link as LinkIcon,
  LogOut,
  Play,
  Plus,
  ReceiptText,
  RefreshCw,
  RotateCw,
  Server,
  ShieldCheck,
  Square,
  Trash2,
  UserRound,
  WalletCards
} from "lucide-react";
import { adminMenuRoutes, navigate, ownerMenuRoutes } from "../consoleRoutes.js";

async function api(path, body, csrfToken) {
  const response = await fetch(path, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-opl-csrf": csrfToken
    },
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  return payload;
}

async function getJson(path) {
  const response = await fetch(path);
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  return payload;
}

function money(value) {
  return `¥${Number(value || 0).toFixed(2)}`;
}

function planHold(plan) {
  if (!plan) return 0;
  return Number(plan.price?.computeHourly || 0) * 24 * 7
    + Number(plan.price?.storageGbMonth || 0) * Number(plan.diskGb || 0) / 30 * 7;
}

function available(wallet) {
  return Number(wallet?.available ?? (Number(wallet?.balance || 0) - Number(wallet?.frozen || 0)));
}

function statusLabel(workspace) {
  if (!workspace) return "No Workspace";
  const labels = {
    running: "运行中",
    stopped_server_disk_retained: "已停止",
    server_destroyed_disk_retained: "计算销毁",
    storage_hold_exhausted: "存储冻结不足",
    stopped_storage_hold_exhausted: "已停止",
    destroyed: "已销毁",
    failed: "失败"
  };
  return labels[workspace.state] || workspace.state;
}

function valueLabel(value) {
  const labels = {
    active: "有效",
    available: "可用",
    running: "运行中",
    stopped: "已停止",
    destroyed: "已销毁",
    failed: "失败",
    retained: "保留",
    attached_retained: "挂载保留",
    detached_retained: "卸载保留",
    restored_retained: "已恢复",
    hold_exhausted: "冻结不足",
    ready: "就绪",
    blocked: "阻塞"
  };
  return labels[value] || value || "-";
}

function statusColor(value) {
  if (["running", "active", "available", "ready"].includes(value)) return "green";
  if (["failed", "destroyed", "hold_exhausted", "blocked"].includes(value)) return "red";
  if (["stopped", "stopped_server_disk_retained", "server_destroyed_disk_retained"].includes(value)) return "orange";
  return "blue";
}

function packageText(plan) {
  if (!plan) return "-";
  return `${plan.cpu} CPU / ${plan.memoryGb}GB / ${plan.diskGb}GB`;
}

function usageAmount(logs, resourceType = "") {
  return (logs || [])
    .filter((log) => !resourceType || log.resourceType === resourceType)
    .reduce((sum, log) => sum + Number(log.amount || 0), 0);
}

function usageQuantity(logs, resourceType) {
  return (logs || [])
    .filter((log) => log.resourceType === resourceType)
    .reduce((sum, log) => sum + Number(log.quantity || 0), 0);
}

function firstWorkspacePath(workspaces) {
  return workspaces?.[0] ? `/console/workspaces/${workspaces[0].id}` : "/console/workspaces";
}

function menuIcon(path) {
  const map = {
    "/console/overview": <Gauge size={17} />,
    "/console/workspaces": <Server size={17} />,
    "/console/gateway": <KeyRound size={17} />,
    "/console/billing": <WalletCards size={17} />,
    "/console/account": <UserRound size={17} />,
    "/console/support": <Headphones size={17} />,
    "/console/alerts": <Bell size={17} />,
    "/admin/overview": <Gauge size={17} />,
    "/admin/users": <UserRound size={17} />,
    "/admin/governance": <ShieldCheck size={17} />,
    "/admin/workspaces": <Server size={17} />,
    "/admin/billing": <CreditCard size={17} />,
    "/admin/gateway": <KeyRound size={17} />,
    "/admin/fabric": <Boxes size={17} />,
    "/admin/ledger": <Database size={17} />,
    "/admin/runtime": <Activity size={17} />,
    "/admin/support": <Headphones size={17} />,
    "/admin/audit": <ClipboardCheck size={17} />,
    "/admin/settings": <Layers size={17} />
  };
  return map[path] || <FileText size={17} />;
}

function buildMenu(isAdmin) {
  const owner = ownerMenuRoutes.map((route) => ({
    path: route.path,
    name: route.label,
    icon: menuIcon(route.path)
  }));
  const admin = isAdmin ? [{
    path: "/admin",
    name: "Admin",
    icon: <ShieldCheck size={17} />,
    children: adminMenuRoutes.map((route) => ({
      path: route.path,
      name: route.label,
      icon: menuIcon(route.path)
    }))
  }] : [];
  return [...owner, ...admin];
}

function useTickets() {
  const [tickets, setTickets] = useState(() => {
    try {
      return JSON.parse(localStorage.getItem("opl-support-tickets") || "[]");
    } catch {
      return [];
    }
  });
  function save(next) {
    setTickets(next);
    localStorage.setItem("opl-support-tickets", JSON.stringify(next));
  }
  function createTicket(input) {
    const ticket = {
      id: `ticket-${Date.now()}`,
      title: input.title,
      category: input.category,
      priority: input.priority,
      workspaceId: input.workspaceId || "",
      status: "open",
      createdAt: new Date().toISOString(),
      messages: [{ author: "Lab Owner", text: input.description || "Created from OPL Console" }]
    };
    save([ticket, ...tickets]);
    return ticket;
  }
  return { tickets, createTicket };
}

export default function ConsolePage({ route, session, onLogout }) {
  const [state, setState] = useState(null);
  const [adminOps, setAdminOps] = useState({ operator: null, runtime: null, launch: null, error: "" });
  const [topUpOpen, setTopUpOpen] = useState(false);
  const [topUpForm] = Form.useForm();
  const [createPackageId, setCreatePackageId] = useState("basic");
  const tickets = useTickets();
  const isAdmin = session.user.role === "admin";
  const path = window.location.pathname;

  async function refresh() {
    const next = await getJson("/api/state");
    setState(next);
  }

  async function refreshAdminOps() {
    if (!isAdmin) return;
    try {
      const [operator, runtime, launch] = await Promise.all([
        getJson("/api/operator/summary"),
        getJson("/api/runtime/readiness"),
        getJson("/api/production/readiness")
      ]);
      setAdminOps({ operator, runtime, launch, error: "" });
    } catch (err) {
      setAdminOps((current) => ({ ...current, error: err.message }));
    }
  }

  async function runAction(action, success = "Done") {
    try {
      await action();
      await refresh();
      await refreshAdminOps();
      message.success(success);
    } catch (err) {
      message.error(err.message);
    }
  }

  async function logout() {
    try {
      await api("/api/auth/logout", {}, session.csrfToken);
    } finally {
      onLogout();
      navigate("/");
    }
  }

  useEffect(() => {
    refresh().catch((err) => message.error(err.message));
  }, []);

  useEffect(() => {
    refreshAdminOps();
  }, [isAdmin]);

  const wallet = state?.wallet || state?.account || { balance: 0, frozen: 0, available: 0, totalRecharged: 0 };
  const selectedId = path.match(/\/(?:console|admin)\/workspaces\/([^/]+)/)?.[1];
  const selected = useMemo(
    () => state?.workspaces?.find((workspace) => workspace.id === selectedId) || state?.workspaces?.[0],
    [state, selectedId]
  );
  const selectedPlan = useMemo(
    () => state?.packages?.find((plan) => plan.id === selected?.packageId) || state?.packages?.find((plan) => plan.id === createPackageId),
    [state, selected, createPackageId]
  );
  const selectedCreatePlan = useMemo(
    () => state?.packages?.find((plan) => plan.id === createPackageId) || state?.packages?.[0],
    [state, createPackageId]
  );

  if (!state) return <div className="loading">Loading OPL Console...</div>;

  return (
    <ProLayout
      title="OPL Cloud"
      logo={<div className="proLogo">OPL</div>}
      location={{ pathname: path }}
      layout="mix"
      navTheme="light"
      menuDataRender={() => buildMenu(isAdmin)}
      menuItemRender={(item, dom) => (
        <a onClick={(event) => {
          event.preventDefault();
          navigate(item.path || "/console/overview");
        }} href={item.path}>{dom}</a>
      )}
      actionsRender={() => [
        <Tag color={isAdmin ? "purple" : "blue"} key="role">{isAdmin ? "Admin" : "Lab Owner"}</Tag>,
        <Button key="logout" icon={<LogOut size={15} />} onClick={logout}>退出</Button>
      ]}
      avatarProps={{
        title: session.user.email,
        size: "small",
        icon: <UserRound size={16} />
      }}
    >
      {renderRoute({
        route,
        path,
        state,
        wallet,
        selected,
        selectedPlan,
        selectedCreatePlan,
        setCreatePackageId,
        session,
        isAdmin,
        adminOps,
        tickets,
        topUpOpen,
        setTopUpOpen,
        topUpForm,
        runAction,
        refresh,
        refreshAdminOps
      })}
    </ProLayout>
  );
}

function renderRoute(ctx) {
  const { path, route, isAdmin } = ctx;
  if (route.area === "admin" && !isAdmin) return <ForbiddenPage />;
  if (path.startsWith("/admin/users")) return <AdminUsersPage {...ctx} />;
  if (path.startsWith("/admin/billing")) return <AdminBillingPage {...ctx} />;
  if (path.startsWith("/admin/fabric")) return <AdminFabricPage {...ctx} />;
  if (path.startsWith("/admin/ledger")) return <AdminLedgerPage {...ctx} />;
  if (path.startsWith("/admin/runtime")) return <AdminRuntimePage {...ctx} />;
  if (path.startsWith("/admin/support")) return <AdminSupportPage {...ctx} />;
  if (path.startsWith("/admin")) return <AdminOverviewPage {...ctx} />;
  if (path.startsWith("/console/workspaces/new")) return <CreateWorkspacePage {...ctx} />;
  if (path.startsWith("/console/workspaces/") || path.startsWith("/admin/workspaces/")) return <WorkspaceDetailPage {...ctx} />;
  if (path.startsWith("/console/workspaces")) return <WorkspacesPage {...ctx} />;
  if (path.startsWith("/console/gateway")) return <GatewayPage {...ctx} />;
  if (path.startsWith("/console/billing")) return <BillingPage {...ctx} />;
  if (path.startsWith("/console/account")) return <AccountPage {...ctx} />;
  if (path.startsWith("/console/support/new")) return <NewSupportTicketPage {...ctx} />;
  if (path.startsWith("/console/support/")) return <SupportTicketPage {...ctx} />;
  if (path.startsWith("/console/support")) return <SupportPage {...ctx} />;
  if (path.startsWith("/console/resources")) return <ResourcesPage {...ctx} />;
  if (path.startsWith("/console/approvals")) return <ApprovalsPage {...ctx} />;
  if (path.startsWith("/console/receipts")) return <ReceiptsPage {...ctx} />;
  if (path.startsWith("/console/alerts")) return <AlertsPage {...ctx} />;
  if (path === "/403") return <ForbiddenPage />;
  return <OverviewPage {...ctx} />;
}

function OverviewPage({ state, wallet, tickets }) {
  const needsAttention = state.notifications?.length || 0;
  return (
    <PageContainer title="总览" subTitle="Workspace delivery, wallet, tickets">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "余额", value: money(wallet.balance) }} />
        <StatisticCard statistic={{ title: "冻结", value: money(wallet.frozen) }} />
        <StatisticCard statistic={{ title: "Workspace", value: state.workspaces.length }} />
        <StatisticCard statistic={{ title: "工单", value: tickets.tickets.length }} />
        <StatisticCard statistic={{ title: "告警", value: needsAttention }} />
      </StatisticCard.Group>
      <ProCard className="sectionCard" gutter={16} split="vertical">
        <ProCard title="下一步" colSpan="35%">
          <Space direction="vertical" size={12}>
            <Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/workspaces/new")}>创建 Workspace</Button>
            <Button icon={<Headphones size={15} />} onClick={() => navigate("/console/support/new")}>提交工单</Button>
            <Button icon={<WalletCards size={15} />} onClick={() => navigate("/console/billing/wallet")}>查看钱包</Button>
          </Space>
        </ProCard>
        <ProCard title="最近告警">
          <AlertList events={state.notifications} />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function WorkspacesPage({ state, wallet, runAction, session }) {
  const planById = Object.fromEntries((state.packages || []).map((plan) => [plan.id, plan]));
  return (
    <PageContainer
      title="OPL Workspace"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/workspaces/new")}>创建</Button>}
    >
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={state.workspaces}
        columns={[
          { title: "名称", dataIndex: "name", render: (_, row) => <Button type="link" onClick={() => navigate(`/console/workspaces/${row.id}`)}>{row.name}</Button> },
          { title: "状态", dataIndex: "state", render: (_, row) => <Tag color={statusColor(row.state)}>{statusLabel(row)}</Tag> },
          { title: "套餐", dataIndex: "packageId", render: (value) => packageText(planById[value]) },
          { title: "Workspace URL", dataIndex: "url", ellipsis: true, render: (_, row) => <Typography.Text copyable={row.access?.tokenStatus === "active"}>{row.url}</Typography.Text> },
          { title: "余额", render: () => money(available(wallet)) },
          {
            title: "操作",
            valueType: "option",
            render: (_, row) => [
              <Button key="open" size="small" icon={<LinkIcon size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => window.open(row.url, "_blank", "noopener,noreferrer")}>打开</Button>,
              <Button key="reset" size="small" icon={<RefreshCw size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/reset-token", { workspaceId: row.id }, session.csrfToken), "URL 已重置")}>重置</Button>,
              <Button key="delete" size="small" danger icon={<Trash2 size={14} />} disabled={row.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/delete-token", { workspaceId: row.id }, session.csrfToken), "URL 已停用")}>停用</Button>
            ]
          }
        ]}
      />
    </PageContainer>
  );
}

function WorkspaceDetailPage({ selected, selectedPlan, state, session, runAction }) {
  if (!selected) return <PageContainer title="Workspace"><Empty description="暂无 Workspace" /></PageContainer>;
  const backups = (state.storageBackups || []).filter((backup) => backup.workspaceId === selected.id);
  return (
    <PageContainer
      title={selected.name}
      subTitle="Workspace URL, compute, storage"
      extra={<Button onClick={() => navigate("/console/workspaces")}>返回列表</Button>}
    >
      <ProCard gutter={16} wrap>
        <ProCard title="Workspace URL" colSpan={{ xs: 24, xl: 12 }}>
          <Space direction="vertical" size={16} className="fullWidth">
            <Typography.Text copyable={selected.access?.tokenStatus === "active"} ellipsis>{selected.url}</Typography.Text>
            <Space wrap>
              <Button icon={<LinkIcon size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => window.open(selected.url, "_blank", "noopener,noreferrer")}>打开</Button>
              <Button icon={<RefreshCw size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/reset-token", { workspaceId: selected.id }, session.csrfToken), "URL 已重置")}>重置</Button>
              <Button danger icon={<Trash2 size={15} />} disabled={selected.access?.tokenStatus !== "active"} onClick={() => runAction(() => api("/api/workspaces/delete-token", { workspaceId: selected.id }, session.csrfToken), "URL 已停用")}>停用</Button>
            </Space>
          </Space>
        </ProCard>
        <ProCard title="计算与存储" colSpan={{ xs: 24, xl: 12 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="状态"><Tag color={statusColor(selected.state)}>{statusLabel(selected)}</Tag></Descriptions.Item>
            <Descriptions.Item label="套餐">{selectedPlan?.name} · {packageText(selectedPlan)}</Descriptions.Item>
            <Descriptions.Item label="计算">{selected.server?.spec} · {valueLabel(selected.server?.status)}</Descriptions.Item>
            <Descriptions.Item label="存储">{selected.disk?.sizeGb}GB · {valueLabel(selected.disk?.status)}</Descriptions.Item>
          </Descriptions>
        </ProCard>
      </ProCard>
      <ProCard className="sectionCard" gutter={16} wrap>
        <ProCard title="生命周期" colSpan={{ xs: 24, xl: 12 }}>
          <Space wrap>
            <Button icon={<Square size={15} />} onClick={() => runAction(() => api("/api/workspaces/stop-server", { workspaceId: selected.id, confirm: true }, session.csrfToken), "计算已停止")}>停止计算</Button>
            <Button icon={<RotateCw size={15} />} onClick={() => runAction(() => api("/api/workspaces/restart-server", { workspaceId: selected.id }, session.csrfToken), "计算已启动")}>启动计算</Button>
            <Button danger icon={<Trash2 size={15} />} onClick={() => runAction(() => api("/api/workspaces/destroy-server", { workspaceId: selected.id, confirm: true }, session.csrfToken), "计算已销毁")}>销毁计算</Button>
            <Button danger icon={<HardDrive size={15} />} onClick={() => runAction(() => api("/api/workspaces/destroy-disk", { workspaceId: selected.id, confirmDataLoss: true }, session.csrfToken), "存储已销毁")}>销毁存储</Button>
          </Space>
        </ProCard>
        <ProCard title="备份" colSpan={{ xs: 24, xl: 12 }}>
          <Space direction="vertical" className="fullWidth">
            <Button icon={<Database size={15} />} onClick={() => runAction(() => api("/api/workspaces/storage-backups", { workspaceId: selected.id, reason: "console", retentionPolicy: { retainLast: 2 } }, session.csrfToken), "备份已创建")}>创建备份</Button>
            <List
              size="small"
              dataSource={backups.slice(-4).reverse()}
              locale={{ emptyText: "暂无备份" }}
              renderItem={(backup) => <List.Item><Tag>{valueLabel(backup.status)}</Tag><Typography.Text ellipsis>{backup.id}</Typography.Text></List.Item>}
            />
          </Space>
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function CreateWorkspacePage({ state, wallet, selectedCreatePlan, setCreatePackageId, session, runAction }) {
  const enough = available(wallet) >= planHold(selectedCreatePlan);
  return (
    <PageContainer title="创建 Workspace" subTitle="Package, price, 7-day hold">
      <ProCard>
        <StepsForm
          onFinish={async (values) => {
            await runAction(
              () => api("/api/workspaces", {
                workspaceName: values.workspaceName,
                packageId: values.packageId
              }, session.csrfToken),
              "Workspace 已创建"
            );
            navigate("/console/workspaces");
            return true;
          }}
        >
          <StepsForm.StepForm name="name" title="Name">
            <ProFormText name="workspaceName" label="名称" initialValue="Lab Workspace" rules={[{ required: true }]} />
          </StepsForm.StepForm>
          <StepsForm.StepForm name="package" title="Package">
            <ProFormSelect
              name="packageId"
              label="套餐"
              initialValue={selectedCreatePlan?.id || "basic"}
              options={(state.packages || []).map((plan) => ({ label: `${plan.name} · ${packageText(plan)}`, value: plan.id }))}
              fieldProps={{ onChange: setCreatePackageId }}
              rules={[{ required: true }]}
            />
          </StepsForm.StepForm>
          <StepsForm.StepForm name="confirm" title="Confirm">
            <ProCard bordered>
              <Descriptions column={1} size="small">
                <Descriptions.Item label="套餐">{selectedCreatePlan?.name}</Descriptions.Item>
                <Descriptions.Item label="计算">{money(selectedCreatePlan?.price?.computeHourly)}/hour</Descriptions.Item>
                <Descriptions.Item label="存储">{money(selectedCreatePlan?.price?.storageGbMonth)}/GB/month</Descriptions.Item>
                <Descriptions.Item label="7-day hold">{money(planHold(selectedCreatePlan))}</Descriptions.Item>
                <Descriptions.Item label="可用余额">{money(available(wallet))}</Descriptions.Item>
              </Descriptions>
              {!enough && <Alert type="warning" showIcon message="余额不足，无法完成预冻结。" />}
            </ProCard>
          </StepsForm.StepForm>
        </StepsForm>
      </ProCard>
    </PageContainer>
  );
}

function GatewayPage({ state }) {
  const requestUsage = state.requestUsageLogs || [];
  return (
    <PageContainer title="OPL Gateway" subTitle="Keys, usage, quotas">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "请求", value: requestUsage.length }} />
        <StatisticCard statistic={{ title: "扣费", value: money(usageAmount(requestUsage)) }} />
        <StatisticCard statistic={{ title: "可用密钥", value: 1 }} />
      </StatisticCard.Group>
      <ProCard className="sectionCard" gutter={16} wrap>
        <ProCard title="接入密钥" colSpan={{ xs: 24, xl: 10 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="状态"><Tag color="green">Active</Tag></Descriptions.Item>
            <Descriptions.Item label="作用域">当前实验室</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="最近用量" colSpan={{ xs: 24, xl: 14 }}>
          <UsageTable data={requestUsage} type="request" />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function BillingPage({ state, wallet }) {
  const resourceUsage = state.resourceUsageLogs || [];
  const requestUsage = state.requestUsageLogs || [];
  const recent = [
    ...resourceUsage.map((item) => ({ ...item, billingType: item.resourceType === "compute" ? "计算" : "存储" })),
    ...requestUsage.map((item) => ({ ...item, billingType: "请求", quantity: 1, unit: "request" }))
  ].slice(-12).reverse();
  return (
    <PageContainer title="账单" subTitle="Wallet, holds, usage">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "余额", value: money(wallet.balance) }} />
        <StatisticCard statistic={{ title: "冻结", value: money(wallet.frozen) }} />
        <StatisticCard statistic={{ title: "可用", value: money(available(wallet)) }} />
        <StatisticCard statistic={{ title: "累计充值", value: money(wallet.totalRecharged) }} />
      </StatisticCard.Group>
      <ProCard className="sectionCard" gutter={16} wrap>
        <ProCard title="资源用量" colSpan={{ xs: 24, xl: 10 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="Compute">{usageQuantity(resourceUsage, "compute").toFixed(1)} hours</Descriptions.Item>
            <Descriptions.Item label="Storage">{usageQuantity(resourceUsage, "storage").toFixed(1)} GB-hour</Descriptions.Item>
            <Descriptions.Item label="Requests">{requestUsage.length}</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="最近扣费" colSpan={{ xs: 24, xl: 14 }}>
          <ProTable
            rowKey={(row) => row.id}
            search={false}
            options={false}
            pagination={false}
            size="small"
            dataSource={recent}
            columns={[
              { title: "类型", dataIndex: "billingType" },
              { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
              { title: "用量", render: (_, row) => `${Number(row.quantity || 0).toFixed(2)} ${row.unit || ""}` },
              { title: "金额", dataIndex: "amount", render: (value) => money(value) }
            ]}
          />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function AccountPage({ state, wallet, session }) {
  return (
    <PageContainer title="账户与实验室" subTitle="Identity, wallet, lab policy">
      <ProCard gutter={16} wrap>
        <ProCard title="身份" colSpan={{ xs: 24, xl: 8 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="邮箱">{session.user.email}</Descriptions.Item>
            <Descriptions.Item label="角色">{session.user.role === "admin" ? "Admin" : "Lab Owner"}</Descriptions.Item>
            <Descriptions.Item label="账号">{state.account.id}</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="钱包" colSpan={{ xs: 24, xl: 8 }}>
          <StatisticCard statistic={{ title: "可用余额", value: money(available(wallet)) }} />
        </ProCard>
        <ProCard title="实验室策略" colSpan={{ xs: 24, xl: 8 }}>
          <List size="small" dataSource={["Workspace URL 可分发", "7 天资源预冻结", "账单按小时解释"]} renderItem={(item) => <List.Item>{item}</List.Item>} />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function SupportPage({ tickets }) {
  return (
    <PageContainer title="工单" extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/support/new")}>提交工单</Button>}>
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={tickets.tickets}
        locale={{ emptyText: <Empty description="暂无工单" /> }}
        columns={[
          { title: "标题", dataIndex: "title", render: (_, row) => <Button type="link" onClick={() => navigate(`/console/support/${row.id}`)}>{row.title}</Button> },
          { title: "分类", dataIndex: "category" },
          { title: "优先级", dataIndex: "priority", render: (value) => <Tag color={value === "high" ? "red" : "blue"}>{value}</Tag> },
          { title: "状态", dataIndex: "status", render: (value) => <Tag color="green">{value}</Tag> }
        ]}
      />
    </PageContainer>
  );
}

function NewSupportTicketPage({ state, tickets }) {
  const [form] = Form.useForm();
  return (
    <PageContainer title="提交工单" subTitle="Account, billing, Workspace">
      <ProCard>
        <Form form={form} layout="vertical" onFinish={(values) => {
          const ticket = tickets.createTicket(values);
          message.success("工单已提交");
          navigate(`/console/support/${ticket.id}`);
        }}>
          <Form.Item name="title" label="标题" rules={[{ required: true }]}>
            <Input placeholder="Workspace 无法打开" />
          </Form.Item>
          <Form.Item name="category" label="分类" initialValue="Workspace">
            <Input />
          </Form.Item>
          <Form.Item name="priority" label="优先级" initialValue="normal">
            <Input />
          </Form.Item>
          <Form.Item name="workspaceId" label="关联 Workspace">
            <Input list="workspaceIds" />
          </Form.Item>
          <datalist id="workspaceIds">
            {state.workspaces.map((workspace) => <option key={workspace.id} value={workspace.id}>{workspace.name}</option>)}
          </datalist>
          <Form.Item name="description" label="说明">
            <Input.TextArea rows={5} />
          </Form.Item>
          <Button type="primary" htmlType="submit">提交</Button>
        </Form>
      </ProCard>
    </PageContainer>
  );
}

function SupportTicketPage({ tickets }) {
  const id = window.location.pathname.split("/").at(-1);
  const ticket = tickets.tickets.find((item) => item.id === id);
  if (!ticket) return <PageContainer title="工单"><Empty description="未找到工单" /></PageContainer>;
  return (
    <PageContainer title={ticket.title} subTitle={ticket.id}>
      <ProCard gutter={16} wrap>
        <ProCard title="状态" colSpan={{ xs: 24, xl: 8 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="分类">{ticket.category}</Descriptions.Item>
            <Descriptions.Item label="优先级">{ticket.priority}</Descriptions.Item>
            <Descriptions.Item label="状态">{ticket.status}</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="对话" colSpan={{ xs: 24, xl: 16 }}>
          <Timeline items={ticket.messages.map((item) => ({ children: <><strong>{item.author}</strong><div>{item.text}</div></> }))} />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}

function AlertsPage({ state, tickets }) {
  const ticketAlerts = tickets.tickets.filter((ticket) => ticket.status !== "closed").map((ticket) => ({
    id: ticket.id,
    type: "support.ticket_open",
    accountId: ticket.title
  }));
  return (
    <PageContainer title="告警">
      <AlertList events={[...(state.notifications || []), ...ticketAlerts]} />
    </PageContainer>
  );
}

function ResourcesPage() {
  return (
    <PageContainer title="资源目录" subTitle="Approved connectors, environments, agents">
      <ProCard gutter={16} wrap>
        <CatalogCard title="连接器" items={["PubMed", "arXiv", "Zotero"]} />
        <CatalogCard title="环境" items={["Python/R", "Quarto/LaTeX", "CUDA"]} />
        <CatalogCard title="Agent 包" items={["Literature Review", "Grant Draft", "Figure Review"]} />
      </ProCard>
    </PageContainer>
  );
}

function ApprovalsPage() {
  return <PageContainer title="待审批"><Empty description="暂无审批事项" /></PageContainer>;
}

function ReceiptsPage({ state }) {
  return (
    <PageContainer title="回执中心">
      <Timeline items={(state.evidenceLedger || []).slice(-12).reverse().map((item) => ({
        children: <><strong>{item.type}</strong><div>{item.workspaceId || item.accountId}</div></>
      }))} />
    </PageContainer>
  );
}

function AdminOverviewPage({ state, adminOps }) {
  return (
    <PageContainer title="管理总览">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "账号", value: adminOps.operator?.accounts?.total ?? 1 }} />
        <StatisticCard statistic={{ title: "Workspace", value: adminOps.operator?.workspaces?.total ?? state.workspaces.length }} />
        <StatisticCard statistic={{ title: "失败操作", value: adminOps.operator?.runtimeOperations?.failed ?? 0 }} />
      </StatisticCard.Group>
    </PageContainer>
  );
}

function AdminUsersPage({ state, wallet, topUpOpen, setTopUpOpen, topUpForm, session, runAction }) {
  return (
    <PageContainer title="用户管理" extra={<Button icon={<Plus size={15} />} onClick={() => navigate("/admin/users/new")}>新建用户</Button>}>
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={[{
          id: session.user.id,
          email: session.user.email,
          role: session.user.role,
          accountId: state.account.id,
          balance: wallet.balance,
          frozen: wallet.frozen,
          status: "active"
        }]}
        columns={[
          { title: "用户", dataIndex: "email" },
          { title: "角色", dataIndex: "role", render: (value) => <Tag>{value}</Tag> },
          { title: "账号", dataIndex: "accountId" },
          { title: "余额", dataIndex: "balance", render: (value) => money(value) },
          { title: "状态", dataIndex: "status", render: (value) => <Tag color="green">{value}</Tag> },
          {
            title: "操作",
            valueType: "option",
            render: (_, row) => [
              <Button key="wallet" size="small" onClick={() => navigate(`/admin/users/${row.id}/wallet`)}>钱包</Button>,
              <Button key="topup" size="small" type="primary" onClick={() => {
                topUpForm.setFieldsValue({ accountId: row.accountId, amount: 200, reason: "commercial top-up" });
                setTopUpOpen(true);
              }}>充值</Button>
            ]
          }
        ]}
      />
      <TopUpDrawer open={topUpOpen} setOpen={setTopUpOpen} form={topUpForm} session={session} runAction={runAction} />
    </PageContainer>
  );
}

function TopUpDrawer({ open, setOpen, form, session, runAction }) {
  return (
    <Drawer title="用户钱包充值" open={open} onClose={() => setOpen(false)} width={420}>
      <Form form={form} layout="vertical" onFinish={(values) => runAction(() => api("/api/accounts/credit", values, session.csrfToken), "充值已记录").then(() => setOpen(false))}>
        <Form.Item name="accountId" label="账号" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="amount" label="金额" rules={[{ required: true }]}><InputNumber min={1} className="fullWidth" /></Form.Item>
        <Form.Item name="reason" label="原因"><Input /></Form.Item>
        <Button type="primary" htmlType="submit">确认充值</Button>
      </Form>
    </Drawer>
  );
}

function AdminBillingPage({ state }) {
  return (
    <PageContainer title="账务运营">
      <ProCard gutter={16} wrap>
        <ProCard title="手工充值记录"><TopupList events={state.manualTopups || []} /></ProCard>
        <ProCard title="钱包流水"><WalletList events={state.walletTransactions || []} /></ProCard>
      </ProCard>
    </PageContainer>
  );
}

function AdminFabricPage() {
  return (
    <PageContainer title="OPL Fabric">
      <ProCard gutter={16} wrap>
        <CatalogCard title="计算" items={["Standard CPU", "GPU reserved", "SSH/HPC adapter"]} />
        <CatalogCard title="存储" items={["Workspace volume", "Private bucket", "Institution storage"]} />
        <CatalogCard title="审批" items={["Connector", "Environment", "Agent package"]} />
      </ProCard>
    </PageContainer>
  );
}

function AdminLedgerPage({ state }) {
  return (
    <PageContainer title="OPL Ledger">
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={{ pageSize: 8 }}
        dataSource={state.billingLedger || []}
        columns={[
          { title: "事件", dataIndex: "type" },
          { title: "账号", dataIndex: "accountId", ellipsis: true },
          { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
          { title: "金额", dataIndex: "amount", render: (value) => money(value) }
        ]}
      />
    </PageContainer>
  );
}

function AdminRuntimePage({ adminOps }) {
  return (
    <PageContainer title="运行时">
      {adminOps.error && <Alert type="error" showIcon message={adminOps.error} />}
      <ProCard gutter={16} wrap>
        <ReadinessCard title="Fabric readiness" readiness={adminOps.runtime} />
        <ReadinessCard title="Launch gates" readiness={adminOps.launch} />
      </ProCard>
    </PageContainer>
  );
}

function AdminSupportPage({ tickets }) {
  return (
    <PageContainer title="工单管理">
      <ProTable
        rowKey="id"
        search={false}
        options={false}
        pagination={false}
        dataSource={tickets.tickets}
        columns={[
          { title: "标题", dataIndex: "title" },
          { title: "分类", dataIndex: "category" },
          { title: "状态", dataIndex: "status" },
          { title: "创建时间", dataIndex: "createdAt" }
        ]}
      />
    </PageContainer>
  );
}

function ForbiddenPage() {
  return <PageContainer title="无权限"><Empty description="当前账号无权访问该页面" /></PageContainer>;
}

function CatalogCard({ title, items }) {
  return (
    <ProCard title={title} colSpan={{ xs: 24, xl: 8 }}>
      <List size="small" dataSource={items} renderItem={(item) => <List.Item><Tag color="blue">Approved</Tag>{item}</List.Item>} />
    </ProCard>
  );
}

function UsageTable({ data, type }) {
  return (
    <ProTable
      rowKey={(row) => row.id}
      search={false}
      options={false}
      pagination={false}
      size="small"
      dataSource={data.slice(-8).reverse()}
      columns={[
        { title: type === "request" ? "请求" : "资源", dataIndex: type === "request" ? "requestId" : "resourceType", ellipsis: true },
        { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
        { title: "金额", dataIndex: "amount", render: (value) => money(value) }
      ]}
    />
  );
}

function AlertList({ events = [] }) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无告警" />;
  return (
    <List
      dataSource={events.slice(-8).reverse()}
      renderItem={(event) => (
        <List.Item>
          <Space>
            <AlertTriangle size={15} />
            <Typography.Text>{event.type || "alert"}</Typography.Text>
            <Typography.Text type="secondary">{event.workspaceId || event.accountId}</Typography.Text>
          </Space>
        </List.Item>
      )}
    />
  );
}

function TopupList({ events }) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无充值记录" />;
  return <List size="small" dataSource={events.slice(-8).reverse()} renderItem={(event) => <List.Item>{event.targetAccountId} · {money(event.amount)}</List.Item>} />;
}

function WalletList({ events }) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无钱包流水" />;
  return <List size="small" dataSource={events.slice(-8).reverse()} renderItem={(event) => <List.Item>{event.type} · {money(event.amount)}</List.Item>} />;
}

function ReadinessCard({ title, readiness }) {
  return (
    <ProCard title={title} colSpan={{ xs: 24, xl: 12 }}>
      <Tag color={readiness?.ready ? "green" : "red"}>{readiness?.ready ? "Ready" : "Blocked"}</Tag>
      <List
        size="small"
        dataSource={[...(readiness?.missingEnv || []), ...(readiness?.missingTools || []), ...(readiness?.failedChecks || [])]}
        locale={{ emptyText: "No blockers" }}
        renderItem={(item) => <List.Item>{item}</List.Item>}
      />
    </ProCard>
  );
}
