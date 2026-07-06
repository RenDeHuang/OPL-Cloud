import React from "react";
import { Alert, Button, Drawer, Form, Input, InputNumber, Select, Typography } from "antd";
import { Plus } from "lucide-react";
import { manualTopUp } from "../../api/billing-api.js";
import { cleanupWorkspaceAccess, createUser, deleteUser, disableUser } from "../../api/console-read-api.js";
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
} from "../shared/commercial-console.jsx";
import { money } from "../shared/formatters.js";

export function AdminOverviewPage({ state, adminOps }) {
  const failed = adminOps.operator?.runtimeOperations?.failed ?? 0;
  return (
    <ConsoleSurface title="管理概览" eyebrow="运营" subtitle="账号、工作区入口、运行证据">
      <MetricStrip
        items={[
          { label: "账号", value: adminOps.operator?.accounts?.total ?? 1, caption: "托管计费账号", tone: "info" },
          { label: "工作区入口", value: adminOps.operator?.workspaces?.total ?? state.workspaces.length, caption: `${adminOps.operator?.workspaces?.running ?? 0} 个运行中`, tone: "good" },
          { label: "失败操作", value: failed, caption: "运行操作失败", tone: failed ? "danger" : "good" },
          { label: "冻结总额", value: money(adminOps.operator?.accounts?.frozen), caption: "全部账号", tone: "warn" },
          { label: "告警", value: adminOps.operator?.notifications?.total ?? 0, caption: "管理员可见", tone: adminOps.operator?.notifications?.error ? "danger" : "neutral" }
        ]}
      />
      <div className="consoleGrid equal">
        <InsightPanel title="运行态" eyebrow="运行">
          <ResourceSplit
            items={[
              { label: "运行就绪", value: adminOps.runtime?.ready ? "就绪" : "阻塞", meta: "运行检查", status: adminOps.runtime?.ready ? "通过" : "检查", tone: adminOps.runtime?.ready ? "good" : "warn" },
              { label: "上线就绪", value: adminOps.launch?.ready ? "就绪" : "阻塞", meta: "生产门禁", status: adminOps.launch?.ready ? "通过" : "检查", tone: adminOps.launch?.ready ? "good" : "warn" },
              { label: "失败操作", value: failed, meta: "运行操作队列", status: failed ? "待处理" : "清空", tone: failed ? "danger" : "good" },
              { label: "计算分配", value: adminOps.operator?.computeAllocations?.total ?? adminOps.operator?.compute?.total ?? 0, meta: "CVM 分配证据", status: "已跟踪", tone: "info" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="最近告警" eyebrow="信号">
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
  const activeUsers = users.filter((user) => !["disabled", "deleted"].includes(user.status)).length;
  return (
    <ConsoleSurface
      title="用户"
      eyebrow="管理"
      subtitle="登录用户、计费账号、钱包操作"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => setCreateOpen(true)}>新建用户</Button>}
    >
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
            { title: "角色", dataIndex: "role", render: (value) => <StatusPill label={value} tone={value === "admin" ? "info" : "good"} /> },
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
                    topUpForm.setFieldsValue({ accountId: row.accountId, amount: 200, reason: "commercial top-up" });
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
                      "用户已禁用"
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
                      () => deleteUser({ userId: row.id, reason: "admin_deleted" }, session.csrfToken),
                      "用户已删除"
                    )}
                  />
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
          <Input placeholder="实验室负责人" />
        </Form.Item>
        <Form.Item name="role" label="角色" rules={[{ required: true, message: "请选择角色" }]}>
          <Select
            options={[
              { label: "实验室负责人", value: "pi" },
              { label: "管理员", value: "admin" }
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
    <ConsoleSurface title="账单运营" eyebrow="管理" subtitle="人工充值和钱包流水证据">
      <div className="consoleGrid equal">
        <InsightPanel title="手工充值记录" eyebrow="充值">
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
        <InsightPanel title="钱包流水" eyebrow="流水">
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

export function AdminLedgerPage({ state }) {
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

function adminResourceEvidenceRows(state = {}) {
  const computeById = new Map((state.computeAllocations || []).map((item) => [item.id, item]));
  const storageById = new Map((state.storageVolumes || []).map((item) => [item.id, item]));
  const attachmentById = new Map((state.storageAttachments || []).map((item) => [item.id, item]));
  return (state.workspaces || []).map((workspace) => {
    const compute = computeById.get(workspace.currentComputeAllocationId);
    const storage = storageById.get(workspace.storageId);
    const attachment = attachmentById.get(workspace.currentAttachmentId);
    const issue = [compute, storage, attachment, workspace].find((item) => item?.safeMessage || item?.error || item?.failureReason || item?.providerRequestId || item?.operationId) || {};
    return {
      id: workspace.id,
      workspaceId: workspace.id,
      accountId: workspace.ownerAccountId,
      ownerAccountId: workspace.ownerAccountId,
      ownerUserId: workspace.ownerUserId || "",
      workspaceIds: [workspace.id],
      computeId: workspace.currentComputeAllocationId || compute?.id || "",
      computeAllocationId: workspace.currentComputeAllocationId || compute?.id || "",
      cvmInstanceId: compute?.cvmInstanceId || compute?.providerResourceId || compute?.nodeName || compute?.machineName || "",
      nodeName: compute?.nodeName || compute?.machineName || "",
      storageId: workspace.storageId || storage?.id || "",
      storageProviderId: storage?.providerResourceId || "",
      attachmentId: workspace.currentAttachmentId || attachment?.id || "",
      ledgerEntryIds: [],
      walletTransactionIds: [],
      status: workspace.state || workspace.runtime?.status || "unknown",
      issue: issue.safeMessage || issue.error || issue.failureReason || "暂无失败",
      providerRequestId: issue.providerRequestId || issue.operationId || ""
    };
  });
}

export function AdminDiagnosticsPage({ managementState, adminOps }) {
  const failedOperations = adminOps.operator?.failedOperations || adminOps.operator?.runtimeOperations?.recentFailed || [];
  const resourceAnomalies = adminOps.operator?.resourceAnomalies || [];
  const resourceLedgerEvidence = managementState.resourceLedgerEvidence || [];
  const resourceEvidence = resourceLedgerEvidence.length ? resourceLedgerEvidence : adminResourceEvidenceRows(managementState);
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
            { title: "Owner", dataIndex: "ownerAccountId", width: 150, ellipsis: true, render: (value, row) => value || row.accountId || "-" },
            { title: "User", dataIndex: "ownerUserId", width: 150, ellipsis: true },
            { title: "CVM / 节点", dataIndex: "cvmInstanceId", width: 190, ellipsis: true, render: (value) => <Typography.Text copyable className="inlineCode">{value || "-"}</Typography.Text> },
            { title: "Node", dataIndex: "nodeName", width: 170, ellipsis: true },
            { title: "计算 ID", dataIndex: "computeAllocationId", width: 170, ellipsis: true, render: (value, row) => value || row.computeId || "-" },
            { title: "存储 ID", dataIndex: "storageId", width: 170, ellipsis: true },
            { title: "存储 provider", dataIndex: "storageProviderId", width: 190, ellipsis: true, render: (value) => <Typography.Text copyable className="inlineCode">{value || "-"}</Typography.Text> },
            { title: "账本", dataIndex: "ledgerEntryIds", width: 190, ellipsis: true, render: (value) => (value || []).join(", ") || "-" },
            { title: "钱包流水", dataIndex: "walletTransactionIds", width: 190, ellipsis: true, render: (value) => (value || []).join(", ") || "-" },
            { title: "状态", dataIndex: "status", width: 95, render: (value) => <StatusPill label={value} tone={value === "failed" ? "danger" : "info"} /> },
            { title: "问题依据", dataIndex: "issue", width: 190, ellipsis: true },
            { title: "请求/操作", dataIndex: "providerRequestId", width: 170, ellipsis: true }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminE2EPage({ adminOps }) {
  return (
    <ConsoleSurface title="E2E记录" eyebrow="管理" subtitle="真实上线验证摘要">
      <ProductionE2EPanel summary={adminOps.operator?.productionE2E || {}} />
      <DataRetentionPolicyPanel />
    </ConsoleSurface>
  );
}

export function AdminCleanupPage({ managementState, session, runAction }) {
  const activeWorkspaces = (managementState.workspaces || []).filter((workspace) => workspace.access?.tokenStatus === "active");
  const destroyedCompute = (managementState.computeAllocations || []).filter((item) => item.status === "destroyed").length;
  const destroyedStorage = (managementState.storageVolumes || []).filter((item) => item.status === "destroyed").length;
  const detachedAttachments = (managementState.storageAttachments || []).filter((item) => item.status === "detached").length;
  return (
    <ConsoleSurface title="入口清理" eyebrow="管理" subtitle="清理已失效资源对应的访问 URL">
      <MetricStrip
        items={[
          { label: "可用 URL", value: activeWorkspaces.length, caption: "候选工作区入口", tone: activeWorkspaces.length ? "warn" : "good" },
          { label: "已销毁计算", value: destroyedCompute, caption: "已停止分配", tone: destroyedCompute ? "info" : "neutral" },
          { label: "已销毁存储", value: destroyedStorage, caption: "已释放数据盘", tone: destroyedStorage ? "info" : "neutral" },
          { label: "已解除挂载", value: detachedAttachments, caption: "非活跃挂载", tone: detachedAttachments ? "info" : "neutral" }
        ]}
      />
      <InsightPanel
        title="访问 URL 清理"
        eyebrow="运营清理"
        actions={(
          <Button
            danger
            onClick={() => runAction(
              () => cleanupWorkspaceAccess({ reason: "operator_cleanup_all" }, session.csrfToken),
              "无效访问 URL 已清理"
            )}
          >
            清理全部无效 URL
          </Button>
        )}
      >
        <CleanupResourceTable
          workspaces={managementState.workspaces || []}
          computeAllocations={managementState.computeAllocations || []}
          storageVolumes={managementState.storageVolumes || []}
          storageAttachments={managementState.storageAttachments || []}
          onCleanup={(row) => runAction(
            () => cleanupWorkspaceAccess({ workspaceIds: [row.id], reason: "operator_cleanup_single" }, session.csrfToken),
            "访问 URL 已清理"
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
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function AdminSupportPage({ tickets }) {
  return (
    <ConsoleSurface title="工单运营" eyebrow="管理" subtitle="全部可见工单">
      <InsightPanel title="工单队列" eyebrow="支持">
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
