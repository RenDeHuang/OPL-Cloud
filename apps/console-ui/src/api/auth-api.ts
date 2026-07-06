import { postJson } from "./console-api.ts";

export async function currentSession() {
  const response = await fetch("/api/auth/me");
  if (!response.ok) return null;
  const payload = await response.json();
  return payload?.user ? payload : null;
}

export function login(credentials) {
  return postJson("/api/auth/login", credentials);
}

export function logout(csrfToken) {
  return postJson("/api/auth/logout", {}, csrfToken);
}
