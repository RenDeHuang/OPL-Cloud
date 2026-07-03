import { useEffect, useState } from "react";
import { createSupportTicket, getSupportTickets } from "../../api/support-api.js";

export function useTickets({ csrfToken = "", all = false } = {}) {
  const [tickets, setTickets] = useState([]);
  const [loading, setLoading] = useState(false);

  async function refresh() {
    setLoading(true);
    try {
      const payload = await getSupportTickets({ all });
      setTickets(payload.tickets || []);
    } finally {
      setLoading(false);
    }
  }

  async function createTicket(input) {
    const ticket = await createSupportTicket(input, csrfToken);
    setTickets((current) => [ticket, ...current]);
    return ticket;
  }

  useEffect(() => {
    refresh();
  }, [all]);

  return { tickets, loading, createTicket, refresh };
}
