# Workspace Runtime Persistence E2E Design

## Status

Approved by continuation of the public staging E2E goal.

## Goal

Extend the production verifier so the public Workspace URL proves the user-visible runtime chain, not only the Console resource chain:

1. Open a real ComputeAllocation.
2. Open a real StorageVolume.
3. Attach storage to compute and deploy one-person-lab-app.
4. Create and open the public Workspace URL.
5. Use the Workspace URL gateway to write or upload a file into the WebUI runtime storage.
6. Read the file back through the WebUI runtime.
7. Destroy the compute allocation while retaining storage.
8. Open a second compute allocation.
9. Reattach the same storage to the second compute allocation.
10. Create and open a second Workspace URL.
11. Read the original file through the second Workspace URL.
12. Clean up compute, attachment, storage, and billable state.

## Boundary

OPL Console owns resource lifecycle, account ownership, billing, URL/token entry, and cleanup.

one-person-lab-app owns file operations, chat UI/API behavior, and runtime workspace semantics. OPL Console must not add a file manager API just to satisfy E2E. The verifier may call the runtime API through the public Workspace URL gateway because that is exactly the user-facing access path.

## Runtime API Contract

The current one-person-lab WebUI implementation is `opl-aion-shell`. Its web host proxies `/api/*` and `/ws` to `aioncore`. Because the verifier is not a browser and does not keep the Workspace gateway cookie jar, every runtime API URL keeps the Workspace URL token query while changing the path from `/w/:workspaceId/` to `/w/:workspaceId/api/...`. The Workspace gateway strips the token before proxying upstream.

The verifier uses only stable WebUI APIs discovered from the current source:

- `GET /api/auth/user`: no-auth session readiness.
- `POST /api/fs/write`: write a text file to an absolute runtime path.
- `POST /api/fs/read`: read a text file from an absolute runtime path.
- `POST /api/fs/upload`: multipart upload with field `file`; optional fields `file_name` and `conversation_id`.
- `POST /api/conversations`: create a runtime conversation.
- `POST /api/conversations/:conversation_id/messages`: send one message.
- `GET /api/conversations/:conversation_id/workspace?path=<relative>`: list workspace files.
- `GET /ws`: runtime event stream.

For the persistence proof, the verifier writes a deterministic text file under `/data/opl-e2e-<runId>.txt` and reads it back before and after compute recreation. This uses the mounted WebUI data root because the current AionCore `/api/fs/write` sandbox allows the default data/work roots; project-file `/projects` write coverage belongs to the WebUI runtime contract, not the OPL Cloud resource-chain verifier.

## Chat Probe

The chat probe is best-effort in this OPL Cloud verifier cycle:

- If the WebUI has a configured model/provider and accepts a runtime conversation/message, the verifier waits for a terminal runtime event and marks chat as verified.
- If the runtime reports missing model/provider configuration, the verifier reports `workspace_chat_unconfigured` instead of hiding the issue.

The commercial resource E2E remains blocking. The chat probe becomes blocking only after the WebUI runtime contract guarantees a provider-independent smoke route or a seeded model configuration in staging.

## Cleanup Rules

The verifier must clean up all resources it creates:

- First attachment is detached before first compute destroy.
- First compute is destroyed.
- Storage is retained through the second compute.
- Second attachment is detached.
- Second compute is destroyed.
- Storage is destroyed last.

If any verification step fails after resource creation, cleanup still runs and reports cleanup errors separately.

## Evidence

Completion requires:

- Local tests covering the verifier request sequence and cleanup on failure.
- Production verifier JSON with checks for:
  - first compute created
  - storage created
  - first attachment created
  - first Workspace URL open
  - runtime file write/read
  - first compute destroyed
  - second compute created
  - second attachment created
  - second Workspace URL open
  - persisted file read after compute replacement
  - ledger/resource usage verified
  - cleanup completed
- Post-run account state showing no active compute, storage, or attachment for the verifier account.
