import React from "react";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Typography } from "antd";
import { Plus } from "lucide-react";
import { manualTopUp, recordBillingReconciliation, settleResourceBilling } from "../../api/billing-api.ts";
import { addOrganizationMember, archiveTerminalResources, cleanupWorkspaceAccess, createOrganization, createUser, deleteUser, disableUser } from "../../api/console-read-api.ts";
import {
  ActionGroup,
  CleanupResourceTable,
  ConsoleSurface,
  DataRetentionPolicyPanel,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  OperationConfirmButton,
  ProductionE2EPanel,
  ResourceSplit,
  StatusPill,
  TimelineList
} from "../shared/commercial-console.tsx";
import { money, moneyValue } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function roleLabel(role = "") {
  return {
    admin: "运维",
    pi: "用户",
    member: "成员",
    owner: "用户"
  }[role] || role || "用户";
}

function manualTopUpIdempotencyKey() {
  return `manual-topup-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function totalDebited(transactions: AnyRecord[] = []) {
  return transactions.reduce((sum, event) => {
    const amount = Number(event.amount || 0);
    return amount < 0 ? sum + Math.abs(amount) : sum;
  }, 0);
}

function firstMessage(ticket: AnyRecord = {}) {
  return (ticket.messages || []).map((message) => message.text).filter(Boolean).join(" / ");
}

function priceSnapshotLabel(row: AnyRecord = {}) {
  const snapshot = row.priceSnapshot || {};
  return [
    row.pricingVersion,
    snapshot.unitPriceCents != null ? `unitPriceCents ${snapshot.unitPriceCents}` : "",
    row.quantity && row.unit ? `${row.quantity} ${row.unit}` : "",
    row.providerCostEvidenceRef
  ].filter(Boolean).join(" · ") || "-";
}

function walletAfterLabel(row: AnyRecord = {}) {
  return [
    row.balanceCents != null ? `balanceCents ${row.balanceCents}` : "",
    row.frozenCents != null ? `frozenCents ${row.frozenCents}` : "",
    row.availableCents != null ? `availableCents ${row.availableCents}` : "",
    row.totalSpentCents != null ? `totalSpentCents ${row.totalSpentCents}` : ""
  ].filter(Boolean).join(" · ") || "-";
}

function costTagsLabel(tags: AnyRecord = {}) {
  return [tags.opl_account_id, tags.opl_workspace_id, tags.opl_resource_id, tags.opl_operation_id].filter(Boolean).join(" · ") || "-";
}

export function AdminOverviewPage({ state, managementState = {}, adminOps }: any) {
  const failed = adminOps.operator?.runtimeOperations?.failed ?? 0;
  const totalSpent = adminOps.operator?.accounts?.totalSpent ?? totalDebited(state.walletTransactions || state.billingLedger || []);
  const usersByAccount = new Map<string, AnyRecord>((managementState.users || []).map((user) => [user.accountId, user]));
  const spendRows = (managementState.accounts || []).slice(0, 8);
  const resourceLedgerEvidence = managementState.resourceLedgerEvidence || [];
  const resourceAnomalies = adminOps.operator?.resourceAnomalies || [];
  const failedOperations = adminOps.operator?.failedOperations || adminOps.operator?.runtimeOperations?.recentFailed || [];
  const statusRows = [
    ...resourceAnomalies.map((item) => ({ ...item, kind: item.type || "资源异常" })),
    ...failedOperations.map((item) => ({ ...item, kind: item.operationType || "失败操作" }))
  ].slice(0, 8);
  return (
    <ConsoleSurface title="管理概览" eyebrow="运营" subtitle="谁花了钱、买了什么、现在怎样">
      <MetricStrip
        items={[
          { label: "账号", value: adminOps.operator?.accounts?.total ?? 0, caption: "托管计费账号", tone: "info" },
          { label: "买了什么", value: resourceLedgerEvidence.length || state.workspaces.length, caption: "资源和工作区", tone: "good" },
          { label: "已消费金额", value: money(totalSpent), caption: "debited / totalSpent", tone: totalSpent ? "warn" : "neutral" },
          { label: "现在怎样", value: resourceAnomalies.length + failed, caption: "异常和失败", tone: resourceAnomalies.length + failed ? "danger" : "good" },
          { label: "冻结总额", value: money(adminOps.operator?.accounts?.frozen), caption: "全部账号", tone: "warn" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="谁花了钱" eyebrow="账号">
          <ObjectTable
            rowKey="id"
            data={spendRows}
            emptyText="暂无账号"
            columns={[
              { title: "用户", render: (_, row) => usersByAccount.get(row.id)?.email || row.ownerEmail || "-" },
              { title: "账号", dataIndex: "id", ellipsis: true },
              { title: "已花费", render: (_, row) => money(row.totalSpent ?? row.totalSpentCents ?? 0) },
              { title: "余额", dataIndex: "balance", render: (value) => money(value) },
              { title: "冻结", dataIndex: "frozen", render: (value) => money(value) }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="买了什么" eyebrow="资源">
          <ObjectTable
            rowKey={(row) => row.id || `${row.resourceType}-${row.workspaceId}`}
            data={resourceLedgerEvidence.slice(0, 8)}
            emptyText="暂无资源记录"
            columns={[
              { title: "资源", dataIndex: "resourceType", ellipsis: true },
              { title: "账号", render: (_, row) => row.ownerAccountId || row.accountId || "-" },
              { title: "工作区", render: (_, row) => (row.workspaceIds || [row.workspaceId]).filter(Boolean).join(", ") || "-" },
              { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "tracked"} tone={value === "failed" ? "danger" : "info"} /> }
            ]}
          />
        </InsightPanel>
      </div>
      <InsightPanel title="现在怎样" eyebrow="状态">
        <ObjectTable
          rowKey={(row) => row.id || `${row.kind}-${row.workspaceId}-${row.resourceId}`}
          data={statusRows}
          emptyText="暂无异常或失败操作"
          columns={[
            { title: "类型", dataIndex: "kind", ellipsis: true },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "资源", dataIndex: "resourceId", ellipsis: true },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "pending"} tone={value === "failed" ? "danger" : "warn"} /> }
          ]}
          />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminUsersPage({ managementState, topUpOpen, setTopUpOpen, topUpForm, session, runAction }: any) {
  const [createOpen, setCreateOpen] = React.useState(false);
  const [organizationOpen, setOrganizationOpen] = React.useState(false);
  const [memberOpen, setMemberOpen] = React.useState(false);
  const [createForm] = Form.useForm();
  const [organizationForm] = Form.useForm();
  const [memberForm] = Form.useForm();
  const accountsById = new Map<string, AnyRecord>((managementState.accounts || []).map((account) => [account.id, account]));
  const users = (managementState.users || []).filter((user) => user.status !== "deleted").map((user) => {
    const account = accountsById.get(user.accountId) || {};
    return {
      ...user,
      balance: account.balance ?? user.balance ?? 0,
      frozen: account.frozen ?? user.frozen ?? 0,
      totalRecharged: account.totalRecharged ?? user.totalRecharged ?? 0
    };
  });
  const activeUsers = users.filter((user) => !["disabled", "deleted"].includes(user.status)).length;
  return (
    <ConsoleSurface
      title="用户"
      eyebrow="管理"
      subtitle="登录用户、组织成员、计费账号、钱包操作"
      extra={(
        <ActionGroup actions={[
          { label: "新建组织", onClick: () => setOrganizationOpen(true) },
          { label: "添加成员", onClick: () => setMemberOpen(true) },
          { label: "新建用户", type: "primary", icon: <Plus size={15} />, onClick: () => setCreateOpen(true) }
        ]} />
      )}
    >
      <InsightPanel title="组织关系" eyebrow="组织">
        <ObjectTable
          rowKey="id"
          data={managementState.memberships || []}
          emptyText="暂无组织成员"
          columns={[
            { title: "组织", dataIndex: "organizationId", ellipsis: true },
            { title: "用户", dataIndex: "userId", ellipsis: true },
            { title: "角色", dataIndex: "role", render: (value) => <StatusPill label={roleLabel(value || "member")} tone="info" /> },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "active"} tone={value === "active" ? "good" : "warn"} /> }
          ]}
        />
      </InsightPanel>
      <InsightPanel title="用户钱包" eyebrow="管理">
        <MetricStrip
          items={[
            { label: "用户", value: users.length, caption: "登录用户", tone: users.length ? "info" : "neutral" },
            { label: "可登录", value: activeUsers, caption: "未禁用/删除", tone: activeUsers ? "good" : "warn" },
            { label: "禁用", value: users.filter((user) => user.status === "disabled").length, caption: "不可登录", tone: "warn" },
            { label: "删除", value: users.filter((user) => user.status === "deleted").length, caption: "资源和账单保留", tone: "danger" }
          ]}
        />
        <ObjectTable
          rowKey="id"
          data={users}
          emptyText="暂无用户"
          columns={[
            { title: "用户", dataIndex: "email" },
            { title: "角色", dataIndex: "role", render: (value) => <StatusPill label={roleLabel(value)} tone={value === "admin" ? "info" : "good"} /> },
            { title: "账号", dataIndex: "accountId", render: (value) => <Typography.Text className="inlineCode">{value}</Typography.Text> },
            { title: "余额", dataIndex: "balance", render: (value) => money(value) },
            { title: "冻结", dataIndex: "frozen", render: (value) => money(value) },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "active" ? "good" : value === "deleted" ? "danger" : "warn"} /> },
            {
              title: "操作",
              valueType: "option",
              render: (_, row) => (
                <ActionGroup actions={[
                  { label: "充值", type: "primary", onClick: () => {
                    topUpForm.setFieldsValue({ accountId: row.accountId, amount: undefined, reason: "" });
                    setTopUpOpen(true);
                  } },
                  <OperationConfirmButton
                    key="disable"
                    label="禁用"
                    title="确认禁用用户"
                    description="禁用后该用户不能登录；资源、账单、工作区入口保留。"
                    disabled={row.status !== "active"}
                    onConfirm={() => runAction(
                      () => disableUser({ userId: row.id, reason: "admin_disabled" }, session.csrfToken),
                      "用户已禁用",
                      { actionKey: `admin-disable-${row.id}` }
                    )}
                  />,
                  <OperationConfirmButton
                    key="delete"
                    label="删除"
                    title="确认删除登录用户"
                    description="删除后用户不能登录；资源和账单保留，便于审计和后续清理。"
                    danger
                    disabled={row.status === "deleted"}
                    onConfirm={() => runAction(
                      () => deleteUser({ userId: row.id, reason: "admin_deleted", confirm: true }, session.csrfToken),
                      "用户已删除",
                      { actionKey: `admin-delete-${row.id}` }
                    )}
                  />
                ]} />
              )
            }
          ]}
        />
      </InsightPanel>
      <CreateUserDrawer open={createOpen} setOpen={setCreateOpen} form={createForm} session={session} runAction={runAction} />
      <CreateOrganizationDrawer open={organizationOpen} setOpen={setOrganizationOpen} form={organizationForm} session={session} runAction={runAction} />
      <AddOrganizationMemberDrawer open={memberOpen} setOpen={setMemberOpen} form={memberForm} session={session} runAction={runAction} users={users} organizations={managementState.organizations || []} />
      <TopUpDrawer open={topUpOpen} setOpen={setTopUpOpen} form={topUpForm} session={session} runAction={runAction} />
    </ConsoleSurface>
  );
}

function CreateOrganizationDrawer({ open, setOpen, form, session, runAction }: any) {
  return (
    <Drawer title="新建组织" open={open} onClose={() => setOpen(false)} width={420}>
      <Form
        form={form}
        layout="vertical"
        onFinish={async (values) => {
          const created = await runAction(
            () => createOrganization(values, session.csrfToken),
            "组织已创建"
          );
          if (created) {
            form.resetFields();
            setOpen(false);
          }
        }}
      >
        <Form.Item name="name" label="组织名称" rules={[{ required: true, message: "请输入组织名称" }]}>
          <Input placeholder="实验室 / 团队名称" />
        </Form.Item>
        <Form.Item name="billingAccountId" label="计费账号" rules={[{ required: true, message: "请输入计费账号" }]}>
          <Input placeholder="acct-lab-alpha" />
        </Form.Item>
        <Button type="primary" htmlType="submit">创建组织</Button>
      </Form>
    </Drawer>
  );
}

function AddOrganizationMemberDrawer({ open, setOpen, form, session, runAction, users = [], organizations = [] }: any) {
  return (
    <Drawer title="添加组织成员" open={open} onClose={() => setOpen(false)} width={420}>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ role: "member" }}
        onFinish={async (values) => {
          const added = await runAction(
            () => addOrganizationMember(values, session.csrfToken),
            "组织成员已添加"
          );
          if (added) {
            form.resetFields();
            setOpen(false);
          }
        }}
      >
        <Form.Item name="organizationId" label="组织" rules={[{ required: true, message: "请选择组织" }]}>
          <Select
            showSearch
            options={organizations.map((organization) => ({
              label: `${organization.name || organization.id} · ${organization.billingAccountId || ""}`,
              value: organization.id
            }))}
          />
        </Form.Item>
        <Form.Item name="userId" label="用户" rules={[{ required: true, message: "请选择用户" }]}>
          <Select
            showSearch
            options={users.map((user) => ({
              label: `${user.email || user.id} · ${user.accountId || ""}`,
              value: user.id
            }))}
          />
        </Form.Item>
        <Form.Item name="role" label="成员角色" rules={[{ required: true, message: "请输入成员角色" }]}>
          <Input placeholder="member" />
        </Form.Item>
        <Button type="primary" htmlType="submit">添加成员</Button>
      </Form>
    </Drawer>
  );
}

function CreateUserDrawer({ open, setOpen, form, session, runAction }: any) {
  return (
    <Drawer title="新建登录用户" open={open} onClose={() => setOpen(false)} width={480}>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ role: "pi", initialBalance: 0 }}
        onFinish={async (values) => {
          const created = await runAction(
            () => createUser(values, session.csrfToken),
            "用户已创建",
            { actionKey: `admin-create-user-${String(values.email || "").toLowerCase().trim()}` }
          );
          if (created) {
            form.resetFields();
            setOpen(false);
          }
        }}
      >
        <Form.Item name="email" label="登录邮箱" rules={[{ required: true, message: "请输入邮箱" }, { type: "email", message: "邮箱格式不正确" }]}>
          <Input placeholder="admin@medopl.cn" />
        </Form.Item>
        <Form.Item name="password" label="初始密码" rules={[{ required: true, message: "请输入初始密码" }]}>
          <Input.Password />
        </Form.Item>
        <Form.Item name="name" label="姓名">
          <Input placeholder="用户姓名" />
        </Form.Item>
        <Form.Item name="role" label="角色" rules={[{ required: true, message: "请选择角色" }]}>
          <Select
            options={[
              { label: "用户", value: "pi" },
              { label: "运维", value: "admin" }
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

function TopUpDrawer({ open, setOpen, form, session, runAction }: any) {
  const topUpOperationKey = React.useRef("");
  React.useEffect(() => {
    if (open && !topUpOperationKey.current) topUpOperationKey.current = manualTopUpIdempotencyKey();
    if (!open) topUpOperationKey.current = "";
  }, [open]);
  return (
    <Drawer title="用户钱包充值" open={open} onClose={() => setOpen(false)} width={420}>
      <Form form={form} layout="vertical" onFinish={async (values) => {
        if (!topUpOperationKey.current) topUpOperationKey.current = manualTopUpIdempotencyKey();
        const toppedUp = await runAction(
          () => manualTopUp({
            ...values,
            operatorUserId: session.user?.id || "",
            operatorAccountId: session.user?.accountId || "",
            idempotencyKey: topUpOperationKey.current,
            confirm: true
          }, session.csrfToken),
          "充值已记录",
          { actionKey: "admin-manual-topup" }
        );
        if (toppedUp) {
          topUpOperationKey.current = "";
          setOpen(false);
        }
      }}>
        <Form.Item name="accountId" label="账号" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="amount" label="金额" rules={[{ required: true }]}><InputNumber min={1} className="fullWidth" /></Form.Item>
        <Form.Item name="reason" label="原因"><Input /></Form.Item>
        <Button type="primary" htmlType="submit">确认充值</Button>
      </Form>
    </Drawer>
  );
}

export function AdminBillingPage({ state, managementState, adminOps, session, runAction }: any) {
  const [reconciliationOpen, setReconciliationOpen] = React.useState(false);
  const [reconciliationForm] = Form.useForm();
  const totalSpent = adminOps.operator?.accounts?.totalSpent ?? totalDebited(state.walletTransactions || state.billingLedger || []);
  const reconciliationGuard = state.billingReconciliation?.guard || adminOps.operator?.billingReconciliation?.guard || {};
  return (
    <ConsoleSurface
      title="账单运营"
      eyebrow="管理"
      subtitle="人工充值、资源结算、对账保护、钱包流水证据"
      extra={(
        <ActionGroup actions={[
          <OperationConfirmButton
            key="settle"
            label="触发资源结算"
            title="确认触发资源结算"
            description="按当前活跃计算和存储生成资源扣费；释放冻结只降低 frozen，使可用余额增加，不会额外增加 balance。"
            onConfirm={() => runAction(
              () => settleResourceBilling({
                hours: 1,
                sourceEventId: `admin_resource_settlement:${Date.now()}`,
                confirm: true
              }, session.csrfToken),
              "资源结算已记录",
              { actionKey: "admin-resource-settlement" }
            )}
          />,
          { label: "记录对账", type: "primary", onClick: () => setReconciliationOpen(true) }
        ]} />
      )}
    >
      <MetricStrip
        items={[
          { label: "已消费金额", value: money(totalSpent), caption: "totalSpent / debited", tone: totalSpent ? "warn" : "neutral" },
          { label: "最近对账", value: reconciliationGuard.status || "not_required", caption: reconciliationGuard.reason || "阻塞原因：无", tone: reconciliationGuard.blockNewWorkspaces ? "danger" : "good" },
          { label: "冻结释放", value: "可用余额增加", caption: "释放冻结降低 frozen，不增加 balance", tone: "info" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="手工充值记录" eyebrow="充值">
          <TimelineList
            emptyText="暂无充值记录"
            items={(state.manualTopups || []).slice(-8).reverse().map((event) => ({
              title: event.targetAccountId,
              description: [
                event.reason || event.id,
                event.operatorUserId || event.operatorAccountId ? `operator ${event.operatorUserId || event.operatorAccountId}` : "",
                event.ledgerEntryId ? `ledgerEntryId ${event.ledgerEntryId}` : "",
                event.walletTransactionId ? `walletTransactionId ${event.walletTransactionId}` : "",
                event.balanceBefore != null ? `${money(event.balanceBefore)} -> ${money(event.balanceAfter)}` : ""
              ].filter(Boolean).join(" · "),
              meta: money(event.amount),
              tone: "good"
            }))}
          />
        </InsightPanel>
        <InsightPanel title="钱包流水" eyebrow="流水">
          <TimelineList
            emptyText="暂无钱包流水"
            items={(state.walletTransactions || []).slice(-8).reverse().map((event) => ({
              title: event.type,
              description: `${event.accountId || ""} · ${walletAfterLabel(event)}`,
              meta: money(event.amount),
              tone: Number(event.amount || 0) < 0 ? "warn" : "good"
            }))}
          />
        </InsightPanel>
        <InsightPanel title="操作审计" eyebrow="审计">
          <TimelineList
            emptyText="暂无操作审计"
            items={(managementState.auditEvents || []).slice(-8).reverse().map((event) => ({
              title: event.action,
              description: [
                event.targetAccountId || event.actorAccountId,
                event.resourceKind && event.resourceId ? `${event.resourceKind} ${event.resourceId}` : "",
                event.actorUserId ? `actor ${event.actorUserId}` : "",
                event.result
              ].filter(Boolean).join(" · "),
              meta: event.createdAt,
              tone: event.result === "succeeded" ? "good" : "warn"
            }))}
          />
        </InsightPanel>
      </div>
      <ReconciliationDrawer open={reconciliationOpen} setOpen={setReconciliationOpen} form={reconciliationForm} session={session} runAction={runAction} />
    </ConsoleSurface>
  );
}

function ReconciliationDrawer({ open, setOpen, form, session, runAction }: any) {
  return (
    <Drawer title="记录账务对账" open={open} onClose={() => setOpen(false)} width={520}>
      <Form
        form={form}
        layout="vertical"
        initialValues={{ status: "ok", reason: "operator_reconciliation", blockNewWorkspaces: false }}
        onFinish={async (values) => {
          const recorded = await runAction(
            () => recordBillingReconciliation({
              source: "operator",
              confirm: true,
              report: {
                id: `operator-reconciliation-${Date.now()}`,
                generatedAt: new Date().toISOString(),
                guard: {
                  status: values.status,
                  reason: values.reason,
                  blockNewWorkspaces: values.blockNewWorkspaces === true
                }
              }
            }, session.csrfToken),
            "对账已记录",
            { actionKey: "admin-billing-reconciliation" }
          );
          if (recorded) {
            form.resetFields();
            setOpen(false);
          }
        }}
      >
        <Form.Item name="status" label="对账状态" rules={[{ required: true }]}>
          <Select
            options={[
              { label: "通过", value: "ok" },
              { label: "阻塞", value: "blocked" },
              { label: "需复核", value: "review_required" }
            ]}
          />
        </Form.Item>
        <Form.Item name="reason" label="阻塞原因 / 处理说明" rules={[{ required: true }]}>
          <Input.TextArea rows={4} placeholder="写清楚最近对账结果、阻塞原因或处理入口" />
        </Form.Item>
        <Form.Item name="blockNewWorkspaces" label="是否阻塞新工作区">
          <Select
            options={[
              { label: "不阻塞", value: false },
              { label: "阻塞", value: true }
            ]}
          />
        </Form.Item>
        <Button type="primary" htmlType="submit">记录对账</Button>
      </Form>
    </Drawer>
  );
}

export function AdminFabricPage() {
  return (
    <ConsoleSurface title="OPL Fabric" eyebrow="管理" subtitle="运行资源边界">
      <ResourceSplit
        items={[
          { label: "计算", value: "标准 CPU", meta: "GPU 验证前不进入当前上线范围", status: "可用", tone: "good" },
          { label: "存储", value: "存储资源", meta: "账号范围数据盘", status: "可用", tone: "good" },
          { label: "环境", value: "one-person-lab-app", meta: "当前 WebUI 运行时", status: "当前", tone: "info" }
        ]}
      />
    </ConsoleSurface>
  );
}

export function AdminLedgerPage({ state }: any) {
  return (
    <ConsoleSurface title="账本" eyebrow="管理" subtitle="账单证据">
      <InsightPanel title="账务事件" eyebrow="证据">
        <ObjectTable
          rowKey="id"
          pagination={{ pageSize: 8 }}
          data={state.billingLedger || []}
          emptyText="暂无账务事件"
          columns={[
            { title: "事件", dataIndex: "type" },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "价格快照", render: (_, row) => <Typography.Text className="inlineCode">{priceSnapshotLabel(row)}</Typography.Text> },
            { title: "成本证据", dataIndex: "providerCostEvidenceRef", ellipsis: true },
            { title: "金额", render: (_, row) => money(moneyValue(row)) }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminRuntimePage({ adminOps }: any) {
  const blockers = [
    ...(adminOps.runtime?.missingEnv || []),
    ...(adminOps.runtime?.missingTools || []),
    ...(adminOps.runtime?.failedChecks || []),
    ...(adminOps.launch?.missingEnv || []),
    ...(adminOps.launch?.missingTools || []),
    ...(adminOps.launch?.failedChecks || [])
  ];
  return (
    <ConsoleSurface title="运行状态" eyebrow="管理" subtitle="就绪门禁和上线阻塞">
      {adminOps.error && <Alert type="error" showIcon message={adminOps.error} />}
      <div className="consoleGrid equal">
        <InsightPanel title="就绪状态" eyebrow="运行">
          <ResourceSplit
            items={[
              { label: "Fabric", value: adminOps.runtime?.ready ? "就绪" : "阻塞", meta: "运行提供方", status: adminOps.runtime?.ready ? "通过" : "检查", tone: adminOps.runtime?.ready ? "good" : "warn" },
              { label: "上线", value: adminOps.launch?.ready ? "就绪" : "阻塞", meta: "生产门禁", status: adminOps.launch?.ready ? "通过" : "检查", tone: adminOps.launch?.ready ? "good" : "warn" },
              { label: "环境", value: (adminOps.launch?.missingEnv || []).length, meta: "缺少环境输入", status: "环境", tone: (adminOps.launch?.missingEnv || []).length ? "warn" : "good" },
              { label: "工具", value: (adminOps.launch?.missingTools || []).length, meta: "主机工具检查", status: "工具", tone: (adminOps.launch?.missingTools || []).length ? "warn" : "good" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="阻塞项" eyebrow="检查">
          <TimelineList
            emptyText="暂无阻塞项"
            items={blockers.map((item) => ({
              title: item,
              description: "就绪检查",
              tone: "warn"
            }))}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}

export function AdminDiagnosticsPage({ managementState, adminOps }: any) {
  const failedOperations = adminOps.operator?.failedOperations || adminOps.operator?.runtimeOperations?.recentFailed || [];
  const resourceAnomalies = adminOps.operator?.resourceAnomalies || [];
  const resourceEvidence = managementState.resourceLedgerEvidence || [];
  return (
    <ConsoleSurface title="线上诊断" eyebrow="管理" subtitle="只读检查、失败操作、资源异常">
      {adminOps.error && <Alert type="error" showIcon message={adminOps.error} />}
      <div className="consoleGrid equal">
        <InsightPanel title="生产门禁" eyebrow="只读">
          <ResourceSplit
            items={[
              { label: "运行就绪", value: adminOps.runtime?.ready ? "就绪" : "阻塞", meta: "runtime readiness", status: adminOps.runtime?.ready ? "通过" : "检查", tone: adminOps.runtime?.ready ? "good" : "warn" },
              { label: "上线就绪", value: adminOps.launch?.ready ? "就绪" : "阻塞", meta: "production readiness", status: adminOps.launch?.ready ? "通过" : "检查", tone: adminOps.launch?.ready ? "good" : "warn" },
              { label: "失败操作", value: failedOperations.length, meta: "failedOperations", status: failedOperations.length ? "待处理" : "清空", tone: failedOperations.length ? "danger" : "good" },
              { label: "资源异常", value: resourceAnomalies.length, meta: "resourceAnomalies", status: resourceAnomalies.length ? "待处理" : "清空", tone: resourceAnomalies.length ? "danger" : "good" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="失败操作" eyebrow="操作">
          <TimelineList
            emptyText="暂无失败操作"
            items={failedOperations.map((operation) => ({
              title: operation.operationType || operation.id,
              description: operation.accountId || operation.workspaceId || operation.resourceId,
              meta: operation.status || operation.updatedAt,
              tone: "danger"
            }))}
          />
        </InsightPanel>
      </div>
      <InsightPanel title="资源异常" eyebrow="资源">
        <ObjectTable
          rowKey={(row) => `${row.type}-${row.workspaceId}-${row.status}`}
          data={resourceAnomalies}
          emptyText="暂无资源异常"
          columns={[
            { title: "类型", dataIndex: "type" },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone="danger" /> }
          ]}
        />
      </InsightPanel>
      <InsightPanel title="资源归属证据" eyebrow="Owner、CVM、存储">
        <ObjectTable
          rowKey="id"
          data={resourceEvidence}
          emptyText="暂无资源归属证据"
          tableLayout="fixed"
          scroll={{ x: 1680 }}
          columns={[
            { title: "资源", dataIndex: "resourceType", width: 90, ellipsis: true },
            { title: "Workspace", dataIndex: "workspaceIds", width: 190, ellipsis: true, render: (value, row) => <Typography.Text copyable className="inlineCode">{(value || [row.workspaceId]).filter(Boolean).join(", ") || "-"}</Typography.Text> },
            { title: "Workspace URL", dataIndex: "workspaceUrl", width: 210, ellipsis: true, render: (value) => <Typography.Text copyable className="inlineCode">{value || "-"}</Typography.Text> },
            { title: "Owner", dataIndex: "ownerAccountId", width: 150, ellipsis: true, render: (value, row) => value || row.accountId || "-" },
            { title: "用户邮箱", dataIndex: "ownerEmail", width: 180, ellipsis: true },
            { title: "User", dataIndex: "ownerUserId", width: 150, ellipsis: true },
            { title: "CVM / 节点", dataIndex: "cvmInstanceId", width: 190, ellipsis: true, render: (value) => <Typography.Text copyable className="inlineCode">{value || "-"}</Typography.Text> },
            { title: "Node", dataIndex: "nodeName", width: 170, ellipsis: true },
            { title: "计算 ID", dataIndex: "computeAllocationId", width: 170, ellipsis: true, render: (value, row) => value || row.computeId || "-" },
            { title: "存储 ID", dataIndex: "storageId", width: 170, ellipsis: true },
            { title: "挂载 ID", dataIndex: "attachmentId", width: 170, ellipsis: true },
            { title: "存储 provider", dataIndex: "storageProviderId", width: 190, ellipsis: true, render: (value) => <Typography.Text copyable className="inlineCode">{value || "-"}</Typography.Text> },
            { title: "账本", dataIndex: "ledgerEntryIds", width: 190, ellipsis: true, render: (value) => (value || []).join(", ") || "-" },
            { title: "钱包流水", dataIndex: "walletTransactionIds", width: 190, ellipsis: true, render: (value) => (value || []).join(", ") || "-" },
            { title: "Operation", dataIndex: "operationId", width: 170, ellipsis: true },
            { title: "Cost tags", dataIndex: "costTags", width: 260, ellipsis: true, render: (value) => <Typography.Text className="inlineCode">{costTagsLabel(value)}</Typography.Text> },
            { title: "状态", dataIndex: "status", width: 95, render: (value) => <StatusPill label={value} tone={value === "failed" ? "danger" : "info"} /> },
            { title: "问题依据", dataIndex: "issue", width: 190, ellipsis: true },
            { title: "请求/操作", dataIndex: "providerRequestId", width: 170, ellipsis: true }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminE2EPage({ adminOps }: any) {
  return (
    <ConsoleSurface title="E2E记录" eyebrow="管理" subtitle="真实上线验证摘要">
      <ProductionE2EPanel summary={adminOps.operator?.productionE2E || {}} />
      <DataRetentionPolicyPanel />
    </ConsoleSurface>
  );
}

export function AdminCleanupPage({ managementState, session, runAction }: any) {
  const activeWorkspaces = (managementState.workspaces || []).filter((workspace) => workspace.access?.tokenStatus === "active");
  const storageById = new Map<string, AnyRecord>((managementState.storageVolumes || []).map((storage) => [storage.id, storage]));
  const localCleanupCandidateCount = activeWorkspaces.filter((workspace) => {
    const storageId = String(workspace.storageId || "").trim();
    const storage = storageId ? storageById.get(storageId) : null;
    return !workspace.ownerAccountId ||
      !storageId ||
      !storage ||
      storage.status === "destroyed" ||
      storage.billingStatus === "stopped";
  }).length;
  const cleanupSummary = managementState.workspaceAccessCleanup || {};
  const cleanupCandidateCount = Number(cleanupSummary.cleanupCandidateCount ?? localCleanupCandidateCount);
  const activeUrlCount = Number(cleanupSummary.activeUrlCount ?? activeWorkspaces.length);
  const destroyedCompute = Number(cleanupSummary.destroyedComputeCount ?? (managementState.computeAllocations || []).filter((item) => item.status === "destroyed").length);
  const destroyedStorage = Number(cleanupSummary.destroyedStorageCount ?? (managementState.storageVolumes || []).filter((item) => item.status === "destroyed").length);
  const detachedAttachments = Number(cleanupSummary.detachedAttachmentCount ?? (managementState.storageAttachments || []).filter((item) => item.status === "detached").length);
  return (
    <ConsoleSurface title="入口清理" eyebrow="管理" subtitle="清理已失效资源对应的访问 URL">
      <MetricStrip
        items={[
          { label: "可用 URL", value: activeUrlCount, caption: "候选工作区入口", tone: activeUrlCount ? "warn" : "good" },
          { label: "已销毁计算", value: destroyedCompute, caption: "已停止分配", tone: destroyedCompute ? "info" : "neutral" },
          { label: "已销毁存储", value: destroyedStorage, caption: "已释放数据盘", tone: destroyedStorage ? "info" : "neutral" },
          { label: "已解除挂载", value: detachedAttachments, caption: "非活跃挂载", tone: detachedAttachments ? "info" : "neutral" }
        ]}
      />
      <InsightPanel
        title="访问 URL 清理"
        eyebrow="运营清理"
        actions={(
          <OperationConfirmButton
            label="清理全部无效 URL"
            title="确认清理全部无效 URL"
            description={`预计 ${cleanupCandidateCount} 个候选入口会被标记为不可用；只清理 URL 状态，不删除计算、存储或账本。`}
            danger
            disabled={cleanupCandidateCount === 0}
            onConfirm={() => runAction(
              () => cleanupWorkspaceAccess({ reason: "operator_cleanup_all", confirm: true }, session.csrfToken),
              "无效访问 URL 已清理",
              { actionKey: "admin-cleanup-workspace-access" }
            )}
          />
        )}
      >
        <CleanupResourceTable
          workspaces={managementState.workspaces || []}
          computeAllocations={managementState.computeAllocations || []}
          storageVolumes={managementState.storageVolumes || []}
          storageAttachments={managementState.storageAttachments || []}
          onCleanup={(row) => runAction(
            () => cleanupWorkspaceAccess({ workspaceIds: [row.id], reason: "operator_cleanup_single", confirm: true }, session.csrfToken),
            "访问 URL 已清理",
            { actionKey: `admin-cleanup-workspace-${row.id}` }
          )}
        />
      </InsightPanel>
      <InsightPanel title="清理边界" eyebrow="安全">
        <ResourceSplit
          items={[
            { label: "不删除", value: "计算 / 存储 / 账本", meta: "只处理访问状态", status: "已保护", tone: "good" },
            { label: "清理条件", value: "资源已销毁或挂载已解除", meta: "可用 URL 变为不可用", status: "当前", tone: "info" },
            { label: "证据", value: "workspace_access_cleaned", meta: "账本金额 0", status: "审计", tone: "info" }
          ]}
        />
        <OperationConfirmButton
          label="归档终态资源"
          title="确认归档终态资源"
          description="已销毁计算、已销毁存储、已解除挂载和不可恢复工作区会移出当前态；账本不会归档或删除。"
          danger
          disabled={(destroyedCompute + destroyedStorage + detachedAttachments) === 0}
          onConfirm={() => runAction(
            () => archiveTerminalResources({ reason: "operator_archive_terminal_resources", confirm: true }, session.csrfToken),
            "终态资源已归档",
            { actionKey: "admin-archive-terminal-resources" }
          )}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminSupportPage({ tickets }: any) {
  return (
    <ConsoleSurface title="工单运营" eyebrow="管理" subtitle="外部工单映射和资源定位">
      <InsightPanel title="外部工单映射" eyebrow="支持">
        <ObjectTable
          rowKey="id"
          data={tickets.tickets}
          emptyText="暂无外部工单映射"
          columns={[
            { title: "外部编号", dataIndex: "externalTicketId", ellipsis: true },
            { title: "外部系统", dataIndex: "externalSystem", ellipsis: true },
            { title: "标题", dataIndex: "title" },
            { title: "分类", dataIndex: "category" },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "用户", dataIndex: "userId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "反馈", dataIndex: "messages", ellipsis: true, render: (_, row) => firstMessage(row) || "-" },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "closed" ? "neutral" : "good"} /> },
            { title: "创建时间", dataIndex: "createdAt" }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}
