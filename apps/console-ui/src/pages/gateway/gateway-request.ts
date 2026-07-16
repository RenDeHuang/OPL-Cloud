export function createGatewayRequestLifecycle() {
  let current: AbortController | null = null;
  return {
    start() {
      current?.abort();
      current = new AbortController();
      return current;
    },
    cancel(controller: AbortController | null = current) {
      controller?.abort();
      if (current === controller) current = null;
    },
    isCurrent(controller: AbortController) {
      return current === controller && !controller.signal.aborted;
    },
    dispose() {
      current?.abort();
      current = null;
    }
  };
}

export function maskGatewaySummary(summary: any) {
  if (!summary?.apiKey) return summary;
  const apiKey = { ...summary.apiKey, revealed: false };
  delete apiKey.value;
  return { ...summary, apiKey };
}
