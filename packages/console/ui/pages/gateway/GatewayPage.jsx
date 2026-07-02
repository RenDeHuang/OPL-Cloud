import React from "react";
import { PageContainer, ProCard, StatisticCard } from "@ant-design/pro-components";
import { Descriptions, Tag } from "antd";
import { UsageTable } from "../shared/page-widgets.jsx";
import { money, usageAmount } from "../shared/formatters.js";

export function GatewayPage({ state }) {
  const requestUsage = state.requestUsageLogs || [];
  return (
    <PageContainer title="OPL Gateway" subTitle="Keys, usage, quotas">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "请求", value: requestUsage.length }} />
        <StatisticCard statistic={{ title: "扣费", value: money(usageAmount(requestUsage)) }} />
        <StatisticCard statistic={{ title: "可用密钥", value: 1 }} />
      </StatisticCard.Group>
      <ProCard className="sectionCard" gutter={16} wrap>
        <ProCard title="接入密钥" colSpan={{ xs: 24, xl: 10 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="状态"><Tag color="green">Active</Tag></Descriptions.Item>
            <Descriptions.Item label="作用域">当前实验室</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="最近用量" colSpan={{ xs: 24, xl: 14 }}>
          <UsageTable data={requestUsage} type="request" />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}
