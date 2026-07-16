# Slide 6 Runtime Owner Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Repository policy forbids subagents for this work.

**Goal:** Ensure ordinary Runtime status never returns a password and only `Workspace.ownerUserId` can reveal or rotate the Runtime password.

**Architecture:** Keep Runtime password authentication for the Pilot. Split Control Plane's status DTO from explicit credential commands, enforce owner-user identity before any Fabric secret read, and mark secret responses `private, no-store`. Rotation reuses Fabric's existing idempotent `CreateWorkspaceRuntime` apply path; a stable credential revision annotation causes Kubernetes to roll the Runtime after Secret data changes.

**Tech Stack:** Go 1.22, `net/http`, existing Control Plane/Fabric clients, Kubernetes manifest JSON, Go tests.

---

## File Map

- Modify `services/control-plane/internal/server/routes_workspace.go`: non-secret status, owner-only reveal, owner-only rotate.
- Modify `services/control-plane/internal/server/server.go`: split safe status and credential response projectors.
- Modify `services/control-plane/internal/controlplane/service.go`: reuse Gateway Secret sync and Runtime create for credential rotation; append reset receipt.
- Modify `services/control-plane/internal/clients/fabric.go`: no new endpoint; reuse `CreateWorkspaceRuntime` and `WorkspaceRuntimeStatus`.
- Modify `services/fabric/internal/fabric/tencent_provider.go`: pod-template credential revision annotation.
- Modify `services/fabric/internal/fabric/tencent_provider_test.go`: stable/revised manifest and password tests.
- Create `services/control-plane/internal/server/runtime_owner_isolation_test.go`: member/owner/tenant/cache/persistence tests.
- Modify shared provider fakes only to keep interfaces compiling.
- Modify `packages/contracts/opl-cloud-business-object-contract.json`, `packages/contracts/opl-cloud-evidence-ledger-contract.json`, `packages/contracts/opl-cloud-launch-freeze-contract.json`, and `docs/invariants.md`.

Do not modify UI files, Workspace launch behavior, billing history, Sub2API usage,
Key CRUD, deployment workflows, production manifests, or add SSO.

### Task 1: Establish the clean baseline

- [ ] **Step 1: Verify isolation and run focused tests**

Run:

```bash
git status --short --branch
go test ./services/control-plane/internal/server ./services/control-plane/internal/controlplane ./services/control-plane/internal/clients ./services/fabric/internal/fabric ./services/fabric/internal/http
```

Expected: branch `fix/slide-6-runtime-owner-isolation`, clean worktree, PASS.

### Task 2: Remove credentials from ordinary Runtime status

- [ ] **Step 1: Write the failing status test**

Create `runtime_owner_isolation_test.go`. Seed an account owner and member, a
Workspace owned by the owner, and a Fabric status containing
`runtime-password-alpha`. Call the existing status route as the member and
assert the response is 200 but contains none of:

```text
runtime-password-alpha password secretRef
```

Also assert the stored Workspace still contains no password.

- [ ] **Step 2: Confirm current leakage**

Run:

```bash
go test ./services/control-plane/internal/server -run TestRuntimeStatusNeverReturnsCredential -count=1
```

Expected: FAIL because `workspaceRuntimeStatusResponse` includes the password.

- [ ] **Step 3: Split the projector**

Replace the secret-bearing helper with two concrete functions:

```go
func workspaceRuntimeStatusResponse(runtime clients.WorkspaceRuntime) map[string]any
func workspaceRuntimeCredentialResponse(runtime clients.WorkspaceRuntime) map[string]any
```

The status response contains provider, Workspace/runtime IDs, URL, service name,
status, ready, checks, username, credential status, and credential version. It
never includes password or Secret reference. The credential response contains
only Workspace ID, username/account, password, credential status, and credential
version.

- [ ] **Step 4: Add cache policy and test**

Set `Cache-Control: private, no-store` before the status response. Run:

```bash
go test ./services/control-plane/internal/server -run TestRuntimeStatusNeverReturnsCredential -count=1
git add services/control-plane/internal/server/routes_workspace.go services/control-plane/internal/server/server.go services/control-plane/internal/server/runtime_owner_isolation_test.go
git commit -m "fix(control-plane): remove password from runtime status"
```

Expected: PASS.

### Task 3: Add owner-only password reveal

- [ ] **Step 1: Write authorization tests**

Test this route:

```text
POST /api/workspaces/{workspaceId}/runtime-credentials/reveal
```

Required assertions:

- account member who is not `ownerUserId`: 403 before Fabric call;
- another account: 404/403 before Fabric call and no existence detail;
- owning user: 200 with password and `private, no-store`;
- response is absent from `/api/state`, Workspace list, persistence, audit, logs,
  and RuntimeOperation results;
- unknown Workspace never reaches Fabric.

- [ ] **Step 2: Implement one shared owner guard**

Add a small helper in `routes_workspace.go`:

```go
func (app *controlPlaneServer) ownedWorkspaceForCredentialCommand(w http.ResponseWriter, r *http.Request, workspaceID string) (map[string]any, bool) {
	workspace, ok := app.getWorkspace(workspaceID)
	user, userOK := app.sessionUserContext(r)
	if !ok || !userOK || !app.canAccessResource(r, workspace) ||
		firstNonEmpty(stringValue(workspace["ownerUserId"]), stringValue(workspace["ownerId"])) != stringValue(user["id"]) {
		writeError(w, http.StatusForbidden, "workspace_owner_required")
		return nil, false
	}
	return workspace, true
}
```

Use it for reveal and later rotation. Do not authorize by account role alone.

- [ ] **Step 3: Implement reveal**

After the owner guard, call `service.WorkspaceRuntimeStatus`, reject a missing or
unready Runtime, set `private, no-store`, and emit only
`workspaceRuntimeCredentialResponse`. Do not save the returned password.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/server -run 'RuntimeCredentialReveal|RuntimeStatusNever' -count=1
git add services/control-plane/internal/server/routes_workspace.go services/control-plane/internal/server/runtime_owner_isolation_test.go
git commit -m "feat(control-plane): restrict runtime credential reveal to owner"
```

Expected: PASS.

### Task 4: Make a credential revision roll the Runtime

- [ ] **Step 1: Write failing Fabric manifest tests**

Call `workspaceManifest` twice with the same credential seed and once with a new
seed. Assert:

- identical seed produces byte-identical Secret and pod template annotation;
- new seed changes `webui_password`, `webui_session_secret`, and exactly one
  pod-template annotation;
- no raw password appears in labels, annotations, operation payload, or logs.

The annotation key is:

```text
opl.medopl.cn/credential-revision
```

- [ ] **Step 2: Confirm the annotation is absent**

Run:

```bash
go test ./services/fabric/internal/fabric -run TestWorkspaceCredentialRevisionRollsRuntime -count=1
```

Expected: FAIL.

- [ ] **Step 3: Add the stable digest annotation**

Compute a non-secret digest from the existing credential seed:

```go
credentialRevision := stableID("workspace-credential", workspaceID, credentialSeed)[:16]
```

Place it in `Deployment.spec.template.metadata.annotations`. Do not put the
password, session secret, or seed itself in metadata.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/fabric/internal/fabric -run 'Workspace.*(Credential|Manifest|Runtime)' -count=1
git add services/fabric/internal/fabric/tencent_provider.go services/fabric/internal/fabric/tencent_provider_test.go
git commit -m "fix(fabric): roll runtime on credential revision"
```

Expected: PASS.

### Task 5: Add owner-only idempotent password rotation

- [ ] **Step 1: Write failing rotation tests**

Test:

```text
POST /api/workspaces/{workspaceId}/runtime-credentials/rotate
Idempotency-Key: rotate-20260716-1
```

Assert non-owner fails before Sub2API/Fabric/Ledger; owner receives a changed
password with `private, no-store`; same key replays the same credential and one
receipt; a new key rotates once; no password is persisted or included in the
receipt.

- [ ] **Step 2: Reuse the existing Fabric apply path**

Add a Control Plane service method that:

1. resolves/writes the account's existing `opl-workspace` Gateway Secret with a
   stable child key;
2. calls existing `FabricClient.CreateWorkspaceRuntime` with the same
   Workspace/compute/storage IDs and a stable rotation child key;
3. calls `WorkspaceRuntimeStatus` to read the now-current credential;
4. appends `workspace.access_token_reset` with credential version, Runtime ID,
   and Secret reference metadata, but no password, Key, or raw provider body.

The route passes account ID, mapped Sub2API user ID, owner user ID, resource IDs,
and top-level idempotency key. It saves only safe Workspace credential metadata.

- [ ] **Step 3: Validate replay**

The existing Fabric Runtime claim is the provider-side idempotency authority.
The Ledger receipt uses `runtime-credential-rotate:<workspaceId>:<key>`. A retry
after the apply but before the response calls status and retries only the
receipt/response path.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/server ./services/control-plane/internal/controlplane ./services/control-plane/internal/clients -run 'RuntimeCredential.*(Rotate|Replay|Owner|NoLeak)' -count=1
git add services/control-plane/internal/server/routes_workspace.go services/control-plane/internal/server/runtime_owner_isolation_test.go services/control-plane/internal/controlplane/service.go services/control-plane/internal/clients/fabric.go services/control-plane/internal/server/server_test.go
git commit -m "feat(control-plane): rotate owner runtime credentials"
```

Expected: PASS.

### Task 6: Update contracts and verify the lane

- [ ] **Step 1: Update contracts accurately**

Record that status is non-secret, reveal/rotate are owner-user-only, responses
are `private, no-store`, and the reset receipt excludes the password. Keep SSO,
identity-bound Runtime requests, browser proof, WebSocket proof, and deployed
rotation evidence pending.

- [ ] **Step 2: Format and test**

Run:

```bash
gofmt -w services/control-plane/internal/server/routes_workspace.go services/control-plane/internal/server/server.go services/control-plane/internal/server/runtime_owner_isolation_test.go services/control-plane/internal/controlplane/service.go services/control-plane/internal/clients/fabric.go services/fabric/internal/fabric/tencent_provider.go services/fabric/internal/fabric/tencent_provider_test.go
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
npm test
git diff --check integration/pilot-launch-b...HEAD
```

Expected: PASS and no UI, launch-operation, customer-facts, or deployment files
in the diff.

- [ ] **Step 3: Commit contracts and rebase**

Run:

```bash
git add docs/invariants.md packages/contracts/opl-cloud-business-object-contract.json packages/contracts/opl-cloud-evidence-ledger-contract.json packages/contracts/opl-cloud-launch-freeze-contract.json
git commit -m "docs: contract owner-only runtime credentials"
git rebase integration/pilot-launch-b
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
```

Expected: PASS. The root integration worktree owns the merge.
