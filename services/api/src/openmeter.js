export class OpenMeterClient {
  constructor({ endpoint, apiKey, fetchImpl = fetch } = {}) {
    this.endpoint = endpoint?.replace(/\/$/, "");
    this.apiKey = apiKey;
    this.fetchImpl = fetchImpl;
  }

  async recordUsage({ event, subject, value, metadata = {} }) {
    const response = await this.fetchImpl(`${this.endpoint}/api/v1/events`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: `Bearer ${this.apiKey}`
      },
      body: JSON.stringify({
        specversion: "1.0",
        type: event,
        source: "opl-cloud",
        subject,
        data: {
          value,
          ...metadata
        }
      })
    });
    if (!response.ok) {
      const body = typeof response.text === "function" ? await response.text() : "";
      throw new Error(`openmeter_event_failed:${response.status}:${body}`);
    }
    const payload = typeof response.json === "function" ? await response.json() : {};
    return {
      ok: true,
      eventId: payload.id || payload.eventId || ""
    };
  }
}

export function createMeterFromEnv(env = process.env) {
  if (!env.OPENMETER_ENDPOINT || !env.OPENMETER_API_KEY) return null;
  return new OpenMeterClient({
    endpoint: env.OPENMETER_ENDPOINT,
    apiKey: env.OPENMETER_API_KEY
  });
}
