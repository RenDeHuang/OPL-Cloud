import React from "react";
import { PageContainer, ProCard, ProTable } from "@ant-design/pro-components";
import { Button, Descriptions, Empty, Form, Input, Tag, Timeline, message } from "antd";
import { Plus } from "lucide-react";
import { navigate } from "../../consoleRoutes.js";

export function SupportPage({ tickets }) {
  return (
    <PageContainer title="工单" extra={<Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/support/new")}>提交工单</Button>}>
      <ProTable
        rowKey="id"
        loading={tickets.loading}
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

export function NewSupportTicketPage({ state, tickets }) {
  const [form] = Form.useForm();
  return (
    <PageContainer title="提交工单" subTitle="Account, billing, Workspace">
      <ProCard>
        <Form form={form} layout="vertical" onFinish={async (values) => {
          const ticket = await tickets.createTicket(values);
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

export function SupportTicketPage({ tickets }) {
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
