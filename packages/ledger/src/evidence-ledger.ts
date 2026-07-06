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

export function createEvidenceReceipt({
  state,
  type,
  accountId,
  workspaceId = "",
  actor = { type: "system", id: "opl-console" },
  plan = {},
  approval = { status: "implicit_console_policy" },
  environment = {},
  resourceRefs = {},
  billingRefs = [],
  inputRefs = [],
  executionRefs = [],
  outputRefs = [],
  reviewResults = [],
  continuation = null,
  metadata = {}
}) {
  const sequence = state?.evidenceLedger?.length ?? 0;
  return {
    id: makeId("receipt", accountId, workspaceId, type, String(sequence)),
    type,
    accountId,
    workspaceId,
    actor: clone(actor),
    plan: clone(plan),
    approval: clone(approval),
    environment: clone(environment),
    resourceRefs: clone(resourceRefs),
    billingRefs: clone(billingRefs),
    inputRefs: clone(inputRefs),
    executionRefs: clone(executionRefs),
    outputRefs: clone(outputRefs),
    reviewResults: clone(reviewResults),
    ...(continuation ? { continuation: clone(continuation) } : {}),
    ...(Object.keys(metadata).length > 0 ? { metadata: clone(metadata) } : {}),
    createdAt: now()
  };
}

export function appendEvidenceReceipt(state, receipt) {
  state.evidenceLedger ??= [];
  state.evidenceLedger.push(clone(receipt));
  return receipt;
}
