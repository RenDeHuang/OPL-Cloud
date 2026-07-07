import { useEffect, useState } from "react";
import { createSupportTicketMapping, getSupportTicketMappings } from "../../api/support-api.ts";

export function useTickets({ csrfToken = "", all = false }: any = {}) {
  const [tickets, setTickets] = useState([]);
  const [loading, setLoading] = useState(false);

  async function refresh() {
    setLoading(true);
    try {
      const payload = await getSupportTicketMappings({ all });
      setTickets(payload.tickets || []);
    } finally {
      setLoading(false);
    }
  }

  async function createTicket(input) {
    const ticket = await createSupportTicketMapping(input, csrfToken);
    await refresh();
    return ticket;
  }

  useEffect(() => {
    refresh();
  }, [all]);

  return { tickets, loading, createTicket, refresh };
}
