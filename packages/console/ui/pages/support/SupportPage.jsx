import React from "react";
import { Button, Empty, Form, Input, Select, message } from "antd";
import { Plus } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.js";
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

export function SupportPage({ tickets }) {
  const openTickets = tickets.tickets.filter((ticket) => ticket.status !== "closed");
  const highPriority = tickets.tickets.filter((ticket) => ticket.priority === "high").length;
  return (
    <ConsoleSurface
      title="Support"
      eyebrow="Tickets"
      subtitle="Account, billing and Workspace support"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("support.create"))}>提交工单</Button>}
    >
      <MetricStrip
        items={[
          { label: "全部工单", value: tickets.tickets.length, caption: "account scoped", tone: tickets.tickets.length ? "info" : "neutral" },
          { label: "处理中", value: openTickets.length, caption: "not closed", tone: openTickets.length ? "warn" : "good" },
          { label: "高优先级", value: highPriority, caption: "needs attention", tone: highPriority ? "danger" : "neutral" },
          { label: "Workspace", value: tickets.tickets.filter((ticket) => ticket.workspaceId).length, caption: "linked tickets", tone: "info" },
          { label: "状态", value: tickets.loading ? "Sync" : "Ready", caption: "ticket API", tone: tickets.loading ? "warn" : "good" }
        ]}
      />

      <InsightPanel title="工单列表" eyebrow="Queue">
        <ObjectTable
          rowKey="id"
          loading={tickets.loading}
          data={tickets.tickets}
          emptyText="暂无工单"
          columns={[
            { title: "标题", dataIndex: "title", render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("support.detail", { id: row.id }))}>{row.title}</Button> },
            { title: "分类", dataIndex: "category" },
            { title: "优先级", dataIndex: "priority", render: (value) => <StatusPill label={value} tone={value === "high" ? "danger" : "info"} /> },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "closed" ? "neutral" : "good"} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function NewSupportTicketPage({ state, tickets }) {
  const [form] = Form.useForm();
  return (
    <ConsoleSurface title="New Ticket" eyebrow="Support" subtitle="Account, billing, Workspace" compact>
      <InsightPanel title="提交工单" eyebrow="Case">
        <Form form={form} layout="vertical" onFinish={async (values) => {
          const ticket = await tickets.createTicket(values);
          message.success("工单已提交");
          navigate(routeTo("support.detail", { id: ticket.id }));
        }}>
          <Form.Item name="title" label="标题" rules={[{ required: true }]}>
            <Input placeholder="Workspace 无法打开" />
          </Form.Item>
          <Form.Item name="category" label="分类" initialValue="Workspace">
            <Select options={[
              { label: "Workspace", value: "Workspace" },
              { label: "Billing", value: "Billing" },
              { label: "Gateway", value: "Gateway" },
              { label: "Account", value: "Account" }
            ]} />
          </Form.Item>
          <Form.Item name="priority" label="优先级" initialValue="normal">
            <Select options={[
              { label: "normal", value: "normal" },
              { label: "high", value: "high" }
            ]} />
          </Form.Item>
          <Form.Item name="workspaceId" label="关联 Workspace">
            <Select
              allowClear
              options={state.workspaces.map((workspace) => ({ label: workspace.name, value: workspace.id }))}
            />
          </Form.Item>
          <Form.Item name="description" label="说明">
            <Input.TextArea rows={5} />
          </Form.Item>
          <ActionGroup actions={[<Button key="submit" type="primary" htmlType="submit">提交</Button>]} />
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function SupportTicketPage({ tickets }) {
  const id = window.location.pathname.split("/").at(-1);
  const ticket = tickets.tickets.find((item) => item.id === id);
  if (!ticket) {
    return (
      <ConsoleSurface title="Ticket" eyebrow="Support">
        <Empty description="未找到工单" />
      </ConsoleSurface>
    );
  }
  return (
    <ConsoleSurface title={ticket.title} eyebrow="Ticket" subtitle={ticket.id}>
      <div className="consoleGrid">
        <InsightPanel title="状态" eyebrow="Case">
          <ResourceSplit
            items={[
              { label: "分类", value: ticket.category, meta: "support queue", status: "category", tone: "info" },
              { label: "优先级", value: ticket.priority, meta: "triage level", status: ticket.priority, tone: ticket.priority === "high" ? "danger" : "info" },
              { label: "状态", value: ticket.status, meta: "current handling state", status: ticket.status, tone: ticket.status === "closed" ? "neutral" : "good" },
              { label: "Workspace", value: ticket.workspaceId || "-", meta: "linked object", status: ticket.workspaceId ? "linked" : "none", tone: ticket.workspaceId ? "info" : "neutral" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="对话" eyebrow="Messages">
          <TimelineList
            items={ticket.messages.map((item) => ({
              title: item.author,
              description: item.text,
              meta: item.createdAt,
              tone: item.author === "support" ? "good" : "info"
            }))}
          />
        </InsightPanel>
      </div>
    </ConsoleSurface>
  );
}
