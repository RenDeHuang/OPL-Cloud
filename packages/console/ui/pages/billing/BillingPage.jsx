import React from "react";
import { PageContainer, ProCard, ProTable, StatisticCard } from "@ant-design/pro-components";
import { Descriptions } from "antd";
import { available, money, usageQuantity } from "../shared/formatters.js";

export function BillingPage({ state, wallet }) {
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
