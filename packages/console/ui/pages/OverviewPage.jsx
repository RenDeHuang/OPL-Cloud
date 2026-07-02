import React from "react";
import { PageContainer, ProCard, StatisticCard } from "@ant-design/pro-components";
import { Button, Space } from "antd";
import { Headphones, Plus, WalletCards } from "lucide-react";
import { navigate } from "../consoleRoutes.js";
import { AlertList } from "./shared/page-widgets.jsx";
import { money } from "./shared/formatters.js";

export function OverviewPage({ state, wallet, tickets }) {
  const needsAttention = state.notifications?.length || 0;
  return (
    <PageContainer title="总览" subTitle="Workspace delivery, wallet, tickets">
      <StatisticCard.Group>
        <StatisticCard statistic={{ title: "余额", value: money(wallet.balance) }} />
        <StatisticCard statistic={{ title: "冻结", value: money(wallet.frozen) }} />
        <StatisticCard statistic={{ title: "Workspace", value: state.workspaces.length }} />
        <StatisticCard statistic={{ title: "工单", value: tickets.tickets.length }} />
        <StatisticCard statistic={{ title: "告警", value: needsAttention }} />
      </StatisticCard.Group>
      <ProCard className="sectionCard" gutter={16} split="vertical">
        <ProCard title="下一步" colSpan="35%">
          <Space direction="vertical" size={12}>
            <Button type="primary" icon={<Plus size={15} />} onClick={() => navigate("/console/workspaces/new")}>创建 Workspace</Button>
            <Button icon={<Headphones size={15} />} onClick={() => navigate("/console/support/new")}>提交工单</Button>
            <Button icon={<WalletCards size={15} />} onClick={() => navigate("/console/billing/wallet")}>查看钱包</Button>
          </Space>
        </ProCard>
        <ProCard title="最近告警">
          <AlertList events={state.notifications} />
        </ProCard>
      </ProCard>
    </PageContainer>
  );
}
