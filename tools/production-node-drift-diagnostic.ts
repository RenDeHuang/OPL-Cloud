import { spawnSync } from "node:child_process";
import { createHash, createHmac } from "node:crypto";
import { chmod, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

const OPERATION_MODE = "normal_launch_node_drift_get_only";
const APPROVED_CUSTOMER_EMAIL_DIGEST = "sha256:d241839999cab1dbb0fc96c4dda28f9433ccfa68e12246e1b2ed0726d19ec376";
const OWNERSHIP_LABELS = [
  "medopl.cn/workload",
  "oplcloud.cn/resource-id",
  "oplcloud.cn/account-id",
  "oplcloud.cn/workspace-id"
];
const TAINT_KEY = "oplcloud.cn/workspace-id";
const DIGEST = /^sha256:[a-f0-9]{64}$/;
const SAFE_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/;
const SAFE_MANAGER = /^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,255}$/;
const SYSTEM_SERVICE_ACCOUNT = /^system:serviceaccount:[a-z0-9.-]+:[a-z0-9.-]+$/;
const READ_ONLY_KUBECTL = new Set(["api-resources", "get"]);
const READ_ONLY_TENCENT_ACTIONS = new Set(["DescribeLogSwitches", "SearchLog"]);
const FABRIC_GET_PATH = /^\/fabric\/(?:operations|compute-allocations\/[A-Za-z0-9._-]+|machine-ownerships\/[A-Za-z0-9._-]+)$/;

function sha256(value) {
  return `sha256:${createHash("sha256").update(String(value)).digest("hex")}`;
}

function diagnosticError(code, firstFalsePredicate, expected, actual, authority) {
  const error = new Error(code);
  error.diagnostic = { failureBoundary: "production.node_drift_identity", reasonCode: code, firstFalsePredicate, expected, actual, authority };
  return error;
}

export function assertOriginalLaunchOwner(launch, control) {
  const expected = String(launch?.ownerUserId || "");
  const actual = String(control?.ownerUserId || "");
  if (!expected || !actual || expected !== actual) {
    throw diagnosticError(
      "node_drift_original_launch_owner_mismatch",
      "identity.originalLaunch.ownerUserId",
      expected ? sha256(expected) : "absent",
      actual ? sha256(actual) : "absent",
      "control-plane.runtime-operation"
    );
  }
}

export function assertApprovedCustomerEmailDigests(digests) {
  const values = Array.isArray(digests) ? digests : [];
  const actual = values.find((digest) => digest !== APPROVED_CUSTOMER_EMAIL_DIGEST);
  if (values.length !== 2 || actual) {
    throw diagnosticError(
      "node_drift_approved_customer_identity_mismatch",
      "identity.normalizedEmailDigest",
      APPROVED_CUSTOMER_EMAIL_DIGEST,
      DIGEST.test(String(actual || "")) ? actual : "absent",
      "control-plane.account-user+production-secret"
    );
  }
}

function exactOne(values, code) {
  if (!Array.isArray(values) || values.length !== 1) throw new Error(code);
  return values[0];
}

function safeTime(value) {
  const text = String(value || "");
  return SAFE_TIME.test(text) && Number.isFinite(Date.parse(text)) ? text : "unavailable";
}

function safeManager(value) {
  const text = String(value || "");
  return SAFE_MANAGER.test(text) && !/(?:bearer|token|secret|password)/i.test(text) ? text : "unavailable";
}

function safeSubject(value) {
  const text = String(value || "");
  if (SYSTEM_SERVICE_ACCOUNT.test(text)) return text;
  return text ? sha256(text) : "unavailable";
}

function safeUserAgent(value) {
  const text = String(value || "");
  return text ? sha256(text) : "unavailable";
}

function deriveIdentity(input) {
  const control = input.controlPlaneIdentity;
  if (!control?.accountId || !control?.workspaceId || !control?.ownerUserId || !control?.ownerEmail || !control?.sub2APIUserId || control?.persistedSub2APIBindingMatches !== true) {
    throw diagnosticError("node_drift_control_plane_identity_invalid", "identity.controlPlane.requiredIdentity", "complete_exact_persisted_binding", "incomplete_or_unavailable", "control_plane.persisted_sub2api_binding");
  }
  assertOriginalLaunchOwner(input.controlPlaneLaunch, control);
  assertApprovedCustomerEmailDigests(input.controlPlaneEmailDigests);
  const operationRef = `${input.launchOperationId}:compute`;
  const launch = input.controlPlaneLaunch;
  if (launch?.operationId !== input.launchOperationId || launch?.accountId !== control.accountId || launch?.workspaceId !== control.workspaceId ||
    !launch?.computeAllocationId || !safeTime(launch?.updatedAt).startsWith("20") || !safeTime(launch?.executeStartedAt).startsWith("20")) {
    throw diagnosticError("node_drift_control_plane_launch_identity_mismatch", "identity.originalLaunch.controlPlaneBinding",
      sha256(JSON.stringify([input.launchOperationId, control.accountId, control.workspaceId])),
      sha256(JSON.stringify([launch?.operationId || "", launch?.accountId || "", launch?.workspaceId || ""])), "control-plane.postgresql");
  }
  const operations = (input.fabricOperations || []).filter((operation) =>
    operation?.action === "create_compute_allocation" &&
    (operation?.operationId === operationRef || operation?.idempotencyKey === operationRef) &&
    operation?.accountId === control.accountId && operation?.workspaceId === control.workspaceId);
  if (operations.length !== 1) {
    throw diagnosticError("node_drift_fabric_compute_operation_not_unique", "identity.fabric.computeOperation", "exact_one", operations.length === 0 ? "absent" : "multiple", "fabric.operation-store-get");
  }
  const operation = operations[0];
  const allocation = input.allocation;
  const ownership = input.ownership;
  const valid = allocation?.id === operation?.resourceId && allocation?.id === launch.computeAllocationId && allocation?.accountId === control.accountId &&
    allocation?.workspaceId === control.workspaceId && ownership?.resourceId === allocation?.id &&
    ownership?.accountId === control.accountId && ownership?.workspaceId === control.workspaceId &&
    ownership?.status === "active" && allocation?.machineName && allocation?.nodePoolId && allocation?.nodeName &&
    ownership?.machineId === allocation.machineName && ownership?.nodePoolId === allocation.nodePoolId &&
    ownership?.nodeName === allocation.nodeName;
  if (!valid) {
    throw diagnosticError("node_drift_authoritative_identity_mismatch", "identity.fabric.allocationOwnershipBinding",
      sha256(JSON.stringify([operation?.resourceId || "", launch.computeAllocationId, control.accountId, control.workspaceId])),
      sha256(JSON.stringify([allocation?.id || "", ownership?.resourceId || "", allocation?.accountId || "", allocation?.workspaceId || ""])),
      "fabric.operation+compute-allocation+machine-ownership-store-get");
  }
  return { ...control, launch, operation, allocation, ownership };
}

function selectorSegment(value) {
  try {
    const parsed = JSON.parse(value);
    const parts = [];
    if (typeof parsed?.key === "string" && /^[A-Za-z0-9./_-]+$/.test(parsed.key)) parts.push(`key=${parsed.key}`);
    if (typeof parsed?.effect === "string" && /^[A-Za-z]+$/.test(parsed.effect)) parts.push(`effect=${parsed.effect}`);
    return parts.length ? `[${parts.join(",")}]` : "[]";
  } catch {
    return "[]";
  }
}

function flattenFieldsV1(value, prefix = [], result = []) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return result;
  for (const [rawKey, child] of Object.entries(value)) {
    if (rawKey === ".") continue;
    if (rawKey.startsWith("f:")) {
      flattenFieldsV1(child, [...prefix, rawKey.slice(2).replaceAll("~1", "/").replaceAll("~0", "~")], result);
      if (!child || typeof child !== "object" || Object.keys(child).length === 0) result.push([...prefix, rawKey.slice(2).replaceAll("~1", "/").replaceAll("~0", "~")].join("."));
      continue;
    }
    if (rawKey.startsWith("k:") && prefix.length) {
      const next = [...prefix];
      next[next.length - 1] += selectorSegment(rawKey.slice(2));
      flattenFieldsV1(child, next, result);
      if (!child || typeof child !== "object" || Object.keys(child).length === 0) result.push(next.join("."));
    }
  }
  return result;
}

function safeStructuralField(path) {
  const text = String(path || "");
  if (!text || text.length > 256 || /["'@]|\b(?:acct|workspace-launch|ws-|ca_|ins-|np-)\b|\b(?:\d{1,3}\.){3}\d{1,3}\b/i.test(text)) return "";
  return /^[A-Za-z0-9._/\[\]=,-]+$/.test(text) ? text : "";
}

function nodeRelevantField(path) {
  if (OWNERSHIP_LABELS.some((label) => path === `metadata.labels.${label}`)) return true;
  const taint = `spec.taints[key=${TAINT_KEY},effect=NoSchedule]`;
  return path === taint || path.startsWith(`${taint}.`);
}

function projectManagedFields(resource, { nodeOnly = false } = {}) {
  const projected = [];
  for (const entry of resource?.metadata?.managedFields || []) {
    const manager = safeManager(entry?.manager);
    const operation = new Set(["Apply", "Update"]).has(entry?.operation) ? entry.operation : "unavailable";
    const fields = [...new Set(flattenFieldsV1(entry?.fieldsV1).map(safeStructuralField).filter(Boolean)
      .filter((field) => !nodeOnly || nodeRelevantField(field)))].sort();
    if (manager !== "unavailable" && operation !== "unavailable" && fields.length) {
      projected.push({
        manager,
        operation,
        apiVersion: /^[A-Za-z0-9./_-]+$/.test(String(entry?.apiVersion || "")) ? entry.apiVersion : "unavailable",
        subresource: /^[A-Za-z0-9._/-]*$/.test(String(entry?.subresource || "")) ? String(entry.subresource || "") : "unavailable",
        time: safeTime(entry?.time),
        fields: fields.slice(0, 64)
      });
    }
  }
  return projected.sort((left, right) => String(left.time).localeCompare(String(right.time)));
}

function projectOwners(resource) {
  return (resource?.metadata?.ownerReferences || []).map((owner) => ({
    apiVersion: /^[A-Za-z0-9./_-]+$/.test(String(owner?.apiVersion || "")) ? owner.apiVersion : "unavailable",
    kind: /^[A-Za-z][A-Za-z0-9._-]{0,127}$/.test(String(owner?.kind || "")) ? owner.kind : "unavailable",
    nameDigest: owner?.name ? sha256(owner.name) : "unavailable",
    uidDigest: owner?.uid ? sha256(owner.uid) : "unavailable",
    controller: owner?.controller === true,
    blockOwnerDeletion: owner?.blockOwnerDeletion === true
  }));
}

function resourceProjection(resource, { nodeOnly = false } = {}) {
  const metadata = resource?.metadata || {};
  return {
    available: Boolean(metadata.name && metadata.uid),
    uidDigest: metadata.uid ? sha256(metadata.uid) : "unavailable",
    creationTimestamp: safeTime(metadata.creationTimestamp),
    resourceVersion: /^[0-9A-Za-z._-]+$/.test(String(metadata.resourceVersion || "")) ? String(metadata.resourceVersion) : "unavailable",
    owners: projectOwners(resource),
    managedFields: projectManagedFields(resource, { nodeOnly })
  };
}

function nodeOwnership(node, identity) {
  const labels = node?.metadata?.labels || {};
  const expected = {
    "medopl.cn/workload": "workspace",
    "oplcloud.cn/resource-id": identity.allocation.id,
    "oplcloud.cn/account-id": identity.accountId,
    "oplcloud.cn/workspace-id": identity.workspaceId
  };
  const labelMatches = Object.fromEntries(OWNERSHIP_LABELS.map((key) => [key, labels[key] === expected[key]]));
  const taints = (node?.spec?.taints || []).filter((taint) => taint?.key === TAINT_KEY);
  const taint = taints[0];
  const taintState = taints.length !== 1 || taint?.effect !== "NoSchedule" ? "conflict"
    : taint.value === identity.workspaceId ? "target_owned"
      : taint.value === "unallocated" ? "unallocated" : "conflict";
  return {
    labels: { matches: labelMatches, currentMatch: Object.values(labelMatches).every(Boolean) },
    taint: { count: taints.length, effectMatches: taint?.effect === "NoSchedule", state: taintState, currentMatch: taintState === "target_owned" }
  };
}

function nodeStateFromAuditObject(value, identity) {
  if (!value || value?.metadata?.name !== identity.allocation.nodeName) return "unavailable";
  const ownership = nodeOwnership(value, identity);
  if (ownership.labels.currentMatch && ownership.taint.state === "target_owned") return "target_owned";
  if (ownership.taint.state === "unallocated") return "unallocated";
  return "conflict";
}

function auditTime(record) {
  return safeTime(record?.stageTimestamp || record?.requestReceivedTimestamp || record?.timestamp || record?.time);
}

function auditSubject(record) {
  return record?.user?.username || record?.username || record?.subject || "";
}

function controllerFromRecord(record) {
  const subject = String(auditSubject(record) || "");
  if (SYSTEM_SERVICE_ACCOUNT.test(subject)) return subject.split(":").at(-1);
  const agent = String(record?.userAgent || "").split("/", 1)[0];
  return SAFE_MANAGER.test(agent) ? agent : "unavailable";
}

function auditRequestOwnershipState(record, identity) {
  if (!Array.isArray(record?.requestObject)) return "unavailable";
  const expectedLabels = {
    "medopl.cn/workload": "workspace",
    "oplcloud.cn/resource-id": identity.allocation.id,
    "oplcloud.cn/account-id": identity.accountId,
    "oplcloud.cn/workspace-id": identity.workspaceId
  };
  const matchedLabels = new Set();
  let taintState = "unavailable";
  for (const operation of record.requestObject) {
    const path = String(operation?.path || "").replaceAll("~1", "/").replaceAll("~0", "~");
    for (const [label, expected] of Object.entries(expectedLabels)) {
      if (path === `/metadata/labels/${label}` && operation?.value === expected) matchedLabels.add(label);
    }
    if (/^\/spec\/taints\/\d+\/value$/.test(path)) {
      if (operation?.value === identity.workspaceId) taintState = "target_owned";
      else if (operation?.value === "unallocated") taintState = "unallocated";
    }
  }
  if (taintState === "target_owned" && matchedLabels.size !== OWNERSHIP_LABELS.length) return "unavailable";
  return taintState;
}

function relevantAuditRecords(raw, identity) {
  return (raw?.records || []).filter((record) =>
    new Set(["patch", "update", "create", "delete"]).has(String(record?.verb || "").toLowerCase()) &&
    record?.objectRef?.resource === "nodes" && record?.objectRef?.name === identity.allocation.nodeName)
    .map((record) => ({
      raw: record,
      verb: String(record.verb).toLowerCase(),
      time: auditTime(record),
      uid: String(record?.objectRef?.uid || record?.responseObject?.metadata?.uid || ""),
      ownershipState: (() => {
        const responseState = nodeStateFromAuditObject(record?.responseObject, identity);
        return responseState === "unavailable" ? auditRequestOwnershipState(record, identity) : responseState;
      })()
    }))
    .sort((left, right) => String(left.time).localeCompare(String(right.time)));
}

function projectAudit(audit, records) {
  const status = new Set(["enabled", "disabled", "unavailable"]).has(audit?.status) ? audit.status : "unavailable";
  return {
    status,
    records: records.map((entry) => ({
      verb: entry.verb,
      subject: safeSubject(auditSubject(entry.raw)),
      userAgent: safeUserAgent(entry.raw?.userAgent),
      time: entry.time,
      nodeUidDigest: entry.uid ? sha256(entry.uid) : "unavailable",
      ownershipState: entry.ownershipState
    }))
  };
}

function projectEvents(events, nodeName, currentUID) {
  return (events?.items || []).filter((event) => event?.involvedObject?.kind === "Node" && event?.involvedObject?.name === nodeName)
    .map((event) => ({
      reason: SAFE_MANAGER.test(String(event?.reason || "")) ? event.reason : "unavailable",
      reportingController: SAFE_MANAGER.test(String(event?.reportingController || event?.source?.component || "")) ? (event.reportingController || event.source.component) : "unavailable",
      action: SAFE_MANAGER.test(String(event?.action || "")) ? event.action : "unavailable",
      nodeUidDigest: event?.involvedObject?.uid ? sha256(event.involvedObject.uid) : "unavailable",
      currentNodeUID: Boolean(currentUID && event?.involvedObject?.uid === currentUID),
      eventTime: safeTime(event?.eventTime),
      firstTimestamp: safeTime(event?.firstTimestamp),
      lastTimestamp: safeTime(event?.lastTimestamp)
    }))
    .sort((left, right) => String(left.eventTime).localeCompare(String(right.eventTime)));
}

function writerFields(nodeManagedFields, writer, time) {
  const targetTime = Date.parse(time);
  const matching = nodeManagedFields.filter((entry) => {
    const entryTime = Date.parse(entry.time);
    return entry.manager === writer && Number.isFinite(targetTime) && Number.isFinite(entryTime) && Math.abs(entryTime - targetTime) <= 10_000;
  });
  return [...new Set(matching.flatMap((entry) => entry.fields))].sort();
}

function auditRequestOwnedFields(record) {
  if (!Array.isArray(record?.requestObject)) return [];
  const fields = [];
  for (const operation of record.requestObject) {
    const path = String(operation?.path || "").replaceAll("~1", "/").replaceAll("~0", "~");
    for (const label of OWNERSHIP_LABELS) {
      if (path === `/metadata/labels/${label}`) fields.push(`metadata.labels.${label}`);
    }
    if (/^\/spec\/taints\/\d+\/value$/.test(path) && operation?.value === "unallocated") {
      fields.push(`spec.taints[key=${TAINT_KEY},effect=NoSchedule]`);
    }
  }
  return [...new Set(fields)].sort();
}

function rootCausePacket(identity, node, managedFields, auditRecords, events) {
  const currentUID = String(node?.metadata?.uid || "");
  const timedRecords = auditRecords.filter((entry) => Number.isFinite(Date.parse(entry.time)))
    .sort((left, right) => Date.parse(left.time) - Date.parse(right.time));
  const targetWrite = [...timedRecords].reverse().find((entry) => entry.ownershipState === "target_owned" && entry.uid);
  const laterWrites = targetWrite ? timedRecords.filter((entry) => Date.parse(entry.time) > Date.parse(targetWrite.time)) : [];
  if (!targetWrite || !currentUID) {
    return { sameNodeUID: null, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: ["historical_node_uid_unavailable"] };
  }
  const sameNodeUID = targetWrite.uid === currentUID;
  if (laterWrites.length === 0) {
    return { sameNodeUID, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: [sameNodeUID ? "audit_target_owned_uid_matches_current" : "audit_target_owned_uid_differs_current", "later_writer_unavailable"] };
  }
  if (sameNodeUID) {
    const laterWrite = laterWrites.find((entry) => entry.uid === currentUID && entry.ownershipState === "unallocated" && new Set(["patch", "update"]).has(entry.verb));
    if (!laterWrite) {
      return { sameNodeUID: true, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: ["audit_target_owned_uid_matches_current", "ownership_reverting_write_unavailable"] };
    }
    const exactWriter = safeSubject(auditSubject(laterWrite.raw));
    const exactController = controllerFromRecord(laterWrite.raw);
    const exactOwnedFields = [...new Set([
      ...writerFields(managedFields, exactController, laterWrite.time),
      ...auditRequestOwnedFields(laterWrite.raw)
    ])].sort();
    const taintPath = `spec.taints[key=${TAINT_KEY},effect=NoSchedule]`;
    if (exactWriter === "unavailable" || exactController === "unavailable" ||
      !exactOwnedFields.some((field) => field === taintPath || field.startsWith(`${taintPath}.`))) {
      return { sameNodeUID: true, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: ["audit_target_owned_uid_matches_current", "writer_owned_fields_unavailable"] };
    }
    return {
      sameNodeUID: true,
      exactWriter,
      exactController,
      exactWriteTime: laterWrite.time,
      exactOwnedFields,
      rootCause: "same_node_ownership_reverted",
      evidence: ["audit_target_owned_uid_matches_current", "audit_later_unallocated_write", ...(exactOwnedFields.length ? ["managed_fields_writer_matches"] : [])]
    };
  }
  const laterWrite = laterWrites.find((entry) => entry.verb === "create" && entry.uid === currentUID);
  if (!laterWrite) {
    return { sameNodeUID: false, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: ["audit_target_owned_uid_differs_current", "current_uid_create_writer_unavailable"] };
  }
  const exactWriter = safeSubject(auditSubject(laterWrite.raw));
  const exactController = controllerFromRecord(laterWrite.raw);
  const exactOwnedFields = writerFields(managedFields, exactController, laterWrite.time);
  if (exactWriter === "unavailable" || exactController === "unavailable" || exactOwnedFields.length === 0) {
    return { sameNodeUID: false, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: ["audit_target_owned_uid_differs_current", "recreation_owned_fields_unavailable"] };
  }
  return {
    sameNodeUID: false,
    exactWriter,
    exactController,
    exactWriteTime: laterWrite.time,
    exactOwnedFields,
    rootCause: "node_deleted_and_recreated",
    evidence: ["audit_target_owned_uid_differs_current", "audit_current_uid_create"]
  };
}

export function projectNodeDriftDiagnostic(input) {
  const identity = deriveIdentity(input);
  if (input.node?.metadata?.name !== identity.allocation.nodeName || input.machine?.metadata?.name !== identity.allocation.machineName ||
    input.nodePool?.metadata?.name !== identity.allocation.nodePoolId) {
    throw diagnosticError("node_drift_kubernetes_identity_mismatch", "identity.kubernetes.nodeMachineNodePool",
      sha256(JSON.stringify([identity.allocation.nodeName, identity.allocation.machineName, identity.allocation.nodePoolId])),
      sha256(JSON.stringify([input.node?.metadata?.name || "", input.machine?.metadata?.name || "", input.nodePool?.metadata?.name || ""])),
      "kubernetes.authoritative-get");
  }
  const currentOwnership = nodeOwnership(input.node, identity);
  const nodeBase = resourceProjection(input.node, { nodeOnly: true });
  const events = projectEvents(input.events, identity.allocation.nodeName, input.node?.metadata?.uid);
  const auditRecords = relevantAuditRecords(input.audit, identity);
  const packet = rootCausePacket(identity, input.node, nodeBase.managedFields, auditRecords, events);
  const identified = packet.rootCause !== "DIAGNOSTIC_INCONCLUSIVE";
  return {
    schemaVersion: 1,
    operationMode: OPERATION_MODE,
    status: identified ? "root_cause_identified" : "diagnostic_inconclusive",
    collectedAt: safeTime(input.collectedAt),
    launchOperationDigest: sha256(input.launchOperationId),
    identity: {
      controlPlaneUnique: true,
      fabricUnique: true,
      bindingMatches: true,
      accountDigest: sha256(identity.accountId),
      ownerUserDigest: sha256(identity.ownerUserId),
      ownerEmailDigest: sha256(identity.ownerEmail),
      persistedSub2APIBindingMatches: identity.persistedSub2APIBindingMatches === true,
      workspaceDigest: sha256(identity.workspaceId),
      computeAllocationDigest: sha256(identity.allocation.id),
      machineDigest: sha256(identity.allocation.machineName),
      nodePoolDigest: sha256(identity.allocation.nodePoolId),
      nodeDigest: sha256(identity.allocation.nodeName)
    },
    node: { ...nodeBase, ownershipLabels: currentOwnership.labels, workspaceTaint: currentOwnership.taint },
    machine: resourceProjection(input.machine),
    nodePool: resourceProjection(input.nodePool),
    events,
    audit: projectAudit(input.audit, auditRecords),
    readCalls: {
      controlPlane: Number(input.controlPlaneReadCalls || 0),
      fabric: 3,
      tencent: Number(input.tencentReadCalls || 0),
      kubernetes: Number(input.kubernetesReadCalls || 0)
    },
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    diagnostic: {
      failureBoundary: identified ? "none" : "production.node_drift_diagnostic",
      reasonCode: identified ? packet.rootCause : "diagnostic_inconclusive",
      firstFalsePredicate: identified ? "none" : "nodeDrift.exactWriterEvidence",
      expected: identified ? "none" : "audit_uid_and_writer_owned_fields",
      actual: identified ? "none" : "unavailable",
      authority: "control_plane_fabric_kubernetes_tke_audit_get",
      mutationOutcome: { attempted: 0, accepted: 0, confirmed: 0, unknown: 0 }
    },
    rootCausePacket: packet
  };
}

function assertExactKeys(value, keys, code) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    JSON.stringify(Object.keys(value).sort()) !== JSON.stringify([...keys].sort())) throw new Error(code);
}

export function assertNodeDriftDiagnosticArtifact(value) {
  assertExactKeys(value, ["schemaVersion", "operationMode", "status", "collectedAt", "launchOperationDigest", "identity", "node", "machine", "nodePool", "events", "audit", "readCalls", "mutationCounts", "diagnostic", "rootCausePacket"], "node_drift_artifact_shape_invalid");
  if (value.schemaVersion !== 1 || value.operationMode !== OPERATION_MODE || !new Set(["root_cause_identified", "diagnostic_inconclusive"]).has(value.status) ||
    !DIGEST.test(value.launchOperationDigest) || value.mutationCounts?.sub2api !== 0 || value.mutationCounts?.tencent !== 0 || value.mutationCounts?.kubernetes !== 0) {
    throw new Error("node_drift_artifact_invalid");
  }
  assertExactKeys(value.identity, ["controlPlaneUnique", "fabricUnique", "bindingMatches", "accountDigest", "ownerUserDigest", "ownerEmailDigest", "persistedSub2APIBindingMatches", "workspaceDigest", "computeAllocationDigest", "machineDigest", "nodePoolDigest", "nodeDigest"], "node_drift_artifact_identity_shape_invalid");
  for (const key of ["accountDigest", "ownerUserDigest", "ownerEmailDigest", "workspaceDigest", "computeAllocationDigest", "machineDigest", "nodePoolDigest", "nodeDigest"]) {
    if (!DIGEST.test(value.identity?.[key])) throw new Error("node_drift_artifact_identity_invalid");
  }
  for (const key of ["controlPlaneUnique", "fabricUnique", "bindingMatches", "persistedSub2APIBindingMatches"]) {
    if (typeof value.identity[key] !== "boolean") throw new Error("node_drift_artifact_identity_invalid");
  }
  const assertOwner = (owner) => {
    assertExactKeys(owner, ["apiVersion", "kind", "nameDigest", "uidDigest", "controller", "blockOwnerDeletion"], "node_drift_artifact_owner_shape_invalid");
    if (!DIGEST.test(owner.nameDigest) || !DIGEST.test(owner.uidDigest) || typeof owner.controller !== "boolean" || typeof owner.blockOwnerDeletion !== "boolean") throw new Error("node_drift_artifact_owner_invalid");
  };
  const assertManaged = (entry) => {
    assertExactKeys(entry, ["manager", "operation", "apiVersion", "subresource", "time", "fields"], "node_drift_artifact_managed_fields_shape_invalid");
    if (!SAFE_MANAGER.test(entry.manager) || !new Set(["Apply", "Update"]).has(entry.operation) || !Array.isArray(entry.fields) || entry.fields.some((field) => !safeStructuralField(field))) throw new Error("node_drift_artifact_managed_fields_invalid");
  };
  const assertResource = (resource, node = false) => {
    assertExactKeys(resource, node
      ? ["available", "uidDigest", "creationTimestamp", "resourceVersion", "owners", "managedFields", "ownershipLabels", "workspaceTaint"]
      : ["available", "uidDigest", "creationTimestamp", "resourceVersion", "owners", "managedFields"], "node_drift_artifact_resource_shape_invalid");
    if (typeof resource.available !== "boolean" || !Array.isArray(resource.owners) || !Array.isArray(resource.managedFields)) throw new Error("node_drift_artifact_resource_invalid");
    resource.owners.forEach(assertOwner);
    resource.managedFields.forEach(assertManaged);
    if (node) {
      assertExactKeys(resource.ownershipLabels, ["matches", "currentMatch"], "node_drift_artifact_node_labels_shape_invalid");
      assertExactKeys(resource.workspaceTaint, ["count", "effectMatches", "state", "currentMatch"], "node_drift_artifact_node_taint_shape_invalid");
      assertExactKeys(resource.ownershipLabels.matches, OWNERSHIP_LABELS, "node_drift_artifact_node_label_matches_shape_invalid");
      if (Object.values(resource.ownershipLabels.matches).some((matches) => typeof matches !== "boolean") ||
        typeof resource.ownershipLabels.currentMatch !== "boolean" || !Number.isInteger(resource.workspaceTaint.count) || resource.workspaceTaint.count < 0 ||
        typeof resource.workspaceTaint.effectMatches !== "boolean" || typeof resource.workspaceTaint.currentMatch !== "boolean" ||
        !new Set(["target_owned", "unallocated", "conflict"]).has(resource.workspaceTaint.state)) throw new Error("node_drift_artifact_node_ownership_invalid");
    }
  };
  assertResource(value.node, true);
  assertResource(value.machine);
  assertResource(value.nodePool);
  if (!Array.isArray(value.events)) throw new Error("node_drift_artifact_events_invalid");
  for (const event of value.events) {
    assertExactKeys(event, ["reason", "reportingController", "action", "nodeUidDigest", "currentNodeUID", "eventTime", "firstTimestamp", "lastTimestamp"], "node_drift_artifact_event_shape_invalid");
    if (typeof event.currentNodeUID !== "boolean" || event.nodeUidDigest !== "unavailable" && !DIGEST.test(event.nodeUidDigest)) throw new Error("node_drift_artifact_event_invalid");
  }
  assertExactKeys(value.audit, ["status", "records"], "node_drift_artifact_audit_shape_invalid");
  if (!new Set(["enabled", "disabled", "unavailable"]).has(value.audit.status) || !Array.isArray(value.audit.records)) throw new Error("node_drift_artifact_audit_invalid");
  for (const record of value.audit.records) {
    assertExactKeys(record, ["verb", "subject", "userAgent", "time", "nodeUidDigest", "ownershipState"], "node_drift_artifact_audit_record_shape_invalid");
    if (!new Set(["patch", "update", "create", "delete"]).has(record.verb) || !new Set(["target_owned", "unallocated", "conflict", "unavailable"]).has(record.ownershipState) ||
      record.nodeUidDigest !== "unavailable" && !DIGEST.test(record.nodeUidDigest) ||
      record.subject !== "unavailable" && !DIGEST.test(record.subject) && !SYSTEM_SERVICE_ACCOUNT.test(record.subject) ||
      record.userAgent !== "unavailable" && !DIGEST.test(record.userAgent)) throw new Error("node_drift_artifact_audit_record_invalid");
  }
  assertExactKeys(value.readCalls, ["controlPlane", "fabric", "tencent", "kubernetes"], "node_drift_artifact_read_calls_shape_invalid");
  if (Object.values(value.readCalls).some((count) => !Number.isInteger(count) || count < 0)) throw new Error("node_drift_artifact_read_calls_invalid");
  assertExactKeys(value.mutationCounts, ["sub2api", "tencent", "kubernetes"], "node_drift_artifact_mutation_counts_shape_invalid");
  assertExactKeys(value.diagnostic, ["failureBoundary", "reasonCode", "firstFalsePredicate", "expected", "actual", "authority", "mutationOutcome"], "node_drift_artifact_diagnostic_shape_invalid");
  assertExactKeys(value.diagnostic.mutationOutcome, ["attempted", "accepted", "confirmed", "unknown"], "node_drift_artifact_mutation_outcome_shape_invalid");
  if (Object.values(value.diagnostic.mutationOutcome).some((count) => count !== 0) ||
    ["failureBoundary", "reasonCode", "firstFalsePredicate", "expected", "actual", "authority"].some((key) =>
      !/^(?:sha256:[a-f0-9]{64}|[A-Za-z0-9._+-]+)$/.test(String(value.diagnostic[key] || "")))) {
    throw new Error("node_drift_artifact_mutation_outcome_invalid");
  }
  const packet = value.rootCausePacket;
  assertExactKeys(packet, ["sameNodeUID", "exactWriter", "exactController", "exactWriteTime", "exactOwnedFields", "rootCause", "evidence"], "node_drift_artifact_root_cause_shape_invalid");
  if (![true, false, null].includes(packet?.sameNodeUID) || typeof packet?.exactWriter !== "string" || typeof packet?.exactController !== "string" ||
    typeof packet?.exactWriteTime !== "string" || !Array.isArray(packet?.exactOwnedFields) || !Array.isArray(packet?.evidence) ||
    !new Set(["same_node_ownership_reverted", "node_deleted_and_recreated", "DIAGNOSTIC_INCONCLUSIVE"]).has(packet?.rootCause) ||
    (value.status === "diagnostic_inconclusive") !== (packet.rootCause === "DIAGNOSTIC_INCONCLUSIVE")) {
    throw new Error("node_drift_artifact_root_cause_invalid");
  }
  if (packet.exactOwnedFields.some((field) => !safeStructuralField(field)) || packet.evidence.some((item) => !/^[A-Za-z0-9._-]+$/.test(String(item))) ||
    packet.exactWriter !== "unavailable" && !DIGEST.test(packet.exactWriter) && !SYSTEM_SERVICE_ACCOUNT.test(packet.exactWriter) ||
    packet.exactController !== "unavailable" && safeManager(packet.exactController) !== packet.exactController ||
    value.status === "root_cause_identified" && (!value.identity.controlPlaneUnique || !value.identity.fabricUnique || !value.identity.bindingMatches || !value.identity.persistedSub2APIBindingMatches)) {
    throw new Error("node_drift_artifact_root_cause_invalid");
  }
  const encoded = JSON.stringify(value);
  if (/workspace-launch-|\bacct-|\bws-|\bca_|\bins-|\bnp-|(?:\d{1,3}\.){3}\d{1,3}|[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}|\bBearer\s+[^"\s]+|"(?:message|labels|token|secret|password|privateIp|nodeName|machineName|nodePoolId|accountId|workspaceId|computeAllocationId|topicId|logsetId|requestObject|responseObject)"\s*:/i.test(encoded)) {
    throw new Error("node_drift_artifact_sensitive_value_forbidden");
  }
  return value;
}

function safeFailureArtifact(error = new Error("node_drift_collection_failed"), launchOperationId = "") {
  const errorCode = error instanceof Error ? error.message : "node_drift_collection_failed";
  const safeCode = /^node_drift_[a-z0-9_]+$/.test(errorCode) ? errorCode : "node_drift_collection_failed";
  const failure = error?.diagnostic || {
    failureBoundary: "production.node_drift_diagnostic",
    reasonCode: safeCode,
    firstFalsePredicate: "nodeDrift.authorityReadback",
    expected: "authoritative_snapshot",
    actual: "unavailable",
    authority: "production.runner"
  };
  return {
    schemaVersion: 1,
    operationMode: OPERATION_MODE,
    status: "diagnostic_inconclusive",
    collectedAt: new Date().toISOString(),
    launchOperationDigest: sha256(launchOperationId || "unavailable"),
    identity: {
      controlPlaneUnique: false, fabricUnique: false, bindingMatches: false, persistedSub2APIBindingMatches: false,
      ...Object.fromEntries(["accountDigest", "ownerUserDigest", "ownerEmailDigest", "workspaceDigest", "computeAllocationDigest", "machineDigest", "nodePoolDigest", "nodeDigest"].map((key) => [key, sha256("unavailable")]))
    },
    node: {
      available: false, uidDigest: "unavailable", creationTimestamp: "unavailable", resourceVersion: "unavailable", owners: [], managedFields: [],
      ownershipLabels: { matches: Object.fromEntries(OWNERSHIP_LABELS.map((key) => [key, false])), currentMatch: false },
      workspaceTaint: { count: 0, effectMatches: false, state: "conflict", currentMatch: false }
    },
    machine: { available: false, uidDigest: "unavailable", creationTimestamp: "unavailable", resourceVersion: "unavailable", owners: [], managedFields: [] },
    nodePool: { available: false, uidDigest: "unavailable", creationTimestamp: "unavailable", resourceVersion: "unavailable", owners: [], managedFields: [] },
    events: [],
    audit: { status: "unavailable", records: [] },
    readCalls: { controlPlane: 0, fabric: 0, tencent: 0, kubernetes: 0 },
    mutationCounts: { sub2api: 0, tencent: 0, kubernetes: 0 },
    diagnostic: { ...failure, mutationOutcome: { attempted: 0, accepted: 0, confirmed: 0, unknown: 0 } },
    rootCausePacket: { sameNodeUID: null, exactWriter: "unavailable", exactController: "unavailable", exactWriteTime: "unavailable", exactOwnedFields: [], rootCause: "DIAGNOSTIC_INCONCLUSIVE", evidence: [safeCode] }
  };
}

function cliArgs(argv) {
  const result = {};
  for (let index = 0; index < argv.length; index += 1) {
    if (!argv[index].startsWith("--")) continue;
    result[argv[index].slice(2)] = argv[index + 1] && !argv[index + 1].startsWith("--") ? argv[++index] : "true";
  }
  return result;
}

async function httpJSON(url, options = {}) {
  const response = await fetch(url, { ...options, signal: AbortSignal.timeout(30_000) });
  const body = await response.text();
  if (body.length > 8 * 1024 * 1024) throw new Error("node_drift_response_too_large");
  let payload;
  try { payload = JSON.parse(body); } catch { throw new Error("node_drift_response_invalid"); }
  if (!response.ok) throw new Error("node_drift_read_failed");
  return { payload, response };
}

function postgresReadEnv(databaseURL) {
  let parsed;
  try { parsed = new URL(databaseURL); } catch { throw diagnosticError("node_drift_original_launch_store_unavailable", "identity.originalLaunch.persistedOperation", "exact_one", "unavailable", "control-plane.postgresql"); }
  if (!new Set(["postgres:", "postgresql:"]).has(parsed.protocol) || !parsed.hostname || !parsed.username || !parsed.pathname.slice(1)) {
    throw diagnosticError("node_drift_original_launch_store_unavailable", "identity.originalLaunch.persistedOperation", "exact_one", "unavailable", "control-plane.postgresql");
  }
  const env = {
    PATH: process.env.PATH,
    PGHOST: parsed.hostname,
    PGPORT: parsed.port || "5432",
    PGUSER: decodeURIComponent(parsed.username),
    PGPASSWORD: decodeURIComponent(parsed.password),
    PGDATABASE: decodeURIComponent(parsed.pathname.slice(1)),
    PGAPPNAME: "opl-node-drift-get-only",
    PGCONNECT_TIMEOUT: "10",
    PGOPTIONS: "-c default_transaction_read_only=on -c statement_timeout=10000"
  };
  for (const key of ["sslmode", "sslrootcert", "sslcert", "sslkey", "sslcrl", "sslcrldir"]) {
    const value = parsed.searchParams.get(key);
    if (value) env[`PG${key.toUpperCase()}`] = value;
  }
  return env;
}

function readControlPlaneIdentity(databaseURL, launchOperationId) {
  const query = `
    SELECT json_build_object(
      'operationId', launch.id,
      'accountId', launch.account_id,
      'ownerUserId', launch.result::jsonb->>'ownerUserId',
      'workspaceId', launch.workspace_id,
      'computeAllocationId', launch.result::jsonb->>'computeAllocationId',
      'updatedAt', to_char(launch.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
      'executeStartedAt', launch.result::jsonb->'recoveryExecution'->>'startedAt',
      'launchSub2apiUserId', launch.result::jsonb->'chargeConfirmation'->>'userId',
      'accountOwnerUserId', account.owner_user_id,
      'sub2apiUserId', account.sub2api_user_id::text,
      'accountStatus', account.status,
      'controlPlaneUserId', owner.id,
      'controlPlaneUserAccountId', owner.account_id,
      'controlPlaneUserEmail', lower(trim(owner.email)),
      'controlPlaneUserRole', owner.role,
      'controlPlaneUserStatus', owner.status
    )::text
    FROM control_plane_runtime_operations launch
    LEFT JOIN control_plane_accounts account ON account.id = launch.account_id
    LEFT JOIN control_plane_users owner ON owner.id = launch.result::jsonb->>'ownerUserId'
    WHERE launch.id = :'launch_id' AND launch.action = 'workspace.launch.v2'
  `;
  const result = spawnSync("psql", ["--no-psqlrc", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1", `--set=launch_id=${launchOperationId}`, "--command", query], {
    encoding: "utf8",
    env: postgresReadEnv(databaseURL),
    maxBuffer: 1024 * 1024
  });
  if (result.status !== 0) {
    throw diagnosticError("node_drift_original_launch_store_unavailable", "identity.originalLaunch.persistedOperation", "exact_one", "unavailable", "control-plane.postgresql");
  }
  const rows = result.stdout.split(/\r?\n/).map((value) => value.trim()).filter(Boolean);
  if (rows.length !== 1) {
    throw diagnosticError("node_drift_original_launch_store_unavailable", "identity.originalLaunch.persistedOperation", "exact_one", rows.length === 0 ? "absent" : "multiple", "control-plane.postgresql");
  }
  let row;
  try { row = JSON.parse(rows[0]); } catch { throw diagnosticError("node_drift_original_launch_store_invalid", "identity.originalLaunch.persistedOperation", "valid", "invalid", "control-plane.postgresql"); }
  if (row?.operationId !== launchOperationId || !row?.accountId || !row?.ownerUserId || !row?.workspaceId || !row?.computeAllocationId ||
    !safeTime(row?.updatedAt).startsWith("20") || !safeTime(row?.executeStartedAt).startsWith("20")) {
    throw diagnosticError("node_drift_original_launch_store_invalid", "identity.originalLaunch.requiredIdentity", "complete", "incomplete", "control-plane.postgresql");
  }
  const expectedBinding = `${row.accountId}\n${row.ownerUserId}`;
  const actualBinding = `${row.controlPlaneUserAccountId || ""}\n${row.accountOwnerUserId || ""}`;
  if (row.accountOwnerUserId !== row.ownerUserId || row.controlPlaneUserId !== row.ownerUserId || row.controlPlaneUserAccountId !== row.accountId ||
    row.accountStatus !== "active" || row.controlPlaneUserStatus !== "active" || row.controlPlaneUserRole !== "owner" || !row.controlPlaneUserEmail) {
    throw diagnosticError("node_drift_control_plane_identity_mismatch", "identity.controlPlane.accountOwnerUser", sha256(expectedBinding), sha256(actualBinding), "control-plane.postgresql");
  }
  const accountSub2APIUserID = String(row.sub2apiUserId || "");
  const launchSub2APIUserID = String(row.launchSub2apiUserId || "");
  if (!/^[1-9][0-9]*$/.test(accountSub2APIUserID) || launchSub2APIUserID !== accountSub2APIUserID) {
    throw diagnosticError("node_drift_persisted_sub2api_binding_mismatch", "identity.originalLaunch.persistedSub2APIUserIdBinding",
      accountSub2APIUserID ? sha256(accountSub2APIUserID) : "absent", launchSub2APIUserID ? sha256(launchSub2APIUserID) : "absent",
      "control_plane.persisted_sub2api_binding");
  }
  return {
    launch: {
      operationId: row.operationId,
      accountId: row.accountId,
      ownerUserId: row.ownerUserId,
      workspaceId: row.workspaceId,
      computeAllocationId: row.computeAllocationId,
      updatedAt: row.updatedAt,
      executeStartedAt: row.executeStartedAt
    },
    control: {
      accountId: row.accountId,
      workspaceId: row.workspaceId,
      ownerUserId: row.ownerUserId,
      ownerEmail: row.controlPlaneUserEmail,
      sub2APIUserId: accountSub2APIUserID,
      persistedSub2APIBindingMatches: true
    }
  };
}

async function collectControlPlane(databaseURL, launchOperationId, expectedCustomerEmail) {
  const { launch: originalLaunch, control } = readControlPlaneIdentity(databaseURL, launchOperationId);
  assertOriginalLaunchOwner(originalLaunch, control);
  const normalizedExpectedEmail = String(expectedCustomerEmail || "").trim().toLowerCase();
  if (!normalizedExpectedEmail || !control.ownerEmail) {
    throw diagnosticError("node_drift_approved_customer_identity_mismatch", "identity.normalizedEmailDigest", APPROVED_CUSTOMER_EMAIL_DIGEST, "absent", "control-plane.account-user+production-secret");
  }
  const emailDigests = [normalizedExpectedEmail, control.ownerEmail].map((value) => sha256(String(value).trim().toLowerCase()));
  assertApprovedCustomerEmailDigests(emailDigests);
  return { controlPlaneIdentity: control, controlPlaneEmailDigests: emailDigests, launch: originalLaunch, readCalls: 1 };
}

async function fabricGET(origin, token, path) {
  if (origin !== "http://127.0.0.1:18082" || !FABRIC_GET_PATH.test(path)) throw new Error("node_drift_fabric_path_forbidden");
  return (await httpJSON(`${origin}${path}`, { headers: { Authorization: `Bearer ${token}` } })).payload;
}

function kubectl(kubeconfig, args, { json = true, optional = false } = {}) {
  if (!READ_ONLY_KUBECTL.has(args[0])) throw new Error("node_drift_kubectl_mutation_forbidden");
  const result = spawnSync("kubectl", ["--kubeconfig", kubeconfig, ...args], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
  if (result.status !== 0) {
    if (optional) return null;
    throw new Error("node_drift_kubernetes_get_failed");
  }
  if (!json) return result.stdout;
  try { return JSON.parse(result.stdout); } catch { throw new Error("node_drift_kubernetes_response_invalid"); }
}

function exactResource(apiResources, pattern) {
  const matches = apiResources.split(/\r?\n/).map((value) => value.trim()).filter((value) => pattern.test(value));
  return matches.length === 1 ? matches[0] : "";
}

function getTkeResource(kubeconfig, resource, name) {
  if (!resource) return null;
  return kubectl(kubeconfig, ["get", resource, name, "-o", "json"], { optional: true }) ||
    kubectl(kubeconfig, ["get", resource, name, "--all-namespaces", "-o", "json"], { optional: true });
}

function hmac(key, value) {
  return createHmac("sha256", key).update(value).digest();
}

async function tencentRead({ service, action, version, region, secretId, secretKey, body }) {
  if (!READ_ONLY_TENCENT_ACTIONS.has(action)) throw new Error("node_drift_tencent_mutation_forbidden");
  const host = `${service}.tencentcloudapi.com`;
  const timestamp = Math.floor(Date.now() / 1000);
  const date = new Date(timestamp * 1000).toISOString().slice(0, 10);
  const encoded = JSON.stringify(body);
  const canonicalHeaders = `content-type:application/json\nhost:${host}\n`;
  const canonicalRequest = ["POST", "/", "", canonicalHeaders, "content-type;host", createHash("sha256").update(encoded).digest("hex")].join("\n");
  const scope = `${date}/${service}/tc3_request`;
  const stringToSign = ["TC3-HMAC-SHA256", String(timestamp), scope, createHash("sha256").update(canonicalRequest).digest("hex")].join("\n");
  const signing = hmac(hmac(hmac(`TC3${secretKey}`, date), service), "tc3_request");
  const signature = createHmac("sha256", signing).update(stringToSign).digest("hex");
  const response = await httpJSON(`https://${host}`, { method: "POST", headers: {
    Authorization: `TC3-HMAC-SHA256 Credential=${secretId}/${scope}, SignedHeaders=content-type;host, Signature=${signature}`,
    "Content-Type": "application/json", Host: host, "X-TC-Action": action, "X-TC-Timestamp": String(timestamp), "X-TC-Version": version, "X-TC-Region": region
  }, body: encoded });
  if (response.payload?.Response?.Error) throw new Error("node_drift_tencent_read_failed");
  return response.payload?.Response;
}

export function parseAuditResults(results) {
  const records = [];
  for (const result of results || []) {
    try {
      const parsed = JSON.parse(String(result?.LogJson || ""));
      if (parsed && typeof parsed === "object" && parsed.objectRef) records.push(parsed);
    } catch { /* raw audit content is intentionally discarded */ }
  }
  return records;
}

async function collectAudit(env, nodeName, operationUpdatedAt) {
  const required = [env.TENCENTCLOUD_SECRET_ID, env.TENCENTCLOUD_SECRET_KEY, env.TENCENTCLOUD_REGION, env.TENCENT_DEPLOY_CLUSTER_ID];
  if (required.some((value) => !String(value || "").trim())) {
    throw diagnosticError("node_drift_tke_audit_configuration_unavailable", "nodeDrift.audit.configuration", "complete", "unavailable", "production.environment");
  }
  let readCalls = 0;
  readCalls += 1;
  const switches = await tencentRead({ service: "tke", action: "DescribeLogSwitches", version: "2018-05-25", region: env.TENCENTCLOUD_REGION,
    secretId: env.TENCENTCLOUD_SECRET_ID, secretKey: env.TENCENTCLOUD_SECRET_KEY,
    body: { ClusterIds: [env.TENCENT_DEPLOY_CLUSTER_ID], ClusterType: "tke" } });
  const current = exactOne((switches?.SwitchSet || []).filter((item) => item?.ClusterId === env.TENCENT_DEPLOY_CLUSTER_ID), "node_drift_tke_switch_not_unique");
  if (current?.Audit?.Enable !== true || current?.Audit?.Status !== "opened" || !current?.Audit?.TopicId) {
    return { audit: { status: "disabled", records: [] }, readCalls };
  }
  const center = Date.parse(operationUpdatedAt);
  if (!Number.isFinite(center)) throw diagnosticError("node_drift_audit_window_invalid", "nodeDrift.audit.window", "valid_launch_updated_at", "invalid", "control-plane.postgresql");
  readCalls += 1;
  const logs = await tencentRead({ service: "cls", action: "SearchLog", version: "2020-10-16", region: current.Audit.TopicRegion || env.TENCENTCLOUD_REGION,
    secretId: env.TENCENTCLOUD_SECRET_ID, secretKey: env.TENCENTCLOUD_SECRET_KEY,
    body: { TopicId: current.Audit.TopicId, From: center - 15 * 60_000, To: center + 15 * 60_000, Query: `\"${nodeName.replaceAll("\"", "")}\"`, Limit: 100, Sort: "asc" } });
  return { audit: { status: "enabled", records: parseAuditResults(logs?.Results) }, readCalls };
}

function launchOperationID(args) {
  const launchOperationId = String(args["launch-operation-id"] || "");
  if (!/^workspace-launch-[A-Za-z0-9-]+$/.test(launchOperationId)) throw new Error("node_drift_launch_operation_invalid");
  return launchOperationId;
}

async function collectControlPlanePreflight(args, env) {
  const launchOperationId = launchOperationID(args);
  return collectControlPlane(
    env.DATABASE_URL,
    launchOperationId,
    env.OPL_NODE_DRIFT_EXPECTED_CUSTOMER_EMAIL
  );
}

function assertControlPlanePreflight(cp, launchOperationId) {
  if (cp?.launch?.operationId !== launchOperationId || !safeTime(cp?.launch?.executeStartedAt).startsWith("20") || !Number.isInteger(cp?.readCalls) || cp.readCalls < 1) {
    throw diagnosticError("node_drift_control_plane_preflight_invalid", "identity.controlPlane.preflightReadback", sha256(launchOperationId),
      cp?.launch?.operationId ? sha256(cp.launch.operationId) : "absent", "runner-temporary-readonly-projection");
  }
  assertOriginalLaunchOwner(cp.launch, cp.controlPlaneIdentity);
  assertApprovedCustomerEmailDigests(cp.controlPlaneEmailDigests);
  return cp;
}

async function collect(args, env) {
  const launchOperationId = launchOperationID(args);
  const identityPath = String(args.identity || "");
  if (!identityPath.startsWith("/") || !identityPath.includes("production-node-drift-diagnostic-raw/control-plane-identity.json")) {
    throw new Error("node_drift_control_plane_preflight_path_invalid");
  }
  const cp = assertControlPlanePreflight(JSON.parse(await readFile(identityPath, "utf8")), launchOperationId);
  const token = (await readFile(env.OPL_INTERNAL_SERVICE_TOKEN_PATH, "utf8")).trim();
  if (!token) throw new Error("node_drift_fabric_token_unavailable");
  const operations = await fabricGET(env.OPL_FABRIC_INTERNAL_ORIGIN, token, "/fabric/operations");
  const operationRef = `${launchOperationId}:compute`;
  const operationMatches = (operations || []).filter((operation) => operation?.action === "create_compute_allocation" &&
    (operation?.operationId === operationRef || operation?.idempotencyKey === operationRef));
  if (operationMatches.length !== 1) {
    throw diagnosticError("node_drift_fabric_compute_operation_not_unique", "identity.fabric.computeOperation", "exact_one",
      operationMatches.length === 0 ? "absent" : "multiple", "fabric.operation-store-get");
  }
  const preliminary = operationMatches[0];
  const allocation = await fabricGET(env.OPL_FABRIC_INTERNAL_ORIGIN, token, `/fabric/compute-allocations/${encodeURIComponent(preliminary.resourceId)}`);
  const ownership = await fabricGET(env.OPL_FABRIC_INTERNAL_ORIGIN, token, `/fabric/machine-ownerships/${encodeURIComponent(preliminary.resourceId)}`);
  const identity = deriveIdentity({ launchOperationId, controlPlaneIdentity: cp.controlPlaneIdentity, controlPlaneEmailDigests: cp.controlPlaneEmailDigests, controlPlaneLaunch: cp.launch, fabricOperations: operations, allocation, ownership });
  const kubeconfig = String(env.OPL_NODE_DRIFT_KUBECONFIG || "");
  const apiResources = kubectl(kubeconfig, ["api-resources", "--verbs=get,list", "-o", "name"], { json: false });
  const machineResource = exactResource(apiResources, /^machines(?:\.[a-z0-9.-]+)?$/);
  const nodePoolResource = exactResource(apiResources, /^(?:machinesets|nodepools)(?:\.[a-z0-9.-]+)?$/);
  const node = kubectl(kubeconfig, ["get", "node", identity.allocation.nodeName, "-o", "json"]);
  const machine = getTkeResource(kubeconfig, machineResource, identity.allocation.machineName);
  const nodePool = getTkeResource(kubeconfig, nodePoolResource, identity.allocation.nodePoolId);
  if (!machine || !nodePool) throw diagnosticError("node_drift_tke_resource_unavailable", "identity.kubernetes.machineNodePool", "both_present",
    !machine && !nodePool ? "both_absent" : !machine ? "machine_absent" : "node_pool_absent", "kubernetes.authoritative-get");
  const events = kubectl(kubeconfig, ["get", "events", "--all-namespaces", "--field-selector", `involvedObject.kind=Node,involvedObject.name=${identity.allocation.nodeName}`, "-o", "json"]);
  const audit = await collectAudit(env, identity.allocation.nodeName, identity.launch.executeStartedAt);
  return projectNodeDriftDiagnostic({
    launchOperationId, controlPlaneIdentity: cp.controlPlaneIdentity, controlPlaneEmailDigests: cp.controlPlaneEmailDigests, controlPlaneLaunch: cp.launch, controlPlaneReadCalls: cp.readCalls,
    fabricOperations: operations, allocation, ownership, node, machine, nodePool, events,
    audit: audit.audit, tencentReadCalls: audit.readCalls, kubernetesReadCalls: 5, collectedAt: new Date().toISOString()
  });
}

async function main() {
  const args = cliArgs(process.argv.slice(2));
  if (args.validate === "true") {
    assertNodeDriftDiagnosticArtifact(JSON.parse(await readFile(args.artifact, "utf8")));
    return;
  }
  const outputPath = String(args.out || "");
  if (!outputPath.startsWith("/") || !outputPath.includes("production-node-drift-diagnostic")) throw new Error("node_drift_output_path_invalid");
  const identityOutputPath = String(args["identity-out"] || "");
  if (identityOutputPath) {
    if (!identityOutputPath.startsWith("/") || !identityOutputPath.includes("production-node-drift-diagnostic-raw/control-plane-identity.json")) {
      throw new Error("node_drift_control_plane_preflight_path_invalid");
    }
    try {
      const preflight = await collectControlPlanePreflight(args, process.env);
      await writeFile(identityOutputPath, `${JSON.stringify(preflight)}\n`, { mode: 0o600 });
      await chmod(identityOutputPath, 0o600);
    } catch (error) {
      const artifact = safeFailureArtifact(error, args["launch-operation-id"]);
      assertNodeDriftDiagnosticArtifact(artifact);
      await writeFile(outputPath, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
      await chmod(outputPath, 0o600);
      process.exitCode = 1;
    }
    return;
  }
  let artifact;
  try {
    artifact = await collect(args, process.env);
  } catch (error) {
    artifact = safeFailureArtifact(error, args["launch-operation-id"]);
    process.exitCode = 1;
  }
  assertNodeDriftDiagnosticArtifact(artifact);
  await writeFile(outputPath, `${JSON.stringify(artifact, null, 2)}\n`, { mode: 0o600 });
  await chmod(outputPath, 0o600);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(() => { process.stderr.write("node_drift_diagnostic_failed\n"); process.exitCode = 1; });
}
