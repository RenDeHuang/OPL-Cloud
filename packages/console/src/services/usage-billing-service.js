import { clone, money, now, stableHash } from "./core-utils.js";

export function requestUsageFingerprint({
  provider = "",
  model = "",
  inputTokens = 0,
  outputTokens = 0,
  requestedAmount = 0,
  sourceEventId = ""
}) {
  return `fp-${stableHash(JSON.stringify({
    provider,
    model,
    inputTokens: Number(inputTokens || 0),
    outputTokens: Number(outputTokens || 0),
    requestedAmount: money(Number(requestedAmount || 0)),
    sourceEventId
  }))}`;
}

export function requestQuotaWindowExpired(quota) {
  const windowSeconds = Number(quota.windowSeconds || 0);
  if (!windowSeconds || !quota.windowStartedAt) return false;
  const startedAt = Date.parse(quota.windowStartedAt);
  if (!Number.isFinite(startedAt)) return false;
  return Date.now() - startedAt >= windowSeconds * 1000;
}

export function incrementRequestQuota(user, units = 1) {
  const quota = user.requestQuota;
  if (!quota) return null;
  const amount = Number(units || 0);
  if (!Number.isFinite(amount) || amount <= 0) return clone(quota);
  quota.used = Number(quota.used || 0);
  if (quota.limit !== undefined && Number(quota.limit) >= 0 && quota.used + amount > Number(quota.limit)) {
    throw new Error("request_quota_exceeded");
  }
  if (requestQuotaWindowExpired(quota)) {
    quota.windowUsed = 0;
    quota.windowStartedAt = now();
  }
  if (quota.windowLimit !== undefined) {
    quota.windowUsed = Number(quota.windowUsed || 0);
    if (Number(quota.windowLimit) >= 0 && quota.windowUsed + amount > Number(quota.windowLimit)) {
      throw new Error("request_quota_exceeded");
    }
    quota.windowUsed = money(quota.windowUsed + amount);
    quota.windowStartedAt ||= now();
  }
  quota.used = money(quota.used + amount);
  return clone(quota);
}
