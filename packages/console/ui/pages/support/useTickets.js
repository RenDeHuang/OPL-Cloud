import { useState } from "react";

export function useTickets() {
  const [tickets, setTickets] = useState(() => {
    try {
      return JSON.parse(localStorage.getItem("opl-support-tickets") || "[]");
    } catch {
      return [];
    }
  });

  function save(next) {
    setTickets(next);
    localStorage.setItem("opl-support-tickets", JSON.stringify(next));
  }

  function createTicket(input) {
    const ticket = {
      id: `ticket-${Date.now()}`,
      title: input.title,
      category: input.category,
      priority: input.priority,
      workspaceId: input.workspaceId || "",
      status: "open",
      createdAt: new Date().toISOString(),
      messages: [{ author: "Lab Owner", text: input.description || "Created from OPL Console" }]
    };
    save([ticket, ...tickets]);
    return ticket;
  }

  return { tickets, createTicket };
}
