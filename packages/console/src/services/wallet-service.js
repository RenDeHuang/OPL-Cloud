import { clone, makeId, money, now } from "./core-utils.js";

export function ensureBillingCollections(state) {
  state.resourceUsageLogs ??= [];
  state.resourceUsageHourly ??= [];
  state.resourceUsageDaily ??= [];
  state.resourceUsageArchive ??= [];
  state.resourceUsageCleanupTasks ??= [];
  state.walletTransactions ??= [];
  state.manualTopups ??= [];
}

export function userIdForAccount(state, accountId) {
  return Object.values(state.users || {}).find((user) => user.accountId === accountId)?.id || `usr-${accountId}`;
}

export function ensureUserWallet(state, { userId = "", accountId, email = "" } = {}) {
  if (!accountId) throw new Error("account_required");
  ensureBillingCollections(state);
  state.users ??= {};
  const existingUser = userId
    ? state.users[userId]
    : Object.values(state.users).find((user) => user.accountId === accountId);
  const id = existingUser?.id || userId || userIdForAccount(state, accountId);
  state.users[id] ??= {
    id,
    email,
    accountId,
    role: "pi",
    status: "active",
    createdAt: now(),
    updatedAt: now()
  };
  const user = state.users[id];
  user.accountId ||= accountId;
  user.email ||= email;
  user.status ||= "active";
  user.balance = money(Number(user.balance ?? 0));
  user.frozen = money(Number(user.frozen ?? 0));
  user.holds ??= {};
  user.resourceHolds ??= { compute: {}, storage: {} };
  user.resourceHolds.compute ??= {};
  user.resourceHolds.storage ??= {};
  user.totalRecharged = money(Number(user.totalRecharged ?? 0));
  return user;
}

export function ensureAccount(state, accountId) {
  return ensureUserWallet(state, { accountId });
}

export function publicWalletUser(user) {
  if (!user) return null;
  const { password, passwordHash, ...safe } = clone(user);
  return safe;
}

export function walletSnapshot(user, accountId) {
  return {
    id: user.id,
    userId: user.id,
    accountId,
    balance: money(Number(user.balance || 0)),
    frozen: money(Number(user.frozen || 0)),
    available: accountAvailable(user),
    holds: clone(user.holds || {}),
    resourceHolds: clone(user.resourceHolds || { compute: {}, storage: {} }),
    totalRecharged: money(Number(user.totalRecharged || 0))
  };
}

export function accountSnapshotForState(state, accountId) {
  const user = Object.values(state.users || {}).find((item) => item.accountId === accountId);
  if (!user) return { id: accountId, balance: 0, frozen: 0, holds: {}, resourceHolds: { compute: {}, storage: {} }, totalRecharged: 0 };
  return {
    id: accountId,
    userId: user.id,
    balance: money(Number(user.balance || 0)),
    frozen: money(Number(user.frozen || 0)),
    holds: clone(user.holds || {}),
    resourceHolds: clone(user.resourceHolds || { compute: {}, storage: {} }),
    totalRecharged: money(Number(user.totalRecharged || 0))
  };
}

export function accountAvailable(account) {
  return money(account.balance - account.frozen);
}

export function accountHold(account, holdType) {
  account.holds ??= {};
  account.holds[holdType] = money(Number(account.holds[holdType] || 0));
  account.frozen = money(Object.values(account.holds).reduce((total, amount) => total + Number(amount || 0), 0));
  return account.holds[holdType];
}

export function addHold(account, holdType, amount) {
  const current = accountHold(account, holdType);
  account.holds[holdType] = money(current + amount);
  account.frozen = money(account.frozen + amount);
}

export function addResourceHold(account, holdType, resourceId, amount) {
  if (!resourceId) throw new Error("resource_hold_id_required");
  const holdAmount = money(Math.max(0, Number(amount || 0)));
  if (holdAmount <= 0) return null;
  addHold(account, holdType, holdAmount);
  account.resourceHolds ??= { compute: {}, storage: {} };
  account.resourceHolds[holdType] ??= {};
  const current = account.resourceHolds[holdType][resourceId] || {
    resourceId,
    holdType,
    initial: 0,
    remaining: 0,
    createdAt: now()
  };
  current.initial = money(Number(current.initial || 0) + holdAmount);
  current.remaining = money(Number(current.remaining || 0) + holdAmount);
  current.updatedAt = now();
  account.resourceHolds[holdType][resourceId] = current;
  return current;
}

export function releaseHold(account, holdType, amount = accountHold(account, holdType)) {
  const current = accountHold(account, holdType);
  const released = money(Math.min(current, Math.max(0, Number(amount || 0))));
  if (released <= 0) return 0;
  account.holds[holdType] = money(current - released);
  account.frozen = money(account.frozen - released);
  return released;
}

export function releaseResourceHold(account, holdType, resourceId, amount = null) {
  if (!resourceId) return 0;
  account.resourceHolds ??= { compute: {}, storage: {} };
  account.resourceHolds[holdType] ??= {};
  const resourceHold = account.resourceHolds[holdType][resourceId];
  if (!resourceHold) return 0;
  const remaining = money(Number(resourceHold.remaining || 0));
  const requested = amount == null ? remaining : money(Math.max(0, Number(amount || 0)));
  const released = money(Math.min(remaining, requested));
  if (released <= 0) return 0;
  resourceHold.remaining = money(remaining - released);
  resourceHold.updatedAt = now();
  const currentHold = accountHold(account, holdType);
  account.holds[holdType] = money(Math.max(0, currentHold - released));
  account.frozen = money(Math.max(0, account.frozen - released));
  if (resourceHold.remaining <= 0) delete account.resourceHolds[holdType][resourceId];
  return released;
}

export function debitAccount(account, holdType, amount) {
  const debit = money(Math.max(0, Number(amount || 0)));
  if (debit <= 0) return 0;
  const currentHold = accountHold(account, holdType);
  const captured = money(Math.min(currentHold, debit));
  if (captured <= 0) return 0;
  account.holds[holdType] = money(currentHold - captured);
  account.frozen = money(Math.max(0, account.frozen - captured));
  account.balance = money(account.balance - captured);
  return captured;
}

export function debitResourceHold(account, holdType, resourceId, amount) {
  const debit = money(Math.max(0, Number(amount || 0)));
  if (debit <= 0 || !resourceId) return 0;
  account.resourceHolds ??= { compute: {}, storage: {} };
  account.resourceHolds[holdType] ??= {};
  const resourceHold = account.resourceHolds[holdType][resourceId];
  const currentResourceHold = money(Number(resourceHold?.remaining || 0));
  const captured = money(Math.min(currentResourceHold, debit));
  if (captured <= 0) return 0;
  resourceHold.remaining = money(currentResourceHold - captured);
  resourceHold.updatedAt = now();
  const currentHold = accountHold(account, holdType);
  account.holds[holdType] = money(Math.max(0, currentHold - captured));
  account.frozen = money(Math.max(0, account.frozen - captured));
  account.balance = money(account.balance - captured);
  if (resourceHold.remaining <= 0) delete account.resourceHolds[holdType][resourceId];
  return captured;
}

export function debitAvailableBalance(account, amount) {
  const debit = money(Math.max(0, Number(amount || 0)));
  if (debit <= 0) return 0;
  const captured = money(Math.min(accountAvailable(account), debit));
  if (captured <= 0) return 0;
  account.balance = money(account.balance - captured);
  return captured;
}

export function chargeAccount(account, holdType, amount) {
  const requested = money(Math.max(0, Number(amount || 0)));
  const available = debitAvailableBalance(account, requested);
  const remainingAfterAvailable = money(requested - available);
  const hold = debitAccount(account, holdType, remainingAfterAvailable);
  return {
    requested,
    available,
    hold,
    charged: money(available + hold),
    unpaid: money(requested - available - hold),
    usedHold: hold > 0,
    exhaustedHold: hold > 0 && accountHold(account, holdType) <= 0
  };
}

export function chargeResourceAccount(account, holdType, resourceId, amount) {
  const requested = money(Math.max(0, Number(amount || 0)));
  const available = debitAvailableBalance(account, requested);
  const remainingAfterAvailable = money(requested - available);
  const hold = debitResourceHold(account, holdType, resourceId, remainingAfterAvailable);
  const remainingHold = money(Number(account.resourceHolds?.[holdType]?.[resourceId]?.remaining || 0));
  return {
    requested,
    available,
    hold,
    charged: money(available + hold),
    unpaid: money(requested - available - hold),
    usedHold: hold > 0,
    exhaustedHold: hold > 0 && remainingHold <= 0
  };
}

export function appendWalletTransaction(state, {
  user,
  accountId,
  workspaceId = "account",
  type,
  amount,
  sourceEventId,
  ledgerEntryId = "",
  usageLogId = "",
  fundingSource = "",
  balanceBefore,
  balanceAfter,
  frozenBefore,
  frozenAfter,
  metadata = null
}) {
  ensureBillingCollections(state);
  const transaction = {
    id: makeId("wallet-tx", user.id, accountId, workspaceId, type, sourceEventId, String(state.walletTransactions.length)),
    userId: user.id,
    accountId,
    workspaceId,
    type,
    amount: money(Number(amount || 0)),
    currency: "CNY",
    balanceBefore: money(Number(balanceBefore || 0)),
    balanceAfter: money(Number(balanceAfter || 0)),
    frozenBefore: money(Number(frozenBefore || 0)),
    frozenAfter: money(Number(frozenAfter || 0)),
    sourceEventId,
    ...(ledgerEntryId ? { ledgerEntryId } : {}),
    ...(usageLogId ? { usageLogId } : {}),
    ...(fundingSource ? { fundingSource } : {}),
    ...(metadata ? { metadata: clone(metadata) } : {}),
    createdAt: now()
  };
  state.walletTransactions.push(transaction);
  return transaction;
}
