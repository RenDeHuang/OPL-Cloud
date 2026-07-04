import React from "react";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Typography } from "antd";
import { Plus } from "lucide-react";
import { manualTopUp } from "../../api/billing-api.js";
import { createUser } from "../../api/console-read-api.js";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill,
  TimelineList
} from "../shared/commercial-console.jsx";
import { money } from "../shared/formatters.js";

export function AdminOverviewPage({ state, adminOps }) {
  const failed = adminOps.operator?.runtimeOperations?.failed ?? 0;
  return (
    <ConsoleSurface title="Admin Overview" eyebrow="Operations" subtitle="Accounts, Workspaces, runtime evidence">
      <MetricStrip
        items={[
          { label: "账号", value: adminOps.operator?.accounts?.total ?? 1, caption: "managed billing accounts", tone: "info" },
          { label: "Workspace", value: adminOps.operator?.workspaces?.total ?? state.workspaces.length, caption: `${adminOps.operator?.workspaces?.running ?? 0} running`, tone: "good" },
          { label: "失败操作", value: failed, caption: "runtime operation failures", tone: failed ? "danger" : "good" },
          { label: "冻结总额", value: money(adminOps.operator?.accounts?.frozen), caption: "all accounts", tone: "warn" },
          { label: "告警", value: adminOps.operator?.notifications?.total ?? 0, caption: "operator visible", tone: adminOps.operator?.notifications?.error ? "danger" : "neutral" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="运行态" eyebrow="Runtime">
          <ResourceSplit
            items={[
              { label: "Ready", value: adminOps.runtime?.ready ? "Ready" : "Blocked", meta: "runtime readiness", status: adminOps.runtime?.ready ? "pass" : "check", tone: adminOps.runtime?.ready ? "good" : "warn" },
              { label: "Launch", value: adminOps.launch?.ready ? "Ready" : "Blocked", meta: "production launch gates", status: adminOps.launch?.ready ? "pass" : "check", tone: adminOps.launch?.ready ? "good" : "warn" },
              { label: "失败操作", value: failed, meta: "runtime operation queue", status: failed ? "needs triage" : "clear", tone: failed ? "danger" : "good" },
              { label: "计算分配", value: adminOps.operator?.computeAllocations?.total ?? adminOps.operator?.compute?.total ?? 0, meta: "CVM allocation evidence", status: "tracked", tone: "info" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="最近告警" eyebrow="Signals">
          <TimelineList
            emptyText="暂无运营告警"
            items={(adminOps.operator?.notifications?.recent || []).map((item) => ({
              title: item.type,
              description: item.workspaceId || item.accountId,
              meta: item.severity,
              tone: item.severity === "error" ? "danger" : "warn"
            }))}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}

export function AdminUsersPage({ managementState, topUpOpen, setTopUpOpen, topUpForm, session, runAction }) {
  const [createOpen, setCreateOpen] = React.useState(false);
  const [createForm] = Form.useForm();
  const accountsById = new Map((managementState.accounts || []).map((account) => [account.id, account]));
  const users = (managementState.users || []).map((user) => {
    const account = accountsById.get(user.accountId) || {};
    return {
      ...user,
      balance: account.balance ?? user.balance ?? 0,
      frozen: account.frozen ?? user.frozen ?? 0,
      totalRecharged: account.totalRecharged ?? user.totalRecharged ?? 0
    };
  });
  return (
    <ConsoleSurface
      title="Users"
      eyebrow="Admin"
      subtitle="Login users, billing accounts, and wallet operations"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => setCreateOpen(true)}>新建用户</Button>}
    >
      <InsightPanel title="用户钱包" eyebrow="Management">
        <ObjectTable
          rowKey="id"
          data={users}
          emptyText="暂无用户"
          columns={[
            { title: "用户", dataIndex: "email" },
            { title: "角色", dataIndex: "role", render: (value) => <StatusPill label={value} tone={value === "admin" ? "info" : "good"} /> },
            { title: "账号", dataIndex: "accountId", render: (value) => <Typography.Text className="inlineCode">{value}</Typography.Text> },
            { title: "余额", dataIndex: "balance", render: (value) => money(value) },
            { title: "冻结", dataIndex: "frozen", render: (value) => money(value) },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone="good" /> },
            {
              title: "操作",
              valueType: "option",
              render: (_, row) => (
                <ActionGroup actions={[
                  { label: "充值", type: "primary", onClick: () => {
                    topUpForm.setFieldsValue({ accountId: row.accountId, amount: 200, reason: "commercial top-up" });
                    setTopUpOpen(true);
                  } }
                ]} />
              )
            }
          ]}
        />
      </InsightPanel>
      <CreateUserDrawer open={createOpen} setOpen={setCreateOpen} form={createForm} session={session} runAction={runAction} />
      <TopUpDrawer open={topUpOpen} setOpen={setTopUpOpen} form={topUpForm} session={session} runAction={runAction} />
    </ConsoleSurface>
  );
}

function CreateUserDrawer({ open, setOpen, form, session, runAction }) {
  return (
    <Drawer title="新建登录用户" open={open} onClose={() => setOpen(false)} width={480}>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ role: "pi", initialBalance: 0 }}
        onFinish={async (values) => {
          const created = await runAction(
            () => createUser(values, session.csrfToken),
            "用户已创建"
          );
          if (created) {
            form.resetFields();
            setOpen(false);
          }
        }}
      >
        <Form.Item name="email" label="登录邮箱" rules={[{ required: true, message: "请输入邮箱" }, { type: "email", message: "邮箱格式不正确" }]}>
          <Input placeholder="owner@example.com" />
        </Form.Item>
        <Form.Item name="password" label="初始密码" rules={[{ required: true, message: "请输入初始密码" }]}>
          <Input.Password />
        </Form.Item>
        <Form.Item name="name" label="姓名">
          <Input placeholder="Lab Owner" />
        </Form.Item>
        <Form.Item name="role" label="角色" rules={[{ required: true, message: "请选择角色" }]}>
          <Select
            options={[
              { label: "Lab Owner", value: "pi" },
              { label: "Admin", value: "admin" }
            ]}
          />
        </Form.Item>
        <Form.Item name="accountId" label="账号 ID" rules={[{ required: true, message: "请输入账号 ID" }]}>
          <Input placeholder="acct-lab-alpha" />
        </Form.Item>
        <Form.Item name="initialBalance" label="初始余额">
          <InputNumber min={0} className="fullWidth" />
        </Form.Item>
        <Button type="primary" htmlType="submit">创建用户</Button>
      </Form>
    </Drawer>
  );
}

function TopUpDrawer({ open, setOpen, form, session, runAction }) {
  return (
    <Drawer title="用户钱包充值" open={open} onClose={() => setOpen(false)} width={420}>
      <Form form={form} layout="vertical" onFinish={async (values) => {
        const toppedUp = await runAction(() => manualTopUp(values, session.csrfToken), "充值已记录");
        if (toppedUp) setOpen(false);
      }}>
        <Form.Item name="accountId" label="账号" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="amount" label="金额" rules={[{ required: true }]}><InputNumber min={1} className="fullWidth" /></Form.Item>
        <Form.Item name="reason" label="原因"><Input /></Form.Item>
        <Button type="primary" htmlType="submit">确认充值</Button>
      </Form>
    </Drawer>
  );
}

export function AdminBillingPage({ state }) {
  return (
    <ConsoleSurface title="Billing Ops" eyebrow="Admin" subtitle="Manual top-ups and wallet transaction evidence">
      <div className="consoleGrid equal">
        <InsightPanel title="手工充值记录" eyebrow="Top-ups">
          <TimelineList
            emptyText="暂无充值记录"
            items={(state.manualTopups || []).slice(-8).reverse().map((event) => ({
              title: event.targetAccountId,
              description: event.reason || event.id,
              meta: money(event.amount),
              tone: "good"
            }))}
          />
        </InsightPanel>
        <InsightPanel title="钱包流水" eyebrow="Transactions">
          <TimelineList
            emptyText="暂无钱包流水"
            items={(state.walletTransactions || []).slice(-8).reverse().map((event) => ({
              title: event.type,
              description: event.accountId,
              meta: money(event.amount),
              tone: Number(event.amount || 0) < 0 ? "warn" : "good"
            }))}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}

export function AdminFabricPage() {
  return (
    <ConsoleSurface title="OPL Fabric" eyebrow="Admin" subtitle="Runtime resource boundary">
      <ResourceSplit
        items={[
          { label: "计算", value: "Standard CPU", meta: "GPU remains backlog until verified", status: "available", tone: "good" },
          { label: "存储", value: "StorageVolume", meta: "account-scoped volume resource", status: "available", tone: "good" },
          { label: "环境", value: "one-person-lab-app", meta: "current WebUI runtime", status: "current", tone: "info" }
        ]}
      />
    </ConsoleSurface>
  );
}

export function AdminLedgerPage({ state }) {
  return (
    <ConsoleSurface title="Ledger" eyebrow="Admin" subtitle="Billing ledger evidence">
      <InsightPanel title="账务事件" eyebrow="Evidence">
        <ObjectTable
          rowKey="id"
          pagination={{ pageSize: 8 }}
          data={state.billingLedger || []}
          emptyText="暂无账务事件"
          columns={[
            { title: "事件", dataIndex: "type" },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "Workspace", dataIndex: "workspaceId", ellipsis: true },
            { title: "金额", dataIndex: "amount", render: (value) => money(value) }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminRuntimePage({ adminOps }) {
  const blockers = [
    ...(adminOps.runtime?.missingEnv || []),
    ...(adminOps.runtime?.missingTools || []),
    ...(adminOps.runtime?.failedChecks || []),
    ...(adminOps.launch?.missingEnv || []),
    ...(adminOps.launch?.missingTools || []),
    ...(adminOps.launch?.failedChecks || [])
  ];
  return (
    <ConsoleSurface title="Runtime" eyebrow="Admin" subtitle="Readiness gates and launch blockers">
      {adminOps.error && <Alert type="error" showIcon message={adminOps.error} />}
      <div className="consoleGrid equal">
        <InsightPanel title="Readiness" eyebrow="Runtime">
          <ResourceSplit
            items={[
              { label: "Fabric", value: adminOps.runtime?.ready ? "Ready" : "Blocked", meta: "runtime provider", status: adminOps.runtime?.ready ? "pass" : "check", tone: adminOps.runtime?.ready ? "good" : "warn" },
              { label: "Launch", value: adminOps.launch?.ready ? "Ready" : "Blocked", meta: "production gates", status: adminOps.launch?.ready ? "pass" : "check", tone: adminOps.launch?.ready ? "good" : "warn" },
              { label: "Env", value: (adminOps.launch?.missingEnv || []).length, meta: "missing environment inputs", status: "env", tone: (adminOps.launch?.missingEnv || []).length ? "warn" : "good" },
              { label: "Tools", value: (adminOps.launch?.missingTools || []).length, meta: "host tool checks", status: "tools", tone: (adminOps.launch?.missingTools || []).length ? "warn" : "good" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="Blockers" eyebrow="Checks">
          <TimelineList
            emptyText="No blockers"
            items={blockers.map((item) => ({
              title: item,
              description: "readiness check",
              tone: "warn"
            }))}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}

export function AdminSupportPage({ tickets }) {
  return (
    <ConsoleSurface title="Support Ops" eyebrow="Admin" subtitle="All visible support tickets">
      <InsightPanel title="工单队列" eyebrow="Support">
        <ObjectTable
          rowKey="id"
          data={tickets.tickets}
          emptyText="暂无工单"
          columns={[
            { title: "标题", dataIndex: "title" },
            { title: "分类", dataIndex: "category" },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "closed" ? "neutral" : "good"} /> },
            { title: "创建时间", dataIndex: "createdAt" }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
