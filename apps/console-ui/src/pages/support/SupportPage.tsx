import { Button, Empty, Form, Input, Select, message } from "antd";
import { Plus } from "lucide-react";
import { navigate, routeTo } from "../../consoleRoutes.ts";
import {
  ActionGroup,
  ConsoleSurface,
  InsightPanel,
  MetricStrip,
  ObjectTable,
  ResourceSplit,
  StatusPill,
  TimelineList
} from "../shared/commercial-console.tsx";
import { customerSafeMessage } from "../shared/formatters.ts";

export function SupportPage({ tickets }: any) {
  const openTickets = tickets.tickets.filter((ticket) => ticket.status !== "closed");
  const highPriority = tickets.tickets.filter((ticket) => ticket.priority === "high").length;
  return (
    <ConsoleSurface
      title="工单"
      eyebrow="支持"
      subtitle="外部工单映射、账号、账单和工作区入口支持"
      extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate(routeTo("support.create"))}>登记外部工单</Button>}
    >
      <MetricStrip
        items={[
          { label: "全部映射", value: tickets.tickets.length, caption: "账号范围", tone: tickets.tickets.length ? "info" : "neutral" },
          { label: "处理中", value: openTickets.length, caption: "外部未关闭", tone: openTickets.length ? "warn" : "good" },
          { label: "高优先级", value: highPriority, caption: "需要关注", tone: highPriority ? "danger" : "neutral" },
          { label: "关联工作区", value: tickets.tickets.filter((ticket) => ticket.workspaceId).length, caption: "已关联工单", tone: "info" },
          { label: "状态", value: tickets.loading ? "同步中" : "就绪", caption: "工单接口", tone: tickets.loading ? "warn" : "good" }
        ]}
      />

      <InsightPanel title="外部工单映射" eyebrow="队列">
        <ObjectTable
          rowKey="id"
          loading={tickets.loading}
          data={tickets.tickets}
          emptyText="暂无外部工单映射"
          columns={[
            { title: "外部编号", dataIndex: "externalTicketId", render: (_, row) => <Button type="link" onClick={() => navigate(routeTo("support.detail", { id: row.id }))}>{row.externalTicketId}</Button> },
            { title: "标题", dataIndex: "title" },
            { title: "分类", dataIndex: "category" },
            { title: "优先级", dataIndex: "priority", render: (value) => <StatusPill label={value} tone={value === "high" ? "danger" : "info"} /> },
            { title: "状态", dataIndex: "status", render: (value) => <StatusPill label={value} tone={value === "closed" ? "neutral" : "good"} /> }
          ]}
        />
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function NewSupportMappingPage({ state, tickets }: any) {
  const [form] = Form.useForm();
  const query = new URLSearchParams(window.location.search);
  const resourceId = query.get("resourceId") || "";
  const operationId = query.get("operationId") || "";
  const failureReason = customerSafeMessage(query.get("failureReason") || "", "");
  const resourceType = query.get("resourceType") || "";
  const initialDescription = [
    failureReason,
    operationId ? `操作编号：${operationId}` : "",
    resourceId ? `资源编号：${resourceId}` : "",
    resourceType ? `资源类型：${resourceType}` : ""
  ].filter(Boolean).join("\n");
  return (
    <ConsoleSurface title="登记外部工单" eyebrow="支持" subtitle="账号、账单、工作区入口" compact>
      <InsightPanel title="外部工单映射" eyebrow="工单">
        <Form
          form={form}
          layout="vertical"
          initialValues={{
            title: failureReason ? "资源操作失败" : "",
            externalSystem: "external-helpdesk",
            category: query.get("category") || "Workspace",
            priority: query.get("priority") || "normal",
            description: initialDescription,
            operationId,
            resourceIds: resourceId ? [resourceId] : []
          }}
          onFinish={async (values) => {
          const ticket = await tickets.createTicket(values);
          message.success("外部工单已登记");
          navigate(routeTo("support.detail", { id: ticket.id }));
        }}>
          <Form.Item name="externalTicketId" label="外部工单编号" rules={[{ required: true, message: "请输入外部工单编号" }]}>
            <Input placeholder="Zammad / Chatwoot / Helpdesk ticket id" />
          </Form.Item>
          <Form.Item name="externalUrl" label="外部工单链接">
            <Input placeholder="https://support.example.com/tickets/123" />
          </Form.Item>
          <Form.Item name="externalSystem" label="外部系统">
            <Input placeholder="zammad / chatwoot / freescout" />
          </Form.Item>
          <Form.Item name="operationId" hidden>
            <Input />
          </Form.Item>
          <Form.Item name="resourceIds" hidden>
            <Select mode="tags" />
          </Form.Item>
          <Form.Item name="title" label="标题" rules={[{ required: true }]}>
            <Input placeholder="工作区入口无法打开" />
          </Form.Item>
          <Form.Item name="category" label="分类">
            <Select options={[
              { label: "工作区", value: "Workspace" },
              { label: "账单", value: "Billing" },
              { label: "网关", value: "Gateway" },
              { label: "账号", value: "Account" }
            ]} />
          </Form.Item>
          <Form.Item name="priority" label="优先级">
            <Select options={[
              { label: "普通", value: "normal" },
              { label: "高", value: "high" }
            ]} />
          </Form.Item>
          <Form.Item name="workspaceId" label="关联工作区">
            <Select
              allowClear
              options={state.workspaces.map((workspace) => ({ label: workspace.name, value: workspace.id }))}
            />
          </Form.Item>
          <Form.Item name="description" label="说明">
            <Input.TextArea rows={5} />
          </Form.Item>
          <ActionGroup actions={[<Button key="submit" type="primary" htmlType="submit">登记</Button>]} />
        </Form>
      </InsightPanel>
    </ConsoleSurface>
  );
}

export function SupportMappingPage({ tickets }: any) {
  const id = window.location.pathname.split("/").at(-1);
  const ticket = tickets.tickets.find((item) => item.id === id);
  if (!ticket) {
    return (
      <ConsoleSurface title="工单" eyebrow="支持">
        <Empty description="未找到外部工单映射" />
      </ConsoleSurface>
    );
  }
  return (
    <ConsoleSurface title={ticket.title} eyebrow="工单" subtitle={ticket.externalTicketId || ticket.id}>
      <div className="consoleGrid">
        <InsightPanel title="状态" eyebrow="工单">
          <ResourceSplit
            items={[
              { label: "分类", value: ticket.category, meta: "支持队列", status: "分类", tone: "info" },
              { label: "优先级", value: ticket.priority, meta: "分诊等级", status: ticket.priority, tone: ticket.priority === "high" ? "danger" : "info" },
              { label: "状态", value: ticket.status, meta: "当前处理状态", status: ticket.status, tone: ticket.status === "closed" ? "neutral" : "good" },
              { label: "工作区", value: ticket.workspaceId || "-", meta: "关联对象", status: ticket.workspaceId ? "已关联" : "无", tone: ticket.workspaceId ? "info" : "neutral" },
              { label: "外部系统", value: ticket.externalSystem || "-", meta: ticket.externalUrl || "未配置链接", status: "映射", tone: "info" }
            ]}
          />
        </InsightPanel>
        <InsightPanel title="上下文" eyebrow="消息">
          <TimelineList
            items={(ticket.messages || []).map((item) => ({
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
