
import {
  appendEvidenceReceipt,
  appendTaskEvidenceReceipt,
  createEvidenceReceipt,
  createTaskEvidenceReceipt,
  filterTaskEvidenceReceipts
} from "../../../ledger/src/index.js";
import { clone, makeId, money, now } from "./core-utils.js";
import { userIdForAccount } from "./wallet-service.js";
import { latestWorkspaceForAccount } from "./workspace-service.js";
import { OplDomainService } from "./opl-domain-service.js";

export class LedgerEvidenceService extends OplDomainService {
  async recordTaskEvidenceReceipt(input) {
    return this.store.update((state) => {
      if (input.workspaceId) latestWorkspaceForAccount(state, input.accountId, input.workspaceId);
      const receipt = createTaskEvidenceReceipt({
        state,
        ...input
      });
      appendTaskEvidenceReceipt(state, receipt);
      state.audit.push(this.auditEvent({
        accountId: input.accountId,
        workspaceId: input.workspaceId || "",
        type: "ledger.task_evidence_recorded",
        sourceEventId: receipt.id
      }));
      return clone(receipt);
    });
  }

  async taskEvidenceReceipts({ accountId, workspaceId = null, taskId = null }) {
    const state = await this.store.read();
    return filterTaskEvidenceReceipts(state, { accountId, workspaceId, taskId });
  }

  notify({ state, accountId, workspaceId, type, severity, message, sourceEventId }) {
    state.notifications ??= [];
    const event = {
      id: makeId("notification", accountId, workspaceId, type, sourceEventId, String(state.notifications.length)),
      accountId,
      workspaceId,
      type,
      severity,
      message,
      sourceEventId,
      createdAt: now()
    };
    state.notifications.push(event);
    return event;
  }

  ledgerEntry({ state, workspaceId, accountId, type, amount, sourceEventId, holdType, billableHours, metadata }) {
    const sequence = state?.billingLedger?.length ?? 0;
    const userId = userIdForAccount(state, accountId);
    return {
      id: makeId("ledger", accountId, workspaceId, type, sourceEventId, String(sequence)),
      workspaceId,
      accountId,
      userId,
      type,
      amount: money(Number(amount)),
      currency: "CNY",
      sourceEventId,
      ...(holdType ? { holdType } : {}),
      ...(billableHours ? { billableHours } : {}),
      ...(metadata ? { metadata: clone(metadata) } : {}),
      createdAt: now()
    };
  }

  recordEvidence({ state, type, accountId, workspace, packagePlan = null, billingRefs = [], continuation = null }) {
    const effectivePackagePlan = packagePlan || this.getPackage(workspace.packageId);
    const receipt = createEvidenceReceipt({
      state,
      type,
      accountId,
      workspaceId: workspace.id,
      actor: workspace.owner?.userId
        ? { type: "user", id: workspace.owner.userId, organizationId: workspace.owner.organizationId }
        : { type: "account", id: accountId },
      plan: {
        workspaceName: workspace.name,
        packageId: workspace.packageId,
        computeProfile: effectivePackagePlan.server,
        storageGb: effectivePackagePlan.diskGb
      },
      approval: { status: "implicit_console_policy" },
      environment: {
        runtimeProvider: workspace.provider,
        workspaceImage: workspace.docker?.image
      },
      resourceRefs: {
        serverId: workspace.server?.id,
        dockerId: workspace.docker?.id,
        storageId: workspace.disk?.id,
        storageMountPath: workspace.disk?.mountPath,
        urlTokenMode: workspace.access?.mode || "long_lived_url_token",
        tokenStatus: workspace.access?.tokenStatus
      },
      billingRefs: billingRefs.map((entry) => ({
        id: entry.id,
        type: entry.type,
        amount: entry.amount,
        currency: entry.currency
      })),
      continuation
    });
    appendEvidenceReceipt(state, receipt);
    return receipt;
  }

  auditEvent({ accountId, workspaceId = "", type, sourceEventId }) {
    return {
      id: makeId("audit", accountId, workspaceId, type, sourceEventId, String(Date.now())),
      accountId,
      workspaceId,
      type,
      sourceEventId,
      createdAt: now()
    };
  }
}
