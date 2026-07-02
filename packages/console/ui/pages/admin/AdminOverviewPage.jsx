import React from "react";
import { PageContainer, ProCard, ProTable, StatisticCard } from "@ant-design/pro-components";
import { Alert, Button, Drawer, Form, Input, InputNumber, Tag } from "antd";
import { Plus } from "lucide-react";
import { api } from "../../api/console-api.js";
import { navigate } from "../../consoleRoutes.js";
import { CatalogCard, ReadinessCard, TopupList, WalletList } from "../shared/page-widgets.jsx";
import { money } from "../shared/formatters.js";

export function AdminOverviewPage({ state, adminOps }) {
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

export function AdminUsersPage({ state, wallet, topUpOpen, setTopUpOpen, topUpForm, session, runAction }) {
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

export function AdminBillingPage({ state }) {
  return (
    <PageContainer title="账务运营">
      <ProCard gutter={16} wrap>
        <ProCard title="手工充值记录"><TopupList events={state.manualTopups || []} /></ProCard>
        <ProCard title="钱包流水"><WalletList events={state.walletTransactions || []} /></ProCard>
      </ProCard>
    </PageContainer>
  );
}

export function AdminFabricPage() {
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

export function AdminLedgerPage({ state }) {
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

export function AdminRuntimePage({ adminOps }) {
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

export function AdminSupportPage({ tickets }) {
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
