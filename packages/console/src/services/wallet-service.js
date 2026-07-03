import { clone, makeId, money, now } from "./core-utils.js";

export function ensureBillingCollections(state) {
  state.resourceUsageLogs ??= [];
  state.requestUsageLogs ??= [];
  state.walletTransactions ??= [];
  state.manualTopups ??= [];
  state.requestUsageDedup ??= [];
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
    totalRecharged: money(Number(user.totalRecharged || 0))
  };
}

export function accountSnapshotForState(state, accountId) {
  const user = Object.values(state.users || {}).find((item) => item.accountId === accountId);
  if (!user) return { id: accountId, balance: 0, frozen: 0, holds: {}, totalRecharged: 0 };
  return {
    id: accountId,
    userId: user.id,
    balance: money(Number(user.balance || 0)),
    frozen: money(Number(user.frozen || 0)),
    holds: clone(user.holds || {}),
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

export function releaseHold(account, holdType, amount = accountHold(account, holdType)) {
  const current = accountHold(account, holdType);
  const released = money(Math.min(current, Math.max(0, Number(amount || 0))));
  if (released <= 0) return 0;
  account.holds[holdType] = money(current - released);
  account.frozen = money(account.frozen - released);
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
