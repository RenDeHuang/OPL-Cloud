function parseJsonArray(value) {
  if (!value) return [];
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function windowConfig() {
  if (typeof window === "undefined") return {};
  return window.__APP_CONFIG__ || {};
}

function envValue(key) {
  return import.meta.env?.[key];
}

export function runtimeConfig() {
  const injected = windowConfig();
  const demoMode = Boolean(injected.demo_mode) || envValue("VITE_OPL_DEMO_MODE") === "1";
  const demoAccounts = Array.isArray(injected.demo_accounts)
    ? injected.demo_accounts
    : parseJsonArray(envValue("VITE_OPL_DEMO_ACCOUNTS_JSON"));
  return {
    demoMode,
    demoAccounts: demoMode ? demoAccounts : []
  };
}
