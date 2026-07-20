import { decodeDto, decodeSource } from "./dtos.ts";
import type { AuthIdentity, AuthMeData, AuthSession, LoginRequest, SourceEnvelope } from "./dtos.ts";
import { postJson } from "./console-api.ts";

function identityFromLogin(value: unknown): AuthIdentity {
  const user = decodeDto<Record<string, unknown>>(value);
  const id = String(user.id || "");
  const accountId = String(user.accountId || "");
  const email = String(user.email || "");
  const role = String(user.role || "");
  const status = user.status === "disabled" ? "disabled" : "active";
  if (!id || !accountId || !email || !role) throw new Error("session_check_failed");
  return { id, accountId, email, role, status, ...(typeof user.name === "string" ? { name: user.name } : {}) };
}

function sessionFromLogin(value: unknown): AuthSession {
  const payload = decodeDto<Record<string, unknown>>(value);
  const user = identityFromLogin(payload.user);
  const csrfToken = String(payload.csrfToken || "");
  if (!csrfToken) throw new Error("session_check_failed");
  return {
    user,
    isOperator: payload.isOperator === true,
    csrfToken,
    ...(typeof payload.expiresAt === "string" ? { expiresAt: payload.expiresAt } : {})
  };
}

function sessionFromAuthMe(value: unknown, csrfToken: string): AuthSession {
  const envelope: SourceEnvelope<AuthMeData> = decodeSource<AuthMeData>(value);
  if (!envelope.available) throw new Error("authentication_unavailable");
  const data = envelope.data;
  const user: AuthIdentity = {
    id: data.consoleUserId,
    consoleUserId: data.consoleUserId,
    accountId: data.accountId,
    role: data.role,
    email: data.email,
    status: data.status,
    sub2apiUserId: data.sub2apiUserId
  };
  if (!user.id || !user.accountId || !user.email || !user.sub2apiUserId) throw new Error("session_check_failed");
  if (!csrfToken) throw new Error("session_check_failed");
  return { user, isOperator: data.role === "admin", csrfToken };
}

export async function currentSession(): Promise<AuthSession | null> {
  const response = await fetch("/api/auth/me", { signal: AbortSignal.timeout(3_000) });
  if (response.status === 401) return null;
  const payload = await response.json().catch(() => null);
  if (!response.ok) throw new Error(String((payload as Record<string, unknown> | null)?.error || "session_check_failed"));
  try {
    return sessionFromAuthMe(payload, response.headers.get("x-opl-csrf-token") || "");
  } catch (error) {
    if (error instanceof Error && error.message === "authentication_unavailable") throw error;
    throw new Error("session_check_failed");
  }
}

export function login(credentials: LoginRequest): Promise<AuthSession> {
  return postJson<unknown>("/api/auth/login", credentials).then(sessionFromLogin);
}

export function logout(csrfToken: string): Promise<unknown> {
  return postJson("/api/auth/logout", {}, csrfToken);
}

export async function logoutLocalFirst(
  csrfToken: string,
  clearLocalSession: () => void,
  redirect: () => void
): Promise<void> {
  clearLocalSession();
  redirect();
  await logout(csrfToken);
}
