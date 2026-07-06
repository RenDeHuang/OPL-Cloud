
import { clone, makeId, now } from "./core-utils.ts";
import { latestWorkspaceForAccount } from "./workspace-service.ts";
import { OplDomainService } from "./opl-domain-service.ts";

export class RuntimeOperationService extends OplDomainService {
  async runtimeStatus({ accountId, workspaceId }) {
    const state = await this.store.read();
    const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
    if (typeof this.runtimeProvider.runtimeStatus === "function") {
      return this.runtimeProvider.runtimeStatus({ workspace: clone(workspace) });
    }
    return {
      provider: workspace.provider,
      workspaceId: workspace.id,
      ready: workspace.state === "running" &&
        workspace.server.status === "running" &&
        workspace.docker.status === "running" &&
        workspace.disk.status === "attached_retained",
      checks: [
        {
          name: "workspace_runtime_running",
          ok: workspace.state === "running" &&
            workspace.server.status === "running" &&
            workspace.docker.status === "running"
        },
        {
          name: "workspace_storage_attached",
          ok: workspace.disk.status === "attached_retained"
        }
      ]
    };
  }

  async runRuntimeOperation({ accountId, workspaceId, operationType, prepare = null, mutate }) {
    let runtimeOperationStarted = false;
    try {
      return await this.store.update(async (state) => {
        const workspace = latestWorkspaceForAccount(state, accountId, workspaceId);
        if (prepare) prepare(state, workspace);
        const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType });
        runtimeOperationStarted = true;
        try {
          return await mutate(state, workspace, operation);
        } catch (error) {
          this.finishRuntimeOperation(operation, "failed", error);
          throw error;
        }
      });
    } catch (error) {
      if (runtimeOperationStarted) {
        await this.recordFailedRuntimeOperation({ accountId, workspaceId, operationType, error });
      }
      throw error;
    }
  }

  startRuntimeOperation({ state, accountId, workspaceId, operationType }) {
    this.runtimeOperationSequence += 1;
    const operation = {
      id: makeId("op", accountId, workspaceId, operationType, String(Date.now()), String(this.runtimeOperationSequence)),
      accountId,
      workspaceId,
      operationType,
      status: "running",
      attempts: 1,
      createdAt: now(),
      updatedAt: now()
    };
    state.runtimeOperations.push(operation);
    return operation;
  }

  finishRuntimeOperation(operation, status, error = null) {
    operation.status = status;
    operation.updatedAt = now();
    if (error) operation.error = error.message;
    return operation;
  }

  async recordFailedRuntimeOperation({ accountId, workspaceId, operationType, error }) {
    return this.store.update((state) => {
      const operation = this.startRuntimeOperation({ state, accountId, workspaceId, operationType });
      return clone(this.finishRuntimeOperation(operation, "failed", error));
    });
  }
}
