function now() {
  return new Date().toISOString();
}

function stableHash(input) {
  let hash = 0;
  for (const char of input) {
    hash = (hash * 31 + char.charCodeAt(0)) >>> 0;
  }
  return hash.toString(36).padStart(6, "0");
}

function makeId(prefix, ...parts) {
  return `${prefix}-${stableHash(parts.join(":"))}`;
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

export function ensureManagementCollections(state) {
  state.organizations ??= {};
  state.users ??= {};
  state.memberships ??= [];
}

export function createOrganizationRecord(state, { organizationId, name, billingAccountId }) {
  ensureManagementCollections(state);
  const id = organizationId || makeId("org", name || "organization");
  if (!id) throw new Error("organization_required");
  if (!name) throw new Error("organization_name_required");
  state.organizations[id] ??= {
    id,
    name,
    billingAccountId: billingAccountId || id,
    status: "active",
    createdAt: now(),
    updatedAt: now()
  };
  return clone(state.organizations[id]);
}

export function createUserRecord(state, { userId, email, name = "" }) {
  ensureManagementCollections(state);
  const id = userId || makeId("usr", email || name || "user");
  if (!id) throw new Error("user_required");
  if (!email) throw new Error("user_email_required");
  state.users[id] ??= {
    id,
    email,
    name,
    status: "active",
    createdAt: now(),
    updatedAt: now()
  };
  return clone(state.users[id]);
}

export function addMembershipRecord(state, { organizationId, userId, role = "member" }) {
  ensureManagementCollections(state);
  if (!state.organizations[organizationId]) throw new Error("organization_not_found");
  if (!state.users[userId]) throw new Error("user_not_found");
  const existing = state.memberships.find((membership) =>
    membership.organizationId === organizationId &&
    membership.userId === userId
  );
  if (existing) {
    existing.role = role;
    existing.status = "active";
    existing.updatedAt = now();
    return clone(existing);
  }
  const membership = {
    id: makeId("membership", organizationId, userId),
    organizationId,
    userId,
    role,
    status: "active",
    createdAt: now(),
    updatedAt: now()
  };
  state.memberships.push(membership);
  return clone(membership);
}

export function resolveWorkspaceOwner(state, { accountId, organizationId, userId }) {
  ensureManagementCollections(state);
  if (!organizationId) {
    if (!accountId) throw new Error("account_required");
    return {
      accountId,
      owner: {
        type: "account",
        billingAccountId: accountId
      }
    };
  }
  const organization = state.organizations[organizationId];
  if (!organization || organization.status !== "active") throw new Error("organization_not_found");
  if (!userId) throw new Error("user_required");
  const membership = state.memberships.find((item) =>
    item.organizationId === organizationId &&
    item.userId === userId &&
    item.status === "active"
  );
  if (!membership) throw new Error("organization_membership_required");
  const billingAccountId = organization.billingAccountId || organization.id;
  return {
    accountId: billingAccountId,
    owner: {
      type: "organization",
      organizationId,
      userId,
      billingAccountId
    }
  };
}

export function managementSnapshot(state, { organizationId, packages, account, workspaces }) {
  ensureManagementCollections(state);
  const organization = state.organizations[organizationId];
  if (!organization) throw new Error("organization_not_found");
  const memberships = state.memberships
    .filter((membership) => membership.organizationId === organizationId)
    .map(clone);
  const userIds = new Set(memberships.map((membership) => membership.userId));
  return {
    organization: clone(organization),
    users: Object.values(state.users)
      .filter((user) => userIds.has(user.id))
      .map(clone),
    memberships,
    billingAccount: clone(account),
    packages: packages.map(clone),
    workspaces: workspaces.map(clone)
  };
}
