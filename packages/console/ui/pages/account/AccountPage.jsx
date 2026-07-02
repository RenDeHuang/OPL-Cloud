import React from "react";
import { PageContainer, ProCard, StatisticCard } from "@ant-design/pro-components";
import { Descriptions, List } from "antd";
import { available, money } from "../shared/formatters.js";

export function AccountPage({ state, wallet, session }) {
  return (
    <PageContainer title="账户与实验室" subTitle="Identity, wallet, lab policy">
      <ProCard gutter={16} wrap>
        <ProCard title="身份" colSpan={{ xs: 24, xl: 8 }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="邮箱">{session.user.email}</Descriptions.Item>
            <Descriptions.Item label="角色">{session.user.role === "admin" ? "Admin" : "Lab Owner"}</Descriptions.Item>
            <Descriptions.Item label="账号">{state.account.id}</Descriptions.Item>
          </Descriptions>
        </ProCard>
        <ProCard title="钱包" colSpan={{ xs: 24, xl: 8 }}>
          <StatisticCard statistic={{ title: "可用余额", value: money(available(wallet)) }} />
        </ProCard>
        <ProCard title="实验室策略" colSpan={{ xs: 24, xl: 8 }}>
          <List size="small" dataSource={["Workspace URL 可分发", "7 天资源预冻结", "账单按小时解释"]} renderItem={(item) => <List.Item>{item}</List.Item>} />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}
