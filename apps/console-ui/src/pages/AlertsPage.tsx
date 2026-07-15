import { PageContainer } from "@ant-design/pro-components";
import { AlertList } from "./shared/page-widgets.tsx";

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
