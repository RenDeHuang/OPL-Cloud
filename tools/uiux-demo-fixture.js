export const uiuxDemoAccounts = Object.freeze([
  {
    label: "Lab Owner",
    id: "usr-owner-uiux",
    email: "owner@opl.local",
    password: "OplOwnerPass2026!",
    name: "Lab Owner",
    role: "pi",
    accountId: "acct-owner-uiux"
  },
  {
    label: "Admin",
    id: "usr-admin-uiux",
    email: "admin@opl.local",
    password: "OplAdminPass2026!",
    name: "OPL Admin",
    role: "admin",
    accountId: "admin"
  }
]);

export function uiuxDemoAuthSeedJson() {
  return JSON.stringify(uiuxDemoAccounts.map(({ label, ...account }) => account));
}

export function uiuxDemoPublicUrl({ env = process.env, port = "8791", networkInterfaces = {} } = {}) {
  if (env.OPL_PUBLIC_URL) return env.OPL_PUBLIC_URL;
  const explicitHost = env.OPL_UIUX_DEMO_PUBLIC_HOST;
  const host = explicitHost || firstExternalIpv4(networkInterfaces) || "127.0.0.1";
  return `http://${host}:${port}`;
}

function firstExternalIpv4(networkInterfaces) {
  for (const entries of Object.values(networkInterfaces || {})) {
    for (const entry of entries || []) {
      if (entry.family === "IPv4" && !entry.internal && entry.address) return entry.address;
    }
  }
  return "";
}
