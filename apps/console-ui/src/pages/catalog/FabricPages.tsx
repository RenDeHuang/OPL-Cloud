import React from "react";
import { PageContainer, ProCard } from "@ant-design/pro-components";
import { Empty, Timeline } from "antd";
import { AlertList, CatalogCard } from "../shared/page-widgets.tsx";

export function ResourcesPage() {
  return (
    <PageContainer title="资源目录" subTitle="Approved connectors, environments, agents">
      <ProCard gutter={16} wrap>
        <CatalogCard title="连接器" items={["PubMed", "arXiv", "Zotero"]} />
        <CatalogCard title="环境" items={["Python/R", "Quarto/LaTeX", "CUDA"]} />
        <CatalogCard title="Agent 包" items={["Literature Review", "Grant Draft", "Figure Review"]} />
      </ProCard>
    </PageContainer>
  );
}

export function ApprovalsPage() {
  return <PageContainer title="待审批"><Empty description="暂无审批事项" /></PageContainer>;
}

export function ReceiptsPage({ state }: any) {
  return (
    <PageContainer title="回执中心">
      <Timeline items={(state.evidenceLedger || []).slice(-12).reverse().map((item) => ({
        children: <><strong>{item.type}</strong><div>{item.workspaceId || item.accountId}</div></>
      }))} />
    </PageContainer>
  );
}

export function AlertsPage({ state, tickets }: any) {
  const ticketAlerts = tickets.tickets.filter((ticket) => ticket.status !== "closed").map((ticket) => ({
    id: ticket.id,
    type: "support.ticket_open",
    accountId: ticket.title
  }));
  return (
    <PageContainer title="告警">
      <AlertList events={[...(state.notifications || []), ...ticketAlerts]} />
    </PageContainer>
  );
}
