import React from "react";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Typography } from "antd";
import { Plus } from "lucide-react";
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
import { moneyCents, paidThrough, usdMicros, valueLabel } from "../shared/formatters.ts";

type AnyRecord = Record<string, any>;

function roleLabel(role = "") {
  return {
    admin: "运维",
    pi: "用户",
    member: "成员",
    owner: "用户"
  }[role] || role || "用户";
}

function firstMessage(ticket: AnyRecord = {}) {
  return (ticket.messages || []).map((message) => message.text).filter(Boolean).join(" / ");
}

function costTagsLabel(tags: AnyRecord = {}) {
  return [tags.opl_account_id, tags.opl_workspace_id, tags.opl_resource_id, tags.opl_operation_id].filter(Boolean).join(" · ") || "-";
}

export function AdminOverviewPage({ state, managementState = {}, adminOps }: any) {
  const failed = adminOps.operator?.runtimeOperations?.failed ?? 0;
  const usersByAccount = new Map<string, AnyRecord>((managementState.users || []).map((user) => [user.accountId, user]));
  const accountRows = (managementState.accounts || []).slice(0, 8);
  const resourceLedgerEvidence = managementState.resourceLedgerEvidence || [];
  const monthlyResources = [...(state.computeAllocations || []), ...(state.storageVolumes || [])];
  const reviewRequired = monthlyResources.filter((item) => item.billingStatus === "manual_review");
  const resourceAnomalies = adminOps.operator?.resourceAnomalies || [];
  const failedOperations = adminOps.operator?.failedOperations || adminOps.operator?.runtimeOperations?.recentFailed || [];
  const statusRows = [
    ...resourceAnomalies.map((item) => ({ ...item, kind: item.type || "资源异常" })),
    ...failedOperations.map((item) => ({ ...item, kind: item.operationType || "失败操作" }))
  ].slice(0, 8);
  return (
    <ConsoleSurface title="管理概览" eyebrow="运营" subtitle="账号映射、月度权益和运行状态">
      <MetricStrip
        items={[
          { label: "账号", value: accountRows.length, caption: "Console 账号", tone: "info" },
          { label: "Sub2API 映射", value: accountRows.filter((row) => row.sub2apiUserId).length, caption: "余额归属", tone: "good" },
          { label: "买了什么", value: resourceLedgerEvidence.length || state.workspaces.length, caption: "资源和工作区", tone: "good" },
          { label: "人工复核", value: reviewRequired.length, caption: "manual_review", tone: reviewRequired.length ? "danger" : "good" },
          { label: "运行异常", value: resourceAnomalies.length + failed, caption: "资源和操作", tone: resourceAnomalies.length + failed ? "danger" : "good" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="账号映射" eyebrow="Sub2API">
          <ObjectTable
            rowKey="id"
            data={accountRows}
            emptyText="暂无账号"
            columns={[
              { title: "用户", render: (_, row) => usersByAccount.get(row.id)?.email || row.ownerEmail || "-" },
              { title: "账号", dataIndex: "id", ellipsis: true },
              { title: "Sub2API User", dataIndex: "sub2apiUserId", render: (value) => value || "未映射" },
              { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "active"} tone={value === "active" ? "good" : "warn"} /> }
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

export function AdminUsersPage({ managementState, session, runAction }: any) {
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
      sub2apiUserId: account.sub2apiUserId ?? user.sub2apiUserId
    };
  });
  const activeUsers = users.filter((user) => !["disabled", "deleted"].includes(user.status)).length;
  return (
    <ConsoleSurface
      title="用户"
      eyebrow="管理"
      subtitle="登录用户、组织成员与 Sub2API 账号映射"
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
      <InsightPanel title="用户映射" eyebrow="管理">
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
            { title: "Sub2API User", dataIndex: "sub2apiUserId", render: (value) => value || "未映射" },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "active" ? "good" : value === "deleted" ? "danger" : "warn"} /> },
            {
              title: "操作",
              valueType: "option",
              render: (_, row) => (
                <ActionGroup actions={[
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
        initialValues={{ role: "pi" }}
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
        <Form.Item name="sub2apiUserId" label="Sub2API User ID" rules={[{ required: true, message: "请输入 Sub2API User ID" }]}>
          <InputNumber min={1} precision={0} className="fullWidth" />
        </Form.Item>
        <Button type="primary" htmlType="submit">创建用户</Button>
      </Form>
    </Drawer>
  );
}

export function AdminBillingPage({ state, managementState = {}, adminOps }: any) {
  const resources = [
    ...(state.computeAllocations || []).map((item) => ({ ...item, resourceType: "计算" })),
    ...(state.storageVolumes || []).map((item) => ({ ...item, resourceType: "存储" }))
  ];
  const reviewRows = resources.filter((item) => ["manual_review", "past_due"].includes(item.billingStatus));
  const accounts = managementState.accounts || [];
  const reconciliation = state.billingReconciliation || adminOps.operator?.billingReconciliation || {};
  return (
    <ConsoleSurface title="账单运营" eyebrow="管理" subtitle="Sub2API 映射、月度权益与异常复核">
      <MetricStrip
        items={[
          { label: "账号映射", value: accounts.filter((row) => row.sub2apiUserId).length, caption: "Sub2API User", tone: "good" },
          { label: "月度资源", value: resources.length, caption: "计算与存储", tone: resources.length ? "info" : "neutral" },
          { label: "人工复核", value: reviewRows.length, caption: "manual_review / past_due", tone: reviewRows.length ? "danger" : "good" },
          { label: "对账状态", value: reconciliation.status || reconciliation.guard?.status || "ok", caption: reconciliation.reason || reconciliation.guard?.reason || "无异常", tone: reconciliation.blockNewWorkspaces || reconciliation.guard?.blockNewWorkspaces ? "danger" : "good" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="账号映射" eyebrow="Sub2API">
          <ObjectTable
            data={accounts}
            emptyText="暂无账号"
            columns={[
              { title: "Console 账号", dataIndex: "id", ellipsis: true },
              { title: "Sub2API User", dataIndex: "sub2apiUserId", render: (value) => value || "未映射" },
              { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "active"} tone={value === "active" ? "good" : "warn"} /> }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="异常复核" eyebrow="月度权益">
          <ObjectTable
            data={reviewRows}
            emptyText="暂无待复核资源"
            columns={[
              { title: "类型", dataIndex: "resourceType" },
              { title: "资源", render: (_, row) => row.name || row.id },
              { title: "状态", dataIndex: "billingStatus", render: (value) => <StatusPill label={valueLabel(value)} tone="danger" /> },
              { title: "错误", dataIndex: "lastBillingError", ellipsis: true },
              { title: "扣款", dataIndex: "chargeUsdMicros", render: (value) => usdMicros(value) }
            ]}
          />
        </InsightPanel>
      </div>
      <InsightPanel title="操作审计" eyebrow="审计">
        <TimelineList
          emptyText="暂无操作审计"
          items={(managementState.auditEvents || []).slice(-8).reverse().map((event) => ({
            title: event.action,
            description: [event.targetAccountId || event.actorAccountId, event.resourceKind && event.resourceId ? `${event.resourceKind} ${event.resourceId}` : "", event.result].filter(Boolean).join(" · "),
            meta: event.createdAt,
            tone: event.result === "succeeded" ? "good" : "warn"
          }))}
        />
      </InsightPanel>
    </ConsoleSurface>
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
  const receipts = state.billingReceipts || [];
  return (
    <ConsoleSurface title="账本" eyebrow="管理" subtitle="月度购买与续费 Receipt">
      <InsightPanel title="Billing Receipt" eyebrow="只读证据">
        <ObjectTable
          rowKey={(row) => row.receiptId || row.id}
          pagination={{ pageSize: 8 }}
          data={receipts}
          emptyText="暂无 Billing Receipt"
          columns={[
            { title: "Receipt", render: (_, row) => row.receiptId || row.id },
            { title: "事件", render: (_, row) => row.type || row.receiptType },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "参考月价", render: (_, row) => moneyCents(row.monthlyPriceCnyCents || row.cost?.monthlyPriceCnyCents) },
            { title: "钱包扣款", render: (_, row) => usdMicros(row.chargeUsdMicros || row.cost?.chargeUsdMicros) },
            { title: "有效期至", render: (_, row) => paidThrough(row.paidThrough || row.cost?.paidThrough) }
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
            { title: "Receipt", dataIndex: "receiptIds", width: 190, ellipsis: true, render: (value, row) => (value || [row.lastReceiptId]).filter(Boolean).join(", ") || "-" },
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

export function AdminE2EPage({ adminOps, managementState = {} }: any) {
  const archive = managementState.archive || {};
  return (
    <ConsoleSurface title="E2E记录" eyebrow="管理" subtitle="真实上线验证摘要">
      <ProductionE2EPanel summary={archive.productionE2E || adminOps.operator?.productionE2E || {}} />
      <DataRetentionPolicyPanel policy={managementState.retentionPolicy || archive.retentionPolicy || {}} />
    </ConsoleSurface>
  );
}

export function AdminCleanupPage({ managementState, session, runAction }: any) {
  const cleanupSummary = managementState.workspaceAccessCleanup || {};
  const archive = managementState.archive || {};
  const archiveResources = archive.resources || [];
  const archiveJobs = archive.jobs || [];
  const cleanupCandidateCount = Number(cleanupSummary.cleanupCandidateCount || 0);
  const activeUrlCount = Number(cleanupSummary.activeUrlCount || 0);
  const destroyedCompute = Number(cleanupSummary.destroyedComputeCount || 0);
  const destroyedStorage = Number(cleanupSummary.destroyedStorageCount || 0);
  const detachedAttachments = Number(cleanupSummary.detachedAttachmentCount || 0);
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
      <InsightPanel title="归档记录" eyebrow="后端归档">
        <MetricStrip
          items={[
            { label: "归档资源", value: archiveResources.length, caption: "archive resources", tone: archiveResources.length ? "info" : "neutral" },
            { label: "归档任务", value: archiveJobs.length, caption: "archive jobs", tone: archiveJobs.length ? "info" : "neutral" },
            { label: "归档审计", value: (archive.adminAuditEvents || []).length, caption: "admin audit", tone: (archive.adminAuditEvents || []).length ? "info" : "neutral" },
            { label: "E2E记录", value: archive.productionE2E?.total || 0, caption: "production e2e", tone: archive.productionE2E?.failed ? "danger" : "neutral" }
          ]}
        />
        <ObjectTable
          rowKey="id"
          data={archiveResources}
          emptyText="暂无归档资源"
          columns={[
            { title: "资源", dataIndex: "resourceId", ellipsis: true },
            { title: "类型", dataIndex: "resourceKind" },
            { title: "账号", dataIndex: "accountId", ellipsis: true },
            { title: "工作区", dataIndex: "workspaceId", ellipsis: true },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value || "-"} tone="neutral" /> },
            { title: "原因", dataIndex: "reason", ellipsis: true },
            { title: "归档时间", dataIndex: "createdAt" }
          ]}
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
      <DataRetentionPolicyPanel policy={managementState.retentionPolicy || archive.retentionPolicy || {}} />
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
