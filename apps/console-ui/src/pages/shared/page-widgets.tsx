import React from "react";
import { PageContainer, ProCard } from "@ant-design/pro-components";
import { Empty, List, Space, Tag, Typography } from "antd";
import { AlertTriangle } from "lucide-react";
import { money } from "./formatters.ts";

type AnyRecord = Record<string, any>;

export function ForbiddenPage() {
  return <PageContainer title="无权限"><Empty description="当前账号无权访问该页面" /></PageContainer>;
}

export function CatalogCard({ title, items }: any) {
  return (
    <ProCard title={title} colSpan={{ xs: 24, xl: 8 }}>
      <List size="small" dataSource={items} renderItem={(item) => <List.Item><Tag color="blue">Approved</Tag>{String(item)}</List.Item>} />
    </ProCard>
  );
}

export function AlertList({ events = [] }: any) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无告警" />;
  return (
    <List
      dataSource={events.slice(-8).reverse()}
      renderItem={(event: AnyRecord) => (
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

export function TopupList({ events }: any) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无充值记录" />;
  return <List size="small" dataSource={events.slice(-8).reverse()} renderItem={(event: AnyRecord) => <List.Item>{event.targetAccountId} · {money(event.amount)}</List.Item>} />;
}

export function WalletList({ events }: any) {
  if (!events.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无钱包流水" />;
  return <List size="small" dataSource={events.slice(-8).reverse()} renderItem={(event: AnyRecord) => <List.Item>{event.type} · {money(event.amount)}</List.Item>} />;
}

export function ReadinessCard({ title, readiness }: any) {
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
