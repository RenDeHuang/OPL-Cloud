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

function requireObject(value, field) {
  if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).length === 0) {
    throw new Error(`task_evidence_${field}_required`);
  }
}

function arrayOrEmpty(value) {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error("task_evidence_refs_must_be_arrays");
  return value;
}

export function createTaskEvidenceReceipt({
  state = { evidenceLedger: [] },
  accountId,
  workspaceId = "",
  taskId,
  actor = { type: "system", id: "opl-ledger" },
  plan,
  approval,
  environment,
  inputRefs = [],
  executionRefs = [],
  outputRefs = [],
  reviewResults = [],
  continuation = null,
  metadata = {}
}) {
  if (!accountId) throw new Error("task_evidence_account_required");
  if (!taskId) throw new Error("task_evidence_task_required");
  requireObject(plan, "plan");
  requireObject(approval, "approval");
  requireObject(environment, "environment");

  const sequence = (state.evidenceLedger || []).filter((entry) => entry.type === "task.evidence.v1").length;
  return {
    id: makeId("task-receipt", accountId, workspaceId, taskId, String(sequence)),
    type: "task.evidence.v1",
    accountId,
    workspaceId,
    taskId,
    actor: clone(actor),
    plan: clone(plan),
    approval: clone(approval),
    environment: clone(environment),
    inputRefs: clone(arrayOrEmpty(inputRefs)),
    executionRefs: clone(arrayOrEmpty(executionRefs)),
    outputRefs: clone(arrayOrEmpty(outputRefs)),
    reviewResults: clone(arrayOrEmpty(reviewResults)),
    ...(continuation ? { continuation: clone(continuation) } : {}),
    ...(Object.keys(metadata).length > 0 ? { metadata: clone(metadata) } : {}),
    createdAt: now()
  };
}

export function appendTaskEvidenceReceipt(state, receipt) {
  state.evidenceLedger ??= [];
  state.evidenceLedger.push(clone(receipt));
  return receipt;
}

export function filterTaskEvidenceReceipts(state, { accountId, workspaceId = null, taskId = null } = {}) {
  if (!accountId) throw new Error("task_evidence_account_required");
  return (state.evidenceLedger || [])
    .filter((entry) => entry.type === "task.evidence.v1")
    .filter((entry) => entry.accountId === accountId)
    .filter((entry) => workspaceId === null || entry.workspaceId === workspaceId)
    .filter((entry) => taskId === null || entry.taskId === taskId)
    .map(clone);
}
